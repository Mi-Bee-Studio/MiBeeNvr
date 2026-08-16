package cascade

import (
	"context"
	"encoding/binary"
	"net"
	"path/filepath"
	"testing"
	"time"

	gb "github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181/psmux"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
	"github.com/stretchr/testify/require"
)

// TestAvccToAnnexB covers the MP4 sample → Annex B conversion.
func TestAvccToAnnexB(t *testing.T) {
	avcc := []byte{}
	var raw []byte
	for _, nalu := range [][]byte{{0x67, 0x42}, {0x65, 0x88, 0x84}} {
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(nalu)))
		avcc = append(avcc, lenBuf[:]...)
		avcc = append(avcc, nalu...)
		raw = append(raw, 0, 0, 0, 1)
		raw = append(raw, nalu...)
	}
	got := avccToAnnexB(nil, avcc)
	require.Equal(t, raw, got)

	// Malformed length prefix truncates instead of panicking.
	require.NotPanics(t, func() {
		_ = avccToAnnexB(nil, []byte{0xFF, 0xFF, 0xFF, 0xFF, 0x01})
	})
}

// TestParseMANSRTSP covers the SIP INFO control bodies the platform sends.
func TestParseMANSRTSP(t *testing.T) {
	m := parseMANSRTSP("PAUSE MANSRTSP/1.0\r\nCSeq: 7\r\nRange: npt=now-\r\n\r\n")
	require.Equal(t, "PAUSE", m.method)
	require.False(t, m.hasNPT)

	m = parseMANSRTSP("PLAY MANSRTSP/1.0\r\nCSeq: 8\r\nScale: 4.00\r\nRange: npt=0.000-\r\n\r\n")
	require.Equal(t, "PLAY", m.method)
	require.Equal(t, 4.0, m.scale)
	require.False(t, m.hasNPT, "npt=0 is the platform's plain-resume form")

	m = parseMANSRTSP("PLAY MANSRTSP/1.0\r\nCSeq: 9\r\nScale: 1.00\r\nRange: npt=12.500-\r\n\r\n")
	require.True(t, m.hasNPT)
	require.Equal(t, 12.5, m.npt)

	require.Empty(t, parseMANSRTSP("TEARDOWN RTSP/1.0\r\n\r\n").method)
	require.Empty(t, parseMANSRTSP("").method)
}

// createPlaybackSegment writes a real fMP4 with 5 samples (IDR P P IDR P,
// 33ms each) and inserts its Recording row.
func createPlaybackSegment(t *testing.T, db interface {
	InsertRecording(ctx context.Context, r *model.Recording) error
}, cameraID string, start time.Time,
) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "seg.mp4")

	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	m := muxer.NewMP4Muxer(path)
	trackID, err := m.AddH264Track(sps, pps)
	require.NoError(t, err)
	idr := []byte{0x65, 0x88, 0x84, 0x00, 0x01}
	p := []byte{0x41, 0x9A, 0x33}
	for i := range 5 {
		nalu := p
		if i == 0 || i == 3 {
			nalu = idr
		}
		require.NoError(t, m.WriteSample(trackID, nalu, time.Duration(i)*33*time.Millisecond, 33*time.Millisecond))
	}
	require.NoError(t, m.Close())

	require.NoError(t, db.InsertRecording(context.Background(), &model.Recording{
		ID:        "rec-pb-1",
		CameraID:  cameraID,
		FilePath:  path,
		Format:    model.FormatH264,
		StartedAt: start,
		EndedAt:   start.Add(5 * 33 * time.Millisecond),
		Duration:  0.165,
	}))
}

// newPlaybackHarness builds a service + session streaming to a local UDP
// receiver; returns the session and a function collecting complete RTP/PS
// access units (marker-bit terminated).
func newPlaybackHarness(t *testing.T) (*playbackSession, func() [][]byte) {
	t.Helper()
	db := newCascadeTestDB(t)
	svc := New(testCfg(), fakeSource{cams: []CameraInfo{{ID: "front", Name: "Front"}}}, db)

	rx, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rx.Close() })
	dst := rx.LocalAddr().(*net.UDPAddr)
	conn, err := net.ListenUDP("udp", nil)
	require.NoError(t, err)

	ps := &playbackSession{
		svc: svc, callID: "test-call", channel: "34020000001320000001", camera: "front",
		conn: conn, dst: dst, ssrc: 0x10000009,
		mux:  psmux.New(),
		rtp:  psmux.NewRTPPacketizer(conn, dst, 0x10000009, 7),
		ctrl: make(chan pbCtrl, 8),
		done: make(chan struct{}),
	}

	collect := func() [][]byte {
		var aus [][]byte
		var cur []byte
		buf := make([]byte, 65536)
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			_ = rx.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			n, err := rx.Read(buf)
			if err != nil {
				if len(aus) > 0 {
					break // idle after data — stream ended
				}
				continue
			}
			pkt := buf[:n]
			cur = append(cur, pkt[12:]...)
			if pkt[1]&0x80 != 0 {
				aus = append(aus, cur)
				cur = nil
			}
		}
		return aus
	}
	return ps, collect
}

