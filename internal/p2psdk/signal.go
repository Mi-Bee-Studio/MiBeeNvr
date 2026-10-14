package peer

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// writeTimeout is the maximum time to wait for a WebSocket write.
const writeTimeout = 10 * time.Second

// keepaliveInterval is how often the client sends an app-level ping. It is
// half the server's idle timeout (30s) and the SDK read timeout (30s) so one
// lost ping/pong never lets either side's deadline expire. A variable (not a
// const) so tests can shorten it; guarded by keepaliveMu because a previous
// test's keepalive goroutine may still read it while the next test writes it.
var (
	keepaliveMu       sync.Mutex
	keepaliveInterval = 15 * time.Second
)

func currentKeepaliveInterval() time.Duration {
	keepaliveMu.Lock()
	defer keepaliveMu.Unlock()
	return keepaliveInterval
}

func setKeepaliveInterval(d time.Duration) {
	keepaliveMu.Lock()
	defer keepaliveMu.Unlock()
	keepaliveInterval = d
}

// signalingClient manages a WebSocket connection to the signaling server.
type signalingClient struct {
	cfg         SignalingConfig
	conn        *websocket.Conn
	connMu      sync.Mutex
	logger      *slog.Logger
	closeCh     chan struct{}
	closeOnce   sync.Once
	done        chan struct{} // closed when recvLoop exits
	onMessage   func([]byte)  // called for every received message
	onClose     func(error)   // called when connection is lost
	onReconnect func()        // called after a successful reconnect
	reconnect   atomic.Bool

	// deviceKey is the loaded Ed25519 private key (DeviceKeyPath), used to
	// sign hellos for pubkey-bound device identities.
	deviceKey ed25519.PrivateKey

	// lastSessionID and lastSeq track the last known session state for resumption.
	lastSessionID string
	lastSeq       uint64

	// #35 ① keepalive idle suppression (syncthing pingSender pattern): any
	// write inside the last half-interval means real traffic already fed both
	// idle timers — the scheduled ping is skipped. Busy sessions stop paying
	// the per-ping tax (~5700 redundant frames/day for an idle-heavy device).
	lastWriteAt time.Time
	// #35 ② short-lived-session cooldown bookkeeping: connectedAt marks this
	// connection's establishment; consecutive short lifetimes stack a cooldown
	// on top of the exponential backoff (crash-loop / sweep-boundary peers must
	// not hammer both sides at the normal reconnect pace).
	connectedAt      time.Time
	shortLivedStreak int
}

// #35 ② thresholds: a session shorter than shortLivedThreshold counts against
// the streak; surviving stableLifetime resets it. Cooldown stacks from
// baseCooldown, capped at maxCooldown (syncthing uses 5min forced-redial cap).
const (
	shortLivedThreshold = 30 * time.Second
	stableLifetime      = 2 * time.Minute
	baseCooldown        = 60 * time.Second
	maxCooldown         = 5 * time.Minute
)

// reconnectCooldownFor returns the extra cooldown for the next reconnect
// after n consecutive short-lived sessions (#35 ②). <2 short lifetimes keep
// the normal backoff (a single network blip must not penalize recovery).
func reconnectCooldownFor(streak int) time.Duration {
	if streak < 2 {
		return 0
	}
	c := baseCooldown << (streak - 2) // 60s, 120s, 240s, …
	if c > maxCooldown || c <= 0 {
		return maxCooldown
	}
	return c
}

