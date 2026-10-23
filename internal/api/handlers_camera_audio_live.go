package api

import (
	"encoding/binary"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

var audioLiveLogger = slogx.Component("api-audio-live")

// --- Audit ring (compliance) ---
//
// Audio evidence access is more sensitive than video: every WAV fetch and
// live-listening session is recorded here (in addition to the slog line) in a
// bounded in-memory ring, queryable via GET /api/recordings/audio-audit.

const audioAuditRingCap = 200

type audioAuditEntry struct {
	At        time.Time `json:"at"`
	Kind      string    `json:"kind"` // "wav" | "live"
	Recording string    `json:"recording,omitempty"`
	CameraID  string    `json:"camera_id"`
	Remote    string    `json:"remote"`
	UA        string    `json:"ua"`
	Bytes     int64     `json:"bytes,omitempty"`
	DurationS float64   `json:"duration_s,omitempty"` // live sessions
}

var audioAudit = struct {
	mu      sync.Mutex
	entries []audioAuditEntry
}{}

func recordAudioAudit(e audioAuditEntry) {
	audioAudit.mu.Lock()
	defer audioAudit.mu.Unlock()
	audioAudit.entries = append(audioAudit.entries, e)
	if len(audioAudit.entries) > audioAuditRingCap {
		audioAudit.entries = audioAudit.entries[len(audioAudit.entries)-audioAuditRingCap:]
	}
}

// handleAudioAudit returns the recent audio-access audit trail, newest first.
func (h *Handler) handleAudioAudit(w http.ResponseWriter, r *http.Request) {
	limit := 200
	audioAudit.mu.Lock()
	n := len(audioAudit.entries)
	out := make([]audioAuditEntry, 0, n)
	for i := n - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, audioAudit.entries[i])
	}
	audioAudit.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"entries": out})
}

// --- Live listening ---
//
// GET /api/cameras/{cameraID}/audio/live.wav — G.711 only, transcoded to a
// streaming PCM WAV (kept for existing consumers: the App and miniprogram
// fetch this URL directly).
//
// GET /api/cameras/{cameraID}/audio/live — format-adaptive, ZERO-DECODE live
// audio: the hub's compressed frames are re-framed (not transcoded) into a
// container the browser decodes natively —
//
//	g711 → audio/wav  (table-decode to 16-bit PCM, same bytes as /live.wav)
//	aac  → audio/aac  (each AU prefixed with a 7-byte ADTS header)
//	opus → audio/ogg  (Ogg Opus: OpusHead + OpusTags + one page per packet)
//
// Per-frame CPU is a memcpy — no decoder runs on the NVR, so live listening
// stays near-free even on low-power ARM boxes. Timing model: AAC AUs carry
// 1024 samples; Opus AUs are the nominal 20ms frame (960 samples @48k) the
// recorder itself uses for its MP4 muxer — live and recorded audio therefore
// agree on pacing.
//
// Both endpoints share the same lifecycle: the stream ends on client
// disconnect, source silence (30s), a mid-session codec change away from the
// opened format (frames stop arriving → silence timeout), a 6h hard cap
// against zombie listeners, or — WAV only — the nominal window the response
// declares. Chromium's WAV demuxer rejects open-ended bodies (it needs a
// known resource size to clamp the 0xFFFFFFFF markers; the same bytes play
// fine as a finite blob), so the WAV branches declare a finite
// Content-Length covering a nominal listening window and end the body
// cleanly at exactly that length (#1052). Earlier ends (idle/cap/disconnect)
// truncate the response, which browsers accept after draining the buffer.
// ADTS and Ogg are self-delimiting streams and stay open-ended.

const (
	audioLiveMaxIdle    = 30 * time.Second
	audioLiveMaxSession = 6 * time.Hour
	audioLiveFlushEvery = 500 * time.Millisecond // frequent small flushes: browsers start streaming playback sooner
)

