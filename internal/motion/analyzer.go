// analyzer.go — the offline motion-score service (issue #435, Phase 1).
//
// It subscribes to TopicSegmentCompleted, scores each finished H.264/H.265
// MP4 segment with the compressed-domain scorer (ScoreSamples), and persists
// motion_score + activity_flags to the recordings row. The per-segment cost
// is a few KB of box-metadata reads (ParseSegmentNoProbe never touches mdat),
// i.e. microseconds on the target hardware — no idle scheduling needed.

package motion

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
)

// ScoreDB is the storage surface the analyzer needs. Satisfied by
// *storage.DB; narrow interface keeps the analyzer testable without SQLite.
type ScoreDB interface {
	UpdateRecordingMotionScore(ctx context.Context, id string, score, confidence float64, flags string) error
}

// segmentParser is swappable for tests (ParseSegmentNoProbe by default).
type segmentParser func(path string) (*merge.SegmentInfo, error)

// FGWindow is the pixel-domain activity summary for a wall-time window,
// supplied by the pixgate FG time series (adapter in pkg/app).
type FGWindow struct {
	Valid       int     // trustworthy samples in the window
	MeanAreaPct float64 // mean largest-blob area over valid samples
	DutyCycle   float64 // fraction of valid samples at/above the trigger area
	TrigAreaPct float64 // the DutyCycle reference
	Coverage    float64 // observed fraction of the window
}

// PixelSource supplies pixgate FG windows per camera + wall-time window.
// When a segment's window has enough pixel coverage, the analyzer prefers
// the pixel signal over the compressed-domain score (2026-09-01: byte
// spikes conflate motion with night gain noise and encoder refresh frames;
// the background-modeled FG duty does not).
type PixelSource interface {
	SegmentFG(cameraID string, start, end time.Time) (FGWindow, bool)
}

// minFGValid / minFGCoverage gate the pixel path: below these the window is
// too thinly observed to speak for the whole segment and the byte score
// stays authoritative.
const (
	minFGValid    = 8
	minFGCoverage = 0.5
)

// Analyzer scores completed segments for activity. It implements the
// pkg/app Service interface (Name/Start/Stop).
type Analyzer struct {
	db      ScoreDB
	bus     *event.EventBus
	rootDir string // storage root; relative FilePath values are resolved against it
	opts    Options
	parse   segmentParser
	pixel   PixelSource

	subCh chan event.Event

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// SetPixelSource wires the pixgate FG time series (pixel-preferred scoring).
// Optional — without it the analyzer stays compressed-domain only. Safe to
// call after Start (read atomically per event).
func (a *Analyzer) SetPixelSource(ps PixelSource) {
	a.mu.Lock()
	a.pixel = ps
	a.mu.Unlock()
}

func (a *Analyzer) pixelSource() PixelSource {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pixel
}

// NewAnalyzer builds the analyzer. rootDir is the storage root used to
// resolve root-relative FilePath values in SegmentCompleted events.
func NewAnalyzer(db ScoreDB, bus *event.EventBus, rootDir string, opts Options) *Analyzer {
	if opts.MinPFrameSamples == 0 {
		opts = DefaultOptions()
	}
	return &Analyzer{
		db:      db,
		bus:     bus,
		rootDir: rootDir,
		opts:    opts,
		parse:   merge.ParseSegmentNoProbe,
	}
}

// Name implements pkg/app.Service.
func (a *Analyzer) Name() string { return "motion-score" }

// Start subscribes to SegmentCompleted and launches the worker loop.
// Re-analysis of already-scored segments never happens: only newly completed
// segments arrive on the bus.
func (a *Analyzer) Start(ctx context.Context) error {
	a.mu.Lock()
	if a.cancel != nil {
		a.mu.Unlock()
		return nil // already running
	}
	runCtx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	done := make(chan struct{})
	a.done = done
	subCh := make(chan event.Event, 64)
	a.subCh = subCh
	a.mu.Unlock()

	if err := a.bus.Subscribe(event.TopicSegmentCompleted, subCh, 64); err != nil {
		cancel()
		a.mu.Lock()
		a.cancel = nil
		a.done = nil
		a.subCh = nil
		a.mu.Unlock()
		return err
	}

	go func() {
		defer close(done)
		a.run(runCtx, subCh)
	}()
	return nil
}

// Stop unsubscribes and joins the worker goroutine.
func (a *Analyzer) Stop() error {
	a.mu.Lock()
	cancel := a.cancel
	done := a.done
	subCh := a.subCh
	a.cancel = nil
	a.done = nil
	a.subCh = nil
	a.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if subCh != nil {
		a.bus.Unsubscribe(event.TopicSegmentCompleted, subCh)
	}
	if done != nil {
		<-done
	}
	return nil
}

// run is the worker loop: reads events until ctx is cancelled. The bus drops
// the oldest event when the channel is full, so a slow disk can never block a
// recorder — unanalyzed segments simply stay motion_score=-1 (neutral for
// cleanup ordering).
func (a *Analyzer) run(ctx context.Context, ch <-chan event.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt := <-ch:
			a.handleEvent(ctx, evt)
		}
	}
}

