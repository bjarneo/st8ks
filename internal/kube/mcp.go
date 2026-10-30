package kube

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// KubeconfigContext returns the name of the context in its kubeconfig file.
// It differs from Name when st8ks added a suffix to keep two contexts apart.
// WriteKubeconfig writes the context under this name.
func (c *Cluster) KubeconfigContext() string { return c.ctxOrig }

// pending lists the kinds whose watch has not listed its objects yet.
func (c *Cluster) pending() []string {
	c.mu.RLock()
	tables := make([]*table, 0, len(c.sources))
	for name := range c.sources {
		if t := c.tables[name]; t != nil {
			tables = append(tables, t)
		}
	}
	c.mu.RUnlock()
	var out []string
	for _, t := range tables {
		t.mu.Lock()
		if !t.synced {
			out = append(out, t.name)
		}
		t.mu.Unlock()
	}
	sort.Strings(out)
	return out
}

// WaitSynced waits until every watch has listed its objects. Then it
// computes the issues and the overview, so that a reader that starts now
// sees them. When ctx ends first, it returns the kinds that did not sync.
func (c *Cluster) WaitSynced(ctx context.Context) []string {
	tk := time.NewTicker(100 * time.Millisecond)
	defer tk.Stop()
	for {
		p := c.pending()
		if len(p) == 0 {
			break
		}
		select {
		case <-ctx.Done():
			return p
		case <-c.ctx.Done():
			return p
		case <-tk.C:
		}
	}
	d := &c.derived
	d.mu.Lock()
	d.issueKey, d.ovKey = 0, [3]uint64{}
	d.mu.Unlock()
	c.refreshDerived()
	return nil
}

// ResolveKind finds a kind by a name that kubectl or the st8ks tree uses:
// the table name such as Pods or certificates.cert-manager.io, the kind such
// as Pod, the resource such as pods or deployments.apps, or the short name
// such as po or deploy. The match ignores case. Built-in kinds come first.
func (c *Cluster) ResolveKind(name string) (KindInfo, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return KindInfo{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	all := append(append([]*Kind{}, c.builtin...), c.custom...)
	for _, k := range all {
		if !k.Local && strings.ToLower(k.Name) == n {
			return k.info(), true
		}
	}
	for _, k := range all {
		if k.Local {
			continue
		}
		kind := strings.ToLower(k.Kind)
		names := []string{kind, k.Resource, k.Short}
		if k.Group != "" {
			names = append(names, k.Resource+"."+k.Group, kind+"."+k.Group)
		}
		for _, s := range names {
			if s != "" && s == n {
				return k.info(), true
			}
		}
	}
	return KindInfo{}, false
}

// TableView is a resource table as text cells.
type TableView struct {
	Kind      string     `json:"kind"`
	Columns   []string   `json:"columns"`
	Rows      [][]string `json:"rows"`
	Total     int        `json:"total"`
	Truncated bool       `json:"truncated,omitempty"`
	Note      string     `json:"note,omitempty"`
}

// TableText returns the rows of a kind with the columns of the list view.
// It starts a full watch for a kind that st8ks watches only on demand, and
// waits for its first list. The filter matches the name, the namespace,
// the cells and the labels, and ignores case. limit caps the rows.
func (c *Cluster) TableText(ctx context.Context, name, ns, filter string, limit int) (TableView, error) {
	k := c.kind(name)
	if k == nil || k.Local {
		return TableView{}, fmt.Errorf("%s is not a cluster resource", name)
	}
	c.mu.RLock()
	s := c.sources[name]
	c.mu.RUnlock()
	if s == nil || s.meta {
		c.Ensure(name)
	}
	wait, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for {
		t := c.table(name)
		if t == nil {
			return TableView{}, fmt.Errorf("%s is not served by this cluster", name)
		}
		t.mu.Lock()
		ok := t.synced
		t.mu.Unlock()
		if ok {
			break
		}
		select {
		case <-wait.Done():
			return TableView{}, fmt.Errorf("the list of %s did not load in 20 seconds", k.Label)
		case <-time.After(100 * time.Millisecond):
		}
	}
	snap := c.Snapshot(name)
	q := strings.ToLower(strings.TrimSpace(filter))
	rows := make([]*Row, 0, len(snap.Rows))
	for _, r := range snap.Rows {
		if ns != "" && k.Namespaced && r.N != ns {
			continue
		}
		if q != "" && !rowMatches(r, q) {
			continue
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].N != rows[j].N {
			return rows[i].N < rows[j].N
		}
		return rows[i].M < rows[j].M
	})
	out := TableView{Kind: k.Name, Columns: make([]string, len(k.Cols)), Rows: [][]string{}, Total: len(rows), Note: snap.Err}
	for i, col := range k.Cols {
		out.Columns[i] = col.L
	}
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
		out.Truncated = true
	}
	for _, r := range rows {
		out.Rows = append(out.Rows, rowCells(k, r, snap.Metrics))
	}
	return out, nil
}

