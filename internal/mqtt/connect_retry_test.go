package mqtt

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/stretchr/testify/require"
)

// TestStart_RetriesInitialConnectFailures: the broker being unreachable at
// boot must not kill MQTT for the process lifetime (#661) — Start keeps
// retrying with backoff and connects once the broker appears.
func TestStart_RetriesInitialConnectFailures(t *testing.T) {
	t.Helper()
	var attempts atomic.Int32
	okToken := &mockToken{}
	errToken := &mockToken{err: errors.New("connection refused")}

	c := NewClient("tcp://127.0.0.1:1883", "cid", "nvr", "", "", nil)
	c.connectFn = func(_ *mqtt.ClientOptions) mqtt.Client {
		if attempts.Add(1) == 1 {
			return &mockPahoClient{connected: true, connectToken: errToken}
		}
		return &mockPahoClient{connected: true, connectToken: okToken}
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Start(ctx) }()

	require.Eventually(t, func() bool { return attempts.Load() >= 2 }, 15*time.Second, 50*time.Millisecond,
		"Start must retry the initial connect after a failure")
	require.Eventually(t, func() bool { return c.getMQTTClient() != nil }, 15*time.Second, 50*time.Millisecond,
		"the client must be retained after a successful retry")

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err, "clean shutdown after a successful connect")
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after ctx cancel")
	}
}

// TestStart_CtxCancelDuringConnectRetries: cancelling the context while
// still retrying must return promptly, surfacing the last dial error.
func TestStart_CtxCancelDuringConnectRetries(t *testing.T) {
	t.Helper()
	errToken := &mockToken{err: errors.New("connection refused")}
	c := NewClient("tcp://127.0.0.1:1883", "cid", "nvr", "", "", nil)
	c.connectFn = func(_ *mqtt.ClientOptions) mqtt.Client {
		return &mockPahoClient{connected: false, connectToken: errToken}
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := c.Start(ctx)
	require.Error(t, err, "shutdown before ever connecting must surface the last dial error")
	require.ErrorContains(t, err, "connection refused")
	require.Less(t, time.Since(start), 5*time.Second, "must return promptly on ctx cancel, not hang in backoff")
}
