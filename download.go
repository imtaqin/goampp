//go:build windows

package main

import (
	"archive/zip"
	cryptorand "crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// apacheSrvRootRe matches `Define SRVROOT "..."` case-insensitively so we
// can rewrite Apache's hardcoded install path regardless of how the ZIP
// author capitalised it. The default is `"C:/Apache24"` but a future build
// could just as easily ship `"c:/Apache24"` or `"D:\\foo"`.
var apacheSrvRootRe = regexp.MustCompile(`(?i)Define SRVROOT "[^"]*"`)

// apacheServerNameRe matches the commented-out `#ServerName ...` line
// that ships in Apache Lounge's default httpd.conf. Uncommenting it
// silences the AH00558 "could not reliably determine FQDN" warning.
var apacheServerNameRe = regexp.MustCompile(`(?m)^#?ServerName\s+[^\n]*`)

// These regexes chain together to make Apache serve files out of
// goampp's `www/` directory instead of the default bin/apache/htdocs,
// enable mod_cgi + mod_actions so PHP is handled via classic CGI,
// and enable mod_proxy + mod_proxy_http so framework dev servers
// (Node, Python, Go, Java) can sit behind a reverse-proxy vhost.
//
// We intentionally do NOT use mod_proxy_fcgi for PHP — that module
// has a long-standing Windows bug (Apache #55345) where it builds
// the upstream URL as "fcgi://host:port" + "C:/script.php" without
// a separator, yielding "fcgi://host:9000C:/..." which parses as
// port="9000C" and fails DNS. mod_proxy_http (used for dev-server
// vhosts) doesn't have this issue because the upstream URL is a
// real HTTP host, not a file path.
var (
	apacheDocRootRe        = regexp.MustCompile(`(?m)^DocumentRoot\s+"[^"]*"`)
	apacheDirectoryRe      = regexp.MustCompile(`(?m)^<Directory\s+"[^"]*/htdocs"\s*>`)
	apacheDirectoryIdxRe   = regexp.MustCompile(`(?m)^(\s*)DirectoryIndex\s+[^\n]*`)
	apacheLoadCgiRe        = regexp.MustCompile(`(?m)^#\s*LoadModule\s+cgi_module\s+modules/mod_cgi\.so`)
	apacheLoadActionsRe    = regexp.MustCompile(`(?m)^#\s*LoadModule\s+actions_module\s+modules/mod_actions\.so`)
	apacheLoadProxyRe      = regexp.MustCompile(`(?m)^#\s*LoadModule\s+proxy_module\s+modules/mod_proxy\.so`)
	apacheLoadProxyHttpRe  = regexp.MustCompile(`(?m)^#\s*LoadModule\s+proxy_http_module\s+modules/mod_proxy_http\.so`)
)

// apachePhpHandlerTemplate is the stanza we append to httpd.conf so .php
// files are handed to php-cgi.exe via classic CGI. Format args are:
//   1. absolute path to bin/php/  (for ScriptAlias target + <Directory>)
//
// We use mod_cgi + mod_actions instead of mod_proxy_fcgi because of
// Apache bug 55345 — on Windows, mod_proxy_fcgi concatenates
// "fcgi://host:port" with the Windows script path "C:/..." without a
// separator, yielding "fcgi://host:9000C:/..." which parses as
// port="9000C" and fails DNS with "AH00898: DNS lookup failure for:
// 127.0.0.1:9000c". The classic CGI approach has no such issue —
// Apache spawns php-cgi.exe per request directly, no proxy URL to
// build. Slightly slower per-request than FastCGI but bulletproof on
// Windows.
//
// The block is guarded by sentinel comments so re-running the
// post-install doesn't duplicate it.
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

// DownloadSpec describes how to fetch and install one service.
//
// All file paths here are RELATIVE to {base}. The runtime joins them with
// app.baseDir when it actually creates directories.
type DownloadSpec struct {
	Version    string // for log messages; no semver parsing
	URL        string // direct download URL (redirects are followed)
	FileName   string // local filename to save the download as
	InstallDir string // e.g. "bin/apache" (flattened into)
	// StripTop is the top-level folder inside the ZIP to strip. If the
	// archive's files live under "Apache24/...", set this to "Apache24/"
	// and we'll drop the prefix so they land in InstallDir directly.
	// Leave empty when the archive is already flat.
	StripTop string
	// Kind is either "zip" (extract archive) or "file" (single file copy).
	Kind string
	// TargetFile is used when Kind == "file" — the in-place filename for
	// single-file drops like Adminer.
	TargetFile string
	// CheckFile is a path relative to InstallDir. If it exists, we assume
	// the service is already installed and skip the download.
	CheckFile string
	// PostInstall runs after extraction — initdb, mariadb-install-db, etc.
	// Receives the absolute install dir so it doesn't need to re-expand.
	PostInstall func(installDir string, log func(string)) error
	// Notes shown in the log when installation starts. Optional.
	Notes string

	// Variants enumerates all installable versions (e.g. PHP 8.2/8.3/8.4/8.5).
	// When non-nil, DownloadAndInstall(name, version, ...) reads URL +
	// FileName from the matching variant instead of the spec's flat URL,
	// and installs into "{InstallDir}-{version}" (e.g. bin/php-8.4)
	// instead of InstallDir directly. The active variant is then exposed
	// at the canonical InstallDir via a directory junction so existing
	// service ExePath references (e.g. bin/php/php-cgi.exe) just work.
	//
	// Empty for single-version services (Apache, MariaDB, Postgres,
	// Redis, phpMyAdmin, Adminer) — the historical flat-URL path applies.
	Variants []VariantSpec
}

// VariantSpec is one installable version inside a DownloadSpec.
// Exists so the service catalogue can carry e.g. PHP 7.4 and PHP 8.5
// side by side without duplicating the rest of the spec (CheckFile,
// PostInstall, etc).
type VariantSpec struct {
	// Version is the human-readable label shown in the per-card
	// version dropdown (e.g. "8.4", "20", "3.12"). Used as the
	// suffix when computing the install directory:
	// bin/php-{Version} → bin/php-8.4. Pick a slug that's
	// filesystem-safe; semver dots are fine on Windows.
	Version  string
	URL      string
	FileName string
	StripTop string
	// Notes overrides the parent DownloadSpec.Notes when this variant
	// is selected. Useful for "this version requires VC++ 14.x".
	Notes string
}

// DownloadCatalog maps ServiceConf.Name → DownloadSpec. URLs were verified
// against upstream pages. Any download link can go stale — update this map
// to bump versions.
var DownloadCatalog = map[string]DownloadSpec{
	"Apache": {
		Version:    "2.4.67 (VS18, win64)",
		URL:        "https://www.apachelounge.com/download/VS18/binaries/httpd-2.4.67-260504-Win64-VS18.zip",
		FileName:   "httpd-2.4.67-260504-Win64-VS18.zip",
		InstallDir: "bin/apache",
		StripTop:   "Apache24/",
		Kind:       "zip",
		// Sentinel must prove the FULL extraction succeeded, not just that
		// httpd.exe landed somewhere. Apache won't even start without
		// conf/httpd.conf, so use that — antivirus quarantine or an
		// interrupted unzip leaves bin/ but trims conf/, and a single-
		// file check would still report "installed" against that wreck.
		CheckFile:  "conf/httpd.conf",
		Notes:      "Apache Lounge build — requires VC++ 2015-2022 Redistributable.",
		PostInstall: func(installDir string, log func(string)) error {
			// Apache's shipped httpd.conf needs a fistful of tweaks
			// before it's useful for a PHP dev setup. We patch all of
			// them in one pass so reinstalling is idempotent.
			//
			//   1. Define SRVROOT  → real install dir (else ServerRoot
			//      errors on boot)
			//   2. #ServerName     → ServerName localhost:80 (silences
			//      AH00558)
			//   3. DocumentRoot    → goampp/www (so we share one
			//      docroot across Apache and Nginx and phpMyAdmin
			//      lands at /phpmyadmin)
			//   4. <Directory>     → match the new docroot + allow
			//      .htaccess overrides so projects can drop their own
			//   5. DirectoryIndex  → add index.php first
			//   6. LoadModule      → uncomment mod_proxy + mod_proxy_fcgi
			//      (needed for the PHP handler below)
			//   7. Append the FilesMatch PHP handler stanza that
			//      forwards .php requests to PHP-FPM on port 9000
			confPath := filepath.Join(installDir, "conf", "httpd.conf")
			data, err := os.ReadFile(confPath)
			if err != nil {
				return nil // not fatal — user may have a custom conf
			}

			// Normalise paths to forward slashes — Apache accepts
			// them and they dodge the backslash-escape minefield in
			// httpd.conf.
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

			// Unlock AllowOverride for the docroot so .htaccess works
			// out of the box. Scoped swap — only the first occurrence,
			// which is the docroot block.
			patched = strings.Replace(patched,
				"AllowOverride None", "AllowOverride All", 1)

			// Append the PHP handler block only if we haven't already
			// (sentinel comment lives inside apachePhpHandlerTemplate).
			if !strings.Contains(patched, "GoAMPP PHP handler BEGIN") {
				patched += fmt.Sprintf(apachePhpHandlerTemplate, phpBin, phpBin)
			}

			// Pull in the auto-generated vhosts file so user projects
			// become visible to Apache the moment they're scaffolded.
			// Idempotent: we guard by sentinel comment so repeat
			// post-installs don't stack the Include line.
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

			// Make sure the docroot exists so Apache doesn't refuse
			// to start. We don't populate it — the user drops their
			// own files in.
			_ = os.MkdirAll(docroot, 0o755)

			// Seed vhosts.conf + welcome page + phpinfo shortcut.
			// All three are idempotent — the helper only writes
			// files that don't exist yet, so reinstalls preserve
			// the user's custom edits. See welcome.go for the
			// shared ensureApacheRuntimeFiles implementation;
			// main.go's WmCreate handler calls the same helper on
			// every launch so upgraded installs self-heal too.
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
		Kind:       "zip",
		CheckFile:  "nginx.exe",
	},
	"PHP-FPM": {
		// Default Version label for the legacy single-version flow —
		// kept so older configs without an active_version still work.
		// When the user picks a different version from the card's
		// dropdown, that variant's URL takes over.
		Version:    "8.5.6 NTS x64",
		URL:        "https://downloads.php.net/~windows/releases/php-8.5.6-nts-Win32-vs17-x64.zip",
		FileName:   "php-8.5.6-nts-Win32-vs17-x64.zip",
		InstallDir: "bin/php",
		// Variants is the per-major-version PHP catalogue. Each entry
		// installs into bin/php-{Version}/; the active one is exposed
		// at bin/php/ via a directory junction, so existing references
		// in config.json (ExePath: bin/php/php-cgi.exe) keep working.
		Variants: []VariantSpec{
			{Version: "7.4", URL: "https://windows.php.net/downloads/releases/archives/php-7.4.33-nts-Win32-vc15-x64.zip", FileName: "php-7.4.33-nts-Win32-vc15-x64.zip", Notes: "PHP 7.4 — needs VC15 (VS2017) runtime."},
			{Version: "8.0", URL: "https://windows.php.net/downloads/releases/archives/php-8.0.30-nts-Win32-vs16-x64.zip", FileName: "php-8.0.30-nts-Win32-vs16-x64.zip", Notes: "PHP 8.0 — VS16 (VS2019) runtime."},
			{Version: "8.1", URL: "https://windows.php.net/downloads/releases/archives/php-8.1.31-nts-Win32-vs16-x64.zip", FileName: "php-8.1.31-nts-Win32-vs16-x64.zip", Notes: "PHP 8.1 — VS16 (VS2019) runtime."},
			{Version: "8.2", URL: "https://windows.php.net/downloads/releases/archives/php-8.2.27-nts-Win32-vs16-x64.zip", FileName: "php-8.2.27-nts-Win32-vs16-x64.zip", Notes: "PHP 8.2 — VS16 (VS2019) runtime."},
			{Version: "8.3", URL: "https://windows.php.net/downloads/releases/archives/php-8.3.15-nts-Win32-vs16-x64.zip", FileName: "php-8.3.15-nts-Win32-vs16-x64.zip", Notes: "PHP 8.3 — VS16 (VS2019) runtime."},
			{Version: "8.4", URL: "https://windows.php.net/downloads/releases/php-8.4.21-nts-Win32-vs17-x64.zip", FileName: "php-8.4.21-nts-Win32-vs17-x64.zip", Notes: "PHP 8.4 — VS17 (VS2022) runtime, v14.4+."},
			{Version: "8.5", URL: "https://downloads.php.net/~windows/releases/php-8.5.6-nts-Win32-vs17-x64.zip", FileName: "php-8.5.6-nts-Win32-vs17-x64.zip", Notes: "PHP 8.5 — VS17 (VS2022) runtime, v14.4+."},
		},
		StripTop:   "", // archive is flat
		Kind:       "zip",
		CheckFile:  "php-cgi.exe",
		Notes:      "Requires VC++ 2015-2022 Redistributable x64.",
		PostInstall: func(installDir string, log func(string)) error {
			// Apache uses mod_cgi to launch php-cgi.exe per request, but
			// it still needs a real php.ini to know about extensions,
			// sessions, etc. We seed one from php.ini-development and
			// then patch it for goampp's layout:
			//   - extension_dir = "ext"                     (find bundled .dll extensions)
			//   - upload_tmp_dir + session.save_path        (writable by CGI processes)
			//   - cgi.force_redirect=0 / cgi.fix_pathinfo=1 (CGI over Apache)
			//   - enable mysqli, pdo_mysql, mbstring, etc.  (required by phpMyAdmin)
			iniPath := filepath.Join(installDir, "php.ini")
			var data []byte
			if b, err := os.ReadFile(iniPath); err == nil {
				data = b
			} else {
				dev := filepath.Join(installDir, "php.ini-development")
				b, derr := os.ReadFile(dev)
				if derr != nil {
					return nil // nothing to work with
				}
				data = b
				log("  created php.ini from php.ini-development")
			}

			// baseDir is two levels up from installDir (bin/php).
			baseDir := filepath.Dir(filepath.Dir(installDir))
			tmpDir := strings.ReplaceAll(filepath.Join(baseDir, "tmp"), `\`, `/`)
			if err := os.MkdirAll(tmpDir, 0o755); err != nil {
				return err
			}

			s := string(data)

			// Settings replacements. Each is an idempotent regex so
			// reinstalling doesn't stack semicolons or duplicate lines.
			patches := []struct{ re, replacement string }{
				{`(?m)^;?\s*extension_dir\s*=\s*"\./"`, `;extension_dir = "./"`},
				{`(?m)^;?\s*extension_dir\s*=\s*"ext"`, `extension_dir = "ext"`},
				{`(?m)^;?\s*upload_tmp_dir\s*=.*`, `upload_tmp_dir = "` + tmpDir + `"`},
				{`(?m)^;?\s*session\.save_path\s*=.*`, `session.save_path = "` + tmpDir + `"`},
				{`(?m)^;?\s*cgi\.force_redirect\s*=.*`, `cgi.force_redirect = 0`},
				{`(?m)^;?\s*cgi\.fix_pathinfo\s*=.*`, `cgi.fix_pathinfo=1`},
				// CGI: any PHP warning printed before headers becomes
				// "malformed header from script" and breaks pages
				// (e.g. phpMyAdmin). Errors still go to log_errors.
				{`(?m)^;?\s*display_errors\s*=.*`, `display_errors = Off`},
				{`(?m)^;?\s*display_startup_errors\s*=.*`, `display_startup_errors = Off`},
			}
			for _, p := range patches {
				s = regexp.MustCompile(p.re).ReplaceAllString(s, p.replacement)
			}

			// Extensions phpMyAdmin needs. Match only the actual
			// extension-list lines (`;extension=...`, no space after
			// the `;`) — NOT the `;   extension=mysqli` example in
			// the comments block, which would cause a duplicate load
			// and "Module 'mysqli' is already loaded" warnings that
			// poison CGI headers.
			// Enable everything modern PHP dev typically wants on
			// out of the box. Grouped by purpose so future edits
			// know what each line buys you. Anything that needs an
			// external client library to even load (oci8, sqlsrv,
			// pdo_firebird, snmp, ldap on some Windows builds) is
			// deliberately skipped — listing it here would just
			// throw "Unable to load dynamic library" on startup.
			exts := []string{
				// Core HTTP / file / encoding
				"bz2", "curl", "exif", "fileinfo", "ftp", "gd",
				"gettext", "gmp", "mbstring", "openssl", "zip",

				// Internationalisation (Laravel, CodeIgniter 4,
				// Symfony all require ext-intl).
				"intl",

				// Database drivers — every common dev DB.
				"mysqli", "pdo_mysql",
				"pdo_pgsql", "pgsql",
				"pdo_sqlite", "sqlite3",

				// Remote / IPC / serialisation
				"soap", "sockets", "xsl",

				// Modern crypto.
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

			// Mirror the bundled v14.44 VC++ runtime DLLs into
			// bin/php/ right now so the user's first php-cgi.exe
			// invocation already has the right runtime — instead of
			// hitting "VCRUNTIME140.dll 14.0 is not compatible"
			// until the next GoAMPP relaunch.
			ensurePhpRuntimeDLLs(baseDir, log)
			return nil
		},
	},
	"MySQL": {
		// MariaDB is our stand-in for MySQL — same wire protocol, simpler
		// ZIP distribution, and it drops a real `mysqld.exe`.
		Version:    "MariaDB 11.4.10 LTS",
		URL:        "https://archive.mariadb.org/mariadb-11.4.10/winx64-packages/mariadb-11.4.10-winx64.zip",
		FileName:   "mariadb-11.4.10-winx64.zip",
		InstallDir: "bin/mysql",
		StripTop:   "mariadb-11.4.10-winx64/",
		Kind:       "zip",
		// Sentinel must prove BOTH (a) the zip fully extracted (share/ is
		// where mysqld looks for error messages and the bootstrap SQL —
		// no share/, no working server) and (b) PostInstall ran (which
		// is where mariadb-install-db seeds data/mysql/ with the
		// privilege tables). Using mysqld.exe alone would silently OK
		// an install missing install-db, share/, and the system DB —
		// exactly the state that produces "Table 'mysql.db' doesn't
		// exist" on every start.
		CheckFile:  "data/mysql",
		Notes:      "MariaDB — MySQL-compatible drop-in.",
		PostInstall: func(installDir string, log func(string)) error {
			dataDir := filepath.Join(installDir, "data")
			if _, err := os.Stat(filepath.Join(dataDir, "mysql")); err == nil {
				log("  data dir already initialized, skipping")
				return nil
			}
			// If a previous broken start left InnoDB files in data/
			// without the system tables, mariadb-install-db.exe will
			// refuse to seed (it won't overwrite an existing cluster).
			// Wipe the dir first — these files are useless without
			// the privilege tables anyway.
			if entries, _ := os.ReadDir(dataDir); len(entries) > 0 {
				log("  wiping stale data/ from earlier broken install")
				if err := os.RemoveAll(dataDir); err != nil {
					return fmt.Errorf("wipe data dir: %w", err)
				}
				if err := os.MkdirAll(dataDir, 0o755); err != nil {
					return fmt.Errorf("recreate data dir: %w", err)
				}
			}
			// Try the modern tool name first, fall back to the legacy one.
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
				// Log the actual error for diagnosis.
				log("  install-db output: " + strings.TrimSpace(string(out)))
				return err
			}
			return nil
		},
	},
	"PostgreSQL": {
		// Stayed on 16.6 LTS instead of 17/18 because the newer EDB
		// Windows binaries crash bootstrap with 0xC0000005 on a
		// surprising number of Windows builds (Defender / WinSxS /
		// reparse-point quirks combined with PG's child-process
		// init code). 16.6 is the last release where bootstrap is
		// known-stable across the Windows configurations we test
		// against — and 16 is supported by community LTS until
		// 2028, so users aren't left behind.
		Version:    "16.6 LTS (EDB)",
		URL:        "https://get.enterprisedb.com/postgresql/postgresql-16.6-1-windows-x64-binaries.zip",
		FileName:   "postgresql-16.6-1-windows-x64-binaries.zip",
		InstallDir: "bin/pgsql",
		StripTop:   "pgsql/",
		Kind:       "zip",
		// Sentinel proves BOTH the zip extracted AND initdb
		// completed (data/PG_VERSION is the very last file initdb
		// creates). bin/postgres.exe alone would silently OK an
		// install whose data/ initdb wiped after a crash, leaving
		// "could not access directory" on every Start until the
		// user manually intervened.
		CheckFile:  "data/PG_VERSION",
		Notes:      "EDB 16.6 LTS — most stable Postgres on Windows; supported until 2028.",
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

			// Drop the bundled v14.44 VC++ runtime DLLs next to
			// postgres.exe before initdb runs. Postgres 18 binaries
			// from EDB are linked against the same MSVC runtime as
			// PHP — a downlevel C:\Windows\System32\vcruntime140.dll
			// makes the bootstrap child process die with
			// 0xC0000005 (access violation) during
			// "performing post-bootstrap initialization". Mirroring
			// the good runtime locally sidesteps the system DLL.
			//
			// We do this BEFORE running initdb, since initdb itself
			// shells out to postgres.exe for the bootstrap.
			pgBin := filepath.Join(installDir, "bin")
			runtimeDir := filepath.Dir(filepath.Dir(installDir)) // {base}
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

			// PG 16 doesn't support --locale-provider=builtin (that's
			// PG 17+). Instead we rely on:
			//   --locale=C            — minimal C locale, no ICU lookup
			//   -E SQL_ASCII          — avoids any UTF-8 / ICU code path
			//                            during bootstrap (UTF8 still
			//                            usable per-database after init)
			//   --no-instructions     — suppresses the "Success" footer
			// SQL_ASCII at cluster level is the safest known config —
			// individual databases created later via psql/Adminer can
			// still be UTF8 (CREATE DATABASE foo ENCODING 'UTF8' ...).
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
		StripTop:   "", // flat archive
		Kind:       "zip",
		CheckFile:  "redis-server.exe",
		Notes:      "Port by Tomasz Poradowski — the Microsoft fork is abandoned.",
	},
	"phpMyAdmin": {
		Version:    "5.2.3",
		URL:        "https://files.phpmyadmin.net/phpMyAdmin/5.2.3/phpMyAdmin-5.2.3-all-languages.zip",
		FileName:   "phpMyAdmin-5.2.3-all-languages.zip",
		InstallDir: "www/phpmyadmin",
		StripTop:   "phpMyAdmin-5.2.3-all-languages/",
		Kind:       "zip",
		CheckFile:  "index.php",
		Notes:      "Served by Apache — make sure Apache's DocumentRoot points at {base}/www.",
		PostInstall: func(installDir string, log func(string)) error {
			// Seed config.inc.php from the sample if it doesn't exist
			// yet, then patch two values for a working dev default:
			//
			//   1. blowfish_secret — required for cookie auth. We
			//      generate one per install using crypto/rand so every
			//      copy of GoAMPP has a unique secret.
			//   2. AllowNoPassword — default MariaDB installs have an
			//      empty root password, and phpMyAdmin 5.2+ refuses
			//      those by default. Flip this to true so users can
			//      just click "Go" on the login screen.
			target := filepath.Join(installDir, "config.inc.php")
			var data []byte
			if b, err := os.ReadFile(target); err == nil {
				data = b
			} else {
				sample := filepath.Join(installDir, "config.sample.inc.php")
				b, derr := os.ReadFile(sample)
				if derr != nil {
					return nil // not fatal
				}
				data = b
				log("  created config.inc.php from sample")
			}

			// Generate a 32-byte random blowfish secret. Base64-encoded
			// because we're inlining it into a PHP string literal — no
			// escape worries with alphanumerics + /+.
			// Go's regexp.ReplaceAll interprets `$` in the replacement
			// as a capture-group reference (`$1`, `$cfg` → "", etc.),
			// which silently strips `$cfg` and `$i` and produces an
			// invalid PHP file ("['blowfish_secret'] = ..."). Use
			// ReplaceAllLiteral so the replacement is taken verbatim.
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
		// Beautiful single-binary PostgreSQL web client. Way nicer
		// than Adminer for Postgres specifically — built for PG so
		// it gets the schema browser, index editor, EXPLAIN viewer,
		// and CSV/JSON export without forcing the multi-DB lowest-
		// common-denominator UX.
		Version:    "0.16.2",
		URL:        "https://github.com/sosedoff/pgweb/releases/download/v0.16.2/pgweb_windows_amd64.zip",
		FileName:   "pgweb_windows_amd64.zip",
		InstallDir: "bin/pgweb",
		Kind:       "zip",
		CheckFile:  "pgweb.exe",
		Notes:      "Modern PostgreSQL web client — runs at http://localhost:8081 once started.",
	},
	"Adminer": {
		Version:    "5.4.2",
		URL:        "https://github.com/vrana/adminer/releases/download/v5.4.2/adminer-5.4.2-en.php",
		FileName:   "adminer-5.4.2-en.php",
		InstallDir: "www/adminer",
		Kind:       "file",
		TargetFile: "index.php",
		CheckFile:  "index.php",
		PostInstall: func(installDir string, log func(string)) error {
			// Adminer 5.4.2 ships a single-file minified bundle.
			// Subclassing won't work because every class is renamed
			// to obscured one-letter symbols (`class c`, `class AS`),
			// so `extends \Adminer\Adminer` finds nothing and the
			// override is silently dropped. Instead patch the bundle
			// itself: rewrite the empty-password guard inside login()
			// so it never trips. The pattern we target is:
			//
			//   function login($X,$Y){if($Y=="")return
			//     sprintf('Adminer does not support accessing ...
			//
			// We swap the `if($Y=="")` to `if(false)` — the rest of
			// login() already returns true on success, so the
			// password gate is skipped end-to-end. Variable names
			// are minified per release, so we match `\$\w+=="" `
			// instead of pinning to a specific identifier.
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

	// ----- Language runtimes (used by frameworks.go scaffolders) -----
	//
	// These aren't long-running services like Apache/MySQL — they're
	// command-line tools (node.exe, python.exe, go.exe, java.exe)
	// that the framework scaffolder invokes to install/build/serve a
	// project. We track them in DownloadCatalog so the existing
	// auto-download UX (button + progress bar + extract) works for
	// them too.

	"Node.js": {
		Version:    "22.22.2 LTS",
		URL:        "https://nodejs.org/dist/v22.22.2/node-v22.22.2-win-x64.zip",
		FileName:   "node-v22.22.2-win-x64.zip",
		InstallDir: "bin/node",
		StripTop:   "node-v22.22.2-win-x64/",
		Kind:       "zip",
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
		StripTop:   "", // embeddable ZIP has no top-level folder
		Kind:       "zip",
		CheckFile:  "python.exe",
		Notes:      "Embeddable Python — pip is bootstrapped automatically post-install.",
		Variants: []VariantSpec{
			{Version: "3.10", URL: "https://www.python.org/ftp/python/3.10.11/python-3.10.11-embed-amd64.zip", FileName: "python-3.10.11-embed-amd64.zip", Notes: "Python 3.10 (security-only)."},
			{Version: "3.11", URL: "https://www.python.org/ftp/python/3.11.9/python-3.11.9-embed-amd64.zip", FileName: "python-3.11.9-embed-amd64.zip", Notes: "Python 3.11."},
			{Version: "3.12", URL: "https://www.python.org/ftp/python/3.12.7/python-3.12.7-embed-amd64.zip", FileName: "python-3.12.7-embed-amd64.zip", Notes: "Python 3.12."},
			{Version: "3.13", URL: "https://www.python.org/ftp/python/3.13.13/python-3.13.13-embed-amd64.zip", FileName: "python-3.13.13-embed-amd64.zip", Notes: "Python 3.13 — current default."},
		},
		PostInstall: func(installDir string, log func(string)) error {
			// 1. The embeddable distribution ships with `import site`
			// commented out in python3X._pth, which prevents pip from
			// finding installed packages. Uncomment it.
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
			// 2. Download get-pip.py and bootstrap pip. Skipped if
			// Scripts/pip.exe already exists.
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
		Kind:       "zip",
		CheckFile:  "bin/go.exe",
		Notes:      "Go compiler + standard tooling. Use `go run`, `go build`, `go mod`.",
	},
	"Java": {
		Version:    "Temurin JDK 21.0.10+7",
		URL:        "https://github.com/adoptium/temurin21-binaries/releases/download/jdk-21.0.10%2B7/OpenJDK21U-jdk_x64_windows_hotspot_21.0.10_7.zip",
		FileName:   "OpenJDK21U-jdk_x64_windows_hotspot_21.0.10_7.zip",
		InstallDir: "bin/java",
		StripTop:   "jdk-21.0.10+7/",
		Kind:       "zip",
		CheckFile:  "bin/java.exe",
		Notes:      "Eclipse Temurin LTS — javac, java, jar all under bin/.",
	},
	"Julia": {
		Version:    "1.11.5",
		URL:        "https://julialang-s3.julialang.org/bin/winnt/x64/1.11/julia-1.11.5-win64.zip",
		FileName:   "julia-1.11.5-win64.zip",
		InstallDir: "bin/julia",
		StripTop:   "julia-1.11.5/",
		Kind:       "zip",
		CheckFile:  "bin/julia.exe",
		Notes:      "Julia — high-performance scientific computing language. REPL + package manager included.",
	},
	"Zig": {
		Version:    "0.14.0",
		URL:        "https://ziglang.org/download/0.14.0/zig-windows-x86_64-0.14.0.zip",
		FileName:   "zig-windows-x86_64-0.14.0.zip",
		InstallDir: "bin/zig",
		StripTop:   "zig-windows-x86_64-0.14.0/",
		Kind:       "zip",
		CheckFile:  "zig.exe",
		Notes:      "Zig — systems programming language + build system + C compiler.",
	},
	"Dart": {
		Version:    "3.7.2",
		URL:        "https://storage.googleapis.com/dart-archive/channels/stable/release/3.7.2/sdk/dartsdk-windows-x64-release.zip",
		FileName:   "dartsdk-windows-x64-release.zip",
		InstallDir: "bin/dart",
		StripTop:   "dart-sdk/",
		Kind:       "zip",
		CheckFile:  "bin/dart.exe",
		Notes:      "Dart SDK — optimised for client + server; use with Flutter.",
	},
	"Idris2": {
		Version:    "nightly (main)",
		URL:        "https://nightly.link/fdciabdul/Idris2/actions/runs/26355077230/idris2-main-windows-x86_64.zip",
		FileName:   "idris2-main-windows-x86_64.zip",
		InstallDir: "bin/idris2",
		StripTop:   "",
		Kind:       "zip",
		CheckFile:  "idris2.exe",
		Notes:      "Idris2 — dependently-typed functional programming language.",
	},
	"Lua": {
		Version:    "5.4.7",
		URL:        "https://iweb.dl.sourceforge.net/project/luabinaries/5.4.7/Tools%20Executables/lua-5.4.7_Win64_bin.zip",
		FileName:   "lua-5.4.7_Win64_bin.zip",
		InstallDir: "bin/lua",
		StripTop:   "",
		Kind:       "zip",
		CheckFile:  "lua54.exe",
		Notes:      "Lua 5.4 scripting language. Executable is lua54.exe.",
	},
	"Ruby": {
		// RubyInstaller2 — NSIS silent install, supports /D= for custom install dir.
		Version:    "3.4.4",
		URL:        "https://github.com/oneclick/rubyinstaller2/releases/download/RubyInstaller-3.4.4-1/rubyinstaller-3.4.4-1-x64.exe",
		FileName:   "rubyinstaller-3.4.4-1-x64.exe",
		InstallDir: "bin/ruby",
		Kind:       "exe",
		CheckFile:  "bin/ruby.exe",
		Notes:      "Ruby 3.4.4 via RubyInstaller2 — silent NSIS install.",
	},
	"Rust": {
		// Rust installed via rustup-init.exe. We download the bootstrapper as
		// a single file, then PostInstall runs it with RUSTUP_HOME + CARGO_HOME
		// pointing inside our bin/rust/ tree so the toolchain stays portable.
		Version:    "stable (via rustup)",
		URL:        "https://static.rust-lang.org/rustup/dist/x86_64-pc-windows-msvc/rustup-init.exe",
		FileName:   "rustup-init.exe",
		InstallDir: "bin/rust",
		Kind:       "file",
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
		Kind:       "zip",
		CheckFile:  "bin/kotlinc.bat",
		Notes:      "Kotlin compiler — requires Java (install Java card first).",
	},
	"Haskell": {
		// GHC — the reference Haskell compiler. Large download (~570 MB).
		Version:    "GHC 9.10.1",
		URL:        "https://downloads.haskell.org/ghc/9.10.1/ghc-9.10.1-x86_64-unknown-mingw32.zip",
		FileName:   "ghc-9.10.1-x86_64-unknown-mingw32.zip",
		InstallDir: "bin/haskell",
		StripTop:   "ghc-9.10.1/",
		Kind:       "zip",
		CheckFile:  "bin/ghc.exe",
		Notes:      "GHC 9.10.1 — Glasgow Haskell Compiler. Large download (~570 MB).",
	},
	"Elixir": {
		// Pre-compiled Elixir for OTP 27. Archive is flat — bin/elixir.bat etc.
		// at the root. Requires Erlang OTP 27 (install the Erlang card first).
		Version:    "1.18.3 (OTP 27)",
		URL:        "https://github.com/elixir-lang/elixir/releases/download/v1.18.3/elixir-otp-27.zip",
		FileName:   "elixir-otp-27.zip",
		InstallDir: "bin/elixir",
		StripTop:   "",
		Kind:       "zip",
		CheckFile:  "bin/elixir.bat",
		Notes:      "Elixir 1.18.3 — requires Erlang OTP 27 (install Erlang card first).",
	},
	"Crystal": {
		// Experimental Windows support — marked unsupported by the Crystal team
		// but generally works for basic CLI usage and compilation.
		Version:    "1.15.1",
		URL:        "https://github.com/crystal-lang/crystal/releases/download/1.15.1/crystal-1.15.1-windows-x86_64-msvc-unsupported.zip",
		FileName:   "crystal-1.15.1-windows-x86_64-msvc-unsupported.zip",
		InstallDir: "bin/crystal",
		StripTop:   "crystal-1.15.1-windows-x86_64-msvc-unsupported/",
		Kind:       "zip",
		CheckFile:  "crystal.exe",
		Notes:      "Crystal 1.15.1 — experimental Windows build; statically-typed Ruby-like language.",
	},
	"Scala": {
		Version:    "2.13.16",
		URL:        "https://downloads.lightbend.com/scala/2.13.16/scala-2.13.16.zip",
		FileName:   "scala-2.13.16.zip",
		InstallDir: "bin/scala",
		StripTop:   "scala-2.13.16/",
		Kind:       "zip",
		CheckFile:  "bin/scala.bat",
		Notes:      "Scala 2.13 LTS — requires Java (install Java card first).",
	},

	// ----- Storage, messaging & queues -----

	"Erlang": {
		// Erlang/OTP runtime — required by RabbitMQ. NSIS installer,
		// run silently with /S /D=<installDir>. GoAMPP handles this via
		// Kind: "exe" which is treated as a silent installer.
		Version:    "27.3.4 (OTP-27)",
		URL:        "https://github.com/erlang/otp/releases/download/OTP-27.3.4/otp_win64_27.3.4.exe",
		FileName:   "otp_win64_27.3.4.exe",
		InstallDir: "bin/erlang",
		Kind:       "exe",
		CheckFile:  "bin/erl.exe",
		Notes:      "Erlang/OTP runtime — required by RabbitMQ. Installed silently.",
	},

	"RabbitMQ": {
		// AMQP message broker. Requires Erlang (see above) — GoAMPP
		// installs Erlang first via the RabbitMQ PostInstall hook.
		// AMQP on :5672, management UI on :15672 (user: guest/guest).
		Version:    "4.3.0",
		URL:        "https://github.com/rabbitmq/rabbitmq-server/releases/download/v4.3.0/rabbitmq-server-windows-4.3.0.zip",
		FileName:   "rabbitmq-server-windows-4.3.0.zip",
		InstallDir: "bin/rabbitmq",
		StripTop:   "rabbitmq_server-4.3.0/",
		Kind:       "zip",
		CheckFile:  "sbin/rabbitmq-server.bat",
		Notes:      "AMQP message broker — AMQP :5672, management UI :15672 (guest/guest).",
		PostInstall: func(installDir string, log func(string)) error {
			baseDir := filepath.Dir(filepath.Dir(installDir))

			// Ensure Erlang is installed before RabbitMQ can run.
			// Inline install to avoid a DownloadCatalog cycle.
			erlangDir := filepath.Join(baseDir, "bin", "erlang")
			if _, err := os.Stat(filepath.Join(erlangDir, "bin", "erl.exe")); err != nil {
				log("  Erlang not found — downloading OTP 27.3.4...")
				erlExe := filepath.Join(baseDir, "downloads", "otp_win64_27.3.4.exe")
				const erlURL = "https://github.com/erlang/otp/releases/download/OTP-27.3.4/otp_win64_27.3.4.exe"
				if _, err := os.Stat(erlExe); err != nil {
					if err := httpDownload(erlURL, erlExe, log, nil); err != nil {
						return fmt.Errorf("erlang download: %w", err)
					}
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

			// Create the data directory RabbitMQ uses for mnesia, logs, etc.
			dataDir := filepath.Join(baseDir, "data", "rabbitmq")
			if err := os.MkdirAll(dataDir, 0o755); err != nil {
				return fmt.Errorf("create rabbitmq data dir: %w", err)
			}
			log("  created data/rabbitmq/ — RabbitMQ stores state here")

			// Enable the management plugin so the web UI is available.
			// Run rabbitmq-plugins.bat via cmd.exe with ERLANG_HOME set.
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
					// Non-fatal — management plugin missing just means no web UI.
				} else {
					log("  enabled rabbitmq_management plugin")
				}
			}
			return nil
		},
	},

	"MinIO": {
		// Single-binary S3-compatible object storage. API on :9010,
		// web console on :9011 (avoids collision with PHP-FPM on :9000).
		Version:    "latest",
		URL:        "https://dl.min.io/server/minio/release/windows-amd64/minio.exe",
		FileName:   "minio.exe",
		InstallDir: "bin/minio",
		Kind:       "file",
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
		// Modern email testing tool — catches outbound SMTP and exposes
		// a clean web UI to inspect messages. Drop-in replacement for
		// MailHog (still maintained, faster, darker theme).
		Version:    "1.30.0",
		URL:        "https://github.com/axllent/mailpit/releases/download/v1.30.0/mailpit-windows-amd64.zip",
		FileName:   "mailpit-windows-amd64.zip",
		InstallDir: "bin/mailpit",
		StripTop:   "",
		Kind:       "zip",
		CheckFile:  "mailpit.exe",
		Notes:      "Email testing — SMTP :1025, web UI at http://localhost:8025.",
	},
}

// downloadMu serialises downloads so we don't hit the same file twice from
// a frantic double-click, and so HTTP output in the log stays readable.
var downloadMu sync.Mutex

// ProgressFunc is the callback httpDownload fires repeatedly while a
// download is in flight. `done` is the number of bytes streamed so far;
// `total` is the Content-Length (0 if the server didn't send one).
// `stage` describes what the downloader is doing in human terms, e.g.
// "downloading", "extracting", "post-install". The UI uses this to show
// a progress bar strip without reading the log text.
type ProgressFunc func(stage, name string, done, total int64)

// NopProgress is the safe default when a caller doesn't need visual
// feedback (e.g. the Install All sweep still wants to DownloadAndInstall
// but has no single progress bar to drive).
func NopProgress(stage, name string, done, total int64) {}

// DownloadAndInstall is the legacy entry point — installs the
// catalog's default version. Callers wanting a specific variant
// should use DownloadAndInstallVersion directly.
func DownloadAndInstall(name, baseDir string, log func(string), progress ProgressFunc) error {
	return DownloadAndInstallVersion(name, "", baseDir, log, progress)
}

// DownloadAndInstallVersion fetches and installs a specific version
// of a service. `version` may be the empty string (use the catalog's
// flat URL — the historical single-version path) or a label that
// matches a Variants[].Version entry. For multi-version services
// the install lands in {InstallDir}-{version} (e.g. bin/php-8.4)
// and the canonical InstallDir (bin/php) is then exposed as a
// directory junction pointing at the active version.
//
// Synchronous — callers should wrap in a goroutine so the UI thread
// doesn't block on a 50 MB download.
func DownloadAndInstallVersion(name, version, baseDir string, log func(string), progress ProgressFunc) error {
	spec, ok := DownloadCatalog[name]
	if !ok {
		return fmt.Errorf("no download info registered for %q", name)
	}

	// Resolve which set of URL/FileName/StripTop/Notes to use.
	// Empty `version` falls back to the spec's flat fields so
	// pre-variant services (Apache, MariaDB) behave exactly as before.
	url := spec.URL
	fileName := spec.FileName
	stripTop := spec.StripTop
	notes := spec.Notes
	versionLabel := spec.Version
	if len(spec.Variants) > 0 && version != "" {
		var v *VariantSpec
		for i := range spec.Variants {
			if spec.Variants[i].Version == version {
				v = &spec.Variants[i]
				break
			}
		}
		if v == nil {
			return fmt.Errorf("[%s] version %q not in catalogue", name, version)
		}
		url = v.URL
		fileName = v.FileName
		stripTop = v.StripTop
		if v.Notes != "" {
			notes = v.Notes
		}
		versionLabel = v.Version
	}

	downloadMu.Lock()
	defer downloadMu.Unlock()

	canonicalDir := filepath.Join(baseDir, filepath.FromSlash(spec.InstallDir))
	installDir := canonicalDir
	if len(spec.Variants) > 0 && version != "" {
		installDir = canonicalDir + "-" + version // bin/php-8.4
	}

	// Short-circuit if THIS variant's check file is already there.
	// The sentinel is checked relative to the variant install dir
	// so each version gets its own "is installed" decision.
	if spec.CheckFile != "" {
		if _, err := os.Stat(filepath.Join(installDir, filepath.FromSlash(spec.CheckFile))); err == nil {
			log(fmt.Sprintf("[%s] %s already installed at %s", name, versionLabel, installDir))
			// Re-mirror the variant onto the canonical dir even on
			// the short-circuit path — the user may have switched
			// versions and bin/<svc>/ still mirrors the old one.
			if installDir != canonicalDir {
				if err := pointJunction(canonicalDir, installDir, log); err != nil {
					return err
				}
			}
			// And re-run PostInstall against canonicalDir. This is
			// what re-applies the php.ini patches and copies the
			// bundled vcruntime140.dll v14.44 into bin/php after
			// robocopy /MIR wiped it (the source variant dir
			// doesn't ship that DLL — it's our own self-heal). All
			// PostInstall hooks are idempotent so re-running is
			// cheap and safe.
			if spec.PostInstall != nil {
				if err := spec.PostInstall(canonicalDir, log); err != nil {
					return fmt.Errorf("post-install: %w", err)
				}
			}
			return nil
		}
	}

	if progress == nil {
		progress = NopProgress
	}
	progress("starting", name, 0, 0)

	log(fmt.Sprintf("[%s] %s — starting install", name, versionLabel))
	if notes != "" {
		log("  note: " + notes)
	}

	// Stage 1: HTTP download to downloads/.
	dlDir := filepath.Join(baseDir, "downloads")
	if err := os.MkdirAll(dlDir, 0o755); err != nil {
		progress("idle", name, 0, 0)
		return fmt.Errorf("create downloads dir: %w", err)
	}
	dlPath := filepath.Join(dlDir, fileName)
	if _, err := os.Stat(dlPath); err != nil {
		if err := httpDownload(url, dlPath, log, func(done, total int64) {
			progress("downloading", name, done, total)
		}); err != nil {
			progress("idle", name, 0, 0)
			return fmt.Errorf("download: %w", err)
		}
	} else {
		log("  using cached download: " + fileName)
		if fi, err := os.Stat(dlPath); err == nil {
			progress("downloading", name, fi.Size(), fi.Size())
		}
	}

	// Stage 2: extract or copy into the install directory.
	// ensureInstallDir handles the case where installDir is already
	// a junction (canonical bin/php pointing at bin/php-8.2) — plain
	// MkdirAll has been seen to spuriously fail on junctions with
	// "Cannot create a file when that file already exists".
	if err := ensureInstallDir(installDir); err != nil {
		progress("idle", name, 0, 0)
		return fmt.Errorf("create install dir: %w", err)
	}

	switch spec.Kind {
	case "zip":
		log(fmt.Sprintf("  extracting into %s", installDir))
		progress("extracting", name, 0, 0)
		if err := extractZip(dlPath, installDir, stripTop, func(done, total int64) {
			progress("extracting", name, done, total)
		}); err != nil {
			progress("idle", name, 0, 0)
			return fmt.Errorf("extract: %w", err)
		}
	case "file":
		target := spec.TargetFile
		if target == "" {
			target = fileName
		}
		log(fmt.Sprintf("  copying to %s/%s", installDir, target))
		if err := copyFile(dlPath, filepath.Join(installDir, target)); err != nil {
			progress("idle", name, 0, 0)
			return fmt.Errorf("copy: %w", err)
		}
	case "exe":
		// NSIS / Inno Setup silent installer.
		// /S = silent mode (standard NSIS flag).
		// /D=<dir> = install directory (must be absolute, NSIS requirement).
		log(fmt.Sprintf("  running silent installer → %s (this may take 30–60s)", installDir))
		progress("post-install", name, 0, 0)
		absInstallDir, _ := filepath.Abs(installDir)
		cmd := exec.Command(dlPath, "/S", "/D="+absInstallDir)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
		if out, err := cmd.CombinedOutput(); err != nil {
			log("  installer output: " + strings.TrimSpace(string(out)))
			progress("idle", name, 0, 0)
			return fmt.Errorf("silent installer: %w", err)
		}
		log("  installer finished")
	default:
		progress("idle", name, 0, 0)
		return fmt.Errorf("unknown kind: %q", spec.Kind)
	}

	// Stage 2b (multi-version only): repoint the canonical junction at
	// this freshly-extracted variant. PostInstall and the rest of the
	// app reference {baseDir}/bin/<service>/ — the junction makes the
	// active variant transparent.
	if installDir != canonicalDir {
		if err := pointJunction(canonicalDir, installDir, log); err != nil {
			return err
		}
	}

	// Stage 3: run the post-install hook against the canonical path
	// (so config file paths in patches stay version-agnostic).
	if spec.PostInstall != nil {
		progress("post-install", name, 0, 0)
		if err := spec.PostInstall(canonicalDir, log); err != nil {
			progress("idle", name, 0, 0)
			return fmt.Errorf("post-install: %w", err)
		}
	}

	log(fmt.Sprintf("[%s] install complete", name))
	progress("done", name, 0, 0)
	time.Sleep(500 * time.Millisecond)
	progress("idle", name, 0, 0)
	return nil
}

// pointJunction makes `canonical` mirror `target` via a robocopy
// /MIR copy. Earlier versions used a Windows directory junction
// (mklink /J), which is faster but is intermittently flagged by
// Defender / antivirus / file-system filters as an "untrusted
// mount point" — Windows then refuses to traverse the junction
// from unelevated processes, breaking php-cgi.exe spawn and
// editor file open. Mirror copies are slower (a couple of seconds
// per version switch on a normal SSD) but work in every Windows
// configuration we've seen.
//
// Idempotent: if `canonical` already has the same files as
// `target`, robocopy /MIR /XO is a no-op. If `canonical` is a
// stale junction left over from a previous build, it gets removed
// and replaced with a real directory.
func pointJunction(canonical, target string, log func(string)) error {
	// If `canonical` is currently a reparse point (junction/symlink
	// from an older build), drop it BEFORE mirroring — robocopy
	// would otherwise try to copy through the junction and either
	// fail with the same "untrusted mount point" error or duplicate
	// the target's files in a loop.
	if fi, err := os.Lstat(canonical); err == nil {
		isReparse := fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0
		if isReparse {
			if err := os.Remove(canonical); err != nil {
				return fmt.Errorf("remove old junction: %w", err)
			}
			log("  removed legacy junction at " + canonical)
		} else if fi.IsDir() {
			// If a previous install left files here from when we
			// did flat extraction (or the user landed on this code
			// path mid-migration), it'll be overwritten by robocopy
			// /MIR — but if the dir is large and entirely unrelated
			// (different service?), preserve it under -legacy first.
			// Heuristic: presence of our service's CheckFile means
			// it's a previous install of the same service, safe to
			// overwrite. Otherwise preserve.
			//
			// We can't see the spec.CheckFile here, so trust the
			// caller (DownloadAndInstallVersion only calls us with
			// freshly-extracted variant dirs).
		}
	}
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		return fmt.Errorf("create canonical dir: %w", err)
	}
	// robocopy returns 0/1/2/3 for "no copy needed" / "files copied"
	// / "extra files in dest" / "files copied + extras" — all are
	// success. Anything 4+ is a real failure.
	//   /MIR      = mirror (copy + delete extras)
	//   /NJH /NJS = no job header / summary
	//   /NFL /NDL = no file / dir list (quieter log)
	//   /NP       = no per-file progress
	//   /R:1 /W:1 = retry once, 1s wait (defaults are 1M retries, 30s)
	cmd := exec.Command("robocopy", target, canonical,
		"/MIR", "/NJH", "/NJS", "/NFL", "/NDL", "/NP", "/R:1", "/W:1")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	out, err := cmd.CombinedOutput()
	if err != nil {
		// exec.ExitError on success exit codes 1-3 — robocopy uses
		// these to signal copy progress, not failure. Treat as ok.
		if exitErr, ok := err.(*exec.ExitError); ok {
			code := exitErr.ExitCode()
			if code >= 0 && code < 8 {
				err = nil
			}
		}
	}
	if err != nil {
		return fmt.Errorf("robocopy mirror %s → %s: %v: %s",
			target, canonical, err, strings.TrimSpace(string(out)))
	}
	log(fmt.Sprintf("  active version → %s", filepath.Base(target)))
	return nil
}

// samePath compares two filesystem paths after canonicalising case
// and slash style. Windows is case-insensitive and lets you mix /
// and \, but a byte-for-byte string compare would say
// "C:/goampp/bin/php-8.4" != "C:\goampp\bin\php-8.4".
func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// ensureInstallDir prepares the target directory for extraction. If
// it's a regular missing path, MkdirAll. If it's already a junction,
// resolve to the real directory and ensure THAT exists. The plain
// MkdirAll on a junction has been observed to fail on some Windows
// configurations with "Cannot create a file when that file already
// exists" even when the junction target is a healthy directory, so
// we do the work ourselves.
func ensureInstallDir(path string) error {
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			// Junction or symlink — follow it and make sure the
			// resolved target exists.
			real, err := filepath.EvalSymlinks(path)
			if err != nil {
				return fmt.Errorf("resolve junction %s: %w", path, err)
			}
			return os.MkdirAll(real, 0o755)
		}
		if fi.IsDir() {
			return nil
		}
		return fmt.Errorf("%s exists but is not a directory", path)
	}
	return os.MkdirAll(path, 0o755)
}

// SetActiveVariant switches a multi-version service to use the given
// installed variant. If the variant isn't installed yet, it's
// downloaded first. After the call, {baseDir}/{InstallDir} (e.g.
// bin/php) is a junction pointing at {InstallDir}-{version}.
func SetActiveVariant(name, version, baseDir string, log func(string), progress ProgressFunc) error {
	spec, ok := DownloadCatalog[name]
	if !ok {
		return fmt.Errorf("no catalogue entry for %q", name)
	}
	if len(spec.Variants) == 0 {
		return fmt.Errorf("[%s] is not multi-version", name)
	}
	// DownloadAndInstallVersion is idempotent: if the variant is
	// already extracted, it just repoints the junction.
	return DownloadAndInstallVersion(name, version, baseDir, log, progress)
}

// httpDownload streams a URL into dest, showing approximate progress in
// the log at most once per second and via the progress callback on every
// buffer read. Follows redirects and writes to a .part file first so
// partial downloads can't be mistaken for complete.
func httpDownload(url, dest string, log func(string), onProgress func(done, total int64)) error {
	log("  GET " + url)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	// Some servers (GitHub, EDB) 403 on empty UA.
	req.Header.Set("User-Agent", "GoAMPP/0.3 (+https://github.com/goampp)")

	client := &http.Client{
		// Downloads can legitimately take several minutes on a slow line.
		Timeout: 10 * time.Minute,
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	total := resp.ContentLength
	if total > 0 {
		log(fmt.Sprintf("  size: %.1f MB", float64(total)/(1024*1024)))
	}

	partPath := dest + ".part"
	f, err := os.Create(partPath)
	if err != nil {
		return err
	}
	// Ensure we don't leave an orphaned .part if we return early.
	closed := false
	defer func() {
		if !closed {
			f.Close()
			os.Remove(partPath)
		}
	}()

	buf := make([]byte, 128*1024)
	var read int64
	lastLog := time.Now()
	lastProgress := time.Now()
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			read += int64(n)
			// Text log ~1Hz (expensive because it hits the UI thread
			// and rewrites the full Edit buffer).
			if time.Since(lastLog) > time.Second {
				if total > 0 {
					pct := float64(read) * 100 / float64(total)
					log(fmt.Sprintf("  %5.1f%%  %6.1f / %.1f MB",
						pct, float64(read)/(1024*1024), float64(total)/(1024*1024)))
				} else {
					log(fmt.Sprintf("  downloaded %.1f MB", float64(read)/(1024*1024)))
				}
				lastLog = time.Now()
			}
			// Progress bar can update more often (~30Hz) — it's just a
			// PostMessage under the hood, basically free.
			if onProgress != nil && time.Since(lastProgress) > 33*time.Millisecond {
				onProgress(read, total)
				lastProgress = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	// One last progress tick at 100% so the bar matches the final state.
	if onProgress != nil {
		onProgress(read, total)
	}

	if err := f.Close(); err != nil {
		return err
	}
	closed = true
	// Atomic swap: rename into place so the absence of dest always means
	// "unfinished download."
	_ = os.Remove(dest)
	return os.Rename(partPath, dest)
}

// extractZip unpacks a ZIP into destDir. If stripTop is non-empty, any
// entry whose name starts with it has the prefix trimmed, so
// "Apache24/bin/httpd.exe" with stripTop="Apache24/" lands as
// "<destDir>/bin/httpd.exe". Entries outside the prefix are skipped.
//
// Guards against "zip slip" — a malicious archive with "../" in entry
// names trying to write outside destDir. Fires onProgress with the
// entry count so the UI has something to render during extraction.
func extractZip(zipPath, destDir, stripTop string, onProgress func(done, total int64)) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	absDest, err := filepath.Abs(destDir)
	if err != nil {
		return err
	}

	total := int64(len(r.File))
	var done int64
	for _, f := range r.File {
		done++
		if onProgress != nil {
			onProgress(done, total)
		}
		name := f.Name
		if stripTop != "" {
			if !strings.HasPrefix(name, stripTop) {
				continue
			}
			name = strings.TrimPrefix(name, stripTop)
		}
		if name == "" {
			continue
		}

		target := filepath.Join(absDest, filepath.FromSlash(name))
		// Resolve symlinks/.. before the prefix check — this is the
		// actual zip-slip defense.
		absTarget, err := filepath.Abs(target)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(absTarget, absDest+string(os.PathSeparator)) && absTarget != absDest {
			return fmt.Errorf("zip entry escapes destination: %s", f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(absTarget, 0o755); err != nil {
				return err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(absTarget), 0o755); err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(absTarget, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return err
		}
		// archive/zip decompresses sequentially — a large io.Copy here is
		// cheap compared to the network fetch that preceded it.
		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			rc.Close()
			return err
		}
		out.Close()
		rc.Close()
	}
	return nil
}

// copyFile writes a single file byte-for-byte, preserving nothing fancy.
// Used for the Adminer single-PHP-file drop.
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// IsInstalled reports whether a service's on-disk install already passes
// its CheckFile probe. Used by the UI to decide what button label to show.
func IsInstalled(name, baseDir string) bool {
	spec, ok := DownloadCatalog[name]
	if !ok {
		return true // unmanaged → assume the user installed it manually
	}
	if spec.CheckFile == "" {
		return false
	}
	installDir := filepath.Join(baseDir, filepath.FromSlash(spec.InstallDir))
	_, err := os.Stat(filepath.Join(installDir, filepath.FromSlash(spec.CheckFile)))
	return err == nil
}
