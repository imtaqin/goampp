//go:build windows

package main

import (
	"sync"
	"unsafe"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"
	"golang.org/x/sys/windows"
)

var (
	gdi32                = windows.NewLazySystemDLL("gdi32.dll")
	procCreateSolidBrush = gdi32.NewProc("CreateSolidBrush")

	user32       = windows.NewLazySystemDLL("user32.dll")
	procFillRect = user32.NewProc("FillRect")
)

func fillRect(hdc win.HDC, rc *win.RECT, hBrush win.HBRUSH) {
	procFillRect.Call(
		uintptr(hdc),
		uintptr(unsafe.Pointer(rc)),
		uintptr(hBrush),
	)
}

func rgb(r, g, b uint8) win.COLORREF {
	return win.COLORREF(uint32(r) | uint32(g)<<8 | uint32(b)<<16)
}

var (
	colorWindowBg = rgb(0xf5, 0xf7, 0xfa)
	colorPanelBg  = rgb(0xff, 0xff, 0xff)

	colorSidebarBg     = rgb(0x1e, 0x24, 0x36)
	colorSidebarHover  = rgb(0x2e, 0x3a, 0x54)
	colorSidebarActive = rgb(0x3a, 0x7a, 0xef)

	colorPrimary   = rgb(0x3a, 0x7a, 0xef)
	colorPrimaryHi = rgb(0x2c, 0x5f, 0xc8)

	colorSuccess   = rgb(0x22, 0xa3, 0x4f)
	colorSuccessHi = rgb(0x17, 0x82, 0x3d)

	colorDanger   = rgb(0xdc, 0x3a, 0x3a)
	colorDangerHi = rgb(0xb4, 0x24, 0x24)

	colorWarning   = rgb(0xed, 0x9d, 0x1d)
	colorWarningHi = rgb(0xc9, 0x7a, 0x0c)

	colorNeutral   = rgb(0x6a, 0x76, 0x8a)
	colorNeutralHi = rgb(0x51, 0x5c, 0x6e)

	colorTextDark  = rgb(0x1a, 0x1d, 0x29)
	colorTextLight = rgb(0xff, 0xff, 0xff)
)

type ColorScheme struct {
	Bg      win.COLORREF
	BgHover win.COLORREF
	Text    win.COLORREF
}

var (
	SchemePrimary = ColorScheme{colorPrimary, colorPrimaryHi, colorTextLight}
	SchemeSuccess = ColorScheme{colorSuccess, colorSuccessHi, colorTextLight}
	SchemeDanger  = ColorScheme{colorDanger, colorDangerHi, colorTextLight}
	SchemeWarning = ColorScheme{colorWarning, colorWarningHi, colorTextLight}
	SchemeNeutral = ColorScheme{colorNeutral, colorNeutralHi, colorTextLight}
	SchemeSidebar = ColorScheme{colorSidebarBg, colorSidebarHover, colorTextLight}
)

var (
	brushMu    sync.Mutex
	brushCache = map[win.COLORREF]win.HBRUSH{}
)

func createSolidBrush(color win.COLORREF) win.HBRUSH {
	r, _, _ := procCreateSolidBrush.Call(uintptr(color))
	return win.HBRUSH(r)
}

func getBrush(color win.COLORREF) win.HBRUSH {
	brushMu.Lock()
	defer brushMu.Unlock()
	if b, ok := brushCache[color]; ok {
		return b
	}
	b := createSolidBrush(color)
	brushCache[color] = b
	return b
}

var buttonSchemes = map[uint16]ColorScheme{}

func newColoredButton(parent ui.Parent, opts *ui.VarOptsButton, scheme ColorScheme) *ui.Button {

	opts.CtrlStyle(co.BS_OWNERDRAW)
	btn := ui.NewButton(parent, opts)
	buttonSchemes[btn.CtrlId()] = scheme
	return btn
}

func handleDrawItem(p ui.WmDrawItem) {
	dis := p.DrawItemStruct()
	if dis.CtlType != co.ODT_BUTTON {
		return
	}
	scheme, ok := buttonSchemes[uint16(dis.CtlID)]
	if !ok {
		scheme = SchemeNeutral
	}

	bg := scheme.Bg
	if dis.ItemState&co.ODS_SELECTED != 0 {
		bg = scheme.BgHover
	}

	rc := dis.RcItem
	fillRect(dis.Hdc, &rc, getBrush(bg))

	hFontRaw, _ := dis.HwndItem.SendMessage(co.WM_GETFONT, 0, 0)
	if hFontRaw != 0 {
		_, _ = dis.Hdc.SelectObjectFont(win.HFONT(hFontRaw))
	}

	text, _ := dis.HwndItem.GetWindowText()
	sz, _ := dis.Hdc.GetTextExtentPoint32(text)

	_, _ = dis.Hdc.SetBkMode(co.BKMODE_TRANSPARENT)
	_, _ = dis.Hdc.SetTextColor(scheme.Text)

	cx := int(rc.Right - rc.Left)
	cy := int(rc.Bottom - rc.Top)
	x := int(rc.Left) + (cx-int(sz.Cx))/2
	y := int(rc.Top) + (cy-int(sz.Cy))/2
	_ = dis.Hdc.TextOut(x, y, text)

}

func windowBgBrush() win.HBRUSH {
	return getBrush(colorWindowBg)
}

func staticBgHandler(p ui.WmCtlColor) win.HBRUSH {
	_, _ = p.Hdc().SetBkMode(co.BKMODE_TRANSPARENT)
	_, _ = p.Hdc().SetTextColor(colorTextDark)
	return windowBgBrush()
}
