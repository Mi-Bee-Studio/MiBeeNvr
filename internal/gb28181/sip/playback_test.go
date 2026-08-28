package sip

import (
	"sync"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181"
	"github.com/mickeyzzc/gb28181-go/manscdp"
	"github.com/stretchr/testify/require"
)

// TestFeedRecordQueryPaged verifies SN-keyed correlation of paged RecordInfo
// responses: the query completes only once SumNum items have accumulated.
func TestFeedRecordQueryPaged(t *testing.T) {
	s := &Server{
		recordQueries: make(map[string]*pendingRecordQuery),
	}
	q := &pendingRecordQuery{sn: 42, done: make(chan struct{})}
	s.recordQueries["34020000001110000001|42"] = q

	// First page announces the total.
	s.feedRecordQuery("34020000001110000001", manscdp.RecordInfo{
		SN: 42, SumNum: 3,
		RecordList: []manscdp.RecordItem{
			{DeviceID: "ch", Name: "a", StartTime: "2026-08-01 10:00:00", EndTime: "2026-08-01 10:30:00"},
		},
	})
	select {
	case <-q.done:
		t.Fatal("query must not complete before all SumNum items arrive")
	default:
	}

	// Different SN (another query) must be ignored.
	s.feedRecordQuery("34020000001110000001", manscdp.RecordInfo{SN: 7, SumNum: 1})

	// Remaining pages complete it.
	s.feedRecordQuery("34020000001110000001", manscdp.RecordInfo{
		SN: 42, SumNum: 3,
		RecordList: []manscdp.RecordItem{
			{DeviceID: "ch", Name: "b"}, {DeviceID: "ch", Name: "c"},
		},
	})
	select {
	case <-q.done:
	default:
		t.Fatal("query should complete at SumNum items")
	}
	q.mu.Lock()
	require.Len(t, q.items, 3)
	q.mu.Unlock()

	// Extra late page after completion is harmless (append-only collector).
	s.feedRecordQuery("34020000001110000001", manscdp.RecordInfo{
		SN: 42, SumNum: 3,
		RecordList: []manscdp.RecordItem{{DeviceID: "ch", Name: "late"}},
	})
}

// TestRecordKeyFormat pins the query-map key layout.
func TestRecordKeyFormat(t *testing.T) {
	require.Equal(t, "dev|7", recordKey("dev", 7))
}

// TestPlaybackControlBodies verifies the MANSRTSP bodies built per action.
// The bodies are assembled inline in PlaybackControl; this test drives a
// minimal server with no dialog and expects an error (dialog absent), which
// exercises action validation without a live SIP stack.
func TestPlaybackControlValidation(t *testing.T) {
	s := &Server{
		recordQueries: make(map[string]*pendingRecordQuery),
		pbMu:          sync.Mutex{},
		playbacks:     make(map[string]*playbackState),
	}
	require.Error(t, s.PlaybackControl("ch", "bogus", 0, 0)) // invalid action
	require.Error(t, s.PlaybackControl("missing", "pause", 0, 0))

	s.playbacks["ch"] = &playbackState{counter: &countingSink{}}
	// pause flips the paused flag even though INFO transmission fails
	// (no dialog installed) — the state transition is what callers check.
	_ = s.PlaybackControl("ch", "pause", 0, 0)
	require.True(t, s.playbacks["ch"].paused)
}

// stopCountingSink is an AUWriter+Stopper test double.
type stopCountingSink struct{ stopped int }

func (s *stopCountingSink) WriteNALU(au [][]byte, ptsTicks int64, isIDR bool) {}
func (s *stopCountingSink) Stop() error                                       { s.stopped++; return nil }

// TestCountingSinkForwardsStop: the session teardown asserts Stopper on its
// sink — the counting wrapper must forward Stop to the recorder or a
// full-speed download (finishing inside one segment) persists nothing.
func TestCountingSinkForwardsStop(t *testing.T) {
	inner := &stopCountingSink{}
	c := &countingSink{inner: inner}
	st, ok := interface{}(c).(gb28181.Stopper)
	if !ok {
		t.Fatal("countingSink must satisfy gb28181.Stopper")
	}
	if err := st.Stop(); err != nil {
		t.Fatal(err)
	}
	if inner.stopped != 1 {
		t.Fatalf("Stop not forwarded, stopped=%d", inner.stopped)
	}
}
