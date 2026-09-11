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

// TestPushBreakerStateMachine is the regression core of the 2026-09-11 vm-v8
// incident class: a Vision instance whose heartbeats arrive fine but whose
// push URL is dead (tunnel down, listener gone, moved network) got hammered
// with a failed push per segment for HOURS. The push breaker opens after
// consecutive upload failures, ignores heartbeats (a heartbeat proves the
// process is alive, NOT that our push URL is reachable), and re-closes via a
// half-open probe after a backoff.
func TestPushBreakerStateMachine(t *testing.T) {
	h := NewHealthTracker(60)
	h.pushBackoffBase = 30 * time.Millisecond // test speed
	h.RecordHeartbeat(HeartbeatStatus{Status: "healthy"})
	require.True(t, h.IsHealthy())

	// Below threshold: failures accumulate but the gate stays open.
	for i := 1; i <= 4; i++ {
		justOpened, justClosed := h.RecordPushOutcome(false)
		require.False(t, justOpened, "failure %d must not open the breaker", i)
		require.False(t, justClosed)
		require.True(t, h.PushGate(), "gate still closed below threshold")
	}

	// Threshold failure opens the breaker.
	justOpened, _ := h.RecordPushOutcome(false)
	require.True(t, justOpened, "5th consecutive failure must open the breaker")
	require.False(t, h.PushGate(), "open breaker must block pushes")

	// THE vm-v8 regression essence: heartbeats must NOT close the breaker.
	h.RecordHeartbeat(HeartbeatStatus{Status: "healthy"})
	require.True(t, h.IsHealthy(), "heartbeat health unaffected")
	require.False(t, h.PushGate(), "healthy heartbeat must NOT reset the push breaker")

	// Backoff elapses → half-open: exactly one probe is allowed.
	require.Eventually(t, func() bool { return h.PushGate() },
		2*time.Second, 5*time.Millisecond, "gate must half-open after the backoff")

	// Probe fails → re-opens (escalated backoff), still no heartbeat reset.
	justOpened, _ = h.RecordPushOutcome(false)
	require.True(t, justOpened, "failed probe must re-open the breaker")
	require.False(t, h.PushGate())
	h.RecordHeartbeat(HeartbeatStatus{Status: "healthy"})
	require.False(t, h.PushGate())

	// Escalated backoff also elapses → probe succeeds → breaker closes and
	// reports justClosed exactly once.
	require.Eventually(t, func() bool { return h.PushGate() },
		2*time.Second, 5*time.Millisecond)
	_, justClosed := h.RecordPushOutcome(true)
	require.True(t, justClosed, "successful probe must report justClosed")
	require.True(t, h.PushGate())
	_, justClosed = h.RecordPushOutcome(true)
	require.False(t, justClosed, "steady-state success reports no transition")
}

// TestPushBreakerBackoffCapsAtMax ensures the exponential backoff is bounded.
func TestPushBreakerBackoffCapsAtMax(t *testing.T) {
	h := NewHealthTracker(60)
	h.pushBackoffBase = 20 * time.Millisecond
	h.pushBackoffMax = 40 * time.Millisecond
	h.RecordHeartbeat(HeartbeatStatus{Status: "healthy"})

	for i := 0; i < 8; i++ { // far past the cap's reach
		h.RecordPushOutcome(false)
	}
	require.False(t, h.PushGate())
	// Cap is 40ms — after 200ms the gate MUST be half-open regardless of how
	// many failures accumulated.
	require.Eventually(t, func() bool { return h.PushGate() },
		2*time.Second, 5*time.Millisecond, "backoff must cap at pushBackoffMax")
}

