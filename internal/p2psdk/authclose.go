package peer

// Auth-class close reasons (#66). The signaling server closes the hello
// handshake with a structured reason string (v1.md §2.3); these two sets
// classify the ones that mean "this credential is dead as-presented":
//
//   - terminal: no fresh credential can fix it — the subject is revoked,
//     the token is forged, or the server cannot even verify. The reconnect
//     ladder MUST stop (abandon) instead of hammering a verdict that can
//     never change — the 2026-10-14 NVR outage showed 23 minutes of a
//     device re-dialing every backoff step with the same dead JWT, feeding
//     the server's handshake failure counter (and getting its subject
//     banned by the too_many_failed_handshakes guard).
//   - mintable: the token itself was once good (signature verified) and a
//     freshly minted one will work — expired (TTL 1h) or single-use JTI
//     already consumed. With a SignalingConfig.TokenFunc the ladder keeps
//     going (re-minting per attempt, bounded); without one these are just
//     as terminal for this process.
//
// Keep in sync with internal/signal close reasons — pinned by the
// cross-assertion test in the server repo (TestSDKAuthClassReasonsCoverServer).
var (
	terminalAuthCloseReasons = map[string]bool{
		"authentication_failed":   true, // no verifier / server-side auth misconfig
		"subject_revoked":         true, // deprovisioned identity
		"token_invalid":           true, // forged / malformed (server's #32 wording)
		"invalid_token":           true, // legacy wording kept for old servers
		"unauthorized":            true, // generic authz denial
		"scope_resolution_failed": true, // tenant resolution rejected the claims
		"device_identity_failed":  true, // pubkey-bound device sent a bad hello signature
	}
	mintableAuthCloseReasons = map[string]bool{
		"token_expired":  true, // past TTL — re-mint and retry
		"token_replayed": true, // single-use JTI consumed — re-mint and retry
	}
)

// IsAuthClassClose reports whether a server close reason is
// authentication-class: the ladder should not keep presenting the same
// credential. Terminal or mintable.
func IsAuthClassClose(reason string) bool {
	return terminalAuthCloseReasons[reason] || mintableAuthCloseReasons[reason]
}

// IsMintableAuthClose reports whether a fresh token could fix this close
// reason (expired / single-use consumed). True implies IsAuthClassClose.
func IsMintableAuthClose(reason string) bool {
	return mintableAuthCloseReasons[reason]
}

// authCloseClass triages a live error from the signaling connection.
type authCloseClass int

const (
	authCloseNone authCloseClass = iota
	authCloseTerminal
	authCloseMintable
)

func classifyCloseError(err error) authCloseClass {
	_, reason, ok := CloseInfo(err)
	if !ok {
		return authCloseNone
	}
	if terminalAuthCloseReasons[reason] {
		return authCloseTerminal
	}
	if mintableAuthCloseReasons[reason] {
		return authCloseMintable
	}
	return authCloseNone
}
