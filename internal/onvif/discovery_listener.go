package onvif

import (
	"context"
	"net"

	"github.com/mickeyzzc/onvif-go/v2/discovery"
)

// Resident WS-Discovery passive listening ("plug in and it appears
// instantly"): the socket loop, datagram parsing and the ONVIF signal gate
// live in onvif-go's discovery package (onvif-go#128, ported from this
// listener's original in-tree implementation and field-hardened against the
// same dialects — vendor prefix variants, Windows/WSD host Hellos, Bye and
// garbage). What stays here is the NVR adapter: mapping the library's Device
// onto DiscoveredDevice and the empty-endpoint skip (a Hello with no XAddrs
// has nothing to connect to).

// ErrListenerStopped is returned by Start after the listener was stopped —
// a stopped listener cannot be restarted; build a new one. The library's
// sentinel, re-exported so callers keep one errors.Is target.
var ErrListenerStopped = discovery.ErrListenerStopped

// HelloListener is a resident UDP 3702 listener that reacts to WS-Discovery
// Hello/ProbeMatches messages the instant they arrive. It shares port 3702
// with the on-demand Discover() path (both sockets join the multicast group;
// the kernel delivers a copy to each). All operations are best-effort and
// non-blocking: parse errors and handler panics are contained inside the
// library listener. The listener runs until Stop() or the Start context is
// cancelled; a stopped listener cannot be restarted.
type HelloListener struct {
	lib *discovery.Listener
}

// NewHelloListener constructs a resident WS-Discovery listener. ifaceName of
// "" means the kernel's default multicast interface (recommended for
// single-NIC hosts). handler is invoked for every ONVIF Hello/ProbeMatches
// seen; it runs on the listener goroutine so it must return promptly (hand
// off to a queue/goroutine for slow work like ONVIF enrichment or DB
// writes).
func NewHelloListener(ifaceName string, handler func(DiscoveredDevice)) (*HelloListener, error) {
	// Eager interface validation preserves this constructor's contract (the
	// library defers the lookup to Start; a misconfigured name should fail
	// at construction, where the auto-discover engine logs it once).
	if ifaceName != "" {
		if _, err := net.InterfaceByName(ifaceName); err != nil {
			return nil, err
		}
	}
	lib, err := discovery.NewListener(ifaceName, func(d *discovery.Device) {
		dev := MapDiscoveredDevice(d)
		if dev.Endpoint == "" {
			return
		}
		handler(dev)
	})
	if err != nil {
		return nil, err
	}
	return &HelloListener{lib: lib}, nil
}

// Start begins listening on UDP 3702 / 239.255.255.250 (ONVIF signal gate
// on). It BLOCKS until Stop() or ctx is cancelled — call it on its own
// goroutine. Returns an error if the multicast socket cannot be created
// (or ErrListenerStopped when called after Stop). Safe to call once.
func (l *HelloListener) Start(ctx context.Context) error {
	return l.lib.Start(ctx)
}

// Stop tears down the listener. Idempotent; after Stop the listener cannot
// be restarted (construct a new one).
func (l *HelloListener) Stop() {
	l.lib.Stop()
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
