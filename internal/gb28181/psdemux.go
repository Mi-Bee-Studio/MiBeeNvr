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

// MPEG-PS stream types from PSM (GB/T 28181 附录 C StreamType assignments)
const (
	streamTypeH264  = 0x1B // AVC video stream
	streamTypeH265  = 0x24 // H.265/HEVC video stream (GB/T 28181-2022)
	streamTypeAAC   = 0x0F // AAC audio stream (ADTS framing)
	streamTypeG711A = 0x90 // G.711 A-law
	streamTypeG711U = 0x91 // G.711 μ-law
	streamTypeG722  = 0x92 // G.722
	streamTypeG723  = 0x93 // G.723
	streamTypeG729  = 0x99 // G.729
	streamTypeSVACA = 0x9B // SVAC audio
)

// Audio codec identifiers reported by AudioFrame.Codec. Only the codecs the
// NVR can mux (G.711, AAC) are demuxed; other PSM audio types are skipped.
const (
	AudioCodecG711A = "g711a"
	AudioCodecG711U = "g711u"
	AudioCodecAAC   = "aac"
)

// AudioFrame is one demuxed audio frame from the PS stream.
type AudioFrame struct {
	Codec    string // "g711a" | "g711u" | "aac"
	Data     []byte // codec payload (ADTS headers stripped for AAC)
	Config   []byte // AAC only: AudioSpecificConfig derived from ADTS (nil when payload is raw)
	PTSTicks int64  // 90kHz, translated into the RTP timestamp domain
	Samples  int    // sample count (G.711: bytes; AAC: 1024 per frame)
}

// AudioFrameHandler consumes demuxed audio frames (receiver callbacks).
type AudioFrameHandler func(frame AudioFrame)

// audioStreamCodec maps a PSM stream_type to the demuxable audio codec (""
// when the NVR cannot handle it — G.722/G.723/G.729/SVAC have no muxer path).
func audioStreamCodec(streamType byte) string {
	switch streamType {
	case streamTypeG711A:
		return AudioCodecG711A
	case streamTypeG711U:
		return AudioCodecG711U
	case streamTypeAAC:
		return AudioCodecAAC
	default:
		return ""
	}
}

// PSDemuxer extracts NALUs from complete MPEG-PS access unit byte streams.
// It is Stage 2 of the two-stage pipeline (Stage 1 = RTP reassembly).
//
// Video handling: a frame's Annex-B elementary stream is CONTINUOUS across
// PES packets — PES_packet_length is 16-bit, so devices MUST split frames
// larger than 64KB (and many split at ~14KB regardless). NALUs may therefore
// straddle PES boundaries; extracting per-PES corrupted exactly those frames
// (every large I-frame). Video PES payloads are accumulated into esBuf and
// NALUs are extracted once per FeedAU call — the RTP marker bit defines the
// access-unit boundary upstream.
type PSDemuxer struct {
	streamType  byte   // 0x1B=H.264, 0x24=H.265, 0=unknown
	naluType    string // "h264" or "h265"
	buf         []byte // residual buffer for incomplete PS structure
	videoPesBuf []byte // buffer for assembling an incomplete video PES (across RTP AUs)
	esBuf       []byte // continuous video elementary stream within the current AU
	currentPTS  int64  // PTS for current PES (90kHz clock)

	// audioCodecs maps audio stream_ids (0xC0-0xDF) to demuxable codecs,
	// populated from the PSM elementary_stream_map.
	audioCodecs map[byte]string
	// psmSeen/psmAudioEntries track the latest PSM: when a PSM declares any
	// audio entries it is authoritative and undeclared audio streams stay
	// dropped; with no PSM at all (or a video-only PSM) the codec is unknown
	// and the no-PSM fallback below resolves it instead.
	psmSeen         bool
	psmAudioEntries int
	// audioResolved latches fallback decisions (SDP hint or byte-shape
	// heuristic) per stream_id. A later PSM declaration still overrides it.
	audioResolved map[byte]string
	// audioHint seeds the fallback with the codec declared in the device's
	// INVITE answer SDP ("" = none).
	audioHint    string
	guessScanned int // bytes scanned while voting μ-law vs A-law
	guessMulaw   int // hits in the μ-law quiet cluster (0xFD-0xFF / 0x7D-0x7F)
	guessAlaw    int // hits in the A-law quiet cluster (0x54-0x56 / 0xD4-0xD6)
	// audioPesBuf holds an audio PES that straddles an AU boundary (rare —
	// audio PES packets are small enough to fit one RTP packet).
	audioPesBuf []byte
	// audioOut collects frames extracted during FeedAU; DrainAudio consumes.
	audioOut []AudioFrame
	// audioContPTSTicks continues the audio timeline across frames whose PES
	// carries no PTS (some firmware omits audio PTS entirely): each frame
	// advances by its sample count instead of inheriting the enclosing video
	// AU's RTP clock.
	audioContPTSTicks int64
	audioContSet      bool
	// ptsOffset aligns the PES PTS clock domain with the RTP timestamp
	// domain: ptsOffset = firstVideoPESPTS - firstAU_RTPts. Audio PES PTS
	// values are translated through it so audio and video share the video
	// pipeline's RTP-anchored clock.
	ptsOffset    int64
	ptsOffsetSet bool
}

