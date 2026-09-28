package kube

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func writeKubeconfig(t *testing.T, path string, ctxs map[string]string) {
	t.Helper()
	body := "apiVersion: v1\nkind: Config\nclusters:\n"
	for name, server := range ctxs {
		body += "- name: " + name + "\n  cluster:\n    server: " + server + "\n    certificate-authority: ca.crt\n"
	}
	body += "users:\n- name: u\n  user:\n    token: t\ncontexts:\n"
	for name := range ctxs {
		body += "- name: " + name + "\n  context:\n    cluster: " + name + "\n    user: u\n"
	}
	body += "current-context: \n"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func names(r loadResult) []string {
	var out []string
	for n := range r.entries {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func TestLoadKubeconfigs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	primary := filepath.Join(home, "work", "a.yaml")
	writeKubeconfig(t, primary, map[string]string{"prod": "https://prod:6443", "dev": "https://dev:6443"})
	extra := filepath.Join(home, "other", "b.yaml")
	writeKubeconfig(t, extra, map[string]string{"dev": "https://dev-b:6443", "staging": "https://staging:6443"})
	// Same server and credentials as the primary dev context, so it is a
	// duplicate. The relative CA path resolves to the same file.
	dup := filepath.Join(home, "work", "copy.yaml")
	writeKubeconfig(t, dup, map[string]string{"dev": "https://dev:6443"})
	writeKubeconfig(t, filepath.Join(home, ".kube", "clusters", "k3s.yaml"), map[string]string{"default": "https://k3s:6443"})
	writeKubeconfig(t, filepath.Join(home, ".kube", "cache", "junk.yaml"), map[string]string{"junk": "https://junk:6443"})
	folder := filepath.Join(home, "configs")
	writeKubeconfig(t, filepath.Join(folder, "edge.yaml"), map[string]string{"default": "https://edge:6443"})
	if err := os.WriteFile(filepath.Join(home, ".kube", "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", primary)

	r := loadKubeconfigs(LoadOptions{Extra: []string{extra, folder, dup}, Scan: true})
	got := names(r)
	want := []string{"default", "default@k3s", "dev", "dev@b", "prod", "staging"}
	if len(got) != len(want) {
		t.Fatalf("contexts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("contexts = %v, want %v", got, want)
		}
	}
	if e := r.entries["dev@b"]; e.orig != "dev" || e.source != extra {
		t.Errorf("dev@b = %+v", e)
	}
	ca := r.entries["prod"].raw.Clusters["prod"].CertificateAuthority
	if !filepath.IsAbs(ca) || filepath.Dir(ca) != filepath.Dir(primary) {
		t.Errorf("certificate path not resolved against the file: %s", ca)
	}
	kinds := map[string]string{}
	for _, s := range r.sources {
		kinds[filepath.Base(s.Path)] = s.Kind
	}
	if kinds["a.yaml"] != "env" || kinds["b.yaml"] != "added" || kinds["configs"] != "folder" || kinds["k3s.yaml"] != "scan" {
		t.Errorf("sources = %+v", r.sources)
	}
	if _, ok := kinds["junk.yaml"]; ok {
		t.Error("the cache folder must not be scanned")
	}

	// --kubeconfig reads only the given files.
	only := loadKubeconfigs(LoadOptions{Explicit: []string{extra}, Extra: []string{folder}, Scan: true})
	if got := names(only); len(got) != 2 || got[0] != "dev" || got[1] != "staging" {
		t.Errorf("explicit contexts = %v", got)
	}
}
