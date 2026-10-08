// Package timesync keeps ONVIF cameras' clocks aligned with the NVR
// (#time-sync).
//
// Path A (active): measure each camera's clock via the pre-auth
// GetSystemDateAndTime and, past the configured skew threshold, write the
// NVR's time to the camera (SetSystemDateAndTime, admin credentials needed).
//
// Path B (long-term heal): point cameras at the NVR's built-in SNTP server
// (SetNTP + DateTimeType=NTP) so they re-sync themselves — including after
// power loss with a dead RTC battery, the most common root cause of "camera
// timestamps occasionally go wrong".
package timesync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
)

// CameraSource is the camera-manager surface this service consumes
// (satisfied by *camera.CameraManager).
type CameraSource interface {
	GetCameraConfig(cameraID string) *config.CameraConfig
	ONVIFCameraIDs() []string
}

// Service runs the automatic correction loop and serves manual operations
// for the API layer.
type Service struct {
	cfg  config.TimeSyncConfig
	cams CameraSource
	bus  *event.EventBus

	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	statusMu sync.Mutex
	status   map[string]*CameraTimeStatus

	// sntpEnabled reports whether path B's server is running (diagnostics in
	// status payloads: "point camera at NVR" only makes sense when it is).
	sntpEnabled func() bool
}

// New constructs the time-sync service. bus may be nil (events skipped).
func New(cfg config.TimeSyncConfig, cams CameraSource, bus *event.EventBus, sntpEnabled func() bool) *Service {
	if sntpEnabled == nil {
		sntpEnabled = func() bool { return false }
	}
	return &Service{
		cfg:         cfg,
		cams:        cams,
		bus:         bus,
		status:      make(map[string]*CameraTimeStatus),
		sntpEnabled: sntpEnabled,
	}
}

// Name implements the app service interface.
func (s *Service) Name() string { return "time-sync" }

// Start stores the lifecycle context and, when auto-correction is enabled,
// launches the periodic loop. Manual operations work regardless.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx != nil {
		return nil
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.ctx, s.cancel = runCtx, cancel
	if !s.cfg.Auto.Enabled {
		slog.Info("camera time-sync auto loop disabled (manual corrections still available)")
		return nil
	}
	slog.Info("camera time-sync auto loop started",
		"interval_minutes", s.cfg.Auto.IntervalOrDefault(),
		"threshold_seconds", s.cfg.Auto.ThresholdOrDefault(),
		"max_correction_seconds", s.cfg.Auto.MaxCorrectionOrDefault())
	s.wg.Add(1)
	go s.loop(runCtx)
	return nil
}

// Stop cancels the loop and waits for it (and any in-flight correction).
func (s *Service) Stop() error {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
	return nil
}

func (s *Service) loop(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(time.Duration(s.cfg.Auto.IntervalOrDefault()) * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweep(ctx)
		}
	}
}

