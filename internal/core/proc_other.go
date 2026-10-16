//go:build !windows

package core

import (
	"os"
	"os/exec"
)

// command is exec.Command for helper programs (see the Windows version for why it exists).
func command(name string, args ...string) *exec.Cmd { return exec.Command(name, args...) }

// framePipe is the pipe frames are exchanged with ffmpeg through (see the Windows version).
func framePipe() (r, w *os.File, err error) { return os.Pipe() }
