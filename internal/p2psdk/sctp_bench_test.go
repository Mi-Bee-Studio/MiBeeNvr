package peer

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"
)

// benchPair builds a bare PC pair with optional SCTP knob overrides taken
// from the environment, mirroring how the fleet A/B experiment will run.
func benchPair(t *testing.T) (*webrtc.PeerConnection, *webrtc.DataChannel, chan struct{}, *webrtc.PeerConnection) {
	t.Helper()
	se := webrtc.SettingEngine{}
	if v := os.Getenv("BENCH_MIN_CWND"); v != "" {
		n, err := strconv.Atoi(v)
		require.NoError(t, err)
		se.SetSCTPMinCwnd(uint32(n))
	}
	if v := os.Getenv("BENCH_FAST_RTX"); v != "" {
		n, err := strconv.Atoi(v)
		require.NoError(t, err)
		se.SetSCTPFastRtxWnd(uint32(n))
	}
	if v := os.Getenv("BENCH_CA_STEP"); v != "" {
		n, err := strconv.Atoi(v)
		require.NoError(t, err)
		se.SetSCTPCwndCAStep(uint32(n))
	}
	if v := os.Getenv("BENCH_RTO_MAX_MS"); v != "" {
		n, err := strconv.Atoi(v)
		require.NoError(t, err)
		se.SetSCTPRTOMax(time.Duration(n) * time.Millisecond)
	}
	api := webrtc.NewAPI(webrtc.WithSettingEngine(se))

	pcOffer, err := api.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pcOffer.Close() })
	pcAnswer, err := api.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pcAnswer.Close() })

	// The data channel must exist before CreateOffer, otherwise the offer
	// carries no m-line and SetRemoteDescription rejects it (no ice-ufrag).
	dc, err := pcOffer.CreateDataChannel("bulk", nil)
	require.NoError(t, err)
	opened := make(chan struct{})
	dc.OnOpen(func() { close(opened) })

	offer, err := pcOffer.CreateOffer(nil)
	require.NoError(t, err)
	require.NoError(t, pcOffer.SetLocalDescription(offer))
	<-webrtc.GatheringCompletePromise(pcOffer)
	require.NoError(t, pcAnswer.SetRemoteDescription(*pcOffer.LocalDescription()))
	answer, err := pcAnswer.CreateAnswer(nil)
	require.NoError(t, err)
	require.NoError(t, pcAnswer.SetLocalDescription(answer))
	<-webrtc.GatheringCompletePromise(pcAnswer)
	require.NoError(t, pcOffer.SetRemoteDescription(*pcAnswer.LocalDescription()))
	return pcOffer, dc, opened, pcAnswer
}

// benchLeg sends frames×size on dc and returns wall duration plus the
// number of >=100ms gaps between application-level frame arrivals.
func benchLeg(t *testing.T, dc *webrtc.DataChannel, arrived chan time.Time, frames, size int) (time.Duration, int) {
	t.Helper()
	before := len(arrived)
	payload := make([]byte, size)
	start := time.Now()
	for range frames {
		require.NoError(t, dc.Send(payload))
	}
	want := before + frames
	deadline := time.After(60 * time.Second)
	for len(arrived) < want {
		select {
		case <-deadline:
			t.Fatalf("leg timed out: %d/%d frames", len(arrived), want)
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
	dur := time.Since(start)
	// drain the channel and count stalls among this leg's arrivals
	times := make([]time.Time, 0, frames)
	for len(times) < frames {
		times = append(times, <-arrived)
	}
	stalls := 0
	for i := 1; i < len(times); i++ {
		if times[i].Sub(times[i-1]) >= 100*time.Millisecond {
			stalls++
		}
	}
	return dur, stalls
}

// TestSCTPBenchColdWarm (#109): reproduce the fleet "20/80 Mbps bimodal
// down" locally. The fleet captures showed legs with 17-23 stalls of
// 100-500ms and clean legs in the SAME association — test whether the
// first bulk leg on a fresh association stalls while a warm second leg
// flies, and which SCTP knob removes the cold-leg stalls.
func TestSCTPBenchColdWarm(t *testing.T) {
	_, dc, opened, pcAnswer := benchPair(t)

	arrived := make(chan time.Time, 4096)
	answerDC := make(chan *webrtc.DataChannel, 1)
	pcAnswer.OnDataChannel(func(d *webrtc.DataChannel) {
		d.OnMessage(func(webrtc.DataChannelMessage) { arrived <- time.Now() })
		select {
		case answerDC <- d:
		default:
		}
	})
	select {
	case <-opened:
	case <-time.After(10 * time.Second):
		t.Fatal("offerer DC never opened")
	}
	select {
	case <-answerDC:
	case <-time.After(10 * time.Second):
		t.Fatal("answerer DC never arrived")
	}
	time.Sleep(200 * time.Millisecond) // let the answerer handler settle

	const frames, size = 200, 16 * 1024
	coldDur, coldStalls := benchLeg(t, dc, arrived, frames, size)
	warmDur, warmStalls := benchLeg(t, dc, arrived, frames, size)

	mbps := func(d time.Duration) float64 {
		return float64(frames*size) * 8 / d.Seconds() / 1e6
	}
	t.Logf("冷腿: %v (%.1f Mbps, %d 次 >=100ms 停顿) | 热腿: %v (%.1f Mbps, %d 次停顿) | 比值 %.1fx",
		coldDur, mbps(coldDur), coldStalls, warmDur, mbps(warmDur), warmStalls,
		float64(warmDur)/float64(max(coldDur, 1)))
}
