package core

// Blackmagic RAW version 5, as recorded by the URSA Cine 12K LF (RGBW sensor).
//
// Worked out for braw-pack from the clips themselves, Blackmagic's published patents (EP3907994:
// coefficient sub-streams; EP3547680: the RGBW filter array) and comparisons with pictures from
// Blackmagic's decoder; docs/braw-v5.md describes the format. The code tables are those of
// versions 1 and 2 (tables.go). What changes:
//
//   - The picture header ("braw") follows a longer metadata block (0x400 bytes), with the same
//     fields at the same places relative to it, but no quantisation table.
//   - Macroblocks are 24x12 pixels: two 12x12 brightness blocks side by side, then a 12x6 block
//     of each colour plane, then a 16-bit side field. The colour planes are half resolution in
//     both directions, as before.
//   - Each tile is coded as four streams. Every block's coefficients are split between them by
//     frequency (rings of 2x2, 4x4, 8x8 and 12x12 coefficients for brightness), so a decoder
//     that reads fewer streams gets a smaller picture. Each codeword goes in the stream of the
//     position its zero run starts at, and the end-of-block code in the stream of the position
//     after the block's last coefficient. DC values and side fields are in the first stream.

import (
	"fmt"
	"math"
)

const (
	v5Streams = 4  // coded streams per tile
	v5MBW     = 24 // macroblock size in brightness pixels
	v5MBH     = 12
)

// Coefficients per block before which each stream ends, brightness and colour.
var (
	v5YSplit = [v5Streams]int{4, 16, 64, 144}
	v5CSplit = [v5Streams]int{2, 8, 32, 72}
)

// v5StreamOf is the stream holding scan position i (i < split[3]).
func v5StreamOf(i int, split *[v5Streams]int) int {
	switch {
	case i < split[0]:
		return 0
	case i < split[1]:
		return 1
	case i < split[2]:
		return 2
	}
	return 3
}

func parseFrameV5(fr []byte, fi *frameInfo) (*frameInfo, error) {
	hs := fi.hs
	// The tile table has room for 1024 entries and nothing follows it in the header.
	if len(fr) < hs+0x1080 {
		return nil, fmt.Errorf("frame too short for its tile table")
	}
	fi.qscale0 = int(fr[hs+9])
	fi.ntw, fi.nth = int(fr[hs+10]), int(fr[hs+11])
	fi.w, fi.h = int(be.Uint16(fr[hs+12:])), int(be.Uint16(fr[hs+14:]))
	fi.tileH = int(be.Uint16(fr[hs+16:]))
	fi.qscale1, fi.qscale2 = int(be.Uint16(fr[hs+0x20:])), int(be.Uint16(fr[hs+0x22:]))
	if fi.ntw == 0 || fi.nth == 0 || fi.ntw > 32 {
		return nil, fmt.Errorf("unexpected tile layout")
	}
	layout := be.Uint16(fr[hs+0x12:])
	// Layout 0x0804 (Pyxis 6K, Bayer sensor) codes 16x8 macroblocks at the header's own size.
	mbw, mbh := v5MBW, v5MBH
	if layout == v5LayoutFull8 {
		mbw, mbh = 16, 8
	}
	x := 0
	for i := 0; i < fi.ntw; i++ {
		tw := int(be.Uint16(fr[hs+0x40+2*i:]))
		if tw == 0 || tw%mbw != 0 {
			return nil, fmt.Errorf("unexpected tile width %d", tw)
		}
		fi.tileW = append(fi.tileW, tw)
		fi.tileX = append(fi.tileX, x)
		x += tw
	}
	if x != fi.w || fi.h%mbh != 0 {
		return nil, fmt.Errorf("unexpected geometry %dx%d", fi.w, fi.h)
	}
	if fi.tileH <= 0 || fi.tileH%mbh != 0 || (fi.nth-1)*fi.tileH >= fi.h {
		return nil, fmt.Errorf("unexpected tile layout")
	}
	n := v5Streams * fi.ntw * fi.nth
	if n > 1024 {
		return nil, fmt.Errorf("unexpected tile layout")
	}
	fi.offs = make([]int, n+1)
	for i := 0; i < n; i++ {
		fi.offs[i] = int(be.Uint32(fr[hs+0x80+4*i:]))
	}
	fi.offs[n] = len(fr) - hs
	if first := fi.offs[0] + hs; first < hs+0x1080 || first > len(fr) {
		return nil, fmt.Errorf("bad tile offsets")
	}
	// A stream may be empty: the last one of a block's coefficients often holds nothing. (The
	// end of the last stream is only checked when it is read: encodePlanes passes the header
	// alone.)
	for i := 0; i+1 < n; i++ {
		if fi.offs[i] > fi.offs[i+1] {
			return nil, fmt.Errorf("bad tile offsets")
		}
	}
	if layout == 0x0a14 || layout == 0x0914 || layout == v5LayoutFull8 {
		return parseFrameV5Scaled(fr, fi)
	}
	// Other transform layouts must be identified before using the 12K interpretation.
	if be.Uint16(fr[hs+0x12:]) != 0x080c {
		return nil, fmt.Errorf("unsupported version 5 transform layout %#04x", be.Uint16(fr[hs+0x12:]))
	}
	return fi, nil
}

