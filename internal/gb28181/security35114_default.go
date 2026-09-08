//go:build !gb35114

package gb28181

import (
	gbsip "github.com/mickeyzzc/gb28181-go/platform/sip"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// This file is the DEFAULT-build half of the GB 35114 A-level seam (#707).
// The real authenticator (security35114.Platform from gb28181-go, itself
// behind the same tag) only compiles with `-tags gb35114`; default builds
// carry this no-op so the wiring site in pkg/app stays build-tag-free.

// ApplySecurity35114 wires the GB 35114 A-level REGISTER authenticator into
// the SIP server config. In this default build it is a no-op: when the config
// enables the section it logs a warning (the setting is silently inert
// otherwise) so an operator notices the missing build tag instead of
// wondering why devices fail the A-level handshake.
func ApplySecurity35114(sip *gbsip.Config, cfg config.GB28181ServerConfig) error {
	if cfg.Security35114.Enabled {
		gb35114Logger.Warn("gb28181.security35114 is enabled but this binary was built without -tags gb35114 — GB35114 A-level registration is inert; Digest devices are unaffected")
	}
	return nil
}
