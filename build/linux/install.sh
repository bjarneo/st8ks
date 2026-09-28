#!/usr/bin/env sh
# Installs st8ks for the current user from an extracted release archive.
#
#   ./install.sh            install into ~/.local
#   PREFIX=/usr/local sudo -E ./install.sh
#   ./install.sh --uninstall
set -eu

PREFIX="${PREFIX:-$HOME/.local}"
HERE="$(cd "$(dirname "$0")" && pwd)"
BIN="$PREFIX/bin/st8ks"
DESKTOP="$PREFIX/share/applications/st8ks.desktop"
ICON="$PREFIX/share/icons/hicolor/256x256/apps/st8ks.png"

if [ "${1:-}" = "--uninstall" ]; then
  rm -f "$BIN" "$DESKTOP" "$ICON"
  echo "Removed st8ks from $PREFIX"
  exit 0
fi

if ! ldconfig -p 2>/dev/null | grep -q 'libwebkit2gtk-4.1'; then
  echo "st8ks needs WebKitGTK 4.1 and GTK 3. Install them first:" >&2
  echo "  Debian, Ubuntu:  sudo apt install libwebkit2gtk-4.1-0 libgtk-3-0" >&2
  echo "  Fedora:          sudo dnf install webkit2gtk4.1 gtk3" >&2
  echo "  Arch:            sudo pacman -S webkit2gtk-4.1 gtk3" >&2
fi

install -Dm755 "$HERE/st8ks" "$BIN"
install -Dm644 "$HERE/st8ks.png" "$ICON"
install -Dm644 "$HERE/st8ks.desktop" "$DESKTOP"
sed -i "s|^Exec=.*|Exec=$BIN|" "$DESKTOP"
if command -v update-desktop-database >/dev/null 2>&1; then
  update-desktop-database "$PREFIX/share/applications" >/dev/null 2>&1 || true
fi
if command -v gtk-update-icon-cache >/dev/null 2>&1; then
  gtk-update-icon-cache -q "$PREFIX/share/icons/hicolor" >/dev/null 2>&1 || true
fi

echo "Installed st8ks $(timeout 5 "$BIN" --version 2>/dev/null | cut -d' ' -f2) to $BIN"
case ":$PATH:" in
  *":$PREFIX/bin:"*) ;;
  *) echo "Add $PREFIX/bin to PATH to start st8ks from a terminal." ;;
esac