// TestPlaybackStreamsRecordings round-trips a playback pass: local fMP4 →
// psmux/RTP → platform-side PSDemuxer must reproduce every frame with
// parameter sets ahead of each IDR.
func TestPlaybackStreamsRecordings(t *testing.T) {
	ps, collect := newPlaybackHarness(t)
	start := time.Now().Add(-time.Minute)
	createPlaybackSegment(t, ps.svc.db, "front", start)
	ps.start = start
	ps.end = start.Add(time.Minute)

	done, seek, err := ps.playOnce(0)
	require.NoError(t, err)
	require.True(t, done)
	require.Nil(t, seek)
	require.False(t, ps.closed.Load())
	ps.finish("test end", false)

	aus := collect()
	require.Len(t, aus, 5, "one PS AU per frame")

	d := gb.NewPSDemuxer()
	var nalus [][]byte
	for i, au := range aus {
		ns, err := d.FeedAU(au, int64(i)*2970, true)
		require.NoError(t, err)
		nalus = append(nalus, ns...)
	}
	// Frame 0: SPS+PPS+IDR (3 NALUs); frames 1-2: P; frame 3: SPS+PPS+IDR;
	// frame 4: P.
	require.Len(t, nalus, 9)
	require.Equal(t, byte(0x67), nalus[0][0])
	require.Equal(t, byte(0x68), nalus[1][0])
	require.Equal(t, byte(0x65), nalus[2][0])
	require.Equal(t, byte(0x41), nalus[3][0])
	require.Equal(t, byte(0x67), nalus[5][0], "second IDR re-carries SPS")
}

// TestPlaybackSeekSkipsToKeyframe seeks 100ms in: only the second GOP
// (IDR at 99ms + its P) may be delivered.
func TestPlaybackSeekSkipsToKeyframe(t *testing.T) {
	ps, collect := newPlaybackHarness(t)
	start := time.Now().Add(-time.Minute)
	createPlaybackSegment(t, ps.svc.db, "front", start)
	ps.start = start
	ps.end = start.Add(time.Minute)

	done, _, err := ps.playOnce(0.050)
	require.NoError(t, err)
	require.True(t, done)
	ps.finish("test end", false)

	aus := collect()
	require.Len(t, aus, 2, "seek lands on the second GOP only")

	d := gb.NewPSDemuxer()
	var first []byte
	for i, au := range aus {
		ns, err := d.FeedAU(au, int64(i)*2970, true)
		require.NoError(t, err)
		if i == 0 {
			first = ns[0]
		}
	}
	require.Equal(t, byte(0x67), first[0], "delivery restarts with SPS at the IDR")
}

// TestPlaybackPauseResume verifies a paused stream freezes (no data) and
// resumes to deliver the remaining frames.
func TestPlaybackPauseResume(t *testing.T) {
	ps, collect := newPlaybackHarness(t)
	start := time.Now().Add(-time.Minute)
	createPlaybackSegment(t, ps.svc.db, "front", start)
	ps.start = start
	ps.end = start.Add(time.Minute)

	ps.postCtrl(pbCtrl{action: "pause"})
	go func() {
		time.Sleep(250 * time.Millisecond)
		ps.postCtrl(pbCtrl{action: "resume"})
	}()
	t0 := time.Now()
	done, _, err := ps.playOnce(0)
	require.NoError(t, err)
	require.True(t, done)
	require.GreaterOrEqual(t, time.Since(t0), 250*time.Millisecond, "pause holds the stream")
	ps.finish("test end", false)

	aus := collect()
	require.Len(t, aus, 5)
}

// TestPlaybackMANSRTSPHeuristic mirrors onInfo's routing table: PAUSE pauses,
// PLAY npt=0 resumes, PLAY npt>1 seeks.
func TestPlaybackMANSRTSPHeuristic(t *testing.T) {
	ps, _ := newPlaybackHarness(t)
	require.Equal(t, "pause", pbActionFor(parseMANSRTSP("PAUSE MANSRTSP/1.0\r\nRange: npt=now-\r\n\r\n")))
	require.Equal(t, "resume", pbActionFor(parseMANSRTSP("PLAY MANSRTSP/1.0\r\nScale: 2.00\r\nRange: npt=0.000-\r\n\r\n")))
	require.Equal(t, "seek", pbActionFor(parseMANSRTSP("PLAY MANSRTSP/1.0\r\nRange: npt=30.000-\r\n\r\n")))
	require.Empty(t, pbActionFor(parseMANSRTSP("BOGUS MANSRTSP/1.0\r\n\r\n")))
	require.NoError(t, ps.conn.Close())
}
