//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"
)

// ----- Layout geometry --------------------------------------------------
// Everything is in design pixels. ui.Dpi scales at construction time, so
// these numbers are what you see on a 100% monitor.
const (
	// Bumped 960 → 1100 so cards can breathe — 228px cards fit
	// 3 buttons (Start / Conf / Ver) without crowding instead of
	// the previous 195px squeeze where labels overlapped.
	winW = 1100
	winH = 630

	// Sidebar (left column of nav buttons).
	sideX = 10
	sideY = 48
	sideW = 110
	sideH = 38
	sideGap = 4

	// Content area — wider to fit the bigger cards.
	contentX = 130
	contentY = 48
	contentW = 960
	contentH = 368

	// Download progress strip — sits 8px below the content area.
	progY = 424
	progH = 18

	// Log panel — full width minus 10px margin each side.
	logX = 10
	logY = 450
	logW = 1080
	logH = 140
)

// ----- Page container tracking ------------------------------------------

// page wraps one of the Control containers that act as a "view" in the
// sidebar navigation. Exactly one page is visible at a time; the rest are
// ShowWindow(SW_HIDE)'d.
type page struct {
	key       string      // matches the sidebar button key
	title     string      // shown in the page header strip
	container *ui.Control // the Win32 child window acting as the page
}

// domainExtensions is the dev-friendly TLD list shown in the
// "Domain extension" dropdown on the Projects and Virtual Hosts
// forms. .test is first because it's the only one reserved by
// RFC 6761 specifically for testing — it can never resolve to a
// real public site, so there's no risk of accidentally hijacking
// production traffic via your hosts file.
//
// .dev is intentionally NOT included: Google bought it as a real
// gTLD with HSTS preload, so any browser that hits an http:// URL
// on .dev will refuse to load it.
var domainExtensions = []string{
	".test",
	".local",
	".localhost",
	".lan",
	".home",
	".site",
}

// essentialServices is the "Start Stack" button's target set — the core
// web stack a developer needs to serve PHP pages with a database:
//
//   - Apache    (web server; runs PHP via classic CGI)
//   - MySQL     (MariaDB — phpMyAdmin is useless without it)
//   - phpMyAdmin (tool-only; auto-installs if missing)
//
// PHP itself doesn't need a running service — Apache spawns php-cgi.exe
// per request via mod_cgi, so there's no "PHP" entry here.
//
// Nginx, Postgres, Redis, Adminer stay manual — boot them from the
// per-row Start button if you need them.
var essentialServices = []string{"Apache", "PHP-FPM", "MySQL", "phpMyAdmin"}

// ensureStackEssentials walks the essential-services list and:
//   1. downloads + installs anything missing,
//   2. launches services whose ServiceConf.Enabled is true AND that
//      have a real daemon (ms.Service != nil),
//   3. opens http://localhost/ in the browser at the end — once,
//      after Apache is ready.
//
// Services with Enabled=false (e.g. PHP-FPM) are install-only:
// Apache spawns php-cgi.exe per-request via mod_cgi.
//
// Tool entries (phpMyAdmin, Adminer) have ms.Service == nil — the
// install step is enough, we don't kick startService on them
// because that would call openPath(OpenURL) and pop a browser tab
// to /phpmyadmin/ in the middle of Start Stack. Users who want
// phpMyAdmin click its card directly.
//
// Synchronous — call from a goroutine. Called from both Start Stack
// and Restart Stack so they share the same install/launch policy.
func ensureStackEssentials() {
	for _, name := range essentialServicesActive() {
		ms := app.findService(name)
		if ms == nil {
			continue
		}
		// Step 1: ensure the binary is on disk. The tightened
		// CheckFile sentinels catch partial installs too — e.g.
		// Apache with no conf/, MySQL with no system tables.
		if _, ok := DownloadCatalog[name]; ok && !IsInstalled(name, app.baseDir) {
			if err := DownloadAndInstallVersion(name, ms.Conf.ActiveVersion, app.baseDir, app.appendLog, uiDownloadProgress); err != nil {
				app.appendLog(fmt.Sprintf("[%s] install failed: %v", name, err))
				continue
			}
		}
		// Step 2: skip non-daemon entries (tools) and disabled
		// daemons. Both fall through with the binary on disk —
		// Apache will serve them once its Action handler / docroot
		// references kick in.
		if ms.Service == nil || !ms.Conf.Enabled {
			continue
		}
		for i, m := range app.services {
			if m == ms {
				startService(i)
				break
			}
		}
		time.Sleep(400 * time.Millisecond)
	}
	app.wnd.UiThread(refreshServiceList)

	// Step 3: open the welcome page once, after the stack is up.
	// Small delay so Apache has had time to bind :80 and accept
	// the first request without the browser hitting "connection
	// refused" and showing an error.
	time.Sleep(800 * time.Millisecond)
	openPath("http://localhost/")
}

// essentialServicesActive returns essentialServices with the web
// server slot replaced by whichever server the user picked in the
// Active Web Server dropdown. So if they switched to Nginx, "Start
// Stack" boots Nginx instead of Apache.
func essentialServicesActive() []string {
	out := make([]string, 0, len(essentialServices))
	web := activeWebServer()
	for _, name := range essentialServices {
		if name == "Apache" || name == "Nginx" {
			out = append(out, web)
			continue
		}
		out = append(out, name)
	}
	return out
}

var (
	pages      []*page
	activePage string // currently-visible page key

	// Download progress strip widgets (set by buildProgressStrip,
	// updated by uiDownloadProgress).
	progBar   *ui.ProgressBar
	progLabel *ui.Static

	// (Services page no longer has a ListView — it's a hand-built
	// grid of serviceCard widgets, declared as its own var below.)

	// Editor page widgets (populated in buildEditorPage).
	edDropdown  *ui.ComboBox
	edPathLabel *ui.Static
	edContent   *ui.Edit
	edKnownPaths []string // indexed parallel to the dropdown items

	// Vhosts page widgets (populated in buildVhostsPage). The
	// domain field is split into a name input + extension dropdown
	// so users pick the TLD from a known-good list rather than
	// typing it free-form (and accidentally creating a .dev vhost
	// that hits HSTS preload).
	vhostList      *ui.ListView
	vhostDomainNm  *ui.Edit
	vhostDomainExt *ui.ComboBox
	vhostDocRoot   *ui.Edit
	vhostPort      *ui.Edit
	vhostServer    *ui.ComboBox

	// Projects page widgets (populated in buildProjectsPage). Same
	// split-domain treatment as the Vhosts form.
	projFramework *ui.ComboBox
	projName      *ui.Edit
	projDomainNm  *ui.Edit
	projDomainExt *ui.ComboBox
	projList      *ui.ListView
)

// ----- Entry point -------------------------------------------------------

// buildMainLayout constructs the whole single-window layout: sidebar on
// the left, content pages in the middle, progress strip + log panel at
// the bottom. Called once from main.go after the Main window is created.
func buildMainLayout(wnd *ui.Main) {
	buildTitleBar(wnd)
	buildSidebar(wnd)
	buildPages(wnd)
	buildProgressStrip(wnd)
	buildLogPanel(wnd)
}

// buildProgressStrip places the download progress bar + its status label
// just above the log panel. Both are always visible — the label just
// reads "Idle" when no download is running.
func buildProgressStrip(wnd *ui.Main) {
	// Status label on the left.
	progLabel = ui.NewStatic(wnd, ui.OptsStatic().
		Text("Idle").
		Position(ui.Dpi(10, progY+3)).
		Size(ui.Dpi(380, 16)))

	// ProgressBar filling the rest of the strip width.
	progBar = ui.NewProgressBar(wnd, ui.OptsProgressBar().
		Position(ui.Dpi(400, progY)).
		Size(ui.Dpi(550, progH)).
		Range(0, 1000)) // we report in thousandths so the bar is smooth
}

// uiDownloadProgress is the DownloadAndInstall progress callback. It
// runs off the UI thread — we marshal every update back to the UI
// thread via wnd.UiThread so SetPos and SetWindowText are safe.
func uiDownloadProgress(stage, name string, done, total int64) {
	if app.wnd == nil {
		return
	}
	// Precompute the values off the UI thread so the closure is cheap.
	var (
		labelText string
		pos       int // 0..1000 so the bar has sub-percent resolution
	)
	switch stage {
	case "starting":
		labelText = fmt.Sprintf("Starting %s ...", name)
		pos = 0
	case "downloading":
		if total > 0 {
			pct := float64(done) * 100 / float64(total)
			labelText = fmt.Sprintf("Downloading %s  %.1f%%  %.1f / %.1f MB",
				name, pct, float64(done)/(1024*1024), float64(total)/(1024*1024))
			pos = int(float64(done) * 1000 / float64(total))
		} else {
			labelText = fmt.Sprintf("Downloading %s  %.1f MB", name, float64(done)/(1024*1024))
			pos = 0
		}
	case "extracting":
		if total > 0 {
			labelText = fmt.Sprintf("Extracting %s  %d / %d files", name, done, total)
			pos = int(float64(done) * 1000 / float64(total))
		} else {
			labelText = fmt.Sprintf("Extracting %s ...", name)
			pos = 500
		}
	case "post-install":
		labelText = fmt.Sprintf("Running post-install for %s ...", name)
		pos = 1000
	case "done":
		labelText = fmt.Sprintf("Installed %s", name)
		pos = 1000
	case "idle":
		labelText = "Idle"
		pos = 0
	default:
		labelText = stage
	}
	app.wnd.UiThread(func() {
		if progBar != nil {
			progBar.SetPos(pos)
		}
		if progLabel != nil {
			progLabel.Hwnd().SetWindowText(labelText)
		}
	})
}

