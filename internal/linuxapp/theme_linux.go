//go:build linux

package linuxapp

import (
	"image/color"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

const (
	linuxColorCard       fyne.ThemeColorName = "goampp.card"
	linuxColorCardBorder fyne.ThemeColorName = "goampp.cardBorder"
	linuxColorSidebar    fyne.ThemeColorName = "goampp.sidebar"
	linuxColorMuted      fyne.ThemeColorName = "goampp.muted"
	linuxColorNavActive  fyne.ThemeColorName = "goampp.navActive"
	linuxColorLogBg      fyne.ThemeColorName = "goampp.logBg"
)

type linuxPalette struct {
	bg, sidebar, card, border, input, button, hover, pressed, fg, muted,
	primary, navActive, separator, menu, overlay, header, scroll, shadow,
	selection, logBg, placeholder, disabled, disabledBtn color.Color
}

func linuxHex(value uint32) color.NRGBA {
	return color.NRGBA{R: uint8(value >> 16), G: uint8(value >> 8), B: uint8(value), A: 0xff}
}

func linuxHexA(value uint32, alpha uint8) color.NRGBA {
	c := linuxHex(value)
	c.A = alpha
	return c
}

var linuxDarkPalette = linuxPalette{
	bg: linuxHex(0x0c0c0e), sidebar: linuxHex(0x0f0f12), card: linuxHex(0x131316),
	border: linuxHex(0x222226), input: linuxHex(0x18181c), button: linuxHex(0x1c1c21),
	hover: linuxHexA(0xffffff, 0x0c), pressed: linuxHexA(0xffffff, 0x18),
	fg: linuxHex(0xe4e4e7), muted: linuxHex(0x85858f), primary: linuxHex(0x4f8cff),
	navActive: linuxHexA(0xffffff, 0x0e), separator: linuxHex(0x1f1f23),
	menu: linuxHex(0x17171b), overlay: linuxHex(0x131316), header: linuxHex(0x131316),
	scroll: linuxHexA(0xffffff, 0x26), shadow: linuxHexA(0x000000, 0x66),
	selection: linuxHexA(0x4f8cff, 0x40), logBg: linuxHex(0x0c0c0e),
	placeholder: linuxHex(0x5b5b64), disabled: linuxHex(0x55555e), disabledBtn: linuxHex(0x16161a),
}

var linuxLightPalette = linuxPalette{
	bg: linuxHex(0xfafafa), sidebar: linuxHex(0xf4f4f5), card: linuxHex(0xffffff),
	border: linuxHex(0xe4e4e7), input: linuxHex(0xffffff), button: linuxHex(0xf4f4f5),
	hover: linuxHexA(0x000000, 0x08), pressed: linuxHexA(0x000000, 0x12),
	fg: linuxHex(0x18181b), muted: linuxHex(0x71717a), primary: linuxHex(0x2563eb),
	navActive: linuxHexA(0x000000, 0x08), separator: linuxHex(0xe9e9ec),
	menu: linuxHex(0xffffff), overlay: linuxHex(0xffffff), header: linuxHex(0xf4f4f5),
	scroll: linuxHexA(0x000000, 0x26), shadow: linuxHexA(0x000000, 0x22),
	selection: linuxHexA(0x2563eb, 0x30), logBg: linuxHex(0xfafafa),
	placeholder: linuxHex(0xa1a1aa), disabled: linuxHex(0xa1a1aa), disabledBtn: linuxHex(0xf4f4f5),
}

type linuxGoamppTheme struct {
	forced *fyne.ThemeVariant
}

func newLinuxGoamppTheme(preference string) *linuxGoamppTheme {
	t := &linuxGoamppTheme{}
	switch strings.ToLower(strings.TrimSpace(preference)) {
	case "light":
		variant := theme.VariantLight
		t.forced = &variant
	case "dark":
		variant := theme.VariantDark
		t.forced = &variant
	}
	return t
}

func (t *linuxGoamppTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	if t.forced != nil {
		variant = *t.forced
	}
	p := &linuxDarkPalette
	if variant == theme.VariantLight {
		p = &linuxLightPalette
	}
	switch name {
	case theme.ColorNameBackground:
		return p.bg
	case theme.ColorNameButton:
		return p.button
	case theme.ColorNameDisabledButton:
		return p.disabledBtn
	case theme.ColorNameDisabled:
		return p.disabled
	case theme.ColorNameForeground:
		return p.fg
	case theme.ColorNameHover:
		return p.hover
	case theme.ColorNamePressed:
		return p.pressed
	case theme.ColorNameInputBackground:
		return p.input
	case theme.ColorNameInputBorder:
		return p.border
	case theme.ColorNamePrimary, theme.ColorNameFocus, theme.ColorNameHyperlink:
		return p.primary
	case theme.ColorNameForegroundOnPrimary, theme.ColorNameForegroundOnError, theme.ColorNameForegroundOnSuccess:
		return color.White
	case theme.ColorNameForegroundOnWarning:
		return linuxHex(0x1b1200)
	case theme.ColorNameSelection:
		return p.selection
	case theme.ColorNameSeparator:
		return p.separator
	case theme.ColorNameMenuBackground:
		return p.menu
	case theme.ColorNameOverlayBackground:
		return p.overlay
	case theme.ColorNameHeaderBackground:
		return p.header
	case theme.ColorNameScrollBar:
		return p.scroll
	case theme.ColorNameShadow:
		return p.shadow
	case theme.ColorNamePlaceHolder:
		return p.placeholder
	case theme.ColorNameSuccess:
		return linuxHex(0x22c55e)
	case theme.ColorNameWarning:
		return linuxHex(0xf59e0b)
	case theme.ColorNameError:
		return linuxHex(0xef4444)
	case linuxColorCard:
		return p.card
	case linuxColorCardBorder:
		return p.border
	case linuxColorSidebar:
		return p.sidebar
	case linuxColorMuted:
		return p.muted
	case linuxColorNavActive:
		return p.navActive
	case linuxColorLogBg:
		return p.logBg
	default:
		return theme.DefaultTheme().Color(name, variant)
	}
}

func (t *linuxGoamppTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(name)
}

func (t *linuxGoamppTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNameText:
		return 12.5
	case theme.SizeNameCaptionText:
		return 11
	case theme.SizeNameHeadingText:
		return 17
	case theme.SizeNameSubHeadingText:
		return 14
	case theme.SizeNamePadding:
		return 4
	case theme.SizeNameInnerPadding:
		return 6
	case theme.SizeNameLineSpacing:
		return 3
	case theme.SizeNameInlineIcon:
		return 16
	case theme.SizeNameInputRadius, theme.SizeNameButtonRadius:
		return 6
	case theme.SizeNameSelectionRadius:
		return 5
	case theme.SizeNameCardRadius, theme.SizeNameDialogRadius:
		return 8
	case theme.SizeNamePopupRadius, theme.SizeNameMenuRadius:
		return 8
	case theme.SizeNameScrollBar:
		return 8
	case theme.SizeNameScrollBarSmall, theme.SizeNameScrollBarRadius:
		return 3
	case theme.SizeNameSeparatorThickness, theme.SizeNameInputBorder:
		return 1
	default:
		return theme.DefaultTheme().Size(name)
	}
}

func (t *linuxGoamppTheme) Font(style fyne.TextStyle) fyne.Resource {
	return theme.DefaultTheme().Font(style)
}
