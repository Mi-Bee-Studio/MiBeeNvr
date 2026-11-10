package onboarding

import (
	"context"
	"encoding/xml"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
)

// Candidate states for hosts found on the factory-default subnet.
const (
	// CandidateUnactivated: a factory-fresh camera (serves ISAPI deviceInfo
	// with activated=false, no credentials yet). The true zero-touch target.
	CandidateUnactivated = "unactivated"
	// CandidateActivated: an activated camera that serves deviceInfo
	// without auth (rare; some firmwares) — adopt with credentials.
	CandidateActivated = "activated"
	// CandidateAuthRequired: an activated camera that requires credentials
	// even for deviceInfo — adopt with credentials (guided mode).
	CandidateAuthRequired = "auth_required"
)

// Candidate is one addressable device found during a scan.
type Candidate struct {
	IP           string `json:"ip"`
	State        string `json:"state"`
	Serial       string `json:"serial,omitempty"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model,omitempty"`
	DeviceName   string `json:"device_name,omitempty"`
}

// scanHostTimeout is the per-request budget of one probe; a /24 at
// scanParallel 16 stays inside ~30s worst case (same bounds the
// rediscovery engine uses for the RPi class).
const (
	scanHostTimeout = 1500 * time.Millisecond
	scanParallel    = 16
)

// ScanSubnet probes every host:port in targets (the caller expands a CIDR
// into ip:port pairs) in bounded parallel and returns the classified
// candidates, ordered by IP. Unresponsive/non-device hosts yield nothing.
func ScanSubnet(ctx context.Context, targets []string, timeout time.Duration, parallel int) []Candidate {
	if parallel <= 0 {
		parallel = scanParallel
	}
	if timeout <= 0 {
		timeout = scanHostTimeout
	}

	var mu sync.Mutex
	var out []Candidate
	var wg sync.WaitGroup
	sem := make(chan struct{}, parallel)

	for _, tgt := range targets {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if c := classifyHost(ctx, addr, timeout); c != nil {
				mu.Lock()
				out = append(out, *c)
				mu.Unlock()
			}
		}(tgt)
	}
	wg.Wait()

	sort.Slice(out, func(i, j int) bool {
		return compareIP(out[i].IP, out[j].IP) < 0
	})
	return out
}

// compareIP orders two dotted-quad host strings numerically.
func compareIP(a, b string) int {
	ai := net.ParseIP(a).To4()
	bi := net.ParseIP(b).To4()
	if ai == nil || bi == nil {
		return strings2Compare(a, b)
	}
	return bytesCompare(ai, bi)
}

func bytesCompare(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func strings2Compare(a, b string) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// classifyHost probes one host:port with unauthenticated device probes:
//  1. ISAPI GET /ISAPI/System/deviceInfo (no auth) — the Hikvision
//     bootstrap face; factory-fresh cameras serve it with activated=false.
//  2. unauthenticated ONVIF GetDeviceInformation (onvif.ProbeDevice) — the
//     generic ONVIF answer; carries the serial for adopt-time matching.
//
// A host that answers neither is not a device: nil.
func classifyHost(ctx context.Context, addr string, timeout time.Duration) *Candidate {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if c := classifyISAPI(cctx, addr); c != nil {
		return c
	}

	// Generic ONVIF face: an ONVIF-open device on the factory subnet is
	// adoptable with credentials (state auth_required).
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil
	}
	dev, err := onvif.ProbeDevice(cctx, host, port, timeout)
	if err != nil || dev == nil {
		return nil
	}
	return &Candidate{
		IP:           host,
		State:        CandidateAuthRequired,
		Serial:       dev.Serial,
		Manufacturer: dev.Manufacturer,
		Model:        dev.Model,
		DeviceName:   dev.Name,
	}
}

// isapiDeviceInfo is the Hikvision bootstrap document. Factory-fresh
// devices serve it WITHOUT authentication and report activated=false;
// activated devices either require auth (401) or (rarely) serve it with
// activated=true.
type isapiDeviceInfo struct {
	DeviceName   string `xml:"deviceName"`
	Manufacturer string `xml:"manufacturer"`
	Model        string `xml:"model"`
	SerialNumber string `xml:"serialNumber"`
	Activated    bool   `xml:"activated"`
}

func classifyISAPI(ctx context.Context, addr string) *Candidate {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://"+addr+"/ISAPI/System/deviceInfo", nil)
	if err != nil {
		return nil
	}

	client := &http.Client{Timeout: scanHostTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized {
			return &Candidate{IP: hostPortHost(addr), State: CandidateAuthRequired}
		}
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil
	}

	var info isapiDeviceInfo
	if err := xml.Unmarshal(body, &info); err != nil {
		return nil
	}
	if info.SerialNumber == "" && info.Manufacturer == "" && info.DeviceName == "" {
		return nil // parsed but not a deviceInfo document
	}

	state := CandidateActivated
	if !info.Activated {
		state = CandidateUnactivated
	}
	return &Candidate{
		IP:           hostPortHost(addr),
		State:        state,
		Serial:       info.SerialNumber,
		Manufacturer: info.Manufacturer,
		Model:        info.Model,
		DeviceName:   info.DeviceName,
	}
}

// hostPortHost returns the host part of a host:port string.
func hostPortHost(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}
