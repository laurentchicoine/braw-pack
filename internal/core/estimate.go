package core

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
)

// EstimateOptions controls Estimate.
type EstimateOptions struct {
	Ctx       context.Context // optional
	Src       string
	Qualities []int  // quality levels to try; nil = a default spread
	Speed     string // as for Pack
	Encoder   string
	Intra     bool
	Layers    int // as for Pack; Qualities are then the detail layer's levels
	FFmpeg    string
	Progress  Progress
}

// EstimateRow is the predicted outcome of packing the clip at one quality level. Ratios are in
// Blackmagic's terms: the unprocessed size of the sensor frames divided by the picture data size,
// so a larger first number means stronger compression (3:1, 5:1, 8:1, 12:1, 18:1 ...).
type EstimateRow struct {
	Quality      int
	ArchiveBytes int64   // whole archive: picture, sound and restore data
	ArchiveRatio float64 // of the archive's picture
	PSNR         float64 // archive vs source, brightness plane, dB
	DetailKept   float64 // percent of the source's fine detail the archive keeps
	RebuildQuant float64 // quantisation scale unpack would use
	RebuiltBytes int64   // size of the clip unpack would produce
	RebuiltRatio float64 // of the rebuilt clip's picture
}

// EstimateResult is what Estimate measured.
type EstimateResult struct {
	Frames, SampleFrames int
	PicW, PicH           int
	SourceBytes          int64
	SourceRatio          float64
	// SourceStep is how finely the camera quantised the clip (see sourceStep), and AutoQuality the
	// level pack would choose from it for the encoder used.
	SourceStep  float64
	AutoQuality int
	// Layers is how many layers the estimate was made for (see PackOptions.Layers), and
	// LayersAuto says whether that was chosen for the clip.
	Layers     int
	LayersAuto bool
	Rows       []EstimateRow
	// GPUFallback is set when the NVIDIA encoder failed on this clip and the estimate was made
	// with x265 instead: it is the GPU's reason.
	GPUFallback string
}

var defaultQualities = []int{4, 6, 8, 10, 12, 14, 16}

// defaultLayerQualities are the detail layer's levels tried for two-layer archives: 16 and 22 keep
// about as much detail as x265 at 4 and 10.
var defaultLayerQualities = []int{13, 16, 19, 22}

