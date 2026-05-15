//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"
	"golang.org/x/sys/windows/registry"
)

// App is the live application state shared across tabs. One instance is
// stored in the package global `app` so button callbacks — which can't
// easily carry context — can reach services and UI controls.
type App struct {
	baseDir  string
	cfg      *Config
	services []*ManagedService

	// Top-level UI
	wnd       *ui.Main
	statusBar *ui.StatusBar

	// Log viewer at the bottom of the window.
	logBox *ui.Edit

	// Log ring-buffer so we can flush to the read-only Edit in whole. We
	// keep it capped — otherwise Edit.SetText() slows down over hours of
	// service logs.
	logMu  sync.Mutex
	logBuf strings.Builder
}

// ManagedService bundles a ServiceConf (config-layer) with its runtime
// Service (process-layer). `Service` is nil for tool-only entries like
// phpMyAdmin that have no exe to launch.
type ManagedService struct {
	Conf    *ServiceConf
	Service *Service
}

var app *App

// Max bytes retained in the log ring buffer. When exceeded we chop off the
// oldest ~25% so we don't hit a copy-the-world wall every write.
const maxLogBytes = 200 * 1024

func main() {
	runtime.LockOSThread()

	baseDir, err := os.Getwd()
	if err != nil {
		die("getwd: " + err.Error())
	}

	cfg, err := LoadConfig(baseDir)
	if err != nil {
		die("load config: " + err.Error())
	}

	app = &App{baseDir: baseDir, cfg: cfg}

	// Build the managed-services slice. We deliberately take a pointer to
	// cfg.Services[i] (not a range-loop copy) so that toggling Enabled in
	// the UI is visible to SaveConfig without an extra sync pass.
	for i := range cfg.Services {
		sc := &cfg.Services[i]
		ms := &ManagedService{Conf: sc}
		if sc.ExePath != "" {
			ms.Service = &Service{
				Name:    sc.Name,
				ExePath: ExpandPath(sc.ExePath, baseDir),
				Args:    expandArgsList(sc.Args, baseDir),
				Port:    sc.Port,
				WorkDir: ExpandPath(sc.WorkDir, baseDir),
				Env:     expandArgsList(sc.Env, baseDir),
			}
			ms.Service.SetLogger(app.appendLog)
		}
		app.services = append(app.services, ms)
	}

	// Housekeeping: nuke any stale child processes left over from a
	// prior crash. Stray httpd.exe / mariadbd.exe workers would keep
	// holding their ports and make the next Start fail with a cryptic
	// "address already in use". Scoped to our own bin/ so we never
	// touch the user's other Apache/MySQL installs.
	killZombieChildren(baseDir)

	// --- Main window ---
	// If we were auto-started with --tray we start hidden so the user
	// sees only the tray icon, not a surprise window on every login.
	initialCmdShow := co.SW_SHOW
	if hasTrayFlag() {
		initialCmdShow = co.SW_HIDE
	}
	wnd := ui.NewMain(
		ui.OptsMain().
			Title("GoAMPP — Local Web Stack Control Panel").
			Size(ui.Dpi(winW, winH)).
			Style(co.WS_CAPTION|co.WS_SYSMENU|co.WS_CLIPCHILDREN|
				co.WS_BORDER|co.WS_VISIBLE|co.WS_MINIMIZEBOX|
				co.WS_MAXIMIZEBOX|co.WS_SIZEBOX).
			ClassBrush(windowBgBrush()). // gray window bg, matches pages
			CmdShow(initialCmdShow),
	)
	app.wnd = wnd

	// Owner-draw handler for coloured buttons that live directly on the
	// main window (the sidebar nav). Page-container buttons register
	// their own handler in newPageContainer.
	wnd.On().WmDrawItem(handleDrawItem)

	// Paint Static text labels (title-bar separator, "Logs", etc.)
	// on top of the gray window background instead of letting them
	// render their own white rectangles.
	wnd.On().WmCtlColorStatic(staticBgHandler)

	// Tray icon callback: the shell pings us on this message whenever
	// the user interacts with the notification-area icon.
	wnd.On().Wm(wmTrayCallback, func(p ui.Wm) uintptr {
		handleTrayCallback(wnd, p)
		return 0
	})

	// Tray popup-menu commands. Each menu item is dispatched via a
	// dedicated WmCommand handler keyed on (cmdId, notifCode=CMD_MENU).
	//
	// Why we can't use a generic Wm(WM_COMMAND, ...) handler here:
	// windigo's processLast() special-cases WM_COMMAND so it ONLY
	// walks the WmCommand list (me.cmds), never the generic Wm
	// list (me.msgs). A handler registered via wnd.On().Wm(...) is
	// silently never invoked for WM_COMMAND messages — which is
	// why the tray Quit button used to look broken.
	wnd.On().WmCommand(trayMenuIdShow, co.CMD_MENU, func() {
		showMainWindow(wnd)
	})
	wnd.On().WmCommand(trayMenuIdStart, co.CMD_MENU, func() {
		go startEssentialStack()
	})
	wnd.On().WmCommand(trayMenuIdStop, co.CMD_MENU, func() {
		go stopAllServices()
	})
	wnd.On().WmCommand(trayMenuIdAutorun, co.CMD_MENU, func() {
		toggleAutoStart()
	})
	wnd.On().WmCommand(trayMenuIdQuit, co.CMD_MENU, func() {
		quitApp(wnd)
	})

	// Minimize-to-tray: when the window gets minimised, hide it and
	// rely on the tray icon to bring it back. The close button (X)
	// also routes here via WM_CLOSE so we don't exit the process —
	// users expect XAMPP-style "stay in tray on close".
	wnd.On().WmSize(func(p ui.WmSize) {
		if p.Request() == co.SIZE_REQ_MINIMIZED {
			hideMainWindow(wnd)
		}
	})
	wnd.On().WmClose(func() {
		hideMainWindow(wnd)
	})

	// --- Main layout (sidebar + pages + log panel) ---
	buildMainLayout(wnd)

	// --- Status bar ---
	app.statusBar = ui.NewStatusBar(wnd,
		ui.OptsStatusBar().
			FixedPart(ui.DpiX(220), "Ready").
			FlexPart(1, truncateMid(baseDir, 70)).
			FixedPart(ui.DpiX(90), "GoAMPP v0.3"),
	)

	// Windows 11 visual tweaks + callback wiring must happen after the
	// HWND exists, so we do it in WM_CREATE.
	wnd.On().WmCreate(func(p ui.WmCreate) int {
		h := wnd.Hwnd()
		// Rounded corners — harmless on Win10, lovely on Win11.
		_ = h.DwmSetWindowAttribute(win.DwmAttrWindowCornerPreference(co.DWMWCP_ROUND))
		// Follow the system theme for the title bar.
		if systemPrefersDark() {
			_ = h.DwmSetWindowAttribute(win.DwmAttrUseImmersiveDarkMode(true))
		}
		// Install the tray icon + set the window icon from logo.ico.
		// Non-fatal if it fails — we just log and keep running without
		// the tray so the app is still usable.
		if err := installTray(wnd); err != nil {
			app.appendLog("tray: " + err.Error())
		}
		// Wire service state callbacks now that controls exist.
		for _, ms := range app.services {
			if ms.Service == nil {
				continue
			}
			ms.Service.SetStateCallback(func(running bool, pid int) {
				wnd.UiThread(refreshServiceList)
			})
		}
		// Populate all the lists now that their HWNDs exist. These
		// can't run at build time — ListView.AddItem needs the
		// underlying native window, which isn't created until
		// WM_CREATE. Icons must be installed BEFORE refreshing so
		// the first draw shows them.
		installServiceIcons()
		refreshServiceList()
		refreshVhostList()
		refreshProjectList()
		populateEditorDropdown()
		showPage("services")

		// Self-heal Apache's runtime-state files (vhosts.conf +
		// welcome page + phpinfo shortcut). Only does anything when
		// Apache is already installed and one of the files is
		// missing — typical after an upgrade from a GoAMPP version
		// that didn't ship the self-heal. Idempotent; the happy
		// path is three stat() calls that return 'exists' and bail.
		apachePath := filepath.Join(baseDir, "bin", "apache", "bin", "httpd.exe")
		if _, err := os.Stat(apachePath); err == nil {
			ensureApacheRuntimeFiles(baseDir, app.appendLog)
		}
		// Kick off any auto-start services.
		for _, name := range cfg.Settings.AutoStart {
			if ms := app.findService(name); ms != nil && ms.Service != nil {
				if err := ms.Service.Start(); err != nil {
					app.appendLog(fmt.Sprintf("[auto-start] %s: %v", name, err))
				}
			}
		}
		return 0
	})

	app.appendLog(fmt.Sprintf("GoAMPP started at %s", time.Now().Format("15:04:05")))
	app.appendLog(fmt.Sprintf("Base dir: %s", baseDir))
	app.appendLog(fmt.Sprintf("Loaded %d services, %d vhosts from config.json",
		len(cfg.Services), len(cfg.Vhosts)))

	wnd.RunAsMain()
}

