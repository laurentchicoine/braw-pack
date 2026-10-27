package core

import (
	"bytes"
	"os"
	"testing"
)

// TestV5PyxisCameraRewrite needs a Pyxis 6K clip (layout 0x0804) in BRAW_PYXIS_CLIP. Each tested
// frame is decoded to coefficient levels and coded again, which must give the camera's bytes
// back: the stream splits, end-of-block rule, side byte, tile geometry and padding are all
// exercised. Set BRAW_PYXIS_ALL=1 to test every frame instead of the first, middle and last.
func TestV5PyxisCameraRewrite(t *testing.T) {
	path := os.Getenv("BRAW_PYXIS_CLIP")
	if path == "" {
		t.Skip("BRAW_PYXIS_CLIP not set")
	}
	c, err := OpenClip(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	frames := []int{0, c.Frames() / 2, c.Frames() - 1}
	if os.Getenv("BRAW_PYXIS_ALL") == "1" {
		frames = make([]int, c.Frames())
		for i := range frames {
			frames[i] = i
		}
	}
	for _, fn := range frames {
		fr, err := c.frame(fn)
		if err != nil {
			t.Fatal(err)
		}
		fi, err := parseFrame(fr)
		if err != nil {
			t.Fatal(err)
		}
		if !fi.v5Full8 || fi.scaledStreams() != 4 {
			t.Fatal("sample is not a Pyxis 6K (layout 0x0804) recording")
		}
		out, err := v5Rewrite(fr, fi)
		if err != nil {
			t.Fatalf("frame %d: %v", fn, err)
		}
		if !bytes.Equal(fr, out) {
			n := min(len(fr), len(out))
			i := 0
			for i < n && fr[i] == out[i] {
				i++
			}
			t.Fatalf("frame %d did not rewrite byte for byte (lengths %d/%d, first difference at %#x)", fn, len(fr), len(out), i)
		}
	}
	t.Logf("%d camera frames rewrite byte for byte", len(frames))
}

// TestV5PyxisScale checks the Pyxis 6K steps: the URSA modes' AC law, with its own DC limit
// factor (v5Full8DCCap), which binds for colour from qscale0 11 on with the camera's limits.
func TestV5PyxisScale(t *testing.T) {
	fi := &frameInfo{v5Full8: true, qscale1: 1280, qscale2: 256}
	ya, yd, ca, cd := fi.scaledScale(5)
	if ya != 450 || yd != 450 || ca != 250 || cd != 250 {
		t.Fatalf("qscale0 5: %v %v %v %v", ya, yd, ca, cd)
	}
	_, _, ca, cd = fi.scaledScale(11)
	if ca != 400 || cd != 256*53.0/35 {
		t.Fatalf("qscale0 11: colour %v, DC %v", ca, cd)
	}
	ursa := &frameInfo{qscale1: 1280, qscale2: 256}
	if _, _, _, cd := ursa.scaledScale(11); cd != 256*10.0/7 {
		t.Fatalf("URSA DC limit changed: %v", cd)
	}
	if !(&frameInfo{version: 5}).rgbw() || (&frameInfo{version: 5, v5Full8: true}).rgbw() {
		t.Fatal("rgbw() must be true for URSA version 5 and false for the Pyxis layout")
	}
}
