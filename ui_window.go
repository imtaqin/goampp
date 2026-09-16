//go:build windows

package main

import (
	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"
)

func mainWindowStyle() co.WS {
	return co.WS_CAPTION | co.WS_SYSMENU | co.WS_CLIPCHILDREN |
		co.WS_BORDER | co.WS_VISIBLE | co.WS_MINIMIZEBOX | co.WS_SIZEBOX
}

func configureMainWindowSizing(wnd *ui.Main) {
	wnd.On().WmGetMinMaxInfo(func(p ui.WmGetMinMaxInfo) {
		clientW, clientH := ui.Dpi(winW, winH)
		rc := win.RECT{
			Right:  int32(clientW),
			Bottom: int32(clientH),
		}
		win.AdjustWindowRectEx(&rc, mainWindowStyle(), false, 0)

		outerW := rc.Right - rc.Left
		outerH := rc.Bottom - rc.Top
		info := p.Info()
		info.PtMinTrackSize.X = outerW
		info.PtMaxTrackSize.X = outerW
		info.PtMinTrackSize.Y = outerH
	})
}
