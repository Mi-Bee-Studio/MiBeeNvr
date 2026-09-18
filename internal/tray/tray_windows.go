//go:build windows

package tray

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

//go:embed icon.ico
var iconICO []byte

// Struct layouts below are the x64 Win32 ones (pointer/handle = 8 bytes,
// natural alignment). Only windows/amd64 and windows/arm64 are supported —
// a 386 build would need different padding.

const (
	className          = "MiBeeNVRTrayWnd"
	dlgClassName       = "MiBeeNVRPasswdDlg"
	dlgListenClassName = "MiBeeNVRListenDlg"
	trayIconID         = 1
	wmTray             = 0x8000 + 1 // WM_APP+1, tray callback message
	cmdOpen            = 100
	cmdPassword        = 101
	cmdQuit            = 102
	cmdListen          = 103
	iconTmpName        = "mibee-nvr-tray.ico"
	tipMaxRunes        = 127 // szTip is [128]uint16 incl. NUL
	menuOpen           = "打开 Web 界面"
	menuPassword       = "修改密码…"
	menuListen         = "监听地址…"
	menuQuit           = "退出"
	niTipMaxWLen       = 128
)

const (
	wmNull          = 0x0000
	wmDestroy       = 0x0002
	wmClose         = 0x0010
	wmCommand       = 0x0111
	wmSetFont       = 0x0030
	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205

	nimAdd     = 0
	nimDelete  = 2
	nifMessage = 0x1
	nifIcon    = 0x2
	nifTip     = 0x4

	mfString    = 0x0
	mfGrayed    = 0x1
	mfSeparator = 0x800

	tpmRightAlign  = 0x0008
	tpmBottomAlign = 0x0020
	tpmReturnCmd   = 0x0100

	imageIcon      = 1
	lrLoadFromFile = 0x0010

	swShowNormal = 1

	wsOverlappedWindow = 0x00CF0000
	wsVisible          = 0x10000000
	wsChild            = 0x40000000
	wsTabstop          = 0x00010000
	wsThickFrame       = 0x00040000
	wsMaximizeBox      = 0x00010000

	esPassword      = 0x0020
	esAutoHscroll   = 0x0080
	bsDefPushButton = 0x0001

	idcEditNew    = 2002
	idcEditConf   = 2003
	idcEditListen = 2004
	idBtnOK       = 1 // IDOK — IsDialogMessage maps Enter to the default button
	idBtnCancel   = 2 // IDCANCEL — IsDialogMessage maps Esc to this

	mbOK              = 0x0
	mbIconError       = 0x10
	mbIconInformation = 0x40

	defaultGuiFont = 17
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")

	pRegisterClassExW    = user32.NewProc("RegisterClassExW")
	pCreateWindowExW     = user32.NewProc("CreateWindowExW")
	pDefWindowProcW      = user32.NewProc("DefWindowProcW")
	pGetMessageW         = user32.NewProc("GetMessageW")
	pDispatchMessageW    = user32.NewProc("DispatchMessageW")
	pTranslateMessage    = user32.NewProc("TranslateMessage")
	pIsDialogMessageW    = user32.NewProc("IsDialogMessageW")
	pPostQuitMessage     = user32.NewProc("PostQuitMessage")
	pDestroyWindow       = user32.NewProc("DestroyWindow")
	pPostMessageW        = user32.NewProc("PostMessageW")
	pSendMessageW        = user32.NewProc("SendMessageW")
	pLoadImageW          = user32.NewProc("LoadImageW")
	pDestroyIcon         = user32.NewProc("DestroyIcon")
	pCreatePopupMenu     = user32.NewProc("CreatePopupMenu")
	pDestroyMenu         = user32.NewProc("DestroyMenu")
	pAppendMenuW         = user32.NewProc("AppendMenuW")
	pTrackPopupMenu      = user32.NewProc("TrackPopupMenu")
	pSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	pGetCursorPos        = user32.NewProc("GetCursorPos")
	pMessageBoxW         = user32.NewProc("MessageBoxW")
	pGetWindowTextW      = user32.NewProc("GetWindowTextW")
	pIsWindow            = user32.NewProc("IsWindow")
	pSetFocus            = user32.NewProc("SetFocus")
	pShellNotifyIconW    = shell32.NewProc("Shell_NotifyIconW")
	pGetModuleHandleW    = kernel32.NewProc("GetModuleHandleW")
	pGetStockObject      = gdi32.NewProc("GetStockObject")
)

