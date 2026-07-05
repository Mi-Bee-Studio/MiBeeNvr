package api

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
	"errors"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/avi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

// playbackUpgrader is the WebSocket upgrader for AVI recording playback.
var playbackUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// playbackState tracks the control state for a single WS playback connection.
type playbackState struct {
	mu     sync.Mutex
	paused bool
}

// handlePlayback handles AVI recording playback via WebSocket.
//
//	GET /api/recordings/{id}/playback
//
// Binary frame format (little-endian):
//
//	[type:1byte][pts:8bytes][len:4bytes][data...]
//	  type 0x01 = MJPEG video
//	  type 0x02 = G.711 audio
//
// Text control frames:
//
//	{"action":"pause"}  — stop streaming (stay at current position)
//	{"action":"play"}   — resume streaming
//	{"action":"stop"}   — close connection
func (h *Handler) handlePlayback(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "recording id is required")
		return
	}

	// Look up recording in DB.
	rec, err := h.db.GetRecording(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		writeError(w, http.StatusNotFound, "recording not found")
		return
	}

	// Must be AVI format.
	if rec.Format != model.FormatAVI {
		writeError(w, http.StatusBadRequest, "recording is not an AVI file")
		return
	}

	// Validate and resolve file path.
	validPath, err := storage.ValidatePath(h.store.RootDir(), rec.FilePath)
	if err != nil {
		writeError(w, http.StatusNotFound, "recording file not found")
		return
	}
	if _, err := os.Stat(validPath); err != nil {
		writeError(w, http.StatusNotFound, "recording file not found on disk")
		return
	}

	// Upgrade to WebSocket.
	conn, err := playbackUpgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Warn("playback WS upgrade failed", "error", err)
		return
	}

	// Run playback loop in background; handler returns once upgraded.
	go playbackLoop(conn, validPath)
}

// playbackLoop streams AVI chunks over WebSocket at real-time speed.
func playbackLoop(conn *websocket.Conn, aviPath string) {
	defer conn.Close()

	f, err := os.Open(aviPath)
	if err != nil {
		logger.Warn("playback: failed to open AVI file", "path", aviPath, "error", err)
		return
	}
	defer f.Close()

	demuxer, err := avi.NewDemuxer(f)
	if err != nil {
		logger.Warn("playback: failed to create demuxer", "error", err)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	state := &playbackState{}

	// Start control message reader goroutine.
	go readPlaybackControls(ctx, conn, state, cancel)

	var lastPTS int64

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Check pause state.
		state.mu.Lock()
		if state.paused {
			state.mu.Unlock()
			time.Sleep(100 * time.Millisecond)
			continue
		}
		state.mu.Unlock()

		chunk, err := demuxer.NextChunk()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			logger.Warn("playback: demuxer error", "error", err)
			break
		}

		// Encode binary frame: [type:1][pts:8 LE][len:4 LE][data...]
		var header [13]byte
		if chunk.Type == avi.ChunkVideo {
			header[0] = 0x01 // MJPEG video
		} else {
			header[0] = 0x02 // G.711 audio
		}
		binary.LittleEndian.PutUint64(header[1:9], uint64(chunk.PTS))
		binary.LittleEndian.PutUint32(header[9:13], uint32(len(chunk.Data)))

		msg := append(header[:], chunk.Data...)

		if err := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
			logger.Warn("playback: set write deadline", "error", err)
			break
		}
		if err := conn.WriteMessage(websocket.BinaryMessage, msg); err != nil {
			logger.Warn("playback: write error", "error", err)
			break
		}

		// Playback speed: sleep based on PTS delta.
		if chunk.PTS > lastPTS {
			delta := chunk.PTS - lastPTS
			if delta > 0 {
				if delta > 1000000 { // cap sleep at 1 second
					delta = 1000000
				}
				time.Sleep(time.Duration(delta) * time.Microsecond)
			}
		}
		lastPTS = chunk.PTS
	}
}

// readPlaybackControls reads WebSocket text control messages.
func readPlaybackControls(ctx context.Context, conn *websocket.Conn, state *playbackState, cancel context.CancelFunc) {
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}

		var cmd struct {
			Action string `json:"action"`
		}
		if err := json.Unmarshal(msg, &cmd); err != nil {
			continue
		}

		switch cmd.Action {
		case "pause":
			state.mu.Lock()
			state.paused = true
			state.mu.Unlock()
		case "play":
			state.mu.Lock()
			state.paused = false
			state.mu.Unlock()
		case "stop":
			cancel()
			return
		}
	}
}
