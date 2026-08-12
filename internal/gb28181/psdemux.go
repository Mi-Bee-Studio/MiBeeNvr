package gb28181

import (
	"errors"
	"slices"
)

var (
	ErrIncompletePSM      = errors.New("gb28181: incomplete PSM packet")
	ErrIncompletePES      = errors.New("gb28181: incomplete PES packet")
	ErrUnknownStreamType  = errors.New("gb28181: unknown stream type")
	ErrNoVideoStreamFound = errors.New("gb28181: no video stream found in PSM")
)

// MPEG-PS start codes
const (
	startCodePack     = 0xBA
	startCodeSystem   = 0xBB
	startCodePSM      = 0xBC
	startCodePadding  = 0xBE
	startCodePrivate2 = 0xBF
	startCodeAudioMin = 0xC0
	startCodeAudioMax = 0xDF
	startCodeVideoMin = 0xE0
	startCodeVideoMax = 0xEF
)

// MPEG-PS stream types from PSM
const (
	streamTypeH264  = 0x1B // AVC video stream
	streamTypeH265  = 0x24 // H.265/HEVC video stream
	streamTypeG711  = 0x90 // G.711
	streamTypeG726  = 0x91 // G.726
	streamTypeG7221 = 0x92 // G.722.1
	streamTypeAAC   = 0x0F // AAC ADTS
)

// PSDemuxer extracts NALUs from complete MPEG-PS access unit byte streams.
// It is Stage 2 of the two-stage pipeline (Stage 1 = RTP reassembly).
type PSDemuxer struct {
	streamType  byte   // 0x1B=H.264, 0x24=H.265, 0=unknown
	naluType    string // "h264" or "h265"
	buf         []byte // residual buffer for incomplete PS structure
	videoPesBuf []byte // buffer for assembling video PES payload
	currentPTS  int64  // PTS for current PES (90kHz clock)
}

// NewPSDemuxer creates a new MPEG-PS to H.264/H.265 NALU demuxer.
func NewPSDemuxer() *PSDemuxer {
	return &PSDemuxer{}
}

