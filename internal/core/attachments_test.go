package core

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func attachmentTestVideo(t *testing.T, ffmpeg, dir string) string {
	t.Helper()
	video := filepath.Join(dir, "video.mkv")
	cmd := command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-f", "rawvideo",
		"-pix_fmt", "yuv420p", "-s", "16x16", "-framerate", "1", "-i", "-", "-frames:v", "1",
		"-c:v", "libx265", "-preset", "ultrafast", "-x265-params", "log-level=error", video)
	cmd.Stdin = bytes.NewReader(make([]byte, 16*16*3/2))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("encode test picture: %v: %s", err, out)
	}
	return video
}

func muxTestAttachments(t *testing.T, ffmpeg, video, archive string, args []string) {
	t.Helper()
	a := append([]string{"-y", "-i", video, "-map", "0:v", "-c", "copy"}, args...)
	if err := runFFmpeg(ffmpeg, append(a, archive)...); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreAttachmentsLegacy(t *testing.T) {
	ffmpeg := CheckTools("")
	if !ffmpeg.X265 {
		t.Skip("no ffmpeg with libx265")
	}
	dir := t.TempDir()
	rd := &restoreData{Meta: Meta{Format: FormatVersion, Width: 16, Height: 8, ChromaScale: 1, FpsNum: 24, FpsDen: 1}, Pre: []byte("original headers")}
	src := filepath.Join(dir, "restore.bin")
	if err := rd.write(src); err != nil {
		t.Fatal(err)
	}
	args, cleanup, err := restoreAttachments(src)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if len(args) != 6 || args[5] != "filename="+AttachmentName {
		t.Fatalf("small archive format changed: %v", args)
	}
	archive := filepath.Join(dir, "legacy.mkv")
	muxTestAttachments(t, ffmpeg.FFmpeg, attachmentTestVideo(t, ffmpeg.FFmpeg, dir), archive, args)
	back, err := dumpRestoreData(ffmpeg.FFmpeg, archive)
	if err != nil {
		t.Fatal(err)
	}
	if !back.equal(rd) {
		t.Fatal("legacy restore data changed")
	}
}

// Reproduce the precise binary-element size rejected in the reported failure.
// No camera footage or GPU is required; sparse input keeps fixture setup cheap.
func TestRestoreAttachmentsLarge(t *testing.T) {
	if testing.Short() {
		t.Skip("large attachment integration test")
	}
	ffmpeg := CheckTools("")
	if !ffmpeg.X265 {
		t.Skip("no ffmpeg with libx265")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "restore.bin")
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(416413416); err != nil {
		f.Close()
		t.Fatal(err)
	}
	// Mark each part differently to detect reordering even for a sparse fixture.
	for i := 0; i < 4; i++ {
		if _, err := f.WriteAt([]byte{byte(i + 1)}, int64(i)*restorePartSize); err != nil {
			f.Close()
			t.Fatal(err)
		}
	}
	f.Close()
	args, cleanup, err := restoreAttachments(src)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if len(args) != 5*6 {
		t.Fatalf("want manifest plus four parts, got %v", args)
	}
	video := attachmentTestVideo(t, ffmpeg.FFmpeg, dir)
	archive := filepath.Join(dir, "large.mkv")
	muxTestAttachments(t, ffmpeg.FFmpeg, video, archive, args)
	first := filepath.Join(dir, "first.bin")
	dumpAttachments(ffmpeg.FFmpeg, archive, []string{first}, 0)
	joined, err := joinRestoreParts(ffmpeg.FFmpeg, archive, first, dir)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := sha256File(src)
	got, err := sha256File(joined)
	if err != nil || got != want {
		t.Fatalf("restore bytes changed: %v", err)
	}
	if _, err := streamHash(ffmpeg.FFmpeg, archive, 1); err != nil {
		t.Fatalf("FFmpeg cannot read final archive: %v", err)
	}
	// An omitted last attachment must never pass restoration.
	missing := filepath.Join(dir, "missing.mkv")
	muxTestAttachments(t, ffmpeg.FFmpeg, video, missing, args[:len(args)-6])
	missingDir := t.TempDir()
	if _, err := joinRestoreParts(ffmpeg.FFmpeg, missing, first, missingDir); err == nil || !strings.Contains(err.Error(), "missing restore attachment") {
		t.Fatalf("missing attachment accepted: %v", err)
	}
	// Reordering complete pieces preserves sizes but must fail the checksum.
	bad := append([]string(nil), args...)
	bad[7], bad[13] = bad[13], bad[7]
	reordered := filepath.Join(dir, "reordered.mkv")
	muxTestAttachments(t, ffmpeg.FFmpeg, video, reordered, bad)
	if _, err := joinRestoreParts(ffmpeg.FFmpeg, reordered, first, t.TempDir()); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("reordered attachments accepted: %v", err)
	}
}

func TestRestoreManifestRejected(t *testing.T) {
	for _, m := range []restoreParts{
		{Parts: 0}, {Parts: maxRestoreParts + 1},
		{Parts: 2, Size: restorePartSize},
		{Parts: 2, Size: 2*restorePartSize + 1},
		{Parts: 2, Size: restorePartSize + 1, SHA256: "invalid"},
	} {
		dir := t.TempDir()
		first := filepath.Join(dir, "manifest.bin")
		b, _ := json.Marshal(m)
		if err := os.WriteFile(first, append([]byte(restorePartsMagic), b...), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := joinRestoreParts("must-not-run", "unused", first, dir); err == nil {
			t.Fatalf("invalid manifest accepted: %+v", m)
		}
	}
}
