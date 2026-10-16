#!/usr/bin/env bash
# Builds the small ffmpeg that ships beside braw-pack. It contains only what braw-pack uses:
# HEVC encode (NVIDIA hardware and x265 at 12 bits), HEVC decode (software and NVIDIA hardware),
# Matroska in/out, QuickTime in (to read a clip's sound), raw video and PCM.
#
#   build.sh <windows|linux|macos> [output-dir]
#
# windows is cross-compiled from Linux with mingw-w64. linux and macos build natively.
# Needs: a C/C++ toolchain, make, cmake, nasm, pkg-config, curl, tar (and mingw-w64 for windows).
# Set WORK to choose where sources are unpacked and built (default: ./work beside this script).
#
# The result includes x265 and is therefore licensed under the GPL (version 2 or later).
# BUILD-INFO.txt, written next to the binary, records the exact sources and options.
set -euo pipefail

TARGET="${1:?usage: build.sh <windows|linux|macos> [output-dir]}"
HERE="$(cd "$(dirname "$0")" && pwd)"
OUT="${2:-$HERE/dist/$TARGET}"
WORK="${WORK:-$HERE/work}"

FFMPEG_VERSION=7.1.1
FFMPEG_SHA256=733984395e0dbbe5c046abda2dc49a5544e7e0e1e2366bba849222ae9e3a03b1
X265_VERSION=4.1
X265_SHA256=a31699c6a89806b74b0151e5e6a7df65de4b49050482fe5ebf8a4379d7af8f29
NVHEADERS_VERSION=13.0.19.0
NVHEADERS_SHA256=13da39edb3a40ed9713ae390ca89faa2f1202c9dda869ef306a8d4383e242bee

JOBS="$( (nproc || sysctl -n hw.ncpu) 2>/dev/null || echo 4)"
SRC="$WORK/src"
BUILD="$WORK/$TARGET"
PREFIX="$BUILD/prefix"
mkdir -p "$SRC" "$BUILD" "$PREFIX" "$OUT"

sha256() { if command -v sha256sum >/dev/null; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }

# fetch <url> <file> <sha256>: download once and refuse anything that is not the pinned file.
fetch() {
  local url="$1" file="$SRC/$2" want="$3"
  [ -f "$file" ] || curl -fL --retry 3 -o "$file.part" "$url"
  [ -f "$file" ] || mv "$file.part" "$file"
  local got; got="$(sha256 "$file")"
  if [ "$got" != "$want" ]; then
    echo "checksum mismatch for $2: got $got, expected $want" >&2
    exit 1
  fi
}

unpack() { rm -rf "$BUILD/$2"; mkdir -p "$BUILD/$2"; tar -xf "$SRC/$1" -C "$BUILD/$2" --strip-components=1; }

fetch "https://ffmpeg.org/releases/ffmpeg-$FFMPEG_VERSION.tar.xz" "ffmpeg-$FFMPEG_VERSION.tar.xz" "$FFMPEG_SHA256"
fetch "https://bitbucket.org/multicoreware/x265_git/downloads/x265_$X265_VERSION.tar.gz" "x265_$X265_VERSION.tar.gz" "$X265_SHA256"

CROSS=""
EXE=""
LDFLAGS_EXTRA=""
STDCXX="-lstdc++"   # how the C++ runtime x265 needs is linked
NVIDIA=1
CMAKE_CROSS=()
FF_CROSS=()
case "$TARGET" in
  windows)
    CROSS=x86_64-w64-mingw32-
    EXE=.exe
    CMAKE_CROSS=(-DCMAKE_SYSTEM_NAME=Windows -DCMAKE_SYSTEM_PROCESSOR=x86_64
      -DCMAKE_C_COMPILER=${CROSS}gcc -DCMAKE_CXX_COMPILER=${CROSS}g++ -DCMAKE_RC_COMPILER=${CROSS}windres
      -DCMAKE_FIND_ROOT_PATH_MODE_PROGRAM=NEVER)
    FF_CROSS=(--arch=x86_64 --target-os=mingw32 --cross-prefix=$CROSS)
    LDFLAGS_EXTRA="-static -static-libgcc -static-libstdc++"   # no compiler DLLs beside the exe
    ;;
  linux)
    # The C++ runtime is linked in so the binary only needs the system's C library. It cannot be
    # fully static: the NVIDIA encoder is loaded from the driver at run time.
    LDFLAGS_EXTRA="-static-libgcc"
    STDCXX="-Wl,-Bstatic -lstdc++ -Wl,-Bdynamic"
    ;;
  macos)
    NVIDIA=0
    STDCXX="-lc++"
    ;;
  *) echo "unknown target $TARGET" >&2; exit 2 ;;
esac

