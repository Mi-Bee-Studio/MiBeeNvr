package tray

import "testing"

func TestListenURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{":9090", "http://127.0.0.1:9090"},
		{"  :9090 ", "http://127.0.0.1:9090"},
		{"0.0.0.0:9090", "http://127.0.0.1:9090"},
		{"[::]:9090", "http://127.0.0.1:9090"},
		{"127.0.0.1:9090", "http://127.0.0.1:9090"},
		{"192.168.63.30:9090", "http://192.168.63.30:9090"},
		{"[fd00::1]:9090", "http://[fd00::1]:9090"},
		{"not-an-addr", "http://127.0.0.1:9090"},
	}
	for _, tc := range cases {
		if got := ListenURL(tc.in); got != tc.want {
			t.Errorf("ListenURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeListenInput(t *testing.T) {
	cases := []struct{ input, current, want string }{
		// Bare port keeps the current host.
		{"9091", "127.0.0.1:9090", "127.0.0.1:9091"},
		{"  9091  ", "0.0.0.0:9090", "0.0.0.0:9091"},
		{"9091", ":9090", ":9091"},           // wildcard stays wildcard
		{"9091", "[::1]:9090", "[::1]:9091"}, // IPv6 host bracketed back
		{"9091", "garbage", ":9091"},         // unparseable current → wildcard
		// Full addresses pass through untouched.
		{"127.0.0.1:9091", "127.0.0.1:9090", "127.0.0.1:9091"},
		{"0.0.0.0:80", "127.0.0.1:9090", "0.0.0.0:80"},
		{"[fd00::1]:9091", "127.0.0.1:9090", "[fd00::1]:9091"},
		// Non-port single words pass through for validation to reject.
		{"localhost", "127.0.0.1:9090", "localhost"},
		{"", "127.0.0.1:9090", ""},
	}
	for _, tc := range cases {
		if got := NormalizeListenInput(tc.input, tc.current); got != tc.want {
			t.Errorf("NormalizeListenInput(%q, %q) = %q, want %q", tc.input, tc.current, got, tc.want)
		}
	}
}

func TestValidateListenAddr(t *testing.T) {
	ok := []string{
		"127.0.0.1:9090",
		"  127.0.0.1:9090  ", // trimmed
		"0.0.0.0:9090",
		":9090",
		"[::1]:9090",
		"192.168.63.30:1",
		"127.0.0.1:65535",
	}
	for _, in := range ok {
		if err := ValidateListenAddr(in); err != nil {
			t.Errorf("ValidateListenAddr(%q) = %v, want nil", in, err)
		}
	}
	bad := map[string]string{
		"":             "empty",
		"   ":          "whitespace",
		"9090":         "no colon",
		"127.0.0.1":    "no port",
		":":            "empty port",
		":abc":         "non-numeric port",
		":0":           "port zero",
		":65536":       "port overflow",
		"127.0.0.1:-1": "negative port",
		"a:b:c:d:9090": "garbage",
	}
	for in := range bad {
		if err := ValidateListenAddr(in); err == nil {
			t.Errorf("ValidateListenAddr(%q) = nil, want error", in)
		}
	}
}
