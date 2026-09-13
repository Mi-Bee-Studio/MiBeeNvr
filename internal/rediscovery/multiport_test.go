package rediscovery

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
)

// fakeProbePorts scripts responses keyed by "host:port" so tests can place a
// device on a NON-STANDARD ONVIF port. Host/port pairs absent from the map
// behave like an unreachable camera (error).
func fakeProbePorts(portSerials map[string]string) ProbeFunc {
	return func(ctx context.Context, host string, port int, timeout time.Duration) (*onvif.DiscoveredDevice, error) {
		serial, ok := portSerials[fmt.Sprintf("%s:%d", host, port)]
		if !ok {
			return nil, errUnreachable
		}
		return &onvif.DiscoveredDevice{
			UUID:     serial,
			Endpoint: fmt.Sprintf("http://%s:%d/onvif/device_service", host, port),
		}, nil
	}
}

// equalIntSlice compares two int slices, treating nil and empty as equal.
func equalIntSlice(tb testing.TB, got, want []int, msg string) {
	tb.Helper()
	if len(got) != len(want) {
		tb.Fatalf("%s: got %v, want %v", msg, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			tb.Fatalf("%s: got %v, want %v (mismatch at %d)", msg, got, want, i)
		}
	}
}

// TestPortsFor_DefaultsAndOverride pins the port-list resolution semantics:
//   - last-known port is ALWAYS first (highest probability of a hit);
//   - an empty ProbePorts list falls back to the default sweep table;
//   - an explicit ProbePorts list REPLACES the defaults (the user knows their
//     network) but the last-known port is still prepended;
//   - duplicates are removed and the final list is capped at maxProbePorts.
func TestPortsFor_DefaultsAndOverride(t *testing.T) {
	c := Config{}

	// Last-known 80 is the default table's head — no duplicate.
	equalIntSlice(t, c.portsFor(80), []int{80, 8080, 8899}, "defaults with lastKnown 80")

	// Last-known non-default port is prepended before the default table.
	equalIntSlice(t, c.portsFor(8000), []int{8000, 80, 8080, 8899}, "defaults with lastKnown 8000")

	// Explicit list replaces defaults; last-known still first.
	equalIntSlice(t, Config{ProbePorts: []int{8123}}.portsFor(80), []int{80, 8123}, "explicit list")

	// Dedup: last-known already in the explicit list must not be probed twice.
	equalIntSlice(t, Config{ProbePorts: []int{8080, 80}}.portsFor(8080), []int{8080, 80}, "dedup")

	// Cap: a long list is truncated to maxProbePorts.
	long := make([]int, 0, 12)
	for p := 9000; len(long) < 12; p++ {
		long = append(long, p)
	}
	got := Config{ProbePorts: long}.portsFor(80)
	if len(got) > maxProbePorts {
		t.Fatalf("portsFor must cap at %d, got %d (%v)", maxProbePorts, len(got), got)
	}
	mustEqual(t, got[0], 80, "last-known survives the cap")
}

// TestDiscoverByStableID_MultiPortSweep: endpoint stored WITHOUT a port (the
// default-80 assumption), but the device actually serves ONVIF on 8080 (factory
// non-standard config or a firmware change). The scan must find it via the
// default probe-port sweep and report the MATCHED port.
func TestDiscoverByStableID_MultiPortSweep(t *testing.T) {
	cam := config.CameraConfig{
		ID:            "cam-1",
		Protocol:      "onvif",
		StableID:      "SN-8080",
		ONVIFEndpoint: "http://192.0.2.10/onvif/device_service", // no port → last-known 80
		SubnetHints:   []string{"192.0.2.0/24"},
	}
	probe := fakeProbePorts(map[string]string{
		"192.0.2.50:8080": "SN-8080",
	})

	eng := NewEngine(Config{MaxParallel: 32, ProbeTimeout: time.Second, MaxDuration: 5 * time.Second}, probe)
	res, err := eng.DiscoverByStableID(context.Background(), cam)
	if err != nil {
		t.Fatalf("expected match on non-default port, got error: %v", err)
	}
	mustEqual(t, res.NewHost, "192.0.2.50", "host")
	mustEqual(t, res.Port, 8080, "matched port")
	mustContain(t, res.NewEndpoint, ":8080/", "endpoint carries matched port")
}

