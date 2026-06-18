package livetranscode

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Mock helpers
// ---------------------------------------------------------------------------

// mockTranscoder implements Transcoder for testing. It signals crash via
// the done channel optionally after a delay.
type mockTranscoder struct {
	t       *testing.T
	startFn func(ctx context.Context) error
	stopFn  func() error

	mu   sync.Mutex
	done chan struct{} // closed when the "process" exits

	started    bool
	encoder    string
	crashed    bool
	crashAfter time.Duration
}

// newMockTranscoder creates a mock that runs successfully until Stop() or crash.
func newMockTranscoder(t *testing.T) *mockTranscoder {
	t.Helper()
	return &mockTranscoder{
		t:       t,
		done:    make(chan struct{}),
		encoder: "mock_encoder",
	}
}

// newCrashingMock creates a mock that "crashes" (closes done) after crashDelay.
func newCrashingMock(t *testing.T, crashAfter time.Duration) *mockTranscoder {
	t.Helper()
	m := newMockTranscoder(t)
	m.crashed = true
	m.crashAfter = crashAfter
	return m
}

func (m *mockTranscoder) Start(ctx context.Context) error {
	m.mu.Lock()
	m.started = true
	done := m.done
	crashAfter := m.crashAfter
	m.mu.Unlock()

	if crashAfter > 0 {
		go func() {
			select {
			case <-time.After(crashAfter):
				close(done)
			case <-ctx.Done():
			}
		}()
	}
	return nil
}

func (m *mockTranscoder) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.started {
		return nil
	}
	select {
	case <-m.done:
		// Already closed
	default:
		close(m.done)
	}
	m.started = false
	return nil
}

func (m *mockTranscoder) Done() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.done
}

func (m *mockTranscoder) EncoderName() string {
	return m.encoder
}

// ---------------------------------------------------------------------------
// ThermalMonitor tests
// ---------------------------------------------------------------------------

// setupMockZones creates temporary thermal zone files in a temp directory.
// Returns the directory path and a cleanup function.
// Each zone file contains a temperature in millidegrees Celsius.
func setupMockZones(t *testing.T, zones map[string]int) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	var paths []string
	for name, millideg := range zones {
		zoneDir := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(zoneDir, 0755))
		path := filepath.Join(zoneDir, "temp")
		require.NoError(t, os.WriteFile(path, []byte(formatMillideg(millideg)), 0644))
		paths = append(paths, path)
	}
	return dir, paths
}

// formatMillideg formats an integer as a millidegree Celsius string
// (e.g. 85000 = "85000\n").
func formatMillideg(v int) string {
	return string(append(formatMillidegBytes(v), '\n'))
}

func formatMillidegBytes(v int) []byte {
	if v == 0 {
		return []byte{'0'}
	}
	var buf [16]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return buf[i:]
}

