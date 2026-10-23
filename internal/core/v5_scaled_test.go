package core

import (
	"bytes"
	"os"
	"testing"
)

func TestV5ScaledBlockRoundTrip(t *testing.T) {
	for _, split := range []*[v5Streams]int{&v5ScaledYSplit, &v5ScaledCSplit, &v5Scaled8YSplit, &v5Scaled8CSplit} {
		for _, mode := range []int{0, 1, 2} {
			full := mode == 1
			var w [v5Streams]bitWriter
			var expected [6][64]int32
			var prev int64
			for b := range expected {
				expected[b][0] = int32(-1200 + b*7)
				for p := 1; p < split[3]; p++ {
					if full || (mode == 0 && p%3 == 0) {
						expected[b][p] = int32((p*17+b*13)%31 - 15)
					}
				}
				if full || mode == 2 {
					expected[b][split[3]-1] = int32(b + 1)
				}
				v5ScaledWriteBlock(&w, split, &prev, expected[b][:split[3]])
			}
			var rd [v5Streams]bitReader
			var bits [v5Streams]int
			for s := range w {
				bits[s] = len(w[s].buf)*8 + int(w[s].nacc)
				w[s].flush()
				rd[s] = bitReader{data: w[s].buf}
			}
			prev = 0
			for b := range expected {
				var actual [64]int32
				if _, err := v5ScaledReadBlock(&rd, split, &prev, actual[:split[3]]); err != nil {
					t.Fatal(err)
				}
				if actual != expected[b] {
					t.Fatalf("size%d full%v block%d: got%v want%v", split[1], full, b, actual, expected[b])
				}
			}
			for s := range rd {
				if rd[s].pos != bits[s] {
					t.Fatalf("size%d full%v stream%d consumed%d bits, want%d", split[1], full, s, rd[s].pos, bits[s])
				}
			}
		}
	}
}

// The byte-for-byte gate requires a camera sample, supplied explicitly. Set
// BRAW_V5_SCALED_ALL=1 to check every frame rather than first/middle/last.
func TestV5ScaledCameraRewrite(t *testing.T) {
	path := os.Getenv("BRAW_V5_SCALED_CLIP")
	if path == "" {
		t.Skip("BRAW_V5_SCALED_CLIP not set")
	}
	c, err := OpenClip(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	frames := []int{0, c.Frames() / 2, c.Frames() - 1}
	if os.Getenv("BRAW_V5_SCALED_ALL") == "1" {
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
		if !fi.v5Scaled {
			t.Fatal("sample is not the supported scaled version 5 mode")
		}
		out, err := v5ScaledRewrite(fr, fi)
		if err != nil {
			t.Fatalf("frame%d: %v", fn, err)
		}
		if !bytes.Equal(fr, out) {
			t.Fatalf("frame%d did not rewrite byte for byte", fn)
		}
	}
	t.Logf("%d camera frames rewrite byte for byte", len(frames))
}
