package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/hls"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/substream"
)

// StreamHandler is a protocol-agnostic interface for live streaming handlers.
// Each streaming protocol (HLS, WebRTC, FLV, LL-HLS) implements this interface
// so the API layer can start/stop streams without type-switch spaghetti.
type StreamHandler interface {
	// Name returns the protocol identifier (e.g. "hls", "webrtc", "flv", "ll-hls").
	Name() string
	// CanHandle returns true if this handler supports the given codec format.
	CanHandle(codec model.Format) bool
	// StartStream starts a live stream for the given camera using the provided recorder.
	StartStream(camID string, rec model.Recorder, opts StreamStartOptions) error
	// StopStream stops the live stream for the given camera.
	StopStream(camID string) error
}

// StreamStartOptions holds optional parameters for starting a stream.
type StreamStartOptions struct {
	MaxFPS       int
	SubStreamURL string
}

// StreamRegistry manages registered stream handlers and provides
// protocol availability queries per codec format.
type StreamRegistry struct {
	handlers []StreamHandler
}

// NewStreamRegistry creates an empty StreamRegistry.
func NewStreamRegistry() *StreamRegistry {
	return &StreamRegistry{}
}

// Register adds a stream handler to the registry.
func (r *StreamRegistry) Register(h StreamHandler) {
	r.handlers = append(r.handlers, h)
}

// handlersForCodec returns all handlers that can handle the given codec format.
func (r *StreamRegistry) handlersForCodec(codec model.Format) []StreamHandler {
	var result []StreamHandler
	for _, h := range r.handlers {
		if h.CanHandle(codec) {
			result = append(result, h)
		}
	}
	return result
}

// protocolsForCodec returns the names of all protocols that support the given codec.
func (r *StreamRegistry) protocolsForCodec(codec model.Format) []string {
	var result []string
	for _, h := range r.handlers {
		if h.CanHandle(codec) {
			result = append(result, h.Name())
		}
	}
	return result
}

// ProtocolDetail describes a protocol's availability for the API response.
// json tags are snake_case like every other API response field (#332) —
// the Go field names used to leak through encoding/json's default naming.
type ProtocolDetail struct {
	Protocol  string `json:"protocol"`
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
}

// ProtocolsDetailForCodec returns detailed protocol availability for the given codec.
// Each handler contributes its availability and optional reason if unavailable.
func (r *StreamRegistry) ProtocolsDetailForCodec(codec model.Format) []ProtocolDetail {
	var result []ProtocolDetail
	for _, h := range r.handlers {
		if h.CanHandle(codec) {
			result = append(result, ProtocolDetail{
				Protocol:  h.Name(),
				Available: true,
			})
		} else if dis, ok := h.(ConditionalHandler); ok {
			// Handler supports this codec but is conditionally unavailable
			if dis.SupportedCodec(codec) {
				result = append(result, ProtocolDetail{
					Protocol:  h.Name(),
					Available: false,
					Reason:    dis.UnavailabilityReason(codec),
				})
			}
		}
	}
	return result
}

// --- HLSStreamHandler ---

// HLSStreamHandler implements StreamHandler for HLS live streaming.
// It encapsulates the HLS-specific logic previously scattered across
// type-switch blocks in handlers_hls.go.
type HLSStreamHandler struct {
	Mgr *hls.Manager
}

// Name returns the protocol identifier for HLS.
func (h *HLSStreamHandler) Name() string { return "hls" }

// CanHandle returns true for H.264 and H.265 formats (HLS supports both).
func (h *HLSStreamHandler) CanHandle(codec model.Format) bool {
	return codec == model.FormatH264 || codec == model.FormatH265
}

// StartStream starts an HLS stream for the given camera.
// It extracts codec parameters from the recorder and subscribes the HLS
// manager to the recorder's StreamHub for frame delivery.
func (h *HLSStreamHandler) StartStream(camID string, rec model.Recorder, opts StreamStartOptions) error {
	if h.Mgr == nil {
		return errors.New("HLS manager not available")
	}

	hub := getRecorderHub(rec)

	// Determine codec and extract parameters from recorder.
	// We use model.HLSProvider interface to get codec params (format-agnostic),
	// or fall back to concrete type access for the unwrapped recorder.
	if provider, ok := rec.(model.HLSProvider); ok {
		codec, sps, pps, vps := provider.CodecParams()
		return h.startFromProvider(camID, codec, sps, pps, vps, hub, provider, opts)
	}

	// For recorders that don't implement HLSProvider, unwrap ONVIF delegation
	// and use concrete type access for SPS/PPS/VPS.
	actualRec := unwrapDelegate(rec)

	switch r := actualRec.(type) {
	case *recorder.H264Recorder:
		sps := r.SPS()
		pps := r.PPS()
		if sps == nil || pps == nil {
			return errors.New("SPS/PPS not available yet, waiting for video stream")
		}
		if err := h.Mgr.StartStream(camID, sps, pps, opts.MaxFPS); err != nil {
			return err
		}
		h.subscribeHub(camID, hub, false, opts)

	case *recorder.H265Recorder:
		vps := r.VPS()
		sps := r.SPS()
		pps := r.PPS()
		if vps == nil || sps == nil || pps == nil {
			return errors.New("VPS/SPS/PPS not available yet, waiting for video stream")
		}
		if err := h.Mgr.StartStreamH265(camID, vps, sps, pps, opts.MaxFPS); err != nil {
			return err
		}
		h.subscribeHub(camID, hub, true, opts)

	default:
		return &model.HLSSupportedCodecError{CameraID: camID}
	}

	return nil
}

