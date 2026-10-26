//go:build linux

package linuxapp

import (
	"bytes"
	"encoding/binary"
	"image/color"
	"path"
	"strings"
	"sync"
	"time"
	"unicode"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"goampp/assets"
)

var (
	linuxAssetMu    sync.Mutex
	linuxAssetCache = map[string]fyne.Resource{}

	linuxToolProbeMu        sync.Mutex
	linuxToolVersionCache   = map[string]string{}
	linuxToolProbePending   = map[string]bool{}
	linuxToolProbeWaiters   = map[string][]*widget.Label{}
	linuxToolProbeSemaphore = make(chan struct{}, 4)
)

func linuxCachedToolVersion(name string) (string, bool) {
	linuxToolProbeMu.Lock()
	defer linuxToolProbeMu.Unlock()
	version, ok := linuxToolVersionCache[name]
	return version, ok
}

func linuxRequestToolVersion(name, binary string, args []string, label *widget.Label) {
	linuxToolProbeMu.Lock()
	if version, ok := linuxToolVersionCache[name]; ok {
		linuxToolProbeMu.Unlock()
		label.SetText(binary + " · " + version)
		return
	}
	linuxToolProbeWaiters[name] = append(linuxToolProbeWaiters[name], label)
	if linuxToolProbePending[name] {
		linuxToolProbeMu.Unlock()
		return
	}
	linuxToolProbePending[name] = true
	linuxToolProbeMu.Unlock()

	go func() {
		linuxToolProbeSemaphore <- struct{}{}
		defer func() { <-linuxToolProbeSemaphore }()
		output, err := runCommandWithTimeout(2*time.Second, binary, args...)
		version := ""
		if err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
				line = strings.TrimSpace(line)
				if line != "" {
					version = line
					break
				}
			}
		}
		fyne.Do(func() {
			linuxToolProbeMu.Lock()
			delete(linuxToolProbePending, name)
			waiters := append([]*widget.Label(nil), linuxToolProbeWaiters[name]...)
			delete(linuxToolProbeWaiters, name)
			if version != "" {
				linuxToolVersionCache[name] = version
			}
			linuxToolProbeMu.Unlock()
			if version != "" {
				for _, waiter := range waiters {
					waiter.SetText(binary + " · " + version)
				}
			}
		})
	}()
}

func linuxResetToolVersionCache() {
	linuxToolProbeMu.Lock()
	linuxToolVersionCache = make(map[string]string)
	linuxToolProbeMu.Unlock()
}


var linuxServiceAssets = map[string]string{
	"Apache HTTP Server": "icons/apache.ico",
	"Nginx":              "logos/NGINX.png",
	"PHP-FPM":             "logos/PHP.png",
	"MariaDB / MySQL":     "logos/MySQL.png",
	"PostgreSQL":          "logos/PostgresSQL.png",
	"Redis":               "logos/Redis.png",
	"RabbitMQ":            "icons/rabbitmq.ico",
}

var linuxToolAssets = map[string]string{
	"PHP":      "logos/PHP.png",
	"Composer": "logos/Composer.png",
	"Node.js":  "logos/Node.js.png",
	"npm":      "logos/Node.js.png",
	"Python":   "logos/Python.png",
	"Go":       "logos/Go.png",
	"Java":     "logos/Java.png",
	"Rust":     "logos/Rust.png",
}

// linuxAsset loads an image from the repository's embedded asset filesystem.
// ICO files are converted to their largest embedded PNG image for Fyne.
func linuxAsset(name string) fyne.Resource {
	if name == "" {
		return nil
	}
	linuxAssetMu.Lock()
	defer linuxAssetMu.Unlock()
	if res, ok := linuxAssetCache[name]; ok {
		return res
	}

	data, err := assets.FS.ReadFile(path.Clean(name))
	if err != nil {
		linuxAssetCache[name] = nil
		return nil
	}
	if strings.EqualFold(path.Ext(name), ".ico") {
		data = pngFromLinuxICO(data)
		if data == nil {
			linuxAssetCache[name] = nil
			return nil
		}
	}
	res := fyne.NewStaticResource(path.Base(name), data)
	linuxAssetCache[name] = res
	return res
}

func linuxLogoResource() fyne.Resource {
	return linuxAsset("logo.png")
}

func linuxServiceResource(name string) fyne.Resource {
	return linuxAsset(linuxServiceAssets[name])
}

func linuxToolResource(name string) fyne.Resource {
	return linuxAsset(linuxToolAssets[name])
}

func pngFromLinuxICO(data []byte) []byte {
	pngSignature := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	if len(data) < 6 || binary.LittleEndian.Uint16(data[2:]) != 1 {
		return nil
	}
	count := int(binary.LittleEndian.Uint16(data[4:]))
	var best []byte
	bestWidth := -1
	for i := 0; i < count; i++ {
		entry := 6 + i*16
		if entry+16 > len(data) {
			break
		}
		width := int(data[entry])
		if width == 0 {
			width = 256
		}
		size := int(binary.LittleEndian.Uint32(data[entry+8:]))
		offset := int(binary.LittleEndian.Uint32(data[entry+12:]))
		if size <= 0 || offset < 0 || offset+size > len(data) {
			continue
		}
		img := data[offset : offset+size]
		if bytes.HasPrefix(img, pngSignature) && width > bestWidth {
			best = img
			bestWidth = width
		}
	}
	return best
}

