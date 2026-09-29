# IDE

The IDE edits a folder of Kubernetes manifests, usually a Git repository with kustomize overlays. It checks each file against the schema and the live objects of the connected cluster. It shows the diff to the cluster, runs server dry runs, applies with server-side apply, and commits and pushes with Git.

## Open the IDE

1. Click **IDE** in the top bar. You can also press `Ctrl K` and run **Open the IDE**.
2. Click **Open folder…** and select the root of your repository.

st8ks remembers the folder and the mode. The next start opens the IDE with the same folder and tabs. To open another folder, click the repository name in the top bar.

To go back to the cluster views, click **Cluster** in the top bar. The IDE keeps its tabs, its terminal and its unsaved changes while the cluster views show.

## Layout

| Area | Contents |
| --- | --- |
| Top bar | The repository and branch, the target context, the command palette, **Dry-run** and **Apply…**. |
| Activity bar | **Explorer**, **Live objects** and **Source control**. |
| Explorer | The files of the folder. A file shows its number of errors and warnings, and a Git status letter: `M` modified, `A` added, `U` untracked, `R` renamed. A folder shows the color of the worst problem in it. |
| Editor | Tabs, the path of the cursor, and three views: **Source**, **Diff vs** the target context, and **Changes vs HEAD**. |
| Panel | **Problems**, **Terminal** and **Output**. Drag the top edge to change the height. |
| Inspector | The object at the cursor, what it builds, its live state, and help for the field at the cursor. The inspector shows when the window is at least 1100 pixels wide. |
| Status bar | The branch, the context, the problem counts, the cursor position and the schema version. |

For a Git repository, st8ks lists the files that Git tracks and the untracked files that `.gitignore` does not exclude. For other folders, it lists every file except `.git`, `node_modules` and similar folders. The explorer shows up to 50,000 files.

## Target context

The IDE works against the connected context of st8ks. Select another context in the context menu of the top bar. The checks, the diff, the inspector, dry runs, applies and the terminal then use the new context.

## Checks

st8ks checks every YAML file of the folder while you type. The editor underlines the problem, puts a dot in the gutter and prints the message at the end of the line. The **Problems** panel lists all problems of the folder.

| Check | Severity | Needs a cluster | Quick fix |
| --- | --- | --- | --- |
| YAML syntax, and tabs in the indentation | Error | No | Convert tabs to spaces |
| Removed API versions, such as `autoscaling/v2beta2` | Error when the cluster version no longer serves it | No | Migrate to the served version, when the schema is the same |
| API versions that the cluster does not serve, such as a missing CRD | Warning | Yes | |
| Unknown fields, from the OpenAPI schema of the cluster | Error | Yes | Rename to the closest field |
| Wrong types, such as a number for an env value or `yes` for a string | Error | Offline for env values, else yes | Quote the value, or remove the quotes |
| Missing required fields | Warning | Yes | |
| A selector that the template labels do not match | Error | No | Set the label |
| A memory or CPU request above its limit | Error | No | |
| An image with the tag `latest` or no tag | Warning | No | Pin the tag that runs on the cluster |
| A container without requests or limits | Warning | No | Add a resources block |
| A container with ports but no readiness probe | Info | No | |
| `spec.replicas` on a workload that an HPA in the folder scales | Info | No | Remove replicas |
| A kustomization path that does not exist, or a failed kustomize build | Error | No | |
| Deprecated kustomization fields, such as `bases` and `commonLabels` | Info | No | |
| Pods that are OOMKilled at the memory limit in the file | Warning | Yes | Raise the limit and the request |
| Pods in a crash loop | Warning | Yes | |
| Pods that cannot pull the image in the file or the `newTag` of a kustomization | Warning | Yes | Revert to the image of the previous revision |

The checks marked **from cluster** use the live pods of the object on the target context. To find the object, st8ks uses the namespace and the name prefix and suffix that a kustomization sets for the file. Without a kustomization, it uses the namespace in the file, and then the default namespace of the context.

st8ks does not check Helm chart folders or files with Go templates. It checks up to 5000 YAML files of up to 1 MB each.

### Quick fixes

To apply a quick fix, press `Ctrl .` on the line, or click the fix in the inspector or in the **Problems** panel. A quick fix is a normal edit, so `Ctrl Z` undoes it.

### The error lens

The message at the end of the line is the error lens. To turn it off, press `Ctrl P` and run **Turn the error lens off**.

## Compare a file

| View | Shows |
| --- | --- |
| **Source** | The editor. |
| **Diff vs** context | The live objects on the target context, compared with the file. The diff shows only the fields that the file sets, so values that the API server adds do not show. For a kustomization, the view runs a server dry run of the build and shows what an apply changes, like `kubectl diff -k`. |
| **Changes vs HEAD** | The file in the last commit, compared with the editor. |

