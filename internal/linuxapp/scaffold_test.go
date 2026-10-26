//go:build linux

package linuxapp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectTemplatesCreateExpectedFiles(t *testing.T) {
	tests := []struct {
		template string
		required []string
	}{
		{"Static HTML", []string{"public/index.html", "public/style.css", "public/script.js", "serve.py", "README.md"}},
		{"PHP", []string{"public/index.php", "router.php", "README.md"}},
		{"Node.js HTTP", []string{"package.json", "server.js", "public/index.html", "README.md"}},
		{"Go net/http", []string{"go.mod", "main.go", "public/index.html", "README.md"}},
		{"Python HTTP", []string{"server.py", "public/index.html", "README.md"}},
	}

	for _, test := range tests {
		t.Run(test.template, func(t *testing.T) {
			root := t.TempDir()
			projectPath, err := createProject(root, "MyProject", test.template)
			if err != nil {
				t.Fatalf("createProject returned error: %v", err)
			}
			if filepath.Base(projectPath) != "myproject" {
				t.Fatalf("project name was not normalized: %s", projectPath)
			}
			for _, rel := range test.required {
				if _, err := os.Stat(filepath.Join(projectPath, filepath.FromSlash(rel))); err != nil {
					t.Errorf("required file %q missing: %v", rel, err)
				}
			}
		})
	}
}

func TestNodeTemplateRoutesRootAssetsOnly(t *testing.T) {
	template, ok := projectTemplateByName("Node.js HTTP")
	if !ok {
		t.Fatal("Node.js HTTP template is missing")
	}
	server := template.Files["server.js"]
	if !strings.Contains(server, "pathname.replace(/^\\/+/, '')") {
		t.Fatal("server template must strip leading slashes from URL paths")
	}
	if !strings.Contains(server, "filename.startsWith(root + path.sep)") {
		t.Fatal("server template must reject lexical paths that escape the public directory")
	}
	if !strings.Contains(server, "fs.lstatSync(publicPath)") ||
		!strings.Contains(server, "publicInfo.isSymbolicLink()") ||
		!strings.Contains(server, "fs.realpathSync(publicPath)") ||
		!strings.Contains(server, "fs.realpath(filename") ||
		!strings.Contains(server, "resolvedFilename.startsWith(root + path.sep)") {
		t.Fatal("server template must reject symlinks that escape the public directory")
	}
	if !strings.Contains(server, "listen(port, '127.0.0.1'") {
		t.Fatal("server must bind to loopback, not every network interface")
	}
}

func TestGoAndPythonStartersConfineSymlinksToPublic(t *testing.T) {
	goTemplate, ok := projectTemplateByName("Go net/http")
	if !ok {
		t.Fatal("Go net/http template is missing")
	}
	for _, required := range []string{"type confinedDir struct", "filepath.EvalSymlinks(candidate)", "filepath.Rel(d.root, resolved)", "os.ErrPermission", "os.Lstat(\"public\")", "publicInfo.Mode()&os.ModeSymlink"} {
		if !strings.Contains(goTemplate.Files["main.go"], required) {
			t.Errorf("Go starter missing document-root confinement check %q", required)
		}
	}

	pythonTemplate, ok := projectTemplateByName("Python HTTP")
	if !ok {
		t.Fatal("Python HTTP template is missing")
	}
	for _, required := range []string{"class SafeHandler", "os.path.realpath(candidate)", "os.path.commonpath", "self.send_error(403, \"Forbidden\")", "os.path.islink(public_path)", "public must be a real directory"} {
		if !strings.Contains(pythonTemplate.Files["server.py"], required) {
			t.Errorf("Python starter missing document-root confinement check %q", required)
		}
	}

	staticTemplate, ok := projectTemplateByName("Static HTML")
	if !ok {
		t.Fatal("Static HTML template is missing")
	}
	for _, required := range []string{"class SafeHandler", "os.path.realpath(candidate)", "os.path.commonpath", "self.send_error(403, \"Forbidden\")", "os.path.islink(public_path)", "public must be a real directory"} {
		if !strings.Contains(staticTemplate.Files["serve.py"], required) {
			t.Errorf("Static HTML starter missing document-root confinement check %q", required)
		}
	}
}

func TestStaticAndPHPStartersKeepProjectMetadataOutsideDocumentRoot(t *testing.T) {
	for _, test := range []struct {
		template string
		rootFile string
		publicFile string
	}{
		{"Static HTML", "index.html", "public/index.html"},
		{"PHP", "index.php", "public/index.php"},
	} {
		t.Run(test.template, func(t *testing.T) {
			projectPath, err := createProject(t.TempDir(), "safe-root", test.template)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(projectPath, test.rootFile)); !os.IsNotExist(err) {
				t.Fatalf("web entry point unexpectedly exists at project root: %v", err)
			}
			if _, err := os.Stat(filepath.Join(projectPath, filepath.FromSlash(test.publicFile))); err != nil {
				t.Fatalf("web entry point missing from public document root: %v", err)
			}
		})
	}
}

