package api

// GB28181 device-media handler coverage (#596): RecordInfo records, playback /
// download fetch lifecycle, MANSRTSP control, talk intercom WS + status,
// alarms/positions, and the camera-bound GB PTZ preset CRUD — all against an
// in-test GB28181DeviceMedia fake plus the real device manager + PTZ
// controller (no SIP network).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181/manscdp"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

// fakeGBMedia implements GB28181DeviceMedia with call flags for assertions.
type fakeGBMedia struct {
	mu sync.Mutex

	records    []manscdp.RecordItem
	queryErr   error
	queried    bool
	startErr   error
	started    bool
	downloadEr error
	downloaded bool
	stopErr    error
	stopped    bool
	controlErr error
	controlled []string

	startTalkErr error
	stopTalkErr  error
	talkActive   bool
	talkWritten  [][]byte

	playback *gb28181.PlaybackInfo

	alarms    []event.GB28181AlarmEvent
	positions []gb28181.GBPosition
}

func (f *fakeGBMedia) QueryChannelRecords(_, _ string, _, _ time.Time) ([]manscdp.RecordItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queried = true
	return f.records, f.queryErr
}

func (f *fakeGBMedia) StartPlayback(_, _ string, _, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = true
	return f.startErr
}

func (f *fakeGBMedia) StartDownload(_, _ string, _, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.downloaded = true
	return f.downloadEr
}

func (f *fakeGBMedia) StopPlayback(string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
	return f.stopErr
}

func (f *fakeGBMedia) PlaybackStatusFor(channelID string) (gb28181.PlaybackInfo, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.playback == nil {
		return gb28181.PlaybackInfo{}, false
	}
	return *f.playback, true
}

func (f *fakeGBMedia) PlaybackControl(_ string, action string, _ float64, _ float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.controlled = append(f.controlled, action)
	return f.controlErr
}

func (f *fakeGBMedia) StartTalk(_, _ string, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startTalkErr != nil {
		return f.startTalkErr
	}
	f.talkActive = true
	return nil
}

func (f *fakeGBMedia) StopTalk(string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.talkActive = false
	return f.stopTalkErr
}

func (f *fakeGBMedia) WriteTalkAudio(_ string, alaw []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.talkWritten = append(f.talkWritten, append([]byte(nil), alaw...))
}

func (f *fakeGBMedia) TalkStatusFor(cameraID string) gb28181.TalkStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return gb28181.TalkStatus{Active: f.talkActive, CameraID: cameraID, Packets: int64(len(f.talkWritten))}
}

func (f *fakeGBMedia) GB28181Alarms(string) []event.GB28181AlarmEvent { return f.alarms }

func (f *fakeGBMedia) GB28181Positions(string) []gb28181.GBPosition { return f.positions }

func (f *fakeGBMedia) wasStarted() bool    { f.mu.Lock(); defer f.mu.Unlock(); return f.started }
func (f *fakeGBMedia) wasDownloaded() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.downloaded }
func (f *fakeGBMedia) wasStopped() bool    { f.mu.Lock(); defer f.mu.Unlock(); return f.stopped }
func (f *fakeGBMedia) talkPackets() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.talkWritten...)
}

func (f *fakeGBMedia) controlledActions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.controlled...)
}

const (
	gbMediaDeviceID        = "34020000001310000001"
	gbMediaChannelID       = "34020000001320000001"
	gbMediaZoomOnlyChannel = "34020000001320000002"
)

