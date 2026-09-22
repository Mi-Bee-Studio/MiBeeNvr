//go:build windows

package install

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

const (
	installDirName = "MiBeeNVR" // under %LOCALAPPDATA%\Programs
	dataDirName    = "MiBeeNVR" // under %LOCALAPPDATA%
	appName        = "MiBee NVR"
	runValueName   = "MiBeeNVR"
	uninstallKey   = `Software\Microsoft\Windows\CurrentVersion\Uninstall\MiBeeNVR`
	runKey         = `Software\Microsoft\Windows\CurrentVersion\Run`
	publisher      = "MiBee Studio"
	homepage       = "https://github.com/Mi-Bee-Studio/MiBeeNvr"
)

// setupConsole switches the console output codepage to UTF-8 so the Chinese
// summary survives cmd.exe's legacy codepage (the ARP-uninstall console).
func setupConsole() {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	_, _, _ = kernel32.NewProc("SetConsoleOutputCP").Call(65001)
}

// HideOwnConsole hides the console window the launcher (Start Menu shortcut,
// Run-key autostart, Explorer double-click) allocated for the server — the
// tray is the UI, a lingering cmd window reads as a stuck program (field
// report 2026-09-19). Only hides a console we OWN: when another process
// shares it (the user's terminal running `mibee-nvr …`), it stays visible.
func HideOwnConsole() {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	hwnd, _, _ := kernel32.NewProc("GetConsoleWindow").Call()
	if hwnd == 0 {
		return
	}
	var procs [8]uintptr
	n, _, _ := kernel32.NewProc("GetConsoleProcessList").Call(
		uintptr(unsafe.Pointer(&procs[0])), uintptr(len(procs)))
	if n > 1 {
		return // shared console — not ours to hide
	}
	user32 := syscall.NewLazyDLL("user32.dll")
	_, _, _ = user32.NewProc("ShowWindow").Call(hwnd, 0) // SW_HIDE
}

// OpenBrowser opens url in the default browser (ShellExecute — the same
// call the tray's 打开 Web 界面 uses).
func OpenBrowser(url string) {
	_ = windowsShellOpen(url)
}

func windowsShellOpen(url string) error {
	u, err := syscall.UTF16PtrFromString(url)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, nil, u, nil, nil, 5 /*SW_SHOW*/)
}

// NotifyDialog is darwin-only; windows reports through the console.
func NotifyDialog(text string) {}

func localAppData() (string, error) {
	d := os.Getenv("LOCALAPPDATA")
	if d == "" {
		return "", fmt.Errorf("install: LOCALAPPDATA not set")
	}
	return d, nil
}

// Paths returns the per-user install locations.
func Paths() (exeDir, dataDir, configPath string, err error) {
	lad, err := localAppData()
	if err != nil {
		return "", "", "", err
	}
	exeDir = filepath.Join(lad, "Programs", installDirName)
	dataDir = filepath.Join(lad, dataDirName)
	return exeDir, dataDir, filepath.Join(dataDir, "mibee-nvr.yaml"), nil
}

// InstalledExePath is the installed binary location ("" when unresolvable).
func InstalledExePath() string {
	exeDir, _, _, err := Paths()
	if err != nil {
		return ""
	}
	return filepath.Join(exeDir, "mibee-nvr.exe")
}

func startMenuShortcut() (string, error) {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return "", fmt.Errorf("install: APPDATA not set")
	}
	return filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "MiBee NVR.lnk"), nil
}

