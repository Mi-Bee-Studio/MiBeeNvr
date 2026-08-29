package recorder

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// ingestTestDB is a minimal RecordingDB that records inserted recordings in-memory.
type ingestTestDB struct {
	recordings []*model.Recording
}

func (d *ingestTestDB) InsertRecording(_ context.Context, r *model.Recording) error {
	d.recordings = append(d.recordings, r)
	return nil
}

func (d *ingestTestDB) InsertRecordingWithRetry(_ context.Context, r *model.Recording, _ int, _ time.Duration) error {
	d.recordings = append(d.recordings, r)
	return nil
}

func (d *ingestTestDB) SetMergeStatus(_ context.Context, _ []string, _ string) error {
	return nil
}

// newIngestRecorder builds an IngestRecorder backed by a temp-dir storage
// manager and an in-memory RecordingDB. The hub is wired the way the camera
// manager wires it (NewStreamHub + SetCameraID).
func newIngestRecorder(t *testing.T, segDur time.Duration) (*IngestRecorder, *storage.Manager, *ingestTestDB) {
	t.Helper()
	store, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	db := &ingestTestDB{}
	rec := NewIngestRecorder(IngestConfig{
		CameraID:   "push-cam",
		Encoding:   "h264",
		SegmentDur: segDur,
		Store:      store,
		DB:         db,
	})
	rec.Hub = model.NewStreamHub()
	rec.Hub.SetCameraID("push-cam")
	require.NoError(t, rec.Start(context.Background()))
	return rec, store, db
}

// camFiles returns all files under the camera's recording dir for assertions.
func camFiles(t *testing.T, store *storage.Manager, cameraID string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(store.RootDir(), cameraID, "*"))
	require.NoError(t, err)
	return matches
}

// TestIngestRecorder_StartIdle verifies the recorder starts in a waiting state
// and does not dial out (no segments created until a publisher pushes frames).
func TestIngestRecorder_StartIdle(t *testing.T) {
	rec, store, db := newIngestRecorder(t, 10*time.Minute)
	t.Cleanup(func() { _ = rec.Stop() })

	// Idle: no segments, no recordings, status not yet "recording".
	require.Equal(t, model.StatusReconnecting, rec.Status())
	require.Empty(t, db.recordings)
	files := camFiles(t, store, "push-cam")
	require.Empty(t, files)
}

// TestIngestRecorder_WriteNALU_RecordsSegment feeds a sequence of SPS/PPS/IDR
// + P-frame NALUs and verifies a recording row is produced and a file lands on
// disk once the segment is closed.
func TestIngestRecorder_WriteNALU_RecordsSegment(t *testing.T) {
	rec, store, db := newIngestRecorder(t, 10*time.Minute)
	t.Cleanup(func() { _ = rec.Stop() })

	// First access unit: SPS + PPS + IDR. Held by the AU assembler until the
	// next picture starts (one-picture assembly lag by design).
	rec.WriteNALU([][]byte{testSPS, testPPS, testIDR}, 0, true)

	// A few non-IDR VCL frames. The first flushes the pending IDR picture,
	// opening a segment + writing the IDR.
	for i := 1; i <= 5; i++ {
		rec.WriteNALU([][]byte{testPFrame}, int64(i)*90, false)
	}
	require.Equal(t, model.StatusRecording, rec.Status())

	// Stop closes the in-flight segment → one recording row + one file.
	require.NoError(t, rec.Stop())
	require.Len(t, db.recordings, 1, "expected one recording row after stop")
	require.Equal(t, "push-cam", db.recordings[0].CameraID)
	require.Equal(t, model.FormatH264, db.recordings[0].Format)
	require.Greater(t, db.recordings[0].FrameCount, 0)

	files := camFiles(t, store, "push-cam")
	require.Len(t, files, 1, "expected one finalized mp4 on disk")
	info, err := os.Stat(files[0])
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))
}

// TestIngestRecorder_OnDisconnect_ClosesSegment verifies that a publisher
// disconnect flushes the in-flight segment and returns the recorder to the
// waiting state, ready for the next publisher.
func TestIngestRecorder_OnDisconnect_ClosesSegment(t *testing.T) {
	rec, store, db := newIngestRecorder(t, 10*time.Minute)
	t.Cleanup(func() { _ = rec.Stop() })

	rec.WriteConnected()
	rec.WriteNALU([][]byte{testSPS, testPPS, testIDR}, 0, true)
	// Flush the pending IDR picture (one-picture assembly lag).
	rec.WriteNALU([][]byte{testPFrame}, 90, false)
	require.Equal(t, model.StatusRecording, rec.Status())

	rec.OnDisconnect()
	require.Equal(t, model.StatusReconnecting, rec.Status(), "disconnect returns recorder to waiting")
	require.Len(t, db.recordings, 1, "in-flight segment flushed on disconnect")

	files := camFiles(t, store, "push-cam")
	require.Len(t, files, 1)
}