// audioLiveWavNominalWindow is the listening window the WAV branches declare
// via Content-Length (a live-listen session far outlasting it is unusual; the
// client simply re-opens). Var so tests can shrink it to exercise the exact
// end-of-body path.
var audioLiveWavNominalWindow = time.Hour

// liveAudioFormat carries the per-codec framing for one live session.
type liveAudioFormat struct {
	contentType string
	ext         string                   // filename extension in Content-Disposition
	header      []byte                   // container header written once (nil = none)
	accept      model.AudioCodec         // codec the stream was opened with; other codecs are dropped
	frame       func(data []byte) []byte // per-AU bytes to append (may allocate)
	// contentLen >0 declares a finite Content-Length and ends the body at
	// exactly that many bytes (WAV branches — see the block comment); 0 keeps
	// the open-ended chunked form (ADTS/Ogg).
	contentLen int64
}

// handleAudioLiveWav: G.711 → streaming PCM WAV (legacy consumers).
func (h *Handler) handleAudioLiveWav(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "cameraID")
	rec := unwrapDelegate(h.camMgr.GetRecorder(cameraID))
	ar, ok := rec.(*recorder.AudioRecorder)
	if !ok {
		WriteError(w, http.StatusNotFound, "not an audio-source device")
		return
	}
	hub := ar.GetHub()
	if hub == nil || ar.AudioCodec() == "" {
		WriteError(w, http.StatusServiceUnavailable, "audio source not connected")
		return
	}
	if c := ar.AudioCodec(); c != "g711" {
		WriteError(w, http.StatusNotAcceptable,
			fmt.Sprintf("live listening supports G.711 sources only (this device: %s) — play back its recorded segments instead", c))
		return
	}
	f, err := newLiveWavFormat(ar)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	runLiveAudioSession(w, r, cameraID, hub, f)
}

// handleAudioLive: format-adaptive zero-decode live audio (see the block
// comment above). Unknown codecs are refused with a hint rather than served
// as garbage.
func (h *Handler) handleAudioLive(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "cameraID")
	rec := unwrapDelegate(h.camMgr.GetRecorder(cameraID))
	ar, ok := rec.(*recorder.AudioRecorder)
	if !ok {
		WriteError(w, http.StatusNotFound, "not an audio-source device")
		return
	}
	hub := ar.GetHub()
	if hub == nil || ar.AudioCodec() == "" {
		WriteError(w, http.StatusServiceUnavailable, "audio source not connected")
		return
	}
	var f liveAudioFormat
	var err error
	switch ar.AudioCodec() {
	case "g711":
		f, err = newLiveWavFormat(ar)
	case "aac":
		f, err = newLiveADTSFormat(ar)
	case "opus":
		f, err = newLiveOggOpusFormat(ar)
	default:
		err = fmt.Errorf("live listening has no browser container for codec %q — play back its recorded segments instead", ar.AudioCodec())
	}
	if err != nil {
		WriteError(w, http.StatusNotAcceptable, err.Error())
		return
	}
	runLiveAudioSession(w, r, cameraID, hub, f)
}

