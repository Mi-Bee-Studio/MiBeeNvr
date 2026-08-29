package streamhub

// Tests for the #469 observability additions: Snapshot(), per-consumer
// bytes/dwell, SubscribeMsg IngestAt relay, compositional drop callbacks,
// and IDR-drop accounting in trySendIDR.

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

func TestStreamHub_Snapshot(t *testing.T) {
	hub := New()
	hub.SetCameraID("cam-snap")
	hub.SetSource("h264")

	var mu sync.Mutex
	received := 0
	require.NoError(t, hub.Subscribe("consumer-a", func(pts int64, au [][]byte) {
		mu.Lock()
		received++
		mu.Unlock()
	}))

	hub.Broadcast(100, [][]byte{make([]byte, 10), make([]byte, 5)}, false)
	hub.Broadcast(200, [][]byte{make([]byte, 20)}, true)

	// Drain happens asynchronously; sends are counted at enqueue so they're
	// already visible in Snapshot without waiting.
	snap := hub.Snapshot()
	require.Equal(t, "cam-snap", snap.CameraID)
	require.Equal(t, "h264", snap.Source)
	require.Equal(t, int64(2), snap.FramesIn)
	require.Equal(t, int64(35), snap.BytesIn)
	require.False(t, snap.LastFrameAt.IsZero())
	require.Len(t, snap.Consumers, 1)

	c := snap.Consumers[0]
	require.Equal(t, "consumer-a", c.ID)
	require.Equal(t, int64(2), c.Sends)
	require.Equal(t, int64(35), c.Bytes)
	require.Equal(t, int64(0), c.Drops)
	require.Equal(t, 150, c.BufferCapacity)
	require.False(t, c.SubscribedAt.IsZero())
	require.False(t, c.LastSendAt.IsZero())

	// Snapshot must be JSON-serializable for the flow API.
	_, err := json.Marshal(snap)
	require.NoError(t, err)
}

func TestStreamHub_SnapshotEmpty(t *testing.T) {
	hub := New()
	hub.SetCameraID("cam-empty")
	snap := hub.Snapshot()
	require.Empty(t, snap.Consumers)
	require.True(t, snap.LastFrameAt.IsZero())
	require.NotNil(t, snap.Consumers) // serialized as [] not null
}

func TestStreamHub_SubscribeMsgRelaysIngestAt(t *testing.T) {
	hub := New()
	hub.SetCameraID("cam-msg")

	got := make(chan model.FrameMsg, 4)
	require.NoError(t, hub.SubscribeMsg("ws-cam-msg", func(msg model.FrameMsg) {
		got <- msg
	}))

	before := time.Now().UnixNano()
	hub.Broadcast(1000, [][]byte{{0x65, 0x01}}, true)

	select {
	case msg := <-got:
		require.True(t, msg.IsKeyframe)
		require.Greater(t, msg.IngestAt, int64(0), "IngestAt must be stamped at enqueue")
		require.LessOrEqual(t, msg.IngestAt, time.Now().UnixNano())
		_ = before
	case <-time.After(2 * time.Second):
		t.Fatal("SubscribeMsg consumer did not receive frame")
	}
}

func TestStreamHub_AddOnDropFiresAllCallbacks(t *testing.T) {
	// Regression for the #469 Phase 0 bug: HLS assigning hub.OnDrop destroyed
	// the camera manager's wiring. AddOnDrop must fan out to every registrant.
	hub := New()
	hub.SetCameraID("cam-drop")
	hub.consumerBufferSize = 1

	var mu sync.Mutex
	var gotA, gotB []bool
	hub.AddOnDrop(func(consumerID string, isIDR bool) {
		mu.Lock()
		gotA = append(gotA, isIDR)
		mu.Unlock()
	})
	hub.AddOnDrop(func(consumerID string, isIDR bool) {
		mu.Lock()
		gotB = append(gotB, isIDR)
		mu.Unlock()
	})

	// Blocking consumer so the buffer (size 1) overflows. firstRecv parks the
	// drain goroutine INSIDE the callback, so subsequent broadcasts face a
	// deterministic buffer state (no race between fill and drain).
	block := make(chan struct{})
	firstRecv := make(chan struct{})
	var gotFirst sync.Once
	require.NoError(t, hub.Subscribe("stuck", func(pts int64, au [][]byte) {
		gotFirst.Do(func() { close(firstRecv) })
		<-block
	}))

	hub.Broadcast(1, [][]byte{{0x01}}, false) // consumed by drain (parks in cb)
	select {
	case <-firstRecv:
	case <-time.After(2 * time.Second):
		t.Fatal("drain did not receive the first frame")
	}
	hub.Broadcast(2, [][]byte{{0x02}}, false) // fills the buffer
	hub.Broadcast(3, [][]byte{{0x03}}, false) // overflows → drop (isIDR=false)
	hub.Broadcast(4, [][]byte{{0x04}}, false) // another drop

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(gotA) >= 2 && len(gotB) >= 2
	}, 2*time.Second, 10*time.Millisecond, "both drop callbacks should fire for every drop")

	close(block)
	// drain the consumer so Unsubscribe (test cleanup) doesn't hang
	require.Eventually(t, func() bool { return hub.Drops("stuck") >= 2 }, 2*time.Second, 10*time.Millisecond)
	hub.Unsubscribe("stuck")
}

