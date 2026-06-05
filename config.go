//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config is the root of config.json. It holds everything GoAMPP needs to know
// about services and virtual hosts. The file is written back whenever the user
// makes changes through the UI, so hand-edits survive round-trips.
type Config struct {
	Version  int            `json:"version"`
	Services []ServiceConf  `json:"services"`
	Vhosts   []Vhost        `json:"vhosts"`
	Projects []Project      `json:"projects"`
	Settings GlobalSettings `json:"settings"`
}

// Project is a scaffolded web application — Laravel, WordPress, a
// Node.js app, whatever. Each project owns a directory inside www/
// and gets an auto-generated vhost pointing at its docroot. Kept
// separately from Vhosts so we can remember *how* each site was
// created (framework + runtime), not just the web-server mapping.
type Project struct {
	Name      string `json:"name"`      // slug; becomes directory name + subdomain
	Framework string `json:"framework"` // matches a key in the Frameworks catalog
	Domain    string `json:"domain"`    // e.g. "myapp.test"
	DocRoot   string `json:"docroot"`   // absolute path, served by Apache
	Port      int    `json:"port,omitempty"` // proxied frameworks (Node/Python) listen here
}

// GlobalSettings holds cross-cutting toggles and paths.
type GlobalSettings struct {
	// AutoStart service names to launch at program boot.
	AutoStart []string `json:"auto_start"`
	// HostsFile lets advanced users point at a custom hosts file (tests etc).
	// Empty means %WINDIR%\System32\drivers\etc\hosts.
	HostsFile string `json:"hosts_file"`
	// ApacheVhostsInclude is a path that should exist on disk; the vhosts tab
	// writes <VirtualHost> blocks here. Empty disables Apache vhost generation.
	ApacheVhostsInclude string `json:"apache_vhosts_include"`
	// NginxSitesDir writes per-site server { } blocks into this directory.
	// Empty disables Nginx vhost generation.
	NginxSitesDir string `json:"nginx_sites_dir"`
	// ActiveWebServer is "Apache" or "Nginx" — only the chosen one is
	// startable from the Services page. Default Apache so existing
	// configs without the field act exactly like before.
	ActiveWebServer string `json:"active_web_server,omitempty"`
}

// ServiceConf is the JSON-friendly flat description of a service. The live
// runtime Service (service.go) is built from this.
type ServiceConf struct {
	// Name shown in the UI. Doubles as a stable key, so don't rename casually.
	Name string `json:"name"`
	// Kind is advisory ("web", "database", "cache", "php", "tool"). Used to
	// group rows and pick icons.
	Kind string `json:"kind"`
	// ExePath may contain {base} which is replaced with the goampp dir.
	ExePath string   `json:"exe"`
	Args    []string `json:"args,omitempty"`
	// Port is probed to detect "already running" before Start.
	Port int `json:"port,omitempty"`
	// WorkDir — {base} expansion applies.
	WorkDir string `json:"workdir,omitempty"`
	// ConfigFile — {base} expansion applies. "Config" button opens this.
	ConfigFile string `json:"config_file,omitempty"`
	// Enabled rows render in the services list. Disabled rows are hidden
	// (but kept in config.json so the user doesn't lose their settings).
	Enabled bool `json:"enabled"`
	// OpenURL — if set, Services tab shows an "Open" button that launches
	// this URL in the default browser. Useful for phpMyAdmin / Adminer.
	OpenURL string `json:"open_url,omitempty"`
	// ActiveVersion is the variant version the user picked from the
	// per-card right-click menu (e.g. "8.4" for PHP, "20" for Node).
	// Empty means "use the catalogue's flat default URL". Persisted
	// to config.json so version choices stick across restarts.
	ActiveVersion string `json:"active_version,omitempty"`
	// Env is a list of "KEY=VALUE" pairs merged into the process environment
	// when the service is launched. {base} expansion applies to each value.
	// Useful for services like RabbitMQ that need ERLANG_HOME set.
	Env []string `json:"env,omitempty"`
}

// Vhost describes one virtual host. A vhost may map to Apache, Nginx, or both;
// in practice we write to whatever vhost include paths are configured.
type Vhost struct {
	Domain     string `json:"domain"`      // e.g. myapp.test
	DocRoot    string `json:"docroot"`     // absolute or {base}-relative path
	Port       int    `json:"port"`        // 80 by default — the listen port
	ServerType string `json:"server_type"` // "apache" | "nginx" | "both"
	Enabled    bool   `json:"enabled"`
	// ProxyPort, when non-zero, makes Apache emit a ProxyPass to
	// http://127.0.0.1:<ProxyPort> instead of serving DocRoot
	// directly. Used by Node/Python/Go projects whose dev server
	// runs in-process and listens on its own port.
	ProxyPort int `json:"proxy_port,omitempty"`
}

