package app

import (
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/rtsp"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

// rtspStreamProvider adapts the camera manager to the RTSP output server's
// StreamProvider (#522): resolves the camera's CURRENT recorder — restarted
// recorders own a fresh hub, and the RTSP server re-resolves on every
// DESCRIBE/SETUP — and reads its codec parameters. Mirrors the api package's
// getCodecParams/getStreamHub (ONVIF delegates unwrap to the concrete recorder
// that owns the params; the outer recorder still carries the live hub).
func rtspStreamProvider(camMgr *camera.CameraManager) rtsp.StreamProvider {
	return func(cameraID string) (rtsp.StreamInfo, bool) {
		rec := camMgr.GetRecorder(cameraID)
		if rec == nil {
			return rtsp.StreamInfo{}, false
		}
		var info rtsp.StreamInfo
		if provider, ok := unwrapRTSPDelegate(rec).(model.HLSProvider); ok {
			info.Codec, info.SPS, info.PPS, info.VPS = provider.CodecParams()
		}
		if h, ok := rec.(interface{ GetHub() *streamhub.StreamHub }); ok {
			info.Hub = h.GetHub()
		}
		if info.Codec == "" || info.Hub == nil {
			return rtsp.StreamInfo{}, false
		}
		return info, true
	}
}

// unwrapRTSPDelegate unwraps delegate layers (e.g. ONVIF → H264/H265) to the
// recorder that implements model.HLSProvider. Same shape as the api package's
// unwrapDelegate, kept local to avoid an app→api dependency.
func unwrapRTSPDelegate(rec model.Recorder) model.Recorder {
	for {
		u, ok := rec.(interface{ Delegate() model.Recorder })
		if !ok {
			return rec
		}
		if d := u.Delegate(); d != nil {
			rec = d
			continue
		}
		return rec
	}
}