// startFromProvider starts an HLS stream using the HLSProvider interface.
func (h *HLSStreamHandler) startFromProvider(
	camID string,
	codec model.Format,
	sps, pps, vps []byte,
	hub *streamhub.StreamHub,
	provider model.HLSProvider,
	opts StreamStartOptions,
) error {
	switch codec {
	case model.FormatH264:
		if sps == nil || pps == nil {
			return errors.New("codec params not ready yet, waiting for video stream")
		}
		if err := h.Mgr.StartStream(camID, sps, pps, opts.MaxFPS); err != nil {
			return err
		}
		h.subscribeHub(camID, hub, false, opts)

	case model.FormatH265:
		if sps == nil || pps == nil {
			return errors.New("codec params not ready yet, waiting for video stream")
		}
		if vps == nil {
			return errors.New("VPS not ready yet, waiting for video stream")
		}
		if err := h.Mgr.StartStreamH265(camID, vps, sps, pps, opts.MaxFPS); err != nil {
			return err
		}
		h.subscribeHub(camID, hub, true, opts)

	default:
		return &model.HLSSupportedCodecError{CameraID: camID}
	}
	return nil
}

// subscribeHub handles the sub-stream URL / main stream subscription logic.
func (h *HLSStreamHandler) subscribeHub(camID string, hub *streamhub.StreamHub, isH265 bool, opts StreamStartOptions) {
	if hub == nil {
		return
	}

	// Check if sub-stream URL is configured
	if opts.SubStreamURL != "" {
		fallback := func() {
			_ = subscribeHLS(hub, camID, h.Mgr, isH265)
		}
		if subErr := h.Mgr.StartSubStreamReader(camID, opts.SubStreamURL, isH265, fallback); subErr != nil {
			streamLogger.Warn("failed to start HLS sub-stream reader, falling back to main stream",
				"camera_id", camID, "error", subErr)
			fallback()
		}
		// Sub-stream reader is running — do NOT subscribe hub on recorder
	} else {
		_ = subscribeHLS(hub, camID, h.Mgr, isH265)
	}
}

// StopStreamWithRecorder stops the HLS stream for the given camera and unsubscribes
// from the recorder's StreamHub.
func (h *HLSStreamHandler) StopStreamWithRecorder(camID string, rec model.Recorder) error {
	if h.Mgr == nil || !h.Mgr.IsActive(camID) {
		return nil
	}

	// Unsubscribe HLS consumer from StreamHub
	if rec != nil {
		hub := getRecorderHub(rec)
		if hub != nil {
			hub.Unsubscribe("hls")
		}
	}

	h.Mgr.StopStream(camID)
	return nil
}

// StopStream implements StreamHandler.StopStream.
func (h *HLSStreamHandler) StopStream(camID string) error {
	return h.StopStreamWithRecorder(camID, nil)
}

var streamLogger = slogx.Component("stream-handler")

// unwrapper is an interface for recorders that delegate to an inner recorder.
// This avoids importing recorder.ONVIFRecorder directly in the stream handler.
type unwrapper interface {
	Delegate() model.Recorder
}

// unwrapDelegate returns the innermost recorder by unwrapping delegate layers (e.g. ONVIF).
// If the recorder is not a delegator, it returns the recorder unchanged.
func unwrapDelegate(rec model.Recorder) model.Recorder {
	for {
		if u, ok := rec.(unwrapper); ok {
			if d := u.Delegate(); d != nil {
				rec = d
				continue
			}
		}
		return rec
	}
}