func newSignalingClient(cfg SignalingConfig) *signalingClient {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(discardWriter{}, nil))
	}
	sc := &signalingClient{
		cfg:    cfg,
		logger: logger.With("component", "signal_client"),
	}
	if cfg.DeviceKeyPath != "" {
		raw, err := os.ReadFile(cfg.DeviceKeyPath) //nolint:gosec // user-provided key path
		if err != nil {
			logger.Warn("device key unreadable; hellos will be unsigned", "path", cfg.DeviceKeyPath, "error", err)
		} else if len(raw) == ed25519.SeedSize {
			sc.deviceKey = ed25519.NewKeyFromSeed(raw)
		} else if len(raw) == ed25519.PrivateKeySize {
			sc.deviceKey = ed25519.PrivateKey(raw)
		} else {
			logger.Warn("device key must be 32 raw bytes (seed) or 64 bytes (full key); hellos will be unsigned",
				"path", cfg.DeviceKeyPath, "size", len(raw))
		}
	}
	sc.closeCh = make(chan struct{})
	sc.done = make(chan struct{})
	sc.reconnect.Store(true)
	return sc
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// connect dials the signaling server WebSocket and returns the welcome message.
// It blocks until the hello handshake completes.
//
//nolint:funlen // complex setup
func (sc *signalingClient) connect(ctx context.Context) (*Welcome, error) {
	dialer := &websocket.Dialer{
		TLSClientConfig:  sc.cfg.TLSConfig,
		HandshakeTimeout: sc.cfg.handshakeTimeout(),
	}

	conn, resp, err := dialer.DialContext(ctx, sc.cfg.ServerURL, http.Header{})
	if err != nil {
		return nil, fmt.Errorf("dial signaling: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	sc.connMu.Lock()
	sc.conn = conn
	sc.connectedAt = time.Now()
	sc.lastWriteAt = sc.connectedAt // the hello itself is traffic
	sc.connMu.Unlock()

	// Send hello
	jwt, err := sc.helloJWT(true)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("token: %w", err)
	}
	hello := Hello{
		ProductID: sc.cfg.ProductID,
		PeerID:    sc.cfg.PeerID,
		Role:      sc.cfg.Role,
		JWT:       jwt,
		Signature: sc.deviceHelloSignature(),
	}
	helloData, err := Marshal(MessageTypeHello, hello)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("marshal hello: %w", err)
	}
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := conn.WriteMessage(websocket.TextMessage, helloData); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("send hello: %w", err)
	}

	// Read welcome
	_ = conn.SetReadDeadline(time.Now().Add(sc.cfg.readTimeout()))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("read welcome: %w", err)
	}
	msg, err := ParseAndValidate(raw)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("parse welcome: %w", err)
	}
	if msg.Type != MessageTypeWelcome {
		// Check for error
		if msg.Type == MessageTypeError {
			var errMsg ErrorMsg
			if e := UnmarshalPayload(msg, &errMsg); e == nil {
				_ = conn.Close()
				return nil, fmt.Errorf("server error: %s: %s", errMsg.Code, errMsg.Message)
			}
		}
		_ = conn.Close()
		return nil, fmt.Errorf("expected welcome, got: %s", msg.Type)
	}
	var welcome Welcome
	if err := UnmarshalPayload(msg, &welcome); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("parse welcome payload: %w", err)
	}

	// recvLoop is started by the caller (Dial) via start() after wiring
	// onMessage/onClose callbacks, avoiding a nil-callback startup race.

	sc.connMu.Lock()
	sc.lastSessionID = welcome.SessionID
	sc.connMu.Unlock()

	sc.logger.Info(
		"signaling connected",
		"session_id", welcome.SessionID,
		"peer_id", welcome.PeerID,
	)

	return &welcome, nil
}

// start launches the background read loop. It must be called exactly once,
// after onMessage/onClose are set.
func (sc *signalingClient) start() {
	go sc.recvLoop()
}

// sendBytes writes raw bytes to the WebSocket connection.
func (sc *signalingClient) sendBytes(data []byte) error {
	// gorilla connections allow only one concurrent writer; hold connMu for
	// the whole write (also pins the conn against close() racing).
	sc.connMu.Lock()
	defer sc.connMu.Unlock()
	if sc.conn == nil {
		return errors.New("not connected")
	}
	_ = sc.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	err := sc.conn.WriteMessage(websocket.TextMessage, data)
	if err == nil {
		sc.lastWriteAt = time.Now() // #35 ①：真实流量已喂两端空闲计时器
	}
	return err
}

// recentWrite reports whether any message was written within the last half
// keepalive interval (#35 ①). Called by keepaliveLoop before sending a ping.
func (sc *signalingClient) recentWrite() bool {
	sc.connMu.Lock()
	defer sc.connMu.Unlock()
	if sc.lastWriteAt.IsZero() {
		return false
	}
	return time.Since(sc.lastWriteAt) < currentKeepaliveInterval()/2
}

// recordSessionLifetime folds a finished connection's lifetime into the
// short-lived streak (#35 ②). Long-lived sessions reset the streak so a
// recovered peer regains normal reconnect pace.
func (sc *signalingClient) recordSessionLifetime(d time.Duration) {
	sc.connMu.Lock()
	defer sc.connMu.Unlock()
	switch {
	case d >= stableLifetime:
		sc.shortLivedStreak = 0
	case d < shortLivedThreshold:
		sc.shortLivedStreak++
	}
}

