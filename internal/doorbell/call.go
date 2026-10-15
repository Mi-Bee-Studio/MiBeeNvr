package doorbell

import (
	"errors"
	"net"
	"sync"
	"time"

	"github.com/ghettovoice/gosip/sip"
	"github.com/google/uuid"
)

// Call states.
const (
	CallRinging  = "ringing"
	CallAnswered = "answered"
	CallEnded    = "ended"
)

// Call is one doorbell session, from INVITE to BYE.
type Call struct {
	ID           string    `json:"id"`
	User         string    `json:"user"`
	Name         string    `json:"name,omitempty"`
	CameraID     string    `json:"camera_id,omitempty"`
	SnapshotPath string    `json:"snapshot_path,omitempty"`
	State        string    `json:"state"`
	StartedAt    time.Time `json:"started_at"`
	AnsweredAt   time.Time `json:"answered_at,omitempty"`
	EndedAt      time.Time `json:"ended_at,omitempty"`
	EndReason    string    `json:"end_reason,omitempty"`

	mu        sync.Mutex
	req       sip.Request
	tx        sip.ServerTransaction
	sipCallID string
	offer     audioOffer
	media     *mediaSession
}

// TalkBridge pipes G.711 between a WebSocket client and the doorbell's RTP
// session. One bridge per call — doorbell talk is single-operator.
type TalkBridge struct {
	In     chan<- []byte // browser → doorbell (raw G.711 payloads)
	Out    <-chan []byte // doorbell → browser
	Done   <-chan struct{}
	detach func()
}

// Close detaches the bridge (idempotent).
func (b *TalkBridge) Close() { b.detach() }

var (
	// ErrUnknownCall maps to 404.
	ErrUnknownCall = errors.New("no such call")
	// ErrCallState maps to 409.
	ErrCallState = errors.New("call is not in the required state")
)

// callManager owns the live calls plus a bounded recent-calls history.
type callManager struct {
	mu     sync.Mutex
	byID   map[string]*Call
	recent []*Call // newest last, capped
	max    int
}

func newCallManager() *callManager {
	return &callManager{byID: map[string]*Call{}, max: 50}
}

func (m *callManager) newCall(req sip.Request, tx sip.ServerTransaction, sipCallID string, user User, offer audioOffer) *Call {
	c := &Call{
		ID:        uuid.NewString(),
		User:      user.Username,
		Name:      user.Name,
		CameraID:  user.CameraID,
		State:     CallRinging,
		StartedAt: time.Now(),
		req:       req,
		tx:        tx,
		sipCallID: sipCallID,
		offer:     offer,
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byID[c.ID] = c
	m.recent = append(m.recent, c)
	if len(m.recent) > m.max {
		delete(m.byID, m.recent[0].ID)
		m.recent = m.recent[1:]
	}
	return c
}

func (m *callManager) get(id string) (*Call, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.byID[id]
	if !ok {
		return nil, ErrUnknownCall
	}
	return c, nil
}

func (m *callManager) bySIPCallID(sipCallID string) *Call {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.recent) - 1; i >= 0; i-- {
		if m.recent[i].sipCallID == sipCallID {
			return m.recent[i]
		}
	}
	return nil
}

// calls snapshots the recent list (API surface).
func (m *callManager) calls() []*Call {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Call, len(m.recent))
	copy(out, m.recent)
	return out
}

// startMedia binds the RTP endpoint for an answered call.
func (c *Call) startMedia() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.media != nil {
		return nil
	}
	pt, _, ok := c.offer.payloadType()
	if !ok {
		return errors.New("no negotiable audio payload")
	}
	m, err := startMedia(pt)
	if err != nil {
		return err
	}
	c.media = m
	c.media.setDestination(&net.UDPAddr{IP: net.ParseIP(c.offer.Address), Port: c.offer.Port})
	return nil
}

// attachTalk wires a bridge to the call's media session.
func (c *Call) attachTalk() (*TalkBridge, error) {
	c.mu.Lock()
	media := c.media
	state := c.State
	c.mu.Unlock()
	if state != CallAnswered || media == nil {
		return nil, ErrCallState
	}
	out, write, detach := media.subscribe(64)
	in := make(chan []byte, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for payload := range in {
			write(payload)
		}
	}()
	return &TalkBridge{In: in, Out: out, Done: done, detach: func() {
		detach()
		close(in)
	}}, nil
}

// end transitions the call to ended and tears down media exactly once.
func (c *Call) end(reason string) {
	c.mu.Lock()
	if c.State == CallEnded {
		c.mu.Unlock()
		return
	}
	c.State = CallEnded
	c.EndedAt = time.Now()
	c.EndReason = reason
	media := c.media
	c.media = nil
	c.mu.Unlock()
	if media != nil {
		media.close()
	}
}