// TestIngestRecorder_SPSChange_RollsSegment verifies that an SPS change closes
// the current segment before starting a new one (avcC must be self-consistent).
func TestIngestRecorder_SPSChange_RollsSegment(t *testing.T) {
	rec, _, db := newIngestRecorder(t, 10*time.Minute)
	t.Cleanup(func() { _ = rec.Stop() })

	rec.WriteNALU([][]byte{testSPS, testPPS, testIDR}, 0, true)
	// Change SPS + new IDR → should close the first segment, open a second.
	rec.WriteNALU([][]byte{testSPS2, testPPS, testIDR}, 90, true)

	require.NoError(t, rec.Stop())
	require.Len(t, db.recordings, 2, "expected two segments after SPS change")
}

// TestIngestRecorder_IgnoresNonIDRBeforeKeyframe verifies no MP4 segment is
// written until an IDR arrives (prevents black-frame P-frame-first segments).
// The recorder still flips to "recording" (a publisher is streaming) and
// broadcasts the frame to live consumers, but the recording side waits for IDR.
func TestIngestRecorder_IgnoresNonIDRBeforeKeyframe(t *testing.T) {
	rec, store, db := newIngestRecorder(t, 10*time.Minute)
	t.Cleanup(func() { _ = rec.Stop() })

	// P-frames only (no SPS/PPS/IDR) → once the first picture completes (the
	// second delivery flushes the first; one-picture assembly lag), status
	// flips to recording (live pusher active) but NO segment is opened (no
	// SPS/PPS, not IDR).
	rec.WriteNALU([][]byte{testPFrame}, 0, false)
	rec.WriteNALU([][]byte{testPFrame}, 90, false)
	require.Equal(t, model.StatusRecording, rec.Status(), "publisher is live, but no segment yet")
	require.Empty(t, db.recordings, "no recording row until IDR opens a segment")

	files := camFiles(t, store, "push-cam")
	require.Empty(t, files, "no file written until IDR")
}

// testPFrame is a minimal H.264 non-IDR slice NAL (type 1).
var testPFrame = []byte{0x41, 0x9a, 0x10, 0x00}

// TestIngestRecorder_GroupsPerSliceDeliveries reproduces the fnOS live-push
// topology failure (2026-08-20): a restreamer publisher emits one NAL unit per
// RTMP message, so libx264 sliced-threads frames (1080p = 4 slices) arrived as
// four separate "AUs" and every downstream consumer showed black video. The
// recorder must regroup them into ONE picture-complete hub broadcast, keyed on
// the assembled picture (IDR), keeping the publisher's inline param sets
// without a duplicate cached prepend.
func TestIngestRecorder_GroupsPerSliceDeliveries(t *testing.T) {
	rec, _, _ := newIngestRecorder(t, 10*time.Minute)
	t.Cleanup(func() { _ = rec.Stop() })

	type hubFrame struct {
		pts int64
		au  [][]byte
	}
	ch := make(chan hubFrame, 4)
	require.NoError(t, rec.Hub.Subscribe("test-grouping", func(pts int64, au [][]byte) {
		ch <- hubFrame{pts, au}
	}))

	// IDR picture split across four deliveries: header param sets + first
	// slice (first_mb_in_slice==0 → testIDR's 0x88), then three continuation
	// slices (first_mb_in_slice!=0 → first header byte 0x00, i.e. ue(v) with
	// leading zero bits).
	s2 := []byte{0x65, 0x08, 0x84, 0x00, 0x10}
	s3 := []byte{0x65, 0x08, 0x84, 0x00, 0x11}
	s4 := []byte{0x65, 0x08, 0x84, 0x00, 0x12}

	rec.WriteNALU([][]byte{testSPS, testPPS, testIDR}, 0, true)
	rec.WriteNALU([][]byte{s2}, 30, true)
	rec.WriteNALU([][]byte{s3}, 60, true)
	rec.WriteNALU([][]byte{s4}, 90, true)

	// Nothing may surface before the picture completes.
	select {
	case f := <-ch:
		t.Fatalf("partial picture leaked to the hub: %+v", f)
	default:
	}

	// The next picture's first slice flushes the grouped IDR picture.
	rec.WriteNALU([][]byte{testPFrame}, 3000, false)

	select {
	case f := <-ch:
		require.Equal(t, int64(0), f.pts, "picture PTS comes from its first VCL delivery")
		require.Equal(t, [][]byte{testSPS, testPPS, testIDR, s2, s3, s4}, f.au,
			"one picture-complete AU with all four slices and no duplicate param prepend")
	case <-time.After(2 * time.Second):
		t.Fatal("grouped IDR picture never reached the hub")
	}
}

