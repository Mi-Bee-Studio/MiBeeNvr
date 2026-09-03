package snapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Persistor ---------------------------------------------------------------

func TestPersistor_Persist(t *testing.T) {
	root := t.TempDir()
	fixed := time.Date(2026, 9, 3, 10, 30, 5, 123456789, time.UTC)
	p := &Persistor{Root: root, Now: func() time.Time { return fixed }}

	rel, err := p.Persist("front-door", []byte("jpeg-bytes"))
	require.NoError(t, err)
	assert.Equal(t, "snapshots/front-door/20260903-103005.123.jpg", rel)

	got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	require.NoError(t, err)
	assert.Equal(t, []byte("jpeg-bytes"), got)

	// Atomic write leaves no temp files behind.
	entries, err := os.ReadDir(filepath.Join(root, "snapshots", "front-door"))
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}

func TestPersistor_SameTimestamp_NoOverwrite(t *testing.T) {
	root := t.TempDir()
	fixed := time.Date(2026, 9, 3, 10, 30, 5, 123456789, time.UTC)
	p := &Persistor{Root: root, Now: func() time.Time { return fixed }}

	rel1, err := p.Persist("cam", []byte("first"))
	require.NoError(t, err)
	rel2, err := p.Persist("cam", []byte("second"))
	require.NoError(t, err)

	require.NotEqual(t, rel1, rel2, "same-millisecond persists must not overwrite")
	b1, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel1)))
	require.NoError(t, err)
	b2, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel2)))
	require.NoError(t, err)
	assert.Equal(t, []byte("first"), b1)
	assert.Equal(t, []byte("second"), b2)
}

func TestPersistor_CameraIDPathGuard(t *testing.T) {
	// A camera ID must never escape its snapshots/ subdirectory.
	root := t.TempDir()
	p := &Persistor{Root: root, Now: func() time.Time { return time.Now() }}

	rel, err := p.Persist("../evil", []byte("x"))
	require.NoError(t, err)
	assert.NotContains(t, rel, "..", "relative path must stay under snapshots/")
	_, err = os.Stat(filepath.Join(root, "snapshots", ".."))
	require.NoError(t, err, "sanity: parent still exists")
	full := filepath.Join(root, filepath.FromSlash(rel))
	under, err := filepath.Rel(filepath.Join(root, "snapshots"), full)
	require.NoError(t, err)
	assert.NotContains(t, under, "..", "file must live under snapshots/")
}

// --- Runner ------------------------------------------------------------------

type fakeBus struct {
	mu     sync.Mutex
	events []event.Event
}

func (b *fakeBus) Publish(_ context.Context, topic string, data any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, event.Event{Topic: topic, Data: data})
}

func (b *fakeBus) collected() []event.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]event.Event(nil), b.events...)
}

type fakeFrameSource struct {
	jpeg []byte
	err  error
}

func (f *fakeFrameSource) Capture(string) ([]byte, error) { return f.jpeg, f.err }

func TestRunner_RunSnapshot_PersistsAndPublishes(t *testing.T) {
	root := t.TempDir()
	bus := &fakeBus{}
	fixed := time.Date(2026, 9, 3, 11, 0, 0, 0, time.UTC)
	r := &Runner{
		Source:  &fakeFrameSource{jpeg: []byte("snap-jpeg")},
		Storage: &Persistor{Root: root},
		Bus:     bus,
		Now:     func() time.Time { return fixed },
	}

	rel, err := r.RunSnapshot(context.Background(), "cam-1", "mqtt")
	require.NoError(t, err)

	got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	require.NoError(t, err)
	assert.Equal(t, []byte("snap-jpeg"), got)

	events := bus.collected()
	require.Len(t, events, 1)
	assert.Equal(t, event.TopicCameraSnapshot, events[0].Topic)
	// The bus stores Data as any; the typed payload must round-trip.
	if ev, ok := events[0].Data.(event.CameraSnapshotEvent); ok {
		assert.Equal(t, "cam-1", ev.CameraID)
		assert.Equal(t, rel, ev.FilePath)
		assert.Equal(t, "mqtt", ev.Trigger)
		assert.True(t, ev.Timestamp.Equal(fixed))
	} else {
		t.Fatalf("event payload is %T, want event.CameraSnapshotEvent", events[0].Data)
	}
}

func TestRunner_RunSnapshot_CaptureFails_NoEventNoFile(t *testing.T) {
	root := t.TempDir()
	bus := &fakeBus{}
	r := &Runner{
		Source:  &fakeFrameSource{err: ErrNoFrame},
		Storage: &Persistor{Root: root},
		Bus:     bus,
		Now:     time.Now,
	}

	_, err := r.RunSnapshot(context.Background(), "cam-1", "mqtt")
	require.ErrorIs(t, err, ErrNoFrame)
	assert.Empty(t, bus.collected())

	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	assert.Empty(t, entries, "failed capture must not create files")
}

func TestRunner_RunSnapshot_NilBus_NoPanic(t *testing.T) {
	r := &Runner{
		Source:  &fakeFrameSource{jpeg: []byte("j")},
		Storage: &Persistor{Root: t.TempDir()},
		Now:     time.Now,
	}
	_, err := r.RunSnapshot(context.Background(), "cam", "mqtt")
	require.NoError(t, err)
}

func TestRunner_RunSnapshot_NilSource_Errors(t *testing.T) {
	r := &Runner{Storage: &Persistor{Root: t.TempDir()}, Now: time.Now}
	_, err := r.RunSnapshot(context.Background(), "cam", "mqtt")
	require.Error(t, err)
	assert.NotErrorIs(t, err, errors.New("different"))
}
