package tunnel

import (
	"encoding/json"
	"net"
	"sync"
	"testing"
)

// laneRecordingTransport records which lane (normal vs priority) every
// outbound wire message took.
type laneRecordingTransport struct {
	mu     sync.Mutex
	normal [][]byte
	high   [][]byte
}

func (r *laneRecordingTransport) Send(d []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.normal = append(r.normal, d)
	return nil
}

func (r *laneRecordingTransport) SendPriority(d []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.high = append(r.high, d)
	return nil
}

func (r *laneRecordingTransport) OnMessage(func([]byte)) {}
func (r *laneRecordingTransport) Close() error           { return nil }

func (r *laneRecordingTransport) lanes() ([][]byte, [][]byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]byte(nil), r.normal...), append([][]byte(nil), r.high...)
}

// dataMessages filters a lane's wire messages down to tunnel data frames
// (open/close always ride the normal lane).
func dataMessages(t *testing.T, lane [][]byte) [][]byte {
	t.Helper()
	var out [][]byte
	for _, raw := range lane {
		var m TunnelMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal wire msg: %v", err)
		}
		if m.Type == TunnelMessageTypeData {
			out = append(out, raw)
		}
	}
	return out
}

// With egress priority armed, a stream's first writes ride the priority
// lane; the open frame stays on the normal lane regardless.
func TestEgressPrioritySmallWritesRideHighLane(t *testing.T) {
	tr := &laneRecordingTransport{}
	tun := New(tr)
	defer func() { _ = tun.Close() }()
	tun.EnableEgressPriority(1 << 20)

	conn, err := tun.Dial("svc")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	sc := conn.(net.Conn)
	if _, err := sc.Write([]byte("hello-priority")); err != nil {
		t.Fatalf("write: %v", err)
	}

	normal, high := tr.lanes()
	if got := len(dataMessages(t, high)); got != 1 {
		t.Fatalf("expected 1 data frame on high lane, got %d (high=%d normal=%d)",
			got, len(high), len(normal))
	}
	if got := len(dataMessages(t, normal)); got != 0 {
		t.Fatalf("data frames leaked to normal lane: %d", got)
	}
}

// Once a stream has consumed its budget it demotes — and stays demoted
// (one-way, which is what keeps intra-stream ordering intact).
func TestEgressPriorityBudgetDemotesOneWay(t *testing.T) {
	tr := &laneRecordingTransport{}
	tun := New(tr)
	defer func() { _ = tun.Close() }()
	tun.EnableEgressPriority(1 << 20) // start generous

	conn, err := tun.Dial("svc")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := conn.Write([]byte("first")); err != nil {
		t.Fatalf("write 1: %v", err)
	}

	_, high := tr.lanes()
	first := dataMessages(t, high)
	if len(first) != 1 {
		t.Fatalf("expected first write on high lane, got %d frames", len(first))
	}
	// Shrink the budget to exactly what stream 1 already used.
	tun.EnableEgressPriority(int64(len(first[0])) - 1)

	if _, err := conn.Write([]byte("second")); err != nil {
		t.Fatalf("write 2: %v", err)
	}
	if _, err := conn.Write([]byte("third")); err != nil {
		t.Fatalf("write 3: %v", err)
	}

	normal, high := tr.lanes()
	if got := len(dataMessages(t, high)); got != 1 {
		t.Fatalf("high lane should still hold exactly the first frame, got %d", got)
	}
	if got := len(dataMessages(t, normal)); got < 2 {
		t.Fatalf("post-budget writes should ride normal lane, got %d data frames", got)
	}
}

// The budget is per-stream: demoting one stream never affects another.
func TestEgressPriorityBudgetIsPerStream(t *testing.T) {
	tr := &laneRecordingTransport{}
	tun := New(tr)
	defer func() { _ = tun.Close() }()

	c1, err := tun.Dial("svc")
	if err != nil {
		t.Fatalf("dial 1: %v", err)
	}
	// Budget exactly covers stream 1's first frame; stream 2 has its own.
	tun.EnableEgressPriority(1 << 20)
	if _, err := c1.Write([]byte("one")); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	_, high := tr.lanes()
	first := dataMessages(t, high)
	if len(first) != 1 {
		t.Fatalf("stream 1 first frame expected on high lane, got %d", len(first))
	}
	tun.EnableEgressPriority(int64(len(first[0])) - 1)

	c2, err := tun.Dial("svc")
	if err != nil {
		t.Fatalf("dial 2: %v", err)
	}
	if _, err := c2.Write([]byte("two")); err != nil {
		t.Fatalf("write 2: %v", err)
	}

	_, high = tr.lanes()
	if got := len(dataMessages(t, high)); got != 2 {
		t.Fatalf("stream 2 should have its own budget (2 high frames total), got %d", got)
	}
}

// Without EnableEgressPriority nothing changes: every frame rides the
// normal lane, byte for byte the pre-feature behavior.
func TestEgressPriorityDisabledByDefault(t *testing.T) {
	tr := &laneRecordingTransport{}
	tun := New(tr)
	defer func() { _ = tun.Close() }()

	conn, err := tun.Dial("svc")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := conn.Write([]byte("plain")); err != nil {
		t.Fatalf("write: %v", err)
	}

	normal, high := tr.lanes()
	if len(high) != 0 {
		t.Fatalf("priority lane used while feature disabled: %d frames", len(high))
	}
	if got := len(dataMessages(t, normal)); got != 1 {
		t.Fatalf("expected 1 normal data frame, got %d", got)
	}
}
