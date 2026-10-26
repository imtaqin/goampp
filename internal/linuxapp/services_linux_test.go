//go:build linux

package linuxapp

import (
	"reflect"
	"testing"
	"time"
)

func TestChooseServiceManagerPrefersActiveSystemd(t *testing.T) {
	tests := []struct {
		name       string
		systemd    bool
		openRC     bool
		sysv       bool
		want       string
	}{
		{"systemd wins when multiple command wrappers exist", true, true, true, "systemd"},
		{"OpenRC fallback", false, true, true, "openrc"},
		{"SysV fallback", false, false, true, "sysvinit"},
		{"no service manager", false, false, false, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := chooseServiceManager(test.systemd, test.openRC, test.sysv); got != test.want {
				t.Fatalf("chooseServiceManager() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestServiceCommand(t *testing.T) {
	tests := []struct {
		manager string
		unit    string
		action  string
		command string
		args    []string
	}{
		{"systemd", "nginx", "start", "systemctl", []string{"start", "nginx"}},
		{"systemd", "nginx", "restart", "systemctl", []string{"restart", "nginx"}},
		{"openrc", "nginx", "start", "rc-service", []string{"nginx", "start"}},
		{"openrc", "nginx", "restart", "rc-service", []string{"nginx", "restart"}},
		{"sysvinit", "nginx", "stop", "service", []string{"nginx", "stop"}},
		{"sysvinit", "nginx", "restart", "service", []string{"nginx", "restart"}},
		{"systemd", "php8.4-fpm", "status", "systemctl", []string{"status", "php8.4-fpm"}},
	}
	for _, test := range tests {
		t.Run(test.manager+"-"+test.action, func(t *testing.T) {
			gotCommand, gotArgs, err := serviceCommand(test.manager, test.unit, test.action)
			if err != nil {
				t.Fatal(err)
			}
			if gotCommand != test.command || !reflect.DeepEqual(gotArgs, test.args) {
				t.Fatalf("serviceCommand() = %q %v, want %q %v", gotCommand, gotArgs, test.command, test.args)
			}
		})
	}
}

func TestServiceCommandRejectsInvalidInput(t *testing.T) {
	for _, test := range []struct {
		manager string
		unit    string
		action  string
	}{
		{"systemd", "", "start"},
		{"systemd", "nginx", "enable"},
		{"unknown", "nginx", "start"},
	} {
		if _, _, err := serviceCommand(test.manager, test.unit, test.action); err == nil {
			t.Errorf("serviceCommand(%q, %q, %q) unexpectedly succeeded", test.manager, test.unit, test.action)
		}
	}
}

func TestMariaDBMySQLIncludesMysqldServiceAlias(t *testing.T) {
	for _, definition := range serviceDefinitions {
		if definition.Name != "MariaDB / MySQL" {
			continue
		}
		for _, unit := range definition.Units {
			if unit == "mysqld" {
				return
			}
		}
		t.Fatal("MariaDB / MySQL should detect the mysqld unit used by some distributions")
	}
	t.Fatal("MariaDB / MySQL service definition is missing")
}

func TestParseSystemdUnitFiles(t *testing.T) {
	output := "nginx.service enabled\nphp8.4-fpm.service enabled\nmulti-user.target static\n redis.service disabled \n"
	got := parseSystemdUnitFiles(output)
	for _, unit := range []string{"nginx.service", "php8.4-fpm.service", "redis.service"} {
		if _, ok := got[unit]; !ok {
			t.Errorf("service unit %q missing from parsed output", unit)
		}
	}
	if _, ok := got["multi-user.target"]; ok {
		t.Error("target unit was incorrectly parsed as a service")
	}
}


func TestRunCommandWithTimeout(t *testing.T) {
	output, err := runCommandWithTimeout(time.Second, "sh", "-c", "printf goampp-timeout-test")
	if err != nil {
		t.Fatalf("command failed unexpectedly: %v", err)
	}
	if string(output) != "goampp-timeout-test" {
		t.Fatalf("unexpected command output: %q", output)
	}
}

func TestRunCommandWithTimeoutStopsSlowCommands(t *testing.T) {
	start := time.Now()
	_, err := runCommandWithTimeout(50*time.Millisecond, "sleep", "3")
	if err == nil {
		t.Fatal("expected slow command to time out")
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("timed-out command took too long to stop: %s", elapsed)
	}
}


func TestFindPHPFPMUnitRespectsServiceManager(t *testing.T) {
	systemd := map[string]struct{}{
		"php8.2-fpm.service": {},
		"php8.4-fpm.service": {},
		"nginx.service":      {},
	}
	initScripts := map[string]struct{}{
		"php-fpm":     {},
		"php82-fpm":   {},
		"nginx":       {},
	}
	tests := []struct {
		name        string
		manager     string
		want        string
	}{
		{"systemd uses newest PHP unit", "systemd", "php8.4-fpm"},
		{"OpenRC uses the matching init script", "openrc", "php82-fpm"},
		{"SysV uses the matching init script", "sysvinit", "php82-fpm"},
		{"unsupported manager has no PHP unit", "", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := findPHPFPMUnit(test.manager, systemd, initScripts); got != test.want {
				t.Fatalf("findPHPFPMUnit(%q) = %q, want %q", test.manager, got, test.want)
			}
		})
	}
}
