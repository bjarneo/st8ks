package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/openapi"
	"k8s.io/utils/ptr"

	"st8ks/internal/ide"
)

// ideView gives the IDE access to the cluster. It is a value, so two views
// of the same cluster are equal.
type ideView struct{ c *Cluster }

// IdeView returns the cluster as the IDE sees it.
func (c *Cluster) IdeView() ide.Cluster { return ideView{c} }

type ideState struct {
	disc     *discovery.DiscoveryClient
	paths    map[string]openapi.GroupVersion
	pullVer  uint64
	pullAt   time.Time
	pullList []ide.PullFailure
}

func (v ideView) Name() string             { return v.c.Name }
func (v ideView) DefaultNamespace() string { return v.c.defaultNS }

var (
	minorRe = regexp.MustCompile(`^v?1\.(\d+)`)
	// podUID matches the "_namespace(uid)" suffix that the kubelet puts after pod names.
	podUID = regexp.MustCompile(`_[a-z0-9-]+\([0-9a-f-]{36}\)`)
)

func (v ideView) Minor() int {
	m := minorRe.FindStringSubmatch(v.c.State().Version)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func (v ideView) Version() uint64 {
	return v.c.tableVer("Pods")*1_000_003 + v.c.tableVer("ReplicaSets")*1009 + v.c.tableVer("Deployments")
}

func (v ideView) mapping(apiVersion, kind string) (*meta.RESTMapping, error) {
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return nil, err
	}
	return v.c.mapper().RESTMapping(schema.GroupKind{Group: gv.Group, Kind: kind}, gv.Version)
}

func (v ideView) Resource(apiVersion, kind string) (ide.Resource, bool) {
	m, err := v.mapping(apiVersion, kind)
	if err != nil {
		return ide.Resource{}, false
	}
	name := strings.ToLower(kind)
	if m.GroupVersionKind.Group != "" {
		name += "." + m.GroupVersionKind.Group
	}
	return ide.Resource{Name: name, Namespaced: m.Scope.Name() == meta.RESTScopeNameNamespace}, true
}

func (v ideView) OpenAPI(gv string) ([]byte, error) {
	c := v.c
	c.ideMu.Lock()
	if c.ide.disc == nil {
		dc, err := discovery.NewDiscoveryClientForConfig(c.cfg)
		if err != nil {
			c.ideMu.Unlock()
			return nil, err
		}
		c.ide.disc = dc
	}
	dc, paths := c.ide.disc, c.ide.paths
	c.ideMu.Unlock()
	if paths == nil {
		p, err := dc.OpenAPIV3().Paths()
		if err != nil {
			return nil, err
		}
		paths = p
		c.ideMu.Lock()
		c.ide.paths = p
		c.ideMu.Unlock()
	}
	key := "apis/" + gv
	if !strings.Contains(gv, "/") {
		key = "api/" + gv
	}
	p, ok := paths[key]
	if !ok {
		return nil, fmt.Errorf("%s does not serve %s", c.Name, gv)
	}
	return p.Schema(runtime.ContentTypeJSON)
}

