// GB/T 28181-2016 § 9.4 语音对讲 (voice intercom): the platform INVITEs the
// device with an audio-only SDP (G.711 A-law, a=sendrecv), the device's 200
// OK carries its receive address, and the platform streams RTP/G.711A to it.
// Audio enters via the /api/cameras/{id}/gb28181/talk WebSocket (browser
// mic → G.711A frames) and is packetized here.

package sip

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181"
	"github.com/ghettovoice/gosip/sip"
	"github.com/mickeyzzc/gb28181-go/manscdp"
)

// talkRTPPayloadType is PCMA (G.711 A-law) — the GB28181 talk default.
const talkRTPPayloadType = 8

// talkMaxFrame guards against absurd WS frames (a 20ms G.711A chunk is
// 160 bytes; even 1s chunks stay far below this).
const talkMaxFrame = 8192

// talkSession is one active voice intercom on a channel.
type talkSession struct {
	cameraID  string
	deviceID  string
	channelID string
	callID    string

	conn   *net.UDPConn // local sending socket; its port is advertised in the SDP offer
	target *net.UDPAddr // device's audio receive address from the 200 OK SDP

	ssrc uint32
	mu   sync.Mutex
	seq  uint16
	ts   uint32

	dialog *inviteDialog

	closed     bool
	packets    int64
	bytesSent  int64
	lastMetric time.Time
}

// StartTalk establishes a voice intercom with a channel: audio-only INVITE
// (s=Play, m=audio <port> RTP/AVP 8, a=sendrecv) → 200 OK → ACK. Idempotent:
// an existing session returns nil.
func (s *Server) StartTalk(cameraID, deviceID, channelID string) error {
	s.talkMu.Lock()
	if t := s.talks[channelID]; t != nil {
		s.talkMu.Unlock()
		return nil
	}
	s.talkMu.Unlock()

	s.mu.Lock()
	srv := s.gosipSrv
	s.mu.Unlock()
	if srv == nil {
		return fmt.Errorf("gb28181: SIP server not started")
	}
	dev, ok := s.deviceMgr.Device(deviceID)
	if !ok {
		return fmt.Errorf("gb28181: device %q not registered", deviceID)
	}
	dev.Mu.RLock()
	netAddr := dev.NetAddr
	dev.Mu.RUnlock()
	serverHost := s.localIPFor(netAddr)

	// Local UDP socket: its port is both the SDP offer port and the sending
	// socket (devices that stream back talk-audio reach us here too).
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("0.0.0.0"), Port: 0})
	if err != nil {
		return fmt.Errorf("gb28181: talk udp socket: %w", err)
	}
	localPort := conn.LocalAddr().(*net.UDPAddr).Port

	s.talkMu.Lock()
	s.talkSeq++
	seqNo := s.talkSeq
	s.talkMu.Unlock()
	ssrcStr := manscdp.SSRC(false, s.cfg.ServerID, 6000+seqNo%1000)
	ssrc, _ := strconv.ParseUint(ssrcStr, 10, 32)

	sdp := fmt.Sprintf("v=0\r\n"+
		"o=%s 0 0 IN IP4 %s\r\n"+
		"s=Play\r\n"+
		"c=IN IP4 %s\r\n"+
		"t=0 0\r\n"+
		"m=audio %d RTP/AVP %d\r\n"+
		"a=sendrecv\r\n"+
		"y=%s\r\n",
		s.cfg.ServerID, serverHost, serverHost, localPort, talkRTPPayloadType, ssrcStr)

	devHost, devPortStr, err := net.SplitHostPort(netAddr)
	if err != nil {
		conn.Close()
		return fmt.Errorf("gb28181: invalid device address %q: %w", netAddr, err)
	}
	devPort, _ := strconv.Atoi(devPortStr)
	portVal := sip.Port(devPort)

	from := &sip.Address{
		DisplayName: sip.String{Str: s.cfg.ServerID},
		Uri:         &sip.SipUri{FUser: sip.String{Str: s.cfg.ServerID}, FHost: serverHost},
	}
	to := &sip.Address{
		DisplayName: sip.String{Str: channelID},
		Uri:         &sip.SipUri{FUser: sip.String{Str: channelID}, FHost: devHost, FPort: &portVal},
	}
	recipient := &sip.SipUri{FUser: sip.String{Str: channelID}, FHost: devHost, FPort: &portVal}
	subject := fmt.Sprintf("%s:%s,%s:0", channelID, ssrcStr, s.cfg.ServerID)

	req, err := s.buildRequest(sip.INVITE, serverHost, from, to, recipient, subject, "application/sdp", sdp)
	if err != nil {
		conn.Close()
		return fmt.Errorf("gb28181: build talk INVITE: %w", err)
	}
	tx, err := srv.Request(req)
	if err != nil {
		conn.Close()
		return fmt.Errorf("gb28181: send talk INVITE to %s: %w", channelID, err)
	}
	resp, err := s.awaitInviteAnswer(srv, tx, req)
	if err != nil {
		conn.Close()
		return fmt.Errorf("gb28181: talk INVITE to %s: %w", channelID, err)
	}
	if resp == nil {
		conn.Close()
		return fmt.Errorf("gb28181: talk INVITE to %s: no matched answer", channelID)
	}

	// Device's audio receive address from the answer SDP (m=audio).
	host, port, ok := sdpAudioAddress([]byte(resp.Body()))
	if !ok {
		// Some firmwares answer video-form SDPs; without a usable media
		// address we cannot deliver audio — fail the talk cleanly.
		ack := sip.NewAckRequest("", req, resp, "", nil)
		_ = srv.Send(ack)
		byeErr := s.sendByeForTalk(req, resp)
		conn.Close()
		if byeErr != nil {
			slog.Debug("gb28181: talk cleanup BYE failed", "channel", channelID, "error", byeErr)
		}
		return fmt.Errorf("gb28181: talk answer SDP carries no usable audio address")
	}

	ack := sip.NewAckRequest("", req, resp, "", nil)
	if err := srv.Send(ack); err != nil {
		conn.Close()
		return fmt.Errorf("gb28181: send talk ACK: %w", err)
	}

	callID := ""
	if cid, ok := resp.CallID(); ok {
		callID = cid.String()
	}
	t := &talkSession{
		cameraID:  cameraID,
		deviceID:  deviceID,
		channelID: channelID,
		callID:    callID,
		conn:      conn,
		target:    &net.UDPAddr{IP: net.ParseIP(host), Port: int(port)},
		ssrc:      uint32(ssrc),
		dialog:    &inviteDialog{req: req, resp: resp},
	}
	s.talkMu.Lock()
	s.talks[channelID] = t
	s.talkMu.Unlock()
	slog.Info("gb28181: talk session established",
		"channel", channelID, "device", deviceID, "target", t.target, "ssrc", ssrcStr)
	return nil
}

