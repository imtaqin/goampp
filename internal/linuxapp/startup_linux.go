//go:build linux

package linuxapp

import (
	"fmt"
	"os"
	"os/exec"
	"time"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

type linuxStartupState struct {
	Service  serviceState
	Enabled  bool
	Known    bool
}

func linuxStartupEnabled(manager, unit string) (bool, bool) {
	switch manager {
	case "systemd":
		output, err := runCommandWithTimeout(4*time.Second, "systemctl", "is-enabled", unit)
		value := strings.TrimSpace(string(output))
		switch value {
		case "enabled", "enabled-runtime", "linked", "linked-runtime", "alias":
			return true, true
		case "disabled", "static", "indirect", "masked", "generated", "transient", "not-found":
			return false, true
		default:
			return err == nil, true
		}
	case "openrc":
		output, err := runCommandWithTimeout(4_000_000_000, "rc-update", "show")
		if err != nil && len(output) == 0 {
			return false, false
		}
		for _, line := range strings.Split(string(output), "\n") {
			fields := strings.Fields(strings.ReplaceAll(line, "|", " "))
			if len(fields) >= 2 && fields[0] == unit && fields[len(fields)-1] == "default" {
				return true, true
			}
		}
		return false, true
	case "sysvinit":
		for _, dir := range []string{"/etc/rc2.d", "/etc/rc3.d", "/etc/rc4.d", "/etc/rc5.d"} {
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), "S") && strings.Contains(entry.Name(), unit) {
					return true, true
				}
			}
		}
		if _, err := os.Stat("/etc/init.d/" + unit); err == nil {
			return false, true
		}
		return false, false
	default:
		return false, false
	}
}

func linuxStartupCommand(manager, unit string, enable bool) (string, error) {
	if strings.TrimSpace(unit) == "" {
		return "", fmt.Errorf("service unit is required")
	}
	switch manager {
	case "systemd":
		action := "disable"
		if enable {
			action = "enable"
		}
		return "systemctl " + action + " " + unit, nil
	case "openrc":
		if enable {
			return "rc-update add " + unit + " default", nil
		}
		return "rc-update del " + unit + " default", nil
	case "sysvinit":
		if enable {
			return "update-rc.d " + unit + " defaults", nil
		}
		return "update-rc.d " + unit + " remove", nil
	default:
		return "", fmt.Errorf("this service manager does not support startup configuration")
	}
}

func setLinuxStartup(manager, unit string, enable bool) error {
	command := ""
	var args []string
	switch manager {
	case "systemd":
		if enable {
			args = []string{"enable", unit}
		} else {
			args = []string{"disable", unit}
		}
		command = "systemctl"
	case "openrc":
		if enable {
			args = []string{"add", unit, "default"}
		} else {
			args = []string{"del", unit, "default"}
		}
		command = "rc-update"
	case "sysvinit":
		if enable {
			args = []string{unit, "defaults"}
		} else {
			args = []string{unit, "remove"}
		}
		command = "update-rc.d"
	default:
		return fmt.Errorf("unsupported startup manager %q", manager)
	}
	if _, err := execLookPath(command); err != nil {
		return err
	}
	output, err := runCommandWithTimeout(15*time.Second, command, args...)
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("%s %s: %s; the operation may require administrator permission", command, strings.Join(args, " "), message)
	}
	return nil
}

func execLookPath(command string) (string, error) {
	path, err := exec.LookPath(command)
	if err != nil {
		return "", fmt.Errorf("%s is not installed or not available on PATH", command)
	}
	return path, nil
}

