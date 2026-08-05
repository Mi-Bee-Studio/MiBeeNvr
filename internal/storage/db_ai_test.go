package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestListAIEventsReturnsInsertedRows is a regression test for issue #212:
// ListAIEvents returned an empty slice (while total was correct) because the
// rows.Next() loop was missing `events = append(events, e)` — a line deleted
// by commit de002b8 ("chore(lint): nolint deferred findings") when it added the
// rows.Err() check. This test inserts a few events and asserts they come back.
func TestListAIEventsReturnsInsertedRows(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_ai_events.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	t.Cleanup(func() { _ = db.Close() })

	const camID = "cam-test-ai"
	// Insert three events across two event_types.
	inserts := []*AIEvent{
		{CameraID: camID, EventType: "zone_intrusion", Severity: "info", Confidence: 0.9, BBox: "[0.1,0.2,0.3,0.4]"},
		{CameraID: camID, EventType: "zone_intrusion", Severity: "warning", Confidence: 0.7},
		{CameraID: camID, EventType: "loitering", Severity: "info", Confidence: 0.5},
		{CameraID: "cam-other", EventType: "zone_intrusion", Severity: "info", Confidence: 0.3},
	}
	for _, e := range inserts {
		_, err := db.InsertAIEvent(ctx, e)
		require.NoError(t, err)
	}

	// 1) No filter → all 4 rows returned (not nil/empty).
	got, total, err := db.ListAIEvents(ctx, AIEventFilter{Limit: 50})
	require.NoError(t, err)
	require.Equal(t, 4, total, "total count must reflect all inserted rows")
	require.Len(t, got, 4, "regression #212: events slice must not be empty when rows exist")

	// 2) Filter by camera_id.
	got, total, err = db.ListAIEvents(ctx, AIEventFilter{CameraID: camID, Limit: 50})
	require.NoError(t, err)
	require.Equal(t, 3, total)
	require.Len(t, got, 3, "camera_id filter must return matching rows")

	// 3) Filter by event_type.
	got, total, err = db.ListAIEvents(ctx, AIEventFilter{CameraID: camID, EventType: "zone_intrusion", Limit: 50})
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, got, 2)
	for _, e := range got {
		require.Equal(t, "zone_intrusion", e.EventType)
	}

	// 4) Default ordering is DESC by created_at/id — newest first. The cam-other
	// row was inserted last, so it must be got[0] in the unfiltered list.
	got, _, err = db.ListAIEvents(ctx, AIEventFilter{Limit: 50})
	require.NoError(t, err)
	require.Equal(t, "cam-other", got[0].CameraID, "DESC ordering: last-inserted row must come first")
}

// TestListAIEventsEmptyWhenNoRows confirms the empty case returns a non-nil
// slice (handler normalizes nil→[]) and total=0, so the frontend gets a clean
// "no events" state rather than a misleading total>0 with empty rows.
func TestListAIEventsEmptyWhenNoRows(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_ai_events_empty.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	t.Cleanup(func() { _ = db.Close() })

	got, total, err := db.ListAIEvents(ctx, AIEventFilter{Limit: 50})
	require.NoError(t, err)
	require.Equal(t, 0, total)
	require.Empty(t, got)
}
