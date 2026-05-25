// SPDX-License-Identifier: MIT
//
// Xiaomi MISS protocol client tests adapted from go2rtc (https://github.com/AlexxIT/go2rtc)
// Copyright (c) go2rtc contributors
// Licensed under the MIT License.

package xiaomi

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// mockMISSConn is a test mock implementing the MISSConn interface.
type mockMISSConn struct {
	mu          sync.Mutex
	writtenCmds []struct {
		cmd  uint32
		data []byte
	}
	readCmdResp  struct {
		cmd  uint32
		data []byte
	}
	readCmdCalled bool
	closed        bool
}

func (m *mockMISSConn) Protocol() string { return "cs2+udp" }
func (m *mockMISSConn) Version() string  { return "CS2" }
func (m *mockMISSConn) RemoteAddr() net.Addr {
	addr, _ := net.ResolveUDPAddr("udp", "192.168.1.1:32108")
	return addr
}
func (m *mockMISSConn) SetDeadline(t time.Time) error { return nil }

func (m *mockMISSConn) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *mockMISSConn) WriteCommand(cmd uint32, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writtenCmds = append(m.writtenCmds, struct {
		cmd  uint32
		data []byte
	}{cmd: cmd, data: data})
	return nil
}

func (m *mockMISSConn) ReadCommand() (cmd uint32, data []byte, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.readCmdCalled = true
	return m.readCmdResp.cmd, m.readCmdResp.data, nil
}

func (m *mockMISSConn) ReadPacket() (hdr, payload []byte, err error) {
	return nil, nil, fmt.Errorf("not implemented")
}

func (m *mockMISSConn) WritePacket(hdr, payload []byte) error {
	return fmt.Errorf("not implemented")
}

func (m *mockMISSConn) lastWrittenCmd() (uint32, []byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.writtenCmds) == 0 {
		return 0, nil, false
	}
	last := m.writtenCmds[len(m.writtenCmds)-1]
	return last.cmd, last.data, true
}

func newTestMISSClient() (*MISSClient, *mockMISSConn) {
	t := &testing.T{}
	t.Helper()
	pub, priv, err := GenerateKey()
	require.NoError(t, err)
	sharedKey, err := CalcSharedKey(hex.EncodeToString(pub), hex.EncodeToString(priv))
	require.NoError(t, err)

	mock := &mockMISSConn{}
	client := &MISSClient{
		Conn:  mock,
		key:   sharedKey,
		model: ModelC200,
	}
	return client, mock
}

func TestMISSLoginCommand(t *testing.T) {
	t.Helper()
	mock := &mockMISSConn{}
	mock.readCmdResp = struct {
		cmd  uint32
		data []byte
	}{
		cmd:  missCmdAuthRes,
		data: []byte(`{"result":"success","token":"abc"}`),
	}

	err := missLogin(mock, "testpublickey123", "testsign456")
	require.NoError(t, err)

	// Verify WriteCommand was called with auth request
	mock.mu.Lock()
	require.Len(t, mock.writtenCmds, 1)
	cmd := mock.writtenCmds[0]
	mock.mu.Unlock()

	require.Equal(t, uint32(missCmdAuthReq), cmd.cmd)

	// Verify JSON format of login payload
	var loginData map[string]interface{}
	err = json.Unmarshal(cmd.data, &loginData)
	require.NoError(t, err)
	require.Equal(t, "testpublickey123", loginData["public_key"])
	require.Equal(t, "testsign456", loginData["sign"])
	require.Equal(t, "", loginData["uuid"])
	require.Equal(t, float64(0), loginData["support_encrypt"])
}

func TestMISSLoginFailure(t *testing.T) {
	t.Helper()
	mock := &mockMISSConn{}
	mock.readCmdResp = struct {
		cmd  uint32
		data []byte
	}{
		cmd:  missCmdAuthRes,
		data: []byte(`{"result":"failed","reason":"bad_sign"}`),
	}

	err := missLogin(mock, "pub", "bad_sign")
	require.Error(t, err)
	require.Contains(t, err.Error(), "miss: auth:")
}

func TestMISSClientVersion(t *testing.T) {
	t.Helper()
	client, _ := newTestMISSClient()
	ver := client.Version()
	require.Contains(t, ver, "CS2")
	require.Contains(t, ver, ModelC200)
}

