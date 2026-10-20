package core

// Two-layer archives.
//
// The NVIDIA encoder codes 10-bit pictures only, and in its predicted frames it keeps an error of
// about one 10-bit code (4 plane values) whatever the level, while a finely quantised camera (an
// URSA Cine 12K codes steps of under one plane value) records detail well below that. Rounding
// the planes to 10 bits is not what loses it: with no codec at all, that keeps 98-99% of the
// detail. Such clips can therefore be stored in two layers:
//
//   - the base: the planes rounded to 10 bits, coded cheaply (layerBaseQuality). It only has to
//     keep the second layer within range, and the detail kept hardly depends on its level;
//   - the detail layer: layerZero plus the difference between the source's 12-bit planes and the
//     base as decoded (its 10-bit values times 4), in 12-bit units, cut to the 10-bit range and
//     coded at the archive's quality level. The encoder's floor of one code then costs a quarter
//     as much.
//
// The picture is base*4 + detail - layerZero, cut to 12 bits. HEVC decoding is exact (the GPU's
// decoder and ffmpeg's own give the same samples), so the base a reader decodes is the one the
// difference was taken from. Each layer has a track per tile: the base tracks first, so that a
// player shows a picture. docs/braw-v5.md has the measurements.

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	// layerBaseQuality is the base layer's level. Levels 10 to 28 gave the same detail kept to
	// within half a point; 22 costs 1-3% of the clip.
	layerBaseQuality = 22
	// layerZero is the detail layer's value for no difference.
	layerZero = 512
	// layerQualityOffset is how far above x265's automatic level the detail layer's automatic
	// level lies. On 24 frames of the URSA Cine 12K test clip, x265 kept 93.1% of the detail at 4
	// (28.8% of the clip) and 85.0% at 10 (6.1%); two layers kept 93.0% at 16 (32.3%) and 85.4%
	// at 22 (7.7%).
	layerQualityOffset = 12
)

// How a two-layer picture's work is spread over the GPU. The tiles of a 12K picture each need an
// encoder while a layer is coded, and a decoder per layer while the picture is decoded. On an 8 GB
// card the four encoders of one layer, or four decoders (1.8 GB each at 6168x4032), already fill
// it, and eight decoders pushed over 5 GB into system memory. The base, coded coarsely, decodes
// fast on the CPU (about 50 ms a tile-frame with two threads), so it is decoded there and the GPU
// carries what it would for a picture of one layer.
var (
	// detailGroup is how many tiles' detail layers are made at once, each with a decoder of its
	// base and an encoder. The source is decoded again for each group.
	detailGroup = 4
	// baseOnGPU has base tracks decoded on the GPU. Decoded on the CPU, they give the same samples.
	baseOnGPU = false
)

// AutoLayers, as PackOptions.Layers, has Pack choose the number of layers from the clip. It is
// the default.
const AutoLayers = 0

// autoLayers is the number of layers chosen for a clip made with encoder: two on the NVIDIA
// encoder for clips of an RGBW sensor (Blackmagic RAW version 5), one otherwise.
// This is a consistent default across native and scaled RGBW recording modes;
// resolution and the measured benefit do not change the layer count.
// Those cameras code finer detail than one GPU layer keeps: on the 12K test clip two layers kept
// 91.1% of it against 73.5%. Other cameras already keep over 90% in one layer and gain nothing
// (98.9% against 98.8% on a Pocket Cinema Camera 4K clip). Scaled RGBW recordings use the
// same path with geometry-driven tiling; docs/braw-v5-scaled.md and braw-v5-8k.md record results.
func autoLayers(encoder string, rgbw bool) int {
	if encoder == "nvenc" && rgbw {
		return 2
	}
	return 1
}

// autoLayerQuality is the detail layer's level for a clip of camera step s (see sourceStep).
func autoLayerQuality(s float64) int { return autoQuality(s, "x265") + layerQualityOffset }

// layers is how many layers the picture is stored in.
func (m *Meta) layers() int {
	if m.Layers == 2 {
		return 2
	}
	return 1
}

