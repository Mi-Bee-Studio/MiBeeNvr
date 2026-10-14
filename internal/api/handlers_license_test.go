package api

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/license"
	"github.com/stretchr/testify/require"
)

func setupLicenseTestHandler(t *testing.T) (*Handler, *config.Config) {
	t.Helper()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	cfgPath := filepath.Join(t.TempDir(), "test-config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("version: \"1.0\"\n"), 0o644))
	cfg := &config.Config{
		Version: "1.0",
		Server:  config.ServerConfig{DeviceID: "dev-test-1"},
		License: config.LicenseConfig{},
	}
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, nil, cfgPath, nil, nil, nil, nil, nil, nil)
	h.SetLicenseManager(license.NewManager("dev-test-1"))
	return h, cfg
}

// mintTestLicense 用给定密钥对铸造一份绑定 dev-test-1 的授权文档。
func mintTestLicense(t *testing.T, priv ed25519.PrivateKey, notBefore, expiresAt time.Time) string {
	t.Helper()
	doc := &license.Document{
		Version:   1,
		Product:   license.Product,
		DeviceID:  "dev-test-1",
		Plan:      "p2p-remote",
		Features:  []string{"p2p"},
		Serial:    "api-test-001",
		NotBefore: notBefore,
		ExpiresAt: expiresAt,
		IssuedAt:  time.Now(),
	}
	document, err := license.Sign(priv, doc)
	require.NoError(t, err)
	return document
}

func TestLicenseEndpoints(t *testing.T) {
	h, cfg := setupLicenseTestHandler(t)
	r := h.Routes()

	// 初始：无文档，state=none，非强制放行。
	rr := doRequest(t, r, "GET", "/api/license", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var st license.Status
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &st))
	require.Equal(t, license.StateNone, st.State)
	require.True(t, st.Allowed)

	// 同一密钥对铸两份文档（有效 + 过期），公钥只装一次。
	pub, priv, err := license.GenerateKeyPair()
	require.NoError(t, err)
	pubB64 := base64.RawStdEncoding.EncodeToString(pub)
	document := mintTestLicense(t, priv, time.Now().Add(-time.Minute), time.Now().Add(12*time.Hour))

	body, _ := json.Marshal(map[string]any{"document": document, "public_key": pubB64, "required": true})
	rr = doRequest(t, r, "PUT", "/api/license", bytes.NewReader(body), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &st))
	require.Equal(t, license.StateValid, st.State)
	require.True(t, st.Allowed)
	require.NotNil(t, st.Document)
	require.Equal(t, "p2p-remote", st.Document.Plan)
	// 配置已落盘 + 共享管理器热生效。
	require.Equal(t, document, cfg.License.Document)
	require.True(t, cfg.License.Required)
	require.True(t, h.licenseMgr.Allowed())

	// 安装过期文档（同钥签发，复用已配置公钥；允许装上，状态如实 expired）。
	expiredDoc := mintTestLicense(t, priv, time.Now().Add(-24*time.Hour), time.Now().Add(-time.Second))
	// 复用已配置公钥（body 不带 public_key）。
	body, _ = json.Marshal(map[string]any{"document": expiredDoc})
	rr = doRequest(t, r, "PUT", "/api/license", bytes.NewReader(body), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &st))
	require.Equal(t, license.StateExpired, st.State)
	require.False(t, st.Allowed)
	require.False(t, h.licenseMgr.Allowed())

	// 续期：装回有效文档 → 恢复放行（换文档即续期）。
	body, _ = json.Marshal(map[string]any{"document": document})
	rr = doRequest(t, r, "PUT", "/api/license", bytes.NewReader(body), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.True(t, h.licenseMgr.Allowed())

	// 垃圾文档 → 400 拒绝（不落配置）。
	savedDoc := cfg.License.Document
	rr = doRequest(t, r, "PUT", "/api/license", bytes.NewReader([]byte(`{"document":"garbage"}`)), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Equal(t, savedDoc, cfg.License.Document)
}

func TestLicenseInstallWithoutAnyKeyRejected(t *testing.T) {
	// 从未配置公钥的实例：装文档必须随带公钥，否则 400。
	h, cfg := setupLicenseTestHandler(t)
	pub, priv, err := license.GenerateKeyPair()
	require.NoError(t, err)
	document := mintTestLicense(t, priv, time.Now().Add(-time.Minute), time.Now().Add(12*time.Hour))
	body, _ := json.Marshal(map[string]any{"document": document, "public_key": ""})
	rr := doRequest(t, h.Routes(), "PUT", "/api/license", bytes.NewReader(body), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Equal(t, "", cfg.License.Document)
	require.Equal(t, "", cfg.License.PublicKey)

	// 随带公钥后成功。
	body, _ = json.Marshal(map[string]any{"document": document, "public_key": base64.RawStdEncoding.EncodeToString(pub)})
	rr = doRequest(t, h.Routes(), "PUT", "/api/license", bytes.NewReader(body), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.True(t, h.licenseMgr.Allowed())
}

func TestLicenseNoManagerReportsNone(t *testing.T) {
	// 未接线 SetLicenseManager（能力缺席）时端点仍可用：报 none 放行。
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil, nil, nil, nil)
	rr := doRequest(t, h.Routes(), "GET", "/api/license", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var st license.Status
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &st))
	require.Equal(t, license.StateNone, st.State)
	require.True(t, st.Allowed)
}
