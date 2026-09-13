//go:build windows

package main

import (
	"fmt"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
)

func buildStartupPage(parent *ui.Control) {
	ui.NewStatic(parent, ui.OptsStatic().
		Text("START SERVICES WITH GOAMPP").
		Position(ui.Dpi(10, 12)).
		Size(ui.Dpi(contentW-20, 18)))
	ui.NewStatic(parent, ui.OptsStatic().
		Text("Choose installed background services to start whenever GoAMPP launches. This is separate from starting GoAMPP with Windows.").
		Position(ui.Dpi(10, 34)).
		Size(ui.Dpi(contentW-20, 18)))
	ui.NewStatic(parent, ui.OptsStatic().
		Text("").
		Position(ui.Dpi(10, 58)).
		Size(ui.Dpi(contentW-20, 1)).
		CtrlStyle(co.SS_ETCHEDHORZ))

	eligible := make([]*ManagedService, 0)
	for _, ms := range app.services {
		if startupServiceEligible(ms) {
			eligible = append(eligible, ms)
		}
	}
	if len(eligible) == 0 {
		ui.NewStatic(parent, ui.OptsStatic().
			Text("No installed startable services yet.").
			Position(ui.Dpi(16, 80)).
			Size(ui.Dpi(contentW-32, 18)))
		return
	}

	const (
		startY = 78
		rowH   = 34
		colW   = 455
	)
	for i, ms := range eligible {
		ms := ms
		col := i % 2
		row := i / 2
		x := 12 + col*colW
		y := startY + row*rowH

		btn := newColoredButton(parent, ui.OptsButton().
			Text("").
			Position(ui.Dpi(x, y)).
			Width(ui.DpiX(76)).Height(ui.DpiY(24)),
			SchemeNeutral)
		setStartupButtonState(btn, autoStartContains(ms.Conf.Name))
		btn.On().BnClicked(func() {
			name := ms.Conf.Name
			next := !autoStartContains(name)
			if err := setServiceAutoStart(name, next); err != nil {
				app.appendLog("startup services: " + err.Error())
				return
			}
			setStartupButtonState(btn, next)
			state := "skip"
			if next {
				state = "start"
			}
			app.appendLog(fmt.Sprintf("startup services: %s → %s", name, state))
		})

		label := ms.Conf.Name
		if ms.Conf.Port > 0 {
			label += fmt.Sprintf("   :%d", ms.Conf.Port)
		}
		ui.NewStatic(parent, ui.OptsStatic().
			Text(label).
			Position(ui.Dpi(x+88, y+4)).
			Size(ui.Dpi(colW-96, 18)))
	}
}

func startupServiceEligible(ms *ManagedService) bool {
	if ms == nil || ms.Service == nil {
		return false
	}
	if (ms.Conf.Name == "Apache" || ms.Conf.Name == "Nginx") && ms.Conf.Name != activeWebServer() {
		return false
	}
	if _, ok := DownloadCatalog[ms.Conf.Name]; ok {
		return IsInstalled(ms.Conf.Name, app.baseDir)
	}
	return true
}

func autoStartContains(name string) bool {
	if app == nil || app.cfg == nil {
		return false
	}
	for _, current := range app.cfg.Settings.AutoStart {
		if current == name {
			return true
		}
	}
	return false
}

func setServiceAutoStart(name string, enabled bool) error {
	if app == nil || app.cfg == nil {
		return fmt.Errorf("configuration not loaded")
	}
	before := append([]string(nil), app.cfg.Settings.AutoStart...)

	next := make([]string, 0, len(before)+1)
	for _, current := range before {
		if current != name {
			next = append(next, current)
		}
	}
	if enabled {
		next = append(next, name)
	}
	app.cfg.Settings.AutoStart = next
	if err := SaveConfig(app.baseDir, app.cfg); err != nil {
		app.cfg.Settings.AutoStart = before
		return err
	}
	return nil
}

func setStartupButtonState(btn *ui.Button, enabled bool) {
	if btn == nil {
		return
	}
	scheme := SchemeNeutral
	text := "Skip"
	if enabled {
		scheme = SchemeSuccess
		text = "Start"
	}
	buttonSchemes[btn.CtrlId()] = scheme
	btn.Hwnd().SetWindowText(text)
	btn.Hwnd().InvalidateRect(nil, true)
}
