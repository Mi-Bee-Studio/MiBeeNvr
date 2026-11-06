package vision

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/stretchr/testify/require"
)

// foldAwareRepusher extends fakeRepusher with merge-lineage lookup, mirroring
// *storage.DB's optional capability (empty map → ID unknown → not folded).
type foldAwareRepusher struct {
	fakeRepusher
	lineage map[string]string
}

func (f *foldAwareRepusher) FindLineageTarget(ctx context.Context, sourceID string) (string, error) {
	return f.lineage[sourceID], nil
}

// TestUploadSegment_FoldedSourceSkipsClean pins the #46 push race: rolling
// merge can fold (and delete) a source file between the completion event and
// the open. With lineage proving the fold, the push counts as handled — the
// merged bucket row carries the window — instead of feeding the breaker
// failures. Without lineage the old Warn+failure behavior stands (genuine
// storage trouble must stay visible).
func TestUploadSegment_FoldedSourceSkipsClean(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "gone.mp4")
	require.NoError(t, os.WriteFile(missing, []byte("x"), 0o644))
	require.NoError(t, os.Remove(missing), "test needs a path that does not exist")

	hdr := map[string]string{"X-Recording-Id": "fold-src", "X-Camera-Id": "cam-1"}

	t.Run("lineage proves the fold", func(t *testing.T) {
		t.Parallel()
		db := &foldAwareRepusher{lineage: map[string]string{"fold-src": "fold-bucket"}}
		c := NewCoordinator(
			func() config.VisionConfig { return config.VisionConfig{Enabled: true, URL: "http://127.0.0.1:1"} },
			func() string { return dir },
			event.NewEventBus(8),
			db, nil,
		)
		// The Vision server must never be contacted — the segment is skipped
		// before any request is built (URL points at a dead port on purpose).
		ok := c.uploadSegment(context.Background(), "http://127.0.0.1:1", missing, 1, hdr)
		require.True(t, ok, "folded source must count as handled")
	})

	t.Run("no lineage keeps the failure", func(t *testing.T) {
		t.Parallel()
		db := &foldAwareRepusher{lineage: map[string]string{}}
		c := NewCoordinator(
			func() config.VisionConfig { return config.VisionConfig{Enabled: true, URL: "http://127.0.0.1:1"} },
			func() string { return dir },
			event.NewEventBus(8),
			db, nil,
		)
		ok := c.uploadSegment(context.Background(), "http://127.0.0.1:1", missing, 1, hdr)
		require.False(t, ok, "missing file without provenance is a real failure")
	})

	t.Run("legacy repusher without lineage stays on the warn path", func(t *testing.T) {
		t.Parallel()
		c := NewCoordinator(
			func() config.VisionConfig { return config.VisionConfig{Enabled: true, URL: "http://127.0.0.1:1"} },
			func() string { return dir },
			event.NewEventBus(8),
			&fakeRepusher{}, nil,
		)
		ok := c.uploadSegment(context.Background(), "http://127.0.0.1:1", missing, 1, hdr)
		require.False(t, ok)
	})
}

// TestUploadSegment_LiveSegmentUnaffected: with the file present the folded
// path never triggers — guard against a regression that would silently eat
// real uploads.
func TestUploadSegment_LiveSegmentUnaffected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "live.mp4")
	require.NoError(t, os.WriteFile(path, []byte("payload"), 0o644))

	db := &foldAwareRepusher{lineage: map[string]string{"live": "fold-bucket"}}
	c := NewCoordinator(
		func() config.VisionConfig { return config.VisionConfig{Enabled: true, URL: srv.URL} },
		func() string { return dir },
		event.NewEventBus(8),
		db, nil,
	)
	ok := c.uploadSegment(context.Background(), srv.URL, path, int64(len("payload")),
		map[string]string{"X-Recording-Id": "live", "X-Camera-Id": "cam-1"})
	require.True(t, ok)
}
