//go:build windows

package main

import (
	"fmt"
	"strings"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"
)

var (
	updatesRefreshBtn    *ui.Button
	updatesApplyBtn      *ui.Button
	updateApplyBusy      bool
	serviceVersionLabels = map[int]*ui.Static{}
)

const (
	updateMenuBase = 7000
	updateMenuAll  = 7099
)

func installUpdateRefreshUI() {
	if svcTabParent == nil {
		return
	}

	if err := loadPersistedLatestCatalog(app.baseDir); err != nil {
		app.appendLog("updates: load cached versions: " + err.Error())
	}

	updatesRefreshBtn = newColoredButton(svcTabParent, ui.OptsButton().
		Text("Refresh").
		Position(ui.Dpi(438, 4)).
		Width(ui.DpiX(84)).Height(ui.DpiY(26)),
		SchemePrimary)
	updatesRefreshBtn.On().BnClicked(func() {
		// Do not disable the button for a refresh that cannot start. The worker
		// owns the same mutex and will perform the authoritative lock attempt.
		if !updateOperationMu.TryLock() {
			return
		}
		updateOperationMu.Unlock()
		setUpdateRefreshBusy(true)
		go refreshAllServiceUpdates()
	})

	updatesApplyBtn = newColoredButton(svcTabParent, ui.OptsButton().
		Text("Update").
		Position(ui.Dpi(528, 4)).
		Width(ui.DpiX(96)).Height(ui.DpiY(26)),
		SchemeSuccess)
	updatesApplyBtn.Hwnd().EnableWindow(false)
	updatesApplyBtn.On().BnClicked(showAvailableUpdatesMenu)

	// Own the visible title text instead of trying to squeeze a second control
	// into the already-full status row. The original title is kept hidden so
	// existing refreshServiceList calls can continue updating it harmlessly.
	for _, c := range serviceCards {
		c.nameStatic.Hwnd().ShowWindow(co.SW_HIDE)
		label := ui.NewStatic(c.cardCtrl, ui.OptsStatic().
			Text(app.services[c.srcIdx].Conf.Name).
			Position(ui.Dpi(58, 10)).
			Size(ui.Dpi(cardW-66, 16)))
		serviceVersionLabels[c.srcIdx] = label
	}
	applyServiceUpdateDecorations()

	// Warm branch-based runtime versions without blocking startup. PHP, Node
	// and Python can therefore refresh the persisted exact patch for the
	// selected branch without forcing a full catalogue refresh.
	go func() {
		if err := refreshVariantCatalog(false); err != nil {
			if app != nil {
				app.appendLog("downloads: latest runtime branch lookup: " + err.Error())
			}
		} else if app != nil {
			if err := persistLatestCatalog(app.baseDir, nil); err != nil {
				app.appendLog("updates: save cached runtime versions: " + err.Error())
			}
		}
		if app != nil && app.wnd != nil {
			app.wnd.UiThread(applyServiceUpdateDecorations)
		}
	}()
}

func setUpdateRefreshBusy(busy bool) {
	if updatesRefreshBtn == nil {
		return
	}
	if busy {
		updatesRefreshBtn.Hwnd().SetWindowText("Checking...")
		updatesRefreshBtn.Hwnd().EnableWindow(false)
		if updatesApplyBtn != nil {
			updatesApplyBtn.Hwnd().EnableWindow(false)
		}
	} else {
		updatesRefreshBtn.Hwnd().SetWindowText("Refresh")
		updatesRefreshBtn.Hwnd().EnableWindow(!updateApplyBusy)
		refreshUpdateApplyButton()
	}
	updatesRefreshBtn.Hwnd().InvalidateRect(nil, true)
}

func setUpdateApplyBusy(busy bool) {
	updateApplyBusy = busy
	if updatesRefreshBtn != nil {
		updatesRefreshBtn.Hwnd().EnableWindow(!busy)
		updatesRefreshBtn.Hwnd().InvalidateRect(nil, true)
	}
	if updatesApplyBtn == nil {
		return
	}
	if busy {
		updatesApplyBtn.Hwnd().SetWindowText("Updating...")
		updatesApplyBtn.Hwnd().EnableWindow(false)
	} else {
		refreshUpdateApplyButton()
	}
	updatesApplyBtn.Hwnd().InvalidateRect(nil, true)
}

func refreshUpdateApplyButton() {
	if updatesApplyBtn == nil || updateApplyBusy {
		return
	}
	count := len(availableServiceUpdates())
	if count == 0 {
		updatesApplyBtn.Hwnd().SetWindowText("Update")
		updatesApplyBtn.Hwnd().EnableWindow(false)
	} else {
		updatesApplyBtn.Hwnd().SetWindowText(fmt.Sprintf("Update (%d)", count))
		updatesApplyBtn.Hwnd().EnableWindow(true)
	}
	updatesApplyBtn.Hwnd().InvalidateRect(nil, true)
}

