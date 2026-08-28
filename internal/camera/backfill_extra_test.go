package camera

// Coverage for backfillStableIDs paths (#583): yaml→db write-through for
// cameras with a valid stable_id, and the dirty/missing-stable_id branches.
// DB-driven and hermetic — the ONVIF discovery branch is reached only with
// a cached device info, which the guard test leaves unset.

import (
	"context"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/stretchr/testify/require"
)

func TestBackfillStableIDs(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{
		{ID: "yaml-has-id", Protocol: "onvif", StableID: "SER-VALID-1"},
		{ID: "onvif-no-id", Protocol: "onvif"},                     // discovery branch, no cache → skip
		{ID: "rtsp-no-id", Protocol: "rtsp", Encoding: "h264"},     // not ONVIF → skip silently
		{ID: "yaml-dirty", Protocol: "onvif", StableID: "0.0.0.0"}, // dirty → discovery branch
	}
	mgr, _, db, _ := newTestManagerWithCfg(t, cfg)
	ctx := context.Background()

	// Seed DB rows so the yaml→db write-through has somewhere to land.
	for _, id := range []string{"yaml-has-id", "onvif-no-id", "rtsp-no-id", "yaml-dirty"} {
		require.NoError(t, db.UpsertCamera(ctx, id, id, "onvif", "", "", "", "", "", "", "", ""))
	}

	mgr.backfillStableIDs(ctx)

	// Valid yaml id written through to DB.
	got, err := db.GetCameraStableID(ctx, "yaml-has-id")
	require.NoError(t, err)
	require.Equal(t, "SER-VALID-1", got)

	// Cameras without a discoverable serial stay empty.
	got, err = db.GetCameraStableID(ctx, "onvif-no-id")
	require.NoError(t, err)
	require.Empty(t, got)
	got, err = db.GetCameraStableID(ctx, "yaml-dirty")
	require.NoError(t, err)
	require.Empty(t, got)

	// Cancelled context bails out promptly.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	mgr.backfillStableIDs(cancelled) // must not touch the DB after cancellation
}
