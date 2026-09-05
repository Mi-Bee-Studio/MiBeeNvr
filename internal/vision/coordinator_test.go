package vision

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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
		nil, // no sub-layer provider
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
		nil, // no sub-layer provider
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

// skip_cameras: cameras the config excludes (e.g. encodings the external
// consumer cannot process) must never be pushed — neither live nor via the
// offline-compensation path (handleSegment is shared).
func TestCoordinatorSkipCameras(t *testing.T) {
	var mu sync.Mutex
	pushed := map[string]int{}
	visionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		pushed[r.Header.Get("X-Recording-Id")]++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer visionSrv.Close()

	bus := event.NewEventBus(64)
	cfg := config.VisionConfig{Enabled: true, URL: visionSrv.URL, SkipCameras: []string{"cam-skip"}}
	c := NewCoordinator(
		func() config.VisionConfig { return cfg },
		func() string { return t.TempDir() },
		bus,
		nil,
		nil, // no sub-layer provider
	)
	require.NoError(t, c.Start(context.Background()))
	t.Cleanup(c.Stop)
	// Healthy heartbeat → live pushes flow.
	c.Health().RecordHeartbeat(HeartbeatStatus{Status: "healthy"})

	dir := t.TempDir()
	publish := func(cam, rec string) {
		p := filepath.Join(dir, rec+".mp4")
		require.NoError(t, os.WriteFile(p, make([]byte, 8), 0o644))
		bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
			CameraID: cam, FilePath: p, Format: "mp4",
			FileSize: 8, RecordingID: rec,
		})
	}
	publish("cam-skip", "rec-skipped")
	publish("cam-ok", "rec-delivered")

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return pushed["rec-delivered"] == 1
	}, 5*time.Second, 50*time.Millisecond, "non-skipped camera must be pushed")

	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	require.Zero(t, pushed["rec-skipped"], "skip_cameras camera must not be pushed")
	require.Equal(t, 1, pushed["rec-delivered"])
}

// #515: a consumer-reported skip list (heartbeat field) must stop pushes for
// those cameras without any NVR-side config; clearing the list (or an old
// consumer never sending it) restores full push.
func TestCoordinatorHeartbeatSkipCameras(t *testing.T) {
	var mu sync.Mutex
	pushed := map[string]int{}
	visionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		pushed[r.Header.Get("X-Recording-Id")]++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer visionSrv.Close()

	bus := event.NewEventBus(64)
	cfg := config.VisionConfig{Enabled: true, URL: visionSrv.URL}
	c := NewCoordinator(
		func() config.VisionConfig { return cfg },
		func() string { return t.TempDir() },
		bus,
		nil,
		nil, // no sub-layer provider
	)
	require.NoError(t, c.Start(context.Background()))
	t.Cleanup(c.Stop)

	// Heartbeat declares a skip list BEFORE any segment flows.
	c.Health().RecordHeartbeat(HeartbeatStatus{Status: "healthy", SkipCameras: []string{"cam-hbskip"}})

	dir := t.TempDir()
	publish := func(cam, rec string) {
		p := filepath.Join(dir, rec+".mp4")
		require.NoError(t, os.WriteFile(p, make([]byte, 8), 0o644))
		bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
			CameraID: cam, FilePath: p, Format: "mp4",
			FileSize: 8, RecordingID: rec,
		})
	}
	publish("cam-hbskip", "rec-hb-skipped")
	publish("cam-ok", "rec-hb-delivered")

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return pushed["rec-hb-delivered"] == 1
	}, 5*time.Second, 50*time.Millisecond, "non-skipped camera must be pushed")

	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	require.Zero(t, pushed["rec-hb-skipped"], "heartbeat-reported skip camera must not be pushed")
	mu.Unlock()

	// Consumer clears the list → pushes resume for the same camera.
	c.Health().RecordHeartbeat(HeartbeatStatus{Status: "healthy"})
	publish("cam-hbskip", "rec-hb-resumed")
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return pushed["rec-hb-resumed"] == 1
	}, 5*time.Second, 50*time.Millisecond, "cleared skip list must resume pushes")

	// SkipCamera semantics without a heartbeat ever received: no skips.
	require.False(t, NewHealthTracker(60).SkipCamera("any"))
}

