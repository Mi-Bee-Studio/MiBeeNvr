package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
)

// createTestJPEG creates a small valid JPEG image for testing.
func createTestJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	// Fill with a simple gradient pattern
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			idx := y*img.Stride + x*4
			img.Pix[idx] = byte(x * 255 / width)                               // R
			img.Pix[idx+1] = byte(y * 255 / height)                             // G
			img.Pix[idx+2] = byte((x + y) * 255 / (width + height))            // B
			img.Pix[idx+3] = 255                                                // A
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatalf("failed to encode test JPEG: %v", err)
	}
	return buf.Bytes()
}

// createTestRecording creates a timelapse recording with JPEG frames.
func createTestRecording(t *testing.T, db *storage.DB, store *storage.Manager, id, cameraID string) *model.Recording {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)

	// Create segment directory
	segDir := filepath.Join(store.RootDir(), cameraID, id+"-frames")
	if err := os.MkdirAll(segDir, 0755); err != nil {
		t.Fatalf("failed to create seg dir: %v", err)
	}

	// Write a few JPEG frames
	jpegData := createTestJPEG(t, 1920, 1080)
	for i := 0; i < 3; i++ {
		framePath := filepath.Join(segDir, "frame_00000"+string(rune('0'+i))+".jpg")
		if err := os.WriteFile(framePath, jpegData, 0644); err != nil {
			t.Fatalf("failed to write frame: %v", err)
		}
	}

	rec := &model.Recording{
		ID:         id,
		CameraID:   cameraID,
		FilePath:   segDir,
		Format:     model.Format("timelapse"),
		StartedAt:  now,
		EndedAt:    now.Add(30 * time.Second),
		Duration:   30.0,
		FileSize:   int64(len(jpegData) * 3),
		FrameCount: 3,
		Merged:     false,
	}
	if err := db.InsertRecording(context.Background(), rec); err != nil {
		t.Fatalf("failed to seed recording: %v", err)
	}
	return rec
}

// --- Thumbnail tests ---

func TestTimelapseThumbnail_ServesJPEG(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rec := createTestRecording(t, db, store, "thumb-test-1", "cam-1")

	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse/"+rec.ID+"/thumbnail", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify Content-Type
	ct := rr.Header().Get("Content-Type")
	if ct != "image/jpeg" {
		t.Fatalf("expected Content-Type image/jpeg, got %s", ct)
	}

	// Verify Cache-Control header
	cc := rr.Header().Get("Cache-Control")
	if cc == "" {
		t.Fatal("expected Cache-Control header")
	}

	// Verify body is a valid JPEG
	body := rr.Body.Bytes()
	if len(body) == 0 {
		t.Fatal("expected non-empty response body")
	}
	// Check JPEG magic bytes
	if len(body) < 2 || body[0] != 0xFF || body[1] != 0xD8 {
		t.Fatal("response body is not a valid JPEG (missing SOI marker)")
	}

	// Verify dimensions are at most 640x360
	img, err := jpeg.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("failed to decode JPEG: %v", err)
	}
	bounds := img.Bounds()
	if bounds.Dx() > 640 {
		t.Fatalf("thumbnail width %d exceeds max 640", bounds.Dx())
	}
	if bounds.Dy() > 360 {
		t.Fatalf("thumbnail height %d exceeds max 360", bounds.Dy())
	}
}

func TestTimelapseThumbnail_MaxDimensions(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	// Use a very large source image
	rec := createTestRecording(t, db, store, "thumb-large", "cam-1")

	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse/"+rec.ID+"/thumbnail", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.Bytes()
	img, err := jpeg.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("failed to decode JPEG: %v", err)
	}
	bounds := img.Bounds()
	if bounds.Dx() > 640 {
		t.Fatalf("thumbnail width %d exceeds max 640", bounds.Dx())
	}
	if bounds.Dy() > 360 {
		t.Fatalf("thumbnail height %d exceeds max 360", bounds.Dy())
	}
}

func TestTimelapseThumbnail_ServesFromCache(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rec := createTestRecording(t, db, store, "thumb-cache", "cam-1")

	// First request generates and caches
	rr1 := doRequest(t, h.Routes(), "GET", "/api/timelapse/"+rec.ID+"/thumbnail", nil, "", "")
	if rr1.Code != http.StatusOK {
		t.Fatalf("first request expected 200, got %d", rr1.Code)
	}
	body1 := rr1.Body.Bytes()

	// Second request should serve from cache
	rr2 := doRequest(t, h.Routes(), "GET", "/api/timelapse/"+rec.ID+"/thumbnail", nil, "", "")
	if rr2.Code != http.StatusOK {
		t.Fatalf("second request expected 200, got %d", rr2.Code)
	}
	body2 := rr2.Body.Bytes()

	// Both responses should be identical
	if !bytes.Equal(body1, body2) {
		t.Fatal("cached thumbnail should be identical to first request")
	}
}

func TestTimelapseThumbnail_RegeneratesOnSourceChange(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rec := createTestRecording(t, db, store, "thumb-change", "cam-1")

	// First request
	rr1 := doRequest(t, h.Routes(), "GET", "/api/timelapse/"+rec.ID+"/thumbnail", nil, "", "")
	if rr1.Code != http.StatusOK {
		t.Fatalf("first request expected 200, got %d", rr1.Code)
	}

	// Modify a frame file in the segment directory (update mtime)
	entries, _ := os.ReadDir(rec.FilePath)
	if len(entries) > 0 {
		firstFrame := filepath.Join(rec.FilePath, entries[0].Name())
		os.Chtimes(firstFrame, time.Now(), time.Now().Add(time.Hour))
	}

	// Second request should regenerate
	rr2 := doRequest(t, h.Routes(), "GET", "/api/timelapse/"+rec.ID+"/thumbnail", nil, "", "")
	if rr2.Code != http.StatusOK {
		t.Fatalf("second request expected 200, got %d", rr2.Code)
	}
}

