package storage

// Long-tail coverage for the storage DB layer (#580): AI stats/deletes,
// archive group stats, camera ingest/activation/stable-id/endpoint ops,
// cascade channels, per-camera stats, dark recordings, transcode task
// listing/recovery, and the db.go maintenance surface. All hermetic
// (per-test SQLite via newTestDB).

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// recSeed is a minimal recording fixture for the long-tail tests.
type recSeed struct {
	id      string
	camera  string
	format  string
	started time.Time
	size    int64
	merge   string
	ended   time.Time
}

func seedRec(t *testing.T, db *DB, s *recSeed) {
	t.Helper()
	ended := s.ended
	if ended.IsZero() {
		ended = s.started.Add(time.Minute)
	}
	merge := s.merge
	if merge == "" {
		merge = model.MergeStatusPending
	}
	require.NoError(t, db.InsertRecording(context.Background(), &model.Recording{
		ID:          s.id,
		CameraID:    s.camera,
		FilePath:    "/tmp/" + s.id + ".mp4",
		Format:      model.Format(s.format),
		StartedAt:   s.started,
		EndedAt:     ended,
		Duration:    60,
		FileSize:    s.size,
		MergeStatus: merge,
	}))
}

func TestAIEventStatsAndDeleteByRecording(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Two events for one recording, one for another camera.
	for _, e := range []*AIEvent{
		{CameraID: "cam-1", RecordingID: "rec-1", EventType: "zone_intrusion", Severity: "warn"},
		{CameraID: "cam-1", RecordingID: "rec-1", EventType: "zone_intrusion", Severity: "warn"},
		{CameraID: "cam-1", RecordingID: "rec-2", EventType: "loitering", Severity: "info"},
		{CameraID: "cam-2", RecordingID: "rec-3", EventType: "person", Severity: "info"},
	} {
		_, err := db.InsertAIEvent(ctx, e)
		require.NoError(t, err)
	}

	stats, err := db.GetAIEventStats(ctx, "cam-1", time.Now().UTC().Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, stats, 2)
	total := 0
	for _, s := range stats {
		total += s.Count
	}
	require.Equal(t, 3, total)

	// Global view: empty cameraID aggregates across cameras.
	stats, err = db.GetAIEventStats(ctx, "", time.Now().UTC().Add(-time.Hour))
	require.NoError(t, err)
	gtotal := 0
	for _, s := range stats {
		gtotal += s.Count
	}
	require.Equal(t, 4, gtotal)

	// Since in the future filters everything out.
	stats, err = db.GetAIEventStats(ctx, "cam-1", time.Now().UTC().Add(time.Hour))
	require.NoError(t, err)
	require.Empty(t, stats)

	// Delete-by-recording removes exactly that recording's rows.
	n, err := db.DeleteAIEventsByRecording(ctx, "rec-1")
	require.NoError(t, err)
	require.Equal(t, int64(2), n)
	stats, err = db.GetAIEventStats(ctx, "cam-1", time.Now().UTC().Add(-time.Hour))
	require.NoError(t, err)
	for _, s := range stats {
		require.NotEqual(t, "zone_intrusion", s.EventType)
	}
}

func TestMarshalBBox(t *testing.T) {
	t.Parallel()
	require.Equal(t, "[0.1,0.2,0.3,0.4]", MarshalBBox([4]float64{0.1, 0.2, 0.3, 0.4}))
}

func TestRecordingAIStatusReaders(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedRec(t, db, &recSeed{id: "ai-1", camera: "cam-1", format: "h264", started: now})
	seedRec(t, db, &recSeed{id: "ai-2", camera: "cam-1", format: "h264", started: now})

	require.NoError(t, db.UpdateRecordingAIStatus(ctx, "ai-1", "processing", ""))
	st, err := db.GetRecordingAIStatus(ctx, "ai-1")
	require.NoError(t, err)
	require.Equal(t, "processing", st)

	_, err = db.GetRecordingAIStatus(ctx, "nope")
	require.ErrorIs(t, err, sql.ErrNoRows)

	batch, err := db.BatchGetRecordingAIStatus(ctx, []string{"ai-1", "ai-2", "nope"})
	require.NoError(t, err)
	require.Equal(t, "processing", batch["ai-1"])
	require.Equal(t, "", batch["ai-2"])
	_, ok := batch["nope"]
	require.False(t, ok, "unknown ids are omitted")
}

