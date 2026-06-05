//go:build windows

package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func ensureApacheRuntimeFiles(baseDir string, log func(string)) {

	vhostsFS := filepath.Join(baseDir, "conf", "apache", "vhosts.conf")
	if _, err := os.Stat(vhostsFS); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(vhostsFS), 0o755); err == nil {
			if err := writeApacheVhosts(vhostsFS, baseDir, nil); err == nil {
				log("  self-heal: seeded " + vhostsFS)
			} else {
				log("  self-heal: failed to write vhosts.conf: " + err.Error())
			}
		}
	}

	wwwDir := filepath.Join(baseDir, "www")
	_ = os.MkdirAll(wwwDir, 0o755)

	wwwIndex := filepath.Join(wwwDir, "index.php")

	writeWelcome := false
	switch existing, err := os.ReadFile(wwwIndex); {
	case os.IsNotExist(err):
		writeWelcome = true
	case err == nil:
		if bytes.Contains(existing, []byte("@goampp-welcome")) {

			if !bytes.Contains(existing, []byte(welcomeMarker)) {
				writeWelcome = true
			}
		}
	}
	if writeWelcome {
		if err := os.WriteFile(wwwIndex, []byte(welcomeIndexPHP), 0o644); err == nil {
			log("  self-heal: wrote welcome page → " + wwwIndex)
		} else {
			log("  self-heal: failed to write index.php: " + err.Error())
		}
	}

	wwwPhpInfo := filepath.Join(wwwDir, "phpinfo.php")
	if _, err := os.Stat(wwwPhpInfo); os.IsNotExist(err) {
		if err := os.WriteFile(wwwPhpInfo, []byte(welcomePhpInfoPHP), 0o644); err == nil {
			log("  self-heal: wrote phpinfo shortcut → " + wwwPhpInfo)
		} else {
			log("  self-heal: failed to write phpinfo.php: " + err.Error())
		}
	}

	ensurePhpRuntimeDLLs(baseDir, log)

	ensureWelcomeAssets(baseDir, log)
}

