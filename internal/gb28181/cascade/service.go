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
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
	"github.com/ghettovoice/gosip"
	"github.com/ghettovoice/gosip/sip"
	"github.com/mickeyzzc/gb28181-go/manscdp"
	gbsip "github.com/mickeyzzc/gb28181-go/platform/sip"
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
	// SubStream selects the camera's low-res tier for forwarding when true
	// (#512): the INVITE acquires the on-demand sub-stream instead of the
	// main hub, falling back to main when unavailable.
	SubStream bool
	// CascadeHidden excludes the camera from the aggregated catalog and makes
	// INVITEs for its channel fail with 404 (catalog convergence: expose only
	// a chosen subset to the upper platform).
	CascadeHidden bool
}

// CameraSource supplies the local camera list and their stream hubs. The
// camera manager adapts to it (pkg/app wiring).
type CameraSource interface {
	Cameras() []CameraInfo
	Hub(cameraID string) *streamhub.StreamHub
}

// SubStreamAcquirer grants the cascade access to the on-demand sub-stream
// tier (#513): one INVITE holds one reference for its lifetime. Nil (or an
// error) falls back to main-stream forwarding.
type SubStreamAcquirer interface {
	AcquireSubHub(ctx context.Context, cameraID string) (hub *streamhub.StreamHub, release func(), err error)
}

// upper is one upper-platform registration session (#370): its own REGISTER /
// keepalive loop and online state over the shared SIP listener. The single
// legacy config form becomes uppers[0]; gb28181_cascade.upstreams appends
// more.
type upper struct {
	cfg    config.GB28181CascadeUpstream // resolved — defaults filled in
	online bool
	regTS  time.Time
}

// Service is the cascade client (pkg/app.Service "gb28181-cascade").
type Service struct {
	cfg config.GB28181CascadeConfig
	src CameraSource
	db  *storage.DB
	// subAcq serves sub-stream forwardings (#512); nil = main-only.
	subAcq SubStreamAcquirer

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	srv gosip.Server

	sn atomic.Int64 // MANSCDP sequence numbers

	uppers []*upper // #370: one entry per upper platform

	mu         sync.Mutex
	sessions   map[string]*mediaSession    // SIP Call-ID → active live forward
	playbacks  map[string]*playbackSession // SIP Call-ID → active playback dialog
	subs       map[string]*catalogSub      // catalog subscriptions (SUBSCRIBE → NOTIFY, #370)
	ptzForward PTZForwarder
	gbLoc      *time.Location // GB naive-clock zone (nil → time.Local)
}

// buildUppers resolves the configured upper platforms: the legacy single form
// (ServerAddr non-empty) first, then every upstreams[] entry with unset
// fields inherited from the single form.
func buildUppers(cfg config.GB28181CascadeConfig) []*upper {
	var uppers []*upper
	if cfg.ServerAddr != "" {
		uppers = append(uppers, &upper{cfg: config.GB28181CascadeUpstream{
			ServerDomain:      cfg.ServerDomain,
			ServerAddr:        cfg.ServerAddr,
			LocalDeviceID:     cfg.LocalDeviceID,
			Realm:             cfg.Realm,
			Password:          cfg.Password,
			HeartbeatInterval: cfg.HeartbeatInterval,
			RegisterExpires:   cfg.RegisterExpires,
		}})
	}
	for _, u := range cfg.Upstreams {
		if u.ServerAddr == "" {
			continue
		}
		if u.LocalDeviceID == "" {
			u.LocalDeviceID = cfg.LocalDeviceID
		}
		if u.Realm == "" {
			u.Realm = cfg.Realm
		}
		if u.Password == "" {
			u.Password = cfg.Password
		}
		if u.HeartbeatInterval == "" {
			u.HeartbeatInterval = cfg.HeartbeatInterval
		}
		if u.RegisterExpires == 0 {
			u.RegisterExpires = cfg.RegisterExpires
		}
		uppers = append(uppers, &upper{cfg: u})
	}
	return uppers
}

// SetSubStreamAcquirer wires the on-demand sub-stream provider (#512). Call
// once at wiring time, before Start.
func (s *Service) SetSubStreamAcquirer(a SubStreamAcquirer) { s.subAcq = a }

func New(cfg config.GB28181CascadeConfig, src CameraSource, db *storage.DB) *Service {
	return &Service{
		cfg: cfg, src: src, db: db,
		uppers:    buildUppers(cfg),
		sessions:  make(map[string]*mediaSession),
		playbacks: make(map[string]*playbackSession),
		subs:      make(map[string]*catalogSub),
	}
}

