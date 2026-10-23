# URSA Cine 12K LF scaled 4K coding mode

Validated recording: `A001_09302216_C095.braw`, 811 frames, 4096 x 2680 output,
BRAW 4:1. This mode identifies itself with picture-header version 5 and
`+0x12..0x13 = 0a 14`. The 12K open-gate recording previously supported by
braw-pack has `08 0c` there.

## Geometry

The picture-header dimensions are **not output pixels**:

| quantity | header units | coded plane pixels |
| --- | ---: | ---: |
| width | 12336 | 4112 |
| height | 4032 | 2688 |
| tile height | 252 | 168 |
| inner tile width | 768 | 256 |
| outer tile width | 792 | 264 |
| macroblock | 24 x 6 | 8 x 4 |

Divide horizontal values by 3; multiply vertical values by 2/3. The container's
`brxr` crop is left 8, top 4, width 4096, height 2680. The padded plane sizes
should be used by archival codecs; source frame headers retain their original
geometry bytes for reconstruction.

## Blocks and entropy

A macroblock contains two brightness 4 x 4 blocks (left, right), then two colour
4 x 2 blocks, then one opaque **8-bit** side field. Colour plane geometry is
half the brightness dimensions in both directions.

The 1024-entry stream-offset table keeps the same layout as the 12K mode:
`column*4*tile_rows + stream*tile_rows + row`. Streams 2 and 3 are empty in this
4K recording. Stream 0 holds brightness scan positions 0..3, colour positions
0..1, DC differences, and side bytes. Stream 1 holds the remaining positions
4..15 or 2..7. Huffman tables, level signs, DC prediction, and stream ownership
by the start of the zero run are unchanged.

**A completely filled block has no trailing end-of-block symbol.** After coding
position 15 in brightness or 7 in colour, advance directly to the next block.
The earlier 12K decoder always reads an EOB, even after a filled 12 x 12 block;
reusing it here loses alignment. This was the cause of the reported coefficient
index error.

Brightness scan order is the first 16 entries of the existing nested-square scan,
expressed with a stride of 4 instead of 12. Colour uses the first 8 entries with
the same stride conversion. These correspond to orthonormal 4-point horizontal
and 4-point/2-point vertical DCTs for the archival plane conversion.

## Validation

`TestV5ScaledBlockRoundTrip` covers consecutive sparse and filled blocks and
checks exact bit consumption, including the missing trailing EOB.

`TestV5ScaledCameraRewrite`, with `BRAW_V5_SCALED_CLIP` set, checks the beginning,
middle, and final source frame byte for byte after decoding and canonical entropy
encoding. Set `BRAW_V5_SCALED_ALL=1` to check every frame. Frames 0, 405, and 810
have passed, at 5,394,688, 4,039,936, and 4,208,384 bytes respectively. All 811 frames also passed the byte-for-byte gate in 91.26 seconds.

## Quantization

The finer 8K investigation superseded the first 4K DC-limit approximation below:
both scaled modes use the exact limit `header_limit * 10/7`, without rounding.
New archives use steps version 3; existing steps-2 4K archives retain the earlier
model on rebuild and were checked against their existing rebuilt frames byte for byte.

AC steps follow the 12K mode's law: brightness `50*(qscale0+4)`, colour
`50*max(4,floor((qscale0+5)/2))`. The two header DC limits, however, are in different
units: use `header_limit * 10/7` before taking the smaller of the DC limit
and the AC step. Using the 12K mode's literal limits causes a colour shift when
rebuilding at a coarser quantizer. This distinction was measured through independent
SDK decodes of altered headers and pixel-identical synthetic frames with rational DC products.

The metadata header can be 0x100 bytes, as on older Bayer recordings. `scaleQuant`
must inspect the version byte, rather than treating that header length as evidence
that the frame has an old-style quantization table.

Plane transforms use four samples per row and four/two rows, with orthonormal
steps scaled by 4/sqrt(8). Tests on frames 0, 405 and 810 preserve all side bytes,
change fewer than 0.06% of coefficients, and keep every coefficient within one
level after converting to 12-bit planes and back. No transform coefficient is
discarded to make the geometry fit the video encoder.

Malformed zero-row layouts and nonzero data in unused streams are refused. This
check applies to every decoded frame, including frames outside the initial sample.

## End-to-end checks

The full 811-frame smaller-BRAW run at target 8:1 produced a 1.50 GiB clip from
the 3.14 GiB source (measured 8.3:1). The measured fine-detail score is 97.4%.
Blackmagic's decoder successfully decoded all 811 output frames. RGB16 comparisons
at the beginning, middle and end gave 44.4-45.5 dB PSNR. Sound, timecode and motion
chunks were also compared with the source before the output was accepted.

These scores describe lossy output, not bit-exact RAW preservation or a percentage
of visible scene detail. The coefficient identity gate establishes the parser;
the SDK checks independently establish that the generated BRAW can be decoded.

The default GPU archive is 49,847,240 bytes (47.5 MiB, 1.48% of the source), with
two untiled video layers, quality 21 for detail and 22 for the base. The residual
clipping score rounds to 0.0000%. Every source frame was compared with the archive;
the whole-clip detail score was 92.6%. Packing took 419 seconds on this machine.
Default rebuilding used the source quantization, took 148 seconds, and produced
2,148,455,284 bytes (2.00 GiB). Blackmagic's SDK successfully decoded all 811
rebuilt frames; first/middle/last comparisons gave 41.2-44.7 dB RGB16 PSNR.

One- and two-layer estimates were repeated with the final implementation on the
same 96 frames, in four stretches spread through the source:

| GPU path | Automatic quality | Estimated archive | Fine-detail score |
| --- | ---: | ---: | ---: |
| one layer | 10 | 39.3 MiB | 89.1% |
| two layers | 21 (base 22) | 64.0 MiB | 91.7% |

The second layer therefore still helps this 4K RGBW recording, with a smaller
gain than on the earlier 12K example. These are each path's automatic settings,
not a comparison at equal file size; the quality numbers refer to different
representations. The size estimates include approximate non-video overhead and
short sampled stretches; they are not exact predictions of the full archive.

`go test ./...`, the desktop GUI tests including scanning the real generated
clips and archive, `go vet ./...`, and the 98-frame 12K byte-identity regression
also passed. Source files and updated CLI/GUI executables are in the main
`X:/braw-pack` folder. The pre-integration main files and executables are saved
under `research/ursa-4k/main-before-integration/`.

