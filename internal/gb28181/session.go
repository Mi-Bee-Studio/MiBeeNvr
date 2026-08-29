package gb28181

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
	"github.com/mickeyzzc/gb28181-go/manscdp"
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

// session holds a live GB28181 media session (INVITE). mu guards the
// lifecycle fields (conn/cancel/tcpLn/closed) against the accept/dial
// goroutines racing with teardown.
type session struct {
	channelID string
	deviceID  string
	channel   *Channel
	receiver  *Receiver
	hub       *streamhub.StreamHub
	port      uint16

	mu     sync.Mutex
	conn   net.Conn
	cancel context.CancelFunc
	closed bool         // teardown ran — no new connections may attach
	tcpLn  net.Listener // TCP-passive media listener (closed on teardown)
	sink   AUWriter     // For playback sessions, stores the sink for Stop() cleanup
}

// Media transport values for SessionManager (GB28181 media_transport).
const (
	MediaUDP        = "udp"
	MediaTCPPassive = "tcp-passive"
	MediaTCPActive  = "tcp-active"
)

// tcpAcceptTimeout bounds how long a TCP-passive listener waits for the
// device to connect after the INVITE handshake. A device that never connects
// leaves a zombie listener that blocks every future auto-INVITE.
const tcpAcceptTimeout = 60 * time.Second

// SessionManager orchestrates INVITE/BYE media sessions.
// Each session maps a MiBee camera (represented by a Channel) to an RTP receiver.
// Thread-safe: sessions map under mutex, channel state under atomics.
type SessionManager struct {
	portManager *PortManager
	sessions    map[string]*session
	// playbackSessions holds device-recording fetch sessions (s=Playback
	// INVITEs) — keyed by channel like sessions but independent, so live
	// streaming and playback fetching can coexist on one channel.
	playbackSessions map[string]*session
	mu               sync.Mutex
	serverID         string // GB28181 server ID (20-digit ASCII)
	ssrcSeq          atomic.Int64

	// mediaTransportFunc resolves the current media transport ("udp" |
	// "tcp-passive" | "tcp-active"); nil → UDP. A func so config reloads are
	// picked up without rebuilding the manager.
	mediaTransportFunc func() string

	// tcpFramingFunc resolves the configured TCP framing ("rfc4571" |
	// "0x24" | "auto"); nil → auto.
	tcpFramingFunc func() string
	// byeSender (optional, set by the SIP server) transmits a SIP BYE to the
	// device before local teardown when a session is stopped locally
	// (user-initiated stop, INVITE failure). Nil in tests.
	byeSender func(channelID string) error

	// playbackByeSender transmits the in-dialog BYE for playback fetch
	// sessions (separate dialog store on the SIP side).
	playbackByeSender func(channelID string) error

	// firstRTPHook (optional, set by the SIP server) fires when a session's
	// receiver gets its first RTP packet — evidence the dialog works even
	// without a transaction-matched 200 OK.
	firstRTPHook func(channelID string)
}

// NewSessionManager creates a SessionManager.
func NewSessionManager(pm *PortManager, serverID string) *SessionManager {
	return &SessionManager{
		portManager:      pm,
		sessions:         make(map[string]*session),
		playbackSessions: make(map[string]*session),
		serverID:         serverID,
	}
}

// SetMediaTransport wires the media transport resolver (see MediaUDP etc.).
func (sm *SessionManager) SetMediaTransport(fn func() string) {
	sm.mu.Lock()
	sm.mediaTransportFunc = fn
	sm.mu.Unlock()
}

// SetTCPFraming wires the TCP framing resolver.
func (sm *SessionManager) SetTCPFraming(fn func() string) {
	sm.mu.Lock()
	sm.tcpFramingFunc = fn
	sm.mu.Unlock()
}

// MediaTransport snapshots the configured media transport (tcp-passive
// default since #460; falls back to MediaUDP for unknown values).
func (sm *SessionManager) MediaTransport() string {
	sm.mu.Lock()
	fn := sm.mediaTransportFunc
	sm.mu.Unlock()
	if fn == nil {
		return MediaUDP
	}
	if t := fn(); t == MediaTCPPassive || t == MediaTCPActive {
		return t
	}
	return MediaUDP
}