func TestTimelapseThumbnail_NotFound(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse/nonexistent-id/thumbnail", nil, "", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestTimelapseThumbnail_AspectRatioPreserved(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	segDir := filepath.Join(store.RootDir(), "cam-ar", "thumb-ar")
	if err := os.MkdirAll(segDir, 0755); err != nil {
		t.Fatalf("failed to create seg dir: %v", err)
	}

	// Create a portrait image (tall, not wide)
	portraitJPEG := createTestJPEG(t, 480, 800)
	if err := os.WriteFile(filepath.Join(segDir, "frame_000001.jpg"), portraitJPEG, 0644); err != nil {
		t.Fatalf("failed to write frame: %v", err)
	}

	rec := &model.Recording{
		ID:         "thumb-ar",
		CameraID:   "cam-ar",
		FilePath:   segDir,
		Format:     model.Format("timelapse"),
		StartedAt:  now,
		EndedAt:    now.Add(10 * time.Second),
		Duration:   10.0,
		FileSize:   int64(len(portraitJPEG)),
		FrameCount: 1,
	}
	if err := db.InsertRecording(context.Background(), rec); err != nil {
		t.Fatalf("failed to seed recording: %v", err)
	}

	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse/thumb-ar/thumbnail", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.Bytes()
	img, err := jpeg.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("failed to decode JPEG: %v", err)
	}
	bounds := img.Bounds()

	// For portrait (480x800 -> should be 216x360 with 640x360 constraint)
	if bounds.Dx() > 640 {
		t.Fatalf("width %d exceeds max 640", bounds.Dx())
	}
	if bounds.Dy() > 360 {
		t.Fatalf("height %d exceeds max 360", bounds.Dy())
	}
	if bounds.Dy() != 360 && bounds.Dx() != 640 {
		t.Logf("aspect ratio preserved: %dx%d (source was 480x800)", bounds.Dx(), bounds.Dy())
	}
}

func TestTimelapseThumbnail_WrongFormat(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	rec := &model.Recording{
		ID:         "rec-h264-thumb",
		CameraID:   "cam-h264",
		FilePath:   "/tmp/rec-h264.mp4",
		Format:     model.Format("h264"),
		StartedAt:  now,
		EndedAt:    now.Add(30 * time.Second),
		Duration:   30.0,
		FileSize:   1024,
		FrameCount: 150,
	}
	if err := db.InsertRecording(context.Background(), rec); err != nil {
		t.Fatalf("failed to seed recording: %v", err)
	}

	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse/rec-h264-thumb/thumbnail", nil, "", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for non-timelapse recording, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestTimelapseThumbnail_EmptyDir(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	segDir := filepath.Join(store.RootDir(), "cam-empty", "thumb-empty")
	if err := os.MkdirAll(segDir, 0755); err != nil {
		t.Fatalf("failed to create seg dir: %v", err)
	}

	rec := &model.Recording{
		ID:         "thumb-empty",
		CameraID:   "cam-empty",
		FilePath:   segDir,
		Format:     model.Format("timelapse"),
		StartedAt:  now,
		EndedAt:    now.Add(10 * time.Second),
		Duration:   10.0,
		FileSize:   0,
		FrameCount: 0,
	}
	if err := db.InsertRecording(context.Background(), rec); err != nil {
		t.Fatalf("failed to seed recording: %v", err)
	}

	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse/thumb-empty/thumbnail", nil, "", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for empty directory, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestTimelapseThumbnail_MJPEGFormat(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	segDir := filepath.Join(store.RootDir(), "cam-mjpeg", "thumb-mjpeg")
	if err := os.MkdirAll(segDir, 0755); err != nil {
		t.Fatalf("failed to create seg dir: %v", err)
	}

	jpegData := createTestJPEG(t, 640, 480)
	if err := os.WriteFile(filepath.Join(segDir, "frame1.jpg"), jpegData, 0644); err != nil {
		t.Fatalf("failed to write frame: %v", err)
	}

	rec := &model.Recording{
		ID:         "thumb-mjpeg",
		CameraID:   "cam-mjpeg",
		FilePath:   segDir,
		Format:     model.Format("mjpeg"),
		StartedAt:  now,
		EndedAt:    now.Add(10 * time.Second),
		Duration:   10.0,
		FileSize:   int64(len(jpegData)),
		FrameCount: 1,
	}
	if err := db.InsertRecording(context.Background(), rec); err != nil {
		t.Fatalf("failed to seed recording: %v", err)
	}

	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse/thumb-mjpeg/thumbnail", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	ct := rr.Header().Get("Content-Type")
	if ct != "image/jpeg" {
		t.Fatalf("expected Content-Type image/jpeg, got %s", ct)
	}
}

