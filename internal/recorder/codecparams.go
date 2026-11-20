package recorder

import (
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

// CodecParamsOf extracts codec parameters from a recorder. It prefers the
// model.HLSProvider interface (covering H264/H265/Ingest/Xiaomi recorders via
// a single atomic codec-snapshot read), then falls back to a concrete type
// switch for JPEG-family recorders which have no codec parameter sets.
//
// Extracted from internal/api when the streaming domain moved into its own
// package — the camera domain needs the same introspection for encoding
// probes, and a leaf-level home keeps the two domain packages from importing
// each other.
func CodecParamsOf(rec model.Recorder) (codec model.Format, sps, pps, vps []byte) {
	actualRec := model.UnwrapDelegate(rec)
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
	case *HTTPJPEGRecorder:
		// ESP32 MiBeeCam and other HTTP JPEG cameras. Live preview is served via
		// latest-frame polling (MjpegLivePlayer), advertised as the "mjpeg" protocol
		// by the MJPEG stream handler. Without this case, encoding probes return ""
		// and /protocols reports no protocols → frontend cannot live-preview.
		codec = model.EncJPEG
	case *MJPEGRecorder:
		codec = model.FormatMJPEG
	case *AudioRecorder:
		// Audio-only source devices: no video codec, no live-video protocols.
		// Leaving codec "" would make /protocols report nothing; the SPA then
		// treats the camera as video-less (correct) and live listening goes
		// through the WS audio path instead. Keep "" — the audio case exists
		// here to document the deliberate no-op.
		codec = ""
	}
	return
}

// HubOf extracts the StreamHub from a recorder via the GetHub() interface
// (implemented by every recorder type). Returns nil if the recorder doesn't
// implement it or the hub is not set.
func HubOf(rec model.Recorder) *streamhub.StreamHub {
	if h, ok := rec.(interface{ GetHub() *streamhub.StreamHub }); ok {
		return h.GetHub()
	}
	return nil
}