// buildTitleBar used to draw a "GoAMPP / Local Web Stack Control
// Panel" header. The user asked for a cleaner layout so we now
// emit just the etched separator line — the OS title bar already
// shows the window title, no need to repeat it inside the chrome.
func buildTitleBar(wnd *ui.Main) {
	ui.NewStatic(wnd, ui.OptsStatic().
		Text("").
		Position(ui.Dpi(0, 44)).
		Size(ui.Dpi(winW, 1)).
		CtrlStyle(co.SS_ETCHEDHORZ))
}

// buildSidebar creates the vertical column of navigation buttons. Each
// button calls showPage() with its own key.
func buildSidebar(wnd *ui.Main) {
	items := []struct {
		key, label string
	}{
		{"services", "Services"},
		{"projects", "Projects"},
		{"editor", "Editor"},
		{"vhosts", "Virtual Hosts"},
		{"settings", "Settings"},
	}
	y := sideY
	for _, it := range items {
		key := it.key
		newColoredButton(wnd, ui.OptsButton().
			Text(it.label).
			Position(ui.Dpi(sideX, y)).
			Width(ui.DpiX(sideW)).Height(ui.DpiY(sideH)),
			SchemeSidebar).
			On().BnClicked(func() {
			showPage(key)
		})
		y += sideH + sideGap
	}

	// Vertical separator between sidebar and content.
	ui.NewStatic(wnd, ui.OptsStatic().
		Text("").
		Position(ui.Dpi(sideX+sideW+6, sideY)).
		Size(ui.Dpi(1, contentH+10)).
		CtrlStyle(co.SS_ETCHEDVERT))
}

// buildPages creates one Control container per view and stores them in
// the pages slice. They all overlap at the same rect — only the active
// one is shown (set in main.go's WmCreate handler, which calls showPage).
func buildPages(wnd *ui.Main) {
	pages = []*page{
		{key: "services", title: "Services", container: newPageContainer(wnd)},
		{key: "projects", title: "Projects", container: newPageContainer(wnd)},
		{key: "editor", title: "Editor", container: newPageContainer(wnd)},
		{key: "vhosts", title: "Virtual Hosts", container: newPageContainer(wnd)},
		{key: "settings", title: "Settings", container: newPageContainer(wnd)},
	}
	buildServicesPage(pages[0].container)
	buildProjectsPage(pages[1].container)
	buildEditorPage(pages[2].container)
	buildVhostsPage(pages[3].container)
	buildSettingsPage(pages[4].container)
	activePage = "services"
}

// newPageContainer mints a Control sized to the content area. We
// override the class brush to our gray window-background colour and
// wire the WM_CTLCOLORSTATIC handler so child Statics blend in
// instead of showing a white box behind their text. WS_EX_CLIENTEDGE
// is stripped so there's no chunky sunken border around each page.
func newPageContainer(wnd *ui.Main) *ui.Control {
	c := ui.NewControl(wnd, ui.OptsControl().
		Position(ui.Dpi(contentX, contentY)).
		Size(ui.Dpi(contentW, contentH)).
		ExStyle(co.WS_EX_LEFT).
		ClassBrush(windowBgBrush())) // gray fill instead of system white
	c.On().WmDrawItem(handleDrawItem)
	c.On().WmCtlColorStatic(staticBgHandler)
	return c
}

// showPage makes `key` visible and hides every other page. Called from
// sidebar button handlers and from the initial WmCreate fixup.
//
// Why the InvalidateRect dance: hiding/showing sibling Control
// containers via SW_HIDE/SW_SHOW does NOT automatically erase the
// pixels where the previous page's children were drawn. With
// WS_CLIPCHILDREN on the parent, only the new page's child rects
// get repainted — anything that lived on the old page but doesn't
// have a corresponding child on the new page leaves stale pixels
// behind. Forcing the new page to invalidate its full client area
// (with erase=true) makes Windows send WM_ERASEBKGND first, which
// fills the area with the class brush (gray), wiping the leftover.
func showPage(key string) {
	// Hide every non-active page FIRST. Doing this before showing the
	// new page means the (now-active) page paints onto an already-cleared
	// region instead of on top of the prior page's stale pixels.
	for _, p := range pages {
		if p.key != key {
			p.container.Hwnd().ShowWindow(co.SW_HIDE)
		}
	}
	for _, p := range pages {
		if p.key == key {
			h := p.container.Hwnd()
			h.ShowWindow(co.SW_SHOW)
			// Force a full erase + repaint of the page AND all of its
			// children. Without RDW_ALLCHILDREN, transparent / non-
			// opaque child controls (Statics with CTLCOLORSTATIC) keep
			// whatever pixels were under them when the prior tab was
			// last drawn — which is exactly the "old tab bleeding
			// through behind the cards" symptom.
			_ = h.RedrawWindow(nil, 0,
				co.RDW_INVALIDATE|co.RDW_ERASE|co.RDW_ALLCHILDREN|co.RDW_UPDATENOW)
		}
	}
	activePage = key
	if app.statusBar != nil {
		app.statusBar.Part(0).SetText(fmt.Sprintf("Page: %s", key))
	}
}

// ----- Services page -----------------------------------------------------

// serviceCard bundles all the widgets for one service tile so
// refreshServiceList can update text + button labels in place
// without rebuilding the whole grid.
type serviceCard struct {
	srcIdx     int        // index into app.services
	iconStatic *ui.Static // SS_ICON, set via STM_SETICON
	nameStatic *ui.Static
	statusLbl  *ui.Static
	versionLbl *ui.Static
	// rect is the card's bounding box in page-client coords. Used by
	// the right-click context-menu hit test to identify which card
	// the user clicked when picking a version.
	rect win.RECT

	// One toggle button replaces the old Start/Stop pair: its label
	// flips between "▶ Start" and "■ Stop" and its colour scheme
	// flips between green (action: start) and red (action: stop)
	// depending on Service.Running(). Restart and Conf stay split
	// out — they're always-applicable verbs that don't toggle.
	btnToggle  *ui.Button
	btnRestart *ui.Button
	btnConf    *ui.Button
	// btnVer is a small "▾" button that opens the version-switch
	// menu, but ONLY shown for multi-version services (PHP, Node,
	// Python). Discoverability: right-click works too, but a
	// visible affordance is the obvious place for users to click.
	btnVer *ui.Button

	// statusDot is a small coloured square painted as a Static with
	// owner-draw — green = running, red = stopped, grey = not
	// installed. Lives at the top-left of the card next to the icon.
	statusDot *ui.Static
}

// versionMenuBase is the starting menu-item ID for the right-click
// context menu's variant entries. We keep it well above any other
// command range used in the app (sidebar/footer buttons + tray
// items) so there's no chance of collision when TrackPopupMenu's
// returned ID is decoded back into a service+version pair.
const versionMenuBase = 6000

// showServiceContextMenu hit-tests the cursor position against
// every service card and, if the click landed on a multi-version
// service, pops a "Switch to version X" menu.
func showServiceContextMenu(parent *ui.Control, screenPt win.POINT) {
	pt := screenPt
	_ = parent.Hwnd().ScreenToClientPt(&pt)

	var hit *serviceCard
	for _, c := range serviceCards {
		if pt.X >= c.rect.Left && pt.X < c.rect.Right &&
			pt.Y >= c.rect.Top && pt.Y < c.rect.Bottom {
			hit = c
			break
		}
	}
	if hit == nil {
		return
	}
	showVersionMenuForCard(hit, screenPt)
}

// showVersionMenuForCard is the shared popup logic used by both the
// right-click context handler and the visible "▾" button on each
// multi-version card. screenPt is where the menu pops up.
func showVersionMenuForCard(hit *serviceCard, screenPt win.POINT) {
	ms := app.services[hit.srcIdx]
	spec, ok := DownloadCatalog[ms.Conf.Name]
	if !ok || len(spec.Variants) == 0 {
		return
	}

	hMenu, err := win.CreatePopupMenu()
	if err != nil {
		return
	}
	defer hMenu.DestroyMenu()

	appendMenuItem(hMenu, co.MF_STRING|co.MF_DISABLED, 0,
		fmt.Sprintf("Switch %s version", ms.Conf.Name))
	appendMenuItem(hMenu, co.MF_SEPARATOR, 0, "")

	current := ms.Conf.ActiveVersion
	for i, v := range spec.Variants {
		flags := co.MF_STRING
		// Mark the active variant with a check.
		if v.Version == current ||
			(current == "" && strings.HasPrefix(spec.Version, v.Version)) {
			flags |= co.MF_CHECKED
		}
		appendMenuItem(hMenu, flags, uintptr(versionMenuBase+i), v.Version)
	}

	// TrackPopupMenu requires foreground for the menu to stay alive
	// past the click. Returning the chosen ID synchronously via
	// TPM_RETURNCMD avoids needing a separate WM_COMMAND handler.
	app.wnd.Hwnd().SetForegroundWindow()
	cmd, _ := hMenu.TrackPopupMenu(
		co.TPM_LEFTBUTTON|co.TPM_RIGHTBUTTON|co.TPM_RETURNCMD,
		int(screenPt.X), int(screenPt.Y), app.wnd.Hwnd())
	_ = app.wnd.Hwnd().PostMessage(co.WM_NULL, 0, 0)
	if cmd <= 0 {
		return
	}

	idx := cmd - versionMenuBase
	if idx < 0 || idx >= len(spec.Variants) {
		return
	}
	chosen := spec.Variants[idx].Version
	serviceName := ms.Conf.Name
	srcIdx := hit.srcIdx

	// Stop the service first if it's running — switching versions
	// while the binary is in use would fail to delete/repoint the
	// junction on Windows (open file handles).
	if ms.Service != nil && ms.Service.Running() {
		app.appendLog(fmt.Sprintf("[%s] stopping before version switch", serviceName))
		_ = ms.Service.Stop()
	}

	go func() {
		err := SetActiveVariant(serviceName, chosen, app.baseDir, app.appendLog, uiDownloadProgress)
		if err != nil {
			app.appendLog(fmt.Sprintf("[%s] switch %s failed: %v", serviceName, chosen, err))
			return
		}
		// Persist the choice so the version sticks across restarts.
		if app.cfg != nil && srcIdx < len(app.cfg.Services) {
			app.cfg.Services[srcIdx].ActiveVersion = chosen
			_ = SaveConfig(app.baseDir, app.cfg)
		}
		app.appendLog(fmt.Sprintf("[%s] switched to %s", serviceName, chosen))
		app.wnd.UiThread(refreshServiceList)
	}()
}

