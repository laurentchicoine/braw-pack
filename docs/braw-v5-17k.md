# URSA Cine 17K 65 validation

Source: `A003_02171447_C055.braw`, 3,358,442,154 bytes, 331 frames at 24 fps.
Camera metadata identifies a URSA Cine 17K 65, firmware 9.3, recording at Q3.
Visible output is 17520x8040; stored planes are 17568x8064. The measured source
compression ratio is 20.84:1.

## Layout and automatic handling

This recording uses the existing native RGBW layout `0x080c`: 12x12 brightness
and 12x6 colour transforms, four coefficient streams and 16-bit side fields.
It differs from the scaled 4K/8K transform layouts. The metadata block is
`0x280` bytes, and the container sample entry is `brvo`; dimensions and crop
are read from the file rather than inferred from the camera name.

All 331 source frames pass a canonical entropy decode/re-encode **byte for
byte**, including headers, coefficient streams, side fields, offsets and
padding. No new transform implementation was needed.

Automatic GPU encoding consistently uses two layers for every supported RGBW
recording, including scaled footage. Resolution and the measured quality gain
do not change that default. The CPU encoder retains its single 12-bit layer.

## Memory and GPU tiles

The archive has six 5856x4032 tiles, twelve video tracks with two layers. Base
and detail encoding run in groups of at most four tiles, also bounded by the
pixel count of the previously tested four-tile 12K path. Base passes collect
restore data only once; muxing preserves the original tile order.

During reconstruction, four detail tiles use the GPU and the remaining two
use the CPU. Base decoding uses the CPU. Each track's own pixel layout is
used when combining layers. Single-layer six-tile archives decode entirely
on the CPU to keep their stitching layout consistent. Concurrent seek decoders
are also limited to four. The archive format and stored values are unchanged.

The real sample tests passed on the RTX 3060 Ti with 8 GiB VRAM. During layered
verification, observed total GPU use was 6910 MiB, including the desktop;
archive test and ffmpeg working sets together were approximately 11.2 GiB at
that observation. These are observations, not a complete peak-memory profile.
See "Full-length archive" below for the whole-clip run.

## Archive comparison

Both runs encoded the same eight source frames: 0-3, 165-166 and 329-330,
using balanced NVENC settings and each layer mode's automatic quality level.
The base quality was 22. Files below contain the sampled video tracks, without
the full source's sound or restore attachment; they are not whole-clip size
estimates or finished full-clip archives.

| Layers | Quality | Sample video size | Fine-detail score | Plane PSNR |
| --- | ---: | ---: | ---: | ---: |
| One | 10 | 8,153,025 bytes (7.8 MiB) | 83.5% | 59.68 dB |
| Two, default | 19 | 29,117,839 bytes (27.8 MiB) | 92.9% | 62.60 dB |

Two layers gain 9.4 percentage points at 3.57 times the sampled video size.
Residual clipping was 0.000000%. Every sampled frame's header and side fields
match the source; every track has exactly eight frames. Beginning, middle and
end sampled archive frames rebuild to canonical BRAW frames, with 65.95-66.57
dB between archive and rebuilt brightness planes.

The detail score is the project's sensor-plane fine-band error metric, not
a literal percentage of visible scene detail or resolution. Eight-frame
sampling is smaller than the 96-frame comparisons used for the earlier 4K/8K
investigation, and these are different scenes and recording settings.

## Full-length archive

On 27 October 2026 the whole 331-frame clip was packed with the command
VideoManager uses (`pack -speed slow -encoder nvenc`, two layers, automatic
quality 19) on the RTX 3060 Ti and 32 GB of RAM:

- 3.13 GiB -> 940.8 MiB (986,535,728 bytes, 29.4% of the original).
- 1,643 seconds in all, of which about 340 seconds of verification.
- Every frame was compared with the source and verified; 93.0% of the fine
  detail was kept; a rebuild at the source's own tables measures 64.80 dB
  brightness and 62.67 dB colour, 85.3% of the original picture size.
- All twelve video tracks of the encoded picture held exactly 331 packets.
- During verification the GPU held about 6.8 GiB, and free system memory fell
  to about 0.4-1.2 GiB while other sessions were also running.

