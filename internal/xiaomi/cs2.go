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
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

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
	go c.worker()
	return c, nil
}

// CS2Conn wraps a CS2 P2P connection (UDP or TCP) to a Xiaomi device.
type CS2Conn struct {
	net.Conn
	isTCP bool
	idleTimeout time.Duration

	mu   sync.Mutex
	err    error
	seqCh0 uint16
	seqCh3 uint16

	channels [4]*cs2DataChannel

	cmdMu  sync.Mutex
	cmdAck func()
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

	const (
		pingInterval = 1 * time.Second
	)
	var keepaliveTS time.Time // only for TCP
	lastData := time.Now()
	buf := make([]byte, 1200)

	for {
		// Set a short read deadline for TCP to wake up and send keepalive pings.
		// For UDP use the full idle timeout since there is no ping mechanism.
		if c.isTCP {
			_ = c.Conn.SetReadDeadline(time.Now().Add(pingInterval))
		} else {
			_ = c.Conn.SetReadDeadline(time.Now().Add(c.idleTimeout))
		}

		n, err := c.Conn.Read(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// TCP: send keepalive ping on each timeout wakeup.
				if c.isTCP && time.Now().After(keepaliveTS) {
					_, _ = c.Conn.Write([]byte{cs2Magic, cs2MsgPing, 0, 0})
					keepaliveTS = time.Now().Add(pingInterval)
				}

				// Detect truly dead connection: no data for idleTimeout.
				if time.Since(lastData) > c.idleTimeout {
					c.setErr(fmt.Errorf("cs2: no data for %v", c.idleTimeout))
					return
				}
				continue
			}
			c.setErr(fmt.Errorf("cs2: %w", err))
			return
		}

		lastData = time.Now()
		if c.isTCP {
			keepaliveTS = time.Now().Add(pingInterval)
		}

		switch buf[1] {
		case cs2MsgDrw:
			ch := buf[5]
			channel := c.channels[ch]

			if c.isTCP {
				err = channel.Push(buf[8:n])
			} else {
				var pushed int

				seqHI, seqLO := buf[6], buf[7]
				seq := uint16(seqHI)<<8 | uint16(seqLO)
				pushed, err = channel.PushSeq(seq, buf[8:n])

				if pushed >= 0 {
					// For UDP we should send ACK.
					ack := []byte{cs2Magic, cs2MsgDrwAck, 0, 6, cs2MagicDrw, ch, 0, 1, seqHI, seqLO}
					_, _ = c.Conn.Write(ack)
				}
			}

			if err != nil {
				c.setErr(fmt.Errorf("cs2: %w", err))
				return
			}

		case cs2MsgPing:
		case cs2MsgPong, cs2MsgP2PRdyUDP, cs2MsgP2PRdyTCP, cs2MsgClose, cs2MsgCloseAck: // skip
		case cs2MsgDrwAck: // only for UDP
			if c.cmdAck != nil {
				c.cmdAck()
			}
		default:
			// unknown message type, silently ignore
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
	return
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

	c.cmdAck = func() {
		repeat.Store(0)
		timeout.Reset(1)
	}

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
			return
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

func (c *cs2TCPConn) Read(p []byte) (n int, err error) {
	tmp := make([]byte, 8)
	if _, err = io.ReadFull(c.rd, tmp); err != nil {
		return
	}
	n = int(binary.BigEndian.Uint16(tmp))
	if len(p) < n {
		return 0, fmt.Errorf("cs2 tcp: buffer too small")
	}
	_, err = io.ReadFull(c.rd, p[:n])
	return
}

func (c *cs2TCPConn) Write(req []byte) (n int, err error) {
	n = len(req)
	buf := make([]byte, 8+n)
	binary.BigEndian.PutUint16(buf, uint16(n))
	buf[2] = cs2MagicTCP
	copy(buf[8:], req)
	_, err = c.TCPConn.Write(buf)
	return
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
