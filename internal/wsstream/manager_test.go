package wsstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── helpers ─────────────────────────────────────────────────────────────

func newTestHub(t *testing.T) *model.StreamHub {
	t.Helper()
	return model.NewStreamHub()
}

func broadcastFrame(t *testing.T, hub *model.StreamHub, pts int64, au [][]byte) {
	t.Helper()
	hub.Broadcast(pts, au, false)
}

var sampleSPS = []byte{0x67, 0x42, 0xc0, 0x1e, 0xd9, 0x00, 0xa0, 0x47, 0xfe, 0xd8}
var samplePPS = []byte{0x68, 0xce, 0x38, 0x80}
var sampleVPS = []byte{0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60, 0x00, 0x00}

func dialWS(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	dialer := websocket.Dialer{}
	conn, resp, err := dialer.Dial(url, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("WebSocket dial failed: %v (HTTP %d)", err, resp.StatusCode)
		}
		t.Fatalf("WebSocket dial failed: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	return conn
}

func readMessage(t *testing.T, conn *websocket.Conn) ([]byte, error) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, msg, err := conn.ReadMessage()
	return msg, err
}

// eventually polls fn until it returns true or timeout elapses.
func eventually(t *testing.T, fn func() bool, timeout, interval time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if fn() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("eventually: timed out")
		case <-ticker.C:
		}
	}
}

// ─── tests ───────────────────────────────────────────────────────────────

func TestNewManager(t *testing.T) {
	m := NewManager()
	assert.NotNil(t, m)
	assert.Equal(t, 10, m.maxViewers)
	assert.Equal(t, 100, m.writeBufSize)
	assert.Equal(t, 60*time.Second, m.idleTimeout)
}

func TestNewManagerWithOptions(t *testing.T) {
	m := NewManager(
		WithMaxViewers(5),
		WithWriteBufSize(50),
		WithIdleTimeout(30*time.Second),
	)
	assert.Equal(t, 5, m.maxViewers)
	assert.Equal(t, 50, m.writeBufSize)
	assert.Equal(t, 30*time.Second, m.idleTimeout)
}

func TestNewManagerOptionsIgnoreZero(t *testing.T) {
	m := NewManager(
		WithMaxViewers(0),
		WithWriteBufSize(0),
		WithIdleTimeout(0),
	)
	assert.Equal(t, 10, m.maxViewers)
	assert.Equal(t, 100, m.writeBufSize)
	assert.Equal(t, 60*time.Second, m.idleTimeout)
}

func TestRegisterStream(t *testing.T) {
	m := NewManager()
	hub := newTestHub(t)

	err := m.RegisterStream("cam1", model.FormatH264, sampleSPS, samplePPS, nil, hub)
	require.NoError(t, err)
	assert.True(t, m.IsActive("cam1"))
	assert.Equal(t, 1, hub.ConsumerCount())
}

func TestRegisterStream_AlreadyExists(t *testing.T) {
	m := NewManager()
	hub := newTestHub(t)

	err := m.RegisterStream("cam1", model.FormatH264, sampleSPS, samplePPS, nil, hub)
	require.NoError(t, err)

	err = m.RegisterStream("cam1", model.FormatH264, sampleSPS, samplePPS, nil, hub)
	assert.ErrorIs(t, err, ErrStreamExists)
}

func TestRegisterStream_NilHub(t *testing.T) {
	m := NewManager()

	err := m.RegisterStream("cam1", model.FormatH264, sampleSPS, samplePPS, nil, nil)
	require.NoError(t, err)
	assert.True(t, m.IsActive("cam1"))
}

func TestUnregisterStream(t *testing.T) {
	m := NewManager()
	hub := newTestHub(t)

	err := m.RegisterStream("cam1", model.FormatH264, sampleSPS, samplePPS, nil, hub)
	require.NoError(t, err)

	m.UnregisterStream("cam1")
	assert.False(t, m.IsActive("cam1"))
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, hub.ConsumerCount())
}

func TestUnregisterStream_NotExists(t *testing.T) {
	m := NewManager()
	m.UnregisterStream("nonexistent")
}

