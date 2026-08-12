package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// Test helper function - must use t.Helper()
func setupGB28181TestHandler(t *testing.T) *Handler {
	t.Helper()
	db, _ := setupTestDB(t)

	deviceMgr := gb28181.NewDeviceManager(60 * time.Second)
	sessionMgr := gb28181.NewSessionManager(gb28181.NewPortManager(30000, 30100), "3402000000")

	return NewHandler(db, nil, noopAuthMW(), nil, nil, nil, "", nil, nil, nil, deviceMgr, sessionMgr)
}

func TestAPI_GB28181_ListDevices_Empty(t *testing.T) {
	t.Helper()
	h := setupGB28181TestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/gb28181/devices", nil)
	w := httptest.NewRecorder()
	h.handleListGB28181Devices(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	// Check ETag header is set
	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatal("expected ETag header to be set")
	}

	// Check response is an empty array
	var devices []storage.GB28181Device
	err := json.Unmarshal(w.Body.Bytes(), &devices)
	if err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if len(devices) != 0 {
		t.Fatalf("expected empty device list, got %d devices", len(devices))
	}
}

func TestAPI_GB28181_ListDevices_WithLimit(t *testing.T) {
	t.Helper()
	h := setupGB28181TestHandler(t)

	// Insert test devices
	ctx := context.Background()
	now := time.Now()
	for i := range 10 {
		_ = h.db.UpsertGB28181Device(ctx, storage.GB28181Device{
			ID:            string(rune('a' + i)),
			Name:          "Test Device",
			Manufacturer:  "Test Manufacturer",
			Model:         "Test Model",
			Status:        "online",
			LastKeepalive: now,
			RegisteredAt:  now,
		})
	}

	// Test default limit (50)
	req := httptest.NewRequest(http.MethodGet, "/api/gb28181/devices", nil)
	w := httptest.NewRecorder()
	h.handleListGB28181Devices(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var devices []storage.GB28181Device
	err := json.Unmarshal(w.Body.Bytes(), &devices)
	if err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if len(devices) != 10 {
		t.Fatalf("expected 10 devices with default limit, got %d", len(devices))
	}

	// Test custom limit
	req = httptest.NewRequest(http.MethodGet, "/api/gb28181/devices?limit=5", nil)
	w = httptest.NewRecorder()
	h.handleListGB28181Devices(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var devicesLimited []storage.GB28181Device
	err = json.Unmarshal(w.Body.Bytes(), &devicesLimited)
	if err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if len(devicesLimited) != 5 {
		t.Fatalf("expected 5 devices with limit=5, got %d", len(devicesLimited))
	}

	// Test max limit clamp
	req = httptest.NewRequest(http.MethodGet, "/api/gb28181/devices?limit=1000", nil)
	w = httptest.NewRecorder()
	h.handleListGB28181Devices(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var devicesClamped []storage.GB28181Device
	err = json.Unmarshal(w.Body.Bytes(), &devicesClamped)
	if err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if len(devicesClamped) != 10 {
		t.Fatalf("expected 10 devices with limit clamped to max, got %d", len(devicesClamped))
	}
}

func TestAPI_GB28181_ListDevices_ETag(t *testing.T) {
	t.Helper()
	h := setupGB28181TestHandler(t)

	ctx := context.Background()
	now := time.Now()
	_ = h.db.UpsertGB28181Device(ctx, storage.GB28181Device{
		ID:            "test1",
		Name:          "Test Device",
		Manufacturer:  "Test Manufacturer",
		Model:         "Test Model",
		Status:        "online",
		LastKeepalive: now,
		RegisteredAt:  now,
	})

	// First request - should get full response
	req := httptest.NewRequest(http.MethodGet, "/api/gb28181/devices", nil)
	w := httptest.NewRecorder()
	h.handleListGB28181Devices(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatal("expected ETag header to be set")
	}

	// Second request with If-None-Match - should get 304
	req = httptest.NewRequest(http.MethodGet, "/api/gb28181/devices", nil)
	req.Header.Set("If-None-Match", etag)
	w = httptest.NewRecorder()
	h.handleListGB28181Devices(w, req)

	if w.Code != http.StatusNotModified {
		t.Fatalf("expected status 304, got %d", w.Code)
	}

	if w.Body.Len() != 0 {
		t.Fatal("expected empty body for 304 response")
	}
}

func TestAPI_GB28181_ListChannels_Success(t *testing.T) {
	t.Helper()
	h := setupGB28181TestHandler(t)

	ctx := context.Background()
	now := time.Now()

	// Create test device and channels
	_ = h.db.UpsertGB28181Device(ctx, storage.GB28181Device{
		ID:            "device1",
		Name:          "Test Device",
		Manufacturer:  "Test Manufacturer",
		Model:         "Test Model",
		Status:        "online",
		LastKeepalive: now,
		RegisteredAt:  now,
	})

	_ = h.db.UpsertGB28181Channel(ctx, storage.GB28181Channel{
		ID:           "channel1",
		DeviceID:     "device1",
		Name:         "Test Channel",
		Manufacturer: "Test Manufacturer",
		Parental:     0,
		Status:       "idle",
		CameraID:     "",
		UpdatedAt:    now,
	})

	rr := doRequest(t, h.Routes(), http.MethodGet, "/api/gb28181/devices/device1/channels", nil, "", "")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var channels []storage.GB28181Channel

	err := json.Unmarshal(rr.Body.Bytes(), &channels)
	if err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(channels))
	}

	if channels[0].ID != "channel1" {
		t.Fatalf("expected channel ID 'channel1', got '%s'", channels[0].ID)
	}
}

func TestAPI_GB28181_ListChannels_DeviceNotFound(t *testing.T) {
	t.Helper()
	h := setupGB28181TestHandler(t)

	rr := doRequest(t, h.Routes(), http.MethodGet, "/api/gb28181/devices/nonexistent/channels", nil, "", "")

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", rr.Code)
	}
}

