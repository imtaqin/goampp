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

const (
	trayIconUID    uint32 = 1
	wmTrayCallback co.WM  = 0x8001

	trayMenuIdShow    uint16 = 40001
	trayMenuIdStart   uint16 = 40002
	trayMenuIdStop    uint16 = 40003
	trayMenuIdAutorun uint16 = 40004
	trayMenuIdQuit    uint16 = 40005

	autoStartKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
	autoStartValue   = "GoAMPP"

	flagStartTray = "--tray"
)

var (
	procAppendMenuW = user32.NewProc("AppendMenuW")
)

var trayState struct {
	hIcon     win.HICON
	installed bool
}

func installTray(wnd *ui.Main) error {
	iconPath := filepath.Join(app.baseDir, "logo.ico")
	if _, err := os.Stat(iconPath); err != nil {
		return fmt.Errorf("logo.ico not found next to exe: %w", err)
	}

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

	h := wnd.Hwnd()
	h.SendMessage(co.WM_SETICON, win.WPARAM(co.ICON_SZ_SMALL), win.LPARAM(trayState.hIcon))
	h.SendMessage(co.WM_SETICON, win.WPARAM(co.ICON_SZ_BIG), win.LPARAM(trayState.hIcon))

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

func handleTrayCallback(wnd *ui.Main, p ui.Wm) {
	mouseEvent := co.WM(uint32(p.LParam) & 0xffff)
	switch mouseEvent {
	case co.WM_LBUTTONDBLCLK, co.WM_LBUTTONUP:
		showMainWindow(wnd)
	case co.WM_RBUTTONUP, co.WM_CONTEXTMENU:
		showTrayMenu(wnd)
	}
}

func showMainWindow(wnd *ui.Main) {
	h := wnd.Hwnd()
	h.ShowWindow(co.SW_SHOW)
	h.ShowWindow(co.SW_RESTORE)
	h.SetForegroundWindow()
}

func hideMainWindow(wnd *ui.Main) {
	wnd.Hwnd().ShowWindow(co.SW_HIDE)
}

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

	pt, _ := win.GetCursorPos()
	wnd.Hwnd().SetForegroundWindow()
	_, _ = hMenu.TrackPopupMenu(co.TPM_LEFTBUTTON|co.TPM_RIGHTBUTTON,
		int(pt.X), int(pt.Y), wnd.Hwnd())
	_ = wnd.Hwnd().PostMessage(co.WM_NULL, 0, 0)
}

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

func stopAllServices() {
	for _, ms := range app.services {
		if ms.Service != nil {
			_ = ms.Service.Stop()
		}
	}
}

func quitApp(wnd *ui.Main) {
	for _, ms := range app.services {
		if ms.Service != nil {
			_ = ms.Service.Stop()
		}
	}
	removeTray(wnd)
	os.Exit(0)
}

func isAutoStartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, autoStartKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(autoStartValue)
	return err == nil
}

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

	cmdLine := fmt.Sprintf(`"%s" %s`, exe, flagStartTray)
	return k.SetStringValue(autoStartValue, cmdLine)
}

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

func hasTrayFlag() bool {
	for _, a := range os.Args[1:] {
		if a == flagStartTray {
			return true
		}
	}
	return false
}