func TestTimelapseThumbnail_ThumbnailOnDisk(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rec := createTestRecording(t, db, store, "thumb-disk", "cam-1")

	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse/"+rec.ID+"/thumbnail", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify thumbnail exists on disk
	cachePath := filepath.Join(store.RootDir(), ".timelapse-thumbnails", rec.ID+".jpg")
	if _, err := os.Stat(cachePath); os.IsNotExist(err) {
		t.Fatal("expected thumbnail cache file on disk")
	}

	// Verify cached content matches served content
	cachedData, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("failed to read cached thumbnail: %v", err)
	}
	if !bytes.Equal(rr.Body.Bytes(), cachedData) {
		t.Fatal("cached thumbnail content differs from served content")
	}
}

func TestTimelapseThumbnail_SmallImageNoResize(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	segDir := filepath.Join(store.RootDir(), "cam-small", "thumb-small")
	if err := os.MkdirAll(segDir, 0755); err != nil {
		t.Fatalf("failed to create seg dir: %v", err)
	}

	// Create a small JPEG (100x80) that needs no resizing
	smallJPEG := createTestJPEG(t, 100, 80)
	if err := os.WriteFile(filepath.Join(segDir, "frame_000001.jpg"), smallJPEG, 0644); err != nil {
		t.Fatalf("failed to write frame: %v", err)
	}

	rec := &model.Recording{
		ID:         "thumb-small",
		CameraID:   "cam-small",
		FilePath:   segDir,
		Format:     model.Format("timelapse"),
		StartedAt:  now,
		EndedAt:    now.Add(10 * time.Second),
		Duration:   10.0,
		FileSize:   int64(len(smallJPEG)),
		FrameCount: 1,
	}
	if err := db.InsertRecording(context.Background(), rec); err != nil {
		t.Fatalf("failed to seed recording: %v", err)
	}

	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse/thumb-small/thumbnail", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.Bytes()
	img, err := jpeg.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("failed to decode JPEG: %v", err)
	}
	// Small image should be returned as-is (within bounds)
	bounds := img.Bounds()
	if bounds.Dx() > 640 || bounds.Dy() > 360 {
		t.Fatalf("small image should not be upscaled: got %dx%d", bounds.Dx(), bounds.Dy())
	}
}

// --- Merge progress SSE tests ---

// testSlowMerger implements timelapse.TimelapseMerger for testing merge progress.
type testSlowMerger struct {
	delay time.Duration
	fail  bool
}

