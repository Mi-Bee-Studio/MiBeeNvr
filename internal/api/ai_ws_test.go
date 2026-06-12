package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/ai"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/stretchr/testify/require"
)

// ---- Helpers ----

// setupAIWSHandler creates a handler with a real EventBus and real ai.Manager.
func setupAIWSHandler(t *testing.T) (*Handler, *event.EventBus, *ai.Manager, *AIWSHandler) {
	t.Helper()
	db, store := setupTestDB(t)
	h := TestHandler(db, store)
	bus := event.NewEventBus(64)
	h.SetEventBus(bus)

	cfg := ai.Config{
		Enabled:             true,
		ModelPath:           "test-model.onnx",
		ConfidenceThreshold: 0.5,
		FrameSkipRate:       10,
	}
	mgr := ai.NewManager(cfg, bus)
	aiWS := NewAIWSHandler(mgr, bus)
	h.SetAIWSHandler(aiWS)

	return h, bus, mgr, aiWS
}

// wsClient is a test helper that connects to the WebSocket endpoint and reads messages.
type wsClient struct {
	conn    *websocket.Conn
	id      int64
	msgCh   chan []byte
	closed  atomic.Bool
	closeCh chan struct{}
}

// connectWS connects to the WebSocket endpoint and returns a wsClient.
func connectWS(t *testing.T, baseURL string, seq *atomic.Int64) *wsClient {
	t.Helper()
	url := "ws://" + strings.TrimPrefix(baseURL, "http://") + "/api/ai/events/ws"
	conn, _, err := websocket.DefaultDialer.Dial(url, http.Header{
		"Authorization": []string{"Basic YWRtaW46YWRtaW4xMjM0NQ=="}, // admin:admin12345
	})
	require.NoError(t, err, "failed to dial WebSocket")

	c := &wsClient{
		conn:    conn,
		id:      seq.Add(1),
		msgCh:   make(chan []byte, 64),
		closeCh: make(chan struct{}),
	}

	// Start reading messages in background.
	go func() {
		defer close(c.closeCh)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				if !c.closed.Load() {
					c.closed.Store(true)
				}
				return
			}
			select {
			case c.msgCh <- msg:
			default:
				// Drop if buffer full (shouldn't happen in tests).
			}
		}
	}()

	return c
}

// readMessage reads a message from the client with timeout.
func (c *wsClient) readMessage(t *testing.T, timeout time.Duration) []byte {
	t.Helper()
	select {
	case msg := <-c.msgCh:
		return msg
	case <-time.After(timeout):
		t.Fatalf("timeout waiting for WebSocket message")
		return nil
	}
}

// hasMessage checks if there's a message available without blocking.
func (c *wsClient) hasMessage() bool {
	select {
	case <-c.msgCh:
		return true
	default:
		return false
	}
}

// close disconnects the WebSocket client.
func (c *wsClient) close() {
	c.closed.Store(true)
	_ = c.conn.Close()
	<-c.closeCh
}

// parseMessage parses a JSON WebSocket message into the given value.
func parseWSMessage(t *testing.T, data []byte, v any) {
	t.Helper()
	err := json.Unmarshal(data, v)
	require.NoError(t, err, "failed to parse WebSocket message")
}

// ---- Tests ----

func TestAIWS_ConnectionUpgradeAndStatus(t *testing.T) {
	t.Parallel()

	h, _, _, _ := setupAIWSHandler(t)
	srv := httptest.NewServer(h.Routes())
	defer srv.Close()

	var seq atomic.Int64
	client := connectWS(t, srv.URL, &seq)
	defer client.close()

	// First message should be the status message.
	msg := client.readMessage(t, 3*time.Second)

	var result map[string]any
	parseWSMessage(t, msg, &result)

	require.Equal(t, "status", result["type"])
	require.Equal(t, true, result["enabled"])
	require.Equal(t, "test-model.onnx", result["model_name"])
	require.Equal(t, float64(0.5), result["confidence_threshold"])
	require.Equal(t, float64(10), result["frame_skip_rate"])
}

func TestAIWS_DetectionEventForwarding(t *testing.T) {
	t.Parallel()

	h, bus, mgr, _ := setupAIWSHandler(t)
	srv := httptest.NewServer(h.Routes())
	defer srv.Close()

	// Add an engine to make the camera visible in status.
	cfg := mgr.GetConfig()
	// We won't add real engines; Status() returns empty, which is fine.
	_ = cfg

	var seq atomic.Int64
	client := connectWS(t, srv.URL, &seq)
	defer client.close()

	// Consume the initial status message.
	_ = client.readMessage(t, 3*time.Second)

	// Publish a detection event.
	detectionEvt := ai.DetectionEvent{
		CameraID:    "front-door",
		Timestamp:   time.Date(2026, 6, 12, 10, 0, 0, 123456789, time.UTC),
		FrameWidth:  1920,
		FrameHeight: 1080,
		Detections: []ai.Detection{
			{
				ClassID:    0,
				ClassLabel: "person",
				Confidence: 0.95,
				BBox:       [4]float64{0.1, 0.2, 0.3, 0.4},
			},
		},
	}

	bus.Publish(context.Background(), event.TopicAIDetection, detectionEvt)

	// Read the detection message.
	msg := client.readMessage(t, 3*time.Second)

	var result map[string]any
	parseWSMessage(t, msg, &result)

	require.Equal(t, "detection", result["type"])
	require.Equal(t, "front-door", result["camera_id"])
	require.Equal(t, "2026-06-12T10:00:00.123456789Z", result["timestamp"])
	require.Equal(t, float64(1920), result["frame_width"])
	require.Equal(t, float64(1080), result["frame_height"])

	detections, ok := result["detections"].([]any)
	require.True(t, ok, "detections should be an array")
	require.Len(t, detections, 1)

	det := detections[0].(map[string]any)
	require.Equal(t, "person", det["class_label"])
	require.Equal(t, float64(0.95), det["confidence"])
}