// Live reads an object from the watch cache, or from the API server when
// the cache does not have it.
func (v ideView) Live(ref ide.Ref) (map[string]any, error) {
	c := v.c
	if k := c.kindByKind(ref.Kind, ref.APIVersion); k != nil && k.Name != "ConfigMaps" && k.Name != "Secrets" && k.Version == apiVersionOf(ref.APIVersion) {
		ns := ref.NS
		if !k.Namespaced {
			ns = ""
		}
		if o := c.getObj(k.Name, ns, ref.Name); o != nil {
			m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
			if err == nil {
				// Objects in the watch cache have no type fields.
				m["apiVersion"], m["kind"] = ref.APIVersion, ref.Kind
				delete(m, "status")
				return m, nil
			}
		}
	}
	m, err := v.mapping(ref.APIVersion, ref.Kind)
	if err != nil {
		return nil, err
	}
	ri := c.dyn.Resource(m.Resource)
	var get func(context.Context, string, metav1.GetOptions, ...string) (*unstructured.Unstructured, error)
	if m.Scope.Name() == meta.RESTScopeNameNamespace {
		get = ri.Namespace(ref.NS).Get
	} else {
		get = ri.Get
	}
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()
	u, err := get(ctx, ref.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cleanForYAML(u)
	delete(u.Object, "status")
	return u.Object, nil
}

func apiVersionOf(apiVersion string) string {
	if i := strings.Index(apiVersion, "/"); i >= 0 {
		return apiVersion[i+1:]
	}
	return apiVersion
}

// revisions returns the ReplicaSets of a Deployment, newest revision first.
func (c *Cluster) revisions(d *appsv1.Deployment) []*appsv1.ReplicaSet {
	var out []*appsv1.ReplicaSet
	for _, o := range c.byIndex("ReplicaSets", "owner", string(d.UID)) {
		out = append(out, o.(*appsv1.ReplicaSet))
	}
	rev := func(rs *appsv1.ReplicaSet) int64 {
		n, _ := strconv.ParseInt(rs.Annotations["deployment.kubernetes.io/revision"], 10, 64)
		return n
	}
	sort.Slice(out, func(i, j int) bool { return rev(out[i]) > rev(out[j]) })
	return out
}

func (c *Cluster) podsOwnedBy(uid types.UID) []*corev1.Pod {
	var out []*corev1.Pod
	for _, o := range c.byIndex("Pods", "owner", string(uid)) {
		out = append(out, o.(*corev1.Pod))
	}
	return out
}

// workloadPods finds the pod template and the pods of a workload.
func (c *Cluster) workloadPods(kind, ns, name string) (*corev1.PodTemplateSpec, []*corev1.Pod, *appsv1.ReplicaSet, bool) {
	switch kind {
	case "Deployment":
		o, ok := c.getObj("Deployments", ns, name).(*appsv1.Deployment)
		if !ok {
			return nil, nil, nil, false
		}
		var pods []*corev1.Pod
		var prev *appsv1.ReplicaSet
		rss := c.revisions(o)
		for i, rs := range rss {
			pods = append(pods, c.podsOwnedBy(rs.UID)...)
			if i == 1 {
				prev = rs
			}
		}
		return &o.Spec.Template, pods, prev, true
	case "StatefulSet":
		o, ok := c.getObj("StatefulSets", ns, name).(*appsv1.StatefulSet)
		if !ok {
			return nil, nil, nil, false
		}
		return &o.Spec.Template, c.podsOwnedBy(o.UID), nil, true
	case "DaemonSet":
		o, ok := c.getObj("DaemonSets", ns, name).(*appsv1.DaemonSet)
		if !ok {
			return nil, nil, nil, false
		}
		return &o.Spec.Template, c.podsOwnedBy(o.UID), nil, true
	case "ReplicaSet":
		o, ok := c.getObj("ReplicaSets", ns, name).(*appsv1.ReplicaSet)
		if !ok {
			return nil, nil, nil, false
		}
		return &o.Spec.Template, c.podsOwnedBy(o.UID), nil, true
	case "Job":
		o, ok := c.getObj("Jobs", ns, name).(*batchv1.Job)
		if !ok {
			return nil, nil, nil, false
		}
		return &o.Spec.Template, c.podsOwnedBy(o.UID), nil, true
	case "CronJob":
		o, ok := c.getObj("CronJobs", ns, name).(*batchv1.CronJob)
		if !ok {
			return nil, nil, nil, false
		}
		var pods []*corev1.Pod
		for _, j := range c.byIndex("Jobs", "owner", string(o.UID)) {
			pods = append(pods, c.podsOwnedBy(j.(*batchv1.Job).UID)...)
		}
		return &o.Spec.JobTemplate.Spec.Template, pods, nil, true
	case "Pod":
		p, ok := c.getObj("Pods", ns, name).(*corev1.Pod)
		if !ok {
			return nil, nil, nil, false
		}
		return &corev1.PodTemplateSpec{Spec: p.Spec}, []*corev1.Pod{p}, nil, true
	}
	return nil, nil, nil, false
}

var pullReasons = map[string]bool{"ImagePullBackOff": true, "ErrImagePull": true, "InvalidImageName": true, "ErrImageNeverPull": true}

// pullMessage returns the reason of an image pull failure in short form.
func (c *Cluster) pullMessage(p *corev1.Pod, w *corev1.ContainerStateWaiting) string {
	msg := w.Message
	for _, e := range c.warningEvents(string(p.UID), 3) {
		if e.Reason == "Failed" && strings.Contains(e.Message, "pull") {
			msg = e.Message
			break
		}
	}
	if i := strings.LastIndex(msg, "desc = "); i >= 0 {
		msg = msg[i+7:]
	}
	msg = strings.TrimSpace(msg)
	// Registry errors end with the answer of the registry, such as "not
	// found" or "unauthorized".
	if i := strings.LastIndex(msg, ": "); i >= 0 && len(msg)-i < 90 {
		return "The registry answered: " + msg[i+2:]
	}
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	if msg == "" {
		return w.Reason
	}
	return strings.ToUpper(msg[:1]) + msg[1:]
}

func containerImages(t *corev1.PodTemplateSpec) map[string]string {
	out := map[string]string{}
	if t == nil {
		return out
	}
	for _, ct := range append(append([]corev1.Container{}, t.Spec.InitContainers...), t.Spec.Containers...) {
		out[ct.Name] = ct.Image
	}
	return out
}

func (v ideView) Workload(ref ide.Ref) *ide.Workload {
	c := v.c
	tpl, pods, prev, ok := c.workloadPods(ref.Kind, ref.NS, ref.Name)
	if !ok {
		return &ide.Workload{}
	}
	w := &ide.Workload{Found: true, Containers: map[string]*ide.ContainerFacts{}}
	prevImg := map[string]string{}
	prevReady := false
	if prev != nil {
		prevImg = containerImages(&prev.Spec.Template)
		prevReady = prev.Status.ReadyReplicas > 0 || prev.Status.AvailableReplicas > 0
	}
	for _, ct := range append(append([]corev1.Container{}, tpl.Spec.InitContainers...), tpl.Spec.Containers...) {
		f := &ide.ContainerFacts{Image: ct.Image, CrashExit: -1, PrevImage: prevImg[ct.Name], PrevReady: prevReady}
		if q, ok := ct.Resources.Limits[corev1.ResourceMemory]; ok {
			f.MemLimit = q.String()
		}
		w.Containers[ct.Name] = f
	}
	for _, p := range pods {
		if p.DeletionTimestamp != nil {
			continue
		}
		specImg := containerImages(&corev1.PodTemplateSpec{Spec: p.Spec})
		for _, cs := range append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...) {
			f := w.Containers[cs.Name]
			if f == nil {
				continue
			}
			f.Pods++
			if cs.Ready {
				f.Ready++
			}
			if t := cs.LastTerminationState.Terminated; t != nil && t.Reason == "OOMKilled" {
				f.OOMKills += max(cs.RestartCount, 1)
			} else if t := cs.State.Terminated; t != nil && t.Reason == "OOMKilled" {
				f.OOMKills++
			}
			if wt := cs.State.Waiting; wt != nil {
				switch {
				case wt.Reason == "CrashLoopBackOff":
					f.Crash = true
					if t := cs.LastTerminationState.Terminated; t != nil {
						f.CrashExit = t.ExitCode
					}
				case pullReasons[wt.Reason]:
					f.PullImage = specImg[cs.Name]
					f.PullMsg = c.pullMessage(p, wt)
				}
			}
		}
	}
	return w
}

