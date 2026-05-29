// SPDX-License-Identifier: MIT
//
// Xiaomi CS2 P2P transport adapted from go2rtc (https://github.com/AlexxIT/go2rtc)
// Copyright (c) go2rtc contributors
// Licensed under the MIT License.

package xiaomi

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCS2MarshalCmd(t *testing.T) {
payload := []byte{0xAA, 0xBB, 0xCC}
	cmd := uint32(0x12345678)
	seq := uint16(0x00AB)
	channel := byte(0)

	result := cs2MarshalCmd(channel, seq, cmd, payload)

	// Total size: 4 (msg header) + 4 (drw header) + 4 (payload size) + 4 (cmd) + 3 (payload) = 19
	expectedLen := 4 + 4 + 4 + 4 + len(payload)
	require.Len(t, result, expectedLen)

	// 1. Message header: magic, msgDrw, size
	require.Equal(t, byte(cs2Magic), result[0])
	require.Equal(t, byte(cs2MsgDrw), result[1])
	require.Equal(t, uint16(4+4+4+len(payload)), binary.BigEndian.Uint16(result[2:]))

	// 2. DRW header
	require.Equal(t, byte(cs2MagicDrw), result[4])
	require.Equal(t, channel, result[5])
	require.Equal(t, seq, binary.BigEndian.Uint16(result[6:]))

	// 3. Payload size (4 + payload length)
	require.Equal(t, uint32(4+len(payload)), binary.BigEndian.Uint32(result[8:]))

	// 4. Command
	require.Equal(t, cmd, binary.BigEndian.Uint32(result[12:]))

	// 5. Payload
	require.Equal(t, payload, result[16:])
}

func TestCS2MarshalCmdEmptyPayload(t *testing.T) {
result := cs2MarshalCmd(0, 1, 0x99, nil)

	// Total: 4 + 4 + 4 + 4 + 0 = 16
	require.Len(t, result, 16)
	require.Equal(t, byte(cs2Magic), result[0])
	require.Equal(t, byte(cs2MsgDrw), result[1])
	require.Equal(t, uint16(12), binary.BigEndian.Uint16(result[2:]))
	require.Equal(t, uint32(0x99), binary.BigEndian.Uint32(result[12:]))
}

func TestCS2DataChannelPushPop(t *testing.T) {
ch := newCS2DataChannel(0, 10)

	// Push data with 4-byte big-endian size prefix
	data := []byte("hello")
	sizeBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(sizeBuf, uint32(len(data)))

	err := ch.Push(append(sizeBuf, data...))
	require.NoError(t, err)

	got, ok := ch.Pop(5 * time.Second)
	require.True(t, ok)
	require.Equal(t, data, got)
}

func TestCS2DataChannelPushMultipleInOnePacket(t *testing.T) {
ch := newCS2DataChannel(0, 10)

	// Two messages in one push: "abc" and "defgh"
	msg1 := []byte("abc")
	msg2 := []byte("defgh")

	buf := make([]byte, 0, 4+len(msg1)+4+len(msg2))
	size1 := make([]byte, 4)
	binary.BigEndian.PutUint32(size1, uint32(len(msg1)))
	buf = append(buf, size1...)
	buf = append(buf, msg1...)

	size2 := make([]byte, 4)
	binary.BigEndian.PutUint32(size2, uint32(len(msg2)))
	buf = append(buf, size2...)
	buf = append(buf, msg2...)

	err := ch.Push(buf)
	require.NoError(t, err)

	got1, ok := ch.Pop(5 * time.Second)
	require.True(t, ok)
	require.Equal(t, msg1, got1)

	got2, ok := ch.Pop(5 * time.Second)
	require.True(t, ok)
	require.Equal(t, msg2, got2)
}

func TestCS2DataChannelPushSeqInOrder(t *testing.T) {
ch := newCS2DataChannel(10, 100)

	data1 := makeDataWithSize("first")
	data2 := makeDataWithSize("second")

	// Push seq 0 (waitSeq starts at 0)
	pushed, err := ch.PushSeq(0, data1)
	require.NoError(t, err)
	require.Equal(t, 1, pushed)

	// Push seq 1
	pushed, err = ch.PushSeq(1, data2)
	require.NoError(t, err)
	require.Equal(t, 1, pushed)

	got1, ok := ch.Pop(5 * time.Second)
	require.True(t, ok)
	require.Equal(t, []byte("first"), got1)

	got2, ok := ch.Pop(5 * time.Second)
	require.True(t, ok)
	require.Equal(t, []byte("second"), got2)
}

