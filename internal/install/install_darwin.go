//go:build darwin

package install

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/tray"
)

const (
	installDirName = "MiBeeNVR" // under ~/Applications
	label          = "com.mibee-nvr"
	barLabel       = "com.mibee-nvr.bar"
)

//go:embed bar_helper.swift
var barHelperSwift string

// Paths returns the per-user install locations.
func Paths() (exeDir, dataDir, configPath string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", "", fmt.Errorf("install: home dir: %w", err)
	}
	exeDir = filepath.Join(home, "Applications", installDirName)
	dataDir = filepath.Join(home, "Library", "Application Support", "MiBeeNVR")
	return exeDir, dataDir, filepath.Join(dataDir, "mibee-nvr.yaml"), nil
}

// InstalledExePath is the installed binary location ("" when unresolvable).
func InstalledExePath() string {
	exeDir, _, _, err := Paths()
	if err != nil {
		return ""
	}
	return filepath.Join(exeDir, "mibee-nvr")
}

// OpenBrowser opens url in the default browser. Run (not Start): `open`
// hands off to LaunchServices and returns immediately, and the waited
// process keeps staticguard happy / leaves no zombie.
func OpenBrowser(url string) {
	_ = exec.Command("open", url).Run()
}

// NotifyDialog shows a modal osascript dialog — the only visible feedback
// when the installer was launched from Finder (an LSUIElement .app has no
// console, stdout goes nowhere).
func NotifyDialog(text string) {
	script := "display dialog " + applescriptQuote(text) + " buttons {\"好\"} default button 1 with icon note"
	_ = exec.Command("osascript", "-e", script).Run()
}

// applescriptQuote wraps s in double quotes, escaping " and \.
func applescriptQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

func agentPlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("install: home dir: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), nil
}

func barAgentPlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("install: home dir: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", barLabel+".plist"), nil
}

// Install copies the running binary to ~/Applications/MiBeeNVR, wires the
// per-user LaunchAgent (RunAtLoad + KeepAlive, logs to the data dir) and the
// optional menu-bar helper (swiftc-compiled AppKit app). No sudo anywhere.
func Install(opts Options) (*Result, error) {
	exeDir, dataDir, cfgPath, err := Paths()
	if err != nil {
		return nil, err
	}
	selfExe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("install: locate running binary: %w", err)
	}
	exePath := filepath.Join(exeDir, "mibee-nvr")

	for _, d := range []string{exeDir, dataDir, filepath.Join(dataDir, "data")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("install: mkdir %s: %w", d, err)
		}
	}
	data, err := os.ReadFile(selfExe)
	if err != nil {
		return nil, fmt.Errorf("install: read binary: %w", err)
	}
	if err := os.WriteFile(exePath, data, 0o755); err != nil {
		return nil, fmt.Errorf("install: 写入 %s 失败: %w", exePath, err)
	}
	// Files written by a quarantined (downloaded, unsigned) installer inherit
	// the quarantine xattr — launchd-exec'ing such a copy gets it KILLED by
	// Gatekeeper with no prompt to approve, which reads as "installed but
	// never starts". The user already approved this installer via the
	// right-click/Open dance; clearing the flag on our own copies is the
	// standard self-installer move.
	stripQuarantine(exePath)
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		if err := os.WriteFile(cfgPath, []byte(StarterConfig(dataDir)), 0o600); err != nil {
			return nil, fmt.Errorf("install: write starter config: %w", err)
		}
	}

	plist, err := agentPlistPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		return nil, fmt.Errorf("install: mkdir LaunchAgents: %w", err)
	}
	logPath := filepath.Join(dataDir, "nvr.log")
	if err := os.WriteFile(plist, []byte(LaunchAgentPlist(label, exePath, cfgPath, logPath)), 0o644); err != nil {
		return nil, fmt.Errorf("install: write LaunchAgent: %w", err)
	}

	// Reload the agent (unload first so re-installs pick up the new binary).
	_, _ = runLaunchctl("unload", plist)
	if out, err := runLaunchctl("load", "-w", plist); err != nil {
		return nil, fmt.Errorf("install: launchctl load: %w\n%s", err, out)
	}

	res := &Result{Exe: exePath, Config: cfgPath, DataDir: dataDir}
	// Honest Started: poll the configured listener instead of assuming the
	// agent came up — a blocked/killed exec surfaces here instead of as a
	// silent failure.
	healthURL := healthURLForConfig(cfgPath)
	res.Started = waitForHTTP(healthURL, 10*time.Second)
	if !res.Started {
		res.Notes = append(res.Notes,
			"⚠️ 服务未能在 10 秒内就绪——排查：launchctl list | grep mibee；日志："+logPath)
	}
	res.Notes = append(res.Notes,
		"已注册 LaunchAgent（登录自启 + 崩溃自动拉起）：launchctl list | grep mibee",
		"日志："+logPath+"（tail -f 跟踪）",
		"管理界面：http://127.0.0.1:9090（默认仅本机、浏览器免密；菜单栏「监听地址…」可开放局域网）")

	// Menu-bar helper: compiled on the spot with swiftc (ships with Xcode
	// Command Line Tools). Optional — a missing compiler only costs the bar
	// icon; the NVR itself stays fully manageable via launchctl + the web UI.
	if err := installMenuBarHelper(exeDir, dataDir, cfgPath); err != nil {
		res.Notes = append(res.Notes, "菜单栏图标未安装："+err.Error())
	} else {
		res.Notes = append(res.Notes, "菜单栏图标已就绪（打开 Web 界面 / 修改密码 / 监听地址 / 退出）")
	}
	return res, nil
}

