#!/usr/bin/env bash
# Validate the shipped executables, not Homebrew substitutes.
set -euo pipefail
STAGE="${1:?usage: check.sh <unpacked-package> <arm64|amd64>}"
ARCH="${2:?missing architecture}"
case "$ARCH" in arm64) MACH=arm64 ;; amd64) MACH=x86_64 ;; *) exit 2 ;; esac
APP="$STAGE/braw-pack.app"
plutil -lint "$APP/Contents/Info.plist"
codesign --verify --deep --strict "$APP"
for BIN in "$STAGE/braw-pack" "$STAGE/ffmpeg" "$APP/Contents/MacOS/braw-pack-gui" "$APP/Contents/MacOS/ffmpeg"; do
  file "$BIN"
  [ "$(lipo -archs "$BIN")" = "$MACH" ]
  codesign --verify --strict "$BIN"
  # No runner-specific /opt/homebrew, /usr/local or @rpath library dependencies.
  otool -L "$BIN"
  otool -L "$BIN" | tail -n +2 | awk '{print $1}' | while read -r LIB; do
    case "$LIB" in /usr/lib/*|/System/Library/*) ;; *) echo "non-system dependency: $LIB" >&2; exit 1 ;; esac
  done
  # Both the old LC_VERSION_MIN_MACOSX and new LC_BUILD_VERSION formats are accepted.
  otool -l "$BIN" | awk '/cmd LC_BUILD_VERSION/{modern=1} /cmd LC_VERSION_MIN_MACOSX/{old=1} modern && $1=="minos"{print $2; modern=0} old && $1=="version"{print $2; old=0}' | \
    python3 -c 'import sys; v=[tuple(map(int,x.strip().split("."))) for x in sys.stdin]; assert v and all(x <= (12,0,0) for x in v), v'
done
"$STAGE/braw-pack" pack -h
"$STAGE/ffmpeg" -hide_banner -version
"$STAGE/ffmpeg" -hide_banner -h encoder=libx265 2>&1 | grep yuv420p12le
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
# Values span all 12 bits, so an accidental 8/10-bit conversion fails byte comparison.
python3 -c 'import struct,sys; open(sys.argv[1],"wb").write(b"".join(struct.pack("<H", (i*73+19)%4096) for i in range(64*64*3//2*3)))' "$TMP/in.yuv"
for FF in "$STAGE/ffmpeg" "$APP/Contents/MacOS/ffmpeg"; do
  "$FF" -v error -y -f rawvideo -pixel_format yuv420p12le -video_size 64x64 -framerate 24 -i "$TMP/in.yuv" \
    -c:v libx265 -preset ultrafast -x265-params lossless=1:log-level=error -pix_fmt yuv420p12le "$TMP/out.mkv"
  "$FF" -v error -y -i "$TMP/out.mkv" -f rawvideo -pix_fmt yuv420p12le "$TMP/out.yuv"
  cmp "$TMP/in.yuv" "$TMP/out.yuv"
done
