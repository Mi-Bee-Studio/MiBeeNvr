package rediscovery

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
)

// strict helpers
func mustEqual[T comparable](tb testing.TB, got, want T, msg string) {
	tb.Helper()
	if got != want {
		tb.Fatalf("%s: got %v, want %v", msg, got, want)
	}
}

func mustContain(tb testing.TB, s, sub string, msg string) {
	tb.Helper()
	if !contains(s, sub) {
		tb.Fatalf("%s: %q does not contain %q", msg, s, sub)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// fakeProbe returns a scripted map of host -> serial number. A host present in
// the map responds with a device whose UUID is the serial; absent hosts behave
// like an unreachable camera (error).
func fakeProbe(hostSerials map[string]string, latency time.Duration) ProbeFunc {
	return func(ctx context.Context, host string, port int, timeout time.Duration) (*onvif.DiscoveredDevice, error) {
		if latency > 0 {
			select {
			case <-time.After(latency):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		serial, ok := hostSerials[host]
		if !ok {
			return nil, errUnreachable
		}
		return &onvif.DiscoveredDevice{UUID: serial, Endpoint: "http://" + host + "/onvif/device_service"}, nil
	}
}

type stringErr string

func (e stringErr) Error() string { return string(e) }

const errUnreachable stringErr = "unreachable"

func TestDiscoverByStableID_MatchesBySerial(t *testing.T) {
	// Camera last known at .10; it roamed to .50. Same serial everywhere.
	cam := config.CameraConfig{
		ID:            "cam-1",
		Protocol:      "onvif",
		StableID:      "SN-AAA",
		ONVIFEndpoint: "http://192.0.2.10:80/onvif/device_service",
		// Use TEST-NET-1 (192.0.2.0/24) so localSubnets() does not pollute it.
		SubnetHints: []string{"192.0.2.0/24"},
	}
	// Only the camera at .50 reports the matching serial; .10 is gone.
	probe := fakeProbe(map[string]string{
		"192.0.2.50": "SN-AAA",
		"192.0.2.11": "SN-OTHER",
	}, 0)

	eng := NewEngine(Config{MaxParallel: 32, ProbeTimeout: time.Second, MaxDuration: 5 * time.Second}, probe)
	res, err := eng.DiscoverByStableID(context.Background(), cam)
	if err != nil {
		t.Fatalf("expected match, got error: %v", err)
	}
	mustEqual(t, res.NewHost, "192.0.2.50", "new host")
	mustEqual(t, res.Port, 80, "port")
	mustContain(t, res.NewEndpoint, "192.0.2.50", "endpoint host")
	mustContain(t, res.NewEndpoint, "/onvif/device_service", "endpoint path")
}

func TestDiscoverByStableID_NotFound(t *testing.T) {
	cam := config.CameraConfig{
		ID:            "cam-1",
		Protocol:      "onvif",
		StableID:      "SN-MISSING",
		ONVIFEndpoint: "http://192.0.2.10/onvif/device_service",
		SubnetHints:   []string{"192.0.2.0/24"},
	}
	// No host reports SN-MISSING.
	probe := fakeProbe(map[string]string{
		"192.0.2.11": "SN-OTHER",
		"192.0.2.12": "SN-OTHER2",
	}, 0)

	eng := NewEngine(Config{MaxParallel: 32, ProbeTimeout: time.Second, MaxDuration: 5 * time.Second}, probe)
	_, err := eng.DiscoverByStableID(context.Background(), cam)
	mustEqual(t, err, ErrNotFound, "not found error")
}

func TestDiscoverByStableID_UnsupportedProtocol(t *testing.T) {
	cam := config.CameraConfig{ID: "c", Protocol: "rtsp", StableID: "SN"}
	eng := NewEngine(Config{MaxParallel: 4, ProbeTimeout: time.Second, MaxDuration: time.Second}, fakeProbe(nil, 0))
	_, err := eng.DiscoverByStableID(context.Background(), cam)
	mustEqual(t, err, ErrUnsupportedProtocol, "unsupported protocol error")
}

func TestDiscoverByStableID_NoStableID(t *testing.T) {
	cam := config.CameraConfig{ID: "c", Protocol: "onvif"} // no StableID
	eng := NewEngine(Config{MaxParallel: 4, ProbeTimeout: time.Second, MaxDuration: time.Second}, fakeProbe(nil, 0))
	_, err := eng.DiscoverByStableID(context.Background(), cam)
	mustEqual(t, err, ErrNoStableID, "no stable id error")
}

func TestDiscoverByStableID_PortPreservedFromEndpoint(t *testing.T) {
	// ONVIF on a non-default port (e.g. 8000) should be reused on the new IP.
	cam := config.CameraConfig{
		ID:            "cam-1",
		Protocol:      "onvif",
		StableID:      "SN-X",
		ONVIFEndpoint: "http://192.0.2.10:8000/onvif/device_service",
		SubnetHints:   []string{"192.0.2.0/24"},
	}
	probe := fakeProbe(map[string]string{"192.0.2.20": "SN-X"}, 0)
	eng := NewEngine(Config{MaxParallel: 32, ProbeTimeout: time.Second, MaxDuration: 5 * time.Second}, probe)
	res, err := eng.DiscoverByStableID(context.Background(), cam)
	if err != nil {
		t.Fatalf("expected match: %v", err)
	}
	mustEqual(t, res.Port, 8000, "non-default port preserved")
	mustContain(t, res.NewEndpoint, ":8000/", "endpoint carries port")
}

func TestScanFor_StopsAtMaxParallel(t *testing.T) {
	// Issue 100 concurrent probes; ensure the worker pool never exceeds MaxParallel.
	const max = 8
	const total = 100
	var inflight, peak int32
	var mu sync.Mutex
	var probeCount int32

	probe := func(ctx context.Context, host string, port int, timeout time.Duration) (*onvif.DiscoveredDevice, error) {
		cur := atomic.AddInt32(&inflight, 1)
		atomic.AddInt32(&probeCount, 1)
		mu.Lock()
		if int(cur) > int(peak) {
			peak = cur
		}
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		atomic.AddInt32(&inflight, -1)
		return nil, errUnreachable // none match
	}

	hosts := make([]string, total)
	for i := range hosts {
		hosts[i] = "10.255.0." + itoa(i+1)
	}

	eng := NewEngine(Config{MaxParallel: max, ProbeTimeout: time.Second, MaxDuration: 10 * time.Second}, probe)
	got := eng.scanFor(context.Background(), hosts, 80, "nobody")
	mustEqual(t, got, "", "no match expected")
	mustEqual(t, int(peak) <= max, true, "peak concurrency must not exceed MaxParallel")
	mustEqual(t, int(atomic.LoadInt32(&probeCount)), total, "all hosts probed")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func TestBuildCandidates_DeduplicatesAndSkipsBroadcast(t *testing.T) {
	cam := config.CameraConfig{
		ID:            "c",
		ONVIFEndpoint: "http://192.0.2.5/onvif/device_service",
		SubnetHints:   []string{"192.0.2.0/24"},
	}
	got := buildCandidates(cam, 80)

	seen := make(map[string]int)
	for _, h := range got {
		seen[h]++
	}
	for h, c := range seen {
		mustEqual(t, c, 1, "host "+h+" should appear exactly once (dedup)")
	}
	// Network (.0) and broadcast (.255) must NOT be present.
	mustEqual(t, seen["192.0.2.0"], 0, "network addr skipped")
	mustEqual(t, seen["192.0.2.255"], 0, "broadcast addr skipped")
	// Last-known host must be present and first.
	mustEqual(t, got[0], "192.0.2.5", "last-known host first")
}

func TestFromConfig_AppliesDefaults(t *testing.T) {
	// Empty config → defaults (MaxParallel 16, 2s probe, 30s max).
	c := FromConfig(config.RediscoveryConfig{})
	mustEqual(t, c.MaxParallel, 16, "default max parallel")
	mustEqual(t, c.ProbeTimeout, 2*time.Second, "default probe timeout")
	mustEqual(t, c.MaxDuration, 30*time.Second, "default max duration")

	// Explicit values honoured.
	c2 := FromConfig(config.RediscoveryConfig{
		MaxParallel:  8,
		ProbeTimeout: "1.5s",
		MaxDuration:  "10s",
	})
	mustEqual(t, c2.MaxParallel, 8, "explicit max parallel")
	mustEqual(t, c2.ProbeTimeout, 1500*time.Millisecond, "explicit probe timeout")
}
