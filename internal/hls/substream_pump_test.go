// SPDX-License-Identifier: MIT
//
// Sub-stream pump coverage: readSubStream/readSubStreamH264/readSubStreamH265
// driven end-to-end by a real local RTSP server (same fake-camera pattern as
// internal/substream's tests), plus the StreamHub broadcast path through
// SubscribeToHub → WriteH264 → writeLoop → muxer, the hub rebind machinery,
// and Handle's playlist proxy.

package hls

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

// Parameter-set vectors borrowed from the substream package tests — real
// camera captures, structurally valid for SDP parsing and the HLS muxer.
var (
	pumpSPS    = []byte{0x67, 0x64, 0x00, 0x0c, 0xac, 0x3b, 0x50, 0xb0, 0x4b, 0x42, 0x00, 0x00, 0x03, 0x00, 0x02, 0x00, 0x00, 0x03, 0x00, 0x3d, 0x08}
	pumpPPS    = []byte{0x68, 0xee, 0x3c, 0x80}
	pumpVPS    = []byte{0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x78, 0x95, 0x98, 0x09}
	pumpSPS265 = []byte{0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x90, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x78, 0xa0, 0x03, 0xc0, 0x80, 0x10, 0xe5, 0x96, 0x66, 0x69, 0x24, 0xca, 0xe0, 0x10, 0x00, 0x00, 0x03, 0x00, 0x10, 0x00, 0x00, 0x03, 0x01, 0xe0, 0x80}
	pumpPPS265 = []byte{0x44, 0x01, 0xc1, 0x72, 0xb4, 0x62, 0x40}

	pumpH264IDR = []byte{0x65, 0x88, 0x84, 0x21, 0xa0}
	pumpH264P   = []byte{0x41, 0x9a, 0x22, 0x10}
	// H265 IDR (nal type 19) and P (TRAIL_R) slices whose PAYLOAD (byte 2, after
	// the 2-byte NAL header) starts with first_slice_segment_in_pic_flag=1 —
	// gohlslib's DTS extractor rejects slice segments without it.
	pumpH265IDR = []byte{0x26, 0x01, 0xaf, 0x08, 0x52}
	pumpH265P   = []byte{0x02, 0x3a, 0xf1, 0xd4}
)

// rtspSource is a local RTSP server playing one video track in a loop
// (adapted from internal/substream/substream_test.go's testSource).
type rtspSource struct {
	gs    *gortsplib.Server
	media *description.Media
	enc   interface {
		Encode(au [][]byte) ([]*rtp.Packet, error)
	}
	idr, pFrame   []byte
	vps, sps, pps []byte
	stop          chan struct{}
	done          chan struct{}
	streamReady   chan struct{}
	stream        *gortsplib.ServerStream
	once          sync.Once
}

