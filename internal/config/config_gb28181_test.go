package config

import (
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

func validGB28181Config() *Config {
	return &Config{
		GB28181: GB28181ServerConfig{
			Enabled:           true,
			SIPListen:         ":5060",
			ServerID:          "34020000002000000001",
			Realm:             "3402000000",
			Password:          "gb-secret",
			PortRange:         "30000-30050",
			AllowedDeviceIDs:  []string{"34020000001310000001"},
			HeartbeatInterval: "60s",
			CatalogInterval:   "30m",
			TCPFraming:        "auto",
		},
		Cameras: []CameraConfig{{
			ID:       "gb-cam",
			Protocol: string(model.ProtoGB28181),
			Encoding: "h264",
			GB28181: GB28181ChannelConfig{
				DeviceID:     "34020000001310000001",
				ChannelID:    "34020000001320000001",
				Manufacturer: "TestVendor",
			},
		}},
	}
}

func TestValidate_GB28181(t *testing.T) {
	t.Run("valid enabled server + gb28181 camera passes", func(t *testing.T) {
		cfg := validGB28181Config()
		cfg.ApplyDefaults()
		require.NoError(t, Validate(cfg))
	})

	t.Run("disabled server passes with empty fields", func(t *testing.T) {
		cfg := &Config{}
		cfg.ApplyDefaults()
		require.NoError(t, Validate(cfg))
	})

	t.Run("enabled with empty server_id rejected", func(t *testing.T) {
		cfg := validGB28181Config()
		cfg.GB28181.ServerID = ""
		cfg.ApplyDefaults()
		err := Validate(cfg)
		require.Error(t, err)
		require.Contains(t, err.Error(), "server_id")
	})

	t.Run("enabled with empty sip_listen rejected", func(t *testing.T) {
		cfg := validGB28181Config()
		cfg.ApplyDefaults()
		cfg.GB28181.SIPListen = ""
		err := Validate(cfg)
		require.Contains(t, err.Error(), "sip_listen")
	})

	t.Run("enabled with empty password rejected", func(t *testing.T) {
		cfg := validGB28181Config()
		cfg.GB28181.Password = ""
		cfg.ApplyDefaults()
		err := Validate(cfg)
		require.Error(t, err)
		require.Contains(t, err.Error(), "password")
	})

	t.Run("invalid port_range rejected", func(t *testing.T) {
		invalid := []string{
			"30000",         // no dash
			"abc-def",       // non-numeric
			"30000-",        // missing end
			"-30050",        // missing start
			"0-65535",       // start below 1
			"30000-70000",   // end above 65535
			"40000-30000",   // start > end
			"30000-30050-1", // extra dash
		}
		for _, pr := range invalid {
			cfg := validGB28181Config()
			cfg.GB28181.PortRange = pr
			cfg.ApplyDefaults()
			err := Validate(cfg)
			require.Error(t, err, "port_range %q should be rejected", pr)
			require.Contains(t, err.Error(), "port_range")
		}
	})

	t.Run("unknown tcp_framing rejected", func(t *testing.T) {
		cfg := validGB28181Config()
		cfg.GB28181.TCPFraming = "weird"
		cfg.ApplyDefaults()
		err := Validate(cfg)
		require.Error(t, err)
		require.Contains(t, err.Error(), "tcp_framing")
	})

	t.Run("gb28181 camera missing device_id rejected", func(t *testing.T) {
		cfg := validGB28181Config()
		cfg.Cameras[0].GB28181.DeviceID = ""
		cfg.ApplyDefaults()
		err := Validate(cfg)
		require.Error(t, err)
		require.Contains(t, err.Error(), "device_id")
	})

	t.Run("gb28181 camera missing channel_id rejected", func(t *testing.T) {
		cfg := validGB28181Config()
		cfg.Cameras[0].GB28181.ChannelID = ""
		cfg.ApplyDefaults()
		err := Validate(cfg)
		require.Error(t, err)
		require.Contains(t, err.Error(), "channel_id")
	})

	t.Run("gb28181 camera without url passes", func(t *testing.T) {
		cfg := validGB28181Config()
		cfg.Cameras[0].URL = ""
		cfg.ApplyDefaults()
		require.NoError(t, Validate(cfg))
	})

	t.Run("defaults applied to gb28181 server block", func(t *testing.T) {
		cfg := &Config{}
		cfg.ApplyDefaults()
		require.Equal(t, ":5060", cfg.GB28181.SIPListen)
		require.Equal(t, "60s", cfg.GB28181.HeartbeatInterval)
		require.Equal(t, "30m", cfg.GB28181.CatalogInterval)
		require.Equal(t, "30000-30050", cfg.GB28181.PortRange)
		require.Equal(t, "auto", cfg.GB28181.TCPFraming)
	})
}
