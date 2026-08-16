package onvif

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// ProbePorts lists the host-relative ports tried by ProbeSerial, in order:
// port conventions of the devices we care about — mibee-rec serves ONVIF on
// :8080, ESP32 MiBeeCam on :80, and :8000 is the Hikvision convention (auth
// usually required there — a failed probe simply yields no fingerprint).
// Exported as a var so tests can point it at a local listener.
var ProbePorts = []string{"8080", "80", "8000"}

const probePath = "/onvif/device_service"

// probeTimeout bounds each endpoint attempt. The overall probe is the sum
// across ports, but callers run it asynchronously (GB enroll) or on a manual
// action (camera create), so a few seconds worst case is acceptable.
const probeTimeout = 1200 * time.Millisecond

var serialRe = regexp.MustCompile(`(?i)<(?:[\w.-]+:)?SerialNumber>\s*([^<]+?)\s*</(?:[\w.-]+:)?SerialNumber>`)

const getDeviceInfoBody = `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">` +
	`<s:Body xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:xsd="http://www.w3.org/2001/XMLSchema">` +
	`<GetDeviceInformation xmlns="http://www.onvif.org/ver10/device/wsdl"/>` +
	`</s:Body></s:Envelope>`

// ProbeSerial queries a device's ONVIF GetDeviceInformation WITHOUT credentials
// and returns its SerialNumber. It is the cross-protocol correlation probe for
// GB28181 auto-enroll dedup: a dual-protocol camera serves the same serial on
// every interface, so probing the SIP source IP identifies a camera that was
// added via ONVIF from a DIFFERENT interface IP (e.g. USB NIC vs WiFi).
// Returns ("", false) when no endpoint answers or none exposes a serial —
// pure-GB28181 cameras (Hikvision/Dahua with auth-required ONVIF) legitimately
// land here and simply fall back to IP-based matching.
func ProbeSerial(ctx context.Context, ip string) (string, bool) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return "", false
	}
	client := &http.Client{Timeout: probeTimeout}
	for _, port := range ProbePorts {
		select {
		case <-ctx.Done():
			return "", false
		default:
		}
		if serial, ok := probeEndpoint(ctx, client, fmt.Sprintf("http://%s:%s%s", ip, port, probePath)); ok {
			return serial, true
		}
	}
	return "", false
}

func probeEndpoint(ctx context.Context, client *http.Client, url string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(getDeviceInfoBody))
	if err != nil {
		return "", false
	}
	req.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")
	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer func() { _ = resp.Body.Close() }()
	// 401/405 etc. mean ONVIF-with-auth or no ONVIF here — not an error worth
	// logging; try the next endpoint.
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", false
	}
	m := serialRe.FindSubmatch(body)
	if len(m) != 2 {
		return "", false
	}
	serial := strings.TrimSpace(string(m[1]))
	if serial == "" {
		return "", false
	}
	return serial, true
}