// setLayers records how many layers the picture is stored in, with the base's level.
func (m *Meta) setLayers(layers, baseQuality int) {
	m.Layers, m.LayerGain, m.BaseQuality, m.ResidualClipped = 0, 0, 0, 0
	if layers == 2 {
		m.Layers, m.LayerGain, m.BaseQuality = 2, 1, baseQuality
	}
	m.setFormat()
}

// trackOnGPU says whether video track k of an archive is decoded on the GPU when gpu is asked for.
func trackOnGPU(m *Meta, gpu bool, k int) bool {
	if !gpu || m.Bits != 10 {
		return false
	}
	tiles := m.tracks()
	limit := gpuMaxTracks
	if m.layers() == 2 && baseOnGPU {
		limit /= 2 // both layers then need a decoder for each selected tile
	}
	groups := gpuTileGroups(tiles, limit)
	if m.layers() == 1 {
		// A single layer must use one sample layout for all tiles while stitching.
		return len(groups) == 1
	}
	if k < len(tiles) && !baseOnGPU {
		return false
	}
	// Each layer carries its own layout, so excess detail tiles can use the CPU.
	return k%len(tiles) < len(groups[0])
}

// trackTitles names the video tracks of a picture of several, in order, for players.
func (m *Meta) trackTitles() []string {
	var out []string
	for l := 0; l < m.layers(); l++ {
		for k, t := range m.Tiles {
			s := fmt.Sprintf("Tile %d of %d (%dx%d at %d,%d)", k+1, len(m.Tiles), t.W, t.H, t.X, t.Y)
			if m.layers() == 2 {
				s += [...]string{", base layer", ", detail layer"}[l]
			}
			out = append(out, s)
		}
		if len(m.Tiles) == 0 && m.layers() == 2 {
			out = append(out, [...]string{"Base layer", "Detail layer (difference from the base)"}[l])
		}
	}
	return out
}

// sampleView is where one plane's samples lie in a frame packed in some layout (see packYUV).
type sampleView struct {
	off, stride, step int  // first sample, bytes per row, bytes from one sample to the next
	shift             uint // semi10 keeps its values in the top bits of each word
	top               int  // largest value
}

// viewPlanes describes the three planes of a w x h frame packed in layout l.
func viewPlanes(l pixLayout, w, h int) [3]sampleView {
	if l == semi10 {
		return [3]sampleView{{0, 2 * w, 2, 6, 1023}, {2 * w * h, 2 * w, 4, 6, 1023}, {2*w*h + 2, 2 * w, 4, 6, 1023}}
	}
	top := 4095
	if l == planar10 {
		top = 1023
	}
	return [3]sampleView{{0, 2 * w, 2, 0, top}, {2 * w * h, w, 2, 0, top}, {2*w*h + w*h/2, w, 2, 0, top}}
}

func (v *sampleView) get(b []byte, i int) int {
	return min(int(uint16(b[i])|uint16(b[i+1])<<8)>>v.shift, v.top)
}

// tilePlane is where plane p (0 brightness, 1 and 2 colour) of tile t lies in its plane of the
// whole w-wide picture: origin, size, and the plane's width.
func tilePlane(t Tile, w, p int) (x, y, tw, th, pw int) {
	if p == 0 {
		return t.X, t.Y, t.W, t.H, w
	}
	return t.X / 2, t.Y / 2, t.W / 2, t.H / 2, w / 2
}

// detailTile makes the detail layer of tile t into dst, a tile frame in the NVIDIA encoder's
// layout (semi10): layerZero plus the difference between the source planes src (the whole w-wide
// picture, 12-bit) and the base as decoded (base, the tile in layout lay), cut to 10 bits. It
// returns how many samples were cut.
func detailTile(dst, base []byte, lay pixLayout, src [3][]uint16, w int, t Tile) int64 {
	bv, dv := viewPlanes(lay, t.W, t.H), viewPlanes(semi10, t.W, t.H)
	var clipped atomic.Int64
	forTiles(t.H, func(r int) {
		cut := int64(0)
		for p := 0; p < 3; p++ {
			pr := r
			if p > 0 {
				if r%2 != 0 {
					break
				}
				pr = r / 2
			}
			x, y, tw, _, pw := tilePlane(t, w, p)
			b, d := &bv[p], &dv[p]
			bi, di := b.off+pr*b.stride, d.off+pr*d.stride
			for _, s := range src[p][(y+pr)*pw+x:][:tw] {
				v := layerZero + int(s) - b.get(base, bi)<<2
				if v < 0 {
					v, cut = 0, cut+1
				} else if v > 1023 {
					v, cut = 1023, cut+1
				}
				v <<= 6
				dst[di], dst[di+1] = byte(v), byte(v>>8)
				bi, di = bi+b.step, di+d.step
			}
		}
		if cut > 0 {
			clipped.Add(cut)
		}
	})
	return clipped.Load()
}

