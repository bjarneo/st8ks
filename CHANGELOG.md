# Release notes

This file lists what changed for you in each release of st8ks. The [release page](https://github.com/bjarneo/st8ks/releases) of each version also lists every commit, the files and their checksums.

## v0.3.0

Released on 2026-10-30.

### New

- **Claude Code and MCP.** `st8ks mcp` serves an MCP server with read-only tools for issues, resources, logs, RBAC, topology and Helm releases. One more tool runs a server dry run. Secret values never leave st8ks. Click **Claude Code** in the top bar to run Claude Code in the dock with the connected context. Each issue has an **Open in Claude Code** button. See [MCP server and Claude Code](docs/mcp.md).
- **Metrics history.** The **Metrics** tab, key `M`, shows CPU and memory charts for pods, workloads and nodes from Prometheus or VictoriaMetrics. The charts show the requests and limits, for 1 hour to 7 days. st8ks finds Prometheus itself and queries it through the API server. The evidence of an OOM kill includes the memory peak. See [Metrics history](docs/metrics.md).
- **GitOps.** Argo CD Applications and Flux objects that fail show in **Needs attention** with evidence. The overview counts them. **Sync**, **Refresh**, **Reconcile**, **Suspend** and **Resume** open a confirmation with an optional server dry run. See [GitOps](docs/gitops.md).
- **Helm chart install.** Click **Install chart…** in the Helm view to search your chart repositories or paste an `oci://` reference. Edit the values, compare them with the defaults, run a server dry run, then install. Add, update and remove chart repositories in st8ks. See [Helm charts and releases](docs/helm.md#install-a-chart).
- **Website.** st8ks has a website at <https://bjarneo.github.io/st8ks/>, with a promo video and these release notes.

### Fixes

- Go 1.27.3 and `golang.org/x/net` v0.60.0 fix 9 vulnerabilities that govulncheck reported in v0.2.0.

### Development

- `make cluster-prometheus` adds Prometheus to the local kind cluster. `make cluster-gitops` adds the Argo CD and Flux CRDs with sample objects that fail.
- The release notes start with the section of this file for the tag.

## v0.2.0

Released on 2026-09-29.

### New

- **IDE for manifests in Git.** Switch with **Cluster** and **IDE** in the top bar. The IDE opens a folder of manifests next to the cluster views. See [IDE](docs/ide.md).
- The IDE checks YAML while you type: syntax, removed APIs, unknown fields and types from the OpenAPI schema of the cluster, selectors, resources, image tags, HPA replicas and kustomize builds.
- The IDE marks the lines that cause live problems on the target context, such as OOM kills, crash loops and image pull failures. Quick fixes change the line.
- Compare a file with the live objects, or a kustomization with a server dry run of its build. Compare a file with `HEAD`.
- Dry-run and apply with server-side apply. The IDE shows field manager conflicts and follows the rollout.
- Commit and push with Git. Open a shell whose `kubectl` uses the target context.

## v0.1.1

Released on 2026-09-28.

### New

- **Text size.** **Settings > Appearance** has a **Text size** from 80 % to 150 %. `Ctrl +` and `Ctrl -` change it in steps, and `Ctrl 0` resets it. On macOS, use `⌘`.
- **Delete contexts.** Each context card that is not connected has a **Delete** button. st8ks removes the context from its file, as `kubectl config delete-context` does. A protected context name must be typed first.
- **Remove kubeconfig sources.** The Clusters view can remove every kubeconfig source, not only added paths. st8ks deletes no file.
- **Test the API key.** **Settings > Assistant** has a **Test** button. The test also runs after you save a key.

### Fixes

- The assistant sends the saved API key to `api.anthropic.com` with no other credential. Before, `ANTHROPIC_BASE_URL` and `ANTHROPIC_AUTH_TOKEN` from the login shell sent the key to another server, and the API rejected a valid key.
- Errors from the assistant show the type and the message from the API.
- A fast scroll no longer shows empty rows for one frame. A filter that makes the list shorter no longer shows an empty list for one frame.
- A change of theme no longer removes an added kubeconfig path or the last context.

## v0.1.0

Released on 2026-09-28.

The first release of st8ks, a Kubernetes desktop client for Linux, macOS and Windows.

- Live lists for every built-in kind and every CRD, with filters, saved views, sorting and bulk actions.
- Issue detection with evidence. Review a proposed fix as a YAML diff, or confirm an action with an optional server dry run.
- Logs, shells, debug containers and port-forwards.
- The RBAC explorer, Helm releases, the topology graph and a read-only assistant.
- Kubeconfig discovery in `KUBECONFIG`, `~/.kube` and added files, with the `--kubeconfig` and `--context` flags.
- A local kind cluster with workloads that fail on purpose: `make cluster-up`.
