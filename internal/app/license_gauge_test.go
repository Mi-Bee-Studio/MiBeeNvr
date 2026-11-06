package app

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/license"
	"github.com/stretchr/testify/require"
)

// TestLicenseStateGaugeValues pins the nvr_license_state enum mapping
// (higher = better, so alerts threshold on < 3 when licensing is enforced).
func TestLicenseStateGaugeValues(t *testing.T) {
	t.Parallel()

	none := license.NewManager("dev-g1")
	require.NoError(t, none.Configure("", "", true))
	require.Equal(t, 0.0, licenseStateGaugeValue(none)())

	pub, priv, err := license.GenerateKeyPair()
	require.NoError(t, err)
	mint := func(t *testing.T, deviceID string, expires time.Time) string {
		t.Helper()
		doc, err := license.Sign(priv, &license.Document{
			Product: "mibeenvr", DeviceID: deviceID, Plan: "p2p-remote",
			Features: []string{"p2p"}, Serial: "gauge-test",
			NotBefore: time.Now().Add(-time.Minute), ExpiresAt: expires, IssuedAt: time.Now(),
		})
		require.NoError(t, err)
		return doc
	}
	pubB64 := base64.RawStdEncoding.EncodeToString(pub)

	valid := license.NewManager("dev-g2")
	require.NoError(t, valid.Configure(mint(t, "dev-g2", time.Now().Add(30*24*time.Hour)), pubB64, true))
	require.Equal(t, 4.0, licenseStateGaugeValue(valid)())

	expiring := license.NewManager("dev-g3")
	require.NoError(t, expiring.Configure(mint(t, "dev-g3", time.Now().Add(time.Hour)), pubB64, true))
	require.Equal(t, 3.0, licenseStateGaugeValue(expiring)())

	expired := license.NewManager("dev-g4")
	require.NoError(t, expired.Configure(mint(t, "dev-g4", time.Now().Add(-time.Hour)), pubB64, true))
	require.Equal(t, 2.0, licenseStateGaugeValue(expired)())
}
