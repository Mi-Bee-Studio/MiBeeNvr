package peer

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeSTUN answers Binding Requests with a caller-chosen XOR-MAPPED
// address; the mapping function receives the per-remote request count so
// tests can simulate mapping changes between netcheck rounds.
type fakeSTUN struct {
	mapping func(remote string, reqs int) *net.UDPAddr
}

func startFakeSTUN(t *testing.T, f fakeSTUN) string {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })
	reqs := map[string]int{}
	go func() {
		buf := make([]byte, 1500)
		for {
			n, raddr, readErr := pc.ReadFrom(buf)
			if readErr != nil {
				return
			}
			if n < 20 || binary.BigEndian.Uint16(buf[0:]) != 0x0001 {
				continue
			}
			reqs[raddr.String()]++
			observed := f.mapping(raddr.String(), reqs[raddr.String()])
			if observed == nil {
				continue // simulate a server that ignores this request
			}
			resp := make([]byte, 32)
			binary.BigEndian.PutUint16(resp[0:], 0x0101)
			binary.BigEndian.PutUint16(resp[2:], 12) // one 12-byte attribute
			binary.BigEndian.PutUint32(resp[4:], stunMagicCookie)
			copy(resp[8:20], buf[8:20]) // echo transaction id
			binary.BigEndian.PutUint16(resp[20:], 0x0020)
			binary.BigEndian.PutUint16(resp[22:], 8)
			resp[24] = 0
			resp[25] = 0x01 // IPv4
			port := uint16(observed.Port)
			binary.BigEndian.PutUint16(resp[26:], port^0x2112)
			var xip [4]byte
			binary.BigEndian.PutUint32(xip[:], binary.BigEndian.Uint32(observed.IP.To4())^stunMagicCookie)
			copy(resp[28:], xip[:])
			_, _ = pc.WriteTo(resp, raddr)
		}
	}()
	return pc.LocalAddr().String()
}

func TestNetCheck_EIM(t *testing.T) {
	endpoint := &net.UDPAddr{IP: net.IPv4(203, 0, 113, 7), Port: 5000}
	servers := []string{
		startFakeSTUN(t, fakeSTUN{mapping: func(string, int) *net.UDPAddr { return endpoint }}),
		startFakeSTUN(t, fakeSTUN{mapping: func(string, int) *net.UDPAddr { return endpoint }}),
	}
	report := NetCheck(context.Background(), servers, 500*time.Millisecond)
	require.False(t, report.UDPBlocked)
	require.Equal(t, NetMappingEIM, report.Mapping)
	require.Equal(t, 2, report.Responded)
	require.False(t, report.MappingChanged)
	require.Len(t, report.MappedAddrs, 2)
}

func TestNetCheck_EDM(t *testing.T) {
	servers := []string{
		startFakeSTUN(t, fakeSTUN{mapping: func(string, int) *net.UDPAddr {
			return &net.UDPAddr{IP: net.IPv4(198, 51, 100, 1), Port: 1111}
		}}),
		startFakeSTUN(t, fakeSTUN{mapping: func(string, int) *net.UDPAddr {
			return &net.UDPAddr{IP: net.IPv4(198, 51, 100, 2), Port: 2222}
		}}),
	}
	report := NetCheck(context.Background(), servers, 500*time.Millisecond)
	require.Equal(t, NetMappingEDM, report.Mapping)
	require.Equal(t, 2, report.Responded)
}

func TestNetCheck_UDPBlocked(t *testing.T) {
	// Bind then close to get a guaranteed-dead port.
	dead, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	deadAddr := dead.LocalAddr().String()
	require.NoError(t, dead.Close())

	report := NetCheck(context.Background(), []string{deadAddr, deadAddr}, 300*time.Millisecond)
	require.True(t, report.UDPBlocked)
	require.Equal(t, NetMappingInconclusive, report.Mapping)
	require.Equal(t, 0, report.Responded)
}

func TestNetCheck_MappingChangedBetweenRounds(t *testing.T) {
	servers := []string{
		startFakeSTUN(t, fakeSTUN{mapping: func(string, int) *net.UDPAddr {
			return &net.UDPAddr{IP: net.IPv4(203, 0, 113, 9), Port: 3000}
		}}),
		startFakeSTUN(t, fakeSTUN{mapping: func(_ string, reqs int) *net.UDPAddr {
			port := 4000
			if reqs <= 1 {
				port = 4100
			}
			return &net.UDPAddr{IP: net.IPv4(203, 0, 113, 9), Port: port}
		}}),
	}
	report := NetCheck(context.Background(), servers, 500*time.Millisecond)
	require.True(t, report.MappingChanged, "round-2 endpoint must differ from round 1")
	require.Equal(t, NetMappingEDM, report.Mapping, "latest-round endpoints differ across servers")
}

func TestNetCheck_EmptyServers(t *testing.T) {
	report := NetCheck(context.Background(), nil, time.Second)
	require.Equal(t, NetMappingInconclusive, report.Mapping)
	require.Equal(t, 0, report.Queried)
	require.False(t, report.UDPBlocked)
}
