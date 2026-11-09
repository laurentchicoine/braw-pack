package core

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// gapClip writes a small clip: a video track of distinct frames whose time table is stts (pairs
// of count and duration), and a second track with one chunk after every video frame, as sound
// is stored. It returns the path and the frames.
func gapClip(t *testing.T, stts ...uint32) (string, [][]byte) {
	t.Helper()
	u32 := func(v ...uint32) []byte {
		b := make([]byte, 4*len(v))
		for i, x := range v {
			be.PutUint32(b[4*i:], x)
		}
		return b
	}
	u64 := func(v int64) []byte { b := make([]byte, 8); be.PutUint64(b, uint64(v)); return b }
	var n uint32
	for i := 0; i < len(stts); i += 2 {
		n += stts[i]
	}
	head := make([]byte, 16)
	var data []byte
	var frames [][]byte
	var vsz, voff, aoff []byte
	pos := int64(4096)
	pad := func() {
		for (int64(len(data))+4096)%4096 != 0 {
			data = append(data, 0)
		}
		pos = 4096 + int64(len(data))
	}
	for i := uint32(0); i < n; i++ {
		fr := bytes.Repeat([]byte{byte(0x10 + i)}, 1000+int(i)*100)
		frames = append(frames, fr)
		vsz = append(vsz, u32(uint32(len(fr)))...)
		voff = append(voff, u64(pos)...)
		data = append(data, fr...)
		pad()
		aoff = append(aoff, u64(pos)...)
		data = append(data, bytes.Repeat([]byte{0xa0 + byte(i)}, 300)...)
		pad()
	}
	video := atom("trak", atom("mdia", atom("minf", atom("stbl",
		atom("stts", u32(append([]uint32{0, uint32(len(stts) / 2)}, stts...)...)),
		atom("stsc", u32(0, 1, 1, 1, 1)),
		atom("stsz", append(u32(0, 0, n), vsz...)),
		atom("co64", append(u32(0, n), voff...))))))
	sound := atom("trak", atom("mdia", atom("minf", atom("stbl",
		atom("stsc", u32(0, 1, 1, 1, 1)),
		atom("stsz", u32(0, 1, n)),
		atom("co64", append(u32(0, n), aoff...))))))
	moov := atom("moov", video, sound)
	be.PutUint32(head[0:], 1)
	copy(head[4:], "mdat")
	be.PutUint64(head[8:], uint64(4096+len(data)))
	file := append(append(append(head, make([]byte, 4096-16)...), data...), moov...)
	path := filepath.Join(t.TempDir(), "clip.braw")
	if err := os.WriteFile(path, file, 0o644); err != nil {
		t.Fatal(err)
	}
	return path, frames
}

func TestRepairDroppedFrames(t *testing.T) {
	// Five frames stored; the second is held for three frame times, the fourth for two.
	path, frames := gapClip(t, 1, 1001, 1, 3003, 1, 1001, 1, 2002, 1, 1001)
	c, err := OpenClip(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	want := []FrameGap{{Frame: 1, Missing: 2}, {Frame: 5, Missing: 1}}
	if !reflect.DeepEqual(c.Repaired, want) {
		t.Fatalf("gaps %v, want %v", c.Repaired, want)
	}
	from := []int{0, 1, 1, 1, 2, 3, 3, 4}
	if c.Frames() != len(from) {
		t.Fatalf("%d frames, want %d", c.Frames(), len(from))
	}
	for i, s := range from {
		fr, err := c.frame(i)
		if err != nil || !bytes.Equal(fr, frames[s]) {
			t.Fatalf("frame %d is not source frame %d (%v)", i, s, err)
		}
	}

	// Written out, the copies are stored and the clip reads back without gaps.
	chunks, err := c.otherChunks()
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "repaired.braw")
	w, err := newBrawWriter(dst, c.pre, c.moov, c.moovPos, chunks)
	if err != nil {
		t.Fatal(err)
	}
	defer w.abort()
	for i := 0; i < c.Frames(); i++ {
		fr, _ := c.frame(i)
		if err := w.frame(i, fr); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.finish(); err != nil {
		t.Fatal(err)
	}
	r, err := OpenClip(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if len(r.Repaired) != 0 || r.Frames() != len(from) {
		t.Fatalf("written clip: %d frames, gaps %v", r.Frames(), r.Repaired)
	}
	got, _ := r.otherChunks()
	if len(got) != len(chunks) {
		t.Fatalf("written clip has %d sound chunks, want %d", len(got), len(chunks))
	}
	for i := range got {
		if !bytes.Equal(bytes.TrimRight(got[i], "\x00"), bytes.TrimRight(chunks[i], "\x00")) {
			t.Fatalf("sound chunk %d changed", i)
		}
	}
	tr := r.tracks[0]
	if e := be.Uint32(r.moov[tr.stts+12:]); e != 1 || be.Uint32(r.moov[tr.stts+16:]) != 8 || be.Uint32(r.moov[tr.stts+20:]) != 1001 {
		t.Fatalf("written time table is not 8 frames of 1001")
	}
}

func TestRepairLeavesOthersAlone(t *testing.T) {
	for name, stts := range map[string][]uint32{
		"no gaps":          {4, 1001},
		"uneven durations": {2, 1001, 1, 1500, 1, 1001},
		"zero duration":    {2, 1001, 1, 0, 1, 1001},
		"implausible gap":  {3, 1, 1, 1000},
	} {
		path, _ := gapClip(t, stts...)
		raw, _ := os.ReadFile(path)
		moov := raw[be.Uint64(raw[8:16]):]
		out, gaps := repairMoov(moov)
		if gaps != nil || !bytes.Equal(out, moov) {
			t.Errorf("%s: index was changed", name)
		}
	}
}
