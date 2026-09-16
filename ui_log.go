//go:build windows

package main

import (
	"syscall"
	"unsafe"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/win"
)

var procGetScrollInfo = user32.NewProc("GetScrollInfo")

const (
	sbVert   int32  = 1
	sifRange uint32 = 0x0001
	sifPage  uint32 = 0x0002
	sifPos   uint32 = 0x0004
)

type scrollInfo struct {
	CbSize    uint32
	FMask     uint32
	NMin      int32
	NMax      int32
	NPage     uint32
	NPos      int32
	NTrackPos int32
}

func appendLogBoxText(a *App, text string) {
	if a == nil || a.logBox == nil || text == "" {
		return
	}

	h := a.logBox.Hwnd()
	follow := editScrolledToBottom(h)
	first, _ := h.SendMessage(co.EM_GETFIRSTVISIBLELINE, 0, 0)

	const eot = uintptr(0x7fffffff)
	h.SendMessage(co.EM_SETSEL, win.WPARAM(eot), win.LPARAM(eot))

	ptr, err := syscall.UTF16PtrFromString(text)
	if err == nil {
		h.SendMessage(co.EM_REPLACESEL, 0, win.LPARAM(uintptr(unsafe.Pointer(ptr))))
	}

	if follow {
		h.SendMessage(co.EM_SCROLLCARET, 0, 0)
		return
	}

	current, _ := h.SendMessage(co.EM_GETFIRSTVISIBLELINE, 0, 0)
	delta := int32(first) - int32(current)
	if delta != 0 {
		h.SendMessage(co.EM_LINESCROLL, 0, win.LPARAM(delta))
	}
}

func resetLogBoxText(a *App, text string) {
	if a == nil || a.logBox == nil {
		return
	}

	h := a.logBox.Hwnd()
	follow := editScrolledToBottom(h)
	first, _ := h.SendMessage(co.EM_GETFIRSTVISIBLELINE, 0, 0)

	a.logBox.SetText(text)

	if follow {
		const eot = uintptr(0x7fffffff)
		h.SendMessage(co.EM_SETSEL, win.WPARAM(eot), win.LPARAM(eot))
		h.SendMessage(co.EM_SCROLLCARET, 0, 0)
		return
	}

	current, _ := h.SendMessage(co.EM_GETFIRSTVISIBLELINE, 0, 0)
	delta := int32(first) - int32(current)
	if delta != 0 {
		h.SendMessage(co.EM_LINESCROLL, 0, win.LPARAM(delta))
	}
}

func editScrolledToBottom(h win.HWND) bool {
	si := scrollInfo{
		CbSize: uint32(unsafe.Sizeof(scrollInfo{})),
		FMask:  sifRange | sifPage | sifPos,
	}
	r, _, _ := procGetScrollInfo.Call(
		uintptr(h),
		uintptr(sbVert),
		uintptr(unsafe.Pointer(&si)),
	)
	if r == 0 || si.NPage == 0 {
		return true
	}
	bottomPos := si.NMax - int32(si.NPage) + 1
	return si.NPos >= bottomPos-1
}
