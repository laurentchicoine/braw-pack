// Package core implements the Blackmagic RAW bitstream handling used by braw-pack.
//
// The bitstream layout and code tables follow the public FFmpeg BRAW decoder patches
// (Paul B Mahol 2019, Lynne 2025; LGPL-2.1-or-later).
package core

import (
	"encoding/binary"
	"fmt"
	"math"
)

var be = binary.BigEndian

// ---------- entropy tables ----------

type vlcEntry struct {
	sym  int16
	bits uint8
}

const dcLookupBits = 13
const acLookupBits = 18

// acFast resolves the AC codes of up to acFastBits bits from a table small enough to stay in the
// CPU cache; an entry with bits == 0 means the code is longer and acLookup must be used.
const acFastBits = 11

var dcLookup [1 << dcLookupBits]vlcEntry
var acLookup [1 << acLookupBits]vlcEntry
var acFast [1 << acFastBits]vlcEntry
var acEncCode [16][13]uint32
var acEncBits [16][13]uint8
var eobCode, zrlCode uint32
var eobBits, zrlBits uint8

func init() {
	fill := func(tab []vlcEntry, lookupBits int, codes []uint32, bits []uint8) {
		for i := range tab {
			tab[i] = vlcEntry{-1, 0}
		}
		for i, c := range codes {
			b := int(bits[i])
			base := int(c) << (lookupBits - b)
			for j := 0; j < 1<<(lookupBits-b); j++ {
				tab[base+j] = vlcEntry{int16(i), uint8(b)}
			}
		}
	}
	fill(dcLookup[:], dcLookupBits, dcCodes[:], dcBits[:])
	fill(acLookup[:], acLookupBits, acCodes[:], acBits[:])
	for i := range acFast {
		if e := acLookup[i<<(acLookupBits-acFastBits)]; e.sym >= 0 && e.bits <= acFastBits {
			acFast[i] = e
		}
	}
	for i, e := range acTable {
		switch {
		case e[0] == 255:
			eobCode, eobBits = acCodes[i], acBits[i]
		case e[1] == 0:
			zrlCode, zrlBits = acCodes[i], acBits[i]
		default:
			acEncCode[e[0]][e[1]] = acCodes[i]
			acEncBits[e[0]][e[1]] = acBits[i]
		}
	}
}

// ---------- bit io ----------

type bitReader struct {
	data []byte
	pos  int // in bits
}

// peek returns the next n (<=32) bits without consuming; zero-filled past the end.
func (r *bitReader) peek(n int) uint32 {
	byteIdx := r.pos >> 3
	if byteIdx+8 <= len(r.data) {
		return uint32(be.Uint64(r.data[byteIdx:]) << uint(r.pos&7) >> (64 - uint(n)))
	}
	var v uint64
	for i := 0; i < 8; i++ {
		v <<= 8
		if byteIdx+i < len(r.data) {
			v |= uint64(r.data[byteIdx+i])
		}
	}
	v <<= uint(r.pos & 7)
	return uint32(v >> (64 - uint(n)))
}

func (r *bitReader) get(n int) uint32 {
	if n == 0 {
		return 0
	}
	v := r.peek(n)
	r.pos += n
	return v
}

type bitWriter struct {
	buf  []byte
	acc  uint64
	nacc uint
}

func (w *bitWriter) put(v uint32, n uint8) {
	w.acc = w.acc<<n | uint64(v)
	w.nacc += uint(n)
	for w.nacc >= 8 {
		w.nacc -= 8
		w.buf = append(w.buf, byte(w.acc>>w.nacc))
	}
}

func (w *bitWriter) flush() {
	if w.nacc > 0 {
		w.buf = append(w.buf, byte(w.acc<<(8-w.nacc)))
		w.nacc = 0
	}
	w.acc = 0
}

// ---------- frame ----------

