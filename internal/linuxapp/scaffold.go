//go:build linux

package linuxapp

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type projectTemplate struct {
	Name        string
	Description string
	Files       map[string]string
}

var projectTemplates = []projectTemplate{
	{
		Name: "Static HTML",
		Description: "HTML, CSS, and JavaScript without a build step.",
		Files: map[string]string{
			"public/index.html": "<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n<title>{{name}}</title>\n<link rel=\"stylesheet\" href=\"/style.css\">\n</head>\n<body><main><p>GOAMPP PROJECT</p><h1>Your next idea starts here.</h1><p>Edit public/index.html and make this page yours.</p><button id=\"hello\">Say hello</button><p id=\"message\" aria-live=\"polite\"></p></main><script src=\"/script.js\"></script></body>\n</html>\n",
			"public/style.css": ":root { color-scheme: light dark; font-family: system-ui, sans-serif; }\n* { box-sizing: border-box; }\nbody { min-height: 100vh; margin: 0; display: grid; place-items: center; padding: 2rem; }\nmain { width: min(100%, 42rem); }\nh1 { font-size: clamp(2.5rem, 8vw, 5rem); line-height: 1; letter-spacing: -.06em; }\nbutton { border: 0; border-radius: .75rem; padding: .8rem 1rem; cursor: pointer; font: inherit; }\n",
			"public/script.js": "document.querySelector('#hello').addEventListener('click', () => {\n  document.querySelector('#message').textContent = 'It works. Now make it yours.';\n});\n",
			"serve.py": "from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer\nfrom functools import partial\nimport os\n\nhost = \"127.0.0.1\"\nport = int(os.environ.get(\"GOAMPP_PORT\", \"8000\"))\n\n\nclass SafeHandler(SimpleHTTPRequestHandler):\n    def send_head(self):\n        candidate = super().translate_path(self.path)\n        root = os.path.realpath(self.directory)\n        resolved = os.path.realpath(candidate)\n        try:\n            inside = os.path.commonpath((root, resolved)) == root\n        except ValueError:\n            inside = False\n        if not inside:\n            self.send_error(403, \"Forbidden\")\n            return None\n        return super().send_head()\n\n\npublic_path = \"public\"\nif os.path.islink(public_path) or not os.path.isdir(public_path):\n    raise RuntimeError(\"public must be a real directory\")\npublic_root = os.path.realpath(public_path)\nhandler = partial(SafeHandler, directory=public_root)\n\nif __name__ == \"__main__\":\n    print(f\"Serving on http://{host}:{port}\")\n    ThreadingHTTPServer((host, port), handler).serve_forever()\n",
			"README.md": "# Static HTML\n\nA tiny HTML, CSS, and JavaScript starter.\n\nRun with: python3 serve.py\nThen open http://127.0.0.1:8000. Requires Python 3.\n",
		},
	},
	{
		Name: "PHP",
		Description: "A PHP page for the built-in PHP development server.",
		Files: map[string]string{
			"public/index.php": "<?php\n$title = \"Hello from GoAMPP\";\n$message = \"Your PHP project is ready.\";\n?>\n<!doctype html>\n<html lang=\"en\">\n<head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width, initial-scale=1\"><title><?= htmlspecialchars($title, ENT_QUOTES, \"UTF-8\") ?></title></head>\n<body style=\"font:1.1rem system-ui,sans-serif;max-width:48rem;margin:12vh auto;padding:0 1.5rem;line-height:1.6\">\n<p>GOAMPP PROJECT</p><h1><?= htmlspecialchars($title, ENT_QUOTES, \"UTF-8\") ?></h1><p><?= htmlspecialchars($message, ENT_QUOTES, \"UTF-8\") ?></p>\n</body></html>\n",
			"router.php": "<?php\n$publicPath = __DIR__ . DIRECTORY_SEPARATOR . 'public';\nif (is_link($publicPath) || !is_dir($publicPath)) {\n    http_response_code(403);\n    echo 'Invalid public directory';\n    exit;\n}\n\n$root = realpath($publicPath);\nif ($root === false) {\n    http_response_code(403);\n    exit;\n}\n\n$requestPath = parse_url($_SERVER['REQUEST_URI'] ?? '/', PHP_URL_PATH);\nif (!is_string($requestPath)) {\n    http_response_code(400);\n    exit;\n}\n$decodedPath = rawurldecode($requestPath);\n$relativePath = ltrim(str_replace('\\\\', '/', $decodedPath), '/');\n$target = $root . DIRECTORY_SEPARATOR . str_replace('/', DIRECTORY_SEPARATOR, $relativePath);\n$resolved = realpath($target);\nif ($resolved === false) {\n    http_response_code(404);\n    echo 'Not found';\n    exit;\n}\n\nif (is_dir($resolved)) {\n    foreach (['index.php', 'index.html', 'index.htm'] as $indexName) {\n        $indexPath = realpath($resolved . DIRECTORY_SEPARATOR . $indexName);\n        if ($indexPath !== false) {\n            $resolved = $indexPath;\n            break;\n        }\n    }\n}\n\nif ($resolved !== false && $resolved !== $root) {\n    $rootPrefix = $root . DIRECTORY_SEPARATOR;\n    if (strncmp($resolved, $rootPrefix, strlen($rootPrefix)) !== 0) {\n        http_response_code(403);\n        echo 'Forbidden';\n        exit;\n    }\n}\nreturn false;\n",
			"README.md": "# PHP starter\n\nRun the built-in PHP server with: php -S 127.0.0.1:8001 -t public router.php\nThen open http://127.0.0.1:8001. Requires PHP CLI.\n",
		},
	},
	{
		Name: "Node.js HTTP",
		Description: "A web server using Node.js built-ins, without npm dependencies.",
		Files: map[string]string{
			"package.json": "{\n  \"name\": \"{{name}}\",\n  \"version\": \"1.0.0\",\n  \"private\": true,\n  \"scripts\": { \"start\": \"node server.js\" }\n}\n",
			"server.js": "const http = require('node:http');\nconst fs = require('node:fs');\nconst path = require('node:path');\nconst publicPath = path.resolve(__dirname, 'public');\nconst publicInfo = fs.lstatSync(publicPath);\nif (publicInfo.isSymbolicLink() || !publicInfo.isDirectory()) {\n  throw new Error('public must be a real directory');\n}\nconst root = fs.realpathSync(publicPath);\nconst port = Number(process.env.GOAMPP_PORT || 3000);\nhttp.createServer((req, res) => {\n  let pathname;\n  try { pathname = decodeURIComponent(new URL(req.url, 'http://127.0.0.1').pathname); }\n  catch { res.writeHead(400).end('Bad request'); return; }\n  const requested = pathname === '/' ? 'index.html' : pathname.replace(/^\\/+/, '');\n  const filename = path.resolve(root, requested);\n  if (!filename.startsWith(root + path.sep) && filename !== root) { res.writeHead(403).end('Forbidden'); return; }\n  fs.realpath(filename, (pathError, resolvedFilename) => {\n    if (pathError) { res.writeHead(404).end('Not found'); return; }\n    if (!resolvedFilename.startsWith(root + path.sep) && resolvedFilename !== root) { res.writeHead(403).end('Forbidden'); return; }\n    fs.readFile(resolvedFilename, (error, data) => {\n      if (error) { res.writeHead(404).end('Not found'); return; }\n      const ext = path.extname(resolvedFilename);\n      const mime = ext === '.css' ? 'text/css' : ext === '.js' ? 'text/javascript' : 'text/html';\n      res.writeHead(200, { 'Content-Type': mime + '; charset=utf-8' }).end(data);\n    });\n  });\n}).listen(port, '127.0.0.1', () => console.log('Listening on http://127.0.0.1:' + port));\n",
			"public/index.html": "<!doctype html>\n<html lang=\"en\"><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width, initial-scale=1\"><title>{{name}}</title></head>\n<body><main><p>GOAMPP PROJECT</p><h1>Node.js is running.</h1><p>Edit public/index.html to start building.</p></main></body></html>\n",
			"README.md": "# Node.js HTTP starter\n\nUses Node.js built-ins only. No npm install is needed.\nRun with: npm start\nThen open http://127.0.0.1:3000.\n",
		},
	},
	{
		Name: "Go net/http",
		Description: "A minimal Go web server that serves the public/ directory.",
		Files: map[string]string{
			"go.mod": "module example.local/{{name}}\n\ngo 1.27\n",
			"main.go": "package main\n\nimport (\n\t\"log\"\n\t\"net/http\"\n\t\"os\"\n\t\"path/filepath\"\n\t\"strings\"\n)\n\ntype confinedDir struct {\n\troot string\n}\n\nfunc (d confinedDir) Open(name string) (http.File, error) {\n\tcleaned := filepath.Clean(\"/\" + filepath.FromSlash(name))\n\trelative := strings.TrimLeft(cleaned, string(os.PathSeparator))\n\tcandidate := filepath.Join(d.root, relative)\n\tresolved, err := filepath.EvalSymlinks(candidate)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\trel, err := filepath.Rel(d.root, resolved)\n\tif err != nil || rel == \"..\" || strings.HasPrefix(rel, \"..\"+string(os.PathSeparator)) || filepath.IsAbs(rel) {\n\t\treturn nil, os.ErrPermission\n\t}\n\treturn os.Open(resolved)\n}\n\nfunc main() {\n\tport := os.Getenv(\"GOAMPP_PORT\")\n\tif port == \"\" {\n\t\tport = \"8080\"\n\t}\n\tpublicInfo, err := os.Lstat(\"public\")\n\tif err != nil {\n\t\tlog.Fatal(err)\n\t}\n\tif publicInfo.Mode()&os.ModeSymlink != 0 || !publicInfo.IsDir() {\n\t\tlog.Fatal(\"public must be a real directory\")\n\t}\n\troot, err := filepath.EvalSymlinks(\"public\")\n\tif err != nil {\n\t\tlog.Fatal(err)\n\t}\n\thttp.Handle(\"/\", http.FileServer(confinedDir{root: root}))\n\tlog.Printf(\"Listening on http://127.0.0.1:%s\", port)\n\tlog.Fatal(http.ListenAndServe(\"127.0.0.1:\"+port, nil))\n}\n",
			"public/index.html": "<!doctype html>\n<html lang=\"en\"><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width, initial-scale=1\"><title>{{name}}</title></head>\n<body><main><p>GOAMPP PROJECT</p><h1>Go net/http is running.</h1><p>Edit public/index.html to start building.</p></main></body></html>\n",
			"README.md": "# Go net/http starter\n\nRun the server with: go run .\nThen open http://127.0.0.1:8080. Requires Go.\n",
		},
	},
	{
		Name: "Python HTTP",
		Description: "A standard-library web server, without pip dependencies.",
		Files: map[string]string{
			"server.py": "from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer\nfrom functools import partial\nimport os\n\nhost = \"127.0.0.1\"\nport = int(os.environ.get(\"GOAMPP_PORT\", \"8002\"))\n\n\nclass SafeHandler(SimpleHTTPRequestHandler):\n    def send_head(self):\n        candidate = super().translate_path(self.path)\n        root = os.path.realpath(self.directory)\n        resolved = os.path.realpath(candidate)\n        try:\n            inside = os.path.commonpath((root, resolved)) == root\n        except ValueError:\n            inside = False\n        if not inside:\n            self.send_error(403, \"Forbidden\")\n            return None\n        return super().send_head()\n\n\npublic_path = \"public\"\nif os.path.islink(public_path) or not os.path.isdir(public_path):\n    raise RuntimeError(\"public must be a real directory\")\npublic_root = os.path.realpath(public_path)\nhandler = partial(SafeHandler, directory=public_root)\n\nif __name__ == \"__main__\":\n    print(f\"Serving on http://{host}:{port}\")\n    ThreadingHTTPServer((host, port), handler).serve_forever()\n",
			"public/index.html": "<!doctype html>\n<html lang=\"en\"><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width, initial-scale=1\"><title>{{name}}</title></head>\n<body><main><p>GOAMPP PROJECT</p><h1>Python is serving this page.</h1><p>Edit public/index.html to start building.</p></main></body></html>\n",
			"README.md": "# Python HTTP starter\n\nRun the standard-library server with: python3 server.py\nThen open http://127.0.0.1:8002. No third-party dependencies required.\n",
		},
	},
}

