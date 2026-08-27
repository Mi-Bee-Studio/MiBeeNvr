package onvif

import (
	"context"
	"encoding/xml"
	"net"
	"strings"
	"sync"
	"time"
)

// WS-Discovery multicast endpoint (RFC-standard, shared with discovery.go's
// fork-backed Discover()). Kept here so the listener is self-contained.
const (
	wsdMulticastGroup = "239.255.255.250"
	wsdPort           = "3702"
)

// helloEnvelope parses a WS-Discovery Hello SOAP message. Hello is structurally
// identical to a single ProbeMatch (EndpointReference/Types/Scopes/XAddrs/
// MetadataVersion) but sits directly under Body (no ProbeMatches container).
// Local-name XML matching (Go ignores namespace prefixes by default).
type helloEnvelope struct {
	Body struct {
		Hello *wsdEntry `xml:"Hello"`
		// ProbeMatches: also accept unicast ProbeMatches responses that arrive on
		// the shared multicast socket (other clients' Probe traffic). Treats the
		// first ProbeMatch as a discovery source — a free extra discovery path.
		ProbeMatches struct {
			ProbeMatch []wsdEntry `xml:"ProbeMatch"`
		} `xml:"ProbeMatches"`
	} `xml:"Body"`
}

// wsdEntry is the shared shape of a Hello element and a ProbeMatch element.
type wsdEntry struct {
	EndpointRef struct {
		Address string `xml:"Address"`
	} `xml:"EndpointReference"`
	Types           string `xml:"Types"`
	Scopes          string `xml:"Scopes"`
	XAddrs          string `xml:"XAddrs"`
	MetadataVersion int    `xml:"MetadataVersion"`
}

// extractScopeInfo pulls the ONVIF name/hardware tokens out of a WS-Discovery
// Scopes list (whitespace-separated onvif://www.onvif.org/{name|hardware}/X).
// Shared by Hello and ProbeMatch parsing.
func extractScopeInfo(scopesStr string) (name, hardware string) {
	for _, scope := range strings.Fields(scopesStr) {
		if strings.Contains(scope, "/name/") {
			parts := strings.Split(scope, "/")
			name = parts[len(parts)-1]
		}
		if strings.Contains(scope, "/hardware/") {
			parts := strings.Split(scope, "/")
			hardware = parts[len(parts)-1]
		}
	}
	return name, hardware
}

// entryToDevice converts a parsed Hello/ProbeMatch entry into a DiscoveredDevice.
func entryToDevice(e wsdEntry) *DiscoveredDevice {
	scopes := strings.Fields(e.Scopes)
	xaddrs := strings.Fields(e.XAddrs)
	name, hardware := extractScopeInfo(e.Scopes)
	endpoint := ""
	if len(xaddrs) > 0 {
		endpoint = xaddrs[0]
	}
	return &DiscoveredDevice{
		UUID:     e.EndpointRef.Address,
		Name:     name,
		XAddrs:   xaddrs,
		Scopes:   scopes,
		Hardware: hardware,
		Endpoint: endpoint,
	}
}

// entryIfONVIF applies the shared ONVIF-ness gate (#266/#554) to a parsed
// Hello/ProbeMatch entry: generic WS-Discovery responders (Windows machines,
// NAS boxes, printers) announce Hellos with Types like "wsdiscovery:Device"
// and non-ONVIF scopes — without this gate they auto-enroll as camera shells
// that can never connect (the mickeybeessd/mickeybeehome zombies, #554).
// Matching either signal (NetworkVideoTransmitter in Types, or an
// onvif://www.onvif.org/ scope prefix) keeps the device, mirroring the active
// Discover path's filter.
func entryIfONVIF(e wsdEntry) *DiscoveredDevice {
	if !isONVIFSignal(e.Types, strings.Fields(e.Scopes)) {
		logger.Debug("dropping non-ONVIF WS-Discovery message",
			"types", e.Types, "xaddrs", e.XAddrs)
		return nil
	}
	return entryToDevice(e)
}

// parseWSDMessage parses a UDP datagram received on the WS-Discovery multicast
// socket and returns the device it advertises, if any. It handles Hello (device
// announcing itself on power-on) and ProbeMatches (a device answering someone
// else's Probe); entries that carry no ONVIF signal are dropped at this
// boundary (#554). Returns nil for Bye, unmatched message types, non-ONVIF
// responders, and parse errors — the caller treats nil as "ignore".
func parseWSDMessage(data []byte) *DiscoveredDevice {
	var env helloEnvelope
	if err := xml.Unmarshal(data, &env); err != nil {
		return nil
	}
	if env.Body.Hello != nil {
		return entryIfONVIF(*env.Body.Hello)
	}
	if len(env.Body.ProbeMatches.ProbeMatch) > 0 {
		return entryIfONVIF(env.Body.ProbeMatches.ProbeMatch[0])
	}
	return nil
}

// HelloListener is a resident UDP 3702 listener that reacts to WS-Discovery
// Hello/ProbeMatches messages the instant they arrive — giving the NVR the
// Hikvision-style "zero-latency" plug-and-play discovery experience (a camera
// is seen the moment it powers on and announces itself, with no polling delay).
//
// It binds the standard WS-Discovery multicast group and shares port 3702 with
// the on-demand Discover() path: Go's net.ListenMulticastUDP sets SO_REUSEADDR,
// and Linux delivers a copy of each multicast datagram to every socket bound to
// the group, so the listener and a concurrent user-triggered Discover() both
// receive incoming Hello/ProbeMatches without contention.
//
// All operations are best-effort and non-blocking: parse errors and handler
// panics are logged and swallowed so one malformed packet never kills the
// listener. The listener runs until Stop() or the Start context is cancelled.
type HelloListener struct {
	iface   *net.Interface // nil = default multicast interface
	handler func(DiscoveredDevice)

	mu      sync.Mutex
	conn    *net.UDPConn
	cancel  context.CancelFunc
	stopped bool
}

