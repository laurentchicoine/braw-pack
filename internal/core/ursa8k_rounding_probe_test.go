package core

import (
	"math"
	"os"
	"testing"
)

func TestUrsa8KRoundingFloor(t *testing.T) {
	path := os.Getenv("BRAW_V5_8K_CLIP")
	if path == "" {
		t.Skip("BRAW_V5_8K_CLIP not set")
	}
	c, err := OpenClip(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	frames := sampleFrames(c.Frames(), 12)
	maxDev := 0.0
	for _, frame := range frames {
		fr, e := c.frame(frame)
		if e != nil {
			t.Fatal(e)
		}
		_, _, _, _, _, st, e := decodePlanes(fr, 1, planesAll)
		if e != nil {
			t.Fatal(e)
		}
		maxDev = math.Max(maxDev, st.maxChroma)
	}
	cs := chooseChromaScale(maxDev)
	lost, detail := 0.0, 0.0
	for _, frame := range frames {
		fr, e := c.frame(frame)
		if e != nil {
			t.Fatal(e)
		}
		fi, y, _, _, _, _, e := decodePlanes(fr, cs, planesLuma)
		if e != nil {
			t.Fatal(e)
		}
		rounded := make([]uint16, len(y))
		for i, v := range y {
			rounded[i] = to10(v) << 2
		}
		l, d := detailEnergy(y, rounded, fi.w, fi.h)
		lost += l
		detail += d
	}
	t.Logf("%d spread frames, colour scale %.6f, camera step %.3f, pure 10-bit rounding retains %.1f%% (no HEVC)", len(frames), cs, clipStep(c, cs), detailKept(lost, detail))
}