var projectNamePattern = regexp.MustCompile("^[a-zA-Z0-9][a-zA-Z0-9_-]{0,47}$")

func projectTemplateNames() []string {
	names := make([]string, 0, len(projectTemplates))
	for _, item := range projectTemplates {
		names = append(names, item.Name)
	}
	return names
}

func projectCreationOptions() []string {
	names := projectTemplateNames()
	names = append(names, linuxFrameworkNames()...)
	return names
}

func projectCreationDescription(name string) string {
	if item, ok := projectTemplateByName(name); ok {
		return item.Description
	}
	if recipe, ok := linuxFrameworkByName(name); ok {
		if len(recipe.Required) == 0 {
			return recipe.Description
		}
		return recipe.Description + " Requires: " + strings.Join(recipe.Required, ", ") + ". Project creation runs in the background and records output in Logs."
	}
	return "Choose a starter project."
}

func projectTemplateByName(name string) (projectTemplate, bool) {
	for _, item := range projectTemplates {
		if item.Name == name {
			return item, true
		}
	}
	return projectTemplate{}, false
}

func createProject(root, name, templateName string) (string, error) {
	return createProjectWithLog(root, name, templateName, nil)
}

func createProjectWithLog(root, name, templateName string, log func(string)) (string, error) {
	if recipe, ok := linuxFrameworkByName(templateName); ok {
		return createLinuxFrameworkProject(root, name, recipe, log)
	}
	return createBuiltinProject(root, name, templateName)
}

