package mqtt

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// manualRecorderLifecycle records ManualRecord calls (#660).
type manualRecorderLifecycle struct {
	fakeLifecycle
	mu      sync.Mutex
	manuals []manualCall
}

type manualCall struct {
	cameraID string
	duration time.Duration
}

func (f *manualRecorderLifecycle) ManualRecord(_ context.Context, cameraID string, d time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.manuals = append(f.manuals, manualCall{cameraID, d})
	return nil
}

func (f *manualRecorderLifecycle) manualCalls() []manualCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]manualCall(nil), f.manuals...)
}

// TestDispatchRecordWithDurationOpensManualWindow: `record` with a duration
// routes to ManualRecord (timed forced recording — footage lands even on
// recording_enabled=false cameras), NOT to the legacy StartCamera.
func TestDispatchRecordWithDurationOpensManualWindow(t *testing.T) {
	fake := &manualRecorderLifecycle{}
	disp := NewActionDispatcher(fake, nil)
	disp("cam-x", "record", 60*time.Second)

	require.Eventually(t, func() bool { return len(fake.manualCalls()) == 1 },
		2*time.Second, 10*time.Millisecond)
	calls := fake.manualCalls()
	require.Equal(t, "cam-x", calls[0].cameraID)
	require.Equal(t, 60*time.Second, calls[0].duration)
	require.Empty(t, fake.started(), "duration route must not use the legacy StartCamera path")
}

// TestDispatchRecordWithoutDurationLegacyStart: plain `record` keeps the
// legacy semantics (start the recorder; disk writes still gated by
// recording_enabled) — documented, unchanged.
func TestDispatchRecordWithoutDurationLegacyStart(t *testing.T) {
	fake := &manualRecorderLifecycle{}
	disp := NewActionDispatcher(fake, nil)
	disp("cam-x", "record", 0)

	require.Eventually(t, func() bool { return len(fake.started()) == 1 },
		2*time.Second, 10*time.Millisecond)
	require.Empty(t, fake.manualCalls(), "no duration → no manual window")
}
