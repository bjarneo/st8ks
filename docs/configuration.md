# Settings, flags and environment variables

## Settings

Click **⚙** in the top bar, or press `Ctrl K` and type `settings`.

| Group | Setting | Default |
| --- | --- | --- |
| Appearance | Theme | Dark |
| | Density: Compact has 28-pixel rows, Comfortable has 36-pixel rows | Compact |
| | Detail layout: Drawer, Split or Page | Drawer |
| | Overview nodes: Table or Node map | Table |
| | Text size, from 80 % to 150 %. Rows, columns and the terminal grow with the text. | 100 % |
| Assistant | Anthropic API key | Not set |
| | Model | `claude-opus-5` |
| Safety | Protected names, a regular expression | `(^\|[-_.])(prod\|production\|prd\|live)($\|[-_.])` |
| Logs | Initial lines | 5000 |
| Kubeconfig | Added files and folders, removed files, and the `~/.kube` scan | Scan on |

st8ks also remembers the last context, the selected namespaces of each context, collapsed tree groups and saved views.

### Settings file

| Platform | Path |
| --- | --- |
| Linux | `~/.config/st8ks/settings.json` |
| macOS | `~/Library/Application Support/st8ks/settings.json` |
| Windows | `%AppData%\st8ks\settings.json` |

Only your user can read the file, because it can hold the API key. st8ks writes it atomically. To go back to the defaults, quit st8ks and delete the file.

## Command-line flags

| Flag | Effect |
| --- | --- |
| `--kubeconfig PATH` | Read only this kubeconfig file. Repeat the flag, or separate paths with `:`, or `;` on Windows. |
| `--context NAME` | Open this context first. |
| `--version` | Print the version and quit. |

```sh
st8ks --kubeconfig ~/clusters/lab.yaml --context kind-lab
```

`wails dev` uses its own flags, so the `--kubeconfig` flag does not work in development. Set `KUBECONFIG` instead.

## Environment variables

| Variable | Effect |
| --- | --- |
| `KUBECONFIG` | The primary kubeconfig files, as for kubectl. |
| `ANTHROPIC_API_KEY` | The API key for the assistant, when Settings has none. |
| `ST8KS_NO_SHELL_ENV=1` | Do not read the environment of the login shell at startup. |
| `ST8KS_DEBUG=1` | Print client-go logs to the terminal. |
| `ST8KS_HIDDEN=1` | Start without a window. The development server then serves the UI to a browser. |
| `WEBKIT_DISABLE_DMABUF_RENDERER=1` | Linux only. Fixes a blank window with some NVIDIA drivers under Wayland. |
