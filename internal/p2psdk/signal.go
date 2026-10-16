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

	// #66 auth-class close state: authTerminal stops the reconnect ladder
	// (no fresh credential can fix the verdict); authFailStreak bounds
	// re-mint retries for mintable verdicts when TokenFunc is set.
	authTerminal   atomic.Bool
	authFailStreak int
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

	serverURL := sc.cfg.ServerURL
	// #48 nearest-node selection: only when discovery is configured, and
	// only as a PREFERRED endpoint — every failure path keeps ServerURL.
	if sc.cfg.NodesURL != "" {
		if best := DiscoverBestNode(ctx, sc.cfg.NodesURL, sc.logger); best != "" {
			serverURL = best
		}
	}

	conn, resp, err := dialer.DialContext(ctx, serverURL, http.Header{})
	if err != nil {
		// The discovered node may be freshly dead (probe raced): one
		// straight retry on the configured URL before giving the rung up.
		conn, resp, err = dialer.DialContext(ctx, sc.cfg.ServerURL, http.Header{})
		if err != nil {
			return nil, fmt.Errorf("dial signaling: %w", err)
		}
	}
	defer func() { _ = resp.Body.Close() }()

	sc.connMu.Lock()
	sc.conn = conn
	sc.connectedAt = time.Now()
	sc.lastWriteAt = sc.connectedAt // the hello itself is traffic
	sc.connMu.Unlock()

	// Send hello — the JWT is minted fresh when TokenFunc is set (#66):
	// server tokens are single-use, so the static config JWT is only the
	// first-connection credential.
	hello := Hello{
		ProductID: sc.cfg.ProductID,
		PeerID:    sc.cfg.PeerID,
		Role:      sc.cfg.Role,
		JWT:       sc.currentJWT(),
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
		if cls := classifyCloseError(err); cls != authCloseNone {
			sc.noteAuthClassClose(cls)
			return nil, fmt.Errorf("%w: server rejected hello: %w", ErrAuthFailed, err)
		}
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
			lifetime := sc.lifetimeLocked()
			sc.connMu.Unlock()
			sc.recordSessionLifetime(lifetime)
			_ = conn.Close()
			sc.notifyClose(err)
			if sc.reconnect.Load() {
				if sc.authClassAbandoned(err) {
					sc.logger.Warn("reconnect ladder abandoned: auth-class close",
						"reason", err.Error(),
					)
					return
				}
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

		delay := reconnectDelay(attempt, baseDelay, maxDelay, sc.currentReconnectCooldown())
		sc.logger.Info("reconnecting", "attempt", attempt+1, "delay", delay)
		select {
		case <-sc.closeCh:
			return
		case <-time.After(delay):
		}

		if sc.reconnectAttempt(attempt) {
			return // reconnected (resume or full hello) or ladder abandoned
		}
	}
}

// reconnectDelay is one ladder step's backoff: exponential from baseDelay
// capped at maxDelay, raised to the short-lived-streak cooldown when that
// is higher (#35 ② — a single normal flash-cut keeps its fast recovery,
// only crash loops get throttled).
func reconnectDelay(attempt int, baseDelay, maxDelay, cooldown time.Duration) time.Duration {
	delay := time.Duration(math.Min(
		float64(baseDelay)*math.Pow(2, float64(attempt)),
		float64(maxDelay),
	))
	if cooldown > delay {
		return cooldown
	}
	return delay
}

