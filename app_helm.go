package main

import "st8ks/internal/kube"

// helmCharts holds the parsed repository indexes and the downloaded charts.
// Repositories are local, so one cache serves every context.
var helmCharts = kube.NewCharts()

// reloadHelmRepos shows a changed repository list in the HelmRepositories table.
func (a *App) reloadHelmRepos() {
	if c := a.m.Current(); c != nil {
		c.ReloadHelmRepos()
	}
}

// HelmRepoList lists the chart repositories of the local Helm config.
func (a *App) HelmRepoList() ([]kube.ChartRepo, error) { return helmCharts.Repos() }

// HelmRepoAdd adds a chart repository, like helm repo add.
func (a *App) HelmRepoAdd(name, url, username, password string) (string, error) {
	msg, err := helmCharts.AddRepo(name, url, username, password)
	a.reloadHelmRepos()
	return msg, err
}

// HelmRepoUpdate downloads the index of one repository, or of all
// repositories when name is empty, like helm repo update.
func (a *App) HelmRepoUpdate(name string) (string, error) { return helmCharts.UpdateRepos(name) }

// HelmRepoRemove removes a chart repository, like helm repo remove.
func (a *App) HelmRepoRemove(name string) (string, error) {
	msg, err := helmCharts.RemoveRepo(name)
	a.reloadHelmRepos()
	return msg, err
}

// HelmSearch finds charts in the cached repository indexes.
func (a *App) HelmSearch(query string, devel bool) (*kube.ChartSearch, error) {
	return helmCharts.Search(query, devel)
}

// HelmChartVersions lists the versions of a chart, newest first.
func (a *App) HelmChartVersions(ref string, devel bool) ([]kube.ChartVersionInfo, error) {
	return helmCharts.Versions(ref, devel)
}

// HelmChartDetail loads the values, the README and the facts of a chart version.
func (a *App) HelmChartDetail(ref, version string) (*kube.ChartDetail, error) {
	return helmCharts.Detail(ref, version)
}

// HelmInstall installs a chart, or renders it with a server-side dry run.
func (a *App) HelmInstall(spec kube.InstallSpec) (*kube.InstallResult, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return nil, err
	}
	return c.HelmInstall(helmCharts, spec)
}
