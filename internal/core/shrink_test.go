package core

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestShrink needs a real clip (BRAW_TEST_CLIP). It shrinks it to the first ratio on offer and
// checks the written clip.
func TestShrink(t *testing.T) {
	src := os.Getenv("BRAW_TEST_CLIP")
	if src == "" {
		t.Skip("BRAW_TEST_CLIP not set")
	}
	plan, err := ShrinkEstimate(nil, src, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Options) == 0 {
		t.Fatalf("no ratio on offer for a clip at %.1f:1", plan.SourceRatio)
	}
	for i, o := range plan.Options {
		if o.Target < plan.SourceRatio*ShrinkMargin || o.Ratio < o.Target {
			t.Errorf("option %+v does not reach its target", o)
		}
		if i > 0 && o.Bytes >= plan.Options[i-1].Bytes {
			t.Errorf("%g:1 is not smaller than %g:1", o.Target, plan.Options[i-1].Target)
		}
	}
	want := plan.Options[0]
	dst := filepath.Join(t.TempDir(), "out.braw")
	res, err := Shrink(ShrinkOptions{Src: src, Dst: dst, Ratio: want.Target})
	if err != nil {
		t.Fatal(err)
	}
	if res.Ratio < want.Target*0.93 || res.Ratio > want.Target*1.07 {
		t.Errorf("asked for %g:1, wrote %.2f:1", want.Target, res.Ratio)
	}
	a, err := OpenClip(src)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := OpenClip(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if a.Frames() != b.Frames() {
		t.Fatalf("frames %d -> %d", a.Frames(), b.Frames())
	}
	for _, i := range sampleFrames(a.Frames(), 4) {
		fa, _ := a.frame(i)
		fb, _ := b.frame(i)
		if len(fb) >= len(fa) {
			t.Errorf("frame %d did not get smaller", i)
		}
		_, ya, _, _, _, _, err := decodePlanes(fa, 1, planesLuma)
		if err != nil {
			t.Fatal(err)
		}
		_, yb, _, _, _, _, err := decodePlanes(fb, 1, planesLuma)
		if err != nil {
			t.Fatalf("frame %d of the shrunk clip: %v", i, err)
		}
		if p := psnr12(ya, yb); p < minPlanePSNR {
			t.Errorf("frame %d: %.1f dB from the source", i, p)
		}
		// An untilted requantisation must leave a frame as it is.
		same, err := requantTilt(fa, 1, 0, 0)
		if err != nil || !bytes.Equal(same, fa[:len(same)]) {
			t.Errorf("frame %d changes at tilt 0", i)
		}
	}

	// Refusals: existing destination, a ratio the clip already has, an unreachable ratio.
	if _, err := Shrink(ShrinkOptions{Src: src, Dst: dst, Ratio: want.Target}); err == nil {
		t.Error("overwrote an existing clip")
	}
	// Replacing is allowed when asked for, but never onto the source itself.
	b.Close() // an open file cannot be replaced on Windows
	if _, err := Shrink(ShrinkOptions{Src: src, Dst: dst, Ratio: want.Target, Replace: true}); err != nil {
		t.Errorf("replace: %v", err)
	}
	before, _ := os.Stat(src)
	if _, err := Shrink(ShrinkOptions{Src: src, Dst: src, Ratio: want.Target, Replace: true}); err == nil {
		t.Fatal("accepted the source as its own destination")
	}
	if after, _ := os.Stat(src); after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("the source was touched")
	}
	for _, r := range []float64{plan.SourceRatio, 200} {
		d := filepath.Join(t.TempDir(), "no.braw")
		if _, err := Shrink(ShrinkOptions{Src: src, Dst: d, Ratio: r}); err == nil {
			t.Errorf("ratio %g was accepted", r)
		}
		if _, err := os.Stat(d); err == nil {
			t.Errorf("ratio %g left a file behind", r)
		}
	}
	// A cancelled run leaves nothing behind.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := filepath.Join(t.TempDir(), "cancel.braw")
	if _, err := Shrink(ShrinkOptions{Ctx: ctx, Src: src, Dst: d, Ratio: want.Target}); err != ErrCanceled {
		t.Errorf("cancelled run returned %v", err)
	}
	if m, _ := filepath.Glob(d + "*"); len(m) > 0 {
		t.Errorf("cancelled run left %v", m)
	}
}