// activeWebServer reads the user's selected web server from
// app.cfg.Settings, defaulting to "Apache" when the setting is
// absent (older configs or fresh installs). Returned in canonical
// case so callers can compare against ServiceConf.Name directly.
func activeWebServer() string {
	if app.cfg != nil && app.cfg.Settings.ActiveWebServer != "" {
		return app.cfg.Settings.ActiveWebServer
	}
	return "Apache"
}

// isWebKindCard reports whether a card belongs to the web-server
// group — used to dim the inactive web server when the user picks
// the other one.
func isWebKindCard(c *serviceCard) bool {
	return groupForKind(app.services[c.srcIdx].Conf.Kind) == groupWeb
}

// serviceCards is the live grid. One entry per app.services entry.
// Built once at WM_CREATE; updated by refreshServiceList.
var serviceCards []*serviceCard

// Per-card geometry. Bumped 195→228 wide so the 3-button row
// (Start/Stop · Conf · Ver ▾) reads cleanly without truncated
// labels. 4 cards + 3 gaps + 2 padding = 228×4 + 8×3 + 10×2 = 936
// which fits in contentW (960) with a comfortable right margin.
const (
	cardW   = 228
	cardH   = 72
	cardGap = 8
)

// webServerPicker is the ComboBox at the top of the Services page
// that selects which web server is "active" — Apache or Nginx. The
// inactive one's card is dimmed and its toggle button disabled, so
// users don't accidentally start two HTTP servers competing for
// :80. Persisted to config.json on change.
var webServerPicker *ui.ComboBox

// buildServicesPage builds the page chrome (web-server picker at
// top, category section headers, card grid, bottom Start Stack
// button row) and creates one service-card widget group per entry
// in app.services. Card text values are set later by
// refreshServiceList — at build time we just create the controls.
func buildServicesPage(parent *ui.Control) {
	// Right-click anywhere on the services page → version picker
	// for the card under the cursor (only fires for services that
	// have Variants; everything else is a no-op so users don't get
	// an empty menu).
	parent.On().WmContextMenu(func(p ui.WmContextMenu) {
		showServiceContextMenu(parent, p.CursorPos())
	})

	// ----- Top strip: Active Web Server picker --------------------
	// Single inline row at the top — no big label, just a short
	// "Web:" prefix and the dropdown. Keeps the chrome out of the
	// way of the actual service grid.
	ui.NewStatic(parent, ui.OptsStatic().
		Text("Web:").
		Position(ui.Dpi(10, 8)).
		Size(ui.Dpi(28, 16)))

	webServerPicker = ui.NewComboBox(parent, ui.OptsComboBox().
		Position(ui.Dpi(40, 6)).
		Width(ui.DpiX(110)).
		Texts("Apache", "Nginx"))
	// Pre-select whichever the config says is active.
	if activeWebServer() == "Nginx" {
		webServerPicker.SelectIndex(1)
	} else {
		webServerPicker.SelectIndex(0)
	}
	// Stack action buttons live on the same top row, immediately
	// after the Web Server picker — this is what the user sees at a
	// glance, no scrolling/looking-down required.
	bx := 160
	btnY := 4
	addStackBtn := func(label string, w int, scheme ColorScheme, onClick func()) {
		b := newColoredButton(parent, ui.OptsButton().
			Text(label).
			Position(ui.Dpi(bx, btnY)).
			Width(ui.DpiX(w)).Height(ui.DpiY(26)),
			scheme)
		b.On().BnClicked(onClick)
		bx += w + 6
	}
	addStackBtn("Start Stack", 100, SchemeSuccess, func() {
		go ensureStackEssentials()
	})
	addStackBtn("Stop All", 80, SchemeDanger, func() {
		go func() {
			for _, ms := range app.services {
				if ms.Service != nil {
					_ = ms.Service.Stop()
				}
			}
		}()
	})
	addStackBtn("Restart", 80, SchemeWarning, func() {
		go func() {
			app.appendLog("restart stack: stopping all services...")
			for _, ms := range app.services {
				if ms.Service != nil {
					_ = ms.Service.Stop()
				}
			}
			killZombieChildren(app.baseDir)
			time.Sleep(1 * time.Second)
			app.appendLog("restart stack: starting essentials...")
			ensureStackEssentials()
			app.appendLog("restart stack: done")
		}()
	})

	webServerPicker.On().CbnSelChange(func() {
		choice := "Apache"
		if webServerPicker.SelectedIndex() == 1 {
			choice = "Nginx"
		}
		if app.cfg != nil {
			app.cfg.Settings.ActiveWebServer = choice
			_ = SaveConfig(app.baseDir, app.cfg)
		}
		// Stop the now-inactive web server if it's running, so
		// switching is safe. Async to avoid blocking the UI thread.
		go func(chosen string) {
			for _, ms := range app.services {
				if groupForKind(ms.Conf.Kind) != groupWeb {
					continue
				}
				if ms.Conf.Name == chosen {
					continue
				}
				if ms.Service != nil && ms.Service.Running() {
					app.appendLog(fmt.Sprintf("[%s] stopped — switching active web server to %s", ms.Conf.Name, chosen))
					_ = ms.Service.Stop()
				}
			}
			app.wnd.UiThread(refreshServiceList)
		}(choice)
		app.appendLog("active web server: " + choice)
		refreshServiceList()
	})

	// ----- Card grid (no section headers — group order is enough) ----
	// Cards flow left-to-right, top-to-bottom in category order. The
	// adjacency itself groups them visually; an explicit header row
	// per category just adds noise when the cards are already only
	// 84px tall and the icons make the type obvious.
	categoryOrder := []int32{groupWeb, groupDatabase, groupLanguage, groupTool}
	gridX := 10
	y := 40 // below the top action row (web picker + Start Stack/Stop/Restart)
	pos := 0
	for _, wantGroup := range categoryOrder {
		for srcIdx, ms := range app.services {
			if groupForKind(ms.Conf.Kind) != wantGroup {
				continue
			}
			col := pos % 4
			row := pos / 4
			x := gridX + col*(cardW+cardGap)
			cy := y + row*(cardH+cardGap)
			card := buildServiceCard(parent, x, cy, srcIdx, ms)
			serviceCards = append(serviceCards, card)
			pos++
		}
	}
	_ = pos
	// (Bottom action buttons moved to the top row alongside the
	// Active Web Server picker — see addStackBtn above.)
}

