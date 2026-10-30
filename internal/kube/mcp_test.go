package kube

import (
	"testing"
	"time"
)

func TestResolveKind(t *testing.T) {
	c := &Cluster{builtin: builtinKinds(), custom: []*Kind{{Name: "certificates.cert-manager.io", Kind: "Certificate", Group: "cert-manager.io",
		Resource: "certificates", Short: "cert", Namespaced: true, Custom: true}}}
	for in, want := range map[string]string{
		"Pods": "Pods", "pod": "Pods", "po": "Pods", "PODS": "Pods", "deploy": "Deployments", "deployments.apps": "Deployments",
		"Deployment": "Deployments", "deployment.apps": "Deployments", "svc": "Services", "cm": "ConfigMaps", "secret": "Secrets",
		"no": "Nodes", "ingresses.networking.k8s.io": "Ingresses", "cert": "certificates.cert-manager.io",
		"certificates.cert-manager.io": "certificates.cert-manager.io", "Certificate": "certificates.cert-manager.io",
	} {
		k, ok := c.ResolveKind(in)
		if !ok || k.Name != want {
			t.Errorf("ResolveKind(%q) = %q %t, want %q", in, k.Name, ok, want)
		}
	}
	for _, in := range []string{"", "nope", "HelmRepositories", "deployments.batch"} {
		if k, ok := c.ResolveKind(in); ok {
			t.Errorf("ResolveKind(%q) = %q, want no match", in, k.Name)
		}
	}
}

func TestRbacResource(t *testing.T) {
	c := &Cluster{builtin: builtinKinds()}
	for in, want := range map[string]RbacResource{
		"pods/log":          {Name: "pods/log"},
		"nodes":             {Name: "nodes", Cluster: true},
		"deployments.apps":  {Name: "deployments", Group: "apps"},
		"deployments/scale": {Name: "deployments/scale", Group: "apps"},
		"persistentvolumes": {Name: "persistentvolumes", Cluster: true},
		"jobs.batch":        {Name: "jobs", Group: "batch"},
		"widgets.example":   {Name: "widgets", Group: "example"},
	} {
		if got := c.rbacResource(in); got != want {
			t.Errorf("rbacResource(%q) = %+v, want %+v", in, got, want)
		}
	}
}

func TestRowCells(t *testing.T) {
	var pods *Kind
	for _, k := range builtinKinds() {
		if k.Name == "Pods" {
			pods = k
		}
	}
	r := &Row{U: "u1", N: "shop", M: "web-1", T: time.Now().Add(-2 * time.Hour).Unix(), C: []string{"", "", "Running", "1/1", "3", "", "", "node-a", ""},
		X: []int64{1000, 512 << 20, 3}}
	got := rowCells(pods, r, map[string][2]int64{"u1": {250, 128 << 20}})
	want := []string{"web-1", "shop", "Running", "1/1", "3", "250m (25%)", "128Mi (25%)", "node-a", "2h"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("rowCells = %q, want %q", got, want)
		}
	}
	if got := rowCells(pods, r, nil); got[5] != "—" {
		t.Errorf("CPU without metrics = %q", got[5])
	}
}
