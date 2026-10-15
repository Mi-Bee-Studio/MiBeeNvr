package p2p

// enroll_test.go — 设备引导绑定客户端回归：平台三端点模拟（票据校验/
// 挑战签名）、密钥落盘与复用、bound/retired 换新 ID 重试、设备身份模式
// token 链（refresh 断=需重绑）。

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakePlatform 模拟 /device/enroll + /device/re-enroll/init + /device/re-enroll。
type fakePlatform struct {
	srv        *httptest.Server
	validTick  atomic.Value // string "id:secret"
	boundSeen  atomic.Int64
	retireOnce atomic.Bool // 首个 device_id 返回 retired（模拟重绑）
	pubkeys    *syncMap    // device_id -> pubkey (b64)
	// existing 按 pubkey(b64) 模拟「同账号+同设备公钥幂等 upsert」
	// （平台幂等 enroll 支持）：命中则 200 返回既有 device_id，
	// 公钥登记在既有身份名下——请求方送来的随机 device_id 被忽略。
	existing *syncMap // pubkey(b64) -> existing device_id
}

type syncMap struct {
	m map[string]string
	// uses package-level mutex via channel-free simple lock
	ch chan struct{}
}

func (s *syncMap) set(k, v string) { <-s.ch; s.m[k] = v; s.ch <- struct{}{} }
func (s *syncMap) get(k string) (string, bool) {
	<-s.ch
	v, ok := s.m[k]
	s.ch <- struct{}{}
	return v, ok
}

func newFakePlatform(t *testing.T) *fakePlatform {
	t.Helper()
	sm := &syncMap{m: map[string]string{}, ch: make(chan struct{}, 1)}
	sm.ch <- struct{}{}
	fp := &fakePlatform{pubkeys: sm}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /device/enroll", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			TicketID  string `json:"ticket_id"`
			Secret    string `json:"secret"`
			ProductID string `json:"product_id"`
			DeviceID  string `json:"device_id"`
			Pubkey    string `json:"pubkey"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if fp.validTick.Load() == nil || fp.validTick.Load().(string) != req.TicketID+":"+req.Secret {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid or expired ticket"})
			return
		}
		if fp.existing != nil {
			if id, hit := fp.existing.get(req.Pubkey); hit {
				// 幂等 upsert（#47）：同账号+同公钥 → 200 + 既有身份。
				fp.pubkeys.set(id, req.Pubkey)
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"device_id": id, "product_id": "mibeenvr", "account_id": "alice",
					"idempotent": true,
				})
				return
			}
		}
		if fp.retireOnce.CompareAndSwap(false, true) {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "device_retired: rebind requires a fresh device identity"})
			return
		}
		fp.boundSeen.Add(1)
		fp.pubkeys.set(req.DeviceID, req.Pubkey)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"device_id": req.DeviceID, "product_id": "mibeenvr", "account_id": "alice",
		})
	})
	mux.HandleFunc("POST /device/re-enroll/init", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"challenge": "deadbeef01"})
	})
	mux.HandleFunc("POST /device/re-enroll", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ProductID string `json:"product_id"`
			DeviceID  string `json:"device_id"`
			Challenge string `json:"challenge"`
			Signature string `json:"signature"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		pubB64, _ := fp.pubkeys.get(req.DeviceID)
		pub, err := base64.StdEncoding.DecodeString(pubB64)
		if err != nil || len(pub) != 32 {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "unknown device"})
			return
		}
		// 签名口径与真实平台一致：mibee-re-enroll:v1:<product>:<device>:<challenge>
		message := "mibee-re-enroll:v1:" + req.ProductID + ":" + req.DeviceID + ":" + req.Challenge
		sig, _ := base64.StdEncoding.DecodeString(req.Signature)
		if !ed25519.Verify(ed25519.PublicKey(pub), []byte(message), sig) {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad signature"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "acc", "refresh_token": "ref-" + req.DeviceID, "expires_in": 3600,
		})
	})
	fp.srv = httptest.NewServer(mux)
	t.Cleanup(fp.srv.Close)
	return fp
}

