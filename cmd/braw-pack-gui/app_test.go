package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestScan needs real files: BRAW_TEST_DIR is a folder holding clips and archives.
func TestScan(t *testing.T) {
	dir := os.Getenv("BRAW_TEST_DIR")
	if dir == "" {
		t.Skip("BRAW_TEST_DIR not set")
	}
	a := &App{}
	for _, kind := range []string{"clip", "archive"} {
		items := a.Scan([]string{dir}, kind)
		if len(items) == 0 {
			t.Fatalf("no %s found", kind)
		}
		for _, it := range items {
			t.Logf("%s %+v", kind, it)
			if it.Error != "" {
				t.Errorf("%s: %s", it.Name, it.Error)
			}
		}
	}
}

func TestShrinkTargetSuffix(t *testing.T) {
	src := filepath.Join("in", "A001_C002.braw")
	cases := []struct{ outDir, suffix, want string }{
		{"out", "", filepath.Join("out", "A001_C002.braw")},
		{"out", "_12to1", filepath.Join("out", "A001_C002_12to1.braw")},
		{"", " small", filepath.Join("in", "A001_C002 small.braw")},
	}
	for _, c := range cases {
		got, err := target(src, filepath.Join(t.TempDir(), c.outDir), "braw", c.suffix)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Base(got) != filepath.Base(c.want) {
			t.Errorf("suffix %q: got %s, want %s", c.suffix, got, c.want)
		}
	}
	if got, _ := target(src, "", "braw", "_x"); got != filepath.Join("in", "A001_C002_x.braw") {
		t.Errorf("next to source: got %s", got)
	}
	// Archives ignore the suffix.
	if got, _ := target(src, "", "archive", "_x"); filepath.Base(got) != "A001_C002.braw.mkv" {
		t.Errorf("archive: got %s", got)
	}
	for _, bad := range []string{"a/b", `a\b`, "x:y", "a?b", "a\tb"} {
		if _, err := target(src, "", "braw", bad); err == nil {
			t.Errorf("suffix %q accepted", bad)
		}
	}
}
