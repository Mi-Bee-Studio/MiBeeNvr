package srt

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"

	srt "github.com/datarhei/gosrt"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// Listener manages an SRT listener that accepts incoming connections,
// maps them to cameras via streamid, and distributes frames via StreamHub.
type Listener struct {
	cfg       config.SRTConfig
	hubs      map[string]*model.StreamHub // camera_id → StreamHub
	receivers map[string]*Receiver        // camera_id → active receiver
	mu        sync.RWMutex
	server    *srt.Server
	running   bool

	// OnConnect is called when a new connection is established.
	// If nil, the listener auto-creates a StreamHub for unknown cameras.
	OnConnect func(cameraID string, hub *model.StreamHub)

	// HubProvider returns the shared StreamHub for a camera ID. When set, the
	// listener uses the hub from the central registry (CameraManager) instead of
	// a locally-created orphan hub, so pushed frames reach the live consumers
	// (HLS/WebRTC/FLV/WS) and the IngestRecorder. If nil, the listener falls
	// back to its internal hubs map (legacy behavior).
	HubProvider func(cameraID string) *model.StreamHub

	// OnDisconnect is called when a publisher disconnects. Used to notify the
	// IngestRecorder to close its in-flight segment and return to Idle.
	OnDisconnect func(cameraID string)

	// NALUProvider returns a callback that forwards NALUs to the camera's
	// IngestRecorder for MP4 recording. Returns nil if the camera has no
	// IngestRecorder (e.g. a non-push camera). main.go sets this from the
	// camera manager.
	NALUProvider func(cameraID string) func(au [][]byte, ptsTicks int64, isIDR bool)
}

// NewListener creates a new SRT listener with the given configuration.
func NewListener(cfg config.SRTConfig) *Listener {
	return &Listener{
		cfg:       cfg,
		hubs:      make(map[string]*model.StreamHub),
		receivers: make(map[string]*Receiver),
	}
}

// registerHub registers a StreamHub for a camera ID.
// This is used to connect SRT streams to existing camera pipelines.
func (l *Listener) registerHub(cameraID string, hub *model.StreamHub) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.hubs[cameraID] = hub
}

// registerHubAndStopReceiver removes a StreamHub for a camera ID.
func (l *Listener) registerHubAndStopReceiver(cameraID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hubs, cameraID)
	if rec, ok := l.receivers[cameraID]; ok {
		rec.Stop()
		delete(l.receivers, cameraID)
	}
}

// addr returns the listener address, or nil if not listening.
func (l *Listener) addr() net.Addr {
	return &net.UDPAddr{Port: l.cfg.Port}
}

// getHubLocked resolves the StreamHub for a camera ID. It prefers the external
// HubProvider (the CameraManager's central registry) so pushed frames reach the
// recorder + live consumers; otherwise it falls back to the local hubs map.
// Returns nil if neither has a hub. Caller must hold l.mu.
func (l *Listener) getHubLocked(cameraID string) *model.StreamHub {
	if l.HubProvider != nil {
		if hub := l.HubProvider(cameraID); hub != nil {
			return hub
		}
	}
	return l.hubs[cameraID]
}

// Start begins listening for SRT connections.
func (l *Listener) Start() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.running {
		return fmt.Errorf("srt listener already running")
	}

	addr := fmt.Sprintf(":%d", l.cfg.Port)

	srtConfig := srt.DefaultConfig()
	srtConfig.Passphrase = "" // Listener doesn't set global passphrase; per-stream passphrases handled in accept

	l.server = &srt.Server{
		Addr:   addr,
		Config: &srtConfig,
		HandleConnect: func(req srt.ConnRequest) srt.ConnType {
			return l.handleConnect(req)
		},
		HandlePublish: func(conn srt.Conn) {
			l.handlePublish(conn)
		},
	}

	go func() {
		if err := l.server.ListenAndServe(); err != nil && !errors.Is(err, srt.ErrServerClosed) {
			logger.Error("SRT server error", "error", err)
		}
	}()

	l.running = true
	logger.Info("SRT listener started", "port", l.cfg.Port)
	return nil
}

// Stop shuts down the SRT listener and all active receivers.
func (l *Listener) Stop() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if !l.running {
		return nil
	}

	// Stop all receivers
	for id, rec := range l.receivers {
		rec.Stop()
		delete(l.receivers, id)
	}

	// Shutdown server
	if l.server != nil {
		l.server.Shutdown()
	}

	l.running = false
	logger.Info("SRT listener stopped")
	return nil
}

// receiverCount returns the number of active receivers.
func (l *Listener) receiverCount() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.receivers)
}

