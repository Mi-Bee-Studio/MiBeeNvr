//go:build gb35114

package gb28181

// Certificate-generation tests for the GB 35114-2017 A-level pilot kit
// (#452): generated material must load through the library's own loaders,
// lay out exactly as the #707 seam consumes, keep private keys owner-only,
// and — the decisive test — carry a full Challenge/VerifyRegister/VerifyOK
// handshake with matching VKEKs on both sides.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mickeyzzc/gb28181-go/security35114"
	"github.com/stretchr/testify/require"
)

const (
	certgenPlatformID = "34020000002000000001"
	certgenDeviceID   = "34020000001320000001"
	certgenDeviceID2  = "34020000001320000002"
)

func TestGenerateGB35114MaterialHandshake(t *testing.T) {
	out := t.TempDir()
	res, err := GenerateGB35114Material(GB35114CertRequest{
		PlatformID: certgenPlatformID,
		DeviceIDs:  []string{certgenDeviceID},
		OutDir:     out,
	})
	require.NoError(t, err)

	// Layout: the #707 seam's own loaders must accept every file as-is.
	identity, err := security35114.LoadIdentityFromFiles(res.PlatformCert, res.PlatformKey)
	require.NoError(t, err, "library must load the generated platform identity")
	deviceCerts, err := loadDeviceCerts(res.DeviceCertsDir)
	require.NoError(t, err)
	require.Len(t, deviceCerts, 1)
	require.Contains(t, deviceCerts, certgenDeviceID,
		"device certs must be <deviceID>.pem so loadDeviceCerts binds them by ID")

	// Private keys stay owner-only.
	for _, key := range []string{
		res.PlatformKey,
		filepath.Join(res.DeviceCertsDir, certgenDeviceID+".key"),
	} {
		fi, err := os.Stat(key)
		require.NoError(t, err)
		require.Zero(t, fi.Mode().Perm()&0o077, "private key %s must be 0600", key)
	}

	// The full A-level REGISTER handshake over the generated material.
	plat, err := security35114.NewPlatform(security35114.PlatformConfig{
		ServerID:    certgenPlatformID,
		Identity:    identity,
		DeviceCerts: deviceCerts,
	})
	require.NoError(t, err)

	devIdentity, err := security35114.LoadIdentityFromFiles(
		filepath.Join(res.DeviceCertsDir, certgenDeviceID+".pem"),
		filepath.Join(res.DeviceCertsDir, certgenDeviceID+".key"))
	require.NoError(t, err, "library must load the generated device identity")
	dev, err := security35114.New(security35114.Options{
		Device:            devIdentity,
		PlatformCert:      identity.Certificate,
		DeviceID:          certgenDeviceID,
		ServerID:          certgenPlatformID,
		IncludeDeviceCert: true,
	})
	require.NoError(t, err)

	challenge, err := plat.Challenge(certgenDeviceID, dev.InitialAuthorization())
	require.NoError(t, err, "platform must challenge the capability announcement")
	auth, err := dev.AuthorizeWithChallenge(challenge)
	require.NoError(t, err, "device must answer the challenge")
	cryptkey, err := plat.VerifyRegister(certgenDeviceID, auth)
	require.NoError(t, err, "platform must accept the device's signed REGISTER")
	require.NoError(t, dev.VerifyOK(cryptkey), "device must open the cryptkey envelope")

	require.NotNil(t, dev.VKEK())
	require.Equal(t, plat.VKEK(certgenDeviceID), dev.VKEK(),
		"both sides must derive the same VKEK — the handshake's whole point")
}

func TestGenerateGB35114MaterialMultipleDevices(t *testing.T) {
	res, err := GenerateGB35114Material(GB35114CertRequest{
		PlatformID: certgenPlatformID,
		DeviceIDs:  []string{certgenDeviceID, certgenDeviceID2},
		OutDir:     t.TempDir(),
	})
	require.NoError(t, err)
	deviceCerts, err := loadDeviceCerts(res.DeviceCertsDir)
	require.NoError(t, err)
	require.Len(t, deviceCerts, 2)
}

func TestGenerateGB35114MaterialValidation(t *testing.T) {
	_, err := GenerateGB35114Material(GB35114CertRequest{
		DeviceIDs: []string{certgenDeviceID},
		OutDir:    t.TempDir(),
	})
	require.ErrorContains(t, err, "platform")

	_, err = GenerateGB35114Material(GB35114CertRequest{
		PlatformID: "not-a-gb-id",
		OutDir:     t.TempDir(),
	})
	require.ErrorContains(t, err, "20")

	_, err = GenerateGB35114Material(GB35114CertRequest{
		PlatformID: certgenPlatformID,
		DeviceIDs:  []string{"short"},
		OutDir:     t.TempDir(),
	})
	require.ErrorContains(t, err, "20")
}