// WriteTalkAudio packetizes one G.711 A-law frame as RTP/PCMA and sends it
// to the device. Silently drops frames after StopTalk raced a writer.
func (s *Server) WriteTalkAudio(channelID string, alaw []byte) {
	if len(alaw) == 0 || len(alaw) > talkMaxFrame {
		return
	}
	s.talkMu.Lock()
	t := s.talks[channelID]
	s.talkMu.Unlock()
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.seq++
	pkt := make([]byte, 12+len(alaw))
	pkt[0] = 0x80 // V=2
	pkt[1] = talkRTPPayloadType & 0x7F
	binary.BigEndian.PutUint16(pkt[2:], t.seq)
	binary.BigEndian.PutUint32(pkt[4:], t.ts)
	binary.BigEndian.PutUint32(pkt[8:], t.ssrc)
	copy(pkt[12:], alaw)
	// G.711 is one sample per byte at 8kHz — the sample count equals the
	// payload length.
	t.ts += uint32(len(alaw))
	_, err := t.conn.WriteToUDP(pkt, t.target)
	if err == nil {
		t.packets++
		t.bytesSent += int64(len(alaw))
	}
	t.mu.Unlock()
	if err != nil {
		slog.Debug("gb28181: talk audio send failed", "channel", channelID, "error", err)
		return
	}
	// Liveness log at most once a minute.
	if time.Since(t.lastMetric) > time.Minute {
		t.mu.Lock()
		t.lastMetric = time.Now()
		pk, by := t.packets, t.bytesSent
		t.mu.Unlock()
		slog.Debug("gb28181: talk streaming", "channel", channelID, "packets", pk, "audio_bytes", by)
	}
}

// StopTalk tears the intercom down: in-dialog BYE, socket close, cleanup.
func (s *Server) StopTalk(channelID string) error {
	s.talkMu.Lock()
	t := s.talks[channelID]
	delete(s.talks, channelID)
	s.talkMu.Unlock()
	if t == nil {
		return nil
	}
	t.mu.Lock()
	t.closed = true
	conn, dlg := t.conn, t.dialog
	pk, by := t.packets, t.bytesSent
	t.mu.Unlock()
	conn.Close()
	slog.Info("gb28181: talk session stopped",
		"channel", channelID, "packets", pk, "audio_bytes", by)
	return s.sendByeForTalk(dlg.req, dlg.resp)
}

// TalkStatusFor reports the active intercom state of a camera's channel
// (nil-status when idle).
func (s *Server) TalkStatusFor(cameraID string) gb28181.TalkStatus {
	s.talkMu.Lock()
	defer s.talkMu.Unlock()
	for _, t := range s.talks {
		if t.cameraID == cameraID {
			t.mu.Lock()
			defer t.mu.Unlock()
			return gb28181.TalkStatus{
				Active:    true,
				CameraID:  t.cameraID,
				ChannelID: t.channelID,
				Packets:   t.packets,
				BytesSent: t.bytesSent,
			}
		}
	}
	return gb28181.TalkStatus{Active: false}
}

