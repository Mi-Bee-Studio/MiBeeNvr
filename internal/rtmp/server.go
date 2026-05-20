package rtmp

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/bluenviron/gortmplib"
	"github.com/bluenviron/gortmplib/pkg/codecs"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

var logger = slog.Default().With("component", "rtmp-server")

// StreamKeyResolver maps a stream key to a camera ID.
// Returns empty string if the stream key is not recognized.
type StreamKeyResolver func(streamKey string) (cameraID string, ok bool)

// CameraHubProvider returns the StreamHub for a given camera.
// Returns nil if no hub is available for the camera.
type CameraHubProvider func(cameraID string) *model.StreamHub

// OnPublisherConnect is called when a publisher connects for a camera.
// The implementation should set up the StreamHub and register the virtual camera.
type OnPublisherConnect func(cameraID string, hub *model.StreamHub)

// OnPublisherDisconnect is called when a publisher disconnects for a camera.
// The implementation should clean up the virtual camera and hub.
type OnPublisherDisconnect func(cameraID string)

// Config holds RTMP server configuration.
type Config struct {
	// Addr is the listen address (default ":1935").
	Addr string
}

// Server is an RTMP ingest server that receives H.264 streams and
// distributes frames via StreamHub.
type Server struct {
	cfg    Config
	resolv StreamKeyResolver
	hubFn  CameraHubProvider
	onConn OnPublisherConnect
	onDisc OnPublisherDisconnect

	mu        sync.Mutex
	listener  net.Listener
	publishers map[string]*publisherEntry // streamKey → entry
	cancel    context.CancelFunc
	done      chan struct{}
}

type publisherEntry struct {
	cameraID string
	hub      *model.StreamHub
	cancel   context.CancelFunc
}

// NewServer creates a new RTMP ingest server.
func NewServer(cfg Config, resolv StreamKeyResolver, hubFn CameraHubProvider, onConn OnPublisherConnect, onDisc OnPublisherDisconnect) *Server {
	if cfg.Addr == "" {
		cfg.Addr = ":1935"
	}
	return &Server{
		cfg:        cfg,
		resolv:     resolv,
		hubFn:      hubFn,
		onConn:     onConn,
		onDisc:     onDisc,
		publishers: make(map[string]*publisherEntry),
	}
}

// Start starts the RTMP server, listening for incoming connections.
func (s *Server) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("rtmp listen %s: %w", s.cfg.Addr, err)
	}
	s.listener = ln

	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.done = make(chan struct{})

	go s.acceptLoop(ctx, ln)
	logger.Info("RTMP server listening", "addr", s.cfg.Addr)
	return nil
}

// Stop gracefully stops the RTMP server, waiting for all publishers to disconnect.
func (s *Server) Stop() error {
	if s.cancel != nil {
		s.cancel()
	}
	if s.listener != nil {
		s.listener.Close()
	}
	if s.done != nil {
		<-s.done
	}
	return nil
}

// Addr returns the listener address. Valid after Start().
func (s *Server) Addr() net.Addr {
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

// ActivePublishers returns the number of active publishers.
func (s *Server) ActivePublishers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.publishers)
}

func (s *Server) acceptLoop(ctx context.Context, ln net.Listener) {
	defer close(s.done)

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				logger.Error("accept error", "error", err)
				continue
			}
		}

		go s.handleConn(ctx, conn)
	}
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	sc := &gortmplib.ServerConn{
		RW: conn,
	}
	if err := sc.Initialize(); err != nil {
		logger.Warn("RTMP handshake failed", "remote", conn.RemoteAddr(), "error", err)
		return
	}

	if err := sc.Accept(); err != nil {
		logger.Warn("RTMP accept failed", "remote", conn.RemoteAddr(), "error", err)
		return
	}

	if !sc.Publish {
		logger.Warn("RTMP read connections not supported", "remote", conn.RemoteAddr())
		return
	}

	// Extract stream key from URL path: rtmp://host/live/{stream-key}
	streamKey := extractStreamKey(sc.URL)
	if streamKey == "" {
		logger.Warn("empty stream key", "remote", conn.RemoteAddr(), "url", sc.URL)
		return
	}

	cameraID, ok := s.resolv(streamKey)
	if !ok {
		logger.Warn("unknown stream key", "stream_key", streamKey, "remote", conn.RemoteAddr())
		return
	}

	// Register publisher
	s.mu.Lock()
	if _, exists := s.publishers[streamKey]; exists {
		s.mu.Unlock()
		logger.Warn("stream key already publishing", "stream_key", streamKey)
		return
	}

	hub := s.hubFn(cameraID)
	if hub == nil {
		hub = model.NewStreamHub()
	}

	pCtx, pCancel := context.WithCancel(ctx)
	entry := &publisherEntry{
		cameraID: cameraID,
		hub:      hub,
		cancel:   pCancel,
	}
	s.publishers[streamKey] = entry
	s.mu.Unlock()

	// Notify lifecycle hooks
	if s.onConn != nil {
		s.onConn(cameraID, hub)
	}

	// Cleanup on disconnect
	defer func() {
		s.mu.Lock()
		delete(s.publishers, streamKey)
		s.mu.Unlock()

		if s.onDisc != nil {
			s.onDisc(cameraID)
		}
		logger.Info("RTMP publisher disconnected", "camera_id", cameraID, "stream_key", streamKey)
	}()

	logger.Info("RTMP publisher connected", "camera_id", cameraID, "stream_key", streamKey, "remote", conn.RemoteAddr())

	s.handlePublisher(pCtx, sc, entry, conn)
}

func (s *Server) handlePublisher(ctx context.Context, sc *gortmplib.ServerConn, entry *publisherEntry, conn net.Conn) {
	// Set read deadline for the initial track analysis phase
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))

	r := &gortmplib.Reader{
		Conn: sc,
	}
	if err := r.Initialize(); err != nil {
		logger.Error("RTMP reader init failed", "camera_id", entry.cameraID, "error", err)
		return
	}

	// Find H.264 track
	var h264Track *gortmplib.Track
	for _, track := range r.Tracks() {
		if _, ok := track.Codec.(*codecs.H264); ok {
			h264Track = track
			break
		}
	}
	if h264Track == nil {
		logger.Error("no H.264 track found", "camera_id", entry.cameraID)
		return
	}

	// Clear deadline for continuous reading
	conn.SetReadDeadline(time.Time{})

	// Set up H.264 data callback — non-blocking broadcast to StreamHub
	r.OnDataH264(h264Track, func(pts time.Duration, dts time.Duration, au [][]byte) {
		// pts is time.Duration from stream start, convert to 90kHz clock ticks
		// for compatibility with StreamHub's existing consumers (HLS, WebRTC, FLV).
		ptsTicks := pts.Nanoseconds() * 90 / 1e6 // ns → 90kHz ticks
		entry.hub.Broadcast(ptsTicks, au)
	})

	// Read loop — runs until disconnect or context cancellation
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		if err := r.Read(); err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				logger.Warn("RTMP read error", "camera_id", entry.cameraID, "error", err)
				return
			}
		}
	}
}

// extractStreamKey gets the stream key from an RTMP URL.
// URL format: rtmp://host:1935/live/{stream-key}
func extractStreamKey(u *url.URL) string {
	if u == nil {
		return ""
	}
	path := u.Path
	// Path is like "/live/mystreamkey" — take the last segment after "/"
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}
