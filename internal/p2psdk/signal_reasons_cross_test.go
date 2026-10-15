package peer

import (
	"errors"
	"fmt"
	"testing"

	"github.com/gorilla/websocket"
)

// serverAuthCloseReasons is the source-of-truth copy of the signal server's
// auth-class hello rejection reasons (signaling-server repo, internal/signal/
// handler.go: classifyTokenRejection → token_invalid / token_expired /
// token_replayed; verifyHello → authentication_failed / subject_revoked).
// The SDK cannot import the internal package, so this test pins the pairing:
// when the server adds a reason, add it HERE AND to authclose.go — CI fails
// only if the two lists within this repo drift apart; the server-side drift
// is caught by reviewing this file's comment against handler.go in the same
// repository (single-repo colocated review).
var serverAuthCloseReasons = []string{
	"token_invalid",
	"token_expired",
	"token_replayed",
	"authentication_failed",
	"subject_revoked",
}

// serverMintableAuthCloseReasons: reasons a freshly minted token can fix —
// these keep the ladder alive only while a TokenFunc can re-mint.
var serverMintableAuthCloseReasons = []string{
	"token_expired",
	"token_replayed",
}

func closeErr(reason string) error {
	return &websocket.CloseError{Code: websocket.ClosePolicyViolation, Text: reason}
}

// TestSDK_AuthCloseReasonsCoverServerSet: every auth-class reason the server
// can send must classify via classifyCloseError — the 2026-10-14 #66 gap let
// token_invalid/token_replayed loop forever.
func TestSDK_AuthCloseReasonsCoverServerSet(t *testing.T) {
	for _, reason := range serverAuthCloseReasons {
		if classifyCloseError(closeErr(reason)) == authCloseNone {
			t.Errorf("server auth reason %q must classify as auth-class", reason)
		}
		if !IsAuthClassClose(reason) {
			t.Errorf("server auth reason %q must classify via IsAuthClassClose", reason)
		}
	}
	for _, reason := range serverMintableAuthCloseReasons {
		if classifyCloseError(closeErr(reason)) != authCloseMintable {
			t.Errorf("reason %q must be mintable, not terminal", reason)
		}
	}
}

// Non-auth close reasons must NOT abandon the ladder (network blips etc.
// stay retryable).
func TestSDK_NonAuthCloseReasonsStayRetryable(t *testing.T) {
	for _, reason := range []string{
		"protocol_error",
		"first_message_not_hello",
		"malformed_resume",
		"resume_session_unknown",
		"replaced",
		"too_many_failed_handshakes",
	} {
		if classifyCloseError(closeErr(reason)) != authCloseNone {
			t.Errorf("non-auth reason %q must stay retryable", reason)
		}
	}
	if classifyCloseError(nil) != authCloseNone ||
		classifyCloseError(errors.New("dial tcp: timeout")) != authCloseNone {
		t.Error("nil/network errors must stay retryable")
	}
	// Gorilla surfaces close reasons via errors.As; wrapped forms classify too.
	if classifyCloseError(fmt.Errorf("read welcome: %w", closeErr("token_replayed"))) != authCloseMintable {
		t.Error("wrapped close error must still classify")
	}
}
