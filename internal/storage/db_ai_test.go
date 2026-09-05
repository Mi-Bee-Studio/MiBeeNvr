package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
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

// TestAIEventCreatedAtRFC3339 pins the API contract fix: created_at must come
// back as RFC3339 with an explicit UTC offset. SQLite datetime('now') stores a
// zoneless "YYYY-MM-DD HH:MM:SS"; clients (JS Date.parse, ISO parsers) read
// that as LOCAL time, which shifted AI-event display and event→recording deep
// links by the browser's UTC offset (8h on a CST deployment).
func TestAIEventCreatedAtRFC3339(t *testing.T) {
	dir := t.TempDir()
	db, err := New(filepath.Join(dir, "t.db"))
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.InsertAIEvent(ctx, &AIEvent{CameraID: "cam-tz", EventType: "loitering", Severity: "info"})
	require.NoError(t, err)

	events, _, err := db.ListAIEvents(ctx, AIEventFilter{Limit: 10})
	require.NoError(t, err)
	require.Len(t, events, 1)

	require.Regexp(t, `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$`, events[0].CreatedAt,
		"created_at must be RFC3339 UTC, not a zoneless SQLite string")

	single, err := db.GetAIEvent(ctx, events[0].ID)
	require.NoError(t, err)
	require.Regexp(t, `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$`, single.CreatedAt)
}

func TestAICreatedAtToRFC3339(t *testing.T) {
	cases := []struct{ in, want string }{
		{"2026-08-25 06:19:51", "2026-08-25T06:19:51Z"},
		{"2026-08-25 06:19:51.123456", "2026-08-25T06:19:51.123456Z"},
		{"2026-08-25T06:19:51Z", "2026-08-25T06:19:51Z"},
		{"2026-08-25T06:19:51+08:00", "2026-08-24T22:19:51Z"}, // offset honored, normalized to UTC
		{"", ""},
		{"garbage", "garbage"}, // unparsable passthrough, never mangled
	}
	for _, c := range cases {
		require.Equal(t, c.want, aiCreatedAtToRFC3339(c.in), "input %q", c.in)
	}
}

// v35 多实例归因:source(API Key 名)随事件落库、列表可按 source 过滤。
func TestAIEventSourceRoundTripAndFilter(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	mk := func(source string) *AIEvent {
		return &AIEvent{CameraID: "cam-1", EventType: "person", Severity: "info", Source: source}
	}
	_, err := db.InsertAIEvent(ctx, mk("jetson-y26"))
	require.NoError(t, err)
	_, err = db.InsertAIEvent(ctx, mk("vm-v8"))
	require.NoError(t, err)
	_, err = db.InsertAIEvent(ctx, mk("")) // 旧版消费者不带 key
	require.NoError(t, err)

	all, total, err := db.ListAIEvents(ctx, AIEventFilter{Limit: 10})
	require.NoError(t, err)
	require.Equal(t, 3, total)
	sources := map[string]bool{}
	for _, e := range all {
		sources[e.Source] = true
	}
	require.True(t, sources["jetson-y26"] && sources["vm-v8"] && sources[""],
		"all three sources must round-trip, got %v", sources)

	only, total, err := db.ListAIEvents(ctx, AIEventFilter{Limit: 10, Source: "vm-v8"})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Equal(t, "vm-v8", only[0].Source)
}

// 多实例聚合卫兵:completed 不被 failed/skipped/processing 降级;
// skipped 可被 completed 升级(补推后真实处理完成的场景)。
func TestUpdateRecordingAIStatusAggregateGuard(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	rec := &model.Recording{
		ID: "rec-guard", CameraID: "cam-1", FilePath: "/x/a.mp4",
		Format:    model.FormatH264,
		StartedAt: time.Now().Add(-time.Minute), EndedAt: time.Now(),
		MergeStatus: model.MergeStatusPending,
	}
	require.NoError(t, db.InsertRecording(ctx, rec))

	set := func(status string) string {
		require.NoError(t, db.UpdateRecordingAIStatus(ctx, "rec-guard", status, ""))
		got, err := db.GetRecordingAIStatus(ctx, "rec-guard")
		require.NoError(t, err)
		return got
	}

	require.Equal(t, "processing", set("processing"))
	require.Equal(t, "failed", set("failed"))
	// 另一实例 completed 胜出(最高优先级)。
	require.Equal(t, "completed", set("completed"))
	// completed 之后:failed/skipped/processing 全部不得降级。
	require.Equal(t, "completed", set("failed"))
	require.Equal(t, "completed", set("skipped"))
	require.Equal(t, "completed", set("processing"))

	// failed 之上 skipped 可写入(丢弃优先于历史失败);再被 completed 升级。
	rec2 := &model.Recording{
		ID: "rec-guard2", CameraID: "cam-1", FilePath: "/x/b.mp4",
		Format:    model.FormatH264,
		StartedAt: time.Now().Add(-time.Minute), EndedAt: time.Now(),
		MergeStatus: model.MergeStatusPending,
	}
	require.NoError(t, db.InsertRecording(ctx, rec2))
	set2 := func(status string) string {
		require.NoError(t, db.UpdateRecordingAIStatus(ctx, "rec-guard2", status, ""))
		got, err := db.GetRecordingAIStatus(ctx, "rec-guard2")
		require.NoError(t, err)
		return got
	}
	require.Equal(t, "failed", set2("failed"))
	require.Equal(t, "skipped", set2("skipped"), "skipped outranks failed")
	require.Equal(t, "skipped", set2("failed"))
	require.Equal(t, "completed", set2("completed"), "re-processing after a drop upgrades to completed")
	require.Equal(t, "completed", set2("skipped"))
}
