//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/win"
)

var serviceIconFiles = map[string]string{

	"Apache":  "apache.ico",
	"Nginx":   "nginx.ico",
	"PHP-FPM": "php.ico",

	"MySQL":      "mysql.ico",
	"PostgreSQL": "postgresql.ico",
	"Redis":      "redis.ico",

	"phpMyAdmin": "phpmyadmin.ico",
	"Adminer":    "adminer.ico",
	"pgweb":      "pgweb.ico",

	"MinIO":    "minio.ico",
	"Mailpit":  "mailpit.ico",
	"RabbitMQ": "rabbitmq.ico",

	"Node.js": "nodejs.ico",
	"Python":  "python.ico",
	"Go":      "go.ico",
	"Java":    "java.ico",
	"Erlang":  "erlang.ico",
	"Julia":   "julia.ico",
	"Zig":     "zig.ico",
	"Dart":    "dart.ico",
	"Lua":     "lua.ico",
	"Ruby":    "ruby.ico",
	"Rust":    "rust.ico",
	"Kotlin":  "kotlin.ico",
	"Haskell": "haskell.ico",
	"Elixir":  "elixir.ico",
	"Crystal": "crystal.ico",
	"Scala":   "scala.ico",
	"Swift":   "swift.ico",
}

var iconState struct {
	hIcons map[string]win.HICON
}

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
			continue
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

const stmSetIcon co.WM = 0x0170

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

const (
	groupWeb      int32 = 1
	groupLanguage int32 = 2
	groupDatabase int32 = 3
	groupTool     int32 = 4
)

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
