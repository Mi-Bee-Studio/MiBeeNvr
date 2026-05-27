package health

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// newCollector creates a StreamStatsCollector for testing with reasonable defaults.
func newCollector(t *testing.T, windowSize time.Duration) (*StreamStatsCollector, *[]model.HealthEvent) {
	t.Helper()
	var events []model.HealthEvent
	var mu sync.Mutex
	handler := func(cameraID string, event model.HealthEvent) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	}
	c := NewStreamStatsCollector(
		0.5,            // bitrate change threshold (50%)
		5.0,            // min FPS
		30*time.Second, // max IDR interval
		windowSize,
		handler,
	)
	return c, &events
}

// getCollectorEvents safely reads collected events.
func getCollectorEvents(t *testing.T, events *[]model.HealthEvent) []model.HealthEvent {
	t.Helper()
	// events slice is append-only from handler, safe to read
	return *events
}

// makeH264Frame creates an H.264 access unit with the given NAL type and payload size.
func makeH264Frame(t *testing.T, nalType byte, payloadSize int) [][]byte {
	t.Helper()
	// H.264 NAL header: forbidden_zero_bit(1) + nal_ref_idc(2) + nal_unit_type(5)
	nalu := make([]byte, 1+payloadSize)
	nalu[0] = (nalType & 0x1F) // set NAL type, ref_idc=0
	return [][]byte{nalu}
}

// makeH265IDRFrame creates an H.265 IDR access unit.
func makeH265IDRFrame(t *testing.T, nalType byte, payloadSize int) [][]byte {
	t.Helper()
	// H.265 NAL header: forbidden_zero_bit(1) + nal_unit_type(6) + nuh_layer_id(6) + nuh_temporal_id_plus1(3)
	nalu := make([]byte, 2+payloadSize)
	nalu[0] = (nalType << 1) & 0x7E // set NAL type in bits 1-6
	nalu[1] = 1                     // temporal_id_plus1 = 1
	return [][]byte{nalu}
}

func TestCollectorFrameCount(t *testing.T) {
	c, _ := newCollector(t, 5*time.Second)

	cb := c.OnFrame("cam-1")
	camera := "cam-1"

	// Feed 100 frames
	for i := 0; i < 100; i++ {
		au := makeH264Frame(t, 1, 1000) // non-IDR, 1001 bytes
		cb(int64(i), au)
	}

	stats := c.GetStats(camera)
	if stats.FrameCount != 100 {
		t.Errorf("expected frame count 100, got %d", stats.FrameCount)
	}
}

func TestCollectorBitrateCalc(t *testing.T) {
	windowSize := 1 * time.Second
	c, _ := newCollector(t, windowSize)

	cb := c.OnFrame("cam-1")

	// Feed 25 frames, each 10000 bytes total (10 NALUs of 1000 bytes each)
	const frameBytes = 10000
	for i := 0; i < 25; i++ {
		au := make([][]byte, 0, 10)
		for j := 0; j < 10; j++ {
			au = append(au, make([]byte, 1000))
		}
		cb(int64(i), au)
	}

	stats := c.GetStats("cam-1")
	expectedTotalBytes := int64(25 * frameBytes)
	if stats.TotalBytes != expectedTotalBytes {
		t.Errorf("expected total bytes %d, got %d", expectedTotalBytes, stats.TotalBytes)
	}

	// Bitrate = totalBytes * 8 / windowSeconds
	expectedBitrate := float64(expectedTotalBytes*8) / windowSize.Seconds()
	if stats.Bitrate != expectedBitrate {
		t.Errorf("expected bitrate %.2f, got %.2f", expectedBitrate, stats.Bitrate)
	}
}

func TestCollectorFPSCalc(t *testing.T) {
	windowSize := 2 * time.Second
	c, _ := newCollector(t, windowSize)

	cb := c.OnFrame("cam-1")

	// Feed 30 frames
	for i := 0; i < 30; i++ {
		au := makeH264Frame(t, 1, 500)
		cb(int64(i), au)
	}

	stats := c.GetStats("cam-1")
	expectedFPS := float64(30) / windowSize.Seconds()
	if stats.FPS != expectedFPS {
		t.Errorf("expected FPS %.2f, got %.2f", expectedFPS, stats.FPS)
	}
}

