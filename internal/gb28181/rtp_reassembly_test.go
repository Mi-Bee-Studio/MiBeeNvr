package gb28181

import (
	"math/rand"
	"net"
	"testing"

	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"

	"github.com/mickeyzzc/gb28181-go/psmux"
)

// Regression coverage for #444: a large H.264 access unit spans multiple PES
// packets (PES_packet_length is 16-bit, psmux chunks at 60KB) and therefore
// dozens of RTP packets — more than the jitter buffer holds at once, so the
// AU reaches the PS demuxer across several mid-AU force-flush feeds. Two bugs
// combined to truncate every such AU at its first PES chunk: the drain loop
// stopped after half the contiguous run (its bound shrank as packets were
// collected), and the marker feed then extracted the AU while its final PES
// was still pending reassembly.

// pseudoRandomNALU builds an n-byte NALU body that contains no Annex-B start
// codes (bytes 1..255, never runs of zeros), so false NALU boundaries cannot
// mask the PES/RTP reassembly behavior under test.
func pseudoRandomNALU(hdr, firstPayload byte, n int, seed int64) []byte {
	body := make([]byte, n)
	body[0] = hdr
	if n > 1 {
		body[1] = firstPayload
	}
	rng := rand.New(rand.NewSource(seed))
	for i := 2; i < n; i++ {
		body[i] = byte(rng.Intn(255) + 1)
	}
	return body
}

// largeH264Stream builds SPS/PPS and one 122KB IDR slice — the PiCameraV1
// shape from the field report (#444): three NALUs, the IDR alone spanning
// three PES packets.
func largeH264Stream() (sps, pps, idr []byte) {
	sps = make([]byte, 30)
	sps[0], sps[1], sps[2], sps[3] = 0x67, 0x64, 0x00, 0xAC
	for i := 4; i < len(sps); i++ {
		sps[i] = byte(0x20 + i)
	}
	pps = make([]byte, 10)
	pps[0] = 0x68
	for i := 1; i < len(pps); i++ {
		pps[i] = 0xAA
	}
	idr = pseudoRandomNALU(0x65, 0x88, 122000, 7) // first_mb_in_slice == 0
	return sps, pps, idr
}

func annexBJoin(nalus ...[]byte) []byte {
	var out []byte
	for _, nalu := range nalus {
		out = append(out, 0, 0, 0, 1)
		out = append(out, nalu...)
	}
	return out
}

// TestReceiverReassemblesMultiPESAU drives the real production path —
// psmux.Muxer chunking, the TCP media packetizer (RFC 4571 framing), and the
// Receiver's framing/jitter/demux stages — and requires the emitted AU to be
// byte-complete.
func TestReceiverReassemblesMultiPESAU(t *testing.T) {
	sps, pps, idr := largeH264Stream()
	mux := psmux.New()
	mux.SetVideoCodec("h264")

	idrPTS := int64(3600)
	idrPS := mux.WriteAU(annexBJoin(sps, pps, idr), idrPTS, true)
	pSlice := pseudoRandomNALU(0x41, 0x88, 8000, 11)
	pPS := func(pts int64) []byte { return mux.WriteAU(annexBJoin(pSlice), pts, false) }

	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	pktr := psmux.NewRTPPacketizerTCP(client, 0x11223344, 1000)

	sendErr := make(chan error, 1)
	go func() {
		var err error
		for g := 0; g < 2 && err == nil; g++ {
			base := int64(g * 11 * 3600)
			if err = pktr.Send(idrPS, idrPTS+base); err != nil {
				break
			}
			for j := 1; j <= 10; j++ {
				if err = pktr.Send(pPS(idrPTS+base+int64(j)*3600), idrPTS+base+int64(j)*3600); err != nil {
					break
				}
			}
		}
		_ = client.Close() // signals the reader loop to stop
		sendErr <- err
	}()

	recv := NewReceiver("test-cam", nil, nil)
	recv.conn = server
	recv.isTCP.Store(true)

	type emittedAU struct {
		size  int
		isIDR bool
		pts   int64
	}
	var aus []emittedAU
	recv.AUCallback = func(au [][]byte, ptsTicks int64, isIDR bool) {
		total := 0
		for _, n := range au {
			total += len(n)
		}
		aus = append(aus, emittedAU{total, isIDR, ptsTicks})
	}

	buf := make([]byte, rtpReadBufSize)
	for {
		n, err := recv.readTCP(buf)
		if err != nil {
			break // sender closed the pipe
		}
		var pkt rtp.Packet
		require.NoError(t, pkt.Unmarshal(buf[:n]))
		pkt.Payload = append([]byte(nil), pkt.Payload...)
		require.NoError(t, recv.feedJitterBuffer(&pkt))
	}
	require.NoError(t, <-sendErr)

	// 2 GOPs of (IDR + 10 P). Every AU must carry its own timestamp — no
	// doubled frames, no leftovers spliced from the previous AU.
	wantAU := 22
	require.Len(t, aus, wantAU, "emitted AUs")
	for i, au := range aus {
		if au.isIDR {
			require.Equal(t, len(sps)+len(pps)+len(idr), au.size,
				"AU[%d]: IDR must reassemble byte-complete, not truncate at the first PES chunk", i)
		} else {
			require.Equal(t, len(pSlice), au.size, "AU[%d]: P frame size", i)
		}
		require.Equal(t, idrPTS+int64(i)*3600, au.pts, "AU[%d]: pts", i)
	}
	require.True(t, aus[0].isIDR && aus[11].isIDR, "GOP headers must be flagged IDR")
	require.Equal(t, int64(wantAU), recv.auEmitted.Load())
}

