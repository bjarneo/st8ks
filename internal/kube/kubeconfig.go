package kube

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// LoadOptions selects the kubeconfig files to read.
type LoadOptions struct {
	// Explicit files from the --kubeconfig flag. When set, only these files
	// are read, as kubectl does.
	Explicit []string
	// Extra files and folders that the user added in the app.
	Extra []string
	// Hidden files are not read. They come from the default location, a
	// folder or the scan, and the user removed them in the app.
	Hidden []string
	// Scan reads every other kubeconfig file in ~/.kube.
	Scan bool
}

// Source is one kubeconfig file or folder and what it contributed.
type Source struct {
	Path     string `json:"path"`
	Kind     string `json:"kind"` // flag, env, default, added, folder, in folder, scan
	Contexts int    `json:"contexts"`
	Err      string `json:"err,omitempty"`
	// Removable is false only for files from the --kubeconfig flag.
	Removable bool `json:"removable"`
	// Hidden files were removed in the app. st8ks lists them but does not read them.
	Hidden bool `json:"hidden,omitempty"`
}

// ctxEntry is one context and the config it resolves in.
type ctxEntry struct {
	name   string
	orig   string
	source string
	raw    *clientcmdapi.Config
}

type loadResult struct {
	entries map[string]*ctxEntry
	sources []Source
	current string
	watch   []string
	dirs    []string
}

func kubeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".kube")
}

func absPath(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	}
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

// looksLikeKubeconfig checks the start of a file for kubeconfig keys, so a
// scan does not parse unrelated files.
func looksLikeKubeconfig(path string) bool {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() == 0 || st.Size() > 4<<20 {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 64<<10)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	return bytes.Contains(head, []byte("contexts")) && bytes.Contains(head, []byte("clusters"))
}

var skipDirs = map[string]bool{"cache": true, "http-cache": true, "kubens": true, "kubectx": true, "plugins": true, "kuberc": true}

