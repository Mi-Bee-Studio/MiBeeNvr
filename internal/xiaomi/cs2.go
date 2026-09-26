// SPDX-License-Identifier: MIT
//
// Xiaomi CS2 P2P transport adapted from go2rtc (https://github.com/AlexxIT/go2rtc)
// Copyright (c) go2rtc contributors
// Licensed under the MIT License.

package xiaomi

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"
)

var cs2Logger = slogx.Component("xiaomi-cs2")

// CS2Dial establishes a CS2 P2P connection to a Xiaomi device.
// transport: "udp" (default), "tcp", or "" (tries both).
func CS2Dial(host, transport string, idleTimeout time.Duration) (*CS2Conn, error) {
	conn, err := cs2Handshake(host, transport)
	if err != nil {
		return nil, err
	}

	_, isTCP := conn.(*cs2TCPConn)

	c := &CS2Conn{
		Conn:  conn,
		isTCP: isTCP,
		channels: [4]*cs2DataChannel{
			newCS2DataChannel(0, 10), nil, newCS2DataChannel(250, 100), nil,
		},
	}
	if idleTimeout == 0 {
		c.idleTimeout = defaultIdleTimeout
	} else {
		c.idleTimeout = idleTimeout
	}
	c.pingInterval = cs2PingInterval
	go c.worker()
	return c, nil
}

// CS2Conn wraps a CS2 P2P connection (UDP or TCP) to a Xiaomi device.
//
// # Lock-order audit (#234): no mu ↔ cmdMu cycle exists.
//
// The two mutexes are never nested by any goroutine:
//   - mu    protects only `err` (via setErr/getErr). The worker goroutine is
//     its only writer; Read*/Error helpers read it. None of these take
//     cmdMu.
//   - cmdMu serializes WriteCommand (channel-0 command sends + UDP ACK retry
//     loop). While holding cmdMu, WriteCommand does NOT take mu — it
//     touches seqCh0 (mutation is serialized by cmdMu itself, so no
//     race) and sets c.cmdAck, then blocks on a timer.
//
// The worker's cs2MsgDrwAck path invokes c.cmdAck() (which unblocks a pending
// WriteCommand) WITHOUT holding mu or cmdMu, so there is no nested acquisition
// in either direction. The PTZ/motor/two-way-audio command paths all go through
// WriteCommand (under cmdMu) and none re-enter mu.
type CS2Conn struct {
	net.Conn
	isTCP       bool
	idleTimeout time.Duration
	// pingInterval is the client PING cadence on TCP sessions; see
	// cs2PingInterval. Worker-goroutine only. Set by CS2Dial; tests may
	// override it to keep the cadence observable without real sleeps.
	pingInterval time.Duration

	mu     sync.Mutex
	err    error
	seqCh0 uint16
	seqCh3 uint16

	channels [4]*cs2DataChannel

	cmdMu sync.Mutex
	// cmdAck holds the ACK callback for an in-flight UDP WriteCommand.
	// atomic: the worker goroutine reads/fires it without cmdMu while
	// WriteCommand stores it under cmdMu (issue #503 race fix).
	cmdAck atomic.Pointer[func()]
	// LogKey identifies the peer in control-write failure logs
	// ("model@host", set by the MISS layer). Empty = fall back to
	// RemoteAddr. Worker-goroutine only (#503).
	LogKey string
	// writeFail counts consecutive control-write failures per frame name.
	// Worker-goroutine only — no lock needed (#503).
	writeFail map[string]int
	// stats is the worker's message timeline for the disconnect forensics
	// log below. Worker-goroutine only — no lock needed (#906).
	stats cs2Stats
}

// cs2Stats tracks the control-frame timeline of one CS2 session. The
// camera tears the connection down ~6s after its liveness probe goes
// unanswered (see the cs2MsgPing case in worker); when that happens the
// forensics line below answers WHICH side of the PING/PONG exchange died
// (no PING ever arrived / PONG sent but ignored / stream misparse) without
// a packet capture (#906).
type cs2Stats struct {
	startedAt     time.Time
	pingSent      int // our keepalive PINGs (TCP) / data-pings
	pongSent      int // PONG replies to camera PING probes
	pingRecv      int // camera PING probes observed
	pongRecv      int
	drwRecv       int // media frames
	unknownRecv   int // frames that matched no known message type
	lastPingSent  time.Time
	lastPongSent  time.Time
	lastPingRecv  time.Time
	lastDataAt    time.Time
	lastUnknownTy byte // message type of the most recent unknown frame
	bufTooSmall   int  // TCP frames larger than the 1200B read buffer
}

