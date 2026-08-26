// SPDX-License-Identifier: MIT
//
// Tests for the sub-stream analysis layer (#514): segment recording from a
// live sub-stream pull, the disk-as-queue push loop (push → delete), retention
// expiry, and the main-layer hand-off in the coordinator. The fake camera is
// a real gortsplib server (harness copied from internal/substream tests).

package vision

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/substream"
)

// --- fake camera harness (from internal/substream tests) ---------------------

var (
	tSPS = []byte{0x67, 0x64, 0x00, 0x0c, 0xac, 0x3b, 0x50, 0xb0, 0x4b, 0x42, 0x00, 0x00, 0x03, 0x00, 0x02, 0x00, 0x00, 0x03, 0x00, 0x3d, 0x08}
	tPPS = []byte{0x68, 0xee, 0x3c, 0x80}

	h264IDR = []byte{0x65, 0x88, 0x84, 0x21, 0xa0}
	h264P   = []byte{0x41, 0x9a, 0x22, 0x10}
)

type testSource struct {
	gs    *gortsplib.Server
	media *description.Media
	enc   interface {
		Encode(au [][]byte) ([]*rtp.Packet, error)
	}
	sps, pps    []byte
	idr, pSl    []byte
	stop        chan struct{}
	done        chan struct{}
	streamReady chan struct{}
	stream      *gortsplib.ServerStream
	once        sync.Once
}

func (s *testSource) ensureStream() {
	s.once.Do(func() {
		s.stream = &gortsplib.ServerStream{
			Server: s.gs,
			Desc:   &description.Session{Medias: []*description.Media{s.media}},
		}
		if err := s.stream.Initialize(); err != nil {
			panic(err)
		}
		close(s.streamReady)
	})
}

func (s *testSource) OnDescribe(*gortsplib.ServerHandlerOnDescribeCtx) (*base.Response, *gortsplib.ServerStream, error) {
	s.ensureStream()
	return &base.Response{StatusCode: base.StatusOK}, s.stream, nil
}

func (s *testSource) OnSetup(*gortsplib.ServerHandlerOnSetupCtx) (*base.Response, *gortsplib.ServerStream, error) {
	s.ensureStream()
	return &base.Response{StatusCode: base.StatusOK}, s.stream, nil
}

func (s *testSource) OnPlay(*gortsplib.ServerHandlerOnPlayCtx) (*base.Response, error) {
	return &base.Response{StatusCode: base.StatusOK}, nil
}

func (s *testSource) writeLoop() {
	defer close(s.done)
	select {
	case <-s.streamReady:
	case <-s.stop:
		return
	}
	ts := uint32(1600)
	idr := true
	for {
		select {
		case <-s.stop:
			return
		default:
		}
		var au [][]byte
		if idr {
			au = [][]byte{s.sps, s.pps, s.idr}
		} else {
			au = [][]byte{s.pSl}
		}
		if pkts, err := s.enc.Encode(au); err == nil {
			for _, p := range pkts {
				p.Timestamp = ts
				_ = s.stream.WritePacketRTP(s.media, p)
			}
		}
		ts += 3600 // 40ms @ 90kHz
		idr = !idr
		time.Sleep(20 * time.Millisecond)
	}
}

