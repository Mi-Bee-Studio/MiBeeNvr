package recorder

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// audioTestStore is a file-backed SegmentStore: real files in t.TempDir() so
// the real MP4 muxer runs end to end (open → write → close → rename).
type audioTestStore struct {
	mu        sync.Mutex
	dir       string
	created   int
	closed    int
	tempPaths []string
}

func (s *audioTestStore) CreateSegment(cameraID string, _ string) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.created++
	suffix := string(rune('a' + s.created - 1))
	temp := filepath.Join(s.dir, cameraID, "seg-tmp-"+suffix+".tmp")
	final := filepath.Join(s.dir, cameraID, "seg-"+suffix+".mp4")
	if err := os.MkdirAll(filepath.Dir(temp), 0o755); err != nil {
		return "", "", err
	}
	f, err := os.Create(temp)
	if err != nil {
		return "", "", err
	}
	f.Close()
	s.tempPaths = append(s.tempPaths, temp)
	return temp, final, nil
}

func (s *audioTestStore) WriteFrame(string, []byte) (int, error) { return 0, nil }

func (s *audioTestStore) CloseSegment(temp, final string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed++
	return os.Rename(temp, final)
}

// audioTestDB captures inserted recordings.
type audioTestDB struct {
	mu   sync.Mutex
	rows []*model.Recording
}

func (d *audioTestDB) InsertRecording(_ context.Context, r *model.Recording) error {
	return d.InsertRecordingWithRetry(context.Background(), r, 1, 0)
}

func (d *audioTestDB) InsertRecordingWithRetry(_ context.Context, r *model.Recording, _ int, _ time.Duration) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rows = append(d.rows, r)
	return nil
}

func (d *audioTestDB) SetMergeStatus(_ context.Context, _ []string, _ string) error { return nil }

func (d *audioTestDB) rows_() []*model.Recording {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]*model.Recording(nil), d.rows...)
}

func newAudioTestRecorder(t *testing.T, segDur time.Duration) (*AudioRecorder, *audioTestStore, *audioTestDB) {
	t.Helper()
	dir := t.TempDir()
	store := &audioTestStore{dir: dir}
	db := &audioTestDB{}
	r := NewAudioRecorder(AudioConfig{
		CameraID:   "cam-mic",
		RTSPURL:    "rtsp://mic/audio",
		SegmentDur: segDur,
		DB:         db,
		Store:      store,
	}, store, nil)
	// Simulate a negotiated G.711 codec snapshot (the connect path's job).
	r.audioCfg.Store(&audioConfig{
		codec:          "g711",
		sampleRate:     8000,
		channels:       1,
		g711MULaw:      true,
		g711SampleRate: 8000,
		muxerConfig:    []byte{1, 0, 0, 0x1f, 0x40}, // μ-law, 8000Hz
	})
	return r, store, db
}

// feedAUs drives the writer goroutine with the given AUs, then cancels.
func feedAUs(t *testing.T, r *AudioRecorder, aus []audioAU) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.writeAUs(ctx)
	}()
	for _, au := range aus {
		r.auCh <- au
	}
	// Give the writer time to drain before cancelling.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.dropped.Load() == 0 && len(r.auCh) == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
}

func g711AU(at time.Time) audioAU {
	// 160 bytes ≙ 20ms of G.711 at 8kHz.
	return audioAU{
		data:     make([]byte, 160),
		codec:    model.AudioG711,
		duration: 20 * time.Millisecond,
		at:       at,
	}
}

// TestAudioRecorder_RotatesOnWallClock pins the core new behavior: segments
// rotate on ARRIVAL-TIME elapsed (no IDR to wait for), each closed segment
// lands as a born-terminal DB row, and files really exist on disk.
func TestAudioRecorder_RotatesOnWallClock(t *testing.T) {
	r, store, db := newAudioTestRecorder(t, 500*time.Millisecond)

	start := time.Now()
	var aus []audioAU
	// 30 AUs × 20ms = 600ms span inside the first segment window.
	for i := range 30 {
		aus = append(aus, g711AU(start.Add(time.Duration(i)*20*time.Millisecond)))
	}
	// Next AU 600ms after start — past the 500ms rotation → new segment.
	aus = append(aus, g711AU(start.Add(600*time.Millisecond)))
	aus = append(aus, g711AU(start.Add(620*time.Millisecond)))

	feedAUs(t, r, aus)

	if got := len(db.rows_()); got != 2 {
		t.Fatalf("expected 2 recording rows after rotation, got %d", got)
	}
	if store.created != 2 || store.closed != 2 {
		t.Fatalf("expected store create/close ×2, got created=%d closed=%d", store.created, store.closed)
	}
	for i, row := range db.rows_() {
		if row.Format != model.FormatAudio {
			t.Errorf("row %d: format = %q, want audio", i, row.Format)
		}
		if row.MergeStatus != model.MergeStatusAudio {
			t.Errorf("row %d: merge_status = %q, want born-terminal audio", i, row.MergeStatus)
		}
		if row.CameraID != "cam-mic" {
			t.Errorf("row %d: camera = %q", i, row.CameraID)
		}
		if row.FrameCount <= 0 {
			t.Errorf("row %d: AU count = %d, want > 0", i, row.FrameCount)
		}
		if _, err := os.Stat(row.FilePath); err != nil {
			t.Errorf("row %d: segment file missing: %v", i, err)
		}
	}
	// First segment holds the pre-rotation AUs, second the late pair.
	// AU[0] anchors segStart=start; AU[25] lands exactly at 500ms and
	// triggers the rotation, so 25 AUs land in segment 1, 7 in segment 2.
	if got := db.rows_()[0].FrameCount; got != 25 {
		t.Errorf("first segment AU count = %d, want 25", got)
	}
	if got := db.rows_()[1].FrameCount; got != 7 {
		t.Errorf("second segment AU count = %d, want 7", got)
	}
}

