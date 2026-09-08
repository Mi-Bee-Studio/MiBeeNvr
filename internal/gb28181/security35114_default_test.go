//go:build !gb35114

package gb28181

import (
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// The GB 35114 seam must be inert in default builds: no authenticator
// injected, no error, even when the config section is enabled (the tagged
// twin of this test lives in security35114_enabled_test.go).
func TestApplySecurity35114_DefaultBuildIsNoop(t *testing.T) {
	sip := SIPConfig(config.GB28181ServerConfig{Enabled: true, ServerID: "34020000002000000001"})
	before := sip.RegisterAuthenticator
	if err := ApplySecurity35114(&sip, config.GB28181ServerConfig{Security35114: config.GB35114SecurityConfig{Enabled: true}}); err != nil {
		t.Fatalf("default-build seam must never error, got %v", err)
	}
	if sip.RegisterAuthenticator != before {
		t.Fatal("default-build seam must not inject a RegisterAuthenticator")
	}
}