// runLiveAudioSession drives the shared stream lifecycle: subscribe, header,
// flush cadence, idle/session caps, audit.
func runLiveAudioSession(w http.ResponseWriter, r *http.Request, cameraID string, hub *streamhub.StreamHub, f liveAudioFormat) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		WriteError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	// Subscribe to the hub; the callback runs on the hub's drain goroutine —
	// it must never block, so frames land in a bounded channel with drops.
	consumerID := fmt.Sprintf("api-live-audio-%d", time.Now().UnixNano())
	frames := make(chan []byte, 64)
	var dropped int64
	if err := hub.SubscribeAudio(consumerID, func(_ int64, codec model.AudioCodec, data []byte) {
		if codec != f.accept {
			return // codec changed mid-session (reconnect) — stream goes silent, idle timeout ends it
		}
		select {
		case frames <- data:
		default:
			dropped++
		}
	}); err != nil {
		WriteError(w, http.StatusInternalServerError, "subscribe failed: "+err.Error())
		return
	}
	defer hub.UnsubscribeAudio(consumerID)

	w.Header().Set("Content-Type", f.contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%s-live.%s", cameraID, f.ext))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Audio-Codec", string(f.accept))
	written := int64(0)
	if f.contentLen > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(f.contentLen, 10))
	}
	if len(f.header) > 0 {
		if _, err := w.Write(f.header); err != nil {
			return // client hung up already
		}
		written = int64(len(f.header))
	}
	flusher.Flush()

	start := time.Now()
	defer func() {
		audioLiveLogger.Info("live listening ended (audit)",
			"camera_id", cameraID, "remote", r.RemoteAddr,
			"ua", r.UserAgent(), "duration", time.Since(start).Round(time.Second),
			"dropped_frames", dropped)
		recordAudioAudit(audioAuditEntry{
			At: start, Kind: "live", CameraID: cameraID,
			Remote: r.RemoteAddr, UA: r.UserAgent(),
			DurationS: time.Since(start).Seconds(),
		})
	}()
	audioLiveLogger.Info("live listening started (audit)",
		"camera_id", cameraID, "remote", r.RemoteAddr, "ua", r.UserAgent(), "codec", string(f.accept))

	ctx := r.Context()
	flushTicker := time.NewTicker(audioLiveFlushEvery)
	defer flushTicker.Stop()
	sessionCap := time.NewTimer(audioLiveMaxSession)
	defer sessionCap.Stop()
	lastFrame := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		case <-sessionCap.C:
			return
		case data := <-frames:
			lastFrame = time.Now()
			out := f.frame(data)
			if f.contentLen > 0 && written+int64(len(out)) > f.contentLen {
				out = out[:f.contentLen-written] // end cleanly at the declared length
			}
			if len(out) > 0 {
				if _, err := w.Write(out); err != nil {
					return
				}
				written += int64(len(out))
			}
			if f.contentLen > 0 && written >= f.contentLen {
				flusher.Flush()
				return
			}
		case <-flushTicker.C:
			flusher.Flush()
			if time.Since(lastFrame) > audioLiveMaxIdle {
				return
			}
		}
	}
}

// newLiveWavFormat frames a G.711 session as an endless PCM WAV. The law
// byte comes from the recorder's muxer config ([muLaw flag, rate BE32]).
func newLiveWavFormat(ar *recorder.AudioRecorder) (liveAudioFormat, error) {
	rate := ar.AudioSampleRate()
	channels := ar.AudioChannels()
	if rate == 0 {
		rate = 8000
	}
	if channels == 0 {
		channels = 1
	}
	muLaw := true
	if cfg := ar.AudioConfig(); len(cfg) > 0 && cfg[0] == 0 {
		muLaw = false
	}

	// Canonical 44-byte header with both size fields at 0xFFFFFFFF — the
	// convention for endless WAV streams; browsers treat the body as an
	// open-ended PCM stream.
	hdr := make([]byte, 44)
	copy(hdr[0:], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:], 0xFFFFFFFF)
	copy(hdr[8:], "WAVE")
	copy(hdr[12:], "fmt ")
	binary.LittleEndian.PutUint32(hdr[16:], 16)
	binary.LittleEndian.PutUint16(hdr[20:], 1)
	binary.LittleEndian.PutUint16(hdr[22:], uint16(channels))
	binary.LittleEndian.PutUint32(hdr[24:], uint32(rate))
	binary.LittleEndian.PutUint32(hdr[28:], uint32(rate*channels*2))
	binary.LittleEndian.PutUint16(hdr[32:], uint16(channels*2))
	binary.LittleEndian.PutUint16(hdr[34:], 16)
	copy(hdr[36:], "data")
	binary.LittleEndian.PutUint32(hdr[40:], 0xFFFFFFFF)

	// Finite declared length for the WAV branches (#1052): Chromium's WAV
	// demuxer needs a known resource size; a nominal window keeps the header
	// honest while staying open for far longer than a live-listen session.
	nominal := int64(rate) * int64(channels) * 2 * int64(audioLiveWavNominalWindow) / int64(time.Second)
	return liveAudioFormat{
		contentType: "audio/wav",
		ext:         "wav",
		header:      hdr,
		accept:      model.AudioG711,
		frame:       func(d []byte) []byte { return g711ToPCM(d, muLaw, rate) },
		contentLen:  int64(len(hdr)) + nominal,
	}, nil
}

