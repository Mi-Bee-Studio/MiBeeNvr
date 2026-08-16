package config

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// reUUIDv4 matches the canonical 8-4-4-4-12 lowercase hex UUID form.
var reUUIDv4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewDeviceIDIsUUIDv4(t *testing.T) {
	t.Parallel()
	seen := make(map[string]bool, 100)
	for range 100 {
		id := newDeviceID()
		require.Regexp(t, reUUIDv4, id)
		require.False(t, seen[id], "duplicate UUID in 100 draws: %s", id)
		seen[id] = true
	}
}

func TestEnsureDeviceIdentityGeneratesAndPersists(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "mibee-nvr.yaml")

	cfg := &Config{}
	cfg.ApplyDefaults()
	require.NoError(t, Save(path, cfg))
	require.Empty(t, cfg.Server.DeviceID, "fresh config must not have an ID yet")

	require.NoError(t, EnsureDeviceIdentity(path, cfg))
	id := cfg.Server.DeviceID
	require.Regexp(t, reUUIDv4, id)

	// The ID must be persisted — a fresh Load keeps the same value.
	reloaded, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, id, reloaded.Server.DeviceID)

	// A second run over the persisted config must not rotate the ID.
	require.NoError(t, EnsureDeviceIdentity(path, reloaded))
	require.Equal(t, id, reloaded.Server.DeviceID, "existing persisted ID must be preserved")
}

func TestEnsureDeviceIdentitySkipsMissingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.yaml")

	cfg := &Config{}
	cfg.ApplyDefaults()
	// No error, no file created — out-of-band configs are left alone.
	require.NoError(t, EnsureDeviceIdentity(path, cfg))
	_, err := os.Stat(path)
	require.True(t, os.IsNotExist(err), "must not create the config file")
	require.Empty(t, cfg.Server.DeviceID, "no in-memory side effect when file is missing")
}

func TestEnsureDeviceIdentityKeepsExistingID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "mibee-nvr.yaml")

	cfg := &Config{}
	cfg.ApplyDefaults()
	cfg.Server.DeviceID = "pre-existing-id"
	require.NoError(t, Save(path, cfg))

	// Load returns a pointer; mutating the copy under test must not leak a rewrite.
	require.NoError(t, EnsureDeviceIdentity(path, cfg))
	require.Equal(t, "pre-existing-id", cfg.Server.DeviceID)
}

func TestDiscoveryDefaultsApplied(t *testing.T) {
	t.Parallel()
	cfg := &Config{}
	cfg.ApplyDefaults()
	require.NotNil(t, cfg.Server.Discovery.UDP.Enabled)
	require.True(t, *cfg.Server.Discovery.UDP.Enabled)
	require.Equal(t, DefaultUDPPort, cfg.Server.Discovery.UDP.Port)
	require.NotEmpty(t, cfg.Server.DeviceName, "device name defaults to hostname")
}

func TestDiscoveryPortValidation(t *testing.T) {
	t.Parallel()
	cfg := &Config{}
	cfg.ApplyDefaults()
	cfg.Server.Discovery.UDP.Port = 70000
	err := Validate(cfg)
	require.ErrorContains(t, err, "server.discovery.udp.port")
}
