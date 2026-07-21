package api

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// timelapseMergesListResponse mirrors the GET /api/timelapse/merges payload.
type timelapseMergesListResponse struct {
	Merges []model.TimelapseMerge `json:"merges"`
	Total  int                    `json:"total"`
}

func seedTimelapseMerge(t *testing.T, db interface {
	InsertTimelapseMerge(ctx context.Context, m *model.TimelapseMerge) (int64, error)
}, m *model.TimelapseMerge,
) {
	t.Helper()
	if _, err := db.InsertTimelapseMerge(context.Background(), m); err != nil {
		t.Fatalf("seed timelapse merge: %v", err)
	}
}

func TestHandleListTimelapseMerges_Empty(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse/merges", nil, "", "")
	require.Equal(t, 200, rr.Code)
	var resp timelapseMergesListResponse
	parseJSON(t, rr, &resp)
	require.Equal(t, 0, resp.Total)
	require.Equal(t, []model.TimelapseMerge{}, resp.Merges, "empty list should be [] not nil")
}

func TestHandleListTimelapseMerges_FilterByCamera(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	windowStart := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	seedTimelapseMerge(t, db, &model.TimelapseMerge{
		CameraID: "cam-a", WindowStart: windowStart, WindowEnd: windowStart.Add(24 * time.Hour),
		DurationLabel: "natural-day", OutputPath: "/tmp/a.mp4", Codec: model.TimelapseMergeCodecH265, FPS: 30,
		Status: model.TimelapseMergeStatusCompleted,
	})
	seedTimelapseMerge(t, db, &model.TimelapseMerge{
		CameraID: "cam-b", WindowStart: windowStart, WindowEnd: windowStart.Add(24 * time.Hour),
		DurationLabel: "natural-day", OutputPath: "/tmp/b.mp4", Codec: model.TimelapseMergeCodecH264, FPS: 30,
		Status: model.TimelapseMergeStatusCompleted,
	})

	// No filter
	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse/merges", nil, "", "")
	require.Equal(t, 200, rr.Code)
	var resp timelapseMergesListResponse
	parseJSON(t, rr, &resp)
	require.Equal(t, 2, resp.Total)
	require.Len(t, resp.Merges, 2)

	// Filter by camera
	rr = doRequest(t, h.Routes(), "GET", "/api/timelapse/merges?camera_id=cam-a", nil, "", "")
	require.Equal(t, 200, rr.Code)
	resp = timelapseMergesListResponse{}
	parseJSON(t, rr, &resp)
	require.Equal(t, 1, resp.Total)
	require.Len(t, resp.Merges, 1)
	require.Equal(t, "cam-a", resp.Merges[0].CameraID)
}

func TestHandleGetTimelapseMerge_NotFound(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse/merges/99999", nil, "", "")
	require.Equal(t, 404, rr.Code)
}

func TestHandleGetTimelapseMerge_Found(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	windowStart := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	m := &model.TimelapseMerge{
		CameraID: "cam-get", WindowStart: windowStart, WindowEnd: windowStart.Add(8 * time.Hour),
		DurationLabel: "8h", OutputPath: "/tmp/get.mp4", Codec: model.TimelapseMergeCodecH265,
		FPS: 30, Status: model.TimelapseMergeStatusCompleted, FrameCount: 2400,
	}
	id, err := db.InsertTimelapseMerge(context.Background(), m)
	require.NoError(t, err)

	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse/merges/"+strconv.FormatInt(id, 10), nil, "", "")
	require.Equal(t, 200, rr.Code)
	var got model.TimelapseMerge
	parseJSON(t, rr, &got)
	require.Equal(t, "cam-get", got.CameraID)
	require.Equal(t, "8h", got.DurationLabel)
	require.Equal(t, model.TimelapseMergeCodecH265, got.Codec)
	require.Equal(t, 2400, got.FrameCount)
}

func TestHandleDownloadTimelapseMerge_ServesCodecHeader(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	// Write a real (empty) file at the output path so http.ServeFile succeeds.
	dir := t.TempDir()
	outFile := dir + "/get.mp4"
	require.NoError(t, os.WriteFile(outFile, []byte("fake mp4"), 0o644))

	windowStart := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	m := &model.TimelapseMerge{
		CameraID: "cam-dl", WindowStart: windowStart, WindowEnd: windowStart.Add(24 * time.Hour),
		DurationLabel: "natural-day", OutputPath: outFile, Codec: model.TimelapseMergeCodecH264,
		FPS: 30, Status: model.TimelapseMergeStatusCompleted,
	}
	id, err := db.InsertTimelapseMerge(context.Background(), m)
	require.NoError(t, err)

	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse/merges/"+strconv.FormatInt(id, 10)+"/download", nil, "", "")
	require.Equal(t, 200, rr.Code, "body: %s", rr.Body.String())
	require.Equal(t, model.TimelapseMergeCodecH264, rr.Header().Get("X-Timelapse-Codec"),
		"download response must surface codec for frontend player selection")
}

func TestHandleDownloadTimelapseMerge_NotCompleted(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	windowStart := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	m := &model.TimelapseMerge{
		CameraID: "cam-pending", WindowStart: windowStart, WindowEnd: windowStart.Add(24 * time.Hour),
		DurationLabel: "natural-day", OutputPath: "/tmp/none.mp4",
		Status: model.TimelapseMergeStatusPending,
	}
	id, err := db.InsertTimelapseMerge(context.Background(), m)
	require.NoError(t, err)

	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse/merges/"+strconv.FormatInt(id, 10)+"/download", nil, "", "")
	require.Equal(t, 404, rr.Code)
}

func TestHandleDeleteTimelapseMerge(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	// Create a real file to delete.
	dir := t.TempDir()
	outFile := dir + "/del.mp4"
	require.NoError(t, os.WriteFile(outFile, []byte("to be deleted"), 0o644))

	windowStart := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	m := &model.TimelapseMerge{
		CameraID: "cam-del", WindowStart: windowStart, WindowEnd: windowStart.Add(24 * time.Hour),
		DurationLabel: "natural-day", OutputPath: outFile,
		Status: model.TimelapseMergeStatusCompleted,
	}
	id, err := db.InsertTimelapseMerge(context.Background(), m)
	require.NoError(t, err)

	rr := doRequest(t, h.Routes(), "DELETE", "/api/timelapse/merges/"+strconv.FormatInt(id, 10), nil, "", "")
	require.Equal(t, 200, rr.Code)

	// DB row gone
	got, err := db.GetTimelapseMerge(context.Background(), id)
	require.Error(t, err)
	require.Nil(t, got)

	// File also gone (best-effort)
	_, statErr := os.Stat(outFile)
	require.True(t, os.IsNotExist(statErr), "output file should have been removed")
}
