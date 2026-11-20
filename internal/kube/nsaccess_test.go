package kube

import (
	"context"
	"slices"
	"testing"

	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
)

func TestRulesAllow(t *testing.T) {
	all := []string{"get", "list", "watch"}
	cases := []struct {
		name   string
		rules  []authv1.ResourceRule
		group  string
		res    string
		verbs  []string
		expect bool
	}{
		{"exact", []authv1.ResourceRule{{Verbs: all, APIGroups: []string{""}, Resources: []string{"pods"}}}, "", "pods", []string{"list", "watch"}, true},
		{"wildcards", []authv1.ResourceRule{{Verbs: []string{"*"}, APIGroups: []string{"*"}, Resources: []string{"*"}}}, "apps", "deployments", []string{"list", "watch"}, true},
		{"other group", []authv1.ResourceRule{{Verbs: all, APIGroups: []string{"apps"}, Resources: []string{"pods"}}}, "", "pods", []string{"list"}, false},
		{"missing verb", []authv1.ResourceRule{{Verbs: []string{"list"}, APIGroups: []string{""}, Resources: []string{"pods"}}}, "", "pods", []string{"list", "watch"}, false},
		{"verbs from two rules", []authv1.ResourceRule{
			{Verbs: []string{"list"}, APIGroups: []string{""}, Resources: []string{"pods"}},
			{Verbs: []string{"watch"}, APIGroups: []string{""}, Resources: []string{"pods"}},
		}, "", "pods", []string{"list", "watch"}, true},
		{"resource names cannot list", []authv1.ResourceRule{{Verbs: all, APIGroups: []string{""}, Resources: []string{"pods"}, ResourceNames: []string{"web"}}}, "", "pods", []string{"list"}, false},
		{"subresource only", []authv1.ResourceRule{{Verbs: all, APIGroups: []string{""}, Resources: []string{"pods/log"}}}, "", "pods", []string{"list"}, false},
	}
	for _, tc := range cases {
		if got := rulesAllow(tc.rules, tc.group, tc.res, tc.verbs); got != tc.expect {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.expect)
		}
	}
}

// restrictedClient returns a fake clientset whose user may list and watch
// pods only in the namespaces of allowed, and Secrets nowhere.
func restrictedClient(names []string, allowed map[string]bool, incomplete map[string]bool, ssar func(ns string) bool) *fake.Clientset {
	var objs []runtime.Object
	for _, n := range names {
		objs = append(objs, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: n}})
	}
	cs := fake.NewClientset(objs...)
	cs.PrependReactor("create", "selfsubjectrulesreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		r := a.(k8stesting.CreateAction).GetObject().(*authv1.SelfSubjectRulesReview)
		ns := r.Spec.Namespace
		r.Status.Incomplete = incomplete[ns]
		if allowed[ns] {
			r.Status.ResourceRules = []authv1.ResourceRule{{Verbs: []string{"get", "list", "watch"}, APIGroups: []string{""}, Resources: []string{"pods", "events"}}}
		}
		return true, r, nil
	})
	cs.PrependReactor("create", "selfsubjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		r := a.(k8stesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
		r.Status.Allowed = ssar != nil && ssar(r.Spec.ResourceAttributes.Namespace)
		return true, r, nil
	})
	return cs
}

func TestPermittedNamespaces(t *testing.T) {
	names := []string{"aibot", "default", "kube-system", "qdrant", "redis"}
	allowed := map[string]bool{"aibot": true, "qdrant": true, "redis": true}
	c := &Cluster{cs: restrictedClient(names, allowed, nil, nil), defaultNS: "redis"}
	got, checked := c.permittedNamespaces(context.Background(), "", "pods", "list", "watch")
	if want := []string{"redis", "aibot", "qdrant"}; !slices.Equal(got, want) {
		t.Fatalf("namespaces %v, want %v (the context namespace first)", got, want)
	}
	if len(checked) != len(names) || checked[0] != "redis" {
		t.Fatalf("checked %v", checked)
	}
	if got, _ := c.permittedNamespaces(context.Background(), "", "secrets", "list"); len(got) != 0 {
		t.Fatalf("secrets allowed in %v, want none", got)
	}
}