// addLayers rebuilds tile t of a two-layer picture into pic, the whole w x h frame in planar12:
// the base (a tile frame in layout lb) times 4, plus the detail layer (in layout ld) less
// layerZero, cut to 12 bits.
func addLayers(pic []byte, w, h int, t Tile, base []byte, lb pixLayout, detail []byte, ld pixLayout) {
	bv, dv, pv := viewPlanes(lb, t.W, t.H), viewPlanes(ld, t.W, t.H), viewPlanes(planar12, w, h)
	forTiles(t.H, func(r int) {
		for p := 0; p < 3; p++ {
			pr := r
			if p > 0 {
				if r%2 != 0 {
					break
				}
				pr = r / 2
			}
			x, y, tw, _, _ := tilePlane(t, w, p)
			b, d := &bv[p], &dv[p]
			bi, di := b.off+pr*b.stride, d.off+pr*d.stride
			pi := pv[p].off + (y+pr)*pv[p].stride + 2*x
			for c := 0; c < tw; c++ {
				v := b.get(base, bi)<<2 + d.get(detail, di) - layerZero
				v = max(0, min(v, 4095))
				pic[pi], pic[pi+1] = byte(v), byte(v>>8)
				bi, di, pi = bi+b.step, di+d.step, pi+2
			}
		}
	})
}

// encodePicture is encodeFrames, storing the picture in two layers when o asks for them: the base
// is coded as a picture of its own, then the detail layer of each tile from the source and the
// decoded base, and the tracks are gathered in out, base first.
func encodePicture(clip *Clip, frames []int, o *PackOptions, rd *restoreData, out string) (maxDev float64, err error) {
	m := &rd.Meta
	if !o.layered() {
		m.setLayers(1, 0)
		return encodeFrames(clip, frames, o, rd, out)
	}
	n := clip.Frames()
	if frames != nil {
		n = len(frames)
	}
	m.setLayers(1, 0)
	baseOut := out + ".base.mkv"
	if kb := o.keepBase; kb != nil && kb.hdrs != nil && kb.cs == m.ChromaScale && kb.frames == n {
		baseOut, maxDev = kb.path, kb.maxDev
		rd.Hdrs, rd.Side = kb.hdrs, kb.side
		m.setTiles(kb.tiles)
	} else {
		// The base is about a third of the work.
		base := *o
		base.Quality = layerBaseQuality
		base.Progress = func(s string, done, total int) { o.Progress(s, done/3, n) }
		if kb != nil {
			baseOut, kb.hdrs = kb.path, nil
		} else {
			defer os.Remove(baseOut)
		}
		if maxDev, err = encodeFrames(clip, frames, &base, rd, baseOut); err != nil {
			return 0, err
		}
		if kb != nil {
			kb.cs, kb.frames, kb.maxDev, kb.tiles = m.ChromaScale, n, maxDev, m.Tiles
			kb.hdrs, kb.side = rd.Hdrs, rd.Side
		}
	}
	m.setLayers(2, layerBaseQuality)
	tiles := m.tracks()
	outs := make([]string, len(tiles))
	for k := range outs {
		outs[k] = out + ".detail" + strconv.Itoa(k) + ".mkv"
		defer os.Remove(outs[k])
	}
	clipped, err := encodeDetail(clip, frames, o, rd, baseOut, outs, func(done int) { o.Progress("encode", n/3+done*2/3, n) })
	if err != nil {
		return 0, err
	}
	var samples float64
	for _, t := range tiles {
		samples += 1.5 * float64(t.W) * float64(t.H) * float64(n)
	}
	m.ResidualClipped = round(100*float64(clipped)/samples, 6)
	args := []string{"-y", "-i", baseOut}
	for _, f := range outs {
		args = append(args, "-i", f)
	}
	for k := 0; k <= len(outs); k++ {
		args = append(args, "-map", strconv.Itoa(k)+":v")
	}
	if err := runFFmpeg(o.FFmpeg, append(args, "-c", "copy", "-f", "matroska", out)...); err != nil {
		return 0, err
	}
	o.Progress("encode", n, n)
	return maxDev, nil
}

