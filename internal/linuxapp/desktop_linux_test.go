//go:build linux

package linuxapp

import (
	"errors"
	"reflect"
	"testing"
)

func TestDesktopOpenCommandPrefersXDGOpen(t *testing.T) {
	lookup := func(name string) (string, error) {
		if name == "xdg-open" {
			return "/usr/bin/xdg-open", nil
		}
		return "", errors.New("not found")
	}
	command, args, err := desktopOpenCommand("http://127.0.0.1:8000", lookup)
	if err != nil {
		t.Fatal(err)
	}
	if command != "/usr/bin/xdg-open" || !reflect.DeepEqual(args, []string{"http://127.0.0.1:8000"}) {
		t.Fatalf("unexpected desktop open command: %q %v", command, args)
	}
}

func TestDesktopOpenCommandFallsBackToGIO(t *testing.T) {
	lookup := func(name string) (string, error) {
		if name == "gio" {
			return "/usr/bin/gio", nil
		}
		return "", errors.New("not found")
	}
	command, args, err := desktopOpenCommand("/tmp/project/index.html", lookup)
	if err != nil {
		t.Fatal(err)
	}
	if command != "/usr/bin/gio" || !reflect.DeepEqual(args, []string{"open", "/tmp/project/index.html"}) {
		t.Fatalf("unexpected gio command: %q %v", command, args)
	}
}

func TestDesktopOpenCommandReportsMissingDesktopOpener(t *testing.T) {
	_, _, err := desktopOpenCommand("/tmp/project", func(string) (string, error) {
		return "", errors.New("not found")
	})
	if err == nil {
		t.Fatal("expected a useful error when no desktop opener is available")
	}
}
