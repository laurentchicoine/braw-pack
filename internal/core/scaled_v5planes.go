package core

// Scaled 4K/8K modes use two/three streams and 4x4/8x8 brightness transforms,
// with colour transforms half as high and one opaque byte per macroblock.

import (
	"fmt"
	"math"
)

var v5ScaledYScan = [16]int{0, 1, 4, 5, 8, 2, 3, 6, 9, 12, 13, 10, 7, 11, 14, 15}
var v5ScaledCScan = [8]int{0, 1, 2, 3, 4, 5, 6, 7}
var scaledV5Basis4, scaledV5Basis2 = dctBasis(4), dctBasis(2)
var scaledV5Basis8 = dctBasis(8)
var v5Scaled8YScan, v5Scaled8CScan = makeScaledV5Scans8()

// Pyxis 6K scan orders (layout 0x0804); see docs/braw-v5-pyxis.md.
var v5Full8YScan, v5Full8CScan = v5Scaled8YScan, v5Scaled8CScan

func makeScaledV5Scans8() (y [64]int, c [32]int) {
	// Construct independently of init ordering in v5planes.go.
	k := 0
	for _, size := range []int{2, 4, 8} {
		for d := 0; d < 15; d++ {
			for p := 0; p < 8; p++ {
				r, col := p, d-p
				if d%2 == 0 {
					r, col = d-p, p
				}
				if r < 0 || col < 0 || r >= size || col >= size {
					continue
				}
				if size > 2 && r < size/2 && col < size/2 {
					continue
				}
				y[k] = r*8 + col
				k++
			}
		}
	}
	k = 0
	for _, size := range []int{1, 2, 4} {
		for d := 0; d < 7; d++ {
			for p := 0; p < 4; p++ {
				r, col := p, d-p
				if d%2 == 0 {
					r, col = d-p, p
				}
				if r < 0 || col < 0 || r >= size || col >= size {
					continue
				}
				if size > 1 && r < size/2 && col < size/2 {
					continue
				}
				c[k], c[k+1] = r*8+2*col, r*8+2*col+1
				k += 2
			}
		}
	}
	return
}

// scaledScans returns the frame's brightness and colour scan orders.
func (fi *frameInfo) scaledScans() (y, c []int) {
	if fi.v5Full8 {
		return v5Full8YScan[:], v5Full8CScan[:]
	}
	return scaledV5Scans(fi.scaledSize())
}

func scaledV5Scans(n int) (y, c []int) {
	if n == 8 {
		return v5Scaled8YScan[:], v5Scaled8CScan[:]
	}
	return v5ScaledYScan[:], v5ScaledCScan[:]
}

func scaledV5Basis(n int) [][]float64 {
	if n == 8 {
		return scaledV5Basis8
	}
	if n == 4 {
		return scaledV5Basis4
	}
	return scaledV5Basis2
}

func scaledV5Scale(q0, qs1, qs2 int) (yAC, yDC, cAC, cDC float64) {
	// Fine SDK probes on both scaled modes put the exact thresholds at 420/210
	// for 600/300-unit AC steps, and at 455/245 for 650/350-unit steps.
	return scaledV5ScaleCap(q0, qs1, qs2, 10.0/7)
}

// v5Full8DCCap is the Pyxis 6K's DC limit factor (in place of the URSA modes' 10/7), measured
// with Blackmagic's decoder: re-coding a frame at qscale0 11 to 60 leaves the picture's mean
// colour unchanged (within 0.00005) only near 1.514, for brightness and colour alike; 10/7
// shifts red by 0.004 from qscale0 11 on. 53/35 is that measurement, not a known formula.
const v5Full8DCCap = 53.0 / 35

func scaledV5ScaleCap(q0, qs1, qs2 int, capK float64) (yAC, yDC, cAC, cDC float64) {
	yAC = 50 * float64(q0+4)
	cAC = 50 * float64(max(4, (q0+5)/2))
	return yAC, math.Min(yAC, float64(qs1)*capK), cAC, math.Min(cAC, float64(qs2)*capK)
}

// scaledScale is scaledV5Scale for this frame's layout at qscale0 q0.
func (fi *frameInfo) scaledScale(q0 int) (yAC, yDC, cAC, cDC float64) {
	if fi.v5Full8 {
		return scaledV5ScaleCap(q0, fi.qscale1, fi.qscale2, v5Full8DCCap)
	}
	return scaledV5Scale(q0, fi.qscale1, fi.qscale2)
}

