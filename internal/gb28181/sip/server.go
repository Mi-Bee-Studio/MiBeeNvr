package sip

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181/manscdp"
	gosip "github.com/ghettovoice/gosip"
	"github.com/ghettovoice/gosip/log"
	"github.com/ghettovoice/gosip/sip"
)

// SIP status codes emitted by this server. gosip's sip package defines no
// named constants, so they are declared here.
const (
	statusOK           sip.StatusCode = 200
	statusBadRequest   sip.StatusCode = 400
	statusUnauthorized sip.StatusCode = 401
	statusForbidden    sip.StatusCode = 403
	statusBusyHere     sip.StatusCode = 486
)

// Server implements the GB/T 28181 SIP platform (UAS) side. It owns the gosip
// SIP stack lifecycle: UDP/TCP listening, REGISTER digest authentication, and
// keepalive/catalog/device-info MESSAGE handling. Media sessions (INVITE/BYE)
// are delegated to the hooks installed by the session manager.
type Server struct {
	cfg       config.GB28181ServerConfig
	deviceMgr *gb28181.DeviceManager

	gosipSrv gosip.Server
	cancel   context.CancelFunc

	mu      sync.Mutex
	started bool

	onInvite func(deviceID, channelID string) // Hook for SessionManager
	onBye    func(deviceID, channelID string) // Hook for SessionManager

	perDeviceMu map[string]*sync.Mutex // serialize SIP handling per device
}

// NewServer creates a GB28181 SIP server bound to the given config.
func NewServer(cfg config.GB28181ServerConfig, deviceMgr *gb28181.DeviceManager) *Server {
	return &Server{
		cfg:         cfg,
		deviceMgr:   deviceMgr,
		perDeviceMu: make(map[string]*sync.Mutex),
	}
}

// Name returns the service name (pkg/app.Service interface).
func (s *Server) Name() string {
	return "gb28181"
}

// Start launches the SIP stack. It is idempotent and returns promptly after
// the listeners are up.
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil
	}
	_, s.cancel = context.WithCancel(ctx)
	s.started = true
	s.mu.Unlock()

	host, port, err := parseSIPListen(s.cfg.SIPListen)
	if err != nil {
		return err
	}
	// gosip panics when Host is set to a non-IP value (e.g. a 20-digit
	// GB28181 server ID); an empty host binds all interfaces.
	if host != "" && net.ParseIP(host) == nil {
		return fmt.Errorf("gb28181: invalid SIP listen host %q", host)
	}

	logger := slogAdapter{slog.Default().With("component", "gb28181_sip")}
	srv := gosip.NewServer(gosip.ServerConfig{
		Host:      host,
		UserAgent: "MiBeeNvr-GB28181/1.0",
	}, nil, nil, logger)
	s.gosipSrv = srv

	_ = srv.OnRequest(sip.REGISTER, s.handleRegister)
	_ = srv.OnRequest(sip.MESSAGE, s.handleMessage)
	_ = srv.OnRequest(sip.INVITE, s.handleInvite)
	_ = srv.OnRequest(sip.BYE, s.handleBye)

	addr := net.JoinHostPort(host, strconv.Itoa(port))
	if err := srv.Listen("UDP", addr); err != nil {
		s.gosipSrv = nil
		return fmt.Errorf("gb28181: listen UDP %s: %w", addr, err)
	}
	if s.cfg.TCPMode {
		if err := srv.Listen("TCP", addr); err != nil {
			s.gosipSrv = nil
			return fmt.Errorf("gb28181: listen TCP %s: %w", addr, err)
		}
	}
	return nil
}

// Stop shuts down the SIP stack. It is idempotent.
func (s *Server) Stop() error {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return nil
	}
	s.started = false
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	srv := s.gosipSrv
	s.gosipSrv = nil
	s.mu.Unlock()

	if srv != nil {
		srv.Shutdown()
	}
	return nil
}

