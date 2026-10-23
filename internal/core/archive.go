package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Version is the tool version written into archives.
const Version = "0.3.0"

// ArchiveSuffix is appended to the clip name for packed files.
const ArchiveSuffix = ".braw.mkv"

// Progress reports a stage name and how many of total frames are done.
type Progress func(stage string, done, total int)

// claim checks that dst may be written. It is never the source itself, and an existing file is
// only accepted when replacing was asked for.
func claim(src, dst string, replace bool) error {
	d, err := os.Stat(dst)
	if err != nil {
		return nil
	}
	if s, err := os.Stat(src); err == nil && os.SameFile(s, d) {
		return fmt.Errorf("the destination is the source file itself: %s", dst)
	}
	if !replace {
		return fmt.Errorf("destination exists, refusing to overwrite: %s", dst)
	}
	if d.IsDir() {
		return fmt.Errorf("destination is a folder: %s", dst)
	}
	return nil
}

// ErrCanceled is returned when the caller's context ends before the work does.
var ErrCanceled = fmt.Errorf("canceled")

// alive reports ErrCanceled once ctx (which may be nil) is done.
func alive(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return ErrCanceled
	}
	return nil
}

// PackOptions controls Pack.
type PackOptions struct {
	Ctx      context.Context // optional; cancelling it stops the work and removes partial output
	Src, Dst string
	// Quality is the constant quantiser; lower = better and larger. AutoQuality (0, the default)
	// chooses it from how finely the camera quantised the clip (see autoQuality).
	Quality int
	Speed   string // fast | balanced | slow
	Encoder string // nvenc | x265
	// Intra codes every frame on its own. Files are much larger, but the quality level then holds
	// for every frame; with frames predicted from their neighbours, encoders stop spending bits
	// on fine noise however low the level is set.
	Intra bool
	// Layers is 2 to store the picture as a base and a detail layer (nvenc only, see layers.go):
	// the GPU then keeps about as much fine detail of finely coded clips as x265, in larger
	// archives, and Quality is the detail layer's level. 1 is a single layer. AutoLayers (0, the
	// default) chooses for the clip (see autoLayers).
	Layers int
	FFmpeg string // ffmpeg executable; "" = the one beside the program, else "ffmpeg" on PATH
	// Replace allows an existing destination to be replaced. It is replaced only at the very
	// end, by a finished and checked file; a failed or cancelled run leaves it as it was.
	Replace bool
	// LUT also writes a viewing LUT beside the archive (see FitLUT). It needs Blackmagic RAW
	// installed.
	LUT bool
	// Verify compares every frame of the archive with the source before reporting success.
	// Without it only a few sample frames are compared (enough to measure the rebuild settings)
	// and the rest are just counted, which is faster.
	Verify   bool
	Progress Progress

	// step is the clip's sourceStep, kept when Quality was chosen from it so that it can be chosen
	// again for another encoder or colour scale.
	step float64
	// keepBase, when set, keeps the base layer of a two-layer picture for the next picture of
	// the same frames (see encodePicture).
	keepBase *layerBase
}

// PackResult summarises a finished archive.
type PackResult struct {
	SourceSize, ArchiveSize int64
	Frames                  int
	ChromaScale             float64
	FpsNum, FpsDen          int
	Verify                  *VerifyResult
	Meta                    Meta
	// LUT describes the viewing LUT written beside the archive, when one was asked for. A LUT
	// that could not be made does not fail the archive: LUTError then says why.
	LUT      *LUTResult
	LUTError string
	// GPUFallback is set when the NVIDIA encoder failed on this clip and x265 made the archive
	// instead (at 12 bits, so Meta says x265): it is the GPU's reason.
	GPUFallback string
	// LayersAuto is set when the number of layers (Meta.Layers) was chosen for the clip (see
	// autoLayers) rather than asked for.
	LayersAuto bool
}

func (o *PackOptions) defaults() error {
	if o.Speed == "" {
		o.Speed = "balanced"
	}
	if o.Encoder == "" {
		o.Encoder = DefaultEncoder()
	}
	if o.FFmpeg == "" {
		o.FFmpeg = FindFFmpeg()
	}
	if o.Quality != AutoQuality && (o.Quality < 1 || o.Quality > 51) {
		return fmt.Errorf("quality must be between 1 and 51")
	}
	if _, ok := speedPresets[o.Speed]; !ok {
		return fmt.Errorf("speed must be fast, balanced or slow")
	}
	if o.Encoder != "nvenc" && o.Encoder != "x265" {
		return fmt.Errorf("encoder must be nvenc or x265")
	}
	if o.Layers < 0 || o.Layers > 2 {
		return fmt.Errorf("layers must be auto, 1 or 2")
	}
	if o.Layers == 2 && o.Encoder != "nvenc" {
		return fmt.Errorf("two layers are made with the nvenc encoder only")
	}
	return nil
}

// layered says whether the picture is to be stored in two layers.
func (o *PackOptions) layered() bool { return o.Layers == 2 && o.Encoder == "nvenc" }

// autoQuality is the level chosen for a clip of camera step s (see sourceStep) with these options.
func (o *PackOptions) autoQuality(s float64) int {
	if o.layered() {
		return autoLayerQuality(s)
	}
	return autoQuality(s, o.Encoder)
}

// speed -> {nvenc preset, x265 preset}
var speedPresets = map[string][2]string{
	"fast":     {"p3", "faster"},
	"balanced": {"p5", "medium"},
	"slow":     {"p7", "slow"},
}

// archiveNote is written into every archive so that whoever finds one later knows where to get
// the tool that restores it.
const archiveNote = "Packed Blackmagic RAW clip. Restore the original .braw with braw-pack: https://github.com/laurentchicoine/braw-pack"

// Colour tags are written as stream metadata only. Passing them as ordinary ffmpeg colour options
// makes ffmpeg rescale the sample values, which would corrupt the stored sensor data.
const hevcTags = "hevc_metadata=video_full_range_flag=1:colour_primaries=1:transfer_characteristics=1:matrix_coefficients=1"

func encoderArgs(o *PackOptions, w, h int) []string {
	p := speedPresets[o.Speed]
	if o.Encoder == "x265" {
		params := "log-level=error"
		if o.Intra {
			params += ":keyint=1"
		}
		args := []string{"-c:v", "libx265", "-preset", p[1], "-qp", strconv.Itoa(o.Quality)}
		if bigPicture(w, h) {
			// x265 then hands back each frame as soon as it is coded (no B-frames, no
			// look-ahead), so the feed can be held close to it (see paceWindow): at 12K every
			// frame ffmpeg and x265 hold costs about 1.5 GB. The archive is somewhat larger.
			args = append(args, "-tune", "zerolatency")
		}
		return append(args, "-pix_fmt", "yuv420p12le", "-x265-params", params, "-bsf:v", hevcTags)
	}
	args := []string{"-c:v", "hevc_nvenc", "-preset", p[0], "-tune", "hq", "-rc", "constqp",
		"-qp", strconv.Itoa(o.Quality), "-pix_fmt", "p010le", "-bsf:v", hevcTags}
	if o.Intra {
		args = append(args, "-g", "0")
	}
	return args
}

