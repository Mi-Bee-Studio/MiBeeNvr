// Package cascade implements the GB/T 28181 LOWER-LEVEL platform role: the
// NVR registers to an upper-level platform (SIP UAC), answers its catalog
// queries with an aggregated view of all local cameras, and on the upper
// platform's INVITE forwards the camera's stream as RTP/PS (via psmux).
//
// The upper platform needs no cascade-specific support — any GB/T 28181
// platform implementation (including this NVR's own platform role) can be the
// upper side: REGISTER / Catalog Query / INVITE are all standard.
package cascade

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181/manscdp"
	mbsip "github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181/sip"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/ghettovoice/gosip"
	"github.com/ghettovoice/gosip/sip"
)

// CameraInfo is the cascade's view of a local camera.
type CameraInfo struct {
	ID    string
	Name  string
	Brand string
	Model string
	// Encoding is the camera's configured codec ("h264"|"h265"|"" unknown) —
	// the preferred PSM type; sniffed from the first NAL when empty.
	Encoding string
}

// CameraSource supplies the local camera list and their stream hubs. The
// camera manager adapts to it (pkg/app wiring).
type CameraSource interface {
	Cameras() []CameraInfo
	Hub(cameraID string) *model.StreamHub
}

// Service is the cascade client (pkg/app.Service "gb28181-cascade").
type Service struct {
	cfg config.GB28181CascadeConfig
	src CameraSource
	db  *storage.DB

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	srv gosip.Server

	sn atomic.Int64 // MANSCDP sequence numbers

	mu         sync.Mutex
	sessions   map[string]*mediaSession // SIP Call-ID → active forward
	online     bool
	regTS      time.Time
	ptzForward PTZForwarder
}

func New(cfg config.GB28181CascadeConfig, src CameraSource, db *storage.DB) *Service {
	return &Service{cfg: cfg, src: src, db: db, sessions: make(map[string]*mediaSession)}
}

func (s *Service) Name() string { return "gb28181-cascade" }

func (s *Service) Start(ctx context.Context) error {
	s.ctx, s.cancel = context.WithCancel(context.Background())

	listen := s.cfg.SIPListen
	if listen == "" {
		listen = ":5061"
	}
	srv, err := newSIPServer(listen)
	if err != nil {
		return fmt.Errorf("gb28181-cascade: SIP listen %s: %w", listen, err)
	}
	s.srv = srv
	_ = srv.OnRequest(sip.MESSAGE, s.onMessage)
	_ = srv.OnRequest(sip.INVITE, s.onInvite)
	_ = srv.OnRequest(sip.BYE, s.onBye)
	// ACK completes the upper platform's INVITE dialog. gosip logs "SIP
	// request handler not found" for unregistered methods; a no-op handler
	// keeps the transaction layer quiet (dialog state lives in the media
	// sessions map, keyed by Call-ID).
	_ = srv.OnRequest(sip.ACK, func(_ sip.Request, _ sip.ServerTransaction) {})
	// The upper platform may SUBSCRIBE to catalog changes; cascade v1 does
	// not push NOTIFYs — answer 200 with Expires 0 so the upper falls back to
	// its periodic catalog polling instead of retry-storming.
	_ = srv.OnRequest(sip.SUBSCRIBE, func(req sip.Request, _ sip.ServerTransaction) {
		zero := sip.Expires(0)
		_, _ = srv.RespondOnRequest(req, 200, "OK", "", []sip.Header{&zero})
	})
	_ = srv.OnRequest(sip.OPTIONS, func(req sip.Request, tx sip.ServerTransaction) {
		allow := sip.AllowHeader{sip.REGISTER, sip.MESSAGE, sip.INVITE, sip.ACK, sip.BYE, sip.CANCEL, sip.OPTIONS}
		_, _ = srv.RespondOnRequest(req, 200, "OK", "", []sip.Header{&allow})
	})

	s.wg.Add(1)
	go s.registerLoop()
	slog.Info("gb28181-cascade: started",
		"listen", listen, "upper", s.cfg.ServerAddr, "device", s.cfg.LocalDeviceID)
	return nil
}

func (s *Service) Stop() error {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()

	// Best-effort unregister (Expires 0) and BYE of active forwards.
	s.mu.Lock()
	sessions := make([]*mediaSession, 0, len(s.sessions))
	for _, ms := range s.sessions {
		sessions = append(sessions, ms)
	}
	s.sessions = make(map[string]*mediaSession)
	s.online = false
	s.mu.Unlock()
	for _, ms := range sessions {
		ms.stop()
	}
	if s.srv != nil {
		_ = s.sendRegister(0)
		s.srv.Shutdown()
	}
	slog.Info("gb28181-cascade: stopped")
	return nil
}

