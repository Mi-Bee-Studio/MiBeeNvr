package cascade

import (
	"encoding/xml"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/ghettovoice/gosip/sip"
	"github.com/mickeyzzc/gb28181-go/manscdp"
	"github.com/stretchr/testify/require"
)

// TestBuildUppers: the legacy single form becomes uppers[0]; upstreams[]
// entries inherit unset fields from it (#370).
func TestBuildUppers(t *testing.T) {
	cfg := config.GB28181CascadeConfig{
		ServerDomain:    "34020000002000000001",
		ServerAddr:      "10.0.0.1:5060",
		LocalDeviceID:   "34020000001320000099",
		Realm:           "3402000000",
		Password:        "secret",
		RegisterExpires: 1800,
	}
	cfg.Upstreams = []config.GB28181CascadeUpstream{{
		ServerDomain:  "34020000002000000002",
		ServerAddr:    "10.0.0.2:5060",
		LocalDeviceID: "34020000001320000077", // per-upper identity
	}}

	uppers := buildUppers(cfg)
	require.Len(t, uppers, 2)
	require.Equal(t, "10.0.0.1:5060", uppers[0].cfg.ServerAddr)
	require.Equal(t, "34020000001320000099", uppers[0].cfg.LocalDeviceID)

	// Inheritance: unset realm/password/expires fall back to the single form;
	// the explicit device ID wins.
	require.Equal(t, "34020000001320000077", uppers[1].cfg.LocalDeviceID)
	require.Equal(t, "3402000000", uppers[1].cfg.Realm)
	require.Equal(t, "secret", uppers[1].cfg.Password)
	require.Equal(t, 1800, uppers[1].cfg.RegisterExpires)

	// No single form (empty server_addr) → only the listed upstreams.
	cfg.ServerAddr = ""
	require.Len(t, buildUppers(cfg), 1)
}

// TestCatalogNotifyBody: the NOTIFY payload carries a Notify root with the
// full item list — the subscription-push form uppers merge on.
func TestCatalogNotifyBody(t *testing.T) {
	body := catalogNotifyBody{
		CmdType:  "Catalog",
		SN:       7,
		DeviceID: "34020000001320000099",
		SumNum:   1,
		Items: []manscdp.Item{{
			DeviceID: "34020000011320000001",
			Name:     "Front",
		}},
	}
	out, err := xml.Marshal(body)
	require.NoError(t, err)
	s := string(out)
	require.Contains(t, s, "<Notify>")
	require.Contains(t, s, "<CmdType>Catalog</CmdType>")
	require.Contains(t, s, "<DeviceList>")
	require.Contains(t, s, "34020000011320000001")
	require.NotContains(t, s, "<Response>")
}

// TestCameraFingerprintChange: the catalog-notify diff trigger notices
// additions, removals, and renames.
func TestCameraFingerprintChange(t *testing.T) {
	svc := New(testCfg(), fakeSource{cams: []CameraInfo{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}}}, newCascadeTestDB(t))
	base := svc.cameraFingerprint()

	same := New(testCfg(), fakeSource{cams: []CameraInfo{{ID: "b", Name: "B"}, {ID: "a", Name: "A"}}}, newCascadeTestDB(t))
	require.Equal(t, base, same.cameraFingerprint(), "order must not matter")

	added := New(testCfg(), fakeSource{cams: []CameraInfo{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}, {ID: "c", Name: "C"}}}, newCascadeTestDB(t))
	require.NotEqual(t, base, added.cameraFingerprint())

	renamed := New(testCfg(), fakeSource{cams: []CameraInfo{{ID: "a", Name: "A"}, {ID: "b", Name: "B2"}}}, newCascadeTestDB(t))
	require.NotEqual(t, base, renamed.cameraFingerprint())
}

// TestUpperOfResolution: multi-upper request routing keys on the From user
// (the upper's server ID).
func TestUpperOfResolution(t *testing.T) {
	cfg := testCfg()
	cfg.Upstreams = []config.GB28181CascadeUpstream{{
		ServerDomain: "34020000002000000002",
		ServerAddr:   "10.0.0.2:5060",
	}}
	svc := New(cfg, fakeSource{cams: []CameraInfo{{ID: "front", Name: "Front"}}}, newCascadeTestDB(t))
	require.Len(t, svc.uppers, 2)

	// Request routing keys on the From user (the upper's server ID): a
	// request from the second upper's domain resolves to uppers[1]; an
	// unknown sender falls back to the first.
	require.Equal(t, svc.uppers[1], svc.upperOf(newFromRequest(t, "34020000002000000002")))
	require.Equal(t, svc.uppers[0], svc.upperOf(newFromRequest(t, "99999999999999999999")))

	// Single-upper deployments short-circuit.
	svc2 := New(testCfg(), fakeSource{}, newCascadeTestDB(t))
	require.Len(t, svc2.uppers, 1)
	require.Equal(t, svc2.uppers[0], svc2.upperOf(newFromRequest(t, "anything")))
}

// newFromRequest assembles a minimal SIP request with the given From user.
func newFromRequest(t *testing.T, user string) sip.Request {
	t.Helper()
	rb := sip.NewRequestBuilder()
	rb.SetMethod(sip.MESSAGE)
	rb.SetFrom(&sip.Address{Uri: &sip.SipUri{FUser: sip.String{Str: user}, FHost: "127.0.0.1"}})
	rb.SetTo(&sip.Address{Uri: &sip.SipUri{FUser: sip.String{Str: "34020000001320000099"}, FHost: "127.0.0.1"}})
	rb.SetRecipient(&sip.SipUri{FUser: sip.String{Str: "34020000001320000099"}, FHost: "127.0.0.1"})
	rb.SetHost("127.0.0.1")
	rb.SetSeqNo(1)
	req, err := rb.Build()
	require.NoError(t, err)
	return req
}
