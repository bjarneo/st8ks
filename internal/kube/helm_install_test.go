package kube

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/repo"
)

// helmTempEnv points the Helm config at a temporary folder, so a test never
// changes the Helm config of the user.
func helmTempEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HELM_REPOSITORY_CONFIG", filepath.Join(dir, "repositories.yaml"))
	t.Setenv("HELM_REPOSITORY_CACHE", filepath.Join(dir, "cache"))
	t.Setenv("HELM_REGISTRY_CONFIG", filepath.Join(dir, "registry", "config.json"))
	return dir
}

func TestParseChartRef(t *testing.T) {
	ok := []struct {
		in   string
		want chartRef
	}{
		{"podinfo/podinfo", chartRef{repo: "podinfo", name: "podinfo", base: "podinfo/podinfo"}},
		{"  bitnami/nginx ", chartRef{repo: "bitnami", name: "nginx", base: "bitnami/nginx"}},
		{"oci://ghcr.io/stefanprodan/charts/podinfo", chartRef{oci: true, host: "ghcr.io", name: "podinfo", base: "oci://ghcr.io/stefanprodan/charts/podinfo"}},
		{"oci://ghcr.io/stefanprodan/charts/podinfo:6.5.0", chartRef{oci: true, host: "ghcr.io", name: "podinfo", base: "oci://ghcr.io/stefanprodan/charts/podinfo", tag: "6.5.0"}},
		{"oci://localhost:5000/podinfo", chartRef{oci: true, host: "localhost:5000", name: "podinfo", base: "oci://localhost:5000/podinfo"}},
		{"oci://localhost:5000/a/b:1.0.0-rc.1", chartRef{oci: true, host: "localhost:5000", name: "b", base: "oci://localhost:5000/a/b", tag: "1.0.0-rc.1"}},
	}
	for _, c := range ok {
		got, err := parseChartRef(c.in)
		if err != nil || got != c.want {
			t.Errorf("parseChartRef(%q) = %+v, %v. Want %+v", c.in, got, err, c.want)
		}
	}
	for _, in := range []string{"", "podinfo", "/podinfo", "podinfo/", "a/b/c", "a/b:1.0", "https://example.com/x", "oci://", "oci://ghcr.io", "oci://ghcr.io/", "oci://ghcr.io/a/", "oci://ghcr.io/a:", "oci://ghcr.io/a@sha256:abc", "oci://ghcr.io//a"} {
		if got, err := parseChartRef(in); err == nil {
			t.Errorf("parseChartRef(%q) = %+v. Want an error", in, got)
		}
	}
	if s := (chartRef{oci: true, base: "oci://r/c", tag: "1.0.0"}).String(); s != "oci://r/c:1.0.0" {
		t.Errorf("String() = %q", s)
	}
}

func TestSortVersions(t *testing.T) {
	vs := []string{"1.2.0", "bad", "1.10.0", "1.2.0-rc.1", "2.0.0", "v1.3.0", "1.2.0-beta.2", "also-bad"}
	sortVersions(vs, func(s string) string { return s })
	want := []string{"2.0.0", "1.10.0", "v1.3.0", "1.2.0", "1.2.0-rc.1", "1.2.0-beta.2", "bad", "also-bad"}
	if !reflect.DeepEqual(vs, want) {
		t.Errorf("sortVersions = %v. Want %v", vs, want)
	}
	for v, pre := range map[string]bool{"1.0.0": false, "1.0.0-rc.1": true, "1.0.0+build.5": false, "1.0.0-alpha+b": true, "bad": false} {
		if isPrerelease(v) != pre {
			t.Errorf("isPrerelease(%q) = %v", v, !pre)
		}
	}
}

func cv(name, version, desc string, keywords ...string) *repo.ChartVersion {
	return &repo.ChartVersion{Metadata: &chart.Metadata{Name: name, Version: version, Description: desc, Keywords: keywords, AppVersion: "app-" + version}}
}

