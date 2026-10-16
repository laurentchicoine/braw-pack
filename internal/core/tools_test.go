package core

import "testing"

func TestCheckTools(t *testing.T) {
	tl := CheckTools("")
	t.Logf("%+v", tl)
	if tl.FFmpeg == "" {
		t.Skip("no ffmpeg on this machine")
	}
}
