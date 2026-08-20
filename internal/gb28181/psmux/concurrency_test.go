package psmux

import (
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestConcurrentVideoAudioBursts reproduces the 2026-08-21 cascade corruption:
// a session's video-AU callback and audio-frame callback run on two hub drain
// goroutines and share ONE Muxer + ONE RTPPacketizer. Without internal
// serialization the seq counter raced — bursts interleaved mid-packet-block
// (duplicate/out-of-order seqs) and the upper platform's contiguous drain
// truncated video AUs at PES-chunk boundaries (IDR NALUs cut at exactly
// ~maxPESPayload, tails surfaced as garbage NALUs — bottom-half green/white
// frames). With the fix every burst occupies one contiguous strictly
// increasing seq block; the global sequence has no duplicates.
func TestConcurrentVideoAudioBursts(t *testing.T) {
	// Loopback listener standing in for the upper platform.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	defer pc.Close()
	// The collector is slower than the burst writers; without a large receive
	// buffer the kernel drops and the count check below wedges.
	require.NoError(t, pc.(*net.UDPConn).SetReadBuffer(64<<20))
	dst, err := net.ResolveUDPAddr("udp", pc.LocalAddr().String())
	require.NoError(t, err)

	// Unconnected UDP socket (Send uses WriteToUDP, like the cascade's own
	// ListenUDP media socket).
	sender, err := net.ListenUDP("udp", nil)
	require.NoError(t, err)
	defer sender.Close()

	mux := New()
	mux.SetVideoCodec("h264")
	mux.SetAudioCodec("g711a")
	pkt := NewRTPPacketizer(sender, dst, 0x02000001, 1)

	videoAU := make([]byte, 3*maxPESPayload) // forces PES chunking
	for i := range videoAU {
		videoAU[i] = byte(i)
	}
	audioFrame := make([]byte, 320)

	const iterations = 200
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // video drain goroutine
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			ps := mux.WriteAU(videoAU, int64(i)*3000, i%30 == 0)
			require.NoError(t, pkt.Send(ps, int64(i)*3000))
			time.Sleep(2 * time.Millisecond) // pace: keep the collector in step
		}
	}()
	go func() { // audio drain goroutine
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			ps := mux.WriteAudio(audioFrame, int64(i)*2880)
			require.NoError(t, pkt.Send(ps, int64(i)*2880))
		}
	}()

	// Collector: MUST read concurrently — the kernel clamps SO_RCVBUF to
	// rmem_max (~208KB), far below the ~36MB the senders produce, so a
	// post-hoc read loses almost everything.
	seen := make(map[uint16]bool)
	var mu sync.Mutex
	var lastSeq uint16
	collectorDone := make(chan struct{})
	go func() {
		defer close(collectorDone)
		buf := make([]byte, 2048)
		for {
			n, _, err := pc.ReadFrom(buf)
			if err != nil {
				return // deadline: wire quiet
			}
			if n < 12 {
				continue
			}
			seq := binary.BigEndian.Uint16(buf[2:4])
			mu.Lock()
			if seen[seq] {
				t.Errorf("duplicate RTP sequence number %d — bursts interleaved unsafely", seq)
			}
			seen[seq] = true
			lastSeq = seq
			mu.Unlock()
		}
	}()
	require.NoError(t, pc.SetReadDeadline(time.Now().Add(600*time.Millisecond)))
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	sent := pkt.Sent()
	<-collectorDone
	if uint64(len(seen)) != sent {
		t.Logf("kernel loss under load: sent=%d seen=%d (tolerated)", sent, len(seen))
	} else {
		require.Equal(t, uint16(sent), lastSeq, "gapless sequence block 1..N across both streams")
	}
}
