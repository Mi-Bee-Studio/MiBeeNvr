package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

const encPrefix = "ENC:"

// encryptionKey caches the key derived from the environment variable.
// It is read once and cached for the process lifetime.
var encryptionKey []byte

// GetEncryptionKey returns the AES-256 encryption key from NVR_ENCRYPTION_KEY
// environment variable. The key must be exactly 32 bytes (base64-encoded or raw).
// Returns nil if no key is configured.
func GetEncryptionKey() []byte {
	if encryptionKey != nil {
		return encryptionKey
	}

	keyStr := os.Getenv("NVR_ENCRYPTION_KEY")
	if keyStr == "" {
		// Also check for key file
		keyFile := os.Getenv("NVR_ENCRYPTION_KEY_FILE")
		if keyFile != "" {
			data, err := os.ReadFile(keyFile)
			if err != nil {
				return nil
			}
			keyStr = strings.TrimSpace(string(data))
		}
	}
	if keyStr == "" {
		return nil
	}

	// Try base64 decode first
	key, err := base64.StdEncoding.DecodeString(keyStr)
	if err == nil && len(key) == 32 {
		encryptionKey = key
		return encryptionKey
	}

	// Try raw bytes (must be exactly 32)
	if len(keyStr) == 32 {
		encryptionKey = []byte(keyStr)
		return encryptionKey
	}

	return nil
}

// HasKey returns true if an encryption key is available.
func HasKey() bool {
	return GetEncryptionKey() != nil
}

