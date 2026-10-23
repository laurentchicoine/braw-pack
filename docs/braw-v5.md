# Blackmagic RAW version 5 (URSA Cine 12K)

How braw-pack reads and writes the frames of Blackmagic RAW clips whose picture header says
version 5, as recorded by the URSA Cine 12K LF (RGBW sensor). The code is in
`internal/core/v5.go` (bitstream) and `internal/core/v5planes.go` (planes).

This was worked out from the clips themselves, from Blackmagic's published patents (EP3907994 /
US11765366 "Method for processing transform coefficients", which describes splitting coefficients
into sub-streams; EP3547680 / US11258992, which describes the RGBW filter array), and by
decoding altered copies of a clip with Blackmagic's decoder through its public SDK and comparing
the pictures. It was checked on one clip: 98 frames of 12288x8040 open gate, recorded at 12:1.

## What is the same as versions 1 and 2

- The container, the per-frame metadata atoms ("bmdf" block) and the picture header fields:
  relative to the header, the tile counts, plane size, tile height, `qscale` fields and tile
  widths are at the same places.
- The entropy code: DC differences and AC run/level codewords use exactly the code tables of
  versions 1 and 2 (`tables.go`), with the same sign rules and the same DC prediction: one
  predictor for brightness and one for each colour plane, reset at the start of every tile.
- The planes: one brightness plane at full size and two colour planes at half size in both
  directions (4:2:0 geometry), still in sensor space.

## What differs

### Frame layout

- The metadata block is 0x400 bytes (it ends with a `skip` atom), so the picture header ("braw",
  big-endian size of the picture part, version byte 5) is at 0x400 instead of 0x100. Call that
  offset `hs`.
- There is no quantisation table: the area after the tile table is zero.
- The tile table (big-endian u32 offsets relative to `hs`, at `hs+0x80`) has four entries per
  tile. Entry `c*4*nth + s*nth + r` is stream `s` (0..3) of tile column `c`, tile row `r`. With
  16x16 tiles that is `c*64 + s*16 + r`, filling all 1024 slots.
- Streams are stored in table order. Each stream is padded with zeros to a multiple of 8 bytes;
  each run of one stream for one tile column (16 streams) starts on a 512-byte boundary, and the
  picture part is padded to 512 bytes. The size field at `hs+4` covers the padded picture part.
- The 16 bytes just before the first stream are opaque, as in versions 1 and 2, and are kept.

### Tiles and macroblocks

- Tiles are 768 pixels wide (792 for the first and last column) and 504 high, all multiples of
  24 x 12. Plane size 12336x8064; Blackmagic's picture is the plane without 24 pixels on the left
  and right and 12 at the top and bottom (12288x8040).
- A tile is a raster of 24x12 macroblocks (32x42 or 33x42), left to right, top to bottom.
- A macroblock holds, in order: brightness block 0 (left 12x12), brightness block 1 (right
  12x12), colour block 1 (12 wide x 6 high, half resolution), colour block 2, then a 16-bit side
  field. Raising colour block 1 in Blackmagic's decoder raises blue against red and green (a
  B-G-like difference); raising colour block 2 raises red against green (R-G-like). Coded values
  are on a strongly non-linear, log-like scale.
- The side field is 0xFFFF in almost every macroblock; other values trace the edges of clipped
  highlights (probably clipping flags, maybe one 2-bit field per 6x6 filter-array cell). braw-pack
  stores it unchanged, as it does the 8-bit side byte of versions 1 and 2.

### Coefficient sub-streams

Each block's coefficients are split between the tile's four streams by frequency:

| stream | brightness scan positions | colour scan positions | decoded alone with the earlier ones |
|---|---|---|---|
| 0 | 0-3 (2x2 corner; also DC and side fields) | 0-1 | 1/6 resolution (2048x1340) |
| 1 | 4-15 (rest of 4x4) | 2-7 | 1/3 (4096x2680) |
| 2 | 16-63 (rest of 8x8) | 8-31 | 2/3 (8192x5360) |
| 3 | 64-143 (rest of 12x12) | 32-71 | full |