func scaledV5Steps(steps int, fi *frameInfo) (y [64]float64, c [32]float64, ok bool) {
	if !v5StepsKnown(steps) {
		return y, c, false
	}
	ya, yd, ca, cd := float64(v5RefY), float64(v5RefY), float64(v5RefC), float64(v5RefC)
	if steps >= 2 {
		ya, yd, ca, cd = fi.scaledScale(fi.qscale0)
		if steps == 2 {
			// Preserve the approximate DC model of the first 4K archives.
			yd = math.Min(ya, math.Ceil(math.Sqrt2*float64(fi.qscale1)))
			cd = math.Min(ca, math.Ceil(math.Sqrt2*float64(fi.qscale2)))
		}
	}
	n := float64(fi.scaledSize())
	for i := range y {
		y[i] = n * ya / v5RefY
	}
	y[0] = n * yd / v5RefY
	for i := range c {
		c[i] = math.Sqrt(n*n/2) * ca / v5RefC
	}
	c[0] = math.Sqrt(n*n/2) * cd / v5RefC
	return y, c, true
}

func scaledV5IDCT(F, f []float64, rows int, size ...int) {
	n := 4
	if len(size) > 0 {
		n = size[0]
	}
	vert, horiz := scaledV5Basis(rows), scaledV5Basis(n)
	var tmp [64]float64
	for v := 0; v < rows; v++ {
		for u := 0; u < n; u++ {
			a := F[v*n+u]
			for x := 0; x < n; x++ {
				tmp[v*n+x] += a * horiz[u][x]
			}
		}
	}
	for y := 0; y < rows; y++ {
		for x := 0; x < n; x++ {
			s := 0.0
			for v := 0; v < rows; v++ {
				s += tmp[v*n+x] * vert[v][y]
			}
			f[y*n+x] = s
		}
	}
}

func scaledV5FDCT(f, F []float64, rows int, size ...int) {
	n := 4
	if len(size) > 0 {
		n = size[0]
	}
	vert, horiz := scaledV5Basis(rows), scaledV5Basis(n)
	var tmp [64]float64
	for y := 0; y < rows; y++ {
		for u := 0; u < n; u++ {
			for x := 0; x < n; x++ {
				tmp[y*n+u] += f[y*n+x] * horiz[u][x]
			}
		}
	}
	for v := 0; v < rows; v++ {
		for u := 0; u < n; u++ {
			s := 0.0
			for y := 0; y < rows; y++ {
				s += tmp[y*n+u] * vert[v][y]
			}
			F[v*n+u] = s
		}
	}
}

func decodePlanesV5Scaled(fr []byte, fi *frameInfo, cs float64, what, steps int) (Y, U, V []uint16, side []byte, st planeStats, err error) {
	qy, qc, ok := scaledV5Steps(steps, fi)
	if !ok || qy[0] <= 0 || qc[0] <= 0 {
		return nil, nil, nil, nil, st, fmt.Errorf("unknown version 5 steps %d, or a frame without quantisation", steps)
	}
	w, cw := fi.w, fi.w/2
	size := fi.scaledSize()
	ys, csplits := fi.scaledSplits()
	yscan, cscan := fi.scaledScans()
	luma, chroma := what&planesLuma != 0, what&planesChroma != 0
	if luma {
		Y = make([]uint16, w*fi.h)
	}
	if chroma {
		U = make([]uint16, cw*fi.h/2)
		V = make([]uint16, len(U))
	}
	n := fi.ntw * fi.nth
	sides := make([][]byte, n)
	stats := make([]planeStats, n)
	errs := make([]error, n)
	forTiles(n, func(t int) {
		c, r := t/fi.nth, t%fi.nth
		rd, e := fi.v5Readers(fr, c, r)
		if e != nil {
			errs[t] = e
			return
		}
		tx, ty, mbw, mbh := fi.v5ScaledTile(c, r)
		sd := make([]byte, 0, mbw*mbh)
		var s planeStats
		var prev [3]int64
		var lv [64]int32
		var F, f [64]float64
		for mb := 0; mb < mbw*mbh; mb++ {
			px, py := tx+(mb%mbw)*(2*size), ty+(mb/mbw)*size
			for b := 0; b < 4; b++ {
				split, pi, scan, q, rows := ys, 0, yscan, qy[:], size
				if b >= 2 {
					split, pi, scan, q, rows = csplits, b-1, cscan, qc[:], size/2
				}
				clear(lv[:])
				last, e := fi.readBlock(&rd, split, &prev[pi], lv[:len(scan)])
				if e != nil {
					errs[t] = fmt.Errorf("tile %d,%d: %w", c, r, e)
					return
				}
				if (b < 2 && !luma) || (b >= 2 && !chroma) {
					continue
				}
				clear(F[:])
				for k := 0; k <= last; k++ {
					i := scan[k]
					F[i] = float64(lv[k]) * q[i]
				}
				scaledV5IDCT(F[:], f[:], rows, size)
				var dst []uint16
				stride, out := w, &s.lumaOut
				switch b {
				case 0, 1:
					dst = Y[py*w+px+size*b:]
				case 2:
					dst, stride, out = U[(py/2)*cw+px/2:], cw, &s.chromaOut
				case 3:
					dst, stride, out = V[(py/2)*cw+px/2:], cw, &s.chromaOut
				}
				for y := 0; y < rows; y++ {
					for x, d := range f[y*size : y*size+size] {
						s.maxChroma = math.Max(s.maxChroma, math.Abs(d))
						v := 2048 + d*cs
						if v >= -0.5 && v < 4095.5 {
							dst[y*stride+x] = uint16(v + 0.5)
						} else {
							put12(&dst[y*stride+x], v, out)
						}
					}
				}
			}
			sd = append(sd, byte(rd[0].get(8)))
		}
		if v5Overrun(&rd) {
			errs[t] = fmt.Errorf("tile %d,%d: overrun", c, r)
			return
		}
		sides[t], stats[t] = sd, s
	})
	for t := 0; t < n; t++ {
		if errs[t] != nil {
			return nil, nil, nil, nil, st, errs[t]
		}
		side = append(side, sides[t]...)
		st.lumaOut += stats[t].lumaOut
		st.chromaOut += stats[t].chromaOut
		st.maxChroma = math.Max(st.maxChroma, stats[t].maxChroma)
	}
	return Y, U, V, side, st, nil
}

