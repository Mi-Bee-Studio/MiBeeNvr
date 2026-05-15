package camera

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/plugin"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

var logger = slog.Default().With("component", "camera-manager")

// CameraUpdate holds optional fields for updating a camera.
// Only non-nil fields will be applied.
type CameraUpdate struct {
	Name         *string
	URL          *string
	Protocol     *string
	Encoding     *string
	Username     *string
	Password     *string
	Enabled      *bool
	Description  *string
	Location     *string
	Brand        *string
	Model        *string
	SerialNumber   *string
	RetentionDays *int
	ONVIFEndpoint  *string
	ProfileToken   *string
	StreamEncoding *string
}

type CameraManager struct {
	cfg        *config.Config
	store      *storage.Manager
	db         *storage.DB
	configPath string
	recorders  map[string]model.Recorder // camera_id → Recorder
	metrics    *metrics.Metrics
	mu         sync.RWMutex
}

// NewCameraManager creates a new CameraManager.
func NewCameraManager(cfg *config.Config, store *storage.Manager, db *storage.DB, configPath string, opts ...*metrics.Metrics) *CameraManager {
	var m *metrics.Metrics
	if len(opts) > 0 {
		m = opts[0]
	}
	return &CameraManager{
		cfg:        cfg,
		store:      store,
		db:         db,
		configPath: configPath,
		recorders:  make(map[string]model.Recorder),
		metrics:    m,
	}
}

// createRecorder creates a recorder for the given camera config.
// Returns nil for unknown protocols.
func (cm *CameraManager) createRecorder(cam config.CameraConfig, segDur time.Duration) model.Recorder {
	// 1. Try plugin registry first
	p := plugin.LookupProtocol(cam.Protocol)
	if p != nil {
		return p.NewRecorder(cam, cm.store, cm.db, cm.metrics)
	}

	// 2. Fall back to built-in recorders
	switch cam.Protocol {
	case string(model.ProtoRTSP):
		switch cam.Encoding {
		case string(model.FormatH264):
			h264Cfg := recorder.H264Config{
				CameraID:   cam.ID,
				RTSPURL:    cam.URL,
				Username:   cam.Username,
				Password:   cam.Password,
				SegmentDur: segDur,
				DB:         cm.db,
			}
			return recorder.NewH264Recorder(h264Cfg, cm.store, cm.metrics)
		case string(model.FormatH265):
			h265Cfg := recorder.H265Config{
				CameraID:   cam.ID,
				RTSPURL:    cam.URL,
				Username:   cam.Username,
				Password:   cam.Password,
				SegmentDur: segDur,
				DB:         cm.db,
			}
			return recorder.NewH265Recorder(h265Cfg, cm.store, cm.metrics)
		case string(model.FormatMJPEG):
			mjpegCfg := recorder.MJPEGConfig{
				CameraID:       cam.ID,
				RTSPURL:        cam.URL,
				SegmentDur:     segDur,
				SampleInterval: cam.SampleInterval,
				DB:             cm.db,
			}
			return recorder.NewMJPEGRecorder(mjpegCfg, cm.store, cm.metrics)
		default:
			return nil
		}
	case string(model.ProtoHTTP):
		if cam.Encoding != string(model.EncJPEG) {
			return nil
		}
		httpJpegCfg := recorder.HTTPJPEGConfig{
			CameraID:   cam.ID,
			URL:        cam.URL,
			SegmentDur: segDur,
			Username:   cam.Username,
			Password:   cam.Password,
			DB:         cm.db,
		}
		return recorder.NewHTTPJPEGRecorder(httpJpegCfg, cm.store, cm.metrics)
	case string(model.ProtoONVIF):
		onvifEndpoint := cam.ONVIFEndpoint
		if onvifEndpoint == "" {
			onvifEndpoint = cam.URL
		}
		onvifClient := onvif.NewClient(onvifEndpoint, cam.Username, cam.Password)
		onvifCfg := recorder.ONVIFConfig{
			CameraID:       cam.ID,
			ProfileToken:   cam.ProfileToken,
			StreamEncoding: cam.StreamEncoding,
			Username:       cam.Username,
			Password:       cam.Password,
			SegmentDur:     segDur,
			DB:             cm.db,
		}
		return recorder.NewONVIFRecorder(onvifCfg, onvifClient, cm.store, cm.metrics)
	default:
		return nil
	}
}