func rowMatches(r *Row, q string) bool {
	if strings.Contains(strings.ToLower(r.M), q) || strings.Contains(strings.ToLower(r.N), q) || strings.Contains(strings.ToLower(r.L), q) {
		return true
	}
	for _, cell := range r.C {
		if strings.Contains(strings.ToLower(cell), q) {
			return true
		}
	}
	return false
}

// rowCells formats a row the way the list view shows it.
func rowCells(k *Kind, r *Row, metrics map[string][2]int64) []string {
	out := make([]string, len(k.Cols))
	use, hasUse := metrics[r.U]
	limit := func(i int) int64 {
		if i < len(r.X) {
			return r.X[i]
		}
		return 0
	}
	for i, col := range k.Cols {
		v := ""
		if i < len(r.C) {
			v = r.C[i]
		}
		switch col.T {
		case CName:
			v = r.M
		case CNs:
			v = r.N
		case CAge:
			v = ago(time.Unix(r.T, 0))
		case CTime:
			if sec, err := strconv.ParseInt(v, 10, 64); err == nil && sec > 0 {
				v = ago(time.Unix(sec, 0)) + " ago"
			}
		case CCPU, CMem, CCPUPct, CMemPct:
			v = usageCell(col.T, use, hasUse, limit(0), limit(1))
		}
		if v == "" {
			v = "—"
		}
		out[i] = v
	}
	return out
}

func usageCell(typ string, use [2]int64, ok bool, cpuCap, memCap int64) string {
	if !ok {
		return "—"
	}
	pct := func(a, b int64) string {
		if b <= 0 {
			return ""
		}
		return " (" + strconv.FormatInt(a*100/b, 10) + "%)"
	}
	switch typ {
	case CCPU, CCPUPct:
		return fmtCPU(use[0]) + pct(use[0], cpuCap)
	default:
		return fmtBytes(use[1]) + pct(use[1], memCap)
	}
}

// TailLogs returns the last lines of a container log. An empty container
// selects the default container, as kubectl does. It returns the container
// that it read.
func (c *Cluster) TailLogs(ns, pod, container string, previous bool, lines int64) (string, string, error) {
	if container == "" {
		if o, ok := c.getObj("Pods", ns, pod).(*corev1.Pod); ok {
			container = o.Annotations["kubectl.kubernetes.io/default-container"]
			if container == "" && len(o.Spec.Containers) > 0 {
				container = o.Spec.Containers[0].Name
			}
		}
	}
	ctx, cancel := context.WithTimeout(c.ctx, 15*time.Second)
	defer cancel()
	opts := &corev1.PodLogOptions{Container: container, Previous: previous, TailLines: &lines}
	b, err := c.cs.CoreV1().Pods(ns).GetLogs(pod, opts).DoRaw(ctx)
	if err != nil {
		return container, "", wrapf(err, "cannot read the log of %s/%s", ns, pod)
	}
	return container, string(b), nil
}
