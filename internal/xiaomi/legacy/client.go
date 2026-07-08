// SPDX-License-Identifier: MIT
//
// Legacy TUTK-only Xiaomi camera client adapted from go2rtc (https://github.com/AlexxIT/go2rtc)
// Copyright (c) go2rtc contributors
// Licensed under the MIT License.

package legacy

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/tutk"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/xiaomi"
)

// Legacy Xiaomi camera model identifiers (new models not in miss.go).
const (
	ModelAqaraG2        = "lumi.camera.gwagl01"
	ModelIMILABA1       = "chuangmi.camera.ipc019e"
	ModelLoockV1        = "loock.cateye.v01"
	ModelXiaobai        = "chuangmi.camera.xiaobai"
	ModelXiaofangLegacy = "isa.camera.isc5"
	// ModelDafang and ModelMijia are imported from xiaomi package.
	// Xiaomi's miss.go already defines:
	//   ModelDafang = "isa.camera.df3"
	//   ModelMijia  = "chuangmi.camera.v2"
)

// authMethod determines how the legacy camera authenticates.
type authMethod int

const (
	authSign     authMethod = iota // AqaraG2, IMILABA1, LoockV1, Xiaobai — encryption-key + sign
	authPassword                   // Dafang, Mijia — password via Dial or xiaofangLogin
	authXiaofang                   // XiaofangLegacy — xiaofangLogin post-dial
)

// TUTK IO control command constants for legacy cameras.
const (
	cmdVideoStart    = 0x01ff
	cmdVideoStop     = 0x02ff
	cmdAudioStart    = 0x0300
	cmdAudioStop     = 0x0301
	cmdStreamCtrlReq = 0x0320
)

// TUTKConn is the interface for TUTK connection operations used by Client.
// *tutk.Conn satisfies this interface.
type TUTKConn interface {
	WriteCommand(ctrlType uint32, ctrlData []byte) error
	ReadCommand() (ctrlType uint32, ctrlData []byte, err error)
	ReadPacket() (hdr, payload []byte, err error)
	Close() error
	Protocol() string
	Version() string
	RemoteAddr() net.Addr
	SetDeadline(t time.Time) error
}

// Client wraps a TUTK connection with model-specific media streaming logic.
type Client struct {
	conn       TUTKConn
	key        []byte // encryption key (sign-based models only)
	model      string
	authMethod authMethod
}

// NewClient parses a legacy_xiaomi:// URL, determines auth method per model,
// dials TUTK, and performs model-specific post-dial authentication.
//
// URL format: legacy_xiaomi://host?uid=...&model=...[&sign=...&device_public=...&client_public=...&client_private=...][&password=...]
func NewClient(rawURL string) (*Client, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("legacy: parse url: %w", err)
	}

	query := u.Query()
	model := query.Get("model")

	var username, password string
	var key []byte
	var method authMethod

	// Determine auth method and build credentials for TUTK Dial.
	switch model {
	case ModelAqaraG2, ModelIMILABA1, ModelLoockV1, ModelXiaobai:
		// Sign-based: shared encryption key, JSON auth body as username.
		key, err = xiaomi.CalcSharedKey(query.Get("device_public"), query.Get("client_private"))
		if err != nil {
			return nil, fmt.Errorf("legacy: calc shared key: %w", err)
		}
		username = fmt.Sprintf(
			`{"public_key":"%s","sign":"%s","account":"admin"}`,
			query.Get("client_public"), query.Get("sign"),
		)
		method = authSign

	case xiaomi.ModelMijia:
		// Password-based: admin + password in Dial.
		username = "admin"
		password = query.Get("password")
		method = authPassword

	case xiaomi.ModelDafang:
		// Dafang: admin in Dial, then xiaofangLogin post-dial.
		username = "admin"
		password = query.Get("password")
		method = authPassword

	case ModelXiaofangLegacy:
		// Xiaofang legacy: admin in Dial, then xiaofangLogin post-dial.
		username = "admin"
		password = query.Get("password")
		method = authXiaofang

	default:
		return nil, fmt.Errorf("legacy: unsupported model: %s", model)
	}

	conn, err := tutk.Dial(u.Host, query.Get("uid"), username, password)
	if err != nil {
		return nil, fmt.Errorf("legacy: dial: %w", err)
	}

	// Post-dial authentication for Dafang and Xiaofang models.
	// Both use the xiaofangLogin sequence (ICAM-based XXTEA challenge-response).
	if model == xiaomi.ModelDafang || model == ModelXiaofangLegacy {
		if err := xiaofangLogin(conn, query.Get("password")); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("legacy: xiaofang login: %w", err)
		}
	}

	return &Client{
		conn:       conn,
		key:        key,
		model:      model,
		authMethod: method,
	}, nil
}

