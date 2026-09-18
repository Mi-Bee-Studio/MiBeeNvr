//go:build windows

package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// TestShortcutRoundtrip exercises the raw-vtable .lnk writer against the
// shell itself: create a shortcut, read it back through IShellLinkW and
// verify the target. Runs only on windows hosts (CI's linux runner skips the
// file entirely).
func TestShortcutRoundtrip(t *testing.T) {
	dir := t.TempDir()
	lnk := filepath.Join(dir, "test.lnk")
	target := `C:\Windows\System32\notepad.exe`

	if err := createShortcut(lnk, target, `-config "C:\some dir\mibee-nvr.yaml"`,
		`C:\some dir`, "test shortcut", target+",0"); err != nil {
		t.Fatalf("createShortcut: %v", err)
	}
	if fi, err := os.Stat(lnk); err != nil || fi.Size() < 200 {
		t.Fatalf("shortcut not written: %v %v", fi, err)
	}
	got, err := ReadShortcutPath(lnk)
	if err != nil {
		t.Fatalf("ReadShortcutPath: %v", err)
	}
	if !strings.EqualFold(got, target) {
		t.Fatalf("shortcut target = %q, want %q", got, target)
	}
}

func TestRegistryRoundtrip(t *testing.T) {
	const key = `Software\MiBeeNvrInstallTest`
	k, _, err := registry.CreateKey(registry.CURRENT_USER, key, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	if err := k.SetStringValue("DisplayName", "MiBee NVR"); err != nil {
		t.Fatalf("SetStringValue: %v", err)
	}
	if err := k.SetDWordValue("NoModify", 1); err != nil {
		t.Fatalf("SetDWordValue: %v", err)
	}
	_ = k.Close()

	k2, err := registry.OpenKey(registry.CURRENT_USER, key, registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("OpenKey: %v", err)
	}
	defer k2.Close()
	if s, _, err := k2.GetStringValue("DisplayName"); err != nil || s != "MiBee NVR" {
		t.Fatalf("DisplayName = %q, %v", s, err)
	}
	if d, _, err := k2.GetIntegerValue("NoModify"); err != nil || d != 1 {
		t.Fatalf("NoModify = %d, %v", d, err)
	}
	_ = k2.Close()
	if err := registry.DeleteKey(registry.CURRENT_USER, key); err != nil {
		t.Fatalf("DeleteKey: %v", err)
	}
}