// NewHelloListener constructs a resident WS-Discovery listener. ifaceName of ""
// means the kernel's default multicast interface (recommended for single-NIC
// hosts). handler is invoked for every Hello/ProbeMatches seen; it runs on the
// listener goroutine so it must return promptly (hand off to a queue/goroutine
// for slow work like ONVIF enrichment or DB writes).
func NewHelloListener(ifaceName string, handler func(DiscoveredDevice)) (*HelloListener, error) {
	l := &HelloListener{handler: handler}
	if ifaceName != "" {
		iface, err := net.InterfaceByName(ifaceName)
		if err != nil {
			return nil, err
		}
		l.iface = iface
	}
	return l, nil
}

// Start begins listening on UDP 3702 / 239.255.255.250. Blocks until Stop() or
// ctx is cancelled. Returns an error only if the multicast socket cannot be
// created (e.g. port in use without SO_REUSEADDR, or no multicast-capable
// interface). Safe to call once; subsequent calls are no-ops.
func (l *HelloListener) Start(ctx context.Context) error {
	l.mu.Lock()
	if l.stopped {
		l.mu.Unlock()
		return nil
	}
	addr, err := net.ResolveUDPAddr("udp", wsdMulticastGroup+":"+wsdPort)
	if err != nil {
		l.mu.Unlock()
		return err
	}
	// net.ListenMulticastUDP sets SO_REUSEADDR internally, allowing this resident
	// listener to coexist with the on-demand Discover() path on the same port.
	conn, err := net.ListenMulticastUDP("udp", l.iface, addr)
	if err != nil {
		l.mu.Unlock()
		return err
	}
	l.conn = conn
	ctx, cancel := context.WithCancel(ctx)
	l.cancel = cancel
	l.mu.Unlock()

	logger.Info("WS-Discovery Hello listener started", "group", wsdMulticastGroup, "port", wsdPort)

	go l.recvLoop(ctx)
	return nil
}

// recvLoop reads datagrams until the context is cancelled or Stop is called.
// Each datagram is parsed as a WS-Discovery message; valid Hello/ProbeMatches
// are handed to the handler. Errors are non-fatal: a read deadline cycles the
// loop so context cancellation is observed promptly.
func (l *HelloListener) recvLoop(ctx context.Context) {
	// Buffered to absorb a burst of Hello messages (e.g. several cameras power
	// on at once after a site-wide power restore). On a Raspberry Pi 3B the
	// 256KB ceiling keeps memory bounded.
	const bufSize = 256 * 1024
	buf := make([]byte, bufSize)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		// Cycle a read deadline so ctx cancellation is observable even during
		// quiet periods (a blocking ReadFromUDP without a deadline would hang
		// until the next packet, delaying shutdown).
		_ = l.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, _, err := l.conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Deadline exceeded or transient error — loop and re-check ctx.
			if isTimeout(err) {
				continue
			}
			// A fatal socket error (e.g. closed by Stop) — exit the loop.
			return
		}
		dev := parseWSDMessage(buf[:n])
		if dev == nil || dev.Endpoint == "" {
			continue
		}
		l.dispatch(*dev)
	}
}

// dispatch invokes the handler with panic recovery so a handler bug cannot take
// down the listener goroutine.
func (l *HelloListener) dispatch(dev DiscoveredDevice) {
	defer func() {
		if r := recover(); r != nil {
			logger.Warn("Hello listener handler panic recovered", "endpoint", dev.Endpoint, "panic", r)
		}
	}()
	l.handler(dev)
}

// Stop tears down the listener. Safe to call multiple times; after Stop the
// listener cannot be restarted (construct a new one).
func (l *HelloListener) Stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return
	}
	l.stopped = true
	if l.cancel != nil {
		l.cancel()
	}
	if l.conn != nil {
		_ = l.conn.Close()
	}
}

// isTimeout reports whether err is a read deadline exceeded / timeout error.
func isTimeout(err error) bool {
	type timeout interface{ Timeout() bool }
	if t, ok := err.(timeout); ok && t.Timeout() {
		return true
	}
	return false
}

// EnrichDevice fills in Manufacturer/Model/Firmware/Serial for a single
// discovered device by issuing an unauthenticated GetDeviceInformation SOAP
// request to its endpoint. Best-effort: on any failure the device is returned
// unchanged (caller proceeds with whatever the Hello/Probe advertised).
//
// Exported so the auto-discover engine can enrich a freshly-seen device before
// persisting it — the Serial in particular is used as the stable_id so IP
// self-healing is armed immediately, without waiting for the recorder's async
// ensureStableID.
func EnrichDevice(ctx context.Context, dev *DiscoveredDevice) {
	endpoint := dev.Endpoint
	if endpoint == "" && len(dev.XAddrs) > 0 {
		endpoint = dev.XAddrs[0]
	}
	if endpoint == "" {
		return
	}
	info, err := fetchDeviceInformation(ctx, endpoint)
	if err != nil || info == nil {
		return
	}
	if dev.Name == "" && info.Manufacturer != "" {
		dev.Name = info.Manufacturer
	}
	if dev.Manufacturer == "" {
		dev.Manufacturer = info.Manufacturer
	}
	if dev.Model == "" {
		dev.Model = info.Model
	}
	if dev.Firmware == "" {
		dev.Firmware = info.FirmwareVersion
	}
	if dev.Serial == "" {
		dev.Serial = info.SerialNumber
	}
}
