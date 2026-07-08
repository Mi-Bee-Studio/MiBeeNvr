// SPDX-License-Identifier: MIT
//
// Legacy TUTK-only Xiaomi camera producer adapted from go2rtc (https://github.com/AlexxIT/go2rtc)
// Copyright (c) go2rtc contributors
// Licensed under the MIT License.

package legacy

import (
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/tutk"
)

// LegacyProducer wraps a legacy TUTK Client and produces MISSPacket-compatible
// output for integration with the recorder pipeline.
type LegacyProducer struct {
	client *Client
}

// NewLegacyProducer creates a new LegacyProducer by establishing a TUTK
// connection via the provided legacy_xiaomi:// URL and starting media.
func NewLegacyProducer(rawURL string) (*LegacyProducer, error) {
	client, err := NewClient(rawURL)
	if err != nil {
		return nil, err
	}

	// Start media with default quality (HD).
	if err := client.StartMedia("hd", false); err != nil {
		_ = client.Close()
		return nil, err
	}

	return &LegacyProducer{client: client}, nil
}

// ReadPacket reads the next media packet from the camera and returns
// a codec type string and the raw payload.
//
// The returned type is one of:
//   - "h264"   — H.264 video
//   - "h265"   - H.265 video
//   - "pcmu"   — G.711 μ-law audio
//   - "pcma"   — G.711 A-law audio
//   - "pcml"   — PCM linear audio
//   - "aac"    — AAC audio
//   - ""        — unknown/unhandled codec (caller should skip)
func (p *LegacyProducer) ReadPacket() (typ string, data []byte, err error) {
	hdr, payload, err := p.client.ReadPacket()
	if err != nil {
		return "", nil, err
	}

	if len(hdr) == 0 {
		return "", payload, nil
	}

	codec := hdr[0]
	switch codec {
	case tutk.CodecH264:
		return "h264", payload, nil
	case tutk.CodecH265:
		return "h265", payload, nil
	case tutk.CodecPCMA:
		return "pcma", payload, nil
	case tutk.CodecPCMU:
		return "pcmu", payload, nil
	case tutk.CodecPCML:
		return "pcml", payload, nil
	case tutk.CodecAACLATM:
		return "aac", payload, nil
	case codecXiaobaiPCMA:
		return "pcma", payload, nil
	default:
		return "", payload, nil
	}
}

// codecXiaobaiPCMA is the PCM A-law codec byte used by
// chuangmi.camera.xiaobai (value 1).
const codecXiaobaiPCMA byte = 1

// Close stops media and closes the underlying TUTK connection.
func (p *LegacyProducer) Close() error {
	_ = p.client.StopMedia()
	return p.client.Close()
}

// Client returns the underlying legacy Client, for advanced use.
func (p *LegacyProducer) Client() *Client {
	return p.client
}