func TestArchiveGroupStatsAndRetention(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, db.UpsertCamera(ctx, "cam-1", "Cam", "onvif", "", "", "", "", "", "", "", ""))
	seedRec(t, db, &recSeed{id: "ar-1", camera: "cam-1", format: "h264", started: now, size: 100})
	seedRec(t, db, &recSeed{id: "ar-2", camera: "cam-1", format: "h264", started: now, size: 50})

	// Nothing archived yet.
	cnt, _, err := db.GetArchiveGroupStats(ctx, "cam-1")
	require.NoError(t, err)
	require.Equal(t, 0, cnt)

	n, err := db.ArchiveAllRecordings(ctx, "cam-1")
	require.NoError(t, err)
	require.Equal(t, int64(2), n)

	cnt, size, err := db.GetArchiveGroupStats(ctx, "cam-1")
	require.NoError(t, err)
	require.Equal(t, 2, cnt)
	require.Equal(t, int64(150), size)

	// Non-archive stats exclude archived rows.
	cnt, _, err = db.GetCameraRecordingStats(ctx, "cam-1")
	require.NoError(t, err)
	require.Equal(t, 0, cnt)

	// Retention applies only to archived cameras — archive it first.
	require.NoError(t, db.ArchiveCameraDB(ctx, "cam-1"))
	require.NoError(t, db.SetArchiveRetention(ctx, "cam-1", 90))
	require.NoError(t, db.UpsertCamera(ctx, "cam-2", "Cam 2", "onvif", "", "", "", "", "", "", "", ""))
	require.ErrorIs(t, db.SetArchiveRetention(ctx, "cam-2", 90), sql.ErrNoRows, "non-archived camera has no archive retention row")

	// No cleanup task exists until one is enqueued.
	has, err := db.HasArchiveCleanupTaskForCamera(ctx, "cam-1")
	require.NoError(t, err)
	require.False(t, has)
}

func TestCameraIngestActivationStableID(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "cam-1", "Cam", "srt", "", "srt://x", "", "", "", "", "", ""))

	// Ingest credentials round-trip.
	require.NoError(t, db.UpsertCameraIngest(ctx, "cam-1", "key-1", "pass-1", "sid-1"))

	// Activation state.
	require.NoError(t, db.UpdateCameraActivationState(ctx, "cam-1", "pending_activation"))
	require.NoError(t, db.UpdateCameraActivationState(ctx, "cam-1", "active"))

	// stable_id write/read/existence, endpoint lookup, reassign.
	require.NoError(t, db.UpdateCameraStableID(ctx, "cam-1", "SER-1"))
	got, err := db.GetCameraStableID(ctx, "cam-1")
	require.NoError(t, err)
	require.Equal(t, "SER-1", got)

	exists, err := db.CameraExistsByStableID(ctx, "SER-1")
	require.NoError(t, err)
	require.True(t, exists)
	exists, err = db.CameraExistsByStableID(ctx, "SER-X")
	require.NoError(t, err)
	require.False(t, exists)

	// Unknown camera's stable id is empty.
	got, err = db.GetCameraStableID(ctx, "ghost")
	require.NoError(t, err)
	require.Empty(t, got)

	// ONVIF endpoint raw write + reader-facing lookup.
	require.NoError(t, db.UpdateCameraOnvifEndpointRaw(ctx, "cam-1", "http://192.168.63.251:80/onvif/device_service"))
	ep, err := db.GetCameraOnvifEndpoint(ctx, "cam-1")
	require.NoError(t, err)
	require.Equal(t, "http://192.168.63.251:80/onvif/device_service", ep)
	ep, err = db.GetCameraOnvifEndpoint(ctx, "ghost")
	require.NoError(t, err)
	require.Empty(t, ep)

	// Endpoint listing for the repair CLI.
	rows, err := db.ListCameraEndpointsForRepair(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "cam-1", rows[0].ID)
	require.NotEmpty(t, rows[0].Endpoint)

	// Reassign stable id to another camera.
	require.NoError(t, db.UpsertCamera(ctx, "cam-2", "Cam 2", "onvif", "", "", "", "", "", "", "", ""))
	require.NoError(t, db.ReassignCameraStableID(ctx, "cam-1", "cam-2"))
	got, err = db.GetCameraStableID(ctx, "cam-2")
	require.NoError(t, err)
	require.Equal(t, "SER-1", got)

	// Delete camera row.
	require.NoError(t, db.DeleteCamera(ctx, "cam-2"))
	got, err = db.GetCameraStableID(ctx, "cam-2")
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestCascadeChannelCRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	ch := CascadeChannel{CameraID: "cam-1", GBChannelID: "34020000001320000001", Name: "Front", UpdatedAt: time.Now().UTC()}
	require.NoError(t, db.UpsertCascadeChannel(ctx, ch))
	// Upsert again with a new name (conflict on camera_id).
	ch.Name = "Front Door"
	require.NoError(t, db.UpsertCascadeChannel(ctx, ch))

	list, err := db.ListCascadeChannels(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "Front Door", list[0].Name)

	// DeleteGB28181Channel operates on the gb28181_channels table (device
	// channels), not cascade_channels — exercise it with a real device row.
	require.NoError(t, db.UpsertGB28181Device(ctx, GB28181Device{ID: "34020000002000000001", Name: "IPC", Status: "online"}))
	require.NoError(t, db.UpsertGB28181Channel(ctx, GB28181Channel{
		ID: "34020000001320000099", DeviceID: "34020000002000000001", Name: "Ch99", Status: "idle",
	}))
	require.NoError(t, db.DeleteGB28181Channel(ctx, "34020000001320000099"))
	dev, err := db.GetGB28181Device(ctx, "34020000002000000001")
	require.NoError(t, err)
	require.NotNil(t, dev)
}

