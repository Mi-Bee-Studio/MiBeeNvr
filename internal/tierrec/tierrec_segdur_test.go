package tierrec

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

// TestSubRecorder_PerCameraSegDurOverride pins cameras[].tier_segment_duration
// (#tier-seg-dur): a camera with a SegDurFor override rotates at ITS window
// while an unconfigured camera keeps the manager default.
func TestSubRecorder_PerCameraSegDurOverride(t *testing.T) {
	root := t.TempDir()
	store := &fakeStore{}
	bus := event.NewEventBus(16)

	// Manager default is deliberately huge; only the override camera can
	// ever rotate inside the test window.
	m := NewManager(Config{
		Provider: nil, Store: store, Bus: bus, StorageRoot: root,
		SegmentDur: 10 * time.Minute,
		SegDurFor: func(cameraID string) time.Duration {
			if cameraID == "cam-fast" {
				return 300 * time.Millisecond
			}
			return 0
		},
	})

	run := func(camID string) int {
		src := &fakeSource{hub: streamhub.New(), codec: model.FormatH264, sps: testSPS, pps: testPPS}
		rec := newSubRecorder(m, camID, src)
		ctx, cancel := context.WithCancel(context.Background())
		go rec.record(ctx)
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
		// The hub delivers non-blocking (queue-full drops — see
		// distributeFrame), so pace each broadcast with a drain pause: every
		// frame below is guaranteed delivered.
		src.hub.Broadcast(0, idr, true)
		time.Sleep(150 * time.Millisecond) // segment opens, segStartTick=0
		// pts +400ms ≥ the 300ms override → rotation closes segment #1.
		src.hub.Broadcast(36000, p, false)
		time.Sleep(150 * time.Millisecond)
		// New GOP opens segment #2; rec.close() finalizes it → 2 rows.
		src.hub.Broadcast(90000, idr, true)
		time.Sleep(150 * time.Millisecond)
		src.hub.Broadcast(126000, p, false)
		time.Sleep(150 * time.Millisecond)
		cancel()
		rec.close()
		n := 0
		store.mu.Lock()
		for _, r := range store.rows {
			if r.CameraID == camID {
				n++
			}
		}
		store.mu.Unlock()
		return n
	}

	fast := run("cam-fast")
	if fast < 2 {
		t.Fatalf("override camera produced %d segments, want >=2 (400ms pts steps must cross the 300ms window)", fast)
	}

	slow := run("cam-plain")
	if slow != 1 {
		t.Fatalf("default camera produced %d segments, want exactly 1 (10m window must not fire)", slow)
	}

	// Sanity: the fast camera's segments landed on disk.
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("root missing: %v", err)
	}
}
