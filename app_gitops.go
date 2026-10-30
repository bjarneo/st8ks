package main

import "st8ks/internal/kube"

// ---- GitOps ----

// ArgoSync starts a sync of an Argo CD Application, like argocd app sync.
func (a *App) ArgoSync(ref kube.Ref, dry bool) (string, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return "", err
	}
	return c.ArgoSync(ref, dry)
}

// ArgoRefresh asks Argo CD to compare an Application with Git again.
func (a *App) ArgoRefresh(ref kube.Ref, hard, dry bool) (string, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return "", err
	}
	return c.ArgoRefresh(ref, hard, dry)
}

// FluxReconcile asks a Flux controller to reconcile an object now.
func (a *App) FluxReconcile(ref kube.Ref, force, dry bool) (string, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return "", err
	}
	return c.FluxReconcile(ref, force, dry)
}

// FluxSuspend suspends a Flux object, or resumes it when suspend is false.
func (a *App) FluxSuspend(ref kube.Ref, suspend, dry bool) (string, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return "", err
	}
	return c.FluxSuspend(ref, suspend, dry)
}
