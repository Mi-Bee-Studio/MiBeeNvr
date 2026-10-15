package dlna

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

type dlnaFixture struct {
	srv   *Server
	db    *storage.DB
	store *storage.Manager
}

func setupDLNA(t *testing.T) *dlnaFixture {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.New(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	store, err := storage.NewManager(filepath.Join(dir, "storage"))
	require.NoError(t, err)

	require.NoError(t, db.UpsertCamera(ctx, "cam-a", "Front Door", "rtsp", "h264", "rtsp://x", "", "", "", "", "", ""))
	require.NoError(t, db.UpsertCamera(ctx, "cam-b", "Room Mic", "rtsp", "audio", "rtsp://y", "", "", "", "", "", ""))

	// Two video recordings on 2026-10-15 local, one on 2026-10-14.
	rel := filepath.ToSlash(filepath.Join("cam-a", "202610", "15", "20", "seg.mp4"))
	abs := filepath.Join(store.RootDir(), filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
	require.NoError(t, os.WriteFile(abs, bytes.Repeat([]byte{0xAB}, 1024), 0o644))

	day := time.Date(2026, 10, 15, 12, 0, 0, 0, time.Local)
	prev := day.AddDate(0, 0, -1)
	insert := func(id string, camID, format string, at time.Time) {
		require.NoError(t, db.InsertRecording(ctx, &model.Recording{
			ID:        id,
			CameraID:  camID,
			FilePath:  rel,
			Format:    model.Format(format),
			StartedAt: at,
			EndedAt:   at.Add(60 * time.Second),
			Duration:  60,
			FileSize:  1024,
		}))
	}
	insert("rec-1", "cam-a", "h264", day)
	insert("rec-2", "cam-a", "h264", day.Add(2*time.Hour))
	insert("rec-3", "cam-a", "h264", prev)
	insert("rec-4", "cam-b", "audio", day)

	srv := NewServer(ServerConfig{Port: 0, FriendlyName: "Test NVR"}, db, store, "test-machine-key")
	return &dlnaFixture{srv: srv, db: db, store: store}
}

func doBrowse(t *testing.T, h http.Handler, objectID, flag string, start, count int) *httptest.ResponseRecorder {
	t.Helper()
	body := `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
  <s:Body>
    <u:Browse xmlns:u="urn:schemas-upnp-org:service:ContentDirectory:1">
      <ObjectID>` + objectID + `</ObjectID>
      <BrowseFlag>` + flag + `</BrowseFlag>
      <Filter>*</Filter>
      <StartingIndex>` + strconv.Itoa(start) + `</StartingIndex>
      <RequestedCount>` + strconv.Itoa(count) + `</RequestedCount>
      <SortCriteria></SortCriteria>
    </u:Browse>
  </s:Body>
</s:Envelope>`
	req := httptest.NewRequest(http.MethodPost, "/dlna/ContentDirectory/control", strings.NewReader(body))
	req.Host = "192.0.2.7:49152"
	req.Header.Set("SOAPAction", `"urn:schemas-upnp-org:service:ContentDirectory:1#Browse"`)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestHandler_DeviceXML(t *testing.T) {
	f := setupDLNA(t)
	req := httptest.NewRequest(http.MethodGet, "/dlna/device.xml", nil)
	req.Host = "192.0.2.10:49152"
	rr := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()
	require.Contains(t, body, "Test NVR")
	require.Contains(t, body, "<URLBase>http://192.0.2.10:49152</URLBase>")
	require.Contains(t, body, "urn:schemas-upnp-org:device:MediaServer:1")
}

func TestHandler_BrowseRootListsCameras(t *testing.T) {
	f := setupDLNA(t)
	rr := doBrowse(t, f.srv.Handler(), "0", "BrowseDirectChildren", 0, 0)
	require.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()
	require.Contains(t, body, "Front Door")
	require.Contains(t, body, "Room Mic")
	require.Contains(t, body, "<NumberReturned>2</NumberReturned>")
	require.Contains(t, body, "<TotalMatches>2</TotalMatches>")
}

func TestHandler_BrowseCameraDaysAndItems(t *testing.T) {
	f := setupDLNA(t)
	h := f.srv.Handler()

	// Camera → day containers (2 days, newest first).
	rr := doBrowse(t, h, "Acam-a", "BrowseDirectChildren", 0, 0)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "Acam-aD20261015")
	require.Contains(t, rr.Body.String(), "Acam-aD20261014")

	// Day → items with absolute res URLs against the request host.
	dayID := "Acam-aD20261015"
	rr = doBrowse(t, h, dayID, "BrowseDirectChildren", 0, 0)
	require.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()
	require.Contains(t, body, "rec-1")
	require.Contains(t, body, "rec-2")
	require.Contains(t, body, "http://192.0.2.7:49152/dlna/item/rec-1")
	require.Contains(t, body, "http-get:*:video/mp4:*")

	// Audio source items advertise audio/mp4 + audioItem class.
	rr = doBrowse(t, h, "Acam-bD20261015", "BrowseDirectChildren", 0, 0)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "audioItem.musicTrack")
	require.Contains(t, rr.Body.String(), "http-get:*:audio/mp4:*")
}

func TestHandler_BrowseMetadataItem(t *testing.T) {
	f := setupDLNA(t)
	rr := doBrowse(t, f.srv.Handler(), "Rrec-1", "BrowseMetadata", 0, 0)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "rec-1")
	require.Contains(t, rr.Body.String(), `<NumberReturned>1</NumberReturned>`)
}

