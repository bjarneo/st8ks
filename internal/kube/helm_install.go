package kube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/helmpath"
	"helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/repo"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/yaml"
)

// chartNetTimeout limits each call to a chart repository or an OCI registry.
const chartNetTimeout = 60 * time.Second

// searchLimit is the maximum count of hits that a search returns.
const searchLimit = 500

// ChartRepo is one chart repository of the local Helm config.
type ChartRepo struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Type    string `json:"type"`
	Charts  int    `json:"charts"`
	Updated int64  `json:"updated"`
	Err     string `json:"err,omitempty"`
}

// ChartHit is one search result.
type ChartHit struct {
	Ref        string `json:"ref"`
	Name       string `json:"name"`
	Repo       string `json:"repo"`
	Version    string `json:"version"`
	AppVersion string `json:"app"`
	Desc       string `json:"desc"`
	Icon       string `json:"icon"`
	Deprecated bool   `json:"deprecated,omitempty"`
}

// ChartSearch holds the best hits and the count of all hits.
type ChartSearch struct {
	Hits     []ChartHit `json:"hits"`
	Total    int        `json:"total"`
	Repos    int        `json:"repos"`
	Warnings []string   `json:"warnings,omitempty"`
}

// ChartVersionInfo is one version of a chart.
type ChartVersionInfo struct {
	Version    string `json:"version"`
	AppVersion string `json:"app,omitempty"`
	Created    int64  `json:"created,omitempty"`
	Pre        bool   `json:"pre,omitempty"`
}

// ChartDep is one dependency from Chart.yaml.
type ChartDep struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	Repository string `json:"repository"`
	Condition  string `json:"condition,omitempty"`
	Alias      string `json:"alias,omitempty"`
	Bundled    bool   `json:"bundled"`
}

// ChartDetail describes one version of a chart.
type ChartDetail struct {
	Ref         string     `json:"ref"`
	Name        string     `json:"name"`
	Version     string     `json:"version"`
	AppVersion  string     `json:"app"`
	Desc        string     `json:"desc"`
	Type        string     `json:"type"`
	APIVersion  string     `json:"apiVersion"`
	KubeVersion string     `json:"kubeVersion"`
	Home        string     `json:"home"`
	Icon        string     `json:"icon"`
	Sources     []string   `json:"sources"`
	Keywords    []string   `json:"keywords"`
	Maintainers []string   `json:"maintainers"`
	Deprecated  bool       `json:"deprecated"`
	Deps        []ChartDep `json:"deps"`
	Values      string     `json:"values"`
	Readme      string     `json:"readme"`
}

// InstallSpec is the input of an install. Timeout is in seconds.
type InstallSpec struct {
	Ref      string `json:"ref"`
	Version  string `json:"version"`
	Name     string `json:"name"`
	NS       string `json:"ns"`
	CreateNS bool   `json:"createNs"`
	Values   string `json:"values"`
	Wait     bool   `json:"wait"`
	Timeout  int    `json:"timeout"`
	Atomic   bool   `json:"atomic"`
	Dry      bool   `json:"dry"`
}

// InstallResult is the result of an install or a dry run. Checks lists the
// objects that the API server refused in a dry run.
type InstallResult struct {
	Name      string   `json:"name"`
	NS        string   `json:"ns"`
	Chart     string   `json:"chart"`
	Version   string   `json:"version"`
	Status    string   `json:"status"`
	Revision  int      `json:"rev"`
	Notes     string   `json:"notes"`
	Manifest  string   `json:"manifest,omitempty"`
	Resources int      `json:"resources"`
	Hooks     int      `json:"hooks"`
	Checks    []string `json:"checks,omitempty"`
	Note      string   `json:"note,omitempty"`
}

// Charts finds, loads and installs Helm charts. It uses the same files as
// the helm CLI: repositories.yaml, the repository cache and the registry
// config. Each index loads once and stays in memory until its file changes.
type Charts struct {
	mu      sync.Mutex
	indexes map[string]*repoIndex // by the path of the index file
	located map[string]string     // chart archives by reference and version

	loadMu sync.Mutex // one index load at a time, so no file parses twice
	fileMu sync.Mutex // one write of repositories.yaml at a time
}

// NewCharts creates an empty chart cache. It reads the Helm environment
// variables on each call, so a changed HELM_REPOSITORY_CONFIG applies.
func NewCharts() *Charts {
	return &Charts{indexes: map[string]*repoIndex{}, located: map[string]string{}}
}

// repoIndex is the parsed index of one repository, reduced to what the
// search and the version list need.
type repoIndex struct {
	repo   string
	mod    time.Time
	size   int64
	err    string
	charts []*indexedChart // sorted by name
	byName map[string]*indexedChart
}

type indexedChart struct {
	name, repo   string
	lname, lrepo string
	lkeys        []string
	ldesc        string
	hay          string
	latest       *repo.ChartVersion
	stable       *repo.ChartVersion // nil when the chart has only prereleases
	versions     []ChartVersionInfo // newest first
}

