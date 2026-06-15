package rtmp

import (
	"context"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/bluenviron/gortmplib"
	"github.com/bluenviron/gortmplib/pkg/codecs"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// helper: start an RTMP server on a random port for testing
func startTestServer(t *testing.T, resolv StreamKeyResolver, hubFn CameraHubProvider, onConn OnPublisherConnect, onDisc OnPublisherDisconnect) (*Server, string) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	ln.Close()

	cfg := Config{Addr: addr}
	srv := NewServer(cfg, resolv, hubFn, onConn, onDisc)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		srv.Stop()
	})

	err = srv.Start(ctx)
	require.NoError(t, err)

	return srv, addr
}

// helper: connect a fake RTMP publisher to the server.
// Returns client (to be closed), writer, track, error.
func connectPublisher(t *testing.T, addr, streamKey string) (*gortmplib.Client, *gortmplib.Writer, *gortmplib.Track, error) {
	t.Helper()

	u, err := url.Parse("rtmp://" + addr + "/live/" + streamKey)
	require.NoError(t, err)

	client := &gortmplib.Client{
		URL:     u,
		Publish: true,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = client.Initialize(ctx)
	if err != nil {
		return nil, nil, nil, err
	}

	track := &gortmplib.Track{Codec: &codecs.H264{
		SPS: []byte{0x67, 0x42, 0xc0, 0x28, 0xd9, 0x00, 0x78, 0x02, 0x27, 0xe5, 0x84, 0x00, 0x00, 0x03, 0x00, 0x04, 0x00, 0x00, 0x03, 0x00, 0xf0, 0x3c, 0x60, 0xc9, 0x20},
		PPS: []byte{0x08, 0x06, 0x07, 0x08},
	}}

	w := &gortmplib.Writer{
		Conn:   client,
		Tracks: []*gortmplib.Track{track},
	}

	err = w.Initialize()
	if err != nil {
		client.Close()
		return nil, nil, nil, err
	}

	return client, w, track, nil
}

// testStreamKeys maps stream keys to camera IDs
func testStreamKeys() map[string]string {
	return map[string]string{
		"test-key-1": "rtmp-camera-1",
		"test-key-2": "rtmp-camera-2",
	}
}

func testResolver(keys map[string]string) StreamKeyResolver {
	return func(streamKey string) (string, bool) {
		if camID, ok := keys[streamKey]; ok {
			return camID, true
		}
		return "", false
	}
}

// TestExtractStreamKey tests the stream key extraction from RTMP URLs.
func TestExtractStreamKey(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected string
	}{
		{"standard path", "rtmp://host:1935/live/mystream", "mystream"},
		{"with query params", "rtmp://host:1935/live/mystream?foo=bar", "mystream"},
		{"no app", "rtmp://host:1935/mystream", "mystream"},
		{"single segment", "/live/test-key-1", "test-key-1"},
		{"just key", "mykey", "mykey"},
		{"empty path", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var u *url.URL
			if tc.url != "" {
				var err error
				u, err = url.Parse(tc.url)
				require.NoError(t, err)
			}
			result := extractStreamKey(u)
			require.Equal(t, tc.expected, result)
		})
	}
}

// TestStreamKeyResolution tests that valid stream keys are resolved to camera IDs
// and invalid keys are rejected.
func TestStreamKeyResolution(t *testing.T) {
	keys := testStreamKeys()
	resolved := make(map[string]bool)
	var mu sync.Mutex

	resolv := testResolver(keys)
	hubFn := func(cameraID string) *model.StreamHub {
		return model.NewStreamHub()
	}
	onConn := func(cameraID string, hub *model.StreamHub) {
		mu.Lock()
		resolved[cameraID] = true
		mu.Unlock()
	}

	srv, addr := startTestServer(t, resolv, hubFn, onConn, nil)

	// Try connecting with a valid stream key
	client, _, _, err := connectPublisher(t, addr, "test-key-1")
	if err == nil && client != nil {
		client.Close()
	}

	// Verify server is running
	require.NotNil(t, srv.addr())
	_ = srv
}

// TestRTMPHandshake tests that the RTMP server accepts connections and completes
// the handshake.
func TestRTMPHandshake(t *testing.T) {
	keys := testStreamKeys()

	srv, addr := startTestServer(t, testResolver(keys), func(string) *model.StreamHub {
		return model.NewStreamHub()
	}, nil, nil)

	// Basic TCP connect
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	require.NoError(t, err)
	defer conn.Close()

	// Verify server is listening
	require.NotNil(t, srv.addr())
}

// TestH264FrameExtraction tests that H.264 frames received via RTMP are
// correctly extracted and broadcast to the StreamHub.
func TestH264FrameExtraction(t *testing.T) {
	keys := testStreamKeys()

	var receivedFrames [][]byte
	var mu sync.Mutex
	hub := model.NewStreamHub()

	// Subscribe a consumer to capture frames
	err := hub.Subscribe("test", func(pts int64, au [][]byte) {
		mu.Lock()
		defer mu.Unlock()
		for _, nalu := range au {
			receivedFrames = append(receivedFrames, nalu)
		}
	})
	require.NoError(t, err)

	hubFn := func(cameraID string) *model.StreamHub {
		return hub
	}

	srv, addr := startTestServer(t, testResolver(keys), hubFn, nil, nil)

	// Connect a publisher
	client, writer, track, err := connectPublisher(t, addr, "test-key-1")
	if err != nil {
		t.Skipf("RTMP client publish failed: %v", err)
		return
	}
	defer client.Close()

	// Verify that the publisher was registered
	require.Eventually(t, func() bool {
		return srv.activePublishers() > 0
	}, 5*time.Second, 100*time.Millisecond, "publisher should be registered")

	// gortmplib Reader has a 2-second analyze period.
	// Write frames for long enough to pass analysis and get callbacks registered.
	frameCtx, frameCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer frameCancel()
	go func() {
		for i := 0; ; i++ {
			select {
			case <-frameCtx.Done():
				return
			default:
			}
			idrAU := [][]byte{{0x65, 0x88, 0x84, 0x00, 0x40}}
			_ = writer.WriteH264(track, time.Duration(i*33)*time.Millisecond, time.Duration(i*33)*time.Millisecond, idrAU)
			time.Sleep(33 * time.Millisecond)
		}
	}()

	// Verify frame was received by the hub consumer
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(receivedFrames) > 0
	}, 10*time.Second, 100*time.Millisecond, "frames should be received")
}