func encodePlanesV5Scaled(hdr []byte, fi *frameInfo, side []byte, Y, U, V []uint16, cs, dz float64, steps int) ([]byte, error) {
	qy, qc, ok := scaledV5Steps(steps, fi)
	if !ok || qy[0] <= 0 || qc[0] <= 0 {
		return nil, fmt.Errorf("unknown version 5 steps %d, or a frame without quantisation", steps)
	}
	w, cw := fi.w, fi.w/2
	size := fi.scaledSize()
	ys, csplits := fi.scaledSplits()
	yscan, cscan := fi.scaledScans()
	if len(Y) != w*fi.h || len(U) != cw*fi.h/2 || len(V) != len(U) {
		return nil, fmt.Errorf("picture size does not match the frame header (%dx%d)", w, fi.h)
	}
	if len(hdr) != fi.offs[0]+fi.hs {
		return nil, fmt.Errorf("frame header is %d bytes, expected %d", len(hdr), fi.offs[0]+fi.hs)
	}
	n := fi.ntw * fi.nth
	sideAt := make([]int, n+1)
	for t := 0; t < n; t++ {
		_, _, mbw, mbh := fi.v5ScaledTile(t/fi.nth, t%fi.nth)
		sideAt[t+1] = sideAt[t] + mbw*mbh
	}
	if len(side) < sideAt[n] {
		return nil, fmt.Errorf("side data too short")
	}
	streams := make([][v5Streams][]byte, n)
	forTiles(n, func(t int) {
		c, r := t/fi.nth, t%fi.nth
		tx, ty, mbw, mbh := fi.v5ScaledTile(c, r)
		sd := side[sideAt[t]:sideAt[t+1]]
		var wr [v5Streams]bitWriter
		var prev [3]int64
		var lv [64]int32
		var F, f [64]float64
		for mb := 0; mb < mbw*mbh; mb++ {
			px, py := tx+(mb%mbw)*(2*size), ty+(mb/mbw)*size
			for b := 0; b < 4; b++ {
				split, pi, scan, q, rows := ys, 0, yscan, qy[:], size
				var src []uint16
				stride := w
				switch b {
				case 0, 1:
					src = Y[py*w+px+size*b:]
				case 2:
					src, stride = U[(py/2)*cw+px/2:], cw
				case 3:
					src, stride = V[(py/2)*cw+px/2:], cw
				}
				if b >= 2 {
					split, pi, scan, q, rows = csplits, b-1, cscan, qc[:], size/2
				}
				for y := 0; y < rows; y++ {
					for x, v := range src[y*stride : y*stride+size] {
						f[y*size+x] = (float64(v) - 2048) / cs
					}
				}
				scaledV5FDCT(f[:], F[:], rows, size)
				lv[0] = int32(math.Floor(F[0]/q[0] + 0.5))
				for k := 1; k < len(scan); k++ {
					i := scan[k]
					x := F[i] / q[i]
					if math.Abs(x) < 0.5+dz {
						lv[k] = 0
						continue
					}
					a := math.Min(math.Floor(math.Abs(x)+0.5-dz), 4095)
					if x < 0 {
						a = -a
					}
					lv[k] = int32(a)
				}
				fi.writeBlock(&wr, split, &prev[pi], lv[:len(scan)])
			}
			wr[0].put(uint32(sd[mb]), 8)
		}
		for s := range wr {
			wr[s].flush()
			streams[t][s] = wr[s].buf
		}
	})
	return fi.v5Frame(hdr, streams), nil
}
