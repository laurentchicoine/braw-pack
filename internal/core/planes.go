// SPDX-License-Identifier: LGPL-2.1-or-later
// Copyright (c) 2019 Paul B Mahol
// Copyright (c) 2025 Lynne
// Copyright (c) 2026 Laurent Chicoine

package core

// Sensor-space planes: BRAW stores, per 16x8 macroblock, two 8x8 "luma" blocks and two 8x4
// "chroma" blocks. Undoing only the DCT gives one full-resolution plane and two half-resolution
// planes (4:2:0 geometry, 12-bit) that are still in sensor space: no white balance, ISO or
// colour science applied. extract writes them as yuv420p12le; restore rebuilds a .braw from them.

import (
	"fmt"
	"math"
)

type frameInfo struct {
	hs, ntw, nth, w, h, tileH int
	tileW, tileX              []int
	offs                      []int
	qscale0, qscale1, qscale2 int
	table                     [96]int
}

func parseFrame(fr []byte) (*frameInfo, error) {
	if len(fr) < 0x1300 || string(fr[4:8]) != "bmdf" {
		return nil, fmt.Errorf("not a braw frame")
	}
	fi := &frameInfo{hs: int(be.Uint32(fr[0:4]))}
	hs := fi.hs
	if hs != 0x100 || string(fr[hs:hs+4]) != "braw" {
		return nil, fmt.Errorf("unexpected frame header")
	}
	if v := fr[hs+8]; v != 1 && v != 2 {
		return nil, fmt.Errorf("unsupported braw version %d", v)
	}
	fi.qscale0 = int(fr[hs+9])
	fi.ntw, fi.nth = int(fr[hs+10]), int(fr[hs+11])
	fi.w, fi.h = int(be.Uint16(fr[hs+12:])), int(be.Uint16(fr[hs+14:]))
	fi.tileH = int(be.Uint16(fr[hs+16:]))
	fi.qscale1, fi.qscale2 = int(be.Uint16(fr[hs+0x20:])), int(be.Uint16(fr[hs+0x22:]))
	x := 0
	for i := 0; i < fi.ntw; i++ {
		tw := int(be.Uint16(fr[hs+0x40+2*i:]))
		fi.tileW = append(fi.tileW, tw)
		fi.tileX = append(fi.tileX, x)
		x += tw
	}
	if x != fi.w || fi.w%16 != 0 || fi.h%8 != 0 {
		return nil, fmt.Errorf("unexpected geometry %dx%d", fi.w, fi.h)
	}
	// Every row of tiles but the last is tileH high, and the last must have something left.
	if fi.tileH <= 0 || (fi.nth-1)*fi.tileH >= fi.h {
		return nil, fmt.Errorf("unexpected tile layout")
	}
	n := fi.ntw * fi.nth
	if len(fr) < 0x180+4*n {
		return nil, fmt.Errorf("frame too short for its tile table")
	}
	fi.offs = make([]int, n+1)
	for i := 0; i < n; i++ {
		fi.offs[i] = int(be.Uint32(fr[0x180+4*i:]))
	}
	fi.offs[n] = len(fr) - hs
	// The first tile follows the tile table and the quantisation table.
	if first := fi.offs[0] + hs; first < 0x180+4*n || first < 0x1240 || first > len(fr) {
		return nil, fmt.Errorf("bad tile offsets")
	}
	for i := range fi.table {
		fi.table[i] = int(be.Uint16(fr[0x1180+2*i:]))
	}
	return fi, nil
}

// tile returns geometry of tile i (file order is column-major).
func (fi *frameInfo) tile(i int) (px, py, bw, bh int) {
	x, y := i/fi.nth, i%fi.nth
	bw = fi.tileW[x] / 16
	bh = fi.tileH / 8
	if y == fi.nth-1 {
		bh = (fi.h - y*fi.tileH) / 8
	}
	return fi.tileX[x], y * fi.tileH, bw, bh
}