func TestAPI_GB28181_ListChannels_MissingDeviceID(t *testing.T) {
	t.Helper()
	h := setupGB28181TestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/gb28181/devices//channels", nil)
	w := httptest.NewRecorder()
	h.handleListGB28181Channels(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}
}

func TestAPI_GB28181_CatalogRefresh_OfflineDevice(t *testing.T) {
	t.Helper()
	h := setupGB28181TestHandler(t)

	ctx := context.Background()
	now := time.Now()

	// Create offline device
	_ = h.db.UpsertGB28181Device(ctx, storage.GB28181Device{
		ID:            "device1",
		Name:          "Test Device",
		Manufacturer:  "Test Manufacturer",
		Model:         "Test Model",
		Status:        "offline",
		LastKeepalive: now,
		RegisteredAt:  now,
	})

	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/gb28181/devices/device1/catalog-refresh", nil, "", "")

	if rr.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d", rr.Code)
	}
}

func TestAPI_GB28181_CatalogRefresh_DeviceNotFound(t *testing.T) {
	t.Helper()
	h := setupGB28181TestHandler(t)

	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/gb28181/devices/nonexistent/catalog-refresh", nil, "", "")

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", rr.Code)
	}
}

func TestAPI_GB28181_CatalogRefresh_MissingDeviceID(t *testing.T) {
	t.Helper()
	h := setupGB28181TestHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/gb28181/devices//catalog-refresh", nil)
	w := httptest.NewRecorder()
	h.handleCatalogRefresh(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}
}

func TestAPI_GB28181_CatalogRefresh_Success(t *testing.T) {
	t.Helper()
	h := setupGB28181TestHandler(t)

	ctx := context.Background()
	now := time.Now()

	// Create online device
	_ = h.db.UpsertGB28181Device(ctx, storage.GB28181Device{
		ID:            "device1",
		Name:          "Test Device",
		Manufacturer:  "Test Manufacturer",
		Model:         "Test Model",
		Status:        "online",
		LastKeepalive: now,
		RegisteredAt:  now,
	})

	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/gb28181/devices/device1/catalog-refresh", nil, "", "")

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d", rr.Code)
	}

	var response map[string]string

	err := json.Unmarshal(rr.Body.Bytes(), &response)
	if err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if response["status"] != "catalog_refresh_requested" {
		t.Fatalf("expected status 'catalog_refresh_requested', got '%s'", response["status"])
	}
}

func TestAPI_GB28181_InviteChannel_MissingChannelID(t *testing.T) {
	t.Helper()
	h := setupGB28181TestHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/gb28181/channels//invite", nil)
	w := httptest.NewRecorder()
	h.handleInviteChannel(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}
}

func TestAPI_GB28181_InviteChannel_ChannelNotFound(t *testing.T) {
	t.Helper()
	h := setupGB28181TestHandler(t)

	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/gb28181/channels/nonexistent/invite", nil, "", "")

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", rr.Code)
	}
}

func TestAPI_GB28181_ByeChannel_MissingChannelID(t *testing.T) {
	t.Helper()
	h := setupGB28181TestHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/gb28181/channels//bye", nil)
	w := httptest.NewRecorder()
	h.handleByeChannel(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}
}

func TestAPI_GB28181_ByeChannel_NoSessionMgr(t *testing.T) {
	t.Helper()
	db, _ := setupTestDB(t)

	h := NewHandler(db, nil, noopAuthMW(), nil, nil, nil, "", nil, nil, nil, nil, nil)

	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/gb28181/channels/test/bye", nil, "", "")

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", rr.Code)
	}
}

