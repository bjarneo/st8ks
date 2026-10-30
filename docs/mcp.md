# MCP server and Claude Code

st8ks gives AI agents read access to your cluster over the [Model Context Protocol](https://modelcontextprotocol.io). The command `st8ks mcp` runs an MCP server on stdin and stdout. st8ks can also start Claude Code in the dock with this server and with `kubectl` set to the connected context.

The server reads the same watch cache and rule engine as the app. An agent sees the same issues, tables and evidence that you see.

## Tools

| Tool | Input | Result |
| --- | --- | --- |
| `cluster_overview` | None | The context, the distribution and version, nodes, pods, CPU and memory requests, and the open issues |
| `list_issues` | `namespace`, optional | Each issue with its id, severity, title, kind, namespace, object, reason and summary |
| `explain_issue` | `id` | The evidence of one issue: findings, the suggested fix, the YAML of the object and its owner, events, and the last 80 log lines of each container. Also the `kubectl` commands that read the same data. |
| `list_resources` | `kind`, `namespace`, `filter`, `limit` | The rows of one kind with the columns of the list view, 200 rows by default and 1000 at most |
| `get_resource` | `kind`, `namespace`, `name` | The YAML without `managedFields`, the status, the owner, the Helm release, related objects and events |
| `get_logs` | `namespace`, `pod`, `container`, `previous`, `tail` | The last lines of a container log, 100 by default and 500 at most |
| `rbac_check` | `subject_kind`, `subject_name`, `subject_namespace`, `verb`, `resource`, `namespace` | If the subject may do the verb, the binding and the rule behind the answer, and the answer of the API server |
| `topology` | `namespace` | The objects of the namespace by column, and the edges between them |
| `helm_releases` | `namespace`, optional | The latest revision of each release |
| `dry_run_apply` | `yaml` | The result of a server dry run of an update to one existing object |

`kind` accepts the names of the st8ks tree, such as `Pods` and `certificates.cert-manager.io`. It also accepts the names that `kubectl` uses, such as `pod`, `deploy`, `svc` and `deployments.apps`.

Every tool has the MCP annotation `readOnlyHint`. No tool creates, changes or deletes an object. `dry_run_apply` sends the update with `dryRun=All`. The API server validates the object and runs admission, but it stores nothing.

## Secret values

The server never sends the value of a Secret. `get_resource` reads a Secret live from the API server, so st8ks redacts it before the result leaves the process:

- Each value in `data` and `stringData` becomes a note with its byte length, for example `password: '<redacted: 19 bytes>'`. The keys stay.
- The `kubectl.kubernetes.io/last-applied-configuration` annotation becomes `<redacted>`, because it holds a copy of the values.
- A Secret in the `items` of a list gets the same treatment.

If st8ks cannot parse the YAML of an object, the tool returns an error and no YAML.

`list_resources` shows only the type and the key count of a Secret. The watch cache of st8ks never holds Secret values.

## Add st8ks to Claude Code

Settings > Assistant shows the command with the path of your st8ks program, and a **Copy** button. Run it once in a terminal:

```sh
claude mcp add st8ks -- /path/to/st8ks mcp
```

The server then uses the current context of your kubeconfig. To pin a context, add `--context`:

```sh
claude mcp add st8ks-staging -- /path/to/st8ks mcp --context staging-eu
```

To check the connection, run `claude mcp list`. The line for st8ks ends with `Connected`.

The program is at `/Applications/st8ks.app/Contents/MacOS/st8ks` on macOS. The Linux `install.sh` copies it to `~/.local/bin/st8ks`.

## Add st8ks to other MCP clients

Clients that read a JSON file of servers, such as Claude Desktop, take this entry:

```json
{
  "mcpServers": {
    "st8ks": {
      "command": "/path/to/st8ks",
      "args": ["mcp", "--context", "staging-eu"]
    }
  }
}
```

The server uses the stdio transport. It writes only protocol messages to stdout, and errors to stderr.

## Command

```sh
st8ks mcp [--kubeconfig PATH]... [--context NAME]
```

| Flag | Effect |
| --- | --- |
| `--kubeconfig PATH` | Read only this kubeconfig file. Repeat the flag for more files. |
| `--context NAME` | Connect to this context. Without the flag, the server uses the current context. When the kubeconfig has no current context and only one context, the server uses that context. |

Without `--kubeconfig`, the server reads the same files as the app: `KUBECONFIG` or `~/.kube/config`, the files and folders that you added in the app, and the `~/.kube` scan. The window of st8ks does not open.

The server answers the handshake at once and connects in the background. The first tool call waits until the watches have listed their objects, for 45 seconds at most. When the connection fails, every tool returns the reason.

To find why a client cannot use the server, run the server in a terminal. Errors print to stderr. Press `Ctrl C` to stop it.

```sh
st8ks mcp --context staging-eu
```

## Claude Code in st8ks

st8ks runs Claude Code in a terminal tab of the dock. Open it in one of these ways:

- Click **Claude Code** in the top bar.
- Click **Open Claude Code** in the assistant panel.
- Press `Ctrl K` and select **Open Claude Code**.

To start with an issue, click **Open in Claude Code** on the issue in the assistant panel, or **Claude Code** on the issue in the overview. In the command palette, select **Open in Claude Code: TITLE**. The first prompt names the issue id and asks Claude to use the st8ks tools, starting with `explain_issue`.

For each session, st8ks does these steps:

1. It writes a kubeconfig with only the connected context to a file that only your user can read.
2. It sets `KUBECONFIG` to this file, so `kubectl` in the session uses the connected context.
3. It writes an MCP config file that starts `st8ks mcp` with this kubeconfig and context.
4. It starts `claude --mcp-config FILE`.

The files are in the cache folder of st8ks, `~/.cache/st8ks` on Linux and `~/Library/Caches/st8ks` on macOS. st8ks deletes them when the session ends. When you switch the context, the dock closes and the session ends.

The session starts in the folder of the IDE workspace. Without a workspace, it starts in your home folder.

Claude Code uses its own login and its own permission settings. st8ks does not need an API key for it.

If you let Claude Code run `kubectl` commands, these commands can change the cluster. The st8ks tools cannot. Read each command before you allow it.

### Requirements

- Claude Code must be on the PATH of your login shell. To install it, run `npm install -g @anthropic-ai/claude-code`. st8ks reads the login shell environment at startup, so a PATH from your shell profile works.
- The terminal needs Linux or macOS. On Windows, run `claude` in a terminal and add st8ks with `claude mcp add`.
