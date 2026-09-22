package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The desktop local_bypass path must refuse browser requests that originate
// from another page: a malicious site the user visited can still issue simple
// requests to http://localhost:9090, and those carry Sec-Fetch-Site:
// cross-site. Non-browser clients send no Sec-Fetch-Site and stay eligible.
func TestIsBypassEligible_SecFetchSite(t *testing.T) {
	t.Parallel()

	req := func(header string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://localhost:9090/api/health", nil)
		r.RemoteAddr = "127.0.0.1:12345"
		if header != "" {
			r.Header.Set("Sec-Fetch-Site", header)
		}
		return r
	}

	cases := []struct {
		header string
		want   bool
	}{
		{"", true},             // non-browser client (curl, tray helper)
		{"none", true},         // user typed the URL directly
		{"same-origin", true},  // the SPA itself
		{"same-site", true},    // same-site subresource
		{"cross-site", false},  // page on another origin
		{"Cross-Site", false},  // case-insensitive match must also reject
		{"bogus-value", false}, // unknown values are not trusted
	}
	for _, tc := range cases {
		if got := IsBypassEligible(req(tc.header)); got != tc.want {
			t.Errorf("Sec-Fetch-Site %q: IsBypassEligible = %v, want %v", tc.header, got, tc.want)
		}
	}
}
