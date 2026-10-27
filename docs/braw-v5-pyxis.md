# Pyxis 6K and Cinema Camera 6K (Blackmagic RAW version 5, layout 0x0804)

Blackmagic's 6K full-frame Bayer cameras record picture-header version 5 with transform layout
`08 04` at `hs+0x12`. The URSA Cine modes use `08 0c` (native 12K/17K), `09 14` (scaled 8K) and
`0a 14` (scaled 4K). braw-pack reads this layout through the scaled-mode code path
(`v5_scaled.go`, `scaled_v5planes.go`), with the differences below. As for the other modes,
the layout was worked out from the clips and checked against Blackmagic's decoder (SDK 6.0,
through `braw-ref`).

Sample clips:

| clip | camera | picture | ratio | frames | qscale0 |
|---|---|---|---|---:|---|
| `A001_11101030_C010.braw` | Pyxis 6K | 6048x4032, 23.976 fps | 8:1 | 226 | 4-5 |
| `A003_11111050_C033.braw` | Pyxis 6K | 6048x3200, 30 fps | 8:1 | 476 | 7-8 |
| `A003_09060807_C002_248.braw` | Cinema Camera 6K | 6048x4032, 24 fps | 3:1 | 246 | 0 |

The 6048x3200 clip's own metadata (`crop_size`, `sensor_area_captured`) gives 6048x3200. That
size is the camera's 17:9 6K mode, not the 16:9 6048x3408 mode.

## Geometry

The header dimensions are the coded plane size, without any scaling: 6064x4048 or 6064x3216,
16 pixels larger than the picture, which the container's crop selects. Tiles are 1520 or 1504
pixels wide (four columns) and 128 or 104 high. A tile is a raster of 16x8 macroblocks, as in
versions 1 and 2.

## Macroblocks and streams

A macroblock holds two 8x8 brightness blocks, then one 8x4 block for each colour plane, then an
8-bit side byte in stream 0. This is the scaled 8K mode's layout at native size. The tile
table, stream order and padding are the same as in the 12K mode (`docs/braw-v5.md`). All four
streams are used:

| stream | brightness scan positions | colour scan positions |
|---|---|---|
| 0 | 0-3 (DC, side byte) | 0-1 |
| 1 | 4-15 | 2-7 |
| 2 | 16-31 | 8-15 |
| 3 | 32-63 | 16-31 |

Unlike the scaled modes, **a filled block still ends with an end-of-block code**. That code goes
in stream 3, as in the 12K mode (`v5ReadBlock`).

These parameters were found by requiring that every stream of every tile be consumed exactly.
Stream 0 alone fixes the first split and the 8-bit side field. Streams 0-1 and then 0-2 fix the
next splits independently of the later streams. The bottom tile row appears to under-read only
because of the 512-byte padding before each column's next stream run.

**Validation:** every frame of all three clips (948 frames) decodes to levels and re-codes byte for
byte (`TestV5PyxisCameraRewrite`, `BRAW_PYXIS_CLIP`, `BRAW_PYXIS_ALL=1`).

## Scan order

The scan is the scaled 8K mode's (rings of 2x2, 4x4 and 8x8 brightness coefficients, and of
1x1, 2x2 and 4x4 colour pairs, each ring in zigzag order). The four-stream splits simply cut that
order at 16 and 32 (8 and 16 for colour).

The scan was confirmed with the SDK. Copies of a frame were made with one group of scan positions
zeroed, both versions were decoded with Blackmagic's decoder, and the picture difference was
correlated with the difference each candidate scan predicts in braw-pack's planes:

| zeroed | 8K scan | transposed | rows 0-3 first | columns 0-3 first |
|---|---:|---:|---:|---:|
| brightness 4-15 | 0.85 | 0.09 | 0.85 | 0.85 |
| brightness 16-31 | **0.69** | 0.08 | -0.12 | 0.07 |
| brightness 32-63 | **0.62** | 0.28 | 0.02 | 0.10 |
| colour 8-15 | **0.74** | — | 0.08 | 0.34 |
| colour 16-31 | **0.71** | — | 0.04 | 0.14 |

(A plain blockiness measure of the decoded planes could not tell the candidates apart: the
URSA 8K clip, with its known scan, scores worse on it than any candidate here.)

## Quantisation

The AC steps follow the URSA law: brightness `50*(qscale0+4)`, colour
`50*max(4,floor((qscale0+5)/2))`. The header limits at `hs+0x20`/`hs+0x22` are 1280 and 256,
the same as the URSA clips, and the bytes `hs+0x14..0x1b` match the scaled modes.

**The DC limit factor differs:** the Pyxis caps DC at `header_limit * 1.514` (`v5Full8DCCap`,
53/35), where the URSA scaled modes use 10/7. The camera's own qscale0 (0-8 on these clips)
never reaches the cap, so archives are unaffected. Rebuilding at a coarser quantiser
(`unpack -quant`) does reach it: colour from qscale0 11, brightness from 33.

Measured with the SDK by re-coding frame 0 of the 8:1 clip at qscale0 `q` and comparing the
picture's mean colour with the original's:

- With 10/7, `q` 6 to 10 change the mean by under 0.00003. From 11 on, red drops by 0.0040 and
  green rises by 0.0016, at every `q` up to 24. The same test on the URSA 8K clip changes its
  mean by under 0.0004.
- A sweep of the factor at `q` 14 and 24 crosses zero at 1.514 ± 0.001, where the change is under
  0.00005. 1.0 shifts red by 0.04 to 0.18, and no cap shifts it by 0.009 to 0.026.
- At `q` 40 and 60, where the brightness cap binds too, 1.514 changes the mean by under 0.0005,
  against 0.04 to 0.16 with 10/7 or no cap.

The factor is a measurement, not a known formula; 53/35 lies within it.

## Pack, rebuild and compression

- The RGBW-only choices do not apply (`frameInfo.rgbw`). `-layers auto` picks one GPU layer, as
  for other Bayer cameras. The automatic quality uses the 8x8 band of `sourceStep`, as for
  versions 1 and 2.
- A 6064x4048 picture is the first single-track picture large enough for the encoder feed to be
  paced (over 64 MB of planes). NVENC's B-frames held more frames than the feed's window, so
  `pack` stalled before its first frame. Paced NVENC tracks are now coded without B-frames, as
  tiled ones already were.

Results on the 8:1 Pyxis clips (default settings, RTX GPU encoder, every frame verified):

| clip | archive | detail kept | rebuilt vs original through the SDK (R/G/B PSNR) |
|---|---|---:|---|
| 6048x4032 | 994.5 MB -> 37.3 MB, 4m48s | 96.3% | 43.8-46.6 / 48.7-51.6 / 44.6-47.5 dB |
| 6048x3200 | 1.63 GB -> 69.6 MB, 6m32s | 97.6% | 46.3-48.0 / 50.0-52.2 / 47.0-48.7 dB |

PSNR is on 16-bit RGB at frames 0, middle and last. Mean colour matches within 0.001. Converting
to planes and back without HEVC gives 70-76 dB with identical means (the 12-bit planes are the
limit there). `unpack -quant 2` gives the same closeness as x1 at 64% of the size. `shrink` to
12:1 keeps 99.5% of the detail and gives 51.6-55.3 dB with identical means. `estimate` works.
Viewing LUTs (`-lut`) and the app have not been tried with these clips.
