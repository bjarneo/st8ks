# GitOps: Argo CD and Flux

st8ks shows Argo CD Applications and Flux objects with their sync, health and readiness. A GitOps object that fails becomes an issue in **Needs attention**, with evidence and a fix action, like a crash loop. The detail panel of these objects has the actions **Sync**, **Refresh**, **Reconcile**, **Suspend** and **Resume**.

st8ks needs no Argo CD or Flux CLI and no access to the Argo CD API server. It reads and changes the custom resources through the Kubernetes API.

## Watched kinds

When the CRDs exist in the cluster, st8ks watches these kinds from the moment it connects. Other custom resources start their watch only when you open their list.

| Tool | Kind | Resource |
| --- | --- | --- |
| Argo CD | Application | `applications.argoproj.io` |
| Flux | Kustomization | `kustomizations.kustomize.toolkit.fluxcd.io` |
| Flux | HelmRelease | `helmreleases.helm.toolkit.fluxcd.io` |
| Flux | GitRepository | `gitrepositories.source.toolkit.fluxcd.io` |
| Flux | OCIRepository | `ocirepositories.source.toolkit.fluxcd.io` |
| Flux | HelmRepository | `helmrepositories.source.toolkit.fluxcd.io` |
| Flux | HelmChart | `helmcharts.source.toolkit.fluxcd.io` |
| Flux | Bucket | `buckets.source.toolkit.fluxcd.io` |

The lists are in **Custom Resources** in the tree. When a CRD appears while st8ks is connected, the watch starts within a second.

## Columns

st8ks replaces the printer columns of the CRDs with columns that have a color for each state.

| Kind | Columns |
| --- | --- |
| Application | Health, Sync, Last sync, Revision, Project |
| Kustomization | Ready, Reason, Suspended, Source, Revision, Message |
| HelmRelease | Ready, Reason, Suspended, Chart, Revision, Message |
| HelmChart | Ready, Reason, Suspended, Chart, Revision, Message |
| GitRepository, OCIRepository, HelmRepository | Ready, Reason, Suspended, URL, Revision, Message |
| Bucket | Ready, Reason, Suspended, Endpoint, Revision, Message |

**Last sync** is the phase of `status.operationState`. It shows `Running` while the `operation` field is set. **Revision** shows a short commit SHA or digest. A row with a warning or an error state counts as unhealthy in the tree and in the **Unhealthy** view of the list.

## Overview

When the cluster has Argo CD Applications or Flux objects, the overview shows a row with two cells under the workload summary:

- **Argo CD apps**: the healthy count and the total count of Applications.
- **Flux objects**: the same counts for all Flux kinds together.

A failing object is an object with an issue. Click a cell to open the list. When an object fails, the list opens on the kind of the first failing object, in the **Unhealthy** view.

## Issues

### Argo CD Applications

st8ks creates one issue for each Application. When more than one rule applies, the reasons join, for example `OperationFailed · Degraded · OutOfSync`. The title, the summary and the fix action come from the first rule in this table.

| Rule | Severity | Evidence | Fix action |
| --- | --- | --- | --- |
| `status.operationState.phase` is `Failed` or `Error` | Error | The operation message, the failed resources of the sync result, the retry count | **Sync** |
| A condition type ends in `Error`, such as `ComparisonError`, `InvalidSpecError` or `SyncError` | Error | The condition message | **Refresh**. **Sync** for `SyncError`. |
| `status.health.status` is `Degraded` | Error | The health message and the degraded resources from `status.resources` with their messages | **Sync** |
| `status.health.status` is `Missing` | Warning | The missing resources | **Sync** |
| `status.sync.status` is `OutOfSync` for 2 minutes | Warning | The time out of sync and the resources that are out of sync | **Sync** |

The 2 minutes of the OutOfSync rule keep a normal sync out of **Needs attention**:

- While a sync runs, OutOfSync does not count. A sync runs when the `operation` field is set, or when the phase is `Running` or `Terminating`.
- When an Application goes out of sync while st8ks watches it, the 2 minutes start at that moment.
- When st8ks sees an Application for the first time, the 2 minutes start at the end of the last sync, or at the creation of the Application.

While a sync runs, the fix action is **Refresh**, because a second sync cannot start.

### Flux objects

The rules apply to all the Flux kinds in the table above. st8ks creates one issue for each object.

| Rule | Severity | Evidence | Fix action |
| --- | --- | --- | --- |
| The `Stalled` condition is `True` | Error | The `Stalled` and `Ready` conditions | **Reconcile** |
| The `Ready` condition is `False` | Error | The reason and the message of `Ready`, the revision | **Reconcile** |
| The `Ready` condition is `Unknown` for 10 minutes | Warning | The reason and the message of `Ready` | **Reconcile** |
| The object has no `Ready` condition 10 minutes after its creation | Warning | The age of the object | **Reconcile** |
| `spec.suspend` is `true` and one of the rules above applies | Warning | The same evidence | **Resume** |