func testIndex(repoName string, charts map[string]repo.ChartVersions) *repoIndex {
	ix := &repoIndex{repo: repoName, byName: map[string]*indexedChart{}}
	for name, cvs := range charts {
		c := newIndexedChart(repoName, name, cvs)
		ix.charts = append(ix.charts, c)
		ix.byName[name] = c
	}
	return ix
}

func TestSearchRanking(t *testing.T) {
	a := testIndex("alpha", map[string]repo.ChartVersions{
		"nginx":         {cv("nginx", "1.0.0", "A web server"), cv("nginx", "2.0.0-rc.1", "A web server")},
		"nginx-ingress": {cv("nginx-ingress", "4.0.0", "Ingress controller")},
		"ingress-nginx": {cv("ingress-nginx", "4.1.0", "Ingress controller")},
		"apache":        {cv("apache", "9.0.0", "The Apache HTTP server", "nginx-alternative", "http")},
		"proxy":         {cv("proxy", "1.0.0", "A proxy", "nginx")},
		"cache":         {cv("cache", "1.0.0", "Caches pages in front of nginx")},
		"preview":       {cv("preview", "0.1.0-alpha.1", "nginx preview build")},
	})
	b := testIndex("beta", map[string]repo.ChartVersions{
		"nginx": {cv("nginx", "3.0.0", "Another web server")},
		"redis": {cv("redis", "17.0.0", "Key value store", "database", "cache")},
	})
	ixs := []*repoIndex{a, b}
	names := func(hits []ChartHit) []string {
		out := make([]string, len(hits))
		for i, h := range hits {
			out[i] = h.Ref
		}
		return out
	}

	hits, total := searchIndexes(ixs, "nginx", false, 100)
	want := []string{"alpha/nginx", "beta/nginx", "alpha/nginx-ingress", "alpha/ingress-nginx", "alpha/proxy", "alpha/apache", "alpha/cache"}
	if got := names(hits); !reflect.DeepEqual(got, want) || total != len(want) {
		t.Errorf("search nginx = %v (%d). Want %v", got, total, want)
	}
	// The stable version shows, not the newer prerelease.
	if hits[0].Version != "1.0.0" || hits[0].AppVersion != "app-1.0.0" {
		t.Errorf("alpha/nginx hit = %+v", hits[0])
	}

	hits, _ = searchIndexes(ixs, "NGINX", true, 100)
	if hits[0].Version != "2.0.0-rc.1" || !strings.Contains(strings.Join(names(hits), ","), "alpha/preview") {
		t.Errorf("devel search = %v, first %+v", names(hits), hits[0])
	}

	hits, total = searchIndexes(ixs, "nginx", false, 2)
	if len(hits) != 2 || total != 7 {
		t.Errorf("limit: %d hits, total %d", len(hits), total)
	}

	for q, want := range map[string][]string{
		"beta/":          {"beta/nginx", "beta/redis"},
		"alpha/nginx":    {"alpha/nginx", "alpha/nginx-ingress"},
		"key value":      {"beta/redis"},
		"ingress   ctrl": {},
		"database":       {"beta/redis"},
		"beta":           {"beta/nginx", "beta/redis"},
		"web server":     {"alpha/nginx", "beta/nginx"},
	} {
		hits, _ := searchIndexes(ixs, q, false, 100)
		if got := names(hits); !reflect.DeepEqual(got, want) {
			t.Errorf("search %q = %v. Want %v", q, got, want)
		}
	}

	hits, total = searchIndexes(ixs, "", false, 100)
	if total != 8 || hits[0].Ref != "alpha/apache" {
		t.Errorf("empty query = %v (%d)", names(hits), total)
	}
}

