package camera

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// CameraUpdate holds optional fields for updating a camera.
// Only non-nil fields will be applied.
type CameraUpdate struct {
	Name     *string
	URL      *string
	Protocol *string
	Username *string
	Password *string
	Enabled  *bool
}

type CameraManager struct {
	cfg        *config.Config
	store      *storage.Manager
	db         *storage.DB
	configPath string
	recorders  map[string]model.Recorder // camera_id → Recorder
	mu         sync.RWMutex
}

// NewCameraManager creates a new CameraManager.
func NewCameraManager(cfg *config.Config, store *storage.Manager, db *storage.DB, configPath string) *CameraManager {
	return &CameraManager{
		cfg:        cfg,
		store:      store,
		db:         db,
		configPath: configPath,
		recorders:  make(map[string]model.Recorder),
	}
}

// createRecorder creates a recorder for the given camera config.
// Returns nil for protocols that don't support recording (http_jpeg, unknown).
func (cm *CameraManager) createRecorder(cam config.CameraConfig, segDur time.Duration) model.Recorder {
	switch cam.Protocol {
	case string(model.ProtoRTSPH264):
		h264Cfg := recorder.H264Config{
			CameraID:   cam.ID,
			RTSPURL:    cam.URL,
			SegmentDur: segDur,
			DB:         cm.db,
		}
		return recorder.NewH264Recorder(h264Cfg, cm.store)
	case string(model.ProtoRTSPMJPEG):
		mjpegCfg := recorder.MJPEGConfig{
			CameraID:   cam.ID,
			RTSPURL:    cam.URL,
			SegmentDur: segDur,
		}
		return recorder.NewMJPEGRecorder(mjpegCfg, cm.store)
	default:
		return nil
	}
}

// startRecorder creates and starts a recorder for the given camera.
// The caller must hold cm.mu (or at least a write lock) if cm.recorders is being modified.
// If the recorder is created, it will be registered in cm.recorders.
func (cm *CameraManager) startRecorder(ctx context.Context, cam config.CameraConfig, segDur time.Duration) error {
	rec := cm.createRecorder(cam, segDur)
	if rec == nil {
		return fmt.Errorf("camera %q: protocol %q does not support recording", cam.ID, cam.Protocol)
	}
	cm.recorders[cam.ID] = rec
	if err := rec.Start(ctx); err != nil {
		delete(cm.recorders, cam.ID)
		return fmt.Errorf("camera %q: failed to start recorder: %w", cam.ID, err)
	}
	log.Printf("[camera-manager] started recorder for camera %q", cam.ID)
	return nil
}

// persistConfig saves the current config to disk if configPath is set.
func (cm *CameraManager) persistConfig() error {
	if cm.configPath != "" {
		if err := config.Save(cm.configPath, cm.cfg); err != nil {
			return fmt.Errorf("camera manager: failed to save config: %w", err)
		}
	}
	return nil
}

// Start creates and starts recorders for all enabled cameras in the config.
// If a single camera fails to start, it logs the error and continues with the rest.
func (cm *CameraManager) Start(ctx context.Context) error {
	segDur, err := time.ParseDuration(cm.cfg.Storage.SegmentDuration)
	if err != nil {
		return fmt.Errorf("camera manager: invalid segment duration %q: %w", cm.cfg.Storage.SegmentDuration, err)
	}

	for _, cam := range cm.cfg.Cameras {
		// Insert camera record into database
		if err := cm.db.UpsertCamera(ctx, cam.ID, cam.Name, string(cam.Protocol), cam.URL, cam.Username, cam.Password, cam.Enabled); err != nil {
			log.Printf("[camera-manager] failed to insert camera record for %q: %v", cam.ID, err)
		} else {
			log.Printf("[camera-manager] inserted camera record for %q", cam.ID)
		}

		if !cam.Enabled {
			log.Printf("[camera-manager] camera %q (%s) is disabled, skipping", cam.ID, cam.Protocol)
			continue
		}

		switch cam.Protocol {
		case string(model.ProtoRTSPH264):
			rec := cm.createRecorder(cam, segDur)
			if rec != nil {
				cm.mu.Lock()
				cm.recorders[cam.ID] = rec
				cm.mu.Unlock()
				if err := rec.Start(ctx); err != nil {
					log.Printf("[camera-manager] failed to start H264 recorder for %q: %v", cam.ID, err)
				} else {
					log.Printf("[camera-manager] started H264 recorder for camera %q", cam.ID)
				}
			}

		case string(model.ProtoRTSPMJPEG):
			rec := cm.createRecorder(cam, segDur)
			if rec != nil {
				cm.mu.Lock()
				cm.recorders[cam.ID] = rec
				cm.mu.Unlock()
				if err := rec.Start(ctx); err != nil {
					log.Printf("[camera-manager] failed to start MJPEG recorder for %q: %v", cam.ID, err)
				} else {
					log.Printf("[camera-manager] started MJPEG recorder for camera %q", cam.ID)
				}
			}

		case string(model.ProtoHTTPJPEG):
			log.Printf("[camera-manager] camera %q uses http_jpeg protocol, skipping (handled by upload handler)", cam.ID)

		default:
			log.Printf("[camera-manager] camera %q has unknown protocol %q, skipping", cam.ID, cam.Protocol)
		}
	}

	return nil
}