const (
	cs2Magic        = 0xF1
	cs2MagicDrw     = 0xD1
	cs2MagicTCP     = 0x68
	cs2MsgLanSearch = 0x30
	cs2MsgPunchPkt  = 0x41
	cs2MsgP2PRdyUDP = 0x42
	cs2MsgP2PRdyTCP = 0x43
	cs2MsgDrw       = 0xD0
	cs2MsgDrwAck    = 0xD1
	cs2MsgPing      = 0xE0
	cs2MsgPong      = 0xE1
	cs2MsgClose     = 0xF0
	cs2MsgCloseAck  = 0xF1
)
const defaultIdleTimeout = 30 * time.Second

const cs2HdrSize = 32

// cs2ReadTimeout is the timeout for Pop() calls in ReadPacket and ReadCommand.
// If no data arrives within this period, the call returns a timeout error.
const cs2ReadTimeout = 15 * time.Second

// cs2PingInterval is the client PING cadence on CS2 TCP sessions. Xiaomi
// cameras expect the client to PING roughly once per second (observed from
// the official Mi Home app; go2rtc parity) and tear the session down after
// their ~6s liveness window otherwise. This is a protocol fact, not a
// tunable; it lives as a CS2Conn field only so tests can shrink it.
const cs2PingInterval = time.Second

// cs2ReadBufSize sizes the worker read buffer. CS2-over-TCP frames carry a
// BE16 length in their 8-byte header, so a single frame may be up to 64KiB:
// cameras chunk routine media to ~1KiB but emit larger frames for HD
// keyframes and encoder parameter refreshes, and a buffer shorter than the
// frame kills the session outright ("cs2 tcp: buffer too small" — observed
// in the field on 2026-09-25 once sessions survived long enough to receive
// one). UDP datagrams are far smaller and simply ignore the extra capacity.
const cs2ReadBufSize = 65536

// cs2PingPolicy decides when the next client-initiated PING is due on a CS2
// TCP session. Inbound data does NOT postpone the next PING — only sending
// one does. The camera's ~6s liveness window counts client PINGs, not
// traffic: sessions that stream media but never PING get FIN'd anyway
// (issue #906; 2026-09-24 packet capture — 7/7 media-flowing TCP sessions
// with zero client pings, camera FIN at +6.19..6.28s, while UDP sessions
// lived indefinitely on per-frame DRW acks alone).
type cs2PingPolicy struct {
	interval time.Duration
	next     time.Time
}

// due reports whether a PING should be sent at instant now.
func (p *cs2PingPolicy) due(now time.Time) bool { return !now.Before(p.next) }

// markSent records a PING sent at instant now, scheduling the next one.
func (p *cs2PingPolicy) markSent(now time.Time) { p.next = now.Add(p.interval) }

func cs2Handshake(host, transport string) (net.Conn, error) {
	conn, err := cs2NewUDPConn(host, 32108)
	if err != nil {
		return nil, err
	}

	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	req := []byte{cs2Magic, cs2MsgLanSearch, 0, 0}
	res, err := conn.(*cs2UDPConn).WriteUntil(req, func(res []byte) bool {
		return res[1] == cs2MsgPunchPkt
	})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	var msgUDP, msgTCP byte

	if transport == "" || transport == "udp" {
		msgUDP = cs2MsgP2PRdyUDP
	}
	if transport == "" || transport == "tcp" {
		msgTCP = cs2MsgP2PRdyTCP
	}

	res, err = conn.(*cs2UDPConn).WriteUntil(res, func(res []byte) bool {
		return res[1] == msgUDP || res[1] == msgTCP
	})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	_ = conn.SetDeadline(time.Time{})

	if res[1] == msgTCP {
		_ = conn.Close()
		return cs2NewTCPConn(conn.RemoteAddr().String())
	}

	return conn, nil
}

