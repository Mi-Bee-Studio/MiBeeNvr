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