These are exactly the resolutions Blackmagic's SDK offers for the clip.

Reading a block uses four bit readers at once (each MSB first). The DC difference is read from
stream 0. Then, with `i` the last coefficient position (0 after DC), each codeword (run/level,
zero-run-of-16, or end-of-block) is read from the stream that owns position `i+1`, or from
stream 3 once `i+1` is past the end of the block. The level bits follow their codeword in the
same stream. So a codeword lives in the stream where its zero run starts, and the end-of-block
code in the stream after the block's last coefficient; it is always present, even after a
coefficient at the last position. Writing mirrors this exactly, which is why braw-pack can
re-code every frame of the test clip byte for byte (`TestV5Rewrite`).

### Scan order

Brightness blocks (12x12) are scanned ring by ring: the 2x2 corner, the rest of 4x4, the rest of
8x8, the rest of 12x12; inside each ring, coefficients follow the usual zigzag (first step to the
right) restricted to that ring.

Colour blocks are 6 rows by 12 columns of coefficients. They are scanned the same way over a
6x6 grid of pairs of horizontally adjacent coefficients (rings of 1x1, 2x2, 4x4, 6x6 pairs),
like the paired scan of versions 1 and 2.

### Transform

12x12 DCT for brightness, 12-wide x 6-high DCT for colour. braw-pack uses orthonormal transforms.

### Quantisation

Frames carry no table of steps. Measured with Blackmagic's decoder, by coding chosen levels into a
frame and decoding it at full resolution:

- **One step for every frequency.** In orthonormal DCT terms, one level of any coefficient is
  worth as much as one level of DC, for brightness and colour alike. What the decoder makes of
  each frequency does fall off smoothly with frequency (to about 0.8 of DC at the top for
  brightness, 0.54 for colour, separable in rows and columns), which is its demosaicing and
  colour upsampling; there is no jump at any of the stream boundaries. That the camera's
  outer ring (stream 3) is almost empty is its encoder's choice, not a coarser step.
- **qscale0 (`hs+9`) sets the steps.** In the decoder's own units: brightness AC 50*(q0+4),
  brightness DC the same capped at qscale1 (u16 at `hs+0x20`, 0x0500 here); colour AC 50*c with
  c = max(4, floor((q0+5)/2)), colour DC the same capped at qscale2 (u16 at `hs+0x22`, 0x0100
  here). So brightness steps change with every qscale0, colour steps only from qscale0 5, and
  the caps only bind from qscale0 22 (brightness) and 7 (colour DC). Raising a cap above the step
  changes nothing.
- **The camera uses it for rate control.** On the test clip (12:1) it codes frames 0-74 at
  qscale0 3 and frames 75-97 at qscale0 2 (14% larger); the same picture's DC levels are then 7/6
  as large, without offset.

The evidence: with brightness levels rescaled by 7/(q0+4), the response to a DC change is exactly
(q0+4)/7 for q0 = 0..8 and colour's is unchanged for q0 = 0..4 (full resolution); frames
re-coded at any qscale0 up to 64 with every level rescaled by this law (and the caps lifted)
decode to the original picture up to rounding (at 1/6 resolution, which shows the lowest
frequencies; at full resolution for qscale0 9 and 23); and the caps were found by scanning `hs+0x20`/`hs+0x22`: at qscale0 3 the
picture changes from 0x15c and 0xc6 down, not from 0x15e and 0xc8 up (steps 350 and 200), at
qscale0 5 from 0xf8 down (250).

### Other header fields

- `hs+0x12` (0x080C here, 0x0004 in versions 1 and 2) is geometry: 0x0004 changes the picture
  size the SDK reports, 0x090C is refused.
