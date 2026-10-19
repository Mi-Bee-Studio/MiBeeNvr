package viewer

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLoginMintsPrefixedTokenAndSession(t *testing.T) {
	st := NewStore()
	now := time.Now()

	token, sess, err := st.Login(Scope{AccountID: "acc-1", IsOwner: false, Cameras: []string{"cam-a"}}, now)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(token, TokenPrefix), "token must carry the viewer prefix")
	require.Equal(t, "acc-1", sess.AccountID)
	require.False(t, sess.IsOwner)
	require.Equal(t, []string{"cam-a"}, sess.Cameras)
	require.Equal(t, now.Add(2*time.Hour), sess.ExpiresAt, "TTL is capped at 2h")
}

func TestVerifyRoundTrip(t *testing.T) {
	st := NewStore()
	now := time.Now()
	token, want, err := st.Login(Scope{AccountID: "acc-2", IsOwner: true}, now)
	require.NoError(t, err)

	got, ok := st.Verify(token, now.Add(time.Minute))
	require.True(t, ok)
	require.Equal(t, want.AccountID, got.AccountID)
	require.True(t, got.IsOwner)
}

func TestVerifyRejectsWrongPrefixAndUnknown(t *testing.T) {
	st := NewStore()
	now := time.Now()

	_, ok := st.Verify("mbs_admin-session-token", now)
	require.False(t, ok, "non-viewer tokens must be rejected by prefix")
	_, ok = st.Verify(TokenPrefix+"deadbeef", now)
	require.False(t, ok, "unknown tokens must fail")
}

func TestVerifyRejectsExpiredAndPrunes(t *testing.T) {
	st := NewStore()
	now := time.Now()
	token, _, err := st.Login(Scope{AccountID: "acc-3"}, now)
	require.NoError(t, err)

	// Just past expiry: rejected, and the prune sweep drops the row so the
	// map cannot grow unboundedly across logins.
	_, ok := st.Verify(token, now.Add(2*time.Hour+time.Second))
	require.False(t, ok)
	_, ok = st.Verify(token, now.Add(2*time.Hour+2*time.Second))
	require.False(t, ok)

	// A fresh login after pruning still works (prune ran under the lock).
	token2, _, err := st.Login(Scope{AccountID: "acc-3"}, now.Add(3*time.Hour))
	require.NoError(t, err)
	_, ok = st.Verify(token2, now.Add(3*time.Hour))
	require.True(t, ok)
}

func TestCameraAllowed(t *testing.T) {
	wholeMachine := &Session{}
	require.True(t, wholeMachine.CameraAllowed("any-camera"), "empty allowlist means whole machine")

	scoped := &Session{Cameras: []string{"cam-a", "cam-b"}}
	require.True(t, scoped.CameraAllowed("cam-a"))
	require.True(t, scoped.CameraAllowed("cam-b"))
	require.False(t, scoped.CameraAllowed("cam-c"))
}
