package health

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// HealthStorage interface for health events (for testability).
// Both *storage.DB and mock implementations satisfy this interface.
type HealthStorage interface {
	InsertHealthEvent(ctx context.Context, event model.HealthEvent) error
	GetLatestCameraHealth(ctx context.Context, cameraID string) (*model.HealthEvent, error)
}

// MQTTPublisher interface for MQTT publishing (for testability).
// Both *mqtt.Client and mock implementations satisfy this interface.
type MQTTPublisher interface {
	Publish(topic string, payload any) error
}

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

	// cooldown tracking: "cameraID:eventType" → last emit time
	lastEmitted map[string]time.Time

	// latest status per camera
	cameraStatus map[string]string
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
		lastEmitted:  make(map[string]time.Time),
		cameraStatus: make(map[string]string),
	}
}

// HandleEvent processes a health event through the pipeline.
// Duplicate events (same cameraID + eventType) within the cooldown period are suppressed.
// Returns nil for both dispatched and suppressed events.
func (p *AlertPipeline) HandleEvent(cameraID string, event model.HealthEvent) error {
	key := cameraID + ":" + event.EventType

	p.mu.Lock()
	now := time.Now()

	// Check cooldown
	if lastTime, exists := p.lastEmitted[key]; exists {
		if now.Sub(lastTime) < p.cooldown {
			p.mu.Unlock()
			return nil // suppressed
		}
	}

	// Record emit time
	p.lastEmitted[key] = now

	// Update camera status
	p.cameraStatus[cameraID] = event.Status

	p.mu.Unlock()

	// Dispatch to storage
	if p.storage != nil {
		if err := p.storage.InsertHealthEvent(context.Background(), event); err != nil {
			// Log but don't fail — storage errors shouldn't block alerts
			fmt.Printf("health: failed to store event for %s: %v\n", cameraID, err)
		}
	}

	// Dispatch to MQTT
	if p.mqttEnabled && p.mqttClient != nil {
		topic := "health/" + cameraID
		if err := p.mqttClient.Publish(topic, event); err != nil {
			fmt.Printf("health: failed to publish MQTT event for %s: %v\n", cameraID, err)
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
