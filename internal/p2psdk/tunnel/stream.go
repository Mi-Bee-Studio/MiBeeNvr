package tunnel

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// streamConn implements net.Conn for a tunnel stream.
type streamConn struct {
	tunnel   *Tunnel
	streamID uint32
	service  string
	logger   *slog.Logger

	// sealed marks a device-side stream whose incoming open negotiated E2EE —
	// every outbound data frame is sealed from the first byte.
	sealed bool

	// prioUsed counts wire bytes this stream has already spent on the
	// high-priority lane; once it passes the tunnel's budget the stream
	// demotes one-way (see Tunnel.EnableEgressPriority).
	prioUsed atomic.Int64

	mu         sync.Mutex
	conn       net.Conn
	closed     bool
	remoteDone bool

	dataCh   chan []byte
	readBuf  []byte
	readOff  int
	wakeCh   chan struct{}
	wakeOnce sync.Once
}

func newStreamConn(tun *Tunnel, id uint32, svc string, log *slog.Logger) *streamConn {
	return &streamConn{
		tunnel:   tun,
		streamID: id,
		service:  svc,
		logger:   log.With("stream_id", id),
		dataCh:   make(chan []byte, 64),
		wakeCh:   make(chan struct{}),
	}
}

func (sc *streamConn) setConn(c net.Conn) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.conn = c
}

func (sc *streamConn) pushData(data []byte) {
	select {
	case sc.dataCh <- data:
	default:
		n := sc.tunnel.droppedFrames.Add(1)
		sc.logger.Warn("data channel full, dropping frame",
			"dropped_total", n, "queue", cap(sc.dataCh))
	}
}

func (sc *streamConn) closeRemote() {
	sc.mu.Lock()
	sc.remoteDone = true
	sc.mu.Unlock()
	sc.wakeOnce.Do(func() { close(sc.wakeCh) })
}

func (sc *streamConn) Read(b []byte) (int, error) {
	for {
		sc.mu.Lock()
		if sc.readOff < len(sc.readBuf) {
			n := copy(b, sc.readBuf[sc.readOff:])
			sc.readOff += n
			if sc.readOff >= len(sc.readBuf) {
				sc.readBuf = nil
				sc.readOff = 0
			}
			sc.mu.Unlock()
			return n, nil
		}
		sc.mu.Unlock()

		// Non-blocking: try to drain remaining dataCh before EOF
		sc.mu.Lock()
		stillClosed := sc.closed || sc.remoteDone
		sc.mu.Unlock()
		if stillClosed {
			select {
			case data, ok := <-sc.dataCh:
				if !ok {
					return 0, io.EOF
				}
				sc.mu.Lock()
				sc.readBuf = data
				sc.readOff = 0
				sc.mu.Unlock()
				continue
			default:
				return 0, io.EOF
			}
		}

		select {
		case data, ok := <-sc.dataCh:
			if !ok {
				return 0, io.EOF
			}
			sc.mu.Lock()
			sc.readBuf = data
			sc.readOff = 0
			sc.mu.Unlock()
		case <-sc.wakeCh:
		}
	}
}

// Write implements net.Conn.Write.
func (sc *streamConn) Write(b []byte) (int, error) {
	fragmenter := NewFragmenter(sc.streamID, sc.tunnel.fragmentSize())
	fragments, err := fragmenter.Fragment(b)
	if err != nil {
		return 0, fmt.Errorf("fragment: %w", err)
	}
	for _, frag := range fragments {
		if sc.sealed || sc.tunnel.e2eeActive() {
			if err := sc.tunnel.sealFragment(&frag); err != nil {
				return 0, fmt.Errorf("tunnel: seal: %w", err)
			}
		}
		msg := TunnelMessage{Type: TunnelMessageTypeData, Payload: mustJSON(frag)}
		data, err := json.Marshal(msg)
		if err != nil {
			return 0, fmt.Errorf("marshal tunnel msg: %w", err)
		}
		if sendErr := sc.tunnel.sendEgress(data, sc.claimEgressPriority(len(data))); sendErr != nil {
			return 0, fmt.Errorf("send: %w", sendErr)
		}
	}
	return len(b), nil
}

// claimEgressPriority atomically charges n wire bytes against this stream's
// high-lane budget. One-way demotion: once the budget is spent the stream
// stays on the normal lane — strict-priority draining then preserves
// intra-stream ordering for any high→normal frame split.
func (sc *streamConn) claimEgressPriority(n int) bool {
	budget := sc.tunnel.egressPrioBudget.Load()
	if budget <= 0 {
		return false
	}
	for {
		cur := sc.prioUsed.Load()
		if cur >= budget {
			return false
		}
		if sc.prioUsed.CompareAndSwap(cur, cur+int64(n)) {
			return true
		}
	}
}

// Close implements net.Conn.Close.
func (sc *streamConn) Close() error {
	sc.mu.Lock()
	sc.closed = true
	sc.mu.Unlock()

	sc.wakeOnce.Do(func() { close(sc.wakeCh) })

	_ = sc.tunnel.sendMsg(TunnelMessage{
		Type:    TunnelMessageTypeClose,
		Payload: mustJSON(TunnelClose{StreamID: sc.streamID}),
	})
	sc.tunnel.registry.CloseStream(sc.streamID)
	sc.tunnel.streams.Delete(sc.streamID)

	sc.mu.Lock()
	if sc.conn != nil {
		_ = sc.conn.Close()
	}
	sc.mu.Unlock()
	return nil
}

func (sc *streamConn) LocalAddr() net.Addr { return fakeAddr{net: "tunnel", str: sc.service} }

func (sc *streamConn) RemoteAddr() net.Addr               { return fakeAddr{net: "tunnel", str: sc.service} }
func (sc *streamConn) SetDeadline(_ time.Time) error      { return nil }
func (sc *streamConn) SetReadDeadline(_ time.Time) error  { return nil }
func (sc *streamConn) SetWriteDeadline(_ time.Time) error { return nil }

type fakeAddr struct {
	net string
	str string
}

func (a fakeAddr) Network() string { return a.net }
func (a fakeAddr) String() string  { return a.str }

func mustJSON(v interface{}) string {
	data, err := json.Marshal(v)
	if err != nil {
		panic("tunnel: marshal: " + err.Error())
	}
	return string(data)
}
