package core

import (
	"bytes"
	"math"
	"os"
	"testing"
)

func TestScaledV5Transforms(t *testing.T) {
	for _, size := range []int{4, 8} {
		for _, rows := range []int{size / 2, size} {
			for impulse := 0; impulse < size*rows; impulse++ {
				var original, samples, restored [64]float64
				original[impulse] = 17.25
				scaledV5IDCT(original[:], samples[:], rows, size)
				scaledV5FDCT(samples[:], restored[:], rows, size)
				for i := 0; i < size*rows; i++ {
					if math.Abs(original[i]-restored[i]) > 1e-12 {
						t.Fatalf("%dx4 coefficient%d impulse%d: %.12g != %.12g", rows, i, impulse, restored[i], original[i])
					}
				}
			}
		}
	}
	for k, i := range v5Scaled8YScan {
		if want := (v5YScan[k]/12)*8 + v5YScan[k]%12; i != want {
			t.Fatalf("8K brightness scan %d: %d != %d", k, i, want)
		}
	}
	for k, i := range v5Scaled8CScan {
		if want := (v5CScan[k]/12)*8 + v5CScan[k]%12; i != want {
			t.Fatalf("8K colour scan %d: %d != %d", k, i, want)
		}
	}
	for k, i := range v5ScaledYScan {
		if want := (v5YScan[k]/12)*4 + v5YScan[k]%12; i != want {
			t.Fatalf("brightness scan%d: %d != %d", k, i, want)
		}
	}
	for k, i := range v5ScaledCScan {
		if want := (v5CScan[k]/12)*4 + v5CScan[k]%12; i != want {
			t.Fatalf("colour scan%d: %d != %d", k, i, want)
		}
	}
}

func TestScaledV5QuantisationSteps(t *testing.T) {
	for _, c := range []struct {
		q0, q1, q2 int
		want       [4]float64
	}{
		{8, 1280, 256, [4]float64{600, 600, 300, 300}},
		{9, 1280, 256, [4]float64{650, 650, 350, 350}},
		{20, 1280, 256, [4]float64{1200, 1200, 600, 2560.0 / 7}},
		{23, 1280, 256, [4]float64{1350, 1350, 700, 2560.0 / 7}},
		{9, 459, 247, [4]float64{650, 650, 350, 350}},
		{9, 455, 245, [4]float64{650, 650, 350, 350}},
		{9, 454, 244, [4]float64{650, 4540.0 / 7, 350, 2440.0 / 7}},
		{47, 1280, 256, [4]float64{2550, 12800.0 / 7, 1300, 2560.0 / 7}},
	} {
		a, b, d, e := scaledV5Scale(c.q0, c.q1, c.q2)
		if got := [4]float64{a, b, d, e}; got != c.want {
			t.Errorf("q%d limits%d,%d: got%v want%v", c.q0, c.q1, c.q2, got, c.want)
		}
	}
}

func TestScaledV5LegacySteps(t *testing.T) {
	fi := &frameInfo{v5Scaled: true, qscale0: 20, qscale1: 1280, qscale2: 256}
	_, old, _ := scaledV5Steps(2, fi)
	_, exact, _ := scaledV5Steps(3, fi)
	if math.Abs(old[0]-math.Sqrt(8)*363/200) > 1e-12 || math.Abs(exact[0]-math.Sqrt(8)*(2560.0/7)/200) > 1e-12 {
		t.Fatal("legacy DC weights changed, or new DC weights were not used")
	}
}

// A source clip is optional because camera media is not part of the repository.
// This checks all coefficients, rather than just comparing our plane conversion
// to its inverse, so bitstream alignment and opaque side data are covered too.
func TestScaledV5PlanesRoundTrip(t *testing.T) {
	path := os.Getenv("BRAW_V5_SCALED_CLIP")
	if path == "" {
		t.Skip("BRAW_V5_SCALED_CLIP not set")
	}
	clip, e := OpenClip(path)
	if e != nil {
		t.Fatal(e)
	}
	defer clip.Close()
	for _, frame := range []int{0, clip.Frames() / 2, clip.Frames() - 1} {
		fr, e := clip.frame(frame)
		if e != nil {
			t.Fatal(e)
		}
		fi, e := parseFrame(fr)
		if e != nil {
			t.Fatal(e)
		}
		if !fi.v5Scaled {
			t.Fatal("expected scaled version 5 camera clip")
		}
		Y, U, V, side, st, e := decodePlanesV5Scaled(fr, fi, .5, planesLuma|planesChroma, 2)
		if e != nil {
			t.Fatal(e)
		}
		if st.lumaOut != 0 || st.chromaOut != 0 {
			t.Fatal("unexpected clipped samples")
		}
		got, e := encodePlanesV5Scaled(fr[:fi.hs+fi.offs[0]], fi, side, Y, U, V, .5, 0, 2)
		if e != nil {
			t.Fatal(e)
		}
		gf, e := parseFrame(got)
		if e != nil {
			t.Fatal(e)
		}
		changed, total := 0, 0
		ys, cs := fi.scaledSplits()
		for col := 0; col < fi.ntw; col++ {
			for row := 0; row < fi.nth; row++ {
				rd, _ := fi.v5Readers(fr, col, row)
				grd, _ := gf.v5Readers(got, col, row)
				_, _, mw, mh := fi.v5ScaledTile(col, row)
				var prev, wantPrev [3]int64
				var want, actual [64]int32
				for mb := 0; mb < mw*mh; mb++ {
					for b := 0; b < 4; b++ {
						sp, p, n := ys, 0, ys[3]
						if b >= 2 {
							sp, p, n = cs, b-1, cs[3]
						}
						clear(want[:])
						clear(actual[:])
						if _, e = v5ScaledReadBlock(&rd, sp, &wantPrev[p], want[:n]); e != nil {
							t.Fatal(e)
						}
						if _, e = v5ScaledReadBlock(&grd, sp, &prev[p], actual[:n]); e != nil {
							t.Fatal(e)
						}
						for i := 0; i < n; i++ {
							d := want[i] - actual[i]
							if d < 0 {
								d = -d
							}
							if d > 1 {
								t.Fatalf("frame%d tile%d,%d macroblock%d block%d coefficient%d changed by%d", frame, col, row, mb, b, i, d)
							}
							if d != 0 {
								changed++
							}
							total++
						}
					}
					if rd[0].get(8) != grd[0].get(8) {
						t.Fatal("side field changed")
					}
				}
			}
		}
		if changed > total/100 {
			t.Fatalf("frame%d: %d/%d coefficients changed", frame, changed, total)
		}
		_, _, _, restoredSide, _, e := decodePlanesV5Scaled(got, gf, .5, 0, 2)
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(side, restoredSide) {
			t.Fatal("side data changed")
		}
		t.Logf("frame%d: %d/%d coefficients differ, each by at most one level", frame, changed, total)
	}
}
