package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timesync"
)

// fakeTimeSync implements TimeSyncOps.
type fakeTimeSync struct {
	status   *timesync.CameraTimeStatus
	statusE  error
	correct  *timesync.CorrectResult
	correctE error
	ntpE     error

	correctTZ string
	ntpServer string
}

func (f *fakeTimeSync) Status(ctx context.Context, id string) (*timesync.CameraTimeStatus, error) {
	return f.status, f.statusE
}

func (f *fakeTimeSync) CorrectCameraTime(ctx context.Context, id, tz string) (*timesync.CorrectResult, error) {
	f.correctTZ = tz
	return f.correct, f.correctE
}

func (f *fakeTimeSync) PointCameraAtNTP(ctx context.Context, id, server string) error {
	f.ntpServer = server
	return f.ntpE
}

// newTimeSyncHandler builds a handler with one pre-seeded ONVIF camera
// ("cam-1") plus the fake time-sync service. No recorder starts — the camera
// exists only in config/DB.
func newTimeSyncHandler(t *testing.T, ts TimeSyncOps) *Handler {
	t.Helper()
	db, store := setupTestDB(t)
	cfg := &config.Config{
		Storage: config.StorageConfig{RootDir: store.RootDir(), SegmentDuration: "30s"},
		Cleanup: config.CleanupConfig{RetentionDays: 30, CheckInterval: "1h", DiskThresholdPercent: 95},
		Cameras: []config.CameraConfig{{
			ID: "cam-1", Name: "Cam 1", Protocol: "onvif",
			ONVIFEndpoint: "http://127.0.0.1:1/onvif/device_service",
		}},
	}
	camMgr := camera.NewCameraManager(cfg, store, db, "")
	h := NewHandler(db, store, noopAuthMW(), cfg, camMgr, nil, "", nil, nil, nil, nil, nil, ts)
	return h
}

func TestCameraTimeStatus_ReturnsPayload(t *testing.T) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	fake := &fakeTimeSync{status: &timesync.CameraTimeStatus{
		CameraID: "cam-1", Available: true, CheckedAt: now,
		SkewSeconds: -92.5, DateTimeType: "Manual", TimeZone: "CST-8",
		SNTPEnabled: true, AutoManaged: false,
	}}
	h := newTimeSyncHandler(t, fake)

	rr := doRequest(t, h.Routes(), http.MethodGet, "/api/cameras/cam-1/time", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	var got timesync.CameraTimeStatus
	parseJSON(t, rr, &got)
	if got.SkewSeconds != -92.5 || got.TimeZone != "CST-8" || !got.SNTPEnabled {
		t.Errorf("payload mismatch: %+v", got)
	}
}

func TestCameraTime_Guards(t *testing.T) {
	t.Helper()
	fake := &fakeTimeSync{status: &timesync.CameraTimeStatus{CameraID: "x", Available: true}}
	h := newTimeSyncHandler(t, fake)
	r := h.Routes()

	// Unknown camera → 404.
	rr := doRequest(t, r, http.MethodGet, "/api/cameras/nope/time", nil, "", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("unknown camera = %d, want 404", rr.Code)
	}
	// Service not wired → 503.
	h2 := newTimeSyncHandler(t, nil)
	rr = doRequest(t, h2.Routes(), http.MethodGet, "/api/cameras/cam-1/time", nil, "", "")
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("service down = %d, want 503", rr.Code)
	}
}

func TestCameraTimeSync_CorrectFlowAndCredentialError(t *testing.T) {
	t.Helper()
	fake := &fakeTimeSync{correct: &timesync.CorrectResult{Changed: true, BeforeSeconds: -90, AfterSeconds: 0.2}}
	h := newTimeSyncHandler(t, fake)

	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/cameras/cam-1/time/sync",
		strings.NewReader(`{"timezone":"CST-8"}`), "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("sync = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if fake.correctTZ != "CST-8" {
		t.Errorf("tz passed = %q, want CST-8", fake.correctTZ)
	}

	// Credentials missing → 400 with the actionable message.
	fake2 := &fakeTimeSync{correctE: onvif.ErrNoCredentials}
	h2 := newTimeSyncHandler(t, fake2)
	rr = doRequest(t, h2.Routes(), http.MethodPost, "/api/cameras/cam-1/time/sync", nil, "", "")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("no-credentials = %d, want 400: %s", rr.Code, rr.Body.String())
	}
}

func TestCameraTimeNTP_PassesServerOverride(t *testing.T) {
	t.Helper()
	fake := &fakeTimeSync{}
	h := newTimeSyncHandler(t, fake)

	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/cameras/cam-1/time/ntp",
		strings.NewReader(`{"server":"192.168.63.30"}`), "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("ntp = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if fake.ntpServer != "192.168.63.30" {
		t.Errorf("server = %q, want passed through", fake.ntpServer)
	}
}

func TestCameraTimeEndpoints_DeviceErrorIsBadGateway(t *testing.T) {
	t.Helper()
	fake := &fakeTimeSync{statusE: errors.New("camera unreachable")}
	h := newTimeSyncHandler(t, fake)
	rr := doRequest(t, h.Routes(), http.MethodGet, "/api/cameras/cam-1/time", nil, "", "")
	if rr.Code != http.StatusBadGateway {
		t.Errorf("device error = %d, want 502: %s", rr.Code, rr.Body.String())
	}
}
