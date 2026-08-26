package camera

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// The cascade catalog must only advertise cameras the DB still lists as
// active. Archiving marks the row archived=1; a partially-failed archive can
// leave the config entry in the YAML (boot re-upserts the row without
// touching the archived flag), and the catalog must not advertise that
// residue to the upper platform (found on M5: archived "Lower Cascade Test"
// still allocated cascade channel …05 on the fnOS upper, dead forever).
func TestCascadeSource_ExcludesArchivedCameras(t *testing.T) {
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{
		{ID: "cam-live", Name: "Live", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://127.0.0.1:1/a"},
		{ID: "cam-residue", Name: "Archived Residue", Protocol: "gb28181", Encoding: ""},
		{ID: "cam-mjpeg", Name: "MJPEG", Protocol: "rtsp", Encoding: "mjpeg", URL: "rtsp://127.0.0.1:1/b"},
	}
	mgr, _, db, _ := newTestManagerWithCfg(t, cfg)
	ctx := context.Background()

	// DB knows only the live camera (the residue camera's row is archived or
	// was never created by the API path).
	require.NoError(t, db.UpsertCamera(ctx, "cam-live", "Live", "rtsp", "h264", "rtsp://127.0.0.1:1/a", "", "", "", "", "", ""))
	// Mirror the M5 sequence: row created active -> archived (partial
	// archive leaves the YAML entry) -> boot re-upsert must not resurrect.
	require.NoError(t, db.UpsertCamera(ctx, "cam-residue", "Archived Residue", "gb28181", "", "", "", "", "", "", "", ""))
	require.NoError(t, db.ArchiveCameraDB(ctx, "cam-residue"))
	require.NoError(t, db.UpsertCamera(ctx, "cam-residue", "Archived Residue", "gb28181", "", "", "", "", "", "", "", ""))
	// Boot-style re-upsert must not resurrect the archived row.
	rows, err := db.ListCameras(ctx)
	require.NoError(t, err)
	ids := map[string]bool{}
	for _, r := range rows {
		ids[r.ID] = true
	}
	require.False(t, ids["cam-residue"], "archived row should stay hidden after re-upsert")

	src := NewCascadeSource(mgr, db)
	var got []string
	for _, c := range src.Cameras() {
		got = append(got, c.ID)
	}
	require.Equal(t, []string{"cam-live"}, got, "catalog must exclude archived residue and MJPEG")

	// nil DB (no filter) preserves the pre-fix behavior: everything but
	// MJPEG/timelapse is offered.
	var gotNil []string
	for _, c := range (cascadeSource{cm: mgr}).Cameras() {
		gotNil = append(gotNil, c.ID)
	}
	require.ElementsMatch(t, []string{"cam-live", "cam-residue"}, gotNil)
}
