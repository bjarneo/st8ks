package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiext "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/metadata/metadatainformer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
)

const lastApplied = "kubectl.kubernetes.io/last-applied-configuration"

type source struct {
	kind *Kind
	inf  cache.SharedIndexInformer
	stop chan struct{}
	meta bool
	ns   string
	note string
	once sync.Once
}

func (s *source) close() { s.once.Do(func() { close(s.stop) }) }

// Cluster is a live connection to one kubeconfig context.
type Cluster struct {
	Name      string
	emitFn    Emitter
	cfg       *rest.Config
	pcfg      *rest.Config
	cs        kubernetes.Interface
	dyn       dynamic.Interface
	meta      metadata.Interface
	ext       apiext.Interface
	mc        metricsv.Interface
	raw       clientcmdapi.Config
	ctxOrig   string
	defaultNS string

	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.RWMutex
	kinds   map[string]*Kind
	builtin []*Kind
	custom  []*Kind
	sources map[string]*source
	tables  map[string]*table
	watched map[string]bool
	avail   map[schema.GroupVersionResource]bool
	state   ClusterState

	podMu     sync.Mutex
	podNode   map[string]string
	nodePods  map[string]int
	nodeDirty map[string]struct{}

	crdDirty   atomic.Int64
	probeNow   chan struct{}
	mapperOnce sync.Once
	restMapper meta.RESTMapper
	met        metricsState
	derived    derivedState
}

func newCluster(name, orig string, raw clientcmdapi.Config, cfg *rest.Config, defaultNS string, emit Emitter) (*Cluster, error) {
	cfg = rest.CopyConfig(cfg)
	cfg.QPS = 100
	cfg.Burst = 200
	cfg.UserAgent = "st8ks"
	cfg.WarningHandler = rest.NoWarnings{}

	pcfg := rest.CopyConfig(cfg)
	pcfg.ContentType = "application/vnd.kubernetes.protobuf"
	pcfg.AcceptContentTypes = "application/vnd.kubernetes.protobuf,application/json"

	cs, err := kubernetes.NewForConfig(pcfg)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	md, err := metadata.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	ext, err := apiext.NewForConfig(pcfg)
	if err != nil {
		return nil, err
	}
	mc, err := metricsv.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &Cluster{
		Name: name, emitFn: emit, cfg: cfg, pcfg: pcfg, cs: cs, dyn: dyn, meta: md, ext: ext, mc: mc, raw: raw, ctxOrig: orig,
		defaultNS: defaultNS, ctx: ctx, cancel: cancel,
		kinds: map[string]*Kind{}, sources: map[string]*source{}, tables: map[string]*table{},
		watched: map[string]bool{}, avail: map[schema.GroupVersionResource]bool{},
		podNode: map[string]string{}, nodePods: map[string]int{}, nodeDirty: map[string]struct{}{},
		probeNow: make(chan struct{}, 1),
		state:    ClusterState{Context: name, Status: StatusConnecting, Server: cfg.Host},
	}
	return c, nil
}

func (c *Cluster) emit(name string, data any) {
	if c.ctx.Err() != nil {
		return
	}
	c.emitFn(name, data)
}

// State returns the connection state.
func (c *Cluster) State() ClusterState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

func (c *Cluster) setState(fn func(s *ClusterState)) {
	c.mu.Lock()
	fn(&c.state)
	s := c.state
	c.mu.Unlock()
	c.emit("cluster", s)
}

// Stop ends all watches and loops.
func (c *Cluster) Stop() {
	c.cancel()
	c.mu.Lock()
	for _, s := range c.sources {
		s.close()
	}
	c.mu.Unlock()
}

