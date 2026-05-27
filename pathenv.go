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

// pathenv.go — manage the user-level PATH so a system terminal
// (cmd, PowerShell, Windows Terminal) can run goampp's bundled
// tools by their bare name. After clicking "Add tools to PATH" in
// Settings, `node`, `php`, `composer`, `mysql`, `redis-cli`, etc.
// all just work in any new shell.
//
// We touch HKCU\Environment\Path (the per-user override) so admin
// rights aren't needed and we don't pollute the machine-wide PATH.
// On change we broadcast WM_SETTINGCHANGE so already-running
// Explorer / Terminal pick up the new value without a reboot.

// goamppPathDirs returns the list of bin directories the user
// would benefit from having on PATH. We list every relevant
// service/runtime — only directories that actually exist on disk
// are returned, so the function is safe to call before all the
// services are installed.
func goamppPathDirs() []string {
	if app == nil {
		return nil
	}
	join := func(parts ...string) string {
		return filepath.Join(append([]string{app.baseDir}, parts...)...)
	}
	candidates := []string{
		// Web stack
		join("bin", "apache", "bin"),        // httpd
		join("bin", "nginx"),                // nginx.exe
		join("bin", "php"),                  // php.exe + php-cgi.exe
		join("bin", "mysql", "bin"),         // mysqld, mysql, mysqldump
		join("bin", "pgsql", "bin"),         // postgres, psql, pg_dump
		join("bin", "redis"),                // redis-server, redis-cli
		// Tools
		join("bin", "pgweb"),                // pgweb.exe
		join("bin", "minio"),                // minio.exe
		join("bin", "mailpit"),              // mailpit.exe
		// Language runtimes
		join("bin", "node"),                 // node, npm, npx
		join("bin", "python"),               // python.exe
		join("bin", "python", "Scripts"),    // pip, pipx
		join("bin", "go", "bin"),            // go, gofmt
		join("bin", "java", "bin"),          // java, javac, jar
		join("bin", "julia", "bin"),         // julia.exe
		join("bin", "zig"),                  // zig.exe (flat archive)
		join("bin", "dart", "bin"),          // dart.exe
		join("bin", "idris2"),               // idris2.exe (flat archive)
		join("bin", "lua"),                  // lua54.exe
		join("bin", "ruby", "bin"),          // ruby.exe, gem, irb
		join("bin", "rust", ".cargo", "bin"),// cargo.exe, rustc.exe, rustup.exe
		join("bin", "kotlin", "bin"),        // kotlinc.bat
		join("bin", "haskell", "bin"),       // ghc.exe, runghc.exe, ghci.exe
		join("bin", "elixir", "bin"),        // elixir.bat, iex.bat, mix.bat
		join("bin", "crystal"),              // crystal.exe (flat archive)
		join("bin", "scala", "bin"),         // scala.bat, scalac.bat
		join("bin", "erlang", "bin"),        // erl.exe, escript.exe
	}
	out := make([]string, 0, len(candidates))
	for _, d := range candidates {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			out = append(out, d)
		}
	}
	return out
}

// AddGoamppToUserPath appends every existing goampp bin/ directory
// to HKCU\Environment\Path. Idempotent: existing entries (matched
// case-insensitively) are skipped, so re-running is safe.
//
// Returns the count of directories added (0 if everything was
// already present) plus any error from the registry write.
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

	// REG_EXPAND_SZ vs REG_SZ matters: if the existing value uses
	// %SystemRoot% style expansion we need to write back the same
	// type or we'll silently nuke env-var references. We use
	// GetStringsValue/SetStringValue from the high-level wrapper
	// which handles both.
	current, valType, err := k.GetStringValue("Path")
	if err != nil && err != registry.ErrNotExist {
		return 0, fmt.Errorf("read Path: %w", err)
	}
	_ = valType

	// Build a lowercase set of existing entries so the dedup is
	// case-insensitive (Windows paths are).
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

	// Trim any empty segments left over from the original split.
	cleaned := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			cleaned = append(cleaned, p)
		}
	}
	newPath := strings.Join(cleaned, ";")

	// Re-create as REG_EXPAND_SZ if the original had env-var
	// references; otherwise plain REG_SZ. SetExpandStringValue
	// covers the first case.
	if strings.Contains(current, "%") {
		err = k.SetExpandStringValue("Path", newPath)
	} else {
		err = k.SetStringValue("Path", newPath)
	}
	if err != nil {
		return 0, fmt.Errorf("write Path: %w", err)
	}

	// Tell every other process the environment changed. Without
	// this they keep the old PATH until they restart. The empty
	// "Environment" string is the magic value MSDN documents.
	broadcastEnvChange()
	return added, nil
}

// RemoveGoamppFromUserPath strips any goampp bin/ directories from
// the user PATH. Used by the "Remove from PATH" button — symmetric
// to the add operation.
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

// IsElevated reports whether the current process is running with
// admin rights. Used by the Settings UI to grey out "Restart as
// Administrator" when we already are.
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

// RelaunchElevated triggers a UAC prompt and starts a new goampp
// process as administrator. The current (non-elevated) instance
// then exits cleanly via quitApp so we don't end up with two
// running side by side fighting over the same config + ports.
//
// If the user dismisses the UAC prompt, ShellExecute returns an
// error and we just stay running unelevated.
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

// broadcastEnvChange sends WM_SETTINGCHANGE with lParam="Environment"
// to every top-level window so anything listening (Explorer,
// Terminal, IDEs) refreshes its env block. Without this, only newly
// spawned processes pick up the new PATH.
//
// SMTO_ABORTIFHUNG with a 1-second timeout means a hung process
// can't stall the UI thread.
func broadcastEnvChange() {
	const (
		HWND_BROADCAST  = uintptr(0xffff)
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
