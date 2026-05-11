package onvif

import (
	"strings"

	"github.com/0x524a/onvif-go/discovery"
)

// MapDiscoveredDevice converts an onvif-go discovery.Device to the project's DiscoveredDevice.
func MapDiscoveredDevice(d *discovery.Device) DiscoveredDevice {
	name := d.GetName()
	endpoint := d.GetDeviceEndpoint()

	// Extract hardware/model from scopes if available
	var hardware string
	for _, scope := range d.Scopes {
		if strings.Contains(scope, "hardware") {
			parts := strings.Split(scope, "/")
			if len(parts) > 0 {
				hardware = parts[len(parts)-1]
			}
		}
	}

	return DiscoveredDevice{
		UUID:     d.EndpointRef,
		Name:     name,
		XAddrs:   d.XAddrs,
		Scopes:   d.Scopes,
		Hardware: hardware,
		Endpoint: endpoint,
	}
}

// MapDiscoveredDevices converts a slice of onvif-go discovery.Device to project DiscoveredDevice.
func MapDiscoveredDevices(devices []*discovery.Device) []DiscoveredDevice {
	result := make([]DiscoveredDevice, 0, len(devices))
	for _, d := range devices {
		result = append(result, MapDiscoveredDevice(d))
	}
	return result
}
