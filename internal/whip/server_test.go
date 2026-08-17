package whip

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// --- test scaffolding ---

type testHarness struct {
	server   *Server
	router   *chi.Mux
	hub      *model.StreamHub
	resolver map[string]string // streamKey → cameraID

	mu          sync.Mutex
	onConnCalls int
	onDiscCalls int
	nalus       [][]byte // captured AUs (flattened)
	idrs        int      // IDR AUs seen
	audioFrames int
	audioCodec  string
	audioRate   int
	audioChans  int
}

func newTestHarness(t *testing.T) *testHarness {
	t.Helper()
	h := &testHarness{
		resolver: map[string]string{"test-key": "cam-whip"},
		hub:      model.NewStreamHub(),
	}
	h.server = NewServer(
		func(key string) (string, bool) {
			camID, ok := h.resolver[key]
			return camID, ok
		},
		func(cameraID string) *model.StreamHub { return h.hub },
		func(cameraID string, _ *model.StreamHub) {
			h.mu.Lock()
			h.onConnCalls++
			h.mu.Unlock()
		},
		func(cameraID string) {
			h.mu.Lock()
			h.onDiscCalls++
			h.mu.Unlock()
		},
		nil,
	)
	h.server.NALUProvider = func(cameraID string) NALUCallback {
		return func(au [][]byte, ptsTicks int64, isIDR bool) {
			h.mu.Lock()
			defer h.mu.Unlock()
			for _, nalu := range au {
				cp := append([]byte(nil), nalu...)
				h.nalus = append(h.nalus, cp)
				if len(cp) > 0 && (cp[0]&0x1F) == 5 {
					h.idrs++
				}
			}
		}
	}
	h.server.AudioFormatter = func(cameraID, codec string, sampleRate, channels int) {
		h.mu.Lock()
		h.audioCodec, h.audioRate, h.audioChans = codec, sampleRate, channels
		h.mu.Unlock()
	}
	h.server.AudioProvider = func(cameraID string) AudioCallback {
		return func(codec string, ptsTicks int64, data []byte, dur time.Duration) {
			h.mu.Lock()
			h.audioFrames++
			h.mu.Unlock()
		}
	}

	h.router = chi.NewMux()
	h.server.RegisterRoutes(h.router)
	t.Cleanup(h.server.Stop)
	return h
}

// newPusher builds a WHIP client (offerer) with H.264 video + Opus audio
// tracks — the same shape OBS/browser publishers produce.
func newPusher(t *testing.T, withAudio bool) (*webrtc.PeerConnection, *webrtc.TrackLocalStaticSample, *webrtc.TrackLocalStaticSample) {
	t.Helper()
	mediaEngine := &webrtc.MediaEngine{}
	require.NoError(t, mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeH264,
			ClockRate:   90000,
			SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f",
		},
		PayloadType: 96,
	}, webrtc.RTPCodecTypeVideo))
	if withAudio {
		require.NoError(t, mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
			},
			PayloadType: 111,
		}, webrtc.RTPCodecTypeAudio))
	}

	ir := &interceptor.Registry{}
	require.NoError(t, webrtc.RegisterDefaultInterceptors(mediaEngine, ir))
	api := webrtc.NewAPI(
		webrtc.WithMediaEngine(mediaEngine),
		webrtc.WithInterceptorRegistry(ir),
	)
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)

	videoTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeH264,
			ClockRate:   90000,
			SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f",
		},
		"video", "whip-test",
	)
	require.NoError(t, err)
	_, err = pc.AddTrack(videoTrack)
	require.NoError(t, err)

	var audioTrack *webrtc.TrackLocalStaticSample
	if withAudio {
		audioTrack, err = webrtc.NewTrackLocalStaticSample(
			webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
			"audio", "whip-test",
		)
		require.NoError(t, err)
		_, err = pc.AddTrack(audioTrack)
		require.NoError(t, err)
	}
	return pc, videoTrack, audioTrack
}

