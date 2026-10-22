package gb28181

import (
	"sync"

	gbsip "github.com/mickeyzzc/gb28181-go/platform/sip"
)

// TalkUplinkRegistry fans the library server's single OnTalkUplink hook
// out to per-channel subscribers — the intercom WebSocket pushes the
// device's return audio (G.711 A-law frames) back to the browser for the
// full-duplex panel. Payloads are the library's fresh per-packet copies;
// consumers must only read them.
type TalkUplinkRegistry struct {
	mu   sync.Mutex
	subs map[string]map[chan []byte]struct{}
}

// NewTalkUplinkRegistry returns an empty registry.
func NewTalkUplinkRegistry() *TalkUplinkRegistry {
	return &TalkUplinkRegistry{subs: make(map[string]map[chan []byte]struct{})}
}

// Attach wires the registry onto the server's uplink hook. Call once,
// before the server starts serving — the library captures the hook per
// talk session at StartTalk time.
func (r *TalkUplinkRegistry) Attach(srv *gbsip.Server) {
	srv.OnTalkUplink = r.Dispatch
}

// Dispatch delivers one uplink payload to the channel's subscribers.
// Delivery is non-blocking: audio is realtime, so a slow consumer drops
// frames instead of stalling the library's read loop.
func (r *TalkUplinkRegistry) Dispatch(channelID string, payload []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for ch := range r.subs[channelID] {
		select {
		case ch <- payload:
		default:
		}
	}
}

// Subscribe registers one consumer for a channel and returns it with an
// unsubscribe func. Unsubscribing closes the channel so range-loops exit.
func (r *TalkUplinkRegistry) Subscribe(channelID string) (<-chan []byte, func()) {
	ch := make(chan []byte, 64) // ~1.3s of 20ms G.711 frames
	r.mu.Lock()
	if r.subs[channelID] == nil {
		r.subs[channelID] = make(map[chan []byte]struct{})
	}
	r.subs[channelID][ch] = struct{}{}
	r.mu.Unlock()
	unsub := func() {
		r.mu.Lock()
		if set := r.subs[channelID]; set != nil {
			delete(set, ch)
			if len(set) == 0 {
				delete(r.subs, channelID)
			}
		}
		r.mu.Unlock()
		close(ch)
	}
	return ch, unsub
}