type point struct{ X, Y int32 }

type wndClassExW struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     windows.Handle
	HIcon         windows.Handle
	HCursor       windows.Handle
	HbrBackground windows.Handle
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       windows.Handle
}

type msg struct {
	HWnd    windows.HWND
	Message uint32
	_       uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
	_       uint32
}

type notifyIconDataW struct {
	CbSize          uint32
	_               uint32
	HWnd            windows.HWND
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	_               uint32
	Icon            windows.Handle
	Tip             [niTipMaxWLen]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	TimeoutVersion  uint32
	_               uint32
	Guid            windows.GUID
	BalloonIcon     windows.Handle
}

// Process-wide tray state. url/listen/version/changeListen are guarded by
// trayMu: written by Start (boot) and by SetAddress (after an in-process
// listen swap, from any goroutine), read on the tray thread.
var (
	trayMu         sync.Mutex
	trayURL        string
	trayListen     string
	trayVersion    string
	changeListenFn func(string) error
	quitCh         chan struct{}
	quitOnce       sync.Once

	// Dialog control handles (single dialog at a time, used only on the
	// tray thread).
	dlgEditNew, dlgEditConf, dlgEditListen uintptr
)

// trayState is a consistent snapshot of the mutable tray settings.
type trayState struct {
	url          string
	listen       string
	version      string
	changeListen func(string) error
}

func snapshot() trayState {
	trayMu.Lock()
	defer trayMu.Unlock()
	return trayState{url: trayURL, listen: trayListen, version: trayVersion, changeListen: changeListenFn}
}

// SetAddress retargets the tray after an in-process listen swap: the 打开
// Web 界面 entry, the loopback password endpoint base and the 监听地址 dialog
// prefill all move to the new address.
func SetAddress(listen string) {
	trayMu.Lock()
	defer trayMu.Unlock()
	trayListen = listen
	trayURL = ListenURL(listen)
}

var wndProcCb = syscall.NewCallback(
	func(hwnd uintptr, msgc uint32, wParam, lParam uintptr) uintptr {
		switch msgc {
		case wmTray:
			switch uint32(uint16(lParam)) { // LOWORD(lParam) = mouse message
			case wmRButtonUp:
				showMenu(windows.HWND(hwnd))
				return 0
			case wmLButtonUp, wmLButtonDblClk:
				openURL()
				return 0
			}
		case wmClose:
			call(pDestroyWindow, hwnd)
			return 0
		case wmDestroy:
			nid := notifyIconDataW{HWnd: windows.HWND(hwnd), ID: trayIconID}
			nid.CbSize = uint32(unsafe.Sizeof(nid))
			call(pShellNotifyIconW, nimDelete, uintptr(unsafe.Pointer(&nid)))
			call(pPostQuitMessage, 0)
			return 0
		}
		ret, _, _ := pDefWindowProcW.Call(hwnd, uintptr(msgc), wParam, lParam)
		return ret
	})

