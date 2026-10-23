// braw-lab: measurement helpers used while developing braw-pack. Not part of the released tool.
package main

import (
	"fmt"
	"os"
	"runtime/pprof"
	"strconv"
	"strings"
	"time"

	"github.com/laurentchicoine/braw-pack/internal/core"
)

func main() {
	if len(os.Args) == 3 && os.Args[1] == "info" {
		ci, err := core.Info(os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Printf("%+v\n", *ci)
		return
	}
	if len(os.Args) >= 3 && os.Args[1] == "scales" {
		for _, intra := range []bool{false, true} {
			if err := core.LabScales(os.Args[2], []int{6, 10, 16, 20, 26}, intra); err != nil {
				fmt.Println(err)
			}
		}
		return
	}
	if len(os.Args) == 3 && os.Args[1] == "requant" {
		if err := core.LabRequant(os.Args[2], []float64{1, 1.5, 2, 2.5, 3, 3.5, 4, 5, 6, 8, 10, 12, 16, 24, 32, 48, 64, 128}, true); err != nil {
			fmt.Println(err)
		}
		return
	}
	if len(os.Args) == 7 && os.Args[1] == "lutcheck" {
		cs, _ := strconv.ParseFloat(os.Args[3], 64)
		bits, _ := strconv.Atoi(os.Args[4])
		frame, _ := strconv.Atoi(os.Args[5])
		if err := core.LabLUTCheck(os.Args[2], cs, bits, frame, os.Args[6]); err != nil {
			fmt.Println(err)
		}
		return
	}
	if len(os.Args) >= 5 && os.Args[1] == "crop" {
		// braw-lab crop <clip.braw> <nvenc|x265> <quality,...> [WxH] [intra]
		var qs []int
		for _, f := range strings.Split(os.Args[4], ",") {
			q, _ := strconv.Atoi(f)
			qs = append(qs, q)
		}
		cw, ch, intra := 4096, 2304, false
		for _, a := range os.Args[5:] {
			if a == "intra" {
				intra = true
			} else {
				fmt.Sscanf(a, "%dx%d", &cw, &ch)
			}
		}
		if err := core.LabCrop(os.Args[2], os.Args[3], "balanced", qs, cw, ch, intra); err != nil {
			fmt.Println(err)
		}
		return
	}
	if len(os.Args) == 4 && os.Args[1] == "asquality" {
		fmt.Println(core.AsQuality(os.Args[2], os.Args[3], 8))
		return
	}
	if len(os.Args) == 6 && os.Args[1] == "planes" {
		a, _ := strconv.Atoi(os.Args[3])
		c, _ := strconv.Atoi(os.Args[4])
		w, h, err := core.DumpPlanes(os.Args[2], a, c, os.Args[5])
		fmt.Println(w, h, err)
		return
	}
	if len(os.Args) >= 4 && os.Args[1] == "verify" {
		if len(os.Args) > 4 {
			f, _ := os.Create(os.Args[4])
			pprof.StartCPUProfile(f)
			defer pprof.StopCPUProfile()
		}
		t := time.Now()
		v, err := core.Verify(os.Args[2], os.Args[3], "", nil)
		if err != nil {
			fmt.Println(err)
			return
		}
		s := time.Since(t).Seconds()
		fmt.Printf("%d frames in %.1f s (%.0f frames/s), mean %.2f dB, min %.2f dB at frame %d\n",
			v.Frames, s, float64(v.Frames)/s, v.MeanPSNR, v.MinPSNR, v.MinFrame)
		return
	}
	if len(os.Args) < 4 || os.Args[1] != "bench" {
		fmt.Println("braw-lab bench <clip.braw> <frames> [cpu.prof]")
		fmt.Println("braw-lab verify <clip.braw> <clip.braw.mkv> [cpu.prof]")
		os.Exit(2)
	}
	n, _ := strconv.Atoi(os.Args[3])
	prof := ""
	if len(os.Args) > 4 {
		prof = os.Args[4]
	}
	if err := core.Bench(os.Args[2], n, prof); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
