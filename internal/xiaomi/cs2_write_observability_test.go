// SPDX-License-Identifier: MIT
//
// CS2 outbound control-write observability tests (issue #503): the four
// silently-dropped writes (keepalive PING, data PING, DrwAck, PONG) now log
// failures — debug per blip, warn from 3 consecutive — and the cmdAck
// callback swap is race-free under -race.

package xiaomi

import (
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// failingWriteConn accepts reads but fails every Write.
type failingWriteConn struct {
	mockCS2Conn
	writeErr error
}

func (f *failingWriteConn) Write(b []byte) (int, error) {
	return 0, f.writeErr
}

// captureCS2Logger swaps the package cs2Logger for one writing to buf and
// restores it on cleanup. Callers must not run parallel tests while active.
func captureCS2Logger(t *testing.T) *strings.Builder {
	t.Helper()
	var buf strings.Builder
	old := cs2Logger
	cs2Logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	t.Cleanup(func() { cs2Logger = old })
	return &buf
}

func TestWriteControlEscalatesAfterConsecutiveFailures(t *testing.T) {
	t.Helper()
	buf := captureCS2Logger(t)

	c := &CS2Conn{
		Conn: &failingWriteConn{writeErr: errors.New("broken pipe")},
	}
	c.LogKey = "isa.camera.hlc8@192.168.63.9"

	c.writeControl("pong", []byte{1, 2, 3, 4})
	require.Contains(t, buf.String(), "level=DEBUG", "first failure logs at debug")
	require.Contains(t, buf.String(), "frame=pong")
	require.Contains(t, buf.String(), "consecutive_failures=1")
	require.Contains(t, buf.String(), "peer=isa.camera.hlc8@192.168.63.9")

	c.writeControl("pong", []byte{1, 2, 3, 4})
	require.NotContains(t, buf.String(), "level=WARN", "second failure is still debug")

	c.writeControl("pong", []byte{1, 2, 3, 4})
	require.Contains(t, buf.String(), "level=WARN", "third consecutive failure escalates to warn")
	require.Contains(t, buf.String(), "consecutive_failures=3")
}

func TestWriteControlResetsCounterOnSuccess(t *testing.T) {
	t.Helper()
	buf := captureCS2Logger(t)

	c := &CS2Conn{Conn: &mockCS2Conn{}}

	// Two failures, then a success, then one failure — the counter must have
	// reset, so the post-success failure logs consecutive_failures=1 at debug,
	// not warn.
	for range 2 {
		c.Conn = &failingWriteConn{writeErr: errors.New("broken pipe")}
		c.writeControl("drw-ack", []byte{1})
	}
	c.Conn = &mockCS2Conn{}
	c.writeControl("drw-ack", []byte{1})
	c.Conn = &failingWriteConn{writeErr: errors.New("broken pipe")}
	c.writeControl("drw-ack", []byte{1})

	require.NotContains(t, buf.String(), "level=WARN")
	require.Contains(t, buf.String(), "consecutive_failures=1")
}

func TestWorkerPongWriteFailureIsLogged(t *testing.T) {
	t.Helper()
	buf := captureCS2Logger(t)

	// Camera PING arrives; every outbound Write (the PONG reply) fails, then
	// the read stream ends and the worker exits.
	pingPacket := []byte{cs2Magic, cs2MsgPing, 0, 0}
	mock := &failingWriteConn{
		writeErr: errors.New("connection refused"),
	}
	mock.reads = [][]byte{pingPacket}
	mock.err = errors.New("read done")

	c := &CS2Conn{
		Conn:        mock,
		isTCP:       false,
		idleTimeout: time.Minute,
		channels: [4]*cs2DataChannel{
			newCS2DataChannel(0, 10), nil, newCS2DataChannel(250, 100), nil,
		},
		LogKey: "cam-x@10.0.0.1",
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

	require.Contains(t, buf.String(), "frame=pong", "undelivered PONG must leave a log line (#503: camera tears down ~6s later)")
	require.Contains(t, buf.String(), "peer=cam-x@10.0.0.1")
}

// TestCmdAckAtomicSwap drives concurrent Store/Load of the UDP command-ACK
// callback — the data race the worker (DrwAck path) vs WriteCommand had
// before #503 (plain func field under cmdMu, read without any lock).
func TestCmdAckAtomicSwap(t *testing.T) {
	t.Helper()
	c := &CS2Conn{}
	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(2)
	go func() { // WriteCommand side (stores under cmdMu)
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			fn := func() {}
			c.cmdAck.Store(&fn)
		}
	}()
	go func() { // worker DrwAck side (loads + fires)
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if fn := c.cmdAck.Load(); fn != nil {
				(*fn)()
			}
		}
	}()

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}