// Encrypt encrypts plaintext using AES-256-GCM and returns a string
// in the format "ENC:base64(nonce+ciphertext+tag)".
// The key must be exactly 32 bytes.
func Encrypt(plaintext string, key []byte) (string, error) {
	if len(key) != 32 {
		return "", fmt.Errorf("encryption key must be 32 bytes, got %d", len(key))
	}
	if plaintext == "" {
		return "", nil
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}

	nonce := make([]byte, aesGCM.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	// Seal appends ciphertext+tag to nonce
	ciphertext := aesGCM.Seal(nonce, nonce, []byte(plaintext), nil)

	return encPrefix + base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decrypts a value that was encrypted with Encrypt.
// If the value does not start with "ENC:", it is returned as-is (plaintext passthrough).
func Decrypt(encoded string, key []byte) (string, error) {
	if !strings.HasPrefix(encoded, encPrefix) {
		return encoded, nil
	}

	if len(key) != 32 {
		return "", fmt.Errorf("encryption key must be 32 bytes, got %d", len(key))
	}

	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encoded, encPrefix))
	if err != nil {
		return "", fmt.Errorf("decode base64: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}

	nonceSize := aesGCM.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("ciphertext too short")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := aesGCM.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}

	return string(plaintext), nil
}

// IsEncrypted returns true if the value starts with the ENC: prefix.
func IsEncrypted(value string) bool {
	return strings.HasPrefix(value, encPrefix)
}

// decryptConfig decrypts all sensitive fields in the config in place.
// Fields without the ENC: prefix are left unchanged.
func decryptConfig(cfg *Config, key []byte) {
	if key == nil {
		return
	}
	// Auth password (plaintext, before auto-hash conversion)
	if v, err := Decrypt(cfg.Auth.Password, key); err == nil {
		cfg.Auth.Password = v
	} // if decrypt fails, leave as-is (might be plaintext)

	// MQTT password
	if v, err := Decrypt(cfg.MQTT.Password, key); err == nil {
		cfg.MQTT.Password = v
	}
	// Xiaomi credentials
	if v, err := Decrypt(cfg.Xiaomi.UserID, key); err == nil {
		cfg.Xiaomi.UserID = v
	}
	if v, err := Decrypt(cfg.Xiaomi.Token, key); err == nil {
		cfg.Xiaomi.Token = v
	}

	// Camera passwords
	for i := range cfg.Cameras {
		if v, err := Decrypt(cfg.Cameras[i].Password, key); err == nil {
			cfg.Cameras[i].Password = v
		}
	}
	// Metrics auth password
	if v, err := Decrypt(cfg.MetricsAuth.Password, key); err == nil {
		cfg.MetricsAuth.Password = v
	}
}

// encryptConfig encrypts all sensitive fields in the config in place.
// Returns a list of field names that were encrypted.
// Callers should restore plaintext values after saving.
func encryptConfig(cfg *Config, key []byte) []string {
	if key == nil {
		return nil
	}

	var encrypted []string

	// Auth password (skip if empty or already encrypted)
	if cfg.Auth.Password != "" && !IsEncrypted(cfg.Auth.Password) {
		if v, err := Encrypt(cfg.Auth.Password, key); err == nil {
			cfg.Auth.Password = v
			encrypted = append(encrypted, "auth.password")
		}
	}

	// MQTT password
	if cfg.MQTT.Password != "" && !IsEncrypted(cfg.MQTT.Password) {
		if v, err := Encrypt(cfg.MQTT.Password, key); err == nil {
			cfg.MQTT.Password = v
			encrypted = append(encrypted, "mqtt.password")
		}
	}

	// Xiaomi credentials
	if cfg.Xiaomi.UserID != "" && !IsEncrypted(cfg.Xiaomi.UserID) {
		if v, err := Encrypt(cfg.Xiaomi.UserID, key); err == nil {
			cfg.Xiaomi.UserID = v
			encrypted = append(encrypted, "xiaomi.user_id")
		}
	}
	if cfg.Xiaomi.Token != "" && !IsEncrypted(cfg.Xiaomi.Token) {
		if v, err := Encrypt(cfg.Xiaomi.Token, key); err == nil {
			cfg.Xiaomi.Token = v
			encrypted = append(encrypted, "xiaomi.token")
		}
	}

	// Camera passwords
	for i := range cfg.Cameras {
		if cfg.Cameras[i].Password != "" && !IsEncrypted(cfg.Cameras[i].Password) {
			if v, err := Encrypt(cfg.Cameras[i].Password, key); err == nil {
				cfg.Cameras[i].Password = v
				encrypted = append(encrypted, fmt.Sprintf("cameras[%d].password", i))
			}
		}
	}
	// Metrics auth password
	if cfg.MetricsAuth.Password != "" && !IsEncrypted(cfg.MetricsAuth.Password) {
		if v, err := Encrypt(cfg.MetricsAuth.Password, key); err == nil {
			cfg.MetricsAuth.Password = v
			encrypted = append(encrypted, "metrics_auth.password")
		}
	}

	return encrypted
}

// SensitiveFieldPaths returns a list of field paths for sensitive values
// currently stored in plaintext (not encrypted).
func SensitiveFieldPaths(cfg *Config) []string {
	var fields []string

	if cfg.Auth.Password != "" && !IsEncrypted(cfg.Auth.Password) {
		fields = append(fields, "auth.password")
	}
	if cfg.MQTT.Password != "" && !IsEncrypted(cfg.MQTT.Password) {
		fields = append(fields, "mqtt.password")
	}
	if cfg.Xiaomi.UserID != "" && !IsEncrypted(cfg.Xiaomi.UserID) {
		fields = append(fields, "xiaomi.user_id")
	}
	if cfg.Xiaomi.Token != "" && !IsEncrypted(cfg.Xiaomi.Token) {
		fields = append(fields, "xiaomi.token")
	}
	for i := range cfg.Cameras {
		if cfg.Cameras[i].Password != "" && !IsEncrypted(cfg.Cameras[i].Password) {
			fields = append(fields, fmt.Sprintf("cameras[%d].password", i))
		}
	}
	if cfg.MetricsAuth.Password != "" && !IsEncrypted(cfg.MetricsAuth.Password) {
		fields = append(fields, "metrics_auth.password")
	}

	return fields
}

// snapshotSensitive captures the current plaintext values of sensitive fields
// so they can be restored after an encrypted save.
type sensitiveSnapshot struct {
	AuthPassword    string
	MQTTPassword    string
	XiaomiUserID    string
	XiaomiToken     string
	CameraPasswords    []string
	MetricsAuthPassword string
}

func snapshotSensitive(cfg *Config) sensitiveSnapshot {
	s := sensitiveSnapshot{
		AuthPassword:    cfg.Auth.Password,
		MQTTPassword:    cfg.MQTT.Password,
		XiaomiUserID:    cfg.Xiaomi.UserID,
		XiaomiToken:     cfg.Xiaomi.Token,
		CameraPasswords:    make([]string, len(cfg.Cameras)),
		MetricsAuthPassword: cfg.MetricsAuth.Password,
	}
	for i := range cfg.Cameras {
		s.CameraPasswords[i] = cfg.Cameras[i].Password
	}
	return s
}

func (s sensitiveSnapshot) restore(cfg *Config) {
	cfg.Auth.Password = s.AuthPassword
	cfg.MQTT.Password = s.MQTTPassword
	cfg.Xiaomi.UserID = s.XiaomiUserID
	cfg.Xiaomi.Token = s.XiaomiToken
	for i := range cfg.Cameras {
		if i < len(s.CameraPasswords) {
			cfg.Cameras[i].Password = s.CameraPasswords[i]
		}
	cfg.MetricsAuth.Password = s.MetricsAuthPassword
	}
}