func TestMISSClientWriteCommandEncrypts(t *testing.T) {
	t.Helper()
	client, mock := newTestMISSClient()

	plaintext := []byte(`{"videoquality":2}`)
	err := client.WriteCommand(plaintext)
	require.NoError(t, err)

	_, data, ok := mock.lastWrittenCmd()
	require.True(t, ok)
	// Encoded data should have 8-byte nonce prefix and differ from plaintext
	require.Len(t, data, 8+len(plaintext))
	require.NotEqual(t, plaintext, data[8:])
}

func TestMISSClientStartMediaDefaultHD(t *testing.T) {
	t.Helper()
	client, mock := newTestMISSClient()
	client.model = ModelC300 // C300 defaults to quality 3

	err := client.StartMedia("", "")
	require.NoError(t, err)

	cmd, data, ok := mock.lastWrittenCmd()
	require.True(t, ok)
	require.Equal(t, uint32(missCmdEncoded), cmd)

	// Decrypt to check the inner command
	decoded, err := Decode(data, client.key)
	require.NoError(t, err)

	// First 4 bytes should be missCmdVideoStart (big-endian)
	innerCmd := binary.BigEndian.Uint32(decoded)
	require.Equal(t, uint32(missCmdVideoStart), innerCmd)

	// Remaining should contain videoquality 3 for C300
	body := string(decoded[4:])
	require.Contains(t, body, `"videoquality":3`)
}

func TestMISSClientStartMediaSD(t *testing.T) {
	t.Helper()
	client, mock := newTestMISSClient()

	err := client.StartMedia("", "sd")
	require.NoError(t, err)

	_, data, ok := mock.lastWrittenCmd()
	require.True(t, ok)

	decoded, err := Decode(data, client.key)
	require.NoError(t, err)
	body := string(decoded[4:])
	require.Contains(t, body, `"videoquality":1`)
}

func TestMISSClientStartMediaAuto(t *testing.T) {
	t.Helper()
	client, mock := newTestMISSClient()

	err := client.StartMedia("", "auto")
	require.NoError(t, err)

	_, data, ok := mock.lastWrittenCmd()
	require.True(t, ok)

	decoded, err := Decode(data, client.key)
	require.NoError(t, err)
	body := string(decoded[4:])
	require.Contains(t, body, `"videoquality":0`)
}

func TestMISSClientStartMediaSecondChannel(t *testing.T) {
	t.Helper()
	client, mock := newTestMISSClient()
	client.model = "some.other.model" // non-C200/C300 defaults to quality 2

	err := client.StartMedia("1", "hd")
	require.NoError(t, err)

	_, data, ok := mock.lastWrittenCmd()
	require.True(t, ok)

	decoded, err := Decode(data, client.key)
	require.NoError(t, err)
	body := string(decoded[4:])
	require.Contains(t, body, `"videoquality":-1`)
	require.Contains(t, body, `"videoquality2":2`)
}

func TestMISSClientStartMediaDefaultHDNonC200(t *testing.T) {
	t.Helper()
	client, mock := newTestMISSClient()
	client.model = "some.other.model"

	err := client.StartMedia("", "hd")
	require.NoError(t, err)

	_, data, ok := mock.lastWrittenCmd()
	require.True(t, ok)

	decoded, err := Decode(data, client.key)
	require.NoError(t, err)
	body := string(decoded[4:])
	require.Contains(t, body, `"videoquality":2`)
}

func TestMISSClientStopMedia(t *testing.T) {
	t.Helper()
	client, mock := newTestMISSClient()

	err := client.StopMedia()
	require.NoError(t, err)

	_, data, ok := mock.lastWrittenCmd()
	require.True(t, ok)

	decoded, err := Decode(data, client.key)
	require.NoError(t, err)
	innerCmd := binary.BigEndian.Uint32(decoded)
	require.Equal(t, uint32(missCmdVideoStop), innerCmd)
}

