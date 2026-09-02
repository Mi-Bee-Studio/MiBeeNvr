package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// testChecksums mirrors the sha256sum format produced by release.yml.
const testChecksums = `3f79bb7b435b05321651daefd374cdc681dc06faa65e374e38337b88ca046dea  mibee-nvr-amd64
9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08  mibee-nvr-arm64
b17ef6d19c7a5b1e83b9895f5d4b9a1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f70  mibee-nvr-armv7
`

func signForTest(t *testing.T, priv ed25519.PrivateKey, data []byte) []byte {
	t.Helper()
	return ed25519.Sign(priv, data)
}

func TestVerifyChecksumsSignature_ValidAndTampered(t *testing.T) {
	t.Parallel()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sig := signForTest(t, priv, []byte(testChecksums))

	if err := verifyWithKey(pub, []byte(testChecksums), sig); err != nil {
		t.Fatalf("valid signature must verify: %v", err)
	}

	// Flip ONE byte in the checksums (the forged-binary scenario) — must fail.
	forged := []byte(testChecksums)
	forged[0] ^= 0x01
	if err := verifyWithKey(pub, forged, sig); err == nil {
		t.Fatal("tampered checksums must fail signature verification")
	}

	// Flip one byte in the signature itself.
	badSig := append([]byte(nil), sig...)
	badSig[7] ^= 0x01
	if err := verifyWithKey(pub, []byte(testChecksums), badSig); err == nil {
		t.Fatal("tampered signature must fail")
	}

	// Wrong key entirely.
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyWithKey(otherPub, []byte(testChecksums), sig); err == nil {
		t.Fatal("signature by a different key must fail")
	}

	// Malformed key sizes.
	if err := verifyWithKey(ed25519.PublicKey(make([]byte, 10)), []byte(testChecksums), sig); err == nil {
		t.Fatal("undersized public key must fail")
	}
	if err := verifyWithKey(pub, []byte(testChecksums), sig[:32]); err == nil {
		t.Fatal("undersized signature must fail")
	}
}

func TestEmbeddedReleaseSigningKeyWellFormed(t *testing.T) {
	t.Parallel()
	raw, err := base64.StdEncoding.DecodeString(releaseSigningPubKeyB64)
	if err != nil {
		t.Fatalf("embedded key must be valid base64: %v", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		t.Fatalf("embedded key must be %d raw bytes (ed25519), got %d", ed25519.PublicKeySize, len(raw))
	}
}

func TestVerifyBinaryChecksum(t *testing.T) {
	t.Parallel()
	payload := []byte("fake binary bytes")
	sum := sha256.Sum256(payload)
	checksums := fmt.Sprintf("%s  mibee-nvr-amd64\n%s  mibee-nvr-arm64\n",
		hex.EncodeToString(sum[:]), strings.Repeat("a", 64))

	if err := VerifyBinaryChecksum([]byte(checksums), "mibee-nvr-amd64", payload); err != nil {
		t.Fatalf("correct hash must verify: %v", err)
	}

	// One flipped payload byte — the forged-binary scenario (#646 acceptance).
	forged := append([]byte(nil), payload...)
	forged[0] ^= 0x01
	if err := VerifyBinaryChecksum([]byte(checksums), "mibee-nvr-amd64", forged); err == nil {
		t.Fatal("modified binary must fail checksum verification")
	}

	if err := VerifyBinaryChecksum([]byte(checksums), "mibee-nvr-armv7", payload); err == nil {
		t.Fatal("file missing from checksums.txt must fail")
	}

	binaryLine := strings.Repeat("z", 64)
	if err := VerifyBinaryChecksum([]byte(binaryLine+"  mibee-nvr-x\n"), "mibee-nvr-x", payload); err == nil {
		t.Fatal("malformed hex digest must fail")
	}
}

// TestVerifyChecksumsSignature_OpenSSLInterop locks the exact signature
// format release.yml produces (`openssl pkeyutl -sign -rawin` over
// checksums.txt with a PKCS#8 DER ed25519 key) to what verifyWithKey accepts:
// a throwaway keypair is signed by the openssl binary and verified in-process.
// Skips when openssl is unavailable (hermetic-by-default per repo test rules).
func TestVerifyChecksumsSignature_OpenSSLInterop(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl not installed")
	}
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "test.key")
	if err := exec.Command("openssl", "genpkey", "-algorithm", "ed25519", "-outform", "DER", "-out", keyPath).Run(); err != nil {
		t.Fatalf("genpkey: %v", err)
	}
	dataPath := filepath.Join(dir, "checksums.txt")
	if err := os.WriteFile(dataPath, []byte(testChecksums), 0o600); err != nil {
		t.Fatal(err)
	}
	sigPath := filepath.Join(dir, "checksums.txt.sig")
	sign := exec.Command("openssl", "pkeyutl", "-sign", "-inkey", keyPath, "-rawin", "-in", dataPath, "-out", sigPath)
	if out, err := sign.CombinedOutput(); err != nil {
		t.Fatalf("pkeyutl -sign: %v\n%s", err, out)
	}

	// Extract the raw 32-byte public key from the SPKI DER (last 32 bytes).
	pubSPKI, err := exec.Command("openssl", "pkey", "-in", keyPath, "-inform", "DER", "-pubout", "-outform", "DER").Output()
	if err != nil {
		t.Fatalf("pkey -pubout: %v", err)
	}
	if len(pubSPKI) < ed25519.PublicKeySize {
		t.Fatalf("unexpected SPKI size %d", len(pubSPKI))
	}
	pub := ed25519.PublicKey(pubSPKI[len(pubSPKI)-ed25519.PublicKeySize:])

	sig, err := os.ReadFile(sigPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(sig) != ed25519.SignatureSize {
		t.Fatalf("openssl produced a %d-byte signature, want %d", len(sig), ed25519.SignatureSize)
	}
	if err := verifyWithKey(pub, []byte(testChecksums), sig); err != nil {
		t.Fatalf("openssl-signed checksums must verify in-process: %v", err)
	}
	forged := []byte(testChecksums)
	forged[0] ^= 0x01
	if err := verifyWithKey(pub, forged, sig); err == nil {
		t.Fatal("tampered checksums must fail against the openssl signature")
	}
}
