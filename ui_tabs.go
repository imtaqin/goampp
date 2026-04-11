//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"
)

// ----- Layout geometry --------------------------------------------------
// Everything is in design pixels. ui.Dpi scales at construction time, so
// these numbers are what you see on a 100% monitor.
const (
	winW = 960
	winH = 760

	// Sidebar (left column of nav buttons).
	sideX = 10
	sideY = 48
	sideW = 110
	sideH = 38
	sideGap = 4

	// Content area (pages live here, stacked on top of each other).
	// Bumped from 332 → 452 to fit a 4×3 grid of service cards with
	// per-card action buttons (Start/Stop/Restart/Conf).
	contentX = 130
	contentY = 48
	contentW = 820
	contentH = 452

	// Download progress strip — always present above the log panel.
	progY = 508
	progH = 20

	// Log panel across the bottom. logY needs enough headroom above
	// it for the "Logs" label (logY-18); progBar bottom is at
	// progY+progH = 528, so logY-18 must be >= 530 → logY >= 548.
	logX = 10
	logY = 552
	logW = 940
	logH = 170
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
var essentialServices = []string{"Apache", "MySQL", "phpMyAdmin"}

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
	for _, p := range pages {
		if p.key == key {
			h := p.container.Hwnd()
			h.ShowWindow(co.SW_SHOW)
			// Erase any stale pixels from a previously-active page
			// before the new page's children paint themselves.
			_ = h.InvalidateRect(nil, true)
			h.UpdateWindow()
		} else {
			p.container.Hwnd().ShowWindow(co.SW_HIDE)
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
	srcIdx     int          // index into app.services
	iconStatic *ui.Static   // SS_ICON, set via STM_SETICON
	nameStatic *ui.Static
	statusLbl  *ui.Static
	versionLbl *ui.Static
	btnStart   *ui.Button   // toggles label between "Start" and "Stop"
	btnStop    *ui.Button
	btnRestart *ui.Button
	btnConf    *ui.Button
}

// serviceCards is the live grid. One entry per app.services entry.
// Built once at WM_CREATE; updated by refreshServiceList.
var serviceCards []*serviceCard

// Per-card geometry. Cards are 199 wide × 110 tall, packed in a
// 4×3 grid below the page title. The math: contentW (820) - 16
// padding = 804; 4 columns + 3 gaps × 8 = 24; (804-24)/4 = 195.
const (
	cardW   = 195
	cardH   = 110
	cardGap = 8
)

// buildServicesPage builds the page chrome (bottom Start Stack
// button row) and creates 12 service-card widget groups, one per
// entry in app.services. Card text values are set later by
// refreshServiceList — at build time we just create the controls.
func buildServicesPage(parent *ui.Control) {
	// Create one card per service in category order so the grid
	// reads top-to-bottom by category.
	categoryOrder := []int32{groupWeb, groupLanguage, groupDatabase, groupTool}
	pos := 0
	gridX := 10
	gridY := 12
	for _, wantGroup := range categoryOrder {
		for srcIdx, ms := range app.services {
			if groupForKind(ms.Conf.Kind) != wantGroup {
				continue
			}
			col := pos % 4
			row := pos / 4
			x := gridX + col*(cardW+cardGap)
			y := gridY + row*(cardH+cardGap)
			card := buildServiceCard(parent, x, y, srcIdx, ms)
			serviceCards = append(serviceCards, card)
			pos++
		}
	}

	// Bottom action area: just Start Stack now that per-card
	// buttons cover the per-service actions.
	btnY := gridY + 3*(cardH+cardGap) + 8
	bx := 10
	addBtn := func(label string, w int, scheme ColorScheme, onClick func()) {
		b := newColoredButton(parent, ui.OptsButton().
			Text(label).
			Position(ui.Dpi(bx, btnY)).
			Width(ui.DpiX(w)).Height(ui.DpiY(30)),
			scheme)
		b.On().BnClicked(onClick)
		bx += w + 8
	}
	addBtn("Start Stack", 130, SchemeSuccess, func() {
		go func() {
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
				time.Sleep(400 * time.Millisecond)
			}
		}()
	})
	addBtn("Stop All", 100, SchemeDanger, func() {
		go func() {
			for _, ms := range app.services {
				if ms.Service != nil {
					_ = ms.Service.Stop()
				}
			}
		}()
	})
	addBtn("Restart Stack", 130, SchemeWarning, func() {
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
				time.Sleep(400 * time.Millisecond)
			}
			app.appendLog("restart stack: done")
		}()
	})
}

