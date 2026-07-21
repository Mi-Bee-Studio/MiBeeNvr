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
	cm.configMu.Lock()
	var cam *config.CameraConfig
	for i := range cm.cfg.Cameras {
		if cm.cfg.Cameras[i].ID == cameraID {
			cam = &cm.cfg.Cameras[i]
			break
		}
	}
	// Re-check under lock in case another goroutine filled it already.
	if cam == nil || strings.TrimSpace(cam.StableID) != "" {
		cm.configMu.Unlock()
		return
	}
	cm.configMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	info := cm.GetCachedDeviceInfo(ctx, cameraID)
	if info == nil || strings.TrimSpace(info.SerialNumber) == "" {
		logger.Debug("could not auto-populate stable_id (no serial number)", "camera_id", cameraID)
		return
	}

	cm.configMu.Lock()
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
	cm.configMu.Unlock()

	if err := cm.persistConfig(); err != nil {
		logger.Warn("failed to persist auto-populated stable_id", "camera_id", cameraID, "error", err)
	}

	// Persist stable_id to DB in addition to YAML. DB write is best-effort:
	// YAML is the source of truth, DB is a fast-lookup cache (used by IP
	// self-healing / rediscovery). Never return or panic on DB failure.
	if cm.db != nil {
		if err := cm.db.UpdateCameraStableID(ctx, cameraID, info.SerialNumber); err != nil {
			logger.Warn("failed to persist stable_id to db", "camera_id", cameraID, "stable_id", info.SerialNumber, "error", err)
		}
	}
}

// backfillStableIDs runs at startup (in a goroutine) to backfill DB stable_id
// from YAML config. It handles two cases:
//
//  1. YAML has stable_id but DB stable_id is empty — backfill DB from YAML.
//  2. ONVIF YAML has no stable_id — attempt ONVIF GetCachedDeviceInfo to
//     discover and persist serial number to both YAML and DB.
//
// Non-ONVIF cameras without a stable_id are skipped (stable_id is only
// meaningful for ONVIF IP self-healing). Must NOT block Start().
func (cm *CameraManager) backfillStableIDs(ctx context.Context) {
	if cm.db == nil {
		return
	}

	// Snapshot the camera list under configMu so concurrent RemoveCamera/
	// UpdateCamera writes (which reslice cm.cfg.Cameras under the same lock)
	// don't race with our read. The rest of the loop does no cfg.Cameras
	// reads except the YAML-write path, which re-locks and re-reads safely.
	cm.configMu.Lock()
	cameras := make([]config.CameraConfig, len(cm.cfg.Cameras))
	copy(cameras, cm.cfg.Cameras)
	cm.configMu.Unlock()

	for i := range cameras {
		// Bail out promptly when the start context is cancelled (process shutdown
		// or test teardown). Without this check, the loop continues issuing db
		// calls after the caller has moved on, racing with db.Close() and crashing
		// with "sql: database is closed".
		if ctx.Err() != nil {
			return
		}
		cam := cameras[i]
		yamlStableID := strings.TrimSpace(cam.StableID)

		if yamlStableID != "" {
			// YAML has stable_id — backfill DB if empty (e.g. cameras added
			// before the stable_id column migration, or restored from backup).
			dbStableID, err := cm.db.GetCameraStableID(ctx, cam.ID)
			if err != nil {
				logger.Warn("backfill: failed to read db stable_id", "camera_id", cam.ID, "error", err)
				continue
			}
			if strings.TrimSpace(dbStableID) == "" {
				if err := cm.db.UpdateCameraStableID(ctx, cam.ID, yamlStableID); err != nil {
					logger.Warn("backfill: failed to write stable_id to db", "camera_id", cam.ID, "stable_id", yamlStableID, "error", err)
				} else {
					logger.Info("backfill: wrote stable_id to db from yaml", "camera_id", cam.ID, "stable_id", yamlStableID)
				}
			}
		} else if strings.EqualFold(string(cam.Protocol), "onvif") {
			// YAML has no stable_id for an ONVIF camera — try to discover it.
			info := cm.GetCachedDeviceInfo(ctx, cam.ID)
			if info != nil && strings.TrimSpace(info.SerialNumber) != "" {
				// Write to YAML config (source of truth).
				cm.configMu.Lock()
				for j := range cm.cfg.Cameras {
					if cm.cfg.Cameras[j].ID == cam.ID {
						if strings.TrimSpace(cm.cfg.Cameras[j].StableID) == "" {
							cm.cfg.Cameras[j].StableID = info.SerialNumber
						}
						break
					}
				}
				cm.configMu.Unlock()

				if err := cm.persistConfig(); err != nil {
					logger.Warn("backfill: failed to persist yaml stable_id", "camera_id", cam.ID, "error", err)
				}

				// Write to DB (best-effort).
				if cm.db != nil {
					if err := cm.db.UpdateCameraStableID(ctx, cam.ID, info.SerialNumber); err != nil {
						logger.Warn("backfill: failed to persist stable_id to db", "camera_id", cam.ID, "error", err)
					}
				}
			} else {
				logger.Debug("backfill: could not discover stable_id for onvif camera", "camera_id", cam.ID)
			}
		}
	}
}

