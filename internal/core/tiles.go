package core

import (
	"bufio"
	"fmt"
	"io"
	"math/bits"
	"os"
	"sync/atomic"
	"time"
)

// Pictures wider or taller than the NVIDIA encoder accepts (8192 pixels; a 12K picture is 12336
// wide) are stored as a grid of tiles, one HEVC track per tile in the same archive, and stitched
// back together when the archive is decoded. Only the NVIDIA encoder needs this: x265 takes the
// whole picture as one track.

// Tile is one part of the picture, in brightness-plane pixels, stored as its own video track.
type Tile struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

const (
	nvencMaxSide = 8192 // widest and tallest picture the NVIDIA encoder and decoder take
	// hevcMaxLumaPs is the largest picture HEVC allows at any level (levels 6 to 6.2), which the
	// tiles are also kept within so that every track is an ordinary HEVC stream.
	hevcMaxLumaPs = 35651584
	tileAlign     = 8 // tile edges fall on multiples of this, so colour samples split evenly
	maxTiles      = 64
	// Keep simultaneous GPU sessions within the envelope tested on an 8 GB card:
	// four 6168x4032 tiles for 12K. Larger pictures take several encoding passes.
	gpuMaxTracks = 4
	gpuMaxPixels = 4 * 6168 * 4032
)

// gpuTileGroups keeps track order while bounding simultaneous sessions and pixels.
func gpuTileGroups(tiles []Tile, tracks int) [][]Tile {
	var groups [][]Tile
	for len(tiles) > 0 {
		n, pixels := 0, 0
		for n < len(tiles) && n < max(1, tracks) {
			area := tiles[n].W * tiles[n].H
			if n > 0 && pixels+area > gpuMaxPixels {
				break
			}
			pixels += area
			n++
		}
		groups = append(groups, tiles[:n])
		tiles = tiles[n:]
	}
	return groups
}

// gpuTiles is how a w x h picture is split for the NVIDIA encoder: nil when it fits as it is,
// otherwise a grid in track order, row by row from the top left. 12336x8064 (URSA Cine 12K)
// gives four tiles of 6168x4032, whose edges also fall on the clip's own tile edges.
func gpuTiles(w, h int) []Tile {
	if w <= nvencMaxSide && h <= nvencMaxSide {
		return nil
	}
	cols, rows := ceilDiv(w, nvencMaxSide), ceilDiv(h, nvencMaxSide)
	for span(w, cols)*span(h, rows) > hevcMaxLumaPs {
		if span(w, cols) >= span(h, rows) {
			cols++
		} else {
			rows++
		}
	}
	var out []Tile
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			t := Tile{X: c * span(w, cols), Y: r * span(h, rows), W: span(w, cols), H: span(h, rows)}
			t.W, t.H = min(t.W, w-t.X), min(t.H, h-t.Y)
			out = append(out, t)
		}
	}
	return out
}

func ceilDiv(a, b int) int { return (a + b - 1) / b }

// span is the size of each of k tiles across n pixels (the last may be smaller).
func span(n, k int) int { return ceilDiv(ceilDiv(n, k), tileAlign) * tileAlign }

// checkTiles reports whether tiles cover a w x h picture exactly, without overlapping, on
// tileAlign edges, each one within what the encoders take.
func checkTiles(tiles []Tile, w, h int) error {
	if len(tiles) < 2 || len(tiles) > maxTiles {
		return fmt.Errorf("%d tiles", len(tiles))
	}
	area := 0
	for i, t := range tiles {
		if t.X < 0 || t.Y < 0 || t.W <= 0 || t.H <= 0 || t.X+t.W > w || t.Y+t.H > h ||
			t.X%tileAlign != 0 || t.Y%tileAlign != 0 || t.W%tileAlign != 0 || t.H%tileAlign != 0 ||
			t.W > nvencMaxSide || t.H > nvencMaxSide {
			return fmt.Errorf("tile %d (%dx%d at %d,%d) does not fit a %dx%d picture", i, t.W, t.H, t.X, t.Y, w, h)
		}
		for _, u := range tiles[:i] {
			if t.X < u.X+u.W && u.X < t.X+t.W && t.Y < u.Y+u.H && u.Y < t.Y+t.H {
				return fmt.Errorf("tile %d overlaps another", i)
			}
		}
		area += t.W * t.H
	}
	if area != w*h {
		return fmt.Errorf("tiles do not cover the picture")
	}
	return nil
}

