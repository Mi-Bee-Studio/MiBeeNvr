package onvif

import (
	"context"
	"crypto/rand"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/0x524a/onvif-go/discovery"
)

const defaultDiscoveryTimeout = 5 * time.Second

// wsDiscoveryProbe is the SOAP envelope for WS-Discovery Probe sent via HTTP POST.
const wsDiscoveryProbe = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:a="http://schemas.xmlsoap.org/ws/2004/08/addressing">
  <s:Header>
    <a:Action s:mustUnderstand="1">http://schemas.xmlsoap.org/ws/2005/04/discovery/Probe</a:Action>
    <a:MessageID>uuid:%s</a:MessageID>
    <a:ReplyTo><a:Address>http://schemas.xmlsoap.org/ws/2004/08/addressing/role/anonymous</a:Address></a:ReplyTo>
    <a:To s:mustUnderstand="1">urn:schemas-xmlsoap-org:ws:2005:04:discovery</a:To>
  </s:Header>
  <s:Body>
    <Probe xmlns="http://schemas.xmlsoap.org/ws/2005/04/discovery">
      <d:Types xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery" xmlns:dp0="http://www.onvif.org/ver10/network/wsdl">dp0:NetworkVideoTransmitter</d:Types>
    </Probe>
  </s:Body>
</s:Envelope>`

// probeMatchEnvelope represents a WS-Discovery ProbeMatches SOAP response.
// Uses local-name matching (Go XML ignores namespace prefixes by default).
type probeMatchEnvelope struct {
	Body struct {
		ProbeMatches struct {
			ProbeMatch []probeMatchEntry `xml:"ProbeMatch"`
		} `xml:"ProbeMatches"`
	} `xml:"Body"`
}

// probeMatchEntry represents a single ProbeMatch inside ProbeMatches.
type probeMatchEntry struct {
	EndpointRef struct {
		Address string `xml:"Address"`
	} `xml:"EndpointReference"`
	Types           string `xml:"Types"`
	Scopes          string `xml:"Scopes"`
	XAddrs          string `xml:"XAddrs"`
	MetadataVersion int    `xml:"MetadataVersion"`
}

// Discover performs WS-Discovery to find ONVIF devices on the local network
// via UDP multicast. Returns empty slice (not error) when no devices are found.
func Discover(ctx context.Context, timeout time.Duration) ([]DiscoveredDevice, error) {
	if timeout <= 0 {
		timeout = defaultDiscoveryTimeout
	}

	logger.Info("starting ONVIF device discovery", "timeout", timeout)

	devices, err := discovery.Discover(ctx, timeout)
	if err != nil {
		logger.Warn("WS-Discovery returned error, returning empty results", "error", err)
		return []DiscoveredDevice{}, nil
	}

	result := MapDiscoveredDevices(devices)
	logger.Info("ONVIF discovery completed", "device_count", len(result))
	return result, nil
}

// ProbeDevice sends a direct WS-Discovery Probe via HTTP POST to a specific
// host:port. This bypasses UDP multicast and works across subnets.
// Returns nil (not error) if the device is not ONVIF or doesn't respond.
func ProbeDevice(ctx context.Context, host string, port int, timeout time.Duration) (*DiscoveredDevice, error) {
	if timeout <= 0 {
		timeout = defaultDiscoveryTimeout
	}

	endpoint := fmt.Sprintf("http://%s:%d/onvif/device_service", host, port)
	logger.Info("probing ONVIF device", "endpoint", endpoint, "timeout", timeout)

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	messageID := generateProbeUUID()
	probeMsg := fmt.Sprintf(wsDiscoveryProbe, messageID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(probeMsg))
	if err != nil {
		return nil, fmt.Errorf("create probe request: %w", err)
	}
	req.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("probe request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logger.Debug("device returned non-200 status", "endpoint", endpoint, "status", resp.StatusCode)
		return nil, nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		return nil, fmt.Errorf("read probe response: %w", err)
	}

	return parseProbeMatchResponse(body, endpoint)
}

// parseProbeMatchResponse parses a WS-Discovery ProbeMatches SOAP response
// and converts the first ProbeMatch to a DiscoveredDevice.
func parseProbeMatchResponse(data []byte, endpoint string) (*DiscoveredDevice, error) {
	var envelope probeMatchEnvelope
	if err := xml.Unmarshal(data, &envelope); err != nil {
		logger.Debug("failed to parse probe response XML", "error", err)
		return nil, nil
	}

	if len(envelope.Body.ProbeMatches.ProbeMatch) == 0 {
		return nil, nil
	}

	pm := envelope.Body.ProbeMatches.ProbeMatch[0]
	scopes := strings.Fields(pm.Scopes)
	xaddrs := strings.Fields(pm.XAddrs)

	var name, hardware string
	for _, scope := range scopes {
		if strings.Contains(scope, "/name/") {
			parts := strings.Split(scope, "/")
			name = parts[len(parts)-1]
		}
		if strings.Contains(scope, "/hardware/") {
			parts := strings.Split(scope, "/")
			hardware = parts[len(parts)-1]
		}
	}

	deviceEndpoint := endpoint
	if len(xaddrs) > 0 {
		deviceEndpoint = xaddrs[0]
	}

	return &DiscoveredDevice{
		UUID:     pm.EndpointRef.Address,
		Name:     name,
		XAddrs:   xaddrs,
		Scopes:   scopes,
		Hardware: hardware,
		Endpoint: deviceEndpoint,
	}, nil
}

// generateProbeUUID generates a random UUID v4 for the WS-Discovery MessageID.
func generateProbeUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // Version 4
	b[8] = (b[8] & 0x3f) | 0x80 // Variant 10
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
