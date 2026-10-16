package relay

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/nacl/box"
)

// #88② dual connection: a second WebSocket authenticated with the SAME key
// pair plus a conn_type byte (0x01). Priority-marked traffic rides this
// physically separate TCP, so control-plane frames cannot be head-of-line
// blocked behind in-flight bulk-data bytes — the relay-path root fix that
// lane queues alone cannot deliver (the server's outbound queue already
// prioritizes, but bytes already in the kernel buffer of one TCP still
// serialize everything behind them).
//
// Failure semantics are deliberately SOFT: a ctrl-link death degrades
// SendPriority back to the main connection's high lane (the server routes
// priority frames to the ctrl connection only when one exists) and logs —
// it never tears the session down. The main connection's liveness machinery
// remains the sole fatality source.

// ctrlLink is one live control-plane WebSocket.
type ctrlLink struct {
	conn     *websocket.Conn
	writeMu  sync.Mutex
	lastRead atomic.Int64
	dead     atomic.Bool
}

func (l *ctrlLink) writeFrame(frame []byte, writeTimeout time.Duration) error {
	if l.dead.Load() {
		return errors.New("relay ctrl link dead")
	}
	l.writeMu.Lock()
	defer l.writeMu.Unlock()
	_ = l.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	return l.conn.WriteMessage(websocket.BinaryMessage, frame)
}

func (l *ctrlLink) close() {
	l.dead.Store(true)
	_ = l.conn.Close()
}

// dialCtrlLink opens and authenticates the control connection. Runs in its
// own goroutine after a successful main Connect when the server advertised
// FrameCapDualConn; every failure path just logs and leaves the client on
// the single-connection behavior.
func (c *Client) dialCtrlLink() {
	c.mu.Lock()
	url := c.serverURL
	writeTimeout := c.writeTimeout
	pingInterval := c.pingInterval
	pongTimeout := c.pongTimeout
	logger := c.logger()
	c.mu.Unlock()

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	conn, _, err := dialer.DialContext(ctx, url, nil)
	cancel()
	if err != nil {
		logger.Warn("relay ctrl link dial failed — staying single-connection", "error", err)
		return
	}

	link := &ctrlLink{conn: conn}
	if err := c.ctrlHandshake(link, writeTimeout); err != nil {
		logger.Warn("relay ctrl link auth failed — staying single-connection", "error", err)
		link.close()
		return
	}
	link.lastRead.Store(time.Now().UnixNano())
	c.ctrl.Store(link)
	logger.Info("relay ctrl link established (#88② dual connection)")
	go c.ctrlReadLoop(link)
	go c.ctrlPingLoop(link, pingInterval, pongTimeout, writeTimeout)
}

// ctrlHandshake authenticates the ctrl connection: the same NaCl box flow
// with one conn_type byte (0x01) appended after the 109-byte legacy
// payload, and the #64 identity proof verified against our fresh nonce.
func (c *Client) ctrlHandshake(link *ctrlLink, writeTimeout time.Duration) error {
	var nonce [NonceLen]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("nonce: %w", err)
	}

	encrypted := box.Seal(nil, c.pubKey[:], &nonce, &c.serverPubKey, &c.privKey)
	payload := make([]byte, 0, KeyLen+NonceLen+len(encrypted)+1)
	payload = append(payload, c.pubKey[:]...)
	payload = append(payload, nonce[:]...)
	payload = append(payload, encrypted...)
	payload = append(payload, 0x01) // conn_type = ctrl (#88②)

	if err := link.writeFrame(encodeFrame(FrameClientAuth, payload), writeTimeout); err != nil {
		return fmt.Errorf("write auth: %w", err)
	}

	_ = link.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, data, err := link.conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("read auth response: %w", err)
	}
	ft, respPayload, err := readFrame(data)
	if err != nil {
		return fmt.Errorf("parse auth response: %w", err)
	}
	if ft != FrameServerAuth {
		return fmt.Errorf("expected server_auth, got 0x%02x", ft)
	}
	if len(respPayload) < 1 || respPayload[0] != 0x00 {
		return fmt.Errorf("relay ctrl auth rejected (status=0x%02x)", respPayload[0])
	}
	const proofLen = 24 + 48
	if len(respPayload) >= 2+proofLen {
		if err := c.verifyServerAuthProof(respPayload[2:], nonce); err != nil {
			return err
		}
	}
	return nil
}

