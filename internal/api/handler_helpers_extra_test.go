package api

// Long-tail helper coverage (#596): the ONVIF error-mapper typed branches,
// the GB28181 cascade status payload, and the pure xiaomi-local /
// stream-probe helpers — all direct unit calls, no I/O.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/api/gbapi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

func TestONVIFErrorMappers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		code int
	}{
		{"camera not found", &model.CameraNotFoundError{CameraID: "c"}, http.StatusNotFound},
		{"not an onvif camera", &model.ONVIFNotCameraError{CameraID: "c"}, http.StatusBadRequest},
		{"connection error", &model.ONVIFConnectionError{CameraID: "c", Err: errors.New("dial")}, http.StatusBadGateway},
		{"no profiles", &model.ONVIFNoProfilesError{CameraID: "c"}, http.StatusNotFound},
		{"generic", errors.New("boom"), http.StatusInternalServerError},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mappers := map[string]func(http.ResponseWriter, string, error){
				"ptz":      gbapi.HandleONVIFPTZError,
				"imaging":  gbapi.HandleONVIFImagingError,
				"snapshot": gbapi.HandleONVIFSnapshotError,
			}
			for name, mapper := range mappers {
				w := httptest.NewRecorder()
				mapper(w, "c", tc.err)
				require.Equal(t, tc.code, w.Code, name)
			}
			// The device-mgmt mapper has no ONVIFNoProfilesError branch —
			// that error surfaces from GetProfiles which it never calls.
			dmCode := tc.code
			var noProfiles *model.ONVIFNoProfilesError
			if errors.As(tc.err, &noProfiles) {
				dmCode = http.StatusInternalServerError
			}
			w := httptest.NewRecorder()
			gbapi.HandleONVIFDeviceMgmtError(w, "c", tc.err)
			require.Equal(t, dmCode, w.Code, "device mgmt")
		})
	}
}

type fakeGBCascade struct {
	online   bool
	since    time.Duration
	sinceOk  bool
	forwards int
}

func (f *fakeGBCascade) Online() bool                             { return f.online }
func (f *fakeGBCascade) RegistrationSince() (time.Duration, bool) { return f.since, f.sinceOk }
func (f *fakeGBCascade) ForwardCount() int                        { return f.forwards }

func TestGBCascadeStatus(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	h := TestHandler(db, store)
	t.Cleanup(h.Close)

	// Not wired: disabled payload.
	w := httptest.NewRecorder()
	gbapi.Handle(gbDeps(h), "handleGB28181CascadeStatus")(w, nil)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"enabled":false`)

	// Wired + registered.
	h.SetGB28181Cascade(&fakeGBCascade{online: true, since: 90 * time.Second, sinceOk: true, forwards: 2})
	w = httptest.NewRecorder()
	gbapi.Handle(gbDeps(h), "handleGB28181CascadeStatus")(w, nil)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"online":true`)
	require.Contains(t, w.Body.String(), `"forwards":2`)
	require.Contains(t, w.Body.String(), `"registered_for_seconds":90`)

	// Wired but never registered: no duration field.
	h.SetGB28181Cascade(&fakeGBCascade{})
	w = httptest.NewRecorder()
	gbapi.Handle(gbDeps(h), "handleGB28181CascadeStatus")(w, nil)
	require.Equal(t, http.StatusOK, w.Code)
	require.NotContains(t, w.Body.String(), "registered_for_seconds")
}
