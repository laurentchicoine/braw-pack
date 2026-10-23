package core

// Version 5 picture planes: quantised levels to sample planes and back (see v5.go for the
// bitstream).

import (
	"fmt"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
)

// Scan order: natural coefficient index (row*12 + column, row being the vertical frequency) by
// scan position. Brightness blocks go ring by ring (2x2, 4x4, 8x8, 12x12), each ring in the usual
// zigzag order. Colour blocks are 6 rows by 12 columns and go the same way over pairs of
// horizontally adjacent coefficients, in rings of 1x1, 2x2, 4x4 and 6x6 pairs.
var (
	v5YScan [144]int
	v5CScan [72]int
)

func init() {
	// zigzag lists the cells of a rows x cols grid in zigzag order, first step to the right.
	zigzag := func(rows, cols int) (out [][2]int) {
		for d := 0; d < rows+cols-1; d++ {
			var diag [][2]int
			for r := 0; r < rows; r++ {
				if c := d - r; c >= 0 && c < cols {
					diag = append(diag, [2]int{r, c})
				}
			}
			if d%2 == 0 { // upwards: rows decreasing
				for i, j := 0, len(diag)-1; i < j; i, j = i+1, j-1 {
					diag[i], diag[j] = diag[j], diag[i]
				}
			}
			out = append(out, diag...)
		}
		return
	}
	rings := func(n int, sizes []int, emit func(r, c int)) {
		order := zigzag(n, n)
		seen := map[[2]int]bool{}
		for _, s := range sizes {
			for _, rc := range order {
				if rc[0] < s && rc[1] < s && !seen[rc] {
					seen[rc] = true
					emit(rc[0], rc[1])
				}
			}
		}
	}
	k := 0
	rings(12, []int{2, 4, 8, 12}, func(r, c int) { v5YScan[k] = r*12 + c; k++ })
	k = 0
	rings(6, []int{1, 2, 4, 6}, func(r, c int) {
		v5CScan[k], v5CScan[k+1] = r*12+2*c, r*12+2*c+1
		k += 2
	})
}

// v5StepsVersion names the quantisation steps below; archives record it (Meta.V5Steps), and
// everything that reads an archive's planes back uses the archive's own version.
//
// Version 5 frames carry no table of steps. Measured with Blackmagic's decoder (black box, see
// docs/braw-v5.md), its steps are the same for every frequency of a block, in orthonormal DCT
// terms, and scale with the frame's qscale fields (v5Scale). Any steps give an exact round trip
// as long as the planes are made and read back with the same ones; they decide how the planes
// weight each frequency and how frames compare with each other.
//
//   - Steps 1 are flat and ignore the qscale fields: one level is one sample unit in every frame.
//     The planes of frames the camera coded at another qscale0 are then on another scale.
//   - Steps 2 are flat too, times Blackmagic's scale for the frame (equal to steps 1 at
//     qscale0 3 when the DC caps are above 350 and 200, as the camera writes them), so the
//     planes hold the picture the decoder sees whatever qscale0 the camera chose, and a frame
//     can be rebuilt at a coarser qscale0.
//   - Steps 3 retain steps 2 for full-size 12K frames and correct the scaled-mode
//     DC limit units to the exact SDK-measured 10/7 ratio. Scaled steps 2 remain
//     available to reconstruct the first 4K archives without changing their values.
const v5StepsVersion = 3

// v5Scale returns the steps Blackmagic's decoder derives from a frame's qscale fields, in its own
// units: brightness 50*(q0+4) for AC, the same capped at qscale1 for DC; colour 50*c with
// c = max(4, floor((q0+5)/2)) for AC, the same capped at qscale2 for DC.
func v5Scale(q0, qs1, qs2 int) (yAC, yDC, cAC, cDC float64) {
	yAC = 50 * float64(q0+4)
	cAC = 50 * float64(max(4, (q0+5)/2))
	return yAC, math.Min(yAC, float64(qs1)), cAC, math.Min(cAC, float64(qs2))
}

