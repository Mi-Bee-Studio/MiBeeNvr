package config

import (
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func helperGenerateKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	n, err := rand.Read(key)
	require.NoError(t, err)
	require.Equal(t, 32, n)
	return key
}

func helperSetEnvKey(t *testing.T, key []byte) {
	t.Helper()
	t.Setenv("NVR_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(key))
	// Reset cached key
	encryptionKey = nil
}

func helperClearEnvKey(t *testing.T) {
	t.Helper()
	t.Setenv("NVR_ENCRYPTION_KEY", "")
	t.Setenv("NVR_ENCRYPTION_KEY_FILE", "")
	encryptionKey = nil
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := helperGenerateKey(t)

	tests := []struct {
		name  string
		input string
	}{
		{"simple", "hello world"},
		{"empty", ""},
		{"special chars", "p@$$w0rd!#$%^&*()"},
		{"unicode", "密码テスト🔐"},
		{"long", string(make([]byte, 1024))}, // 1KB of null bytes
		{"base64-like", "dGVzdC1rZXk="},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			encrypted, err := Encrypt(tc.input, key)
			require.NoError(t, err)

			if tc.input == "" {
				require.Equal(t, "", encrypted)
				return
			}

			require.NotEqual(t, tc.input, encrypted)
			require.True(t, IsEncrypted(encrypted))

			decrypted, err := Decrypt(encrypted, key)
			require.NoError(t, err)
			require.Equal(t, tc.input, decrypted)
		})
	}
}

func TestEncryptProducesDifferentCiphertext(t *testing.T) {
	key := helperGenerateKey(t)
	plaintext := "same-input"

	enc1, err := Encrypt(plaintext, key)
	require.NoError(t, err)
	enc2, err := Encrypt(plaintext, key)
	require.NoError(t, err)

	// Different nonces should produce different ciphertext
	require.NotEqual(t, enc1, enc2)

	// Both should decrypt to the same value
	dec1, err := Decrypt(enc1, key)
	require.NoError(t, err)
	dec2, err := Decrypt(enc2, key)
	require.NoError(t, err)
	require.Equal(t, plaintext, dec1)
	require.Equal(t, plaintext, dec2)
}

