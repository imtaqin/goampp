//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// tray.go — system tray icon, minimize-to-tray, and auto-start-on-boot
// wiring. A single shared icon handle is used for the title bar, the
// taskbar entry, and the notification-area icon.

const (
	// UID and callback message for the tray icon. The UID is an
	// arbitrary number unique per (hwnd, UID) pair; we only register
	// one icon so 1 is fine. The callback message is a WM_APP-range
	// custom message that we handle in the main window proc to know
	// when the user clicked/right-clicked/hovered the tray icon.
	trayIconUID    uint32 = 1
	wmTrayCallback co.WM  = 0x8001 // WM_APP + 1

	// Tray menu item IDs. Kept well above windigo's auto-generated
	// control IDs (which start in the low thousands) so there's no
	// risk of collision with the in-app buttons and list views.
	trayMenuIdShow    uint16 = 40001
	trayMenuIdStart   uint16 = 40002
	trayMenuIdStop    uint16 = 40003
	trayMenuIdAutorun uint16 = 40004
	trayMenuIdQuit    uint16 = 40005

	// Registry key + value name for the Windows "run on login" entry.
	// HKCU — per-user, so we don't need admin rights.
	autoStartKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
	autoStartValue   = "GoAMPP"

	// Command-line flag used when Windows auto-starts us — we pass it
	// through the Run-key value so the app boots hidden into the tray
	// instead of popping up a window at every login.
	flagStartTray = "--tray"
)

// Syscalls windigo doesn't expose. user32 is already declared in
// paint.go, so we only need to grab the extra procs.
var (
	procAppendMenuW = user32.NewProc("AppendMenuW")
)

// trayState is the runtime tracking for the tray icon installation.
// We only ever install one, so a package-level var is fine.
var trayState struct {
	hIcon     win.HICON
	installed bool
}

// installTray loads logo.ico and registers the tray icon. Safe to call
// from the main WM_CREATE handler — hwnd must already exist.
func installTray(wnd *ui.Main) error {
	iconPath := filepath.Join(app.baseDir, "logo.ico")
	if _, err := os.Stat(iconPath); err != nil {
		return fmt.Errorf("logo.ico not found next to exe: %w", err)
	}

	// Load the icon from the external file. LR_DEFAULTSIZE picks an
	// appropriate size for the caller's context; Windows picks 32x32
	// for notification-area icons on high-DPI.
	hGdi, err := win.HINSTANCE(0).LoadImage(
		win.ResIdStr(iconPath),
		co.IMAGE_ICON,
		0, 0,
		co.LR_LOADFROMFILE|co.LR_DEFAULTSIZE|co.LR_SHARED,
	)
	if err != nil {
		return fmt.Errorf("LoadImage: %w", err)
	}
	trayState.hIcon = win.HICON(hGdi)

	// Also set the window icon so the title bar + taskbar entry +
	// alt-tab all show the GoAMPP gopher instead of the default
	// generic Win32 icon.
	h := wnd.Hwnd()
	h.SendMessage(co.WM_SETICON, win.WPARAM(co.ICON_SZ_SMALL), win.LPARAM(trayState.hIcon))
	h.SendMessage(co.WM_SETICON, win.WPARAM(co.ICON_SZ_BIG), win.LPARAM(trayState.hIcon))

	// Register the tray icon.
	var nid win.NOTIFYICONDATA
	nid.SetCbSize()
	nid.HWnd = h
	nid.UID = trayIconUID
	nid.UFlags = co.NIF_ICON | co.NIF_MESSAGE | co.NIF_TIP
	nid.UCallbackMessage = wmTrayCallback
	nid.HIcon = trayState.hIcon
	nid.SetSzTip("GoAMPP — Local Web Stack")

	if err := win.Shell_NotifyIcon(co.NIM_ADD, &nid); err != nil {
		return fmt.Errorf("Shell_NotifyIcon: %w", err)
	}
	trayState.installed = true
	return nil
}

