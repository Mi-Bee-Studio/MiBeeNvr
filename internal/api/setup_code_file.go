package api

// setup_code_file.go — first-boot setup code file (companion to the #879
// arming, issue #1032): headless deployments (Docker on a NAS) have no
// terminal, and a wrapper-script log never carries the app's own stdout, so
// the printed code was effectively unreachable. While setup is pending, the
// code is additionally written to setup-code.txt next to the config file —
// /data in container deployments, i.e. the volume the user mounted and can
// browse. It is removed the moment setup completes, and at boot when
// credentials already exist.
//
// Security: reading the file requires data-directory access, and anyone with
// write access there already owns the NVR (mibee-nvr.yaml with password_hash
// sits in the same folder). The #879 threat — a NETWORK peer claiming the
// admin account — cannot read the volume, so the arming guarantee stands.

import (
	"fmt"
	"os"
	"path/filepath"
)

// firstBootSetupCodePath returns the setup-code.txt location: the config
// file's directory.
func firstBootSetupCodePath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "setup-code.txt")
}

// WriteFirstBootSetupCodeFile drops the current first-boot code next to the
// config file so headless operators can read it without terminal access.
// Best-effort by contract: callers treat an error as non-fatal because the
// code is still printed to stdout.
func WriteFirstBootSetupCodeFile(configPath, code string) error {
	content := fmt.Sprintf(`MiBee NVR 首次设置校验码 / first-boot setup code

%s

在初始化向导的「设置校验码」一栏填入上面的码，完成管理员账户设置。
Enter the code above in the setup wizard's "setup code" field to create
the admin account.

- 本机(localhost)访问的向导无需填写 / local (localhost) wizards don't ask for it
- 码在完成设置前每次重启都会更换，以最新一次为准 / the code rotates on
  every restart until setup completes — always use the newest one
- 完成设置后本文件自动删除 / this file is deleted once setup is done
`, code)
	return os.WriteFile(firstBootSetupCodePath(configPath), []byte(content), 0o600)
}

// RemoveFirstBootSetupCodeFile deletes the code file (setup completed, or a
// boot with credentials already present). A missing file is not an error.
func RemoveFirstBootSetupCodeFile(configPath string) {
	_ = os.Remove(firstBootSetupCodePath(configPath))
}