func TestEnroll_HappyAndFreshIdentityRetry(t *testing.T) {
	fp := newFakePlatform(t)
	fp.validTick.Store("t-1:s-1")
	key := filepath.Join(t.TempDir(), "device.key")

	res, err := Enroll(context.Background(), EnrollOptions{
		BaseURL: fp.srv.URL, TicketID: "t-1", Secret: "s-1",
		ProductID: "mibeenvr", KeyPath: key,
	})
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	// 首个 device_id 被 fake 平台打回 device_retired → 客户端换新 ID 重试成功。
	if res.Username == "" || !strings.HasPrefix(res.Username, "device:mibeenvr:nvr-") {
		t.Fatalf("unexpected username %q", res.Username)
	}
	if res.Refresh == "" || res.Access == "" {
		t.Fatalf("missing tokens: %+v", res)
	}
	if got, _ := fp.pubkeys.get(res.DeviceID); got == "" {
		t.Fatal("pubkey not registered")
	}

	// 密钥复用：第二次引导用同一把私钥（文件已存在，不重新生成）。
	before, err := os.ReadFile(key)
	if err != nil {
		t.Fatal(err)
	}
	fp.validTick.Store("t-2:s-2")
	res2, err := Enroll(context.Background(), EnrollOptions{
		BaseURL: fp.srv.URL, TicketID: "t-2", Secret: "s-2",
		ProductID: "mibeenvr", KeyPath: key,
	})
	if err != nil {
		t.Fatalf("second enroll: %v", err)
	}
	after, _ := os.ReadFile(key)
	if string(before) != string(after) {
		t.Fatal("device key must be reused, not regenerated")
	}
	_ = res2
}

func TestEnroll_BadTicket(t *testing.T) {
	fp := newFakePlatform(t)
	fp.validTick.Store("right:right")
	_, err := Enroll(context.Background(), EnrollOptions{
		BaseURL: fp.srv.URL, TicketID: "t", Secret: "s",
		ProductID: "mibeenvr", KeyPath: filepath.Join(t.TempDir(), "k.key"),
	})
	if err == nil || !strings.Contains(err.Error(), ErrBadTicket.Error()) {
		t.Fatalf("want bad-ticket error, got %v", err)
	}
}

// 平台幂等 upsert：同账号+同设备
// 公钥重复 enroll 返回 200 + 既有 device_id。客户端必须以响应身份走完
// re-enroll 挑战并落配置——继续用本地生成的随机 ID 会让第④步打到不存
// 在的身份上（unknown device）。
func TestEnroll_IdempotentRebindAdoptsServerDeviceID(t *testing.T) {
	fp := newFakePlatform(t)
	fp.validTick.Store("t:s")

	key := filepath.Join(t.TempDir(), "device.key")
	priv, err := LoadOrCreateDeviceKey(key)
	if err != nil {
		t.Fatal(err)
	}
	existingID := "nvr-test-0001"
	existing := &syncMap{m: map[string]string{}, ch: make(chan struct{}, 1)}
	existing.ch <- struct{}{}
	existing.set(base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)), existingID)
	fp.existing = existing

	res, err := Enroll(context.Background(), EnrollOptions{
		BaseURL: fp.srv.URL, TicketID: "t", Secret: "s",
		ProductID: "mibeenvr", KeyPath: key,
	})
	if err != nil {
		t.Fatalf("idempotent rebind must succeed, got: %v", err)
	}
	if res.DeviceID != existingID {
		t.Fatalf("device_id must adopt server identity %q, got %q", existingID, res.DeviceID)
	}
	if res.Refresh != "ref-"+existingID {
		t.Fatalf("tokens must be minted for the adopted identity, refresh=%q", res.Refresh)
	}
	if !strings.HasPrefix(res.Username, "device:mibeenvr:"+existingID) {
		t.Fatalf("username must reflect adopted identity, got %q", res.Username)
	}
}