// LoadConfig reads config.json from baseDir. If missing, it writes a default
// config and returns that. This keeps first-run zero-config.
func LoadConfig(baseDir string) (*Config, error) {
	path := filepath.Join(baseDir, "config.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		cfg := DefaultConfig(baseDir)
		if err := SaveConfig(baseDir, cfg); err != nil {
			return nil, fmt.Errorf("write default config: %w", err)
		}
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	// Migration: older configs (before the multi-web-server picker
	// landed) don't have ActiveWebServer set, and may also have
	// Nginx.Enabled=true left over from an earlier default. Force
	// Apache to be the on-by-default web server unless the user has
	// explicitly switched in this same session — and persist back so
	// the next launch reads a clean file.
	migrated := false
	if cfg.Settings.ActiveWebServer == "" {
		cfg.Settings.ActiveWebServer = "Apache"
		migrated = true
	}
	for i := range cfg.Services {
		switch cfg.Services[i].Name {
		case "Apache":
			if !cfg.Services[i].Enabled {
				cfg.Services[i].Enabled = true
				migrated = true
			}
		case "Nginx":
			// Only flip Nginx OFF when the active server is Apache
			// (the default). If a user picked Nginx as active we
			// leave their choice alone — they want it enabled.
			if cfg.Settings.ActiveWebServer == "Apache" && cfg.Services[i].Enabled {
				cfg.Services[i].Enabled = false
				migrated = true
			}
		}
	}
	// Migration: append services present in DefaultConfig but absent from
	// the loaded config. This picks up new services added in later releases
	// without requiring the user to delete config.json.
	existing := map[string]bool{}
	for _, s := range cfg.Services {
		existing[s.Name] = true
	}
	for _, ds := range DefaultConfig(baseDir).Services {
		if !existing[ds.Name] {
			cfg.Services = append(cfg.Services, ds)
			migrated = true
		}
	}

	if migrated {
		_ = SaveConfig(baseDir, &cfg)
	}
	return &cfg, nil
}

// SaveConfig writes config.json back with pretty indentation so a human can
// still diff and hand-edit it.
func SaveConfig(baseDir string, cfg *Config) error {
	path := filepath.Join(baseDir, "config.json")
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// DefaultConfig is the first-run bundle of services. Paths are placeholders —
// users drop the real binaries into bin/ and GoAMPP finds them.
func DefaultConfig(baseDir string) *Config {
	return &Config{
		Version: 1,
		Services: []ServiceConf{
			{
				Name: "Apache", Kind: "web",
				ExePath:    "{base}/bin/apache/bin/httpd.exe",
				WorkDir:    "{base}/bin/apache",
				ConfigFile: "{base}/bin/apache/conf/httpd.conf",
				Port:       80, Enabled: true,
			},
			{
				Name: "Nginx", Kind: "web",
				ExePath:    "{base}/bin/nginx/nginx.exe",
				WorkDir:    "{base}/bin/nginx",
				ConfigFile: "{base}/bin/nginx/conf/nginx.conf",
				Port:       8080, Enabled: false,
			},
			{
				Name: "PHP-FPM", Kind: "php",
				ExePath:    "{base}/bin/php/php-cgi.exe",
				Args:       []string{"-b", "127.0.0.1:9000"},
				WorkDir:    "{base}/bin/php",
				ConfigFile: "{base}/bin/php/php.ini",
				Port:       9000, Enabled: false,
			},
			{
				Name: "MySQL", Kind: "database",
				ExePath: "{base}/bin/mysql/bin/mysqld.exe",
				// MariaDB looks up InnoDB's ibdata1 relative to its
				// current working directory. If we launch from bin/
				// it can't find the data/ sibling and refuses to
				// start. Explicit --basedir/--datadir bypasses the
				// heuristic entirely.
				Args: []string{
					"--console",
					"--basedir={base}/bin/mysql",
					"--datadir={base}/bin/mysql/data",
				},
				WorkDir:    "{base}/bin/mysql",
				ConfigFile: "{base}/bin/mysql/my.ini",
				Port:       3306, Enabled: true,
			},
			{
				Name: "PostgreSQL", Kind: "database",
				ExePath:    "{base}/bin/pgsql/bin/postgres.exe",
				Args:       []string{"-D", "{base}/bin/pgsql/data"},
				WorkDir:    "{base}/bin/pgsql/bin",
				ConfigFile: "{base}/bin/pgsql/data/postgresql.conf",
				Port:       5432, Enabled: false,
			},
			{
				Name: "Redis", Kind: "cache",
				ExePath:    "{base}/bin/redis/redis-server.exe",
				Args:       []string{"{base}/bin/redis/redis.windows.conf"},
				WorkDir:    "{base}/bin/redis",
				ConfigFile: "{base}/bin/redis/redis.windows.conf",
				Port:       6379, Enabled: false,
			},
			{
				Name: "phpMyAdmin", Kind: "tool",
				// phpMyAdmin is just a PHP app served by Apache — nothing to
				// launch. The "Open" button opens the URL below.
				OpenURL: "http://localhost/phpmyadmin/",
				Enabled: true,
			},
			{
				// Single-file PHP admin client. Cheap to install and
				// it covers MySQL/MariaDB AND PostgreSQL — enabled
				// by default so the welcome page's PostgreSQL Admin
				// tile actually opens something usable.
				Name: "Adminer", Kind: "tool",
				OpenURL: "http://localhost/adminer/",
				Enabled: true,
			},
			{
				// pgweb — modern web client for PostgreSQL. Daemon
				// service that listens on its own port (8081). The
				// --url flag pre-binds it to our local Postgres
				// cluster so there's no connection form to fill in
				// on first open.
				Name: "pgweb", Kind: "web",
				ExePath: "{base}/bin/pgweb/pgweb.exe",
				Args: []string{
					"--bind=127.0.0.1",
					"--listen=8081",
					"--url=postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable",
					"--skip-open",
				},
				WorkDir: "{base}/bin/pgweb",
				Port:    8081,
				Enabled: false, // user starts it manually from the card
				OpenURL: "http://localhost:8081/",
			},

			// ----- Message queues -----
			{
				// Erlang is a dependency, not a user-started service.
				// The card installs Erlang; RabbitMQ PostInstall also
				// auto-installs it, so users rarely need to click this.
				Name: "Erlang", Kind: "runtime",
				Enabled: false,
			},
			{
				Name: "RabbitMQ", Kind: "queue",
				// rabbitmq-server.bat is a batch file — needs cmd.exe /c.
				ExePath: "cmd.exe",
				Args: []string{
					"/c", "{base}/bin/rabbitmq/sbin/rabbitmq-server.bat",
				},
				WorkDir: "{base}/bin/rabbitmq",
				Env: []string{
					"ERLANG_HOME={base}/bin/erlang",
					"RABBITMQ_BASE={base}/data/rabbitmq",
					"RABBITMQ_NODENAME=rabbit@localhost",
				},
				Port:    5672, Enabled: false,
				OpenURL: "http://localhost:15672/",
			},

			// ----- Storage & messaging -----
			{
				Name: "MinIO", Kind: "storage",
				ExePath: "{base}/bin/minio/minio.exe",
				Args: []string{
					"server", "{base}/data/minio",
					"--address", ":9010",
					"--console-address", ":9011",
				},
				WorkDir: "{base}/bin/minio",
				Port:    9010, Enabled: false,
				OpenURL: "http://localhost:9011/",
			},
			{
				Name: "Mailpit", Kind: "mail",
				ExePath: "{base}/bin/mailpit/mailpit.exe",
				WorkDir: "{base}/bin/mailpit",
				Port:    8025, Enabled: false,
				OpenURL: "http://localhost:8025/",
			},

			// ----- Language runtimes (no long-running process) -----
			// Tool-only entries: ExePath is empty so the Start button
			// just downloads/installs them. Used by frameworks.go
			// scaffolders to know which interpreters are available.
			{
				Name: "Node.js", Kind: "runtime",
				Enabled: false,
			},
			{
				Name: "Python", Kind: "runtime",
				Enabled: false,
			},
			{
				Name: "Go", Kind: "runtime",
				Enabled: false,
			},
			{
				Name: "Java", Kind: "runtime",
				Enabled: false,
			},
			{
				Name: "Julia", Kind: "runtime",
				Enabled: false,
			},
			{
				Name: "Zig", Kind: "runtime",
				Enabled: false,
			},
			{
				Name: "Dart", Kind: "runtime",
				Enabled: false,
			},
			{
				Name: "Lua", Kind: "runtime",
				Enabled: false,
			},
			{
				Name: "Ruby", Kind: "runtime",
				Enabled: false,
			},
			{
				Name: "Rust", Kind: "runtime",
				Enabled: false,
			},
			{
				Name: "Kotlin", Kind: "runtime",
				Enabled: false,
			},
			{
				Name: "Haskell", Kind: "runtime",
				Enabled: false,
			},
			{
				Name: "Elixir", Kind: "runtime",
				Enabled: false,
			},
			{
				Name: "Crystal", Kind: "runtime",
				Enabled: false,
			},
			{
				Name: "Scala", Kind: "runtime",
				Enabled: false,
			},
			{
				Name: "Swift", Kind: "runtime",
				Enabled: false,
			},
		},
		Vhosts: []Vhost{
			{Domain: "myapp.test", DocRoot: "{base}/www/myapp", Port: 80, ServerType: "apache", Enabled: false},
		},
		Settings: GlobalSettings{
			AutoStart:           []string{},
			HostsFile:           "",
			ApacheVhostsInclude: "{base}/conf/apache/vhosts.conf",
			NginxSitesDir:       "{base}/conf/nginx/sites",
			ActiveWebServer:     "Apache",
		},
	}
}

// ExpandPath replaces {base} and environment variables so config values become
// real filesystem paths. Always called on the way out of the config layer.
func ExpandPath(s, baseDir string) string {
	if s == "" {
		return ""
	}
	s = os.ExpandEnv(s)
	// Replace every {base} token. We do it manually because strings.ReplaceAll
	// lives in strings and we're keeping this file import-light.
	out := make([]byte, 0, len(s))
	i := 0
	for i < len(s) {
		if i+6 <= len(s) && s[i:i+6] == "{base}" {
			out = append(out, baseDir...)
			i += 6
			continue
		}
		out = append(out, s[i])
		i++
	}
	return string(out)
}