func TestThermalMonitor_ReadSingleZone(t *testing.T) {
	_, paths := setupMockZones(t, map[string]int{"zone0": 55000}) // 55°C
	m := NewThermalMonitorWithZones(85, paths)
	m.SetCheckInterval(100 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch := m.Start(ctx)

	assert.Equal(t, 85, m.ThermalLimit())
	assert.Equal(t, 95, m.ShutdownLimit())
	assert.Equal(t, paths, m.ZonePaths())

	// No event expected at 55°C (below throttle threshold)
	select {
	case evt := <-ch:
		t.Fatalf("unexpected event at 55°C: %+v", evt)
	case <-time.After(100 * time.Millisecond):
		// OK — no event
	}

	// Update the zone to 86°C (above throttle, below shutdown)
	require.NoError(t, os.WriteFile(paths[0], []byte("86000\n"), 0644))

	// Wait for throttle event
	select {
	case evt := <-ch:
		assert.Equal(t, ThermalEventThrottle, evt.Kind)
		assert.Equal(t, 86, evt.Temperature)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for throttle event")
	}
}

func TestThermalMonitor_ThrottleEvent(t *testing.T) {
	_, paths := setupMockZones(t, map[string]int{"zone0": 85000}) // 85°C = exactly at limit
	m := NewThermalMonitorWithZones(85, paths)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch := m.Start(ctx)

	select {
	case evt := <-ch:
		assert.Equal(t, ThermalEventThrottle, evt.Kind)
		assert.Equal(t, 85, evt.Temperature)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for throttle event (85°C)")
	}
}

func TestThermalMonitor_ShutdownEvent(t *testing.T) {
	_, paths := setupMockZones(t, map[string]int{"zone0": 95000}) // 95°C
	m := NewThermalMonitorWithZones(85, paths)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch := m.Start(ctx)

	select {
	case evt := <-ch:
		assert.Equal(t, ThermalEventShutdown, evt.Kind)
		assert.Equal(t, 95, evt.Temperature)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for shutdown event (95°C)")
	}

	// Channel should be closed after shutdown
	_, ok := <-ch
	assert.False(t, ok, "channel should be closed after shutdown")
}

func TestThermalMonitor_NoZones(t *testing.T) {
	t.Run("no zone paths at construction", func(t *testing.T) {
		// Use the real constructor which scans /sys — on CI/x86 there may be
		// no zones, which is the expected path.
		m := NewThermalMonitor(85)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		ch := m.Start(ctx)
		// Channel should not receive events (no zones). With real /sys/thermal
		// zones on some machines, the channel may be open (no events) or closed
		// if zones exist. Verify no unexpected events.
		select {
		case evt, ok := <-ch:
			if ok {
				t.Fatalf("unexpected event: %+v", evt)
			}
			// Channel closed (may happen if /sys has zones)
		case <-time.After(50 * time.Millisecond):
			// Expected — no events when no zones
		}
	})

	t.Run("explicit empty zones", func(t *testing.T) {
		m := NewThermalMonitorWithZones(85, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		ch := m.Start(ctx)
		// No zones, channel stays open but never receives events.
		select {
		case evt, ok := <-ch:
			if ok {
				t.Fatalf("unexpected event with nil zones: %+v", evt)
			}
			// Channel closed
		case <-time.After(50 * time.Millisecond):
			// Expected — no events
		}
	})
}
func TestThermalMonitor_HighestTempWins(t *testing.T) {
	_, paths := setupMockZones(t, map[string]int{
		"zone0": 70000, // 70°C
		"zone1": 88000, // 88°C — highest, triggers throttle
		"zone2": 82000, // 82°C
	})
	m := NewThermalMonitorWithZones(85, paths)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch := m.Start(ctx)

	select {
	case evt := <-ch:
		assert.Equal(t, ThermalEventThrottle, evt.Kind)
		assert.Equal(t, 88, evt.Temperature, "should report highest temperature")
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for throttle event (88°C)")
	}
}

func TestThermalMonitor_Stop(t *testing.T) {
	_, paths := setupMockZones(t, map[string]int{"zone0": 55000})
	m := NewThermalMonitorWithZones(85, paths)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch := m.Start(ctx)
	m.Stop()

	// Channel should be closed after Stop
	_, ok := <-ch
	assert.False(t, ok, "channel should be closed after Stop")
}

func TestThermalMonitor_DefaultLimits(t *testing.T) {
	m := NewThermalMonitor(0) // zero should use defaults
	assert.Equal(t, 85, m.ThermalLimit())
	assert.Equal(t, 95, m.ShutdownLimit())
}

func TestThermalMonitor_CustomLimit(t *testing.T) {
	m := NewThermalMonitor(80)
	assert.Equal(t, 80, m.ThermalLimit())
	assert.Equal(t, 90, m.ShutdownLimit())
}

func TestThermalMonitor_LimitCappedAt95(t *testing.T) {
	m := NewThermalMonitor(90)
	assert.Equal(t, 90, m.ThermalLimit())
	assert.Equal(t, 95, m.ShutdownLimit(), "should cap at 95")
}

// ---------------------------------------------------------------------------
// TranscodeManager tests
// ---------------------------------------------------------------------------

func TestTranscodeManager_ThrottleDowngradePreset(t *testing.T) {
	basePreset := ResolvedPreset{
		Name:             "test",
		VideoBitrateKbps: 4000,
		Resolution:       "1920x1080",
		Framerate:        30,
		GopSeconds:       2,
		Profile:          "main",
		Bframes:          0,
	}

	var createdPreset ResolvedPreset
	var createMu sync.Mutex
	createFn := func(preset ResolvedPreset) Transcoder {
		createMu.Lock()
		createdPreset = preset
		createMu.Unlock()
		return newMockTranscoder(t)
	}

	mgr := NewTranscodeManager(basePreset, 85, createFn)

	// Set up mock thermal zones at throttle temp
	_, paths := setupMockZones(t, map[string]int{"zone0": 86000}) // 86°C
	zoneMonitor := NewThermalMonitorWithZones(85, paths)
	mgr.thermal = zoneMonitor

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mgr.Run(ctx)

	// After throttle, the preset should be downgraded
	createMu.Lock()
	current := createdPreset
	createMu.Unlock()

	assert.Equal(t, 2000, current.VideoBitrateKbps, "bitrate should be halved")
	assert.Equal(t, "852x480", current.Resolution, "resolution should drop to 480p")

	// Error status should be empty (throttle is not permanent error)
	assert.Equal(t, "", mgr.ErrorStatus())
}

func TestTranscodeManager_ThrottleThenNormalDowngradedPreset(t *testing.T) {
	basePreset := ResolvedPreset{
		Name:             "test",
		VideoBitrateKbps: 2000,
		Resolution:       "1280x720",
		Framerate:        30,
	}

	var createdPresets []ResolvedPreset
	var createMu sync.Mutex
	createFn := func(preset ResolvedPreset) Transcoder {
		createMu.Lock()
		createdPresets = append(createdPresets, preset)
		createMu.Unlock()
		return newMockTranscoder(t)
	}

	mgr := NewTranscodeManager(basePreset, 85, createFn)

	_, paths := setupMockZones(t, map[string]int{"zone0": 86000})
	zoneMonitor := NewThermalMonitorWithZones(85, paths)
	mgr.thermal = zoneMonitor

	// Run with throttle — should start with base, then downgrade on restart
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mgr.Run(ctx)

	createMu.Lock()
	current := mgr.CurrentPreset()
	downgraded := mgr.downgradedPreset
	createMu.Unlock()

	// Current preset should be the downgraded one
	assert.Equal(t, 1000, current.VideoBitrateKbps, "bitrate should be halved")
	assert.Equal(t, "852x480", current.Resolution)
	assert.Equal(t, 1000, downgraded.VideoBitrateKbps)
	assert.Equal(t, "852x480", downgraded.Resolution)
}

func TestTranscodeManager_ThermalShutdown(t *testing.T) {
	basePreset := ResolvedPreset{
		Name:             "test",
		VideoBitrateKbps: 2000,
		Resolution:       "1280x720",
		Framerate:        30,
	}

	createFn := func(preset ResolvedPreset) Transcoder {
		return newMockTranscoder(t)
	}

	mgr := NewTranscodeManager(basePreset, 85, createFn)

	// Set up mock thermal zones at shutdown temp
	_, paths := setupMockZones(t, map[string]int{"zone0": 96000}) // 96°C
	zoneMonitor := NewThermalMonitorWithZones(85, paths)
	mgr.thermal = zoneMonitor

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mgr.Run(ctx)

	// Error status should be set for thermal shutdown
	assert.Contains(t, mgr.ErrorStatus(), "thermal shutdown",
		"should report thermal shutdown error")
}

func TestTranscodeManager_BackoffRestart(t *testing.T) {
	basePreset := ResolvedPreset{
		Name:             "test",
		VideoBitrateKbps: 2000,
		Resolution:       "1280x720",
		Framerate:        30,
	}

	var createCount int
	var createMu sync.Mutex
	createFn := func(preset ResolvedPreset) Transcoder {
		createMu.Lock()
		createCount++
		createMu.Unlock()
		// Crash after 50ms — the manager should restart
		return newCrashingMock(t, 50*time.Millisecond)
	}

	mgr := NewTranscodeManager(basePreset, 85, createFn)
	mgr.SetBackoffFn(func(attempt int) time.Duration { return 10 * time.Millisecond })

	// No thermal zones for this test (thermalCh closed immediately)
	_, paths := setupMockZones(t, nil) // no zones
	zoneMonitor := NewThermalMonitorWithZones(85, paths)
	mgr.thermal = zoneMonitor

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mgr.Run(ctx)

	createMu.Lock()
	count := createCount
	createMu.Unlock()

	// Should have created multiple transcoders (at least 2) before hitting
	// the 5-failure cap
	assert.GreaterOrEqual(t, count, 2, "should restart at least once")
	assert.Contains(t, mgr.ErrorStatus(), "crashed too many times",
		"should report crash failure")
}

func TestTranscodeManager_FiveFailureCap(t *testing.T) {
	basePreset := ResolvedPreset{
		Name:             "test",
		VideoBitrateKbps: 2000,
		Resolution:       "1280x720",
		Framerate:        30,
	}

	var createCount int
	var createMu sync.Mutex
	createFn := func(preset ResolvedPreset) Transcoder {
		createMu.Lock()
		createCount++
		createMu.Unlock()
		return newCrashingMock(t, 30*time.Millisecond)
	}

	mgr := NewTranscodeManager(basePreset, 85, createFn)
	mgr.SetBackoffFn(func(attempt int) time.Duration { return 10 * time.Millisecond })

	_, paths := setupMockZones(t, nil) // no thermal zones
	zoneMonitor := NewThermalMonitorWithZones(85, paths)
	mgr.thermal = zoneMonitor

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mgr.Run(ctx)

	createMu.Lock()
	count := createCount
	createMu.Unlock()

	// Should have attempted exactly 1 (first) + 5 retries = 6 starts at most
	// The first attempt is not a "failure" for backoff counting,
	// so 5 failures = 6 total creates
	assert.LessOrEqual(t, count, 7, "should not create more than 6-7 transcoders")
	assert.GreaterOrEqual(t, count, 5, "should have attempted multiple restarts")
	assert.Contains(t, mgr.ErrorStatus(), "crashed too many times",
		"should report max failure limit")
}

func TestTranscodeManager_StartsCleanly(t *testing.T) {
	basePreset := ResolvedPreset{
		Name:             "test",
		VideoBitrateKbps: 2000,
		Resolution:       "1280x720",
		Framerate:        30,
	}

	var createdPreset ResolvedPreset
	createFn := func(preset ResolvedPreset) Transcoder {
		createdPreset = preset
		return newMockTranscoder(t)
	}

	mgr := NewTranscodeManager(basePreset, 85, createFn)

	_, paths := setupMockZones(t, nil) // no thermal zones
	zoneMonitor := NewThermalMonitorWithZones(85, paths)
	mgr.thermal = zoneMonitor

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mgr.Run(ctx)

	// Should exit cleanly (no thermal, no crash)
	assert.Equal(t, "", mgr.ErrorStatus(), "no error expected")
	assert.Equal(t, basePreset, createdPreset, "should use base preset")
}

func TestTranscodeManager_ContextCancellation(t *testing.T) {
	basePreset := ResolvedPreset{
		Name:             "test",
		VideoBitrateKbps: 2000,
		Resolution:       "1280x720",
		Framerate:        30,
	}

	createFn := func(preset ResolvedPreset) Transcoder {
		return newMockTranscoder(t)
	}

	mgr := NewTranscodeManager(basePreset, 85, createFn)

	_, paths := setupMockZones(t, nil)
	zoneMonitor := NewThermalMonitorWithZones(85, paths)
	mgr.thermal = zoneMonitor

	// Create a context that will be cancelled immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	mgr.Run(ctx)

	assert.Equal(t, "", mgr.ErrorStatus(), "context cancellation is not an error")
}

func TestTranscodeManager_CurrentPresetAccessor(t *testing.T) {
	basePreset := ResolvedPreset{
		Name:             "test",
		VideoBitrateKbps: 3000,
		Resolution:       "1920x1080",
		Framerate:        30,
	}

	createFn := func(preset ResolvedPreset) Transcoder {
		return newMockTranscoder(t)
	}

	mgr := NewTranscodeManager(basePreset, 85, createFn)

	// Before Run, CurrentPreset should be base
	assert.Equal(t, basePreset, mgr.CurrentPreset())

	// downgradedPreset should have halved bitrate and 480p
	downgraded := mgr.downgradedPreset
	assert.Equal(t, 1500, downgraded.VideoBitrateKbps)
	assert.Equal(t, "852x480", downgraded.Resolution)
}

func TestTranscodeManager_MinBitrateFloor(t *testing.T) {
	// Even very low bitrates should floor at 100 kbps after halving
	basePreset := ResolvedPreset{
		Name:             "low",
		VideoBitrateKbps: 100,
		Resolution:       "640x360",
	}

	createFn := func(preset ResolvedPreset) Transcoder {
		return newMockTranscoder(t)
	}

	mgr := NewTranscodeManager(basePreset, 85, createFn)

	downgraded := mgr.downgradedPreset
	assert.Equal(t, 100, downgraded.VideoBitrateKbps,
		"bitrate should floor at 100 kbps")
}