func TestGetGB28181Device(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	dev, err := db.GetGB28181Device(ctx, "34020000002000000001")
	require.NoError(t, err)
	require.Nil(t, dev)
}

func TestPerCameraStatsLongTail(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedRec(t, db, &recSeed{id: "st-1", camera: "cam-1", format: "h264", started: now.Add(-2 * time.Hour), size: 10})
	seedRec(t, db, &recSeed{id: "st-2", camera: "cam-1", format: "h264", started: now.Add(-1 * time.Hour), size: 20})

	cnt, err := db.CountRecordingsByCamera(ctx, "cam-1")
	require.NoError(t, err)
	require.Equal(t, 2, cnt)

	// CountRowsByCamera serves the allowlisted camera-keyed tables.
	cnt, err = db.CountRowsByCamera(ctx, "ai_events", "cam-1")
	require.NoError(t, err)
	require.Equal(t, 0, cnt)
	_, err = db.CountRowsByCamera(ctx, "cameras", "cam-1")
	require.ErrorContains(t, err, "unknown table")

	paths, err := db.ListRecordingFilePathsByCamera(ctx, "cam-1")
	require.NoError(t, err)
	require.Len(t, paths, 2)

	last, err := db.GetLastRecordingTime(ctx, "cam-1")
	require.NoError(t, err)
	require.NotNil(t, last)
	require.True(t, last.After(now.Add(-90*time.Minute)))

	last, err = db.GetLastRecordingTime(ctx, "ghost")
	require.NoError(t, err)
	require.Nil(t, last)

	all, err := db.GetAllLastRecordingTimes(ctx)
	require.NoError(t, err)
	require.Contains(t, all, "cam-1")
	require.NotNil(t, all["cam-1"])
}

func TestListDarkRecordingsCounts(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Dark segment older than the grace period is listed; a fresh one is not.
	old := time.Now().UTC().Add(-2 * time.Hour)
	seedRec(t, db, &recSeed{id: "dk-1", camera: "cam-1", format: "h264", started: old, merge: "dark"})
	seedRec(t, db, &recSeed{id: "dk-2", camera: "cam-1", format: "h264", started: time.Now().UTC(), merge: "dark"})
	seedRec(t, db, &recSeed{id: "ok-1", camera: "cam-1", format: "h264", started: old, merge: "pending"})

	dark, err := db.ListDarkRecordings(ctx, time.Hour)
	require.NoError(t, err)
	require.Len(t, dark, 1)
	require.Equal(t, "dk-1", dark[0].ID)
}

func TestTranscodeTaskListAndRecovery(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	require.NoError(t, db.EnqueueTask(ctx, &TranscodeTask{
		CameraID: "cam-1", RecordingID: "r1", InputPath: "/in.mp4", InputFormat: "h265",
		OutputPath: "/out.mp4", OutputFormat: "h264", Status: "pending",
	}))

	tasks, total, err := db.ListTranscodeTasks(ctx, TranscodeTaskFilter{Limit: 10})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, tasks, 1)
	require.Equal(t, "r1", tasks[0].RecordingID)

	// Filter by status narrows correctly.
	tasks, total, err = db.ListTranscodeTasks(ctx, TranscodeTaskFilter{Status: "completed"})
	require.NoError(t, err)
	require.Equal(t, 0, total)
	require.Empty(t, tasks)

	// Stuck-task recovery: claim a task and backdate it via SQL.
	require.NoError(t, db.EnqueueTask(ctx, &TranscodeTask{
		CameraID: "cam-1", RecordingID: "r2", InputPath: "/in2.mp4", InputFormat: "h265",
		OutputPath: "/out2.mp4", OutputFormat: "h264", Status: "pending",
	}))
	task, err := db.DequeueTask(ctx)
	require.NoError(t, err)
	require.NotNil(t, task)
	_, err = db.db.ExecContext(ctx, "UPDATE transcoding_tasks SET started_at=? WHERE id=?", time.Now().UTC().Add(-time.Hour).Format("2006-01-02 15:04:05.999999999"), task.ID)
	require.NoError(t, err)

	n, err := db.RecoverStuckTasks(ctx, 10*time.Minute)
	require.NoError(t, err)
	require.Equal(t, int64(1), n)

	// Format rewrite.
	require.NoError(t, db.UpdateRecordingFormat(ctx, "r1", "h265"))
}
