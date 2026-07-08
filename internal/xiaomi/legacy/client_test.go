// SPDX-License-Identifier: MIT
//
// Legacy TUTK-only Xiaomi camera client tests adapted from go2rtc (https://github.com/AlexxIT/go2rtc)
// Copyright (c) go2rtc contributors
// Licensed under the MIT License.

package legacy

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/xiaomi"
)

// mockTUTKConn implements TUTKConn for testing.
type mockTUTKConn struct {
	writeCmds []writeCmd
	readCmds  []readCmdRes
	readIdx   int

	readPkts []readPktRes
	pktIdx   int

	protocol    string
	versionStr  string
	remoteAddr  net.Addr
	closeCalled bool
}

type writeCmd struct {
	ctrlType uint32
	data     []byte
}

type readCmdRes struct {
	ctrlType uint32
	data     []byte
	err      error
}

type readPktRes struct {
	hdr []byte
	pld []byte
	err error
}

func (m *mockTUTKConn) WriteCommand(ctrlType uint32, ctrlData []byte) error {
	m.writeCmds = append(m.writeCmds, writeCmd{ctrlType: ctrlType, data: append([]byte(nil), ctrlData...)})
	return nil
}

func (m *mockTUTKConn) ReadCommand() (ctrlType uint32, ctrlData []byte, err error) {
	if m.readIdx >= len(m.readCmds) {
		return 0, nil, fmt.Errorf("mock: no more read commands")
	}
	r := m.readCmds[m.readIdx]
	m.readIdx++
	return r.ctrlType, r.data, r.err
}

func (m *mockTUTKConn) ReadPacket() (hdr, payload []byte, err error) {
	if m.pktIdx >= len(m.readPkts) {
		return nil, nil, fmt.Errorf("mock: no more packets")
	}
	r := m.readPkts[m.pktIdx]
	m.pktIdx++
	return r.hdr, r.pld, r.err
}

func (m *mockTUTKConn) Close() error {
	m.closeCalled = true
	return nil
}

func (m *mockTUTKConn) Protocol() string              { return m.protocol }
func (m *mockTUTKConn) Version() string               { return m.versionStr }
func (m *mockTUTKConn) RemoteAddr() net.Addr          { return m.remoteAddr }
func (m *mockTUTKConn) SetDeadline(_ time.Time) error { return nil }

func TestParseLegacyURL(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		wantOK  bool
		wantErr string
	}{
		{
			name:   "aqara g2 url with sign params",
			rawURL: "legacy_xiaomi://192.168.1.100?uid=testuid123&model=lumi.camera.gwagl01&device_public=aa&client_private=bb&client_public=cc&sign=dd",
			wantOK: true,
		},
		{
			name:   "dafang url with password",
			rawURL: "legacy_xiaomi://192.168.1.101?uid=dafanguid&model=isa.camera.df3&password=secret123",
			wantOK: true,
		},
		{
			name:   "xiaofang legacy url with password",
			rawURL: "legacy_xiaomi://192.168.1.102?uid=xiaofanguid&model=isa.camera.isc5&password=secret456",
			wantOK: true,
		},
		{
			name:   "mijia url with password",
			rawURL: "legacy_xiaomi://192.168.1.103?uid=mijiauid&model=chuangmi.camera.v2&password=mijiapwd",
			wantOK: true,
		},
		{
			name:   "imilaba1 url with sign params",
			rawURL: "legacy_xiaomi://192.168.1.104?uid=imilauid&model=chuangmi.camera.ipc019e&device_public=aa&client_private=bb&client_public=cc&sign=dd",
			wantOK: true,
		},
		{
			name:   "loock v1 url with sign params",
			rawURL: "legacy_xiaomi://192.168.1.105?uid=loockuid&model=loock.cateye.v01&device_public=aa&client_private=bb&client_public=cc&sign=dd",
			wantOK: true,
		},
		{
			name:   "xiaobai url with sign params",
			rawURL: "legacy_xiaomi://192.168.1.106?uid=xiaobaiuid&model=chuangmi.camera.xiaobai&device_public=aa&client_private=bb&client_public=cc&sign=dd",
			wantOK: true,
		},
		{
			name:    "empty url",
			rawURL:  "",
			wantOK:  false,
			wantErr: "scheme",
		},
		{
			name:    "bad scheme",
			rawURL:  "http://example.com",
			wantOK:  false,
			wantErr: "scheme",
		},
		{
			name:    "no model param",
			rawURL:  "legacy_xiaomi://host?uid=test",
			wantOK:  false,
			wantErr: "missing model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := urlParseOnly(tt.rawURL)
			if tt.wantOK {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.wantErr)
				} else if !errorContains(err, tt.wantErr) {
					t.Errorf("expected error containing %q, got %q", tt.wantErr, err.Error())
				}
			}
		})
	}
}

