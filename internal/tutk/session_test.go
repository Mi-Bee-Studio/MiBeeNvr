// SPDX-License-Identifier: MIT
//
// TUTK P2P transport adapted from go2rtc (https://github.com/AlexxIT/go2rtc)
// Copyright (c) go2rtc contributors
// Licensed under the MIT License.

package tutk

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func TestSession16_New(t *testing.T) {
	sid8 := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	s := NewSession16(client, sid8)

	// sid16 should be: first 2 bytes of sid8, pad 2 bytes, 0x0c, pad 3 bytes, then sid8
	expected := make([]byte, 16)
	expected[0] = sid8[0]
	expected[1] = sid8[1]
	expected[4] = 0x0c
	copy(expected[8:], sid8)

	if !bytes.Equal(s.sid16, expected) {
		t.Errorf("sid16 mismatch:\ngot:  %x\nwant: %x", s.sid16, expected)
	}
}

func TestSession16_ClientStart(t *testing.T) {
	sid8 := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	client := &mockConn{}
	s := NewSession16(client, sid8)

	msg := s.ClientStart(0, "testuser", "testpass")

	// Verify magic header
	if !bytes.HasPrefix(msg, []byte(magic)) {
		t.Errorf("msg should start with magic")
	}

	// Verify stage is 0x0a (connected)
	if msg[3] != 0x0a {
		t.Errorf("stage should be 0x0a, got 0x%02x", msg[3])
	}

	// Verify size field
	expectedSize := uint16(len(msg) - 16)
	gotSize := binary.LittleEndian.Uint16(msg[4:])
	if gotSize != expectedSize {
		t.Errorf("size mismatch: got %d, want %d", gotSize, expectedSize)
	}

	// Verify sid16 is present
	if !bytes.Equal(msg[12:28], s.sid16) {
		t.Errorf("sid16 mismatch in msg")
	}

	// Verify username is in data area (offset from msgHhrSize + cmdHdrSize)
	msgHhrSize := 28
	cmdHdrSize := 24
	data := msg[msgHhrSize+cmdHdrSize:]
	if string(data[:8]) != "testuser" {
		t.Errorf("username mismatch: got %q", string(data[:8]))
	}
}

func TestSession16_ClientStart_SecondStage(t *testing.T) {
	sid8 := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	client := &mockConn{}
	s := NewSession16(client, sid8)

	msg := s.ClientStart(1, "user", "pass")

	// For second stage (i=1), cmd[1] should be 0x20
	cmd := msg[msgHhrSize:]
	if cmd[1] != 0x20 {
		t.Errorf("cmd[1] should be 0x20 for second stage, got 0x%02x", cmd[1])
	}
}

func TestSession16_SendIOCtrl(t *testing.T) {
	sid8 := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	client := &mockConn{}
	s := NewSession16(client, sid8)

	// seqSendCmd1 starts at 0, SendIOCtrl increments to 1
	ctrlType := uint32(0x01000001)
	ctrlData := []byte{0x0a, 0x0b, 0x0c}
	msg := s.SendIOCtrl(ctrlType, ctrlData)

	// Verify command header
	cmd := msg[msgHhrSize:]
	if string(cmd[:4]) != "\x00\x70\x0b\x00" {
		t.Errorf("SendIOCtrl command header mismatch: got %x", cmd[:4])
	}

	// Verify seq is 1 (first call, starts from 0, incremented to 1)
	seq := binary.LittleEndian.Uint16(cmd[4:])
	if seq != 1 {
		t.Errorf("SendIOCtrl seq should be 1, got %d", seq)
	}

	// Verify ctrlType in data
	data := cmd[cmdHdrSize:]
	gotType := binary.LittleEndian.Uint32(data)
	if gotType != ctrlType {
		t.Errorf("ctrlType mismatch: got 0x%08x, want 0x%08x", gotType, ctrlType)
	}

	// Verify ctrlData follows
	if !bytes.Equal(data[4:], ctrlData) {
		t.Errorf("ctrlData mismatch: got %x, want %x", data[4:], ctrlData)
	}
}

