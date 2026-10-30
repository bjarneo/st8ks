//go:build integration

// Run against the local kind cluster. The test needs access to the internet:
//
//	ST8KS_IT_KUBECONFIG=~/.kube/st8ks-dev.yaml go test -count=1 -tags integration -run TestHelmInstallIntegration -v ./internal/kube/
package kube

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Masterminds/semver/v3"
	"helm.sh/helm/v3/pkg/repo"
)

func TestHelmInstallIntegration(t *testing.T) {
	kc := os.Getenv("ST8KS_IT_KUBECONFIG")
	if kc == "" {
		t.Skip("set ST8KS_IT_KUBECONFIG")
	}
	dir := helmTempEnv(t)
	rec := &recorder{counts: map[string]int{}, logs: map[string][]string{}, exec: map[string]string{}}
	m := NewManager(rec.emit, func() LoadOptions { return LoadOptions{Explicit: []string{kc}} })
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	st, err := m.Connect("kind-st8ks-dev")
	if err != nil || st.Status != StatusConnected {
		t.Fatalf("connect: %v %+v", err, st)
	}
	c := m.Current()
	defer m.Shutdown()

	const ns = "helm-install-test"
	const ociRef = "oci://ghcr.io/stefanprodan/charts/podinfo"
	releases := []string{"podinfo", "podinfo-oci"}
	uninstall := func() {
		for _, name := range releases {
			_, _ = c.HelmUninstall(ns, name, false)
		}
	}
	uninstall()
	t.Cleanup(uninstall)
	h := NewCharts()

	t.Run("repository", func(t *testing.T) {
		msg, err := h.AddRepo("podinfo", "https://stefanprodan.github.io/podinfo", "", "")
		if err != nil || !strings.HasPrefix(msg, "Added repository podinfo") {
			t.Fatalf("add = %q, %v", msg, err)
		}
		f, err := repo.LoadFile(filepath.Join(dir, "repositories.yaml"))
		if err != nil || !f.Has("podinfo") {
			t.Fatalf("repositories.yaml = %+v, %v", f, err)
		}
		c.ReloadHelmRepos()
		if r := c.table("HelmRepositories").get("repo:podinfo"); r == nil || r.C[1] != "https://stefanprodan.github.io/podinfo" {
			t.Errorf("HelmRepositories row = %+v", r)
		}
		if msg, err := h.UpdateRepos(""); err != nil || msg != "Updated repository podinfo" {
			t.Errorf("update = %q, %v", msg, err)
		}
		if _, err := h.AddRepo("broken", "https://stefanprodan.github.io/podinfo/nothing-here", "", ""); err == nil || !strings.Contains(err.Error(), "has no index.yaml") {
			t.Errorf("add of a URL without an index = %v", err)
		}
	})

	var defaults string
	t.Run("search and detail", func(t *testing.T) {
		start := time.Now()
		res, err := h.Search("podinfo", false)
		if err != nil || len(res.Hits) == 0 || res.Hits[0].Ref != "podinfo/podinfo" || res.Hits[0].Version == "" {
			t.Fatalf("search = %+v, %v", res, err)
		}
		t.Logf("first search with the index load: %s", time.Since(start).Round(time.Microsecond))
		start = time.Now()
		if _, err := h.Search("kubernetes", false); err != nil {
			t.Fatal(err)
		}
		t.Logf("cached search: %s", time.Since(start).Round(time.Microsecond))

		vs, err := h.Versions("podinfo/podinfo", false)
		if err != nil || len(vs) < 5 {
			t.Fatalf("versions = %d, %v", len(vs), err)
		}
		for i := 1; i < len(vs); i++ {
			a, b := semver.MustParse(vs[i-1].Version), semver.MustParse(vs[i].Version)
			if !a.GreaterThan(b) || vs[i].Pre {
				t.Errorf("versions out of order at %d: %s, %s", i, vs[i-1].Version, vs[i].Version)
			}
		}
		d, err := h.Detail("podinfo/podinfo", "")
		if err != nil || d.Version != vs[0].Version || !strings.Contains(d.Values, "replicaCount") || d.Readme == "" || d.Name != "podinfo" {
			t.Fatalf("detail = %+v, %v", d, err)
		}
		defaults = d.Values
		old, err := h.Detail("podinfo/podinfo", vs[len(vs)-1].Version)
		if err != nil || old.Version != vs[len(vs)-1].Version {
			t.Errorf("detail of the oldest version = %+v, %v", old, err)
		}
		for ref, want := range map[string]string{
			"podinfo/nothing": "the chart nothing is not in the repository podinfo",
			"nothing/podinfo": "the repository nothing is not in the Helm repository list",
			"podinfo":         "enter a chart as repo/chart",
		} {
			if _, err := h.Detail(ref, ""); err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("detail %s = %v. Want %q", ref, err, want)
			}
		}
		if _, err := h.Detail("podinfo/podinfo", "0.0.1"); err == nil || !strings.Contains(err.Error(), "version 0.0.1 of podinfo/podinfo does not exist") {
			t.Errorf("detail of a missing version = %v", err)
		}
	})

	t.Run("dry run and install from the repository", func(t *testing.T) {
		values := strings.Replace(defaults, "replicaCount: 1", "replicaCount: 2", 1)
		spec := InstallSpec{Ref: "podinfo/podinfo", Name: "podinfo", NS: ns, CreateNS: true, Values: values, Dry: true}
		res, err := c.HelmInstall(h, spec)
		if err != nil || !strings.Contains(res.Manifest, "kind: Deployment") || !strings.Contains(res.Manifest, "replicas: 2") || res.Resources < 2 || len(res.Checks) > 0 {
			t.Fatalf("dry run = %+v, %v", res, err)
		}
		t.Logf("dry run: %d resources, %d hooks, note %q", res.Resources, res.Hooks, res.Note)
		if hist, err := c.HelmReleases(); err == nil {
			for _, r := range hist {
				if r.NS == ns && r.Name == "podinfo" {
					t.Fatalf("the dry run stored a release: %+v", r)
				}
			}
		}

		spec.Dry = false
		res, err = c.HelmInstall(h, spec)
		if err != nil || res.Status != "deployed" || res.Notes == "" || res.Revision != 1 {
			t.Fatalf("install = %+v, %v", res, err)
		}
		found := false
		rels, _ := c.HelmReleases()
		for _, r := range rels {
			if r.NS == ns && r.Name == "podinfo" && r.Status == "deployed" {
				found = true
			}
		}
		if !found {
			t.Errorf("podinfo is not in the releases: %+v", rels)
		}
		d, err := c.HelmDetail(ns, "podinfo")
		if err != nil || strings.TrimSpace(d.Values) != "replicaCount: 2" {
			t.Errorf("stored values = %q, %v. Want only the changed value", d.Values, err)
		}

		spec.Dry = true
		if _, err := c.HelmInstall(h, spec); err == nil || !strings.Contains(err.Error(), "the release name podinfo is in use in the namespace helm-install-test") {
			t.Errorf("dry run with a used name = %v", err)
		}
	})

	t.Run("install from the OCI registry", func(t *testing.T) {
		vs, err := h.Versions(ociRef, false)
		if err != nil || len(vs) < 5 {
			t.Fatalf("OCI versions = %d, %v", len(vs), err)
		}
		d, err := h.Detail(ociRef, "")
		if err != nil || d.Version != vs[0].Version || !strings.Contains(d.Values, "replicaCount") {
			t.Fatalf("OCI detail = %+v, %v", d, err)
		}
		if _, err := h.Detail(ociRef, "0.0.1"); err == nil || !strings.Contains(err.Error(), "version 0.0.1 of "+ociRef+" does not exist") {
			t.Errorf("OCI detail of a missing version = %v", err)
		}
		if _, err := h.Versions("oci://ghcr.io/stefanprodan/charts/st8ks-no-such-chart", false); err == nil ||
			!(strings.Contains(err.Error(), "refused access") || strings.Contains(err.Error(), "is not in the registry")) {
			t.Errorf("OCI versions of a missing chart = %v", err)
		}

		// The namespace exists now, so the API server checks the objects.
		bad := InstallSpec{Ref: ociRef, Name: "podinfo-oci", NS: ns, Values: "replicaCount: -1\n", Dry: true}
		res, err := c.HelmInstall(h, bad)
		if err == nil && len(res.Checks) == 0 {
			t.Errorf("the dry run accepted replicaCount -1: %+v", res)
		} else {
			t.Logf("dry run with replicaCount -1: %v %v", err, res.Checks)
		}

		spec := InstallSpec{Ref: ociRef, Version: d.Version, Name: "podinfo-oci", NS: ns, Values: d.Values, Wait: true, Atomic: true, Timeout: 240}
		res, err = c.HelmInstall(h, spec)
		if err != nil || res.Status != "deployed" || res.Version != d.Version {
			t.Fatalf("OCI install = %+v, %v", res, err)
		}
	})

	t.Run("uninstall", func(t *testing.T) {
		for _, name := range releases {
			if msg, err := c.HelmUninstall(ns, name, false); err != nil {
				t.Errorf("uninstall %s = %q, %v", name, msg, err)
			}
		}
		rels, _ := c.HelmReleases()
		for _, r := range rels {
			if r.NS == ns {
				t.Errorf("release left after uninstall: %+v", r)
			}
		}
	})
}