// pushOffer runs the WHIP POST exchange against the harness router.
func pushOffer(t *testing.T, h *testHarness, offerSDP []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/whip/test-key", bytes.NewReader(offerSDP))
	req.Header.Set("Content-Type", "application/sdp")
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

// buildOffer creates the client SDP offer with gathered candidates.
func buildOffer(t *testing.T, pc *webrtc.PeerConnection) []byte {
	t.Helper()
	offer, err := pc.CreateOffer(nil)
	require.NoError(t, err)
	gatherComplete := webrtc.GatheringCompletePromise(pc)
	require.NoError(t, pc.SetLocalDescription(offer))
	select {
	case <-gatherComplete:
	case <-time.After(10 * time.Second):
		t.Fatal("client ICE gathering timeout")
	}
	return []byte(pc.LocalDescription().SDP)
}

// connectPusher performs the full exchange and returns the answer recorder.
func connectPusher(t *testing.T, h *testHarness, pc *webrtc.PeerConnection) *httptest.ResponseRecorder {
	t.Helper()
	offer := buildOffer(t, pc)
	rec := pushOffer(t, h, offer)
	require.Equal(t, http.StatusCreated, rec.Code, "offer must be accepted: %s", rec.Body.String())
	require.Equal(t, "application/sdp", rec.Header().Get("Content-Type"))
	require.NotEmpty(t, rec.Header().Get("Location"))
	require.NoError(t, pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer, SDP: rec.Body.String(),
	}))
	return rec
}

// Minimal but structurally valid H.264 test payloads: a real SPS/PPS pair and
// a slice header that reads as non-IDR/IDR after Annex B assembly. The NAL
// type only matters for classification — the WHIP path forwards bytes.
var (
	testSPS    = []byte{0x67, 0x64, 0x00, 0x1f, 0xac, 0xd9, 0x40, 0x50, 0x05, 0xbb, 0x01, 0x10}
	testPPS    = []byte{0x68, 0xee, 0x3c, 0x80}
	testIDR    = []byte{0x65, 0x88, 0x84, 0x00, 0x40, 0xff, 0xfe, 0xf8, 0xc0}
	testPFrame = []byte{0x41, 0x9a, 0x02, 0x05}
)

func annexB(nalus ...[]byte) []byte {
	var buf []byte
	for _, nalu := range nalus {
		buf = append(buf, 0x00, 0x00, 0x00, 0x01)
		buf = append(buf, nalu...)
	}
	return buf
}

// --- tests ---

// End-to-end: a pion publisher (OBS-shaped: H.264+Opus sendonly) pushes
// through the WHIP exchange; the recorder callbacks receive assembled AUs
// with correct IDR classification and Opus frames.
func TestWHIPPushToEndEnd(t *testing.T) {
	h := newTestHarness(t)
	pc, videoTrack, audioTrack := newPusher(t, true)
	defer func() { _ = pc.Close() }()

	rec := connectPusher(t, h, pc)
	location := rec.Header().Get("Location")
	require.True(t, strings.HasPrefix(location, "/whip/test-key/"), "Location must be the session URL, got %q", location)

	// RTP written before ICE connects is dropped — wait for the transport.
	iceConnected := make(chan struct{}, 1)
	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		if state == webrtc.ICEConnectionStateConnected {
			select {
			case iceConnected <- struct{}{}:
			default:
			}
		}
	})
	select {
	case <-iceConnected:
	case <-time.After(10 * time.Second):
		t.Fatal("publisher ICE never connected")
	}

	// Push media like a real publisher: IDR every 10 frames (packets sent
	// before DTLS finishes are dropped, so a single leading keyframe would be
	// lost to the handshake race — periodic keyframes are what OBS/browsers
	// actually send). Keep pushing until the callbacks observe media.
	deadline := time.Now().Add(10 * time.Second)
	frame := 0
	for time.Now().Before(deadline) {
		if frame%10 == 0 {
			require.NoError(t, videoTrack.WriteSample(media.Sample{Data: annexB(testSPS, testPPS, testIDR), Duration: 33 * time.Millisecond}))
		} else {
			require.NoError(t, videoTrack.WriteSample(media.Sample{Data: annexB(testPFrame), Duration: 33 * time.Millisecond}))
		}
		require.NoError(t, audioTrack.WriteSample(media.Sample{Data: make([]byte, 120), Duration: 20 * time.Millisecond}))
		frame++

		h.mu.Lock()
		done := h.idrs >= 1 && h.audioFrames >= 3
		h.mu.Unlock()
		if done {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}

	require.Eventually(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.idrs >= 1 && h.audioFrames >= 3
	}, 2*time.Second, 50*time.Millisecond, "video AUs (IDR) and audio frames must reach the recorder callbacks")

	h.mu.Lock()
	require.Equal(t, "opus", h.audioCodec, "audio negotiation must reach AudioFormatter")
	require.Equal(t, 48000, h.audioRate)
	require.Equal(t, 2, h.audioChans)
	require.Equal(t, 1, h.onConnCalls, "onConn fires exactly once")
	h.mu.Unlock()

	// DELETE tears the session down.
	req := httptest.NewRequest(http.MethodDelete, location, nil)
	delRec := httptest.NewRecorder()
	h.router.ServeHTTP(delRec, req)
	require.Equal(t, http.StatusOK, delRec.Code)

	require.Eventually(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.onDiscCalls == 1
	}, 5*time.Second, 50*time.Millisecond, "onDisc must fire on DELETE")
	require.Equal(t, 0, h.server.activeSessions())
}

