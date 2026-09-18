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
