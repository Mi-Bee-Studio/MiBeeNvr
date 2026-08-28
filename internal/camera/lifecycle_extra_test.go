package camera

// Long-tail coverage for camera lifecycle + substream wiring (#583):
// StartCamera guards, stale-recorder replacement, adapter surface, cascade
// sub-acquirer, and stopCamerasByProtocol. Hermetic — recorders that WOULD
// dial are only exercised on their fail-fast paths (dead loopback ports).

import (
	"context"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/stretchr/testify/require"
)

// statusStub is a recorder whose status the test controls.
type statusStub struct {
	status model.RecorderStatus
}

func (s *statusStub) Start(ctx context.Context) error { return nil }
func (s *statusStub) Stop() error                     { return nil }
func (s *statusStub) Status() model.RecorderStatus    { return s.status }

func TestStartCameraGuards(t *testing.T) {
	t.Parallel()
	cm, _, _, _ := newTestManager(t)
	ctx := context.Background()

	// Unknown camera.
	err := cm.StartCamera(ctx, "ghost")
	var notFound *model.CameraNotFoundError
	require.ErrorAs(t, err, &notFound)

	// Camera already recording → CameraAlreadyRunningError.
	cm.SetTestRecorder("cam-h264", &statusStub{status: model.StatusRecording})
	err = cm.StartCamera(ctx, "cam-h264")
	var running *model.CameraAlreadyRunningError
	require.ErrorAs(t, err, &running)

	// Stale (error-state) recorder is dropped and replaced: the fresh
	// recorder registers immediately and reconnects asynchronously (the
	// manager's non-blocking contract), so success here is the expectation.
	cm.SetTestRecorder("cam-h264", &statusStub{status: model.StatusError})
	require.NoError(t, cm.StartCamera(ctx, "cam-h264"))
	fresh := cm.GetRecorder("cam-h264")
	require.NotNil(t, fresh)
	_, isStub := fresh.(*statusStub)
	require.False(t, isStub, "the stale stub must have been replaced")
}

func TestStopCamerasByProtocol(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{
		{ID: "push-a", Protocol: "srt"},
		{ID: "push-b", Protocol: "rtmp"},
		{ID: "keep", Protocol: "rtsp", Encoding: "h264"},
	}
	cm, _, _, _ := newTestManagerWithCfg(t, cfg)

	srt := recorder.NewIngestRecorder(recorder.IngestConfig{CameraID: "push-a"})
	rtmp := recorder.NewIngestRecorder(recorder.IngestConfig{CameraID: "push-b"})
	rtsp := recorder.NewH264Recorder(recorder.H264Config{CameraID: "keep"}, nil)
	cm.SetTestRecorder("push-a", srt)
	cm.SetTestRecorder("push-b", rtmp)
	cm.SetTestRecorder("keep", rtsp)

	cm.stopCamerasByProtocol("srt")
	cm.stopCamerasByProtocol("rtmp")

	require.Nil(t, cm.GetRecorder("push-a"))
	require.Nil(t, cm.GetRecorder("push-b"))
	require.NotNil(t, cm.GetRecorder("keep"), "rtsp camera must be untouched")
}

func TestAdapterNameAndCascadeSubAcquirer(t *testing.T) {
	t.Parallel()
	cm, _, _, _ := newTestManager(t)

	// Manager + public adapter names.
	require.Equal(t, "camera", cm.Name())

	// Cascade sub-acquirer fails cleanly for cameras without a sub config.
	acq := NewCascadeSubAcquirer(cm)
	_, _, err := acq.AcquireSubHub(context.Background(), "cam-h264")
	require.Error(t, err, "no sub-stream configured → explicit error, not nil hub")
}
