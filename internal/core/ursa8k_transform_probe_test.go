package core

import (
	"os"
	"path/filepath"
	"testing"
)

// Research-only fixtures retain the camera index and point all video samples at
// a chosen source frame. Only frame zero is used in the SDK probes.
func ursa8kFixture(t *testing.T, c *Clip, fr []byte, name string) string {
	t.Helper()
	out := append([]byte(nil), c.pre...)
	off := len(out)
	out = append(out, fr...)
	for len(out)%4096 != 0 {
		out = append(out, 0)
	}
	moov := append([]byte(nil), c.moov...)
	for ti, tr := range c.tracks {
		for i := range tr.chunks {
			be.PutUint64(moov[tr.co64+16+8*i:], uint64(off))
			if ti == 0 {
				be.PutUint32(moov[tr.stsz+20+4*i:], uint32(len(fr)))
			}
		}
	}
	be.PutUint32(out[:4], 8)
	copy(out[4:8], "wide")
	be.PutUint32(out[8:12], uint32(len(out)-8))
	copy(out[12:16], "mdat")
	out = append(out, moov...)
	root := `X:/braw-pack/research/ursa-8k`
	if e := os.MkdirAll(root, 0755); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(root, name+`.braw`)
	if e := os.WriteFile(path, out, 0644); e != nil {
		t.Fatal(e)
	}
	return path
}

func TestUrsa8KTransformFixture(t *testing.T) {
	if os.Getenv("BRAW_8K_PROBE") == "" {
		t.Skip("BRAW_8K_PROBE not set")
	}
	c, e := OpenClip(`X:/temp/compress3/A001_09302023_C039.braw`)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	fr, e := c.frame(0)
	if e != nil {
		t.Fatal(e)
	}
	hs := int(be.Uint32(fr))
	t.Logf("frames%d hs%x q0%d caps%d/%d header%x", c.Frames(), hs, fr[hs+9], be.Uint16(fr[hs+0x20:]), be.Uint16(fr[hs+0x22:]), fr[hs:hs+0x80])
	t.Log(ursa8kFixture(t, c, fr, "baseline-frame0"))
}

func TestUrsa8KConstantDCLaw(t *testing.T) {
	if os.Getenv("BRAW_8K_PROBE") == "" {
		t.Skip("BRAW_8K_PROBE not set")
	}
	c, e := OpenClip(`X:/temp/compress3/A001_09302216_C095.braw`)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	fr, e := c.frame(0)
	if e != nil {
		t.Fatal(e)
	}
	fi, e := parseFrame(fr)
	if e != nil {
		t.Fatal(e)
	}
	// Uncapped q8 colour step300 *211 equals the candidate q20 cap
	// (211*10/7) *210. With Y halved for q20, both pictures must be identical
	// if the cap uses the exact rational value without integer rounding.
	for _, v := range []struct {
		name         string
		q, y, c, cap int
	}{{"constant-q8", 8, -1000, 211, 256}, {"constant-q20-cap211", 20, -500, 210, 211}, {"constant-q8-negative", 8, -1000, -211, 256}, {"constant-q20-cap211-negative", 20, -500, -210, 211}} {
		streams := make([][v5Streams][]byte, fi.ntw*fi.nth)
		ys, cs := fi.scaledSplits()
		for col := 0; col < fi.ntw; col++ {
			for row := 0; row < fi.nth; row++ {
				_, _, mw, mh := fi.v5ScaledTile(col, row)
				var wr [v5Streams]bitWriter
				var prev [3]int64
				var lv [64]int32
				for mb := 0; mb < mw*mh; mb++ {
					for block := 0; block < 4; block++ {
						sp, p, count := ys, 0, 16
						lv[0] = int32(v.y)
						if block >= 2 {
							sp, p, count = cs, block-1, 8
							lv[0] = 0
							if block == 2 {
								lv[0] = int32(v.c)
							}
						}
						v5ScaledWriteBlock(&wr, sp, &prev[p], lv[:count])
					}
					wr[0].put(255, 8)
				}
				for s := range wr {
					wr[s].flush()
					streams[col*fi.nth+row][s] = wr[s].buf
				}
			}
		}
		hdr := append([]byte(nil), fr[:fi.hs+fi.offs[0]]...)
		hdr[fi.hs+9] = byte(v.q)
		be.PutUint16(hdr[fi.hs+0x22:], uint16(v.cap))
		out := fi.v5Frame(hdr, streams)
		t.Log(ursa8kFixture(t, c, out, v.name))
	}
}

func TestUrsa8KQuantFixtures(t *testing.T) {
	if os.Getenv("BRAW_8K_PROBE") == "" {
		t.Skip("BRAW_8K_PROBE not set")
	}
	c, e := OpenClip(`X:/temp/compress3/A001_09302023_C039.braw`)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	fr, e := c.frame(0)
	if e != nil {
		t.Fatal(e)
	}
	fi, e := parseFrame(fr)
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range []struct {
		name string
		q    int
	}{{"8k-quant-k2", v5QFor(fi.qscale0, 2)}, {"8k-quant-q23", 23}, {"8k-quant-q47", 47}} {
		out, e := v5ScaledRequant(fr, fi, v.q, 0, 0)
		if e != nil {
			t.Fatal(e)
		}
		t.Logf("%s q%d bytes%d->%d %s", v.name, v.q, len(fr), len(out), ursa8kFixture(t, c, out, v.name))
	}
	Y, U, V, side, st, e := decodePlanesV5Scaled(fr, fi, .5, planesLuma|planesChroma, 3)
	if e != nil {
		t.Fatal(e)
	}
	out, e := encodePlanesV5Scaled(fr[:fi.hs+fi.offs[0]], fi, side, Y, U, V, .5, 0, 3)
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("planesmax%.1f clips%d/%d %s", st.maxChroma, st.lumaOut, st.chromaOut, ursa8kFixture(t, c, out, "8k-plane-roundtrip"))
}