// newGBMediaEnv wires a Handler with the media fake, a registered online
// PTZ-capable device/channel, and a real CameraManager holding a bound
// gb28181 camera (plus a pan/tilt-only channel bound to a second camera).
func newGBMediaEnv(t *testing.T) (*Handler, *fakeGBMedia) {
	t.Helper()
	db, store := setupTestDB(t)
	t.Cleanup(func() { _ = db.Close() })

	deviceMgr := gb28181.NewDeviceManager(60 * time.Second)
	sessionMgr := gb28181.NewSessionManager(gb28181.NewPortManager(30000, 30100), "3402000000")
	h := NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil, deviceMgr, sessionMgr)
	t.Cleanup(h.Close)

	media := &fakeGBMedia{}
	h.SetGB28181DeviceMedia(media)
	// Deterministic timezone for naive device timestamps.
	h.SetGB28181Timezone(time.UTC)

	dev := &gb28181.Device{ID: gbMediaDeviceID, Name: "Front Gate", NetAddr: "192.168.1.50:5060"}
	deviceMgr.Register(dev)
	deviceMgr.RegisterChannel(dev.ID, &gb28181.Channel{ID: gbMediaChannelID, Name: "Ch1", Parental: 1, PTZType: 2})
	deviceMgr.RegisterChannel(dev.ID, &gb28181.Channel{ID: gbMediaZoomOnlyChannel, Name: "Ch2", Parental: 1, PTZType: 1})

	cfg := &config.Config{Cameras: []config.CameraConfig{
		{
			ID: "gb-cam", Name: "GB Cam", Protocol: "gb28181", Encoding: "h264",
			GB28181: config.GB28181ChannelConfig{DeviceID: gbMediaDeviceID, ChannelID: gbMediaChannelID},
		},
		{
			ID: "gb-pancam", Name: "Pan Cam", Protocol: "gb28181", Encoding: "h264",
			GB28181: config.GB28181ChannelConfig{DeviceID: gbMediaDeviceID, ChannelID: gbMediaZoomOnlyChannel},
		},
		{ID: "gb-unbound", Name: "Unbound", Protocol: "gb28181", Encoding: "h264"},
	}}
	cm := camera.NewCameraManager(cfg, store, db, filepath.Join(t.TempDir(), "cameras.yaml"))
	h.camMgr = cm
	h.SetGB28181PTZ(gb28181.NewPTZController(deviceMgr, &fakePTZSender{}))

	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "gb-cam", "GB Cam", "gb28181", "h264", "", "", "", "", "", "", ""))
	require.NoError(t, db.UpsertCamera(ctx, "gb-pancam", "Pan Cam", "gb28181", "h264", "", "", "", "", "", "", ""))
	require.NoError(t, db.UpsertCamera(ctx, "gb-unbound", "Unbound", "gb28181", "h264", "", "", "", "", "", "", ""))
	return h, media
}

func gbDo(t *testing.T, h *Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	return w
}

