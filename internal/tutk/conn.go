// SPDX-License-Identifier: MIT
//
// TUTK P2P transport adapted from go2rtc (https://github.com/AlexxIT/go2rtc)
// Copyright (c) go2rtc contributors
// Licensed under the MIT License.

package tutk

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Dial establishes a TUTK P2P connection to a device identified by UID.
// host: IP address with optional port (default 32761).
// uid: device UID string.
// username/password: authentication credentials.
func Dial(host, uid, username, password string) (*Conn, error) {
	addr, err := net.ResolveUDPAddr("udp", host)
	if err != nil {
		// Default port for listening incoming LAN connections.
		// Important. It's not used for real connection.
		addr = &net.UDPAddr{IP: net.ParseIP(host), Port: 32761}
	}

	udpConn, err := net.ListenUDP("udp", nil)
	if err != nil {
		return nil, err
	}

	c := &Conn{
		UDPConn:     udpConn,
		addr:        addr,
		idleTimeout: 30 * time.Second,
		lastData:    time.Now(),
	}

	sid := GenSessionID()

	_ = c.SetDeadline(time.Now().Add(5 * time.Second))

	if addr.Port != 10001 {
		err = c.connectDirect(uid, sid)
	} else {
		err = c.connectRemote(uid, sid)
	}
	if err != nil {
		_ = c.Close()
		return nil, err
	}

	if c.ver[0] >= 25 {
		c.session = NewSession25(c, sid)
	} else {
		c.session = NewSession16(c, sid)
	}

	if err = c.clientStart(username, password); err != nil {
		_ = c.Close()
		return nil, err
	}

	go c.worker()

	return c, nil
}

// Conn wraps a TUTK P2P connection over UDP.
type Conn struct {
	*net.UDPConn
	addr    *net.UDPAddr
	session Session

	ver    []byte
	err    error
	cmdMu  sync.Mutex
	cmdAck func()

	mu          sync.Mutex
	idleTimeout time.Duration
	lastData    time.Time
}

func (c *Conn) setErr(err error) {
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
}

func (c *Conn) getErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Read overrides net.Conn.Read. It reads from the UDP connection, filters by
// remote IP, and applies ReverseTransCodePartial on the payload.
func (c *Conn) Read(buf []byte) (n int, err error) {
	for {
		var addr *net.UDPAddr
		if n, addr, err = c.UDPConn.ReadFromUDP(buf); err != nil {
			return 0, err
		}

		if string(c.addr.IP) != string(addr.IP) || n < 16 {
			continue // skip messages from another IP
		}

		if c.addr.Port != addr.Port {
			c.addr.Port = addr.Port
		}

		ReverseTransCodePartial(buf, buf[:n])

		c.mu.Lock()
		c.lastData = time.Now()
		c.mu.Unlock()

		return n, nil
	}
}

// Write overrides net.Conn.Write. It applies TransCodePartial on the payload
// and sends to the remote address.
func (c *Conn) Write(b []byte) (n int, err error) {
	return c.UDPConn.WriteToUDP(TransCodePartial(nil, b), c.addr)
}

// RemoteAddr overrides net.Conn.RemoteAddr.
func (c *Conn) RemoteAddr() net.Addr {
	return c.addr
}

// Protocol returns the transport protocol string.
func (c *Conn) Protocol() string {
	return "tutk"
}

// Version returns the TUTK version string.
func (c *Conn) Version() string {
	if len(c.ver) == 1 {
		return fmt.Sprintf("TUTK/%d", c.ver[0])
	}
	return fmt.Sprintf("TUTK/%d SDK %d.%d.%d.%d", c.ver[0], c.ver[1], c.ver[2], c.ver[3], c.ver[4])
}

// ReadCommand reads a command from the camera (blocking).
func (c *Conn) ReadCommand() (ctrlType uint32, ctrlData []byte, err error) {
	return c.session.RecvIOCtrl()
}

// WriteCommand sends a command to the camera with retry and ACK tracking.
func (c *Conn) WriteCommand(ctrlType uint32, ctrlData []byte) error {
	c.cmdMu.Lock()
	defer c.cmdMu.Unlock()

	var repeat atomic.Int32
	repeat.Store(5)

	timeout := time.NewTicker(time.Second)
	defer timeout.Stop()

	c.cmdAck = func() {
		repeat.Store(0)
		timeout.Reset(1)
	}

	buf := c.session.SendIOCtrl(ctrlType, ctrlData)

	for {
		if err := c.session.SessionWrite(0, buf); err != nil {
			return err
		}
		<-timeout.C
		r := repeat.Add(-1)
		if r < 0 {
			return nil
		}
		if r == 0 {
			return fmt.Errorf("tutk: can't send command %d", ctrlType)
		}
	}
}