// Blackmagic's steps at qscale0 3 (DC caps not reached), where steps 2 equal steps 1.
const v5RefY, v5RefC = 350, 200

// v5StepsKnown says whether this braw-pack knows steps version v.
func v5StepsKnown(v int) bool { return v >= 1 && v <= 3 }

// v5Steps returns the step of every coefficient (natural order) for steps version v and the
// frame described by fi.
func v5Steps(v int, fi *frameInfo) (y [144]float64, c [72]float64, ok bool) {
	if !v5StepsKnown(v) {
		return y, c, false
	}
	// With orthonormal transforms, a step of sqrt(samples) makes one DC level one sample unit.
	ky, kc := 12.0, math.Sqrt(72)
	yAC, yDC, cAC, cDC := float64(v5RefY), float64(v5RefY), float64(v5RefC), float64(v5RefC)
	if v >= 2 {
		yAC, yDC, cAC, cDC = v5Scale(fi.qscale0, fi.qscale1, fi.qscale2)
	}
	for i := range y {
		y[i] = ky * yAC / v5RefY
	}
	y[0] = ky * yDC / v5RefY
	for i := range c {
		c[i] = kc * cAC / v5RefC
	}
	c[0] = kc * cDC / v5RefC
	return y, c, true
}

// Orthonormal DCT bases: dctN[k][n] = a(k) cos((2n+1) k pi / 2N).
var dct12, dct6 = dctBasis(12), dctBasis(6)

func dctBasis(n int) [][]float64 {
	b := make([][]float64, n)
	for k := range b {
		b[k] = make([]float64, n)
		a := math.Sqrt(2 / float64(n))
		if k == 0 {
			a = math.Sqrt(1 / float64(n))
		}
		for i := range b[k] {
			b[k][i] = a * math.Cos(float64(2*i+1)*float64(k)*math.Pi/float64(2*n))
		}
	}
	return b
}

// v5IDCT turns the coefficients F (rows x 12, natural order) into samples f. Coefficient rows
// outside mask (bit v = row v) are all zero and skipped. rows is 12 (brightness) or 6 (colour).
func v5IDCT(F, f []float64, rows int, mask uint) {
	vert := dct12
	if rows == 6 {
		vert = dct6
	}
	var tmp [144]float64
	for v := 0; v < rows; v++ {
		if mask&(1<<uint(v)) == 0 {
			continue
		}
		out := tmp[v*12 : v*12+12]
		for u, c := range F[v*12 : v*12+12] {
			if c == 0 {
				continue
			}
			for x, b := range dct12[u] {
				out[x] += c * b
			}
		}
	}
	for y := 0; y < rows; y++ {
		out := f[y*12 : y*12+12]
		clear(out)
		for v := 0; v < rows; v++ {
			if mask&(1<<uint(v)) == 0 {
				continue
			}
			b := vert[v][y]
			for x, t := range tmp[v*12 : v*12+12] {
				out[x] += t * b
			}
		}
	}
}

// v5FDCT is the inverse of v5IDCT.
func v5FDCT(f, F []float64, rows int) {
	vert := dct12
	if rows == 6 {
		vert = dct6
	}
	var tmp [144]float64
	for y := 0; y < rows; y++ {
		in := f[y*12 : y*12+12]
		out := tmp[y*12 : y*12+12]
		for u := range out {
			s := 0.0
			for x, b := range dct12[u] {
				s += in[x] * b
			}
			out[u] = s
		}
	}
	for v := 0; v < rows; v++ {
		out := F[v*12 : v*12+12]
		clear(out)
		for y := 0; y < rows; y++ {
			b := vert[v][y]
			for u, t := range tmp[y*12 : y*12+12] {
				out[u] += t * b
			}
		}
	}
}