- `hs+0x14..0x1b` (0010 0010 010e 020c) are four pairs (k, 16-2k); only 2k plus the second byte
  matters. Changing the first three pairs alters 0.02-0.3% of the pixels, the fourth breaks the
  tones of half the picture: format parameters, not steps.
- `hs+0x1c` (0x01A7 at qscale0 3, 0x01AA at qscale0 2 in the camera's frames) and `hs+0x1e`
  (0x0183) change the picture only slightly (at most 0.014-0.03 on a linear scale where white
  clips near 19), whatever their value: perhaps strengths of a filter that follows the
  quantisation. braw-pack keeps them as recorded.

## Not known yet

- How DC levels map to Blackmagic's 12-bit values (offset and scale).
- The meaning of `hs+0x1c`/`hs+0x1e`, of the side field, and of the extra metadata atoms
  (`shdp`, `dcpu`, `dcpg`, `dcpr`, `dcpb`, all zero here).

## How braw-pack handles it

- **Planes.** Levels are turned into samples with flat steps scaled by the frame's qscale fields
  as above ("steps 2", the same as steps 1 at qscale0 3), so the planes show the picture the
  decoder sees whichever qscale0 the camera chose. Archives record the steps they were made with
  (`v5_steps`); the first builds that read these clips made "steps 1", flat steps that ignore
  qscale0 (their planes jump by 7/6 where the camera switches qscale0), and such archives are
  still verified and rebuilt with them. Any steps give a faithful round trip as long as the same ones are used
  both ways.
- **Range.** The camera codes more finely than 12-bit planes hold (DC alone spans about 4300
  levels over the brightness range), so all three planes are scaled by the archive's colour scale,
  not only the colour planes. Converting to planes and back keeps every level within one step,
  and in practice changes about 1 block in 1000 by one level (`TestV5PlanesRoundTrip`), far below
  what the H.265 archive changes.
- **Coarser quantisation.** `unpack -quant k` codes the planes at the qscale0 whose brightness
  steps come closest to k times the frame's own (from qscale0 3: 1/7 apart, k = 2 gives
  qscale0 10), which scales DC too, as the camera does; archives made with steps 1 cannot.
  `shrink` cannot coarsen high frequencies more than low ones through the steps, so it drops the
  smallest high-frequency levels instead, by a zero bin that widens with frequency (the tilt).
  On the test clip (6 sample frames) that kept more fine brightness detail than raising qscale0
  at the same ratio (97.8% against 89.5% at about 15:1, 91.1% against 84.2% at about 23:1), and
  reaches a ratio close to the one asked, where raising qscale0 jumps (15:1, then 23.5:1, then
  36:1: most levels in the noise are 1, and they all vanish once the step doubles). In
  Blackmagic's colour picture too: frame 0 tilted to 8.1 MB came out at 73.9 dB, re-coded at
  qscale0 9 to 11.3 MB at 69.3 dB. Blackmagic's decoder reads the re-coded frames: against the
  original frame, re-coded at qscale0 9 or 23, tilted, or rebuilt from planes at `-quant 2`, the
  mean colour stays within 0.3% in the shadows and 0.07% elsewhere (65-74 dB).
