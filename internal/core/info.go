package core

import "bytes"

// ClipInfo describes a .braw clip.
type ClipInfo struct {
	Frames         int
	Width, Height  int // size of the stored brightness plane
	PicW, PicH     int // recorded sensor area
	FpsNum, FpsDen int
	Size           int64
	VideoBytes     int64
	// Ratio is the clip's compression ratio in Blackmagic's terms: the unprocessed size of a
	// sensor frame (12 bits per photosite) divided by the average stored frame size.
	Ratio float64
	// QScale and Table0 are the quantisation fields of the first, middle and last frame.
	QScale [3][3]int
	Table  [3][8]int
}

// rawFrameBytes is the unprocessed size of one sensor frame.
func rawFrameBytes(w, h int) float64 { return float64(w) * float64(h) * 12 / 8 }

// pictureSize returns the clip's picture size as declared in its index (the sensor area that was
// recorded). The stored planes are a little larger because they are padded to whole blocks.
func (c *Clip) pictureSize(planeW, planeH int) (int, int) {
	if i := bytes.Index(c.moov, []byte("brvm")); i >= 0 && i+32 <= len(c.moov) {
		w, h := int(be.Uint16(c.moov[i+28:])), int(be.Uint16(c.moov[i+30:]))
		if w > planeW-64 && w <= planeW && h > planeH-64 && h <= planeH {
			return w, h
		}
	}
	return planeW, planeH
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
	return ci, nil
}
