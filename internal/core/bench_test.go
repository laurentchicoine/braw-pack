package core

import (
	"os"
	"testing"
)

func benchFrame(b *testing.B) []byte {
	path := os.Getenv("BRAW_TEST_CLIP")
	if path == "" {
		b.Skip("BRAW_TEST_CLIP not set")
	}
	clip, err := OpenClip(path)
	if err != nil {
		b.Fatal(err)
	}
	defer clip.Close()
	fr, err := clip.frame(clip.Frames() / 2)
	if err != nil {
		b.Fatal(err)
	}
	return fr
}

func BenchmarkDecodePlanes(b *testing.B) {
	fr := benchFrame(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		decodePlanes(fr, 1, planesAll)
	}
}

func BenchmarkEncodePlanes(b *testing.B) {
	fr := benchFrame(b)
	fi, Y, U, V, side, _, _ := decodePlanes(fr, 1, planesAll)
	hdr := fr[:fi.offs[0]+fi.hs]
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		encodePlanes(hdr, side, Y, U, V, 1, 0)
	}
}
