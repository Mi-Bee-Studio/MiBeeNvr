package config

// Zero-touch onboarding config validation (#1120) — the section is
// default-off and inert; validation only bites when enabled.

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateOnboarding_DisabledInert(t *testing.T) {
	cfg := &Config{}
	cfg.CameraOnboarding = CameraOnboardingConfig{Enabled: false}
	require.NoError(t, ValidateOnboarding(cfg), "a disabled section must not be validated")
}

func TestValidateOnboarding_SubnetRequired(t *testing.T) {
	cfg := &Config{}
	cfg.CameraOnboarding = CameraOnboardingConfig{Enabled: true}
	require.ErrorContains(t, ValidateOnboarding(cfg), "default_subnet must be CIDR")

	cfg.CameraOnboarding.DefaultSubnet = "192.168.1.0/24"
	require.NoError(t, ValidateOnboarding(cfg), "minimal enabled config: subnet alone is enough (DHCP target, default alias)")
}

func TestValidateOnboarding_AliasDefaultsToSubnetLast(t *testing.T) {
	cfg := &Config{}
	cfg.CameraOnboarding = CameraOnboardingConfig{Enabled: true, DefaultSubnet: "192.168.1.0/24"}
	require.Equal(t, "192.168.1.254", cfg.CameraOnboarding.ResolveOnboardingAliasIP().String())

	cfg.CameraOnboarding.AliasIP = "192.168.1.250"
	require.Equal(t, "192.168.1.250", cfg.CameraOnboarding.ResolveOnboardingAliasIP().String())
}

func TestValidateOnboarding_AliasMustLiveInsideSubnet(t *testing.T) {
	cfg := &Config{}
	cfg.CameraOnboarding = CameraOnboardingConfig{
		Enabled:       true,
		DefaultSubnet: "192.168.1.0/24",
		AliasIP:       "10.0.0.9",
	}
	require.ErrorContains(t, ValidateOnboarding(cfg), "must be inside default_subnet")
}

func TestValidateOnboarding_StaticTargetRequired(t *testing.T) {
	cfg := &Config{}
	cfg.CameraOnboarding = CameraOnboardingConfig{
		Enabled:       true,
		DefaultSubnet: "192.168.1.0/24",
		TargetDHCP:    boolPtr(false),
	}
	require.ErrorContains(t, ValidateOnboarding(cfg), "target_static_ip")

	cfg.CameraOnboarding.TargetStaticIP = "192.168.63.77"
	require.NoError(t, ValidateOnboarding(cfg))
}

func TestValidateOnboarding_ScanIntervalFloors(t *testing.T) {
	cfg := &Config{}
	cfg.CameraOnboarding = CameraOnboardingConfig{Enabled: true, DefaultSubnet: "192.168.1.0/24", ScanIntervalS: 30}
	require.ErrorContains(t, ValidateOnboarding(cfg), "below 60 would hammer the LAN")

	cfg.CameraOnboarding.ScanIntervalS = 300
	require.NoError(t, ValidateOnboarding(cfg))
}