// #637 tiered cameras: tierrec layer=1 sub segments are the analysis input —
// pushed with X-Layer: sub; layer=0 main segments yield. Non-tiered cameras'
// sub segments (should not exist today, defensive) are dropped.
func TestCoordinatorTieredCamerasLayerRouting(t *testing.T) {
	var mu sync.Mutex
	pushed := map[string]string{} // recording id -> X-Layer header
	visionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		pushed[r.Header.Get("X-Recording-Id")] = r.Header.Get("X-Layer")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer visionSrv.Close()

	bus := event.NewEventBus(64)
	cfg := config.VisionConfig{
		Enabled:       true,
		URL:           visionSrv.URL,
		TieredCameras: []string{"cam-tiered"},
	}
	c := NewCoordinator(
		func() config.VisionConfig { return cfg },
		func() string { return t.TempDir() },
		bus,
		nil,
		nil, // no sub-layer provider
	)
	require.NoError(t, c.Start(context.Background()))
	t.Cleanup(c.Stop)
	c.Health().RecordHeartbeat(HeartbeatStatus{Status: "healthy"})

	dir := t.TempDir()
	publish := func(cam, rec string, layer int) {
		p := filepath.Join(dir, rec+".mp4")
		require.NoError(t, os.WriteFile(p, make([]byte, 8), 0o644))
		bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
			CameraID: cam, FilePath: p, Format: "mp4",
			FileSize: 8, RecordingID: rec, Layer: layer,
		})
	}
	publish("cam-tiered", "rec-sub", model.LayerSub)
	publish("cam-tiered", "rec-main", model.LayerMain)
	publish("cam-other", "rec-other-sub", model.LayerSub)
	publish("cam-other", "rec-other-main", model.LayerMain)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return pushed["rec-sub"] == "sub" && pushed["rec-other-main"] == ""
	}, 5*time.Second, 50*time.Millisecond, "sub of tiered + main of non-tiered must be pushed")

	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, "sub", pushed["rec-sub"], "tiered camera's sub segment must carry X-Layer: sub")
	require.NotContains(t, pushed, "rec-main", "tiered camera's main segment must yield")
	require.NotContains(t, pushed, "rec-other-sub", "non-tiered camera's sub segment must be dropped")
	require.Equal(t, "", pushed["rec-other-main"], "non-tiered main pushes without X-Layer")
}

// 2026-09 Jetson incident: a wedged worker keeps heartbeating "degraded" —
// the NVR must treat that as unhealthy (pause pushes) and fire the recovery
// compensation when the status flips back to healthy.
func TestHealthTrackerDegradedHeartbeat(t *testing.T) {
	h := NewHealthTracker(60)
	var fired atomic.Int32
	h.SetOnRecovery(func() { fired.Add(1) })

	h.RecordHeartbeat(HeartbeatStatus{Status: "healthy"})
	require.True(t, h.IsHealthy())
	require.Eventually(t, func() bool { return fired.Load() == 1 },
		2*time.Second, 20*time.Millisecond)

	// Worker wedges: fresh heartbeats, degraded status.
	h.RecordHeartbeat(HeartbeatStatus{Status: "degraded", QueueDepth: 64})
	require.False(t, h.IsHealthy(), "degraded heartbeat must read unhealthy")
	healthy, _, st := h.Snapshot()
	require.False(t, healthy)
	require.Equal(t, "degraded", st.Status)

	// Worker recovers: the degraded→healthy transition must fire recovery
	// (compensates the paused window).
	h.RecordHeartbeat(HeartbeatStatus{Status: "healthy"})
	require.True(t, h.IsHealthy())
	require.Eventually(t, func() bool { return fired.Load() == 2 },
		2*time.Second, 20*time.Millisecond, "degraded→healthy must fire recovery")
}

