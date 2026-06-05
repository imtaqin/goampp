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

const (
	winW = 1100

	sideX   = 10
	sideY   = 48
	sideW   = 110
	sideH   = 38
	sideGap = 4

	contentX = 130
	contentY = 48
	contentW = 960

	progH = 18

	logX = 10
	logW = 1080
	logH = 90

	svcTabBtnW   = 84
	svcTabBtnH   = 24
	svcTabStripY = 36
	cardGridY    = 64
)

var (
	contentH int
	progY    int
	logY     int
	winH     int
)

func computeLayout() {
	ncols := (contentW + cardGap) / (cardW + cardGap)
	if ncols < 1 {
		ncols = 1
	}
	nrows := (len(app.services) + ncols - 1) / ncols
	if nrows < 2 {
		nrows = 2
	}

	contentH = cardGridY + nrows*(cardH+cardGap) - cardGap + 16
	progY = contentY + contentH + 8
	logY = progY + progH + 8
	winH = logY + logH + 40
}

type page struct {
	key       string
	title     string
	container *ui.Control
}

var domainExtensions = []string{
	".test",
	".local",
	".localhost",
	".lan",
	".home",
	".site",
}

var essentialServices = []string{"Apache", "PHP-FPM", "MySQL", "phpMyAdmin"}

func ensureStackEssentials() {
	for _, name := range essentialServicesActive() {
		ms := app.findService(name)
		if ms == nil {
			continue
		}

		if _, ok := DownloadCatalog[name]; ok && !IsInstalled(name, app.baseDir) {
			if err := DownloadAndInstallVersion(name, ms.Conf.ActiveVersion, app.baseDir, app.appendLog, uiDownloadProgress); err != nil {
				app.appendLog(fmt.Sprintf("[%s] install failed: %v", name, err))
				continue
			}
		}

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

	time.Sleep(800 * time.Millisecond)
	openPath("http://localhost/")
}

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
	activePage string

	progBar   *ui.ProgressBar
	progLabel *ui.Static

	edDropdown   *ui.ComboBox
	edPathLabel  *ui.Static
	edContent    *ui.Edit
	edKnownPaths []string

	vhostList      *ui.ListView
	vhostDomainNm  *ui.Edit
	vhostDomainExt *ui.ComboBox
	vhostDocRoot   *ui.Edit
	vhostPort      *ui.Edit
	vhostServer    *ui.ComboBox

	projFramework *ui.ComboBox
	projName      *ui.Edit
	projDomainNm  *ui.Edit
	projDomainExt *ui.ComboBox
	projList      *ui.ListView
)

func buildMainLayout(wnd *ui.Main) {
	buildTitleBar(wnd)
	buildSidebar(wnd)
	buildPages(wnd)
	buildProgressStrip(wnd)
	buildLogPanel(wnd)
}

func buildProgressStrip(wnd *ui.Main) {

	progLabel = ui.NewStatic(wnd, ui.OptsStatic().
		Text("Idle").
		Position(ui.Dpi(10, progY+3)).
		Size(ui.Dpi(380, 16)))

	progBar = ui.NewProgressBar(wnd, ui.OptsProgressBar().
		Position(ui.Dpi(400, progY)).
		Size(ui.Dpi(550, progH)).
		Range(0, 1000))
}