func TestPermittedNamespacesIncompleteRules(t *testing.T) {
	names := []string{"a", "b"}
	// The rules review knows nothing, as with a webhook authorizer. The
	// access review allows namespace b.
	incomplete := map[string]bool{"a": true, "b": true}
	c := &Cluster{cs: restrictedClient(names, nil, incomplete, func(ns string) bool { return ns == "b" })}
	got, _ := c.permittedNamespaces(context.Background(), "", "pods", "list", "watch")
	if want := []string{"b"}; !slices.Equal(got, want) {
		t.Fatalf("namespaces %v, want %v", got, want)
	}
}

func TestPermittedNamespacesNoNamespaceList(t *testing.T) {
	cs := restrictedClient(nil, map[string]bool{"team-a": true}, nil, nil)
	cs.PrependReactor("list", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errForbidden("namespaces")
	})
	c := &Cluster{cs: cs, defaultNS: "team-a"}
	got, checked := c.permittedNamespaces(context.Background(), "", "pods", "list", "watch")
	if !slices.Equal(got, []string{"team-a"}) || !slices.Equal(checked, []string{"team-a"}) {
		t.Fatalf("got %v, checked %v; want the context namespace", got, checked)
	}

	c = &Cluster{cs: cs}
	if got, checked := c.permittedNamespaces(context.Background(), "", "pods", "list", "watch"); len(got) != 0 || len(checked) != 0 {
		t.Fatalf("got %v, checked %v; want nothing without a context namespace", got, checked)
	}
}

func TestFallbackNote(t *testing.T) {
	k := &Kind{Label: "Pods"}
	cases := []struct {
		live      []string
		permitted int
		checked   []string
		want      string
	}{
		{nil, 0, nil, "Your credentials cannot list Pods in all namespaces. Set a namespace on the context to show one."},
		{nil, 0, []string{"default"}, "Your credentials cannot list Pods in namespace default."},
		{nil, 0, []string{"a", "b"}, "Your credentials cannot list Pods in any namespace."},
		{[]string{"aibot"}, 1, []string{"a", "aibot"}, "Your credentials cannot list Pods in all namespaces. Showing namespace aibot only."},
		{[]string{"a", "b"}, 2, []string{"a", "b"}, "Your credentials cannot list Pods in all namespaces. Showing 2 namespaces: a, b."},
		{[]string{"a", "b", "c", "d", "e", "f"}, 6, []string{"a"}, "Your credentials cannot list Pods in all namespaces. Showing 6 namespaces: a, b, c, d and 2 more."},
		{[]string{"a", "b"}, 3, []string{"a"}, "Your credentials cannot list Pods in all namespaces. Showing 2 of 3 namespaces: a, b."},
	}
	for _, tc := range cases {
		if got := fallbackNote(k, tc.live, tc.permitted, tc.checked); got != tc.want {
			t.Errorf("fallbackNote(%v, %d, %v)\n got %q\nwant %q", tc.live, tc.permitted, tc.checked, got, tc.want)
		}
	}
}

func TestMultiIndex(t *testing.T) {
	newPart := func(ns string, pods ...string) *part {
		inf := cache.NewSharedIndexInformer(nil, &corev1.Pod{}, 0, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
		for _, name := range pods {
			_ = inf.GetIndexer().Add(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}})
		}
		return &part{ns: ns, inf: inf, stop: make(chan struct{})}
	}
	a, b, dead := newPart("a", "web", "db"), newPart("b", "web"), newPart("c", "gone")
	dead.dead.Store(true)
	m := multiIndex{a, b, dead}

	if n := len(m.List()); n != 3 {
		t.Fatalf("List returned %d objects, want 3", n)
	}
	if o, ok, _ := m.GetByKey("b/web"); !ok || o.(*corev1.Pod).Namespace != "b" {
		t.Fatalf("GetByKey b/web: %v %v", o, ok)
	}
	if _, ok, _ := m.GetByKey("c/gone"); ok {
		t.Fatal("GetByKey found an object of a failed part")
	}
	if objs, _ := m.ByIndex(cache.NamespaceIndex, "a"); len(objs) != 2 {
		t.Fatalf("ByIndex a returned %d objects, want 2", len(objs))
	}

	// A fallback source with no namespace reads as watched and empty.
	c := &Cluster{sources: map[string]*source{"Pods": {kind: &Kind{Name: "Pods"}, fallback: true}}}
	if ix := c.indexer("Pods"); ix == nil || len(ix.List()) != 0 {
		t.Fatalf("indexer of an empty fallback source = %v", ix)
	}
}

func errForbidden(resource string) error {
	return apierrors.NewForbidden(schema.GroupResource{Resource: resource}, "", nil)
}