func TestAPI_GB28181_ByeChannel_Success(t *testing.T) {
	t.Helper()
	h := setupGB28181TestHandler(t)

	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/gb28181/channels/test/bye", nil, "", "")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var response map[string]string

	err := json.Unmarshal(rr.Body.Bytes(), &response)
	if err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if response["status"] != "bye_sent" {
		t.Fatalf("expected status 'bye_sent', got '%s'", response["status"])
	}
}

// fakePTZSender records the last PTZ send for assertions.
type fakePTZSender struct {
	deviceID string
	body     string
}

func (f *fakePTZSender) SendMessage(deviceID string, body []byte) error {
	f.deviceID = deviceID
	f.body = string(body)
	return nil
}

// setupGB28181PTZHandler returns a handler with a wired PTZ controller and a
// registered online PTZ-capable device + channel.
func setupGB28181PTZHandler(t *testing.T) (*Handler, *fakePTZSender) {
	t.Helper()
	db, _ := setupTestDB(t)

	deviceMgr := gb28181.NewDeviceManager(60 * time.Second)
	sessionMgr := gb28181.NewSessionManager(gb28181.NewPortManager(30000, 30100), "3402000000")
	h := NewHandler(db, nil, noopAuthMW(), nil, nil, nil, "", nil, nil, nil, deviceMgr, sessionMgr)

	dev := &gb28181.Device{ID: "34020000001310000001", Name: "Front Gate", NetAddr: "192.168.1.50:5060"}
	deviceMgr.Register(dev)
	deviceMgr.RegisterChannel(dev.ID, &gb28181.Channel{ID: "34020000001320000001", Name: "Channel 1", Parental: 1, PTZType: 2})

	sender := &fakePTZSender{}
	h.SetGB28181PTZ(gb28181.NewPTZController(deviceMgr, sender))
	return h, sender
}

func TestAPI_GB28181_PTZ_MissingChannelID(t *testing.T) {
	t.Helper()
	h, _ := setupGB28181PTZHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/gb28181/channels//ptz", strings.NewReader(`{"direction":"up"}`))
	w := httptest.NewRecorder()
	h.handlePTZChannel(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}
}

func TestAPI_GB28181_PTZ_NoController(t *testing.T) {
	t.Helper()
	h := setupGB28181TestHandler(t)

	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/gb28181/channels/test/ptz", strings.NewReader(`{"direction":"up"}`), "", "")

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", rr.Code)
	}
}

func TestAPI_GB28181_PTZ_InvalidBody(t *testing.T) {
	t.Helper()
	h, _ := setupGB28181PTZHandler(t)

	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/gb28181/channels/34020000001320000001/ptz", strings.NewReader(`not-json`), "", "")

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rr.Code)
	}
}

func TestAPI_GB28181_PTZ_MissingDirection(t *testing.T) {
	t.Helper()
	h, _ := setupGB28181PTZHandler(t)

	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/gb28181/channels/34020000001320000001/ptz", strings.NewReader(`{}`), "", "")

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rr.Code)
	}
}

func TestAPI_GB28181_PTZ_ChannelNotFound(t *testing.T) {
	t.Helper()
	h, _ := setupGB28181PTZHandler(t)

	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/gb28181/channels/nonexistent/ptz", strings.NewReader(`{"direction":"up"}`), "", "")

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", rr.Code)
	}
}

func TestAPI_GB28181_PTZ_Success(t *testing.T) {
	t.Helper()
	h, sender := setupGB28181PTZHandler(t)

	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/gb28181/channels/34020000001320000001/ptz", strings.NewReader(`{"direction":"up","zoom":5,"preset":2,"speed":32}`), "", "")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var response map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if response["status"] != "ptz_sent" {
		t.Fatalf("expected status 'ptz_sent', got '%s'", response["status"])
	}

	// The controller must have sent the DeviceControl to the device.
	if sender.deviceID != "34020000001310000001" {
		t.Fatalf("expected PTZ sent to device, got '%s'", sender.deviceID)
	}
	if !strings.Contains(sender.body, "<PTZCmd>A5 0F 01 08 00 20 00 DD</PTZCmd>") {
		t.Fatalf("expected PTZCmd in body, got: %s", sender.body)
	}
}

// A channel without PTZ capability (PTZType 0) must get 404 "PTZ not
// supported" — not 400, not 500.
func TestAPI_GB28181_PTZ_NotSupported(t *testing.T) {
	t.Helper()
	h, _ := setupGB28181PTZHandler(t)

	// Register a second channel on the same device with no PTZ capability.
	h.gb28181DeviceMgr.RegisterChannel("34020000001310000001", &gb28181.Channel{ID: "34020000001320000002", Name: "No PTZ", Parental: 1, PTZType: 0})

	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/gb28181/channels/34020000001320000002/ptz", strings.NewReader(`{"direction":"up"}`), "", "")

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "PTZ not supported") {
		t.Fatalf("expected 'PTZ not supported' message, got: %s", rr.Body.String())
	}
}
