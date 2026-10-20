package core

import (
	"bytes"
	"testing"
)

func TestV5RejectsMissingTileRows(t *testing.T) {
	fr := make([]byte, 0x1600)
	be.PutUint32(fr, 0x100)
	copy(fr[4:], "bmdf")
	copy(fr[0x100:], "braw")
	fr[0x108], fr[0x10a] = 5, 1
	be.PutUint16(fr[0x10c:], 24)
	be.PutUint16(fr[0x10e:], 12)
	be.PutUint16(fr[0x110:], 12)
	be.PutUint16(fr[0x112:], 0x0a14)
	be.PutUint16(fr[0x140:], 24)
	if _, err := parseFrame(fr); err == nil {
		t.Fatal("accepted a frame with zero tile rows")
	}
}

func TestV5ScaledRejectsUnusedStreamData(t *testing.T) {
	fi := &frameInfo{v5Scaled: true, ntw: 1, nth: 1, offs: []int{0, 1, 2, 3, 4}}
	if _, err := fi.v5Readers([]byte{0, 0, 0, 0}, 0, 0); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []int{2, 3} {
		fr := make([]byte, 4)
		fr[stream] = 1
		if _, err := fi.v5Readers(fr, 0, 0); err == nil {
			t.Fatalf("ignored nonzero data in stream %d", stream)
		}
	}
}

func TestScaleQuantV5ShortMetadata(t *testing.T) {
	// The 4K camera uses the same metadata-header length as old Bayer clips.
	// Version 5 still changes qscale0; it must never modify a nonexistent table.
	hdr := make([]byte, 0x1500)
	be.PutUint32(hdr, 0x100)
	copy(hdr[0x100:], "braw")
	hdr[0x108], hdr[0x109] = 5, 8
	want := append([]byte(nil), hdr...)
	want[0x109] = byte(v5QFor(8, 2))
	if actual := scaleQuant(hdr, 2); !bytes.Equal(actual, want) {
		t.Fatal("version 5 scaling changed more than qscale0")
	}
}