func (v ideView) PullFailures() []ide.PullFailure {
	c := v.c
	ver := c.tableVer("Pods")
	c.ideMu.Lock()
	if c.ide.pullVer == ver && time.Since(c.ide.pullAt) < 2*time.Second {
		l := c.ide.pullList
		c.ideMu.Unlock()
		return l
	}
	c.ideMu.Unlock()
	idx := map[string]int{}
	var out []ide.PullFailure
	for _, o := range c.list("Pods") {
		p := o.(*corev1.Pod)
		if p.DeletionTimestamp != nil {
			continue
		}
		specImg := containerImages(&corev1.PodTemplateSpec{Spec: p.Spec})
		for _, cs := range append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...) {
			wt := cs.State.Waiting
			if wt == nil || !pullReasons[wt.Reason] {
				continue
			}
			ok, on := c.owner(p.Namespace, p.OwnerReferences)
			key := p.Namespace + "/" + specImg[cs.Name] + "/" + ok + "/" + on
			if i, seen := idx[key]; seen {
				out[i].Pods++
				continue
			}
			f := ide.PullFailure{NS: p.Namespace, Image: specImg[cs.Name], Msg: c.pullMessage(p, wt), Pods: 1, Container: cs.Name,
				Owner: ide.Ref{Kind: singular(ok), NS: p.Namespace, Name: on}}
			if ok == "Deployments" {
				if d, isD := c.getObj("Deployments", p.Namespace, on).(*appsv1.Deployment); isD {
					if rss := c.revisions(d); len(rss) > 1 {
						f.PrevImage = containerImages(&rss[1].Spec.Template)[cs.Name]
					}
				}
			}
			idx[key] = len(out)
			out = append(out, f)
		}
	}
	c.ideMu.Lock()
	c.ide.pullVer, c.ide.pullAt, c.ide.pullList = ver, time.Now(), out
	c.ideMu.Unlock()
	return out
}

