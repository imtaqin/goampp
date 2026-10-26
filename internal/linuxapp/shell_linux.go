//go:build linux

package linuxapp

import (
	"fmt"
	"image/color"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

type linuxPage struct {
	key, title string
	icon       fyne.Resource
	actions    fyne.CanvasObject
	body       fyne.CanvasObject
	nav        *linuxNavItem
}

type linuxShellState struct {
	app     fyne.App
	window  fyne.Window
	root    fyne.CanvasObject
	pages   []*linuxPage
	current string

	headerTitle   *canvas.Text
	headerActions *fyne.Container
	statusText    *widget.Label
	statusDot     *canvas.Circle
	logList       *widget.List
	logLines      []string
	pendingLogs   []string
	logFlushQueued bool
	logBody       fyne.CanvasObject
	logTools      *fyne.Container
	logToggle     *widget.Button
	logFollow     bool
	logExpanded   bool
	status        string
	logMu         sync.Mutex
}

var linuxShell linuxShellState

func linuxBoldText(value string, size float32) *canvas.Text {
	t := canvas.NewText(value, theme.Color(theme.ColorNameForeground))
	t.TextStyle = fyne.TextStyle{Bold: true}
	t.TextSize = size
	return t
}

func linuxMutedText(value string, size float32) *canvas.Text {
	t := canvas.NewText(value, theme.Color(linuxColorMuted))
	t.TextSize = size
	return t
}

func linuxHSpace(width float32) fyne.CanvasObject {
	r := canvas.NewRectangle(color.Transparent)
	r.SetMinSize(fyne.NewSize(width, 1))
	return r
}

func linuxMinSize(obj fyne.CanvasObject, width, height float32) fyne.CanvasObject {
	r := canvas.NewRectangle(color.Transparent)
	r.SetMinSize(fyne.NewSize(width, height))
	return container.NewStack(r, obj)
}

func attachLinuxShell(a fyne.App, w fyne.Window, pages []*linuxPage, initial string) {
	linuxShell.app = a
	linuxShell.window = w
	linuxShell.pages = pages
	linuxShell.logFollow = true
	linuxShell.status = "Ready"
	linuxShell.current = initial
	renderLinuxShell()
}

func renderLinuxShell() {
	if linuxShell.window == nil {
		return
	}
	linuxShell.headerTitle = linuxBoldText("", 17)
	linuxShell.headerActions = container.NewHBox()
	pageHost := container.NewStack()
	for _, p := range linuxShell.pages {
		p.body.Hide()
		pageHost.Add(p.body)
	}
	header := container.NewBorder(nil, nil, container.NewCenter(linuxShell.headerTitle), linuxShell.headerActions)
	main := container.NewBorder(
		container.New(layout.NewCustomPaddedLayout(12, 8, 18, 18), header),
		buildLinuxBottomBar(),
		nil, nil,
		container.New(layout.NewCustomPaddedLayout(0, 10, 18, 18), pageHost),
	)
	bg := canvas.NewRectangle(theme.Color(theme.ColorNameBackground))
	linuxShell.root = container.NewBorder(nil, nil, buildLinuxSidebar(), nil, container.NewStack(bg, main))
	linuxShell.window.SetContent(linuxShell.root)
	linuxShowPage(linuxShell.current)
}

func linuxShowPage(key string) {
	for _, p := range linuxShell.pages {
		if p.key != key {
			p.body.Hide()
			if p.nav != nil {
				p.nav.setSelected(false)
			}
			continue
		}
		p.body.Show()
		if p.nav != nil {
			p.nav.setSelected(true)
		}
		linuxShell.headerTitle.Text = p.title
		linuxShell.headerTitle.Refresh()
		linuxShell.headerActions.Objects = nil
		if p.actions != nil {
			linuxShell.headerActions.Objects = []fyne.CanvasObject{p.actions}
		}
		linuxShell.headerActions.Refresh()
	}
	linuxShell.current = key
}

func buildLinuxSidebar() fyne.CanvasObject {
	brand := container.NewHBox(
		linuxImage(linuxLogoResource(), 24, "GoAMPP"),
		linuxHSpace(7),
		container.NewCenter(linuxBoldText("GoAMPP", 14)),
	)
	nav := container.NewVBox()
	for _, page := range linuxShell.pages {
		p := page
		p.nav = newLinuxNavItem(p.title, p.icon, func() { linuxShowPage(p.key) })
		nav.Add(p.nav)
	}
	brandBlock := container.NewVBox(
		container.New(layout.NewCustomPaddedLayout(0, 0, 6, 0), brand),
		linuxVSpace(14),
		nav,
	)
	footer := container.NewVBox(
		container.New(layout.NewCustomPaddedLayout(0, 0, 5, 0), linuxMutedText("LINUX NATIVE MODE", 10.5)),
		linuxMutedText("Distribution-managed services", 10.5),
		linuxVSpace(4),
		linuxMutedText(linuxVersionLabel(), 10),
	)
	bg := canvas.NewRectangle(theme.Color(linuxColorSidebar))
	edge := canvas.NewRectangle(theme.Color(theme.ColorNameSeparator))
	return container.NewBorder(
		nil, nil, nil, container.NewGridWrap(fyne.NewSize(1, 1), edge),
		container.NewStack(bg, linuxMinSize(container.New(layout.NewCustomPaddedLayout(14, 10, 10, 10),
			container.NewBorder(brandBlock, footer, nil, nil)), 184, 0)),
	)
}

func buildLinuxBottomBar() fyne.CanvasObject {
	linuxShell.statusText = widget.NewLabel(linuxShell.status)
	linuxShell.statusText.SizeName = theme.SizeNameCaptionText
	linuxShell.statusText.TextStyle = fyne.TextStyle{Monospace: true}
	linuxShell.statusText.Truncation = fyne.TextTruncateEllipsis
	linuxShell.statusText.Importance = widget.LowImportance
	linuxShell.statusDot = canvas.NewCircle(theme.Color(theme.ColorNamePrimary))

	linuxShell.logList = widget.NewList(
		func() int { return len(linuxShell.logLines) },
		func() fyne.CanvasObject {
			t := canvas.NewText("", theme.Color(theme.ColorNameForeground))
			t.TextStyle = fyne.TextStyle{Monospace: true}
			t.TextSize = 11.5
			return t
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			t := obj.(*canvas.Text)
			if id >= len(linuxShell.logLines) {
				t.Text = ""
			} else {
				line := linuxShell.logLines[id]
				t.Text = line
				t.Color = linuxLogColor(line)
			}
			t.Refresh()
		},
	)
	linuxShell.logList.HideSeparators = true
	logBG := canvas.NewRectangle(theme.Color(linuxColorLogBg))
	logBG.CornerRadius = 6
	logBG.StrokeColor = theme.Color(linuxColorCardBorder)
	logBG.StrokeWidth = 1
	linuxShell.logBody = linuxMinSize(container.NewStack(logBG,
		container.New(layout.NewCustomPaddedLayout(2, 2, 6, 6), linuxShell.logList)), 0, 140)
	linuxShell.logBody.Hide()

	follow := widget.NewCheck("Follow", func(checked bool) { linuxShell.logFollow = checked })
	follow.SetChecked(linuxShell.logFollow)
	clear := widget.NewButtonWithIcon("", theme.ContentClearIcon(), func() {
		linuxShell.logMu.Lock()
		linuxShell.logLines = nil
		linuxShell.logMu.Unlock()
		linuxShell.logList.Refresh()
		linuxSetStatus("Log cleared")
	})
	copyButton := widget.NewButtonWithIcon("", theme.ContentCopyIcon(), func() {
		linuxShell.logMu.Lock()
		content := strings.Join(linuxShell.logLines, "\n")
		linuxShell.logMu.Unlock()
		linuxShell.window.Clipboard().SetContent(content)
		linuxSetStatus("Log copied to clipboard")
	})
	linuxShell.logTools = container.NewHBox(follow, clear, copyButton)
	linuxShell.logTools.Hide()
	linuxShell.logToggle = widget.NewButtonWithIcon("Log", theme.MenuDropUpIcon(), nil)
	linuxShell.logToggle.IconPlacement = widget.ButtonIconTrailingText
	linuxShell.logToggle.Importance = widget.LowImportance
	linuxShell.logToggle.OnTapped = func() {
		linuxShell.logExpanded = !linuxShell.logExpanded
		if linuxShell.logExpanded {
			linuxShell.logBody.Show()
			linuxShell.logTools.Show()
			linuxShell.logToggle.SetIcon(theme.MenuDropDownIcon())
			linuxShell.logList.ScrollToBottom()
		} else {
			linuxShell.logBody.Hide()
			linuxShell.logTools.Hide()
			linuxShell.logToggle.SetIcon(theme.MenuDropUpIcon())
		}
	}
	dot := container.NewCenter(container.NewGridWrap(fyne.NewSize(6, 6), linuxShell.statusDot))
	line := container.NewBorder(nil, nil,
		container.NewHBox(dot, linuxHSpace(3)),
		container.NewHBox(linuxShell.logTools, linuxShell.logToggle),
		linuxShell.statusText,
	)
	edge := canvas.NewRectangle(theme.Color(theme.ColorNameSeparator))
	edge.SetMinSize(fyne.NewSize(1, 1))
	return container.NewBorder(edge, nil, nil, nil,
		container.New(layout.NewCustomPaddedLayout(4, 4, 18, 12), container.NewVBox(linuxShell.logBody, line)))
}

func linuxSetStatus(message string) {
	linuxShell.status = message
	fyne.Do(func() {
		if linuxShell.statusText != nil {
			linuxShell.statusText.SetText(message)
		}
	})
}

func linuxAddLog(message string) {
	stamped := time.Now().Format("15:04:05") + "  " + strings.TrimRight(message, "\r\n")
	linuxShell.logMu.Lock()
	linuxShell.pendingLogs = append(linuxShell.pendingLogs, stamped)
	shouldQueue := !linuxShell.logFlushQueued
	linuxShell.logFlushQueued = true
	linuxShell.logMu.Unlock()
	if shouldQueue {
		fyne.Do(flushLinuxPendingLogs)
	}
}

func flushLinuxPendingLogs() {
	linuxShell.logMu.Lock()
	pending := linuxShell.pendingLogs
	linuxShell.pendingLogs = nil
	linuxShell.logFlushQueued = false
	if len(pending) == 0 {
		linuxShell.logMu.Unlock()
		return
	}
	linuxShell.logLines = append(linuxShell.logLines, pending...)
	if len(linuxShell.logLines) > 2000 {
		linuxShell.logLines = append([]string(nil), linuxShell.logLines[len(linuxShell.logLines)-1500:]...)
	}
	linuxShell.logMu.Unlock()

	if linuxShell.logList != nil {
		linuxShell.logList.Refresh()
		if linuxShell.logFollow {
			linuxShell.logList.ScrollToBottom()
		}
	}
	if linuxShell.statusText != nil {
		last := pending[len(pending)-1]
		if parts := strings.SplitN(last, "  ", 2); len(parts) == 2 {
			linuxShell.statusText.SetText(parts[1])
		} else {
			linuxShell.statusText.SetText(last)
		}
	}
}
func linuxLogColor(line string) color.Color {
	lower := strings.ToLower(line)
	switch {
	case strings.Contains(lower, "failed"), strings.Contains(lower, "error"), strings.Contains(lower, "forbidden"):
		return linuxHex(0xef4444)
	case strings.Contains(lower, "warning"), strings.Contains(lower, "stopping"):
		return linuxHex(0xf59e0b)
	case strings.Contains(lower, "created"), strings.Contains(lower, "running"), strings.Contains(lower, "started"), strings.Contains(lower, "stopped"), strings.Contains(lower, "installed"):
		return linuxHex(0x22c55e)
	default:
		return theme.Color(linuxColorMuted)
	}
}

type linuxNavItem struct {
	widget.BaseWidget
	label    string
	icon     fyne.Resource
	selected bool
	hovered  bool
	onTap    func()
	bg       *canvas.Rectangle
	img      *widget.Icon
	text     *canvas.Text
}

func newLinuxNavItem(label string, icon fyne.Resource, onTap func()) *linuxNavItem {
	n := &linuxNavItem{label: label, icon: icon, onTap: onTap}
	n.ExtendBaseWidget(n)
	return n
}

func (n *linuxNavItem) CreateRenderer() fyne.WidgetRenderer {
	n.bg = canvas.NewRectangle(color.Transparent)
	n.bg.CornerRadius = 6
	n.img = widget.NewIcon(n.icon)
	n.text = canvas.NewText(n.label, theme.Color(theme.ColorNameForeground))
	n.text.TextSize = 12.5
	row := container.NewHBox(container.NewCenter(container.NewGridWrap(fyne.NewSize(15, 15), n.img)), n.text)
	content := container.NewStack(n.bg, container.New(layout.NewCustomPaddedLayout(5, 5, 8, 8), row))
	n.applyState()
	return widget.NewSimpleRenderer(content)
}

func (n *linuxNavItem) setSelected(selected bool) {
	n.selected = selected
	n.applyState()
}

func (n *linuxNavItem) applyState() {
	if n.bg == nil {
		return
	}
	switch {
	case n.selected:
		n.bg.FillColor = theme.Color(linuxColorNavActive)
		n.text.Color = theme.Color(theme.ColorNameForeground)
		n.text.TextStyle = fyne.TextStyle{Bold: true}
		n.img.SetResource(n.icon)
	case n.hovered:
		n.bg.FillColor = theme.Color(theme.ColorNameHover)
		n.text.Color = theme.Color(theme.ColorNameForeground)
		n.text.TextStyle = fyne.TextStyle{}
		n.img.SetResource(n.icon)
	default:
		n.bg.FillColor = color.Transparent
		n.text.Color = theme.Color(linuxColorMuted)
		n.text.TextStyle = fyne.TextStyle{}
		n.img.SetResource(theme.NewDisabledResource(n.icon))
	}
	n.bg.Refresh()
	n.text.Refresh()
}

func (n *linuxNavItem) Tapped(*fyne.PointEvent) {
	if n.onTap != nil {
		n.onTap()
	}
}
func (n *linuxNavItem) MouseIn(*desktop.MouseEvent) { n.hovered = true; n.applyState() }
func (n *linuxNavItem) MouseMoved(*desktop.MouseEvent) {}
func (n *linuxNavItem) MouseOut() { n.hovered = false; n.applyState() }
func (n *linuxNavItem) Cursor() desktop.Cursor { return desktop.PointerCursor }

func linuxVSpace(height float32) fyne.CanvasObject {
	gap := canvas.NewRectangle(color.Transparent)
	gap.SetMinSize(fyne.NewSize(1, height))
	return gap
}

func linuxSectionLabel(value string) fyne.CanvasObject {
	return container.New(layout.NewCustomPaddedLayout(8, 2, 2, 0), linuxMutedText(value, 10.5))
}

func linuxInfoPage(title, body string, icon fyne.Resource) fyne.CanvasObject {
	iconView := container.NewGridWrap(fyne.NewSize(34, 34), widget.NewIcon(icon))
	description := widget.NewLabel(body)
	description.Wrapping = fyne.TextWrapWord
	content := container.NewVBox(
		container.NewHBox(iconView, linuxHSpace(10), linuxBoldText(title, 14)),
		linuxVSpace(8),
		description,
	)
	return container.NewVScroll(linuxPad(linuxSurface(content, 14, 14, 14, 14), 10, 10, 12, 12))
}

func linuxVersionLabel() string {
	return fmt.Sprintf("GoAMPP for Linux · %s", "native")
}


func linuxSameColor(left, right color.Color) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	l := color.NRGBAModel.Convert(left).(color.NRGBA)
	r := color.NRGBAModel.Convert(right).(color.NRGBA)
	return l == r
}

