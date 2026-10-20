# URSA Cine 8K layout and layer comparison

Source: `A001_09302023_C039.braw`, 647 frames at 50 fps, 8192x3408 output,
nominal BRAW 5:1; source size 5,473,893,220 bytes (5.10 GiB).

## Automatic layout detection

The header identifies the coding layout independently of the camera name and
the displayed pixel dimensions. The decoder now recognizes:

| Layout header | Brightness block | Colour block | Live streams | Side field |
| --- | --- | --- | ---: | ---: |
| 0x080c (12K sample) | 12x12 | 12x6 | 4 | 16 bits |
| 0x0914 (scaled 8K) | 8x8 | 8x4 | 3 | 8 bits |
| 0x0a14 (scaled 4K) | 4x4 | 4x2 | 2 | 8 bits |

Scaled modes use the shared parameterized reader, writer and transforms. Crop,
aspect ratio, frame dimensions and tile geometry come from the clip. Unknown
coding layouts remain refused until their interpretation has been validated.

For this 8K clip, header dimensions 12336x2568 become padded 8224x3424 planes.
Each 16x8 macroblock has two brightness and two colour blocks. A full block
omits its terminal EOB. The padded width exceeds the NVIDIA 8192-pixel limit,
so the archive uses two horizontal tiles; two-layer encoding has four tracks.

All 647 frames passed independent byte-identical entropy/layout reconstruction.
The production canonical encoder also passed beginning/middle/end samples;
plane conversion changes at most one coefficient level on fewer than 0.001%
of coefficients in these frames. Independent SDK plane reconstruction gave
93.88 dB RGB16 PSNR on frame zero.

## Quantizer refinement and compatibility

Finer SDK probes on both 4K and 8K establish the exact scaled DC cap:
`header_limit * 10/7`, without integer rounding. This supersedes the initial
4K approximation `ceil(sqrt(2)*header_limit)`. Synthetic positive and negative
DC fixtures produce pixel-identical SDK images under the exact rational law.

New archives use `v5_steps=3`. Steps 1 and 2 remain supported; steps-2 4K
archives retain their original approximate model on rebuild. Three decoded
frames of the previous archive were rebuilt byte for byte to their existing
BRAW reference. Full-size 12K steps 3 equal steps 2.

## Comparison method

Each estimate actually encoded the same 96 source frames: four stretches of
24 frames spread across the 647-frame clip. Balanced NVENC settings were used
with one layer at quality 4, 10, 16 and two layers at detail quality 16, 22, 28
(base quality 22). A further detail-quality 25 run targets a similar budget to
the automatic one-layer result. Sizes are estimates for the whole source,
including approximate audio/restore overhead, not completed full-clip archives.

The reported detail score measures sensor-plane errors in a fine spatial band;
it is not a percentage of visible scene detail or output resolution. Different
clips, framing and recording compression prevent isolating resolution alone
as a cause. This source's measured camera step is about 0.94 plane values,
versus 2.16 on the earlier 4K source, so it contains finer coded differences.

On twelve spread frames, rounding the planes to ten bits without HEVC retains
98.8% of the measured detail. The much larger one-layer loss therefore arises
primarily in the encoder, not the initial ten-bit conversion.

## Results

| Layers | Quality | Estimated whole archive | Fine-detail score |
| --- | ---: | ---: | ---: |
| one | 4 | 530.6 MiB | 57.5% |
| one, automatic | 10 | 81.6 MiB | 61.0% |
| one | 16 | 33.2 MiB | 60.3% |
| two, automatic | 16 | about 1.05 GiB | 86.6% |
| two | 22 | 166.6 MiB | 70.9% |
| two | 25 | 68.9 MiB | 67.3% |
| two | 28 | 40.7 MiB | 64.7% |

At the automatic settings, the second layer adds 25.6 percentage points at
approximately 13 times the archive size. At the budget-oriented quality 25,
two layers improve the score by 6.3 points while producing an estimate about
16% smaller than automatic one-layer encoding. Even two layers at quality 28
outscore all tested one-layer settings at roughly half the automatic one-layer
size. Lower one-layer quality numbers do not resolve the encoder's error floor;
the metric need not improve monotonically with the number of bits spent.

By comparison, the previous 4K automatic estimates were 39.3 MiB/89.1% versus
64.0 MiB/91.7%: 63% more storage for 2.6 points. Two layers therefore have a
materially different storage/quality trade-off on these two source clips.

All package tests, desktop GUI tests and vet pass. Beginning/middle/end source
frames pass canonical byte-identity and plane conversion tests. All 647 source
frames also pass the independent entropy-token/layout identity gate. Existing
4K frame tests and old steps-2 archive reconstruction pass after generalization.

## 17K scope

Blackmagic specifies the URSA Cine 17K 65 at 17520x8040:
https://www.blackmagicdesign.com/products/blackmagicursacine/techspecs/W-URSA-62 .
That is about 43% more pixels than 12288x8040, not several times as many.
Using this archive's planar16 storage, one unpadded 17K YUV420 frame would be
about 403 MiB; transforms, encoder/decoder buffers and concurrent frames add
substantially to this. The present GPU tiling calculation would require six
tracks at that size, so GPU-session batching and memory use need testing.
A native 17K sample was subsequently tested with bounded encoding groups and mixed CPU/GPU decoding; see [braw-v5-17k.md](braw-v5-17k.md) for the results and remaining SDK validation limit.