// chartRef is a parsed chart reference: repo/chart or oci://registry/path/chart.
type chartRef struct {
	oci  bool
	repo string // the repository name of repo/chart
	name string // the chart name
	base string // the reference without the tag
	host string // the registry host of an OCI reference
	tag  string // the tag of an OCI reference
}

func (r chartRef) String() string {
	if r.tag != "" {
		return r.base + ":" + r.tag
	}
	return r.base
}

var errRefFormat = errors.New("enter a chart as repo/chart or oci://registry/path/chart")

// parseChartRef reads repo/chart or oci://registry/path/chart[:tag].
func parseChartRef(s string) (chartRef, error) {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(s, "oci://"); ok {
		host, path, _ := strings.Cut(rest, "/")
		if host == "" || path == "" || strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") || strings.Contains(path, "//") || strings.ContainsAny(rest, " \t") {
			return chartRef{}, fmt.Errorf("%s is not a valid OCI reference. Use oci://registry/path/chart", s)
		}
		dir, last := "", path
		if i := strings.LastIndex(path, "/"); i >= 0 {
			dir, last = path[:i+1], path[i+1:]
		}
		if strings.Contains(last, "@") {
			return chartRef{}, fmt.Errorf("%s has a digest. Select a version with a tag instead", s)
		}
		r := chartRef{oci: true, host: host}
		if n, tag, ok := strings.Cut(last, ":"); ok {
			if n == "" || tag == "" {
				return chartRef{}, fmt.Errorf("%s is not a valid OCI reference. Use oci://registry/path/chart", s)
			}
			last, r.tag = n, tag
		}
		r.name = last
		r.base = "oci://" + host + "/" + dir + last
		return r, nil
	}
	repoName, name, ok := strings.Cut(s, "/")
	if !ok || repoName == "" || name == "" || strings.Contains(name, "/") || strings.ContainsAny(s, " \t:@") {
		return chartRef{}, errRefFormat
	}
	return chartRef{repo: repoName, name: name, base: s}, nil
}

// sortVersions orders items newest first by semantic version. Items that
// are not valid semantic versions go last and keep their order.
func sortVersions[T any](items []T, version func(T) string) {
	type pair struct {
		v  *semver.Version
		it T
	}
	ps := make([]pair, len(items))
	for i, it := range items {
		v, _ := semver.NewVersion(version(it))
		ps[i] = pair{v, it}
	}
	sort.SliceStable(ps, func(a, b int) bool {
		va, vb := ps[a].v, ps[b].v
		if va == nil || vb == nil {
			return va != nil
		}
		return va.GreaterThan(vb)
	})
	for i := range ps {
		items[i] = ps[i].it
	}
}

func isPrerelease(v string) bool {
	sv, err := semver.NewVersion(v)
	return err == nil && sv.Prerelease() != ""
}

// rankChart scores how well a chart matches a lowercase query. A lower
// score is a better match. The result is -1 when the chart does not match.
func rankChart(c *indexedChart, q string) int {
	if q == "" {
		return 0
	}
	if strings.Contains(q, "/") {
		ref := c.lrepo + "/" + c.lname
		switch {
		case ref == q:
			return 0
		case strings.HasPrefix(ref, q):
			return 1
		case strings.Contains(ref, q):
			return 2
		}
		return -1
	}
	switch {
	case c.lname == q:
		return 0
	case strings.HasPrefix(c.lname, q):
		return 1
	case strings.Contains(c.lname, q):
		return 2
	case slices.Contains(c.lkeys, q):
		return 3
	case slices.ContainsFunc(c.lkeys, func(k string) bool { return strings.Contains(k, q) }):
		return 4
	case strings.Contains(c.ldesc, q):
		return 5
	case c.lrepo == q:
		return 6
	}
	words := strings.Fields(q)
	if len(words) < 2 {
		return -1
	}
	for _, w := range words {
		if !strings.Contains(c.hay, w) {
			return -1
		}
	}
	return 7
}

// searchIndexes ranks the charts of all indexes. It returns at most limit
// hits and the count of all hits.
func searchIndexes(ixs []*repoIndex, query string, devel bool, limit int) ([]ChartHit, int) {
	q := strings.ToLower(strings.Join(strings.Fields(query), " "))
	type scored struct {
		s int
		c *indexedChart
		v *repo.ChartVersion
	}
	var all []scored
	for _, ix := range ixs {
		for _, c := range ix.charts {
			v := c.stable
			if devel {
				v = c.latest
			}
			if v == nil {
				continue
			}
			if s := rankChart(c, q); s >= 0 {
				all = append(all, scored{s, c, v})
			}
		}
	}
	sort.Slice(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.s != b.s {
			return a.s < b.s
		}
		if a.c.name != b.c.name {
			return a.c.name < b.c.name
		}
		return a.c.repo < b.c.repo
	})
	total := len(all)
	if len(all) > limit {
		all = all[:limit]
	}
	hits := make([]ChartHit, len(all))
	for i, x := range all {
		hits[i] = ChartHit{Ref: x.c.repo + "/" + x.c.name, Name: x.c.name, Repo: x.c.repo, Version: x.v.Version,
			AppVersion: x.v.AppVersion, Desc: x.v.Description, Icon: x.v.Icon, Deprecated: x.v.Deprecated}
	}
	return hits, total
}