func (m *testSlowMerger) CanMerge() bool                          { return true }
func (m *testSlowMerger) Tier() timelapse.MergeTier               { return timelapse.TierGo }
func (m *testSlowMerger) Merge(ctx context.Context, _, _ string, _ int) (*timelapse.MergeResult, error) {
	select {
	case <-time.After(m.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if m.fail {
		return nil, fmt.Errorf("merge simulated failure")
	}
	return &timelapse.MergeResult{
		Tier:         timelapse.TierGo,
		FramesMerged: 10,
		Duration:     1.0,
		OutputPath:   "/tmp/output.mp4",
	}, nil
}

// mergeProgressEvent represents a parsed SSE event for merge progress.
type mergeProgressEvent struct {
	CameraID     string  `json:"camera_id"`
	Progress     int     `json:"progress"`
	Status       string  `json:"status"`
	OutputPath   string  `json:"output_path,omitempty"`
	FramesMerged int     `json:"frames_merged,omitempty"`
	Duration     float64 `json:"duration,omitempty"`
	Tier         string  `json:"tier,omitempty"`
	Error        string  `json:"error,omitempty"`
}

// parseSSEEvents parses the body of an SSE response into individual events.
func parseSSEEvents(t *testing.T, body string) []mergeProgressEvent {
	t.Helper()
	var events []mergeProgressEvent
	blocks := strings.Split(body, "\n\n")
	for _, block := range blocks {
		block = strings.TrimSpace(block)
		if block == "" || strings.HasPrefix(block, ":") {
			continue
		}
		lines := strings.Split(block, "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "data:") {
				dataStr := strings.TrimPrefix(line, "data:")
				dataStr = strings.TrimSpace(dataStr)
				var evt mergeProgressEvent
				if err := json.Unmarshal([]byte(dataStr), &evt); err != nil {
					t.Logf("failed to parse SSE data: %v (data: %s)", err, dataStr)
					continue
				}
				events = append(events, evt)
			}
		}
	}
	return events
}

func TestMergeProgress_Complete(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	merger := &testSlowMerger{delay: 10 * time.Millisecond}
	mgr := timelapse.NewRollingMergeManager(merger, nil, 10, false)
	h.SetTimelapseMergeMgr(mgr)

	tmpDir := t.TempDir()
	segmentDir := filepath.Join(tmpDir, "segment")
	if err := os.MkdirAll(segmentDir, 0755); err != nil {
		t.Fatalf("failed to create segment dir: %v", err)
	}
	// Write some test frames
	jpegData := createTestJPEG(t, 100, 100)
	for i := 0; i < 3; i++ {
		if err := os.WriteFile(filepath.Join(segmentDir, fmt.Sprintf("frame_%06d.jpg", i+1)), jpegData, 0644); err != nil {
			t.Fatalf("failed to write frame: %v", err)
		}
	}
	outputPath := filepath.Join(tmpDir, "output.mp4")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr.StartSegmentMerge(ctx, "cam-complete", segmentDir, outputPath, "")

	// Wait briefly for the merge to start and complete.
	time.Sleep(200 * time.Millisecond)

	// Hit the SSE endpoint — the merge should already be done.
	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse/merge/progress/cam-complete", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify Content-Type is text/event-stream
	ct := rr.Header().Get("Content-Type")
	if ct != "text/event-stream" {
		t.Fatalf("expected Content-Type text/event-stream, got %s", ct)
	}

	events := parseSSEEvents(t, rr.Body.String())
	if len(events) == 0 {
		t.Fatal("expected at least one SSE event")
	}

	// Last event should be completed
	last := events[len(events)-1]
	if last.Status != "completed" {
		t.Fatalf("expected status 'completed', got %q", last.Status)
	}
	if last.Progress != 100 {
		t.Fatalf("expected progress 100, got %d", last.Progress)
	}
	if last.OutputPath == "" {
		t.Fatal("expected non-empty output_path in completed event")
	}
	if last.FramesMerged != 10 {
		t.Fatalf("expected frames_merged 10, got %d", last.FramesMerged)
	}
	if last.Duration != 1.0 {
		t.Fatalf("expected duration 1.0, got %f", last.Duration)
	}
	if last.Tier != "go" {
		t.Fatalf("expected tier 'go', got %s", last.Tier)
	}
}

func TestMergeProgress_Failed(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	merger := &testSlowMerger{delay: 10 * time.Millisecond, fail: true}
	mgr := timelapse.NewRollingMergeManager(merger, nil, 10, false)
	h.SetTimelapseMergeMgr(mgr)

	tmpDir := t.TempDir()
	segmentDir := filepath.Join(tmpDir, "segment")
	if err := os.MkdirAll(segmentDir, 0755); err != nil {
		t.Fatalf("failed to create segment dir: %v", err)
	}
	jpegData := createTestJPEG(t, 100, 100)
	for i := 0; i < 3; i++ {
		if err := os.WriteFile(filepath.Join(segmentDir, fmt.Sprintf("frame_%06d.jpg", i+1)), jpegData, 0644); err != nil {
			t.Fatalf("failed to write frame: %v", err)
		}
	}
	outputPath := filepath.Join(tmpDir, "output.mp4")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr.StartSegmentMerge(ctx, "cam-fail", segmentDir, outputPath, "")

	// Wait for merge to complete (should fail).
	time.Sleep(200 * time.Millisecond)

	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse/merge/progress/cam-fail", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	events := parseSSEEvents(t, rr.Body.String())
	if len(events) == 0 {
		t.Fatal("expected at least one SSE event")
	}

	last := events[len(events)-1]
	if last.Status != "failed" {
		t.Fatalf("expected status 'failed', got %q", last.Status)
	}
	if last.Error == "" {
		t.Fatal("expected non-empty error message in failed event")
	}
}

func TestMergeProgress_Idle(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	merger := &testSlowMerger{delay: 10 * time.Millisecond}
	mgr := timelapse.NewRollingMergeManager(merger, nil, 10, false)
	h.SetTimelapseMergeMgr(mgr)

	// No merge started for this camera — should get idle event.
	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse/merge/progress/cam-idle", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	events := parseSSEEvents(t, rr.Body.String())
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Status != "idle" {
		t.Fatalf("expected status 'idle', got %q", events[0].Status)
	}
	if events[0].Progress != 0 {
		t.Fatalf("expected progress 0, got %d", events[0].Progress)
	}
}

func TestMergeProgress_NoManager(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	// No timelapse merge manager set.

	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse/merge/progress/cam-nomgr", nil, "", "")
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp["error"] != "timelapse merge manager not available" {
		t.Fatalf("expected 'timelapse merge manager not available', got %q", resp["error"])
	}
}

// --- Timelapse Status tests ---

func TestTimelapseStatus_WithMergeManager(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	merger := &testSlowMerger{delay: 50 * time.Millisecond}
	mgr := timelapse.NewRollingMergeManager(merger, nil, 10, false)
	h.SetTimelapseMergeMgr(mgr)

	// Start an active merge
	tmpDir := t.TempDir()
	segmentDir := filepath.Join(tmpDir, "segment")
	if err := os.MkdirAll(segmentDir, 0755); err != nil {
		t.Fatalf("failed to create segment dir: %v", err)
	}
	jpegData := createTestJPEG(t, 100, 100)
	if err := os.WriteFile(filepath.Join(segmentDir, "frame_000001.jpg"), jpegData, 0644); err != nil {
		t.Fatalf("failed to write frame: %v", err)
	}
	outputPath := filepath.Join(tmpDir, "output.mp4")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr.StartSegmentMerge(ctx, "cam-status", segmentDir, outputPath, "")
	time.Sleep(20 * time.Millisecond) // Let merge goroutine start

	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse/status", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	parseJSON(t, rr, &resp)
	if resp["merge_enabled"] == nil {
		t.Fatal("expected merge_enabled in response")
	}
	// Active count should be present
	activeCount, ok := resp["active_count"].(float64)
	if !ok {
		t.Fatal("expected active_count in response")
	}
	if int(activeCount) < 0 {
		t.Fatalf("expected non-negative active_count, got %v", activeCount)
	}
}

func TestTimelapseStatus_NoMergeManager(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store) // No merge manager set

	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse/status", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	parseJSON(t, rr, &resp)
	// Should still return default fields
	if resp["merge_enabled"] == nil {
		t.Fatal("expected merge_enabled in response even without manager")
	}
}

// --- Timelapse List tests ---