func TestPickVersion(t *testing.T) {
	r := chartRef{base: "a/b"}
	vs := []ChartVersionInfo{{Version: "2.0.0-rc.1", Pre: true}, {Version: "1.5.0"}, {Version: "1.4.2"}, {Version: "0.9.0"}}
	for in, want := range map[string]string{"": "1.5.0", "1.4.2": "1.4.2", "2.0.0-rc.1": "2.0.0-rc.1", "~1.4": "1.4.2", "<1": "0.9.0"} {
		if got, err := pickVersion(r, vs, in); err != nil || got != want {
			t.Errorf("pickVersion(%q) = %q, %v. Want %q", in, got, err, want)
		}
	}
	if _, err := pickVersion(r, vs, "3.0.0"); err == nil || !strings.Contains(err.Error(), "version 3.0.0 of a/b does not exist") {
		t.Errorf("missing version error = %v", err)
	}
	if got, _ := pickVersion(r, vs[:1], ""); got != "2.0.0-rc.1" {
		t.Errorf("only prereleases = %q", got)
	}
}

func TestValueOverrides(t *testing.T) {
	defaults := map[string]any{
		"replicaCount": float64(1),
		"image":        map[string]any{"repository": "nginx", "tag": "1.27"},
		"ports":        []any{float64(80)},
		"extra":        "x",
	}
	vals := map[string]any{
		"replicaCount": float64(1),
		"image":        map[string]any{"repository": "nginx", "tag": "1.28"},
		"ports":        []any{float64(80)},
		"extra":        nil,
		"new":          map[string]any{"a": true},
	}
	want := map[string]any{"image": map[string]any{"tag": "1.28"}, "extra": nil, "new": map[string]any{"a": true}}
	if got := valueOverrides(vals, defaults); !reflect.DeepEqual(got, want) {
		t.Errorf("valueOverrides = %v. Want %v", got, want)
	}
	if got := valueOverrides(defaults, defaults); len(got) != 0 {
		t.Errorf("unchanged defaults = %v. Want none", got)
	}
}

func TestRedactURLs(t *testing.T) {
	in := `failed to fetch https://user:s3cret@charts.example.com/index.yaml : 401, and oci://bob:pw@reg.io/x`
	got := redactURLs(in)
	if strings.Contains(got, "s3cret") || strings.Contains(got, "pw@") || !strings.Contains(got, "https://charts.example.com/index.yaml") {
		t.Errorf("redactURLs = %q", got)
	}
}

const testIndexYAML = `apiVersion: v1
entries:
  podinfo:
  - name: podinfo
    version: 6.5.0
    appVersion: 6.5.0
    description: Podinfo Helm chart for Kubernetes
    keywords: [podinfo, demo]
    urls: [https://example.com/podinfo-6.5.0.tgz]
  - name: podinfo
    version: 6.6.0-rc.1
    appVersion: 6.6.0
    description: Podinfo Helm chart for Kubernetes
    urls: [https://example.com/podinfo-6.6.0-rc.1.tgz]
  - name: podinfo
    version: 6.10.1
    appVersion: 6.10.1
    description: Podinfo Helm chart for Kubernetes
    urls: [https://example.com/podinfo-6.10.1.tgz]
generated: "2026-01-01T00:00:00Z"
`