// managerOf names the GitOps controller or Helm release that owns an object.
func managerOf(labels, ann map[string]string) string {
	switch {
	case ann["argocd.argoproj.io/tracking-id"] != "":
		app, _, _ := strings.Cut(ann["argocd.argoproj.io/tracking-id"], ":")
		return "Argo CD application " + app
	case labels["argocd.argoproj.io/instance"] != "":
		return "Argo CD application " + labels["argocd.argoproj.io/instance"]
	case labels["kustomize.toolkit.fluxcd.io/name"] != "":
		return "Flux Kustomization " + labels["kustomize.toolkit.fluxcd.io/namespace"] + "/" + labels["kustomize.toolkit.fluxcd.io/name"]
	case labels["helm.toolkit.fluxcd.io/name"] != "":
		return "Flux HelmRelease " + labels["helm.toolkit.fluxcd.io/namespace"] + "/" + labels["helm.toolkit.fluxcd.io/name"]
	case ann["meta.helm.sh/release-name"] != "":
		return "Helm release " + ann["meta.helm.sh/release-namespace"] + "/" + ann["meta.helm.sh/release-name"]
	}
	return ""
}

func podTone(st podState) string {
	if st.reason == "Running" && st.ready == st.total {
		return string(tOK)
	}
	if st.reason == "Running" {
		return string(tWarn)
	}
	return string(statusTone(st.reason))
}

func (v ideView) Status(ref ide.Ref) *ide.LiveStatus {
	c := v.c
	k := c.kindByKind(ref.Kind, ref.APIVersion)
	if k == nil {
		return &ide.LiveStatus{}
	}
	ns := ref.NS
	if !k.Namespaced {
		ns = ""
	}
	doc, err := c.GetObject(Ref{Kind: k.Name, NS: ns, Name: ref.Name})
	if err != nil {
		return &ide.LiveStatus{}
	}
	st := &ide.LiveStatus{Found: true, Kind: k.Name, Status: doc.Status, Tone: doc.Tone, Rows: []ide.KV{}, Pods: []ide.PodRow{}}
	for _, kv := range doc.Summary {
		if kv.V == "" || kv.V == "-" || len(st.Rows) >= 6 {
			continue
		}
		st.Rows = append(st.Rows, ide.KV{K: kv.K, V: kv.V, T: kv.T})
	}
	if ns != "" {
		st.Rows = append(st.Rows, ide.KV{K: "Namespace", V: ns})
	}
	labels, ann := map[string]string{}, map[string]string{}
	for _, l := range doc.Labels {
		k, v, _ := strings.Cut(l, "=")
		labels[k] = v
	}
	for _, a := range doc.Annotations {
		ann[a.K] = a.V
	}
	st.Manager = managerOf(labels, ann)
	var newest *corev1.Event
	pick := func(uid types.UID) {
		for _, e := range c.warningEvents(string(uid), 1) {
			if newest == nil || eventTime(e).After(eventTime(newest)) {
				newest = e
			}
		}
	}
	pick(types.UID(doc.UID))
	if tpl, pods, _, ok := c.workloadPods(ref.Kind, ns, ref.Name); ok && tpl != nil && ref.Kind != "Pod" {
		sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
		for _, p := range pods {
			if p.DeletionTimestamp != nil || podTerminal(p) && ref.Kind != "Job" && ref.Kind != "CronJob" {
				continue
			}
			ps := podStatusOf(p)
			s := ps.reason
			if ps.restarts > 0 {
				s += " · " + strconv.FormatInt(ps.restarts, 10) + "↻"
			}
			if len(st.Pods) < 8 {
				st.Pods = append(st.Pods, ide.PodRow{Name: p.Name, Status: s, Tone: podTone(ps)})
			}
			pick(p.UID)
		}
	}
	if newest != nil {
		msg := podUID.ReplaceAllString(strings.TrimSpace(newest.Message), "")
		st.Event = fmt.Sprintf("%s ×%d · %s", newest.Reason, eventCount(newest), msg)
	}
	return st
}

