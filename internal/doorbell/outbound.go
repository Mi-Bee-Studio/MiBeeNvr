package doorbell

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/ghettovoice/gosip/log"

	"github.com/ghettovoice/gosip/sip"
)

// outboundLoop keeps the NVR registered to the external PBX (mode B):
// REGISTER → 401 digest challenge → REGISTER with Authorization → 200 OK,
// refreshed at expires/2. INVITEs then arrive through the PBX and hit the
// same handleInvite path (mode-B trust: PBX authenticated us, we accept
// INVITEs whose From user is a configured door station).
func (s *Server) outboundLoop(ctx context.Context) {
	o := s.cfg.Outbound
	interval := o.Expires / 2
	if interval < 10*time.Second {
		interval = 10 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	// Register immediately, then on cadence.
	s.registerOutbound(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopped:
			return
		case <-t.C:
			s.registerOutbound(ctx)
		}
	}
}

func (s *Server) registerOutbound(ctx context.Context) {
	o := s.cfg.Outbound
	if err := s.sendOutboundRegister(ctx, o); err != nil {
		s.log.Warn("doorbell outbound REGISTER failed", "server", o.Server, "error", err)
	} else {
		s.log.Info("doorbell outbound REGISTER ok", "server", o.Server, "user", o.Username)
	}
}

func md5hex(parts ...string) string {
	h := md5.New() //nolint:gosec // SIP digest mandates MD5
	_, _ = h.Write([]byte(strings.Join(parts, ":")))
	return hex.EncodeToString(h.Sum(nil))
}

var challengeParamRe = regexp.MustCompile(`(\w+)\s*=\s*"([^"]+)"`)

func (s *Server) sendOutboundRegister(ctx context.Context, o *Outbound) error {
	server := strings.TrimPrefix(o.Server, "sip:")
	serverHost, serverPort, err := net.SplitHostPort(server)
	if err != nil {
		serverHost, serverPort = server, "5060"
	}
	portVal := sip.Port(mustAtoi(serverPort, 5060))
	ourHost := s.localHostFor(serverHost)
	recipient := &sip.SipUri{
		FUser: sip.String{Str: o.Username},
		FHost: serverHost,
		FPort: &portVal,
	}
	from := &sip.Address{Uri: recipient}
	to := &sip.Address{Uri: recipient}

	req, err := s.buildSIPRequest(sip.REGISTER, ourHost, from, to, recipient, "", "", "")
	if err != nil {
		return err
	}
	resp, err := s.sipRoundTrip(ctx, req)
	if err != nil {
		return err
	}
	if resp.StatusCode() != 401 && resp.StatusCode() != 407 {
		if resp.IsSuccess() {
			return nil
		}
		return fmt.Errorf("status %d (%s)", resp.StatusCode(), resp.Reason())
	}

	// Digest dance (same shape as the gb28181 cascade client).
	var challenge string
	for _, h := range resp.GetHeaders("WWW-Authenticate") {
		if g, ok := h.(*sip.GenericHeader); ok {
			challenge = g.Contents
			break
		}
	}
	if challenge == "" {
		return fmt.Errorf("401 without WWW-Authenticate")
	}
	vals := map[string]string{}
	for _, m := range challengeParamRe.FindAllStringSubmatch(challenge, -1) {
		vals[m[1]] = m[2]
	}
	realm := vals["realm"]
	nonce := vals["nonce"]
	if nonce == "" {
		return fmt.Errorf("challenge without nonce")
	}
	uri := fmt.Sprintf("sip:%s@%s", o.Username, net.JoinHostPort(serverHost, serverPort))
	respHash := md5hex(md5hex(o.Username, realm, o.Secret), nonce, md5hex("REGISTER", uri))
	authVal := fmt.Sprintf(
		`Digest realm="%s",algorithm=MD5,nonce="%s",username="%s",uri="%s",response="%s"`,
		realm, nonce, o.Username, uri, respHash,
	)

	req2, err := s.buildSIPRequest(sip.REGISTER, ourHost, from, to, recipient, "", "", "")
	if err != nil {
		return err
	}
	req2.AppendHeader(&sip.GenericHeader{HeaderName: "Authorization", Contents: authVal})
	expiresVal := sip.Expires(int(o.Expires.Seconds()))
	req2.AppendHeader(&expiresVal)

	resp2, err := s.sipRoundTrip(ctx, req2)
	if err != nil {
		return err
	}
	if !resp2.IsSuccess() {
		return fmt.Errorf("register: status %d (%s)", resp2.StatusCode(), resp2.Reason())
	}
	return nil
}