// HideOwnConsole is windows-only (console hiding for server mode).
func HideOwnConsole() {}

// stripQuarantine best-effort removes com.apple.quarantine from path.
func stripQuarantine(path string) {
	_, _ = runCmd("xattr", "-d", "com.apple.quarantine", path)
}

// RefreshMenuBarHelper recompiles and restarts the menu-bar helper after a
// listen change so its baked base URL follows the new address. No-op when
// the helper was never installed (or on non-darwin builds).
func RefreshMenuBarHelper() error {
	barPlist, err := barAgentPlistPath()
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(barPlist); statErr != nil {
		return nil // helper not installed — nothing to refresh
	}
	exeDir, dataDir, cfgPath, err := Paths()
	if err != nil {
		return err
	}
	return installMenuBarHelper(exeDir, dataDir, cfgPath)
}

// installMenuBarHelper compiles the embedded AppKit helper with swiftc and
// registers its own LaunchAgent (RunAtLoad + KeepAlive).
func installMenuBarHelper(exeDir, dataDir, cfgPath string) error {
	if _, err := exec.LookPath("swiftc"); err != nil {
		return fmt.Errorf("未找到 swiftc（安装 Xcode Command Line Tools 后重新执行 mibee-nvr install）")
	}
	base := "http://127.0.0.1:9090"
	if cfg, err := config.Load(cfgPath); err == nil && cfg.Server.Listen != "" {
		base = tray.ListenURL(cfg.Server.Listen)
	}
	src := filepath.Join(dataDir, "mibee-nvr-bar.swift")
	if err := os.WriteFile(src, []byte(strings.ReplaceAll(barHelperSwift, "__BASE_URL__", base)), 0o644); err != nil {
		return fmt.Errorf("写助手源码: %w", err)
	}
	barPath := filepath.Join(exeDir, "mibee-nvr-bar")
	out, err := runCmd("swiftc", "-O", "-o", barPath, src)
	if err != nil {
		return fmt.Errorf("swiftc 编译失败: %v\n%s", err, out)
	}
	stripQuarantine(barPath) // launchd would kill a quarantined helper

	barPlist, err := barAgentPlistPath()
	if err != nil {
		return err
	}
	logPath := filepath.Join(dataDir, "nvr-bar.log")
	if err := os.WriteFile(barPlist, []byte(LaunchAgentPlist(barLabel, barPath, "", logPath)), 0o644); err != nil {
		return fmt.Errorf("write bar LaunchAgent: %w", err)
	}
	_, _ = runLaunchctl("unload", barPlist)
	if out, err := runLaunchctl("load", "-w", barPlist); err != nil {
		return fmt.Errorf("launchctl load bar: %v\n%s", err, out)
	}
	return nil
}

// Uninstall unloads the LaunchAgents and removes program files. Data is kept
// unless purge is true.
func Uninstall(purge bool) (*Result, error) {
	exeDir, dataDir, cfgPath, err := Paths()
	if err != nil {
		return nil, err
	}
	plist, err := agentPlistPath()
	if err != nil {
		return nil, err
	}
	res := &Result{Exe: filepath.Join(exeDir, "mibee-nvr"), Config: cfgPath, DataDir: dataDir}

	if _, statErr := os.Stat(plist); statErr == nil {
		if out, err := runLaunchctl("unload", "-w", plist); err != nil {
			return nil, fmt.Errorf("install: launchctl unload: %w\n%s", err, out)
		}
		_ = os.Remove(plist)
	}
	if barPlist, err := barAgentPlistPath(); err == nil {
		if _, statErr := os.Stat(barPlist); statErr == nil {
			_, _ = runLaunchctl("unload", "-w", barPlist)
			_ = os.Remove(barPlist)
		}
	}
	_ = os.RemoveAll(exeDir)

	if purge {
		if err := os.RemoveAll(dataDir); err != nil {
			return nil, fmt.Errorf("install: 清除数据目录: %w", err)
		}
		res.Notes = append(res.Notes, "已清除数据目录（配置 + 录像 + 数据库）")
	} else {
		res.KeptData = true
		res.Notes = append(res.Notes, "数据目录已保留（配置 + 录像 + 数据库）："+dataDir)
		res.Notes = append(res.Notes, "如需一并删除，运行: mibee-nvr uninstall --purge")
	}
	return res, nil
}

func runLaunchctl(args ...string) (string, error) {
	out, err := exec.Command("launchctl", args...).CombinedOutput()
	return string(out), err
}

func runCmd(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

func setupConsole() {}
