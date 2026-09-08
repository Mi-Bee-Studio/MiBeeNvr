//go:build gb35114

package gb28181

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// Tagged-build seam test (#707): the section loads the platform SM2 identity
// from testdata (sourced from gb28181-go's own MIT-licensed test fixtures),
// pre-provisions device certs by filename, and injects the authenticator.
func TestApplySecurity35114_TaggedBuildWiresAuthenticator(t *testing.T) {
	cfg := config.GB28181ServerConfig{
		Enabled:  true,
		ServerID: "34020000002000000001",
		Security35114: config.GB35114SecurityConfig{
			Enabled:        true,
			PlatformCert:   filepath.Join("testdata", "platform_cert.pem"),
			PlatformKey:    filepath.Join("testdata", "platform_key.pem"),
			DeviceCertsDir: filepath.Join("testdata", "device_certs"),
		},
	}
	sip := SIPConfig(cfg)
	if err := ApplySecurity35114(&sip, cfg); err != nil {
		t.Fatalf("seam must wire with valid fixtures: %v", err)
	}
	if sip.RegisterAuthenticator == nil {
		t.Fatal("RegisterAuthenticator must be injected in tagged builds")
	}
	// The wired platform answers the RegisterAuthenticator contract.
	if _, err := sip.RegisterAuthenticator.Challenge("34020000001320000001", ""); err != nil {
		t.Fatalf("Challenge must answer a first REGISTER: %v", err)
	}
}

// Disabled section stays inert even in tagged builds.
func TestApplySecurity35114_TaggedBuildDisabledNoop(t *testing.T) {
	cfg := config.GB28181ServerConfig{Enabled: true, ServerID: "34020000002000000001"}
	sip := SIPConfig(cfg)
	if err := ApplySecurity35114(&sip, cfg); err != nil {
		t.Fatalf("disabled section must be a no-op, got %v", err)
	}
	if sip.RegisterAuthenticator != nil {
		t.Fatal("disabled section must not inject an authenticator")
	}
}

// A bad device cert fails the boot — trust-on-first-use must not silently
// replace an operator's explicit (broken) provisioning.
func TestApplySecurity35114_BadDeviceCertFails(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "34020000001320000002.pem")
	if err := os.WriteFile(bad, []byte("not a pem"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.GB28181ServerConfig{
		Enabled:  true,
		ServerID: "34020000002000000001",
		Security35114: config.GB35114SecurityConfig{
			Enabled:        true,
			PlatformCert:   filepath.Join("testdata", "platform_cert.pem"),
			PlatformKey:    filepath.Join("testdata", "platform_key.pem"),
			DeviceCertsDir: dir,
		},
	}
	sip := SIPConfig(cfg)
	if err := ApplySecurity35114(&sip, cfg); err == nil {
		t.Fatal("unparseable device cert must fail, not silently skip")
	}
}
