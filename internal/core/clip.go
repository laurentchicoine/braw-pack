package core

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sort"
)

// chunkRef identifies one chunk of one track; refs are kept in file order.
type chunkRef struct {
	off    int64
	ti, ci int
}

// Clip is an opened .braw file: its index, and access to frames and non-video data.
type Clip struct {
	Path    string
	Size    int64
	f       *os.File
	pre     []byte // everything before the first chunk (header atoms)
	moov    []byte
	moovPos int64
	tracks  []*track
	order   []chunkRef
}

// layout parses moov and derives the chunk order. mdatEnd bounds the last chunk.
func layout(moov []byte, mdatEnd int64) ([]*track, []chunkRef, error) {
	if len(moov) < 8 || string(moov[4:8]) != "moov" || int(be.Uint32(moov[0:4])) != len(moov) {
		return nil, nil, fmt.Errorf("moov must be the last atom")
	}
	tracks, err := parseMoov(moov)
	if err != nil {
		return nil, nil, err
	}
	if len(tracks) == 0 {
		return nil, nil, fmt.Errorf("no tracks")
	}
	if len(tracks[0].chunks) == 0 {
		return nil, nil, fmt.Errorf("clip has no frames")
	}
	if tracks[0].fixed != 0 {
		return nil, nil, fmt.Errorf("expected a size for each frame")
	}
	for _, s := range tracks[0].spc {
		if s != 1 {
			return nil, nil, fmt.Errorf("expected one frame per chunk")
		}
	}
	var order []chunkRef
	for ti, t := range tracks {
		for ci, c := range t.chunks {
			order = append(order, chunkRef{c.off, ti, ci})
		}
	}
	sort.Slice(order, func(a, b int) bool { return order[a].off < order[b].off })
	// A truncated or damaged file shows up here: its index points past the data that is there.
	for _, r := range order {
		if r.off < 0 || r.off > mdatEnd || (r.ti == 0 && tracks[0].chunks[r.ci].size > mdatEnd-r.off) {
			return nil, nil, fmt.Errorf("index points outside the file (truncated or damaged clip?)")
		}
	}
	// Non-video chunks extend to the start of the next chunk: QuickTime PCM audio declares a
	// sample size of 1 whatever the real bytes per sample, so table-derived sizes are unreliable.
	for i, r := range order {
		if r.ti == 0 {
			continue
		}
		end := mdatEnd
		if i+1 < len(order) {
			end = order[i+1].off
		}
		tracks[r.ti].chunks[r.ci].size = end - r.off
	}
	return tracks, order, nil
}

// OpenClip opens a .braw for reading. The file is never modified.
func OpenClip(path string) (*Clip, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	c := &Clip{Path: path, f: f}
	fail := func(err error) (*Clip, error) {
		f.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	st, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	c.Size = st.Size()
	head := make([]byte, 16)
	if _, err := f.ReadAt(head, 0); err != nil {
		return fail(err)
	}
	switch {
	case string(head[4:8]) == "wide" && string(head[12:16]) == "mdat":
		c.moovPos = 8 + int64(be.Uint32(head[8:12]))
	case be.Uint32(head[0:4]) == 1 && string(head[4:8]) == "mdat":
		c.moovPos = int64(be.Uint64(head[8:16]))
	default:
		return fail(fmt.Errorf("unexpected file layout (expected mdat first)"))
	}
	if c.moovPos <= 16 || c.moovPos >= c.Size {
		return fail(fmt.Errorf("bad mdat size"))
	}
	if c.Size-c.moovPos > 1<<30 {
		return fail(fmt.Errorf("index is implausibly large"))
	}
	c.moov = make([]byte, c.Size-c.moovPos)
	if _, err := f.ReadAt(c.moov, c.moovPos); err != nil {
		return fail(err)
	}
	if c.tracks, c.order, err = layout(c.moov, c.moovPos); err != nil {
		return fail(err)
	}
	c.pre = make([]byte, c.order[0].off)
	if _, err := f.ReadAt(c.pre, 0); err != nil {
		return fail(err)
	}
	return c, nil
}

func (c *Clip) Close() error { return c.f.Close() }

// Frames is the number of video frames.
func (c *Clip) Frames() int { return len(c.tracks[0].chunks) }

// VideoBytes is the total size of all video frames.
func (c *Clip) VideoBytes() int64 {
	var n int64
	for _, ch := range c.tracks[0].chunks {
		n += ch.size
	}
	return n
}

func (c *Clip) frame(i int) ([]byte, error) {
	ch := c.tracks[0].chunks[i]
	fr := make([]byte, ch.size)
	if _, err := c.f.ReadAt(fr, ch.off); err != nil {
		return nil, fmt.Errorf("frame %d: %w", i, err)
	}
	return fr, nil
}

// otherChunks returns every non-video chunk's bytes in file order.
func (c *Clip) otherChunks() ([][]byte, error) {
	var out [][]byte
	for _, r := range c.order {
		if r.ti == 0 {
			continue
		}
		b := make([]byte, c.tracks[r.ti].chunks[r.ci].size)
		if _, err := c.f.ReadAt(b, r.off); err != nil && err != io.EOF {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// SensorRate is the captured frame rate, read from the camera's per-frame metadata ("srte").
// It falls back to the container's playback rate when the field is missing.
func (c *Clip) SensorRate() (num, den int, fromSensor bool, err error) {
	fr, err := c.frame(0)
	if err != nil {
		return 0, 0, false, err
	}
	if len(fr) >= 0x100 {
		if i := bytes.Index(fr[:0x100], []byte("srte")); i >= 0 && i+12 <= 0x100 {
			n, d := int(be.Uint32(fr[i+4:])), int(be.Uint32(fr[i+8:]))
			if n > 0 && d > 0 {
				return n, d, true, nil
			}
		}
	}
	t := c.tracks[0]
	if t.mdhd > 0 && t.stts > 0 && t.mdhd+24 <= len(c.moov) && t.stts+24 <= len(c.moov) {
		scale := int(be.Uint32(c.moov[t.mdhd+20:]))
		if int(be.Uint32(c.moov[t.stts+12:])) >= 1 {
			delta := int(be.Uint32(c.moov[t.stts+20:]))
			if scale > 0 && delta > 0 {
				return scale, delta, false, nil
			}
		}
	}
	return 0, 0, false, fmt.Errorf("cannot determine frame rate")
}