func TestGBChannelRecords(t *testing.T) {
	t.Parallel()
	h, media := newGBMediaEnv(t)

	media.records = []manscdp.RecordItem{
		{Name: "rec1", FilePath: "/dvr/1.mp4", StartTime: "2025-06-01T10:00:00", EndTime: "2025-06-01T10:10:00"},
		{Name: "rec2", StartTime: "2025-06-01 11:00:00", EndTime: "2025-06-01 11:05:00"}, // space separator
		{Name: "bad", StartTime: "not-a-time", EndTime: "2025-06-01T12:00:00"},           // dropped
	}
	w := gbDo(t, h, http.MethodGet, "/api/gb28181/channels/"+gbMediaChannelID+"/records?start=2025-06-01T00:00:00Z&end=2025-06-02T00:00:00Z", "")
	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Count   int `json:"count"`
		Records []struct {
			Name      string `json:"name"`
			StartTime string `json:"start_time"`
		} `json:"records"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, 2, resp.Count, "malformed device entries must be dropped, not fatal")
	require.Equal(t, "2025-06-01T10:00:00Z", resp.Records[0].StartTime, "naive device time interpreted in the configured GB timezone")

	// Validation matrix.
	base := "/api/gb28181/channels/" + gbMediaChannelID + "/records"
	require.Equal(t, http.StatusBadRequest, gbDo(t, h, http.MethodGet, base, "").Code)
	require.Equal(t, http.StatusBadRequest, gbDo(t, h, http.MethodGet, base+"?start=xx&end=2025-06-02T00:00:00Z", "").Code)
	require.Equal(t, http.StatusBadRequest, gbDo(t, h, http.MethodGet, base+"?start=2025-06-01T00:00:00Z&end=yy", "").Code)
	require.Equal(t, http.StatusBadRequest, gbDo(t, h, http.MethodGet, base+"?start=2025-06-02T00:00:00Z&end=2025-06-01T00:00:00Z", "").Code)
	require.Equal(t, http.StatusBadRequest, gbDo(t, h, http.MethodGet, base+"?start=2025-06-01T00:00:00Z&end=2025-06-20T00:00:00Z", "").Code)
	// Unknown channel and query failure.
	require.Equal(t, http.StatusNotFound, gbDo(t, h, http.MethodGet, "/api/gb28181/channels/999/records?start=2025-06-01T00:00:00Z&end=2025-06-02T00:00:00Z", "").Code)
	media.queryErr = errors.New("device busy")
	w = gbDo(t, h, http.MethodGet, base+"?start=2025-06-01T00:00:00Z&end=2025-06-02T00:00:00Z", "")
	require.Equal(t, http.StatusBadGateway, w.Code)
}

func TestGBChannelPlaybackLifecycle(t *testing.T) {
	t.Parallel()
	h, media := newGBMediaEnv(t)

	const path = "/api/gb28181/channels/" + gbMediaChannelID + "/playback"
	const body = `{"start":"2025-06-01T10:00:00Z","end":"2025-06-01T11:00:00Z"}`

	// Start: accepted.
	require.Equal(t, http.StatusAccepted, gbDo(t, h, http.MethodPost, path, body).Code)
	require.True(t, media.wasStarted())

	// Start validation/errors.
	require.Equal(t, http.StatusBadRequest, gbDo(t, h, http.MethodPost, path, "not json").Code)
	require.Equal(t, http.StatusBadRequest, gbDo(t, h, http.MethodPost, path, `{"start":"xx","end":"2025-06-01T11:00:00Z"}`).Code)
	require.Equal(t, http.StatusBadRequest, gbDo(t, h, http.MethodPost, path, `{"start":"2025-06-01T10:00:00Z","end":"2025-06-01T09:00:00Z"}`).Code)
	require.Equal(t, http.StatusBadRequest, gbDo(t, h, http.MethodPost, path, `{"start":"2025-06-01T10:00:00Z","end":"2025-06-03T10:00:00Z"}`).Code) // >24h
	require.Equal(t, http.StatusNotFound, gbDo(t, h, http.MethodPost, "/api/gb28181/channels/999/playback", body).Code)
	media.startErr = errors.New("invite timeout")
	require.Equal(t, http.StatusBadGateway, gbDo(t, h, http.MethodPost, path, body).Code)
	media.startErr = nil

	// Status: idle then active.
	w := gbDo(t, h, http.MethodGet, path, "")
	require.Equal(t, http.StatusOK, w.Code)
	var idle struct {
		Active bool `json:"active"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &idle))
	require.False(t, idle.Active)

	media.playback = &gb28181.PlaybackInfo{Active: true, Kind: "playback", ChannelID: gbMediaChannelID, Frames: 42, Paused: true, Scale: 2}
	w = gbDo(t, h, http.MethodGet, path, "")
	require.Equal(t, http.StatusOK, w.Code)
	var active struct {
		Active bool    `json:"active"`
		Frames int64   `json:"frames"`
		Paused bool    `json:"paused"`
		Scale  float64 `json:"scale"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &active))
	require.True(t, active.Active)
	require.EqualValues(t, 42, active.Frames)
	require.True(t, active.Paused)
	require.Equal(t, 2.0, active.Scale)

	// Control: happy + validation + conflict.
	require.Equal(t, http.StatusOK, gbDo(t, h, http.MethodPost, path+"/control", `{"action":"pause"}`).Code)
	require.Equal(t, http.StatusOK, gbDo(t, h, http.MethodPost, path+"/control", `{"action":"seek","position":60}`).Code)
	require.Equal(t, []string{"pause", "seek"}, media.controlledActions())
	require.Equal(t, http.StatusBadRequest, gbDo(t, h, http.MethodPost, path+"/control", "not json").Code)
	require.Equal(t, http.StatusBadRequest, gbDo(t, h, http.MethodPost, path+"/control", `{"action":"fastforward"}`).Code)
	media.controlErr = errors.New("not playing")
	require.Equal(t, http.StatusConflict, gbDo(t, h, http.MethodPost, path+"/control", `{"action":"resume"}`).Code)
	media.controlErr = nil

	// Stop: happy + error.
	require.Equal(t, http.StatusOK, gbDo(t, h, http.MethodDelete, path, "").Code)
	require.True(t, media.wasStopped())
	media.stopErr = errors.New("bye failed")
	require.Equal(t, http.StatusInternalServerError, gbDo(t, h, http.MethodDelete, path, "").Code)
}

func TestGBChannelDownloadStart(t *testing.T) {
	t.Parallel()
	h, media := newGBMediaEnv(t)

	const path = "/api/gb28181/channels/" + gbMediaChannelID + "/download"
	require.Equal(t, http.StatusAccepted, gbDo(t, h, http.MethodPost, path, `{"start":"2025-06-01T10:00:00Z","end":"2025-06-05T10:00:00Z"}`).Code)
	require.True(t, media.wasDownloaded())

	// Downloads span up to 7 days; a 8-day range is refused.
	require.Equal(t, http.StatusBadRequest, gbDo(t, h, http.MethodPost, path, `{"start":"2025-06-01T10:00:00Z","end":"2025-06-09T10:00:00Z"}`).Code)
	require.Equal(t, http.StatusBadRequest, gbDo(t, h, http.MethodPost, path, "not json").Code)
	require.Equal(t, http.StatusBadRequest, gbDo(t, h, http.MethodPost, path, `{"start":"2025-06-01T10:00:00Z","end":"bad"}`).Code)
	require.Equal(t, http.StatusNotFound, gbDo(t, h, http.MethodPost, "/api/gb28181/channels/999/download", `{"start":"2025-06-01T10:00:00Z","end":"2025-06-01T11:00:00Z"}`).Code)
	media.downloadEr = errors.New("device refused")
	require.Equal(t, http.StatusBadGateway, gbDo(t, h, http.MethodPost, path, `{"start":"2025-06-01T10:00:00Z","end":"2025-06-01T11:00:00Z"}`).Code)
}

func TestGBTalkStatusAlarmsPositions(t *testing.T) {
	t.Parallel()
	h, media := newGBMediaEnv(t)

	media.alarms = []event.GB28181AlarmEvent{{DeviceID: gbMediaDeviceID}}
	media.positions = []gb28181.GBPosition{{DeviceID: gbMediaDeviceID, Longitude: "116.4", Latitude: "39.9"}}

	w := gbDo(t, h, http.MethodGet, "/api/cameras/gb-cam/gb28181/talk/status", "")
	require.Equal(t, http.StatusOK, w.Code)
	var talk struct {
		Active bool `json:"active"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &talk))
	require.False(t, talk.Active)

	media.mu.Lock()
	media.talkActive = true
	media.mu.Unlock()
	w = gbDo(t, h, http.MethodGet, "/api/cameras/gb-cam/gb28181/talk/status", "")
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &talk))
	require.True(t, talk.Active)

	w = gbDo(t, h, http.MethodGet, "/api/gb28181/devices/"+gbMediaDeviceID+"/alarms", "")
	require.Equal(t, http.StatusOK, w.Code)
	require.NotEmpty(t, w.Body.String())

	w = gbDo(t, h, http.MethodGet, "/api/gb28181/devices/"+gbMediaDeviceID+"/positions", "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "116.4")

	// Nil-media defaults (before wiring): talk inactive, empty alarms/positions.
	h2, _ := newGBMediaEnv(t)
	h2.gb28181Media = nil
	w = gbDo(t, h2, http.MethodGet, "/api/cameras/gb-cam/gb28181/talk/status", "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"active":false`)
	require.Equal(t, http.StatusOK, gbDo(t, h2, http.MethodGet, "/api/gb28181/devices/"+gbMediaDeviceID+"/alarms", "").Code)
	require.Equal(t, http.StatusOK, gbDo(t, h2, http.MethodGet, "/api/gb28181/devices/"+gbMediaDeviceID+"/positions", "").Code)
}

// TestGBTalkWS drives the intercom WebSocket: binary frames are forwarded as
// G.711 audio, text frames ignored, and closing the socket BYEs the session.
func TestGBTalkWS(t *testing.T) {
	t.Parallel()
	h, media := newGBMediaEnv(t)

	srv := httptest.NewServer(h.Routes())
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/cameras/gb-cam/gb28181/talk"

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	require.NoError(t, conn.WriteMessage(websocket.BinaryMessage, []byte{0x01, 0x55, 0x7f}))
	require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte("ignored")))
	require.NoError(t, conn.Close())

	require.Eventually(t, func() bool {
		pkts := media.talkPackets()
		return len(pkts) == 1 && pkts[0][0] == 0x01
	}, 5*time.Second, 50*time.Millisecond, "binary audio frames must reach WriteTalkAudio exactly once")

	// Setup failure before upgrade ⇒ 502 with context.
	media.mu.Lock()
	media.startTalkErr = errors.New("invite rejected")
	media.mu.Unlock()
	require.Equal(t, http.StatusBadGateway, gbDo(t, h, http.MethodGet, "/api/cameras/gb-cam/gb28181/talk", "").Code)

	// Camera without GB binding ⇒ 400.
	require.Equal(t, http.StatusBadRequest, gbDo(t, h, http.MethodGet, "/api/cameras/gb-unbound/gb28181/talk", "").Code)
	// Unknown camera (nil config) ⇒ 400.
	require.Equal(t, http.StatusBadRequest, gbDo(t, h, http.MethodGet, "/api/cameras/nope/gb28181/talk", "").Code)
}

