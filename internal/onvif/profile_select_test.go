package onvif

import "testing"

func TestSelectMainProfile_Empty(t *testing.T) {
	t.Helper()
	if got := SelectMainProfile(nil); got != "" {
		t.Fatalf("expected empty token for nil profiles, got %q", got)
	}
}

func TestSelectMainProfile_Single(t *testing.T) {
	t.Helper()
	profiles := []DeviceProfile{{Token: "prof-1", Width: 1920, Height: 1080}}
	if got := SelectMainProfile(profiles); got != "prof-1" {
		t.Fatalf("expected prof-1, got %q", got)
	}
}

func TestSelectMainProfile_PicksHighestResolution(t *testing.T) {
	t.Helper()
	// Sub-stream first — must NOT be picked.
	profiles := []DeviceProfile{
		{Token: "sub", Width: 640, Height: 480, Encoding: "H264"},
		{Token: "main", Width: 1920, Height: 1080, Encoding: "H264"},
	}
	if got := SelectMainProfile(profiles); got != "main" {
		t.Fatalf("expected main (highest res), got %q", got)
	}
}

func TestSelectMainProfile_PicksMainEvenWhenListedFirst(t *testing.T) {
	t.Helper()
	// Main-stream first — still picked (order shouldn't matter).
	profiles := []DeviceProfile{
		{Token: "main", Width: 1920, Height: 1080},
		{Token: "sub", Width: 640, Height: 480},
	}
	if got := SelectMainProfile(profiles); got != "main" {
		t.Fatalf("expected main, got %q", got)
	}
}

func TestSelectMainProfile_TiebreakByName(t *testing.T) {
	t.Helper()
	// Same resolution — prefer the token/name that looks like a main stream.
	profiles := []DeviceProfile{
		{Token: "profileToken_2", Name: "SubStream", Width: 1920, Height: 1080},
		{Token: "profileToken_1", Name: "MainStream", Width: 1920, Height: 1080},
	}
	if got := SelectMainProfile(profiles); got != "profileToken_1" {
		t.Fatalf("expected main-named profile on tie, got %q", got)
	}
}

func TestSelectMainProfile_MissingResolutionFallsBackToToken(t *testing.T) {
	t.Helper()
	// No resolution info (minimal ONVIF device) — tiebreak on name; otherwise
	// keep list order. Both have 0 pixels, so the main-named one wins.
	profiles := []DeviceProfile{
		{Token: "p2", Name: "secondary"},
		{Token: "p1", Name: "main"},
	}
	if got := SelectMainProfile(profiles); got != "p1" {
		t.Fatalf("expected main-named profile when no resolution, got %q", got)
	}
}

func TestLooksLikeMain(t *testing.T) {
	t.Helper()
	cases := []struct {
		token, name string
		want        bool
	}{
		{"MainStreamToken", "", true},
		{"", "MainStream", true},
		{"profile_1", "主码流", true},
		{"sub", "", false},
		{"", "secondary", false},
		{"", "extra1", false},
		{"profile_2", "", false},
	}
	for _, c := range cases {
		if got := looksLikeMain(c.token, c.name); got != c.want {
			t.Errorf("looksLikeMain(%q,%q) = %v, want %v", c.token, c.name, got, c.want)
		}
	}
}
