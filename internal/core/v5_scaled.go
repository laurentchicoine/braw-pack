package core

// URSA Cine 12K LF's in-sensor scaled 4K and 8K modes. Their coded blocks
// are 4x4/4x2 or 8x8/8x4, in two or three progressive streams respectively.
//
// The Pyxis 6K (Bayer sensor) records layout 0x0804, which shares this path:
// 8x8/8x4 blocks in 16x8 macroblocks with an 8-bit side byte, but at the
// header's own size, in four streams, and with the end-of-block code kept
// after a filled block, as in the 12K mode (docs/braw-v5-pyxis.md).
import (
	"fmt"
	"math"
)

// v5LayoutFull8 is the transform layout (hs+0x12) of Pyxis 6K recordings.
const v5LayoutFull8 = 0x0804

var (
	v5ScaledYSplit  = [v5Streams]int{4, 16, 16, 16}
	v5ScaledCSplit  = [v5Streams]int{2, 8, 8, 8}
	v5Scaled8YSplit = [v5Streams]int{4, 16, 64, 64}
	v5Scaled8CSplit = [v5Streams]int{2, 8, 32, 32}
	v5Full8YSplit   = [v5Streams]int{4, 16, 32, 64}
	v5Full8CSplit   = [v5Streams]int{2, 8, 16, 32}
)

func (fi *frameInfo) scaledSize() int {
	if fi.v5BlockSize == 8 {
		return 8
	}
	return 4
}

func (fi *frameInfo) scaledStreams() int {
	if fi.v5Full8 {
		return 4
	}
	if fi.scaledSize() == 8 {
		return 3
	}
	return 2
}

func (fi *frameInfo) scaledSplits() (y, c *[v5Streams]int) {
	if fi.v5Full8 {
		return &v5Full8YSplit, &v5Full8CSplit
	}
	if fi.scaledSize() == 8 {
		return &v5Scaled8YSplit, &v5Scaled8CSplit
	}
	return &v5ScaledYSplit, &v5ScaledCSplit
}

// parseFrameV5Scaled converts the already validated sensor geometry to the
// actual coded plane geometry. The original header bytes remain untouched.
func parseFrameV5Scaled(fr []byte, fi *frameInfo) (*frameInfo, error) {
	switch be.Uint16(fr[fi.hs+0x12:]) {
	case 0x0a14:
		fi.v5BlockSize = 4
	case 0x0914:
		fi.v5BlockSize = 8
	case v5LayoutFull8:
		// The header already gives the coded plane size (checked by parseFrameV5).
		fi.v5BlockSize, fi.v5Scaled, fi.v5Full8 = 8, true, true
		return fi, nil
	default:
		return nil, fmt.Errorf("unsupported scaled version 5 layout")
	}
	n := fi.scaledSize()
	if fi.w*n%12 != 0 || fi.h*n%6 != 0 || fi.tileH*n%6 != 0 {
		return nil, fmt.Errorf("unexpected scaled version 5 geometry")
	}
	fi.v5Scaled = true
	fi.w = fi.w * n / 12
	fi.h = fi.h * n / 6
	fi.tileH = fi.tileH * n / 6
	for i := range fi.tileW {
		if fi.tileW[i]*n%12 != 0 {
			return nil, fmt.Errorf("unexpected scaled tile width")
		}
		fi.tileW[i] = fi.tileW[i] * n / 12
		fi.tileX[i] = fi.tileX[i] * n / 12
	}
	return fi, nil
}

func (fi *frameInfo) v5ScaledTile(c, r int) (px, py, mbw, mbh int) {
	h := fi.tileH
	if r == fi.nth-1 {
		h = fi.h - r*fi.tileH
	}
	n := fi.scaledSize()
	return fi.tileX[c], r * fi.tileH, fi.tileW[c] / (2 * n), h / n
}

// readBlock and writeBlock code one block the way this frame's layout does.
func (fi *frameInfo) readBlock(rd *[v5Streams]bitReader, split *[v5Streams]int, prev *int64, lv []int32) (int, error) {
	if fi.v5Full8 {
		return v5ReadBlock(rd, split, prev, lv)
	}
	return v5ScaledReadBlock(rd, split, prev, lv)
}

func (fi *frameInfo) writeBlock(w *[v5Streams]bitWriter, split *[v5Streams]int, prev *int64, lv []int32) {
	if fi.v5Full8 {
		v5WriteBlock(w, split, prev, lv)
		return
	}
	v5ScaledWriteBlock(w, split, prev, lv)
}

// Like the full-size mode, each codeword belongs to the stream in which
// its zero run starts. Unlike that mode, a completely filled block has no
// end-of-block code. Reading another code in that case loses stream alignment.
func v5ScaledReadBlock(rd *[v5Streams]bitReader, split *[v5Streams]int, prev *int64, lv []int32) (int, error) {
	r := &rd[0]
	e := dcLookup[r.peek(dcLookupBits)]
	if e.sym < 0 {
		return 0, fmt.Errorf("bad DC code")
	}
	r.pos += int(e.bits)
	d := dcTable[e.sym]
	sgn := r.get(int(d[1]))
	*prev += int64(r.get(int(d[0]))) + dcvalTab[sgn][d[2]]
	lv[0] = int32(*prev)
	maxc := split[3]
	i, last := 0, 0
	for i+1 < maxc {
		s := v5StreamOf(i+1, split)
		r = &rd[s]
		ahead := r.peek(acLookupBits)
		e := acFast[ahead>>(acLookupBits-acFastBits)]
		if e.bits == 0 {
			e = acLookup[ahead]
			if e.sym < 0 {
				return 0, fmt.Errorf("bad AC code")
			}
		}
		r.pos += int(e.bits)
		run, ln := int(acTable[e.sym][0]), int(acTable[e.sym][1])
		if run == 255 {
			return last, nil
		}
		i += 1 + run
		if i >= maxc {
			return 0, fmt.Errorf("coefficient index out of range")
		}
		if ln == 0 {
			continue
		}
		v := int32(r.get(ln))
		if v < 1<<(ln-1) {
			v -= 1<<ln - 1
		}
		lv[i], last = v, i
	}
	return last, nil
}

