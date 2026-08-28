package camera

// Coverage for the dual-mode timelapse schedule monitor (#583): the
// initial-state decision fires synchronously in the monitor goroutine, so
// both branches are observable via callbacks — no tick waiting (the ticker
// is 1-minute), per the #571 assert-on-observable-state rule.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// atomicStopStub records Stop() calls atomically (monitor goroutine writer,
// test reader — #571 race hygiene).
type atomicStopStub struct {
	statusStub
	stopped atomic.Bool
}

func (a *atomicStopStub) Stop() error {
	a.stopped.Store(true)
	return nil
}

var _ model.Recorder = (*atomicStopStub)(nil)

func TestDualModeScheduleMonitor(t *testing.T) {
	t.Parallel()

	outside := config.CameraTimelapseConfig{
		Enabled:  true,
		Interval: "30s",
		Schedule: &config.ScheduleConfig{
			// A one-minute window far in the past: 00:00-00:01.
			TimeRanges: []config.TimeRange{{Start: "00:00", End: "00:01"}},
		},
	}
	inside := config.CameraTimelapseConfig{
		Enabled:  true,
		Interval: "30s",
		Schedule: &config.ScheduleConfig{
			// All day, every day.
			TimeRanges: []config.TimeRange{{Start: "00:00", End: "23:59"}},
		},
	}

	t.Run("outside hours stops immediately", func(t *testing.T) {
		t.Parallel()
		mgr, _, _, _ := newTestManager(t)
		stopped := make(chan struct{}, 1)
		started := make(chan struct{}, 1)
		mgr.startDualModeTimelapseScheduleMonitor(
			context.Background(), "cam-h264",
			config.CameraConfig{ID: "cam-h264", Timelapse: &outside},
			func() { started <- struct{}{} },
			func() { stopped <- struct{}{} },
		)
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Fatal("outside recording hours: stopFn must fire immediately")
		}
		select {
		case <-started:
			t.Fatal("startFn must not fire outside recording hours")
		case <-time.After(300 * time.Millisecond):
		}
		mgr.Stop() // cancels the monitor via scheduleMonitors teardown
	})

	t.Run("inside hours starts quiet", func(t *testing.T) {
		t.Parallel()
		mgr, _, _, _ := newTestManager(t)
		stopped := make(chan struct{}, 1)
		started := make(chan struct{}, 1)
		mgr.startDualModeTimelapseScheduleMonitor(
			context.Background(), "cam-h264",
			config.CameraConfig{ID: "cam-h264", Timelapse: &inside},
			func() { started <- struct{}{} },
			func() { stopped <- struct{}{} },
		)
		select {
		case <-stopped:
			t.Fatal("inside recording hours: stopFn must not fire at startup")
		case <-time.After(300 * time.Millisecond):
		}
		mgr.Stop()
	})

	t.Run("no schedule no monitor", func(t *testing.T) {
		t.Parallel()
		mgr, _, _, _ := newTestManager(t)
		// Nil/paused schedules return without registering anything.
		mgr.startDualModeTimelapseScheduleMonitor(context.Background(), "c",
			config.CameraConfig{ID: "c", Timelapse: nil}, func() {}, func() {})
		paused := inside
		paused.Paused = true
		mgr.startDualModeTimelapseScheduleMonitor(context.Background(), "c",
			config.CameraConfig{ID: "c", Timelapse: &paused}, func() {}, func() {})
		mgr.auxMu.Lock()
		n := len(mgr.scheduleMonitors)
		mgr.auxMu.Unlock()
		require.Equal(t, 0, n)
	})
}

func TestRecordingScheduleMonitor(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{
		{
			ID: "sched-cam", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://127.0.0.1:1/x",
			// A one-minute window in the far past: outside active hours now.
			RecordingSchedule: &config.ScheduleConfig{
				TimeRanges: []config.TimeRange{{Start: "00:00", End: "00:01"}},
			},
		},
		{ID: "nosched", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://127.0.0.1:1/x"},
	}
	mgr, _, _, _ := newTestManagerWithCfg(t, cfg)

	// A recording-status stub gets stopped immediately (outside hours).
	stopCount := &atomicStopStub{statusStub: statusStub{status: "recording"}}
	mgr.SetTestRecorder("sched-cam", stopCount)
	mgr.startRecordingScheduleMonitor(context.Background(), "sched-cam")

	// Camera without a schedule: the monitor goroutine exits on its own
	// (entry stays until Stop, by design).
	mgr.startRecordingScheduleMonitor(context.Background(), "nosched")

	require.Eventually(t, func() bool { return stopCount.stopped.Load() },
		5*time.Second, 50*time.Millisecond, "outside-hours recorder must be stopped at monitor start")
}