// Stop stops all running recorders and waits for them to complete.
func (cm *CameraManager) Stop() error {
	cm.mu.RLock()
	recs := make([]model.Recorder, 0, len(cm.recorders))
	for _, rec := range cm.recorders {
		recs = append(recs, rec)
	}
	cm.mu.RUnlock()

	var errs []error
	for _, rec := range recs {
		if err := rec.Stop(); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("camera manager: %d recorder(s) failed to stop", len(errs))
	}
	return nil
}

// Status returns the status of all managed recorders.
func (cm *CameraManager) Status() map[string]model.RecorderStatus {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	result := make(map[string]model.RecorderStatus, len(cm.recorders))
	for id, rec := range cm.recorders {
		result[id] = rec.Status()
	}
	return result
}

// CameraStatus returns the status of a single camera recorder.
func (cm *CameraManager) CameraStatus(cameraID string) model.RecorderStatus {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	rec, ok := cm.recorders[cameraID]
	if !ok {
		return model.StatusError
	}
	return rec.Status()
}

// RecorderCount returns the number of managed recorders.
func (cm *CameraManager) RecorderCount() int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return len(cm.recorders)
}

// AddCamera adds a new camera to the manager and starts its recorder if enabled.
// If cam.ID is empty, a new ID is generated automatically.
// Returns the camera ID.
func (cm *CameraManager) AddCamera(ctx context.Context, cam config.CameraConfig) (string, error) {
	if cam.ID == "" {
		cam.ID = GenerateCameraID()
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Check for duplicate ID
	for _, existing := range cm.cfg.Cameras {
		if existing.ID == cam.ID {
			return "", fmt.Errorf("camera %q already exists", cam.ID)
		}
	}

	// Append to config
	cm.cfg.Cameras = append(cm.cfg.Cameras, cam)

	// Persist to database
	if cm.db != nil {
		if err := cm.db.UpsertCamera(ctx, cam.ID, cam.Name, string(cam.Protocol), cam.URL, cam.Username, cam.Password, cam.Enabled); err != nil {
			log.Printf("[camera-manager] failed to upsert camera record for %q: %v", cam.ID, err)
		}
	}

	// Start recorder if enabled and protocol supports it
	if cam.Enabled {
		segDur, err := time.ParseDuration(cm.cfg.Storage.SegmentDuration)
		if err != nil {
			segDur = recorder.DefaultSegmentDur
		}
		if err := cm.startRecorder(ctx, cam, segDur); err != nil {
			log.Printf("[camera-manager] add camera: %v", err)
		}
	}

	// Persist config to disk
	if err := cm.persistConfig(); err != nil {
		log.Printf("[camera-manager] add camera: %v", err)
	}

	return cam.ID, nil
}

// RemoveCamera removes a camera from the manager, stops its recorder, and removes it from config.
// Does NOT delete the camera record from the database.
func (cm *CameraManager) RemoveCamera(ctx context.Context, cameraID string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Find camera index
	idx := -1
	for i, cam := range cm.cfg.Cameras {
		if cam.ID == cameraID {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("camera %q not found", cameraID)
	}

	// Stop and remove recorder if running
	if rec, ok := cm.recorders[cameraID]; ok {
		if err := rec.Stop(); err != nil {
			log.Printf("[camera-manager] failed to stop recorder for %q: %v", cameraID, err)
		}
		delete(cm.recorders, cameraID)
	}

	// Remove from config slice
	cm.cfg.Cameras = append(cm.cfg.Cameras[:idx], cm.cfg.Cameras[idx+1:]...)

	// Persist config to disk
	if err := cm.persistConfig(); err != nil {
		log.Printf("[camera-manager] remove camera: %v", err)
	}

	return nil
}

// UpdateCamera applies partial updates to an existing camera.
// Returns the updated CameraConfig.
func (cm *CameraManager) UpdateCamera(ctx context.Context, cameraID string, updates CameraUpdate) (*config.CameraConfig, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Find camera
	idx := -1
	var cam *config.CameraConfig
	for i := range cm.cfg.Cameras {
		if cm.cfg.Cameras[i].ID == cameraID {
			idx = i
			cam = &cm.cfg.Cameras[i]
			break
		}
	}
	if idx == -1 {
		return nil, fmt.Errorf("camera %q not found", cameraID)
	}

	// Determine if recorder needs restart
	needsRestart := false
	if updates.URL != nil && *updates.URL != cam.URL {
		needsRestart = true
	}
	if updates.Protocol != nil && *updates.Protocol != cam.Protocol {
		needsRestart = true
	}
	if updates.Username != nil && *updates.Username != cam.Username {
		needsRestart = true
	}
	if updates.Password != nil && *updates.Password != cam.Password {
		needsRestart = true
	}

	// Apply updates
	if updates.Name != nil {
		cam.Name = *updates.Name
	}
	if updates.URL != nil {
		cam.URL = *updates.URL
	}
	if updates.Protocol != nil {
		cam.Protocol = *updates.Protocol
	}
	if updates.Username != nil {
		cam.Username = *updates.Username
	}
	if updates.Password != nil {
		cam.Password = *updates.Password
	}

	// Handle enabled state changes
	enabledChanged := updates.Enabled != nil && *updates.Enabled != cam.Enabled
	if updates.Enabled != nil {
		cam.Enabled = *updates.Enabled
	}

	// Persist to database
	if cm.db != nil {
		if err := cm.db.UpsertCamera(ctx, cam.ID, cam.Name, string(cam.Protocol), cam.URL, cam.Username, cam.Password, cam.Enabled); err != nil {
			log.Printf("[camera-manager] failed to upsert camera record for %q: %v", cam.ID, err)
		}
	}

	segDur, err := time.ParseDuration(cm.cfg.Storage.SegmentDuration)
	if err != nil {
		segDur = recorder.DefaultSegmentDur
	}

	// Stop existing recorder if needs restart
	if needsRestart {
		if rec, ok := cm.recorders[cam.ID]; ok {
			if err := rec.Stop(); err != nil {
				log.Printf("[camera-manager] failed to stop recorder for %q: %v", cam.ID, err)
			}
			delete(cm.recorders, cam.ID)
		}
	}

	// Start recorder if newly enabled or protocol changed to a recordable one
	if cam.Enabled {
		if needsRestart || enabledChanged {
			// Only start if we don't already have a recorder (needsRestart cleared it, or was never running)
			if _, exists := cm.recorders[cam.ID]; !exists {
				if err := cm.startRecorder(ctx, *cam, segDur); err != nil {
					log.Printf("[camera-manager] update camera: %v", err)
				}
			}
		}
	}

	// If disabled, stop recorder
	if !cam.Enabled && enabledChanged {
		if rec, ok := cm.recorders[cam.ID]; ok {
			if err := rec.Stop(); err != nil {
				log.Printf("[camera-manager] failed to stop recorder for %q: %v", cam.ID, err)
			}
			delete(cm.recorders, cam.ID)
		}
	}

	// Persist config to disk
	if err := cm.persistConfig(); err != nil {
		log.Printf("[camera-manager] update camera: %v", err)
	}

	return cam, nil
}

// RestartRecorder stops and recreates the recorder for the given camera.
// The camera must be enabled.
func (cm *CameraManager) RestartRecorder(ctx context.Context, cameraID string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Find camera config
	var cam *config.CameraConfig
	for i := range cm.cfg.Cameras {
		if cm.cfg.Cameras[i].ID == cameraID {
			cam = &cm.cfg.Cameras[i]
			break
		}
	}
	if cam == nil {
		return fmt.Errorf("camera %q not found", cameraID)
	}
	if !cam.Enabled {
		return fmt.Errorf("camera %q is disabled, cannot restart recorder", cameraID)
	}

	// Stop existing recorder
	if rec, ok := cm.recorders[cameraID]; ok {
		if err := rec.Stop(); err != nil {
			log.Printf("[camera-manager] failed to stop recorder for %q: %v", cameraID, err)
		}
		delete(cm.recorders, cameraID)
	}

	// Create and start new recorder
	segDur, err := time.ParseDuration(cm.cfg.Storage.SegmentDuration)
	if err != nil {
		segDur = recorder.DefaultSegmentDur
	}
	return cm.startRecorder(ctx, *cam, segDur)
}