// TestCoordinatorUploadContentLengthMatchesFile reproduces the 2026-09-02
// field bug: tierrec's event FileSize counts media bytes only (the muxer
// appends a ~10 KB moov at close), and a Content-Length short of the real
// body aborted every tiered push client-side ("ContentLength=X with Body
// length Y", 530 consecutive losses). The upload must stat the file and use
// the on-disk size for both Content-Length and X-File-Size.
func TestCoordinatorUploadContentLengthMatchesFile(t *testing.T) {
	var got struct {
		contentLength int64
		headerSize    string
		bodyLen       int64
	}
	visionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.contentLength = r.ContentLength
		got.headerSize = r.Header.Get("X-File-Size")
		buf, _ := io.ReadAll(r.Body)
		got.bodyLen = int64(len(buf))
		w.WriteHeader(http.StatusOK)
	}))
	defer visionSrv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "seg.mp4")
	payload := bytes.Repeat([]byte{0xAB}, 40338) // media bytes + a fat 10338-byte "moov"
	require.NoError(t, os.WriteFile(path, payload, 0o644))

	c := NewCoordinator(
		func() config.VisionConfig { return config.VisionConfig{Enabled: true, URL: visionSrv.URL} },
		func() string { return dir },
		event.NewEventBus(8),
		nil, nil,
	)
	// Deliberately lie in the event-derived size AND header, as tierrec did.
	ok := c.uploadSegment(context.Background(), visionSrv.URL, path, 30000, map[string]string{
		"X-File-Size": "30000",
		"X-Camera-Id": "cam-x",
	})
	require.True(t, ok)
	require.Equal(t, int64(len(payload)), got.contentLength, "Content-Length must be the on-disk size")
	require.Equal(t, int64(len(payload)), got.bodyLen, "server must receive the whole file")
	require.Equal(t, strconv.Itoa(len(payload)), got.headerSize, "X-File-Size must be corrected to the real size")
}

// ---- 多实例(vision.instances)----

// 双实例扇出:一台相机(未配 vision_targets)的段推给全部启用实例;
// 显式配置 vision_targets 的相机只推指定实例;一台实例离线不影响另一台。
func TestCoordinatorMultiInstanceFanout(t *testing.T) {
	var mu sync.Mutex
	got := map[string]map[string]int{} // server -> recording id -> count
	mkSrv := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			if got[name] == nil {
				got[name] = map[string]int{}
			}
			got[name][r.Header.Get("X-Recording-Id")]++
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		}))
	}
	srvA, srvB := mkSrv("a"), mkSrv("b")
	defer srvA.Close()
	defer srvB.Close()

	enabled := true
	bus := event.NewEventBus(64)
	cfg := config.VisionConfig{
		Enabled: true,
		Instances: []config.VisionInstance{
			{Name: "a", URL: srvA.URL, APIKeyName: "key-a"},
			{Name: "b", URL: srvB.URL, APIKeyName: "key-b", Enabled: &enabled},
		},
	}
	c := NewCoordinator(
		func() config.VisionConfig { return cfg },
		func() string { return t.TempDir() },
		bus,
		nil,
		nil,
	)
	targets := map[string][]string{"cam-pinned": {"b"}}
	c.SetCameraTargets(func(cameraID string) []string { return targets[cameraID] })
	require.NoError(t, c.Start(context.Background()))
	t.Cleanup(c.Stop)

	// 心跳按 API key 名归因:两条 key 分别落到各自实例。
	require.Equal(t, "a", c.RecordHeartbeat("key-a", HeartbeatStatus{Status: "healthy"}))
	require.Equal(t, "b", c.RecordHeartbeat("key-b", HeartbeatStatus{Status: "healthy"}))

	dir := t.TempDir()
	publish := func(cam, rec string) {
		p := filepath.Join(dir, rec+".mp4")
		require.NoError(t, os.WriteFile(p, make([]byte, 8), 0o644))
		bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
			CameraID: cam, FilePath: p, Format: "mp4",
			FileSize: 8, RecordingID: rec,
		})
	}
	publish("cam-broadcast", "rec-broadcast")
	publish("cam-pinned", "rec-pinned")

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return got["a"]["rec-broadcast"] == 1 && got["b"]["rec-broadcast"] == 1 &&
			got["a"]["rec-pinned"] == 0 && got["b"]["rec-pinned"] == 1
	}, 5*time.Second, 50*time.Millisecond, "broadcast must reach both, pinned only b")

	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 1, got["a"]["rec-broadcast"], "no duplicates to a")
	require.Equal(t, 1, got["b"]["rec-broadcast"], "no duplicates to b")
	require.NotContains(t, got["a"], "rec-pinned", "pinned camera must not reach a")
}