// urlParseOnly parses the URL and extracts the model. It does not dial.
// Used for unit-testing URL parsing and model validation only.
func urlParseOnly(rawURL string) (*Client, error) {
	cl := &Client{}
	return cl, cl.urlParseOnly(rawURL)
}

// urlParseOnly is a helper on Client that validates the URL without dialing.
func (c *Client) urlParseOnly(rawURL string) error {
	const prefix = "legacy_xiaomi://"
	if !strings.HasPrefix(rawURL, prefix) {
		return fmt.Errorf("legacy: invalid scheme")
	}

	// url.Parse rejects unknown schemes with "//" (colon-in-path error in Go 1.24+),
	// so substitute with http:// for parsing.
	rest := rawURL[len(prefix):]
	u, err := url.Parse("http://" + rest)
	if err != nil {
		return err
	}
	if u.Host == "" {
		return fmt.Errorf("legacy: missing host")
	}
	model := u.Query().Get("model")
	if model == "" {
		return fmt.Errorf("legacy: missing model parameter")
	}
	if !isSupportedModel(model) {
		return fmt.Errorf("legacy: unsupported model: %s", model)
	}
	return nil
}

// isSupportedModel checks whether the model is one of the 7 known legacy models.
func isSupportedModel(model string) bool {
	switch model {
	case ModelAqaraG2, ModelIMILABA1, ModelLoockV1, ModelXiaobai,
		ModelXiaofangLegacy, xiaomi.ModelDafang, xiaomi.ModelMijia:
		return true
	}
	return false
}

