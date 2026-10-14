package p2p

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// TestManagerLadderTokenFuncMintsWithAntiHammer 钉住梯子铸币闭包的行为：
// 走 TokenManager 真实链路（refresh/password grant），且防锤间隔原样
// 传导到梯子侧（第二次立即调用拿到 ErrMintBackoff 而非再发一次 HTTP）。
func TestManagerLadderTokenFuncMintsWithAntiHammer(t *testing.T) {
	oidc := &fakeOIDC{t: t, passwords: map[string]string{"dev": "secret"}}
	srv := httptest.NewServer(http.HandlerFunc(oidc.handler))
	defer srv.Close()

	cfg := config.P2PConfig{
		ServerURL:     "wss://ladder-test.invalid/signal",
		TokenEndpoint: srv.URL,
		Username:      "dev",
		Password:      "secret",
	}
	var buf bytes.Buffer
	m := New(cfg, "127.0.0.1:9090", filepath.Join(t.TempDir(), "token.json"),
		slog.New(slog.NewTextHandler(&buf, nil)), nil)

	tf := m.ladderTokenFunc()
	if tf == nil {
		t.Fatal("ladder token func must be wired")
	}
	tok, err := tf()
	if err != nil || tok != "acc-pw" {
		t.Fatalf("first mint = %q, %v (want acc-pw)", tok, err)
	}
	if _, err := tf(); err == nil {
		t.Fatal("second immediate mint must hit the anti-hammer interval")
	} else if err.Error() != ErrMintBackoff.Error() {
		t.Fatalf("second mint error = %v, want ErrMintBackoff", err)
	}
}
