package core

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// EstimateOptions controls Estimate.
type EstimateOptions struct {
	Ctx       context.Context // optional
	Src       string
	Qualities []int  // quality levels to try; nil = a default spread
	Speed     string // as for Pack
	Encoder   string
	Intra     bool
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
	Rows                 []EstimateRow
}

var defaultQualities = []int{4, 6, 8, 10, 12, 14, 16}

// Estimate predicts archive size, closeness to the source and rebuilt size for several quality
// levels by really encoding short stretches spread through the clip.
func Estimate(o EstimateOptions) (est *EstimateResult, err error) {
	defer rescue(&err)
	po := PackOptions{Ctx: o.Ctx, Speed: o.Speed, Encoder: o.Encoder, Intra: o.Intra, FFmpeg: o.FFmpeg, Quality: DefaultQuality}
	if err := po.defaults(); err != nil {
		return nil, err
	}
	if o.Progress == nil {
		o.Progress = func(string, int, int) {}
	}
	po.Progress = func(string, int, int) {}
	qualities := o.Qualities
	if len(qualities) == 0 {
		qualities = defaultQualities
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
	for _, i := range frames {
		fr, err := clip.frame(i)
		if err != nil {
			return nil, err
		}
		if i%4 != 0 && i != frames[0] {
			continue
		}
		fi, _, _, _, _, st, err := decodePlanes(fr, 1, planesChroma)
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
		FpsNum: num, FpsDen: den, Encoder: po.Encoder, Speed: po.Speed, Bits: bits}

	dir, err := os.MkdirTemp("", "braw-pack-estimate-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	picW, picH := clip.pictureSize(w, h)
	raw := rawFrameBytes(picW, picH) * float64(n)
	video := clip.VideoBytes()
	other := clip.Size - video // sound, timecode, motion data, index
	scale := float64(n) / float64(ns)
	est = &EstimateResult{Frames: n, SampleFrames: ns, PicW: picW, PicH: picH, SourceBytes: clip.Size, SourceRatio: raw / float64(video)}

	// Each quality level is two passes over the sample frames: compress, then check.
	total := 2 * ns * len(qualities)
	o.Progress("estimate", 0, total)
	var restoreBytes float64
	for qi, q := range qualities {
		po.Quality = q
		base := 2 * ns * qi
		po.Progress = func(_ string, done, _ int) { o.Progress("estimate", base+done, total) }
		rd := &restoreData{Meta: meta}
		rd.Meta.Quality = q
		out := filepath.Join(dir, fmt.Sprintf("q%d.mkv", q))
		if _, err := encodeFrames(clip, frames, &po, rd, out); err != nil {
			return nil, err
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
			func(_ string, done, _ int) { o.Progress("estimate", base+ns+done, total) }, true, true)
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
		o.Progress("estimate", base+2*ns, total)
	}
	return est, nil
}
