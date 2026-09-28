# Details, YAML editing and safe changes

Click a row to open the detail panel. Choose where it opens in Settings:

| Layout | Behavior |
| --- | --- |
| Drawer | Opens over the right side of the list. This is the default. |
| Split | Shares the space with the list. |
| Page | Replaces the list. **← Back** returns to the list. |

## Tabs

| Tab | Key | Contents |
| --- | --- | --- |
| Overview | `o` | For a pod: node, IP, QoS class, labels, conditions and each container with its state, last state, resources, ports, probes and mounts. For other kinds: the list columns, labels, annotations, the owner and the Helm release. |
| YAML | `y` | The live object, an editor and a diff. |
| Events | `e` | The events of the object, newest first. |
| Related | `r` | Owners, children, services that select the pod, mounted ConfigMaps and Secrets, the service account, the node and the namespace. Click an entry to open it. |

The panel reloads when the object changes in the cluster. When an object is deleted, the panel shows **deleted**. When a new object replaces it with the same name, for example a StatefulSet pod, the panel shows the new object.

## Edit YAML

1. Click **Edit**, or open the YAML tab and click **Edit**.
2. Change the YAML. The editor has search with `Ctrl F`, folding and YAML highlighting.
3. Click **Diff** to review your changes against the live object.
4. Click **Server dry-run**. The API server validates the change and runs admission webhooks, but stores nothing.
5. Click **Apply**.

st8ks sends the whole object with its `resourceVersion`. If someone changed the object after you opened it, the API server rejects the change with a conflict. Click **Reset** to load the live object and edit again.

The YAML view hides `managedFields` and the `kubectl.kubernetes.io/last-applied-configuration` annotation. When you apply, st8ks keeps the annotation, so a later `kubectl apply` still works.

A pod that a controller manages shows a notice. A change to the pod is lost when the controller replaces it. Click **Edit owner** to edit the Deployment or StatefulSet instead.

## Object actions

| Action | Kinds | What it does |
| --- | --- | --- |
| **Logs**, **Shell** | Pods | Opens the dock. See [Logs, shells and port-forwards](logs-shell-port-forward.md). |
| **Restart…** | Deployments, StatefulSets, DaemonSets | Sets `kubectl.kubernetes.io/restartedAt` in the pod template. Pods are replaced within `maxUnavailable` and the PodDisruptionBudgets. |
| **Scale…** | Deployments, StatefulSets, ReplicaSets | Sets the replica count through the scale subresource. |
| **Cordon…**, **Uncordon…** | Nodes | Sets `spec.unschedulable`. |
| **Run now…** | CronJobs | Creates a Job from the template, like `kubectl create job --from=cronjob/NAME`. |
| **Suspend…**, **Resume…** | CronJobs | Sets `spec.suspend`. |
| **Delete…** | All | Deletes with `propagationPolicy=Background`. |

## Confirmations

Every change opens a confirmation that lists the objects. Check **Dry run first** to send the change as a server dry run. The result shows in the dialog, and nothing changes. Clear the check box to run the change.

### Protected names

When the context name or a namespace matches the protected pattern, the dialog asks you to type a word first. For one object, the word is its name. For many objects, it is `delete 3`, `restart 2` and so on.

The default pattern matches names that contain `prod`, `production`, `prd` or `live` as a separate word:

```text
(^|[-_.])(prod|production|prd|live)($|[-_.])
```

Change the pattern in Settings > Safety. It is a JavaScript regular expression and ignores case.
