package recorder

import (
	"fmt"
	"net/url"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// RTSPMediaInfo is a DESCRIBE-level snapshot of an RTSP source: which video
// format it serves and whether it carries a recordable audio track. It backs
// the camera-form connection probe for standalone audio sources (#964) —
// "what actually comes out of this URL" — without a SETUP or any media flow.
type RTSPMediaInfo struct {
	DescribeOK  bool
	DescribeErr string // failure reason when DescribeOK is false

	Video string // "H265"/"H264"/"MJPEG"/"" — same values as ProbeRTSPEncoding

	AudioCodec string // supported audio codec: "AAC"/"PCMA"/"PCMU"/"Opus"; "" if none
	ClockRate  int    // of AudioCodec
	Channels   int    // of AudioCodec

	// AudioOther names an audio track that exists but is none of AAC/G.711/
	// Opus (the recorder's supported set) — e.g. "G726". Surfaced so the
	// camera-form probe can tell "no audio track" from "audio you can't
	// record".
	AudioOther string
}

// RecordableAudio reports whether the source has an audio track the
// standalone audio recorder supports.
func (i RTSPMediaInfo) RecordableAudio() bool { return i.AudioCodec != "" }

// AudioDescription renders the audio findings as one user-facing fragment
// ("PCMA 8000Hz", "G726 (unsupported)"), or "" when the stream has no audio.
func (i RTSPMediaInfo) AudioDescription() string {
	switch {
	case i.AudioCodec != "":
		s := i.AudioCodec
		if i.ClockRate > 0 {
			s += fmt.Sprintf(" %dHz", i.ClockRate)
		}
		return s
	case i.AudioOther != "":
		return i.AudioOther + " (unsupported)"
	default:
		return ""
	}
}

// ProbeRTSPMedia DESCRIBEs an RTSP source and reports its video format and
// audio track layout. It shares connection parameters with the encoding
// probe (TCP, same timeouts) so the two probes agree on a given URL.
func ProbeRTSPMedia(rtspURL, username, password string) RTSPMediaInfo {
	u, err := base.ParseURL(rtspURL)
	if err != nil {
		return RTSPMediaInfo{DescribeErr: fmt.Sprintf("invalid URL: %v", err)}
	}
	if u.User == nil && username != "" {
		u.User = url.UserPassword(username, password)
	}
	tcp := gortsplib.ProtocolTCP
	client := &gortsplib.Client{
		Scheme:       u.Scheme,
		Host:         u.Host,
		Protocol:     &tcp,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: config.DefaultRTSPTimeout,
	}
	if err := client.Start(); err != nil {
		return RTSPMediaInfo{DescribeErr: fmt.Sprintf("connect: %v", err)}
	}
	defer client.Close()

	desc, _, err := client.Describe(u)
	if err != nil {
		return RTSPMediaInfo{DescribeErr: fmt.Sprintf("RTSP DESCRIBE failed: %v", err)}
	}
	return describeMediaInfo(desc)
}

// describeMediaInfo classifies a session description with no network
// dependency, so tests can feed hand-built descriptions directly.
func describeMediaInfo(desc *description.Session) RTSPMediaInfo {
	info := RTSPMediaInfo{DescribeOK: true}

	// Video precedence mirrors probeRTSPEncodingFor: H265 first — many ONVIF
	// cameras declare H264 but stream H265.
	var h265Forma *format.H265
	var h264Forma *format.H264
	var mjpegForma *format.MJPEG
	if desc.FindFormat(&h265Forma) != nil {
		info.Video = "H265"
	} else if desc.FindFormat(&h264Forma) != nil {
		info.Video = "H264"
	} else if desc.FindFormat(&mjpegForma) != nil {
		info.Video = "MJPEG"
	}

	// Audio precedence mirrors the audio recorder's negotiation: AAC, then
	// G.711, then Opus (all three mux straight into MP4).
	var aacForma *format.MPEG4Audio
	var g711Forma *format.G711
	var opusForma *format.Opus
	switch {
	case desc.FindFormat(&aacForma) != nil:
		info.AudioCodec = "AAC"
		if aacForma.Config != nil {
			info.ClockRate = aacForma.Config.SampleRate
			info.Channels = aacForma.Config.ChannelCount
		}
	case desc.FindFormat(&g711Forma) != nil:
		if g711Forma.MULaw {
			info.AudioCodec = "PCMU"
		} else {
			info.AudioCodec = "PCMA"
		}
		info.ClockRate = g711Forma.SampleRate
		info.Channels = 1
	case desc.FindFormat(&opusForma) != nil:
		info.AudioCodec = "Opus"
		info.ClockRate = opusForma.ClockRate()
		info.Channels = opusForma.ChannelCount
	default:
		// No supported audio format — name an unsupported one if present so
		// the probe can distinguish "no audio" from "unrecordable audio".
		for _, medi := range desc.Medias {
			if medi.Type != description.MediaTypeAudio {
				continue
			}
			for _, forma := range medi.Formats {
				switch forma.(type) {
				case *format.G711, *format.MPEG4Audio, *format.Opus:
				default:
					info.AudioOther = forma.Codec()
				}
			}
		}
	}
	return info
}
