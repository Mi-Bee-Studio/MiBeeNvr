package camera

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
)

// getOrCreateONVIFClient returns a cached ONVIF client for the given camera,
// creating one if it doesn't exist in the cache.
// Camera config lookup uses the lock-free snapshot (GetCameraConfig), so it
// does not acquire onvifMu or configMu — safe to call from any context.
func (cm *CameraManager) getOrCreateONVIFClient(ctx context.Context, cameraID string) (*onvif.Client, error) {
	cam := cm.GetCameraConfig(cameraID)
	if cam == nil {
		return nil, &model.CameraNotFoundError{CameraID: cameraID}
	}
	if cam.Protocol != string(model.ProtoONVIF) {
		return nil, &model.ONVIFNotCameraError{CameraID: cameraID}
	}
	endpoint := cam.ONVIFEndpoint
	if endpoint == "" {
		endpoint = cam.URL
	}

	cm.onvifMu.Lock()
	defer cm.onvifMu.Unlock()

	if cached, ok := cm.onvifClients[cameraID]; ok {
		if !cached.IsReady() {
			if err := cached.Connect(ctx); err != nil {
				return nil, &model.ONVIFConnectionError{CameraID: cameraID, Err: err}
			}
		}
		return cached, nil
	}

	client := onvif.NewClient(endpoint, cam.Username, cam.Password)
	if err := client.Connect(ctx); err != nil {
		return nil, &model.ONVIFConnectionError{CameraID: cameraID, Err: err}
	}
	cm.onvifClients[cameraID] = client
	return client, nil
}

// reuseOrCreateONVIFClient returns the cached ONVIF client for the camera if one
// exists, otherwise creates a new client, registers it in the cache, and returns
// it (unconnected). Sharing one client across the recorder, snapshot auto-populator
// and PTZ controller avoids redundant GetCapabilities handshakes — critical for
// minimal ONVIF devices (ESP32 MiBeeCam) that block under concurrent HTTP load.
//
// Unlike getOrCreateONVIFClient, this variant takes the resolved endpoint +
// credentials as arguments rather than looking them up via GetCameraConfig.
// Callers needing a connected client must call Connect on the result (idempotent).
func (cm *CameraManager) reuseOrCreateONVIFClient(cameraID, endpoint, username, password string) *onvif.Client {
	cm.onvifMu.Lock()
	defer cm.onvifMu.Unlock()
	if cached, ok := cm.onvifClients[cameraID]; ok {
		return cached
	}
	c := onvif.NewClient(endpoint, username, password)
	cm.onvifClients[cameraID] = c
	return c
}

// CloseONVIFClient removes a cached ONVIF client for the given camera.
// Also cleans up any cached device info for this camera.
func (cm *CameraManager) CloseONVIFClient(cameraID string) {
	cm.onvifMu.Lock()
	delete(cm.onvifClients, cameraID)
	cm.onvifMu.Unlock()

	cm.deviceInfoMu.Lock()
	delete(cm.deviceInfoCache, cameraID)
	cm.deviceInfoMu.Unlock()
}

// GetCachedDeviceInfo returns cached device info for the given ONVIF camera.
// On first call, fetches from the device and caches the result.
// Returns nil if the camera is not ONVIF, not found, or the device info cannot be fetched.
func (cm *CameraManager) GetCachedDeviceInfo(ctx context.Context, cameraID string) *onvif.DeviceInfo {
	// Check cache with read lock
	cm.deviceInfoMu.RLock()
	if info, ok := cm.deviceInfoCache[cameraID]; ok {
		cm.deviceInfoMu.RUnlock()
		return info
	}
	cm.deviceInfoMu.RUnlock()

	// Not cached — fetch from device
	client, err := cm.getOrCreateONVIFClient(ctx, cameraID)
	if err != nil {
		logger.Warn("failed to get ONVIF client for device info", "camera_id", cameraID, "error", err)
		return nil
	}

	info, err := client.GetDeviceInformation(ctx)
	if err != nil {
		logger.Warn("failed to get device information", "camera_id", cameraID, "error", err)
		return nil
	}

	// Cache with write lock
	cm.deviceInfoMu.Lock()
	cm.deviceInfoCache[cameraID] = info
	cm.deviceInfoMu.Unlock()

	return info
}