// quantArrays gives the 16.16 fixed-point dequantisers the decoder derives from a stored table.
func (fi *frameInfo) quantArrays(table *[96]int) (ql [64]int64, qc [32]int64) {
	sl := int64(fi.qscale0+4) * 2048
	for n := 0; n < 64; n++ {
		ql[n] = int64(table[n]) * sl
	}
	if ql[0] <= int64(fi.qscale1) {
		ql[0] = int64(fi.qscale1)
	}
	sc := int64(math.Max(4, math.Floor(float64(fi.qscale0+4)*0.5+0.5+0.5))) * 2048
	for n := 0; n < 32; n++ {
		qc[n] = int64(table[64+n]) * sc
	}
	if qc[0] <= int64(fi.qscale2) {
		qc[0] = int64(fi.qscale2)
	}
	return
}

// ---------- transforms (orthonormal 8x8; 8x4 carries an extra sqrt(2), as in the decoder) ----------

var cos8 [8][8]float64 // [k][n]
var cos4 [4][4]float64

func init() {
	for k := 0; k < 8; k++ {
		a := 0.5
		if k == 0 {
			a = 1 / math.Sqrt(8)
		}
		for n := 0; n < 8; n++ {
			cos8[k][n] = a * math.Cos(float64(2*n+1)*float64(k)*math.Pi/16)
		}
	}
	for k := 0; k < 4; k++ {
		a := 1 / math.Sqrt(2)
		if k == 0 {
			a = 0.5
		}
		for n := 0; n < 4; n++ {
			cos4[k][n] = a * math.Cos(float64(2*n+1)*float64(k)*math.Pi/8)
		}
	}
}

// Scaled cosines for the fast transforms: c[k] = cos(k*pi/16)/2, g = cos(k*pi/8)/sqrt(2).
var (
	fr8                          = 1 / math.Sqrt(8)
	fc1, fc2, fc3, fc5, fc6, fc7 = fcos(1), fcos(2), fcos(3), fcos(5), fcos(6), fcos(7)
	fg1, fg3                     = math.Cos(math.Pi/8) / math.Sqrt2, math.Cos(3*math.Pi/8) / math.Sqrt2
)

func fcos(k int) float64 { return 0.5 * math.Cos(float64(k)*math.Pi/16) }

// idct8 is the 8-point inverse transform of in[0], in[s], ... written to out[0], out[s], ...
func idct8(in, out []float64, s int) {
	x0, x1, x2, x3 := in[0], in[s], in[2*s], in[3*s]
	x4, x5, x6, x7 := in[4*s], in[5*s], in[6*s], in[7*s]
	t0, t1 := (x0+x4)*fr8, (x0-x4)*fr8
	t2, t3 := x2*fc2+x6*fc6, x2*fc6-x6*fc2
	e0, e1, e2, e3 := t0+t2, t1+t3, t1-t3, t0-t2
	o0 := x1*fc1 + x3*fc3 + x5*fc5 + x7*fc7
	o1 := x1*fc3 - x3*fc7 - x5*fc1 - x7*fc5
	o2 := x1*fc5 - x3*fc1 + x5*fc7 + x7*fc3
	o3 := x1*fc7 - x3*fc5 + x5*fc3 - x7*fc1
	out[0], out[7*s] = e0+o0, e0-o0
	out[s], out[6*s] = e1+o1, e1-o1
	out[2*s], out[5*s] = e2+o2, e2-o2
	out[3*s], out[4*s] = e3+o3, e3-o3
}

