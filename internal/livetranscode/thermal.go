// Package livetranscode provides live transcoding utilities including thermal
// monitoring for the FFmpeg subprocess and automatic backoff restart.
package livetranscode

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/backoff"
)

// ---------------------------------------------------------------------------
// Thermal event types
// ---------------------------------------------------------------------------

// ThermalEventKind indicates the severity of a thermal event.
type ThermalEventKind int

const (
	// ThermalEventNone is the zero value placeholder.
	ThermalEventNone ThermalEventKind = iota
	// ThermalEventThrottle is sent when temperature exceeds the throttle threshold.
	ThermalEventThrottle
	// ThermalEventShutdown is sent when temperature exceeds the shutdown threshold.
	ThermalEventShutdown
)

// ThermalEvent carries temperature readings from ThermalMonitor.
type ThermalEvent struct {
	Kind        ThermalEventKind
	Temperature int // Celsius
}

// errThermalShutdown is returned by TranscodeManager when a thermal shutdown
// event terminates the transcoder permanently.
var errThermalShutdown = errors.New("thermal shutdown")

// ---------------------------------------------------------------------------
// ThermalMonitor
// ---------------------------------------------------------------------------

// ThermalMonitor reads /sys/class/thermal/thermal_zone*/temp at a fixed
// interval and sends ThermalEvent values on a channel when the temperature
// exceeds configurable thresholds.
type ThermalMonitor struct {
	logger        *slog.Logger
	thermalLimit  int
	shutdownLimit int
	checkInterval time.Duration

	zonePaths []string

	ch      chan ThermalEvent
	cancel  context.CancelFunc
	started atomic.Bool
	stopMu  sync.Mutex
}

// NewThermalMonitor creates a ThermalMonitor with the given throttle threshold
// in °C. The shutdown threshold defaults to thermalLimit + 10 (capped at 95).
// The check interval defaults to 30s. Thermal zone paths are auto-discovered
// from /sys/class/thermal/thermal_zone*/temp.
func NewThermalMonitor(thermalLimit int) *ThermalMonitor {
	if thermalLimit <= 0 {
		thermalLimit = 85
	}
	shutdownLimit := thermalLimit + 10
	if shutdownLimit > 95 {
		shutdownLimit = 95
	}

	zones := discoverThermalZones()
	log := slog.Default().With("component", "thermal")
	if len(zones) == 0 {
		log.Info("no thermal zones found, thermal monitoring disabled")
	}

	return &ThermalMonitor{
		logger:        log,
		thermalLimit:  thermalLimit,
		shutdownLimit: shutdownLimit,
		checkInterval: 30 * time.Second,
		zonePaths:     zones,
		ch:            make(chan ThermalEvent, 4),
	}
}

// NewThermalMonitorWithZones creates a ThermalMonitor with explicit zone file
// paths. Used for testing — normal callers should use NewThermalMonitor.
func NewThermalMonitorWithZones(thermalLimit int, zonePaths []string) *ThermalMonitor {
	if thermalLimit <= 0 {
		thermalLimit = 85
	}
	shutdownLimit := thermalLimit + 10
	if shutdownLimit > 95 {
		shutdownLimit = 95
	}

	return &ThermalMonitor{
		logger:        slog.Default().With("component", "thermal"),
		thermalLimit:  thermalLimit,
		shutdownLimit: shutdownLimit,
		checkInterval: 30 * time.Second,
		zonePaths:     zonePaths,
		ch:            make(chan ThermalEvent, 4),
	}
}

// discoverThermalZones finds all readable thermal zone temperature files.
func discoverThermalZones() []string {
	matches, err := filepath.Glob("/sys/class/thermal/thermal_zone*/temp")
	if err != nil {
		return nil
	}
	// Filter to readable files only
	var zones []string
	for _, m := range matches {
		if _, err := os.Stat(m); err == nil {
			zones = append(zones, m)
		}
	}
	return zones
}

// ThermalLimit returns the throttle threshold in °C.
func (m *ThermalMonitor) ThermalLimit() int { return m.thermalLimit }

// ShutdownLimit returns the shutdown threshold in °C.
func (m *ThermalMonitor) ShutdownLimit() int { return m.shutdownLimit }

// ZonePaths returns the discovered thermal zone file paths (may be empty on
// non-ARM / non-Linux platforms).
func (m *ThermalMonitor) ZonePaths() []string { return m.zonePaths }

// SetCheckInterval sets the thermal zone check interval (for testing).
func (m *ThermalMonitor) SetCheckInterval(d time.Duration) {
	m.checkInterval = d
}

