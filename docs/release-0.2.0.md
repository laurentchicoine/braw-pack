# braw-pack 0.2.0 (unpublished)

This release covers the full branch, including RGBW sensor cameras and new platform builds.
The branch currently exists locally as `macos-beta`. The earlier Mac-only prerelease was withdrawn.

## Release notes

- Support URSA Cine RGBW Blackmagic RAW version 5 recordings: native 12K and 17K, plus scaled
  4K and 8K coding layouts. Archive/rebuild, estimates and smaller-BRAW compression support these
  layouts; unknown layouts are refused. The validation scope is documented in the camera reports.
- Add tiled and two-layer archives, automatic layer/quality choices, bounded GPU processing and
  improved CLI/GUI reporting. Oversized 17K CPU archives use 12-bit tiles to respect HEVC width limits.
- Add native Apple Silicon and Intel macOS app/CLI packages, targeting macOS 12 or newer,
  with bundled ffmpeg and CPU encoding. Builds are ad-hoc signed, without Apple notarization.
- Include Linux packaging and documented WSL validation, plus LUT helper updates on Windows.

Mac native test/race/vet, package and startup checks passed for the earlier 0.1.0 development
binaries. Windows/Linux checks and sampled 4K/8K/12K/17K CPU camera checks also passed.
Version 0.2.0 packages must be rebuilt and checked before release; do not rename the old ZIPs
and treat them as 0.2.0 builds. Interactive Mac workflows and actual Monterey execution remain
for the beta tester. Keep original footage.

## Commit dates at publication

From a clean tracked working tree on `macos-beta`, preview the publication:

```powershell
python build/publish-branch.py
```

When ready to publish the branch:

```powershell
python build/publish-branch.py --publish
```

The command captures the current UTC time, rebuilds every commit in `main..macos-beta` with
that same author and committer timestamp, preserves authors, commit messages, file trees and
parent relationships, and pushes the branch. Existing `main` history is unchanged. Commit hashes
change, so older validation hashes in reports remain historical references. A local backup ref
preserves the previous tip before rewriting. Untracked research and nested worktrees are left alone.

Publication requires the remote branch to be absent. The push uses an explicit absent-branch
lease so it cannot overwrite a branch someone creates concurrently. If the push fails, the local
rewritten commits and backup remain; retrying later sets a new publication time.

GitHub records its own upload time separately; this command sets both Git commit date fields
to the time the publication command starts. It does not backdate shared `main` history or alter
the historical GitHub Actions runs.

## Build and release after publication

1. Let the native Mac Actions workflow finish on the published branch, then retrieve and verify
   the new `braw-pack-0.2.0-macos-arm64.zip` and `braw-pack-0.2.0-macos-amd64.zip` plus checksums.
2. Rebuild/test the Windows and Linux packages for 0.2.0 as well, since this is a full feature release.
3. Create a prerelease tagged `v0.2.0-beta.1` from the tested branch commit, or `v0.2.0` when ready
   for the final release. Attach the new platform packages, SHA-256 manifests, matching FFmpeg/x265
   source archives and tester instructions. Use the feature notes above and the actual validation
   results, including remaining beta limitations.

The publication helper pushes the branch only. It does not create a tag or a GitHub release.
