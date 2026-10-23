package core

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime/pprof"
	"time"
)

// Bench times the per-frame stages on one thread and writes a CPU profile.
func Bench(src string, frames int, prof string) error {
	clip, err := OpenClip(src)
	if err != nil {
		return err
	}
	defer clip.Close()
	if prof != "" {
		f, _ := os.Create(prof)
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}
	var tRead, tReq, tDec, tPack, tUnpack, tEnc time.Duration
	for i := 0; i < frames; i++ {
		t := time.Now()
		fr, err := clip.frame(i * (clip.Frames() - 1) / frames)
		if err != nil {
			return err
		}
		tRead += time.Since(t)
		t = time.Now()
		if _, err := requantFrame(fr, 1, 0); err != nil {
			return err
		}
		tReq += time.Since(t)
		t = time.Now()
		fi, Y, U, V, side, _, err := decodePlanes(fr, 1, planesAll)
		if err != nil {
			return err
		}
		tDec += time.Since(t)
		t = time.Now()
		buf := packYUV(Y, U, V, planar12)
		tPack += time.Since(t)
		t = time.Now()
		Y, U, V = unpackYUV(buf, fi.w, fi.h, planar12)
		tUnpack += time.Since(t)
		t = time.Now()
		if _, err := encodePlanes(fr[:fi.offs[0]+fi.hs], side, Y, U, V, 1, 0); err != nil {
			return err
		}
		tEnc += time.Since(t)
	}
	for _, what := range []int{planesChroma, planesLuma, planesAll} {
		for _, workers := range []int{1, 6, 12} {
			t := time.Now()
			sem := make(chan struct{}, workers)
			total := frames * 4
			for i := 0; i < total; i++ {
				sem <- struct{}{}
				go func(i int) {
					fr, _ := clip.frame(i % clip.Frames())
					decodePlanes(fr, 1, what)
					<-sem
				}(i)
			}
			for i := 0; i < workers; i++ {
				sem <- struct{}{}
			}
			fmt.Printf("decode planes=%d, %2d workers: %.0f frames/s\n", what, workers, float64(total)/time.Since(t).Seconds())
		}
	}
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 / float64(frames) }
	fmt.Printf("per frame, one thread: read %.1f ms, entropy only (decode+encode) %.1f ms, decode to planes %.1f ms, pack %.1f ms, unpack %.1f ms, encode from planes %.1f ms\n",
		ms(tRead), ms(tReq), ms(tDec), ms(tPack), ms(tUnpack), ms(tEnc))
	return nil
}

// DumpPlanes writes count frames from start as 10-bit planar samples, for encoder experiments.
func DumpPlanes(src string, start, count int, out string) (int, int, error) {
	clip, err := OpenClip(src)
	if err != nil {
		return 0, 0, err
	}
	defer clip.Close()
	f, err := os.Create(out)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	var w, h int
	for i := start; i < start+count; i++ {
		fr, err := clip.frame(i)
		if err != nil {
			return 0, 0, err
		}
		fi, Y, U, V, _, _, err := decodePlanes(fr, 1, planesAll)
		if err != nil {
			return 0, 0, err
		}
		w, h = fi.w, fi.h
		f.Write(packYUV(Y, U, V, planar10))
	}
	return w, h, nil
}

// AsQuality rebuilds sampled frames of src with the quantisation fields of a frame from ref (a clip
// of the same geometry shot at another quality setting) and reports size and closeness to src.
func AsQuality(src, ref string, samples int) (sizeFrac, y, c float64, err error) {
	a, err := OpenClip(src)
	if err != nil {
		return
	}
	defer a.Close()
	b, err := OpenClip(ref)
	if err != nil {
		return
	}
	defer b.Close()
	rf, err := b.frame(0)
	if err != nil {
		return
	}
	var srcB, outB float64
	idx := sampleFrames(a.Frames(), samples)
	for _, i := range idx {
		fr, e := a.frame(i)
		if e != nil {
			return 0, 0, 0, e
		}
		fi, Y, U, V, side, _, e := decodePlanes(fr, 1, planesAll)
		if e != nil {
			return 0, 0, 0, e
		}
		hdr := append([]byte(nil), fr[:fi.offs[0]+fi.hs]...)
		hdr[fi.hs+9] = rf[fi.hs+9]                                  // qscale0
		copy(hdr[fi.hs+0x20:fi.hs+0x24], rf[fi.hs+0x20:fi.hs+0x24]) // qscale1, qscale2
		copy(hdr[0x1180:0x1180+192], rf[0x1180:0x1180+192])         // table
		out, e := encodePlanes(hdr, side, Y, U, V, 1, 0)
		if e != nil {
			return 0, 0, 0, e
		}
		_, ry, ru, rv, _, _, e := decodePlanes(out, 1, planesAll)
		if e != nil {
			return 0, 0, 0, e
		}
		srcB += float64(len(fr))
		outB += float64(len(out))
		y += psnr12(ry, Y)
		c += (psnr12(ru, U) + psnr12(rv, V)) / 2
	}
	n := float64(len(idx))
	return outB / srcB, y / n, c / n, nil
}

