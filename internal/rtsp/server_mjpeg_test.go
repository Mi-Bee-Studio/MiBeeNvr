package rtsp

// RTP/JPEG serving (RFC 2435, #658): MJPEG/JPEG cameras gain a
// rtsp://host/<id> URL like H.264/H.265 — every frame is standalone, so no
// parameter sets or GOP gating are involved.

import (
	"encoding/base64"
	"encoding/binary"
	"testing"
	"time"

	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

// tinyJPEGB64 is a real 64×48 JPEG (ffmpeg testsrc2) — the RTP/JPEG encoder
// parses SOF/DQT from it, so a synthetic byte blob would not round-trip.
const tinyJPEGB64 = "/9j/4AAQSkZJRgABAgAAAQABAAD//gAQTGF2YzYwLjMxLjEwMgD/2wBDAAgKCgsKCw0NDQ0NDRAPEBAQEBAQEBAQEBASEhIVFRUSEhIQEBISFBQVFRcXFxUVFRUXFxkZGR4eHBwjIyQrKzP/xACWAAADAQEBAQAAAAAAAAAAAAAFBAYDBwAIAQADAQEAAAAAAAAAAAAAAAAEBQYHCBAAAQIFAQMJBAoDAQEAAAAAAQIDABMREgQFIUIxNFEyQYEzFRQisQaCssLBg6MjYtMWVEMk0WFSoREAAgECBAQEBgMBAQAAAAAAAQIDBBESAAUhUTIiMUEzE4FzsXIGYkKys2Mjwf/AABEIADAAQAMBIgACEQADEQD/2gAMAwEAAhEDEQA/AOXKOY7kuNMbbU3mtgShISCpSlrolKRzqIHVBLFe1jHdW0g2qU2l0mjBQW6EhyYQW7PzXUrs4xrjqQvxJiyctzyi0sVKVPIZUFuJSRvWdQ9X/kGkG0La8N8jKo933krzeGjkTZV/G+Vu95TbbCzVtWl1CS8kQZMfSjn1PTWQKxYYiOhdl5VG3MM01bRKYaunklZ4yzYlN8DmKU4UwWYXJAK8xB7Kcj06zrWFkIufsVbcky2FJKVJNFJIQpKgR1gkQXa95feB9RSjKqQCo/hYwCUjiVKU0AAOcmA+Qqzw1lNuO62t5dilVkB1wKbvUQeCdpu9QHSEM4qrxntKtfcWttdqTSeG3SpywgDiNoptpwEIJIqdlDmmgJ7bxqdvUK4u1sNurm9/HKCjo4YC0UfQuLFZQF6jCrYdgDiv0cl9u19swvktQnSbPVLnVublyqVmzayrKb91tdlaxu25nactSFGWVpbWKS1pWkglKkqFyVJPOkkdUWUxnwnw+T/kco8heqZJ81OlX8b5O23vabbYEZK5aNLYRbivNOOuS1qu8qHnwtmYpQPBNCQoXAbVJ2xdR67qM7FHkktjdLeo/UqKWE3mMfTYgKLKy3OzntllFSpTyLJG7KwUMGU2Ks3SY7hV6gCSdwbd1Gc8HUdUW+JDnqSlSibWQEpCTVSitNqR/wBVsg94r7wTJc71WTa2Y1kulb5ltln5rqV2cYGaeu/xNlZTkvOONLtQqgyA06pTstQA4jaLRUjakRQXt+H+Tl/jd95S43y58yXdxvl7veU22wBV6rVrLbqbqRN2Y2VlDGW5dTgUkjdVXbdx2yDqGm0+ozCWq/6usBwvIA5bC7WgUsj2Y3JADMd9kOecZ3KXPh+URkzvdka53KXPh+URkzvdkCr5S/Sv/mWWud6z4zf25ea6Y7fZD0ItdMdvsh6BZO/tmbpfLP1H5DMdDTG92fXCsNMb3Z9cXD8pyVT+avv8jl5vpjt9kOwk30x2+yHYAfv7ZC1Pz1+GP5Nn2XiPuvrUlFQaUNUjdA6zGuJpOc9fYzdS2vrbHGvOsQbil0T+/wCz+lETNWSRQmwTpCgXB4gcc6B94UUVFo9fXRlzIGjbCxBS8lQinYKG7MbdWZjH939VddSlONUmtBNZHUT1uQZ/amufw/vsf9WOjaZy1r4/kVFzEvV65VRyABIeUHdX4n885roM7VtI8kgAImZem4FgiHxJ45+KpLnN/wDR/uCGHhZD18tF1La+pI415yI9FNon9/2f0o6kqNCpUiZg8+1v2TiP88vNIQVVfDC9wrY7ldjtGx8bjw4ZSx9Hz3HUpSzUmtBe2OonrXBj9u6v/G+9Z/Uiu0zljXx/IqLeJWqoIo3ADPy33I4n8czX3xVPo2qQwQBXVqVJSZblrmWVf1KC1lHhn//Z"

func tinyJPEG(t *testing.T) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(tinyJPEGB64)
	require.NoError(t, err)
	return b
}

