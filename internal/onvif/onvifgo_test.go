package onvif

import (
	"testing"

	"github.com/mickeyzzc/onvif-go/v2/discovery"
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

	// TestMapDiscoveredDevices_DropsNonONVIF is the #266 regression guard:
	// generic WS-Discovery responders (Synology NAS DSM, printers, Windows
	// machines) answer the NVR's NetworkVideoTransmitter Probe with neither
	// that Type nor any onvif:// scope. They must be filtered out at the
	// discovery boundary so they never become pending_activation shells.
	t.Run("drops non-ONVIF WS-Discovery responders (#266)", func(t *testing.T) {
		t.Helper()
		devices := []*discovery.Device{
			// Real ONVIF camera — kept (Types has NetworkVideoTransmitter).
			{
				EndpointRef: "uuid:real-cam", XAddrs: []string{"http://cam/onvif/device_service"},
				Types: []string{"dp0:NetworkVideoTransmitter"}, Scopes: []string{"onvif://www.onvif.org/name/Cam"},
			},
			// Synology NAS DSM — no NetworkVideoTransmitter type, no onvif:// scope → dropped.
			{
				EndpointRef: "uuid:synology", XAddrs: []string{"http://synologynas:5357/wsd/abc"},
				Types: []string{"wsdiscovery:Device"}, Scopes: []string{"http://schemas.xmlsoap.org/ws/2006/02/devprof/devcat/Computers"},
			},
			// Generic responder, empty Types + empty Scopes → dropped.
			{EndpointRef: "uuid:empty", XAddrs: []string{"http://maoplus:5000/wsd/def"}, Types: nil, Scopes: nil},
			// Camera that only advertises via onvif:// scope (no Types) — kept.
			{
				EndpointRef: "uuid:scope-only", XAddrs: []string{"http://cam2/onvif"},
				Types: nil, Scopes: []string{"onvif://www.onvif.org/name/Cam2", "onvif://www.onvif.org/hardware/H264"},
			},
		}

		result := MapDiscoveredDevices(devices)

		require.Len(t, result, 2, "only the two ONVIF devices (Types=NetworkVideoTransmitter, scope-only) should survive")
		require.Equal(t, "uuid:real-cam", result[0].UUID)
		require.Equal(t, "uuid:scope-only", result[1].UUID)
	})
}

func TestIsONVIFDevice(t *testing.T) {
	t.Helper()
	tests := []struct {
		name string
		d    *discovery.Device
		want bool
	}{
		{
			name: "NetworkVideoTransmitter type (with prefix)",
			d:    &discovery.Device{Types: []string{"dp0:NetworkVideoTransmitter"}},
			want: true,
		},
		{
			name: "NetworkVideoTransmitter type (bare)",
			d:    &discovery.Device{Types: []string{"NetworkVideoTransmitter"}},
			want: true,
		},
		{
			name: "onvif:// scope only (no type)",
			d:    &discovery.Device{Scopes: []string{"onvif://www.onvif.org/name/Cam", "onvif://www.onvif.org/hardware/X"}},
			want: true,
		},
		{
			name: "Synology NAS (wsdiscovery:Device, no onvif scope)",
			d:    &discovery.Device{Types: []string{"wsdiscovery:Device"}, Scopes: []string{"http://schemas.xmlsoap.org/ws/2006/02/devprof/devcat/Computers"}},
			want: false,
		},
		{
			name: "printer (empty type, vendor scope)",
			d:    &discovery.Device{Types: []string{""}, Scopes: []string{"http://vendor.example.com/printer"}},
			want: false,
		},
		{
			name: "completely empty (Windows machine)",
			d:    &discovery.Device{},
			want: false,
		},
		{
			name: "false-positive guard: scope contains 'onvif' substring but not the namespace prefix",
			d:    &discovery.Device{Scopes: []string{"http://example.com/not-onvif-device"}},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isONVIFDevice(tt.d); got != tt.want {
				t.Errorf("isONVIFDevice() = %v, want %v", got, tt.want)
			}
		})
	}
}