// buildServiceCard creates the widgets for one service tile at the
// given top-left coordinates. Layout:
//
//   ┌────────────────────────────────────────────┐
//   │ [icon] [●] Service Name                     │  (icon + status dot + name)
//   │            Status                           │
//   │            Version                          │
//   │ [Start/Stop] [Restart] [Conf]               │  (3 buttons)
//   └────────────────────────────────────────────┘
//
// The Start/Stop button is a single toggle whose label and colour
// flip based on Service.Running() — green "▶ Start" when stopped,
// red "■ Stop" when running. This is what the user asked for: one
// button so the running indicator is unambiguous (green = action
// available to start; red = action available to stop).
func buildServiceCard(parent *ui.Control, x, y, srcIdx int, ms *ManagedService) *serviceCard {
	c := &serviceCard{
		srcIdx: srcIdx,
		rect: win.RECT{
			Left: int32(x), Top: int32(y),
			Right: int32(x + cardW), Bottom: int32(y + cardH),
		},
	}

	// Background border — a Static styled SS_ETCHEDFRAME draws a
	// thin sunken outline so the cards visually separate from the
	// page background. Sized to enclose all the inner widgets.
	ui.NewStatic(parent, ui.OptsStatic().
		Text("").
		Position(ui.Dpi(x, y)).
		Size(ui.Dpi(cardW, cardH)).
		CtrlStyle(co.SS_ETCHEDFRAME))

	// Icon — SS_ICON Static. STM_SETICON gets the actual HICON
	// pointer in installServiceIcons (which already loads icons
	// into HIMAGELISTs; we extract the HICON via ImageList_GetIcon
	// in setCardIcon below).
	c.iconStatic = ui.NewStatic(parent, ui.OptsStatic().
		Text("").
		Position(ui.Dpi(x+8, y+8)).
		Size(ui.Dpi(32, 32)).
		CtrlStyle(co.SS_ICON|co.SS_REALSIZECONTROL))

	// Status dot — sits left of the name. Glyph swaps reflect state
	// (●/○/·); colour comes from the global static text rule.
	c.statusDot = ui.NewStatic(parent, ui.OptsStatic().
		Text("●").
		Position(ui.Dpi(x+44, y+10)).
		Size(ui.Dpi(12, 14)))

	// Name — the dominant text on the card.
	c.nameStatic = ui.NewStatic(parent, ui.OptsStatic().
		Text(ms.Conf.Name).
		Position(ui.Dpi(x+58, y+10)).
		Size(ui.Dpi(cardW-66, 16)))

	// Status — single short line; merges port info, no separate
	// version row (we tuck the version into the name on refresh).
	c.statusLbl = ui.NewStatic(parent, ui.OptsStatic().
		Text("...").
		Position(ui.Dpi(x+48, y+28)).
		Size(ui.Dpi(cardW-56, 14)))

	// versionLbl is unused on the simplified card but kept on the
	// struct so refreshServiceList doesn't need a separate code
	// path for old vs new layout. Hidden by setting empty text +
	// zero-height position (off the visible area).
	c.versionLbl = ui.NewStatic(parent, ui.OptsStatic().
		Text("").
		Position(ui.Dpi(x+48, y+cardH)).
		Size(ui.Dpi(1, 1)))

	// Button row — just two buttons now: Start/Stop toggle and
	// Conf. Restart is redundant with Stop+Start and was clutter.
	btnRowY := y + cardH - 28
	idx := srcIdx

	c.btnToggle = newColoredButton(parent, ui.OptsButton().
		Text("▶ Start").
		Position(ui.Dpi(x+8, btnRowY)).
		Width(ui.DpiX(96)).Height(ui.DpiY(22)),
		SchemeSuccess)
	c.btnToggle.On().BnClicked(func() {
		ms := app.services[idx]
		if groupForKind(ms.Conf.Kind) == groupWeb && ms.Conf.Name != activeWebServer() {
			app.appendLog(fmt.Sprintf(
				"[%s] not the active web server — set Active Web Server to %s first",
				ms.Conf.Name, ms.Conf.Name))
			return
		}
		if s := ms.Service; s != nil && s.Running() {
			_ = s.Stop()
			return
		}
		startService(idx)
	})

	// btnRestart kept on the struct (refreshServiceList still
	// references it on legacy cards) but rendered off-card so the
	// simplified layout doesn't show it. Setting size 0×0 + position
	// outside the card is the cheapest way to "remove" without
	// rippling through every refresh path.
	c.btnRestart = newColoredButton(parent, ui.OptsButton().
		Text("").
		Position(ui.Dpi(x+cardW+100, y+cardH+100)).
		Width(ui.DpiX(1)).Height(ui.DpiY(1)),
		SchemeWarning)
	c.btnRestart.On().BnClicked(func() {
		s := app.services[idx].Service
		if s == nil {
			return
		}
		go func(svc *Service) {
			_ = svc.Stop()
			time.Sleep(500 * time.Millisecond)
			if err := svc.Start(); err != nil {
				app.appendLog(fmt.Sprintf("[%s] restart: %v", svc.Name, err))
			}
		}(s)
	})

	// Layout split depending on whether this service has multiple
	// versions in the catalogue. Multi-version cards get a visible
	// "Version ▾" button taking the place of width on the right.
	hasVariants := false
	if spec, ok := DownloadCatalog[ms.Conf.Name]; ok && len(spec.Variants) > 0 {
		hasVariants = true
	}

	if hasVariants {
		// 3 buttons in a 228-wide card:
		//   toggle (96) + Conf (52) + Ver ▾ (60)
		//   8 padding + 96 + 6 gap + 52 + 6 gap + 60 + 8 padding = 236
		//   (cards have a slight overflow margin since gaps are 6 not 8 here)
		// Resize the toggle button (originally created at width 96 in
		// the buildServiceCard prologue) — same width here, just keep
		// it explicit so a future card-width tweak only needs to touch
		// this block.
		px, py := ui.Dpi(x+8, btnRowY)
		c.btnToggle.Hwnd().SetWindowPos(win.HWND(0),
			win.POINT{X: int32(px), Y: int32(py)},
			win.SIZE{Cx: int32(ui.DpiX(96)), Cy: int32(ui.DpiY(22))},
			co.SWP_NOZORDER)
		c.btnConf = newColoredButton(parent, ui.OptsButton().
			Text("Conf").
			Position(ui.Dpi(x+110, btnRowY)).
			Width(ui.DpiX(52)).Height(ui.DpiY(22)),
			SchemePrimary)
		card := c
		c.btnVer = newColoredButton(parent, ui.OptsButton().
			Text("Ver ▾").
			Position(ui.Dpi(x+168, btnRowY)).
			Width(ui.DpiX(52)).Height(ui.DpiY(22)),
			SchemeWarning)
		c.btnVer.On().BnClicked(func() {
			rc, err := card.btnVer.Hwnd().GetWindowRect()
			if err != nil {
				return
			}
			pt := win.POINT{X: rc.Left, Y: rc.Bottom}
			showVersionMenuForCard(card, pt)
		})
	} else {
		// Single-version services: just toggle + Conf with comfy
		// widths in the wider card.
		// 8 padding + 130 + 8 gap + 74 + 8 padding = 228 ✓
		px, py := ui.Dpi(x+8, btnRowY)
		c.btnToggle.Hwnd().SetWindowPos(win.HWND(0),
			win.POINT{X: int32(px), Y: int32(py)},
			win.SIZE{Cx: int32(ui.DpiX(130)), Cy: int32(ui.DpiY(22))},
			co.SWP_NOZORDER)
		c.btnConf = newColoredButton(parent, ui.OptsButton().
			Text("Conf").
			Position(ui.Dpi(x+146, btnRowY)).
			Width(ui.DpiX(74)).Height(ui.DpiY(22)),
			SchemePrimary)
	}
	c.btnConf.On().BnClicked(func() {
		ms := app.services[idx]
		if ms.Conf.ConfigFile == "" {
			// Fall back to "Open URL" for tool-only entries
			// (phpMyAdmin/Adminer) so the Conf button isn't
			// useless on those.
			if ms.Conf.OpenURL != "" {
				openPath(ms.Conf.OpenURL)
				return
			}
			app.appendLog(fmt.Sprintf("[%s] no config file", ms.Conf.Name))
			return
		}
		showPage("editor")
		loadFileIntoEditor(ExpandPath(ms.Conf.ConfigFile, app.baseDir))
	})

	return c
}

// refreshServiceList walks every card and re-syncs its labels +
// button state with the live ManagedService. Called on initial
// startup and whenever a service's state callback fires.
func refreshServiceList() {
	if len(serviceCards) == 0 {
		return
	}
	active := activeWebServer()
	for _, c := range serviceCards {
		ms := app.services[c.srcIdx]
		running := ms.Service != nil && ms.Service.Running()
		installed := IsInstalled(ms.Conf.Name, app.baseDir) || ms.Service == nil

		// Status text — short, no leading dot (we have the dot widget
		// for that).
		status := "Stopped"
		if !ms.Conf.Enabled {
			status = "Disabled"
		}
		if ms.Service == nil {
			if IsInstalled(ms.Conf.Name, app.baseDir) {
				status = "Installed (tool)"
			} else {
				status = "Not installed"
			}
		} else if running {
			status = "Running  pid " + fmt.Sprint(ms.Service.PID())
		}
		if ms.Conf.Port > 0 {
			status += "  :" + fmt.Sprint(ms.Conf.Port)
		}
		// Web server cards that aren't the active choice get a
		// clear hint so the user knows why their toggle is muted.
		if isWebKindCard(c) && ms.Conf.Name != active {
			status = "Inactive — pick " + ms.Conf.Name + " up top"
		}
		c.statusLbl.Hwnd().SetWindowText(status)

		// Status dot — green running, red stopped, grey not
		// installed / inactive web server. The actual colour comes
		// from CTLCOLORSTATIC, which we don't intercept per-widget,
		// so for now we use unicode glyph swaps as a colour proxy
		// that survives without owner-draw plumbing:
		//   ●  filled    — running (or pickable)
		//   ○  hollow    — stopped / not running
		//   ·  small dot — inactive / not installed
		dot := "○"
		switch {
		case isWebKindCard(c) && ms.Conf.Name != active:
			dot = "·"
		case !installed:
			dot = "·"
		case running:
			dot = "●"
		}
		c.statusDot.Hwnd().SetWindowText(dot)

		// Toggle button — label + scheme flip based on running state.
		// Inactive web server gets greyed out (Start label, but no
		// click effect since the handler refuses).
		switch {
		case isWebKindCard(c) && ms.Conf.Name != active:
			c.btnToggle.Hwnd().SetWindowText("▶ Start")
			c.btnToggle.Hwnd().EnableWindow(false)
		case running:
			c.btnToggle.Hwnd().SetWindowText("■ Stop")
			c.btnToggle.Hwnd().EnableWindow(true)
		default:
			c.btnToggle.Hwnd().SetWindowText("▶ Start")
			c.btnToggle.Hwnd().EnableWindow(true)
		}

		// Merge the version into the name line so the simplified
		// card doesn't need a separate version row. Multi-version
		// services show the active variant from config; otherwise
		// fall back to the catalogue's flat Version label.
		nameLine := ms.Conf.Name
		if spec, ok := DownloadCatalog[ms.Conf.Name]; ok {
			short := spec.Version
			if len(spec.Variants) > 0 {
				active := ""
				for _, v := range spec.Variants {
					if v.Version == short ||
						strings.HasPrefix(short, v.Version) {
						active = v.Version
					}
				}
				if active != "" {
					short = active
				}
			}
			if short != "" {
				nameLine = ms.Conf.Name + "  " + short
			}
		}
		c.nameStatic.Hwnd().SetWindowText(nameLine)
		c.versionLbl.Hwnd().SetWindowText("")

		// Drop the icon onto the SS_ICON Static. Done via the
		// per-name HICON map populated by installServiceIcons.
		setCardIcon(c, ms.Conf.Name)
	}
}

// categoryLabel returns the human-readable category name for a
// group ID. Kept in one place so the Services column values match
// whatever headers we might later re-introduce.

