package camera

import (
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	pkgcamera "github.com/Mi-Bee-Studio/MiBeeNvr/pkg/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/pkg/streamhub"
)

// Compile-time assertions.
//
// *CameraManager satisfies pkg/app.Service via Name + Start + Stop.
// *publicAdapter (not *CameraManager) satisfies pkg/camera.Manager —
// the wrapper is required because pkg/camera.Manager.Status(id) collides
// in name with the legacy CameraManager.Status() (no args, returns map).
var (
	_ interface{ Name() string } = (*CameraManager)(nil)
	_ pkgcamera.Manager          = (*publicAdapter)(nil)
)

// Name returns the service identifier used by pkg/app.App.
// *CameraManager satisfies pkg/app.Service via Name + Start + Stop.
func (cm *CameraManager) Name() string { return "camera" }

// AsPublic returns a read-only wrapper that satisfies pkg/camera.Manager.
//
// The wrapper exists because pkg/camera.Manager.Status(id) collides in
// name with the existing CameraManager.Status() (which returns the
// status map for all cameras). Rather than rename the legacy method,
// we expose a separate adapter for out-of-module consumers.
//
// Register with pkg/app.App for typed retrieval:
//
//	camMgr := camera.NewCameraManager(...)
//	a.Register(camMgr)                       // lifecycle (Start/Stop)
//	a.RegisterValue("camera-manager", camMgr.AsPublic())  // pkg/camera.Manager
func (cm *CameraManager) AsPublic() pkgcamera.Manager {
	return &publicAdapter{cm: cm}
}

// publicAdapter wraps *CameraManager to satisfy pkg/camera.Manager.
type publicAdapter struct {
	cm *CameraManager
}

// List implements pkg/camera.Manager.
func (a *publicAdapter) List() []pkgcamera.Camera {
	a.cm.mu.RLock()
	defer a.cm.mu.RUnlock()
	out := make([]pkgcamera.Camera, 0, len(a.cm.cfg.Cameras))
	for i := range a.cm.cfg.Cameras {
		out = append(out, cameraView{cfg: &a.cm.cfg.Cameras[i]})
	}
	return out
}

// Get implements pkg/camera.Manager.
func (a *publicAdapter) Get(id string) (pkgcamera.Camera, error) {
	a.cm.mu.RLock()
	defer a.cm.mu.RUnlock()
	for i := range a.cm.cfg.Cameras {
		if a.cm.cfg.Cameras[i].ID == id {
			return cameraView{cfg: &a.cm.cfg.Cameras[i]}, nil
		}
	}
	return nil, pkgcamera.NewNotFoundError(id)
}

// Status implements pkg/camera.Manager.
//
// Runtime status is derived from the recorder's current state plus any
// error detail recorded for the camera. FPS and BitrateKbps are left
// zero in this v0.1 adapter; wiring them to metrics is deferred.
func (a *publicAdapter) Status(id string) (pkgcamera.Status, error) {
	cfg := a.cm.GetCameraConfig(id)
	if cfg == nil {
		return pkgcamera.Status{}, pkgcamera.NewNotFoundError(id)
	}

	rs := a.cm.CameraStatus(id)
	s := pkgcamera.Status{
		ID:        id,
		Online:    rs == model.StatusRecording || rs == model.StatusReconnecting,
		Recording: rs == model.StatusRecording,
	}
	if detail := a.cm.GetErrorDetail(id); detail != nil {
		s.Error = detail.Message
	}
	return s, nil
}

// Hub implements pkg/camera.Manager.
//
// Returns the frame distribution hub for the camera. The returned Hub
// is shared across all callers; each must Subscribe under a unique
// consumerID. The underlying *model.StreamHub is wrapped via
// model.NewHubAdapter to satisfy the pkg/streamhub.Hub interface.
func (a *publicAdapter) Hub(id string) (streamhub.Hub, error) {
	h := a.cm.GetHub(id)
	if h == nil {
		return nil, pkgcamera.NewNotFoundError(id)
	}
	return model.NewHubAdapter(h), nil
}

// cameraView adapts a *config.CameraConfig to the pkg/camera.Camera
// interface. It is a thin read-only view; mutations to the underlying
// config are visible through the adapter.
type cameraView struct {
	cfg *config.CameraConfig
}

func (c cameraView) ID() string         { return c.cfg.ID }
func (c cameraView) Name() string       { return c.cfg.Name }
func (c cameraView) Protocol() string   { return c.cfg.Protocol }
func (c cameraView) Encoding() string   { return c.cfg.Encoding }
func (c cameraView) AudioEnabled() bool { return c.cfg.AudioEnabled }
