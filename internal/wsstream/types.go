package wsstream

// Message types for WebSocket binary protocol.
const (
	MsgTypeCodecInfo      byte = 0x01 // codec_info: server→client
	MsgTypeVideoFrame     byte = 0x02 // video_frame: server→client
	MsgTypeAudioFrame     byte = 0x03 // audio_frame: server→client
	MsgTypeKeyframeReq    byte = 0x04 // keyframe_request: client→server
	MsgTypeAudioCodecInfo byte = 0x05 // audio_codec_info: server→client, sent before audio frames
	MsgTypeEOS            byte = 0xFF // eos: server→client, camera went offline
)

// Codec string constants.
const (
	CodecH264 = "h264"
	CodecH265 = "h265"
)

// CodecInfo contains codec configuration data sent once at stream start.
// This is the binary equivalent of AVCDecoderConfigurationRecord /
// HEVCDecoderConfigurationRecord, but simplified for WebSocket transport.
type CodecInfo struct {
	Codec   string // "h264" or "h265"
	Profile byte   // profile indication from SPS
	Level   byte   // level indication from SPS
	SPS     []byte // sequence parameter set
	PPS     []byte // picture parameter set
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
}

// AudioFrameData contains a single audio frame's presentation timestamp,
// codec identifier, and raw encoded audio data.
type AudioFrameData struct {
	PTS   int64  // presentation timestamp in 90kHz clock
	Codec byte   // audio codec byte (AudioCodecG711Mu, etc.)
	Data  []byte // raw encoded audio data
}
