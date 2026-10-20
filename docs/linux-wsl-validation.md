# Linux validation in WSL2

Checked on 20 October 2026 in Ubuntu 24.04.1 LTS, x86-64, under WSL2.
The distro has 16 GiB RAM and 4 GiB swap. Hardware is an RTX 3060 Ti with
8 GiB VRAM; the NVIDIA driver reports 591.86.

## Build and startup

- Go 1.27.0 built the CLI and desktop GUI from the current main checkout.
- All package tests pass with `webkit2_41`; desktop/production GUI tests and
  vet also pass.
- The bundled Linux FFmpeg 7.1.1 passes a real NVIDIA test encode and includes
  x265. Synthetic tiled/layered encode and decode tests pass.
- GTK 3.24.41 and WebKitGTK 4.1 package version 2.52.6 were available; the
  desktop executable has no unresolved shared libraries.
- The GUI launched under Xvfb and remained running for the 15-second smoke
  test, then was stopped intentionally. Headless DRI3 rendering warnings were
  nonfatal. Interactive file selection and a complete GUI workflow were not
  automated.
- The CLI is a native, statically linked Linux ELF executable. The GUI needs
  its normal GTK/WebKit system libraries.

## Full 12K archive and rebuild

Input: `A001_11190957_C081.braw`, 98 frames, 12288x8040 visible picture.

The packaged CLI completed `pack` using NVIDIA, automatic quality 16 and
automatic two layers, with four 6168x4032 tiles. Every frame was verified
against the source. Residual clipping was zero; the sensor-plane fine-detail
score was 91.1%, matching the previous Windows result.

The resulting archive is 382,353,862 bytes (364.6 MiB). Pack took 376 seconds.
Default `unpack` completed all 98 frames at source quantization, producing
878,166,327 bytes (837.5 MiB) in 209 seconds.

Blackmagic RAW SDK 6.0 on Windows independently decoded frames 0, 49 and 97
of the Linux-rebuilt file at 12288x8040. RGB16 PSNR against the SDK's decode
of the original source was 43.44, 41.34 and 41.44 dB. This is a lossy archive;
the images are not expected to match the original pixel for pixel.

Observed WSL memory during 12K verification reached approximately 14 GiB with
no swap in use at that observation. These are observations, not a continuous
peak-memory profile.

## Real 4K, 8K and 17K samples

Beginning, middle and end frames of the scaled 4K and 8K recordings pass
byte-exact canonical frame rewriting in Linux.

The native 17K source `A003_02171447_C055.braw` passed the eight-frame archive
comparison using six GPU tiles, bounded encoding groups and mixed CPU/GPU
decoding. Sizes and scores match Windows:

| Layers | Automatic quality | Sample video bytes | Fine-detail score |
| --- | ---: | ---: | ---: |
| One | 10 | 8,153,025 | 83.5% |
| Two | 19 | 29,117,839 | 92.9% |

No residual samples were clipped. Beginning, middle and end sampled archive
frames rebuild into canonically readable BRAW frames; archive-to-rebuild
brightness PSNR is 65.95-66.57 dB. Archive sampling used source frames 0-3,
165-166 and 329-330, not the full 331-frame archive.

Full-clip `shrink -ratio 25` also passed on the 17K recording: all 331 frames
were written and checked, producing 2,728,914,010 bytes (2.54 GiB), 81.3% of
the 3.13 GiB source. The output reaches about 25.65:1 and retains 98.7% on the
sampled fine-detail metric. This run took 285 seconds.

The complete Linux and Windows smaller-BRAW outputs are byte-for-byte
identical by SHA-256:
`908c3ae82c16ba19ea14a31d7a6a6c423933641523ea9d3f12c430315f320410`.

## Scope and artifacts

These Linux operations did not use Blackmagic's SDK or runtime. Optional
viewing-LUT generation remains Windows-only; it was not part of Linux testing.
The Windows SDK was used only for the separate cross-platform image check.

This confirms the tested Ubuntu/WSL environment, not every Linux distribution
or GPU. WSL throughput was lower than the earlier Windows measurements.

The rebuilt distribution is `dist/braw-pack-0.1.0-linux-amd64.tar.gz`. Test logs,
output files and the SDK comparison report are under
`research/linux-wsl-20261020/`. Large artifacts and caches are ignored.

The opt-in 17K tests accept `BRAW_V5_17K_OUTPUT` to isolate the Linux sample
archives from the existing Windows research outputs.