var zigzag = [64]int{0, 1, 8, 16, 9, 2, 3, 10, 17, 24, 32, 25, 18, 11, 4, 5,
	12, 19, 26, 33, 40, 48, 41, 34, 27, 20, 13, 6, 7, 14, 21, 28,
	35, 42, 49, 56, 57, 50, 43, 36, 29, 22, 15, 23, 30, 37, 44, 51,
	58, 59, 52, 45, 38, 31, 39, 46, 53, 60, 61, 54, 47, 55, 62, 63}
var halfScan = [32]int{0, 1, 2, 3, 8, 9, 16, 17, 10, 11, 4, 5, 6, 7, 12, 13, 18,
	19, 24, 25, 26, 27, 20, 21, 14, 15, 22, 23, 28, 29, 30, 31}

// requantTile decodes one tile and re-emits it with AC levels scaled by the given ratios.
func requantTile(data []byte, nmb int, rl *[64]float64, rc *[32]float64, dz float64, w *bitWriter) error {
	r := bitReader{data: data}
	total := len(data) * 8
	for mb := 0; mb < nmb; mb++ {
		for b := 0; b < 4; b++ {
			// DC: copied verbatim
			e := dcLookup[r.peek(dcLookupBits)]
			if e.sym < 0 {
				return fmt.Errorf("bad DC code at bit %d", r.pos)
			}
			d := dcTable[e.sym]
			n := int(e.bits) + int(d[1]) + int(d[0])
			w.put(r.get(n), uint8(n))
			maxc := 64
			if b >= 2 {
				maxc = 32
			}
			i, last := 0, 0
			for {
				e := acLookup[r.peek(acLookupBits)]
				if e.sym < 0 {
					return fmt.Errorf("bad AC code at bit %d", r.pos)
				}
				r.pos += int(e.bits)
				run, ln := int(acTable[e.sym][0]), int(acTable[e.sym][1])
				if run == 255 {
					break
				}
				i += 1 + run
				if ln == 0 {
					continue
				}
				if i >= maxc {
					return fmt.Errorf("coefficient index %d out of range", i)
				}
				val := int(r.get(ln))
				neg := val < 1<<(ln-1)
				if neg {
					val = (1<<ln - 1) - val
				}
				var ratio float64
				if b < 2 {
					ratio = rl[i]
				} else {
					ratio = rc[i]
				}
				a := int(math.Ceil(float64(val)*ratio - 0.5 - dz)) // ties round toward zero
				if a <= 0 {
					continue
				}
				zr := i - last - 1
				for zr > 15 {
					w.put(zrlCode, zrlBits)
					zr -= 16
				}
				nb := 0
				for t := a; t > 0; t >>= 1 {
					nb++
				}
				if nb > 12 {
					return fmt.Errorf("level too large")
				}
				w.put(acEncCode[zr][nb], acEncBits[zr][nb])
				if neg {
					w.put(uint32((1<<nb-1)-a), uint8(nb))
				} else {
					w.put(uint32(a), uint8(nb))
				}
				last = i
			}
			w.put(eobCode, eobBits)
		}
		w.put(r.get(8), 8) // per-macroblock side byte, meaning unknown: kept as is
		if r.pos > total {
			return fmt.Errorf("tile overrun")
		}
	}
	w.flush()
	return nil
}

// tiltWeight says how strongly a tilted requantisation scales each table entry: 0 for the lowest
// frequencies up to 1 for the highest. Entries 0..63 are 8x8 brightness blocks, 64..95 are 8x4
// colour blocks.
func tiltWeight(i int) float64 {
	if i < 64 {
		return float64(i/8+i%8) / 14
	}
	i -= 64
	return float64(2*(i/8)+i%8) / 13
}

func requantFrame(fr []byte, k, dz float64) ([]byte, error) { return requantTilt(fr, k, 0, dz) }

