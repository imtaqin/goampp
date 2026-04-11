//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// frameworks.go — the catalog of things GoAMPP can scaffold for the
// user, plus the runner that executes a chosen framework's install
// procedure. Each framework maps to a Project in config.json after
// a successful scaffold.
//
// Three "kinds" of scaffold:
//
//  - "composer"  — runs `php composer.phar create-project <pkg> .`
//                 inside a fresh project dir. Used for Laravel etc.
//  - "download"  — downloads + extracts a ZIP (WordPress and any
//                 other pre-packaged CMS). Reuses the existing
//                 extractZip() helper.
//  - "static"    — just drops a minimal index.html. No external tools.
//
// Future: "node" via npx, "python" via pip+venv, "go" via go mod init.

// Framework describes one scaffoldable project template.
type Framework struct {
	Name        string // human label, shown in the dropdown
	IconFile    string // matches assets/icons/<file>
	Runtime     string // "php" | "node" | "python" | "java" | "go" | "static"
	Description string // one-line hint under the picker

	// RequiredTools is the list of binaries/tools this framework
	// needs before it can scaffold. We check each via hasTool()
	// before running the command and bail if anything is missing.
	RequiredTools []string

	// Kind is one of "composer", "download", "static".
	Kind string

	// ComposerPackage — only used when Kind == "composer". Value is
	// the composer package name, e.g. "laravel/laravel".
	ComposerPackage string

	// DownloadURL — only used when Kind == "download". A ZIP file
	// we fetch and extract into the project directory.
	DownloadURL string
	StripTop    string // optional top-level folder inside the ZIP to strip

	// DocRoot is the subdirectory of the project the web server
	// should serve. "" means the project root. "public" for Laravel.
	DocRoot string
}

// Framework also supports custom command sets for Node/Python/Go/Java
// scaffolders. These are tail commands that run inside the project
// directory after creation, in order. Used by Kind="cmd" frameworks.
//
// CmdLines exist alongside ComposerPackage / DownloadURL because
// some scaffolders are shell-style multi-step (npm init -y → npm
// install express → write index.js) and don't fit a single command.
type cmdStep struct {
	desc string   // logged before run
	args []string // resolveTool() runs on args[0]
}

// We store the command list inside an extended Framework struct
// (cmdSteps) hidden from JSON. The base Framework type carries the
// metadata; cmdSteps drives the scaffolder.
//
// To keep diffs small we just attach a parallel map keyed by name.
var frameworkCmdSteps = map[string][]cmdStep{}

// proxyPort is the port a non-PHP framework's dev server listens on.
// When set, scaffoldFramework records a Project.Port and the vhost
// writer emits a ProxyPass instead of a DocumentRoot.
var frameworkProxyPort = map[string]int{}

// frameworkPostFile lets a scaffolder drop a starter file (index.js,
// app.py, etc.) into the project root after the command steps run.
// Path is relative to the project dir; content is written verbatim.
var frameworkPostFile = map[string]struct {
	path    string
	content string
}{}

