package storage

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
)

// HealthState represents the health state of the storage subsystem.
type HealthState int

const (
	// HealthHealthy indicates storage is operating normally.
	HealthHealthy HealthState = iota
	// HealthDegraded indicates some I/O errors but may still be writable.
	HealthDegraded
	// HealthFailed indicates storage is unavailable and writes should be skipped.
	HealthFailed
)

const (
	// maxConsecutiveFailures before escalating to HealthFailed.
	maxConsecutiveFailures = 3
	// healthCheckInterval for periodic mount point checks when in failed state.
	healthCheckInterval = 30 * time.Second
)

// healthStateStr returns a human-readable string for the health state.
func healthStateStr(s HealthState) string {
	switch s {
	case HealthHealthy:
		return "healthy"
	case HealthDegraded:
		return "degraded"
	case HealthFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// StorageHealth returns the current health state of the storage.
func (m *Manager) StorageHealth() HealthState {
	m.healthMu.Lock()
	defer m.healthMu.Unlock()
	return m.healthState
}

// recordWriteFailureLocked increments the failure counter and escalates
// health state if threshold is exceeded. Must be called while holding m.healthMu.
// Returns the new health state.
func (m *Manager) recordWriteFailureLocked() HealthState {
	m.writeFailCount++
	count := m.writeFailCount
	prevState := m.healthState
	newState := prevState

	if count >= maxConsecutiveFailures {
		newState = HealthFailed
	} else if prevState == HealthHealthy {
		newState = HealthDegraded
	}
	m.healthState = newState

	if newState != prevState {
		m.metrics.IncStorageWriteErrors()
		slog.Warn("storage health state changed",
			"from", healthStateStr(prevState),
			"to", healthStateStr(newState),
			"failures", count,
			"root", m.rootDir,
		)
		m.emitHealthEvent(prevState, newState)
	}
	return newState
}

// recordWriteSuccessLocked resets the failure counter and restores health.
// Must be called while holding m.healthMu.
func (m *Manager) recordWriteSuccessLocked() {
	prevState := m.healthState
	m.writeFailCount = 0
	m.healthState = HealthHealthy

	if prevState != HealthHealthy {
		slog.Info("storage health restored",
			"from", healthStateStr(prevState),
			"to", "healthy",
			"root", m.rootDir,
		)
		m.emitHealthEvent(prevState, HealthHealthy)
	}
}

// recordWriteFailure records a write I/O failure.
// Safe for concurrent use with the main mutex.
func (m *Manager) recordWriteFailure() {
	m.healthMu.Lock()
	m.recordWriteFailureLocked()
	m.healthMu.Unlock()
}

// recordWriteSuccess records a successful write I/O operation.
// Safe for concurrent use with the main mutex.
func (m *Manager) recordWriteSuccess() {
	m.healthMu.Lock()
	m.recordWriteSuccessLocked()
	m.healthMu.Unlock()
}

// SetEventBus attaches an event bus for publishing health state change notifications.
func (m *Manager) SetEventBus(bus *event.EventBus) {
	m.healthMu.Lock()
	defer m.healthMu.Unlock()
	m.eventBus = bus
}

// checkMountPoint checks whether the storage root directory is accessible.
func (m *Manager) checkMountPoint() error {
	_, err := os.Stat(m.rootDir)
	return err
}

// StartHealthCheck starts a background goroutine that periodically checks
// storage health when in a degraded or failed state, and attempts automatic
// recovery when the mount point becomes accessible again.
func (m *Manager) StartHealthCheck(ctx context.Context) {
	go m.healthCheckLoop(ctx)
}

func (m *Manager) healthCheckLoop(ctx context.Context) {
	ticker := time.NewTicker(healthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.performHealthCheck()
		}
	}
}

func (m *Manager) performHealthCheck() {
	m.healthMu.Lock()
	state := m.healthState
	m.healthMu.Unlock()

	// Only check mount when in degraded or failed states.
	if state == HealthHealthy {
		return
	}

	err := m.checkMountPoint()
	if err != nil {
		// Mount still unavailable — ensure failed state.
		if state != HealthFailed {
			m.healthMu.Lock()
			m.healthState = HealthFailed
			m.healthMu.Unlock()
			slog.Warn("storage mount point inaccessible",
				"root", m.rootDir, "error", err)
		}
		return
	}

	// Mount is accessible again — transition to degraded for one write attempt.
	if state == HealthFailed {
		m.healthMu.Lock()
		m.healthState = HealthDegraded
		m.writeFailCount = 0
		m.healthMu.Unlock()
		slog.Info("storage mount point recovered, state set to degraded",
			"root", m.rootDir)
		m.emitHealthEvent(state, HealthDegraded)
	}
}

func (m *Manager) emitHealthEvent(from, to HealthState) {
	if m.eventBus == nil {
		return
	}
	evt := event.StorageHealthChanged{
		PreviousState: healthStateStr(from),
		CurrentState:  healthStateStr(to),
	}
	m.eventBus.Publish(context.Background(), event.TopicStorageHealthChanged, evt)
}