// stopTalkOnBye ends a talk session whose BYE arrived from the device
// (matched by Call-ID). Returns true when the BYE belonged to a talk dialog.
func (s *Server) stopTalkOnBye(req sip.Request) bool {
	callID, ok := req.CallID()
	if !ok {
		return false
	}
	s.talkMu.Lock()
	defer s.talkMu.Unlock()
	for chID, t := range s.talks {
		if t.callID != "" && t.callID == callID.String() {
			t.mu.Lock()
			t.closed = true
			conn := t.conn
			t.mu.Unlock()
			conn.Close()
			delete(s.talks, chID)
			slog.Info("gb28181: talk BYE received from device", "channel", chID)
			return true
		}
	}
	return false
}

// sendByeForTalk transmits an in-dialog BYE for a talk session.
func (s *Server) sendByeForTalk(inviteReq sip.Request, inviteResp sip.Response) error {
	s.mu.Lock()
	srv := s.gosipSrv
	s.mu.Unlock()
	if srv == nil {
		return fmt.Errorf("gb28181: SIP server not started")
	}
	fromHdr, hasFrom := inviteResp.From()
	toHdr, hasTo := inviteResp.To()
	if !hasFrom || !hasTo {
		return nil
	}
	callID, hasCallID := inviteResp.CallID()
	seq := uint(1)
	if cseq, ok := inviteResp.CSeq(); ok {
		seq = uint(cseq.SeqNo) + 1
	}
	serverHost := s.localIPFor(inviteReq.Recipient().String())
	_, sipPort, err := parseSIPListen(s.cfg.SIPListen)
	if err != nil {
		sipPort = 5060
	}
	sipPortVal := sip.Port(sipPort)

	rb := sip.NewRequestBuilder()
	rb.SetMethod(sip.BYE)
	rb.SetFrom(&sip.Address{DisplayName: fromHdr.DisplayName, Uri: fromHdr.Address})
	rb.SetTo(&sip.Address{DisplayName: toHdr.DisplayName, Uri: toHdr.Address})
	if hasCallID {
		rb.SetCallID(callID)
	}
	rb.SetRecipient(inviteReq.Recipient())
	rb.SetHost(serverHost)
	rb.AddVia(&sip.ViaHop{
		Host:   serverHost,
		Port:   &sipPortVal,
		Params: sip.NewParams().Add("branch", sip.String{Str: sip.GenerateBranch()}),
	})
	rb.SetSeqNo(seq)
	byeReq, err := rb.Build()
	if err != nil {
		return fmt.Errorf("gb28181: build talk BYE: %w", err)
	}
	if _, err := srv.Request(byeReq); err != nil {
		return fmt.Errorf("gb28181: send talk BYE: %w", err)
	}
	return nil
}

// sdpAudioAddress extracts the connection address and m=audio media port
// from an SDP body (talk answers carry audio, not video, media lines).
func sdpAudioAddress(sdp []byte) (string, uint16, bool) {
	host := ""
	port := 0
	for _, line := range splitCRLF(string(sdp)) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "c=IN IP4 ") {
			host = strings.TrimSpace(strings.TrimPrefix(line, "c=IN IP4"))
		} else if strings.HasPrefix(line, "m=audio ") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if p, err := strconv.Atoi(fields[1]); err == nil {
					port = p
				}
			}
		}
	}
	if host == "" || port <= 0 || port > 65535 || net.ParseIP(host) == nil {
		return "", 0, false
	}
	return host, uint16(port), true
}

// sdpAudioCodec extracts the audio codec an SDP body declares on its m=audio
// media line: PCMA→g711a, PCMU→g711u, mpeg4-generic→aac. Returns "" when the
// body declares no usable audio codec. Used to seed the PS demuxer's no-PSM
// audio fallback for devices that mux audio into the PS stream without ever
// sending a Program Stream Map.
func sdpAudioCodec(sdp []byte) string {
	inAudio := false
	for _, line := range splitCRLF(string(sdp)) {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "m="):
			inAudio = strings.HasPrefix(line, "m=audio ")
		case inAudio && strings.HasPrefix(line, "a=rtpmap:"):
			// a=rtpmap:<pt> <encoding>/<clock> [/<params>]
			fields := strings.Fields(strings.TrimPrefix(line, "a=rtpmap:"))
			if len(fields) < 2 {
				continue
			}
			switch strings.ToUpper(fields[1]) {
			case "PCMA":
				return gb28181.AudioCodecG711A
			case "PCMU":
				return gb28181.AudioCodecG711U
			case "MPEG4-GENERIC":
				return gb28181.AudioCodecAAC
			}
		}
	}
	return ""
}
