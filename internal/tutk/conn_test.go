// SPDX-License-Identifier: MIT
//
// TUTK P2P transport adapted from go2rtc (https://github.com/AlexxIT/go2rtc)
// Copyright (c) go2rtc contributors
// Licensed under the MIT License.

package tutk

import (
	"fmt"
	"net"
	"testing"
	"time"
)

func TestTransCodeRoundTrip(t *testing.T) {
	original := []byte{
		0x04, 0x02, 0x19, 0x02, 0x34, 0x00, 0x00, 0x00,
		0x01, 0x06, 0x21, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x54, 0x45, 0x53, 0x54, 0x55, 0x49, 0x44, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x06, 0x00, 0x03, 0x03, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00,
	}

	// TransCode and then ReverseTransCode should yield the original
	encoded := TransCodePartial(nil, original)
	if len(encoded) != len(original) {
		t.Fatalf("encoded length %d != original %d", len(encoded), len(original))
	}

	decoded := make([]byte, len(encoded))
	ReverseTransCodePartial(decoded, encoded)

	if len(decoded) != len(original) {
		t.Fatalf("decoded length %d != original %d", len(decoded), len(original))
	}

	for i := range original {
		if decoded[i] != original[i] {
			t.Errorf("byte %d mismatch: got 0x%02x, want 0x%02x", i, decoded[i], original[i])
		}
	}
}

func TestTransCodeShortInput(t *testing.T) {
	// Test TransCode/ReverseTransCode with input < 16 bytes
	original := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}

	encoded := TransCodePartial(nil, original)
	if len(encoded) != len(original) {
		t.Fatalf("encoded length %d != original %d", len(encoded), len(original))
	}

	decoded := make([]byte, len(encoded))
	ReverseTransCodePartial(decoded, encoded)

	for i := range original {
		if decoded[i] != original[i] {
			t.Errorf("byte %d mismatch: got 0x%02x, want 0x%02x", i, decoded[i], original[i])
		}
	}
}

func TestTransCodeMultiBlock(t *testing.T) {
	// Test with enough data to exercise the 16-byte block loop
	original := make([]byte, 64)
	for i := range original {
		original[i] = byte(i)
	}

	encoded := TransCodePartial(nil, original)
	decoded := make([]byte, len(encoded))
	ReverseTransCodePartial(decoded, encoded)

	for i := range original {
		if decoded[i] != original[i] {
			t.Errorf("byte %d mismatch: got 0x%02x, want 0x%02x", i, decoded[i], original[i])
		}
	}
}

func TestWorkerExitGuard_Panic(t *testing.T) {
	// Setup: create a Conn with a throwaway UDP listener
	udpConn, err := net.ListenUDP("udp", nil)
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	defer udpConn.Close()

	c := &Conn{
		UDPConn: udpConn,
	}

	// workerExitGuard should handle panic and set error
	func() {
		defer c.workerExitGuard()
		panic("test panic")
	}()

	if c.getErr() == nil {
		t.Fatal("error should be set after panic recovery")
	}
	errStr := c.getErr().Error()
	if errStr != "tutk: panic: test panic" {
		t.Errorf("unexpected error message: %s", errStr)
	}
}

func TestWorkerExitGuard_NormalExit(t *testing.T) {
	udpConn, err := net.ListenUDP("udp", nil)
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	defer udpConn.Close()

	c := &Conn{
		UDPConn: udpConn,
	}

	// workerExitGuard should set "connection closed" when no error is set
	c.workerExitGuard()

	if c.getErr() == nil {
		t.Fatal("error should be set after workerExitGuard")
	}
	errStr := c.getErr().Error()
	if errStr != "tutk: connection closed" {
		t.Errorf("unexpected error message: %s", errStr)
	}
}

func TestErrorDefault(t *testing.T) {
	udpConn, err := net.ListenUDP("udp", nil)
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	defer udpConn.Close()

	c := &Conn{UDPConn: udpConn}

	// Error should return io.EOF when no error set
	if c.Error() == nil {
		t.Fatal("Error() should not return nil")
	}
	if c.Error().Error() != "EOF" {
		t.Errorf("Error() should return EOF, got: %v", c.Error())
	}
}

