package core

import (
	"math"
	"os"
	"testing"
)

func infoSizePair(w, h float32) []byte {
	b := make([]byte, 8)
	be.PutUint32(b, math.Float32bits(w))
	be.PutUint32(b[4:], math.Float32bits(h))
	return b
}

func infoTestMetadata(fullBox bool, crop []byte, ratio string) []byte {
	keys := make([]byte, 8)
	be.PutUint32(keys[4:], 3)
	keys = append(keys, atom("mdta", []byte("crop_size"))...)
	keys = append(keys, atom("mdta", []byte("sensor_area_captured"))...)
	keys = append(keys, atom("mdta", []byte("braw_compression_ratio"))...)
	value := func(i, typ uint32, data []byte) []byte {
		head, index := make([]byte, 8), make([]byte, 4)
		be.PutUint32(head, typ)
		be.PutUint32(index, i)
		return atom(string(index), atom("data", head, data))
	}
	ilst := atom("ilst", value(1, 0x47, crop), value(2, 0x47, infoSizePair(12288, 8040)), value(3, 1, []byte(ratio)))
	var prefix []byte
	if fullBox {
		prefix = make([]byte, 4)
	}
	return atom("meta", prefix, atom("keys", keys), ilst)
}

func infoTestTrack(codec string, w, h int) []byte {
	entry := make([]byte, 28)
	be.PutUint16(entry[24:], uint16(w))
	be.PutUint16(entry[26:], uint16(h))
	stsd := make([]byte, 8)
	be.PutUint32(stsd[4:], 1)
	return atom("trak", atom("mdia", atom("minf", atom("stbl", atom("stsd", stsd, atom(codec, entry))))))
}

// These dimensions exercise the distinction among RGBW packed planes, the padded sample
// description, the scaled picture, and the physical sensor. None is interchangeable.
func TestPictureSize(t *testing.T) {
	for _, tc := range []struct {
		name, codec                  string
		sw, sh, pw, ph, wantW, wantH int
		meta                         []byte
	}{
		{"legacy brvm", "brvm", 6144, 3456, 6144, 3456, 6144, 3456, nil},
		{"small plane fallback", "none", 0, 0, 32, 16, 32, 16, nil},
		{"12K brvm padding", "brvm", 12288, 8040, 12336, 8064, 12288, 8040, nil},
		{"17K brvo padding", "brvo", 17520, 8040, 17568, 8064, 17520, 8040, nil},
		{"17K brvo crop", "brvo", 17568, 8064, 17568, 8064, 17520, 8040, infoTestMetadata(false, infoSizePair(17520, 8040), "Q3")},
		{"reject unrelated brvm", "brvm", 6144, 3456, 4096, 2688, 4096, 2688, nil},
		{"4K brxr padding", "brxr", 4112, 2688, 12336, 4032, 4112, 2688, nil},
		{"4K crop", "brxr", 4112, 2688, 12336, 4032, 4096, 2680, infoTestMetadata(false, infoSizePair(4096, 2680), "4:1")},
		{"4K FullBox crop", "brxr", 4112, 2688, 12336, 4032, 4096, 2680, infoTestMetadata(true, infoSizePair(4096, 2680), "Q0")},
		{"reject sensor as crop", "brxr", 4112, 2688, 12336, 4032, 4112, 2688, infoTestMetadata(false, infoSizePair(12288, 8040), "4:1")},
		{"reject NaN crop", "brxr", 4112, 2688, 12336, 4032, 4112, 2688, infoTestMetadata(false, infoSizePair(float32(math.NaN()), 2680), "4:1")},
		{"reject truncated crop", "brxr", 4112, 2688, 12336, 4032, 4112, 2688, infoTestMetadata(false, []byte{1, 2}, "4:1")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Clip{moov: atom("moov", infoTestTrack(tc.codec, tc.sw, tc.sh), tc.meta)}
			if w, h := c.pictureSize(tc.pw, tc.ph); w != tc.wantW || h != tc.wantH {
				t.Fatalf("got %dx%d, want %dx%d", w, h, tc.wantW, tc.wantH)
			}
		})
	}
}

func TestClipMetadata(t *testing.T) {
	for _, fullBox := range []bool{false, true} {
		c := &Clip{moov: atom("moov", atom("udta", infoTestMetadata(fullBox, infoSizePair(4096, 2680), "Q0")))}
		values := c.clipMetadata()
		if v := values["braw_compression_ratio"]; v.typ != 1 || string(v.data) != "Q0" {
			t.Errorf("recording ratio: %+v", v)
		}
		if w, h := metadataSize(values["sensor_area_captured"]); w != 12288 || h != 8040 {
			t.Errorf("sensor area: %dx%d", w, h)
		}
	}
	// Strings that happen to look like atoms in arbitrary payloads must not supply dimensions.
	c := &Clip{moov: atom("moov", atom("free", infoTestTrack("brxr", 2, 2), infoTestMetadata(false, infoSizePair(2, 2), "99:1")))}
	if w, h := c.pictureSize(64, 32); w != 64 || h != 32 || len(c.clipMetadata()) != 0 {
		t.Fatal("accepted metadata from an unrelated payload")
	}
	for n := 0; n < len(c.moov); n++ {
		// Every truncation must be harmless, even when it falls inside an atom header.
		broken := &Clip{moov: c.moov[:n]}
		broken.pictureSize(64, 32)
	}
}

func TestInfoUrsa4K(t *testing.T) {
	path := os.Getenv("BRAW_URSA_4K_CLIP")
	if path == "" {
		t.Skip("BRAW_URSA_4K_CLIP not set")
	}
	ci, err := Info(path)
	if err != nil {
		t.Fatal(err)
	}
	if ci.PicW != 4096 || ci.PicH != 2680 || ci.RecordingRatio != "4:1" || ci.FpsNum != 50 || ci.FpsDen != 1 {
		t.Fatalf("unexpected sample metadata: %+v", ci)
	}
	if ci.Ratio < 3.9 || ci.Ratio > 4.1 {
		t.Errorf("measured ratio %.4f does not match this 4:1 sample", ci.Ratio)
	}
	t.Logf("%dx%d output, %dx%d packed planes, measured %.4f:1, recording setting %s", ci.PicW, ci.PicH, ci.Width, ci.Height, ci.Ratio, ci.RecordingRatio)
}