func (c *Cluster) serverVersion(ctx context.Context) (*version.Info, error) {
	body, err := c.cs.Discovery().RESTClient().Get().AbsPath("/version").Do(ctx).Raw()
	if err != nil {
		return nil, err
	}
	var info version.Info
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// Start checks the API server and starts the watches.
func (c *Cluster) Start() error {
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	info, err := c.serverVersion(ctx)
	cancel()
	if err != nil {
		msg := friendlyErr(err)
		c.setState(func(s *ClusterState) { s.Status = StatusUnreachable; s.Error = msg })
		return errors.New(msg)
	}
	dist := distFromVersion(info.GitVersion)
	c.setState(func(s *ClusterState) {
		s.Version = info.GitVersion
		s.Dist = dist
	})

	_, lists, err := c.cs.Discovery().ServerGroupsAndResources()
	if err != nil && !discovery.IsGroupDiscoveryFailedError(err) && len(lists) == 0 {
		msg := friendlyErr(err)
		c.setState(func(s *ClusterState) { s.Status = StatusUnreachable; s.Error = msg })
		return errors.New(msg)
	}
	c.mu.Lock()
	for _, l := range lists {
		gv, err := schema.ParseGroupVersion(l.GroupVersion)
		if err != nil {
			continue
		}
		for _, r := range l.APIResources {
			if !strings.Contains(r.Name, "/") {
				c.avail[gv.WithResource(r.Name)] = true
			}
		}
	}
	for _, k := range builtinKinds() {
		if k.Label == "" {
			k.Label = kindLabel(k.Name)
		}
		if k.Local || c.avail[k.gvr()] {
			c.builtin = append(c.builtin, k)
			c.kinds[k.Name] = k
			c.tables[k.Name] = newTable(k)
		}
	}
	metricsOK := c.avail[schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}]
	openshift := c.avail[schema.GroupVersionResource{Group: "config.openshift.io", Version: "v1", Resource: "clusterversions"}]
	c.mu.Unlock()

	if openshift {
		dist = "OpenShift"
	}
	c.setState(func(s *ClusterState) {
		s.Status = StatusConnected
		s.Error = ""
		s.Metrics = metricsOK
		s.Dist = dist
	})

	for _, k := range c.builtin {
		switch {
		case k.Local:
			c.loadHelmRepos()
		case k.Lazy:
			c.startSource(k, true, "")
		default:
			c.startSource(k, false, "")
		}
	}
	go c.flushLoop()
	go c.healthLoop()
	go c.derivedLoop()
	if metricsOK {
		go c.metricsLoop()
	}
	return nil
}

func friendlyErr(err error) string {
	msg := err.Error()
	if len(msg) > 400 {
		msg = msg[:400] + "…"
	}
	return msg
}

func (c *Cluster) indexers(k *Kind) cache.Indexers {
	ix := cache.Indexers{"owner": ownerIndex}
	if k.Namespaced {
		ix[cache.NamespaceIndex] = cache.MetaNamespaceIndexFunc
	}
	switch k.Name {
	case "Pods":
		ix["node"] = func(obj any) ([]string, error) {
			if p, ok := obj.(*corev1.Pod); ok && p.Spec.NodeName != "" {
				return []string{p.Spec.NodeName}, nil
			}
			return nil, nil
		}
	case "Events":
		ix["involved"] = func(obj any) ([]string, error) {
			if e, ok := obj.(*corev1.Event); ok && e.InvolvedObject.UID != "" {
				return []string{string(e.InvolvedObject.UID)}, nil
			}
			return nil, nil
		}
	}
	return ix
}

func ownerIndex(obj any) ([]string, error) {
	m, err := meta.Accessor(obj)
	if err != nil {
		return nil, nil
	}
	refs := m.GetOwnerReferences()
	if len(refs) == 0 {
		return nil, nil
	}
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = string(r.UID)
	}
	return out, nil
}

func (c *Cluster) transform(k *Kind) cache.TransformFunc {
	return func(obj any) (any, error) {
		if m, err := meta.Accessor(obj); err == nil {
			m.SetManagedFields(nil)
			if a := m.GetAnnotations(); a != nil {
				delete(a, lastApplied)
			}
		}
		if k.strip != nil {
			k.strip(obj)
		}
		return obj, nil
	}
}

// startSource starts an informer for a kind. metaOnly watches only object
// metadata, which is cheap and enough for the counts in the tree.
func (c *Cluster) startSource(k *Kind, metaOnly bool, ns string) {
	c.startSourceIf(k, metaOnly, ns, nil, "")
}

