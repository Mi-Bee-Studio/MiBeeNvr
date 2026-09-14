package substream

// AcquireTarget tests (#451 cascade main-stream activation): an explicitly
// resolved target must start a pull WITHOUT consulting the resolver, under
// a caller-chosen key that coexists with (not clobbers) the resolver-based
// sub-stream entry of the same camera.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func waitInvited(t *testing.T, puller *fakeGBPuller) {
	t.Helper()
	require.Eventually(t, func() bool {
		puller.mu.Lock()
		defer puller.mu.Unlock()
		return puller.onAU != nil
	}, 3*time.Second, 10*time.Millisecond, "GB session must come up")
}

func pullerReleased(puller *fakeGBPuller) int {
	puller.mu.Lock()
	defer puller.mu.Unlock()
	return len(puller.released)
}

func TestAcquireTargetBypassesResolver(t *testing.T) {
	m := NewManager(Config{
		// The resolver would refuse everything — the explicit target must win.
		Resolver: func(ctx context.Context, cameraID string) (Target, bool, error) {
			return Target{}, false, errors.New("resolver must not be consulted")
		},
		IdleTimeout: 100 * time.Millisecond,
	})
	puller := &fakeGBPuller{}
	m.SetGBPuller(puller)
	t.Cleanup(m.Stop)

	main := Target{Kind: KindGB28181, GBDeviceID: "dev-1", GBChannelID: "ch-main"}
	acq := make(chan acqRes, 2)
	go func() {
		s, e := m.AcquireTarget(context.Background(), "cascade-main:cam-1", main)
		acq <- acqRes{s, e}
	}()
	waitInvited(t, puller)
	puller.feed(h264ParamAU, 3600, true)

	r := <-acq
	require.NoError(t, r.err)
	require.NotNil(t, r.src.Hub())

	puller.mu.Lock()
	invited := append([]string(nil), puller.invited...)
	puller.mu.Unlock()
	require.Equal(t, []string{"dev-1/ch-main"}, invited,
		"the puller must be invited for the explicit target, not the resolver's")

	// A same-key acquire joins the existing entry — no second invite.
	go func() {
		s, e := m.AcquireTarget(context.Background(), "cascade-main:cam-1", main)
		acq <- acqRes{s, e}
	}()
	r = <-acq
	require.NoError(t, r.err)
	puller.mu.Lock()
	invited = append([]string(nil), puller.invited...)
	puller.mu.Unlock()
	require.Len(t, invited, 1, "second acquire must join, not re-invite")

	// Releasing both references retires the pull after the idle timeout.
	m.Release("cascade-main:cam-1")
	m.Release("cascade-main:cam-1")
	require.Eventually(t, func() bool { return pullerReleased(puller) == 1 },
		3*time.Second, 20*time.Millisecond, "release must BYE the invite")
}

func TestAcquireTargetKeyIsolatedFromResolverEntries(t *testing.T) {
	m := NewManager(Config{
		Resolver: func(ctx context.Context, cameraID string) (Target, bool, error) {
			return Target{Kind: KindGB28181, GBDeviceID: "dev-1", GBChannelID: "ch-sub"}, true, nil
		},
		IdleTimeout: 100 * time.Millisecond,
	})
	puller := &fakeGBPuller{}
	m.SetGBPuller(puller)
	t.Cleanup(m.Stop)

	acq := make(chan acqRes, 2)
	go func() {
		s, e := m.Acquire(context.Background(), "cam-1")
		acq <- acqRes{s, e}
	}()
	waitInvited(t, puller)
	puller.feed(h264ParamAU, 3600, true)
	r := <-acq
	require.NoError(t, r.err)

	go func() {
		s, e := m.AcquireTarget(context.Background(), "cascade-main:cam-1",
			Target{Kind: KindGB28181, GBDeviceID: "dev-1", GBChannelID: "ch-main"})
		acq <- acqRes{s, e}
	}()
	// The fake keeps ONE onAU (latest invite wins) — wait for BOTH invites
	// before feeding, else the frame reaches the sub pull's callback.
	require.Eventually(t, func() bool {
		puller.mu.Lock()
		defer puller.mu.Unlock()
		return len(puller.invited) == 2
	}, 3*time.Second, 10*time.Millisecond, "both pulls must come up")
	puller.feed(h264ParamAU, 3600, true)
	r = <-acq
	require.NoError(t, r.err)

	puller.mu.Lock()
	invited := append([]string(nil), puller.invited...)
	puller.mu.Unlock()
	require.Equal(t, []string{"dev-1/ch-sub", "dev-1/ch-main"}, invited,
		"sub and main entries must coexist as separate pulls")

	// Retiring the main entry must not touch the sub pull.
	m.Release("cascade-main:cam-1")
	require.Eventually(t, func() bool { return pullerReleased(puller) == 1 },
		3*time.Second, 20*time.Millisecond, "main entry must retire after idle")
	require.Equal(t, 1, pullerReleased(puller), "the sub pull must stay up")
}

func TestAcquireTargetRejectsInvalidInput(t *testing.T) {
	m := NewManager(Config{Resolver: func(context.Context, string) (Target, bool, error) {
		return Target{}, false, nil
	}})
	t.Cleanup(m.Stop)

	_, err := m.AcquireTarget(context.Background(), "", Target{Kind: KindGB28181})
	require.ErrorIs(t, err, ErrNoSubStream)

	_, err = m.AcquireTarget(context.Background(), "k", Target{})
	require.ErrorIs(t, err, ErrNoSubStream, "an empty target has nothing to pull")
}