// ctrlReadLoop mirrors the main readLoop's dispatch: recv_packets on the
// ctrl lane feed the same per-source routes, pings get pongs. A read error
// DEGRADES the link (clear + close) instead of failing the client.
func (c *Client) ctrlReadLoop(link *ctrlLink) {
	c.mu.Lock()
	pongTimeout := c.pongTimeout
	writeTimeout := c.writeTimeout
	c.mu.Unlock()
	for {
		_ = link.conn.SetReadDeadline(time.Now().Add(pongTimeout))
		_, data, err := link.conn.ReadMessage()
		if err != nil {
			select {
			case <-c.closeCh:
				return
			default:
			}
			c.degradeCtrl(link, fmt.Errorf("relay ctrl read: %w", err))
			return
		}
		link.lastRead.Store(time.Now().UnixNano())

		ft, payload, err := readFrame(data)
		if err != nil {
			continue
		}
		switch FrameBase(ft) {
		case FrameRecvPacket:
			if len(payload) < KeyLen {
				continue
			}
			var src [32]byte
			copy(src[:], payload[:KeyLen])
			c.mu.Lock()
			h := c.routes[src]
			if h == nil {
				h = c.onMessage
			}
			c.mu.Unlock()
			if h != nil {
				h(payload[KeyLen:])
			}
		case FramePing:
			_ = link.writeFrame(encodeFrame(FramePong, nil), writeTimeout)
		case FrameKeepAlive, FramePong:
			// liveness only
		default:
		}
	}
}

// ctrlPingLoop keeps the ctrl connection alive on the same cadence as the
// main one. Death degrades (the server's idle reap then clears its
// registry slot on its own).
func (c *Client) ctrlPingLoop(link *ctrlLink, interval, pongTimeout, writeTimeout time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-c.closeCh:
			return
		case <-ticker.C:
			last := time.Unix(0, link.lastRead.Load())
			if since := time.Since(last); since > pongTimeout {
				c.degradeCtrl(link, fmt.Errorf("relay ctrl keepalive: no inbound frame for %s",
					since.Truncate(time.Second)))
				return
			}
			if err := link.writeFrame(encodeFrame(FramePing, nil), writeTimeout); err != nil {
				c.degradeCtrl(link, fmt.Errorf("relay ctrl ping: %w", err))
				return
			}
		}
	}
}

// degradeCtrl clears a dead ctrl link: compare-and-swap so only the
// discovering goroutine logs, and SendPriority falls back to the main
// connection's negotiated high lane.
func (c *Client) degradeCtrl(link *ctrlLink, cause error) {
	if !link.dead.Swap(true) {
		c.logger().Warn("relay ctrl link degraded — priority falls back to shared lane (#88②)",
			"error", cause)
	}
	if cur := c.ctrl.Load(); cur == link {
		c.ctrl.CompareAndSwap(link, nil)
	}
	link.close()
}

// CtrlLinkActive reports whether the physically separate control
// connection is currently live (#88② — diagnostics and tests).
func (c *Client) CtrlLinkActive() bool {
	l := c.ctrl.Load()
	return l != nil && !l.dead.Load()
}

// sendViaCtrl writes one priority frame on the ctrl link when it is live.
// Returns false when the caller should use the shared-connection path.
func (c *Client) sendViaCtrl(dst [32]byte, data []byte) bool {
	link := c.ctrl.Load()
	if link == nil || link.dead.Load() {
		return false
	}
	c.mu.Lock()
	writeTimeout := c.writeTimeout
	c.mu.Unlock()
	payload := make([]byte, KeyLen+len(data))
	copy(payload[:KeyLen], dst[:])
	copy(payload[KeyLen:], data)
	return link.writeFrame(encodeFrame(FrameSendPacket|FramePriorityBit, payload), writeTimeout) == nil
}
