package core

import (
	"math"
	"testing"
)

func TestSourceStep(t *testing.T) {
	// Version 2: a flat table of 50 at qscale0 9, the setting of some Pocket Cinema Camera 4K
	// clips. A coefficient step of 50*13*2048/65536 is that over 8 in root mean square.
	fi := &frameInfo{version: 2, qscale0: 9, qscale1: 1280, qscale2: 256}
	for i := range fi.table {
		fi.table[i] = 50
	}
	if got, want := sourceStep(fi, 0.5), 50*13*2048/65536.0/8; math.Abs(got-want) > 1e-9 {
		t.Errorf("version 2: got %v, want %v", got, want)
	}
	// Only the low frequencies count: changing the others leaves the step as it was.
	for v := 0; v < 8; v++ {
		for u := 4; u < 8; u++ {
			fi.table[v*8+u] = 999
		}
	}
	if got, want := sourceStep(fi, 1), 50*13*2048/65536.0/8; math.Abs(got-want) > 1e-9 {
		t.Errorf("version 2, high frequencies changed: got %v, want %v", got, want)
	}
	// Version 5: at qscale0 3 (the URSA Cine 12K test clip's setting) one level is one plane
	// value, times the colour scale (which version 5 applies to brightness too); at qscale0 2 the
	// camera's steps are 6/7 of that.
	if got := sourceStep(&frameInfo{version: 5, qscale0: 3, qscale1: 1280, qscale2: 256}, 0.828); math.Abs(got-0.828) > 1e-9 {
		t.Errorf("version 5, qscale0 3: got %v, want 0.828", got)
	}
	if got, want := sourceStep(&frameInfo{version: 5, qscale0: 2, qscale1: 1280, qscale2: 256}, 0.828), 0.828*6/7; math.Abs(got-want) > 1e-9 {
		t.Errorf("version 5, qscale0 2: got %v, want %v", got, want)
	}
}

func TestAutoQuality(t *testing.T) {
	// Camera steps measured on Pocket Cinema Camera 4K clips (docs/quality.md): each keeps about
	// its best detail at the top level, so none may be given a lower one.
	for _, s := range []float64{2.54, 3.35, 4.23, 5.31, 15.6, 35} {
		for _, enc := range []string{"nvenc", "x265"} {
			if q := autoQuality(s, enc); q != autoTop {
				t.Errorf("step %v, %s: level %d, want %d", s, enc, q, autoTop)
			}
		}
	}
	if q := autoQuality(0, "x265"); q != autoTop {
		t.Errorf("unknown step: level %d, want %d", q, autoTop)
	}
	// Finer steps get lower levels, never below the encoder's floor.
	prev := autoTop
	for s := 2.5; s > 0.05; s /= 1.2 {
		q := autoQuality(s, "x265")
		if q > prev || q < autoFloor("x265") {
			t.Errorf("step %.3f: level %d after %d", s, q, prev)
		}
		prev = q
	}
	if q := autoQuality(0.83, "x265"); q != 4 {
		t.Errorf("URSA Cine 12K step, x265: level %d, want 4", q)
	}
	// The GPU encoder gains nothing below the top level.
	if q := autoQuality(0.83, "nvenc"); q != autoTop {
		t.Errorf("URSA Cine 12K step, nvenc: level %d, want %d", q, autoTop)
	}
}

// TestDetailAt: at scale 2, the lab's multi-scale measure is detailEnergy itself.
func TestDetailAt(t *testing.T) {
	const w, h = 64, 48
	src, arc := make([]uint16, w*h), make([]uint16, w*h)
	for i := range src {
		src[i] = uint16(2048 + 300*math.Sin(float64(i)*0.37))
		arc[i] = src[i] + uint16(i%3)
	}
	l1, d1 := detailEnergy(src, arc, w, h)
	l2, d2 := detailAt(src, arc, w, h, 2)
	if l1 != l2 || d1 != d2 {
		t.Errorf("detailAt(2) = %v, %v; detailEnergy = %v, %v", l2, d2, l1, d1)
	}
}