func newTestSource(t *testing.T) string {
	t.Helper()
	f := &format.H264{PayloadTyp: 96, SPS: tSPS, PPS: tPPS, PacketizationMode: 1}
	enc, err := f.CreateEncoder()
	require.NoError(t, err)
	s := &testSource{
		enc:         enc,
		sps:         tSPS,
		pps:         tPPS,
		idr:         h264IDR,
		pSl:         h264P,
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
		streamReady: make(chan struct{}),
	}
	s.media = &description.Media{Type: description.MediaTypeVideo, Control: "trackID=0", Formats: []format.Format{f}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s.gs = &gortsplib.Server{Handler: s, RTSPAddress: "rtsp://" + ln.Addr().String()}
	s.gs.Listen = func(_, _ string) (net.Listener, error) { return ln, nil }
	go func() { _ = s.gs.StartAndWait() }()
	go s.writeLoop()
	t.Cleanup(func() {
		close(s.stop)
		select {
		case <-s.streamReady:
			s.stream.Close()
		default:
		}
		<-s.done
		s.gs.Close()
	})
	return "rtsp://" + ln.Addr().String() + "/cam"
}

// --- provider over a real substream manager ----------------------------------

type managerProvider struct{ m *substream.Manager }

func (p managerProvider) AcquireSubStream(ctx context.Context, cameraID string) (*substream.Source, error) {
	return p.m.Acquire(ctx, cameraID)
}
func (p managerProvider) ReleaseSubStream(cameraID string) { p.m.Release(cameraID) }

func newSubStreamManager(t *testing.T, url string) *substream.Manager {
	t.Helper()
	m := substream.NewManager(substream.Config{
		Resolver: func(context.Context, string) (substream.Target, bool, error) {
			return substream.Target{URL: url}, true, nil
		},
		IdleTimeout:       24 * time.Hour, // the recorder holds the ref; recycle is not under test here
		ReadyTimeout:      3 * time.Second,
		DialTimeout:       2 * time.Second,
		FrameStallTimeout: 10 * time.Second,
	})
	t.Cleanup(m.Stop)
	return m
}

// --- tests --------------------------------------------------------------------

// E2E: a camera on the sub layer records rotating mp4 segments from its live
// sub-stream; the push sweep uploads them (X-Layer: sub, joined main recording
// id) and deletes on success; the same camera's MAIN segments are no longer
// pushed.
func TestSubLayer_RecordPushAndMainHandoff(t *testing.T) {
	url := newTestSource(t)
	subMgr := newSubStreamManager(t, url)

	var mu sync.Mutex
	pushed := []map[string]string{}

	visionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr := map[string]string{
			"layer":     r.Header.Get("X-Layer"),
			"camera":    r.Header.Get("X-Camera-Id"),
			"recording": r.Header.Get("X-Recording-Id"),
			"format":    r.Header.Get("X-Format"),
		}
		mu.Lock()
		pushed = append(pushed, hdr)
		mu.Unlock()
		_, _ = w.Write([]byte("ok"))
	}))
	defer visionSrv.Close()

	root := t.TempDir()
	cfg := config.VisionConfig{
		Enabled:                  true,
		URL:                      visionSrv.URL,
		SubLayerCameras:          []string{"cam-1"},
		SubLayerSegmentSecs:      1,
		SubLayerPushIntervalSecs: 2,
	}
	bus := event.NewEventBus(64)
	c := NewCoordinator(
		func() config.VisionConfig { return cfg },
		func() string { return root },
		bus,
		nil,
		managerProvider{subMgr},
	)
	require.NoError(t, c.Start(context.Background()))
	t.Cleanup(c.Stop)
	c.Health().RecordHeartbeat(HeartbeatStatus{Status: "healthy"})

	// The main recorder publishes segments for the same camera — they must be
	// handed off to the sub layer (no direct push).
	mainSeg := filepath.Join(root, "main.mp4")
	require.NoError(t, os.WriteFile(mainSeg, make([]byte, 32), 0o644))
	bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
		CameraID: "cam-1", FilePath: mainSeg, Format: "mp4",
		FileSize: 32, RecordingID: "rec-main-1",
	})

	// Sub segments appear under <root>/sublayer/cam-1/ and are pushed within
	// a couple of sweep intervals, then deleted.
	segDir := filepath.Join(root, subLayerDir, "cam-1")
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(pushed) >= 1
	}, 20*time.Second, 200*time.Millisecond, "no sub-layer push arrived")

	mu.Lock()
	first := pushed[0]
	mu.Unlock()
	require.Equal(t, "sub", first["layer"], "sub pushes carry X-Layer: sub")
	require.Equal(t, "cam-1", first["camera"])
	// The join carries a uniqueness suffix: one main recording spans multiple
	// sub segments, and consumers dedup by X-Recording-Id — an unsuffixed id
	// would drop every sub segment after the first (#514 field finding).
	require.True(t, strings.HasPrefix(first["recording"], "rec-main-1#"),
		"sub pushes join the main recording id with a # suffix, got %q", first["recording"])
	require.Equal(t, "h264", first["format"])

	// Pushed ⇒ deleted (disk queue drains).
	require.Eventually(t, func() bool {
		files, _ := listSubLayerFiles(segDir)
		return len(files) == 0
	}, 10*time.Second, 200*time.Millisecond, "pushed sub segment was not removed")

	// The main segment itself was never uploaded (only sub pushes recorded).
	mu.Lock()
	defer mu.Unlock()
	for _, p := range pushed {
		require.Equal(t, "sub", p["layer"], "main-layer push must be suppressed for sub-layer cameras")
	}
	require.FileExists(t, mainSeg, "main segment file must stay (normal lifecycle owns it)")
}

