package relay

// egress_lane_test.go — #46 回归钉（SDK 侧）：keepalive ping/pong 走控制车
// 道，必须跳过已排队的数据帧；数据车道严格 FIFO；背压溢出必须 fail 传输
// （会话级重建）而不是丢帧。

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClientEgressControlLaneJumpsQueuedData: four data frames already
// queued when a ping is enqueued — the ping must be written first.
func TestClientEgressControlLaneJumpsQueuedData(t *testing.T) {
	c := NewClient()

	var mu sync.Mutex
	var order []byte
	c.writeSync = func(b []byte) error {
		mu.Lock()
		order = append(order, b[0])
		mu.Unlock()
		return nil
	}

	for range 4 {
		require.NoError(t, c.enqueueData(encodeFrame(FrameSendPacket, make([]byte, KeyLen+8)), false))
	}
	c.enqueueCtrl(encodeFrame(FramePing, nil))

	done := make(chan struct{})
	go func() { defer close(done); c.writerLoop() }()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(order) == 5
	}, 3*time.Second, 10*time.Millisecond, "all five frames must be written")

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, FramePing, order[0], "control frame must jump queued data")
	for _, ft := range order[1:] {
		assert.Equal(t, FrameSendPacket, ft, "data lane stays FIFO")
	}

	_ = c.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("writerLoop must exit after Close")
	}
}

// TestClientEgressOverflowBackpressuresThenFailsOnStall：车道满时的语义
// 是**有界阻塞**（2026-10-14 生产修正——立即判死使弱蜂窝中继视频会话
// ~15s 必死）：writer 恢复排空后超限帧必须入队成功且不触发 fatal；
// 只有停滞超过 clientDataQueueWait 才判死。
func TestClientEgressOverflowBackpressuresThenFailsOnStall(t *testing.T) {
	c := NewClient()
	fatal := make(chan error, 1)
	c.OnFatal(func(err error) { fatal <- err })
	c.writeSync = func([]byte) error { return nil }

	// 停滞的 writer：先吞掉所有写入（不排空 dataBytes 的记账由 writerLoop
	// 负责——这里不启动 writerLoop，队列只进不出）。
	for range clientDataQueueFrames {
		require.NoError(t, c.enqueueData(encodeFrame(FrameSendPacket, make([]byte, KeyLen+8)), false))
	}

	// 超限帧：有界阻塞——200ms 内不得返回、不得 fatal。
	enqueued := make(chan error, 1)
	go func() {
		enqueued <- c.enqueueData(encodeFrame(FrameSendPacket, make([]byte, KeyLen+8)), false)
	}()
	select {
	case err := <-enqueued:
		t.Fatalf("enqueue must block while stalled, returned %v", err)
	case ferr := <-fatal:
		t.Fatalf("must not fail while within the wait budget: %v", ferr)
	case <-time.After(200 * time.Millisecond):
	}

	// 排空恢复：writerLoop 把队列喝干，超限帧入队成功、无 fatal。
	drained := make(chan struct{})
	go func() { defer close(drained); c.writerLoop() }()
	select {
	case err := <-enqueued:
		require.NoError(t, err, "enqueue must succeed after drain resumes")
	case <-time.After(3 * time.Second):
		t.Fatal("enqueue never completed after drain resumed")
	}
	select {
	case ferr := <-fatal:
		t.Fatalf("no fatal expected on recoverable backlog: %v", ferr)
	case <-time.After(100 * time.Millisecond):
	}
	_ = c.Close()
	<-drained
}

// TestClientEgressStallBeyondDeadlineFails：真停滞（超过等待预算零排
// 空）必须判死——保住死链检出语义。
func TestClientEgressStallBeyondDeadlineFails(t *testing.T) {
	orig := clientDataQueueWait
	clientDataQueueWait = 150 * time.Millisecond
	t.Cleanup(func() { clientDataQueueWait = orig })

	c := NewClient()
	fatal := make(chan error, 1)
	c.OnFatal(func(err error) { fatal <- err })

	for range clientDataQueueFrames {
		require.NoError(t, c.enqueueData(encodeFrame(FrameSendPacket, make([]byte, KeyLen+8)), false))
	}
	err := c.enqueueData(encodeFrame(FrameSendPacket, make([]byte, KeyLen+8)), false)
	require.Error(t, err, "zero drain beyond the wait budget must fail the transport")

	select {
	case ferr := <-fatal:
		require.ErrorIs(t, ferr, err, "fatal callback carries the stall error")
	case <-time.After(time.Second):
		t.Fatal("stall must fire OnFatal")
	}
}
