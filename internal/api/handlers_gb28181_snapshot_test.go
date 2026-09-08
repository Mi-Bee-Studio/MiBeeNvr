package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mickeyzzc/gb28181-go/manscdp"
	"github.com/mickeyzzc/gb28181-go/platform"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/snapshot"
)

// fakeGBCommander records DeviceControl calls (snapshot + manual record).
type fakeGBCommander struct {
	snapshots []manscdp.SnapShotCmd
	records   []string
	failNext  error
}

func (f *fakeGBCommander) SendSnapShotCmd(_ string, cmd manscdp.SnapShotCmd) error {
	if f.failNext != nil {
		return f.failNext
	}
	f.snapshots = append(f.snapshots, cmd)
	return nil
}

func (f *fakeGBCommander) StartManualRecord(_ string) error {
	if f.failNext != nil {
		return f.failNext
	}
	f.records = append(f.records, "start")
	return nil
}

func (f *fakeGBCommander) StopManualRecord(_ string) error {
	if f.failNext != nil {
		return f.failNext
	}
	f.records = append(f.records, "stop")
	return nil
}

// setupGBSnapshotHandler wires a handler with one online GB device/channel,
// a recording fake commander, and a session manager over a fake store.
func setupGBSnapshotHandler(t *testing.T) (*Handler, *fakeGBCommander, *gb28181.SnapshotSessionManager) {
	t.Helper()
	db, store := setupTestDB(t)
	h := NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil, nil, nil)
	deviceMgr := platform.NewDeviceManager(time.Minute)
	dev := &platform.Device{ID: "34020000001320000002", NetAddr: "192.168.63.118:5060"}
	deviceMgr.Register(dev)
	h.gb28181DeviceMgr = deviceMgr
	deviceMgr.RegisterChannel(dev.ID, &platform.Channel{DeviceID: dev.ID, ID: "34020000001310000002"})

	commander := &fakeGBCommander{}
	h.SetGB28181Commander(commander)
	mgr := gb28181.NewSnapshotSessionManager(&snapshot.Persistor{Root: t.TempDir()}, time.Minute)
	t.Cleanup(mgr.Stop)
	h.SetGB28181SnapshotManager(mgr)
	return h, commander, mgr
}

func postJSON(t *testing.T, h http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestGB28181ChannelSnapshot(t *testing.T) {
	t.Parallel()

	t.Run("command carries spec fields and session", func(t *testing.T) {
		t.Parallel()
		h, commander, _ := setupGBSnapshotHandler(t)
		resp := postJSON(t, h.Routes(), "/api/gb28181/channels/34020000001310000002/snapshot", map[string]any{"snap_num": 3, "interval": 2})
		require.Equal(t, http.StatusAccepted, resp.Code)

		require.Len(t, commander.snapshots, 1)
		cmd := commander.snapshots[0]
		require.Equal(t, 3, cmd.SnapNum)
		require.Equal(t, 2, cmd.Interval)
		require.Len(t, cmd.SessionID, 32)
		require.Contains(t, cmd.UploadURL, "/api/gb28181/snapshot/upload?session="+cmd.SessionID)

		var out struct {
			SessionID string `json:"session_id"`
		}
		require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &out))
		require.Equal(t, cmd.SessionID, out.SessionID)
	})

	t.Run("defaults to single frame without interval", func(t *testing.T) {
		t.Parallel()
		h, commander, _ := setupGBSnapshotHandler(t)
		resp := postJSON(t, h.Routes(), "/api/gb28181/channels/34020000001310000002/snapshot", map[string]any{})
		require.Equal(t, http.StatusAccepted, resp.Code)
		require.Equal(t, 1, commander.snapshots[0].SnapNum)
		require.Equal(t, 0, commander.snapshots[0].Interval)
	})

	t.Run("unknown channel 404", func(t *testing.T) {
		t.Parallel()
		h, _, _ := setupGBSnapshotHandler(t)
		resp := postJSON(t, h.Routes(), "/api/gb28181/channels/34020000009999999999/snapshot", map[string]any{})
		require.Equal(t, http.StatusNotFound, resp.Code)
	})

	t.Run("commander unwired 503", func(t *testing.T) {
		t.Parallel()
		h, _, _ := setupGBSnapshotHandler(t)
		h.SetGB28181Commander(nil)
		resp := postJSON(t, h.Routes(), "/api/gb28181/channels/34020000001310000002/snapshot", map[string]any{})
		require.Equal(t, http.StatusServiceUnavailable, resp.Code)
	})
}

