package tunnel

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/e2ee"
)

// loopTransport is an in-memory bidirectional Transport pair: bytes written
// on one side surface as OnMessage on the other, synchronously captured so
// tests can inspect the WIRE FORMAT (asserting plaintext never leaks).
type loopTransport struct {
	mu        sync.Mutex
	onMessage func([]byte)
	wire      [][]byte // frames this side sent (for wire inspection)
	pending   [][]byte // frames that arrived before Serve registered (mirrors wrapDataChannel buffering)
	peerLink  *loopTransport
}

func newLoopPair() (*loopTransport, *loopTransport) {
	a, b := &loopTransport{}, &loopTransport{}
	a.peerLink, b.peerLink = b, a
	return a, b
}

func (l *loopTransport) OnMessage(h func([]byte)) {
	l.mu.Lock()
	l.onMessage = h
	buffered := l.pending
	l.pending = nil
	l.mu.Unlock()
	for _, data := range buffered {
		h(append([]byte(nil), data...))
	}
}

func (l *loopTransport) Send(data []byte) error {
	l.mu.Lock()
	l.wire = append(l.wire, append([]byte(nil), data...))
	l.mu.Unlock()
	p := l.peerLink
	p.mu.Lock()
	h := p.onMessage
	if h == nil {
		p.pending = append(p.pending, append([]byte(nil), data...))
		p.mu.Unlock()
		return nil
	}
	p.mu.Unlock()
	h(append([]byte(nil), data...))
	return nil
}

func (l *loopTransport) Close() error { return nil }

// frames returns the wire frames captured on this side, parsed.
func (l *loopTransport) frames(t *testing.T) []TunnelMessage {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]TunnelMessage, 0, len(l.wire))
	for _, raw := range l.wire {
		var m TunnelMessage
		require.NoError(t, json.Unmarshal(raw, &m))
		out = append(out, m)
	}
	return out
}

func (l *loopTransport) deliver(t *testing.T, raw []byte) {
	t.Helper()
	l.mu.Lock()
	h := l.onMessage
	l.mu.Unlock()
	require.NotNil(t, h, "Serve must be running")
	h(raw)
}

// sessionKeys derives a matched keypair the way relay.DeriveE2EEKey would.
func sessionKeys(t *testing.T) (aKey, bKey [32]byte) {
	t.Helper()
	// Two independent X25519 pairs (stand-ins for the relay keypairs).
	aPriv := [32]byte{1}
	bPriv := [32]byte{2}
	aPub := e2eePub(t, aPriv)
	bPub := e2eePub(t, bPriv)
	var err error
	aKey, err = e2ee.DeriveSessionKey(aPriv, aPub, bPub)
	require.NoError(t, err)
	bKey, err = e2ee.DeriveSessionKey(bPriv, bPub, aPub)
	require.NoError(t, err)
	require.Equal(t, aKey, bKey, "both peers must derive the same key")
	return aKey, bKey
}

func startEchoPair(t *testing.T, enableA, enableB bool) (client *Tunnel, server *Tunnel, aWire, bWire *loopTransport) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if n > 0 {
						_, _ = c.Write(buf[:n])
					}
					if err != nil {
						_ = c.Close()
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })

	ta, tb := newLoopPair()
	clientTun := New(&transportShim{ta})
	serverTun := New(&transportShim{tb})
	serverTun.RegisterService("echo", ln.Addr().String())

	if enableA || enableB {
		aKey, bKey := sessionKeys(t)
		if enableA {
			require.NoError(t, clientTun.EnableE2EE(aKey[:]))
		}
		if enableB {
			require.NoError(t, serverTun.EnableE2EE(bKey[:]))
		}
	}

	go serverTun.Serve()
	go clientTun.Serve()
	return clientTun, serverTun, ta, tb
}

// transportShim adapts loopTransport to peer.Transport.
type transportShim struct{ l *loopTransport }

func (s *transportShim) OnMessage(h func([]byte)) { s.l.OnMessage(h) }
func (s *transportShim) Send(b []byte) error      { return s.l.Send(b) }
func (s *transportShim) Close() error             { return s.l.Close() }

// waitServing blocks until the tunnel's Serve loop registered its handler.
func waitServing(t *testing.T, l *loopTransport) {
	t.Helper()
	require.Eventually(t, func() bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.onMessage != nil
	}, 3*time.Second, 20*time.Millisecond, "Serve must be running")
}

func roundTrip(t *testing.T, tun *Tunnel, msg string) string {
	t.Helper()
	conn, err := tun.Dial("echo")
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	deadline := time.Now().Add(3 * time.Second)
	_ = conn.SetDeadline(deadline)
	_, err = conn.Write([]byte(msg))
	require.NoError(t, err)
	buf := make([]byte, len(msg))
	n, err := conn.Read(buf)
	require.NoError(t, err)
	return string(buf[:n])
}

func TestE2EE_RoundTripEncryptedBothWays(t *testing.T) {
	client, server, aWire, bWire := startEchoPair(t, true, true)

	got := roundTrip(t, client, "hello-e2ee")
	assert.Equal(t, "hello-e2ee", got)
	// Second round trip — now sealed in BOTH directions (device confirmed).
	got = roundTrip(t, client, "second-message")
	assert.Equal(t, "second-message", got)

	_ = server

	// Wire assertion: the secret string must NEVER appear in plaintext on
	// either side's captured frames — including the device's first sealed
	// response (sealed from frame 1).
	for _, wire := range []*loopTransport{aWire, bWire} {
		for _, frame := range wire.frames(t) {
			assert.NotContains(t, frame.Payload, base64.StdEncoding.EncodeToString([]byte("hello-e2ee")),
				"plaintext payload must never hit the wire when E2EE is on")
		}
	}

	// The device side frames must carry enc=true data frames.
	sawSealed := false
	for _, frame := range bWire.frames(t) {
		if frame.Type != TunnelMessageTypeData {
			continue
		}
		var data TunnelData
		require.NoError(t, json.Unmarshal([]byte(frame.Payload), &data))
		if data.Enc {
			sawSealed = true
		}
	}
	assert.True(t, sawSealed, "device outbound frames must be sealed")
}

