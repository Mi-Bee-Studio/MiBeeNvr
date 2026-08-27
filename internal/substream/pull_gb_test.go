package substream

// GB pull-path tests (#560): a fake GBPuller drives the KindGB28181 target
// through the Manager's acquire/ready/broadcast contract — codec detection
// from in-band parameter sets, monotonic rebased timestamps, stall-triggered
// reconnect, and release on recycle.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// fakeGBPuller simulates the SIP side: registers the channel, hands the test
// the AU callback, and records releases.
type fakeGBPuller struct {
	mu        sync.Mutex
	ensured   []string
	invited   []string
	released  []string
	onAU      func(au [][]byte, ptsTicks int64, isIDR bool)
	inviteErr error
}

func (f *fakeGBPuller) EnsureSubChannelRegistered(deviceID, channelID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensured = append(f.ensured, deviceID+"/"+channelID)
	return nil
}

func (f *fakeGBPuller) InviteSubChannel(deviceID, channelID string, onAU func(au [][]byte, ptsTicks int64, isIDR bool)) (func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inviteErr != nil {
		return nil, f.inviteErr
	}
	f.invited = append(f.invited, deviceID+"/"+channelID)
	f.onAU = onAU
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.released = append(f.released, deviceID+"/"+channelID)
	}, nil
}

func (f *fakeGBPuller) feed(au [][]byte, pts int64, idr bool) {
	f.mu.Lock()
	cb := f.onAU
	f.mu.Unlock()
	if cb != nil {
		cb(au, pts, idr)
	}
}

// h264SPS/PPS NAL headers (type 7/8) + an IDR slice (type 5).
var h264ParamAU = [][]byte{{0x67, 0x64, 0x00, 0x1e}, {0x68, 0xee, 0x3c, 0x80}, {0x65, 0x01, 0x02, 0x03}}

// h265 VPS/SPS/PPS (types 32/33/34) + an IDR slice (type 19).
var h265ParamAU = [][]byte{{0x40, 0x01}, {0x42, 0x01}, {0x44, 0x01}, {0x26, 0x01, 0x02}}

func TestAcquireGB_H264ReadyAndFrames(t *testing.T) {
	t.Helper()
	puller := &fakeGBPuller{}
	m := NewManager(Config{
		Resolver: func(ctx context.Context, cameraID string) (Target, bool, error) {
			return Target{Kind: KindGB28181, GBDeviceID: "dev-1", GBChannelID: "ch-sub"}, true, nil
		},
		ReadyTimeout:      2 * time.Second,
		FrameStallTimeout: 5 * time.Second,
	})
	m.SetGBPuller(puller)
	t.Cleanup(m.Stop)

	acq := make(chan acqRes, 1)
	go func() {
		s, e := m.Acquire(context.Background(), "cam-gb")
		acq <- acqRes{s, e}
	}()
	waitFeeder := func() {
		require.Eventually(t, func() bool {
			puller.mu.Lock()
			defer puller.mu.Unlock()
			return puller.onAU != nil
		}, 2*time.Second, 10*time.Millisecond, "GB session must come up")
	}
	waitFeeder()

	// A slice-only AU must not make the source ready (codec is unknowable
	// from the GB SDP answer; slice NALUs never decide — see detectCodecGB).
	puller.feed([][]byte{{0x41, 0x01}}, 1000, false)
	select {
	case r := <-acq:
		t.Fatalf("slice-only AU must not unblock Acquire (params=%v)", r.src != nil && r.src.params.Load() != nil)
	case <-time.After(150 * time.Millisecond):
	}

	puller.feed(h264ParamAU, 3600, true)
	r := <-acq
	require.NoError(t, r.err)
	src := r.src
	require.Eventually(t, func() bool {
		codec, sps, pps, vps := src.CodecParams()
		return codec == model.FormatH264 && len(sps) > 0 && len(pps) > 0 && vps == nil
	}, 2*time.Second, 20*time.Millisecond)

	// Frames broadcast with rebased monotonic timestamps.
	got := make(chan int64, 4)
	subErr := src.Hub().Subscribe("test-sub", func(pts int64, au [][]byte) {
		select {
		case got <- pts:
		default:
		}
	})
	require.NoError(t, subErr)
	puller.feed(h264ParamAU, 7200, false)
	select {
	case pts := <-got:
		require.Greater(t, pts, int64(0))
	case <-time.After(time.Second):
		t.Fatal("no broadcast after feed")
	}
	src.Hub().Unsubscribe("test-sub")

	m.Release("cam-gb")
	require.Equal(t, []string{"dev-1/ch-sub"}, puller.invited)
}