// Start begins periodic thermal zone monitoring. Returns a receive-only
// channel of ThermalEvent values. If no thermal zones were discovered, the
// channel never sends events (no-op select). Safe to call multiple times —
// subsequent calls return the same channel.
func (m *ThermalMonitor) Start(ctx context.Context) <-chan ThermalEvent {
	if !m.started.CompareAndSwap(false, true) {
		return m.ch
	}
	if len(m.zonePaths) == 0 {
		return m.ch // never sends events — no-op select
	}
	ctx, m.cancel = context.WithCancel(ctx)
	go m.run(ctx)
	return m.ch
}

// Stop cancels the monitoring goroutine and closes the event channel.
func (m *ThermalMonitor) Stop() {
	m.stopMu.Lock()
	defer m.stopMu.Unlock()
	if m.cancel != nil {
		m.cancel()
	}
}

// run is the main monitoring loop.
func (m *ThermalMonitor) run(ctx context.Context) {
	defer close(m.ch)

	// Do initial check immediately
	m.check(ctx)

	ticker := time.NewTicker(m.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !m.check(ctx) {
				return // shutdown sent, stop monitoring
			}
		}
	}
}

// check reads all thermal zones and sends events if thresholds are exceeded.
// Returns false if a shutdown event was sent.
func (m *ThermalMonitor) check(ctx context.Context) bool {
	temp := m.readHighestTemp()
	if temp < 0 {
		return true // no zones readable, keep monitoring
	}

	if temp >= m.shutdownLimit {
		select {
		case m.ch <- ThermalEvent{Kind: ThermalEventShutdown, Temperature: temp}:
		case <-ctx.Done():
			return false
		}
		return false
	}

	if temp >= m.thermalLimit {
		select {
		case m.ch <- ThermalEvent{Kind: ThermalEventThrottle, Temperature: temp}:
		case <-ctx.Done():
			return false
		}
	}

	return true
}

// readHighestTemp reads all thermal zones and returns the highest temperature
// in °C. Returns -1 if no zones are readable.
func (m *ThermalMonitor) readHighestTemp() int {
	highest := -1
	for _, path := range m.zonePaths {
		temp, err := readZoneTemp(path)
		if err != nil {
			m.logger.Warn("failed to read thermal zone", "path", path, "error", err)
			continue
		}
		if temp > highest {
			highest = temp
		}
	}
	return highest
}

// readZoneTemp reads a single thermal zone temp file and returns the
// temperature in °C. Thermal zone files report millidegrees Celsius.
func readZoneTemp(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	millideg, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("parse thermal zone %q: %w", path, err)
	}
	return millideg / 1000, nil
}

// ---------------------------------------------------------------------------
// Transcoder interface
// ---------------------------------------------------------------------------

// Transcoder defines the lifecycle methods used by TranscodeManager.
// LiveTranscoder implements this interface.
type Transcoder interface {
	// Start spawns the subprocess and begins transcoding.
	Start(ctx context.Context) error
	// Stop gracefully shuts down the subprocess.
	Stop() error
	// Done returns a channel that is closed when the subprocess exits
	// (either intentionally via Stop or unexpectedly via crash).
	Done() <-chan struct{}
	// EncoderName returns the selected encoder name for status reporting.
	EncoderName() string
}

// ---------------------------------------------------------------------------
// TranscodeManager
// ---------------------------------------------------------------------------

// TranscodeManager wraps a Transcoder with thermal monitoring and backoff
// restart logic. It reacts to thermal events (throttle → downgrade preset,
// shutdown → stop permanently) and handles unexpected transcoder exits with
// TieredBackoffWithJitter.
type TranscodeManager struct {
	logger    *slog.Logger
	thermal   *ThermalMonitor
	backoffFn func(attempt int) time.Duration

	transcoder     Transcoder
	transcoderMu   sync.Mutex
	createFn       func(preset ResolvedPreset) Transcoder
	transcoderDone <-chan struct{}

	basePreset       ResolvedPreset
	currentPreset    ResolvedPreset
	downgradedPreset ResolvedPreset

	// Backoff state
	mu            sync.Mutex
	attempt       int
	firstFailure  time.Time
	maxAttempts   int
	failureWindow time.Duration
	errStatus     string

	// Thermal throttle state — prevents repeated downgrade logging
	warmedStyled atomic.Bool

	// Metrics
	temperatureGauge        prometheus.Gauge
	restartsCounter         prometheus.Counter
	thermalThrottlesCounter prometheus.Counter
}

