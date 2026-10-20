package core

import (
	"bytes"
	"context"
	"math"
	"os"
	"testing"
)

// TestV5Scale checks the step law measured with Blackmagic's decoder (docs/braw-v5.md) at the
// settings that were measured, and that steps 2 equal steps 1 at qscale0 3.
func TestV5Scale(t *testing.T) {
	for _, c := range []struct {
		q0, qs1, qs2       int
		yAC, yDC, cAC, cDC float64
	}{
		{3, 1280, 256, 350, 350, 200, 200},
		{2, 1280, 256, 300, 300, 200, 200},
		{0, 1280, 256, 200, 200, 200, 200},
		{4, 1280, 256, 400, 400, 200, 200},
		{5, 1280, 256, 450, 450, 250, 250},
		{6, 1280, 256, 500, 500, 250, 250},
		{8, 1280, 256, 600, 600, 300, 256}, // colour DC capped by qscale2
		{21, 1280, 256, 1250, 1250, 650, 256},
		{22, 1280, 256, 1300, 1280, 650, 256}, // brightness DC capped by qscale1
		{3, 0x15c, 0xc6, 350, 348, 200, 198},
	} {
		yAC, yDC, cAC, cDC := v5Scale(c.q0, c.qs1, c.qs2)
		if yAC != c.yAC || yDC != c.yDC || cAC != c.cAC || cDC != c.cDC {
			t.Errorf("q0 %d caps %d,%d: got %v %v %v %v", c.q0, c.qs1, c.qs2, yAC, yDC, cAC, cDC)
		}
	}
	fi := &frameInfo{qscale0: 3, qscale1: 1280, qscale2: 256}
	y1, c1, _ := v5Steps(1, fi)
	y2, c2, _ := v5Steps(2, fi)
	if y1 != y2 || c1 != c2 {
		t.Errorf("steps 2 differ from steps 1 at qscale0 3")
	}
	fi.qscale0 = 2
	y2, c2, _ = v5Steps(2, fi)
	if math.Abs(y2[0]-12*6.0/7) > 1e-12 || math.Abs(y2[143]-12*6.0/7) > 1e-12 || c2 != c1 {
		t.Errorf("steps 2 at qscale0 2: %v %v", y2[0], c2[0])
	}
	if _, _, ok := v5Steps(4, fi); ok {
		t.Errorf("unknown steps version accepted")
	}
	for _, q := range []int{0, 3, 8, 23, 47} {
		fi.qscale0 = q
		y2, c2, _ := v5Steps(2, fi)
		y3, c3, _ := v5Steps(3, fi)
		if y2 != y3 || c2 != c3 {
			t.Errorf("full-size 12K weights changed at q%d", q)
		}
	}
	for _, c := range []struct {
		q    int
		k    float64
		want int
	}{{3, 1, 3}, {3, 2, 10}, {2, 1.25, 4}, {3, 1.1, 4}, {3, 0.5, 0}, {3, 100, 255}} {
		if got := v5QFor(c.q, c.k); got != c.want {
			t.Errorf("v5QFor(%d, %v) = %d, want %d", c.q, c.k, got, c.want)
		}
	}
}

func openV5Clip(t *testing.T) *Clip {
	path := os.Getenv("BRAW_V5_CLIP")
	if path == "" {
		t.Skip("BRAW_V5_CLIP not set")
	}
	clip, err := OpenClip(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { clip.Close() })
	return clip
}

// v5QSwitch returns two neighbouring frames the camera coded at different qscale0 values.
func v5QSwitch(t *testing.T, clip *Clip) (a, b []byte) {
	var prev []byte
	q := -1
	for i := 0; i < clip.Frames(); i++ {
		fr, err := clip.frame(i)
		if err != nil {
			t.Fatal(err)
		}
		fi, err := parseFrame(fr)
		if err != nil {
			t.Fatal(err)
		}
		if q >= 0 && fi.qscale0 != q {
			return prev, fr
		}
		prev, q = fr, fi.qscale0
	}
	t.Skip("the clip keeps one qscale0")
	return nil, nil
}

func meanDev(p []uint16) float64 {
	s := 0.0
	for _, v := range p {
		s += float64(v) - 2048
	}
	return s / float64(len(p))
}