func TestStopAll(t *testing.T) {
	m := NewManager()
	hub1 := newTestHub(t)
	hub2 := newTestHub(t)

	_ = m.RegisterStream("cam1", model.FormatH264, sampleSPS, samplePPS, nil, hub1)
	_ = m.RegisterStream("cam2", model.FormatH265, sampleSPS, samplePPS, sampleVPS, hub2)

	m.StopAll()
	assert.False(t, m.IsActive("cam1"))
	assert.False(t, m.IsActive("cam2"))

	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, hub1.ConsumerCount())
	assert.Equal(t, 0, hub2.ConsumerCount())
}

func TestViewerCount(t *testing.T) {
	m := NewManager()
	assert.Equal(t, 0, m.ViewerCount("nonexistent"))

	hub := newTestHub(t)
	_ = m.RegisterStream("cam1", model.FormatH264, sampleSPS, samplePPS, nil, hub)
	assert.Equal(t, 0, m.ViewerCount("cam1"))
}

func TestServeWS_CodecInfoFirstMessage(t *testing.T) {
	m := NewManager()
	hub := newTestHub(t)

	err := m.RegisterStream("cam1", model.FormatH264, sampleSPS, samplePPS, nil, hub)
	require.NoError(t, err)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = m.ServeWS("cam1", w, r)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/"
	conn := dialWS(t, wsURL)
	defer conn.Close()

	msg, err := readMessage(t, conn)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(msg), 5)
	assert.Equal(t, MsgTypeCodecInfo, msg[0])

	ci, err := DecodeCodecInfo(msg)
	require.NoError(t, err)
	assert.Equal(t, CodecH264, ci.Codec)
	assert.Equal(t, sampleSPS, ci.SPS)
	assert.Equal(t, samplePPS, ci.PPS)
}

func TestServeWS_CodecInfoH265(t *testing.T) {
	m := NewManager()
	hub := newTestHub(t)

	err := m.RegisterStream("cam1", model.FormatH265, sampleSPS, samplePPS, sampleVPS, hub)
	require.NoError(t, err)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = m.ServeWS("cam1", w, r)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/"
	conn := dialWS(t, wsURL)
	defer conn.Close()

	msg, err := readMessage(t, conn)
	require.NoError(t, err)

	ci, err := DecodeCodecInfo(msg)
	require.NoError(t, err)
	assert.Equal(t, CodecH265, ci.Codec)
	assert.Equal(t, sampleVPS, ci.VPS)
}

func TestServeWS_FrameStreaming(t *testing.T) {
	m := NewManager()
	hub := newTestHub(t)

	err := m.RegisterStream("cam1", model.FormatH264, sampleSPS, samplePPS, nil, hub)
	require.NoError(t, err)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = m.ServeWS("cam1", w, r)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/"
	conn := dialWS(t, wsURL)
	defer conn.Close()

	// Read CodecInfo first
	msg, err := readMessage(t, conn)
	require.NoError(t, err)
	assert.Equal(t, MsgTypeCodecInfo, msg[0])

	// Broadcast a frame
	idrNALU := []byte{0x65, 0x01, 0x02, 0x03}
	broadcastFrame(t, hub, 90000, [][]byte{idrNALU})

	// Read the video frame
	msg, err = readMessage(t, conn)
	require.NoError(t, err)
	assert.Equal(t, MsgTypeVideoFrame, msg[0])

	vf, err := DecodeVideoFrame(msg)
	require.NoError(t, err)
	assert.Equal(t, int64(90000), vf.PTS)
	assert.True(t, vf.IsKeyframe)
	assert.Len(t, vf.NALUs, 1)
	assert.Equal(t, idrNALU, vf.NALUs[0])
}