// NewTranscodeManager creates a TranscodeManager.
//
//   - basePreset: the starting encoding preset.
//   - thermalLimit: throttle temperature in °C (default 85). Shutdown is
//     thermalLimit + 10 (capped at 95).
//   - createTranscoder: factory used to create a new Transcoder each time a
//     (re)start is needed. The factory receives the current preset.
func NewTranscodeManager(
	basePreset ResolvedPreset,
	thermalLimit int,
	createTranscoder func(preset ResolvedPreset) Transcoder,
) *TranscodeManager {
	if thermalLimit <= 0 {
		thermalLimit = 85
	}

	// Build downgraded preset: halve bitrate, drop resolution to 480p
	downgraded := basePreset
	if downgraded.VideoBitrateKbps > 0 {
		downgraded.VideoBitrateKbps /= 2
	}
	if downgraded.VideoBitrateKbps < 100 {
		downgraded.VideoBitrateKbps = 100 // floor
	}
	downgraded.Resolution = "852x480"

	return &TranscodeManager{
		logger:           slog.Default().With("component", "transcode_manager"),
		thermal:          NewThermalMonitor(thermalLimit),
		backoffFn:        backoff.TieredBackoffWithJitter,
		createFn:         createTranscoder,
		basePreset:       basePreset,
		currentPreset:    basePreset,
		downgradedPreset: downgraded,
		maxAttempts:      5,
		failureWindow:    60 * time.Second,

		temperatureGauge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "nvr_relay_transcoder_temperature_c",
			Help: "Current transcoder thermal zone temperature in Celsius.",
		}),
		restartsCounter: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "nvr_relay_transcoder_restarts_total",
			Help: "Total number of transcoder restarts.",
		}),
		thermalThrottlesCounter: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "nvr_relay_transcoder_thermal_throttles_total",
			Help: "Total number of thermal throttle events that caused preset downgrade.",
		}),
	}
}

// MetricsCollectors returns the Prometheus collectors that should be
// registered with a registry. The caller (main.go) is responsible for
// registering them.
func (m *TranscodeManager) MetricsCollectors() []prometheus.Collector {
	return []prometheus.Collector{
		m.temperatureGauge,
		m.restartsCounter,
		m.thermalThrottlesCounter,
	}
}

// ThermalMonitor returns the underlying thermal monitor for status inspection.
func (m *TranscodeManager) ThermalMonitor() *ThermalMonitor { return m.thermal }

// CurrentPreset returns the preset currently in use (may differ from
// basePreset after a thermal throttle downgrade).
func (m *TranscodeManager) CurrentPreset() ResolvedPreset {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.currentPreset
}

// ErrorStatus returns a non-empty string if the manager has permanently
// stopped retrying (e.g. thermal shutdown or too many failures).
func (m *TranscodeManager) ErrorStatus() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.errStatus
}

// SetBackoffFn replaces the default backoff function (for testing).
func (m *TranscodeManager) SetBackoffFn(fn func(attempt int) time.Duration) {
	m.backoffFn = fn
}

// ---------------------------------------------------------------------------
// Run loop
// ---------------------------------------------------------------------------

// Run starts thermal monitoring and the transcoder lifecycle loop. It blocks
// until:
//   - ctx is cancelled
//   - a ThermalShutdown event is received
//   - the maximum number of consecutive restart failures is exceeded
//
// On ThermalThrottle the preset is downgraded and the transcoder is restarted.
// On unexpected exit the transcoder is restarted with backoff.
func (m *TranscodeManager) Run(ctx context.Context) {
	thermalCh := m.thermal.Start(ctx)

	for {
		// Check context
		if ctx.Err() != nil {
			return
		}

		// Check permanent error
		if m.ErrorStatus() != "" {
			return
		}

		// Start the transcoder
		if err := m.start(ctx); err != nil {
			m.logger.Error("failed to start transcoder", "error", err)
			if m.recordFailure() {
				m.backoffWait(ctx)
				continue
			}
			m.setError("transcoder start failed after max retries")
			return
		}

		// Wait for transcoder exit or thermal event
		err := m.wait(ctx, thermalCh)
		if err == nil {
			// Intentional stop (non-thermal) — loop restarts
			continue
		}
		if errors.Is(err, errThermalShutdown) {
			return
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}

		// Unexpected exit — backoff and retry
		m.logger.Warn("transcoder exited unexpectedly, backing off", "error", err)
		if m.recordFailure() {
			m.backoffWait(ctx)
			continue
		}
		m.setError("transcoder crashed too many times")
		return
	}
}