// tracks lists the parts of the picture in track order: the archive's tiles, or the whole picture
// as its one track.
func (m *Meta) tracks() []Tile {
	if len(m.Tiles) > 0 {
		return m.Tiles
	}
	return []Tile{{0, 0, m.Width, m.Height}}
}

// trackCount is how many video tracks hold the picture: one per tile and layer.
func (m *Meta) trackCount() int { return len(m.tracks()) * m.layers() }

// setTiles records how the picture is stored. Tiled archives carry a higher format number, so
// that a braw-pack which does not know tiles refuses them instead of misreading the picture.
func (m *Meta) setTiles(t []Tile) {
	m.Tiles = t
	m.setFormat()
}

// setFormat sets the lowest format number that describes how the picture is stored.
func (m *Meta) setFormat() {
	switch {
	case m.Layers == 2:
		m.Format = FormatLayered
	case len(m.Tiles) > 0:
		m.Format = FormatTiled
	default:
		m.Format = FormatVersion
	}
}

// planeSpan is where one plane of a packed frame lies: its byte offset, bytes per row and rows.
type planeSpan struct{ off, stride, rows int }

// layoutPlanes describes the planes of a w x h frame packed in layout l (see packYUV).
func layoutPlanes(l pixLayout, w, h int) []planeSpan {
	if l == semi10 {
		return []planeSpan{{0, 2 * w, h}, {2 * w * h, 2 * w, h / 2}}
	}
	return []planeSpan{{0, 2 * w, h}, {2 * w * h, w, h / 2}, {2*w*h + w*h/2, w, h / 2}}
}

// copyTile moves the samples of tile t between a packed frame of the whole w x h picture and a
// packed frame of the tile alone, both in layout l: into the picture (stitching) when in is set,
// out of it (cropping) otherwise.
func copyTile(pic, tile []byte, w, h int, t Tile, l pixLayout, in bool) {
	tp := layoutPlanes(l, t.W, t.H)
	for i, p := range layoutPlanes(l, w, h) {
		q := tp[i]
		x, y := t.X*p.stride/w, t.Y*p.rows/h
		for r := 0; r < q.rows; r++ {
			a := pic[p.off+(y+r)*p.stride+x:][:q.stride]
			b := tile[q.off+r*q.stride:][:q.stride]
			if in {
				copy(a, b)
			} else {
				copy(b, a)
			}
		}
	}
}

// cropFilter is the ffmpeg filter that cuts tile t out of the picture.
func cropFilter(t Tile) string { return fmt.Sprintf("crop=%d:%d:%d:%d", t.W, t.H, t.X, t.Y) }

// feeder copies an archive into the input of the decoder of one of its tracks, never more than
// feedAhead frames of that track beyond those already read from the decoder: ffmpeg otherwise
// decodes as far ahead as its queues hold (about 40 frames, 3 GB per 6168x4032 track). Should the
// decoder need more input before it returns a frame, the allowance grows after a pause (feedStall).
type feeder struct {
	ends   []int64 // end of each of the track's frames in the file; nil = feed freely
	size   int64
	read   atomic.Int64 // frames read from the decoder
	extra  atomic.Int64 // frames added to the allowance because the decoder stalled
	wanted atomic.Bool  // a frame is being waited for
	wake   chan struct{}
}

const (
	feedAhead = 3
	// feedStall is how long a decoder may take to return a wanted frame at the end of its
	// allowance before the allowance grows. Decoding with one thread and no B-frames never needs
	// more input than the frames it returns, so this only guards against waiting forever; a GPU
	// busy with other work must not set it off.
	feedStall = 15 * time.Second
)

func newFeeder(ends []int64, size int64) *feeder {
	return &feeder{ends: ends, size: size, wake: make(chan struct{}, 1)}
}

// want says that a frame is being waited for (until got).
func (f *feeder) want() { f.wanted.Store(true) }

