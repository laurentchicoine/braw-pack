package core

import (
	"bytes"
	"fmt"
	"os"
	"testing"
)

func u8parse(fr []byte) (*frameInfo, error) {
	f := append([]byte(nil), fr...)
	h := bytes.Index(f[:min(2048, len(f))], []byte("braw"))
	if h < 0 {
		return nil, fmt.Errorf("header")
	}
	f[h+0x12] = 8
	f[h+0x13] = 12
	return parseFrame(f)
}
func u8read(rd *[4]bitReader, w *[4]bitWriter, split *[4]int, prev *int64, lv []int32, active int) (int, error) {
	r := &rd[0]
	e := dcLookup[r.peek(dcLookupBits)]
	if e.sym < 0 {
		return 0, fmt.Errorf("DC")
	}
	w[0].put(r.get(int(e.bits)), e.bits)
	d := dcTable[e.sym]
	sgn := r.get(int(d[1]))
	w[0].put(sgn, uint8(d[1]))
	val := r.get(int(d[0]))
	w[0].put(val, uint8(d[0]))
	*prev += int64(val) + dcvalTab[sgn][d[2]]
	lv[0] = int32(*prev)
	i, last := 0, 0
	maxc := split[active-1]
	for i+1 < maxc {
		s := v5StreamOf(i+1, split)
		r = &rd[s]
		a := r.peek(acLookupBits)
		e := acFast[a>>(acLookupBits-acFastBits)]
		if e.bits == 0 {
			e = acLookup[a]
			if e.sym < 0 {
				return 0, fmt.Errorf("AC")
			}
		}
		w[s].put(r.get(int(e.bits)), e.bits)
		run, ln := int(acTable[e.sym][0]), int(acTable[e.sym][1])
		if run == 255 {
			return last, nil
		}
		i += 1 + run
		if i >= maxc {
			return 0, fmt.Errorf("range%d", i)
		}
		v := r.get(ln)
		w[s].put(v, uint8(ln))
		if ln > 0 {
			sv := int32(v)
			if sv < 1<<(ln-1) {
				sv -= 1<<ln - 1
			}
			lv[i] = sv
			last = i
		}
	}
	return last, nil
}
func TestUrsa8kProbe(t *testing.T) {
	path := os.Getenv("BRAW_V5_8K_CLIP")
	if path == "" {
		t.Skip("BRAW_V5_8K_CLIP not set")
	}
	c, e := OpenClip(path)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	f, _ := c.frame(0)
	fi, e := u8parse(f)
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("frames%d hs%x w%dh%d tiles%d,%d height%d header%x", c.Frames(), fi.hs, fi.w, fi.h, fi.ntw, fi.nth, fi.tileH, f[fi.hs:fi.hs+0x40])
	rd, _ := fi.v5Readers(f, 0, 0)
	for s := range rd {
		t.Logf("s%d bytes%d", s, len(rd[s].data))
	}
	for _, side := range []int{8, 16, 32} {
		rd, _ := fi.v5Readers(f, 0, 0)
		var w [4]bitWriter
		var prev [3]int64
		var lv [64]int32
		sy := [4]int{4, 16, 64, 64}
		sc := [4]int{2, 8, 32, 32}
		mb := 0
		var err error
		for ; mb < 4000; mb++ {
			for b := 0; b < 4; b++ {
				sp, p := &sy, 0
				if b >= 2 {
					sp, p = &sc, b-1
				}
				_, err = u8read(&rd, &w, sp, &prev[p], lv[:], 3)
				if err != nil {
					break
				}
			}
			if err != nil {
				break
			}
			rd[0].get(side)
			if v5Overrun(&rd) {
				break
			}
		}
		t.Logf("side%d mb%d err%v pos%d/%d %d/%d %d/%d", side, mb, err, rd[0].pos, len(rd[0].data)*8, rd[1].pos, len(rd[1].data)*8, rd[2].pos, len(rd[2].data)*8)
	}
}
func TestUrsa8kExact(t *testing.T) {
	path := os.Getenv("BRAW_V5_8K_CLIP")
	if path == "" {
		t.Skip("BRAW_V5_8K_CLIP not set")
	}
	c, e := OpenClip(path)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	frames := []int{0, c.Frames() / 2, c.Frames() - 1}
	if os.Getenv("BRAW_8K_ALL") == "1" {
		frames = make([]int, c.Frames())
		for i := range frames {
			frames[i] = i
		}
	}
	sy := [4]int{4, 16, 64, 64}
	sc := [4]int{2, 8, 32, 32}
	for _, fn := range frames {
		f, _ := c.frame(fn)
		fi, e := u8parse(f)
		if e != nil {
			t.Fatal(e)
		}
		streams := make([][4][]byte, fi.ntw*fi.nth)
		for col := 0; col < fi.ntw; col++ {
			for row := 0; row < fi.nth; row++ {
				rd, _ := fi.v5Readers(f, col, row)
				var w [4]bitWriter
				var prev [3]int64
				var lv [64]int32
				h := fi.tileH
				if row == fi.nth-1 {
					h = fi.h - row*fi.tileH
				}
				for mb := 0; mb < (fi.tileW[col]/24)*(h/6); mb++ {
					for b := 0; b < 4; b++ {
						sp, p := &sy, 0
						if b >= 2 {
							sp, p = &sc, b-1
						}
						_, e = u8read(&rd, &w, sp, &prev[p], lv[:], 3)
						if e != nil {
							t.Fatalf("fn%d tile%d,%d mb%d b%d: %v", fn, col, row, mb, b, e)
						}
					}
					w[0].put(rd[0].get(8), 8)
				}
				if v5Overrun(&rd) {
					t.Fatalf("fn%d tile%d,%d overrun", fn, col, row)
				}
				for s := range w {
					w[s].flush()
					streams[col*fi.nth+row][s] = w[s].buf
				}
			}
		}
		out := fi.v5Frame(f[:fi.offs[0]+fi.hs], streams)
		if !bytes.Equal(f, out) {
			off := 0
			for off < min(len(f), len(out)) && f[off] == out[off] {
				off++
			}
			t.Fatalf("fn%d mismatchat%d lens%d,%d", fn, off, len(f), len(out))
		}
		if len(frames) <= 3 || fn%100 == 0 {
			t.Logf("fn%d exact%dbytes", fn, len(f))
		}
	}
	t.Logf("all %d frames exact", len(frames))
}