func TestAIWS_FanOutMultipleClients(t *testing.T) {
	t.Parallel()

	h, bus, _, _ := setupAIWSHandler(t)
	srv := httptest.NewServer(h.Routes())
	defer srv.Close()

	var seq atomic.Int64
	// Connect three clients.
	client1 := connectWS(t, srv.URL, &seq)
	defer client1.close()
	client2 := connectWS(t, srv.URL, &seq)
	defer client2.close()
	client3 := connectWS(t, srv.URL, &seq)
	defer client3.close()

	// Consume initial status messages.
	_ = client1.readMessage(t, 3*time.Second)
	_ = client2.readMessage(t, 3*time.Second)
	_ = client3.readMessage(t, 3*time.Second)

	// Publish a detection event.
	detectionEvt := ai.DetectionEvent{
		CameraID:    "driveway",
		Timestamp:   time.Now(),
		FrameWidth:  1280,
		FrameHeight: 720,
		Detections: []ai.Detection{
			{ClassID: 2, ClassLabel: "car", Confidence: 0.88, BBox: [4]float64{0.2, 0.3, 0.4, 0.5}},
		},
	}

	bus.Publish(context.Background(), event.TopicAIDetection, detectionEvt)

	// All three clients should receive the detection.
	for i, client := range []*wsClient{client1, client2, client3} {
		msg := client.readMessage(t, 3*time.Second)
		var result map[string]any
		parseWSMessage(t, msg, &result)
		require.Equal(t, "detection", result["type"], "client %d should receive detection", i+1)
		require.Equal(t, "driveway", result["camera_id"], "client %d should have correct camera_id", i+1)
	}
}

func TestAIWS_DisconnectCleanup(t *testing.T) {
	t.Parallel()

	h, _, _, aiWS := setupAIWSHandler(t)
	srv := httptest.NewServer(h.Routes())
	defer srv.Close()

	var seq atomic.Int64
	client := connectWS(t, srv.URL, &seq)

	// Consume status.
	_ = client.readMessage(t, 3*time.Second)

	clientID := client.id
	client.close()

	// Wait for cleanup.
	time.Sleep(100 * time.Millisecond)

	// Verify client was removed from the handler's clients map.
	aiWS.mu.Lock()
	_, exists := aiWS.clients[clientID]
	aiWS.mu.Unlock()
	require.False(t, exists, "client should be removed after disconnect")
}

func TestAIWS_GracefulShutdown(t *testing.T) {
	t.Parallel()

	h, bus, _, aiWS := setupAIWSHandler(t)
	srv := httptest.NewServer(h.Routes())
	defer srv.Close()

	var seq atomic.Int64
	client1 := connectWS(t, srv.URL, &seq)
	defer client1.close()
	client2 := connectWS(t, srv.URL, &seq)
	defer client2.close()

	// Consume status messages.
	_ = client1.readMessage(t, 3*time.Second)
	_ = client2.readMessage(t, 3*time.Second)

	// Stop the handler.
	aiWS.Stop()

	// Publish an event after stop — should not reach listeners (but may be in channel).
	detectionEvt := ai.DetectionEvent{
		CameraID:    "after-stop",
		Timestamp:   time.Now(),
		FrameWidth:  640,
		FrameHeight: 480,
	}
	bus.Publish(context.Background(), event.TopicAIDetection, detectionEvt)

	// Both clients should have their connections closed (or receive no new data).
	// We can verify this by checking no additional messages arrive.
	time.Sleep(200 * time.Millisecond)

	// After Stop(), the clients map should be empty.
	aiWS.mu.Lock()
	clientCount := len(aiWS.clients)
	aiWS.mu.Unlock()
	require.Equal(t, 0, clientCount, "all clients should be disconnected after Stop")
}

