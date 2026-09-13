package recorder

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/avi"
)

// #761: MJPEG recording defaults to single-file AVI segments (one file per
// segment) instead of one JPEG file per frame. The legacy directory form
// remains available as an explicit opt-out (Form: "dir").

// TestMJPEGRecorder_VideoOnlyAVIDefault asserts a video-only MJPEG camera
// records single .avi files per segment with NO explicit form setting — the
// container is the default shape, and every sent frame is demuxable from it.
func TestMJPEGRecorder_VideoOnlyAVIDefault(t *testing.T) {
	t.Helper()
	srv := newMjpegTestServer(t)
	defer srv.close()

	mgr := newTestManager(t)
	rec := NewMJPEGRecorder(MJPEGConfig{
		CameraID:   "cam-mjpeg-avi-default",
		RTSPURL:    srv.rtspURL,
		SegmentDur: 5 * time.Minute,
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, rec.Start(ctx))

	srv.waitPlay(t, 5*time.Second)
	time.Sleep(300 * time.Millisecond)
	srv.sendFrames(5, 30*time.Millisecond)
	time.Sleep(300 * time.Millisecond)

	require.NoError(t, rec.Stop())

	files, err := mgr.ListSegments("cam-mjpeg-avi-default")
	require.NoError(t, err)
	require.NotEmpty(t, files, "expected at least one recorded segment")
	for _, f := range files {
		info, err := os.Stat(f)
		require.NoError(t, err)
		require.False(t, info.IsDir(), "default form must be a single AVI file, got directory %s", f)
		require.Equal(t, ".avi", filepath.Ext(f), "segment %s should be a .avi file", f)

		fh, err := os.Open(f)
		require.NoError(t, err)
		d, err := avi.NewDemuxer(fh)
		require.NoError(t, err)
		frames := 0
		for {
			c, err := d.NextChunk()
			if err != nil {
				break
			}
			if c.Type == avi.ChunkVideo {
				frames++
			}
		}
		fh.Close()
		require.Greater(t, frames, 0, "segment %s should demux at least one video frame", f)
	}
}

// TestMJPEGRecorder_DirFormOptOut asserts the legacy per-frame directory form
// still works when explicitly requested (escape hatch for the default flip).
func TestMJPEGRecorder_DirFormOptOut(t *testing.T) {
	t.Helper()
	srv := newMjpegTestServer(t)
	defer srv.close()

	mgr := newTestManager(t)
	rec := NewMJPEGRecorder(MJPEGConfig{
		CameraID:   "cam-mjpeg-dir-optout",
		RTSPURL:    srv.rtspURL,
		SegmentDur: 5 * time.Minute,
		Form:       MJPEGFormDir,
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, rec.Start(ctx))

	srv.waitPlay(t, 5*time.Second)
	time.Sleep(300 * time.Millisecond)
	srv.sendFrames(5, 30*time.Millisecond)
	time.Sleep(300 * time.Millisecond)

	require.NoError(t, rec.Stop())

	files, err := mgr.ListSegments("cam-mjpeg-dir-optout")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, f := range files {
		info, err := os.Stat(f)
		require.NoError(t, err)
		require.True(t, info.IsDir(), "dir form opt-out must keep directory segments, got file %s", f)
		require.Greater(t, countJPGFiles(t, f), 0)
	}
}

// TestMJPEGRecorder_AVIRotateOnSize asserts a segment rotates early when the
// AVI file approaches the RIFF uint32 size ceiling — with the guard lowered to
// a tiny value, several frames must split across multiple segment files long
// before SegmentDur elapses.
func TestMJPEGRecorder_AVIRotateOnSize(t *testing.T) {
	t.Helper()
	orig := aviRotateBytes
	aviRotateBytes = 1024 // smaller than one 16x16 test JPEG chunk
	t.Cleanup(func() { aviRotateBytes = orig })

	srv := newMjpegTestServer(t)
	defer srv.close()

	mgr := newTestManager(t)
	rec := NewMJPEGRecorder(MJPEGConfig{
		CameraID:   "cam-mjpeg-avi-rotate",
		RTSPURL:    srv.rtspURL,
		SegmentDur: 5 * time.Minute, // duration alone must NOT rotate here
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, rec.Start(ctx))

	srv.waitPlay(t, 5*time.Second)
	time.Sleep(300 * time.Millisecond)
	srv.sendFrames(5, 30*time.Millisecond)

	require.Eventually(t, func() bool {
		files, err := mgr.ListSegments("cam-mjpeg-avi-rotate")
		return err == nil && len(files) >= 2
	}, 5*time.Second, 100*time.Millisecond, "size guard should rotate segments before SegmentDur")

	require.NoError(t, rec.Stop())
}
