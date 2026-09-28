#!/usr/bin/env bash
# Packages the output of `wails build` into release assets in dist/.
#
#   scripts/package.sh linux-amd64
#   scripts/package.sh linux-arm64
#   scripts/package.sh darwin-universal
#   scripts/package.sh windows-amd64
#   scripts/package.sh windows-arm64
#
# The release workflow runs this script after `wails build`. You can run it
# locally too. On macOS it signs the app ad hoc unless these variables hold a
# Developer ID certificate and notarization credentials:
#
#   MACOS_CERTIFICATE           base64 of a .p12 file
#   MACOS_CERTIFICATE_PASSWORD  password of the .p12 file
#   MACOS_SIGNING_IDENTITY      for example "Developer ID Application: Name (TEAMID)"
#   APPLE_ID, APPLE_TEAM_ID, APPLE_APP_PASSWORD   for notarytool
set -euo pipefail

TARGET="${1:?usage: scripts/package.sh <target>}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="$ROOT/build/bin"
DIST="$ROOT/dist"
mkdir -p "$DIST"

log() { printf '==> %s\n' "$*"; }

zip_dir() { # zip_dir <archive> <folder in the current directory>
  if command -v 7z >/dev/null 2>&1; then
    7z a -tzip -mx=9 "$1" "$2" >/dev/null
  elif command -v zip >/dev/null 2>&1; then
    zip -qr9 "$1" "$2"
  else
    powershell -NoProfile -Command "Compress-Archive -Path '$2' -DestinationPath '$1' -Force"
  fi
}

package_linux() {
  local name="st8ks-$TARGET" stage
  stage="$(mktemp -d)/$name"
  mkdir -p "$stage"
  install -m755 "$BIN/st8ks" "$stage/st8ks"
  install -m644 "$ROOT/build/linux/st8ks.desktop" "$stage/st8ks.desktop"
  install -m755 "$ROOT/build/linux/install.sh" "$stage/install.sh"
  if command -v magick >/dev/null 2>&1; then
    magick "$ROOT/build/appicon.png" -resize 256x256 "$stage/st8ks.png"
  elif command -v convert >/dev/null 2>&1; then
    convert "$ROOT/build/appicon.png" -resize 256x256 "$stage/st8ks.png"
  else
    install -m644 "$ROOT/build/appicon.png" "$stage/st8ks.png"
  fi
  tar -C "$(dirname "$stage")" -czf "$DIST/$name.tar.gz" "$name"
  log "Wrote dist/$name.tar.gz"
}

sign_macos() {
  local app="$1"
  if [ -z "${MACOS_CERTIFICATE:-}" ]; then
    # An ad hoc signature lets the app run on Apple silicon. Gatekeeper still
    # asks the user to confirm the first start.
    codesign --force --deep --sign - "$app"
    log "Signed $app ad hoc"
    return
  fi
  local keychain="${RUNNER_TEMP:-/tmp}/st8ks-signing.keychain-db"
  local kpass
  kpass="$(openssl rand -hex 16)"
  echo "$MACOS_CERTIFICATE" | base64 --decode > "${RUNNER_TEMP:-/tmp}/st8ks-cert.p12"
  security create-keychain -p "$kpass" "$keychain"
  security set-keychain-settings -lut 21600 "$keychain"
  security unlock-keychain -p "$kpass" "$keychain"
  security import "${RUNNER_TEMP:-/tmp}/st8ks-cert.p12" -k "$keychain" -P "$MACOS_CERTIFICATE_PASSWORD" -T /usr/bin/codesign
  security set-key-partition-list -S apple-tool:,apple: -s -k "$kpass" "$keychain" >/dev/null
  local existing=() k
  while IFS= read -r k; do
    k="${k//\"/}"
    existing+=("${k#"${k%%[![:space:]]*}"}")
  done < <(security list-keychains -d user)
  security list-keychains -d user -s "$keychain" "${existing[@]}"
  rm -f "${RUNNER_TEMP:-/tmp}/st8ks-cert.p12"
  codesign --force --deep --options runtime --timestamp --sign "$MACOS_SIGNING_IDENTITY" "$app"
  codesign --verify --deep --strict "$app"
  log "Signed $app with $MACOS_SIGNING_IDENTITY"
}

notarize_macos() { # notarize_macos <file>
  if [ -z "${APPLE_ID:-}" ] || [ -z "${MACOS_CERTIFICATE:-}" ]; then
    return 1
  fi
  xcrun notarytool submit "$1" --apple-id "$APPLE_ID" --team-id "$APPLE_TEAM_ID" \
    --password "$APPLE_APP_PASSWORD" --wait
}

package_darwin() {
  local app="$BIN/st8ks.app" zip="$DIST/st8ks-$TARGET.zip" dmg="$DIST/st8ks-$TARGET.dmg"
  sign_macos "$app"
  ditto -c -k --keepParent "$app" "$zip"
  if notarize_macos "$zip"; then
    xcrun stapler staple "$app"
    rm -f "$zip"
    ditto -c -k --keepParent "$app" "$zip"
    log "Notarized and stapled $app"
  fi
  local stage
  stage="$(mktemp -d)/st8ks"
  mkdir -p "$stage"
  cp -R "$app" "$stage/"
  ln -s /Applications "$stage/Applications"
  hdiutil create -volname st8ks -srcfolder "$stage" -ov -format UDZO "$dmg" >/dev/null
  if [ -n "${MACOS_CERTIFICATE:-}" ]; then
    codesign --force --timestamp --sign "$MACOS_SIGNING_IDENTITY" "$dmg"
    if notarize_macos "$dmg"; then
      xcrun stapler staple "$dmg"
    fi
  fi
  log "Wrote dist/st8ks-$TARGET.zip and dist/st8ks-$TARGET.dmg"
}

package_windows() {
  local arch="${TARGET#windows-}" name="st8ks-$TARGET" stage
  stage="$(mktemp -d)"
  mkdir -p "$stage/$name"
  cp "$BIN/st8ks.exe" "$stage/$name/st8ks.exe"
  (cd "$stage" && zip_dir "$DIST/$name.zip" "$name")
  log "Wrote dist/$name.zip"
  local installer="$BIN/st8ks-$arch-installer.exe"
  if [ -f "$installer" ]; then
    cp "$installer" "$DIST/st8ks-$TARGET-setup.exe"
    log "Wrote dist/st8ks-$TARGET-setup.exe"
  fi
}

case "$TARGET" in
  linux-amd64 | linux-arm64) package_linux ;;
  darwin-universal) package_darwin ;;
  windows-amd64 | windows-arm64) package_windows ;;
  *) echo "unknown target: $TARGET" >&2; exit 2 ;;
esac