// SetInviteHook installs the INVITE handler used by the session manager.
func (s *Server) SetInviteHook(hook func(deviceID, channelID string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onInvite = hook
}

// SetByeHook installs the BYE handler used by the session manager.
func (s *Server) SetByeHook(hook func(deviceID, channelID string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onBye = hook
}

// SendMessage sends a SIP MESSAGE request carrying the given MANSCDP body to
// deviceID. It implements gb28181.MessageSender so the PTZ controller can push
// DeviceControl commands to a registered device.
func (s *Server) SendMessage(deviceID string, body []byte) error {
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
	devHost, devPortStr, err := net.SplitHostPort(netAddr)
	if err != nil {
		return fmt.Errorf("gb28181: invalid device address %q: %w", netAddr, err)
	}
	devPort, err := strconv.Atoi(devPortStr)
	if err != nil {
		return fmt.Errorf("gb28181: invalid device port %q: %w", devPortStr, err)
	}
	portVal := sip.Port(devPort)

	serverHost := s.localIP()

	from := &sip.Address{
		DisplayName: sip.String{Str: s.cfg.ServerID},
		Uri: &sip.SipUri{
			FUser: sip.String{Str: s.cfg.ServerID},
			FHost: serverHost,
		},
	}
	to := &sip.Address{
		DisplayName: sip.String{Str: deviceID},
		Uri: &sip.SipUri{
			FUser: sip.String{Str: deviceID},
			FHost: devHost,
			FPort: &portVal,
		},
	}
	recipient := &sip.SipUri{
		FUser: sip.String{Str: deviceID},
		FHost: devHost,
		FPort: &portVal,
	}

	rb := sip.NewRequestBuilder()
	rb.SetMethod(sip.MESSAGE)
	rb.SetFrom(from)
	rb.SetTo(to)
	rb.SetRecipient(recipient)
	rb.SetHost(serverHost)
	rb.AddVia(&sip.ViaHop{
		Host:   serverHost,
		Params: sip.NewParams().Add("branch", sip.String{Str: sip.GenerateBranch()}),
	})
	rb.SetSeqNo(1)
	ct := sip.ContentType("Application/MANSCDP+xml")
	rb.SetContentType(&ct)
	rb.SetBody(string(body))

	req, err := rb.Build()
	if err != nil {
		return fmt.Errorf("gb28181: build MESSAGE request: %w", err)
	}
	if _, err := srv.Request(req); err != nil {
		return fmt.Errorf("gb28181: send MESSAGE to %s: %w", deviceID, err)
	}
	return nil
}

// handleRegister processes a device REGISTER: digest challenge → validate →
// mark the device online (or unregister it when Expires is 0).
func (s *Server) handleRegister(req sip.Request, tx sip.ServerTransaction) {
	from, ok := req.From()
	if !ok {
		s.respond(req, tx, statusBadRequest, "Missing From header", nil)
		return
	}
	deviceID := from.Address.User().String()
	if deviceID == "" {
		s.respond(req, tx, statusBadRequest, "Missing device ID", nil)
		return
	}

	if !s.isAllowedDevice(deviceID) {
		s.respond(req, tx, statusForbidden, "Device not allowed", nil)
		return
	}

	if s.cfg.Password != "" {
		auth := s.getAuthHeader(req)
		if auth == nil {
			s.send401Challenge(req, tx)
			return
		}
		if !s.validateDigest(auth, deviceID, req) {
			s.respond(req, tx, statusForbidden, "Invalid credentials", nil)
			return
		}
	}

	// Serialize per device so REGISTER/keepalive ordering is preserved.
	mu := s.getDeviceMu(deviceID)
	mu.Lock()
	defer mu.Unlock()

	expires := 3600
	if h := s.requestExpires(req); h >= 0 {
		expires = h
	}

	if expires == 0 {
		s.deviceMgr.Unregister(deviceID)
	} else {
		s.deviceMgr.Register(&gb28181.Device{
			ID:      deviceID,
			NetAddr: req.Source(),
		})
	}

	exp := sip.Expires(expires)
	s.respond(req, tx, statusOK, "OK", []sip.Header{&exp})
}

// handleMessage processes device MESSAGE bodies (keepalive, catalog,
// device-info) via manscdp.
func (s *Server) handleMessage(req sip.Request, tx sip.ServerTransaction) {
	body := req.Body()
	if body == "" {
		s.respond(req, tx, statusBadRequest, "Empty body", nil)
		return
	}

	ct, payload, err := manscdp.Decode([]byte(body))
	if err != nil {
		s.respond(req, tx, statusBadRequest, "Invalid MANSCDP body", nil)
		return
	}

	switch ct {
	case manscdp.CmdKeepalive:
		p := payload.(manscdp.Keepalive)
		s.deviceMgr.Touch(p.DeviceID)
	case manscdp.CmdCatalog:
		p := payload.(manscdp.Catalog)
		for _, item := range p.Item {
			s.deviceMgr.RegisterChannel(p.DeviceID, &gb28181.Channel{
				ID:       item.DeviceID,
				Name:     item.Name,
				Parental: item.Parental,
				PTZType:  item.PTZType,
			})
		}
	case manscdp.CmdDeviceInfo:
		p := payload.(manscdp.DeviceInfo)
		if d, ok := s.deviceMgr.Device(p.DeviceID); ok {
			d.Mu.Lock()
			if p.DeviceName != "" {
				d.Name = p.DeviceName
			}
			if p.Manufacturer != "" {
				d.Manufacturer = p.Manufacturer
			}
			if p.Model != "" {
				d.Model = p.Model
			}
			d.Mu.Unlock()
		}
	}

	s.respond(req, tx, statusOK, "OK", nil)
}

// handleInvite delegates to the session manager hook, or rejects with 486
// when no hook is installed (media sessions not yet wired).
func (s *Server) handleInvite(req sip.Request, tx sip.ServerTransaction) {
	deviceID, channelID := s.requestIDs(req)
	s.mu.Lock()
	hook := s.onInvite
	s.mu.Unlock()
	if hook == nil {
		s.respond(req, tx, statusBusyHere, "Busy Here", nil)
		return
	}
	hook(deviceID, channelID)
}

// handleBye delegates to the session manager hook, or acks with 200 when none
// is installed.
func (s *Server) handleBye(req sip.Request, tx sip.ServerTransaction) {
	deviceID, channelID := s.requestIDs(req)
	s.mu.Lock()
	hook := s.onBye
	s.mu.Unlock()
	if hook == nil {
		s.respond(req, tx, statusOK, "OK", nil)
		return
	}
	hook(deviceID, channelID)
}

// requestIDs extracts the device ID (From user) and channel ID (To user) from
// an INVITE/BYE request.
func (s *Server) requestIDs(req sip.Request) (deviceID, channelID string) {
	if from, ok := req.From(); ok {
		deviceID = from.Address.User().String()
	}
	if to, ok := req.To(); ok {
		channelID = to.Address.User().String()
	}
	return deviceID, channelID
}

// isAllowedDevice reports whether deviceID may register. An empty allowlist
// permits any device.
func (s *Server) isAllowedDevice(deviceID string) bool {
	if len(s.cfg.AllowedDeviceIDs) == 0 {
		return true
	}
	for _, id := range s.cfg.AllowedDeviceIDs {
		if id == deviceID {
			return true
		}
	}
	return false
}

// send401Challenge replies with a Digest challenge (RFC 3261 § 22.2).
func (s *Server) send401Challenge(req sip.Request, tx sip.ServerTransaction) {
	realm := s.cfg.Realm
	if realm == "" {
		realm = "gb28181"
	}
	value := fmt.Sprintf(`Digest realm="%s", nonce="%s", algorithm=MD5, qop="auth"`, realm, generateNonce())
	headers := []sip.Header{&sip.GenericHeader{HeaderName: "WWW-Authenticate", Contents: value}}
	s.respond(req, tx, statusUnauthorized, "Unauthorized", headers)
}

// getAuthHeader extracts the Authorization header from a request. gosip's
// parser produces a GenericHeader for Authorization, so it is re-parsed.
func (s *Server) getAuthHeader(req sip.Request) *sip.Authorization {
	for _, h := range req.GetHeaders("Authorization") {
		if gh, ok := h.(*sip.GenericHeader); ok {
			return sip.AuthFromValue(gh.Contents)
		}
	}
	return nil
}

// validateDigest verifies the digest response against the configured password.
func (s *Server) validateDigest(auth *sip.Authorization, deviceID string, req sip.Request) bool {
	if auth == nil || auth.Username() != deviceID {
		return false
	}
	auth.SetPassword(s.cfg.Password)
	auth.SetMethod(string(req.Method()))
	return auth.CalcResponse() == auth.Response()
}

// requestExpires returns the request's Expires header value, or -1 when the
// header is absent.
func (s *Server) requestExpires(req sip.Request) int {
	for _, h := range req.GetHeaders("Expires") {
		if exp, ok := h.(*sip.Expires); ok {
			return int(*exp)
		}
	}
	return -1
}

// respond sends a response for the given request. Safe to call after Stop —
// the write is skipped once the server is gone.
func (s *Server) respond(req sip.Request, tx sip.ServerTransaction, status sip.StatusCode, reason string, headers []sip.Header) {
	s.mu.Lock()
	srv := s.gosipSrv
	s.mu.Unlock()
	if srv == nil {
		return
	}
	if _, err := srv.RespondOnRequest(req, status, reason, "", headers); err != nil {
		slog.Warn("gb28181: respond failed", "method", req.Method(), "status", status, "error", err)
	}
}

// getDeviceMu returns (creating if needed) the per-device mutex.
func (s *Server) getDeviceMu(deviceID string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	mu, ok := s.perDeviceMu[deviceID]
	if !ok {
		mu = &sync.Mutex{}
		s.perDeviceMu[deviceID] = mu
	}
	return mu
}

// parseSIPListen splits a "host:port" listen address, defaulting to :5060.
func parseSIPListen(listen string) (host string, port int, err error) {
	if listen == "" {
		return "", 5060, nil
	}
	host, portStr, err := net.SplitHostPort(listen)
	if err != nil {
		return "", 0, fmt.Errorf("gb28181: invalid sip_listen %q: %w", listen, err)
	}
	port, err = strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("gb28181: invalid sip_listen port %q", portStr)
	}
	return host, port, nil
}

// localIP returns the IP address the platform should advertise in SIP From/
// Via headers of outbound requests. It prefers the host from sip_listen when
// configured, otherwise the first non-loopback IPv4 interface address, and
// falls back to 127.0.0.1.
func (s *Server) localIP() string {
	if host, _, err := parseSIPListen(s.cfg.SIPListen); err == nil && host != "" {
		return host
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok {
			if ip4 := ipnet.IP.To4(); ip4 != nil && !ip4.IsLoopback() {
				return ip4.String()
			}
		}
	}
	return "127.0.0.1"
}

// generateNonce returns a random hex nonce for digest challenges.
func generateNonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return hex.EncodeToString(b)
}