// Install copies the running binary into the per-user program dir and wires
// the OS integration: Add/Remove Programs entry, Start Menu shortcut, login
// autostart (HKCU Run). Per-user only — no elevation, ever.
func Install(opts Options) (*Result, error) {
	exeDir, dataDir, cfgPath, err := Paths()
	if err != nil {
		return nil, err
	}
	selfExe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("install: locate running binary: %w", err)
	}
	selfExe, _ = filepath.EvalSymlinks(selfExe)
	exePath := filepath.Join(exeDir, "mibee-nvr.exe")

	for _, d := range []string{exeDir, dataDir, filepath.Join(dataDir, "data")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("install: mkdir %s: %w", d, err)
		}
	}

	selfIsTarget := strings.EqualFold(filepath.Clean(exePath), filepath.Clean(selfExe))
	if !selfIsTarget {
		data, err := os.ReadFile(selfExe)
		if err != nil {
			return nil, fmt.Errorf("install: read binary: %w", err)
		}
		if err := writeExeOverRunning(exePath, data); err != nil {
			return nil, fmt.Errorf("install: 写入 %s 失败: %w", exePath, err)
		}
	}

	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		if err := os.WriteFile(cfgPath, []byte(StarterConfig(dataDir)), 0o600); err != nil {
			return nil, fmt.Errorf("install: write starter config: %w", err)
		}
	}

	if err := writeUninstallEntry(exePath, opts.Version); err != nil {
		return nil, fmt.Errorf("install: 注册卸载信息: %w", err)
	}
	if err := writeRunKey(exePath, cfgPath); err != nil {
		return nil, fmt.Errorf("install: 注册开机自启: %w", err)
	}

	lnk, err := startMenuShortcut()
	if err != nil {
		return nil, err
	}
	if err := createShortcut(lnk, exePath, `-config `+quote(cfgPath), dataDir,
		"MiBee NVR（服务器 + 托盘，Web 管理界面见托盘菜单）", exePath+",0"); err != nil {
		return nil, fmt.Errorf("install: 创建开始菜单快捷方式: %w", err)
	}

	res := &Result{Exe: exePath, Config: cfgPath, DataDir: dataDir}
	res.Notes = append(res.Notes,
		"开始菜单 → 「MiBee NVR」启动；启动后任务栏托盘出现图标（右键：打开 Web 界面 / 修改密码 / 监听地址 / 退出）",
		"默认仅本机可访问（http://127.0.0.1:9090）；托盘「监听地址…」可改为 0.0.0.0:9090 开放局域网",
		"已注册开机自启（仅当前用户）；控制面板「应用」/ 设置→应用 中可卸载")

	// Best-effort autostart: only when nothing is already listening on the
	// starter port (a running instance would collide).
	if !portInUse("127.0.0.1:9090") {
		cmd := exec.Command(exePath, "-config", cfgPath)
		cmd.Dir = dataDir
		// GUI-subsystem guard: the server child must NOT attach to this
		// installer's console (see console_windows.go) — the console would
		// then live exactly as long as the server, and closing it would
		// kill the server.
		cmd.Env = append(os.Environ(), desktopSpawnEnv+"=1")
		if err := cmd.Start(); err != nil {
			res.Notes = append(res.Notes, "自动启动失败（可从开始菜单手动启动）: "+err.Error())
		} else {
			res.Started = waitForHTTP(healthURLForConfig(cfgPath), 10*time.Second)
			if !res.Started {
				res.Notes = append(res.Notes, "⚠️ 服务未能在 10 秒内就绪——排查日志："+filepath.Join(dataDir, "nvr.log"))
			}
		}
	} else {
		// Already running (e.g. upgrade-in-place): Started tells the finish
		// dialog the truth — the service answers, just not from this run.
		res.Started = waitForHTTP(healthURLForConfig(cfgPath), 5*time.Second)
		res.Notes = append(res.Notes, "检测到 127.0.0.1:9090 已有服务在监听，跳过自动启动")
	}
	return res, nil
}

func quote(p string) string { return `"` + p + `"` }

// RefreshMenuBarHelper is darwin-only — windows manages the tray in-process.
func RefreshMenuBarHelper() error { return nil }

// UnloadDesktopAgents is darwin-only — windows has no supervisor to detach
// from; a tray quit is a plain process exit and stays down.
func UnloadDesktopAgents() {}

// DetachForUninstall is darwin-only process-group plumbing.
func DetachForUninstall() {}

