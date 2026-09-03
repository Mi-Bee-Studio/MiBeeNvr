package mqtt

import (
	"context"
	"errors"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// CameraLifecycle is the camera-manager surface the MQTT trigger dispatcher
// needs. Satisfied by *camera.CameraManager; narrow interface keeps the
// dispatcher testable without the full manager.
type CameraLifecycle interface {
	StartCamera(ctx context.Context, cameraID string) error
	StopCamera(ctx context.Context, cameraID string) error
}

// SnapshotRunner captures a frame, persists it under the storage root, and
// publishes a camera.snapshot event. Satisfied by *snapshot.Runner (wired in
// pkg/app); narrow interface keeps the dispatcher testable with a stub.
type SnapshotRunner interface {
	RunSnapshot(ctx context.Context, cameraID string, trigger string) (string, error)
}

// Trigger actions accepted on {prefix}/trigger/{camera_id}.
const (
	actionRecord   = "record"
	actionStop     = "stop"
	actionSnapshot = "snapshot"
)

// actionTimeout bounds one triggered lifecycle call (camera dial + recorder
// start can legitimately take seconds; 30s leaves room for slow devices).
const actionTimeout = 30 * time.Second

var actionLogger = slogx.Component("mqtt-trigger")

// NewActionDispatcher maps MQTT trigger actions to camera lifecycle
// operations and returns the onAction callback for NewClient. Each action
// runs on its own goroutine: the paho message handler must never block
// (internal/mqtt anti-pattern), and StartCamera dials the camera. The
// snapshot action runs the SnapshotRunner (capture → persist → event, #656).
func NewActionDispatcher(lifecycle CameraLifecycle, snap SnapshotRunner) func(cameraID, action string) {
	return func(cameraID, action string) {
		switch action {
		case actionRecord, actionStop:
			if lifecycle == nil {
				actionLogger.Error("mqtt trigger dropped: no camera lifecycle wired", "camera_id", cameraID, "action", action)
				return
			}
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
				defer cancel()

				var err error
				if action == actionRecord {
					err = lifecycle.StartCamera(ctx, cameraID)
				} else {
					err = lifecycle.StopCamera(ctx, cameraID)
				}
				if err != nil {
					var running *model.CameraAlreadyRunningError
					if errors.As(err, &running) {
						actionLogger.Info("mqtt trigger: camera already recording", "camera_id", cameraID, "action", action)
						return
					}
					actionLogger.Warn("mqtt trigger action failed", "camera_id", cameraID, "action", action, "error", err)
					return
				}
				actionLogger.Info("mqtt trigger applied", "camera_id", cameraID, "action", action)
			}()
		case actionSnapshot:
			if snap == nil {
				actionLogger.Warn("mqtt snapshot trigger dropped: no snapshot runner wired", "camera_id", cameraID)
				return
			}
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
				defer cancel()

				path, err := snap.RunSnapshot(ctx, cameraID, "mqtt")
				if err != nil {
					actionLogger.Warn("mqtt snapshot trigger failed", "camera_id", cameraID, "error", err)
					return
				}
				actionLogger.Info("mqtt snapshot captured", "camera_id", cameraID, "path", path)
			}()
		default:
			actionLogger.Warn("unknown mqtt trigger action ignored", "camera_id", cameraID, "action", action)
		}
	}
}
