package peer

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDCEgressEnqueueStallFailsAfterBudget pins the 2026-10-14 wedge fix:
// when the peer stops draining (BufferedAmount pinned above the watermark,
// lane over cap), a parked enqueue must fail after dcEgressParkBudget —
// not park forever. Forever-parking hung Tunnel.Close() on its pipe
// goroutines and froze the NVR's Manager until SIGKILL.
func TestDCEgressEnqueueStallFailsAfterBudget(t *testing.T) {
	f := &fakeEgressDC{}
	// Pin BufferedAmount above the admission watermark: run() never admits.
	f.setBA(uint64(dcEgressLowWater) + 1)
	tr := newSchedTransport(f)

	orig := dcEgressParkBudget
	dcEgressParkBudget = 100 * time.Millisecond
	t.Cleanup(func() { dcEgressParkBudget = orig })

	// Pre-fill the data lane past its cap (first frame is always admitted).
	require.NoError(t, tr.Send(make([]byte, dcEgressDataCap+1)))

	done := make(chan error, 1)
	go func() { done <- tr.Send([]byte("parks-then-fails")) }()

	select {
	case err := <-done:
		require.Error(t, err, "stalled enqueue must fail after the budget")
		assert.Contains(t, err.Error(), "egress stalled")
	case <-time.After(15 * time.Second):
		t.Fatal("enqueue parked forever — stall budget not enforced")
	}

	// Terminal failure: later enqueues fail fast instead of re-parking.
	assert.Error(t, tr.Send([]byte("x")))
}
