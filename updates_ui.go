//go:build windows

package main

import (
	"strings"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
)

var (
	updatesRefreshBtn    *ui.Button
	serviceVersionLabels = map[int]*ui.Static{}
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
		if !updateRefreshMu.TryLock() {
			return
		}
		updateRefreshMu.Unlock()
		setUpdateRefreshBusy(true)
		go refreshAllServiceUpdates()
	})

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
	} else {
		updatesRefreshBtn.Hwnd().SetWindowText("Refresh")
		updatesRefreshBtn.Hwnd().EnableWindow(true)
	}
	updatesRefreshBtn.Hwnd().InvalidateRect(nil, true)
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
}