// TestDiscoverByStableID_LastKnownPortProbedFirst: probes for a single host run
// sequentially in ports order, so the last-known port must be the FIRST probe
// on the last-known host, with the default ports following.
func TestDiscoverByStableID_LastKnownPortProbedFirst(t *testing.T) {
	cam := config.CameraConfig{
		ID:            "cam-1",
		Protocol:      "onvif",
		StableID:      "SN-ORDER",
		ONVIFEndpoint: "http://192.0.2.10:8899/onvif/device_service",
		SubnetHints:   []string{"192.0.2.0/24"},
	}
	var mu sync.Mutex
	var order []string // "host:port" in probe-call order
	probe := func(ctx context.Context, host string, port int, timeout time.Duration) (*onvif.DiscoveredDevice, error) {
		mu.Lock()
		order = append(order, fmt.Sprintf("%s:%d", host, port))
		mu.Unlock()
		return nil, errUnreachable
	}

	eng := NewEngine(Config{MaxParallel: 32, ProbeTimeout: time.Second, MaxDuration: 5 * time.Second}, probe)
	_, err := eng.DiscoverByStableID(context.Background(), cam)
	mustEqual(t, err, ErrNotFound, "no device answers")

	// Collect the probes aimed at the last-known host. Workers iterate the port
	// list sequentially per host, so this order is deterministic.
	var lastKnownPorts []string
	for _, hp := range order {
		if strings.HasPrefix(hp, "192.0.2.10:") {
			lastKnownPorts = append(lastKnownPorts, strings.TrimPrefix(hp, "192.0.2.10:"))
		}
	}
	if len(lastKnownPorts) < 3 {
		t.Fatalf("expected >=3 probes on last-known host, got %v", lastKnownPorts)
	}
	mustEqual(t, lastKnownPorts[0], "8899", "last-known port probed first")
	seen := map[string]bool{}
	for _, p := range lastKnownPorts {
		seen[p] = true
	}
	for _, want := range []string{"80", "8080"} {
		if !seen[want] {
			t.Fatalf("default port %s not probed on last-known host: %v", want, lastKnownPorts)
		}
	}
}

// TestDiscoverByStableID_ProbePortsConfigOverride: an explicit probe_ports list
// is honored — the sweep finds a device on a port that is neither the default
// table nor the last-known port.
func TestDiscoverByStableID_ProbePortsConfigOverride(t *testing.T) {
	cam := config.CameraConfig{
		ID:            "cam-1",
		Protocol:      "onvif",
		StableID:      "SN-CUSTOM",
		ONVIFEndpoint: "http://192.0.2.10:9000/onvif/device_service",
		SubnetHints:   []string{"192.0.2.0/24"},
	}
	probe := fakeProbePorts(map[string]string{
		"192.0.2.30:8123": "SN-CUSTOM",
	})

	eng := NewEngine(Config{MaxParallel: 32, ProbeTimeout: time.Second, MaxDuration: 5 * time.Second, ProbePorts: []int{8123}}, probe)
	res, err := eng.DiscoverByStableID(context.Background(), cam)
	if err != nil {
		t.Fatalf("expected match on configured port, got error: %v", err)
	}
	mustEqual(t, res.NewHost, "192.0.2.30", "host")
	mustEqual(t, res.Port, 8123, "matched port")
}

// TestDiscoverByStableID_ProbePortsDedup: a port that is both last-known and in
// the configured list must be probed exactly ONCE per host (no duplicate probes,
// which would double the scan cost on the RPi).
func TestDiscoverByStableID_ProbePortsDedup(t *testing.T) {
	cam := config.CameraConfig{
		ID:            "cam-1",
		Protocol:      "onvif",
		StableID:      "SN-DEDUP",
		ONVIFEndpoint: "http://192.0.2.10:8080/onvif/device_service",
		SubnetHints:   []string{"192.0.2.0/24"},
	}
	var mu sync.Mutex
	counts := map[string]int{}
	probe := func(ctx context.Context, host string, port int, timeout time.Duration) (*onvif.DiscoveredDevice, error) {
		mu.Lock()
		counts[fmt.Sprintf("%s:%d", host, port)]++
		mu.Unlock()
		return nil, errUnreachable
	}

	eng := NewEngine(Config{MaxParallel: 32, ProbeTimeout: time.Second, MaxDuration: 5 * time.Second, ProbePorts: []int{8080, 80}}, probe)
	_, err := eng.DiscoverByStableID(context.Background(), cam)
	mustEqual(t, err, ErrNotFound, "no device answers")

	mustEqual(t, counts["192.0.2.10:8080"], 1, "8080 probed exactly once (dedup)")
	mustEqual(t, counts["192.0.2.10:80"], 1, "80 probed exactly once")
}