func TestPHPTemplateConfinesRequestsToPublic(t *testing.T) {
	template, ok := projectTemplateByName("PHP")
	if !ok {
		t.Fatal("PHP template is missing")
	}
	router := template.Files["router.php"]
	for _, required := range []string{
		"is_link($publicPath)",
		"rawurldecode($requestPath)",
		"realpath($target)",
		"strncmp($resolved, $rootPrefix",
		"http_response_code(403)",
	} {
		if !strings.Contains(router, required) {
			t.Errorf("PHP router is missing document-root protection %q", required)
		}
	}
}

func TestCreateProjectRejectsUnsafeNames(t *testing.T) {
	root := t.TempDir()
	names := []string{"", ".", "..", "../outside", "/tmp/project", "bad/name", "has space", "-starts-with-dash"}
	for _, name := range names {
		if path, err := createProject(root, name, "Static HTML"); err == nil {
			t.Errorf("createProject(%q) unexpectedly succeeded at %s", name, path)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "outside")); err == nil {
		t.Fatal("unsafe project name wrote outside the project root")
	}
}

func TestCreateProjectRejectsTemplatePathTraversal(t *testing.T) {
	original := projectTemplates
	projectTemplates = append(append([]projectTemplate(nil), original...), projectTemplate{
		Name: "Unsafe test template",
		Files: map[string]string{
			"../escape.txt": "must not be written",
		},
	})
	defer func() {
		projectTemplates = original
	}()

	root := t.TempDir()
	if _, err := createProject(root, "safe-project", "Unsafe test template"); err == nil {
		t.Fatal("expected a template path outside the project root to be rejected")
	}
	if _, err := os.Stat(filepath.Join(root, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("template traversal wrote outside the project root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "safe-project")); !os.IsNotExist(err) {
		t.Fatal("invalid template unexpectedly left a project directory behind")
	}
}

func TestValidTemplatePath(t *testing.T) {
	for _, path := range []string{"index.html", "public/index.html", "assets/css/site.css"} {
		if !validTemplatePath(path) {
			t.Errorf("valid template path %q was rejected", path)
		}
	}
	for _, path := range []string{"", ".", "..", "../secret", "public/../../secret", "/tmp/secret"} {
		if validTemplatePath(path) {
			t.Errorf("unsafe template path %q was accepted", path)
		}
	}
}

func TestCreateProjectNeverOverwritesExistingDirectory(t *testing.T) {
	root := t.TempDir()
	projectPath, err := createProject(root, "sample", "Static HTML")
	if err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(projectPath, "index.html")
	if err := os.WriteFile(index, []byte("user content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := createProject(root, "sample", "PHP"); err == nil {
		t.Fatal("expected an existing project directory to be rejected")
	}
	data, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "user content" {
		t.Fatalf("existing user file was changed: %q", data)
	}
}

func TestCreateProjectRejectsUnknownTemplate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-created")
	if _, err := createProject(root, "sample", "Unknown"); err == nil {
		t.Fatal("expected unknown template to fail")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("unknown template unexpectedly created root: %v", err)
	}
}

func TestListProjectsSortsAndSkipsHiddenDirectories(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"zeta", "alpha", ".internal"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := listProjects(root)
	if len(got) != 2 || got[0] != "alpha" || got[1] != "zeta" {
		t.Fatalf("unexpected project list: %#v", got)
	}
}


func TestNetworkStarterTemplatesHonorAssignedPort(t *testing.T) {
	tests := []struct {
		template string
		file     string
		needle   string
	}{
		{"Node.js HTTP", "server.js", "process.env.GOAMPP_PORT"},
		{"Static HTML", "serve.py", "GOAMPP_PORT"},
		{"Go net/http", "main.go", "os.Getenv(\"GOAMPP_PORT\")"},
		{"Python HTTP", "server.py", "os.environ.get(\"GOAMPP_PORT\""},
	}
	for _, test := range tests {
		t.Run(test.template, func(t *testing.T) {
			template, ok := projectTemplateByName(test.template)
			if !ok {
				t.Fatalf("template %q is missing", test.template)
			}
			if !strings.Contains(template.Files[test.file], test.needle) {
				t.Fatalf("%s must read GOAMPP_PORT so GoAMPP can avoid port conflicts", test.file)
			}
		})
	}
}
