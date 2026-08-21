package cascade

import (
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181/psmux"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/ghettovoice/gosip"
	"github.com/ghettovoice/gosip/sip"
)

// mediaSession forwards ONE camera's stream to the upper platform: hub
// subscription → psmux → RTP/UDP. Created on the upper platform's INVITE,
// torn down on BYE / Stop / send errors.
//
// v1 forwards video only: the hub's audio callback carries model.AudioG711
// without the A/μ-law distinction, and guessing the law distorts audio.
// Audio passthrough lands together with a hub-level law field (#364
// follow-up).
type mediaSession struct {
	svc     *Service
	callID  string
	channel string // GB channel ID the upper platform INVITEd
	camera  string // local camera ID
	upper   *upper // owning upper platform (#370 dialog routing)

	conn net.Conn     // UDP socket or the dialed TCP media connection
	dst  *net.UDPAddr // nil for TCP media
	ssrc uint32

	mux       *psmux.Muxer
	rtp       *psmux.RTPPacketizer
	subID     string
	sdpBody   string
	codecHint string
	// withAudio: the upper's INVITE carried an audio m-line — hub audio
	// frames are PS-muxed alongside video (#370).
	withAudio    bool
	audioSubID   string
	audioLawWarn bool // one-shot: audio present but law unknown/unsupported

	// audioMu guards audioPending: G.711 frames buffered by the audio
	// callback goroutine until the next video AU's PS burst muxes them in.
	audioMu sync.Mutex
	// audioPending holds frames awaiting the next video AU burst (see
	// Muxer.AppendAudioPES for why audio must ride inside video bursts).
	audioPending []audioPendingFrame

	closed atomic.Bool
}

// audioPendingFrame is one buffered G.711 frame awaiting the next video AU.
type audioPendingFrame struct {
	pts  int64
	data []byte
}

// inviteSDP is the subset of an INVITE's SDP the cascade cares about.
type inviteSDP struct {
	host  string
	port  int
	tcp   bool // m= line negotiated TCP/RTP/AVP (upper's tcp-passive offer)
	ssrc  uint32
	name  string // s= session name ("Play"|"Playback")
	t0    int64  // t= start, Unix seconds (NTP-era values normalized)
	t1    int64  // t= end, Unix seconds
	hasT  bool
	rawT0 string // original t= tokens, echoed by the playback answer
	rawT1 string
}

// sdpFromInvite extracts the upper platform's receive address (c= + m=video
// port), requested SSRC (y= line), session name (s=), and time range (t=).
// The t= values follow GB/T 28181 Annex C: NTP-era seconds — but Unix-era
// seconds are accepted too (both conventions exist in the field).
func sdpFromInvite(body []byte) (inviteSDP, error) {
	var sd inviteSDP
	for _, line := range strings.Split(string(body), "\r\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "c=IN IP4 "):
			sd.host = strings.TrimSpace(strings.TrimPrefix(line, "c=IN IP4"))
		case strings.HasPrefix(line, "m=video "):
			fields := strings.Fields(line)
			if len(fields) >= 3 && fields[2] == "TCP/RTP/AVP" {
				sd.tcp = true
			}
			if len(fields) >= 2 {
				sd.port, _ = strconv.Atoi(fields[1])
			}
		case strings.HasPrefix(line, "y="):
			v, _ := strconv.ParseUint(strings.TrimPrefix(line, "y="), 10, 32)
			sd.ssrc = uint32(v)
		case strings.HasPrefix(line, "s="):
			sd.name = strings.TrimSpace(strings.TrimPrefix(line, "s="))
		case strings.HasPrefix(line, "t="):
			fields := strings.Fields(strings.TrimPrefix(line, "t="))
			if len(fields) >= 2 {
				sd.rawT0, sd.rawT1 = fields[0], fields[1]
				if v0, err0 := strconv.ParseInt(fields[0], 10, 64); err0 == nil && v0 > 0 {
					if v1, err1 := strconv.ParseInt(fields[1], 10, 64); err1 == nil && v1 > 0 {
						sd.t0, sd.t1 = sdpToUnix(v0), sdpToUnix(v1)
						sd.hasT = true
					}
				}
			}
		}
	}
	if sd.host == "" || sd.port <= 0 {
		return sd, fmt.Errorf("invite SDP lacks c=/m=video address")
	}
	return sd, nil
}

