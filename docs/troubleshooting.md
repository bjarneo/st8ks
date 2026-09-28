# Troubleshooting

## No contexts show

st8ks shows "No clusters found" when it finds no kubeconfig file.

1. Run `kubectl config get-contexts` in a terminal. If kubectl finds no contexts either, create a kubeconfig file first.
2. Open **Clusters** and read **Kubeconfig sources**. Each source shows the number of contexts or an error.
3. If your kubeconfig is in an unusual place, click **Add file…** or **Add folder…**.

If you started st8ks with `--kubeconfig`, it reads only those files.

## A cloud context shows as unreachable

The error under the context tells the cause. The most common causes:

| Error contains | Cause | Fix |
| --- | --- | --- |
| `executable aws not found`, `gke-gcloud-auth-plugin not found`, `kubelogin not found` | st8ks cannot find the exec plugin. | Check that the plugin works in a terminal. Quit st8ks and start it again, so it reads your login shell environment. On Linux, start st8ks from a terminal once to test. |
| `ExpiredToken`, `token has expired`, `Unauthorized` | The cloud login expired. | Log in again, for example with `aws sso login` or `gcloud auth login`, then click **Retry**. |
| `context deadline exceeded`, `i/o timeout`, `no route to host` | The API server does not answer. | Check your VPN and network, then click **Retry**. |
| `x509: certificate` | The certificate does not match the kubeconfig. | Get a new kubeconfig from your cluster provider. |

## A list is empty, or shows only one namespace

- Check the namespace menu in the top bar. Choose **All namespaces**.
- A notice such as "Your credentials cannot list Pods in all namespaces" means that RBAC limits your access. st8ks then shows the namespace of the context. Set it with `kubectl config set-context --current --namespace=NAME`.
- A kind that the cluster does not serve does not show in the tree.

## CPU and memory show a dash

The CPU and memory columns and bars need metrics-server. The status bar shows `metrics-server ✕` when st8ks cannot read metrics. Install metrics-server:

```sh
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
```

The first values arrive about 30 seconds after metrics-server starts.

## The shell does not open

- "Cannot exec: container is not running" means that the container crashed or has not started. Use **Start debug container**.
- An error that says the executable is not found means that the image has no shell. Use **Start debug container**.
- "Forbidden" means that your role does not allow `create` on `pods/exec`. Check it in the [RBAC explorer](rbac.md).

## The assistant shows an error

| Error | Fix |
| --- | --- |
| No Anthropic API key is set | Add a key in Settings > Assistant, or set `ANTHROPIC_API_KEY`. |
| The Anthropic API rejected the key | Check the key in the Anthropic Console, then save it again. |
| The model is not available for this API key | Choose another model in Settings > Assistant. |
| The rate limit is reached | Wait a minute and ask again. |

## The window is blank on Linux

WebKitGTK can fail with some NVIDIA drivers under Wayland. Start st8ks with:

```sh
WEBKIT_DISABLE_DMABUF_RENDERER=1 st8ks
```

To make it permanent, add the variable to the `Exec` line of `~/.local/share/applications/st8ks.desktop`:

```ini
Exec=env WEBKIT_DISABLE_DMABUF_RENDERER=1 /home/you/.local/bin/st8ks
```

## macOS says that st8ks is damaged, or cannot check it

The build has no Developer ID signature. Allow it once:

```sh
xattr -dr com.apple.quarantine /Applications/st8ks.app
```

## Get logs for a bug report

Start st8ks from a terminal with debug logs:

```sh
ST8KS_DEBUG=1 st8ks 2>&1 | tee st8ks.log
```

Remove tokens and host names that you do not want to share, then attach the log to the issue.
