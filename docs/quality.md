# Choosing the quality level

`pack -quality auto` (the default, and *Choose for each clip* in the app) picks the archive's
quality level from how finely the camera quantised the clip. This page gives the rule and the
measurements behind it. The code is in `internal/core/quality.go`.

## The rule

**Camera step.** For each clip braw-pack measures the step of the camera's own quantiser: how much
one coded level of a low-frequency coefficient changes braw-pack's 12-bit brightness plane (root
mean square over the block). It takes the geometric mean over the coefficients below a quarter of
the sampling rate, without DC: those are the frequencies "detail kept" looks at. For versions 1
and 2 it comes from the frame's quantisation table and `qscale` fields. For version 5 (URSA Cine
12K) it comes from braw-pack's own steps (`v5Steps`) times the colour scale, which in version 5
scales brightness too. It is the median over five frames, since cameras recording at a constant
bitrate change their quantisation from frame to frame.

**Level.**

- With x265: `10 + 6 × log2(step / 2.5)`, rounded, kept between 4 and 10. The encoder's step
  doubles every 6 levels, so below a camera step of 2.5 the two stay in proportion.
- With NVENC: always 10.
- With NVENC in two layers (`-layers 2`): the level is the detail layer's, x265's level plus 12
  (16 to 22). At those levels two layers kept about as much detail as x265 at its level (on 24
  frames of the 12K clip: 93.0% at 16 against x265's 93.1% at 4, 85.4% at 22 against 85.0% at
  10), at 1.1-1.3 times its size; the base is always coded at 22 (see docs/braw-v5.md).
- If NVENC fails on a clip and x265 encodes it instead, the level is chosen again for x265.
  (12K pictures, too wide for the GPU as one stream, are stored as tiles and stay on the GPU.)

| camera step | example | x265 | NVENC | NVENC, two layers |
|---|---|---|---|---|
| 2.5 or more | Pocket Cinema Camera 4K and 6K at constant quality (4.23), Pocket 4K clips measured (2.54–5.31) | 10 | 10 | 22 |
| 2.32 | Pocket 6K at 8:1, a detailed scene | 9 | 10 | 21 |
| 1.29–1.55 | Pocket 6K at 8:1 | 4–6 | 10 | 16–18 |
| 0.83–0.89 | URSA Cine 12K LF at 12:1 | 4 | 10 (stored as tiles) | 16 (as tiles) |

`estimate` prints the camera step and the level `auto` chooses, includes that level in the table
and marks it with `*`. Archives record both (`quality_auto`, `source_step`).

## Why

A fixed level kept 98–99% of the fine detail of Pocket Cinema Camera 4K clips but only about 70%
of the URSA Cine 12K test clip. The 12K camera codes one level as 0.83 plane values, five times
finer than a Pocket 4K at constant quality (4.23). The archive's encoder then rounds away detail
the camera coded as one or two levels.

Among the Pocket 4K and 6K clips, the step does not predict detail kept. The picture's content
does: busy, high-contrast scenes keep more. Every one of those camera originals keeps, at level
10, within 0.6 points of the most NVENC gets from it at any level. Lower levels only make larger files.
That is why level 10 stays the level for those cameras, and why the rule only lowers it for
finer steps.

