package vision

// Tests for dropmarker.go (#671): interpreting the consumer's batched drop
// report and marking the affected recordings ai_status='skipped'.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeMarkerDB struct {
	byIDsCalls  []idCall
	rangeCalls  []rangeCall
	idsResult   int64
	rangeResult int64
}

type idCall struct {
	ids   []string
	aiErr string
}

type rangeCall struct {
	cameraID string
	from, to time.Time
	aiErr    string
}

func (f *fakeMarkerDB) MarkRecordingsSkippedByIDs(ctx context.Context, ids []string, aiErr string) (int64, error) {
	f.byIDsCalls = append(f.byIDsCalls, idCall{ids: append([]string(nil), ids...), aiErr: aiErr})
	return f.idsResult, nil
}

func (f *fakeMarkerDB) MarkRecordingsSkippedByRange(ctx context.Context, cameraID string, from, to time.Time, aiErr string) (int64, error) {
	f.rangeCalls = append(f.rangeCalls, rangeCall{cameraID: cameraID, from: from, to: to, aiErr: aiErr})
	return f.rangeResult, nil
}

func TestStripSubLayerSuffix(t *testing.T) {
	require.Equal(t, "1786611799700038099", stripSubLayerSuffix("1786611799700038099#1756742400000000000"))
	require.Equal(t, "plain", stripSubLayerSuffix("plain"))
	require.Equal(t, "", stripSubLayerSuffix(""))
	// Suffix stripping only applies to the sub-layer "#<nano>" join; an id
	// containing '#' earlier in the string is untouched.
	require.Equal(t, "a#b#c", stripSubLayerSuffix("a#b#c"))
}

func TestApplyDropsPreciseIDs(t *testing.T) {
	fake := &fakeMarkerDB{idsResult: 2}
	drops := &VisionDrops{Seq: 1, Ranges: []VisionDropRange{{
		CameraID: "cam-1", Reason: "queue_full", Count: 2,
		From: "2026-09-02T04:00:01Z", To: "2026-09-02T04:01:00Z",
		IDs: []string{"rec-1", "1786...#1756742400000000000", "rec-1"}, // dup + sub-layer join
	}}}
	marked := ApplyDrops(context.Background(), fake, drops)
	require.Equal(t, int64(2), marked)
	require.Len(t, fake.byIDsCalls, 1)
	require.Equal(t, []string{"rec-1", "1786..."}, fake.byIDsCalls[0].ids, "dup removed, #nano stripped")
	require.Equal(t, "vision drop:queue_full", fake.byIDsCalls[0].aiErr)
	require.Empty(t, fake.rangeCalls, "precise ids win over the time-window fallback")
}

func TestApplyDropsRangeFallback(t *testing.T) {
	fake := &fakeMarkerDB{rangeResult: 37}
	drops := &VisionDrops{Seq: 2, Ranges: []VisionDropRange{{
		CameraID: "cam-2", Reason: "ttl_expired", Count: 37,
		From: "2026-09-02T04:00:01Z", To: "2026-09-02T04:31:20Z",
	}}}
	marked := ApplyDrops(context.Background(), fake, drops)
	require.Equal(t, int64(37), marked)
	require.Len(t, fake.rangeCalls, 1)
	call := fake.rangeCalls[0]
	require.Equal(t, "cam-2", call.cameraID)
	require.Equal(t, "vision drop:ttl_expired", call.aiErr)
	wantFrom, _ := time.Parse(time.RFC3339, "2026-09-02T04:00:01Z")
	wantTo, _ := time.Parse(time.RFC3339, "2026-09-02T04:31:20Z")
	require.Equal(t, wantFrom, call.from)
	require.Equal(t, wantTo, call.to)
}

func TestApplyDropsSanitizesReason(t *testing.T) {
	fake := &fakeMarkerDB{idsResult: 1}
	drops := &VisionDrops{Ranges: []VisionDropRange{{
		CameraID: "cam-1", Reason: "Queue<FULL>; DROP TABLE", Count: 1,
		From: "2026-09-02T04:00:01Z", To: "2026-09-02T04:00:30Z",
		IDs: []string{"rec-x"},
	}}}
	ApplyDrops(context.Background(), fake, drops)
	// Leading run of [a-z0-9_] only — injected characters can't ride along
	// into ai_error.
	require.Equal(t, "vision drop:queue", fake.byIDsCalls[0].aiErr)
}

func TestApplyDropsSkipsMalformedRanges(t *testing.T) {
	fake := &fakeMarkerDB{}
	drops := &VisionDrops{Ranges: []VisionDropRange{
		{
			CameraID: "cam-1", Reason: "queue_full", Count: 1,
			From: "not-a-time", To: "also-not", IDs: []string{"rec-1"},
		},
		{
			CameraID: "", Reason: "queue_full", Count: 1,
			From: "2026-09-02T04:00:01Z", To: "2026-09-02T04:00:30Z",
		}, // no camera, no ids
	}}
	marked := ApplyDrops(context.Background(), fake, drops)
	// rec-1 still marked precisely (ids present, times unparseable);
	// the camera-less range with bad semantics is skipped entirely.
	require.Equal(t, int64(0), marked)
	require.Len(t, fake.byIDsCalls, 1)
	require.Empty(t, fake.rangeCalls)
}

func TestApplyDropsNilSafe(t *testing.T) {
	require.Zero(t, ApplyDrops(context.Background(), &fakeMarkerDB{}, nil))
	require.Zero(t, ApplyDrops(context.Background(), &fakeMarkerDB{}, &VisionDrops{}))
}