func TestStreamHub_TrySendIDRCountsEvictedAndIDRDrops(t *testing.T) {
	hub := New()
	hub.SetCameraID("cam-idr")
	hub.consumerBufferSize = 3

	// Same determinism trick as above: park drain inside the callback after
	// the first frame so the buffer fill state is exact.
	block := make(chan struct{})
	firstRecv := make(chan struct{})
	var gotFirst sync.Once
	require.NoError(t, hub.Subscribe("stuck", func(pts int64, au [][]byte) {
		gotFirst.Do(func() { close(firstRecv) })
		<-block
	}))
	defer func() {
		close(block)
		require.Eventually(t, func() bool { return hub.ConsumerCount() >= 1 }, 2*time.Second, 10*time.Millisecond)
		hub.Unsubscribe("stuck")
	}()

	hub.Broadcast(0, [][]byte{{0x00}}, false) // consumed by drain (parks in cb)
	select {
	case <-firstRecv:
	case <-time.After(2 * time.Second):
		t.Fatal("drain did not receive the first frame")
	}
	// Fill the buffer (capacity 3) with 3 more non-IDR frames.
	for i := 1; i <= 3; i++ {
		hub.Broadcast(int64(i), [][]byte{{byte(i)}}, false)
	}
	require.Equal(t, int64(0), hub.Drops("stuck"))

	// IDR arrives on a full buffer → trySendIDR evicts the oldest non-IDR
	// (counted as a drop) and enqueues the IDR (counted as a send).
	hub.Broadcast(900, [][]byte{{0x65}}, true)
	snap := hub.Snapshot()
	c := snap.Consumers[0]
	require.Equal(t, int64(1), c.Drops, "evicted non-IDR must count as a drop")
	require.Equal(t, int64(5), c.Sends, "1 parked + 3 buffered + 1 IDR enqueued")
	require.Equal(t, int64(0), c.IDRDrops)

	// Buffer now holds [f2, f3, IDR] — send more IDRs only, so an IDR
	// eventually finds no evictable non-IDR frame and is itself dropped.
	hub.Broadcast(901, [][]byte{{0x65}}, true)
	hub.Broadcast(902, [][]byte{{0x65}}, true)
	hub.Broadcast(903, [][]byte{{0x65}}, true)

	require.Eventually(t, func() bool {
		snap = hub.Snapshot()
		c = snap.Consumers[0]
		return c.IDRDrops >= 1
	}, 2*time.Second, 10*time.Millisecond, "unprotectable IDR drop must be counted in idr_drops")
	require.GreaterOrEqual(t, c.Drops, int64(2))
}

func TestStreamHub_AudioDropFiresOnDrop(t *testing.T) {
	hub := New()
	hub.SetCameraID("cam-audio")

	var fired int32
	hub.AddOnDrop(func(consumerID string, isIDR bool) {
		// audio drops route through fireOnDrop with isIDR=false
		fired++
	})

	block := make(chan struct{})
	require.NoError(t, hub.SubscribeAudio("audio-stuck", func(pts int64, codec model.AudioCodec, data []byte) {
		<-block
	}))
	// audio buffer is 50 — overflow it
	for i := range 60 {
		hub.BroadcastAudio(int64(i), model.AudioG711U, []byte{0x00})
	}
	require.GreaterOrEqual(t, fired, int32(1), "audio overflow should fire drop callbacks")
	close(block)
	hub.UnsubscribeAudio("audio-stuck")
}