// categoryLabel returns the human-readable category name for a
// group ID. Kept in one place so the Services column values match
// whatever headers we might later re-introduce.
func categoryLabel(groupID int32) string {
	switch groupID {
	case groupWeb:
		return "Web Server"
	case groupLanguage:
		return "Language"
	case groupDatabase:
		return "Database / Cache"
	default:
		return "Admin Tool"
	}
}

// ----- Editor page -------------------------------------------------------

// buildEditorPage creates the built-in text editor view: a dropdown of
// every known config file in the project, a big multi-line edit control,
// and Save / Reload buttons. No notepad.exe — files are edited in-place.
func buildEditorPage(parent *ui.Control) {
	ui.NewStatic(parent, ui.OptsStatic().
		Text("File:").
		Position(ui.Dpi(10, 34)))
	edDropdown = ui.NewComboBox(parent, ui.OptsComboBox().
		Position(ui.Dpi(50, 30)).
		Width(ui.DpiX(360)))
	// When a user picks a file from the dropdown, load it immediately.
	edDropdown.On().CbnSelChange(func() {
		idx := edDropdown.SelectedIndex()
		if idx < 0 || idx >= len(edKnownPaths) {
			return
		}
		loadFileIntoEditor(edKnownPaths[idx])
	})

	// Only Save + Reload here. "Open in Explorer" and "Refresh List"
	// were clutter — users can refresh via the file dropdown, and the
	// editor is the point of this page so Explorer is redundant.
	newColoredButton(parent, ui.OptsButton().
		Text("Save").
		Position(ui.Dpi(420, 28)).
		Width(ui.DpiX(84)).Height(ui.DpiY(26)),
		SchemeSuccess).
		On().BnClicked(func() {
		saveEditorBuffer()
	})
	newColoredButton(parent, ui.OptsButton().
		Text("Reload").
		Position(ui.Dpi(512, 28)).
		Width(ui.DpiX(84)).Height(ui.DpiY(26)),
		SchemeNeutral).
		On().BnClicked(func() {
		idx := edDropdown.SelectedIndex()
		if idx >= 0 && idx < len(edKnownPaths) {
			loadFileIntoEditor(edKnownPaths[idx])
		}
	})

	// Path label (tells the user exactly which file they're editing).
	ui.NewStatic(parent, ui.OptsStatic().
		Text("Path:").
		Position(ui.Dpi(10, 62)))
	edPathLabel = ui.NewStatic(parent, ui.OptsStatic().
		Text("(no file loaded)").
		Position(ui.Dpi(50, 62)).
		Size(ui.Dpi(contentW-60, 16)))

	// The editor itself — multi-line, vertical+horizontal scroll.
	edContent = ui.NewEdit(parent, ui.OptsEdit().
		Position(ui.Dpi(10, 86)).
		Width(ui.DpiX(contentW-20)).
		Height(ui.DpiY(contentH-100)).
		CtrlStyle(co.ES_MULTILINE|co.ES_AUTOVSCROLL|co.ES_AUTOHSCROLL|co.ES_WANTRETURN|co.ES_NOHIDESEL).
		WndStyle(co.WS_CHILD|co.WS_VISIBLE|co.WS_TABSTOP|co.WS_VSCROLL|co.WS_HSCROLL|co.WS_BORDER))

	// Initial dropdown population happens in main.go WmCreate (after the
	// combo box has an actual HWND to add items to).
}

// populateEditorDropdown fills the combo box with every known config
// file that currently exists on disk, plus a few always-useful entries
// (config.json, hosts file). Called from WmCreate and the "Refresh List"
// button.
func populateEditorDropdown() {
	if edDropdown == nil {
		return
	}
	edDropdown.DeleteAllItems()
	edKnownPaths = nil

	add := func(label, path string) {
		if path == "" {
			return
		}
		if _, err := os.Stat(path); err != nil {
			return
		}
		edDropdown.AddItem(fmt.Sprintf("%s  —  %s", label, truncateMid(path, 60)))
		edKnownPaths = append(edKnownPaths, path)
	}

	// 1. Every service config file that exists
	for _, ms := range app.services {
		if ms.Conf.ConfigFile == "" {
			continue
		}
		add(ms.Conf.Name, ExpandPath(ms.Conf.ConfigFile, app.baseDir))
	}
	// 2. GoAMPP's own config
	add("GoAMPP config", filepath.Join(app.baseDir, "config.json"))
	// 3. Apache/Nginx generated vhost includes (if present)
	add("Apache vhosts", ExpandPath(app.cfg.Settings.ApacheVhostsInclude, app.baseDir))
	// 4. Windows hosts file (edit needs admin — we just open read-only to
	// the user first; they can try Save, which will return the access
	// error so they know to relaunch elevated).
	add("Windows hosts", DefaultHostsFile())

	if len(edKnownPaths) > 0 {
		edDropdown.SelectIndex(0)
		loadFileIntoEditor(edKnownPaths[0])
	}
}

// loadFileIntoEditor reads a file's contents into the editor buffer and
// updates the path label. Silently no-ops if the editor isn't built yet.
func loadFileIntoEditor(path string) {
	if edContent == nil || edPathLabel == nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		app.appendLog(fmt.Sprintf("editor: %v", err))
		edContent.SetText("")
		edPathLabel.Hwnd().SetWindowText("(failed to read: " + path + ")")
		return
	}
	// Windows Edit controls expect CRLF for newlines. Upgrade bare LFs
	// so files that came from a Linux checkout display as one-line-per-
	// logical-line instead of a single squashed glob.
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\n", "\r\n")
	edContent.SetText(text)
	edPathLabel.Hwnd().SetWindowText(path)

	// Also select the matching entry in the dropdown so the UI stays
	// consistent when loadFileIntoEditor is called from elsewhere
	// (e.g. the Services tab "Edit Config" button).
	for i, p := range edKnownPaths {
		if p == path {
			edDropdown.SelectIndex(i)
			break
		}
	}
	app.appendLog(fmt.Sprintf("editor: loaded %s", path))
}

// saveEditorBuffer writes the editor's current text back to whichever
// file the path label points to. An atomic temp-file rename keeps a
// half-written buffer from clobbering a working config.
func saveEditorBuffer() {
	if edContent == nil || edPathLabel == nil {
		return
	}
	path := strings.TrimSpace(edPathLabel.Text())
	if path == "" || strings.HasPrefix(path, "(") {
		app.appendLog("editor: no file loaded")
		return
	}
	data := []byte(edContent.Text())
	// The Edit control already emits CRLF. Don't translate — config files
	// on Windows expect CRLF anyway.
	if err := atomicWrite(path, data); err != nil {
		app.appendLog(fmt.Sprintf("editor save: %v", err))
		return
	}
	app.appendLog(fmt.Sprintf("editor: saved %s (%d bytes)", path, len(data)))
}

// ----- Vhosts page -------------------------------------------------------

