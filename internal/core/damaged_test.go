package core

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// atom wraps a body in a QuickTime atom of the given type.
func atom(typ string, body ...[]byte) []byte {
	out := make([]byte, 8)
	copy(out[4:], typ)
	for _, b := range body {
		out = append(out, b...)
	}
	be.PutUint32(out, uint32(len(out)))
	return out
}

// TestDamagedClips feeds broken files to the entry points that read clips: each must come back
// with an error, never a panic, so a bad file in a batch is skipped and the rest carry on.
func TestDamagedClips(t *testing.T) {
	head := func(mdat int) []byte {
		h := make([]byte, 16+mdat)
		be.PutUint32(h[0:], 8)
		copy(h[4:], "wide")
		be.PutUint32(h[8:], uint32(8+mdat))
		copy(h[12:], "mdat")
		return h
	}
	u32 := func(v ...uint32) []byte {
		b := make([]byte, 4*len(v))
		for i, x := range v {
			be.PutUint32(b[4*i:], x)
		}
		return b
	}
	stbl := func(stsz, co64, stsc []byte) []byte {
		return atom("moov", atom("trak", atom("mdia", atom("minf", atom("stbl",
			atom("stsz", stsz), atom("co64", co64), atom("stsc", stsc))))))
	}
	cases := map[string][]byte{
		"empty":             {},
		"not a clip":        []byte("this is not a Blackmagic RAW clip at all, just text"),
		"table before trak": append(head(8), atom("moov", atom("stsz", make([]byte, 8)))...),
		"no tracks":         append(head(8), atom("moov")...),
		"short tables":      append(head(8), stbl(nil, nil, nil)...),
		"huge counts":       append(head(8), stbl(u32(0, 0, 0xffffffff), u32(0, 0xffffffff), u32(0, 0xffffffff))...),
		"no frames":         append(head(8), stbl(u32(0, 0, 0), u32(0, 0), u32(0, 0))...),
		"frame past the end": append(head(8), stbl(u32(0, 0, 1, 0x100000), append(u32(0, 1), 0, 0, 0, 0, 0, 0, 0, 16),
			u32(0, 1, 1, 1, 1))...),
		"garbage frame": append(head(0x2000), stbl(u32(0, 0, 1, 0x2000), append(u32(0, 1), 0, 0, 0, 0, 0, 0, 0, 16),
			u32(0, 1, 1, 1, 1))...),
	}
	dir := t.TempDir()
	for name, data := range cases {
		path := filepath.Join(dir, "clip.braw")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Info(path); err == nil {
			t.Errorf("%s: Info accepted the file", name)
		}
		if _, err := ShrinkEstimate(nil, path, nil, nil); err == nil {
			t.Errorf("%s: ShrinkEstimate accepted the file", name)
		}
		if _, err := Shrink(ShrinkOptions{Src: path, Dst: filepath.Join(dir, "out.braw"), Ratio: 12}); err == nil {
			t.Errorf("%s: Shrink accepted the file", name)
		}
	}
}

// TestDamagedFrames gives the frame decoders random bytes behind a believable header.
func TestDamagedFrames(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 2000; i++ {
		fr := make([]byte, 0x1300+rng.Intn(0x2000))
		rng.Read(fr)
		be.PutUint32(fr, 0x100)
		copy(fr[4:], "bmdf")
		copy(fr[0x100:], "braw")
		fr[0x108] = 2
		if i%2 == 0 {
			// A layout that passes the header checks, so the tile data itself is what is wrong.
			fr[0x10a], fr[0x10b] = 1, 1
			be.PutUint16(fr[0x10c:], 64)
			be.PutUint16(fr[0x10e:], 32)
			be.PutUint16(fr[0x110:], 32)
			be.PutUint16(fr[0x140:], 64)
			be.PutUint32(fr[0x180:], 0x1200)
		}
		decodePlanes(fr, 1, planesAll)
		requantTilt(fr, 1, 4, 0)
	}
}

func TestDamagedRestoreData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restore.bin")
	rd := &restoreData{Meta: Meta{Format: FormatVersion, Frames: 1 << 40, Width: 1 << 30, Height: 1 << 30}}
	if err := rd.write(path); err != nil {
		t.Fatal(err)
	}
	if _, err := readRestoreData(path); err == nil {
		t.Error("impossible restore data was accepted")
	}
}