// TestIngestRecorder_ConcurrentLockNarrowing verifies that WriteNALU, Status,
// and Stop can be called concurrently without data races.
// WriteNALU holds r.mu only for shared state access, not for muxer writes
// or hub broadcasts.
func TestIngestRecorder_ConcurrentLockNarrowing(t *testing.T) {
	rec, _, db := newIngestRecorder(t, 10*time.Minute)

	var wg sync.WaitGroup

	// Writer goroutine — feeds frames continuously.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 500 {
			rec.WriteNALU([][]byte{testSPS, testPPS, testIDR}, int64(i)*90, true)
			rec.WriteNALU([][]byte{testPFrame}, int64(i)*90+45, false)
			time.Sleep(time.Microsecond)
		}
	}()

	// Reader goroutines — call Status(), SPS(), PPS() concurrently.
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				_ = rec.Status()
				_ = rec.SPS()
				_ = rec.PPS()
			}
		}()
	}

	wg.Wait()

	// Stop — verifies no race with the last writes.
	require.NoError(t, rec.Stop())
	_ = db
}

// TestIngestRecorder_Audio verifies the WHIP audio path (#369): negotiated
// Opus format + WriteAudio frames land in the MP4 (audio track present) and
// on the hub for live consumers; audio info accessors reflect the negotiation.
func TestIngestRecorder_Audio(t *testing.T) {
	rec, store, db := newIngestRecorder(t, 10*time.Minute)
	t.Cleanup(func() { _ = rec.Stop() })

	// No audio until negotiated.
	require.Empty(t, rec.AudioCodec())
	require.Equal(t, 0, rec.AudioSampleRate())

	// WHIP-style negotiation, then a publisher session with video + audio.
	rec.SetAudioFormat("opus", 48000, 2)
	require.Equal(t, "opus", rec.AudioCodec())
	require.Equal(t, 48000, rec.AudioSampleRate())
	require.Equal(t, 2, rec.AudioChannels())

	// Subscribe to the hub's audio stream to observe the live fan-out.
	var audioMu sync.Mutex
	audioFrames := 0
	require.NoError(t, rec.Hub.SubscribeAudio("test-audio", func(pts int64, codec model.AudioCodec, data []byte) {
		audioMu.Lock()
		audioFrames++
		audioMu.Unlock()
	}))

	rec.WriteConnected()
	rec.WriteNALU([][]byte{testSPS, testPPS, testIDR}, 0, true)
	for i := 1; i <= 5; i++ {
		rec.WriteNALU([][]byte{testPFrame}, int64(i)*90, false)
		rec.WriteAudio("opus", int64(i)*960, []byte{0x11, 0x22, 0x33, 0x44}, 20*time.Millisecond)
	}
	// Non-opus audio is ignored.
	rec.WriteAudio("g711", 0, []byte{0x55}, time.Millisecond)

	require.NoError(t, rec.Stop())
	require.Len(t, db.recordings, 1)

	audioMu.Lock()
	require.Equal(t, 5, audioFrames, "audio frames must reach the hub")
	audioMu.Unlock()

	// The finalized MP4 must contain an Opus track — probe the raw bytes for
	// the sample entry box (dOps) rather than pulling a full MP4 parser here.
	// Segments land under cameraID/<date>/.
	segs, err := filepath.Glob(filepath.Join(store.RootDir(), "push-cam", "*", "*", "*", "*.mp4"))
	require.NoError(t, err)
	require.Len(t, segs, 1, "expected one finalized mp4, got %v", segs)
	data, err := os.ReadFile(segs[0])
	require.NoError(t, err)
	require.Contains(t, string(data), "Opus", "MP4 must carry an Opus sample entry")
	require.Contains(t, string(data), "dOps", "MP4 must carry a dOps box")
}

// TestIngestRecorder_AudioLiveOnly verifies the RecordEnabled=false gate:
// audio still reaches the hub but nothing is written to disk.
func TestIngestRecorder_AudioLiveOnly(t *testing.T) {
	store, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	db := &ingestTestDB{}
	liveOnly := false
	rec := NewIngestRecorder(IngestConfig{
		CameraID:      "push-live",
		Encoding:      "h264",
		SegmentDur:    10 * time.Minute,
		Store:         store,
		DB:            db,
		RecordEnabled: &liveOnly,
	})
	rec.Hub = model.NewStreamHub()
	rec.Hub.SetCameraID("push-live")
	require.NoError(t, rec.Start(context.Background()))
	t.Cleanup(func() { _ = rec.Stop() })

	rec.SetAudioFormat("opus", 48000, 2)
	rec.WriteNALU([][]byte{testSPS, testPPS, testIDR}, 0, true)
	rec.WriteAudio("opus", 960, []byte{0x11}, 20*time.Millisecond)
	require.NoError(t, rec.Stop())

	require.Empty(t, db.recordings, "live-only mode must not record")
	files, err := filepath.Glob(filepath.Join(store.RootDir(), "push-live", "*"))
	require.NoError(t, err)
	require.Empty(t, files)
}