**NVENC** stores 10 bits. For finely coded clips detail stops growing below about level 10, and
goes down again below about 6. The cause is not the rounding to 10 bits itself (that alone keeps
98-99% of the 12K clip's detail) but the encoder's predicted frames, whose error stays at about
one 10-bit code whatever the level (see docs/braw-v5.md):

- On the 12K clip NVENC never passes 58% on the detailed crop below; on the whole frame, stored as
  tiles, `estimate` gives 73.5% at level 10 and at most 76.5% at any level.
- On the finest Pocket 6K clip it never passes 95.4%.

x265 keeps 12 bits and keeps more of such clips, so the rule lowers the level only for x265, and
for the detail layer of two-layer archives, which is coded in 12-bit units.

**The 12K level (4)** was the user's choice between archive size and fine detail, from the
measurements below. At 4K scale, level 10 already keeps as much of the 12K clip (98.6%) as a Pocket
4K clip keeps. What it loses is detail finer than a 4K picture shows, which matters when the
picture is cropped or reframed. Level 4 keeps 98% at 8K scale for about a quarter of the clip's
size.

## Measurements

All with `-speed balanced` unless noted. "Detail kept" is the measure `pack` and `estimate`
report: the archive's error against the source's fine detail, both on 2x2-pixel averages, the
detail being each average's difference from its 8x8 neighbourhood. Archive sizes are the whole
archive as a share of the clip.

### Clips

| clip | camera, picture | recorded at | camera step | note |
|---|---|---|---|---|
| A | Pocket 4K, 2720x1528 | 11.1:1 | 2.54 | flat table |
| B | Pocket 4K, 2720x1528 | 14.8:1 | 3.35 | flat table |
| C | Pocket 4K, 3872x2176 | 18.8:1 | 4.23 | constant-quality table |
| D | Pocket 4K, 2912x1528 | 6.6:1 | 4.23 | same table as C, a much busier scene |
| E | Pocket 4K, 3872x2176 | 22.5:1 | 5.31 | table of C × 1.25 |
| C′ | C re-saved by `braw-pack shrink` | 25:1 | 15.6 | not a camera original |
| F′ | a 2.6K clip re-saved by `shrink` | 25:1 | 35 | not a camera original |
| G | Pocket 6K, 5776x3040 | 8.0:1 | 1.29 | constant bitrate, step changes per frame |
| H | Pocket 6K, 5776x3040 | 8.0:1 | 2.32 | constant bitrate |
| I | Pocket 6K, 5776x3040 | 12.4:1 | 4.23 | constant quality |
| J | Pocket 6K, 5776x3040 | 8.0:1 | 1.55 | constant bitrate, a busy scene |
| K | URSA Cine 12K LF, 12336x8064 planes | 12:1 | 0.83 | version 5, colour scale 0.828 with the first steps; with steps 2 (which follow the camera's per-frame qscale0) 0.891 and step 0.89, same level |

C′ and F′ were the `X:\temp` copies of `A250_10101839_C002` and `A250_10102104_C129` used in
earlier tests. They are `shrink` outputs with steeply tilted tables, not camera originals. The
camera original of C002 is clip C.

### NVENC, detail kept (`estimate`, 96 frames)

| clip | step | 4 | 6 | 8 | **10** | 12 | 16 | archive at 10 |
|---|---|---|---|---|---|---|---|---|
| A | 2.54 | 98.9 | 99.0 | 99.0 | **98.9** | 98.6 | 98.0 | 10.9% |
| B | 3.35 | 98.4 | 98.5 | 98.6 | **98.5** | 98.1 | 97.3 | 13.8% |
| C | 4.23 | 99.0 | 99.1 | 99.0 | **98.8** | 98.5 | 98.0 | 22.2% |
| D | 4.23 | 99.8 | 99.8 | 99.8 | **99.8** | 99.7 | 99.5 | 24.7% |
| E | 5.31 | 97.6 | 97.9 | 98.0 | **97.6** | 97.0 | 96.1 | 22.4% |
| C′ | 15.6 | 98.9 | 99.0 | 98.9 | **98.2** | 97.5 | 96.5 | 44.4% |
| F′ | 35 | 99.7 | 99.6 | 99.5 | **99.2** | 98.7 | 96.7 | 54.7% |
| G | 1.29 | 94.5 | 95.2 | 95.4 | **95.2** | 94.7 | 93.6 | 4.0% |
| H | 2.32 | 97.0 | 97.5 | 97.5 | **96.9** | 96.4 | 95.3 | 14.4% |
| I | 4.23 | 99.3 | 99.4 | 99.4 | **99.3** | 99.2 | 98.9 | 21.2% |
| J | 1.55 | 99.9 | 99.9 | 99.9 | **99.8** | 99.8 | 99.6 | 24.4% |

Levels 0 and 2 keep less than 4 to 8 on most clips. With `-speed slow`, clip C gives the same
detail (99.0, 99.1, 99.0, 98.8, 98.5, 98.0) in archives about 1% smaller.

### x265, detail kept (`estimate`, 96 frames)

| clip | step | 2 | 4 | 6 | 8 | **10** | 12 | 14 | archive at 10 |
|---|---|---|---|---|---|---|---|---|---|
| A | 2.54 | 99.8 | 99.7 | 99.6 | 99.4 | **99.2** | 98.9 | 98.6 | 12.6% |
| B | 3.35 | 99.8 | 99.7 | 99.6 | 99.3 | **99.0** | 98.6 | 98.1 | 16.9% |
| C | 4.23 | 99.9 | 99.8 | 99.7 | 99.6 | **99.3** | 99.0 | 98.7 | 27.1% |
| D | 4.23 | 100.0 | 100.0 | 99.9 | 99.9 | **99.9** | 99.8 | 99.7 | 31.2% |
| E | 5.31 | 99.8 | 99.7 | 99.4 | 99.2 | **98.7** | 97.9 | 97.2 | 30.0% |
| F′ | 35 | 99.9 | 99.9 | 99.9 | 99.8 | **99.6** | 99.4 | 99.0 | 54.4% |
| G | 1.29 | | 98.3 | 97.8 | 97.2 | **96.6** | 96.0 | | 4.7% |
| H | 2.32 | | 99.6 | 99.2 | 98.7 | **98.0** | 97.3 | | 22.0% |

x265 keeps more than NVENC at every level, and keeps gaining slowly below 10, at about 30% more
archive per two levels. On the finely coded Pocket 6K clips it is the only encoder that gets close
to 98%. Clip G, where NVENC stops at 95.4%, keeps 98.3% at level 4 (its automatic level), in an
archive of 17.9% of the clip instead of 4.7% at 10.

### URSA Cine 12K, x265

A 12K `estimate` or full check needs more memory than a 32 GB machine has, so the clip was
measured with `braw-lab crop`. It encodes a 4096x2304 crop from the middle of the planes
(24 frames in two stretches) with the settings `pack` uses for the whole picture
(`-tune zerolatency` at 8K and above). It then measures detail kept at the usual scale and at
two coarser ones. 4x4 and 6x6 averages at 12K cover what 2x2 covers at 8K and at 4K. Archive
sizes are against the clip's data for the same area.

| level | archive | detail kept at 12K scale (2x2) | 8K scale (4x4) | 4K scale (6x6) |
|---|---|---|---|---|
| 0 | 51.6% | 91.2 | 99.0 | 99.8 |
| 2 | 37.4% | 88.3 | | |
| **4** | **25.9%** | **83.6** | **98.0** | **99.5** |
| 6 | 14.9% | 77.2 | | |
| 8 | 8.6% | 72.1 | 95.8 | 98.9 |
| 10 | 4.7% | 68.2 | 94.8 | 98.6 |
| 12 | 2.4% | 64.8 | 93.7 | 98.2 |

Other settings on the same crop:

- **Every frame on its own** (`-intra`): level 12 gives 9.4%, keeping 75.9 / 96.6 / 99.2. Level
  8 gives 27.3%, keeping 86.8 / 98.5 / 99.6. That is a little more than the default mode at the
  same size.
- **x265 without zerolatency** (as below 8K): level 6 gives 11.9% and 74.4%, level 10 gives 3.7%
  and 65.5%. That is the same trade between size and detail.
- **NVENC** (the crop is small enough for the GPU): 57.6, 58.3 and 55.4% at levels 4, 8 and 12,
  and 51.6% at 0.

**Full-frame check.** One real `pack -spot-check` of the whole clip with the defaults (`-encoder nvenc`). The GPU refused the 12K picture, x265 took over, and the level was chosen again for it (4). It made an archive of 24.9% of the clip (288 MB from 1.13 GB) in 17 minutes. That matches the crop (25.9%). The two sample frames keep 90.6% of the fine detail, more than the crop's 83.6%: the whole frame holds plainer areas than its middle. ffmpeg and braw-pack together left as little as 3.5 GB available on the 32 GB machine.

## Limits

- Only one 12K clip, one scene. Pocket 4K and 6K clips are from the same owner's football
  recordings.
- In version 5 clips the step comes from braw-pack's steps (`v5Steps`), so it follows them if
  they change (a new `v5StepsVersion`). Steps that follow the frame's `qscale0`, as Blackmagic's
  do, would give the test clip 0.83 on its frames at `qscale0` 3 and 0.71 on those at 2. Both are
  below the floor, so the level stays 4.
- "Detail kept" counts some sensor noise as detail, more at high resolutions where 2x2 pixels
  are a tiny area. Part of what the 12K clip loses at 2x2 is noise.
- The rule never goes above 10. Clips coded more coarsely than a Pocket 4K, such as C′ and F′,
  could take higher levels, but the shrunk copies already lose 0.5 to 1 point at 10, so it was
  left alone.