// FeedAU processes a complete access unit PS byte stream and returns extracted NALUs.
// The auPayload must be a complete PS packet (Stage 1 already reassembled it).
// NALUs are returned with Annex-B start codes stripped.
func (d *PSDemuxer) FeedAU(auPayload []byte, ptsTicks int64) ([][]byte, error) {
	if len(auPayload) == 0 {
		return nil, nil
	}

	// Prepend any residual buffers from previous calls
	data := auPayload
	if len(d.buf) > 0 {
		data = slices.Concat(d.buf, data)
		d.buf = nil
	}
	if len(d.videoPesBuf) > 0 {
		data = slices.Concat(d.videoPesBuf, data)
		d.videoPesBuf = nil
	}

	var nalus [][]byte
	offset := 0

feedLoop:
	for offset < len(data) {
		// Find next start code
		startCodePos, startCode, err := findPSStartCode(data[offset:])
		if err != nil {
			// No more start codes - save remainder as residual
			d.buf = data[offset:]
			break feedLoop
		}
		startCodePos += offset

		switch startCode {
		case startCodePack:
			// Pack header: 4-byte start code + 10 fixed bytes + stuffing bytes.
			if startCodePos+14 > len(data) {
				d.buf = data[startCodePos:]
				break feedLoop
			}
			// Stuffing length is in the low 3 bits of byte 13.
			offset = startCodePos + 14 + int(data[startCodePos+13]&0x07)
			// Fallback: some encoders emit 0xFF stuffing not counted in the length.
			if offset < len(data) && data[offset]&0x07 == 0x07 {
				for offset < len(data) && data[offset] == 0xFF {
					offset++
				}
			}
		case startCodeSystem:
			// System header: 4-byte start code + 2-byte length + content bytes.
			if startCodePos+6 > len(data) {
				d.buf = data[startCodePos:]
				break feedLoop
			}
			headerLen := int(data[startCodePos+4])<<8 | int(data[startCodePos+5])
			if startCodePos+6+headerLen > len(data) {
				d.buf = data[startCodePos:]
				break feedLoop
			}
			offset = startCodePos + 6 + headerLen
		case startCodePSM:
			// Program Stream Map - parse stream_type
			psmEnd, err := parsePSM(data[startCodePos:])
			if err != nil {
				// Incomplete PSM - save as residual
				d.buf = data[startCodePos:]
				break feedLoop
			}
			// Extract stream_type from PSM
			if psmEnd > 6 {
				psmData := data[startCodePos+6 : startCodePos+psmEnd]
				streamType, found := findVideoStreamType(psmData)
				if found {
					d.streamType = streamType
					switch streamType {
					case streamTypeH264:
						d.naluType = "h264"
					case streamTypeH265:
						d.naluType = "h265"
					}
				}
			}
			offset = startCodePos + psmEnd
		case startCodePadding, startCodePrivate2:
			// Padding/private2 streams - skip
			if startCodePos+6 > len(data) {
				d.buf = data[startCodePos:]
				break feedLoop
			}
			headerLen := int(data[startCodePos+4])<<8 | int(data[startCodePos+5])
			if startCodePos+6+headerLen > len(data) {
				d.buf = data[startCodePos:]
				break feedLoop
			}
			offset = startCodePos + 6 + headerLen
		default:
			if startCode >= startCodeAudioMin && startCode <= startCodeAudioMax {
				// Audio PES - skip it
				if startCodePos+6 > len(data) {
					d.buf = data[startCodePos:]
					break feedLoop
				}
				pesLen := int(data[startCodePos+4])<<8 | int(data[startCodePos+5])
				if pesLen == 0 {
					// Unbounded - skip to next start code
					offset = startCodePos + 6
				} else if startCodePos+6+pesLen > len(data) {
					d.buf = data[startCodePos:]
					break feedLoop
				} else {
					offset = startCodePos + 6 + pesLen
				}
			} else if startCode >= startCodeVideoMin && startCode <= startCodeVideoMax {
				// Video PES - extract NALUs
				pesData := data[startCodePos:]
				pesPayload, pesEnd, err := parseVideoPES(pesData)
				if err != nil {
					// Incomplete PES - keep buffering for the next call
					d.videoPesBuf = append(d.videoPesBuf, pesData...)
					break feedLoop
				}
				if pesEnd > len(pesData) {
					// Incomplete - buffer for the next call
					d.videoPesBuf = append(d.videoPesBuf, pesData...)
					break feedLoop
				}
				// Extract NALUs from the PES payload
				if len(pesPayload) > 0 {
					d.currentPTS = ptsTicks
					nalus = append(nalus, extractNALUs(pesPayload, d.naluType)...)
				}
				// Advance past the PES
				offset = startCodePos + pesEnd
			} else {
				// Unknown start code - skip to next position
				offset = startCodePos + 4
			}
		}
	}

	return nalus, nil
}

// Flush returns any remaining buffered NALUs from incomplete PES data.
func (d *PSDemuxer) Flush() [][]byte {
	var nalus [][]byte

	// Process any buffered video PES data
	if len(d.videoPesBuf) > 0 {
		// Strip the PES header so payload NALUs are extracted cleanly.
		payload := d.videoPesBuf
		if len(payload) >= 8 {
			headerLen := 8 + int(payload[7])
			if headerLen <= len(payload) {
				payload = payload[headerLen:]
			}
		}
		nalus = append(nalus, extractNALUs(payload, d.naluType)...)
		d.videoPesBuf = nil
	}

	// Process any residual buffer
	if len(d.buf) > 0 {
		nalus = append(nalus, extractNALUs(d.buf, d.naluType)...)
		d.buf = nil
	}

	return nalus
}

// Codec returns the detected codec type ("h264" or "h265").
func (d *PSDemuxer) Codec() string {
	return d.naluType
}