// SetGBTimezone pins the zone used for GB/T 28181 naive timestamps (RecordInfo
// query parsing / response formatting). Deployments whose host clock zone
// differs from the devices' (e.g. a UTC container cascading into CST cameras)
// set this to the devices' zone.
func (s *Service) SetGBTimezone(loc *time.Location) {
	if loc != nil {
		s.gbLoc = loc
	}
}

// gbTZ returns the effective GB naive-clock zone.
func (s *Service) gbTZ() *time.Location {
	if s.gbLoc != nil {
		return s.gbLoc
	}
	return time.Local
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
	// The upper platform may SUBSCRIBE to catalog changes — catalog NOTIFYs
	// push channel additions/removals without waiting for the upper's
	// polling fallback (#370).
	_ = srv.OnRequest(sip.SUBSCRIBE, s.onSubscribe)
	_ = srv.OnRequest(sip.OPTIONS, func(req sip.Request, tx sip.ServerTransaction) {
		allow := sip.AllowHeader{sip.REGISTER, sip.MESSAGE, sip.INVITE, sip.ACK, sip.BYE, sip.CANCEL, sip.OPTIONS}
		_, _ = srv.RespondOnRequest(req, 200, "OK", "", []sip.Header{&allow})
	})
	// MANSRTSP playback controls (pause/resume/seek/scale) ride in-dialog INFO
	// messages; onInfo routes them to the channel's playback session.
	_ = srv.OnRequest(sip.INFO, s.onInfo)

	if len(s.uppers) == 0 {
		return fmt.Errorf("gb28181-cascade: no upper platform configured (server_addr / upstreams)")
	}
	for _, u := range s.uppers {
		s.wg.Add(1)
		go s.registerLoop(u)
	}
	s.wg.Add(1)
	go s.catalogNotifyLoop()
	slog.Info("gb28181-cascade: started",
		"listen", listen, "uppers", len(s.uppers), "device", s.cfg.LocalDeviceID)
	return nil
}

func (s *Service) Stop() error {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()

	// Best-effort unregister (Expires 0) and BYE of active forwards/playbacks.
	s.mu.Lock()
	sessions := make([]*mediaSession, 0, len(s.sessions))
	for _, ms := range s.sessions {
		sessions = append(sessions, ms)
	}
	s.sessions = make(map[string]*mediaSession)
	playbacks := make([]*playbackSession, 0, len(s.playbacks))
	for _, ps := range s.playbacks {
		playbacks = append(playbacks, ps)
	}
	s.playbacks = make(map[string]*playbackSession)
	for _, u := range s.uppers {
		u.online = false
	}
	s.mu.Unlock()
	for _, ms := range sessions {
		ms.stop()
	}
	for _, ps := range playbacks {
		ps.stop()
	}
	if s.srv != nil {
		for _, u := range s.uppers {
			_ = s.sendRegister(u, 0)
		}
		s.srv.Shutdown()
	}
	slog.Info("gb28181-cascade: stopped")
	return nil
}

// ---- registration & keepalive ----

func (s *Service) registerLoop(u *upper) {
	defer s.wg.Done()
	expires := u.cfg.RegisterExpires
	if expires <= 0 {
		expires = 3600
	}
	for {
		if s.ctx.Err() != nil {
			return
		}
		if err := s.sendRegister(u, expires); err != nil {
			slog.Warn("gb28181-cascade: register failed, retrying",
				"upper", u.cfg.ServerAddr, "error", err)
			s.setOnline(u, false)
			if !sleepCtx(s.ctx, 15*time.Second) {
				return
			}
			continue
		}
		s.setOnline(u, true)
		// Keepalive cadence while registered.
		hb := 60 * time.Second
		if d, err := time.ParseDuration(u.cfg.HeartbeatInterval); err == nil && d > 0 {
			hb = d
		}
		reRegister := time.Duration(expires)*8/10*time.Second - hb
		for i := time.Duration(0); i < reRegister; i += hb {
			if !sleepCtx(s.ctx, hb) {
				return
			}
			if err := s.sendKeepalive(u); err != nil {
				// A keepalive failure usually means the upper platform
				// restarted (403 Device not registered) or vanished —
				// re-REGISTER immediately instead of waiting out the
				// Expires window.
				slog.Warn("gb28181-cascade: keepalive failed — re-registering",
					"upper", u.cfg.ServerAddr, "error", err)
				s.setOnline(u, false)
				break
			}
		}
	}
}

