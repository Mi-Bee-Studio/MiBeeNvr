package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/license"
	"github.com/stretchr/testify/require"
)

// quietGoroutineTripwire lifts the goroutine check's baseline to the config
// maximum: under the full parallel suite, leftover goroutines from other
// tests would otherwise trip the default threshold and make every overall-
// status assertion environment-dependent.
func quietGoroutineTripwire(h *Handler) {
	h.config = &config.Config{Health: config.HealthConfig{GoroutineBaseline: 1000000}}
}

// mintLicense produces (pubB64, signedDocument) for a required=true manager,
// the same way the operational minting tool does.
func mintLicense(t *testing.T, deviceID string, expiresAt time.Time) (string, string) {
	t.Helper()
	pub, priv, err := license.GenerateKeyPair()
	require.NoError(t, err)
	doc, err := license.Sign(priv, &license.Document{
		Version:   1,
		Product:   "mibeenvr",
		DeviceID:  deviceID,
		Plan:      "p2p-remote",
		Features:  []string{"p2p"},
		Serial:    "test-serial",
		NotBefore: time.Now().Add(-time.Minute),
		ExpiresAt: expiresAt,
		IssuedAt:  time.Now(),
	})
	require.NoError(t, err)
	return base64.RawStdEncoding.EncodeToString(pub), doc
}

type healthPayload struct {
	Status  string                     `json:"status"`
	Checks  map[string]json.RawMessage `json:"checks"`
	License *HealthLicense             `json:"license"`
}

// TestHealthLicenseSummary pins #49: the licensing gate must be visible on
// the standard probe endpoint — the incident was a required gate silently
// blocking remote access for a week with no health/panel/event signal.
func TestHealthLicenseSummary(t *testing.T) {
	t.Run("required gate blocking (the incident shape)", func(t *testing.T) {
		t.Parallel()
		db, store := setupTestDB(t)
		t.Cleanup(func() { db.Close() })
		h := TestHandler(db, store)
		m := license.NewManager("dev-h1")
		require.NoError(t, m.Configure("", "", true)) // doc wiped, required stays
		quietGoroutineTripwire(h)
		h.SetLicenseManager(m)

		rr := doRequest(t, h.Routes(), http.MethodGet, "/api/health", nil, "", "")
		require.Equal(t, http.StatusOK, rr.Code)

		var p healthPayload
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &p))
		require.NotNil(t, p.License, "enforced gate must surface in /api/health")
		require.Equal(t, "none", p.License.State)
		require.True(t, p.License.Required)
		require.False(t, p.License.Allowed)
		require.Equal(t, "warning", checkStatus(t, p.Checks["license"]))
		require.Equal(t, "degraded", p.Status, "a blocking gate degrades overall health")
	})

	t.Run("valid license is ok and not degrading", func(t *testing.T) {
		t.Parallel()
		db, store := setupTestDB(t)
		t.Cleanup(func() { db.Close() })
		h := TestHandler(db, store)
		pub, doc := mintLicense(t, "dev-h2", time.Now().Add(30*24*time.Hour))
		m := license.NewManager("dev-h2")
		require.NoError(t, m.Configure(doc, pub, true))
		quietGoroutineTripwire(h)
		h.SetLicenseManager(m)

		rr := doRequest(t, h.Routes(), http.MethodGet, "/api/health", nil, "", "")
		require.Equal(t, http.StatusOK, rr.Code)

		var p healthPayload
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &p))
		require.NotNil(t, p.License)
		require.Equal(t, "valid", p.License.State)
		require.True(t, p.License.Allowed)
		require.NotEmpty(t, p.License.ExpiresAt)
		require.Equal(t, "ok", checkStatus(t, p.Checks["license"]))
		require.Equal(t, "ok", p.Status)
	})

	t.Run("expiring soon warns", func(t *testing.T) {
		t.Parallel()
		db, store := setupTestDB(t)
		t.Cleanup(func() { db.Close() })
		h := TestHandler(db, store)
		pub, doc := mintLicense(t, "dev-h3", time.Now().Add(time.Hour)) // < 6h window
		m := license.NewManager("dev-h3")
		require.NoError(t, m.Configure(doc, pub, true))
		quietGoroutineTripwire(h)
		h.SetLicenseManager(m)

		rr := doRequest(t, h.Routes(), http.MethodGet, "/api/health", nil, "", "")
		require.Equal(t, http.StatusOK, rr.Code)

		var p healthPayload
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &p))
		require.Equal(t, "expiring", p.License.State)
		require.True(t, p.License.Allowed, "expiring is informational, still allowed")
		require.Equal(t, "warning", checkStatus(t, p.Checks["license"]))
		require.Equal(t, "degraded", p.Status)
	})

	t.Run("not enforced stays invisible", func(t *testing.T) {
		t.Parallel()
		db, store := setupTestDB(t)
		t.Cleanup(func() { db.Close() })
		h := TestHandler(db, store)
		m := license.NewManager("dev-h4")
		require.NoError(t, m.Configure("", "", false))
		quietGoroutineTripwire(h)
		h.SetLicenseManager(m)

		rr := doRequest(t, h.Routes(), http.MethodGet, "/api/health", nil, "", "")
		require.Equal(t, http.StatusOK, rr.Code)

		var p healthPayload
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &p))
		require.Nil(t, p.License, "non-required deployments must not advertise license machinery")
		_, has := p.Checks["license"]
		require.False(t, has)
		require.Equal(t, "ok", p.Status)
	})
}

func checkStatus(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	if len(raw) == 0 {
		t.Fatal("check entry missing")
	}
	var c struct {
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal(raw, &c))
	return c.Status
}
