package core

import (
	"bytes"
	"math"
	"math/rand"
	"os"
	"strings"
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

// TestNewerVersionRefused: a frame of an unknown version, with the picture header where version 5
// (URSA Cine 12K) puts it, must be refused by name, not as a damaged header.
func TestNewerVersionRefused(t *testing.T) {
	fr := make([]byte, 0x2000)
	be.PutUint32(fr, 0x400)
	copy(fr[4:], "bmdf")
	copy(fr[0x400:], "braw")
	fr[0x408] = 6
	_, err := parseFrame(fr)
	if err == nil || !strings.Contains(err.Error(), "version 6 is not supported") {
		t.Fatalf("got %v", err)
	}
	// A version 5 header with nothing behind it is a damaged frame, not a crash.
	fr[0x408] = 5
	if _, err := parseFrame(fr); err == nil {
		t.Fatal("empty version 5 frame accepted")
	}
	// The same header where versions 1 and 2 keep it, with a known version but in the wrong
	// place, is still a damaged frame.
	fr[0x408] = 2
	if _, err := parseFrame(fr); err == nil || err.Error() != "unexpected frame header" {
		t.Fatalf("got %v", err)
	}
}

// TestV5Rewrite needs a version 5 clip (BRAW_V5_CLIP, e.g. from an URSA Cine 12K): every frame,
// decoded to quantised levels and coded again, must come back byte for byte.
func TestV5Rewrite(t *testing.T) {
	path := os.Getenv("BRAW_V5_CLIP")
	if path == "" {
		t.Skip("BRAW_V5_CLIP not set")
	}
	clip, err := OpenClip(path)
	if err != nil {
		t.Fatal(err)
	}
	defer clip.Close()
	for i := 0; i < clip.Frames(); i++ {
		fr, err := clip.frame(i)
		if err != nil {
			t.Fatal(err)
		}
		out, err := requantFrame(fr, 1, 0)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if !bytes.Equal(out, fr) {
			n := 0
			for n < len(out) && n < len(fr) && out[n] == fr[n] {
				n++
			}
			t.Fatalf("frame %d differs: %d bytes vs %d, first difference at %#x", i, len(out), len(fr), n)
		}
	}
	t.Logf("%d frames identical", clip.Frames())
}

// TestV5PlanesRoundTrip needs a version 5 clip (BRAW_V5_CLIP): sample frames converted to planes,
// at the scale chosen for them, and coded again must keep their levels. The camera codes more
// finely than 12-bit planes can hold (DC spans about 4300 levels over the brightness range), so
// in smooth blocks, where the samples' rounding errors line up instead of averaging out, a level
// may come back one off; that must stay rare.
func TestV5PlanesRoundTrip(t *testing.T) {
	path := os.Getenv("BRAW_V5_CLIP")
	if path == "" {
		t.Skip("BRAW_V5_CLIP not set")
	}
	clip, err := OpenClip(path)
	if err != nil {
		t.Fatal(err)
	}
	defer clip.Close()
	for _, i := range sampleFrames(clip.Frames(), 3) {
		fr, err := clip.frame(i)
		if err != nil {
			t.Fatal(err)
		}
		_, _, _, _, _, st, err := decodePlanes(fr, 1, planesAll)
		if err != nil {
			t.Fatal(err)
		}
		cs := chooseChromaScale(st.maxChroma)
		fi, Y, U, V, side, st, err := decodePlanes(fr, cs, planesAll)
		if err != nil {
			t.Fatal(err)
		}
		if st.lumaOut+st.chromaOut > 0 {
			t.Fatalf("frame %d: %d+%d samples clipped at scale %v", i, st.lumaOut, st.chromaOut, cs)
		}
		out, err := encodePlanes(fr[:fi.offs[0]+fi.hs], side, Y, U, V, cs, 0)
		if err != nil {
			t.Fatal(err)
		}
		fo, err := parseFrame(out)
		if err != nil {
			t.Fatal(err)
		}
		blocks, off := 0, 0
		for c := 0; c < fi.ntw; c++ {
			for r := 0; r < fi.nth; r++ {
				ra, _ := fi.v5Readers(fr, c, r)
				rb, _ := fo.v5Readers(out, c, r)
				_, _, mbw, mbh := fi.v5Tile(c, r)
				var pa, pb [3]int64
				for mb := 0; mb < mbw*mbh; mb++ {
					for b := 0; b < 4; b++ {
						split, p := &v5YSplit, 0
						if b >= 2 {
							split, p = &v5CSplit, b-1
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
						for k := range la {
							if d := la[k] - lb[k]; d < -1 || d > 1 {
								t.Fatalf("frame %d tile %d,%d mb %d block %d: level %d: %d -> %d", i, c, r, mb, b, k, la[k], lb[k])
							}
						}
					}
					if ra[0].get(16) != rb[0].get(16) {
						t.Fatalf("frame %d: side field differs", i)
					}
				}
			}
		}
		if off*100 > blocks {
			t.Fatalf("frame %d: levels off by one in %d of %d blocks", i, off, blocks)
		}
		t.Logf("frame %d at scale %v: levels off by one in %d of %d blocks", i, cs, off, blocks)
	}
}
