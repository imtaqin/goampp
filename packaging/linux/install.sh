#!/usr/bin/env sh
set -eu

if [ -z "${HOME:-}" ]; then
  printf 'HOME is not set; cannot install GoAMPP for a user.\n' >&2
  exit 1
fi

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
BIN_SOURCE="$SCRIPT_DIR/goampp-linux"
DESKTOP_SOURCE="$SCRIPT_DIR/goampp.desktop"
ICON_SOURCE="$SCRIPT_DIR/logo.png"

if [ ! -f "$BIN_SOURCE" ]; then
  printf 'GoAMPP binary not found: %s\n' "$BIN_SOURCE" >&2
  printf 'Extract the complete Linux release archive, then run install.sh again.\n' >&2
  exit 1
fi
if [ ! -f "$DESKTOP_SOURCE" ]; then
  printf 'Desktop entry not found: %s\n' "$DESKTOP_SOURCE" >&2
  exit 1
fi

install -Dm755 "$BIN_SOURCE" "$HOME/.local/bin/goampp"

DESKTOP_TMP=$(mktemp)
trap 'rm -f "$DESKTOP_TMP"' EXIT
trap 'exit 1' HUP INT TERM
sed '/^Exec=/d' "$DESKTOP_SOURCE" > "$DESKTOP_TMP"
printf 'Exec="%s"\n' "$HOME/.local/bin/goampp" >> "$DESKTOP_TMP"
install -Dm644 "$DESKTOP_TMP" "$HOME/.local/share/applications/goampp.desktop"

if [ -f "$ICON_SOURCE" ]; then
  install -Dm644 "$ICON_SOURCE" "$HOME/.local/share/icons/hicolor/512x512/apps/goampp.png"
fi

printf 'GoAMPP installed for %s.\n' "$(id -un)"
printf 'Binary: %s\n' "$HOME/.local/bin/goampp"
printf 'Launcher: %s\n' "$HOME/.local/share/applications/goampp.desktop"

case ":$PATH:" in
  *":$HOME/.local/bin:"*) ;;
  *) printf 'Note: add %s to PATH to launch with the goampp command.\n' "$HOME/.local/bin" ;;
esac
printf 'You can also launch GoAMPP from your desktop application menu.\n'