// getCodecParams extracts codec parameters from a recorder.
// Uses the HLSProvider interface first (covering H264/H265/Ingest/Xiaomi
// recorders via a single atomic codec-snapshot read), then falls back to a
// concrete type switch for JPEG/MJPEG cameras which have no codec parameter sets.
func getCodecParams(rec model.Recorder) (codec model.Format, sps, pps, vps []byte) {
	// Unwrap delegates (e.g. ONVIFRecorder → concrete H264/H265) so the
	// HLSProvider check sees the real recorder that owns the codec params.
	actualRec := unwrapDelegate(rec)
	if provider, ok := actualRec.(model.HLSProvider); ok {
		codec, sps, pps, vps = provider.CodecParams()
		// Return as soon as we have a usable param set. codec is always set even
		// when params are still nil (before the first keyframe) — callers treat a
		// non-nil codec with nil sps/pps as "stream initializing" (503) and a nil
		// codec as "not a codec-streaming camera" (e.g. JPEG/MJPEG).
		if codec != "" {
			return
		}
	}

	switch actualRec.(type) {
	case *recorder.HTTPJPEGRecorder:
		// ESP32 MiBeeCam and other HTTP JPEG cameras. Live preview is served via
		// latest-frame polling (MjpegLivePlayer), advertised as the "mjpeg" protocol
		// by MJPEGStreamHandler. Without this case, encoding probes return "" and
		// /protocols reports no protocols → frontend cannot live-preview the camera.
		codec = model.EncJPEG
	case *recorder.MJPEGRecorder:
		codec = model.FormatMJPEG
	}
	return
}

// getStreamHub extracts the StreamHub from a recorder via the GetHub()
// interface (implemented by every recorder type). Returns nil if the recorder
// doesn't implement it or the hub is not set.
func getStreamHub(rec model.Recorder) *streamhub.StreamHub {
	if h, ok := rec.(interface{ GetHub() *streamhub.StreamHub }); ok {
		return h.GetHub()
	}
	return nil
}

// SetStreamRegistry sets the stream registry on the handler for protocol queries.
func (h *Handler) SetStreamRegistry(reg *StreamRegistry) {
	h.streamRegistry = reg
}

// --- Stream quality negotiation (#513) ---

// Quality values for the live egress endpoints. "main" is the default;
// "sub" selects the camera's on-demand sub-stream (falls back to main when
// the camera has none — callers detect the fallback via X-Stream-Quality).
const (
	qualityMain = "main"
	qualitySub  = "sub"
)

// parseQuality parses the ?quality= parameter ("" and "main" → main).
func parseQuality(r *http.Request) (string, error) {
	switch q := r.URL.Query().Get("quality"); q {
	case "", qualityMain:
		return qualityMain, nil
	case qualitySub:
		return qualitySub, nil
	default:
		return "", fmt.Errorf("invalid quality %q (supported: main, sub)", q)
	}
}

// subKey is the protocol-manager stream key under which a camera's sub-stream
// egress is registered. Managers key entries by an opaque string, so the
// suffixed key reuses all of their machinery (viewer caps, GOP cache, idle
// watchdogs) without any per-quality plumbing inside them.
func subKey(cameraID string) string { return cameraID + "/" + qualitySub }

// acquireSub attempts the on-demand sub-stream acquisition and reports
// whether egress should serve the sub entry. On any failure (no sub config,
// pull not ready) it falls back to main — quality negotiation degrades, never
// hard-fails — and logs the reason. The header tells the client which quality
// it actually got.
func (h *Handler) acquireSub(w http.ResponseWriter, r *http.Request, cameraID string) *substream.Source {
	if h.camMgr == nil {
		w.Header().Set("X-Stream-Quality", qualityMain)
		return nil
	}
	src, err := h.camMgr.AcquireSubStream(r.Context(), cameraID)
	if err != nil {
		streamLogger.Info("quality=sub unavailable, serving main stream",
			"camera_id", cameraID, "reason", err.Error())
		w.Header().Set("X-Stream-Quality", qualityMain)
		return nil
	}
	w.Header().Set("X-Stream-Quality", qualitySub)
	return src
}

// --- WebRTCStreamHandler ---

// WebRTCStreamHandler implements StreamHandler for WebRTC WHEP.
// WebRTC streams start/stop on-demand via WHEP POST/DELETE, so StartStream
// and StopStream are no-ops. This registration exists purely for protocol
// discovery (so /api/cameras/{id}/protocols returns the correct list).
type WebRTCStreamHandler struct{}

func (h *WebRTCStreamHandler) Name() string { return "webrtc" }

func (h *WebRTCStreamHandler) CanHandle(codec model.Format) bool {
	return codec == model.FormatH264 // WebRTC only supports H.264
}

func (h *WebRTCStreamHandler) StartStream(camID string, rec model.Recorder, opts StreamStartOptions) error {
	return nil // WebRTC streams start on-demand via WHEP POST
}