func fdct8(in, out []float64, s int) {
	s0, d0 := in[0]+in[7*s], in[0]-in[7*s]
	s1, d1 := in[s]+in[6*s], in[s]-in[6*s]
	s2, d2 := in[2*s]+in[5*s], in[2*s]-in[5*s]
	s3, d3 := in[3*s]+in[4*s], in[3*s]-in[4*s]
	a, b := s0-s3, s1-s2
	out[0] = (s0 + s1 + s2 + s3) * fr8
	out[4*s] = (s0 - s1 - s2 + s3) * fr8
	out[2*s] = a*fc2 + b*fc6
	out[6*s] = a*fc6 - b*fc2
	out[s] = d0*fc1 + d1*fc3 + d2*fc5 + d3*fc7
	out[3*s] = d0*fc3 - d1*fc7 - d2*fc1 - d3*fc5
	out[5*s] = d0*fc5 - d1*fc1 + d2*fc7 + d3*fc3
	out[7*s] = d0*fc7 - d1*fc5 + d2*fc3 - d3*fc1
}

// idct: F[v*8+u] -> f[y*8+x], rows = number of vertical samples (8 or 4).
func idct(F, f []float64, rows int) {
	mask := uint(0)
	for v := 0; v < rows; v++ {
		r := F[v*8 : v*8+8]
		if r[0] != 0 || r[1] != 0 || r[2] != 0 || r[3] != 0 || r[4] != 0 || r[5] != 0 || r[6] != 0 || r[7] != 0 {
			mask |= 1 << uint(v)
		}
	}
	idctRows(F, f, rows, mask)
}

// idctRows is idct for a block whose coefficient rows outside mask (bit v = row v) are all zero.
// Camera footage leaves most rows empty, so skipping them is where the time is saved.
func idctRows(F, f []float64, rows int, mask uint) {
	if mask <= 1 {
		// Only the first coefficient row: every output row is the same.
		var t [8]float64
		idct8(F[:8], t[:], 1)
		k := fr8
		if rows == 4 {
			k = 0.5 * math.Sqrt2
		}
		for x := range t {
			t[x] *= k
		}
		for y := 0; y < rows; y++ {
			copy(f[y*8:y*8+8], t[:])
		}
		return
	}
	var tmp [64]float64
	for v := 0; v < rows; v++ {
		if mask&(1<<uint(v)) != 0 {
			idct8(F[v*8:v*8+8], tmp[v*8:v*8+8], 1)
		}
	}
	if rows == 8 {
		for x := 0; x < 8; x++ {
			idct8(tmp[x:], f[x:], 8)
		}
		return
	}
	for x := 0; x < 8; x++ {
		x0, x1, x2, x3 := tmp[x], tmp[8+x], tmp[16+x], tmp[24+x]
		e0, e1 := (x0+x2)*0.5, (x0-x2)*0.5
		o0, o1 := x1*fg1+x3*fg3, x1*fg3-x3*fg1
		f[x] = (e0 + o0) * math.Sqrt2
		f[8+x] = (e1 + o1) * math.Sqrt2
		f[16+x] = (e1 - o1) * math.Sqrt2
		f[24+x] = (e0 - o0) * math.Sqrt2
	}
}

func fdct(f, F []float64, rows int) {
	var tmp [64]float64
	for y := 0; y < rows; y++ {
		fdct8(f[y*8:y*8+8], tmp[y*8:y*8+8], 1)
	}
	if rows == 8 {
		for u := 0; u < 8; u++ {
			fdct8(tmp[u:], F[u:], 8)
		}
		return
	}
	for u := 0; u < 8; u++ {
		x0, x1, x2, x3 := tmp[u], tmp[8+u], tmp[16+u], tmp[24+u]
		s0, s1, d0, d1 := x0+x3, x1+x2, x0-x3, x1-x2
		F[u] = (s0 + s1) * 0.5 / math.Sqrt2
		F[16+u] = (s0 - s1) * 0.5 / math.Sqrt2
		F[8+u] = (d0*fg1 + d1*fg3) / math.Sqrt2
		F[24+u] = (d0*fg3 - d1*fg1) / math.Sqrt2
	}
}

