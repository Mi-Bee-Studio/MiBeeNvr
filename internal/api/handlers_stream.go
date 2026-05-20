package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/hls"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/go-chi/chi/v5"
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

// HandlersForCodec returns all handlers that can handle the given codec format.
func (r *StreamRegistry) HandlersForCodec(codec model.Format) []StreamHandler {
	var result []StreamHandler
	for _, h := range r.handlers {
		if h.CanHandle(codec) {
			result = append(result, h)
		}
	}
	return result
}

// ProtocolsForCodec returns the names of all protocols that support the given codec.
func (r *StreamRegistry) ProtocolsForCodec(codec model.Format) []string {
	var result []string
	for _, h := range r.handlers {
		if h.CanHandle(codec) {
			result = append(result, h.Name())
		}
	}
	return result
}

// Handler returns the stream handler by name, or nil if not found.
func (r *StreamRegistry) Handler(name string) StreamHandler {
	for _, h := range r.handlers {
		if h.Name() == name {
			return h
		}
	}
	return nil
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
		// Use deprecated SetOnHLSFrame for HLSProvider backward compat
		provider.SetOnHLSFrame(func(pts int64, au [][]byte) {
			_ = h.Mgr.WriteH264(camID, pts, au)
		})

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
		provider.SetOnHLSFrame(func(pts int64, au [][]byte) {
			_ = h.Mgr.WriteH265(camID, pts, au)
		})

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

// --- HTTP handler for /api/cameras/{id}/stream/* (HLS proxy) ---

// handleHLSStreamViaRegistry is the registry-based HLS stream handler.
// It replaces the type-switch spaghetti in handleHLSStream with a
// delegation to the HLSStreamHandler via the StreamRegistry.
func (h *Handler) handleHLSStreamViaRegistry(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if h.hlsMgr == nil || h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "HLS not available")
		return
	}

	cam, err := h.db.GetCamera(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get camera")
		return
	}
	if cam == nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}

	// If stream not active, start it via HLSStreamHandler
	if !h.hlsMgr.IsActive(id) {
		rec := h.camMgr.GetRecorder(id)
		if rec == nil {
			writeError(w, http.StatusBadRequest, "camera recorder not running")
			return
		}

		// Get camera config for HLS options
		camCfg := h.camMgr.GetCameraConfig(id)
		opts := StreamStartOptions{}
		if camCfg != nil {
			opts.MaxFPS = camCfg.HLSMaxFPS
			opts.SubStreamURL = camCfg.SubStreamURL
		}

		hlsHandler := &HLSStreamHandler{Mgr: h.hlsMgr}
		if err := hlsHandler.StartStream(id, rec, opts); err != nil {
			if errors.Is(err, hls.ErrMaxStreamsReached) {
				writeAPIError(w, http.StatusServiceUnavailable, &model.HLSMaxStreamsError{})
			} else if _, ok := err.(*model.HLSSupportedCodecError); ok {
				writeAPIError(w, http.StatusBadRequest, err)
			} else {
				streamLogger.Error("failed to start HLS stream", "camera_id", id, "error", err)
				writeError(w, http.StatusInternalServerError, "failed to start HLS stream")
			}
			return
		}
	}

	// Proxy to muxer handler
	if !h.hlsMgr.Handle(id, w, r) {
		writeError(w, http.StatusServiceUnavailable, "HLS stream not available")
	}
}

// handleStopHLSStreamViaRegistry stops the HLS stream via the HLSStreamHandler.
func (h *Handler) handleStopHLSStreamViaRegistry(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if h.hlsMgr == nil {
		writeError(w, http.StatusInternalServerError, "HLS not available")
		return
	}

	if !h.hlsMgr.IsActive(id) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "not active"})
		return
	}

	// Unsubscribe HLS consumer from StreamHub before stopping the stream
	var rec model.Recorder
	if h.camMgr != nil {
		rec = h.camMgr.GetRecorder(id)
	}

	hlsHandler := &HLSStreamHandler{Mgr: h.hlsMgr}
	_ = hlsHandler.StopStreamWithRecorder(id, rec)

	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

// SetStreamRegistry sets the stream registry on the handler for protocol queries.
func (h *Handler) SetStreamRegistry(reg *StreamRegistry) {
	h.streamRegistry = reg
}