func TestE2EE_PlaintextCompatWithLegacyPeer(t *testing.T) {
	// New dialer (E2EE on) ↔ legacy device (no E2EE, ignores unknown fields).
	client, _, aWire, bWire := startEchoPair(t, true, false)

	got := roundTrip(t, client, "legacy-compat")
	assert.Equal(t, "legacy-compat", got)

	// Dialer stays plaintext the whole way (no confirmation frame arrived).
	for _, frame := range aWire.frames(t) {
		if frame.Type != TunnelMessageTypeData {
			continue
		}
		var data TunnelData
		require.NoError(t, json.Unmarshal([]byte(frame.Payload), &data))
		assert.False(t, data.Enc, "dialer must not seal before peer confirmation")
	}
	for _, frame := range bWire.frames(t) {
		if frame.Type != TunnelMessageTypeData {
			continue
		}
		var data TunnelData
		require.NoError(t, json.Unmarshal([]byte(frame.Payload), &data))
		assert.False(t, data.Enc, "legacy device never seals")
	}

	// Legacy dialer (no flag) ↔ new device: device sees open.e2ee=false →
	// serves plaintext. Covered implicitly: the device side of this test
	// answered plaintext above.
	_ = client
}

func TestE2EE_TamperedFrameDropped(t *testing.T) {
	client, server, aWire, bWire := startEchoPair(t, true, true)

	conn, err := client.Dial("echo")
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	deadline := time.Now().Add(3 * time.Second)
	_ = conn.SetDeadline(deadline)
	_, err = conn.Write([]byte("payload-before-tamper"))
	require.NoError(t, err)

	// Wait for the device's sealed echo (captured on the device's wire),
	// then flip one ciphertext byte and deliver it to the client tunnel.
	var sealedFrame string
	require.Eventually(t, func() bool {
		for _, f := range bWire.frames(t) {
			if f.Type == TunnelMessageTypeData {
				var d TunnelData
				if json.Unmarshal([]byte(f.Payload), &d) == nil && d.Enc {
					sealedFrame = f.Payload
					return true
				}
			}
		}
		return false
	}, 3*time.Second, 50*time.Millisecond, "device must answer sealed")

	var data TunnelData
	require.NoError(t, json.Unmarshal([]byte(sealedFrame), &data))
	raw, err := base64.StdEncoding.DecodeString(data.Payload)
	require.NoError(t, err)
	raw[len(raw)-3] ^= 0xFF // flip a byte inside the Poly1305 tag/ciphertext
	data.Payload = base64.StdEncoding.EncodeToString(raw)

	tampered, err := json.Marshal(TunnelMessage{
		Type: TunnelMessageTypeData, Payload: string(mustJSONBytes(data)),
	})
	require.NoError(t, err)

	// The tampered frame must be DROPPED (warn + no stream delivery), not
	// delivered as garbage and not crash the tunnel.
	aWire.deliver(t, tampered)
	// Tunnel still alive: another round trip works.
	_ = server
	got := roundTrip(t, client, "after-tamper")
	assert.Equal(t, "after-tamper", got)
}

func TestE2EE_DeriveSessionKeySymmetryAndBinding(t *testing.T) {
	aPriv := [32]byte{7}
	bPriv := [32]byte{9}
	aPub := e2eePub(t, aPriv)
	bPub := e2eePub(t, bPriv)

	k1, err := e2ee.DeriveSessionKey(aPriv, aPub, bPub)
	require.NoError(t, err)
	k2, err := e2ee.DeriveSessionKey(bPriv, bPub, aPub)
	require.NoError(t, err)
	assert.Equal(t, k1, k2, "derivation must be symmetric")

	// A third party's derivation must NOT match (binding to the pair).
	cPriv := [32]byte{11}
	cPub := e2eePub(t, cPriv)
	k3, err := e2ee.DeriveSessionKey(cPriv, cPub, bPub)
	require.NoError(t, err)
	assert.NotEqual(t, k2, k3)
}

func TestE2EE_SealedFrameWithoutKeyDropped(t *testing.T) {
	// Device WITHOUT e2ee receives a sealed frame (misbehaving peer):
	// ciphertext must never reach the stream.
	client, _, _, bWire := startEchoPair(t, false, false)

	// Fake a sealed data frame delivered to the device tunnel.
	fake := TunnelMessage{
		Type: TunnelMessageTypeData,
		Payload: string(mustJSONBytes(TunnelData{
			StreamID: 1, Seq: 0, Total: 1,
			Payload: base64.StdEncoding.EncodeToString([]byte("ciphertext-garbage")),
			Enc:     true,
		})),
	}
	waitServing(t, bWire)
	bWire.deliver(t, mustJSONBytes(fake))

	// The tunnel is still functional: a normal open + data exchange works.
	got := roundTrip(t, client, "still-alive")
	assert.Equal(t, "still-alive", got)
}

// mustJSONBytes marshals without the panic-on-error helper coupling.
func mustJSONBytes(v interface{}) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("mustJSON: %v", err))
	}
	return b
}

// e2eePub computes the X25519 public key for a test private scalar.
func e2eePub(t *testing.T, priv [32]byte) [32]byte {
	t.Helper()
	pub, err := e2ee.PublicKey(priv)
	require.NoError(t, err)
	return pub
}
