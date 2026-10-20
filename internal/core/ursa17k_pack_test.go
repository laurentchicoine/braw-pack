package core

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func ursa17kArchiveDir() string {
	if dir := os.Getenv("BRAW_V5_17K_OUTPUT"); dir != "" {
		return dir
	}
	return filepath.Join("..", "..", "research", "ursa-17k", "outputs", "sample-archive")
}

func TestUrsa17KRebuildSample(t *testing.T) {
	path := os.Getenv("BRAW_V5_17K_CLIP")
	if path == "" {
		t.Skip("BRAW_V5_17K_CLIP not set")
	}
	c, err := OpenClip(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	po := PackOptions{Progress: func(string, int, int) {}}
	if err := po.defaults(); err != nil {
		t.Fatal(err)
	}
	frames := []int{0, 1, 2, 3, 165, 166, 329, 330}
	w, h, dev, err := colourRange(c, frames, &po)
	if err != nil {
		t.Fatal(err)
	}
	cs := chooseChromaScale(dev)
	m := &Meta{Frames: len(frames), Width: w, Height: h, FpsNum: 24, FpsDen: 1, Bits: 10, V5Steps: v5StepsVersion}
	m.setTiles(gpuTiles(w, h))
	m.setLayers(2, layerBaseQuality)
	root := ursa17kArchiveDir()
	for _, i := range []int{0, 4, 7} {
		buf, lay, ok, err := archiveFrame(po.FFmpeg, filepath.Join(root, "two-layers.mkv"), m, i)
		if err != nil || !ok {
			t.Fatalf("frame %d: %v, found %v", i, err, ok)
		}
		fr, err := c.frame(frames[i])
		if err != nil {
			t.Fatal(err)
		}
		fi, _, _, _, side, _, err := decodePlanes(fr, cs, planesChroma)
		if err != nil {
			t.Fatal(err)
		}
		y, u, v := unpackYUV(buf, w, h, lay)
		out, err := encodePlanesSteps(fr[:fi.offs[0]+fi.hs], side, y, u, v, cs, 0, v5StepsVersion)
		if err != nil {
			t.Fatal(err)
		}
		again, err := requantFrame(out, 1, 0)
		if err != nil || !bytes.Equal(again, out) {
			t.Fatalf("rebuilt frame %d failed canonical rewrite: %v", frames[i], err)
		}
		_, ry, _, _, _, _, err := decodePlanes(out, cs, planesLuma)
		if err != nil {
			t.Fatal(err)
		}
		p := psnr12(y, ry)
		if p < 60 {
			t.Fatalf("rebuilt frame %d lost archive precision: %.3f dB", frames[i], p)
		}
		t.Logf("source frame %d: rebuilt %d bytes, canonical identity, archive-to-rebuild %.3f dB", frames[i], len(out), p)
	}
}

// Opt-in real-footage test: exercise grouped encoding, mixed decoding and the
// fine-detail comparison on identical stretches without encoding the entire clip.
func TestUrsa17KArchive(t *testing.T) {
	path := os.Getenv("BRAW_V5_17K_CLIP")
	if path == "" {
		t.Skip("BRAW_V5_17K_CLIP not set")
	}
	clip, err := OpenClip(path)
	if err != nil {
		t.Fatal(err)
	}
	defer clip.Close()
	frames := []int{0, 1, 2, 3, 165, 166, 329, 330}
	po := PackOptions{Quality: AutoQuality, Progress: func(string, int, int) {}}
	if err := po.defaults(); err != nil {
		t.Fatal(err)
	}
	w, h, dev, err := colourRange(clip, frames, &po)
	if err != nil {
		t.Fatal(err)
	}
	cs := chooseChromaScale(dev)
	step := clipStep(clip, cs)
	num, den, _, err := clip.SensorRate()
	if err != nil {
		t.Fatal(err)
	}
	root := ursa17kArchiveDir()
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	t.Logf("source %dx%d, frames %v, chroma scale %g, camera step %g", w, h, frames, cs, step)
	var results []*VerifyResult
	for _, layers := range []int{1, autoLayers(po.Encoder, true)} {
		po.Layers = layers
		po.Quality = po.autoQuality(step)
		name := "one-layer.mkv"
		if layers == 2 {
			name = "two-layers.mkv"
		}
		out := filepath.Join(root, name)
		rd := &restoreData{Meta: Meta{Frames: len(frames), Width: w, Height: h, ChromaScale: cs,
			FpsNum: num, FpsDen: den, Encoder: po.Encoder, Speed: po.Speed, Quality: po.Quality, Bits: 10, V5Steps: v5StepsVersion}}
		po.Progress = func(s string, done, total int) {
			if done == total {
				t.Logf("%d layers: %s %d/%d", layers, s, done, total)
			}
		}
		if _, err := encodePicture(clip, frames, &po, rd, out); err != nil {
			t.Fatal(err)
		}
		if len(rd.Hdrs) != len(frames) || len(rd.Side) != len(frames) || len(rd.Meta.Tiles) != 6 || rd.Meta.layers() != layers {
			t.Fatalf("bad restore metadata: %d headers, %d side fields, %v", len(rd.Hdrs), len(rd.Side), rd.Meta)
		}
		ends, err := videoBlockEnds(out)
		if err != nil || len(ends) != 6*layers {
			t.Fatalf("wrong tracks: %d, %v", len(ends), err)
		}
		for _, end := range ends {
			if len(end) != len(frames) {
				t.Fatalf("track has %d frames", len(end))
			}
		}
		v, err := checkPicture(nil, clip, frames, out, rd, po.FFmpeg, po.Progress, true, true)
		if err != nil {
			t.Fatal(err)
		}
		st, err := os.Stat(out)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("RESULT layers%d q%d bytes%d psnr%.3f detail%.3f clipped%.6f%% rebuildquant%g", layers, po.Quality, st.Size(), v.MeanPSNR, v.DetailKept, rd.Meta.ResidualClipped, v.RebuildQuant)
		results = append(results, v)
	}
	if results[1].MeanPSNR <= results[0].MeanPSNR || results[1].DetailKept < results[0].DetailKept {
		t.Fatal("two layers failed to improve the reconstruction")
	}
}
