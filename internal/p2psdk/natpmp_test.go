package peer

// natpmp_test.go — #43-D 回归钉：NAT-PMP 请求/应答线格式、result 码拒绝、
// /proc/net/route 解析。协议行为用本地假网关（UDP 回环）验证。

import (
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePMPGateway answers NAT-PMP requests on a loopback UDP socket.
type fakePMPGateway struct {
	t        *testing.T
	addr     string
	wanIP    [4]byte
	mapCode  uint16 // result code for map responses (0 = success)
	extPort  uint16 // assigned external port in the map response
	listener *net.UDPConn
}

func newFakePMPGateway(t *testing.T) *fakePMPGateway {
	t.Helper()
	return newFakePMPGatewayCfg(t, 0, 0)
}

// newFakePMPGatewayCfg builds the fake with its response knobs set BEFORE
// the serve goroutine starts (no field races under -race).
func newFakePMPGatewayCfg(t *testing.T, mapCode uint16, extPort uint16) *fakePMPGateway {
	t.Helper()
	l, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	g := &fakePMPGateway{
		t: t, addr: l.LocalAddr().String(), listener: l,
		wanIP: [4]byte{203, 0, 113, 9}, mapCode: mapCode, extPort: extPort,
	}
	go g.serve()
	return g
}

func (g *fakePMPGateway) serve() {
	buf := make([]byte, 64)
	for {
		n, from, err := g.listener.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if n < 2 {
			continue
		}
		var resp []byte
		switch buf[1] {
		case natPMPOpAddr:
			resp = make([]byte, 12)
			resp[0], resp[1] = natPMPVersion, natPMPOpAddr|0x80
			copy(resp[8:12], g.wanIP[:])
		case natPMPOpMap:
			resp = make([]byte, 16)
			resp[0], resp[1] = natPMPVersion, natPMPOpMap|0x80
			binary.BigEndian.PutUint16(resp[2:4], g.mapCode)
			copy(resp[8:10], buf[4:6]) // internal port echo
			ext := g.extPort
			if ext == 0 && g.mapCode == 0 {
				ext = binary.BigEndian.Uint16(buf[6:8]) // honor the request
			}
			binary.BigEndian.PutUint16(resp[12:14], ext)
		default:
			continue
		}
		_, _ = g.listener.WriteToUDP(resp, from)
	}
}

func TestPMPExternalIP(t *testing.T) {
	g := newFakePMPGateway(t)
	ip, err := pmpExternalIP(g.addr, 500*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, net.IPv4(203, 0, 113, 9).String(), ip.String())
}

func TestPMPMapUDP(t *testing.T) {
	g := newFakePMPGateway(t)
	port, err := pmpMapUDP(g.addr, 40000, 500*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, 40000, port, "same-port mapping must be echoed")
}

func TestPMPMapUDPDenied(t *testing.T) {
	g := newFakePMPGatewayCfg(t, 3, 0) // result code 3 = denied
	_, err := pmpMapUDP(g.addr, 40000, 500*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "result code 3")
}

func TestPMPExchangeNoServer(t *testing.T) {
	// Nothing listens here: an ephemeral port on loopback that we never bind.
	_, err := pmpMapUDP("127.0.0.1:1", 40000, 200*time.Millisecond)
	require.Error(t, err)
}

func TestParseProcNetRoutes(t *testing.T) {
	// Little-endian hex gateways on default routes only.
	data := "Iface\tDestination\tGateway\tFlags\n" +
		"eth0\t00000000\t0100A8C0\t0003\n" + // 192.168.0.1
		"eth0\t00000000\tC0A80001\t0003\n" + // dup default → both parsed
		"eth1\t0000FEA9\t0100A8C0\t0001\n" // non-default → skipped
	ips := parseProcNetRoutesBytes(data)
	require.Len(t, ips, 2)
	assert.Equal(t, "192.168.0.1", ips[0].String())
	assert.Equal(t, "1.0.168.192", ips[1].String())
}