func (s *Service) setOnline(u *upper, v bool) {
	s.mu.Lock()
	changed := u.online != v
	u.online = v
	if v {
		u.regTS = time.Now()
	}
	s.mu.Unlock()
	if changed {
		state := "offline"
		if v {
			state = "online"
		}
		slog.Info("gb28181-cascade: registration state",
			"upper", u.cfg.ServerAddr, "state", state)
	}
}

// Online reports the registration state (diagnostics).
func (s *Service) Online() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.uppers {
		if u.online {
			return true
		}
	}
	return false
}

// RegistrationSince returns how long the OLDEST live registration has been
// up (ok=false when every upper is offline).
func (s *Service) RegistrationSince() (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var oldest time.Time
	for _, u := range s.uppers {
		if u.online && (oldest.IsZero() || u.regTS.Before(oldest)) {
			oldest = u.regTS
		}
	}
	if oldest.IsZero() {
		return 0, false
	}
	return time.Since(oldest), true
}

// ForwardCount returns the number of active media dialogs (live forwards +
// playback streams) currently sending to the upper platform.
func (s *Service) ForwardCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions) + len(s.playbacks)
}

func upperAddr(u *upper) (*net.UDPAddr, error) {
	return net.ResolveUDPAddr("udp", u.cfg.ServerAddr)
}

// upperOf resolves which upper platform an incoming request belongs to: the
// From user of a platform's requests is its server ID (ServerDomain). Falls
// back to the sole upper; unknown senders on a multi-upper deployment land
// on the first (their dialogs still route by Call-ID).
func (s *Service) upperOf(req sip.Request) *upper {
	if len(s.uppers) == 0 {
		return nil
	}
	if len(s.uppers) == 1 {
		return s.uppers[0]
	}
	if from, ok := req.From(); ok {
		user := from.Address.User().String()
		for _, u := range s.uppers {
			if u.cfg.ServerDomain == user {
				return u
			}
		}
	}
	return s.uppers[0]
}

