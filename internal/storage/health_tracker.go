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

// cameraHealth holds per-camera storage health state.
type cameraHealth struct {
	state     HealthState
	failCount int
}

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

// getOrCreateCameraHealth returns the camera health tracker for the given
// cameraID, creating a new one with HealthHealthy if it doesn't exist.
// Must be called while holding m.healthMu.
func (m *Manager) getOrCreateCameraHealth(cameraID string) *cameraHealth {
	ch, ok := m.cameraHealths[cameraID]
	if !ok {
		ch = &cameraHealth{state: HealthHealthy}
		m.cameraHealths[cameraID] = ch
	}
	return ch
}

// StorageHealth returns the current health state for a specific camera.
// Unknown cameraIDs default to HealthHealthy.
func (m *Manager) StorageHealth(cameraID string) HealthState {
	m.healthMu.Lock()
	defer m.healthMu.Unlock()
	ch := m.getOrCreateCameraHealth(cameraID)
	return ch.state
}

// StorageFailed reports whether storage for a specific camera is in a failed
// state and writes should be skipped. Unknown cameraIDs default to healthy
// (false) so new cameras are never blocked by stale state.
func (m *Manager) StorageFailed(cameraID string) bool {
	m.healthMu.Lock()
	defer m.healthMu.Unlock()
	ch := m.getOrCreateCameraHealth(cameraID)
	return ch.state >= HealthFailed
}

// StorageFailedLegacy reports whether ANY camera is in a failed state.
// Provided for backward compatibility — prefer StorageFailed(cameraID).
func (m *Manager) StorageFailedLegacy() bool {
	m.healthMu.Lock()
	defer m.healthMu.Unlock()
	for _, ch := range m.cameraHealths {
		if ch.state >= HealthFailed {
			return true
		}
	}
	return false
}

// recordWriteFailureLocked increments the failure counter for a camera and
// escalates health state if threshold is exceeded. Must be called while
// holding m.healthMu. Returns the new health state.
func (m *Manager) recordWriteFailureLocked(cameraID string) HealthState {
	ch := m.getOrCreateCameraHealth(cameraID)
	ch.failCount++
	count := ch.failCount
	prevState := ch.state
	newState := prevState

	if count >= maxConsecutiveFailures {
		newState = HealthFailed
	} else if prevState == HealthHealthy {
		newState = HealthDegraded
	}
	ch.state = newState

	if newState != prevState {
		m.metrics.IncStorageWriteErrors()
		slog.Warn(
			"storage health state changed",
			"camera_id", cameraID,
			"from", healthStateStr(prevState),
			"to", healthStateStr(newState),
			"failures", count,
			"root", m.rootDir,
		)
		m.emitHealthEvent(cameraID, prevState, newState)
	}
	return newState
}

// recordWriteSuccessLocked resets the failure counter for a camera and
// restores health. Must be called while holding m.healthMu.
func (m *Manager) recordWriteSuccessLocked(cameraID string) {
	ch := m.getOrCreateCameraHealth(cameraID)
	prevState := ch.state
	ch.failCount = 0
	ch.state = HealthHealthy

	if prevState != HealthHealthy {
		slog.Info(
			"storage health restored",
			"camera_id", cameraID,
			"from", healthStateStr(prevState),
			"to", "healthy",
			"root", m.rootDir,
		)
		m.emitHealthEvent(cameraID, prevState, HealthHealthy)
	}
}

// recordWriteFailure records a write I/O failure for a specific camera.
// Safe for concurrent use with the main mutex.
func (m *Manager) recordWriteFailure(cameraID string) {
	m.healthMu.Lock()
	m.recordWriteFailureLocked(cameraID)
	m.healthMu.Unlock()
}

// recordWriteSuccess records a successful write I/O operation for a camera.
// Safe for concurrent use with the main mutex.
func (m *Manager) recordWriteSuccess(cameraID string) {
	m.healthMu.Lock()
	m.recordWriteSuccessLocked(cameraID)
	m.healthMu.Unlock()
}

// RecordWriteFailureForPath looks up the cameraID from the temp path mapping
// and records a write I/O failure. If the temp path is unknown, no failure
// is recorded (safe default for external callers).
func (m *Manager) RecordWriteFailureForPath(tempPath string) {
	cameraID := m.lookupCameraByPath(tempPath)
	m.recordWriteFailure(cameraID)
}

// RecordWriteSuccessForPath looks up the cameraID from the temp path mapping
// and records a successful write. If the temp path is unknown, no success
// is recorded (safe default for external callers).
func (m *Manager) RecordWriteSuccessForPath(tempPath string) {
	cameraID := m.lookupCameraByPath(tempPath)
	m.recordWriteSuccess(cameraID)
}

// lookupCameraByPath finds the cameraID registered for the given temp path.
// Returns empty string if not found, which the health methods treat as
// a healthy default.
func (m *Manager) lookupCameraByPath(tempPath string) string {
	m.segMapMu.RLock()
	cameraID := m.segmentCameraMap[tempPath]
	m.segMapMu.RUnlock()
	return cameraID
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

	// Snapshot: check if any camera is in non-healthy state.
	hasIssues := false
	cameraIDs := make([]string, 0, len(m.cameraHealths))
	for id, ch := range m.cameraHealths {
		cameraIDs = append(cameraIDs, id)
		if ch.state != HealthHealthy {
			hasIssues = true
		}
	}
	if !hasIssues {
		m.healthMu.Unlock()
		return
	}

	m.healthMu.Unlock()

	err := m.checkMountPoint()

	m.healthMu.Lock()
	defer m.healthMu.Unlock()

	if err != nil {
		// Mount still unavailable — ensure all cameras are in failed state.
		for _, id := range cameraIDs {
			ch, ok := m.cameraHealths[id]
			if !ok {
				continue
			}
			if ch.state != HealthFailed {
				ch.state = HealthFailed
			}
		}
		slog.Warn("storage mount point inaccessible",
			"root", m.rootDir, "error", err)
		return
	}

	// Mount is accessible again — transition all Failed cameras to Degraded.
	for cameraID, ch := range m.cameraHealths {
		if ch.state == HealthFailed {
			ch.state = HealthDegraded
			ch.failCount = 0
			slog.Info("storage mount point recovered, state set to degraded",
				"root", m.rootDir, "camera_id", cameraID)
			m.emitHealthEvent(cameraID, HealthFailed, HealthDegraded)
		}
	}
}

func (m *Manager) emitHealthEvent(cameraID string, from, to HealthState) {
	if m.eventBus == nil {
		return
	}
	evt := event.StorageHealthChanged{
		CameraID:      cameraID,
		PreviousState: healthStateStr(from),
		CurrentState:  healthStateStr(to),
	}
	m.eventBus.Publish(context.Background(), event.TopicStorageHealthChanged, evt)
}
