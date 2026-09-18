package main

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestWantsDesktopInstall(t *testing.T) {
	installedPath := filepath.Join(string(filepath.Separator), "Apps", "MiBeeNVR", "mibee-nvr")
	installed := func() string { return installedPath }

	cases := []struct {
		name    string
		goos    string
		argsLen int
		self    string
		want    bool
	}{
		{"bare windows download", "windows", 1, `C:\Users\u\Downloads\MiBeeNVR-Setup.exe`, true},
		{"bare darwin dmg app", "darwin", 1, "/Volumes/MiBee NVR/MiBeeNVR.app/Contents/MacOS/mibee-nvr", true},
		{"installed exe serves instead", "windows", 1, installedPath, false},
		{"flags mean server/cli", "windows", 2, `C:\Downloads\Setup.exe`, false},
		{"subcommand", "darwin", 2, "/tmp/mibee-nvr", false},
		{"linux never installs", "linux", 1, "/opt/mibee-nvr", false},
		{"no self path", "windows", 1, "", false},
		{"no argv at all", "windows", 0, installedPath, false},
	}
	for _, tc := range cases {
		if got := wantsDesktopInstall(tc.goos, tc.argsLen, tc.self, installed); got != tc.want {
			t.Errorf("%s: wantsDesktopInstall(%q,%d,%q) = %v, want %v",
				tc.name, tc.goos, tc.argsLen, tc.self, got, tc.want)
		}
	}

	// Unresolvable install location → classic server path.
	if got := wantsDesktopInstall("windows", 1, `C:\Downloads\Setup.exe`, func() string { return "" }); got {
		t.Error("unresolvable installed exe must not trigger install")
	}
}

func TestSameExePath(t *testing.T) {
	a := filepath.Join(string(filepath.Separator), "x", "mibee-nvr")
	if !sameExePath(a, a+string(filepath.Separator)) {
		t.Error("trailing separator must not matter")
	}
	if sameExePath(a, filepath.Join(string(filepath.Separator), "y", "mibee-nvr")) {
		t.Error("different dirs must differ")
	}
	// Windows paths are case-insensitive; other platforms are not.
	b := filepath.Join(string(filepath.Separator), "X", "MiBee-NVR")
	if runtime.GOOS == "windows" {
		if !sameExePath(a, b) {
			t.Error("windows paths must compare case-insensitively")
		}
	}
}