// idctRef and fdctRef are the plain matrix forms, kept as the reference for the fast versions.
func idctRef(F, f []float64, rows int) {
	var tmp [64]float64
	for v := 0; v < rows; v++ {
		for x := 0; x < 8; x++ {
			s := 0.0
			for u := 0; u < 8; u++ {
				s += F[v*8+u] * cos8[u][x]
			}
			tmp[v*8+x] = s
		}
	}
	for x := 0; x < 8; x++ {
		for y := 0; y < rows; y++ {
			s := 0.0
			if rows == 8 {
				for v := 0; v < 8; v++ {
					s += tmp[v*8+x] * cos8[v][y]
				}
			} else {
				for v := 0; v < 4; v++ {
					s += tmp[v*8+x] * cos4[v][y]
				}
				s *= math.Sqrt2
			}
			f[y*8+x] = s
		}
	}
}

func fdctRef(f, F []float64, rows int) {
	var tmp [64]float64
	for y := 0; y < rows; y++ {
		for u := 0; u < 8; u++ {
			s := 0.0
			for x := 0; x < 8; x++ {
				s += f[y*8+x] * cos8[u][x]
			}
			tmp[y*8+u] = s
		}
	}
	for u := 0; u < 8; u++ {
		for v := 0; v < rows; v++ {
			s := 0.0
			if rows == 8 {
				for y := 0; y < 8; y++ {
					s += tmp[y*8+u] * cos8[v][y]
				}
			} else {
				for y := 0; y < 4; y++ {
					s += tmp[y*8+u] * cos4[v][y]
				}
				s /= math.Sqrt2
			}
			F[v*8+u] = s
		}
	}
}

// ---------- decode to planes ----------

type planeStats struct {
	lumaOut, chromaOut int64   // samples outside 0..4095 before clipping
	maxChroma          float64 // largest colour-plane deviation from mid-scale, before scaling
}

var dcvalTab = func() (t [2][16]int64) {
	for i := 0; i < 15; i++ {
		t[0][i] = -(int64(1)<<(i+1) - 1)
		t[1][i] = int64(1) << i
	}
	return
}()

// put12 stores v rounded to the nearest 12-bit value, counting samples that had to be clipped.
func put12(dst *uint16, v float64, out *int64) {
	if v < -0.5 {
		*dst = 0
		*out++
		return
	}
	r := int(v + 0.5)
	if r > 4095 {
		r = 4095
		*out++
	}
	*dst = uint16(r)
}

// Which planes decodePlanes should produce. Skipping a plane skips its inverse transform; the
// bitstream is still parsed in full.
const (
	planesLuma   = 1
	planesChroma = 2
	planesAll    = planesLuma | planesChroma
)