A suspended object that does not fail has no issue.

When a Kustomization or a HelmRelease fails, st8ks follows its source and names each source that is not ready:

- A Kustomization uses `spec.sourceRef`.
- A HelmRelease uses `spec.chartRef`, or the HelmChart in `status.helmChart`. helm-controller creates this HelmChart from `spec.chart`.
- A HelmChart uses `spec.sourceRef`, for example a HelmRepository.

st8ks follows up to three sources. When a source fails, the issue title is "NAME KIND fails because its source fails", and the fix names the source to fix first. A source of a kind that st8ks does not watch, such as an ExternalArtifact, adds no evidence.

### Commands in the issue

Each issue lists read-only commands that show the same data:

| Tool | Commands |
| --- | --- |
| Argo CD | `argocd app get NS/NAME`, `argocd app diff NS/NAME` when the Application is out of sync, `kubectl describe application.argoproj.io NAME -n NS` |
| Flux | `flux get kustomizations NAME -n NS` or the matching noun, `flux events --for KIND/NAME -n NS`, `kubectl describe KIND.GROUP NAME -n NS` |

The suggested fix gives the CLI command of the fix action, for example `argocd app sync gitops/payments` or `flux reconcile kustomization platform -n gitops --with-source`.

## Actions

The detail panel shows the actions of the kind. The fix action in the assistant panel opens the same confirmation. Every action supports **Dry run first**, which sends the change as a server dry run. In a protected context or namespace, you type the object name to confirm.

| Action | Kinds | Change | CLI equivalent |
| --- | --- | --- | --- |
| **Sync** | Application | Sets the `operation` field | `argocd app sync NS/NAME` |
| **Refresh** | Application | Sets the annotation `argocd.argoproj.io/refresh` to `normal`, or to `hard` for a hard refresh | `argocd app get NS/NAME --refresh` or `--hard-refresh` |
| **Reconcile** | All Flux kinds | Sets the annotation `reconcile.fluxcd.io/requestedAt` to the current time | `flux reconcile kustomization NAME -n NS` |
| **Reconcile** with **Force** | HelmRelease | Also sets `reconcile.fluxcd.io/forceAt` to the same time | `flux reconcile helmrelease NAME -n NS --force` |
| **Suspend** | All Flux kinds | Sets `spec.suspend` to `true` | `flux suspend kustomization NAME -n NS` |
| **Resume** | All Flux kinds | Sets `spec.suspend` to `false` | `flux resume kustomization NAME -n NS` |

All actions are JSON merge patches with the field manager `st8ks`.

### Sync

**Sync** sets the `operation` field the way the Argo CD API server does for `argocd app sync`:

- `operation.sync.revision` is `spec.source.targetRevision`, or `HEAD` when it is empty. A multi-source Application gets `operation.sync.revisions` with one revision for each source.
- `operation.sync.source` or `operation.sync.sources` is a copy of the source of the spec.
- `operation.sync.syncOptions` and `operation.retry` come from `spec.syncPolicy`.
- `operation.sync.syncStrategy` is `hook`, the default of the CLI.
- `operation.initiatedBy.username` is `st8ks`.
- `status.operationState` is removed, like the API server does.

**Sync** does not prune. It fails when another operation runs, and when the Application changed after st8ks read it. The Argo CD controller then runs the sync and removes the `operation` field.

The same change with kubectl:

```sh
kubectl -n gitops patch application.argoproj.io payments --type merge -p \
  '{"operation":{"initiatedBy":{"username":"st8ks"},"sync":{"revision":"HEAD","syncStrategy":{"hook":{}}}}}'
```

### Reconcile

**Reconcile** fails for a suspended object. Resume the object first. The same change with kubectl:

```sh
kubectl -n gitops annotate kustomization.kustomize.toolkit.fluxcd.io apps \
  reconcile.fluxcd.io/requestedAt="$(date -u +%Y-%m-%dT%H:%M:%SZ)" --overwrite
```

## Permissions

| Feature | Verbs |
| --- | --- |
| Lists, columns, overview and issues | `get`, `list` and `watch` on the kinds above |
| All actions | `patch` on the kind of the object |

When your credentials cannot list a kind in all namespaces, st8ks watches the namespace of the context instead. See [RBAC](rbac.md).

## Try it on the local cluster

The local kind cluster has no Argo CD and no Flux. `make cluster-gitops` installs their CRDs and sample objects without controllers. See [Development](development.md#gitops-objects).

```sh
make cluster-up
make cluster-gitops
KUBECONFIG=~/.kube/st8ks-dev.yaml make dev
```
