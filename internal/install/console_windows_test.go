//go:build windows

package install

import (
	"os"
	"testing"
)

// TestStdHandleValid: a live handle Stats cleanly; a nil file does not. The
// GUI-subsystem redirect logic keys off this, so a false negative would
// break terminal redirection and a false positive would swallow output.
func TestStdHandleValid(t *testing.T) {
	if !stdHandleValid(os.Stdout) {
		t.Fatal("os.Stdout should be a valid handle under go test")
	}
	if stdHandleValid(nil) {
		t.Fatal("nil file must not count as a valid handle")
	}
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Skipf("cannot open %s: %v", os.DevNull, err)
	}
	defer f.Close()
	if !stdHandleValid(f) {
		t.Fatal("freshly opened file should be valid")
	}
}

// TestEnsureParentConsoleSpawnGuard: the installer-spawned server child must
// skip the parent-console attach entirely — the attach is what would keep
// the installer's console alive for the server's lifetime.
func TestEnsureParentConsoleSpawnGuard(t *testing.T) {
	t.Setenv(desktopSpawnEnv, "1")
	// Must return without attaching (and without panicking); observable
	// effect is "no console gained", which under `go test` already has one.
	EnsureParentConsole()
}
