package discovery

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"sync"

	mdns "github.com/hashicorp/mdns"
)

// ServiceType is the DNS-SD service type this NVR announces on the LAN.
const ServiceType = "_mibee-nvr._tcp"

// MDNSRegistrar announces the NVR via mDNS/DNS-SD (ServiceType, local
// domain) so LAN clients can discover it without subnet scanning. This is
// the fast path of the discovery layer; the UDP responder (responder.go) is
// the fallback for multicast-restricted networks. It implements the
// pkg/app.Service interface.
type MDNSRegistrar struct {
	instance string
	txt      []string
	port     int

	mu     sync.Mutex
	server *mdns.Server
}

// NewMDNSRegistrar builds a registrar for the given identity. apiPort is the
// HTTP API port (parsed from server.listen via ParseAPIPort); tls reports
// whether the HTTPS listener is enabled (surfaced as a TXT flag so clients
// can pick the scheme; the SRV record always carries the HTTP API port).
func NewMDNSRegistrar(deviceID, deviceName string, apiPort int, tlsEnabled bool) *MDNSRegistrar {
	tlsFlag := "0"
	if tlsEnabled {
		tlsFlag = "1"
	}
	// TXT values are per the discovery contract: clients resolve them into
	// their device records and dispatch on ver. name is URL-encoded so the
	// TXT string survives arbitrary UTF-8 device names.
	txt := []string{
		"ver=1",
		"id=" + deviceID,
		"name=" + url.QueryEscape(deviceName),
		"tls=" + tlsFlag,
		"api=" + strconv.Itoa(apiPort),
	}
	return &MDNSRegistrar{
		instance: deviceName,
		txt:      txt,
		port:     apiPort,
	}
}

// Name implements pkg/app.Service.
func (m *MDNSRegistrar) Name() string { return "mdns" }

// Start registers the service. A failure (multicast unavailable, port 5353
// occupied by a resident avahi, etc.) is logged and returned; callers treat
// discovery as optional and must not fail startup because of it.
func (m *MDNSRegistrar) Start(_ context.Context) error {
	svc, err := mdns.NewMDNSService(m.instance, ServiceType, "", "", m.port, nil, m.txt)
	if err != nil {
		return fmt.Errorf("build %s service: %w", ServiceType, err)
	}
	server, err := mdns.NewServer(&mdns.Config{Zone: svc})
	if err != nil {
		return fmt.Errorf("listen mDNS 5353: %w", err)
	}
	m.mu.Lock()
	m.server = server
	m.mu.Unlock()
	slog.Info("discovery: mdns service registered", "type", ServiceType, "instance", m.instance, "port", m.port)
	return nil
}

// Stop shuts the responder down.
func (m *MDNSRegistrar) Stop() error {
	m.mu.Lock()
	server := m.server
	m.server = nil
	m.mu.Unlock()
	if server != nil {
		return server.Shutdown()
	}
	return nil
}
