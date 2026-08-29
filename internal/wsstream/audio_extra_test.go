// SPDX-License-Identifier: MIT
//
// Long-tail coverage: audio info configuration, audio frame distribution,
// viewer-channel drain on shutdown, the AudioUpstream WS loop driven directly,
// and the metrics option.

package wsstream

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

var audioTestSPS = []byte{0x67, 0x42, 0xc0, 0x0a, 0xd9, 0x00, 0xa0, 0x47, 0xfe, 0x88}

func TestWithMetricsOption(t *testing.T) {
	m := NewManager(WithMetrics(metrics.NewMetrics()))
	require.NotNil(t, m.metrics)
	require.NoError(t, m.RegisterStream("cam-met", model.FormatH264, audioTestSPS, nil, nil, nil))
	m.UnregisterStream("cam-met")

	// Without metrics the noop counters still satisfy Inc on the drop path.
	m2 := NewManager()
	require.NoError(t, m2.RegisterStream("cam-noop", model.FormatH264, audioTestSPS, nil, nil, nil))
	m2.UnregisterStream("cam-noop")
}

func TestViewerCountPaths(t *testing.T) {
	m := NewManager()
	require.Equal(t, 0, m.ViewerCount("cam-none"))

	require.NoError(t, m.RegisterStream("cam-vc", model.FormatH264, audioTestSPS, nil, nil, nil))
	require.Equal(t, 0, m.ViewerCount("cam-vc")) // registered, no viewers yet
	m.UnregisterStream("cam-vc")
}

