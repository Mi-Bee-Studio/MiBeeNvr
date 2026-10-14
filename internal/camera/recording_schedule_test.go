package camera

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// startCtxStub records Start() calls and whether the ctx it was handed can
// ever be cancelled (a recorder bound to a cancellable caller ctx dies with it).
type startCtxStub struct {
	statusStub
	started     atomic.Bool
	cancellable atomic.Bool
}

func (s *startCtxStub) Start(ctx context.Context) error {
	s.cancellable.Store(ctx.Done() != nil)
	s.started.Store(true)
	return nil
}

var _ model.Recorder = (*startCtxStub)(nil)

func pastWindowSchedule() *config.ScheduleConfig {
	// A one-minute window right after midnight: outside active hours for the
	// mocked noon clock and (almost always) for the real clock.
	return &config.ScheduleConfig{TimeRanges: []config.TimeRange{{Start: "00:00", End: "00:01"}}}
}

func hasRecordingMonitor(cm *CameraManager, cameraID string) bool {
	cm.auxMu.Lock()
	defer cm.auxMu.Unlock()
	_, ok := cm.scheduleMonitors["rec-"+cameraID]
	return ok
}

// The schedule monitor is cancelled and recreated on every camera update, so a
// recorder it starts must not inherit its ctx.
func TestEnforceRecordingSchedule_StartsRecorderDetached(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{
		{ID: "sched-cam", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://127.0.0.1:1/x"},
	}
	mgr, _, _, _ := newTestManagerWithCfg(t, cfg)
	noon := time.Date(2026, 10, 14, 12, 0, 0, 0, time.Local)
	mgr.scheduler.SetClockForTesting(func() time.Time { return noon })

	stub := &startCtxStub{statusStub: statusStub{status: model.StatusStopped}}
	mgr.SetTestRecorder("sched-cam", stub)

	inside := &config.ScheduleConfig{TimeRanges: []config.TimeRange{{Start: "09:00", End: "18:00"}}}
	mgr.enforceRecordingSchedule("sched-cam", inside, false)
	require.True(t, stub.started.Load(), "stopped recorder inside the window must be started")
	require.False(t, stub.cancellable.Load(), "recorder must not be bound to the monitor's ctx")
}

// Unchecking the schedule in the UI sends an empty schedule: it must clear the
// persisted schedule, drop the monitor, and resume a recorder it had stopped.
func TestUpdateCamera_EmptyRecordingScheduleClearsIt(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{
		{
			ID: "sched-cam", Name: "Sched", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://127.0.0.1:1/x",
			RecordingSchedule: pastWindowSchedule(),
		},
	}
	mgr, _, _, _ := newTestManagerWithCfg(t, cfg)
	ctx := context.Background()

	stub := &startCtxStub{statusStub: statusStub{status: model.StatusStopped}}
	mgr.SetTestRecorder("sched-cam", stub)
	mgr.startRecordingScheduleMonitor(ctx, "sched-cam")

	// An update that doesn't touch the schedule keeps it.
	newName := "Renamed"
	_, err := mgr.UpdateCamera(ctx, "sched-cam", CameraUpdate{Name: &newName})
	require.NoError(t, err)
	require.NotNil(t, mgr.GetCameraConfig("sched-cam").RecordingSchedule)
	require.True(t, hasRecordingMonitor(mgr, "sched-cam"))
	require.False(t, stub.started.Load())

	_, err = mgr.UpdateCamera(ctx, "sched-cam", CameraUpdate{RecordingSchedule: &config.ScheduleConfig{}})
	require.NoError(t, err)
	require.Nil(t, mgr.GetCameraConfig("sched-cam").RecordingSchedule, "empty schedule must clear it")
	require.False(t, hasRecordingMonitor(mgr, "sched-cam"), "monitor must be stopped")
	require.True(t, stub.started.Load(), "recorder stopped by the schedule must resume")
	require.False(t, stub.cancellable.Load())
}

func TestRemoveCamera_StopsRecordingScheduleMonitor(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{
		{
			ID: "sched-cam", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://127.0.0.1:1/x",
			RecordingSchedule: pastWindowSchedule(),
		},
	}
	mgr, _, _, _ := newTestManagerWithCfg(t, cfg)
	ctx := context.Background()

	mgr.startRecordingScheduleMonitor(ctx, "sched-cam")
	require.True(t, hasRecordingMonitor(mgr, "sched-cam"))
	require.NoError(t, mgr.RemoveCamera(ctx, "sched-cam"))
	require.False(t, hasRecordingMonitor(mgr, "sched-cam"), "removed camera must not leak its monitor")
}