// TestCoordinatorPushBreakerEndToEnd drives the full wiring: a dead push URL
// under healthy heartbeats gets breaker-opened after the threshold, further
// segments are not even attempted (no disk read, no request), the pause
// window is anchored for compensation, and a later successful probe closes
// the breaker and fires the bounded offline compensation.
func TestCoordinatorPushBreakerEndToEnd(t *testing.T) {
	var hits atomic.Int32
	var mu sync.Mutex
	pushedIDs := map[string]int{}
	fail := atomic.Bool{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		mu.Lock()
		pushedIDs[r.Header.Get("X-Recording-Id")]++
		mu.Unlock()
		if fail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	segFile := filepath.Join(t.TempDir(), "seg.mp4")
	require.NoError(t, os.WriteFile(segFile, []byte("payload"), 0o600))

	bus := event.NewEventBus(64)
	cfg := config.VisionConfig{
		Enabled:   true,
		PushMode:  "upload",
		Instances: []config.VisionInstance{{Name: "default", URL: srv.URL}},
	}
	rep := &fakeRepusher{recs: []model.Recording{{
		ID: "rec-lost", CameraID: "cam-a", FilePath: segFile, Format: "h264",
		EndedAt: time.Now().Add(-time.Minute),
	}}}
	c := NewCoordinator(
		func() config.VisionConfig { return cfg },
		func() string { return t.TempDir() },
		bus, rep, nil,
	)
	require.NoError(t, c.Start(context.Background()))
	defer c.Stop()
	c.RecordHeartbeat("", HeartbeatStatus{Status: "healthy"})

	// Shrink the breaker timing for the test (unexported, same package).
	c.instMu.Lock()
	dflt := c.insts["default"]
	c.instMu.Unlock()
	require.NotNil(t, dflt)
	dflt.health.pushBackoffBase = 30 * time.Millisecond

	seg := event.SegmentCompleted{
		CameraID: "cam-a", RecordingID: "rec-1", FilePath: segFile,
		FileSize: 7, Format: "h264", Encoding: "h264", Layer: model.LayerMain,
	}

	fail.Store(true)
	for i := 0; i < 5; i++ {
		c.handleSegment(context.Background(), seg)
	}
	require.Equal(t, int32(5), hits.Load(), "five failed pushes hit the server")

	// Breaker open: the 6th segment must not even attempt a request.
	c.handleSegment(context.Background(), seg)
	require.Equal(t, int32(5), hits.Load(), "open breaker must skip the upload entirely")
	require.False(t, c.takePausedSinceFor("default").IsZero(),
		"a failed push must anchor the compensation window")
	c.rearmPausedFor("default", time.Now().Add(-2*time.Minute))

	// Heartbeat still healthy — breaker must stay open regardless.
	c.RecordHeartbeat("", HeartbeatStatus{Status: "healthy"})
	c.handleSegment(context.Background(), seg)
	require.Equal(t, int32(5), hits.Load())

	// Half-open probe succeeds → breaker closes → compensation re-pushes the
	// lost recording (repush routes through handleSegment again).
	fail.Store(false)
	require.Eventually(t, func() bool {
		c.handleSegment(context.Background(), seg)
		return hits.Load() > 5
	}, 3*time.Second, 10*time.Millisecond, "half-open probe must eventually go through")
	require.Eventually(t, func() bool {
		c.instMu.Lock()
		defer c.instMu.Unlock()
		return c.insts["default"].health.PushGate()
	}, 2*time.Second, 5*time.Millisecond, "successful probe must close the breaker")
	// 熔断闭合触发有界补偿:错过的 rec-lost 被补推上去。
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return pushedIDs["rec-lost"] > 0
	}, 3*time.Second, 10*time.Millisecond, "breaker close must compensate missed segments")
}

// TestCoordinatorMjpegDirSegmentSkipped is the #726 guard: MJPEG (directory-
// form) segments can never be uploaded as a single byte stream — pushing them
// just burns a failed request + compensation retry per segment, forever.
func TestCoordinatorMjpegDirSegmentSkipped(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	bus := event.NewEventBus(64)
	cfg := config.VisionConfig{
		Enabled:   true,
		PushMode:  "upload",
		Instances: []config.VisionInstance{{Name: "default", URL: srv.URL}},
	}
	c := NewCoordinator(
		func() config.VisionConfig { return cfg },
		func() string { return t.TempDir() },
		bus, nil, nil,
	)
	require.NoError(t, c.Start(context.Background()))
	defer c.Stop()
	c.RecordHeartbeat("", HeartbeatStatus{Status: "healthy"})

	// Directory-form segment (what MJPEG cameras actually produce).
	dirSeg := filepath.Join(t.TempDir(), "frames-dir")
	require.NoError(t, os.Mkdir(dirSeg, 0o755))

	c.handleSegment(context.Background(), event.SegmentCompleted{
		CameraID: "cam-mjpeg", RecordingID: "rec-dir", FilePath: dirSeg,
		Format: "mjpeg", Layer: model.LayerMain,
	})
	// Labeled-mjpeg segment whose path happens to be a file — still skipped
	// (the consumer cannot decode an MJPEG byte stream anyway).
	fileSeg := filepath.Join(t.TempDir(), "seg.mp4")
	require.NoError(t, os.WriteFile(fileSeg, []byte("x"), 0o600))
	c.handleSegment(context.Background(), event.SegmentCompleted{
		CameraID: "cam-mjpeg", RecordingID: "rec-mjpeg", FilePath: fileSeg,
		Format: "mjpeg", Layer: model.LayerMain,
	})
	// Directory-form segment with a non-mjpeg label — skipped mechanically.
	c.handleSegment(context.Background(), event.SegmentCompleted{
		CameraID: "cam-b", RecordingID: "rec-dir2", FilePath: dirSeg,
		Format: "h264", Layer: model.LayerMain,
	})
	// Sanity: a normal h264 file segment DOES get pushed.
	c.handleSegment(context.Background(), event.SegmentCompleted{
		CameraID: "cam-b", RecordingID: "rec-ok", FilePath: fileSeg,
		Format: "h264", Layer: model.LayerMain,
	})
	require.Equal(t, int32(1), hits.Load(),
		"only the regular h264 segment may be uploaded")
}