func showAvailableUpdatesMenu() {
	infos := availableServiceUpdates()
	if len(infos) == 0 || updatesApplyBtn == nil {
		return
	}

	hMenu, err := win.CreatePopupMenu()
	if err != nil {
		return
	}
	defer hMenu.DestroyMenu()

	appendMenuItem(hMenu, co.MF_STRING|co.MF_DISABLED, 0, "Available updates")
	appendMenuItem(hMenu, co.MF_SEPARATOR, 0, "")
	for i, info := range infos {
		current := compactServiceVersion(info.Name, info.Current.Version)
		latest := compactServiceVersion(info.Name, info.Latest.Version)
		label := info.Name + "  " + latest
		if current != "" && latest != "" {
			label = fmt.Sprintf("%s  %s → %s", info.Name, current, latest)
		}
		appendMenuItem(hMenu, co.MF_STRING, uintptr(updateMenuBase+i), label)
	}
	appendMenuItem(hMenu, co.MF_SEPARATOR, 0, "")
	appendMenuItem(hMenu, co.MF_STRING, uintptr(updateMenuAll), fmt.Sprintf("Update all (%d)", len(infos)))

	rc, err := updatesApplyBtn.Hwnd().GetWindowRect()
	if err != nil {
		return
	}
	app.wnd.Hwnd().SetForegroundWindow()
	cmd, _ := hMenu.TrackPopupMenu(
		co.TPM_LEFTBUTTON|co.TPM_RIGHTBUTTON|co.TPM_RETURNCMD,
		int(rc.Left), int(rc.Bottom), app.wnd.Hwnd())
	_ = app.wnd.Hwnd().PostMessage(co.WM_NULL, 0, 0)
	if cmd <= 0 {
		return
	}

	var names []string
	if cmd == updateMenuAll {
		names = make([]string, 0, len(infos))
		for _, info := range infos {
			names = append(names, info.Name)
		}
	} else {
		idx := cmd - updateMenuBase
		if idx < 0 || idx >= len(infos) {
			return
		}
		names = []string{infos[idx].Name}
	}

	setUpdateApplyBusy(true)
	go applyServiceUpdates(names)
}

func compactServiceVersion(name, version string) string {
	v := strings.TrimSpace(version)
	if v == "" {
		return ""
	}

	switch name {
	case "Apache":
		// Bundled fallback includes compiler/architecture prose. The live
		// resolver returns the useful Lounge package identity, including build.
		if strings.Contains(v, " ") {
			v = strings.Fields(v)[0]
		}
	case "Nginx":
		v = strings.TrimSpace(strings.TrimSuffix(v, "stable"))
	case "MySQL":
		v = strings.TrimSpace(strings.TrimSuffix(v, "LTS"))
	case "Java":
		v = strings.TrimPrefix(v, "Temurin JDK ")
	case "Rust":
		v = strings.TrimSpace(strings.TrimSuffix(v, "stable"))
	case "Erlang":
		if i := strings.Index(v, " (OTP-27)"); i > 0 {
			v = "OTP " + v[:i]
		}
	case "Elixir":
		v = strings.ReplaceAll(v, " (OTP 27)", " / OTP27")
	case "MinIO":
		if strings.HasPrefix(v, "RELEASE.") {
			v = strings.TrimPrefix(v, "RELEASE.")
			if len(v) > 10 {
				v = v[:10]
			}
		}
	}
	return strings.TrimSpace(v)
}

func displayedServiceVersion(ms *ManagedService) (version string, updateAvailable bool) {
	if info, ok := cachedServiceUpdate(ms.Conf.Name); ok && info.Latest.Version != "" {
		return compactServiceVersion(ms.Conf.Name, info.Latest.Version), info.Available
	}

	if isDynamicVariantService(ms.Conf.Name) {
		if resolved, ok := resolvedVariantDownload(ms.Conf.Name, ms.Conf.ActiveVersion); ok {
			return compactServiceVersion(ms.Conf.Name, resolved.Version), false
		}
	}

	if resolved, ok := cachedPersistedLatest(ms.Conf.Name); ok && resolved.Version != "" {
		return compactServiceVersion(ms.Conf.Name, resolved.Version), false
	}

	spec, ok := DownloadCatalog[ms.Conf.Name]
	if !ok {
		return "", false
	}
	if len(spec.Variants) > 0 {
		return effectiveVariant(ms.Conf.Name, ms.Conf.ActiveVersion), false
	}
	return compactServiceVersion(ms.Conf.Name, spec.Version), false
}

func applyServiceUpdateDecorations() {
	for _, c := range serviceCards {
		label := serviceVersionLabels[c.srcIdx]
		if label == nil {
			continue
		}

		ms := app.services[c.srcIdx]
		version, updateAvailable := displayedServiceVersion(ms)
		text := ms.Conf.Name
		if version != "" {
			text += "  " + version
		}
		if updateAvailable {
			text += "  ↑"
		}
		label.Hwnd().SetWindowText(text)
		label.Hwnd().InvalidateRect(nil, true)
	}
	refreshUpdateApplyButton()
}