func startPlatform(opts Options) (stop func(), quit <-chan struct{}, err error) {
	quitCh = make(chan struct{})
	trayMu.Lock()
	trayURL = opts.OpenURL
	trayListen = opts.ListenAddr
	trayVersion = opts.Version
	changeListenFn = opts.OnChangeListen
	trayMu.Unlock()

	iconPath := filepath.Join(os.TempDir(), iconTmpName)
	if err := os.WriteFile(iconPath, iconICO, 0o644); err != nil {
		return nil, nil, fmt.Errorf("tray: write icon: %w", err)
	}

	type started struct {
		hwnd windows.HWND
		err  error
	}
	ready := make(chan started, 1)

	go func() {
		// A Win32 window is bound to the OS thread that creates it, and its
		// messages are queued to that thread — the goroutine must therefore
		// stay pinned for the whole lifetime of the message loop. Without
		// this the runtime may migrate us and every tray click silently
		// dies with an unpumped queue (the "icon shows but does nothing"
		// field report).
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		hinst, _, _ := pGetModuleHandleW.Call(0)
		cls, _ := windows.UTF16PtrFromString(className)
		wc := wndClassExW{LpfnWndProc: wndProcCb, LpszClassName: cls, HInstance: windows.Handle(hinst)}
		wc.CbSize = uint32(unsafe.Sizeof(wc))
		// Failure is tolerable: the class survives from an earlier Start in
		// the same process (error 1410 ERROR_CLASS_HAS_WINDOWS lineage).
		call(pRegisterClassExW, uintptr(unsafe.Pointer(&wc)))

		hwnd, _, cerr := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), 0,
			wsOverlappedWindow, 0, 0, 0, 0, 0, 0, hinst, 0)
		if hwnd == 0 {
			ready <- started{err: fmt.Errorf("tray: CreateWindowEx: %w", cerr)}
			return
		}

		ipath, _ := windows.UTF16PtrFromString(iconPath)
		icon, _, lerr := pLoadImageW.Call(0, uintptr(unsafe.Pointer(ipath)),
			imageIcon, 0, 0, lrLoadFromFile)
		if icon == 0 {
			call(pDestroyWindow, hwnd)
			ready <- started{err: fmt.Errorf("tray: LoadImage: %w", lerr)}
			return
		}

		nid := notifyIconDataW{
			HWnd:            windows.HWND(hwnd),
			ID:              trayIconID,
			Flags:           nifMessage | nifIcon | nifTip,
			CallbackMessage: wmTray,
			Icon:            windows.Handle(icon),
		}
		nid.CbSize = uint32(unsafe.Sizeof(nid))
		copy(nid.Tip[:], truncateUTF16(opts.Tooltip))
		if r, _, serr := pShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid))); r == 0 {
			call(pDestroyIcon, icon)
			call(pDestroyWindow, hwnd)
			ready <- started{err: fmt.Errorf("tray: Shell_NotifyIcon: %w", serr)}
			return
		}
		ready <- started{hwnd: windows.HWND(hwnd)}

		slog.Info("tray icon created", "url", snapshot().url)
		var m msg
		for {
			r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if int32(r) <= 0 {
				break
			}
			call(pDispatchMessageW, uintptr(unsafe.Pointer(&m)))
		}
		slog.Info("tray message loop exited")
		call(pDestroyIcon, icon)
		_ = os.Remove(iconPath)
	}()

	st := <-ready
	if st.err != nil {
		return nil, nil, st.err
	}

	var stopOnce sync.Once
	return func() {
		stopOnce.Do(func() { call(pPostMessageW, uintptr(st.hwnd), wmClose, 0, 0) })
	}, quitCh, nil
}

// truncateUTF16 clamps s to tipMaxRunes runes (NUL-terminated UTF-16).
func truncateUTF16(s string) []uint16 {
	u := []rune(s)
	if len(u) > tipMaxRunes {
		u = u[:tipMaxRunes]
	}
	return syscall.StringToUTF16(string(u))
}

// call runs a Win32 proc and drops its error return: lazy-proc errors only
// surface when the symbol is missing (impossible for user32/kernel32/shell32
// exports pinned since forever), and every site whose RESULT matters reads
// the first return value directly.
func call(p *windows.LazyProc, args ...uintptr) {
	_, _, _ = p.Call(args...)
}

func utf16ptr(s string) uintptr {
	p, _ := windows.UTF16PtrFromString(s)
	return uintptr(unsafe.Pointer(p))
}

func menuTitle(version string) string {
	if version != "" {
		return "MiBee NVR  " + version
	}
	return "MiBee NVR"
}