// removeTray deregisters the tray icon. Called from the Quit handler
// so the icon disappears immediately instead of waiting for the shell
// to notice the window is gone (which can take minutes).
func removeTray(wnd *ui.Main) {
	if !trayState.installed {
		return
	}
	var nid win.NOTIFYICONDATA
	nid.SetCbSize()
	nid.HWnd = wnd.Hwnd()
	nid.UID = trayIconUID
	_ = win.Shell_NotifyIcon(co.NIM_DELETE, &nid)
	trayState.installed = false
}

// handleTrayCallback is wired to wnd.On().Wm(wmTrayCallback, ...). The
// low word of LPARAM is the actual mouse event — we route it to either
// "show the window" (left click) or "open the popup menu" (right click).
func handleTrayCallback(wnd *ui.Main, p ui.Wm) {
	mouseEvent := co.WM(uint32(p.LParam) & 0xffff)
	switch mouseEvent {
	case co.WM_LBUTTONDBLCLK, co.WM_LBUTTONUP:
		showMainWindow(wnd)
	case co.WM_RBUTTONUP, co.WM_CONTEXTMENU:
		showTrayMenu(wnd)
	}
}

// showMainWindow un-hides + un-minimises the main window and brings it
// to the foreground. Called from the tray left-click and from the
// "Show GoAMPP" menu item.
func showMainWindow(wnd *ui.Main) {
	h := wnd.Hwnd()
	h.ShowWindow(co.SW_SHOW)
	h.ShowWindow(co.SW_RESTORE)
	h.SetForegroundWindow()
}

// hideMainWindow hides the main window without destroying it. The tray
// icon stays visible; clicking it re-shows the window.
func hideMainWindow(wnd *ui.Main) {
	wnd.Hwnd().ShowWindow(co.SW_HIDE)
}

// showTrayMenu builds a context menu and displays it at the cursor
// position via TrackPopupMenu. The menu lives only for the duration of
// this call — TrackPopupMenu blocks until the user picks an item or
// dismisses it, then we destroy the menu.
func showTrayMenu(wnd *ui.Main) {
	hMenu, err := win.CreatePopupMenu()
	if err != nil {
		return
	}
	defer hMenu.DestroyMenu()

	appendMenuItem(hMenu, co.MF_STRING, uintptr(trayMenuIdShow), "&Show GoAMPP")
	appendMenuItem(hMenu, co.MF_SEPARATOR, 0, "")
	appendMenuItem(hMenu, co.MF_STRING, uintptr(trayMenuIdStart), "Start &Stack")
	appendMenuItem(hMenu, co.MF_STRING, uintptr(trayMenuIdStop), "Stop &All")
	appendMenuItem(hMenu, co.MF_SEPARATOR, 0, "")

	flags := co.MF_STRING
	if isAutoStartEnabled() {
		flags |= co.MF_CHECKED
	}
	appendMenuItem(hMenu, flags, uintptr(trayMenuIdAutorun), "&Auto-start with Windows")

	appendMenuItem(hMenu, co.MF_SEPARATOR, 0, "")
	appendMenuItem(hMenu, co.MF_STRING, uintptr(trayMenuIdQuit), "&Quit")

	// TrackPopupMenu requires the calling window to be in the
	// foreground, otherwise the menu dismisses as soon as the user
	// clicks outside it. The PostMessage(WM_NULL) afterwards is a
	// well-known Win32 workaround that forces the menu's IMM to
	// release its grab — without it the menu reappears on the
	// second right click.
	pt, _ := win.GetCursorPos()
	wnd.Hwnd().SetForegroundWindow()
	_, _ = hMenu.TrackPopupMenu(co.TPM_LEFTBUTTON|co.TPM_RIGHTBUTTON,
		int(pt.X), int(pt.Y), wnd.Hwnd())
	_ = wnd.Hwnd().PostMessage(co.WM_NULL, 0, 0)
}

