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

	// First access unit: SPS + PPS + IDR. Should open a segment + write the IDR.
	rec.WriteNALU([][]byte{testSPS, testPPS, testIDR}, 0, true)
	require.Equal(t, model.StatusRecording, rec.Status())

	// A few non-IDR VCL frames.
	for i := 1; i <= 5; i++ {
		rec.WriteNALU([][]byte{testPFrame}, int64(i)*90, false)
	}

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

	// P-frame only (no SPS/PPS/IDR) → status flips to recording (live pusher
	// active) but NO segment is opened (no SPS/PPS, not IDR).
	rec.WriteNALU([][]byte{testPFrame}, 0, false)
	require.Equal(t, model.StatusRecording, rec.Status(), "publisher is live, but no segment yet")
	require.Empty(t, db.recordings, "no recording row until IDR opens a segment")

	files := camFiles(t, store, "push-cam")
	require.Empty(t, files, "no file written until IDR")
}

// testPFrame is a minimal H.264 non-IDR slice NAL (type 1).
var testPFrame = []byte{0x41, 0x9a, 0x10, 0x00}

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