func TestCS2DataChannelPushSeqOutOfOrder(t *testing.T) {
ch := newCS2DataChannel(10, 100)

	data0 := makeDataWithSize("zero")
	data1 := makeDataWithSize("one")

	// Push seq 1 first (out of order) — should be buffered
	pushed, err := ch.PushSeq(1, data1)
	require.NoError(t, err)
	require.Equal(t, 0, pushed) // saved to buffer, not processed

	// Push seq 0 — should process both 0 and 1
	pushed, err = ch.PushSeq(0, data0)
	require.NoError(t, err)
	require.Equal(t, 2, pushed) // processed both seq 0 and seq 1

	got0, ok := ch.Pop(5 * time.Second)
	require.True(t, ok)
	require.Equal(t, []byte("zero"), got0)

	got1, ok := ch.Pop(5 * time.Second)
	require.True(t, ok)
	require.Equal(t, []byte("one"), got1)
}

func TestCS2DataChannelPushSeqDuplicate(t *testing.T) {
ch := newCS2DataChannel(10, 100)

	data := makeDataWithSize("hello")

	// Push seq 0
	pushed, err := ch.PushSeq(0, data)
	require.NoError(t, err)
	require.Equal(t, 1, pushed)

	// Push seq 0 again (from the past)
	pushed, err = ch.PushSeq(0, data)
	require.NoError(t, err)
	require.Equal(t, 0, pushed) // already processed, ignored
}

func TestCS2DataChannelPushSeqBufferFull(t *testing.T) {
ch := newCS2DataChannel(2, 100) // small push buffer

	// Push future seq 1
	pushed, err := ch.PushSeq(1, []byte("a"))
	require.NoError(t, err)
	require.Equal(t, 0, pushed)

	// Push future seq 2
	pushed, err = ch.PushSeq(2, []byte("b"))
	require.NoError(t, err)
	require.Equal(t, 0, pushed)

	// Push future seq 3 — buffer full
	pushed, err = ch.PushSeq(3, []byte("c"))
	require.NoError(t, err)
	require.Equal(t, -1, pushed) // couldn't save
}

func TestCS2DataChannelPushSeqNoBuffer(t *testing.T) {
ch := newCS2DataChannel(0, 100) // pushSize=0, no reorder buffer

	// Future seq can't be saved
	pushed, err := ch.PushSeq(5, []byte("future"))
	require.NoError(t, err)
	require.Equal(t, -1, pushed)
}

func TestCS2DataChannelClose(t *testing.T) {
ch := newCS2DataChannel(0, 10)
	ch.Close()

	_, ok := ch.Pop(5 * time.Second)
	require.False(t, ok)
}

func TestCS2DataChannelPopBufferFull(t *testing.T) {
	ch := newCS2DataChannel(0, 1) // pop buffer size 1

	// Push one message
	sizeBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(sizeBuf, 3)
	err := ch.Push(append(sizeBuf, "abc"...))
	require.NoError(t, err)

	// Push another — pop buffer full, should drain oldest and succeed
	err = ch.Push(append(sizeBuf, "def"...))
	require.NoError(t, err)

	// Pop with small timeout (not 0, as time.After(0) races with channel receive)
	data, ok := ch.Pop(10 * time.Millisecond)
	require.True(t, ok)
	require.Equal(t, string(data), "def")
}

func TestCS2ConnStructFields(t *testing.T) {
// Verify struct can be initialized (no actual network connection needed)
	c := &CS2Conn{
		channels: [4]*cs2DataChannel{
			newCS2DataChannel(0, 10), nil, newCS2DataChannel(250, 100), nil,
		},
	}

	require.False(t, c.isTCP)
	require.Equal(t, "cs2+udp", c.Protocol())
	require.Equal(t, "CS2", c.Version())
	require.Equal(t, io.EOF, c.Error()) // no error set, should return EOF

	// TCP variant
	c.isTCP = true
	require.Equal(t, "cs2+tcp", c.Protocol())
}

func TestCS2MarshalCmdChannelByte(t *testing.T) {
// Test with different channel values
	for _, ch := range []byte{0, 1, 2, 3} {
		result := cs2MarshalCmd(ch, 0, 0x01, nil)
		require.Equal(t, ch, result[5])
	}
}

