package peer

import (
	"errors"
	"sync"
	"testing"
	"time"
)

var errBoom = errors.New("boom")

// fakeEgressDC is a scriptable DataChannel for scheduler tests: sends are
// recorded in departure order and BufferedAmount is test-controlled (the
// knob the scheduler gates admission on).
type fakeEgressDC struct {
	mu   sync.Mutex
	sent [][]byte
	ba   uint64
	err  error
}

func (f *fakeEgressDC) send(data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, data)
	return nil
}

func (f *fakeEgressDC) setBA(v uint64) {
	f.mu.Lock()
	f.ba = v
	f.mu.Unlock()
}

func (f *fakeEgressDC) setErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

func (f *fakeEgressDC) snapshot() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.sent...)
}

func newSchedTransport(f *fakeEgressDC) Transport {
	return NewDataChannelTransport(DataChannel{
		Label: "test",
		Send:  f.send,
		Close: func() error { return nil },
		OnMessage: func(func([]byte)) {
		},
		BufferedAmount: func() uint64 {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.ba
		},
	})
}

// waitFor polls cond until true or the deadline passes.
func waitFor(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}

// A high-priority frame enqueued after queued data frames must depart
// first once SCTP admits traffic again — the whole point of the scheduler.
func TestDCEgressStrictPriority(t *testing.T) {
	f := &fakeEgressDC{}
	tr := newSchedTransport(f)
	f.setBA(1 << 20) // saturate: scheduler must admit nothing

	if err := tr.Send([]byte("data-1")); err != nil {
		t.Fatalf("send data-1: %v", err)
	}
	if err := tr.Send([]byte("data-2")); err != nil {
		t.Fatalf("send data-2: %v", err)
	}
	if err := SendPriority(tr, []byte("high-1")); err != nil {
		t.Fatalf("send high-1: %v", err)
	}

	time.Sleep(30 * time.Millisecond)
	if got := len(f.snapshot()); got != 0 {
		t.Fatalf("low-water gate failed: %d frames departed while saturated", got)
	}

	f.setBA(0)
	if !waitFor(t, time.Second, func() bool { return len(f.snapshot()) == 3 }) {
		t.Fatalf("drain incomplete: %v", f.snapshot())
	}

	got := f.snapshot()
	want := []byte("high-1")
	if string(got[0]) != string(want) {
		t.Fatalf("strict priority violated: first departure %q, want %q (all: %v)",
			got[0], want, got)
	}
}

// While BufferedAmount sits above the watermark nothing departs; the moment
// it drops below, queued frames flow.
func TestDCEgressLowWaterGate(t *testing.T) {
	f := &fakeEgressDC{}
	tr := newSchedTransport(f)
	f.setBA(1 << 20)

	_ = tr.Send([]byte("held"))

	time.Sleep(30 * time.Millisecond)
	if got := len(f.snapshot()); got != 0 {
		t.Fatalf("frame departed while above watermark: %v", f.snapshot())
	}

	f.setBA(0)
	if !waitFor(t, time.Second, func() bool { return len(f.snapshot()) == 1 }) {
		t.Fatalf("frame never departed after drain: %v", f.snapshot())
	}
}

// The data lane's byte cap turns Send into backpressure: once exceeded the
// caller blocks until the writer drains. The high lane cap does the same
// for SendPriority.
func TestDCEgressBackpressureBlocksAndResumes(t *testing.T) {
	origData, origHigh := dcEgressDataCap, dcEgressHighCap
	dcEgressDataCap, dcEgressHighCap = 100, 100
	defer func() { dcEgressDataCap, dcEgressHighCap = origData, origHigh }()

	f := &fakeEgressDC{}
	tr := newSchedTransport(f)
	f.setBA(1 << 20) // hold the writer so the queue builds

	if err := tr.Send(make([]byte, 60)); err != nil {
		t.Fatalf("first send: %v", err)
	}

	blocked := make(chan error, 1)
	go func() { blocked <- tr.Send(make([]byte, 60)) }()

	select {
	case err := <-blocked:
		t.Fatalf("Send should block on lane cap, returned %v", err)
	case <-time.After(80 * time.Millisecond):
	}

	f.setBA(0) // writer drains both, cap frees
	select {
	case err := <-blocked:
		if err != nil {
			t.Fatalf("blocked send resumed with error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked Send never resumed after drain")
	}
	if !waitFor(t, time.Second, func() bool { return len(f.snapshot()) == 2 }) {
		t.Fatalf("both frames not delivered: %v", f.snapshot())
	}
}

// A transport whose DataChannel cannot observe BufferedAmount must behave
// exactly as before: direct synchronous Send, and SendPriority delegating
// to the optional dc.SendPriority hook (relay path).
func TestDCEgressPassthroughWithoutBufferedAmount(t *testing.T) {
	var mu sync.Mutex
	var normal, high [][]byte
	f := &fakeEgressDC{}

	tr := NewDataChannelTransport(DataChannel{
		Label: "relay-ish",
		Send: func(data []byte) error {
			mu.Lock()
			normal = append(normal, data)
			mu.Unlock()
			return f.send(data)
		},
		SendPriority: func(data []byte) error {
			mu.Lock()
			high = append(high, data)
			mu.Unlock()
			return nil
		},
		Close: func() error { return nil },
		OnMessage: func(func([]byte)) {
		},
	})

	if err := tr.Send([]byte("n")); err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := SendPriority(tr, []byte("h")); err != nil {
		t.Fatalf("send priority: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(normal) != 1 || string(normal[0]) != "n" {
		t.Fatalf("normal passthrough wrong: %v", normal)
	}
	if len(high) != 1 || string(high[0]) != "h" {
		t.Fatalf("priority hook not used: %v", high)
	}
}

// Close must unblock any caller parked on lane capacity with an error.
func TestDCEgressCloseUnblocksSenders(t *testing.T) {
	origData := dcEgressDataCap
	dcEgressDataCap = 100
	defer func() { dcEgressDataCap = origData }()

	f := &fakeEgressDC{}
	tr := newSchedTransport(f)
	f.setBA(1 << 20)

	if err := tr.Send(make([]byte, 60)); err != nil {
		t.Fatalf("first send: %v", err)
	}

	blocked := make(chan error, 1)
	go func() { blocked <- tr.Send(make([]byte, 60)) }()
	time.Sleep(30 * time.Millisecond)

	_ = tr.Close()
	select {
	case err := <-blocked:
		if err == nil {
			t.Fatal("blocked Send should fail after Close")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked Send never returned after Close")
	}
}

// A failing underlying Send parks the scheduler permanently and surfaces
// the error to subsequent callers instead of silently swallowing frames.
func TestDCEgressSendErrorPropagates(t *testing.T) {
	f := &fakeEgressDC{}
	tr := newSchedTransport(f)

	if err := tr.Send([]byte("ok")); err != nil {
		t.Fatalf("send: %v", err)
	}
	if !waitFor(t, time.Second, func() bool { return len(f.snapshot()) == 1 }) {
		t.Fatal("first frame never departed")
	}

	f.setErr(errBoom)
	// Failure is asynchronous: this frame enqueues, the writer hits the
	// error on it, and every subsequent Send must observe the dead state.
	_ = tr.Send([]byte("bad"))
	if !waitFor(t, time.Second, func() bool { return tr.Send([]byte("probe")) != nil }) {
		t.Fatal("transport error never propagated to subsequent senders")
	}
}