func TestErrorWithErr(t *testing.T) {
	udpConn, err := net.ListenUDP("udp", nil)
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	defer udpConn.Close()

	c := &Conn{UDPConn: udpConn}
	c.setErr(fmt.Errorf("custom error"))

	if c.Error() == nil {
		t.Fatal("Error() should not return nil")
	}
	if c.Error().Error() != "custom error" {
		t.Errorf("Error() should return 'custom error', got: %v", c.Error())
	}
}

func TestProtocol(t *testing.T) {
	udpConn, err := net.ListenUDP("udp", nil)
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	defer udpConn.Close()

	c := &Conn{UDPConn: udpConn}
	if c.Protocol() != "tutk" {
		t.Errorf("Protocol() should return 'tutk', got %q", c.Protocol())
	}
}

func TestVersion(t *testing.T) {
	tests := []struct {
		ver  []byte
		want string
	}{
		{[]byte{0x19}, "TUTK/25"},
		{[]byte{0x10}, "TUTK/16"},
		{[]byte{0x19, 0x06, 0x00, 0x03, 0x03}, "TUTK/25 SDK 6.0.3.3"},
		{[]byte{0x10, 0x06, 0x00, 0x03, 0x03}, "TUTK/16 SDK 6.0.3.3"},
	}

	for _, tc := range tests {
		c := &Conn{ver: tc.ver}
		got := c.Version()
		if got != tc.want {
			t.Errorf("Version() with ver=%v: got %q, want %q", tc.ver, got, tc.want)
		}
	}
}

func TestSetGetErr(t *testing.T) {
	c := &Conn{}

	if err := c.getErr(); err != nil {
		t.Errorf("getErr() on fresh Conn should be nil, got %v", err)
	}

	c.setErr(fmt.Errorf("test error"))
	if err := c.getErr(); err == nil || err.Error() != "test error" {
		t.Errorf("getErr() should return 'test error', got %v", err)
	}
}

// TestIdleTimeoutBehavior tests that the idle timeout detection logic works
// correctly by setting lastData far in the past.
func TestIdleTimeoutBehavior(t *testing.T) {
	udpConn, err := net.ListenUDP("udp", nil)
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	defer udpConn.Close()

	c := &Conn{
		UDPConn:     udpConn,
		idleTimeout: 50 * time.Millisecond,
		lastData:    time.Now().Add(-1 * time.Hour), // far in the past
	}
	// Session must be set to avoid nil dereference in worker defer
	c.session = NewSession16(&mockConn{}, GenSessionID())

	// Use a done channel to detect worker exit
	done := make(chan struct{})
	go func() {
		c.worker()
		close(done)
	}()

	// Worker should exit within a few hundred ms due to idle timeout
	select {
	case <-done:
		// Success - worker detected idle timeout
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit within timeout - idle detection failed")
	}

	// Verify error is set
	if c.getErr() == nil {
		t.Fatal("error should be set after idle timeout")
	}
	errStr := c.getErr().Error()
	if errStr != "tutk: no data for 50ms" {
		t.Errorf("unexpected error: %s", errStr)
	}
}

// newKeepaliveTestConn builds a Conn wired to a fake camera UDP socket for
// keepalive wire-format tests. Returns the client Conn and the fake camera.
func newKeepaliveTestConn(t *testing.T) (*Conn, *net.UDPConn) {
	t.Helper()

	camera, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("camera ListenUDP: %v", err)
	}
	t.Cleanup(func() { _ = camera.Close() })

	client, err := net.ListenUDP("udp", nil)
	if err != nil {
		t.Fatalf("client ListenUDP: %v", err)
	}

	c := &Conn{
		UDPConn:     client,
		addr:        camera.LocalAddr().(*net.UDPAddr),
		uid:         "TESTUID",
		idleTimeout: 30 * time.Second,
		lastData:    time.Now(),
	}
	c.session = NewSession25(c, GenSessionID())
	return c, camera
}

// readDecoded reads one obfuscated packet from the fake camera and reverses
// the wire obfuscation.
func readDecoded(t *testing.T, camera *net.UDPConn) []byte {
	t.Helper()

	buf := make([]byte, 1200)
	_ = camera.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := camera.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("camera ReadFromUDP: %v", err)
	}
	msg := buf[:n]
	ReverseTransCodePartial(msg, msg)
	return msg
}

