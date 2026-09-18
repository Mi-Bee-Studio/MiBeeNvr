//go:build !windows && !darwin

package install

import "fmt"

// Paths is unsupported on non-desktop platforms — servers install via the
// Makefile systemd targets (install-service).
func Paths() (string, string, string, error) {
	return "", "", "", fmt.Errorf("install: 桌面安装仅支持 windows/darwin；Linux 服务器请使用 make install-service（systemd）")
}

func Install(opts Options) (*Result, error) {
	return nil, unsupported()
}

func Uninstall(purge bool) (*Result, error) {
	return nil, unsupported()
}

func setupConsole() {}

// RefreshMenuBarHelper has no menu-bar helper to refresh off-desktop.
func RefreshMenuBarHelper() error { return nil }

func unsupported() error {
	return fmt.Errorf("install: 桌面安装仅支持 windows/darwin；Linux 服务器请使用 systemd（make install-service / uninstall-service）")
}
