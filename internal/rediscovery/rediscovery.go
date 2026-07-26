// Package rediscovery implements IP self-healing for cameras whose address
// changes at runtime (e.g. a Wi-Fi camera roaming across APs that each run their
// own DHCP, so it gets a new IP after an AP reboot).
//
// The camera is re-acquired by a HARDWARE-STABLE identifier — the ONVIF device
// serial number (stored as CameraConfig.StableID) — rather than by IP. Discovery
// uses UNICAST HTTP probing (onvif.ProbeDevice), NOT multicast WS-Discovery, so
// it works across routed subnets where multicast does not travel.
//
// Flow:
//  1. A camera is blacklisted by health auto-remediation (persistent failure).
//  2. Engine.DiscoverByStableID builds a candidate IP list: the last-known host,
//     the NVR's own interface subnets (/24), and the camera's SubnetHints.
//  3. Each candidate is probed concurrently (bounded worker pool, RPi-3B safe).
//  4. The device whose ONVIF serial == StableID is the match; its new endpoint is
//     returned and the caller (CameraManager) updates the config + reconnects.
//
// Currently only ONVIF cameras are supported. Non-ONVIF protocols return
// ErrUnsupportedProtocol; a future extension can add MAC/ARP-based discovery.
package rediscovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
)

var logger = slog.Default().With("component", "rediscovery")

// Sentinel errors.
var (
	// ErrNotFound means no device with the given StableID responded within the
	// candidate set. Not fatal — the camera may simply not have roamed yet.
	ErrNotFound = errors.New("rediscovery: device not found in candidate subnets")
	// ErrUnsupportedProtocol means the camera protocol does not (yet) support
	// IP self-healing. Non-ONVIF cameras hit this.
	ErrUnsupportedProtocol = errors.New("rediscovery: protocol does not support IP self-healing")
	// ErrNoStableID means the camera has no StableID, so it cannot be matched.
	ErrNoStableID = errors.New("rediscovery: camera has no stable_id")
)

// ProbeFunc is the ONVIF unicast probe signature (mirrors onvif.ProbeDevice).
// Exposed as a field so tests can inject a fake without touching the network.
type ProbeFunc func(ctx context.Context, host string, port int, timeout time.Duration) (*onvif.DiscoveredDevice, error)

// ConfirmFunc enriches a discovered device with its ONVIF serial number when the
// probe path did not yield one (e.g. a device that answered a WS-Discovery
// ProbeMatch — where DiscoveredDevice.UUID is the EndpointReference Address, an
// urn:uuid:..., NOT the hardware serial). The default implementation is
// onvif.EnrichDevice, which issues an unauthenticated GetDeviceInformation and
// fills dev.Serial. Mirrors ProbeFunc as an injectable field for testability.
//
// This exists to fix the issue #121 silent-miss: scanFor used to compare
// dev.UUID == want (want = the camera's stable_id = ONVIF serial), but on the
// ProbeMatch path UUID is NOT the serial, so the match always failed for devices
// that answer ProbeMatch. With ConfirmFunc, scanFor re-fetches the real serial
// and matches against dev.Serial as well.
type ConfirmFunc func(ctx context.Context, dev *onvif.DiscoveredDevice)

// Config tunes the scanner. Mirrors the relevant subset of
// config.Health.RediscoveryConfig but as plain values for testability.
type Config struct {
	MaxParallel  int           // concurrent probes (default 16)
	ProbeTimeout time.Duration // per-IP probe timeout (default 2s)
	MaxDuration  time.Duration // bound on a full scan (default 30s)
}

// FromConfig converts a config.RediscoveryConfig into a resolved Config with
// defaults applied. Returns the zero-value Config (all defaults) when given nil.
func FromConfig(cfg config.RediscoveryConfig) Config {
	c := Config{
		MaxParallel:  cfg.MaxParallel,
		ProbeTimeout: parseDur(cfg.ProbeTimeout, 2*time.Second),
		MaxDuration:  parseDur(cfg.MaxDuration, 30*time.Second),
	}
	if c.MaxParallel <= 0 {
		c.MaxParallel = 16
	}
	return c
}

func parseDur(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return def
}

// Engine re-discovers a camera by its stable hardware identifier.
type Engine struct {
	cfg     Config
	probe   ProbeFunc
	confirm ConfirmFunc
}

// NewEngine creates an Engine. If probe is nil, the real onvif.ProbeDevice is
// used. confirm defaults to onvif.EnrichDevice when nil (the production
// behavior); pass a custom ConfirmFunc (or WithConfirm) for tests.
func NewEngine(cfg Config, probe ProbeFunc) *Engine {
	return NewEngineWithConfirm(cfg, probe, onvif.EnrichDevice)
}

// NewEngineWithConfirm is like NewEngine but allows injecting a custom
// ConfirmFunc. Exposed primarily for tests that need to script the second-stage
// serial confirmation independently of the probe. Pass nil for confirm to
// disable confirmation entirely (legacy behavior — not recommended in
// production, kept for the test that pins the ProbeMatch bug).
func NewEngineWithConfirm(cfg Config, probe ProbeFunc, confirm ConfirmFunc) *Engine {
	if probe == nil {
		probe = onvif.ProbeDevice
	}
	return &Engine{cfg: cfg, probe: probe, confirm: confirm}
}

