# braw-pack

Store Blackmagic RAW clips as compact H.265 archives that can be rebuilt into working `.braw`
files, keeping ISO and white-balance control, highlight recovery, gyro data and sound. It can also
re-save a clip as a smaller `.braw`, as if the camera had recorded it at a stronger compression
ratio.

**Status: early, experimental.** Tested only on Blackmagic Pocket Cinema Camera 4K clips recorded
at Q5 and Q3. The archive is lossy: a rebuilt clip is close to the original, not identical. Keep
your originals until you have checked rebuilt clips in your own workflow.

## Running it

braw-pack is portable: there is nothing to install, and clips and archives are written only to
the folders you choose. The app keeps one folder of its own, `braw-pack-data`, beside the programs: it holds the
built-in web view's data on Windows, including the folders you saved to last. Delete it whenever
you like. If the app sits somewhere it cannot write, it uses a temporary folder for that run and
remembers nothing afterwards. (On Linux the web view keeps its data under your home folder.)

1. Download the archive for your system from the Releases page and unpack it anywhere.
2. Keep the files together. The folder holds the app (`braw-pack-gui`), the command-line tool
   (`braw-pack`) and its own small `ffmpeg`, which both programs look for beside themselves.
3. Start `braw-pack-gui`, or use `braw-pack` from a terminal.

| | Windows | Linux |
|---|---|---|
| Download | `braw-pack-<version>-windows-amd64.zip` | `braw-pack-<version>-linux-amd64.tar.gz` |
| System | 64-bit Windows 10 or 11 | 64-bit, x86 |
| App needs | Microsoft Edge WebView2 runtime (part of Windows 11 and of up-to-date Windows 10) | GTK 3 and WebKitGTK 4.1 (`libgtk-3-0`, `libwebkit2gtk-4.1-0` on Debian/Ubuntu) |
| Command-line tool needs | nothing else | nothing else |

### Limitations

- **Fast (GPU) compression needs an NVIDIA card** with a current driver. It stores the picture at
  10 bits. Without one, braw-pack uses the CPU encoder (x265, 12 bits), which is much slower. The
  app checks at startup and disables the GPU choice when it cannot be used.
- **Viewing LUTs need Windows and Blackmagic RAW installed** (the free Blackmagic RAW Player
  from Blackmagic Design). braw-pack does not contain Blackmagic's decoder; to fit a LUT it asks
  the installed one, through the small `braw-ref` helper in the download, what the clip should
  look like. DaVinci Resolve alone is not enough. Without it the option is disabled and
  everything else works.
- **No macOS build yet.**
- **Linux builds currently need a recent distribution** (system C library 2.38 or newer, for
  example Ubuntu 24.04), and GPU use on Linux has only been tried under WSL.
- **Cameras:** only Pocket Cinema Camera 4K clips have been tested. A clip the tool does not fully
  understand is refused before anything is written.
- **Another ffmpeg:** if no `ffmpeg` sits beside the programs, the one on the `PATH` is used. It
  must include `libx265`, and `hevc_nvenc` for GPU use. `-ffmpeg path` selects one explicitly.

### The app

- **Compress:** add clips or a whole folder (buttons, or drop them on the window), choose the
  settings, press Compress. The app asks
  where to save every time, starting from the folder used last. *Estimate* tries
  several quality levels on short stretches of the selected clip and shows the archive size, the
  share of fine detail kept and the size of the clip you would get back.
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

```
braw-pack pack     [-o dir] [-quality N] [-speed fast|balanced|slow] [-encoder nvenc|x265] [-intra] [-spot-check] [-lut] <clip.braw | folder>
braw-pack unpack   [-o dir] [-quant auto|source|N] <clip.braw.mkv | folder>
braw-pack verify   <clip.braw> <clip.braw.mkv>
braw-pack estimate [-intra] [-qualities 6,8,10] <clip.braw>
braw-pack shrink   -o dir [-ratio N] <clip.braw | folder>
```

- `-quality` is a constant quantiser level (default 10). Lower is better and larger. On clean
  footage it behaves as expected. On noisy footage (night, high ISO) the default mode stops
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
- `-encoder nvenc` uses an NVIDIA GPU (10-bit). `-encoder x265` uses the CPU (12-bit).
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
`mingw-w64` for the Windows target. Only the `windows` and `linux` targets have been built so far.

```
WORK=~/braw-pack-ffmpeg build/ffmpeg/build.sh linux
WORK=~/braw-pack-ffmpeg build/ffmpeg/build.sh windows
```

### The LUT helper

`build/brawref/build.sh` builds `braw-ref.exe` with mingw-w64 (in WSL on Windows). It needs the
Blackmagic RAW SDK, which installs with Blackmagic RAW and is not part of this repository; set
`BRAW_SDK` if it is not in the default folder. The interface header is generated from the SDK's
`BlackmagicRawAPI.idl` with `widl` (package `mingw-w64-tools`), or taken from `BRAW_GENERATED`
when a MIDL-made one is at hand. The helper links nothing of Blackmagic's: it loads the decoder
library from the user's installation at run time. `core.LUTSupport` decides whether the option
is offered; a build without the helper simply has it disabled. There is no Linux or macOS
helper yet.

### Packaging

With the ffmpeg built for the target, run on the platform being packaged (Git Bash on Windows):

```
build/package.sh windows    # dist/braw-pack-<version>-windows-amd64.zip
build/package.sh linux      # dist/braw-pack-<version>-linux-amd64.tar.gz
```

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
  `omitempty` so existing archives keep opening.

## Notes

- The bitstream handling follows the public FFmpeg Blackmagic RAW decoder patches
  (Paul B Mahol 2019, Lynne 2025), which are LGPL-2.1-or-later. This project is not affiliated
  with or endorsed by Blackmagic Design. "Blackmagic RAW" is their trademark.
- The bundled ffmpeg includes x265 and is licensed under the GPL (version 2 or later). It is a
  separate program; `ffmpeg-BUILD-INFO.txt` in the download lists its sources and build options.
- Licence of braw-pack itself: not chosen yet (must be compatible with LGPL-2.1-or-later).