// startSourceIf starts a source. When expect is set, the new source
// replaces only that source, so a late fallback never replaces a newer one.
func (c *Cluster) startSourceIf(k *Kind, metaOnly bool, ns string, expect *source, note string) {
	if c.ctx.Err() != nil {
		return
	}
	var inf cache.SharedIndexInformer
	rowFn := k.row
	switch {
	case metaOnly:
		inf = metadatainformer.NewFilteredMetadataInformer(c.meta, k.gvr(), ns, 0, cache.Indexers{}, nil).Informer()
		rowFn = metaRow(k)
	case k.Custom:
		inf = dynamicinformer.NewFilteredDynamicInformer(c.dyn, k.gvr(), ns, 0, c.indexers(k), nil).Informer()
	default:
		lw := cache.NewListWatchFromClient(k.client(c), k.Resource, ns, fields.Everything())
		inf = cache.NewSharedIndexInformer(lw, k.newObj(), 0, c.indexers(k))
	}
	s := &source{kind: k, inf: inf, stop: make(chan struct{}), meta: metaOnly, ns: ns, note: note}

	c.mu.Lock()
	// Stop cancels the context before it takes the lock, so a source that
	// registers after Stop sees the canceled context here.
	if c.ctx.Err() != nil || (expect != nil && c.sources[k.Name] != expect) {
		c.mu.Unlock()
		return
	}
	t := c.tables[k.Name]
	if t == nil {
		t = newTable(k)
		t.watched = c.watched[k.Name]
		c.tables[k.Name] = t
	}
	if old := c.sources[k.Name]; old != nil {
		old.close()
	}
	c.sources[k.Name] = s
	c.mu.Unlock()

	_ = inf.SetTransform(c.transform(k))
	_ = inf.SetWatchErrorHandler(func(_ *cache.Reflector, err error) { c.onWatchError(s, t, err) })
	_, _ = inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			t.upsert(rowFn(c, obj))
			c.hook(k, obj, false)
		},
		UpdateFunc: func(_, obj any) {
			t.upsert(rowFn(c, obj))
			c.hook(k, obj, false)
		},
		DeleteFunc: func(obj any) {
			if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = d.Obj
			}
			m, err := meta.Accessor(obj)
			if err != nil {
				return
			}
			t.remove(string(m.GetUID()))
			c.hook(k, obj, true)
		},
	})
	t.setSynced(false)
	go inf.Run(s.stop)
	go func() {
		if !cache.WaitForCacheSync(s.stop, inf.HasSynced) || !c.isCurrent(s) {
			return
		}
		// Drop rows that a previous source left behind.
		keep := map[string]struct{}{}
		for _, obj := range inf.GetStore().List() {
			if m, err := meta.Accessor(obj); err == nil {
				keep[string(m.GetUID())] = struct{}{}
			}
		}
		t.retain(keep)
		t.setErr(s.note)
		t.setSynced(true)
		if k.Name == "Nodes" {
			c.updateNodeMeta()
		}
	}()
}

func (c *Cluster) isCurrent(s *source) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sources[s.kind.Name] == s
}

func (c *Cluster) onWatchError(s *source, t *table, err error) {
	k := s.kind
	if !c.isCurrent(s) {
		return
	}
	switch {
	case apierrors.IsForbidden(err):
		msg := "Your credentials cannot list " + k.Label + " in all namespaces."
		if s.ns != "" {
			msg = "Your credentials cannot list " + k.Label + " in namespace " + s.ns + "."
		}
		s.close()
		if k.Namespaced && s.ns == "" && c.defaultNS != "" {
			note := msg + " Showing namespace " + c.defaultNS + " only."
			t.setErr(note)
			go c.startSourceIf(k, s.meta, c.defaultNS, s, note)
			return
		}
		t.setErr(msg)
		t.setSynced(true)
	case apierrors.IsNotFound(err) || meta.IsNoMatchError(err):
		s.close()
		t.setErr(k.Label + " are not served by this cluster.")
		t.setSynced(true)
	default:
		select {
		case c.probeNow <- struct{}{}:
		default:
		}
	}
}

// hook updates derived state when some kinds change.
func (c *Cluster) hook(k *Kind, obj any, deleted bool) {
	switch k.Name {
	case "Pods":
		if p, ok := obj.(*corev1.Pod); ok {
			c.podChanged(p, deleted)
		}
	case "CustomResourceDefinitions":
		c.crdDirty.Store(time.Now().UnixMilli())
	}
}

func (c *Cluster) podChanged(p *corev1.Pod, deleted bool) {
	uid := string(p.UID)
	node := p.Spec.NodeName
	if deleted || podTerminal(p) {
		node = ""
	}
	c.podMu.Lock()
	defer c.podMu.Unlock()
	old := c.podNode[uid]
	if old == node {
		return
	}
	if old != "" {
		if c.nodePods[old]--; c.nodePods[old] <= 0 {
			delete(c.nodePods, old)
		}
		c.nodeDirty[old] = struct{}{}
	}
	if node != "" {
		c.nodePods[node]++
		c.podNode[uid] = node
		c.nodeDirty[node] = struct{}{}
	} else {
		delete(c.podNode, uid)
	}
}

