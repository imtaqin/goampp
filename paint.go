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

// paint.go gives GoAMPP a real color palette. Windigo's default buttons
// are the boring system-themed rectangles; here we draw them ourselves
// with BS_OWNERDRAW + WM_DRAWITEM, backed by cached GDI brushes.
//
// Windigo doesn't expose CreateSolidBrush, so we dial gdi32.dll directly
// through x/sys/windows's lazy DLL loader. The brushes live in a cache
// that's never freed — Windows reclaims them on process exit.

var (
	gdi32                = windows.NewLazySystemDLL("gdi32.dll")
	procCreateSolidBrush = gdi32.NewProc("CreateSolidBrush")

	// windigo v0.2.5 has a bug: HDC.FillRect looks the symbol up in
	// gdi32.dll, but FillRect actually lives in user32.dll. Rather than
	// patch the vendored library, we reach into user32 ourselves.
	user32       = windows.NewLazySystemDLL("user32.dll")
	procFillRect = user32.NewProc("FillRect")
)

// fillRect paints `rc` inside `hdc` with the given brush. Thin wrapper
// over the user32 FillRect syscall.
func fillRect(hdc win.HDC, rc *win.RECT, hBrush win.HBRUSH) {
	procFillRect.Call(
		uintptr(hdc),
		uintptr(unsafe.Pointer(rc)),
		uintptr(hBrush),
	)
}

// rgb packs R, G, B into Windows' 0x00BBGGRR COLORREF.
func rgb(r, g, b uint8) win.COLORREF {
	return win.COLORREF(uint32(r) | uint32(g)<<8 | uint32(b)<<16)
}

// ----- Color palette ------------------------------------------------------
//
// Flat, modern, reads well on both light and dark system themes. Picked
// to be accessible (text has WCAG AA contrast against its background).

var (
	// Main window & page backgrounds.
	colorWindowBg = rgb(0xf5, 0xf7, 0xfa) // nearly white with a blue hint
	colorPanelBg  = rgb(0xff, 0xff, 0xff) // pure white for page content

	// Sidebar column — darker than the content so the nav pops.
	colorSidebarBg    = rgb(0x1e, 0x24, 0x36)
	colorSidebarHover = rgb(0x2e, 0x3a, 0x54)
	colorSidebarActive = rgb(0x3a, 0x7a, 0xef)

	// Accent buttons.
	colorPrimary   = rgb(0x3a, 0x7a, 0xef) // vivid blue
	colorPrimaryHi = rgb(0x2c, 0x5f, 0xc8)

	colorSuccess   = rgb(0x22, 0xa3, 0x4f) // emerald green
	colorSuccessHi = rgb(0x17, 0x82, 0x3d)

	colorDanger   = rgb(0xdc, 0x3a, 0x3a) // red
	colorDangerHi = rgb(0xb4, 0x24, 0x24)

	colorWarning   = rgb(0xed, 0x9d, 0x1d) // amber
	colorWarningHi = rgb(0xc9, 0x7a, 0x0c)

	colorNeutral   = rgb(0x6a, 0x76, 0x8a) // slate
	colorNeutralHi = rgb(0x51, 0x5c, 0x6e)

	// Text colors.
	colorTextDark  = rgb(0x1a, 0x1d, 0x29) // on light bg
	colorTextLight = rgb(0xff, 0xff, 0xff) // on dark bg
)

// ColorScheme is the tuple an owner-drawn button needs to paint itself:
// normal bg, pressed/hover bg, and text color.
type ColorScheme struct {
	Bg      win.COLORREF
	BgHover win.COLORREF
	Text    win.COLORREF
}

// Predefined schemes used throughout the UI. Anything that reads "this
// button does a thing" should map to one of these.
var (
	SchemePrimary = ColorScheme{colorPrimary, colorPrimaryHi, colorTextLight}
	SchemeSuccess = ColorScheme{colorSuccess, colorSuccessHi, colorTextLight}
	SchemeDanger  = ColorScheme{colorDanger, colorDangerHi, colorTextLight}
	SchemeWarning = ColorScheme{colorWarning, colorWarningHi, colorTextLight}
	SchemeNeutral = ColorScheme{colorNeutral, colorNeutralHi, colorTextLight}
	SchemeSidebar = ColorScheme{colorSidebarBg, colorSidebarHover, colorTextLight}
)

// ----- GDI brush cache ---------------------------------------------------

var (
	brushMu    sync.Mutex
	brushCache = map[win.COLORREF]win.HBRUSH{}
)

// createSolidBrush calls gdi32.CreateSolidBrush via direct syscall.
// Windigo exposes GetSysColorBrush but not CreateSolidBrush, so we dial
// in through x/sys/windows.
func createSolidBrush(color win.COLORREF) win.HBRUSH {
	r, _, _ := procCreateSolidBrush.Call(uintptr(color))
	return win.HBRUSH(r)
}