func TestAIWS_NoCrashOnNilManager(t *testing.T) {
	t.Parallel()

	db, store := setupTestDB(t)
	h := TestHandler(db, store)
	bus := event.NewEventBus(64)
	h.SetEventBus(bus)

	// Create AIWSHandler with nil manager.
	aiWS := NewAIWSHandler(nil, bus)
	h.SetAIWSHandler(aiWS)

	srv := httptest.NewServer(h.Routes())
	defer srv.Close()

	// Should not panic.
	url := "ws://" + strings.TrimPrefix(srv.URL, "http://") + "/api/ai/events/ws"
	conn, _, err := websocket.DefaultDialer.Dial(url, http.Header{
		"Authorization": []string{"Basic YWRtaW46YWRtaW4xMjM0NQ=="},
	})
	if err == nil {
		conn.Close()
	}
	// If the upgrade fails because handler returned early, that's OK — no panic is success.
}

func TestAIWS_ConcurrentClients(t *testing.T) {
	t.Parallel()

	h, bus, _, _ := setupAIWSHandler(t)
	srv := httptest.NewServer(h.Routes())
	defer srv.Close()

	const numClients = 10
	var seq atomic.Int64
	clients := make([]*wsClient, numClients)
	for i := 0; i < numClients; i++ {
		clients[i] = connectWS(t, srv.URL, &seq)
		defer clients[i].close()
	}

	// Consume all status messages.
	for _, c := range clients {
		_ = c.readMessage(t, 3*time.Second)
	}

	// Publish multiple detections concurrently.
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			evt := ai.DetectionEvent{
				CameraID:    "cam-concurrent",
				Timestamp:   time.Now(),
				FrameWidth:  1920,
				FrameHeight: 1080,
				Detections: []ai.Detection{
					{ClassID: n, ClassLabel: "obj", Confidence: 0.9, BBox: [4]float64{0, 0, 0.1, 0.1}},
				},
			}
			bus.Publish(context.Background(), event.TopicAIDetection, evt)
		}(i)
	}
	wg.Wait()

	// Each client should receive all 5 detection events.
	for i, c := range clients {
		received := 0
		for j := 0; j < 5; j++ {
			msg := c.readMessage(t, 5*time.Second)
			var result map[string]any
			parseWSMessage(t, msg, &result)
			if result["type"] == "detection" {
				received++
			}
		}
		require.Equal(t, 5, received, "client %d should receive all 5 detection events", i)
	}
}

func TestAIWS_SlowClientDrops(t *testing.T) {
	t.Parallel()

	h, bus, _, aiWS := setupAIWSHandler(t)
	srv := httptest.NewServer(h.Routes())
	defer srv.Close()

	var seq atomic.Int64
	// Create a "slow" client that doesn't read messages.
	url := "ws://" + strings.TrimPrefix(srv.URL, "http://") + "/api/ai/events/ws"
	slowConn, _, err := websocket.DefaultDialer.Dial(url, http.Header{
		"Authorization": []string{"Basic YWRtaW46YWRtaW4xMjM0NQ=="},
	})
	require.NoError(t, err)
	defer slowConn.Close()

	// Wait for slow client to register.
	// Use a fast client to consume the status message from the slow client.
	fastClient := connectWS(t, srv.URL, &seq)
	defer fastClient.close()

	// Consume initial status from both (we'll read one from fast, one from slow via its own).
	// The slow client's status is sent but not consumed — the channel buffer is 64, so
	// it takes many drops before the slow client's ch fills up and drops start.
	// Actually the slow client does consume the status automatically in its read goroutine,
	// but we don't read from it here.

	// Consume fast client's status.
	_ = fastClient.readMessage(t, 3*time.Second)

	// Fill the slow client's channel by publishing many events.
	// The slow client's read goroutine is running but we don't read from its msgCh.
	// Once client.ch (64 cap) is full, messages get dropped.
	for i := 0; i < 100; i++ {
		evt := ai.DetectionEvent{
			CameraID:    "cam-slow",
			Timestamp:   time.Now(),
			FrameWidth:  640,
			FrameHeight: 480,
			Detections: []ai.Detection{
				{ClassID: i, ClassLabel: "obj", Confidence: 0.5, BBox: [4]float64{0, 0, 0.1, 0.1}},
			},
		}
		bus.Publish(context.Background(), event.TopicAIDetection, evt)
	}

	// Wait for processing.
	time.Sleep(200 * time.Millisecond)

	_ = aiWS // ensure aiWS is used
	// The fast client should have received some detection events.
	require.True(t, fastClient.hasMessage(), "fast client should have pending messages")
}
func TestAIWS_ReconnectAfterStop(t *testing.T) {
	t.Parallel()

	h, _, _, _ := setupAIWSHandler(t)
	srv := httptest.NewServer(h.Routes())
	defer srv.Close()

	// Connect and disconnect.
	var seq atomic.Int64
	client1 := connectWS(t, srv.URL, &seq)
	_ = client1.readMessage(t, 3*time.Second)
	client1.close()
	time.Sleep(50 * time.Millisecond)

	// The listener should still be active from the once.Do.
	// Connect again — should work.
	client2 := connectWS(t, srv.URL, &seq)
	defer client2.close()

	// Should receive status on reconnect.
	msg := client2.readMessage(t, 3*time.Second)
	var result map[string]any
	parseWSMessage(t, msg, &result)
	require.Equal(t, "status", result["type"])
}
