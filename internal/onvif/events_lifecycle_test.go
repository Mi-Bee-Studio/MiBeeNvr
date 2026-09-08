package onvif

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	onvifgo "github.com/mickeyzzc/onvif-go/v2/onvif"
	"github.com/stretchr/testify/require"
)

// motionMsg builds a MotionAlarm notification like the mibee_cam contract
// emits (Source=CSI, Data State + Score).
func motionMsg(state string, score string) onvifgo.NotificationMessage {
	return onvifgo.NotificationMessage{
		Topic: "tns1:VideoSource/MotionAlarm",
		Message: onvifgo.EventMessage{
			UtcTime: time.Now().UTC(),
			Source:  []onvifgo.SimpleItem{{Name: "Source", Value: "CSI"}},
			Data: []onvifgo.SimpleItem{
				{Name: "State", Value: state},
				{Name: "Score", Value: score},
			},
		},
	}
}

// TestSubscriberDeliversEventsAndRenewsShortGrants: a device granting a short
// term (mibee_cam grants 1h; 30min here to keep the test fast) must renew on
// a sane cadence — the pre-#711 code renewed when < SubscriptionRenewBefore
// (1h) remained, i.e. on EVERY poll tick for such devices.
func TestSubscriberDeliversEventsAndRenewsShortGrants(t *testing.T) {
	mock := &mockEventClient{
		createPullPointFn: func(ctx context.Context, filter string, tt *time.Duration, policy string) (*onvifgo.PullPointSubscription, error) {
			return &onvifgo.PullPointSubscription{
				SubscriptionReference: "http://mock/sub/1",
				CurrentTime:           time.Now(),
				// Short grant (250ms): the renew threshold (max(grant/2, 2×poll))
				// falls inside the test window, so renewal fires early.
				TerminationTime: time.Now().Add(250 * time.Millisecond),
			}, nil
		},
		pullMessagesFn: func(ctx context.Context, ref string, timeout time.Duration, limit int) ([]onvifgo.NotificationMessage, error) {
			return []onvifgo.NotificationMessage{motionMsg("true", "87")}, nil
		},
		renewFn: func(ctx context.Context, ref string, d time.Duration) (time.Time, time.Time, error) {
			// Renewal extends far out so it happens exactly once.
			return time.Now(), time.Now().Add(10 * time.Minute), nil
		},
	}

	var mu sync.Mutex
	var received []ONVIFEvent
	es := helperNewEventSubscriberWithMock(mock,
		WithEventCallback(func(evt ONVIFEvent) { mu.Lock(); received = append(received, evt); mu.Unlock() }),
		WithPollInterval(40*time.Millisecond),
	)

	require.NoError(t, es.Subscribe(context.Background(), "cam-1"))
	defer es.StopAll(context.Background())

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if mock.renewCalls.Load() >= 1 {
			mu.Lock()
			n := len(received)
			mu.Unlock()
			if n >= 2 {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.GreaterOrEqual(t, mock.renewCalls.Load(), int32(1),
		"short-granted subscription must renew when < half the grant remains")
	mu.Lock()
	require.GreaterOrEqual(t, len(received), 2, "events must flow to the callback")
	require.Equal(t, "tns1:VideoSource/MotionAlarm", received[0].Topic)
	require.Equal(t, "87", received[0].Data["Score"])
	mu.Unlock()

	st := es.Status("cam-1")
	require.True(t, st.Subscribed)
	require.Equal(t, StateActive, st.State)
	require.GreaterOrEqual(t, st.EventCount, int64(2))
	require.False(t, st.LastEventAt.IsZero())
}

// TestSubscriberResubscribesAfterTerminalPollFailures: a subscription whose
// PullMessages keep failing (expired / SubscriptionReferenceDereferenced /
// device reboot) must be REBUILT, not polled forever — the pre-#711 loop
// logged a warning per tick for eternity.
func TestSubscriberResubscribesAfterTerminalPollFailures(t *testing.T) {
	mock := &mockEventClient{
		pullMessagesFn: func(ctx context.Context, ref string, timeout time.Duration, limit int) ([]onvifgo.NotificationMessage, error) {
			return nil, errors.New("SubscriptionReferenceDereferenced")
		},
	}
	es := helperNewEventSubscriberWithMock(mock, WithPollInterval(20*time.Millisecond))
	require.NoError(t, es.Subscribe(context.Background(), "cam-1"))
	defer es.StopAll(context.Background())

	// 5 consecutive failures per cycle (terminalPollFailures) + rebuild.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if mock.subscribeCalls.Load() >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.GreaterOrEqual(t, mock.subscribeCalls.Load(), int32(2),
		"a dead subscription must be rebuilt, not polled forever")
	require.True(t, es.IsSubscribed("cam-1"))
}

// TestSubscriberUnsupportedTombstone: a device answering "Action Not
// Implemented" must not be re-probed by an internal retry loop.
func TestSubscriberUnsupportedTombstone(t *testing.T) {
	var calls int32
	mock := &mockEventClient{
		createPullPointFn: func(ctx context.Context, filter string, tt *time.Duration, policy string) (*onvifgo.PullPointSubscription, error) {
			calls++
			return nil, errors.New("SOAP fault: Action Not Implemented")
		},
	}
	es := helperNewEventSubscriberWithMock(mock, WithPollInterval(20*time.Millisecond))

	err := es.Subscribe(context.Background(), "cam-1")
	require.ErrorIs(t, err, ErrEventsNotSupported)
	require.False(t, es.IsSubscribed("cam-1"))

	time.Sleep(150 * time.Millisecond)
	require.Equal(t, int32(1), calls, "no background retry loop for unsupported devices")
	require.Equal(t, StateUnsupported, es.Status("cam-1").State)

	// A repeat Subscribe (reconcile path) no-ops on the tombstone.
	require.NoError(t, es.Subscribe(context.Background(), "cam-1"))
	require.Equal(t, int32(1), calls)
}

// TestSubscriberStatusReportsLastError: transient poll errors surface in the
// diagnostics without killing the subscription.
func TestSubscriberStatusReportsLastError(t *testing.T) {
	var fail bool
	var mu sync.Mutex
	mock := &mockEventClient{
		pullMessagesFn: func(ctx context.Context, ref string, timeout time.Duration, limit int) ([]onvifgo.NotificationMessage, error) {
			mu.Lock()
			f := fail
			mu.Unlock()
			if f {
				return nil, errors.New("temporary network hiccup")
			}
			return nil, nil
		},
	}
	es := helperNewEventSubscriberWithMock(mock, WithPollInterval(20*time.Millisecond))
	require.NoError(t, es.Subscribe(context.Background(), "cam-1"))
	defer es.StopAll(context.Background())

	mu.Lock()
	fail = true
	mu.Unlock()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if es.Status("cam-1").LastError != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.NotEmpty(t, es.Status("cam-1").LastError)
	require.True(t, es.IsSubscribed("cam-1"), "transient errors must not tear the subscription down")
	require.GreaterOrEqual(t, es.Status("cam-1").ConsecutivePollErrors, 1)
}

func TestRenewThreshold(t *testing.T) {
	// 24h grant → capped at SubscriptionRenewBefore (1h).
	require.Equal(t, SubscriptionRenewBefore, renewThreshold(24*time.Hour, 5*time.Second))
	// 1h grant (mibee_cam) → half: 30min.
	require.Equal(t, 30*time.Minute, renewThreshold(time.Hour, 5*time.Second))
	// Tiny grant → floored at 2× poll interval.
	require.Equal(t, 2*time.Second, renewThreshold(500*time.Millisecond, time.Second))
}

func TestWithPollIntervalExported(t *testing.T) {
	es := newEventSubscriber(nil, WithPollInterval(time.Second))
	require.Equal(t, time.Second, es.pollInterval)
	// Non-positive is ignored (default retained).
	es = newEventSubscriber(nil, WithPollInterval(0))
	require.Equal(t, DefaultPollInterval, es.pollInterval)
}