// getBrush returns a cached HBRUSH for color, creating it on first use.
// Safe to call from any thread — GDI handles are process-wide and
// brushes are read-only once created.
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

// ----- Owner-drawn buttons -----------------------------------------------

// buttonSchemes is a ctrl_id → scheme registry. Populated by
// newColoredButton and consulted by handleDrawItem.
var buttonSchemes = map[uint16]ColorScheme{}

// newColoredButton is a thin wrapper around ui.NewButton that marks the
// button as BS_OWNERDRAW and records its color scheme. Every button
// GoAMPP shows on screen should go through this helper — otherwise you
// get the plain system-themed button that the user doesn't like.
func newColoredButton(parent ui.Parent, opts *ui.VarOptsButton, scheme ColorScheme) *ui.Button {
	// BS_OWNERDRAW tells Windows to send us WM_DRAWITEM whenever the
	// button needs to repaint. Click events still fire normally.
	opts.CtrlStyle(co.BS_OWNERDRAW)
	btn := ui.NewButton(parent, opts)
	buttonSchemes[btn.CtrlId()] = scheme
	return btn
}

// handleDrawItem is the shared WM_DRAWITEM handler registered on every
// page container + the main window. It looks up the control's color
// scheme, paints the background, and centers the caption text.
func handleDrawItem(p ui.WmDrawItem) {
	dis := p.DrawItemStruct()
	if dis.CtlType != co.ODT_BUTTON {
		return
	}
	scheme, ok := buttonSchemes[uint16(dis.CtlID)]
	if !ok {
		scheme = SchemeNeutral
	}

	// Darker "pressed" background when the user is mid-click.
	bg := scheme.Bg
	if dis.ItemState&co.ODS_SELECTED != 0 {
		bg = scheme.BgHover
	}

	// 1. Fill the button rect with our bg color. We use our own syscall
	// wrapper because windigo's HDC.FillRect looks FillRect up in
	// gdi32.dll, but it actually lives in user32.dll.
	rc := dis.RcItem
	fillRect(dis.Hdc, &rc, getBrush(bg))

	// 2. Select the button's font into the DC so text measurement +
	// drawing use the same face the rest of the UI uses. Without this,
	// TextOut uses the DC's default (SYSTEM_FIXED_FONT) which looks
	// terrible.
	hFontRaw, _ := dis.HwndItem.SendMessage(co.WM_GETFONT, 0, 0)
	if hFontRaw != 0 {
		_, _ = dis.Hdc.SelectObjectFont(win.HFONT(hFontRaw))
	}

	// 3. Measure the caption so we can center it inside the rect.
	text, _ := dis.HwndItem.GetWindowText()
	sz, _ := dis.Hdc.GetTextExtentPoint32(text)

	// 4. Paint the text — transparent background so our bg color shows.
	_, _ = dis.Hdc.SetBkMode(co.BKMODE_TRANSPARENT)
	_, _ = dis.Hdc.SetTextColor(scheme.Text)

	cx := int(rc.Right - rc.Left)
	cy := int(rc.Bottom - rc.Top)
	x := int(rc.Left) + (cx-int(sz.Cx))/2
	y := int(rc.Top) + (cy-int(sz.Cy))/2
	_ = dis.Hdc.TextOut(x, y, text)

	// 5. Focus rectangle — a thin dotted outline when keyboard-focused.
	// Skipped for the first pass; the color change is enough of a cue.
}

// ----- Background coloring for the main window + pages ------------------

// windowBgBrush is returned from WmCtlColorStatic so our Static labels
// paint on top of the window color instead of the default COLOR_WINDOW
// system brush.
func windowBgBrush() win.HBRUSH {
	return getBrush(colorWindowBg)
}

// staticBgHandler returns a WmCtlColorStatic callback that paints
// every Static control's text on top of the gray window background.
//
// How it works:
//   1. SetBkMode(TRANSPARENT) — text glyphs render without filling
//      their character cells, so the gray underneath shows through
//      cleanly. Without this, you get a white box around the text
//      because the default bg mode (OPAQUE) paints rectangles
//      using the system COLOR_WINDOW (near-white).
//   2. SetTextColor(colorTextDark) — explicit dark text so it
//      stays readable on the light gray background.
//   3. Return windowBgBrush — Windows uses this to paint the
//      Static's *background* (the area not covered by glyphs).
//      With BkMode TRANSPARENT this brush call ends up no-op'd
//      for the text rect itself, but it's still required by the
//      WM_CTLCOLORSTATIC contract.
//
// Wired on both the main window (for Statics that live directly on
// the main window) and on each page Control (for Statics inside a
// page). Without both wirings the chrome and pages render with
// inconsistent backgrounds.
func staticBgHandler(p ui.WmCtlColor) win.HBRUSH {
	_, _ = p.Hdc().SetBkMode(co.BKMODE_TRANSPARENT)
	_, _ = p.Hdc().SetTextColor(colorTextDark)
	return windowBgBrush()
}
