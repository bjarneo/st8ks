// Package mcpserver serves the st8ks cluster data to AI agents over the
// Model Context Protocol. The tools read the watch cache of one context
// through internal/kube. No tool changes the cluster.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"st8ks/internal/kube"
)

// Cluster is the cluster access of the tools. *kube.Cluster implements it.
type Cluster interface {
	ClusterContext() string
	Issues() []kube.Issue
	IssueContext(id string) (string, []string, error)
	ResolveKind(name string) (kube.KindInfo, bool)
	TableText(ctx context.Context, kind, ns, filter string, limit int) (kube.TableView, error)
	GetObject(ref kube.Ref) (*kube.ObjectDoc, error)
	TailLogs(ns, pod, container string, previous bool, lines int64) (string, string, error)
	RbacExplain(sub kube.Subject, ns, res, verb string) kube.RbacAnswer
	Topology(ns string) kube.Topology
	HelmReleases() ([]kube.HelmRelease, error)
	Apply(yaml string, dry bool) (string, error)
}

// Source returns the cluster. It can block until the watches have synced.
// note describes data that is not complete, or is empty.
type Source func(ctx context.Context) (c Cluster, note string, err error)

// Info describes the server to the client.
type Info struct {
	Version string
	Context string
}

const instructions = `These tools read one Kubernetes cluster through st8ks, a Kubernetes desktop client.
Start with cluster_overview or list_issues. Use explain_issue for the evidence of one issue, then get_resource, get_logs and list_resources for more data.
Every tool is read-only. dry_run_apply runs a server dry run, which validates a change and stores nothing.
st8ks removes the values of Secrets before they leave the process. Do not ask the user for Secret values.`

// New creates the server with every tool.
func New(src Source, info Info) *mcp.Server {
	ins := instructions
	if info.Context != "" {
		ins = "The kubeconfig context is " + info.Context + ".\n" + ins
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "st8ks", Title: "st8ks Kubernetes", Version: info.Version},
		&mcp.ServerOptions{Instructions: ins, Capabilities: &mcp.ServerCapabilities{}})
	t := &tools{src: src}
	add(s, "cluster_overview", "Cluster overview",
		"Summarize the cluster. The result has the context, the distribution and version, the nodes and their readiness, and the running pods against the capacity. It also has the CPU and memory requests against allocatable, and one line for each open issue. Call this tool first.",
		t.overview)
	add(s, "list_issues", "List issues",
		"List the problems that the st8ks rule engine detects now. Examples are crash loops, OOM kills, images that cannot be pulled, pods that cannot be scheduled, nodes that are not ready, stuck rollouts and failed jobs. Each item has an id for explain_issue, a severity of error or warning, the object, the reason and a summary. Pods of one workload group into one issue.",
		t.listIssues)
	add(s, "explain_issue", "Explain an issue",
		"Collect the evidence for one issue. The result has the findings and the suggested fix of the rule engine, the YAML of the object and of its owning workload, and the recent events. It also has the last 80 log lines of each container. For a container that restarted, these lines come from before the restart. The result ends with the kubectl commands that read the same data.",
		t.explainIssue)
	add(s, "list_resources", "List resources",
		"List the objects of one kind with the columns of the st8ks list view, such as status, ready count, restarts and age. kind accepts the st8ks tree names such as Pods or certificates.cert-manager.io, and kubectl names such as pod, deploy, svc or deployments.apps. Rows are sorted by namespace and name. Secret rows show only the type and the key count.",
		t.listResources)
	add(s, "get_resource", "Get a resource",
		"Read one object: its YAML without managedFields, its status, owner and Helm release, the objects that relate to it, and its events, newest first. st8ks replaces each value of a Secret with its byte length, so the result never holds Secret data.",
		t.getResource)
	add(s, "get_logs", "Get container logs",
		"Read the last lines of a container log. tail is the number of lines, 100 by default and 500 at most. Set previous to read the log of the container before its last restart, for example after a crash. Without container, the default container of the pod is used.",
		t.getLogs)
	add(s, "rbac_check", "Check RBAC permission",
		"Check if a user, group or service account may do a verb on a resource, and name the binding and the rule that allow it. The API server confirms the answer with a SubjectAccessReview. Also returns the kubectl auth can-i command that asks the same question.",
		t.rbacCheck)
	add(s, "topology", "Namespace topology",
		"Show the object graph of one namespace: ingresses, services, pods, ReplicaSets and Jobs, workloads, and the ConfigMaps, Secrets and claims that pods use. Each node has a status. Edges show what owns, routes to or uses what.",
		t.topology)
	add(s, "helm_releases", "List Helm releases",
		"List the latest revision of every Helm release with its namespace, chart, app version, revision, status and the time of the last deploy.",
		t.helmReleases)
	add(s, "dry_run_apply", "Dry-run a change",
		"Validate a changed version of one existing object with a server dry run. The API server runs validation and admission for an update of the object, but it stores nothing. The cluster does not change. The YAML must hold exactly one object that exists in the cluster. Remove metadata.resourceVersion to skip the conflict check. Use this tool to check a proposed fix before you show it to the user.",
		t.dryRunApply)
	return s
}

