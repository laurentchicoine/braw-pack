#!/usr/bin/env bash
# Writes THIRD_PARTY_NOTICES.txt: the licences of the Go modules and the Go runtime that are
# compiled into the braw-pack programs. Run it after changing go.mod.
#
#   build/notices.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
OUT="$ROOT/THIRD_PARTY_NOTICES.txt"
CACHE="$(go env GOMODCACHE)"

modules() {
  for os in windows linux; do
    GOOS=$os go list -deps -tags desktop,production,webkit2_41 \
      -f '{{if not .Standard}}{{with .Module}}{{.Path}}@{{.Version}}{{end}}{{end}}' ./cmd/braw-pack ./cmd/braw-pack-gui
  done | grep -v '^github.com/laurentchicoine/' | grep . | sort -u
}

{
  echo "Third-party software in the braw-pack programs"
  echo
  echo "braw-pack and braw-pack-gui are compiled from Go and include the Go runtime and the"
  echo "modules listed below. Their licences follow. ffmpeg, shipped beside them, is a separate"
  echo "program: see ffmpeg-BUILD-INFO.txt and ffmpeg-COPYING.txt."
  echo
  echo "================================================================================"
  echo "Go (runtime and standard library) $(go env GOVERSION)"
  echo "================================================================================"
  cat "$(go env GOROOT)/LICENSE"
  modules | while read -r m; do
    path="${m%@*}" ver="${m#*@}"
    # The module cache writes upper-case letters as !lower-case.
    dir="$CACHE/$(echo "$path" | sed 's/[A-Z]/!\L&/g')@$ver"
    echo
    echo "================================================================================"
    echo "$path $ver"
    echo "================================================================================"
    found=0
    for f in "$dir"/LICENSE* "$dir"/LICENCE* "$dir"/COPYING* "$dir"/UNLICENSE* "$dir"/NOTICE*; do
      [ -f "$f" ] || continue
      cat "$f"; echo; found=1
    done
    [ "$found" = 1 ] || { echo "no licence file found for $m" >&2; exit 1; }
  done
} | tr -d '\r' > "$OUT"
echo "wrote $OUT"
