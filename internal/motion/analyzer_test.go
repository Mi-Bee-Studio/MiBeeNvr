package motion

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
)

// fakeScoreDB records UpdateRecordingMotionScore calls.
type fakeScoreDB struct {
	mu     sync.Mutex
	scored map[string][2]any // id -> {score, flags}
}

func (f *fakeScoreDB) UpdateRecordingMotionScore(_ context.Context, id string, score float64, flags string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.scored == nil {
		f.scored = map[string][2]any{}
	}
	f.scored[id] = [2]any{score, flags}
	return nil
}

func (f *fakeScoreDB) get(id string) ([2]any, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.scored[id]
	return v, ok
}

// writeTestSegment writes a real MP4 with the muxer so the analyzer's parse
// path (ParseSegmentNoProbe) runs against genuine box structure. NAL payload
// sizes follow `pattern`: "static" = flat small frames, "active" = 10% spikes.
func writeTestSegment(t *testing.T, path, pattern string) {
	t.Helper()
	var (
		sps = []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
		pps = []byte{0x68, 0xce, 0x3c, 0x80}
	)
	m := muxer.NewMP4Muxer(path)
	trackID, err := m.AddH264Track(sps, pps)
	if err != nil {
		t.Fatalf("add track: %v", err)
	}
	frameDur := 50 * time.Millisecond
	for i := range 300 {
		size := 800
		if pattern == "active" && i >= 50 && i < 80 {
			size = 9000
		}
		nalu := make([]byte, size)
		nalu[0] = 0x41 // non-IDR slice header-ish first byte (content irrelevant)
		if err := m.WriteSample(trackID, nalu, time.Duration(i)*frameDur, frameDur); err != nil {
			t.Fatalf("write sample %d: %v", i, err)
		}
	}
	if err := m.Close(); err != nil {
		t.Fatalf("close muxer: %v", err)
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", timeout)
}

func TestAnalyzer_ScoresCompletedSegment(t *testing.T) {
	dir := t.TempDir()
	segPath := filepath.Join(dir, "seg_static.mp4")
	writeTestSegment(t, segPath, "static")

	db := &fakeScoreDB{}
	bus := event.NewEventBus(64)
	a := NewAnalyzer(db, bus, dir, DefaultOptions())
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer a.Stop()

	bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
		CameraID:    "cam-1",
		FilePath:    segPath,
		Format:      "h264",
		RecordingID: "rec-static",
	})

	waitFor(t, 3*time.Second, func() bool {
		_, ok := db.get("rec-static")
		return ok
	})
	got, _ := db.get("rec-static")
	score := got[0].(float64)
	flags := got[1].(string)
	if score != 0 || flags != "static" {
		t.Fatalf("static segment: got score=%v flags=%q, want 0/static", score, flags)
	}
}

func TestAnalyzer_ActiveSegmentScoresHigh(t *testing.T) {
	dir := t.TempDir()
	segPath := filepath.Join(dir, "seg_active.mp4")
	writeTestSegment(t, segPath, "active")

	db := &fakeScoreDB{}
	bus := event.NewEventBus(64)
	a := NewAnalyzer(db, bus, dir, DefaultOptions())
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer a.Stop()

	bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
		CameraID:    "cam-1",
		FilePath:    segPath,
		Format:      "h264",
		RecordingID: "rec-active",
	})

	waitFor(t, 3*time.Second, func() bool {
		_, ok := db.get("rec-active")
		return ok
	})
	got, _ := db.get("rec-active")
	if got[0].(float64) < 0.3 {
		t.Fatalf("active segment score = %v, want >= 0.3", got[0].(float64))
	}
}

func TestAnalyzer_RelativePathResolvedAgainstRoot(t *testing.T) {
	dir := t.TempDir()
	segPath := filepath.Join(dir, "recordings", "cam-1", "seg_rel.mp4")
	if err := os.MkdirAll(filepath.Dir(segPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestSegment(t, segPath, "static")

	db := &fakeScoreDB{}
	bus := event.NewEventBus(64)
	a := NewAnalyzer(db, bus, dir, DefaultOptions())
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer a.Stop()

	rel, err := filepath.Rel(dir, segPath)
	if err != nil {
		t.Fatal(err)
	}
	bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
		CameraID:    "cam-1",
		FilePath:    rel, // root-relative, as documented on the event type
		Format:      "h264",
		RecordingID: "rec-rel",
	})

	waitFor(t, 3*time.Second, func() bool {
		_, ok := db.get("rec-rel")
		return ok
	})
}

func TestAnalyzer_SkipsNonDifferentialFormats(t *testing.T) {
	dir := t.TempDir()
	db := &fakeScoreDB{}
	bus := event.NewEventBus(64)
	a := NewAnalyzer(db, bus, dir, DefaultOptions())
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer a.Stop()

	for _, format := range []string{"mjpeg", "jpeg", "avi", "timelapse"} {
		bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
			CameraID:    "cam-1",
			FilePath:    filepath.Join(dir, "nope.mp4"),
			Format:      format,
			RecordingID: "rec-" + format,
		})
	}

	// Nothing should ever be scored for these formats. A short wait makes the
	// negative assertion robust against slow scheduling.
	time.Sleep(150 * time.Millisecond)
	db.mu.Lock()
	n := len(db.scored)
	db.mu.Unlock()
	if n != 0 {
		t.Fatalf("non-differential formats must be skipped, %d recordings scored", n)
	}
}

func TestAnalyzer_MissingFileIsNonFatal(t *testing.T) {
	dir := t.TempDir()
	db := &fakeScoreDB{}
	bus := event.NewEventBus(64)
	a := NewAnalyzer(db, bus, dir, DefaultOptions())
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer a.Stop()

	// Missing file → parse error → logged + skipped; the worker must survive.
	bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
		CameraID:    "cam-1",
		FilePath:    filepath.Join(dir, "gone.mp4"),
		Format:      "h264",
		RecordingID: "rec-gone",
	})

	segPath := filepath.Join(dir, "seg_after.mp4")
	writeTestSegment(t, segPath, "static")
	bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
		CameraID:    "cam-1",
		FilePath:    segPath,
		Format:      "h264",
		RecordingID: "rec-after",
	})

	waitFor(t, 3*time.Second, func() bool {
		_, ok := db.get("rec-after")
		return ok
	})
	if _, ok := db.get("rec-gone"); ok {
		t.Fatal("missing file must not be scored")
	}
}