func TestSession16_SendFrameData(t *testing.T) {
	sid8 := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	client := &mockConn{}
	s := NewSession16(client, sid8)

	frameInfo := []byte{0x01, 0x02, 0x03, 0x04}
	frameData := []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee}
	msg := s.SendFrameData(frameInfo, frameData)

	// Verify command header starts with 0x01 0x03
	cmd := msg[msgHhrSize:]
	if string(cmd[:4]) != "\x01\x03\x0b\x00" {
		t.Errorf("SendFrameData command header mismatch: got %x", cmd[:4])
	}

	// Verify dataSize in command
	n := uint16(len(frameData))
	dataSize := n + 8 + 32
	gotSize := binary.LittleEndian.Uint16(cmd[16:])
	if gotSize != dataSize {
		t.Errorf("dataSize mismatch: got %d, want %d", gotSize, dataSize)
	}

	// Verify frame data in payload
	data := cmd[cmdHdrSize:]
	if !bytes.Equal(data[:n], frameData) {
		t.Errorf("frameData mismatch in payload")
	}

	// Verify "ODUA " trailer
	if string(data[n:n+5]) != "ODUA " {
		t.Errorf("missing ODUA trailer: got %x", data[n:n+5])
	}
	// Verify frame info is a prefix of the trailer area
	trailer := data[n+8:]
	if !bytes.HasPrefix(trailer, frameInfo) {
		t.Errorf("frameInfo mismatch at end of payload: got %x, want prefix %x", trailer, frameInfo)
	}
}

func TestSession16_SessionWrite(t *testing.T) {
	s := NewSession16(&mockConn{}, []byte{1, 2, 3, 4, 5, 6, 7, 8})

	// Initial seq should be 0
	if s.seqSendCh0 != 0 {
		t.Errorf("initial seqSendCh0 should be 0, got %d", s.seqSendCh0)
	}

	// Create a test message via Msg
	msg := s.Msg(100)

	// Write on ch0, should set seq at buf[6]
	oldSeq := s.seqSendCh0
	if err := s.SessionWrite(0, msg); err != nil {
		t.Fatalf("SessionWrite ch0: %v", err)
	}

	// Verify seq was incremented
	if s.seqSendCh0 != oldSeq+1 {
		t.Errorf("seqSendCh0 should be %d, got %d", oldSeq+1, s.seqSendCh0)
	}

	// Verify seq was written to buffer
	writtenSeq := binary.LittleEndian.Uint16(msg[6:])
	if writtenSeq != oldSeq {
		t.Errorf("written seq should be %d, got %d", oldSeq, writtenSeq)
	}

	// Write on ch1
	msg2 := s.Msg(100)
	if err := s.SessionWrite(1, msg2); err != nil {
		t.Fatalf("SessionWrite ch1: %v", err)
	}

	// Verify ch1 channel marker
	if msg2[14] != 1 {
		t.Errorf("ch1 should have buf[14] = 1, got %d", msg2[14])
	}
}

func TestSession16_SessionRead_Unknown(t *testing.T) {
	s := NewSession16(&mockConn{}, []byte{1, 2, 3, 4, 5, 6, 7, 8})

	// Unknown command
	result := s.SessionRead(0, []byte{0xff, 0xff, 0x0b, 0x00})
	if result != msgUnknown {
		t.Errorf("expected msgUnknown, got %d", result)
	}
}

func TestSession16_SessionRead_CommandAck(t *testing.T) {
	s := NewSession16(&mockConn{}, []byte{1, 2, 3, 4, 5, 6, 7, 8})

	// Test command ack (cmd[0]=0x00, cmd[1]=0x71)
	result := s.SessionRead(0, []byte{
		0x00, 0x71, 0x0b, 0x00, // cmd + version
		0x01, 0x00, 0x00, 0x00, // seq
		0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
		0xff, 0xff, 0xff, 0xff, // random
	})
	if result != msgCommandAck {
		t.Errorf("expected msgCommandAck, got %d", result)
	}
}

func TestSession16_RecvIOCtrl(t *testing.T) {
	s := NewSession16(&mockConn{}, []byte{1, 2, 3, 4, 5, 6, 7, 8})

	expectedType := uint32(0x0102)
	expectedData := []byte{0x0a, 0x0b}
	buf := make([]byte, 6) // 4 bytes type + 2 bytes data
	binary.LittleEndian.PutUint32(buf, expectedType)
	copy(buf[4:], expectedData)

	go func() {
		// Simulate an IOCtrl message from the camera
		// Must be exactly 24+6=30 bytes so cmd[24:] produces only 6 bytes (type+data)
		cmd := make([]byte, 30)
		cmd[0] = 0x00
		cmd[1] = 0x70
		copy(cmd[24:], buf)
		s.SessionRead(0, cmd)
}()

	// Give it time to process
	time.Sleep(50 * time.Millisecond)

	ctrlType, ctrlData, err := s.RecvIOCtrl()
	if err != nil {
		t.Fatalf("RecvIOCtrl error: %v", err)
	}
	if ctrlType != expectedType {
		t.Errorf("ctrlType mismatch: got 0x%08x, want 0x%08x", ctrlType, expectedType)
	}
	if !bytes.Equal(ctrlData, expectedData) {
		t.Errorf("ctrlData mismatch: got %x, want %x", ctrlData, expectedData)
	}
}