// Estimate predicts archive size, closeness to the source and rebuilt size for several quality
// levels by really encoding short stretches spread through the clip.
func Estimate(o EstimateOptions) (est *EstimateResult, err error) {
	defer rescue(&err)
	po := PackOptions{Ctx: o.Ctx, Speed: o.Speed, Encoder: o.Encoder, Intra: o.Intra, Layers: o.Layers, FFmpeg: o.FFmpeg, Quality: AutoQuality}
	if err := po.defaults(); err != nil {
		return nil, err
	}
	if o.Progress == nil {
		o.Progress = func(string, int, int) {}
	}
	po.Progress = func(string, int, int) {}
	clip, err := OpenClip(o.Src)
	if err != nil {
		return nil, err
	}
	defer clip.Close()
	n := clip.Frames()
	if n == 0 {
		return nil, fmt.Errorf("clip has no frames")
	}
	// Four stretches of consecutive frames, so the encoder's use of neighbouring frames is
	// represented, spread across the clip.
	const stretches, length = 4, 24
	var frames []int
	if n <= stretches*length {
		for i := 0; i < n; i++ {
			frames = append(frames, i)
		}
	} else {
		for s := 0; s < stretches; s++ {
			start := s * (n - length) / (stretches - 1)
			for i := 0; i < length; i++ {
				frames = append(frames, start+i)
			}
		}
	}
	ns := len(frames)

	var maxDev float64
	var w, h int
	what, v5, rgbw := planesChroma, 0, false
	if fi := clip.firstFrame(); fi != nil && fi.version == 5 {
		what, v5 = planesAll, v5StepsVersion // the scale covers every plane (see decodePlanesV5)
		rgbw = fi.rgbw()
	}
	for _, i := range frames {
		fr, err := clip.frame(i)
		if err != nil {
			return nil, err
		}
		if i%4 != 0 && i != frames[0] {
			continue
		}
		fi, _, _, _, _, st, err := decodePlanes(fr, 1, what)
		if err != nil {
			return nil, err
		}
		w, h = fi.w, fi.h
		maxDev = math.Max(maxDev, st.maxChroma)
	}
	num, den, _, err := clip.SensorRate()
	if err != nil {
		return nil, err
	}
	bits := 10
	if po.Encoder == "x265" {
		bits = 12
	}
	meta := Meta{Format: FormatVersion, Frames: ns, Width: w, Height: h, ChromaScale: chooseChromaScale(maxDev),
		FpsNum: num, FpsDen: den, Encoder: po.Encoder, Speed: po.Speed, Bits: bits, V5Steps: v5}

	// The level pack would choose for this clip is always among those tried, unless levels were given.
	step := clipStep(clip, meta.ChromaScale)
	autoLayered := o.Layers == AutoLayers
	if autoLayered {
		po.Layers = autoLayers(po.Encoder, rgbw)
	}
	autoQ := po.autoQuality(step)
	qualities := o.Qualities
	if len(qualities) == 0 {
		qualities = defaultQualities
		if po.layered() {
			qualities = defaultLayerQualities
		}
		qualities = withLevel(qualities, autoQ)
	}

	dir, err := os.MkdirTemp("", "braw-pack-estimate-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	// The base of a two-layer picture does not depend on the level: code it once.
	po.keepBase = &layerBase{path: filepath.Join(dir, "base.mkv")}

	picW, picH := clip.pictureSize(w, h)
	raw := rawFrameBytes(picW, picH) * float64(n)
	video := clip.VideoBytes()
	other := clip.Size - video // sound, timecode, motion data, index
	scale := float64(n) / float64(ns)
	est = &EstimateResult{Frames: n, SampleFrames: ns, PicW: picW, PicH: picH, SourceBytes: clip.Size,
		SourceRatio: raw / float64(video), SourceStep: round(step, 3), AutoQuality: autoQ,
		Layers: max(po.Layers, 1), LayersAuto: autoLayered}

	// Each quality level is two passes over the sample frames: compress, then check.
	total := func() int { return 2 * ns * len(qualities) }
	o.Progress("estimate", 0, total())
	var restoreBytes float64
	for qi := 0; qi < len(qualities); qi++ {
		q := qualities[qi]
		po.Quality = q
		base := 2 * ns * qi
		po.Progress = func(_ string, done, _ int) { o.Progress("estimate", base+done, total()) }
		rd := &restoreData{Meta: meta}
		rd.Meta.Quality = q
		out := filepath.Join(dir, fmt.Sprintf("q%d.mkv", q))
		_, gpuErr, err := encodeOrFallBack(clip, frames, &po, rd, out)
		if err != nil {
			return nil, err
		}
		if gpuErr != "" {
			// The remaining levels go straight to the CPU, as packing this clip would, and pack would
			// choose its level for that encoder.
			est.GPUFallback = gpuErr
			meta.Encoder, meta.Bits = rd.Meta.Encoder, rd.Meta.Bits
			est.Layers = 1
			if a := po.autoQuality(step); a != autoQ {
				autoQ, est.AutoQuality = a, a
				if len(o.Qualities) == 0 && !slices.Contains(qualities, a) {
					qualities = append(qualities, a)
				}
			}
		}
		st, err := os.Stat(out)
		if err != nil {
			return nil, err
		}
		if qi == 0 {
			// Per-frame restore data does not depend on the quality level: measure it once.
			tmp := filepath.Join(dir, "restore.bin")
			if err := rd.write(tmp); err != nil {
				return nil, err
			}
			if rs, err := os.Stat(tmp); err == nil {
				restoreBytes = float64(rs.Size()) * scale
			}
		}
		v, err := checkPicture(o.Ctx, clip, frames, out, rd, po.FFmpeg,
			func(_ string, done, _ int) { o.Progress("estimate", base+ns+done, total()) }, true, true)
		if err == ErrCanceled {
			return nil, err
		}
		if err != nil {
			return nil, fmt.Errorf("quality %d: %w", q, err)
		}
		pic := float64(st.Size()) * scale
		row := EstimateRow{Quality: q, PSNR: round(v.MeanPSNR, 2), DetailKept: v.DetailKept, RebuildQuant: v.RebuildQuant,
			// The archive carries the sound twice: as a playable track and inside the restore data.
			ArchiveBytes: int64(pic + restoreBytes + 2*float64(other)),
			ArchiveRatio: raw / pic}
		size := 1.0
		for _, opt := range v.Rebuild {
			if opt.Quant == v.RebuildQuant {
				size = opt.Size
			}
		}
		row.RebuiltBytes = int64(float64(video)*size) + other
		row.RebuiltRatio = raw / (float64(video) * size)
		est.Rows = append(est.Rows, row)
		o.Progress("estimate", base+2*ns, total())
	}
	slices.SortFunc(est.Rows, func(a, b EstimateRow) int { return a.Quality - b.Quality })
	return est, nil
}

// withLevel is the sorted list of levels with q added.
func withLevel(levels []int, q int) []int {
	out := append([]int(nil), levels...)
	if !slices.Contains(out, q) {
		out = append(out, q)
		slices.Sort(out)
	}
	return out
}