func TestEncryptInvalidKeySize(t *testing.T) {
	_, err := Encrypt("test", []byte("short"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "32 bytes")
}

func TestDecryptInvalidKeySize(t *testing.T) {
	_, err := Decrypt("ENC:dGVzdA==", []byte("short"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "32 bytes")
}

func TestDecryptPlaintextPassthrough(t *testing.T) {
	key := helperGenerateKey(t)
	result, err := Decrypt("plain-password", key)
	require.NoError(t, err)
	require.Equal(t, "plain-password", result)
}

func TestDecryptWrongKey(t *testing.T) {
	key1 := helperGenerateKey(t)
	key2 := helperGenerateKey(t)

	encrypted, err := Encrypt("secret", key1)
	require.NoError(t, err)

	_, err = Decrypt(encrypted, key2)
	require.Error(t, err)
	require.Contains(t, err.Error(), "decrypt")
}

func TestDecryptInvalidBase64(t *testing.T) {
	key := helperGenerateKey(t)
	_, err := Decrypt("ENC:!!!invalid-base64!!!", key)
	require.Error(t, err)
	require.Contains(t, err.Error(), "decode base64")
}

func TestDecryptCiphertextTooShort(t *testing.T) {
	key := helperGenerateKey(t)
	// nonce size for AES-GCM is 12 bytes, this is way too short
	_, err := Decrypt("ENC:AA==", key)
	require.Error(t, err)
	require.Contains(t, err.Error(), "too short")
}

func TestIsEncrypted(t *testing.T) {
	require.True(t, IsEncrypted("ENC:abc123"))
	require.True(t, IsEncrypted("ENC:"))
	require.False(t, IsEncrypted("plain"))
	require.False(t, IsEncrypted(""))
	require.False(t, IsEncrypted("enc:lowercase"))
}

func TestHasKeyNoEnvVar(t *testing.T) {
	helperClearEnvKey(t)
	require.False(t, HasKey())
}

func TestHasKeyWithEnvVar(t *testing.T) {
	key := helperGenerateKey(t)
	helperSetEnvKey(t, key)
	require.True(t, HasKey())
}

func TestGetEncryptionKeyFromRaw(t *testing.T) {
	key := helperGenerateKey(t)
	helperSetEnvKey(t, key)

	got := GetEncryptionKey()
	require.NotNil(t, got)
	require.Equal(t, key, got)
}

func TestGetEncryptionKeyFromFile(t *testing.T) {
	key := helperGenerateKey(t)
	helperClearEnvKey(t)

	// Write key to temp file
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key")
	require.NoError(t, os.WriteFile(keyFile, []byte(base64.StdEncoding.EncodeToString(key)), 0o600))

	t.Setenv("NVR_ENCRYPTION_KEY_FILE", keyFile)
	encryptionKey = nil

	got := GetEncryptionKey()
	require.NotNil(t, got)
	require.Equal(t, key, got)
}

func TestGetEncryptionKeyInvalid(t *testing.T) {
	helperClearEnvKey(t)
	t.Setenv("NVR_ENCRYPTION_KEY", "too-short")
	encryptionKey = nil
	require.Nil(t, GetEncryptionKey())
}

func TestDecryptConfig(t *testing.T) {
	key := helperGenerateKey(t)

	encPass, err := Encrypt("cam-secret", key)
	require.NoError(t, err)
	encToken, err := Encrypt("xiaomi-token", key)
	require.NoError(t, err)
	encUserID, err := Encrypt("user-123", key)
	require.NoError(t, err)
	encAuthPass, err := Encrypt("auth-pass", key)
	require.NoError(t, err)

	cfg := &Config{
		Auth: AuthConfig{Password: encAuthPass},
		Xiaomi: XiaomiConfig{
			UserID: encUserID,
			Token:  encToken,
		},
		Cameras: []CameraConfig{
			{Password: encPass},
			{Password: "plain-pass"}, // mixed: plaintext
		},
	}

	decryptConfig(cfg, key)

	require.Equal(t, "auth-pass", cfg.Auth.Password)
	require.Equal(t, "user-123", cfg.Xiaomi.UserID)
	require.Equal(t, "xiaomi-token", cfg.Xiaomi.Token)
	require.Equal(t, "cam-secret", cfg.Cameras[0].Password)
	require.Equal(t, "plain-pass", cfg.Cameras[1].Password) // unchanged
}

func TestDecryptConfigNilKey(t *testing.T) {
	cfg := &Config{
		Auth:   AuthConfig{Password: "ENC:something"},
		Xiaomi: XiaomiConfig{Token: "ENC:else"},
	}
	// Should not panic and should not modify fields
	decryptConfig(cfg, nil)
	require.Equal(t, "ENC:something", cfg.Auth.Password)
}

func TestEncryptConfig(t *testing.T) {
	key := helperGenerateKey(t)

	cfg := &Config{
		Auth: AuthConfig{Password: "auth-pass"},
		Xiaomi: XiaomiConfig{
			UserID: "user-123",
			Token:  "xiaomi-token",
		},
		Cameras: []CameraConfig{
			{Password: "cam-secret"},
			{Password: ""}, // empty, should be skipped
		},
	}

	encrypted := encryptConfig(cfg, key)

	require.Len(t, encrypted, 4)
	require.Contains(t, encrypted, "auth.password")
	require.Contains(t, encrypted, "xiaomi.user_id")
	require.Contains(t, encrypted, "xiaomi.token")
	require.Contains(t, encrypted, "cameras[0].password")

	require.True(t, IsEncrypted(cfg.Auth.Password))
	require.True(t, IsEncrypted(cfg.Xiaomi.UserID))
	require.True(t, IsEncrypted(cfg.Xiaomi.Token))
	require.True(t, IsEncrypted(cfg.Cameras[0].Password))
	require.Equal(t, "", cfg.Cameras[1].Password) // empty stays empty
}

func TestEncryptConfigSkipsAlreadyEncrypted(t *testing.T) {
	key := helperGenerateKey(t)

	encPass, err := Encrypt("already-encrypted", key)
	require.NoError(t, err)

	cfg := &Config{Auth: AuthConfig{Password: encPass}}
	encrypted := encryptConfig(cfg, key)
	require.Empty(t, encrypted) // nothing new to encrypt
}

func TestEncryptConfigNilKey(t *testing.T) {
	cfg := &Config{Auth: AuthConfig{Password: "plain"}}
	encrypted := encryptConfig(cfg, nil)
	require.Empty(t, encrypted)
	require.Equal(t, "plain", cfg.Auth.Password) // unchanged
}

func TestSnapshotRestore(t *testing.T) {
	cfg := &Config{
		Auth: AuthConfig{Password: "original-pass"},
		Xiaomi: XiaomiConfig{
			UserID: "original-user",
			Token:  "original-token",
		},
		Cameras: []CameraConfig{
			{Password: "cam1-pass"},
			{Password: "cam2-pass"},
		},
	}

	snap := snapshotSensitive(cfg)

	// Modify config
	cfg.Auth.Password = "modified"
	cfg.Xiaomi.Token = "modified"
	cfg.Cameras[0].Password = "modified"

	// Restore
	snap.restore(cfg)

	require.Equal(t, "original-pass", cfg.Auth.Password)
	require.Equal(t, "original-user", cfg.Xiaomi.UserID)
	require.Equal(t, "original-token", cfg.Xiaomi.Token)
	require.Equal(t, "cam1-pass", cfg.Cameras[0].Password)
	require.Equal(t, "cam2-pass", cfg.Cameras[1].Password)
}

func TestSensitiveFieldPaths(t *testing.T) {
	cfg := &Config{
		Auth: AuthConfig{Password: "plain-auth"},
		Xiaomi: XiaomiConfig{
			UserID: "plain-user",
			Token:  "plain-token",
		},
		Cameras: []CameraConfig{
			{Password: "cam1-pass"},
			{Password: ""}, // empty, not sensitive
		},
	}

	fields := SensitiveFieldPaths(cfg)
	require.Len(t, fields, 4)
	require.Contains(t, fields, "auth.password")
	require.Contains(t, fields, "xiaomi.user_id")
	require.Contains(t, fields, "xiaomi.token")
	require.Contains(t, fields, "cameras[0].password")
}

func TestSensitiveFieldPathsAllEncrypted(t *testing.T) {
	key := helperGenerateKey(t)

	cfg := &Config{
		Auth:   AuthConfig{Password: "plain"},
		Xiaomi: XiaomiConfig{Token: "plain"},
	}

	// Encrypt first
	encryptConfig(cfg, key)

	// Now all should be encrypted, so no plaintext paths
	fields := SensitiveFieldPaths(cfg)
	require.Empty(t, fields)
}

func TestSaveLoadWithEncryption(t *testing.T) {
	key := helperGenerateKey(t)
	helperSetEnvKey(t, key)

	dir := t.TempDir()
	path := filepath.Join(dir, "test-config.yaml")

	ftpEnabled := true
	cfg := &Config{
		Server:  ServerConfig{Listen: ":9090"},
		Storage: StorageConfig{RootDir: "/data"},
		Auth:    AuthConfig{Username: "admin", Password: "my-secret-pass"},
		Xiaomi: XiaomiConfig{
			UserID: "user-123",
			Token:  "token-abc",
			Region: "cn",
		},
		Cameras: []CameraConfig{
			{
				ID:       "cam1",
				Protocol: "rtsp",
				Encoding: "h264",
				URL:      "rtsp://192.168.1.10/stream",
				Password: "cam-secret",
				Enabled:  true,
			},
		},
		FTP: FTPConfig{Enabled: &ftpEnabled, Port: 2121},
	}
	cfg.ApplyDefaults()

	// Save (should encrypt)
	err := Save(path, cfg)
	require.NoError(t, err)

	// In-memory config should still have plaintext values
	require.Equal(t, "my-secret-pass", cfg.Auth.Password)
	require.Equal(t, "user-123", cfg.Xiaomi.UserID)
	require.Equal(t, "token-abc", cfg.Xiaomi.Token)
	require.Equal(t, "cam-secret", cfg.Cameras[0].Password)

	// Read raw file and verify ENC: prefix
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	rawStr := string(raw)
	require.Contains(t, rawStr, "ENC:")
	// Plaintext values should NOT appear in raw file
	require.NotContains(t, rawStr, "my-secret-pass")
	require.NotContains(t, rawStr, "cam-secret")
	require.NotContains(t, rawStr, "token-abc")

	// Load (should decrypt)
	loaded, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "my-secret-pass", loaded.Auth.Password)
	require.Equal(t, "user-123", loaded.Xiaomi.UserID)
	require.Equal(t, "token-abc", loaded.Xiaomi.Token)
	require.Equal(t, "cam-secret", loaded.Cameras[0].Password)
}

func TestSaveLoadWithoutEncryption(t *testing.T) {
	helperClearEnvKey(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "test-config.yaml")

	ftpEnabled := true
	cfg := &Config{
		Server:  ServerConfig{Listen: ":9090"},
		Storage: StorageConfig{RootDir: "/data"},
		Auth:    AuthConfig{Password: "plain-pass"},
		Cameras: []CameraConfig{{ID: "cam1", Password: "cam-plain"}},
		FTP:     FTPConfig{Enabled: &ftpEnabled, Port: 2121},
	}
	cfg.ApplyDefaults()

	err := Save(path, cfg)
	require.NoError(t, err)

	// Raw file should have plaintext
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(raw), "plain-pass")
	require.Contains(t, string(raw), "cam-plain")

	// Load should return plaintext
	loaded, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "plain-pass", loaded.Auth.Password)
	require.Equal(t, "cam-plain", loaded.Cameras[0].Password)
}

