// eval_replay.go — the eval-replay CLI subcommand (#639).
//
// Replays the offline activity scorer and the adaptive gate over a golden
// corpus of finished recordings so gate/scorer changes ship with a
// before/after table instead of a field gamble.
//
//	mibee-nvr eval-replay --corpus /path/to/corpus.json            # scorer replay
//	mibee-nvr eval-replay --corpus ... --gate                      # gate replay (default config)
//	mibee-nvr eval-replay --corpus ... --gate --videoexit=false    # candidate config vs default, side by side
//
// Corpus format (JSON array; paths absolute or relative to the manifest):
//
//	[{"path": "/mnt/data/nvr/cam-…/seg.mp4", "camera": "视通-9楼", "label": "rain"}]
//
// Labels are free-form but the framework convention is: rain / lowbitrate /
// static / active — see tools/eval/README.md.

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/motion"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
)

type corpusEntry struct {
	Path   string `json:"path"`
	Camera string `json:"camera"`
	Label  string `json:"label"`
}

func cmdEvalReplay() {
	fs := flag.NewFlagSet("eval-replay", flag.ExitOnError)
	corpusPath := fs.String("corpus", "", "path to the corpus manifest JSON (required)")
	gate := fs.Bool("gate", false, "replay the adaptive gate instead of the scorer")
	fps := fs.Float64("fps", 0, "frame rate for gate replay; 0 = derive from each file (frames/duration)")
	spike := fs.Float64("spike", 0, "candidate gate: spike_factor")
	floorBytes := fs.Float64("noisefloor-bytes", 0, "candidate gate: explicit noise_floor_bytes")
	autoNoise := fs.String("autonoise", "", "candidate gate: auto_noise_floor true|false")
	videoExit := fs.String("videoexit", "", "candidate gate: video_exit true|false")
	_ = fs.Parse(os.Args[2:])

	if *corpusPath == "" {
		fmt.Fprintln(os.Stderr, "usage: mibee-nvr eval-replay --corpus corpus.json [--gate] [candidate flags]")
		os.Exit(2)
	}
	raw, err := os.ReadFile(*corpusPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read corpus: %v\n", err)
		os.Exit(1)
	}
	var corpus []corpusEntry
	if err := json.Unmarshal(raw, &corpus); err != nil {
		fmt.Fprintf(os.Stderr, "parse corpus: %v\n", err)
		os.Exit(1)
	}
	baseDir := filepath.Dir(*corpusPath)

	candCfg := recorder.DefaultAdaptiveConfig()
	hasCandidate := *spike > 0 || *floorBytes > 0 || *autoNoise != "" || *videoExit != ""
	if *spike > 0 {
		candCfg.SpikeFactor = *spike
	}
	if *floorBytes > 0 {
		candCfg.NoiseFloorBytes = *floorBytes
	}
	if *autoNoise != "" {
		candCfg.AutoNoiseFloor = *autoNoise == "true"
	}
	if *videoExit != "" {
		candCfg.NoVideoExit = *videoExit == "false"
	}

	byLabel := map[string]*labelAgg{}
	fmt.Println()
	if !*gate {
		fmt.Printf("%-28s %-11s %7s %9s %6s %9s\n", "file", "label", "score", "medianP", "conf", "effective")
	} else {
		head := fmt.Sprintf("%-28s %-11s %7s %6s %8s", "file", "label", "TL%", "swch", "floorB")
		if hasCandidate {
			head += fmt.Sprintf(" | %7s %6s %8s", "TL%'", "swch'", "floorB'")
		}
		fmt.Println(head)
	}
	for _, e := range corpus {
		p := e.Path
		if !filepath.IsAbs(p) {
			p = filepath.Join(baseDir, p)
		}
		info, err := merge.ParseSegmentNoProbe(p)
		if err != nil {
			fmt.Printf("%-28s %-11s ERROR %v\n", filepath.Base(p), e.Label, err)
			continue
		}
		frames := make([]recorder.ReplayFrame, 0, len(info.Samples))
		for _, s := range info.Samples {
			frames = append(frames, recorder.ReplayFrame{Size: int(s.Size), IsKeyframe: s.IsKeyFrame})
		}
		name := filepath.Base(p)
		if len(name) > 28 {
			name = name[:25] + "…"
		}
		agg := byLabel[e.Label]
		if agg == nil {
			agg = &labelAgg{}
			byLabel[e.Label] = agg
		}
		agg.n++
		if !*gate {
			samples := make([]motion.FrameSample, 0, len(info.Samples))
			for _, s := range info.Samples {
				samples = append(samples, motion.FrameSample{Size: s.Size, IsKeyframe: s.IsKeyFrame})
			}
			res := motion.ScoreSamples(samples, motion.DefaultOptions())
			eff := res.Score * res.Confidence
			fmt.Printf("%-28s %-11s %7.2f %9.0f %6.2f %9.2f\n", name, e.Label, res.Score, res.MedianP, res.Confidence, eff)
			agg.scoreSum += res.Score
			agg.effSum += eff
		} else {
			interval := time.Second / time.Duration(max(1, int(*fps)))
			if *fps == 0 && info.TotalDuration > 0 && len(frames) > 0 {
				interval = info.TotalDuration / time.Duration(len(frames))
			}
			base := recorder.ReplayAdaptive(frames, interval, recorder.DefaultAdaptiveConfig())
			fmt.Printf("%-28s %-11s %6.0f%% %6d %8.0f", name, e.Label, base.TLShare*100, base.Switches, base.NoiseFloor)
			agg.tlSum += base.TLShare
			agg.swSum += float64(base.Switches)
			if hasCandidate {
				c := recorder.ReplayAdaptive(frames, interval, candCfg)
				fmt.Printf(" | %6.0f%% %6d %8.0f", c.TLShare*100, c.Switches, c.NoiseFloor)
				agg.tlCand += c.TLShare
				agg.swCand += float64(c.Switches)
			}
			fmt.Println()
		}
	}

	fmt.Println()
	fmt.Printf("by label (%d entries):\n", len(corpus))
	for label, agg := range byLabel {
		if agg.n == 0 {
			continue
		}
		switch {
		case *gate:
			line := fmt.Sprintf("  %-11s mean TL%%=%.0f switches=%.0f", label, agg.tlSum/float64(agg.n)*100, agg.swSum/float64(agg.n))
			if hasCandidate {
				line += fmt.Sprintf(" | candidate TL%%=%.0f switches=%.0f", agg.tlCand/float64(agg.n)*100, agg.swCand/float64(agg.n))
			}
			fmt.Println(line)
		default:
			fmt.Printf("  %-11s mean score=%.2f effective=%.2f\n", label, agg.scoreSum/float64(agg.n), agg.effSum/float64(agg.n))
		}
	}
	os.Exit(0)
}

type labelAgg struct {
	n                int
	scoreSum, effSum float64
	tlSum, swSum     float64
	tlCand, swCand   float64
}
