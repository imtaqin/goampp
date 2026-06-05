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
	"syscall"
	"time"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"
	"golang.org/x/sys/windows/registry"
)

type App struct {
	baseDir  string
	cfg      *Config
	services []*ManagedService

	wnd       *ui.Main
	statusBar *ui.StatusBar

	logBox *ui.Edit

	logMu  sync.Mutex
	logBuf strings.Builder
}

type ManagedService struct {
	Conf    *ServiceConf
	Service *Service
}

var app *App

const maxLogBytes = 200 * 1024

func main() {

	if len(os.Args) >= 3 && os.Args[1] == "--hide-run" {
		cmd := exec.Command(os.Args[2], os.Args[3:]...)
		cmd.SysProcAttr = &syscall.SysProcAttr{
			HideWindow:    true,
			CreationFlags: 0x08000000,
		}
		_ = cmd.Start()
		return
	}

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

	computeLayout()

	killZombieChildren(baseDir)

	initialCmdShow := co.SW_SHOW
	if hasTrayFlag() {
		initialCmdShow = co.SW_HIDE
	}
	wnd := ui.NewMain(
		ui.OptsMain().
			Title("GoAMPP — Local Web Stack Control Panel").
			Size(ui.Dpi(winW, winH)).
			Style(co.WS_CAPTION | co.WS_SYSMENU | co.WS_CLIPCHILDREN |
				co.WS_BORDER | co.WS_VISIBLE | co.WS_MINIMIZEBOX |
				co.WS_MAXIMIZEBOX | co.WS_SIZEBOX).
			ClassBrush(windowBgBrush()).
			CmdShow(initialCmdShow),
	)
	app.wnd = wnd

	wnd.On().WmDrawItem(handleDrawItem)

	wnd.On().WmCtlColorStatic(staticBgHandler)

	wnd.On().Wm(wmTrayCallback, func(p ui.Wm) uintptr {
		handleTrayCallback(wnd, p)
		return 0
	})

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

	wnd.On().WmSize(func(p ui.WmSize) {
		if p.Request() == co.SIZE_REQ_MINIMIZED {
			hideMainWindow(wnd)
		}
	})
	wnd.On().WmClose(func() {
		hideMainWindow(wnd)
	})

	buildMainLayout(wnd)

	app.statusBar = ui.NewStatusBar(wnd,
		ui.OptsStatusBar().
			FixedPart(ui.DpiX(220), "Ready").
			FlexPart(1, truncateMid(baseDir, 70)).
			FixedPart(ui.DpiX(90), "GoAMPP v0.6.1"),
	)

	wnd.On().WmCreate(func(p ui.WmCreate) int {
		h := wnd.Hwnd()

		_ = h.DwmSetWindowAttribute(win.DwmAttrWindowCornerPreference(co.DWMWCP_ROUND))

		if systemPrefersDark() {
			_ = h.DwmSetWindowAttribute(win.DwmAttrUseImmersiveDarkMode(true))
		}

		if err := installTray(wnd); err != nil {
			app.appendLog("tray: " + err.Error())
		}

		for _, ms := range app.services {
			if ms.Service == nil {
				continue
			}
			ms.Service.SetStateCallback(func(running bool, pid int) {
				wnd.UiThread(refreshServiceList)
			})
		}

		installServiceIcons()
		refreshServiceList()
		refreshVhostList()
		refreshProjectList()
		populateEditorDropdown()
		showPage("services")

		apachePath := filepath.Join(baseDir, "bin", "apache", "bin", "httpd.exe")
		if _, err := os.Stat(apachePath); err == nil {
			ensureApacheRuntimeFiles(baseDir, app.appendLog)
		}

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

func (a *App) findService(name string) *ManagedService {
	for _, ms := range a.services {
		if ms.Conf.Name == name {
			return ms
		}
	}
	return nil
}

func (a *App) appendLog(line string) {
	a.logMu.Lock()
	ts := time.Now().Format("15:04:05")
	a.logBuf.WriteString(ts)
	a.logBuf.WriteString(" ")
	a.logBuf.WriteString(line)
	a.logBuf.WriteString("\r\n")

	if a.logBuf.Len() > maxLogBytes {
		full := a.logBuf.String()
		cut := len(full) / 4

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

		const eot = uintptr(0x7FFFFFFF)
		h := a.logBox.Hwnd()
		h.SendMessage(co.EM_SETSEL, win.WPARAM(eot), win.LPARAM(eot))
		h.SendMessage(co.EM_SCROLLCARET, 0, 0)
	})
}

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

func openPath(path string) {

	cmd := exec.Command("cmd", "/c", "start", "", path)
	_ = cmd.Start()
}

func openFolder(path string) {
	if _, err := os.Stat(path); err != nil {
		_ = os.MkdirAll(path, 0o755)
	}
	_ = exec.Command("explorer.exe", filepath.FromSlash(path)).Start()
}

func truncateMid(s string, max int) string {
	if len(s) <= max {
		return s
	}
	keep := (max - 3) / 2
	return s[:keep] + "..." + s[len(s)-keep:]
}

func die(msg string) {

	win.HWND(0).MessageBox(msg, "GoAMPP — fatal error",
		co.MB_ICONERROR|co.MB_OK)
	os.Exit(1)
}
