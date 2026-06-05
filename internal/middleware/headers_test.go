package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCOOPHeaders(t *testing.T) {
	t.Parallel()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	t.Run("no TLS", func(t *testing.T) {
		t.Parallel()
		handler := COOPHeaders(inner)
		req := httptest.NewRequest("GET", "/", nil) // r.TLS == nil
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		for _, h := range []string{"Cross-Origin-Opener-Policy", "Cross-Origin-Embedder-Policy"} {
			if v := w.Header().Get(h); v != "" {
				t.Errorf("%s should not be set without TLS, got %q", h, v)
			}
		}
	})

	t.Run("with TLS", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewTLSServer(COOPHeaders(inner))
		defer srv.Close()
		cli := srv.Client()
		resp, err := cli.Get(srv.URL)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		tests := []struct{ header, want string }{
			{"Cross-Origin-Opener-Policy", "same-origin"},
			{"Cross-Origin-Embedder-Policy", "require-corp"},
		}
		for _, tt := range tests {
			got := resp.Header.Get(tt.header)
			if got != tt.want {
				t.Errorf("%s = %q, want %q", tt.header, got, tt.want)
			}
		}
	})
}
