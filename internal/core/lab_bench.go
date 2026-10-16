package core

import (
	"fmt"
	"os"
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
