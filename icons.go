//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/win"
)

// icons.go — per-service logo loading and the Service-Kind →
// category mapping.
//
// Windigo's Ico type only supports icons embedded as resources; we
// load from arbitrary .ico files under assets/icons/ via LoadImage
// and stash the HICON in a per-service map. The Services page card
// grid then sends each card's SS_ICON Static an STM_SETICON message
// pointing at the matching HICON.

// serviceIconFiles maps a ServiceConf.Name → the .ico file in
// assets/icons/ that represents it. Services not listed here fall back
// to no icon at all (the row just renders text).
var serviceIconFiles = map[string]string{
	// Web stack
	"Apache":     "apache.ico",
	"Nginx":      "nginx.ico",
	"PHP-FPM":    "php.ico",
	// Databases
	"MySQL":      "mysql.ico", // actually MariaDB — same shape, same protocol
	"PostgreSQL": "postgresql.ico",
	"Redis":      "redis.ico",
	// Admin tools
	"phpMyAdmin": "phpmyadmin.ico",
	"Adminer":    "adminer.ico",
	"pgweb":      "pgweb.ico",
	// Storage & messaging
	"MinIO":      "minio.ico",
	"Mailpit":    "mailpit.ico",
	// Language runtimes (downloadable, no long-running process)
	"Node.js":    "nodejs.ico",
	"Python":     "python.ico",
	"Go":         "go.ico",
	"Java":       "java.ico",
}

// iconState holds the per-service HICONs we hand to the card
// widgets via STM_SETICON. The previous design built two HIMAGELISTs
// for the ListView's LVSIL_SMALL/NORMAL slots — those are gone now
// that the Services page uses individual SS_ICON Statics.
var iconState struct {
	hIcons map[string]win.HICON // service name → 32×32 HICON
}

// installServiceIcons loads every .ico under assets/icons/ as a
// 32×32 HICON and stores it in iconState.hIcons keyed by service
// name. The card grid then sends STM_SETICON to its SS_ICON Statics
// to drop those handles in place.
//
// Safe to call only AFTER the main window's HWND exists (i.e. from
// the main WmCreate handler) — LoadImage doesn't strictly need it,
// but the card widgets we'll target via setCardIcon do.
func installServiceIcons() {
	iconState.hIcons = map[string]win.HICON{}

	assetsDir := filepath.Join(app.baseDir, "assets", "icons")
	for _, ms := range app.services {
		file, ok := serviceIconFiles[ms.Conf.Name]
		if !ok {
			continue
		}
		icoPath := filepath.Join(assetsDir, file)
		if _, err := os.Stat(icoPath); err != nil {
			continue // missing icon → blank tile, non-fatal
		}
		hGdi, err := win.HINSTANCE(0).LoadImage(
			win.ResIdStr(icoPath), co.IMAGE_ICON, 32, 32, co.LR_LOADFROMFILE)
		if err != nil {
			app.appendLog("icons: LoadImage " + file + ": " + err.Error())
			continue
		}
		iconState.hIcons[ms.Conf.Name] = win.HICON(hGdi)
	}
}

// stmSetIcon is the message ID for STM_SETICON. Not exposed by
// windigo's co package, so we define it here.
const stmSetIcon co.WM = 0x0170

// setCardIcon hands the loaded HICON to a card's SS_ICON Static.
// Called from refreshServiceList for every card on every refresh
// — sending the same HICON twice is a no-op so it's safe.
func setCardIcon(c *serviceCard, name string) {
	if c == nil || c.iconStatic == nil {
		return
	}
	hIcon, ok := iconState.hIcons[name]
	if !ok {
		return
	}
	c.iconStatic.Hwnd().SendMessage(
		stmSetIcon,
		win.WPARAM(hIcon),
		0,
	)
}

// ----- Category mapping --------------------------------------------------

// Group IDs — stable, fixed set so we can look them up by service Kind.
const (
	groupWeb      int32 = 1
	groupLanguage int32 = 2
	groupDatabase int32 = 3
	groupTool     int32 = 4
)

// groupForKind maps a ServiceConf.Kind to the numeric group ID.
// Unknown kinds default to the tool group so nothing falls off the
// edge of the list.
func groupForKind(kind string) int32 {
	switch strings.ToLower(kind) {
	case "web":
		return groupWeb
	case "php", "language", "runtime":
		return groupLanguage
	case "database", "cache":
		return groupDatabase
	default:
		return groupTool
	}
}