func add[In, Out any](s *mcp.Server, name, title, desc string, h mcp.ToolHandlerFor[In, Out]) {
	closed := false
	mcp.AddTool(s, &mcp.Tool{
		Name:        name,
		Title:       title,
		Description: desc,
		Annotations: &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closed},
	}, h)
}

type tools struct {
	src Source
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func withNote(s, note string) string {
	if note == "" {
		return s
	}
	return "Note: " + note + "\n\n" + s
}

// ---- Tools ----

type noInput struct{}

func (t *tools) overview(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, any, error) {
	c, note, err := t.src(ctx)
	if err != nil {
		return nil, nil, err
	}
	return text(withNote(c.ClusterContext(), note)), nil, nil
}

type listIssuesIn struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"Only issues in this namespace. Leave it out for all namespaces."`
}

// IssueItem is one issue in list_issues.
type IssueItem struct {
	ID        string `json:"id" jsonschema:"The id to pass to explain_issue"`
	Severity  string `json:"severity" jsonschema:"error or warning"`
	Title     string `json:"title"`
	Kind      string `json:"kind" jsonschema:"The st8ks kind name of the object, such as Pods"`
	Namespace string `json:"namespace,omitempty"`
	Object    string `json:"object"`
	Reason    string `json:"reason"`
	Summary   string `json:"summary"`
}

// IssueList is the result of list_issues.
type IssueList struct {
	Issues []IssueItem `json:"issues"`
	Note   string      `json:"note,omitempty"`
}

func severity(s string) string {
	if s == "er" {
		return "error"
	}
	return "warning"
}

func (t *tools) listIssues(ctx context.Context, _ *mcp.CallToolRequest, in listIssuesIn) (*mcp.CallToolResult, IssueList, error) {
	c, note, err := t.src(ctx)
	if err != nil {
		return nil, IssueList{}, err
	}
	out := IssueList{Issues: []IssueItem{}, Note: note}
	for _, i := range c.Issues() {
		if in.Namespace != "" && i.NS != in.Namespace {
			continue
		}
		out.Issues = append(out.Issues, IssueItem{ID: i.ID, Severity: severity(i.Sev), Title: i.Title, Kind: i.Kind,
			Namespace: i.NS, Object: i.Obj, Reason: i.Reason, Summary: i.Summary})
	}
	return nil, out, nil
}

type explainIn struct {
	ID string `json:"id" jsonschema:"The issue id from list_issues"`
}

func (t *tools) explainIssue(ctx context.Context, _ *mcp.CallToolRequest, in explainIn) (*mcp.CallToolResult, any, error) {
	c, _, err := t.src(ctx)
	if err != nil {
		return nil, nil, err
	}
	var cmds []string
	for _, i := range c.Issues() {
		if i.ID == in.ID {
			cmds = append(cmds, i.Cmds...)
		}
	}
	body, more, err := c.IssueContext(in.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("issue %s: %w. Call list_issues for the current ids", in.ID, err)
	}
	var b strings.Builder
	b.WriteString(body)
	if cmds = append(cmds, more...); len(cmds) > 0 {
		b.WriteString("\n--- kubectl commands that read the same data ---\n")
		seen := map[string]bool{}
		for _, cmd := range cmds {
			if !seen[cmd] {
				seen[cmd] = true
				b.WriteString("$ " + cmd + "\n")
			}
		}
	}
	return text(b.String()), nil, nil
}

type listIn struct {
	Kind      string `json:"kind" jsonschema:"The kind, such as Pods, pod, deploy, svc, deployments.apps or certificates.cert-manager.io"`
	Namespace string `json:"namespace,omitempty" jsonschema:"Only objects in this namespace. Leave it out for all namespaces."`
	Filter    string `json:"filter,omitempty" jsonschema:"Only rows whose name, namespace, cells or labels contain this text. The match ignores case. A label matches as key=value."`
	Limit     int    `json:"limit,omitempty" jsonschema:"The maximum number of rows, 200 by default and 1000 at most"`
}

func resolve(c Cluster, kind string) (kube.KindInfo, error) {
	k, ok := c.ResolveKind(kind)
	if !ok {
		return kube.KindInfo{}, fmt.Errorf("unknown kind %q. Use a kind such as Pods, pod, deploy, svc, deployments.apps or certificates.cert-manager.io", kind)
	}
	return k, nil
}

func (t *tools) listResources(ctx context.Context, _ *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, kube.TableView, error) {
	c, note, err := t.src(ctx)
	if err != nil {
		return nil, kube.TableView{}, err
	}
	k, err := resolve(c, in.Kind)
	if err != nil {
		return nil, kube.TableView{}, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 200
	}
	limit = min(limit, 1000)
	tv, err := c.TableText(ctx, k.Name, in.Namespace, in.Filter, limit)
	if err != nil {
		return nil, kube.TableView{}, err
	}
	if note != "" {
		tv.Note = strings.TrimSpace(tv.Note + " " + note)
	}
	return nil, tv, nil
}

type getIn struct {
	Kind      string `json:"kind" jsonschema:"The kind, such as Pods, pod, deploy, svc, deployments.apps or certificates.cert-manager.io"`
	Namespace string `json:"namespace,omitempty" jsonschema:"The namespace. Leave it out for a cluster-scoped kind such as nodes."`
	Name      string `json:"name" jsonschema:"The object name"`
}

func (t *tools) getResource(ctx context.Context, _ *mcp.CallToolRequest, in getIn) (*mcp.CallToolResult, any, error) {
	c, _, err := t.src(ctx)
	if err != nil {
		return nil, nil, err
	}
	k, err := resolve(c, in.Kind)
	if err != nil {
		return nil, nil, err
	}
	if k.Namespaced && in.Namespace == "" {
		return nil, nil, fmt.Errorf("%s is a namespaced kind. Set namespace", k.Kind)
	}
	ns := in.Namespace
	if !k.Namespaced {
		ns = ""
	}
	doc, err := c.GetObject(kube.Ref{Kind: k.Name, NS: ns, Name: in.Name})
	if err != nil {
		return nil, nil, err
	}
	yml, err := RedactSecrets(doc.YAML)
	if err != nil {
		return nil, nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\napiVersion: %s\n", k.Kind, objName(doc.NS, doc.Name), doc.API)
	if doc.Status != "" {
		fmt.Fprintf(&b, "Status: %s\n", doc.Status)
	}
	if doc.Owner != nil {
		fmt.Fprintf(&b, "Owner: %s %s\n", doc.Owner.Kind, doc.Owner.Name)
	}
	if doc.Managed != nil && (doc.Owner == nil || *doc.Managed != *doc.Owner) {
		fmt.Fprintf(&b, "Managed by: %s %s\n", doc.Managed.Kind, doc.Managed.Name)
	}
	if doc.Helm != nil {
		fmt.Fprintf(&b, "Helm release: %s\n", objName(doc.Helm.NS, doc.Helm.Name))
	}
	b.WriteString("\n--- YAML ---\n")
	b.WriteString(yml)
	if len(doc.Related) > 0 {
		b.WriteString("\n--- Related objects ---\n")
		for _, r := range doc.Related {
			fmt.Fprintf(&b, "%s: %s %s\n", r.Rel, r.Kind, objName(r.NS, r.Name))
		}
	}
	b.WriteString("\n--- Events, newest first ---\n")
	if len(doc.Events) == 0 {
		b.WriteString("No events.\n")
	}
	for n, e := range doc.Events {
		if n == 30 {
			fmt.Fprintf(&b, "… %d older events\n", len(doc.Events)-n)
			break
		}
		fmt.Fprintf(&b, "%s %s x%d (%s ago): %s\n", e.Type, e.Reason, e.Count, age(e.Last), e.Msg)
	}
	return text(b.String()), nil, nil
}

func objName(ns, name string) string {
	if ns == "" {
		return name
	}
	return ns + "/" + name
}

// age formats the time since unix seconds like kubectl: 45s, 12m, 3h, 2d.
func age(unix int64) string {
	if unix <= 0 {
		return "?"
	}
	d := time.Since(time.Unix(unix, 0))
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(0, int(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

type logsIn struct {
	Namespace string `json:"namespace" jsonschema:"The namespace of the pod"`
	Pod       string `json:"pod" jsonschema:"The pod name"`
	Container string `json:"container,omitempty" jsonschema:"The container. Leave it out for the default container."`
	Previous  bool   `json:"previous,omitempty" jsonschema:"Read the log of the container before its last restart"`
	Tail      int    `json:"tail,omitempty" jsonschema:"The number of lines from the end, 100 by default and 500 at most"`
}

const maxTail = 500

func (t *tools) getLogs(ctx context.Context, _ *mcp.CallToolRequest, in logsIn) (*mcp.CallToolResult, any, error) {
	c, _, err := t.src(ctx)
	if err != nil {
		return nil, nil, err
	}
	n := in.Tail
	if n <= 0 {
		n = 100
	}
	n = min(n, maxTail)
	ct, logs, err := c.TailLogs(in.Namespace, in.Pod, in.Container, in.Previous, int64(n))
	if err != nil {
		return nil, nil, err
	}
	which := "current"
	if in.Previous {
		which = "previous"
	}
	head := fmt.Sprintf("Last %d lines of the %s log of container %s in pod %s/%s:\n", n, which, ct, in.Namespace, in.Pod)
	if strings.TrimSpace(logs) == "" {
		return text(head + "The log is empty."), nil, nil
	}
	return text(head + logs), nil, nil
}

type rbacIn struct {
	SubjectKind      string `json:"subject_kind" jsonschema:"User, Group or ServiceAccount"`
	SubjectName      string `json:"subject_name" jsonschema:"The name of the user, group or service account. A service account can also be system:serviceaccount:NAMESPACE:NAME."`
	SubjectNamespace string `json:"subject_namespace,omitempty" jsonschema:"The namespace of the service account"`
	Verb             string `json:"verb" jsonschema:"The verb, such as get, list, watch, create, update, patch or delete"`
	Resource         string `json:"resource" jsonschema:"The resource, such as pods, pods/log, pods/exec, deployments.apps, secrets or nodes. Kind names such as deploy also work."`
	Namespace        string `json:"namespace,omitempty" jsonschema:"The namespace of the request, default when left out. Cluster-scoped resources ignore it."`
}

// RbacResult is the result of rbac_check.
type RbacResult struct {
	Allowed   bool   `json:"allowed"`
	Partial   bool   `json:"partial,omitempty" jsonschema:"True when a rule allows the verb only for named objects"`
	Why       string `json:"why" jsonschema:"The binding and the rule that decide the answer"`
	Kubectl   string `json:"kubectl" jsonschema:"The kubectl command that asks the same question"`
	APIServer string `json:"apiServer" jsonschema:"The answer of the API server to a SubjectAccessReview"`
}

func subject(in rbacIn) (kube.Subject, error) {
	name := strings.TrimSpace(in.SubjectName)
	if name == "" {
		return kube.Subject{}, errors.New("set subject_name")
	}
	switch strings.ToLower(strings.TrimSpace(in.SubjectKind)) {
	case "user":
		if rest, ok := strings.CutPrefix(name, "system:serviceaccount:"); ok {
			if ns, sa, ok := strings.Cut(rest, ":"); ok {
				return kube.Subject{Kind: "ServiceAccount", Name: sa, NS: ns}, nil
			}
		}
		return kube.Subject{Kind: "User", Name: name}, nil
	case "group":
		return kube.Subject{Kind: "Group", Name: name}, nil
	case "serviceaccount", "sa":
		ns := in.SubjectNamespace
		if rest, ok := strings.CutPrefix(name, "system:serviceaccount:"); ok {
			if n, sa, ok := strings.Cut(rest, ":"); ok {
				ns, name = n, sa
			}
		}
		if ns == "" {
			return kube.Subject{}, errors.New("set subject_namespace for a ServiceAccount")
		}
		return kube.Subject{Kind: "ServiceAccount", Name: name, NS: ns}, nil
	}
	return kube.Subject{}, fmt.Errorf("subject_kind must be User, Group or ServiceAccount, not %q", in.SubjectKind)
}

func (t *tools) rbacCheck(ctx context.Context, _ *mcp.CallToolRequest, in rbacIn) (*mcp.CallToolResult, RbacResult, error) {
	c, _, err := t.src(ctx)
	if err != nil {
		return nil, RbacResult{}, err
	}
	sub, err := subject(in)
	if err != nil {
		return nil, RbacResult{}, err
	}
	verb := strings.ToLower(strings.TrimSpace(in.Verb))
	if verb == "" {
		return nil, RbacResult{}, errors.New("set verb")
	}
	res := strings.TrimSpace(in.Resource)
	base, sub2, hasSub := strings.Cut(res, "/")
	if k, ok := c.ResolveKind(base); ok {
		res = k.Resource
		if k.Group != "" {
			res += "." + k.Group
		}
		if hasSub {
			res += "/" + sub2
		}
	}
	if res == "" {
		return nil, RbacResult{}, errors.New("set resource")
	}
	ns := in.Namespace
	if ns == "" {
		ns = "default"
	}
	a := c.RbacExplain(sub, ns, res, verb)
	return nil, RbacResult{Allowed: a.Allowed, Partial: a.Partial, Why: a.Why, Kubectl: a.Cmd, APIServer: a.Verified}, nil
}

type nsIn struct {
	Namespace string `json:"namespace" jsonschema:"The namespace"`
}

func (t *tools) topology(ctx context.Context, _ *mcp.CallToolRequest, in nsIn) (*mcp.CallToolResult, any, error) {
	c, note, err := t.src(ctx)
	if err != nil {
		return nil, nil, err
	}
	if in.Namespace == "" {
		return nil, nil, errors.New("set namespace")
	}
	return text(withNote(TopologyText(c.Topology(in.Namespace)), note)), nil, nil
}

type helmIn struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"Only releases in this namespace. Leave it out for all namespaces."`
}

