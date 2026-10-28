# braw-pack 0.4.0

## Pyxis 6K and Cinema Camera 6K

Blackmagic's 6K full-frame Bayer cameras (Pyxis 6K and Cinema Camera 6K) record
Blackmagic RAW version 5 in a layout of their own (`0x0804`), which earlier
versions refused. braw-pack now packs, rebuilds, verifies and estimates these
clips, including 3:1 and 8:1 ratios, 17:9 and anamorphic picture sizes, and
4K/4.8K/6K recording modes. Every frame of the five sample clips (2544 frames)
decodes and re-codes byte for byte, and the layout was checked against
Blackmagic's own decoder. Details are in [braw-v5-pyxis.md](braw-v5-pyxis.md).

- Smaller-BRAW (`-quant`) rebuilds of these clips keep their colour, using the
  limits measured for these cameras.
- Automatic layer selection no longer assumes RGBW for version 5 files. For
  clips other than URSA Cine RGBW, `-layers auto` first codes three stretches of
  eight frames in one GPU layer. If that keeps under 90% of the fine detail,
  the whole clip is packed in two layers. Estimate uses the same trial, and the
  messages say which way it went. The "finer than the GPU" hint now goes by the
  detail actually kept.
- Very large single-picture 6K tracks are coded without B-frames, which fixes a
  stall before the first frame.

## Name suffix for smaller BRAW clips

Smaller clips used to keep their source names, so they could only be written to
another folder. An optional suffix (for example `_12to1`) is now added before
`.braw`: use `-suffix` on the shrink command, or the new field in the GUI's
Smaller BRAW settings. With a suffix, clips can be saved beside the originals
(shrink then needs no `-o`), and the CLI skips clips whose name already ends in
the suffix. Archives are unchanged.

## Documentation

- First full-length URSA Cine 17K pack recorded in [braw-v5-17k.md](braw-v5-17k.md).

## macOS beta

Apple Silicon and Intel macOS downloads remain **beta**, targeting macOS 12 or
newer. Manual interactive Mac testing of this release is pending. Builds use CPU
encoding and ad-hoc signing without Apple notarization. See
[the Mac beta instructions](macos-beta.md).

## Compatibility

Archives made by 0.3.0 and earlier are read as before. Archives of Pyxis 6K and
Cinema Camera 6K clips are new in this release; keep braw-pack 0.4.0 or newer on
every machine that needs to restore them. Keep original footage until rebuilt
output has been checked.

## Publication

Merge the release changes into main before tagging v0.4.0. Build packages from
the merged commit, verify required checks, and publish matching Windows, Linux
and macOS downloads with checksums and corresponding FFmpeg/x265 sources.
Record the actual build and validation results in the published release notes;
keep macOS downloads clearly labeled beta.