// buildSIPRequest mirrors the gb28181 server's request builder (Via with
// fresh branch + rport, CSeq 1).
func (s *Server) buildSIPRequest(method sip.RequestMethod, host string, from, to *sip.Address, recipient *sip.SipUri, subject, contentType, body string) (sip.Request, error) {
	_, _, port, _ := splitListen(s.cfg.Listen)
	portVal := sip.Port(port)
	rb := sip.NewRequestBuilder()
	rb.SetMethod(method)
	rb.SetFrom(from)
	rb.SetTo(to)
	rb.SetRecipient(recipient)
	rb.SetHost(host)
	rb.SetContact(&sip.Address{
		Uri: &sip.SipUri{FUser: sip.String{Str: "mibee-nvr"}, FHost: host, FPort: &portVal},
	})
	rb.AddVia(&sip.ViaHop{
		Host: host,
		Port: &portVal,
		Params: sip.NewParams().
			Add("branch", sip.String{Str: sip.GenerateBranch()}).
			Add("rport", sip.String{}),
	})
	rb.SetSeqNo(1)
	if subject != "" {
		rb.AddHeader(&sip.GenericHeader{HeaderName: "Subject", Contents: subject})
	}
	if contentType != "" {
		ct := sip.ContentType(contentType)
		rb.SetContentType(&ct)
	}
	if body != "" {
		rb.SetBody(body)
	}
	return rb.Build()
}

// sipRoundTrip sends a request through the server's client side and waits
// for the final response.
func (s *Server) sipRoundTrip(ctx context.Context, req sip.Request) (sip.Response, error) {
	tx, err := s.srv.Request(req)
	if err != nil {
		return nil, err
	}
	responses := tx.Responses()
	defer tx.Done()
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
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.stopped:
			return nil, fmt.Errorf("stopped")
		}
	}
}

// localHostFor picks the interface IP that routes toward dst.
func (s *Server) localHostFor(dst string) string {
	d := net.Dialer{Timeout: 2 * time.Second}
	c, err := d.Dial("udp", net.JoinHostPort(dst, "9"))
	if err != nil {
		return "127.0.0.1"
	}
	defer c.Close()
	host, _, err := net.SplitHostPort(c.LocalAddr().String())
	if err != nil {
		return "127.0.0.1"
	}
	return host
}

func splitListen(listen string) (network, host string, port int, err error) {
	host, portStr, err := net.SplitHostPort(listen)
	if err != nil {
		return "", "", 5070, err
	}
	return "", host, mustAtoi(portStr, 5070), nil
}

func mustAtoi(s string, def int) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
	}
	if n == 0 {
		return def
	}
	return n
}

// gosipSlogLogger adapts *slog.Logger to gosip's log.Logger (same shape as
// the gb28181 SIP server's adapter).
type gosipSlogLogger struct{ l *slog.Logger }

func gosipLog(l *slog.Logger) gosipSlogLogger { return gosipSlogLogger{l: l} }

func (a gosipSlogLogger) WithPrefix(prefix string) log.Logger {
	return gosipLog(a.l.With("prefix", prefix))
}
func (a gosipSlogLogger) Prefix() string { return "" }
func (a gosipSlogLogger) WithFields(fields map[string]interface{}) log.Logger {
	attrs := make([]any, 0, len(fields)*2)
	for k, v := range fields {
		attrs = append(attrs, k, v)
	}
	return gosipLog(a.l.With(attrs...))
}
func (a gosipSlogLogger) Fields() log.Fields        { return nil }
func (a gosipSlogLogger) SetLevel(level uint32)     {}
func (a gosipSlogLogger) Fatal(args ...interface{}) { a.l.Error(fmt.Sprint(args...)) }
func (a gosipSlogLogger) Fatalf(format string, args ...interface{}) {
	a.l.Error(fmt.Sprintf(format, args...))
}
func (a gosipSlogLogger) Panic(args ...interface{}) { a.l.Error(fmt.Sprint(args...)) }
func (a gosipSlogLogger) Panicf(format string, args ...interface{}) {
	a.l.Error(fmt.Sprintf(format, args...))
}
func (a gosipSlogLogger) Trace(args ...interface{}) { a.l.Debug(fmt.Sprint(args...)) }
func (a gosipSlogLogger) Tracef(format string, args ...interface{}) {
	a.l.Debug(fmt.Sprintf(format, args...))
}
func (a gosipSlogLogger) Debug(args ...interface{}) { a.l.Debug(fmt.Sprint(args...)) }
func (a gosipSlogLogger) Debugf(format string, args ...interface{}) {
	a.l.Debug(fmt.Sprintf(format, args...))
}
func (a gosipSlogLogger) Print(args ...interface{}) { a.l.Info(fmt.Sprint(args...)) }
func (a gosipSlogLogger) Printf(format string, args ...interface{}) {
	a.l.Info(fmt.Sprintf(format, args...))
}
func (a gosipSlogLogger) Info(args ...interface{}) { a.l.Info(fmt.Sprint(args...)) }
func (a gosipSlogLogger) Infof(format string, args ...interface{}) {
	a.l.Info(fmt.Sprintf(format, args...))
}
func (a gosipSlogLogger) Warn(args ...interface{}) { a.l.Warn(fmt.Sprint(args...)) }
func (a gosipSlogLogger) Warnf(format string, args ...interface{}) {
	a.l.Warn(fmt.Sprintf(format, args...))
}
func (a gosipSlogLogger) Error(args ...interface{}) { a.l.Error(fmt.Sprint(args...)) }
func (a gosipSlogLogger) Errorf(format string, args ...interface{}) {
	a.l.Error(fmt.Sprintf(format, args...))
}