func (a *Analyzer) handleEvent(ctx context.Context, evt event.Event) {
	seg, ok := evt.Data.(event.SegmentCompleted)
	if !ok {
		return
	}
	// Only differential codecs carry the P-frame-size motion signal, and
	// ParseSegment is MP4-only. MJPEG/JPEG/AVI/timelapse segments are skipped
	// (they keep motion_score=-1, which ranks neutrally in cleanup).
	switch seg.Format {
	case "h264", "h265":
	default:
		return
	}
	if seg.RecordingID == "" || seg.FilePath == "" {
		return
	}

	path := seg.FilePath
	if !filepath.IsAbs(path) {
		path = filepath.Join(a.rootDir, path)
	}

	start := time.Now()
	info, err := a.parse(path)
	if err != nil {
		// Actively-written or already-merged-away files are expected races —
		// demote to debug. Anything else is a warning, never fatal.
		slog.Debug("motion analyzer: parse segment failed",
			"camera_id", seg.CameraID, "path", path, "error", err)
		return
	}

	samples := make([]FrameSample, 0, len(info.Samples))
	for _, s := range info.Samples {
		samples = append(samples, FrameSample{Size: s.Size, IsKeyframe: s.IsKeyFrame})
	}
	res := ScoreSamples(samples, a.opts)

	// Pixel-preferred routing (2026-09-01): when the pixgate FG series
	// covers the segment's window well enough, its background-modeled duty
	// cycle IS the activity score — immune to night gain noise and encoder
	// refresh frames that inflate byte spikes. The byte result still runs
	// (microseconds) for the scene_cut flag and field comparison.
	score, conf, flags := res.Score, res.Confidence, res.Flags
	source := "bytes"
	if ps := a.pixelSource(); ps != nil {
		if start, end, ok := segmentWindow(seg); ok {
			if w, wok := ps.SegmentFG(seg.CameraID, start, end); wok &&
				w.Valid >= minFGValid && w.Coverage >= minFGCoverage {
				score = pixelScore(w)
				conf = w.Coverage
				flags = pixelFlags(w, res.Flags)
				source = "pixel"
			}
		}
	}

	if err := a.db.UpdateRecordingMotionScore(ctx, seg.RecordingID, score, conf, strings.Join(flags, ",")); err != nil {
		slog.Warn("motion analyzer: update score failed",
			"recording_id", seg.RecordingID, "error", err)
		return
	}
	slog.Debug("motion analyzer: scored segment",
		"camera_id", seg.CameraID, "recording_id", seg.RecordingID,
		"source", source,
		"score", score, "confidence", conf, "byte_score", res.Score,
		"flags", strings.Join(flags, ","),
		"frames", res.PCount, "took", time.Since(start))
}

// segmentWindow parses the event's wall-time bounds (RFC3339Nano from the
// recorders; a couple of tolerant fallbacks for safety).
func segmentWindow(seg event.SegmentCompleted) (time.Time, time.Time, bool) {
	layouts := []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999 -0700 MST", "2006-01-02 15:04:05.999999999"}
	start, err1 := time.Parse(layouts[0], seg.StartedAt)
	if err1 != nil {
		for _, l := range layouts[1:] {
			if t, err := time.Parse(l, seg.StartedAt); err == nil {
				start, err1 = t, nil
				break
			}
		}
	}
	end, err2 := time.Parse(layouts[0], seg.EndedAt)
	if err2 != nil {
		for _, l := range layouts[1:] {
			if t, err := time.Parse(l, seg.EndedAt); err == nil {
				end, err2 = t, nil
				break
			}
		}
	}
	if err1 != nil || err2 != nil || !end.After(start) {
		return time.Time{}, time.Time{}, false
	}
	return start, end, true
}

// pixelScore maps the FG window onto [0,1]: duty dominates (a person present
// for most of the window ⇒ high; a car crossing once ⇒ low-but-nonzero;
// night shimmer ⇒ ~0), with a small magnitude term so a short but
// scene-filling event still registers.
func pixelScore(w FGWindow) float64 {
	mag := 0.0
	if w.TrigAreaPct > 0 {
		mag = w.MeanAreaPct / (2 * w.TrigAreaPct)
		if mag > 1 {
			mag = 1
		}
	}
	s := 0.8*w.DutyCycle + 0.2*mag
	if s > 1 {
		s = 1
	}
	if s < 0 {
		s = 0
	}
	return s
}

// pixelFlags derives the activity vocabulary from the FG window; scene_cut
// passes through from the byte result (a real discontinuity signal).
func pixelFlags(w FGWindow, byteFlags []string) []string {
	var flags []string
	if w.DutyCycle >= 0.02 {
		flags = append(flags, "motion")
	} else {
		flags = append(flags, "static")
	}
	for _, f := range byteFlags {
		if f == "scene_cut" {
			flags = append(flags, f)
		}
	}
	return flags
}
