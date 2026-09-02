package update

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Release artifact verification (#646).
//
// release.yml attaches checksums.txt (sha256sum format over the bare
// binaries) and checksums.txt.sig (raw 64-byte ed25519 signature over
// checksums.txt, produced from the RELEASE_SIGNING_KEY GitHub secret) to
// every release. This file carries the matching public key and the pure-Go
// verification the future self-update pipeline (#647) — and manual users —
// use to prove a downloaded binary is the exact artifact this project
// released: verify the signature over checksums.txt, then match the binary's
// sha256 against its line.

// releaseSigningPubKeyB64 is the raw 32-byte ed25519 public key (base64)
// corresponding to the RELEASE_SIGNING_KEY secret. The PEM form for manual
// openssl verification ships in deploy/keys/release-signing.pub.pem.
// Rotating the key means regenerating the pair, updating this const (and the
// PEM), and replacing the secret — releases signed by the old key will then
// fail verification, which is the intended fail-closed behavior.
const releaseSigningPubKeyB64 = "8NQQQl1tSJAZZ/t2n3qzMmdzxoYqAcNEXfF4v05bmyw="

// VerifyChecksumsSignature checks an ed25519 signature (raw 64 bytes, as
// produced by `openssl pkeyutl -sign -rawin`) over the full checksums.txt
// bytes, using the embedded release signing key.
func VerifyChecksumsSignature(checksums, sig []byte) error {
	raw, err := base64.StdEncoding.DecodeString(releaseSigningPubKeyB64)
	if err != nil {
		return fmt.Errorf("update: embedded release signing key is malformed: %w", err)
	}
	return verifyWithKey(ed25519.PublicKey(raw), checksums, sig)
}

// verifyWithKey is VerifyChecksumsSignature with an injected key (tests sign
// with their own throwaway pairs).
func verifyWithKey(pub ed25519.PublicKey, checksums, sig []byte) error {
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("update: bad public key size %d (want %d)", len(pub), ed25519.PublicKeySize)
	}
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("update: bad signature size %d (want %d)", len(sig), ed25519.SignatureSize)
	}
	if !ed25519.Verify(pub, checksums, sig) {
		return errors.New("update: checksums.txt signature verification failed — the artifact may be corrupted or tampered with")
	}
	return nil
}

// VerifyBinaryChecksum matches data's sha256 against filename's line in a
// sha256sum-format checksums.txt ("<64 hex chars>  <filename>" per line).
// This is the second half of the chain: a valid signature over checksums.txt
// plus a matching digest proves the downloaded bytes are the released ones.
func VerifyBinaryChecksum(checksums []byte, filename string, data []byte) error {
	want, err := digestFor(checksums, filename)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != want {
		return fmt.Errorf("update: %s checksum mismatch: got %s, release says %s", filename, got, want)
	}
	return nil
}

func digestFor(checksums []byte, filename string) (string, error) {
	for line := range strings.Lines(string(checksums)) {
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 || parts[1] != filename {
			continue
		}
		digest := parts[0]
		if len(digest) != 64 {
			return "", fmt.Errorf("update: malformed digest for %s in checksums.txt", filename)
		}
		if _, err := hex.DecodeString(digest); err != nil {
			return "", fmt.Errorf("update: non-hex digest for %s in checksums.txt", filename)
		}
		return digest, nil
	}
	return "", fmt.Errorf("update: %s not listed in checksums.txt", filename)
}