// TestV5StepsQSwitch needs a version 5 clip in which the camera changes qscale0 (BRAW_V5_CLIP):
// with steps 2 the brightness plane carries on across the change, with steps 1 it jumps.
func TestV5StepsQSwitch(t *testing.T) {
	clip := openV5Clip(t)
	a, b := v5QSwitch(t, clip)
	mean := func(fr []byte, steps int) float64 {
		_, y, _, _, _, st, err := decodePlanesSteps(fr, 0.5, planesLuma, steps)
		if err != nil {
			t.Fatal(err)
		}
		if st.lumaOut > 0 {
			t.Fatalf("%d samples clipped", st.lumaOut)
		}
		return meanDev(y)
	}
	r2 := mean(b, 2) / mean(a, 2)
	r1 := mean(b, 1) / mean(a, 1)
	t.Logf("mean brightness plane, next frame / previous: steps 2 %.4f, steps 1 %.4f", r2, r1)
	if math.Abs(r2-1) > 0.01 {
		t.Errorf("steps 2: brightness plane jumps by %.4f across the qscale0 change", r2)
	}
	if math.Abs(r1-1) < 0.05 {
		t.Errorf("steps 1 did not jump (%.4f): is qscale0 really changing?", r1)
	}
}

// TestV5PlanesRoundTripSteps1 needs a version 5 clip (BRAW_V5_CLIP): archives made with steps 1
// must still convert back to their levels.
func TestV5PlanesRoundTripSteps1(t *testing.T) {
	clip := openV5Clip(t)
	_, fr := v5QSwitch(t, clip)
	cs := 0.5
	fi, Y, U, V, side, _, err := decodePlanesSteps(fr, cs, planesAll, 1)
	if err != nil {
		t.Fatal(err)
	}
	out, err := encodePlanesSteps(fr[:fi.offs[0]+fi.hs], side, Y, U, V, cs, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	same, off, n := v5CompareLevels(t, fr, out, 1)
	if !same {
		t.Fatalf("levels differ by more than one step")
	}
	if off*100 > n {
		t.Fatalf("levels off by one in %d of %d blocks", off, n)
	}
}

// v5CompareLevels reads both frames block by block and reports whether every level of b is within
// tol of a's, and how many blocks differ at all.
func v5CompareLevels(t *testing.T, a, b []byte, tol int32) (ok bool, off, blocks int) {
	fa, err := parseFrame(a)
	if err != nil {
		t.Fatal(err)
	}
	fb, err := parseFrame(b)
	if err != nil {
		t.Fatal(err)
	}
	ok = true
	for c := 0; c < fa.ntw; c++ {
		for r := 0; r < fa.nth; r++ {
			ra, _ := fa.v5Readers(a, c, r)
			rb, _ := fb.v5Readers(b, c, r)
			_, _, mbw, mbh := fa.v5Tile(c, r)
			var pa, pb [3]int64
			for mb := 0; mb < mbw*mbh; mb++ {
				for k := 0; k < 4; k++ {
					split, p := &v5YSplit, 0
					if k >= 2 {
						split, p = &v5CSplit, k-1
					}
					var la, lb [144]int32
					if _, err := v5ReadBlock(&ra, split, &pa[p], la[:split[3]]); err != nil {
						t.Fatal(err)
					}
					if _, err := v5ReadBlock(&rb, split, &pb[p], lb[:split[3]]); err != nil {
						t.Fatal(err)
					}
					blocks++
					if la != lb {
						off++
					}
					for i := range la {
						if d := la[i] - lb[i]; d < -tol || d > tol {
							ok = false
						}
					}
				}
				if ra[0].get(16) != rb[0].get(16) {
					t.Fatalf("side field differs")
				}
			}
		}
	}
	return ok, off, blocks
}

// TestV5Requant needs a version 5 clip (BRAW_V5_CLIP): frames re-coded at a coarser qscale0, or
// with a tilt (as shrink does), must carry the qscale0 in their header, re-code byte for byte,
// keep the picture (as the planes of steps 2 show it) without a bias, and be smaller.
func TestV5Requant(t *testing.T) {
	clip := openV5Clip(t)
	a, b := v5QSwitch(t, clip)
	for _, fr := range [][]byte{a, b} {
		fi, err := parseFrame(fr)
		if err != nil {
			t.Fatal(err)
		}
		_, sy, su, _, _, _, err := decodePlanesSteps(fr, 0.5, planesAll, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct{ k, tilt float64 }{{2, 0}, {3.7, 0}, {1, 2.2}} {
			k := c.k
			out, err := requantTilt(fr, k, c.tilt, 0)
			if err != nil {
				t.Fatal(err)
			}
			fo, err := parseFrame(out)
			if err != nil {
				t.Fatal(err)
			}
			if want := v5QFor(fi.qscale0, k); fo.qscale0 != want {
				t.Fatalf("qscale0 %d, want %d", fo.qscale0, want)
			}
			again, err := requantFrame(out, 1, 0)
			if err != nil || !bytes.Equal(again, out) {
				t.Fatalf("re-coded frame does not read back byte for byte (%v)", err)
			}
			_, ry, ru, _, _, _, err := decodePlanesSteps(out, 0.5, planesAll, 2)
			if err != nil {
				t.Fatal(err)
			}
			by, bu := meanDev(ry)-meanDev(sy), meanDev(ru)-meanDev(su)
			py, pu := psnr12(ry, sy), psnr12(ru, su)
			t.Logf("qscale0 %d -> %d, tilt %v: %d -> %d bytes, brightness %.1f dB (bias %.3f), colour %.1f dB (bias %.3f)",
				fi.qscale0, fo.qscale0, c.tilt, len(fr), len(out), py, by, pu, bu)
			if len(out) >= len(fr) {
				t.Errorf("not smaller")
			}
			if math.Abs(by) > 0.1 || math.Abs(bu) > 0.1 {
				t.Errorf("biased: brightness %.3f, colour %.3f plane units", by, bu)
			}
			if py < 50 || pu < 50 {
				t.Errorf("picture changed too much: %.1f / %.1f dB", py, pu)
			}
		}
	}
}

// TestV5RebuildQuant needs a version 5 clip (BRAW_V5_CLIP): planes coded against a header scaled
// as for unpack -quant 2 must come out at the coarser qscale0, within a level of re-coding the
// frame there directly (ties are broken by the planes' rounding instead of toward zero), with the
// picture kept.
func TestV5RebuildQuant(t *testing.T) {
	clip := openV5Clip(t)
	_, fr := v5QSwitch(t, clip)
	cs := 0.5
	fi, Y, U, V, side, _, err := decodePlanesSteps(fr, cs, planesAll, 2)
	if err != nil {
		t.Fatal(err)
	}
	out, err := encodePlanesSteps(scaleQuant(fr[:fi.offs[0]+fi.hs], 2), side, Y, U, V, cs, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := requantTilt(fr, 2, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	ok, off, n := v5CompareLevels(t, direct, out, 1)
	_, ry, ru, _, _, _, err := decodePlanesSteps(out, cs, planesAll, 2)
	if err != nil {
		t.Fatal(err)
	}
	by, bu := meanDev(ry)-meanDev(Y), meanDev(ru)-meanDev(U)
	py, pu := psnr12(ry, Y), psnr12(ru, U)
	t.Logf("rebuilt at -quant 2: %d bytes (re-coded directly %d); levels off by one in %d of %d blocks; brightness %.1f dB (bias %.3f), colour %.1f dB (bias %.3f)",
		len(out), len(direct), off, n, py, by, pu, bu)
	if !ok {
		t.Fatalf("levels differ by more than one step from re-coding the frame directly")
	}
	if math.Abs(by) > 0.1 || math.Abs(bu) > 0.1 || py < 50 || pu < 50 {
		t.Fatalf("picture not kept")
	}
}

// TestV5Shrink needs a version 5 clip (BRAW_V5_CLIP) and memory (six 12K sample frames): a
// stronger ratio is reached with a tilt, close to the target.
func TestV5Shrink(t *testing.T) {
	clip := openV5Clip(t)
	p, err := newShrinkProbe(clip)
	if err != nil {
		t.Fatal(err)
	}
	target := p.sourceRatio() * 1.5
	opt, err := p.option(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if opt == nil {
		t.Fatalf("%.1f:1 not reached", target)
	}
	t.Logf("%.1f:1 -> tilt %.2f: %.2f:1, detail kept %.1f%%", target, opt.tilt, opt.Ratio, opt.DetailKept)
	if opt.Ratio < target || opt.Ratio > target*1.15 || opt.DetailKept < 80 {
		t.Fatalf("bad option %+v", opt)
	}
}
