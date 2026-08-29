package recorder

import (
	"context"
	"sync"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

// StubRecorder is a no-op recorder that implements model.Recorder.
// It provides a camera struct for the timelapse subsystem when a standalone
// timelapse camera uses rtsp_keyframe frame source but has no host recorder.
// It does not connect to any stream, record frames, or produce output —
// it exists solely to satisfy the recorder interface so the camera can be
// managed by the timelapse schedule monitor.
type StubRecorder struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	status model.RecorderStatus

	Hub *streamhub.StreamHub
}

// GetHub returns the StreamHub for frame fan-out. Always nil for StubRecorder.
func (r *StubRecorder) GetHub() *streamhub.StreamHub { return r.Hub }

// SetHub wires the StreamHub for frame fan-out (streamhub.HubHost).
func (r *StubRecorder) SetHub(hub *streamhub.StreamHub) { r.Hub = hub }

// HubSource labels the hub for the flow-path observability view.
func (r *StubRecorder) HubSource() string { return "stub" }

// Start initializes the stub recorder. It returns immediately without error.
func (r *StubRecorder) Start(_ context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == model.StatusRecording {
		return nil
	}
	_, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.done = make(chan struct{})
	close(r.done)
	r.status = model.StatusRecording
	return nil
}

// Stop stops the stub recorder. It returns immediately without error.
func (r *StubRecorder) Stop() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	r.done = nil
	r.status = model.StatusStopped
	return nil
}

// Status returns the current recorder status.
func (r *StubRecorder) Status() model.RecorderStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

// ensure StubRecorder satisfies model.Recorder at compile time.
var _ model.Recorder = (*StubRecorder)(nil)