func TestCS2MarshalCmdSeqIncrement(t *testing.T) {
result0 := cs2MarshalCmd(0, 0, 0x01, nil)
	result1 := cs2MarshalCmd(0, 1, 0x01, nil)

	require.Equal(t, uint16(0), binary.BigEndian.Uint16(result0[6:]))
	require.Equal(t, uint16(1), binary.BigEndian.Uint16(result1[6:]))
}

// makeDataWithSize creates a CS2 data payload with a 4-byte big-endian size prefix.
func makeDataWithSize(s string) []byte {
	data := []byte(s)
	buf := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(buf, uint32(len(data)))
	copy(buf[4:], data)
	return buf
}

func TestCS2ConnErrorSetterGetter(t *testing.T) {
	c := &CS2Conn{
		channels: [4]*cs2DataChannel{
			newCS2DataChannel(0, 10), nil, newCS2DataChannel(250, 100), nil,
		},
	}

	// Initially nil error
	require.Nil(t, c.getErr())

	// Set an error
	testErr := fmt.Errorf("test error")
	c.setErr(testErr)
	require.Equal(t, testErr, c.getErr())

	// Overwrite with nil
	c.setErr(nil)
	require.Nil(t, c.getErr())

	// Overwrite with another error
	err2 := fmt.Errorf("another error")
	c.setErr(err2)
	require.Equal(t, err2, c.getErr())
}

func TestCS2ConnErrorConcurrentAccess(t *testing.T) {
	c := &CS2Conn{
		channels: [4]*cs2DataChannel{
			newCS2DataChannel(0, 10), nil, newCS2DataChannel(250, 100), nil,
		},
	}

	done := make(chan struct{})
	const N = 50

	// Concurrent writers
	go func() {
		for i := 0; i < N; i++ {
			c.setErr(fmt.Errorf("writer error %d", i))
			time.Sleep(time.Microsecond)
		}
		done <- struct{}{}
	}()

	// Concurrent readers
	go func() {
		for i := 0; i < N; i++ {
			_ = c.getErr()
			time.Sleep(time.Microsecond)
		}
		done <- struct{}{}
	}()

	// Concurrent Error() reader
	go func() {
		for i := 0; i < N; i++ {
			_ = c.Error()
			time.Sleep(time.Microsecond)
		}
		done <- struct{}{}
	}()

	// Wait for all goroutines
	for i := 0; i < 3; i++ {
		<-done
	}
}

func TestCS2DialDefaultIdleTimeout(t *testing.T) {
	// Verify defaultIdleTimeout constant is 30s.
	require.Equal(t, 30*time.Second, defaultIdleTimeout)

	// Verify that CS2Conn created with idleTimeout=0 gets the default.
	c := &CS2Conn{
		idleTimeout: 0,
		channels: [4]*cs2DataChannel{
			newCS2DataChannel(0, 10), nil, newCS2DataChannel(250, 100), nil,
		},
	}
	// Simulate the default logic from CS2Dial.
	if c.idleTimeout == 0 {
		c.idleTimeout = defaultIdleTimeout
	}
	require.Equal(t, 30*time.Second, c.idleTimeout)
}

func TestCS2DialWithIdleTimeout(t *testing.T) {
	// Verify that CS2Conn created with a custom idleTimeout keeps that value.
	customTimeout := 15 * time.Second
	c := &CS2Conn{
		idleTimeout: customTimeout,
		channels: [4]*cs2DataChannel{
			newCS2DataChannel(0, 10), nil, newCS2DataChannel(250, 100), nil,
		},
	}
	require.Equal(t, customTimeout, c.idleTimeout)

	// Verify it is not the default.
	require.NotEqual(t, defaultIdleTimeout, c.idleTimeout)
}

// mockCS2Conn implements net.Conn for testing, recording all writes.
type mockCS2Conn struct {
	reads   [][]byte
	readIdx int
	writes  [][]byte
	err     error
	mu      sync.Mutex
}

func (m *mockCS2Conn) Read(b []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.readIdx >= len(m.reads) {
		return 0, m.err
	}
	data := m.reads[m.readIdx]
	m.readIdx++
	return copy(b, data), nil
}

func (m *mockCS2Conn) Write(b []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	buf := make([]byte, len(b))
	copy(buf, b)
	m.writes = append(m.writes, buf)
	return len(b), nil
}

func (m *mockCS2Conn) Close() error { return nil }

type mockAddr struct{}