func linuxImage(res fyne.Resource, size float32, fallbackName string) fyne.CanvasObject {
	if res == nil {
		return linuxLetterMark(fallbackName, size)
	}
	img := canvas.NewImageFromResource(res)
	img.FillMode = canvas.ImageFillContain
	img.ScaleMode = canvas.ImageScaleSmooth
	img.SetMinSize(fyne.NewSize(size, size))
	return container.NewGridWrap(fyne.NewSize(size, size), container.NewCenter(img))
}

var linuxAvatarPalette = []uint32{0x2563eb, 0x0f766e, 0x7c3aed, 0xb45309, 0x0369a1, 0xbe123c, 0x4d7c0f, 0x6d28d9}

func linuxLetterMark(name string, size float32) fyne.CanvasObject {
	sum := 0
	for _, r := range name {
		sum += int(r)
	}
	value := linuxAvatarPalette[sum%len(linuxAvatarPalette)]
	bg := canvas.NewRectangle(color.NRGBA{R: uint8(value >> 16), G: uint8(value >> 8), B: uint8(value), A: 0xff})
	bg.CornerRadius = size / 4
	letter := "?"
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			letter = strings.ToUpper(string(r))
			break
		}
	}
	label := canvas.NewText(letter, color.White)
	label.Alignment = fyne.TextAlignCenter
	label.TextStyle = fyne.TextStyle{Bold: true}
	label.TextSize = size * 0.45
	return container.NewGridWrap(fyne.NewSize(size, size), container.NewStack(bg, container.NewCenter(label)))
}

func linuxPad(obj fyne.CanvasObject, top, bottom, left, right float32) fyne.CanvasObject {
	return container.New(layout.NewCustomPaddedLayout(top, bottom, left, right), obj)
}

func linuxSurface(content fyne.CanvasObject, top, bottom, left, right float32) fyne.CanvasObject {
	bg := canvas.NewRectangle(theme.Color(theme.ColorNameInputBackground))
	bg.CornerRadius = 12
	bg.StrokeColor = theme.Color(theme.ColorNameSeparator)
	bg.StrokeWidth = 1
	return container.NewStack(bg, linuxPad(content, top, bottom, left, right))
}

func linuxGap(height float32) fyne.CanvasObject {
	gap := canvas.NewRectangle(color.Transparent)
	gap.SetMinSize(fyne.NewSize(1, height))
	return gap
}

func linuxHGap(width float32) fyne.CanvasObject {
	gap := canvas.NewRectangle(color.Transparent)
	gap.SetMinSize(fyne.NewSize(width, 1))
	return gap
}

func linuxStatusBadge(label string, value color.Color) fyne.CanvasObject {
	dot := canvas.NewCircle(value)
	dotBox := container.NewGridWrap(fyne.NewSize(6, 6), dot)
	text := canvas.NewText(label, theme.Color(linuxColorMuted))
	text.TextSize = 11.5
	return container.NewHBox(container.NewCenter(dotBox), text)
}

func linuxPageIntro(title, subtitle string, icon fyne.Resource) fyne.CanvasObject {
	iconView := container.NewGridWrap(fyne.NewSize(42, 42), widget.NewIcon(icon))
	heading := container.NewVBox(
		widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewLabel(subtitle),
	)
	return linuxSurface(container.NewHBox(iconView, linuxHGap(12), heading), 10, 10, 12, 12)
}


type linuxSegmented struct {
	buttons  []*widget.Button
	selected int
	onChange func(int)
	obj      fyne.CanvasObject
}

func newLinuxSegmented(options []string, onChange func(int)) *linuxSegmented {
	s := &linuxSegmented{onChange: onChange}
	row := container.NewHBox()
	for i, option := range options {
		index := i
		button := widget.NewButton(option, func() { s.setSelected(index, true) })
		s.buttons = append(s.buttons, button)
		row.Add(button)
	}
	bg := canvas.NewRectangle(theme.Color(theme.ColorNameInputBackground))
	bg.CornerRadius = 6
	bg.StrokeColor = theme.Color(theme.ColorNameSeparator)
	bg.StrokeWidth = 1
	s.obj = container.NewStack(bg, container.New(layout.NewCustomPaddedLayout(2, 2, 2, 2), row))
	if len(options) > 0 {
		s.setSelected(0, false)
	}
	return s
}

func (s *linuxSegmented) setSelected(index int, notify bool) {
	if index < 0 || index >= len(s.buttons) {
		return
	}
	s.selected = index
	for i, button := range s.buttons {
		if i == index {
			button.Importance = widget.MediumImportance
		} else {
			button.Importance = widget.LowImportance
		}
		button.Refresh()
	}
	if notify && s.onChange != nil {
		s.onChange(index)
	}
}
