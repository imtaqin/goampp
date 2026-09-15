//go:build windows

package main

import (
	cryptorand "crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

var apacheSrvRootRe = regexp.MustCompile(`(?i)Define SRVROOT "[^"]*"`)

var apacheServerNameRe = regexp.MustCompile(`(?m)^#?ServerName\s+[^\n]*`)

var (
	apacheDocRootRe       = regexp.MustCompile(`(?m)^DocumentRoot\s+"[^"]*"`)
	apacheDirectoryRe     = regexp.MustCompile(`(?m)^<Directory\s+"[^"]*/htdocs"\s*>`)
	apacheDirectoryIdxRe  = regexp.MustCompile(`(?m)^(\s*)DirectoryIndex\s+[^\n]*`)
	apacheLoadCgiRe       = regexp.MustCompile(`(?m)^#\s*LoadModule\s+cgi_module\s+modules/mod_cgi\.so`)
	apacheLoadActionsRe   = regexp.MustCompile(`(?m)^#\s*LoadModule\s+actions_module\s+modules/mod_actions\.so`)
	apacheLoadProxyRe     = regexp.MustCompile(`(?m)^#\s*LoadModule\s+proxy_module\s+modules/mod_proxy\.so`)
	apacheLoadProxyHttpRe = regexp.MustCompile(`(?m)^#\s*LoadModule\s+proxy_http_module\s+modules/mod_proxy_http\.so`)
)

const apachePhpHandlerTemplate = `

# >>> GoAMPP PHP handler BEGIN — do not edit between these markers <<<
# Classic CGI handler (see download.go for the rationale).
#
# Why both <Directory> AND <Location> grants:
# When Apache 2.4 services a .php request, the Action directive
# triggers an internal sub-request to /__goampp-php-bin__/php-cgi.exe.
# That sub-request is evaluated against URL-space first (<Location>),
# THEN filesystem-space (<Directory>). The shipped httpd.conf has a
# global <Directory /> with "Require all denied" — without an explicit
# <Location> grant, the URL-space walk inherits that deny and the
# request 403s with: "client denied by server configuration:
# C:/goampp/bin/php" before our <Directory "C:/goampp/bin/php"> grant
# is even consulted. Adding <Location "/__goampp-php-bin__/"> with
# Require all granted bypasses the filesystem deny entirely for this
# specific URL prefix.
ScriptAlias "/__goampp-php-bin__/" "%s/"
<Directory "%s">
    AllowOverride None
    Options +ExecCGI
    <Files "php-cgi.exe">
        Require all granted
    </Files>
    Require all granted
</Directory>
<Location "/__goampp-php-bin__/">
    Require all granted
</Location>
AddHandler application/x-httpd-php .php
Action application/x-httpd-php "/__goampp-php-bin__/php-cgi.exe"
# <<< GoAMPP PHP handler END >>>
`

// FileKind selects how a downloaded artifact is installed. These were bare
// strings compared in a switch, so a typo in the catalogue fell through to the
// "unknown kind" arm at install time rather than failing to compile.
type FileKind string

const (
	Zip  FileKind = "zip"  // archive — extracted, with StripTop stripped off
	Exe  FileKind = "exe"  // silent installer — run with /S /D=<dir>
	File FileKind = "file" // one file — copied to TargetFile
)

type DownloadSpec struct {
	Version    string
	URL        string
	FileName   string
	InstallDir string

	StripTop string

	Kind FileKind

	TargetFile string

	CheckFile string

	PostInstall func(installDir string, log func(string)) error

	Notes string

	Variants []VariantSpec

	URLResolver func(log func(string)) (url, fileName, stripTop, version string, err error)
}

type VariantSpec struct {
	Version  string
	URL      string
	FileName string
	StripTop string

	Notes string
}

var DownloadCatalog = map[string]DownloadSpec{
	"Apache": {
		Version:    "2.4.68 (VS18, win64)",
		URL:        "https://www.apachelounge.com/download/VS18/binaries/httpd-2.4.68-260827-Win64-VS18.zip",
		FileName:   "httpd-2.4.68-260827-Win64-VS18.zip",
		InstallDir: "bin/apache",
		StripTop:   "Apache24/",
		Kind:       Zip,

		CheckFile: "conf/httpd.conf",
		Notes:     "Apache Lounge build — requires VC++ 2015-2022 Redistributable.",

		URLResolver: resolveApacheLatest,
		PostInstall: func(installDir string, log func(string)) error {

			confPath := filepath.Join(installDir, "conf", "httpd.conf")
			data, err := os.ReadFile(confPath)
			if err != nil {
				return nil
			}

			srvroot := strings.ReplaceAll(installDir, `\`, `/`)
			baseDir := filepath.Dir(filepath.Dir(installDir))
			docroot := strings.ReplaceAll(filepath.Join(baseDir, "www"), `\`, `/`)
			phpBin := strings.ReplaceAll(filepath.Join(baseDir, "bin", "php"), `\`, `/`)

			patched := string(data)
			patched = apacheSrvRootRe.ReplaceAllString(patched,
				`Define SRVROOT "`+srvroot+`"`)
			patched = apacheServerNameRe.ReplaceAllString(patched,
				"ServerName localhost:80")
			patched = apacheDocRootRe.ReplaceAllString(patched,
				`DocumentRoot "`+docroot+`"`)
			patched = apacheDirectoryRe.ReplaceAllString(patched,
				`<Directory "`+docroot+`">`)
			patched = apacheDirectoryIdxRe.ReplaceAllString(patched,
				`${1}DirectoryIndex index.php index.html`)
			patched = apacheLoadCgiRe.ReplaceAllString(patched,
				`LoadModule cgi_module modules/mod_cgi.so`)
			patched = apacheLoadActionsRe.ReplaceAllString(patched,
				`LoadModule actions_module modules/mod_actions.so`)
			patched = apacheLoadProxyRe.ReplaceAllString(patched,
				`LoadModule proxy_module modules/mod_proxy.so`)
			patched = apacheLoadProxyHttpRe.ReplaceAllString(patched,
				`LoadModule proxy_http_module modules/mod_proxy_http.so`)

			patched = strings.Replace(patched,
				"AllowOverride None", "AllowOverride All", 1)

			if !strings.Contains(patched, "GoAMPP PHP handler BEGIN") {
				patched += fmt.Sprintf(apachePhpHandlerTemplate, phpBin, phpBin)
			}

			vhostsFS := filepath.Join(baseDir, "conf", "apache", "vhosts.conf")
			vhostsInc := strings.ReplaceAll(vhostsFS, `\`, `/`)
			if !strings.Contains(patched, "GoAMPP vhost include BEGIN") {
				patched += "\r\n# >>> GoAMPP vhost include BEGIN <<<\r\n"
				patched += fmt.Sprintf("Include \"%s\"\r\n", vhostsInc)
				patched += "# <<< GoAMPP vhost include END >>>\r\n"
			}

			if patched != string(data) {
				log("  patched httpd.conf (SRVROOT, ServerName, DocumentRoot, PHP handler)")
				if err := os.WriteFile(confPath, []byte(patched), 0o644); err != nil {
					return err
				}
			}

			_ = os.MkdirAll(docroot, 0o755)

			ensureApacheRuntimeFiles(baseDir, log)
			return nil
		},
	},
	"Nginx": {
		Version:    "1.28.3 stable",
		URL:        "https://nginx.org/download/nginx-1.28.3.zip",
		FileName:   "nginx-1.28.3.zip",
		InstallDir: "bin/nginx",
		StripTop:   "nginx-1.28.3/",
		Kind:       Zip,
		CheckFile:  "nginx.exe",
	},
	"PHP-FPM": {

		Version:    "8.4.22 NTS x64",
		URL:        "https://windows.php.net/downloads/releases/php-8.4.22-nts-Win32-vs17-x64.zip",
		FileName:   "php-8.4.22-nts-Win32-vs17-x64.zip",
		InstallDir: "bin/php",

		Variants: []VariantSpec{
			{Version: "7.4", URL: "https://windows.php.net/downloads/releases/archives/php-7.4.33-nts-Win32-vc15-x64.zip", FileName: "php-7.4.33-nts-Win32-vc15-x64.zip", Notes: "PHP 7.4 — needs VC15 (VS2017) runtime."},
			{Version: "8.0", URL: "https://windows.php.net/downloads/releases/archives/php-8.0.30-nts-Win32-vs16-x64.zip", FileName: "php-8.0.30-nts-Win32-vs16-x64.zip", Notes: "PHP 8.0 — VS16 (VS2019) runtime."},
			{Version: "8.1", URL: "https://windows.php.net/downloads/releases/archives/php-8.1.31-nts-Win32-vs16-x64.zip", FileName: "php-8.1.31-nts-Win32-vs16-x64.zip", Notes: "PHP 8.1 — VS16 (VS2019) runtime."},
			{Version: "8.2", URL: "https://windows.php.net/downloads/releases/archives/php-8.2.27-nts-Win32-vs16-x64.zip", FileName: "php-8.2.27-nts-Win32-vs16-x64.zip", Notes: "PHP 8.2 — VS16 (VS2019) runtime."},
			{Version: "8.3", URL: "https://windows.php.net/downloads/releases/archives/php-8.3.15-nts-Win32-vs16-x64.zip", FileName: "php-8.3.15-nts-Win32-vs16-x64.zip", Notes: "PHP 8.3 — VS16 (VS2019) runtime."},
			{Version: "8.4", URL: "https://windows.php.net/downloads/releases/php-8.4.22-nts-Win32-vs17-x64.zip", FileName: "php-8.4.22-nts-Win32-vs17-x64.zip", Notes: "PHP 8.4 — VS17 (VS2022) runtime, v14.4+."},
			{Version: "8.5", URL: "https://downloads.php.net/~windows/releases/archives/php-8.5.7-nts-Win32-vs17-x64.zip", FileName: "php-8.5.7-nts-Win32-vs17-x64.zip", Notes: "PHP 8.5 — VS17 (VS2022) runtime, v14.4+."},
		},
		StripTop:  "",
		Kind:      Zip,
		CheckFile: "php-cgi.exe",
		Notes:     "Requires VC++ 2015-2022 Redistributable x64.",
		PostInstall: func(installDir string, log func(string)) error {

			iniPath := filepath.Join(installDir, "php.ini")
			var data []byte
			if b, err := os.ReadFile(iniPath); err == nil {
				data = b
			} else {
				dev := filepath.Join(installDir, "php.ini-development")
				b, derr := os.ReadFile(dev)
				if derr != nil {
					return nil
				}
				data = b
				log("  created php.ini from php.ini-development")
			}

			baseDir := filepath.Dir(filepath.Dir(installDir))
			tmpDir := strings.ReplaceAll(filepath.Join(baseDir, "tmp"), `\`, `/`)
			if err := os.MkdirAll(tmpDir, 0o755); err != nil {
				return err
			}

			s := string(data)

			patches := []struct{ re, replacement string }{
				{`(?m)^;?\s*extension_dir\s*=\s*"\./"`, `;extension_dir = "./"`},
				{`(?m)^;?\s*extension_dir\s*=\s*"ext"`, `extension_dir = "ext"`},
				{`(?m)^;?\s*upload_tmp_dir\s*=.*`, `upload_tmp_dir = "` + tmpDir + `"`},
				{`(?m)^;?\s*session\.save_path\s*=.*`, `session.save_path = "` + tmpDir + `"`},
				{`(?m)^;?\s*cgi\.force_redirect\s*=.*`, `cgi.force_redirect = 0`},
				{`(?m)^;?\s*cgi\.fix_pathinfo\s*=.*`, `cgi.fix_pathinfo=1`},

				{`(?m)^;?\s*display_errors\s*=.*`, `display_errors = Off`},
				{`(?m)^;?\s*display_startup_errors\s*=.*`, `display_startup_errors = Off`},
			}
			for _, p := range patches {
				s = regexp.MustCompile(p.re).ReplaceAllString(s, p.replacement)
			}

			exts := []string{

				"bz2", "curl", "exif", "fileinfo", "ftp", "gd",
				"gettext", "gmp", "mbstring", "openssl", "zip",

				"intl",

				"mysqli", "pdo_mysql",
				"pdo_pgsql", "pgsql",
				"pdo_sqlite", "sqlite3",

				"soap", "sockets", "xsl",

				"sodium",
			}
			for _, e := range exts {
				re := regexp.MustCompile(`(?m)^;extension\s*=\s*` + e + `\b.*`)
				s = re.ReplaceAllString(s, "extension="+e)
			}

			if err := os.WriteFile(iniPath, []byte(s), 0o644); err != nil {
				return err
			}
			log("  patched php.ini (extension_dir, session.save_path, 8 extensions)")

			ensurePhpRuntimeDLLs(baseDir, log)
			return nil
		},
	},
	"MySQL": {

		Version:    "MariaDB 11.4.10 LTS",
		URL:        "https://archive.mariadb.org/mariadb-11.4.10/winx64-packages/mariadb-11.4.10-winx64.zip",
		FileName:   "mariadb-11.4.10-winx64.zip",
		InstallDir: "bin/mysql",
		StripTop:   "mariadb-11.4.10-winx64/",
		Kind:       Zip,

		CheckFile: "data/mysql",
		Notes:     "MariaDB — MySQL-compatible drop-in.",
		PostInstall: func(installDir string, log func(string)) error {
			dataDir := filepath.Join(installDir, "data")
			if _, err := os.Stat(filepath.Join(dataDir, "mysql")); err == nil {
				log("  data dir already initialized, skipping")
				return nil
			}

			if entries, _ := os.ReadDir(dataDir); len(entries) > 0 {
				log("  wiping stale data/ from earlier broken install")
				if err := os.RemoveAll(dataDir); err != nil {
					return fmt.Errorf("wipe data dir: %w", err)
				}
				if err := os.MkdirAll(dataDir, 0o755); err != nil {
					return fmt.Errorf("recreate data dir: %w", err)
				}
			}

			candidates := []string{
				filepath.Join(installDir, "bin", "mariadb-install-db.exe"),
				filepath.Join(installDir, "bin", "mysql_install_db.exe"),
			}
			var initExe string
			for _, c := range candidates {
				if _, err := os.Stat(c); err == nil {
					initExe = c
					break
				}
			}
			if initExe == "" {
				return fmt.Errorf("mariadb-install-db.exe not found")
			}
			log("  initializing MariaDB data directory (this can take ~30s)...")
			cmd := exec.Command(initExe, "--datadir="+dataDir)
			cmd.Dir = filepath.Join(installDir, "bin")
			out, err := cmd.CombinedOutput()
			if err != nil {

				log("  install-db output: " + strings.TrimSpace(string(out)))
				return err
			}
			return nil
		},
	},
	"PostgreSQL": {

		Version:    "16.6 LTS (EDB)",
		URL:        "https://get.enterprisedb.com/postgresql/postgresql-16.6-1-windows-x64-binaries.zip",
		FileName:   "postgresql-16.6-1-windows-x64-binaries.zip",
		InstallDir: "bin/pgsql",
		StripTop:   "pgsql/",
		Kind:       Zip,

		CheckFile: "data/PG_VERSION",
		Notes:     "EDB 16.6 LTS — most stable Postgres on Windows; supported until 2028.",
		PostInstall: func(installDir string, log func(string)) error {
			dataDir := filepath.Join(installDir, "data")
			if _, err := os.Stat(filepath.Join(dataDir, "PG_VERSION")); err == nil {
				log("  cluster already initialized, skipping")
				return nil
			}
			initExe := filepath.Join(installDir, "bin", "initdb.exe")
			if _, err := os.Stat(initExe); err != nil {
				return fmt.Errorf("initdb.exe not found")
			}

			pgBin := filepath.Join(installDir, "bin")
			runtimeDir := filepath.Dir(filepath.Dir(installDir))
			runtimeSrc := filepath.Join(runtimeDir, "runtime")
			if _, err := os.Stat(runtimeSrc); err == nil {
				for _, dll := range []string{"vcruntime140.dll", "vcruntime140_1.dll", "msvcp140.dll"} {
					_ = copyFile(
						filepath.Join(runtimeSrc, dll),
						filepath.Join(pgBin, dll),
					)
				}
				log("  seeded VC++ 14.44 runtime into pgsql/bin/")
			}

			pwTmp := filepath.Join(os.TempDir(), "goampp-pg-pw.txt")
			if err := os.WriteFile(pwTmp, []byte("postgres"), 0o600); err != nil {
				return fmt.Errorf("write pw file: %w", err)
			}
			defer os.Remove(pwTmp)

			log("  initializing PostgreSQL cluster (user=postgres, password=postgres, locale=C, auth=trust)...")
			cmd := exec.Command(initExe,
				"-D", dataDir,
				"-U", "postgres",
				"--pwfile="+pwTmp,
				"-E", "SQL_ASCII",
				"-A", "trust",
				"--locale=C",
				"--no-instructions",
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				log("  initdb output: " + strings.TrimSpace(string(out)))
				return err
			}
			return nil
		},
	},
	"Redis": {
		Version:    "5.0.14.1 (tporadowski)",
		URL:        "https://github.com/tporadowski/redis/releases/download/v5.0.14.1/Redis-x64-5.0.14.1.zip",
		FileName:   "Redis-x64-5.0.14.1.zip",
		InstallDir: "bin/redis",
		StripTop:   "",
		Kind:       Zip,
		CheckFile:  "redis-server.exe",
		Notes:      "Port by Tomasz Poradowski — the Microsoft fork is abandoned.",
	},
	"phpMyAdmin": {
		Version:    "5.2.3",
		URL:        "https://files.phpmyadmin.net/phpMyAdmin/5.2.3/phpMyAdmin-5.2.3-all-languages.zip",
		FileName:   "phpMyAdmin-5.2.3-all-languages.zip",
		InstallDir: "www/phpmyadmin",
		StripTop:   "phpMyAdmin-5.2.3-all-languages/",
		Kind:       Zip,
		CheckFile:  "index.php",
		Notes:      "Served by Apache — make sure Apache's DocumentRoot points at {base}/www.",
		PostInstall: func(installDir string, log func(string)) error {

			target := filepath.Join(installDir, "config.inc.php")
			var data []byte
			if b, err := os.ReadFile(target); err == nil {
				data = b
			} else {
				sample := filepath.Join(installDir, "config.sample.inc.php")
				b, derr := os.ReadFile(sample)
				if derr != nil {
					return nil
				}
				data = b
				log("  created config.inc.php from sample")
			}

			var secret [24]byte
			if _, err := cryptorand.Read(secret[:]); err == nil {
				enc := base64.StdEncoding.EncodeToString(secret[:])
				reBf := regexp.MustCompile(`(?m)^\$cfg\['blowfish_secret'\]\s*=\s*'[^']*';.*$`)
				data = reBf.ReplaceAllLiteral(data,
					[]byte(`$cfg['blowfish_secret'] = '`+enc+`'; /* auto-generated by GoAMPP */`))
			}

			reAllow := regexp.MustCompile(`(?m)^\$cfg\['Servers'\]\[\$i\]\['AllowNoPassword'\]\s*=\s*(?:true|false);.*$`)
			data = reAllow.ReplaceAllLiteral(data,
				[]byte(`$cfg['Servers'][$i]['AllowNoPassword'] = true; /* local dev — empty root password is fine */`))

			log("  patched config.inc.php (blowfish_secret, AllowNoPassword)")
			return os.WriteFile(target, data, 0o644)
		},
	},
	"pgweb": {

		Version:    "0.16.2",
		URL:        "https://github.com/sosedoff/pgweb/releases/download/v0.16.2/pgweb_windows_amd64.zip",
		FileName:   "pgweb_windows_amd64.zip",
		InstallDir: "bin/pgweb",
		Kind:       Zip,
		CheckFile:  "pgweb.exe",
		Notes:      "Modern PostgreSQL web client — runs at http://localhost:8081 once started.",
	},
	"Adminer": {
		Version:    "5.4.2",
		URL:        "https://github.com/vrana/adminer/releases/download/v5.4.2/adminer-5.4.2-en.php",
		FileName:   "adminer-5.4.2-en.php",
		InstallDir: "www/adminer",
		Kind:       File,
		TargetFile: "index.php",
		CheckFile:  "index.php",
		PostInstall: func(installDir string, log func(string)) error {

			target := filepath.Join(installDir, "index.php")
			data, err := os.ReadFile(target)
			if err != nil {
				return fmt.Errorf("read adminer: %w", err)
			}
			re := regexp.MustCompile(`if\(\$\w+==""\)return\s+sprintf\('Adminer does not support`)
			patched := re.ReplaceAllLiteralString(string(data),
				`if(false)return sprintf('Adminer does not support`)
			if patched == string(data) {
				log("  Adminer: empty-password guard pattern not found (already patched?)")
				return nil
			}
			if err := os.WriteFile(target, []byte(patched), 0o644); err != nil {
				return fmt.Errorf("write patched adminer: %w", err)
			}
			log("  patched Adminer to allow blank-password local dev logins")
			return nil
		},
	},

	"Composer": {
		Version:    "latest stable",
		URL:        "https://getcomposer.org/composer-stable.phar",
		FileName:   "composer-stable.phar",
		InstallDir: "bin/php",
		Kind:       File,
		TargetFile: "composer.phar",
		CheckFile:  "composer.bat",
		Notes:      "Composer — PHP dependency manager. Installs into bin/php alongside php.exe.",
		PostInstall: func(installDir string, log func(string)) error {
			bat := filepath.Join(installDir, "composer.bat")
			if _, err := os.Stat(bat); err == nil {
				return nil
			}
			content := "@echo off\r\nphp \"%~dp0composer.phar\" %*\r\n"
			if err := os.WriteFile(bat, []byte(content), 0o644); err != nil {
				return fmt.Errorf("write composer.bat: %w", err)
			}
			log("  created composer.bat wrapper")
			return nil
		},
	},

	"Node.js": {
		Version:    "22.22.2 LTS",
		URL:        "https://nodejs.org/dist/v22.22.2/node-v22.22.2-win-x64.zip",
		FileName:   "node-v22.22.2-win-x64.zip",
		InstallDir: "bin/node",
		StripTop:   "node-v22.22.2-win-x64/",
		Kind:       Zip,
		CheckFile:  "node.exe",
		Notes:      "JavaScript runtime — npm and npx are included alongside node.exe.",
		Variants: []VariantSpec{
			{Version: "18", URL: "https://nodejs.org/dist/v18.20.5/node-v18.20.5-win-x64.zip", FileName: "node-v18.20.5-win-x64.zip", StripTop: "node-v18.20.5-win-x64/", Notes: "Node 18 LTS (Hydrogen, EOL April 2025)."},
			{Version: "20", URL: "https://nodejs.org/dist/v20.18.1/node-v20.18.1-win-x64.zip", FileName: "node-v20.18.1-win-x64.zip", StripTop: "node-v20.18.1-win-x64/", Notes: "Node 20 LTS (Iron)."},
			{Version: "22", URL: "https://nodejs.org/dist/v22.22.2/node-v22.22.2-win-x64.zip", FileName: "node-v22.22.2-win-x64.zip", StripTop: "node-v22.22.2-win-x64/", Notes: "Node 22 LTS (Jod) — current default."},
		},
	},
	"Python": {
		Version:    "3.13.13 (embeddable)",
		URL:        "https://www.python.org/ftp/python/3.13.13/python-3.13.13-embed-amd64.zip",
		FileName:   "python-3.13.13-embed-amd64.zip",
		InstallDir: "bin/python",
		StripTop:   "",
		Kind:       Zip,
		CheckFile:  "python.exe",
		Notes:      "Embeddable Python — pip is bootstrapped automatically post-install.",
		Variants: []VariantSpec{
			{Version: "3.10", URL: "https://www.python.org/ftp/python/3.10.11/python-3.10.11-embed-amd64.zip", FileName: "python-3.10.11-embed-amd64.zip", Notes: "Python 3.10 (security-only)."},
			{Version: "3.11", URL: "https://www.python.org/ftp/python/3.11.9/python-3.11.9-embed-amd64.zip", FileName: "python-3.11.9-embed-amd64.zip", Notes: "Python 3.11."},
			{Version: "3.12", URL: "https://www.python.org/ftp/python/3.12.7/python-3.12.7-embed-amd64.zip", FileName: "python-3.12.7-embed-amd64.zip", Notes: "Python 3.12."},
			{Version: "3.13", URL: "https://www.python.org/ftp/python/3.13.13/python-3.13.13-embed-amd64.zip", FileName: "python-3.13.13-embed-amd64.zip", Notes: "Python 3.13 — current default."},
		},
		PostInstall: func(installDir string, log func(string)) error {

			files, _ := filepath.Glob(filepath.Join(installDir, "python*._pth"))
			for _, p := range files {
				data, err := os.ReadFile(p)
				if err == nil {
					patched := strings.ReplaceAll(string(data),
						"#import site", "import site")
					if patched != string(data) {
						log("  uncommented import site in " + filepath.Base(p))
						_ = os.WriteFile(p, []byte(patched), 0o644)
					}
				}
			}

			pipExe := filepath.Join(installDir, "Scripts", "pip.exe")
			if _, err := os.Stat(pipExe); err == nil {
				log("  pip already installed")
				return nil
			}
			getPip := filepath.Join(installDir, "get-pip.py")
			log("  downloading get-pip.py from bootstrap.pypa.io")
			if err := httpDownload("https://bootstrap.pypa.io/get-pip.py", getPip, log, nil); err != nil {
				log("  get-pip download failed (non-fatal): " + err.Error())
				return nil
			}
			log("  bootstrapping pip via python.exe get-pip.py ...")
			pyExe := filepath.Join(installDir, "python.exe")
			cmd := exec.Command(pyExe, getPip, "--no-warn-script-location", "--quiet")
			cmd.Dir = installDir
			cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
			if out, err := cmd.CombinedOutput(); err != nil {
				log("  pip bootstrap output: " + strings.TrimSpace(string(out)))
				return err
			}
			log("  pip ready")
			return nil
		},
	},
	"Go": {
		Version:    "1.26.2",
		URL:        "https://go.dev/dl/go1.26.2.windows-amd64.zip",
		FileName:   "go1.26.2.windows-amd64.zip",
		InstallDir: "bin/go",
		StripTop:   "go/",
		Kind:       Zip,
		CheckFile:  "bin/go.exe",
		Notes:      "Go compiler + standard tooling. Use `go run`, `go build`, `go mod`.",
	},
	"Java": {
		Version:    "Temurin JDK 21.0.10+7",
		URL:        "https://github.com/adoptium/temurin21-binaries/releases/download/jdk-21.0.10%2B7/OpenJDK21U-jdk_x64_windows_hotspot_21.0.10_7.zip",
		FileName:   "OpenJDK21U-jdk_x64_windows_hotspot_21.0.10_7.zip",
		InstallDir: "bin/java",
		StripTop:   "jdk-21.0.10+7/",
		Kind:       Zip,
		CheckFile:  "bin/java.exe",
		Notes:      "Eclipse Temurin LTS — javac, java, jar all under bin/.",
	},
	"Julia": {
		Version:    "1.11.5",
		URL:        "https://julialang-s3.julialang.org/bin/winnt/x64/1.11/julia-1.11.5-win64.zip",
		FileName:   "julia-1.11.5-win64.zip",
		InstallDir: "bin/julia",
		StripTop:   "julia-1.11.5/",
		Kind:       Zip,
		CheckFile:  "bin/julia.exe",
		Notes:      "Julia — high-performance scientific computing language. REPL + package manager included.",
	},
	"Zig": {
		Version:     "latest stable",
		URL:         "https://ziglang.org/download/0.14.0/zig-windows-x86_64-0.14.0.zip",
		FileName:    "zig-windows-x86_64-0.14.0.zip",
		InstallDir:  "bin/zig",
		StripTop:    "zig-windows-x86_64-0.14.0/",
		Kind:        Zip,
		CheckFile:   "zig.exe",
		Notes:       "Zig — systems programming language + build system + C compiler.",
		URLResolver: resolveZigLatest,
	},
	"Dart": {
		Version:    "3.7.2",
		URL:        "https://storage.googleapis.com/dart-archive/channels/stable/release/3.7.2/sdk/dartsdk-windows-x64-release.zip",
		FileName:   "dartsdk-windows-x64-release.zip",
		InstallDir: "bin/dart",
		StripTop:   "dart-sdk/",
		Kind:       Zip,
		CheckFile:  "bin/dart.exe",
		Notes:      "Dart SDK — optimised for client + server; use with Flutter.",
	},
	"Lua": {
		Version:    "5.4.7",
		URL:        "https://iweb.dl.sourceforge.net/project/luabinaries/5.4.7/Tools%20Executables/lua-5.4.7_Win64_bin.zip",
		FileName:   "lua-5.4.7_Win64_bin.zip",
		InstallDir: "bin/lua",
		StripTop:   "",
		Kind:       Zip,
		CheckFile:  "lua54.exe",
		Notes:      "Lua 5.4 scripting language. Executable is lua54.exe.",
	},
	"Ruby": {

		Version:    "3.4.4",
		URL:        "https://github.com/oneclick/rubyinstaller2/releases/download/RubyInstaller-3.4.4-1/rubyinstaller-3.4.4-1-x64.exe",
		FileName:   "rubyinstaller-3.4.4-1-x64.exe",
		InstallDir: "bin/ruby",
		Kind:       Exe,
		CheckFile:  "bin/ruby.exe",
		Notes:      "Ruby 3.4.4 via RubyInstaller2 — silent NSIS install.",
	},
	"Rust": {

		Version:    "stable (via rustup)",
		URL:        "https://static.rust-lang.org/rustup/dist/x86_64-pc-windows-msvc/rustup-init.exe",
		FileName:   "rustup-init.exe",
		InstallDir: "bin/rust",
		Kind:       File,
		TargetFile: "rustup-init.exe",
		CheckFile:  ".cargo/bin/cargo.exe",
		Notes:      "Rust stable toolchain — cargo + rustc land in bin/rust/.cargo/bin/.",
		PostInstall: func(installDir string, log func(string)) error {
			rustupInit := filepath.Join(installDir, "rustup-init.exe")
			if _, err := os.Stat(rustupInit); err != nil {
				return fmt.Errorf("rustup-init.exe not found in %s", installDir)
			}
			log("  installing Rust stable via rustup (3–5 min first time)...")
			cmd := exec.Command(rustupInit,
				"-y",
				"--no-modify-path",
				"--default-toolchain", "stable",
				"--profile", "default",
			)
			cmd.Env = append(os.Environ(),
				"RUSTUP_HOME="+filepath.Join(installDir, ".rustup"),
				"CARGO_HOME="+filepath.Join(installDir, ".cargo"),
			)
			cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
			if out, err := cmd.CombinedOutput(); err != nil {
				log("  rustup output: " + strings.TrimSpace(string(out)))
				return err
			}
			log("  Rust stable ready — cargo at bin/rust/.cargo/bin/cargo.exe")
			return nil
		},
	},
	"Kotlin": {
		Version:    "2.1.21",
		URL:        "https://github.com/JetBrains/kotlin/releases/download/v2.1.21/kotlin-compiler-2.1.21.zip",
		FileName:   "kotlin-compiler-2.1.21.zip",
		InstallDir: "bin/kotlin",
		StripTop:   "kotlinc/",
		Kind:       Zip,
		CheckFile:  "bin/kotlinc.bat",
		Notes:      "Kotlin compiler — requires Java (install Java card first).",
	},
	"Haskell": {

		Version:    "GHC 9.10.1",
		URL:        "https://downloads.haskell.org/ghc/9.10.1/ghc-9.10.1-x86_64-unknown-mingw32.zip",
		FileName:   "ghc-9.10.1-x86_64-unknown-mingw32.zip",
		InstallDir: "bin/haskell",
		StripTop:   "ghc-9.10.1/",
		Kind:       Zip,
		CheckFile:  "bin/ghc.exe",
		Notes:      "GHC 9.10.1 — Glasgow Haskell Compiler. Large download (~570 MB).",
	},
	"Elixir": {

		Version:    "1.18.3 (OTP 27)",
		URL:        "https://github.com/elixir-lang/elixir/releases/download/v1.18.3/elixir-otp-27.zip",
		FileName:   "elixir-otp-27.zip",
		InstallDir: "bin/elixir",
		StripTop:   "",
		Kind:       Zip,
		CheckFile:  "bin/elixir.bat",
		Notes:      "Elixir 1.18.3 — requires Erlang OTP 27 (install Erlang card first).",
	},
	"Crystal": {

		Version:    "1.15.1",
		URL:        "https://github.com/crystal-lang/crystal/releases/download/1.15.1/crystal-1.15.1-windows-x86_64-msvc-unsupported.zip",
		FileName:   "crystal-1.15.1-windows-x86_64-msvc-unsupported.zip",
		InstallDir: "bin/crystal",
		StripTop:   "crystal-1.15.1-windows-x86_64-msvc-unsupported/",
		Kind:       Zip,
		CheckFile:  "crystal.exe",
		Notes:      "Crystal 1.15.1 — experimental Windows build; statically-typed Ruby-like language.",
	},
	"Scala": {
		Version:    "2.13.16",
		URL:        "https://downloads.lightbend.com/scala/2.13.16/scala-2.13.16.zip",
		FileName:   "scala-2.13.16.zip",
		InstallDir: "bin/scala",
		StripTop:   "scala-2.13.16/",
		Kind:       Zip,
		CheckFile:  "bin/scala.bat",
		Notes:      "Scala 2.13 LTS — requires Java (install Java card first).",
	},

	"Erlang": {

		Version:    "27.3.4 (OTP-27)",
		URL:        "https://github.com/erlang/otp/releases/download/OTP-27.3.4/otp_win64_27.3.4.exe",
		FileName:   "otp_win64_27.3.4.exe",
		InstallDir: "bin/erlang",
		Kind:       Exe,
		CheckFile:  "bin/erl.exe",
		Notes:      "Erlang/OTP runtime — required by RabbitMQ. Installed silently.",
	},

	"RabbitMQ": {

		Version:    "4.3.0",
		URL:        "https://github.com/rabbitmq/rabbitmq-server/releases/download/v4.3.0/rabbitmq-server-windows-4.3.0.zip",
		FileName:   "rabbitmq-server-windows-4.3.0.zip",
		InstallDir: "bin/rabbitmq",
		StripTop:   "rabbitmq_server-4.3.0/",
		Kind:       Zip,
		CheckFile:  "sbin/rabbitmq-server.bat",
		Notes:      "AMQP message broker — AMQP :5672, management UI :15672 (guest/guest).",
		PostInstall: func(installDir string, log func(string)) error {
			baseDir := filepath.Dir(filepath.Dir(installDir))

			erlangDir := filepath.Join(baseDir, "bin", "erlang")
			if _, err := os.Stat(filepath.Join(erlangDir, "bin", "erl.exe")); err != nil {
				log("  Erlang not found — downloading OTP 27.3.4...")
				const erlURL = "https://github.com/erlang/otp/releases/download/OTP-27.3.4/otp_win64_27.3.4.exe"
				erlExe, err := cacheFor(baseDir, log).Fetch("otp_win64_27.3.4.exe", erlURL, nil)
				if err != nil {
					return fmt.Errorf("erlang download: %w", err)
				}
				log("  installing Erlang OTP silently...")
				if err := os.MkdirAll(erlangDir, 0o755); err != nil {
					return fmt.Errorf("erlang dir: %w", err)
				}
				abs, _ := filepath.Abs(erlangDir)
				cmd := exec.Command(erlExe, "/S", "/D="+abs)
				cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
				if out, err := cmd.CombinedOutput(); err != nil {
					log("  erlang installer: " + strings.TrimSpace(string(out)))
					return fmt.Errorf("erlang install: %w", err)
				}
				log("  Erlang OTP installed")
			}

			dataDir := filepath.Join(baseDir, "data", "rabbitmq")
			if err := os.MkdirAll(dataDir, 0o755); err != nil {
				return fmt.Errorf("create rabbitmq data dir: %w", err)
			}
			log("  created data/rabbitmq/ — RabbitMQ stores state here")

			pluginsExe := filepath.Join(installDir, "sbin", "rabbitmq-plugins.bat")
			if _, err := os.Stat(pluginsExe); err == nil {
				cmd := exec.Command("cmd.exe", "/c", pluginsExe, "enable", "rabbitmq_management")
				cmd.Env = append(os.Environ(),
					"ERLANG_HOME="+erlangDir,
					"RABBITMQ_BASE="+dataDir,
				)
				cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
				if out, err := cmd.CombinedOutput(); err != nil {
					log("  rabbitmq-plugins: " + strings.TrimSpace(string(out)))

				} else {
					log("  enabled rabbitmq_management plugin")
				}
			}
			return nil
		},
	},

	"MinIO": {

		Version:    "latest",
		URL:        "https://dl.min.io/server/minio/release/windows-amd64/minio.exe",
		FileName:   "minio.exe",
		InstallDir: "bin/minio",
		Kind:       File,
		TargetFile: "minio.exe",
		CheckFile:  "minio.exe",
		Notes:      "S3-compatible object storage — API :9010, console at http://localhost:9011 (user: minioadmin).",
		PostInstall: func(installDir string, log func(string)) error {
			baseDir := filepath.Dir(filepath.Dir(installDir))
			dataDir := filepath.Join(baseDir, "data", "minio")
			if err := os.MkdirAll(dataDir, 0o755); err != nil {
				return fmt.Errorf("create minio data dir: %w", err)
			}
			log("  created data/minio/ — MinIO will store objects here")
			return nil
		},
	},

	"Mailpit": {

		Version:    "1.30.0",
		URL:        "https://github.com/axllent/mailpit/releases/download/v1.30.0/mailpit-windows-amd64.zip",
		FileName:   "mailpit-windows-amd64.zip",
		InstallDir: "bin/mailpit",
		StripTop:   "",
		Kind:       Zip,
		CheckFile:  "mailpit.exe",
		Notes:      "Email testing — SMTP :1025, web UI at http://localhost:8025.",
	},
}