// decodePlanes undoes entropy coding and the DCT. It returns Y (w*h), U and V (w/2*h/2). cs scales
// the colour planes around mid-scale so that colour differences beyond the 12-bit range are not
// clipped (1 = no scaling). side receives the per-macroblock side byte in tile order.
func decodePlanes(fr []byte, cs float64, what int) (fi *frameInfo, Y, U, V []uint16, side []byte, st planeStats, err error) {
	fi, err = parseFrame(fr)
	if err != nil {
		return
	}
	w, h := fi.w, fi.h
	cw := w / 2
	luma, chroma := what&planesLuma != 0, what&planesChroma != 0
	if luma {
		Y = make([]uint16, w*h)
	}
	if chroma {
		U = make([]uint16, cw*h/2)
		V = make([]uint16, cw*h/2)
	}
	side = make([]byte, 0, w*h/128)
	ql, qc := fi.quantArrays(&fi.table)
	var F, f [64]float64
	var cmin, cmax float64
	defer func() { st.maxChroma = math.Max(cmax, -cmin) }()
	for ti := 0; ti < fi.ntw*fi.nth; ti++ {
		tx, ty, bw, bh := fi.tile(ti)
		start, end := fi.offs[ti]+fi.hs, fi.offs[ti+1]+fi.hs
		if start >= end || end > len(fr) {
			err = fmt.Errorf("bad tile offsets")
			return
		}
		r := bitReader{data: fr[start:end]}
		var prev [3]int64
		for my := 0; my < bh; my++ {
			for mx := 0; mx < bw; mx++ {
				px, py := tx+mx*16, ty+my*8
				for b := 0; b < 4; b++ {
					maxc, pi, add := 64, 0, int64(16384)
					if b >= 2 {
						maxc, pi, add = 32, b-1, 8192
					}
					want := luma
					if b >= 2 {
						want = chroma
					}
					mask := uint(1)
					e := dcLookup[r.peek(dcLookupBits)]
					if e.sym < 0 {
						err = fmt.Errorf("bad DC code")
						return
					}
					r.pos += int(e.bits)
					d := dcTable[e.sym]
					sgn := r.get(int(d[1]))
					code := int64(r.get(int(d[0])))
					val := code + prev[pi] + dcvalTab[sgn][d[2]]
					prev[pi] = val
					var q0 int64
					if b < 2 {
						q0 = ql[0]
					} else {
						q0 = qc[0]
					}
					dc := (val*q0+0x8000)>>16 + add
					if dc > 32767 {
						dc = 32767
					}
					F[0] = float64(dc)
					i := 0
					for {
						bitsAhead := r.peek(acLookupBits)
						e := acFast[bitsAhead>>(acLookupBits-acFastBits)]
						if e.bits == 0 {
							e = acLookup[bitsAhead]
							if e.sym < 0 {
								err = fmt.Errorf("bad AC code")
								return
							}
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
							err = fmt.Errorf("coefficient index out of range")
							return
						}
						v := int64(r.get(ln))
						if v < 1<<(ln-1) {
							v -= 1<<ln - 1
						}
						if !want {
							continue
						}
						if b < 2 {
							z := zigzag[i]
							F[z] = float64((v*ql[z] + 0x8000) >> 16)
							mask |= 1 << uint(z>>3)
						} else {
							z := halfScan[i]
							F[z] = float64((v*qc[z] + 0x8000) >> 16)
							mask |= 1 << uint(z>>3)
						}
					}
					if !want {
						continue
					}
					if b < 2 {
						idctRows(F[:], f[:], 8, mask)
						ox := px + b*8
						for y := 0; y < 8; y++ {
							row := Y[(py+y)*w+ox:]
							row = row[:8]
							src := f[y*8 : y*8+8]
							for x, v := range src {
								if v >= -0.5 && v < 4095.5 {
									row[x] = uint16(v + 0.5)
								} else {
									put12(&row[x], v, &st.lumaOut)
								}
							}
						}
					} else {
						idctRows(F[:], f[:], 4, mask)
						dst := U
						if b == 3 {
							dst = V
						}
						for y := 0; y < 4; y++ {
							row := dst[(py/2+y)*cw+px/2:]
							row = row[:8]
							src := f[y*8 : y*8+8]
							for x, v := range src {
								d := v - 2048
								if d > cmax {
									cmax = d
								} else if d < cmin {
									cmin = d
								}
								if c := 2048 + d*cs; c >= -0.5 && c < 4095.5 {
									row[x] = uint16(c + 0.5)
								} else {
									put12(&row[x], c, &st.chromaOut)
								}
							}
						}
					}
					// F must be all zero again for the next block.
					for v := uint(0); mask != 0; v, mask = v+1, mask>>1 {
						if mask&1 != 0 {
							*(*[8]float64)(F[v*8:]) = [8]float64{}
						}
					}
				}
				side = append(side, byte(r.get(8)))
			}
		}
		if r.pos > len(r.data)*8 {
			err = fmt.Errorf("tile overrun")
			return
		}
	}
	return
}

// ---------- encode from planes ----------

func bitLen(a int64) int {
	n := 0
	for ; a > 0; a >>= 1 {
		n++
	}
	return n
}