// currentReconnectCooldown returns the cooldown to apply on the next
// reconnect (0 unless the short-lived streak has stacked up).
func (sc *signalingClient) currentReconnectCooldown() time.Duration {
	sc.connMu.Lock()
	defer sc.connMu.Unlock()
	return reconnectCooldownFor(sc.shortLivedStreak)
}

// send sends a message to the signaling server.
func (sc *signalingClient) send(data []byte) error {
	return sc.sendBytes(data)
}

// recvLoop reads messages from the WebSocket connection and dispatches them.
func (sc *signalingClient) recvLoop() {
	defer close(sc.done)
	conn := sc.conn
	kaDone := make(chan struct{})
	defer close(kaDone)
	go sc.keepaliveLoop(kaDone)

	for {
		_ = conn.SetReadDeadline(time.Now().Add(sc.cfg.readTimeout()))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			// Check if we should reconnect
			if websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				sc.logger.Warn("signaling connection lost", "error", err)
			}
			// #35 ②：结算本次连接存活并打点重连原因（崩溃循环观测）。
			sc.connMu.Lock()
			sc.conn = nil
			lifetime := time.Duration(0)
			if !sc.connectedAt.IsZero() {
				lifetime = time.Since(sc.connectedAt)
			}
			sc.connMu.Unlock()
			sc.recordSessionLifetime(lifetime)
			_ = conn.Close()
			if sc.onClose != nil {
				sc.onClose(err)
			}
			if sc.reconnect.Load() {
				sc.logger.Info("reconnect scheduled",
					"reason", err.Error(),
					"lifetime_s", lifetime.Seconds(),
					"cooldown", sc.currentReconnectCooldown(),
				)
				sc.reconnectWithBackoff()
			}
			return
		}
		if sc.onMessage != nil {
			sc.onMessage(raw)
		}
	}
}

// keepaliveLoop sends app-level pings until the connection ends. The server
// echoes pong, so a single interval refreshes both the server's idle timer
// and this client's read deadline.
func (sc *signalingClient) keepaliveLoop(done <-chan struct{}) {
	ticker := time.NewTicker(currentKeepaliveInterval())
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-sc.closeCh:
			return
		case <-ticker.C:
			// #35 ①：半周期内有真实写入则跳过本次 ping——流量本身已喂
			// 两端空闲计时器（syncthing pingSender 同款语义）。
			if sc.recentWrite() {
				continue
			}
			ping, err := Marshal(MessageTypePing, Ping{Timestamp: time.Now().UnixMilli()})
			if err != nil {
				return
			}
			if sc.sendBytes(ping) != nil {
				return // dead connection; recvLoop observes it too
			}
		}
	}
}

