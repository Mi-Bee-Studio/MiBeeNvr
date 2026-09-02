package tierrec

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

// fakeSource wraps a real StreamHub with canned codec params.
type fakeSource struct {
	hub   *streamhub.StreamHub
	codec model.Format
	sps   []byte
	pps   []byte
}

func (f *fakeSource) Hub() *streamhub.StreamHub { return f.hub }
func (f *fakeSource) CodecParams() (model.Format, []byte, []byte, []byte) {
	return f.codec, f.sps, f.pps, nil
}
func (f *fakeSource) State() string { return "live" }

// fakeStore records inserted rows.
type fakeStore struct {
	mu   sync.Mutex
	rows []*model.Recording
}

func (s *fakeStore) InsertRecording(_ context.Context, r *model.Recording) error {
	s.mu.Lock()
	s.rows = append(s.rows, r)
	s.mu.Unlock()
	return nil
}

var (
	testSPS = []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	testPPS = []byte{0x68, 0xce, 0x3c, 0x80}
)

// TestSubRecorder_WritesLayerOneSegments: an H.264 sub-stream (IDR + P
// frames) produces a layer=1 recording row, a file on disk under the
// camera's tree with the sub_ prefix, and a SegmentCompleted event carrying
// Layer=1.
func TestSubRecorder_WritesLayerOneSegments(t *testing.T) {
	root := t.TempDir()
	store := &fakeStore{}
	bus := event.NewEventBus(16)
	var evMu sync.Mutex
	var got []event.SegmentCompleted
	subCh := make(chan event.Event, 4)
	if err := bus.Subscribe(event.TopicSegmentCompleted, subCh, 4); err != nil {
		t.Fatal(err)
	}

	src := &fakeSource{hub: streamhub.New(), codec: model.FormatH264, sps: testSPS, pps: testPPS}
	m := NewManager(Config{Provider: nil, Store: store, Bus: bus, StorageRoot: root, SegmentDur: 500 * time.Millisecond})

	rec := newSubRecorder(m, "cam-sub", src)
	ctx, cancel := context.WithCancel(context.Background())
	go rec.record(ctx)
	// Let the subscribe land before the first broadcast (hub drops frames
	// for unknown consumers).
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		rec.mu.Lock()
		subbed := rec.subID != ""
		rec.mu.Unlock()
		if subbed {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	idr := [][]byte{{0x65, 1, 2, 3}, {0x01, 0x02}}
	p := [][]byte{{0x41, 1, 2, 3}}
	tick := int64(0)
	src.hub.Broadcast(tick, idr, true)
	for i := 1; i <= 30; i++ {
		tick += 3000 // 30ms @ 90kHz
		src.hub.Broadcast(tick, p, false)
		time.Sleep(8 * time.Millisecond)
	}
	cancel()
	rec.close()

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.rows) != 1 {
		t.Fatalf("expected 1 layer-1 row, got %d", len(store.rows))
	}
	r := store.rows[0]
	if r.Layer != model.LayerSub {
		t.Fatalf("row layer = %d, want %d", r.Layer, model.LayerSub)
	}
	if r.FrameCount == 0 || r.CameraID != "cam-sub" {
		t.Fatalf("bad row: frames=%d cam=%s", r.FrameCount, r.CameraID)
	}
	abs := filepath.Join(root, r.FilePath)
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("segment file missing: %v", err)
	}
	if filepath.Base(r.FilePath)[:4] != "sub_" {
		t.Fatalf("segment file %q must carry the sub_ prefix", filepath.Base(r.FilePath))
	}

	// Event carries the layer.
	timeout := time.After(time.Second)
	for {
		select {
		case e := <-subCh:
			if sc, ok := e.Data.(event.SegmentCompleted); ok && sc.Layer == model.LayerSub && sc.CameraID == "cam-sub" {
				evMu.Lock()
				got = append(got, sc)
				evMu.Unlock()
				return
			}
		case <-timeout:
			t.Fatalf("SegmentCompleted with Layer=1 never arrived (got %v)", got)
		}
	}
}