func (s *rtspSource) ensureStream() {
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

func (s *rtspSource) OnDescribe(*gortsplib.ServerHandlerOnDescribeCtx) (*base.Response, *gortsplib.ServerStream, error) {
	s.ensureStream()
	return &base.Response{StatusCode: base.StatusOK}, s.stream, nil
}

func (s *rtspSource) OnSetup(*gortsplib.ServerHandlerOnSetupCtx) (*base.Response, *gortsplib.ServerStream, error) {
	s.ensureStream()
	return &base.Response{StatusCode: base.StatusOK}, s.stream, nil
}

func (s *rtspSource) OnPlay(*gortsplib.ServerHandlerOnPlayCtx) (*base.Response, error) {
	return &base.Response{StatusCode: base.StatusOK}, nil
}

func (s *rtspSource) writeLoop() {
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
			if s.vps != nil {
				au = [][]byte{s.vps, s.sps, s.pps, s.idr}
			} else {
				au = [][]byte{s.sps, s.pps, s.idr}
			}
		} else {
			au = [][]byte{s.pFrame}
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

// newRTSPSource starts an H.264 or H.265 source on a random local port.
func newRTSPSource(t *testing.T, isH265 bool) string {
	t.Helper()
	s := &rtspSource{
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
		streamReady: make(chan struct{}),
	}
	if isH265 {
		f := &format.H265{PayloadTyp: 96, VPS: pumpVPS, SPS: pumpSPS265, PPS: pumpPPS265}
		enc, err := f.CreateEncoder()
		require.NoError(t, err)
		s.enc, s.idr, s.pFrame, s.vps, s.sps, s.pps = enc, pumpH265IDR, pumpH265P, pumpVPS, pumpSPS265, pumpPPS265
		s.media = &description.Media{
			Type:    description.MediaTypeVideo,
			Control: "trackID=0",
			Formats: []format.Format{f},
		}
	} else {
		f := &format.H264{PayloadTyp: 96, SPS: pumpSPS, PPS: pumpPPS, PacketizationMode: 1}
		enc, err := f.CreateEncoder()
		require.NoError(t, err)
		s.enc, s.idr, s.pFrame, s.sps, s.pps = enc, pumpH264IDR, pumpH264P, pumpSPS, pumpPPS
		s.media = &description.Media{
			Type:    description.MediaTypeVideo,
			Control: "trackID=0",
			Formats: []format.Format{f},
		}
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s.gs = &gortsplib.Server{
		Handler:     s,
		RTSPAddress: "rtsp://" + ln.Addr().String(),
	}
	s.gs.Listen = func(_, _ string) (net.Listener, error) { return ln, nil }
	// No t.Logf from this goroutine — it can outlive the test (data race on T).
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

// segmentFiles returns the non-playlist media files written under the stream's
// segment directory — the observable end-state of the write pump.
func segmentFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || e.Name() == "index.m3u8" || filepath.Ext(e.Name()) == ".m3u8" {
			continue
		}
		files = append(files, e.Name())
	}
	return files
}

func TestSubStreamPumpH264(t *testing.T) {
	m := NewManagerWithOpts(context.Background(), t.TempDir(), 64, 1<<20, 3)
	t.Cleanup(m.StopAll)

	require.NoError(t, m.StartStream("cam-h264", pumpSPS, pumpPPS, 0))
	url := newRTSPSource(t, false)

	require.NoError(t, m.StartSubStreamReader("cam-h264", url, false, nil))

	m.mu.RLock()
	dir := m.streams["cam-h264"].dirPath
	m.mu.RUnlock()
	require.Eventually(t, func() bool { return len(segmentFiles(t, dir)) > 0 },
		15*time.Second, 200*time.Millisecond, "H264 sub-stream never produced segments")

	// Second StartSubStreamReader while running is a no-op success (dedup).
	require.NoError(t, m.StartSubStreamReader("cam-h264", url, false, nil))
}

func TestSubStreamPumpH265(t *testing.T) {
	m := NewManagerWithOpts(context.Background(), t.TempDir(), 64, 1<<20, 3)
	t.Cleanup(m.StopAll)

	require.NoError(t, m.StartStreamH265("cam-h265", pumpVPS, pumpSPS265, pumpPPS265, 0))
	url := newRTSPSource(t, true)

	require.NoError(t, m.StartSubStreamReader("cam-h265", url, true, nil))

	m.mu.RLock()
	dir := m.streams["cam-h265"].dirPath
	m.mu.RUnlock()
	require.Eventually(t, func() bool { return len(segmentFiles(t, dir)) > 0 },
		15*time.Second, 200*time.Millisecond, "H265 sub-stream never produced segments")
}

func TestSubStreamCodecMismatchFallsBack(t *testing.T) {
	for _, tc := range []struct {
		name      string
		entryH265 bool
		srcH265   bool
	}{
		{name: "H265 entry, H264 source", entryH265: true, srcH265: false},
		{name: "H264 entry, H265 source", entryH265: false, srcH265: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManagerWithOpts(context.Background(), t.TempDir(), 64, 1<<20, 3)
			t.Cleanup(m.StopAll)

			if tc.entryH265 {
				require.NoError(t, m.StartStreamH265("cam-mix", pumpVPS, pumpSPS265, pumpPPS265, 0))
			} else {
				require.NoError(t, m.StartStream("cam-mix", pumpSPS, pumpPPS, 0))
			}

			fallback := make(chan struct{}, 1)
			url := newRTSPSource(t, tc.srcH265)
			require.NoError(t, m.StartSubStreamReader("cam-mix", url, tc.entryH265, func() {
				select {
				case fallback <- struct{}{}:
				default:
				}
			}))

			require.Eventually(t, func() bool {
				select {
				case <-fallback:
					return true
				default:
					return false
				}
			}, 10*time.Second, 100*time.Millisecond, "codec mismatch never triggered fallback")
		})
	}
}

func TestSubStreamInvalidURLFallsBack(t *testing.T) {
	m := NewManagerWithOpts(context.Background(), t.TempDir(), 64, 1<<20, 3)
	t.Cleanup(m.StopAll)
	require.NoError(t, m.StartStream("cam-bad", pumpSPS, pumpPPS, 0))

	fallback := make(chan struct{}, 1)
	require.NoError(t, m.StartSubStreamReader("cam-bad", "not-a-url", false, func() {
		fallback <- struct{}{}
	}))
	require.Eventually(t, func() bool {
		select {
		case <-fallback:
			return true
		default:
			return false
		}
	}, 5*time.Second, 50*time.Millisecond, "invalid URL never triggered fallback")
}

// TestHubPumpAndRebind drives SubscribeToHub → hub.Broadcast → WriteH264 →
// writeLoop → muxer with synthetic IDR/P frames, then exercises the rebind
// machinery the sub-stream recycler (#513) relies on.
func TestHubPumpAndRebind(t *testing.T) {
	mm := metrics.NewMetrics()
	m := NewManagerWithOpts(context.Background(), t.TempDir(), 64, 1<<20, 3, mm)
	t.Cleanup(m.StopAll)

	require.NoError(t, m.StartStream("cam-hub", pumpSPS, pumpPPS, 0))
	m.mu.RLock()
	dir := m.streams["cam-hub"].dirPath
	m.mu.RUnlock()

	hub1 := streamhub.New()
	require.NoError(t, m.SubscribeToHub("cam-hub", hub1, false))
	require.Equal(t, hub1, m.ActiveHub("cam-hub"))
	require.Nil(t, m.ActiveHub("cam-nope"))

	// Broadcast a few IDR+P pairs — enough for the muxer to cut a segment.
	for i := range 60 {
		hub1.Broadcast(int64(90000*i), [][]byte{pumpSPS, pumpPPS, pumpH264IDR}, true)
		hub1.Broadcast(int64(90000*i+3000), [][]byte{pumpH264P}, false)
	}
	require.Eventually(t, func() bool { return len(segmentFiles(t, dir)) > 0 },
		15*time.Second, 200*time.Millisecond, "hub broadcast never produced segments")

	// Rebind to a fresh hub: old hub's consumer is removed, ActiveHub moves.
	hub2 := streamhub.New()
	m.RebindHub("cam-hub", hub2, false)
	require.Equal(t, hub2, m.ActiveHub("cam-hub"))

	// Idempotent / guarded rebinds.
	m.RebindHub("cam-hub", hub2, false)  // same hub → no-op
	m.RebindHub("cam-hub", nil, false)   // nil hub → no-op
	m.RebindHub("cam-nope", hub2, false) // unknown camera → no-op
	require.Equal(t, hub2, m.ActiveHub("cam-hub"))

	// Broadcast still flows after rebind.
	for i := range 30 {
		hub2.Broadcast(int64(90000*(100+i)), [][]byte{pumpSPS, pumpPPS, pumpH264IDR}, true)
	}

	// With segments on disk, Handle proxies the playlist to HTTP clients.
	require.Eventually(t, func() bool {
		rec := httptest.NewRecorder()
		if !m.Handle("cam-hub", rec, httptest.NewRequest(http.MethodGet, "/hls/cam-hub/index.m3u8", nil)) {
			return false
		}
		return rec.Body.Len() > 0
	}, 15*time.Second, 200*time.Millisecond, "Handle never served the playlist")
}

func TestHandleProxiesPlaylist(t *testing.T) {
	mm := metrics.NewMetrics()
	m := NewManagerWithOpts(context.Background(), t.TempDir(), 64, 1<<20, 3, mm)
	t.Cleanup(m.StopAll)

	// Unknown camera → false, caller answers 404.
	rec := httptest.NewRecorder()
	require.False(t, m.Handle("cam-unknown", rec, httptest.NewRequest(http.MethodGet, "/hls/cam-unknown/index.m3u8", nil)))

	require.NoError(t, m.StartStream("cam-pl", pumpSPS, pumpPPS, 0))

	// Destroyed muxer (write-error recovery window) → false, not a panic.
	m.mu.RLock()
	entry := m.streams["cam-pl"]
	m.mu.RUnlock()
	entry.mu.Lock()
	entry.mux = nil
	entry.mu.Unlock()
	rec2 := httptest.NewRecorder()
	require.False(t, m.Handle("cam-pl", rec2, httptest.NewRequest(http.MethodGet, "/hls/cam-pl/index.m3u8", nil)))
}

func TestCodecFor(t *testing.T) {
	m := NewManagerWithOpts(context.Background(), t.TempDir(), 64, 1<<20, 3)
	t.Cleanup(m.StopAll)

	_, ok := m.CodecFor("cam-none")
	require.False(t, ok)

	require.NoError(t, m.StartStream("cam-264", pumpSPS, pumpPPS, 0))
	codec, ok := m.CodecFor("cam-264")
	require.True(t, ok)
	require.Equal(t, model.FormatH264, codec)

	require.NoError(t, m.StartStreamH265("cam-265", pumpVPS, pumpSPS265, pumpPPS265, 0))
	codec, ok = m.CodecFor("cam-265")
	require.True(t, ok)
	require.Equal(t, model.FormatH265, codec)
}