// buildVhostsPage draws the virtual host manager: list of configured
// domains, inline add/edit form, and Apply button that writes to the
// hosts file + Apache/Nginx vhost includes.
func buildVhostsPage(parent *ui.Control) {
	vhostList = ui.NewListView(parent, ui.OptsListView().
		Position(ui.Dpi(10, 28)).
		Size(ui.Dpi(contentW-20, 160)).
		CtrlStyle(co.LVS_REPORT|co.LVS_SHOWSELALWAYS|co.LVS_SINGLESEL).
		CtrlExStyle(co.LVS_EX_FULLROWSELECT|co.LVS_EX_DOUBLEBUFFER|co.LVS_EX_GRIDLINES).
		Column("", ui.DpiX(30)).
		Column("Domain", ui.DpiX(180)).
		Column("Document Root", ui.DpiX(360)).
		Column("Port", ui.DpiX(60)).
		Column("Server", ui.DpiX(90)))
	vhostList.On().LvnItemChanged(func(p *win.NMLISTVIEW) {
		// Guard everything in a recover so a panic in the click
		// handler can't propagate up into LVM_SETITEMTEXT and make
		// the next AddItem look like it failed.
		defer func() {
			if r := recover(); r != nil {
				app.appendLog(fmt.Sprintf("vhost row select: %v", r))
			}
		}()
		if p.UChanged&co.LVIF_STATE == 0 {
			return
		}
		// Only react when the row was *selected* — without the
		// state mask check we get fired during AddItem too, which
		// then tries to populate the form from a half-built vhost
		// and ends up reading garbage indices.
		if p.UNewState&0x0002 == 0 { // LVIS_SELECTED
			return
		}
		idx := int(p.IItem)
		if idx < 0 || idx >= len(app.cfg.Vhosts) {
			return
		}
		// All form widgets must be created before we can poke them.
		// During the very first AddItem (called from WmCreate),
		// vhostDomainNm/Ext might still be nil because they're
		// constructed AFTER vhostList in build order — bail out
		// silently and let the user re-select once everything is up.
		if vhostDomainNm == nil || vhostDomainExt == nil ||
			vhostDocRoot == nil || vhostPort == nil || vhostServer == nil {
			return
		}
		v := app.cfg.Vhosts[idx]
		// Split the saved domain into name + ext for the form. The
		// split is on the LAST dot so multi-dot names like
		// "api.myapp.test" still work — name="api.myapp", ext=".test".
		name, ext := splitDomain(v.Domain)
		vhostDomainNm.SetText(name)
		setDomainExtSelection(vhostDomainExt, ext)
		vhostDocRoot.SetText(v.DocRoot)
		port := v.Port
		if port == 0 {
			port = 80
		}
		vhostPort.SetText(fmt.Sprint(port))
		switch v.ServerType {
		case "nginx":
			vhostServer.SelectIndex(1)
		case "both":
			vhostServer.SelectIndex(2)
		default:
			vhostServer.SelectIndex(0)
		}
	})
	// (refreshVhostList() is called from main.go's WmCreate handler once
	// the ListView's HWND actually exists.)

	// Inline form. Domain is split into a name input + an extension
	// dropdown so users pick from the curated TLD list rather than
	// typing free-form (and accidentally hitting HSTS-preloaded TLDs
	// like .dev).
	formY := 200
	ui.NewStatic(parent, ui.OptsStatic().
		Text("Domain").
		Position(ui.Dpi(10, formY+4)))
	vhostDomainNm = ui.NewEdit(parent, ui.OptsEdit().
		Position(ui.Dpi(72, formY)).
		Width(ui.DpiX(140)))
	vhostDomainExt = ui.NewComboBox(parent, ui.OptsComboBox().
		Position(ui.Dpi(216, formY)).
		Width(ui.DpiX(80)).
		Texts(domainExtensions...).
		Select(0))

	ui.NewStatic(parent, ui.OptsStatic().
		Text("Port").
		Position(ui.Dpi(310, formY+4)))
	vhostPort = ui.NewEdit(parent, ui.OptsEdit().
		Text("80").
		Position(ui.Dpi(345, formY)).
		Width(ui.DpiX(60)))

	ui.NewStatic(parent, ui.OptsStatic().
		Text("Server").
		Position(ui.Dpi(420, formY+4)))
	vhostServer = ui.NewComboBox(parent, ui.OptsComboBox().
		Position(ui.Dpi(470, formY)).
		Width(ui.DpiX(100)).
		Texts("apache", "nginx", "both").
		Select(0))

	formY2 := formY + 30
	ui.NewStatic(parent, ui.OptsStatic().
		Text("DocRoot").
		Position(ui.Dpi(10, formY2+4)))
	vhostDocRoot = ui.NewEdit(parent, ui.OptsEdit().
		Position(ui.Dpi(72, formY2)).
		Width(ui.DpiX(contentW-100)))

	// Action buttons
	btnY := formY2 + 34
	x := 10
	addBtn := func(label string, w int, scheme ColorScheme, onClick func()) {
		b := newColoredButton(parent, ui.OptsButton().
			Text(label).
			Position(ui.Dpi(x, btnY)).
			Width(ui.DpiX(w)).Height(ui.DpiY(26)),
			scheme)
		b.On().BnClicked(onClick)
		x += w + 6
	}
	// Three buttons is enough: Save handles both add (no selection) and
	// update (selection). Delete removes the selected row. Apply writes
	// to the hosts file + Apache/Nginx configs on disk.
	addBtn("Save", 90, SchemeSuccess, func() {
		v, err := readVhostForm()
		if err != nil {
			app.appendLog("vhost save: " + err.Error())
			return
		}
		if idx := selectedVhostIndex(); idx >= 0 {
			// Preserve enabled flag from the existing row.
			v.Enabled = app.cfg.Vhosts[idx].Enabled
			app.cfg.Vhosts[idx] = v
		} else {
			v.Enabled = true
			app.cfg.Vhosts = append(app.cfg.Vhosts, v)
		}
		_ = SaveConfig(app.baseDir, app.cfg)
		refreshVhostList()
	})
	addBtn("Delete", 90, SchemeDanger, func() {
		idx := selectedVhostIndex()
		if idx < 0 {
			return
		}
		app.cfg.Vhosts = append(app.cfg.Vhosts[:idx], app.cfg.Vhosts[idx+1:]...)
		_ = SaveConfig(app.baseDir, app.cfg)
		refreshVhostList()
	})
	addBtn("Apply to System", 140, SchemePrimary, func() {
		if err := ApplyVhosts(app.baseDir, app.cfg); err != nil {
			app.appendLog("apply vhosts: " + err.Error())
			app.appendLog("  → if 'access denied', relaunch GoAMPP as administrator")
			return
		}
		app.appendLog("vhosts applied — hosts file + Apache/Nginx configs updated")
	})
}

// refreshVhostList rebuilds the vhost ListView from app.cfg.Vhosts.
func refreshVhostList() {
	if vhostList == nil {
		return
	}
	vhostList.DeleteAllItems()
	for _, v := range app.cfg.Vhosts {
		mark := " "
		if v.Enabled {
			mark = "✓"
		}
		port := fmt.Sprint(v.Port)
		if v.Port == 0 {
			port = "80"
		}
		server := v.ServerType
		if server == "" {
			server = "apache"
		}
		vhostList.AddItem(mark, v.Domain, ExpandPath(v.DocRoot, app.baseDir), port, server)
	}
}

// ----- Settings page -----------------------------------------------------

// buildSettingsPage shows an overview of the runtime paths plus a small
// "dashboard" strip with global shortcuts.
func buildSettingsPage(parent *ui.Control) {
	y := 10
	section := func(title string) {
		ui.NewStatic(parent, ui.OptsStatic().
			Text(title).
			Position(ui.Dpi(10, y)).
			Size(ui.Dpi(contentW-20, 16)))
		// Thin separator line under the section header.
		ui.NewStatic(parent, ui.OptsStatic().
			Text("").
			Position(ui.Dpi(10, y+18)).
			Size(ui.Dpi(contentW-20, 1)).
			CtrlStyle(co.SS_ETCHEDHORZ))
		y += 26
	}
	row := func(label, value string) {
		ui.NewStatic(parent, ui.OptsStatic().
			Text(label).
			Position(ui.Dpi(20, y)).
			Size(ui.Dpi(150, 16)))
		ui.NewStatic(parent, ui.OptsStatic().
			Text(value).
			Position(ui.Dpi(170, y)).
			Size(ui.Dpi(contentW-180, 16)))
		y += 18
	}

	// ----- Paths --------------------------------------------------
	section("PATHS")
	row("Install dir", app.baseDir)
	row("Config", filepath.Join(app.baseDir, "config.json"))
	row("Apache vhosts", ExpandPath(app.cfg.Settings.ApacheVhostsInclude, app.baseDir))
	row("Nginx sites", ExpandPath(app.cfg.Settings.NginxSitesDir, app.baseDir))
	row("Hosts file", DefaultHostsFile())
	row("Downloads cache", filepath.Join(app.baseDir, "downloads"))

	// ----- State --------------------------------------------------
	y += 8
	section("STATE")
	row("Services", fmt.Sprintf("%d total · %d enabled",
		len(app.cfg.Services), countEnabledServices()))
	row("Vhosts", fmt.Sprintf("%d", len(app.cfg.Vhosts)))
	row("Active web server", activeWebServer())
	autoStartState := "off"
	if isAutoStartEnabled() {
		autoStartState = "on — launches into tray on login"
	}
	row("Auto-start on boot", autoStartState)
	elev := "no — vhost Apply needs admin"
	if IsElevated() {
		elev = "yes (administrator)"
	}
	row("Running elevated", elev)

	// ----- Actions ------------------------------------------------
	y += 12
	section("ACTIONS")

	// Buttons in a tidy 4-column grid. Each column is 180px wide,
	// each button 170px, gap 10. Two rows max so nothing wraps off
	// the page.
	const (
		btnW = 170
		btnH = 28
		gapX = 10
		gapY = 8
		col0 = 20
	)
	col := 0
	rowY := y
	addBtn := func(label string, scheme ColorScheme, onClick func()) {
		x := col0 + col*(btnW+gapX)
		b := newColoredButton(parent, ui.OptsButton().
			Text(label).
			Position(ui.Dpi(x, rowY)).
			Width(ui.DpiX(btnW)).Height(ui.DpiY(btnH)),
			scheme)
		b.On().BnClicked(onClick)
		col++
		if col >= 4 {
			col = 0
			rowY += btnH + gapY
		}
	}

	addBtn("Edit config", SchemePrimary, func() {
		showPage("editor")
		loadFileIntoEditor(filepath.Join(app.baseDir, "config.json"))
	})
	addBtn("Reload config", SchemeNeutral, func() {
		if err := reloadConfig(); err != nil {
			app.appendLog("reload: " + err.Error())
			return
		}
		refreshServiceList()
		refreshVhostList()
		populateEditorDropdown()
		app.appendLog("config reloaded")
	})
	addBtn("Toggle Auto-start", SchemePrimary, func() {
		toggleAutoStart()
	})
	if !IsElevated() {
		addBtn("Restart as Admin", SchemeWarning, func() {
			if err := RelaunchElevated(); err != nil {
				app.appendLog("elevate: " + err.Error())
				return
			}
			app.appendLog("relaunching as administrator — this instance will exit")
			time.Sleep(200 * time.Millisecond)
			quitApp(app.wnd)
		})
	}
	addBtn("Add tools to PATH", SchemeSuccess, func() {
		n, err := AddGoamppToUserPath()
		if err != nil {
			app.appendLog("path: " + err.Error())
			return
		}
		if n == 0 {
			app.appendLog("path: already on PATH (no changes)")
			return
		}
		app.appendLog(fmt.Sprintf("path: added %d goampp bin dirs to user PATH", n))
		app.appendLog("path: open a NEW terminal to use them")
	})
	addBtn("Remove from PATH", SchemeWarning, func() {
		n, err := RemoveGoamppFromUserPath()
		if err != nil {
			app.appendLog("path: " + err.Error())
			return
		}
		if n == 0 {
			app.appendLog("path: nothing to remove")
			return
		}
		app.appendLog(fmt.Sprintf("path: removed %d goampp entries from user PATH", n))
	})
	// Postgres console — opens a cmd window with bin/pgsql/bin/ on
	// PATH so the user can run psql / pg_dump / createdb without
	// fishing for the path. Only useful if Postgres is installed,
	// but harmless when it's not (cmd just opens, missing exe at
	// most prints "not recognized" on first command).
	addBtn("Open psql Console", SchemePrimary, func() {
		pgBin := filepath.Join(app.baseDir, "bin", "pgsql", "bin")
		if _, err := os.Stat(filepath.Join(pgBin, "psql.exe")); err != nil {
			app.appendLog("psql: PostgreSQL not installed — install it from the Services tab first")
			return
		}
		// Launch a detached cmd window with PATH primed and a friendly
		// banner. /K keeps the prompt open after psql exits.
		cmd := exec.Command("cmd", "/c", "start", "", "cmd", "/K",
			"set PATH="+pgBin+";%PATH% && cd /d "+app.baseDir+
				" && echo PostgreSQL console — try: psql -U postgres")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: false}
		_ = cmd.Start()
	})
	addBtn("Quit GoAMPP", SchemeDanger, func() {
		quitApp(app.wnd)
	})
}