// TestIngestRecorder_SetAudioFormatLockedIn verifies renegotiation cannot
// swap the format once set (the muxer track would be inconsistent).
func TestIngestRecorder_SetAudioFormatLockedIn(t *testing.T) {
	rec, _, _ := newIngestRecorder(t, 10*time.Minute)
	t.Cleanup(func() { _ = rec.Stop() })

	rec.SetAudioFormat("opus", 48000, 2)
	rec.SetAudioFormat("opus", 16000, 1)
	require.Equal(t, 48000, rec.AudioSampleRate(), "first negotiation wins")
	require.Equal(t, 2, rec.AudioChannels())

	// Non-opus is rejected outright.
	other, _, _ := newIngestRecorder(t, 10*time.Minute)
	t.Cleanup(func() { _ = other.Stop() })
	other.SetAudioFormat("g711", 8000, 1)
	require.Empty(t, other.AudioCodec())
}

// TestIngestRecorder_H265_RecordsSegment verifies the H.265 push-ingest path
// (#433, enhanced-RTMP hvc1): the VPS/SPS/PPS triple is cached, the segment is
// created as an H.265 track, IDR broadcasts carry the param-set triple
// in-band, and the recording row is format h265.
func TestIngestRecorder_H265_RecordsSegment(t *testing.T) {
	store, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	db := &ingestTestDB{}
	rec := NewIngestRecorder(IngestConfig{
		CameraID:   "push-cam-h265",
		Encoding:   "h265",
		SegmentDur: 10 * time.Minute,
		Store:      store,
		DB:         db,
	})
	rec.Hub = model.NewStreamHub()
	rec.Hub.SetCameraID("push-cam-h265")
	require.NoError(t, rec.Start(context.Background()))
	t.Cleanup(func() { _ = rec.Stop() })

	// Out-of-band param-set feed first (mirrors the RTMP sequence-header feed
	// in internal/rtmp/server.go), then the IDR picture, then P frames.
	rec.WriteNALU([][]byte{testVPS265, testSPS265, testPPS265}, 0, true)
	pFrame265 := []byte{0x02, 0x01, 0xaf, 0x09, 0x40, 0xc0, 0x00, 0x10} // TRAIL_R slice (NAL type 1)
	rec.WriteNALU([][]byte{testIDR265}, 90, true)
	for i := 2; i <= 6; i++ {
		rec.WriteNALU([][]byte{pFrame265}, int64(i)*90, false)
	}
	require.Equal(t, model.StatusRecording, rec.Status())

	// CodecParams must report the H.265 format with the full triple.
	f, sps, pps, vps := rec.CodecParams()
	require.Equal(t, model.FormatH265, f)
	require.NotNil(t, sps)
	require.NotNil(t, pps)
	require.NotNil(t, vps)

	// A subscriber sees IDR AUs carrying the VPS/SPS/PPS triple in-band.
	var gotIDR bool
	done := make(chan struct{})
	subID := "ingest-h265-test"
	require.NoError(t, rec.Hub.Subscribe(subID, func(_ int64, au [][]byte) {
		if !gotIDR {
			gotIDR = true
			v, s, p := extractTripleForTest(au)
			require.NotNil(t, v)
			require.NotNil(t, s)
			require.NotNil(t, p)
			close(done)
		}
	}))
	defer rec.Hub.Unsubscribe(subID)
	// Push another IDR after subscribing so the hub delivers a fresh keyframe.
	rec.WriteNALU([][]byte{testIDR265}, 700, true)
	rec.WriteNALU([][]byte{pFrame265}, 800, false)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("no IDR broadcast observed within timeout")
	}
	require.True(t, gotIDR)

	require.NoError(t, rec.Stop())
	require.Len(t, db.recordings, 1)
	require.Equal(t, model.FormatH265, db.recordings[0].Format)
	files := camFiles(t, store, "push-cam-h265")
	require.NotEmpty(t, files)
}

// extractTripleForTest pulls VPS/SPS/PPS out of a broadcast AU (test helper).
func extractTripleForTest(au [][]byte) (vps, sps, pps []byte) {
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}
		switch (nalu[0] >> 1) & 0x3F {
		case 32:
			vps = nalu
		case 33:
			sps = nalu
		case 34:
			pps = nalu
		}
	}
	return
}