func linuxRecolorColor(current color.Color, role string) color.Color {
	pairs := []struct {
		dark, light color.Color
		name         fyne.ThemeColorName
	}{
		{linuxDarkPalette.bg, linuxLightPalette.bg, theme.ColorNameBackground},
		{linuxDarkPalette.sidebar, linuxLightPalette.sidebar, linuxColorSidebar},
		{linuxDarkPalette.card, linuxLightPalette.card, linuxColorCard},
		{linuxDarkPalette.border, linuxLightPalette.border, linuxColorCardBorder},
		{linuxDarkPalette.input, linuxLightPalette.input, theme.ColorNameInputBackground},
		{linuxDarkPalette.button, linuxLightPalette.button, theme.ColorNameButton},
		{linuxDarkPalette.hover, linuxLightPalette.hover, theme.ColorNameHover},
		{linuxDarkPalette.pressed, linuxLightPalette.pressed, theme.ColorNamePressed},
		{linuxDarkPalette.fg, linuxLightPalette.fg, theme.ColorNameForeground},
		{linuxDarkPalette.muted, linuxLightPalette.muted, linuxColorMuted},
		{linuxDarkPalette.primary, linuxLightPalette.primary, theme.ColorNamePrimary},
		{linuxDarkPalette.navActive, linuxLightPalette.navActive, linuxColorNavActive},
		{linuxDarkPalette.separator, linuxLightPalette.separator, theme.ColorNameSeparator},
		{linuxDarkPalette.menu, linuxLightPalette.menu, theme.ColorNameMenuBackground},
		{linuxDarkPalette.overlay, linuxLightPalette.overlay, theme.ColorNameOverlayBackground},
		{linuxDarkPalette.header, linuxLightPalette.header, theme.ColorNameHeaderBackground},
		{linuxDarkPalette.scroll, linuxLightPalette.scroll, theme.ColorNameScrollBar},
		{linuxDarkPalette.shadow, linuxLightPalette.shadow, theme.ColorNameShadow},
		{linuxDarkPalette.selection, linuxLightPalette.selection, theme.ColorNameSelection},
		{linuxDarkPalette.logBg, linuxLightPalette.logBg, linuxColorLogBg},
		{linuxDarkPalette.placeholder, linuxLightPalette.placeholder, theme.ColorNamePlaceHolder},
		{linuxDarkPalette.disabled, linuxLightPalette.disabled, theme.ColorNameDisabled},
		{linuxDarkPalette.disabledBtn, linuxLightPalette.disabledBtn, theme.ColorNameDisabledButton},
	}
	for _, pair := range pairs {
		switch role {
		case "text":
			if pair.name != theme.ColorNameForeground &&
				pair.name != linuxColorMuted &&
				pair.name != theme.ColorNamePrimary &&
				pair.name != theme.ColorNamePlaceHolder &&
				pair.name != theme.ColorNameDisabled {
				continue
			}
		case "accent":
			if pair.name != theme.ColorNamePrimary {
				continue
			}
		}
		if linuxSameColor(current, pair.dark) || linuxSameColor(current, pair.light) {
			return theme.Color(pair.name)
		}
	}
	return current
}
func linuxRecolorObject(obj fyne.CanvasObject) {
	switch item := obj.(type) {
	case *linuxNavItem:
		item.applyState()
	case *canvas.Rectangle:
		item.FillColor = linuxRecolorColor(item.FillColor, "surface")
		item.StrokeColor = linuxRecolorColor(item.StrokeColor, "surface")
	case *canvas.Text:
		item.Color = linuxRecolorColor(item.Color, "text")
	case *canvas.Circle:
		item.FillColor = linuxRecolorColor(item.FillColor, "accent")
	case *fyne.Container:
		for _, child := range item.Objects {
			linuxRecolorObject(child)
		}
	}
}

func refreshLinuxThemeSurfaces() {
	fyne.Do(func() {
		if linuxShell.root == nil || linuxShell.window == nil {
			return
		}
		linuxRecolorObject(linuxShell.root)
		linuxShell.window.Canvas().Refresh(linuxShell.root)
	})
}