// ----- Projects page ----------------------------------------------------

// buildProjectsPage draws the scaffold UI: framework picker + project
// name input + domain + Create button, plus a ListView of existing
// projects pulled from app.cfg.Projects.
func buildProjectsPage(parent *ui.Control) {
	// Framework picker row.
	ui.NewStatic(parent, ui.OptsStatic().
		Text("Framework:").
		Position(ui.Dpi(10, 34)))

	// Build the framework name list at construction time and pass it
	// via .Texts(...) — windigo only actually creates the underlying
	// HWND on WM_CREATE, so AddItem() calls in build code are silent
	// no-ops (they SendMessage to a null hwnd). Texts() bakes the
	// list into the create-time options so the items appear.
	frameworkNames := make([]string, len(Frameworks))
	for i, f := range Frameworks {
		frameworkNames[i] = f.Name
	}
	projFramework = ui.NewComboBox(parent, ui.OptsComboBox().
		Position(ui.Dpi(90, 30)).
		Width(ui.DpiX(200)).
		Texts(frameworkNames...).
		Select(0))

	// Project name. Slug-friendly — we strip non-alphanumerics in
	// onCreateProject so the user can type anything here.
	ui.NewStatic(parent, ui.OptsStatic().
		Text("Name:").
		Position(ui.Dpi(305, 34)))
	projName = ui.NewEdit(parent, ui.OptsEdit().
		Position(ui.Dpi(345, 30)).
		Width(ui.DpiX(110)))

	// Domain: split into a free-form name input + a dropdown of
	// dev-friendly TLDs (.test by default). The full domain we
	// register is `name + ext`, e.g. "myapp" + ".test" = "myapp.test".
	ui.NewStatic(parent, ui.OptsStatic().
		Text("Domain:").
		Position(ui.Dpi(465, 34)))
	projDomainNm = ui.NewEdit(parent, ui.OptsEdit().
		Position(ui.Dpi(515, 30)).
		Width(ui.DpiX(100)))
	projDomainExt = ui.NewComboBox(parent, ui.OptsComboBox().
		Position(ui.Dpi(617, 30)).
		Width(ui.DpiX(80)).
		Texts(domainExtensions...).
		Select(0))

	// Create button — the whole point of this page.
	newColoredButton(parent, ui.OptsButton().
		Text("Create Project").
		Position(ui.Dpi(705, 28)).
		Width(ui.DpiX(95)).Height(ui.DpiY(26)),
		SchemeSuccess).
		On().BnClicked(func() {
		onCreateProject()
	})

	// Runtime status row — tells the user what's available on their
	// system and what the scaffolder will be able to use.
	runtimeY := 66
	ui.NewStatic(parent, ui.OptsStatic().
		Text("Runtime status: "+runtimeStatusText()).
		Position(ui.Dpi(10, runtimeY)).
		Size(ui.Dpi(contentW-20, 16)))

	// Existing-projects list.
	ui.NewStatic(parent, ui.OptsStatic().
		Text("Existing projects (drops in www/):").
		Position(ui.Dpi(10, runtimeY+24)))
	projList = ui.NewListView(parent, ui.OptsListView().
		Position(ui.Dpi(10, runtimeY+44)).
		Size(ui.Dpi(contentW-20, 170)).
		CtrlStyle(co.LVS_REPORT|co.LVS_SHOWSELALWAYS|co.LVS_SINGLESEL).
		CtrlExStyle(co.LVS_EX_FULLROWSELECT|co.LVS_EX_DOUBLEBUFFER|co.LVS_EX_GRIDLINES).
		Column("Name", ui.DpiX(120)).
		Column("Framework", ui.DpiX(140)).
		Column("Domain", ui.DpiX(160)).
		Column("Document Root", ui.DpiX(340)))

	// Actions for the selected project.
	btnY := runtimeY + 44 + 178
	x := 10
	addBtn := func(label string, w int, scheme ColorScheme, onClick func()) {
		b := newColoredButton(parent, ui.OptsButton().
			Text(label).
			Position(ui.Dpi(x, btnY)).
			Width(ui.DpiX(w)).Height(ui.DpiY(26)),
			scheme)
		b.On().BnClicked(onClick)
		x += w + 6
	}
	addBtn("Open in Browser", 140, SchemePrimary, func() {
		idx := selectedProjectIndex()
		if idx < 0 {
			return
		}
		openPath("http://" + app.cfg.Projects[idx].Domain)
	})
	addBtn("Open Folder", 110, SchemeNeutral, func() {
		idx := selectedProjectIndex()
		if idx < 0 {
			return
		}
		openFolder(filepath.Join(app.baseDir, "www", app.cfg.Projects[idx].Name))
	})
	addBtn("Delete", 90, SchemeDanger, func() {
		idx := selectedProjectIndex()
		if idx < 0 {
			return
		}
		deleteProject(idx)
	})
}

// runtimeStatusText produces a one-liner showing which framework
// runtimes are available. Used at the top of the Projects page so
// users know what they can scaffold without guessing.
func runtimeStatusText() string {
	var parts []string
	check := func(label, tool string) {
		if hasTool(tool) {
			parts = append(parts, "✓ "+label)
		} else {
			parts = append(parts, "○ "+label)
		}
	}
	check("PHP", "php")
	check("Composer", "composer")
	check("Node.js", "node")
	check("Python", "python")
	check("Java", "java")
	check("Go", "go")
	return strings.Join(parts, "   ")
}

// onCreateProject is wired to the "Create Project" button. Reads
// the form fields, sanitises them, then runs createProject off the
// UI thread so the scaffold doesn't freeze the window.
func onCreateProject() {
	fname := projFramework.CurrentText()
	if fname == "" {
		app.appendLog("projects: pick a framework first")
		return
	}
	f := frameworkByName(fname)
	if f == nil {
		app.appendLog("projects: unknown framework " + fname)
		return
	}
	name := strings.TrimSpace(projName.Text())
	if name == "" {
		app.appendLog("projects: project name required")
		return
	}
	// Slugify: keep alphanumerics, replace anything else with "-".
	name = slugify(name)

	// Domain: optional name override + required extension. If the
	// name field is empty we default to the project slug, so the
	// usual case "type project name → click Create" still works.
	domainName := strings.TrimSpace(projDomainNm.Text())
	if domainName == "" {
		domainName = name
	}
	ext := projDomainExt.CurrentText()
	if ext == "" {
		ext = ".test"
	}
	domain := domainName + ext

	app.appendLog(fmt.Sprintf("projects: creating '%s' (%s) at %s ...",
		name, f.Name, domain))
	go func() {
		if err := createProject(f, name, domain, app.appendLog); err != nil {
			app.appendLog("projects: " + err.Error())
			return
		}
		app.wnd.UiThread(func() {
			refreshProjectList()
			refreshVhostList()
		})
	}()
}

// splitDomain breaks a "name.ext" string into ("name", ".ext"). The
// split is on the LAST dot so multi-part names like "api.myapp.test"
// round-trip cleanly: name="api.myapp", ext=".test". Domains with no
// dot at all return ("name", ".test") so the caller still has a
// usable extension default.
func splitDomain(domain string) (string, string) {
	domain = strings.TrimSpace(domain)
	i := strings.LastIndex(domain, ".")
	if i < 0 {
		return domain, ".test"
	}
	return domain[:i], domain[i:]
}

// setDomainExtSelection picks the matching item in the extension
// combobox for the given ".ext" string. Falls back to index 0
// (.test) when the extension isn't in our curated list — keeps the
// dropdown deterministic instead of showing a stale leftover.
func setDomainExtSelection(combo *ui.ComboBox, ext string) {
	if combo == nil {
		return
	}
	for i, e := range domainExtensions {
		if strings.EqualFold(e, ext) {
			combo.SelectIndex(i)
			return
		}
	}
	combo.SelectIndex(0)
}

// slugify turns "My App" into "my-app" — lowercase, alphanumerics
// and dashes only. Used as the directory name + default subdomain
// so projects don't land in paths that break Apache.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// refreshProjectList rebuilds the Projects ListView from config.
func refreshProjectList() {
	if projList == nil {
		return
	}
	projList.DeleteAllItems()
	for _, p := range app.cfg.Projects {
		projList.AddItem(p.Name, p.Framework, p.Domain, p.DocRoot)
	}
}

// selectedProjectIndex returns the first selected row in the
// projects ListView, or -1.
func selectedProjectIndex() int {
	if projList == nil {
		return -1
	}
	sel := projList.SelectedItems()
	if len(sel) == 0 {
		return -1
	}
	idx := sel[0].Index()
	if idx < 0 || idx >= len(app.cfg.Projects) {
		return -1
	}
	return idx
}

