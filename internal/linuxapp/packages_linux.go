//go:build linux

package linuxapp

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

type linuxPackageNames struct {
	APT    string
	Pacman string
	DNF    string
	Zypper string
}

var linuxServicePackages = map[string]linuxPackageNames{
	"Apache HTTP Server": {APT: "apache2", Pacman: "apache", DNF: "httpd", Zypper: "apache2"},
	"Nginx":              {APT: "nginx", Pacman: "nginx", DNF: "nginx", Zypper: "nginx"},
	"Caddy":              {APT: "caddy", Pacman: "caddy", DNF: "caddy", Zypper: "caddy"},
	"PHP-FPM":            {APT: "php-fpm", Pacman: "php", DNF: "php-fpm", Zypper: "php8-fpm"},
	"MariaDB / MySQL":    {APT: "mariadb-server", Pacman: "mariadb", DNF: "mariadb-server", Zypper: "mariadb"},
	"PostgreSQL":         {APT: "postgresql", Pacman: "postgresql", DNF: "postgresql-server", Zypper: "postgresql-server"},
	"Redis":              {APT: "redis-server", Pacman: "redis", DNF: "redis", Zypper: "redis"},
	"Memcached":          {APT: "memcached", Pacman: "memcached", DNF: "memcached", Zypper: "memcached"},
	"RabbitMQ":           {APT: "rabbitmq-server", Pacman: "rabbitmq", DNF: "rabbitmq-server", Zypper: "rabbitmq-server"},
	"Docker Engine":      {APT: "docker.io", Pacman: "docker", DNF: "docker", Zypper: "docker"},
}

var linuxToolPackages = map[string]linuxPackageNames{
	"PHP":      {APT: "php-cli", Pacman: "php", DNF: "php-cli", Zypper: "php8"},
	"Composer": {APT: "composer", Pacman: "composer", DNF: "composer", Zypper: "composer"},
	"Node.js":  {APT: "nodejs", Pacman: "nodejs", DNF: "nodejs", Zypper: "nodejs"},
	"npm":      {APT: "npm", Pacman: "npm", DNF: "npm", Zypper: "npm"},
	"Python":   {APT: "python3", Pacman: "python", DNF: "python3", Zypper: "python3"},
	"Go":       {APT: "golang-go", Pacman: "go", DNF: "golang", Zypper: "go"},
	"Java":     {APT: "default-jdk", Pacman: "jdk-openjdk", DNF: "java-latest-openjdk-devel", Zypper: "java-17-openjdk-devel"},
	"Rust":     {APT: "rustc cargo", Pacman: "rust", DNF: "rust cargo", Zypper: "rust cargo"},
	"Cargo":    {APT: "cargo", Pacman: "rust", DNF: "cargo", Zypper: "cargo"},
	"Ruby":     {APT: "ruby", Pacman: "ruby", DNF: "ruby", Zypper: "ruby"},
	"Bundler":  {APT: "ruby-bundler", Pacman: "ruby-bundler", DNF: "rubygem-bundler", Zypper: "ruby-bundler"},
	"Elixir":   {APT: "elixir", Pacman: "elixir", DNF: "elixir", Zypper: "elixir"},
	"Erlang":   {APT: "erlang-base", Pacman: "erlang", DNF: "erlang", Zypper: "erlang"},
	"Maven":    {APT: "maven", Pacman: "maven", DNF: "maven", Zypper: "maven"},
	"Gradle":   {APT: "gradle", Pacman: "gradle", DNF: "gradle", Zypper: "gradle"},
	"CMake":    {APT: "cmake", Pacman: "cmake", DNF: "cmake", Zypper: "cmake"},
	"GCC":      {APT: "build-essential", Pacman: "base-devel", DNF: "gcc", Zypper: "gcc"},
	"Perl":     {APT: "perl", Pacman: "perl", DNF: "perl", Zypper: "perl"},
	"SQLite":   {APT: "sqlite3", Pacman: "sqlite", DNF: "sqlite", Zypper: "sqlite3"},
	"Git":      {APT: "git", Pacman: "git", DNF: "git", Zypper: "git"},
	"Docker CLI":          {APT: "docker.io", Pacman: "docker", DNF: "docker", Zypper: "docker"},
	"PostgreSQL client":   {APT: "postgresql-client", Pacman: "postgresql", DNF: "postgresql", Zypper: "postgresql"},
}

