package kube

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/repo"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/yaml"
)

// HelmRelease is one row of the releases list.
type HelmRelease struct {
	Name    string `json:"name"`
	NS      string `json:"ns"`
	Chart   string `json:"chart"`
	App     string `json:"app"`
	Rev     int    `json:"rev"`
	Status  string `json:"status"`
	Tone    string `json:"tone"`
	Updated int64  `json:"updated"`
}

// HelmRevision is one entry of a release history.
type HelmRevision struct {
	Rev     int    `json:"rev"`
	Status  string `json:"status"`
	Tone    string `json:"tone"`
	Chart   string `json:"chart"`
	App     string `json:"app"`
	Desc    string `json:"desc"`
	Updated int64  `json:"updated"`
}

// HelmDetail is the side panel of one release.
type HelmDetail struct {
	Release   HelmRelease    `json:"release"`
	Revisions []HelmRevision `json:"revisions"`
	Values    string         `json:"values"`
	Notes     string         `json:"notes"`
}

var gzipMagic = []byte{0x1f, 0x8b, 0x08}

// decodeRelease reads a release stored by the Helm secrets driver.
func decodeRelease(data []byte) (*release.Release, error) {
	b, err := base64.StdEncoding.DecodeString(string(data))
	if err != nil {
		return nil, err
	}
	if len(b) > 3 && bytes.Equal(b[:3], gzipMagic) {
		r, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		defer r.Close()
		if b, err = io.ReadAll(r); err != nil {
			return nil, err
		}
	}
	var rel release.Release
	if err := json.Unmarshal(b, &rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

func helmTone(s string) string {
	switch s {
	case "deployed":
		return string(tOK)
	case "failed":
		return string(tErr)
	case "superseded", "uninstalled":
		return string(tMuted)
	}
	return string(tWarn)
}

func chartName(r *release.Release) (string, string) {
	if r.Chart == nil || r.Chart.Metadata == nil {
		return "—", "—"
	}
	return r.Chart.Metadata.Name + "-" + r.Chart.Metadata.Version, orDash(r.Chart.Metadata.AppVersion)
}

func releaseRow(r *release.Release) HelmRelease {
	ch, app := chartName(r)
	h := HelmRelease{Name: r.Name, NS: r.Namespace, Chart: ch, App: app, Rev: r.Version}
	if r.Info != nil {
		h.Status = string(r.Info.Status)
		h.Updated = r.Info.LastDeployed.Unix()
	}
	h.Tone = helmTone(h.Status)
	return h
}

// HelmReleases lists the latest revision of every release. It skips
// superseded revisions, so it decodes far fewer secrets than helm list.
func (c *Cluster) HelmReleases() ([]HelmRelease, error) {
	ctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
	defer cancel()
	list, err := c.cs.CoreV1().Secrets("").List(ctx, metav1.ListOptions{LabelSelector: "owner=helm,status!=superseded"})
	if err != nil {
		if c.defaultNS == "" {
			return nil, wrapf(err, "cannot list Helm releases")
		}
		list, err = c.cs.CoreV1().Secrets(c.defaultNS).List(ctx, metav1.ListOptions{LabelSelector: "owner=helm,status!=superseded"})
		if err != nil {
			return nil, wrapf(err, "cannot list Helm releases")
		}
	}
	latest := map[string]*release.Release{}
	for i := range list.Items {
		s := &list.Items[i]
		rel, err := decodeRelease(s.Data["release"])
		if err != nil {
			continue
		}
		key := rel.Namespace + "/" + rel.Name
		if cur := latest[key]; cur == nil || rel.Version > cur.Version {
			latest[key] = rel
		}
	}
	out := make([]HelmRelease, 0, len(latest))
	for _, r := range latest {
		if r.Info != nil && r.Info.Status == release.StatusUninstalled {
			continue
		}
		out = append(out, releaseRow(r))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].NS != out[j].NS {
			return out[i].NS < out[j].NS
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (c *Cluster) releaseHistory(ctx context.Context, ns, name string) ([]*release.Release, error) {
	list, err := c.cs.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{LabelSelector: "owner=helm,name=" + name})
	if err != nil {
		return nil, wrapf(err, "cannot read the history of %s", name)
	}
	var out []*release.Release
	for i := range list.Items {
		if rel, err := decodeRelease(list.Items[i].Data["release"]); err == nil {
			out = append(out, rel)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out, nil
}

// HelmDetail loads the history and values of a release.
func (c *Cluster) HelmDetail(ns, name string) (*HelmDetail, error) {
	ctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
	defer cancel()
	hist, err := c.releaseHistory(ctx, ns, name)
	if err != nil {
		return nil, err
	}
	if len(hist) == 0 {
		return nil, fmt.Errorf("release %s/%s not found", ns, name)
	}
	d := &HelmDetail{Release: releaseRow(hist[0]), Revisions: []HelmRevision{}}
	for _, r := range hist {
		ch, app := chartName(r)
		rv := HelmRevision{Rev: r.Version, Chart: ch, App: app}
		if r.Info != nil {
			rv.Status = string(r.Info.Status)
			rv.Desc = r.Info.Description
			rv.Updated = r.Info.LastDeployed.Unix()
		}
		rv.Tone = helmTone(rv.Status)
		d.Revisions = append(d.Revisions, rv)
	}
	if len(hist[0].Config) > 0 {
		b, _ := yaml.Marshal(hist[0].Config)
		d.Values = string(b)
	} else {
		d.Values = "# No user-supplied values. The chart defaults apply.\n"
	}
	if hist[0].Info != nil {
		d.Notes = hist[0].Info.Notes
	}
	return d, nil
}

// HelmRollbackTarget finds the last good revision before the current one.
func (c *Cluster) HelmRollbackTarget(ns, name string) (int, int, error) {
	ctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
	defer cancel()
	hist, err := c.releaseHistory(ctx, ns, name)
	if err != nil {
		return 0, 0, err
	}
	if len(hist) < 2 {
		return 0, 0, errors.New("the release has no earlier revision")
	}
	cur := hist[0].Version
	for _, r := range hist[1:] {
		if r.Info != nil && (r.Info.Status == release.StatusSuperseded || r.Info.Status == release.StatusDeployed) {
			return cur, r.Version, nil
		}
	}
	return cur, hist[1].Version, nil
}

// helmGetter gives the Helm SDK access to the current context.
type helmGetter struct {
	c  *Cluster
	ns string
}

func (g helmGetter) ToRESTConfig() (*rest.Config, error) { return rest.CopyConfig(g.c.cfg), nil }

func (g helmGetter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	dc, err := discovery.NewDiscoveryClientForConfig(g.c.cfg)
	if err != nil {
		return nil, err
	}
	return memory.NewMemCacheClient(dc), nil
}

func (g helmGetter) ToRESTMapper() (meta.RESTMapper, error) { return g.c.mapper(), nil }

func (g helmGetter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	return clientcmd.NewNonInteractiveClientConfig(g.c.raw, g.c.ctxOrig,
		&clientcmd.ConfigOverrides{Context: clientcmdapi.Context{Namespace: g.ns}}, nil)
}

func (c *Cluster) helmConfig(ns string) (*action.Configuration, error) {
	cfg := new(action.Configuration)
	if err := cfg.Init(helmGetter{c: c, ns: ns}, ns, "secret", func(string, ...interface{}) {}); err != nil {
		return nil, err
	}
	return cfg, nil
}

// HelmRollback rolls a release back to a revision.
func (c *Cluster) HelmRollback(ns, name string, rev int, dry bool) (string, error) {
	cfg, err := c.helmConfig(ns)
	if err != nil {
		return "", err
	}
	rb := action.NewRollback(cfg)
	rb.Version = rev
	rb.DryRun = dry
	rb.Timeout = 5 * time.Minute
	if err := rb.Run(name); err != nil {
		return "", fmt.Errorf("rollback of %s failed: %w", name, err)
	}
	if dry {
		return fmt.Sprintf("helm rollback %s %d --dry-run: revision %d renders without errors.", name, rev, rev), nil
	}
	return fmt.Sprintf("%s rolled back to revision %d", name, rev), nil
}

// HelmUninstall removes a release and its resources.
func (c *Cluster) HelmUninstall(ns, name string, dry bool) (string, error) {
	cfg, err := c.helmConfig(ns)
	if err != nil {
		return "", err
	}
	un := action.NewUninstall(cfg)
	un.DryRun = dry
	un.Timeout = 5 * time.Minute
	res, err := un.Run(name)
	if err != nil {
		return "", fmt.Errorf("uninstall of %s failed: %w", name, err)
	}
	if dry {
		n := 0
		if res != nil && res.Release != nil {
			n = countManifests(res.Release.Manifest)
		}
		return fmt.Sprintf("helm uninstall --dry-run: %d resources would be deleted.", n), nil
	}
	return name + " uninstalled", nil
}

// HelmUpgradeValues upgrades a release with new values and the same chart.
func (c *Cluster) HelmUpgradeValues(ns, name, values string, dry bool) (string, error) {
	vals := map[string]any{}
	if strings.TrimSpace(values) != "" {
		if err := yaml.Unmarshal([]byte(values), &vals); err != nil {
			return "", fmt.Errorf("the values are not valid YAML: %w", err)
		}
	}
	cfg, err := c.helmConfig(ns)
	if err != nil {
		return "", err
	}
	cur, err := action.NewGet(cfg).Run(name)
	if err != nil {
		return "", fmt.Errorf("cannot read release %s: %w", name, err)
	}
	up := action.NewUpgrade(cfg)
	up.Namespace = ns
	up.ResetValues = true
	up.DryRun = dry
	up.Timeout = 5 * time.Minute
	rel, err := up.Run(name, cur.Chart, vals)
	if err != nil {
		return "", fmt.Errorf("upgrade of %s failed: %w", name, err)
	}
	if dry {
		return fmt.Sprintf("helm upgrade --dry-run: revision %d renders %d resources.", rel.Version, countManifests(rel.Manifest)), nil
	}
	return fmt.Sprintf("%s upgraded · revision %d %s", name, rel.Version, rel.Info.Status), nil
}

func countManifests(m string) int {
	n := 0
	for _, doc := range strings.Split(m, "\n---") {
		if strings.Contains(doc, "kind:") {
			n++
		}
	}
	return n
}

// loadHelmRepos fills the repositories table from the local Helm config.
func (c *Cluster) loadHelmRepos() {
	t := c.table("HelmRepositories")
	if t == nil {
		return
	}
	f, err := repo.LoadFile(cli.New().RepositoryConfig)
	if err != nil {
		t.setSynced(true)
		return
	}
	for _, r := range f.Repositories {
		typ := "HTTP"
		if strings.HasPrefix(r.URL, "oci://") {
			typ = "OCI"
		}
		t.upsert(&Row{U: "repo:" + r.Name, M: r.Name, C: []string{"", r.URL, typ}, K: "---"})
	}
	t.setSynced(true)
}