// Result is the outcome of a successful re-discovery.
type Result struct {
	// NewEndpoint is the full ONVIF device_service URL at the camera's new address
	// (e.g. "http://192.168.64.50:80/onvif/device_service").
	NewEndpoint string
	// NewHost is the bare IP (no port) of the matched device.
	NewHost string
	// Port is the ONVIF port (usually 80).
	Port int
}

// DiscoverByStableID scans candidate addresses for a device whose ONVIF serial
// number equals cam.StableID and returns its new endpoint.
//
// Only ONVIF cameras are supported. The candidate set is built from:
//   - the last-known host parsed from cam.ONVIFEndpoint / cam.URL (tried first);
//   - the NVR host's own interface subnets (/24), to catch same-subnet roams;
//   - cam.SubnetHints (user-declared CIDRs, e.g. other AP segments).
func (e *Engine) DiscoverByStableID(ctx context.Context, cam config.CameraConfig) (*Result, error) {
	proto := strings.ToLower(strings.TrimSpace(cam.Protocol))
	if proto != "onvif" {
		return nil, ErrUnsupportedProtocol
	}
	if strings.TrimSpace(cam.StableID) == "" {
		return nil, ErrNoStableID
	}
	want := strings.TrimSpace(cam.StableID)

	port := onvifPortFromEndpoint(cam.ONVIFEndpoint, cam.URL)

	// Build candidate hosts, de-duplicated, preserving priority order so the
	// last-known host is probed first.
	candidates := buildCandidates(cam, port)

	if len(candidates) == 0 {
		return nil, ErrNotFound
	}

	// Bound the whole scan so a wide hint list cannot pin the heal loop.
	scanCtx, cancel := context.WithTimeout(ctx, e.cfg.MaxDuration)
	defer cancel()

	logger.Info("rediscovery scan starting",
		"camera_id", cam.ID, "stable_id", want, "candidates", len(candidates),
		"port", port, "max_parallel", e.cfg.MaxParallel)

	match := e.scanFor(scanCtx, candidates, port, want)
	if match == "" {
		return nil, ErrNotFound
	}
	return &Result{
		NewEndpoint: fmt.Sprintf("http://%s:%d/onvif/device_service", match, port),
		NewHost:     match,
		Port:        port,
	}, nil
}

// scanFor probes every candidate concurrently and returns the first host whose
// reported ONVIF serial equals want, or "" if none match within the context.
func (e *Engine) scanFor(ctx context.Context, hosts []string, port int, want string) string {
	sem := make(chan struct{}, e.cfg.MaxParallel)
	results := make(chan string, len(hosts))
	var wg sync.WaitGroup

	for _, h := range hosts {
		select {
		case <-ctx.Done():
			// Context expired: stop scheduling new probes, wait for in-flight.
			wg.Wait()
			close(results)
			return firstHit(results)
		case sem <- struct{}{}:
		}

		wg.Add(1)
		go func(host string) {
			defer wg.Done()
			defer func() { <-sem }()

			probeCtx, cancel := context.WithTimeout(ctx, e.cfg.ProbeTimeout)
			defer cancel()

			dev, err := e.probe(probeCtx, host, port, e.cfg.ProbeTimeout)
			if err != nil || dev == nil {
				return
			}
			// DiscoveredDevice.UUID holds the serial number on the unicast
			// GetDeviceInformation fallback path (see internal/onvif/discovery.go).
			// But on the WS-Discovery ProbeMatch path, UUID is the device's
			// EndpointReference Address (an urn:uuid:...) and dev.Serial is empty.
			// When that happens, re-fetch the real serial via GetDeviceInformation
			// so the match below can succeed (issue #121: previously this host was
			// silently skipped because UUID != serial).
			if strings.TrimSpace(dev.Serial) == "" && e.confirm != nil {
				// Use a fresh timeout budget independent of probeCtx (whose budget
				// the probe above may have largely consumed), so the confirmation
				// call is not starved.
				confirmCtx, confirmCancel := context.WithTimeout(ctx, e.cfg.ProbeTimeout)
				e.confirm(confirmCtx, dev)
				confirmCancel()
			}
			// Match against EITHER UUID (GetDeviceInformation path) or Serial
			// (ProbeMatch + confirmation path).
			if strings.TrimSpace(dev.UUID) == want || strings.TrimSpace(dev.Serial) == want {
				select {
				case results <- host:
				default:
				}
			}
		}(h)
	}

	wg.Wait()
	close(results)
	return firstHit(results)
}

func firstHit(results <-chan string) string {
	for h := range results {
		return h
	}
	return ""
}

// onvifPortFromEndpoint derives the ONVIF port from the configured endpoint(s).
// Defaults to 80 when not specified. The ONVIF port is usually fixed per device
// regardless of which IP it gets, so the last-known port is reused on the new IP.
func onvifPortFromEndpoint(onvifEndpoint, fallbackURL string) int {
	for _, raw := range []string{onvifEndpoint, fallbackURL} {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if p := portFromURL(raw); p > 0 {
			return p
		}
	}
	return 80
}

func portFromURL(raw string) int {
	u, err := url.Parse(raw)
	if err != nil {
		return 0
	}
	host := u.Hostname()
	if host == "" {
		// Maybe it was just "host:port" without scheme.
		if h, p, err := net.SplitHostPort(raw); err == nil && h != "" {
			if pn, err := strconv.Atoi(p); err == nil {
				return pn
			}
		}
		return 0
	}
	if p := u.Port(); p != "" {
		if pn, err := strconv.Atoi(p); err == nil {
			return pn
		}
	}
	return 0
}