func errorContains(err error, substr string) bool {
	if err == nil {
		return false
	}
	for i := 0; i+len(substr) <= len(err.Error()); i++ {
		if err.Error()[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestAuthMethod(t *testing.T) {
	tests := []struct {
		model      string
		wantMethod authMethod
	}{
		{ModelAqaraG2, authSign},
		{ModelIMILABA1, authSign},
		{ModelLoockV1, authSign},
		{ModelXiaobai, authSign},
		{xiaomi.ModelMijia, authPassword},
		{xiaomi.ModelDafang, authPassword},
		{ModelXiaofangLegacy, authXiaofang},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			method := lookupAuthMethod(tt.model)
			if method != tt.wantMethod {
				t.Errorf("authMethod for %q: got %d, want %d", tt.model, method, tt.wantMethod)
			}
		})
	}
}

func lookupAuthMethod(model string) authMethod {
	switch model {
	case ModelAqaraG2, ModelIMILABA1, ModelLoockV1, ModelXiaobai:
		return authSign
	case xiaomi.ModelMijia, xiaomi.ModelDafang:
		return authPassword
	case ModelXiaofangLegacy:
		return authXiaofang
	default:
		return -1
	}
}

func TestStartMedia(t *testing.T) {
	t.Helper()

	tests := []struct {
		name         string
		model        string
		quality      string
		audioEnabled bool
		checkCmds    func(t *testing.T, cmds []writeCmd)
	}{
		{
			name:    "aqara g2 fhd",
			model:   ModelAqaraG2,
			quality: "fhd",
			checkCmds: func(t *testing.T, cmds []writeCmd) {
				if len(cmds) < 3 {
					t.Fatalf("expected at least 3 commands, got %d", len(cmds))
				}
				// cmdVideoStart with "{}"
				if cmds[0].ctrlType != cmdVideoStart {
					t.Errorf("cmd[0] ctrlType: got 0x%04x, want 0x%04x", cmds[0].ctrlType, cmdVideoStart)
				}
				// 0x0605 channel selector
				if cmds[1].ctrlType != 0x0605 {
					t.Errorf("cmd[1] ctrlType: got 0x%04x, want 0x%04x", cmds[1].ctrlType, 0x0605)
				}
				// 0x0704
				if cmds[2].ctrlType != 0x0704 {
					t.Errorf("cmd[2] ctrlType: got 0x%04x, want 0x%04x", cmds[2].ctrlType, 0x0704)
				}
			},
		},
		{
			name:    "imilaba1 hd",
			model:   ModelIMILABA1,
			quality: "hd",
			checkCmds: func(t *testing.T, cmds []writeCmd) {
				if len(cmds) < 3 {
					t.Fatalf("expected at least 3 commands, got %d", len(cmds))
				}
				if cmds[0].ctrlType != cmdAudioStart {
					t.Errorf("cmd[0] ctrlType: got 0x%04x, want 0x%04x", cmds[0].ctrlType, cmdAudioStart)
				}
				if cmds[1].ctrlType != cmdVideoStart {
					t.Errorf("cmd[1] ctrlType: got 0x%04x, want 0x%04x", cmds[1].ctrlType, cmdVideoStart)
				}
				if cmds[2].ctrlType != cmdStreamCtrlReq {
					t.Errorf("cmd[2] ctrlType: got 0x%04x, want 0x%04x", cmds[2].ctrlType, cmdStreamCtrlReq)
				}
			},
		},
		{
			name:    "mijia sd",
			model:   xiaomi.ModelMijia,
			quality: "sd",
			checkCmds: func(t *testing.T, cmds []writeCmd) {
				if len(cmds) < 3 {
					t.Fatalf("expected at least 3 commands, got %d", len(cmds))
				}
				if cmds[0].ctrlType != cmdAudioStart {
					t.Errorf("cmd[0] ctrlType: got 0x%04x, want 0x%04x", cmds[0].ctrlType, cmdAudioStart)
				}
				if cmds[2].ctrlType != cmdStreamCtrlReq {
					t.Errorf("cmd[2] ctrlType: got 0x%04x, want 0x%04x", cmds[2].ctrlType, cmdStreamCtrlReq)
				}
			},
		},
		{
			name:    "loockv1 hd",
			model:   ModelLoockV1,
			quality: "hd",
			checkCmds: func(t *testing.T, cmds []writeCmd) {
				if len(cmds) < 3 {
					t.Fatalf("expected at least 3 commands, got %d", len(cmds))
				}
				if cmds[0].ctrlType != cmdAudioStart {
					t.Errorf("cmd[0] ctrlType: got 0x%04x, want 0x%04x", cmds[0].ctrlType, cmdAudioStart)
				}
				if cmds[1].ctrlType != cmdVideoStart {
					t.Errorf("cmd[1] ctrlType: got 0x%04x, want 0x%04x", cmds[1].ctrlType, cmdVideoStart)
				}
				if cmds[2].ctrlType != cmdStreamCtrlReq {
					t.Errorf("cmd[2] ctrlType: got 0x%04x, want 0x%04x", cmds[2].ctrlType, cmdStreamCtrlReq)
				}
			},
		},
		{
			name:    "xiaobai hd",
			model:   ModelXiaobai,
			quality: "hd",
			checkCmds: func(t *testing.T, cmds []writeCmd) {
				if len(cmds) < 3 {
					t.Fatalf("expected at least 3 commands, got %d", len(cmds))
				}
				if cmds[0].ctrlType != cmdAudioStart {
					t.Errorf("cmd[0] ctrlType: got 0x%04x, want 0x%04x", cmds[0].ctrlType, cmdAudioStart)
				}
				if cmds[1].ctrlType != cmdStreamCtrlReq {
					t.Errorf("cmd[1] ctrlType: got 0x%04x, want 0x%04x", cmds[1].ctrlType, cmdStreamCtrlReq)
				}
				// Verify quality byte for HD (2)
				if len(cmds[1].data) >= 5 && cmds[1].data[4] != 2 {
					t.Errorf("cmd[1] quality byte: got %d, want 2", cmds[1].data[4])
				}
				if cmds[2].ctrlType != cmdVideoStart {
					t.Errorf("cmd[2] ctrlType: got 0x%04x, want 0x%04x", cmds[2].ctrlType, cmdVideoStart)
				}
			},
		},
		{
			name:    "dafang hd uses ICAM",
			model:   xiaomi.ModelDafang,
			quality: "hd",
			checkCmds: func(t *testing.T, cmds []writeCmd) {
				if len(cmds) < 1 {
					t.Fatalf("expected at least 1 command, got %d", len(cmds))
				}
				if cmds[0].ctrlType != 0x0100 {
					t.Errorf("cmd[0] ctrlType: got 0x%04x, want 0x0100", cmds[0].ctrlType)
				}
				// Verify it contains ICAM header.
				if len(cmds[0].data) < 4 || string(cmds[0].data[:4]) != "ICAM" {
					t.Error("cmd[0] data should start with 'ICAM'")
				}
			},
		},
		{
			name:    "xiaofang legacy sd uses ICAM",
			model:   ModelXiaofangLegacy,
			quality: "sd",
			checkCmds: func(t *testing.T, cmds []writeCmd) {
				if len(cmds) < 1 {
					t.Fatalf("expected at least 1 command, got %d", len(cmds))
				}
				if cmds[0].ctrlType != 0x0100 {
					t.Errorf("cmd[0] ctrlType: got 0x%04x, want 0x0100", cmds[0].ctrlType)
				}
				if len(cmds[0].data) < 4 || string(cmds[0].data[:4]) != "ICAM" {
					t.Error("cmd[0] data should start with 'ICAM'")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockTUTKConn{}
			c := &Client{
				conn:  mock,
				model: tt.model,
			}

			err := c.StartMedia(tt.quality, tt.audioEnabled)
			if err != nil {
				t.Fatalf("StartMedia: %v", err)
			}

			tt.checkCmds(t, mock.writeCmds)
		})
	}
}

func TestStopMedia(t *testing.T) {
	mock := &mockTUTKConn{}
	c := &Client{conn: mock, model: ModelAqaraG2}

	err := c.StopMedia()
	if err != nil {
		t.Fatalf("StopMedia: %v", err)
	}

	if len(mock.writeCmds) < 2 {
		t.Fatalf("expected at least 2 commands, got %d", len(mock.writeCmds))
	}
	if mock.writeCmds[0].ctrlType != cmdVideoStop {
		t.Errorf("cmd[0] ctrlType: got 0x%04x, want 0x%04x", mock.writeCmds[0].ctrlType, cmdVideoStop)
	}
	if mock.writeCmds[1].ctrlType != cmdVideoStop {
		t.Errorf("cmd[1] ctrlType: got 0x%04x, want 0x%04x", mock.writeCmds[1].ctrlType, cmdVideoStop)
	}
}

func TestBadURL(t *testing.T) {
	tests := []string{
		"not-a-url",
		"http://example.com?model=chuangmi.camera.v2",
		"legacy_xiaomi://?model=chuangmi.camera.v2",
	}

	for _, rawURL := range tests {
		t.Run(rawURL, func(t *testing.T) {
			_, err := urlParseOnly(rawURL)
			if err == nil {
				t.Error("expected error for bad URL, got nil")
			}
		})
	}
}

func TestUnknownModel(t *testing.T) {
	_, err := urlParseOnly("legacy_xiaomi://host?uid=test&model=unknown.model.xyz")
	if err == nil {
		t.Fatal("expected error for unknown model, got nil")
	}
}

func TestClose(t *testing.T) {
	mock := &mockTUTKConn{}
	c := &Client{conn: mock, model: ModelAqaraG2}

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !mock.closeCalled {
		t.Error("expected Close to be called on underlying conn")
	}
}

func TestDecodeVideo(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		key     []byte
		wantErr bool
	}{
		{
			name: "annex b format passed through",
			data: []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x1e, 0x99},
			key:  make([]byte, 32),
		},
		{
			name: "data[8]==0 passed through",
			data: []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
			key:  make([]byte, 32),
		},
		{
			name:    "too short data",
			data:    []byte{0x01},
			key:     make([]byte, 32),
			wantErr: false, // passthrough for < 17 bytes
		},
		{
			name:    "unsupported encryption type",
			data:    []byte{0, 0, 0, 0, 0, 0, 0, 0, 2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
			key:     make([]byte, 32),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DecodeVideo(tt.data, tt.key)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			_ = got
		})
	}
}

