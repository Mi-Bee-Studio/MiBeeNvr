// Package relay provides a DERP-like relay client for P2P connectivity
// when direct ICE/DTLS connections are unavailable.
package relay

import (
	"crypto/rand"

	"golang.org/x/crypto/curve25519"
)

// GenerateKeyPair creates a new X25519 key pair for relay authentication.
func GenerateKeyPair() (publicKey [32]byte, privateKey [32]byte) {
	_, err := rand.Read(privateKey[:])
	if err != nil {
		panic("crypto/rand: " + err.Error())
	}
	// Clamp for X25519.
	privateKey[0] &= 248
	privateKey[31] &= 127
	privateKey[31] |= 64

	curve25519.ScalarBaseMult(&publicKey, &privateKey)
	return
}
