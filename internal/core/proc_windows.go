package core

import (
	"os"
	"os/exec"
	"syscall"
)

// command is exec.Command for helper programs. On Windows it stops each ffmpeg run from opening a
// console window, which would otherwise flash on screen when braw-pack runs as a desktop app.
func command(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	return cmd
}

// framePipe is os.Pipe with a buffer large enough for whole frames. The default Windows pipe
// holds 4 KB, which makes every frame exchanged with ffmpeg thousands of blocking hand-overs.
func framePipe() (r, w *os.File, err error) {
	var rh, wh syscall.Handle
	if err := syscall.CreatePipe(&rh, &wh, nil, framePipeBytes); err != nil {
		return nil, nil, os.NewSyscallError("pipe", err)
	}
	return os.NewFile(uintptr(rh), "|0"), os.NewFile(uintptr(wh), "|1"), nil
}
