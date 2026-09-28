# Keyboard shortcuts

On macOS, use `⌘` where the table shows `Ctrl`.

## Everywhere

| Key | Action |
| --- | --- |
| `Ctrl K` | Open or close the command palette |
| `Esc` | Close the top dialog, the detail panel or the assistant, or leave a text box |
| `a` | Show or hide the assistant |
| `/` | Focus the filter of the list |

## Lists

| Key | Action |
| --- | --- |
| `j` or `↓` | Move the cursor down |
| `k` or `↑` | Move the cursor up |
| `Enter` | Open the object under the cursor |
| `x` | Select or clear the object under the cursor |

In the filter box, `Enter` or `↓` moves to the list, and `Esc` clears the filter.

## Detail panel

| Key | Action |
| --- | --- |
| `o` | Overview tab |
| `y` | YAML tab |
| `e` | Events tab |
| `r` | Related tab |
| `l` | Logs of the pod |
| `s` | Shell in the pod |

## Command palette

| Key | Action |
| --- | --- |
| `↑`, `↓` | Move the selection |
| `Enter` | Run the selected entry |
| `Esc` | Close the palette |

The palette finds resources by name, and runs these commands:

- `logs: NAME` and `shell: NAME` for pods.
- `Switch context: NAME`.
- Every tree entry, such as `Pods` or `RBAC Explorer`.
- `Explain: ISSUE` for each open issue.
- Theme, density, detail layout, overview style and Settings.
- Add a kubeconfig file or folder, reload kubeconfig files, and stop all port-forwards.

The palette matches the start of words first, then any part of the name, then the letters in order. For example, `chk` finds `checkout`.

## YAML editor

The editor uses the standard CodeMirror keys. `Ctrl F` searches, `Ctrl Z` undoes, and `Tab` indents.

Single-letter shortcuts do not work while the focus is in a text box, the YAML editor or a terminal.