// findService looks up a managed service by its configured Name. O(n) is
// fine — the list is tiny.
func (a *App) findService(name string) *ManagedService {
	for _, ms := range a.services {
		if ms.Conf.Name == name {
			return ms
		}
	}
	return nil
}

// appendLog is the single sink for every log line in the app. It's safe to
// call from any goroutine (services fire it from their Wait() watcher).
func (a *App) appendLog(line string) {
	a.logMu.Lock()
	ts := time.Now().Format("15:04:05")
	a.logBuf.WriteString(ts)
	a.logBuf.WriteString(" ")
	a.logBuf.WriteString(line)
	a.logBuf.WriteString("\r\n")

	// Trim the front of the buffer when it grows unbounded.
	if a.logBuf.Len() > maxLogBytes {
		full := a.logBuf.String()
		cut := len(full) / 4
		// Snap cut to the next newline so we don't slice a line in half.
		if nl := strings.IndexByte(full[cut:], '\n'); nl >= 0 {
			cut += nl + 1
		}
		a.logBuf.Reset()
		a.logBuf.WriteString(full[cut:])
	}
	text := a.logBuf.String()
	a.logMu.Unlock()

	if a.wnd == nil || a.logBox == nil {
		return
	}
	a.wnd.UiThread(func() {
		a.logBox.SetText(text)
		// Auto-scroll the log Edit control to the bottom on every
		// append. There's a subtle gotcha here: the obvious idiom
		//
		//   SendMessage(EM_SETSEL, -1, -1)
		//
		// doesn't move the caret to the end of the buffer — passing
		// WPARAM=-1 tells the edit control to *deselect* the
		// current selection (per MSDN: "If the start is -1, any
		// current selection is deselected."). The fix is to use a
		// positive sentinel that exceeds any plausible buffer size;
		// 0x7FFFFFFF gets clamped to the actual text length, which
		// puts the caret at the end. EM_SCROLLCARET then brings the
		// caret into view, which is the actual auto-scroll.
		const eot = uintptr(0x7FFFFFFF)
		h := a.logBox.Hwnd()
		h.SendMessage(co.EM_SETSEL, win.WPARAM(eot), win.LPARAM(eot))
		h.SendMessage(co.EM_SCROLLCARET, 0, 0)
	})
}