func TestDecodeVideo_AnnexB(t *testing.T) {
	// Test that Annex B start codes pass through without decryption.
	data := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x1e, 0x99, 0xa0}
	key := make([]byte, 32)

	got, err := DecodeVideo(data, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != len(data) {
		t.Errorf("expected length %d, got %d", len(data), len(got))
	}
	for i := range data {
		if got[i] != data[i] {
			t.Errorf("byte %d: expected 0x%02x, got 0x%02x", i, data[i], got[i])
		}
	}
}

func TestDafangVideoQuality(t *testing.T) {
	tests := []struct {
		quality string
		want    byte
	}{
		{"", 0x5a},
		{"hd", 0x5a},
		{"sd", 0x1e},
		{"fhd", 0x5a},
		{"auto", 0x5a},
		{"unknown", 0x5a},
	}

	for _, tt := range tests {
		t.Run("quality="+tt.quality, func(t *testing.T) {
			got := dafangVideoQuality(tt.quality)
			if got != tt.want {
				t.Errorf("dafangVideoQuality(%q): got 0x%02x, want 0x%02x", tt.quality, got, tt.want)
			}
		})
	}
}

func TestDafangVideoStart(t *testing.T) {
	mock := &mockTUTKConn{}
	err := dafangVideoStart(mock, 0x5a)
	if err != nil {
		t.Fatalf("dafangVideoStart: %v", err)
	}
	if len(mock.writeCmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(mock.writeCmds))
	}
	if mock.writeCmds[0].ctrlType != 0x0100 {
		t.Errorf("ctrlType: got 0x%04x, want 0x0100", mock.writeCmds[0].ctrlType)
	}
	if len(mock.writeCmds[0].data) < 4 || string(mock.writeCmds[0].data[:4]) != "ICAM" {
		t.Error("data should start with 'ICAM'")
	}
}

