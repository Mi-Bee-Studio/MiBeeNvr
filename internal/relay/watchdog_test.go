// SPDX-License-Identifier: MIT
//
// Stall watchdog tests (issue #429): a relay target whose receiver restarted
// reported status=streaming with kbps=0 for hours — write errors were the
// only failure signal, and with no frames flowing there are no writes to
// fail. The watchdog turns a frozen byte counter into a forced reconnect,
// and the RTMP publish conn bounds each write so dead-peer writes fail fast
// instead of blocking forever on a full kernel send buffer.

package relay

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/bluenviron/gortmplib/pkg/bytecounter"
	"github.com/stretchr/testify/require"
)

// deadlineRecorder is a net.Conn that records SetWriteDeadline calls and
// swallows writes.
type deadlineRecorder struct {
	written       int
	deadlineSet   bool
	lastDeadline  time.Time
	deadlineCalls int
}

func (d *deadlineRecorder) Write(b []byte) (int, error)         { d.written += len(b); return len(b), nil }
func (d *deadlineRecorder) Read(_ []byte) (int, error)          { return 0, io.EOF }
func (d *deadlineRecorder) Close() error                        { return nil }
func (d *deadlineRecorder) LocalAddr() net.Addr                 { return nil }
func (d *deadlineRecorder) RemoteAddr() net.Addr                { return nil }
func (d *deadlineRecorder) SetDeadline(_ time.Time) error       { return nil }
func (d *deadlineRecorder) SetReadDeadline(_ time.Time) error   { return nil }
func (d *deadlineRecorder) SetWriteDeadline(t time.Time) error {
	d.deadlineCalls++
	d.deadlineSet = true
	d.lastDeadline = t
	return nil
}

var _ net.Conn = (*deadlineRecorder)(nil)

func newWatchdogTarget(t *testing.T) *PushTarget {
	t.Helper()
	target := NewPushTarget("cam-test", PushTargetConfig{
		ID:       "t1",
		Protocol: "rtmp",
		URL:      "rtmp://127.0.0.1:1935/live/key",
		Enabled:  true,
	}, &model.StreamHub{}, func() ([]byte, []byte, bool) { return nil, nil, false })
	require.NotNil(t, target)
	target.stallAfter = 200 * time.Millisecond
	return target
}

// --- stallMonitor semantics ---

func TestStallMonitorDetectsFrozenStream(t *testing.T) {
	t.Helper()
	target := newWatchdogTarget(t)
	target.setStatus(StatusStreaming, "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stalled := make(chan struct{})
	go target.stallMonitor(ctx, stalled, cancel)

	select {
	case <-stalled:
		// watchdog fired — ctx was canceled too
		require.Error(t, ctx.Err())
	case <-time.After(3 * time.Second):
		t.Fatal("watchdog did not fire on a frozen streaming target")
	}
}

func TestStallMonitorIgnoresFrozenConnecting(t *testing.T) {
	t.Helper()
	target := newWatchdogTarget(t)
	// Connecting (handshake) phases have their own timeouts — the watchdog
	// window must reset while not streaming.
	target.setStatus(StatusConnecting, "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stalled := make(chan struct{})
	go target.stallMonitor(ctx, stalled, cancel)

	select {
	case <-stalled:
		t.Fatal("watchdog fired while target is still connecting")
	case <-time.After(600 * time.Millisecond):
		cancel()
	}
}

func TestStallMonitorResetsOnByteProgress(t *testing.T) {
	t.Helper()
	target := newWatchdogTarget(t)
	target.setStatus(StatusStreaming, "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stalled := make(chan struct{})
	go target.stallMonitor(ctx, stalled, cancel)

	// Keep the byte counter moving for longer than stallAfter.
	deadline := time.Now().Add(700 * time.Millisecond)
	for time.Now().Before(deadline) {
		target.bytesSent.Add(1024)
		time.Sleep(50 * time.Millisecond)
	}
	select {
	case <-stalled:
		t.Fatal("watchdog fired while bytes are flowing")
	default:
		cancel()
	}
}

// --- connectAndStreamWatched translation ---

func TestConnectAndStreamWatchedTranslatesStall(t *testing.T) {
	t.Helper()
	target := newWatchdogTarget(t)
	target.setStatus(StatusStreaming, "")

	// Simulate a streaming loop that only exits on context cancellation and
	// reports ctx.Err() — the shape of every real connect method's select.
	target.connectFn = func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}

	err := target.connectAndStreamWatched(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "stream stalled")
	require.Contains(t, err.Error(), "dead target connection")
}

func TestConnectAndStreamWatchedPassesThroughNormalError(t *testing.T) {
	t.Helper()
	target := newWatchdogTarget(t)
	sentinel := errors.New("writer: connection reset")
	target.connectFn = func(context.Context) error { return sentinel }

	err := target.connectAndStreamWatched(context.Background())
	require.ErrorIs(t, err, sentinel)
}

func TestConnectAndStreamWatchedParentCancelIsNotStall(t *testing.T) {
	t.Helper()
	target := newWatchdogTarget(t)
	target.setStatus(StatusStreaming, "")

	ctx, cancel := context.WithCancel(context.Background())
	target.connectFn = func(inner context.Context) error {
		<-inner.Done()
		return inner.Err()
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel() // NVR shutdown, not a stall
	}()
	err := target.connectAndStreamWatched(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.NotContains(t, err.Error(), "stream stalled")
}

// --- RTMP write deadline ---

func TestWriteRawMessageSetsWriteDeadline(t *testing.T) {
	t.Helper()
	rec := &deadlineRecorder{}
	c := &rtmpPublishConn{
		nconn: rec,
		bc:    bytecounter.NewReadWriter(rec),
	}
	before := time.Now()
	err := c.writeRawMessage(6, 0x09, 0, []byte{0x10, 0x01, 0, 0, 0, 0xAA})
	require.NoError(t, err)
	require.True(t, rec.deadlineSet, "writeRawMessage must set a write deadline")
	require.GreaterOrEqual(t, rec.lastDeadline, before.Add(relayWriteTimeout-time.Second))
	require.LessOrEqual(t, rec.lastDeadline, time.Now().Add(relayWriteTimeout+time.Second))
}

func TestSetWriteDeadlineBounded(t *testing.T) {
	t.Helper()
	require.Equal(t, 10*time.Second, relayWriteTimeout, "write timeout changed — update the dead-peer fail-fast budget docs in engine.go")
}
