package core

import "runtime"

// The hardware encoders. Both take and return 10-bit samples in the semi-planar layout (see
// Meta.gpuLayout), can store a second, detail layer, and are given up for x265 when they fail.
const (
	encNVENC        = "nvenc"        // NVIDIA cards (Windows, Linux)
	encVideoToolbox = "videotoolbox" // the media engine of Apple silicon Macs
)

// Hardware reports whether encoder is one of the hardware encoders.
func Hardware(encoder string) bool { return encoder == encNVENC || encoder == encVideoToolbox }

// hwCodec is ffmpeg's name for a hardware encoder.
func hwCodec(encoder string) string {
	if encoder == encVideoToolbox {
		return "hevc_videotoolbox"
	}
	return "hevc_nvenc"
}

// HardwareEncoder is the hardware encoder this kind of machine can have: Apple's on Macs, where
// there is no NVIDIA path, and NVIDIA's elsewhere.
func HardwareEncoder() string {
	if runtime.GOOS == "darwin" {
		return encVideoToolbox
	}
	return encNVENC
}

// hwDecode has ffmpeg decode on this kind of machine's hardware.
func hwDecode() []string {
	if runtime.GOOS == "darwin" {
		return []string{"-hwaccel", "videotoolbox"}
	}
	return []string{"-hwaccel", "cuda"}
}

// DefaultEncoder is the hardware encoder, except on Intel Macs: their HEVC hardware is not one
// braw-pack has been tried on, so they keep the CPU encoder unless asked.
func DefaultEncoder() string {
	if runtime.GOOS == "darwin" && runtime.GOARCH != "arm64" {
		return "x265"
	}
	return HardwareEncoder()
}