// v5Tile returns tile c,r's position in brightness pixels and its size in macroblocks.
func (fi *frameInfo) v5Tile(c, r int) (px, py, mbw, mbh int) {
	h := fi.tileH
	if r == fi.nth-1 {
		h = fi.h - r*fi.tileH
	}
	return fi.tileX[c], r * fi.tileH, fi.tileW[c] / v5MBW, h / v5MBH
}

// v5Readers returns bit readers on the four streams of tile c,r of frame fr.
func (fi *frameInfo) v5Readers(fr []byte, c, r int) (rd [v5Streams]bitReader, err error) {
	for s := range rd {
		i := c*v5Streams*fi.nth + s*fi.nth + r
		start, end := fi.offs[i]+fi.hs, fi.offs[i+1]+fi.hs
		if start > end || end > len(fr) {
			return rd, fmt.Errorf("bad tile offsets")
		}
		rd[s] = bitReader{data: fr[start:end]}
		if fi.v5Scaled && s >= fi.scaledStreams() {
			for _, value := range rd[s].data {
				if value != 0 {
					return rd, fmt.Errorf("unsupported scaled version 5 data in stream %d", s)
				}
			}
		}
	}
	return rd, nil
}

// v5Overrun reports whether any reader went past the end of its stream.
func v5Overrun(rd *[v5Streams]bitReader) bool {
	for s := range rd {
		if rd[s].pos > len(rd[s].data)*8 {
			return true
		}
	}
	return false
}

