package api

// Tests for the uncovered long tail of handlers_recording.go (#578):
// timeline segments / daily summary, MiBeeVision recording CRUD, timeline
// seek, timeline gaps, and timelapse/AVI frame serving. All hermetic
// (temp DB + temp files; AVI built with the internal avi muxer).

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/avi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

func TestIsTimelapseFrame(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]bool{
		"frame_1.jpg": true, "f2.jpeg": true, "seg.h264": true, "seg.h265": true,
		"notes.txt": false, "plan.mp4": false, "frame.PNG": false,
	} {
		require.Equal(t, want, isTimelapseFrame(name), name)
	}
}

func TestParseDateParts(t *testing.T) {
	t.Parallel()
	y, m, d, err := parseDateParts("2026-08-20")
	require.NoError(t, err)
	require.Equal(t, 2026, y)
	require.Equal(t, 8, m)
	require.Equal(t, 20, d)

	for _, bad := range []string{"2026-08", "2026/08/20", "aaaa-bb-cc", ""} {
		_, _, _, err := parseDateParts(bad)
		require.Error(t, err, bad)
	}
}

func TestTimelineSegments(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	day := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	seedRecording(t, db, makeRecording("seg-1", "cam-1", "h264", day, false))
	seedRecording(t, db, makeRecording("seg-2", "cam-1", "h264", day.Add(time.Hour), true))

	rr := doRequest(t, h.Routes(), http.MethodGet,
		"/api/recordings/timeline?camera_id=cam-1&start=2026-08-20T00:00:00Z&end=2026-08-21T00:00:00Z", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var resp struct {
		Segments  []model.TimelineSegment `json:"segments"`
		Total     int                     `json:"total"`
		Truncated bool                    `json:"truncated"`
	}
	parseJSON(t, rr, &resp)
	require.Len(t, resp.Segments, 2)
	require.Equal(t, 2, resp.Total)
	require.False(t, resp.Truncated)

	// Empty range → non-nil empty array.
	rr = doRequest(t, h.Routes(), http.MethodGet,
		"/api/recordings/timeline?camera_id=ghost&start=2026-08-20T00:00:00Z&end=2026-08-21T00:00:00Z", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"segments":[]`)
}

func TestDailyRecordingSummary(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	day := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	seedRecording(t, db, makeRecording("sum-1", "cam-1", "h264", day, false))
	seedRecording(t, db, makeRecording("sum-2", "cam-1", "mjpeg", day.Add(2*time.Hour), false))

	rr := doRequest(t, h.Routes(), http.MethodGet,
		"/api/recordings/daily-summary?start=2026-08-20T00:00:00Z&end=2026-08-21T00:00:00Z&formats=h264,mjpeg&tz_offset=480", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"days":`)
	require.Contains(t, rr.Body.String(), "2026-08-20")

	// No data → empty array, not null.
	rr = doRequest(t, h.Routes(), http.MethodGet,
		"/api/recordings/daily-summary?start=2027-01-01T00:00:00Z&end=2027-01-02T00:00:00Z", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"days":[]`)
}

func TestRecordingCRUD_VisionPaths(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	routes := withAPIKey("vision", h.Routes())

	// Without API-key context → 401.
	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/recordings", strings.NewReader(`{}`), "", "")
	require.Equal(t, http.StatusUnauthorized, rr.Code)

	// Bad body / missing fields.
	rr = doRequest(t, routes, http.MethodPost, "/api/recordings", strings.NewReader(`{bad`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, routes, http.MethodPost, "/api/recordings", strings.NewReader(`{"camera_id":"c"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	// Create happy path.
	body := `{"id":"vis-1","camera_id":"cam-1","file_path":"/x/a.mp4","format":"h264",
		"started_at":"2026-08-20T10:00:00Z","ended_at":"2026-08-20T10:05:00Z","duration":300}`
	rr = doRequest(t, routes, http.MethodPost, "/api/recordings", strings.NewReader(body), "", "")
	require.Equal(t, http.StatusCreated, rr.Code)
	require.Contains(t, rr.Body.String(), "vis-1")

	// PATCH: unknown → 404, known → update applied.
	rr = doRequest(t, routes, http.MethodPatch, "/api/recordings/nope", strings.NewReader(`{}`), "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
	rr = doRequest(t, routes, http.MethodPatch, "/api/recordings/vis-1",
		strings.NewReader(`{"duration":42,"frame_count":7,"file_size":100}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	rec, err := db.GetRecording(context.Background(), "vis-1")
	require.NoError(t, err)
	require.NotNil(t, rec)
	require.Equal(t, 42.0, rec.Duration)
	require.Equal(t, 7, rec.FrameCount)
	require.Equal(t, int64(100), rec.FileSize)

	// AI status: invalid value / unknown recording / happy path.
	rr = doRequest(t, routes, http.MethodPatch, "/api/recordings/vis-1/ai-status",
		strings.NewReader(`{"ai_status":"weird"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, routes, http.MethodPatch, "/api/recordings/nope/ai-status",
		strings.NewReader(`{"ai_status":"completed"}`), "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
	rr = doRequest(t, routes, http.MethodPatch, "/api/recordings/vis-1/ai-status",
		strings.NewReader(`{"ai_status":"completed"}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	rec, err = db.GetRecording(context.Background(), "vis-1")
	require.NoError(t, err)
	require.Equal(t, "completed", rec.AIStatus)
}

func TestRecordingAIStatus_SkippedAccepted(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	routes := withAPIKey("vision", h.Routes())
	seedRecording(t, db, makeRecording("skip-1", "cam-1", "mp4", time.Now(), false))

	// 'skipped' = consumer-reported queue drop (#671): terminal, stamps
	// ai_processed_at, carries the reason in ai_error.
	rr := doRequest(t, routes, http.MethodPatch, "/api/recordings/skip-1/ai-status",
		strings.NewReader(`{"ai_status":"skipped","ai_error":"vision drop:queue_full"}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	rec, err := db.GetRecording(context.Background(), "skip-1")
	require.NoError(t, err)
	require.Equal(t, "skipped", rec.AIStatus)
	require.Equal(t, "vision drop:queue_full", rec.AIError)
	require.NotNil(t, rec.AIProcessedAt)
}

func TestTimelineSeekEvent(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/recordings/timeline/seek-event",
		strings.NewReader(`{bad`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	rr = doRequest(t, h.Routes(), http.MethodPost, "/api/recordings/timeline/seek-event",
		strings.NewReader(`{"camera_id":"cam-1","type":"intra"}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "recorded")

	// Missing camera/type default to "unknown"/"segment" — still 200.
	rr = doRequest(t, h.Routes(), http.MethodPost, "/api/recordings/timeline/seek-event",
		strings.NewReader(`{}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
}

func TestTimelineGaps(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	// Missing date → 400; malformed date → 400.
	rr := doRequest(t, h.Routes(), http.MethodGet, "/api/cameras/cam-1/timeline/gaps", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, h.Routes(), http.MethodGet, "/api/cameras/cam-1/timeline/gaps?date=2026/08/20", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	day := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	r1 := makeRecording("g1", "cam-1", "h264", day.Add(1*time.Hour), false)
	r1.EndedAt = r1.StartedAt.Add(10 * time.Minute)
	r2 := makeRecording("g2", "cam-1", "h264", day.Add(2*time.Hour), false)
	r2.EndedAt = r2.StartedAt.Add(10 * time.Minute)
	seedRecording(t, db, r1)
	seedRecording(t, db, r2)

	// Default min_gap=30s → the 50-minute hole between r1.end and r2.start.
	rr = doRequest(t, h.Routes(), http.MethodGet, "/api/cameras/cam-1/timeline/gaps?date=2026-08-20", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var resp struct {
		Gaps []struct {
			Start    string  `json:"start"`
			End      string  `json:"end"`
			Duration float64 `json:"duration"`
		} `json:"gaps"`
		TotalGaps int `json:"total_gaps"`
	}
	parseJSON(t, rr, &resp)
	require.Equal(t, 1, resp.TotalGaps)
	require.InDelta(t, 3000.0, resp.Gaps[0].Duration, 0.1)

	// min_gap larger than the hole → no gaps.
	rr = doRequest(t, h.Routes(), http.MethodGet, "/api/cameras/cam-1/timeline/gaps?date=2026-08-20&min_gap=2h", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"total_gaps":0`)

	// Garbage min_gap falls back to 30s without error.
	rr = doRequest(t, h.Routes(), http.MethodGet, "/api/cameras/cam-1/timeline/gaps?date=2026-08-20&min_gap=nonsense", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
}

func TestTimelapseFrames_ListAndServe(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	framesDir := t.TempDir()
	for _, n := range []string{"frame_20260820_100001.jpg", "frame_20260820_100002.jpg", "notes.txt"} {
		require.NoError(t, os.WriteFile(filepath.Join(framesDir, n), []byte("jpeg-bytes-"+n), 0o644))
	}

	rec := makeRecording("tl-1", "cam-1", "timelapse", time.Now(), false)
	rec.FilePath = framesDir
	seedRecording(t, db, rec)
	seedRecording(t, db, makeRecording("h264-1", "cam-1", "h264", time.Now(), false))

	// Listing: only frame files, sorted.
	rr := doRequest(t, h.Routes(), http.MethodGet, "/api/recordings/tl-1/timelapse-frames", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var frames []struct {
		Filename string `json:"filename"`
		URL      string `json:"url"`
	}
	parseJSON(t, rr, &frames)
	require.Len(t, frames, 2)
	require.Equal(t, "frame_20260820_100001.jpg", frames[0].Filename)
	require.Contains(t, frames[0].URL, "/api/recordings/tl-1/timelapse-frames/")

	// Serving one frame returns its bytes.
	rr = doRequest(t, h.Routes(), http.MethodGet, "/api/recordings/tl-1/timelapse-frames/frame_20260820_100001.jpg", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "jpeg-bytes-frame_20260820_100001.jpg", rr.Body.String())

	// Guards: bad filename chars → 400; dot/dotdot → 400; unknown recording → 404;
	// non-timelapse format → 404.
	rr = doRequest(t, h.Routes(), http.MethodGet, "/api/recordings/tl-1/timelapse-frames/bad%2Fname.jpg", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, h.Routes(), http.MethodGet, "/api/recordings/tl-1/timelapse-frames/..", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, h.Routes(), http.MethodGet, "/api/recordings/nope/timelapse-frames", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
	rr = doRequest(t, h.Routes(), http.MethodGet, "/api/recordings/h264-1/timelapse-frames", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
	require.Contains(t, rr.Body.String(), "not a timelapse")

	// Recording whose directory vanished → 404.
	gone := makeRecording("tl-gone", "cam-1", "timelapse", time.Now(), false)
	gone.FilePath = filepath.Join(t.TempDir(), "does-not-exist")
	seedRecording(t, db, gone)
	rr = doRequest(t, h.Routes(), http.MethodGet, "/api/recordings/tl-gone/timelapse-frames", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	// FilePath pointing at a regular file (not a dir) → 404.
	notDir := makeRecording("tl-file", "cam-1", "timelapse", time.Now(), false)
	notDir.FilePath = filepath.Join(t.TempDir(), "plain.jpg")
	require.NoError(t, os.WriteFile(notDir.FilePath, []byte("x"), 0o644))
	seedRecording(t, db, notDir)
	rr = doRequest(t, h.Routes(), http.MethodGet, "/api/recordings/tl-file/timelapse-frames", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

// buildTestAVI writes a small AVI with n video frames (synthetic JPEG
// payloads) and returns its path.
func buildTestAVI(t *testing.T, n int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "seg.avi")
	f, err := os.Create(p)
	require.NoError(t, err)
	defer f.Close()
	mux := avi.NewMuxer(f, 64, 48, 8000, true)
	for i := range n {
		frame := []byte(fmt.Sprintf("jpeg-frame-%02d-", i) + strings.Repeat("x", 32))
		require.NoError(t, mux.WriteVideo(frame, int64(i)*1_000_000))
	}
	require.NoError(t, mux.Close())
	return p
}

func TestAVIFrames_ListAndServe(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	rec := makeRecording("avi-1", "cam-1", "avi", time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC), false)
	rec.FilePath = buildTestAVI(t, 3)
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), http.MethodGet, "/api/recordings/avi-1/timelapse-frames", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var frames []struct {
		Filename string `json:"filename"`
		Size     int64  `json:"size"`
	}
	parseJSON(t, rr, &frames)
	require.Len(t, frames, 3)
	require.Equal(t, "f000000.jpg", frames[0].Filename)
	require.Positive(t, frames[0].Size)

	// Serve frame 1: content matches the muxed payload prefix.
	rr = doRequest(t, h.Routes(), http.MethodGet, "/api/recordings/avi-1/timelapse-frames/f000001.jpg", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "jpeg-frame-01")

	// Out-of-range and malformed names.
	rr = doRequest(t, h.Routes(), http.MethodGet, "/api/recordings/avi-1/timelapse-frames/f000099.jpg", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
	rr = doRequest(t, h.Routes(), http.MethodGet, "/api/recordings/avi-1/timelapse-frames/notafnumber.jpg", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	// Missing file on disk → 404.
	gone := makeRecording("avi-gone", "cam-1", "avi", time.Now(), false)
	gone.FilePath = filepath.Join(t.TempDir(), "missing.avi")
	seedRecording(t, db, gone)
	rr = doRequest(t, h.Routes(), http.MethodGet, "/api/recordings/avi-gone/timelapse-frames", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}
