package core

import "testing"

func TestCheckTools(t *testing.T) {
	tl := CheckTools("")
	t.Logf("%+v", tl)
	if tl.FFmpeg == "" {
		t.Skip("no ffmpeg on this machine")
	}
}

func TestNVENCReason(t *testing.T) {
	// What the bundled ffmpeg prints when a picture is wider than the card can encode.
	msg := "encoder stopped: write |1: The pipe is being closed.: [hevc_nvenc @ 0000020b4ccea440] No capable devices found\n" +
		"[vost#0:0/hevc_nvenc @ 0000020b4cbcdd40] Error while opening encoder - maybe incorrect parameters such as bit_rate, rate, width or height."
	if got := nvencReason(msg); got != "No capable devices found" {
		t.Errorf("got %q", got)
	}
	if got := nvencReason("ffmpeg encode failed: exit status 1: \nsomething else"); got != "ffmpeg encode failed: exit status 1:" {
		t.Errorf("got %q", got)
	}
}
