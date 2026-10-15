package doorbell

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ghettovoice/gosip"
	"github.com/ghettovoice/gosip/sip"
	gosiptransport "github.com/ghettovoice/gosip/transport"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"
)

// SnapshotRunner mirrors the MQTT action dispatcher's seam (#656): capture,
// persist and publish for a camera ID.
type SnapshotRunner interface {
	RunSnapshot(ctx context.Context, cameraID, trigger string) (string, error)
}

// Server is the SIP doorbell ingress: registrar (mode A) + INVITE handling
// + two-way G.711 media, optionally outbound-registered to a PBX (mode B).
type Server struct {
	cfg    Config
	bus    *event.EventBus
	snap   SnapshotRunner
	log    *slog.Logger
	srv    gosip.Server
	reg    *registry
	nonces *nonces
	calls  *callManager

	stopOnce sync.Once
	stopped  chan struct{}
}

// NewServer builds a doorbell server. bus may be nil (events dropped);
// snap may be nil (no snapshots).
func NewServer(cfg Config, bus *event.EventBus, snap SnapshotRunner) *Server {
	s := &Server{
		cfg:     cfg,
		bus:     bus,
		snap:    snap,
		log:     slogx.Component("doorbell"),
		reg:     newRegistry(),
		nonces:  newNonces(),
		calls:   newCallManager(),
		stopped: make(chan struct{}),
	}
	return s
}

// Calls exposes the recent-call list (API surface).
func (s *Server) Calls() []*Call { return s.calls.calls() }

// Answer accepts a ringing call: 200 OK with our SDP, media up.
func (s *Server) Answer(callID string) error {
	c, err := s.calls.get(callID)
	if err != nil {
		return err
	}
	c.mu.Lock()
	state := c.State
	c.mu.Unlock()
	if state != CallRinging {
		return ErrCallState
	}
	if err := c.startMedia(); err != nil {
		_ = s.respond(c, 500, "Media setup failed", "")
		c.end("media-error")
		return err
	}
	pt, codecName, _ := c.offer.payloadType()
	host, port, err := c.media.localEndpoint(c.offer.Address)
	if err != nil {
		_ = s.respond(c, 500, "Media setup failed", "")
		c.end("media-error")
		return err
	}
	body := buildAudioAnswer(host, port, pt, codecName)
	if err := s.respond(c, 200, "OK", body); err != nil {
		c.end("answer-failed")
		return err
	}
	c.mu.Lock()
	c.State = CallAnswered
	c.AnsweredAt = time.Now()
	c.mu.Unlock()
	s.publish(event.TopicDoorbellAnswered, c, "")
	s.log.Info("doorbell call answered", "call_id", c.ID, "user", c.User)
	return nil
}

// Reject declines a ringing call (486 busy or 603 decline).
func (s *Server) Reject(callID string, decline bool) error {
	c, err := s.calls.get(callID)
	if err != nil {
		return err
	}
	c.mu.Lock()
	state := c.State
	c.mu.Unlock()
	if state != CallRinging {
		return ErrCallState
	}
	status := sip.StatusCode(486)
	reason := "Busy Here"
	if decline {
		status = 603
		reason = "Decline"
	}
	_ = s.respond(c, status, reason, "")
	c.end("rejected")
	s.publish(event.TopicDoorbellRejected, c, "rejected")
	return nil
}

// Hangup ends an answered call with a BYE to the doorbell.
func (s *Server) Hangup(callID string) error {
	c, err := s.calls.get(callID)
	if err != nil {
		return err
	}
	c.mu.Lock()
	state := c.State
	c.mu.Unlock()
	if state != CallAnswered {
		return ErrCallState
	}
	s.sendBYE(c)
	c.end("hangup")
	s.publish(event.TopicDoorbellEnded, c, "hangup")
	return nil
}

// TalkBridge attaches the WebSocket talk bridge to an answered call.
func (s *Server) TalkBridge(callID string) (*TalkBridge, error) {
	c, err := s.calls.get(callID)
	if err != nil {
		return nil, err
	}
	return c.attachTalk()
}