// ---- registration & keepalive ----

func (s *Service) registerLoop() {
	defer s.wg.Done()
	expires := s.cfg.RegisterExpires
	if expires <= 0 {
		expires = 3600
	}
	for {
		if s.ctx.Err() != nil {
			return
		}
		if err := s.sendRegister(expires); err != nil {
			slog.Warn("gb28181-cascade: register failed, retrying", "error", err)
			s.setOnline(false)
			if !sleepCtx(s.ctx, 15*time.Second) {
				return
			}
			continue
		}
		s.setOnline(true)
		// Keepalive cadence while registered.
		hb := 60 * time.Second
		if d, err := time.ParseDuration(s.cfg.HeartbeatInterval); err == nil && d > 0 {
			hb = d
		}
		reRegister := time.Duration(expires)*8/10*time.Second - hb
		for i := time.Duration(0); i < reRegister; i += hb {
			if !sleepCtx(s.ctx, hb) {
				return
			}
			if err := s.sendKeepalive(); err != nil {
				// A keepalive failure usually means the upper platform
				// restarted (403 Device not registered) or vanished —
				// re-REGISTER immediately instead of waiting out the
				// Expires window.
				slog.Warn("gb28181-cascade: keepalive failed — re-registering", "error", err)
				s.setOnline(false)
				break
			}
		}
	}
}

func (s *Service) setOnline(v bool) {
	s.mu.Lock()
	changed := s.online != v
	s.online = v
	if v {
		s.regTS = time.Now()
	}
	s.mu.Unlock()
	if changed {
		state := "offline"
		if v {
			state = "online"
		}
		slog.Info("gb28181-cascade: registration state", "state", state)
	}
}

// Online reports the registration state (diagnostics).
func (s *Service) Online() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.online
}

// RegistrationSince returns how long the current registration has been up
// (ok=false when offline).
func (s *Service) RegistrationSince() (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.online {
		return 0, false
	}
	return time.Since(s.regTS), true
}

// ForwardCount returns the number of active media forwards (INVITE dialogs
// currently streaming to the upper platform).
func (s *Service) ForwardCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

func (s *Service) upperAddr() (*net.UDPAddr, error) {
	return net.ResolveUDPAddr("udp", s.cfg.ServerAddr)
}

// buildCoreRequest assembles a REGISTER/MESSAGE request toward the upper
// platform on the cascade's own SIP listening port.
func (s *Service) buildCoreRequest(method sip.RequestMethod, localHost string, localPort int, body, contentType string) (sip.Request, error) {
	dst, err := s.upperAddr()
	if err != nil {
		return nil, err
	}
	port := sip.Port(localPort)
	rb := sip.NewRequestBuilder()
	rb.SetMethod(method)
	rb.SetFrom(&sip.Address{
		Uri:    &sip.SipUri{FUser: sip.String{Str: s.cfg.LocalDeviceID}, FHost: localHost, FPort: &port},
		Params: sip.NewParams().Add("tag", sip.String{Str: sip.GenerateBranch()}),
	})
	rb.SetTo(&sip.Address{
		Uri: &sip.SipUri{FUser: sip.String{Str: s.cfg.ServerDomain}, FHost: dst.IP.String()},
	})
	dstPort := sip.Port(dst.Port)
	rb.SetRecipient(&sip.SipUri{FUser: sip.String{Str: s.cfg.ServerDomain}, FHost: dst.IP.String(), FPort: &dstPort})
	rb.SetHost(localHost)
	rb.SetContact(&sip.Address{
		Uri: &sip.SipUri{FUser: sip.String{Str: s.cfg.LocalDeviceID}, FHost: localHost, FPort: &port},
	})
	rb.AddVia(&sip.ViaHop{
		Host: localHost,
		Port: &port,
		Params: sip.NewParams().
			Add("branch", sip.String{Str: sip.GenerateBranch()}).
			Add("rport", sip.String{}),
	})
	rb.SetSeqNo(1)
	if contentType != "" {
		ct := sip.ContentType(contentType)
		rb.SetContentType(&ct)
	}
	if body != "" {
		rb.SetBody(body)
	}
	return rb.Build()
}

