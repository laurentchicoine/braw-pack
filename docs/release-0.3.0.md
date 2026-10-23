# braw-pack 0.3.0

## Large-file packing fix

Large clips could finish encoding but fail archive validation because FFmpeg
wrote restore attachments larger than its 256 MiB reader limit. Restore data
larger than 128 MiB is now split into attachments of at most 128 MiB, with a
manifest recording the total size, part count and SHA-256 checksum. Reading
checks every part's size and the combined checksum before accepting the data.

Existing single-attachment archives remain supported. New multipart archives
require braw-pack 0.3.0 or newer for restoration, verification and metadata
inspection. Keep original footage until the rebuilt output has been checked.

## macOS beta

Apple Silicon and Intel macOS downloads remain **beta**, targeting macOS 12 or
newer. Manual interactive Mac testing of this release is pending. Builds use
CPU encoding and ad-hoc signing without Apple notarization. See
[the Mac beta instructions](macos-beta.md) for setup and validation scope.

## Publication

Merge the release PR into main before tagging v0.3.0. Build packages from the
merged release commit, verify required checks, and publish matching Windows,
Linux and macOS downloads with checksums and corresponding FFmpeg/x265 sources.
Record the actual build and validation results in the published release notes;
keep macOS downloads clearly labeled beta.