func v5ScaledWriteBlock(w *[v5Streams]bitWriter, split *[v5Streams]int, prev *int64, lv []int32) {
	diff := int64(lv[0]) - *prev
	if diff > 32767 {
		diff = 32767
	} else if diff < -32767 {
		diff = -32767
	}
	*prev += diff
	ad := diff
	if ad < 0 {
		ad = -ad
	}
	k := bitLen(ad)
	w[0].put(dcCodes[k], dcBits[k])
	if k > 0 {
		if diff > 0 {
			w[0].put(1, 1)
			w[0].put(uint32(diff-int64(1)<<(k-1)), uint8(k-1))
		} else {
			w[0].put(0, 1)
			w[0].put(uint32(diff+int64(1)<<k-1), uint8(k-1))
		}
	}
	i := 0
	maxc := split[3]
	for p := 1; p < maxc; p++ {
		a := lv[p]
		if a == 0 {
			continue
		}
		for p-i-1 > 15 {
			w[v5StreamOf(i+1, split)].put(zrlCode, zrlBits)
			i += 16
		}
		s := v5StreamOf(i+1, split)
		neg := a < 0
		if neg {
			a = -a
		}
		nb := bitLen(int64(a))
		w[s].put(acEncCode[p-i-1][nb], acEncBits[p-i-1][nb])
		if neg {
			w[s].put(uint32((1<<nb-1)-a), uint8(nb))
		} else {
			w[s].put(uint32(a), uint8(nb))
		}
		i = p
	}
	if i+1 < maxc {
		s := v5StreamOf(i+1, split)
		w[s].put(eobCode, eobBits)
	}
}

func v5ScaledRewrite(fr []byte, fi *frameInfo) ([]byte, error) {
	return v5ScaledRequant(fr, fi, fi.qscale0, 0, 0)
}

func v5ScaledRequant(fr []byte, fi *frameInfo, q int, dz, tilt float64) ([]byte, error) {
	if q < 0 || q > 255 {
		return nil, fmt.Errorf("qscale0 %d out of range", q)
	}
	change := q != fi.qscale0 || dz != 0 || tilt != 0
	n := fi.scaledSize()
	ys, cs := fi.scaledSplits()
	yscan, cscan := fi.scaledScans()
	yA0, yD0, cA0, cD0 := fi.scaledScale(fi.qscale0)
	yA1, yD1, cA1, cD1 := fi.scaledScale(q)
	if change && (yD1 <= 0 || cD1 <= 0 || yD0 <= 0 || cD0 <= 0) {
		return nil, fmt.Errorf("frame without quantisation")
	}
	ratio := [2][2]float64{{yD0 / yD1, yA0 / yA1}, {cD0 / cD1, cA0 / cA1}}
	streams := make([][v5Streams][]byte, fi.ntw*fi.nth)
	errs := make([]error, len(streams))
	forTiles(len(streams), func(t int) {
		c, r := t/fi.nth, t%fi.nth
		rd, err := fi.v5Readers(fr, c, r)
		if err != nil {
			errs[t] = err
			return
		}
		var w [v5Streams]bitWriter
		_, _, mbw, mbh := fi.v5ScaledTile(c, r)
		var pi, po [3]int64
		var lv [64]int32
		for mb := 0; mb < mbw*mbh; mb++ {
			for b := 0; b < 4; b++ {
				split, p, rt := ys, 0, &ratio[0]
				if b >= 2 {
					split, p, rt = cs, b-1, &ratio[1]
				}
				last, e := fi.readBlock(&rd, split, &pi[p], lv[:split[3]])
				if e != nil {
					errs[t] = fmt.Errorf("tile %d,%d: %w", c, r, e)
					return
				}
				if change {
					lv[0] = int32(math.RoundToEven(float64(lv[0]) * rt[0]))
					for k := 1; k <= last; k++ {
						v := lv[k]
						if v == 0 {
							continue
						}
						weight := 0.0
						if b < 2 {
							i := yscan[k]
							weight = float64(i/n+i%n) / float64(2*n-2)
						} else {
							i := cscan[k]
							weight = float64(2*(i/n)+i%n) / float64(2*n-3)
						}
						x := math.Abs(float64(v)) * rt[1]
						a := int32(0)
						if x >= 0.5+dz+tilt*weight {
							a = int32(min(int(math.Ceil(x-0.5-dz)), 4095))
						}
						if v < 0 {
							a = -a
						}
						lv[k] = a
					}
				}
				fi.writeBlock(&w, split, &po[p], lv[:split[3]])
				clear(lv[:last+1])
			}
			w[0].put(rd[0].get(8), 8)
		}
		if v5Overrun(&rd) {
			errs[t] = fmt.Errorf("tile %d,%d: overrun", c, r)
			return
		}
		for s := range w {
			w[s].flush()
			streams[t][s] = w[s].buf
		}
	})
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	hdr := append([]byte(nil), fr[:fi.offs[0]+fi.hs]...)
	hdr[fi.hs+9] = byte(q)
	return fi.v5Frame(hdr, streams), nil
}
