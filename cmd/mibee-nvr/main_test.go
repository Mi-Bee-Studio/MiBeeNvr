package main

import (
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