func showMenu(hwnd windows.HWND) {
	st := snapshot()

	menu, _, _ := pCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer call(pDestroyMenu, menu)

	call(pAppendMenuW, menu, mfString|mfGrayed, 0, utf16ptr(menuTitle(st.version)))
	if st.url != "" {
		if st.listen != "" {
			call(pAppendMenuW, menu, mfString|mfGrayed, 0, utf16ptr("监听："+st.listen))
		}
		call(pAppendMenuW, menu, mfString, cmdOpen, utf16ptr(menuOpen))
		call(pAppendMenuW, menu, mfString, cmdPassword, utf16ptr(menuPassword))
		if st.changeListen != nil {
			call(pAppendMenuW, menu, mfString, cmdListen, utf16ptr(menuListen))
		}
	}
	call(pAppendMenuW, menu, mfSeparator, 0, 0)
	call(pAppendMenuW, menu, mfString, cmdQuit, utf16ptr(menuQuit))

	// Foreground activation + WM_NULL so the menu dismisses when the user
	// clicks elsewhere (classic KB135788 recipe for tray menus).
	call(pSetForegroundWindow, uintptr(hwnd))
	var pt point
	call(pGetCursorPos, uintptr(unsafe.Pointer(&pt)))
	cmd, _, _ := pTrackPopupMenu.Call(menu, tpmRightAlign|tpmBottomAlign|tpmReturnCmd,
		uintptr(pt.X), uintptr(pt.Y), 0, uintptr(hwnd), 0)
	call(pPostMessageW, uintptr(hwnd), wmNull, 0, 0)

	switch cmd {
	case cmdOpen:
		openURL()
	case cmdPassword:
		changePasswordDialog()
	case cmdListen:
		listenDialog()
	case cmdQuit:
		quitOnce.Do(func() { close(quitCh) })
	}
}

func openURL() {
	url := snapshot().url
	if url == "" {
		return
	}
	u, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return
	}
	if err := windows.ShellExecute(0, nil, u, nil, nil, swShowNormal); err != nil {
		slog.Warn("tray: open web ui failed", "error", err)
	} else {
		slog.Info("tray: opened web ui", "url", url)
	}
}

// ---- 修改密码 native dialog (runs modally on the tray thread) ----

var dlgProcCb = syscall.NewCallback(
	func(hwnd uintptr, msgc uint32, wParam, lParam uintptr) uintptr {
		switch msgc {
		case wmCommand:
			if lParam == 0 { // menu/accelerator notifications carry no hwnd
				switch uint16(wParam & 0xFFFF) {
				case idBtnOK:
					submitPasswordChange(hwnd)
					return 0
				case idBtnCancel:
					call(pDestroyWindow, hwnd)
					return 0
				}
			}
		}
		ret, _, _ := pDefWindowProcW.Call(hwnd, uintptr(msgc), wParam, lParam)
		return ret
	})

func changePasswordDialog() {
	hinst, _, _ := pGetModuleHandleW.Call(0)
	cls, _ := windows.UTF16PtrFromString(dlgClassName)
	wc := wndClassExW{LpfnWndProc: dlgProcCb, LpszClassName: cls, HInstance: windows.Handle(hinst)}
	wc.CbSize = uint32(unsafe.Sizeof(wc))
	call(pRegisterClassExW, uintptr(unsafe.Pointer(&wc))) // re-register is tolerated

	font, _, _ := pGetStockObject.Call(defaultGuiFont)
	style := uintptr((wsOverlappedWindow &^ wsThickFrame &^ wsMaximizeBox) | wsVisible)
	title, _ := windows.UTF16PtrFromString("MiBee NVR — 修改密码")
	hwnd, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)),
		style, 0x80000000 /*CW_USEDEFAULT*/, 0x80000000, 400, 190, 0, 0, hinst, 0)
	if hwnd == 0 {
		slog.Warn("tray: create password dialog failed")
		return
	}

	addCtrl := func(class, text string, id uintptr, style uintptr, x, y, w, h int32) uintptr {
		t, _ := windows.UTF16PtrFromString(text)
		c, _ := windows.UTF16PtrFromString(class)
		ch, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(t)),
			wsChild|wsVisible|style, uintptr(x), uintptr(y), uintptr(w), uintptr(h),
			hwnd, id, hinst, 0)
		if ch != 0 {
			call(pSendMessageW, ch, wmSetFont, font, 1)
		}
		return ch
	}
	addCtrl("STATIC", "新密码", 0, 0, 12, 26, 76, 20)
	dlgEditNew = addCtrl("EDIT", "", idcEditNew, wsTabstop|esPassword|esAutoHscroll, 96, 24, 264, 24)
	addCtrl("STATIC", "确认新密码", 0, 0, 12, 66, 76, 20)
	dlgEditConf = addCtrl("EDIT", "", idcEditConf, wsTabstop|esPassword|esAutoHscroll, 96, 64, 264, 24)
	addCtrl("BUTTON", "确定", idBtnOK, bsDefPushButton|wsTabstop, 212, 104, 76, 28)
	addCtrl("BUTTON", "取消", idBtnCancel, wsTabstop, 296, 104, 76, 28)
	call(pSetFocus, dlgEditNew)

	// Modal pump on the tray thread: IsDialogMessage gives Tab navigation,
	// Enter→default button and Esc→cancel for free.
	var m msg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		ok, _, _ := pIsDialogMessageW.Call(hwnd, uintptr(unsafe.Pointer(&m)))
		if ok == 0 {
			call(pTranslateMessage, uintptr(unsafe.Pointer(&m)))
			call(pDispatchMessageW, uintptr(unsafe.Pointer(&m)))
		}
		if w, _, _ := pIsWindow.Call(hwnd); w == 0 {
			return
		}
	}
}

