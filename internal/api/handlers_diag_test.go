package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 预投产调试上报端点（2026-10-07）：App DiagLog → NVR 落盘链路。

const _diagReportBody = `{
  "boot_id": "ab12cd34",
  "app_version": "0.1.3-debug+6108",
  "package_kind": "debug",
  "platform": "android",
  "meta": {"site": "家"},
  "events": [
    {"t": "2026-10-07T13:00:00.000", "cat": "net", "lvl": "i", "msg": "网络 cellular"},
    {"t": "2026-10-07T13:00:43.000", "cat": "p2p", "lvl": "w", "msg": "链路死亡", "attrs": {"reason": "ice_disconnected_15s"}}
  ]
}`

func diagEnv(t *testing.T) (*Handler, http.Handler) {
	t.Helper()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil, nil, nil, nil)
	return h, h.Routes()
}

func postDiagReport(t *testing.T, routes http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, "/api/diagnostics/app", bytes.NewReader([]byte(body)))
	rr := httptest.NewRecorder()
	routes.ServeHTTP(rr, req)
	return rr
}

func TestAppDiagnosticsReportStored(t *testing.T) {
	h, routes := diagEnv(t)
	rr := postDiagReport(t, routes, _diagReportBody)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	dir := filepath.Join(h.store.RootDir(), "diag", "app")
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected 1 stored file, err=%v n=%d", err, len(entries))
	}
	// 文件名口径（2026-10-08）：<bootId>_<version>_<ts>.json——bootId 仍
	// 是首段（prune 分组语义不变），版本紧跟其后一眼可辨。
	if !strings.HasPrefix(entries[0].Name(), "ab12cd34_") || !strings.HasSuffix(entries[0].Name(), ".json") {
		t.Fatalf("unexpected file name %q", entries[0].Name())
	}
	if !strings.Contains(entries[0].Name(), "0.1.3-debug+6108") {
		t.Fatalf("file name should embed app version, got %q", entries[0].Name())
	}
	raw, _ := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("stored file not valid json: %v", err)
	}
	if back["boot_id"] != "ab12cd34" {
		t.Fatalf("boot_id round-trip failed: %v", back["boot_id"])
	}
	if evs, ok := back["events"].([]any); !ok || len(evs) != 2 {
		t.Fatalf("events round-trip failed: %v", back["events"])
	}
}

func TestAppDiagnosticsRejectsGarbage(t *testing.T) {
	_, routes := diagEnv(t)
	if rr := postDiagReport(t, routes, "not json"); rr.Code != http.StatusBadRequest {
		t.Fatalf("garbage body should 400, got %d", rr.Code)
	}
	if rr := postDiagReport(t, routes, `{"boot_id":"x","events":[]}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("empty events should 400, got %d", rr.Code)
	}
}

func TestAppDiagnosticsPrune(t *testing.T) {
	h, routes := diagEnv(t)
	dir := filepath.Join(h.store.RootDir(), "diag", "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 预置超量旧文件（同 boot 前缀 60 份）——上报一次应裁到 51（60-50+新1）。
	for i := range 60 {
		n := "oldboot_20260101-1234" + string(rune('0'+i/10)) + string(rune('0'+i%10)) + ".000.json"
		_ = os.WriteFile(filepath.Join(dir, n), []byte("{}"), 0o644)
	}
	if rr := postDiagReport(t, routes, _diagReportBody); rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != diagKeepFiles+1 {
		t.Fatalf("prune should keep %d+1 files, got %d", diagKeepFiles, len(entries))
	}
}
