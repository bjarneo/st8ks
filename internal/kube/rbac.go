package kube

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Subject is a user, group or service account that appears in a binding.
type Subject struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	NS       string `json:"ns,omitempty"`
	Bindings int    `json:"bindings"`
}

func (s Subject) id() string {
	if s.Kind == rbacv1.ServiceAccountKind {
		return s.NS + "/" + s.Name
	}
	return s.Name
}

// RbacResource is one row of the permission matrix.
type RbacResource struct {
	Name    string `json:"name"`
	Group   string `json:"group"`
	Cluster bool   `json:"cluster"`
}

// RbacCell is one verb on one resource.
type RbacCell struct {
	A  bool `json:"a"`
	P  bool `json:"p,omitempty"`
	NA bool `json:"na,omitempty"`
}

// RbacMatrix holds effective permissions of a subject in a namespace.
type RbacMatrix struct {
	Resources []RbacResource `json:"resources"`
	Verbs     []string       `json:"verbs"`
	Cells     [][]RbacCell   `json:"cells"`
	Bindings  []string       `json:"bindings"`
}

// RbacAnswer explains one cell.
type RbacAnswer struct {
	Allowed  bool   `json:"allowed"`
	Partial  bool   `json:"partial"`
	Why      string `json:"why"`
	Cmd      string `json:"cmd"`
	Verified string `json:"verified"`
}

var rbacResources = []RbacResource{
	{Name: "pods"}, {Name: "pods/log"}, {Name: "pods/exec"}, {Name: "deployments", Group: "apps"},
	{Name: "services"}, {Name: "configmaps"}, {Name: "secrets"}, {Name: "ingresses", Group: "networking.k8s.io"},
	{Name: "persistentvolumeclaims"}, {Name: "roles", Group: "rbac.authorization.k8s.io"},
	{Name: "rolebindings", Group: "rbac.authorization.k8s.io"}, {Name: "nodes", Cluster: true}, {Name: "namespaces", Cluster: true},
}

var rbacVerbs = []string{"get", "list", "watch", "create", "update", "patch", "delete"}

func verbApplies(res, verb string) bool {
	switch res {
	case "pods/log":
		return verb == "get"
	case "pods/exec":
		return verb == "get" || verb == "create"
	}
	return true
}

// RbacSubjects lists every subject named in a binding.
func (c *Cluster) RbacSubjects() []Subject {
	c.Ensure("RoleBindings")
	c.Ensure("ClusterRoleBindings")
	seen := map[string]*Subject{}
	addAll := func(subs []rbacv1.Subject, defNS string) {
		for _, s := range subs {
			sub := Subject{Kind: s.Kind, Name: s.Name}
			if s.Kind == rbacv1.ServiceAccountKind {
				sub.NS = s.Namespace
				if sub.NS == "" {
					sub.NS = defNS
				}
			}
			key := sub.Kind + ":" + sub.id()
			if seen[key] == nil {
				seen[key] = &sub
			}
			seen[key].Bindings++
		}
	}
	for _, o := range c.list("ClusterRoleBindings") {
		addAll(o.(*rbacv1.ClusterRoleBinding).Subjects, "")
	}
	for _, o := range c.list("RoleBindings") {
		rb := o.(*rbacv1.RoleBinding)
		addAll(rb.Subjects, rb.Namespace)
	}
	out := make([]Subject, 0, len(seen))
	for _, s := range seen {
		out = append(out, *s)
	}
	rank := func(s Subject) int {
		r := map[string]int{"User": 0, "Group": 1, "ServiceAccount": 2}[s.Kind]
		if strings.HasPrefix(s.Name, "system:") || strings.HasPrefix(s.NS, "kube-") {
			r += 10
		}
		return r
	}
	sort.Slice(out, func(i, j int) bool {
		if rank(out[i]) != rank(out[j]) {
			return rank(out[i]) < rank(out[j])
		}
		return out[i].id() < out[j].id()
	})
	return out
}

type grant struct {
	binding string
	role    string
	via     string
	rule    rbacv1.PolicyRule
	cluster bool
}

