package core

import "runtime"

// DefaultEncoder selects the CPU encoder on Macs, which have no supported NVIDIA path.
func DefaultEncoder() string {
	if runtime.GOOS == "darwin" {
		return "x265"
	}
	return "nvenc"
}