## Inspector

| Section | Contents |
| --- | --- |
| Resource | The kind and name of the object at the cursor, and the kustomizations that build the file. |
| Builds | For a kustomization, the objects of `kustomize build`. Click an object to open its file. |
| On context | The live status, the pods, the newest warning event and the GitOps controller that manages the object. **Show diff** opens the diff when the file differs from the live object. **Open in Cluster** opens the object in the cluster views. |
| Field at cursor | The path, the type and the description from the schema of the cluster, and the value in the file next to the live value. The problems of the line show below, with their quick fixes. |

## Dry run and apply

**Dry-run** sends the active file to the API server with a server-side apply dry run. For a `kustomization.yaml`, st8ks builds the kustomization first. The output panel shows the result for each object:

```text
$ kubectl apply --server-side --field-manager=st8ks --dry-run=server -k overlays/prod --context prod-eu
deployment.apps/checkout configured (server dry run)
service/checkout unchanged (server dry run)
```

**Apply…** runs the same dry run and opens a confirmation with the diff of each object. Read the notes before you apply:

- A file that a kustomization builds gets a note. An apply of the file alone skips the changes of the build, such as the namespace and image tags. Apply the `kustomization.yaml` instead.
- An object that Argo CD, Flux or Helm manages gets a note. A sync can revert a change that is not in Git.
- When other field managers own fields that the apply changes, the dialog lists the conflicts. Select **Take over these fields** to apply with `--force-conflicts`.
- A protected context or namespace needs its name typed first. Set the pattern in Settings, under Safety.

The apply saves the file first. Then st8ks follows the rollout of each Deployment, StatefulSet and DaemonSet in the output panel, like `kubectl rollout status`.

Errors in the file stop a dry run and an apply. For a kustomization, errors in the files that it builds also stop them. Fix the errors in the **Problems** panel first.

A file without a namespace goes to the namespace of the kustomization that builds it, when that is clear. Else it goes to the default namespace of the context. The dialog shows the namespace of each object.

## Live objects

The **Live objects** view lists the Deployments, StatefulSets, Pods, Services and ConfigMaps of one namespace on the target context. The namespace follows the object at the cursor. Select another one in the list.

Click an object to open its live YAML read-only. **Open source file** finds the file in the folder that defines the object.

## Source control

The **Source control** view needs a Git repository and the `git` command.

1. Write a commit message.
2. Click **Commit**, or press `Ctrl Enter` in the message box. st8ks commits every change in the folder, also new and deleted files. Unsaved editor changes are not in the commit.
3. Click **Push**. A branch without an upstream shows **Publish** instead, which pushes to `origin`.

Git runs with your configuration, your credential helper and your SSH agent. Git cannot ask for a password, so set up a credential helper or an SSH key first. The output panel shows the output of Git.

## Terminal

The **Terminal** tab opens your shell in the folder. `KUBECONFIG` points to a file that holds only the target context, and `ST8KS_CONTEXT` holds its name. When you switch the context, st8ks rewrites the file, so the next `kubectl` command uses the new context.

```sh
kubectl get pods
kustomize build overlays/prod | kubectl diff -f -
git log --oneline -5
```

If your shell profile sets `KUBECONFIG`, the profile wins. The terminal needs Linux or macOS.

## Unsaved changes

A tab with unsaved changes shows `●`. When you quit st8ks, it keeps the unsaved changes and restores them in their tabs at the next start. Before you open another folder or close a tab, st8ks asks you to discard the unsaved changes. A file that changes on disk reloads in the editor when its tab has no unsaved changes.

## Keyboard shortcuts

On macOS, use `⌘` where the table shows `Ctrl`.

| Key | Action |
| --- | --- |
| `Ctrl P` or `Ctrl K` | Go to a file, a problem or a command |
| `Ctrl S` | Save the file |
| `Ctrl .` | Apply the quick fix of the line |
| `Ctrl Enter` | Server dry run of the file |
| `Ctrl Shift Enter` | Apply the file |
| `Ctrl J` | Show or hide the panel |
| `Ctrl F` | Search in the file |
| `Ctrl Z`, `Ctrl Shift Z` | Undo, redo |
| `Tab`, `Shift Tab` | Indent, outdent |

In the terminal, `Ctrl` keys go to the shell.

## Limits

- st8ks does not render Helm charts, and the kustomize build does not run `helmCharts`, plugins or KRM functions.
- Remote bases in a kustomization need network access and the `git` command.
- The IDE does not create, rename or delete files. Use the terminal for that.