// StartCallers starts all configured caller-mode streams.
// Each caller receiver dials the remote SRT address and starts receiving.
func (l *Listener) StartCallers() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	for _, stream := range l.cfg.Streams {
		if stream.Mode != "caller" {
			continue
		}

		hub := l.getHubLocked(stream.CameraID)
		if hub == nil {
			logger.Warn("SRT caller: no hub registered for camera, creating new one",
				"camera_id", stream.CameraID)
			hub = model.NewStreamHub()
			l.hubs[stream.CameraID] = hub
		}

		rec := NewReceiver(stream, hub)
		if err := rec.Start(nil); err != nil {
			logger.Error("SRT caller failed to start", "camera_id", stream.CameraID, "error", err)
			continue
		}
		l.receivers[stream.CameraID] = rec
		logger.Info("started SRT caller", "camera_id", stream.CameraID, "address", stream.Address)
	}

	return nil
}

// handleConnect is called for each incoming SRT connection.
// It parses the streamid, finds the camera, and returns PUBLISH or REJECT.
func (l *Listener) handleConnect(req srt.ConnRequest) srt.ConnType {
	streamID := req.StreamId()
	cameraID := ParseStreamID(streamID)

	if cameraID == "" {
		logger.Warn("SRT connection rejected: empty camera ID from streamid", "streamid", streamID)
		return srt.REJECT
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Check if there's already a receiver for this camera
	if rec, ok := l.receivers[cameraID]; ok && rec.Running() {
		logger.Warn("SRT connection rejected: camera already has active receiver",
			"camera_id", cameraID)
		return srt.REJECT
	}

	// Find or create hub
	hub := l.getHubLocked(cameraID)
	if hub == nil {
		hub = model.NewStreamHub()
		l.hubs[cameraID] = hub
	}

	// Handle passphrase for encrypted connections
	if req.IsEncrypted() {
		// Find passphrase from config streams
		var passphrase string
		for _, s := range l.cfg.Streams {
			if s.CameraID == cameraID {
				passphrase = s.Passphrase
				break
			}
		}
		if passphrase == "" {
			logger.Warn("SRT connection rejected: encrypted but no passphrase configured",
				"camera_id", cameraID)
			return srt.REJECT
		}
		if err := req.SetPassphrase(passphrase); err != nil {
			logger.Warn("SRT connection rejected: passphrase mismatch",
				"camera_id", cameraID, "error", err)
			return srt.REJECT
		}
	}

	logger.Info("SRT connection accepted", "camera_id", cameraID, "streamid", streamID,
		"remote", req.RemoteAddr().String())

	return srt.PUBLISH
}

// handlePublish is called when a publishing connection is established.
func (l *Listener) handlePublish(conn srt.Conn) {
	streamID := conn.StreamId()
	cameraID := ParseStreamID(streamID)

	l.mu.Lock()
	hub := l.getHubLocked(cameraID)
	if hub == nil {
		hub = model.NewStreamHub()
		l.hubs[cameraID] = hub
	}

	// Find config for this stream to determine mode
	streamCfg := config.SRTStream{
		CameraID: cameraID,
		Mode:     "listener",
	}
	for _, s := range l.cfg.Streams {
		if s.CameraID == cameraID {
			streamCfg = s
			break
		}
	}

	rec := NewReceiver(streamCfg, hub)
	// Forward NALUs to the IngestRecorder for recording, if a NALU provider is
	// wired (main.go sets NALUProvider from the camera manager).
	if l.NALUProvider != nil {
		rec.NALUCallback = l.NALUProvider(cameraID)
	}
	l.receivers[cameraID] = rec
	l.mu.Unlock()

	// Defer cleanup: if anything panics, stop receiver, remove from map, and log
	defer func() {
		if r := recover(); r != nil {
			slog.Error("srt receiver panic", "camera", cameraID, "error", r)
			rec.Stop()
			l.mu.Lock()
			delete(l.receivers, cameraID)
			l.mu.Unlock()
		}
	}()

	// Start receiving in a goroutine
	rec.StartListener(conn)

	// Notify callback
	if l.OnConnect != nil {
		l.OnConnect(cameraID, hub)
	}

	// Wait for receiver to finish, then clean up
	<-rec.done

	l.mu.Lock()
	delete(l.receivers, cameraID)
	l.mu.Unlock()

	// Notify disconnect so the IngestRecorder closes its in-flight segment and
	// returns to Idle (ready for the next publisher).
	if l.OnDisconnect != nil {
		l.OnDisconnect(cameraID)
	}

	logger.Info("SRT publisher disconnected", "camera_id", cameraID)
}