// subjectMatches reports if a binding subject covers sub, directly or
// through an implicit group.
func subjectMatches(b rbacv1.Subject, bindingNS string, sub Subject) (bool, string) {
	switch b.Kind {
	case rbacv1.GroupKind:
		if sub.Kind == rbacv1.GroupKind && b.Name == sub.Name {
			return true, ""
		}
		if sub.Kind == rbacv1.UserKind || sub.Kind == rbacv1.ServiceAccountKind {
			implicit := []string{"system:authenticated"}
			if sub.Kind == rbacv1.ServiceAccountKind {
				implicit = append(implicit, "system:serviceaccounts", "system:serviceaccounts:"+sub.NS)
			}
			if slices.Contains(implicit, b.Name) {
				return true, "Group " + b.Name
			}
		}
	case rbacv1.UserKind:
		if sub.Kind == rbacv1.UserKind && b.Name == sub.Name {
			return true, ""
		}
		if sub.Kind == rbacv1.ServiceAccountKind && b.Name == "system:serviceaccount:"+sub.NS+":"+sub.Name {
			return true, ""
		}
	case rbacv1.ServiceAccountKind:
		ns := b.Namespace
		if ns == "" {
			ns = bindingNS
		}
		if sub.Kind == rbacv1.ServiceAccountKind && b.Name == sub.Name && ns == sub.NS {
			return true, ""
		}
	}
	return false, ""
}

func (c *Cluster) roleRules(ns string, ref rbacv1.RoleRef) []rbacv1.PolicyRule {
	if ref.Kind == "Role" {
		if o := c.getObj("Roles", ns, ref.Name); o != nil {
			return o.(*rbacv1.Role).Rules
		}
		return nil
	}
	if o := c.getObj("ClusterRoles", "", ref.Name); o != nil {
		return o.(*rbacv1.ClusterRole).Rules
	}
	return nil
}

func (c *Cluster) grants(sub Subject, ns string) ([]grant, []string) {
	c.Ensure("Roles")
	c.Ensure("ClusterRoles")
	var out []grant
	var bindings []string
	for _, o := range c.list("ClusterRoleBindings") {
		b := o.(*rbacv1.ClusterRoleBinding)
		for _, s := range b.Subjects {
			if ok, via := subjectMatches(s, "", sub); ok {
				name := "ClusterRoleBinding " + b.Name
				bindings = append(bindings, name+" → ClusterRole "+b.RoleRef.Name)
				for _, r := range c.roleRules("", b.RoleRef) {
					out = append(out, grant{binding: name, role: "ClusterRole " + b.RoleRef.Name, via: via, rule: r, cluster: true})
				}
				break
			}
		}
	}
	for _, o := range c.byIndex("RoleBindings", "namespace", ns) {
		b := o.(*rbacv1.RoleBinding)
		for _, s := range b.Subjects {
			if ok, via := subjectMatches(s, b.Namespace, sub); ok {
				name := "RoleBinding " + b.Namespace + "/" + b.Name
				bindings = append(bindings, name+" → "+b.RoleRef.Kind+" "+b.RoleRef.Name)
				for _, r := range c.roleRules(b.Namespace, b.RoleRef) {
					out = append(out, grant{binding: name, role: b.RoleRef.Kind + " " + b.RoleRef.Name, via: via, rule: r})
				}
				break
			}
		}
	}
	return out, bindings
}

func ruleMatches(r rbacv1.PolicyRule, group, res, verb string) bool {
	if !slices.Contains(r.Verbs, verb) && !slices.Contains(r.Verbs, "*") {
		return false
	}
	if !slices.Contains(r.APIGroups, group) && !slices.Contains(r.APIGroups, "*") {
		return false
	}
	if slices.Contains(r.Resources, res) || slices.Contains(r.Resources, "*") {
		return true
	}
	if i := strings.Index(res, "/"); i > 0 && slices.Contains(r.Resources, "*"+res[i:]) {
		return true
	}
	return false
}

func (c *Cluster) evaluate(sub Subject, ns string, rr RbacResource, verb string, gs []grant) (bool, bool, *grant) {
	var partial *grant
	for i := range gs {
		g := &gs[i]
		if rr.Cluster && !g.cluster {
			continue
		}
		if !ruleMatches(g.rule, rr.Group, rr.Name, verb) {
			continue
		}
		if len(g.rule.ResourceNames) > 0 {
			if partial == nil {
				partial = g
			}
			continue
		}
		return true, false, g
	}
	if partial != nil {
		return false, true, partial
	}
	return false, false, nil
}

