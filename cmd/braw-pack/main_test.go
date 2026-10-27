package main

import (
	"strings"
	"testing"

	"github.com/laurentchicoine/braw-pack/internal/core"
)

func TestParseQuality(t *testing.T) {
	for in, want := range map[string]int{"auto": core.AutoQuality, "10": 10, "1": 1, "51": 51} {
		if got, err := parseQuality(in); err != nil || got != want {
			t.Errorf("%q: got %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"0", "52", "-3", "ten", ""} {
		if _, err := parseQuality(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestParseLayers(t *testing.T) {
	for in, want := range map[string]int{"auto": core.AutoLayers, "1": 1, "2": 2} {
		if got, err := parseLayers(in); err != nil || got != want {
			t.Errorf("%q: got %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"0", "3", "two", ""} {
		if _, err := parseLayers(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestLayersNote(t *testing.T) {
	for _, c := range []struct {
		layers  int
		auto    bool
		encoder string
		trial   float64
		want    string
	}{
		{2, true, "nvenc", 0, "two layers, automatic for RGBW recordings"},
		{2, true, "nvenc", 75.5, "one GPU layer kept only 75.5% of its fine detail in a trial (under 90%)"},
		{2, false, "nvenc", 0, "two layers, as asked"},
		{1, true, "x265", 0, "one layer: the CPU encoder"},
		{1, false, "nvenc", 0, "one layer, as asked"},
		{1, true, "nvenc", 0, "one layer, chosen for this clip"},
		{1, true, "nvenc", 96.3, "one GPU layer kept 96.3% of its fine detail in a trial"},
	} {
		if got := layersNote(c.layers, c.auto, c.encoder, c.trial); !strings.Contains(got, c.want) {
			t.Errorf("%+v: %q", c, got)
		}
	}
}