// reconnectWithBackoff attempts to reconnect with exponential backoff.
// It first tries to resume an existing session; if that fails, it falls
// back to a full hello handshake.
//
//nolint:funlen // complex reconnection logic
func (sc *signalingClient) reconnectWithBackoff() {
	baseDelay := 1 * time.Second
	maxDelay := 30 * time.Second

	for attempt := 0; ; attempt++ {
		select {
		case <-sc.closeCh:
			return
		default:
		}

		delay := time.Duration(math.Min(
			float64(baseDelay)*math.Pow(2, float64(attempt)),
			float64(maxDelay),
		))
		// #35 ②：短命会话连击时叠加冷却（与指数退避取 max——单次正常
		// 闪断的恢复速度不受影响，只有崩溃循环被压速）。
		if cd := sc.currentReconnectCooldown(); cd > delay {
			delay = cd
		}

		sc.logger.Info("reconnecting", "attempt", attempt+1, "delay", delay)

		select {
		case <-sc.closeCh:
			return
		case <-time.After(delay):
		}

		dialer := &websocket.Dialer{
			TLSClientConfig:  sc.cfg.TLSConfig,
			HandshakeTimeout: sc.cfg.handshakeTimeout(),
		}
		conn, resp, err := dialer.Dial(sc.cfg.ServerURL, http.Header{})
		if err != nil {
			sc.logger.Warn("reconnect dial failed", "attempt", attempt+1, "error", err)
			continue
		}
		_ = resp.Body.Close()

		// ── Attempt session resume if we have a session ID ────────────
		if sc.lastSessionID != "" {
			if sc.tryResume(conn) {
				sc.logger.Info("reconnected")
				if sc.onReconnect != nil {
					sc.onReconnect()
				}
				return
			}
			// Resume rejected — the server closed the connection after the
			// failed attempt, so redial and do a full hello handshake instead
			// of retrying resume forever (the resume window is single-use).
			sc.logger.Debug("resume failed, falling back to full handshake")
			_ = conn.Close()
			conn, resp, err = dialer.Dial(sc.cfg.ServerURL, http.Header{})
			if err != nil {
				sc.logger.Warn("reconnect dial failed", "attempt", attempt+1, "error", err)
				continue
			}
			_ = resp.Body.Close()
		}

		// ── Full hello handshake ──────────────────────────────────────
		jwt, err := sc.helloJWT(false)
		if err != nil {
			sc.logger.Warn("token refresh failed; deferring attempt",
				"attempt", attempt+1, "error", err)
			continue
		}
		hello := Hello{
			ProductID: sc.cfg.ProductID,
			PeerID:    sc.cfg.PeerID,
			Role:      sc.cfg.Role,
			JWT:       jwt,
			Signature: sc.deviceHelloSignature(),
		}
		helloData, err := Marshal(MessageTypeHello, hello)
		if err != nil {
			sc.logger.Warn("reconnect hello marshal failed", "error", err)
			_ = conn.Close()
			continue
		}
		_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		if err := conn.WriteMessage(websocket.TextMessage, helloData); err != nil {
			sc.logger.Warn("reconnect hello send failed", "attempt", attempt+1, "error", err)
			_ = conn.Close()
			continue
		}

		// Read welcome
		_ = conn.SetReadDeadline(time.Now().Add(sc.cfg.readTimeout()))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			sc.logger.Warn("reconnect hello rejected", "attempt", attempt+1, "error", err)
			_ = conn.Close()
			// Auth-class rejection with no refresher: the static JWT can
			// never satisfy the server — spinning at backoff forever just
			// hides the outage (2026-10-14 production: 48 silent attempts).
			// Give up the ladder; the session owner's watchdog/Close must
			// redial with fresh credentials. With TokenFunc the next
			// attempt mints a fresh token, so keep retrying.
			if isAuthClassClose(err) && sc.cfg.TokenFunc == nil {
				sc.logger.Warn("auth-class rejection with static JWT; abandoning reconnect ladder")
				return
			}
			continue
		}
		msg, err := ParseAndValidate(raw)
		if err != nil {
			sc.logger.Warn("reconnect welcome parse failed", "attempt", attempt+1, "error", err)
			_ = conn.Close()
			continue
		}
		if msg.Type != MessageTypeWelcome {
			if msg.Type == MessageTypeError {
				var errMsg ErrorMsg
				if e := UnmarshalPayload(msg, &errMsg); e == nil {
					sc.logger.Warn("server error in response to reconnect hello",
						"code", errMsg.Code, "message", errMsg.Message)
				}
			} else {
				sc.logger.Warn("expected welcome on reconnect", "got", msg.Type)
			}
			_ = conn.Close()
			continue
		}
		var welcome Welcome
		if err := UnmarshalPayload(msg, &welcome); err != nil {
			sc.logger.Warn("reconnect welcome payload failed", "error", err)
			_ = conn.Close()
			continue
		}

		sc.connMu.Lock()
		sc.conn = conn
		sc.lastSessionID = welcome.SessionID
		// Reset done channel for the new recvLoop (the previous one was closed
		// on exit). Under connMu: close() snapshots sc.done under the same
		// lock — an unlocked swap is a data race, and a stale snapshot would
		// let close() return while the fresh recvLoop still runs.
		sc.done = make(chan struct{})
		sc.connMu.Unlock()

		sc.logger.Info("reconnected")
		if sc.onReconnect != nil {
			sc.onReconnect()
		}
		go sc.recvLoop()
		return
	}
}

