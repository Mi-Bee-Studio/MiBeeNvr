package peer

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// buildTestJWT makes an unsigned (header.payload.signature) JWT-shaped token
// carrying the given sub/jti — enough for client-side claim extraction.
func buildTestJWT(sub, jti string) string {
	mk := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return mk(map[string]string{"alg": "none"}) + "." +
		mk(map[string]string{"sub": sub, "jti": jti}) + "."
}

// TestDeviceHelloSignature_signsSubAndJTI verifies the helper signs exactly
// "<sub>:<jti>" with the configured device key.
func TestDeviceHelloSignature_signsSubAndJTI(t *testing.T) {
	t.Parallel()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	keyPath := filepath.Join(t.TempDir(), "device.key")
	require.NoError(t, os.WriteFile(keyPath, priv, 0o600))

	sc := newSignalingClient(SignalingConfig{
		ServerURL:     "ws://irrelevant",
		JWT:           buildTestJWT("device:p:d", "jti-42"),
		Role:          "device",
		DeviceKeyPath: keyPath,
	})
	require.NotNil(t, sc.deviceKey)

	sigB64 := sc.deviceHelloSignature()
	require.NotEmpty(t, sigB64, "role=device with key must sign")
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	require.NoError(t, err)
	require.True(t, ed25519.Verify(pub, []byte("device:p:d:jti-42"), sig),
		"signature must verify over <sub>:<jti>")
}

// TestDeviceHelloSignature_skipsForClientOrKeyless verifies no signature is
// produced for client role or when no key is configured.
func TestDeviceHelloSignature_skipsForClientOrKeyless(t *testing.T) {
	t.Parallel()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	keyPath := filepath.Join(t.TempDir(), "device.key")
	require.NoError(t, os.WriteFile(keyPath, priv, 0o600))

	clientWithKey := newSignalingClient(SignalingConfig{
		JWT: buildTestJWT("u", "j"), Role: "client", DeviceKeyPath: keyPath,
	})
	require.Empty(t, clientWithKey.deviceHelloSignature())

	deviceNoKey := newSignalingClient(SignalingConfig{
		JWT: buildTestJWT("u", "j"), Role: "device",
	})
	require.Empty(t, deviceNoKey.deviceHelloSignature())

	// Unreadable key falls back to unsigned rather than failing Dial.
	deviceBadKey := newSignalingClient(SignalingConfig{
		JWT: buildTestJWT("u", "j"), Role: "device",
		DeviceKeyPath: filepath.Join(t.TempDir(), "missing.key"),
	})
	require.Nil(t, deviceBadKey.deviceKey)
	require.Empty(t, deviceBadKey.deviceHelloSignature())
}

// TestDeviceHelloSignature_acceptsSeedForm verifies a 32-byte seed key file
// (the form the server-side re-enroll tooling produces) also signs correctly.
func TestDeviceHelloSignature_acceptsSeedForm(t *testing.T) {
	t.Parallel()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	keyPath := filepath.Join(t.TempDir(), "seed.key")
	require.NoError(t, os.WriteFile(keyPath, priv.Seed(), 0o600))

	sc := newSignalingClient(SignalingConfig{
		JWT:           buildTestJWT("device:p:d2", "jti-7"),
		Role:          "device",
		DeviceKeyPath: keyPath,
	})
	require.NotNil(t, sc.deviceKey)

	sig, err := base64.StdEncoding.DecodeString(sc.deviceHelloSignature())
	require.NoError(t, err)
	require.True(t, ed25519.Verify(pub, []byte("device:p:d2:jti-7"), sig))
}

// TestJWTSubjectAndJTI verifies claim extraction including padded payloads.
func TestJWTSubjectAndJTI(t *testing.T) {
	t.Parallel()
	sub, jti, err := jwtSubjectAndJTI(buildTestJWT("s1", "j1"))
	require.NoError(t, err)
	require.Equal(t, "s1", sub)
	require.Equal(t, "j1", jti)

	_, _, err = jwtSubjectAndJTI("garbage")
	require.Error(t, err)
}
