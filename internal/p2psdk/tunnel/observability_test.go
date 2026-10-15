package tunnel

import (
	"bytes"
	"encoding/base64"
	"log/slog"
	"testing"

	peer "github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk"
)

// nilTransport satisfies peer.Transport without a network.
type nilTransport struct{}

func (nilTransport) Send([]byte) error      { return nil }
func (nilTransport) OnMessage(func([]byte)) {}
func (nilTransport) Close() error           { return nil }
func base64Decode(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }
func nilLogger() *slog.Logger               { return slog.New(slog.DiscardHandler) }

var _ peer.Transport = nilTransport{}

// TestStreamDropCounter: the stream delivery queue is intentionally
// non-blocking (an ordered stream cannot back-pressure without stalling
// every other stream) — queue-full drops must be COUNTED, not just logged
// (#13 observability: the drop is the "consumer stalled" signal).
func TestStreamDropCounter(t *testing.T) {
	tun := New(nilTransport{})
	sc := newStreamConn(tun, 1, "svc", nilLogger())

	const queueCap = 64
	for i := range queueCap + 7 {
		sc.pushData([]byte{byte(i)})
	}
	if got := tun.DroppedFrames(); got != 7 {
		t.Fatalf("DroppedFrames = %d, want 7 (queue cap %d + 7 pushes)", got, queueCap)
	}
	// Reads drain the queue; further pushes still land.
	for range 3 {
		buf := make([]byte, 1)
		_, _ = sc.Read(buf)
	}
	sc.pushData([]byte{0xff})
	if got := tun.DroppedFrames(); got != 7 {
		t.Fatalf("DroppedFrames after drain+push = %d, want still 7", got)
	}
}

// TestFragmenterSizeOption: WithMaxPayloadBytes shrinks fragments; the
// receiver accepts any size (interop), the cap is a sender-side choice.
func TestFragmenterSizeOption(t *testing.T) {
	tun := New(nilTransport{}, WithMaxPayloadBytes(16*1024))
	if got := tun.fragmentSize(); got != 16*1024 {
		t.Fatalf("fragmentSize = %d, want 16384", got)
	}
	// Non-positive option values fall back to the package default.
	tun2 := New(nilTransport{}, WithMaxPayloadBytes(0))
	if got := tun2.fragmentSize(); got != MaxPayloadBytes {
		t.Fatalf("fragmentSize(0) = %d, want %d", got, MaxPayloadBytes)
	}

	f := NewFragmenter(7, 16*1024)
	frags, err := f.Fragment(bytes.Repeat([]byte{0x42}, 16*1024*2+5))
	if err != nil {
		t.Fatalf("fragment: %v", err)
	}
	if len(frags) != 3 {
		t.Fatalf("fragments = %d, want 3", len(frags))
	}
	for i, fr := range frags {
		raw, err := base64Decode(fr.Payload)
		if err != nil {
			t.Fatalf("frag %d decode: %v", i, err)
		}
		want := 16 * 1024
		if i == 2 {
			want = 5
		}
		if len(raw) != want {
			t.Fatalf("frag %d payload = %d bytes, want %d", i, len(raw), want)
		}
	}
	// Default fragmenter keeps the historical 32 KiB ceiling.
	fd := NewFragmenter(9, 0)
	if frags, _ := fd.Fragment(bytes.Repeat([]byte{1}, MaxPayloadBytes+1)); len(frags) != 2 {
		t.Fatalf("default fragments = %d, want 2", len(frags))
	}
}