func (c *CS2Conn) worker() {
	defer c.workerExitGuard()

	defer func() {
		c.channels[0].Close()
		c.channels[2].Close()
	}()

	// #906 forensics: on exit, dump the session's control-frame timeline.
	defer func() {
		s := c.stats
		uptime := time.Since(s.startedAt).Round(time.Millisecond)
		age := func(t time.Time) string {
			if t.IsZero() {
				return "never"
			}
			return time.Since(t).Round(time.Millisecond).String() + " ago"
		}
		cs2Logger.Info("cs2: session closed — control-frame timeline",
			"peer", c.peer(), "proto", c.Protocol(), "uptime", uptime.String(),
			"err", c.getErr(),
			"ping_sent", s.pingSent, "last_ping_sent", age(s.lastPingSent),
			"ping_recv", s.pingRecv, "last_ping_recv", age(s.lastPingRecv),
			"pong_sent", s.pongSent, "last_pong_sent", age(s.lastPongSent),
			"pong_recv", s.pongRecv,
			"drw_recv", s.drwRecv, "last_data", age(s.lastDataAt),
			"unknown_recv", s.unknownRecv, "last_unknown_type", fmt.Sprintf("0x%02X", s.lastUnknownTy),
			"buf_too_small", s.bufTooSmall)
	}()
	c.stats.startedAt = time.Now()
	ping := cs2PingPolicy{interval: c.pingInterval}
	lastData := time.Now()
	buf := make([]byte, cs2ReadBufSize)

	for {
		// Short read deadline for TCP to wake up and send keepalive PINGs
		// while idle; UDP has no ping mechanism, so it waits the full idle
		// timeout instead.
		if c.isTCP {
			_ = c.Conn.SetReadDeadline(time.Now().Add(c.pingInterval))
		} else {
			_ = c.Conn.SetReadDeadline(time.Now().Add(c.idleTimeout))
		}

		n, err := c.Conn.Read(buf)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				// TCP: no inbound for a full interval — send keepalive PING.
				if c.isTCP {
					if now := time.Now(); ping.due(now) {
						c.writeControl("keepalive-ping", []byte{cs2Magic, cs2MsgPing, 0, 0})
						c.stats.pingSent++
						c.stats.lastPingSent = now
						ping.markSent(now)
					}
				}

				// Detect truly dead connection: no data for idleTimeout.
				if time.Since(lastData) > c.idleTimeout {
					c.setErr(fmt.Errorf("cs2: no data for %v", c.idleTimeout))
					return
				}
				continue
			}
			var tooSmall *cs2BufTooSmallError
			if errors.As(err, &tooSmall) {
				c.stats.bufTooSmall++
			}
			c.setErr(fmt.Errorf("cs2: %w", err))
			return
		}

		lastData = time.Now()
		c.stats.lastDataAt = lastData

		switch buf[1] {
		case cs2MsgDrw:
			c.stats.drwRecv++
			ch := buf[5]
			channel := c.channels[ch]

			if c.isTCP {
				// Send PING on data receive, matching the official Mi Home app
				// (go2rtc parity). Throttled by the ping policy: inbound data
				// must NOT delay the next PING — see cs2PingPolicy.
				if now := time.Now(); ping.due(now) {
					c.writeControl("data-ping", []byte{cs2Magic, cs2MsgPing, 0, 0})
					c.stats.pingSent++
					c.stats.lastPingSent = now
					ping.markSent(now)
				}
				err = channel.Push(buf[8:n])
			} else {
				var pushed int

				seqHI, seqLO := buf[6], buf[7]
				seq := uint16(seqHI)<<8 | uint16(seqLO)
				pushed, err = channel.PushSeq(seq, buf[8:n])

				if pushed >= 0 {
					// For UDP we should send ACK.
					ack := []byte{cs2Magic, cs2MsgDrwAck, 0, 6, cs2MagicDrw, ch, 0, 1, seqHI, seqLO}
					c.writeControl("drw-ack", ack)
				}
			}

			if err != nil {
				c.setErr(fmt.Errorf("cs2: %w", err))
				return
			}

		case cs2MsgPing:
			// Camera probes client liveness with PING; we MUST reply PONG or it
			// tears down the connection after its retry window (~6s). go2rtc parity.
			c.stats.pingRecv++
			c.stats.lastPingRecv = time.Now()
			c.writeControl("pong", []byte{cs2Magic, cs2MsgPong, 0, 0})
			c.stats.pongSent++
			c.stats.lastPongSent = time.Now()
		case cs2MsgPong:
			c.stats.pongRecv++
		case cs2MsgP2PRdyUDP, cs2MsgP2PRdyTCP, cs2MsgClose, cs2MsgCloseAck: // skip
		case cs2MsgDrwAck: // only for UDP
			if fn := c.cmdAck.Load(); fn != nil {
				(*fn)()
			}
		default:
			// unknown message type, silently ignore — but counted: a stream
			// misparse (e.g. relayed framing drift) surfaces as a burst of
			// unknown types right before the teardown (#906).
			c.stats.unknownRecv++
			c.stats.lastUnknownTy = buf[1]
		}
	}
}