func ensureWelcomeAssets(baseDir string, log func(string)) {
	wwwAssets := filepath.Join(baseDir, "www", "assets")
	wwwIcons := filepath.Join(wwwAssets, "icons")
	if err := os.MkdirAll(wwwIcons, 0o755); err != nil {
		return
	}

	if src := filepath.Join(baseDir, "logo.png"); fileExists(src) {
		_ = copyFileIfChanged(src, filepath.Join(wwwAssets, "goampp.png"))
	}

	srcIcons := filepath.Join(baseDir, "assets", "icons")
	entries, err := os.ReadDir(srcIcons)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		_ = copyFileIfChanged(
			filepath.Join(srcIcons, e.Name()),
			filepath.Join(wwwIcons, e.Name()),
		)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func ensurePhpRuntimeDLLs(baseDir string, log func(string)) {
	runtimeDir := filepath.Join(baseDir, "runtime")
	if _, err := os.Stat(runtimeDir); err != nil {
		return
	}
	dlls := []string{"vcruntime140.dll", "vcruntime140_1.dll", "msvcp140.dll"}

	binDir := filepath.Join(baseDir, "bin")
	entries, err := os.ReadDir(binDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name != "php" && !strings.HasPrefix(name, "php-") {
			continue
		}

		if strings.HasSuffix(name, "-legacy") {
			continue
		}
		phpDir := filepath.Join(binDir, name)
		if _, err := os.Stat(filepath.Join(phpDir, "php-cgi.exe")); err != nil {
			continue
		}
		for _, dll := range dlls {
			src := filepath.Join(runtimeDir, dll)
			dst := filepath.Join(phpDir, dll)
			if err := copyFileIfChanged(src, dst); err != nil {
				log("  self-heal: failed to mirror " + dll + " into " + name + ": " + err.Error())
			}
		}
	}
}

func copyFileIfChanged(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	if dstInfo, err := os.Stat(dst); err == nil {
		if dstInfo.Size() == srcInfo.Size() && dstInfo.ModTime().Equal(srcInfo.ModTime()) {
			return nil
		}
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
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}

	return os.Chtimes(dst, srcInfo.ModTime(), srcInfo.ModTime())
}

const welcomeMarker = "@goampp-welcome v6"

const welcomeIndexPHP = `<?php
// @goampp-welcome v6 — DO NOT edit this marker line; GoAMPP looks
// for it on launch to decide whether to ship a refreshed welcome
// page. Strip the marker (or replace it) to keep your edits safe.

$php_version    = phpversion();
$apache_version = $_SERVER['SERVER_SOFTWARE'] ?? 'Apache';
$server_name    = $_SERVER['SERVER_NAME'] ?? 'localhost';
$document_root  = $_SERVER['DOCUMENT_ROOT'] ?? '';
$now            = date('Y-m-d H:i:s');

$db_status = 'offline'; $db_version = '—';
if (function_exists('mysqli_connect')) {
    $conn = @mysqli_connect('127.0.0.1', 'root', '', '', 3306);
    if ($conn) {
        $db_status = 'online';
        $db_version = mysqli_get_server_info($conn);
        mysqli_close($conn);
    }
}

$redis_status = 'offline';
if (class_exists('Redis')) {
    try {
        $r = new Redis();
        if (@$r->connect('127.0.0.1', 6379, 0.5)) { $redis_status = 'online'; $r->close(); }
    } catch (\Throwable $e) {}
}

// Probe pgweb on :8081 so the "Modern PostgreSQL UI" tile can
// either open it (online) or hint that it needs to be started
// (offline) — same fsockopen pattern as the Postgres probe.
$pgweb_status = 'offline';
$conn = @fsockopen('127.0.0.1', 8081, $errno, $errstr, 0.3);
if ($conn) { $pgweb_status = 'online'; fclose($conn); }

// Postgres probe: a TCP connect to :5432 with a 0.5s timeout. We
// don't try to authenticate — just check the listener is up. The
// pg_isready binary would be more accurate but isn't always on PATH;
// fsockopen is good enough for an "is the server breathing?" pill.
$pg_status = 'offline';
$conn = @fsockopen('127.0.0.1', 5432, $errno, $errstr, 0.5);
if ($conn) { $pg_status = 'online'; fclose($conn); }

$projects = [];
if ($document_root && is_dir($document_root)) {
    foreach (scandir($document_root) as $e) {
        if ($e === '.' || $e === '..' || $e === 'phpmyadmin' || $e === 'adminer') continue;
        $full = $document_root . DIRECTORY_SEPARATOR . $e;
        if (is_dir($full) && (file_exists("$full/index.php") || file_exists("$full/index.html") || is_dir("$full/public"))) {
            $projects[] = $e;
        }
    }
    sort($projects);
}
?><!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>GoAMPP Dashboard</title>
<style>
:root {
    /* Light professional palette — XAMPP / Laragon vibe */
    --brand: #ef6c1a;
    --brand-dark: #c95612;
    --topbar: #1f2937;
    --topbar-2: #111827;
    --bg: #f4f5f7;
    --panel: #ffffff;
    --border: #e1e5ec;
    --border-2: #cdd3de;
    --text: #1f2937;
    --muted: #6b7280;
    --dim: #9ca3af;
    --success: #16a34a;
    --danger: #dc2626;
    --warn: #d97706;
    --link: #2563eb;
}
* { box-sizing: border-box; }
html, body { margin: 0; background: var(--bg); color: var(--text);
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, system-ui, sans-serif;
    font-size: 14px; line-height: 1.55; -webkit-font-smoothing: antialiased; }
a { color: var(--link); text-decoration: none; }
a:hover { text-decoration: underline; }

/* ---------------- Top bar ---------------- */
.topbar { background: linear-gradient(180deg, var(--topbar) 0%, var(--topbar-2) 100%);
    color: #fff; padding: 0 24px; height: 56px;
    display: flex; align-items: center; gap: 16px;
    border-bottom: 3px solid var(--brand); position: sticky; top: 0; z-index: 10;
    box-shadow: 0 1px 4px rgba(0,0,0,.08); }
.topbar .logo { display: flex; align-items: center; gap: 10px; font-weight: 700;
    font-size: 16px; letter-spacing: .2px; color: #fff; }
.topbar .logo:hover { text-decoration: none; }
.topbar .logo img { width: 28px; height: 28px; }
.topbar .logo .a { color: var(--brand); }
.topbar .stack-tag { color: #cbd5e1; font-size: 12px; padding-left: 16px;
    border-left: 1px solid #374151; margin-left: 4px; letter-spacing: .3px; }
.topbar .stack-tag b { color: #fff; font-weight: 600; }
.topbar .right { margin-left: auto; display: flex; gap: 12px; align-items: center;
    font-size: 12px; color: #cbd5e1; }
.topbar .right .pill { background: rgba(255,255,255,.08); border: 1px solid rgba(255,255,255,.12);
    padding: 4px 10px; border-radius: 999px; font-family: "SFMono-Regular", Consolas, monospace; }
.topbar .right a { color: #cbd5e1; }
.topbar .right a:hover { color: #fff; text-decoration: none; }

/* ---------------- Layout: sidebar + main ---------------- */
.layout { display: flex; min-height: calc(100vh - 56px); }
.sidebar { width: 220px; background: var(--panel); border-right: 1px solid var(--border);
    padding: 24px 0; flex-shrink: 0; }
.sidebar h4 { font-size: 11px; text-transform: uppercase; letter-spacing: 1px;
    color: var(--dim); margin: 0 0 8px; padding: 0 24px; font-weight: 700; }
.sidebar nav { display: flex; flex-direction: column; margin-bottom: 24px; }
.sidebar nav a { padding: 9px 24px; color: var(--text); font-size: 13.5px;
    border-left: 3px solid transparent; display: flex; align-items: center; gap: 10px; }
.sidebar nav a:hover { background: #f9fafb; text-decoration: none; }
.sidebar nav a.active { background: #fff7ee; color: var(--brand-dark);
    border-left-color: var(--brand); font-weight: 600; }
.sidebar nav a img { width: 16px; height: 16px; }

.main { flex: 1; padding: 32px 40px 60px; max-width: 1080px; }
.page-title { font-size: 22px; font-weight: 700; margin: 0 0 4px; color: var(--text); }
.page-sub { color: var(--muted); font-size: 13px; margin: 0 0 28px; }

/* ---------------- Panels (XAMPP-style framed sections) ---------------- */
.panel { background: var(--panel); border: 1px solid var(--border); border-radius: 6px;
    margin-bottom: 22px; overflow: hidden; }
.panel-head { padding: 12px 18px; border-bottom: 1px solid var(--border);
    background: #fafbfc; font-weight: 600; font-size: 13px;
    display: flex; align-items: center; justify-content: space-between; }
.panel-head .badge { font-size: 11px; padding: 2px 8px; border-radius: 999px;
    font-weight: 600; letter-spacing: .3px; }
.panel-head .badge.on  { background: #dcfce7; color: var(--success); }
.panel-head .badge.off { background: #fee2e2; color: var(--danger); }
.panel-body { padding: 16px 18px; }

/* ---------------- Stack table (phpinfo-like) ---------------- */
.stack-table { width: 100%; border-collapse: collapse; font-size: 13px; }
.stack-table th, .stack-table td { text-align: left; padding: 9px 14px;
    border-bottom: 1px solid var(--border); vertical-align: middle; }
.stack-table thead th { background: #f3f4f6; font-size: 11px; text-transform: uppercase;
    letter-spacing: .8px; color: var(--muted); font-weight: 600; }
.stack-table tbody tr:last-child td { border-bottom: none; }
.stack-table tbody tr:hover { background: #f9fafb; }
.stack-table .svc { display: flex; align-items: center; gap: 10px; font-weight: 600; }
.stack-table .svc img { width: 22px; height: 22px; }
.stack-table code { background: #f3f4f6; padding: 1px 6px; border-radius: 3px;
    font-family: "SFMono-Regular", Consolas, monospace; font-size: 12px; color: var(--text); }
.dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%;
    margin-right: 7px; vertical-align: middle; }
.dot.on  { background: var(--success); box-shadow: 0 0 0 3px #dcfce7; }
.dot.off { background: var(--danger);  box-shadow: 0 0 0 3px #fee2e2; }

/* ---------------- Tools grid ---------------- */
.tools { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 0; }
.tool { display: flex; align-items: center; gap: 14px; padding: 16px 18px;
    border-bottom: 1px solid var(--border); border-right: 1px solid var(--border);
    color: var(--text); }
.tool:hover { background: #fafbfc; text-decoration: none; }
.tool img { width: 32px; height: 32px; image-rendering: -webkit-optimize-contrast; }
.tool .meta { display: flex; flex-direction: column; min-width: 0; }
.tool .meta .t { font-weight: 600; font-size: 13.5px; color: var(--text); }
.tool .meta .d { font-size: 12px; color: var(--muted); margin-top: 1px; }

/* ---------------- Projects ---------------- */
.projects { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 10px; }
.project { display: block; padding: 13px 16px; background: #fafbfc;
    border: 1px solid var(--border); border-radius: 4px;
    color: var(--text); font-size: 13px; font-weight: 600; }
.project:hover { border-color: var(--brand); background: #fff7ee; text-decoration: none; }
.project small { display: block; color: var(--muted); font-weight: 400; font-size: 11px;
    margin-top: 3px; font-family: "SFMono-Regular", Consolas, monospace; }
.empty { color: var(--muted); font-size: 13px; font-style: italic;
    padding: 12px 0; margin: 0; }

/* ---------------- Quick links / docs ---------------- */
.docs { display: grid; grid-template-columns: repeat(auto-fill, minmax(240px, 1fr)); gap: 10px; }
.docs a { display: block; padding: 11px 14px; border: 1px solid var(--border);
    border-radius: 4px; background: #fff; color: var(--text); font-size: 13px; }
.docs a:hover { border-color: var(--brand); text-decoration: none; }
.docs a strong { display: block; font-weight: 600; margin-bottom: 2px; }
.docs a span { font-size: 12px; color: var(--muted); }

/* ---------------- Mini info table (server info) ---------------- */
.kv { width: 100%; border-collapse: collapse; font-size: 12.5px;
    font-family: "SFMono-Regular", Consolas, monospace; }
.kv th { text-align: left; color: var(--muted); font-weight: 500;
    padding: 6px 0; width: 38%; vertical-align: top; }
.kv td { padding: 6px 0; word-break: break-all; }

footer { padding: 16px 40px; border-top: 1px solid var(--border); background: #fafbfc;
    color: var(--muted); font-size: 12px; display: flex; justify-content: space-between;
    flex-wrap: wrap; gap: 8px; font-family: "SFMono-Regular", Consolas, monospace; }
footer a { color: var(--brand); }

@media (max-width: 760px) {
    .layout { flex-direction: column; }
    .sidebar { width: auto; padding: 12px 0;
        display: flex; gap: 0; overflow-x: auto;
        border-right: 0; border-bottom: 1px solid var(--border); }
    .sidebar h4 { display: none; }
    .sidebar nav { flex-direction: row; flex-wrap: nowrap; }
    .sidebar nav a { white-space: nowrap; border-left: 0; border-bottom: 3px solid transparent; }
    .sidebar nav a.active { border-left: 0; border-bottom-color: var(--brand); background: transparent; }
    .topbar .stack-tag { display: none; }
    .main { padding: 20px 18px 40px; }
}
</style>
</head>
<body>

<header class="topbar">
    <a href="/" class="logo">
        <img src="/assets/goampp.png" alt="" onerror="this.style.display='none'">
        Go<span class="a">A</span>MPP
    </a>
    <span class="stack-tag"><b>Apache</b> + <b>PHP</b> + <b>MariaDB</b> + <b>PostgreSQL</b> + <b>Redis</b></span>
    <div class="right">
        <span class="pill"><?= htmlspecialchars($server_name) ?>:<?= $_SERVER['SERVER_PORT'] ?? '80' ?></span>
        <a href="https://github.com/imtaqin/goampp" target="_blank" rel="noopener">GitHub</a>
    </div>
</header>

<div class="layout">
    <aside class="sidebar">
        <h4>Dashboard</h4>
        <nav>
            <a href="#welcome" class="active"><img src="/assets/goampp.png" alt="">Welcome</a>
            <a href="#stack"><img src="/assets/icons/apache.ico" alt="">Stack Status</a>
            <a href="#tools"><img src="/assets/icons/phpmyadmin.ico" alt="">Admin Tools</a>
            <a href="#projects"><img src="/assets/icons/laravel.ico" alt="">Projects</a>
            <a href="#serverinfo"><img src="/assets/icons/php.ico" alt="">Server Info</a>
            <a href="#docs"><img src="/assets/icons/composer.ico" alt="">Documentation</a>
        </nav>
        <h4>Open</h4>
        <nav>
            <a href="/phpmyadmin/" target="_blank"><img src="/assets/icons/phpmyadmin.ico" alt="">phpMyAdmin</a>
            <a href="/adminer/" target="_blank"><img src="/assets/icons/adminer.ico" alt="">Adminer</a>
            <a href="/phpinfo.php" target="_blank"><img src="/assets/icons/php.ico" alt="">phpinfo()</a>
        </nav>
    </aside>

    <main class="main">
        <h1 class="page-title" id="welcome">Welcome to GoAMPP</h1>
        <p class="page-sub">Your local development stack is up and running. Use the panels below to manage services and access tools.</p>

        <!-- Stack panel -->
        <section class="panel" id="stack">
            <div class="panel-head">
                Stack Status
                <span class="badge on">Apache · PHP · MariaDB online</span>
            </div>
            <table class="stack-table">
                <thead>
                    <tr><th>Service</th><th>Version</th><th>Endpoint</th><th>Status</th></tr>
                </thead>
                <tbody>
                    <tr>
                        <td><span class="svc"><img src="/assets/icons/apache.ico" alt="">Apache HTTP Server</span></td>
                        <td><?= htmlspecialchars(explode(' (', $apache_version)[0]) ?></td>
                        <td><code>localhost:<?= $_SERVER['SERVER_PORT'] ?? '80' ?></code></td>
                        <td><span class="dot on"></span>Running</td>
                    </tr>
                    <tr>
                        <td><span class="svc"><img src="/assets/icons/php.ico" alt="">PHP</span></td>
                        <td><?= htmlspecialchars($php_version) ?></td>
                        <td><code>mod_cgi · php-cgi.exe</code></td>
                        <td><span class="dot on"></span>Loaded</td>
                    </tr>
                    <tr>
                        <td><span class="svc"><img src="/assets/icons/mysql.ico" alt="">MariaDB</span></td>
                        <td><?= htmlspecialchars($db_version) ?></td>
                        <td><code>localhost:3306 · root</code></td>
                        <td><span class="dot <?= $db_status === 'online' ? 'on' : 'off' ?>"></span><?= $db_status === 'online' ? 'Running' : 'Stopped' ?></td>
                    </tr>
                    <tr>
                        <td><span class="svc"><img src="/assets/icons/postgresql.ico" alt="">PostgreSQL</span></td>
                        <td><?= $pg_status === 'online' ? '18.x' : '—' ?></td>
                        <td><code>localhost:5432 · postgres / postgres</code></td>
                        <td><span class="dot <?= $pg_status === 'online' ? 'on' : 'off' ?>"></span><?= $pg_status === 'online' ? 'Running' : 'Stopped' ?></td>
                    </tr>
                    <tr>
                        <td><span class="svc"><img src="/assets/icons/redis.ico" alt="">Redis</span></td>
                        <td><?= $redis_status === 'online' ? '5.0.x' : '—' ?></td>
                        <td><code>localhost:6379</code></td>
                        <td><span class="dot <?= $redis_status === 'online' ? 'on' : 'off' ?>"></span><?= $redis_status === 'online' ? 'Running' : 'Stopped' ?></td>
                    </tr>
                </tbody>
            </table>
        </section>

        <!-- Admin Tools panel -->
        <section class="panel" id="tools">
            <div class="panel-head">Admin Tools</div>
            <div class="tools">
                <a class="tool" href="/phpmyadmin/" target="_blank" rel="noopener">
                    <img src="/assets/icons/phpmyadmin.ico" alt="">
                    <div class="meta">
                        <span class="t">phpMyAdmin</span>
                        <span class="d">MariaDB / MySQL · root, no password</span>
                    </div>
                </a>
                <a class="tool" href="/adminer/" target="_blank" rel="noopener">
                    <img src="/assets/icons/adminer.ico" alt="">
                    <div class="meta">
                        <span class="t">Adminer</span>
                        <span class="d">Universal lightweight DB client</span>
                    </div>
                </a>
                <a class="tool" href="<?= $pgweb_status === 'online' ? 'http://localhost:8081/' : '/adminer/?pgsql=localhost&amp;username=postgres&amp;db=postgres' ?>" target="_blank" rel="noopener">
                    <img src="/assets/icons/postgresql.ico" alt="">
                    <div class="meta">
                        <span class="t">PostgreSQL Admin <?= $pgweb_status === 'online' ? '· pgweb' : '· Adminer' ?></span>
                        <span class="d"><?= $pgweb_status === 'online' ? 'Modern UI on :8081' : 'Start "pgweb" service for the modern UI' ?></span>
                    </div>
                </a>
                <a class="tool" href="/phpinfo.php" target="_blank" rel="noopener">
                    <img src="/assets/icons/php.ico" alt="">
                    <div class="meta">
                        <span class="t">phpinfo()</span>
                        <span class="d">PHP runtime configuration</span>
                    </div>
                </a>
            </div>
        </section>

        <!-- Projects panel -->
        <section class="panel" id="projects">
            <div class="panel-head">
                Your Projects
                <span class="badge <?= count($projects) > 0 ? 'on' : 'off' ?>"><?= count($projects) ?> total</span>
            </div>
            <div class="panel-body">
                <?php if (empty($projects)): ?>
                    <p class="empty">No projects yet. Open the GoAMPP control panel → <strong>Projects</strong> tab → pick a framework → Create.</p>
                <?php else: ?>
                    <div class="projects">
                        <?php foreach ($projects as $p): ?>
                            <a class="project" href="/<?= htmlspecialchars($p) ?>/">
                                <?= htmlspecialchars($p) ?>
                                <small>www/<?= htmlspecialchars($p) ?>/</small>
                            </a>
                        <?php endforeach; ?>
                    </div>
                <?php endif; ?>
            </div>
        </section>

        <!-- Server Information panel -->
        <section class="panel" id="serverinfo">
            <div class="panel-head">Server Information</div>
            <div class="panel-body">
                <table class="kv">
                    <tr><th>Server software</th><td><?= htmlspecialchars($apache_version) ?></td></tr>
                    <tr><th>Document root</th><td><?= htmlspecialchars($document_root) ?></td></tr>
                    <tr><th>Server name</th><td><?= htmlspecialchars($server_name) ?></td></tr>
                    <tr><th>PHP version</th><td><?= htmlspecialchars($php_version) ?> (NTS x64, classic CGI)</td></tr>
                    <tr><th>PHP SAPI</th><td><?= htmlspecialchars(php_sapi_name()) ?></td></tr>
                    <tr><th>Loaded extensions</th><td><?= count(get_loaded_extensions()) ?></td></tr>
                    <tr><th>Memory limit</th><td><?= htmlspecialchars(ini_get('memory_limit')) ?></td></tr>
                    <tr><th>Upload max</th><td><?= htmlspecialchars(ini_get('upload_max_filesize')) ?></td></tr>
                    <tr><th>Date / time</th><td><?= $now ?></td></tr>
                </table>
            </div>
        </section>

        <!-- Documentation panel -->
        <section class="panel" id="docs">
            <div class="panel-head">Documentation</div>
            <div class="panel-body">
                <div class="docs">
                    <a href="https://github.com/imtaqin/goampp" target="_blank" rel="noopener">
                        <strong>GoAMPP on GitHub</strong>
                        <span>Source · issues · releases</span>
                    </a>
                    <a href="https://www.php.net/docs.php" target="_blank" rel="noopener">
                        <strong>PHP Manual</strong>
                        <span>Reference for PHP <?= htmlspecialchars($php_version) ?></span>
                    </a>
                    <a href="https://httpd.apache.org/docs/2.4/" target="_blank" rel="noopener">
                        <strong>Apache HTTP Server 2.4</strong>
                        <span>Directives · modules · configuration</span>
                    </a>
                    <a href="https://mariadb.com/kb/en/" target="_blank" rel="noopener">
                        <strong>MariaDB Knowledge Base</strong>
                        <span>SQL · administration · tuning</span>
                    </a>
                    <a href="https://www.postgresql.org/docs/current/" target="_blank" rel="noopener">
                        <strong>PostgreSQL Documentation</strong>
                        <span>Current release manual</span>
                    </a>
                    <a href="https://redis.io/docs/" target="_blank" rel="noopener">
                        <strong>Redis Documentation</strong>
                        <span>Commands · clients · persistence</span>
                    </a>
                </div>
            </div>
        </section>
    </main>
</div>

<footer>
    <span>GoAMPP · serving from <?= htmlspecialchars($document_root) ?></span>
    <span>Generated <?= $now ?></span>
</footer>

</body>
</html>
`

const welcomePhpInfoPHP = `<?php
// phpinfo() dump — linked from the GoAMPP welcome page as a quick
// way to inspect the running PHP configuration. Delete this file
// if you don't want it exposed (it reveals loaded extensions, ini
// paths, and compile flags — low sensitivity on localhost but not
// something to ship to production).
phpinfo();
`