// --- ADTS (AAC) ---

// adtsFreqRates is the MPEG samplingFrequencyIndex table (index 13/14
// reserved, 15 = explicit rate in the stream, unrepresentable in ADTS).
var adtsFreqRates = [13]int{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}

func adtsFreqIndex(rate int) (byte, bool) {
	for i, v := range adtsFreqRates {
		if v == rate {
			return byte(i), true
		}
	}
	return 0, false
}

// parseAudioSpecificConfig extracts the ADTS-relevant fields from a 2-byte
// AudioSpecificConfig: audioObjectType, sampling frequency index and channel
// configuration. ok=false when any field falls outside the ADTS-representable
// ranges (AOT must be AAC-LC/LTP/Main/SSR, freq index ≤ 12, 1–6 channels).
func parseAudioSpecificConfig(asc []byte) (objType, freqIdx, ch int, ok bool) {
	if len(asc) < 2 {
		return 0, 0, 0, false
	}
	objType = int(asc[0]>>3) & 0x1F
	freqIdx = int(asc[0]&0x07)<<1 | int(asc[1]>>7)
	ch = int(asc[1]>>3) & 0x0F
	if objType >= 2 && objType <= 5 && freqIdx <= 12 && ch >= 1 && ch <= 6 {
		return objType, freqIdx, ch, true
	}
	return 0, 0, 0, false
}

// newLiveADTSFormat re-frames AAC AUs from the hub into an ADTS stream the
// browser decodes natively. Header parameters come from the recorder's
// AudioSpecificConfig when present (RTSP sources carry it); push ingests
// that only know rate/channels fall back to AAC-LC at the armed rate.
// Known limitation: HE-AAC's implicit SBR cannot be signalled in ADTS —
// such streams play at half speed; camera mics are invariably AAC-LC.
func newLiveADTSFormat(ar *recorder.AudioRecorder) (liveAudioFormat, error) {
	var profile, freqIdx, ch byte
	fromASC := false
	if objType, fi, cc, ok := parseAudioSpecificConfig(ar.AudioConfig()); ok {
		profile = byte(objType - 1) // ADTS profile field = audioObjectType-1 (2=LC → 1)
		freqIdx = byte(fi)
		ch = byte(cc)
		fromASC = true
	}
	if !fromASC {
		fi, ok := adtsFreqIndex(ar.AudioSampleRate())
		if !ok {
			return liveAudioFormat{}, fmt.Errorf("cannot frame AAC without a standard sample rate (armed %d Hz) — ASC missing", ar.AudioSampleRate())
		}
		c := ar.AudioChannels()
		if c < 1 || c > 6 {
			c = 1
		}
		profile, freqIdx, ch = 1, fi, byte(c)
	}
	return liveAudioFormat{
		contentType: "audio/aac",
		ext:         "aac",
		accept:      model.AudioAAC,
		frame: func(au []byte) []byte {
			out := make([]byte, 7+len(au))
			n := len(au) + 7            // frame_length includes the ADTS header
			out[0], out[1] = 0xFF, 0xF1 // syncword, MPEG-4, layer 0, no CRC
			out[2] = profile<<6 | freqIdx<<2 | ch>>2
			out[3] = ch&3<<6 | byte(n>>11)&0x03
			out[4] = byte(n >> 3)
			out[5] = byte(n&0x07)<<5 | 0x1F // buffer fullness 0x7FF (VBR), high bits
			out[6] = 0xFC                   // low bits + zero blocks-in-frame
			copy(out[7:], au)
			return out
		},
	}, nil
}

// --- Ogg Opus ---

