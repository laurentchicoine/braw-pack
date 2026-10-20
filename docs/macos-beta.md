# macOS beta

## Downloads and compatibility

Download the ZIP matching your Mac from the
[macOS beta prerelease](https://github.com/laurentchicoine/braw-pack/releases/tag/v0.1.0-macos-beta.1).
It is a tagged snapshot of the `macos-beta` branch, separate from the existing Windows/Linux
prerelease. Later branch commits do not change an existing release's tag or downloads.
Native build artifacts are also available from successful
[macOS beta Actions runs](https://github.com/laurentchicoine/braw-pack/actions/workflows/macos-beta.yml)
on `macos-beta`; Actions artifact downloads require GitHub sign-in.

| Artifact | Hardware | OS target |
| --- | --- | --- |
| `braw-pack-macos-arm64` | Apple Silicon, M1 and later | macOS 12 Monterey or newer |
| `braw-pack-macos-amd64` | Intel 64-bit | macOS 12 Monterey or newer |

Extract the versioned release ZIP directly. If using an Actions artifact instead, extract its
wrapper ZIP first. The versioned ZIP preserves the app bundle and executable permissions.
Move `braw-pack.app` to `/Applications` or
`~/Applications`; it contains its own ffmpeg and needs no Homebrew, Blackmagic SDK or Rosetta.
For the CLI, keep the separate `braw-pack` and `ffmpeg` in the same folder.
The adjacent `.zip.sha256` file is checked with `shasum -a 256 -c <filename>.zip.sha256`
from the directory containing the versioned ZIP.

These are experimental, ad-hoc-signed builds, without a Developer ID certificate or Apple
notarization. Gatekeeper may warn that the developer cannot be verified or that the app
is damaged. After downloading from this repository and verifying the checksum, try opening it,
then use System Settings > Privacy & Security > Open Anyway if offered.
For a trusted beta download that macOS still refuses, remove quarantine from this app only:

```sh
xattr -dr com.apple.quarantine "/Applications/braw-pack.app"
open "/Applications/braw-pack.app"
```

For terminal use from the extracted package, macOS may also quarantine the separate tools:

```sh
xattr -d com.apple.quarantine ./braw-pack ./ffmpeg
./braw-pack pack -h
```

The CLI command can report that no quarantine attribute exists if those files are already clear.
Do not disable Gatekeeper globally. Production distribution would need Developer ID signing
and notarization; no Apple signing credentials are used by this workflow.

## Behavior on Mac

- Encoding uses x265 on the CPU at 12 bits. CLI `pack` and `estimate` default to x265 on Mac;
  the GUI's capability check disables the unavailable NVIDIA choice. There is no Metal or
  VideoToolbox encoder in this beta. Existing NVIDIA-made archives, including tiled/two-layer
  archives, decode on the CPU.
- Native 17K exceeds the HEVC parser's single-stream width limit. CPU encoding now stores it
  as 12-bit tiles, one tile at a time to bound memory; normal players show only the first tile.
  Rebuilding stitches every tile back together. 12K and smaller CPU archives keep their existing layout.
- Smaller-BRAW compression works independently of ffmpeg. Viewing LUTs remain Windows-only.
- The app is self-contained after moving it. It resolves ffmpeg beside its executable inside
  `Contents/MacOS`; the current working directory and terminal PATH are irrelevant.
- The app does not create `braw-pack-data` inside the bundle. WebKit manages its own website data
  under the user's Library. Standard macOS privacy prompts may apply when accessing protected folders.
- macOS 12 is the build target, based on [Go 1.25's minimum](https://go.dev/doc/go1.25).
  The hosted tests run on macOS 15, so Monterey and specific older Intel hardware remain unverified.
  High-resolution footage needs substantial RAM; the existing 12K/17K memory notes apply here too.

## Automated validation

The workflow uses native `macos-15` Apple Silicon and `macos-15-intel` runners, with separate
downloads. Each job builds checksummed FFmpeg 7.1.1 and static 12-bit x265 4.1 from source,
then runs all Go packages with the production desktop tags, race detection and `go vet`.
The exact source tarballs are uploaded beside the binary artifacts for GPL compliance.

Packaging validates the plist, native architecture, ad-hoc signature and deployment target of
every executable. `otool` rejects dependencies outside Apple's system libraries/frameworks.
A three-frame 12-bit pattern is encoded losslessly and decoded through each shipped ffmpeg;
all sample bytes must match. Core integration tests also exercise streaming CPU archive decode,
tiled and two-layer archive decode, and malformed input handling.

The ZIP is extracted, checked again, and the app moved to a separate directory containing spaces.
The relocated GUI is launched from `/` with Homebrew excluded from PATH; it must remain running
for ten seconds and keep its signature valid. This detects startup crashes, not successful
rendering or an interactive archive workflow. Test and build logs are retained as workflow artifacts.
Real camera footage is not uploaded; tests requiring footage skip when their fixture variables are absent.

Local Windows checks include the full Go suite and static analysis, plus cross-compilation
of the CLI and core test binaries for both Darwin architectures. `TestCPUCameraSamples`
can sample local footage via `BRAW_CPU_TEST_CLIP`, encode it through the same x265 path,
verify every sampled frame and rebuild canonical BRAW frames without uploading it.
See the completed validation results at the end of this document.

## Beta tester checklist

1. Report Mac model/chip, memory, macOS version and the artifact/run used. Confirm the matching
   architecture launches after moving the app to Applications, including any Gatekeeper steps needed.
2. Confirm the interface appears, buttons work, text can be copied/pasted, and file/folder dialogs
   and drag-and-drop accept footage. Check output selection and remembered output folders after restart.
3. With a short original clip, make a default CPU archive, keeping every-frame verification enabled.
   Note camera, resolution, frame count, duration, source/archive sizes, elapsed time and memory pressure.
4. Rebuild into a separate folder. Open the rebuilt clip in Resolve or Blackmagic RAW Player;
   check duration, first/middle/last frames, sound and sync, timecode, ISO and white balance controls.
   Keep the original. For a terminal check, run:

   ```sh
   ./braw-pack verify "/path/original.braw" "/path/archive/original.braw.mkv"
   ```

5. Test Estimate and Smaller BRAW, cancellation, an existing destination, paths with spaces or
   accented characters, and a read-only destination. Confirm originals remain unchanged.
6. If available, rebuild an archive previously made on Windows/Linux, including a tiled/two-layer one.
   Start with small footage before attempting 12K/17K on a memory-constrained Mac.

For a launch failure, include the macOS message and any crash report from Console. A terminal launch
can capture additional output:

```sh
"/Applications/braw-pack.app/Contents/MacOS/braw-pack-gui" > ~/Desktop/braw-pack-startup.log 2>&1
```

## Completed validation results

Checked locally on Windows and Ubuntu 24.04 WSL2 on 20 October 2026:

- Final Windows Go suite and vet passed, including the 17K extension and 12-bit tile tests;
  a Windows GUI regression build also passed. Forty top-level tests passed and 24 fixture-dependent
  tests skipped in the general suite; sampled camera checks were run separately as listed below.
- Full Linux desktop/production suite passed with race detection.
- Darwin arm64 and amd64 CLI and core test binaries cross-compiled successfully.
- The lossless CPU sample test passes for both a single stream and 12-bit tiles.
- Sampled original camera footage passed CPU encoding, picture verification and canonical BRAW
  frame reconstruction. Each test uses first/middle/last frames and the `fast` speed preset:

| Recording | Frames sampled | Coded planes | Quality | Mean plane PSNR | Detail kept |
| --- | --- | --- | --- | --- | --- |
| URSA Cine scaled 4K | 0, 405, 810 | 4112 x 2688 | 9 | 60.449 dB | 92.6% |
| URSA Cine scaled 8K | 0, 323, 646 | 8224 x 3424 | 4 | 63.807 dB | 87.2% |
| URSA Cine native 12K | 0, 48, 97 | 12336 x 8064 | 4 | 63.710 dB | 90.5% |
| URSA Cine native 17K | 0, 165, 330 | 17568 x 8064, six CPU tiles | 7 | 63.156 dB | 92.6% |

These are sensor-plane measurements on Windows, not independent Blackmagic SDK image comparisons
or complete Mac camera workflows. The 17K test first reproduced FFmpeg's 16888-pixel width rejection;
the CPU tiling change fixes that rejection and passes verification/reconstruction on all three samples.

The 17K test was also repeated against bundled FFmpeg 7.1.1: 63.153 dB, 92.6% detail,
all three samples verified and canonically rebuilt. The earlier table used the newer locally
installed FFmpeg; its measurements are retained above with that scope.

Both native jobs passed in [Actions run 39469531123](https://github.com/laurentchicoine/braw-pack/actions/runs/39469531123),
building code commit `f9bdeaa` with Go 1.25.14 on macOS 15:

- arm64 and amd64: 40 top-level tests passed, 24 footage-dependent tests skipped; race tests and vet passed.
- Both complete packages passed architecture, macOS 12 deployment target, system-only dylib dependency,
  plist and code-signature checks. Each bundled ffmpeg passed byte-exact 12-bit encode/decode.
- Both extracted apps were relocated into a separate directory containing spaces, launched from `/`
  without Homebrew on PATH, and stayed running for ten seconds. Neither modified its signed bundle.
- Both downloaded ZIPs were checked again on Windows for SHA-256, ZIP integrity, native Mach-O CPU type,
  deployment target, executable permission bits, plist and required licence/notices files.
- x265's build-CPU targeting defaults to OFF; ARM uses runtime feature detection. These are portable
  architecture builds, not binaries specialized to the hosted runner's CPU.

The matching FFmpeg/x265 source archives and validation logs are available in the same Actions run.
Local copies of the downloads, logs and sources are under `dist/macos-beta-downloads/`;
the ready-to-send ZIPs are also directly in `dist/`.
Interactive Mac workflows, actual Monterey execution and independent Mac SDK/Resolve image checks
remain for the beta tester.