// deleteProject removes a project's directory, drops its vhost,
// saves config, re-applies vhosts. Leaves downloaded archives in
// place so a re-create is fast.
func deleteProject(idx int) {
	if idx < 0 || idx >= len(app.cfg.Projects) {
		return
	}
	p := app.cfg.Projects[idx]
	projectDir := filepath.Join(app.baseDir, "www", p.Name)

	app.appendLog(fmt.Sprintf("projects: deleting '%s' (%s)...", p.Name, p.Domain))
	if err := os.RemoveAll(projectDir); err != nil {
		app.appendLog("projects delete: " + err.Error())
		// Continue anyway — user can remove manually.
	}
	// Drop from config.
	app.cfg.Projects = append(app.cfg.Projects[:idx], app.cfg.Projects[idx+1:]...)
	// Drop matching vhost.
	for i := len(app.cfg.Vhosts) - 1; i >= 0; i-- {
		if app.cfg.Vhosts[i].Domain == p.Domain {
			app.cfg.Vhosts = append(app.cfg.Vhosts[:i], app.cfg.Vhosts[i+1:]...)
		}
	}
	_ = SaveConfig(app.baseDir, app.cfg)
	if err := ApplyVhosts(app.baseDir, app.cfg); err != nil {
		app.appendLog("projects delete: apply vhosts: " + err.Error())
	}
	refreshProjectList()
	refreshVhostList()
}

// ----- Log panel ---------------------------------------------------------

// buildLogPanel creates the read-only multi-line Edit that always sits
// at the bottom of the window, regardless of which page is active.
func buildLogPanel(wnd *ui.Main) {
	ui.NewStatic(wnd, ui.OptsStatic().
		Text("Logs").
		Position(ui.Dpi(logX, logY-18)))

	app.logBox = ui.NewEdit(wnd, ui.OptsEdit().
		Position(ui.Dpi(logX, logY)).
		Width(ui.DpiX(logW)).Height(ui.DpiY(logH)).
		CtrlStyle(co.ES_MULTILINE|co.ES_READONLY|co.ES_AUTOVSCROLL|co.ES_WANTRETURN).
		Layout(ui.LAY_HOLD_RESIZE))
}

// ----- Shared helpers ----------------------------------------------------

// startService runs Start() on a service, auto-downloading first if the
// exe is missing. Shared between the Start button and Start All Enabled.
func startService(idx int) {
	if idx < 0 || idx >= len(app.services) {
		return
	}
	ms := app.services[idx]
	if ms.Service == nil {
		// Tool-only entries (phpMyAdmin, Adminer) — install if missing,
		// otherwise just open the admin URL.
		if _, ok := DownloadCatalog[ms.Conf.Name]; ok && !IsInstalled(ms.Conf.Name, app.baseDir) {
			downloadInBackground(ms.Conf.Name)
			return
		}
		if ms.Conf.OpenURL != "" {
			openPath(ms.Conf.OpenURL)
		}
		return
	}
	// Two failure modes the sentinel catches:
	//   1. Service was never installed at all (exe missing).
	//   2. Partial install — exe is there but the install is otherwise
	//      busted (e.g. Apache has bin/httpd.exe but no conf/httpd.conf
	//      because AV quarantined the rest of the zip, or MySQL has
	//      mysqld.exe but data/mysql/ was never seeded by install-db).
	// Both look the same to the user and both demand the same fix:
	// re-run DownloadAndInstall, which wipes InstallDir and re-extracts.
	_, hasCatalog := DownloadCatalog[ms.Conf.Name]
	if hasCatalog && !IsInstalled(ms.Conf.Name, app.baseDir) {
		name := ms.Conf.Name
		svc := ms.Service
		// If the user previously picked a specific version (PHP 8.2,
		// Node 20, etc.), honour that — installing the catalogue's
		// default would overwrite their choice and, worse, try to
		// MkdirAll the canonical InstallDir which is now a junction
		// to bin/<svc>-<version>. Pass the saved ActiveVersion so the
		// install lands in the correct bin/<svc>-<version>/ tree.
		version := ms.Conf.ActiveVersion
		app.appendLog(fmt.Sprintf("[%s] install incomplete — auto-reinstalling...", name))
		go func() {
			if err := DownloadAndInstallVersion(name, version, app.baseDir, app.appendLog, uiDownloadProgress); err != nil {
				app.appendLog(fmt.Sprintf("[%s] install failed: %v", name, err))
				return
			}
			app.wnd.UiThread(refreshServiceList)
			if err := svc.Start(); err != nil {
				app.appendLog(fmt.Sprintf("[%s] start: %v", name, err))
			}
		}()
		return
	}
	if _, err := os.Stat(ms.Service.ExePath); err != nil {
		app.appendLog(fmt.Sprintf("[%s] exe missing: %s", ms.Conf.Name, ms.Service.ExePath))
		return
	}
	if err := ms.Service.Start(); err != nil {
		app.appendLog(fmt.Sprintf("[%s] start: %v", ms.Conf.Name, err))
	}
}

// (toggleSelectedService and forSelectedService were removed when
// the Services page switched from a ListView to a card grid. Each
// card now wires its own button-click handlers directly via
// closures, so there's no "currently selected service" concept to
// hand off to a generic helper.)

// selectedVhostIndex returns the first selected row in the vhost list,
// or -1 if nothing is selected.
func selectedVhostIndex() int {
	if vhostList == nil {
		return -1
	}
	sel := vhostList.SelectedItems()
	if len(sel) == 0 {
		return -1
	}
	return sel[0].Index()
}

// readVhostForm parses the inline vhost editor fields into a Vhost
// struct, validating port and required fields. The domain is
// reassembled from the split name + extension widgets — e.g.
// "myapp" + ".test" → "myapp.test".
func readVhostForm() (Vhost, error) {
	name := strings.TrimSpace(vhostDomainNm.Text())
	if name == "" {
		return Vhost{}, fmt.Errorf("domain name is required")
	}
	ext := vhostDomainExt.CurrentText()
	if ext == "" {
		ext = ".test"
	}
	domain := name + ext
	docroot := strings.TrimSpace(vhostDocRoot.Text())
	if docroot == "" {
		return Vhost{}, fmt.Errorf("docroot is required")
	}
	portStr := strings.TrimSpace(vhostPort.Text())
	if portStr == "" {
		portStr = "80"
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return Vhost{}, fmt.Errorf("invalid port: %s", portStr)
	}
	server := vhostServer.CurrentText()
	if server == "" {
		server = "apache"
	}
	return Vhost{
		Domain:     domain,
		DocRoot:    docroot,
		Port:       port,
		ServerType: server,
	}, nil
}

// reloadConfig re-reads config.json and rebuilds the services slice,
// reusing live Service runtime objects for names that still exist.
func reloadConfig() error {
	cfg, err := LoadConfig(app.baseDir)
	if err != nil {
		return err
	}
	app.cfg = cfg
	old := app.services
	app.services = nil
	for i := range cfg.Services {
		sc := &cfg.Services[i]
		ms := &ManagedService{Conf: sc}
		for _, o := range old {
			if o.Conf.Name == sc.Name && o.Service != nil {
				ms.Service = o.Service
				break
			}
		}
		if ms.Service == nil && sc.ExePath != "" {
			ms.Service = &Service{
				Name:    sc.Name,
				ExePath: ExpandPath(sc.ExePath, app.baseDir),
				Args:    expandArgsList(sc.Args, app.baseDir),
				Port:    sc.Port,
				WorkDir: ExpandPath(sc.WorkDir, app.baseDir),
			}
			ms.Service.SetLogger(app.appendLog)
		}
		app.services = append(app.services, ms)
	}
	return nil
}

// countEnabledServices returns how many services in the config have
// enabled:true. Used by the Settings page summary.
func countEnabledServices() int {
	n := 0
	for _, s := range app.cfg.Services {
		if s.Enabled {
			n++
		}
	}
	return n
}

// downloadInBackground fires a DownloadAndInstall on a goroutine and
// refreshes the services list on completion so the "Installed" column
// flips from "no" to "yes".
func downloadInBackground(name string) {
	if _, ok := DownloadCatalog[name]; !ok {
		app.appendLog(fmt.Sprintf("[%s] no download recipe", name))
		return
	}
	go func() {
		if err := DownloadAndInstall(name, app.baseDir, app.appendLog, uiDownloadProgress); err != nil {
			app.appendLog(fmt.Sprintf("[%s] %v", name, err))
			return
		}
		if app.wnd != nil {
			app.wnd.UiThread(refreshServiceList)
		}
	}()
}

// uninstallService wipes a service's install directory. Won't touch a
// running process — the user has to Stop it first.
func uninstallService(idx int) {
	if idx < 0 || idx >= len(app.services) {
		return
	}
	ms := app.services[idx]
	if ms.Service != nil && ms.Service.Running() {
		app.appendLog(fmt.Sprintf("[%s] stop the service before uninstalling", ms.Conf.Name))
		return
	}
	spec, ok := DownloadCatalog[ms.Conf.Name]
	if !ok {
		app.appendLog(fmt.Sprintf("[%s] no uninstall recipe", ms.Conf.Name))
		return
	}
	installDir := filepath.Join(app.baseDir, filepath.FromSlash(spec.InstallDir))
	if _, err := os.Stat(installDir); err != nil {
		app.appendLog(fmt.Sprintf("[%s] not installed at %s", ms.Conf.Name, installDir))
		return
	}
	app.appendLog(fmt.Sprintf("[%s] removing %s ...", ms.Conf.Name, installDir))
	if err := os.RemoveAll(installDir); err != nil {
		app.appendLog(fmt.Sprintf("[%s] uninstall: %v", ms.Conf.Name, err))
		return
	}
	app.appendLog(fmt.Sprintf("[%s] uninstalled", ms.Conf.Name))
	refreshServiceList()
}
