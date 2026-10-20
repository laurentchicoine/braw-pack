# braw-pack

Store Blackmagic RAW clips as compact H.265 archives that can be rebuilt into working `.braw`
files, keeping ISO and white-balance control, highlight recovery, gyro data and sound. It can also
re-save a clip as a smaller `.braw`, as if the camera had recorded it at a stronger compression
ratio.

**Status: early, experimental.** Tested on Blackmagic Pocket Cinema Camera 4K clips recorded
at Q5 and Q3, and on URSA Cine 12K LF recordings in 12K (12:1), scaled 8K (5:1),
and scaled 4K (4:1), plus a native URSA Cine 17K 65 Q3 sample (validation scope below). The archive is lossy: a rebuilt clip is
close to the original, not identical. Keep your originals until you have checked rebuilt clips in
your own workflow.

## Running it

braw-pack is portable: there is nothing to install, and clips and archives are written only to
the folders you choose. The app keeps one folder of its own, `braw-pack-data`, beside the programs: it holds the
built-in web view's data on Windows, including the folders you saved to last. Delete it whenever
you like. If the app sits somewhere it cannot write, it uses a temporary folder for that run and
remembers nothing afterwards. (On Linux and macOS the web view keeps its data under your home folder.)

1. Download the archive for your system from the
   [Releases page](https://github.com/laurentchicoine/braw-pack/releases) and unpack it anywhere.
2. Keep the files together. The folder holds the app (`braw-pack-gui`), the command-line tool
   (`braw-pack`) and its own small `ffmpeg`, which both programs look for beside themselves.
3. Start `braw-pack-gui`, or use `braw-pack` from a terminal.

The latest local Windows executables are in the main project folder:
`braw-pack-gui.exe` and `braw-pack.exe`. The Linux executables are `braw-pack`
and `braw-pack-gui` in that same folder; keep the matching `ffmpeg` beside them.
Ready-to-unpack builds are in `dist/` and include this README
and the validation notes in `docs/`.

**macOS beta:** download `braw-pack-macos-arm64` for M1/M2/M3/M4 and later Apple Silicon,
or the Intel ZIP from the
[macOS beta prerelease](https://github.com/laurentchicoine/braw-pack/releases/tag/v0.1.0-macos-beta.1).
The prerelease is a tagged snapshot of the `macos-beta` branch. Native build artifacts also remain
available from [macOS beta Actions runs](https://github.com/laurentchicoine/braw-pack/actions/workflows/macos-beta.yml).
For a release download, extract the versioned ZIP directly; for an Actions artifact, first extract
its wrapper ZIP. Both target **macOS 12 Monterey or newer**.
Move `braw-pack.app` to Applications and open it. The app contains its own ffmpeg;
the separate `braw-pack` and `ffmpeg` files are for terminal use and should stay together.
These builds are ad-hoc signed, without Apple notarization, so Gatekeeper may block first launch.
See [Mac beta setup and tester checklist](docs/macos-beta.md) for the exact opening steps and test scope.

**Users do not need to install the Blackmagic RAW SDK.** Archive, rebuild,
verification, estimates and smaller-BRAW compression work without Blackmagic's
software. Only the optional Windows viewing-LUT feature needs the free
Blackmagic RAW Player 6.0 runtime, as described below.

| | Windows | Linux |
|---|---|---|
| Download | `braw-pack-<version>-windows-amd64.zip` | `braw-pack-<version>-linux-amd64.tar.gz` |
| System | 64-bit Windows 10 or 11 | 64-bit, x86 |
| App needs | Microsoft Edge WebView2 runtime (part of Windows 11 and of up-to-date Windows 10) | GTK 3 and WebKitGTK 4.1 (`libgtk-3-0`, `libwebkit2gtk-4.1-0` on Debian/Ubuntu) |
| Command-line tool needs | nothing else | nothing else |

### Limitations

- **Fast (GPU) compression needs an NVIDIA card** with a current driver. It stores the picture at
  10 bits. Without one, braw-pack uses the CPU encoder (x265, 12 bits), which is much slower. The
  app checks at startup and disables the GPU choice when it cannot be used. Pictures wider or
  taller than NVIDIA's encoders take (8192 pixels) are stored as tiles, one video track per tile
  and layer, and stitched back on unpack. The grid follows the recording dimensions; larger
  grids are encoded in bounded groups to limit GPU memory. When the GPU encoder fails on a particular clip,
  that clip is encoded on the CPU instead and the result says so.
  Native 17K also exceeds HEVC's single-stream width limit on the CPU; x265 stores it as
  12-bit tiles, encoded one at a time. Rebuilding joins the complete picture.
- **Viewing LUTs need Windows and Blackmagic RAW 6.0 installed** (the free Blackmagic RAW Player
  from Blackmagic Design). braw-pack does not contain Blackmagic's decoder; to fit a LUT it asks
  the installed one, through the small `braw-ref` helper in the download, what the clip should
  look like. DaVinci Resolve alone is not enough. Without it the option is disabled and
  everything else works.
- **macOS beta uses CPU encoding (x265, 12 bits).** Apple Silicon and Intel downloads are
  separate; older Macs must run macOS 12 or newer. Apple VideoToolbox/Metal acceleration and
  viewing LUTs are not supported. Automated native builds and startup checks run in GitHub Actions;
  a complete interactive workflow and the minimum OS still need a tester. See [docs/macos-beta.md](docs/macos-beta.md).
- **Linux builds currently need a recent distribution** (system C library 2.38 or newer, for
  example Ubuntu 24.04), and GPU use on Linux has only been tried under WSL.
  The current Linux CLI passed a full 12K GPU archive/rebuild, scaled 4K/8K frame
  checks, sampled 17K GPU archive/rebuild and full 17K smaller-BRAW compression
  in Ubuntu 24.04 WSL2. The GUI passed build, tests and a startup smoke test;
  a complete interactive GUI workflow has not been tested. See
  [docs/linux-wsl-validation.md](docs/linux-wsl-validation.md).
- **Cameras:** Pocket Cinema Camera 4K clips, and URSA Cine 12K LF recordings in 12K, 8K and 4K, have been tested. A
  clip with an unknown coding layout is refused before output is completed.
- **URSA Cine 12K (Blackmagic RAW version 5)** clips work with every command (`pack`, `unpack`
  including `-quant`, `verify`, `estimate`, `shrink`, the viewing LUT), worked out without
  Blackmagic's documentation; see [docs/braw-v5.md](docs/braw-v5.md). Archives made by the first
  builds that read these clips (`v5_steps` 1) still rebuild exactly as before, but only at the
  clip's own quantisation. A 12K picture is too large for NVIDIA's encoders as one stream, so
  with the GPU encoder (the default) it is stored as four 6168x4032 tiles, and in two layers (a
  10-bit picture and a detail layer coded in 12-bit units, see `-layers`): one GPU layer keeps
  only 73.5% of this camera's fine detail at quality 10 and at most about 76% at any level. On
  the test clip (4 seconds, RTX 3060 Ti, 6-core i7-5820K), `pack` took 4 minutes and kept 91.1%
  of the detail in 31.5% of the clip; one layer (`-layers 1`) took 2.5 minutes for 73.5% in
  4.4%; x265 on the CPU took 14 minutes for 91.3% in 28% at quality 4, the level `-quality auto`
  picks for these clips with x265, and 8 to 24 minutes for 79% at quality 10. `-speed slow`
  changes little here with the GPU and makes larger archives with x265. 12K needs plenty of
  memory: 15 to 16 GB for `pack`, `verify` and `estimate` with the GPU encoder (about 13 GB with
  one layer), plus up to 3 GB that Windows moves out of a full 8 GB graphics card; 13 GB for
  `unpack` (10 GB for one layer); 15 to 20 GB for `pack` with x265. The app has not been tried
  with a 12K clip.
- **URSA Cine scaled 4K/8K:** the 4096x2680 and 8192x3408 RGBW recording modes are supported by archive,
  rebuild, estimate and smaller-BRAW compression. Its internal geometry, smaller transforms
  and quantization differ from 12K; the output resolution and compression ratio are read
  separately from the codec's internal dimensions. See [docs/braw-v5-scaled.md](docs/braw-v5-scaled.md)
  and [docs/braw-v5-8k.md](docs/braw-v5-8k.md) for the format and validation results.
  Supported transform layouts are detected from the headers, with crop and tile geometry
  read from each file. Unknown coding layouts are refused.
- **URSA Cine 17K 65:** the native 17520x8040 sample uses the same transform layout as native
  12K. All 331 source frames pass byte-exact entropy rewriting, full-clip smaller-BRAW
  compression passes, and an eight-frame GPU archive/rebuild test passes with six tiles and
  two layers on an 8 GB card. Source, smaller-BRAW and archive-rebuilt samples also pass
  independent image checks through SDK 6.0. Full-clip archive/rebuild and GUI use with this
  footage have not been tested. See [docs/braw-v5-17k.md](docs/braw-v5-17k.md).
- **Another ffmpeg:** if no `ffmpeg` sits beside the programs, the one on the `PATH` is used. It
  must include `libx265`, and `hevc_nvenc` for GPU use. `-ffmpeg path` selects one explicitly.

### The app

- **Compress:** add clips or a whole folder (buttons, or drop them on the window), choose the
  settings, press Compress. A large folder takes a moment to read; the list shows how far it has
  got, and Compress waits until it is done. The app asks
  where to save every time, starting from the folder used last. *Estimate* tries
  several quality levels on short stretches of the selected clip and shows the archive size, the
  share of fine detail kept and the size of the clip you would get back. *Quality level, Choose
  for each clip* (the default) picks the level from each clip as `-quality auto` does; the
  estimate marks that level. *Layers* (GPU only) is `-layers`: *Automatic* (the default) uses two
  for all supported RGBW recordings, including scaled footage, and one for other cameras, *One* and *Two* force it; the result says how
  many were used, and the estimate follows the setting.
- **Compress, Smaller BRAW:** instead of an archive, writes a new, smaller `.braw`
  under the same name in another folder, compressed about as hard as the ratio you pick. Only ratios
  stronger than the selected clip's own are offered. No GPU or ffmpeg is involved and it is fast.
- **Compress, Viewing LUT:** also writes `name.braw.cube` (about 7 MB, a standard 65-point
  3D LUT) beside each archive, which adds roughly 15 seconds per clip. Applied to the
  archive in an editor, it makes the picture look like the clip does in Blackmagic RAW with its
  as-shot settings (Blackmagic's own decode of the clip's ISO, white balance and gamma), so an
  archive can be viewed or rough-cut without decompressing it. It is an approximation fitted on
  sample frames of that one clip: it cannot recover highlights or change ISO and white balance
  afterwards, the picture is a little softer than a real decode, and colours the clip never
  shows may be off. Decompress when the real clip is needed.
- **Decompress:** add `.braw.mkv` archives and press Decompress.
- A clip that has been processed is left alone on the next run. *Reset selected* and *Reset all*,
  beside the Compress button, clear that status so it can be done again with other settings.

Sources are never modified or deleted. When a result already exists in the chosen folder, the
app asks whether to replace it or skip it; a replaced file is only swapped out once its new
version is finished and checked. The command-line tool always skips existing results.

### Command line

`braw-pack` (`braw-pack.exe` on Windows) is the command-line tool. The app, `braw-pack-gui`, takes
no command-line options. Run `braw-pack` alone for the list of commands and
`braw-pack <command> -h` for the options of one. Options go **before** the clip or folder. A
folder is searched recursively for `.braw` clips (or `.braw.mkv` archives for `unpack`).

```
braw-pack pack     [options] <clip.braw | folder>       make archives (name.braw.mkv)
braw-pack unpack   [options] <clip.braw.mkv | folder>   rebuild .braw clips from archives
braw-pack verify   [options] <clip.braw> <clip.braw.mkv>
braw-pack estimate [options] <clip.braw>                predict sizes for several quality levels
braw-pack shrink   -o dir [-ratio N] <clip.braw | folder>  re-save as smaller .braw clips
```

| Command | Option | Default | Meaning |
|---|---|---|---|
| `pack` | `-o dir` | beside each clip | folder the archives are written to |
| | `-quality auto\|N` | `auto` | constant quality level, 1 to 51; lower is better and larger. `auto` chooses it per clip |
| | `-speed fast\|balanced\|slow` | `balanced` | slower gives a smaller file |
| | `-encoder nvenc\|x265` | `nvenc` | NVIDIA GPU (10-bit) or CPU (12-bit) |
| | `-intra` | off | code every frame on its own ("Keep sensor noise" in the app) |
| | `-layers auto\|1\|2` | `auto` | with `nvenc`: `2` stores the picture as a 10-bit base and a detail layer, which keeps about as much of a finely coded clip's detail as x265; `-quality` is then the detail layer's level. `auto` chooses 2 for all supported RGBW recordings at every resolution, 1 for other cameras ("Layers" in the app) |
| | `-spot-check` | off | compare only sample frames with the source ("Check every frame" off in the app) |
| | `-lut` | off | also write a viewing LUT, `name.braw.cube` |
| | `-ffmpeg path` | see below | the ffmpeg to use |
| `unpack` | `-o dir` | beside each archive | folder the rebuilt clips are written to |
| | `-quant auto\|source\|N` | `auto` | how finely the rebuilt clip is quantised |
| | `-ffmpeg path` | | |
| `verify` | `-ffmpeg path` | | |
| `estimate` | `-qualities 6,8,10` | `4,6,8,10,12,14,16` and the `auto` level | quality levels to try |
| | `-speed`, `-encoder`, `-intra`, `-layers` | as for `pack` | estimate for archives made with these settings; with two layers the levels tried are 13, 16, 19, 22 and the `auto` one |
| | `-ffmpeg path` | | |
| `shrink` | `-o dir` | required | output folder; must not be the folder the clip is in |
| | `-ratio N` | none | ratio to reach, e.g. `12` for 12:1; without it the reachable ratios are listed |

`-ffmpeg` defaults to the `ffmpeg` beside the program, else the one on the `PATH`. Existing
results are skipped. The exit status is 0 on success, 1 when a file failed or the arguments are
wrong, and 2 when the command or an option is not recognised.

Examples:

```
braw-pack pack -o D:\Archive -quality 8 E:\Footage
braw-pack pack -encoder x265 -intra -lut clip.braw
braw-pack estimate -qualities 6,8,10 clip.braw
braw-pack unpack -o D:\Restored D:\Archive
braw-pack shrink -o D:\Smaller -ratio 12 E:\Footage
```

More about the options:

- `-quality` is a constant quantiser level. Lower is better and larger. The default, `auto`,
  chooses it for each clip from how finely the camera quantised the picture: 10 for clips coded
  about as coarsely as a Pocket Cinema Camera 4K at its constant-quality settings, or more
  coarsely, since those keep about as much detail at 10 as at any lower level. With x265 it is
  lower for clips coded more finely, down to 4 for URSA Cine 12K clips, so they do not lose the
  finest detail they hold. With NVENC it stays at 10: the GPU encoder keeps no more of such clips
  at any lower level, and x265 or `-layers 2` is the better choice for them. The level is printed with each
  result and by `estimate`;
  `-quality 10` gives the fixed level of earlier versions. [docs/quality.md](docs/quality.md) has
  the measurements. On clean footage it behaves as expected. On noisy footage (night, high ISO) the default mode stops
  getting closer to the source below some level and only grows the file; use `-intra` when the
  noise itself must be kept, and `estimate` to see what a clip does.
- `-intra` codes every frame on its own ("Keep sensor noise" in the app). Archives are several
  times larger, but the quality level then holds for every frame. Without it, frames are predicted
  from their neighbours and the encoder smooths fine sensor noise away, much like light noise
  reduction.
- `estimate` prints, per quality level, the expected archive size, the share of the original's
  fine picture detail the archive keeps, and the size of the clip `unpack` would produce. "Detail
  kept" compares 2x2-pixel averages, so random sensor noise mostly drops out of it and what it
  reports is loss of real structure. A lower level does not always keep more; read the table. Sizes are also given as ratios
  against the unprocessed sensor data, the same scale as the camera's 3:1 ... 18:1 settings.
- `-speed` trades conversion time against file size. In the default mode it can also change how
  close the result is to the source, because faster presets spend more bits on fine noise.
- `-encoder nvenc` uses an NVIDIA GPU (10-bit). `-encoder x265` uses the CPU (12-bit). If the GPU
  encoder fails on a clip, that clip is encoded with x265 instead, and the output says why. A
  picture wider or taller than 8192 pixels is split for the GPU into a grid of tiles (2x2 for
  URSA Cine 12K), each its own HEVC track in the archive; braw-pack stitches them back whenever it
  reads the archive, but a normal player shows only the first tile. Such archives need this
  version of braw-pack or later (older ones refuse them).
- `-layers` (with `nvenc`): `2` stores the picture in two layers, a base, the picture rounded to
  10 bits and coded coarsely, and a detail layer holding what the base misses, in 12-bit units.
  The GPU's encoder loses fine detail in predicted frames by about one 10-bit step whatever the
  level, which matters for clips coded as finely as the URSA Cine 12K's; in the detail layer
  that step is four times smaller. `-quality` is then the detail layer's level, and
  `-quality auto` chooses x265's level plus 12 (16 for 12K clips, which keeps about as much
  detail as x265 at 4, in an archive about 1.1 times as large). The default, `auto`, chooses two
  layers for clips of an RGBW sensor (Blackmagic RAW version 5: URSA Cine 12K), and one for other
  cameras, which already keep over 90% of their detail in one: a Pocket Cinema Camera 4K clip
  kept 98.9% of its detail in two layers against 98.8% in one, in a larger archive and twice the
  time. 12K, scaled 8K and scaled 4K RGBW recordings have been tried; the benefit depends on the clip.
  The
  choice and its reason are printed with each result. The base tracks come first, so a player
  shows the base. Two-layer archives need this version of braw-pack or later. x265 always stores
  one layer, also when it takes over from a failed GPU encoder. See
  [docs/braw-v5.md](docs/braw-v5.md).
- `unpack -quant` sets how finely the rebuilt clip is quantised. `pack` measures, on sample
  frames, the coarsest setting that loses nothing worth keeping compared with the archive itself,
  and stores it in the archive; `auto` uses it. `source` forces the original clip's own tables,
  which only makes a bigger file when the archive holds less detail than the source did.
- `pack` compares every frame of a new archive with its source before reporting success.
  `-spot-check` ("Check every frame" switched off in the app) compares only a few sample frames
  and counts the rest, which is faster; the restore data and the frame count are checked either
  way.
- `pack -lut` also writes the viewing LUT described above (`name.braw.cube`) beside each archive
  and reports how closely it matched Blackmagic's picture on sample frames kept out of the fit.
  A LUT that cannot be made does not fail the archive.
- `shrink -ratio 12 -o dir` writes each clip under its own name into `dir` (which must not be
  the folder the clip is in), as a normal Blackmagic RAW clip compressed to about
  12:1 (unprocessed sensor data against stored picture data, the camera's own scale). Without
  `-ratio` it lists, for each clip, the ratios it can reach with the size and detail kept at each.
  It coarsens the clip's existing quantisation, finest detail first; sound, timecode, motion data
  and camera settings are copied unchanged. The limit is around 25:1 to 30:1 depending on the
  clip: past that almost nothing but the average of each 8x8 block is left, so stronger targets
  are refused. For anything smaller, make an archive. The clip's own metadata still names the
  quality setting it was originally recorded at.

## How it works

A `.braw` frame is a set of DCT-coded planes that are still in sensor space (no white balance,
ISO or colour science applied). braw-pack undoes only the DCT, stores those planes as H.265 video,
and keeps everything else losslessly. Rebuilding runs the DCT again and writes a normal `.braw`.

An archive is one Matroska file, `name.braw.mkv`:

| Part | Content |
|---|---|
| Video track | sensor planes, H.265, at the captured frame rate |
| Audio track | the clip's original PCM sound |
| Attachment `braw-pack-restore.bin` | original index, audio/timecode/gyro/accelerometer data, per-frame camera metadata, the clip's `.sidecar` file if present, and the measured rebuild settings |

Played directly, the picture has a green cast and odd contrast because it is raw sensor data.

## Building from source

### Layout

| Path | What it is |
|---|---|
| `internal/core` | everything that touches clips and archives: bitstream, planes, pack, unpack, verify, estimate, shrink |
| `cmd/braw-pack` | command-line tool |
| `cmd/braw-pack-gui` | desktop app ([Wails v2](https://wails.io)): `app.go` is the API the interface calls, `frontend/` is plain HTML, CSS and JavaScript with no build step |
| `cmd/braw-lab` | measurement helpers used during development; not shipped |
| `build/ffmpeg/build.sh` | builds the small ffmpeg that ships with the tool |
| `build/brawref` | `braw-ref`, the helper that asks an installed Blackmagic RAW decoder for reference frames (viewing LUTs) |
| `build/package.sh` | assembles the portable download |

### Requirements

- Go 1.25 or newer.
- An `ffmpeg` with `libx265` (and `hevc_nvenc` for GPU work) on the `PATH` or beside the built
  programs, until you build the bundled one.
- App on Windows: nothing more (the WebView2 runtime at run time).
- App on Linux: a C compiler plus GTK 3 and WebKitGTK 4.1 headers
  (`sudo apt install build-essential libgtk-3-dev libwebkit2gtk-4.1-dev`).
- App on macOS: Xcode Command Line Tools (`xcode-select --install`). Packaging also uses
  Apple's `codesign`, `sips`, `iconutil`, `plutil`, `otool`, `lipo` and `ditto`.
  The Mac Actions workflow pins Go 1.25.x to retain its macOS 12 deployment floor.

### Build and test

```
go build -o braw-pack ./cmd/braw-pack
go test ./internal/core
```

The app is built with plain `go build`; the Wails command-line tool is not needed.

```
# Windows
go build -tags desktop,production -ldflags "-H windowsgui" -o braw-pack-gui.exe ./cmd/braw-pack-gui
# Linux
go build -tags desktop,production,webkit2_41 -o braw-pack-gui ./cmd/braw-pack-gui
# macOS (native architecture; packaging below produces the Finder-launchable app)
MACOSX_DEPLOYMENT_TARGET=12.0 go build -tags desktop,production -o braw-pack-gui ./cmd/braw-pack-gui
```

Some tests need real footage and are skipped without it:

```
BRAW_TEST_CLIP=/path/to/clip.braw go test ./internal/core
BRAW_TEST_DIR=/folder/with/clips/and/archives go test -tags desktop,production ./cmd/braw-pack-gui
```

`BRAW_TEST_CLIP` also enables the benchmarks (`go test -bench . ./internal/core`).

### Working on the interface

`cmd/braw-pack-gui/frontend/index.html` also runs in an ordinary browser, where a stand-in backend
with sample data replaces the real one. Serve the folder and open it:

```
python -m http.server 8765 --directory cmd/braw-pack-gui/frontend
```

The app icon is drawn by `cmd/braw-pack-gui/icon/make_icon.py`; the header of that file says how
to regenerate the Windows resource after changing it.

### The bundled ffmpeg

`build/ffmpeg/build.sh <windows|linux|macos> [output-dir]` downloads pinned, checksummed sources
(ffmpeg, x265, NVIDIA codec headers) and builds an ffmpeg of about 10 MB containing only what
braw-pack uses. It runs on Linux; the Windows binary is cross-compiled there with mingw-w64, so on
Windows use WSL. It needs `gcc`/`g++`, `make`, `cmake`, `nasm`, `pkg-config`, `curl`, and
`mingw-w64` for the Windows target. The macOS target builds natively on each architecture,
uses CMake 3.31.6 for x265 4.1's Apple policy compatibility, and disables NVIDIA support.
Its output is under `build/ffmpeg/dist/macos-arm64` or `macos-amd64`; the native architecture
is detected automatically, or can be checked explicitly with `ARCH=arm64` / `ARCH=amd64`.

```
WORK=~/braw-pack-ffmpeg build/ffmpeg/build.sh linux
WORK=~/braw-pack-ffmpeg build/ffmpeg/build.sh windows
```

### The LUT helper

`build/brawref/build.sh` builds `braw-ref.exe` with mingw-w64 (in WSL on Windows). It needs the
Blackmagic RAW 6.0 SDK, which installs with Blackmagic RAW and is not part of this repository; set
`BRAW_SDK` if it is not in the default folder. The interface header is generated from the SDK's
`BlackmagicRawAPI.idl` with `widl` (package `mingw-w64-tools`), or taken from `BRAW_GENERATED`
when a MIDL-made one is at hand. Besides its own code, the helper compiles in only the SDK's small
loader (`BlackmagicRawAPIDispatch.cpp`, under a permissive licence); Blackmagic's decoder itself is
loaded from the user's installation at run time. `core.LUTSupport` decides whether the option
is offered; a build without the helper simply has it disabled. There is no Linux or macOS
helper yet.

### Packaging

With the ffmpeg built for the target, run on the platform being packaged (Git Bash on Windows):

```
build/package.sh windows    # dist/braw-pack-<version>-windows-amd64.zip
build/package.sh linux      # dist/braw-pack-<version>-linux-amd64.tar.gz
bash build/package.sh macos arm64  # dist/braw-pack-<version>-macos-arm64.zip (on Apple Silicon)
bash build/package.sh macos amd64  # dist/braw-pack-<version>-macos-amd64.zip (on Intel)
```

The download carries `LICENSE`, `LICENSES/`, `THIRD_PARTY_NOTICES.txt` and ffmpeg's licence and
build notes. After changing `go.mod`, run `build/notices.sh` to regenerate
`THIRD_PARTY_NOTICES.txt`.

### Things to know before changing the core

- `Pack` refuses a clip whose frames do not survive an entropy decode and re-encode unchanged, and
  discards any archive that fails the comparison with its source. Keep both gates intact.
- `TestLosslessIdentity` checks that planes decoded from a frame encode back to the same bytes.
  Run it with a real clip after touching the transforms or the bit reader and writer.
- Samples are handed to and taken from ffmpeg at the encoder's own bit depth, so ffmpeg never
  converts values. With the NVIDIA encoder and decoder they are also exchanged in the GPU's own
  arrangement (P010), which holds the same values and saves ffmpeg a pass over every frame; see
  `pixLayout` in `planes.go`. Colour tags are written as stream metadata only; passing them as ordinary
  ffmpeg colour options makes it rescale the samples.
- The restore data has a format number (`FormatVersion`). New optional fields go in `Meta` with
  `omitempty` so existing archives keep opening. Unknown fields are ignored when an archive is
  read, so a change that older versions must not misread needs a new format number: tiled
  archives (`Meta.Tiles`, see `tiles.go`) are format 2 (`FormatTiled`) for that reason,
  two-layer archives (`Meta.Layers`, see `layers.go`), tiled or not, format 3 (`FormatLayered`),
  and everything else stays at 1.

## Licence

braw-pack is released under the [MIT licence](LICENSE), with these exceptions:

- `internal/core/codec.go`, `planes.go` and `tables.go` follow the public FFmpeg Blackmagic RAW
  decoder patches (Paul B Mahol 2019, Lynne 2025) and stay under the
  [LGPL 2.1 or later](LICENSES/LGPL-2.1.txt). You may change those files and relink the programs
  with your changes; the complete source is at <https://github.com/laurentchicoine/braw-pack>.
- `build/brawref/brawref.cpp` follows a Blackmagic RAW SDK sample and keeps that sample's
  permissive licence, reproduced in the file.
- The `ffmpeg` in the download is a separate program, licensed under the
  [GPL 2 or later](LICENSES/GPL-2.0.txt) because it includes x265. `ffmpeg-BUILD-INFO.txt` lists
  its exact sources and build options, and the release page carries those source archives.
- Go modules compiled into the programs (Wails and its dependencies, under MIT, BSD, Apache 2.0
  and similar licences) are listed with their licences in
  [THIRD_PARTY_NOTICES.txt](THIRD_PARTY_NOTICES.txt).

This project is not affiliated with or endorsed by Blackmagic Design. "Blackmagic RAW" is their
trademark.
