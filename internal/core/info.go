package core

import "math"

// ClipInfo describes a .braw clip.
type ClipInfo struct {
	Frames         int
	Width, Height  int // size of the stored brightness plane
	PicW, PicH     int // output picture size, excluding padding (may be scaled from the sensor)
	FpsNum, FpsDen int
	Size           int64
	VideoBytes     int64
	// Ratio is the clip's compression ratio in Blackmagic's terms: the unprocessed size of an
	// output RAW frame (12 bits per output pixel) divided by the average stored frame size.
	Ratio float64
	// RecordingRatio is the camera's nominal recording setting (for example "4:1" or "Q0").
	// Unlike Ratio it is not a measurement, and remains the original setting after recompression.
	RecordingRatio string
	// QScale and Table0 are the quantisation fields of the first, middle and last frame.
	QScale [3][3]int
	Table  [3][8]int
}

// rawFrameBytes is the unprocessed size of one output RAW frame. A scaled RGBW recording uses
// its output dimensions, not sensor_area_captured or the codec's packed plane dimensions.
func rawFrameBytes(w, h int) float64 { return float64(w) * float64(h) * 12 / 8 }

// infoAtoms visits complete immediate children; optional descriptive metadata must never make
// an otherwise readable clip panic. The clip's structural tables are checked by OpenClip.
func infoAtoms(b []byte, visit func(typ string, body []byte)) {
	for len(b) >= 8 {
		n, head := uint64(be.Uint32(b)), 8
		if n == 1 {
			if len(b) < 16 {
				return
			}
			n, head = be.Uint64(b[8:]), 16
		} else if n == 0 {
			n = uint64(len(b))
		}
		if n < uint64(head) || n > uint64(len(b)) {
			return
		}
		visit(string(b[4:8]), b[head:int(n)])
		b = b[int(n):]
	}
}

func infoChild(b []byte, typ string) (out []byte) {
	infoAtoms(b, func(t string, body []byte) {
		if t == typ && out == nil {
			out = body
		}
	})
	return out
}

type clipMetadataValue struct {
	typ  uint32
	data []byte
}

// clipMetadata resolves the keyed QuickTime metadata table, rather than searching for key
// names in arbitrary payloads (which can include an embedded LUT or other binary content).
func (c *Clip) clipMetadata() map[string]clipMetadataValue {
	root := infoChild(c.moov, "moov")
	meta := infoChild(root, "meta")
	if meta == nil {
		meta = infoChild(infoChild(root, "udta"), "meta")
	}
	keys := infoChild(meta, "keys")
	if keys == nil && len(meta) >= 4 {
		meta = meta[4:] // ISO FullBox form; camera clips also use the QuickTime form.
		keys = infoChild(meta, "keys")
	}
	if len(keys) < 8 {
		return nil
	}
	names := make(map[uint32]string)
	i, count := uint32(0), be.Uint32(keys[4:])
	infoAtoms(keys[8:], func(namespace string, name []byte) {
		i++
		if i <= count && namespace == "mdta" {
			names[i] = string(name)
		}
	})
	values := make(map[string]clipMetadataValue)
	infoAtoms(infoChild(meta, "ilst"), func(index string, item []byte) {
		name := names[be.Uint32([]byte(index))]
		data := infoChild(item, "data")
		if name != "" && len(data) >= 8 {
			values[name] = clipMetadataValue{be.Uint32(data), data[8:]}
		}
	})
	return values
}

func metadataSize(v clipMetadataValue) (int, int) {
	if v.typ != 0x47 || len(v.data) != 8 { // QuickTime float32 point/size pair
		return 0, 0
	}
	w, h := float64(math.Float32frombits(be.Uint32(v.data))), float64(math.Float32frombits(be.Uint32(v.data[4:])))
	if w < 1 || h < 1 || w > 65535 || h > 65535 || math.Trunc(w) != w || math.Trunc(h) != h {
		return 0, 0
	}
	return int(w), int(h)
}

// pictureSize reads the output dimensions, which can differ substantially from both the
// captured sensor area and the packed transform planes in scaled RGBW recordings.
func (c *Clip) pictureSize(planeW, planeH int) (int, int) {
	w, h := planeW, planeH
	infoAtoms(infoChild(c.moov, "moov"), func(typ string, track []byte) {
		if typ != "trak" {
			return
		}
		stsd := infoChild(infoChild(infoChild(infoChild(track, "mdia"), "minf"), "stbl"), "stsd")
		if len(stsd) < 8 {
			return
		}
		infoAtoms(stsd[8:], func(codec string, entry []byte) {
			if (codec != "brvm" && codec != "brxr" && codec != "brvo") || len(entry) < 28 {
				return
			}
			x, y := int(be.Uint16(entry[24:])), int(be.Uint16(entry[26:]))
			if x > 0 && y > 0 && (codec == "brxr" || (x > planeW-64 && x <= planeW && y > planeH-64 && y <= planeH)) {
				w, h = x, y
			}
		})
	})
	if x, y := metadataSize(c.clipMetadata()["crop_size"]); x > 0 && y > 0 && x > w-64 && x <= w && y > h-64 && y <= h {
		return x, y
	}
	return w, h
}

// Info reads a clip's basic facts without decoding its picture.
func Info(path string) (ci *ClipInfo, err error) {
	defer rescue(&err)
	clip, err := OpenClip(path)
	if err != nil {
		return nil, err
	}
	defer clip.Close()
	n := clip.Frames()
	ci = &ClipInfo{Frames: n, Size: clip.Size, VideoBytes: clip.VideoBytes()}
	for k, i := range []int{0, n / 2, n - 1} {
		fr, err := clip.frame(i)
		if err != nil {
			return nil, err
		}
		fi, err := parseFrame(fr)
		if err != nil {
			return nil, err
		}
		ci.Width, ci.Height = fi.w, fi.h
		ci.QScale[k] = [3]int{fi.qscale0, fi.qscale1, fi.qscale2}
		copy(ci.Table[k][:], fi.table[:8])
	}
	if ci.FpsNum, ci.FpsDen, _, err = clip.SensorRate(); err != nil {
		return nil, err
	}
	ci.PicW, ci.PicH = clip.pictureSize(ci.Width, ci.Height)
	ci.Ratio = rawFrameBytes(ci.PicW, ci.PicH) * float64(n) / float64(ci.VideoBytes)
	if v := clip.clipMetadata()["braw_compression_ratio"]; v.typ == 1 {
		ci.RecordingRatio = string(v.data)
	}
	return ci, nil
}