// startRecorder creates and starts a recorder for the given camera config.
// The caller must hold cm.mu (or at least a write lock) if cm.recorders is being modified.
// If the recorder is created, it will be registered in cm.recorders.
func (cm *CameraManager) startRecorder(ctx context.Context, cam config.CameraConfig, segDur time.Duration) error {
	rec := cm.createRecorder(cam, segDur)
	if rec == nil {
		return fmt.Errorf("camera %q: protocol %q does not support recording", cam.ID, cam.Protocol)
	}
	cm.recorders[cam.ID] = rec
	// Recorders derive their run context from context.Background() internally,
	// so their lifecycle is independent of this ctx (e.g. HTTP request context).
	// The ctx is only used for short initial setup (e.g. ONVIF device probe).
	if err := rec.Start(ctx); err != nil {
		delete(cm.recorders, cam.ID)
		return fmt.Errorf("camera %q: failed to start recorder: %w", cam.ID, err)
	}
	if cm.metrics != nil {
		cm.metrics.ActiveCameras.Inc()
	}
	logger.Info("started recorder for camera", "camera_id", cam.ID)
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
		if err := cm.db.UpsertCamera(ctx, cam.ID, cam.Name, string(cam.Protocol), cam.Encoding, cam.URL, cam.Username, cam.Password, cam.Enabled, cam.ONVIFEndpoint, cam.ProfileToken, cam.StreamEncoding); err != nil {
			logger.Error("failed to insert camera record", "camera_id", cam.ID, "error", err)
		} else {
			logger.Info("inserted camera record", "camera_id", cam.ID)
		}

		if !cam.Enabled {
			logger.Info("camera disabled, skipping", "camera_id", cam.ID, "protocol", cam.Protocol)
			continue
		}

		switch cam.Protocol {
		case string(model.ProtoRTSP), string(model.ProtoHTTP):
			rec := cm.createRecorder(cam, segDur)
			if rec != nil {
				cm.mu.Lock()
				cm.recorders[cam.ID] = rec
				cm.mu.Unlock()
				if err := rec.Start(ctx); err != nil {
					logger.Error("failed to start recorder", "camera_id", cam.ID, "error", err)
				} else {
					logger.Info("started recorder", "camera_id", cam.ID, "protocol", cam.Protocol, "encoding", cam.Encoding)
				}
			}
		case string(model.ProtoONVIF):
			if err := cm.startRecorder(ctx, cam, segDur); err != nil {
				logger.Error("failed to start ONVIF recorder", "camera_id", cam.ID, "error", err)
			} else {
				logger.Info("started ONVIF recorder", "camera_id", cam.ID)
			}
		default:
			// Try plugin-registered protocols (e.g. xiaomi)
			if err := cm.startRecorder(ctx, cam, segDur); err != nil {
				logger.Warn("camera has unknown protocol, skipping", "camera_id", cam.ID, "protocol", cam.Protocol)
			} else {
				logger.Info("started plugin recorder", "camera_id", cam.ID, "protocol", cam.Protocol)
			}
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

// GetRecorder returns the recorder for the given camera ID, or nil if not found.
func (cm *CameraManager) GetRecorder(cameraID string) model.Recorder {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.recorders[cameraID]
}

// GetCameraConfig returns the config for the given camera ID, or nil if not found.
func (cm *CameraManager) GetCameraConfig(cameraID string) *config.CameraConfig {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	for i := range cm.cfg.Cameras {
		if cm.cfg.Cameras[i].ID == cameraID {
			return &cm.cfg.Cameras[i]
		}
	}
	return nil
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
	if err := cm.db.UpsertCamera(ctx, cam.ID, cam.Name, string(cam.Protocol), cam.Encoding, cam.URL, cam.Username, cam.Password, cam.Enabled, cam.ONVIFEndpoint, cam.ProfileToken, cam.StreamEncoding); err != nil {
			logger.Error("failed to upsert camera record", "camera_id", cam.ID, "error", err)
		}
	}

	// Start recorder if enabled and protocol supports it
	if cam.Enabled {
		segDur, err := time.ParseDuration(cm.cfg.Storage.SegmentDuration)
		if err != nil {
			segDur = recorder.DefaultSegmentDur
		}
		if err := cm.startRecorder(ctx, cam, segDur); err != nil {
			logger.Error("failed to start recorder", "error", err)
		}
	}

	// Persist config to disk
	if err := cm.persistConfig(); err != nil {
		logger.Error("failed to persist config", "error", err)
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
			logger.Warn("failed to stop recorder", "camera_id", cameraID, "error", err)
		}
		delete(cm.recorders, cameraID)
		if cm.metrics != nil {
			cm.metrics.ActiveCameras.Dec()
		}
	}

	// Remove from config slice
	cm.cfg.Cameras = append(cm.cfg.Cameras[:idx], cm.cfg.Cameras[idx+1:]...)

	// Persist config to disk
	if err := cm.persistConfig(); err != nil {
		logger.Error("failed to persist config", "error", err)
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
	if updates.Encoding != nil {
		if *updates.Encoding != cam.Encoding {
			needsRestart = true
		}
		cam.Encoding = *updates.Encoding
	}
	if updates.Username != nil {
		cam.Username = *updates.Username
	}
	if updates.Password != nil {
		cam.Password = *updates.Password
	}
	if updates.ONVIFEndpoint != nil {
		cam.ONVIFEndpoint = *updates.ONVIFEndpoint
	}
	if updates.ProfileToken != nil {
		cam.ProfileToken = *updates.ProfileToken
	}
	if updates.StreamEncoding != nil {
		if *updates.StreamEncoding != cam.StreamEncoding {
			needsRestart = true
		}
		cam.StreamEncoding = *updates.StreamEncoding
	}

	// Handle enabled state changes
	enabledChanged := updates.Enabled != nil && *updates.Enabled != cam.Enabled
	if updates.Enabled != nil {
		cam.Enabled = *updates.Enabled
	}

	// Persist to database
	if cm.db != nil {
		if err := cm.db.UpsertCamera(ctx, cam.ID, cam.Name, string(cam.Protocol), cam.Encoding, cam.URL, cam.Username, cam.Password, cam.Enabled, cam.ONVIFEndpoint, cam.ProfileToken, cam.StreamEncoding); err != nil {
			logger.Error("failed to upsert camera record", "camera_id", cam.ID, "error", err)
		}
		// Persist DB-only metadata fields
		if updates.Description != nil || updates.Location != nil || updates.Brand != nil || updates.Model != nil || updates.SerialNumber != nil || updates.RetentionDays != nil {
			desc := strPtrOrEmpty(updates.Description)
			loc := strPtrOrEmpty(updates.Location)
			br := strPtrOrEmpty(updates.Brand)
			mo := strPtrOrEmpty(updates.Model)
			sn := strPtrOrEmpty(updates.SerialNumber)
		rd := intPtrOrZero(updates.RetentionDays)
			if err := cm.db.UpdateCameraMetadata(ctx, cam.ID, desc, loc, br, mo, sn, rd); err != nil {
				logger.Error("failed to update camera metadata", "camera_id", cam.ID, "error", err)
			}
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
				logger.Warn("failed to stop recorder", "camera_id", cam.ID, "error", err)
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
					logger.Error("failed to start recorder", "error", err)
				}
			}
		}
	}

	// If disabled, stop recorder
	if !cam.Enabled && enabledChanged {
		if rec, ok := cm.recorders[cam.ID]; ok {
			if err := rec.Stop(); err != nil {
				logger.Warn("failed to stop recorder", "camera_id", cam.ID, "error", err)
			}
			delete(cm.recorders, cam.ID)
			if cm.metrics != nil {
				cm.metrics.ActiveCameras.Dec()
			}
		}
	}

	// Persist config to disk
	if err := cm.persistConfig(); err != nil {
		logger.Error("failed to persist config", "error", err)
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
			logger.Warn("failed to stop recorder", "camera_id", cameraID, "error", err)
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

// GetONVIFPTZController returns a PTZController for the given ONVIF camera.
// Returns error if camera is not found, not ONVIF, or client creation fails.
func (cm *CameraManager) GetONVIFPTZController(ctx context.Context, cameraID string) (onvif.PTZController, error) {
	cm.mu.RLock()
	cam := cm.GetCameraConfig(cameraID)
	cm.mu.RUnlock()
	if cam == nil {
		return nil, fmt.Errorf("camera %q not found", cameraID)
	}
	if cam.Protocol != string(model.ProtoONVIF) {
		return nil, fmt.Errorf("camera %q is not an ONVIF camera", cameraID)
	}
	endpoint := cam.ONVIFEndpoint
	if endpoint == "" {
		endpoint = cam.URL
	}
	client := onvif.NewClient(endpoint, cam.Username, cam.Password)
	if err := client.Connect(ctx); err != nil {
		return nil, fmt.Errorf("connect to ONVIF camera %q: %w", cameraID, err)
	}
	profiles, err := client.GetProfiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("get profiles for camera %q: %w", cameraID, err)
	}
	if len(profiles) == 0 {
		return nil, fmt.Errorf("no media profiles found for camera %q", cameraID)
	}
	return client.NewPTZController(profiles[0].Token), nil
}

// strPtrOrEmpty returns the string value of a *string pointer, or empty string if nil.
func strPtrOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// intPtrOrZero returns the int value of a *int pointer, or 0 if nil.
func intPtrOrZero(i *int) int {
	if i == nil {
		return 0
	}
	return *i
}