func TestCollectorIDRDetection(t *testing.T) {
	c, _ := newCollector(t, 5*time.Second)

	cb := c.OnFrame("cam-1")

	// Feed non-IDR frames
	for i := 0; i < 10; i++ {
		au := makeH264Frame(t, 1, 500) // non-IDR
		cb(int64(i), au)
	}

	// Feed IDR frame (H.264 type 5)
	idrAU := makeH264Frame(t, 5, 500)
	cb(10, idrAU)

	beforeIDR := c.GetStats("cam-1")

	// Feed more non-IDR
	for i := 0; i < 10; i++ {
		au := makeH264Frame(t, 1, 500)
		cb(int64(20+i), au)
	}

	// Feed another IDR
	idrAU2 := makeH264Frame(t, 5, 500)
	cb(30, idrAU2)

	stats := c.GetStats("cam-1")

	// Total frames = 10 + 1 + 10 + 1 = 22
	if stats.FrameCount != 22 {
		t.Errorf("expected 22 frames, got %d", stats.FrameCount)
	}

	// Before second IDR, we should have had 1 IDR
	_ = beforeIDR

	// IDR interval should be very small (just happened)
	if stats.IDRInterval < 0 {
		t.Errorf("IDR interval should be non-negative, got %f", stats.IDRInterval)
	}
}

func TestCollectorH265IDRDetection(t *testing.T) {
	c, _ := newCollector(t, 5*time.Second)

	cb := c.OnFrame("cam-1")

	// Feed H.265 IDR frame (type 19 = IDR_W_RADL)
	idrAU := makeH265IDRFrame(t, 19, 500)
	cb(0, idrAU)

	stats := c.GetStats("cam-1")

	if stats.FrameCount != 1 {
		t.Errorf("expected 1 frame, got %d", stats.FrameCount)
	}
	// IDR interval should be near zero
	if stats.IDRInterval > 1.0 {
		t.Errorf("IDR interval should be small, got %f", stats.IDRInterval)
	}
}

func TestCollectorLowFPSAnomaly(t *testing.T) {
	windowSize := 1 * time.Second
	c, events := newCollector(t, windowSize)

	cb := c.OnFrame("cam-1")

	// First check: feed only 2 frames (below minFPS=5) — streak=1, no emit
	for i := 0; i < 2; i++ {
		au := makeH264Frame(t, 1, 500)
		cb(int64(i), au)
	}
	c.CheckAndReset()

	// Second check: feed 2 more frames — streak=2, should emit
	for i := 0; i < 2; i++ {
		au := makeH264Frame(t, 1, 500)
		cb(int64(2+i), au)
	}
	c.CheckAndReset()

	evts := getCollectorEvents(t, events)
	if len(evts) == 0 {
		t.Fatal("expected at least one anomaly event, got none")
	}

	found := false
	for _, e := range evts {
		if e.EventType == string(model.HealthEventStreamAnomaly) && e.Message == "Low FPS detected" {
			found = true
			if e.CameraID != "cam-1" {
				t.Errorf("expected camera ID cam-1, got %s", e.CameraID)
			}
			if e.Status != string(model.HealthStatusWarning) {
				t.Errorf("expected status warning, got %s", e.Status)
			}
			// Verify metadata contains fps info
			var meta map[string]any
			if err := json.Unmarshal([]byte(e.Metadata), &meta); err != nil {
				t.Fatalf("failed to parse metadata JSON: %v", err)
			}
			if _, ok := meta["fps"]; !ok {
				t.Error("metadata missing fps field")
			}
			if _, ok := meta["threshold"]; !ok {
				t.Error("metadata missing threshold field")
			}
		}
	}
	if !found {
		t.Error("expected Low FPS anomaly event not found")
	}
}

