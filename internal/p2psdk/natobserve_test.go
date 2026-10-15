package peer

import (
	"log/slog"
	"testing"
	"time"
)

// TestObserveNATStoresResult pins the Dial → observeNAT wiring (#70/#14:
// observation only): welcome STUN list drives the probe, the result lands
// on the Session and fires the callback; a welcome without STUN servers
// never probes.
func TestObserveNATStoresResult(t *testing.T) {
	stunA := newFakeStunServer(t)
	stunB := newFakeStunServer(t)

	welcome := &Welcome{
		ICEServers: []ICEServer{{URLs: []string{
			"stun:" + stunA.addr, "stun:" + stunB.addr,
		}}},
	}
	got := make(chan NatObservation, 1)
	s := &Session{logger: slog.New(slog.NewTextHandler(discardWriter{}, nil))}
	s.OnNATObservation = func(o NatObservation) { got <- o }

	s.observeNAT(welcome)
	select {
	case o := <-got:
		if o.Class == NATUnknown {
			t.Fatalf("two responsive fake servers must yield a class verdict, got unknown (obs=%+v)", o)
		}
		if !o.StunReachable || o.Responsive < 2 {
			t.Fatalf("reachability = %v/%d, want reachable with >=2 responsive", o.StunReachable, o.Responsive)
		}
		if s.NATObservation() == nil {
			t.Fatal("NATObservation must be stored on the session")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OnNATObservation must fire after a successful probe")
	}
}

func TestObserveNATSkipsWithoutStun(t *testing.T) {
	s := &Session{logger: slog.New(slog.NewTextHandler(discardWriter{}, nil))}
	fired := false
	s.OnNATObservation = func(NatObservation) { fired = true }
	s.observeNAT(&Welcome{}) // no ICE servers
	if fired {
		t.Fatal("no STUN servers in welcome must never probe")
	}
	if s.NATObservation() != nil {
		t.Fatal("no observation must be stored")
	}
}
