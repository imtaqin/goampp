//go:build linux

package linuxapp

import (
	"fmt"
	"os/exec"
	"path/filepath"
)

type linuxTerminalCandidate struct {
	Name   string
	Prefix []string
}

var linuxTerminalCandidates = []linuxTerminalCandidate{
	{Name: "x-terminal-emulator", Prefix: []string{"-e"}},
	{Name: "konsole", Prefix: []string{"-e"}},
	{Name: "gnome-terminal", Prefix: []string{"--"}},
	{Name: "mate-terminal", Prefix: []string{"--"}},
	{Name: "alacritty", Prefix: []string{"-e"}},
	{Name: "kitty", Prefix: []string{"-e"}},
	{Name: "xterm", Prefix: []string{"-e"}},
}

func linuxTerminalCommand(_ string, command string, args []string, lookup func(string) (string, error)) (string, []string, error) {
	if command == "" {
		return "", nil, fmt.Errorf("terminal command is required")
	}
	for _, candidate := range linuxTerminalCandidates {
		binary, err := lookup(candidate.Name)
		if err != nil {
			continue
		}
		argv := append([]string(nil), candidate.Prefix...)
		argv = append(argv, command)
		argv = append(argv, args...)
		return binary, argv, nil
	}
	return "", nil, fmt.Errorf("no supported terminal emulator found; install xterm, Konsole, GNOME Terminal, or your desktop's x-terminal-emulator")
}

func openLinuxTerminal(directory string, command string, args ...string) error {
	binary, argv, err := linuxTerminalCommand(directory, command, args, exec.LookPath)
	if err != nil {
		return err
	}
	cmd := exec.Command(binary, argv...)
	if directory != "" {
		absolute, err := filepath.Abs(directory)
		if err != nil {
			return fmt.Errorf("resolve terminal working directory: %w", err)
		}
		cmd.Dir = absolute
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open terminal: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