func createBuiltinProject(root, name, templateName string) (string, error) {
	name = strings.TrimSpace(name)
	if !projectNamePattern.MatchString(name) {
		return "", fmt.Errorf("use 1–48 letters, numbers, underscores, or hyphens; the first character must be a letter or number")
	}
	name = strings.ToLower(name)
	template, ok := projectTemplateByName(templateName)
	if !ok {
		return "", fmt.Errorf("unknown project template %q", templateName)
	}
	for rel := range template.Files {
		if !validTemplatePath(rel) {
			return "", fmt.Errorf("invalid template path %q", rel)
		}
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("create projects directory: %w", err)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	target := filepath.Join(rootAbs, name)
	if _, err := os.Lstat(target); err == nil {
		return "", fmt.Errorf("a project named %q already exists", name)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		return "", fmt.Errorf("create project directory: %w", err)
	}

	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(target)
		}
	}()
	for rel, body := range template.Files {
		body = strings.ReplaceAll(body, "{{name}}", name)
		out := filepath.Join(target, filepath.FromSlash(rel))
		relative, err := filepath.Rel(target, out)
		if err != nil || relative == "." || relative == ".." ||
			strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || filepath.IsAbs(relative) {
			return "", fmt.Errorf("invalid template path %q", rel)
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return "", fmt.Errorf("create %s: %w", rel, err)
		}
		if err := os.WriteFile(out, []byte(body), 0o644); err != nil {
			return "", fmt.Errorf("write %s: %w", rel, err)
		}
	}
	complete = true
	return target, nil
}

func validTemplatePath(rel string) bool {
	if rel == "" {
		return false
	}
	path := filepath.FromSlash(rel)
	if filepath.IsAbs(path) {
		return false
	}
	cleaned := filepath.Clean(path)
	return cleaned != "." && cleaned != ".." &&
		!strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) &&
		!filepath.IsAbs(cleaned)
}

func listProjects(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}