// Frameworks is the catalog. Keys are the same as Framework.Name.
// Stored as an ordered slice so the UI picker has a predictable
// presentation instead of map iteration chaos.
//
// Grouped roughly: PHP first (since Apache+PHP is the only stack
// that works without spinning up a separate dev server), then
// Node, then Python, then Go, then Java, then "static" odds-and-
// ends.
var Frameworks = []*Framework{
	// ----- PHP frameworks (run under Apache+mod_cgi, port 80) -----
	{
		Name:            "Laravel",
		IconFile:        "laravel.ico",
		Runtime:         "php",
		Description:     "PHP web framework — composer create-project laravel/laravel",
		RequiredTools:   []string{"php", "composer"},
		Kind:            "composer",
		ComposerPackage: "laravel/laravel",
		DocRoot:         "public",
	},
	{
		Name:            "Laravel + Livewire",
		IconFile:        "livewire.ico",
		Runtime:         "php",
		Description:     "Laravel app with Livewire full-stack pre-wired",
		RequiredTools:   []string{"php", "composer"},
		Kind:            "composer",
		ComposerPackage: "laravel/laravel",
		DocRoot:         "public",
	},
	{
		Name:            "Symfony",
		IconFile:        "php.ico", // user doesn't have a Symfony icon
		Runtime:         "php",
		Description:     "PHP enterprise framework — symfony/skeleton via composer",
		RequiredTools:   []string{"php", "composer"},
		Kind:            "composer",
		ComposerPackage: "symfony/skeleton",
		DocRoot:         "public",
	},
	{
		Name:            "CodeIgniter 4",
		IconFile:        "php.ico",
		Runtime:         "php",
		Description:     "Lightweight PHP MVC — codeigniter4/appstarter",
		RequiredTools:   []string{"php", "composer"},
		Kind:            "composer",
		ComposerPackage: "codeigniter4/appstarter",
		DocRoot:         "public",
	},
	{
		Name:          "WordPress",
		IconFile:      "wordpress.ico",
		Runtime:       "php",
		Description:   "WordPress CMS — latest release from wordpress.org",
		RequiredTools: []string{"php"},
		Kind:          "download",
		DownloadURL:   "https://wordpress.org/latest.zip",
		StripTop:      "wordpress/",
		DocRoot:       "",
	},

	// ----- Node.js frameworks (each ships its own dev server) -----
	{
		Name:          "Next.js",
		IconFile:      "nodejs.ico",
		Runtime:       "node",
		Description:   "React framework — npx create-next-app",
		RequiredTools: []string{"node", "npm"},
		Kind:          "cmd",
		DocRoot:       ".next",
	},
	{
		Name:          "Vite + React",
		IconFile:      "nodejs.ico",
		Runtime:       "node",
		Description:   "Vite React starter — npm create vite@latest",
		RequiredTools: []string{"node", "npm"},
		Kind:          "cmd",
		DocRoot:       "dist",
	},
	{
		Name:          "Express",
		IconFile:      "nodejs.ico",
		Runtime:       "node",
		Description:   "Minimal Node.js web framework — npm + a hello-world server",
		RequiredTools: []string{"node", "npm"},
		Kind:          "cmd",
		DocRoot:       "",
	},
	{
		Name:          "NestJS",
		IconFile:      "nodejs.ico",
		Runtime:       "node",
		Description:   "Node.js TypeScript framework — npm i -g @nestjs/cli && nest new",
		RequiredTools: []string{"node", "npm"},
		Kind:          "cmd",
		DocRoot:       "dist",
	},
	{
		Name:          "AdonisJS",
		IconFile:      "adonisjs.ico",
		Runtime:       "node",
		Description:   "Node.js full-stack framework — npm init adonis-ts-app",
		RequiredTools: []string{"node", "npm"},
		Kind:          "cmd",
		DocRoot:       "build",
	},

	// ----- Python frameworks (each ships its own dev server) -----
	{
		Name:          "Flask",
		IconFile:      "python.ico",
		Runtime:       "python",
		Description:   "Python micro web framework — pip install flask + starter app.py",
		RequiredTools: []string{"python", "pip"},
		Kind:          "cmd",
		DocRoot:       "",
	},
	{
		Name:          "Django",
		IconFile:      "python.ico",
		Runtime:       "python",
		Description:   "Python full-stack framework — pip install django + django-admin startproject",
		RequiredTools: []string{"python", "pip"},
		Kind:          "cmd",
		DocRoot:       "",
	},
	{
		Name:          "FastAPI",
		IconFile:      "python.ico",
		Runtime:       "python",
		Description:   "Modern async Python API framework — pip install fastapi uvicorn",
		RequiredTools: []string{"python", "pip"},
		Kind:          "cmd",
		DocRoot:       "",
	},

	// ----- Go frameworks (each compiles to a single static binary) -----
	{
		Name:          "Go HTTP server",
		IconFile:      "go.ico",
		Runtime:       "go",
		Description:   "Standard library net/http hello-world",
		RequiredTools: []string{"go"},
		Kind:          "cmd",
		DocRoot:       "",
	},
	{
		Name:          "Gin (Go)",
		IconFile:      "go.ico",
		Runtime:       "go",
		Description:   "Go web framework — go mod + gin-gonic/gin",
		RequiredTools: []string{"go"},
		Kind:          "cmd",
		DocRoot:       "",
	},

	// ----- Java frameworks -----
	{
		Name:          "Spring Boot",
		IconFile:      "java.ico",
		Runtime:       "java",
		Description:   "Spring Boot starter — fetched from start.spring.io",
		RequiredTools: []string{"java"},
		Kind:          "download",
		// start.spring.io serves a generated zip from /starter.zip with
		// query params. Default deps: web + devtools, language Java.
		DownloadURL: "https://start.spring.io/starter.zip?type=maven-project&language=java&dependencies=web,devtools&packageName=com.example.demo&name=demo",
		StripTop:    "demo/",
		DocRoot:     "",
	},

	// ----- Static -----
	{
		Name:        "Static HTML",
		IconFile:    "",
		Runtime:     "static",
		Description: "Plain HTML5 boilerplate — no build step",
		Kind:        "static",
		DocRoot:     "",
	},
}