// sweep walks all ONVIF cameras once: measure, record status, correct the
// ones past the threshold (respecting the per-camera opt-out and the max
// correction ceiling).
func (s *Service) sweep(ctx context.Context) {
	for _, id := range s.cams.ONVIFCameraIDs() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		cam := s.cams.GetCameraConfig(id)
		if cam == nil {
			continue
		}
		client := onvif.NewClient(endpointOf(cam), cam.Username, cam.Password)
		dt, err := client.GetDeviceTime(ctx)
		if err != nil {
			s.recordStatus(id, &CameraTimeStatus{CameraID: id, CheckedAt: time.Now(), Error: err.Error()})
			continue
		}
		s.recordStatus(id, statusFromDeviceTime(id, dt))
		// Per-camera opt-out skips the WRITE, not the measurement — the UI
		// still shows the skew for opted-out cameras.
		if cam.AutoTimeSync != nil && !*cam.AutoTimeSync {
			continue
		}

		threshold := time.Duration(s.cfg.Auto.ThresholdOrDefault()) * time.Second
		maxC := time.Duration(s.cfg.Auto.MaxCorrectionOrDefault()) * time.Second
		skew := dt.Skew
		if skew < 0 {
			skew = -skew
		}
		if skew < threshold {
			continue
		}
		if skew > maxC {
			slog.Warn("camera clock skew exceeds max_correction_seconds — not auto-correcting; trigger a manual sync",
				"camera_id", id, "skew_seconds", dt.Skew.Seconds())
			s.publish(id, "over_max", dt.Skew.Seconds(), false)
			continue
		}
		res, err := s.correctWith(ctx, cam, client, dt, "")
		if err != nil {
			slog.Warn("auto time correction failed", "camera_id", id, "error", err)
			continue
		}
		slog.Info("auto time correction applied", "camera_id", id,
			"before_seconds", res.BeforeSeconds, "after_seconds", res.AfterSeconds)
		s.publish(id, "auto", res.AfterSeconds, true)

		if s.cfg.Auto.NTPPush && s.sntpEnabled() {
			if err := s.pointAtNVR(ctx, cam, ""); err != nil {
				slog.Warn("NTP push after correction failed", "camera_id", id, "error", err)
			}
		}
		// Small gap between cameras: don't burst SOAP at the LAN.
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// CameraTimeStatus is the API-facing snapshot of one camera's clock state.
type CameraTimeStatus struct {
	CameraID     string    `json:"camera_id"`
	Available    bool      `json:"available"` // camera clock readable
	CheckedAt    time.Time `json:"checked_at"`
	SkewSeconds  float64   `json:"skew_seconds"` // device − NVR, signed
	DeviceUTC    string    `json:"device_utc,omitempty"`
	DateTimeType string    `json:"datetime_type,omitempty"` // Manual | NTP
	TimeZone     string    `json:"timezone,omitempty"`
	Error        string    `json:"error,omitempty"`
	AutoManaged  bool      `json:"auto_managed"` // in the auto loop's scope
	SNTPEnabled  bool      `json:"sntp_enabled"` // NVR's own SNTP server running
	// Credentials reports whether admin write operations are possible for
	// this camera (SetSystemDateAndTime/SetNTP need them).
	Credentials bool `json:"credentials"`
}

func statusFromDeviceTime(id string, dt *onvif.DeviceTime) *CameraTimeStatus {
	return &CameraTimeStatus{
		CameraID:     id,
		Available:    true,
		CheckedAt:    time.Now(),
		SkewSeconds:  math.Round(dt.Skew.Seconds()*100) / 100,
		DeviceUTC:    dt.UTCDateTime.Format(time.RFC3339),
		DateTimeType: dt.DateTimeType,
		TimeZone:     dt.TimeZone,
	}
}

func (s *Service) recordStatus(id string, st *CameraTimeStatus) {
	s.decorateStatus(st)
	s.statusMu.Lock()
	s.status[id] = st
	s.statusMu.Unlock()
}

func (s *Service) decorateStatus(st *CameraTimeStatus) {
	st.SNTPEnabled = s.sntpEnabled()
	st.AutoManaged = s.autoManaged(st.CameraID)
}

func (s *Service) autoManaged(cameraID string) bool {
	if !s.cfg.Auto.Enabled {
		return false
	}
	cam := s.cams.GetCameraConfig(cameraID)
	if cam == nil {
		return false
	}
	if cam.AutoTimeSync != nil {
		return *cam.AutoTimeSync
	}
	return true
}

// Status returns the camera's clock status — cached when fresh (< 2 minutes),
// measured live otherwise. Errors are cached too (an unreachable camera stays
// "unavailable" for the cache window instead of blocking every UI open).
func (s *Service) Status(ctx context.Context, cameraID string) (*CameraTimeStatus, error) {
	cam := s.cams.GetCameraConfig(cameraID)
	if cam == nil {
		return nil, fmt.Errorf("camera %q not found", cameraID)
	}
	if cam.Protocol != "onvif" {
		return nil, errors.New("time sync is only available for ONVIF cameras")
	}

	s.statusMu.Lock()
	cached := s.status[cameraID]
	s.statusMu.Unlock()
	if cached != nil && time.Since(cached.CheckedAt) < 2*time.Minute {
		cp := *cached
		return &cp, nil
	}

	client := onvif.NewClient(endpointOf(cam), cam.Username, cam.Password)
	dt, err := client.GetDeviceTime(ctx)
	if err != nil {
		st := &CameraTimeStatus{CameraID: cameraID, CheckedAt: time.Now(), Error: err.Error()}
		s.recordStatus(cameraID, st)
		cp := *st
		return &cp, nil //nolint:nilerr // the error is folded into the status payload — an unreadable clock is a state, not a 500
	}
	st := statusFromDeviceTime(cameraID, dt)
	s.recordStatus(cameraID, st)
	cp := *st
	return &cp, nil
}

// CorrectResult reports one correction round trip.
type CorrectResult struct {
	Changed       bool    `json:"changed"`
	BeforeSeconds float64 `json:"before_seconds"`
	AfterSeconds  float64 `json:"after_seconds"`
}

// CorrectCameraTime measures and (when needed) writes the NVR time to the
// camera, then re-measures. Skews under 1s are left alone (Changed=false).
// tzOverride lets the API caller pin the POSIX TZ string; empty = config
// value or NVR-local derivation.
func (s *Service) CorrectCameraTime(ctx context.Context, cameraID, tzOverride string) (*CorrectResult, error) {
	cam := s.cams.GetCameraConfig(cameraID)
	if cam == nil {
		return nil, fmt.Errorf("camera %q not found", cameraID)
	}
	if cam.Protocol != "onvif" {
		return nil, errors.New("time sync is only available for ONVIF cameras")
	}
	client := onvif.NewClient(endpointOf(cam), cam.Username, cam.Password)
	dt, err := client.GetDeviceTime(ctx)
	if err != nil {
		return nil, fmt.Errorf("read camera time: %w", err)
	}
	s.recordStatus(cameraID, statusFromDeviceTime(cameraID, dt))
	res, err := s.correctWith(ctx, cam, client, dt, tzOverride)
	if err != nil {
		return nil, err
	}
	if res.Changed {
		s.publish(cameraID, "manual", res.AfterSeconds, true)
	}
	return res, nil
}

// correctWith performs the write + re-measure for an already-measured camera.
// Shared by the manual API path and the auto loop.
func (s *Service) correctWith(ctx context.Context, cam *config.CameraConfig, client *onvif.Client, dt *onvif.DeviceTime, tzOverride string) (*CorrectResult, error) {
	res := &CorrectResult{BeforeSeconds: math.Round(dt.Skew.Seconds()*100) / 100}
	if absSeconds(dt.Skew) < 1 {
		res.AfterSeconds = res.BeforeSeconds
		return res, nil
	}
	if cam.Username == "" {
		return nil, onvif.ErrNoCredentials
	}
	tz := tzOverride
	if tz == "" {
		tz = s.cfg.Auto.Timezone
	}
	if tz == "" {
		tz = posixLocalTZ(time.Now())
	}
	if err := client.SetDeviceTime(ctx, tz); err != nil {
		return nil, fmt.Errorf("write camera time: %w", err)
	}
	after, err := client.GetDeviceTime(ctx)
	if err != nil {
		return nil, fmt.Errorf("re-read camera time after correction: %w", err)
	}
	s.recordStatus(cam.ID, statusFromDeviceTime(cam.ID, after))
	res.Changed = true
	res.AfterSeconds = math.Round(after.Skew.Seconds()*100) / 100
	return res, nil
}

// PointCameraAtNTP sets the camera's NTP server (default: the NVR's own IP
// as routable from the camera) and flips its DateTimeType to NTP — path B.
func (s *Service) PointCameraAtNTP(ctx context.Context, cameraID, serverOverride string) error {
	cam := s.cams.GetCameraConfig(cameraID)
	if cam == nil {
		return fmt.Errorf("camera %q not found", cameraID)
	}
	if cam.Protocol != "onvif" {
		return errors.New("time sync is only available for ONVIF cameras")
	}
	if cam.Username == "" {
		return onvif.ErrNoCredentials
	}
	return s.pointAtNVR(ctx, cam, serverOverride)
}

func (s *Service) pointAtNVR(ctx context.Context, cam *config.CameraConfig, serverOverride string) error {
	server := serverOverride
	if server == "" {
		ip, err := localAddrFor(cam)
		if err != nil {
			return fmt.Errorf("resolve NVR address for camera: %w", err)
		}
		server = ip
	}
	client := onvif.NewClient(endpointOf(cam), cam.Username, cam.Password)
	if err := client.SetNTPServer(ctx, server); err != nil {
		return err
	}
	slog.Info("camera pointed at NVR SNTP server", "camera_id", cam.ID, "server", server)
	return nil
}

func (s *Service) publish(cameraID, method string, skewSeconds float64, corrected bool) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(context.Background(), event.TopicCameraTimeSync, map[string]any{
		"camera_id":    cameraID,
		"method":       method,
		"skew_seconds": skewSeconds,
		"corrected":    corrected,
	})
}