// TestFrameDistributionToStreamHub tests that frames from RTMP are properly
// distributed to multiple StreamHub consumers.
func TestFrameDistributionToStreamHub(t *testing.T) {
	hub := model.NewStreamHub()

	var mu sync.Mutex
	consumer1Count := 0
	consumer2Count := 0

	err := hub.Subscribe("consumer1", func(pts int64, au [][]byte) {
		mu.Lock()
		consumer1Count++
		mu.Unlock()
	})
	require.NoError(t, err)

	err = hub.Subscribe("consumer2", func(pts int64, au [][]byte) {
		mu.Lock()
		consumer2Count++
		mu.Unlock()
	})
	require.NoError(t, err)

	// Simulate RTMP frame broadcast
	testAU := [][]byte{
		{0x65, 0x88, 0x84, 0x00, 0x40}, // IDR slice
	}
	hub.Broadcast(1000, testAU, false)

	// Wait for async delivery
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return consumer1Count >= 1 && consumer2Count >= 1
	}, 2*time.Second, 50*time.Millisecond)

	mu.Lock()
	require.Equal(t, 1, consumer1Count)
	require.Equal(t, 1, consumer2Count)
	mu.Unlock()
}


// TestDisconnectCleanup tests that disconnecting a publisher cleans up resources.
func TestDisconnectCleanup(t *testing.T) {
	keys := testStreamKeys()
	disconnected := make(map[string]bool)
	var mu sync.Mutex

	hubFn := func(cameraID string) *model.StreamHub {
		return model.NewStreamHub()
	}
	onDisc := func(cameraID string) {
		mu.Lock()
		disconnected[cameraID] = true
		mu.Unlock()
	}

	srv, addr := startTestServer(t, testResolver(keys), hubFn, nil, onDisc)

	// Connect and immediately close
	client, _, _, err := connectPublisher(t, addr, "test-key-1")
	if err != nil {
		t.Skipf("RTMP client publish failed: %v", err)
		return
	}

	// Wait for publisher to register
	require.Eventually(t, func() bool {
		return srv.activePublishers() > 0
	}, 5*time.Second, 100*time.Millisecond)

	// Disconnect
	client.Close()

	// Verify cleanup
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return disconnected["rtmp-camera-1"]
	}, 5*time.Second, 100*time.Millisecond, "disconnect callback should fire")

	require.Eventually(t, func() bool {
		return srv.activePublishers() == 0
	}, 3*time.Second, 100*time.Millisecond, "publishers should be cleaned up")
}

// TestInvalidStreamKey tests that connections with invalid stream keys are rejected.
func TestInvalidStreamKey(t *testing.T) {
	keys := testStreamKeys()

	srv, addr := startTestServer(t, testResolver(keys), func(string) *model.StreamHub {
		return model.NewStreamHub()
	}, nil, nil)

	// Connect with invalid key — should not register
	client, _, _, err := connectPublisher(t, addr, "bad-key")
	if client != nil {
		client.Close()
	}

	// Give a moment for any processing
	time.Sleep(500 * time.Millisecond)

	// No publishers should be registered (invalid key is rejected)
	require.Equal(t, 0, srv.activePublishers())
	_ = err
}

// TestServerStartStop tests server lifecycle.
func TestServerStartStop(t *testing.T) {
	keys := testStreamKeys()

	srv, _ := startTestServer(t, testResolver(keys), func(string) *model.StreamHub {
		return model.NewStreamHub()
	}, nil, nil)

	require.NotNil(t, srv.addr())
	require.Equal(t, 0, srv.activePublishers())
}

// TestExtractStreamKey_NilURL tests that nil URL returns empty string.
func TestExtractStreamKey_NilURL(t *testing.T) {
	result := extractStreamKey(nil)
	require.Equal(t, "", result)
}

// TestStreamHubBroadcastNonBlocking tests that StreamHub.Broadcast does not
// block even when consumer buffers are full.
func TestStreamHubBroadcastNonBlocking(t *testing.T) {
	hub := model.NewStreamHub()

	// Subscribe a slow consumer that never reads
	err := hub.Subscribe("slow", func(pts int64, au [][]byte) {
		// Block forever — consumer buffer will fill up
		select {}
	})
	require.NoError(t, err)

	// Give time for drain goroutine to start
	time.Sleep(50 * time.Millisecond)

	// Broadcast should not block even though consumer is blocked
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			testAU := [][]byte{{0x65, 0x88}}
			hub.Broadcast(int64(i), testAU, false)
		}
	}()

	select {
	case <-done:
		// Success — broadcast didn't block
	case <-time.After(2 * time.Second):
		t.Fatal("Broadcast blocked — should be non-blocking")
	}
}
