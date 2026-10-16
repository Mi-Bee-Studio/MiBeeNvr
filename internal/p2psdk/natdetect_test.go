package peer

import (
	"context"
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── userspace symmetric-NAT simulation (#70) ────────────────────────────────
//
// The acceptance asks for a symmetric-NAT scenario test that needs no
// privileged network ops (iptables/CAP_NET_ADMIN stay out of CI). The
// interposition below is a faithful userspace model: the client sends to
// two STUN endpoints THROUGH the proxy, and the proxy egresses each
// destination from its OWN per-destination socket — exactly the
// endpoint-dependent mapping that defines a symmetric NAT.

// fakeStunServer answers Binding requests with XOR-MAPPED-ADDRESS set to
// the observed source — the minimum a real STUN server does.
type fakeStunServer struct {
	conn *net.UDPConn
	addr string
	done chan struct{}
}

func newFakeStunServer(t *testing.T) *fakeStunServer {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	s := &fakeStunServer{conn: conn, addr: conn.LocalAddr().String(), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		buf := make([]byte, 1500)
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if n < 20 || binary.BigEndian.Uint16(buf[0:]) != 0x0001 {
				continue
			}
			_, _ = conn.WriteToUDP(buildStunBindingResponse(buf[:n], from), from)
		}
	}()
	t.Cleanup(func() { _ = conn.Close() })
	return s
}

// buildStunBindingResponse mirrors the request's transaction id and reports
// the observed source as XOR-MAPPED-ADDRESS (RFC 5389 §15.2).
func buildStunBindingResponse(req []byte, observed *net.UDPAddr) []byte {
	resp := make([]byte, 20+4+8)
	binary.BigEndian.PutUint16(resp[0:], 0x0101) // Binding Success
	binary.BigEndian.PutUint16(resp[2:], 8)      // attr length
	copy(resp[4:8], req[4:8])                    // magic cookie
	copy(resp[8:20], req[8:20])                  // transaction id
	binary.BigEndian.PutUint16(resp[20:], 0x0020)
	binary.BigEndian.PutUint16(resp[22:], 8)
	resp[24] = 0
	resp[25] = 0x01                               // IPv4
	port := uint16(observed.Port&0xFFFF) ^ 0x2112 //nolint:gosec // G115: ports <65536
	binary.BigEndian.PutUint16(resp[26:], port)
	var ip4 [4]byte
	copy(ip4[:], observed.IP.To4())
	binary.BigEndian.PutUint32(resp[28:], binary.BigEndian.Uint32(ip4[:])^0x2112A442)
	return resp
}

// symmetricNATProxy listens on one port per advertised "server" and
// forwards each destination through a dedicated socket, so every
// destination sees a different source port — the endpoint-dependent
// mapping that defines a symmetric NAT.
type symmetricNATProxy struct {
	listeners []*net.UDPConn // client-facing ports (one per destination)
	done      chan struct{}
}

//nolint:funlen // two symmetric forwarding legs per destination
func newSymmetricNATProxy(t *testing.T, destinations ...string) *symmetricNATProxy {
	t.Helper()
	p := &symmetricNATProxy{done: make(chan struct{})}
	var wg sync.WaitGroup
	for _, dst := range destinations {
		l, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
		require.NoError(t, err)
		p.listeners = append(p.listeners, l)
		raddr, err := net.ResolveUDPAddr("udp4", dst)
		require.NoError(t, err)

		// Per-destination egress socket — the symmetric mapping.
		egress, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
		require.NoError(t, err)

		var (
			mu         sync.Mutex
			lastClient *net.UDPAddr
		)
		wg.Add(2)
		go func() { // client -> destination
			defer wg.Done()
			buf := make([]byte, 1500)
			for {
				n, from, err := l.ReadFromUDP(buf)
				if err != nil {
					return
				}
				mu.Lock()
				lastClient = from
				mu.Unlock()
				_, _ = egress.WriteToUDP(buf[:n], raddr)
			}
		}()
		go func() { // destination -> client
			defer wg.Done()
			buf := make([]byte, 1500)
			for {
				n, _, err := egress.ReadFromUDP(buf)
				if err != nil {
					return
				}
				mu.Lock()
				to := lastClient
				mu.Unlock()
				if to == nil {
					continue
				}
				_, _ = l.WriteToUDP(buf[:n], to)
			}
		}()
	}
	go func() { wg.Wait(); close(p.done) }()
	t.Cleanup(func() {
		for _, l := range p.listeners {
			_ = l.Close()
		}
		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
		}
	})
	return p
}

