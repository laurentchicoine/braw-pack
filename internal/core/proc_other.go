//go:build !windows

package core

import (
	"os"
	"os/exec"
	"runtime"
)

// command is exec.Command for helper programs (see the Windows version for why it exists).
func command(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	if runtime.GOOS == "darwin" {
		// ffmpeg takes each raw frame in a block of its own and frees it. macOS keeps freed large
		// blocks for reuse without giving their memory back: packing a 17K clip (425 MB a frame),
		// ffmpeg passed 24 GB, 11 of every 12 of them freed blocks. Without that cache it peaked
		// at 4.6 GB. A setting already in the environment is left as it is.
		if _, set := os.LookupEnv("MallocLargeCache"); !set {
			cmd.Env = append(os.Environ(), "MallocLargeCache=0")
		}
	}
	return cmd
}

// framePipe is the pipe frames are exchanged with ffmpeg through (see the Windows version).
func framePipe() (r, w *os.File, err error) { return os.Pipe() }