// tryResume attempts to resume an existing session on a fresh WebSocket connection.
// It sends a signal_resume message and waits for a Welcome response with a 2-second
// deadline. Returns true if resume succeeded (the caller must NOT close the conn).
func (sc *signalingClient) tryResume(conn *websocket.Conn) bool {
	payload := SyncState{
		SessionID:   sc.lastSessionID,
		LastSeenSeq: sc.lastSeq,
	}
	data, err := Marshal(MessageTypeSignalResume, payload)
	if err != nil {
		return false
	}
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return false
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		return false
	}
	msg, err := ParseAndValidate(raw)
	if err != nil || msg.Type != MessageTypeWelcome {
		return false
	}
	var welcome Welcome
	if err := UnmarshalPayload(msg, &welcome); err != nil {
		return false
	}
	sc.connMu.Lock()
	sc.conn = conn
	sc.lastSessionID = welcome.SessionID
	sc.connMu.Unlock()
	sc.done = make(chan struct{})
	sc.logger.Info("resumed session", "session_id", welcome.SessionID)
	go sc.recvLoop()
	return true
}

// close shuts down the signaling client.
func (sc *signalingClient) close() {
	sc.closeOnce.Do(func() {
		sc.reconnect.Store(false)
		close(sc.closeCh)
		sc.connMu.Lock()
		if sc.conn != nil {
			_ = sc.conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			_ = sc.conn.Close()
		}
		sc.connMu.Unlock()
	})
	// A reconnect already in flight when close() started may still swap
	// sc.done once (it checked sc.reconnect before close cleared it), so
	// snapshot under connMu and re-check after waiting: blocking on a stale,
	// already-closed channel would return while the fresh recvLoop runs.
	for {
		sc.connMu.Lock()
		done := sc.done
		sc.connMu.Unlock()
		<-done
		sc.connMu.Lock()
		latest := sc.done
		sc.connMu.Unlock()
		if latest == done {
			return
		}
	}
}

// deviceHelloSignature signs "<sub>:<jti>" — the identity-binding message the
// signal server verifies against the device record's registered public key —
// with the configured device key. The claims are read from the JWT's own
// payload (unverified locally; the server verifies the token signature).
// Returns "" when no key is configured or the payload is unreadable.
func (sc *signalingClient) deviceHelloSignature() string {
	if sc.deviceKey == nil || sc.cfg.Role != "device" {
		return ""
	}
	sub, jti, err := jwtSubjectAndJTI(sc.cfg.JWT)
	if err != nil {
		sc.logger.Warn("device hello signature skipped: unreadable JWT payload", "error", err)
		return ""
	}
	sig := ed25519.Sign(sc.deviceKey, []byte(sub+":"+jti))
	return base64.StdEncoding.EncodeToString(sig)
}

// jwtSubjectAndJTI extracts the sub and jti claims from a JWT payload
// without verifying the signature (the server does that).
func jwtSubjectAndJTI(token string) (string, string, error) {
	parts := splitJWT(token)
	if len(parts) != 3 {
		return "", "", errors.New("malformed jwt")
	}
	payload := parts[1]
	if l := len(payload) % 4; l != 0 {
		payload += strings.Repeat("=", 4-l)
	}
	raw, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return "", "", fmt.Errorf("decode payload: %w", err)
	}
	var claims struct {
		Sub string `json:"sub"`
		Jti string `json:"jti"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		return "", "", fmt.Errorf("parse claims: %w", err)
	}
	if claims.Sub == "" || claims.Jti == "" {
		return "", "", errors.New("missing sub or jti claim")
	}
	return claims.Sub, claims.Jti, nil
}

// splitJWT splits a compact JWS into its three dot-separated segments.
func splitJWT(token string) []string {
	return strings.Split(token, ".")
}

// helloJWT resolves the JWT for a full hello handshake. The initial connect
// prefers the static cfg.JWT when present — session owners like the NVR
// Manager mint it immediately before Dial, so consulting TokenFunc again
// would double-mint inside the anti-hammer interval and fail every first
// dial. Ladder attempts (initial=false) always re-mint via TokenFunc when
// wired: by then the Dial-time token may be hours past its TTL.
func (sc *signalingClient) helloJWT(initial bool) (string, error) {
	if initial && sc.cfg.JWT != "" {
		return sc.cfg.JWT, nil
	}
	if sc.cfg.TokenFunc != nil {
		return sc.cfg.TokenFunc()
	}
	return sc.cfg.JWT, nil
}

// isAuthClassClose reports whether a hello-phase error carries an auth-class
// close reason from the server ("authentication_failed", "subject_revoked",
// …). The server rejects bad hellos by closing the WebSocket with the reason
// as close text (no protocol error frame), and gorilla surfaces that text in
// the error string.
func isAuthClassClose(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, s := range []string{
		"authentication_failed",
		"subject_revoked",
		"token_expired",
		"invalid_token",
		"unauthorized",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