func (c *Cluster) podCount(node string) int {
	c.podMu.Lock()
	defer c.podMu.Unlock()
	return c.nodePods[node]
}

// Watch sets the kinds whose row changes the frontend receives. It starts
// full watches for lazy and custom kinds on first use.
func (c *Cluster) Watch(kinds []string) {
	c.mu.Lock()
	c.watched = map[string]bool{}
	for _, name := range kinds {
		c.watched[name] = true
	}
	for name, t := range c.tables {
		t.setWatched(c.watched[name])
	}
	var start []*Kind
	for _, name := range kinds {
		k := c.kinds[name]
		if k == nil || k.Local {
			continue
		}
		s := c.sources[name]
		if s == nil || s.meta {
			start = append(start, k)
		}
	}
	c.mu.Unlock()
	for _, k := range start {
		c.startSource(k, false, "")
	}
}

// Ensure starts a full watch for a kind if it does not run yet.
func (c *Cluster) Ensure(name string) {
	c.mu.RLock()
	k := c.kinds[name]
	s := c.sources[name]
	c.mu.RUnlock()
	if k != nil && !k.Local && (s == nil || s.meta) {
		c.startSource(k, false, "")
	}
}

// Snapshot returns the full table for a kind.
func (c *Cluster) Snapshot(name string) Snapshot {
	c.mu.RLock()
	t := c.tables[name]
	c.mu.RUnlock()
	if t == nil {
		return Snapshot{Kind: name, Synced: true, Available: false, Rows: []*Row{}}
	}
	s := t.snapshot()
	switch name {
	case "Pods":
		s.Metrics = c.met.podsSnapshot()
	case "Nodes":
		s.Metrics = c.met.nodesSnapshot()
	}
	return s
}

// Counts returns total and problem counts per kind and namespace.
func (c *Cluster) Counts() map[string]map[string][2]int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := map[string]map[string][2]int{}
	for name, t := range c.tables {
		m, _ := t.takeCounts(true)
		out[name] = m
	}
	return out
}

func (c *Cluster) table(name string) *table {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tables[name]
}

func (c *Cluster) indexer(name string) cache.Indexer {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s := c.sources[name]
	if s == nil || s.meta {
		return nil
	}
	return s.inf.GetIndexer()
}

func (c *Cluster) list(name string) []any {
	ix := c.indexer(name)
	if ix == nil {
		return nil
	}
	return ix.List()
}

func (c *Cluster) byIndex(name, index, key string) []any {
	ix := c.indexer(name)
	if ix == nil {
		return nil
	}
	out, _ := ix.ByIndex(index, key)
	return out
}

func (c *Cluster) getObj(name, ns, obj string) any {
	ix := c.indexer(name)
	if ix == nil {
		return nil
	}
	key := obj
	if ns != "" {
		key = ns + "/" + obj
	}
	o, ok, _ := ix.GetByKey(key)
	if !ok {
		return nil
	}
	return o
}

func (c *Cluster) kind(name string) *Kind {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.kinds[name]
}

// kindByKind finds a kind by its singular name and optional API group.
func (c *Cluster) kindByKind(kind, apiVersion string) *Kind {
	group := ""
	if i := strings.Index(apiVersion, "/"); i >= 0 {
		group = apiVersion[:i]
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, k := range c.kinds {
		if k.Kind == kind && (apiVersion == "" || k.Group == group) && !k.Local {
			return k
		}
	}
	return nil
}

// Kinds describes all kinds that the cluster serves.
func (c *Cluster) Kinds() []KindInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]KindInfo, 0, len(c.kinds))
	for _, k := range c.builtin {
		out = append(out, k.info())
	}
	for _, k := range c.custom {
		out = append(out, k.info())
	}
	return out
}

var sectionOrder = []struct{ id, label string }{
	{"cluster", "Cluster"}, {"workloads", "Workloads"}, {"network", "Network"}, {"config", "Config"},
	{"storage", "Storage"}, {"access", "Access Control"}, {"crd", "Custom Resources"}, {"helm", "Helm"}, {"views", "Views"},
}