// encodePlanes builds a frame from planes. hdr is the frame's original bytes up to the first tile
// (camera metadata, picture header, quantisation table); side holds the per-macroblock side bytes
// in tile order; cs is the colour scale the planes were stored with; dz widens the zero bin of
// the AC quantiser (0 = round to nearest).
func encodePlanes(hdr []byte, side []byte, Y, U, V []uint16, cs, dz float64) ([]byte, error) {
	fi, err := parseFrame(hdr)
	if err != nil {
		return nil, err
	}
	w, hs := fi.w, fi.hs
	cw := w / 2
	if len(Y) != w*fi.h || len(U) != cw*fi.h/2 || len(V) != cw*fi.h/2 {
		return nil, fmt.Errorf("picture size does not match the frame header (%dx%d)", w, fi.h)
	}
	table := fi.table
	ql, qc := fi.quantArrays(&table)
	var rl [64]float64 // reciprocal steps, in scan order
	var rc [32]float64
	for i := range rl {
		rl[i] = 65536 / float64(ql[zigzag[i]])
	}
	for i := range rc {
		rc[i] = 65536 / float64(qc[halfScan[i]])
	}
	zero := 0.5 + dz // magnitudes below this quantise to nothing
	first := fi.offs[0] + hs
	if len(hdr) != first {
		return nil, fmt.Errorf("frame header is %d bytes, expected %d", len(hdr), first)
	}
	bwr := bitWriter{buf: make([]byte, 0, len(hdr)+len(Y)/4)}
	bwr.buf = append(bwr.buf, hdr...)
	sp := 0
	pad := func(m int) {
		for (len(bwr.buf)-hs)%m != 0 {
			bwr.buf = append(bwr.buf, 0)
		}
	}
	n := fi.ntw * fi.nth
	newOffs := make([]int, n)
	var F, f [64]float64
	for ti := 0; ti < n; ti++ {
		tx, ty, bw, bh := fi.tile(ti)
		if sp+bw*bh > len(side) {
			return nil, fmt.Errorf("side data too short")
		}
		if ti%fi.nth == 0 {
			pad(512)
		}
		newOffs[ti] = len(bwr.buf) - hs
		var prev [3]int64
		for my := 0; my < bh; my++ {
			for mx := 0; mx < bw; mx++ {
				px, py := tx+mx*16, ty+my*8
				for b := 0; b < 4; b++ {
					maxc, pi, add := 64, 0, 16384.0
					var q0 int64
					if b < 2 {
						ox := px + b*8
						for y := 0; y < 8; y++ {
							row := Y[(py+y)*w+ox:]
							for x := 0; x < 8; x++ {
								f[y*8+x] = float64(row[x])
							}
						}
						fdct(f[:], F[:], 8)
						q0 = ql[0]
					} else {
						maxc, pi, add = 32, b-1, 8192.0
						src := U
						if b == 3 {
							src = V
						}
						for y := 0; y < 4; y++ {
							row := src[(py/2+y)*cw+px/2:]
							for x := 0; x < 8; x++ {
								f[y*8+x] = 2048 + (float64(row[x])-2048)/cs
							}
						}
						fdct(f[:], F[:], 4)
						q0 = qc[0]
					}
					// DC
					val := int64(math.Floor((F[0]-add)*65536/float64(q0) + 0.5))
					diff := val - prev[pi]
					if diff > 32767 {
						diff = 32767
					} else if diff < -32767 {
						diff = -32767
					}
					prev[pi] += diff
					ad := diff
					if ad < 0 {
						ad = -ad
					}
					k := bitLen(ad)
					bwr.put(dcCodes[k], dcBits[k])
					if k > 0 {
						if diff > 0 {
							bwr.put(1, 1)
							bwr.put(uint32(diff-int64(1)<<(k-1)), uint8(k-1))
						} else {
							bwr.put(0, 1)
							bwr.put(uint32(diff+int64(1)<<k-1), uint8(k-1))
						}
					}
					// AC
					last := 0
					for i := 1; i < maxc; i++ {
						var x float64
						if b < 2 {
							x = F[zigzag[i]] * rl[i]
						} else {
							x = F[halfScan[i]] * rc[i]
						}
						// Most coefficients fall in the zero bin.
						if x < zero && x > -zero {
							continue
						}
						neg := x < 0
						if neg {
							x = -x
						}
						a := int(x + 0.5 - dz)
						if a > 4095 {
							a = 4095
						}
						zr := i - last - 1
						for zr > 15 {
							bwr.put(zrlCode, zrlBits)
							zr -= 16
						}
						nb := bitLen(int64(a))
						bwr.put(acEncCode[zr][nb], acEncBits[zr][nb])
						if neg {
							bwr.put(uint32((1<<nb-1)-a), uint8(nb))
						} else {
							bwr.put(uint32(a), uint8(nb))
						}
						last = i
					}
					bwr.put(eobCode, eobBits)
				}
				bwr.put(uint32(side[sp]), 8)
				sp++
			}
		}
		bwr.flush()
		pad(8)
	}
	pad(512)
	out := bwr.buf
	for i, o := range newOffs {
		be.PutUint32(out[0x180+4*i:], uint32(o))
	}
	be.PutUint32(out[hs+4:], uint32(len(out)-hs))
	return out, nil
}

