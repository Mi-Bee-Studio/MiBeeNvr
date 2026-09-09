package gb28181

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	gbsip "github.com/mickeyzzc/gb28181-go/platform/sip"
	"github.com/stretchr/testify/require"
)

// fakeSnapshotStore records Persist calls; optionally fails.
type fakeSnapshotStore struct {
	fail  error
	calls []struct {
		cameraID string
		jpegLen  int
	}
}

func (f *fakeSnapshotStore) Persist(cameraID string, jpeg []byte) (string, error) {
	if f.fail != nil {
		return "", f.fail
	}
	f.calls = append(f.calls, struct {
		cameraID string
		jpegLen  int
	}{cameraID, len(jpeg)})
	return fmt.Sprintf("snapshots/%s/%d.jpg", cameraID, len(f.calls)), nil
}

type publishedEvent struct {
	topic string
	data  any
}

func newTestSnapshotManager(t *testing.T) (*SnapshotSessionManager, *fakeSnapshotStore, *[]publishedEvent, *time.Time) {
	t.Helper()
	store := &fakeSnapshotStore{}
	var events []publishedEvent
	now := time.Now()
	m := NewSnapshotSessionManager(store, 30*time.Second, WithSnapshotClock(func() time.Time { return now }), WithSnapshotPublisher(func(topic string, data any) {
		events = append(events, publishedEvent{topic, data})
	}))
	t.Cleanup(m.Stop)
	return m, store, &events, &now
}

var testJPEG = append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 64)...)

func TestSnapshotSessionManager_Lifecycle(t *testing.T) {
	t.Parallel()
	m, store, events, _ := newTestSnapshotManager(t)

	sess, err := m.CreateSession("34020000011320000001", "34020000001320000002", "cam-gb", 2)
	require.NoError(t, err)
	require.Len(t, sess.ID, 32, "SessionID must be 32 chars (spec: 32..128 [A-Za-z0-9-])")

	// First frame received and stored.
	path, err := m.Receive(sess.ID, testJPEG)
	require.NoError(t, err)
	require.Equal(t, "snapshots/cam-gb/1.jpg", path)
	require.Len(t, store.calls, 1)

	// The camera.snapshot event fires per stored file with the gb trigger.
	require.Len(t, *events, 1)
	require.Equal(t, "camera.snapshot", (*events)[0].topic)

	// Completion notify closes the session as complete.
	m.Finish(sess.ID, 2)
	require.Equal(t, SnapshotComplete, sess.Outcome())

	// No further receives after finish.
	_, err = m.Receive(sess.ID, testJPEG)
	require.ErrorIs(t, err, ErrSnapshotSessionClosed)
}

func TestSnapshotSessionManager_TimeoutSweeps(t *testing.T) {
	t.Parallel()
	m, _, _, now := newTestSnapshotManager(t)

	sess, err := m.CreateSession("ch", "dev", "cam-gb", 1)
	require.NoError(t, err)

	*now = now.Add(31 * time.Second) // past the 30s TTL
	m.Sweep()

	require.Equal(t, SnapshotTimeout, sess.Outcome())

	// An expired session is evicted by the sweep — later uploads report an
	// unknown ID (the Closed error is reserved for finished-but-not-yet-
	// swept sessions).
	_, err = m.Receive(sess.ID, testJPEG)
	require.ErrorIs(t, err, ErrSnapshotSessionUnknown)
	m.Sweep()
	_, err = m.Receive(sess.ID, testJPEG)
	require.ErrorIs(t, err, ErrSnapshotSessionUnknown)
}

func TestSnapshotSessionManager_ReceiveValidation(t *testing.T) {
	t.Parallel()
	m, _, _, _ := newTestSnapshotManager(t)

	_, err := m.Receive("no-such-session", testJPEG)
	require.ErrorIs(t, err, ErrSnapshotSessionUnknown)

	sess, err := m.CreateSession("ch", "dev", "cam-gb", 1)
	require.NoError(t, err)

	_, err = m.Receive(sess.ID, []byte("not a jpeg"))
	require.ErrorContains(t, err, "JPEG")

	// Persist failures surface but leave the session open for retries.
	m2store := &fakeSnapshotStore{fail: errors.New("disk full")}
	m2 := NewSnapshotSessionManager(m2store, time.Minute)
	t.Cleanup(m2.Stop)
	s2, err := m2.CreateSession("ch", "dev", "cam-gb", 1)
	require.NoError(t, err)
	_, err = m2.Receive(s2.ID, testJPEG)
	require.ErrorContains(t, err, "disk full")
	require.Equal(t, SnapshotPending, s2.Outcome())
}