func linuxPackageCommand(manager string, packages linuxPackageNames) (string, error) {
	var names string
	var command string
	switch manager {
	case "apt":
		command, names = "sudo apt install", packages.APT
	case "pacman":
		command, names = "sudo pacman -S --needed", packages.Pacman
	case "dnf":
		command, names = "sudo dnf install", packages.DNF
	case "zypper":
		command, names = "sudo zypper install", packages.Zypper
	default:
		return "", fmt.Errorf("unsupported package manager %q", manager)
	}
	if strings.TrimSpace(names) == "" {
		return "", fmt.Errorf("no package mapping is defined for %s", manager)
	}
	return command + " " + names, nil
}

func detectLinuxPackageManager() string {
	for _, candidate := range []struct {
		name string
		bin  string
	}{
		{"pacman", "pacman"},
		{"dnf", "dnf"},
		{"zypper", "zypper"},
		{"apt", "apt-get"},
	} {
		if _, err := exec.LookPath(candidate.bin); err == nil {
			return candidate.name
		}
	}
	return ""
}

func suggestedLinuxInstallCommand(name string, isTool bool) (string, error) {
	manager := detectLinuxPackageManager()
	if manager == "" {
		return "", fmt.Errorf("no supported package manager was detected; use your distribution's software installer")
	}
	packages := linuxServicePackages
	if isTool {
		packages = linuxToolPackages
	}
	definition, ok := packages[name]
	if !ok {
		return "", fmt.Errorf("there is no package mapping for %s", name)
	}
	return linuxPackageCommand(manager, definition)
}

func showLinuxPackageCommand(w fyne.Window, name string, isTool bool) {
	command, err := suggestedLinuxInstallCommand(name, isTool)
	if err != nil {
		dialog.ShowInformation("Install "+name, err.Error(), w)
		return
	}
	commandLabel := widget.NewLabel(command)
	commandLabel.Wrapping = fyne.TextWrapBreak
	commandLabel.TextStyle = fyne.TextStyle{Monospace: true}
	commandLabel.Selectable = true
	copyButton := widget.NewButtonWithIcon("Copy command", theme.ContentCopyIcon(), func() {
		w.Clipboard().SetContent(command)
		linuxSetStatus("Install command copied for " + name)
		linuxAddLog("Install command copied for " + name)
	})
	runButton := widget.NewButtonWithIcon("Run in terminal", theme.ComputerIcon(), func() {
		dialog.ShowConfirm(
			"Run package installation?",
			"This opens a terminal and runs the package-manager command shown above. It may change system packages and ask for your administrator password. GoAMPP will not hide the command output.",
			func(ok bool) {
				if !ok {
					return
				}
				fields := strings.Fields(command)
				if len(fields) == 0 {
					dialog.ShowError(fmt.Errorf("empty package command"), w)
					return
				}
				quoted := make([]string, 0, len(fields))
				for _, field := range fields {
					quoted = append(quoted, shellQuoteLinuxArg(field))
				}
				shellScript := strings.Join(quoted, " ") + "; status=$?; printf '\\nPackage command exited with status %s. Press Enter to close this terminal.\\n' \"$status\"; read -r _; exit \"$status\""
				shell := "bash"
				if _, err := exec.LookPath(shell); err != nil {
					shell = "sh"
				}
				home, _ := os.UserHomeDir()
				if err := openLinuxTerminal(home, shell, "-lc", shellScript); err != nil {
					dialog.ShowError(err, w)
					return
				}
				linuxSetStatus("Package command started in terminal for " + name)
				linuxAddLog("Started explicit package installation in terminal for " + name)
			},
			w,
		)
	})
	note := widget.NewLabel("Review the distribution package names before running. Installation occurs only after you press Run in terminal and confirm; service configuration is not changed automatically.")
	note.Wrapping = fyne.TextWrapWord
	content := container.NewVBox(note, linuxVSpace(8), commandLabel, linuxVSpace(8), container.NewHBox(copyButton, runButton))
	d := dialog.NewCustom("Install "+name, "Close", content, w)
	d.Resize(fyne.NewSize(620, 220))
	d.Show()
}

func shellQuoteLinuxArg(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