func TestGBPTZPresetsCRUD(t *testing.T) {
	t.Parallel()
	h, _ := newGBMediaEnv(t)

	// Create: token allocated from the DB, SET sent best-effort.
	w := gbDo(t, h, http.MethodPost, "/api/cameras/gb-cam/ptz/presets", `{"name":"gate"}`)
	require.Equal(t, http.StatusOK, w.Code)
	var created struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	require.Equal(t, "1", created.Token)

	// List: the created preset appears.
	w = gbDo(t, h, http.MethodGet, "/api/cameras/gb-cam/ptz/presets", "")
	require.Equal(t, http.StatusOK, w.Code)
	var listResp struct {
		Presets []struct {
			Token string `json:"token"`
			Name  string `json:"name"`
		} `json:"presets"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &listResp))
	require.Len(t, listResp.Presets, 1)
	require.Equal(t, "gate", listResp.Presets[0].Name)

	// Goto + delete round-trip.
	require.Equal(t, http.StatusOK, gbDo(t, h, http.MethodPost, "/api/cameras/gb-cam/ptz/presets/1/goto", "").Code)
	require.Equal(t, http.StatusOK, gbDo(t, h, http.MethodDelete, "/api/cameras/gb-cam/ptz/presets/1", "").Code)
	w = gbDo(t, h, http.MethodGet, "/api/cameras/gb-cam/ptz/presets", "")
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &listResp))
	require.Empty(t, listResp.Presets)

	// Validation: name required, unbound camera, bad tokens.
	require.Equal(t, http.StatusBadRequest, gbDo(t, h, http.MethodPost, "/api/cameras/gb-cam/ptz/presets", `{"name":""}`).Code)
	require.Equal(t, http.StatusBadRequest, gbDo(t, h, http.MethodPost, "/api/cameras/gb-unbound/ptz/presets", `{"name":"x"}`).Code)
	require.Equal(t, http.StatusBadRequest, gbDo(t, h, http.MethodPost, "/api/cameras/gb-cam/ptz/presets/0/goto", "").Code)
	require.Equal(t, http.StatusBadRequest, gbDo(t, h, http.MethodPost, "/api/cameras/gb-cam/ptz/presets/256/goto", "").Code)
	require.Equal(t, http.StatusBadRequest, gbDo(t, h, http.MethodPost, "/api/cameras/gb-cam/ptz/presets/abc/goto", "").Code)
	require.Equal(t, http.StatusBadRequest, gbDo(t, h, http.MethodDelete, "/api/cameras/gb-cam/ptz/presets/abc", "").Code)
}

// TestGBPTZMoveDirections: the ONVIF-style continuous-move endpoint maps onto
// GB direction bits for gb28181 cameras (one axis at a time).
func TestGBPTZMoveDirections(t *testing.T) {
	t.Parallel()
	h, _ := newGBMediaEnv(t)

	move := func(body string) int {
		return gbDo(t, h, http.MethodPost, "/api/cameras/gb-cam/ptz/move", body).Code
	}
	require.Equal(t, http.StatusOK, move(`{"mode":"continuous","zoom":0.5}`))
	require.Equal(t, http.StatusOK, move(`{"mode":"continuous","zoom":-0.5}`))
	require.Equal(t, http.StatusOK, move(`{"mode":"continuous","tilt":0.5}`))
	require.Equal(t, http.StatusOK, move(`{"mode":"continuous","tilt":-0.5}`))
	require.Equal(t, http.StatusOK, move(`{"mode":"continuous","pan":0.5}`))
	require.Equal(t, http.StatusOK, move(`{"mode":"continuous","pan":-0.5}`))
	require.Equal(t, http.StatusOK, move(`{"mode":"continuous"}`)) // zero vector ⇒ stop
	require.Equal(t, http.StatusOK, gbDo(t, h, http.MethodPost, "/api/cameras/gb-cam/ptz/stop", "").Code)

	// Pan/tilt-only channel (PTZType=1): zoom is refused with ErrZoomUnsupported ⇒ 404.
	require.Equal(t, http.StatusNotFound, gbDo(t, h, http.MethodPost, "/api/cameras/gb-pancam/ptz/move", `{"mode":"continuous","zoom":0.5}`).Code)
	require.Equal(t, http.StatusOK, gbDo(t, h, http.MethodPost, "/api/cameras/gb-pancam/ptz/move", `{"mode":"continuous","pan":0.5}`).Code)

	// Guards: unbound camera ⇒ 400; no PTZ controller ⇒ 503.
	require.Equal(t, http.StatusBadRequest, gbDo(t, h, http.MethodPost, "/api/cameras/gb-unbound/ptz/move", `{"mode":"continuous","pan":1}`).Code)
	h2, _ := newGBMediaEnv(t)
	h2.gb28181PTZ = nil
	require.Equal(t, http.StatusServiceUnavailable, gbDo(t, h2, http.MethodPost, "/api/cameras/gb-cam/ptz/move", `{"mode":"continuous","pan":1}`).Code)
	require.Equal(t, http.StatusServiceUnavailable, gbDo(t, h2, http.MethodPost, "/api/cameras/gb-cam/ptz/stop", "").Code)
}