// HelmItem is one release in helm_releases.
type HelmItem struct {
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
	Chart      string `json:"chart"`
	AppVersion string `json:"appVersion"`
	Revision   int    `json:"revision"`
	Status     string `json:"status"`
	Updated    string `json:"updated,omitempty" jsonschema:"The time of the last deploy, RFC 3339"`
}

// HelmList is the result of helm_releases.
type HelmList struct {
	Releases []HelmItem `json:"releases"`
}

func (t *tools) helmReleases(ctx context.Context, _ *mcp.CallToolRequest, in helmIn) (*mcp.CallToolResult, HelmList, error) {
	c, _, err := t.src(ctx)
	if err != nil {
		return nil, HelmList{}, err
	}
	rels, err := c.HelmReleases()
	if err != nil {
		return nil, HelmList{}, err
	}
	out := HelmList{Releases: []HelmItem{}}
	for _, r := range rels {
		if in.Namespace != "" && r.NS != in.Namespace {
			continue
		}
		h := HelmItem{Name: r.Name, Namespace: r.NS, Chart: r.Chart, AppVersion: r.App, Revision: r.Rev, Status: r.Status}
		if r.Updated > 0 {
			h.Updated = time.Unix(r.Updated, 0).UTC().Format(time.RFC3339)
		}
		out.Releases = append(out.Releases, h)
	}
	return nil, out, nil
}

type dryRunIn struct {
	YAML string `json:"yaml" jsonschema:"The full YAML of one changed object, with apiVersion, kind and metadata.name. Namespaced objects without metadata.namespace use default."`
}

func (t *tools) dryRunApply(ctx context.Context, _ *mcp.CallToolRequest, in dryRunIn) (*mcp.CallToolResult, any, error) {
	c, _, err := t.src(ctx)
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(in.YAML) == "" {
		return nil, nil, errors.New("set yaml")
	}
	// The dry flag is fixed. This tool never changes the cluster.
	msg, err := c.Apply(in.YAML, true)
	if err != nil {
		return nil, nil, fmt.Errorf("the server dry run failed: %w", err)
	}
	return text(msg + "\nThe API server accepted the change and stored nothing. The user decides if the change is applied."), nil, nil
}
