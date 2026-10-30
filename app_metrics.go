package main

import "st8ks/internal/kube"

// ---- Metrics history ----

// MetricsSource finds the Prometheus of the active context and sends one
// test query to it.
func (a *App) MetricsSource() (kube.PromSource, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return kube.PromSource{}, err
	}
	return c.CheckPrometheus(), nil
}

// Metrics returns the CPU and memory history of a pod, a workload or a
// node from Prometheus. rng is 1h, 6h, 24h or 7d. The frontend calls it
// while the Metrics tab shows, so nothing polls in the background.
func (a *App) Metrics(ref kube.Ref, rng string) (*kube.MetricsData, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return nil, err
	}
	return c.Metrics(ref, rng)
}