// buildCoreRequest assembles a REGISTER/MESSAGE request toward the upper
// platform on the cascade's own SIP listening port.
func (s *Service) buildCoreRequest(u *upper, method sip.RequestMethod, localHost string, localPort int, body, contentType string) (sip.Request, error) {
	dst, err := upperAddr(u)
	if err != nil {
		return nil, err
	}
	port := sip.Port(localPort)
	rb := sip.NewRequestBuilder()
	rb.SetMethod(method)
	rb.SetFrom(&sip.Address{
		Uri:    &sip.SipUri{FUser: sip.String{Str: u.cfg.LocalDeviceID}, FHost: localHost, FPort: &port},
		Params: sip.NewParams().Add("tag", sip.String{Str: sip.GenerateBranch()}),
	})
	rb.SetTo(&sip.Address{
		Uri: &sip.SipUri{FUser: sip.String{Str: u.cfg.ServerDomain}, FHost: dst.IP.String()},
	})
	dstPort := sip.Port(dst.Port)
	rb.SetRecipient(&sip.SipUri{FUser: sip.String{Str: u.cfg.ServerDomain}, FHost: dst.IP.String(), FPort: &dstPort})
	rb.SetHost(localHost)
	rb.SetContact(&sip.Address{
		Uri: &sip.SipUri{FUser: sip.String{Str: u.cfg.LocalDeviceID}, FHost: localHost, FPort: &port},
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

func (s *Service) localHostPort(u *upper) (string, int) {
	listen := s.cfg.SIPListen
	if listen == "" {
		listen = ":5061"
	}
	if u == nil {
		return "127.0.0.1", 5061
	}
	if dst, err := upperAddr(u); err == nil {
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
func (s *Service) sendRegister(u *upper, expires int) error {
	if s.srv == nil {
		return fmt.Errorf("not started")
	}
	host, port := s.localHostPort(u)
	req, err := s.buildCoreRequest(u, sip.REGISTER, host, port, "", "")
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
		auth, err2 := s.digestFrom(u, resp, req)
		if err2 != nil {
			return err2
		}
		req2, err2 := s.buildCoreRequest(u, sip.REGISTER, host, port, "", "")
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
func (s *Service) digestFrom(u *upper, resp sip.Response, origReq sip.Request) (sip.Header, error) {
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
		realm = u.cfg.Realm
	}
	nonce := vals["nonce"]
	if nonce == "" {
		return nil, fmt.Errorf("challenge without nonce")
	}

	uri := fmt.Sprintf("sip:%s@%s", u.cfg.ServerDomain, addrHost(u.cfg.ServerAddr))
	ha1 := md5hex(u.cfg.LocalDeviceID, realm, u.cfg.Password)
	ha2 := md5hex("REGISTER", uri)
	response := md5hex(ha1, nonce, ha2)

	value := fmt.Sprintf(`Digest realm="%s",algorithm=MD5,nonce="%s",username="%s",uri="%s",response="%s"`,
		realm, nonce, u.cfg.LocalDeviceID, uri, response)
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

func (s *Service) sendKeepalive(u *upper) error {
	body, err := manscdp.Encode(manscdp.Keepalive{
		CmdType:  manscdp.CmdKeepalive,
		SN:       int(s.sn.Add(1)),
		DeviceID: u.cfg.LocalDeviceID,
		Status:   "OK",
	})
	if err != nil {
		return err
	}
	host, port := s.localHostPort(u)
	req, err := s.buildCoreRequest(u, sip.MESSAGE, host, port, string(body), "Application/MANSCDP+xml")
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
	u := s.upperOf(req)
	switch cmd {
	case manscdp.CmdCatalog:
		// Queries (root <Query>) come from the upper platform; Response-root
		// Catalogs are other devices' answers and never reach the cascade.
		if q, ok := payload.(manscdp.CatalogQuery); ok && q.SN > 0 {
			s.answerCatalog(u, q.SN)
		}
	case manscdp.CmdDeviceInfo:
		if d, ok := payload.(manscdp.DeviceInfo); ok && d.SN > 0 {
			s.answerDeviceInfo(u, d.SN)
		}
	case manscdp.CmdRecordInfo:
		// Root <Query> carries CmdType RecordInfo (decoded as
		// RecordInfoQuery); the Response-root form is a device answer that
		// never reaches the cascade.
		if q, ok := payload.(manscdp.RecordInfoQuery); ok && q.SN > 0 {
			go s.answerRecordInfo(u, q)
		}
	case manscdp.CmdDeviceControl:
		if dc, ok := payload.(manscdp.DeviceControl); ok {
			go s.forwardDeviceControl(dc)
		}
	}
}

func (s *Service) answerCatalog(u *upper, sn int) {
	items, err := s.catalogItems()
	if err != nil {
		slog.Warn("gb28181-cascade: catalog build failed", "error", err)
		return
	}
	body, err := manscdp.Encode(manscdp.Catalog{
		CmdType:  manscdp.CmdCatalog,
		SN:       sn,
		DeviceID: u.cfg.LocalDeviceID,
		SumNum:   len(items),
		Item:     items,
	})
	if err != nil {
		return
	}
	if err := s.sendMessageBodyTo(u, body, "Application/MANSCDP+xml"); err != nil {
		slog.Warn("gb28181-cascade: catalog response failed", "channels", len(items), "error", err)
	} else {
		slog.Info("gb28181-cascade: catalog response sent", "channels", len(items))
	}
}

func (s *Service) answerDeviceInfo(u *upper, sn int) {
	body, err := manscdp.Encode(manscdp.DeviceInfo{
		CmdType:      manscdp.CmdDeviceInfo,
		SN:           sn,
		DeviceID:     u.cfg.LocalDeviceID,
		DeviceName:   "MiBee NVR",
		Manufacturer: "MiBee",
		Model:        "MiBeeNvr",
	})
	if err == nil {
		if err := s.sendMessageBodyTo(u, body, "Application/MANSCDP+xml"); err != nil {
			slog.Warn("gb28181-cascade: deviceinfo response failed", "error", err)
		}
	}
}

func (s *Service) sendMessageBodyTo(u *upper, body []byte, contentType string) error {
	host, port := s.localHostPort(u)
	req, err := s.buildCoreRequest(u, sip.MESSAGE, host, port, string(body), contentType)
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
	}, nil, nil, gbsip.SlogLogger(slog.Default().With("component", "gb28181_cascade")))
	if err := srv.Listen("UDP", net.JoinHostPort(host, strconv.Itoa(port))); err != nil {
		return nil, err
	}
	return srv, nil
}
