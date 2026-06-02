package health

import (
	"math"
	"testing"
	"time"
)

func newTestTracker(window time.Duration) *QualityTracker {
	return NewQualityTracker(window)
}

// TestQualityTracker_RecordUptime simulates 23h recording + 1h offline → uptime ≈ 95.8%
func TestQualityTracker_RecordUptime(t *testing.T) {
	t.Parallel()
	now := time.Now()
	q := &QualityTracker{
		cameras: make(map[string]*cameraSession),
		window:  24 * time.Hour,
		nowFunc: func() time.Time { return now },
	}

	// Camera goes online at t=0
	now = now.Add(0)
	q.OnOnline("cam1")

	// After 23h of being online, camera goes offline
	now = now.Add(23 * time.Hour)
	q.OnOffline("cam1")

	// Compute quality at the end of 24h window
	now = now.Add(1 * time.Hour)
	quality := q.GetQuality("cam1")

	// uptime = 23h / 24h * 100 ≈ 95.83%
	expected := 23.0 / 24.0 * 100.0
	if math.Abs(quality.UptimePercent-expected) > 0.1 {
		t.Errorf("expected uptime ≈ %.2f%%, got %.2f%%", expected, quality.UptimePercent)
	}

	if quality.CurrentStatus != "offline" {
		t.Errorf("expected status 'offline', got %q", quality.CurrentStatus)
	}
}

// TestQualityTracker_RecordFailure simulates 3 failures → TotalFailures=3
func TestQualityTracker_RecordFailure(t *testing.T) {
	t.Parallel()
	now := time.Now()
	q := &QualityTracker{
		cameras: make(map[string]*cameraSession),
		window:  24 * time.Hour,
		nowFunc: func() time.Time { return now },
	}

	// Cycle 1: online → offline
	q.OnOnline("cam1")
	now = now.Add(1 * time.Hour)
	q.OnOffline("cam1")

	// Cycle 2: online → offline
	now = now.Add(10 * time.Minute)
	q.OnOnline("cam1")
	now = now.Add(2 * time.Hour)
	q.OnOffline("cam1")

	// Cycle 3: online → offline
	now = now.Add(10 * time.Minute)
	q.OnOnline("cam1")
	now = now.Add(3 * time.Hour)
	q.OnOffline("cam1")

	quality := q.GetQuality("cam1")

	if quality.TotalFailures != 3 {
		t.Errorf("expected 3 failures, got %d", quality.TotalFailures)
	}

	if quality.LastFailure.IsZero() {
		t.Error("expected LastFailure to be set")
	}
}

// TestQualityTracker_MTBF simulates failures at t=0, t=2h, t=4h → MTBF ≈ 2h
func TestQualityTracker_MTBF(t *testing.T) {
	t.Parallel()
	now := time.Now()
	q := &QualityTracker{
		cameras: make(map[string]*cameraSession),
		window:  24 * time.Hour,
		nowFunc: func() time.Time { return now },
	}

	// Failure at t=0
	q.OnOnline("cam1")
	q.OnOffline("cam1")

	// Failure at t=2h
	now = now.Add(2 * time.Hour)
	q.OnOnline("cam1")
	q.OnOffline("cam1")

	// Failure at t=4h
	now = now.Add(2 * time.Hour)
	q.OnOnline("cam1")
	q.OnOffline("cam1")

	quality := q.GetQuality("cam1")

	// 3 failures → MTBF = total_time / (failures - 1) = 4h / 2 = 2h
	expectedMTBF := 2 * time.Hour
	tolerance := 1 * time.Second
	if quality.MTBF < expectedMTBF-tolerance || quality.MTBF > expectedMTBF+tolerance {
		t.Errorf("expected MTBF ≈ %v, got %v", expectedMTBF, quality.MTBF)
	}
}