// Start binds the SIP listeners (returns on bind failure) and spawns the
// housekeeping loops. Non-blocking — the app service wrapper owns lifetime.
func (s *Server) Start(ctx context.Context) error {
	s.srv = gosip.NewServer(gosip.ServerConfig{Host: "0.0.0.0", UserAgent: "MiBeeNVR Doorbell"}, nil, nil, gosipLog(s.log))
	_ = s.srv.OnRequest(sip.REGISTER, s.handleRegister)
	_ = s.srv.OnRequest(sip.INVITE, s.handleInvite)
	_ = s.srv.OnRequest(sip.BYE, s.handleBye)
	_ = s.srv.OnRequest(sip.CANCEL, s.handleCancel)
	_ = s.srv.OnRequest(sip.OPTIONS, s.handleOptions)
	_ = s.srv.OnRequest(sip.ACK, s.handleAck)

	listen := s.cfg.Listen
	if listen == "" {
		listen = ":5070"
	}
	listen = widenWildcardListen(listen)
	network := strings.ToUpper(s.cfg.Transport)
	if network == "" {
		network = "UDP"
	}
	var err error
	switch network {
	case "TLS":
		err = s.srv.Listen(network, listen, &gosiptransport.TLSConfig{Cert: s.cfg.TLSCert, Key: s.cfg.TLSKey})
	default:
		err = s.srv.Listen(network, listen)
	}
	if err != nil {
		return fmt.Errorf("doorbell: sip listen %s %s: %w", network, listen, err)
	}

	if s.cfg.Outbound != nil {
		go s.outboundLoop(ctx)
	}

	go s.sweepLoop(ctx)
	s.log.Info("doorbell server listening", "listen", listen, "transport", network,
		"users", len(s.cfg.Users), "outbound", s.cfg.Outbound != nil, "lan_only", s.cfg.LANOnly)
	return nil
}

// Stop shuts the SIP server down (idempotent).
func (s *Server) Stop() {
	s.stopOnce.Do(func() {
		if s.srv != nil {
			s.srv.Shutdown()
		}
		for _, c := range s.calls.calls() {
			c.end("shutdown")
		}
		close(s.stopped)
	})
}

func (s *Server) sweepLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopped:
			return
		case <-t.C:
			for _, c := range s.calls.calls() {
				c.mu.Lock()
				state := c.State
				started := c.StartedAt
				c.mu.Unlock()
				// Ring timeout: nobody answered.
				if state == CallRinging && time.Since(started) > s.cfg.RingTimeout {
					_ = s.respond(c, 487, "Request Terminated", "")
					c.end("timeout")
					s.publish(event.TopicDoorbellEnded, c, "timeout")
				}
				// Safety cap: no eternal answered calls either.
				if state == CallAnswered && time.Since(started) > 10*time.Minute {
					s.sendBYE(c)
					c.end("timeout")
					s.publish(event.TopicDoorbellEnded, c, "timeout")
				}
			}
		}
	}
}

// ── SIP handlers ──

func (s *Server) handleRegister(req sip.Request, tx sip.ServerTransaction) {
	if !s.lanAllowed(req) {
		_ = s.respondRaw(req, 403, "Forbidden", "")
		return
	}
	user, ok := s.fromUser(req)
	if !ok {
		_ = s.respondRaw(req, 403, "Unknown user", "")
		return
	}
	u, configured := s.cfg.user(user)
	if !configured {
		_ = s.respondRaw(req, 403, "Unknown user", "")
		return
	}
	if !verifyDigest(req, u.Username, u.Secret) {
		nonce := s.nonces.issue(u.Username)
		_ = s.respondRaw(req, 401, "Unauthorized", "", challengeHeader(s.cfg.Realm, nonce))
		return
	}
	// Burn the nonce the client used (single-use; re-challenge if stale).
	if hdrs := req.GetHeaders("Authorization"); len(hdrs) > 0 {
		if g, ok := hdrs[0].(*sip.GenericHeader); ok {
			if auth := sip.AuthFromValue(g.Contents); auth != nil {
				if !s.nonces.consume(auth.Nonce(), u.Username) {
					// Stale nonce (e.g. retry after re-challenge) — ask again.
					nonce := s.nonces.issue(u.Username)
					_ = s.respondRaw(req, 401, "Stale nonce", "", challengeHeader(s.cfg.Realm, nonce))
					return
				}
			}
		}
	}

	expires := 300
	if v := headerInt(req, "Expires"); v > 0 {
		expires = v
	}
	if expires > 3600 {
		expires = 3600
	}
	if expires == 0 {
		s.reg.remove(u.Username) // unregister
	} else {
		s.reg.put(u.Username, req.Source(), time.Duration(expires)*time.Second)
	}
	_ = s.respondRaw(req, 200, "OK", "")
	s.log.Info("doorbell REGISTER", "user", u.Username, "source", req.Source(), "expires", expires)
}