// GetONVIFClient returns a cached ONVIF client for the given camera.
// Returns error if camera is not found, not ONVIF, or client creation fails.
func (cm *CameraManager) GetONVIFClient(ctx context.Context, cameraID string) (*onvif.Client, error) {
	return cm.getOrCreateONVIFClient(ctx, cameraID)
}

// closeAllONVIFClients clears the entire ONVIF client and device info caches.
func (cm *CameraManager) closeAllONVIFClients() {
	cm.onvifMu.Lock()
	cm.onvifClients = make(map[string]*onvif.Client)
	cm.onvifMu.Unlock()

	cm.deviceInfoMu.Lock()
	cm.deviceInfoCache = make(map[string]*onvif.DeviceInfo)
	cm.deviceInfoMu.Unlock()
}

// GetONVIFPTZController returns a PTZController for the given ONVIF camera.
// Returns error if camera is not found, not ONVIF, or client creation fails.
func (cm *CameraManager) GetONVIFPTZController(ctx context.Context, cameraID string) (onvif.PTZController, error) {
	client, err := cm.getOrCreateONVIFClient(ctx, cameraID)
	if err != nil {
		return nil, err
	}
	profiles, err := client.GetProfiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("get profiles for camera %q: %w", cameraID, err)
	}
	if len(profiles) == 0 {
		return nil, &model.ONVIFNoProfilesError{CameraID: cameraID}
	}
	return client.NewPTZController(profiles[0].Token), nil
}

// GetImagingController returns an ImagingController for the given ONVIF camera.
// Returns error if camera is not found, not ONVIF, or client creation fails.
func (cm *CameraManager) GetImagingController(ctx context.Context, cameraID string) (onvif.ImagingController, error) {
	client, err := cm.getOrCreateONVIFClient(ctx, cameraID)
	if err != nil {
		return nil, err
	}
	profiles, err := client.GetProfiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("get profiles for camera %q: %w", cameraID, err)
	}
	if len(profiles) == 0 {
		return nil, &model.ONVIFNoProfilesError{CameraID: cameraID}
	}
	// Imaging operations (GetImagingSettings/SetImagingSettings/GetOptions) require
	// a VideoSourceToken, NOT the profile token. The two differ on most cameras
	// (e.g. profile token "MainStreamProfileToken" vs video source token
	// "VideoIPCameraSourceToken"); passing the profile token yields HTTP 400.
	// Fall back to the profile token only when the video source token wasn't
	// parsed (older devices / minimal profiles without VideoSourceConfiguration).
	sourceToken := profiles[0].VideoSourceToken
	if sourceToken == "" {
		sourceToken = profiles[0].Token
	}
	ctrl := client.NewImagingController(sourceToken)
	if ctrl == nil {
		return nil, fmt.Errorf("failed to create imaging controller for camera %q", cameraID)
	}
	endpoint := client.GetEndpoint()
	imgEndpoint := strings.TrimSuffix(endpoint, "/device_service") + "/imaging_service"
	ctrl.SetImagingEndpoint(imgEndpoint)
	return ctrl, nil
}

// GetSnapshotProvider returns a SnapshotProvider for the given ONVIF camera.
// Returns error if camera is not found, not ONVIF, or client creation fails.
func (cm *CameraManager) GetSnapshotProvider(ctx context.Context, cameraID string) (onvif.SnapshotProvider, error) {
	client, err := cm.getOrCreateONVIFClient(ctx, cameraID)
	if err != nil {
		return nil, err
	}
	profiles, err := client.GetProfiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("get profiles for camera %q: %w", cameraID, err)
	}
	if len(profiles) == 0 {
		return nil, &model.ONVIFNoProfilesError{CameraID: cameraID}
	}
	return client.NewSnapshotProvider(profiles[0].Token), nil
}

