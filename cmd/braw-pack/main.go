// braw-pack: store Blackmagic RAW clips as compact H.265 archives that can be rebuilt into .braw.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/laurentchicoine/braw-pack/internal/core"
)

const usage = `braw-pack ` + core.Version + `

  braw-pack pack   [options] <clip.braw | folder>      make archives (name.braw.mkv)
  braw-pack unpack [options] <clip.braw.mkv | folder>  rebuild .braw files from archives
  braw-pack verify [options] <clip.braw> <clip.braw.mkv>
  braw-pack estimate [options] <clip.braw>             predict sizes for several quality levels
  braw-pack shrink [options] <clip.braw | folder>      re-save as smaller .braw files at a stronger ratio

Run "braw-pack <command> -h" for the options of a command.
The source files are never modified or deleted.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "pack":
		err = cmdPack(os.Args[2:])
	case "unpack":
		err = cmdUnpack(os.Args[2:])
	case "verify":
		err = cmdVerify(os.Args[2:])
	case "estimate":
		err = cmdEstimate(os.Args[2:])
	case "shrink":
		err = cmdShrink(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Print(usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// progress prints one updating line per stage.
func progress() core.Progress {
	last := time.Time{}
	stage := ""
	var began time.Time
	return func(s string, done, total int) {
		if s != stage {
			if stage != "" {
				fmt.Println()
			}
			stage = s
			last = time.Time{}
			began = time.Now()
		}
		if done == total || time.Since(last) > 500*time.Millisecond {
			last = time.Now()
			fmt.Printf("\r  %-8s %3d%%  %4.0fs", s, 100*done/max(total, 1), time.Since(began).Seconds())
		}
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// inputs expands a file or folder argument into the files ending in suffix.
func inputs(arg, suffix string) ([]string, error) {
	st, err := os.Stat(arg)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return []string{arg}, nil
	}
	var out []string
	err = filepath.WalkDir(arg, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(strings.ToLower(p), suffix) {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

func outPath(in, outDir, name string) (string, error) {
	dir := filepath.Dir(in)
	if outDir != "" {
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return "", err
		}
		dir = outDir
	}
	return filepath.Join(dir, name), nil
}

func cmdPack(args []string) error {
	fs := flag.NewFlagSet("pack", flag.ExitOnError)
	out := fs.String("o", "", "output folder (default: next to each source)")
	quality := fs.Int("quality", core.DefaultQuality, "constant quality level; lower = better and larger")
	speed := fs.String("speed", "balanced", "fast | balanced | slow (slower = smaller file)")
	encoder := fs.String("encoder", "nvenc", "nvenc (NVIDIA GPU, 10-bit) | x265 (CPU, 12-bit)")
	ffmpeg := fs.String("ffmpeg", "", "path to ffmpeg (default: the one beside this program, else on the PATH)")
	intra := fs.Bool("intra", false, "code every frame on its own: much larger, but the quality level holds for every frame")
	lut := fs.Bool("lut", false, "also write a viewing LUT (name.braw.cube) beside each archive; needs Blackmagic RAW installed")
	spot := fs.Bool("spot-check", false, "compare only a few sample frames with the source instead of every frame (faster)")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("pack needs one clip or folder")
	}
	files, err := inputs(fs.Arg(0), ".braw")
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no .braw files found")
	}
	failed := 0
	for _, src := range files {
		base := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
		dst, err := outPath(src, *out, base+core.ArchiveSuffix)
		if err != nil {
			return err
		}
		fmt.Println(src)
		if _, err := os.Stat(dst); err == nil {
			fmt.Println("  skipped: archive already exists")
			continue
		}
		t0 := time.Now()
		res, err := core.Pack(core.PackOptions{Src: src, Dst: dst, Quality: *quality, Speed: *speed,
			Encoder: *encoder, Intra: *intra, FFmpeg: *ffmpeg, Verify: !*spot, LUT: *lut, Progress: progress()})
		fmt.Println()
		if err != nil {
			fmt.Fprintln(os.Stderr, "  FAILED:", err)
			failed++
			continue
		}
		fmt.Printf("  %s -> %s (%.1f%%), %d frames at %d/%d fps, colour scale %.3f, %.0fs\n",
			size(res.SourceSize), size(res.ArchiveSize), 100*float64(res.ArchiveSize)/float64(res.SourceSize),
			res.Frames, res.FpsNum, res.FpsDen, res.ChromaScale, time.Since(t0).Seconds())
		if v := res.Verify; v != nil {
			how := "every frame compared with the source"
			if v.Compared < v.Frames {
				how = fmt.Sprintf("%d sample frames compared with the source", v.Compared)
			}
			fmt.Printf("  checked: %s; picture keeps %.1f%% of the fine detail\n", how, v.DetailKept)
			printRebuild(res.Meta)
		}
		if res.LUT != nil {
			fmt.Printf("  viewing LUT: %s (%.1f dB from Blackmagic's picture on held-out frames; %.0f%% of its colours were seen in the clip)\n",
				res.LUT.Path, res.LUT.Accuracy, 100*res.LUT.Measured)
		} else if res.LUTError != "" {
			fmt.Fprintln(os.Stderr, "  no viewing LUT:", res.LUTError)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d clips failed", failed, len(files))
	}
	return nil
}

func cmdUnpack(args []string) error {
	fs := flag.NewFlagSet("unpack", flag.ExitOnError)
	out := fs.String("o", "", "output folder (default: next to each archive)")
	ffmpeg := fs.String("ffmpeg", "", "path to ffmpeg (default: the one beside this program, else on the PATH)")
	quantArg := fs.String("quant", "auto", "auto (matched to the detail the archive holds) | source (the original clip's own tables) | a scale such as 2")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("unpack needs one archive or folder")
	}
	var quant float64
	switch *quantArg {
	case "auto":
	case "source":
		quant = 1
	default:
		var err error
		if quant, err = strconv.ParseFloat(*quantArg, 64); err != nil || quant <= 0 {
			return fmt.Errorf("-quant must be auto, source or a positive number")
		}
	}
	files, err := inputs(fs.Arg(0), core.ArchiveSuffix)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no %s files found", core.ArchiveSuffix)
	}
	failed := 0
	for _, src := range files {
		name := filepath.Base(src)
		if strings.HasSuffix(strings.ToLower(name), core.ArchiveSuffix) {
			name = name[:len(name)-len(core.ArchiveSuffix)]
		} else {
			name = strings.TrimSuffix(name, filepath.Ext(name))
		}
		dst, err := outPath(src, *out, name+".braw")
		if err != nil {
			return err
		}
		fmt.Println(src)
		if _, err := os.Stat(dst); err == nil {
			fmt.Println("  skipped: " + dst + " already exists")
			continue
		}
		t0 := time.Now()
		r, err := core.Unpack(core.UnpackOptions{Src: src, Dst: dst, FFmpeg: *ffmpeg, Quant: quant, Progress: progress()})
		fmt.Println()
		if err != nil {
			fmt.Fprintln(os.Stderr, "  FAILED:", err)
			failed++
			continue
		}
		fmt.Printf("  rebuilt %s: %s (original was %s), %d frames, quantisation x%g, %.0fs\n", dst, size(r.Size),
			size(r.Meta.SourceSize), r.Meta.Frames, r.Quant, time.Since(t0).Seconds())
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d archives failed", failed, len(files))
	}
	return nil
}

func cmdVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	ffmpeg := fs.String("ffmpeg", "", "path to ffmpeg (default: the one beside this program, else on the PATH)")
	fs.Parse(args)
	if fs.NArg() != 2 {
		return fmt.Errorf("verify needs the original clip and its archive")
	}
	v, err := core.Verify(fs.Arg(0), fs.Arg(1), *ffmpeg, progress())
	fmt.Println()
	if err != nil {
		return err
	}
	fmt.Printf("OK: %d frames, restore data matches the source; picture keeps %.1f%% of the fine detail\n", v.Frames, v.DetailKept)
	printRebuild(core.Meta{RebuildQuant: v.RebuildQuant, Rebuild: v.Rebuild})
	return nil
}

// printRebuild lists the rebuild settings measured for an archive and marks the default.
func printRebuild(m core.Meta) {
	if len(m.Rebuild) == 0 {
		return
	}
	fmt.Println("  rebuild options (size and closeness to the source in dB, measured on sample frames):")
	for _, o := range m.Rebuild {
		mark := " "
		if o.Quant == m.RebuildQuant {
			mark = "*"
		}
		fmt.Printf("   %s x%-5g %5.1f%% of the original picture size, %.2f dB brightness, %.2f dB colour\n",
			mark, o.Quant, 100*o.Size, o.Y, o.C)
	}
	fmt.Println("    * = what unpack uses unless told otherwise (-quant)")
}

func cmdEstimate(args []string) error {
	fs := flag.NewFlagSet("estimate", flag.ExitOnError)
	speed := fs.String("speed", "balanced", "fast | balanced | slow")
	encoder := fs.String("encoder", "nvenc", "nvenc | x265")
	ffmpeg := fs.String("ffmpeg", "", "path to ffmpeg (default: the one beside this program, else on the PATH)")
	list := fs.String("qualities", "", "comma-separated quality levels to try (default 4,6,8,10,12,14,16)")
	intra := fs.Bool("intra", false, "estimate for archives made with -intra")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("estimate needs one clip")
	}
	var qualities []int
	for _, f := range strings.Split(*list, ",") {
		if f = strings.TrimSpace(f); f != "" {
			q, err := strconv.Atoi(f)
			if err != nil {
				return fmt.Errorf("bad quality %q", f)
			}
			qualities = append(qualities, q)
		}
	}
	e, err := core.Estimate(core.EstimateOptions{Src: fs.Arg(0), Qualities: qualities, Speed: *speed,
		Encoder: *encoder, Intra: *intra, FFmpeg: *ffmpeg, Progress: progress()})
	fmt.Println()
	if err != nil {
		return err
	}
	fmt.Printf("%s: %s, %dx%d, recorded at %s\n", fs.Arg(0), size(e.SourceBytes), e.PicW, e.PicH, ratio(e.SourceRatio))
	fmt.Printf("estimated from %d of %d frames; ratios are against the unprocessed sensor data\n", e.SampleFrames, e.Frames)
	fmt.Println("detail kept = share of the original's fine picture detail still in the archive; sensor noise is mostly left out of it")
	fmt.Println("  quality   archive size          detail kept   unpacks to")
	for _, r := range e.Rows {
		fmt.Printf("  %7d   %9s  %5.1f%%  %7s   %10.1f%%   %9s  %7s BRAW\n", r.Quality, size(r.ArchiveBytes),
			100*float64(r.ArchiveBytes)/float64(e.SourceBytes), ratio(r.ArchiveRatio), r.DetailKept, size(r.RebuiltBytes), ratio(r.RebuiltRatio))
	}
	return nil
}

func cmdShrink(args []string) error {
	fs := flag.NewFlagSet("shrink", flag.ExitOnError)
	out := fs.String("o", "", "output folder; clips keep their names, so it must not be the folder they are in")
	target := fs.Float64("ratio", 0, "compression ratio to reach, for example 12 for 12:1; without it, the choices for the clip are listed")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("shrink needs one clip or folder")
	}
	files, err := inputs(fs.Arg(0), ".braw")
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no .braw files found")
	}
	failed := 0
	for _, src := range files {
		fmt.Println(src)
		if *target == 0 {
			p, err := core.ShrinkEstimate(nil, src, nil, nil)
			if err != nil {
				fmt.Fprintln(os.Stderr, "  FAILED:", err)
				failed++
				continue
			}
			fmt.Printf("  %s, %dx%d, recorded at %s; as Blackmagic RAW it can go to about %s\n",
				size(p.SourceBytes), p.PicW, p.PicH, ratio(p.SourceRatio), ratio(p.MaxRatio))
			fmt.Println("    ratio        size          detail kept")
			for _, o := range p.Options {
				fmt.Printf("  %7s   %9s  %5.1f%%   %8.1f%%\n", ratio(o.Target), size(o.Bytes),
					100*float64(o.Bytes)/float64(p.SourceBytes), o.DetailKept)
			}
			continue
		}
		if *out == "" {
			return fmt.Errorf("shrink keeps file names: give another folder with -o")
		}
		dst, err := outPath(src, *out, filepath.Base(src))
		if err != nil {
			return err
		}
		if a, err := os.Stat(src); err == nil {
			if b, err := os.Stat(dst); err == nil && os.SameFile(a, b) {
				fmt.Println("  skipped: the output folder is the one holding this clip")
				continue
			}
		}
		if _, err := os.Stat(dst); err == nil {
			fmt.Println("  skipped: " + dst + " already exists")
			continue
		}
		t0 := time.Now()
		r, err := core.Shrink(core.ShrinkOptions{Src: src, Dst: dst, Ratio: *target, Progress: progress()})
		fmt.Println()
		if err != nil {
			fmt.Fprintln(os.Stderr, "  FAILED:", err)
			failed++
			continue
		}
		fmt.Printf("  %s (%s) -> %s (%s, %.1f%%), keeps %.1f%% of the fine detail, %d frames, %.0fs\n",
			size(r.SourceSize), ratio(r.SourceRatio), size(r.Size), ratio(r.Ratio),
			100*float64(r.Size)/float64(r.SourceSize), r.DetailKept, r.Frames, time.Since(t0).Seconds())
		fmt.Println("  ->", dst)
	}
	if failed > 0 {
		return fmt.Errorf("%d file(s) failed", failed)
	}
	return nil
}

// ratio formats a compression ratio the way cameras name them, e.g. 5.9:1.
func ratio(r float64) string {
	if r >= 20 {
		return fmt.Sprintf("%.0f:1", r)
	}
	return fmt.Sprintf("%.1f:1", r)
}

func size(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d bytes", n)
}