func newIndexedChart(repoName, name string, cvs repo.ChartVersions) *indexedChart {
	vs := make([]*repo.ChartVersion, 0, len(cvs))
	for _, cv := range cvs {
		if cv != nil && cv.Metadata != nil && !cv.Removed {
			vs = append(vs, cv)
		}
	}
	if len(vs) == 0 {
		return nil
	}
	sortVersions(vs, func(cv *repo.ChartVersion) string { return cv.Version })
	c := &indexedChart{name: name, repo: repoName, lname: strings.ToLower(name), lrepo: strings.ToLower(repoName),
		latest: vs[0], versions: make([]ChartVersionInfo, len(vs))}
	for i, cv := range vs {
		pre := isPrerelease(cv.Version)
		c.versions[i] = ChartVersionInfo{Version: cv.Version, AppVersion: cv.AppVersion, Pre: pre}
		if !cv.Created.IsZero() {
			c.versions[i].Created = cv.Created.Unix()
		}
		if !pre && c.stable == nil {
			c.stable = cv
		}
	}
	for _, k := range c.latest.Keywords {
		c.lkeys = append(c.lkeys, strings.ToLower(k))
	}
	c.ldesc = strings.ToLower(c.latest.Description)
	c.hay = c.lname + " " + c.lrepo + " " + strings.Join(c.lkeys, " ") + " " + c.ldesc
	return c
}

func parseIndex(name, path string, st fs.FileInfo) *repoIndex {
	ix := &repoIndex{repo: name, mod: st.ModTime(), size: st.Size(), byName: map[string]*indexedChart{}}
	f, err := repo.LoadIndexFile(path)
	if err != nil {
		ix.err = fmt.Sprintf("the cached index of %s is not valid. Update the repository", name)
		return ix
	}
	for cname, cvs := range f.Entries {
		if c := newIndexedChart(name, cname, cvs); c != nil {
			ix.charts = append(ix.charts, c)
			ix.byName[cname] = c
		}
	}
	sort.Slice(ix.charts, func(i, j int) bool { return ix.charts[i].name < ix.charts[j].name })
	return ix
}

func indexPath(s *cli.EnvSettings, name string) string {
	return filepath.Join(s.RepositoryCache, helmpath.CacheIndexFile(name))
}

// loadIndexes returns the parsed index of each repository, in the order of
// the list. It parses a file again only when its size or modification time
// changed, and it parses changed files in parallel.
func (h *Charts) loadIndexes(s *cli.EnvSettings, repos []*repo.Entry) []*repoIndex {
	h.loadMu.Lock()
	defer h.loadMu.Unlock()
	out := make([]*repoIndex, len(repos))
	var wg sync.WaitGroup
	for i, e := range repos {
		path := indexPath(s, e.Name)
		st, err := os.Stat(path)
		if err != nil {
			out[i] = &repoIndex{repo: e.Name, err: fmt.Sprintf("%s has no index in the cache. Update the repository", e.Name)}
			continue
		}
		h.mu.Lock()
		cur := h.indexes[path]
		h.mu.Unlock()
		if cur != nil && cur.repo == e.Name && cur.mod.Equal(st.ModTime()) && cur.size == st.Size() {
			out[i] = cur
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ix := parseIndex(e.Name, path, st)
			h.mu.Lock()
			h.indexes[path] = ix
			h.mu.Unlock()
			out[i] = ix
		}()
	}
	wg.Wait()
	return out
}

func loadRepoFile(path string) (*repo.File, error) {
	f, err := repo.LoadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return repo.NewFile(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read the Helm repository list %s: %s", path, err)
	}
	return f, nil
}

