package core

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Shrinking re-saves a clip as a smaller .braw by coarsening its own quantisation, the way the
// camera would have recorded it at a stronger compression setting. Nothing is transformed: the
// stored coefficients are re-scaled and re-coded. The scale grows with frequency (see
// requantTilt), which removes the finest detail first and gives a smooth range of sizes.
// Version 5 (URSA Cine 12K) frames have one step for every frequency, so there the tilt drops
// the smallest high-frequency levels instead (v5Requant); on the test clip that kept more detail
// at every ratio than raising qscale0, the camera's own control, whose sizes also jump in steps.

// ShrinkRatios are the compression ratios offered, in Blackmagic's terms.
var ShrinkRatios = []float64{5, 8, 10, 12, 15, 18, 20, 22, 25}

// maxTilt bounds the search. Beyond it the file barely gets smaller: what is left is mostly the
// average of each 8x8 block, which this method does not touch.
const maxTilt = 128

// ShrinkMargin is how much stronger than the clip's own ratio a target must be to be worth doing.
const ShrinkMargin = 1.1

// ShrinkOption is the measured outcome of one target ratio.
type ShrinkOption struct {
	Target     float64 // ratio asked for
	Ratio      float64 // ratio reached on the sample frames
	Bytes      int64   // predicted size of the whole clip
	DetailKept float64 // percent of the source's fine detail kept
	tilt       float64
}

// ShrinkPlan lists what a clip can be shrunk to.
type ShrinkPlan struct {
	Frames, SampleFrames int
	PicW, PicH           int
	SourceBytes          int64
	SourceRatio          float64
	MaxRatio             float64 // strongest ratio this method reaches on the clip
	Options              []ShrinkOption
}

type shrinkSample struct {
	fr []byte
	y  []uint16
	w  int
	h  int
}

// shrinkProbe holds sample frames of a clip and measures requantisations of them.
type shrinkProbe struct {
	clip    *Clip
	samples []*shrinkSample
	srcB    float64 // bytes of the sample frames
	raw     float64 // unprocessed bytes of one frame
	picW    int
	picH    int
}

