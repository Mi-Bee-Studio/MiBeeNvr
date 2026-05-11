package onvif

import (
	"testing"

	"github.com/0x524a/onvif-go/discovery"
	"github.com/stretchr/testify/require"
)

func TestMapDiscoveredDevice(t *testing.T) {
	t.Helper()

	t.Run("maps all fields from onvif-go Device", func(t *testing.T) {
		t.Helper()
		d := &discovery.Device{
			EndpointRef:     "uuid:abc123",
			XAddrs:          []string{"http://192.168.1.100:8080/onvif/device_service"},
			Types:           []string{"dn:NetworkVideoTransmitter"},
			Scopes:          []string{"onvif://www.onvif.org/name/Camera1", "onvif://www.onvif.org/location/Office", "onvif://www.onvif.org/hardware/ModelX"},
			MetadataVersion: 1,
		}

		result := MapDiscoveredDevice(d)

		require.Equal(t, "uuid:abc123", result.UUID)
		require.Equal(t, "Camera1", result.Name)
		require.Equal(t, []string{"http://192.168.1.100:8080/onvif/device_service"}, result.XAddrs)
		require.Equal(t, d.Scopes, result.Scopes)
		require.Equal(t, "ModelX", result.Hardware)
		require.Equal(t, "http://192.168.1.100:8080/onvif/device_service", result.Endpoint)
	})

	t.Run("handles empty device", func(t *testing.T) {
		t.Helper()
		d := &discovery.Device{}

		result := MapDiscoveredDevice(d)

		require.Equal(t, "", result.UUID)
		require.Equal(t, "", result.Name)
		require.Empty(t, result.XAddrs)
		require.Empty(t, result.Scopes)
		require.Equal(t, "", result.Hardware)
		require.Equal(t, "", result.Endpoint)
	})

	t.Run("handles missing name and hardware in scopes", func(t *testing.T) {
		t.Helper()
		d := &discovery.Device{
			EndpointRef: "uuid:xyz",
			Scopes:      []string{"onvif://www.onvif.org/Profile/Streaming"},
		}

		result := MapDiscoveredDevice(d)

		require.Equal(t, "uuid:xyz", result.UUID)
		require.Equal(t, "", result.Name)
		require.Equal(t, "", result.Hardware)
	})
}

func TestMapDiscoveredDevices(t *testing.T) {
	t.Helper()

	t.Run("maps empty slice", func(t *testing.T) {
		t.Helper()
		result := MapDiscoveredDevices(nil)
		require.Empty(t, result)
	})

	t.Run("maps multiple devices", func(t *testing.T) {
		t.Helper()
		devices := []*discovery.Device{
			{EndpointRef: "uuid:1", XAddrs: []string{"http://cam1/onvif"}, Scopes: []string{"onvif://www.onvif.org/name/Cam1"}},
			{EndpointRef: "uuid:2", XAddrs: []string{"http://cam2/onvif"}, Scopes: []string{"onvif://www.onvif.org/name/Cam2"}},
		}

		result := MapDiscoveredDevices(devices)

		require.Len(t, result, 2)
		require.Equal(t, "Cam1", result[0].Name)
		require.Equal(t, "Cam2", result[1].Name)
	})
}
