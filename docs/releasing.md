# Release process

A tag that starts with `v` publishes a release. `.github/workflows/release.yml` builds st8ks for five targets, packages them, and creates a GitHub release with notes, checksums and build provenance.

## Publish a release

1. Make sure that CI passes on `main`.
2. Tag the commit and push the tag:

   ```sh
   git tag v1.2.0
   git push origin v1.2.0
   ```

3. Open the **Actions** tab and follow the **Release** workflow until all jobs pass.
4. Open the release and check the notes.

A tag with a hyphen, such as `v1.2.0-beta.1`, publishes a prerelease. The notes of a prerelease list the changes since the previous tag. The notes of a final release list every change since the previous final release, including the changes of its betas.

## Test the build without a release

Run the workflow by hand: **Actions > Release > Run workflow**. It builds and packages every target and uploads the files as workflow artifacts. It does not create a release. The version is `dev-` and the short commit hash.

## What the workflow builds

| Target | Runner | Build | Files |
| --- | --- | --- | --- |
| `linux-amd64` | `ubuntu-24.04` | `wails build -platform linux/amd64 -tags webkit2_41` | `st8ks-linux-amd64.tar.gz` |
| `linux-arm64` | `ubuntu-24.04-arm` | `wails build -platform linux/arm64 -tags webkit2_41` | `st8ks-linux-arm64.tar.gz` |
| `darwin-universal` | `macos-26` | `wails build -platform darwin/universal` | `st8ks-darwin-universal.dmg`, `.zip` |
| `windows-amd64` | `windows-2025` | `wails build -platform windows/amd64 -nsis` | `st8ks-windows-amd64-setup.exe`, `.zip` |
| `windows-arm64` | `windows-2025` | `wails build -platform windows/arm64 -nsis` | `st8ks-windows-arm64-setup.exe`, `.zip` |

Hosted Arm runners are not available on every plan. If the `linux-arm64` job fails, the release ships without that file. The other targets must succeed.

## How each target is built

Each build job runs these steps:

1. Sets up Go from `go.mod`, and Node.js 24, with caches.
2. On Linux, installs `libgtk-3-dev` and `libwebkit2gtk-4.1-dev`. On Windows, installs NSIS when it is missing.
3. Installs the Wails CLI at the version in `go.mod`, so the CLI and the library always match.
4. Sets the version. The tag goes into the binary with `-ldflags "-X main.version=v1.2.0"`. The numeric part goes into `wails.json`, so the macOS and Windows version fields match. A prerelease tag such as `v1.2.0-beta.1` becomes `1.2.0` there.
5. Runs `npm ci` and builds the frontend with Vite.
6. Runs `wails build -s -clean -trimpath` with `-s -w` to strip debug data.
7. On Linux and macOS, runs `st8ks --version` and checks the output against the tag.
8. Runs `scripts/package.sh TARGET` to create the files in `dist/`.
9. Writes `build-TARGET.json` with the runner image, Go, Node.js and Wails versions, the build tags and the signing.

The release job then:

1. Downloads the files of all targets.
2. Writes `checksums.txt` with SHA256 sums.
3. Creates a build provenance attestation for every file. This step runs on public repositories only.
4. Writes the release notes: the changes with authors and commit links, a download table, a "How this release was built" table from the `build-*.json` files, verification commands, and the checksums.
5. Creates the release with the files and `checksums.txt`.

## Packages

| Platform | Package |
| --- | --- |
| Linux | A folder with `st8ks`, `st8ks.desktop`, a 256-pixel icon and `install.sh`, compressed with gzip. |
| macOS | The universal app in a disk image with a link to Applications, and the same app as a zip archive. |
| Windows | An NSIS installer that installs WebView2 when it is missing, and a zip archive with `st8ks.exe`. |

Run `scripts/package.sh` locally after `make build` to get the same package for your platform.

## Sign and notarize the macOS app

Without secrets, the workflow signs the app ad hoc. It then runs on Apple silicon, but Gatekeeper asks the user to confirm the first start. To sign with a Developer ID and notarize, add these repository secrets under **Settings > Secrets and variables > Actions**:

| Secret | Value |
| --- | --- |
| `MACOS_CERTIFICATE` | Your Developer ID Application certificate as a `.p12` file, encoded with `base64` |
| `MACOS_CERTIFICATE_PASSWORD` | The password of the `.p12` file |
| `MACOS_SIGNING_IDENTITY` | The identity, for example `Developer ID Application: Example Ltd (TEAMID123)` |
| `APPLE_ID` | The Apple ID for notarization |
| `APPLE_TEAM_ID` | Your team ID |
| `APPLE_APP_PASSWORD` | An app-specific password for that Apple ID |

To encode the certificate, run:

```sh
base64 -i DeveloperID.p12 | pbcopy
```

With the certificate, `scripts/package.sh` signs with the hardened runtime and a secure timestamp. With the Apple ID too, it notarizes and staples the app and the disk image. The release notes show the signing of each target.

The Windows builds are not signed. To sign them, add a signing step after `wails build` in `release.yml`, for example with Azure Trusted Signing.

## Verify a release

```sh
sha256sum -c checksums.txt --ignore-missing
gh attestation verify st8ks-linux-amd64.tar.gz --repo bjarneo/st8ks
```

## Keep the workflow current

Dependabot proposes updates for GitHub Actions, Go modules and npm packages every week. The Kubernetes libraries and the Helm SDK update together as one group, because they must match.

To lint the workflows locally:

```sh
docker run --rm -v "$PWD:/repo" -w /repo rhysd/actionlint:1.7.12 .github/workflows/*.yml
```