func TestAcquireGB_H265CodecDetection(t *testing.T) {
	t.Helper()
	puller := &fakeGBPuller{}
	m := NewManager(Config{
		Resolver: func(ctx context.Context, cameraID string) (Target, bool, error) {
			return Target{Kind: KindGB28181, GBDeviceID: "dev-2", GBChannelID: "ch-sub2"}, true, nil
		},
		ReadyTimeout:      2 * time.Second,
		FrameStallTimeout: 5 * time.Second,
	})
	m.SetGBPuller(puller)
	t.Cleanup(m.Stop)

	acq2 := make(chan acqRes, 1)
	go func() {
		s, e := m.Acquire(context.Background(), "cam-gb265")
		acq2 <- acqRes{s, e}
	}()
	require.Eventually(t, func() bool {
		puller.mu.Lock()
		defer puller.mu.Unlock()
		return puller.onAU != nil
	}, 2*time.Second, 10*time.Millisecond)
	puller.feed(h265ParamAU, 9000, true)
	r2 := <-acq2
	require.NoError(t, r2.err)
	src := r2.src
	require.Eventually(t, func() bool {
		codec, _, _, vps := src.CodecParams()
		return codec == model.FormatH265 && len(vps) > 0
	}, 2*time.Second, 20*time.Millisecond)
	m.Release("cam-gb265")
}

func TestAcquireGB_StallReconnects(t *testing.T) {
	t.Helper()
	puller := &fakeGBPuller{}
	m := NewManager(Config{
		Resolver: func(ctx context.Context, cameraID string) (Target, bool, error) {
			return Target{Kind: KindGB28181, GBDeviceID: "dev-3", GBChannelID: "ch-sub3"}, true, nil
		},
		ReadyTimeout:      2 * time.Second,
		FrameStallTimeout: 300 * time.Millisecond,
	})
	m.SetGBPuller(puller)
	t.Cleanup(m.Stop)

	acq3 := make(chan acqRes, 1)
	go func() {
		s, e := m.Acquire(context.Background(), "cam-gb-stall")
		acq3 <- acqRes{s, e}
	}()
	require.Eventually(t, func() bool {
		puller.mu.Lock()
		defer puller.mu.Unlock()
		return puller.onAU != nil
	}, 2*time.Second, 10*time.Millisecond)
	puller.feed(h264ParamAU, 1000, true)
	r3 := <-acq3
	require.NoError(t, r3.err)
	src := r3.src
	require.Eventually(t, func() bool {
		codec, _, _, _ := src.CodecParams()
		return codec == model.FormatH264
	}, 2*time.Second, 20*time.Millisecond)

	// Silence: the stall watchdog must recycle (release) and the reconnect
	// loop must re-INVITE.
	require.Eventually(t, func() bool {
		puller.mu.Lock()
		defer puller.mu.Unlock()
		return len(puller.released) >= 1 && len(puller.invited) >= 2
	}, 4*time.Second, 50*time.Millisecond, "stall must BYE and re-INVITE")

	m.Release("cam-gb-stall")
}

func TestAcquireGB_PullerNotWired(t *testing.T) {
	t.Helper()
	m := NewManager(Config{
		Resolver: func(ctx context.Context, cameraID string) (Target, bool, error) {
			return Target{Kind: KindGB28181, GBDeviceID: "dev-4", GBChannelID: "ch-sub4"}, true, nil
		},
		ReadyTimeout: 300 * time.Millisecond,
	})
	t.Cleanup(m.Stop)

	_, err := m.Acquire(context.Background(), "cam-gb-nowire")
	require.ErrorIs(t, err, ErrNotReady)
}

// acqRes carries an asynchronous Acquire result.
type acqRes struct {
	src *Source
	err error
}

func TestDetectCodecGB(t *testing.T) {
	t.Helper()
	require.Equal(t, model.FormatH264, detectCodecGB(h264ParamAU))
	require.Equal(t, model.FormatH265, detectCodecGB(h265ParamAU))
	// H.264 slice (0x41/0x45) — collides with H.265 param slots under the
	// 6-bit shift; must stay ambiguous, never decide.
	require.Equal(t, model.Format(""), detectCodecGB([][]byte{{0x41, 0x01}, {0x45, 0x02}}))
	require.Equal(t, model.Format(""), detectCodecGB([][]byte{{}}))
	require.Equal(t, model.Format(""), detectCodecGB(nil))
}