// layerBase is a base layer kept for the next picture of the same frames at another level.
type layerBase struct {
	path       string
	cs         float64
	frames     int
	hdrs, side [][]byte
	maxDev     float64
	tiles      []Tile
}

// encodeDetail codes the detail layer of every tile of rd's picture into outs, one file per tile,
// detailGroup tiles at a time, from the source and the base (the file base). Should a group fail
// in the encoder or in the GPU, its tiles are tried again one at a time; if one still fails, the
// error is an encoderFailure. progress is told how many frames' worth of the work is done. It
// returns how many detail samples were cut to the layer's range.
func encodeDetail(clip *Clip, frames []int, o *PackOptions, rd *restoreData, base string, outs []string, progress func(done int)) (clipped int64, err error) {
	n := clip.Frames()
	if frames != nil {
		n = len(frames)
	}
	tiles := rd.Meta.tracks()
	var groups [][]int
	k := 0
	for _, group := range gpuTileGroups(tiles, min(detailGroup, gpuMaxTracks)) {
		var g []int
		for range group {
			g = append(g, k)
			k++
		}
		groups = append(groups, g)
	}
	for gi, g := range groups {
		report := func(done int) { progress((gi*n + done) / len(groups)) }
		c, err := detailPass(clip, frames, o, rd, base, outs, g, report)
		var ef encoderFailure
		if err != nil && errors.As(err, &ef) && len(g) > 1 {
			// Most likely the GPU had no room for this many sessions at once.
			c = 0
			for _, k := range g {
				ck, err := detailPass(clip, frames, o, rd, base, outs, []int{k}, report)
				if err != nil {
					return 0, err
				}
				c += ck
			}
		} else if err != nil {
			return 0, err
		}
		clipped += c
	}
	return clipped, nil
}