func TestEnrollPreferredIDNoRetry(t *testing.T) {
	fp := newFakePlatform(t)
	fp.validTick.Store("t:s")
	// 指定首选 ID 时遇 retired 不重试（调用方决定怎么办）。
	_, err := Enroll(context.Background(), EnrollOptions{
		BaseURL: fp.srv.URL, TicketID: "t", Secret: "s",
		ProductID: "mibeenvr", KeyPath: filepath.Join(t.TempDir(), "k.key"),
		PreferredDeviceID: "nvr-fixed-1",
	})
	if err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("want retired error surfaced, got %v", err)
	}
}

func TestTokenManagerDeviceModeNeedsRebind(t *testing.T) {
	var grants atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		grants.Add(1)
		// 一切 grant 失败（refresh 链已死的服务端视角）。
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
	}))
	defer srv.Close()

	tm := NewTokenManager(srv.URL, "p2p-signaling", "", "", filepath.Join(t.TempDir(), "state.json"))
	tm.minInterval = 0
	tm.AdoptRefresh("stale-refresh")
	if _, err := tm.Mint(context.Background()); err == nil || err.Error() != ErrRebindNeeded.Error() {
		t.Fatalf("want ErrRebindNeeded, got %v", err)
	}

	// 服务账号模式（有凭据）仍走 password 回退路径（此处同样失败但错误
	// 不同——不是 ErrRebindNeeded）。
	tm2 := NewTokenManager(srv.URL, "p2p-signaling", "nvr-svc", "pw", filepath.Join(t.TempDir(), "s.json"))
	tm2.minInterval = 0
	tm2.AdoptRefresh("stale-refresh")
	_, err := tm2.Mint(context.Background())
	if err == nil || err.Error() == ErrRebindNeeded.Error() {
		t.Fatalf("service-account mode must not report rebind-needed, got %v", err)
	}
}

func TestEnrollHTTPBase(t *testing.T) {
	cases := map[string]string{
		"wss://signal.example.invalid/signal": "https://signal.example.invalid", // RFC 2606 reserved — never real endpoints in vectors
		"ws://127.0.0.1:9444/signal":          "http://127.0.0.1:9444",
		// 显式端口保留（#11）：wss 带端口不再静默丢。
		"wss://h:8443/signal": "https://h:8443",
		"garbage":             "",
	}
	for in, want := range cases {
		if got := EnrollHTTPBase(in); got != want {
			t.Errorf("EnrollHTTPBase(%q) = %q, want %q", in, got, want)
		}
	}
	first, second := GenerateDeviceID(), GenerateDeviceID()
	if first == second {
		t.Fatal("device id collision")
	}
	_ = time.Second
}

func TestLoadOrCreateDeviceKeyDurableWrite(t *testing.T) {
	key := filepath.Join(t.TempDir(), "sub", "device.key")
	priv, err := LoadOrCreateDeviceKey(key)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// 临时文件不残留；终件即 seed；二次加载返回同一身份
	if _, err := os.Stat(key + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("tmp leftover: %v", err)
	}
	b, err := os.ReadFile(key)
	if err != nil || len(b) != ed25519.SeedSize || !bytes.Equal(b, priv.Seed()) {
		t.Fatalf("key file not the seed: err=%v len=%d", err, len(b))
	}
	priv2, err := LoadOrCreateDeviceKey(key)
	if err != nil || !bytes.Equal(priv2, priv) {
		t.Fatalf("reload mismatch: %v", err)
	}
	// 半写 key（崩溃现场形态）→ 显式报错而非静默再生身份
	if err := os.WriteFile(key, make([]byte, 10), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateDeviceKey(key); err == nil || !strings.Contains(err.Error(), "unexpected size") {
		t.Fatalf("want unexpected size error, got %v", err)
	}
}