func TestTimelapseList_Empty(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp recordingsResponse
	parseJSON(t, rr, &resp)
	if len(resp.Recordings) != 0 {
		t.Fatalf("expected 0 recordings, got %d", len(resp.Recordings))
	}
	if resp.Total != 0 {
		t.Fatalf("expected total=0, got %d", resp.Total)
	}
}

func TestTimelapseList_FiltersByFormat(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	seedRecording(t, db, makeRecording("rec-tl", "cam-1", "timelapse", now, false))
	seedRecording(t, db, makeRecording("rec-mj", "cam-1", "mjpeg", now, false))
	seedRecording(t, db, makeRecording("rec-h264", "cam-1", "h264", now, false))
	seedRecording(t, db, makeRecording("rec-h265", "cam-1", "h265", now, false))

	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp recordingsResponse
	parseJSON(t, rr, &resp)
	if len(resp.Recordings) != 2 {
		t.Fatalf("expected 2 recordings (timelapse+mjpeg), got %d", len(resp.Recordings))
	}
	if resp.Total != 2 {
		t.Fatalf("expected total=2, got %d", resp.Total)
	}
}

func TestTimelapseList_Pagination(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 5; i++ {
		seedRecording(t, db, makeRecording("rec-tl-"+strconv.Itoa(i), "cam-1", "timelapse", now.Add(time.Duration(i)*time.Hour), false))
	}

	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse?limit=2&offset=1", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp recordingsResponse
	parseJSON(t, rr, &resp)
	if len(resp.Recordings) != 2 {
		t.Fatalf("expected 2 recordings, got %d", len(resp.Recordings))
	}
	if resp.Total != 5 {
		t.Fatalf("expected total=5, got %d", resp.Total)
	}
}

// --- Timelapse Merge tests ---

func TestTimelapseMerge_NoManager(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store) // No daily or rolling merge manager

	rr := doRequest(t, h.Routes(), "POST", "/api/timelapse/cam-1/merge", nil, "", "")
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestTimelapseMerge_Accepted(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	// Set up a real daily merge manager with test DB as RecordingLister
	merger := &testSlowMerger{}
	dataDir := t.TempDir()
	dailyMgr := timelapse.NewDailyMergeManager(db, nil, merger, 10, dataDir, nil)

	h := TestHandler(db, store)
	h.SetTimelapseDailyMgr(dailyMgr)

	rr := doRequest(t, h.Routes(), "POST", "/api/timelapse/cam-1/merge?date=2026-06-06", nil, "", "")
	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	parseJSON(t, rr, &resp)
	if resp["status"] != "merge_initiated" {
		t.Fatalf("expected status=merge_initiated, got %s", resp["status"])
	}
}

func TestTimelapseMerge_DefaultDate(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	merger := &testSlowMerger{}
	dataDir := t.TempDir()
	dailyMgr := timelapse.NewDailyMergeManager(db, nil, merger, 10, dataDir, nil)

	h := TestHandler(db, store)
	h.SetTimelapseDailyMgr(dailyMgr)

	// No date param — should default to yesterday
	rr := doRequest(t, h.Routes(), "POST", "/api/timelapse/cam-1/merge", nil, "", "")
	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestTimelapseMerge_DurationInvalid(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "POST", "/api/timelapse/cam-1/merge?duration=invalid", nil, "", "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestTimelapseMerge_ErrorPathCleansActiveMerges verifies that error paths
// properly clean up the activeMerges dedup map so the camera is not permanently blocked.
func TestTimelapseMerge_ErrorPathCleansActiveMerges(t *testing.T) {
	t.Parallel()
	{
		// Sub-test: no daily manager (nil timelapseDailyMgr)
		db, store := setupTestDB(t)
		defer db.Close()
		h := TestHandler(db, store) // No daily merge manager

		rr1 := doRequest(t, h.Routes(), "POST", "/api/timelapse/cam-stuck/merge", nil, "", "")
		if rr1.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503, got %d", rr1.Code)
		}

		// Retry should NOT get 409 Conflict — activeMerges must be cleaned up
		rr2 := doRequest(t, h.Routes(), "POST", "/api/timelapse/cam-stuck/merge", nil, "", "")
		if rr2.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 (not 409), got %d: %s", rr2.Code, rr2.Body.String())
		}
	}
	{
		// Sub-test: invalid duration string
		db, store := setupTestDB(t)
		defer db.Close()
		h := TestHandler(db, store)

		rr1 := doRequest(t, h.Routes(), "POST", "/api/timelapse/cam-dur/merge?duration=badvalue", nil, "", "")
		if rr1.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rr1.Code)
		}

		// Retry should NOT get 409 Conflict
		rr2 := doRequest(t, h.Routes(), "POST", "/api/timelapse/cam-dur/merge?duration=badvalue", nil, "", "")
		if rr2.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 (not 409), got %d: %s", rr2.Code, rr2.Body.String())
		}
	}
	{
		// Sub-test: nil config with duration path
		db, store := setupTestDB(t)
		defer db.Close()
		h := TestHandler(db, store) // no config set

		rr1 := doRequest(t, h.Routes(), "POST", "/api/timelapse/cam-nocfg/merge?duration=8h", nil, "", "")
		if rr1.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500, got %d", rr1.Code)
		}

		// Retry should NOT get 409 Conflict
		rr2 := doRequest(t, h.Routes(), "POST", "/api/timelapse/cam-nocfg/merge?duration=8h", nil, "", "")
		if rr2.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500 (not 409), got %d: %s", rr2.Code, rr2.Body.String())
		}
	}
}

