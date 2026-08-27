package camera

import (
	"context"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181/cascade"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// cascadeSource adapts the CameraManager to the cascade client's
// CameraSource: local cameras (H.264/H.265 pull + GB28181 alike) become the
// aggregated catalog, and forwards subscribe to their stream hubs.
type cascadeSource struct {
	cm *CameraManager
	db *storage.DB
}

// NewCascadeSource builds the cascade view of the camera manager. The DB is
// the authority on which cameras are archived: archiving marks the DB row
// archived=1, but a partially-failed archive can leave the config entry in
// the YAML (the manager re-upserts the row at boot without touching the
// archived flag). The camera list API is DB-backed, so the catalog must gate
// on the same set — an archived camera must never be advertised to the upper
// platform as a playable channel.
func NewCascadeSource(cm *CameraManager, db *storage.DB) cascade.CameraSource {
	return cascadeSource{cm: cm, db: db}
}

// Cameras lists cameras eligible for cascade forwarding. MJPEG/JPEG cameras
// are excluded (no PS mux story); everything else is offered — GB28181
// cameras store encoding "" in config until their stream starts, so the
// forwarder sniffs the codec from the first NAL when the hint is empty.
func (s cascadeSource) Cameras() []cascade.CameraInfo {
	snap := s.cm.loadSnapshot()
	active := s.activeCameraIDs()
	out := make([]cascade.CameraInfo, 0, len(snap.configs))
	for _, cfg := range snap.configs {
		if cfg.Encoding == "mjpeg" || cfg.Encoding == "jpeg" {
			continue
		}
		if cfg.Protocol == "timelapse" {
			continue
		}
		if active != nil && !active[cfg.ID] {
			// Archived (or DB-less residue) camera — not listed by the API,
			// not advertised to the upper platform.
			continue
		}
		out = append(out, cascade.CameraInfo{
			ID:        cfg.ID,
			Name:      cfg.Name,
			Encoding:  cfg.Encoding,
			SubStream: cfg.CascadeSubStream,
			// Catalog convergence: cascade_enabled=false hides the camera from
			// the upper platform entirely (catalog + INVITE gate).
			CascadeHidden: cfg.CascadeEnabled != nil && !*cfg.CascadeEnabled,
			// Brand/Model live only in DB rows (config.CameraConfig has no
			// such fields); the catalog falls back to MiBee/MiBeeNvr.
		})
	}
	return out
}

// activeCameraIDs returns the set of non-archived camera IDs, or nil when the
// set cannot be determined (no DB / query error) — callers treat nil as "no
// filtering", preserving the pre-fix behavior instead of blanking the catalog.
func (s cascadeSource) activeCameraIDs() map[string]bool {
	if s.db == nil {
		return nil
	}
	rows, err := s.db.ListCameras(context.Background())
	if err != nil {
		return nil
	}
	set := make(map[string]bool, len(rows))
	for _, r := range rows {
		set[r.ID] = true
	}
	return set
}

// Hub returns the camera's stream hub (nil when the camera has no hub —
// answered 500 to the upper platform's INVITE).
func (s cascadeSource) Hub(cameraID string) *model.StreamHub {
	return s.cm.GetHub(cameraID)
}
