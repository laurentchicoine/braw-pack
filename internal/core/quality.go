package core

// Choosing the quality level from the clip.
//
// The archive's encoder rounds the planes at a fixed step for a given quality level, but cameras
// quantise their pictures at very different steps: a Pocket Cinema Camera 4K at constant quality
// codes one level of a low-frequency coefficient as about 4 plane values, an URSA Cine 12K at 12:1
// as under one. When the encoder's step is larger than the camera's, the faint detail coded as one
// or two levels is rounded away, so a level that keeps nearly everything of one clip keeps much
// less of another. Pack therefore measures the camera's step (sourceStep) and, with x265, lowers
// the level for finely coded clips to keep the two steps in proportion (autoQuality).
// docs/quality.md has the measurements.

import (
	"math"
	"sort"
)

// AutoQuality, as PackOptions.Quality, has Pack choose the level from the clip. It is the default.
const AutoQuality = 0

// sourceStep is how finely a frame was quantised: the change in braw-pack's 12-bit brightness
// plane that one level of a low-frequency coefficient makes (its root mean square over the block),
// averaged geometrically over the coefficients below a quarter of the sampling rate, DC excluded.
// Those are the frequencies "detail kept" measures. cs is the colour scale, which for version 5
// clips scales the brightness plane too.
func sourceStep(fi *frameInfo, cs float64) float64 {
	sum, n := 0.0, 0
	add := func(step float64) {
		sum += math.Log(step)
		n++
	}
	if fi.version == 5 {
		qy, _, ok := v5Steps(v5StepsVersion, fi) // steps 2 follow each frame's own qscale fields
		if !ok {
			return 0
		}
		// Orthonormal 12x12 transform: a coefficient c is c/12 in root mean square.
		for v := 0; v < 6; v++ {
			for u := 0; u < 6; u++ {
				if u+v > 0 {
					add(qy[v*12+u] / 12 * cs)
				}
			}
		}
	} else {
		ql, _ := fi.quantArrays(&fi.table)
		// Orthonormal 8x8 transform, 16.16 fixed-point steps.
		for v := 0; v < 4; v++ {
			for u := 0; u < 4; u++ {
				if u+v > 0 {
					add(float64(ql[v*8+u]) / 65536 / 8)
				}
			}
		}
	}
	return math.Exp(sum / float64(n))
}

// clipStep is sourceStep over a few frames spread through the clip (the median, should the
// camera change its tables part-way); 0 if no frame can be read.
func clipStep(clip *Clip, cs float64) float64 {
	var steps []float64
	for _, i := range sampleFrames(clip.Frames(), 5) {
		fr, err := clip.frame(i)
		if err != nil {
			continue
		}
		if fi, err := parseFrame(fr); err == nil {
			if s := sourceStep(fi, cs); s > 0 {
				steps = append(steps, s)
			}
		}
	}
	if len(steps) == 0 {
		return 0
	}
	sort.Float64s(steps)
	return steps[len(steps)/2]
}

// Automatic quality (docs/quality.md has the measurements behind these values).
const (
	// autoTop is the level of clips quantised as coarsely as a Pocket Cinema Camera 4K's, or more:
	// those keep about as much detail at 10 as at any lower level.
	autoTop = 10
	// autoStepRef is the camera step, in plane values, below which the level is lowered: the
	// finest Pocket Cinema Camera 4K clip measured (2.54) still keeps its best detail at autoTop.
	autoStepRef = 2.5
)

// autoFloor is the lowest level chosen automatically for each encoder. The NVIDIA encoder's
// predicted frames keep an error of about one 10-bit code (4 plane values) whatever the level, so
// for finely coded clips detail stops growing below about 10 (it is x265 that keeps more of
// them); x265 goes down to 4, where the URSA Cine 12K test clip keeps 98% of its detail at 8K scale (docs/quality.md).
func autoFloor(encoder string) int {
	if encoder == "nvenc" {
		return autoTop
	}
	return 4
}

// autoQuality is the level for a clip of camera step s (sourceStep) made with encoder. The
// encoders' step doubles every 6 levels, so below autoStepRef the level drops by 6 for every
// halving of the camera's step, keeping the two in proportion, down to autoFloor. An unknown step
// (0) gives autoTop.
func autoQuality(s float64, encoder string) int {
	if s <= 0 {
		return autoTop
	}
	q := int(math.Round(autoTop + 6*math.Log2(s/autoStepRef)))
	return max(autoFloor(encoder), min(q, autoTop))
}

// FinerThanGPU reports whether a clip of camera step s (Meta.SourceStep) is coded more finely than
// the NVIDIA encoder keeps well (see autoFloor): x265 keeps clearly more of such clips.
func FinerThanGPU(s float64) bool { return s > 0 && s < autoStepRef }