// A second publisher for the same camera is rejected with 409 while the
// first is active (single-publisher arbitration, aligned with ingest).
func TestWHIPSecondPublisherRejected(t *testing.T) {
	h := newTestHarness(t)
	pc1, _, _ := newPusher(t, false)
	defer func() { _ = pc1.Close() }()
	connectPusher(t, h, pc1)

	pc2, _, _ := newPusher(t, false)
	defer func() { _ = pc2.Close() }()
	offer := buildOffer(t, pc2)
	rec := pushOffer(t, h, offer)
	require.Equal(t, http.StatusConflict, rec.Code, "second publisher must be rejected")
}

// Unknown stream keys never create a session.
func TestWHIPUnknownStreamKey(t *testing.T) {
	h := newTestHarness(t)
	pc, _, _ := newPusher(t, false)
	defer func() { _ = pc.Close() }()
	offer := buildOffer(t, pc)

	req := httptest.NewRequest(http.MethodPost, "/whip/no-such-key", bytes.NewReader(offer))
	req.Header.Set("Content-Type", "application/sdp")
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, 0, h.server.activeSessions())
}

// Non-SDP content types are rejected before any session work.
func TestWHIPBadContentType(t *testing.T) {
	h := newTestHarness(t)
	req := httptest.NewRequest(http.MethodPost, "/whip/test-key", strings.NewReader("junk"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnsupportedMediaType, rec.Code)
}

func TestAnnexBToNALUs(t *testing.T) {
	data := annexB(testSPS, testPPS, testIDR)
	nalus := annexBToNALUs(data)
	require.Len(t, nalus, 3)
	require.Equal(t, testSPS, nalus[0])
	require.Equal(t, testPPS, nalus[1])
	require.Equal(t, testIDR, nalus[2])

	// Empty and start-code-only inputs yield nothing.
	require.Empty(t, annexBToNALUs(nil))
	require.Empty(t, annexBToNALUs([]byte{0, 0, 1}))
}

// Sessions whose publisher never sends RTP are reaped by the idle watchdog,
// freeing the camera's publisher slot.
func TestWHIPNoMediaWatchdog(t *testing.T) {
	h := newTestHarness(t)
	pc, _, _ := newPusher(t, false)
	defer func() { _ = pc.Close() }()
	connectPusher(t, h, pc)
	require.Equal(t, 1, h.server.activeSessions())

	// noMediaTimeout is 30s — too long for a unit test, so drive the same
	// code path by aging the session's lastPacket directly.
	h.mu.Lock()
	h.server.mu.Lock()
	for _, sess := range h.server.sessions {
		sess.lastPacket.Store(time.Now().Add(-2 * idleTimeout).UnixNano())
	}
	h.server.mu.Unlock()
	h.mu.Unlock()

	require.Eventually(t, func() bool {
		return h.server.activeSessions() == 0
	}, 15*time.Second, 500*time.Millisecond, "aged session must be reaped by the watchdog")
}

// context.Context compile-time usage guard for lint (used in tests above via httptest).
var _ = context.Background

// TestWHIPPushToRealRecorder runs the full live path — a pion publisher
// pushing through the WHIP exchange into a REAL IngestRecorder — and asserts
// the finalized MP4 carries an Opus track (regression: the request-context
// cancellation bug and the audio-track-at-muxer-open race both only surface
// in this wiring).
func TestWHIPPushToRealRecorder(t *testing.T) {
	store, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	db := &captureDB{}
	rec := recorder.NewIngestRecorder(recorder.IngestConfig{
		CameraID:   "cam-whip",
		Encoding:   "h264",
		SegmentDur: 10 * time.Minute,
		Store:      store,
		DB:         db,
	})
	rec.Hub = model.NewStreamHub()
	rec.Hub.SetCameraID("cam-whip")
	require.NoError(t, rec.Start(context.Background()))
	t.Cleanup(func() { _ = rec.Stop() })

	h := newTestHarness(t)
	h.server.NALUProvider = func(cameraID string) NALUCallback {
		return func(au [][]byte, ptsTicks int64, isIDR bool) {
			rec.WriteNALU(au, ptsTicks, isIDR)
		}
	}
	h.server.AudioFormatter = func(cameraID, codec string, sampleRate, channels int) {
		rec.SetAudioFormat(codec, sampleRate, channels)
	}
	h.server.AudioProvider = func(cameraID string) AudioCallback {
		return func(codec string, ptsTicks int64, data []byte, dur time.Duration) {
			rec.WriteAudio(codec, ptsTicks, data, dur)
		}
	}

	pc, videoTrack, audioTrack := newPusher(t, true)
	defer func() { _ = pc.Close() }()
	connectPusher(t, h, pc)

	iceConnected := make(chan struct{}, 1)
	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		if state == webrtc.ICEConnectionStateConnected {
			select {
			case iceConnected <- struct{}{}:
			default:
			}
		}
	})
	select {
	case <-iceConnected:
	case <-time.After(10 * time.Second):
		t.Fatal("publisher ICE never connected")
	}

	deadline := time.Now().Add(10 * time.Second)
	frame := 0
	startedAt := time.Time{}
	for time.Now().Before(deadline) {
		if frame%10 == 0 {
			require.NoError(t, videoTrack.WriteSample(media.Sample{Data: annexB(testSPS, testPPS, testIDR), Duration: 33 * time.Millisecond}))
		} else {
			require.NoError(t, videoTrack.WriteSample(media.Sample{Data: annexB(testPFrame), Duration: 33 * time.Millisecond}))
		}
		require.NoError(t, audioTrack.WriteSample(media.Sample{Data: make([]byte, 120), Duration: 20 * time.Millisecond}))
		frame++
		time.Sleep(40 * time.Millisecond)
		// Once recording starts, keep pushing for another second — the muxer
		// opens on the first IDR and audio frames after that are what land in
		// the Opus track.
		if startedAt.IsZero() && rec.Status() == model.StatusRecording {
			startedAt = time.Now()
		}
		if !startedAt.IsZero() && time.Since(startedAt) > time.Second {
			break
		}
	}

	require.NoError(t, rec.Stop())
	segs, err := filepath.Glob(filepath.Join(store.RootDir(), "cam-whip", "*", "*", "*", "*.mp4"))
	require.NoError(t, err)
	require.NotEmpty(t, segs, "expected a finalized recording")
	data, err := os.ReadFile(segs[0])
	require.NoError(t, err)
	require.Contains(t, string(data), "dOps", "MP4 must carry an Opus track (audio recorded)")
}

type captureDB struct {
	recordings []*model.Recording
}

func (d *captureDB) InsertRecording(_ context.Context, r *model.Recording) error {
	d.recordings = append(d.recordings, r)
	return nil
}

func (d *captureDB) InsertRecordingWithRetry(_ context.Context, r *model.Recording, _ int, _ time.Duration) error {
	d.recordings = append(d.recordings, r)
	return nil
}
func (d *captureDB) SetMergeStatus(_ context.Context, _ []string, _ string) error { return nil }