// Tree builds the navigation tree.
func (c *Cluster) Tree() []TreeSection {
	c.mu.RLock()
	defer c.mu.RUnlock()
	items := map[string][]TreeItem{}
	items["cluster"] = []TreeItem{{Label: "Overview", View: "overview"}}
	for _, k := range c.builtin {
		if k.Section == "" {
			continue
		}
		items[k.Section] = append(items[k.Section], TreeItem{Label: k.Label, Kind: k.Name})
	}
	items["cluster"] = append(items["cluster"], TreeItem{Label: "Events", View: "events"})
	items["access"] = append(items["access"], TreeItem{Label: "RBAC Explorer", View: "rbac"})
	for _, k := range c.custom {
		items["crd"] = append(items["crd"], TreeItem{Label: k.Label, Kind: k.Name})
	}
	items["helm"] = append([]TreeItem{{Label: "Releases", View: "helm"}}, items["helm"]...)
	items["views"] = []TreeItem{{Label: "Topology", View: "topology"}, {Label: "Clusters", View: "clusters"}}
	out := make([]TreeSection, 0, len(sectionOrder))
	for _, s := range sectionOrder {
		if len(items[s.id]) > 0 {
			out = append(out, TreeSection{ID: s.id, Label: s.label, Items: items[s.id]})
		}
	}
	return out
}

func (c *Cluster) rebuildCustomKinds() {
	var crds []*apiextv1.CustomResourceDefinition
	for _, o := range c.list("CustomResourceDefinitions") {
		if crd, ok := o.(*apiextv1.CustomResourceDefinition); ok {
			for _, cond := range crd.Status.Conditions {
				if cond.Type == apiextv1.Established && cond.Status == apiextv1.ConditionTrue {
					crds = append(crds, crd)
					break
				}
			}
		}
	}
	kinds := customKinds(crds)
	c.mu.Lock()
	next := map[string]bool{}
	for _, k := range kinds {
		next[k.Name] = true
		if old := c.kinds[k.Name]; old != nil && old.Custom {
			// Keep the running source when the printer did not change.
			if old.sig == k.sig {
				continue
			}
			if s := c.sources[k.Name]; s != nil {
				s.close()
				delete(c.sources, k.Name)
			}
			if t := c.tables[k.Name]; t != nil {
				t.replaceKind(k)
			}
		}
		c.kinds[k.Name] = k
	}
	var stale []*Kind
	for name, k := range c.kinds {
		if k.Custom && !next[name] {
			stale = append(stale, k)
		}
	}
	for _, k := range stale {
		if s := c.sources[k.Name]; s != nil {
			s.close()
			delete(c.sources, k.Name)
		}
		delete(c.tables, k.Name)
		delete(c.kinds, k.Name)
	}
	c.custom = c.custom[:0]
	for _, k := range kinds {
		c.custom = append(c.custom, c.kinds[k.Name])
	}
	watched := make([]string, 0)
	for name := range c.watched {
		watched = append(watched, name)
	}
	c.mu.Unlock()
	// New CRDs add resources that the cached REST mapper does not know.
	if m, ok := c.mapper().(meta.ResettableRESTMapper); ok {
		m.Reset()
	}
	// Certificates are part of the overview health, so watch them early.
	for _, k := range kinds {
		if k.Name == "certificates.cert-manager.io" {
			c.Ensure(k.Name)
		}
	}
	for _, w := range watched {
		c.Ensure(w)
	}
	c.emit("tree", map[string]any{"tree": c.Tree(), "kinds": c.Kinds()})
}

// flushLoop sends row changes and counts to the frontend.
func (c *Cluster) flushLoop() {
	tk := time.NewTicker(100 * time.Millisecond)
	defer tk.Stop()
	tick := 0
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-tk.C:
		}
		tick++
		c.flushNodes()
		if at := c.crdDirty.Load(); at != 0 && time.Now().UnixMilli()-at > 400 {
			c.crdDirty.Store(0)
			c.rebuildCustomKinds()
		}
		c.mu.RLock()
		tables := make([]*table, 0, len(c.tables))
		for _, t := range c.tables {
			tables = append(tables, t)
		}
		c.mu.RUnlock()
		for _, t := range tables {
			if d := t.takeDelta(); d != nil {
				c.emit("rows", d)
			}
		}
		if tick%4 == 0 {
			counts := map[string]map[string][2]int{}
			for _, t := range tables {
				if m, ok := t.takeCounts(false); ok {
					counts[t.name] = m
				}
			}
			if len(counts) > 0 {
				c.emit("counts", counts)
			}
		}
	}
}

// flushNodes recomputes node rows whose pod count changed.
func (c *Cluster) flushNodes() {
	c.podMu.Lock()
	if len(c.nodeDirty) == 0 {
		c.podMu.Unlock()
		return
	}
	names := make([]string, 0, len(c.nodeDirty))
	for n := range c.nodeDirty {
		names = append(names, n)
	}
	clear(c.nodeDirty)
	c.podMu.Unlock()
	t := c.table("Nodes")
	if t == nil {
		return
	}
	for _, n := range names {
		if obj := c.getObj("Nodes", "", n); obj != nil {
			t.upsert(nodeRow(c, obj))
		}
	}
}