// TestDetectNATClass_SymmetricVsDirect is the #70 acceptance test: the
// path decision must be correct for the NAT shape the environment actually
// presents — relay for symmetric, direct-first otherwise — without any
// privileged network setup.
func TestDetectNATClass_SymmetricVsDirect(t *testing.T) {
	stunA := newFakeStunServer(t)
	stunB := newFakeStunServer(t)
	direct := []string{stunA.addr, stunB.addr}

	// Control: no interposition — same source port to both servers. On the
	// loopback test bed the mapped address is the machine's own, so the
	// class is public (or cone behind CI NATs); either way NOT relay-first.
	res := DetectNATClass(context.Background(), direct, 2*time.Second)
	require.GreaterOrEqual(t, len(res.MappedByServer), 2, "both fake servers must answer")
	assert.False(t, PreferRelay(res.Class),
		"a consistent mapping must never select relay-first (class=%s)", res.Class)

	// Symmetric simulation: both endpoints dialed through the per-destination
	// proxy — the servers observe different source ports.
	proxy := newSymmetricNATProxy(t, stunA.addr, stunB.addr)
	viaNAT := []string{proxy.listeners[0].LocalAddr().String(), proxy.listeners[1].LocalAddr().String()}

	res = DetectNATClass(context.Background(), viaNAT, 2*time.Second)
	require.GreaterOrEqual(t, len(res.MappedByServer), 2, "both servers must answer through the NAT proxy")
	require.Equal(t, NATSymmetric, res.Class,
		"per-destination mappings must classify as symmetric (got %+v)", res.MappedByServer)
	assert.True(t, PreferRelay(res.Class),
		"symmetric NAT must select the relay path — the path-decision assertion")

	// Fewer than two answers stays unknown (no false verdicts).
	dead := []string{"127.0.0.1:1", "127.0.0.1:2"}
	res = DetectNATClass(context.Background(), dead, 300*time.Millisecond)
	assert.Equal(t, NATUnknown, res.Class)
	assert.False(t, PreferRelay(res.Class))
}

// TestDetectNATClass_DualStackFlag：DualStack 与 STUN 探测正交，即使探
// 测没跑（servers<2）也如实上报接口 v6 形态——auto 钉选消费它豁免
// v4 对称判定。
func TestDetectNATClass_DualStackFlag(t *testing.T) {
	saved := globalIPv6Probe
	t.Cleanup(func() { globalIPv6Probe = saved })

	globalIPv6Probe = func() bool { return true }
	res := DetectNATClass(context.Background(), nil, 0)
	assert.True(t, res.DualStack)
	assert.Equal(t, NATUnknown, res.Class, "no servers must stay unknown")

	globalIPv6Probe = func() bool { return false }
	res = DetectNATClass(context.Background(), nil, 0)
	assert.False(t, res.DualStack)
}

// TestSessionSetForceRelay：钉选写回对后续会话可见（auto 模式的执行
// 面），读回走锁快照。
func TestSessionSetForceRelay(t *testing.T) {
	s := &Session{}
	s.SetForceRelay(true)
	assert.True(t, s.sessionConfig().ForceRelay)
	s.SetForceRelay(false)
	assert.False(t, s.sessionConfig().ForceRelay)
}
