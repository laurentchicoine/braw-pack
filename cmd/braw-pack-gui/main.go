// braw-pack-gui: desktop interface for braw-pack.
package main

import (
	"embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend
var assets embed.FS

//go:embed icon/icon.png
var iconPNG []byte

// dataFolder is the name of the folder the app keeps beside itself for the web view's data,
// which includes the folders last saved to.
const dataFolder = "braw-pack-data"

// dataDir returns where the web view may keep its data: a folder beside the program, so the app
// stays self-contained. When that place cannot be written (a read-only disc, a protected folder)
// a temporary folder is used instead and removed on exit, so nothing is remembered between runs.
func dataDir() (dir string, cleanup func()) {
	// Only WebView2 uses this directory. In particular, never write inside a signed Mac bundle.
	if goruntime.GOOS != "windows" {
		return "", func() {}
	}
	if exe, err := os.Executable(); err == nil {
		d := filepath.Join(filepath.Dir(exe), dataFolder)
		if os.MkdirAll(d, 0o755) == nil {
			probe := filepath.Join(d, ".write-test")
			if f, err := os.Create(probe); err == nil {
				f.Close()
				os.Remove(probe)
				return d, func() {}
			}
		}
	}
	d, err := os.MkdirTemp("", "braw-pack-")
	if err != nil {
		return "", func() {}
	}
	return d, func() { os.RemoveAll(d) }
}

func main() {
	app := &App{}
	data, cleanup := dataDir()
	defer cleanup()
	err := wails.Run(&options.App{
		Title:            "braw-pack",
		Width:            1080,
		Height:           760,
		MinWidth:         860,
		MinHeight:        600,
		AssetServer:      &assetserver.Options{Assets: assets},
		BackgroundColour: &options.RGBA{R: 20, G: 22, B: 26, A: 255},
		OnStartup:        app.startup,
		Bind:             []interface{}{app},
		Linux:            &linux.Options{Icon: iconPNG, ProgramName: "braw-pack"},
		Windows:          &windows.Options{WebviewUserDataPath: data},
		// Files dropped on the window are handed to the interface as paths; without this the
		// web view treats a dropped file as something to open or download.
		DragAndDrop: &options.DragAndDrop{EnableFileDrop: true, DisableWebViewDrop: true},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		cleanup()
		os.Exit(1)
	}
}

func reveal(path string) {
	switch goruntime.GOOS {
	case "windows":
		exec.Command("explorer", "/select,", filepath.Clean(path)).Start()
	case "darwin":
		exec.Command("open", "-R", path).Start()
	default:
		exec.Command("xdg-open", filepath.Dir(path)).Start()
	}
}
