package mqtt

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSnapshotRunner records RunSnapshot calls for the dispatcher tests.
type fakeSnapshotRunner struct {
	mu    sync.Mutex
	calls []string
	path  string
	err   error
}

func (f *fakeSnapshotRunner) RunSnapshot(_ context.Context, cameraID string, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, cameraID)
	return f.path, f.err
}

func (f *fakeSnapshotRunner) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func TestActionDispatcher_Snapshot_RunsCapture(t *testing.T) {
	t.Helper()
	fake := &fakeSnapshotRunner{path: "snapshots/cam-1/x.jpg"}
	dispatch := NewActionDispatcher(&fakeLifecycle{}, fake)

	dispatch("cam-1", "snapshot")

	require.Eventually(t, func() bool {
		return len(fake.recorded()) == 1
	}, 15*time.Second, 50*time.Millisecond, "RunSnapshot should be called for the snapshot action")
	assert.Equal(t, []string{"cam-1"}, fake.recorded())
}

func TestActionDispatcher_Snapshot_OwnGoroutine(t *testing.T) {
	t.Helper()
	// The paho message handler must never block: dispatch returns before the
	// (slow) snapshot capture finishes.
	block := make(chan struct{})
	late := make(chan struct{})
	slow := &blockingSnapshotRunner{block: block, done: late}
	dispatch := NewActionDispatcher(&fakeLifecycle{}, slow)

	returned := make(chan struct{})
	go func() {
		dispatch("cam-1", "snapshot")
		close(returned)
	}()
	select {
	case <-returned:
		// good: dispatch returned while capture is still blocked
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch blocked on the snapshot capture — must run on its own goroutine")
	}

	close(block) // release the capture
	select {
	case <-late:
	case <-time.After(5 * time.Second):
		t.Fatal("snapshot capture never ran")
	}
}

type blockingSnapshotRunner struct {
	block chan struct{}
	done  chan struct{}
}

func (b *blockingSnapshotRunner) RunSnapshot(_ context.Context, _ string, _ string) (string, error) {
	<-b.block
	close(b.done)
	return "snapshots/cam-1/x.jpg", nil
}

func TestActionDispatcher_Snapshot_NilRunner_NoPanic(t *testing.T) {
	t.Helper()
	dispatch := NewActionDispatcher(&fakeLifecycle{}, nil)
	require.NotPanics(t, func() { dispatch("cam-1", "snapshot") })
}

func TestActionDispatcher_Snapshot_FailureLoggedNotFatal(t *testing.T) {
	t.Helper()
	fake := &fakeSnapshotRunner{err: errors.New("no frame")}
	dispatch := NewActionDispatcher(&fakeLifecycle{}, fake)

	require.NotPanics(t, func() { dispatch("cam-1", "snapshot") })

	require.Eventually(t, func() bool {
		return len(fake.recorded()) == 1
	}, 15*time.Second, 50*time.Millisecond, "failed capture must still be attempted (and logged), not skipped")
}

func TestActionDispatcher_Snapshot_DoesNotTouchLifecycle(t *testing.T) {
	t.Helper()
	life := &fakeLifecycle{}
	dispatch := NewActionDispatcher(life, &fakeSnapshotRunner{path: "p"})

	dispatch("cam-1", "snapshot")

	assert.Never(t, func() bool {
		return len(life.started()) > 0 || len(life.stopped()) > 0
	}, 500*time.Millisecond, 50*time.Millisecond, "snapshot action must never start/stop the camera")
}