- **Size.** A 12K picture is wider than NVIDIA's encoders and decoders accept (8192 pixels a
  side; `hevc_nvenc` answers "No capable devices found"). With `-encoder nvenc` (the default),
  the picture is therefore stored as tiles: a 2x2 grid of 6168x4032 (colour 3084x2016), one HEVC
  track per tile in the same `.braw.mkv`, cut by ffmpeg's `crop` from the one picture braw-pack
  sends it and coded by four NVIDIA encoders at once. Unpack, `verify` and `estimate` decode the
  four tracks side by side (on the GPU when there is one) and stitch them back before anything
  else sees the picture; the bundled ffmpeg has no filter that stacks pictures, so the stitching
  is done by braw-pack. The tile edges (6168 and 4032) fall on the clip's own tile edges (792 or
  768 pixels wide, 504 high), so no block of the camera's straddles a seam, and 6168x4032 keeps
  every track within HEVC's largest picture size (level 6.2), which the 12K picture as one
  stream exceeds. The tiles are listed in the restore data (`tiles`), and tiled archives carry
  restore data format 2, which earlier braw-pack versions refuse ("restore data format 2 is not
  supported by this version") instead of misreading; other archives stay at format 1. A normal player shows
  only the first track, the top-left quarter. If the tiled GPU encode fails (an older card, or
  too many encoder sessions in use: four are needed at once), the clip is encoded with x265 on
  the CPU (12-bit) as one track, as before. `-encoder x265` never tiles.
- **Why the GPU keeps less.** Tiling makes the GPU usable, not better. Rounding the planes to
  10 bits is not what loses the detail: with no codec at all, it keeps 98-99% of it. The loss
  comes from NVIDIA's encoder itself: in its predicted (P) frames the error stays at about one
  10-bit code (4 plane values) whatever the quality level, preset or B-frame setting, while this
  camera codes steps of under one plane value. Coding every frame on its own keeps much more
  (95% at quality 4) but makes archives of over a third of the clip. x265's floor is one 12-bit
  value, which is why it keeps more. Two layers (below) get round it.
- **Two layers (`-layers`, two by default for these clips).** With `-encoder nvenc`, `-layers
  auto` (the default, and *Automatic* in the app) stores the clips of an RGBW sensor (version 5)
  in two layers and those of other cameras in one; `-layers 1` keeps one. The scaled 4K mode
  is also supported and tested without tiles; see [braw-v5-scaled.md](braw-v5-scaled.md). The picture
  is stored as a base, the planes rounded to 10 bits and
  coded coarsely (quality 22, 1-3% of the clip: it only has to keep the next layer within range),
  and a detail layer: 512 plus the difference between the source's 12-bit planes and the base as
  decoded, in 12-bit units, coded at the archive's quality level. The encoder's floor of one code
  then costs a quarter as much. HEVC decoding is exact (the GPU's decoder and ffmpeg's own give the
  same samples), so the base unpack decodes is the one the difference was taken from; the picture
  is base x 4 + detail - 512. Each layer has a track per tile, base tracks first (a player shows
  the base's top-left quarter). The base is coded as a tiled picture is today; then each tile's
  detail layer is made by braw-pack (the bundled ffmpeg has no filter that subtracts pictures)
  from the source and that tile's base, decoded on the CPU (about 50 ms a tile-frame with two
  threads), and coded by its own encoder, all four at once. Unpack, `verify`, `estimate` and the
  pack check decode the base tracks on the CPU and the detail tracks on the GPU, and rebuild the
  picture before anything else sees it, so the GPU carries what it does for one layer: one
  decoder of a 6168x4032 tile takes 1.8 GB of the card, and eight at once pushed over 5 GB into
  system memory. Should a tile's encoder fail, the tiles are tried again one at a time, and then
  on x265 as before. The detail layer is coded without B-frames, as the tiles are. The first
  experiments, on single tiles, used B-frames; on the whole picture they need 2.6 GB of the card
  per encoder and more frames ahead in every decoder, and a trial with two tiles at a time pushed
  7.6 GB into system memory before it was stopped. Two-layer archives carry
  restore data format 3, which earlier versions refuse. With `-quality auto` the detail layer's
  level is x265's automatic level plus 12, 16 for this clip. On frames 0-23 of the test clip
  (detail measured on frames 0 and 23 as pack does; sizes against those frames of the clip):

  | archive | size | detail kept |
  |---|---|---|
  | GPU, one layer, quality 10 | 4.3% | 78.7% |
  | x265, quality 10 | 6.1% | 85.0% |
  | x265, quality 4 | 28.8% | 93.1% |
  | GPU, two layers, detail at 22 | 7.7% | 85.4% |
  | GPU, two layers, detail at 19 | 16.0% | 89.7% |
  | GPU, two layers, detail at 16 | 32.3% | 93.0% |
  | GPU, two layers, detail at 13 | 46.8% | 95.5% |

  With the detail layer 12 levels above x265's, two layers keep as much detail as x265, at 1.1-1.3
  times its size. No difference had to be cut to the layer's range at any of these levels. On the
  whole clip (below) `pack -layers 2` took 4 minutes against x265's 14 for the same detail. A
  Pocket Cinema Camera 4K clip gains nothing from two layers (98.9% against 98.8% of the detail,
  in 9% more and twice the time): one layer already keeps nearly all of such coarsely coded clips.
- **Memory (GPU, tiled).** Each tile's encoder is run without B-frames (`-bf 0`): with them,
  NVENC keeps 16 frames per session, 2.6 GB per 6168x4032 tile and over 10 GB for the four, which
  stopped the first test run. Decoding is done by one ffmpeg per tile (one thread on the GPU),
  fed the archive a few frames at a time: ffmpeg queues about 40 decoded frames whenever it
  decodes faster than braw-pack reads them, which NVDEC always does, and that was 3 GB per tile
  (12 GB for four). braw-pack reads where each track's frames end in the Matroska file
  (`videoBlockEnds`) and feeds each decoder at most three frames ahead (`feeder`). The four
  encoders, and later the four decoders, fill an 8 GB card (7.5 GB in use at the peak); when the
  card is full, Windows moves the rest into system memory (2.7 GB during `pack`), on top of what
  braw-pack and ffmpeg use themselves. Other programs using the GPU push more of it out.
- **Memory.** At 12K, ffmpeg and x265 hold about 1.5 GB for every frame in their pipeline, and
  neither refuses frames while busy: fed freely, they grew past 15 GB. For pictures of 8K and
  more, braw-pack therefore runs x265 with its zero-latency tuning (no B-frames, no look-ahead),
  which hands each frame back as soon as it is coded, and keeps the feed at most three frames
  ahead of what ffmpeg reports done. The archives are somewhat larger than with B-frames.

## Results on the test clip

URSA Cine 12K LF, 12288x8040, 98 frames (4 seconds) at 25 fps, recorded at 12:1 (1.13 GB). Measured
on 2026-10-19 with the build that has every part above (steps 2, tiles, automatic quality, two
layers), on a
6-core i7-5820K with 32 GB and an RTX 3060 Ti (8 GB). "Peak" is braw-pack and its ffmpeg processes
together; with the GPU, Windows also moved up to 2.7 GB out of the full graphics card into system
memory. Detail kept is braw-pack's measure of the fine picture detail left in the archive.

| run | time | result | peak memory |
|---|---|---|---|
| `pack` (defaults: GPU, 4 tiles, quality 10 chosen, every frame checked) | 145 s | 50.4 MB (4.4% of the clip), 73.5% of the detail | 12.6 GB |
| `pack -speed slow -encoder nvenc` (what VideoManager passes) | 142 s | 50.5 MB (4.4%), 73.5% | 13.0 GB |
| `pack -encoder x265` (quality 4 chosen, every frame checked) | 14 min | 323.8 MB (28.0%), 91.3% | 14.8 GB |
| `pack -layers 2` (GPU, two layers, detail quality 16 chosen, every frame checked) | 245 s | 364.7 MB (31.5%), 91.1% | 15.2 GB |
| `pack -layers 2 -speed slow -encoder nvenc` | 254 s | 370.4 MB (32.0%), 91.3% | 16.3 GB |
| `pack -layers 2 -spot-check` | 208 s | 364.7 MB (31.5%), 91.1% | 11.3 GB |
| `pack -spot-check` with no usable GPU (CUDA hidden): NVENC fails, x265 takes over at quality 4, one track | 919 s | 323.8 MB (28.0%), 91.3% | 14.8 GB |
| `pack -lut -spot-check` | 408 s | viewing LUT 44.6 dB from Blackmagic's picture on held-out frames | 10.8 GB |
| `estimate` (GPU, levels 4 to 16) | 622 s | level 10: 51.9 MB, 73.5% (the real pack: 50.4 MB, 73.5%); level 4: 13.1%, 76.5% | 13.0 GB |
| `estimate -layers 2` (detail levels 13 to 22) | 656 s | 16: 31.6%, 91.1% (the real pack: 31.5%, 91.1%); 13: 45.5%, 94.4%; 19: 15.5%, 87.0%; 22: 7.5%, 81.6% | 15.1 GB |
| `unpack` of the tiled archive | 65 s | 739.5 MB clip | 9.9 GB |
| `unpack` of the two-layer archive | 111 s | 837.5 MB clip | 13.3 GB |
| `verify` of the two-layer archive | 80 s | every frame's restore data matches, 91.1% | 15.1 GB |
| `unpack -quant 2` | 60 s | 428.3 MB clip | 10.0 GB |
| `unpack` of the x265 archive | 115 s | 853.0 MB clip | 9.8 GB |
| `verify` | 42 s | every frame's restore data matches | 12.1 GB |
| `shrink -ratio 18` | 45 s | 708 MB `.braw` (19.7:1), 93.6% of the detail | 5.8 GB |
| `unpack` of an archive from the first builds (steps 1) | 91 s | byte for byte the clip the first build rebuilt | 9.4 GB |

Blackmagic's decoder (SDK 4.2, through its public API) was given the frames below of each clip
(every frame only for the shrunk clip, at 1/6 scale). Against its decode of the original, as-shot
16-bit picture at 2/3 scale:

| clip | frame 0 | frame 97 |
|---|---|---|
| rebuilt from the tiled GPU archive | 40.8 dB | 38.1 dB |
| the same at `-quant 2` (42% smaller) | 40.6 dB | 38.1 dB |
| rebuilt from the x265 archive at quality 4 (853 MB) | 45.1 dB | 42.9 dB |
| rebuilt from the two-layer GPU archive at quality 16 (837.5 MB) | 44.6 dB | 42.4 dB |
| `shrink` to 19.7:1 (all 98 frames decode) | 48.5 dB | 48.8 dB |
| rebuilt from the first builds' x265 archive (`-speed fast`, quality 10) | 40-42 dB (frames 0, 49, 97) | |

**Tile seams.** At full scale (frames 0 and 49), the error along the horizontal seam is within
the range of the camera's own tile edges. Along the vertical seam it is 12-18% above the inside of
the tiles in a band of about 4 pixels; the same column of a single-stream x265 archive is already
11% above, so the seam adds about 6%. The step in mean level across the seam is within what the
camera's tile edges show. A crop over the crossing, amplified 16 times, shows no line. The
two-layer archive's seams measure the same: along the vertical seam 18-19% above the inside of the
tiles (the camera's tile edges 7-14%), along the horizontal one within the camera's tile edges.

Earlier measurements, from builds before steps 2 and tiles (single-stream x265): `pack -speed fast
-spot-check` 6 minutes, 58.4 MB (5.0%), 79% of the detail; `pack` (balanced, every frame checked)
7 m 45 s, 60.8 MB (5.3%), 79.3%, about 19 GB; `-speed slow` 23 m 48 s, 84.1 MB (7.3%), 80.6%,
about 21 GB; `verify` 1 m 15 s, about 17 GB; `estimate` with x265 did not finish on 32 GB.

Every frame re-codes byte for byte (`TestV5Rewrite`); converting to planes and back keeps every
level within one step (`TestV5PlanesRoundTrip`, `TestV5PlanesRoundTripSteps1`).

The detail kept is lower than on Pocket Cinema Camera 4K clips (about 99% at quality 10) because
this camera codes finer: one coded step is under one 12-bit plane value here, against about three
there. With x265, a lower quality number keeps more, and `-quality auto` chooses 4. With the GPU,
lower levels make larger archives for little more detail (see "Why the GPU keeps less").