func TestLoadMixedEncryptedPlaintext(t *testing.T) {
	key := helperGenerateKey(t)

	encToken, err := Encrypt("secret-token", key)
	require.NoError(t, err)

	dir := t.TempDir()
	path := filepath.Join(dir, "mixed.yaml")

	// Write a config with mixed encrypted/plaintext fields
	yaml := `server:
  listen: ":9090"
storage:
  root_dir: /data
cameras:
  - id: cam1
    password: ENC:` + encToken[4:] + `
    url: rtsp://x
    protocol: rtsp
    encoding: h264
  - id: cam2
    password: plain-password
    url: rtsp://y
    protocol: rtsp
    encoding: h264
xiaomi:
  token: ` + encToken + `
  user_id: plain-user
  region: cn
`
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o644))

	helperSetEnvKey(t, key)

	cfg, err := Load(path)
	require.NoError(t, err)

	// Encrypted fields should be decrypted
	require.Equal(t, "secret-token", cfg.Cameras[0].Password)
	require.Equal(t, "secret-token", cfg.Xiaomi.Token)
	// Plaintext fields should pass through
	require.Equal(t, "plain-password", cfg.Cameras[1].Password)
	require.Equal(t, "plain-user", cfg.Xiaomi.UserID)
}

func TestLoadEncryptedWithoutKey(t *testing.T) {
	key := helperGenerateKey(t)

	encPass, err := Encrypt("secret", key)
	require.NoError(t, err)

	dir := t.TempDir()
	path := filepath.Join(dir, "enc.yaml")

	yaml := `server:
  listen: ":9090"
storage:
  root_dir: /data
xiaomi:
  token: ` + encPass + `
  region: cn
`
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o644))

	// No encryption key set — encrypted values should remain as-is
	helperClearEnvKey(t)

	cfg, err := Load(path)
	require.NoError(t, err)
	// Without key, ENC: values stay as-is (decryptConfig is skipped)
	require.Equal(t, encPass, cfg.Xiaomi.Token)
}