// reconnectAttempt performs one ladder step: dial, optional resume, full
// hello. Returns true when the ladder must stop (success or abandonment).
func (sc *signalingClient) reconnectAttempt(attempt int) bool {
	dialer := &websocket.Dialer{
		TLSClientConfig:  sc.cfg.TLSConfig,
		HandshakeTimeout: sc.cfg.handshakeTimeout(),
	}
	conn, resp, err := dialer.Dial(sc.cfg.ServerURL, http.Header{})
	if err != nil {
		sc.logger.Warn("reconnect dial failed", "attempt", attempt+1, "error", err)
		return false
	}
	_ = resp.Body.Close()

	// ── Attempt session resume if we have a session ID ────────────
	if sc.lastSessionID != "" {
		resumed, rconn, rerr := sc.tryResumeOrRedial(dialer, conn, attempt)
		if rerr != nil {
			return false
		}
		if resumed {
			sc.fireOnReconnect()
			return true
		}
		conn = rconn
	}
	return sc.helloReconnect(conn)
}

// helloReconnect completes a full hello handshake on a live connection and
// installs it. Returns true when the ladder must stop: success, or a
// terminal auth verdict (#66 — mintable verdicts already re-minted by the
// caller's TokenFunc keep the ladder going).
func (sc *signalingClient) helloReconnect(conn *websocket.Conn) bool {
	if sc.authTerminal.Load() {
		sc.logger.Warn("reconnect ladder abandoned: auth-class close")
		return true
	}
	hello := Hello{
		ProductID: sc.cfg.ProductID,
		PeerID:    sc.cfg.PeerID,
		Role:      sc.cfg.Role,
		JWT:       sc.currentJWT(),
		Signature: sc.deviceHelloSignature(),
	}
	helloData, err := Marshal(MessageTypeHello, hello)
	if err != nil {
		_ = conn.Close()
		return false
	}
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := conn.WriteMessage(websocket.TextMessage, helloData); err != nil {
		_ = conn.Close()
		return false
	}

	// Read welcome; an auth-class close triages the ladder (#66).
	msg, rerr := sc.readWelcomeOrClose(conn)
	if rerr != nil {
		var stop authLadderStop
		return errors.As(rerr, &stop)
	}
	var welcome Welcome
	if err := UnmarshalPayload(msg, &welcome); err != nil {
		_ = conn.Close()
		return false
	}

	sc.adoptReconnectedConn(conn, welcome.SessionID)
	sc.fireOnReconnect()
	go sc.recvLoop()
	return true
}

// adoptReconnectedConn installs a freshly handshaked connection for the
// recvLoop. Under connMu: close() snapshots sc.done under the same lock —
// an unlocked swap is a data race, and a stale snapshot would let close()
// return while the fresh recvLoop still runs.
func (sc *signalingClient) adoptReconnectedConn(conn *websocket.Conn, sessionID string) {
	sc.connMu.Lock()
	defer sc.connMu.Unlock()
	sc.conn = conn
	sc.lastSessionID = sessionID
	sc.done = make(chan struct{})
	sc.authFailStreak = 0
}