// tcpFraming snapshots the configured TCP framing.
func (sm *SessionManager) tcpFraming() TCPMode {
	sm.mu.Lock()
	fn := sm.tcpFramingFunc
	sm.mu.Unlock()
	if fn == nil {
		return TCPModeAuto
	}
	switch fn() {
	case "rfc4571":
		return TCPModeRFC4571
	case "0x24":
		return TCPMode0x24
	default:
		return TCPModeAuto
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
// UDP socket. onAudio (optional) receives demuxed PS audio frames.
//
// Transport (SessionManager.MediaTransport):
//   - udp: RTP/AVP over the allocated UDP port (the classic GB28181 path);
//   - tcp-passive: NVR listens TCP on the allocated port; the device
//     connects after answering (a=setup:passive) — Hikvision/Dahua default;
//   - tcp-active: the offer declares a=setup:active; after the device's 200
//     OK the caller must invoke ConnectActive with the answer SDP so the
//     NVR dials the device's media address.
func (sm *SessionManager) Invite(channel *Channel, serverIP string, deviceAddr string, sdpOffer []byte, onAU func(au [][]byte, ptsTicks int64, isIDR bool), onAudio AudioFrameHandler) ([]byte, error) {
	if channel == nil {
		return nil, fmt.Errorf("gb28181: channel is nil")
	}

	// Idempotency guard: recycle any prior session for this channel before
	// allocating a new one (its port, socket, and receiver would otherwise
	// leak — auto-INVITE fires on every device re-REGISTER). The SIP BYE must
	// reach the device BEFORE the old port is recycled: a sender that never
	// hears a BYE keeps streaming into the recycled port, which the port pool
	// hands to a DIFFERENT channel — two interleaved SSRCs then corrupt both
	// demuxers (observed as SPS flip-flop + AU starvation on cascades).
	sm.mu.Lock()
	if old, ok := sm.sessions[channel.ID]; ok {
		delete(sm.sessions, channel.ID)
		sender := sm.byeSender
		sm.mu.Unlock()
		if sender != nil {
			if err := sender(channel.ID); err != nil {
				slog.Warn("gb28181: SIP BYE for replaced session failed", "channel_id", channel.ID, "error", err)
			}
		}
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

	transport := sm.MediaTransport()
	// Build SDP answer (GB28181 minimal format)
	sdpAnswer := buildLiveSDP(serverIP, port, transport, ssrc)

	// Use the provided AU callback (from the recorder) instead of creating
	// an orphaned hub. When onAU is nil (tests), fall back to a local hub.
	var hub *streamhub.StreamHub
	if onAU == nil {
		hub = streamhub.New()
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
	receiver.AudioCallback = onAudio

	sess := &session{
		channelID: channel.ID,
		deviceID:  channel.DeviceID,
		channel:   channel,
		receiver:  receiver,
		hub:       hub,
		port:      port,
	}

	switch transport {
	case MediaTCPPassive:
		ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", serverIP, port))
		if err != nil {
			sm.portManager.Recycle(port)
			return nil, fmt.Errorf("gb28181: failed to listen TCP %s:%d: %w", serverIP, port, err)
		}
		sess.tcpLn = ln
		// The receiver starts once the device connects (after it answers the
		// INVITE). A device that never connects must not wedge the port.
		go sm.acceptTCP(sess)
	case MediaTCPActive:
		// Nothing to dial yet — ConnectActive (after the 200 OK) does it.
	default: // UDP
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
		setUDPReadBuffer(conn)
		sess.conn = conn
		ctx, cancel := context.WithCancel(context.Background())
		if err := receiver.Start(ctx, conn); err != nil {
			conn.Close()
			sm.portManager.Recycle(port)
			cancel()
			return nil, fmt.Errorf("gb28181: failed to start receiver: %w", err)
		}
		sess.cancel = cancel
	}

	// Store session
	sm.mu.Lock()
	sm.sessions[channel.ID] = sess
	sm.mu.Unlock()

	// Transition channel state: idle -> inviting
	channel.Status.CompareAndSwap(ChannelIdle, ChannelInviting)

	slog.Info("gb28181: session created", "channel_id", channel.ID, "device_id", channel.DeviceID,
		"port", port, "ssrc", ssrc, "transport", transport, "remote", deviceAddr)

	return sdpAnswer, nil
}

// buildLiveSDP assembles the live-view SDP for the requested transport.
func buildLiveSDP(serverIP string, port uint16, transport, ssrc string) []byte {
	var mLine, setup string
	switch transport {
	case MediaTCPPassive:
		mLine = fmt.Sprintf("m=video %d TCP/RTP/AVP 96\r\n", port)
		setup = "a=setup:passive\r\na=connection:new\r\n"
	case MediaTCPActive:
		// Offerer is active: port 9 (discard) — the answerer's 200 OK SDP
		// carries the address the NVR dials.
		mLine = "m=video 9 TCP/RTP/AVP 96\r\n"
		setup = "a=setup:active\r\na=connection:new\r\n"
	default:
		mLine = fmt.Sprintf("m=video %d RTP/AVP 96\r\n", port)
	}
	return []byte(fmt.Sprintf(
		"v=0\r\no=- 0 0 IN IP4 %s\r\ns=Play\r\nc=IN IP4 %s\r\nt=0 0\r\n%s"+
			"a=recvonly\r\na=rtpmap:96 PS/90000\r\n%sy=%s\r\n",
		serverIP, serverIP, mLine, setup, ssrc,
	))
}

// acceptTCP waits for the device's media connection on the session's TCP
// listener and starts the receiver with the accepted connection (single
// connection — the listener closes after the first accept). Recycles the
// session when the accept window expires.
func (sm *SessionManager) acceptTCP(sess *session) {
	ln := sess.tcpLn
	if ln == nil {
		return
	}
	if tcpLn, ok := ln.(*net.TCPListener); ok {
		_ = tcpLn.SetDeadline(time.Now().Add(tcpAcceptTimeout))
	}
	conn, err := ln.Accept()
	_ = ln.Close() // single-connection dialog; reject anything further
	if err != nil {
		slog.Debug("gb28181: TCP media accept failed", "channel_id", sess.channelID, "error", err)
		// A timeout with the session still installed means the device never
		// connected despite answering — recycle so the next auto-INVITE can.
		sm.mu.Lock()
		cur, alive := sm.sessions[sess.channelID]
		stillOurs := alive && cur == sess
		if stillOurs {
			delete(sm.sessions, sess.channelID)
		}
		sess.mu.Lock()
		sess.closed = true
		sess.tcpLn = nil
		sess.mu.Unlock()
		sm.mu.Unlock()
		if stillOurs {
			sender := sm.byeSender
			if sender != nil {
				_ = sender(sess.channelID)
			}
			sm.portManager.Recycle(sess.port)
			if sess.channel != nil {
				sess.channel.Status.Store(ChannelIdle)
			}
			slog.Warn("gb28181: device never connected TCP media — session recycled",
				"channel_id", sess.channelID)
		}
		return
	}
	sess.mu.Lock()
	if sess.closed {
		// Teardown won the race — reject the late connection.
		sess.mu.Unlock()
		conn.Close()
		return
	}
	sess.conn = conn
	sess.mu.Unlock()
	sess.receiver.SetTCPMode(sm.tcpFraming())
	ctx, cancel := context.WithCancel(context.Background())
	if err := sess.receiver.Start(ctx, conn); err != nil {
		slog.Warn("gb28181: failed to start TCP receiver", "channel_id", sess.channelID, "error", err)
		conn.Close()
		cancel()
		return
	}
	sess.mu.Lock()
	sess.cancel = cancel
	sess.mu.Unlock()
	slog.Info("gb28181: TCP media connection accepted", "channel_id", sess.channelID,
		"remote", conn.RemoteAddr(), "framing", sess.receiver.tcpMode)
}

// ConnectActiveTCP dials the device's TCP media address from its INVITE
// answer SDP (c= line + m= port) and starts the receiver — the final step
// of a tcp-active INVITE, invoked by the SIP server after the 200 OK.
func (sm *SessionManager) ConnectActiveTCP(channelID string, answerSDP []byte) error {
	sm.mu.Lock()
	sess, ok := sm.sessions[channelID]
	sm.mu.Unlock()
	if !ok {
		return fmt.Errorf("gb28181: no session for channel %q", channelID)
	}
	host, port, ok := sdpMediaAddress(answerSDP)
	if !ok {
		return fmt.Errorf("gb28181: answer SDP carries no usable media address")
	}
	addr := net.JoinHostPort(host, strconv.Itoa(int(port)))
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("gb28181: dial device media %s: %w", addr, err)
	}
	sess.mu.Lock()
	if sess.closed {
		sess.mu.Unlock()
		conn.Close()
		return fmt.Errorf("gb28181: session already torn down")
	}
	sess.conn = conn
	sess.mu.Unlock()
	sess.receiver.SetTCPMode(sm.tcpFraming())
	ctx, cancel := context.WithCancel(context.Background())
	if err := sess.receiver.Start(ctx, conn); err != nil {
		conn.Close()
		cancel()
		return fmt.Errorf("gb28181: start TCP receiver: %w", err)
	}
	sess.mu.Lock()
	sess.cancel = cancel
	sess.mu.Unlock()
	slog.Info("gb28181: TCP media dialed (active)", "channel_id", channelID, "remote", addr)
	return nil
}

// sdpMediaAddress extracts the connection address (c=IN IP4) and media port
// (m=video <port>) from an SDP body.
func sdpMediaAddress(sdp []byte) (string, uint16, bool) {
	host := ""
	port := 0
	for _, line := range strings.Split(string(sdp), "\r\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "c=IN IP4 ") {
			host = strings.TrimSpace(strings.TrimPrefix(line, "c=IN IP4"))
		} else if strings.HasPrefix(line, "m=video ") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if p, err := strconv.Atoi(fields[1]); err == nil {
					port = p
				}
			}
		}
	}
	if host == "" || port <= 0 || port > 65535 {
		return "", 0, false
	}
	return host, uint16(port), true
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
	// Mark closed and snapshot lifecycle fields under the session lock so the
	// accept/dial goroutines can never attach a connection to a dead session.
	sess.mu.Lock()
	sess.closed = true
	conn, ln, cancel := sess.conn, sess.tcpLn, sess.cancel
	sess.mu.Unlock()

	// Stop receiver
	if err := sess.receiver.Stop(); err != nil {
		slog.Warn("gb28181: receiver stop error", "channel_id", sess.channelID, "error", err)
	}

	// Close connection
	if conn != nil {
		_ = conn.Close()
	}

	// Close the TCP-passive media listener (unaccepted sessions)
	if ln != nil {
		_ = ln.Close()
	}

	// Recycle port
	sm.portManager.Recycle(sess.port)

	// Cancel context
	if cancel != nil {
		cancel()
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
// as a UAC INVITE. Playback sessions live in a separate map from live
// sessions — the same channel can record live and fetch playback at once.
// onAudio (optional) receives demuxed PS audio frames.
func (sm *SessionManager) InvitePlayback(channel *Channel, serverIP string, start, end time.Time, sink AUWriter, onAudio AudioFrameHandler) ([]byte, error) {
	return sm.inviteFetch(channel, serverIP, start, end, sink, onAudio, false)
}

// InviteDownload creates a download session (#378): identical pipeline to a
// playback fetch, but the SDP names s=Download (GB/T 28181-2022 §9.3.4 file
// transfer semantics — the device sends at file speed, not 1x pacing) and the
// SSRC carries the download leading digit 2. Sessions share the fetch map
// with playbacks: one fetch per channel regardless of kind.
func (sm *SessionManager) InviteDownload(channel *Channel, serverIP string, start, end time.Time, sink AUWriter, onAudio AudioFrameHandler) ([]byte, error) {
	return sm.inviteFetch(channel, serverIP, start, end, sink, onAudio, true)
}

// inviteFetch is the shared playback/download session constructor.
func (sm *SessionManager) inviteFetch(channel *Channel, serverIP string, start, end time.Time, sink AUWriter, onAudio AudioFrameHandler, download bool) ([]byte, error) {
	if channel == nil {
		return nil, fmt.Errorf("gb28181: channel is nil")
	}
	if sink == nil {
		return nil, fmt.Errorf("gb28181: sink is nil")
	}

	// One playback/download per channel: recycle a prior fetch first.
	sm.mu.Lock()
	if old, ok := sm.playbackSessions[channel.ID]; ok {
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

	// Generate SSRC per GB/T 28181 Annex C.2.4: playback leading digit 1,
	// download leading digit 2 (2022 extension).
	sdpName, ssrc := "Playback", manscdp.SSRC(true, sm.serverID, int(sm.ssrcSeq.Add(1)))
	if download {
		sdpName = "Download"
		ssrc = manscdp.SSRCDownload(sm.serverID, int(sm.ssrcSeq.Add(1)))
	}

	// Convert times to NTP timestamps (seconds since 1900-01-01)
	// NTP timestamp = Unix timestamp + 2208988800
	startNTP := start.Unix() + 2208988800
	endNTP := end.Unix() + 2208988800

	// Build SDP answer for the fetch (GB28181 format with t= line)
	sdpAnswer := []byte(fmt.Sprintf(
		"v=0\r\no=- 0 0 IN IP4 %s\r\ns=%s\r\nc=IN IP4 %s\r\nt=%d %d\r\nm=video %d RTP/AVP 96\r\na=recvonly\r\na=rtpmap:96 PS/90000\r\ny=%s\r\n",
		serverIP, sdpName, serverIP, startNTP, endNTP, port, ssrc,
	))

	// Create StreamHub for this session
	hub := streamhub.New()
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
	setUDPReadBuffer(conn)

	// Feed complete AUs to the sink (AU grouping preserved for muxing)
	receiver.AUCallback = func(au [][]byte, ptsTicks int64, isIDR bool) {
		sink.WriteNALU(au, ptsTicks, isIDR)
	}
	receiver.AudioCallback = onAudio

	// Start receiver in context
	ctx, cancel := context.WithCancel(context.Background())
	if err := receiver.Start(ctx, conn); err != nil {
		conn.Close()
		sm.portManager.Recycle(port)
		cancel()
		return nil, fmt.Errorf("gb28181: failed to start receiver: %w", err)
	}

	// Store session (playback map — independent of the live session)
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
	sm.playbackSessions[channel.ID] = sess
	sm.mu.Unlock()

	slog.Info("gb28181: fetch session created", "kind", sdpName, "channel_id", channel.ID, "device_id", channel.DeviceID,
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

// GetPlaybackReceiver returns the playback fetch receiver for channelID, or
// nil when no fetch is running.
func (sm *SessionManager) GetPlaybackReceiver(channelID string) *Receiver {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sess, ok := sm.playbackSessions[channelID]; ok {
		return sess.receiver
	}
	return nil
}

// PlaybackActive reports whether a device-recording fetch is running.
func (sm *SessionManager) PlaybackActive(channelID string) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	_, ok := sm.playbackSessions[channelID]
	return ok
}

// ByePlayback stops a playback fetch session (SIP BYE via the registered
// playback bye sender when wired, then local teardown — closing the sink
// finalizes the recording). A no-op when no fetch is active.
func (sm *SessionManager) ByePlayback(channelID string) error {
	sm.mu.Lock()
	sess, ok := sm.playbackSessions[channelID]
	if !ok {
		sm.mu.Unlock()
		return nil
	}
	delete(sm.playbackSessions, channelID)
	sender := sm.playbackByeSender
	sm.mu.Unlock()

	if sender != nil {
		if err := sender(channelID); err != nil {
			slog.Warn("gb28181: playback SIP BYE send failed", "channel_id", channelID, "error", err)
		}
	}
	sm.teardown(sess, false)
	return nil
}

// SetPlaybackByeSender wires the SIP BYE transmitter for playback sessions.
func (sm *SessionManager) SetPlaybackByeSender(sender func(channelID string) error) {
	sm.mu.Lock()
	sm.playbackByeSender = sender
	sm.mu.Unlock()
}

// GetHub returns the StreamHub for the given channelID, or nil if no active session.
func (sm *SessionManager) GetHub(channelID string) *streamhub.StreamHub {
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
	sessions := make(map[string]*session, len(sm.sessions)+len(sm.playbackSessions))
	for k, v := range sm.sessions {
		sessions[k] = v
	}
	for k, v := range sm.playbackSessions {
		sessions["pb:"+k] = v
	}
	sm.sessions = make(map[string]*session)
	sm.playbackSessions = make(map[string]*session)
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