// jpegSOFDimensions parses width/height from the first SOF marker.
func jpegSOFDimensions(t *testing.T, jpeg []byte) (w, h int) {
	t.Helper()
	for i := 0; i+9 < len(jpeg); i++ {
		if jpeg[i] != 0xFF {
			continue
		}
		switch jpeg[i+1] {
		case 0xC0, 0xC1, 0xC2: // SOF0/1/2
			markerLen := int(binary.BigEndian.Uint16(jpeg[i+2:]))
			if markerLen < 8 || i+9 >= len(jpeg) {
				break
			}
			return int(binary.BigEndian.Uint16(jpeg[i+7:])), int(binary.BigEndian.Uint16(jpeg[i+5:]))
		}
	}
	t.Fatal("no SOF marker found in JPEG")
	return 0, 0
}

func TestStreamInfoReady_MJPEG(t *testing.T) {
	t.Parallel()
	require.True(t, StreamInfo{Codec: model.FormatMJPEG, Hub: streamhub.New()}.Ready(),
		"MJPEG needs only the hub — no parameter sets exist")
}

func TestRTSPServeMJPEG(t *testing.T) {
	ts := startTestServer(t, Config{})
	ts.codec = model.FormatMJPEG
	ts.sps, ts.pps = nil, nil

	c := dialClient(t, ts.url)
	desc, _, err := c.Describe(mustURL(t, ts.url))
	require.NoError(t, err)
	require.Len(t, desc.Medias, 1)
	f, ok := desc.Medias[0].Formats[0].(*format.MJPEG)
	require.True(t, ok, "SDP must declare MJPEG, got %T", desc.Medias[0].Formats[0])
	require.Equal(t, uint8(26), f.PayloadType(), "RTP/JPEG uses static payload type 26")

	_, err = c.Setup(desc.BaseURL, desc.Medias[0], 0, 0)
	require.NoError(t, err)

	pkts := make(chan *rtp.Packet, 64)
	c.OnPacketRTP(desc.Medias[0], desc.Medias[0].Formats[0], func(pkt *rtp.Packet) {
		select {
		case pkts <- pkt:
		default:
		}
	})
	_, err = c.Play(nil)
	require.NoError(t, err)

	frame := tinyJPEG(t)
	wantW, wantH := jpegSOFDimensions(t, frame)
	dec, err := (&format.MJPEG{}).CreateDecoder()
	require.NoError(t, err)

	deadline := time.After(5 * time.Second)
	for {
		ts.hub.Broadcast(0, [][]byte{frame}, true)
		select {
		case p := <-pkts:
			img, derr := dec.Decode(p)
			if derr == nil && len(img) > 0 {
				gotW, gotH := jpegSOFDimensions(t, img)
				require.Equal(t, wantW, gotW, "round-tripped frame must keep source width")
				require.Equal(t, wantH, gotH, "round-tripped frame must keep source height")
				return
			}
		case <-time.After(200 * time.Millisecond):
			select {
			case <-deadline:
				t.Fatal("no decodable RTP/JPEG frame received within 5s")
			default:
			}
		}
	}
}