// filesIn lists kubeconfig files in a folder and one level below it.
func filesIn(dir string, depth int) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		name := e.Name()
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".lock") || strings.HasSuffix(name, "~") {
			continue
		}
		p := filepath.Join(dir, name)
		if e.IsDir() {
			if depth > 0 && !skipDirs[name] {
				out = append(out, filesIn(p, depth-1)...)
			}
			continue
		}
		if looksLikeKubeconfig(p) {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func loadFile(path string) (*clientcmdapi.Config, error) {
	// ExplicitPath resolves relative certificate paths against the file.
	return (&clientcmd.ClientConfigLoadingRules{ExplicitPath: path}).Load()
}

// sameTarget reports if two contexts reach the same cluster with the same
// credentials. Names alone are not enough: two EKS files can use the same
// user name with different AWS profiles.
func sameTarget(a, b *ctxEntry) bool {
	ca, cb := a.raw.Contexts[a.orig], b.raw.Contexts[b.orig]
	if ca == nil || cb == nil || ca.Namespace != cb.Namespace {
		return false
	}
	return equalIgnoringOrigin(a.raw.Clusters[ca.Cluster], b.raw.Clusters[cb.Cluster]) &&
		equalIgnoringOrigin(a.raw.AuthInfos[ca.AuthInfo], b.raw.AuthInfos[cb.AuthInfo])
}

// equalIgnoringOrigin compares two kubeconfig entries without the file
// they came from.
func equalIgnoringOrigin[T any](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	x, y := *a, *b
	clearOrigin(&x)
	clearOrigin(&y)
	return reflect.DeepEqual(x, y)
}

func clearOrigin(v any) {
	switch t := v.(type) {
	case *clientcmdapi.Cluster:
		t.LocationOfOrigin = ""
	case *clientcmdapi.AuthInfo:
		t.LocationOfOrigin = ""
	}
}

// loadKubeconfigs reads the primary kubeconfig the way kubectl does, then
// every extra file on its own. A context name that exists in two files
// gets the file name as a suffix, so no context hides another.
func loadKubeconfigs(o LoadOptions) loadResult {
	res := loadResult{entries: map[string]*ctxEntry{}}
	seenFile := map[string]bool{}

	var primary []string
	primaryKind := "default"
	switch {
	case len(o.Explicit) > 0:
		for _, p := range o.Explicit {
			primary = append(primary, filepath.SplitList(p)...)
		}
		primaryKind = "flag"
	case os.Getenv(clientcmd.RecommendedConfigPathEnvVar) != "":
		primary = filepath.SplitList(os.Getenv(clientcmd.RecommendedConfigPathEnvVar))
		primaryKind = "env"
	default:
		primary = []string{clientcmd.RecommendedHomeFile}
	}
	hidden := map[string]bool{}
	for _, p := range o.Hidden {
		hidden[absPath(p)] = true
	}
	var read []string
	for i := range primary {
		primary[i] = absPath(primary[i])
		if primaryKind == "flag" || !hidden[primary[i]] {
			read = append(read, primary[i])
		}
	}

	// The primary files merge into one config, so a context can use a user
	// or cluster from another file in the list.
	var merged *clientcmdapi.Config
	var mergeErr error
	if len(read) > 0 {
		merged, mergeErr = (&clientcmd.ClientConfigLoadingRules{Precedence: read}).Load()
	}
	for _, p := range primary {
		if seenFile[p] {
			continue
		}
		seenFile[p] = true
		if primaryKind != "flag" && hidden[p] {
			res.sources = append(res.sources, Source{Path: p, Kind: primaryKind, Removable: true, Hidden: true})
			continue
		}
		res.watch = append(res.watch, p)
		src := Source{Path: p, Kind: primaryKind, Removable: primaryKind != "flag"}
		if _, err := os.Stat(p); err != nil {
			if primaryKind == "default" && os.IsNotExist(err) {
				continue
			}
			src.Err = "file not found"
		} else if cfg, err := loadFile(p); err != nil {
			src.Err = err.Error()
		} else {
			src.Contexts = len(cfg.Contexts)
		}
		res.sources = append(res.sources, src)
	}
	if mergeErr == nil && merged != nil {
		res.current = merged.CurrentContext
		for name, c := range merged.Contexts {
			res.entries[name] = &ctxEntry{name: name, orig: name, source: c.LocationOfOrigin, raw: merged}
		}
	}

	add := func(path, kind string) {
		path = absPath(path)
		if seenFile[path] {
			return
		}
		seenFile[path] = true
		if hidden[path] {
			res.sources = append(res.sources, Source{Path: path, Kind: kind, Removable: true, Hidden: true})
			return
		}
		res.watch = append(res.watch, path)
		src := Source{Path: path, Kind: kind, Removable: true}
		cfg, err := loadFile(path)
		if err != nil {
			src.Err = err.Error()
			res.sources = append(res.sources, src)
			return
		}
		names := make([]string, 0, len(cfg.Contexts))
		for n := range cfg.Contexts {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			e := &ctxEntry{name: n, orig: n, source: path, raw: cfg}
			if prev, ok := res.entries[n]; ok {
				if sameTarget(prev, e) {
					continue
				}
				base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
				e.name = n + "@" + base
				for i := 2; res.entries[e.name] != nil; i++ {
					e.name = n + "@" + base + "-" + strconv.Itoa(i)
				}
			}
			res.entries[e.name] = e
			src.Contexts++
		}
		res.sources = append(res.sources, src)
	}

	if len(o.Explicit) == 0 {
		for _, p := range o.Extra {
			p = absPath(p)
			st, err := os.Stat(p)
			switch {
			case err != nil:
				res.sources = append(res.sources, Source{Path: p, Kind: "added", Err: "file not found", Removable: true})
				seenFile[p] = true
			case st.IsDir():
				res.dirs = append(res.dirs, p)
				seenFile[p] = true
				at := len(res.sources)
				res.sources = append(res.sources, Source{Path: p, Kind: "folder", Removable: true})
				before := len(res.entries)
				for _, f := range filesIn(p, 1) {
					add(f, "in folder")
				}
				res.sources[at].Contexts = len(res.entries) - before
			default:
				add(p, "added")
			}
		}
		if o.Scan {
			if dir := kubeDir(); dir != "" {
				res.dirs = append(res.dirs, dir)
				for _, f := range filesIn(dir, 1) {
					add(f, "scan")
				}
			}
		}
	}
	return res
}