// requantTilt re-emits a frame with its AC quantisation entries multiplied by k + tilt*weight,
// where weight grows with frequency, so a tilt removes the finest detail first.
func requantTilt(fr []byte, k, tilt, dz float64) ([]byte, error) {
	// parseFrame checks the header, so the fields read below are known to lie inside the frame.
	if _, err := parseFrame(fr); err != nil {
		return nil, err
	}
	hs := 0x100
	ntw, nth := int(fr[hs+10]), int(fr[hs+11])
	height := int(be.Uint16(fr[hs+14:]))
	tileH := int(be.Uint16(fr[hs+16:]))
	tileW := make([]int, ntw)
	for x := range tileW {
		tileW[x] = int(be.Uint16(fr[hs+0x40+2*x:]))
	}
	n := ntw * nth
	offs := make([]int, n+1)
	for i := 0; i < n; i++ {
		offs[i] = int(be.Uint32(fr[0x180+4*i:]))
	}
	offs[n] = len(fr) - hs
	var q0, q1 [96]int
	for i := range q0 {
		q0[i] = int(be.Uint16(fr[0x1180+2*i:]))
		q1[i] = q0[i]
		if i != 0 && i != 64 {
			v := int(float64(q0[i])*(k+tilt*tiltWeight(i)) + 0.5)
			if v < 1 {
				v = 1
			}
			if v > 65535 {
				v = 65535
			}
			q1[i] = v
		}
	}
	var rl [64]float64
	var rc [32]float64
	for i := 1; i < 64; i++ {
		rl[i] = float64(q0[zigzag[i]]) / float64(q1[zigzag[i]])
	}
	for i := 1; i < 32; i++ {
		rc[i] = float64(q0[64+halfScan[i]]) / float64(q1[64+halfScan[i]])
	}
	first := offs[0] + hs
	w := bitWriter{buf: make([]byte, 0, len(fr))}
	w.buf = append(w.buf, fr[:first]...)
	for i := range q1 {
		be.PutUint16(w.buf[0x1180+2*i:], uint16(q1[i]))
	}
	newOffs := make([]int, n)
	pad := func(m int) {
		for (len(w.buf)-hs)%m != 0 {
			w.buf = append(w.buf, 0)
		}
	}
	for i := 0; i < n; i++ {
		x, y := i/nth, i%nth
		start, end := offs[i]+hs, offs[i+1]+hs
		if start >= end || end > len(fr) {
			return nil, fmt.Errorf("bad tile offsets")
		}
		bw := tileW[x] / 16
		bh := tileH / 8
		if y == nth-1 {
			bh = (height - y*tileH) / 8
		}
		if y == 0 {
			pad(512) // camera aligns each tile column to 512 bytes
		}
		newOffs[i] = len(w.buf) - hs
		if err := requantTile(fr[start:end], bw*bh, &rl, &rc, dz, &w); err != nil {
			return nil, fmt.Errorf("tile %d,%d: %w", x, y, err)
		}
		pad(8)
	}
	pad(512) // camera pads the picture part to 512 bytes
	out := w.buf
	for i, o := range newOffs {
		be.PutUint32(out[0x180+4*i:], uint32(o))
	}
	be.PutUint32(out[hs+4:], uint32(len(out)-hs))
	return out, nil
}

// ---------- container ----------

type chunk struct {
	off  int64
	size int64
}

type track struct {
	stsz, co64, stsc int // positions inside moov
	mdhd, stts       int
	n                int
	fixed            uint32
	sizes            []uint32
	offs             []int64
	spc              []int
	chunks           []chunk
}

