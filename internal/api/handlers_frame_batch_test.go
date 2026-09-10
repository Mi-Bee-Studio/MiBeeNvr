// Multipart frame-batch endpoint tests: one HTTP request returns a batch of
// JPEG frames for MJPEG playback (merged timelapse outputs + raw recording
// directories + AVI files), replacing the per-frame GET cycler hot path.
package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/avi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
	"github.com/stretchr/testify/require"
)

// buildMJPAMergeFixture merges n distinct minimal JPEGs into a real mjpa MP4
// via the production GoMerger and returns its path plus the source frame bytes.
func buildMJPAMergeFixture(t *testing.T, dir string, n int) (string, [][]byte) {
	t.Helper()
	framesDir := filepath.Join(dir, "frames")
	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	frames := make([][]byte, n)
	for i := range n {
		frames[i] = []byte{0xFF, 0xD8, 0xFF, byte(0xA0 + i), byte(i), 0xFF, 0xD9}
		if err := os.WriteFile(filepath.Join(framesDir, fmt.Sprintf("frame_%06d.jpg", i+1)), frames[i], 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(dir, "periodic_test.mp4")
	_, err := timelapse.NewGoMerger().Merge(context.Background(), framesDir, out, 10)
	require.NoError(t, err, "GoMerger must produce a real mjpa MP4 for the fixture")
	return out, frames
}

// parseMultipartParts decodes a multipart/mixed response body into parts.
func parseMultipartParts(t *testing.T, resp *http.Response, body []byte) [][]byte {
	t.Helper()
	ct := resp.Header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(ct)
	require.NoError(t, err)
	require.Equal(t, "multipart/mixed", mediaType)
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	var parts [][]byte
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		data, err := io.ReadAll(p)
		require.NoError(t, err)
		parts = append(parts, data)
	}
	return parts
}

func TestHandleTimelapseMergeFrames_MultipartBatch(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	dir := t.TempDir()
	outPath, frames := buildMJPAMergeFixture(t, dir, 5)

	windowStart := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	id, err := db.InsertTimelapseMerge(context.Background(), &model.TimelapseMerge{
		CameraID: "cam-a", WindowStart: windowStart, WindowEnd: windowStart.Add(24 * time.Hour),
		DurationLabel: "natural-day", OutputPath: outPath, Codec: model.TimelapseMergeCodecMJPEG,
		FPS: 10, FrameCount: 5, Status: model.TimelapseMergeStatusCompleted,
	})
	require.NoError(t, err)

	resp := doRequest(t, h.Routes(), "GET", fmt.Sprintf("/api/timelapse/merges/%d/frames?offset=1&limit=2", id), nil, "", "")
	require.Equal(t, 200, resp.Code, "body: %s", resp.Body.String())

	require.Equal(t, "5", resp.Header().Get("X-Frame-Total"))
	require.Equal(t, "1", resp.Header().Get("X-Frame-Offset"))
	require.Equal(t, "2", resp.Header().Get("X-Frame-Count"))
	require.Equal(t, "10", resp.Header().Get("X-Frame-Fps"))

	parts := parseMultipartParts(t, resp.Result(), resp.Body.Bytes())
	require.Len(t, parts, 2)
	require.Equal(t, frames[1], parts[0], "offset=1 must return the 2nd frame")
	require.Equal(t, frames[2], parts[1])
}

func TestHandleTimelapseMergeFrames_EmptyTail(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	dir := t.TempDir()
	outPath, _ := buildMJPAMergeFixture(t, dir, 3)

	windowStart := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	id, err := db.InsertTimelapseMerge(context.Background(), &model.TimelapseMerge{
		CameraID: "cam-a", WindowStart: windowStart, WindowEnd: windowStart.Add(24 * time.Hour),
		DurationLabel: "natural-day", OutputPath: outPath, Codec: model.TimelapseMergeCodecMJPEG,
		FPS: 10, FrameCount: 3, Status: model.TimelapseMergeStatusCompleted,
	})
	require.NoError(t, err)

	// Offset beyond the end → 200 with an EMPTY batch (player stops cleanly).
	resp := doRequest(t, h.Routes(), "GET", fmt.Sprintf("/api/timelapse/merges/%d/frames?offset=99", id), nil, "", "")
	require.Equal(t, 200, resp.Code)
	require.Equal(t, "3", resp.Header().Get("X-Frame-Total"))
	require.Equal(t, "0", resp.Header().Get("X-Frame-Count"))
	parts := parseMultipartParts(t, resp.Result(), resp.Body.Bytes())
	require.Empty(t, parts)
}

func TestHandleTimelapseMergeFrames_H264Rejected(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	windowStart := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	id, err := db.InsertTimelapseMerge(context.Background(), &model.TimelapseMerge{
		CameraID: "cam-a", WindowStart: windowStart, WindowEnd: windowStart.Add(24 * time.Hour),
		DurationLabel: "natural-day", OutputPath: "/tmp/x.mp4", Codec: model.TimelapseMergeCodecH264,
		FPS: 30, Status: model.TimelapseMergeStatusCompleted,
	})
	require.NoError(t, err)

	resp := doRequest(t, h.Routes(), "GET", fmt.Sprintf("/api/timelapse/merges/%d/frames", id), nil, "", "")
	require.Equal(t, http.StatusNotFound, resp.Code)
}

func TestHandleTimelapseMergeFrames_LimitClamped(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	dir := t.TempDir()
	outPath, frames := buildMJPAMergeFixture(t, dir, 5)

	windowStart := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	id, err := db.InsertTimelapseMerge(context.Background(), &model.TimelapseMerge{
		CameraID: "cam-a", WindowStart: windowStart, WindowEnd: windowStart.Add(24 * time.Hour),
		DurationLabel: "natural-day", OutputPath: outPath, Codec: model.TimelapseMergeCodecMJPEG,
		FPS: 10, FrameCount: 5, Status: model.TimelapseMergeStatusCompleted,
	})
	require.NoError(t, err)

	// limit=9999 clamps to the 5 available frames.
	resp := doRequest(t, h.Routes(), "GET", fmt.Sprintf("/api/timelapse/merges/%d/frames?limit=9999", id), nil, "", "")
	require.Equal(t, 200, resp.Code)
	require.Equal(t, "5", resp.Header().Get("X-Frame-Count"))
	parts := parseMultipartParts(t, resp.Result(), resp.Body.Bytes())
	require.Len(t, parts, 5)
	require.Equal(t, frames[0], parts[0])
}

// --- Recordings batch endpoint (mjpeg/timelapse dirs + AVI) ---

func TestHandleTimelapseFramesBatch_MJPEGDir(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	dir := t.TempDir()
	segDir := filepath.Join(dir, "seg")
	if err := os.MkdirAll(segDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Timestamped names like storage.Manager.WriteFrame produces.
	base := time.Date(2026, 9, 9, 10, 0, 0, 0, time.Local)
	frames := make([][]byte, 4)
	names := make([]string, 4)
	for i := range 4 {
		frames[i] = []byte{0xFF, 0xD8, byte(0xB0 + i), 0xFF, 0xD9}
		names[i] = base.Add(time.Duration(i)*time.Second).Format("20060102_150405.000") + ".jpg"
		if err := os.WriteFile(filepath.Join(segDir, names[i]), frames[i], 0o644); err != nil {
			t.Fatal(err)
		}
	}

	rec := &model.Recording{
		ID: "rec-batch-1", CameraID: "cam-a", FilePath: segDir, Format: model.FormatMJPEG,
		StartedAt: base, EndedAt: base.Add(4 * time.Second), Duration: 4,
	}
	require.NoError(t, db.InsertRecording(context.Background(), rec))

	resp := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-batch-1/timelapse-frames/batch?offset=1&limit=2", nil, "", "")
	require.Equal(t, 200, resp.Code, "body: %s", resp.Body.String())
	require.Equal(t, "4", resp.Header().Get("X-Frame-Total"))
	require.Equal(t, "1", resp.Header().Get("X-Frame-Offset"))
	require.Equal(t, "2", resp.Header().Get("X-Frame-Count"))

	parts := parseMultipartParts(t, resp.Result(), resp.Body.Bytes())
	require.Len(t, parts, 2)
	require.Equal(t, frames[1], parts[0])
	require.Equal(t, frames[2], parts[1])
}

func TestHandleTimelapseFramesBatch_AVI(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	dir := t.TempDir()
	aviPath := filepath.Join(dir, "rec.avi")
	frames := make([][]byte, 3)
	{
		var buf bytes.Buffer
		m := avi.NewVideoOnlyMuxer(&buf, 64, 48)
		for i := range 3 {
			frames[i] = []byte{0xFF, 0xD8, 0xFF, byte(0xC0 + i), 0xFF, 0xD9}
			require.NoError(t, m.WriteVideo(frames[i], int64(i)*33333))
		}
		require.NoError(t, m.Close())
		require.NoError(t, os.WriteFile(aviPath, buf.Bytes(), 0o644))
	}

	started := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	require.NoError(t, db.InsertRecording(context.Background(), &model.Recording{
		ID: "rec-avi-1", CameraID: "cam-a", FilePath: aviPath, Format: model.FormatAVI,
		StartedAt: started, EndedAt: started.Add(time.Second), Duration: 1,
	}))

	resp := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-avi-1/timelapse-frames/batch", nil, "", "")
	require.Equal(t, 200, resp.Code, "body: %s", resp.Body.String())
	require.Equal(t, "3", resp.Header().Get("X-Frame-Total"))

	parts := parseMultipartParts(t, resp.Result(), resp.Body.Bytes())
	require.Len(t, parts, 3)
	// AVI chunks store the exact JPEG bytes written via WriteVideo.
	require.Equal(t, frames[0], parts[0])
	require.Equal(t, frames[2], parts[2])
}

func TestHandleTimelapseFramesBatch_NotFound(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	// Missing recording.
	resp := doRequest(t, h.Routes(), "GET", "/api/recordings/nope/timelapse-frames/batch", nil, "", "")
	require.Equal(t, http.StatusNotFound, resp.Code)

	// Wrong format (h264 recording) → 404, not 500.
	started := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	require.NoError(t, db.InsertRecording(context.Background(), &model.Recording{
		ID: "rec-h264", CameraID: "cam-a", FilePath: "/tmp/nope.mp4", Format: model.FormatH264,
		StartedAt: started, EndedAt: started.Add(time.Second), Duration: 1,
	}))
	resp = doRequest(t, h.Routes(), "GET", "/api/recordings/rec-h264/timelapse-frames/batch", nil, "", "")
	require.Equal(t, http.StatusNotFound, resp.Code)
}

// TestHandleTimelapseFramesBatch_BadParams verifies invalid query params are
// rejected rather than panicking.
func TestHandleTimelapseFramesBatch_BadParams(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	dir := t.TempDir()
	segDir := filepath.Join(dir, "seg")
	if err := os.MkdirAll(segDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(segDir, "20260909_100000.000.jpg"), []byte{0xFF, 0xD8, 0xFF, 0xD9}, 0o644); err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	require.NoError(t, db.InsertRecording(context.Background(), &model.Recording{
		ID: "rec-bad", CameraID: "cam-a", FilePath: segDir, Format: model.FormatMJPEG,
		StartedAt: started, EndedAt: started.Add(time.Second), Duration: 1,
	}))

	for _, q := range []string{"offset=-5", "limit=0", "offset=abc", "limit=xyz"} {
		resp := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-bad/timelapse-frames/batch?"+q, nil, "", "")
		require.Equal(t, http.StatusBadRequest, resp.Code, "query %q", q)
	}
}
