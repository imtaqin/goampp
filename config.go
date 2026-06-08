//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Config struct {
	Version  int            `json:"version"`
	Services []ServiceConf  `json:"services"`
	Vhosts   []Vhost        `json:"vhosts"`
	Projects []Project      `json:"projects"`
	Settings GlobalSettings `json:"settings"`
}

type Project struct {
	Name      string `json:"name"`
	Framework string `json:"framework"`
	Domain    string `json:"domain"`
	DocRoot   string `json:"docroot"`
	Port      int    `json:"port,omitempty"`
}

type GlobalSettings struct {
	AutoStart []string `json:"auto_start"`

	HostsFile string `json:"hosts_file"`

	ApacheVhostsInclude string `json:"apache_vhosts_include"`

	NginxSitesDir string `json:"nginx_sites_dir"`

	ActiveWebServer string `json:"active_web_server,omitempty"`
}

type ServiceConf struct {
	Name string `json:"name"`

	Kind string `json:"kind"`

	ExePath string   `json:"exe"`
	Args    []string `json:"args,omitempty"`

	Port int `json:"port,omitempty"`

	WorkDir string `json:"workdir,omitempty"`

	ConfigFile string `json:"config_file,omitempty"`

	Enabled bool `json:"enabled"`

	OpenURL string `json:"open_url,omitempty"`

	ActiveVersion string `json:"active_version,omitempty"`

	Env []string `json:"env,omitempty"`
}

type Vhost struct {
	Domain     string `json:"domain"`
	DocRoot    string `json:"docroot"`
	Port       int    `json:"port"`
	ServerType string `json:"server_type"`
	Enabled    bool   `json:"enabled"`

	ProxyPort int `json:"proxy_port,omitempty"`
}

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

			if cfg.Settings.ActiveWebServer == "Apache" && cfg.Services[i].Enabled {
				cfg.Services[i].Enabled = false
				migrated = true
			}
		}
	}

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

func SaveConfig(baseDir string, cfg *Config) error {
	path := filepath.Join(baseDir, "config.json")
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

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

				OpenURL: "http://localhost/phpmyadmin/",
				Enabled: true,
			},
			{
				Name: "Adminer", Kind: "tool",
				OpenURL: "http://localhost/adminer/",
				Enabled: true,
			},
			{
				Name: "Composer", Kind: "tool",
				Enabled: true,
			},
			{

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
				Enabled: false,
				OpenURL: "http://localhost:8081/",
			},

			{

				Name: "Erlang", Kind: "runtime",
				Enabled: false,
			},
			{
				Name: "RabbitMQ", Kind: "queue",

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
				Port: 5672, Enabled: false,
				OpenURL: "http://localhost:15672/",
			},

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

func ExpandPath(s, baseDir string) string {
	if s == "" {
		return ""
	}
	s = os.ExpandEnv(s)

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