// TestQualityTracker_SessionTracking verifies online/offline durations are tracked
func TestQualityTracker_SessionTracking(t *testing.T) {
	t.Parallel()
	now := time.Now()
	q := &QualityTracker{
		cameras: make(map[string]*cameraSession),
		window:  24 * time.Hour,
		nowFunc: func() time.Time { return now },
	}

	// Session 1: 5h online
	q.OnOnline("cam1")
	now = now.Add(5 * time.Hour)
	q.OnOffline("cam1")

	// Offline gap: 30min
	now = now.Add(30 * time.Minute)

	// Session 2: 3h online
	q.OnOnline("cam1")
	now = now.Add(3 * time.Hour)
	q.OnOffline("cam1")

	// Offline gap: 15min
	now = now.Add(15 * time.Minute)

	// Session 3 (still active): 2h so far
	q.OnOnline("cam1")
	now = now.Add(2 * time.Hour)

	quality := q.GetQuality("cam1")

	// Total online: 5h + 3h + 2h = 10h
	// Window starts at first failure, window duration covers the entire span
	// Average session = 10h / 3 sessions
	expectedAvg := (5*time.Hour + 3*time.Hour + 2*time.Hour) / 3
	tolerance := 1 * time.Second
	if quality.AvgSessionDuration < expectedAvg-tolerance || quality.AvgSessionDuration > expectedAvg+tolerance {
		t.Errorf("expected avg session ≈ %v, got %v", expectedAvg, quality.AvgSessionDuration)
	}

	if quality.CurrentStatus != "online" {
		t.Errorf("expected status 'online', got %q", quality.CurrentStatus)
	}

	if quality.TotalFailures != 2 {
		t.Errorf("expected 2 failures (from 2 offline events), got %d", quality.TotalFailures)
	}
}

// TestQualityTracker_GetAllQuality returns metrics for all tracked cameras
func TestQualityTracker_GetAllQuality(t *testing.T) {
	t.Parallel()
	now := time.Now()
	q := &QualityTracker{
		cameras: make(map[string]*cameraSession),
		window:  24 * time.Hour,
		nowFunc: func() time.Time { return now },
	}

	q.OnOnline("cam1")
	q.OnOnline("cam2")

	now = now.Add(1 * time.Hour)
	q.OnOffline("cam1")

	all := q.GetAllQuality()
	if len(all) != 2 {
		t.Fatalf("expected 2 cameras, got %d", len(all))
	}

	if _, ok := all["cam1"]; !ok {
		t.Error("expected cam1 in results")
	}
	if _, ok := all["cam2"]; !ok {
		t.Error("expected cam2 in results")
	}
}

// TestQualityTracker_RemoveCamera clears tracking for a camera
func TestQualityTracker_RemoveCamera(t *testing.T) {
	t.Parallel()
	q := newTestTracker(24 * time.Hour)

	q.OnOnline("cam1")
	q.RemoveCamera("cam1")

	quality := q.GetQuality("cam1")
	if quality.TotalFailures != 0 {
		t.Errorf("expected 0 failures after removal, got %d", quality.TotalFailures)
	}
}

// TestQualityTracker_MTBFZeroFailures returns 0 MTBF when <2 failures
func TestQualityTracker_MTBFZeroFailures(t *testing.T) {
	t.Parallel()
	now := time.Now()
	q := &QualityTracker{
		cameras: make(map[string]*cameraSession),
		window:  24 * time.Hour,
		nowFunc: func() time.Time { return now },
	}

	// Only 1 failure — not enough for MTBF
	q.OnOnline("cam1")
	q.OnOffline("cam1")

	quality := q.GetQuality("cam1")
	if quality.MTBF != 0 {
		t.Errorf("expected MTBF=0 with <2 failures, got %v", quality.MTBF)
	}
}

// TestQualityTracker_WindowExpiry prunes sessions outside the rolling window
func TestQualityTracker_WindowExpiry(t *testing.T) {
	t.Parallel()
	now := time.Now()
	q := &QualityTracker{
		cameras: make(map[string]*cameraSession),
		window:  2 * time.Hour,
		nowFunc: func() time.Time { return now },
	}

	// Old session: 3h ago → outside 2h window
	q.OnOnline("cam1")
	now = now.Add(1 * time.Hour)
	q.OnOffline("cam1") // failure 1

	// Move 3h forward — old session is outside window
	now = now.Add(3 * time.Hour)

	quality := q.GetQuality("cam1")

	// Old session should be pruned, so 0 failures in window
	if quality.TotalFailures != 0 {
		t.Errorf("expected 0 failures after window expiry, got %d", quality.TotalFailures)
	}
}

// TestQualityTracker_NewQualityTrackerDefaults verifies constructor
func TestQualityTracker_NewQualityTrackerDefaults(t *testing.T) {
	t.Parallel()
	q := NewQualityTracker(12 * time.Hour)
	if q == nil {
		t.Fatal("expected non-nil tracker")
	}
	if q.window != 12*time.Hour {
		t.Errorf("expected 12h window, got %v", q.window)
	}
	if q.cameras == nil {
		t.Error("expected initialized cameras map")
	}
}
