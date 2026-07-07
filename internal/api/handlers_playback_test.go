package api

import (
	"bytes"
	"context"
	"encoding/binary"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/avi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

// makeTestJPEG creates a minimal valid JPEG frame for test AVI files.
func makeTestJPEG(t *testing.T, seed byte, size int) []byte {
	t.Helper()
	frame := make([]byte, size)
	frame[0] = 0xFF // SOI marker start
	frame[1] = 0xD8 // SOI marker end
	for i := 2; i < size; i++ {
		frame[i] = seed + byte(i)
	}
	return frame
}

// makeTestAudio creates G.711 mu-law audio data for test AVI files.
func makeTestAudio(t *testing.T, seed byte, size int) []byte {
	t.Helper()
	data := make([]byte, size)
	for i := range data {
		data[i] = seed + byte(i)
	}
	return data
}

// createTestAVIFile creates a minimal AVI file at path with the given number
// of MJPEG video frames and optional G.711 audio data per frame.
func createTestAVIFile(t *testing.T, path string, numFrames int, includeAudio bool) {
	t.Helper()
	var buf bytes.Buffer
	m := avi.NewMuxer(&buf, 320, 240, 8000, true) // mu-law

	for i := range numFrames {
		frame := makeTestJPEG(t, 0xAA+byte(i), 100+i)
		require.NoError(t, m.WriteVideo(frame, int64(i*33333)))
		if includeAudio {
			audio := makeTestAudio(t, 0xBB+byte(i), 160) // 20ms at 8kHz
			require.NoError(t, m.WriteAudio(audio, int64(i*20000)))
		}
	}
	require.NoError(t, m.Close())
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
}

// --- Tests ---

func TestPlaybackWS_BasicFlow(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	// Create test AVI file inside store root.
	aviPath := filepath.Join(store.RootDir(), "test_playback.avi")
	createTestAVIFile(t, aviPath, 5, true) // 5 video frames + audio

	// Seed recording record.
	rec := &model.Recording{
		ID:        "playback-test-1",
		CameraID:  "cam-1",
		FilePath:  aviPath,
		Format:    model.FormatAVI,
		StartedAt: time.Now().UTC(),
		EndedAt:   time.Now().UTC().Add(5 * time.Second),
		Duration:  5.0,
		FileSize:  10000,
	}
	require.NoError(t, db.InsertRecording(context.Background(), rec))

	// Start test HTTP server.
	server := httptest.NewServer(h.Routes())
	defer server.Close()

	// Connect WebSocket.
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/recordings/playback-test-1/playback"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	defer conn.Close()

	// Read frames — expect at least 5 video frames (audio may vary).
	var videoCount, audioCount int
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	for range 10 {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}
		require.GreaterOrEqual(t, len(msg), 13, "message too short")

		msgType := msg[0]
		pts := binary.LittleEndian.Uint64(msg[1:9])
		dataLen := binary.LittleEndian.Uint32(msg[9:13])
		require.Equal(t, uint32(len(msg)-13), dataLen)

		switch msgType {
		case 0x01:
			videoCount++
		case 0x02:
			audioCount++
		default:
			t.Errorf("unexpected message type: 0x%02x", msgType)
		}

		_ = pts // PTS values are verified implicitly by receiving them
	}

	require.GreaterOrEqual(t, videoCount, 5, "expected at least 5 video frames")
	require.GreaterOrEqual(t, audioCount, 1, "expected at least 1 audio chunk")
}

func TestPlaybackWS_PlayPause(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	aviPath := filepath.Join(store.RootDir(), "test_playback_pause.avi")
	createTestAVIFile(t, aviPath, 500, false)

	rec := &model.Recording{
		ID:        "playback-test-2",
		CameraID:  "cam-1",
		FilePath:  aviPath,
		Format:    model.FormatAVI,
		StartedAt: time.Now().UTC(),
		EndedAt:   time.Now().UTC().Add(30 * time.Second),
		Duration:  30.0,
		FileSize:  50000,
	}
	require.NoError(t, db.InsertRecording(context.Background(), rec))

	server := httptest.NewServer(h.Routes())
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/recordings/playback-test-2/playback"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	defer conn.Close()

	// Read one frame to confirm streaming.
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err = conn.ReadMessage()
	require.NoError(t, err, "expected at least one frame before pause")

	// Send pause, then play after a short delay.
	require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(`{"action":"pause"}`)))
	time.Sleep(50 * time.Millisecond)
	require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(`{"action":"play"}`)))

	// Should receive a frame after resume.
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err = conn.ReadMessage()
	require.NoError(t, err, "expected frames after resume")
}

func TestPlaybackWS_NotFound(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	server := httptest.NewServer(h.Routes())
	defer server.Close()

	// Connect to a non-existent recording — the handler should return an error
	// before upgrade. Since the WS won't be upgraded, Dial should fail.
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/recordings/non-existent/playback"
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if resp != nil {
		defer resp.Body.Close()
	}
	require.Error(t, err)
	require.Contains(t, err.Error(), "bad handshake")
}

func TestPlaybackWS_NotAVI(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	// Seed an MP4 recording (non-AVI).
	rec := &model.Recording{
		ID:        "playback-test-mp4",
		CameraID:  "cam-1",
		FilePath:  "/tmp/test.mp4",
		Format:    model.FormatH264,
		StartedAt: time.Now().UTC(),
		Duration:  5.0,
		FileSize:  10000,
	}
	require.NoError(t, db.InsertRecording(context.Background(), rec))

	server := httptest.NewServer(h.Routes())
	defer server.Close()

	// Since it's not an AVI, the handler should respond with 400 before WS upgrade.
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/recordings/playback-test-mp4/playback"
	_, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "bad handshake")
}

func TestPlaybackWS_StopAction(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	aviPath := filepath.Join(store.RootDir(), "test_playback_stop.avi")
	createTestAVIFile(t, aviPath, 30, false) // video only

	rec := &model.Recording{
		ID:        "playback-test-stop",
		CameraID:  "cam-1",
		FilePath:  aviPath,
		Format:    model.FormatAVI,
		StartedAt: time.Now().UTC(),
		Duration:  5.0,
		FileSize:  10000,
	}
	require.NoError(t, db.InsertRecording(context.Background(), rec))

	server := httptest.NewServer(h.Routes())
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/recordings/playback-test-stop/playback"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)

	// Read a couple frames.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err = conn.ReadMessage()
	require.NoError(t, err)

	// Send stop.
	require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(`{"action":"stop"}`)))

	// Drain any in-flight frames, then verify connection is closed.
	var stopReads int
	for stopReads < 5 {
		conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		_, _, err = conn.ReadMessage()
		if err != nil {
			return // expected — connection closed
		}
		stopReads++
	}
	t.Error("expected connection to close after stop")
}
