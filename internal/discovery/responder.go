// Package discovery implements LAN self-announcement for NVR clients that
// cannot rely on subnet scanning or multicast mDNS.
//
// UDP responder (MIBEE-NVR-DISC/v1, #334): listens on 0.0.0.0:49090 and
// answers the exact probe "MIBEE-NVR-DISCv1?" with a unicast JSON payload:
//
//	{"v":1,"id":"<device_id>","name":"<device_name>","api":9090,"tls":false}
//
// This is the fallback for multicast-restricted Wi-Fi (common on consumer
// APs) where mDNS does not travel. Clients treat the reply's source address
// as the NVR host and are expected to cross-check GET /api/health before
// trusting the responder — the payload deliberately carries no more
// information than that already-public endpoint.
package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ProbeV1 is the exact byte payload a client sends to discover NVRs.
const ProbeV1 = "MIBEE-NVR-DISCv1?"

// probeBufSize caps the datagram we are willing to read; anything larger
// than the probe cannot match it and is dropped without a reply.
const probeBufSize = 64

// replyMinInterval throttles replies per source IP so a probe flood cannot
// turn the responder into a broadcast amplification vector.
const replyMinInterval = 100 * time.Millisecond

// replyIPCacheCap bounds the per-IP throttle map; when full it is reset
// rather than grown (a reset only briefly restores the rate limit for
// previously-throttled IPs — acceptable for a discovery convenience path).
const replyIPCacheCap = 1024

// udpResponse is the MIBEE-NVR-DISC/v1 reply payload.
type udpResponse struct {
	V    int    `json:"v"`
	ID   string `json:"id"`
	Name string `json:"name"`
	API  int    `json:"api"`
	TLS  bool   `json:"tls"`
}

// UDPResponder answers MIBEE-NVR-DISC/v1 probes over UDP. It implements the
// pkg/app.Service interface (Name/Start/Stop).
type UDPResponder struct {
	payload []byte
	port    int

	mu   sync.Mutex
	conn *net.UDPConn
	done chan struct{}

	lastReplyMu sync.Mutex
	lastReply   map[string]time.Time
}

// NewUDPResponder pre-marshals the identity payload. apiPort is the HTTP API
// port (parsed from server.listen via ParseAPIPort); tls reports whether the
// HTTPS listener is enabled. port 0 binds an ephemeral port (tests).
func NewUDPResponder(deviceID, deviceName string, apiPort int, tls bool, port int) *UDPResponder {
	payload, err := json.Marshal(udpResponse{
		V:    1,
		ID:   deviceID,
		Name: deviceName,
		API:  apiPort,
		TLS:  tls,
	})
	if err != nil {
		// json.Marshal of this plain struct cannot fail; keep the zero value
		// so Start still listens (and drops probes) rather than panicking.
		slog.Error("discovery: marshal response payload", "error", err)
		payload = nil
	}
	return &UDPResponder{
		payload:   payload,
		port:      port,
		lastReply: make(map[string]time.Time),
	}
}

// Name implements pkg/app.Service.
func (r *UDPResponder) Name() string { return "discovery" }

// Start binds the UDP socket and serves probes until Stop. A bind failure
// (e.g. port already in use) is returned to the caller, who treats the
// responder as optional and logs without failing startup.
func (r *UDPResponder) Start(_ context.Context) error {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: r.port})
	if err != nil {
		return fmt.Errorf("bind udp %d: %w", r.port, err)
	}
	r.mu.Lock()
	r.conn = conn
	done := make(chan struct{})
	r.done = done
	r.mu.Unlock()
	// Pass done explicitly: reading r.done inside serve would race with Stop
	// resetting the field to nil before the goroutine's first statement runs
	// (observed as "panic: close of nil channel" under TestRunFree_SmokeStartStop).
	go r.serve(conn, done)
	slog.Info("discovery: udp responder listening", "addr", conn.LocalAddr().String())
	return nil
}

// Stop closes the socket and waits for the serve goroutine to exit.
func (r *UDPResponder) Stop() error {
	r.mu.Lock()
	conn := r.conn
	done := r.done
	r.conn = nil
	r.done = nil
	r.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	if done != nil {
		<-done
	}
	return nil
}

// Addr returns the bound UDP address (nil before Start or after Stop).
// Primarily for tests binding to ephemeral port 0.
func (r *UDPResponder) Addr() *net.UDPAddr {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conn == nil {
		return nil
	}
	return r.conn.LocalAddr().(*net.UDPAddr)
}

func (r *UDPResponder) serve(conn *net.UDPConn, done chan struct{}) {
	defer close(done)
	buf := make([]byte, probeBufSize)
	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			return // socket closed by Stop, or unrecoverable read error
		}
		if string(buf[:n]) != ProbeV1 {
			continue
		}
		if !r.allowReply(src.IP.String()) {
			continue
		}
		if _, err := conn.WriteToUDP(r.payload, src); err != nil {
			// Commonly ECONNREFUSED after the probing socket closed; keep serving.
			continue
		}
	}
}

// allowReply enforces the per-IP throttle.
func (r *UDPResponder) allowReply(ip string) bool {
	now := time.Now()
	r.lastReplyMu.Lock()
	defer r.lastReplyMu.Unlock()
	if len(r.lastReply) >= replyIPCacheCap {
		r.lastReply = make(map[string]time.Time)
	}
	if t, ok := r.lastReply[ip]; ok && now.Sub(t) < replyMinInterval {
		return false
	}
	r.lastReply[ip] = now
	return true
}

// ParseAPIPort extracts the numeric port from a server.listen value. Accepts
// ":9090", "0.0.0.0:9090", "192.0.2.1:9090", and bare "9090". Returns the
// default 9090 for anything unparseable — the reply is advisory, and clients
// re-verify against /api/health anyway.
func ParseAPIPort(listen string) int {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return 9090
	}
	if !strings.Contains(listen, ":") {
		if p, err := strconv.Atoi(listen); err == nil && p > 0 && p < 65536 {
			return p
		}
		return 9090
	}
	_, portStr, err := net.SplitHostPort(listen)
	if err != nil {
		return 9090
	}
	p, err := strconv.Atoi(portStr)
	if err != nil || p <= 0 || p > 65535 {
		return 9090
	}
	return p
}
