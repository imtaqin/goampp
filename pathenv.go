//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func goamppPathDirs() []string {
	if app == nil {
		return nil
	}
	join := func(parts ...string) string {
		return filepath.Join(append([]string{app.baseDir}, parts...)...)
	}
	candidates := []string{

		join("bin", "apache", "bin"),
		join("bin", "nginx"),
		join("bin", "php"),
		join("bin", "mysql", "bin"),
		join("bin", "pgsql", "bin"),
		join("bin", "redis"),

		join("bin", "pgweb"),
		join("bin", "minio"),
		join("bin", "mailpit"),

		join("bin", "node"),
		join("bin", "python"),
		join("bin", "python", "Scripts"),
		join("bin", "go", "bin"),
		join("bin", "java", "bin"),
		join("bin", "julia", "bin"),
		join("bin", "zig"),
		join("bin", "dart", "bin"),
		join("bin", "lua"),
		join("bin", "ruby", "bin"),
		join("bin", "rust", ".cargo", "bin"),
		join("bin", "kotlin", "bin"),
		join("bin", "haskell", "bin"),
		join("bin", "elixir", "bin"),
		join("bin", "crystal"),
		join("bin", "scala", "bin"),
		join("bin", "erlang", "bin"),
	}
	out := make([]string, 0, len(candidates))
	for _, d := range candidates {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			out = append(out, d)
		}
	}
	return out
}

func AddGoamppToUserPath() (int, error) {
	dirs := goamppPathDirs()
	if len(dirs) == 0 {
		return 0, fmt.Errorf("no goampp tools installed yet")
	}

	k, err := registry.OpenKey(registry.CURRENT_USER, "Environment",
		registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return 0, fmt.Errorf("open HKCU\\Environment: %w", err)
	}
	defer k.Close()

	current, valType, err := k.GetStringValue("Path")
	if err != nil && err != registry.ErrNotExist {
		return 0, fmt.Errorf("read Path: %w", err)
	}
	_ = valType

	existing := map[string]bool{}
	parts := strings.Split(current, ";")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			existing[strings.ToLower(strings.ReplaceAll(p, `/`, `\`))] = true
		}
	}

	added := 0
	for _, d := range dirs {
		key := strings.ToLower(strings.ReplaceAll(d, `/`, `\`))
		if existing[key] {
			continue
		}
		parts = append(parts, d)
		existing[key] = true
		added++
	}
	if added == 0 {
		return 0, nil
	}

	cleaned := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			cleaned = append(cleaned, p)
		}
	}
	newPath := strings.Join(cleaned, ";")

	if strings.Contains(current, "%") {
		err = k.SetExpandStringValue("Path", newPath)
	} else {
		err = k.SetStringValue("Path", newPath)
	}
	if err != nil {
		return 0, fmt.Errorf("write Path: %w", err)
	}

	broadcastEnvChange()
	return added, nil
}

func RemoveGoamppFromUserPath() (int, error) {
	if app == nil {
		return 0, fmt.Errorf("app not initialised")
	}
	prefix := strings.ToLower(
		strings.ReplaceAll(app.baseDir, `/`, `\`)) + `\bin`

	k, err := registry.OpenKey(registry.CURRENT_USER, "Environment",
		registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return 0, err
	}
	defer k.Close()

	current, _, err := k.GetStringValue("Path")
	if err != nil {
		return 0, err
	}

	parts := strings.Split(current, ";")
	kept := make([]string, 0, len(parts))
	removed := 0
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			continue
		}
		key := strings.ToLower(strings.ReplaceAll(p, `/`, `\`))
		if strings.HasPrefix(key, prefix) {
			removed++
			continue
		}
		kept = append(kept, p)
	}
	if removed == 0 {
		return 0, nil
	}

	newPath := strings.Join(kept, ";")
	if strings.Contains(current, "%") {
		err = k.SetExpandStringValue("Path", newPath)
	} else {
		err = k.SetStringValue("Path", newPath)
	}
	if err != nil {
		return 0, err
	}
	broadcastEnvChange()
	return removed, nil
}

func IsElevated() bool {
	var token windows.Token
	err := windows.OpenProcessToken(windows.CurrentProcess(),
		windows.TOKEN_QUERY, &token)
	if err != nil {
		return false
	}
	defer token.Close()
	return token.IsElevated()
}

func RelaunchElevated() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	verb, _ := syscall.UTF16PtrFromString("runas")
	file, _ := syscall.UTF16PtrFromString(exe)
	if err := windows.ShellExecute(0, verb, file, nil, nil, windows.SW_NORMAL); err != nil {
		return fmt.Errorf("UAC: %w", err)
	}
	return nil
}

func broadcastEnvChange() {
	const (
		HWND_BROADCAST   = uintptr(0xffff)
		WM_SETTINGCHANGE = uintptr(0x001A)
		SMTO_ABORTIFHUNG = uintptr(0x0002)
	)
	user32 := windows.NewLazySystemDLL("user32.dll")
	proc := user32.NewProc("SendMessageTimeoutW")
	envStr, _ := syscall.UTF16PtrFromString("Environment")
	var result uintptr
	proc.Call(
		HWND_BROADCAST,
		WM_SETTINGCHANGE,
		0,
		uintptr(unsafe.Pointer(envStr)),
		SMTO_ABORTIFHUNG,
		1000,
		uintptr(unsafe.Pointer(&result)),
	)
}