func TestServeWS_MultipleFrames(t *testing.T) {
	m := NewManager()
	hub := newTestHub(t)

	err := m.RegisterStream("cam1", model.FormatH264, sampleSPS, samplePPS, nil, hub)
	require.NoError(t, err)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = m.ServeWS("cam1", w, r)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/"
	conn := dialWS(t, wsURL)
	defer conn.Close()

	// Read CodecInfo
	_, err = readMessage(t, conn)
	require.NoError(t, err)

	// Send 3 frames
	for i := 0; i < 3; i++ {
		nalu := []byte{0x65, byte(i), 0x02, 0x03}
		broadcastFrame(t, hub, int64(90000*(i+1)), [][]byte{nalu})
		time.Sleep(10 * time.Millisecond)
	}

	// Read 3 video frames
	for i := 0; i < 3; i++ {
		msg, err := readMessage(t, conn)
		require.NoError(t, err, "frame %d", i)
		assert.Equal(t, MsgTypeVideoFrame, msg[0], "frame %d", i)

		vf, err := DecodeVideoFrame(msg)
		require.NoError(t, err)
		assert.Equal(t, int64(90000*(i+1)), vf.PTS)
	}
}

func TestServeWS_NonexistentStream(t *testing.T) {
	m := NewManager()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := m.ServeWS("nonexistent", w, r)
		require.ErrorIs(t, err, ErrStreamNotActive)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Get(server.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.NotEqual(t, http.StatusSwitchingProtocols, resp.StatusCode)
}

func TestServeWS_MaxViewers(t *testing.T) {
	m := NewManager(WithMaxViewers(1))
	hub := newTestHub(t)

	err := m.RegisterStream("cam1", model.FormatH264, sampleSPS, samplePPS, nil, hub)
	require.NoError(t, err)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = m.ServeWS("cam1", w, r)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/"

	conn1 := dialWS(t, wsURL)
	defer conn1.Close()

	_, err = readMessage(t, conn1)
	require.NoError(t, err)

	dialer := websocket.Dialer{}
	_, resp, err := dialer.Dial(wsURL, nil)
	assert.True(t, err != nil || (resp != nil && resp.StatusCode != http.StatusSwitchingProtocols),
		"expected second connection to fail")
}

func TestServeWS_DisconnectCleanup(t *testing.T) {
	m := NewManager()
	hub := newTestHub(t)

	err := m.RegisterStream("cam1", model.FormatH264, sampleSPS, samplePPS, nil, hub)
	require.NoError(t, err)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = m.ServeWS("cam1", w, r)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/"
	conn := dialWS(t, wsURL)

	// Read CodecInfo
	_, err = readMessage(t, conn)
	require.NoError(t, err)

	conn.Close()

	// Poll for cleanup — read pump detects close and calls viewerCancel
	eventually(t, func() bool {
		return m.ViewerCount("cam1") == 0
	}, 200*time.Millisecond, 10*time.Millisecond)
}

func TestNonBlockingChannelDrop(t *testing.T) {
	m := NewManager(WithWriteBufSize(5))
	hub := newTestHub(t)

	err := m.RegisterStream("cam1", model.FormatH264, sampleSPS, samplePPS, nil, hub)
	require.NoError(t, err)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = m.ServeWS("cam1", w, r)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/"
	conn := dialWS(t, wsURL)
	defer conn.Close()

	_, err = readMessage(t, conn)
	require.NoError(t, err)

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	for i := 0; i < 200; i++ {
		nalu := []byte{0x65, byte(i)}
		hub.Broadcast(int64(90000*(i+1)), [][]byte{nalu}, false)
	}

	time.Sleep(50 * time.Millisecond)
}

func TestIdleTimeout(t *testing.T) {
	m := NewManager(WithIdleTimeout(200*time.Millisecond), WithWriteBufSize(5))
	hub := newTestHub(t)

	err := m.RegisterStream("cam1", model.FormatH264, sampleSPS, samplePPS, nil, hub)
	require.NoError(t, err)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = m.ServeWS("cam1", w, r)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/"
	conn := dialWS(t, wsURL)

	_, err = readMessage(t, conn)
	require.NoError(t, err)

	// Wait for idle timeout — no frames sent, watchdog triggers
	eventually(t, func() bool {
		return m.ViewerCount("cam1") == 0
	}, 500*time.Millisecond, 20*time.Millisecond)
}

func TestServeWS_ContextCancel(t *testing.T) {
	m := NewManager()
	hub := newTestHub(t)

	err := m.RegisterStream("cam1", model.FormatH264, sampleSPS, samplePPS, nil, hub)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = m.ServeWS("cam1", w, r.Clone(ctx))
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/"
	conn := dialWS(t, wsURL)

	_, err = readMessage(t, conn)
	require.NoError(t, err)

	cancel()

	eventually(t, func() bool {
		return m.ViewerCount("cam1") == 0
	}, 200*time.Millisecond, 10*time.Millisecond)
}

func TestUnregisterStream_DisconnectsViewers(t *testing.T) {
	m := NewManager()
	hub := newTestHub(t)

	err := m.RegisterStream("cam1", model.FormatH264, sampleSPS, samplePPS, nil, hub)
	require.NoError(t, err)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = m.ServeWS("cam1", w, r)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/"
	conn := dialWS(t, wsURL)
	defer conn.Close()

	_, err = readMessage(t, conn)
	require.NoError(t, err)

	m.UnregisterStream("cam1")
	eventually(t, func() bool {
		return m.ViewerCount("cam1") == 0
	}, 200*time.Millisecond, 10*time.Millisecond)
}

func TestGoroutineCleanup(t *testing.T) {
	m := NewManager()
	hub := newTestHub(t)

	err := m.RegisterStream("cam1", model.FormatH264, sampleSPS, samplePPS, nil, hub)
	require.NoError(t, err)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = m.ServeWS("cam1", w, r)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/"

	for i := 0; i < 5; i++ {
		conn := dialWS(t, wsURL)
		_, _ = readMessage(t, conn)
		conn.Close()
		time.Sleep(20 * time.Millisecond)
	}

	m.StopAll()
	time.Sleep(200 * time.Millisecond)
}

func TestMultipleViewers(t *testing.T) {
	m := NewManager(WithMaxViewers(5), WithWriteBufSize(100))
	hub := newTestHub(t)

	err := m.RegisterStream("cam1", model.FormatH264, sampleSPS, samplePPS, nil, hub)
	require.NoError(t, err)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = m.ServeWS("cam1", w, r)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/"

	var conns []*websocket.Conn
	for i := 0; i < 3; i++ {
		conn := dialWS(t, wsURL)
		conns = append(conns, conn)
	}

	for _, conn := range conns {
		msg, err := readMessage(t, conn)
		require.NoError(t, err)
		assert.Equal(t, MsgTypeCodecInfo, msg[0])
	}

	assert.Equal(t, 3, m.ViewerCount("cam1"))

	nalu := []byte{0x65, 0x01, 0x02, 0x03}
	hub.Broadcast(90000, [][]byte{nalu}, false)

	for _, conn := range conns {
		msg, err := readMessage(t, conn)
		require.NoError(t, err)
		assert.Equal(t, MsgTypeVideoFrame, msg[0])
	}

	for _, conn := range conns {
		conn.Close()
	}
}

func TestWriteFrame_EmptyAU(t *testing.T) {
	m := NewManager()
	hub := newTestHub(t)

	err := m.RegisterStream("cam1", model.FormatH264, sampleSPS, samplePPS, nil, hub)
	require.NoError(t, err)

	hub.Broadcast(90000, nil, false)
	hub.Broadcast(90000, [][]byte{}, false)

	time.Sleep(20 * time.Millisecond)
	m.StopAll()
}

func TestServeWS_ConcurrentStreams(t *testing.T) {
	m := NewManager(WithMaxViewers(5))
	hub1 := newTestHub(t)
	hub2 := newTestHub(t)

	_ = m.RegisterStream("cam1", model.FormatH264, sampleSPS, samplePPS, nil, hub1)
	_ = m.RegisterStream("cam2", model.FormatH265, sampleSPS, samplePPS, sampleVPS, hub2)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		camID := r.URL.Query().Get("cam")
		_ = m.ServeWS(camID, w, r)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL1 := "ws" + strings.TrimPrefix(server.URL, "http") + "/?cam=cam1"
	wsURL2 := "ws" + strings.TrimPrefix(server.URL, "http") + "/?cam=cam2"

	conn1 := dialWS(t, wsURL1)
	defer conn1.Close()
	conn2 := dialWS(t, wsURL2)
	defer conn2.Close()

	msg1, err := readMessage(t, conn1)
	require.NoError(t, err)
	ci1, err := DecodeCodecInfo(msg1)
	require.NoError(t, err)
	assert.Equal(t, CodecH264, ci1.Codec)

	msg2, err := readMessage(t, conn2)
	require.NoError(t, err)
	ci2, err := DecodeCodecInfo(msg2)
	require.NoError(t, err)
	assert.Equal(t, CodecH265, ci2.Codec)

	hub1.Broadcast(90000, [][]byte{{0x65, 0x01}}, false)
	hub2.Broadcast(90000, [][]byte{{0x26, 0x01}}, false)

	msg1, err = readMessage(t, conn1)
	require.NoError(t, err)
	assert.Equal(t, MsgTypeVideoFrame, msg1[0])

	msg2, err = readMessage(t, conn2)
	require.NoError(t, err)
	assert.Equal(t, MsgTypeVideoFrame, msg2[0])
}

func TestWriteFrame_NonexistentStream(t *testing.T) {
	m := NewManager()
	m.WriteH264("nonexistent", 90000, [][]byte{{0x65}})
	m.WriteH265("nonexistent", 90000, [][]byte{{0x26}})
	time.Sleep(10 * time.Millisecond)
}

func TestWriteFrame_H265KeyframeDetection(t *testing.T) {
	m := NewManager(WithWriteBufSize(100))
	hub := newTestHub(t)

	err := m.RegisterStream("cam1", model.FormatH265, sampleSPS, samplePPS, sampleVPS, hub)
	require.NoError(t, err)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = m.ServeWS("cam1", w, r)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/"
	conn := dialWS(t, wsURL)
	defer conn.Close()

	// Read CodecInfo first
	_, err = readMessage(t, conn)
	require.NoError(t, err)

	// H.265 IDR_W_RADL (type 19): first byte = 0 | 19<<1 | 0 = 0x26
	idrNALU := []byte{0x26, 0x01, 0x02, 0x03}
	broadcastFrame(t, hub, 90000, [][]byte{idrNALU})

	msg, err := readMessage(t, conn)
	require.NoError(t, err)

	vf, err := DecodeVideoFrame(msg)
	require.NoError(t, err)
	assert.True(t, vf.IsKeyframe, "H.265 IDR should be detected as keyframe")
	assert.Equal(t, int64(90000), vf.PTS)
}

// TestManagerInterface verifies the Manager satisfies expected interface.
func TestManagerInterface(t *testing.T) {
	var _ interface {
		RegisterStream(camID string, codec model.Format, sps, pps, vps []byte, hub *model.StreamHub) error
		UnregisterStream(camID string)
		IsActive(camID string) bool
		ViewerCount(camID string) int
		WriteH264(camID string, pts int64, au [][]byte)
		WriteH265(camID string, pts int64, au [][]byte)
		ServeWS(camID string, w http.ResponseWriter, r *http.Request) error
		StopAll()
	} = (*Manager)(nil)
}

// Benchmark writing frames through the manager.
func BenchmarkWriteFrame(b *testing.B) {
	m := NewManager(WithWriteBufSize(100))
	hub := model.NewStreamHub()
	_ = m.RegisterStream("cam1", model.FormatH264, sampleSPS, samplePPS, nil, hub)
	defer m.StopAll()

	nalu := []byte{0x65, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.WriteH264("cam1", int64(90000*(i+1)), [][]byte{nalu})
	}
}

func BenchmarkEncodeVideoFrame(b *testing.B) {
	nalu := make([]byte, 1024)
	for i := range nalu {
		nalu[i] = byte(i)
	}
	vf := &VideoFrame{
		PTS:        90000,
		IsKeyframe: true,
		NALUs:      [][]byte{nalu, nalu},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = EncodeVideoFrame(vf)
	}
}