func TestSnapshotSessionManager_FinishSignals(t *testing.T) {
	t.Parallel()
	m, _, _, _ := newTestSnapshotManager(t)

	t.Run("empty list is failure", func(t *testing.T) {
		sess, err := m.CreateSession("ch", "dev", "cam-gb", 3)
		require.NoError(t, err)
		m.Finish(sess.ID, 0)
		require.Equal(t, SnapshotFailed, sess.Outcome())
	})

	t.Run("partial list", func(t *testing.T) {
		sess, err := m.CreateSession("ch", "dev", "cam-gb", 3)
		require.NoError(t, err)
		m.Finish(sess.ID, 2)
		require.Equal(t, SnapshotPartial, sess.Outcome())
	})

	t.Run("unknown session finish is a no-op", func(t *testing.T) {
		require.NotPanics(t, func() { m.Finish("ghost", 1) })
	})
}

// The session's event payload must carry the gb trigger + stored path so UI
// automations treat device-captured snapshots like any other.
func TestSnapshotSessionManager_EventPayload(t *testing.T) {
	t.Parallel()
	m, _, events, _ := newTestSnapshotManager(t)

	sess, err := m.CreateSession("34020000011320000001", "dev", "cam-gb", 1)
	require.NoError(t, err)
	_, err = m.Receive(sess.ID, testJPEG)
	require.NoError(t, err)

	require.Len(t, *events, 1)
	ev, ok := (*events)[0].data.(map[string]any)
	require.True(t, ok, "payload is the CameraSnapshotEvent-compatible map")
	require.Equal(t, "cam-gb", ev["camera_id"])
	require.Equal(t, "gb28181", ev["trigger"])
	require.True(t, strings.HasPrefix(fmt.Sprint(ev["file_path"]), "snapshots/cam-gb/"))
}

func TestSnapshotSessionManager_SubscribeFinished(t *testing.T) {
	m, _, _, _ := newTestSnapshotManager(t)
	sess, err := m.CreateSession("34020000011320000001", "34020000001320000002", "cam-gb", 2)
	require.NoError(t, err)

	bus := gbsip.NewEventBus(16)
	m.SubscribeFinished(bus)

	// The device's own completion notify (lib PR #54): 2 of 2 uploaded.
	bus.Publish(context.Background(), gbsip.TopicGB28181SnapshotFinished, gbsip.GB28181SnapshotFinishedEvent{
		DeviceID:     "34020000011320000001",
		SessionID:    sess.ID,
		FileIDs:      []string{"f1", "f2"},
		SuccessCount: 2,
	})
	require.Eventually(t, func() bool { return sess.Outcome() == SnapshotComplete },
		2*time.Second, 10*time.Millisecond)

	// Stray/late notifies for unknown sessions and non-matching payloads are
	// no-ops, not panics.
	bus.Publish(context.Background(), gbsip.TopicGB28181SnapshotFinished,
		gbsip.GB28181SnapshotFinishedEvent{SessionID: strings.Repeat("f", 32)})
	bus.Publish(context.Background(), gbsip.TopicGB28181SnapshotFinished, "garbage")
	time.Sleep(50 * time.Millisecond)

	// Partial report maps to the partial outcome.
	sess2, err := m.CreateSession("34020000011320000001", "34020000001320000002", "cam-gb", 2)
	require.NoError(t, err)
	bus.Publish(context.Background(), gbsip.TopicGB28181SnapshotFinished,
		gbsip.GB28181SnapshotFinishedEvent{SessionID: sess2.ID, SuccessCount: 1})
	require.Eventually(t, func() bool { return sess2.Outcome() == SnapshotPartial },
		2*time.Second, 10*time.Millisecond)

	// Stop unwinds the subscription goroutine.
	m.Stop()
}