// 单实例故障隔离:b 离线(无心跳)时段只到 a;b 的暂停窗记账,恢复后补推。
func TestCoordinatorMultiInstanceIsolation(t *testing.T) {
	var mu sync.Mutex
	count := map[string]int{}
	mkSrv := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			count[name]++
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		}))
	}
	srvA, srvB := mkSrv("a"), mkSrv("b")
	defer srvA.Close()
	defer srvB.Close()

	bus := event.NewEventBus(64)
	cfg := config.VisionConfig{
		Enabled: true,
		Instances: []config.VisionInstance{
			{Name: "a", URL: srvA.URL},
			{Name: "b", URL: srvB.URL},
		},
	}
	c := NewCoordinator(
		func() config.VisionConfig { return cfg },
		func() string { return t.TempDir() },
		bus,
		nil,
		nil,
	)
	require.NoError(t, c.Start(context.Background()))
	t.Cleanup(c.Stop)

	// 只有 a 健康(匿名心跳落 default=第一个实例)。
	require.Equal(t, "a", c.RecordHeartbeat("", HeartbeatStatus{Status: "healthy"}))

	dir := t.TempDir()
	p := filepath.Join(dir, "seg.mp4")
	require.NoError(t, os.WriteFile(p, make([]byte, 8), 0o644))
	bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
		CameraID: "cam1", FilePath: p, Format: "mp4",
		FileSize: 8, RecordingID: "rec-iso",
	})

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return count["a"] == 1
	}, 5*time.Second, 50*time.Millisecond, "healthy instance a must receive the push")

	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	require.Zero(t, count["b"], "offline instance b must not be attempted")
	mu.Unlock()

	// 匿名心跳归因 default(第一个实例)——旧行为兼容。
	// 指名 key 归因未配置 key 的实例也落 default。
	require.Equal(t, "a", c.RecordHeartbeat("unknown-key", HeartbeatStatus{Status: "healthy"}))
}

// InstancesStatus 按配置顺序展开,含健康与 last_seen。
func TestCoordinatorInstancesStatus(t *testing.T) {
	cfg := config.VisionConfig{
		Enabled: true,
		URL:     "http://jetson:9091", // legacy → default
	}
	c := NewCoordinator(
		func() config.VisionConfig { return cfg },
		func() string { return t.TempDir() },
		event.NewEventBus(8),
		nil, nil,
	)
	st := c.InstancesStatus()
	require.Len(t, st, 1)
	require.Equal(t, "default", st[0].Name)
	require.False(t, st[0].Healthy)
	require.Nil(t, st[0].LastSeen, "no heartbeat yet — omit zero time")

	c.RecordHeartbeat("", HeartbeatStatus{Status: "healthy", Device: "cuda"})
	st = c.InstancesStatus()
	require.True(t, st[0].Healthy)
	require.NotNil(t, st[0].LastSeen)
	require.Equal(t, "cuda", st[0].Device)
}