func (s *Server) handleInvite(req sip.Request, tx sip.ServerTransaction) {
	if !s.lanAllowed(req) {
		_ = s.respondRaw(req, 403, "Forbidden", "")
		return
	}
	user, ok := s.fromUser(req)
	if !ok {
		_ = s.respondRaw(req, 403, "Forbidden", "")
		return
	}
	u, configured := s.cfg.user(user)
	if !configured {
		// Hard requirement: no anonymous INVITE, ever — 403 before SDP.
		_ = s.respondRaw(req, 403, "Forbidden", "")
		return
	}
	sourceIP := hostOf(req.Source())
	if s.cfg.Outbound == nil {
		// Mode A: the INVITE must come from a live registration of this
		// user, from the same source IP that registered.
		if !s.reg.registered(u.Username, sourceIP) {
			_ = s.respondRaw(req, 403, "Not registered", "")
			return
		}
	} else if sourceIP != s.outboundHost() {
		// Mode B (PBX): calls arrive through the PBX we authenticated to —
		// an INVITE from anywhere else is not that PBX, refuse it.
		_ = s.respondRaw(req, 403, "Forbidden", "")
		return
	}

	offer, ok := parseAudioOffer(req.Body())
	if !ok {
		_ = s.respondRaw(req, 488, "Not Acceptable Here", "")
		return
	}

	sipCallID := ""
	if id, ok := req.CallID(); ok && id != nil {
		sipCallID = id.String()
	}
	call := s.calls.newCall(req, tx, sipCallID, u, offer)

	_ = s.respondRaw(req, 180, "Ringing", "")

	// Snapshot the linked camera before ringing the UI, with a hard bound —
	// a slow camera must not delay the popup.
	if s.snap != nil && u.CameraID != "" {
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if path, err := s.snap.RunSnapshot(sctx, u.CameraID, "doorbell"); err == nil {
			call.SnapshotPath = path
		} else {
			s.log.Warn("doorbell ring snapshot failed", "camera_id", u.CameraID, "error", err)
		}
		cancel()
	}

	s.publish(event.TopicDoorbellRing, call, "")
	s.log.Info("doorbell ring", "call_id", call.ID, "user", u.Username, "source", req.Source())
}

func (s *Server) handleBye(req sip.Request, tx sip.ServerTransaction) {
	sipCallID := ""
	if id, ok := req.CallID(); ok && id != nil {
		sipCallID = id.String()
	}
	if c := s.calls.bySIPCallID(sipCallID); c != nil {
		_ = s.respond(c, 200, "OK", "")
		c.end("bye")
		s.publish(event.TopicDoorbellEnded, c, "bye")
		return
	}
	_ = s.respondRaw(req, 481, "Call/Transaction Does Not Exist", "")
}

func (s *Server) handleCancel(req sip.Request, tx sip.ServerTransaction) {
	_ = s.respondRaw(req, 200, "OK", "")
	sipCallID := ""
	if id, ok := req.CallID(); ok && id != nil {
		sipCallID = id.String()
	}
	if c := s.calls.bySIPCallID(sipCallID); c != nil {
		_ = s.respond(c, 487, "Request Terminated", "")
		c.end("cancel")
		s.publish(event.TopicDoorbellEnded, c, "cancel")
	}
}

func (s *Server) handleOptions(req sip.Request, tx sip.ServerTransaction) {
	_ = s.respondRaw(req, 200, "OK", "")
}

func (s *Server) handleAck(req sip.Request, tx sip.ServerTransaction) {
	// ACK to our 200 OK closes the INVITE transaction; media is already up.
}

// ── helpers ──

// respond answers on a stored call's INVITE (retx-safe through the saved tx).
func (s *Server) respond(c *Call, status sip.StatusCode, reason, body string) error {
	c.mu.Lock()
	req := c.req
	c.mu.Unlock()
	return s.respondRaw(req, status, reason, body)
}

