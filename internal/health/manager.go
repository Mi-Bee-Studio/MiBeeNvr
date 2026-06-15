package health

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// Manager orchestrates all health monitoring layers:
//   - Layer 1: ConnectionMonitor — detects camera connection loss/restoration
//   - Layer 2: StreamStatsCollector — detects bitrate/FPS/IDR anomalies
//   - Layer 2.5: FreezeDetector — detects frozen video streams
//   - AlertPipeline — deduplicates and dispatches events to storage + MQTT
// StatusFunc returns current camera statuses as map[cameraID]status.
type StatusFunc func() map[string]string
type Manager struct {
	cfg config.HealthConfig

	conn      *ConnectionMonitor
	collector *StreamStatsCollector
	freeze    *FreezeDetector
	pipeline  *AlertPipeline
	autoRemediate *AutoRemediator
	qualityTracker      *QualityTracker // 24h rolling window
	qualityTrackerShort *QualityTracker // 1h rolling window for trend

	statusFn      StatusFunc
	knownStatuses map[string]string

	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex

	store HealthStorage
}
// NewManager creates a health manager. Returns nil if health monitoring is disabled.
func NewManager(cfg config.HealthConfig, store HealthStorage) *Manager {
	if !cfg.Enabled {
		return nil
	}

	// Parse durations from config
	offlineThreshold, _ := time.ParseDuration(cfg.Layer1.OfflineThreshold)
	cooldown, _ := time.ParseDuration(cfg.Alerts.Cooldown)
	maxIDRInterval, _ := time.ParseDuration(cfg.Layer2.MaxIDRInterval)
	freezeTimeout, _ := time.ParseDuration(cfg.Layer2_5.FreezeTimeout)

	// Create alert pipeline with storage (MQTT injected later via SetMQTTClient)
	pipeline := NewAlertPipeline(cooldown, cfg.Alerts.MQTT, store, nil, "mibee-nvr")

	// Event handler that routes through pipeline
	handler := func(cameraID string, event model.HealthEvent) {
		_ = pipeline.HandleEvent(cameraID, event)
	}

	// Create sub-components
	conn := NewConnectionMonitor(offlineThreshold, handler)
	collector := NewStreamStatsCollector(
		cfg.Layer2.BitrateChangeThreshold,
		float64(cfg.Layer2.MinFPS),
		maxIDRInterval,
		30*time.Second, // check window
		handler,
	)
	freeze := NewFreezeDetector(freezeTimeout, handler)

	// Create auto-remediator if enabled (functions injected later via SetRestarter/SetCameraEnabledFn)
	var remediator *AutoRemediator
	if cfg.AutoRemediation.Enabled {
		remediator = NewAutoRemediator(cfg.AutoRemediation, nil, nil)
	}

	return &Manager{
		cfg:       cfg,
		conn:      conn,
		collector: collector,
		freeze:    freeze,
		pipeline:  pipeline,
		autoRemediate: remediator,
		qualityTracker:      NewQualityTracker(24 * time.Hour),
		qualityTrackerShort: NewQualityTracker(1 * time.Hour),

		knownStatuses: make(map[string]string),
		done:      make(chan struct{}),
		store:     store,
	}
}

// Start begins the periodic health check loop.
func (m *Manager) Start(ctx context.Context) error {
	if m == nil {
		return nil
	}

	// Clean up old noisy stream_anomaly events on startup
	// These were caused by a fixed IDR interval detection bug; new ones are genuine.
	if m.store != nil {
		deleted, err := m.store.DeleteHealthEventsByType(ctx, string(model.HealthEventStreamAnomaly), time.Now())
		if err != nil {
			slog.Warn("failed to clean up old health events", "error", err)
		} else if deleted > 0 {
			slog.Info("cleaned up old health events", "type", "stream_anomaly", "deleted", deleted)
		}
	}

	childCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel

	go m.run(childCtx)
	slog.Info("health manager started")
	return nil
}

