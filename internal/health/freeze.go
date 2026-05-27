package health

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// cameraFreezeState tracks per-camera freeze detection state.
type cameraFreezeState struct {
	lastFrameTime atomic.Value // stores time.Time
	isRecording   atomic.Bool
	frozen        bool
	freezeSince   time.Time
}

// FreezeDetector detects frozen video streams by monitoring frame timestamps.
// It operates in Layer 2.5 of the health monitoring system.
type FreezeDetector struct {
	mu             sync.Mutex
	freezeTimeout  time.Duration
	cameraOverrides map[string]time.Duration // per-camera freeze timeout overrides
	cameras        map[string]*cameraFreezeState
	eventHandler   func(cameraID string, event model.HealthEvent)
}

// NewFreezeDetector creates a new freeze detector.
func NewFreezeDetector(freezeTimeout time.Duration, handler func(string, model.HealthEvent)) *FreezeDetector {
	return &FreezeDetector{
		freezeTimeout:   freezeTimeout,
		cameraOverrides: make(map[string]time.Duration),
		cameras:         make(map[string]*cameraFreezeState),
		eventHandler:    handler,
	}
}

// OnFrame returns a callback for the camera's StreamHub subscription.
// MUST be non-blocking — only stores the current timestamp.
func (f *FreezeDetector) OnFrame(cameraID string) func(pts int64, au [][]byte) {
	state := f.getOrCreateState(cameraID)
	return func(pts int64, au [][]byte) {
		state.lastFrameTime.Store(time.Now())
	}
}

// getOrCreateState returns the state for a camera, creating one if it doesn't exist.
func (f *FreezeDetector) getOrCreateState(cameraID string) *cameraFreezeState {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.cameras[cameraID]; !ok {
		s := &cameraFreezeState{}
		s.lastFrameTime.Store(time.Now())
		f.cameras[cameraID] = s
	}
	return f.cameras[cameraID]
}

// SetRecording updates the recording state for a camera.
// When recording starts, the frame timer is reset to prevent false positives.
func (f *FreezeDetector) SetRecording(cameraID string, recording bool) {
	state := f.getOrCreateState(cameraID)
	wasRecording := state.isRecording.Load()
	state.isRecording.Store(recording)
	if recording && !wasRecording {
		state.lastFrameTime.Store(time.Now())
		state.frozen = false
	}
}

// Check is called periodically to detect freezes.
// Only checks cameras that are currently recording.
func (f *FreezeDetector) Check() {
	f.mu.Lock()
	defer f.mu.Unlock()

	now := time.Now()
	for cameraID, state := range f.cameras {
		if !state.isRecording.Load() {
			continue
		}

		lastFrame, ok := state.lastFrameTime.Load().(time.Time)
		if !ok {
			continue
		}

		elapsed := now.Sub(lastFrame)

		timeout := f.freezeTimeout
		if t, ok := f.cameraOverrides[cameraID]; ok && t > 0 {
			timeout = t
		}

		if !state.frozen && elapsed > timeout {
			state.frozen = true
			state.freezeSince = lastFrame
			meta, _ := json.Marshal(map[string]any{"frozen_for": elapsed.String()})
			f.eventHandler(cameraID, model.HealthEvent{
				CameraID:  cameraID,
				EventType: string(model.HealthEventFreezeDetected),
				Status:    string(model.HealthStatusError),
				Message:   "Video freeze detected",
				Metadata:  string(meta),
			})
		}
	}
}

// OnFrameReceived is called when a frame arrives after a freeze.
// It triggers a recovery event if the camera was in frozen state.
func (f *FreezeDetector) OnFrameReceived(cameraID string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	state, ok := f.cameras[cameraID]
	if !ok || !state.frozen {
		return
	}

	frozenDuration := time.Since(state.freezeSince)
	state.frozen = false
	meta, _ := json.Marshal(map[string]any{"frozen_duration": frozenDuration.String()})
	f.eventHandler(cameraID, model.HealthEvent{
		CameraID:  cameraID,
		EventType: string(model.HealthEventFreezeRecovered),
		Status:    string(model.HealthStatusHealthy),
		Message:   "Video recovered from freeze",
		Metadata:  string(meta),
	})
}

// RemoveCamera removes tracking for a camera.
func (f *FreezeDetector) RemoveCamera(cameraID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.cameras, cameraID)
	delete(f.cameraOverrides, cameraID)
}

// SetCameraOverride sets the per-camera freeze timeout override.
func (f *FreezeDetector) SetCameraOverride(cameraID string, timeout time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cameraOverrides[cameraID] = timeout
}