// expandArgsList runs ExpandPath on every arg, so {base} and env vars in
// args (e.g. mysqld --datadir={base}/bin/mysql/data) are resolved.
func expandArgsList(args []string, baseDir string) []string {
	if len(args) == 0 {
		return nil
	}
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = ExpandPath(a, baseDir)
	}
	return out
}

// systemPrefersDark returns true if the user has dark mode enabled for apps
// in Windows Settings. Falls back to false on any error — the app just
// uses the default light title bar in that case.
func systemPrefersDark() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`,
		registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	if err != nil {
		return false
	}
	return v == 0
}

// Helpers used from several tabs. --------------------------------------------

// openPath hands a file or folder to the system shell (Explorer / default
// editor). Used by the "Open config", "Open folder", "Open URL" buttons.
func openPath(path string) {
	// `cmd /c start "" "<path>"` is the idiomatic "ShellExecute but safer"
	// trick: the empty "" is a dummy title argument so paths with spaces
	// aren't misinterpreted.
	cmd := exec.Command("cmd", "/c", "start", "", path)
	_ = cmd.Start()
}

// openFolder opens Explorer at a path, creating it first if it doesn't exist
// yet (nice DX — users can "open logs" on first run without errors).
func openFolder(path string) {
	if _, err := os.Stat(path); err != nil {
		_ = os.MkdirAll(path, 0o755)
	}
	_ = exec.Command("explorer.exe", filepath.FromSlash(path)).Start()
}

// truncateMid shortens long paths with an ellipsis in the middle — keeps the
// drive letter and the last segment visible at a glance.
func truncateMid(s string, max int) string {
	if len(s) <= max {
		return s
	}
	keep := (max - 3) / 2
	return s[:keep] + "..." + s[len(s)-keep:]
}

// die is the one-and-only fatal path. We show a message box so a
// GUI-only user sees *something* instead of the window silently failing.
func die(msg string) {
	// Use the Win32 MessageBox directly — we may not have a window yet.
	win.HWND(0).MessageBox(msg, "GoAMPP — fatal error",
		co.MB_ICONERROR|co.MB_OK)
	os.Exit(1)
}
