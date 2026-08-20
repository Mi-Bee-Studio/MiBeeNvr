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

// Cameras lists cameras eligible for cascade forwarding. MJPEG/JPEG cameras
// are excluded (no PS mux story); everything else is offered — GB28181
// cameras store encoding "" in config until their stream starts, so the
// forwarder sniffs the codec from the first NAL when the hint is empty.
func (s cascadeSource) Cameras() []cascade.CameraInfo {
	snap := s.cm.loadSnapshot()
	out := make([]cascade.CameraInfo, 0, len(snap.configs))
	for _, cfg := range snap.configs {
		if cfg.Encoding == "mjpeg" || cfg.Encoding == "jpeg" {
			continue
		}
		if cfg.Protocol == "timelapse" {
			continue
		}
		out = append(out, cascade.CameraInfo{
			ID:       cfg.ID,
			Name:     cfg.Name,
			Encoding: cfg.Encoding,
			// Catalog convergence: cascade_enabled=false hides the camera from
			// the upper platform entirely (catalog + INVITE gate).
			CascadeHidden: cfg.CascadeEnabled != nil && !*cfg.CascadeEnabled,
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
