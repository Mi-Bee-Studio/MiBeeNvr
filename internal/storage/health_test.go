package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

func setupHealthTest(t *testing.T) (*DB, context.Context) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "health_test.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	return db, ctx
}

func insertHealthEvent(t *testing.T, db *DB, ctx context.Context, event model.HealthEvent) {
	t.Helper()
	err := db.InsertHealthEvent(ctx, event)
	require.NoError(t, err)
}

func TestHealthEventsCRUD(t *testing.T) {
	db, ctx := setupHealthTest(t)
	defer db.Close()

	now := time.Now().UTC()

	// Insert a health event
	event := model.HealthEvent{
		CameraID:  "cam1",
		EventType: "connectivity",
		Status:    "ok",
		Message:   "Camera is reachable",
		Metadata:  `{"ping_ms": 5}`,
		CreatedAt: now,
	}
	err := db.InsertHealthEvent(ctx, event)
	require.NoError(t, err)

	// List health events
	events, total, err := db.ListHealthEvents(ctx, HealthEventsFilter{})
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, 1, total)
	require.Equal(t, "cam1", events[0].CameraID)
	require.Equal(t, "ok", events[0].Status)

	// Get latest for camera
	latest, err := db.GetLatestCameraHealth(ctx, "cam1")
	require.NoError(t, err)
	require.NotNil(t, latest)
	require.Equal(t, "ok", latest.Status)
	require.Equal(t, "connectivity", latest.EventType)

	// Get latest for non-existent camera
	latest, err = db.GetLatestCameraHealth(ctx, "nonexistent")
	require.NoError(t, err)
	require.Nil(t, latest)
}

func TestHealthEventsFilter(t *testing.T) {
	db, ctx := setupHealthTest(t)
	defer db.Close()

	now := time.Now().UTC()

	// Insert events for two cameras
	events := []model.HealthEvent{
		{CameraID: "cam1", EventType: "connectivity", Status: "ok", Message: "ok", Metadata: "{}", CreatedAt: now.Add(-2 * time.Minute)},
		{CameraID: "cam1", EventType: "disk", Status: "warning", Message: "low space", Metadata: "{}", CreatedAt: now.Add(-1 * time.Minute)},
		{CameraID: "cam2", EventType: "connectivity", Status: "error", Message: "timeout", Metadata: "{}", CreatedAt: now},
	}
	for _, e := range events {
		insertHealthEvent(t, db, ctx, e)
	}

	// Filter by camera_id
	filtered, total, err := db.ListHealthEvents(ctx, HealthEventsFilter{CameraID: "cam1"})
	require.NoError(t, err)
	require.Len(t, filtered, 2)
	require.Equal(t, 2, total)

	// Filter by time range (since = now - 90s)
	since := now.Add(-90 * time.Second)
	filtered, total, err = db.ListHealthEvents(ctx, HealthEventsFilter{Since: since.Format(sqliteTimeFormat)})
	require.NoError(t, err)
	require.Len(t, filtered, 2)
	require.Equal(t, 2, total)

	// Filter by camera + time
	filtered, total, err = db.ListHealthEvents(ctx, HealthEventsFilter{
		CameraID: "cam1",
		Since:    now.Add(-90 * time.Second).Format(sqliteTimeFormat),
	})
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	require.Equal(t, 1, total)
	require.Equal(t, "disk", filtered[0].EventType)
}

func TestHealthEventsFilterByType(t *testing.T) {
	db, ctx := setupHealthTest(t)
	defer db.Close()

	now := time.Now().UTC()

	// Insert events of different types
	events := []model.HealthEvent{
		{CameraID: "cam1", EventType: "connection_lost", Status: "error", Message: "lost", Metadata: "{}", CreatedAt: now.Add(-2 * time.Minute)},
		{CameraID: "cam1", EventType: "connection_restored", Status: "ok", Message: "restored", Metadata: "{}", CreatedAt: now.Add(-1 * time.Minute)},
		{CameraID: "cam1", EventType: "stream_anomaly", Status: "warning", Message: "IDR interval", Metadata: "{}", CreatedAt: now},
	}
	for _, e := range events {
		insertHealthEvent(t, db, ctx, e)
	}

	// Filter by connection_lost
	filtered, total, err := db.ListHealthEvents(ctx, HealthEventsFilter{EventType: "connection_lost"})
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	require.Equal(t, 1, total)
	require.Equal(t, "connection_lost", filtered[0].EventType)
	require.Equal(t, "lost", filtered[0].Message)

	// Filter by stream_anomaly
	filtered, total, err = db.ListHealthEvents(ctx, HealthEventsFilter{EventType: "stream_anomaly"})
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	require.Equal(t, 1, total)
	require.Equal(t, "stream_anomaly", filtered[0].EventType)

	// Filter by non-existent type returns empty
	filtered, total, err = db.ListHealthEvents(ctx, HealthEventsFilter{EventType: "nonexistent"})
	require.NoError(t, err)
	require.Len(t, filtered, 0)
	require.Equal(t, 0, total)
}

