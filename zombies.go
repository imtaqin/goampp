//go:build windows

package main

import (
	"bufio"
	"os/exec"
	"strings"
	"syscall"
)

// zombies.go — housekeeping for stale child processes from previous
// GoAMPP runs. Apache's winnt MPM, MariaDB, and a few others fork
// worker children that can outlive the parent on hard exits (crashes,
// power loss, a Kill() that lost its job handle). Those zombies keep
// holding their ports and make the next Start fail with a cryptic
// "port already in use".
//
// This file provides a single sweep that runs at app startup. It
// enumerates every process on the system with its full image path
// via PowerShell, filters to the ones that live inside goampp's
// bin/ directory, and taskkill's them.
//
// Earlier revisions used wmic, but Microsoft is removing wmic from
// Windows 11 22H2+ — on modern boxes the binary just isn't there
// and our sweep failed silently. PowerShell's Get-Process ships with
// every Windows 10+ install and exposes the same data.

// killZombieChildren scans all running processes and force-kills any
// whose image path is inside baseDir. Scoped strictly — a user
// running their own Apache on the same machine (from a different
// directory) is not touched.
//
// Non-fatal: any error is swallowed. The Start handlers already
// detect port conflicts and log them, so the worst case is the same
// "port busy" error the user already knows how to interpret.
func killZombieChildren(baseDir string) {
	// PowerShell Get-Process gives us Id + Path for every process.
	// We pipe through Format-Table so the output is stable CSV-ish
	// lines we can parse. Hiding the column headers with
	// -HideTableHeaders keeps the loop below simple.
	//
	// -NoProfile skips the user's PowerShell profile, which on some
	// machines prints banners that would corrupt our parser.
	script := `Get-Process | Where-Object { $_.Path } | ForEach-Object { "$($_.Id)|$($_.Path)" }`
	cmd := exec.Command("powershell.exe",
		"-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
	out, err := cmd.Output()
	if err != nil {
		return
	}

	// Normalise the haystack so path comparison works regardless of
	// separator style. We want every process whose exe lives under
	// <baseDir>/bin/ specifically — matching just <baseDir> would
	// include goampp.exe itself and our own running instance.
	needle := strings.ToLower(strings.ReplaceAll(baseDir, `\`, `/`)) + "/bin/"

	var killed []string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		// Format: <pid>|<path>
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
		// Found a stale child inside our bin/. Nuke it (and any
		// grandchildren — mariadbd sometimes spawns helpers).
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