func TestSession16_RecvFrameData(t *testing.T) {
	s := NewSession16(&mockConn{}, []byte{1, 2, 3, 4, 5, 6, 7, 8})

	expectedHdr := []byte{0x01, 0x02, 0x03, 0x04}
	expectedPayload := []byte{0xaa, 0xbb, 0xcc}

go func() {
		// Simulate a frame data command (cmd[0]=0x01, cmd[1]=0x04)
		// cmd must be exactly 24 + hdrSize + payloadSize to avoid extra trailing zeros
		totalSize := 24 + len(expectedHdr) + len(expectedPayload)
		cmd := make([]byte, totalSize)
		cmd[0] = 0x01
		cmd[1] = 0x04
		binary.LittleEndian.PutUint16(cmd[14:], uint16(len(expectedHdr)))
		copy(cmd[24:], expectedHdr)
		copy(cmd[24+len(expectedHdr):], expectedPayload)
		s.SessionRead(0, cmd)
}()

	time.Sleep(50 * time.Millisecond)

	hdr, payload, err := s.RecvFrameData()
	if err != nil {
		t.Fatalf("RecvFrameData error: %v", err)
	}
	if !bytes.Equal(hdr, expectedHdr) {
		t.Errorf("hdr mismatch: got %x, want %x", hdr, expectedHdr)
	}
	if !bytes.Equal(payload, expectedPayload) {
		t.Errorf("payload mismatch: got %x, want %x", payload, expectedPayload)
	}
}

func TestSession16_Close(t *testing.T) {
	s := NewSession16(&mockConn{}, []byte{1, 2, 3, 4, 5, 6, 7, 8})

	// Close should not panic
	s.Close()

	// Recv should return error after close
	_, _, err := s.RecvIOCtrl()
	if err == nil {
		t.Errorf("RecvIOCtrl should return error after Close")
	}
}

// --- ReorderBuffer tests ---

func TestReorderBuffer_New(t *testing.T) {
	t.Helper()
	rb := NewReorderBuffer(5)
	if rb.size != 5 {
		t.Errorf("size should be 5, got %d", rb.size)
	}
	if len(rb.buf) != 0 {
		t.Errorf("buf should be empty, got %d items", len(rb.buf))
	}
	if rb.seq != 0 {
		t.Errorf("initial seq should be 0, got %d", rb.seq)
	}
}

func TestReorderBuffer_InOrder(t *testing.T) {
	rb := NewReorderBuffer(5)

	for i := uint16(0); i < 5; i++ {
		if !rb.Check(i) {
			t.Errorf("Check(%d) should be true (in-order)", i)
		}
		rb.Next()
	}
}

func TestReorderBuffer_OutOfOrder(t *testing.T) {
	rb := NewReorderBuffer(5)

	// Expecting seq 0 but get seq 2
	if rb.Check(2) {
		t.Errorf("Check(2) should be false (out of order)")
	}

	rb.Push(2, []byte("seq2"))
	rb.Push(1, []byte("seq1"))

	// Still should be waiting for seq 0
	if !rb.Check(0) {
		t.Errorf("Check(0) should still be true")
	}

	// Now seq 0 arrives
	rb.Next()

	// Pop should return seq 1 then seq 2
	data1 := rb.Pop()
	if string(data1) != "seq1" {
		t.Errorf("Pop() after seq0 should return seq1, got %q", string(data1))
	}

	data2 := rb.Pop()
	if string(data2) != "seq2" {
		t.Errorf("Pop() should return seq2, got %q", string(data2))
	}

	// No more items
	got := rb.Pop()
	if got != nil {
		t.Errorf("Pop() on empty buffer should return nil")
	}
}

func TestReorderBuffer_BufferFullDrop(t *testing.T) {
	rb := NewReorderBuffer(2) // size 2

	// Fill buffer with out-of-order items
	rb.Push(2, []byte("seq2"))
	rb.Push(3, []byte("seq3"))

	// Available should be 0
	if rb.Available() != 0 {
		t.Errorf("Available should be 0, got %d", rb.Available())
	}

	// Push another - should replace (no error, just map overwrite)
	rb.Push(4, []byte("seq4"))
}

func TestReorderBuffer_PopEmpty(t *testing.T) {
	rb := NewReorderBuffer(5)

	// Pop on empty buffer with available slots should return nil
	result := rb.Pop()
	if result != nil {
		t.Errorf("Pop on empty buffer should return nil, got %v", result)
	}
}

func TestReorderBuffer_PopFullSkip(t *testing.T) {
	rb := NewReorderBuffer(1) // only 1 slot

	// Push seq 1 (not seq 0), filling the buffer
	rb.Push(1, []byte("seq1"))

	// Pop should detect buffer is full and skip seq 0, then return seq 1
	data := rb.Pop()
	if string(data) != "seq1" {
		t.Errorf("Pop should return seq1 after skipping seq 0, got %q", string(data))
	}

	// seq should now be 2
	if rb.seq != 2 {
		t.Errorf("seq should be 2 after skipping 0 and 1, got %d", rb.seq)
	}
}

