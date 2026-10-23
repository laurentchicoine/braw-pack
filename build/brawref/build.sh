#!/usr/bin/env bash
# Builds braw-ref.exe, the small Windows program braw-pack uses to ask Blackmagic's own decoder
# for reference frames when it fits a viewing LUT.
#
#   build/brawref/build.sh [output-dir]
#
# Runs on Linux (WSL on Windows) with mingw-w64, like the ffmpeg build. It needs the Blackmagic
# RAW SDK, which is not part of this repository: install "Blackmagic RAW 6.0" from Blackmagic Design
# and point BRAW_SDK at its "Blackmagic RAW SDK/Win" folder if it is not in the default place.
# The interface header is generated from the SDK's own description (BlackmagicRawAPI.idl) with
# widl (package mingw-w64-tools). Nothing of Blackmagic's is linked in: the decoder library is
# loaded at run time from the user's own installation.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
OUT="${1:-$HERE/dist}"
SDK="${BRAW_SDK:-/mnt/c/Program Files (x86)/Blackmagic Design/Blackmagic RAW/Blackmagic RAW SDK/Win}"
WORK="${WORK:-$(mktemp -d)}"
CXX=x86_64-w64-mingw32-g++
CC=x86_64-w64-mingw32-gcc
WIDL="$(command -v x86_64-w64-mingw32-widl || command -v widl || true)"

[ -f "$SDK/Include/BlackmagicRawAPI.idl" ] || { echo "Blackmagic RAW SDK not found at: $SDK (set BRAW_SDK)" >&2; exit 1; }
grep -q 'ReadAudioComplete' "$SDK/Include/BlackmagicRawAPI.idl" || { echo "Update the Blackmagic RAW SDK to 6.0 before building this helper." >&2; exit 1; }

mkdir -p "$OUT" "$WORK"
install -m 644 "$SDK/Include/BlackmagicRawAPI.idl" "$SDK/Include/BlackmagicRawAPIDispatch.h" "$SDK/Include/BlackmagicRawAPIDispatch.cpp" "$WORK/"
cd "$WORK"
if [ -n "${BRAW_GENERATED:-}" ]; then
  # A header already generated from the IDL (by Microsoft's MIDL, for example) can be used as is.
  install -m 644 "$BRAW_GENERATED/BlackmagicRawAPI.h" "$BRAW_GENERATED/BlackmagicRawAPI_i.c" .
else
  [ -n "$WIDL" ] || { echo "widl not found: install mingw-w64-tools, or set BRAW_GENERATED" >&2; exit 1; }
  "$WIDL" -h -o BlackmagicRawAPI.h BlackmagicRawAPI.idl
  "$WIDL" -u -o BlackmagicRawAPI_i.c BlackmagicRawAPI.idl
fi
# A MIDL-made header asks for a newer rpcndr.h than mingw-w64 declares; what it uses is there.
DEFS="-D__REQUIRED_RPCNDR_H_VERSION__=475 -D__REQUIRED_RPCSAL_H_VERSION__=100"
$CC -c -O2 $DEFS BlackmagicRawAPI_i.c -o iid.o
$CXX -O2 $DEFS -municode -static -I. "$HERE/brawref.cpp" BlackmagicRawAPIDispatch.cpp iid.o \
  -lole32 -loleaut32 -luuid -o "$OUT/braw-ref.exe"
x86_64-w64-mingw32-strip "$OUT/braw-ref.exe"
ls -l "$OUT/braw-ref.exe"
