package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/laurentchicoine/braw-pack/internal/core"
)

// App is the object the interface calls. Long work runs in the background and reports through
// events: "progress" while a file is being processed or estimated (stage "estimate"), "done" when
// a file ends, "idle" when the queue is empty again.
type App struct {
	ctx context.Context

	mu     sync.Mutex
	cancel context.CancelFunc // non-nil while a queue is running
}

func (a *App) startup(ctx context.Context) { a.ctx = ctx }

// Item is one file in a list.
type Item struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Folder string `json:"folder"`
	Size   int64  `json:"size"`
	Error  string `json:"error,omitempty"`

	// Clips
	Frames int     `json:"frames,omitempty"`
	Width  int     `json:"width,omitempty"`
	Height int     `json:"height,omitempty"`
	Fps    float64 `json:"fps,omitempty"`
	Ratio  float64 `json:"ratio,omitempty"` // compression ratio against unprocessed sensor data

	// Archives
	SourceSize   int64                `json:"sourceSize,omitempty"`
	Quality      int                  `json:"quality,omitempty"`
	Encoder      string               `json:"encoder,omitempty"`
	Intra        bool                 `json:"intra,omitempty"`
	RebuildQuant float64              `json:"rebuildQuant,omitempty"`
	Rebuild      []core.RebuildOption `json:"rebuild,omitempty"`
	AutoSize     int64                `json:"autoSize,omitempty"`    // predicted size at the recommended setting
	SourceQSize  int64                `json:"sourceQSize,omitempty"` // predicted size with the source's own tables
}

// Tools reports whether ffmpeg and the encoders are usable.
func (a *App) Tools() core.Tools { return core.CheckTools("") }

// PickFiles opens a file dialog. kind is "clip" or "archive".
func (a *App) PickFiles(kind string) ([]string, error) {
	f := runtime.FileFilter{DisplayName: "Blackmagic RAW (*.braw)", Pattern: "*.braw"}
	if kind == "archive" {
		f = runtime.FileFilter{DisplayName: "braw-pack archives (*.braw.mkv)", Pattern: "*.mkv"}
	}
	return runtime.OpenMultipleFilesDialog(a.ctx, runtime.OpenDialogOptions{Title: "Add files", Filters: []runtime.FileFilter{f}})
}

// PickFolder opens a folder dialog, starting in startDir when that folder still exists.
func (a *App) PickFolder(title, startDir string) (string, error) {
	o := runtime.OpenDialogOptions{Title: title, CanCreateDirectories: true}
	if st, err := os.Stat(startDir); startDir != "" && err == nil && st.IsDir() {
		o.DefaultDirectory = startDir
	}
	return runtime.OpenDirectoryDialog(a.ctx, o)
}

func suffixFor(kind string) string {
	if kind == "archive" {
		return core.ArchiveSuffix
	}
	return ".braw"
}

// scanned reports how far Scan has got through the "scan" event. total is -1 while folders are
// still being searched and the number of files is not known yet.
func (a *App) scanned(n, total int) {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "scan", map[string]any{"done": n, "total": total})
}

// Scan expands files and folders (searched recursively) into list items of the given kind.
// Reading each file takes a moment, so progress is reported through "scan" events.
func (a *App) Scan(paths []string, kind string) []Item {
	a.scanned(0, -1)
	suffix := suffixFor(kind)
	seen := map[string]bool{}
	var files []string
	add := func(p string) {
		if strings.HasSuffix(strings.ToLower(p), suffix) && !seen[p] {
			seen[p] = true
			files = append(files, p)
		}
	}
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if !st.IsDir() {
			add(p)
			continue
		}
		filepath.WalkDir(p, func(q string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				add(q)
			}
			return nil
		})
	}
	sort.Strings(files)
	items := make([]Item, len(files))
	a.scanned(0, len(files))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	var mu sync.Mutex
	var read int
	var last time.Time
	for i, p := range files {
		wg.Add(1)
		go func(i int, p string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			items[i] = describe(p, kind)
			mu.Lock()
			defer mu.Unlock()
			read++
			if read == len(files) || time.Since(last) >= 100*time.Millisecond {
				last = time.Now()
				a.scanned(read, len(files))
			}
		}(i, p)
	}
	wg.Wait()
	return items
}

