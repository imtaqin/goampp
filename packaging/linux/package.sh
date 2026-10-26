#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
VERSION="${VERSION:-dev}"
VERSION_NUM="${VERSION#v}"
if [[ "$VERSION_NUM" == "dev" ]]; then
  PACKAGE_VERSION="0.0.0"
else
  PACKAGE_VERSION="$VERSION_NUM"
fi
OUT="${OUTPUT_DIR:-$ROOT/dist/packages}"
BIN="$ROOT/dist/linux/goampp-linux"
ICON="$ROOT/dist/linux/goampp-icon-512.png"
STAGE="$ROOT/dist/linux-bundle"
APPDIR="$OUT/GoAMPP.AppDir"
mkdir -p "$OUT" "$ROOT/dist/linux"

if [[ ! -x "$BIN" ]]; then
  echo "Linux binary not found at $BIN; build it first." >&2
  exit 1
fi
if ! command -v convert >/dev/null 2>&1; then
  echo "ImageMagick 'convert' is required to generate standard-size application icons." >&2
  exit 1
fi
convert "$ROOT/assets/logo.png" -resize 512x512\! "$ICON"

# Standalone archive keeps the existing per-user install/uninstall workflow.
rm -rf "$STAGE"
mkdir -p "$STAGE"
install -m 0755 "$BIN" "$STAGE/goampp-linux"
install -m 0644 "$ROOT/README.md" "$ROOT/packaging/linux/goampp.desktop" "$STAGE/"
install -m 0755 "$ROOT/packaging/linux/install.sh" "$ROOT/packaging/linux/uninstall.sh" "$STAGE/"
install -m 0644 "$ICON" "$STAGE/logo.png"
tar -czf "$OUT/goampp-$VERSION-linux-amd64.tar.gz" -C "$STAGE" goampp-linux README.md goampp.desktop install.sh uninstall.sh logo.png

# Native Debian/RPM packages install the binary, desktop entry and icon in
# system locations. No post-install script starts services or changes config.
CONFIG="$OUT/nfpm.yaml"
sed "s/@VERSION@/$PACKAGE_VERSION/g" "$ROOT/packaging/linux/nfpm.yaml.in" > "$CONFIG"
(
  cd "$ROOT"
  nfpm package --packager deb --target "$OUT/goampp-$VERSION-linux-amd64.deb" --config "$CONFIG"
  nfpm package --packager rpm --target "$OUT/goampp-$VERSION-linux-amd64.rpm" --config "$CONFIG"
)

# AppImage uses linuxdeploy to bundle non-system shared libraries. The host's
# display server and graphics driver remain host-provided, as required by GUI apps.
LINUXDEPLOY="${LINUXDEPLOY:-$OUT/.tools/linuxdeploy-x86_64.AppImage}"
if [[ ! -x "$LINUXDEPLOY" ]]; then
  mkdir -p "$(dirname -- "$LINUXDEPLOY")"
  curl -fsSL --retry 3 \
    https://github.com/linuxdeploy/linuxdeploy/releases/download/continuous/linuxdeploy-x86_64.AppImage \
    -o "$LINUXDEPLOY"
  chmod +x "$LINUXDEPLOY"
fi
rm -rf "$APPDIR"
mkdir -p "$APPDIR/usr/bin" "$APPDIR/usr/share/applications" "$APPDIR/usr/share/icons/hicolor/512x512/apps"
install -m 0755 "$BIN" "$APPDIR/usr/bin/goampp"
install -m 0644 "$ICON" "$APPDIR/usr/share/icons/hicolor/512x512/apps/goampp.png"
install -m 0644 "$ICON" "$APPDIR/goampp.png"
sed 's/^Exec=.*/Exec=AppRun/' "$ROOT/packaging/linux/goampp.desktop" > "$APPDIR/goampp.desktop"
install -m 0644 "$APPDIR/goampp.desktop" "$APPDIR/usr/share/applications/goampp.desktop"
cat > "$APPDIR/AppRun" <<'APPRUN'
#!/usr/bin/env sh
HERE="$(CDPATH= cd -- "$(dirname -- "$(readlink -f -- "$0")")" && pwd)"
exec "$HERE/usr/bin/goampp" "$@"
APPRUN
chmod 0755 "$APPDIR/AppRun"

(
  cd "$OUT"
  rm -f "$OUT/GoAMPP-x86_64.AppImage" "$OUT/goampp-$VERSION-linux-amd64.AppImage"
  APPIMAGE_EXTRACT_AND_RUN=1 ARCH=x86_64 "$LINUXDEPLOY" \
    --appdir "$APPDIR" \
    --executable "$APPDIR/usr/bin/goampp" \
    --desktop-file "$APPDIR/goampp.desktop" \
    --icon-file "$APPDIR/goampp.png" \
    --output appimage
  generated="$(find "$OUT" -maxdepth 1 -type f -name '*.AppImage' -print -quit)"
  if [[ -z "$generated" ]]; then
    echo "linuxdeploy did not produce an AppImage." >&2
    exit 1
  fi
  mv "$generated" "$OUT/goampp-$VERSION-linux-amd64.AppImage"
)

rm -rf "$STAGE" "$APPDIR" "$CONFIG"
printf 'Linux packages created in %s:\n' "$OUT"
find "$OUT" -maxdepth 1 -type f -name "goampp-$VERSION-linux-amd64.*" -printf '  %f (%s bytes)\n' | sort