// NewPSDemuxer creates a new MPEG-PS to H.264/H.265 NALU demuxer.
func NewPSDemuxer() *PSDemuxer {
	return &PSDemuxer{}
}

// FeedAU processes one access unit PS byte stream and returns extracted
// NALUs. auPayload is the PS data reassembled by Stage 1; complete marks
// whether it ends on a real access-unit boundary (RTP marker). When complete
// is false (a mid-AU jitter-buffer overflow flush), extracted NALUs cannot
// be trusted to be whole — payloads are only accumulated and the caller
// receives nothing until the AU completes. NALUs are returned with Annex-B
// start codes stripped.
func (d *PSDemuxer) FeedAU(auPayload []byte, ptsTicks int64, complete bool) ([][]byte, error) {
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
	if len(d.audioPesBuf) > 0 {
		data = slices.Concat(d.audioPesBuf, data)
		d.audioPesBuf = nil
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
				if d.audioCodecs == nil {
					d.audioCodecs = make(map[byte]string)
				}
				entries := audioStreamTypes(psmData)
				d.psmSeen = true
				d.psmAudioEntries = len(entries)
				for esID, st := range entries {
					d.audioCodecs[esID] = audioStreamCodec(st)
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
				// Audio PES - demux when the PSM declared a codec for the stream
				pesData := data[startCodePos:]
				payload, pesPTS, hasPTS, pesEnd, err := parseAudioPES(pesData)
				if err != nil || pesEnd > len(pesData) {
					// Incomplete PES (straddles the AU boundary). REPLACE the
					// buffer — the data already includes previously buffered
					// bytes (prepended above), so appending duplicates them.
					d.audioPesBuf = append([]byte(nil), pesData...)
					break feedLoop
				}
				if len(payload) > 0 {
					d.emitAudio(payload, startCode, pesPTS, hasPTS, ptsTicks)
				}
				offset = startCodePos + pesEnd
			} else if startCode >= startCodeVideoMin && startCode <= startCodeVideoMax {
				// Video PES - accumulate payload into the continuous ES buffer.
				pesData := data[startCodePos:]
				pesPayload, pesEnd, err := parseVideoPES(pesData)
				if err != nil || pesEnd > len(pesData) {
					// Incomplete PES (AU split across RTP packets or a
					// mid-AU flush). REPLACE the reassembly buffer with the
					// PES-so-far slice — data already starts with the
					// previously buffered bytes (prepended below), so
					// appending would duplicate them.
					d.videoPesBuf = append([]byte(nil), pesData...)
					break feedLoop
				}
				if len(pesPayload) > 0 {
					d.currentPTS = ptsTicks
					d.establishClockOffset(pesData, ptsTicks)
					d.esBuf = append(d.esBuf, pesPayload...)
				}
				// Advance past the PES
				offset = startCodePos + pesEnd
			} else {
				// Unknown start code - skip to next position
				offset = startCodePos + 4
			}
		}
	}

	// Extract NALUs from the accumulated elementary stream only on a real AU
	// boundary (RTP marker): the trailing NALU ends exactly there. Mid-AU
	// flushes keep accumulating — emitting a truncated trailing NALU would
	// corrupt the frame and desync the stream.
	if complete && len(d.esBuf) > 0 {
		nalus = append(nalus, extractNALUs(d.esBuf, d.naluType)...)
		d.esBuf = nil
	}
	return nalus, nil
}