// TestAudioRecorder_NoAUsNoSegment pins lazy segment creation: a recorder
// that never receives audio opens no segment and inserts no row.
func TestAudioRecorder_NoAUsNoSegment(t *testing.T) {
	r, store, db := newAudioTestRecorder(t, time.Second)
	feedAUs(t, r, nil)
	if store.created != 0 {
		t.Fatalf("expected no segments, got %d", store.created)
	}
	if len(db.rows_()) != 0 {
		t.Fatalf("expected no rows, got %d", len(db.rows_()))
	}
}

// TestAudioRecorder_LiveOnlySkipsDisk pins the record_enabled=false contract:
// AUs flow (live path), no segment is ever created.
func TestAudioRecorder_LiveOnlySkipsDisk(t *testing.T) {
	dir := t.TempDir()
	store := &audioTestStore{dir: dir}
	db := &audioTestDB{}
	no := false
	r := NewAudioRecorder(AudioConfig{
		CameraID:      "cam-mic",
		SegmentDur:    time.Second,
		DB:            db,
		Store:         store,
		RecordEnabled: &no,
	}, store, nil)
	r.audioCfg.Store(&audioConfig{
		codec:       "g711",
		sampleRate:  8000,
		channels:    1,
		muxerConfig: []byte{1, 0, 0, 0x1f, 0x40},
	})

	start := time.Now()
	var aus []audioAU
	for i := range 50 {
		aus = append(aus, g711AU(start.Add(time.Duration(i)*20*time.Millisecond)))
	}
	feedAUs(t, r, aus)

	if store.created != 0 || len(db.rows_()) != 0 {
		t.Fatalf("live-only wrote to disk: segments=%d rows=%d", store.created, len(db.rows_()))
	}
}

// TestAudioRecorder_EventsAndStats pins the SegmentCompleted publish and the
// stats snapshot shape.
func TestAudioRecorder_EventsAndStats(t *testing.T) {
	r, _, db := newAudioTestRecorder(t, 10*time.Second)
	bus := event.NewEventBus(16)
	r.cfg.EventBus = bus

	events := make(chan event.Event, 4)
	if err := bus.Subscribe(event.TopicSegmentCompleted, events, 4); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	start := time.Now()
	feedAUs(t, r, []audioAU{g711AU(start), g711AU(start.Add(20 * time.Millisecond))})

	select {
	case evt := <-events:
		if evt.Topic != event.TopicSegmentCompleted {
			t.Fatalf("event topic = %q", evt.Topic)
		}
		sc, ok := evt.Data.(event.SegmentCompleted)
		if !ok {
			t.Fatalf("event data type = %T", evt.Data)
		}
		if sc.CameraID != "cam-mic" || sc.Format != string(model.FormatAudio) || sc.RecordingID == "" {
			t.Fatalf("unexpected SegmentCompleted payload: %+v", sc)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SegmentCompleted event not delivered within 2s")
	}
	if len(db.rows_()) != 1 {
		t.Fatalf("expected 1 row, got %d", len(db.rows_()))
	}

	// feedAUs bypasses enqueue (which stamps lastAU); stamp it here so the
	// stats assertion measures the Stats accessor, not the feed path.
	r.lastAU.Store(time.Now().UnixNano())
	st := r.Stats()
	if st.LastAUAge > 5*time.Second {
		t.Errorf("LastAUAge = %s, want recent", st.LastAUAge)
	}
	if !st.SegmentOpen {
		// The single segment was closed by feedAUs' cancel path.
		t.Log("segment already closed by teardown — acceptable")
	}
}

// TestAudioRecorder_ImplementsRecorder is a compile-time interface check.
func TestAudioRecorder_ImplementsRecorder(t *testing.T) {
	var _ model.Recorder = (*AudioRecorder)(nil)
}