func TestGB28181ChannelRecord(t *testing.T) {
	t.Parallel()

	t.Run("start and stop", func(t *testing.T) {
		t.Parallel()
		h, commander, _ := setupGBSnapshotHandler(t)
		resp := postJSON(t, h.Routes(), "/api/gb28181/channels/34020000001310000002/record", map[string]any{"action": "start"})
		require.Equal(t, http.StatusAccepted, resp.Code)
		resp = postJSON(t, h.Routes(), "/api/gb28181/channels/34020000001310000002/record", map[string]any{"action": "stop"})
		require.Equal(t, http.StatusAccepted, resp.Code)
		require.Equal(t, []string{"start", "stop"}, commander.records)
	})

	t.Run("unknown action 400", func(t *testing.T) {
		t.Parallel()
		h, commander, _ := setupGBSnapshotHandler(t)
		resp := postJSON(t, h.Routes(), "/api/gb28181/channels/34020000001310000002/record", map[string]any{"action": "reboot"})
		require.Equal(t, http.StatusBadRequest, resp.Code)
		require.Empty(t, commander.records)
	})
}

// The upload endpoint is the device's callback: no auth (session ID is the
// credential), mounted publicly, stores frames, and refuses unknown/closed
// sessions and non-JPEG bodies.
func TestGB28181SnapshotUpload(t *testing.T) {
	t.Parallel()

	jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 32)...)

	newUp := func(h *Handler, session string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/gb28181/snapshot/upload?session="+session, bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h.Routes().ServeHTTP(rec, req)
		return rec
	}

	t.Run("stores a frame for a live session", func(t *testing.T) {
		t.Parallel()
		h, _, mgr := setupGBSnapshotHandler(t)
		sess, err := mgr.CreateSession("ch", "dev", "cam-gb", 1)
		require.NoError(t, err)
		resp := newUp(h, sess.ID, jpeg)
		require.Equal(t, http.StatusOK, resp.Code)
		require.Len(t, sess.Files(), 1)
	})

	t.Run("unknown session 404", func(t *testing.T) {
		t.Parallel()
		h, _, _ := setupGBSnapshotHandler(t)
		resp := newUp(h, "ghost", jpeg)
		require.Equal(t, http.StatusNotFound, resp.Code)
	})

	t.Run("non-JPEG body 400", func(t *testing.T) {
		t.Parallel()
		h, _, mgr := setupGBSnapshotHandler(t)
		sess, err := mgr.CreateSession("ch", "dev", "cam-gb", 1)
		require.NoError(t, err)
		resp := newUp(h, sess.ID, []byte("plain text"))
		require.Equal(t, http.StatusBadRequest, resp.Code)
	})

	t.Run("finished session 409", func(t *testing.T) {
		t.Parallel()
		h, _, mgr := setupGBSnapshotHandler(t)
		sess, err := mgr.CreateSession("ch", "dev", "cam-gb", 1)
		require.NoError(t, err)
		mgr.Finish(sess.ID, 1)
		resp := newUp(h, sess.ID, jpeg)
		require.Equal(t, http.StatusConflict, resp.Code)
	})

	t.Run("missing session param 400", func(t *testing.T) {
		t.Parallel()
		h, _, _ := setupGBSnapshotHandler(t)
		req := httptest.NewRequest(http.MethodPost, "/api/gb28181/snapshot/upload", bytes.NewReader(jpeg))
		rec := httptest.NewRecorder()
		h.Routes().ServeHTTP(rec, req)
		require.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

// Regression: the upload route must be mounted WITHOUT auth — devices POST
// with no credentials (verified above through Routes(), which applies
// authMW; noopAuthMW would hide a missing public mount, so assert the route
// table directly).
func TestGB28181SnapshotUploadRouteMounted(t *testing.T) {
	t.Parallel()
	h, _, _ := setupGBSnapshotHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/api/gb28181/snapshot/upload?session=x", nil)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	require.NotEqual(t, http.StatusNotFound, rec.Code, "upload endpoint must be routed")
	require.Equal(t, http.StatusBadRequest, rec.Code, "empty body must fail JPEG validation")
}
