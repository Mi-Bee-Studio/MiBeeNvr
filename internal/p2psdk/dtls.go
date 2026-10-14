// Package peer provides the MiBee P2P Go SDK.
package peer

import (
	"github.com/pion/dtls/v3"
	"github.com/pion/webrtc/v4"
)

// hardenedSettingEngine creates a webrtc.SettingEngine with hardened DTLS/SRTP
// configuration. It restricts cipher suites to AEAD-only (removing CBC-SHA),
// requires Extended Master Secret (RFC 7627), and sets SRTP to AEAD_AES_256_GCM.
//
// certType selects the certificate type for cipher suite filtering:
//   - "ecdsa" (or empty) — ECDSA cipher suites only (pion default)
//   - "rsa" — RSA cipher suites only
func hardenedSettingEngine(certType string) webrtc.SettingEngine {
	var se webrtc.SettingEngine

	// AEAD-only cipher suites. CBC-SHA ciphers are excluded because they are
	// vulnerable to padding oracle attacks. All included suites use ECDHE key
	// exchange, preserving Perfect Forward Secrecy (PFS).
	switch certType {
	case "rsa":
		se.SetDTLSCipherSuites(
			dtls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			dtls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			dtls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
		)
	default:
		se.SetDTLSCipherSuites(
			dtls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			dtls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			dtls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
		)
	}

	// Require Extended Master Secret (RFC 7627) — prevents a cross-protocol
	// attack where a session resumption handshake is forwarded to a different
	// cipher suite than the original. When set to Require, the DTLS handshake
	// fails if the peer does not support the extension.
	se.SetDTLSExtendedMasterSecret(dtls.RequireExtendedMasterSecret)

	// Use AEAD_AES_256_GCM for SRTP — the strongest SRTP protection profile
	// supported by pion. This provides authenticated encryption with a 256-bit
	// key and a 96-bit salt, offering resistance against ciphertext
	// manipulation in the RTP/RTCP media path.
	se.SetSRTPProtectionProfiles(dtls.SRTP_AEAD_AES_256_GCM)

	return se
}
