package srt

import (
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/datarhei/gosrt"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model/nalutil"
)

var logger = slog.Default().With("component", "srt-receiver")

// Receiver manages an SRT connection and distributes H.264 frames to a StreamHub.
// It handles both listener mode (receiving pushes) and caller mode (pulling from remote).
type Receiver struct {
	cameraID   string
	mode       string // "listener" or "caller"
	address    string
	passphrase string
	streamID   string
	hub        *model.StreamHub
	conn       srt.Conn
	done       chan struct{}
	running    atomic.Bool

	// Metrics
	frameCount atomic.Int64
	dropCount  atomic.Int64
}

// NewReceiver creates a new SRT receiver for the given stream configuration.
func NewReceiver(stream config.SRTStream, hub *model.StreamHub) *Receiver {
	return &Receiver{
		cameraID:   stream.CameraID,
		mode:       stream.Mode,
		address:    stream.Address,
		passphrase: stream.Passphrase,
		streamID:   stream.StreamID,
		hub:        hub,
		done:       make(chan struct{}),
	}
}

// Start begins receiving frames from the SRT connection.
// For listener mode, conn must be provided (from the listener accept loop).
// For caller mode, the receiver dials the remote address.
func (r *Receiver) Start(conn srt.Conn) error {
	if r.running.Load() {
		return fmt.Errorf("srt receiver for camera %q already running", r.cameraID)
	}

	if r.mode == "caller" {
		cfg := srt.DefaultConfig()
		cfg.StreamId = r.streamID
		cfg.Passphrase = r.passphrase
		if err := cfg.Validate(); err != nil {
			return fmt.Errorf("srt caller config invalid: %w", err)
		}

		var err error
		conn, err = srt.Dial("srt", r.address, cfg)
		if err != nil {
			return fmt.Errorf("srt dial %s: %w", r.address, err)
		}
	}

	r.conn = conn
	r.running.Store(true)
	close(r.done) // signal initial setup complete

	go r.readLoop(conn)

	// Reset done channel for stop signaling
	r.done = make(chan struct{})

	return nil
}

// StartListener handles a connection from the SRT listener.
func (r *Receiver) StartListener(conn srt.Conn) {
	r.conn = conn
	r.running.Store(true)
	r.done = make(chan struct{})
	go r.readLoop(conn)
}

// Stop terminates the SRT connection and stops frame reception.
func (r *Receiver) Stop() error {
	if !r.running.Swap(false) {
		return nil
	}

	if r.conn != nil {
		r.conn.Close()
	}

	if r.done != nil {
		close(r.done)
	}

	logger.Info("stopped SRT receiver", "camera_id", r.cameraID,
		"frames_sent", r.frameCount.Load(), "frames_dropped", r.dropCount.Load())
	return nil
}

// Running returns whether the receiver is active.
func (r *Receiver) Running() bool {
	return r.running.Load()
}

// FrameCount returns total frames sent to the hub.
func (r *Receiver) FrameCount() int64 {
	return r.frameCount.Load()
}

// getDropCount returns total frames dropped by the hub.
func (r *Receiver) getDropCount() int64 {
	return r.dropCount.Load()
}


// readLoop reads MPEG-TS data from the SRT connection, demuxes it,
// extracts H.264 NALUs, and broadcasts access units to the StreamHub.
func (r *Receiver) readLoop(conn srt.Conn) {
	defer func() {
		r.running.Store(false)
		logger.Info("SRT read loop exited", "camera_id", r.cameraID)
	}()

	demuxer := NewTSDemuxer()
	buf := make([]byte, 1316) // Typical SRT payload size (7 * 188 = 1316)

	for r.running.Load() {
		n, err := conn.Read(buf)
		if err != nil {
			if !r.running.Load() {
				return // Clean shutdown
			}
			if err == io.EOF || err == srt.ErrClientClosed {
				logger.Info("SRT connection closed", "camera_id", r.cameraID)
				return
			}
			logger.Warn("SRT read error", "camera_id", r.cameraID, "error", err)
			return
		}

		if n == 0 {
			continue
		}

		// Feed data to MPEG-TS demuxer
		nalus := demuxer.Feed(buf[:n])

		// Assemble NALUs into access units and broadcast
		if len(nalus) > 0 {
			frames := assembleAccessUnit(nalus)
			for _, au := range frames {
				if len(au) == 0 {
					continue
				}

				// Determine PTS from the first NALU in the access unit
				pts := nalus[0].PTS

				r.hub.Broadcast(pts, au, nalutil.IsIDR(au, false))
				r.frameCount.Add(1)
			}
		}
	}

	// Flush any remaining data
	nalus := demuxer.Flush()
	if len(nalus) > 0 {
		frames := assembleAccessUnit(nalus)
		for _, au := range frames {
			if len(au) == 0 {
				continue
			}
			pts := nalus[0].PTS
			r.hub.Broadcast(pts, au, nalutil.IsIDR(au, false))
			r.frameCount.Add(1)
		}
	}
}

// ParseStreamID parses the SRT streamid to extract the camera ID.
// The streamid format can be:
//   - Plain camera ID: "front-door"
//   - Query string: "camera_id=front-door"
//   - Path with query: "/live/front-door"
//   - Publish prefix: "publish:/live/front-door"
func ParseStreamID(streamID string) string {
	if streamID == "" {
		return ""
	}

	// Strip "publish:" prefix
	streamID = strings.TrimPrefix(streamID, "publish:")

	// Try parsing as URL query string
	if strings.Contains(streamID, "=") {
		if vals, err := url.ParseQuery(streamID); err == nil {
			if cid := vals.Get("camera_id"); cid != "" {
				return cid
			}
		}
	}

	// Try parsing as URL path
	if strings.HasPrefix(streamID, "/") {
		parts := strings.Split(strings.Trim(streamID, "/"), "/")
		// Use last path segment as camera ID
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}

	// Return as-is (plain camera ID)
	return streamID
}