func TestMISSPacketSampleRate(t *testing.T) {
	t.Helper()
	tests := []struct {
		name      string
		flags     uint32
		wantRate  uint32
	}{
		{
			name:     "flags with sample rate bits set → 16000",
			flags:    0b000011000, // bits 3-6 = 0b0011 = 3 → nonzero
			wantRate: 16000,
		},
		{
			name:     "flags with zero sample rate bits → 8000",
			flags:    0b000000000,
			wantRate: 8000,
		},
		{
			name:     "flags value from comment example 1_0011_000 → 16000",
			flags:    0b10011000,
			wantRate: 16000,
		},
		{
			name:     "flags value from comment 100_00_01_0000_000 → 8000",
			flags:    0b10000010000000,
			wantRate: 8000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Helper()
			pkt := &MISSPacket{Flags: tt.flags}
			require.Equal(t, tt.wantRate, pkt.SampleRate())
		})
	}
}

func TestMISSCodecConstants(t *testing.T) {
	t.Helper()
	require.Equal(t, uint32(4), uint32(missCodecH264))
	require.Equal(t, uint32(5), uint32(missCodecH265))
	require.Equal(t, uint32(1024), uint32(missCodecPCM))
	require.Equal(t, uint32(1026), uint32(missCodecPCMU))
	require.Equal(t, uint32(1027), uint32(missCodecPCMA))
	require.Equal(t, uint32(1032), uint32(missCodecOPUS))
}

func TestMISSModelConstants(t *testing.T) {
	t.Helper()
	require.Equal(t, "isa.camera.df3", ModelDafang)
	require.Equal(t, "loock.cateye.v02", ModelLoockV2)
	require.Equal(t, "chuangmi.camera.046c04", ModelC200)
	require.Equal(t, "chuangmi.camera.72ac1", ModelC300)
	require.Equal(t, "isa.camera.isc5c1", ModelXiaofang)
	require.Equal(t, "isa.camera.hlc8", ModelHLC8)
}

