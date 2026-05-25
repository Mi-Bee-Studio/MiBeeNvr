// SPDX-License-Identifier: MIT
//
// Xiaomi MISS protocol client adapted from go2rtc (https://github.com/AlexxIT/go2rtc)
// Copyright (c) go2rtc contributors
// Licensed under the MIT License.

package xiaomi

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"net/url"
	"time"
)

// MISS protocol command constants.
const (
	missCmdAuthReq   = 0x100
	missCmdAuthRes   = 0x101
	missCmdVideoStart = 0x102
	missCmdVideoStop  = 0x103

	missCmdEncoded = 0x1001
)

// MISS codec ID constants (from producer.go).
const (
	missCodecH264 = 4
	missCodecH265 = 5
	missCodecPCM  = 1024
	missCodecPCMU = 1026
	missCodecPCMA = 1027
	missCodecOPUS = 1032
)

// Known Xiaomi camera model identifiers.
const (
	ModelDafang   = "isa.camera.df3"
	ModelLoockV2  = "loock.cateye.v02"
	ModelC200     = "chuangmi.camera.046c04"
	ModelC300     = "chuangmi.camera.72ac1"
	ModelXiaofang = "isa.camera.isc5c1"
	ModelHLC8     = "isa.camera.hlc8"
)

// missHdrSize is the size of a MISS media packet header.
const missHdrSize = 32

// MISSConn is the interface for a MISS protocol transport connection.
// CS2Conn implements this interface.
type MISSConn interface {
	Protocol() string
	Version() string
	ReadCommand() (cmd uint32, data []byte, err error)
	WriteCommand(cmd uint32, data []byte) error
	ReadPacket() (hdr, payload []byte, err error)
	WritePacket(hdr, payload []byte) error
	RemoteAddr() net.Addr
	SetDeadline(t time.Time) error
	Close() error
}

// MISSClient wraps a MISSConn with encryption and protocol logic.
type MISSClient struct {
	Conn  MISSConn
	key   []byte
	model string
}

// MISSPacket is a decoded media packet from a Xiaomi camera.
type MISSPacket struct {
	CodecID   uint32
	Sequence  uint32
	Flags     uint32
	Timestamp uint64 // msec
	Payload   []byte
}

// SampleRate returns the audio sample rate derived from packet flags.
func (p *MISSPacket) SampleRate() uint32 {
	v := (p.Flags >> 3) & 0b1111
	if v != 0 {
		return 16000
	}
	return 8000
}

// NewMISSClient parses a MISS URL, establishes a CS2 connection, and performs login.
// URL format: miss://host?vendor=cs2&device_public=...&client_private=...&client_public=...&sign=...&model=...
func NewMISSClient(rawURL string, idleTimeout time.Duration) (*MISSClient, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}

	// 1. Calculate shared encryption key.
	query := u.Query()
	key, err := CalcSharedKey(query.Get("device_public"), query.Get("client_private"))
	if err != nil {
		return nil, err
	}

	model := query.Get("model")

	// 2. Establish transport connection (CS2 only, TUTK removed).
	var conn MISSConn
	switch s := query.Get("vendor"); s {
	case "cs2":
		conn, err = CS2Dial(u.Host, query.Get("transport"), idleTimeout)
	default:
		err = fmt.Errorf("miss: unsupported vendor %q", s)
	}

	if err != nil {
		return nil, err
	}

	// 3. Login with credentials.
	err = missLogin(conn, query.Get("client_public"), query.Get("sign"))
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	return &MISSClient{Conn: conn, key: key, model: model}, nil
}

// missLogin sends the authentication request and verifies the response.
func missLogin(conn MISSConn, clientPublic, sign string) error {
	s := fmt.Sprintf(`{"public_key":"%s","sign":"%s","uuid":"","support_encrypt":0}`, clientPublic, sign)
	if err := conn.WriteCommand(missCmdAuthReq, []byte(s)); err != nil {
		return err
	}

	_, data, err := conn.ReadCommand()
	if err != nil {
		return err
	}

	if !bytes.Contains(data, []byte(`"result":"success"`)) {
		return fmt.Errorf("miss: auth: %s", data)
	}

	return nil
}

// Version returns a human-readable protocol + model version string.
func (c *MISSClient) Version() string {
	return fmt.Sprintf("%s (%s)", c.Conn.Version(), c.model)
}

// WriteCommand encrypts data and sends it as an encoded command.
func (c *MISSClient) WriteCommand(data []byte) error {
	data, err := Encode(data, c.key)
	if err != nil {
		return err
	}
	return c.Conn.WriteCommand(missCmdEncoded, data)
}

// StartMedia sends the video start command for the given channel and quality.
// Quality: "auto"=0, "sd"=1, "hd"=2 (or 3 for C200/C300), default="hd".
func (c *MISSClient) StartMedia(channel, quality string) error {
	// 0 - auto, 1 - sd, 2 - hd, default - hd
	switch quality {
	case "", "hd":
		// Different models require different default quality settings.
		switch c.model {
		case ModelC200, ModelC300:
			quality = "3"
		default:
			quality = "2"
		}
	case "sd":
		quality = "1"
	case "auto":
		quality = "0"
	}

	data := binary.BigEndian.AppendUint32(nil, missCmdVideoStart)
	switch channel {
	case "", "0":
		data = fmt.Appendf(data, `{"videoquality":%s}`, quality)
	default:
		data = fmt.Appendf(data, `{"videoquality":-1,"videoquality2":%s}`, quality)
	}
	return c.WriteCommand(data)
}

// StopMedia sends the video stop command.
func (c *MISSClient) StopMedia() error {
	data := binary.BigEndian.AppendUint32(nil, missCmdVideoStop)
	return c.WriteCommand(data)
}

// ReadPacket reads and decrypts a media packet from the connection.
func (c *MISSClient) ReadPacket() (*MISSPacket, error) {
	hdr, payload, err := c.Conn.ReadPacket()
	if err != nil {
		return nil, fmt.Errorf("miss: read media: %w", err)
	}

	if len(hdr) < missHdrSize {
		return nil, fmt.Errorf("miss: packet header too small")
	}

	payload, err = Decode(payload, c.key)
	if err != nil {
		return nil, err
	}

	pkt := &MISSPacket{
		CodecID:   binary.LittleEndian.Uint32(hdr[4:]),
		Sequence:  binary.LittleEndian.Uint32(hdr[8:]),
		Flags:     binary.LittleEndian.Uint32(hdr[12:]),
		Payload:   payload,
	}

	// Model-specific timestamp handling.
	switch c.model {
	case ModelDafang, ModelXiaofang, ModelLoockV2:
		// Dafang has ts in sec, LoockV2 has ts in msec for video but zero for audio.
		pkt.Timestamp = uint64(time.Now().UnixMilli())
	default:
		pkt.Timestamp = binary.LittleEndian.Uint64(hdr[16:])
	}

	return pkt, nil
}