func describe(p, kind string) Item {
	it := Item{Path: p, Name: filepath.Base(p), Folder: filepath.Dir(p)}
	if st, err := os.Stat(p); err == nil {
		it.Size = st.Size()
	}
	if kind == "archive" {
		m, err := core.ArchiveMeta("", p)
		if err != nil {
			it.Error = "not a braw-pack archive"
			return it
		}
		it.Frames, it.Width, it.Height = m.Frames, m.Width, m.Height
		if m.FpsDen > 0 {
			it.Fps = float64(m.FpsNum) / float64(m.FpsDen)
		}
		it.SourceSize, it.Quality, it.Encoder, it.Intra = m.SourceSize, m.Quality, m.Encoder, m.Intra
		it.RebuildQuant, it.Rebuild = m.RebuildQuant, m.Rebuild
		it.AutoSize, _ = m.RebuiltSize(0)
		it.SourceQSize, _ = m.RebuiltSize(1)
		return it
	}
	ci, err := core.Info(p)
	if err != nil {
		// The row already shows the file; keep only the reason.
		it.Error = "Cannot be read: " + strings.TrimPrefix(err.Error(), p+": ")
		return it
	}
	it.Frames, it.Width, it.Height, it.Ratio = ci.Frames, ci.PicW, ci.PicH, ci.Ratio
	if ci.FpsDen > 0 {
		it.Fps = float64(ci.FpsNum) / float64(ci.FpsDen)
	}
	return it
}

// Settings are the compression choices made in the interface.
type Settings struct {
	Encoder string `json:"encoder"` // nvenc | x265
	Quality int    `json:"quality"`
	Speed   string `json:"speed"` // fast | balanced | slow
	Intra   bool   `json:"intra"`
	OutDir  string `json:"outDir"` // where results are written; the interface always asks for it
	// Output is "archive" (a .braw.mkv, the default) or "braw" (a smaller .braw at Ratio:1).
	Output string  `json:"output"`
	Ratio  float64 `json:"ratio"`
	// CheckAll compares every frame of a new archive with its source. Without it only a few
	// sample frames are compared, which is faster.
	CheckAll bool `json:"checkAll"`
	// LUT also writes a viewing LUT beside each archive.
	LUT bool `json:"lut"`
	// Replace lets results take the place of files that already exist. Without it those are skipped.
	Replace bool `json:"replace"`
}

// EstimateRow mirrors core.EstimateRow with JSON names.
type EstimateRow struct {
	Quality      int     `json:"quality"`
	ArchiveBytes int64   `json:"archiveBytes"`
	ArchiveRatio float64 `json:"archiveRatio"`
	DetailKept   float64 `json:"detailKept"` // percent of the original's fine detail kept
	RebuiltBytes int64   `json:"rebuiltBytes"`
	RebuiltRatio float64 `json:"rebuiltRatio"`
}

// EstimateResult is what Estimate returns to the interface.
type EstimateResult struct {
	Path        string        `json:"path"`
	SourceBytes int64         `json:"sourceBytes"`
	SourceRatio float64       `json:"sourceRatio"`
	Sampled     int           `json:"sampled"`
	Frames      int           `json:"frames"`
	Rows        []EstimateRow `json:"rows"`
}

