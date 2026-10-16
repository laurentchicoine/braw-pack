package main

import (
	"os"
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
