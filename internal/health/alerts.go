package health

import (
	"context"
	"log/slog"
	"sync"
	"time"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// HealthStorage interface for health events (for testability).
// Both *storage.DB and mock implementations satisfy this interface.
type HealthStorage interface {
	InsertHealthEvent(ctx context.Context, event model.HealthEvent) error
	GetLatestCameraHealth(ctx context.Context, cameraID string) (*model.HealthEvent, error)
	DeleteHealthEventsByType(ctx context.Context, eventType string, before time.Time) (int64, error)
}

// MQTTPublisher interface for MQTT publishing (for testability).
// Both *mqtt.Client and mock implementations satisfy this interface.
type MQTTPublisher interface {
	Publish(topic string, payload any) error
}

// emitState tracks cooldown and escalation state for a specific cooldown key.
type emitState struct {
	lastEmit  time.Time
	firstEmit time.Time
	count     int
}

// escalationWindow is the time window within which repeated emissions escalate.
const escalationWindow = 1 * time.Hour

// AlertPipeline handles event deduplication and dispatch.
// It suppresses duplicate events within a cooldown window and dispatches
// to both SQLite storage and MQTT.
type AlertPipeline struct {
	mu          sync.Mutex
	cooldown    time.Duration
	mqttEnabled bool
	storage     HealthStorage
	mqttClient  MQTTPublisher
	topicPrefix string

	// cooldown tracking: "cameraID:eventType:message" → emit state
	emitStates map[string]*emitState

	// latest status per camera
	cameraStatus map[string]string

	// recent anomaly timestamps per camera (for health score computation)
	anomalyTimes map[string][]time.Time
}

// NewAlertPipeline creates a new alert pipeline.
func NewAlertPipeline(
	cooldown time.Duration,
	mqttEnabled bool,
	store HealthStorage,
	mqttClient MQTTPublisher,
	topicPrefix string,
) *AlertPipeline {
	return &AlertPipeline{
		cooldown:     cooldown,
		mqttEnabled:  mqttEnabled,
		storage:      store,
		mqttClient:   mqttClient,
		topicPrefix:  topicPrefix,
		emitStates:   make(map[string]*emitState),
		cameraStatus: make(map[string]string),
		anomalyTimes: make(map[string][]time.Time),
	}
}

// HandleEvent processes a health event through the pipeline.
// Duplicate events (same cameraID + eventType + message) within the cooldown period are suppressed.
// Repeated same-message events within the escalation window are escalated to error.
// Returns nil for both dispatched and suppressed events.
func (p *AlertPipeline) HandleEvent(cameraID string, event model.HealthEvent) error {
	key := cameraID + ":" + event.EventType + ":" + event.Message

	p.mu.Lock()
	now := time.Now()

	// Ensure CreatedAt is set (event producers may not set it)
	if event.CreatedAt.IsZero() {
		event.CreatedAt = now
	}

	// Get or create emit state
	state, ok := p.emitStates[key]
	if !ok {
		state = &emitState{}
		p.emitStates[key] = state
	}

	// Check cooldown
	if !state.lastEmit.IsZero() && now.Sub(state.lastEmit) < p.cooldown {
		p.mu.Unlock()
		return nil // suppressed
	}

	// Reset escalation window if expired
	if !state.firstEmit.IsZero() && now.Sub(state.firstEmit) >= escalationWindow {
		state.count = 0
		state.firstEmit = now
	}

	// Set first emit time on first emission
	if state.firstEmit.IsZero() {
		state.firstEmit = now
	}

	// Increment count and escalate if this is a repeat
	state.count++
	if state.count > 1 {
		event.Status = string(model.HealthStatusError)
	}

	// Record emit time
	state.lastEmit = now

	// Update camera status
	p.cameraStatus[cameraID] = event.Status

	// Track anomaly events for health score (stream_anomaly, freeze_detected)
	if isAnomalyEvent(event.EventType) {
		p.anomalyTimes[cameraID] = append(p.anomalyTimes[cameraID], now)
	}

	p.mu.Unlock()

	// Dispatch to storage
	if p.storage != nil {
		if err := p.storage.InsertHealthEvent(context.Background(), event); err != nil {
			// Log but don't fail — storage errors shouldn't block alerts
			slog.Warn("failed to store health event", "camera_id", cameraID, "error", err)
		}
	}

	// Dispatch to MQTT
	if p.mqttEnabled && p.mqttClient != nil {
		topic := "health/" + cameraID
		if err := p.mqttClient.Publish(topic, event); err != nil {
			slog.Warn("failed to publish MQTT health event", "camera_id", cameraID, "error", err)
		}
	}
	return nil
}

// GetCameraStatus returns the current health status for a camera.
// Returns HealthStatusUnknown if no events have been received for the camera.
func (p *AlertPipeline) GetCameraStatus(cameraID string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if status, ok := p.cameraStatus[cameraID]; ok {
		return status
	}
	return string(model.HealthStatusUnknown)
}

// GetAllStatuses returns a copy of all camera health statuses.
func (p *AlertPipeline) GetAllStatuses() map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make(map[string]string, len(p.cameraStatus))
	for k, v := range p.cameraStatus {
		result[k] = v
	}
	return result
}

// SetCameraStatus initializes the health status for a camera.
func (p *AlertPipeline) SetCameraStatus(cameraID, status string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cameraStatus[cameraID] = status
}

// GetAnomalyCount returns the number of anomaly events in the last hour for a camera.
// Anomaly events include stream_anomaly and freeze_detected.
func (p *AlertPipeline) GetAnomalyCount(cameraID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	cutoff := time.Now().Add(-1 * time.Hour)
	times := p.anomalyTimes[cameraID]
	count := 0
	for _, t := range times {
		if t.After(cutoff) {
			count++
		}
	}
	return count
}

// CleanStaleAnomalies removes anomaly records older than 1 hour.
func (p *AlertPipeline) CleanStaleAnomalies() {
	p.mu.Lock()
	defer p.mu.Unlock()
	cutoff := time.Now().Add(-1 * time.Hour)
	for cameraID, times := range p.anomalyTimes {
		var fresh []time.Time
		for _, t := range times {
			if t.After(cutoff) {
				fresh = append(fresh, t)
			}
		}
		if len(fresh) == 0 {
			delete(p.anomalyTimes, cameraID)
		} else {
			p.anomalyTimes[cameraID] = fresh
		}
	}
}

// isAnomalyEvent returns true if the event type is a stream anomaly.
func isAnomalyEvent(eventType string) bool {
	return eventType == string(model.HealthEventStreamAnomaly) || eventType == string(model.HealthEventFreezeDetected)
}