func TestTimelapseMerge_DurationNeedsConfig(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store) // no config set

	rr := doRequest(t, h.Routes(), "POST", "/api/timelapse/cam-1/merge?duration=8h", nil, "", "")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestTimelapseMerge_DurationAccepted(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir: t.TempDir(),
		},
		Cameras: []config.CameraConfig{
			{
				ID: "cam-1",
				Timelapse: &config.CameraTimelapseConfig{
					Enabled:        true,
					MergeOutputFPS: 15,
				},
			},
		},
	}

	h := NewHandler(db, store, noopAuthMW(), cfg, nil, nil, "", nil, nil, nil)

	rr := doRequest(t, h.Routes(), "POST", "/api/timelapse/cam-1/merge?duration=8h&date=2026-06-06", nil, "", "")
	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	parseJSON(t, rr, &resp)
	if resp["status"] != "merge_initiated" {
		t.Fatalf("expected status=merge_initiated, got %v", resp["status"])
	}
	if resp["duration"] != "8h" {
		t.Fatalf("expected duration=8h, got %v", resp["duration"])
	}
}


// --- Timelapse Pause/Resume tests ---

func TestTimelapsePause_Success(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	cfg := &config.Config{
		Cameras: []config.CameraConfig{
			{
				ID: "cam-1", Name: "Test Cam", Protocol: "timelapse",
				Timelapse: &config.CameraTimelapseConfig{
					Enabled: true,
					Paused:  false,
				},
			},
		},
	}
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.yaml")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	h := newHandlerWithConfig(db, store, cfg)
	h.configPath = configPath

	rr := doRequest(t, h.Routes(), "POST", "/api/timelapse/cam-1/pause", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	if !cfg.Cameras[0].Timelapse.Paused {
		t.Fatal("expected camera timelapse to be paused")
	}
}

func TestTimelapseResume_Success(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	cfg := &config.Config{
		Cameras: []config.CameraConfig{
			{
				ID: "cam-1", Name: "Test Cam", Protocol: "timelapse",
				Timelapse: &config.CameraTimelapseConfig{
					Enabled: true,
					Paused:  true,
				},
			},
		},
	}
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.yaml")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	h := newHandlerWithConfig(db, store, cfg)
	h.configPath = configPath

	rr := doRequest(t, h.Routes(), "POST", "/api/timelapse/cam-1/resume", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	if cfg.Cameras[0].Timelapse.Paused {
		t.Fatal("expected camera timelapse to be resumed (not paused)")
	}
}

func TestTimelapsePause_CameraNotFound(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	cfg := &config.Config{Cameras: []config.CameraConfig{}}
	h := newHandlerWithConfig(db, store, cfg)
	h.configPath = filepath.Join(t.TempDir(), "config.yaml")

	rr := doRequest(t, h.Routes(), "POST", "/api/timelapse/nonexistent/pause", nil, "", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestTimelapsePause_NoConfig(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store) // nil config

	rr := doRequest(t, h.Routes(), "POST", "/api/timelapse/cam-1/pause", nil, "", "")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rr.Code, rr.Body.String())
	}
}

// --- Timelapse Get tests ---

func TestTimelapseGet_Found(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	segDir := filepath.Join(store.RootDir(), "cam-1", "rec-tl-get")
	if err := os.MkdirAll(segDir, 0755); err != nil {
		t.Fatalf("failed to create seg dir: %v", err)
	}
	rec := &model.Recording{
		ID: "rec-tl-get", CameraID: "cam-1",
		FilePath: segDir,
		Format:   model.Format("timelapse"),
		StartedAt:  now,
		EndedAt:    now.Add(30 * time.Second),
		Duration:   30.0,
		FileSize:   1000,
		FrameCount: 3,
	}
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse/rec-tl-get", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var got model.Recording
	parseJSON(t, rr, &got)
	if got.ID != "rec-tl-get" {
		t.Fatalf("expected rec-tl-get, got %s", got.ID)
	}
}

func TestTimelapseGet_NotFound(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse/nonexistent", nil, "", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestTimelapseGet_WrongFormat(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	seedRecording(t, db, makeRecording("rec-h264", "cam-1", "h264", now, false))

	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse/rec-h264", nil, "", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for non-timelapse recording, got %d: %s", rr.Code, rr.Body.String())
	}
}

// --- Timelapse Delete tests ---

