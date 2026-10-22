package camera

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// TestSyncIngestEncoding pins the auto-detect-first semantics for push-in
// cameras: the publisher's observed codec overwrites a stale manual annotation
// (yaml + DB), non-ingest protocols are left alone (pull cameras keep the
// fill-once ensureEncoding semantics — a user may deliberately pin
// stream_encoding to work around a lying SDP), and a matching encoding writes
// nothing (idempotent reconnects).
func TestSyncIngestEncoding(t *testing.T) {
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{
		{ID: "rtmp-1", Name: "push camera", Protocol: "rtmp", Encoding: "h264"},
		{ID: "onvif-1", Name: "pull camera", Protocol: "onvif", Encoding: "h264"},
	}
	mgr, _, db, configPath := newTestManagerWithCfg(t, cfg)
	ctx := context.Background()
	for _, c := range cfg.Cameras {
		require.NoError(t, db.UpsertCamera(ctx, c.ID, c.Name, c.Protocol, c.Encoding, c.URL, "", "", "", "", "", ""))
	}

	// Stale annotation heals to the observed codec.
	mgr.SyncIngestEncoding("rtmp-1", "h265")
	require.Equal(t, "h265", mgr.snapshotConfig("rtmp-1").Encoding)
	row, err := db.GetCamera(ctx, "rtmp-1")
	require.NoError(t, err)
	require.Equal(t, "h265", row.Encoding)
	// Persisted to yaml as well.
	saved, err := config.Load(configPath)
	require.NoError(t, err)
	var rtmpCfg *config.CameraConfig
	for i := range saved.Cameras {
		if saved.Cameras[i].ID == "rtmp-1" {
			rtmpCfg = &saved.Cameras[i]
		}
	}
	require.NotNil(t, rtmpCfg)
	require.Equal(t, "h265", rtmpCfg.Encoding)

	// Idempotent: matching encoding must not rewrite (same values again).
	mgr.SyncIngestEncoding("rtmp-1", "h265")
	require.Equal(t, "h265", mgr.snapshotConfig("rtmp-1").Encoding)

	// Non-ingest protocol untouched even when the encoding differs.
	mgr.SyncIngestEncoding("onvif-1", "h265")
	require.Equal(t, "h264", mgr.snapshotConfig("onvif-1").Encoding)

	// Unknown camera / bogus codec: no panic, no write.
	mgr.SyncIngestEncoding("missing", "h265")
	mgr.SyncIngestEncoding("rtmp-1", "vp9")
	require.Equal(t, "h265", mgr.snapshotConfig("rtmp-1").Encoding)
}
