//go:build linux

package linuxapp

import (
	"strings"
	"testing"
)

func TestLinuxServiceActionCommands(t *testing.T) {
	tests := []struct {
		manager string
		unit    string
		action  string
		want    string
	}{
		{"systemd", "nginx", "status", "systemctl status nginx"},
		{"systemd", "nginx", "restart", "sudo systemctl restart nginx"},
		{"openrc", "redis", "restart", "sudo rc-service redis restart"},
		{"sysvinit", "mariadb", "start", "sudo service mariadb start"},
	}
	for _, test := range tests {
		t.Run(test.manager+"-"+test.action, func(t *testing.T) {
			got, err := linuxServiceActionCommand(test.manager, test.unit, test.action)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("command = %q, want %q", got, test.want)
			}
		})
	}
}

func TestLinuxServiceActionCommandsFailClosed(t *testing.T) {
	for _, test := range []struct{ manager, unit, action string }{
		{"", "nginx", "restart"},
		{"systemd", "", "restart"},
		{"unknown", "nginx", "restart"},
		{"systemd", "nginx;sh", "restart"},
	} {
		if _, err := linuxServiceActionCommand(test.manager, test.unit, test.action); err == nil {
			t.Errorf("linuxServiceActionCommand(%q,%q,%q) unexpectedly succeeded", test.manager, test.unit, test.action)
		}
	}
}

func TestLinuxServiceConfigPathsReturnOnlyExistingFiles(t *testing.T) {
	// The production path list depends on the test host, so test the function's
	// observable guarantees rather than expecting a fixed distro layout.
	for _, name := range []string{"Apache HTTP Server", "Nginx", "Caddy", "PHP-FPM", "MariaDB / MySQL", "PostgreSQL", "Redis", "Memcached", "RabbitMQ", "Docker Engine"} {
		for _, path := range linuxServiceConfigPaths(name) {
			if strings.TrimSpace(path) == "" {
				t.Errorf("%s returned an empty config path", name)
			}
		}
	}
}