func writeTestRepo(t *testing.T, dir string, names ...string) {
	t.Helper()
	f := repo.NewFile()
	for _, n := range names {
		f.Update(&repo.Entry{Name: n, URL: "https://user:pw@example.com/" + n})
		if err := os.MkdirAll(filepath.Join(dir, "cache"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cache", n+"-index.yaml"), []byte(testIndexYAML), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cache", n+"-charts.txt"), []byte("podinfo\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeRepoFile(filepath.Join(dir, "repositories.yaml"), f); err != nil {
		t.Fatal(err)
	}
}

func TestChartsIndexCache(t *testing.T) {
	dir := helmTempEnv(t)
	writeTestRepo(t, dir, "one", "two")
	h := NewCharts()

	res, err := h.Search("podinfo", false)
	if err != nil || res.Total != 2 || res.Repos != 2 || res.Hits[0].Version != "6.10.1" || res.Hits[0].Ref != "one/podinfo" {
		t.Fatalf("search = %+v, %v", res, err)
	}
	path := filepath.Join(dir, "cache", "one-index.yaml")
	first := h.indexes[path]
	if _, err := h.Search("demo", false); err != nil || h.indexes[path] != first {
		t.Fatalf("the second search parsed the index again: %v", err)
	}

	// A changed file loads again.
	changed := strings.Replace(testIndexYAML, "6.10.1", "6.11.0", 2)
	if err := os.WriteFile(path, []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	res, _ = h.Search("podinfo", false)
	if h.indexes[path] == first || res.Hits[0].Version != "6.11.0" {
		t.Fatalf("the changed index did not load again: %+v", res.Hits)
	}

	vs, err := h.Versions("two/podinfo", false)
	if err != nil || len(vs) != 2 || vs[0].Version != "6.10.1" || vs[1].Version != "6.5.0" {
		t.Errorf("versions = %+v, %v", vs, err)
	}
	vs, _ = h.Versions("two/podinfo", true)
	if len(vs) != 3 || vs[1].Version != "6.6.0-rc.1" || !vs[1].Pre {
		t.Errorf("versions with prereleases = %+v", vs)
	}
	if _, err := h.Versions("nope/podinfo", false); err == nil || !strings.Contains(err.Error(), "the repository nope is not in the Helm repository list") {
		t.Errorf("unknown repository error = %v", err)
	}
	if _, err := h.Versions("one/missing", false); err == nil || !strings.Contains(err.Error(), "the chart missing is not in the repository one") {
		t.Errorf("unknown chart error = %v", err)
	}
	if _, err := h.Detail("one/podinfo", "9.9.9"); err == nil || !strings.Contains(err.Error(), "version 9.9.9 of one/podinfo does not exist") {
		t.Errorf("unknown version error = %v", err)
	}

	repos, err := h.Repos()
	if err != nil || len(repos) != 2 || repos[0].Charts != 1 || repos[0].URL != "https://example.com/one" || repos[0].Updated == 0 {
		t.Errorf("repos = %+v, %v", repos, err)
	}

	if msg, err := h.RemoveRepo("one"); err != nil || msg != "Removed repository one" {
		t.Fatalf("remove = %q, %v", msg, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the cached index of one still exists: %v", err)
	}
	f, err := repo.LoadFile(filepath.Join(dir, "repositories.yaml"))
	if err != nil || len(f.Repositories) != 1 || f.Repositories[0].Name != "two" || f.Repositories[0].URL != "https://user:pw@example.com/two" {
		t.Errorf("repositories.yaml after remove = %+v, %v", f, err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "repositories.yaml")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("repositories.yaml mode = %v, %v", fi.Mode(), err)
	}
	if _, err := h.RemoveRepo("one"); err == nil {
		t.Error("a second remove did not fail")
	}
	if _, err := h.UpdateRepos("one"); err == nil || !strings.Contains(err.Error(), "not in the Helm repository list") {
		t.Errorf("update of an unknown repository = %v", err)
	}
	if _, err := h.AddRepo("oci", "oci://ghcr.io/x", "", ""); err == nil || !strings.Contains(err.Error(), "OCI registry has no index") {
		t.Errorf("add of an OCI URL = %v", err)
	}
	if _, err := h.AddRepo("two", "https://other.example.com", "", ""); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("add of a used name = %v", err)
	}
}

func TestWriteRepoFileFollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles", "repositories.yaml")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("apiVersion: v1\nrepositories: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "repositories.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("no symbolic links:", err)
	}
	f := repo.NewFile()
	f.Update(&repo.Entry{Name: "x", URL: "https://example.com"})
	if err := writeRepoFile(link, f); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was replaced: %v %v", fi, err)
	}
	if got, err := repo.LoadFile(target); err != nil || !got.Has("x") {
		t.Errorf("target = %+v, %v", got, err)
	}
}