func TestDeleteHealthEventsByType(t *testing.T) {
	db, ctx := setupHealthTest(t)
	defer db.Close()

	now := time.Now().UTC()

	// Insert 3 connection_lost (old), 2 connection_restored (old), 1 stream_anomaly (recent)
	for i := 0; i < 3; i++ {
		insertHealthEvent(t, db, ctx, model.HealthEvent{
			CameraID: "cam1", EventType: "connection_lost", Status: "error", Message: "lost", Metadata: "{}", CreatedAt: now.Add(-24 * time.Hour),
		})
	}
	for i := 0; i < 2; i++ {
		insertHealthEvent(t, db, ctx, model.HealthEvent{
			CameraID: "cam1", EventType: "connection_restored", Status: "ok", Message: "restored", Metadata: "{}", CreatedAt: now.Add(-24 * time.Hour),
		})
	}
	insertHealthEvent(t, db, ctx, model.HealthEvent{
		CameraID: "cam1", EventType: "stream_anomaly", Status: "warning", Message: "IDR interval too long", Metadata: "{}", CreatedAt: now,
	})

	// Delete stream_anomaly events before now+1min (all of them)
	deleted, err := db.DeleteHealthEventsByType(ctx, "stream_anomaly", now.Add(1*time.Minute))
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)

	// Verify 5 remaining (3 connection_lost + 2 connection_restored)
	events, total, err := db.ListHealthEvents(ctx, HealthEventsFilter{})
	require.NoError(t, err)
	require.Len(t, events, 5)
	require.Equal(t, 5, total)

	// Verify no stream_anomaly events remain
	filtered, _, err := db.ListHealthEvents(ctx, HealthEventsFilter{EventType: "stream_anomaly"})
	require.NoError(t, err)
	require.Len(t, filtered, 0)
}

func TestHealthEventsPagination(t *testing.T) {
	db, ctx := setupHealthTest(t)
	defer db.Close()

	now := time.Now().UTC()

	// Insert 5 events
	for i := 0; i < 5; i++ {
		insertHealthEvent(t, db, ctx, model.HealthEvent{
			CameraID:  "cam1",
			EventType: "test",
			Status:    "ok",
			Message:   "event",
			Metadata:  "{}",
			CreatedAt: now.Add(time.Duration(i) * time.Minute),
		})
	}

	// Limit 2
	events, total, err := db.ListHealthEvents(ctx, HealthEventsFilter{Limit: 2})
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Equal(t, 5, total)

	// Offset 3 (skip first 3)
	events, total, err = db.ListHealthEvents(ctx, HealthEventsFilter{Limit: 10, Offset: 3})
	require.NoError(t, err)
	require.Len(t, events, 2) // 2 remaining
	require.Equal(t, 5, total)
}

func TestHealthEventsDeleteOld(t *testing.T) {
	db, ctx := setupHealthTest(t)
	defer db.Close()

	now := time.Now().UTC()

	// Insert old events (7 days ago)
	for i := 0; i < 3; i++ {
		insertHealthEvent(t, db, ctx, model.HealthEvent{
			CameraID:  "cam1",
			EventType: "test",
			Status:    "ok",
			Message:   "old",
			Metadata:  "{}",
			CreatedAt: now.Add(-7 * 24 * time.Hour),
		})
	}

	// Insert recent events (1 hour ago)
	for i := 0; i < 2; i++ {
		insertHealthEvent(t, db, ctx, model.HealthEvent{
			CameraID:  "cam1",
			EventType: "test",
			Status:    "ok",
			Message:   "recent",
			Metadata:  "{}",
			CreatedAt: now.Add(-1 * time.Hour),
		})
	}

	// Delete events before 2 days ago
	cutoff := now.Add(-2 * 24 * time.Hour)
	deleted, err := db.DeleteHealthEventsBefore(ctx, cutoff)
	require.NoError(t, err)
	require.Equal(t, int64(3), deleted)

	// Verify only 2 recent events remain
	events, total, err := db.ListHealthEvents(ctx, HealthEventsFilter{})
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Equal(t, 2, total)

	// Verify all remaining are recent
	for _, e := range events {
		require.Equal(t, "recent", e.Message)
	}
}

func TestHealthEventsSummary(t *testing.T) {
	db, ctx := setupHealthTest(t)
	defer db.Close()

	now := time.Now().UTC()

	// Insert events for multiple cameras
	events := []model.HealthEvent{
		{CameraID: "cam1", EventType: "connectivity", Status: "ok", Message: "all good", Metadata: "{}", CreatedAt: now.Add(-1 * time.Minute)},
		{CameraID: "cam1", EventType: "connectivity", Status: "error", Message: "offline", Metadata: "{}", CreatedAt: now.Add(-2 * time.Minute)},
		{CameraID: "cam2", EventType: "disk", Status: "warning", Message: "disk 90%", Metadata: "{}", CreatedAt: now},
		{CameraID: "cam3", EventType: "connectivity", Status: "ok", Message: "stable", Metadata: "{}", CreatedAt: now},
	}
	for _, e := range events {
		insertHealthEvent(t, db, ctx, e)
	}

	summary, err := db.GetCameraHealthSummary(ctx)
	require.NoError(t, err)
	require.Len(t, summary, 3)

	// cam1: latest is "ok" (more recent than "error")
	require.Equal(t, "ok", summary["cam1"].LatestStatus)
	require.Equal(t, "connectivity", summary["cam1"].LatestEvent)
	require.Equal(t, "all good", summary["cam1"].LatestMessage)

	// cam2: latest is "warning"
	require.Equal(t, "warning", summary["cam2"].LatestStatus)
	require.Equal(t, "disk", summary["cam2"].LatestEvent)

	// cam3: latest is "ok"
	require.Equal(t, "ok", summary["cam3"].LatestStatus)
}

func TestHealthEventsMigrationIdempotent(t *testing.T) {
	db, ctx := setupHealthTest(t)
	defer db.Close()

	// Init already ran once during setup. Run again.
	err := db.Init(ctx)
	require.NoError(t, err)

	// Verify we can still insert and read
	insertHealthEvent(t, db, ctx, model.HealthEvent{
		CameraID:  "cam1",
		EventType: "test",
		Status:    "ok",
		Message:   "idempotent",
		Metadata:  "{}",
		CreatedAt: time.Now().UTC(),
	})

	events, total, err := db.ListHealthEvents(ctx, HealthEventsFilter{})
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, 1, total)
	require.Equal(t, "idempotent", events[0].Message)
}