func TestCollectorBitrateAnomaly(t *testing.T) {
	windowSize := 1 * time.Second
	c, events := newCollector(t, windowSize)

	cb := c.OnFrame("cam-1")

	// Window 1: feed 10 frames with small payload → low bitrate
	for i := 0; i < 10; i++ {
		au := makeH264Frame(t, 1, 100) // 101 bytes each
		cb(int64(i), au)
	}
	c.CheckAndReset()

	// Window 2: high bitrate (>50% change) — streak=1, no emit
	for i := 0; i < 10; i++ {
		au := makeH264Frame(t, 1, 10000) // 10001 bytes each
		cb(int64(10+i), au)
	}
	c.CheckAndReset()

	// Window 3: back to low bitrate → another >50% change — streak=2, emit
	for i := 0; i < 10; i++ {
		au := makeH264Frame(t, 1, 100) // 101 bytes each
		cb(int64(20+i), au)
	}
	c.CheckAndReset()

	evts := getCollectorEvents(t, events)
	found := false
	for _, e := range evts {
		if e.EventType == string(model.HealthEventStreamAnomaly) && e.Message == "Bitrate anomaly detected" {
			found = true
			var meta map[string]any
			if err := json.Unmarshal([]byte(e.Metadata), &meta); err != nil {
				t.Fatalf("failed to parse metadata JSON: %v", err)
			}
			if _, ok := meta["bitrate"]; !ok {
				t.Error("metadata missing bitrate field")
			}
			if _, ok := meta["change"]; !ok {
				t.Error("metadata missing change field")
			}
		}
	}
	if !found {
		t.Error("expected Bitrate anomaly event not found")
	}
}

func TestCollectorIDRIntervalAnomaly(t *testing.T) {
	windowSize := 1 * time.Second
	// Use very short max IDR interval to trigger anomaly easily
	c, events := newCollector(t, windowSize)
	c.maxIDRInterval = 1 * time.Millisecond // very short

	cb := c.OnFrame("cam-1")

	// Feed only non-IDR frames
	for i := 0; i < 10; i++ {
		au := makeH264Frame(t, 1, 500)
		cb(int64(i), au)
	}

	// First check: IDR interval exceeded — streak=1, no emit
	time.Sleep(5 * time.Millisecond)
	c.CheckAndReset()

	// Second check: IDR interval still exceeded — streak=2, should emit
	time.Sleep(5 * time.Millisecond)
	c.CheckAndReset()

	evts := getCollectorEvents(t, events)
	found := false
	for _, e := range evts {
		if e.EventType == string(model.HealthEventStreamAnomaly) && e.Message == "IDR interval too long" {
			found = true
		}
	}
	if !found {
		t.Error("expected IDR interval anomaly event not found")
	}
}

func TestCollectorNoBlocking(t *testing.T) {
	c, _ := newCollector(t, 5*time.Second)

	cb := c.OnFrame("cam-1")

	au := makeH264Frame(t, 1, 500)

	// The callback should return in well under 1ms since it only does atomics
	start := time.Now()
	for i := 0; i < 10000; i++ {
		cb(int64(i), au)
	}
	elapsed := time.Since(start)

	// 10k frames should complete in well under 100ms with atomics
	if elapsed > 100*time.Millisecond {
		t.Errorf("callback took too long for 10k frames: %v", elapsed)
	}

	stats := c.GetStats("cam-1")
	if stats.FrameCount != 10000 {
		t.Errorf("expected 10000 frames, got %d", stats.FrameCount)
	}
}

func TestCollectorCheckAndResetCounters(t *testing.T) {
	windowSize := 1 * time.Second
	c, _ := newCollector(t, windowSize)

	cb := c.OnFrame("cam-1")

	// Feed frames
	for i := 0; i < 50; i++ {
		au := makeH264Frame(t, 1, 500)
		cb(int64(i), au)
	}

	// Before reset, frame count should be 50
	stats := c.GetStats("cam-1")
	if stats.FrameCount != 50 {
		t.Errorf("expected 50 frames before reset, got %d", stats.FrameCount)
	}

	// Reset swaps counters to 0
	c.CheckAndReset()

	stats = c.GetStats("cam-1")
	if stats.FrameCount != 0 {
		t.Errorf("expected 0 frames after reset, got %d", stats.FrameCount)
	}
}