func (s *Service) localHostPort() (string, int) {
	listen := s.cfg.SIPListen
	if listen == "" {
		listen = ":5061"
	}
	if dst, err := s.upperAddr(); err == nil {
		// Route via the interface that reaches the upper platform.
		if conn, err := net.DialUDP("udp", nil, dst); err == nil {
			defer func() { _ = conn.Close() }()
			if local, ok := conn.LocalAddr().(*net.UDPAddr); ok {
				host := local.IP.String()
				if host == "::" || host == "" {
					host = "127.0.0.1"
				}
				_, portStr, _ := net.SplitHostPort(listen)
				p, _ := strconv.Atoi(portStr)
				if p == 0 {
					p = 5061
				}
				return host, p
			}
		}
	}
	return "127.0.0.1", 5061
}

// sendRegister performs the REGISTER + digest challenge round. expires=0
// unregisters.
func (s *Service) sendRegister(expires int) error {
	if s.srv == nil {
		return fmt.Errorf("not started")
	}
	host, port := s.localHostPort()
	req, err := s.buildCoreRequest(sip.REGISTER, host, port, "", "")
	if err != nil {
		return err
	}
	exp := sip.Expires(uint32(expires))
	req.AppendHeader(&exp)

	resp, err := s.request(req)
	if err != nil {
		return err
	}
	if resp.StatusCode() == 401 {
		auth, err2 := s.digestFrom(resp, req)
		if err2 != nil {
			return err2
		}
		req2, err2 := s.buildCoreRequest(sip.REGISTER, host, port, "", "")
		if err2 != nil {
			return err2
		}
		exp2 := sip.Expires(uint32(expires))
		req2.AppendHeader(&exp2)
		req2.AppendHeader(auth)
		resp, err = s.request(req2)
		if err != nil {
			return err
		}
	}
	if !resp.IsSuccess() {
		return fmt.Errorf("register: status %d (%s)", resp.StatusCode(), resp.Reason())
	}
	return nil
}

var challengeRe = regexp.MustCompile(`(\w+)\s*=\s*"([^"]+)"`)

// digestFrom computes the Authorization header for a 401 challenge.
func (s *Service) digestFrom(resp sip.Response, origReq sip.Request) (sip.Header, error) {
	var hdrVal string
	for _, h := range resp.GetHeaders("WWW-Authenticate") {
		if g, ok := h.(*sip.GenericHeader); ok {
			hdrVal = g.Contents
			break
		}
	}
	if hdrVal == "" {
		return nil, fmt.Errorf("401 without WWW-Authenticate")
	}
	vals := map[string]string{}
	for _, m := range challengeRe.FindAllStringSubmatch(hdrVal, -1) {
		vals[m[1]] = m[2]
	}
	realm := vals["realm"]
	if realm == "" {
		realm = s.cfg.Realm
	}
	nonce := vals["nonce"]
	if nonce == "" {
		return nil, fmt.Errorf("challenge without nonce")
	}

	uri := fmt.Sprintf("sip:%s@%s", s.cfg.ServerDomain, addrHost(s.cfg.ServerAddr))
	ha1 := md5hex(s.cfg.LocalDeviceID, realm, s.cfg.Password)
	ha2 := md5hex("REGISTER", uri)
	response := md5hex(ha1, nonce, ha2)

	value := fmt.Sprintf(`Digest realm="%s",algorithm=MD5,nonce="%s",username="%s",uri="%s",response="%s"`,
		realm, nonce, s.cfg.LocalDeviceID, uri, response)
	return &sip.GenericHeader{HeaderName: "Authorization", Contents: value}, nil
}

func md5hex(parts ...string) string {
	h := md5.New() //nolint:gosec // GB28181 digest mandates MD5
	_, _ = h.Write([]byte(strings.Join(parts, ":")))
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Service) request(req sip.Request) (sip.Response, error) {
	tx, err := s.srv.Request(req)
	if err != nil {
		return nil, err
	}
	responses := tx.Responses()
	defer tx.Done()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case resp, ok := <-responses:
			if !ok {
				return nil, fmt.Errorf("no response")
			}
			if resp.IsProvisional() {
				continue
			}
			return resp, nil
		case <-time.After(8 * time.Second):
			return nil, fmt.Errorf("timeout")
		case <-s.ctx.Done():
			return nil, s.ctx.Err()
		}
	}
}

func (s *Service) sendKeepalive() error {
	body, err := manscdp.Encode(manscdp.Keepalive{
		CmdType:  manscdp.CmdKeepalive,
		SN:       int(s.sn.Add(1)),
		DeviceID: s.cfg.LocalDeviceID,
		Status:   "OK",
	})
	if err != nil {
		return err
	}
	host, port := s.localHostPort()
	req, err := s.buildCoreRequest(sip.MESSAGE, host, port, string(body), "Application/MANSCDP+xml")
	if err != nil {
		return err
	}
	resp, err := s.request(req)
	if err != nil {
		return err
	}
	if !resp.IsSuccess() {
		return fmt.Errorf("keepalive: status %d", resp.StatusCode())
	}
	return nil
}

