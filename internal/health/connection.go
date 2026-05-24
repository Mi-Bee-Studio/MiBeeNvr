package health

import (
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// cameraConnState tracks per-camera connection state.
type cameraConnState struct {
	currentStatus string
	statusSince   time.Time
	alerted       bool // whether we already emitted connection_lost for this incident
}

// ConnectionMonitor detects camera connection loss and restoration.
// It observes recorder status transitions and emits HealthEvents when a camera
// remains in error/reconnecting state beyond the configured offline threshold.
type ConnectionMonitor struct {
	mu               sync.Mutex
	offlineThreshold time.Duration
	cameras          map[string]*cameraConnState
	eventHandler     func(cameraID string, event model.HealthEvent)
}

// NewConnectionMonitor creates a new connection health monitor.
// The handler callback receives connection_lost and connection_restored events.
func NewConnectionMonitor(offlineThreshold time.Duration, handler func(string, model.HealthEvent)) *ConnectionMonitor {
	return &ConnectionMonitor{
		offlineThreshold: offlineThreshold,
		cameras:          make(map[string]*cameraConnState),
		eventHandler:     handler,
	}
}

// OnStatusChange is called when a camera's recorder status changes.
func (m *ConnectionMonitor) OnStatusChange(cameraID string, status string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	state, exists := m.cameras[cameraID]
	if !exists {
		state = &cameraConnState{currentStatus: status, statusSince: now}
		m.cameras[cameraID] = state
		return
	}

	prevStatus := state.currentStatus
	state.currentStatus = status

	// Status actually changed
	if prevStatus != status {
		// Transition TO error/reconnecting — start the timer
		if status == string(model.StatusError) || status == string(model.StatusReconnecting) {
			state.statusSince = now
			state.alerted = false
		}

		// Transition FROM error/reconnecting TO recording → connection restored
		if isOfflineStatus(prevStatus) && status == string(model.StatusRecording) {
			if state.alerted {
				m.eventHandler(cameraID, model.HealthEvent{
					CameraID:  cameraID,
					EventType: string(model.HealthEventConnectionRestored),
					Status:    string(model.HealthStatusHealthy),
					Message:   "Connection restored",
					Metadata:  `{"downtime":"` + time.Since(state.statusSince).String() + `"}`,
				})
			}
			state.alerted = false
		}
	}
}

// Check is called periodically to detect threshold breaches.
func (m *ConnectionMonitor) Check() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	for cameraID, state := range m.cameras {
		if isOfflineStatus(state.currentStatus) && !state.alerted {
			if now.Sub(state.statusSince) >= m.offlineThreshold {
				state.alerted = true
				m.eventHandler(cameraID, model.HealthEvent{
					CameraID:  cameraID,
					EventType: string(model.HealthEventConnectionLost),
					Status:    string(model.HealthStatusError),
					Message:   "Camera offline",
					Metadata:  `{"offline_duration":"` + now.Sub(state.statusSince).String() + `"}`,
				})
			}
		}
	}
}

// RemoveCamera removes tracking for a camera.
func (m *ConnectionMonitor) RemoveCamera(cameraID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cameras, cameraID)
}

// isOfflineStatus returns true if the status represents a disconnected state.
func isOfflineStatus(status string) bool {
	return status == string(model.StatusError) || status == string(model.StatusReconnecting)
}
