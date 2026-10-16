package core

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// FormatVersion is the version of the restore data embedded in an archive.
const FormatVersion = 1

// AttachmentName is the Matroska attachment that carries the restore data.
const AttachmentName = "braw-pack-restore.bin"

const restoreMagic = "BRAWPACK\x01\n"

// Meta describes an archive. It is stored as JSON at the start of the restore data.
type Meta struct {
	Format       int     `json:"format"`
	Tool         string  `json:"tool"`
	SourceName   string  `json:"source_name"`
	SourceSize   int64   `json:"source_size"`
	SourceSHA256 string  `json:"source_sha256"`
	Frames       int     `json:"frames"`
	Width        int     `json:"width"`
	Height       int     `json:"height"`
	ChromaScale  float64 `json:"chroma_scale"`
	FpsNum       int     `json:"fps_num"`
	FpsDen       int     `json:"fps_den"`
	Encoder      string  `json:"encoder"`
	Quality      int     `json:"quality"`
	Speed        string  `json:"speed"`

	// Bits is the sample depth of the stored picture (10 or 12). It is 0 in the first archives,
	// whose samples went through ffmpeg's own depth conversion.
	Bits int `json:"bits,omitempty"`
	// Intra is set when every frame was coded on its own.
	Intra bool `json:"intra,omitempty"`
	// SourceVideoBytes is the size of the picture data in the source clip.
	SourceVideoBytes int64 `json:"source_video_bytes,omitempty"`
	// ArchivePSNR is how close the stored picture is to the source (brightness plane, dB).
	ArchivePSNR float64 `json:"archive_psnr,omitempty"`
	// DetailKept is the share of the source's fine picture detail the archive keeps, in percent,
	// measured on sample frames in a way that mostly ignores sensor noise. 0 = not measured.
	DetailKept float64 `json:"detail_kept,omitempty"`
	// RebuildQuant is the quantisation scale unpack uses by default: 1 rebuilds with the source's
	// own tables, larger values with coarser tables matched to the detail the archive really holds.
	// 0 means it was not measured (treated as 1).
	RebuildQuant float64 `json:"rebuild_quant,omitempty"`
	// Rebuild lists the settings measured at pack time, finest first.
	Rebuild []RebuildOption `json:"rebuild_options,omitempty"`
}

// RebuildOption is one measured way of rebuilding the clip from its archive.
type RebuildOption struct {
	Quant float64 `json:"quant"` // scale applied to the source's quantisation tables
	Size  float64 `json:"size"`  // rebuilt picture size as a fraction of the source's
	Y     float64 `json:"y"`     // rebuilt vs source, brightness plane, dB
	C     float64 `json:"c"`     // rebuilt vs source, colour planes, dB
}

// planar is the planar layout matching the depth of this archive's picture.
func (m *Meta) planar() pixLayout {
	if m.Bits == 10 {
		return planar10
	}
	return planar12
}

// gpuLayout is the layout exchanged with the NVIDIA encoder and decoder, which only handle the
// 10-bit picture.
func (m *Meta) gpuLayout() pixLayout {
	if m.Bits == 10 {
		return semi10
	}
	return planar12
}

// restoreData is everything besides the picture that is needed to rebuild the .braw.
type restoreData struct {
	Meta    Meta
	Pre     []byte   // bytes before the first chunk
	Moov    []byte   // original index, patched on restore
	Chunks  [][]byte // audio, timecode, gyro, accelerometer chunks in file order
	Sidecar []byte   // contents of the clip's .sidecar file, if any
	Hdrs    [][]byte // per frame: bytes up to the first tile
	Side    [][]byte // per frame: per-macroblock side bytes
}