func each(n int, fn func(i int) error) error {
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer rescue(&errs[i])
			errs[i] = fn(i)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func newShrinkProbe(clip *Clip) (*shrinkProbe, error) {
	n := 16
	if fi := clip.firstFrame(); fi != nil && fi.version == 5 {
		n = 6 // a 12K sample holds a 200 MB brightness plane, and each measurement decodes another
	}
	idx := sampleFrames(clip.Frames(), n)
	p := &shrinkProbe{clip: clip, samples: make([]*shrinkSample, len(idx))}
	err := each(len(idx), func(j int) error {
		fr, err := clip.frame(idx[j])
		if err != nil {
			return err
		}
		// Gate: the bitstream must survive a decode and re-encode unchanged.
		out, err := requantFrame(fr, 1, 0)
		if err != nil {
			return fmt.Errorf("unsupported BRAW variant (frame %d): %w", idx[j], err)
		}
		if len(out) > len(fr) || !bytes.Equal(out, fr[:len(out)]) {
			return fmt.Errorf("unsupported BRAW variant: frame %d does not round-trip exactly", idx[j])
		}
		fi, y, _, _, _, _, err := decodePlanes(fr, 1, planesLuma)
		if err != nil {
			return err
		}
		p.samples[j] = &shrinkSample{fr, y, fi.w, fi.h}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, s := range p.samples {
		p.srcB += float64(len(s.fr))
	}
	p.picW, p.picH = clip.pictureSize(p.samples[0].w, p.samples[0].h)
	p.raw = rawFrameBytes(p.picW, p.picH)
	return p, nil
}

// measure requantises the sample frames at a tilt and returns the ratio reached and, when asked,
// the share of fine detail kept.
func (p *shrinkProbe) measure(tilt float64, detail bool) (ratio, kept float64, err error) {
	sizes := make([]float64, len(p.samples))
	lost := make([]float64, len(p.samples))
	det := make([]float64, len(p.samples))
	err = each(len(p.samples), func(j int) error {
		s := p.samples[j]
		out, err := requantTilt(s.fr, 1, tilt, 0)
		if err != nil {
			return err
		}
		sizes[j] = float64(len(out))
		if detail {
			_, y, _, _, _, _, err := decodePlanes(out, 1, planesLuma)
			if err != nil {
				return err
			}
			lost[j], det[j] = detailEnergy(s.y, y, s.w, s.h)
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	var b, l, d float64
	for j := range sizes {
		b, l, d = b+sizes[j], l+lost[j], d+det[j]
	}
	return p.raw * float64(len(sizes)) / b, detailKept(l, d), nil
}

// find returns the weakest tilt that reaches the target ratio on the sample frames.
func (p *shrinkProbe) find(ctx context.Context, target float64) (tilt float64, ok bool, err error) {
	r, _, err := p.measure(maxTilt, false)
	if err != nil || r < target {
		return 0, false, err
	}
	lo, hi := 0.0, float64(maxTilt)
	for i := 0; i < 12; i++ {
		if err := alive(ctx); err != nil {
			return 0, false, err
		}
		mid := (lo + hi) / 2
		r, _, err := p.measure(mid, false)
		if err != nil {
			return 0, false, err
		}
		if r >= target {
			hi = mid
		} else {
			lo = mid
		}
	}
	return hi, true, nil
}

func (p *shrinkProbe) option(ctx context.Context, target float64) (*ShrinkOption, error) {
	tilt, ok, err := p.find(ctx, target)
	if err != nil || !ok {
		return nil, err
	}
	r, kept, err := p.measure(tilt, true)
	if err != nil {
		return nil, err
	}
	video := float64(p.clip.VideoBytes())
	n := float64(p.clip.Frames())
	return &ShrinkOption{Target: target, Ratio: r, DetailKept: kept, tilt: tilt,
		Bytes: p.clip.Size - int64(video) + int64(p.raw*n/r)}, nil
}

func (p *shrinkProbe) sourceRatio() float64 {
	return p.raw * float64(p.clip.Frames()) / float64(p.clip.VideoBytes())
}

// ShrinkEstimate measures, on sample frames, what the clip would become at each target ratio
// (nil = the standard list). Targets the clip already meets or cannot reach are left out.
func ShrinkEstimate(ctx context.Context, src string, targets []float64, progress Progress) (plan *ShrinkPlan, err error) {
	defer rescue(&err)
	if progress == nil {
		progress = func(string, int, int) {}
	}
	if len(targets) == 0 {
		targets = ShrinkRatios
	}
	clip, err := OpenClip(src)
	if err != nil {
		return nil, err
	}
	defer clip.Close()
	if clip.Frames() == 0 {
		return nil, fmt.Errorf("clip has no frames")
	}
	progress("estimate", 0, len(targets)+1)
	p, err := newShrinkProbe(clip)
	if err != nil {
		return nil, err
	}
	plan = &ShrinkPlan{Frames: clip.Frames(), SampleFrames: len(p.samples), PicW: p.picW, PicH: p.picH,
		SourceBytes: clip.Size, SourceRatio: p.sourceRatio()}
	if plan.MaxRatio, _, err = p.measure(maxTilt, false); err != nil {
		return nil, err
	}
	for i, t := range targets {
		progress("estimate", i+1, len(targets)+1)
		if t < plan.SourceRatio*ShrinkMargin {
			continue
		}
		opt, err := p.option(ctx, t)
		if err != nil {
			return nil, err
		}
		if opt != nil {
			plan.Options = append(plan.Options, *opt)
		}
	}
	progress("estimate", len(targets)+1, len(targets)+1)
	return plan, nil
}

// ShrinkOptions controls Shrink.
type ShrinkOptions struct {
	Ctx      context.Context // optional; cancelling it stops the work and removes partial output
	Src, Dst string
	Ratio    float64 // target compression ratio, for example 12 for 12:1
	Replace  bool    // allow an existing destination to be replaced, at the end, by the finished clip
	Progress Progress
}

// ShrinkResult summarises a shrunk clip.
type ShrinkResult struct {
	Frames           int
	SourceSize, Size int64
	SourceRatio      float64
	Ratio            float64 // of the written clip
	DetailKept       float64 // measured on sample frames
}

// Shrink writes a smaller copy of a clip, compressed about as strongly as the target ratio. The
// source is only read. Sound, timecode, motion data and camera settings are copied unchanged.
func Shrink(o ShrinkOptions) (res *ShrinkResult, err error) {
	defer rescue(&err)
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
	o.Progress("analyse", 0, 1)
	p, err := newShrinkProbe(clip)
	if err != nil {
		return nil, err
	}
	srcRatio := p.sourceRatio()
	if o.Ratio < srcRatio*ShrinkMargin {
		return nil, fmt.Errorf("the clip is already at %.1f:1; choose a stronger ratio than %.4g:1", srcRatio, o.Ratio)
	}
	opt, err := p.option(o.Ctx, o.Ratio)
	if err != nil {
		return nil, err
	}
	if opt == nil {
		max, _, _ := p.measure(maxTilt, false)
		return nil, fmt.Errorf("%.4g:1 cannot be reached as Blackmagic RAW on this clip (the limit is about %.0f:1)", o.Ratio, max)
	}
	o.Progress("analyse", 1, 1)

	chunks, err := clip.otherChunks()
	if err != nil {
		return nil, err
	}
	bw, err := newBrawWriter(o.Dst, clip.pre, clip.moov, clip.moovPos, chunks)
	if err != nil {
		return nil, err
	}
	defer bw.abort()
	// The written clip is reopened and checked before it takes its final name.
	bw.check = func(path string) error {
		if err := checkShrunk(path, n, chunks); err != nil {
			return fmt.Errorf("verification failed, clip discarded: %w", err)
		}
		return nil
	}
	var video int64
	err = orderedFrames(n, clip.slots(), func(i int) ([]byte, error) {
		fr, err := clip.frame(i)
		if err != nil {
			return nil, err
		}
		out, err := requantTilt(fr, 1, opt.tilt, 0)
		if err != nil {
			return nil, err
		}
		// The new frame must itself read back and re-code to the same bytes.
		again, err := requantFrame(out, 1, 0)
		if err != nil || !bytes.Equal(again, out) {
			return nil, fmt.Errorf("shrunk frame does not read back")
		}
		return out, nil
	}, func(i int, fr []byte) error {
		if err := alive(o.Ctx); err != nil {
			return err
		}
		video += int64(len(fr))
		o.Progress("encode", i+1, n)
		return bw.frame(i, fr)
	})
	if err != nil {
		return nil, err
	}
	o.Progress("finish", 0, 1)
	size, err := bw.finish()
	if err != nil {
		return nil, err
	}
	if sc, err := os.ReadFile(strings.TrimSuffix(o.Src, filepath.Ext(o.Src)) + ".sidecar"); err == nil {
		dst := strings.TrimSuffix(o.Dst, filepath.Ext(o.Dst)) + ".sidecar"
		if _, err := os.Stat(dst); err != nil {
			if err := os.WriteFile(dst, sc, 0o644); err != nil {
				return nil, err
			}
		}
	}
	o.Progress("finish", 1, 1)
	return &ShrinkResult{Frames: n, SourceSize: clip.Size, Size: size, SourceRatio: srcRatio,
		Ratio: p.raw * float64(n) / float64(video), DetailKept: opt.DetailKept}, nil
}

// checkShrunk reopens a written clip: it must index the same number of frames, and its sound,
// timecode and motion data must be the source's, byte for byte.
func checkShrunk(path string, frames int, chunks [][]byte) error {
	c, err := OpenClip(path)
	if err != nil {
		return err
	}
	defer c.Close()
	if c.Frames() != frames {
		return fmt.Errorf("written clip has %d frames, source %d", c.Frames(), frames)
	}
	got, err := c.otherChunks()
	if err != nil {
		return err
	}
	if len(got) != len(chunks) {
		return fmt.Errorf("written clip has %d non-video chunks, source %d", len(got), len(chunks))
	}
	for i := range got {
		// Chunks run to the next one, so each may carry different alignment padding at its end.
		a, b := bytes.TrimRight(got[i], "\x00"), bytes.TrimRight(chunks[i], "\x00")
		if !bytes.Equal(a, b) {
			return fmt.Errorf("non-video chunk %d differs from the source", i)
		}
	}
	for _, i := range sampleFrames(frames, 6) {
		fr, err := c.frame(i)
		if err != nil {
			return err
		}
		if _, _, _, _, _, _, err := decodePlanes(fr, 1, planesChroma); err != nil {
			return fmt.Errorf("frame %d: %w", i, err)
		}
	}
	return nil
}
