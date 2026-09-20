package p2p

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeOIDC 模拟 mibee-oidc token 端点：password grant 验账号密码、
// refresh grant 验轮换语义（旧 refresh 第二次用即 invalid_grant）。
type fakeOIDC struct {
	t           *testing.T
	passwords   map[string]string // username → password
	validAccess map[string]bool   // 未消费的 access token（jti 单次由服务端语义模拟）
	refresh     string            // 当前有效 refresh token
	nextRefresh string
	passwordGot int
	refreshGot  int
}

func (f *fakeOIDC) handler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(400)
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "password":
		f.passwordGot++
		u, p := r.PostForm.Get("username"), r.PostForm.Get("password")
		if f.passwords[u] != p {
			writeJSON(w, 401, map[string]any{"error": "invalid_grant"})
			return
		}
		f.nextRefresh = "refresh-" + time.Now().Format("150405.000000000")
		writeJSON(w, 200, map[string]any{
			"access_token": "acc-pw", "refresh_token": f.nextRefresh, "expires_in": 3600,
		})
	case "refresh_token":
		f.refreshGot++
		if f.refresh == "" || r.PostForm.Get("refresh_token") != f.refresh {
			writeJSON(w, 400, map[string]any{"error": "invalid_grant"})
			return
		}
		// 轮换：旧 refresh 立即作废。
		f.refresh = f.nextRefresh
		f.nextRefresh = "refresh-" + time.Now().Format("150405.000000000")
		writeJSON(w, 200, map[string]any{
			"access_token": "acc-rf", "refresh_token": f.nextRefresh, "expires_in": 3600,
		})
	default:
		writeJSON(w, 400, map[string]any{"error": "unsupported_grant_type"})
	}
}

func writeJSON(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func newTestManager(t *testing.T, srv *httptest.Server, statePath string) *TokenManager {
	t.Helper()
	tm := NewTokenManager(srv.URL, "p2p-signaling", "dev", "secret", statePath)
	tm.minInterval = 0 // 测试不做铸造间隔
	return tm
}

func TestTokenManagerPasswordGrantPersistsRefresh(t *testing.T) {
	oidc := &fakeOIDC{t: t, passwords: map[string]string{"dev": "secret"}}
	srv := httptest.NewServer(http.HandlerFunc(oidc.handler))
	defer srv.Close()

	state := filepath.Join(t.TempDir(), "state.json")
	tm := newTestManager(t, srv, state)

	tok, err := tm.Mint(context.Background())
	if err != nil || tok != "acc-pw" {
		t.Fatalf("mint = %q, %v", tok, err)
	}
	b, err := os.ReadFile(state)
	if err != nil {
		t.Fatalf("state file: %v", err)
	}
	var st tokenState
	if json.Unmarshal(b, &st) != nil || st.RefreshToken == "" {
		t.Fatalf("state file missing refresh: %s", b)
	}
}

func TestTokenManagerRefreshRotation(t *testing.T) {
	oidc := &fakeOIDC{t: t, passwords: map[string]string{"dev": "secret"}}
	srv := httptest.NewServer(http.HandlerFunc(oidc.handler))
	defer srv.Close()

	state := filepath.Join(t.TempDir(), "state.json")
	tm := newTestManager(t, srv, state)

	if _, err := tm.Mint(context.Background()); err != nil { // password → refresh#1
		t.Fatal(err)
	}
	oidc.refresh = oidc.nextRefresh // 服务端激活 refresh#1

	tok, err := tm.Mint(context.Background()) // refresh 轮换路径
	if err != nil || tok != "acc-rf" {
		t.Fatalf("second mint = %q, %v (want refresh-grant path)", tok, err)
	}
	if oidc.refreshGot != 1 || oidc.passwordGot != 1 {
		t.Fatalf("grants: password=%d refresh=%d (want 1/1)", oidc.passwordGot, oidc.refreshGot)
	}
}

func TestTokenManagerRefreshInvalidFallsBackToPassword(t *testing.T) {
	oidc := &fakeOIDC{t: t, passwords: map[string]string{"dev": "secret"}}
	srv := httptest.NewServer(http.HandlerFunc(oidc.handler))
	defer srv.Close()

	state := filepath.Join(t.TempDir(), "state.json")
	tm := newTestManager(t, srv, state)

	if _, err := tm.Mint(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 服务端不激活 refresh（模拟 7 天过期）→ 下一次 Mint 应回退 password。

	tok, err := tm.Mint(context.Background())
	if err != nil || tok != "acc-pw" {
		t.Fatalf("mint = %q, %v (want password fallback)", tok, err)
	}
	if oidc.passwordGot != 2 {
		t.Fatalf("password grants = %d, want 2", oidc.passwordGot)
	}
}

func TestTokenManagerWrongPasswordNoRetryLoop(t *testing.T) {
	oidc := &fakeOIDC{t: t, passwords: map[string]string{"dev": "right"}}
	srv := httptest.NewServer(http.HandlerFunc(oidc.handler))
	defer srv.Close()

	tm := newTestManager(t, srv, filepath.Join(t.TempDir(), "state.json"))
	tm.minInterval = defaultMinMintInterval // 生产间隔

	if _, err := tm.Mint(context.Background()); err == nil {
		t.Fatal("want error for wrong password")
	}
	// 立即再 Mint：防锤直接拒绝，**不**再打端点（锁定红线）。
	if _, err := tm.Mint(context.Background()); err != ErrMintBackoff {
		t.Fatalf("second mint err = %v, want ErrMintBackoff", err)
	}
	if oidc.passwordGot != 1 {
		t.Fatalf("endpoint hits = %d, want exactly 1", oidc.passwordGot)
	}
}

func TestDeriveTokenEndpoint(t *testing.T) {
	cases := map[string]string{
		"wss://ali-gz.example.com/signal":     "https://ali-gz.example.com/oidc/token",
		"ws://192.168.63.10:3000/signal":      "http://192.168.63.10:3000/oidc/token",
		"wss://host:8443/signal":              "https://host/oidc/token",
		"not-a-url":                           "",
	}
	for in, want := range cases {
		if got := DeriveTokenEndpoint(in); got != want {
			t.Errorf("DeriveTokenEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStatePathFor(t *testing.T) {
	if got := StatePathFor("/etc/mibee/nvr.yaml"); got != "/etc/mibee/nvr.p2p-token.json" {
		t.Errorf("state path = %q", got)
	}
	if got := StatePathFor(""); got != "" {
		t.Errorf("empty config path → %q, want empty", got)
	}
}

func TestIsAuthError(t *testing.T) {
	for _, code := range []string{"invalid_token", "unauthorized", "jti_replayed"} {
		if !isAuthError(code) {
			t.Errorf("%q should be auth-class", code)
		}
	}
	for _, code := range []string{"rate_limited", "internal", ""} {
		if isAuthError(code) {
			t.Errorf("%q should not be auth-class", code)
		}
	}
}