func buildLinuxStartupPage(w fyne.Window) fyne.CanvasObject {
	rows := container.NewVBox()
	status := widget.NewLabel("Scan installed services to inspect and configure automatic startup.")
	status.Wrapping = fyne.TextWrapWord
	refreshButton := widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), nil)
	copyHelp := widget.NewButtonWithIcon("Copy startup commands", theme.ContentCopyIcon(), func() {
		w.Clipboard().SetContent("systemd: sudo systemctl enable <unit>\nOpenRC: sudo rc-update add <unit> default\nSysV init: sudo update-rc.d <unit> defaults")
		dialog.ShowInformation("Commands copied", "Commands are copied as examples. Replace <unit> with the service unit name; do not run every line.", w)
	})
	refresh := func() {
		refreshButton.Disable()
		status.SetText("Scanning service units and startup state…")
		go func() {
			services := discoverServices()
			states := make([]linuxStartupState, 0, len(services))
			for _, service := range services {
				if service.Unit == "" || service.Manager == "" {
					continue
				}
				enabled, known := linuxStartupEnabled(service.Manager, service.Unit)
				states = append(states, linuxStartupState{Service: service, Enabled: enabled, Known: known})
			}
			fyne.Do(func() {
				rows.Objects = nil
				for _, state := range states {
					state := state
					name := widget.NewLabel(state.Service.Definition.Name)
					name.TextStyle = fyne.TextStyle{Bold: true}
					meta := widget.NewLabel(fmt.Sprintf("%s · %s · currently %s", state.Service.Unit, state.Service.Manager, serviceRunStatus(state.Service.Running)))
					meta.Wrapping = fyne.TextWrapBreak
					checkLabel := "Start this service at boot"
					if !state.Known {
						checkLabel = "Startup state is not detectable on this init system"
					}
					check := widget.NewCheck(checkLabel, nil)
					check.SetChecked(state.Enabled)
					if !state.Known { check.Disable() }
					check.OnChanged = func(enable bool) {
						check.Disable()
						go func() {
							err := setLinuxStartup(state.Service.Manager, state.Service.Unit, enable)
							fyne.Do(func() {
								if err != nil {
									check.SetChecked(!enable)
									dialog.ShowError(err, w)
									linuxSetStatus("Startup change failed for " + state.Service.Unit)
									linuxAddLog("Startup change failed for " + state.Service.Unit + ": " + err.Error())
								} else {
									check.SetChecked(enable)
									verb := "disabled"
									if enable { verb = "enabled" }
									status.SetText("Automatic startup " + verb + " for " + state.Service.Unit + ".")
									linuxSetStatus("Startup " + verb + ": " + state.Service.Unit)
									linuxAddLog("Service startup " + verb + ": " + state.Service.Unit)
								}
								check.Enable()
							})
						}()
					}
					copy := widget.NewButtonWithIcon("Copy command", theme.ContentCopyIcon(), func() {
						command, err := linuxStartupCommand(state.Service.Manager, state.Service.Unit, !state.Enabled)
						if err != nil {
							dialog.ShowError(err, w)
							return
						}
						w.Clipboard().SetContent("sudo " + command)
						dialog.ShowInformation("Command copied", "Copied a command that changes the boot-start state for "+state.Service.Unit+". Review it before running it in a terminal.", w)
					})
					rows.Add(linuxSurface(container.NewBorder(nil, nil, container.NewVBox(name, meta, check), copy, nil), 8, 8, 10, 10))
					rows.Add(linuxVSpace(6))
				}
				if len(states) == 0 {
					rows.Add(container.NewCenter(widget.NewLabel("No supported service units were detected. Install a service through your distribution package manager, then refresh.")))
				}
				status.SetText(fmt.Sprintf("Found %d service units. GoAMPP does not silently elevate privileges; a startup change may be rejected by your service manager.", len(states)))
				rows.Refresh()
				refreshButton.Enable()
			})
		}()
	}
	refreshButton.OnTapped = refresh
	help := linuxSurface(container.NewVBox(
		linuxBoldText("Automatic service startup", 14),
		linuxMutedText("Enable or disable boot startup for distribution-managed services. This does not start or stop a service immediately.", 11.5),
		container.NewHBox(refreshButton, copyHelp),
	), 10, 10, 12, 12)
	refresh()
	return container.NewVScroll(linuxPad(container.NewVBox(help, linuxVSpace(8), status, linuxVSpace(6), rows), 0, 10, 0, 0))
}

func serviceRunStatus(running bool) string {
	if running {
		return "running"
	}
	return "stopped"
}