// GetDeviceManager returns a DeviceManager for the given ONVIF camera.
// Returns error if camera is not found, not ONVIF, or client creation fails.
func (cm *CameraManager) GetDeviceManager(ctx context.Context, cameraID string) (onvif.DeviceManager, error) {
	client, err := cm.getOrCreateONVIFClient(ctx, cameraID)
	if err != nil {
		return nil, err
	}
	dm := client.NewDeviceManager()
	if dm == nil {
		return nil, fmt.Errorf("failed to create device manager for camera %q", cameraID)
	}
	return dm, nil
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

// SubscribeONVIFEvents subscribes to PullPoint events for the given camera.
// The eventCallback is invoked when events are received.
// Returns error if camera is not found, not ONVIF, or subscription fails.
func (cm *CameraManager) SubscribeONVIFEvents(ctx context.Context, cameraID string, eventCallback onvif.EventCallback) error {
	client, err := cm.getOrCreateONVIFClient(ctx, cameraID)
	if err != nil {
		return err
	}

	cm.onvifMu.Lock()
	defer cm.onvifMu.Unlock()

	if _, exists := cm.eventSubscribers[cameraID]; exists {
		return nil // Already subscribed
	}

	sub := client.NewEventSubscriber(onvif.WithEventCallback(eventCallback))
	if sub == nil {
		return fmt.Errorf("camera %q: failed to create event subscriber", cameraID)
	}
	if err := sub.Subscribe(ctx, cameraID); err != nil {
		return fmt.Errorf("camera %q: subscribe to events: %w", cameraID, err)
	}
	cm.eventSubscribers[cameraID] = sub
	logger.Info("subscribed to ONVIF events", "camera_id", cameraID)
	return nil
}

// UnsubscribeONVIFEvents unsubscribes from PullPoint events for the given camera.
func (cm *CameraManager) UnsubscribeONVIFEvents(ctx context.Context, cameraID string) error {
	cm.onvifMu.Lock()
	defer cm.onvifMu.Unlock()

	sub, exists := cm.eventSubscribers[cameraID]
	if !exists {
		return nil
	}

	if err := sub.Unsubscribe(ctx, cameraID); err != nil {
		logger.Warn("failed to unsubscribe from events", "camera_id", cameraID, "error", err)
	}
	delete(cm.eventSubscribers, cameraID)
	logger.Info("unsubscribed from ONVIF events", "camera_id", cameraID)
	return nil
}

// StopAllONVIFEvents unsubscribes from all ONVIF event subscriptions.
func (cm *CameraManager) StopAllONVIFEvents(ctx context.Context) {
	cm.onvifMu.Lock()
	for id, sub := range cm.eventSubscribers {
		_ = sub.Unsubscribe(ctx, id)
	}
	cm.eventSubscribers = make(map[string]onvif.EventSubscriber)
	cm.onvifMu.Unlock()
}

// autoPopulateSnapshotURL fetches the ONVIF snapshot URI and sets cam.SnapshotURL if empty.
// Runs in a goroutine — manages its own locking to avoid deadlock with callers.
func (cm *CameraManager) autoPopulateSnapshotURL(ctx context.Context, cameraID string) {
	// Use a short-lived context to avoid blocking forever
	fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	client, err := cm.getOrCreateONVIFClient(fetchCtx, cameraID)
	if err != nil {
		logger.Warn("failed to get ONVIF client for snapshot URL", "camera_id", cameraID, "error", err)
		return
	}

	profiles, err := client.GetProfiles(fetchCtx)
	if err != nil {
		logger.Warn("failed to get profiles for snapshot URL", "camera_id", cameraID, "error", err)
		return
	}
	if len(profiles) == 0 {
		logger.Warn("no profiles found for snapshot URL", "camera_id", cameraID)
		return
	}

	provider := client.NewSnapshotProvider(profiles[0].Token)
	if provider == nil {
		logger.Warn("failed to create snapshot provider", "camera_id", cameraID)
		return
	}

	uri, err := provider.GetSnapshotUri(fetchCtx)
	if err != nil {
		logger.Warn("failed to get snapshot URI from ONVIF device", "camera_id", cameraID, "error", err)
		return
	}

	// Update SnapshotURL under configMu (cfg.Cameras is the mutable config slice).
	// The snapshot's configs[id] points into cfg.Cameras, so the new value is
	// visible through the existing pointer without republishing the snapshot.
	cm.configMu.Lock()
	for i := range cm.cfg.Cameras {
		if cm.cfg.Cameras[i].ID == cameraID && cm.cfg.Cameras[i].SnapshotURL == "" {
			cm.cfg.Cameras[i].SnapshotURL = uri
			break
		}
	}
	if err := cm.persistConfig(); err != nil {
		logger.Warn("failed to persist snapshot URL", "camera_id", cameraID, "error", err)
	}
	cm.configMu.Unlock()

	logger.Info("auto-populated snapshot URL from ONVIF device", "camera_id", cameraID, "url", uri)
}