// LabScales reports, for sampled frames of a clip packed at each quality, the brightness error at
// several block-averaging scales next to the source's own detail energy at that scale.
func LabScales(src string, qualities []int, intra bool) error {
	clip, err := OpenClip(src)
	if err != nil {
		return err
	}
	defer clip.Close()
	n := clip.Frames()
	var frames []int
	for s := 0; s < 4; s++ {
		start := s * (n - 12) / 3
		for i := 0; i < 12; i++ {
			frames = append(frames, start+i)
		}
	}
	fr0, _ := clip.frame(0)
	fi, _, _, _, _, _, err := decodePlanes(fr0, 1, planesChroma)
	if err != nil {
		return err
	}
	num, den, _, _ := clip.SensorRate()
	dir, _ := os.MkdirTemp("", "braw-lab-")
	defer os.RemoveAll(dir)
	for _, q := range qualities {
		po := PackOptions{Quality: q, Intra: intra}
		po.defaults()
		po.Progress = func(string, int, int) {}
		rd := &restoreData{Meta: Meta{Format: FormatVersion, Frames: len(frames), Width: fi.w, Height: fi.h, ChromaScale: 1,
			FpsNum: num, FpsDen: den, Encoder: "nvenc", Bits: 10}}
		out := dir + fmt.Sprintf("/q%d.mkv", q)
		if _, err := encodeFrames(clip, frames, &po, rd, out); err != nil {
			return err
		}
		st, _ := os.Stat(out)
		scales := []int{1, 2, 4, 8, 16}
		errE := make([]float64, len(scales))
		detE := make([]float64, len(scales))
		cnt := 0
		m := rd.Meta
		err := decodeArchive(po.FFmpeg, out, &m, func(j int, buf []byte, lay pixLayout) error {
			if j%4 != 2 {
				return nil
			}
			fr, err := clip.frame(frames[j])
			if err != nil {
				return err
			}
			_, sy, _, _, _, _, err := decodePlanes(fr, 1, planesLuma)
			if err != nil {
				return err
			}
			ay, _, _ := unpackYUV(buf, m.Width, m.Height, lay)
			for k, s := range scales {
				a, bw, bh := blockAvg(ay, m.Width, m.Height, s)
				b, _, _ := blockAvg(sy, m.Width, m.Height, s)
				c, cw, _ := blockAvg(sy, m.Width, m.Height, s*4)
				var e, d float64
				for y := 0; y < bh/4*4; y++ {
					for x := 0; x < bw/4*4; x++ {
						df := a[y*bw+x] - b[y*bw+x]
						e += df * df
						dd := b[y*bw+x] - c[(y/4)*cw+x/4]
						d += dd * dd
					}
				}
				errE[k] += e / float64(bw*bh)
				detE[k] += d / float64(bw*bh)
			}
			cnt++
			return nil
		})
		if err != nil {
			return err
		}
		fmt.Printf("q%-2d intra=%v %5.1f%% size |", q, intra, 100*float64(st.Size())*float64(n)/float64(len(frames))/float64(clip.VideoBytes()))
		for k, s := range scales {
			fmt.Printf("  s%-2d err %6.2f det %7.2f kept %5.1f%%", s, errE[k]/float64(cnt), detE[k]/float64(cnt), 100*(1-errE[k]/detE[k]))
		}
		fmt.Println()
	}
	return nil
}

// LabRequant reports, for sampled frames requantised at each scale, the compression ratio against
// unprocessed sensor data and the closeness to the source.
func LabRequant(src string, scales []float64, tilted bool) error {
	clip, err := OpenClip(src)
	if err != nil {
		return err
	}
	defer clip.Close()
	idx := sampleFrames(clip.Frames(), 8)
	for _, k := range scales {
		var srcB, outB, y, c, lost, detail, raw float64
		for _, i := range idx {
			fr, err := clip.frame(i)
			if err != nil {
				return err
			}
			fi, Y, U, V, _, _, err := decodePlanes(fr, 1, planesAll)
			if err != nil {
				return err
			}
			out, err := requantFrame(fr, k, 0)
			if tilted {
				out, err = requantTilt(fr, 1, k-1, 0)
			}
			if err != nil {
				return err
			}
			_, ry, ru, rv, _, _, err := decodePlanes(out, 1, planesAll)
			if err != nil {
				return err
			}
			pw, ph := clip.pictureSize(fi.w, fi.h)
			raw += rawFrameBytes(pw, ph)
			srcB += float64(len(fr))
			outB += float64(len(out))
			y += psnr12(ry, Y)
			c += (psnr12(ru, U) + psnr12(rv, V)) / 2
			l, d := detailEnergy(Y, ry, fi.w, fi.h)
			lost, detail = lost+l, detail+d
		}
		n := float64(len(idx))
		fmt.Printf("x%-5g %5.1f%% of source  %6.1f:1 (source %.1f:1)  Y %.1f dB  C %.1f dB  detail kept %.1f%%\n",
			k, 100*outB/srcB, raw/outB, raw/srcB, y/n, c/n, detailKept(lost, detail))
	}
	return nil
}

