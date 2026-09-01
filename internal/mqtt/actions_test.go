package mqtt

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeLifecycle records lifecycle calls; blockFor simulates a slow camera dial.
type fakeLifecycle struct {
	mu       sync.Mutex
	starts   []string
	stops    []string
	startErr error
	stopErr  error
	blockFor time.Duration
}

func (f *fakeLifecycle) StartCamera(_ context.Context, cameraID string) error {
	if f.blockFor > 0 {
		time.Sleep(f.blockFor)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts = append(f.starts, cameraID)
	return f.startErr
}

func (f *fakeLifecycle) StopCamera(_ context.Context, cameraID string) error {
	if f.blockFor > 0 {
		time.Sleep(f.blockFor)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops = append(f.stops, cameraID)
	return f.stopErr
}

func (f *fakeLifecycle) started() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.starts...)
}

func (f *fakeLifecycle) stopped() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.stops...)
}

func TestActionDispatcher_Record_StartsCamera(t *testing.T) {
	t.Helper()
	fake := &fakeLifecycle{}
	dispatch := NewActionDispatcher(fake)

	dispatch("cam-1", "record")

	require.Eventually(t, func() bool {
		return len(fake.started()) == 1
	}, 15*time.Second, 50*time.Millisecond, "StartCamera should be called for the record action")
	assert.Equal(t, []string{"cam-1"}, fake.started())
	assert.Empty(t, fake.stopped())
}

func TestActionDispatcher_Stop_StopsCamera(t *testing.T) {
	t.Helper()
	fake := &fakeLifecycle{}
	dispatch := NewActionDispatcher(fake)

	dispatch("cam-1", "stop")

	require.Eventually(t, func() bool {
		return len(fake.stopped()) == 1
	}, 15*time.Second, 50*time.Millisecond, "StopCamera should be called for the stop action")
	assert.Equal(t, []string{"cam-1"}, fake.stopped())
	assert.Empty(t, fake.started())
}

func TestActionDispatcher_UnknownAction_NoCalls(t *testing.T) {
	t.Helper()
	fake := &fakeLifecycle{}
	dispatch := NewActionDispatcher(fake)

	dispatch("cam-1", "reboot")

	assert.Never(t, func() bool {
		return len(fake.started())+len(fake.stopped()) > 0
	}, 500*time.Millisecond, 50*time.Millisecond, "unknown actions must not touch the lifecycle")
}

func TestActionDispatcher_Snapshot_LogsOnly(t *testing.T) {
	t.Helper()
	fake := &fakeLifecycle{}
	dispatch := NewActionDispatcher(fake)

	dispatch("cam-1", "snapshot")

	assert.Never(t, func() bool {
		return len(fake.started())+len(fake.stopped()) > 0
	}, 500*time.Millisecond, 50*time.Millisecond, "snapshot is not wired to the lifecycle yet")
}

func TestActionDispatcher_AlreadyRunning_Idempotent(t *testing.T) {
	t.Helper()
	fake := &fakeLifecycle{startErr: &model.CameraAlreadyRunningError{CameraID: "cam-1"}}
	dispatch := NewActionDispatcher(fake)

	dispatch("cam-1", "record")

	require.Eventually(t, func() bool {
		return len(fake.started()) == 1
	}, 15*time.Second, 50*time.Millisecond)
	// A duplicate trigger must not retry in a loop: the call count stays at 1.
	assert.Never(t, func() bool {
		return len(fake.started()) > 1
	}, 500*time.Millisecond, 50*time.Millisecond)
}

func TestActionDispatcher_StartError_NotFatal(t *testing.T) {
	t.Helper()
	fake := &fakeLifecycle{startErr: errors.New("dial timeout")}
	dispatch := NewActionDispatcher(fake)

	dispatch("cam-1", "record")
	require.Eventually(t, func() bool {
		return len(fake.started()) == 1
	}, 15*time.Second, 50*time.Millisecond)

	// The dispatcher keeps working after a failed action.
	dispatch("cam-1", "stop")
	require.Eventually(t, func() bool {
		return len(fake.stopped()) == 1
	}, 15*time.Second, 50*time.Millisecond)
}

func TestActionDispatcher_NonBlocking(t *testing.T) {
	t.Helper()
	fake := &fakeLifecycle{blockFor: 300 * time.Millisecond}
	dispatch := NewActionDispatcher(fake)

	start := time.Now()
	dispatch("cam-1", "record")
	elapsed := time.Since(start)

	assert.Less(t, elapsed, 50*time.Millisecond, "dispatcher must not block the paho handler goroutine")
}

func TestActionDispatcher_NilLifecycle_Noop(t *testing.T) {
	t.Helper()
	dispatch := NewActionDispatcher(nil)

	dispatch("cam-1", "record")
	dispatch("cam-1", "stop")
	dispatch("cam-1", "snapshot")
	dispatch("cam-1", "bogus")
}
