package core

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// A viewing LUT lets an archive be looked at in an editor without rebuilding the clip. Played
// as it is, an archive shows raw sensor planes: green and flat. The LUT maps that picture to
// what Blackmagic's own decoder shows for the clip with its as-shot settings.
//
// The mapping is not computed from a model of the camera: it is measured. Sample frames of the
// clip are decoded by Blackmagic's decoder (through the braw-ref helper, which loads the library
// of an installed Blackmagic RAW) and compared, colour by colour, with the same frames as the
// archive stores them. A LUT therefore belongs to one clip, and is only as good as the range of
// colours that clip shows.

// LUTSuffix replaces ArchiveSuffix in the name of an archive's LUT file.
const LUTSuffix = ".braw.cube"

// lutSize is the number of nodes per axis; lutBlock the size of the pixel blocks both pictures
// are averaged over before comparing (the archive picture has no demosaic sharpening, so single
// pixels do not correspond); lutFrames how many frames are sampled; lutPasses how often the fit
// is refined (see refine).
//
// Measured on frames kept out of the fit, against Blackmagic's decode: a dark night clip went
// from 35 dB (33 nodes, no refining) to 49 dB with these values, a second night clip from 41 to
// 53 dB, a daylight clip from 53 to 56 dB.
const (
	lutSize   = 65
	lutBlock  = 4
	lutFrames = 24
	lutPasses = 12
	lutHold   = 6 // every lutHold-th sample is kept out of the fit to measure it
)

// refName is the helper program's file name.
func refName() string {
	if runtime.GOOS == "windows" {
		return "braw-ref.exe"
	}
	return "braw-ref"
}