func editText(h uintptr) string {
	if h == 0 {
		return ""
	}
	buf := make([]uint16, 256)
	n, _, _ := pGetWindowTextW.Call(h, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return utf16ToString(buf[:n])
}

func utf16ToString(u []uint16) string {
	return syscall.UTF16ToString(u)
}

func dlgMsgBox(hwnd uintptr, text, caption string, icon uintptr) {
	call(pMessageBoxW, hwnd, utf16ptr(text), utf16ptr(caption), mbOK|icon)
}

func submitPasswordChange(hwnd uintptr) {
	newPw := editText(dlgEditNew)
	confPw := editText(dlgEditConf)

	if newPw == "" {
		dlgMsgBox(hwnd, "请填写新密码。", "修改密码", mbIconError)
		return
	}
	if len(newPw) < 8 {
		dlgMsgBox(hwnd, "新密码至少 8 个字符。", "修改密码", mbIconError)
		return
	}
	if newPw != confPw {
		dlgMsgBox(hwnd, "两次输入的新密码不一致。", "修改密码", mbIconError)
		return
	}
	if err := postPasswordChange(newPw); err != nil {
		slog.Warn("tray: password change failed", "error", err)
		dlgMsgBox(hwnd, "修改失败："+err.Error(), "修改密码", mbIconError)
		return
	}
	slog.Info("tray: password changed")
	dlgMsgBox(hwnd, "密码已修改（局域网登录请使用新密码）。", "修改密码", mbIconInformation)
	call(pDestroyWindow, hwnd)
}

// postPasswordChange calls POST /api/auth/password on the NVR's loopback
// listener. The endpoint is local-machine-only (IsBypassEligible), so no old
// password is needed — sitting at the console is the authorization.
func postPasswordChange(newPw string) error {
	base := snapshot().url
	if base == "" {
		return fmt.Errorf("Web 地址未知")
	}
	body, _ := json.Marshal(map[string]string{"new_password": newPw})
	req, err := http.NewRequest(http.MethodPost,
		strings.TrimRight(base, "/")+"/api/auth/password", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("无法连接 NVR（%w）", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusForbidden:
		return fmt.Errorf("仅限在 NVR 本机上修改")
	case http.StatusConflict:
		return fmt.Errorf("尚未完成初始设置——请先通过「打开 Web 界面」完成向导")
	default:
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return fmt.Errorf("HTTP %d：%s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
}

// ---- 监听地址 native dialog (runs modally on the tray thread) ----

var listenDlgProcCb = syscall.NewCallback(
	func(hwnd uintptr, msgc uint32, wParam, lParam uintptr) uintptr {
		switch msgc {
		case wmCommand:
			if lParam == 0 { // menu/accelerator notifications carry no hwnd
				switch uint16(wParam & 0xFFFF) {
				case idBtnOK:
					submitListenChange(hwnd)
					return 0
				case idBtnCancel:
					call(pDestroyWindow, hwnd)
					return 0
				}
			}
		}
		ret, _, _ := pDefWindowProcW.Call(hwnd, uintptr(msgc), wParam, lParam)
		return ret
	})

func listenDialog() {
	hinst, _, _ := pGetModuleHandleW.Call(0)
	cls, _ := windows.UTF16PtrFromString(dlgListenClassName)
	wc := wndClassExW{LpfnWndProc: listenDlgProcCb, LpszClassName: cls, HInstance: windows.Handle(hinst)}
	wc.CbSize = uint32(unsafe.Sizeof(wc))
	call(pRegisterClassExW, uintptr(unsafe.Pointer(&wc))) // re-register is tolerated

	font, _, _ := pGetStockObject.Call(defaultGuiFont)
	style := uintptr((wsOverlappedWindow &^ wsThickFrame &^ wsMaximizeBox) | wsVisible)
	title, _ := windows.UTF16PtrFromString("MiBee NVR — 监听地址")
	hwnd, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)),
		style, 0x80000000 /*CW_USEDEFAULT*/, 0x80000000, 400, 170, 0, 0, hinst, 0)
	if hwnd == 0 {
		slog.Warn("tray: create listen dialog failed")
		return
	}

	addCtrl := func(class, text string, id uintptr, style uintptr, x, y, w, h int32) uintptr {
		t, _ := windows.UTF16PtrFromString(text)
		c, _ := windows.UTF16PtrFromString(class)
		ch, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(t)),
			wsChild|wsVisible|style, uintptr(x), uintptr(y), uintptr(w), uintptr(h),
			hwnd, id, hinst, 0)
		if ch != 0 {
			call(pSendMessageW, ch, wmSetFont, font, 1)
		}
		return ch
	}
	addCtrl("STATIC", "监听地址", 0, 0, 12, 26, 76, 20)
	dlgEditListen = addCtrl("EDIT", snapshot().listen, idcEditListen, wsTabstop|esAutoHscroll, 96, 24, 264, 24)
	addCtrl("STATIC", "127.0.0.1 = 仅本机访问；0.0.0.0 = 开放局域网访问（需改端口则一并填写）", 0, 0, 12, 62, 360, 20)
	addCtrl("BUTTON", "确定", idBtnOK, bsDefPushButton|wsTabstop, 212, 96, 76, 28)
	addCtrl("BUTTON", "取消", idBtnCancel, wsTabstop, 296, 96, 76, 28)
	call(pSetFocus, dlgEditListen)

	// Modal pump on the tray thread (same recipe as the password dialog).
	var m msg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		ok, _, _ := pIsDialogMessageW.Call(hwnd, uintptr(unsafe.Pointer(&m)))
		if ok == 0 {
			call(pTranslateMessage, uintptr(unsafe.Pointer(&m)))
			call(pDispatchMessageW, uintptr(unsafe.Pointer(&m)))
		}
		if w, _, _ := pIsWindow.Call(hwnd); w == 0 {
			return
		}
	}
}

func submitListenChange(hwnd uintptr) {
	fn := snapshot().changeListen
	if fn == nil {
		call(pDestroyWindow, hwnd)
		return
	}
	addr := strings.TrimSpace(editText(dlgEditListen))
	if err := ValidateListenAddr(addr); err != nil {
		dlgMsgBox(hwnd, err.Error(), "监听地址", mbIconError)
		return
	}
	// Synchronous on purpose: fn binds the new address before touching the
	// old listener, so an error here leaves everything serving as-is. The
	// drain typically completes in well under a second.
	if err := fn(addr); err != nil {
		slog.Warn("tray: listen change failed", "error", err)
		dlgMsgBox(hwnd, "切换失败："+err.Error(), "监听地址", mbIconError)
		return
	}
	slog.Info("tray: listen address changed", "addr", addr)
	dlgMsgBox(hwnd, "监听地址已切换为 "+addr+"。\n旧连接已断开，请用新地址访问 Web 界面。", "监听地址", mbIconInformation)
	call(pDestroyWindow, hwnd)
}
