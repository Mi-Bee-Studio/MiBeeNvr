package install

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestStarterConfig(t *testing.T) {
	got := StarterConfig(filepath.Join("x", "data"))
	for _, want := range []string{
		`listen: "127.0.0.1:9090"`, // loopback-only default — LAN access is opt-in via the tray
		filepath.Join("x", "data", "data"),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("starter config missing %q:\n%s", want, got)
		}
	}
	// Auth must stay empty so the web first-run setup wizard drives password
	// creation — never bake credentials into the installer.
	if strings.Contains(got, "password") || strings.Contains(got, "username") {
		t.Errorf("starter config must not embed auth, got:\n%s", got)
	}
}

func TestLaunchAgentPlist(t *testing.T) {
	t.Helper()
	got := LaunchAgentPlist("com.mibee-nvr",
		"/u/Applications/MiBeeNVR/mibee-nvr",
		"/u/Library/Application Support/MiBeeNVR/mibee-nvr.yaml",
		"/u/Library/Application Support/MiBeeNVR/nvr.log")
	for _, want := range []string{
		"<string>com.mibee-nvr</string>",
		"<string>/u/Applications/MiBeeNVR/mibee-nvr</string>",
		"<string>-config</string>",
		"<string>/u/Library/Application Support/MiBeeNVR/mibee-nvr.yaml</string>",
		"<key>RunAtLoad</key><true/>",
		"<key>KeepAlive</key><true/>",
		"<string>/u/Library/Application Support/MiBeeNVR/nvr.log</string>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plist missing %q:\n%s", want, got)
		}
	}
	if !strings.HasPrefix(got, "<?xml") {
		t.Errorf("plist must start with xml decl, got:\n%s", got)
	}
}

// The menu-bar helper agent must carry its OWN label — a second agent with
// the NVR's label collides at launchctl load (field report 2026-09-19:
// helper compiled but never appeared) — and no -config pair (it takes none).
func TestLaunchAgentPlistMenuBarHelper(t *testing.T) {
	t.Helper()
	got := LaunchAgentPlist("com.mibee-nvr.bar",
		"/u/Applications/MiBeeNVR/mibee-nvr-bar",
		"",
		"/u/Library/Application Support/MiBeeNVR/nvr-bar.log")
	for _, want := range []string{
		"<string>com.mibee-nvr.bar</string>",
		"<string>/u/Applications/MiBeeNVR/mibee-nvr-bar</string>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("bar plist missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "-config") {
		t.Errorf("bar plist must not pass -config:\n%s", got)
	}
}
