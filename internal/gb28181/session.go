package gb28181

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
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
	deviceID  string
	channel   *Channel
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
	ssrcSeq     atomic.Int64

	// byeSender (optional, set by the SIP server) transmits a SIP BYE to the
	// device before local teardown when a session is stopped locally
	// (user-initiated stop, INVITE failure). Nil in tests.
	byeSender func(channelID string) error

	// firstRTPHook (optional, set by the SIP server) fires when a session's
	// receiver gets its first RTP packet — evidence the dialog works even
	// without a transaction-matched 200 OK.
	firstRTPHook func(channelID string)
}

// NewSessionManager creates a SessionManager.
func NewSessionManager(pm *PortManager, serverID string) *SessionManager {
	return &SessionManager{
		portManager: pm,
		sessions:    make(map[string]*session),
		serverID:    serverID,
	}
}

// SetByeSender wires the SIP BYE transmitter used when a session is torn
// down locally. Without it, Bye only closes the local receiver.
func (sm *SessionManager) SetByeSender(sender func(channelID string) error) {
	sm.mu.Lock()
	sm.byeSender = sender
	sm.mu.Unlock()
}

// SetFirstRTPHook wires the once-per-session first-RTP callback.
func (sm *SessionManager) SetFirstRTPHook(hook func(channelID string)) {
	sm.mu.Lock()
	sm.firstRTPHook = hook
	sm.mu.Unlock()
}

// Invite allocates a port, creates an SDP answer, starts a Receiver, and
// transitions the channel to inviting state. The caller (SIP server) sends
// the SDP answer to the device. An existing session for the channel is torn
// down first so re-INVITEs never leak the old port, receiver goroutine, or
// UDP socket.
func (sm *SessionManager) Invite(channel *Channel, serverIP string, deviceAddr string, sdpOffer []byte, onAU func(au [][]byte, ptsTicks int64, isIDR bool)) ([]byte, error) {
	if channel == nil {
		return nil, fmt.Errorf("gb28181: channel is nil")
	}

	// Idempotency guard: recycle any prior session for this channel before
	// allocating a new one (its port, socket, and receiver would otherwise
	// leak — auto-INVITE fires on every device re-REGISTER).
	sm.mu.Lock()
	if old, ok := sm.sessions[channel.ID]; ok {
		sm.mu.Unlock()
		sm.teardown(old, false)
	} else {
		sm.mu.Unlock()
	}

	// Allocate RTP port from pool
	port, err := sm.portManager.Get()
	if err != nil {
		return nil, fmt.Errorf("gb28181: failed to allocate port: %w", err)
	}

	// Generate SSRC per GB/T 28181-2016 Annex C.2.4: 10-digit decimal
	// (0=live, 1=playback + digits 4-8 of the platform ID + sequence).
	ssrc := manscdp.SSRC(false, sm.serverID, int(sm.ssrcSeq.Add(1)))

	// Build SDP answer (GB28181 minimal format)
	sdpAnswer := []byte(fmt.Sprintf(
		"v=0\r\no=- 0 0 IN IP4 %s\r\ns=Play\r\nc=IN IP4 %s\r\nt=0 0\r\nm=video %d RTP/AVP 96\r\na=recvonly\r\na=rtpmap:96 PS/90000\r\ny=%s\r\n",
		serverIP, serverIP, port, ssrc,
	))

	// Use the provided AU callback (from the recorder) instead of creating
	// an orphaned hub. When onAU is nil (tests), fall back to a local hub.
	var hub *model.StreamHub
	if onAU == nil {
		hub = model.NewStreamHub()
		hub.SetCameraID(channel.ID)
	}
	receiver := NewReceiver(channel.ID, hub, sm.portManager)
	sm.mu.Lock()
	firstRTP := sm.firstRTPHook
	sm.mu.Unlock()
	channelID := channel.ID
	if firstRTP != nil {
		receiver.OnFirstRTP = func() { firstRTP(channelID) }
	}
	receiver.AUCallback = func(au [][]byte, ptsTicks int64, isIDR bool) {
		if onAU != nil {
			onAU(au, ptsTicks, isIDR)
		} else if hub != nil {
			hub.Broadcast(ptsTicks, au, isIDR)
		}
	}

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

	// Start receiver in context
	ctx, cancel := context.WithCancel(context.Background())
	if err := receiver.Start(ctx, conn); err != nil {
		conn.Close()
		sm.portManager.Recycle(port)
		cancel()
		return nil, fmt.Errorf("gb28181: failed to start receiver: %w", err)
	}

	// Store session
	sess := &session{
		channelID: channel.ID,
		deviceID:  channel.DeviceID,
		channel:   channel,
		receiver:  receiver,
		hub:       hub,
		port:      port,
		conn:      conn,
		cancel:    cancel,
	}
	sm.mu.Lock()
	sm.sessions[channel.ID] = sess
	sm.mu.Unlock()

	// Transition channel state: idle -> inviting
	channel.Status.CompareAndSwap(ChannelIdle, ChannelInviting)

	slog.Info("gb28181: session created", "channel_id", channel.ID, "device_id", channel.DeviceID,
		"port", port, "ssrc", ssrc, "remote", deviceAddr)

	return sdpAnswer, nil
}

