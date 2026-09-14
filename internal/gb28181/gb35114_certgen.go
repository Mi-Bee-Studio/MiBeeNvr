//go:build gb35114

package gb28181

// GB 35114-2017 A-level pilot certificate kit (#452): self-signed SM2
// identities for the platform and its devices, laid out exactly as the
// #707 seam consumes them. Real deployments provision from a CA (CFCA et
// al.); pilots and labs start here — the library's platform-side
// verification is cryptographic (SM2 signatures over the GM/T 0015-2012
// profile), with trust anchored by the device-ID→certificate mapping, so
// self-signed material is functional, not a toy.

import (
	"crypto/rand"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/emmansun/gmsm/sm2"
	"github.com/emmansun/gmsm/smx509"
)

// GB35114CertRequest describes one pilot-kit issuance run.
type GB35114CertRequest struct {
	// PlatformID is this NVR's 20-digit GB platform ID (gb28181.server_id).
	PlatformID string
	// DeviceIDs are the 20-digit GB IDs of devices to issue for.
	DeviceIDs []string
	// OutDir receives platform.pem/platform.key and devices/<id>.pem|.key.
	OutDir string
	// Validity bounds the certificates. Zero = 10 years.
	Validity time.Duration
	// Rand overrides the entropy source (tests). Nil = crypto/rand.
	Rand io.Reader
}

// GB35114CertResult reports the written material paths. DeviceCertsDir is
// the value for gb28181.security35114.device_certs_dir.
type GB35114CertResult struct {
	PlatformCert   string
	PlatformKey    string
	DeviceCertsDir string
}

const gb35114ValidityDefault = 10 * 365 * 24 * time.Hour

// GenerateGB35114Material issues a self-signed SM2 platform identity plus
// device identities signed by the platform (pilot mini-CA), all as
// PKCS#8 keys and GM/T 0015-2012 certificates the library's loaders
// (LoadIdentityFromFiles / LoadCertificate) accept directly.
func GenerateGB35114Material(req GB35114CertRequest) (GB35114CertResult, error) {
	if err := validateGBID(req.PlatformID, "platform"); err != nil {
		return GB35114CertResult{}, err
	}
	for _, id := range req.DeviceIDs {
		if err := validateGBID(id, "device"); err != nil {
			return GB35114CertResult{}, err
		}
	}
	if req.OutDir == "" {
		return GB35114CertResult{}, errors.New("gb35114: out dir is required")
	}
	rd := req.Rand
	if rd == nil {
		rd = rand.Reader
	}
	validity := req.Validity
	if validity <= 0 {
		validity = gb35114ValidityDefault
	}
	devicesDir := filepath.Join(req.OutDir, "devices")
	if err := os.MkdirAll(devicesDir, 0o755); err != nil {
		return GB35114CertResult{}, fmt.Errorf("gb35114: create %s: %w", devicesDir, err)
	}

	now := time.Now()
	platformKey, err := sm2.GenerateKey(rd)
	if err != nil {
		return GB35114CertResult{}, fmt.Errorf("gb35114: platform key: %w", err)
	}
	platformTpl := gb35114CertTemplate(rd, req.PlatformID, now, validity, true)
	platformDER, err := smx509.CreateCertificate(rd, platformTpl, platformTpl,
		&platformKey.PublicKey, platformKey)
	if err != nil {
		return GB35114CertResult{}, fmt.Errorf("gb35114: platform cert: %w", err)
	}
	platformCert, err := smx509.ParseCertificate(platformDER)
	if err != nil {
		return GB35114CertResult{}, fmt.Errorf("gb35114: parse back platform cert: %w", err)
	}

	res := GB35114CertResult{
		PlatformCert:   filepath.Join(req.OutDir, "platform.pem"),
		PlatformKey:    filepath.Join(req.OutDir, "platform.key"),
		DeviceCertsDir: devicesDir,
	}
	if err := writeCertPEM(res.PlatformCert, platformDER); err != nil {
		return GB35114CertResult{}, err
	}
	if err := writeKeyPKCS8(res.PlatformKey, platformKey); err != nil {
		return GB35114CertResult{}, err
	}

	for _, id := range req.DeviceIDs {
		key, err := sm2.GenerateKey(rd)
		if err != nil {
			return GB35114CertResult{}, fmt.Errorf("gb35114: device %s key: %w", id, err)
		}
		tpl := gb35114CertTemplate(rd, id, now, validity, false)
		der, err := smx509.CreateCertificate(rd, tpl, platformCert,
			&key.PublicKey, platformKey)
		if err != nil {
			return GB35114CertResult{}, fmt.Errorf("gb35114: device %s cert: %w", id, err)
		}
		if err := writeCertPEM(filepath.Join(devicesDir, id+".pem"), der); err != nil {
			return GB35114CertResult{}, err
		}
		if err := writeKeyPKCS8(filepath.Join(devicesDir, id+".key"), key); err != nil {
			return GB35114CertResult{}, err
		}
	}
	return res, nil
}

// gb35114CertTemplate builds the GM/T 0015-2012 profile: SM2 key, SM3
// digest, CN = GB ID. CA=true only for the self-signed platform identity.
func gb35114CertTemplate(rd io.Reader, id string, now time.Time, validity time.Duration, ca bool) *smx509.Certificate {
	serial, err := rand.Int(rd, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		serial = big.NewInt(time.Now().UnixNano())
	}
	tpl := &smx509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: id, Organization: []string{"MiBee NVR pilot"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(validity),
		KeyUsage:              smx509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		SignatureAlgorithm:    smx509.SM2WithSM3,
	}
	if ca {
		tpl.IsCA = true
		tpl.KeyUsage |= smx509.KeyUsageCertSign
	}
	return tpl
}

func writeCertPEM(path string, der []byte) error {
	return writePEM(path, "CERTIFICATE", der, 0o644)
}

func writeKeyPKCS8(path string, key *sm2.PrivateKey) error {
	der, err := smx509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("gb35114: marshal key %s: %w", path, err)
	}
	return writePEM(path, "PRIVATE KEY", der, 0o600)
}

func writePEM(path, blockType string, der []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("gb35114: create %s: %w", path, err)
	}
	pemStr := string(pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}))
	if _, err := f.WriteString(pemStr); err != nil {
		f.Close()
		return fmt.Errorf("gb35114: write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("gb35114: close %s: %w", path, err)
	}
	return nil
}

// validateGBID enforces the 20-digit GB/T 28181 identifier shape — a
// mistyped ID here would silently mis-bind the device-cert trust anchor.
func validateGBID(id, what string) error {
	if len(id) != 20 {
		return fmt.Errorf("gb35114: %s ID must be 20 digits, got %q", what, id)
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return fmt.Errorf("gb35114: %s ID must be digits only, got %q", what, id)
		}
	}
	return nil
}