// LabLUTCheck fits a viewing LUT for a clip and writes three pictures of one frame for a visual
// check: the archive as a player shows it, the same through the LUT, and Blackmagic's decode.
func LabLUTCheck(src string, chromaScale float64, bits, frame int, prefix string) error {
	clip, err := OpenClip(src)
	if err != nil {
		return err
	}
	defer clip.Close()
	res, err := FitLUT(nil, clip, chromaScale, bits, prefix+".cube", nil)
	if err != nil {
		return err
	}
	fmt.Printf("%+v\n", *res)
	l, err := readCube(prefix + ".cube")
	if err != nil {
		return err
	}
	fr, err := clip.frame(frame)
	if err != nil {
		return err
	}
	fi, Y, U, V, _, _, err := decodePlanes(fr, chromaScale, planesAll)
	if err != nil {
		return err
	}
	arc := playerRGB(Y, U, V, fi.w, fi.h, bits)
	return referenceFrames(nil, src, []int{frame}, func(_ int, ref *rgbImage) error {
		ox, oy, _ := findOffset(arc, ref)
		raw, out := newRGB(ref.w, ref.h), newRGB(ref.w, ref.h)
		for y := 0; y < ref.h; y++ {
			for x := 0; x < ref.w; x++ {
				k, a := y*ref.w+x, (oy+y)*arc.w+ox+x
				raw.r[k], raw.g[k], raw.b[k] = arc.r[a], arc.g[a], arc.b[a]
				out.r[k], out.g[k], out.b[k] = l.apply(arc.r[a], arc.g[a], arc.b[a])
			}
		}
		for name, im := range map[string]*rgbImage{"_archive.ppm": raw, "_lut.ppm": out, "_blackmagic.ppm": ref} {
			f, err := os.Create(prefix + name)
			if err != nil {
				return err
			}
			fmt.Fprintf(f, "P6\n%d %d\n255\n", im.w, im.h)
			buf := make([]byte, 0, 3*im.w*im.h)
			q := func(v float32) byte {
				if v <= 0 {
					return 0
				}
				if v >= 1 {
					return 255
				}
				return byte(v*255 + 0.5)
			}
			for k := range im.r {
				buf = append(buf, q(im.r[k]), q(im.g[k]), q(im.b[k]))
			}
			f.Write(buf)
			f.Close()
		}
		return nil
	})
}