// writeRepoFile writes repositories.yaml through a temporary file, so a
// failed write cannot leave a broken file. It follows a symbolic link.
func writeRepoFile(path string, f *repo.File) error {
	if p, err := filepath.EvalSymlinks(path); err == nil {
		path = p
	}
	b, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".repositories-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

var urlCreds = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/@\s]+@`)

// redactURLs removes the user name and the password from every URL in s.
func redactURLs(s string) string { return urlCreds.ReplaceAllString(s, "$1") }

func repoType(u string) string {
	if registry.IsOCI(u) {
		return "OCI"
	}
	return "HTTP"
}

func errUnknownRepo(name string) error {
	return fmt.Errorf("the repository %s is not in the Helm repository list. Add it first", name)
}

// withDeadline runs fn and stops to wait for it after chartNetTimeout. Some
// Helm calls take no context, so this is the only way to limit them.
func withDeadline[T any](what string, fn func() (T, error)) (T, error) {
	type result struct {
		v   T
		err error
	}
	ch := make(chan result, 1)
	go func() {
		v, err := fn()
		ch <- result{v, err}
	}()
	t := time.NewTimer(chartNetTimeout)
	defer t.Stop()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-t.C:
		var zero T
		return zero, fmt.Errorf("%s did not answer in %d seconds", what, int(chartNetTimeout.Seconds()))
	}
}

// Repos lists the repositories of the local Helm config with the chart
// count of each cached index.
func (h *Charts) Repos() ([]ChartRepo, error) {
	s := cli.New()
	f, err := loadRepoFile(s.RepositoryConfig)
	if err != nil {
		return nil, err
	}
	ixs := h.loadIndexes(s, f.Repositories)
	out := make([]ChartRepo, len(f.Repositories))
	for i, e := range f.Repositories {
		out[i] = ChartRepo{Name: e.Name, URL: redactURLs(e.URL), Type: repoType(e.URL), Charts: len(ixs[i].charts), Err: ixs[i].err}
		if !ixs[i].mod.IsZero() {
			out[i].Updated = ixs[i].mod.Unix()
		}
	}
	return out, nil
}

// downloadIndex downloads the index of a repository into the cache.
func downloadIndex(s *cli.EnvSettings, e *repo.Entry) error {
	r, err := repo.NewChartRepository(e, getter.All(s, getter.WithTimeout(chartNetTimeout)))
	if err != nil {
		return fmt.Errorf("cannot use %s: %s", redactURLs(e.URL), redactURLs(err.Error()))
	}
	r.CachePath = s.RepositoryCache
	if _, err := r.DownloadIndexFile(); err != nil {
		msg := redactURLs(err.Error())
		low := strings.ToLower(msg)
		switch {
		case strings.Contains(low, "401") || strings.Contains(low, "403"):
			return fmt.Errorf("the repository %s refused access. Check the user name and the password", redactURLs(e.URL))
		case strings.Contains(low, "404"):
			return fmt.Errorf("%s has no index.yaml. Check the URL", redactURLs(e.URL))
		case strings.Contains(low, "timeout") || strings.Contains(low, "deadline exceeded"):
			return fmt.Errorf("%s did not answer in %d seconds", redactURLs(e.URL), int(chartNetTimeout.Seconds()))
		}
		return fmt.Errorf("cannot download the index of %s: %s", e.Name, msg)
	}
	return nil
}

// AddRepo adds a chart repository and downloads its index, like helm repo add.
func (h *Charts) AddRepo(name, rawURL, username, password string) (string, error) {
	name, rawURL, username = strings.TrimSpace(name), strings.TrimSpace(rawURL), strings.TrimSpace(username)
	switch {
	case name == "":
		return "", errors.New("enter a name for the repository")
	case strings.ContainsAny(name, "/\\ \t"):
		return "", fmt.Errorf("the repository name %q must not contain a slash or a space", name)
	case rawURL == "":
		return "", errors.New("enter the URL of the repository")
	case registry.IsOCI(rawURL):
		return "", errors.New("an OCI registry has no index, so you cannot add it as a repository. Paste the oci:// reference of the chart in the search instead")
	}
	if u, err := url.Parse(rawURL); err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("%s is not a valid URL", redactURLs(rawURL))
	}
	s := cli.New()
	h.fileMu.Lock()
	defer h.fileMu.Unlock()
	f, err := loadRepoFile(s.RepositoryConfig)
	if err != nil {
		return "", err
	}
	e := &repo.Entry{Name: name, URL: rawURL, Username: username, Password: password}
	cur := f.Get(name)
	if cur != nil {
		if strings.TrimSuffix(cur.URL, "/") != strings.TrimSuffix(rawURL, "/") || (username != "" && (cur.Username != username || cur.Password != password)) {
			return "", fmt.Errorf("a repository with the name %s already exists. Remove it first, or use a different name", name)
		}
		e = cur
	}
	if err := downloadIndex(s, e); err != nil {
		return "", err
	}
	if cur == nil {
		f.Update(e)
		if err := writeRepoFile(s.RepositoryConfig, f); err != nil {
			return "", fmt.Errorf("cannot write %s: %w", s.RepositoryConfig, err)
		}
	}
	n := countCharts(len(h.loadIndexes(s, []*repo.Entry{e})[0].charts))
	if cur != nil {
		return fmt.Sprintf("%s is already in the list. Updated its index · %s", name, n), nil
	}
	return fmt.Sprintf("Added repository %s · %s", name, n), nil
}

func countCharts(n int) string {
	if n == 1 {
		return "1 chart"
	}
	return fmt.Sprintf("%d charts", n)
}

// UpdateRepos downloads the index of one repository, or of all of them
// when name is empty, like helm repo update.
func (h *Charts) UpdateRepos(name string) (string, error) {
	s := cli.New()
	f, err := loadRepoFile(s.RepositoryConfig)
	if err != nil {
		return "", err
	}
	list := f.Repositories
	if name != "" {
		e := f.Get(name)
		if e == nil {
			return "", errUnknownRepo(name)
		}
		list = []*repo.Entry{e}
	}
	if len(list) == 0 {
		return "", errors.New("there are no chart repositories. Add one first")
	}
	errs := make([]error, len(list))
	var wg sync.WaitGroup
	for i, e := range list {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = downloadIndex(s, e)
		}()
	}
	wg.Wait()
	var failed []string
	for i, err := range errs {
		if err != nil {
			failed = append(failed, list[i].Name+": "+err.Error())
		}
	}
	if len(failed) > 0 {
		return "", fmt.Errorf("updated %d of %d repositories.\n%s", len(list)-len(failed), len(list), strings.Join(failed, "\n"))
	}
	if len(list) == 1 {
		return "Updated repository " + list[0].Name, nil
	}
	return fmt.Sprintf("Updated %d repositories", len(list)), nil
}

// RemoveRepo removes a repository and its cached index, like helm repo remove.
func (h *Charts) RemoveRepo(name string) (string, error) {
	s := cli.New()
	h.fileMu.Lock()
	defer h.fileMu.Unlock()
	f, err := loadRepoFile(s.RepositoryConfig)
	if err != nil {
		return "", err
	}
	if !f.Remove(name) {
		return "", errUnknownRepo(name)
	}
	if err := writeRepoFile(s.RepositoryConfig, f); err != nil {
		return "", fmt.Errorf("cannot write %s: %w", s.RepositoryConfig, err)
	}
	path := indexPath(s, name)
	for _, p := range []string{path, filepath.Join(s.RepositoryCache, helmpath.CacheChartsFile(name))} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("removed %s, but cannot delete its cached index: %w", name, err)
		}
	}
	h.mu.Lock()
	delete(h.indexes, path)
	h.mu.Unlock()
	return "Removed repository " + name, nil
}

// Search finds charts in the cached indexes by name, keyword and
// description, like helm search repo. devel includes prerelease versions.
func (h *Charts) Search(query string, devel bool) (*ChartSearch, error) {
	s := cli.New()
	f, err := loadRepoFile(s.RepositoryConfig)
	if err != nil {
		return nil, err
	}
	ixs := h.loadIndexes(s, f.Repositories)
	res := &ChartSearch{Repos: len(f.Repositories)}
	for _, ix := range ixs {
		if ix.err != "" {
			res.Warnings = append(res.Warnings, ix.err)
		}
	}
	res.Hits, res.Total = searchIndexes(ixs, query, devel, searchLimit)
	return res, nil
}

// findChart looks up a repo/chart reference in the cached index.
func (h *Charts) findChart(s *cli.EnvSettings, r chartRef) (*indexedChart, error) {
	f, err := loadRepoFile(s.RepositoryConfig)
	if err != nil {
		return nil, err
	}
	e := f.Get(r.repo)
	if e == nil {
		return nil, errUnknownRepo(r.repo)
	}
	ix := h.loadIndexes(s, []*repo.Entry{e})[0]
	if ix.err != "" {
		return nil, errors.New(ix.err)
	}
	c := ix.byName[r.name]
	if c == nil {
		return nil, fmt.Errorf("the chart %s is not in the repository %s. Update the repository, then try again", r.name, r.repo)
	}
	return c, nil
}

func newRegistryClient(s *cli.EnvSettings) (*registry.Client, error) {
	rc, err := registry.NewClient(
		registry.ClientOptCredentialsFile(s.RegistryConfig),
		registry.ClientOptEnableCache(true),
		registry.ClientOptWriter(io.Discard),
	)
	if err != nil {
		return nil, fmt.Errorf("cannot read the Helm registry config %s: %s", s.RegistryConfig, err)
	}
	return rc, nil
}

// ociErr names the cause of a failed registry call.
func ociErr(r chartRef, version string, err error) error {
	msg := redactURLs(err.Error())
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "401") || strings.Contains(low, "403") || strings.Contains(low, "unauthorized") ||
		strings.Contains(low, "forbidden") || strings.Contains(low, "denied") || strings.Contains(low, "authentication required"):
		return fmt.Errorf("the registry %s refused access to %s. The chart does not exist, or the registry needs a login. Run helm registry login %s, then try again", r.host, r.base, r.host)
	case strings.Contains(low, "not found") || strings.Contains(low, "404") || strings.Contains(low, "manifest unknown") || strings.Contains(low, "name unknown"):
		if version != "" {
			return fmt.Errorf("version %s of %s is not in the registry", version, r.base)
		}
		return fmt.Errorf("the chart %s is not in the registry", r.base)
	}
	return fmt.Errorf("cannot read %s: %s", r.base, msg)
}

// ociTags lists the semantic version tags of an OCI chart, newest first.
func ociTags(s *cli.EnvSettings, r chartRef) ([]string, error) {
	rc, err := newRegistryClient(s)
	if err != nil {
		return nil, err
	}
	tags, err := withDeadline("the registry "+r.host, func() ([]string, error) {
		return rc.Tags(strings.TrimPrefix(r.base, "oci://"))
	})
	if err != nil {
		return nil, ociErr(r, "", err)
	}
	sortVersions(tags, func(t string) string { return t })
	return tags, nil
}

// Versions lists the versions of a chart, newest first. devel includes
// prerelease versions.
func (h *Charts) Versions(ref string, devel bool) ([]ChartVersionInfo, error) {
	r, err := parseChartRef(ref)
	if err != nil {
		return nil, err
	}
	s := cli.New()
	var all []ChartVersionInfo
	if r.oci {
		tags, err := ociTags(s, r)
		if err != nil {
			return nil, err
		}
		for _, t := range tags {
			all = append(all, ChartVersionInfo{Version: t, Pre: isPrerelease(t)})
		}
	} else {
		c, err := h.findChart(s, r)
		if err != nil {
			return nil, err
		}
		all = c.versions
	}
	out := make([]ChartVersionInfo, 0, len(all))
	for _, v := range all {
		if devel || !v.Pre {
			out = append(out, v)
		}
	}
	switch {
	case len(all) == 0:
		return nil, fmt.Errorf("%s has no versions", r.base)
	case len(out) == 0:
		return nil, fmt.Errorf("%s has only prerelease versions. Include prereleases to see them", r.base)
	}
	return out, nil
}

// pickVersion finds a version in a list, newest first. An empty version
// selects the newest stable version. A constraint such as ^1.2 also works.
func pickVersion(r chartRef, versions []ChartVersionInfo, version string) (string, error) {
	if len(versions) == 0 {
		return "", fmt.Errorf("%s has no versions", r.base)
	}
	if version == "" {
		for _, v := range versions {
			if !v.Pre {
				return v.Version, nil
			}
		}
		return versions[0].Version, nil
	}
	for _, v := range versions {
		if v.Version == version {
			return version, nil
		}
	}
	if c, err := semver.NewConstraint(version); err == nil {
		for _, v := range versions {
			if sv, err := semver.NewVersion(v.Version); err == nil && c.Check(sv) {
				return v.Version, nil
			}
		}
	}
	return "", fmt.Errorf("version %s of %s does not exist. The newest version is %s", version, r.base, versions[0].Version)
}

// resolve returns the exact version to load.
func (h *Charts) resolve(s *cli.EnvSettings, r chartRef, version string) (string, error) {
	if r.tag != "" {
		if version != "" && version != r.tag {
			return "", fmt.Errorf("the reference has the tag %s, but the version is %s", r.tag, version)
		}
		version = r.tag
	}
	if !r.oci {
		c, err := h.findChart(s, r)
		if err != nil {
			return "", err
		}
		return pickVersion(r, c.versions, version)
	}
	tags, err := ociTags(s, r)
	if err != nil {
		return "", err
	}
	vs := make([]ChartVersionInfo, len(tags))
	for i, t := range tags {
		vs[i] = ChartVersionInfo{Version: t, Pre: isPrerelease(t)}
	}
	return pickVersion(r, vs, version)
}

// load finds, downloads and loads one version of a chart. A chart archive
// downloads once per reference and version.
func (h *Charts) load(ref, version string) (*chart.Chart, chartRef, error) {
	r, err := parseChartRef(ref)
	if err != nil {
		return nil, r, err
	}
	s := cli.New()
	version = strings.TrimSpace(version)
	if version, err = h.resolve(s, r, version); err != nil {
		return nil, r, err
	}
	key := r.base + "@" + version
	h.mu.Lock()
	path := h.located[key]
	h.mu.Unlock()
	if _, err := os.Stat(path); err != nil {
		// LocateChart prefers a local folder with the same name.
		if _, err := os.Stat(r.base); err == nil {
			return nil, r, fmt.Errorf("a local file or folder has the name %s. Start st8ks from another folder", r.base)
		}
		rc, err := newRegistryClient(s)
		if err != nil {
			return nil, r, err
		}
		inst := action.NewInstall(&action.Configuration{})
		inst.SetRegistryClient(rc)
		inst.Version = version
		where := "the repository " + r.repo
		if r.oci {
			where = "the registry " + r.host
		}
		path, err = withDeadline(where, func() (string, error) { return inst.LocateChart(r.base, s) })
		if err != nil {
			if r.oci {
				return nil, r, ociErr(r, version, err)
			}
			return nil, r, fmt.Errorf("cannot download %s %s: %s", r.base, version, redactURLs(err.Error()))
		}
		h.mu.Lock()
		h.located[key] = path
		h.mu.Unlock()
	}
	ch, err := loader.Load(path)
	if err != nil {
		return nil, r, fmt.Errorf("cannot read the chart %s %s: %s", r.base, version, err)
	}
	return ch, r, nil
}

var readmeNames = []string{"readme.md", "readme.txt", "readme"}

// maxReadme limits the README text that goes to the frontend.
const maxReadme = 512 << 10

func chartDetail(r chartRef, ch *chart.Chart) *ChartDetail {
	m := ch.Metadata
	d := &ChartDetail{Ref: r.base, Name: m.Name, Version: m.Version, AppVersion: m.AppVersion, Desc: m.Description, Type: m.Type,
		APIVersion: m.APIVersion, KubeVersion: m.KubeVersion, Home: m.Home, Icon: m.Icon, Deprecated: m.Deprecated,
		Sources: append([]string{}, m.Sources...), Keywords: append([]string{}, m.Keywords...), Maintainers: []string{}, Deps: []ChartDep{}}
	if d.Type == "" {
		d.Type = "application"
	}
	for _, mt := range m.Maintainers {
		if mt == nil || mt.Name == "" {
			continue
		}
		if mt.URL != "" {
			d.Maintainers = append(d.Maintainers, mt.Name+" · "+mt.URL)
		} else {
			d.Maintainers = append(d.Maintainers, mt.Name)
		}
	}
	bundled := map[string]bool{}
	for _, sub := range ch.Dependencies() {
		bundled[sub.Name()] = true
	}
	for _, dep := range m.Dependencies {
		if dep == nil {
			continue
		}
		d.Deps = append(d.Deps, ChartDep{Name: dep.Name, Version: dep.Version, Repository: redactURLs(dep.Repository),
			Condition: dep.Condition, Alias: dep.Alias, Bundled: bundled[dep.Name]})
	}
	for _, f := range ch.Raw {
		if f.Name == chartutil.ValuesfileName {
			d.Values = string(f.Data)
		}
	}
	if d.Values == "" && len(ch.Values) > 0 {
		b, _ := yaml.Marshal(ch.Values)
		d.Values = string(b)
	}
	for _, n := range readmeNames {
		for _, f := range ch.Files {
			if f != nil && strings.EqualFold(f.Name, n) && d.Readme == "" {
				d.Readme = string(f.Data)
			}
		}
	}
	if len(d.Readme) > maxReadme {
		d.Readme = d.Readme[:maxReadme] + "\n\n…"
	}
	return d
}

// Detail loads one version of a chart: the default values, the README, the
// Chart.yaml facts and the dependencies. An empty version selects the
// newest stable version.
func (h *Charts) Detail(ref, version string) (*ChartDetail, error) {
	ch, r, err := h.load(ref, version)
	if err != nil {
		return nil, err
	}
	return chartDetail(r, ch), nil
}

// valueOverrides returns the values that differ from the chart defaults, so
// the release stores only the changes, like helm install -f values.yaml.
func valueOverrides(vals, defaults map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range vals {
		d, ok := defaults[k]
		if !ok {
			out[k] = v
			continue
		}
		vm, vok := v.(map[string]any)
		dm, dok := d.(map[string]any)
		if vok && dok {
			if sub := valueOverrides(vm, dm); len(sub) > 0 {
				out[k] = sub
			}
			continue
		}
		if !reflect.DeepEqual(v, d) {
			out[k] = v
		}
	}
	return out
}

// ReloadHelmRepos reads the repository list again after a change.
func (c *Cluster) ReloadHelmRepos() { c.loadHelmRepos() }

// HelmInstall installs a chart as a new release, like helm install. A dry
// run renders the chart, and the API server checks every object with a
// server-side dry run. A dry run changes nothing in the cluster.
func (c *Cluster) HelmInstall(h *Charts, spec InstallSpec) (*InstallResult, error) {
	name, ns := strings.TrimSpace(spec.Name), strings.TrimSpace(spec.NS)
	if err := chartutil.ValidateReleaseName(name); err != nil {
		return nil, fmt.Errorf("the release name %q is not valid. Use 53 or fewer lowercase letters, digits and dashes", name)
	}
	if errs := validation.IsDNS1123Label(ns); len(errs) > 0 {
		return nil, fmt.Errorf("the namespace %q is not valid: %s", ns, errs[0])
	}
	vals := map[string]any{}
	if strings.TrimSpace(spec.Values) != "" {
		if err := yaml.Unmarshal([]byte(spec.Values), &vals); err != nil {
			return nil, fmt.Errorf("the values are not valid YAML: %w", err)
		}
	}
	ch, r, err := h.load(spec.Ref, spec.Version)
	if err != nil {
		return nil, err
	}
	if t := ch.Metadata.Type; t != "" && t != "application" {
		return nil, fmt.Errorf("%s is a %s chart. You can install only application charts", r.base, t)
	}
	if req := ch.Metadata.Dependencies; len(req) > 0 {
		if err := action.CheckDependencies(ch, req); err != nil {
			return nil, fmt.Errorf("the chart %s is not complete: %s", r.base, err)
		}
	}
	vals = valueOverrides(vals, ch.Values)
	cfg, err := c.helmConfig(ns)
	if err != nil {
		return nil, err
	}
	if hist, err := cfg.Releases.History(name); err == nil && len(hist) > 0 {
		return nil, fmt.Errorf("the release name %s is in use in the namespace %s. Use a different name", name, ns)
	}
	inst := action.NewInstall(cfg)
	inst.ReleaseName = name
	inst.Namespace = ns
	inst.CreateNamespace = spec.CreateNS
	inst.Wait = spec.Wait || spec.Atomic
	inst.Atomic = spec.Atomic
	inst.Timeout = time.Duration(min(max(spec.Timeout, 10), 3600)) * time.Second
	if spec.Timeout <= 0 {
		inst.Timeout = 5 * time.Minute
	}
	if spec.Dry {
		inst.DryRun = true
		inst.DryRunOption = "server"
	}
	rel, err := inst.RunWithContext(c.ctx, ch, vals)
	if err != nil {
		msg := redactURLs(err.Error())
		if strings.Contains(msg, "cannot re-use a name that is still in use") {
			return nil, fmt.Errorf("the release name %s is in use in the namespace %s. Use a different name", name, ns)
		}
		if spec.Dry {
			return nil, fmt.Errorf("dry run of %s failed: %s", name, msg)
		}
		return nil, fmt.Errorf("install of %s failed: %s", name, msg)
	}
	res := &InstallResult{Name: rel.Name, NS: rel.Namespace, Chart: ch.Metadata.Name + "-" + ch.Metadata.Version,
		Version: ch.Metadata.Version, Revision: rel.Version, Resources: countManifests(rel.Manifest), Hooks: len(rel.Hooks)}
	if rel.Info != nil {
		res.Status = string(rel.Info.Status)
		res.Notes = rel.Info.Notes
	}
	if spec.Dry {
		var b strings.Builder
		b.WriteString(strings.TrimSpace(rel.Manifest))
		for _, hk := range rel.Hooks {
			fmt.Fprintf(&b, "\n---\n# Source: %s\n# Hook: %s\n%s", hk.Path, joinEvents(hk.Events), strings.TrimSpace(hk.Manifest))
		}
		res.Manifest = b.String() + "\n"
		res.Checks, res.Note = c.serverDryRun(cfg, res.Manifest, ns, spec.CreateNS)
	}
	return res, nil
}

func joinEvents[T ~string](evs []T) string {
	s := make([]string, len(evs))
	for i, e := range evs {
		s[i] = string(e)
	}
	return strings.Join(s, ", ")
}

// maxChecks limits the list of refused objects of a dry run.
const maxChecks = 20

// serverDryRun sends every rendered object to the API server with
// dryRun=All. The API server validates each object and runs the admission
// webhooks, but stores nothing. The result is the list of refused objects
// and a note when a missing namespace limits the check.
func (c *Cluster) serverDryRun(cfg *action.Configuration, manifest, ns string, createNS bool) ([]string, string) {
	ctx, cancel := context.WithTimeout(c.ctx, chartNetTimeout)
	defer cancel()
	nsMissing := false
	if _, err := c.cs.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{}); apierrors.IsNotFound(err) {
		if !createNS {
			return []string{fmt.Sprintf("The namespace %s does not exist. Select Create namespace, or use an existing namespace.", ns)}, ""
		}
		nsMissing = true
	}
	list, err := cfg.KubeClient.Build(strings.NewReader(manifest), false)
	if err != nil {
		return []string{redactURLs(err.Error())}, ""
	}
	var out []string
	skipped := 0
	opts := metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}, FieldManager: "helm"}
	for _, info := range list {
		if info.Mapping == nil {
			continue
		}
		if nsMissing && info.Namespaced() {
			skipped++
			continue
		}
		u, ok := info.Object.(*unstructured.Unstructured)
		if !ok {
			m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(info.Object)
			if err != nil {
				continue
			}
			u = &unstructured.Unstructured{Object: m}
		}
		ri := c.dyn.Resource(info.Mapping.Resource)
		if info.Namespaced() {
			_, err = ri.Namespace(info.Namespace).Create(ctx, u, opts)
		} else {
			_, err = ri.Create(ctx, u, opts)
		}
		if err != nil && !apierrors.IsAlreadyExists(err) && len(out) < maxChecks {
			out = append(out, fmt.Sprintf("%s %s: %s", info.Mapping.GroupVersionKind.Kind, info.Name, errString(err)))
		}
	}
	note := ""
	switch {
	case skipped > 0 && skipped == len(list):
		note = fmt.Sprintf("The namespace %s does not exist yet, so the API server cannot check the objects. Helm creates the namespace at install.", ns)
	case skipped > 0:
		note = fmt.Sprintf("The namespace %s does not exist yet, so the API server checked only the %d cluster-scoped objects. Helm creates the namespace at install.", ns, len(list)-skipped)
	}
	return out, note
}
