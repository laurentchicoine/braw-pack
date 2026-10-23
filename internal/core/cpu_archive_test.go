package core

import (
	"bytes"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestCPUArchiveSamples exercises the 12-bit path used on Macs, including values
// that cannot survive conversion to ten bits. It requires no camera footage.
func TestCPUArchiveSamples(t *testing.T) {
	tl := CheckTools("")
	if !tl.X265 {
		t.Skip("no ffmpeg with libx265")
	}
	const w, h, n = 128, 64, 3
	rng := rand.New(rand.NewSource(73))
	var raw []byte
	for i := 0; i < n; i++ {
		y, u, v := randomPlanes(rng, w, h)
		raw = append(raw, packYUV(y, u, v, planar12)...)
	}
	for _, tiled := range []bool{false, true} {
		t.Run("tiled="+strconv.FormatBool(tiled), func(t *testing.T) {
			mkv := filepath.Join(t.TempDir(), "cpu.mkv")
			m := &Meta{Width: w, Height: h, Frames: n, Bits: 12, Encoder: "x265", FpsNum: 24, FpsDen: 1}
			args := []string{"-v", "error", "-y", "-f", "rawvideo", "-pix_fmt", "yuv420p12le",
				"-s", "128x64", "-framerate", "24", "-i", "-"}
			if tiled {
				m.setTiles([]Tile{{0, 0, 64, 64}, {64, 0, 64, 64}})
				for k, tile := range m.Tiles {
					args = append(args, "-map", "0:v", "-filter:v:"+strconv.Itoa(k), cropFilter(tile))
				}
			}
			cmd := command(tl.FFmpeg, append(args, "-c:v", "libx265", "-preset", "ultrafast",
				"-x265-params", "lossless=1:log-level=error", "-pix_fmt", "yuv420p12le", mkv)...)
			cmd.Stdin = bytes.NewReader(raw)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("encode: %v: %s", err, out)
			}
			var got []byte
			if err := decodeArchive(tl.FFmpeg, mkv, m, func(i int, buf []byte, lay pixLayout) error {
				if lay != planar12 {
					t.Errorf("frame %d: layout %v, want planar12", i, lay)
				}
				got = append(got, buf...)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, raw) {
				if len(got) != len(raw) {
					t.Fatalf("CPU archive length %d, want %d", len(got), len(raw))
				}
				for i := range got {
					if got[i] != raw[i] {
						t.Fatalf("CPU archive changed 12-bit samples at byte %d: got %v, want %v (lengths %d/%d)", i, got[i:i+min(8, len(got)-i)], raw[i:i+min(8, len(raw)-i)], len(got), len(raw))
					}
				}
				t.Fatalf("CPU archive length %d, want %d", len(got), len(raw))
			}
		})
	}
}

// TestCPUCameraSamples checks the actual Mac encoding/rebuilding path on a short
// sample of local camera footage. The footage is never uploaded to CI.
func TestCPUCameraSamples(t *testing.T) {
	path := os.Getenv("BRAW_CPU_TEST_CLIP")
	if path == "" {
		t.Skip("BRAW_CPU_TEST_CLIP not set")
	}
	c, err := OpenClip(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	frames := sampleFrames(c.Frames(), 3)
	po := PackOptions{Encoder: "x265", Speed: "fast", Progress: func(string, int, int) {}}
	if err := po.defaults(); err != nil {
		t.Fatal(err)
	}
	w, h, dev, err := colourRange(c, frames, &po)
	if err != nil {
		t.Fatal(err)
	}
	cs := chooseChromaScale(dev)
	po.Quality = po.autoQuality(clipStep(c, cs))
	num, den, _, err := c.SensorRate()
	if err != nil {
		t.Fatal(err)
	}
	rd := &restoreData{Meta: Meta{Frames: len(frames), Width: w, Height: h, ChromaScale: cs,
		FpsNum: num, FpsDen: den, Encoder: "x265", Speed: po.Speed, Quality: po.Quality, Bits: 12, V5Steps: v5StepsVersion}}
	mkv := filepath.Join(t.TempDir(), "camera.mkv")
	if _, err := encodePicture(c, frames, &po, rd, mkv); err != nil {
		t.Fatal(err)
	}
	v, err := checkPicture(nil, c, frames, mkv, rd, po.FFmpeg, po.Progress, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rd.Hdrs) != len(frames) || len(rd.Side) != len(frames) || rd.Meta.Bits != 12 {
		t.Fatal("incomplete CPU restore data")
	}
	if (w > hevcMaxSide || h > hevcMaxSide) && len(rd.Meta.Tiles) == 0 {
		t.Fatal("oversized HEVC picture was not tiled")
	}
	for i := range frames {
		buf, lay, ok, err := archiveFrame(po.FFmpeg, mkv, &rd.Meta, i)
		if err != nil || !ok {
			t.Fatalf("frame %d: present=%v: %v", i, ok, err)
		}
		y, u, v := unpackYUV(buf, w, h, lay)
		fr, err := encodePlanesSteps(rd.Hdrs[i], rd.Side[i], y, u, v, cs, 0, v5StepsVersion)
		if err != nil {
			t.Fatal(err)
		}
		again, err := requantFrame(fr, 1, 0)
		if err != nil || !bytes.Equal(fr, again) {
			t.Fatalf("rebuilt frame %d is not canonical: %v", i, err)
		}
	}
	t.Logf("%dx%d frames %v: x265 quality %d, %.3f dB, %.3f%% detail, canonical BRAW rebuilds",
		w, h, frames, po.Quality, v.MeanPSNR, v.DetailKept)
}
