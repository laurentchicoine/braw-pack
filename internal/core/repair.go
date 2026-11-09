package core

import (
	"fmt"
	"strings"
)

// Dropped frames. A camera that cannot write fast enough (seen with an SD card on a Pocket 4K)
// sometimes skips frames. It does not store anything for them: the frame before the gap is given
// a longer duration in the video track's time table (stts), so it is held over the missing frames.
// Sound and motion data keep running, and the clip keeps its full length. Blackmagic's decoder
// numbers frames by time, so it reports the full count but cannot read the frames in the gap, and
// editing software shows them as broken.
//
// The repair gives every frame the normal duration and fills each gap with copies of the frame
// before it, byte for byte, which is what the camera's own timing says should be shown there.
// Nothing else changes: the clip keeps its length, and picture, sound and motion data stay in step.

// FrameGap is a run of frames the camera dropped, filled with copies of the frame before it.
// Frame numbers are those of the repaired clip.
type FrameGap struct {
	Frame   int `json:"frame"`   // the frame that is repeated
	Missing int `json:"missing"` // how many copies follow it: frames Frame+1 to Frame+Missing
}

func (g FrameGap) String() string {
	if g.Missing == 1 {
		return fmt.Sprintf("1 dropped frame (%d) filled with a copy of frame %d", g.Frame+1, g.Frame)
	}
	return fmt.Sprintf("%d dropped frames (%d-%d) filled with copies of frame %d", g.Missing, g.Frame+1, g.Frame+g.Missing, g.Frame)
}

// DroppedFrames is the number of frames filled in by the repair.
func DroppedFrames(gaps []FrameGap) int {
	n := 0
	for _, g := range gaps {
		n += g.Missing
	}
	return n
}

// RepairSummary describes the repair in one sentence, or is empty when nothing was repaired.
func RepairSummary(gaps []FrameGap) string {
	if len(gaps) == 0 {
		return ""
	}
	parts := make([]string, len(gaps))
	for i, g := range gaps {
		parts[i] = g.String()
	}
	return "the camera dropped frames in this clip; repaired: " + strings.Join(parts, "; ")
}

// maxRepairGrowth bounds how many frames a repair may add, as a multiple of the frames stored:
// a time table claiming more than this is damaged, not a few dropped frames.
const maxRepairGrowth = 4

