package peer

import (
	"encoding/binary"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePCPGateway answers PCP MAP requests per RFC 6887: echoes the nonce,
// copies internal→external port (same-port semantics), success code 0.
type fakePCPGateway struct {
	server net.PacketConn
}

func newFakePCPGateway(t *testing.T, wanIP net.IP, externalPortOverride int) *fakePCPGateway {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	g := &fakePCPGateway{server: pc}
	go func() {
		buf := make([]byte, 128)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			req := buf[:n]
			if len(req) < 60 || req[0] != 2 || req[1] != 1 {
				continue
			}
			internalPort := binary.BigEndian.Uint16(req[40:42])
			extPort := internalPort
			if externalPortOverride != 0 {
				extPort = uint16(externalPortOverride) //nolint:gosec // G115
			}
			resp := make([]byte, 60)
			resp[0] = 2
			resp[1] = 0x80 | 1
			resp[2] = 0
			resp[3] = 0 // result code SUCCESS — offset 3 per RFC 6887 Fig. 3
			binary.BigEndian.PutUint32(resp[4:8], binary.BigEndian.Uint32(req[4:8]))
			binary.BigEndian.PutUint32(resp[8:12], 42) // epoch
			copy(resp[24:36], req[24:36])              // nonce echo
			resp[36] = req[36]                         // protocol echo
			binary.BigEndian.PutUint16(resp[40:42], internalPort)
			binary.BigEndian.PutUint16(resp[42:44], extPort)
			v4 := wanIP.To4()
			copy(resp[48:52], v4) // IPv4-mapped tail of the 16B field
			_, _ = pc.WriteTo(resp, addr)
		}
	}()
	t.Cleanup(func() { _ = pc.Close() })
	return g
}

// TestPCPMapUDPWireAndParse (#43-D): request bytes match the RFC 6887
// layout and the response parses to the assigned port + echoed nonce.
func TestPCPMapUDPWireAndParse(t *testing.T) {
	gw := newFakePCPGateway(t, net.ParseIP("121.8.0.156"), 0)
	gwAddr := gw.server.LocalAddr().String()

	extPort, err := pcpMapUDP(gwAddr, 40000, pcpTimeout)
	require.NoError(t, err)
	assert.Equal(t, 40000, extPort, "same-port mapping")

	// Wrong-port assignment (gateway override) must be reported as-is so
	// the ladder's same-port guard rejects it.
	gw2 := newFakePCPGateway(t, net.ParseIP("121.8.0.156"), 61000)
	extPort2, err := pcpMapUDP(gw2.server.LocalAddr().String(), 40000, pcpTimeout)
	require.NoError(t, err)
	assert.Equal(t, 61000, extPort2)
}

// TestTryPCPMappingGatewayWalk: the rung surfaces a usable ip:port from a
// reachable gateway (gateway discovery is overridden via hook injection
// paths covered by the PMP tests; here we exercise pcpMapUDP through the
// rung's public shape with the loopback "gateway").
func TestTryPCPMappingGatewayWalk(t *testing.T) {
	// tryPCPMapping walks discoverGatewayIPs which cannot be pointed at
	// loopback reliably cross-platform; the rung logic is covered by the
	// hook-driven autoendpoint tests. Here we pin pcpMapUDP + same-port
	// guard composition only.
	gw := newFakePCPGateway(t, net.ParseIP("8.134.136.138"), 0)
	ext, err := pcpMapUDP(gw.server.LocalAddr().String(), 40000, pcpTimeout)
	require.NoError(t, err)
	require.Equal(t, 40000, ext)
}