// LabCrop measures detail kept against quality level on a cw x ch crop from the middle of the
// clip's planes, over two stretches of 12 frames, with the encoder settings pack uses for the whole
// picture (zerolatency for 8K and larger). The crop keeps ffmpeg's memory small enough to measure
// 12K clips on an ordinary machine. Detail kept is given at the usual scale (2x2 averages against
// their 8x8 neighbourhood) and at 4x4 and 6x6, which at 12K cover what 2x2 covers at 8K and 4K.
func LabCrop(src, encoder, speed string, qualities []int, cw, ch int, intra bool) error {
	clip, err := OpenClip(src)
	if err != nil {
		return err
	}
	defer clip.Close()
	n := clip.Frames()
	var frames []int
	for s := 0; s < 2; s++ {
		start := s * max(0, n-12)
		for i := 0; i < 12 && start+i < n; i++ {
			frames = append(frames, start+i)
		}
	}
	what := planesChroma
	if fi := clip.firstFrame(); fi != nil && fi.version == 5 {
		what = planesAll
	}
	var maxDev float64
	var w, h int
	for _, i := range sampleFrames(n, 8) {
		fr, err := clip.frame(i)
		if err != nil {
			return err
		}
		fi, _, _, _, _, st, err := decodePlanes(fr, 1, what)
		if err != nil {
			return err
		}
		w, h = fi.w, fi.h
		maxDev = math.Max(maxDev, st.maxChroma)
	}
	cs := chooseChromaScale(maxDev)
	cw, ch = min(cw, w)&^15, min(ch, h)&^15
	x0, y0 := (w-cw)/2&^15, (h-ch)/2&^15
	fmt.Printf("%s: %dx%d, colour scale %.3f, camera step %.3f; %dx%d crop at %d,%d, %d frames, %s %s intra=%v\n",
		filepath.Base(src), w, h, cs, clipStep(clip, cs), cw, ch, x0, y0, len(frames), encoder, speed, intra)
	type crop struct{ y, u, v []uint16 }
	var crops []crop
	var srcBytes float64
	for _, i := range frames {
		fr, err := clip.frame(i)
		if err != nil {
			return err
		}
		srcBytes += float64(len(fr))
		_, Y, U, V, _, _, err := decodePlanes(fr, cs, planesAll)
		if err != nil {
			return err
		}
		c := crop{make([]uint16, cw*ch), make([]uint16, cw*ch/4), make([]uint16, cw*ch/4)}
		for y := 0; y < ch; y++ {
			copy(c.y[y*cw:(y+1)*cw], Y[(y0+y)*w+x0:])
		}
		for y := 0; y < ch/2; y++ {
			copy(c.u[y*cw/2:(y+1)*cw/2], U[(y0/2+y)*(w/2)+x0/2:])
			copy(c.v[y*cw/2:(y+1)*cw/2], V[(y0/2+y)*(w/2)+x0/2:])
		}
		crops = append(crops, c)
	}
	srcBytes *= float64(cw*ch) / float64(w*h) // the source's bytes for the cropped area, in proportion
	dir, err := os.MkdirTemp("", "braw-lab-crop-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	lay, back := planar12, planar12
	if encoder == "nvenc" {
		lay, back = semi10, planar10
	}
	fmt.Println("  quality   archive   detail kept at 2x2   4x4     6x6")
	for _, q := range qualities {
		po := PackOptions{Quality: q, Encoder: encoder, Speed: speed, Intra: intra}
		if err := po.defaults(); err != nil {
			return err
		}
		out := filepath.Join(dir, fmt.Sprintf("q%d.mkv", q))
		args := []string{"-hide_banner", "-loglevel", "error", "-y", "-f", "rawvideo", "-pix_fmt", lay.pixFmt(),
			"-s", fmt.Sprintf("%dx%d", cw, ch), "-framerate", "24", "-i", "-"}
		args = append(append(args, encoderArgs(&po, w, h)...), "-f", "matroska", out)
		enc := command(po.FFmpeg, args...)
		var stderr bytes.Buffer
		enc.Stderr = &stderr
		in, err := enc.StdinPipe()
		if err != nil {
			return err
		}
		if err := enc.Start(); err != nil {
			return err
		}
		for _, c := range crops {
			if err := writeFrame(in, packYUV(c.y, c.u, c.v, lay)); err != nil {
				return fmt.Errorf("quality %d: %v: %s", q, err, stderr.String())
			}
		}
		in.Close()
		if err := enc.Wait(); err != nil {
			return fmt.Errorf("quality %d: %v: %s", q, err, stderr.String())
		}
		st, err := os.Stat(out)
		if err != nil {
			return err
		}
		dec := command(po.FFmpeg, "-hide_banner", "-loglevel", "error", "-i", out, "-f", "rawvideo", "-pix_fmt", back.pixFmt(), "-")
		dec.Stderr = &stderr
		rd, err := dec.StdoutPipe()
		if err != nil {
			return err
		}
		if err := dec.Start(); err != nil {
			return err
		}
		buf := make([]byte, cw*ch*3)
		var lost, det [3]float64
		for _, c := range crops {
			if _, err := io.ReadFull(rd, buf); err != nil {
				dec.Process.Kill()
				return fmt.Errorf("quality %d: %v: %s", q, err, stderr.String())
			}
			ay, _, _ := unpackYUV(buf, cw, ch, back)
			for k, s := range []int{2, 4, 6} {
				l, d := detailAt(c.y, ay, cw, ch, s)
				lost[k] += l
				det[k] += d
			}
		}
		dec.Wait()
		fmt.Printf("  %7d   %6.1f%%   %18.1f%%   %5.1f%%  %5.1f%%\n", q, 100*float64(st.Size())/srcBytes,
			detailKept(lost[0], det[0]), detailKept(lost[1], det[1]), detailKept(lost[2], det[2]))
	}
	return nil
}

// detailAt is detailEnergy at another scale: s x s averages against their 4s x 4s neighbourhood
// (detailEnergy is s = 2).
func detailAt(src, arc []uint16, w, h, s int) (lost, detail float64) {
	a, bw, bh := blockAvg(arc, w, h, s)
	b, _, _ := blockAvg(src, w, h, s)
	c, cw, _ := blockAvg(src, w, h, 4*s)
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
