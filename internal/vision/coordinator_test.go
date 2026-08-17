package vision

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

func TestHealthTrackerRecoveryCallback(t *testing.T) {
	h := NewHealthTracker(60)

	var fired atomic.Int32
	h.SetOnRecovery(func() { fired.Add(1) })

	// First heartbeat: unhealthy → healthy transition fires recovery.
	h.RecordHeartbeat(HeartbeatStatus{Status: "healthy"})
	require.True(t, h.IsHealthy())
	require.Eventually(t, func() bool { return fired.Load() == 1 },
		2*time.Second, 20*time.Millisecond, "recovery callback not fired on first heartbeat")

	// Subsequent heartbeats while already healthy must NOT fire again.
	h.RecordHeartbeat(HeartbeatStatus{Status: "healthy"})
	h.RecordHeartbeat(HeartbeatStatus{Status: "healthy"})
	require.Equal(t, int32(1), fired.Load())
}

func TestCoordinatorPauseWindowTracking(t *testing.T) {
	bus := event.NewEventBus(64)
	cfg := config.VisionConfig{Enabled: true, URL: "http://127.0.0.1:1"}
	c := NewCoordinator(
		func() config.VisionConfig { return cfg },
		func() string { return t.TempDir() },
		bus,
		nil, // no DB → compensation disabled, but pause tracking still runs
	)

	require.True(t, c.takePausedSince().IsZero(), "no pause before any skip")

	c.markPaused()
	first := c.takePausedSince()
	require.False(t, first.IsZero())

	// Idempotent while armed: repeated skips don't move the window start.
	c.rearmPaused(first)
	c.markPaused()
	require.Equal(t, first, c.takePausedSince(), "markPaused must not overwrite an armed window")

	// takePausedSince clears the window.
	require.True(t, c.takePausedSince().IsZero())
}

type fakeRepusher struct {
	mu   sync.Mutex
	recs []model.Recording
}

// Mirror of the SQL window: completion-keyed with a 1-minute grace on since.
func (f *fakeRepusher) ListRecordingsForVisionRepush(ctx context.Context, since, until time.Time, limit int) ([]model.Recording, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	grace := since.Add(-time.Minute)
	out := make([]model.Recording, 0, len(f.recs))
	for _, r := range f.recs {
		if !r.EndedAt.Before(grace) && !r.EndedAt.After(until) {
			out = append(out, r)
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// Core #329 scenario: segments skipped while offline are re-pushed when the
// consumer's heartbeat recovers.
func TestCoordinatorOfflineCompensation(t *testing.T) {
	var mu sync.Mutex
	pushed := map[string]int{}

	visionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		pushed[r.Header.Get("X-Recording-Id")]++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer visionSrv.Close()

	rp := &fakeRepusher{}
	bus := event.NewEventBus(64)
	cfg := config.VisionConfig{Enabled: true, URL: visionSrv.URL}
	c := NewCoordinator(
		func() config.VisionConfig { return cfg },
		func() string { return t.TempDir() },
		bus,
		rp,
	)
	require.NoError(t, c.Start(context.Background()))
	t.Cleanup(c.Stop)

	// Vision is offline (no heartbeat): live pushes are skipped.
	segPath := filepath.Join(t.TempDir(), "seg1.mp4")
	require.NoError(t, os.WriteFile(segPath, make([]byte, 16), 0o644))
	bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
		CameraID: "cam1", FilePath: segPath, Format: "mp4",
		FileSize: 16, RecordingID: "rec-live",
	})
	// Give the event loop a moment — the push must be skipped, not delivered.
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	require.Zero(t, pushed["rec-live"], "live push must be skipped while offline")
	mu.Unlock()

	// The missed segment is discoverable in the DB on recovery.
	rp.mu.Lock()
	rp.recs = []model.Recording{{
		ID: "rec-live", CameraID: "cam1", FilePath: segPath, Format: "mp4",
		StartedAt: time.Now().Add(-time.Minute), EndedAt: time.Now(),
		FileSize: 16, MergeStatus: model.MergeStatusPending,
	}}
	rp.mu.Unlock()

	// Heartbeat recovery → compensation fires → the missed segment is pushed.
	c.Health().RecordHeartbeat(HeartbeatStatus{Status: "healthy"})
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return pushed["rec-live"] == 1
	}, 5*time.Second, 50*time.Millisecond, "compensation push did not arrive")

	// Exactly once — no duplicate pushes from the same recovery.
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	require.Equal(t, 1, pushed["rec-live"])
	mu.Unlock()

	// The pause window is consumed: another recovery with no new offline gap
	// pushes nothing.
	c.Health().RecordHeartbeat(HeartbeatStatus{Status: "healthy"})
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	require.Equal(t, 1, pushed["rec-live"])
	mu.Unlock()
}

func TestRecordingToSegment(t *testing.T) {
	st := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	et := st.Add(30 * time.Second)
	seg := recordingToSegment(model.Recording{
		ID: "r1", CameraID: "cam1", FilePath: "/data/cam1/x.mp4",
		Format: "mp4", StartedAt: st, EndedAt: et, FileSize: 123,
	})
	require.Equal(t, "r1", seg.RecordingID)
	require.Equal(t, "cam1", seg.CameraID)
	require.Equal(t, "mp4", seg.Format)
	require.Equal(t, int64(123), seg.FileSize)
	require.Equal(t, "2026-08-16T12:00:00Z", seg.StartedAt)
	require.Equal(t, "2026-08-16T12:00:30Z", seg.EndedAt)
}