// ReadPacket reads a media data packet from the camera (blocking).
func (c *Conn) ReadPacket() (hdr, payload []byte, err error) {
	return c.session.RecvFrameData()
}

// WritePacket sends a media data packet to the camera.
func (c *Conn) WritePacket(hdr, payload []byte) error {
	buf := c.session.SendFrameData(hdr, payload)
	return c.session.SessionWrite(1, buf)
}

// Error returns the connection error, or io.EOF if cleanly closed.
func (c *Conn) Error() error {
	if c.getErr() != nil {
		return c.getErr()
	}
	return io.EOF
}

func (c *Conn) worker() {
	defer c.workerExitGuard()
	defer c.session.Close()

	buf := make([]byte, 1200)

	for {
		_ = c.UDPConn.SetReadDeadline(time.Now().Add(c.idleTimeout))

		n, err := c.Read(buf)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				c.mu.Lock()
				idle := time.Since(c.lastData) > c.idleTimeout
				c.mu.Unlock()
				if idle {
					c.setErr(fmt.Errorf("tutk: no data for %v", c.idleTimeout))
					return
				}
				continue
			}
			c.setErr(fmt.Errorf("tutk: %w", err))
			return
		}

		switch c.handleMsg(buf[:n]) {
		case msgUnknown:
			// Silently ignore unknown messages
		case msgError:
			return
		case msgCommandAck:
			if c.cmdAck != nil {
				c.cmdAck()
			}
		}
	}
}

// workerExitGuard ensures c.err is set when worker() exits.
// Must be called as a defer in worker(). Handles panic recovery and
// ensures a descriptive error is always present (never bare io.EOF).
func (c *Conn) workerExitGuard() {
	if r := recover(); r != nil {
		c.setErr(fmt.Errorf("tutk: panic: %v", r))
	}
	if c.getErr() == nil {
		c.setErr(fmt.Errorf("tutk: connection closed"))
	}
}

// Message type constants returned by handleMsg.
const (
	msgUnknown = iota
	msgError
	msgPing
	msgUnknownPing
	msgClientStart
	msgClientStart2
	msgClientStartAck2
	msgCommand
	msgCommandAck
	msgCounters
	msgMediaChunk
	msgMediaFrame
	msgMediaReorder
	msgMediaLost
	msgCh5

	msgUnknown0007 // time sync without data
	msgUnknown0008 // time sync with data
	msgUnknown0010
	msgUnknown0013
	msgUnknown0900
	msgUnknown0a08
	msgUnknownCh1c
	msgDafang0012
)

func (c *Conn) handleMsg(msg []byte) int {
	// off sample
	// 0   0402      tutk magic
	// 2   120a      tutk version (120a, 190a...)
	// 4   0800      msg size = len(b)-16
	// 6   0000      channel seq
	// 8   28041200  msg type
	// 14  0100      channel (not all msg)
	// 28  0700      msg data (not all msg)
	switch msg[8] {
	case 0x08:
		switch ch := msg[14]; ch {
		case 0, 1:
			return c.session.SessionRead(ch, msg[28:])
		case 5:
			if len(msg) == 48 {
				_, _ = c.Write(msgAckCh5(msg))
				return msgCh5
			}
		case 0x1c:
			return msgUnknownCh1c
		}
	case 0x18:
		return msgUnknownPing
	case 0x28:
		if len(msg) == 24 {
			_, _ = c.Write(msgAckPing(msg))
			return msgPing
		}
	}
	return msgUnknown
}

// msgAckPing responds to a TUTK ping by modifying the message type.
//
//	<- [24] 0402120a 08000000 28041200 000000005b0d4202070aa8c0
//	-> [24] 04021a0a 08000000 27042100 000000005b0d4202070aa8c0
func msgAckPing(msg []byte) []byte {
	msg[8] = 0x27
	msg[10] = 0x21
	return msg
}

// msgAckCh5 responds to a TUTK channel 5 message.
//
//	<- [48] 0402190a 20000400 07042100 7ecc05000c0000007ecc93c456c2561f ...
//	-> [48] 0402190a 20000400 08041200 7ecc05000c0000007ecc93c456c2561f ...
func msgAckCh5(msg []byte) []byte {
	msg[8] = 0x07
	msg[10] = 0x21
	msg[32] = 0x41
	return msg
}