// buildServiceCard creates the widgets for one service tile at the
// given top-left coordinates. Layout:
//
//   ┌────────────────────────────────────────┐
//   │ [icon]  Service Name                    │  (icon 32×32 + name)
//   │         Status                          │
//   │         Version                         │
//   │ [Start] [Stop] [Restart] [Conf]         │  (4 buttons)
//   └────────────────────────────────────────┘
func buildServiceCard(parent *ui.Control, x, y, srcIdx int, ms *ManagedService) *serviceCard {
	c := &serviceCard{srcIdx: srcIdx}

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

	// Name — bold-ish (we don't change font, but the position is
	// the dominant text on the card so it reads as the title).
	c.nameStatic = ui.NewStatic(parent, ui.OptsStatic().
		Text(ms.Conf.Name).
		Position(ui.Dpi(x+48, y+8)).
		Size(ui.Dpi(cardW-56, 16)))

	c.statusLbl = ui.NewStatic(parent, ui.OptsStatic().
		Text("...").
		Position(ui.Dpi(x+48, y+24)).
		Size(ui.Dpi(cardW-56, 14)))

	c.versionLbl = ui.NewStatic(parent, ui.OptsStatic().
		Text("").
		Position(ui.Dpi(x+48, y+40)).
		Size(ui.Dpi(cardW-56, 14)))

	// Button row at the bottom of the card. 4 buttons of 42×22
	// each + 3×4 gaps + 8 padding = 192. Card is 195 wide so
	// they just fit.
	btnRowY := y + cardH - 30
	idx := srcIdx // capture for closures

	c.btnStart = newColoredButton(parent, ui.OptsButton().
		Text("Start").
		Position(ui.Dpi(x+8, btnRowY)).
		Width(ui.DpiX(42)).Height(ui.DpiY(22)),
		SchemeSuccess)
	c.btnStart.On().BnClicked(func() {
		startService(idx)
	})

	c.btnStop = newColoredButton(parent, ui.OptsButton().
		Text("Stop").
		Position(ui.Dpi(x+54, btnRowY)).
		Width(ui.DpiX(42)).Height(ui.DpiY(22)),
		SchemeDanger)
	c.btnStop.On().BnClicked(func() {
		if s := app.services[idx].Service; s != nil {
			_ = s.Stop()
		}
	})

	c.btnRestart = newColoredButton(parent, ui.OptsButton().
		Text("Rstrt").
		Position(ui.Dpi(x+100, btnRowY)).
		Width(ui.DpiX(42)).Height(ui.DpiY(22)),
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

	c.btnConf = newColoredButton(parent, ui.OptsButton().
		Text("Conf").
		Position(ui.Dpi(x+146, btnRowY)).
		Width(ui.DpiX(42)).Height(ui.DpiY(22)),
		SchemePrimary)
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
	for _, c := range serviceCards {
		ms := app.services[c.srcIdx]
		// Status text + colour-ish hint via the literal label.
		status := "Stopped"
		if !ms.Conf.Enabled {
			status = "Disabled"
		}
		if ms.Service == nil {
			if IsInstalled(ms.Conf.Name, app.baseDir) {
				status = "(tool — installed)"
			} else {
				status = "(not installed)"
			}
		} else if ms.Service.Running() {
			status = "● Running  pid " + fmt.Sprint(ms.Service.PID())
		}
		// Append the port to status when relevant — saves a row.
		if ms.Conf.Port > 0 {
			status += "  :" + fmt.Sprint(ms.Conf.Port)
		}
		c.statusLbl.Hwnd().SetWindowText(status)

		version := ""
		if spec, ok := DownloadCatalog[ms.Conf.Name]; ok {
			version = spec.Version
		}
		c.versionLbl.Hwnd().SetWindowText(version)

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
	y := 14
	row := func(label, value string) {
		ui.NewStatic(parent, ui.OptsStatic().
			Text(label).
			Position(ui.Dpi(10, y)))
		ui.NewStatic(parent, ui.OptsStatic().
			Text(value).
			Position(ui.Dpi(180, y)).
			Size(ui.Dpi(contentW-200, 16)))
		y += 22
	}

	row("Base directory:", app.baseDir)
	row("Config file:", filepath.Join(app.baseDir, "config.json"))
	row("Windows hosts:", DefaultHostsFile())
	row("Apache vhosts:", ExpandPath(app.cfg.Settings.ApacheVhostsInclude, app.baseDir))
	row("Nginx sites dir:", ExpandPath(app.cfg.Settings.NginxSitesDir, app.baseDir))
	row("Downloads cache:", filepath.Join(app.baseDir, "downloads"))
	row("Services loaded:", fmt.Sprintf("%d total, %d enabled",
		len(app.cfg.Services), countEnabledServices()))
	row("Vhosts loaded:", fmt.Sprintf("%d", len(app.cfg.Vhosts)))
	autoStartState := "no"
	if isAutoStartEnabled() {
		autoStartState = "yes — launches hidden into tray on login"
	}
	row("Auto-start on boot:", autoStartState)

	// Show elevation status — needed for hosts file writes when
	// applying virtual hosts. The "Restart as Admin" button below
	// is only rendered when this is "no".
	elev := "no — vhost Apply will fail with 'Access is denied'"
	if IsElevated() {
		elev = "yes (administrator)"
	}
	row("Running elevated:", elev)

	y += 12
	x := 10
	addBtn := func(label string, w int, scheme ColorScheme, onClick func()) {
		b := newColoredButton(parent, ui.OptsButton().
			Text(label).
			Position(ui.Dpi(x, y)).
			Width(ui.DpiX(w)).Height(ui.DpiY(26)),
			scheme)
		b.On().BnClicked(onClick)
		x += w + 6
	}
	// Four buttons: the essentials. Removed "Open base dir" and "Clear
	// downloads cache" and "Hide to Tray" — those are reachable from
	// the tray menu and the Services page.
	addBtn("Edit config", 110, SchemePrimary, func() {
		showPage("editor")
		loadFileIntoEditor(filepath.Join(app.baseDir, "config.json"))
	})
	addBtn("Reload config", 120, SchemeNeutral, func() {
		if err := reloadConfig(); err != nil {
			app.appendLog("reload: " + err.Error())
			return
		}
		refreshServiceList()
		refreshVhostList()
		populateEditorDropdown()
		app.appendLog("config reloaded")
	})
	addBtn("Toggle Auto-start", 150, SchemePrimary, func() {
		toggleAutoStart()
	})
	if !IsElevated() {
		// Show the elevation button only when we're not already
		// admin. Once elevated, the button would just no-op.
		addBtn("Restart as Admin", 150, SchemeWarning, func() {
			if err := RelaunchElevated(); err != nil {
				app.appendLog("elevate: " + err.Error())
				return
			}
			// Hand off cleanly: stop everything we manage so
			// the elevated instance can take over the ports.
			app.appendLog("relaunching as administrator — this instance will exit")
			time.Sleep(200 * time.Millisecond)
			quitApp(app.wnd)
		})
	}
	addBtn("Add tools to PATH", 160, SchemeSuccess, func() {
		// Runs synchronously — registry writes are fast and the
		// broadcast is bounded to 1 second per window.
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
		app.appendLog("path: open a NEW terminal to use them — already-running shells keep the old PATH")
	})
	addBtn("Remove from PATH", 150, SchemeWarning, func() {
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
	addBtn("Quit", 90, SchemeDanger, func() {
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
	if _, err := os.Stat(ms.Service.ExePath); err != nil {
		if _, ok := DownloadCatalog[ms.Conf.Name]; !ok {
			app.appendLog(fmt.Sprintf("[%s] exe missing: %s", ms.Conf.Name, ms.Service.ExePath))
			return
		}
		name := ms.Conf.Name
		svc := ms.Service
		app.appendLog(fmt.Sprintf("[%s] not installed — auto-downloading...", name))
		go func() {
			if err := DownloadAndInstall(name, app.baseDir, app.appendLog, uiDownloadProgress); err != nil {
				app.appendLog(fmt.Sprintf("[%s] install failed: %v", name, err))
				return
			}
			app.wnd.UiThread(refreshServiceList)
			if _, err := os.Stat(svc.ExePath); err != nil {
				app.appendLog(fmt.Sprintf("[%s] exe still not at %s — check config.json", name, svc.ExePath))
				return
			}
			if err := svc.Start(); err != nil {
				app.appendLog(fmt.Sprintf("[%s] start: %v", name, err))
			}
		}()
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
