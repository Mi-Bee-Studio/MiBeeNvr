package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCameraAudioLinkRoundtrip covers the v43 association column end to end
// at the storage layer: default empty, UpdateCameraAudioLink round-trips
// through GetCamera and ListCameras, empty string clears the link, and a
// missing row is a silent no-op (same idempotence contract as group_name).
func TestCameraAudioLinkRoundtrip(t *testing.T) {
	dir := t.TempDir()
	db, err := New(filepath.Join(dir, "test_audio_link.db"))
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	t.Cleanup(func() { _ = db.Close() })

	require.NoError(t, db.UpsertCamera(ctx, "cam-mic", "Mic", "rtsp", "audio", "rtsp://mic/stream", "", "", "", "", "", ""))
	require.NoError(t, db.UpsertCamera(ctx, "cam-cam", "Cam", "rtsp", "h264", "rtsp://cam/stream", "", "", "", "", "", ""))

	// Default: unlinked ('' via COALESCE, never NULL).
	row, err := db.GetCamera(ctx, "cam-mic")
	require.NoError(t, err)
	require.NotNil(t, row)
	require.Equal(t, "", row.AudioLinkCameraID)

	// Link the mic to the camera.
	require.NoError(t, db.UpdateCameraAudioLink(ctx, "cam-mic", "cam-cam"))
	row, err = db.GetCamera(ctx, "cam-mic")
	require.NoError(t, err)
	require.Equal(t, "cam-cam", row.AudioLinkCameraID)

	// List agrees with get (the SPA derives playback associations from lists).
	cams, err := db.ListCameras(ctx)
	require.NoError(t, err)
	links := map[string]string{}
	for _, c := range cams {
		links[c.ID] = c.AudioLinkCameraID
	}
	require.Equal(t, "cam-cam", links["cam-mic"])
	require.Equal(t, "", links["cam-cam"])

	// Clearing the link round-trips back to ''.
	require.NoError(t, db.UpdateCameraAudioLink(ctx, "cam-mic", ""))
	row, err = db.GetCamera(ctx, "cam-mic")
	require.NoError(t, err)
	require.Equal(t, "", row.AudioLinkCameraID)

	// Missing row: silent no-op.
	require.NoError(t, db.UpdateCameraAudioLink(ctx, "cam-nobody", "cam-cam"))
}

// TestCreateSegmentAudioFormat pins the storage-side acceptance of the
// "audio" segment format: same single-file MP4 shape as h264/h265 (temp
// file created, .mp4 final path proposed, camera dir ensured).
func TestCreateSegmentAudioFormat(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(filepath.Join(dir, "root"))
	require.NoError(t, err)

	temp, final, err := m.CreateSegment("cam-mic", "audio")
	require.NoError(t, err)
	require.NotEmpty(t, temp)
	require.Contains(t, final, "cam-mic")
	require.Contains(t, final, ".mp4")
	require.FileExists(t, temp)

	// Finalize through the normal close path.
	require.NoError(t, m.CloseSegment(temp, final))
	require.FileExists(t, final)
	m.UnregisterActiveTemp(temp)
}
