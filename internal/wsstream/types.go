package wsstream

// Message types for WebSocket binary protocol.
const (
	MsgTypeCodecInfo   byte = 0x01 // codec_info: server→client
	MsgTypeVideoFrame  byte = 0x02 // video_frame: server→client
	MsgTypeAudioFrame  byte = 0x03 // audio_frame: reserved, server→client
	MsgTypeKeyframeReq byte = 0x04 // keyframe_request: client→server
	MsgTypeEOS       byte = 0xFF // eos: server→client, camera went offline
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

// WSError represents a WebSocket protocol-level error.
type WSError struct {
	Message string
}

func (e *WSError) Error() string {
	return e.Message
}
