#!/bin/bash
# Builds one small DMG per CPU type. Run on macOS with Xcode Command Line Tools.
#   ./scripts/build-mac.sh [version] [arm64|amd64|all]
# Set MAC_SIGN_ID to a "Developer ID Application" identity to sign for release.
set -euo pipefail
VERSION="${1:-1.1.0}"
TARGET="${2:-all}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
mkdir -p dist
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

ICONSET="$WORK/GuyAPK.iconset"
mkdir -p "$ICONSET"
for s in 16 32 128 256 512; do
  sips -z "$s" "$s" build/appicon.png --out "$ICONSET/icon_${s}x${s}.png" >/dev/null
  d=$((s * 2))
  sips -z "$d" "$d" build/appicon.png --out "$ICONSET/icon_${s}x${s}@2x.png" >/dev/null
done
iconutil -c icns "$ICONSET" -o "$WORK/GuyAPK.icns"

build() {
  local goarch="$1" clangarch="$2" label="$3"
  local app="$WORK/$label/GuyAPK.app"
  mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"

  CGO_ENABLED=1 GOOS=darwin GOARCH="$goarch" \
  CC="clang -arch $clangarch" CXX="clang++ -arch $clangarch" \
  MACOSX_DEPLOYMENT_TARGET=11.0 \
  CGO_CFLAGS="-mmacosx-version-min=11.0" \
  CGO_LDFLAGS="-framework UniformTypeIdentifiers -mmacosx-version-min=11.0" \
    go build -trimpath -tags "desktop,production" \
      -ldflags "-s -w -buildid= -X main.Version=$VERSION" \
      -o "$app/Contents/MacOS/GuyAPK" .

  cp "$WORK/GuyAPK.icns" "$app/Contents/Resources/GuyAPK.icns"
  sed "s/__VERSION__/$VERSION/g" build/darwin/Info.plist > "$app/Contents/Info.plist"

  codesign --force --deep --options runtime --timestamp=none --sign "${MAC_SIGN_ID:--}" "$app"

  local stage="$WORK/$label/dmg"
  mkdir -p "$stage"
  cp -R "$app" "$stage/"
  ln -s /Applications "$stage/Applications"
  local out="dist/GuyAPK-$VERSION-macOS-$label.dmg"
  rm -f "$out"
  hdiutil create -quiet -volname "GuyAPK" -srcfolder "$stage" -fs APFS -format ULMO "$out"
  du -h "$out"
}

case "$TARGET" in
  arm64) build arm64 arm64 AppleSilicon ;;
  amd64) build amd64 x86_64 Intel ;;
  all)   build arm64 arm64 AppleSilicon; build amd64 x86_64 Intel ;;
  *) echo "unknown target: $TARGET" >&2; exit 2 ;;
esac
