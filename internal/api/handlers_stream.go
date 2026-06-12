package api

import (
	"errors"
	"log/slog"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/hls"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/xiaomi"
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
	MaxFPS      int
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
type ProtocolDetail struct {
	Protocol  string
	Available bool
	Reason    string
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
	hub *model.StreamHub,
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
func (h *HLSStreamHandler) subscribeHub(camID string, hub *model.StreamHub, isH265 bool, opts StreamStartOptions) {
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

var streamLogger = slog.Default().With("component", "stream-handler")

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
// Uses HLSProvider interface first, then falls back to concrete type access.
func getCodecParams(rec model.Recorder) (codec model.Format, sps, pps, vps []byte) {
	if provider, ok := rec.(model.HLSProvider); ok {
		codec, sps, pps, vps = provider.CodecParams()
		if sps != nil && pps != nil {
			return
		}
		// HLSProvider returned empty params — fall through to concrete type switch
	}

	actualRec := unwrapDelegate(rec)
	switch r := actualRec.(type) {
	case *recorder.H264Recorder:
		codec = model.FormatH264
		sps = r.SPS()
		pps = r.PPS()
	case *recorder.H265Recorder:
		codec = model.FormatH265
		vps = r.VPS()
		sps = r.SPS()
		pps = r.PPS()
	}
	return
}

// getStreamHub extracts the StreamHub from a recorder.
// Returns nil if the recorder doesn't have a Hub or it's not set.
func getStreamHub(rec model.Recorder) *model.StreamHub {
	if h, ok := rec.(interface{ GetHub() *model.StreamHub }); ok {
		return h.GetHub()
	}
	actualRec := unwrapDelegate(rec)
	switch r := actualRec.(type) {
	case *recorder.H264Recorder:
		return r.Hub
	case *recorder.H265Recorder:
		return r.Hub
	case *recorder.MJPEGRecorder:
		return r.Hub
	case *recorder.HTTPJPEGRecorder:
		return r.Hub
	case *recorder.ONVIFRecorder:
		return r.Hub
	case *xiaomi.XiaomiRecorder:
		return r.Hub
	}
	return nil
}

// --- HTTP handler for /api/cameras/{id}/stream/* (HLS proxy) ---



// SetStreamRegistry sets the stream registry on the handler for protocol queries.
func (h *Handler) SetStreamRegistry(reg *StreamRegistry) {
	h.streamRegistry = reg
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
// When low-latency is disabled, it appears as unavailable with a reason.
type LLHLSStreamHandler struct {
	HLSStreamHandler
	LowLatencyEnabled bool
}

func (h *LLHLSStreamHandler) Name() string { return "ll-hls" }

// CanHandle returns true only when low-latency is actually enabled.
// When disabled, the ConditionalHandler interface provides the "greyed out" UX.
func (h *LLHLSStreamHandler) CanHandle(codec model.Format) bool {
	return h.LowLatencyEnabled && (codec == model.FormatH264 || codec == model.FormatH265)
}

// SupportedCodec returns true for codecs that LL-HLS would support if enabled.
func (h *LLHLSStreamHandler) SupportedCodec(codec model.Format) bool {
	return codec == model.FormatH264 || codec == model.FormatH265
}

// UnavailabilityReason returns why LL-HLS is not available.
func (h *LLHLSStreamHandler) UnavailabilityReason(_ model.Format) string {
	return "Enable low-latency HLS in Settings"
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
