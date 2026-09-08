package tierrec

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
	"github.com/stretchr/testify/require"
)

// tempRegistrySpy records Register/Unregister calls for assertions.
type tempRegistrySpy struct {
	mu           sync.Mutex
	registered   []string
	unregistered []string
}

func (s *tempRegistrySpy) RegisterActiveTemp(path, cameraID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registered = append(s.registered, path)
}

func (s *tempRegistrySpy) UnregisterActiveTemp(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unregistered = append(s.unregistered, path)
}

func (s *tempRegistrySpy) snapshot() (reg, unreg []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.registered...), append([]string(nil), s.unregistered...)
}

// tierrec writes its segment temps directly under cam-*/ trees (not via
// storage.CreateSegment), so the startup temp-cleanup scan can race-delete
// an in-flight segment exactly like the 2026-09-08 production incident. The
// temp must be registered as active for the whole open→finalize window.
func TestSubRecorder_TempRegisteredWhileSegmentOpen(t *testing.T) {
	reg := &tempRegistrySpy{}
	m := NewManager(Config{StorageRoot: t.TempDir(), Store: &fakeStore{}, TempRegistry: reg})
	src := &fakeSource{hub: streamhub.New(), codec: model.FormatH264, sps: testSPS, pps: testPPS}
	r := newSubRecorder(m, "cam-reg", src)

	require.NoError(t, r.openSegmentLocked(model.FormatH264, testSPS, testPPS, nil))
	require.NoError(t, r.mux.WriteSample(r.trackID, []byte{0x65, 0x01}, 0, 33*time.Millisecond))
	r.frames++

	r.mu.Lock()
	tmp := r.tmpPath
	r.mu.Unlock()
	require.NotEmpty(t, tmp)

	registered, _ := reg.snapshot()
	require.Equal(t, []string{tmp}, registered, "temp must be registered (active) when the segment opens")

	r.closeSegmentLocked()

	_, unregistered := reg.snapshot()
	require.Equal(t, []string{tmp}, unregistered, "temp must be unregistered once the segment is finalized")

	r.mu.Lock()
	final := r.finalPath
	r.mu.Unlock()
	_, err := os.Stat(final)
	require.NoError(t, err, "finalized segment file must exist")
}

// If the temp vanishes mid-segment (the cleanup race), closeSegmentLocked
// must not insert a recording row — the file never materialized. This pins
// the existing rename-failure semantics against regression.
func TestSubRecorder_NoRowWhenTempVanished(t *testing.T) {
	store := &fakeStore{}
	m := NewManager(Config{StorageRoot: t.TempDir(), Store: store})
	src := &fakeSource{hub: streamhub.New(), codec: model.FormatH264, sps: testSPS, pps: testPPS}
	r := newSubRecorder(m, "cam-vanish", src)

	require.NoError(t, r.openSegmentLocked(model.FormatH264, testSPS, testPPS, nil))
	require.NoError(t, r.mux.WriteSample(r.trackID, []byte{0x65, 0x01}, 0, 33*time.Millisecond))
	r.frames++

	r.mu.Lock()
	tmp := r.tmpPath
	r.mu.Unlock()
	require.NoError(t, os.Remove(tmp))

	r.closeSegmentLocked()

	require.Zero(t, len(store.rows), "vanished temp must not produce a recording row")
	_ = context.Background()
}
