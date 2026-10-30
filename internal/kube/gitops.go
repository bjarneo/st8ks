package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// GitOps support for Argo CD and Flux: the row columns, the issue rules and
// the actions of the detail panel and the assistant.

// argoApps is the table name of Argo CD Applications.
const argoApps = "applications.argoproj.io"

const (
	// argoSyncGrace hides an OutOfSync application while a normal sync can
	// still be on its way.
	argoSyncGrace = 2 * time.Minute
	// fluxUnknownGrace is how long a Flux object can stay in Ready Unknown.
	fluxUnknownGrace = 10 * time.Minute
)

// fluxKind describes one Flux kind.
type fluxKind struct {
	name string // the table name
	kind string // the singular kind, such as GitRepository
	noun string // the flux CLI noun for reconcile, suspend and resume
	get  string // the flux CLI noun for get
	col  string // the label of the source column
}

// fluxOrder lists the Flux kinds in the order of the issue loop.
var fluxOrder = []string{
	"kustomizations.kustomize.toolkit.fluxcd.io",
	"helmreleases.helm.toolkit.fluxcd.io",
	"gitrepositories.source.toolkit.fluxcd.io",
	"ocirepositories.source.toolkit.fluxcd.io",
	"helmrepositories.source.toolkit.fluxcd.io",
	"helmcharts.source.toolkit.fluxcd.io",
	"buckets.source.toolkit.fluxcd.io",
}

var fluxKinds = map[string]fluxKind{
	"kustomizations.kustomize.toolkit.fluxcd.io": {kind: "Kustomization", noun: "kustomization", get: "kustomizations", col: "Source"},
	"helmreleases.helm.toolkit.fluxcd.io":        {kind: "HelmRelease", noun: "helmrelease", get: "helmreleases", col: "Chart"},
	"gitrepositories.source.toolkit.fluxcd.io":   {kind: "GitRepository", noun: "source git", get: "sources git", col: "URL"},
	"ocirepositories.source.toolkit.fluxcd.io":   {kind: "OCIRepository", noun: "source oci", get: "sources oci", col: "URL"},
	"helmrepositories.source.toolkit.fluxcd.io":  {kind: "HelmRepository", noun: "source helm", get: "sources helm", col: "URL"},
	"helmcharts.source.toolkit.fluxcd.io":        {kind: "HelmChart", noun: "source chart", get: "sources chart", col: "Chart"},
	"buckets.source.toolkit.fluxcd.io":           {kind: "Bucket", noun: "source bucket", get: "sources bucket", col: "Endpoint"},
}

func init() {
	for name, fk := range fluxKinds {
		fk.name = name
		fluxKinds[name] = fk
	}
}

// gitopsKinds are the kinds that st8ks watches from the moment it connects,
// because the issue loop reads them.
var gitopsKinds = append([]string{argoApps}, fluxOrder...)

// isGitopsKind reports if st8ks watches a kind early for the GitOps rules.
func isGitopsKind(name string) bool {
	_, flux := fluxKinds[name]
	return name == argoApps || flux
}

// fluxTable maps a singular Flux kind to its table name.
func fluxTable(kind string) string {
	for name, fk := range fluxKinds {
		if fk.kind == kind {
			return name
		}
	}
	return ""
}

// ---- Fields ----

func nstr(obj map[string]any, fields ...string) string {
	v, _, _ := unstructured.NestedFieldNoCopy(obj, fields...)
	s, _ := v.(string)
	return s
}

func nbool(obj map[string]any, fields ...string) bool {
	v, _, _ := unstructured.NestedFieldNoCopy(obj, fields...)
	b, _ := v.(bool)
	return b
}

func nint(obj map[string]any, fields ...string) int64 {
	v, _, _ := unstructured.NestedFieldNoCopy(obj, fields...)
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	}
	return 0
}

func nslice(obj map[string]any, fields ...string) []any {
	v, _, _ := unstructured.NestedFieldNoCopy(obj, fields...)
	s, _ := v.([]any)
	return s
}

func nmap(obj map[string]any, fields ...string) map[string]any {
	v, _, _ := unstructured.NestedFieldNoCopy(obj, fields...)
	m, _ := v.(map[string]any)
	return m
}