// TestReceiverPacketLossDropsHoledAU verifies UDP-loss handling: when a
// sequence gap holes a large AU mid-burst, the receiver must DROP the partial
// AU — never emit a frame truncated at the last completed PES, and never
// splice the pending PES into the next burst's AU.
func TestReceiverPacketLossDropsHoledAU(t *testing.T) {
	sps, pps, idr := largeH264Stream()
	mux := psmux.New()
	mux.SetVideoCodec("h264")

	idrPTS := int64(3600)
	idrPS := mux.WriteAU(annexBJoin(sps, pps, idr), idrPTS, true)
	pSlice := pseudoRandomNALU(0x41, 0x88, 8000, 11)

	fragment := func(ps []byte, seq uint16, ts int64) []*rtp.Packet {
		var pkts []*rtp.Packet
		for off := 0; off < len(ps); off += psmux.RTPMTU {
			end := off + psmux.RTPMTU
			if end > len(ps) {
				end = len(ps)
			}
			pkts = append(pkts, &rtp.Packet{
				Header: rtp.Header{
					SequenceNumber: seq + uint16(len(pkts)),
					Timestamp:      uint32(ts),
					SSRC:           0x11223344,
					Marker:         end == len(ps),
				},
				Payload: append([]byte(nil), ps[off:end]...),
			})
		}
		return pkts
	}

	recv := NewReceiver("test-cam", nil, nil)
	var idrEmitted, pEmitted int
	recv.AUCallback = func(au [][]byte, ptsTicks int64, isIDR bool) {
		if isIDR {
			idrEmitted++
			return
		}
		total := 0
		for _, n := range au {
			total += len(n)
		}
		require.Equal(t, len(pSlice), total, "P frame after loss must be clean")
		require.Equal(t, pEmitted*3600+7200, int(ptsTicks), "P frame pts")
		pEmitted++
	}

	feed := func(pkts []*rtp.Packet) {
		for _, p := range pkts {
			require.NoError(t, recv.feedJitterBuffer(p))
		}
	}

	// Drop one packet from the middle of the large IDR burst.
	idrPkts := fragment(idrPS, 1000, idrPTS)
	require.Greater(t, len(idrPkts), 40, "IDR burst must span many packets")
	for i, p := range idrPkts {
		if i == 40 {
			continue // lost on the wire
		}
		require.NoError(t, recv.feedJitterBuffer(p))
	}

	// Following P frames must arrive intact and on time.
	nextSeq := uint16(1000 + len(idrPkts))
	for j := 1; j <= 3; j++ {
		pts := idrPTS + int64(j)*3600
		pkts := fragment(mux.WriteAU(annexBJoin(pSlice), pts, false), nextSeq, pts)
		nextSeq += uint16(len(pkts))
		feed(pkts)
	}

	require.Zero(t, idrEmitted,
		"a holed IDR must be dropped, never emitted truncated at a PES chunk")
	require.Equal(t, 3, pEmitted, "stream must resync cleanly at the next AU")
	require.Positive(t, int(recv.gapSkippedPackets()), "the gap must be accounted as loss")
}
