//go:build gb35114

package gb28181

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/emmansun/gmsm/smx509"
	gbsip "github.com/mickeyzzc/gb28181-go/platform/sip"
	"github.com/mickeyzzc/gb28181-go/security35114"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// This file is the `-tags gb35114` half of the seam (#707). It loads the
// platform's SM2 signing identity plus any pre-provisioned device
// certificates, constructs the library's GB 35114 A-level Platform, and
// injects it as the SIP server's RegisterAuthenticator. Devices registering
// with a Digest Authorization keep flowing through gb28181.password — the
// library routes by scheme and regression-tests the coexistence.

// ApplySecurity35114 wires the GB 35114 A-level REGISTER authenticator into
// the SIP server config when gb28181.security35114.enabled is set. Returns an
// error (boot-fatal at the builders.go call site) when the section is enabled
// but the certificate material cannot be loaded — a half-configured security
// layer must not come up silently.
func ApplySecurity35114(sip *gbsip.Config, cfg config.GB28181ServerConfig) error {
	if !cfg.Security35114.Enabled {
		return nil
	}
	sc := cfg.Security35114

	identity, err := security35114.LoadIdentityFromFiles(sc.PlatformCert, sc.PlatformKey)
	if err != nil {
		return fmt.Errorf("gb35114: load platform identity: %w", err)
	}

	deviceCerts, err := loadDeviceCerts(sc.DeviceCertsDir)
	if err != nil {
		return err
	}
	if len(deviceCerts) > 0 {
		gb35114Logger.Info("GB35114 A-level enabled", "device_certs", len(deviceCerts))
	} else {
		gb35114Logger.Info("GB35114 A-level enabled (no pre-provisioned device certs — devices must announce via Capability cnonce)")
	}

	plat, err := security35114.NewPlatform(security35114.PlatformConfig{
		ServerID:    cfg.ServerID,
		Identity:    identity,
		DeviceCerts: deviceCerts,
	})
	if err != nil {
		return fmt.Errorf("gb35114: construct platform: %w", err)
	}
	sip.RegisterAuthenticator = plat
	return nil
}

// loadDeviceCerts reads <deviceID>.pem files from dir (empty dir = none).
// The filename minus extension is the 20-digit GB device ID the certificate
// is trusted for. Unparseable files fail the boot — a typo'd cert must not
// silently downgrade that device to trust-on-first-use.
func loadDeviceCerts(dir string) (map[string]*smx509.Certificate, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("gb35114: read device certs dir %q: %w", dir, err)
	}
	certs := make(map[string]*smx509.Certificate)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := filepath.Ext(e.Name())
		if ext != ".pem" && ext != ".crt" {
			continue
		}
		deviceID := strings.TrimSuffix(e.Name(), ext)
		cert, err := security35114.LoadCertificate(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("gb35114: load device cert %s: %w", e.Name(), err)
		}
		certs[deviceID] = cert
	}
	return certs, nil
}
