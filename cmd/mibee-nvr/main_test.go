package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// dispatchRecorder tracks which stub subcommand handlers were invoked.
var dispatchRecorder []string

// TestSubcommandDispatch verifies that CLI subcommands are correctly dispatched.
//
// It uses test stubs via cmdEncryptConfigFn / cmdDownloadModelFn function
// variables, avoiding the real implementations which call os.Exit().
func TestSubcommandDispatch(t *testing.T) {
	origEnc := cmdEncryptConfigFn
	origDl := cmdDownloadModelFn
	t.Cleanup(func() {
		cmdEncryptConfigFn = origEnc
		cmdDownloadModelFn = origDl
	})

	tests := []struct {
		name      string
		args      []string
		wantCalls []string // expected subcommands called, in order
	}{
		{
			name:      "encrypt-config dispatches cmdEncryptConfig only",
			args:      []string{"mibee-nvr", "encrypt-config"},
			wantCalls: []string{"encrypt-config"},
		},
		{
			name:      "download-model dispatches cmdDownloadModel only",
			args:      []string{"mibee-nvr", "download-model"},
			wantCalls: []string{"download-model"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dispatchRecorder = nil

			// Install recording stubs.
			cmdEncryptConfigFn = func() { dispatchRecorder = append(dispatchRecorder, "encrypt-config") }
			cmdDownloadModelFn = func() { dispatchRecorder = append(dispatchRecorder, "download-model") }

			// Exercise dispatch.
			dispatchSubcommand(tt.args)

			if len(dispatchRecorder) != len(tt.wantCalls) {
				t.Fatalf("expected %d call(s), got %d: %v",
					len(tt.wantCalls), len(dispatchRecorder), dispatchRecorder)
			}
			for i, want := range tt.wantCalls {
				if dispatchRecorder[i] != want {
					t.Errorf("call %d: expected %q, got %q", i, want, dispatchRecorder[i])
				}
			}
		})
	}
}

// TestUnrecognizedSubcommand verifies that an unknown subcommand does nothing.
func TestUnrecognizedSubcommand(t *testing.T) {
	origEnc := cmdEncryptConfigFn
	origDl := cmdDownloadModelFn
	t.Cleanup(func() {
		cmdEncryptConfigFn = origEnc
		cmdDownloadModelFn = origDl
	})

	dispatchRecorder = nil
	cmdEncryptConfigFn = func() { dispatchRecorder = append(dispatchRecorder, "encrypt-config") }
	cmdDownloadModelFn = func() { dispatchRecorder = append(dispatchRecorder, "download-model") }

	dispatchSubcommand([]string{"mibee-nvr", "unknown-command"})

	if len(dispatchRecorder) != 0 {
		t.Errorf("expected 0 calls for unknown subcommand, got %v", dispatchRecorder)
	}
}

// TestNoArgs verifies the bare dispatch path. On windows/darwin a bare run
// is the desktop installer entry (stubbed here — a unit test must never
// install for real); elsewhere nothing happens.
func TestNoArgs(t *testing.T) {
	origEnc := cmdEncryptConfigFn
	origDl := cmdDownloadModelFn
	origDesk := cmdInstallDesktopFn
	t.Cleanup(func() {
		cmdEncryptConfigFn = origEnc
		cmdDownloadModelFn = origDl
		cmdInstallDesktopFn = origDesk
	})

	dispatchRecorder = nil
	desktopCalls := 0
	cmdEncryptConfigFn = func() { dispatchRecorder = append(dispatchRecorder, "encrypt-config") }
	cmdDownloadModelFn = func() { dispatchRecorder = append(dispatchRecorder, "download-model") }
	cmdInstallDesktopFn = func() { desktopCalls++ }

	dispatchSubcommand([]string{"mibee-nvr"})

	if len(dispatchRecorder) != 0 {
		t.Errorf("expected 0 subcommand calls with no args, got %v", dispatchRecorder)
	}
	switch runtime.GOOS {
	case "windows", "darwin":
		// The test binary never lives at the installed location, so the
		// desktop entry fires — routed to the stub, never the real install.
		if desktopCalls != 1 {
			t.Errorf("expected 1 desktop-install call on %s, got %d", runtime.GOOS, desktopCalls)
		}
	default:
		if desktopCalls != 0 {
			t.Errorf("expected 0 desktop-install calls on %s, got %d", runtime.GOOS, desktopCalls)
		}
	}
}

// writeFile is a t.Helper test helper that writes content to path.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestResolveHealthAddr verifies the health-probe address resolution,
// including Docker auto-detection of the configured listen port (issue #77).
func TestResolveHealthAddr(t *testing.T) {
	// Build a fake Docker data dir with a config carrying a custom listen port.
	dataDir := t.TempDir()
	writeFile(t, filepath.Join(dataDir, "mibee-nvr.yaml"),
		"server:\n  listen: \":9191\"\n")
	// Point dockerStorageDir() at our temp dir and ensure no real /data leaks in.
	t.Setenv("NVR_DATA_DIR", dataDir)

	tests := []struct {
		name    string
		args    []string
		want    string
		wantErr bool
	}{
		{
			name: "no flags auto-detects Docker config port",
			args: []string{"mibee-nvr", "health"},
			want: ":9191",
		},
		{
			name: "--addr overrides auto-detection",
			args: []string{"mibee-nvr", "health", "--addr", ":7777"},
			want: ":7777",
		},
		{
			name: "--config overrides auto-detection",
			args: []string{
				"mibee-nvr", "health", "--config",
				filepath.Join(dataDir, "mibee-nvr.yaml"),
			},
			want: ":9191",
		},
		{
			name: "--config with nonexistent path errors",
			args: []string{
				"mibee-nvr", "health", "--config",
				filepath.Join(dataDir, "nope.yaml"),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveHealthAddr(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got addr=%q err=nil", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("addr: got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestResolveHealthAddrNoDocker verifies the default port when not in Docker.
func TestResolveHealthAddrNoDocker(t *testing.T) {
	// Neutralize Docker detection: no NVR_DATA_DIR and the /data auto-detect
	// path can't be disabled, so rely on --addr to avoid environment coupling.
	t.Setenv("NVR_DATA_DIR", "")
	got, err := resolveHealthAddr([]string{"mibee-nvr", "health", "--addr", ":9090"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != ":9090" {
		t.Errorf("addr: got %q, want \":9090\"", got)
	}
}

// TestStorageRootUsable covers the #434 gate: a recordings root that exists
// or can be created is usable (custom-mounted volumes stay untouched), while
// an empty path or one under a regular file can never be — the Docker
// auto-fix must fall back to the container data volume for those.
