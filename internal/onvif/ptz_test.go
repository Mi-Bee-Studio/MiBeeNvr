package onvif

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newConnectedClient(t *testing.T) *Client {
	t.Helper()
	client := NewClient("http://localhost:8080/onvif/device_service", "admin", "password")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, client.Connect(ctx))
	return client
}

func TestPTZNotConnected(t *testing.T) {
	client := NewClient("http://localhost:8080/onvif/device_service", "admin", "password")
	ctx := context.Background()

	err := client.PTZContinuousMove(ctx, "profile1", PTZVector{Pan: 0.5})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not connected")
}

func TestPTZStopNotConnected(t *testing.T) {
	client := NewClient("http://localhost:8080/onvif/device_service", "admin", "password")
	ctx := context.Background()

	err := client.PTZStop(ctx, "profile1")
	require.Error(t, err)
}

func TestPTZGetStatusNotImplemented(t *testing.T) {
	client := newConnectedClient(t)
	ctx := context.Background()

	_, err := client.PTZGetStatus(ctx, "profile1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not yet implemented")
}
