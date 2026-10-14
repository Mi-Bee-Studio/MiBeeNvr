package relay

// priority_test.go — #46 v2 回归钉（SDK 侧）：server_auth 能力字节解析、
// SendPriority 降级门控（旧服务端=普通车道）、高车道出队顺序。

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSupportsPriority_FromAuthPayload pins the capability-byte parse.
func TestSupportsPriority_FromAuthPayload(t *testing.T) {
	c := NewClient()
	assert.False(t, c.SupportsPriority(), "no auth yet")

	c.mu.Lock()
	c.serverCaps = FrameCapPriority
	c.mu.Unlock()
	assert.True(t, c.SupportsPriority())

	c.mu.Lock()
	c.serverCaps = 0x02 // some other capability
	c.mu.Unlock()
	assert.False(t, c.SupportsPriority())
}

// TestSendPriorityDowngradesWithoutCapability: a legacy server (no caps)
// must route SendPriority through the normal lane — wire type WITHOUT the
// priority bit, normal queue.
func TestSendPriorityDowngradesWithoutCapability(t *testing.T) {
	c := NewClient()
	var mu sync.Mutex
	var types []byte
	c.writeSync = func(b []byte) error {
		mu.Lock()
		types = append(types, b[0])
		mu.Unlock()
		return nil
	}
	// Enqueue everything BEFORE the writer starts: writerLoop is a racing
	// consumer, so enqueue-then-start makes lane ordering deterministic.
	var dst [32]byte
	dst[0] = 1
	require.NoError(t, c.SendPriorityTo(dst, []byte("x")))
	require.NoError(t, c.SendTo(dst, []byte("y")))

	done := make(chan struct{})
	go func() { defer close(done); c.writerLoop() }()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(types) == 2
	}, 2*time.Second, 5*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, FrameSendPacket, types[0], "downgraded: no priority bit on the wire")
	assert.Equal(t, FrameSendPacket, types[1])
	_ = c.Close()
}

// TestSendPriorityUsesHighLane: with the capability advertised, SendPriority
// frames carry the priority bit AND jump queued normal data; both stay FIFO
// within their lanes.
func TestSendPriorityUsesHighLane(t *testing.T) {
	c := NewClient()
	c.mu.Lock()
	c.serverCaps = FrameCapPriority
	c.mu.Unlock()

	var mu sync.Mutex
	var order []byte
	c.writeSync = func(b []byte) error {
		mu.Lock()
		order = append(order, b[0])
		mu.Unlock()
		return nil
	}
	// Enqueue everything BEFORE the writer starts: writerLoop is a racing
	// consumer, so enqueue-then-start makes lane ordering deterministic.
	var dst [32]byte
	dst[0] = 1
	// Queue two normal frames, then a priority frame.
	require.NoError(t, c.SendTo(dst, []byte("n1")))
	require.NoError(t, c.SendTo(dst, []byte("n2")))
	require.NoError(t, c.SendPriorityTo(dst, []byte("hi")))

	done := make(chan struct{})
	go func() { defer close(done); c.writerLoop() }()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(order) == 3
	}, 2*time.Second, 5*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, FrameSendPacket|FramePriorityBit, order[0], "high jumps normal data")
	assert.Equal(t, FrameSendPacket, order[1])
	assert.Equal(t, FrameSendPacket, order[2])
	_ = c.Close()
}