// FindBrawRef returns the braw-ref helper beside this program, or "". BRAW_REF names one to use
// instead.
func FindBrawRef() string {
	if p := os.Getenv("BRAW_REF"); p != "" {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
		return ""
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	for _, d := range []string{filepath.Dir(exe), filepath.Join(filepath.Dir(exe), "build", "brawref", "dist")} {
		p := filepath.Join(d, refName())
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// FindBlackmagicLibrary returns the folder of an installed Blackmagic RAW decoder library, or "".
// BRAW_LIBRARY names a folder to use instead. DaVinci Resolve's own copy is not used: it is a
// different build that the helper cannot drive.
func FindBlackmagicLibrary() string {
	has := func(dir string) bool {
		st, err := os.Stat(filepath.Join(dir, "BlackmagicRawAPI.dll"))
		return err == nil && !st.IsDir()
	}
	if d := os.Getenv("BRAW_LIBRARY"); d != "" {
		if has(d) {
			return d
		}
		return ""
	}
	if runtime.GOOS != "windows" {
		return ""
	}
	for _, root := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles")} {
		if root == "" {
			continue
		}
		base := filepath.Join(root, "Blackmagic Design", "Blackmagic RAW")
		for _, d := range []string{
			filepath.Join(base, "Blackmagic RAW Player", "BlackmagicRawAPI"),
			filepath.Join(base, "Blackmagic RAW SDK", "Win", "Libraries"),
		} {
			if has(d) {
				return d
			}
		}
	}
	return ""
}

// LUTSupport says whether viewing LUTs can be made here, and if not, why.
func LUTSupport() (ok bool, reason string) {
	if runtime.GOOS != "windows" {
		return false, "Viewing LUTs can only be made on Windows for now."
	}
	if FindBrawRef() == "" {
		return false, "The " + refName() + " helper is missing; it should sit beside this program."
	}
	if FindBlackmagicLibrary() == "" {
		return false, "Needs Blackmagic RAW installed (the free Blackmagic RAW Player from Blackmagic Design)."
	}
	return true, ""
}

// rgbImage is a picture as three planes of values, nominally 0..1.
type rgbImage struct {
	w, h    int
	r, g, b []float32
}

func newRGB(w, h int) *rgbImage {
	return &rgbImage{w: w, h: h, r: make([]float32, w*h), g: make([]float32, w*h), b: make([]float32, w*h)}
}

// playerRGB converts stored planes the way a video player decodes the archive: full-range
// BT.709 YCbCr to RGB, without clamping. bits is the archive's sample depth; the planes are
// rounded to it exactly as Pack does.
func playerRGB(Y, U, V []uint16, w, h, bits int) *rgbImage {
	max, mid := float32(4095), float32(2048)
	q := func(v uint16) float32 { return float32(v) }
	if bits == 10 {
		max, mid = 1023, 512
		q = func(v uint16) float32 {
			s := (v + 2) >> 2
			if s > 1023 {
				s = 1023
			}
			return float32(s)
		}
	}
	cw := w / 2
	im := newRGB(w, h)
	for y := 0; y < h; y++ {
		cy := y / 2
		for x := 0; x < w; x++ {
			i := y*w + x
			c := cy*cw + x/2
			l := q(Y[i]) / max
			cb := (q(U[c]) - mid) / max
			cr := (q(V[c]) - mid) / max
			im.r[i] = l + 1.5748*cr
			im.g[i] = l - 0.1873*cb - 0.4681*cr
			im.b[i] = l + 1.8556*cb
		}
	}
	return im
}

// refRGB reads one rgb48le frame.
func refRGB(buf []byte, w, h int) *rgbImage {
	im := newRGB(w, h)
	for i := 0; i < w*h; i++ {
		im.r[i] = float32(binary.LittleEndian.Uint16(buf[6*i:])) / 65535
		im.g[i] = float32(binary.LittleEndian.Uint16(buf[6*i+2:])) / 65535
		im.b[i] = float32(binary.LittleEndian.Uint16(buf[6*i+4:])) / 65535
	}
	return im
}

// blocks averages lutBlock x lutBlock pixel blocks of the region at (ox, oy) of size (rw, rh).
func (im *rgbImage) blocks(ox, oy, rw, rh int) *rgbImage {
	dw, dh := rw/lutBlock, rh/lutBlock
	o := newRGB(dw, dh)
	const n = lutBlock * lutBlock
	for y := 0; y < dh; y++ {
		for x := 0; x < dw; x++ {
			var r, g, b float32
			for j := 0; j < lutBlock; j++ {
				row := (oy+y*lutBlock+j)*im.w + ox + x*lutBlock
				for i := 0; i < lutBlock; i++ {
					r += im.r[row+i]
					g += im.g[row+i]
					b += im.b[row+i]
				}
			}
			k := y*dw + x
			o.r[k], o.g[k], o.b[k] = r/n, g/n, b/n
		}
	}
	return o
}

// findOffset locates the reference picture inside the slightly larger stored planes by
// correlating the green channels. It returns the correlation reached, which is near 1 when the
// two pictures really are the same frame.
func findOffset(p, ref *rgbImage) (ox, oy int, corr float64) {
	corr = math.Inf(-1)
	for y0 := 0; y0 <= p.h-ref.h; y0++ {
		for x0 := 0; x0 <= p.w-ref.w; x0++ {
			var sa, sb, saa, sbb, sab, n float64
			for y := 8; y < ref.h-8; y += 7 {
				for x := 8; x < ref.w-8; x += 7 {
					a := float64(p.g[(y0+y)*p.w+x0+x])
					b := float64(ref.g[y*ref.w+x])
					sa, sb = sa+a, sb+b
					saa, sbb, sab = saa+a*a, sbb+b*b, sab+a*b
					n++
				}
			}
			den := math.Sqrt((saa - sa*sa/n) * (sbb - sb*sb/n))
			if den <= 0 {
				continue
			}
			if c := (sab - sa*sb/n) / den; c > corr {
				corr, ox, oy = c, x0, y0
			}
		}
	}
	return
}

// cubeLUT is a 3D LUT being fitted: sums of output colours per node, weighted by how close each
// sample fell to the node.
type cubeLUT struct {
	n      int
	lo, hi float32
	v      []float32 // n*n*n*3, node index (b*n+g)*n+r
	wsum   []float32
}

func (l *cubeLUT) pos(x float32) (int, float32) {
	u := (x - l.lo) / (l.hi - l.lo)
	t := u * float32(l.n-1)
	if t < 0 {
		t = 0
	}
	if t > float32(l.n-1) {
		t = float32(l.n - 1)
	}
	i := int(t)
	if i >= l.n-1 {
		i = l.n - 2
	}
	return i, t - float32(i)
}

// corners calls fn with the eight nodes around a colour and their weights.
func (l *cubeLUT) corners(r, g, b float32, fn func(k int, w float32)) {
	ri, rf := l.pos(r)
	gi, gf := l.pos(g)
	bi, bf := l.pos(b)
	for db := 0; db < 2; db++ {
		wb := 1 - bf
		if db == 1 {
			wb = bf
		}
		for dg := 0; dg < 2; dg++ {
			wg := 1 - gf
			if dg == 1 {
				wg = gf
			}
			for dr := 0; dr < 2; dr++ {
				wr := 1 - rf
				if dr == 1 {
					wr = rf
				}
				fn(((bi+db)*l.n+gi+dg)*l.n+ri+dr, wr*wg*wb)
			}
		}
	}
}

func (l *cubeLUT) add(r, g, b, or, og, ob float32) {
	l.corners(r, g, b, func(k int, w float32) {
		l.v[3*k] += w * or
		l.v[3*k+1] += w * og
		l.v[3*k+2] += w * ob
		l.wsum[k] += w
	})
}

func (l *cubeLUT) apply(r, g, b float32) (or, og, ob float32) {
	l.corners(r, g, b, func(k int, w float32) {
		or += w * l.v[3*k]
		og += w * l.v[3*k+1]
		ob += w * l.v[3*k+2]
	})
	return
}

// settle turns the sums into node values: each node becomes the weighted average of the samples
// around it. It returns which nodes were measured.
func (l *cubeLUT) settle() (known []bool) {
	known = make([]bool, len(l.wsum))
	for k := range l.wsum {
		if l.wsum[k] > 2 {
			for c := 0; c < 3; c++ {
				l.v[3*k+c] /= l.wsum[k]
			}
			known[k] = true
		}
	}
	return known
}

// refine moves the measured nodes so that the LUT, read back with interpolation, lands closer
// to the samples. Averaging alone blurs the mapping where it bends sharply (deep shadows, where
// many sensor values share a node), which lifts and desaturates them; each pass spreads what the
// LUT still gets wrong back onto the nodes. samples calls its argument once per sample.
func (l *cubeLUT) refine(known []bool, passes int, samples func(fn func(r, g, b, or, og, ob float32))) {
	for p := 0; p < passes; p++ {
		corr := make([]float32, len(l.v))
		samples(func(r, g, b, or, og, ob float32) {
			ar, ag, ab := l.apply(r, g, b)
			l.corners(r, g, b, func(k int, w float32) {
				corr[3*k] += w * (or - ar)
				corr[3*k+1] += w * (og - ag)
				corr[3*k+2] += w * (ob - ab)
			})
		})
		for k, ok := range known {
			if ok {
				for c := 0; c < 3; c++ {
					l.v[3*k+c] += corr[3*k+c] / l.wsum[k]
				}
			}
		}
	}
}

// fill gives a value to the nodes no sample reached, by growing outward from measured ones, and
// smooths only that filled-in region. It returns the share of nodes that were measured.
func (l *cubeLUT) fill(known []bool) float64 {
	n := l.n
	total := n * n * n
	measured := 0
	for _, ok := range known {
		if ok {
			measured++
		}
	}
	orig := append([]bool(nil), known...)
	neighbours := func(k int, fn func(j int)) {
		r, g, b := k%n, (k/n)%n, k/(n*n)
		if r > 0 {
			fn(k - 1)
		}
		if r < n-1 {
			fn(k + 1)
		}
		if g > 0 {
			fn(k - n)
		}
		if g < n-1 {
			fn(k + n)
		}
		if b > 0 {
			fn(k - n*n)
		}
		if b < n-1 {
			fn(k + n*n)
		}
	}
	for measured > 0 {
		type fill struct {
			k int
			v [3]float32
		}
		var add []fill
		for k := 0; k < total; k++ {
			if known[k] {
				continue
			}
			var s [3]float32
			cnt := 0
			neighbours(k, func(j int) {
				if known[j] {
					s[0], s[1], s[2] = s[0]+l.v[3*j], s[1]+l.v[3*j+1], s[2]+l.v[3*j+2]
					cnt++
				}
			})
			if cnt > 0 {
				add = append(add, fill{k, [3]float32{s[0] / float32(cnt), s[1] / float32(cnt), s[2] / float32(cnt)}})
			}
		}
		if len(add) == 0 {
			break
		}
		for _, f := range add {
			l.v[3*f.k], l.v[3*f.k+1], l.v[3*f.k+2] = f.v[0], f.v[1], f.v[2]
			known[f.k] = true
		}
	}
	for it := 0; it < 30; it++ {
		next := append([]float32(nil), l.v...)
		for k := 0; k < total; k++ {
			if orig[k] {
				continue
			}
			var s [3]float32
			cnt := 0
			neighbours(k, func(j int) {
				s[0], s[1], s[2] = s[0]+l.v[3*j], s[1]+l.v[3*j+1], s[2]+l.v[3*j+2]
				cnt++
			})
			next[3*k], next[3*k+1], next[3*k+2] = s[0]/float32(cnt), s[1]/float32(cnt), s[2]/float32(cnt)
		}
		l.v = next
	}
	return float64(measured) / float64(total)
}

// write stores the LUT in the .cube format, through a temporary file.
func (l *cubeLUT) write(path, title string) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	fmt.Fprintf(w, "TITLE \"%s\"\nLUT_3D_SIZE %d\nLUT_3D_INPUT_RANGE %.6f %.6f\n", title, l.n, l.lo, l.hi)
	clamp := func(x float32) float32 {
		if x < 0 {
			return 0
		}
		if x > 1 {
			return 1
		}
		return x
	}
	for k := 0; k < l.n*l.n*l.n; k++ {
		fmt.Fprintf(w, "%.6f %.6f %.6f\n", clamp(l.v[3*k]), clamp(l.v[3*k+1]), clamp(l.v[3*k+2]))
	}
	err = w.Flush()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// referenceFrames asks Blackmagic's decoder, through the helper, for the listed frames.
func referenceFrames(ctx context.Context, src string, frames []int, each func(j int, im *rgbImage) error) error {
	helper, lib := FindBrawRef(), FindBlackmagicLibrary()
	if ok, reason := LUTSupport(); !ok {
		return fmt.Errorf("%s", reason)
	}
	abs, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	args := []string{lib, abs}
	for _, i := range frames {
		args = append(args, fmt.Sprint(i))
	}
	cmd := command(helper, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("cannot start %s: %w", helper, err)
	}
	fail := func(err error) error {
		cmd.Process.Kill()
		cmd.Wait()
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%w (%s)", err, msg)
		}
		return err
	}
	var head [8]byte
	if _, err := io.ReadFull(out, head[:]); err != nil {
		return fail(fmt.Errorf("Blackmagic's decoder gave no picture"))
	}
	w, h := int(binary.LittleEndian.Uint32(head[0:])), int(binary.LittleEndian.Uint32(head[4:]))
	if w < 16 || h < 16 || w > 1<<15 || h > 1<<15 {
		return fail(fmt.Errorf("Blackmagic's decoder reported a %dx%d picture", w, h))
	}
	buf := make([]byte, w*h*6)
	for j := range frames {
		if err := alive(ctx); err != nil {
			return fail(err)
		}
		if _, err := io.ReadFull(out, buf); err != nil {
			return fail(fmt.Errorf("Blackmagic's decoder stopped at sample %d of %d", j+1, len(frames)))
		}
		if err := each(j, refRGB(buf, w, h)); err != nil {
			return fail(err)
		}
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("Blackmagic's decoder failed: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// LUTResult describes a fitted viewing LUT.
type LUTResult struct {
	Path     string
	Frames   int     // sample frames used
	Accuracy float64 // dB against Blackmagic's decode on frames kept out of the fit (block averages)
	Measured float64 // share of the LUT's nodes seen in the clip, 0..1; the rest is filled in
}

// FitLUT measures a viewing LUT for a clip as an archive with the given colour scale and sample
// depth stores it, and writes it to dst (replacing a file already there: a LUT belongs to its
// archive). The clip is only read.
func FitLUT(ctx context.Context, clip *Clip, chromaScale float64, bits int, dst string, progress Progress) (*LUTResult, error) {
	if progress == nil {
		progress = func(string, int, int) {}
	}
	frames := sampleFrames(clip.Frames(), lutFrames)
	type pair struct{ arc, ref *rgbImage }
	pairs := make([]pair, len(frames))
	ox, oy := 0, 0
	lo, hi := float32(0), float32(1)
	progress("lut", 0, len(frames)+1)
	err := referenceFrames(ctx, clip.Path, frames, func(j int, ref *rgbImage) (err error) {
		defer rescue(&err)
		fr, err := clip.frame(frames[j])
		if err != nil {
			return err
		}
		fi, Y, U, V, _, _, err := decodePlanes(fr, chromaScale, planesAll)
		if err != nil {
			return err
		}
		arc := playerRGB(Y, U, V, fi.w, fi.h, bits)
		if ref.w > arc.w || ref.h > arc.h {
			return fmt.Errorf("Blackmagic's picture (%dx%d) is larger than the stored planes (%dx%d)", ref.w, ref.h, arc.w, arc.h)
		}
		if j == 0 {
			var corr float64
			if ox, oy, corr = findOffset(arc, ref); corr < 0.9 {
				return fmt.Errorf("the stored planes do not line up with Blackmagic's picture (correlation %.2f)", corr)
			}
		}
		a := arc.blocks(ox, oy, ref.w, ref.h)
		for _, p := range [][]float32{a.r, a.g, a.b} {
			for _, v := range p {
				if v < lo {
					lo = v
				}
				if v > hi {
					hi = v
				}
			}
		}
		pairs[j] = pair{a, ref.blocks(0, 0, ref.w, ref.h)}
		progress("lut", j+1, len(frames)+1)
		return nil
	})
	if err != nil {
		return nil, err
	}
	held := func(j int) bool { return len(pairs) >= lutHold && j%lutHold == lutHold-1 }
	l := &cubeLUT{n: lutSize, lo: lo - 0.01, hi: hi + 0.01, v: make([]float32, lutSize*lutSize*lutSize*3), wsum: make([]float32, lutSize*lutSize*lutSize)}
	for j, p := range pairs {
		if held(j) {
			continue
		}
		for k := range p.arc.r {
			l.add(p.arc.r[k], p.arc.g[k], p.arc.b[k], p.ref.r[k], p.ref.g[k], p.ref.b[k])
		}
	}
	known := l.settle()
	// Unmeasured nodes take part in reading the LUT back, so they need values first.
	saved := append([]bool(nil), known...)
	l.fill(known)
	l.refine(saved, lutPasses, func(fn func(r, g, b, or, og, ob float32)) {
		for j, p := range pairs {
			if held(j) {
				continue
			}
			for k := range p.arc.r {
				fn(p.arc.r[k], p.arc.g[k], p.arc.b[k], p.ref.r[k], p.ref.g[k], p.ref.b[k])
			}
		}
	})
	res := &LUTResult{Path: dst, Frames: len(frames), Measured: l.fill(saved)}
	if res.Measured == 0 {
		return nil, fmt.Errorf("no colour of the clip could be measured")
	}
	var se, n float64
	for j, p := range pairs {
		// With too few frames to hold any out, the fit is measured on its own samples.
		if len(pairs) >= lutHold && !held(j) {
			continue
		}
		for k := range p.arc.r {
			r, g, b := l.apply(p.arc.r[k], p.arc.g[k], p.arc.b[k])
			dr, dg, db := float64(r-p.ref.r[k]), float64(g-p.ref.g[k]), float64(b-p.ref.b[k])
			se += dr*dr + dg*dg + db*db
			n += 3
		}
	}
	if se > 0 {
		res.Accuracy = round(10*math.Log10(n/se), 1)
	}
	title := "braw-pack viewing LUT for " + strings.ReplaceAll(filepath.Base(clip.Path), "\"", "'")
	if err := l.write(dst, title); err != nil {
		return nil, err
	}
	progress("lut", len(frames)+1, len(frames)+1)
	return res, nil
}

// readCube loads a LUT written by cubeLUT.write.
func readCube(path string) (*cubeLUT, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	l := &cubeLUT{lo: 0, hi: 1}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		var a, b, c float32
		switch {
		case strings.HasPrefix(line, "LUT_3D_SIZE"):
			fmt.Sscanf(line, "LUT_3D_SIZE %d", &l.n)
		case strings.HasPrefix(line, "LUT_3D_INPUT_RANGE"):
			fmt.Sscanf(line, "LUT_3D_INPUT_RANGE %f %f", &l.lo, &l.hi)
		default:
			if n, _ := fmt.Sscanf(line, "%f %f %f", &a, &b, &c); n == 3 {
				l.v = append(l.v, a, b, c)
			}
		}
	}
	if l.n < 2 || len(l.v) != 3*l.n*l.n*l.n {
		return nil, fmt.Errorf("%s is not a complete 3D LUT", path)
	}
	return l, sc.Err()
}
