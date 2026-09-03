package mqtt

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockStatusMQTT records Publish calls; err (when set) fails every publish.
// attempts counts every call (failed ones included) so tests can observe the
// drain loop's progress even while publishes fail.
type mockStatusMQTT struct {
	mu       sync.Mutex
	topics   []string
	payloads []any
	attempts int
	err      error
}

func (m *mockStatusMQTT) Publish(topic string, payload any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attempts++
	if m.err != nil {
		return m.err
	}
	m.topics = append(m.topics, topic)
	m.payloads = append(m.payloads, payload)
	return nil
}

func (m *mockStatusMQTT) attemptCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.attempts
}

func (m *mockStatusMQTT) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.topics)
}

func (m *mockStatusMQTT) last() (string, any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.topics) == 0 {
		return "", nil
	}
	return m.topics[len(m.topics)-1], m.payloads[len(m.payloads)-1]
}

func (m *mockStatusMQTT) setErr(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

func newStatusTestBus(t *testing.T) *event.EventBus {
	t.Helper()
	return event.NewEventBus(64)
}

func TestStatusPublisher_ForwardsSegmentCompleted(t *testing.T) {
	t.Helper()
	bus := newStatusTestBus(t)
	mock := &mockStatusMQTT{}
	p := NewStatusPublisher(bus, mock)
	require.NoError(t, p.Start(context.Background()))
	defer func() { require.NoError(t, p.Stop()) }()

	seg := event.SegmentCompleted{
		CameraID:    "cam-1",
		FilePath:    "cam-1/2026/rec.mp4",
		Format:      "mp4",
		Encoding:    "h264",
		RecordingID: "rec-42",
	}
	bus.Publish(context.Background(), event.TopicSegmentCompleted, seg)

	require.Eventually(t, func() bool {
		return mock.calls() == 1
	}, 15*time.Second, 50*time.Millisecond)
	topic, payload := mock.last()
	assert.Equal(t, "event/segment.completed", topic)
	assert.Equal(t, seg, payload)
}

func TestStatusPublisher_ForwardsCameraAdded(t *testing.T) {
	t.Helper()
	bus := newStatusTestBus(t)
	mock := &mockStatusMQTT{}
	p := NewStatusPublisher(bus, mock)
	require.NoError(t, p.Start(context.Background()))
	defer func() { require.NoError(t, p.Stop()) }()

	payload := map[string]any{"camera_id": "front-door", "name": "前门"}
	bus.Publish(context.Background(), event.TopicCameraAdded, payload)

	require.Eventually(t, func() bool {
		return mock.calls() == 1
	}, 15*time.Second, 50*time.Millisecond)
	topic, got := mock.last()
	assert.Equal(t, "event/camera.added", topic)
	assert.Equal(t, payload, got)
}

func TestStatusPublisher_IgnoresOtherTopics(t *testing.T) {
	t.Helper()
	bus := newStatusTestBus(t)
	mock := &mockStatusMQTT{}
	p := NewStatusPublisher(bus, mock)
	require.NoError(t, p.Start(context.Background()))
	defer func() { require.NoError(t, p.Stop()) }()

	bus.Publish(context.Background(), event.TopicAIDetection, map[string]any{"camera_id": "cam-1"})

	assert.Never(t, func() bool {
		return mock.calls() > 0
	}, 300*time.Millisecond, 50*time.Millisecond, "non-whitelisted topics must not be forwarded")
}

func TestStatusPublisher_StopUnsubscribes(t *testing.T) {
	t.Helper()
	bus := newStatusTestBus(t)
	mock := &mockStatusMQTT{}
	p := NewStatusPublisher(bus, mock)
	require.NoError(t, p.Start(context.Background()))

	bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{CameraID: "cam-1"})
	require.Eventually(t, func() bool {
		return mock.calls() == 1
	}, 15*time.Second, 50*time.Millisecond)

	require.NoError(t, p.Stop())

	bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{CameraID: "cam-1"})
	assert.Never(t, func() bool {
		return mock.calls() > 1
	}, 300*time.Millisecond, 50*time.Millisecond, "events after Stop must not be forwarded")
}

func TestStatusPublisher_PublishErrorNotFatal(t *testing.T) {
	t.Helper()
	bus := newStatusTestBus(t)
	mock := &mockStatusMQTT{err: errors.New("broker down")}
	p := NewStatusPublisher(bus, mock)
	require.NoError(t, p.Start(context.Background()))
	defer func() { require.NoError(t, p.Stop()) }()

	bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{CameraID: "cam-1"})
	// Wait until the drain loop has processed (and failed) the first event
	// before clearing the error, so exactly one publish can succeed.
	require.Eventually(t, func() bool {
		return mock.attemptCount() == 1
	}, 15*time.Second, 50*time.Millisecond)
	mock.setErr(nil)
	bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{CameraID: "cam-2"})

	require.Eventually(t, func() bool {
		return mock.calls() == 1
	}, 15*time.Second, 50*time.Millisecond, "the second event must still be forwarded after a failed publish")
}

func TestStatusPublisher_NilMQTT_Noop(t *testing.T) {
	t.Helper()
	bus := newStatusTestBus(t)
	p := NewStatusPublisher(bus, nil)
	require.NoError(t, p.Start(context.Background()))

	bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{CameraID: "cam-1"})

	require.NoError(t, p.Stop())
}

func TestStatusPublisher_Name(t *testing.T) {
	t.Helper()
	p := NewStatusPublisher(newStatusTestBus(t), &mockStatusMQTT{})
	assert.Equal(t, "mqtt-status", p.Name())
}

func TestStatusPublisher_ForwardsCameraSnapshot(t *testing.T) {
	t.Helper()
	bus := newStatusTestBus(t)
	mock := &mockStatusMQTT{}
	p := NewStatusPublisher(bus, mock)
	require.NoError(t, p.Start(context.Background()))
	defer func() { require.NoError(t, p.Stop()) }()

	snap := event.CameraSnapshotEvent{
		CameraID:  "front-door",
		FilePath:  "snapshots/front-door/20260903-103005.123.jpg",
		Timestamp: time.Date(2026, 9, 3, 10, 30, 5, 0, time.UTC),
		Trigger:   "mqtt",
	}
	bus.Publish(context.Background(), event.TopicCameraSnapshot, snap)

	require.Eventually(t, func() bool {
		return mock.calls() == 1
	}, 15*time.Second, 50*time.Millisecond)
	topic, got := mock.last()
	assert.Equal(t, "event/camera.snapshot", topic)
	assert.Equal(t, snap, got)
}