// Statuses exposes the cached statuses for diagnostics (API layer).
func (s *Service) Statuses() map[string]*CameraTimeStatus {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	out := make(map[string]*CameraTimeStatus, len(s.status))
	for k, v := range s.status {
		cp := *v
		out[k] = &cp
	}
	return out
}

// --- helpers ---

func endpointOf(cam *config.CameraConfig) string {
	if cam.ONVIFEndpoint != "" {
		return cam.ONVIFEndpoint
	}
	return cam.URL
}

func absSeconds(d time.Duration) float64 {
	if d < 0 {
		return float64(-d) / float64(time.Second)
	}
	return float64(d) / float64(time.Second)
}

// localAddrFor determines the NVR's source IP on the route towards this
// camera — the address the camera should use as its NTP server.
func localAddrFor(cam *config.CameraConfig) (string, error) {
	host := endpointOf(cam)
	host = strings.TrimPrefix(strings.TrimPrefix(host, "http://"), "https://")
	if i := strings.IndexAny(host, "/"); i >= 0 {
		host = host[:i]
	}
	if i := strings.LastIndex(host, ":"); i > 0 && !strings.Contains(host, "]") {
		host = host[:i]
	}
	conn, err := net.Dial("udp", net.JoinHostPort(host, "80"))
	if err != nil {
		return "", err
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String(), nil
}

// posixLocalTZ derives a POSIX TZ string from the NVR's local timezone
// (e.g. "CST-8" for UTC+8 with a CST abbreviation). Used when
// time_sync.auto.timezone is unset: cameras get written the NVR's zone, not
// UTC, so their OSD timestamps read local time like before.
func posixLocalTZ(now time.Time) string {
	local := now.In(time.Local)
	name, off := local.Zone()
	if name == "" || strings.ContainsAny(name, "0123456789+-") {
		// Numeric offset zone (e.g. fixed-offset containers): use a neutral name.
		name = "UTC"
	}
	// POSIX sign is inverted: UTC+8 → CST-8.
	pos := -off
	h := pos / 3600
	m := (pos % 3600) / 60
	if m < 0 {
		m = -m
	}
	if m == 0 {
		return fmt.Sprintf("%s%d", name, h)
	}
	return fmt.Sprintf("%s%d:%02d", name, h, m)
}
