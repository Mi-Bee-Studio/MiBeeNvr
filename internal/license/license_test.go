package license

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

// mint 用临时密钥对铸造一份授权文档（测试与 API 测试共用形态）。
func mint(t *testing.T, deviceID string, notBefore, expiresAt time.Time) (pubB64, document string) {
	t.Helper()
	pub, priv, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	doc := &Document{
		Version:   1,
		Product:   Product,
		DeviceID:  deviceID,
		Plan:      "p2p-remote",
		Features:  []string{"p2p"},
		Serial:    "test-001",
		NotBefore: notBefore,
		ExpiresAt: expiresAt,
		IssuedAt:  time.Now(),
	}
	document, err = Sign(priv, doc)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	return base64.RawStdEncoding.EncodeToString(pub), document
}

func TestSignDecodeVerifyRoundtrip(t *testing.T) {
	pubB64, document := mint(t, "dev-1", time.Now().Add(-time.Minute), time.Now().Add(12*time.Hour))
	m := NewManager("dev-1")
	if err := m.Configure(document, pubB64, false); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	st := m.Status()
	if st.State != StateValid {
		t.Fatalf("state = %s (reason %s), want valid", st.State, st.Reason)
	}
	if st.Document == nil || st.Document.Plan != "p2p-remote" {
		t.Fatalf("document not surfaced: %+v", st.Document)
	}
	if st.TimeLeftSec <= 0 || st.TimeLeftSec > int64((12*time.Hour).Seconds()) {
		t.Fatalf("TimeLeftSec = %d, want within (0, 12h]", st.TimeLeftSec)
	}
	if !m.Allowed() {
		t.Fatal("Allowed() = false for valid document, want true")
	}
}

func TestStateExpired(t *testing.T) {
	pubB64, document := mint(t, "dev-1", time.Now().Add(-24*time.Hour), time.Now().Add(-time.Second))
	m := NewManager("dev-1")
	_ = m.Configure(document, pubB64, true)
	st := m.Status()
	if st.State != StateExpired {
		t.Fatalf("state = %s, want expired", st.State)
	}
	if m.Allowed() {
		t.Fatal("Allowed() = true for expired document under required, want false")
	}
}

func TestStateNotYetValid(t *testing.T) {
	pubB64, document := mint(t, "dev-1", time.Now().Add(time.Hour), time.Now().Add(25*time.Hour))
	m := NewManager("dev-1")
	_ = m.Configure(document, pubB64, true)
	if st := m.Status(); st.State != StateInvalid {
		t.Fatalf("state = %s (reason %s), want invalid (not yet valid)", st.State, st.Reason)
	}
	if m.Allowed() {
		t.Fatal("Allowed() = true for not-yet-valid document, want false")
	}
}

func TestStateExpiringSoon(t *testing.T) {
	pubB64, document := mint(t, "dev-1", time.Now().Add(-time.Hour), time.Now().Add(2*time.Hour))
	m := NewManager("dev-1")
	_ = m.Configure(document, pubB64, true)
	st := m.Status()
	if st.State != StateExpiringSoon {
		t.Fatalf("state = %s, want expiring", st.State)
	}
	if !m.Allowed() {
		t.Fatal("Allowed() = false for expiring-soon document, want true (信息窗不拦截)")
	}
}

func TestDeviceBindingMismatch(t *testing.T) {
	pubB64, document := mint(t, "dev-other", time.Now().Add(-time.Minute), time.Now().Add(12*time.Hour))
	m := NewManager("dev-1")
	_ = m.Configure(document, pubB64, true)
	if st := m.Status(); st.State != StateInvalid {
		t.Fatalf("state = %s (reason %s), want invalid (device mismatch)", st.State, st.Reason)
	}
}

func TestTamperedPayloadRejected(t *testing.T) {
	pubB64, document := mint(t, "dev-1", time.Now().Add(-time.Minute), time.Now().Add(12*time.Hour))
	payload, sig, doc, err := Decode(document)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	// 篡改载荷：延长有效期后重新编码，签名保持原样。
	doc.ExpiresAt = time.Now().Add(100 * 24 * time.Hour)
	tamperedPayload, _ := json.Marshal(doc)
	tampered := base64.RawStdEncoding.EncodeToString(tamperedPayload) + "." +
		base64.RawStdEncoding.EncodeToString(sig)
	if tampered == document || string(tamperedPayload) == string(payload) {
		t.Fatal("tampering setup failed to change anything")
	}
	m := NewManager("dev-1")
	_ = m.Configure(tampered, pubB64, true)
	st := m.Status()
	if st.State != StateInvalid {
		t.Fatalf("state = %s, want invalid (signature must fail)", st.State)
	}
	if st.Reason != "signature verification failed" {
		t.Fatalf("reason = %q, want signature verification failed", st.Reason)
	}
}

func TestWrongPublicKeyRejected(t *testing.T) {
	_, document := mint(t, "dev-1", time.Now().Add(-time.Minute), time.Now().Add(12*time.Hour))
	otherPub, _, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	m := NewManager("dev-1")
	_ = m.Configure(document, base64.RawStdEncoding.EncodeToString(otherPub), true)
	if st := m.Status(); st.State != StateInvalid {
		t.Fatalf("state = %s, want invalid (wrong key)", st.State)
	}
}

func TestStateNoneAndNotRequiredUngated(t *testing.T) {
	m := NewManager("dev-1")
	st := m.Status()
	if st.State != StateNone {
		t.Fatalf("state = %s, want none", st.State)
	}
	if !m.Allowed() {
		t.Fatal("Allowed() = false with no document and required=false, want true (能力在场未启用)")
	}
	// required=true 且未配文档 → 拦截。
	m.SetRequired(true)
	if m.Allowed() {
		t.Fatal("Allowed() = true with no document and required=true, want false")
	}
	if st := m.Status(); st.State != StateNone || st.Allowed {
		t.Fatalf("state=%s allowed=%v, want none/false", st.State, st.Allowed)
	}
}

func TestConfigureRejectsGarbage(t *testing.T) {
	m := NewManager("dev-1")
	if err := m.Configure("not-a-document", "", false); err == nil {
		t.Fatal("Configure accepted malformed document")
	}
	if err := m.Configure("", "!!!not-base64!!!", false); err == nil {
		t.Fatal("Configure accepted malformed public key")
	}
	pub, _, _ := GenerateKeyPair()
	short := base64.RawStdEncoding.EncodeToString(pub[:16])
	if err := m.Configure("", short, false); err == nil {
		t.Fatal("Configure accepted short public key")
	}
}

func TestEvaluateProductMismatch(t *testing.T) {
	doc := &Document{
		Product:   "other-product",
		DeviceID:  "dev-1",
		NotBefore: time.Now().Add(-time.Hour),
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if st, reason := Evaluate(doc, "dev-1", time.Now()); st != StateInvalid || reason == "" {
		t.Fatalf("state=%s reason=%q, want invalid with reason", st, reason)
	}
}

func TestBlockConstantMatchesPricingGranularity(t *testing.T) {
	if Block != 12*time.Hour {
		t.Fatalf("Block = %v, want 12h (0.5 元/12 小时粒度)", Block)
	}
}