// ensureProfileToken persists the profile token that the ONVIF recorder
// auto-selected during Start (via onvif.SelectMainProfile). Without this, every
// NVR restart re-runs GetProfiles to re-select — a redundant round-trip that
// strains the ESP32 MiBeeCam's limited HTTP connection pool.
//
// Best-effort and non-blocking: waits up to 15s for the recorder to come online
// (Start is async), reads the resolved token, and persists it to config if the
// camera didn't already have one. No-op if the token is already set or can't be
// resolved.
func (cm *CameraManager) ensureProfileToken(cameraID string) {
	// Wait for the recorder to come online so ProfileToken is resolved.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return // recorder didn't come online in time — skip silently
		case <-ticker.C:
		}
		rec := cm.GetRecorder(cameraID)
		if rec == nil {
			continue
		}
		if rec.Status() != model.StatusRecording {
			continue // still connecting
		}
		onvifRec, ok := rec.(*recorder.ONVIFRecorder)
		if !ok {
			return // not an ONVIF recorder
		}
		token := onvifRec.ResolvedProfileToken()
		if token == "" {
			return // nothing resolved
		}
		// Check if the config already has this token — avoid unnecessary writes.
		cm.configMu.Lock()
		alreadySet := false
		for i := range cm.cfg.Cameras {
			if cm.cfg.Cameras[i].ID == cameraID {
				if cm.cfg.Cameras[i].ProfileToken == token {
					alreadySet = true
				}
				break
			}
		}
		if alreadySet {
			cm.configMu.Unlock()
			return
		}
		// Persist the resolved token.
		for i := range cm.cfg.Cameras {
			if cm.cfg.Cameras[i].ID == cameraID {
				cm.cfg.Cameras[i].ProfileToken = token
				break
			}
		}
		cm.configMu.Unlock()
		if err := cm.persistConfig(); err != nil {
			logger.Warn("failed to persist auto-selected profile_token", "camera_id", cameraID, "error", err)
		} else {
			logger.Info("auto-persisted profile_token for camera", "camera_id", cameraID, "profile_token", token)
		}
		return
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
	cm.configMu.Lock()
	var cam config.CameraConfig
	for i := range cm.cfg.Cameras {
		if cm.cfg.Cameras[i].ID == cameraID {
			cam = cm.cfg.Cameras[i]
			break
		}
	}
	cm.configMu.Unlock()

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
	cm.configMu.Lock()
	idx := -1
	for i := range cm.cfg.Cameras {
		if cm.cfg.Cameras[i].ID == cameraID {
			idx = i
			break
		}
	}
	if idx < 0 {
		cm.configMu.Unlock()
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
		if uerr := cm.db.UpsertCamera(ctx, c.ID, c.Name, string(c.Protocol), c.Encoding, c.URL, c.Username, c.Password, c.ONVIFEndpoint, c.ProfileToken, c.StreamEncoding, c.StableID); uerr != nil {
			logger.Error("failed to upsert camera after rediscovery", "camera_id", cameraID, "error", uerr)
		}
	}
	if perr := cm.persistConfig(); perr != nil {
		// Rollback the in-memory change so config and disk stay consistent.
		cm.cfg.Cameras[idx] = savedCam
		cm.configMu.Unlock()
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
	// Snapshot config + segDur, then release configMu before startRecorder
	// (startRecorder registers via apply and runs lock-free).
	camCopy := cm.cfg.Cameras[idx]
	cm.configMu.Unlock()
	rerr = cm.startRecorder(ctx, camCopy, segDur)
	if rerr != nil {
		return false, fmt.Errorf("rediscovery: failed to restart recorder: %w", rerr)
	}
	return true, nil
}