func portInUse(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// configListenInUse reports whether a service instance is answering on the
// configured listen address (wildcards probed on loopback).
func configListenInUse(cfgPath string) bool {
	return portInUse(loopbackListenAddr(cfgPath))
}

// loopbackListenAddr resolves the configured listen address into the
// loopback form an on-machine client (installer/uninstaller) should dial.
func loopbackListenAddr(cfgPath string) string {
	addr := config.DefaultListenAddr
	if cfg, err := config.Load(cfgPath); err == nil && cfg.Server.Listen != "" {
		addr = cfg.Server.Listen
	}
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return "127.0.0.1:9090"
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// exeLocked reports whether some process holds the installed exe open for
// write (a running instance or a paused installer console).
func exeLocked(exePath string) bool {
	f, err := os.OpenFile(exePath, os.O_RDWR, 0)
	if err != nil {
		return true
	}
	_ = f.Close()
	return false
}

// stopRunningInstance gracefully stops a serving NVR via the loopback-local
// shutdown endpoint (POST /api/system/shutdown — the same trust model the
// tray uses) and waits for the port to free. Returns true when nothing is
// listening anymore (including when nothing was in the first place).
func stopRunningInstance(cfgPath string) bool {
	addr := loopbackListenAddr(cfgPath)
	if !portInUse(addr) {
		return true
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Post("http://"+addr+"/api/system/shutdown", "application/json", strings.NewReader("{}"))
	if err == nil {
		_ = resp.Body.Close()
	}
	// Graceful shutdown drains recorders etc. — give it a generous window.
	for range 40 {
		if !portInUse(addr) {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return !portInUse(addr)
}

func writeUninstallEntry(exePath, version string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, uninstallKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	size := int64(0)
	if fi, err := os.Stat(exePath); err == nil {
		size = (fi.Size() + 1023) / 1024
	}
	vals := []struct {
		name  string
		value string
	}{
		{"DisplayName", appName},
		{"DisplayVersion", version},
		{"Publisher", publisher},
		{"URLInfoAbout", homepage},
		{"InstallLocation", filepath.Dir(exePath)},
		{"DisplayIcon", exePath + ",0"},
		{"UninstallString", quote(exePath) + " uninstall"},
	}
	for _, v := range vals {
		if err := k.SetStringValue(v.name, v.value); err != nil {
			return err
		}
	}
	for _, v := range []struct {
		name  string
		value uint32
	}{
		{"EstimatedSize", uint32(size)},
		{"NoModify", 1},
		{"NoRepair", 1},
	} {
		if err := k.SetDWordValue(v.name, v.value); err != nil {
			return err
		}
	}
	return nil
}

func writeRunKey(exePath, cfgPath string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(runValueName, quote(exePath)+" -config "+quote(cfgPath))
}

// writeExeOverRunning installs the new binary even while a running instance
// locks the target: Windows refuses to open a running exe for writing, but
// DOES allow renaming it — the classic rename-aside upgrade. The running
// instance keeps executing from the renamed .old file; the next start (or
// the tray 退出 + autostart cycle) picks up the new binary. Stale .old
// files from earlier upgrades are cleaned on entry; the current one lives
// until the next install/uninstall.
func writeExeOverRunning(exePath string, data []byte) error {
	_ = os.Remove(exePath + ".old")
	if _, err := os.Stat(exePath); err == nil {
		if err := os.WriteFile(exePath, data, 0o755); err == nil {
			return nil
		}
		if err := os.Rename(exePath, exePath+".old"); err != nil {
			return fmt.Errorf("旧程序被占用且无法移开（%w）——若 MiBee NVR 正在运行，请先从托盘菜单退出后重试", err)
		}
	}
	return os.WriteFile(exePath, data, 0o755)
}

// Uninstall removes the OS integration and program files. Data (config +
// recordings + DB) is kept unless purge is true.
func Uninstall(purge bool) (*Result, error) {
	exeDir, dataDir, cfgPath, err := Paths()
	if err != nil {
		return nil, err
	}
	exePath := filepath.Join(exeDir, "mibee-nvr.exe")
	res := &Result{Exe: exePath, Config: cfgPath, DataDir: dataDir}

	selfExe, _ := os.Executable()
	selfExe, _ = filepath.EvalSymlinks(selfExe)
	selfIsTarget := selfExe != "" && strings.EqualFold(filepath.Clean(exePath), filepath.Clean(selfExe))

	if _, statErr := os.Stat(exePath); statErr == nil {
		// A running instance (or a paused installer) locks the exe and would
		// silently sink the delayed self-delete. Stop the server OURSELVES via
		// the loopback shutdown endpoint — the field-reported alternative
		// ("please quit from the tray first") was invisible in the flash
		// console the ARP path gets, and raced the logon autostart's bind.
		if !selfIsTarget {
			if exeLocked(exePath) {
				if !stopRunningInstance(cfgPath) || exeLocked(exePath) {
					return nil, fmt.Errorf("install: MiBee NVR 正在运行且无法自动停止——请从托盘图标右键退出（或结束 mibee-nvr 进程）后重试卸载")
				}
			}
		} else if configListenInUse(cfgPath) {
			// ARP path: our own process always locks the file, so probe the
			// service port for the running instance instead.
			if !stopRunningInstance(cfgPath) {
				return nil, fmt.Errorf("install: MiBee NVR 正在运行且无法自动停止——请从托盘图标右键退出（或结束 mibee-nvr 进程）后重试卸载")
			}
		}
	}

	_ = registry.DeleteKey(registry.CURRENT_USER, uninstallKey)
	if k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE); err == nil {
		_ = k.DeleteValue(runValueName)
		_ = k.Close()
	}
	if lnk, err := startMenuShortcut(); err == nil {
		_ = os.Remove(lnk)
	}

	if selfIsTarget {
		// The running uninstaller cannot delete its own exe. Hand the job to
		// a small detached .cmd file — passing a quoted script through
		// `cmd /c <argv>` mangles the quotes (Go escapes them MSVCRT-style,
		// cmd.exe does not understand that), while a script FILE path is a
		// single simply-quoted argument. The script RETRIES the deletes for
		// up to ~90s: the uninstaller console may hold the lock for its
		// summary pause (bounded, but the belt-and-braces window absorbs any
		// other slow releaser too), and a one-shot del used to silently sink
		// the whole removal (field report 2026-09-19).
		script := fmt.Sprintf("@echo off\r\n"+
			"set /a n=0\r\n"+
			":retry\r\n"+
			"del /f /q %s\r\ndel /f /q %s\r\n"+
			"if not exist %s if not exist %s goto done\r\n"+
			"set /a n+=1\r\nif %%n%% geq 45 goto done\r\n"+
			"timeout /t 2 /nobreak >nul 2>&1\r\ngoto retry\r\n"+
			":done\r\n"+
			"del /f /q \"%%~f0\" & rmdir \"%%~dp0\"\r\n",
			quote(exePath), quote(exePath+".old"), quote(exePath), quote(exePath+".old"))
		scriptPath := filepath.Join(exeDir, "uninstall-mibee-nvr.cmd")
		if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
			return nil, fmt.Errorf("install: 写自删除脚本失败: %w", err)
		}
		cmd := exec.Command("cmd.exe", "/c", scriptPath)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows_CREATE_NEW_PROCESS_GROUP}
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("install: 调度自删除失败: %w", err)
		}
	} else {
		_ = os.Remove(exePath)
		_ = os.Remove(exePath + ".old") // rename-aside leftovers from upgrades
		_ = os.Remove(exeDir)
	}

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

// windows_CREATE_NEW_PROCESS_GROUP detaches the delayed-delete shell so the
// uninstaller can exit immediately.
const windows_CREATE_NEW_PROCESS_GROUP = 0x00000200

// ---- IShellLinkW shortcut creation (pure syscall, no dependencies) ----

var (
	ole32             = syscall.NewLazyDLL("ole32.dll")
	pCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	pCoUninitialize   = ole32.NewProc("CoUninitialize")
	pCoCreateInstance = ole32.NewProc("CoCreateInstance")

	clsidShellLink  = windowsGUID{Data1: 0x00021401, Data2: 0x0000, Data3: 0x0000, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIShellLinkW  = windowsGUID{Data1: 0x000214F9, Data2: 0x0000, Data3: 0x0000, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIPersistFile = windowsGUID{Data1: 0x0000010B, Data2: 0x0000, Data3: 0x0000, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
)

type windowsGUID = syscall.GUID

const (
	clsctxInprocServer  = 1
	coinitApartment     = 2
	coinitChangedModeHr = 0x80010106 // S_FALSE/RPC_E_CHANGED_MODE → skip CoUninitialize
	hresultOK           = 0
	vtRelease           = 2
	vtSetDescription    = 7
	vtSetWorkingDir     = 9
	vtSetArguments      = 11
	vtSetIconLocation   = 17
	vtSetPath           = 20
	vtQI                = 0
	vtPersistSave       = 6
)

// comVtCall invokes a COM interface method by vtable index. obj is a live
// interface pointer held as uintptr (the standard no-cgo COM pattern); the
// unsafeptr conversions below are therefore false positives.
func comVtCall(obj uintptr, idx int, args ...uintptr) error {
	vtbl := *(*uintptr)(unsafe.Pointer(obj))                                         //nolint:govet // live COM interface pointer
	fn := *(*uintptr)(unsafe.Pointer(vtbl + uintptr(idx)*unsafe.Sizeof(uintptr(0)))) //nolint:govet // vtable slot
	r1, _, _ := syscall.SyscallN(fn, append([]uintptr{obj}, args...)...)
	if r1 != hresultOK {
		return fmt.Errorf("com: hr=0x%08x", r1)
	}
	return nil
}

func utf16p(s string) uintptr {
	p, _ := syscall.UTF16PtrFromString(s)
	return uintptr(unsafe.Pointer(p))
}

// createShortcut writes a .lnk via the shell's IShellLinkW/IPersistFile.
func createShortcut(lnk, target, args, workdir, desc, icon string) error {
	initR, _, _ := pCoInitializeEx.Call(0, coinitApartment)
	needUninit := initR == hresultOK
	defer func() {
		if needUninit {
			_, _, _ = pCoUninitialize.Call()
		}
	}()

	var obj uintptr
	r, _, _ := pCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidShellLink)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIShellLinkW)), uintptr(unsafe.Pointer(&obj)))
	if r != hresultOK {
		return fmt.Errorf("CoCreateInstance: hr=0x%08x", r)
	}
	defer func() { _ = comVtCall(obj, vtRelease) }()

	for _, c := range []struct {
		idx   int
		arg0  uintptr
		extra []uintptr
	}{
		{vtSetPath, utf16p(target), nil},
		{vtSetArguments, utf16p(args), nil},
		{vtSetWorkingDir, utf16p(workdir), nil},
		{vtSetDescription, utf16p(desc), nil},
		{vtSetIconLocation, utf16p(icon), []uintptr{0}},
	} {
		if err := comVtCall(obj, c.idx, append([]uintptr{c.arg0}, c.extra...)...); err != nil {
			return err
		}
	}

	var pf uintptr
	if err := comVtCall(obj, vtQI, uintptr(unsafe.Pointer(&iidIPersistFile)), uintptr(unsafe.Pointer(&pf))); err != nil {
		return err
	}
	defer func() { _ = comVtCall(pf, vtRelease) }()
	return comVtCall(pf, vtPersistSave, utf16p(lnk), 1)
}

// ReadShortcutPath loads a .lnk and returns its target path (verification
// helper, also used by tests).
func ReadShortcutPath(lnk string) (string, error) {
	initR, _, _ := pCoInitializeEx.Call(0, coinitApartment)
	needUninit := initR == hresultOK
	defer func() {
		if needUninit {
			_, _, _ = pCoUninitialize.Call()
		}
	}()

	var obj uintptr
	r, _, _ := pCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidShellLink)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIShellLinkW)), uintptr(unsafe.Pointer(&obj)))
	if r != hresultOK {
		return "", fmt.Errorf("CoCreateInstance: hr=0x%08x", r)
	}
	defer func() { _ = comVtCall(obj, vtRelease) }()

	var pf uintptr
	if err := comVtCall(obj, vtQI, uintptr(unsafe.Pointer(&iidIPersistFile)), uintptr(unsafe.Pointer(&pf))); err != nil {
		return "", err
	}
	defer func() { _ = comVtCall(pf, vtRelease) }()
	if err := comVtCall(pf, 5 /*IPersistFile.Load*/, utf16p(lnk), 0 /*STGM_READ*/); err != nil {
		return "", err
	}

	buf := make([]uint16, 1024)
	if err := comVtCall(obj, 3, /*IShellLinkW.GetPath*/
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0); err != nil {
		return "", err
	}
	return utf16ToString(buf), nil
}

func utf16ToString(u []uint16) string {
	for i, v := range u {
		if v == 0 {
			return string(utf16.Decode(u[:i]))
		}
	}
	return string(utf16.Decode(u))
}
