package wsstream

// Message types for WebSocket binary protocol.
const (
	MsgTypeCodecInfo      byte = 0x01 // codec_info: server→client
	MsgTypeVideoFrame     byte = 0x02 // video_frame: server→client
	MsgTypeAudioFrame     byte = 0x03 // audio_frame: server→client
	MsgTypeKeyframeReq    byte = 0x04 // keyframe_request: client→server
	MsgTypeAudioCodecInfo byte = 0x05 // audio_codec_info: server→client, sent before audio frames
	MsgTypeQualityInfo    byte = 0x06 // quality_info: server→client, sent once at stream start (#541)
	MsgTypeEOS            byte = 0xFF // eos: server→client, camera went offline
)

// Codec string constants.
const (
	CodecH264  = "h264"
	CodecH265  = "h265"
	CodecMJPEG = "mjpeg" // JPEG frames: VideoFrame.NALUs[0] contains a complete JPEG image
)

// CodecInfo contains codec configuration data sent once at stream start.
// This is the binary equivalent of AVCDecoderConfigurationRecord /
// HEVCDecoderConfigurationRecord, but simplified for WebSocket transport.
type CodecInfo struct {
	Codec   string // "h264", "h265", or "mjpeg"
	Profile byte   // profile indication from SPS (0 for MJPEG)
	Level   byte   // level indication from SPS (0 for MJPEG)
	SPS     []byte // sequence parameter set (nil for MJPEG)
	PPS     []byte // picture parameter set (nil for MJPEG)
	VPS     []byte // video parameter set (H.265 only)
}

// VideoFrame contains a single video frame's presentation timestamp
// and NAL unit data. NALUs do NOT include start codes (Annex B) or
// length prefixes — they are raw NAL unit payloads matching the
// [][]byte format used by StreamHub.
type VideoFrame struct {
	PTS        int64    // presentation timestamp in 90kHz clock
	IsKeyframe bool     // true for IDR frames
	NALUs      [][]byte // access unit NALUs without start codes
	// IngestAt is the hub-entry wallclock (unix ms) relayed from
	// model.FrameMsg.IngestAt so the browser can measure end-to-end live
	// latency (#469). Appended to the wire format as a trailing 8-byte field —
	// backwards-compatible; older clients stop parsing at the last NALU.
	IngestAt int64
}

// Audio codec byte constants for wire format.
const (
	AudioCodecG711Mu byte = 0x01 // G.711 μ-law
	AudioCodecG711A  byte = 0x02 // G.711 A-law
	AudioCodecOpus   byte = 0x03 // Opus
	AudioCodecAAC    byte = 0x04 // AAC
)

// AudioCodecInfo contains audio codec configuration data sent once when
// audio is available on a stream. Sent after CodecInfo on viewer connect.
type AudioCodecInfo struct {
	Codec      byte   // audio codec byte (AudioCodecG711Mu, etc.)
	SampleRate uint32 // sample rate in Hz (e.g. 8000, 44100, 48000)
	Channels   uint8  // number of channels (1=mono, 2=stereo)
	// Config carries codec-specific setup bytes needed by the client decoder:
	//   AAC:  AudioSpecificConfig (AASC) — required by WebCodecs AudioDecoder.
	//   Opus: channel mapping / pre-skip blob (reserved; not currently produced).
	//   G.711: nil (μ-law/A-law is fully described by Codec + SampleRate).
	// Older clients that stop parsing after Channels ignore this field, so the
	// wire extension (config_len + config appended in EncodeAudioCodecInfo) is
	// backwards-compatible.
	Config []byte
}

// QualityInfo reports which stream variant the server is actually serving
// ("main" or "sub"). Sent once as the first message on viewer connect when
// quality negotiation applies (#541) — the WebSocket 101 upgrade response
// cannot carry the X-Stream-Quality header (the upgrader writes its own
// header set), so the negotiated outcome travels in-band instead. Clients
// that don't know the type ignore the message (backwards-compatible).
type QualityInfo struct {
	Quality string // "main" or "sub"
}

// AudioFrameData contains a single audio frame's presentation timestamp,
// codec identifier, and raw encoded audio data.
type AudioFrameData struct {
	PTS   int64  // presentation timestamp in 90kHz clock
	Codec byte   // audio codec byte (AudioCodecG711Mu, etc.)
	Data  []byte // raw encoded audio data
}