// cleanDiff removes the fields that change on every write, for the apply
// preview.
func cleanDiff(u *unstructured.Unstructured) string {
	c := u.DeepCopy()
	cleanForYAML(c)
	unstructured.RemoveNestedField(c.Object, "status")
	for _, f := range []string{"resourceVersion", "generation", "uid", "creationTimestamp", "managedFields"} {
		unstructured.RemoveNestedField(c.Object, "metadata", f)
	}
	return toYAML(c.Object)
}

func (v ideView) Apply(objs []map[string]any, dry, force bool) []ide.ApplyResult {
	c := v.c
	ctx, cancel := context.WithTimeout(c.ctx, 90*time.Second)
	defer cancel()
	def := c.defaultNS
	if def == "" {
		def = "default"
	}
	out := make([]ide.ApplyResult, 0, len(objs))
	for _, o := range objs {
		u := &unstructured.Unstructured{Object: o}
		r := ide.ApplyResult{Resource: strings.ToLower(u.GetKind()) + "/" + u.GetName(), Verb: "failed"}
		ri, res, err := c.resolve(u, def)
		r.NS = u.GetNamespace()
		r.Ref = ide.Ref{APIVersion: u.GetAPIVersion(), Kind: u.GetKind(), NS: u.GetNamespace(), Name: u.GetName()}
		if err != nil {
			r.Err = err.Error()
			out = append(out, r)
			continue
		}
		r.Resource = res + "/" + u.GetName()
		live, err := ri.Get(ctx, u.GetName(), metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			live = nil
		case err != nil:
			r.Err = errString(err)
			out = append(out, r)
			continue
		default:
			r.Before = cleanDiff(live)
			r.Manager = managerOf(live.GetLabels(), live.GetAnnotations())
		}
		body, err := json.Marshal(u.Object)
		if err != nil {
			r.Err = err.Error()
			out = append(out, r)
			continue
		}
		opts := metav1.PatchOptions{FieldManager: "st8ks", Force: ptr.To(force), DryRun: dryOpts(dry), FieldValidation: "Strict"}
		got, err := ri.Patch(ctx, u.GetName(), types.ApplyPatchType, body, opts)
		if err != nil {
			r.Err = errString(err)
			r.Conflict = apierrors.IsConflict(err)
			out = append(out, r)
			continue
		}
		r.After = cleanDiff(got)
		switch {
		case live == nil:
			r.Verb = "created"
		case r.Before == r.After:
			r.Verb = "unchanged"
		default:
			r.Verb = "configured"
		}
		out = append(out, r)
	}
	return out
}

// Rollout follows a rollout like kubectl rollout status.
func (v ideView) Rollout(ref ide.Ref, log func(text, tone string)) {
	c := v.c
	deadline := time.Now().Add(5 * time.Minute)
	last := ""
	for time.Now().Before(deadline) {
		select {
		case <-c.ctx.Done():
			return
		case <-time.After(time.Second):
		}
		msg, tone, done := c.rolloutStatus(ref)
		if msg != "" && msg != last {
			log(msg, tone)
			last = msg
		}
		if done {
			return
		}
	}
	log(fmt.Sprintf("%s %q did not finish its rollout within 5 minutes", strings.ToLower(ref.Kind), ref.Name), "wa")
}

