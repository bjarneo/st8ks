package kube

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
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

func TestHiddenKubeconfigs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KUBECONFIG", "")
	def := filepath.Join(home, ".kube", "config")
	writeKubeconfig(t, def, map[string]string{"main": "https://main:6443"})
	scanned := filepath.Join(home, ".kube", "lab.yaml")
	writeKubeconfig(t, scanned, map[string]string{"lab": "https://lab:6443"})
	folder := filepath.Join(home, "configs")
	inFolder := filepath.Join(folder, "edge.yaml")
	writeKubeconfig(t, inFolder, map[string]string{"edge": "https://edge:6443"})

	r := loadKubeconfigs(LoadOptions{Extra: []string{folder}, Hidden: []string{def, scanned, inFolder}, Scan: true})
	if got := names(r); len(got) != 0 {
		t.Fatalf("hidden files gave contexts %v", got)
	}
	hidden := 0
	for _, s := range r.sources {
		if s.Hidden {
			hidden++
			if !s.Removable || s.Contexts != 0 {
				t.Errorf("hidden source = %+v", s)
			}
		}
	}
	if hidden != 3 {
		t.Errorf("hidden sources = %d, want 3: %+v", hidden, r.sources)
	}
	for _, w := range r.watch {
		if w == def || w == scanned || w == inFolder {
			t.Errorf("hidden file %s is watched", w)
		}
	}

	// A file from --kubeconfig is always read.
	only := loadKubeconfigs(LoadOptions{Explicit: []string{scanned}, Hidden: []string{scanned}})
	if got := names(only); len(got) != 1 || got[0] != "lab" || only.sources[0].Removable {
		t.Errorf("explicit hidden file: contexts %v, sources %+v", got, only.sources)
	}
}

func TestDeleteContext(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	primary := filepath.Join(home, "a.yaml")
	writeKubeconfig(t, primary, map[string]string{"prod": "https://prod:6443", "dev": "https://dev:6443"})
	extra := filepath.Join(home, "b.yaml")
	writeKubeconfig(t, extra, map[string]string{"dev": "https://dev-b:6443"})
	t.Setenv("KUBECONFIG", primary)
	m := NewManager(func(string, any) {}, func() LoadOptions { return LoadOptions{Extra: []string{extra}} })
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}

	// dev@b is the context "dev" in b.yaml. The dev context in a.yaml stays.
	file, err := m.DeleteContext("dev@b")
	if err != nil || file != extra {
		t.Fatalf("DeleteContext = %q, %v", file, err)
	}
	cfg, err := clientcmd.LoadFromFile(extra)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Contexts) != 0 || cfg.Clusters["dev"] == nil || cfg.AuthInfos["u"] == nil {
		t.Errorf("b.yaml after delete: contexts %v, clusters %v, users %v", cfg.Contexts, cfg.Clusters, cfg.AuthInfos)
	}
	if cfg.Clusters["dev"].CertificateAuthority != "ca.crt" {
		t.Errorf("relative certificate path changed to %s", cfg.Clusters["dev"].CertificateAuthority)
	}
	var got []string
	for _, c := range m.Contexts() {
		got = append(got, c.Name)
	}
	if len(got) != 2 || got[0] != "dev" || got[1] != "prod" {
		t.Errorf("contexts after delete = %v", got)
	}

	// A context from the merged primary list is removed from its own file.
	if file, err := m.DeleteContext("prod"); err != nil || file != primary {
		t.Fatalf("DeleteContext(prod) = %q, %v", file, err)
	}
	if cfg, _ := clientcmd.LoadFromFile(primary); cfg.Contexts["prod"] != nil || cfg.Contexts["dev"] == nil {
		t.Errorf("a.yaml contexts after delete = %v", cfg.Contexts)
	}
	if _, err := m.DeleteContext("missing"); err == nil {
		t.Error("deleting an unknown context must fail")
	}
}