// Estimate predicts the outcome of compressing one clip at several quality levels.
func (a *App) Estimate(path string, s Settings) (*EstimateResult, error) {
	ctx, release, err := a.begin()
	if err != nil {
		return nil, err
	}
	defer release()
	e, err := core.Estimate(core.EstimateOptions{Ctx: ctx, Src: path, Speed: s.Speed, Encoder: s.Encoder, Intra: s.Intra,
		Progress: a.progress(path, stepsEstimate)})
	if err != nil {
		return nil, err
	}
	out := &EstimateResult{Path: path, SourceBytes: e.SourceBytes, SourceRatio: e.SourceRatio, Sampled: e.SampleFrames, Frames: e.Frames}
	for _, r := range e.Rows {
		out.Rows = append(out.Rows, EstimateRow{r.Quality, r.ArchiveBytes, r.ArchiveRatio, r.DetailKept, r.RebuiltBytes, r.RebuiltRatio})
	}
	return out, nil
}

// ShrinkRow is one compression ratio a clip can be re-saved at.
type ShrinkRow struct {
	Ratio      float64 `json:"ratio"`
	Bytes      int64   `json:"bytes"`
	DetailKept float64 `json:"detailKept"`
}

// ShrinkPlan is what ShrinkEstimate returns to the interface.
type ShrinkPlan struct {
	Path        string      `json:"path"`
	SourceBytes int64       `json:"sourceBytes"`
	SourceRatio float64     `json:"sourceRatio"`
	MaxRatio    float64     `json:"maxRatio"`
	Rows        []ShrinkRow `json:"rows"`
}

// ShrinkRatios lists the compression ratios the interface offers.
func (a *App) ShrinkRatios() []float64 { return core.ShrinkRatios }

// ShrinkEstimate measures what one clip becomes when re-saved as Blackmagic RAW at each ratio.
func (a *App) ShrinkEstimate(path string) (*ShrinkPlan, error) {
	ctx, release, err := a.begin()
	if err != nil {
		return nil, err
	}
	defer release()
	p, err := core.ShrinkEstimate(ctx, path, nil, a.progress(path, stepsEstimate))
	if err != nil {
		return nil, err
	}
	out := &ShrinkPlan{Path: path, SourceBytes: p.SourceBytes, SourceRatio: p.SourceRatio, MaxRatio: p.MaxRatio}
	for _, o := range p.Options {
		out.Rows = append(out.Rows, ShrinkRow{o.Target, o.Bytes, o.DetailKept})
	}
	return out, nil
}

// begin reserves the single work slot.
func (a *App) begin() (context.Context, func(), error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		return nil, nil, fmt.Errorf("something is already running")
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.cancel = cancel
	return ctx, func() {
		a.mu.Lock()
		a.cancel = nil
		a.mu.Unlock()
		cancel()
	}, nil
}

// Cancel stops whatever is running. The file in progress is abandoned and its partial output removed.
func (a *App) Cancel() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		a.cancel()
	}
}

func outPath(src, outDir, name string) (string, error) {
	dir := filepath.Dir(src)
	if outDir != "" {
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return "", err
		}
		dir = outDir
	}
	return filepath.Join(dir, name), nil
}

// done is the payload of the "done" event.
type done struct {
	Path    string `json:"path"`
	Status  string `json:"status"` // ok | skipped | failed | canceled
	Message string `json:"message"`
	Output  string `json:"output,omitempty"`
	Size    int64  `json:"size,omitempty"`
	Seconds int    `json:"seconds"`
}

// step is one stage of a job and its usual share of the job's time.
type step struct {
	name   string
	weight float64
}

// Usual shares of the time, measured on a two-minute 120 fps clip (NVIDIA encoder) and on a
// short clip (CPU encoder). They only shape the progress bar.
var (
	stepsPackGPU     = []step{{"analyse", 3}, {"encode", 43}, {"verify", 47}, {"finish", 7}}
	stepsPackGPUSpot = []step{{"analyse", 4}, {"encode", 61}, {"verify", 26}, {"finish", 9}}
	stepsPackCPU     = []step{{"analyse", 1}, {"encode", 90}, {"verify", 8}, {"finish", 1}}
	stepsPackCPUSpot = []step{{"analyse", 1}, {"encode", 95}, {"verify", 3}, {"finish", 1}}
	stepsShrink      = []step{{"analyse", 4}, {"encode", 94}, {"finish", 2}}
	stepsUnpack      = []step{{"rebuild", 1}}
	stepsEstimate    = []step{{"estimate", 1}}
)