// got records that one more frame was read from the decoder.
func (f *feeder) got() {
	f.wanted.Store(false)
	f.read.Add(1)
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

// limit is how far into the file the decoder may be fed now.
func (f *feeder) limit() int64 {
	i := f.read.Load() + feedAhead + f.extra.Load()
	if f.ends == nil || i >= int64(len(f.ends)) {
		return f.size
	}
	return f.ends[i-1]
}

// run copies the file at path into w within the allowance, then closes w. It stops early when
// done is closed or the decoder stops reading.
func (f *feeder) run(path string, w io.WriteCloser, done <-chan struct{}) {
	defer w.Close()
	src, err := os.Open(path)
	if err != nil {
		return // the decoder then fails on its empty input, and says so
	}
	defer src.Close()
	buf := make([]byte, 256<<10)
	var pos int64
	for pos < f.size {
		if lim := f.limit(); pos < lim {
			want := len(buf)
			if lim-pos < int64(want) {
				want = int(lim - pos)
			}
			n, err := src.Read(buf[:want])
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return
				}
				pos += int64(n)
			}
			if err != nil {
				return
			}
			continue
		}
		select {
		case <-f.wake:
		case <-time.After(feedStall):
			if f.wanted.Load() {
				f.extra.Add(1)
			}
		case <-done:
			return
		}
	}
}

// Matroska IDs read by videoBlockEnds.
const (
	mkvSegment    = 0x18538067
	mkvCluster    = 0x1F43B675
	mkvTracks     = 0x1654AE6B
	mkvTrackEntry = 0xAE
	mkvTrackNum   = 0xD7
	mkvTrackType  = 0x83
	mkvBlockGroup = 0xA0
	mkvBlock      = 0xA1
	mkvSimple     = 0xA3
)

// videoBlockEnds lists, for each video track of a Matroska file in order, where each of its
// blocks (one frame each) ends in the file. It reads only element headers, track entries and the
// first bytes of each block.
func videoBlockEnds(path string) ([][]int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	r := bufio.NewReaderSize(f, 1<<16)
	var pos int64
	vint := func(keepMarker bool) (uint64, int, error) {
		b, err := r.ReadByte()
		if err != nil {
			return 0, 0, err
		}
		n := bits.LeadingZeros8(b) + 1
		if n > 8 {
			return 0, 0, fmt.Errorf("bad element at %d", pos)
		}
		v := uint64(b)
		if !keepMarker {
			v &= 0xFF >> n
		}
		for i := 1; i < n; i++ {
			c, err := r.ReadByte()
			if err != nil {
				return 0, 0, err
			}
			v = v<<8 | uint64(c)
		}
		pos += int64(n)
		return v, n, nil
	}
	skip := func(n int64) error {
		d, err := r.Discard(int(n))
		pos += int64(d)
		return err
	}
	var video []uint64 // video track numbers, in track order
	blocks := map[uint64][]int64{}
	var num, typ uint64
	inEntry := int64(-1) // end of the track entry being read
	for pos < st.Size() {
		if inEntry >= 0 && pos >= inEntry {
			if typ == 1 {
				video = append(video, num)
			}
			num, typ, inEntry = 0, 0, -1
		}
		id, _, err := vint(true)
		if err != nil {
			return nil, err
		}
		size, n, err := vint(false)
		if err != nil {
			return nil, err
		}
		unknown := size == 1<<(7*n)-1
		switch id {
		case mkvSegment, mkvCluster, mkvTracks, mkvBlockGroup:
			continue // look inside
		case mkvTrackEntry:
			inEntry = pos + int64(size)
			continue
		}
		if unknown || pos+int64(size) > st.Size() {
			return nil, fmt.Errorf("element %x at %d has no usable size", id, pos)
		}
		end := pos + int64(size)
		switch id {
		case mkvTrackNum, mkvTrackType:
			var v uint64
			for i := uint64(0); i < size; i++ {
				c, err := r.ReadByte()
				if err != nil {
					return nil, err
				}
				v = v<<8 | uint64(c)
			}
			pos = end
			if id == mkvTrackNum {
				num = v
			} else {
				typ = v
			}
		case mkvBlock, mkvSimple:
			start := pos
			track, _, err := vint(false)
			if err != nil {
				return nil, err
			}
			blocks[track] = append(blocks[track], end)
			if err := skip(end - (pos - start) - start); err != nil {
				return nil, err
			}
		default:
			if err := skip(int64(size)); err != nil {
				return nil, err
			}
		}
	}
	out := make([][]int64, len(video))
	for k, t := range video {
		out[k] = blocks[t]
	}
	return out, nil
}