func (s *Server) respondRaw(req sip.Request, status sip.StatusCode, reason, body string, extra ...sip.Header) error {
	if s.srv == nil || req == nil {
		return fmt.Errorf("doorbell: server stopped")
	}
	var headers []sip.Header
	if body != "" {
		ct := sip.ContentType("application/sdp")
		headers = append(headers, &ct)
	}
	headers = append(headers, extra...)
	_, err := s.srv.RespondOnRequest(req, status, reason, body, headers)
	return err
}

// sendBYE builds and sends a BYE for an answered call.
func (s *Server) sendBYE(c *Call) {
	c.mu.Lock()
	req := c.req
	c.mu.Unlock()
	if req == nil {
		return
	}
	from, okF := req.From()
	to, okT := req.To()
	if !okF || !okT || from == nil || to == nil || req.Recipient() == nil {
		return
	}
	_, host, port, _ := splitListen(s.cfg.Listen)
	_ = host
	portVal := sip.Port(port)
	callIDVal := sip.CallID(c.sipCallID)
	seq := uint(1)
	if cs, ok := req.CSeq(); ok && cs != nil {
		seq = uint(cs.SeqNo) + 1
	}
	rb := sip.NewRequestBuilder()
	rb.SetMethod(sip.BYE)
	rb.SetFrom(&sip.Address{DisplayName: from.DisplayName, Uri: from.Address})
	rb.SetTo(&sip.Address{DisplayName: to.DisplayName, Uri: to.Address})
	rb.SetCallID(&callIDVal)
	rb.SetRecipient(req.Recipient())
	rb.SetHost(s.localHostFor(c.offer.Address))
	rb.AddVia(&sip.ViaHop{Host: s.localHostFor(c.offer.Address), Port: &portVal, Params: sip.NewParams().Add("branch", sip.String{Str: sip.GenerateBranch()})})
	rb.SetSeqNo(seq)
	bye, err := rb.Build()
	if err != nil {
		s.log.Warn("doorbell BYE build failed", "error", err)
		return
	}
	if _, err := s.srv.Request(bye); err != nil {
		s.log.Warn("doorbell BYE send failed", "error", err)
	}
}

// widenWildcardListen expands ":5070" to "0.0.0.0:5070". gosip's
// FillTargetHostAndPort fills an empty host with 127.0.0.1 — without this,
// the documented wildcard listen silently binds the registrar to loopback
// only and no LAN door station can ever reach it.
func widenWildcardListen(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen // bare port/malformed: let gosip surface the error
	}
	if strings.TrimSpace(host) == "" {
		return net.JoinHostPort("0.0.0.0", port)
	}
	return listen
}

// outboundHost is the PBX host we registered to (mode B) — the only source
// an INVITE may come from in that mode.
func (s *Server) outboundHost() string {
	if s.cfg.Outbound == nil {
		return ""
	}
	server := strings.TrimPrefix(s.cfg.Outbound.Server, "sip:")
	if h, _, err := net.SplitHostPort(server); err == nil {
		return h
	}
	return server
}

// fromUser extracts the From user.
func (s *Server) fromUser(req sip.Request) (string, bool) {
	from, okF := req.From()
	if !okF || from == nil {
		return "", false
	}
	user := from.Address.User()
	if user == nil || user.String() == "" {
		return "", false
	}
	return user.String(), true
}

// lanAllowed enforces the default "never exposed to WAN" posture: with
// lan_only (default), SIP from a non-private source IP is refused outright.
func (s *Server) lanAllowed(req sip.Request) bool {
	if !s.cfg.LANOnly {
		return true
	}
	ip := net.ParseIP(hostOf(req.Source()))
	if ip == nil {
		return false
	}
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
}

func (s *Server) publish(topic string, c *Call, reason string) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(context.Background(), topic, event.DoorbellEvent{
		CallID:       c.ID,
		User:         c.User,
		Name:         c.Name,
		CameraID:     c.CameraID,
		SnapshotPath: c.SnapshotPath,
		Reason:       reason,
		Timestamp:    time.Now(),
	})
}

// headerInt extracts an integer header value (Expires & friends) tolerantly.
func headerInt(req sip.Request, name string) int {
	for _, h := range req.GetHeaders(name) {
		if g, ok := h.(*sip.GenericHeader); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(g.Contents)); err == nil {
				return n
			}
		}
	}
	return 0
}
