package core

import (
	"bytes"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestGPUTiles(t *testing.T) {
	// URSA Cine 12K: four tiles of 6168x4032, row by row from the top left.
	want := []Tile{{0, 0, 6168, 4032}, {6168, 0, 6168, 4032}, {0, 4032, 6168, 4032}, {6168, 4032, 6168, 4032}}
	if got := gpuTiles(12336, 8064); !reflect.DeepEqual(got, want) {
		t.Errorf("12K: got %v", got)
	}
	for _, s := range [][2]int{{4096, 2160}, {6144, 3456}, {8192, 8192}, {8192, 5360}} {
		if got := gpuTiles(s[0], s[1]); got != nil {
			t.Errorf("%dx%d fits the encoder but got tiles %v", s[0], s[1], got)
		}
	}
	for w := 16; w <= 40000; w += 16 * 97 {
		for h := 8; h <= 40000; h += 8 * 89 {
			tiles := gpuTiles(w, h)
			if tiles == nil {
				if w > nvencMaxSide || h > nvencMaxSide {
					t.Fatalf("%dx%d: no tiles", w, h)
				}
				continue
			}
			if err := checkTiles(tiles, w, h); err != nil {
				t.Fatalf("%dx%d: %v (%v)", w, h, err, tiles)
			}
			for _, tl := range tiles {
				if tl.W*tl.H > hevcMaxLumaPs {
					t.Fatalf("%dx%d: tile %v is larger than HEVC allows", w, h, tl)
				}
			}
		}
	}
}

func TestGPUTileGroups(t *testing.T) {
	for _, tc := range []struct {
		w, h   int
		counts []int
	}{{12336, 8064, []int{4}}, {17568, 8064, []int{4, 2}}, {32768, 16384, nil}} {
		tiles := gpuTiles(tc.w, tc.h)
		groups := gpuTileGroups(tiles, gpuMaxTracks)
		var counts []int
		var joined []Tile
		for _, group := range groups {
			pixels := 0
			for _, tile := range group {
				pixels += tile.W * tile.H
			}
			if len(group) > gpuMaxTracks || pixels > gpuMaxPixels {
				t.Fatalf("unbounded group: %v, %d pixels", group, pixels)
			}
			counts = append(counts, len(group))
			joined = append(joined, group...)
		}
		if !reflect.DeepEqual(joined, tiles) || tc.counts != nil && !reflect.DeepEqual(counts, tc.counts) {
			t.Fatalf("%dx%d: groups %v", tc.w, tc.h, counts)
		}
	}
}

func TestLargeArchiveGPUTracks(t *testing.T) {
	m := &Meta{Width: 17568, Height: 8064, Bits: 10}
	m.setTiles(gpuTiles(m.Width, m.Height))
	defer func(b bool) { baseOnGPU = b }(baseOnGPU)
	baseOnGPU = false
	for _, layers := range []int{1, 2} {
		m.setLayers(layers, layerBaseQuality)
		for k := 0; k < m.trackCount(); k++ {
			want := layers == 2 && k >= 6 && k < 10
			if got := trackOnGPU(m, true, k); got != want {
				t.Fatalf("layers %d track %d: GPU %v, want %v", layers, k, got, want)
			}
			if trackOnGPU(m, false, k) {
				t.Fatal("used GPU on CPU route")
			}
		}
	}
}

func TestCheckTiles(t *testing.T) {
	good := gpuTiles(12336, 8064)
	if err := checkTiles(good, 12336, 8064); err != nil {
		t.Fatal(err)
	}
	bad := map[string][]Tile{
		"one tile":   {{0, 0, 12336, 8064}},
		"gap":        good[:3],
		"overlap":    {{0, 0, 6168, 4032}, {6160, 0, 6176, 4032}, {0, 4032, 6168, 4032}, {6168, 4032, 6168, 4032}},
		"misaligned": {{0, 0, 6164, 4032}, {6164, 0, 6172, 4032}, {0, 4032, 6168, 4032}, {6168, 4032, 6168, 4032}},
		"outside":    {{0, 0, 6168, 4032}, {6168, 0, 6176, 4032}, {0, 4032, 6168, 4032}, {6168, 4032, 6168, 4032}},
		"too wide":   {{0, 0, 12336, 4032}, {0, 4032, 12336, 4032}},
		"negative":   {{-8, 0, 6176, 4032}, {6168, 0, 6168, 4032}, {0, 4032, 6168, 4032}, {6168, 4032, 6168, 4032}},
	}
	for name, tiles := range bad {
		if checkTiles(tiles, 12336, 8064) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// randomPlanes returns 12-bit planes of a w x h picture.
func randomPlanes(rng *rand.Rand, w, h int) (Y, U, V []uint16) {
	Y, U, V = make([]uint16, w*h), make([]uint16, w*h/4), make([]uint16, w*h/4)
	for _, p := range [][]uint16{Y, U, V} {
		for i := range p {
			p[i] = uint16(rng.Intn(4096))
		}
	}
	return
}

// cropPlanes cuts tile t out of the planes directly, as a reference for copyTile.
func cropPlanes(Y, U, V []uint16, w int, t Tile) (y, u, v []uint16) {
	for r := 0; r < t.H; r++ {
		y = append(y, Y[(t.Y+r)*w+t.X:][:t.W]...)
	}
	for r := 0; r < t.H/2; r++ {
		u = append(u, U[(t.Y/2+r)*w/2+t.X/2:][:t.W/2]...)
		v = append(v, V[(t.Y/2+r)*w/2+t.X/2:][:t.W/2]...)
	}
	return
}

// testTiles is an uneven 2x2 grid of a 320x176 picture.
var testTiles = []Tile{{0, 0, 168, 96}, {168, 0, 152, 96}, {0, 96, 168, 80}, {168, 96, 152, 80}}

func TestCopyTile(t *testing.T) {
	const w, h = 320, 176
	if err := checkTiles(testTiles, w, h); err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(1))
	Y, U, V := randomPlanes(rng, w, h)
	for _, l := range []pixLayout{planar12, planar10, semi10} {
		pic := packYUV(Y, U, V, l)
		back := make([]byte, len(pic))
		for _, tl := range testTiles {
			tile := make([]byte, planeBytes(tl.W, tl.H))
			copyTile(pic, tile, w, h, tl, l, false)
			y, u, v := cropPlanes(Y, U, V, w, tl)
			if !bytes.Equal(tile, packYUV(y, u, v, l)) {
				t.Fatalf("%s: tile %v cut wrongly", l.pixFmt(), tl)
			}
			copyTile(back, tile, w, h, tl, l, true)
		}
		if !bytes.Equal(back, pic) {
			t.Errorf("%s: stitched tiles differ from the picture", l.pixFmt())
		}
	}
}

func TestTiledRestoreData(t *testing.T) {
	dir := t.TempDir()
	base := Meta{Frames: 1, Width: 12336, Height: 8064, ChromaScale: 1, FpsNum: 24, FpsDen: 1, Bits: 10}
	write := func(m Meta) (*restoreData, error) {
		path := filepath.Join(dir, "restore.bin")
		rd := &restoreData{Meta: m, Hdrs: [][]byte{{1, 2}}, Side: [][]byte{{3}}}
		if err := rd.write(path); err != nil {
			t.Fatal(err)
		}
		return readRestoreData(path)
	}

	m := base
	m.setTiles(gpuTiles(m.Width, m.Height))
	if m.Format != FormatTiled {
		t.Fatalf("tiled archive has format %d", m.Format)
	}
	rd, err := write(m)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rd.Meta.Tiles, m.Tiles) || len(rd.Meta.tracks()) != 4 {
		t.Errorf("tiles read back as %v", rd.Meta.Tiles)
	}

	m = base
	m.setTiles(nil)
	if rd, err = write(m); err != nil || rd.Meta.Format != FormatVersion || len(rd.Meta.tracks()) != 1 ||
		rd.Meta.tracks()[0] != (Tile{0, 0, 12336, 8064}) {
		t.Errorf("untiled archive: %v %+v", err, rd)
	}

	refused := map[string]func(*Meta){
		"newer format":         func(m *Meta) { m.Format = FormatLayered + 1 },
		"tiles in format 1":    func(m *Meta) { m.Tiles = gpuTiles(m.Width, m.Height); m.Format = FormatVersion },
		"format 2 no tiles":    func(m *Meta) { m.Format = FormatTiled },
		"tiles of another pic": func(m *Meta) { m.setTiles(gpuTiles(16384, 8064)) },
	}
	for name, f := range refused {
		m := base
		m.setTiles(nil)
		f(&m)
		if _, err := write(m); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestTiledDecode stores a picture as tiles the way Pack does (one ffmpeg, a crop per track),
// losslessly so the samples can be compared, and checks that the archive decoders stitch them
// back into the same picture, on the CPU and on the GPU when there is one.
func TestTiledDecode(t *testing.T) {
	tl := CheckTools("")
	if !tl.X265 {
		t.Skip("no ffmpeg with libx265 on this machine")
	}
	const w, h, n = 320, 176, 3
	dir := t.TempDir()
	rng := rand.New(rand.NewSource(2))
	var frames [][]byte
	var raw []byte
	for i := 0; i < n; i++ {
		Y, U, V := randomPlanes(rng, w, h)
		for _, p := range [][]uint16{Y, U, V} {
			for k := range p {
				p[k] &^= 3 // 10-bit values, which every route keeps exactly
			}
		}
		frames = append(frames, packYUV(Y, U, V, planar12))
		raw = append(raw, packYUV(Y, U, V, semi10)...)
	}
	encode := func(mkv string, raw []byte) {
		args := []string{"-hide_banner", "-loglevel", "error", "-y", "-f", "rawvideo", "-pix_fmt", "p010le",
			"-s", strconv.Itoa(w) + "x" + strconv.Itoa(h), "-framerate", "24", "-i", "-"}
		for k, t := range testTiles {
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
	mkv := filepath.Join(dir, "tiles.mkv")
	encode(mkv, raw)
	m := &Meta{Frames: n, Width: w, Height: h, FpsNum: 24, FpsDen: 1, Bits: 10}
	m.setTiles(testTiles)
	same := func(route string, i int, buf []byte, l pixLayout) {
		Y, U, V := unpackYUV(buf, w, h, l)
		if !bytes.Equal(packYUV(Y, U, V, planar12), frames[i]) {
			t.Errorf("%s: frame %d differs after stitching", route, i)
		}
	}
	for _, r := range []struct {
		name string
		gpu  bool
	}{{"cpu", false}, {"gpu", true}} {
		got, err := decodeArchiveWith(tl.FFmpeg, mkv, m, r.gpu, func(i int, buf []byte, l pixLayout) error {
			same(r.name, i, buf, l)
			return nil
		})
		if err != nil && r.name == "gpu" && !tl.NVENC {
			t.Logf("gpu: %v (no usable NVIDIA card)", err)
			continue
		}
		if err != nil || got != n {
			t.Errorf("%s: %d frames, %v", r.name, got, err)
		}
	}
	for i := 0; i <= n; i++ {
		buf, l, ok, err := archiveFrame(tl.FFmpeg, mkv, m, i)
		if err != nil || ok != (i < n) {
			t.Fatalf("frame %d: ok %v, %v", i, ok, err)
		}
		if ok {
			same("seek", i, buf, l)
		}
	}

	// A track that is shorter than the others is an error, not a shorter clip.
	two, short := filepath.Join(dir, "two.mkv"), filepath.Join(dir, "short.mkv")
	encode(two, raw[:2*len(raw)/n])
	if err := runFFmpeg(tl.FFmpeg, "-y", "-i", mkv, "-i", two, "-map", "0:v:0", "-map", "0:v:1",
		"-map", "0:v:2", "-map", "1:v:3", "-c", "copy", "-f", "matroska", short); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeArchiveWith(tl.FFmpeg, short, m, false, func(int, []byte, pixLayout) error { return nil }); err == nil ||
		!strings.Contains(err.Error(), "frame 2") {
		t.Errorf("short track: %v", err)
	}
}

// TestFFmpegCrop checks that ffmpeg cuts the tiles out of a P010 picture, as Pack sends it, with
// exactly the samples copyTile takes, so the stitched picture is the one that was sent.
func TestFFmpegCrop(t *testing.T) {
	tl := CheckTools("")
	if tl.FFmpeg == "" {
		t.Skip("no ffmpeg on this machine")
	}
	const w, h = 320, 176
	Y, U, V := randomPlanes(rand.New(rand.NewSource(3)), w, h)
	pic := packYUV(Y, U, V, semi10)
	for _, tile := range testTiles {
		cmd := command(tl.FFmpeg, "-hide_banner", "-loglevel", "error", "-f", "rawvideo", "-pix_fmt", "p010le",
			"-s", strconv.Itoa(w)+"x"+strconv.Itoa(h), "-i", "-", "-vf", cropFilter(tile),
			"-f", "rawvideo", "-pix_fmt", "p010le", "-")
		cmd.Stdin = bytes.NewReader(pic)
		got, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		want := make([]byte, planeBytes(tile.W, tile.H))
		copyTile(pic, want, w, h, tile, semi10, false)
		if !bytes.Equal(got, want) {
			t.Errorf("tile %v: ffmpeg's crop differs from copyTile's", tile)
		}
	}
}

func TestVideoBlockEnds(t *testing.T) {
	tl := CheckTools("")
	if !tl.X265 {
		t.Skip("no ffmpeg with libx265 on this machine")
	}
	const w, h, n = 320, 176, 3
	Y, U, V := randomPlanes(rand.New(rand.NewSource(4)), w, h)
	raw := bytes.Repeat(packYUV(Y, U, V, planar12), n)
	mkv := filepath.Join(t.TempDir(), "t.mkv")
	args := []string{"-hide_banner", "-loglevel", "error", "-y", "-f", "rawvideo", "-pix_fmt", "yuv420p12le",
		"-s", "320x176", "-framerate", "24", "-i", "-"}
	for k, tile := range testTiles {
		args = append(args, "-map", "0:v", "-filter:v:"+strconv.Itoa(k), cropFilter(tile))
	}
	cmd := command(tl.FFmpeg, append(args, "-c:v", "libx265", "-x265-params", "log-level=error", "-f", "matroska", mkv)...)
	cmd.Stdin = bytes.NewReader(raw)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	ends, err := videoBlockEnds(mkv)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(mkv)
	if len(ends) != len(testTiles) {
		t.Fatalf("%d video tracks", len(ends))
	}
	for k, e := range ends {
		if len(e) != n {
			t.Errorf("track %d: %d frames", k, len(e))
		}
		for i := range e {
			if (i > 0 && e[i] <= e[i-1]) || e[i] > st.Size() {
				t.Errorf("track %d: frame ends %v", k, e)
			}
		}
	}
}
