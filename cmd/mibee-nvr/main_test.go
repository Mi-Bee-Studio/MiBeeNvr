package main

import (
	"os"
	"path/filepath"
	"testing"
)

// recorder tracks which stub subcommand handlers were invoked.
var recorder []string

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
			recorder = nil

			// Install recording stubs.
			cmdEncryptConfigFn = func() { recorder = append(recorder, "encrypt-config") }
			cmdDownloadModelFn = func() { recorder = append(recorder, "download-model") }

			// Exercise dispatch.
			dispatchSubcommand(tt.args)

			if len(recorder) != len(tt.wantCalls) {
				t.Fatalf("expected %d call(s), got %d: %v",
					len(tt.wantCalls), len(recorder), recorder)
			}
			for i, want := range tt.wantCalls {
				if recorder[i] != want {
					t.Errorf("call %d: expected %q, got %q", i, want, recorder[i])
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

	recorder = nil
	cmdEncryptConfigFn = func() { recorder = append(recorder, "encrypt-config") }
	cmdDownloadModelFn = func() { recorder = append(recorder, "download-model") }

	dispatchSubcommand([]string{"mibee-nvr", "unknown-command"})

	if len(recorder) != 0 {
		t.Errorf("expected 0 calls for unknown subcommand, got %v", recorder)
	}
}

// TestNoArgs verifies dispatch does nothing when no subcommand is given.
func TestNoArgs(t *testing.T) {
	origEnc := cmdEncryptConfigFn
	origDl := cmdDownloadModelFn
	t.Cleanup(func() {
		cmdEncryptConfigFn = origEnc
		cmdDownloadModelFn = origDl
	})

	recorder = nil
	cmdEncryptConfigFn = func() { recorder = append(recorder, "encrypt-config") }
	cmdDownloadModelFn = func() { recorder = append(recorder, "download-model") }

	dispatchSubcommand([]string{"mibee-nvr"})

	if len(recorder) != 0 {
		t.Errorf("expected 0 calls with no args, got %v", recorder)
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