// init builds the cmdSteps map for the Kind="cmd" frameworks. This
// is in init() so the slice literal above stays scannable instead
// of having every npm/pip command embedded inline.
func init() {
	frameworkCmdSteps["Next.js"] = []cmdStep{
		{"create-next-app", []string{"npx", "--yes", "create-next-app@latest", ".",
			"--js", "--no-tailwind", "--no-src-dir", "--no-app", "--no-eslint",
			"--no-import-alias", "--use-npm"}},
	}
	frameworkProxyPort["Next.js"] = 3000

	frameworkCmdSteps["Vite + React"] = []cmdStep{
		{"create vite", []string{"npm", "create", "vite@latest", ".", "--",
			"--template", "react"}},
		{"npm install", []string{"npm", "install"}},
	}
	frameworkProxyPort["Vite + React"] = 5173

	frameworkCmdSteps["Express"] = []cmdStep{
		{"npm init", []string{"npm", "init", "-y"}},
		{"install express", []string{"npm", "install", "express"}},
	}
	frameworkPostFile["Express"] = struct {
		path    string
		content string
	}{
		path: "index.js",
		content: `const express = require('express');
const app = express();
const port = 3000;

app.get('/', (req, res) => {
  res.send('Hello from GoAMPP + Express!');
});

app.listen(port, () => {
  console.log(` + "`Express listening on http://localhost:${port}`" + `);
});
`,
	}
	frameworkProxyPort["Express"] = 3000

	frameworkCmdSteps["NestJS"] = []cmdStep{
		{"nest new", []string{"npx", "--yes", "@nestjs/cli", "new", ".",
			"--package-manager", "npm", "--skip-git"}},
	}
	frameworkProxyPort["NestJS"] = 3000

	frameworkCmdSteps["AdonisJS"] = []cmdStep{
		{"adonis create", []string{"npm", "init", "adonisjs@latest", ".", "--",
			"--kit=web", "--no-install"}},
		{"npm install", []string{"npm", "install"}},
	}
	frameworkProxyPort["AdonisJS"] = 3333

	// ----- Python -----
	frameworkCmdSteps["Flask"] = []cmdStep{
		{"pip install flask", []string{"pip", "install", "flask"}},
	}
	frameworkPostFile["Flask"] = struct {
		path    string
		content string
	}{
		path: "app.py",
		content: `from flask import Flask

app = Flask(__name__)

@app.route('/')
def hello():
    return 'Hello from GoAMPP + Flask!'

if __name__ == '__main__':
    app.run(host='127.0.0.1', port=5000)
`,
	}
	frameworkProxyPort["Flask"] = 5000

	frameworkCmdSteps["Django"] = []cmdStep{
		{"pip install django", []string{"pip", "install", "django"}},
		{"django-admin startproject", []string{"python", "-m", "django", "startproject", "site_app", "."}},
	}
	frameworkProxyPort["Django"] = 8000

	frameworkCmdSteps["FastAPI"] = []cmdStep{
		{"pip install fastapi", []string{"pip", "install", "fastapi", "uvicorn[standard]"}},
	}
	frameworkPostFile["FastAPI"] = struct {
		path    string
		content string
	}{
		path: "main.py",
		content: `from fastapi import FastAPI

app = FastAPI()

@app.get('/')
def root():
    return {'message': 'Hello from GoAMPP + FastAPI!'}

# Run with: uvicorn main:app --reload
`,
	}
	frameworkProxyPort["FastAPI"] = 8000

	// ----- Go -----
	frameworkCmdSteps["Go HTTP server"] = []cmdStep{
		{"go mod init", []string{"go", "mod", "init", "goampp.local/app"}},
	}
	frameworkPostFile["Go HTTP server"] = struct {
		path    string
		content string
	}{
		path: "main.go",
		content: `package main

import (
	"fmt"
	"log"
	"net/http"
)

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "Hello from GoAMPP + Go net/http!")
	})
	log.Println("listening on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
`,
	}
	frameworkProxyPort["Go HTTP server"] = 8080

	frameworkCmdSteps["Gin (Go)"] = []cmdStep{
		{"go mod init", []string{"go", "mod", "init", "goampp.local/gin-app"}},
		{"go get gin", []string{"go", "get", "github.com/gin-gonic/gin"}},
	}
	frameworkPostFile["Gin (Go)"] = struct {
		path    string
		content string
	}{
		path: "main.go",
		content: `package main

import "github.com/gin-gonic/gin"

func main() {
	r := gin.Default()
	r.GET("/", func(c *gin.Context) {
		c.JSON(200, gin.H{"message": "Hello from GoAMPP + Gin!"})
	})
	r.Run(":8080") // listen on http://localhost:8080
}
`,
	}
	frameworkProxyPort["Gin (Go)"] = 8080
}