func TestSendKeepaliveWireFormat(t *testing.T) {
	c, camera := newKeepaliveTestConn(t)

	if !c.sendKeepalive() {
		t.Fatal("sendKeepalive should report success on Session25")
	}

	msg := readDecoded(t, camera)
	if len(msg) < msgHhrSize+4 {
		t.Fatalf("keepalive packet too short: %d bytes", len(msg))
	}

	// Header: client-request marker at [8:11] and channel-0 dispatch shape.
	if string(msg[8:11]) != "\x07\x04\x21" {
		t.Errorf("keepalive header [8:11] = %x, want 070421", msg[8:11])
	}
	// Payload starts at 28 with the counters command 09 00 0b 00.
	if string(msg[msgHhrSize:msgHhrSize+4]) != "\x09\x00\x0b\x00" {
		t.Errorf("keepalive cmd = %x, want 09000b00", msg[msgHhrSize:msgHhrSize+4])
	}
}

func TestSendKeepaliveGating(t *testing.T) {
	c, _ := newKeepaliveTestConn(t)

	t.Run("session16 has no counters builder", func(t *testing.T) {
		c16 := &Conn{UDPConn: c.UDPConn, addr: c.addr, uid: c.uid}
		c16.session = NewSession16(&mockConn{}, GenSessionID())
		if c16.sendKeepalive() {
			t.Error("sendKeepalive should be a no-op on Session16")
		}
	})

	t.Run("env off", func(t *testing.T) {
		t.Setenv("TUTK_KEEPALIVE", "off")
		if c.sendKeepalive() {
			t.Error("sendKeepalive should be disabled via TUTK_KEEPALIVE=off")
		}
	})
}

func TestWorkerSendsKeepaliveWhenIdle(t *testing.T) {
	orig := keepaliveInterval
	keepaliveInterval = 100 * time.Millisecond
	t.Cleanup(func() { keepaliveInterval = orig })

	c, camera := newKeepaliveTestConn(t)

	done := make(chan struct{})
	go func() {
		c.worker()
		close(done)
	}()
	t.Cleanup(func() {
		_ = c.UDPConn.Close() // unblocks worker read; it will exit via error
		<-done
	})

	// With no inbound data the worker should emit several keepalives.
	first := readDecoded(t, camera)
	if string(first[msgHhrSize:msgHhrSize+4]) != "\x09\x00\x0b\x00" {
		t.Errorf("expected counters keepalive, got cmd %x", first[msgHhrSize:msgHhrSize+4])
	}

	select {
	case <-done:
		t.Fatal("worker exited prematurely — idle timeout should be 30s")
	default:
	}
}

// TestStaleWriteDeadlineKillsWrites is the regression test for issue #167's
// root cause: Dial used to arm a combined SetDeadline(5s) for the handshake
// and never cleared it, so every outbound packet after dial+5s (per-frame
// counters ACKs, MISS commands, keepalives) failed with a silent
// "write udp: i/o timeout". The camera, starved of ACKs, stopped streaming
// ~20s later. This test pins the failure mechanism: a stale write deadline
// breaks keepalive writes; clearing the deadline restores them.
func TestStaleWriteDeadlineKillsWrites(t *testing.T) {
	c, camera := newKeepaliveTestConn(t)

	// Simulate the old Dial: deadline set 5s ago and forgotten.
	_ = c.UDPConn.SetDeadline(time.Now().Add(-5 * time.Second))

	if c.sendKeepalive() {
		t.Fatal("sendKeepalive should fail under a stale write deadline")
	}

	// Fix under test: handshake clears the deadline before the worker runs.
	_ = c.UDPConn.SetDeadline(time.Time{})

	if !c.sendKeepalive() {
		t.Fatal("sendKeepalive should succeed after deadline is cleared")
	}

	msg := readDecoded(t, camera)
	if string(msg[msgHhrSize:msgHhrSize+4]) != "\x09\x00\x0b\x00" {
		t.Errorf("expected counters keepalive, got cmd %x", msg[msgHhrSize:msgHhrSize+4])
	}
}