// appendMenuItem is a thin wrapper around the AppendMenuW syscall. We
// dial user32 directly because windigo only exposes the more verbose
// InsertMenuItem API that needs a MENUITEMINFO struct.
func appendMenuItem(hMenu win.HMENU, flags co.MF, id uintptr, text string) {
	var pText uintptr
	if text != "" {
		t, _ := windows.UTF16PtrFromString(text)
		pText = uintptr(unsafe.Pointer(t))
	}
	procAppendMenuW.Call(
		uintptr(hMenu),
		uintptr(flags),
		id,
		pText,
	)
}

// handleTrayMenuCommand dispatches a menu ID that came back through a
// WM_COMMAND. Called from the main window proc after the user picks
// an item from the tray context menu.
func handleTrayMenuCommand(wnd *ui.Main, cmdId uint16) bool {
	switch cmdId {
	case trayMenuIdShow:
		showMainWindow(wnd)
	case trayMenuIdStart:
		go startEssentialStack()
	case trayMenuIdStop:
		go stopAllServices()
	case trayMenuIdAutorun:
		toggleAutoStart()
	case trayMenuIdQuit:
		quitApp(wnd)
	default:
		return false
	}
	return true
}

// startEssentialStack is the tray menu's "Start Stack" action —
// mirrors the button on the Services page. Runs off the UI thread
// because startService may block on port probes and child spawns.
func startEssentialStack() {
	for _, name := range essentialServices {
		ms := app.findService(name)
		if ms == nil {
			continue
		}
		for i, m := range app.services {
			if m == ms {
				startService(i)
				break
			}
		}
	}
}

// stopAllServices is the tray menu's "Stop All" action.
func stopAllServices() {
	for _, ms := range app.services {
		if ms.Service != nil {
			_ = ms.Service.Stop()
		}
	}
}

// quitApp stops every service, removes the tray icon, and exits. Does
// everything in the right order so the icon disappears cleanly instead
// of lingering in the notification area as a dead tooltip.
func quitApp(wnd *ui.Main) {
	for _, ms := range app.services {
		if ms.Service != nil {
			_ = ms.Service.Stop()
		}
	}
	removeTray(wnd)
	os.Exit(0)
}

// ----- Auto-start on Windows boot ---------------------------------------

// isAutoStartEnabled reports whether the HKCU Run key has a GoAMPP
// entry. Absence of the value = disabled; presence = enabled regardless
// of what the value actually points to (the user can edit it
// manually with regedit if they want).
func isAutoStartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, autoStartKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(autoStartValue)
	return err == nil
}

// setAutoStart writes or removes the HKCU Run key entry. When enabling,
// we point at the current exe with the --tray flag so auto-started
// instances come up hidden in the tray instead of popping a window at
// every login.
func setAutoStart(enable bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, autoStartKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	if !enable {
		if err := k.DeleteValue(autoStartValue); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// Quote the path so a Program Files install doesn't break on spaces.
	cmdLine := fmt.Sprintf(`"%s" %s`, exe, flagStartTray)
	return k.SetStringValue(autoStartValue, cmdLine)
}

// toggleAutoStart flips the Run-key entry state and logs the result.
// Called from the tray menu and the Settings page button.
func toggleAutoStart() {
	next := !isAutoStartEnabled()
	if err := setAutoStart(next); err != nil {
		app.appendLog("auto-start: " + err.Error())
		return
	}
	if next {
		app.appendLog("auto-start enabled — GoAMPP will launch into the tray on login")
	} else {
		app.appendLog("auto-start disabled")
	}
}

// hasTrayFlag reports whether the process was launched with --tray,
// meaning the main window should start hidden.
func hasTrayFlag() bool {
	for _, a := range os.Args[1:] {
		if a == flagStartTray {
			return true
		}
	}
	return false
}
