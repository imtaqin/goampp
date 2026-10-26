//go:build linux

package linuxapp

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinuxTerminalCommandSelectsAvailableEmulatorWithoutShellInterpolation(t *testing.T) {
	lookups := map[string]string{
		"gnome-terminal": "/usr/bin/gnome-terminal",
	}
	lookup := func(name string) (string, error) {
		if path, ok := lookups[name]; ok {
			return path, nil
		}
		return "", errors.New("not found")
	}
	directory := filepath.Join(t.TempDir(), "workspace with spaces")
	binary, args, err := linuxTerminalCommand(directory, "psql", []string{"--set", "PROMPT1=goampp;$(not-run)"}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if binary != "/usr/bin/gnome-terminal" {
		t.Fatalf("terminal binary = %q", binary)
	}
	want := []string{"--", "psql", "--set", "PROMPT1=goampp;$(not-run)"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("terminal arguments = %#v, want %#v", args, want)
	}
}

func TestLinuxTerminalCommandFailsWhenNoEmulatorIsInstalled(t *testing.T) {
	lookup := func(string) (string, error) { return "", errors.New("not found") }
	if _, _, err := linuxTerminalCommand("/tmp", "psql", nil, lookup); err == nil {
		t.Fatal("expected an error when no terminal emulator is available")
	}
	if _, _, err := linuxTerminalCommand("/tmp", "", nil, lookup); err == nil {
		t.Fatal("expected an error when the terminal command is empty")
	}
}

func TestLinuxServiceUnitValidation(t *testing.T) {
	for _, unit := range []string{"nginx", "php8.4-fpm", "mariadb.service", "worker@1.service", "docker:root"} {
		if !validLinuxServiceUnit(unit) {
			t.Errorf("valid service unit %q was rejected", unit)
		}
	}
	for _, unit := range []string{"", "nginx;sh", "unit with spaces", "../nginx", "nginx/worker", "nginx\nstart"} {
		if validLinuxServiceUnit(unit) {
			t.Errorf("unsafe service unit %q was accepted", unit)
		}
	}
}
