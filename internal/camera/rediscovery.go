package camera

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/rediscovery"
)

// ensureStableID fetches the ONVIF device serial number and persists it as the
// camera's StableID (the hardware-level identity used for IP self-healing).
// Best-effort: skips silently on any error (no serial, device unreachable,
// minimal ONVIF devices that reject the call). No-op if StableID is already set.
func (cm *CameraManager) ensureStableID(cameraID string) {
	cm.mu.Lock()
	var cam *config.CameraConfig
	for i := range cm.cfg.Cameras {
		if cm.cfg.Cameras[i].ID == cameraID {
			cam = &cm.cfg.Cameras[i]
			break
		}
	}
	// Re-check under lock in case another goroutine filled it already.
	if cam == nil || strings.TrimSpace(cam.StableID) != "" {
		cm.mu.Unlock()
		return
	}
	cm.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	info := cm.GetCachedDeviceInfo(ctx, cameraID)
	if info == nil || strings.TrimSpace(info.SerialNumber) == "" {
		logger.Debug("could not auto-populate stable_id (no serial number)", "camera_id", cameraID)
		return
	}

	cm.mu.Lock()
	// Locate again under the write lock (slice may have been mutated).
	for i := range cm.cfg.Cameras {
		if cm.cfg.Cameras[i].ID == cameraID {
			if strings.TrimSpace(cm.cfg.Cameras[i].StableID) == "" {
				cm.cfg.Cameras[i].StableID = info.SerialNumber
				logger.Info("auto-populated stable_id for camera", "camera_id", cameraID, "stable_id", info.SerialNumber)
			}
			break
		}
	}
	cm.mu.Unlock()

	if err := cm.persistConfig(); err != nil {
		logger.Warn("failed to persist auto-populated stable_id", "camera_id", cameraID, "error", err)
	}
}

// RediscoverAndReconnect attempts to relocate a camera whose IP has changed by
// scanning candidate subnets for a device whose ONVIF serial number matches the
// camera's StableID, then updates the config and restarts the recorder.
//
// This is the IP self-healing entry point. It is invoked automatically when a
// camera is blacklisted by health auto-remediation (persistent failure), and is
// also exposed via the manual POST /api/cameras/{id}/rediscover endpoint.
//
// Returns:
//   - (found=true, nil) when the camera was relocated and reconnection started.
//   - (found=false, nil) when the protocol is unsupported or no StableID, or the
//     device was not found in the candidate set (NOT an error — camera may simply
//     be offline).
//   - (found=false, err) on a hard failure (config persistence, restart).
func (cm *CameraManager) RediscoverAndReconnect(ctx context.Context, cameraID string) (found bool, err error) {
	// Snapshot the camera config under the lock, then release it for the (slow)
	// network scan. The recorder is updated afterwards under a fresh lock.
	cm.mu.Lock()
	var cam config.CameraConfig
	for i := range cm.cfg.Cameras {
		if cm.cfg.Cameras[i].ID == cameraID {
			cam = cm.cfg.Cameras[i]
			break
		}
	}
	cm.mu.Unlock()

	if cam.ID == "" {
		return false, &model.CameraNotFoundError{CameraID: cameraID}
	}

	// Only ONVIF cameras support re-discovery today (see internal/rediscovery).
	proto := strings.ToLower(strings.TrimSpace(cam.Protocol))
	if proto != "onvif" {
		logger.Debug("rediscovery skipped: non-ONVIF protocol", "camera_id", cameraID, "protocol", cam.Protocol)
		return false, nil
	}
	if strings.TrimSpace(cam.StableID) == "" {
		logger.Debug("rediscovery skipped: camera has no stable_id", "camera_id", cameraID)
		return false, nil
	}

	eng := rediscovery.NewEngine(rediscovery.FromConfig(cm.cfg.Health.Rediscovery), nil)
	result, rerr := eng.DiscoverByStableID(ctx, cam)
	if rerr != nil {
		logger.Info("rediscovery did not relocate camera", "camera_id", cameraID, "stable_id", cam.StableID, "reason", rerr)
		return false, nil
	}

	logger.Info("rediscovery located camera at new address",
		"camera_id", cameraID, "stable_id", cam.StableID,
		"old_endpoint", cam.ONVIFEndpoint, "new_endpoint", result.NewEndpoint)

	// Apply the new endpoint under the lock + persist + restart.
	cm.mu.Lock()
	idx := -1
	for i := range cm.cfg.Cameras {
		if cm.cfg.Cameras[i].ID == cameraID {
			idx = i
			break
		}
	}
	if idx < 0 {
		cm.mu.Unlock()
		return false, &model.CameraNotFoundError{CameraID: cameraID}
	}
	savedCam := cm.cfg.Cameras[idx]
	cm.cfg.Cameras[idx].ONVIFEndpoint = result.NewEndpoint
	cm.cfg.Cameras[idx].URL = result.NewEndpoint
	// The cached ONVIF client holds the OLD endpoint — evict it so the next
	// reuseOrCreateONVIFClient rebuilds against the new address.
	cm.CloseONVIFClient(cameraID)
	if cm.db != nil {
		c := cm.cfg.Cameras[idx]
		if uerr := cm.db.UpsertCamera(ctx, c.ID, c.Name, string(c.Protocol), c.Encoding, c.URL, c.Username, c.Password, c.ONVIFEndpoint, c.ProfileToken, c.StreamEncoding); uerr != nil {
			logger.Error("failed to upsert camera after rediscovery", "camera_id", cameraID, "error", uerr)
		}
	}
	if perr := cm.persistConfig(); perr != nil {
		// Rollback the in-memory change so config and disk stay consistent.
		cm.cfg.Cameras[idx] = savedCam
		cm.mu.Unlock()
		return false, fmt.Errorf("rediscovery: failed to persist config: %w", perr)
	}

	// Record reconnect attempt metric.
	if cm.metrics != nil {
		cm.metrics.CameraReconnectAttemptsTotal.WithLabelValues(cameraID).Inc()
	}

	segDur, perr := time.ParseDuration(cm.cfg.Storage.SegmentDuration)
	if perr != nil {
		segDur = recorder.DefaultSegmentDur
	}
	rerr = cm.startRecorder(ctx, cm.cfg.Cameras[idx], segDur)
	cm.mu.Unlock()
	if rerr != nil {
		return false, fmt.Errorf("rediscovery: failed to restart recorder: %w", rerr)
	}
	return true, nil
}
