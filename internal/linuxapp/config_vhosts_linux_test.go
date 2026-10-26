//go:build linux

package linuxapp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidLinuxHostDomain(t *testing.T) {
	for _, domain := range []string{"myapp.test", "shop.localhost", "api.example.org", "a-b.dev"} {
		if !validLinuxHostDomain(domain) {
			t.Errorf("expected valid domain %q", domain)
		}
	}
	for _, domain := range []string{"", "localhost", "-bad.test", "bad-.test", "bad..test", "bad/name.test", "bad host.test", ".test"} {
		if validLinuxHostDomain(domain) {
			t.Errorf("expected invalid domain %q", domain)
		}
	}
}

func TestValidLinuxDocumentRootPath(t *testing.T) {
	for _, path := range []string{"/home/user/GoAMPP/www/demo/public", "/srv/www/café", "/var/www/app-v2"} {
		if !validLinuxDocumentRootPath(path) {
			t.Errorf("expected document root %q to be valid", path)
		}
	}
	for _, path := range []string{"", "  ", "/srv/www/demo\nserver {", "/srv/www/$HOME", "/srv/www/with\x00nul"} {
		if validLinuxDocumentRootPath(path) {
			t.Errorf("expected document root %q to be rejected", path)
		}
	}
}

func TestLinuxHostConfigGeneratesApacheAndNginx(t *testing.T) {
	host := linuxVHost{Domain: "demo.test", DocumentRoot: "/home/test/GoAMPP/www/demo/public", Port: 8088, Server: "both", Enabled: true}
	config := linuxHostConfig(host)
	for _, expected := range []string{
		"VirtualHost *:8088",
		"ServerName demo.test",
		"DocumentRoot \"/home/test/GoAMPP/www/demo/public\"",
		"server {",
		"listen 8088;",
		"server_name demo.test;",
		"root '/home/test/GoAMPP/www/demo/public';",
	} {
		if !strings.Contains(config, expected) {
			t.Errorf("server config is missing %q:\n%s", expected, config)
		}
	}
}

func TestSaveAndLoadLinuxVHosts(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hosts := []linuxVHost{{Domain: "demo.test", DocumentRoot: "/tmp/demo/public", Port: 80, Server: "apache", Enabled: true}}
	if err := saveLinuxVHosts(hosts); err != nil {
		t.Fatal(err)
	}
	got, err := loadLinuxVHosts()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != hosts[0] {
		t.Fatalf("loaded hosts = %#v, want %#v", got, hosts)
	}
}

func TestAtomicWriteLinuxFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.txt")
	if err := atomicWriteLinuxFile(path, []byte("first"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteLinuxFile(path, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second" {
		t.Fatalf("file = %q, want second", got)
	}
}

func TestSaveLinuxConfigFileBacksUpBeforeReplacing(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "nginx.conf")
	if err := os.WriteFile(path, []byte("old configuration"), 0o640); err != nil {
		t.Fatal(err)
	}
	backup, err := saveLinuxConfigFile(path, []byte("new configuration"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new configuration" {
		t.Fatalf("current config = %q", got)
	}
	old, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if string(old) != "old configuration" {
		t.Fatalf("backup = %q, want original contents", old)
	}
}

func TestSaveLinuxConfigFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.conf")
	link := filepath.Join(dir, "linked.conf")
	if err := os.WriteFile(target, []byte("do not change"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	if _, err := saveLinuxConfigFile(link, []byte("changed")); err == nil {
		t.Fatal("saving through a symlink must be rejected")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "do not change" {
		t.Fatalf("symlink target changed: %q", got)
	}
}