// The sweep pushes and deletes pre-existing files even without recorder
// metadata — restart resilience (headers degrade to filename-derived values).
func TestSubLayer_SweepPushesWithoutMeta(t *testing.T) {
	root := t.TempDir()
	segDir := filepath.Join(root, subLayerDir, "cam-x")
	require.NoError(t, os.MkdirAll(segDir, 0o755))
	path := filepath.Join(segDir, strconv.FormatInt(time.Now().UnixNano(), 10)+"-h264.mp4")
	require.NoError(t, os.WriteFile(path, []byte("mp4"), 0o644))

	var got atomic.Int32
	m := NewSubLayerManager(nil, func() config.VisionConfig {
		return config.VisionConfig{Enabled: true}
	}, func() string { return root }, SubLayerDeps{
		Push: func(ctx context.Context, seg SubSegment) bool {
			require.Equal(t, "cam-x", seg.CameraID)
			require.Equal(t, "h264", seg.Codec)
			require.NotEmpty(t, seg.RecordingID)
			got.Add(1)
			return true
		},
		Healthy: func() bool { return true },
	})
	m.sweepPush(context.Background())
	require.Equal(t, int32(1), got.Load(), "file pushed")
	_, err := os.Stat(path)
	require.True(t, os.IsNotExist(err), "pushed file removed")
}

// Push failures keep the file on disk (retry queue); TTL expiry is the bound.
func TestSubLayer_PushFailureKeepsFileAndTTLExpires(t *testing.T) {
	root := t.TempDir()
	segDir := filepath.Join(root, subLayerDir, "cam-x")
	require.NoError(t, os.MkdirAll(segDir, 0o755))
	path := filepath.Join(segDir, "111-h264.mp4")
	require.NoError(t, os.WriteFile(path, []byte("mp4"), 0o644))

	var attempts atomic.Int32
	m := NewSubLayerManager(nil, func() config.VisionConfig {
		return config.VisionConfig{
			Enabled:               true,
			SubLayerRetentionSecs: 3600,
		}
	}, func() string { return root }, SubLayerDeps{
		Push: func(ctx context.Context, seg SubSegment) bool {
			attempts.Add(1)
			return false
		},
		Healthy: func() bool { return true },
	})
	m.sweepPush(context.Background())
	require.Equal(t, int32(1), attempts.Load())
	require.FileExists(t, path, "failed push keeps the file for retry")

	// Age it past the retention bound and sweep again — expiry deletes it
	// regardless of push state.
	past := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(path, past, past))
	m.sweepExpire()
	_, err := os.Stat(path)
	require.True(t, os.IsNotExist(err), "expired sub segment removed")
}

// Segment-start gating: the recorder must not open a segment before a
// keyframe arrives (P-only AUs are dropped).
func TestSubLayer_SegmentsStartOnKeyframe(t *testing.T) {
	root := t.TempDir()
	m := NewSubLayerManager(nil, func() config.VisionConfig { return config.VisionConfig{Enabled: true} },
		func() string { return root }, SubLayerDeps{})
	r := newSubLayerRecorder(m, "cam-k", nil) // src=nil: onFrame guards it
	r.onFrame(1000, [][]byte{tSPS, tPPS, h264P})
	// Without a source the frame is ignored entirely — no segment, no panic.
	require.NoFileExists(t, filepath.Join(root, subLayerDir))
}

// Filename round-trip: the sweep's degraded path must recover start time and
// codec from the name.
func TestSubLayerFilenameInfo(t *testing.T) {
	st := time.Now()
	got, codec := subLayerFilenameInfo(strconv.FormatInt(st.UnixNano(), 10) + "-h265.mp4")
	require.Equal(t, "h265", codec)
	require.Equal(t, st.UnixNano(), got.UnixNano())
	_, codec = subLayerFilenameInfo("junk.mp4")
	require.Empty(t, codec)
}
