// Package sntp implements a minimal SNTP server (RFC 4330) so the NVR can act
// as the LAN time source for its cameras (#time-sync path B).
//
// Design: stateless single-packet UDP responder backed by the NVR's own clock.
// Cameras are undemanding clients — they poll on their own schedule (minutes
// to hours), so the request rate is negligible. The server reports stratum 1
// with refid "LOCL" (local clock); cameras accept this from a LAN source.
package sntp

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
)

// Server answers NTP/SNTP client requests on one UDP port.
type Server struct {
	addr string

	mu      sync.Mutex
	conn    *net.UDPConn
	started bool
	queries uint64
}

// NewServer creates an SNTP server bound to addr (e.g. ":123" or
// "127.0.0.1:123"). Binding happens on Start.
func NewServer(addr string) *Server {
	return &Server{addr: addr}
}

// Name implements the app service interface.
func (s *Server) Name() string { return "sntp" }

// Start binds the UDP socket and serves until ctx is cancelled. A bind
// failure (port 123 needs privileges on some hosts; docker bridge needs the
// port mapped) is returned as an error — the caller decides whether to log
// and continue (time-sync path A still works without the SNTP server).
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	pc, err := net.ListenPacket("udp", s.addr)
	if err != nil {
		return fmt.Errorf("sntp: bind %s: %w", s.addr, err)
	}

	s.mu.Lock()
	s.started = true
	s.conn = pc.(*net.UDPConn)
	s.mu.Unlock()

	go func() {
		<-ctx.Done()
		_ = pc.Close()
	}()

	buf := make([]byte, 1024)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if ctx.Err() != nil {
			return nil //nolint:nilerr // socket closed by our own shutdown — not a read failure
		}
		if err != nil {
			return fmt.Errorf("sntp: read: %w", err)
		}
		if n < 48 {
			continue // not an NTP packet
		}
		resp := buildResponse(buf[:n])
		if _, err := pc.WriteTo(resp, addr); err != nil {
			slog.Debug("sntp: write failed", "remote", addr.String(), "error", err)
		}
		s.mu.Lock()
		s.queries++
		s.mu.Unlock()
	}
}

// Stop closes the socket (idempotent; Start's ctx cancellation also closes).
func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn = nil
	}
	s.started = false
	return nil
}

// QueryCount returns the number of NTP requests answered (diagnostics).
func (s *Server) QueryCount() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queries
}

// Running reports whether the server socket is currently bound and serving.
// Consumers (time-sync status payloads) use this to tell the user whether
// "point cameras at the NVR" is actually possible.
func (s *Server) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started && s.conn != nil
}