// xiaofangLogin performs the ICAM-based challenge-response authentication
// used by Dafang and Xiaofang legacy cameras. It sends an ICAM(0x0400be)
// "ask login" command, receives the challenge, decrypts it with XXTEA using
// the camera password, and sends the response.
func xiaofangLogin(conn TUTKConn, password string) error {
	// Ask login.
	data := tutk.ICAM(0x0400be)
	if err := conn.WriteCommand(0x0100, data); err != nil {
		return fmt.Errorf("write ask login: %w", err)
	}

	// Read challenge.
	_, data, err := conn.ReadCommand()
	if err != nil {
		return fmt.Errorf("read challenge: %w", err)
	}

	if len(data) < 25 {
		return fmt.Errorf("xiaofang login: challenge too short (%d bytes)", len(data))
	}

	// Decrypt challenge (payload starts at offset 24 when data[23]==3).
	enc := data[24:]
	tutk.XXTEADecrypt(enc, enc, []byte(password))

	// Build login response.
	enc = append(enc, 0, 0, 0, 0, 1, 1, 1)
	data = tutk.ICAM(0x0400c0, enc...)
	if err := conn.WriteCommand(0x0100, data); err != nil {
		return fmt.Errorf("write login response: %w", err)
	}

	// Read acknowledgement.
	_, _, err = conn.ReadCommand()
	return err
}

// writeCommandJSON sends a JSON string as a TUTK command payload.
// If args are provided, format is treated as a fmt.Sprintf template.
func (c *Client) writeCommandJSON(ctrlType uint32, format string, a ...any) error {
	if len(a) > 0 {
		format = fmt.Sprintf(format, a...)
	}
	return c.conn.WriteCommand(ctrlType, []byte(format))
}

// ReadPacket reads a media packet from the TUTK connection, applying
// model-specific decryption when needed.
func (c *Client) ReadPacket() (hdr, payload []byte, err error) {
	hdr, payload, err = c.conn.ReadPacket()
	if err != nil {
		return nil, nil, err
	}

	if c.key != nil {
		if c.model == ModelAqaraG2 && len(hdr) > 0 && hdr[0] == tutk.CodecH265 {
			payload, err = DecodeVideo(payload, c.key)
		} else {
			// ModelAqaraG2 (audio AAC), ModelIMILABA1 (video HEVC, audio PCMA).
			payload, err = xiaomi.Decode(payload, c.key)
		}
		if err != nil {
			return nil, nil, err
		}
	}

	return hdr, payload, nil
}

// StartMedia sends model-specific TUTK IO control sequences to begin
// video and audio streaming at the requested quality.
//
// quality: "" (default HD), "hd", "sd", "fhd", "auto".
// audioEnabled: when true, audio stream is started alongside video.
func (c *Client) StartMedia(quality string, audioEnabled bool) error {
	switch c.model {
	case ModelAqaraG2:
		// 0 — 1920x1080 (FHD), 1 — 1280x720 (HD), 2 — SD.
		var ch string
		switch quality {
		case "", "fhd":
			ch = "0"
		case "hd":
			ch = "1"
		case "sd":
			ch = "2"
		default:
			ch = "0"
		}

		return errors.Join(
			c.writeCommandJSON(cmdVideoStart, `{}`),
			c.writeCommandJSON(0x0605, `{"channel":%s}`, ch),
			c.writeCommandJSON(0x0704, `{}`), // unknown purpose, kept for go2rtc compat
		)

	case ModelIMILABA1, xiaomi.ModelMijia:
		// 0 — auto, 1 — low, 3 — hd.
		var vq string
		switch quality {
		case "", "hd":
			vq = "3"
		case "sd":
			vq = "1"
		case "auto":
			vq = "0"
		default:
			vq = "3"
		}

		return errors.Join(
			c.writeCommandJSON(cmdAudioStart, `{}`),
			c.writeCommandJSON(cmdVideoStart, `{}`),
			c.writeCommandJSON(cmdStreamCtrlReq, `{"videoquality":%s}`, vq),
		)

	case ModelLoockV1:
		// LoockV1 uses the same quality sequence as IMILABA1/Mijia
		// (sign-based auth, similar command set).
		var vq string
		switch quality {
		case "", "hd":
			vq = "3"
		case "sd":
			vq = "1"
		case "auto":
			vq = "0"
		default:
			vq = "3"
		}

		return errors.Join(
			c.writeCommandJSON(cmdAudioStart, `{}`),
			c.writeCommandJSON(cmdVideoStart, `{}`),
			c.writeCommandJSON(cmdStreamCtrlReq, `{"videoquality":%s}`, vq),
		)

	case ModelXiaobai:
		// 00030000 7b7d  audio on
		// 20030000 0000000001000000  fhd
		// 20030000 0000000002000000  hd
		// 20030000 0000000004000000  low
		// ff010000 7b7d  video start
		var b byte
		switch quality {
		case "", "fhd":
			b = 1
		case "hd":
			b = 2
		case "sd":
			b = 4
		case "auto":
			b = 0xff
		default:
			b = 1
		}

		return errors.Join(
			c.writeCommandJSON(cmdAudioStart, `{}`),
			c.conn.WriteCommand(cmdStreamCtrlReq, []byte{0, 0, 0, 0, b, 0, 0, 0}),
			c.writeCommandJSON(cmdVideoStart, `{}`),
		)

	case xiaomi.ModelDafang, ModelXiaofangLegacy:
		// Dafang/Xiaofang: set video quality via ICAM command.
		// Ported from go2rtc's commented-out Dafang start sequence.
		qualityByte := dafangVideoQuality(quality)
		return dafangVideoStart(c.conn, qualityByte)

	default:
		return fmt.Errorf("legacy: unsupported model: %s", c.model)
	}
}