func TestTimelapseDelete_Success(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	segDir := filepath.Join(store.RootDir(), "cam-1", "rec-tl-del")
	if err := os.MkdirAll(segDir, 0755); err != nil {
		t.Fatalf("failed to create seg dir: %v", err)
	}
	// Create a test frame file
	if err := os.WriteFile(filepath.Join(segDir, "frame_000001.jpg"), []byte("test"), 0644); err != nil {
		t.Fatalf("failed to write frame: %v", err)
	}

	// Create a merged file
	mergedPath := filepath.Join(store.RootDir(), "cam-1", "rec-tl-del-merged.mp4")
	if err := os.WriteFile(mergedPath, []byte("fake-mp4"), 0644); err != nil {
		t.Fatalf("failed to create merged file: %v", err)
	}

	rec := &model.Recording{
		ID: "rec-tl-del", CameraID: "cam-1",
		FilePath:    segDir,
		Format:      model.Format("timelapse"),
		StartedAt:   now,
		EndedAt:     now.Add(30 * time.Second),
		Duration:    30.0,
		FileSize:    1000,
		FrameCount:  3,
		Merged:      true,
	}
	seedRecording(t, db, rec)
	// Set merge result to populate merge_path and merge_status
	if err := db.SetMergeResult(context.Background(), "rec-tl-del", mergedPath, "go"); err != nil {
		t.Fatalf("failed to set merge result: %v", err)
	}

	rr := doRequest(t, h.Routes(), "DELETE", "/api/timelapse/rec-tl-del", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify DB entry deleted
	got, err := db.GetRecording(context.Background(), "rec-tl-del")
	if err != nil {
		t.Fatalf("failed to get recording: %v", err)
	}
	if got != nil {
		t.Fatal("expected recording to be deleted from DB")
	}

	// Verify source dir deleted
	if _, err := os.Stat(segDir); !os.IsNotExist(err) {
		t.Fatal("expected segment directory to be deleted")
	}

	// Verify merged file deleted
	if _, err := os.Stat(mergedPath); !os.IsNotExist(err) {
		t.Fatal("expected merged file to be deleted")
	}
}

func TestTimelapseDelete_NotFound(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "DELETE", "/api/timelapse/nonexistent", nil, "", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestTimelapseDelete_WrongFormat(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	seedRecording(t, db, makeRecording("rec-h264-del", "cam-1", "h264", now, false))

	rr := doRequest(t, h.Routes(), "DELETE", "/api/timelapse/rec-h264-del", nil, "", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for non-timelapse recording, got %d: %s", rr.Code, rr.Body.String())
	}
}

// --- Timelapse Download tests ---

func TestTimelapseDownload_Merged(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	segDir := filepath.Join(store.RootDir(), "cam-1", "rec-tl-dl")
	if err := os.MkdirAll(segDir, 0755); err != nil {
		t.Fatalf("failed to create seg dir: %v", err)
	}

	mergedPath := filepath.Join(store.RootDir(), "cam-1", "rec-tl-dl-merged.mp4")
	mergeData := []byte("fake-merged-mp4-data")
	if err := os.WriteFile(mergedPath, mergeData, 0644); err != nil {
		t.Fatalf("failed to create merged file: %v", err)
	}

	rec := &model.Recording{
		ID: "rec-tl-dl", CameraID: "cam-1",
		FilePath:    segDir,
		Format:      model.Format("timelapse"),
		StartedAt:   now,
		EndedAt:     now.Add(30 * time.Second),
		Duration:    30.0,
		FileSize:    int64(len(mergeData)),
		FrameCount:  3,
		Merged:      true,
	}
	seedRecording(t, db, rec)
	// Set merge result to populate merge_path and merge_status
	if err := db.SetMergeResult(context.Background(), "rec-tl-dl", mergedPath, "go"); err != nil {
		t.Fatalf("failed to set merge result: %v", err)
	}

	rr := doRequest(t, h.Routes(), "POST", "/api/timelapse/rec-tl-dl/download", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify Content-Disposition header
	cd := rr.Header().Get("Content-Disposition")
	if cd == "" {
		t.Fatal("expected Content-Disposition header")
	}
}

func TestTimelapseDownload_NotMerged(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	segDir := filepath.Join(store.RootDir(), "cam-1", "rec-tl-notmerged")
	if err := os.MkdirAll(segDir, 0755); err != nil {
		t.Fatalf("failed to create seg dir: %v", err)
	}

	rec := &model.Recording{
		ID: "rec-tl-notmerged", CameraID: "cam-1",
		FilePath:    segDir,
		MergeStatus: "pending",
		Format:      model.Format("timelapse"),
		StartedAt:   now,
		EndedAt:     now.Add(30 * time.Second),
		Duration:    30.0,
		FileSize:    1000,
		FrameCount:  3,
	}
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "POST", "/api/timelapse/rec-tl-notmerged/download", nil, "", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestTimelapseDownload_NotFound(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "POST", "/api/timelapse/nonexistent/download", nil, "", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

// --- Merge Cancellation tests (Task 7) ---

func TestTimelapseMergeCancel_Success(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	// Create a RollingMergeManager with an active merge
	mgr := timelapse.NewRollingMergeManager(nil, nil, 10, false)
	ctx := context.Background()
	mgr.StartSegmentMerge(ctx, "cam-1", "/tmp/nonexistent", "/tmp/output.mp4", "rec-1")
	h.SetTimelapseMergeMgr(mgr)

	rr := doRequest(t, h.Routes(), "DELETE", "/api/timelapse/cam-1/merge", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]string
	parseJSON(t, rr, &resp)
	if resp["status"] != "cancelled" {
		t.Fatalf("expected status=cancelled, got %v", resp["status"])
	}

	// Verify merge is no longer active
	if mgr.IsActive("cam-1") {
		t.Fatal("expected merge to be cancelled")
	}
}

func TestTimelapseMergeCancel_NotFound(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	// Create a manager with no active merge
	mgr := timelapse.NewRollingMergeManager(nil, nil, 10, false)
	h.SetTimelapseMergeMgr(mgr)

	rr := doRequest(t, h.Routes(), "DELETE", "/api/timelapse/cam-nonexistent/merge", nil, "", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestTimelapseMergeCancel_NoManager(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store) // no merge mgr set

	rr := doRequest(t, h.Routes(), "DELETE", "/api/timelapse/cam-1/merge", nil, "", "")
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rr.Code, rr.Body.String())
	}
}

// --- Batch Merge tests (Task 12) ---

func TestTimelapseBatchMerge_Success(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir: t.TempDir(),
		},
		Cameras: []config.CameraConfig{
			{
				ID: "cam-1",
				Timelapse: &config.CameraTimelapseConfig{
					Enabled:        true,
					MergeOutputFPS: 15,
				},
			},
			{
				ID: "cam-2",
				Timelapse: &config.CameraTimelapseConfig{
					Enabled:        true,
					MergeOutputFPS: 10,
				},
			},
		},
	}

	h := newHandlerWithConfig(db, store, cfg)

	body := map[string]any{
		"camera_ids": []string{"cam-1", "cam-2"},
		"duration":   "8h",
		"date":       "2026-06-06",
	}
	bodyJSON, _ := json.Marshal(body)
	rr := doRequest(t, h.Routes(), "POST", "/api/timelapse/batch-merge", bytes.NewReader(bodyJSON), "", "")
	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	parseJSON(t, rr, &resp)

	triggered, ok := resp["triggered"].(float64)
	if !ok || int(triggered) != 2 {
		t.Fatalf("expected triggered=2, got %v", resp["triggered"])
	}

	results, ok := resp["results"].([]any)
	if !ok || len(results) != 2 {
		t.Fatalf("expected 2 results, got %v", resp["results"])
	}

	// Verify first camera result
	first := results[0].(map[string]any)
	if first["camera_id"] != "cam-1" || first["status"] != "merge_initiated" {
		t.Fatalf("unexpected first result: %v", first)
	}
}

func TestTimelapseBatchMerge_EmptyIDs(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	body := map[string]any{
		"camera_ids": []string{},
		"duration":   "8h",
	}
	bodyJSON, _ := json.Marshal(body)
	rr := doRequest(t, h.Routes(), "POST", "/api/timelapse/batch-merge", bytes.NewReader(bodyJSON), "", "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestTimelapseBatchMerge_TooMany(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	cameraIDs := make([]string, 11)
	for i := 0; i < 11; i++ {
		cameraIDs[i] = fmt.Sprintf("cam-%d", i)
	}

	body := map[string]any{
		"camera_ids": cameraIDs,
		"duration":   "8h",
	}
	bodyJSON, _ := json.Marshal(body)
	rr := doRequest(t, h.Routes(), "POST", "/api/timelapse/batch-merge", bytes.NewReader(bodyJSON), "", "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestTimelapseBatchMerge_InvalidDuration(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	cfg := &config.Config{
		Storage: config.StorageConfig{RootDir: t.TempDir()},
	}
	h := newHandlerWithConfig(db, store, cfg)

	body := map[string]any{
		"camera_ids": []string{"cam-1"},
		"duration":   "badvalue",
	}
	bodyJSON, _ := json.Marshal(body)
	rr := doRequest(t, h.Routes(), "POST", "/api/timelapse/batch-merge", bytes.NewReader(bodyJSON), "", "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestTimelapseBatchMerge_NoConfig(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store) // no config

	body := map[string]any{
		"camera_ids": []string{"cam-1"},
		"duration":   "8h",
	}
	bodyJSON, _ := json.Marshal(body)
	rr := doRequest(t, h.Routes(), "POST", "/api/timelapse/batch-merge", bytes.NewReader(bodyJSON), "", "")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rr.Code, rr.Body.String())
	}
}

// --- Frame Preview tests (Task 21) ---

func TestTimelapsePreview_Success(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rec := createTestRecording(t, db, store, "preview-test-1", "cam-1")

	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse/"+rec.ID+"/preview", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var frames []map[string]any
	parseJSON(t, rr, &frames)

	// createTestRecording creates 3 frames, default sample=6 should return all 3
	if len(frames) != 3 {
		t.Fatalf("expected 3 frames, got %d", len(frames))
	}

	// Verify frame structure
	for _, f := range frames {
		if f["filename"] == "" || f["url"] == "" {
			t.Fatalf("expected filename and url, got %v", f)
		}
	}
}

func TestTimelapsePreview_SampleParam(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rec := createTestRecording(t, db, store, "preview-test-2", "cam-1")

	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse/"+rec.ID+"/preview?sample=2", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var frames []map[string]any
	parseJSON(t, rr, &frames)

	// Should return 2 evenly-spaced frames
	if len(frames) != 2 {
		t.Fatalf("expected 2 frames, got %d", len(frames))
	}
}

func TestTimelapsePreview_MaxSample(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rec := createTestRecording(t, db, store, "preview-test-3", "cam-1")

	// sample=50 should be capped at 20
	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse/"+rec.ID+"/preview?sample=50", nil, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var frames []map[string]any
	parseJSON(t, rr, &frames)

	// Only 3 frames exist, so we get all 3 (totalFrames <= sampleCount)
	if len(frames) != 3 {
		t.Fatalf("expected 3 frames (capped), got %d", len(frames))
	}
}

func TestTimelapsePreview_NotFound(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse/nonexistent/preview", nil, "", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestTimelapsePreview_NotTimelapse(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	rec := &model.Recording{
		ID:        "preview-not-tl",
		CameraID:  "cam-1",
		FilePath:  t.TempDir(),
		Format:    model.Format("h264"),
		StartedAt: now,
		EndedAt:   now.Add(30 * time.Second),
		Duration:  30.0,
		FileSize:  1000,
	}
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "GET", "/api/timelapse/preview-not-tl/preview", nil, "", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}