// v5ReadBlock reads one block: lv receives its levels by scan position, lv[0] being the DC value
// once the prediction prev is added. lv must be all zero on entry. It returns the last position
// coded (0 when the block has no AC coefficients).
func v5ReadBlock(rd *[v5Streams]bitReader, split *[v5Streams]int, prev *int64, lv []int32) (int, error) {
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
	maxc := split[v5Streams-1]
	i, last := 0, 0
	for {
		r := &rd[v5Streams-1]
		if i+1 < maxc {
			r = &rd[v5StreamOf(i+1, split)]
		}
		bitsAhead := r.peek(acLookupBits)
		e := acFast[bitsAhead>>(acLookupBits-acFastBits)]
		if e.bits == 0 {
			if e = acLookup[bitsAhead]; e.sym < 0 {
				return 0, fmt.Errorf("bad AC code")
			}
		}
		r.pos += int(e.bits)
		run, ln := int(acTable[e.sym][0]), int(acTable[e.sym][1])
		if run == 255 {
			return last, nil
		}
		if i += 1 + run; i >= maxc {
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
}

// v5WriteBlock is the inverse of v5ReadBlock: it codes the levels lv (by scan position, lv[0]
// the DC value) into the four streams. AC levels must lie within +-4095. The DC difference is
// limited to what the code can carry, and prev follows what was coded.
func v5WriteBlock(w *[v5Streams]bitWriter, split *[v5Streams]int, prev *int64, lv []int32) {
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
	maxc := split[v5Streams-1]
	i := 0
	for p := 1; p < maxc; p++ {
		a := lv[p]
		if a == 0 {
			continue
		}
		for p-i-1 > 15 {
			w[v5StreamOf(i+1, split)].put(zrlCode, zrlBits)
			i += 16
		}
		zr := p - i - 1
		s := &w[v5StreamOf(i+1, split)]
		neg := a < 0
		if neg {
			a = -a
		}
		nb := bitLen(int64(a))
		s.put(acEncCode[zr][nb], acEncBits[zr][nb])
		if neg {
			s.put(uint32((1<<nb-1)-a), uint8(nb))
		} else {
			s.put(uint32(a), uint8(nb))
		}
		i = p
	}
	if i+1 < maxc {
		w[v5StreamOf(i+1, split)].put(eobCode, eobBits)
	} else {
		w[v5Streams-1].put(eobCode, eobBits)
	}
}

// v5Frame lays out a frame from its header (the original bytes up to the first stream) and the
// coded streams of every tile, indexed [c*nth+r][stream]. Like the camera, it starts each tile
// column's run of one stream on a 512-byte boundary, pads every stream to 8 bytes and the
// picture to 512.
func (fi *frameInfo) v5Frame(hdr []byte, streams [][v5Streams][]byte) []byte {
	size := len(hdr) + 512*(v5Streams*fi.ntw+1)
	for _, t := range streams {
		for _, s := range t {
			size += len(s) + 8
		}
	}
	hs := fi.hs
	out := append(make([]byte, 0, size), hdr...)
	pad := func(m int) {
		for (len(out)-hs)%m != 0 {
			out = append(out, 0)
		}
	}
	offs := make([]int, v5Streams*fi.ntw*fi.nth)
	for c := 0; c < fi.ntw; c++ {
		for s := 0; s < v5Streams; s++ {
			pad(512)
			for r := 0; r < fi.nth; r++ {
				offs[c*v5Streams*fi.nth+s*fi.nth+r] = len(out) - hs
				out = append(out, streams[c*fi.nth+r][s]...)
				pad(8)
			}
		}
	}
	pad(512)
	for i, o := range offs {
		be.PutUint32(out[hs+0x80+4*i:], uint32(o))
	}
	be.PutUint32(out[hs+4:], uint32(len(out)-hs))
	return out
}

// v5Rewrite decodes every block of a frame and codes it again with the same levels. A frame the
// camera wrote comes back byte for byte, which shows the bitstream is fully understood.
func v5Rewrite(fr []byte, fi *frameInfo) ([]byte, error) {
	if fi.v5Scaled {
		return v5ScaledRewrite(fr, fi)
	}
	streams := make([][v5Streams][]byte, fi.ntw*fi.nth)
	var lv [144]int32
	for c := 0; c < fi.ntw; c++ {
		for r := 0; r < fi.nth; r++ {
			rd, err := fi.v5Readers(fr, c, r)
			if err != nil {
				return nil, err
			}
			var w [v5Streams]bitWriter
			_, _, mbw, mbh := fi.v5Tile(c, r)
			var prevIn, prevOut [3]int64
			for mb := 0; mb < mbw*mbh; mb++ {
				for b := 0; b < 4; b++ {
					split, p := &v5YSplit, 0
					if b >= 2 {
						split, p = &v5CSplit, b-1
					}
					last, err := v5ReadBlock(&rd, split, &prevIn[p], lv[:split[3]])
					if err != nil {
						return nil, fmt.Errorf("tile %d,%d: %w", c, r, err)
					}
					v5WriteBlock(&w, split, &prevOut[p], lv[:split[3]])
					clear(lv[:last+1])
				}
				w[0].put(rd[0].get(16), 16)
			}
			if v5Overrun(&rd) {
				return nil, fmt.Errorf("tile %d,%d: overrun", c, r)
			}
			for s := range w {
				w[s].flush()
				streams[c*fi.nth+r][s] = w[s].buf
			}
		}
	}
	return fi.v5Frame(fr[:fi.offs[0]+fi.hs], streams), nil
}

// v5QFor returns the qscale0 whose brightness steps come closest to k times those of qscale0 q
// (the steps grow as q0+4, see v5Scale).
func v5QFor(q int, k float64) int {
	return min(255, max(0, int(math.Floor(k*float64(q+4)+0.5))-4))
}

// v5TiltWeight says how strongly a tilt widens the zero bin of natural coefficient n of a
// brightness (rows 12) or colour (rows 6) block: 0 at DC up to 1 for the highest frequency, as
// tiltWeight does for versions 1 and 2.
func v5TiltWeight(n, rows int) float64 {
	if rows == 12 {
		return float64(n/12+n%12) / 22
	}
	return float64(2*(n/12)+n%12) / 21
}

// v5Requant re-codes a frame at qscale0 q: every level, DC included, is rescaled by the ratio of
// Blackmagic's steps at the two settings (v5Scale) and rounded, AC magnitudes with the zero bin
// widened by dz, as in requantTile. The steps cannot be tilted, so a tilt instead drops the AC
// levels whose rescaled magnitude stays under 0.5+dz plus tilt times their weight
// (v5TiltWeight): the smallest high-frequency ones first, leaving the others alone. Only
// qscale0 changes in the header. The caps at hs+0x20 and hs+0x22 are kept, as are the fields at
// hs+0x1c..0x1f, which the camera also changes with qscale0 but which barely alter Blackmagic's
// picture (docs/braw-v5.md).
func v5Requant(fr []byte, fi *frameInfo, q int, dz, tilt float64) ([]byte, error) {
	if fi.v5Scaled {
		return v5ScaledRequant(fr, fi, q, dz, tilt)
	}
	if q < 0 || q > 255 {
		return nil, fmt.Errorf("qscale0 %d out of range", q)
	}
	if q == fi.qscale0 && dz == 0 && tilt == 0 {
		return v5Rewrite(fr, fi)
	}
	yA0, yD0, cA0, cD0 := v5Scale(fi.qscale0, fi.qscale1, fi.qscale2)
	yA1, yD1, cA1, cD1 := v5Scale(q, fi.qscale1, fi.qscale2)
	if yD1 <= 0 || cD1 <= 0 || yD0 <= 0 || cD0 <= 0 {
		return nil, fmt.Errorf("frame without quantisation (qscale1 %d, qscale2 %d)", fi.qscale1, fi.qscale2)
	}
	// [DC, AC] ratio of old to new steps, brightness then colour.
	ratio := [2][2]float64{{yD0 / yD1, yA0 / yA1}, {cD0 / cD1, cA0 / cA1}}
	// Zero-bin threshold by scan position.
	var zy, zc [144]float64
	for p, n := range v5YScan {
		zy[p] = 0.5 + dz + tilt*v5TiltWeight(n, 12)
	}
	for p, n := range v5CScan {
		zc[p] = 0.5 + dz + tilt*v5TiltWeight(n, 6)
	}
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
		_, _, mbw, mbh := fi.v5Tile(c, r)
		var prevIn, prevOut [3]int64
		var lv [144]int32
		for mb := 0; mb < mbw*mbh; mb++ {
			for b := 0; b < 4; b++ {
				split, p, rt, zb := &v5YSplit, 0, &ratio[0], &zy
				if b >= 2 {
					split, p, rt, zb = &v5CSplit, b-1, &ratio[1], &zc
				}
				last, err := v5ReadBlock(&rd, split, &prevIn[p], lv[:split[3]])
				if err != nil {
					errs[t] = fmt.Errorf("tile %d,%d: %w", c, r, err)
					return
				}
				lv[0] = int32(math.RoundToEven(float64(lv[0]) * rt[0])) // unbiased on ties
				for k := 1; k <= last; k++ {
					v := lv[k]
					if v == 0 {
						continue
					}
					x := math.Abs(float64(v)) * rt[1]
					a := int32(0)
					if x >= zb[k] {
						a = int32(min(int(math.Ceil(x-0.5-dz)), 4095)) // ties toward zero
					}
					if v < 0 {
						a = -a
					}
					lv[k] = a
				}
				v5WriteBlock(&w, split, &prevOut[p], lv[:split[3]])
				clear(lv[:last+1])
			}
			w[0].put(rd[0].get(16), 16)
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