// Protocol returns the transport protocol string ("cs2+tcp" or "cs2+udp").
func (c *CS2Conn) Protocol() string {
	if c.isTCP {
		return "cs2+tcp"
	}
	return "cs2+udp"
}

// Version returns the protocol version string.
func (c *CS2Conn) Version() string {
	return "CS2"
}

func (c *CS2Conn) setErr(err error) {
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
}

// peer returns the log identity for control-write failures: the MISS layer's
// model@host key when set, else the remote address (#503).
func (c *CS2Conn) peer() string {
	if c.LogKey != "" {
		return c.LogKey
	}
	if c.Conn != nil {
		if addr := c.RemoteAddr(); addr != nil {
			return addr.String()
		}
	}
	return "unknown-peer"
}

// writeControl sends an outbound keepalive/ACK control frame (#503). Write
// failures here were silently dropped — an undelivered PONG is followed ~6s
// later by the camera tearing down the connection and a dropped DrwAck by
// camera-side retransmit stalls, so without a log line the ensuing EOF /
// no-media storms cannot be attributed to our side vs the camera (#167
// lesson: silent ACK-write drops made the TUTK starvation invisible).
// A single failure logs at debug (flaky P2P links blip routinely); three or
// more consecutive failures escalate to warn with the running count, so
// dead-link evidence survives even at default INFO level.
//
// Worker-goroutine only.
func (c *CS2Conn) writeControl(frame string, b []byte) {
	if c.writeFail == nil {
		c.writeFail = make(map[string]int)
	}
	if _, err := c.Conn.Write(b); err != nil {
		c.writeFail[frame]++
		if c.writeFail[frame] >= 3 {
			cs2Logger.Warn("cs2: control write failed (consecutive)",
				"peer", c.peer(), "frame", frame,
				"consecutive_failures", c.writeFail[frame], "error", err)
		} else {
			cs2Logger.Debug("cs2: control write failed",
				"peer", c.peer(), "frame", frame,
				"consecutive_failures", c.writeFail[frame], "error", err)
		}
		return
	}
	c.writeFail[frame] = 0
}

func (c *CS2Conn) getErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Error returns the connection error, or io.EOF if cleanly closed.
func (c *CS2Conn) Error() error {
	if c.getErr() != nil {
		return c.getErr()
	}
	return io.EOF
}

// workerExitGuard ensures c.err is set when worker() exits.
// Must be called as a defer in worker(). Handles panic recovery and
// ensures a descriptive error is always present (never bare io.EOF).
func (c *CS2Conn) workerExitGuard() {
	if r := recover(); r != nil {
		c.setErr(fmt.Errorf("cs2: panic: %v", r))
	}
	if c.getErr() == nil {
		c.setErr(fmt.Errorf("cs2: connection closed"))
	}
}

// ReadCommand reads a command response from channel 0.
func (c *CS2Conn) ReadCommand() (cmd uint32, data []byte, err error) {
	buf, ok := c.channels[0].Pop(cs2ReadTimeout)
	if !ok {
		if c.getErr() != nil {
			return 0, nil, c.getErr()
		}
		return 0, nil, fmt.Errorf("cs2: no command data for %v", cs2ReadTimeout)
	}
	cmd = binary.LittleEndian.Uint32(buf)
	data = buf[4:]
	return cmd, data, err
}

// WriteCommand sends a command on channel 0 with ACK retry for UDP.
func (c *CS2Conn) WriteCommand(cmd uint32, data []byte) error {
	c.cmdMu.Lock()
	defer c.cmdMu.Unlock()

	req := cs2MarshalCmd(0, c.seqCh0, cmd, data)
	c.seqCh0++

	if c.isTCP {
		_, err := c.Conn.Write(req)
		return err
	}

	var repeat atomic.Int32
	repeat.Store(5)

	timeout := time.NewTicker(time.Second)
	defer timeout.Stop()

	fn := func() {
		repeat.Store(0)
		timeout.Reset(1)
	}
	c.cmdAck.Store(&fn)
	defer c.cmdAck.Store(nil) // a late DrwAck must not fire a stale callback

	for {
		if _, err := c.Conn.Write(req); err != nil {
			return err
		}
		<-timeout.C
		r := repeat.Add(-1)
		if r < 0 {
			return nil
		}
		if r == 0 {
			return fmt.Errorf("cs2: can't send command %d", cmd)
		}
	}
}

