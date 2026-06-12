package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/ai"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
)

// aiWSClient represents a single WebSocket client connected to the AI events stream.
type aiWSClient struct {
	id     int64
	conn   *websocket.Conn
	ch     chan []byte
	cancel context.CancelFunc
}

// AIWSHandler handles WebSocket connections for streaming AI detection events.
type AIWSHandler struct {
	manager  *ai.Manager
	eventBus *event.EventBus
	upgrader websocket.Upgrader
	logger   *slog.Logger

	mu        sync.Mutex
	clients   map[int64]*aiWSClient
	clientSeq atomic.Int64

	ctx       context.Context
	cancel    context.CancelFunc
	startOnce sync.Once
}

// NewAIWSHandler creates a new AIWSHandler.
func NewAIWSHandler(mgr *ai.Manager, bus *event.EventBus) *AIWSHandler {
	return &AIWSHandler{
		manager:  mgr,
		eventBus: bus,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin:     func(r *http.Request) bool { return true },
		},
		logger:  slog.Default().With("component", "ai-ws"),
		clients: make(map[int64]*aiWSClient),
	}
}

// ServeWS handles GET /api/ai/events/ws.
// It upgrades the HTTP connection to WebSocket and streams AI detection events
// to the client as JSON messages.
func (h *AIWSHandler) ServeWS(w http.ResponseWriter, r *http.Request) {
	if h.manager == nil || h.eventBus == nil {
		h.logger.Warn("AI WS handler not initialized")
		return
	}

	// Ensure the event listener goroutine is started (lazy init, once).
	h.startOnce.Do(func() {
		h.ctx, h.cancel = context.WithCancel(context.Background())
		go h.listenerLoop(h.ctx)
	})
	// Upgrade HTTP to WebSocket.
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.Warn("WebSocket upgrade failed", "error", err)
		return
	}

	clientCtx, clientCancel := context.WithCancel(r.Context())
	clientID := h.clientSeq.Add(1)
	client := &aiWSClient{
		id:     clientID,
		conn:   conn,
		ch:     make(chan []byte, 64),
		cancel: clientCancel,
	}

	// Register client.
	h.mu.Lock()
	h.clients[clientID] = client
	clientCount := len(h.clients)
	h.mu.Unlock()

	h.logger.Debug("AI WS client connected", "client_id", clientID, "active_clients", clientCount)

	// Cleanup on exit.
	defer func() {
		clientCancel()
		h.mu.Lock()
		delete(h.clients, clientID)
		remaining := len(h.clients)
		h.mu.Unlock()
		_ = conn.Close()
		h.logger.Debug("AI WS client disconnected", "client_id", clientID, "active_clients", remaining)
	}()

	// Send initial AI status as first message.
	if err := h.sendStatusMessage(conn); err != nil {
		h.logger.Warn("Failed to send initial status", "client_id", clientID, "error", err)
		return
	}

	// Start read pump to detect client disconnect.
	go h.readPump(clientCtx, clientCancel, conn, clientID)

	// Write pump: drain client.ch to WebSocket connection.
	for {
		select {
		case <-clientCtx.Done():
			return
		case data, ok := <-client.ch:
			if !ok {
				return
			}
			if err := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
				return
			}
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				h.logger.Debug("AI WS write error", "client_id", clientID, "error", err)
				return
			}
		}
	}
}

// sendStatusMessage marshals the current AI status and sends it as JSON
// with type "status" over the WebSocket connection.
func (h *AIWSHandler) sendStatusMessage(conn *websocket.Conn) error {
	cfg := h.manager.GetConfig()
	cameraStatuses := h.manager.Status()

	var activeCameras int
	for _, s := range cameraStatuses {
		if s.Running {
			activeCameras++
		}
	}

	msg := map[string]any{
		"type":                 "status",
		"enabled":              cfg.Enabled,
		"ncnn_available":       h.manager.IsNCNNAvailable(),
		"model_name":           cfg.ModelPath,
		"active_cameras":       activeCameras,
		"cameras":              cameraStatuses,
		"confidence_threshold": cfg.ConfidenceThreshold,
		"frame_skip_rate":      cfg.FrameSkipRate,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	if err := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, data)
}

// readPump reads from the WebSocket connection to detect client disconnect.
// When a read error occurs (client disconnected), it cancels the client context.
func (h *AIWSHandler) readPump(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, clientID int64) {
	defer func() {
		if r := recover(); r != nil {
			h.logger.Warn("AI WS read pump panic recovered", "client_id", clientID, "error", r)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		_, _, err := conn.ReadMessage()
		if err != nil {
			// Client disconnected or read error — cancel context to stop write pump.
			h.logger.Debug("AI WS client read error", "client_id", clientID, "error", err)
			cancel()
			return
		}
	}
}

// listenerLoop subscribes to EventBus TopicAIDetection and fans out
// detection events as JSON to all connected WebSocket clients.
func (h *AIWSHandler) listenerLoop(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			h.logger.Warn("AI WS listener loop panic recovered", "error", r)
		}
	}()

	eventCh := make(chan event.Event, 64)
	if err := h.eventBus.Subscribe(event.TopicAIDetection, eventCh, 64); err != nil {
		h.logger.Error("AI WS listener: failed to subscribe", "error", err)
		return
	}
	defer h.eventBus.Unsubscribe(event.TopicAIDetection, eventCh)

	h.logger.Info("AI WS listener started")

	for {
		select {
		case <-ctx.Done():
			h.logger.Info("AI WS listener stopped")
			return
		case evt, ok := <-eventCh:
			if !ok {
				h.logger.Warn("AI WS listener: event channel closed")
				return
			}

			detectionEvt, ok := evt.Data.(ai.DetectionEvent)
			if !ok {
				continue
			}

			msg := map[string]any{
				"type":         "detection",
				"camera_id":    detectionEvt.CameraID,
				"timestamp":    detectionEvt.Timestamp.Format(time.RFC3339Nano),
				"detections":   detectionEvt.Detections,
				"frame_width":  detectionEvt.FrameWidth,
				"frame_height": detectionEvt.FrameHeight,
			}

			data, err := json.Marshal(msg)
			if err != nil {
				h.logger.Warn("AI WS listener: marshal error", "error", err)
				continue
			}

			// Fan-out to all connected clients (non-blocking per client).
			h.mu.Lock()
			for _, client := range h.clients {
				select {
				case client.ch <- data:
				default:
					// Slow client — drop message.
					h.logger.Warn("AI WS: dropping detection for slow client", "client_id", client.id)
				}
			}
			h.mu.Unlock()
		}
	}
}

// Stop gracefully shuts down the listener and disconnects all clients.
func (h *AIWSHandler) Stop() {
	if h.cancel != nil {
		h.cancel()
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	for _, client := range h.clients {
		client.cancel()
		_ = client.conn.Close()
		close(client.ch)
	}
	h.clients = make(map[int64]*aiWSClient)
}
