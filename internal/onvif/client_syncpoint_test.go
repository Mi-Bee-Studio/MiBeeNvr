// SPDX-License-Identifier: MIT

// SetSynchronizationPoint client surface (#921): the recorder asks the device
// to mark its next frame as a keyframe right after each RTSP PLAY, so segment
// writing starts within a frame instead of at the next GOP boundary. The
// wrapper is best-effort — faults surface as errors that callers log-and-ignore.

package onvif

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const soapSetSyncPointResponse = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
            xmlns:trt="http://www.onvif.org/ver10/media/wsdl">
  <s:Body>
    <trt:SetSynchronizationPointResponse/>
  </s:Body>
</s:Envelope>`

func TestRequestSyncPoint_Success(t *testing.T) {
	mock := newOnvifMockServer(t)
	mock.setHandler("GetCapabilities", soapGetCapabilitiesResponse)
	mock.setHandler("SetSynchronizationPoint", soapSetSyncPointResponse)
	server := mock.startServer(t)
	defer server.Close()

	client := NewClient(server.URL, "admin", "password")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, client.Connect(ctx))

	require.NoError(t, client.RequestSyncPoint(ctx, "profile_1"))
	require.Equal(t, 1, mock.callCount("SetSynchronizationPoint"))
}

func TestRequestSyncPoint_DeviceFaultIsError(t *testing.T) {
	mock := newOnvifMockServer(t)
	mock.setHandler("GetCapabilities", soapGetCapabilitiesResponse)
	// No SetSynchronizationPoint handler: the mock answers a Sender fault —
	// the shape devices without the action produce.
	server := mock.startServer(t)
	defer server.Close()

	client := NewClient(server.URL, "admin", "password")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, client.Connect(ctx))

	err := client.RequestSyncPoint(ctx, "profile_1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "set synchronization point")
	require.Equal(t, 1, mock.callCount("SetSynchronizationPoint"))
}

func TestRequestSyncPoint_NotConnected(t *testing.T) {
	client := NewClient("http://localhost:1/onvif/device_service", "admin", "password")
	err := client.RequestSyncPoint(context.Background(), "profile_1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not connected")
}
