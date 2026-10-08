package sntp

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"testing"
	"time"
)

// freeUDPPort asks the OS for an available UDP port (bind :0, read it, close).
func freeUDPPort(t *testing.T) int {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer pc.Close()
	return pc.LocalAddr().(*net.UDPAddr).Port
}

// waitRunning blocks until the server reports its socket bound. A UDP dial
// succeeds even before the server binds (connectionless), so dial-success
// cannot be the readiness signal — the first packet would be refused.
func waitRunning(t *testing.T, srv *Server) {
	t.Helper()
	for range 200 {
		if srv.Running() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server never came up")
}

func TestServer_ResponseShape(t *testing.T) {
	t.Helper()
	port := freeUDPPort(t)
	srv := NewServer(fmt.Sprintf("127.0.0.1:%d", port))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	waitRunning(t, srv)

	conn, err := net.Dial("udp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Client request: LI=0 VN=4 Mode=3, zero timestamps.
	req := make([]byte, 48)
	req[0] = 0x23 // LI=0 | VN=4 | Mode=3(client)
	clientTransmit := toNTPTime(time.Now())
	binary.BigEndian.PutUint64(req[40:], clientTransmit)

	if _, err := conn.Write(req); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	resp := make([]byte, 48)
	if _, err := conn.Read(resp); err != nil {
		t.Fatalf("read: %v", err)
	}

	// Mode = server(4), version echoed (4), LI = 0.
	if got := resp[0] >> 6 & 0x3; got != 0 {
		t.Errorf("LI = %d, want 0", got)
	}
	if got := resp[0] >> 3 & 0x7; got != 4 {
		t.Errorf("VN = %d, want 4", got)
	}
	if got := resp[0] & 0x7; got != 4 {
		t.Errorf("Mode = %d, want 4 (server)", got)
	}
	if resp[1] != 1 {
		t.Errorf("stratum = %d, want 1", resp[1])
	}
	if string(resp[4:8]) != "LOCL" {
		t.Errorf("refid = %q, want LOCL", string(resp[4:8]))
	}
	// Originate echoes the client transmit timestamp.
	if orig := binary.BigEndian.Uint64(resp[24:]); orig != clientTransmit {
		t.Errorf("originate = %#x, want client transmit %#x", orig, clientTransmit)
	}
	// Transmit timestamp is within a few seconds of now.
	tx := binary.BigEndian.Uint64(resp[40:])
	nowNTP := toNTPTime(time.Now())
	diff := int64(tx>>32) - int64(nowNTP>>32)
	if diff < -2 || diff > 2 {
		t.Errorf("transmit off by %ds, want ≈0", diff)
	}

	cancel()
	_ = srv.Stop()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("Start did not return after cancel")
	}
}

func TestServer_IgnoresShortPackets(t *testing.T) {
	t.Helper()
	port := freeUDPPort(t)
	srv := NewServer(fmt.Sprintf("127.0.0.1:%d", port))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Start(ctx) }()
	waitRunning(t, srv)

	conn, err := net.Dial("udp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write(make([]byte, 20)); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 64)
	if n, err := conn.Read(buf); err == nil {
		t.Errorf("short packet got a %d-byte response, want silence", n)
	}
	_ = srv.Stop()
}

func TestToNTPTime(t *testing.T) {
	t.Helper()
	// 1970-01-01T00:00:00Z → 2208988800s in NTP epoch.
	got := toNTPTime(time.Unix(0, 0))
	if want := uint64(2208988800) << 32; got != want {
		t.Errorf("toNTPTime(epoch) = %#x, want %#x", got, want)
	}
	if toNTPTime(time.Time{}) != 0 {
		t.Error("zero time should map to 0")
	}
}
