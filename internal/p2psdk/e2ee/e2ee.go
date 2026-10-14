// Package e2ee provides the SDK-side tunnel-layer end-to-end encryption:
// X25519 key agreement over the relay keypair (already exchanged via
// sdp_offer/sdp_answer relay_pub_key) and XChaCha20-Poly1305 AEAD for
// TunnelData payloads. Mirrors internal/e2ee semantics for the server docs,
// with added session binding in the key derivation.
package e2ee

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
)

// KeySize is the symmetric AEAD key size in bytes.
const KeySize = 32

// label binds derived keys to this protocol version so the same ECDH secret
// can never be reused as a key for a different purpose.
var label = []byte("mibee-tunnel-e2ee-v1")

// DeriveSessionKey derives the tunnel AEAD key from our relay keypair and
// the remote peer's relay public key. Both peers derive the same key. The
// sorted public keys are mixed in to bind the key to this exact pair
// (unknown-key-share defense).
func DeriveSessionKey(myPriv, myPub, remotePub [32]byte) ([32]byte, error) {
	shared, err := curve25519.X25519(myPriv[:], remotePub[:])
	if err != nil {
		return [32]byte{}, fmt.Errorf("e2ee: x25519: %w", err)
	}
	lo, hi := myPub[:], remotePub[:]
	for i := range lo {
		if lo[i] != hi[i] {
			if lo[i] > hi[i] {
				lo, hi = hi, lo
			}
			break
		}
	}
	h := sha256.New()
	h.Write(label)
	h.Write(shared)
	h.Write(lo)
	h.Write(hi)
	var key [KeySize]byte
	copy(key[:], h.Sum(nil))
	return key, nil
}

// PublicKey computes the X25519 public key for a private scalar (the relay
// keypairs already hold both; tests and tooling use this to derive peers).
func PublicKey(priv [32]byte) ([32]byte, error) {
	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return [32]byte{}, fmt.Errorf("e2ee: public key: %w", err)
	}
	var out [32]byte
	copy(out[:], pub)
	return out, nil
}

// Cipher seals/opens tunnel payloads with XChaCha20-Poly1305. The 24-byte
// random nonce is prepended to every sealed output. Safe for concurrent use.
type Cipher struct {
	key []byte
}

// NewCipher creates a Cipher from a 32-byte session key.
func NewCipher(key [KeySize]byte) (*Cipher, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("e2ee: key must be %d bytes, got %d", KeySize, len(key))
	}
	k := make([]byte, KeySize)
	copy(k, key[:])
	return &Cipher{key: k}, nil
}

// Seal encrypts plaintext with associated data, returning nonce||ct+tag.
func (c *Cipher) Seal(plaintext, associated []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(c.key)
	if err != nil {
		return nil, fmt.Errorf("e2ee: new xchacha: %w", err)
	}
	nonce := make([]byte, aead.NonceSize(), aead.NonceSize()+len(plaintext)+aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("e2ee: nonce: %w", err)
	}
	return aead.Seal(nonce, nonce, plaintext, associated), nil
}

// Open decrypts a sealed nonce||ct+tag blob, verifying associated data.
// Returns a non-nil error on tamper, wrong key, or AAD mismatch.
func (c *Cipher) Open(sealed, associated []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(c.key)
	if err != nil {
		return nil, fmt.Errorf("e2ee: new xchacha: %w", err)
	}
	ns := aead.NonceSize()
	if len(sealed) < ns+aead.Overhead() {
		return nil, errors.New("e2ee: ciphertext too short")
	}
	return aead.Open(nil, sealed[:ns], sealed[ns:], associated)
}