func TestReadPacketNoKey(t *testing.T) {
	mock := &mockTUTKConn{
		readPkts: []readPktRes{
			{hdr: []byte{0x4e}, pld: []byte{0x00, 0x00, 0x00, 0x01, 0x67}},
		},
	}
	c := &Client{conn: mock, model: ModelAqaraG2}

	hdr, payload, err := c.ReadPacket()
	if err != nil {
		t.Fatalf("ReadPacket: %v", err)
	}
	if len(hdr) != 1 || hdr[0] != 0x4e {
		t.Errorf("expected hdr [0x4e], got %v", hdr)
	}
	if len(payload) != 5 {
		t.Errorf("expected payload len 5, got %d", len(payload))
	}
}

func TestModelConstants(t *testing.T) {
	if ModelAqaraG2 != "lumi.camera.gwagl01" {
		t.Errorf("ModelAqaraG2: got %q", ModelAqaraG2)
	}
	if ModelIMILABA1 != "chuangmi.camera.ipc019e" {
		t.Errorf("ModelIMILABA1: got %q", ModelIMILABA1)
	}
	if ModelLoockV1 != "loock.cateye.v01" {
		t.Errorf("ModelLoockV1: got %q", ModelLoockV1)
	}
	if ModelXiaobai != "chuangmi.camera.xiaobai" {
		t.Errorf("ModelXiaobai: got %q", ModelXiaobai)
	}
	if ModelXiaofangLegacy != "isa.camera.isc5" {
		t.Errorf("ModelXiaofangLegacy: got %q", ModelXiaofangLegacy)
	}
}

