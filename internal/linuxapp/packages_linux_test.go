//go:build linux

package linuxapp

import (
	"strings"
	"testing"
)

func TestLinuxPackageCommandUsesDistributionSyntax(t *testing.T) {
	packages := linuxPackageNames{
		APT:    "nginx",
		Pacman: "nginx",
		DNF:    "nginx",
		Zypper: "nginx",
	}
	tests := []struct {
		manager string
		want    string
	}{
		{"apt", "sudo apt install nginx"},
		{"pacman", "sudo pacman -S --needed nginx"},
		{"dnf", "sudo dnf install nginx"},
		{"zypper", "sudo zypper install nginx"},
	}
	for _, test := range tests {
		t.Run(test.manager, func(t *testing.T) {
			got, err := linuxPackageCommand(test.manager, packages)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("linuxPackageCommand() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestShellQuoteLinuxArgPreservesLiteralArguments(t *testing.T) {
	tests := []struct{ input, want string }{
		{"sudo", "'sudo'"},
		{"php-fpm", "'php-fpm'"},
		{"name with spaces", "'name with spaces'"},
		{"$(touch nope)", "'$(touch nope)'"},
		{"can't", "'can'\\''t'"},
	}
	for _, test := range tests {
		if got := shellQuoteLinuxArg(test.input); got != test.want {
			t.Errorf("shellQuoteLinuxArg(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestLinuxPackageCommandRejectsMissingMapping(t *testing.T) {
	if _, err := linuxPackageCommand("apt", linuxPackageNames{}); err == nil {
		t.Fatal("missing package mapping should return an error")
	}
	if _, err := linuxPackageCommand("unknown", linuxPackageNames{APT: "nginx"}); err == nil {
		t.Fatal("unknown package manager should return an error")
	}
}

func TestLinuxPackageMappingsCoverCoreServicesAndRuntimes(t *testing.T) {
	for _, name := range []string{"Apache HTTP Server", "Nginx", "Caddy", "PHP-FPM", "MariaDB / MySQL", "PostgreSQL", "Redis", "Memcached", "RabbitMQ", "Docker Engine"} {
		if _, ok := linuxServicePackages[name]; !ok {
			t.Errorf("service package mapping missing for %q", name)
		}
	}
	for _, name := range []string{"PHP", "Composer", "Node.js", "npm", "Python", "Go", "Java", "Rust", "Cargo", "Ruby", "Bundler", "Elixir", "Erlang", "Maven", "Gradle", "CMake", "GCC", "Perl", "SQLite", "Git", "Docker CLI", "PostgreSQL client"} {
		packages, ok := linuxToolPackages[name]
		if !ok {
			t.Errorf("tool package mapping missing for %q", name)
			continue
		}
		if strings.TrimSpace(packages.APT) == "" || strings.TrimSpace(packages.Pacman) == "" ||
			strings.TrimSpace(packages.DNF) == "" || strings.TrimSpace(packages.Zypper) == "" {
			t.Errorf("tool %q should include mappings for apt, pacman, dnf, and zypper", name)
		}
	}
}
