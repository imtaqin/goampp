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

type Framework struct {
	Name        string
	IconFile    string
	Runtime     string
	Description string

	RequiredTools []string

	Kind string

	ComposerPackage string

	DownloadURL string
	StripTop    string

	DocRoot string
}

type cmdStep struct {
	desc string
	args []string
}

var frameworkCmdSteps = map[string][]cmdStep{}

var frameworkProxyPort = map[string]int{}

var frameworkPostFile = map[string]struct {
	path    string
	content string
}{}

var Frameworks = []*Framework{

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
		IconFile:        "php.ico",
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

	{
		Name:          "Spring Boot",
		IconFile:      "java.ico",
		Runtime:       "java",
		Description:   "Spring Boot starter — fetched from start.spring.io",
		RequiredTools: []string{"java"},
		Kind:          "download",

		DownloadURL: "https://start.spring.io/starter.zip?type=maven-project&language=java&dependencies=web,devtools&packageName=com.example.demo&name=demo",
		StripTop:    "demo/",
		DocRoot:     "",
	},

	{
		Name:        "Static HTML",
		IconFile:    "",
		Runtime:     "static",
		Description: "Plain HTML5 boilerplate — no build step",
		Kind:        "static",
		DocRoot:     "",
	},
}

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

func frameworkByName(name string) *Framework {
	for _, f := range Frameworks {
		if f.Name == name {
			return f
		}
	}
	return nil
}

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

func resolveTool(name string) string {
	if p := knownToolPath(name); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return name
}

const composerURL = "https://getcomposer.org/composer.phar"

func ensureComposer(log func(string)) (string, error) {
	target := filepath.Join(app.baseDir, "bin", "php", "composer.phar")
	if _, err := os.Stat(target); err == nil {
		return target, nil
	}
	log("composer.phar not found — downloading from getcomposer.org")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}

	if err := httpDownload(composerURL, target, log, nil); err != nil {
		return "", fmt.Errorf("download composer: %w", err)
	}
	return target, nil
}

func runInDir(dir string, log func(string), argv ...string) error {
	if len(argv) == 0 {
		return fmt.Errorf("empty command")
	}
	argv[0] = resolveTool(argv[0])
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000,
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

	go streamToLog(stdout, func(format string, a ...any) {
		log(fmt.Sprintf(format, a...))
	})
	go streamToLog(stderr, func(format string, a ...any) {
		log(fmt.Sprintf(format, a...))
	})
	return cmd.Wait()
}

type ScaffoldResult struct {
	DocRoot string
	Warning string
}

func scaffoldFramework(f *Framework, projectDir string, log func(string)) (*ScaffoldResult, error) {

	if fi, err := os.Stat(projectDir); err == nil && fi.IsDir() {
		entries, _ := os.ReadDir(projectDir)
		if len(entries) > 0 {
			return nil, fmt.Errorf("%s already exists and is not empty", projectDir)
		}
	}
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		return nil, fmt.Errorf("create project dir: %w", err)
	}

	for _, tool := range f.RequiredTools {
		if tool == "composer" {

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

	if f.Name == "Laravel + Livewire" {
		log("adding Livewire via composer require...")
		if err := runInDir(projectDir, log,
			phpExe, composer, "require", "livewire/livewire", "--no-interaction"); err != nil {

			log("livewire install failed (non-fatal): " + err.Error())
		}
	}

	docroot := filepath.Join(projectDir, f.DocRoot)
	return &ScaffoldResult{DocRoot: docroot}, nil
}

func scaffoldDownload(f *Framework, projectDir string, log func(string)) (*ScaffoldResult, error) {

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

	docrootRel := "{base}/" + strings.ReplaceAll(
		strings.TrimPrefix(result.DocRoot, app.baseDir+string(os.PathSeparator)),
		`\`, `/`)

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

		vh.ProxyPort = proxyPort
	}
	app.cfg.Vhosts = append(app.cfg.Vhosts, vh)
	if err := SaveConfig(app.baseDir, app.cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	if err := ApplyVhosts(app.baseDir, app.cfg); err != nil {
		log("apply vhosts: " + err.Error())
		log("  → run GoAMPP as administrator for hosts-file writes to work")
		return fmt.Errorf("apply vhosts: %w", err)
	}

	log(fmt.Sprintf("project '%s' created at %s → http://%s", name, result.DocRoot, domain))
	log("NOTE: Apache needs a restart to pick up the new vhost — click 'Restart Stack'")
	return nil
}
