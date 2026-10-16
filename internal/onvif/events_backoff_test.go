package onvif

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	onvifgo "github.com/mickeyzzc/onvif-go/v2/onvif"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTransientTransportErrorClassification pins the error-class split that
// keeps the resubscribe backoff SHORT: dial-phase network failures recover on
// their own once the link returns, while credential/protocol faults must keep
// the escalating backoff.
func TestTransientTransportErrorClassification(t *testing.T) {
	transient := []error{
		syscall.ENETUNREACH,
		syscall.EHOSTUNREACH,
		syscall.ECONNREFUSED,
		syscall.ENETDOWN,
		errors.New("dial tcp 192.168.62.104:8080: connect: network is unreachable"),
		errors.New("Post \"http://192.168.62.104:8080/onvif/events_service\": dial tcp: connection refused"),
		errors.New("read tcp 10.0.0.2:1->10.0.0.1:8080: read: connection reset by peer"),
		errors.New("unexpected EOF"),
		&netTimeoutError{},
	}
	for _, err := range transient {
		assert.True(t, transientTransportError(err), "%v must classify transient", err)
	}

	permanent := []error{
		errors.New("soap: Sender not authorized"),
		ErrEventsNotSupported,
		errors.New("invalid subscription reference"),
		context.Canceled,
	}
	for _, err := range permanent {
		assert.False(t, transientTransportError(err), "%v must NOT classify transient", err)
	}
}

type netTimeoutError struct{}

func (*netTimeoutError) Error() string   { return "i/o timeout" }
func (*netTimeoutError) Timeout() bool   { return true }
func (*netTimeoutError) Temporary() bool { return true }

// TestSubscriberRecoversQuicklyFromConsecutiveTransientFailures: several
// back-to-back ENETUNREACH rebuilds (WiFi flap) must NOT escalate the backoff
// — retries stay at the short floor, so the subscription recovers within
// milliseconds of the link returning instead of the observed 7+ minute stall.
func TestSubscriberRecoversQuicklyFromConsecutiveTransientFailures(t *testing.T) {
	var mu sync.Mutex
	var failAttempts []time.Time
	var attempts atomic.Int32
	var recoveredAt time.Time

	mock := &mockEventClient{
		// Attempt 1 (initial Subscribe) succeeds; attempts 2-5 are the WiFi
		// flap (transient dial failures); attempt 6+ recovers.
		createPullPointFn: func(ctx context.Context, filter string, tt *time.Duration, policy string) (*onvifgo.PullPointSubscription, error) {
			n := attempts.Add(1)
			if n >= 2 && n <= 5 {
				mu.Lock()
				failAttempts = append(failAttempts, time.Now())
				mu.Unlock()
				return nil, &netOpError{msg: "dial tcp 192.168.62.104:8080: connect: network is unreachable"}
			}
			if n >= 6 {
				mu.Lock()
				if recoveredAt.IsZero() {
					recoveredAt = time.Now()
				}
				mu.Unlock()
			}
			return &onvifgo.PullPointSubscription{
				SubscriptionReference: "http://mock/sub/recovered",
				CurrentTime:           time.Now(),
				TerminationTime:       time.Now().Add(10 * time.Minute),
			}, nil
		},
		pullMessagesFn: func(ctx context.Context, ref string, timeout time.Duration, limit int) ([]onvifgo.NotificationMessage, error) {
			// Terminal condition forces the rebuild path immediately.
			return nil, errors.New("SubscriptionReferenceDereferenced")
		},
		renewFn: func(ctx context.Context, ref string, d time.Duration) (time.Time, time.Time, error) {
			return time.Now(), time.Now().Add(10 * time.Minute), nil
		},
	}

	es := helperNewEventSubscriberWithMock(mock,
		WithPollInterval(40*time.Millisecond),
		withResubscribeBackoff(40*time.Millisecond), // would double to 80/160/320ms if escalation applied
	)
	require.NoError(t, es.Subscribe(context.Background(), "flap-cam"))
	t.Cleanup(func() { _ = es.Unsubscribe(context.Background(), "flap-cam") })

	// Wait for the full arc: initial subscribe → terminal poll → 4 transient
	// rebuild failures → recovery (attempt 6). Waiting on Active alone would
	// pass immediately on the initial subscription.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && attempts.Load() < 6 {
		time.Sleep(10 * time.Millisecond)
	}
	require.GreaterOrEqual(t, attempts.Load(), int32(6), "recovery attempt must have run")
	require.Eventually(t, func() bool { return es.Status("flap-cam").State == StateActive },
		3*time.Second, 20*time.Millisecond, "must be active after the flap ends")

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, failAttempts, 4, "four transient rebuild failures expected")
	require.False(t, recoveredAt.IsZero(), "recovery attempt must have happened")
	// No-escalation bound: total dark time across 4 failures stays near
	// 4×floor (160ms); with doubling it would exceed 4×floor comfortably.
	dark := recoveredAt.Sub(failAttempts[0])
	assert.Less(t, dark, time.Second,
		"consecutive transient failures must not escalate the backoff (dark=%v)", dark)
}

type netOpError struct{ msg string }

func (e *netOpError) Error() string { return e.msg }