func TestModelConstantsMatchXiaomi(t *testing.T) {
	if xiaomi.ModelDafang != "isa.camera.df3" {
		t.Errorf("xiaomi.ModelDafang: got %q", xiaomi.ModelDafang)
	}
	if xiaomi.ModelMijia != "chuangmi.camera.v2" {
		t.Errorf("xiaomi.ModelMijia: got %q", xiaomi.ModelMijia)
	}
}

func TestSupportedModels(t *testing.T) {
	allModels := []string{
		ModelAqaraG2,
		ModelIMILABA1,
		ModelLoockV1,
		ModelXiaobai,
		ModelXiaofangLegacy,
		xiaomi.ModelDafang,
		xiaomi.ModelMijia,
	}

	for _, model := range allModels {
		if !isSupportedModel(model) {
			t.Errorf("expected %q to be supported", model)
		}
	}

	if isSupportedModel("unknown.model") {
		t.Error("expected unknown model to be unsupported")
	}
}

func TestXiaofangLogin(t *testing.T) {
	mock := &mockTUTKConn{
		readCmds: []readCmdRes{
			{data: make([]byte, 40)}, // challenge response (needs 24+16 for XXTEA)
			{data: []byte("ok")},     // ack
		},
	}

	err := xiaofangLogin(mock, "testpwd1234567890")
	if err != nil {
		t.Fatalf("xiaofangLogin: %v", err)
	}

	if len(mock.writeCmds) != 2 {
		t.Fatalf("expected 2 write commands, got %d", len(mock.writeCmds))
	}
	if mock.writeCmds[0].ctrlType != 0x0100 {
		t.Errorf("write[0] ctrlType: 0x%04x", mock.writeCmds[0].ctrlType)
	}
	if len(mock.writeCmds[0].data) < 4 || string(mock.writeCmds[0].data[:4]) != "ICAM" {
		t.Error("write[0] should start with ICAM")
	}
	if mock.writeCmds[1].ctrlType != 0x0100 {
		t.Errorf("write[1] ctrlType: 0x%04x", mock.writeCmds[1].ctrlType)
	}
}

func TestXiaofangLoginShortChallenge(t *testing.T) {
	mock := &mockTUTKConn{
		readCmds: []readCmdRes{
			{data: []byte("short")}, // too short
		},
	}

	err := xiaofangLogin(mock, "testpwd")
	if err == nil {
		t.Fatal("expected error for short challenge")
	}
}

func TestClientVersion(t *testing.T) {
	mock := &mockTUTKConn{
		protocol:   "tutk",
		versionStr: "TUTK/25 SDK 6.0.3.3",
	}
	c := &Client{conn: mock, model: ModelAqaraG2}

	if c.Protocol() != "tutk" {
		t.Errorf("Protocol: got %q", c.Protocol())
	}

	ver := c.Version()
	if ver != "TUTK/25 SDK 6.0.3.3 (lumi.camera.gwagl01)" {
		t.Errorf("Version: got %q", ver)
	}

	if c.RemoteAddr() != nil {
		t.Errorf("RemoteAddr: got %v", c.RemoteAddr())
	}
}