func (c *Cluster) rolloutStatus(ref ide.Ref) (msg, tone string, done bool) {
	switch ref.Kind {
	case "Deployment":
		d, ok := c.getObj("Deployments", ref.NS, ref.Name).(*appsv1.Deployment)
		if !ok {
			return "", "", true
		}
		if d.Generation > d.Status.ObservedGeneration {
			return "Waiting for the deployment spec update to be observed…", "", false
		}
		for _, cond := range d.Status.Conditions {
			if cond.Type == appsv1.DeploymentProgressing && cond.Reason == "ProgressDeadlineExceeded" {
				return fmt.Sprintf("error: deployment %q exceeded its progress deadline", d.Name), "er", true
			}
		}
		want := ptr.Deref(d.Spec.Replicas, 1)
		s := d.Status
		switch {
		case s.UpdatedReplicas < want:
			return fmt.Sprintf("Waiting for deployment %q rollout to finish: %d of %d new replicas are updated…", d.Name, s.UpdatedReplicas, want), "", false
		case s.Replicas > s.UpdatedReplicas:
			return fmt.Sprintf("Waiting for deployment %q rollout to finish: %d old replicas are pending termination…", d.Name, s.Replicas-s.UpdatedReplicas), "", false
		case s.AvailableReplicas < s.UpdatedReplicas:
			return fmt.Sprintf("Waiting for deployment %q rollout to finish: %d of %d updated replicas are available…", d.Name, s.AvailableReplicas, s.UpdatedReplicas), "", false
		}
		return fmt.Sprintf("deployment %q successfully rolled out", d.Name), "ok", true
	case "StatefulSet":
		s, ok := c.getObj("StatefulSets", ref.NS, ref.Name).(*appsv1.StatefulSet)
		if !ok {
			return "", "", true
		}
		if s.Spec.UpdateStrategy.Type != appsv1.RollingUpdateStatefulSetStrategyType {
			return "", "", true
		}
		if s.Status.ObservedGeneration == 0 || s.Generation > s.Status.ObservedGeneration {
			return "Waiting for the statefulset spec update to be observed…", "", false
		}
		if want := ptr.Deref(s.Spec.Replicas, 1); s.Status.ReadyReplicas < want {
			return fmt.Sprintf("Waiting for statefulset %q: %d of %d pods are ready…", s.Name, s.Status.ReadyReplicas, want), "", false
		}
		if s.Status.UpdateRevision != s.Status.CurrentRevision {
			return fmt.Sprintf("Waiting for statefulset %q rolling update to complete: %d pods at revision %s…", s.Name, s.Status.UpdatedReplicas, s.Status.UpdateRevision), "", false
		}
		return fmt.Sprintf("statefulset %q rolling update complete", s.Name), "ok", true
	case "DaemonSet":
		d, ok := c.getObj("DaemonSets", ref.NS, ref.Name).(*appsv1.DaemonSet)
		if !ok {
			return "", "", true
		}
		if d.Generation > d.Status.ObservedGeneration {
			return "Waiting for the daemon set spec update to be observed…", "", false
		}
		s := d.Status
		switch {
		case s.UpdatedNumberScheduled < s.DesiredNumberScheduled:
			return fmt.Sprintf("Waiting for daemon set %q rollout to finish: %d of %d new pods are updated…", d.Name, s.UpdatedNumberScheduled, s.DesiredNumberScheduled), "", false
		case s.NumberAvailable < s.DesiredNumberScheduled:
			return fmt.Sprintf("Waiting for daemon set %q rollout to finish: %d of %d updated pods are available…", d.Name, s.NumberAvailable, s.DesiredNumberScheduled), "", false
		}
		return fmt.Sprintf("daemon set %q successfully rolled out", d.Name), "ok", true
	}
	return "", "", true
}