// ReadPacket reads a media packet from channel 2.
func (c *CS2Conn) ReadPacket() (hdr, payload []byte, err error) {
	data, ok := c.channels[2].Pop(cs2ReadTimeout)
	if !ok {
		if c.getErr() != nil {
			return nil, nil, c.getErr()
		}
		return nil, nil, fmt.Errorf("cs2: no media data for %v", cs2ReadTimeout)
	}
	return data[:cs2HdrSize], data[cs2HdrSize:], nil
}

// WritePacket writes a media packet on channel 3.
func (c *CS2Conn) WritePacket(hdr, payload []byte) error {
	const offset = 12

	n := cs2HdrSize + uint32(len(payload))
	req := make([]byte, n+offset)
	req[0] = cs2Magic
	req[1] = cs2MsgDrw
	binary.BigEndian.PutUint16(req[2:], uint16(n+8))

	req[4] = cs2MagicDrw
	req[5] = 3 // channel
	binary.BigEndian.PutUint16(req[6:], c.seqCh3)
	c.seqCh3++
	binary.BigEndian.PutUint32(req[8:], n)
	copy(req[offset:], hdr)
	// Bug-for-bug compat: original copies hdr twice instead of payload.
	// Kept as-is to match go2rtc behavior.
	copy(req[offset+cs2HdrSize:], hdr)

	_, err := c.Conn.Write(req)
	return err
}

func cs2MarshalCmd(channel byte, seq uint16, cmd uint32, payload []byte) []byte {
	size := len(payload)
	req := make([]byte, 4+4+4+4+size)

	// 1. message header (4 bytes)
	req[0] = cs2Magic
	req[1] = cs2MsgDrw
	binary.BigEndian.PutUint16(req[2:], uint16(4+4+4+size))

	// 2. drw header (4 bytes)
	req[4] = cs2MagicDrw
	req[5] = channel
	binary.BigEndian.PutUint16(req[6:], seq)

	// 3. payload size (4 bytes)
	binary.BigEndian.PutUint32(req[8:], uint32(4+size))

	// 4. payload command (4 bytes)
	binary.BigEndian.PutUint32(req[12:], cmd)

	// 5. payload
	copy(req[16:], payload)

	return req
}

func cs2NewUDPConn(host string, port int) (net.Conn, error) {
	conn, err := net.ListenUDP("udp", nil)
	if err != nil {
		return nil, err
	}

	addr, err := net.ResolveUDPAddr("udp", host)
	if err != nil {
		addr = &net.UDPAddr{IP: net.ParseIP(host), Port: port}
	}

	return &cs2UDPConn{UDPConn: conn, addr: addr}, nil
}

type cs2UDPConn struct {
	*net.UDPConn
	addr *net.UDPAddr
}

func (c *cs2UDPConn) Read(b []byte) (n int, err error) {
	var addr *net.UDPAddr
	for {
		n, addr, err = c.UDPConn.ReadFromUDP(b)
		if err != nil {
			return 0, err
		}

		if string(addr.IP) == string(c.addr.IP) || n >= 8 {
			return n, err
		}
	}
}

func (c *cs2UDPConn) Write(b []byte) (n int, err error) {
	return c.UDPConn.WriteToUDP(b, c.addr)
}

func (c *cs2UDPConn) RemoteAddr() net.Addr {
	return c.addr
}

func (c *cs2UDPConn) WriteUntil(req []byte, ok func(res []byte) bool) ([]byte, error) {
	stopRetransmit := make(chan struct{})
	defer close(stopRetransmit)

	go func() {
		time.Sleep(time.Nanosecond)
		for {
			select {
			case <-stopRetransmit:
				return
			default:
			}
			if _, err := c.Write(req); err != nil {
				return
			}
			select {
			case <-stopRetransmit:
				return
			case <-time.After(time.Second):
			}
		}
	}()

	buf := make([]byte, 1200)

	for {
		n, addr, err := c.UDPConn.ReadFromUDP(buf)
		if err != nil {
			return nil, err
		}

		if string(addr.IP) != string(c.addr.IP) || n < 16 {
			continue
		}

		if ok(buf[:n]) {
			c.addr.Port = addr.Port
			return buf[:n], nil
		}
	}
}

func cs2NewTCPConn(addr string) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return nil, err
	}
	return &cs2TCPConn{TCPConn: conn.(*net.TCPConn), rd: bufio.NewReader(conn)}, nil
}