func ntime(obj map[string]any, fields ...string) time.Time {
	t, _ := time.Parse(time.RFC3339, nstr(obj, fields...))
	return t
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// firstLine returns the first line of a message, clipped to n bytes.
func firstLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

func clipText(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// shortRev shortens the commit SHA or digest in a revision, such as
// main@sha1:0a1b2c3d… or a 40-character Argo CD revision.
func shortRev(rev string) string {
	for _, p := range []string{"sha1:", "sha256:"} {
		if i := strings.Index(rev, p); i >= 0 && len(rev) > i+len(p)+12 {
			return rev[:i+len(p)+12]
		}
	}
	if len(rev) == 40 && strings.Trim(rev, "0123456789abcdef") == "" {
		return rev[:7]
	}
	return rev
}

// sinceText formats the time since t, or "" for a zero time.
func sinceText(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return "since " + fmtDuration(now.Sub(t))
}

func digest(parts ...string) uint64 {
	h := fnv.New64a()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return h.Sum64()
}

// ---- Columns ----

// gitopsColumns replaces the printer columns of the GitOps kinds. The CRD
// columns of Argo CD have no tones, and the Flux columns show the message
// in the status column.
func gitopsColumns(k *Kind) {
	if !k.Namespaced {
		return
	}
	if k.Name == argoApps {
		k.Cols = nsCols(col("Health", CStatus), col("Sync", CStatus), col("Last sync", CStatus), col("Revision", CMono), col("Project", CText))
		k.row = argoRow
		k.Actions = []string{"argocd-sync", "argocd-refresh", "edit", "delete"}
		k.sig += "\x00gitops"
		return
	}
	fk, ok := fluxKinds[k.Name]
	if !ok {
		return
	}
	k.Cols = nsCols(col("Ready", CStatus), col("Reason", CStatus), col("Suspended", CStatus), col(fk.col, CMono), col("Revision", CMono), col("Message", CText))
	k.row = func(_ *Cluster, obj any) *Row { return fluxRow(fk, obj) }
	k.Actions = []string{"flux-reconcile", "flux-suspend", "edit", "delete"}
	k.sig += "\x00gitops"
}

// ---- Argo CD ----

// argoApp holds the fields of an Application that the rules and the row read.
type argoApp struct {
	health, healthMsg string
	healthAt          time.Time
	sync, revision    string
	project           string
	operation         bool // the operation field is set
	phase, opMsg      string
	finished          time.Time
	retries           int64
	created           time.Time
	conds             []map[string]any
	resources         []map[string]any
	results           []map[string]any
}

func readArgo(u *unstructured.Unstructured) argoApp {
	o := u.Object
	a := argoApp{
		health: nstr(o, "status", "health", "status"), healthMsg: nstr(o, "status", "health", "message"),
		healthAt: ntime(o, "status", "health", "lastTransitionTime"),
		sync:     nstr(o, "status", "sync", "status"), revision: nstr(o, "status", "sync", "revision"),
		project: nstr(o, "spec", "project"), operation: o["operation"] != nil,
		phase: nstr(o, "status", "operationState", "phase"), opMsg: nstr(o, "status", "operationState", "message"),
		finished: ntime(o, "status", "operationState", "finishedAt"), retries: nint(o, "status", "operationState", "retryCount"),
		created: u.GetCreationTimestamp().Time,
	}
	if a.revision == "" {
		var revs []string
		for _, r := range nslice(o, "status", "sync", "revisions") {
			if s, ok := r.(string); ok {
				revs = append(revs, shortRev(s))
			}
		}
		a.revision = strings.Join(revs, ", ")
	}
	maps := func(fields ...string) []map[string]any {
		var out []map[string]any
		for _, v := range nslice(o, fields...) {
			if m, ok := v.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	a.conds = maps("status", "conditions")
	a.resources = maps("status", "resources")
	a.results = maps("status", "operationState", "syncResult", "resources")
	return a
}

// running reports if a sync operation is in progress.
func (a *argoApp) running() bool {
	return a.operation || a.phase == "Running" || a.phase == "Terminating"
}

// outOfSync reports if the application is out of sync without a sync in
// progress.
func (a *argoApp) outOfSync() bool { return a.sync == "OutOfSync" && !a.running() }

// syncRef is the earliest time from which an application that st8ks sees
// for the first time can be out of sync.
func (a *argoApp) syncRef() time.Time {
	if a.finished.After(a.created) {
		return a.finished
	}
	return a.created
}

func (a *argoApp) lastSync() string {
	if a.operation && a.phase == "" {
		return "Running"
	}
	return a.phase
}

// digest covers the fields that the rules read and no cell shows, so a
// change to them runs the issue loop again.
func (a *argoApp) digest() uint64 {
	parts := []string{a.healthMsg, a.opMsg, a.finished.String(), strconv.FormatInt(a.retries, 10), strconv.FormatBool(a.operation)}
	for _, c := range a.conds {
		parts = append(parts, nstr(c, "type"), nstr(c, "message"))
	}
	for _, r := range a.resources {
		if h := nstr(r, "health", "status"); (h != "" && h != "Healthy") || nstr(r, "status") == "OutOfSync" {
			parts = append(parts, argoResource(r), nstr(r, "status"), h, nstr(r, "health", "message"))
		}
	}
	for _, r := range a.results {
		if nstr(r, "status") == "SyncFailed" || nstr(r, "hookPhase") == "Failed" || nstr(r, "hookPhase") == "Error" {
			parts = append(parts, argoResource(r), nstr(r, "message"))
		}
	}
	return digest(parts...)
}

func argoRow(_ *Cluster, obj any) *Row {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil
	}
	a := readArgo(u)
	b := begin(u, 8)
	b.skip().skip()
	b.status(a.health)
	b.status(a.sync)
	last := a.lastSync()
	b.add(orDash(last), phaseTone(last))
	b.text(shortRev(a.revision))
	b.text(a.project)
	b.skip()
	r := b.done(-1)
	r.S = a.digest()
	return r
}

func phaseTone(phase string) byte {
	switch phase {
	case "Succeeded":
		return tOK
	case "Failed", "Error":
		return tErr
	case "Running", "Terminating":
		return tWarn
	}
	return tNone
}

// argoResource names a resource of an Application, such as
// "Deployment prod/api".
func argoResource(r map[string]any) string {
	name := nstr(r, "name")
	if ns := nstr(r, "namespace"); ns != "" {
		name = ns + "/" + name
	}
	return nstr(r, "kind") + " " + name
}

// listMax adds at most n items to the evidence, and a count of the rest.
func listMax(ev, items []string, n int) []string {
	if len(items) > n {
		return append(append(ev, items[:n]...), fmt.Sprintf("%d more", len(items)-n))
	}
	return append(ev, items...)
}

type argoFinding struct {
	sev, reason, title, summary, fix, meta string
	refresh                                bool
	ev                                     []string
}

// argoIssue checks one Application. since is the time from which the
// application is out of sync, or zero. The second result is the time at
// which a pending rule fires, or zero.
func argoIssue(u *unstructured.Unstructured, a argoApp, now, since time.Time) (*Issue, time.Time) {
	ns, name := u.GetNamespace(), u.GetName()
	qn := ns + "/" + name
	var wake time.Time
	var found []argoFinding

	if !a.running() && (a.phase == "Failed" || a.phase == "Error") {
		f := argoFinding{sev: "er", reason: "Operation" + a.phase, title: name + " sync failed",
			summary: "The last sync ended in phase " + a.phase + ".",
			fix:     "Read the sync result and fix the manifests in Git. Then sync the application again."}
		if m := firstLine(a.opMsg, 300); m != "" {
			f.summary = "The last sync failed: " + m
		}
		if !a.finished.IsZero() {
			f.meta = "failed " + fmtDuration(now.Sub(a.finished)) + " ago"
		}
		f.ev = append(f.ev, "Sync phase "+a.phase+": "+orDash(clipText(a.opMsg, 1000)))
		var failed []string
		for _, r := range a.results {
			if nstr(r, "status") == "SyncFailed" || nstr(r, "hookPhase") == "Failed" || nstr(r, "hookPhase") == "Error" {
				failed = append(failed, argoResource(r)+": "+clipText(nstr(r, "message"), 400))
			}
		}
		f.ev = listMax(f.ev, failed, 5)
		if a.retries > 0 {
			f.ev = append(f.ev, fmt.Sprintf("Argo CD retried the sync %d times", a.retries))
		}
		found = append(found, f)
	}

	for _, c := range a.conds {
		typ, msg := nstr(c, "type"), nstr(c, "message")
		if !strings.HasSuffix(typ, "Error") {
			continue
		}
		f := argoFinding{sev: "er", reason: typ, title: name + " has a " + typ, summary: firstLine(msg, 300), refresh: true,
			fix: "Read the condition message and fix the cause. Then refresh the application."}
		switch typ {
		case "ComparisonError":
			f.summary = "Argo CD cannot compare the live state with Git, so the sync status is not known."
			f.fix = "Check the repository URL, the revision and the manifests in the message. Then refresh the application."
		case "InvalidSpecError":
			f.summary = "The application spec is not valid, so Argo CD does not reconcile the application."
			f.fix = "Fix the project, the destination or the source in the spec. Then refresh the application."
		case "SyncError":
			f.summary = "Argo CD cannot start the sync."
			f.fix = "Fix the cause in the message, then sync the application again."
			f.refresh = false
		}
		f.meta = sinceText(now, ntime(c, "lastTransitionTime"))
		f.ev = []string{typ + ": " + clipText(msg, 1000)}
		found = append(found, f)
	}

	resources := func(health, sync string) []string {
		var out []string
		for _, r := range a.resources {
			if (health != "" && nstr(r, "health", "status") == health) || (sync != "" && nstr(r, "status") == sync) {
				s := argoResource(r)
				if m := nstr(r, "health", "message"); m != "" && health != "" {
					s += ": " + clipText(m, 400)
				}
				if sync != "" && nbool(r, "requiresPruning") {
					s += " requires pruning"
				}
				out = append(out, s)
			}
		}
		return out
	}
	switch a.health {
	case "Degraded":
		bad := resources("Degraded", "")
		f := argoFinding{sev: "er", reason: "Degraded", title: name + " is degraded",
			summary: fmt.Sprintf("%d resources of the application are degraded.", len(bad)),
			fix:     "Open the degraded resources and check their events and logs. Fix the cause in Git, then sync the application."}
		if len(bad) == 1 {
			f.summary = "1 resource of the application is degraded."
		}
		if a.healthMsg != "" {
			f.summary = firstLine(a.healthMsg, 300)
			f.ev = append(f.ev, "Health Degraded: "+clipText(a.healthMsg, 1000))
		}
		f.meta = sinceText(now, a.healthAt)
		f.ev = listMax(f.ev, bad, 5)
		found = append(found, f)
	case "Missing":
		missing := resources("Missing", "")
		f := argoFinding{sev: "wa", reason: "Missing", title: name + " has missing resources", meta: sinceText(now, a.healthAt),
			summary: "Resources of the application do not exist in the cluster.",
			fix:     "Sync the application to create the missing resources."}
		if a.healthMsg != "" {
			f.ev = append(f.ev, "Health Missing: "+clipText(a.healthMsg, 1000))
		}
		f.ev = listMax(f.ev, missing, 5)
		found = append(found, f)
	}

	if a.outOfSync() && !since.IsZero() {
		if d := now.Sub(since); d >= argoSyncGrace {
			f := argoFinding{sev: "wa", reason: "OutOfSync", title: name + " is out of sync", meta: "out of sync " + fmtDuration(d),
				summary: "The live state differs from Git.",
				fix:     "Review the diff, then sync the application. If automated sync is on, check why it does not run."}
			f.ev = listMax([]string{"Out of sync for " + fmtDuration(d)}, resources("", "OutOfSync"), 5)
			found = append(found, f)
		} else {
			wake = since.Add(argoSyncGrace)
		}
	}
	if len(found) == 0 {
		return nil, wake
	}

	p := found[0]
	i := &Issue{ID: "argocd:" + qn, Sev: "wa", Title: p.title, Kind: argoApps, NS: ns, Obj: name, Summary: p.summary,
		Meta: firstNonEmpty(p.meta, "health "+orDash(a.health)+" · sync "+orDash(a.sync))}
	var reasons []string
	for _, f := range found {
		if f.sev == "er" {
			i.Sev = "er"
		}
		reasons = append(reasons, f.reason)
		i.Evidence = append(i.Evidence, f.ev...)
	}
	i.Reason = strings.Join(reasons, " · ")
	i.Cmds = []string{"argocd app get " + qn}
	if a.sync == "OutOfSync" {
		i.Cmds = append(i.Cmds, "argocd app diff "+qn)
	}
	i.Cmds = append(i.Cmds, "kubectl describe application.argoproj.io "+name+" -n "+ns)
	if p.refresh || a.running() {
		i.Fix = p.fix + " To refresh with the CLI, run argocd app get " + qn + " --hard-refresh."
		i.Action = &IssueAction{Label: "Refresh", Type: "argocd-refresh", Kind: argoApps, NS: ns, Name: name}
	} else {
		i.Fix = p.fix + " To sync with the CLI, run argocd app sync " + qn + "."
		i.Action = &IssueAction{Label: "Sync", Type: "argocd-sync", Kind: argoApps, NS: ns, Name: name}
	}
	return i, wake
}

// ---- Flux ----

type fluxCond struct {
	status, reason, message string
	at                      time.Time
}

func fluxConditions(obj map[string]any) map[string]fluxCond {
	out := map[string]fluxCond{}
	for _, v := range nslice(obj, "status", "conditions") {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		out[nstr(m, "type")] = fluxCond{status: nstr(m, "status"), reason: nstr(m, "reason"), message: nstr(m, "message"), at: ntime(m, "lastTransitionTime")}
	}
	return out
}

// fluxRef points to the source of a Flux object.
type fluxRef struct{ kind, ns, name string }

func (r fluxRef) String() string { return r.kind + " " + r.ns + "/" + r.name }

// fluxSource returns the source of a Kustomization, a HelmRelease or a
// HelmChart. A HelmRelease with a chart template uses the HelmChart that
// helm-controller creates for it.
func fluxSource(fk fluxKind, u *unstructured.Unstructured) *fluxRef {
	o, ns := u.Object, u.GetNamespace()
	ref := func(m map[string]any, defNS string) *fluxRef {
		if m == nil || nstr(m, "name") == "" {
			return nil
		}
		return &fluxRef{kind: nstr(m, "kind"), ns: firstNonEmpty(nstr(m, "namespace"), defNS), name: nstr(m, "name")}
	}
	switch fk.kind {
	case "Kustomization":
		return ref(nmap(o, "spec", "sourceRef"), ns)
	case "HelmChart":
		return ref(nmap(o, "spec", "sourceRef"), ns)
	case "HelmRelease":
		if r := ref(nmap(o, "spec", "chartRef"), ns); r != nil {
			return r
		}
		if hc := nstr(o, "status", "helmChart"); hc != "" {
			if cns, cname, ok := strings.Cut(hc, "/"); ok {
				return &fluxRef{kind: "HelmChart", ns: cns, name: cname}
			}
		}
		if src := ref(nmap(o, "spec", "chart", "spec", "sourceRef"), ns); src != nil {
			return &fluxRef{kind: "HelmChart", ns: src.ns, name: ns + "-" + u.GetName()}
		}
	}
	return nil
}

// fluxSourceCell is the text of the source column.
func fluxSourceCell(fk fluxKind, u *unstructured.Unstructured) string {
	o := u.Object
	at := func(name, version string) string {
		if version == "" {
			return name
		}
		return name + "@" + version
	}
	switch fk.kind {
	case "Kustomization":
		if r := fluxSource(fk, u); r != nil {
			if r.ns != u.GetNamespace() {
				return r.kind + "/" + r.ns + "/" + r.name
			}
			return r.kind + "/" + r.name
		}
	case "HelmRelease":
		if r := nmap(o, "spec", "chartRef"); r != nil {
			return nstr(r, "kind") + "/" + nstr(r, "name")
		}
		return at(nstr(o, "spec", "chart", "spec", "chart"), nstr(o, "spec", "chart", "spec", "version"))
	case "HelmChart":
		return at(nstr(o, "spec", "chart"), nstr(o, "spec", "version"))
	case "Bucket":
		if e := nstr(o, "spec", "endpoint"); e != "" {
			return e + "/" + nstr(o, "spec", "bucketName")
		}
	default:
		return nstr(o, "spec", "url")
	}
	return ""
}

func fluxRevision(fk fluxKind, o map[string]any) string {
	switch fk.kind {
	case "Kustomization":
		return firstNonEmpty(nstr(o, "status", "lastAppliedRevision"), nstr(o, "status", "lastAttemptedRevision"))
	case "HelmRelease":
		if r := nstr(o, "status", "lastAttemptedRevision"); r != "" {
			return r
		}
		if h := nslice(o, "status", "history"); len(h) > 0 {
			if m, ok := h[0].(map[string]any); ok {
				return nstr(m, "chartVersion")
			}
		}
		return ""
	}
	return nstr(o, "status", "artifact", "revision")
}

func fluxRow(fk fluxKind, obj any) *Row {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil
	}
	conds := fluxConditions(u.Object)
	ready, hasReady := conds["Ready"]
	b := begin(u, 9)
	b.skip().skip()
	if hasReady {
		b.add(orDash(ready.status), condTone(ready.status))
	} else {
		b.add("—", tNone)
	}
	reason := ready.reason
	if st := conds["Stalled"]; st.status == "True" {
		reason = "Stalled"
	}
	b.add(orDash(reason), condTone(ready.status))
	suspended := nbool(u.Object, "spec", "suspend")
	if suspended {
		b.add("True", tWarn)
	} else {
		b.add("False", tNone)
	}
	b.text(fluxSourceCell(fk, u))
	b.text(shortRev(fluxRevision(fk, u.Object)))
	b.text(firstLine(ready.message, 300))
	b.skip()
	r := b.done(-1)
	parts := []string{strconv.FormatBool(suspended), nstr(u.Object, "status", "helmChart")}
	if src := fluxSource(fk, u); src != nil {
		parts = append(parts, src.String())
	}
	for _, v := range nslice(u.Object, "status", "conditions") {
		if m, ok := v.(map[string]any); ok {
			parts = append(parts, nstr(m, "type"), nstr(m, "status"), nstr(m, "reason"), nstr(m, "message"), nstr(m, "lastTransitionTime"))
		}
	}
	r.S = digest(parts...)
	return r
}

// sourceLookup finds a Flux source in the watch cache. known is false when
// st8ks does not watch the kind.
type sourceLookup func(kind, ns, name string) (obj *unstructured.Unstructured, known bool)

// fluxFailing returns the failure of a source as text, or "" when it is
// ready. A Ready condition that is not True counts as a failure, because the
// object that uses the source fails now.
func fluxFailing(u *unstructured.Unstructured) string {
	conds := fluxConditions(u.Object)
	ready, ok := conds["Ready"]
	switch {
	case conds["Stalled"].status == "True":
		return "stalled: " + orDash(conds["Stalled"].reason) + ": " + firstLine(conds["Stalled"].message, 400)
	case !ok:
		return "it has no Ready condition"
	case ready.status == "True":
		return ""
	}
	return "Ready " + ready.status + ", reason " + orDash(ready.reason) + ": " + firstLine(ready.message, 400)
}

// fluxSourceEvidence follows the sources of an object, up to three steps,
// and names each source that is not ready. It returns the first source that
// fails, or nil.
func fluxSourceEvidence(fk fluxKind, u *unstructured.Unstructured, lookup sourceLookup) ([]string, *fluxRef) {
	var ev []string
	var first *fluxRef
	ref := fluxSource(fk, u)
	for step := 0; step < 3 && ref != nil && lookup != nil; step++ {
		su, known := lookup(ref.kind, ref.ns, ref.name)
		if !known {
			break
		}
		if su == nil {
			ev = append(ev, "st8ks cannot find source "+ref.String()+".")
			if first == nil {
				first = ref
			}
			break
		}
		why := fluxFailing(su)
		if why == "" {
			break
		}
		ev = append(ev, "Source "+ref.String()+" is not ready: "+why)
		if first == nil {
			first = ref
		}
		next := fluxKinds[fluxTable(ref.kind)]
		ref = fluxSource(next, su)
	}
	return ev, first
}

// fluxIssue checks one Flux object. The second result is the time at which
// a pending rule fires, or zero.
func fluxIssue(u *unstructured.Unstructured, fk fluxKind, now time.Time, lookup sourceLookup) (*Issue, time.Time) {
	conds := fluxConditions(u.Object)
	ready, hasReady := conds["Ready"]
	stalled := conds["Stalled"]
	ns, name := u.GetNamespace(), u.GetName()
	what := name + " " + fk.kind
	i := &Issue{ID: "flux:" + ns + "/" + fk.kind + "/" + name, Kind: fk.name, NS: ns, Obj: name}
	var msg string
	start := ready.at
	if !hasReady || start.IsZero() {
		start = u.GetCreationTimestamp().Time
	}
	switch {
	case stalled.status == "True":
		i.Sev, i.Reason, i.Title = "er", "Stalled", what+" is stalled"
		if stalled.reason != "" {
			i.Reason += " · " + stalled.reason
		}
		msg = firstNonEmpty(stalled.message, ready.message)
		i.Meta = sinceText(now, stalled.at)
	case hasReady && ready.status == "False":
		i.Sev, i.Reason, i.Title = "er", orDash(ready.reason), what+" is not ready"
		msg = ready.message
		i.Meta = sinceText(now, ready.at)
	case hasReady && ready.status == "True":
		return nil, time.Time{}
	default:
		if now.Sub(start) < fluxUnknownGrace {
			return nil, start.Add(fluxUnknownGrace)
		}
		i.Sev, i.Reason, i.Title = "wa", "Unknown", what+" does not become ready"
		if ready.reason != "" {
			i.Reason += " · " + ready.reason
		}
		msg = ready.message
		i.Meta = "unknown for " + fmtDuration(now.Sub(start))
	}

	if hasReady {
		i.Evidence = append(i.Evidence, "Ready "+ready.status+", reason "+orDash(ready.reason)+": "+orDash(clipText(ready.message, 1000)))
	} else {
		i.Evidence = append(i.Evidence, "No controller reported a Ready condition for "+fmtDuration(now.Sub(u.GetCreationTimestamp().Time))+".")
	}
	if stalled.status == "True" {
		i.Evidence = append(i.Evidence, "Stalled True, reason "+orDash(stalled.reason)+": "+orDash(clipText(stalled.message, 1000)))
	}
	if rev := fluxRevision(fk, u.Object); rev != "" {
		i.Evidence = append(i.Evidence, "Revision "+rev)
	}
	var src *fluxRef
	if fk.kind == "Kustomization" || fk.kind == "HelmRelease" {
		var ev []string
		ev, src = fluxSourceEvidence(fk, u, lookup)
		i.Evidence = append(i.Evidence, ev...)
	}

	cli := " " + name + " -n " + ns
	group := strings.SplitN(fk.name, ".", 2)[1]
	i.Cmds = []string{"flux get " + fk.get + cli, "flux events --for " + fk.kind + "/" + name + " -n " + ns,
		"kubectl describe " + strings.ToLower(fk.kind) + "." + group + cli}
	i.Summary = firstLine(msg, 300)
	reconcile := "flux reconcile " + fk.noun + cli
	switch {
	case src != nil:
		i.Title = what + " fails because its source fails"
		i.Fix = "Fix source " + src.String() + " first. Then reconcile the " + fk.kind + ". To do this with the CLI, run " + reconcile + " --with-source."
	case stalled.status == "True":
		i.Fix = "Flux stopped the retries, because the error needs a change. Fix the spec or the source, then reconcile. To do this with the CLI, run " + reconcile + "."
	case i.Sev == "wa":
		i.Fix = "Check that the Flux controllers run, and read their logs. Then reconcile. To do this with the CLI, run " + reconcile + "."
		if i.Summary == "" {
			i.Summary = "The state of the " + fk.kind + " is unknown for " + fmtDuration(now.Sub(start)) + "."
		}
	case fk.kind == "HelmRelease":
		i.Fix = "Read the Helm error in the message. Fix the chart or the values, then reconcile. To force an upgrade with the CLI, run " + reconcile + " --force."
	case fk.kind == "Kustomization":
		i.Fix = "Fix the manifests in the source, then reconcile. To do this with the CLI, run " + reconcile + "."
	default:
		i.Fix = "Check the URL, the credentials in spec.secretRef and the network access of source-controller. Then reconcile. To do this with the CLI, run " + reconcile + "."
	}
	if i.Summary == "" {
		i.Summary = "The " + fk.kind + " is not ready."
	}
	i.Action = &IssueAction{Label: "Reconcile", Type: "flux-reconcile", Kind: fk.name, NS: ns, Name: name}

	if nbool(u.Object, "spec", "suspend") {
		i.Sev = "wa"
		i.Title = what + " is suspended and not ready"
		i.Reason = "Suspended · " + i.Reason
		i.Summary = "Flux does not reconcile this " + fk.kind + ", because spec.suspend is true. Its last state is a failure."
		i.Fix = "Fix the cause, then resume the " + fk.kind + ". To do this with the CLI, run flux resume " + fk.noun + cli + "."
		i.Action = &IssueAction{Label: "Resume", Type: "flux-resume", Kind: fk.name, NS: ns, Name: name}
	}
	return i, time.Time{}
}

// ---- Issue loop ----

// gitopsState is what the GitOps rules remember between two runs.
type gitopsState struct {
	mu   sync.Mutex
	wake time.Time
	seen map[string]bool      // UIDs of the applications of the last run
	live map[string]bool      // UIDs of the applications of this run
	oos  map[string]time.Time // since when an application is out of sync, by UID
}

// since returns since when an application is out of sync, or zero. For an
// application that st8ks saw before in another state, the time starts now.
// For an application that st8ks sees for the first time, it starts at ref.
func (g *gitopsState) since(uid string, oos bool, ref, now time.Time) time.Time {
	if g.oos == nil {
		g.oos = map[string]time.Time{}
	}
	if g.live == nil {
		g.live = map[string]bool{}
	}
	g.live[uid] = true
	if !oos {
		delete(g.oos, uid)
		return time.Time{}
	}
	if t, ok := g.oos[uid]; ok {
		return t
	}
	t := now
	if !g.seen[uid] && !ref.IsZero() && ref.Before(now) {
		t = ref
	}
	g.oos[uid] = t
	return t
}

// commit forgets the applications that the run did not see.
func (g *gitopsState) commit() {
	g.seen, g.live = g.live, nil
	for uid := range g.oos {
		if !g.seen[uid] {
			delete(g.oos, uid)
		}
	}
}

// due reports if a time-based GitOps rule can fire now.
func (g *gitopsState) due(now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return !g.wake.IsZero() && !now.Before(g.wake)
}

// gitopsIssues runs the Argo CD and Flux rules on the watch cache.
func (c *Cluster) gitopsIssues(now time.Time, add func(*Issue)) {
	g := &c.derived.gitops
	g.mu.Lock()
	defer g.mu.Unlock()
	var wake time.Time
	soon := func(t time.Time) {
		if !t.IsZero() && (wake.IsZero() || t.Before(wake)) {
			wake = t
		}
	}
	for _, o := range c.list(argoApps) {
		u, ok := o.(*unstructured.Unstructured)
		if !ok {
			continue
		}
		a := readArgo(u)
		i, w := argoIssue(u, a, now, g.since(string(u.GetUID()), a.outOfSync(), a.syncRef(), now))
		soon(w)
		if i != nil {
			add(i)
		}
	}
	g.commit()
	lookup := func(kind, ns, name string) (*unstructured.Unstructured, bool) {
		t := fluxTable(kind)
		if t == "" || c.indexer(t) == nil {
			return nil, false
		}
		u, _ := c.getObj(t, ns, name).(*unstructured.Unstructured)
		return u, true
	}
	for _, name := range fluxOrder {
		fk := fluxKinds[name]
		for _, o := range c.list(name) {
			u, ok := o.(*unstructured.Unstructured)
			if !ok {
				continue
			}
			i, w := fluxIssue(u, fk, now, lookup)
			soon(w)
			if i != nil {
				add(i)
			}
		}
	}
	g.wake = wake
}

// ---- Actions ----

// gitopsObject reads an object for an action and checks its kind.
func (c *Cluster) gitopsObject(ctx context.Context, r Ref, ok bool, what string) (*Kind, *unstructured.Unstructured, error) {
	k := c.kind(r.Kind)
	if !ok || k == nil {
		return nil, nil, fmt.Errorf("%s %s is not %s", singular(r.Kind), r.Name, what)
	}
	u, err := c.ri(k, r.NS).Get(ctx, r.Name, metav1.GetOptions{})
	if err != nil {
		return nil, nil, wrapf(err, "cannot read %s %s", resourceString(k), r.Name)
	}
	return k, u, nil
}

func (c *Cluster) gitopsPatch(ctx context.Context, k *Kind, r Ref, patch []byte, dry bool) error {
	_, err := c.ri(k, r.NS).Patch(ctx, r.Name, types.MergePatchType, patch, metav1.PatchOptions{FieldManager: "st8ks", DryRun: dryOpts(dry)})
	return wrapf(err, "%s/%s was not changed", resourceString(k), r.Name)
}

// argoSyncPatch builds the change that starts a sync. It sets the operation
// field the way the Argo CD API server does for argocd app sync: the target
// revision of each source, the sync options and the retry strategy of the
// sync policy, and the hook strategy, without prune. Like the API server, it
// removes the state of the last operation. The resource version makes the
// patch fail when the application changed after the read.
func argoSyncPatch(u *unstructured.Unstructured) ([]byte, string, error) {
	if u.Object["operation"] != nil {
		return nil, "", fmt.Errorf("another operation runs on application %s. Wait until it ends", u.GetName())
	}
	if u.GetDeletionTimestamp() != nil {
		return nil, "", fmt.Errorf("application %s is being deleted", u.GetName())
	}
	o := u.Object
	sync := map[string]any{"syncStrategy": map[string]any{"hook": map[string]any{}}}
	var revs []string
	if srcs := nslice(o, "spec", "sources"); len(srcs) > 0 {
		list := make([]any, len(srcs))
		for i, s := range srcs {
			m, _ := s.(map[string]any)
			rev := firstNonEmpty(nstr(m, "targetRevision"), "HEAD")
			list[i] = rev
			revs = append(revs, rev)
		}
		sync["sources"] = srcs
		sync["revisions"] = list
	} else {
		src := nmap(o, "spec", "source")
		if src == nil {
			return nil, "", fmt.Errorf("application %s has no source", u.GetName())
		}
		rev := firstNonEmpty(nstr(src, "targetRevision"), "HEAD")
		sync["source"] = src
		sync["revision"] = rev
		revs = append(revs, rev)
	}
	if opts := nslice(o, "spec", "syncPolicy", "syncOptions"); len(opts) > 0 {
		sync["syncOptions"] = opts
	}
	op := map[string]any{"sync": sync, "initiatedBy": map[string]any{"username": "st8ks"}}
	if retry := nmap(o, "spec", "syncPolicy", "retry"); retry != nil {
		op["retry"] = retry
	}
	b, err := json.Marshal(map[string]any{
		"metadata":  map[string]any{"resourceVersion": u.GetResourceVersion()},
		"operation": op,
		"status":    map[string]any{"operationState": nil},
	})
	return b, strings.Join(revs, ", "), err
}

// ArgoSync starts a sync of an Argo CD Application, like argocd app sync.
func (c *Cluster) ArgoSync(r Ref, dry bool) (string, error) {
	ctx, cancel := context.WithTimeout(c.ctx, 20*time.Second)
	defer cancel()
	k, u, err := c.gitopsObject(ctx, r, r.Kind == argoApps, "an Argo CD Application")
	if err != nil {
		return "", err
	}
	patch, rev, err := argoSyncPatch(u)
	if err != nil {
		return "", err
	}
	if err := c.gitopsPatch(ctx, k, r, patch, dry); err != nil {
		return "", err
	}
	return resourceString(k) + "/" + r.Name + " sync to " + rev + " started" + suffix(dry), nil
}

// ArgoRefresh asks Argo CD to compare an Application with Git again. A hard
// refresh also generates the manifests again.
func (c *Cluster) ArgoRefresh(r Ref, hard, dry bool) (string, error) {
	ctx, cancel := context.WithTimeout(c.ctx, 20*time.Second)
	defer cancel()
	k, _, err := c.gitopsObject(ctx, r, r.Kind == argoApps, "an Argo CD Application")
	if err != nil {
		return "", err
	}
	mode := "normal"
	if hard {
		mode = "hard"
	}
	patch := []byte(`{"metadata":{"annotations":{"argocd.argoproj.io/refresh":"` + mode + `"}}}`)
	if err := c.gitopsPatch(ctx, k, r, patch, dry); err != nil {
		return "", err
	}
	return resourceString(k) + "/" + r.Name + " " + mode + " refresh requested (argocd.argoproj.io/refresh=" + mode + ")" + suffix(dry), nil
}

// FluxReconcile asks a Flux controller to reconcile an object now, like
// flux reconcile. force also runs a one-off Helm install or upgrade of a
// HelmRelease, like flux reconcile helmrelease --force.
func (c *Cluster) FluxReconcile(r Ref, force, dry bool) (string, error) {
	ctx, cancel := context.WithTimeout(c.ctx, 20*time.Second)
	defer cancel()
	fk, ok := fluxKinds[r.Kind]
	k, u, err := c.gitopsObject(ctx, r, ok, "a Flux object")
	if err != nil {
		return "", err
	}
	if force && fk.kind != "HelmRelease" {
		return "", fmt.Errorf("only a HelmRelease can be forced, and %s is a %s", r.Name, fk.kind)
	}
	if nbool(u.Object, "spec", "suspend") {
		return "", fmt.Errorf("%s %s is suspended. Resume it first", fk.kind, r.Name)
	}
	now := time.Now().Format(time.RFC3339Nano)
	ann := map[string]any{"reconcile.fluxcd.io/requestedAt": now}
	keys := "reconcile.fluxcd.io/requestedAt"
	if force {
		ann["reconcile.fluxcd.io/forceAt"] = now
		keys += " and reconcile.fluxcd.io/forceAt"
	}
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": ann}})
	if err != nil {
		return "", err
	}
	if err := c.gitopsPatch(ctx, k, r, patch, dry); err != nil {
		return "", err
	}
	return resourceString(k) + "/" + r.Name + " reconcile requested (" + keys + "=" + now + ")" + suffix(dry), nil
}

// FluxSuspend suspends or resumes a Flux object, like flux suspend and flux
// resume.
func (c *Cluster) FluxSuspend(r Ref, suspend, dry bool) (string, error) {
	ctx, cancel := context.WithTimeout(c.ctx, 20*time.Second)
	defer cancel()
	_, ok := fluxKinds[r.Kind]
	k, _, err := c.gitopsObject(ctx, r, ok, "a Flux object")
	if err != nil {
		return "", err
	}
	patch := []byte(fmt.Sprintf(`{"spec":{"suspend":%t}}`, suspend))
	if err := c.gitopsPatch(ctx, k, r, patch, dry); err != nil {
		return "", err
	}
	verb := "resumed"
	if suspend {
		verb = "suspended"
	}
	return fmt.Sprintf("%s/%s %s (spec.suspend=%t)%s", resourceString(k), r.Name, verb, suspend, suffix(dry)), nil
}