// decodePlanesV5 is decodePlanes for version 5 frames. The planes are laid out as for the
// other versions (brightness full size, colour half size both ways), and side receives two
// bytes per macroblock. cs scales all three planes around mid-scale, not only the colour ones:
// the steps' scale is braw-pack's own, so nothing guarantees that brightness fits 12 bits, and
// st.maxChroma is the largest deviation from mid-scale in any plane.
func decodePlanesV5(fr []byte, fi *frameInfo, cs float64, what, steps int) (Y, U, V []uint16, side []byte, st planeStats, err error) {
	qy, qc, ok := v5Steps(steps, fi)
	if !ok || qy[0] <= 0 || qc[0] <= 0 {
		return nil, nil, nil, nil, st, fmt.Errorf("unknown version 5 steps %d, or a frame without quantisation", steps)
	}
	w, cw := fi.w, fi.w/2
	luma, chroma := what&planesLuma != 0, what&planesChroma != 0
	if luma {
		Y = make([]uint16, w*fi.h)
	}
	if chroma {
		U = make([]uint16, cw*fi.h/2)
		V = make([]uint16, cw*fi.h/2)
	}
	n := fi.ntw * fi.nth
	sides := make([][]byte, n)
	stats := make([]planeStats, n)
	errs := make([]error, n)
	forTiles(n, func(t int) {
		c, r := t/fi.nth, t%fi.nth
		rd, err := fi.v5Readers(fr, c, r)
		if err != nil {
			errs[t] = err
			return
		}
		tx, ty, mbw, mbh := fi.v5Tile(c, r)
		sd := make([]byte, 0, 2*mbw*mbh)
		var s planeStats
		var lv [144]int32
		var F, f [144]float64
		var prev [3]int64
		for mb := 0; mb < mbw*mbh; mb++ {
			px, py := tx+(mb%mbw)*v5MBW, ty+(mb/mbw)*v5MBH
			for b := 0; b < 4; b++ {
				split, pi, scan, q, rows := &v5YSplit, 0, v5YScan[:], qy[:], 12
				if b >= 2 {
					split, pi, scan, q, rows = &v5CSplit, b-1, v5CScan[:], qc[:], 6
				}
				last, err := v5ReadBlock(&rd, split, &prev[pi], lv[:split[3]])
				if err != nil {
					errs[t] = fmt.Errorf("tile %d,%d: %w", c, r, err)
					return
				}
				if (b < 2 && !luma) || (b >= 2 && !chroma) {
					clear(lv[:last+1])
					continue
				}
				F[0] = float64(lv[0]) * q[0]
				mask := uint(1)
				for k := 1; k <= last; k++ {
					if lv[k] != 0 {
						i := scan[k]
						F[i] = float64(lv[k]) * q[i]
						mask |= 1 << uint(i/12)
					}
				}
				clear(lv[:last+1])
				v5IDCT(F[:], f[:], rows, mask)
				for v := 0; v < rows; v++ {
					if mask&(1<<uint(v)) != 0 {
						clear(F[v*12 : v*12+12])
					}
				}
				var dst []uint16
				stride, out := w, &s.lumaOut
				switch b {
				case 0, 1:
					dst = Y[py*w+px+12*b:]
				case 2:
					dst, stride, out = U[(py/2)*cw+px/2:], cw, &s.chromaOut
				case 3:
					dst, stride, out = V[(py/2)*cw+px/2:], cw, &s.chromaOut
				}
				for y := 0; y < rows; y++ {
					row := dst[y*stride : y*stride+12]
					for x, d := range f[y*12 : y*12+12] {
						if d > s.maxChroma {
							s.maxChroma = d
						} else if -d > s.maxChroma {
							s.maxChroma = -d
						}
						if v := 2048 + d*cs; v >= -0.5 && v < 4095.5 {
							row[x] = uint16(v + 0.5)
						} else {
							put12(&row[x], v, out)
						}
					}
				}
			}
			sd = append(sd, byte(rd[0].get(8)), byte(rd[0].get(8)))
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

// encodePlanesV5 is encodePlanes for version 5 frames. The steps follow hdr's qscale fields, so
// planes made at one qscale0 can be coded at another (with steps 2).
func encodePlanesV5(hdr []byte, fi *frameInfo, side []byte, Y, U, V []uint16, cs, dz float64, steps int) ([]byte, error) {
	qy, qc, ok := v5Steps(steps, fi)
	if !ok || qy[0] <= 0 || qc[0] <= 0 {
		return nil, fmt.Errorf("unknown version 5 steps %d, or a frame without quantisation", steps)
	}
	w, cw := fi.w, fi.w/2
	if len(Y) != w*fi.h || len(U) != cw*fi.h/2 || len(V) != cw*fi.h/2 {
		return nil, fmt.Errorf("picture size does not match the frame header (%dx%d)", w, fi.h)
	}
	if len(hdr) != fi.offs[0]+fi.hs {
		return nil, fmt.Errorf("frame header is %d bytes, expected %d", len(hdr), fi.offs[0]+fi.hs)
	}
	n := fi.ntw * fi.nth
	// Side fields are stored in tile order, two bytes per macroblock.
	sideAt := make([]int, n+1)
	for t := 0; t < n; t++ {
		_, _, mbw, mbh := fi.v5Tile(t/fi.nth, t%fi.nth)
		sideAt[t+1] = sideAt[t] + 2*mbw*mbh
	}
	if len(side) < sideAt[n] {
		return nil, fmt.Errorf("side data too short")
	}
	var ry [144]float64 // reciprocal steps
	var rc [72]float64
	for i := range ry {
		ry[i] = 1 / qy[i]
	}
	for i := range rc {
		rc[i] = 1 / qc[i]
	}
	zero := 0.5 + dz // AC magnitudes below this quantise to nothing
	streams := make([][v5Streams][]byte, n)
	forTiles(n, func(t int) {
		c, r := t/fi.nth, t%fi.nth
		tx, ty, mbw, mbh := fi.v5Tile(c, r)
		sd := side[sideAt[t]:sideAt[t+1]]
		var wr [v5Streams]bitWriter
		var lv [144]int32
		var F, f [144]float64
		var prev [3]int64
		for mb := 0; mb < mbw*mbh; mb++ {
			px, py := tx+(mb%mbw)*v5MBW, ty+(mb/mbw)*v5MBH
			for b := 0; b < 4; b++ {
				split, pi, scan, rq, rows := &v5YSplit, 0, v5YScan[:], ry[:], 12
				var src []uint16
				stride := w
				switch b {
				case 0, 1:
					src = Y[py*w+px+12*b:]
				case 2:
					src, stride = U[(py/2)*cw+px/2:], cw
				case 3:
					src, stride = V[(py/2)*cw+px/2:], cw
				}
				if b >= 2 {
					split, pi, scan, rq, rows = &v5CSplit, b-1, v5CScan[:], rc[:], 6
				}
				for y := 0; y < rows; y++ {
					for x, v := range src[y*stride : y*stride+12] {
						f[y*12+x] = (float64(v) - 2048) / cs
					}
				}
				v5FDCT(f[:], F[:], rows)
				maxc := split[3]
				lv[0] = int32(math.Floor(F[0]*rq[0] + 0.5))
				for k := 1; k < maxc; k++ {
					i := scan[k]
					x := F[i] * rq[i]
					if x < zero && x > -zero {
						lv[k] = 0
						continue
					}
					a := math.Min(math.Floor(math.Abs(x)+0.5-dz), 4095)
					if x < 0 {
						a = -a
					}
					lv[k] = int32(a)
				}
				v5WriteBlock(&wr, split, &prev[pi], lv[:maxc])
			}
			wr[0].put(uint32(sd[2*mb])<<8|uint32(sd[2*mb+1]), 16)
		}
		for s := range wr {
			wr[s].flush()
			streams[t][s] = wr[s].buf
		}
	})
	return fi.v5Frame(hdr, streams), nil
}

// forTiles runs fn for tiles 0..n-1, spread over the CPUs. Tiles are independent, and a 12K frame
// is large enough to be worth splitting.
func forTiles(n int, fn func(t int)) {
	var next int64 = -1
	var wg sync.WaitGroup
	for g := 0; g < runtime.NumCPU() && g < n; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				t := int(atomic.AddInt64(&next, 1))
				if t >= n {
					return
				}
				fn(t)
			}
		}()
	}
	wg.Wait()
}