type cs2TCPConn struct {
	*net.TCPConn
	rd *bufio.Reader
}

// cs2BufTooSmallError marks a TCP frame larger than the worker's 1200-byte
// read buffer: the frame is skipped and the stream position advances past
// it, which can desynchronize framing on relays that emit bigger frames
// (#906 forensics counter).
type cs2BufTooSmallError struct{ need int }

func (e *cs2BufTooSmallError) Error() string {
	return fmt.Sprintf("cs2 tcp: buffer too small (frame %d bytes)", e.need)
}

func (c *cs2TCPConn) Read(p []byte) (n int, err error) {
	tmp := make([]byte, 8)
	if _, err = io.ReadFull(c.rd, tmp); err != nil {
		return n, err
	}
	n = int(binary.BigEndian.Uint16(tmp))
	if len(p) < n {
		return 0, &cs2BufTooSmallError{need: n}
	}
	_, err = io.ReadFull(c.rd, p[:n])
	return n, err
}

func (c *cs2TCPConn) Write(req []byte) (n int, err error) {
	n = len(req)
	buf := make([]byte, 8+n)
	binary.BigEndian.PutUint16(buf, uint16(n))
	buf[2] = cs2MagicTCP
	copy(buf[8:], req)
	_, err = c.TCPConn.Write(buf)
	return n, err
}

func newCS2DataChannel(pushSize, popSize int) *cs2DataChannel {
	c := &cs2DataChannel{}
	if pushSize > 0 {
		c.pushBuf = make(map[uint16][]byte, pushSize)
		c.pushSize = pushSize
	}
	if popSize >= 0 {
		c.popBuf = make(chan []byte, popSize)
	}
	return c
}

type cs2DataChannel struct {
	waitSeq  uint16
	pushBuf  map[uint16][]byte
	pushSize int

	waitData []byte
	waitSize int
	popBuf   chan []byte
}

func (c *cs2DataChannel) Push(b []byte) error {
	c.waitData = append(c.waitData, b...)

	for len(c.waitData) > 4 {
		// Every new data starts with size. There can be several data inside one packet.
		if c.waitSize == 0 {
			c.waitSize = int(binary.BigEndian.Uint32(c.waitData))
			c.waitData = c.waitData[4:]
		}
		if c.waitSize > len(c.waitData) {
			break
		}

		select {
		case c.popBuf <- c.waitData[:c.waitSize]:
		default:
			// Drop oldest frame to make room for new one.
			// For video streams, dropping a frame is far better than
			// disconnecting and reconnecting the entire P2P session.
			select {
			case <-c.popBuf:
			default:
			}
			select {
			case c.popBuf <- c.waitData[:c.waitSize]:
			default:
				return fmt.Errorf("cs2: pop buffer still full after drain")
			}
		}

		c.waitData = c.waitData[c.waitSize:]
		c.waitSize = 0
	}
	return nil
}

func (c *cs2DataChannel) Pop(timeout time.Duration) ([]byte, bool) {
	select {
	case data, ok := <-c.popBuf:
		return data, ok
	case <-time.After(timeout):
		return nil, false
	}
}

func (c *cs2DataChannel) Close() {
	close(c.popBuf)
}

// PushSeq returns how many seq were processed.
// Returns 0 if seq was saved or processed earlier.
// Returns -1 if seq could not be saved (buffer full or disabled).
func (c *cs2DataChannel) PushSeq(seq uint16, data []byte) (int, error) {
	diff := int16(seq - c.waitSeq)
	// Check if this is seq from the future.
	if diff > 0 {
		// Support disabled buffer.
		if c.pushSize == 0 {
			return -1, nil
		}
		// Check if we don't have this seq in the buffer.
		if c.pushBuf[seq] == nil {
			// Check if there is enough space in the buffer.
			if len(c.pushBuf) == c.pushSize {
				return -1, nil
			}
			c.pushBuf[seq] = bytes.Clone(data)
		}
		return 0, nil
	}

	// Check if this is seq from the past.
	if diff < 0 {
		return 0, nil
	}

	for i := 1; ; i++ {
		if err := c.Push(data); err != nil {
			return i, err
		}
		c.waitSeq++
		// Check if we have next seq in the buffer.
		if data = c.pushBuf[c.waitSeq]; data != nil {
			delete(c.pushBuf, c.waitSeq)
		} else {
			return i, nil
		}
	}
}