func TestCollectorMultipleCameras(t *testing.T) {
	c, _ := newCollector(t, 5*time.Second)

	cb1 := c.OnFrame("cam-1")
	cb2 := c.OnFrame("cam-2")

	for i := 0; i < 10; i++ {
		au := makeH264Frame(t, 1, 500)
		cb1(int64(i), au)
		cb2(int64(i), au)
	}

	stats1 := c.GetStats("cam-1")
	stats2 := c.GetStats("cam-2")

	if stats1.FrameCount != 10 {
		t.Errorf("cam-1: expected 10 frames, got %d", stats1.FrameCount)
	}
	if stats2.FrameCount != 10 {
		t.Errorf("cam-2: expected 10 frames, got %d", stats2.FrameCount)
	}
}

func TestCollectorRemoveCamera(t *testing.T) {
	c, _ := newCollector(t, 5*time.Second)

	_ = c.OnFrame("cam-1")

	c.RemoveCamera("cam-1")

	stats := c.GetStats("cam-1")
	if stats.FrameCount != 0 || stats.Bitrate != 0 {
		t.Errorf("expected zero stats after removal, got %+v", stats)
	}
}

func TestCollectorUnknownCamera(t *testing.T) {
	c, _ := newCollector(t, 5*time.Second)

	stats := c.GetStats("nonexistent")
	if stats.FrameCount != 0 || stats.Bitrate != 0 || stats.FPS != 0 {
		t.Errorf("expected zero stats for unknown camera, got %+v", stats)
	}
}

func TestCollectorNoAnomalyWhenDisabled(t *testing.T) {
	windowSize := 1 * time.Second
	// Set minFPS to 0 — effectively disables FPS anomaly
	c, events := newCollector(t, windowSize)
	c.minFPS = 0

	cb := c.OnFrame("cam-1")

	// Feed only 1 frame (would be low FPS if threshold were > 0)
	for i := 0; i < 1; i++ {
		au := makeH264Frame(t, 1, 500)
		cb(int64(i), au)
	}

	c.CheckAndReset()

	evts := getCollectorEvents(t, events)
	for _, e := range evts {
		if e.Message == "Low FPS detected" {
			t.Error("should not emit low FPS event when minFPS is 0")
		}
	}
}

func TestCollectorResetOnReconnect(t *testing.T) {
	windowSize := 1 * time.Second
	c, _ := newCollector(t, windowSize)

	cb := c.OnFrame("cam-1")

	// Feed frames including an IDR frame to set lastIDRTime
	for i := 0; i < 10; i++ {
		au := makeH264Frame(t, 1, 500) // non-IDR
		cb(int64(i), au)
	}
	// Feed IDR frame
	idrAU := makeH264Frame(t, 5, 500)
	cb(10, idrAU)

	// Set prevBitrate for this camera to simulate prior window
	c.mu.Lock()
	c.prevBitrate["cam-1"] = 1000.0
	c.mu.Unlock()

	// Record the old lastIDRTime
	oldStats := c.GetStats("cam-1")
	oldIDRTime := oldStats.LastIDRTime

	// Wait a moment so we can verify reset changed the time
	time.Sleep(2 * time.Millisecond)

	// Reset camera state (simulates reconnect)
	c.ResetCameraState("cam-1")

	// Verify lastIDRTime is fresh (reset to ~now)
	newStats := c.GetStats("cam-1")
	if newStats.LastIDRTime.Before(oldIDRTime) || newStats.LastIDRTime.Equal(oldIDRTime) {
		t.Error("expected lastIDRTime to be reset to current time after reset")
	}

	// Verify prevBitrate entry is deleted
	c.mu.Lock()
	_, hasPrev := c.prevBitrate["cam-1"]
	c.mu.Unlock()
	if hasPrev {
		t.Error("expected prevBitrate entry to be deleted after reset")
	}

	// Verify atomic counters are reset
	if newStats.FrameCount != 0 {
		t.Errorf("expected frame count 0 after reset, got %d", newStats.FrameCount)
	}
	if newStats.TotalBytes != 0 {
		t.Errorf("expected byte count 0 after reset, got %d", newStats.TotalBytes)
	}
}