func (m *Manager) run(ctx context.Context) {
	defer close(m.done)

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.conn.Check()
			m.collector.CheckAndReset()
			m.freeze.Check()
			m.pipeline.CleanStaleAnomalies()
			m.pollStatuses()
			if m.autoRemediate != nil {
				m.autoRemediate.CheckAll(m.knownStatuses)
			}
		}
	}
}
// Stop shuts down the health manager.
func (m *Manager) Stop() {
	if m == nil || m.cancel == nil {
		return
	}
	m.cancel()
	<-m.done
	slog.Info("health manager stopped")
}

// SetStatusFunc sets the function used to poll camera statuses.
func (m *Manager) SetStatusFunc(fn StatusFunc) {
	if m == nil {
		return
	}
	m.statusFn = fn
}

// SetMQTTClient injects the MQTT client for health event publishing.
// Call after MQTT initialization if alerts.MQTT is enabled.
func (m *Manager) SetMQTTClient(client MQTTPublisher) {
	if m == nil || m.pipeline == nil {
		return
	}
	m.pipeline.mqttClient = client
}

// SetRestarter injects the function used to restart camera recorders for auto-remediation.
func (m *Manager) SetRestarter(fn RestartRecorderFunc) {
	if m == nil || m.autoRemediate == nil {
		return
	}
	m.autoRemediate.restartFn = fn
}

// SetCameraEnabledFn injects the function used to check if a camera is enabled for auto-remediation.
func (m *Manager) SetCameraEnabledFn(fn IsCameraEnabledFunc) {
	if m == nil || m.autoRemediate == nil {
		return
	}
	m.autoRemediate.isEnabledFn = fn
}

// OnCameraAdded starts monitoring a newly added camera.
// If overrides is non-nil, per-camera thresholds are applied.
func (m *Manager) OnCameraAdded(cameraID string, recorder model.Recorder, overrides *config.ResolvedHealthOverrides) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	// Apply per-camera overrides if provided
	if overrides != nil {
		offlineThreshold, _ := time.ParseDuration(overrides.OfflineThreshold)
		maxIDR, _ := time.ParseDuration(overrides.MaxIDRInterval)
		freezeTimeout, _ := time.ParseDuration(overrides.FreezeTimeout)

		m.conn.SetCameraOverride(cameraID, offlineThreshold)
		m.collector.SetCameraOverride(cameraID, overrides.BitrateChangeThreshold, float64(overrides.MinFPS), maxIDR)
		m.freeze.SetCameraOverride(cameraID, freezeTimeout)
	}

	// Subscribe to StreamHub for stats and freeze detection
	if hub := getHub(recorder); hub != nil {
		statsCallback := m.collector.OnFrame(cameraID)
		_ = hub.Subscribe("health-stats-"+cameraID, statsCallback)

		freezeCallback := m.freeze.OnFrame(cameraID)
		_ = hub.Subscribe("health-freeze-"+cameraID, freezeCallback)
	}

	// Initialize connection monitoring
	m.conn.OnStatusChange(cameraID, string(model.StatusRecording))
	m.freeze.SetRecording(cameraID, true)
	m.pipeline.SetCameraStatus(cameraID, string(model.HealthStatusHealthy))

	m.knownStatuses[cameraID] = string(model.StatusRecording)
	slog.Info("health monitoring started for camera", "camera_id", cameraID)
}

// OnCameraRemoved stops monitoring a removed camera.
func (m *Manager) OnCameraRemoved(cameraID string, recorder model.Recorder) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	// Unsubscribe from StreamHub
	if hub := getHub(recorder); hub != nil {
		hub.Unsubscribe("health-stats-" + cameraID)
		hub.Unsubscribe("health-freeze-" + cameraID)
	}

	m.conn.RemoveCamera(cameraID)
	m.collector.RemoveCamera(cameraID)
	m.freeze.RemoveCamera(cameraID)
	m.qualityTracker.RemoveCamera(cameraID)
	m.qualityTrackerShort.RemoveCamera(cameraID)
	m.pipeline.SetCameraStatus(cameraID, "")
	delete(m.knownStatuses, cameraID)

	slog.Info("health monitoring stopped for camera", "camera_id", cameraID)
}

