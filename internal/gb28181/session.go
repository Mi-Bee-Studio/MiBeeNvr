package gb28181

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181/manscdp"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// AUWriter receives access units (full NALU lists) from the demuxer.
type AUWriter interface {
	WriteNALU(au [][]byte, ptsTicks int64, isIDR bool)
}

// Stopper is an optional interface implemented by AUWriter sinks that need
// explicit teardown (e.g., file recorders, network connections).
type Stopper interface {
	Stop() error
}

// session holds a live GB28181 media session (INVITE).
type session struct {
	channelID string
	receiver  *Receiver
	hub       *model.StreamHub
	port      uint16
	conn      net.Conn
	cancel    context.CancelFunc
	sink      AUWriter // For playback sessions, stores the sink for Stop() cleanup
}

// SessionManager orchestrates INVITE/BYE media sessions.
// Each session maps a MiBee camera (represented by a Channel) to an RTP receiver.
// Thread-safe: sessions map under mutex, channel state under atomics.
type SessionManager struct {
	portManager *PortManager
	sessions    map[string]*session
	mu          sync.Mutex
	serverID    string // GB28181 server ID (20-digit ASCII)
}

// NewSessionManager creates a SessionManager.
func NewSessionManager(pm *PortManager, serverID string) *SessionManager {
	return &SessionManager{
		portManager: pm,
		sessions:    make(map[string]*session),
		serverID:    serverID,
	}
}

// Invite allocates a port, creates an SDP answer, starts a Receiver, and transitions
// the channel to inviting state. The caller (SIP server) sends the SDP answer to the device.
func (sm *SessionManager) Invite(channel *Channel, serverIP string, deviceAddr string, sdpOffer []byte) ([]byte, error) {
	if channel == nil {
		return nil, fmt.Errorf("gb28181: channel is nil")
	}

	// Allocate RTP port from pool
	port, err := sm.portManager.Get()
	if err != nil {
		return nil, fmt.Errorf("gb28181: failed to allocate port: %w", err)
	}

	// Generate SSRC per GB28181 convention: 10-digit decimal (0=live, 1=playback
	// + last 8 digits of channel ID).
	ssrc := manscdp.SSRC(false, channel.ID)

	// Build SDP answer (GB28181 minimal format)
	sdpAnswer := []byte(fmt.Sprintf(
		"v=0\r\no=- 0 0 IN IP4 %s\r\ns=Play\r\nc=IN IP4 %s\r\nt=0 0\r\nm=video %d RTP/AVP 96\r\na=recvonly\r\na=rtpmap:96 PS/90000\r\ny=%s\r\n",
		serverIP, serverIP, port, ssrc,
	))

	// Create StreamHub for this session
	hub := model.NewStreamHub()
	hub.SetCameraID(channel.ID)

	// Create receiver
	receiver := NewReceiver(channel.ID, hub, sm.portManager)

	// Create UDP listener on allocated port
	addr, err := netip.ParseAddrPort(fmt.Sprintf("%s:%d", serverIP, port))
	if err != nil {
		sm.portManager.Recycle(port)
		return nil, fmt.Errorf("gb28181: failed to parse addr %s:%d: %w", serverIP, port, err)
	}

	conn, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(addr))
	if err != nil {
		sm.portManager.Recycle(port)
		return nil, fmt.Errorf("gb28181: failed to bind UDP port %d: %w", port, err)
	}

	// Set NALU callback to broadcast to hub
	receiver.NALUCallback = func(nalu []byte, ptsTicks int64, isIDR bool) {
		hub.Broadcast(ptsTicks, [][]byte{nalu}, isIDR)
	}

	// Start receiver in context
	ctx, cancel := context.WithCancel(context.Background())
	if err := receiver.Start(ctx, conn); err != nil {
		conn.Close()
		sm.portManager.Recycle(port)
		cancel()
		return nil, fmt.Errorf("gb28181: failed to start receiver: %w", err)
	}

	// Store session
	sm.mu.Lock()
	sess := &session{
		channelID: channel.ID,
		receiver:  receiver,
		hub:       hub,
		port:      port,
		conn:      conn,
		cancel:    cancel,
	}
	sm.sessions[channel.ID] = sess
	sm.mu.Unlock()

	// Transition channel state: idle -> inviting
	channel.Status.CompareAndSwap(ChannelIdle, ChannelInviting)

	slog.Info("gb28181: session created", "channel_id", channel.ID, "device_id", channel.DeviceID,
		"port", port, "ssrc", ssrc, "remote", deviceAddr)

	return sdpAnswer, nil
}

// Bye stops a session, recycles its port, and transitions the channel back to idle.
// A no-op if the session does not exist.
func (sm *SessionManager) Bye(channelID string) error {
	sm.mu.Lock()
	sess, ok := sm.sessions[channelID]
	if !ok {
		sm.mu.Unlock()
		return nil // No-op if session doesn't exist
	}
	delete(sm.sessions, channelID)
	sm.mu.Unlock()

	// Stop receiver
	if err := sess.receiver.Stop(); err != nil {
		slog.Warn("gb28181: receiver stop error", "channel_id", channelID, "error", err)
	}

	// Close connection
	if sess.conn != nil {
		_ = sess.conn.Close()
	}

	// Recycle port
	sm.portManager.Recycle(sess.port)

	// Cancel context
	if sess.cancel != nil {
		sess.cancel()
	}

	// Call Stop() on the sink if it implements Stopper (for playback sessions)
	if sess.sink != nil {
		if stopper, ok := sess.sink.(Stopper); ok {
			if err := stopper.Stop(); err != nil {
				slog.Warn("gb28181: sink stop error", "channel_id", channelID, "error", err)
			}
		}
	}

	slog.Info("gb28181: session stopped", "channel_id", channelID, "port", sess.port)

	return nil
}