func TestEncryptConfigFile(t *testing.T) {
	key := helperGenerateKey(t)
	helperSetEnvKey(t, key)

	dir := t.TempDir()
	path := filepath.Join(dir, "encrypt-test.yaml")

	ftpEnabled := true
	cfg := &Config{
		Server:  ServerConfig{Listen: ":9090"},
		Storage: StorageConfig{RootDir: "/data"},
		Auth:    AuthConfig{Password: "will-be-encrypted"},
		Xiaomi:  XiaomiConfig{Token: "token-to-encrypt", Region: "cn"},
		Cameras: []CameraConfig{{ID: "c1", Password: "cam-pass", URL: "rtsp://x", Protocol: "rtsp", Encoding: "h264"}},
		FTP:     FTPConfig{Enabled: &ftpEnabled, Port: 2121},
	}
	cfg.ApplyDefaults()
	require.NoError(t, Save(path, cfg))

	// Save without key first to write plaintext
	helperClearEnvKey(t)
	require.NoError(t, Save(path, cfg))

	// Verify plaintext in file
	raw, _ := os.ReadFile(path)
	require.Contains(t, string(raw), "will-be-encrypted")

	// Now encrypt
	helperSetEnvKey(t, key)
	fields, err := EncryptConfigFile(path)
	require.NoError(t, err)
	require.Len(t, fields, 3) // auth.password, xiaomi.token, cameras[0].password

	// Verify encrypted in file
	raw, _ = os.ReadFile(path)
	rawStr := string(raw)
	require.NotContains(t, rawStr, "will-be-encrypted")
	require.NotContains(t, rawStr, "token-to-encrypt")
	require.NotContains(t, rawStr, "cam-pass")
	require.Contains(t, rawStr, "ENC:")

	// Verify can still decrypt on load
	loaded, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "will-be-encrypted", loaded.Auth.Password)
	require.Equal(t, "token-to-encrypt", loaded.Xiaomi.Token)
	require.Equal(t, "cam-pass", loaded.Cameras[0].Password)
}

