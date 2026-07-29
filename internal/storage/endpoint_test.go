package storage

import "testing"

// TestNormalizeOnvifEndpoint verifies the URL canonicalization helper.
// Cases ported from the former autodiscover.TestNormalizeEndpoint plus extra
// coverage for the invalid-URL fallback and IPv6-style hosts.
func TestNormalizeOnvifEndpoint(t *testing.T) {
	t.Helper()
	cases := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"http://1.2.3.4/onvif/device_service", "http://1.2.3.4/onvif/device_service"},
		{"http://1.2.3.4:80/onvif/device_service", "http://1.2.3.4/onvif/device_service"},
		{"https://1.2.3.4:443/onvif/device_service", "https://1.2.3.4/onvif/device_service"},
		{"http://1.2.3.4:8080/onvif/device_service", "http://1.2.3.4:8080/onvif/device_service"},
		{"HTTP://1.2.3.4/Path", "http://1.2.3.4/Path"},
		{"http://CAMERA.LOCAL/onvif/device_service", "http://camera.local/onvif/device_service"},
		{"http://1.2.3.4/onvif/device_service/", "http://1.2.3.4/onvif/device_service"},
		{"  http://1.2.3.4/onvif/device_service  ", "http://1.2.3.4/onvif/device_service"},
		// Invalid URL fallback: no scheme/host → best-effort TrimRight, no panic.
		{"not-a-url/", "not-a-url"},
		// Idempotency: normalizing an already-canonical form is a no-op.
		{"http://1.2.3.4/onvif/device_service", "http://1.2.3.4/onvif/device_service"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			t.Helper()
			got := NormalizeOnvifEndpoint(tc.input)
			if got != tc.want {
				t.Errorf("NormalizeOnvifEndpoint(%q) = %q, want %q", tc.input, got, tc.want)
			}
			// Idempotency: normalizing the output must not change it.
			if got != "" {
				if again := NormalizeOnvifEndpoint(got); again != got {
					t.Errorf("NormalizeOnvifEndpoint not idempotent: %q → %q", got, again)
				}
			}
		})
	}
}
