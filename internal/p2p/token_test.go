package p2p

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeOIDC 模拟 mibee-oidc token 端点：password grant 验账号密码、
// refresh grant 验轮换语义（旧 refresh 第二次用即 invalid_grant）。
type fakeOIDC struct {
	t           *testing.T
	passwords   map[string]string // username → password
	refresh     string            // 当前有效 refresh token
	nextRefresh string
	passwordGot int
	refreshGot  int
}

func (f *fakeOIDC) handler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusBadRequest)
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
	if _, err := tm.Mint(context.Background()); !errors.Is(err, ErrMintBackoff) {
		t.Fatalf("second mint err = %v, want ErrMintBackoff", err)
	}
	if oidc.passwordGot != 1 {
		t.Fatalf("endpoint hits = %d, want exactly 1", oidc.passwordGot)
	}
}

func TestDeriveTokenEndpoint(t *testing.T) {
	cases := map[string]string{
		"wss://signal2.example.invalid/signal": "https://signal2.example.invalid/oidc/token",
		"ws://signal.example.invalid/signal":   "http://signal.example.invalid/oidc/token",
		// 显式端口必须保留（#11：丢端口会静默打到 443，非 443 部署
		// token/enroll/grants 全部指向错误端点）。
		"wss://host:8443/signal": "https://host:8443/oidc/token",
		"wss://h:18443/signal":   "https://h:18443/oidc/token",
		"ws://h:18080/signal":    "http://h:18080/oidc/token",
		"not-a-url":              "",
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

// 设备身份模式防锤间隔（2026-10-06 E2E 回归钉）：enrollment 引导的
// device: 凭据只走 refresh grant（无 password 锁定面），间隔降到秒级，
// 避免撞上信令重拨的铸造导致 viewer 首连 429。
func TestTokenManager_DeviceIdentityShortInterval(t *testing.T) {
	tm := NewTokenManager("http://oidc.test/token", "c", "device:mibeenvr:nvr-x", "", t.TempDir()+"/state.json")
	if tm.minInterval != deviceIdentityMinMintInterval {
		t.Fatalf("device identity interval = %v, want %v", tm.minInterval, deviceIdentityMinMintInterval)
	}
	svc := NewTokenManager("http://oidc.test/token", "c", "nvr-pilot-device", "pw", t.TempDir()+"/state.json")
	if svc.minInterval != defaultMinMintInterval {
		t.Fatalf("service account interval = %v, want %v", svc.minInterval, defaultMinMintInterval)
	}
}

// TestTokenManagerDrainWaitsForInFlightMint: Drain 必须等在途 Mint 完成
// （轮换已落盘）再返回——关停排水是「refresh 单次消费+未持久化=身份
// 死亡」竞态的修复（2026-10-08 两次生产事故）。
func TestTokenManagerDrainWaitsForInFlightMint(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.json")

	release := make(chan struct{})
	arrived := make(chan struct{})
	var once sync.Once
	oidc := &fakeOIDC{passwords: map[string]string{"dev": "secret"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(arrived) }) // 请求已到（Mint 已持锁进 HTTP）
		<-release                          // 拖住第一枚 grant，模拟关停恰逢在途铸造
		oidc.handler(w, r)
	}))
	defer srv.Close()

	tm := newTestManager(t, srv, state)
	minted := make(chan error, 1)
	go func() {
		_, err := tm.Mint(context.Background())
		minted <- err
	}()

	select {
	case <-arrived:
	case <-time.After(2 * time.Second):
		t.Fatal("mint request never arrived")
	}

	// Mint 进行中（HTTP 被拖住）：Drain 不得提前返回。
	drained := make(chan struct{})
	go func() { tm.Drain(); close(drained) }()
	select {
	case <-drained:
		t.Fatal("Drain returned while mint in flight")
	case <-time.After(150 * time.Millisecond):
		// 仍在等待——正确。
	}

	close(release) // 放行 grant
	require.NoError(t, <-minted)

	select {
	case <-drained:
	case <-time.After(2 * time.Second):
		t.Fatal("Drain did not return after mint completed")
	}

	// 轮换已落盘：新 manager（同 state）应能继续 refresh 链。
	raw, err := os.ReadFile(state)
	require.NoError(t, err)
	var st struct {
		RefreshToken string `json:"refresh_token"`
	}
	require.NoError(t, json.Unmarshal(raw, &st))
	require.NotEmpty(t, st.RefreshToken)
	oidc.refresh = st.RefreshToken // 武装假服务端（首签不自动入账是假件语义）
	tm2 := newTestManager(t, srv, state)
	tm2.username, tm2.password = "", "" // 断掉回退，链断即失败
	if _, err := tm2.Mint(context.Background()); err != nil {
		t.Fatalf("rotated refresh not persisted across Drain: %v", err)
	}
}

// captureErrLog 抓 slog Error 记录（loadState 告警断言用，#6）。
type captureErrLog struct {
	mu   sync.Mutex
	msgs []string
}

func (c *captureErrLog) Enabled(context.Context, slog.Level) bool { return true }

func (c *captureErrLog) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r.Level >= slog.LevelError {
		c.msgs = append(c.msgs, r.Message)
	}
	return nil
}

func (c *captureErrLog) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *captureErrLog) WithGroup(string) slog.Handler      { return c }

func TestLoadStateCorruptStateLogsOncePerEpisode(t *testing.T) {
	lg := &captureErrLog{}
	old := slog.Default()
	slog.SetDefault(slog.New(lg))
	t.Cleanup(func() { slog.SetDefault(old) })

	path := filepath.Join(t.TempDir(), "state.json")
	require.NoError(t, os.WriteFile(path, []byte("{half-written"), 0o600))

	tm := NewTokenManager("http://x", "cid", "u", "p", path)
	tm.loadState()
	require.Empty(t, tm.refresh, "损坏 state 不得被采信")
	require.Len(t, lg.msgs, 1, "损坏即告警一次")

	tm.loadState() // 同一损坏周期不刷屏（每次 Mint 都会重读）
	require.Len(t, lg.msgs, 1)

	// 手工重建 state（生产 09:59 事故的自愈路径）——重读必须捞起
	require.NoError(t, os.WriteFile(path, []byte(`{"refresh_token":"r1"}`), 0o600))
	tm.loadState()
	require.Equal(t, "r1", tm.refresh)

	// 再次损坏 = 新周期，再告警一次
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
	tm.loadState()
	require.Len(t, lg.msgs, 2)
}

func TestLoadStateEmptyRefreshLogs(t *testing.T) {
	lg := &captureErrLog{}
	old := slog.Default()
	slog.SetDefault(slog.New(lg))
	t.Cleanup(func() { slog.SetDefault(old) })

	path := filepath.Join(t.TempDir(), "state.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"refresh_token":""}`), 0o600))

	tm := NewTokenManager("http://x", "cid", "u", "p", path)
	tm.loadState()
	require.Empty(t, tm.refresh)
	require.Len(t, lg.msgs, 1, "空 refresh 视同损坏：显式告警")
}
