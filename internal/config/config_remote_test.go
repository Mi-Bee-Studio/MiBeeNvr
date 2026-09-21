package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newRemoteConfig builds a minimal enabled remote-storage config for
// validation tests.
func newRemoteConfig() *RemoteStorageConfig {
	t := true
	return &RemoteStorageConfig{
		Enabled:         true,
		EndpointURL:     "http://minio.local:9000",
		Bucket:          "nvr",
		AccessKeyID:     "minioadmin",
		SecretAccessKey: "minioadmin",
		PathStyle:       &t,
	}
}

func TestRemoteStorageDefaults(t *testing.T) {
	t.Parallel()
	cfg := &Config{}
	cfg.ApplyDefaults()

	remote := cfg.Storage.Remote
	assert.False(t, remote.Enabled, "off by default")
	assert.Equal(t, "auto", remote.Region)
	require.NotNil(t, remote.PathStyle)
	assert.True(t, *remote.PathStyle, "path-style is the S3-compatible default")
	assert.Equal(t, 1, remote.Upload.MaxConcurrency)
	assert.Equal(t, 60, remote.Upload.ScanIntervalS)
	assert.Equal(t, 900, remote.Upload.MinAgeS)
	assert.Equal(t, 5000, remote.Upload.BacklogLimit)
	assert.Equal(t, "recordings", remote.Prefix)
	assert.Equal(t, 0, remote.Evict.AfterDays, "0 = upload-only, never evict")
}

func TestRemoteStorageValidation(t *testing.T) {
	t.Parallel()

	t.Run("disabled skips field checks", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Storage: StorageConfig{Remote: RemoteStorageConfig{}}}
		cfg.ApplyDefaults()
		assert.NoError(t, Validate(cfg))
	})

	t.Run("enabled requires endpoint bucket creds", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Storage: StorageConfig{Remote: *newRemoteConfig()}}
		cfg.ApplyDefaults()
		cfg.Storage.Remote.EndpointURL = ""
		err := Validate(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "endpoint_url")

		cfg = &Config{Storage: StorageConfig{Remote: *newRemoteConfig()}}
		cfg.ApplyDefaults()
		cfg.Storage.Remote.Bucket = ""
		err = Validate(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "bucket")

		cfg = &Config{Storage: StorageConfig{Remote: *newRemoteConfig()}}
		cfg.ApplyDefaults()
		cfg.Storage.Remote.SecretAccessKey = ""
		err = Validate(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "secret_access_key")
	})

	t.Run("valid config passes", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Storage: StorageConfig{Remote: *newRemoteConfig()}}
		cfg.ApplyDefaults()
		assert.NoError(t, Validate(cfg))
	})

	t.Run("bad endpoint url rejected", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Storage: StorageConfig{Remote: *newRemoteConfig()}}
		cfg.ApplyDefaults()
		cfg.Storage.Remote.EndpointURL = "not a url"
		err := Validate(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "endpoint_url")
	})

	t.Run("out-of-range knobs rejected", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Storage: StorageConfig{Remote: *newRemoteConfig()}}
		cfg.ApplyDefaults()
		cfg.Storage.Remote.Upload.MaxConcurrency = 99
		err := Validate(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "max_concurrency")

		cfg = &Config{Storage: StorageConfig{Remote: *newRemoteConfig()}}
		cfg.ApplyDefaults()
		cfg.Storage.Remote.Upload.MinAgeS = 10
		err = Validate(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "min_age_s")
	})
}

func TestExpandEnvRefs(t *testing.T) {
	// Not parallel: t.Setenv forbids it.
	t.Setenv("S3_TEST_KEY", "abc123")
	t.Setenv("S3_TEST_SECRET", "shhh")

	assert.Equal(t, "abc123", ExpandEnvRefs("${S3_TEST_KEY}"))
	assert.Equal(t, "abc123:shhh", ExpandEnvRefs("${S3_TEST_KEY}:${S3_TEST_SECRET}"))
	assert.Equal(t, "", ExpandEnvRefs("${S3_TEST_UNSET_VAR_9183}"), "unset var expands to empty so validation catches it")
	assert.Equal(t, "plain", ExpandEnvRefs("plain"))
	assert.Equal(t, "${1bad}", ExpandEnvRefs("${1bad}"), "invalid name left untouched")
	assert.Equal(t, "pre-abc123-post", ExpandEnvRefs("pre-${S3_TEST_KEY}-post"))
}