// repairMoov returns moov with the video track's dropped frames filled in, and the gaps it
// filled. A clip without gaps, or whose tables do not have the expected shape, comes back
// unchanged with no gaps: the repair is only made when its meaning is clear.
func repairMoov(moov []byte) ([]byte, []FrameGap) {
	tracks, err := parseMoov(moov)
	if err != nil || len(tracks) == 0 {
		return moov, nil
	}
	t := tracks[0]
	if t.stts == 0 || t.fixed != 0 || len(t.chunks) != t.n {
		return moov, nil
	}
	for _, s := range t.spc {
		if s != 1 {
			return moov, nil
		}
	}
	size := func(p int) int { return int(be.Uint32(moov[p:])) }
	p := t.stts
	if size(p) < 16 {
		return moov, nil
	}
	ne := int(be.Uint32(moov[p+12:]))
	if ne < 1 || size(p) < 16+8*ne {
		return moov, nil
	}
	counts, deltas := make([]int64, ne), make([]int64, ne)
	var total int64
	base, most := int64(0), int64(-1)
	for i := 0; i < ne; i++ {
		counts[i] = int64(be.Uint32(moov[p+16+8*i:]))
		deltas[i] = int64(be.Uint32(moov[p+20+8*i:]))
		total += counts[i]
		if counts[i] > most {
			base, most = deltas[i], counts[i]
		}
	}
	if total != int64(t.n) || base <= 0 {
		return moov, nil
	}
	// Every duration must be a whole number of normal frames.
	var frames int64
	for i := range deltas {
		if deltas[i] <= 0 || deltas[i]%base != 0 {
			return moov, nil
		}
		frames += counts[i] * (deltas[i] / base)
	}
	if frames == total || frames > total*maxRepairGrowth {
		return moov, nil
	}

	// Expand the frame list: a frame lasting k normal frames is stored k times.
	desc := chunkDescriptions(moov, t)
	if desc == nil {
		return moov, nil
	}
	sizes := make([]uint32, 0, frames)
	offs := make([]int64, 0, frames)
	descs := make([]uint32, 0, frames)
	var gaps []FrameGap
	s := 0
	for i := range counts {
		k := int(deltas[i] / base)
		for j := int64(0); j < counts[i]; j++ {
			if k > 1 {
				gaps = append(gaps, FrameGap{Frame: len(sizes), Missing: k - 1})
			}
			for c := 0; c < k; c++ {
				sizes = append(sizes, t.sizes[s])
				offs = append(offs, t.offs[s])
				descs = append(descs, desc[s])
			}
			s++
		}
	}

	stts := fullAtom("stts", 8)
	stts = be.AppendUint32(stts, 1)
	stts = be.AppendUint32(stts, uint32(len(sizes)))
	stts = be.AppendUint32(stts, uint32(base))
	stsz := fullAtom("stsz", 8+4*len(sizes))
	stsz = be.AppendUint32(stsz, 0)
	stsz = be.AppendUint32(stsz, uint32(len(sizes)))
	for _, v := range sizes {
		stsz = be.AppendUint32(stsz, v)
	}
	co64 := fullAtom("co64", 4+8*len(offs))
	co64 = be.AppendUint32(co64, uint32(len(offs)))
	for _, v := range offs {
		co64 = be.AppendUint64(co64, uint64(v))
	}
	// One sample per chunk; a new entry wherever the sample description changes.
	var runs [][2]uint32
	for c, d := range descs {
		if len(runs) == 0 || runs[len(runs)-1][1] != d {
			runs = append(runs, [2]uint32{uint32(c + 1), d})
		}
	}
	stsc := fullAtom("stsc", 4+12*len(runs))
	stsc = be.AppendUint32(stsc, uint32(len(runs)))
	for _, r := range runs {
		stsc = be.AppendUint32(stsc, r[0])
		stsc = be.AppendUint32(stsc, 1)
		stsc = be.AppendUint32(stsc, r[1])
	}
	for _, a := range [][]byte{stts, stsz, co64, stsc} {
		be.PutUint32(a, uint32(len(a)))
	}

	out := rebuildAtoms(moov, 0, len(moov), map[int][]byte{t.stts: stts, t.stsz: stsz, t.co64: co64, t.stsc: stsc})
	if _, err := parseMoov(out); err != nil {
		return moov, nil
	}
	return out, gaps
}

// fullAtom starts a version-0 full atom of type typ with room for n more bytes; the caller fills
// in the size once the body is written.
func fullAtom(typ string, n int) []byte {
	b := make([]byte, 12, 12+n)
	copy(b[4:], typ)
	return b
}

// chunkDescriptions is the sample description index of each chunk of t, from its stsc table.
func chunkDescriptions(moov []byte, t *track) []uint32 {
	p := t.stsc
	ne := int(be.Uint32(moov[p+12:]))
	nc := len(t.offs)
	out := make([]uint32, 0, nc)
	for i := 0; i < ne; i++ {
		first := int(be.Uint32(moov[p+16+12*i:]))
		d := be.Uint32(moov[p+24+12*i:])
		last := nc
		if i+1 < ne {
			last = int(be.Uint32(moov[p+16+12*(i+1):])) - 1
		}
		for c := first; c <= last && len(out) < nc; c++ {
			out = append(out, d)
		}
	}
	if len(out) != nc {
		return nil
	}
	return out
}

// rebuildAtoms copies the atoms in buf[start:end], putting the atom at each position in repl in
// place of the original and correcting the sizes of the containers around it.
func rebuildAtoms(buf []byte, start, end int, repl map[int][]byte) []byte {
	containers := map[string]bool{"moov": true, "trak": true, "mdia": true, "minf": true, "stbl": true}
	var out []byte
	for pos := start; pos+8 <= end; {
		size := int(be.Uint32(buf[pos:]))
		typ := string(buf[pos+4 : pos+8])
		switch {
		case repl[pos] != nil:
			out = append(out, repl[pos]...)
		case containers[typ]:
			body := rebuildAtoms(buf, pos+8, pos+size, repl)
			out = be.AppendUint32(out, uint32(8+len(body)))
			out = append(out, typ...)
			out = append(out, body...)
		default:
			out = append(out, buf[pos:pos+size]...)
		}
		pos += size
	}
	return out
}