// OnStatusChange handles recorder status changes.
func (m *Manager) OnStatusChange(cameraID string, status string) {
	if m == nil {
		return
	}
	m.conn.OnStatusChange(cameraID, status)

	// Update freeze detector recording state
	isRecording := status == string(model.StatusRecording)
	m.freeze.SetRecording(cameraID, isRecording)

	// Track quality via QualityTracker
	if isRecording {
		m.qualityTracker.OnOnline(cameraID)
		m.qualityTrackerShort.OnOnline(cameraID)
	} else {
		m.qualityTracker.OnOffline(cameraID)
		m.qualityTrackerShort.OnOffline(cameraID)
	}

	// Update pipeline status for API queries and reset collector on reconnect
	if isRecording {
		m.pipeline.SetCameraStatus(cameraID, string(model.HealthStatusHealthy))
		m.collector.ResetCameraState(cameraID)
	}
}
// GetCameraHealth returns the current health status for a camera.
func (m *Manager) GetCameraHealth(cameraID string) *model.CameraHealth {
	if m == nil {
		return nil
	}
	status := m.pipeline.GetCameraStatus(cameraID)
	// Map pipeline health status to recorder status for score computation
	recorderStatus := m.knownRecorderStatus(cameraID)
	offlineDur := m.conn.GetOfflineDuration(cameraID)
	anomalyCount := m.pipeline.GetAnomalyCount(cameraID)
	score := ComputeHealthScore(recorderStatus, offlineDur, anomalyCount, 100.0)
	return &model.CameraHealth{
		CameraID:     cameraID,
		LatestStatus: status,
		Score:        score.Score,
		ScoreFactors: formatFactors(score.Factors),
}
}

// GetAllHealth returns health status for all monitored cameras.
func (m *Manager) GetAllHealth() map[string]*model.CameraHealth {
	if m == nil {
		return nil
	}
	statuses := m.pipeline.GetAllStatuses()
	result := make(map[string]*model.CameraHealth, len(statuses))
	for camID, status := range statuses {
		recorderStatus := m.knownRecorderStatus(camID)
		offlineDur := m.conn.GetOfflineDuration(camID)
		anomalyCount := m.pipeline.GetAnomalyCount(camID)
		score := ComputeHealthScore(recorderStatus, offlineDur, anomalyCount, 100.0)
		result[camID] = &model.CameraHealth{
			CameraID:     camID,
			LatestStatus: status,
			Score:        score.Score,
			ScoreFactors: formatFactors(score.Factors),
		}
	}
	return result
}

// getHub extracts the StreamHub from a recorder via type assertion.
func getHub(recorder model.Recorder) *model.StreamHub {
	type hubber interface {
		GetHub() *model.StreamHub
	}
	if h, ok := recorder.(hubber); ok {
		return h.GetHub()
	}
	return nil
}

// pollStatuses checks camera statuses and forwards transitions to connection monitor.
// Also proactively recovers pipeline status for cameras that are recording
// but whose pipeline status is stale (e.g., brief reconnect resolved between polls
// on limited-ONVIF cameras).
func (m *Manager) pollStatuses() {
	if m.statusFn == nil {
		return
	}
	statuses := m.statusFn()
	for cameraID, status := range statuses {
		if prev, ok := m.knownStatuses[cameraID]; ok && prev != status {
			m.OnStatusChange(cameraID, status)
		} else if status == string(model.StatusRecording) {
			// Proactively recover: if camera is recording but pipeline shows
			// non-healthy, reset to healthy. This handles cases where brief
			// reconnects complete between 10s poll ticks on limited devices.
			if pipelineStatus := m.pipeline.GetCameraStatus(cameraID); pipelineStatus != string(model.HealthStatusHealthy) {
				m.pipeline.SetCameraStatus(cameraID, string(model.HealthStatusHealthy))
				m.conn.OnStatusChange(cameraID, status)
				m.collector.ResetCameraState(cameraID)
			}
		}
		m.knownStatuses[cameraID] = status
	}
}

