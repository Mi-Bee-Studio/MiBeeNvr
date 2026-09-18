//go:build darwin

package install

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	installDirName = "MiBeeNVR" // under ~/Applications
	label          = "com.mibee-nvr"
)

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

func agentPlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("install: home dir: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), nil
}

// Install copies the running binary to ~/Applications/MiBeeNVR and wires a
// per-user LaunchAgent (RunAtLoad + KeepAlive, logs to the data dir). No
// sudo anywhere.
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
	if err := os.WriteFile(plist, []byte(LaunchAgentPlist(exePath, cfgPath, logPath)), 0o644); err != nil {
		return nil, fmt.Errorf("install: write LaunchAgent: %w", err)
	}

	// Reload the agent (unload first so re-installs pick up the new binary).
	_, _ = runLaunchctl("unload", plist)
	if out, err := runLaunchctl("load", "-w", plist); err != nil {
		return nil, fmt.Errorf("install: launchctl load: %w\n%s", err, out)
	}

	res := &Result{Exe: exePath, Config: cfgPath, DataDir: dataDir, Started: true}
	res.Notes = append(res.Notes,
		"已注册 LaunchAgent（登录自启 + 崩溃自动拉起）：launchctl list | grep mibee",
		"日志："+logPath+"（tail -f 跟踪）",
		"管理界面：http://127.0.0.1:9090（局域网用本机 IP 访问）")
	return res, nil
}

// Uninstall unloads the LaunchAgent and removes program files. Data is kept
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

func setupConsole() {}
