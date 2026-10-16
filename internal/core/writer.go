package core

import (
	"fmt"
	"os"
)

// brawWriter writes a .braw file with new video frames around a source clip's own header,
// index and non-video data. Chunks keep the source's order and 4096-byte alignment; the index is
// rewritten with the new frame sizes and positions. The file appears under its final name only
// when finish succeeds.
type brawWriter struct {
	dst, tmp string
	g        *os.File
	pos      int64
	moov     []byte
	tracks   []*track
	order    []chunkRef
	chunks   [][]byte // non-video chunks, in file order
	sizes    []uint32
	next     int // next chunk in file order
	other    int // next non-video chunk
	done     bool
	// check, when set, is given the finished file under its temporary name; an error discards it.
	check func(path string) error
}

// newBrawWriter starts dst. pre is everything before the source's first chunk, moov its index,
// mdatEnd where its chunk data ended, chunks its non-video chunks in file order.
func newBrawWriter(dst string, pre, moov []byte, mdatEnd int64, chunks [][]byte) (*brawWriter, error) {
	tracks, order, err := layout(moov, mdatEnd)
	if err != nil {
		return nil, err
	}
	w := &brawWriter{dst: dst, tmp: dst + ".tmp", moov: moov, tracks: tracks, order: order, chunks: chunks,
		sizes: make([]uint32, len(tracks[0].chunks))}
	if w.g, err = os.Create(w.tmp); err != nil {
		return nil, err
	}
	if err := w.write(pre); err != nil {
		w.abort()
		return nil, err
	}
	return w, nil
}

func (w *brawWriter) frames() int { return len(w.sizes) }

func (w *brawWriter) write(b []byte) error {
	_, err := w.g.Write(b)
	w.pos += int64(len(b))
	return err
}

func (w *brawWriter) align() error {
	if r := w.pos % 4096; r != 0 {
		return w.write(make([]byte, 4096-r))
	}
	return nil
}

// frame writes video frame fi, preceded by the non-video chunks the source stored before it.
// Frames must be given in order. fi == frames() with a nil frame writes the trailing chunks.
func (w *brawWriter) frame(fi int, frame []byte) error {
	for w.next < len(w.order) {
		r := w.order[w.next]
		if r.ti == 0 && r.ci > fi {
			return nil
		}
		if err := w.align(); err != nil {
			return err
		}
		w.tracks[r.ti].offs[r.ci] = w.pos
		if r.ti == 0 {
			if r.ci != fi {
				return fmt.Errorf("video frames are not stored in order")
			}
			w.sizes[fi] = uint32(len(frame))
			if err := w.write(frame); err != nil {
				return err
			}
			w.next++
			continue
		}
		if w.other >= len(w.chunks) {
			return fmt.Errorf("missing non-video chunks")
		}
		// Trailing padding captured with the chunk is dropped; layout is re-aligned anyway.
		if err := w.write(w.chunks[w.other]); err != nil {
			return err
		}
		w.other++
		w.next++
	}
	return nil
}

// finish writes the trailing chunks and the index, and renames the file into place.
func (w *brawWriter) finish() (int64, error) {
	if err := w.frame(w.frames(), nil); err != nil {
		return 0, err
	}
	if err := w.align(); err != nil {
		return 0, err
	}
	moovPos := w.pos
	moov := append([]byte(nil), w.moov...)
	video := w.tracks[0]
	for i, s := range w.sizes {
		be.PutUint32(moov[video.stsz+20+4*i:], s)
	}
	for _, t := range w.tracks {
		for i, off := range t.offs {
			be.PutUint64(moov[t.co64+16+8*i:], uint64(off))
		}
	}
	if err := w.write(moov); err != nil {
		return 0, err
	}
	hdr := make([]byte, 16)
	if moovPos-8 < 1<<32 {
		be.PutUint32(hdr[0:], 8)
		copy(hdr[4:], "wide")
		be.PutUint32(hdr[8:], uint32(moovPos-8))
		copy(hdr[12:], "mdat")
	} else {
		be.PutUint32(hdr[0:], 1)
		copy(hdr[4:], "mdat")
		be.PutUint64(hdr[8:], uint64(moovPos))
	}
	if _, err := w.g.WriteAt(hdr, 0); err != nil {
		return 0, err
	}
	if err := w.g.Sync(); err != nil {
		return 0, err
	}
	if err := w.g.Close(); err != nil {
		return 0, err
	}
	if w.check != nil {
		if err := w.check(w.tmp); err != nil {
			return 0, err
		}
	}
	if err := os.Rename(w.tmp, w.dst); err != nil {
		return 0, err
	}
	w.done = true
	return w.pos, nil
}

// abort removes the unfinished file. It does nothing after a successful finish.
func (w *brawWriter) abort() {
	if !w.done {
		w.g.Close()
		os.Remove(w.tmp)
	}
}
