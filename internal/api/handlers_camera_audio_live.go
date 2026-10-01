package api

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"
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

// --- Live listening: GET /api/cameras/{cameraID}/audio/live.wav ---
//
// Streams a never-ending PCM WAV transcoded live from the audio device's hub
// (G.711 only — AAC has no WAV story and is refused with a hint; AAC live
// would need an ADTS wrapper, deferred until a real AAC mic shows up).
// Browsers play progressive WAV through <audio> with unknown sizes spelled as
// 0xFFFFFFFF. The stream ends on client disconnect, source silence (30s), a
// codec change away from G.711 (frames stop arriving → silence timeout), or a
// 6h hard cap against zombie listeners.

const (
	audioLiveMaxIdle    = 30 * time.Second
	audioLiveMaxSession = 6 * time.Hour
	audioLiveFlushEvery = 2 * time.Second
)

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
	flusher, ok := w.(http.Flusher)
	if !ok {
		WriteError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	rate := ar.AudioSampleRate()
	channels := ar.AudioChannels()
	if rate == 0 {
		rate = 8000
	}
	if channels == 0 {
		channels = 1
	}
	// G.711 muxerConfig: [muLaw flag byte, rate big-endian 4 bytes].
	muLaw := true
	if cfg := ar.AudioConfig(); len(cfg) > 0 && cfg[0] == 0 {
		muLaw = false
	}

	// Subscribe to the hub; the callback runs on the hub's drain goroutine —
	// it must never block, so frames land in a bounded channel with drops.
	consumerID := fmt.Sprintf("api-live-audio-%d", time.Now().UnixNano())
	frames := make(chan []byte, 64)
	var dropped int64
	if err := hub.SubscribeAudio(consumerID, func(_ int64, codec model.AudioCodec, data []byte) {
		if codec != model.AudioG711 {
			return // codec changed mid-session (reconnect) — WAV stream goes silent, idle timeout ends it
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

	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%s-live.wav", cameraID))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	if err := writeStreamingWavHeader(w, rate, channels); err != nil {
		return // client hung up already
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
		"camera_id", cameraID, "remote", r.RemoteAddr, "ua", r.UserAgent())

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
			if _, err := w.Write(g711ToPCM(data, muLaw, rate)); err != nil {
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

// writeStreamingWavHeader emits the canonical 44-byte header with both size
// fields at 0xFFFFFFFF — the convention for endless WAV streams; browsers
// treat the body as an open-ended PCM stream.
func writeStreamingWavHeader(w http.ResponseWriter, sampleRate, channels int) error {
	hdr := make([]byte, 44)
	copy(hdr[0:], "RIFF")
	putUint32(hdr[4:], 0xFFFFFFFF)
	copy(hdr[8:], "WAVE")
	copy(hdr[12:], "fmt ")
	putUint32(hdr[16:], 16)
	putUint16(hdr[20:], 1)
	putUint16(hdr[22:], uint16(channels))
	putUint32(hdr[24:], uint32(sampleRate))
	putUint32(hdr[28:], uint32(sampleRate*channels*2))
	putUint16(hdr[32:], uint16(channels*2))
	putUint16(hdr[34:], 16)
	copy(hdr[36:], "data")
	putUint32(hdr[40:], 0xFFFFFFFF)
	_, err := w.Write(hdr)
	return err
}

func putUint32(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}

func putUint16(b []byte, v uint16) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
}