func (mockAddr) Network() string { return "mock" }
func (mockAddr) String() string  { return "mock" }

func (m *mockCS2Conn) LocalAddr() net.Addr  { return mockAddr{} }
func (m *mockCS2Conn) RemoteAddr() net.Addr { return mockAddr{} }
func (m *mockCS2Conn) SetDeadline(t time.Time) error      { return nil }
func (m *mockCS2Conn) SetReadDeadline(t time.Time) error   { return nil }
func (m *mockCS2Conn) SetWriteDeadline(t time.Time) error  { return nil }

func TestCS2WorkerNoPongOnPing(t *testing.T) {
	t.Parallel()

	// Simulate a PING packet as the camera would send it.
	pingPacket := []byte{cs2Magic, cs2MsgPing, 0, 0}
	wantErr := fmt.Errorf("mock read error")
	mock := &mockCS2Conn{
		reads: [][]byte{pingPacket},
		err:   wantErr,
	}

	c := &CS2Conn{
		Conn:        mock,
		isTCP:       false,
		idleTimeout: time.Minute,
		channels: [4]*cs2DataChannel{
			newCS2DataChannel(0, 10), nil, newCS2DataChannel(250, 100), nil,
		},
	}

	done := make(chan struct{})
	go func() {
		c.worker()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not exit in time")
	}

	// Worker should have exited with our mocked error.
	cerr := c.Error()
	require.Error(t, cerr)
	require.Contains(t, cerr.Error(), "mock read error")

	// Verify no PONG was written in response to PING.
	for _, w := range mock.writes {
		if len(w) >= 2 && w[1] == cs2MsgPong {
			t.Errorf("worker wrote unexpected PONG on PING: %x", w)
		}
	}
}

// newTestCS2Conn creates a CS2Conn suitable for unit tests.
func newTestCS2Conn(t *testing.T, conn net.Conn) *CS2Conn {
	t.Helper()
	return &CS2Conn{
		Conn: conn,
		channels: [4]*cs2DataChannel{
			newCS2DataChannel(0, 10), nil, newCS2DataChannel(250, 100), nil,
		},
		idleTimeout: defaultIdleTimeout,
	}
}

// panicOnReadConn causes a panic on every Read call.
type panicOnReadConn struct {
	net.Conn
}

func (p *panicOnReadConn) Read(b []byte) (int, error) {
	panic("worker panic test")
}

func TestCS2ConnErrorReturnsActualError(t *testing.T) {
	t.Parallel()

	// When worker() exits (simulated by closing the pipe), Error() should return
	// a descriptive wrapped error, not bare io.EOF.
	server, client := net.Pipe()
	defer server.Close()

	c := newTestCS2Conn(t, client)
	go c.worker()

	// Close server side to make worker's Read return an error.
	server.Close()

	// Wait for worker to exit (channels close in defer).
	require.Eventually(t, func() bool {
		_, ok := c.channels[0].Pop(time.Millisecond)
		return !ok // channel is closed
	}, time.Second, 10*time.Millisecond)

	err := c.Error()
	require.Error(t, err)
	require.NotEqual(t, io.EOF, err)
	require.Contains(t, err.Error(), "cs2:")
}

func TestCS2ConnErrorOnCleanClose(t *testing.T) {
	t.Parallel()

	// When worker() exits without an explicit error,
	// Error() should return "cs2: connection closed", not io.EOF.
	c := newTestCS2Conn(t, nil)
	c.channels[0].Close()
	c.channels[2].Close()

	// Simulate worker exit guard (runs as defer in worker()).
	c.workerExitGuard()

	err := c.Error()
	require.Error(t, err)
	require.NotEqual(t, io.EOF, err)
	require.Contains(t, err.Error(), "cs2: connection closed")
}

func TestCS2WorkerPanicRecovery(t *testing.T) {
	t.Parallel()

	// If worker() panics, the defer should recover and set c.err.
	server, client := net.Pipe()
	defer server.Close()

	c := newTestCS2Conn(t, &panicOnReadConn{Conn: client})

	done := make(chan struct{})
	go func() {
		c.worker()
		close(done)
	}()

	select {
	case <-done:
		// worker exited (panic was recovered)
	case <-time.After(time.Second):
		t.Fatal("worker did not exit after panic recovery")
	}

	err := c.Error()
	require.Error(t, err)
	require.Contains(t, err.Error(), "cs2: panic:")
}
