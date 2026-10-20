package core

import "testing"

// Detection is driven by coding metadata. Crop/aspect variants do not require
// another camera-name or output-resolution special case.
func TestV5ScaledGeometryVariants(t *testing.T) {
	for _, tc := range []struct{ layout, rawW, rawH, w, h, size int }{
		{0x0a14, 12336, 4032, 4112, 2688, 4},
		{0x0a14, 12336, 2568, 4112, 1712, 4},
		{0x0914, 12336, 4032, 8224, 5376, 8},
		{0x0914, 12336, 2568, 8224, 3424, 8},
		{0x0914, 11568, 3264, 7712, 4352, 8},
	} {
		fr := make([]byte, 0x140)
		be.PutUint16(fr[0x112:], uint16(tc.layout))
		fi := &frameInfo{hs: 0x100, w: tc.rawW, h: tc.rawH, tileH: tc.rawH, ntw: 1, nth: 1, tileW: []int{tc.rawW}, tileX: []int{0}}
		got, err := parseFrameV5Scaled(fr, fi)
		if err != nil {
			t.Fatal(err)
		}
		if got.w != tc.w || got.h != tc.h || got.scaledSize() != tc.size {
			t.Fatalf("layout%x: %+v", tc.layout, got)
		}
		_, _, mw, mh := got.v5ScaledTile(0, 0)
		if mw*2*tc.size != got.w || mh*tc.size != got.h {
			t.Fatal("macroblocks do not cover scaled picture")
		}
	}
}