// InvitePlayback creates a playback session: builds SDP with s=Playback,
// t=<start> <end>, SSRC with leading digit 1, allocates port, starts receiver
// with AUCallback feeding sink, and returns the SDP for the caller to send
// as a UAC INVITE.
func (sm *SessionManager) InvitePlayback(channel *Channel, serverIP string, start, end time.Time, sink AUWriter) ([]byte, error) {
	if channel == nil {
		return nil, fmt.Errorf("gb28181: channel is nil")
	}
	if sink == nil {
		return nil, fmt.Errorf("gb28181: sink is nil")
	}

	// Allocate RTP port from pool
	port, err := sm.portManager.Get()
	if err != nil {
		return nil, fmt.Errorf("gb28181: failed to allocate port: %w", err)
	}

	// Generate SSRC per GB28181 convention: 10-digit decimal (playback uses digit 1).
	ssrc := manscdp.SSRC(true, channel.ID)

	// Convert times to NTP timestamps (seconds since 1900-01-01)
	// NTP timestamp = Unix timestamp + 2208988800
	startNTP := start.Unix() + 2208988800
	endNTP := end.Unix() + 2208988800

	// Build SDP answer for playback (GB28181 format with t= line)
	sdpAnswer := []byte(fmt.Sprintf(
		"v=0\r\no=- 0 0 IN IP4 %s\r\ns=Playback\r\nc=IN IP4 %s\r\nt=%d %d\r\nm=video %d RTP/AVP 96\r\na=recvonly\r\na=rtpmap:96 PS/90000\r\ny=%s\r\n",
		serverIP, serverIP, startNTP, endNTP, port, ssrc,
	))

	// Create StreamHub for this session
	hub := model.NewStreamHub()
	hub.SetCameraID(channel.ID)

	// Create receiver
	receiver := NewReceiver(channel.ID, hub, sm.portManager)

	// Create UDP listener on allocated port
	addr, err := netip.ParseAddrPort(fmt.Sprintf("%s:%d", serverIP, port))
	if err != nil {
		sm.portManager.Recycle(port)
		return nil, fmt.Errorf("gb28181: failed to parse addr %s:%d: %w", serverIP, port, err)
	}

	conn, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(addr))
	if err != nil {
		sm.portManager.Recycle(port)
		return nil, fmt.Errorf("gb28181: failed to bind UDP port %d: %w", port, err)
	}

	// Set NALU callback to feed the sink (instead of broadcasting to hub)
	receiver.NALUCallback = func(nalu []byte, ptsTicks int64, isIDR bool) {
		sink.WriteNALU([][]byte{nalu}, ptsTicks, isIDR)
	}

	// Start receiver in context
	ctx, cancel := context.WithCancel(context.Background())
	if err := receiver.Start(ctx, conn); err != nil {
		conn.Close()
		sm.portManager.Recycle(port)
		cancel()
		return nil, fmt.Errorf("gb28181: failed to start receiver: %w", err)
	}

	// Store session
	sm.mu.Lock()
	sess := &session{
		channelID: channel.ID,
		receiver:  receiver,
		hub:       hub,
		port:      port,
		conn:      conn,
		cancel:    cancel,
		sink:      sink,
	}
	sm.sessions[channel.ID] = sess
	sm.mu.Unlock()

	slog.Info("gb28181: playback session created", "channel_id", channel.ID, "device_id", channel.DeviceID,
		"port", port, "ssrc", ssrc, "start", start, "end", end)

	return sdpAnswer, nil
}

// GetReceiver returns the receiver for the given channelID, or nil if no active session.
func (sm *SessionManager) GetReceiver(channelID string) *Receiver {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sess, ok := sm.sessions[channelID]; ok {
		return sess.receiver
	}
	return nil
}

// GetHub returns the StreamHub for the given channelID, or nil if no active session.
func (sm *SessionManager) GetHub(channelID string) *model.StreamHub {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sess, ok := sm.sessions[channelID]; ok {
		return sess.hub
	}
	return nil
}

// StopAll stops all active sessions and recycles all ports.
// Called during server shutdown.
func (sm *SessionManager) StopAll() {
	sm.mu.Lock()
	sessions := make(map[string]*session, len(sm.sessions))
	for k, v := range sm.sessions {
		sessions[k] = v
	}
	sm.sessions = make(map[string]*session)
	sm.mu.Unlock()

	for channelID, sess := range sessions {
		// Stop receiver
		if err := sess.receiver.Stop(); err != nil {
			slog.Warn("gb28181: receiver stop error during shutdown", "channel_id", channelID, "error", err)
		}

		// Close connection
		if sess.conn != nil {
			_ = sess.conn.Close()
		}

		// Recycle port
		sm.portManager.Recycle(sess.port)

		// Cancel context
		if sess.cancel != nil {
			sess.cancel()
		}
	}
}

// SessionCount returns the number of active sessions.
func (sm *SessionManager) SessionCount() int {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return len(sm.sessions)
}

// MarkPlaying transitions a channel from inviting to playing state.
// Called by the SIP server after receiving ACK from the device.
func (sm *SessionManager) MarkPlaying(channelID string) error {
	sm.mu.Lock()
	sess, ok := sm.sessions[channelID]
	sm.mu.Unlock()

	if !ok {
		return fmt.Errorf("gb28181: no active session for channel %q", channelID)
	}

	sess.receiver.SetTCPMode(TCPModeAuto) // Auto-detect framing

	// Note: We don't have direct access to the Channel object here.
	// The caller (SIP server) must transition the channel state.

	slog.Info("gb28181: session marked as playing", "channel_id", channelID)

	return nil
}
