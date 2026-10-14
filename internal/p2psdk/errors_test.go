package peer

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSentinelErrors_Available verifies that all public sentinel errors are
// non-nil and carry a human-readable message. These are the contract callers
// use with errors.Is to branch on failure cause without string matching.
func TestSentinelErrors_Available(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"ErrInvalidConfig", ErrInvalidConfig},
		{"ErrAuthFailed", ErrAuthFailed},
		{"ErrSignalingDisconnected", ErrSignalingDisconnected},
		{"ErrICETimeout", ErrICETimeout},
		{"ErrRelayUnavailable", ErrRelayUnavailable},
		{"ErrSessionNotFound", ErrSessionNotFound},
		{"ErrProtocolMismatch", ErrProtocolMismatch},
		{"ErrDirectICEConnected", ErrDirectICEConnected},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Error(t, tc.err, "sentinel must be non-nil")
			assert.NotEmpty(t, tc.err.Error(), "must carry a message")
		})
	}
}

// TestSentinelErrors_Distinct verifies sentinels are distinguishable via
// errors.Is — wrapping one must not match another.
func TestSentinelErrors_Distinct(t *testing.T) {
	all := []error{
		ErrInvalidConfig, ErrAuthFailed, ErrSignalingDisconnected,
		ErrICETimeout, ErrRelayUnavailable, ErrSessionNotFound,
		ErrProtocolMismatch, ErrDirectICEConnected,
	}
	for ti, target := range all {
		for wi, wrapped := range all {
			if ti == wi {
				continue
			}
			assert.Falsef(t, errors.Is(wrapped, target),
				"errors.Is(%v, %v) should be false — sentinels must be distinct",
				wrapped, target)
		}
	}
}

// TestSentinelErrors_WrappingPreservesIdentity verifies the standard %w
// wrapping pattern keeps the sentinel matchable via errors.Is. This is the
// guarantee callers rely on: internal code wraps a sentinel with context, the
// caller still matches the root cause.
func TestSentinelErrors_WrappingPreservesIdentity(t *testing.T) {
	wrapped := fmt.Errorf("dial failed: server returned 401: %w", ErrAuthFailed)
	assert.True(t, errors.Is(wrapped, ErrAuthFailed),
		"wrapped ErrAuthFailed must still match via errors.Is")

	wrapped2 := fmt.Errorf("ice agent timed out after 15s: %w", ErrICETimeout)
	assert.True(t, errors.Is(wrapped2, ErrICETimeout))
}

// TestDialValidation_TypedErrors verifies Dial returns ErrInvalidConfig (not a
// bare errors.New string) for each missing required field. Callers should be
// able to do errors.Is(err, peer.ErrInvalidConfig) without string matching.
func TestDialValidation_TypedErrors(t *testing.T) {
	cases := []struct {
		name string
		cfg  SignalingConfig
	}{
		{"missing url", SignalingConfig{JWT: "j", ProductID: "p", PeerID: "p", Role: "d"}},
		{"missing jwt", SignalingConfig{ServerURL: "ws://x", ProductID: "p", PeerID: "p", Role: "d"}},
		{"missing product", SignalingConfig{ServerURL: "ws://x", JWT: "j", PeerID: "p", Role: "d"}},
		{"missing peer", SignalingConfig{ServerURL: "ws://x", JWT: "j", ProductID: "p", Role: "d"}},
		{"missing role", SignalingConfig{ServerURL: "ws://x", JWT: "j", ProductID: "p", PeerID: "p"}},
		{
			"invalid role",
			SignalingConfig{ServerURL: "ws://x", JWT: "j", ProductID: "p", PeerID: "p", Role: "server"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Dial(t.Context(), tc.cfg)
			require.Error(t, err)
			assert.Truef(t, errors.Is(err, ErrInvalidConfig),
				"expected ErrInvalidConfig, got %v", err)
		})
	}
}