func uiDownloadProgress(stage, name string, done, total int64) {
	if app.wnd == nil {
		return
	}

	var (
		labelText string
		pos       int
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

func buildTitleBar(wnd *ui.Main) {
	ui.NewStatic(wnd, ui.OptsStatic().
		Text("").
		Position(ui.Dpi(0, 44)).
		Size(ui.Dpi(winW, 1)).
		CtrlStyle(co.SS_ETCHEDHORZ))
}

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

	ui.NewStatic(wnd, ui.OptsStatic().
		Text("").
		Position(ui.Dpi(sideX+sideW+6, sideY)).
		Size(ui.Dpi(1, contentH+10)).
		CtrlStyle(co.SS_ETCHEDVERT))
}

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

func newPageContainer(wnd *ui.Main) *ui.Control {
	c := ui.NewControl(wnd, ui.OptsControl().
		Position(ui.Dpi(contentX, contentY)).
		Size(ui.Dpi(contentW, contentH)).
		ExStyle(co.WS_EX_LEFT).
		ClassBrush(windowBgBrush()))
	c.On().WmDrawItem(handleDrawItem)
	c.On().WmCtlColorStatic(staticBgHandler)
	return c
}

func showPage(key string) {

	for _, p := range pages {
		if p.key != key {
			p.container.Hwnd().ShowWindow(co.SW_HIDE)
		}
	}
	for _, p := range pages {
		if p.key == key {
			h := p.container.Hwnd()
			h.ShowWindow(co.SW_SHOW)

			_ = h.RedrawWindow(nil, 0,
				co.RDW_INVALIDATE|co.RDW_ERASE|co.RDW_ALLCHILDREN|co.RDW_UPDATENOW)
		}
	}
	activePage = key
	if app.statusBar != nil {
		app.statusBar.Part(0).SetText(fmt.Sprintf("Page: %s", key))
	}
}

type serviceCard struct {
	srcIdx     int
	cardCtrl   *ui.Control
	iconStatic *ui.Static
	nameStatic *ui.Static
	statusLbl  *ui.Static
	versionLbl *ui.Static

	rect win.RECT

	btnToggle  *ui.Button
	btnRestart *ui.Button
	btnConf    *ui.Button

	btnVer *ui.Button

	statusDot *ui.Static
}

const versionMenuBase = 6000

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

		if v.Version == current ||
			(current == "" && strings.HasPrefix(spec.Version, v.Version)) {
			flags |= co.MF_CHECKED
		}
		appendMenuItem(hMenu, flags, uintptr(versionMenuBase+i), v.Version)
	}

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

		if app.cfg != nil && srcIdx < len(app.cfg.Services) {
			app.cfg.Services[srcIdx].ActiveVersion = chosen
			_ = SaveConfig(app.baseDir, app.cfg)
		}
		app.appendLog(fmt.Sprintf("[%s] switched to %s", serviceName, chosen))
		app.wnd.UiThread(refreshServiceList)
	}()
}

func activeWebServer() string {
	if app.cfg != nil && app.cfg.Settings.ActiveWebServer != "" {
		return app.cfg.Settings.ActiveWebServer
	}
	return "Apache"
}

func isWebKindCard(c *serviceCard) bool {
	return groupForKind(app.services[c.srcIdx].Conf.Kind) == groupWeb
}

var serviceCards []*serviceCard

type svcTabGroup struct {
	label  string
	groups []int32
}

var svcTabGroups = []svcTabGroup{
	{"All", nil},
	{"Web", []int32{groupWeb}},
	{"Database", []int32{groupDatabase}},
	{"Language", []int32{groupLanguage}},
	{"Tools", []int32{groupTool}},
}

var (
	activeSvcTabIdx int
	svcTabBtns      []*ui.Button
	svcTabParent    *ui.Control
)

const (
	cardW   = 228
	cardH   = 72
	cardGap = 8
)

var webServerPicker *ui.ComboBox

func buildServicesPage(parent *ui.Control) {

	parent.On().WmContextMenu(func(p ui.WmContextMenu) {
		showServiceContextMenu(parent, p.CursorPos())
	})

	ui.NewStatic(parent, ui.OptsStatic().
		Text("Web:").
		Position(ui.Dpi(10, 8)).
		Size(ui.Dpi(28, 16)))

	webServerPicker = ui.NewComboBox(parent, ui.OptsComboBox().
		Position(ui.Dpi(40, 6)).
		Width(ui.DpiX(110)).
		Texts("Apache", "Nginx"))

	if activeWebServer() == "Nginx" {
		webServerPicker.SelectIndex(1)
	} else {
		webServerPicker.SelectIndex(0)
	}

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

	svcTabParent = parent
	svcTabBtns = svcTabBtns[:0]
	tabX := 10
	for i, tg := range svcTabGroups {
		i, tg := i, tg
		scheme := SchemeSidebar
		if i == 0 {
			scheme = SchemePrimary
		}
		btn := newColoredButton(parent, ui.OptsButton().
			Text(tg.label).
			Position(ui.Dpi(tabX, svcTabStripY)).
			Width(ui.DpiX(svcTabBtnW)).Height(ui.DpiY(svcTabBtnH)),
			scheme)
		btn.On().BnClicked(func() { switchSvcTab(i) })
		svcTabBtns = append(svcTabBtns, btn)
		tabX += svcTabBtnW + 4
	}

	ncols := (contentW + cardGap) / (cardW + cardGap)
	if ncols < 1 {
		ncols = 1
	}

	categoryOrder := []int32{groupWeb, groupDatabase, groupLanguage, groupTool}
	gridX := 10
	pos := 0
	for _, wantGroup := range categoryOrder {
		for srcIdx, ms := range app.services {
			if groupForKind(ms.Conf.Kind) != wantGroup {
				continue
			}
			col := pos % ncols
			row := pos / ncols
			x := gridX + col*(cardW+cardGap)
			cy := cardGridY + row*(cardH+cardGap)
			card := buildServiceCard(parent, x, cy, srcIdx, ms)
			serviceCards = append(serviceCards, card)
			pos++
		}
	}
	_ = pos
}