func TestEncryptConfigFileNoKey(t *testing.T) {
	helperClearEnvKey(t)
	_, err := EncryptConfigFile("test.yaml")
	require.Error(t, err)
	require.Contains(t, err.Error(), "NVR_ENCRYPTION_KEY")
}

func TestEncryptConfigFileAlreadyEncrypted(t *testing.T) {
	key := helperGenerateKey(t)
	helperSetEnvKey(t, key)

	dir := t.TempDir()
	path := filepath.Join(dir, "already.yaml")

	ftpEnabled := true
	cfg := &Config{
		Server:  ServerConfig{Listen: ":9090"},
		Storage: StorageConfig{RootDir: "/data"},
		Auth:    AuthConfig{Password: "pass"},
		FTP:     FTPConfig{Enabled: &ftpEnabled, Port: 2121},
	}
	cfg.ApplyDefaults()
	require.NoError(t, Save(path, cfg)) // Save will encrypt

	// Second encrypt will find plaintext fields (they were decrypted on load), re-encrypt them
	fields, err := EncryptConfigFile(path)
	require.NoError(t, err)
	require.NotEmpty(t, fields) // fields were plaintext in memory, now re-encrypted

	// Verify the raw file still has ENC: prefix (re-encrypted)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	rawStr := string(raw)
	require.Contains(t, rawStr, "password: ENC:")
}