An earlier full-length attempt that day failed its check with "archive video
ends mid-frame at frame 328", meaning one decoder delivered fewer frames than
the others. The same code and command then passed, so that failure depended on
run-time conditions, most likely other work sharing the 8 GiB GPU or RAM at
the time; its temporary files were gone, so the exact cause was not confirmed.
Machines meant for 17K footage are expected to have more headroom, so no
change was made. If it recurs, check first whether another job was using the
GPU, and keep the `.video.tmp.mkv` to count each track's packets with ffprobe.

## Full-clip smaller-BRAW compression

`shrink -ratio 25` processed the entire 331-frame recording in 165 seconds:

- Original: 3,358,442,154 bytes (3.13 GiB).
- Result: 2,728,914,010 bytes (2.54 GiB), 81.3% of the original size.
- Measured output ratio: 25.65:1; sampled fine-detail score: 98.7%.
- Every rewritten frame passed an immediate canonical read-back check.
- The finished container reopened with all 331 frames and unchanged sound,
  timecode and motion chunks; six spread frames passed plane decoding.

## Independent decoder validation and limits

Blackmagic RAW SDK and Player were updated from 4.2 to **6.0** on 20 October
2026 using the vendor-signed Windows installer. No restart was requested.
The previous SDK could not open the original 17K source. RAW 4.5 introduced
17K 65 support; the official release catalogue is at
https://www.blackmagicdesign.com/api/support/us/downloads.json .

The old `braw-ref` helper also needed rebuilding. The new SDK inserts methods
in the codec and callback interfaces, so a helper built with the old headers
calls the wrong vtable entries. This explains the access violations seen both
with the newer Resolve DLL and, after updating, with the standalone SDK.

The helper now uses SDK 6.0 headers, queries for its expected codec interface
before calling typed methods, implements the audio callback slot and proper
COM reference counting, and releases jobs from their submitting owners.
It checks that decoded resources are CPU RGB16 buffers of the expected size.
Native faults report to stderr without a blocking Windows error window.
Both the main and packaging copies of `braw-ref.exe` were replaced.
Production library discovery continues to use standalone RAW rather than Resolve.

SDK 6.0 independently decoded source frames 0, 165 and 330, the corresponding
frames of the completed smaller-BRAW output, and fixtures rebuilt from the
two-layer archive samples. All outputs have the expected 17520x8040 dimensions.
Compared with the SDK's as-shot RGB16 decode of each original source frame:

| Source frame | Smaller BRAW RGB16 PSNR | Archive-rebuilt sample RGB16 PSNR |
| ---: | ---: | ---: |
| 0 | 47.31 dB | 42.91 dB |
| 165 | 47.37 dB | 39.33 dB |
| 330 | 47.39 dB | 39.20 dB |

The unmodified frame-0 fixture decodes pixel-identically to the original clip,
confirming the fixture container is suitable for these image checks. A frame-0
source-to-12-bit-planes round trip measures 56.99 dB through the SDK. Coarser
quantization and tilt fixtures also decode successfully.

The final helper additionally passes frame-0 protocol checks for the real 4K,
8K, 12K and 17K recordings using the installed Player runtime. A missing input
returns exit 1 with a readable error and no image output. Package tests and vet
pass; the helper source received a separate static review.

SDK comparisons cover the samples above, not every frame of the full clip.
A full-clip archive has been made and verified (see above); a full-clip
rebuild, a complete 17K viewing-LUT fit, and GUI use with this footage have
not been run. Archive encoding validation still uses the eight
real source frames described above.

Reports: `research/ursa-17k/sdk6-image-checks.json`, `sdk6-archive-checks.json`,
`sdk6-protocol-checks.json`, and `sdk-validation-status.json`.

## Reproduction

Logs and output files are under `research/ursa-17k/` (large artifacts ignored).

```powershell
$env:BRAW_V5_CLIP='X:/temp/compress3/A003_02171447_C055.braw'
go test ./internal/core -run '^TestV5Rewrite$' -count=1 -v
$env:BRAW_V5_17K_CLIP=$env:BRAW_V5_CLIP
go test ./internal/core -run '^TestUrsa17KArchive$' -count=1 -v
go test ./internal/core -run '^TestUrsa17KRebuildSample$' -count=1 -v
braw-pack shrink -ratio 25 -o research/ursa-17k/outputs/shrink-25 $env:BRAW_V5_CLIP
```

All package tests, desktop GUI tests, and vet pass after the production changes.