// slogAdapter adapts log/slog to gosip's log.Logger interface.
type slogAdapter struct {
	logger *slog.Logger
}

func (a slogAdapter) WithPrefix(prefix string) log.Logger {
	return slogAdapter{a.logger.With("prefix", prefix)}
}

func (a slogAdapter) Prefix() string { return "" }

func (a slogAdapter) WithFields(fields map[string]interface{}) log.Logger {
	attrs := make([]any, 0, len(fields)*2)
	for k, v := range fields {
		attrs = append(attrs, k, v)
	}
	return slogAdapter{a.logger.With(attrs...)}
}

func (a slogAdapter) Fields() log.Fields { return nil }

func (a slogAdapter) SetLevel(level uint32) {}

func (a slogAdapter) Fatal(args ...interface{}) { a.logger.Error(fmt.Sprint(args...)) }
func (a slogAdapter) Fatalf(format string, args ...interface{}) {
	a.logger.Error(fmt.Sprintf(format, args...))
}
func (a slogAdapter) Panic(args ...interface{}) { a.logger.Error(fmt.Sprint(args...)) }
func (a slogAdapter) Panicf(format string, args ...interface{}) {
	a.logger.Error(fmt.Sprintf(format, args...))
}
func (a slogAdapter) Trace(args ...interface{}) { a.logger.Debug(fmt.Sprint(args...)) }
func (a slogAdapter) Tracef(format string, args ...interface{}) {
	a.logger.Debug(fmt.Sprintf(format, args...))
}
func (a slogAdapter) Debug(args ...interface{}) { a.logger.Debug(fmt.Sprint(args...)) }
func (a slogAdapter) Debugf(format string, args ...interface{}) {
	a.logger.Debug(fmt.Sprintf(format, args...))
}
func (a slogAdapter) Print(args ...interface{}) { a.logger.Info(fmt.Sprint(args...)) }
func (a slogAdapter) Printf(format string, args ...interface{}) {
	a.logger.Info(fmt.Sprintf(format, args...))
}
func (a slogAdapter) Info(args ...interface{}) { a.logger.Info(fmt.Sprint(args...)) }
func (a slogAdapter) Infof(format string, args ...interface{}) {
	a.logger.Info(fmt.Sprintf(format, args...))
}
func (a slogAdapter) Warn(args ...interface{}) { a.logger.Warn(fmt.Sprint(args...)) }
func (a slogAdapter) Warnf(format string, args ...interface{}) {
	a.logger.Warn(fmt.Sprintf(format, args...))
}
func (a slogAdapter) Error(args ...interface{}) { a.logger.Error(fmt.Sprint(args...)) }
func (a slogAdapter) Errorf(format string, args ...interface{}) {
	a.logger.Error(fmt.Sprintf(format, args...))
}
