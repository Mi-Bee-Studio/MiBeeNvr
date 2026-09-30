// SPDX-License-Identifier: MIT

// SetSynchronizationPoint wiring (#921): ONVIF recorders hand the delegate
// H.264/H.265 recorders a session-start callback that asks the camera for an
// immediate keyframe. The callback is fire-and-forget — device faults and
// timeouts must never disturb the recording session.

package recorder

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
)

func newSyncPointRecorder(t *testing.T, client *onvif.MockDeviceClient, profileToken string) *ONVIFRecorder {
	t.Helper()
	return NewONVIFRecorder(ONVIFConfig{
		CameraID:     "cam-sync",
		ProfileToken: profileToken,
	}, client, newTestManager(t))
}

func TestSyncPointRequester_RequestsKeyframeWithResolvedToken(t *testing.T) {
	client := &onvif.MockDeviceClient{}
	rec := newSyncPointRecorder(t, client, "profile_main")

	rec.syncPointRequester()

	require.Eventually(t, func() bool {
		return client.RequestSyncPointCalls == 1
	}, 2*time.Second, 10*time.Millisecond, "sync point must be requested asynchronously")
	require.Equal(t, []string{"profile_main"}, client.RequestSyncPointTokens)
}

func TestSyncPointRequester_EmptyTokenIsNoop(t *testing.T) {
	client := &onvif.MockDeviceClient{}
	rec := newSyncPointRecorder(t, client, "")

	rec.syncPointRequester()

	time.Sleep(100 * time.Millisecond)
	require.Equal(t, 0, client.RequestSyncPointCalls, "no resolved token yet → nothing to request")
}

func TestSyncPointRequester_DeviceErrorSwallowed(t *testing.T) {
	client := &onvif.MockDeviceClient{RequestSyncPointError: errors.New("Sender fault: ActionNotSupported")}
	rec := newSyncPointRecorder(t, client, "profile_main")

	// Must not panic and must not block — a faulting device (e.g. one that
	// cannot signal its encoder mid-stream) only forfeits the early keyframe.
	done := make(chan struct{})
	go func() {
		rec.syncPointRequester()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("syncPointRequester must return immediately (SOAP runs in a goroutine)")
	}
	require.Eventually(t, func() bool {
		return client.RequestSyncPointCalls == 1
	}, 2*time.Second, 10*time.Millisecond)
}
