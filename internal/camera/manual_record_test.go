package camera

import (
	"context"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// armableStubSafe is a recorder stub that supports the manual recording
// window surface (#660).
type armableStubSafe struct {
	status model.RecorderStatus
	armed  []time.Duration
}

func (s *armableStubSafe) Start(ctx context.Context) error    { return nil }
func (s *armableStubSafe) Stop() error                        { return nil }
func (s *armableStubSafe) Status() model.RecorderStatus       { return s.status }
func (s *armableStubSafe) ArmManualRecording(d time.Duration) { s.armed = append(s.armed, d) }
func (s *armableStubSafe) ManualRecordingActive() bool        { return len(s.armed) > 0 }

// TestManualRecord_ArmsRunningRecorder: a recording camera gets the window
// armed in place — no stop/start churn.
func TestManualRecord_ArmsRunningRecorder(t *testing.T) {
	t.Parallel()
	cm, _, _, _ := newTestManager(t)
	ctx := context.Background()

	stub := &armableStubSafe{status: model.StatusRecording}
	cm.SetTestRecorder("cam-h264", stub)

	require.NoError(t, cm.ManualRecord(ctx, "cam-h264", 45*time.Second))
	require.Equal(t, []time.Duration{45 * time.Second}, stub.armed)
	require.IsType(t, &armableStubSafe{}, cm.GetRecorder("cam-h264"),
		"running recorder must be armed in place, not replaced")
}

// TestManualRecord_UnsupportedRecorder: a recorder without the manual-window
// surface returns a clear error instead of silently pretending.
func TestManualRecord_UnsupportedRecorder(t *testing.T) {
	t.Parallel()
	cm, _, _, _ := newTestManager(t)

	cm.SetTestRecorder("cam-h264", &statusStub{status: model.StatusRecording})
	err := cm.ManualRecord(context.Background(), "cam-h264", 30*time.Second)
	require.Error(t, err)
	require.Contains(t, err.Error(), "manual")
}

// TestManualRecord_StartsAndArmsWhenNotRunning: a stopped/stale camera is
// started (StartCamera semantics) and the fresh recorder carries an active
// window.
func TestManualRecord_StartsAndArmsWhenNotRunning(t *testing.T) {
	t.Parallel()
	cm, _, _, _ := newTestManager(t)

	cm.SetTestRecorder("cam-h264", &statusStub{status: model.StatusError})
	require.NoError(t, cm.ManualRecord(context.Background(), "cam-h264", 30*time.Second))

	fresh := cm.GetRecorder("cam-h264")
	require.NotNil(t, fresh)
	armed, ok := fresh.(interface{ ManualRecordingActive() bool })
	require.True(t, ok, "fresh recorder must expose the manual-window surface")
	require.True(t, armed.ManualRecordingActive(), "fresh recorder must carry an armed window")
}

// TestManualRecord_UnknownCamera: unknown ID → CameraNotFoundError.
func TestManualRecord_UnknownCamera(t *testing.T) {
	t.Parallel()
	cm, _, _, _ := newTestManager(t)
	err := cm.ManualRecord(context.Background(), "ghost", 30*time.Second)
	var notFound *model.CameraNotFoundError
	require.ErrorAs(t, err, &notFound)
}
