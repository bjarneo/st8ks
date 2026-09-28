# Development

## Requirements

| Tool | Version | Notes |
| --- | --- | --- |
| Go | The version in `go.mod` | `mise.toml` pins it for [mise](https://mise.jdx.dev) users. |
| Node.js | 20.19 or later | CI uses Node.js 24. |
| Wails CLI | The version in `go.mod` | `go install github.com/wailsapp/wails/v2/cmd/wails@v2.16.0` |
| Docker | Any recent version | Only for the local cluster. |

Platform packages:

| Platform | Install |
| --- | --- |
| Debian, Ubuntu | `sudo apt install build-essential libgtk-3-dev libwebkit2gtk-4.1-dev` |
| Fedora | `sudo dnf install gcc gtk3-devel webkit2gtk4.1-devel` |
| Arch Linux | `sudo pacman -S base-devel gtk3 webkit2gtk-4.1` |
| macOS | `xcode-select --install` |
| Windows | Nothing extra. WebView2 is part of Windows 10 and 11. |

Run `wails doctor` to check the setup.

## Run the app

```sh
make dev
```

The frontend reloads when you save a file. The backend rebuilds and restarts when you save a Go file. On Linux, the Makefile adds the build tag `webkit2_41`. Without make, run `wails dev -tags webkit2_41` on Linux and `wails dev` on macOS and Windows.

`wails dev` has its own flags and rejects `--kubeconfig`. To use a specific kubeconfig in development, set `KUBECONFIG`:

```sh
KUBECONFIG=~/.kube/st8ks-dev.yaml make dev
```

### Test the UI in a browser

`wails dev` serves the UI at `http://localhost:34115` with working calls to Go. To use a browser without the native window, set `ST8KS_HIDDEN=1`:

```sh
ST8KS_HIDDEN=1 make dev
```

This is useful for browser developer tools and for automated screenshots with Puppeteer or Playwright.

## Local cluster

`hack/dev-cluster` creates a three-node [kind](https://kind.sigs.k8s.io) cluster with workloads that fail on purpose. It needs Docker. When `kind` or `helm` is missing, the scripts download pinned versions to `hack/dev-cluster/.cache/bin`. `kubectl` runs inside the control plane container, so the host needs no kubectl.

| Command | Effect |
| --- | --- |
| `make cluster-up` | Creates the cluster. If it exists, prints how to use it. |
| `make cluster-down` | Deletes the cluster and its kubeconfig. |
| `make cluster-reset` | Deletes the cluster and creates it again. |
| `make cluster-load` | Adds 6,000 ConfigMaps for a load test. |
| `hack/dev-cluster/load.sh 20000` | Adds any number of ConfigMaps. |
| `hack/dev-cluster/load.sh --churn` | Relabels all of them, to test live updates. |

The kubeconfig goes to `~/.kube/st8ks-dev.yaml`, and the context is `kind-st8ks-dev`. st8ks finds the file through its `~/.kube` scan, so `~/.kube/config` stays unchanged. Set `ST8KS_DEV_KUBECONFIG` to use another path, and `ST8KS_DEV_CLUSTER` for another cluster name.

The cluster contains:

| Namespace | Object | What it shows |
| --- | --- | --- |
| prod | Deployment `checkout` | A crash loop from an OOM kill. The memory limit is 64 MiB. |
| prod | Deployment `web`, Helm release `web` | A failed Helm upgrade to an image tag that does not exist |
| prod | Deployments `api-gateway`, `frontend` | Healthy workloads behind the ingress `shop` |
| prod | HPA, PDB, CronJob, ConfigMap, Secret | Kinds for the lists and the relations |
| staging | Deployment `image-resizer` | An image pull failure |
| data | StatefulSet `redis` | A workload with a PersistentVolumeClaim |
| data | Deployment `analytics` | A pod that cannot be scheduled. Node `st8ks-dev-worker2` is cordoned. |
| jobs | Job `backfill-orders` | A failed job |
| all | RoleBindings for `payments-devs`, `oncall`, `alice@acme.test` | Data for the RBAC explorer |
| kube-system | metrics-server | CPU and memory data |

## Checks

| Command | Runs |
| --- | --- |
| `make ci` | Everything that CI runs: frontend build and type check, gofmt, go vet, staticcheck, govulncheck and the race tests |
| `make test` | Unit tests |
| `make race` | Unit tests with the race detector |
| `make integration` | The integration test against the local cluster |
| `make typecheck` | The TypeScript check |

The integration test drives the backend against a real cluster. It covers informers and tables, issue detection, fix proposals, dry runs, logs, exec, port-forwards, Helm, RBAC, the topology and metrics:

```sh
make cluster-up
make integration
```

## Code conventions

- Go code passes `gofmt`, `go vet` and staticcheck.
- The frontend passes `tsc --noEmit` with strict settings.
- Keep the backend fast. Build rows once per change, send only changed rows, and never block the informer handlers.
- Keep the frontend fast. Select the smallest state with `useStore`, memoize rows, and render lists with `VTable`.
- Documentation and UI text follow ASD-STE100 Simplified Technical English: short sentences, active voice, one instruction per sentence.

## Add a built-in kind

1. Add an entry to `builtinKinds` in `internal/kube/kinds.go` with its columns, typed client and actions.
2. Write a row builder. Put the cells in the same order as the columns, and give each status cell a tone.
3. Run `make ci` and `make integration`.

Custom resources need no code. st8ks builds them from the CRD printer columns.

## Build a release locally

```sh
make build
scripts/package.sh linux-amd64
```

`dist/` then holds the same archive that the release workflow publishes. See the [release process](releasing.md).