// Bye stops a session, notifies the device via SIP BYE (when a bye sender is
// wired), recycles its port, and transitions the channel back to idle.
// A no-op if the session does not exist.
func (sm *SessionManager) Bye(channelID string) error {
	sm.mu.Lock()
	sess, ok := sm.sessions[channelID]
	if !ok {
		sm.mu.Unlock()
		return nil // No-op if session doesn't exist
	}
	delete(sm.sessions, channelID)
	sender := sm.byeSender
	sm.mu.Unlock()

	// Notify the device first (best-effort) so it stops streaming before the
	// port is recycled — otherwise stale RTP poisons the next session that
	// reuses the recycled port.
	if sender != nil {
		if err := sender(channelID); err != nil {
			slog.Warn("gb28181: SIP BYE send failed", "channel_id", channelID, "error", err)
		}
	}

	sm.teardown(sess, true)
	return nil
}

// ByeDevice stops every session belonging to deviceID (device went offline
// or unregistered). The channel status of each session is reset to idle.
func (sm *SessionManager) ByeDevice(deviceID string) {
	sm.mu.Lock()
	var doomed []*session
	for id, sess := range sm.sessions {
		if sess.deviceID == deviceID {
			doomed = append(doomed, sess)
			delete(sm.sessions, id)
		}
	}
	sm.mu.Unlock()

	for _, sess := range doomed {
		sm.teardown(sess, true)
	}
}

// ChannelIDs returns the channel IDs with an active session (diagnostics).
func (sm *SessionManager) ChannelIDs() []string {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	out := make([]string, 0, len(sm.sessions))
	for id := range sm.sessions {
		out = append(out, id)
	}
	return out
}

// teardown releases a session's resources: receiver goroutine, UDP socket,
// port, context, sink, and channel status. notifyStatus controls whether the
// channel is transitioned back to idle (skipped for pre-replace teardown,
// which immediately re-INVITEs).
func (sm *SessionManager) teardown(sess *session, resetStatus bool) {
	// Stop receiver
	if err := sess.receiver.Stop(); err != nil {
		slog.Warn("gb28181: receiver stop error", "channel_id", sess.channelID, "error", err)
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
				slog.Warn("gb28181: sink stop error", "channel_id", sess.channelID, "error", err)
			}
		}
	}

	if resetStatus && sess.channel != nil {
		sess.channel.Status.Store(ChannelIdle)
	}

	slog.Info("gb28181: session stopped", "channel_id", sess.channelID, "port", sess.port)
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

	// Generate SSRC per GB/T 28181 Annex C.2.4: playback uses leading digit 1.
	ssrc := manscdp.SSRC(true, sm.serverID, int(sm.ssrcSeq.Add(1)))

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

	// Feed complete AUs to the sink (AU grouping preserved for muxing)
	receiver.AUCallback = func(au [][]byte, ptsTicks int64, isIDR bool) {
		sink.WriteNALU(au, ptsTicks, isIDR)
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
	sess := &session{
		channelID: channel.ID,
		deviceID:  channel.DeviceID,
		channel:   channel,
		receiver:  receiver,
		hub:       hub,
		port:      port,
		conn:      conn,
		cancel:    cancel,
		sink:      sink,
	}
	sm.mu.Lock()
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
// Called during server shutdown. No SIP BYEs are sent — the devices detect
// the dead dialog via re-REGISTER/keepalive timeouts.
func (sm *SessionManager) StopAll() {
	sm.mu.Lock()
	sessions := make(map[string]*session, len(sm.sessions))
	for k, v := range sm.sessions {
		sessions[k] = v
	}
	sm.sessions = make(map[string]*session)
	sm.mu.Unlock()

	for channelID, sess := range sessions {
		_ = channelID
		sm.teardown(sess, true)
	}
}

// SessionCount returns the number of active sessions.
func (sm *SessionManager) SessionCount() int {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return len(sm.sessions)
}

// MarkPlaying transitions a channel from inviting to playing state.
// Called by the SIP server after the device answers the INVITE with 200 OK
// (and the ACK has been sent).
func (sm *SessionManager) MarkPlaying(channelID string) error {
	sm.mu.Lock()
	sess, ok := sm.sessions[channelID]
	sm.mu.Unlock()

	if !ok {
		return fmt.Errorf("gb28181: no active session for channel %q", channelID)
	}

	if sess.channel != nil {
		sess.channel.Status.CompareAndSwap(ChannelInviting, ChannelPlaying)
	}

	slog.Info("gb28181: session marked as playing", "channel_id", channelID)

	return nil
}