// knownRecorderStatus returns the last known recorder status for a camera.
// Falls back to the pipeline health status if no recorder status is tracked.
func (m *Manager) knownRecorderStatus(cameraID string) string {
	if s, ok := m.knownStatuses[cameraID]; ok {
		return s
	}
	return m.pipeline.GetCameraStatus(cameraID)
}

// formatFactors converts ScoreFactor slice to human-readable strings.
func formatFactors(factors []ScoreFactor) []string {
	if len(factors) == 0 {
		return nil
	}
	result := make([]string, len(factors))
	for i, f := range factors {
		result[i] = fmt.Sprintf("%s: %+d (%s)", f.Name, f.Impact, f.Detail)
	}
	return result
}

// StabilityData represents the stability quality metrics for a single camera,
// including a computed trend based on short vs long window comparison.
type StabilityData struct {
	UptimePercent      float64 `json:"uptime_percent"`
	TotalFailures      int     `json:"total_failures"`
	MTBF               string  `json:"mtbf"`
	AvgSession         string  `json:"avg_session"`
	LastFailure        string  `json:"last_failure,omitempty"`
	CurrentStatus      string  `json:"current_status"`
	Trend              string  `json:"trend"` // "stable", "degrading", "improving"
}

// GetStability returns stability quality data for a single camera.
func (m *Manager) GetStability(cameraID string) *StabilityData {
	if m == nil {
		return nil
	}
	q := m.qualityTracker.GetQuality(cameraID)
	if q.CurrentStatus == "unknown" {
		return nil
	}
	trend := m.computeTrend(cameraID)
	return qualityToStabilityData(q, trend)
}

// GetAllStability returns stability quality data for all tracked cameras.
func (m *Manager) GetAllStability() map[string]*StabilityData {
	if m == nil {
		return nil
	}
	all := m.qualityTracker.GetAllQuality()
	result := make(map[string]*StabilityData, len(all))
	for camID := range all {
		q := m.qualityTracker.GetQuality(camID)
		trend := m.computeTrend(camID)
		result[camID] = qualityToStabilityData(q, trend)
	}
	return result
}

// computeTrend compares 1h short window uptime vs 24h long window uptime.
func (m *Manager) computeTrend(cameraID string) string {
	longQ := m.qualityTracker.GetQuality(cameraID)
	shortQ := m.qualityTrackerShort.GetQuality(cameraID)

	// If camera is unknown in either window, can't determine trend
	if longQ.CurrentStatus == "unknown" || shortQ.CurrentStatus == "unknown" {
		return "stable"
	}

	// Compare uptime percentages with a 5% threshold
	delta := shortQ.UptimePercent - longQ.UptimePercent
	switch {
	case delta > 5:
		return "improving"
	case delta < -5:
		return "degrading"
	default:
		return "stable"
	}
}

// qualityToStabilityData converts a ConnectionQuality to a StabilityData.
func qualityToStabilityData(q ConnectionQuality, trend string) *StabilityData {
	data := &StabilityData{
		UptimePercent:  q.UptimePercent,
		TotalFailures:  q.TotalFailures,
		MTBF:           q.MTBF.String(),
		AvgSession:     q.AvgSessionDuration.String(),
		CurrentStatus:  q.CurrentStatus,
		Trend:          trend,
	}
	if !q.LastFailure.IsZero() {
		data.LastFailure = q.LastFailure.Format(time.RFC3339)
	}
	return data
}