// ---------- sample exchange with ffmpeg ----------

func planeBytes(w, h int) int64 { return int64(w) * int64(h) * 3 }

// pixLayout is how one frame's samples are arranged when exchanged with ffmpeg, always as
// little-endian 16-bit words: the brightness plane first, then colour at half resolution.
type pixLayout int

const (
	planar12 pixLayout = iota // 12-bit values, then all of U, then all of V
	planar10                  // the same with 10-bit values
	// semi10 is the NVIDIA encoder's and decoder's own arrangement (P010): 10-bit values in the
	// top bits of each word, and U and V interleaved in one plane. Exchanging it directly saves
	// ffmpeg a pass over every frame; the values are the same as in planar10.
	semi10
)

// pixFmt is ffmpeg's name for the layout.
func (l pixLayout) pixFmt() string {
	return [...]string{"yuv420p12le", "yuv420p10le", "p010le"}[l]
}

// to10 rounds a 12-bit sample to 10 bits. It is done here, not by ffmpeg, so the stored values
// are fully defined.
func to10(v uint16) uint16 {
	if v = (v + 2) >> 2; v > 1023 {
		return 1023
	}
	return v
}

// packYUV lays the planes out for ffmpeg in the given layout.
func packYUV(Y, U, V []uint16, l pixLayout) []byte {
	out := make([]byte, 2*(len(Y)+len(U)+len(V)))
	if l == semi10 {
		dst := out[:2*len(Y)]
		for i, v := range Y {
			v = to10(v) << 6
			dst[2*i] = byte(v)
			dst[2*i+1] = byte(v >> 8)
		}
		dst = out[2*len(Y):]
		V = V[:len(U)]
		for i, u := range U {
			u, v := to10(u)<<6, to10(V[i])<<6
			dst[4*i] = byte(u)
			dst[4*i+1] = byte(u >> 8)
			dst[4*i+2] = byte(v)
			dst[4*i+3] = byte(v >> 8)
		}
		return out
	}
	p := 0
	for _, pl := range [][]uint16{Y, U, V} {
		if l == planar10 {
			for _, v := range pl {
				v = to10(v)
				out[p] = byte(v)
				out[p+1] = byte(v >> 8)
				p += 2
			}
			continue
		}
		for _, v := range pl {
			out[p] = byte(v)
			out[p+1] = byte(v >> 8)
			p += 2
		}
	}
	return out
}