func walk(buf []byte, start, end int, depth int, tracks *[]*track) error {
	containers := map[string]bool{"moov": true, "trak": true, "mdia": true, "minf": true, "stbl": true}
	if depth > 8 {
		return fmt.Errorf("atoms nested too deeply")
	}
	pos := start
	for pos+8 <= end {
		size := int(be.Uint32(buf[pos:]))
		typ := string(buf[pos+4 : pos+8])
		if size < 8 || pos+size > end {
			return fmt.Errorf("bad atom %q at %d", typ, pos)
		}
		switch typ {
		case "trak":
			*tracks = append(*tracks, &track{})
		case "stsz", "co64", "stsc", "mdhd", "stts":
			if len(*tracks) == 0 {
				return fmt.Errorf("atom %q outside a track", typ)
			}
			t := (*tracks)[len(*tracks)-1]
			switch typ {
			case "stsz":
				t.stsz = pos
			case "co64":
				t.co64 = pos
			case "stsc":
				t.stsc = pos
			case "mdhd":
				t.mdhd = pos
			case "stts":
				t.stts = pos
			}
		case "stco":
			return fmt.Errorf("32-bit chunk offsets not supported")
		}
		if containers[typ] {
			if err := walk(buf, pos+8, pos+size, depth+1, tracks); err != nil {
				return err
			}
		}
		pos += size
	}
	return nil
}

func parseMoov(moov []byte) ([]*track, error) {
	var tracks []*track
	if err := walk(moov, 0, len(moov), 0, &tracks); err != nil {
		return nil, err
	}
	for _, t := range tracks {
		if t.stsz == 0 || t.co64 == 0 || t.stsc == 0 {
			return nil, fmt.Errorf("track without sample tables")
		}
		// Every table must fit inside its own atom (walk made sure the atom fits inside moov).
		size := func(p int) int { return int(be.Uint32(moov[p:])) }
		p := t.stsz
		if size(p) < 20 {
			return nil, fmt.Errorf("bad sample size table")
		}
		t.fixed = be.Uint32(moov[p+12:])
		t.n = int(be.Uint32(moov[p+16:]))
		if t.fixed == 0 {
			if size(p) < 20+4*t.n {
				return nil, fmt.Errorf("bad sample size table")
			}
			t.sizes = make([]uint32, t.n)
			for i := range t.sizes {
				t.sizes[i] = be.Uint32(moov[p+20+4*i:])
			}
		}
		p = t.co64
		if size(p) < 16 {
			return nil, fmt.Errorf("bad chunk offset table")
		}
		nc := int(be.Uint32(moov[p+12:]))
		if size(p) < 16+8*nc {
			return nil, fmt.Errorf("bad chunk offset table")
		}
		t.offs = make([]int64, nc)
		for i := range t.offs {
			t.offs[i] = int64(be.Uint64(moov[p+16+8*i:]))
		}
		p = t.stsc
		if size(p) < 16 {
			return nil, fmt.Errorf("bad sample-to-chunk table")
		}
		ne := int(be.Uint32(moov[p+12:]))
		if size(p) < 16+12*ne {
			return nil, fmt.Errorf("bad sample-to-chunk table")
		}
		t.spc = make([]int, 0, nc)
		for i := 0; i < ne; i++ {
			first := int(be.Uint32(moov[p+16+12*i:]))
			per := int(be.Uint32(moov[p+20+12*i:]))
			last := nc
			if i+1 < ne {
				last = int(be.Uint32(moov[p+16+12*(i+1):])) - 1
			}
			for c := first; c <= last; c++ {
				if len(t.spc) == nc {
					return nil, fmt.Errorf("stsc mismatch")
				}
				t.spc = append(t.spc, per)
			}
		}
		if len(t.spc) != nc {
			return nil, fmt.Errorf("stsc mismatch")
		}
		s := 0
		for c := 0; c < nc; c++ {
			if t.spc[c] > t.n-s {
				return nil, fmt.Errorf("stsc sample count mismatch")
			}
			var sz int64
			if t.fixed != 0 {
				sz = int64(t.fixed) * int64(t.spc[c])
			} else {
				for j := 0; j < t.spc[c]; j++ {
					sz += int64(t.sizes[s+j])
				}
			}
			t.chunks = append(t.chunks, chunk{t.offs[c], sz})
			s += t.spc[c]
		}
		if s != t.n {
			return nil, fmt.Errorf("stsc sample count mismatch")
		}
	}
	return tracks, nil
}