func TestCollectorResetPreventsFalseIDRAlert(t *testing.T) {
	windowSize := 1 * time.Second
	c, events := newCollector(t, windowSize)
	c.maxIDRInterval = 1 * time.Millisecond // very short threshold

	cb := c.OnFrame("cam-1")

	// Feed only non-IDR frames (lastIDRTime from init)
	for i := 0; i < 5; i++ {
		au := makeH264Frame(t, 1, 500)
		cb(int64(i), au)
	}

	// Wait for IDR interval to exceed threshold
	time.Sleep(5 * time.Millisecond)

	// First check: IDR interval exceeded — streak=1
	c.CheckAndReset()

	// Second check: still exceeded — streak=2, emit event
	time.Sleep(5 * time.Millisecond)
	c.CheckAndReset()

	preResetCount := len(getCollectorEvents(t, events))
	if preResetCount == 0 {
		t.Fatal("expected at least one event (IDR alert) before reset")
	}

	// Reset camera state — refreshes lastIDRTime to now
	c.ResetCameraState("cam-1")

	// CheckAndReset again — should NOT emit new IDR alert
	c.CheckAndReset()

	// Event count should be unchanged (no new events after reset)
	postResetCount := len(getCollectorEvents(t, events))
	if postResetCount != preResetCount {
		t.Errorf("expected no new events after reset, got %d new events", postResetCount-preResetCount)
	}
}

func TestCollectorDebounceSuppressesSingleCheck(t *testing.T) {
	t.Helper()
	windowSize := 1 * time.Second
	c, events := newCollector(t, windowSize)

	cb := c.OnFrame("cam-1")

	// Feed only 2 frames in the window (below minFPS=5)
	for i := 0; i < 2; i++ {
		au := makeH264Frame(t, 1, 500)
		cb(int64(i), au)
	}

	// Single CheckAndReset — should NOT emit (needs 2 consecutive)
	c.CheckAndReset()

	evts := getCollectorEvents(t, events)
	for _, e := range evts {
		if e.Message == "Low FPS detected" {
			t.Error("should not emit low FPS event after only one anomalous check")
		}
	}
}

func TestCollectorDebounceResetsOnRecovery(t *testing.T) {
	t.Helper()
	windowSize := 1 * time.Second
	c, events := newCollector(t, windowSize)

	cb := c.OnFrame("cam-1")

	// First check: low FPS — streak=1
	for i := 0; i < 2; i++ {
		au := makeH264Frame(t, 1, 500)
		cb(int64(i), au)
	}
	c.CheckAndReset()

	// Second check: normal FPS — resets streak
	for i := 0; i < 20; i++ {
		au := makeH264Frame(t, 1, 500)
		cb(int64(2+i), au)
	}
	c.CheckAndReset()

	// Third check: low FPS again — streak=1 (reset happened)
	for i := 0; i < 2; i++ {
		au := makeH264Frame(t, 1, 500)
		cb(int64(22+i), au)
	}
	c.CheckAndReset()

	evts := getCollectorEvents(t, events)
	for _, e := range evts {
		if e.Message == "Low FPS detected" {
			t.Error("should not emit low FPS event — streak was reset by recovery")
		}
	}
}

func TestCollectorDebounceResetOnReconnect(t *testing.T) {
	t.Helper()
	windowSize := 1 * time.Second
	c, events := newCollector(t, windowSize)
	c.maxIDRInterval = 1 * time.Millisecond

	cb := c.OnFrame("cam-1")

	// First check: IDR interval exceeded — streak=1
	for i := 0; i < 5; i++ {
		au := makeH264Frame(t, 1, 500)
		cb(int64(i), au)
	}
	time.Sleep(5 * time.Millisecond)
	c.CheckAndReset()

	// Reset camera state (reconnect) — clears debounce
	c.ResetCameraState("cam-1")

	// Feed frames again, still no IDR
	for i := 0; i < 5; i++ {
		au := makeH264Frame(t, 1, 500)
		cb(int64(5+i), au)
	}
	time.Sleep(5 * time.Millisecond)
	c.CheckAndReset()

	// Only 1 check after reconnect — no event expected
	evts := getCollectorEvents(t, events)
	for _, e := range evts {
		if e.Message == "IDR interval too long" {
			t.Error("should not emit IDR event — debounce reset on reconnect, only 1 check since")
		}
	}
}
