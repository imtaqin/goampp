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
ScriptAlias "/__goampp-php-bin__/" "%s/"
<Directory "%s">
    AllowOverride None
    Options +ExecCGI
    Require all granted
</Directory>
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
}

// DownloadCatalog maps ServiceConf.Name → DownloadSpec. URLs were verified
// against upstream pages. Any download link can go stale — update this map
// to bump versions.
var DownloadCatalog = map[string]DownloadSpec{
	"Apache": {
		Version:    "2.4.66 (VS18, win64)",
		URL:        "https://www.apachelounge.com/download/VS18/binaries/httpd-2.4.66-260223-Win64-VS18.zip",
		FileName:   "httpd-2.4.66-260223-Win64-VS18.zip",
		InstallDir: "bin/apache",
		StripTop:   "Apache24/",
		Kind:       "zip",
		CheckFile:  "bin/httpd.exe",
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
			vhostsInc := strings.ReplaceAll(
				filepath.Join(baseDir, "conf", "apache", "vhosts.conf"),
				`\`, `/`)
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
		Version:    "8.5.5 NTS x64",
		URL:        "https://downloads.php.net/~windows/releases/php-8.5.5-nts-Win32-vs17-x64.zip",
		FileName:   "php-8.5.5-nts-Win32-vs17-x64.zip",
		InstallDir: "bin/php",
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
			}
			for _, p := range patches {
				s = regexp.MustCompile(p.re).ReplaceAllString(s, p.replacement)
			}

			// Extensions phpMyAdmin needs. We uncomment these by
			// regex-replacing the leading ";" on each line.
			exts := []string{
				"curl", "fileinfo", "gd", "mbstring", "mysqli",
				"openssl", "pdo_mysql", "zip",
			}
			for _, e := range exts {
				re := regexp.MustCompile(`(?m)^;\s*extension\s*=\s*` + e + `\b.*`)
				s = re.ReplaceAllString(s, "extension="+e)
			}

			if err := os.WriteFile(iniPath, []byte(s), 0o644); err != nil {
				return err
			}
			log("  patched php.ini (extension_dir, session.save_path, 8 extensions)")
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
		CheckFile:  "bin/mysqld.exe",
		Notes:      "MariaDB — MySQL-compatible drop-in.",
		PostInstall: func(installDir string, log func(string)) error {
			dataDir := filepath.Join(installDir, "data")
			if _, err := os.Stat(filepath.Join(dataDir, "mysql")); err == nil {
				log("  data dir already initialized, skipping")
				return nil
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
		Version:    "18.3 (EDB)",
		URL:        "https://sbp.enterprisedb.com/getfile.jsp?fileid=1260119",
		FileName:   "postgresql-18.3-1-windows-x64-binaries.zip",
		InstallDir: "bin/pgsql",
		StripTop:   "pgsql/",
		Kind:       "zip",
		CheckFile:  "bin/postgres.exe",
		Notes:      "EDB binaries — requires VC++ 2015-2022 Redistributable.",
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
			log("  initializing PostgreSQL cluster...")
			cmd := exec.Command(initExe,
				"-D", dataDir,
				"-U", "postgres",
				"-E", "UTF8",
				"-A", "trust",
				"--locale=C",
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
			var secret [24]byte
			if _, err := cryptorand.Read(secret[:]); err == nil {
				enc := base64.StdEncoding.EncodeToString(secret[:])
				reBf := regexp.MustCompile(`(?m)^\$cfg\['blowfish_secret'\]\s*=\s*'[^']*';.*$`)
				data = reBf.ReplaceAll(data,
					[]byte(`$cfg['blowfish_secret'] = '`+enc+`'; /* auto-generated by GoAMPP */`))
			}

			reAllow := regexp.MustCompile(`(?m)^\$cfg\['Servers'\]\[\$i\]\['AllowNoPassword'\]\s*=\s*(?:true|false);.*$`)
			data = reAllow.ReplaceAll(data,
				[]byte(`$cfg['Servers'][$i]['AllowNoPassword'] = true; /* local dev — empty root password is fine */`))

			log("  patched config.inc.php (blowfish_secret, AllowNoPassword)")
			return os.WriteFile(target, data, 0o644)
		},
	},
	"Adminer": {
		Version:    "5.4.2",
		URL:        "https://github.com/vrana/adminer/releases/download/v5.4.2/adminer-5.4.2-en.php",
		FileName:   "adminer-5.4.2-en.php",
		InstallDir: "www/adminer",
		Kind:       "file",
		TargetFile: "index.php",
		CheckFile:  "index.php",
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

// DownloadAndInstall is the single entry point called from the UI. It's
// synchronous — callers should wrap it in a goroutine so the UI thread
// doesn't block on a 50 MB download. `progress` can be NopProgress if
// the caller only wants log output.
func DownloadAndInstall(name, baseDir string, log func(string), progress ProgressFunc) error {
	spec, ok := DownloadCatalog[name]
	if !ok {
		return fmt.Errorf("no download info registered for %q", name)
	}

	downloadMu.Lock()
	defer downloadMu.Unlock()

	installDir := filepath.Join(baseDir, filepath.FromSlash(spec.InstallDir))

	// Quick short-circuit if the check file already exists.
	if spec.CheckFile != "" {
		if _, err := os.Stat(filepath.Join(installDir, filepath.FromSlash(spec.CheckFile))); err == nil {
			log(fmt.Sprintf("[%s] already installed at %s", name, installDir))
			return nil
		}
	}

	if progress == nil {
		progress = NopProgress
	}
	progress("starting", name, 0, 0)

	log(fmt.Sprintf("[%s] %s — starting install", name, spec.Version))
	if spec.Notes != "" {
		log("  note: " + spec.Notes)
	}

	// Stage 1: HTTP download to downloads/ (kept around so re-installs
	// don't re-download — the user can rm -rf downloads/ to force).
	dlDir := filepath.Join(baseDir, "downloads")
	if err := os.MkdirAll(dlDir, 0o755); err != nil {
		progress("idle", name, 0, 0)
		return fmt.Errorf("create downloads dir: %w", err)
	}
	dlPath := filepath.Join(dlDir, spec.FileName)
	if _, err := os.Stat(dlPath); err != nil {
		if err := httpDownload(spec.URL, dlPath, log, func(done, total int64) {
			progress("downloading", name, done, total)
		}); err != nil {
			progress("idle", name, 0, 0)
			return fmt.Errorf("download: %w", err)
		}
	} else {
		log("  using cached download: " + spec.FileName)
		// Report a "full" download so the progress bar fills up visually
		// even when we skipped the network fetch.
		if fi, err := os.Stat(dlPath); err == nil {
			progress("downloading", name, fi.Size(), fi.Size())
		}
	}

	// Stage 2: extract or copy into the install directory.
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		progress("idle", name, 0, 0)
		return fmt.Errorf("create install dir: %w", err)
	}

	switch spec.Kind {
	case "zip":
		log(fmt.Sprintf("  extracting into %s", installDir))
		progress("extracting", name, 0, 0)
		if err := extractZip(dlPath, installDir, spec.StripTop, func(done, total int64) {
			progress("extracting", name, done, total)
		}); err != nil {
			progress("idle", name, 0, 0)
			return fmt.Errorf("extract: %w", err)
		}
	case "file":
		target := spec.TargetFile
		if target == "" {
			target = spec.FileName
		}
		log(fmt.Sprintf("  copying to %s/%s", installDir, target))
		if err := copyFile(dlPath, filepath.Join(installDir, target)); err != nil {
			progress("idle", name, 0, 0)
			return fmt.Errorf("copy: %w", err)
		}
	default:
		progress("idle", name, 0, 0)
		return fmt.Errorf("unknown kind: %q", spec.Kind)
	}

	// Stage 3: run the post-install hook (initdb, config rewrite, etc.)
	if spec.PostInstall != nil {
		progress("post-install", name, 0, 0)
		if err := spec.PostInstall(installDir, log); err != nil {
			progress("idle", name, 0, 0)
			return fmt.Errorf("post-install: %w", err)
		}
	}

	log(fmt.Sprintf("[%s] install complete", name))
	progress("done", name, 0, 0)
	// Brief pause at 100% so the user sees the bar full before it resets.
	time.Sleep(500 * time.Millisecond)
	progress("idle", name, 0, 0)
	return nil
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