// healthLoop checks the API server every 10 seconds. After a failure it
// checks again after 2 seconds. Two failures in a row mark the cluster as
// unreachable, so the user never acts on stale data, and one slow answer
// does not cause a false alarm.
func (c *Cluster) healthLoop() {
	fails := 0
	next := 10 * time.Second
	timer := time.NewTimer(next)
	defer timer.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-timer.C:
		case <-c.probeNow:
			// A watch failed. Wait a moment and check the server.
			select {
			case <-c.ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
		ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
		_, err := c.serverVersion(ctx)
		cancel()
		if c.ctx.Err() != nil {
			return
		}
		next = 10 * time.Second
		if err != nil {
			fails++
			next = 2 * time.Second
			if fails >= 2 && c.State().Status != StatusUnreachable {
				msg := friendlyErr(err)
				c.setState(func(s *ClusterState) { s.Status = StatusUnreachable; s.Error = msg })
			}
		} else {
			if c.State().Status == StatusUnreachable {
				c.setState(func(s *ClusterState) { s.Status = StatusConnected; s.Error = "" })
			}
			fails = 0
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(next)
	}
}

// Probe checks the server now and returns the state.
func (c *Cluster) Probe() ClusterState {
	ctx, cancel := context.WithTimeout(c.ctx, 8*time.Second)
	_, err := c.serverVersion(ctx)
	cancel()
	if err != nil {
		msg := friendlyErr(err)
		c.setState(func(s *ClusterState) { s.Status = StatusUnreachable; s.Error = msg })
	} else {
		c.setState(func(s *ClusterState) { s.Status = StatusConnected; s.Error = "" })
	}
	return c.State()
}

// updateNodeMeta reads the distribution and region from the nodes.
func (c *Cluster) updateNodeMeta() {
	var region, provider string
	nodes := c.list("Nodes")
	for _, o := range nodes {
		n := o.(*corev1.Node)
		if region == "" {
			region = n.Labels["topology.kubernetes.io/region"]
		}
		if provider == "" {
			provider = n.Spec.ProviderID
			if _, ok := n.Labels["minikube.k8s.io/name"]; ok {
				provider = "minikube://"
			}
			if n.Name == "docker-desktop" {
				provider = "docker-desktop://"
			}
		}
	}
	c.setState(func(s *ClusterState) {
		s.Dist = distFromProvider(s.Dist, provider)
		s.Region = region
		if region == "" && isLocalDist(s.Dist) {
			s.Region = "local"
		}
	})
}

func distFromVersion(v string) string {
	switch {
	case strings.Contains(v, "-eks-"):
		return "EKS"
	case strings.Contains(v, "-gke."):
		return "GKE"
	case strings.Contains(v, "+k3s"):
		return "k3s"
	case strings.Contains(v, "+rke2"):
		return "RKE2"
	case strings.Contains(v, "+k0s"):
		return "k0s"
	}
	return "Kubernetes"
}

func distFromProvider(cur, provider string) string {
	if cur != "Kubernetes" {
		return cur
	}
	switch {
	case strings.HasPrefix(provider, "kind://"):
		return "kind"
	case strings.HasPrefix(provider, "aws://"):
		return "EKS"
	case strings.HasPrefix(provider, "gce://"):
		return "GKE"
	case strings.HasPrefix(provider, "azure://"):
		return "AKS"
	case strings.HasPrefix(provider, "digitalocean://"):
		return "DOKS"
	case strings.HasPrefix(provider, "minikube://"):
		return "minikube"
	case strings.HasPrefix(provider, "docker-desktop://"):
		return "Docker Desktop"
	}
	return cur
}

func isLocalDist(d string) bool {
	return d == "kind" || d == "minikube" || d == "Docker Desktop" || d == "k3s"
}

// objMeta returns the metadata of a typed or unstructured object.
func objMeta(obj any) metav1.Object {
	m, err := meta.Accessor(obj)
	if err != nil {
		return nil
	}
	return m
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	var st apierrors.APIStatus
	if errors.As(err, &st) {
		return st.Status().Message
	}
	return err.Error()
}

func wrapf(err error, format string, a ...any) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf(format+": %s", append(a, errString(err))...)
}