// StopMedia sends the video stop command sequence to the camera.
// Matches go2rtc's StopMedia: JSON stop + binary stop.
func (c *Client) StopMedia() error {
	return errors.Join(
		c.writeCommandJSON(cmdVideoStop, `{}`),
		c.conn.WriteCommand(cmdVideoStop, make([]byte, 8)),
	)
}

// Close closes the underlying TUTK connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// Protocol returns the transport protocol string from the underlying connection.
func (c *Client) Protocol() string {
	return c.conn.Protocol()
}

// Version returns a human-readable version string including the model name.
func (c *Client) Version() string {
	return fmt.Sprintf("%s (%s)", c.conn.Version(), c.model)
}

// RemoteAddr returns the remote address of the connection.
func (c *Client) RemoteAddr() net.Addr {
	return c.conn.RemoteAddr()
}

// SetDeadline sets the read deadline on the underlying connection.
func (c *Client) SetDeadline(t time.Time) error {
	return c.conn.SetDeadline(t)
}

// DecodeVideo decrypts AqaraG2 H.265 video data using the camera's
// session key. It uses a sparse-XOR scheme where every 160th byte
// block (starting at offset 32) is decrypted via ChaCha20 with the
// nonce from the packet header.
//
// Ported from go2rtc pkg/xiaomi/legacy/client.go DecodeVideo.
func DecodeVideo(data, key []byte) ([]byte, error) {
	if len(data) < 17 {
		return data, nil
	}

	// Check if data is already in Annex B format (no encryption).
	if string(data[:4]) == "\x00\x00\x00\x01" || data[8] == 0 {
		return data, nil
	}

	if data[8] != 1 {
		return nil, fmt.Errorf("legacy: unsupported AqaraG2 encryption type %d", data[8])
	}

	nonce8 := data[:8]
	i1 := binary.LittleEndian.Uint32(data[9:])
	i2 := binary.LittleEndian.Uint32(data[13:])
	videoData := data[17:]

	if int(i1+i2) > len(videoData) {
		return nil, fmt.Errorf("legacy: AqaraG2 decrypt bounds out of range")
	}

	src := videoData[i1 : i1+i2]

	// Decrypt every 160th byte block (16 bytes at a time).
	for i := 32; i+16 <= len(src); i += 160 {
		dst, err := xiaomi.DecodeNonce(src[i:i+16], nonce8, key)
		if err != nil {
			return nil, err
		}
		copy(src[i:], dst)
	}

	return videoData, nil
}

// dafangVideoQuality maps the quality string to a byte parameter for
// the Dafang/Xiaofang ICAM bitrate command.
func dafangVideoQuality(quality string) byte {
	switch quality {
	case "", "hd":
		return 0x5a // bitrate ~90k
	case "sd":
		return 0x1e // bitrate ~30k
	default:
		return 0x5a
	}
}

// dafangVideoStart sends the ICAM-based video start command for Dafang
// and Xiaofang legacy cameras.
func dafangVideoStart(conn TUTKConn, quality byte) error {
	data := tutk.ICAM(0x040195, 0xd2, 4, 0, 0, quality, 7)
	return conn.WriteCommand(0x0100, data)
}
