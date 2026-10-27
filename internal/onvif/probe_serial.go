package onvif

import (
	"context"
	"strconv"

	onvifdiscovery "github.com/mickeyzzc/onvif-go/v2/discovery"
)

// ProbePorts lists the host-relative ports tried by ProbeSerial, in order:
// port conventions of the devices we care about — mibee-rec serves ONVIF on
// :8080, ESP32 MiBeeCam on :80, and :8000 is the Hikvision convention (auth
// usually required there — a failed probe simply yields no fingerprint).
// Exported as a var so tests can point it at a local listener.
var ProbePorts = []string{"8080", "80", "8000"}

// ProbeSerial queries a device's ONVIF GetDeviceInformation WITHOUT credentials
// and returns its SerialNumber. It is the cross-protocol correlation probe for
// GB28181 auto-enroll dedup: a dual-protocol camera serves the same serial on
// every interface, so probing the SIP source IP identifies a camera that was
// added via ONVIF from a DIFFERENT interface IP (e.g. USB NIC vs WiFi).
// Returns ("", false) when no endpoint answers or none exposes a serial —
// pure-GB28181 cameras (Hikvision/Dahua with auth-required ONVIF) legitimately
// land here and simply fall back to IP-based matching.
func ProbeSerial(ctx context.Context, ip string) (string, bool) {
	ports := make([]int, 0, len(ProbePorts))
	for _, p := range ProbePorts {
		if n, err := strconv.Atoi(p); err == nil {
			ports = append(ports, n)
		}
	}
	return onvifdiscovery.ProbeSerial(ctx, ip, ports)
}