// ---- upper-platform requests (UAS side) ----

func (s *Service) onMessage(req sip.Request, _ sip.ServerTransaction) {
	cmd, payload, err := manscdp.Decode([]byte(req.Body()))
	if err != nil {
		_, _ = s.srv.RespondOnRequest(req, 400, "Bad MANSCDP", "", nil)
		return
	}
	_, _ = s.srv.RespondOnRequest(req, 200, "OK", "", nil)
	switch cmd {
	case manscdp.CmdCatalog:
		// Queries (root <Query>) come from the upper platform; Response-root
		// Catalogs are other devices' answers and never reach the cascade.
		if q, ok := payload.(manscdp.CatalogQuery); ok && q.SN > 0 {
			s.answerCatalog(q.SN)
		}
	case manscdp.CmdDeviceInfo:
		if d, ok := payload.(manscdp.DeviceInfo); ok && d.SN > 0 {
			s.answerDeviceInfo(d.SN)
		}
	case manscdp.CmdRecordInfo:
		// Root <Query> carries CmdType RecordInfo (decoded as
		// RecordInfoQuery); the Response-root form is a device answer that
		// never reaches the cascade.
		if q, ok := payload.(manscdp.RecordInfoQuery); ok && q.SN > 0 {
			go s.answerRecordInfo(q)
		}
	case manscdp.CmdDeviceControl:
		if dc, ok := payload.(manscdp.DeviceControl); ok {
			go s.forwardDeviceControl(dc)
		}
	}
}

func (s *Service) answerCatalog(sn int) {
	items, err := s.catalogItems()
	if err != nil {
		slog.Warn("gb28181-cascade: catalog build failed", "error", err)
		return
	}
	body, err := manscdp.Encode(manscdp.Catalog{
		CmdType:  manscdp.CmdCatalog,
		SN:       sn,
		DeviceID: s.cfg.LocalDeviceID,
		SumNum:   len(items),
		Item:     items,
	})
	if err != nil {
		return
	}
	if err := s.sendMessageBody(body, "Application/MANSCDP+xml"); err != nil {
		slog.Warn("gb28181-cascade: catalog response failed", "channels", len(items), "error", err)
	} else {
		slog.Info("gb28181-cascade: catalog response sent", "channels", len(items))
	}
}

func (s *Service) answerDeviceInfo(sn int) {
	body, err := manscdp.Encode(manscdp.DeviceInfo{
		CmdType:      manscdp.CmdDeviceInfo,
		SN:           sn,
		DeviceID:     s.cfg.LocalDeviceID,
		DeviceName:   "MiBee NVR",
		Manufacturer: "MiBee",
		Model:        "MiBeeNvr",
	})
	if err == nil {
		if err := s.sendMessageBody(body, "Application/MANSCDP+xml"); err != nil {
			slog.Warn("gb28181-cascade: deviceinfo response failed", "error", err)
		}
	}
}

func (s *Service) sendMessageBody(body []byte, contentType string) error {
	host, port := s.localHostPort()
	req, err := s.buildCoreRequest(sip.MESSAGE, host, port, string(body), contentType)
	if err != nil {
		return err
	}
	resp, err := s.request(req)
	if err != nil {
		return err
	}
	if !resp.IsSuccess() {
		return fmt.Errorf("message: status %d", resp.StatusCode())
	}
	return nil
}

// addrHost extracts the host part of host:port.
func addrHost(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return strings.TrimSpace(addr)
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// newSIPServer builds a gosip UDP server bound to listen (":5061") — the
// same construction the platform-role server uses.
func newSIPServer(listen string) (gosip.Server, error) {
	host, portStr, err := net.SplitHostPort(listen)
	if err != nil || portStr == "" {
		return nil, fmt.Errorf("invalid listen %q", listen)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, err
	}
	if host == "" {
		host = "0.0.0.0"
	}
	srv := gosip.NewServer(gosip.ServerConfig{
		Host:      host,
		UserAgent: "MiBeeNvr-GB28181-Cascade/1.0",
	}, nil, nil, mbsip.SlogLogger(slog.Default().With("component", "gb28181_cascade")))
	if err := srv.Listen("UDP", net.JoinHostPort(host, strconv.Itoa(port))); err != nil {
		return nil, err
	}
	return srv, nil
}