func TestHandler_BrowsePagination(t *testing.T) {
	f := setupDLNA(t)
	rr := doBrowse(t, f.srv.Handler(), "Acam-aD20261015", "BrowseDirectChildren", 0, 1)
	require.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()
	require.Contains(t, body, "<NumberReturned>1</NumberReturned>")
	require.Contains(t, body, "<TotalMatches>2</TotalMatches>")
	require.Contains(t, body, "rec-1")
	require.NotContains(t, body, "rec-2")
}

func TestHandler_BrowseNoSuchObject(t *testing.T) {
	f := setupDLNA(t)
	rr := doBrowse(t, f.srv.Handler(), "Rnope", "BrowseMetadata", 0, 0)
	require.Contains(t, rr.Body.String(), "<errorCode>701</errorCode>")
}

func TestHandler_ItemServesRange(t *testing.T) {
	f := setupDLNA(t)
	h := f.srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/dlna/item/rec-1", nil)
	req.Header.Set("Range", "bytes=0-3")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusPartialContent, rr.Code)
	require.Equal(t, "video/mp4", rr.Header().Get("Content-Type"))
	require.Equal(t, 4, rr.Body.Len())

	// Unknown recording → 404.
	req = httptest.NewRequest(http.MethodGet, "/dlna/item/does-not-exist", nil)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusNotFound, rr.Code)

	// Audio recordings serve as audio/mp4.
	req = httptest.NewRequest(http.MethodGet, "/dlna/item/rec-4", nil)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "audio/mp4", rr.Header().Get("Content-Type"))
}

func TestHandler_ConnectionManager(t *testing.T) {
	f := setupDLNA(t)
	h := f.srv.Handler()

	req := httptest.NewRequest(http.MethodPost, "/dlna/ConnectionManager/control", strings.NewReader(""))
	req.Header.Set("SOAPAction", `"urn:schemas-upnp-org:service:ConnectionManager:1#GetProtocolInfo"`)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "GetProtocolInfoResponse")

	req = httptest.NewRequest(http.MethodPost, "/dlna/ConnectionManager/control", strings.NewReader(""))
	req.Header.Set("SOAPAction", `"urn:schemas-upnp-org:service:ConnectionManager:1#Bogus"`)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Contains(t, rr.Body.String(), "<errorCode>401</errorCode>")
}

func TestHandler_SCPDsAndGENA(t *testing.T) {
	f := setupDLNA(t)
	h := f.srv.Handler()

	for path, marker := range map[string]string{
		"/dlna/ContentDirectory/scpd.xml":  "<name>Browse</name>",
		"/dlna/ConnectionManager/scpd.xml": "<name>GetProtocolInfo</name>",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code, path)
		require.Contains(t, rr.Body.String(), marker, path)
	}

	req := httptest.NewRequest("SUBSCRIBE", "/dlna/ContentDirectory/event", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.NotEmpty(t, rr.Header().Get("SID"))
}

func TestNewServer_Defaults(t *testing.T) {
	f := setupDLNA(t) // constructed with Port 0 + empty friendly name → defaults
	require.Equal(t, "Test NVR", f.srv.cfg.FriendlyName)
	require.Equal(t, 49152, f.srv.cfg.Port) // NewServer fills 0 → default

	srv := NewServer(ServerConfig{}, nil, nil, "k")
	require.Equal(t, "MiBee NVR", srv.cfg.FriendlyName)
	require.Equal(t, 49152, srv.cfg.Port)
}
