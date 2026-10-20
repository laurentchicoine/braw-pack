#!/usr/bin/env bash
# Native builds only; the Actions matrix supplies Apple Silicon and Intel Macs.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"
[ "$(uname -s)" = Darwin ] || { echo 'Mac packaging requires macOS (use GitHub Actions)' >&2; exit 2; }
ARCH="${1:-$(go env GOARCH)}"
case "$ARCH" in
  arm64) NATIVE=arm64 ;;
  amd64) NATIVE=x86_64 ;;
  *) echo "unsupported architecture: $ARCH" >&2; exit 2 ;;
esac
[ "$(uname -m)" = "$NATIVE" ] || { echo 'use a native runner for this architecture' >&2; exit 2; }
export GOOS=darwin GOARCH="$ARCH" MACOSX_DEPLOYMENT_TARGET=12.0
export CGO_CFLAGS="${CGO_CFLAGS:-} -mmacosx-version-min=12.0"
export CGO_LDFLAGS="${CGO_LDFLAGS:-} -mmacosx-version-min=12.0"
export GOFLAGS=-buildvcs=false
VERSION="$(sed -n 's/^const Version = "\(.*\)"$/\1/p' internal/core/archive.go)"
NAME="braw-pack-$VERSION-macos-$ARCH"
STAGE="$ROOT/dist/$NAME"
APP="$STAGE/braw-pack.app"
FFDIR="$ROOT/build/ffmpeg/dist/macos-$ARCH"
[ -f "$FFDIR/ffmpeg" ] || { echo "build ffmpeg first: ARCH=$ARCH bash build/ffmpeg/build.sh macos" >&2; exit 1; }
# All outputs are inside this target's fixed dist directory; no caller-controlled deletion.
rm -rf "$STAGE"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o "$STAGE/braw-pack" ./cmd/braw-pack
CGO_ENABLED=1 go build -trimpath -tags desktop,production -ldflags '-s -w' \
  -o "$APP/Contents/MacOS/braw-pack-gui" ./cmd/braw-pack-gui
cp "$FFDIR/ffmpeg" "$APP/Contents/MacOS/ffmpeg"
# A separate copy serves the portable CLI; moving the app never breaks its helper lookup.
cp "$FFDIR/ffmpeg" "$STAGE/ffmpeg"
sed "s/@VERSION@/$VERSION/g" build/macos/Info.plist.in > "$APP/Contents/Info.plist"
plutil -lint "$APP/Contents/Info.plist"
ICONSET="$ROOT/dist/macos-$ARCH.iconset"
mkdir -p "$ICONSET"
for SIZE in 16 32 128 256 512; do
  sips -z "$SIZE" "$SIZE" cmd/braw-pack-gui/icon/icon.png --out "$ICONSET/icon_${SIZE}x${SIZE}.png" >/dev/null
  DOUBLE=$((SIZE * 2))
  sips -z "$DOUBLE" "$DOUBLE" cmd/braw-pack-gui/icon/icon.png --out "$ICONSET/icon_${SIZE}x${SIZE}@2x.png" >/dev/null
done
iconutil -c icns "$ICONSET" -o "$APP/Contents/Resources/AppIcon.icns"
cp README.md LICENSE THIRD_PARTY_NOTICES.txt "$STAGE/"
cp -R LICENSES docs "$STAGE/"
cp "$FFDIR/BUILD-INFO.txt" "$STAGE/ffmpeg-BUILD-INFO.txt"
cp LICENSES/GPL-2.0.txt "$STAGE/ffmpeg-COPYING.txt"
cp README.md LICENSE THIRD_PARTY_NOTICES.txt "$APP/Contents/Resources/"
cp -R LICENSES "$APP/Contents/Resources/"
cp "$STAGE/ffmpeg-BUILD-INFO.txt" "$STAGE/ffmpeg-COPYING.txt" "$APP/Contents/Resources/"
chmod +x "$STAGE/braw-pack" "$STAGE/ffmpeg" "$APP/Contents/MacOS/"*
# Ad-hoc signatures make arm64 executables loadable. They are not Developer ID / notarization.
for BIN in "$STAGE/braw-pack" "$STAGE/ffmpeg" "$APP/Contents/MacOS/ffmpeg"; do
  codesign --force --sign - "$BIN"
done
codesign --force --sign - "$APP"
codesign --verify --deep --strict "$APP"
bash build/macos/check.sh "$STAGE" "$ARCH"
ditto -c -k --sequesterRsrc --keepParent "$STAGE" "$ROOT/dist/$NAME.zip"
shasum -a 256 "$ROOT/dist/$NAME.zip" > "$ROOT/dist/$NAME.zip.sha256"
ls -lh "$ROOT/dist/$NAME.zip"
