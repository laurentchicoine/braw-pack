#!/usr/bin/env bash
# Assembles the portable download: unzip anywhere and run, nothing is installed.
#
#   build/package.sh <windows|linux>
#
# Run on the platform being packaged (Git Bash on Windows). The stripped ffmpeg must have been
# built first with build/ffmpeg/build.sh <target>. Linux needs GTK 3 and WebKitGTK 4.1 headers
# (libgtk-3-dev, libwebkit2gtk-4.1-dev). The result is written to dist/.
set -euo pipefail

TARGET="${1:?usage: package.sh <windows|linux|macos> [arm64|amd64]}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
if [ "$TARGET" = macos ]; then
  exec bash "$ROOT/build/macos/package.sh" "${2:-$(go env GOARCH)}"
fi
VERSION="$(sed -n 's/^const Version = "\(.*\)"$/\1/p' internal/core/archive.go)"
NAME="braw-pack-$VERSION-$TARGET-amd64"
STAGE="${STAGE:-$ROOT/dist/stage}/braw-pack"
FFDIR="$ROOT/build/ffmpeg/dist/$TARGET"
export GOFLAGS=-buildvcs=false

rm -rf "$STAGE" && mkdir -p "$STAGE" "$ROOT/dist"
case "$TARGET" in
  windows)
    [ -f "$FFDIR/ffmpeg.exe" ] || { echo "build the ffmpeg first: build/ffmpeg/build.sh windows" >&2; exit 1; }
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$STAGE/braw-pack.exe" ./cmd/braw-pack
    go build -trimpath -tags desktop,production -ldflags "-s -w -H windowsgui" -o "$STAGE/braw-pack-gui.exe" ./cmd/braw-pack-gui
    cp "$FFDIR/ffmpeg.exe" "$STAGE/"
    # The helper for viewing LUTs is optional: build/brawref/build.sh makes it.
    if [ -f "$ROOT/build/brawref/dist/braw-ref.exe" ]; then cp "$ROOT/build/brawref/dist/braw-ref.exe" "$STAGE/"
    else echo "note: braw-ref.exe not built, the download will not make viewing LUTs" >&2; fi
    ;;
  linux)
    [ -f "$FFDIR/ffmpeg" ] || { echo "build the ffmpeg first: build/ffmpeg/build.sh linux" >&2; exit 1; }
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$STAGE/braw-pack" ./cmd/braw-pack
    go build -trimpath -tags desktop,production,webkit2_41 -ldflags "-s -w" -o "$STAGE/braw-pack-gui" ./cmd/braw-pack-gui
    cp "$FFDIR/ffmpeg" "$STAGE/"
    chmod +x "$STAGE"/braw-pack "$STAGE"/braw-pack-gui "$STAGE"/ffmpeg
    ;;
  *) echo "unknown target $TARGET" >&2; exit 2 ;;
esac
cp "$FFDIR/BUILD-INFO.txt" "$STAGE/ffmpeg-BUILD-INFO.txt"
cp LICENSES/GPL-2.0.txt "$STAGE/ffmpeg-COPYING.txt"
cp README.md LICENSE THIRD_PARTY_NOTICES.txt "$STAGE/"
cp -r LICENSES "$STAGE/"
cp -r docs "$STAGE/"

cd "$(dirname "$STAGE")"
if [ "$TARGET" = windows ]; then
  rm -f "$ROOT/dist/$NAME.zip"
  if command -v zip >/dev/null; then zip -qr "$ROOT/dist/$NAME.zip" braw-pack
  else powershell -NoProfile -Command "Compress-Archive -Path braw-pack -DestinationPath '$(cygpath -w "$ROOT/dist/$NAME.zip")'"; fi
  ls -l "$ROOT/dist/$NAME.zip"
else
  tar -czf "$ROOT/dist/$NAME.tar.gz" braw-pack
  ls -l "$ROOT/dist/$NAME.tar.gz"
fi