// unpackYUV is the inverse of packYUV and always returns 12-bit samples.
func unpackYUV(buf []byte, w, h int, l pixLayout) (Y, U, V []uint16) {
	ny, nc := w*h, w*h/4
	if l == semi10 {
		Y = make([]uint16, ny)
		src := buf[:2*ny]
		for i := range Y {
			Y[i] = (uint16(src[2*i]) | uint16(src[2*i+1])<<8) >> 6 << 2
		}
		U, V = make([]uint16, nc), make([]uint16, nc)
		src = buf[2*ny : 2*ny+4*nc]
		V = V[:len(U)]
		for i := range U {
			U[i] = (uint16(src[4*i]) | uint16(src[4*i+1])<<8) >> 6 << 2
			V[i] = (uint16(src[4*i+2]) | uint16(src[4*i+3])<<8) >> 6 << 2
		}
		return Y, U, V
	}
	shift, top := uint(0), uint16(4095)
	if l == planar10 {
		shift, top = 2, 1023
	}
	get := func(off, n int) []uint16 {
		pl := make([]uint16, n)
		src := buf[off : off+2*n]
		for i := range pl {
			v := uint16(src[2*i]) | uint16(src[2*i+1])<<8
			if v > top {
				v = top
			}
			pl[i] = v << shift
		}
		return pl
	}
	return get(0, ny), get(2*ny, nc), get(2*(ny+nc), nc)
}

// scaleQuant returns a copy of a frame header whose stored AC quantisation entries are multiplied
// by k (the two DC entries are left alone). Decoders read the table from the frame, so a frame
// encoded against the scaled table decodes normally, with coarser steps and fewer bits.
func scaleQuant(hdr []byte, k float64) []byte {
	out := append([]byte(nil), hdr...)
	if k == 1 {
		return out
	}
	for i := 0; i < 96; i++ {
		if i == 0 || i == 64 {
			continue
		}
		v := int(float64(be.Uint16(out[0x1180+2*i:]))*k + 0.5)
		if v < 1 {
			v = 1
		}
		if v > 65535 {
			v = 65535
		}
		be.PutUint16(out[0x1180+2*i:], uint16(v))
	}
	return out
}

func psnr12(a, b []uint16) float64 {
	var se float64
	for i, v := range a {
		d := float64(v) - float64(b[i])
		se += d * d
	}
	if se == 0 {
		return 99
	}
	return 10 * math.Log10(4095*4095*float64(len(a))/se)
}

// blockAvg averages a w*h plane over s*s blocks (edges that do not fill a block are dropped).
func blockAvg(p []uint16, w, h, s int) ([]float64, int, int) {
	bw, bh := w/s, h/s
	out := make([]float64, bw*bh)
	for y := 0; y < bh*s; y++ {
		row := p[y*w : y*w+bw*s]
		o := out[(y/s)*bw:]
		for x, v := range row {
			o[x/s] += float64(v)
		}
	}
	k := 1 / float64(s*s)
	for i := range out {
		out[i] *= k
	}
	return out, bw, bh
}

// detailEnergy compares an archive's brightness plane with the source's in a way that mostly
// ignores sensor noise. Both are averaged over 2x2 pixels, which quarters random noise but keeps
// structure. It returns the energy of the archive's error and the energy of the source's fine
// detail (how far each 2x2 average is from the average of its 8x8 neighbourhood), both summed
// over the same samples. 1 - lost/detail is the share of fine detail the archive keeps.
func detailEnergy(src, arc []uint16, w, h int) (lost, detail float64) {
	a, bw, bh := blockAvg(arc, w, h, 2)
	b, _, _ := blockAvg(src, w, h, 2)
	c, cw, _ := blockAvg(src, w, h, 8)
	for y := 0; y < bh/4*4; y++ {
		for x := 0; x < bw/4*4; x++ {
			e := a[y*bw+x] - b[y*bw+x]
			lost += e * e
			d := b[y*bw+x] - c[(y/4)*cw+x/4]
			detail += d * d
		}
	}
	return
}

// detailKept turns the two energies into a percentage between 0 and 100.
func detailKept(lost, detail float64) float64 {
	if detail <= 0 {
		return 100
	}
	k := 100 * (1 - lost/detail)
	if k < 0 {
		k = 0
	}
	return math.Round(k*10) / 10
}