// oggCRCPoly: Ogg uses CRC-32 with polynomial 0x04c11db7, init 0, no
// reflection, no final xor — not the IEEE table hash/crc32 ships.
var oggCRCTable = func() [256]uint32 {
	var t [256]uint32
	for i := range 256 {
		c := uint32(i) << 24
		for range 8 {
			if c&0x80000000 != 0 {
				c = c<<1 ^ 0x04c11db7
			} else {
				c <<= 1
			}
		}
		t[i] = c
	}
	return t
}()

func oggCRC(p []byte) uint32 {
	c := uint32(0)
	for _, b := range p {
		c = c<<8 ^ oggCRCTable[byte(c>>24)^b]
	}
	return c
}

// oggOpusBuilder emits Ogg pages for one logical Opus stream.
type oggOpusBuilder struct {
	serial  uint32
	seq     uint32
	granule int64
}

func (b *oggOpusBuilder) page(headerType byte, granule int64, payload []byte) []byte {
	nSegs := (len(payload) + 254) / 255
	segs := make([]byte, nSegs)
	for i := range segs {
		segs[i] = 255
	}
	if rem := len(payload) % 255; rem > 0 {
		segs[nSegs-1] = byte(rem)
	}
	buf := make([]byte, 27+nSegs+len(payload))
	copy(buf, "OggS")
	buf[5] = headerType
	binary.LittleEndian.PutUint64(buf[6:], uint64(granule))
	binary.LittleEndian.PutUint32(buf[14:], b.serial)
	binary.LittleEndian.PutUint32(buf[18:], b.seq)
	b.seq++
	buf[26] = byte(nSegs)
	copy(buf[27:], segs)
	copy(buf[27+nSegs:], payload)
	binary.LittleEndian.PutUint32(buf[22:], oggCRC(buf)) // CRC computed over the page with the field zeroed
	return buf
}

// newLiveOggOpusFormat re-frames Opus packets from the hub into an Ogg Opus
// stream. Granule advances by the nominal 20ms frame (960 samples @48k) —
// the same duration model the AudioRecorder's own MP4 muxer uses for Opus
// AUs, so live pacing matches recordings. Input sample rate is informational
// (browsers always decode Opus at 48kHz).
func newLiveOggOpusFormat(ar *recorder.AudioRecorder) (liveAudioFormat, error) {
	ch := ar.AudioChannels()
	if ch == 0 {
		if cfg := ar.AudioConfig(); len(cfg) > 0 && cfg[0] > 0 {
			ch = int(cfg[0])
		}
	}
	if ch == 0 || ch > 8 {
		ch = 1
	}
	rate := ar.AudioSampleRate()
	if rate == 0 {
		rate = 48000
	}
	b := &oggOpusBuilder{serial: uint32(time.Now().UnixNano())}

	head := make([]byte, 0, 19)
	head = append(head, "OpusHead"...)
	head = append(head, 1, byte(ch))                 // version 1, channel count
	head = binary.LittleEndian.AppendUint16(head, 0) // pre-skip (RTP sources: none)
	head = binary.LittleEndian.AppendUint32(head, uint32(rate))
	head = binary.LittleEndian.AppendUint16(head, 0) // output gain
	head = append(head, 0)                           // mapping family 0 (mono/stereo)

	tags := make([]byte, 0, 8+4+len("MiBeeNvr")+4)
	tags = append(tags, "OpusTags"...)
	tags = binary.LittleEndian.AppendUint32(tags, uint32(len("MiBeeNvr")))
	tags = append(tags, "MiBeeNvr"...)
	tags = binary.LittleEndian.AppendUint32(tags, 0)

	return liveAudioFormat{
		contentType: "audio/ogg",
		ext:         "ogg",
		header:      append(b.page(0x02, 0, head), b.page(0, 0, tags)...),
		accept:      model.AudioOpus,
		frame: func(pkt []byte) []byte {
			b.granule += 960
			return b.page(0, b.granule, pkt)
		},
	}, nil
}
