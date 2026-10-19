package p2p

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeReenroll 模拟平台的 /device/re-enroll 两步端点（挑战单次使用 +
// Ed25519 签名校验），返回设备 token 对。
type fakeReenroll struct {
	t         *testing.T
	pub       ed25519.PublicKey
	challenge atomic.Value // string
	initGot   atomic.Int32
	finalGot  atomic.Int32
	issued    atomic.Int32
}

func (f *fakeReenroll) mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /device/re-enroll/init", func(w http.ResponseWriter, r *http.Request) {
		f.initGot.Add(1)
		var req struct {
			ProductID string `json:"product_id"`
			DeviceID  string `json:"device_id"`
		}
		require.NoError(f.t, json.NewDecoder(r.Body).Decode(&req))
		require.Equal(f.t, "mibeenvr", req.ProductID)
		require.Equal(f.t, "nvr-test-01", req.DeviceID)
		ch := hex.EncodeToString([]byte("challenge-bytes-32!!!!!!!!!!!!!!!!!"))
		f.challenge.Store(ch)
		writeJSON(w, 200, map[string]any{"challenge": ch, "expires_in": 60})
	})
	mux.HandleFunc("POST /device/re-enroll", func(w http.ResponseWriter, r *http.Request) {
		f.finalGot.Add(1)
		var req struct {
			ProductID string `json:"product_id"`
			DeviceID  string `json:"device_id"`
			Challenge string `json:"challenge"`
			Signature string `json:"signature"`
		}
		require.NoError(f.t, json.NewDecoder(r.Body).Decode(&req))
		require.Equal(f.t, f.challenge.Load(), req.Challenge, "挑战应原样回传")
		msg := "mibee-re-enroll:v1:" + req.ProductID + ":" + req.DeviceID + ":" + req.Challenge
		sig, err := base64.StdEncoding.DecodeString(req.Signature)
		require.NoError(f.t, err)
		if !ed25519.Verify(f.pub, []byte(msg), sig) {
			writeJSON(w, 401, map[string]any{"error": "invalid_signature"})
			return
		}
		f.issued.Add(1)
		writeJSON(w, 200, map[string]any{
			"access_token": "acc-reenroll", "refresh_token": "refresh-reenroll",
			"token_type": "Bearer", "expires_in": 3600,
		})
	})
}

// RED→GREEN #27：设备身份 refresh 链死亡后，Mint 应走 re-enroll 自愈
// 而不是返回 ErrRebindNeeded。
func TestTokenManager_Reenroll_SelfHealsDeadRefreshChain(t *testing.T) {
	oidc := httptest.NewServer(newFakeRefreshRejector(t))
	defer oidc.Close()
	mux := http.NewServeMux()
	fr := &fakeReenroll{t: t}
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	fr.pub = pub
	fr.mount(mux)
	platform := httptest.NewServer(mux)
	defer platform.Close()

	tm := NewTokenManager(oidc.URL+"/oidc/token", "nvr",
		"device:mibeenvr:nvr-test-01", "", t.TempDir()+"/state.json")
	keyPath := t.TempDir() + "/device.key"
	require.NoError(t, osWriteFile0600(keyPath, priv.Seed()))
	tm.SetReenroll(platform.URL, "mibeenvr", "nvr-test-01", keyPath, nil)
	tm.minInterval = 0

	acc, err := tm.Mint(context.Background())
	require.NoError(t, err, "refresh 死链应经 re-enroll 自愈")
	require.Equal(t, "acc-reenroll", acc)
	require.EqualValues(t, 1, fr.initGot.Load())
	require.EqualValues(t, 1, fr.finalGot.Load())
	require.EqualValues(t, 1, fr.issued.Load())

	// 自愈出的 refresh 已持久化：第二次 Mint 走 refresh grant，不再
	// 打 re-enroll（挑战单次使用，重复打反而会失败）。
	acc2, err := tm.Mint(context.Background())
	require.NoError(t, err)
	require.NotEqual(t, "acc-reenroll", acc2, "第二次应走新 refresh 链")
	require.EqualValues(t, 1, fr.finalGot.Load(), "re-enroll 只应发生一次")
}

// RED→GREEN：无设备密钥时保持既有语义（ErrRebindNeeded）。
func TestTokenManager_Reenroll_NoKeyStillErrRebind(t *testing.T) {
	oidc := httptest.NewServer(newFakeRefreshRejector(t))
	defer oidc.Close()

	tm := NewTokenManager(oidc.URL+"/oidc/token", "nvr",
		"device:mibeenvr:nvr-test-01", "", t.TempDir()+"/state.json")
	_, err := tm.Mint(context.Background())
	require.ErrorIs(t, err, ErrRebindNeeded)
}

// RED→GREEN：re-enroll 端点故障时错误透传（不吞成 ErrRebindNeeded），
// 下个 Mint 周期再试。
func TestTokenManager_Reenroll_PlatformDownSurfacesError(t *testing.T) {
	oidc := httptest.NewServer(newFakeRefreshRejector(t))
	defer oidc.Close()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 503, map[string]any{"error": "unavailable"})
	}))
	defer dead.Close()

	tm := NewTokenManager(oidc.URL+"/oidc/token", "nvr",
		"device:mibeenvr:nvr-test-01", "", t.TempDir()+"/state.json")
	keyPath := t.TempDir() + "/device.key"
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	require.NoError(t, osWriteFile0600(keyPath, priv.Seed()))
	tm.SetReenroll(dead.URL, "mibeenvr", "nvr-test-01", keyPath, nil)
	tm.minInterval = 0

	_, err = tm.Mint(context.Background())
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrRebindNeeded, "平台故障应透传而非误判链死")
	require.Contains(t, err.Error(), "re-enroll")
}

// newFakeRefreshRejector：OIDC 端点把既有 refresh 一律判 invalid_grant
//（模拟轮换链断裂），但 re-enroll 换发的新 refresh（refresh-reenroll）
// 有效——可断言「自愈后链条续上了」。
func newFakeRefreshRejector(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch r.PostForm.Get("grant_type") {
		case "refresh_token":
			if r.PostForm.Get("refresh_token") == "refresh-reenroll" {
				writeJSON(w, 200, map[string]any{
					"access_token": "acc-refresh2", "refresh_token": "refresh-rotated",
					"expires_in": 3600,
				})
				return
			}
			writeJSON(w, 400, map[string]any{"error": "invalid_grant"})
		default:
			writeJSON(w, 400, map[string]any{"error": "unsupported_grant_type"})
		}
	})
}

func osWriteFile0600(path string, b []byte) error {
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
