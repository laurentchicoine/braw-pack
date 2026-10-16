package core

import (
	"bytes"
	"math"
	"math/rand"
	"os"
	"testing"
)

func TestFastTransformsMatchReference(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, rows := range []int{8, 4} {
		for it := 0; it < 2000; it++ {
			var a, f1, f2 [64]float64
			for i := 0; i < rows*8; i++ {
				if it%3 == 0 || rng.Intn(4) == 0 {
					a[i] = rng.Float64()*8190 - 4095
				}
			}
			idct(a[:], f1[:], rows)
			idctRef(a[:], f2[:], rows)
			for i := 0; i < rows*8; i++ {
				if math.Abs(f1[i]-f2[i]) > 1e-8 {
					t.Fatalf("idct rows=%d differs at %d: %v vs %v", rows, i, f1[i], f2[i])
				}
			}
			fdct(a[:], f1[:], rows)
			fdctRef(a[:], f2[:], rows)
			for i := 0; i < rows*8; i++ {
				if math.Abs(f1[i]-f2[i]) > 1e-8 {
					t.Fatalf("fdct rows=%d differs at %d: %v vs %v", rows, i, f1[i], f2[i])
				}
			}
		}
	}
}

// TestLosslessIdentity needs a real clip: set BRAW_TEST_CLIP. Planes decoded from a frame and
// encoded again must give the original frame bytes whenever no sample was clipped.
func TestLosslessIdentity(t *testing.T) {
	path := os.Getenv("BRAW_TEST_CLIP")
	if path == "" {
		t.Skip("BRAW_TEST_CLIP not set")
	}
	clip, err := OpenClip(path)
	if err != nil {
		t.Fatal(err)
	}
	defer clip.Close()
	checked := 0
	for _, i := range sampleFrames(clip.Frames(), 12) {
		fr, err := clip.frame(i)
		if err != nil {
			t.Fatal(err)
		}
		fi, Y, U, V, side, st, err := decodePlanes(fr, 1, planesAll)
		if err != nil {
			t.Fatal(err)
		}
		if st.lumaOut+st.chromaOut > 0 {
			continue
		}
		out, err := encodePlanes(fr[:fi.offs[0]+fi.hs], side, Y, U, V, 1, 0)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(out, fr[:len(out)]) || len(out) > len(fr) {
			t.Fatalf("frame %d is not rebuilt identically", i)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no sampled frame was free of clipped samples, nothing was checked")
	}
	t.Logf("%d frames identical", checked)
}
