package health

import (
	"context"
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

	statusFn      StatusFunc
	knownStatuses map[string]string

	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
}

// NewManager creates a health manager. Returns nil if health monitoring is disabled.
func NewManager(cfg config.HealthConfig) *Manager {
	if !cfg.Enabled {
		return nil
	}

	// Parse durations from config
	offlineThreshold, _ := time.ParseDuration(cfg.Layer1.OfflineThreshold)
	cooldown, _ := time.ParseDuration(cfg.Alerts.Cooldown)
	maxIDRInterval, _ := time.ParseDuration(cfg.Layer2.MaxIDRInterval)
	freezeTimeout, _ := time.ParseDuration(cfg.Layer2_5.FreezeTimeout)

	// Create alert pipeline
	pipeline := NewAlertPipeline(cooldown, cfg.Alerts.MQTT, nil, nil, "mibee-nvr")

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

	return &Manager{
		cfg:       cfg,
		conn:      conn,
		collector: collector,
		freeze:    freeze,
		pipeline:  pipeline,
		knownStatuses: make(map[string]string),
		done:      make(chan struct{}),
	}


}
// Start begins the periodic health check loop.
func (m *Manager) Start(ctx context.Context) error {
	if m == nil {
		return nil
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
			m.pollStatuses()
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

// OnCameraAdded starts monitoring a newly added camera.
func (m *Manager) OnCameraAdded(cameraID string, recorder model.Recorder) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

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

	// Update pipeline status for API queries
	if isRecording {
		m.pipeline.SetCameraStatus(cameraID, string(model.HealthStatusHealthy))
	}
}
// GetCameraHealth returns the current health status for a camera.
func (m *Manager) GetCameraHealth(cameraID string) *model.CameraHealth {
	if m == nil {
		return nil
	}
	status := m.pipeline.GetCameraStatus(cameraID)
	return &model.CameraHealth{
		CameraID:     cameraID,
		LatestStatus: status,
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
		result[camID] = &model.CameraHealth{
			CameraID:     camID,
			LatestStatus: status,
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
func (m *Manager) pollStatuses() {
	if m.statusFn == nil {
		return
	}
	statuses := m.statusFn()
	for cameraID, status := range statuses {
		if prev, ok := m.knownStatuses[cameraID]; ok && prev != status {
			m.OnStatusChange(cameraID, status)
		}
		m.knownStatuses[cameraID] = status
	}
}