// start creates and starts a Transcoder with the current preset.
func (m *TranscodeManager) start(ctx context.Context) error {
	m.transcoderMu.Lock()
	m.transcoder = m.createFn(m.currentPreset)
	m.transcoderMu.Unlock()

	if err := m.transcoder.Start(ctx); err != nil {
		return err
	}
	m.transcoderDone = m.transcoder.Done()
	return nil
}

// stop shuts down the current transcoder.
func (m *TranscodeManager) stop() {
	m.transcoderMu.Lock()
	defer m.transcoderMu.Unlock()
	if m.transcoder != nil {
		m.transcoder.Stop()
	}
	m.transcoderDone = nil
}

// wait blocks until the transcoder exits or a thermal event is received.
// Returns nil for intentional stop, errThermalShutdown for thermal shutdown,
// or the error from transcoder.Done() otherwise.
func (m *TranscodeManager) wait(ctx context.Context, thermalCh <-chan ThermalEvent) error {
	select {
	case <-ctx.Done():
		m.stop()
		return ctx.Err()

	case evt, ok := <-thermalCh:
		if !ok {
			// Thermal monitor stopped (e.g. no zones) — treat as normal stop
			return nil
		}

		switch evt.Kind {
		case ThermalEventShutdown:
			m.logger.Error("thermal shutdown",
				"temperature", evt.Temperature,
				"shutdown_limit", m.thermal.ShutdownLimit())
			m.temperatureGauge.Set(float64(evt.Temperature))
			m.stop()
			m.setError(fmt.Sprintf("thermal shutdown at %d°C", evt.Temperature))
			return errThermalShutdown

		case ThermalEventThrottle:
			m.logger.Warn("thermal throttle",
				"temperature", evt.Temperature,
				"thermal_limit", m.thermal.ThermalLimit())
			m.temperatureGauge.Set(float64(evt.Temperature))
			m.thermalThrottlesCounter.Inc()
			m.stop()
			m.applyThrottle()
			return nil // loop will restart with downgraded preset
		}
		return nil

	case <-m.waitCh():
		// Transcoder exited (crashed or process died)
		return m.transcoderExitedError()
	}
}

// waitCh returns a channel that fires when the current transcoder exits.
func (m *TranscodeManager) waitCh() <-chan struct{} {
	m.transcoderMu.Lock()
	defer m.transcoderMu.Unlock()
	if m.transcoderDone != nil {
		return m.transcoderDone
	}
	// Closed channel — fires immediately (no running transcoder)
	ch := make(chan struct{})
	close(ch)
	return ch
}

// transcoderExitedError returns the error from a crashed transcoder.
func (m *TranscodeManager) transcoderExitedError() error {
	m.transcoderMu.Lock()
	defer m.transcoderMu.Unlock()
	if m.transcoder != nil {
		// Clean up
		m.transcoder.Stop()
	}
	return fmt.Errorf("transcoder exited unexpectedly")
}

// applyThrottle downgrades the current preset on thermal throttle.
func (m *TranscodeManager) applyThrottle() {
	if m.warmedStyled.CompareAndSwap(false, true) {
		m.logger.Warn("preset downgraded due to thermal throttle",
			"previous_preset", m.currentPreset,
			"downgraded_preset", m.downgradedPreset)
	}
	m.mu.Lock()
	m.currentPreset = m.downgradedPreset
	m.attempt = 0 // reset failure counter on intentional restart
	m.mu.Unlock()
}

// setError sets the permanent error status and logs it.
func (m *TranscodeManager) setError(msg string) {
	m.mu.Lock()
	m.errStatus = msg
	m.mu.Unlock()
	m.logger.Error("transcode manager stopped", "reason", msg)
}

// recordFailure increments the failure counter. Returns true if the number
// of failures within the window is below the maximum.
func (m *TranscodeManager) recordFailure() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	if m.attempt == 0 {
		m.firstFailure = now
	}

	// Reset window if too old
	if now.Sub(m.firstFailure) > m.failureWindow {
		m.attempt = 0
		m.firstFailure = now
	}

	m.attempt++
	return m.attempt <= m.maxAttempts
}

// backoffWait sleeps for the backoff duration or until ctx is cancelled.
func (m *TranscodeManager) backoffWait(ctx context.Context) {
	m.mu.Lock()
	attempt := m.attempt
	m.mu.Unlock()

	dur := m.backoffFn(attempt)
	m.logger.Warn("transcoder restart backoff",
		"attempt", attempt, "backoff", dur)
	m.restartsCounter.Inc()

	select {
	case <-ctx.Done():
	case <-time.After(dur):
	}
}
