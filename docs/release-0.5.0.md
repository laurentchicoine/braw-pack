# braw-pack 0.5.0

## Clips with dropped frames are repaired

A camera that cannot write fast enough, for example a Pocket 4K recording to a
slow SD card, sometimes skips frames. It stores nothing for them and holds the
frame before the gap for the missing time instead. Blackmagic RAW Player and
DaVinci Resolve then report the frames in the gap as unreadable.

When such a clip is compressed, as an archive or as a smaller BRAW, braw-pack
now gives every frame its normal duration and fills each gap with copies of the
frame before it. The result has a readable frame at every point and stays in
step with sound and motion data. The source file is never changed.

- The app shows a warning on each repaired clip when it is added and after it is
  compressed, and names the repaired clips when the batch ends.
- The command line prints a `WARNING:` line for each repaired clip and a list at
  the end of the batch. A repaired clip still counts as a success (exit code
  unchanged).
- 23.976 fps clips are handled, including rounding of the last frame.
- Archives of repaired clips record where the source's chunk data ended, since
  their index is longer than the source's.

## Apple hardware encoder on Apple Silicon (macOS beta)

Apple Silicon Macs now use Apple's hardware HEVC encoder (VideoToolbox,
`-encoder videotoolbox`, "Apple GPU" in the app) by default. Intel Macs keep
x265. Archives are 10-bit and tiled by the same rule as NVIDIA's encoder, so an
archive made on either kind of hardware decodes on the other.

- `auto` quality can go as low as with x265. One layer is used by default, RGBW
  included; two layers only when a short trial keeps under 90% of the detail
  in one.
- Verify and unpack decode 10-bit archives with the Mac's hardware decoder.
- ffmpeg's memory stays under 5 GB on 12K and 17K clips (it passed 24 GB at 17K
  without a macOS allocator setting now applied).
- On an M1 Max, a 6K clip packs in about 20 s, a 12K clip in about 5 minutes and
  a 17K clip in under 12 minutes.
- During development the macOS hardware encoder driver crashed the system twice,
  only on synthetic input braw-pack never produces. See
  [the Mac beta instructions](macos-beta.md).

## All platforms

- Decoding scaled RGBW frames on the CPU is faster (zero coefficients and unused
  rows are skipped). Output is bit-identical.

## Compatibility

Archives made by 0.4.0 and earlier are read as before. Archives of repaired
clips record extra information; use braw-pack 0.5.0 or newer to restore them. Keep original footage until rebuilt output has been checked.

## Publication

Merge the release changes into main before tagging v0.5.0. Build packages from
the merged commit, verify required checks, and publish matching Windows, Linux
and macOS downloads with checksums and corresponding FFmpeg/x265 sources. The
macOS ffmpeg must be rebuilt with VideoToolbox. Keep macOS downloads clearly
labeled beta.
