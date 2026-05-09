package onvif

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewClient(t *testing.T) {
	client := NewClient("http://192.168.1.100:80/onvif/device_service", "admin", "password")
	require.NotNil(t, client)
	require.Equal(t, "http://192.168.1.100:80/onvif/device_service", client.endpoint)
	require.Equal(t, "admin", client.username)
	require.Equal(t, "password", client.password)
	require.False(t, client.ready)
}

func TestClientNotConnected(t *testing.T) {
	client := NewClient("http://localhost:8080/onvif/device_service", "admin", "password")
	ctx := context.Background()

	_, err := client.GetDeviceInformation(ctx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not connected")

	_, err = client.GetProfiles(ctx)
	require.Error(t, err)

	_, err = client.GetStreamURI(ctx, "profile1")
	require.Error(t, err)

	_, err = client.GetCapabilities(ctx)
	require.Error(t, err)
}

func TestClientConnect(t *testing.T) {
	client := NewClient("http://localhost:8080/onvif/device_service", "admin", "password")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Connect will succeed (placeholder implementation)
	err := client.Connect(ctx)
	require.NoError(t, err)
	require.True(t, client.ready)

	// After connect, GetDeviceInformation should work (returns placeholder)
	info, err := client.GetDeviceInformation(ctx)
	require.NoError(t, err)
	require.NotNil(t, info)
}
