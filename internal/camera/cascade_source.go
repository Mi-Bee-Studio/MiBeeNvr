package camera

import (
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181/cascade"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// cascadeSource adapts the CameraManager to the cascade client's
// CameraSource: local cameras (H.264/H.265 pull + GB28181 alike) become the
// aggregated catalog, and forwards subscribe to their stream hubs.
type cascadeSource struct {
	cm *CameraManager
}

// NewCascadeSource builds the cascade view of the camera manager.
func NewCascadeSource(cm *CameraManager) cascade.CameraSource { return cascadeSource{cm: cm} }

// Cameras lists cameras eligible for cascade forwarding: H.264/H.265
// encodings with a live hub (the PS muxer has no MJPEG story). Encoding is
// resolved from the config with the live recorder's backfill as fallback —
// GB28181 cameras store "" until their stream starts, and the muxer sniffs
// the codec from the first NAL anyway, so an empty encoding only excludes a
// camera that has never streamed.
func (s cascadeSource) Cameras() []cascade.CameraInfo {
	snap := s.cm.loadSnapshot()
	out := make([]cascade.CameraInfo, 0, len(snap.configs))
	for _, cfg := range snap.configs {
		switch cfg.Encoding {
		case "h264", "h265":
		default:
			continue
		}
		out = append(out, cascade.CameraInfo{
			ID:       cfg.ID,
			Name:     cfg.Name,
			Encoding: cfg.Encoding,
			// Brand/Model live only in DB rows (config.CameraConfig has no
			// such fields); the catalog falls back to MiBee/MiBeeNvr.
		})
	}
	return out
}

// Hub returns the camera's stream hub (nil when the camera has no hub —
// answered 500 to the upper platform's INVITE).
func (s cascadeSource) Hub(cameraID string) *model.StreamHub {
	return s.cm.GetHub(cameraID)
}