# ---- NVIDIA codec headers (MIT licence; only headers, the driver provides the rest) ----
if [ "$NVIDIA" = 1 ]; then
  fetch "https://github.com/FFmpeg/nv-codec-headers/releases/download/n$NVHEADERS_VERSION/nv-codec-headers-$NVHEADERS_VERSION.tar.gz" \
    "nv-codec-headers-$NVHEADERS_VERSION.tar.gz" "$NVHEADERS_SHA256"
  unpack "nv-codec-headers-$NVHEADERS_VERSION.tar.gz" nv-codec-headers
  make -C "$BUILD/nv-codec-headers" install PREFIX="$PREFIX" >/dev/null
fi

# ---- x265, 12-bit only (the depth braw-pack's CPU path stores) ----
unpack "x265_$X265_VERSION.tar.gz" x265
mkdir -p "$BUILD/x265/out"
( cd "$BUILD/x265/out"
  cmake ../source -G "Unix Makefiles" "${CMAKE_CROSS[@]}" \
    -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$PREFIX" \
    -DHIGH_BIT_DEPTH=ON -DMAIN12=ON -DENABLE_SHARED=OFF -DENABLE_CLI=OFF \
    -DCMAKE_POLICY_VERSION_MINIMUM=3.5 >/dev/null
  make -j"$JOBS" >/dev/null
  make install >/dev/null )
# x265 asks for the shared compiler and C++ runtimes; drop those requests (STDCXX below says how the
# C++ runtime is linked) so the result has no extra library to carry around.
sed -i.bak 's/-lgcc_s//g; s/-lstdc++//g' "$PREFIX/lib/pkgconfig/x265.pc"

# ---- ffmpeg ----
unpack "ffmpeg-$FFMPEG_VERSION.tar.xz" ffmpeg
CONFIGURE=(
  --prefix="$PREFIX" --pkg-config=pkg-config --pkg-config-flags=--static
  --disable-everything --disable-autodetect --disable-doc --disable-debug
  --disable-ffplay --disable-ffprobe --disable-network
  --disable-avdevice --disable-postproc
  --enable-static --disable-shared --enable-gpl --enable-small
  --enable-libx265
  --enable-protocol=file,pipe,fd
  --enable-demuxer=matroska,mov,rawvideo,hevc
  --enable-muxer=matroska,rawvideo,md5,null,hevc
  --enable-decoder=hevc,rawvideo,pcm_s16le,pcm_s24le,pcm_s24be,pcm_s32le
  --enable-encoder=libx265,rawvideo
  --enable-parser=hevc
  --enable-bsf=hevc_metadata,hevc_mp4toannexb,extract_extradata
  --enable-filter=scale,format,null,anull,aformat,aresample
)
if [ "$NVIDIA" = 1 ]; then
  CONFIGURE+=(--enable-ffnvcodec --enable-cuda --enable-nvenc --enable-nvdec
    --enable-encoder=hevc_nvenc --enable-hwaccel=hevc_nvdec)
fi
( cd "$BUILD/ffmpeg"
  PKG_CONFIG_LIBDIR="$PREFIX/lib/pkgconfig" PKG_CONFIG_PATH="$PREFIX/lib/pkgconfig" \
    ./configure "${CONFIGURE[@]}" "${FF_CROSS[@]}" \
      --extra-cflags="-I$PREFIX/include" --extra-ldflags="-L$PREFIX/lib $LDFLAGS_EXTRA" --extra-libs="$STDCXX -lm"
  make -j"$JOBS" >/dev/null
  "${CROSS}strip" "ffmpeg$EXE" )

cp "$BUILD/ffmpeg/ffmpeg$EXE" "$OUT/ffmpeg$EXE"
{
  echo "ffmpeg for braw-pack ($TARGET)"
  echo
  echo "This program is FFmpeg, built with x265, and is licensed under the GNU General Public"
  echo "License version 2 or later. It is a separate program that braw-pack runs; it is not part"
  echo "of braw-pack itself. Sources:"
  echo "  ffmpeg $FFMPEG_VERSION      https://ffmpeg.org/releases/ffmpeg-$FFMPEG_VERSION.tar.xz"
  echo "  x265 $X265_VERSION           https://bitbucket.org/multicoreware/x265_git/downloads/x265_$X265_VERSION.tar.gz"
  [ "$NVIDIA" = 1 ] && echo "  nv-codec-headers $NVHEADERS_VERSION (MIT)  https://github.com/FFmpeg/nv-codec-headers"
  echo
  echo "Built by build/ffmpeg/build.sh in the braw-pack repository with:"
  echo "  ./configure ${CONFIGURE[*]} ${FF_CROSS[*]}" | sed "s#$PREFIX#PREFIX#g"
} > "$OUT/BUILD-INFO.txt"
ls -l "$OUT"
