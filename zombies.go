//go:build windows

package main

import (
	"bufio"
	"os/exec"
	"strings"
	"syscall"
)

func killZombieChildren(baseDir string) {

	script := `Get-Process | Where-Object { $_.Path } | ForEach-Object { "$($_.Id)|$($_.Path)" }`
	cmd := exec.Command("powershell.exe",
		"-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000,
	}
	out, err := cmd.Output()
	if err != nil {
		return
	}

	needle := strings.ToLower(strings.ReplaceAll(baseDir, `\`, `/`)) + "/bin/"

	var killed []string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}

		sep := strings.Index(line, "|")
		if sep < 0 {
			continue
		}
		pid := strings.TrimSpace(line[:sep])
		exePath := strings.TrimSpace(line[sep+1:])
		if pid == "" || exePath == "" {
			continue
		}
		normalised := strings.ToLower(strings.ReplaceAll(exePath, `\`, `/`))
		if !strings.HasPrefix(normalised, needle) {
			continue
		}

		kill := exec.Command("taskkill", "/F", "/T", "/PID", pid)
		kill.SysProcAttr = &syscall.SysProcAttr{
			HideWindow:    true,
			CreationFlags: 0x08000000,
		}
		if kill.Run() == nil {
			killed = append(killed, exePath+" (pid "+pid+")")
		}
	}

	if len(killed) > 0 && app != nil {
		for _, k := range killed {
			app.appendLog("startup sweep: killed stale " + k)
		}
	}
}