// detailPass codes the detail layer of the listed tiles, each with its own decoder of the base and
// its own encoder, from one pass over the source.
func detailPass(clip *Clip, frames []int, o *PackOptions, rd *restoreData, base string, outs []string, ks []int, progress func(done int)) (clipped int64, err error) {
	m := &rd.Meta
	n := clip.Frames()
	if frames != nil {
		n = len(frames)
	}
	src := func(j int) int {
		if frames != nil {
			return frames[j]
		}
		return j
	}
	tiles := m.tracks()
	// The base file's description: one layer, so that its tracks are decoded as they are.
	bm := *m
	bm.setLayers(1, 0)
	bm.Frames = n
	var ends [][]int64 // fed as decodeArchiveWith feeds them
	if e, err := videoBlockEnds(base); err == nil && len(e) >= len(tiles) && len(m.Tiles) > 0 {
		ends = e
	}
	st, err := os.Stat(base)
	if err != nil {
		return 0, err
	}
	type tileWork struct {
		k    int
		dec  *trackDecoder
		enc  *encoderPipe
		in   chan [3][]uint16
		out  []byte
		mu   sync.Mutex
		err  error // the first error met by the tile's worker
		cut  int64
		sent atomic.Int64 // frames sent to the encoder
	}
	failed := func(t *tileWork) error {
		t.mu.Lock()
		defer t.mu.Unlock()
		return t.err
	}
	done := make(chan struct{})
	var work []*tileWork
	var wg sync.WaitGroup
	// finish stops the workers, the encoders (killed when the work stopped early) and the
	// decoders, and returns the error that says best why the work stopped, if it did.
	finish := func(err error) error {
		for _, t := range work {
			if t.in != nil {
				close(t.in)
			}
		}
		wg.Wait()
		encErr := make([]error, len(work))
		for i, t := range work {
			if t.enc != nil {
				encErr[i] = t.enc.finish(err != nil || failed(t) != nil)
			}
		}
		close(done)
		for _, t := range work {
			if t.dec != nil {
				t.dec.out.Close()
				t.dec.cmd.Process.Kill()
				t.dec.cmd.Wait()
			}
		}
		if err == ErrCanceled {
			return err
		}
		var ef encoderFailure
		for _, t := range work {
			if e := failed(t); errors.As(e, &ef) {
				return e
			}
		}
		for i, t := range work {
			if t.enc != nil && t.enc.writeErr != nil {
				return encErr[i] // the encoder stopped taking frames: its own error output says why
			}
		}
		if err != nil {
			return err
		}
		for _, t := range work {
			if e := failed(t); e != nil {
				return e
			}
		}
		for _, e := range encErr {
			if e != nil {
				return e
			}
		}
		return nil
	}
	for _, k := range ks {
		t := &tileWork{k: k}
		work = append(work, t)
		var e []int64
		if ends != nil {
			e = ends[k]
		}
		if t.dec, err = startTrackDecoder(o.FFmpeg, base, &bm, k, trackOnGPU(&bm, baseOnGPU, k), true, e, st.Size(), done); err != nil {
			return 0, finish(err)
		}
		tl := tiles[k]
		paced := bigPicture(tl.W, tl.H)
		args := []string{"-hide_banner", "-loglevel", "error", "-y", "-f", "rawvideo", "-pix_fmt", semi10.pixFmt(),
			"-s", fmt.Sprintf("%dx%d", tl.W, tl.H), "-framerate", fmt.Sprintf("%d/%d", m.FpsNum, m.FpsDen), "-i", "-"}
		if paced {
			args = append([]string{"-progress", "pipe:1"}, args...)
		}
		args = append(args, encoderArgs(o, tl.W, tl.H)...)
		if len(m.Tiles) > 0 {
			args = append(args, "-bf", "0") // as for the base (see encodeFrames)
		}
		if t.enc, err = startEncoder(o.FFmpeg, append(args, "-f", "matroska", outs[k]), paced); err != nil {
			return 0, finish(err)
		}
		t.in = make(chan [3][]uint16, 1)
		t.out = make([]byte, planeBytes(tl.W, tl.H))
		t.dec.buf = make([]byte, planeBytes(tl.W, tl.H))
		wg.Add(1)
		go func(t *tileWork) {
			defer wg.Done()
			fail := func(err error) {
				t.mu.Lock()
				t.err = err
				t.mu.Unlock()
			}
			for p := range t.in {
				if failed(t) != nil {
					continue // the frames still sent are dropped
				}
				j := int(t.sent.Load())
				t.dec.feed.want()
				if err := readFrame(t.dec.out, t.dec.buf); err != nil {
					fail(encoderFailure{fmt.Errorf("base layer decode failed at frame %d: %v: %s", j, err, strings.TrimSpace(t.dec.stderr.String()))})
					continue
				}
				t.dec.feed.got()
				t.cut += detailTile(t.out, t.dec.buf, t.dec.lay, p, m.Width, tiles[t.k])
				if err := t.enc.send(o.Ctx, j, t.out); err != nil {
					fail(err)
					continue
				}
				t.sent.Add(1)
			}
		}(t)
	}
	err = orderedFrames(n, frameSlots(m.Width, m.Height), func(j int) ([3][]uint16, error) {
		fr, err := clip.frame(src(j))
		if err != nil {
			return [3][]uint16{}, err
		}
		fi, Y, U, V, _, _, err := decodePlanes(fr, m.ChromaScale, planesAll)
		if err != nil {
			return [3][]uint16{}, err
		}
		if fi.w != m.Width || fi.h != m.Height {
			return [3][]uint16{}, fmt.Errorf("resolution changes")
		}
		return [3][]uint16{Y, U, V}, nil
	}, func(j int, p [3][]uint16) error {
		if err := alive(o.Ctx); err != nil {
			return err
		}
		least := j
		for _, t := range work {
			if err := failed(t); err != nil {
				return err
			}
			t.in <- p
			least = min(least, int(t.sent.Load()))
		}
		progress(least)
		return nil
	})
	if err := finish(err); err != nil {
		return 0, err
	}
	for _, t := range work {
		clipped += t.cut
	}
	progress(n)
	return clipped, nil
}