// RbacMatrix computes the permission matrix of a subject.
func (c *Cluster) RbacMatrix(sub Subject, ns string) RbacMatrix {
	gs, bindings := c.grants(sub, ns)
	m := RbacMatrix{Resources: rbacResources, Verbs: rbacVerbs, Bindings: bindings}
	if m.Bindings == nil {
		m.Bindings = []string{}
	}
	for _, rr := range rbacResources {
		row := make([]RbacCell, len(rbacVerbs))
		for j, v := range rbacVerbs {
			if !verbApplies(rr.Name, v) {
				row[j] = RbacCell{NA: true}
				continue
			}
			a, p, _ := c.evaluate(sub, ns, rr, v, gs)
			row[j] = RbacCell{A: a, P: p}
		}
		m.Cells = append(m.Cells, row)
	}
	return m
}

func isWildcardRule(r rbacv1.PolicyRule) bool {
	return slices.Contains(r.Verbs, "*") && slices.Contains(r.Resources, "*") && slices.Contains(r.APIGroups, "*")
}

// RbacExplain explains one cell and checks it with the API server.
func (c *Cluster) RbacExplain(sub Subject, ns, res, verb string) RbacAnswer {
	var rr RbacResource
	for _, r := range rbacResources {
		if r.Name == res {
			rr = r
		}
	}
	gs, bindings := c.grants(sub, ns)
	a, p, g := c.evaluate(sub, ns, rr, verb, gs)
	ans := RbacAnswer{Allowed: a, Partial: p}
	scope := "in namespace " + ns
	if rr.Cluster {
		scope = "cluster-wide"
	}
	via := ""
	if g != nil && g.via != "" {
		via = " (inherited via " + g.via + ")"
	}
	switch {
	case a && isWildcardRule(g.rule):
		ans.Why = g.binding + " → " + g.role + " grants every verb on every resource" + via
	case a:
		ans.Why = g.binding + " → " + g.role + " allows " + verb + " on " + res + via
	case p:
		ans.Why = g.binding + " → " + g.role + " allows " + verb + " on " + res + " only for named objects: " + strings.Join(g.rule.ResourceNames, ", ") + via
	case len(bindings) == 0:
		ans.Why = "No binding for " + sub.Kind + " " + sub.id() + " " + scope + "."
	case rr.Cluster:
		ans.Why = "Only ClusterRoleBindings grant cluster-wide resources, and none of them allows " + verb + " on " + res + ". Bindings: " + strings.Join(bindings, "; ")
	default:
		ans.Why = "None of the bindings allows " + verb + " on " + res + " " + scope + ". Bindings: " + strings.Join(bindings, "; ")
	}
	resName, subres, _ := strings.Cut(res, "/")
	as := ""
	switch sub.Kind {
	case rbacv1.UserKind:
		as = "--as " + sub.Name
	case rbacv1.GroupKind:
		as = "--as nobody --as-group " + sub.Name
	case rbacv1.ServiceAccountKind:
		as = "--as system:serviceaccount:" + sub.NS + ":" + sub.Name
	}
	nsFlag := " -n " + ns
	if rr.Cluster {
		nsFlag = ""
	}
	ans.Cmd = "kubectl auth can-i " + verb + " " + res + nsFlag + " " + as

	// Ask the API server as well. This covers authorizers other than RBAC.
	sar := &authv1.SubjectAccessReview{Spec: authv1.SubjectAccessReviewSpec{
		ResourceAttributes: &authv1.ResourceAttributes{Verb: verb, Group: rr.Group, Resource: resName, Subresource: subres},
	}}
	if !rr.Cluster {
		sar.Spec.ResourceAttributes.Namespace = ns
	}
	switch sub.Kind {
	case rbacv1.UserKind:
		sar.Spec.User = sub.Name
		sar.Spec.Groups = []string{"system:authenticated"}
	case rbacv1.GroupKind:
		sar.Spec.User = "st8ks:rbac-explorer"
		sar.Spec.Groups = []string{sub.Name}
	case rbacv1.ServiceAccountKind:
		sar.Spec.User = "system:serviceaccount:" + sub.NS + ":" + sub.Name
		sar.Spec.Groups = []string{"system:serviceaccounts", "system:serviceaccounts:" + sub.NS, "system:authenticated"}
	}
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()
	res2, err := c.cs.AuthorizationV1().SubjectAccessReviews().Create(ctx, sar, metav1.CreateOptions{})
	switch {
	case err != nil:
		ans.Verified = "The API server check is not available: " + errString(err)
	case res2.Status.Allowed == a:
		ans.Verified = "The API server agrees."
	default:
		ans.Verified = fmt.Sprintf("The API server says allowed=%t. Another authorizer decides this request. %s", res2.Status.Allowed, res2.Status.Reason)
	}
	return ans
}