func (h *WebRTCStreamHandler) StopStream(camID string) error {
	return nil // WebRTC streams stop via WHEP DELETE
}

// --- FLVStreamHandler ---

// FLVStreamHandler implements StreamHandler for HTTP-FLV.
// FLV streams start/stop on-demand via GET /stream.flv, so StartStream
// and StopStream are no-ops. This registration exists purely for protocol
// discovery (so /api/cameras/{id}/protocols returns the correct list).
type FLVStreamHandler struct{}

func (h *FLVStreamHandler) Name() string { return "flv" }

func (h *FLVStreamHandler) CanHandle(codec model.Format) bool {
	// The FLV manager muxes both H.264 and H.265 into FLV containers. Whether
	// the browser can actually DECODE the chosen codec (mpegts.js relies on MSE,
	// and Chrome/Firefox MSE lacks H.265) is a client-side concern — the frontend
	// gates FLV availability via detectMSEH265() in stream-selection.ts. Keeping
	// the backend honest about manager capability lets the frontend make the
	// right per-browser call instead of being force-routed to HLS.
	return codec == model.FormatH264 || codec == model.FormatH265
}

func (h *FLVStreamHandler) StartStream(camID string, rec model.Recorder, opts StreamStartOptions) error {
	return nil // FLV streams start on-demand via GET /stream.flv
}

func (h *FLVStreamHandler) StopStream(camID string) error {
	return nil // FLV streams stop when client disconnects
}

// --- ConditionalHandler interface ---

// ConditionalHandler is an optional interface for StreamHandlers that may be
// unavailable even for supported codecs (e.g., LL-HLS when low-latency is disabled).
type ConditionalHandler interface {
	// SupportedCodec returns true if this handler would normally support the codec,
	// regardless of current availability state.
	SupportedCodec(codec model.Format) bool
	// UnavailabilityReason returns a human-readable reason why the protocol is unavailable.
	UnavailabilityReason(codec model.Format) string
}

// --- LLHLSStreamHandler ---

// LLHLSStreamHandler implements StreamHandler for Low-Latency HLS.
// It wraps HLSStreamHandler but is registered separately in the StreamRegistry
// so the frontend can discover LL-HLS as a distinct protocol.
//
// LL-HLS is always advertised as available (H.264/H.265) — the backend muxer
// supports low-latency fMP4 unconditionally. Whether the browser can actually
// play it (e.g. H.265 via MSE) is a frontend capability concern, gated by the
// same browser-probe logic as HLS/FLV.
type LLHLSStreamHandler struct {
	HLSStreamHandler
}

func (h *LLHLSStreamHandler) Name() string { return "ll-hls" }

// CanHandle returns true for H.264 and H.265 — LL-HLS is always available.
func (h *LLHLSStreamHandler) CanHandle(codec model.Format) bool {
	return codec == model.FormatH264 || codec == model.FormatH265
}

// --- WSStreamHandler ---

// WSStreamHandler implements StreamHandler for WebSocket streaming.
// WebSocket streams start/stop on-demand via GET /stream/ws, so StartStream
// and StopStream are no-ops. This registration exists purely for protocol
// discovery (so /api/cameras/{id}/protocols returns the correct list).
type WSStreamHandler struct{}

func (h *WSStreamHandler) Name() string { return "wasm" }

func (h *WSStreamHandler) CanHandle(codec model.Format) bool {
	return codec == model.FormatH264 || codec == model.FormatH265
}

func (h *WSStreamHandler) StartStream(camID string, rec model.Recorder, opts StreamStartOptions) error {
	return nil // WebSocket streams start on-demand via GET /stream/ws
}

func (h *WSStreamHandler) StopStream(camID string) error {
	return nil // WebSocket streams stop when client disconnects
}

// --- MJPEGStreamHandler ---

// MJPEGStreamHandler implements StreamHandler for live MJPEG streaming.
// MJPEG streams are proxied on-demand via GET /stream.mjpeg, so StartStream
// and StopStream are no-ops. This registration exists purely for protocol
// discovery (so /api/cameras/{id}/protocols returns the correct list).
type MJPEGStreamHandler struct{}

func (h *MJPEGStreamHandler) Name() string { return "mjpeg" }

func (h *MJPEGStreamHandler) CanHandle(codec model.Format) bool {
	return codec == model.FormatMJPEG || codec == model.EncJPEG
}

func (h *MJPEGStreamHandler) StartStream(camID string, rec model.Recorder, opts StreamStartOptions) error {
	return nil // MJPEG streams start on-demand via GET /stream.mjpeg
}

func (h *MJPEGStreamHandler) StopStream(camID string) error {
	return nil // MJPEG streams stop when client disconnects
}
