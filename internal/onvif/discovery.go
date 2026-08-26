package onvif

import (
	"context"
	"crypto/rand"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
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

// getDeviceInfoProbe is the SOAP envelope for GetDeviceInformation, used as a
// fallback when WS-Discovery Probe is rejected. Many cameras (e.g. 天视通)
// reject WS-Discovery Probe over HTTP POST but allow unauthenticated
// GetDeviceInformation.
const getDeviceInfoProbe = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
  <s:Body>
    <tds:GetDeviceInformation/>
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

// deviceInfoEnvelope parses a GetDeviceInformationResponse.
type deviceInfoEnvelope struct {
	Body struct {
		GetDeviceInformationResponse struct {
			Manufacturer    string `xml:"Manufacturer"`
			Model           string `xml:"Model"`
			FirmwareVersion string `xml:"FirmwareVersion"`
			SerialNumber    string `xml:"SerialNumber"`
			HardwareId      string `xml:"HardwareId"`
		} `xml:"GetDeviceInformationResponse"`
	} `xml:"Body"`
}

// discoverFunc is the backing implementation of Discover / DiscoverNoEnrich.
// It is a package-level indirection so that tests in this module can stub it
// out to avoid the real UDP multicast WS-Discovery, which is
// network-dependent and non-deterministic in CI sandboxes (issue #221:
// TestONVIFDiscoverEndpoint assumed "no devices on the network" and asserted
// len(devices)==0, which failed when a CI runner shared a LAN with a real ONVIF
// camera). Atomic: a parallel test's SetDiscoverFuncForTest restore races the
// Discover calls other parallel tests are still serving (-race). Production
// code must never reassign this; only test code does.
var discoverFunc atomic.Pointer[func(ctx context.Context, timeout time.Duration, enrich bool) *DiscoveryResult]

func init() {
	defaultDiscover := discover
	discoverFunc.Store(&defaultDiscover)
}

// SetDiscoverFuncForTest replaces the discovery implementation for the
// duration of a test. Pass nil to restore the default (real WS-Discovery). It
// exists so callers in other packages (e.g. internal/api handler tests) can
// make the /api/onvif/discover endpoint deterministic without performing real
// network multicast. The returned restore func MUST be deferred by the caller.
//
// Usage:
//
//	restore := onvif.SetDiscoverFuncForTest(func(ctx context.Context, timeout time.Duration, enrich bool) *onvif.DiscoveryResult {
//	    return &onvif.DiscoveryResult{Devices: []onvif.DiscoveredDevice{}}
//	})
//	defer restore()
func SetDiscoverFuncForTest(fn func(ctx context.Context, timeout time.Duration, enrich bool) *DiscoveryResult) (restore func()) {
	prev := discoverFunc.Load()
	if fn == nil {
		defaultDiscover := discover
		fn = defaultDiscover
	}
	discoverFunc.Store(&fn)
	return func() { discoverFunc.Store(prev) }
}

// callDiscover invokes the current discovery implementation.
func callDiscover(ctx context.Context, timeout time.Duration, enrich bool) *DiscoveryResult {
	if fn := discoverFunc.Load(); fn != nil {
		return (*fn)(ctx, timeout, enrich)
	}
	return discover(ctx, timeout, enrich)
}

// Discover performs WS-Discovery to find ONVIF devices on the local network
// via UDP multicast, then enriches each device with GetDeviceInformation.
// The enrichment runs in parallel and does not require authentication.
// Returns a DiscoveryResult with categorized errors.
// The result always contains a non-nil Devices slice (empty when no devices found).
//
// This is the variant for callers that consume the enriched fields directly
// (e.g. the manual-scan API handler, which returns Manufacturer/Firmware to the
// UI). Background callers that re-enrich themselves (autodiscover.Scanner →
// Adder.HandleDiscovered) should use DiscoverNoEnrich to avoid a redundant
// GetDeviceInformation round-trip per device (issue #161).
func Discover(ctx context.Context, timeout time.Duration) *DiscoveryResult {
	return callDiscover(ctx, timeout, true)
}

// DiscoverNoEnrich is the same as Discover but WITHOUT the internal
// GetDeviceInformation enrichment pass. Use it when the caller will enrich the
// devices itself (and wants its own dedup/gating to apply BEFORE enrichment) —
// notably autodiscover.Scanner, whose Adder.HandleDiscovered re-runs
// EnrichDevice and would otherwise pay for GetDeviceInformation twice per
// device on the first scan (once inside Discover, once inside HandleDiscovered).
// The returned devices have Endpoint/XAddrs/Scopes populated but
// Manufacturer/Model/Firmware/Serial empty (filled in later by the caller).
func DiscoverNoEnrich(ctx context.Context, timeout time.Duration) *DiscoveryResult {
	return callDiscover(ctx, timeout, false)
}

// discover is the shared core of Discover / DiscoverNoEnrich. When enrich is
// true, devices are enriched with GetDeviceInformation in parallel before
// returning; when false, the caller owns enrichment.
func discover(ctx context.Context, timeout time.Duration, enrich bool) *DiscoveryResult {
	if timeout <= 0 {
		timeout = defaultDiscoveryTimeout
	}

	logger.Info("starting ONVIF device discovery", "timeout", timeout, "enrich", enrich)

	devices, err := discovery.Discover(ctx, timeout)
	if err != nil {
		logger.Warn("WS-Discovery returned error", "error", err)
		return &DiscoveryResult{
			Devices: []DiscoveredDevice{},
			Error:   categorizeDiscoveryError(ctx, err),
		}
	}

	result := MapDiscoveredDevices(devices)
	if len(result) == 0 {
		logger.Info("ONVIF discovery completed, no devices found")
		return &DiscoveryResult{
			Devices: []DiscoveredDevice{},
			Error: &DiscoveryError{
				Category: "NO_DEVICES",
				Message:  "no ONVIF devices found on the network",
			},
		}
	}

	if enrich {
		// Enrich devices with GetDeviceInformation (no auth, best-effort).
		// Use a fresh background context with its own timeout so enrichment
		// is not affected by the expired discovery context.
		enrichCtx, enrichCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer enrichCancel()
		enrichDevices(enrichCtx, result)
	}

	logger.Info("ONVIF discovery completed", "device_count", len(result), "enriched", enrich)
	return &DiscoveryResult{Devices: result}
}

// ProbeDevice probes an ONVIF device at a specific host:port. It first tries
// WS-Discovery Probe via HTTP POST. If that fails (some cameras reject it with
// auth errors), it falls back to a GetDeviceInformation SOAP request which is
// widely supported without authentication.
// Returns nil (not error) if the device is not ONVIF or doesn't respond.
func ProbeDevice(ctx context.Context, host string, port int, timeout time.Duration) (device *DiscoveredDevice, err error) {
	if timeout <= 0 {
		timeout = defaultDiscoveryTimeout
	}

	endpoint := fmt.Sprintf("http://%s:%d/onvif/device_service", host, port)
	logger.Info("probing ONVIF device", "endpoint", endpoint, "timeout", timeout)

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Catch panics from any strategy to prevent crashing the HTTP handler.
	defer func() {
		if r := recover(); r != nil {
			logger.Warn("ProbeDevice panic recovered", "endpoint", endpoint, "panic", r)
			device, err = nil, fmt.Errorf("probe panic: %v", r)
		}
	}()

	// Strategy 1: WS-Discovery Probe via HTTP POST
	if d, e := probeViaWSDiscovery(ctx, endpoint); e != nil || d != nil {
		return d, e
	}

	// Strategy 2: Fallback — GetDeviceInformation
	logger.Debug("WS-Discovery probe failed, trying GetDeviceInformation fallback", "endpoint", endpoint)
	return probeViaGetDeviceInformation(ctx, endpoint)
}

// probeViaWSDiscovery sends a WS-Discovery Probe SOAP message via HTTP POST.
func probeViaWSDiscovery(ctx context.Context, endpoint string) (*DiscoveredDevice, error) {
	messageID := generateProbeUUID()
	probeMsg := fmt.Sprintf(wsDiscoveryProbe, messageID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(probeMsg))
	if err != nil {
		return nil, fmt.Errorf("create probe request: %w", err)
	}
	req.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("probe request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Non-200 on a WS-Discovery Probe is common (some cameras reject Probe over
		// HTTP). Log the response snippet for diagnosability, then return nil so the
		// caller falls through to the GetDeviceInformation strategy.
		snippet := readSnippet(resp.Body, 256)
		logger.Debug("WS-Discovery probe returned non-200, will try GetDeviceInformation fallback",
			"endpoint", endpoint, "status", resp.StatusCode, "body_snippet", snippet)
		return nil, nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		return nil, fmt.Errorf("read probe response: %w", err)
	}

	return parseProbeMatchResponse(body, endpoint)
}

// probeViaGetDeviceInformation sends a GetDeviceInformation SOAP request as
// a fallback when WS-Discovery Probe is rejected. Many cameras allow
// unauthenticated GetDeviceInformation even though they require auth for Probe.
func probeViaGetDeviceInformation(ctx context.Context, endpoint string) (*DiscoveredDevice, error) {
	info, err := fetchDeviceInformation(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	if info == nil {
		// fetchDeviceInformation returns nil when the device didn't respond with
		// parseable data — not an error, just "not an ONVIF device" or offline.
		return nil, nil
	}

	name := info.Manufacturer
	if name == "" {
		name = info.Model
	}

	return &DiscoveredDevice{
		UUID:         info.SerialNumber,
		Name:         name,
		XAddrs:       []string{endpoint},
		Scopes:       []string{},
		Hardware:     info.HardwareId,
		Endpoint:     endpoint,
		Manufacturer: info.Manufacturer,
		Model:        info.Model,
		Firmware:     info.FirmwareVersion,
		Serial:       info.SerialNumber,
	}, nil
}

// enrichDevices enriches discovered devices with GetDeviceInformation in parallel.
// Best-effort: failures are logged and silently skipped.
func enrichDevices(ctx context.Context, devices []DiscoveredDevice) {
	type enrichResult struct {
		index int
		info  *deviceInfoFields
	}

	ch := make(chan enrichResult, len(devices))
	for i, d := range devices {
		endpoint := d.Endpoint
		if endpoint == "" && len(d.XAddrs) > 0 {
			endpoint = d.XAddrs[0]
		}
		if endpoint == "" {
			continue
		}
		go func(idx int, ep string) {
			defer func() {
				if r := recover(); r != nil {
					logger.Warn("enrichment goroutine panic recovered", "endpoint", ep, "panic", r)
					ch <- enrichResult{index: idx, info: nil}
				}
			}()
			info, _ := fetchDeviceInformation(ctx, ep)
			ch <- enrichResult{index: idx, info: info}
		}(i, endpoint)
	}

	for range devices {
		result := <-ch
		if result.info == nil {
			continue
		}
		d := &devices[result.index]
		if d.Name == "" && result.info.Manufacturer != "" {
			d.Name = result.info.Manufacturer
		}
		if d.Manufacturer == "" {
			d.Manufacturer = result.info.Manufacturer
		}
		if d.Model == "" {
			d.Model = result.info.Model
		}
		if d.Firmware == "" {
			d.Firmware = result.info.FirmwareVersion
		}
		if d.Hardware == "" {
			d.Hardware = result.info.HardwareId
		}
		// Capture the serial so it can be sent as stable_id at add time — this
		// makes the camera immediately self-healable (IP re-acquisition by ONVIF
		// serial) without waiting for the async ensureStableID goroutine that
		// runs after the recorder connects. Previously the serial was fetched
		// here but discarded (no field on DiscoveredDevice to hold it).
		if d.Serial == "" {
			d.Serial = result.info.SerialNumber
		}
	}
}

// deviceInfoFields holds the parsed fields from GetDeviceInformationResponse.
type deviceInfoFields struct {
	Manufacturer    string
	Model           string
	FirmwareVersion string
	SerialNumber    string
	HardwareId      string
}

// fetchDeviceInformation sends a GetDeviceInformation SOAP request and returns
// parsed device info. Returns (nil, nil) if the device doesn't respond or returns
// empty/invalid data. No authentication required.
func fetchDeviceInformation(ctx context.Context, endpoint string) (*deviceInfoFields, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(getDeviceInfoProbe))
	if err != nil {
		return nil, fmt.Errorf("create GetDeviceInformation request: %w", err)
	}
	req.Header.Set("Content-Type", "application/soap+xml")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GetDeviceInformation request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet := readSnippet(resp.Body, 256)
		logger.Debug("GetDeviceInformation returned non-200, device may not be ONVIF or may require auth",
			"endpoint", endpoint, "status", resp.StatusCode, "body_snippet", snippet)
		return nil, nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read GetDeviceInformation response: %w", err)
	}

	var envelope deviceInfoEnvelope
	if err := xml.Unmarshal(body, &envelope); err != nil {
		logger.Debug("failed to parse GetDeviceInformation response", "endpoint", endpoint, "error", err)
		return nil, nil
	}

	info := envelope.Body.GetDeviceInformationResponse
	if info.SerialNumber == "" && info.FirmwareVersion == "" && info.HardwareId == "" {
		logger.Debug("GetDeviceInformation returned empty device info", "endpoint", endpoint)
		return nil, nil
	}

	logger.Info("enriched device info", "endpoint", endpoint, "manufacturer", info.Manufacturer, "model", info.Model, "firmware", info.FirmwareVersion)

	return &deviceInfoFields{
		Manufacturer:    info.Manufacturer,
		Model:           info.Model,
		FirmwareVersion: info.FirmwareVersion,
		SerialNumber:    info.SerialNumber,
		HardwareId:      info.HardwareId,
	}, nil
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

// categorizeDiscoveryError maps a discovery error to a DiscoveryError category.
func categorizeDiscoveryError(ctx context.Context, err error) *DiscoveryError {
	if err == nil {
		return nil
	}

	msg := err.Error()

	ctxErr := ctx.Err()
	if ctxErr == context.DeadlineExceeded {
		return &DiscoveryError{Category: "TIMEOUT", Message: "discovery timed out: " + msg}
	}
	if ctxErr == context.Canceled {
		return &DiscoveryError{Category: "TIMEOUT", Message: "discovery was cancelled"}
	}
	if strings.Contains(msg, "deadline exceeded") || strings.Contains(msg, "timeout") {
		return &DiscoveryError{Category: "TIMEOUT", Message: "discovery timed out: " + msg}
	}

	// Check for network errors
	if strings.Contains(msg, "network is unreachable") ||
		strings.Contains(msg, "no route to host") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "dial ") ||
		strings.Contains(msg, "resolve") ||
		strings.Contains(msg, "DNS") {
		return &DiscoveryError{Category: "NETWORK", Message: "network error: " + msg}
	}

	// Default to PARSE_ERROR for unexpected errors
	return &DiscoveryError{Category: "PARSE_ERROR", Message: msg}
}

// readSnippet reads up to maxLen bytes from r for diagnostic logging. Best-effort:
// errors are ignored (returns "" on failure). Used to surface a response body
// fragment when a probe returns a non-200 status, so logs explain WHY a device
// was skipped (e.g. a SOAP Fault, a redirect, an auth challenge).
func readSnippet(r io.Reader, maxLen int) string {
	if r == nil {
		return ""
	}
	buf := make([]byte, maxLen)
	n, _ := io.ReadFull(r, buf)
	if n <= 0 {
		return ""
	}
	// io.ReadFull returns ErrUnexpectedEOF when fewer than len(buf) bytes exist;
	// trim to what we actually got.
	if n < len(buf) {
		buf = buf[:n]
	}
	// Collapse whitespace for compact log lines.
	s := strings.TrimSpace(string(buf))
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > maxLen {
		s = s[:maxLen]
	}
	return s
}