// sdpToUnix normalizes an SDP t= value to Unix seconds. NTP-era seconds
// (post-2037 in Unix terms, i.e. ≥3e9) are shifted by the 1900→1970 delta.
func sdpToUnix(v int64) int64 {
	if v >= 3_000_000_000 {
		return v - ntpEpochDelta
	}
	return v
}

// onInvite handles the upper platform's INVITE for one aggregated channel:
// 200 OK with our sendonly SDP, then forward the camera's stream (ACK from
// the upper platform completes the dialog; gosip auto-matches it).
func (s *Service) onInvite(req sip.Request, _ sip.ServerTransaction) {
	if s.srv == nil {
		return
	}
	callID := ""
	if h, ok := req.CallID(); ok {
		callID = h.String()
	}
	_, channelID := reqIDs(req)

	sd, err := sdpFromInvite([]byte(req.Body()))
	if err != nil {
		slog.Warn("gb28181-cascade: INVITE SDP parse failed", "error", err)
		_, _ = s.srv.RespondOnRequest(req, 400, "Bad SDP", "", nil)
		return
	}

	// Playback/download dialogs take the recordings-backed path (download =
	// same pump without 1x pacing, #378).
	if strings.EqualFold(sd.name, "Playback") || strings.EqualFold(sd.name, "Download") {
		s.onPlaybackInvite(req, callID, channelID, sd)
		return
	}

	// Idempotency: a re-INVITE for an active forward keeps it. A re-INVITE
	// for a live playback restarts it when the requested window moved.
	s.mu.Lock()
	if ms, ok := s.sessions[callID]; ok {
		sdp := ms.sdpBody
		s.mu.Unlock()
		_, _ = s.srv.RespondOnRequest(req, 200, "OK", sdp, nil)
		return
	}
	if ps, ok := s.playbacks[callID]; ok {
		sameWindow := sd.hasT && abs64(sd.t0-ps.start.Unix()) < 2 && abs64(sd.t1-ps.end.Unix()) < 2
		if sameWindow {
			sdp := ps.sdpBody
			s.mu.Unlock()
			_, _ = s.srv.RespondOnRequest(req, 200, "OK", sdp, nil)
			return
		}
		delete(s.playbacks, callID)
		s.mu.Unlock()
		ps.finish("re-INVITE with new window", true)
	} else {
		s.mu.Unlock()
	}

	cameraID, ok := s.cameraOfChannel(channelID)
	if !ok {
		slog.Warn("gb28181-cascade: INVITE for unknown channel", "channel", channelID)
		_, _ = s.srv.RespondOnRequest(req, 404, "Unknown Channel", "", nil)
		return
	}
	if cam, ok := s.cameraInfo(cameraID); ok && cam.CascadeHidden {
		// Catalog convergence: the channel was allocated once (allocation rows
		// persist) but the camera is now hidden — the upper may still hold the
		// stale binding and INVITE it. Refuse like an unknown channel.
		slog.Info("gb28181-cascade: INVITE for hidden camera refused", "channel", channelID, "camera", cameraID)
		_, _ = s.srv.RespondOnRequest(req, 404, "Unknown Channel", "", nil)
		return
	}
	hub := s.src.Hub(cameraID)
	if hub == nil {
		_, _ = s.srv.RespondOnRequest(req, 500, "Stream Unavailable", "", nil)
		return
	}

	// Supersede: a new-dialog INVITE for a channel that is already forwarding
	// means the upper recycled the session (its BYE may be lost or still in
	// flight). Keeping both forwards alive overlaps two SSRCs onto the upper's
	// recycled receive port — the upper's first-packet SSRC latch grabs
	// whichever sender arrives first and every packet of the other is dropped
	// as foreign (observed as endless "recycling stale session / no keyframe"
	// churn on the fnOS upper, 2026-08-19). One channel, one live forward.
	s.mu.Lock()
	for otherID, other := range s.sessions {
		if otherID != callID && other.channel == channelID {
			delete(s.sessions, otherID)
			go other.teardown("superseded by new-dialog re-INVITE")
		}
	}
	s.mu.Unlock()

	// Media transport: the upper's tcp-passive offer (TCP/RTP/AVP +
	// a=setup:passive) means WE connect — TCP retransmission survives lossy
	// hops that shred back-to-back UDP bursts (4K IDR bursts lost ~10% of
	// packets on one deployment's switch, truncating every keyframe at a PES
	// boundary; 2026-08-21). UDP offers keep the classic socket.
	var conn net.Conn
	var dst *net.UDPAddr
	if sd.tcp {
		conn, err = net.DialTimeout("tcp", net.JoinHostPort(sd.host, strconv.Itoa(sd.port)), 5*time.Second)
		if err != nil {
			slog.Warn("gb28181-cascade: TCP media dial failed", "channel", channelID, "upper", sd.host, "error", err)
			_, _ = s.srv.RespondOnRequest(req, 500, "Internal Error", "", nil)
			return
		}
		slog.Info("gb28181-cascade: TCP media connected", "channel", channelID, "upper", conn.RemoteAddr())
	} else {
		dst = &net.UDPAddr{IP: net.ParseIP(sd.host), Port: sd.port}
		conn, err = net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
		if err != nil {
			_, _ = s.srv.RespondOnRequest(req, 500, "Internal Error", "", nil)
			return
		}
	}

	ms := &mediaSession{
		svc: s, callID: callID, channel: channelID, camera: cameraID,
		upper: s.upperOf(req),
		conn:  conn, dst: dst, ssrc: sd.ssrc,
		mux:       psmux.New(),
		withAudio: strings.Contains(string(req.Body()), "m=audio"),
	}
	if cam, ok := s.cameraInfo(cameraID); ok && cam.Encoding != "" {
		ms.codecHint = cam.Encoding
		ms.mux.SetVideoCodec(cam.Encoding)
	}
	if sd.tcp {
		ms.rtp = psmux.NewRTPPacketizerTCP(conn, sd.ssrc, uint16(time.Now().UnixNano()&0xFFFF))
	} else {
		ms.rtp = psmux.NewRTPPacketizer(conn, dst, sd.ssrc, uint16(time.Now().UnixNano()&0xFFFF))
	}
	ms.sdpBody = fmt.Sprintf(
		"v=0\r\no=- 0 0 IN IP4 %s\r\ns=Play\r\nc=IN IP4 %s\r\nt=0 0\r\n"+
			"m=video %d RTP/AVP 96\r\na=sendonly\r\na=rtpmap:96 PS/90000\r\ny=%d\r\n",
		ms.localHost(), ms.localHost(), ms.localPort(), sd.ssrc)
	if sd.tcp {
		// Answer as the TCP-active side: we dialed, per the offer's setup:passive.
		ms.sdpBody = fmt.Sprintf(
			"v=0\r\no=- 0 0 IN IP4 %s\r\ns=Play\r\nc=IN IP4 %s\r\nt=0 0\r\n"+
				"m=video %d TCP/RTP/AVP 96\r\na=sendonly\r\na=setup:active\r\na=connection:new\r\n"+
				"a=rtpmap:96 PS/90000\r\ny=%d\r\n",
			ms.localHost(), ms.localHost(), ms.localPort(), sd.ssrc)
	}

	s.mu.Lock()
	s.sessions[callID] = ms
	s.mu.Unlock()

	_, _ = s.srv.RespondOnRequest(req, 200, "OK", ms.sdpBody, nil)
	go ms.run(hub)
	slog.Info("gb28181-cascade: INVITE accepted — forwarding",
		"channel", channelID, "camera", cameraID, "to", dst.String(), "ssrc", sd.ssrc)
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func reqIDs(req sip.Request) (deviceID, channelID string) {
	if to, ok := req.To(); ok {
		channelID = to.Address.User().String()
	}
	if from, ok := req.From(); ok {
		deviceID = from.Address.User().String()
	}
	return
}

// bareCallID strips a serialized header prefix. req.CallID().String() returns
// the FULL header ("Call-ID: <value>") — usable as a map key but NOT as the
// value when re-building a request (the doubled prefix makes gosip's parser
// on the peer reject the whole message).
func bareCallID(callID string) string {
	return strings.TrimSpace(strings.TrimPrefix(callID, "Call-ID:"))
}

func (ms *mediaSession) localHost() string {
	h, _ := ms.svc.localHostPort(ms.upper)
	return h
}

func (ms *mediaSession) localPort() int {
	_, p := ms.svc.localHostPort(ms.upper)
	return p
}

// run subscribes to the camera's hub and pumps frames until stopped.
func (ms *mediaSession) run(hub *model.StreamHub) {
	// Unique per dialog: the upper platform may re-INVITE the same channel in
	// a NEW dialog while an old one lingers — a channel-only ID collides in
	// the hub's consumer registry.
	subID := "cascade-" + ms.callID
	err := hub.Subscribe(subID, func(pts int64, au [][]byte) {
		if ms.closed.Load() || len(au) == 0 {
			return
		}
		// Annex-B framing for psmux.
		var annexB []byte
		for _, nalu := range au {
			annexB = append(annexB, 0, 0, 0, 1)
			annexB = append(annexB, nalu...)
		}
		if ms.codecHint == "" {
			ms.codecHint = sniffCodec(au[0])
			ms.mux.SetVideoCodec(ms.codecHint)
		}
		// Take the audio buffered since the last video AU and mux it into
		// THIS PS burst — one RTP stream, one marker per access unit. A
		// standalone audio burst carries its own marker, and receivers treat
		// ANY marker as an AU boundary: an audio burst slipping between a
		// large video AU's RTP packets truncated the frame at the last
		// completed PES (IDRs cut at exactly ~maxPESPayload with garbage
		// tails; 2026-08-21).
		ms.audioMu.Lock()
		pending := ms.audioPending
		ms.audioPending = nil
		ms.audioMu.Unlock()
		ps := ms.mux.WriteAU(annexB, pts, auIsIDR(au, ms.codecHint))
		for _, f := range pending {
			ps = ms.mux.AppendAudioPES(ps, f.data, f.pts)
		}
		if err := ms.rtp.Send(ps, pts); err != nil {
			ms.teardown("send error")
		}
	})
	if err != nil {
		slog.Warn("gb28181-cascade: hub subscribe failed", "camera", ms.camera, "error", err)
		ms.teardown("subscribe failed")
		return
	}
	ms.subID = subID

	// Audio upstream (#370): when the upper INVITEd with an audio m-line,
	// subscribe to the hub's audio and PS-mux frames alongside video. Only
	// law-specific G.711 is forwardable — PS stream types distinguish A-law
	// (0x90) from μ-law (0x91), and legacy producers still broadcasting the
	// unspecified "g711" codec are skipped rather than sent under a guessed
	// law (wrong-law audio is loud noise).
	if !ms.withAudio {
		return
	}
	audioSubID := subID + "-audio"
	if err := hub.SubscribeAudio(audioSubID, func(pts int64, codec model.AudioCodec, data []byte) {
		if ms.closed.Load() || len(data) == 0 {
			return
		}
		switch string(codec) {
		case "g711a":
			ms.mux.SetAudioCodec("g711a")
		case "g711u":
			ms.mux.SetAudioCodec("g711u")
		default:
			if !ms.audioLawWarn {
				ms.audioLawWarn = true
				slog.Warn("gb28181-cascade: audio codec not forwardable over PS — skipped",
					"camera", ms.camera, "codec", string(codec))
			}
			return
		}
		// Buffer (do NOT Send): the next video AU muxes this frame in via
		// AppendAudioPES. If video ever stalls (>~180ms of audio queued),
		// flush standalone so audio does not buffer forever — the marker
		// hazard only exists while video packets are in flight around it.
		ms.audioMu.Lock()
		ms.audioPending = append(ms.audioPending, audioPendingFrame{pts: pts, data: append([]byte(nil), data...)})
		standalone := len(ms.audioPending) > 9
		var flush []audioPendingFrame
		if standalone {
			flush = ms.audioPending
			ms.audioPending = nil
		}
		ms.audioMu.Unlock()
		if standalone {
			out := ms.mux.WriteAudio(nil, pts)
			for _, f := range flush {
				out = ms.mux.AppendAudioPES(out, f.data, f.pts)
			}
			if err := ms.rtp.Send(out, pts); err != nil {
				ms.teardown("audio send error")
			}
		}
	}); err != nil {
		slog.Warn("gb28181-cascade: hub audio subscribe failed", "camera", ms.camera, "error", err)
		return
	}
	ms.audioSubID = audioSubID
}

// onBye tears a forward or playback dialog down when the upper platform
// sends BYE.
func (s *Service) onBye(req sip.Request, _ sip.ServerTransaction) {
	callID := ""
	if h, ok := req.CallID(); ok {
		callID = h.String()
	}
	_, _ = s.srv.RespondOnRequest(req, 200, "OK", "", nil)

	s.mu.Lock()
	ms := s.sessions[callID]
	delete(s.sessions, callID)
	ps := s.playbacks[callID]
	delete(s.playbacks, callID)
	s.mu.Unlock()
	if ms != nil {
		ms.close()
		slog.Info("gb28181-cascade: BYE — forward stopped", "channel", ms.channel)
	}
	if ps != nil {
		ps.finish("BYE from upper platform", false) // already BYEd — no reply BYE
	}
}

// teardown stops the session on error (self-initiated) and BYEs the dialog.
func (ms *mediaSession) teardown(reason string) {
	if !ms.closed.CompareAndSwap(false, true) {
		return
	}
	slog.Warn("gb28181-cascade: forward error, stopping", "channel", ms.channel, "reason", reason)
	ms.svc.mu.Lock()
	delete(ms.svc.sessions, ms.callID)
	ms.svc.mu.Unlock()
	ms.close()
	ms.svc.sendBye(ms.upper, ms.callID, ms.channel)
}

func (ms *mediaSession) close() {
	ms.closed.Store(true)
	if hub := ms.svc.src.Hub(ms.camera); hub != nil {
		if ms.audioSubID != "" {
			hub.UnsubscribeAudio(ms.audioSubID)
			ms.audioSubID = ""
		}
		if ms.subID != "" {
			hub.Unsubscribe(ms.subID)
		}
	}
	if ms.conn != nil {
		_ = ms.conn.Close()
	}
}

// stop is the Stop()-path teardown (no BYE — the service is shutting down
// and sends a blanket unregister).
func (ms *mediaSession) stop() {
	ms.close()
}

// sendBye delivers an in-dialog BYE for an ended forward or playback
// dialog. Best-effort.
func (s *Service) sendBye(u *upper, callID, channelID string) {
	if s.srv == nil || u == nil {
		return
	}
	dst, err := upperAddr(u)
	if err != nil {
		return
	}
	host, port := s.localHostPort(u)
	p := sip.Port(port)
	dstPort := sip.Port(dst.Port)
	rb := sip.NewRequestBuilder()
	rb.SetMethod(sip.BYE)
	rb.SetFrom(&sip.Address{Uri: &sip.SipUri{FUser: sip.String{Str: u.cfg.LocalDeviceID}, FHost: host, FPort: &p}})
	rb.SetTo(&sip.Address{Uri: &sip.SipUri{FUser: sip.String{Str: channelID}, FHost: dst.IP.String()}})
	rb.SetRecipient(&sip.SipUri{FUser: sip.String{Str: channelID}, FHost: dst.IP.String(), FPort: &dstPort})
	cid := sip.CallID(bareCallID(callID))
	rb.SetCallID(&cid)
	rb.SetHost(host)
	rb.SetSeqNo(2)
	// A request without Via is unroutable — the upper platform's transaction
	// layer drops it (observed: end-of-playback BYE never matched the dialog).
	rb.AddVia(&sip.ViaHop{
		Host: host,
		Port: &p,
		Params: sip.NewParams().
			Add("branch", sip.String{Str: sip.GenerateBranch()}).
			Add("rport", sip.String{}),
	})
	req, err := rb.Build()
	if err != nil {
		slog.Warn("gb28181-cascade: BYE build failed", "channel", channelID, "error", err)
		return
	}
	if _, err := s.srv.Request(req); err != nil {
		slog.Warn("gb28181-cascade: BYE send failed", "channel", channelID, "error", err)
	}
}

// auIsIDR reports whether an AU opens a new stream/ GOP: H.264 IDR/SPS
// (NAL 5/7) or H.265 IDR/CRA/VPS/SPS (types 19/20/32/33). codec disambiguates
// the shared byte space ("" checks both readings — used only until the first
// frame fixes the codec).
func auIsIDR(au [][]byte, codec string) bool {
	for _, nalu := range au {
		if len(nalu) == 0 || nalu[0]&0x80 != 0 {
			continue
		}
		if codec == "h264" || codec == "" {
			if t := nalu[0] & 0x1F; t == 5 || t == 7 {
				return true
			}
		}
		if codec == "h265" || codec == "" {
			if t := (nalu[0] >> 1) & 0x3F; t == 19 || t == 20 || t == 32 || t == 33 {
				return true
			}
		}
	}
	return false
}

// sniffCodec guesses h264 vs h265 from the AU's leading NAL byte. Only the
// canonical H.265 VPS/SPS leads (0x40/0x42) are treated as h265 — other
// bytes are ambiguous between the two syntaxes and h264 (by far the more
// common source) wins. The camera's configured encoding takes precedence
// over this fallback whenever known.
func sniffCodec(firstNALU []byte) string {
	if len(firstNALU) > 0 && (firstNALU[0] == 0x40 || firstNALU[0] == 0x42) {
		return "h265"
	}
	return "h264"
}

var _ = gosip.Server(nil) // keep import until Stop() signature settles
