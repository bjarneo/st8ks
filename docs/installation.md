# Installation

Every release has builds for macOS, Windows and Linux. Get them from the [latest release](../../../releases/latest).

| Platform | File | Contents |
| --- | --- | --- |
| macOS 12 or later, Apple silicon and Intel | `st8ks-darwin-universal.dmg` | Disk image with the app |
| | `st8ks-darwin-universal.zip` | The same app as a zip archive |
| Windows 10 or 11, x64 | `st8ks-windows-amd64-setup.exe` | Installer |
| | `st8ks-windows-amd64.zip` | Portable `st8ks.exe` |
| Windows 11, Arm | `st8ks-windows-arm64-setup.exe` | Installer |
| | `st8ks-windows-arm64.zip` | Portable `st8ks.exe` |
| Linux x86-64 | `st8ks-linux-amd64.tar.gz` | Binary, desktop entry, icon and `install.sh` |
| Linux Arm64 | `st8ks-linux-arm64.tar.gz` | Binary, desktop entry, icon and `install.sh` |
| All | `checksums.txt` | SHA256 checksums of all files |

## macOS

1. Open `st8ks-darwin-universal.dmg`.
2. Drag st8ks to Applications.
3. Start st8ks from Applications.

A release that the maintainers sign with a Developer ID and notarize opens without a prompt. The release notes show the signing of each build under "How this release was built".

If the build has an ad hoc signature, macOS blocks the first start. To allow it, open System Settings > Privacy & Security and click **Open Anyway**. Or remove the quarantine flag:

```sh
xattr -dr com.apple.quarantine /Applications/st8ks.app
```

st8ks reads the environment of your login shell when it starts. Kubeconfig exec plugins such as `aws`, `gke-gcloud-auth-plugin` and `kubelogin` then work from the Dock too.

## Windows

1. Run `st8ks-windows-amd64-setup.exe`, or the Arm installer on an Arm device.
2. Start st8ks from the Start menu.

The Windows builds are not signed. If SmartScreen shows "Windows protected your PC", click **More info**, then **Run anyway**.

st8ks uses Microsoft Edge WebView2. Windows 10 and 11 include it. When it is missing, the installer downloads it.

For a portable copy, extract `st8ks-windows-amd64.zip` and run `st8ks.exe`.

## Linux

st8ks needs GTK 3, WebKitGTK 4.1 and glibc 2.39 or later. Ubuntu 24.04, Debian 13, Fedora 40 and Arch Linux meet these requirements.

1. Install the libraries:

   | Distribution | Command |
   | --- | --- |
   | Debian, Ubuntu | `sudo apt install libwebkit2gtk-4.1-0 libgtk-3-0` |
   | Fedora | `sudo dnf install webkit2gtk4.1 gtk3` |
   | Arch Linux | `sudo pacman -S webkit2gtk-4.1 gtk3` |

2. Download and extract the archive for your CPU:

   ```sh
   curl -fsSLO https://github.com/bjarneo/st8ks/releases/latest/download/st8ks-linux-amd64.tar.gz
   tar -xzf st8ks-linux-amd64.tar.gz
   ```

3. Install st8ks for your user:

   ```sh
   ./st8ks-linux-amd64/install.sh
   ```

   The script copies `st8ks` to `~/.local/bin`, and adds a desktop entry and an icon. To install for all users, run `PREFIX=/usr/local sudo -E ./st8ks-linux-amd64/install.sh`.

To remove st8ks, run `./st8ks-linux-amd64/install.sh --uninstall`.

## Verify a download

1. Download `checksums.txt` from the same release.
2. Compare the checksums:

   ```sh
   sha256sum -c checksums.txt --ignore-missing
   ```

On a public repository, every file also has a build provenance attestation. To check that GitHub Actions built a file from the st8ks repository, run:

```sh
gh attestation verify st8ks-linux-amd64.tar.gz --repo bjarneo/st8ks
```

## Build from source

See [Development](development.md#requirements) for the tools, then run:

```sh
make build
```

## Next step

[Getting started](getting-started.md) shows the main screens.
