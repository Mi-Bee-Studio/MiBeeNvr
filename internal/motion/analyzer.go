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
	UpdateRecordingMotionScore(ctx context.Context, id string, score float64, flags string) error
}

// segmentParser is swappable for tests (ParseSegmentNoProbe by default).
type segmentParser func(path string) (*merge.SegmentInfo, error)

// Analyzer scores completed segments for activity. It implements the
// pkg/app Service interface (Name/Start/Stop).
type Analyzer struct {
	db      ScoreDB
	bus     *event.EventBus
	rootDir string // storage root; relative FilePath values are resolved against it
	opts    Options
	parse   segmentParser

	subCh chan event.Event

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
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

	if err := a.db.UpdateRecordingMotionScore(ctx, seg.RecordingID, res.Score, strings.Join(res.Flags, ",")); err != nil {
		slog.Warn("motion analyzer: update score failed",
			"recording_id", seg.RecordingID, "error", err)
		return
	}
	slog.Debug("motion analyzer: scored segment",
		"camera_id", seg.CameraID, "recording_id", seg.RecordingID,
		"score", res.Score, "flags", strings.Join(res.Flags, ","),
		"frames", res.PCount, "took", time.Since(start))
}
