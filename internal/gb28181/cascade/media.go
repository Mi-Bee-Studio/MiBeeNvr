package cascade

import (
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
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

	conn *net.UDPConn
	dst  *net.UDPAddr
	ssrc uint32

	mux       *psmux.Muxer
	rtp       *psmux.RTPPacketizer
	subID     string
	sdpBody   string
	codecHint string

	closed atomic.Bool
}

// sdpFromInvite extracts the upper platform's receive address (c= + m=video
// port) and requested SSRC (y= line).
func sdpFromInvite(body []byte) (host string, port int, ssrc uint32, err error) {
	for _, line := range strings.Split(string(body), "\r\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "c=IN IP4 "):
			host = strings.TrimSpace(strings.TrimPrefix(line, "c=IN IP4"))
		case strings.HasPrefix(line, "m=video "):
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				port, _ = strconv.Atoi(fields[1])
			}
		case strings.HasPrefix(line, "y="):
			v, _ := strconv.ParseUint(strings.TrimPrefix(line, "y="), 10, 32)
			ssrc = uint32(v)
		}
	}
	if host == "" || port <= 0 {
		return "", 0, 0, fmt.Errorf("invite SDP lacks c=/m=video address")
	}
	return host, port, ssrc, nil
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

	// Idempotency: a re-INVITE for an active forward keeps it.
	s.mu.Lock()
	if ms, ok := s.sessions[callID]; ok {
		sdp := ms.sdpBody
		s.mu.Unlock()
		_, _ = s.srv.RespondOnRequest(req, 200, "OK", sdp, nil)
		return
	}
	s.mu.Unlock()

	cameraID, ok := s.cameraOfChannel(channelID)
	if !ok {
		slog.Warn("gb28181-cascade: INVITE for unknown channel", "channel", channelID)
		_, _ = s.srv.RespondOnRequest(req, 404, "Unknown Channel", "", nil)
		return
	}
	hub := s.src.Hub(cameraID)
	if hub == nil {
		_, _ = s.srv.RespondOnRequest(req, 500, "Stream Unavailable", "", nil)
		return
	}

	host, port, ssrc, err := sdpFromInvite([]byte(req.Body()))
	if err != nil {
		slog.Warn("gb28181-cascade: INVITE SDP parse failed", "error", err)
		_, _ = s.srv.RespondOnRequest(req, 400, "Bad SDP", "", nil)
		return
	}
	dst := &net.UDPAddr{IP: net.ParseIP(host), Port: port}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		_, _ = s.srv.RespondOnRequest(req, 500, "Internal Error", "", nil)
		return
	}

	ms := &mediaSession{
		svc: s, callID: callID, channel: channelID, camera: cameraID,
		conn: conn, dst: dst, ssrc: ssrc,
		mux: psmux.New(),
	}
	if cam, ok := s.cameraInfo(cameraID); ok && cam.Encoding != "" {
		ms.codecHint = cam.Encoding
		ms.mux.SetVideoCodec(cam.Encoding)
	}
	ms.rtp = psmux.NewRTPPacketizer(conn, dst, ssrc, uint16(time.Now().UnixNano()&0xFFFF))
	ms.sdpBody = fmt.Sprintf(
		"v=0\r\no=- 0 0 IN IP4 %s\r\ns=Play\r\nc=IN IP4 %s\r\nt=0 0\r\n"+
			"m=video %d RTP/AVP 96\r\na=sendonly\r\na=rtpmap:96 PS/90000\r\ny=%d\r\n",
		ms.localHost(), ms.localHost(), ms.localPort(), ssrc)

	s.mu.Lock()
	s.sessions[callID] = ms
	s.mu.Unlock()

	_, _ = s.srv.RespondOnRequest(req, 200, "OK", ms.sdpBody, nil)
	go ms.run(hub)
	slog.Info("gb28181-cascade: INVITE accepted — forwarding",
		"channel", channelID, "camera", cameraID, "to", dst.String(), "ssrc", ssrc)
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

func (ms *mediaSession) localHost() string {
	h, _ := ms.svc.localHostPort()
	return h
}

func (ms *mediaSession) localPort() int {
	_, p := ms.svc.localHostPort()
	return p
}

// run subscribes to the camera's hub and pumps frames until stopped.
func (ms *mediaSession) run(hub *model.StreamHub) {
	subID := "cascade-" + ms.channel
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
		if err := ms.rtp.Send(ms.mux.WriteAU(annexB, pts, auIsIDR(au, ms.codecHint)), pts); err != nil {
			ms.teardown("send error")
		}
	})
	if err != nil {
		slog.Warn("gb28181-cascade: hub subscribe failed", "camera", ms.camera, "error", err)
		ms.teardown("subscribe failed")
		return
	}
	ms.subID = subID
}

// onBye tears a forward down when the upper platform sends BYE.
func (s *Service) onBye(req sip.Request, _ sip.ServerTransaction) {
	callID := ""
	if h, ok := req.CallID(); ok {
		callID = h.String()
	}
	_, _ = s.srv.RespondOnRequest(req, 200, "OK", "", nil)

	s.mu.Lock()
	ms := s.sessions[callID]
	delete(s.sessions, callID)
	s.mu.Unlock()
	if ms != nil {
		ms.close()
		slog.Info("gb28181-cascade: BYE — forward stopped", "channel", ms.channel)
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
	ms.svc.sendBye(ms.callID, ms.channel)
}

func (ms *mediaSession) close() {
	ms.closed.Store(true)
	if hub := ms.svc.src.Hub(ms.camera); hub != nil {
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

// sendBye delivers an in-dialog BYE for an errored forward. Best-effort.
func (s *Service) sendBye(callID, channelID string) {
	if s.srv == nil {
		return
	}
	dst, err := s.upperAddr()
	if err != nil {
		return
	}
	host, port := s.localHostPort()
	p := sip.Port(port)
	dstPort := sip.Port(dst.Port)
	rb := sip.NewRequestBuilder()
	rb.SetMethod(sip.BYE)
	rb.SetFrom(&sip.Address{Uri: &sip.SipUri{FUser: sip.String{Str: s.cfg.LocalDeviceID}, FHost: host, FPort: &p}})
	rb.SetTo(&sip.Address{Uri: &sip.SipUri{FUser: sip.String{Str: channelID}, FHost: dst.IP.String()}})
	rb.SetRecipient(&sip.SipUri{FUser: sip.String{Str: channelID}, FHost: dst.IP.String(), FPort: &dstPort})
	cid := sip.CallID(callID)
	rb.SetCallID(&cid)
	rb.SetHost(host)
	rb.SetSeqNo(2)
	if req, err := rb.Build(); err == nil {
		_, _ = s.srv.Request(req)
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