func buildServiceCard(parent *ui.Control, x, y, srcIdx int, ms *ManagedService) *serviceCard {
	c := &serviceCard{
		srcIdx: srcIdx,
		rect: win.RECT{
			Left: int32(x), Top: int32(y),
			Right: int32(x + cardW), Bottom: int32(y + cardH),
		},
	}

	c.cardCtrl = ui.NewControl(parent, ui.OptsControl().
		Position(ui.Dpi(x, y)).
		Size(ui.Dpi(cardW, cardH)).
		ExStyle(co.WS_EX_LEFT).
		ClassBrush(windowBgBrush()))
	c.cardCtrl.On().WmDrawItem(handleDrawItem)
	c.cardCtrl.On().WmCtlColorStatic(staticBgHandler)

	cc := c.cardCtrl

	ui.NewStatic(cc, ui.OptsStatic().
		Text("").
		Position(ui.Dpi(0, 0)).
		Size(ui.Dpi(cardW, cardH)).
		CtrlStyle(co.SS_ETCHEDFRAME))

	c.iconStatic = ui.NewStatic(cc, ui.OptsStatic().
		Text("").
		Position(ui.Dpi(8, 8)).
		Size(ui.Dpi(32, 32)).
		CtrlStyle(co.SS_ICON|co.SS_REALSIZECONTROL))

	c.statusDot = ui.NewStatic(cc, ui.OptsStatic().
		Text("●").
		Position(ui.Dpi(44, 10)).
		Size(ui.Dpi(12, 14)))

	c.nameStatic = ui.NewStatic(cc, ui.OptsStatic().
		Text(ms.Conf.Name).
		Position(ui.Dpi(58, 10)).
		Size(ui.Dpi(cardW-66, 16)))

	c.statusLbl = ui.NewStatic(cc, ui.OptsStatic().
		Text("...").
		Position(ui.Dpi(48, 28)).
		Size(ui.Dpi(cardW-56, 14)))

	c.versionLbl = ui.NewStatic(cc, ui.OptsStatic().
		Text("").
		Position(ui.Dpi(48, cardH)).
		Size(ui.Dpi(1, 1)))

	btnRowY := cardH - 28
	idx := srcIdx

	c.btnToggle = newColoredButton(cc, ui.OptsButton().
		Text("▶ Start").
		Position(ui.Dpi(8, btnRowY)).
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

	c.btnRestart = newColoredButton(cc, ui.OptsButton().
		Text("").
		Position(ui.Dpi(cardW+100, cardH+100)).
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

	hasVariants := false
	if spec, ok := DownloadCatalog[ms.Conf.Name]; ok && len(spec.Variants) > 0 {
		hasVariants = true
	}

	if hasVariants {
		px, py := ui.Dpi(8, btnRowY)
		c.btnToggle.Hwnd().SetWindowPos(win.HWND(0),
			win.POINT{X: int32(px), Y: int32(py)},
			win.SIZE{Cx: int32(ui.DpiX(96)), Cy: int32(ui.DpiY(22))},
			co.SWP_NOZORDER)
		c.btnConf = newColoredButton(cc, ui.OptsButton().
			Text("Conf").
			Position(ui.Dpi(110, btnRowY)).
			Width(ui.DpiX(52)).Height(ui.DpiY(22)),
			SchemePrimary)
		card := c
		c.btnVer = newColoredButton(cc, ui.OptsButton().
			Text("Ver ▾").
			Position(ui.Dpi(168, btnRowY)).
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
		isRuntime := strings.ToLower(ms.Conf.Kind) == "runtime"
		px, py := ui.Dpi(8, btnRowY)
		c.btnToggle.Hwnd().SetWindowPos(win.HWND(0),
			win.POINT{X: int32(px), Y: int32(py)},
			win.SIZE{Cx: int32(ui.DpiX(130)), Cy: int32(ui.DpiY(22))},
			co.SWP_NOZORDER)
		if isRuntime {
			c.btnConf = newColoredButton(cc, ui.OptsButton().
				Text("⌨ Term").
				Position(ui.Dpi(146, btnRowY)).
				Width(ui.DpiX(74)).Height(ui.DpiY(22)),
				SchemeSidebar)
		} else {
			c.btnConf = newColoredButton(cc, ui.OptsButton().
				Text("Conf").
				Position(ui.Dpi(146, btnRowY)).
				Width(ui.DpiX(74)).Height(ui.DpiY(22)),
				SchemePrimary)
		}
	}
	c.btnConf.On().BnClicked(func() {
		ms := app.services[idx]

		if strings.ToLower(ms.Conf.Kind) == "runtime" {
			openTerminalForService(ms.Conf.Name)
			return
		}
		if ms.Conf.ConfigFile == "" {

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

var langBinDirs = map[string][]string{
	"Node.js": {"bin/node"},
	"Python":  {"bin/python", "bin/python/Scripts"},
	"Go":      {"bin/go/bin"},
	"Java":    {"bin/java/bin"},
	"Julia":   {"bin/julia/bin"},
	"Zig":     {"bin/zig"},
	"Dart":    {"bin/dart/bin"},
	"Lua":     {"bin/lua"},
	"Ruby":    {"bin/ruby/bin"},
	"Rust":    {"bin/rust/.cargo/bin"},
	"Kotlin":  {"bin/kotlin/bin"},
	"Haskell": {"bin/haskell/bin"},
	"Elixir":  {"bin/elixir/bin", "bin/erlang/bin"},
	"Crystal": {"bin/crystal"},
	"Scala":   {"bin/scala/bin"},
	"Erlang":  {"bin/erlang/bin"},
	"Swift":   {},
}

func openTerminalForService(name string) {
	conEmu := filepath.Join(app.baseDir, "installer", "ConEmu64.exe")

	relDirs := langBinDirs[name]
	var prepend []string
	for _, rel := range relDirs {
		prepend = append(prepend, filepath.Join(app.baseDir, filepath.FromSlash(rel)))
	}

	env := os.Environ()
	for i, e := range env {
		if strings.HasPrefix(strings.ToUpper(e), "PATH=") {
			existing := e[5:]
			all := append(prepend, existing)
			env[i] = "PATH=" + strings.Join(all, ";")
			break
		}
	}

	env = append(env, "GOAMPP_BASE="+app.baseDir)

	if _, err := os.Stat(conEmu); err != nil {

		cmd := exec.Command("cmd.exe")
		cmd.Env = env
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000010}
		_ = cmd.Start()
		return
	}

	title := "GoAMPP — " + name
	cmd := exec.Command(conEmu, "/Title", title, "/cmd", "cmd.exe")
	cmd.Env = env
	_ = cmd.Start()
}

func switchSvcTab(idx int) {
	if idx == activeSvcTabIdx && idx != 0 {
		return
	}
	activeSvcTabIdx = idx
	targetGroups := svcTabGroups[idx].groups

	ncols := (contentW + cardGap) / (cardW + cardGap)
	if ncols < 1 {
		ncols = 1
	}

	pos := 0
	for _, c := range serviceCards {
		ms := app.services[c.srcIdx]
		group := groupForKind(ms.Conf.Kind)
		visible := len(targetGroups) == 0 || containsInt32(targetGroups, group)

		if !visible {
			c.cardCtrl.Hwnd().ShowWindow(co.SW_HIDE)
			continue
		}

		col := pos % ncols
		row := pos / ncols
		newX := 10 + col*(cardW+cardGap)
		newY := cardGridY + row*(cardH+cardGap)
		px, py := ui.Dpi(newX, newY)
		c.cardCtrl.Hwnd().SetWindowPos(win.HWND(0),
			win.POINT{X: int32(px), Y: int32(py)},
			win.SIZE{},
			co.SWP_NOZORDER|co.SWP_NOSIZE|co.SWP_NOACTIVATE)
		c.cardCtrl.Hwnd().ShowWindow(co.SW_SHOWNA)

		c.rect = win.RECT{
			Left: int32(newX), Top: int32(newY),
			Right: int32(newX + cardW), Bottom: int32(newY + cardH),
		}
		pos++
	}

	for i, btn := range svcTabBtns {
		scheme := SchemeSidebar
		if i == idx {
			scheme = SchemePrimary
		}
		buttonSchemes[btn.CtrlId()] = scheme
		btn.Hwnd().InvalidateRect(nil, true)
	}

	if svcTabParent != nil {
		svcTabParent.Hwnd().RedrawWindow(nil, 0,
			co.RDW_INVALIDATE|co.RDW_ERASE|co.RDW_ALLCHILDREN|co.RDW_UPDATENOW)
	}
}

func containsInt32(slice []int32, val int32) bool {
	for _, v := range slice {
		if v == val {
			return true
		}
	}
	return false
}

func refreshServiceList() {
	if len(serviceCards) == 0 {
		return
	}
	active := activeWebServer()
	for _, c := range serviceCards {
		ms := app.services[c.srcIdx]
		running := ms.Service != nil && ms.Service.Running()
		installed := IsInstalled(ms.Conf.Name, app.baseDir) || ms.Service == nil

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

		if isWebKindCard(c) && ms.Conf.Name != active {
			status = "Inactive — pick " + ms.Conf.Name + " up top"
		}
		c.statusLbl.Hwnd().SetWindowText(status)

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

		setCardIcon(c, ms.Conf.Name)
	}
}

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

func buildEditorPage(parent *ui.Control) {
	ui.NewStatic(parent, ui.OptsStatic().
		Text("File:").
		Position(ui.Dpi(10, 34)))
	edDropdown = ui.NewComboBox(parent, ui.OptsComboBox().
		Position(ui.Dpi(50, 30)).
		Width(ui.DpiX(360)))

	edDropdown.On().CbnSelChange(func() {
		idx := edDropdown.SelectedIndex()
		if idx < 0 || idx >= len(edKnownPaths) {
			return
		}
		loadFileIntoEditor(edKnownPaths[idx])
	})

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

	ui.NewStatic(parent, ui.OptsStatic().
		Text("Path:").
		Position(ui.Dpi(10, 62)))
	edPathLabel = ui.NewStatic(parent, ui.OptsStatic().
		Text("(no file loaded)").
		Position(ui.Dpi(50, 62)).
		Size(ui.Dpi(contentW-60, 16)))

	edContent = ui.NewEdit(parent, ui.OptsEdit().
		Position(ui.Dpi(10, 86)).
		Width(ui.DpiX(contentW-20)).
		Height(ui.DpiY(contentH-100)).
		CtrlStyle(co.ES_MULTILINE|co.ES_AUTOVSCROLL|co.ES_AUTOHSCROLL|co.ES_WANTRETURN|co.ES_NOHIDESEL).
		WndStyle(co.WS_CHILD|co.WS_VISIBLE|co.WS_TABSTOP|co.WS_VSCROLL|co.WS_HSCROLL|co.WS_BORDER))

}

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

	for _, ms := range app.services {
		if ms.Conf.ConfigFile == "" {
			continue
		}
		add(ms.Conf.Name, ExpandPath(ms.Conf.ConfigFile, app.baseDir))
	}

	add("GoAMPP config", filepath.Join(app.baseDir, "config.json"))

	add("Apache vhosts", ExpandPath(app.cfg.Settings.ApacheVhostsInclude, app.baseDir))

	add("Windows hosts", DefaultHostsFile())

	if len(edKnownPaths) > 0 {
		edDropdown.SelectIndex(0)
		loadFileIntoEditor(edKnownPaths[0])
	}
}

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

	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\n", "\r\n")
	edContent.SetText(text)
	edPathLabel.Hwnd().SetWindowText(path)

	for i, p := range edKnownPaths {
		if p == path {
			edDropdown.SelectIndex(i)
			break
		}
	}
	app.appendLog(fmt.Sprintf("editor: loaded %s", path))
}

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

	if err := atomicWrite(path, data); err != nil {
		app.appendLog(fmt.Sprintf("editor save: %v", err))
		return
	}
	app.appendLog(fmt.Sprintf("editor: saved %s (%d bytes)", path, len(data)))
}

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

		defer func() {
			if r := recover(); r != nil {
				app.appendLog(fmt.Sprintf("vhost row select: %v", r))
			}
		}()
		if p.UChanged&co.LVIF_STATE == 0 {
			return
		}

		if p.UNewState&0x0002 == 0 {
			return
		}
		idx := int(p.IItem)
		if idx < 0 || idx >= len(app.cfg.Vhosts) {
			return
		}

		if vhostDomainNm == nil || vhostDomainExt == nil ||
			vhostDocRoot == nil || vhostPort == nil || vhostServer == nil {
			return
		}
		v := app.cfg.Vhosts[idx]

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

	addBtn("Save", 90, SchemeSuccess, func() {
		v, err := readVhostForm()
		if err != nil {
			app.appendLog("vhost save: " + err.Error())
			return
		}
		if idx := selectedVhostIndex(); idx >= 0 {

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

func buildSettingsPage(parent *ui.Control) {
	y := 10
	section := func(title string) {
		ui.NewStatic(parent, ui.OptsStatic().
			Text(title).
			Position(ui.Dpi(10, y)).
			Size(ui.Dpi(contentW-20, 16)))

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

	section("PATHS")
	row("Install dir", app.baseDir)
	row("Config", filepath.Join(app.baseDir, "config.json"))
	row("Apache vhosts", ExpandPath(app.cfg.Settings.ApacheVhostsInclude, app.baseDir))
	row("Nginx sites", ExpandPath(app.cfg.Settings.NginxSitesDir, app.baseDir))
	row("Hosts file", DefaultHostsFile())
	row("Downloads cache", filepath.Join(app.baseDir, "downloads"))

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

	y += 12
	section("ACTIONS")

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

	addBtn("Open psql Console", SchemePrimary, func() {
		pgBin := filepath.Join(app.baseDir, "bin", "pgsql", "bin")
		if _, err := os.Stat(filepath.Join(pgBin, "psql.exe")); err != nil {
			app.appendLog("psql: PostgreSQL not installed — install it from the Services tab first")
			return
		}

		cmd := exec.Command("cmd", "/c", "start", "", "cmd", "/K",
			"set PATH="+pgBin+";%PATH% && cd /d "+app.baseDir+
				" && echo PostgreSQL console — try: psql -U postgres")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: false}
		_ = cmd.Start()
	})
	addBtn("Quit GoAMPP", SchemeDanger, func() {
		quitApp(app.wnd)
	})

	y = rowY + btnH + gapY + 16
	section("ABOUT")
	row("Project", "GoAMPP — Native Windows local dev stack")
	row("Author", "imtaqin (fdciabdul)")
	row("Version", "v0.6.1")
	row("GitHub", "https://github.com/imtaqin/goampp")
	row("Website", "https://imtaqin.id")

	y += 4
	donateBtn := newColoredButton(parent, ui.OptsButton().
		Text("☕ Donate via Saweria").
		Position(ui.Dpi(col0, y)).
		Width(ui.DpiX(200)).Height(ui.DpiY(btnH)),
		SchemeSuccess)
	donateBtn.On().BnClicked(func() {
		openPath("https://saweria.co/fdciabdul")
	})
	y += btnH + gapY

	ghBtn := newColoredButton(parent, ui.OptsButton().
		Text("★ Star on GitHub").
		Position(ui.Dpi(col0+210, y-btnH-gapY)).
		Width(ui.DpiX(160)).Height(ui.DpiY(btnH)),
		SchemePrimary)
	ghBtn.On().BnClicked(func() {
		openPath("https://github.com/imtaqin/goampp")
	})
}

func buildProjectsPage(parent *ui.Control) {

	ui.NewStatic(parent, ui.OptsStatic().
		Text("Framework:").
		Position(ui.Dpi(10, 34)))

	frameworkNames := make([]string, len(Frameworks))
	for i, f := range Frameworks {
		frameworkNames[i] = f.Name
	}
	projFramework = ui.NewComboBox(parent, ui.OptsComboBox().
		Position(ui.Dpi(90, 30)).
		Width(ui.DpiX(200)).
		Texts(frameworkNames...).
		Select(0))

	ui.NewStatic(parent, ui.OptsStatic().
		Text("Name:").
		Position(ui.Dpi(305, 34)))
	projName = ui.NewEdit(parent, ui.OptsEdit().
		Position(ui.Dpi(345, 30)).
		Width(ui.DpiX(110)))

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

	newColoredButton(parent, ui.OptsButton().
		Text("Create Project").
		Position(ui.Dpi(705, 28)).
		Width(ui.DpiX(95)).Height(ui.DpiY(26)),
		SchemeSuccess).
		On().BnClicked(func() {
		onCreateProject()
	})

	runtimeY := 66
	ui.NewStatic(parent, ui.OptsStatic().
		Text("Runtime status: "+runtimeStatusText()).
		Position(ui.Dpi(10, runtimeY)).
		Size(ui.Dpi(contentW-20, 16)))

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

	name = slugify(name)

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

func splitDomain(domain string) (string, string) {
	domain = strings.TrimSpace(domain)
	i := strings.LastIndex(domain, ".")
	if i < 0 {
		return domain, ".test"
	}
	return domain[:i], domain[i:]
}

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

func refreshProjectList() {
	if projList == nil {
		return
	}
	projList.DeleteAllItems()
	for _, p := range app.cfg.Projects {
		projList.AddItem(p.Name, p.Framework, p.Domain, p.DocRoot)
	}
}

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

func deleteProject(idx int) {
	if idx < 0 || idx >= len(app.cfg.Projects) {
		return
	}
	p := app.cfg.Projects[idx]
	projectDir := filepath.Join(app.baseDir, "www", p.Name)

	app.appendLog(fmt.Sprintf("projects: deleting '%s' (%s)...", p.Name, p.Domain))
	if err := os.RemoveAll(projectDir); err != nil {
		app.appendLog("projects delete: " + err.Error())

	}

	app.cfg.Projects = append(app.cfg.Projects[:idx], app.cfg.Projects[idx+1:]...)

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

func startService(idx int) {
	if idx < 0 || idx >= len(app.services) {
		return
	}
	ms := app.services[idx]
	if ms.Service == nil {

		if _, ok := DownloadCatalog[ms.Conf.Name]; ok && !IsInstalled(ms.Conf.Name, app.baseDir) {
			downloadInBackground(ms.Conf.Name)
			return
		}
		if ms.Conf.OpenURL != "" {
			openPath(ms.Conf.OpenURL)
		}
		return
	}

	_, hasCatalog := DownloadCatalog[ms.Conf.Name]
	if hasCatalog && !IsInstalled(ms.Conf.Name, app.baseDir) {
		name := ms.Conf.Name
		svc := ms.Service

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

	if filepath.IsAbs(ms.Service.ExePath) {
		if _, err := os.Stat(ms.Service.ExePath); err != nil {
			app.appendLog(fmt.Sprintf("[%s] exe missing: %s", ms.Conf.Name, ms.Service.ExePath))
			return
		}
	}
	if err := ms.Service.Start(); err != nil {
		app.appendLog(fmt.Sprintf("[%s] start: %v", ms.Conf.Name, err))
	}
}

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

func countEnabledServices() int {
	n := 0
	for _, s := range app.cfg.Services {
		if s.Enabled {
			n++
		}
	}
	return n
}

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