func TestSetAudioInfo(t *testing.T) {
	m := NewManager()
	hub := streamhub.New()
	require.NoError(t, m.RegisterStream("cam-aud", model.FormatH264, audioTestSPS, nil, nil, hub))

	// Unknown camera.
	require.ErrorIs(t, m.SetAudioInfo("cam-none", "aac", false, 8000, 1, nil), ErrStreamNotActive)

	// Unknown codec.
	err := m.SetAudioInfo("cam-aud", "mp3", false, 8000, 1, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown audio codec")

	// SetAudioInfo subscribes the hub audio consumer under a fixed ID, so each
	// case gets its own camera registration.
	for i, tc := range []struct {
		codec  string
		muLaw  bool
		want   byte
		config []byte
	}{
		{codec: "aac", want: AudioCodecAAC, config: []byte{0x12, 0x10}},
		{codec: "g711", muLaw: true, want: AudioCodecG711Mu},
		{codec: "g711", muLaw: false, want: AudioCodecG711A},
		{codec: "opus", want: AudioCodecOpus},
	} {
		camID := "cam-aud-" + tc.codec + string(rune('a'+i))
		require.NoError(t, m.RegisterStream(camID, model.FormatH264, audioTestSPS, nil, nil, hub))
		require.NoError(t, m.SetAudioInfo(camID, tc.codec, tc.muLaw, 8000, 2, tc.config))

		m.mu.RLock()
		entry := m.streams[camID]
		m.mu.RUnlock()
		entry.viewerMu.Lock()
		gotCodec, gotRate, gotCh := entry.audioCodec, entry.audioSampleRate, entry.audioChannels
		gotAudioCh := entry.audioCh
		entry.viewerMu.Unlock()
		require.Equal(t, tc.want, gotCodec)
		require.Equal(t, uint32(8000), gotRate)
		require.Equal(t, uint8(2), gotCh)
		require.NotNil(t, gotAudioCh, "audio channel lazily allocated")
		m.UnregisterStream(camID)
	}

	// Config is deep-copied: mutating the caller's slice must not affect viewers.
	cfg := []byte{0x12, 0x10}
	require.NoError(t, m.SetAudioInfo("cam-aud", "aac", false, 44100, 2, cfg))
	m.mu.RLock()
	entry := m.streams["cam-aud"]
	m.mu.RUnlock()
	entry.viewerMu.Lock()
	before := append([]byte(nil), entry.audioConfig...)
	entry.viewerMu.Unlock()
	cfg[0] = 0xff
	entry.viewerMu.Lock()
	after := append([]byte(nil), entry.audioConfig...)
	entry.viewerMu.Unlock()
	require.Equal(t, before, after)
	m.UnregisterStream("cam-aud")
}

func TestDistributeAudioFrame(t *testing.T) {
	m := NewManager()
	require.NoError(t, m.RegisterStream("cam-dist", model.FormatH264, audioTestSPS, nil, nil, nil))
	require.NoError(t, m.SetAudioInfo("cam-dist", "g711", true, 8000, 1, nil))

	m.mu.RLock()
	entry := m.streams["cam-dist"]
	m.mu.RUnlock()

	regular := &viewerConn{ch: make(chan []byte, 1)}
	audioOnly := &viewerConn{ch: make(chan []byte, 1), audioOnly: true}
	full := &viewerConn{ch: make(chan []byte, 1)} // slow client: buffer already occupied
	full.ch <- []byte("stale")
	entry.viewerMu.Lock()
	entry.viewers[1] = regular
	entry.viewers[2] = audioOnly
	entry.viewers[3] = full
	entry.viewerMu.Unlock()

	m.distributeAudioFrame(entry, "cam-dist", model.AudioFrame{PTS: 42, Data: []byte{1, 2, 3}})

	// Both regular and audio-only viewers receive audio frames; the wire
	// message carries the frame type, PTS, codec byte, and payload.
	for _, v := range []*viewerConn{regular, audioOnly} {
		select {
		case msg := <-v.ch:
			require.Equal(t, MsgTypeAudioFrame, msg[0])
			require.Equal(t, []byte{1, 2, 3}, msg[14:])
		default:
			t.Fatal("viewer did not receive the audio frame")
		}
	}
	// The slow client's frame was dropped silently, not blocking distribution.
	select {
	case msg := <-full.ch:
		require.Equal(t, []byte("stale"), msg) // original occupant untouched
	default:
		t.Fatal("slow client's buffered frame unexpectedly replaced")
	}
	// Hand-built viewers lack the lifecycle fields UnregisterStream touches —
	// detach them before unregistering.
	entry.viewerMu.Lock()
	entry.viewers = make(map[int64]*viewerConn)
	entry.viewerMu.Unlock()
	m.UnregisterStream("cam-dist")
}

func TestDrainViewerCh(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(up.Close)
	wsURL := "ws" + strings.TrimPrefix(up.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	// Empty channel → returns immediately.
	ch := make(chan []byte, 4)
	drainViewerCh(ch, conn)

	// Buffered messages are flushed before returning.
	ch <- []byte("one")
	ch <- []byte("two")
	drainViewerCh(ch, conn)
	require.Len(t, ch, 0)

	// Closed channel → returns.
	close(ch)
	drainViewerCh(ch, conn)

	// Dead conn → first write fails, drain stops silently (no panic).
	dead, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	require.NoError(t, dead.Close())
	ch2 := make(chan []byte, 1)
	ch2 <- []byte("gone")
	require.NotPanics(t, func() { drainViewerCh(ch2, dead) })
	require.Len(t, ch2, 0) // message consumed by the failed write attempt
}

func TestAudioUpstreamLoop(t *testing.T) {
	m := NewManager()
	received := make(chan []byte, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No assertions in here: this goroutine outlives the test (it ends
		// when the client disconnects), and touching *testing.T after tRunner
		// returns is a failure in itself.
		_ = m.AudioUpstream(w, r, func(msg []byte) error {
			received <- append([]byte(nil), msg...)
			return nil
		})
	}))
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/audio"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	require.NoError(t, conn.WriteMessage(websocket.BinaryMessage, []byte{0, 1, 2}))
	require.Eventually(t, func() bool {
		select {
		case msg := <-received:
			require.Equal(t, []byte{0, 1, 2}, msg)
			return true
		default:
			return false
		}
	}, 5*time.Second, 20*time.Millisecond)

	// Non-WebSocket request → upgrade error surfaced to the caller (who logs at debug).
	req := httptest.NewRequest(http.MethodGet, "/audio", nil)
	rec := httptest.NewRecorder()
	require.Error(t, m.AudioUpstream(rec, req, func([]byte) error { return nil }))
}
