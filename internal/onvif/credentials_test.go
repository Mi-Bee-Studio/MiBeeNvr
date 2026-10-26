package onvif

import (
	"context"
	"crypto/sha1" //nolint:gosec // ONVIF digest auth mandates SHA-1
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// between extracts the substring between two markers (test helper).
func between(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	rest := s[i+len(start):]
	j := strings.Index(rest, end)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// TestCredentialProber_TryVerdicts walks the three verdicts against a fake
// device: accepted (200 no fault), rejected (401 then an auth fault on the
// PasswordText retry), inconclusive (transport dead).
func TestCredentialProber_TryVerdicts(t *testing.T) {
	t.Helper()
	// Device whose clock is 30 minutes ahead — ensures digests still work via
	// skew compensation (prober measures once and reuses).
	deviceClock := time.Now().UTC().Add(30 * time.Minute).Truncate(time.Second)

	// Real server-side digest validation: base64(sha1(nonce+created+password))
	// with the one true password "right" — mirrors what a real camera checks.
	validDigest := func(body string) bool {
		nonceB64 := between(body, "<wsse:Nonce EncodingType=\"http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-soap-message-security-1.0#Base64Binary\">", "</wsse:Nonce>")
		created := between(body, "<wsu:Created>", "</wsu:Created>")
		gotDigest := between(body, `#PasswordDigest">`, "</wsse:Password>")
		nonce, err := base64.StdEncoding.DecodeString(nonceB64)
		if err != nil {
			return false
		}
		h := sha1.New() //nolint:gosec // ONVIF digest auth mandates SHA-1
		h.Write(nonce)
		h.Write([]byte(created))
		h.Write([]byte("right"))
		want := base64.StdEncoding.EncodeToString(h.Sum(nil))
		return gotDigest != "" && gotDigest == want
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		if strings.Contains(string(body), "GetSystemDateAndTime") {
			_, _ = w.Write([]byte(systemDateAndTimeXML(deviceClock, "Manual", "")))
			return
		}
		sb := string(body)
		switch {
		case strings.Contains(sb, "#PasswordDigest") && validDigest(sb):
			_, _ = w.Write([]byte(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><GetUsersResponse><User>admin</User></GetUsersResponse></s:Body></s:Envelope>`))
		case strings.Contains(sb, "#PasswordText"):
			// PasswordText retry: only the true password passes.
			if strings.Contains(sb, "<wsse:Password Type=\"http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-username-token-profile-1.0#PasswordText\">right</wsse:Password>") {
				_, _ = w.Write([]byte(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><GetUsersResponse><User>admin</User></GetUsersResponse></s:Body></s:Envelope>`))
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><s:Fault><s:Value>s:Sender</s:Value><s:Subcode><s:Value>ter:NotAuthorized</s:Value></s:Subcode></s:Fault></s:Body></s:Envelope>`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer srv.Close()

	p := NewCredentialProber(srv.URL)

	// Rejected: digest 401 → PasswordText retry hits the auth fault → still rejected.
	if v := p.Try(context.Background(), "admin", "wrong"); v != CredentialRejected {
		t.Errorf("Try(wrong) = %v, want CredentialRejected", v)
	}
	// Accepted: digest math checks out for "right".
	if v := p.Try(context.Background(), "admin", "right"); v != CredentialAccepted {
		t.Errorf("Try(right) = %v, want CredentialAccepted", v)
	}

	// Inconclusive: dead endpoint.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("unreachable")
	}))
	deadURL := dead.URL
	dead.Close()
	p2 := NewCredentialProber(deadURL)
	if v := p2.Try(context.Background(), "admin", "x"); v != CredentialInconclusive {
		t.Errorf("Try(dead) = %v, want CredentialInconclusive", v)
	}
}

// TestCredentialProber_SetUserPasswordWire verifies the tds:SetUser body and
// that a fault surfaces as an error.
func TestCredentialProber_SetUserPasswordWire(t *testing.T) {
	t.Helper()
	deviceClock := time.Now().UTC().Truncate(time.Second)
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		if strings.Contains(string(body), "GetSystemDateAndTime") {
			_, _ = w.Write([]byte(systemDateAndTimeXML(deviceClock, "Manual", "")))
			return
		}
		gotBody = string(body)
		_, _ = w.Write([]byte(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><SetUserResponse/></s:Body></s:Envelope>`))
	}))
	defer srv.Close()

	p := NewCredentialProber(srv.URL)
	if err := p.SetUserPassword(context.Background(), "admin", "old", "admin", "NewPass!23"); err != nil {
		t.Fatalf("SetUserPassword: %v", err)
	}
	for _, want := range []string{
		"<tds:SetUser>",
		"<tt:Username>admin</tt:Username>",
		"<tt:Password>NewPass!23</tt:Password>",
	} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("SetUser body missing %q\nbody: %s", want, gotBody)
		}
	}
}

func TestCredentialProber_SetUserPasswordFault(t *testing.T) {
	t.Helper()
	deviceClock := time.Now().UTC().Truncate(time.Second)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		if strings.Contains(string(body), "GetSystemDateAndTime") {
			_, _ = w.Write([]byte(systemDateAndTimeXML(deviceClock, "Manual", "")))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><s:Fault><s:Value>s:Sender</s:Value></s:Fault></s:Body></s:Envelope>`))
	}))
	defer srv.Close()

	p := NewCredentialProber(srv.URL)
	if err := p.SetUserPassword(context.Background(), "admin", "old", "admin", "x"); err == nil {
		t.Error("expected fault error, got nil")
	}
}
