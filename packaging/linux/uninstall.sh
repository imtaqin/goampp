#!/usr/bin/env sh
set -eu

if [ -z "${HOME:-}" ]; then
  printf 'HOME is not set; refusing to remove user files.\n' >&2
  exit 1
fi

rm -f "$HOME/.local/bin/goampp"
rm -f "$HOME/.local/share/applications/goampp.desktop"
rm -f "$HOME/.local/share/icons/hicolor/512x512/apps/goampp.png"
printf 'GoAMPP application files removed from your home directory.\n'
printf 'Project files in ~/GoAMPP/www were not changed.\n'
