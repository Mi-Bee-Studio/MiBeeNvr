package license

import (
	"encoding/base64"
	"testing"
	"time"
)

// TestFeatureAllowed 钉死按能力门禁语义：未强制恒放行；强制时空 features
// = 全能（兼容存量文档）；有清单按清单判。
func TestFeatureAllowed(t *testing.T) {
	pub, priv, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	pubB64 := base64.RawStdEncoding.EncodeToString(pub)
	mint := func(features []string) string {
		doc := &Document{
			Version: 1, Product: Product, DeviceID: "dev-1",
			Plan: "p2p-remote", Features: features,
			NotBefore: time.Now().Add(-time.Minute),
			ExpiresAt: time.Now().Add(12 * time.Hour),
		}
		s, err := Sign(priv, doc)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	// 未强制：无文档也放行。
	m := NewManager("dev-1")
	if !m.FeatureAllowed("p2p") {
		t.Fatal("unenforced license must allow everything")
	}

	// 强制 + 空 features = 全能。
	if err := m.Configure(mint(nil), pubB64, true); err != nil {
		t.Fatal(err)
	}
	if !m.FeatureAllowed("p2p") {
		t.Fatal("empty feature list must allow everything")
	}

	// 强制 + features=["audio"]：p2p 拒、audio 许。
	if err := m.Configure(mint([]string{"audio"}), pubB64, true); err != nil {
		t.Fatal(err)
	}
	if m.FeatureAllowed("p2p") {
		t.Fatal("p2p not in features must be denied")
	}
	if !m.FeatureAllowed("audio") {
		t.Fatal("audio in features must be allowed")
	}

	// 强制 + features=["p2p","audio"]。
	if err := m.Configure(mint([]string{"p2p", "audio"}), pubB64, true); err != nil {
		t.Fatal(err)
	}
	if !m.FeatureAllowed("p2p") {
		t.Fatal("p2p in features must be allowed")
	}
}

// TestMaxClientsQuota 配额语义：valid/expiring 报文档配额；invalid/
// expired/未配 → 0（拨号本就被 Allowed 拦截）。
func TestMaxClientsQuota(t *testing.T) {
	pub, priv, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	pubB64 := base64.RawStdEncoding.EncodeToString(pub)
	mint := func(quota int, expires time.Time) string {
		doc := &Document{
			Version: 1, Product: Product, DeviceID: "dev-1",
			Plan: "p2p-remote", Features: []string{"p2p"}, MaxClients: quota,
			NotBefore: time.Now().Add(-time.Minute), ExpiresAt: expires,
		}
		s, err := Sign(priv, doc)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	m := NewManager("dev-1")
	if err := m.Configure(mint(5, time.Now().Add(12*time.Hour)), pubB64, true); err != nil {
		t.Fatal(err)
	}
	if got := m.MaxClients(); got != 5 {
		t.Fatalf("valid doc quota = %d, want 5", got)
	}

	// expiring（< 6h 余量）仍计配额。
	if err := m.Configure(mint(3, time.Now().Add(time.Hour)), pubB64, true); err != nil {
		t.Fatal(err)
	}
	if got := m.MaxClients(); got != 3 {
		t.Fatalf("expiring doc quota = %d, want 3", got)
	}

	// 过期 → 0。
	if err := m.Configure(mint(9, time.Now().Add(-time.Minute)), pubB64, true); err != nil {
		t.Fatal(err)
	}
	if got := m.MaxClients(); got != 0 {
		t.Fatalf("expired doc quota = %d, want 0", got)
	}

	// 无文档 → 0。
	if err := m.Configure("", "", true); err != nil {
		t.Fatal(err)
	}
	if got := m.MaxClients(); got != 0 {
		t.Fatalf("no-doc quota = %d, want 0", got)
	}
}

// TestDocumentMaxClientsRoundTrip 确认 max_clients 字段经 Sign/Decode
// 往返保持（含 0 = omitempty 不出现在载荷里的兼容形态）。
func TestDocumentMaxClientsRoundTrip(t *testing.T) {
	_, priv, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	doc := &Document{
		Version: 1, Product: Product, DeviceID: "dev-1",
		Plan: "pro", Features: []string{"p2p"}, Serial: "q-1", MaxClients: 7,
		NotBefore: time.Now(), ExpiresAt: time.Now().Add(Block), IssuedAt: time.Now(),
	}
	s, err := Sign(priv, doc)
	if err != nil {
		t.Fatal(err)
	}
	_, _, parsed, err := Decode(s)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.MaxClients != 7 {
		t.Fatalf("round-trip max_clients = %d, want 7", parsed.MaxClients)
	}
	if len(parsed.Features) != 1 || parsed.Features[0] != "p2p" {
		t.Fatalf("round-trip features = %v, want [p2p]", parsed.Features)
	}
}
