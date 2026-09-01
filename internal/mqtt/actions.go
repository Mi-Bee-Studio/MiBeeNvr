package mqtt

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// CameraLifecycle is the camera-manager surface the MQTT trigger dispatcher
// needs. Satisfied by *camera.CameraManager; narrow interface keeps the
// dispatcher testable without the full manager.
type CameraLifecycle interface {
	StartCamera(ctx context.Context, cameraID string) error
	StopCamera(ctx context.Context, cameraID string) error
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

var actionLogger = slog.Default().With("component", "mqtt-trigger")

// NewActionDispatcher maps MQTT trigger actions to camera lifecycle
// operations and returns the onAction callback for NewClient. Each action
// runs on its own goroutine: the paho message handler must never block
// (internal/mqtt anti-pattern), and StartCamera dials the camera.
func NewActionDispatcher(lifecycle CameraLifecycle) func(cameraID, action string) {
	return func(cameraID, action string) {
		if action != actionRecord && action != actionStop {
			switch action {
			case actionSnapshot:
				// TODO(#656): capture a frame and persist it to storage.
				actionLogger.Warn("mqtt snapshot trigger is not implemented yet", "camera_id", cameraID)
			default:
				actionLogger.Warn("unknown mqtt trigger action ignored", "camera_id", cameraID, "action", action)
			}
			return
		}
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
	}
}