func runFFmpeg(bin string, args ...string) error {
	cmd := command(bin, append([]string{"-hide_banner", "-loglevel", "error", "-nostdin"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg failed: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// frameSlots is how many frames of a w x h picture may be worked on at once: two per CPU, but no
// more than fit in about 2 GB, since every frame in flight holds its planes and their packed
// samples (about 600 MB for a 12K frame). A frame that large is decoded with every CPU anyway
// (see forTiles), and x265 needs much of the memory at that size.
func frameSlots(w, h int) int {
	n := runtime.NumCPU() * 2
	if per := 6 * int64(w) * int64(h); per > 0 && int64(n)*per > 2<<30 {
		n = int((2 << 30) / per)
	}
	return max(n, 2)
}

// firstFrame describes the clip's first frame; nil if it cannot be read.
func (c *Clip) firstFrame() *frameInfo {
	if fr, err := c.frame(0); err == nil {
		if fi, err := parseFrame(fr); err == nil {
			return fi
		}
	}
	return nil
}

// slots is frameSlots for the clip's picture size.
func (c *Clip) slots() int {
	if fi := c.firstFrame(); fi != nil {
		return frameSlots(fi.w, fi.h)
	}
	return frameSlots(0, 0)
}

// orderedFrames runs fn over frames [0,n) in parallel, at most slots at a time, and delivers
// results to sink in order.
func orderedFrames[T any](n, slots int, fn func(i int) (T, error), sink func(i int, v T) error) error {
	type res struct {
		v   T
		err error
	}
	out := make([]chan res, n)
	for i := range out {
		out[i] = make(chan res, 1)
	}
	sem := make(chan struct{}, slots)
	stop := make(chan struct{})
	go func() {
		for i := 0; i < n; i++ {
			select {
			case sem <- struct{}{}:
			case <-stop:
				return
			}
			go func(i int) {
				var r res
				defer func() { out[i] <- r }()
				defer rescue(&r.err)
				r.v, r.err = fn(i)
			}(i)
		}
	}()
	defer close(stop)
	for i := 0; i < n; i++ {
		r := <-out[i]
		<-sem
		if r.err != nil {
			return fmt.Errorf("frame %d: %w", i, r.err)
		}
		if err := sink(i, r.v); err != nil {
			return err
		}
	}
	return nil
}

// chooseChromaScale returns the largest colour scale (in 1/64 steps) that keeps every colour
// sample inside the 12-bit range, with a small margin.
func chooseChromaScale(maxDev float64) float64 {
	if maxDev+8 <= 2047 {
		return 1
	}
	s := math.Floor(2047/(maxDev+8)*64) / 64
	if s < 0.25 {
		s = 0.25
	}
	return s
}

// colourRange returns the picture size and the largest colour deviation (planeStats.maxChroma)
// of the listed frames (nil = every frame). Only the colour planes are transformed; brightness
// is not needed to choose the scale, except for version 5 clips, where the scale covers all
// three planes (see decodePlanesV5).
func colourRange(clip *Clip, frames []int, o *PackOptions) (w, h int, maxDev float64, err error) {
	n := clip.Frames()
	if frames != nil {
		n = len(frames)
	}
	what := planesChroma
	if fi := clip.firstFrame(); fi != nil && fi.version == 5 {
		what = planesAll
	}
	type geom struct {
		w, h   int
		maxDev float64
	}
	err = orderedFrames(n, clip.slots(), func(j int) (geom, error) {
		i := j
		if frames != nil {
			i = frames[j]
		}
		fr, err := clip.frame(i)
		if err != nil {
			return geom{}, err
		}
		fi, _, _, _, _, st, err := decodePlanes(fr, 1, what)
		if err != nil {
			return geom{}, err
		}
		return geom{fi.w, fi.h, st.maxChroma}, nil
	}, func(j int, g geom) error {
		if j == 0 {
			w, h = g.w, g.h
		} else if g.w != w || g.h != h {
			return fmt.Errorf("the clip changes resolution part-way")
		}
		maxDev = math.Max(maxDev, g.maxDev)
		if err := alive(o.Ctx); err != nil {
			return err
		}
		o.Progress("analyse", j+1, n)
		return nil
	})
	return w, h, maxDev, err
}

// Pack converts a .braw into a self-contained archive. The source is only read.
func Pack(o PackOptions) (res *PackResult, err error) {
	defer rescue(&err)
	if err := o.defaults(); err != nil {
		return nil, err
	}
	if o.Progress == nil {
		o.Progress = func(string, int, int) {}
	}
	if err := claim(o.Src, o.Dst, o.Replace); err != nil {
		return nil, err
	}
	clip, err := OpenClip(o.Src)
	if err != nil {
		return nil, err
	}
	defer clip.Close()
	n := clip.Frames()
	if n == 0 {
		return nil, fmt.Errorf("clip has no frames")
	}

	// Gate: this file's bitstream must survive a decode/re-encode of sampled frames unchanged.
	for _, i := range sampleFrames(n, 6) {
		fr, err := clip.frame(i)
		if err != nil {
			return nil, err
		}
		out, err := requantFrame(fr, 1, 0)
		if err != nil {
			return nil, fmt.Errorf("unsupported BRAW variant (frame %d): %w", i, err)
		}
		if !bytes.Equal(out, fr[:min(len(out), len(fr))]) || len(out) > len(fr) {
			return nil, fmt.Errorf("unsupported BRAW variant: frame %d does not round-trip exactly", i)
		}
	}

	// Pass 1: geometry and the colour range used by this clip, measured on a spread of frames.
	// Most clips need no colour scaling, and the encoding pass confirms that on every frame. A
	// clip that does need it is measured in full, since the scale depends on its strongest frame.
	w, h, maxDev, err := colourRange(clip, sampleFrames(n, max(24, n/16)), &o)
	if err == nil && chooseChromaScale(maxDev) < 1 {
		w, h, maxDev, err = colourRange(clip, nil, &o)
	}
	if err != nil {
		return nil, err
	}
	cs := chooseChromaScale(maxDev)
	// The camera's step is recorded in any case, and chooses the layers and the level unless they
	// were given.
	step := clipStep(clip, cs)
	autoLayered := o.Layers == AutoLayers
	if autoLayered {
		fi := clip.firstFrame()
		o.Layers = autoLayers(o.Encoder, fi != nil && fi.version == 5)
	}
	auto := o.Quality == AutoQuality
	if auto {
		o.step = step
		o.Quality = o.autoQuality(step)
	}
	num, den, _, err := clip.SensorRate()
	if err != nil {
		return nil, err
	}
	bits := 10
	if o.Encoder == "x265" {
		bits = 12
	}

	rd := &restoreData{Pre: clip.pre, Moov: clip.moov}
	if rd.Chunks, err = clip.otherChunks(); err != nil {
		return nil, err
	}
	if sc, err := os.ReadFile(strings.TrimSuffix(o.Src, filepath.Ext(o.Src)) + ".sidecar"); err == nil {
		rd.Sidecar = sc
	}
	sumCh := make(chan error, 1)
	var sum string
	go func() {
		var err error
		sum, err = sha256File(o.Src)
		sumCh <- err
	}()
	rd.Meta = Meta{Format: FormatVersion, Tool: "braw-pack " + Version, SourceName: filepath.Base(o.Src),
		SourceSize: clip.Size, Frames: n, Width: w, Height: h, ChromaScale: cs,
		FpsNum: num, FpsDen: den, Encoder: o.Encoder, Quality: o.Quality, Speed: o.Speed,
		Bits: bits, SourceVideoBytes: clip.VideoBytes(), Intra: o.Intra, QualityAuto: auto, SourceStep: round(step, 3)}
	if fi := clip.firstFrame(); fi != nil && fi.version == 5 {
		rd.Meta.V5Steps = v5StepsVersion
	}

	tmpVideo := o.Dst + ".video.tmp.mkv"
	tmpData := o.Dst + ".restore.tmp"
	tmpOut := o.Dst + ".tmp.mkv"
	defer os.Remove(tmpVideo)
	defer os.Remove(tmpData)
	defer os.Remove(tmpOut)

	// Pass 2: feed the planes to the encoder while collecting headers and side bytes.
	seen, gpuErr, err := encodeOrFallBack(clip, nil, &o, rd, tmpVideo)
	if err != nil {
		return nil, err
	}
	if s := chooseChromaScale(seen); s != cs {
		// A frame outside the measured spread has stronger colour than any in it. Encode again
		// at the scale the whole clip needs.
		cs, rd.Meta.ChromaScale = s, s
		rd.Hdrs, rd.Side = nil, nil
		if rd.Meta.V5Steps != 0 {
			// The scale also scales a version 5 clip's brightness, and so its step.
			step = clipStep(clip, cs)
			rd.Meta.SourceStep = round(step, 3)
			if auto {
				o.step = step
				o.Quality = o.autoQuality(step)
				rd.Meta.Quality = o.Quality
			}
		}
		_, e, err := encodeOrFallBack(clip, nil, &o, rd, tmpVideo)
		if err != nil {
			return nil, err
		}
		if e != "" {
			gpuErr = e
		}
	}
	bits = rd.Meta.Bits
	// Recorded only in two-layer archives, so that other archives stay as they were.
	rd.Meta.LayersAuto = autoLayered && rd.Meta.Layers == 2

	// Pass 3: decode what was just encoded, compare it with the source, and measure which
	// quantisation a rebuild should use. The result is recorded in the archive.
	res = &PackResult{SourceSize: clip.Size, Frames: n, ChromaScale: cs, FpsNum: num, FpsDen: den, GPUFallback: gpuErr,
		LayersAuto: autoLayered}
	{
		v, err := checkPicture(o.Ctx, clip, nil, tmpVideo, rd, o.FFmpeg, o.Progress, true, o.Verify)
		if err == ErrCanceled {
			return nil, err
		}
		if err != nil {
			return nil, fmt.Errorf("verification failed, archive discarded: %w", err)
		}
		res.Verify = v
		rd.Meta.ArchivePSNR = round(v.MeanPSNR, 2)
		rd.Meta.DetailKept = v.DetailKept
		rd.Meta.RebuildQuant, rd.Meta.Rebuild = v.RebuildQuant, v.Rebuild
	}
	if err := <-sumCh; err != nil {
		return nil, err
	}
	rd.Meta.SourceSHA256 = sum
	res.Meta = rd.Meta
	if err := rd.write(tmpData); err != nil {
		return nil, err
	}

	attachments, cleanupAttachments, err := restoreAttachments(tmpData)
	if err != nil {
		return nil, err
	}
	defer cleanupAttachments()

	// Mux: picture, the clip's own sound (so the archive plays), and the restore data.
	o.Progress("finish", 0, 1)
	mux := []string{"-y", "-i", tmpVideo, "-i", o.Src, "-map", "0:v", "-map", "1:a?", "-c", "copy",
		"-color_range", "pc", "-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709",
		"-metadata", "braw_pack_format=" + strconv.Itoa(rd.Meta.Format),
		"-metadata", "title=" + filepath.Base(o.Src) + " (braw-pack archive)",
		"-metadata", "comment=" + archiveNote}
	for k, title := range rd.Meta.trackTitles() {
		// Players show one track at a time: say which part of the picture each one is.
		mux = append(mux, "-metadata:s:v:"+strconv.Itoa(k), "title="+title)
	}
	mux = append(mux, attachments...)
	mux = append(mux, "-f", "matroska", tmpOut)
	if err := runFFmpeg(o.FFmpeg, mux...); err != nil {
		return nil, err
	}
	// The finished file must carry exactly the checked picture and the restore data just written.
	back, err := dumpRestoreData(o.FFmpeg, tmpOut)
	if err != nil {
		return nil, err
	}
	if !back.equal(rd) {
		return nil, fmt.Errorf("restore data read back from the archive differs from what was written")
	}
	h1, err := streamHash(o.FFmpeg, tmpVideo, rd.Meta.trackCount())
	if err != nil {
		return nil, err
	}
	h2, err := streamHash(o.FFmpeg, tmpOut, rd.Meta.trackCount())
	if err != nil {
		return nil, err
	}
	if h1 != h2 {
		return nil, fmt.Errorf("picture in the finished archive differs from the checked one")
	}
	o.Progress("finish", 1, 1)
	if err := os.Rename(tmpOut, o.Dst); err != nil {
		return nil, err
	}
	if st, err := os.Stat(o.Dst); err == nil {
		res.ArchiveSize = st.Size()
	}
	if o.LUT {
		l, err := FitLUT(o.Ctx, clip, cs, bits, LUTPath(o.Dst), o.Progress)
		if err == ErrCanceled {
			return nil, err
		}
		if err != nil {
			res.LUTError = err.Error()
		}
		res.LUT = l
	}
	return res, nil
}

// LUTPath is where the viewing LUT of an archive goes: beside it, under the same name.
func LUTPath(archive string) string {
	if strings.HasSuffix(strings.ToLower(archive), ArchiveSuffix) {
		return archive[:len(archive)-len(ArchiveSuffix)] + LUTSuffix
	}
	return archive + ".cube"
}

// encodeFrames feeds the planes of the listed source frames (nil = every frame) to the encoder,
// writing the picture to out and appending each frame's header and side bytes to rd. Samples are
// handed over at the encoder's own depth so ffmpeg never converts them. maxDev is the largest
// colour deviation met (see planeStats.maxChroma), which the colour scale has to accommodate.
func encodeFrames(clip *Clip, frames []int, o *PackOptions, rd *restoreData, out string) (maxDev float64, err error) {
	m := &rd.Meta
	if o.Encoder == "nvenc" || m.Width > hevcMaxSide || m.Height > hevcMaxSide {
		m.setTiles(gpuTiles(m.Width, m.Height))
	} else {
		m.setTiles(nil)
	}
	tracks := gpuMaxTracks
	if o.Encoder == "x265" {
		// CPU tiles are encoded one at a time to bound x265's memory on very large footage.
		tracks = 1
	}
	groups := gpuTileGroups(m.Tiles, tracks)
	if len(groups) <= 1 {
		return encodeFrameGroup(clip, frames, o, rd, out, m.Tiles, true)
	}
	n := clip.Frames()
	if frames != nil {
		n = len(frames)
	}
	var paths []string
	for i, tiles := range groups {
		path := out + ".group" + strconv.Itoa(i) + ".mkv"
		defer os.Remove(path)
		pass := *o
		pass.Progress = func(s string, done, _ int) { o.Progress(s, (i*n+done)/len(groups), n) }
		dev, err := encodeFrameGroup(clip, frames, &pass, rd, path, tiles, i == 0)
		if err != nil {
			return 0, err
		}
		maxDev = math.Max(maxDev, dev)
		paths = append(paths, path)
	}
	args := []string{"-y"}
	for _, path := range paths {
		args = append(args, "-i", path)
	}
	for i := range paths {
		args = append(args, "-map", strconv.Itoa(i)+":v")
	}
	if err := runFFmpeg(o.FFmpeg, append(args, "-c", "copy", "-f", "matroska", out)...); err != nil {
		return 0, err
	}
	return maxDev, nil
}

// encodeFrameGroup encodes only the listed tiles, preserving the whole source's geometry.
// Restore headers and side data are collected once, on the first pass over the source.
func encodeFrameGroup(clip *Clip, frames []int, o *PackOptions, rd *restoreData, out string, tiles []Tile, collect bool) (maxDev float64, err error) {
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
	lay := m.gpuLayout()
	args := []string{"-hide_banner", "-loglevel", "error", "-y", "-f", "rawvideo", "-pix_fmt", lay.pixFmt(),
		"-s", fmt.Sprintf("%dx%d", m.Width, m.Height), "-framerate", fmt.Sprintf("%d/%d", m.FpsNum, m.FpsDen), "-i", "-"}
	paced := bigPicture(m.Width, m.Height)
	if paced {
		args = append([]string{"-progress", "pipe:1"}, args...)
	}
	for k, t := range tiles {
		args = append(args, "-map", "0:v", "-filter:v:"+strconv.Itoa(k), cropFilter(t))
	}
	args = append(args, encoderArgs(o, m.Width, m.Height)...)
	if len(m.Tiles) > 0 {
		// With B-frames each encoder keeps 16 frames of its tile (2.6 GB per 6168x4032 tile, over
		// 10 GB for a 12K picture); without, 4. As with x265 at this size, the archive is
		// somewhat larger.
		args = append(args, "-bf", "0")
	}
	args = append(args, "-f", "matroska", out)
	enc, err := startEncoder(o.FFmpeg, args, paced)
	if err != nil {
		return 0, err
	}
	type packed struct {
		yuv, hdr, side []byte
		maxDev         float64
	}
	err = orderedFrames(n, frameSlots(m.Width, m.Height), func(j int) (packed, error) {
		fr, err := clip.frame(src(j))
		if err != nil {
			return packed{}, err
		}
		fi, Y, U, V, side, st, err := decodePlanes(fr, m.ChromaScale, planesAll)
		if err != nil {
			return packed{}, err
		}
		if fi.w != m.Width || fi.h != m.Height {
			return packed{}, fmt.Errorf("resolution changes")
		}
		return packed{packYUV(Y, U, V, lay), append([]byte(nil), fr[:fi.offs[0]+fi.hs]...), side, st.maxChroma}, nil
	}, func(j int, p packed) error {
		if err := alive(o.Ctx); err != nil {
			return err
		}
		if err := enc.send(o.Ctx, j, p.yuv); err != nil {
			return err
		}
		if collect {
			rd.Hdrs = append(rd.Hdrs, p.hdr)
			rd.Side = append(rd.Side, p.side)
		}
		maxDev = math.Max(maxDev, p.maxDev)
		o.Progress("encode", j+1, n)
		return nil
	})
	if werr := enc.finish(err == ErrCanceled); enc.writeErr != nil || (err == nil && werr != nil) {
		return 0, werr
	}
	if err != nil {
		return 0, err
	}
	return maxDev, nil
}

// encoderPipe is an ffmpeg encoder that is sent raw frames through its input.
type encoderPipe struct {
	cmd      *exec.Cmd
	stdin    *os.File
	stderr   bytes.Buffer
	prog     progressWriter
	paced    bool
	lastSent int64
	writeErr error // set when ffmpeg stops taking frames
}

// startEncoder runs ffmpeg with args, which read raw frames from its input. A paced encoder's
// args must have it report its progress on its output (-progress pipe:1); its frames are then held
// back while it is busy (see send).
func startEncoder(ffmpeg string, args []string, paced bool) (*encoderPipe, error) {
	e := &encoderPipe{cmd: command(ffmpeg, args...), paced: paced, lastSent: time.Now().UnixNano()}
	e.prog.changed.Store(time.Now().UnixNano())
	e.cmd.Stderr = &e.stderr
	e.cmd.Stdout = &e.prog
	pr, stdin, err := framePipe()
	if err != nil {
		return nil, err
	}
	e.cmd.Stdin, e.stdin = pr, stdin
	err = e.cmd.Start()
	pr.Close()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("cannot start ffmpeg (%s): %w", ffmpeg, err)
	}
	return e, nil
}

// send writes frame j (counted from 0) to the encoder.
//
// Neither ffmpeg nor x265 refuses frames while it is busy: it keeps every frame it is sent. Very
// large frames are therefore held back until the encoder is close behind. The hold is soft, up to
// a point: when the encoder has said nothing for a while (it may need more frames before it
// finishes any), one more frame goes through, but never more than twice the window ahead, however
// slowly it encodes.
func (e *encoderPipe) send(ctx context.Context, j int, frame []byte) error {
	for e.paced {
		ahead := j - int(e.prog.frames.Load())
		quiet := time.Since(time.Unix(0, max(e.prog.changed.Load(), e.lastSent))) >= paceStall
		if ahead <= paceWindow || (quiet && ahead <= 2*paceWindow) {
			break
		}
		if err := alive(ctx); err != nil {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := writeFrame(e.stdin, frame); err != nil {
		e.writeErr = err
		return err
	}
	e.lastSent = time.Now().UnixNano()
	return nil
}

// finish ends the encoder's input, stopping it first when kill is set, and waits for it. It
// returns the encoder's failure, if it failed or stopped taking frames.
func (e *encoderPipe) finish(kill bool) error {
	e.stdin.Close()
	if kill {
		e.cmd.Process.Kill()
	}
	werr := e.cmd.Wait()
	// ffmpeg's error output is complete, and safe to read, only once Wait has returned.
	if e.writeErr != nil {
		return encoderFailure{fmt.Errorf("encoder stopped: %v: %s", e.writeErr, strings.TrimSpace(e.stderr.String()))}
	}
	if werr != nil {
		return encoderFailure{fmt.Errorf("ffmpeg encode failed: %v: %s", werr, strings.TrimSpace(e.stderr.String()))}
	}
	return nil
}

// decoderThreads limits the HEVC decoder's threads for very large pictures: each thread holds a
// frame of its own, and at 12K the default (one per CPU) took 15 GB where 4 threads take under 5,
// as fast.
func decoderThreads(w, h int) []string {
	if bigPicture(w, h) {
		return []string{"-threads", "4"}
	}
	return nil
}

// tileThreads is decoderThreads for one track of an archive. The tracks of a tiled picture are
// decoded four at a time, so each gets fewer threads: one on the GPU, which does the decoding
// (a little less memory: 0.63 against 0.77 GB per 6168x4032 track), two on the CPU.
func tileThreads(m *Meta, hw []string, t Tile) []string {
	switch {
	case len(m.Tiles) == 0:
		return decoderThreads(t.W, t.H)
	case len(hw) > 0:
		return []string{"-threads", "1"}
	}
	return []string{"-threads", "2"}
}

// bigPicture says whether frames of this size are paced into the encoder (see encodeFrames): 8K
// and larger, whose frames take a few hundred MB each.
func bigPicture(w, h int) bool { return planeBytes(w, h) > 64<<20 }

const (
	paceWindow = 3                // frames a paced encoder may be sent beyond those it finished
	paceStall  = 15 * time.Second // after this long without news or a frame sent, send one more
)

// progressWriter takes ffmpeg's -progress report and keeps the number of frames encoded so far.
type progressWriter struct {
	line    []byte
	frames  atomic.Int64
	changed atomic.Int64 // when frames last changed, in Unix nanoseconds
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.line = append(p.line, b...)
	for {
		i := bytes.IndexByte(p.line, '\n')
		if i < 0 {
			break
		}
		if v, ok := strings.CutPrefix(strings.TrimSpace(string(p.line[:i])), "frame="); ok {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n != p.frames.Load() {
				p.frames.Store(n)
				p.changed.Store(time.Now().UnixNano())
			}
		}
		p.line = p.line[i+1:]
	}
	return len(b), nil
}

// encoderFailure is an error of the encoding ffmpeg itself, as opposed to one met reading the
// source, which another encoder would meet again.
type encoderFailure struct{ error }

func (e encoderFailure) Unwrap() error { return e.error }

// encodeOrFallBack is encodePicture, trying the NVIDIA encoder first when that is the one asked
// for. If it fails (on an older card that cannot take a tiled picture's tiles, too many encoder
// sessions at once, a missing or busy card), the frames are encoded again on the CPU with x265
// at 12 bits; o and rd.Meta are switched over (with the level chosen again when it was chosen
// from the clip) and gpuErr says why the GPU was given up.
func encodeOrFallBack(clip *Clip, frames []int, o *PackOptions, rd *restoreData, out string) (maxDev float64, gpuErr string, err error) {
	maxDev, err = encodePicture(clip, frames, o, rd, out)
	var ef encoderFailure
	if o.Encoder != "nvenc" || !errors.As(err, &ef) {
		return maxDev, "", err
	}
	gpuErr = nvencReason(ef.Error())
	o.Encoder, o.Layers = "x265", 1
	rd.Meta.Encoder, rd.Meta.Bits = "x265", 12
	rd.Meta.setLayers(1, 0)
	rd.Hdrs, rd.Side = nil, nil
	if o.step > 0 {
		// The level was chosen for the GPU encoder: choose it again for this one.
		o.Quality = o.autoQuality(o.step)
		rd.Meta.Quality = o.Quality
	}
	if maxDev, err = encodeFrames(clip, frames, o, rd, out); err == ErrCanceled {
		return 0, gpuErr, err
	} else if err != nil {
		return 0, gpuErr, fmt.Errorf("%w (tried after the GPU encoder failed: %s)", err, gpuErr)
	}
	return maxDev, gpuErr, nil
}

// nvencReason picks the NVIDIA encoder's own message out of ffmpeg's error output, which also
// carries the follow-on errors of the stages around it.
func nvencReason(msg string) string {
	for _, line := range strings.Split(msg, "\n") {
		if i := strings.Index(line, "[hevc_nvenc @ "); i >= 0 {
			if j := strings.Index(line[i:], "] "); j >= 0 {
				return strings.TrimSpace(line[i+j+2:])
			}
		}
	}
	msg, _, _ = strings.Cut(msg, "\n")
	return strings.TrimSpace(msg)
}

// streamHash is a hash of the video packets of the first tracks video tracks of a file, without
// decoding them. Each track is hashed on its own, so the result does not depend on how the
// tracks' packets are interleaved.
func streamHash(ffmpeg, path string, tracks int) (string, error) {
	var sums []string
	for k := 0; k < tracks; k++ {
		cmd := command(ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-i", path,
			"-map", "0:v:"+strconv.Itoa(k), "-c", "copy", "-f", "md5", "-")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil || len(out) == 0 {
			return "", fmt.Errorf("cannot hash %s: %v: %s", path, err, strings.TrimSpace(stderr.String()))
		}
		sums = append(sums, strings.TrimSpace(string(out)))
	}
	return strings.Join(sums, " "), nil
}

func (d *restoreData) equal(o *restoreData) bool {
	eq := func(a, b [][]byte) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if !bytes.Equal(a[i], b[i]) {
				return false
			}
		}
		return true
	}
	return bytes.Equal(d.Pre, o.Pre) && bytes.Equal(d.Moov, o.Moov) && bytes.Equal(d.Sidecar, o.Sidecar) &&
		eq(d.Chunks, o.Chunks) && eq(d.Hdrs, o.Hdrs) && eq(d.Side, o.Side) &&
		d.Meta.SourceSHA256 == o.Meta.SourceSHA256 && d.Meta.Frames == o.Meta.Frames
}

func sampleFrames(n, k int) []int {
	if n <= k {
		out := make([]int, n)
		for i := range out {
			out[i] = i
		}
		return out
	}
	out := make([]int, k)
	for i := range out {
		out[i] = i * (n - 1) / (k - 1)
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// dumpRestoreData extracts the restore attachment from an archive into a temp file.
func dumpRestoreData(ffmpeg, mkv string) (*restoreData, error) {
	dir, err := os.MkdirTemp(filepath.Dir(mkv), "braw-pack-restore-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	first := filepath.Join(dir, "first.bin")
	diagnostics := dumpAttachments(ffmpeg, mkv, []string{first}, 0)
	restored, err := joinRestoreParts(ffmpeg, mkv, first, dir)
	if err != nil {
		return nil, fmt.Errorf("%s: cannot read restore attachments: %w", mkv, err)
	}
	d, err := readRestoreData(restored)
	if err != nil {
		return nil, fmt.Errorf("%s: cannot read restore data (%v); is this a braw-pack archive? %s",
			mkv, err, strings.TrimSpace(diagnostics))
	}
	return d, nil
}

// decodeArchive streams the archive's picture, frame by frame, as samples at the archive's own
// depth, in the layout each is told. New 10-bit archives are decoded on the GPU when one is
// usable, and then arrive in its layout; HEVC decoding is exact, so both routes give the same
// samples. Two-layer archives arrive rebuilt from their layers, as 12-bit samples.
func decodeArchive(ffmpeg, mkv string, m *Meta, each func(i int, buf []byte, l pixLayout) error) error {
	if m.Bits == 10 {
		// Fall back to the CPU only if the GPU route delivered nothing at all.
		called := false
		_, err := decodeArchiveWith(ffmpeg, mkv, m, true, func(i int, buf []byte, l pixLayout) error {
			called = true
			return each(i, buf, l)
		})
		if err == nil || called {
			return err
		}
	}
	_, err := decodeArchiveWith(ffmpeg, mkv, m, false, each)
	return err
}

// framePipeBytes is the buffer asked for on the pipes that carry frames to and from ffmpeg.
const framePipeBytes = 16 << 20

// writeFrame writes a whole frame to a pipe a piece at a time: Windows refuses to move a 12K
// frame (some hundreds of MB) through a pipe in one call ("insufficient system resources").
func writeFrame(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b[:min(len(b), framePipeBytes)])
		if err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

// readFrame is io.ReadFull for a whole frame from a pipe, a piece at a time (see writeFrame).
// Like io.ReadFull it returns io.EOF only when nothing at all was read.
func readFrame(r io.Reader, b []byte) error {
	read := 0
	for read < len(b) {
		n, err := io.ReadFull(r, b[read:min(len(b), read+framePipeBytes)])
		read += n
		if err == io.EOF && read > 0 {
			err = io.ErrUnexpectedEOF
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// errStopped ends an archive decode early once a frame comparison has already failed.
var errStopped = fmt.Errorf("stopped")

// frameBufs recycles the buffers decodeArchive hands out, one decoded frame each. A consumer
// that is finished with a buffer may give it back with putFrameBuf.
var frameBufs sync.Pool

func getFrameBuf(size int) []byte {
	if b, _ := frameBufs.Get().(*[]byte); b != nil && cap(*b) >= size {
		return (*b)[:size]
	}
	return make([]byte, size)
}

func putFrameBuf(b []byte) { frameBufs.Put(&b) }

// decodeArchiveWith calls each with every frame in order; each owns the buffer it is given. A
// picture of several tracks is decoded one track per ffmpeg (the bundled ffmpeg has no filter that
// stacks pictures), and its tiles are stitched into the whole frame here, or rebuilt from their
// two layers. gpu has the tracks decoded on the GPU (see trackOnGPU).
//
// Those decoders are fed the archive through their input a few frames at a time (see feeder):
// ffmpeg otherwise queues about 40 decoded frames whenever it decodes faster than its output is
// read, which the GPU does, and at 12K that was over 3 GB per tile.
func decodeArchiveWith(ffmpeg, mkv string, m *Meta, gpu bool, each func(i int, buf []byte, l pixLayout) error) (int, error) {
	w, h, n := m.Width, m.Height, m.Frames
	tiles := m.tracks()
	nt := m.trackCount()
	var tracks []*trackDecoder
	var ends [][]int64
	var fileSize int64
	if nt > 1 {
		// Without the frames' places in the file the decoders are fed freely, which only costs
		// memory. Tiles are coded without B-frames (see encodeFrames), so their decoders return a
		// frame for every frame they are fed; the two layers of a smaller picture may need several
		// frames more, and are fed freely.
		if e, err := videoBlockEnds(mkv); err == nil && len(e) >= nt && len(m.Tiles) > 0 {
			ends = e
		}
		st, err := os.Stat(mkv)
		if err != nil {
			return 0, err
		}
		fileSize = st.Size()
	}
	done := make(chan struct{})
	defer func() {
		close(done)
		for _, t := range tracks {
			t.out.Close()
		}
	}()
	kill := func() {
		for _, t := range tracks {
			t.cmd.Process.Kill()
			t.cmd.Wait()
		}
	}
	for k := 0; k < nt; k++ {
		var e []int64
		if ends != nil {
			e = ends[k]
		}
		t, err := startTrackDecoder(ffmpeg, mkv, m, k, trackOnGPU(m, gpu, k), nt > 1, e, fileSize, done)
		if err != nil {
			kill()
			return 0, err
		}
		tracks = append(tracks, t)
	}
	lay := tracks[0].lay
	if m.layers() == 2 {
		lay = planar12
	}
	// read fills buf with the next frame: io.EOF when every track has ended there.
	read := func(buf []byte) error {
		if nt == 1 {
			return readFrame(tracks[0].out, buf)
		}
		errs := make([]error, nt)
		var wg sync.WaitGroup
		for k, t := range tracks {
			wg.Add(1)
			go func(k int, t *trackDecoder) {
				defer wg.Done()
				tl := tiles[k%len(tiles)]
				if t.buf == nil {
					t.buf = make([]byte, planeBytes(tl.W, tl.H))
				}
				t.feed.want()
				if errs[k] = readFrame(t.out, t.buf); errs[k] == nil {
					t.feed.got()
					if m.layers() == 1 {
						copyTile(buf, t.buf, w, h, tl, lay, true)
					}
				}
			}(k, t)
		}
		wg.Wait()
		ended := 0
		for _, err := range errs {
			if err == io.EOF {
				ended++
			} else if err != nil {
				return err
			}
		}
		switch ended {
		case 0:
		case len(errs):
			return io.EOF
		default:
			return io.ErrUnexpectedEOF // some tracks are shorter than others
		}
		if m.layers() == 2 {
			for k, tl := range tiles {
				b, d := tracks[k], tracks[k+len(tiles)]
				addLayers(buf, w, h, tl, b.buf, b.lay, d.buf, d.lay)
			}
		}
		return nil
	}
	size := int(planeBytes(w, h))
	var ferr error
	i := 0
	for ; ; i++ {
		buf := getFrameBuf(size)
		if err := read(buf); err != nil {
			if err != io.EOF {
				ferr = fmt.Errorf("archive video ends mid-frame at frame %d", i)
			}
			break
		}
		if i >= n {
			ferr = fmt.Errorf("archive has more than the expected %d frames", n)
			break
		}
		if ferr = each(i, buf, lay); ferr != nil {
			break
		}
	}
	if ferr != nil {
		kill()
		return i, ferr
	}
	for _, t := range tracks {
		if err := t.cmd.Wait(); err != nil {
			kill()
			return i, fmt.Errorf("ffmpeg decode failed: %v: %s", err, strings.TrimSpace(t.stderr.String()))
		}
	}
	if i != n {
		return i, fmt.Errorf("archive has %d frames, expected %d", i, n)
	}
	return i, nil
}

// trackDecoder is an ffmpeg decoding one video track of an archive to raw samples.
type trackDecoder struct {
	cmd    *exec.Cmd
	out    *os.File
	stderr bytes.Buffer
	lay    pixLayout
	buf    []byte  // one frame of the track, when the picture has several
	feed   *feeder // nil when ffmpeg reads the file itself
}

// startTrackDecoder starts decoding video track k of the archive mkv, on the GPU when gpu is set.
// fed has ffmpeg read the file through its input, from a feeder that keeps it within a few
// frames of what has been read from it (ends: where the track's frames end in the file, nil to
// feed freely; size: the file's size); the feeder stops when done is closed.
func startTrackDecoder(ffmpeg, mkv string, m *Meta, k int, gpu, fed bool, ends []int64, size int64, done <-chan struct{}) (*trackDecoder, error) {
	t := &trackDecoder{lay: m.planar()}
	var hw []string
	if gpu {
		hw, t.lay = []string{"-hwaccel", "cuda"}, m.gpuLayout()
	}
	tiles := m.tracks()
	args := []string{"-hide_banner", "-loglevel", "error"}
	input := []string{"-nostdin", "-i", mkv}
	if fed {
		// The codec settings are in the file's header: probing for them would wait for
		// seconds of input, which the feed holds back.
		input = []string{"-thread_queue_size", "1", "-probesize", "32", "-analyzeduration", "0",
			"-f", "matroska", "-i", "-"}
	}
	args = append(append(append(args, hw...), tileThreads(m, hw, tiles[k%len(tiles)])...), input...)
	args = append(args, "-map", "0:v:"+strconv.Itoa(k), "-f", "rawvideo", "-pix_fmt", t.lay.pixFmt(), "-")
	t.cmd = command(ffmpeg, args...)
	t.cmd.Stderr = &t.stderr
	var feedIn, feedOut *os.File
	if fed {
		// An ordinary pipe, whose small buffer keeps the feed close to what ffmpeg has read.
		r, pw, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		t.cmd.Stdin, feedOut, feedIn = r, r, pw
	}
	out, pw, err := framePipe()
	if err != nil {
		if fed {
			feedIn.Close()
			feedOut.Close()
		}
		return nil, err
	}
	t.out = out
	t.cmd.Stdout = pw
	err = t.cmd.Start()
	pw.Close()
	if feedOut != nil {
		feedOut.Close() // ffmpeg has its own handle; the feed then fails as soon as ffmpeg exits
	}
	if err != nil {
		out.Close()
		if feedIn != nil {
			feedIn.Close()
		}
		return nil, fmt.Errorf("cannot start ffmpeg (%s): %w", ffmpeg, err)
	}
	if feedIn != nil {
		t.feed = newFeeder(ends, size)
		go t.feed.run(mkv, feedIn, done)
	}
	return t, nil
}

// UnpackOptions controls Unpack.
type UnpackOptions struct {
	Ctx      context.Context // optional; cancelling it stops the work and removes partial output
	Src, Dst string
	FFmpeg   string
	// Replace allows an existing destination to be replaced, at the end, by the finished clip.
	Replace bool
	// Quant scales the source's quantisation tables for the rebuilt clip. 0 uses the value
	// measured when the archive was made; 1 uses the source's own tables.
	Quant    float64
	Progress Progress
}

// UnpackResult summarises a rebuilt clip.
type UnpackResult struct {
	Meta  Meta
	Quant float64 // quantisation scale used
	Size  int64   // size of the rebuilt clip
}

// Unpack rebuilds a .braw from an archive alone.
func Unpack(o UnpackOptions) (res *UnpackResult, err error) {
	defer rescue(&err)
	if o.FFmpeg == "" {
		o.FFmpeg = FindFFmpeg()
	}
	if o.Progress == nil {
		o.Progress = func(string, int, int) {}
	}
	if err := claim(o.Src, o.Dst, o.Replace); err != nil {
		return nil, err
	}
	rd, err := dumpRestoreData(o.FFmpeg, o.Src)
	if err != nil {
		return nil, err
	}
	m := rd.Meta
	quant := o.Quant
	if quant == 0 {
		quant = m.RebuildQuant
	}
	if quant == 0 {
		quant = 1
	}
	if quant < 0.1 || quant > 64 {
		return nil, fmt.Errorf("quantisation scale %g is out of range", quant)
	}
	if m.V5Steps != 0 {
		if !v5StepsKnown(m.V5Steps) {
			return nil, fmt.Errorf("this archive was made by a newer braw-pack (version 5 steps %d)", m.V5Steps)
		}
		if quant != 1 && m.V5Steps < 2 {
			// Steps 1 ignore qscale0, so these planes cannot be coded at another one.
			return nil, fmt.Errorf("this archive of a Blackmagic RAW version 5 clip was made by an older braw-pack: it can only be rebuilt at the clip's own quantisation (-quant source)")
		}
	}
	// mdat ended where moov began in the source; chunk extents were captured at pack time.
	bw, err := newBrawWriter(o.Dst, rd.Pre, rd.Moov, m.SourceSize-int64(len(rd.Moov)), rd.Chunks)
	if err != nil {
		return nil, err
	}
	defer bw.abort()
	if bw.frames() != m.Frames {
		return nil, fmt.Errorf("index lists %d frames, restore data %d", bw.frames(), m.Frames)
	}

	type job struct {
		buf []byte
		out chan []byte
		err error
	}
	slots := frameSlots(m.Width, m.Height)
	if bigPicture(m.Width, m.Height) {
		// Frames this large (version 5, 12K) are rebuilt with every CPU already (see forTiles), and hold
		// about 1.6 GB each while they are: queue fewer.
		slots = 2
	}
	jobs := make(chan *job, slots)
	writeErr := make(chan error, 1)
	go func() {
		i := 0
		var werr error
		for j := range jobs {
			fr := <-j.out
			if werr == nil && j.err != nil {
				werr = fmt.Errorf("frame %d: %w", i, j.err)
			}
			if werr == nil {
				werr = bw.frame(i, fr)
				o.Progress("rebuild", i+1, m.Frames)
			}
			i++
		}
		writeErr <- werr
	}()
	derr := decodeArchive(o.FFmpeg, o.Src, &m, func(i int, buf []byte, lay pixLayout) error {
		if err := alive(o.Ctx); err != nil {
			return err
		}
		j := &job{buf: buf, out: make(chan []byte, 1)}
		jobs <- j
		go func() {
			var fr []byte
			defer func() { j.out <- fr }()
			defer rescue(&j.err)
			Y, U, V := unpackYUV(j.buf, m.Width, m.Height, lay)
			putFrameBuf(j.buf)
			fr, j.err = encodePlanesSteps(scaleQuant(rd.Hdrs[i], quant), rd.Side[i], Y, U, V, m.ChromaScale, 0, m.V5Steps)
		}()
		return nil
	})
	close(jobs)
	if werr := <-writeErr; werr != nil {
		return nil, werr
	}
	if derr != nil {
		return nil, derr
	}
	size, err := bw.finish()
	if err != nil {
		return nil, err
	}
	if len(rd.Sidecar) > 0 {
		sc := strings.TrimSuffix(o.Dst, filepath.Ext(o.Dst)) + ".sidecar"
		if _, err := os.Stat(sc); err != nil {
			if err := os.WriteFile(sc, rd.Sidecar, 0o644); err != nil {
				return nil, err
			}
		}
	}
	return &UnpackResult{Meta: m, Quant: quant, Size: size}, nil
}

// VerifyResult is what the archive check measured.
type VerifyResult struct {
	Frames            int
	Compared          int     // frames whose picture was compared with the source
	MeanPSNR, MinPSNR float64 // of the brightness plane, archive vs source, in dB
	MinFrame          int
	DetailKept        float64         // percent of the source's fine detail kept (when measured)
	RebuildQuant      float64         // recommended quantisation scale for unpack (when measured)
	Rebuild           []RebuildOption // the settings that were measured
}

// minPlanePSNR is the floor below which an archive is rejected.
const minPlanePSNR = 36.0

// rebuildTolerance is how much closeness to the source (dB) a coarser rebuild may give up,
// compared with rebuilding at the source's own tables, and still be chosen automatically.
const rebuildTolerance = 0.25

// rebuildLadder lists the quantisation scales tried, finest first.
var rebuildLadder = []float64{1, 1.25, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10, 12, 16}

// Verify checks an archive against the original clip: restore data must match the source byte
// for byte, the frame count must be exact, and every frame's picture must be close to the source.
func Verify(src, mkv, ffmpeg string, progress Progress) (v *VerifyResult, err error) {
	defer rescue(&err)
	if ffmpeg == "" {
		ffmpeg = FindFFmpeg()
	}
	if progress == nil {
		progress = func(string, int, int) {}
	}
	clip, err := OpenClip(src)
	if err != nil {
		return nil, err
	}
	defer clip.Close()
	rd, err := dumpRestoreData(ffmpeg, mkv)
	if err != nil {
		return nil, err
	}
	m := rd.Meta
	if m.Frames != clip.Frames() {
		return nil, fmt.Errorf("frame count differs: archive %d, source %d", m.Frames, clip.Frames())
	}
	if m.SourceSize != clip.Size {
		return nil, fmt.Errorf("source size differs from the one recorded in the archive")
	}
	if !bytes.Equal(rd.Pre, clip.pre) || !bytes.Equal(rd.Moov, clip.moov) {
		return nil, fmt.Errorf("container data in the archive differs from the source")
	}
	chunks, err := clip.otherChunks()
	if err != nil {
		return nil, err
	}
	if len(chunks) != len(rd.Chunks) {
		return nil, fmt.Errorf("audio/metadata chunk count differs")
	}
	for i := range chunks {
		if !bytes.Equal(chunks[i], rd.Chunks[i]) {
			return nil, fmt.Errorf("audio/metadata chunk %d differs from the source", i)
		}
	}
	return checkPicture(nil, clip, nil, mkv, rd, ffmpeg, progress, true, true)
}

// sample is one frame kept for the rebuild measurement: source planes and archive planes.
type sample struct {
	i                      int
	sy, su, sv, ay, au, av []uint16
	srcBytes               int
}

// checkPicture decodes the picture in video and compares every frame with the source clip. Each
// frame's stored header and side bytes must equal the source's. With measure set it also works
// out, on sampled frames, which quantisation a rebuild should use. frames lists the source frames
// the video holds, in order (nil = every frame); rd.Hdrs and rd.Side follow the same order.
func checkPicture(ctx context.Context, clip *Clip, frames []int, video string, rd *restoreData, ffmpeg string, progress Progress, measure, full bool) (*VerifyResult, error) {
	m := rd.Meta
	n := clip.Frames()
	if frames != nil {
		n = len(frames)
	}
	srcIndex := func(j int) int {
		if frames != nil {
			return frames[j]
		}
		return j
	}
	m.Frames = n
	if len(rd.Hdrs) != n || len(rd.Side) != n {
		return nil, fmt.Errorf("restore data holds %d frames, source %d", len(rd.Hdrs), n)
	}
	if !full {
		return spotCheck(ctx, clip, srcIndex, n, video, rd, ffmpeg, progress)
	}
	// keep marks the frames whose planes are held for the rebuild measurement.
	keep := map[int]bool{}
	if measure {
		for _, i := range sampleFrames(n, heldSamples(m.Width, m.Height)) {
			keep[i] = true
		}
	}
	res := &VerifyResult{Frames: n, Compared: n, MinPSNR: math.Inf(1)}
	var sum float64
	type src struct {
		y, u, v []uint16
		ok      bool // header and side bytes match
		size    int
		err     error
	}
	// The source is decoded in parallel a little ahead of the archive decoder. Only the
	// brightness plane is needed, except on the frames kept for the rebuild measurement.
	slots := make([]chan src, n)
	for i := range slots {
		slots[i] = make(chan src, 1)
	}
	sem := make(chan struct{}, frameSlots(m.Width, m.Height))
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for i := 0; i < n; i++ {
			select {
			case sem <- struct{}{}:
			case <-stop:
				return
			}
			go func(i int) {
				var s src
				defer func() { slots[i] <- s }()
				defer rescue(&s.err)
				fr, err := clip.frame(srcIndex(i))
				if err == nil {
					what := planesLuma
					if keep[i] {
						what = planesAll
					}
					var fi *frameInfo
					var side []byte
					fi, s.y, s.u, s.v, side, _, err = decodePlanesSteps(fr, m.ChromaScale, what, m.V5Steps)
					if err == nil {
						s.ok = bytes.Equal(fr[:fi.offs[0]+fi.hs], rd.Hdrs[i]) && bytes.Equal(side, rd.Side[i])
						s.size = len(fr)
					}
				}
				s.err = err
			}(i)
		}
	}()
	// Frames are compared in parallel as the archive decoder delivers them; the results are
	// collected in frame order, so the first bad frame is the one reported.
	type cmp struct {
		done chan struct{}
		p    float64
		smp  *sample
		err  error
	}
	queue := make(chan *cmp, min(runtime.NumCPU(), frameSlots(m.Width, m.Height)))
	var failed atomic.Bool
	var samples []*sample
	cmpErr := make(chan error, 1)
	go func() {
		var first error
		i := 0
		for c := range queue {
			<-c.done
			if first == nil && c.err != nil {
				first = c.err
				failed.Store(true)
			}
			if first == nil {
				if c.smp != nil {
					samples = append(samples, c.smp)
				}
				sum += c.p
				if c.p < res.MinPSNR {
					res.MinPSNR, res.MinFrame = c.p, i
				}
				progress("verify", i+1, n)
			}
			i++
		}
		cmpErr <- first
	}()
	err := decodeArchive(ffmpeg, video, &m, func(i int, buf []byte, lay pixLayout) error {
		if err := alive(ctx); err != nil {
			return err
		}
		if failed.Load() {
			return errStopped
		}
		c := &cmp{done: make(chan struct{})}
		queue <- c
		go func() {
			defer close(c.done)
			defer rescue(&c.err)
			s := <-slots[i]
			<-sem
			if s.err != nil {
				c.err = fmt.Errorf("frame %d: %w", i, s.err)
				return
			}
			if !s.ok {
				c.err = fmt.Errorf("frame %d: stored header or side data differs from the source", i)
				return
			}
			if keep[i] {
				ay, au, av := unpackYUV(buf, m.Width, m.Height, lay)
				c.p = psnr12(ay, s.y)
				c.smp = &sample{i, s.y, s.u, s.v, ay, au, av, s.size}
			} else {
				c.p = lumaPSNR(s.y, buf, lay)
			}
			putFrameBuf(buf)
		}()
		return nil
	})
	close(queue)
	if cerr := <-cmpErr; cerr != nil {
		return nil, cerr
	}
	if err != nil {
		return nil, err
	}
	res.MeanPSNR = sum / float64(n)
	if res.MinPSNR < minPlanePSNR {
		return res, fmt.Errorf("frame %d is only %.1f dB from the source (floor %.0f dB)", res.MinFrame, res.MinPSNR, minPlanePSNR)
	}
	if measure {
		err = measureSamples(res, samples, rd)
	}
	return res, err
}

// measureSamples fills in the detail kept and the rebuild settings from the sample frames.
func measureSamples(res *VerifyResult, samples []*sample, rd *restoreData) (err error) {
	m := rd.Meta
	{
		var lost, detail float64
		for _, s := range samples {
			l, d := detailEnergy(s.sy, s.ay, m.Width, m.Height)
			lost, detail = lost+l, detail+d
		}
		if len(samples) > 0 {
			res.DetailKept = detailKept(lost, detail)
		}
		res.RebuildQuant, res.Rebuild, err = measureRebuild(samples, rd)
	}
	return err
}

// archiveFrame decodes the single frame i of an archive's picture by seeking to it, and says
// which layout the samples are in. ok is false when the archive has no such frame. The tracks of
// a picture of several are decoded side by side and stitched, or rebuilt from their layers.
func archiveFrame(ffmpeg, mkv string, m *Meta, i int) (buf []byte, lay pixLayout, ok bool, err error) {
	size := int(planeBytes(m.Width, m.Height))
	tiles := m.tracks()
	nt := m.trackCount()
	run := func(gpu bool, k int) ([]byte, pixLayout, error) {
		seekDecoders <- struct{}{}
		defer func() { <-seekDecoders }()
		lay, hw := m.planar(), []string(nil)
		if gpu {
			lay, hw = m.gpuLayout(), []string{"-hwaccel", "cuda"}
		}
		args := append(append([]string{"-hide_banner", "-loglevel", "error", "-nostdin"}, hw...), tileThreads(m, hw, tiles[k%len(tiles)])...)
		if i > 0 {
			// Half a frame early, so the first frame at or after that time is frame i whatever
			// rounding the container applied to its time.
			t := (float64(i) - 0.5) * float64(m.FpsDen) / float64(m.FpsNum)
			args = append(args, "-ss", strconv.FormatFloat(t, 'f', 6, 64))
		}
		args = append(args, "-i", mkv, "-map", "0:v:"+strconv.Itoa(k), "-frames:v", "1", "-f", "rawvideo",
			"-pix_fmt", lay.pixFmt(), "-")
		cmd := command(ffmpeg, args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, lay, fmt.Errorf("ffmpeg decode failed: %v: %s", err, strings.TrimSpace(stderr.String()))
		}
		return out, lay, nil
	}
	// all decodes every track; a picture that is a single track comes back as it is.
	all := func(gpu bool) ([]byte, pixLayout, error) {
		if nt == 1 {
			return run(gpu, 0)
		}
		parts := make([][]byte, nt)
		lays := make([]pixLayout, nt)
		if err := each(nt, func(k int) (err error) {
			parts[k], lays[k], err = run(trackOnGPU(m, gpu, k), k)
			return err
		}); err != nil {
			return nil, 0, err
		}
		ended := 0
		for k, p := range parts {
			tl := tiles[k%len(tiles)]
			switch len(p) {
			case 0:
				ended++
			case int(planeBytes(tl.W, tl.H)):
			default:
				return nil, 0, fmt.Errorf("frame %d of archive track %d has %d bytes, expected %d", i, k, len(p), planeBytes(tl.W, tl.H))
			}
		}
		if ended == len(parts) {
			return nil, 0, nil
		}
		if ended > 0 {
			return nil, 0, fmt.Errorf("frame %d is missing from some of the archive's tracks", i)
		}
		pic := make([]byte, size)
		if m.layers() == 2 {
			for k, tl := range tiles {
				d := k + len(tiles)
				addLayers(pic, m.Width, m.Height, tl, parts[k], lays[k], parts[d], lays[d])
			}
			return pic, planar12, nil
		}
		for k, p := range parts {
			copyTile(pic, p, m.Width, m.Height, tiles[k], lays[k], true)
		}
		return pic, lays[0], nil
	}
	var out []byte
	if m.Bits == 10 {
		out, lay, err = all(true)
	}
	if m.Bits != 10 || err != nil {
		if out, lay, err = all(false); err != nil {
			return nil, lay, false, err
		}
	}
	switch len(out) {
	case 0:
		return nil, lay, false, nil
	case size:
		return out, lay, true, nil
	}
	return nil, lay, false, fmt.Errorf("frame %d of the archive has %d bytes, expected %d", i, len(out), size)
}

// seekDecoders holds a place for each ffmpeg archiveFrame runs, so that sample frames decoded
// side by side stay within the same session limit as a streaming decode.
var seekDecoders = make(chan struct{}, gpuMaxTracks)

// heldSamples is how many sample frames' planes the rebuild measurement holds at once: 8, fewer
// when that would take more than about 1.5 GB (each holds the source's and the archive's planes,
// about 600 MB for a 12K picture).
func heldSamples(w, h int) int {
	per := 6 * int64(w) * int64(h)
	if per <= 0 {
		return 8
	}
	return max(2, min(8, int((1536<<20)/per)))
}

// spotCheck is the quick form of checkPicture: it compares a few sample frames with the source
// (enough to measure the rebuild settings), and checks that the archive ends on the right frame.
// The other frames are not decoded.
func spotCheck(ctx context.Context, clip *Clip, srcIndex func(int) int, n int, video string, rd *restoreData, ffmpeg string, progress Progress) (*VerifyResult, error) {
	m := rd.Meta
	m.Frames = n
	idx := sampleFrames(n, heldSamples(m.Width, m.Height))
	res := &VerifyResult{Frames: n, Compared: len(idx), MinPSNR: math.Inf(1)}
	samples := make([]*sample, len(idx))
	psnr := make([]float64, len(idx))
	var done int32
	err := each(len(idx), func(j int) (err error) {
		defer rescue(&err)
		i := idx[j]
		if err := alive(ctx); err != nil {
			return err
		}
		fr, err := clip.frame(srcIndex(i))
		if err != nil {
			return err
		}
		fi, sy, su, sv, side, _, err := decodePlanesSteps(fr, m.ChromaScale, planesAll, m.V5Steps)
		if err != nil {
			return fmt.Errorf("frame %d: %w", i, err)
		}
		if !bytes.Equal(fr[:fi.offs[0]+fi.hs], rd.Hdrs[i]) || !bytes.Equal(side, rd.Side[i]) {
			return fmt.Errorf("frame %d: stored header or side data differs from the source", i)
		}
		buf, lay, ok, err := archiveFrame(ffmpeg, video, &m, i)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("archive has no frame %d", i)
		}
		ay, au, av := unpackYUV(buf, m.Width, m.Height, lay)
		psnr[j] = psnr12(ay, sy)
		samples[j] = &sample{i, sy, su, sv, ay, au, av, len(fr)}
		progress("verify", int(atomic.AddInt32(&done, 1)), len(idx)+1)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, _, extra, err := archiveFrame(ffmpeg, video, &m, n); err != nil {
		return nil, err
	} else if extra {
		return nil, fmt.Errorf("archive has more than the expected %d frames", n)
	}
	var sum float64
	for j, p := range psnr {
		sum += p
		if p < res.MinPSNR {
			res.MinPSNR, res.MinFrame = p, idx[j]
		}
	}
	res.MeanPSNR = sum / float64(len(idx))
	if res.MinPSNR < minPlanePSNR {
		return res, fmt.Errorf("frame %d is only %.1f dB from the source (floor %.0f dB)", res.MinFrame, res.MinPSNR, minPlanePSNR)
	}
	if err := measureSamples(res, samples, rd); err != nil {
		return nil, err
	}
	progress("verify", len(idx)+1, len(idx)+1)
	return res, nil
}

// lumaPSNR compares a source brightness plane with the start of a packed sample buffer.
func lumaPSNR(y []uint16, buf []byte, l pixLayout) float64 {
	shift, top := uint(0), uint16(4095)
	if l == planar10 {
		shift, top = 2, 1023
	}
	var se int64
	buf = buf[:2*len(y)]
	if l == semi10 {
		for k, v := range y {
			a := (uint16(buf[2*k]) | uint16(buf[2*k+1])<<8) >> 6 << 2
			d := int64(v) - int64(a)
			se += d * d
		}
	} else {
		for k, v := range y {
			a := uint16(buf[2*k]) | uint16(buf[2*k+1])<<8
			if a > top {
				a = top
			}
			d := int64(v) - int64(a<<shift)
			se += d * d
		}
	}
	if se == 0 {
		return 99
	}
	return 10 * math.Log10(4095*4095*float64(len(y))/float64(se))
}

// measureRebuild rebuilds the sampled frames at increasingly coarse quantisation and returns the
// coarsest scale that stays within rebuildTolerance of a rebuild at the source's own tables,
// together with every setting it measured. A rebuild finer than the archive's own precision only
// spends bytes on describing compression error, so the coarsest harmless scale is the right one.
func measureRebuild(samples []*sample, rd *restoreData) (float64, []RebuildOption, error) {
	if len(samples) == 0 {
		return 0, nil, nil
	}
	cs := rd.Meta.ChromaScale
	var srcBytes float64
	for _, s := range samples {
		srcBytes += float64(s.srcBytes)
	}
	type one struct {
		bytes int
		y, c  float64
		err   error
	}
	var opts []RebuildOption
	best := 1.0
	ladder := rebuildLadder
	if rd.Meta.V5Steps != 0 {
		// Not measured for version 5 clips: their coarser steps (a higher qscale0) scale DC too,
		// and this choice was not tried on them; -quant still asks for one explicitly.
		ladder = ladder[:1]
	}
	for _, k := range ladder {
		out := make([]one, len(samples))
		done := make(chan struct{}, len(samples))
		for j, s := range samples {
			go func(j int, s *sample) {
				defer func() { done <- struct{}{} }()
				defer rescue(&out[j].err)
				fr, err := encodePlanesSteps(scaleQuant(rd.Hdrs[s.i], k), rd.Side[s.i], s.ay, s.au, s.av, cs, 0, rd.Meta.V5Steps)
				if err != nil {
					out[j].err = err
					return
				}
				_, ry, ru, rv, _, _, err := decodePlanesSteps(fr, cs, planesAll, rd.Meta.V5Steps)
				if err != nil {
					out[j].err = fmt.Errorf("rebuilt frame %d does not decode: %w", s.i, err)
					return
				}
				out[j] = one{len(fr), psnr12(ry, s.sy), (psnr12(ru, s.su) + psnr12(rv, s.sv)) / 2, nil}
			}(j, s)
		}
		for range samples {
			<-done
		}
		var opt RebuildOption
		for _, r := range out {
			if r.err != nil {
				return 0, nil, r.err
			}
			opt.Size += float64(r.bytes)
			opt.Y += r.y
			opt.C += r.c
		}
		nf := float64(len(samples))
		opt = RebuildOption{Quant: k, Size: round(opt.Size/srcBytes, 4), Y: round(opt.Y/nf, 2), C: round(opt.C/nf, 2)}
		opts = append(opts, opt)
		if k > 1 && (opt.Y < opts[0].Y-rebuildTolerance || opt.C < opts[0].C-rebuildTolerance) {
			break
		}
		best = k
	}
	return best, opts, nil
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

// ArchiveMeta reads the description stored in an archive.
func ArchiveMeta(ffmpeg, mkv string) (m *Meta, err error) {
	defer rescue(&err)
	if ffmpeg == "" {
		ffmpeg = FindFFmpeg()
	}
	rd, err := dumpRestoreData(ffmpeg, mkv)
	if err != nil {
		return nil, err
	}
	return &rd.Meta, nil
}

// RebuiltSize predicts the size of the clip unpack would produce at the given quantisation scale
// (0 = the archive's recommendation). ok is false when the archive holds no measurement for it.
func (m *Meta) RebuiltSize(quant float64) (size int64, ok bool) {
	if quant == 0 {
		quant = m.RebuildQuant
	}
	for _, o := range m.Rebuild {
		if o.Quant == quant && m.SourceVideoBytes > 0 {
			return m.SourceSize - m.SourceVideoBytes + int64(float64(m.SourceVideoBytes)*o.Size), true
		}
	}
	return 0, false
}

// Tools describes the ffmpeg that will be used.
type Tools struct {
	FFmpeg  string // resolved path, "" when not found
	Version string
	NVENC   bool // hevc_nvenc is present and a test encode works on this machine
	X265    bool // libx265 is present
	// LUT says whether viewing LUTs can be made here; LUTReason says why not.
	LUT       bool
	LUTReason string
}

// CheckTools looks for ffmpeg and the encoders braw-pack can use.
func CheckTools(ffmpeg string) (t Tools) {
	defer func() { t.LUT, t.LUTReason = LUTSupport() }()
	if ffmpeg == "" {
		ffmpeg = FindFFmpeg()
	}
	path, err := exec.LookPath(ffmpeg)
	if err != nil {
		return t
	}
	t.FFmpeg = path
	if out, err := command(path, "-hide_banner", "-version").Output(); err == nil {
		if f := strings.Fields(string(out)); len(f) > 2 {
			t.Version = f[2]
		}
	}
	if out, err := command(path, "-hide_banner", "-encoders").Output(); err == nil {
		t.X265 = strings.Contains(string(out), "libx265")
		if strings.Contains(string(out), "hevc_nvenc") {
			// Listed is not the same as usable: it needs an NVIDIA card and driver.
			// Two grey 256x256 frames fed as raw samples, so the check needs no test-pattern source.
			cmd := command(path, "-hide_banner", "-loglevel", "error", "-f", "rawvideo", "-pix_fmt", "yuv420p10le",
				"-s", "256x256", "-framerate", "10", "-i", "-", "-c:v", "hevc_nvenc", "-pix_fmt", "p010le", "-f", "null", "-")
			frame := make([]byte, 256*256*3)
			for i := 1; i < len(frame); i += 2 {
				frame[i] = 2 // 0x0200 = mid grey at 10 bits, little-endian
			}
			cmd.Stdin = bytes.NewReader(append(frame, frame...))
			t.NVENC = cmd.Run() == nil
		}
	}
	return t
}

// FindFFmpeg returns the ffmpeg to use: the one shipped beside this program if there is one
// (the portable layout: unzip and run), otherwise "ffmpeg" to be found on the PATH.
func FindFFmpeg() string {
	name := "ffmpeg"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		for _, dir := range []string{filepath.Dir(exe), filepath.Join(filepath.Dir(exe), "ffmpeg")} {
			p := filepath.Join(dir, name)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p
			}
		}
	}
	return "ffmpeg"
}
