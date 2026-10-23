package core

import (
	"bytes"
	"os"
	"testing"
)

// The first 4K archives used the approximate DC model. A newer transform model
// must still reproduce their existing rebuilt BRAW, rather than reinterpret it.
func TestScaledV5ArchiveSteps2Compatibility(t *testing.T) {
	archive, rebuilt := os.Getenv("BRAW_STEPS2_ARCHIVE"), os.Getenv("BRAW_STEPS2_REBUILT")
	if archive == "" || rebuilt == "" {
		t.Skip("legacy archive fixture not set")
	}
	ffmpeg := FindFFmpeg()
	rd, err := dumpRestoreData(ffmpeg, archive)
	if err != nil {
		t.Fatal(err)
	}
	if rd.Meta.V5Steps != 2 {
		t.Fatal("fixture is not a steps 2 archive")
	}
	clip, err := OpenClip(rebuilt)
	if err != nil {
		t.Fatal(err)
	}
	defer clip.Close()
	for _, frame := range []int{0, clip.Frames() / 2, clip.Frames() - 1} {
		buf, layout, ok, err := archiveFrame(ffmpeg, archive, &rd.Meta, frame)
		if err != nil || !ok {
			t.Fatalf("frame %d: %v", frame, err)
		}
		y, u, v := unpackYUV(buf, rd.Meta.Width, rd.Meta.Height, layout)
		got, err := encodePlanesSteps(rd.Hdrs[frame], rd.Side[frame], y, u, v, rd.Meta.ChromaScale, 0, 2)
		if err != nil {
			t.Fatal(err)
		}
		want, err := clip.frame(frame)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("legacy frame %d changed", frame)
		}
	}
}