func TestMISSNewClientUnsupportedVendor(t *testing.T) {
	t.Helper()
	// Provide valid hex keys so we reach the vendor check
	pub, priv, err := GenerateKey()
	require.NoError(t, err)
	pubHex := hex.EncodeToString(pub)
	privHex := hex.EncodeToString(priv)

	url := fmt.Sprintf(
		"miss://192.168.1.1?vendor=tutk&device_public=%s&client_private=%s&client_public=%s&sign=test",
		pubHex, privHex, pubHex,
	)
	_, err = NewMISSClient(url, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported vendor")
}

func TestMISSNewClientBadURL(t *testing.T) {
	t.Helper()
	_, err := NewMISSClient("://bad-url", 0)
	require.Error(t, err)
}

func TestMISSNewClientMissingKeys(t *testing.T) {
	t.Helper()
	_, err := NewMISSClient("miss://192.168.1.1?vendor=cs2", 0)
	require.Error(t, err)
}

func TestMISSReadPacketDecrypts(t *testing.T) {
	t.Helper()
	pub, priv, err := GenerateKey()
	require.NoError(t, err)
	sharedKey, err := CalcSharedKey(hex.EncodeToString(pub), hex.EncodeToString(priv))
	require.NoError(t, err)

	// Build a fake encrypted payload
	plainPayload := []byte("video frame data here")
	encPayload, err := Encode(plainPayload, sharedKey)
	require.NoError(t, err)

	// Build a fake header (32 bytes)
	hdr := make([]byte, missHdrSize)
	binary.LittleEndian.PutUint32(hdr[4:], missCodecH264)    // CodecID
	binary.LittleEndian.PutUint32(hdr[8:], 42)               // Sequence
	binary.LittleEndian.PutUint32(hdr[12:], 0)               // Flags
	binary.LittleEndian.PutUint64(hdr[16:], 12345678)        // Timestamp (msec)

	mock := &mockMISSConnReadPacket{hdr: hdr, payload: encPayload}
	client := &MISSClient{
		Conn:  mock,
		key:   sharedKey,
		model: "test.model",
	}

	pkt, err := client.ReadPacket()
	require.NoError(t, err)
	require.Equal(t, uint32(missCodecH264), pkt.CodecID)
	require.Equal(t, uint32(42), pkt.Sequence)
	require.Equal(t, uint32(0), pkt.Flags)
	require.Equal(t, uint64(12345678), pkt.Timestamp)
	require.Equal(t, plainPayload, pkt.Payload)
}

func TestMISSReadPacketDafangTimestamp(t *testing.T) {
	t.Helper()
	pub, priv, err := GenerateKey()
	require.NoError(t, err)
	sharedKey, err := CalcSharedKey(hex.EncodeToString(pub), hex.EncodeToString(priv))
	require.NoError(t, err)

	plainPayload := []byte("frame")
	encPayload, err := Encode(plainPayload, sharedKey)
	require.NoError(t, err)

	hdr := make([]byte, missHdrSize)
	binary.LittleEndian.PutUint32(hdr[4:], missCodecH265)
	binary.LittleEndian.PutUint64(hdr[16:], 0) // zero timestamp in header

	mock := &mockMISSConnReadPacket{hdr: hdr, payload: encPayload}
	client := &MISSClient{
		Conn:  mock,
		key:   sharedKey,
		model: ModelDafang,
	}

	before := time.Now().UnixMilli()
	pkt, err := client.ReadPacket()
	after := time.Now().UnixMilli()

	require.NoError(t, err)
	// Dafang should use current time, not header timestamp
	require.True(t, int64(pkt.Timestamp) >= before && int64(pkt.Timestamp) <= after)
}

func TestMISSReadPacketHeaderTooSmall(t *testing.T) {
	t.Helper()
	pub, priv, err := GenerateKey()
	require.NoError(t, err)
	sharedKey, err := CalcSharedKey(hex.EncodeToString(pub), hex.EncodeToString(priv))
	require.NoError(t, err)

	mock := &mockMISSConnReadPacket{
		hdr:     make([]byte, 16), // too small
		payload: []byte("x"),
	}
	client := &MISSClient{
		Conn:  mock,
		key:   sharedKey,
		model: "test",
	}

	_, err = client.ReadPacket()
	require.Error(t, err)
	require.Contains(t, err.Error(), "packet header too small")
}

func TestMISSReadPacketConnError(t *testing.T) {
	t.Helper()
	client := &MISSClient{
		Conn:  &mockMISSConnReadPacket{err: fmt.Errorf("connection lost")},
		key:   []byte(strings.Repeat("\x00", 32)),
		model: "test",
	}

	_, err := client.ReadPacket()
	require.Error(t, err)
	require.Contains(t, err.Error(), "miss: read media:")
}

// mockMISSConnReadPacket is a specialized mock for ReadPacket tests.
type mockMISSConnReadPacket struct {
	hdr     []byte
	payload []byte
	err     error
}

func (m *mockMISSConnReadPacket) Protocol() string                                    { return "cs2+udp" }
func (m *mockMISSConnReadPacket) Version() string                                     { return "CS2" }
func (m *mockMISSConnReadPacket) ReadCommand() (uint32, []byte, error)               { return 0, nil, nil }
func (m *mockMISSConnReadPacket) WriteCommand(uint32, []byte) error                   { return nil }
func (m *mockMISSConnReadPacket) ReadPacket() ([]byte, []byte, error)                { return m.hdr, m.payload, m.err }
func (m *mockMISSConnReadPacket) WritePacket([]byte, []byte) error                    { return nil }
func (m *mockMISSConnReadPacket) RemoteAddr() net.Addr                                { return nil }
func (m *mockMISSConnReadPacket) SetDeadline(time.Time) error                         { return nil }
func (m *mockMISSConnReadPacket) Close() error                                        { return nil }

func TestMISSLoginJSONFormat(t *testing.T) {
	t.Helper()
	// Verify exact JSON format: {"public_key":"...","sign":"...","uuid":"","support_encrypt":0}
	mock := &mockMISSConn{}
	mock.readCmdResp.data = []byte(`{"result":"success"}`)

	err := missLogin(mock, "myPublic", "mySign")
	require.NoError(t, err)

	mock.mu.Lock()
	require.Len(t, mock.writtenCmds, 1)
	data := mock.writtenCmds[0].data
	mock.mu.Unlock()

	expected := `{"public_key":"myPublic","sign":"mySign","uuid":"","support_encrypt":0}`
	require.Equal(t, expected, string(data))
}

func TestMISSClientStartMediaC200HD(t *testing.T) {
	t.Helper()
	client, mock := newTestMISSClient()
	client.model = ModelC200

	err := client.StartMedia("", "")
	require.NoError(t, err)

	_, data, ok := mock.lastWrittenCmd()
	require.True(t, ok)

	decoded, err := Decode(data, client.key)
	require.NoError(t, err)
	body := string(decoded[4:])
	require.Contains(t, body, `"videoquality":3`)
}
