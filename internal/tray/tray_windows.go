//go:build windows

package tray

import (
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

//go:embed icon.ico
var iconICO []byte

// Struct layouts below are the x64 Win32 ones (pointer/handle = 8 bytes,
// natural alignment). Only windows/amd64 and windows/arm64 are supported —
// a 386 build would need different padding.

const (
	className    = "MiBeeNVRTrayWnd"
	trayIconID   = 1
	wmTray       = 0x8000 + 1 // WM_APP+1, tray callback message
	cmdOpen      = 100
	cmdQuit      = 101
	iconTmpName  = "mibee-nvr-tray.ico"
	tipMaxRunes  = 127 // szTip is [128]uint16 incl. NUL
	menuTitle    = "MiBee NVR"
	menuOpen     = "打开 Web 界面"
	menuQuit     = "退出"
	trayQuitMsg  = "tray quit requested, shutting down"
	niTipMaxWLen = 128
)

const (
	wmNull          = 0x0000
	wmDestroy       = 0x0002
	wmClose         = 0x0010
	wmCommand       = 0x0111
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
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	pRegisterClassExW    = user32.NewProc("RegisterClassExW")
	pCreateWindowExW     = user32.NewProc("CreateWindowExW")
	pDefWindowProcW      = user32.NewProc("DefWindowProcW")
	pGetMessageW         = user32.NewProc("GetMessageW")
	pDispatchMessageW    = user32.NewProc("DispatchMessageW")
	pPostQuitMessage     = user32.NewProc("PostQuitMessage")
	pDestroyWindow       = user32.NewProc("DestroyWindow")
	pPostMessageW        = user32.NewProc("PostMessageW")
	pLoadImageW          = user32.NewProc("LoadImageW")
	pDestroyIcon         = user32.NewProc("DestroyIcon")
	pCreatePopupMenu     = user32.NewProc("CreatePopupMenu")
	pDestroyMenu         = user32.NewProc("DestroyMenu")
	pAppendMenuW         = user32.NewProc("AppendMenuW")
	pTrackPopupMenu      = user32.NewProc("TrackPopupMenu")
	pSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	pGetCursorPos        = user32.NewProc("GetCursorPos")
	pShellNotifyIconW    = shell32.NewProc("Shell_NotifyIconW")
	pGetModuleHandleW    = kernel32.NewProc("GetModuleHandleW")
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

// Process-wide tray state, owned by the tray goroutine after Start returns.
var (
	trayURL  string
	quitCh   chan struct{}
	quitOnce sync.Once
)

var wndProcCb = syscall.NewCallback(
	func(hwnd uintptr, msgc uint32, wParam, lParam uintptr) uintptr {
		switch msgc {
		case wmTray:
			switch uint32(uint16(lParam)) { // LOWORD(lParam) = mouse message
			case wmRButtonUp:
				showMenu(windows.HWND(hwnd))
				return 0
			case wmLButtonDblClk:
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
	trayURL = opts.OpenURL

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

		slog.Info("tray icon created", "url", trayURL)
		var m msg
		for {
			r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if int32(r) <= 0 {
				break
			}
			call(pDispatchMessageW, uintptr(unsafe.Pointer(&m)))
		}
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

func showMenu(hwnd windows.HWND) {
	menu, _, _ := pCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer call(pDestroyMenu, menu)

	call(pAppendMenuW, menu, mfString|mfGrayed, 0, utf16ptr(menuTitle))
	if trayURL != "" {
		call(pAppendMenuW, menu, mfString, cmdOpen, utf16ptr(menuOpen))
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
	case cmdQuit:
		quitOnce.Do(func() { close(quitCh) })
	}
}

func openURL() {
	if trayURL == "" {
		return
	}
	u, err := windows.UTF16PtrFromString(trayURL)
	if err != nil {
		return
	}
	if err := windows.ShellExecute(0, nil, u, nil, nil, swShowNormal); err != nil {
		slog.Warn("tray: open web ui failed", "error", err)
	}
}