// --- msgAck tests ---

func TestMsgAckPing(t *testing.T) {
	msg := []byte{
		0x04, 0x02, 0x12, 0x0a, 0x08, 0x00, 0x00, 0x00,
		0x28, 0x04, 0x12, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x5b, 0x0d, 0x42, 0x02, 0x07, 0x0a, 0xa8, 0xc0,
	}

	result := msgAckPing(msg)

	// Verify modifications
	if result[8] != 0x27 {
		t.Errorf("msg[8] should be 0x27, got 0x%02x", result[8])
	}
	if result[10] != 0x21 {
		t.Errorf("msg[10] should be 0x21, got 0x%02x", result[10])
	}

	// Verify rest unchanged
	if result[0] != 0x04 || result[1] != 0x02 {
		t.Errorf("magic bytes modified")
	}
}

func TestMsgAckCh5(t *testing.T) {
	msg := make([]byte, 48)
	msg[8] = 0x07
	msg[10] = 0x21
	msg[32] = 0x41

	result := msgAckCh5(msg)

	if result[8] != 0x07 {
		t.Errorf("msg[8] should be 0x07, got 0x%02x", result[8])
	}
	if result[10] != 0x21 {
		t.Errorf("msg[10] should be 0x21, got 0x%02x", result[10])
	}
	if result[32] != 0x41 {
		t.Errorf("msg[32] should be 0x41, got 0x%02x", result[32])
	}
}

// --- ConnectByUID tests ---

func TestConnectByUID_Broadcast(t *testing.T) {
	uid := "TESTUID1234567"
	sid8 := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}

	b := ConnectByUID(stageBroadcast, uid, sid8)

	// Length should be 68
	if len(b) != 68 {
		t.Errorf("broadcast packet should be 68 bytes, got %d", len(b))
	}

	// Verify magic
	if !bytes.HasPrefix(b, []byte(magic)) {
		t.Errorf("packet should start with magic")
	}

	// Verify stage = 0x02
	if b[3] != 0x02 {
		t.Errorf("stage should be 0x02, got 0x%02x", b[3])
	}

	// Verify size field
	size := binary.LittleEndian.Uint16(b[4:])
	if int(size) != len(b)-16 {
		t.Errorf("size should be %d, got %d", len(b)-16, size)
	}

	// Verify UID is at offset 16
	if string(b[16:16+len(uid)]) != uid {
		t.Errorf("UID mismatch at offset 16")
	}

	// Verify sdk version at offset 52
	if string(b[52:56]) != sdkVersion {
		t.Errorf("SDK version mismatch at offset 52")
	}

	// Verify sid at offset 56
	if !bytes.Equal(b[56:64], sid8) {
		t.Errorf("sid mismatch at offset 56")
	}

	// Verify stage value at offset 64
	if b[64] != stageBroadcast {
		t.Errorf("stage value should be %d, got %d", stageBroadcast, b[64])
	}
}

func TestConnectByUID_GetRemoteIP(t *testing.T) {
	uid := "TESTUID1234567"
	sid8 := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}

	b := ConnectByUID(stageGetRemoteIP, uid, sid8)

	// Length should be 112
	if len(b) != 112 {
		t.Errorf("GetRemoteIP packet should be 112 bytes, got %d", len(b))
	}

	// Verify command at offset 8
	if string(b[8:11]) != "\x03\x02\x34" {
		t.Errorf("command mismatch at offset 8: got %x", b[8:11])
	}

	// Verify sid at offset 100
	if !bytes.Equal(b[100:108], sid8) {
		t.Errorf("sid mismatch at offset 100")
	}

	// Verify stage = stageDirect at offset 108
	if b[108] != stageDirect {
		t.Errorf("stage value should be %d, got %d", stageDirect, b[108])
	}
}

// --- mockConn ---

// mockConn implements net.Conn with no-op operations for testing.
type mockConn struct {
	net.Conn
}

func (m *mockConn) Read(b []byte) (int, error) {
	return len(b), nil
}

func (m *mockConn) Write(b []byte) (int, error) {
	return len(b), nil
}

func (m *mockConn) Close() error {
	return nil
}

func (m *mockConn) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}
}

func (m *mockConn) RemoteAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}
}

func (m *mockConn) SetDeadline(t time.Time) error {
	return nil
}

func (m *mockConn) SetReadDeadline(t time.Time) error {
	return nil
}

func (m *mockConn) SetWriteDeadline(t time.Time) error {
	return nil
}