// progress reports a job's stages as one fraction of the whole job.
func (a *App) progress(path string, steps []step) core.Progress {
	var last time.Time
	stage := ""
	var sum float64
	for _, st := range steps {
		sum += st.weight
	}
	return func(s string, n, total int) {
		if s == stage && n != total && time.Since(last) < 200*time.Millisecond {
			return
		}
		stage, last = s, time.Now()
		if total < 1 {
			total = 1
		}
		var before, weight float64
		for _, st := range steps {
			if st.name == s {
				weight = st.weight
				break
			}
			before += st.weight
		}
		f := (before + weight*float64(n)/float64(total)) / sum
		runtime.EventsEmit(a.ctx, "progress", map[string]any{"path": path, "stage": s, "fraction": f})
	}
}

// run processes paths one after another with fn and reports each outcome.
func (a *App) run(paths []string, fn func(ctx context.Context, src string) done) error {
	ctx, release, err := a.begin()
	if err != nil {
		return err
	}
	go func() {
		defer func() {
			release()
			runtime.EventsEmit(a.ctx, "idle")
		}()
		for _, src := range paths {
			if ctx.Err() != nil {
				runtime.EventsEmit(a.ctx, "done", done{Path: src, Status: "canceled", Message: "Canceled"})
				continue
			}
			t0 := time.Now()
			d := fn(ctx, src)
			d.Path, d.Seconds = src, int(time.Since(t0).Seconds())
			runtime.EventsEmit(a.ctx, "done", d)
		}
	}()
	return nil
}

func failure(err error) done {
	if err == core.ErrCanceled {
		return done{Status: "canceled", Message: "Canceled"}
	}
	return done{Status: "failed", Message: err.Error()}
}

// target is where the result for src goes. kind is "archive", "braw" (a smaller clip) or "clip"
// (a clip rebuilt from an archive).
func target(src, outDir, kind string) (string, error) {
	name := filepath.Base(src)
	switch kind {
	case "archive":
		name = strings.TrimSuffix(name, filepath.Ext(name)) + core.ArchiveSuffix
	case "braw":
		name = strings.TrimSuffix(name, filepath.Ext(name)) + ".braw"
	default:
		if strings.HasSuffix(strings.ToLower(name), core.ArchiveSuffix) {
			name = name[:len(name)-len(core.ArchiveSuffix)]
		} else {
			name = strings.TrimSuffix(name, filepath.Ext(name))
		}
		name += ".braw"
	}
	return outPath(src, outDir, name)
}

// sameFile reports whether two paths name one existing file.
func sameFile(a, b string) bool {
	x, err := os.Stat(a)
	if err != nil {
		return false
	}
	y, err := os.Stat(b)
	return err == nil && os.SameFile(x, y)
}

// Existing returns the sources whose result already exists in outDir, so the interface can ask
// whether to replace or skip them. A result that would be the source itself is never listed.
func (a *App) Existing(paths []string, outDir, kind string) []string {
	out := []string{}
	for _, src := range paths {
		dst, err := target(src, outDir, kind)
		if err != nil || sameFile(src, dst) {
			continue
		}
		if _, err := os.Stat(dst); err == nil {
			out = append(out, src)
		}
	}
	return out
}