// fireOnReconnect notifies the application that the link recovered.
func (sc *signalingClient) fireOnReconnect() {
	sc.logger.Info("reconnected")
	if sc.onReconnect != nil {
		sc.onReconnect()
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
// currentJWT returns the JWT for the next hello (#66): TokenFunc mint when
// available (server tokens are single-use), static config JWT otherwise.
func (sc *signalingClient) currentJWT() string {
	if sc.cfg.TokenFunc == nil {
		return sc.cfg.JWT
	}
	fresh, err := sc.cfg.TokenFunc()
	if err != nil || fresh == "" {
		sc.logger.Warn("TokenFunc mint failed, falling back to static JWT", "error", err)
		return sc.cfg.JWT
	}
	return fresh
}

// noteAuthClassClose records an auth-class server verdict and decides whether
// the reconnect ladder must stop (#66). Terminal verdicts always stop.
// Mintable verdicts (token_expired / token_replayed) stop unless a TokenFunc
// can re-mint — and even then after maxAuthRemintFailures consecutive
// mint-and-fail cycles, so a persistently failing mint doesn't loop forever.
// The return value is "stop the ladder".
func (sc *signalingClient) noteAuthClassClose(cls authCloseClass) bool {
	sc.connMu.Lock()
	defer sc.connMu.Unlock()
	switch cls {
	case authCloseTerminal:
		sc.authTerminal.Store(true)
		return true
	case authCloseMintable:
		if sc.cfg.TokenFunc == nil {
			sc.authTerminal.Store(true)
			return true
		}
		sc.authFailStreak++
		if sc.authFailStreak >= maxAuthRemintFailures {
			sc.authTerminal.Store(true)
			return true
		}
		return false
	case authCloseNone:
		return false
	default:
		return false
	}
}

// maxAuthRemintFailures bounds mintable-verdict re-mint retries.
const maxAuthRemintFailures = 3

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

// authLadderStop is the typed signal that readWelcomeOrClose wants the
// reconnect ladder terminated (terminal auth verdict).
type authLadderStop struct{ reason string }

func (authLadderStop) Error() string { return "ladder abandoned" }

// readWelcomeOrClose reads the welcome on a freshly dialed ladder
// connection. A non-nil error that is NOT authLadderStop means "retry the
// ladder"; authLadderStop means "abandon" (#66).
func (sc *signalingClient) readWelcomeOrClose(conn *websocket.Conn) (*Message, error) {
	_ = conn.SetReadDeadline(time.Now().Add(sc.cfg.readTimeout()))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		_ = conn.Close()
		if cls := classifyCloseError(err); cls != authCloseNone {
			if sc.noteAuthClassClose(cls) {
				sc.logger.Warn("reconnect ladder abandoned: auth-class close",
					"reason", err.Error())
				return nil, authLadderStop{reason: err.Error()}
			}
		}
		return nil, err
	}
	msg, err := ParseAndValidate(raw)
	if err != nil || msg.Type != MessageTypeWelcome {
		_ = conn.Close()
		return nil, errNotWelcome
	}
	return msg, nil
}

// errNotWelcome marks a ladder attempt whose answer was not a welcome.
var errNotWelcome = errors.New("signaling answer was not a welcome")

// tryResumeOrRedial attempts signal_resume on a freshly dialed connection;
// when the resume is rejected (single-use window miss) it redials for a
// full hello. Returns (resumed=true, liveConn) on resume success,
// (false, redialedConn) for the hello path, or an error when both dials
// failed and the ladder should advance to the next attempt.
func (sc *signalingClient) tryResumeOrRedial(
	dialer *websocket.Dialer, conn *websocket.Conn, attempt int,
) (bool, *websocket.Conn, error) {
	if sc.tryResume(conn) {
		return true, conn, nil
	}
	// Resume rejected — the server closed the connection after the failed
	// attempt, so redial for a full hello instead of retrying resume
	// forever (the resume window is single-use).
	sc.logger.Debug("resume failed, falling back to full handshake")
	_ = conn.Close()
	redialed, resp, err := dialer.Dial(sc.cfg.ServerURL, http.Header{})
	if err != nil {
		sc.logger.Warn("reconnect dial failed", "attempt", attempt+1, "error", err)
		return false, nil, err
	}
	_ = resp.Body.Close()
	return false, redialed, nil
}

// authClassAbandoned classifies a connection-loss error and records the
// verdict: true means the ladder must stop (#66 — terminal verdict, or a
// mintable one past the re-mint budget / without a TokenFunc).
func (sc *signalingClient) authClassAbandoned(err error) bool {
	cls := classifyCloseError(err)
	return cls != authCloseNone && sc.noteAuthClassClose(cls)
}

// lifetimeLocked returns the age of the connection being torn down; the
// caller holds connMu (zero connectedAt counts as never-lived).
func (sc *signalingClient) lifetimeLocked() time.Duration {
	if sc.connectedAt.IsZero() {
		return 0
	}
	return time.Since(sc.connectedAt)
}

// notifyClose fires the OnDisconnect callback when registered.
func (sc *signalingClient) notifyClose(err error) {
	if cb := sc.onClose; cb != nil {
		cb(err)
	}
}