// frameworkByName is a convenience lookup by Name.
func frameworkByName(name string) *Framework {
	for _, f := range Frameworks {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// ----- Tool detection ---------------------------------------------------

// hasTool reports whether a command-line tool is available, either
// in the system PATH or at a goampp-managed location (for tools we
// install ourselves, like composer.phar under bin/php/).
func hasTool(name string) bool {
	if _, err := exec.LookPath(name); err == nil {
		return true
	}
	if p := knownToolPath(name); p != "" {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// knownToolPath returns the goampp-local path for a tool, or empty
// if we don't track one. This lets hasTool + resolveTool find the
// runtimes we install via DownloadCatalog without depending on the
// user's system PATH.
//
// Layout matches what extractZip produces after StripTop:
//   bin/php/php.exe
//   bin/php/composer.phar     (auto-downloaded by ensureComposer)
//   bin/node/node.exe         (Node ZIP is flat after stripping)
//   bin/node/npm.cmd
//   bin/python/python.exe     (embeddable ZIP, no top folder)
//   bin/python/Scripts/pip.exe (bootstrapped by Python post-install)
//   bin/go/bin/go.exe         (Go ZIP keeps the bin/ subdir)
//   bin/java/bin/java.exe     (JDK ZIP keeps the bin/ subdir)
func knownToolPath(name string) string {
	if app == nil {
		return ""
	}
	join := func(parts ...string) string {
		return filepath.Join(append([]string{app.baseDir}, parts...)...)
	}
	switch name {
	case "php":
		return join("bin", "php", "php.exe")
	case "composer":
		return join("bin", "php", "composer.phar")
	case "node":
		return join("bin", "node", "node.exe")
	case "npm":
		return join("bin", "node", "npm.cmd")
	case "npx":
		return join("bin", "node", "npx.cmd")
	case "python", "python3":
		return join("bin", "python", "python.exe")
	case "pip", "pip3":
		return join("bin", "python", "Scripts", "pip.exe")
	case "go":
		return join("bin", "go", "bin", "go.exe")
	case "java":
		return join("bin", "java", "bin", "java.exe")
	case "javac":
		return join("bin", "java", "bin", "javac.exe")
	case "jar":
		return join("bin", "java", "bin", "jar.exe")
	}
	return ""
}

// resolveTool returns an absolute path for a known tool name when
// goampp manages it locally, or the bare name otherwise (so PATH
// lookup falls through to the OS). Used by runInDir so framework
// scaffolders can write `node` or `python` and get the bundled
// install instead of relying on the user's system PATH.
func resolveTool(name string) string {
	if p := knownToolPath(name); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return name
}

// ----- Composer auto-install --------------------------------------------

// composerURL is the canonical installer for composer.phar. It's a
// single PHP script — no installer to run, just save and invoke via
// `php composer.phar ...`.
const composerURL = "https://getcomposer.org/composer.phar"

// ensureComposer checks for composer.phar and downloads it on the
// first call. Cached to bin/php/composer.phar so subsequent runs
// are instant. Returns the absolute path to the .phar.
func ensureComposer(log func(string)) (string, error) {
	target := filepath.Join(app.baseDir, "bin", "php", "composer.phar")
	if _, err := os.Stat(target); err == nil {
		return target, nil
	}
	log("composer.phar not found — downloading from getcomposer.org")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	// httpDownload is already in download.go, so we inherit the
	// progress reporting + .part file atomicity.
	if err := httpDownload(composerURL, target, log, nil); err != nil {
		return "", fmt.Errorf("download composer: %w", err)
	}
	return target, nil
}

// runInDir launches a command with the given working directory,
// piping both stdout and stderr into the logger line-by-line so
// the user sees scaffold/composer output as it streams. Blocks
// until the command exits.
//
// argv[0] is resolved through resolveTool, so callers can write
// "node" / "python" / "composer" and get the goampp-bundled
// version instead of relying on the user's system PATH.
func runInDir(dir string, log func(string), argv ...string) error {
	if len(argv) == 0 {
		return fmt.Errorf("empty command")
	}
	argv[0] = resolveTool(argv[0])
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	// Pipe both streams into the log. We reuse streamToLog from
	// service.go which handles the line-buffered scanner with a
	// 1MB line budget for very long output.
	go streamToLog(stdout, func(format string, a ...any) {
		log(fmt.Sprintf(format, a...))
	})
	go streamToLog(stderr, func(format string, a ...any) {
		log(fmt.Sprintf(format, a...))
	})
	return cmd.Wait()
}

// ----- The scaffolder ---------------------------------------------------

// ScaffoldResult is what the UI shows after a successful create:
// the docroot path it should point Apache at, plus any warnings
// the scaffolder wants to surface.
type ScaffoldResult struct {
	DocRoot string // absolute
	Warning string // optional
}

// scaffoldFramework builds a new project at projectDir using the
// given framework template. Blocking — call from a goroutine.
// Logs everything through `log`.
func scaffoldFramework(f *Framework, projectDir string, log func(string)) (*ScaffoldResult, error) {
	// Abort early if the project dir already exists and isn't empty
	// — a scaffold would clobber whatever's in there.
	if fi, err := os.Stat(projectDir); err == nil && fi.IsDir() {
		entries, _ := os.ReadDir(projectDir)
		if len(entries) > 0 {
			return nil, fmt.Errorf("%s already exists and is not empty", projectDir)
		}
	}
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		return nil, fmt.Errorf("create project dir: %w", err)
	}

	// Check every required tool up front so we fail fast with a
	// clear message instead of bombing halfway through.
	for _, tool := range f.RequiredTools {
		if tool == "composer" {
			// Composer is special — we auto-install it.
			continue
		}
		if !hasTool(tool) {
			return nil, fmt.Errorf("missing %s: install it first (Settings → runtimes)", tool)
		}
	}

	switch f.Kind {
	case "composer":
		return scaffoldComposer(f, projectDir, log)
	case "download":
		return scaffoldDownload(f, projectDir, log)
	case "static":
		return scaffoldStatic(f, projectDir, log)
	case "cmd":
		return scaffoldCmd(f, projectDir, log)
	}
	return nil, fmt.Errorf("unknown framework kind: %s", f.Kind)
}

// scaffoldCmd runs the predefined command sequence stored in
// frameworkCmdSteps[f.Name], then drops a starter file from
// frameworkPostFile if one is registered. Used for Node, Python,
// and Go scaffolders that need multiple steps (npm init → install
// → write entry point) or aren't single composer-style commands.
func scaffoldCmd(f *Framework, projectDir string, log func(string)) (*ScaffoldResult, error) {
	steps, ok := frameworkCmdSteps[f.Name]
	if !ok || len(steps) == 0 {
		return nil, fmt.Errorf("no command steps registered for %s", f.Name)
	}
	for i, step := range steps {
		log(fmt.Sprintf("[%d/%d] %s — %s",
			i+1, len(steps), step.desc, strings.Join(step.args, " ")))
		if err := runInDir(projectDir, log, step.args...); err != nil {
			return nil, fmt.Errorf("%s: %w", step.desc, err)
		}
	}
	// Drop the starter file (entry point, hello-world handler, etc.)
	// if the framework has one. Skipped silently when none registered.
	if pf, has := frameworkPostFile[f.Name]; has {
		full := filepath.Join(projectDir, pf.path)
		if err := os.WriteFile(full, []byte(pf.content), 0o644); err != nil {
			return nil, fmt.Errorf("write starter file: %w", err)
		}
		log("  wrote " + pf.path)
	}
	docroot := projectDir
	if f.DocRoot != "" {
		docroot = filepath.Join(projectDir, f.DocRoot)
	}
	return &ScaffoldResult{DocRoot: docroot}, nil
}

// scaffoldComposer runs `php composer.phar create-project <pkg> .`
// inside projectDir. Auto-downloads composer.phar if missing.
func scaffoldComposer(f *Framework, projectDir string, log func(string)) (*ScaffoldResult, error) {
	composer, err := ensureComposer(log)
	if err != nil {
		return nil, err
	}
	phpExe := knownToolPath("php")
	if _, err := os.Stat(phpExe); err != nil {
		return nil, fmt.Errorf("php.exe not found at %s — install the PHP service first", phpExe)
	}

	log(fmt.Sprintf("scaffolding %s into %s (this may take a few minutes)...", f.Name, projectDir))
	args := []string{phpExe, composer, "create-project", "--no-interaction", f.ComposerPackage, "."}
	if err := runInDir(projectDir, log, args...); err != nil {
		return nil, fmt.Errorf("composer create-project: %w", err)
	}

	// Special case: Laravel + Livewire adds Livewire via composer require.
	if f.Name == "Laravel + Livewire" {
		log("adding Livewire via composer require...")
		if err := runInDir(projectDir, log,
			phpExe, composer, "require", "livewire/livewire", "--no-interaction"); err != nil {
			// Non-fatal — project still works, user can add it manually.
			log("livewire install failed (non-fatal): " + err.Error())
		}
	}

	docroot := filepath.Join(projectDir, f.DocRoot)
	return &ScaffoldResult{DocRoot: docroot}, nil
}

// scaffoldDownload fetches a ZIP and extracts it into projectDir
// with optional top-level directory stripping.
func scaffoldDownload(f *Framework, projectDir string, log func(string)) (*ScaffoldResult, error) {
	// Stash the ZIP in downloads/ so a re-create doesn't re-fetch.
	dlDir := filepath.Join(app.baseDir, "downloads")
	if err := os.MkdirAll(dlDir, 0o755); err != nil {
		return nil, err
	}
	dlPath := filepath.Join(dlDir, safeFileName(f.Name)+".zip")

	if _, err := os.Stat(dlPath); err != nil {
		log(fmt.Sprintf("downloading %s from %s", f.Name, f.DownloadURL))
		if err := httpDownload(f.DownloadURL, dlPath, log, nil); err != nil {
			return nil, fmt.Errorf("download: %w", err)
		}
	} else {
		log("using cached " + filepath.Base(dlPath))
	}

	log(fmt.Sprintf("extracting into %s", projectDir))
	if err := extractZip(dlPath, projectDir, f.StripTop, nil); err != nil {
		return nil, fmt.Errorf("extract: %w", err)
	}

	docroot := filepath.Join(projectDir, f.DocRoot)
	return &ScaffoldResult{DocRoot: docroot}, nil
}

// scaffoldStatic drops a minimal index.html so the vhost has
// something to serve from day one.
func scaffoldStatic(f *Framework, projectDir string, log func(string)) (*ScaffoldResult, error) {
	html := `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>` + filepath.Base(projectDir) + `</title>
  <style>
    body { font-family: system-ui, sans-serif; max-width: 40rem; margin: 4rem auto; padding: 0 1rem; }
    h1   { color: #3a7aef; }
    code { background: #f0f2f5; padding: .15rem .4rem; border-radius: .25rem; }
  </style>
</head>
<body>
  <h1>It works! 🎉</h1>
  <p>This site is served by GoAMPP from <code>` + projectDir + `</code>.</p>
  <p>Edit <code>index.html</code> to get started.</p>
</body>
</html>
`
	if err := os.WriteFile(filepath.Join(projectDir, "index.html"), []byte(html), 0o644); err != nil {
		return nil, err
	}
	log("created index.html boilerplate")
	return &ScaffoldResult{DocRoot: projectDir}, nil
}

// ----- Post-scaffold wiring ---------------------------------------------

// createProject is the single high-level entry point the UI calls
// when the user clicks "Create Project". Runs scaffoldFramework,
// then registers the project as a Vhost, persists config, and
// triggers ApplyVhosts so the hosts file + Apache vhost include
// pick up the new domain without a manual edit.
//
// Blocking — callers should wrap in a goroutine.
func createProject(f *Framework, name, domain string, log func(string)) error {
	if name == "" {
		return fmt.Errorf("project name required")
	}
	if domain == "" {
		domain = name + ".test"
	}
	projectDir := filepath.Join(app.baseDir, "www", name)

	result, err := scaffoldFramework(f, projectDir, log)
	if err != nil {
		return err
	}

	// Record in config.json as both a Project (for the UI list)
	// and a Vhost (so ApplyVhosts writes it out).
	docrootRel := "{base}/" + strings.ReplaceAll(
		strings.TrimPrefix(result.DocRoot, app.baseDir+string(os.PathSeparator)),
		`\`, `/`)

	// Frameworks that ship their own dev server (Node, Python, Go,
	// Java) get a "proxy" vhost — Apache forwards requests to the
	// dev server's port instead of serving files. The user has to
	// run the framework's dev command (`npm run dev`, `flask run`,
	// etc.) themselves; we just route the traffic.
	proxyPort := frameworkProxyPort[f.Name]

	app.cfg.Projects = append(app.cfg.Projects, Project{
		Name:      name,
		Framework: f.Name,
		Domain:    domain,
		DocRoot:   result.DocRoot,
		Port:      proxyPort,
	})
	vh := Vhost{
		Domain:     domain,
		DocRoot:    docrootRel,
		Port:       80,
		ServerType: "apache",
		Enabled:    true,
	}
	if proxyPort > 0 {
		// vhost.go uses ProxyPort to decide between DocumentRoot
		// and ProxyPass when emitting the Apache vhost block.
		vh.ProxyPort = proxyPort
	}
	app.cfg.Vhosts = append(app.cfg.Vhosts, vh)
	if err := SaveConfig(app.baseDir, app.cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	// Apply so the hosts file + Apache vhosts.conf both learn the
	// new domain. May fail if we're not admin — the error bubbles
	// up with a clear hint.
	if err := ApplyVhosts(app.baseDir, app.cfg); err != nil {
		log("apply vhosts: " + err.Error())
		log("  → run GoAMPP as administrator for hosts-file writes to work")
		return fmt.Errorf("apply vhosts: %w", err)
	}

	log(fmt.Sprintf("project '%s' created at %s → http://%s", name, result.DocRoot, domain))
	log("NOTE: Apache needs a restart to pick up the new vhost — click 'Restart Stack'")
	return nil
}