// Flush returns any remaining buffered NALUs from incomplete PES data.
func (d *PSDemuxer) Flush() [][]byte {
	var nalus [][]byte

	// Extract from the residual elementary stream first.
	if len(d.esBuf) > 0 {
		nalus = append(nalus, extractNALUs(d.esBuf, d.naluType)...)
		d.esBuf = nil
	}

	// Process any buffered video PES data
	if len(d.videoPesBuf) > 0 {
		// Strip the PES header so payload NALUs are extracted cleanly.
		payload := d.videoPesBuf
		// Standard PES header: flags (2) + PES_header_data_length (1) at
		// bytes 6-8, payload at 9 + header_data_length.
		if len(payload) >= 9 {
			headerLen := 9 + int(payload[8])
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

// DrainAudio returns audio frames extracted since the last drain. Frames are
// copied out; the internal buffer is reset.
func (d *PSDemuxer) DrainAudio() []AudioFrame {
	if len(d.audioOut) == 0 {
		return nil
	}
	out := d.audioOut
	d.audioOut = nil
	return out
}

// establishClockOffset anchors the PES PTS clock to the RTP timestamp domain
// using the first video PES that carries a parseable PTS. Audio frames are
// translated through this offset so both clocks compose into one timeline
// (video PTS downstream is RTP-derived, not PES-derived).
func (d *PSDemuxer) establishClockOffset(pesData []byte, rtpTicks int64) {
	if d.ptsOffsetSet {
		return
	}
	pts, ok := parsePESPTS(pesData)
	if !ok {
		return
	}
	d.ptsOffset = pts - rtpTicks
	d.ptsOffsetSet = true
}

// emitAudio converts one audio PES payload into AudioFrames on the collector.
func (d *PSDemuxer) emitAudio(payload []byte, streamID byte, pesPTS int64, hasPTS bool, rtpTicks int64) {
	codec := d.audioCodecs[streamID]
	if codec == "" && !(d.psmSeen && d.psmAudioEntries > 0) {
		// No authoritative PSM declaration for this stream — resolve via the
		// SDP hint / byte-shape fallback (see resolveFallbackAudioCodec).
		codec = d.resolveFallbackAudioCodec(streamID, payload)
	}
	if codec == "" {
		return // not declared in PSM (or unsupported: G.722/G.723/...) and not inferable
	}
	if !d.ptsOffsetSet {
		// No video PTS seen yet (audio-only start): anchor audio to the RTP
		// clock directly so frames are still on a sane timeline.
		if hasPTS {
			d.ptsOffset = pesPTS - rtpTicks
			d.ptsOffsetSet = true
		} else {
			return
		}
	}
	var pts int64
	if hasPTS {
		pts = pesPTS - d.ptsOffset
		if pts < 0 {
			pts = rtpTicks
		}
	} else if d.audioContSet {
		pts = d.audioContPTSTicks
	} else {
		d.audioContPTSTicks = rtpTicks
		d.audioContSet = true
		pts = rtpTicks
	}

	switch codec {
	case AudioCodecG711A, AudioCodecG711U:
		d.audioOut = append(d.audioOut, AudioFrame{
			Codec:    codec,
			Data:     payload,
			PTSTicks: pts,
			Samples:  len(payload),
		})
		d.audioContPTSTicks, d.audioContSet = pts+int64(len(payload)), true
	case AudioCodecAAC:
		// GB28181 devices send AAC with ADTS framing; strip each frame and
		// derive the AudioSpecificConfig from the first header. Raw payloads
		// (no ADTS) carry Config=nil — sinks without a stored ASC skip them.
		for _, frame := range splitADTS(payload) {
			d.audioOut = append(d.audioOut, AudioFrame{
				Codec:    codec,
				Data:     frame.data,
				Config:   adtsToASC(frame.header),
				PTSTicks: pts,
				Samples:  1024,
			})
			pts += 1024
			d.audioContPTSTicks, d.audioContSet = pts, true
		}
	}
}

// Codec returns the detected codec type ("h264" or "h265").
func (d *PSDemuxer) Codec() string {
	return d.naluType
}