// Compress starts packing the given clips. Sources are never modified or deleted.
func (a *App) Compress(paths []string, s Settings) error {
	return a.run(paths, func(ctx context.Context, src string) done {
		if s.Output == "braw" {
			return a.shrink(ctx, src, s)
		}
		dst, err := target(src, s.OutDir, "archive")
		if err != nil {
			return failure(err)
		}
		if _, err := os.Stat(dst); err == nil && !s.Replace {
			return done{Status: "skipped", Message: "Archive already exists", Output: dst}
		}
		steps := stepsPackGPU
		switch {
		case s.Encoder == "x265" && s.CheckAll:
			steps = stepsPackCPU
		case s.Encoder == "x265":
			steps = stepsPackCPUSpot
		case !s.CheckAll:
			steps = stepsPackGPUSpot
		}
		if s.LUT {
			// Fitting the LUT takes about as long whatever the clip's length.
			steps = append(append([]step{}, steps...), step{"lut", 15})
		}
		res, err := core.Pack(core.PackOptions{Ctx: ctx, Src: src, Dst: dst, Quality: s.Quality, Speed: s.Speed,
			Encoder: s.Encoder, Intra: s.Intra, Verify: s.CheckAll, LUT: s.LUT, Replace: s.Replace, Progress: a.progress(src, steps)})
		if err != nil {
			return failure(err)
		}
		checked := "every frame checked"
		if !s.CheckAll {
			checked = "sample frames checked"
		}
		msg := fmt.Sprintf("%.1f%% of the original size, %.1f%% of detail kept, %s",
			100*float64(res.ArchiveSize)/float64(res.SourceSize), res.Verify.DetailKept, checked)
		if res.LUT != nil {
			msg += ", LUT written"
		} else if res.LUTError != "" {
			msg += ". No LUT: " + res.LUTError
		}
		return done{Status: "ok", Message: msg, Output: dst, Size: res.ArchiveSize}
	})
}

// shrink re-saves one clip as a smaller .braw.
func (a *App) shrink(ctx context.Context, src string, s Settings) done {
	// The smaller clip keeps the original's name, so it can only go to another folder.
	if s.OutDir == "" {
		return done{Status: "skipped", Message: "No folder chosen"}
	}
	dst, err := target(src, s.OutDir, "braw")
	if err != nil {
		return failure(err)
	}
	if sameFile(src, dst) {
		return done{Status: "skipped", Message: "Not saved: the chosen folder is the one holding this clip"}
	}
	if _, err := os.Stat(dst); err == nil && !s.Replace {
		return done{Status: "skipped", Message: "A clip with this name already exists there", Output: dst}
	}
	if ci, err := core.Info(src); err == nil && s.Ratio < ci.Ratio*core.ShrinkMargin {
		return done{Status: "skipped", Message: fmt.Sprintf("Already at %.1f:1, left as it is", ci.Ratio)}
	}
	res, err := core.Shrink(core.ShrinkOptions{Ctx: ctx, Src: src, Dst: dst, Ratio: s.Ratio, Replace: s.Replace, Progress: a.progress(src, stepsShrink)})
	if err != nil {
		return failure(err)
	}
	msg := fmt.Sprintf("%.1f:1, %.1f%% of the original size, %.1f%% of detail kept",
		res.Ratio, 100*float64(res.Size)/float64(res.SourceSize), res.DetailKept)
	return done{Status: "ok", Message: msg, Output: dst, Size: res.Size}
}

// Decompress starts rebuilding clips from the given archives. sourceTables forces the original
// clip's own quantisation instead of the setting measured when the archive was made. replace lets
// rebuilt clips take the place of existing files; without it those are skipped.
func (a *App) Decompress(paths []string, outDir string, sourceTables, replace bool) error {
	return a.run(paths, func(ctx context.Context, src string) done {
		dst, err := target(src, outDir, "clip")
		if err != nil {
			return failure(err)
		}
		if _, err := os.Stat(dst); err == nil && !replace {
			return done{Status: "skipped", Message: "Clip already exists", Output: dst}
		}
		var quant float64
		if sourceTables {
			quant = 1
		}
		res, err := core.Unpack(core.UnpackOptions{Ctx: ctx, Src: src, Dst: dst, Quant: quant, Replace: replace, Progress: a.progress(src, stepsUnpack)})
		if err != nil {
			return failure(err)
		}
		return done{Status: "ok", Message: "Rebuilt", Output: dst, Size: res.Size}
	})
}

// Reveal shows a file in the system's file manager.
func (a *App) Reveal(path string) { reveal(path) }
