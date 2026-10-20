package core

import (
	"bytes"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// lossyBase is a 10-bit base for the planes src, as a lossy encoder might give it: each value
// rounded, then off by a little, and now and then by far more than the detail layer can carry.
func lossyBase(rng *rand.Rand, src []uint16) []uint16 {
	b := make([]uint16, len(src))
	for i, s := range src {
		v := int(to10(s)) + rng.Intn(61) - 30
		if rng.Intn(100) == 0 {
			v += rng.Intn(601) - 300
		}
		b[i] = uint16(max(0, min(v, 1023)))
	}
	return b
}

// times4 turns 10-bit planes into the 12-bit values packYUV rounds back to them.
func times4(p []uint16) []uint16 {
	out := make([]uint16, len(p))
	for i, v := range p {
		out[i] = v << 2
	}
	return out
}

// TestDetailLayers checks the detail layer and the rebuild from both layers, tile by tile, for
// each layout a base or a detail layer can be decoded in: the source comes back exactly wherever
// the difference fits the layer, and is otherwise cut to the nearest value it can carry.
func TestDetailLayers(t *testing.T) {
	const w, h = 320, 176
	rng := rand.New(rand.NewSource(3))
	Y, U, V := randomPlanes(rng, w, h)
	src := [3][]uint16{Y, U, V}
	var base [3][]uint16
	wantCut := int64(0)
	for p := range src {
		base[p] = lossyBase(rng, src[p])
		for i, s := range src[p] {
			if d := int(s) - 4*int(base[p][i]); d < -layerZero || d > 1023-layerZero {
				wantCut++
			}
		}
	}
	if wantCut == 0 {
		t.Fatal("the test base never puts the difference out of range")
	}
	for _, lb := range []pixLayout{semi10, planar10} {
		for _, ld := range []pixLayout{semi10, planar10} {
			pic := make([]byte, planeBytes(w, h))
			cut := int64(0)
			for _, tl := range testTiles {
				y, u, v := cropPlanes(times4(base[0]), times4(base[1]), times4(base[2]), w, tl)
				b := packYUV(y, u, v, lb)
				d := make([]byte, planeBytes(tl.W, tl.H))
				cut += detailTile(d, b, lb, src, w, tl)
				if ld != semi10 {
					y, u, v := unpackYUV(d, tl.W, tl.H, semi10)
					d = packYUV(y, u, v, ld)
				}
				addLayers(pic, w, h, tl, b, lb, d, ld)
			}
			if cut != wantCut {
				t.Errorf("%s/%s: %d samples cut, want %d", lb.pixFmt(), ld.pixFmt(), cut, wantCut)
			}
			gy, gu, gv := unpackYUV(pic, w, h, planar12)
			for p, got := range [][]uint16{gy, gu, gv} {
				for i, s := range src[p] {
					b4 := 4 * int(base[p][i])
					want := b4 + max(-layerZero, min(int(s)-b4, 1023-layerZero))
					if int(got[i]) != max(0, min(want, 4095)) {
						t.Fatalf("%s/%s: plane %d sample %d: source %d, base %d, rebuilt %d", lb.pixFmt(), ld.pixFmt(), p, i, s, b4, got[i])
					}
				}
			}
		}
	}
}

// TestAddLayersRange checks that the rebuild stays within 12 bits at the extremes.
func TestAddLayersRange(t *testing.T) {
	const w, h = 16, 8
	tl := Tile{0, 0, w, h}
	fill := func(v uint16) []byte {
		p := make([]uint16, w*h*3/2)
		for i := range p {
			p[i] = v << 2
		}
		return packYUV(p[:w*h], p[w*h:w*h*5/4], p[w*h*5/4:], semi10)
	}
	for _, c := range []struct{ base, detail, want uint16 }{
		{1023, 1023, 4095}, {0, 0, 0}, {1023, 0, 4092 - layerZero}, {0, 1023, 1023 - layerZero}, {300, layerZero, 1200},
	} {
		pic := make([]byte, planeBytes(w, h))
		addLayers(pic, w, h, tl, fill(c.base), semi10, fill(c.detail), semi10)
		y, u, v := unpackYUV(pic, w, h, planar12)
		for _, p := range [][]uint16{y, u, v} {
			for _, s := range p {
				if s != c.want {
					t.Fatalf("base %d, detail %d: rebuilt %d, want %d", c.base, c.detail, s, c.want)
				}
			}
		}
	}
	// A source at either end against a base at the other: the difference is cut, not wrapped.
	d := make([]byte, planeBytes(w, h))
	src := [3][]uint16{make([]uint16, w*h), make([]uint16, w*h/4), make([]uint16, w*h/4)}
	if cut := detailTile(d, fill(1023), semi10, src, w, tl); cut != w*h*3/2 {
		t.Errorf("source 0 on base 1023: %d samples cut", cut)
	}
	if y, _, _ := unpackYUV(d, w, h, semi10); y[0] != 0 {
		t.Errorf("source 0 on base 1023: detail %d, want 0", y[0]>>2)
	}
}

func TestLayeredRestoreData(t *testing.T) {
	dir := t.TempDir()
	base := Meta{Frames: 1, Width: 12336, Height: 8064, ChromaScale: 1, FpsNum: 24, FpsDen: 1, Bits: 10, Encoder: "nvenc"}
	write := func(m Meta) (*restoreData, error) {
		path := filepath.Join(dir, "restore.bin")
		rd := &restoreData{Meta: m, Hdrs: [][]byte{{1, 2}}, Side: [][]byte{{3}}}
		if err := rd.write(path); err != nil {
			t.Fatal(err)
		}
		return readRestoreData(path)
	}
	layered := func(tiles []Tile) Meta {
		m := base
		m.setTiles(tiles)
		m.setLayers(2, layerBaseQuality)
		return m
	}

	m := layered(gpuTiles(base.Width, base.Height))
	if m.Format != FormatLayered || m.trackCount() != 8 || len(m.trackTitles()) != 8 {
		t.Fatalf("tiled two-layer archive: format %d, %d tracks, %d titles", m.Format, m.trackCount(), len(m.trackTitles()))
	}
	rd, err := write(m)
	if err != nil || rd.Meta.layers() != 2 || rd.Meta.trackCount() != 8 || rd.Meta.BaseQuality != layerBaseQuality {
		t.Fatalf("tiled two-layer archive read back: %v %+v", err, rd)
	}
	m = layered(nil)
	if rd, err = write(m); err != nil || rd.Meta.Format != FormatLayered || rd.Meta.trackCount() != 2 || len(rd.Meta.trackTitles()) != 2 {
		t.Errorf("untiled two-layer archive: %v %+v", err, rd)
	}
	// Back to one layer: the format follows.
	m = layered(gpuTiles(base.Width, base.Height))
	m.setLayers(1, 0)
	if m.Format != FormatTiled || m.trackCount() != 4 || m.LayerGain != 0 || m.BaseQuality != 0 {
		t.Errorf("one layer again: %+v", m)
	}

	refused := map[string]func(*Meta){
		"newer format":            func(m *Meta) { m.Format = FormatLayered + 1 },
		"format 3 without layers": func(m *Meta) { m.setLayers(1, 0); m.Format = FormatLayered },
		"layers in format 2":      func(m *Meta) { m.Format = FormatTiled },
		"layers in format 1":      func(m *Meta) { m.Tiles = nil; m.Format = FormatVersion },
		"another gain":            func(m *Meta) { m.LayerGain = 2 },
		"three layers":            func(m *Meta) { m.Layers = 3 },
		"x265 layers":             func(m *Meta) { m.Encoder = "x265" },
		"12-bit layers":           func(m *Meta) { m.Bits = 12 },
		"no base level":           func(m *Meta) { m.BaseQuality = 0 },
		"tiles of another pic":    func(m *Meta) { m.Tiles = gpuTiles(16384, 8064) },
	}
	for name, f := range refused {
		m := layered(gpuTiles(base.Width, base.Height))
		f(&m)
		if _, err := write(m); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestLayeredDecode stores a two-layer tiled picture the way Pack does (base tracks, then detail
// tracks), losslessly so the samples can be compared, and checks that the archive decoders rebuild
// it, on the CPU, on the GPU when there is one, with the base decoded on the CPU and the detail on
// the GPU, and frame by frame.
func TestLayeredDecode(t *testing.T) {
	tl := CheckTools("")
	if !tl.X265 {
		t.Skip("no ffmpeg with libx265 on this machine")
	}
	const w, h, n = 320, 176, 3
	// Six tiles exercise the mixed GPU/CPU path used by 17K archives.
	tiles := []Tile{{0, 0, 112, 88}, {112, 0, 112, 88}, {224, 0, 96, 88}, {0, 88, 112, 88}, {112, 88, 112, 88}, {224, 88, 96, 88}}
	dir := t.TempDir()
	rng := rand.New(rand.NewSource(4))
	var want [][]byte
	var baseRaw, detailRaw []byte
	for i := 0; i < n; i++ {
		Y, U, V := randomPlanes(rng, w, h)
		src := [3][]uint16{Y, U, V}
		var b [3][]uint16
		for p := range src {
			b[p] = times4(lossyBase(rng, src[p]))
		}
		bpic := packYUV(b[0], b[1], b[2], semi10)
		dpic := make([]byte, len(bpic))
		rebuilt := make([]byte, planeBytes(w, h))
		for _, t := range tiles {
			bt, dt := make([]byte, planeBytes(t.W, t.H)), make([]byte, planeBytes(t.W, t.H))
			copyTile(bpic, bt, w, h, t, semi10, false)
			detailTile(dt, bt, semi10, src, w, t)
			copyTile(dpic, dt, w, h, t, semi10, true)
			addLayers(rebuilt, w, h, t, bt, semi10, dt, semi10)
		}
		want = append(want, rebuilt)
		baseRaw, detailRaw = append(baseRaw, bpic...), append(detailRaw, dpic...)
	}
	encode := func(mkv string, raw []byte) {
		args := []string{"-hide_banner", "-loglevel", "error", "-y", "-f", "rawvideo", "-pix_fmt", "p010le",
			"-s", strconv.Itoa(w) + "x" + strconv.Itoa(h), "-framerate", "24", "-i", "-"}
		for k, t := range tiles {
			args = append(args, "-map", "0:v", "-filter:v:"+strconv.Itoa(k), cropFilter(t))
		}
		args = append(args, "-c:v", "libx265", "-x265-params", "lossless=1:log-level=error", "-pix_fmt", "yuv420p10le",
			"-f", "matroska", mkv)
		cmd := command(tl.FFmpeg, args...)
		cmd.Stdin = bytes.NewReader(raw)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("encode: %v: %s", err, stderr.String())
		}
	}
	bmkv, dmkv, mkv := filepath.Join(dir, "base.mkv"), filepath.Join(dir, "detail.mkv"), filepath.Join(dir, "layers.mkv")
	encode(bmkv, baseRaw)
	encode(dmkv, detailRaw)
	if err := runFFmpeg(tl.FFmpeg, "-y", "-i", bmkv, "-i", dmkv, "-map", "0:v", "-map", "1:v", "-c", "copy", "-f", "matroska", mkv); err != nil {
		t.Fatal(err)
	}
	m := &Meta{Frames: n, Width: w, Height: h, FpsNum: 24, FpsDen: 1, Bits: 10, Encoder: "nvenc"}
	m.setTiles(tiles)
	m.setLayers(2, layerBaseQuality)
	same := func(route string, i int, buf []byte, l pixLayout) {
		if l != planar12 || !bytes.Equal(buf, want[i]) {
			t.Errorf("%s: frame %d differs from the rebuilt picture (layout %s)", route, i, l.pixFmt())
		}
	}
	defer func(b bool) { baseOnGPU = b }(baseOnGPU)
	for _, r := range []struct {
		name      string
		gpu, base bool
	}{{"cpu", false, true}, {"gpu", true, true}, {"gpu detail only", true, false}} {
		baseOnGPU = r.base
		got, err := decodeArchiveWith(tl.FFmpeg, mkv, m, r.gpu, func(i int, buf []byte, l pixLayout) error {
			same(r.name, i, buf, l)
			return nil
		})
		if err != nil && r.gpu && !tl.NVENC {
			t.Logf("%s: %v (no usable NVIDIA card)", r.name, err)
			continue
		}
		if err != nil || got != n {
			t.Errorf("%s: %d frames, %v", r.name, got, err)
		}
	}
	baseOnGPU = true
	for i := 0; i <= n; i++ {
		buf, l, ok, err := archiveFrame(tl.FFmpeg, mkv, m, i)
		if err != nil || ok != (i < n) {
			t.Fatalf("frame %d: ok %v, %v", i, ok, err)
		}
		if ok {
			same("seek", i, buf, l)
		}
	}
}

// TestLayeredPack needs a real clip (BRAW_TEST_CLIP) and an NVIDIA card: a stretch of the clip is
// stored in two layers and checked the way Pack checks an archive. Two layers must come closer to
// the source than one at the same level, and every frame's restore data must match.
func TestLayeredPack(t *testing.T) {
	path := os.Getenv("BRAW_TEST_CLIP")
	if path == "" {
		t.Skip("BRAW_TEST_CLIP not set")
	}
	if !CheckTools("").NVENC {
		t.Skip("no usable NVIDIA encoder")
	}
	clip, err := OpenClip(path)
	if err != nil {
		t.Fatal(err)
	}
	defer clip.Close()
	frames := []int{0, 1, 2, 3, 4, 5, 6, 7}
	fi := clip.firstFrame()
	num, den, _, err := clip.SensorRate()
	if err != nil || fi == nil {
		t.Fatal("cannot read the clip", err)
	}
	dir := t.TempDir()
	run := func(layers int) *VerifyResult {
		po := PackOptions{Quality: 16, Layers: layers, Progress: func(string, int, int) {}}
		if err := po.defaults(); err != nil {
			t.Fatal(err)
		}
		rd := &restoreData{Meta: Meta{Frames: len(frames), Width: fi.w, Height: fi.h, ChromaScale: 1,
			FpsNum: num, FpsDen: den, Encoder: "nvenc", Bits: 10, Quality: 16}}
		out := filepath.Join(dir, "l"+strconv.Itoa(layers)+".mkv")
		if _, gpuErr, err := encodeOrFallBack(clip, frames, &po, rd, out); err != nil || gpuErr != "" {
			t.Fatalf("%d layers: %v %s", layers, err, gpuErr)
		}
		if rd.Meta.layers() != layers || len(rd.Hdrs) != len(frames) {
			t.Fatalf("%d layers: archive has %d layers, %d frames", layers, rd.Meta.layers(), len(rd.Hdrs))
		}
		if n, err := videoBlockEnds(out); err != nil || len(n) != rd.Meta.trackCount() {
			t.Fatalf("%d layers: %d tracks, %v", layers, len(n), err)
		}
		v, err := checkPicture(nil, clip, frames, out, rd, po.FFmpeg, func(string, int, int) {}, true, true)
		if err != nil {
			t.Fatalf("%d layers: %v", layers, err)
		}
		t.Logf("%d layers: %.2f dB, %.1f%% of the detail, %.5f%% cut", layers, v.MeanPSNR, v.DetailKept, rd.Meta.ResidualClipped)
		return v
	}
	one, two := run(1), run(2)
	if two.MeanPSNR <= one.MeanPSNR || two.DetailKept < one.DetailKept {
		t.Errorf("two layers (%.2f dB, %.1f%%) are no closer than one (%.2f dB, %.1f%%)", two.MeanPSNR, two.DetailKept, one.MeanPSNR, one.DetailKept)
	}
}

func TestLayerOptions(t *testing.T) {
	for _, c := range []struct {
		encoder string
		layers  int
		ok      bool
	}{{"nvenc", 0, true}, {"nvenc", 1, true}, {"nvenc", 2, true}, {"x265", 1, true}, {"x265", 2, false}, {"nvenc", 3, false}, {"nvenc", -1, false}} {
		o := PackOptions{Encoder: c.encoder, Layers: c.layers, FFmpeg: "ffmpeg"}
		if err := o.defaults(); (err == nil) != c.ok {
			t.Errorf("%s with %d layers: %v", c.encoder, c.layers, err)
		}
	}
	// The detail layer's automatic level follows x265's: 4 for the URSA Cine 12K test clip
	// (step 0.71), 10 for Pocket Cinema Camera 4K clips.
	for _, s := range []float64{0.71, 1.4, 2.6, 4} {
		two := PackOptions{Encoder: "nvenc", Layers: 2}
		one := PackOptions{Encoder: "nvenc", Layers: 1}
		if got, want := two.autoQuality(s), autoQuality(s, "x265")+layerQualityOffset; got != want {
			t.Errorf("step %g: two layers at %d, want %d", s, got, want)
		}
		if got := one.autoQuality(s); got != autoQuality(s, "nvenc") {
			t.Errorf("step %g: one layer at %d", s, got)
		}
	}
	if q := autoLayerQuality(0.71); q != 16 {
		t.Errorf("12K clip: detail layer at %d, want 16", q)
	}
}

// TestAutoLayers checks the automatic choice: two layers for RGBW-sensor clips (version 5) on the
// GPU, one otherwise.
func TestAutoLayers(t *testing.T) {
	for _, c := range []struct {
		encoder string
		rgbw    bool
		want    int
	}{{"nvenc", true, 2}, {"nvenc", false, 1}, {"x265", true, 1}, {"x265", false, 1}} {
		if got := autoLayers(c.encoder, c.rgbw); got != c.want {
			t.Errorf("%s, RGBW %v: %d layers, want %d", c.encoder, c.rgbw, got, c.want)
		}
	}
}
