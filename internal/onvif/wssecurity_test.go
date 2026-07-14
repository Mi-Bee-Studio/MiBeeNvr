package onvif

import (
	"crypto/sha1" //nolint:gosec // SHA1 mandated by the ONVIF UsernameToken profile
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// TestWsseDigestUsernameToken_Format verifies the header has all required
// elements in the correct WSS structure, with PasswordDigest type and a
// Base64Binary nonce.
func TestWsseDigestUsernameToken_Format(t *testing.T) {
	t.Helper()
	created := time.Date(2026, 1, 15, 12, 30, 45, 0, time.UTC)
	nonce := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	header := wsseDigestUsernameToken("admin", "secret", created, nonce)

	checks := []string{
		`<wsse:Username>admin</wsse:Username>`,
		`Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordDigest"`,
		`EncodingType="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-soap-message-security-1.0#Base64Binary"`,
		`<wsu:Created>2026-01-15T12:30:45Z</wsu:Created>`,
	}
	for _, want := range checks {
		if !strings.Contains(header, want) {
			t.Errorf("header missing %q\ngot: %s", want, header)
		}
	}
}

// TestWsseDigestUsernameToken_DigestMath verifies the digest equals
// base64(sha1(nonce + created + password)) per OASIS UsernameTokenProfile 1.0.
// This is the load-bearing assertion — a wrong digest silently fails auth.
func TestWsseDigestUsernameToken_DigestMath(t *testing.T) {
	t.Helper()
	created := time.Date(2026, 1, 15, 12, 30, 45, 0, time.UTC)
	nonce := []byte{0xAA, 0xBB, 0xCC}
	password := "p@ssw0rd"
	header := wsseDigestUsernameToken("user", password, created, nonce)

	// Recompute the expected digest independently.
	createdStr := created.UTC().Format(time.RFC3339)
	input := append(append([]byte{}, nonce...), []byte(createdStr)...)
	input = append(input, []byte(password)...)
	sum := sha1.Sum(input) //nolint:gosec // SHA1 mandated by the ONVIF profile
	wantDigest := base64.StdEncoding.EncodeToString(sum[:])

	if !strings.Contains(header, wantDigest) {
		t.Errorf("header does not contain the expected digest %q\nheader: %s", wantDigest, header)
	}
}

// TestWsseDigestUsernameToken_XmlEscape ensures a username with XML-special
// characters is escaped (a raw < or & would break the SOAP envelope).
func TestWsseDigestUsernameToken_XmlEscape(t *testing.T) {
	t.Helper()
	header := wsseDigestUsernameToken("a<b>&c", "pw", time.Now().UTC(), []byte{1, 2})
	if strings.Contains(header, "<wsse:Username>a<b>") {
		t.Error("username was not XML-escaped — raw < in output would break the envelope")
	}
	if !strings.Contains(header, "<wsse:Username>a&lt;b&gt;&amp;c</wsse:Username>") {
		t.Errorf("username not escaped correctly\nheader: %s", header)
	}
}

// TestWsseDigestUsernameToken_DifferentTimeDifferentDigest confirms the digest
// actually depends on the timestamp — the whole point of skew correction.
func TestWsseDigestUsernameToken_DifferentTimeDifferentDigest(t *testing.T) {
	t.Helper()
	nonce := []byte{1, 2, 3, 4}
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	h1 := wsseDigestUsernameToken("u", "p", t1, nonce)
	h2 := wsseDigestUsernameToken("u", "p", t2, nonce)
	// Extract the password digest value from each (between PasswordDigest tag).
	d1 := extractDigest(t, h1)
	d2 := extractDigest(t, h2)
	if d1 == d2 {
		t.Error("digests identical despite different timestamps — skew correction would be a no-op")
	}
}

// TestMeasureClockSkew_UnreachableReturnsZero verifies the skew measurement is
// best-effort: an unreachable device returns 0 (no skew) rather than erroring,
// so callers fall back to local-time behavior gracefully.
func TestMeasureClockSkew_UnreachableReturnsZero(t *testing.T) {
	t.Helper()
	c := &Client{endpoint: "http://127.0.0.1:1/onvif/device_service"}
	skew := c.measureClockSkew(t.Context(), "http://127.0.0.1:1/onvif/device_service")
	if skew != 0 {
		t.Errorf("expected 0 skew for unreachable device, got %v", skew)
	}
}

// extractDigest pulls the PasswordDigest value out of the header for comparison.
func extractDigest(t *testing.T, header string) string {
	t.Helper()
	const marker = `#PasswordDigest">`
	i := strings.Index(header, marker)
	if i < 0 {
		t.Fatalf("no PasswordDigest in header: %s", header)
	}
	rest := header[i+len(marker):]
	end := strings.Index(rest, "</wsse:Password>")
	if end < 0 {
		t.Fatalf("no closing Password tag: %s", header)
	}
	return rest[:end]
}