func writeBlob(w io.Writer, b []byte) error {
	var l [8]byte
	binary.BigEndian.PutUint64(l[:], uint64(len(b)))
	if _, err := w.Write(l[:]); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

func readBlob(r io.Reader, max uint64) ([]byte, error) {
	var l [8]byte
	if _, err := io.ReadFull(r, l[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint64(l[:])
	if n > max {
		return nil, fmt.Errorf("restore data: section of %d bytes is implausible", n)
	}
	if n <= 1<<24 {
		b := make([]byte, n)
		_, err := io.ReadFull(r, b)
		return b, err
	}
	// A large section grows as its bytes arrive, so a damaged length cannot ask for memory the
	// file does not back.
	var buf bytes.Buffer
	if _, err := io.CopyN(&buf, r, int64(n)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// rescue turns a panic into an error. The parsers check what they read, but a file damaged in a
// way nobody foresaw must still fail on its own instead of ending the program.
func rescue(err *error) {
	if r := recover(); r != nil {
		*err = fmt.Errorf("unreadable or damaged file (%v)", r)
	}
}

func xorPrev(cur, prev []byte) []byte {
	if len(prev) != len(cur) {
		return cur
	}
	out := make([]byte, len(cur))
	for i := range cur {
		out[i] = cur[i] ^ prev[i]
	}
	return out
}

// write stores the restore data. Frame headers are XORed with the previous frame's header
// (they are nearly constant), then everything is gzip-compressed.
func (d *restoreData) write(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(restoreMagic); err != nil {
		return err
	}
	bw := bufio.NewWriterSize(f, 1<<20)
	zw, _ := gzip.NewWriterLevel(bw, restoreLevel)
	meta, err := json.Marshal(d.Meta)
	if err != nil {
		return err
	}
	blobs := [][]byte{meta, d.Pre, d.Moov, d.Sidecar}
	for _, b := range blobs {
		if err := writeBlob(zw, b); err != nil {
			return err
		}
	}
	var cnt [8]byte
	binary.BigEndian.PutUint64(cnt[:], uint64(len(d.Chunks)))
	zw.Write(cnt[:])
	for _, c := range d.Chunks {
		if err := writeBlob(zw, c); err != nil {
			return err
		}
	}
	if len(d.Hdrs) != len(d.Side) {
		return fmt.Errorf("restore data: header/side count mismatch")
	}
	binary.BigEndian.PutUint64(cnt[:], uint64(len(d.Hdrs)))
	zw.Write(cnt[:])
	var prev []byte
	for i := range d.Hdrs {
		if err := writeBlob(zw, xorPrev(d.Hdrs[i], prev)); err != nil {
			return err
		}
		prev = d.Hdrs[i]
		if err := writeBlob(zw, d.Side[i]); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	return f.Sync()
}

func readRestoreData(path string) (*restoreData, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	magic := make([]byte, len(restoreMagic))
	if _, err := io.ReadFull(f, magic); err != nil || string(magic) != restoreMagic {
		return nil, fmt.Errorf("not braw-pack restore data (or a newer, unsupported version)")
	}
	zr, err := gzip.NewReader(bufio.NewReaderSize(f, 1<<20))
	if err != nil {
		return nil, err
	}
	const maxBlob = 1 << 34
	d := &restoreData{}
	meta, err := readBlob(zr, 1<<20)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(meta, &d.Meta); err != nil {
		return nil, err
	}
	if d.Meta.Format != FormatVersion {
		return nil, fmt.Errorf("restore data format %d is not supported by this version", d.Meta.Format)
	}
	if m := &d.Meta; m.Frames < 0 || m.SourceSize < 0 || m.Width < 16 || m.Width > 65535 || m.Width%16 != 0 ||
		m.Height < 8 || m.Height > 65535 || m.Height%8 != 0 || !(m.ChromaScale > 0 && m.ChromaScale <= 1) ||
		m.FpsNum <= 0 || m.FpsDen <= 0 {
		return nil, fmt.Errorf("restore data describes an impossible clip")
	}
	for _, dst := range []*[]byte{&d.Pre, &d.Moov, &d.Sidecar} {
		if *dst, err = readBlob(zr, maxBlob); err != nil {
			return nil, err
		}
	}
	var cnt [8]byte
	if _, err := io.ReadFull(zr, cnt[:]); err != nil {
		return nil, err
	}
	for i, n := uint64(0), binary.BigEndian.Uint64(cnt[:]); i < n; i++ {
		b, err := readBlob(zr, maxBlob)
		if err != nil {
			return nil, err
		}
		d.Chunks = append(d.Chunks, b)
	}
	if _, err := io.ReadFull(zr, cnt[:]); err != nil {
		return nil, err
	}
	var prev []byte
	for i, n := uint64(0), binary.BigEndian.Uint64(cnt[:]); i < n; i++ {
		h, err := readBlob(zr, 1<<24)
		if err != nil {
			return nil, err
		}
		h = xorPrev(h, prev)
		prev = h
		s, err := readBlob(zr, 1<<26)
		if err != nil {
			return nil, err
		}
		d.Hdrs = append(d.Hdrs, h)
		d.Side = append(d.Side, s)
	}
	if len(d.Hdrs) != d.Meta.Frames {
		return nil, fmt.Errorf("restore data holds %d frames, expected %d", len(d.Hdrs), d.Meta.Frames)
	}
	return d, nil
}

// restoreLevel is the gzip level of the restore data. The strongest level took 45 times longer
// on a two-minute clip (46 s against 1 s) to save 8% of the restore data.
const restoreLevel = gzip.DefaultCompression
