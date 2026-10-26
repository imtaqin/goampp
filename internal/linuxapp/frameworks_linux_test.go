//go:build linux

package linuxapp

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLinuxFrameworkCatalogHasRunnableRecipes(t *testing.T) {
	expected := []string{
		"Laravel", "Laravel + Livewire", "Symfony", "CodeIgniter 4", "WordPress",
		"Next.js", "Vite + React", "Express", "NestJS", "AdonisJS",
		"Flask", "Django", "FastAPI", "Go HTTP server", "Gin (Go)", "Spring Boot",
	}
	got := linuxFrameworkNames()
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("framework order = %#v, want %#v", got, expected)
	}
	options := projectCreationOptions()
	for _, name := range expected {
		recipe, ok := linuxFrameworkByName(name)
		if !ok {
			t.Errorf("recipe %q missing from registry", name)
			continue
		}
		if recipe.Description == "" || (len(recipe.Steps) == 0 && recipe.DownloadURL == "") {
			t.Errorf("recipe %q is missing a description or generation step", name)
		}
		if !containsString(options, name) {
			t.Errorf("Projects template options do not include %q", name)
		}
		if projectCreationDescription(name) == "" {
			t.Errorf("recipe %q has no UI description", name)
		}
	}
}

func TestFrameworkProjectRunnerConfigurationsStayOnLoopback(t *testing.T) {
	tests := []struct {
		name     string
		files    []string
		portMode string
		command  string
		argsHas  []string
	}{
		{"Laravel", []string{"artisan"}, "framework:laravel", "php", []string{"serve", "--host", "127.0.0.1", "--port", "23456"}},
		{"Laravel + Livewire", []string{"artisan"}, "framework:livewire", "php", []string{"serve", "--host", "127.0.0.1", "--port", "23456"}},
		{"Symfony", []string{"public/index.php"}, "framework:symfony", "php", []string{"127.0.0.1:23456", "public/index.php"}},
		{"CodeIgniter 4", []string{"spark"}, "framework:codeigniter", "php", []string{"serve", "--host", "127.0.0.1", "--port", "23456"}},
		{"WordPress", []string{"index.php", ".goampp-router.php"}, "framework:wordpress", "php", []string{"127.0.0.1:23456", ".goampp-router.php"}},
		{"Next.js", []string{"package.json"}, "framework:nextjs", "npm", []string{"dev", "--hostname", "127.0.0.1", "--port", "23456"}},
		{"Vite + React", []string{"package.json"}, "framework:vite", "npm", []string{"dev", "--host", "127.0.0.1", "--port", "23456", "--strictPort"}},
		{"Express", []string{"package.json", "index.js"}, "framework:express", "node", []string{"index.js"}},
		{"NestJS", []string{"package.json"}, "framework:nestjs", "npm", []string{"start:dev"}},
		{"AdonisJS", []string{"package.json"}, "framework:adonis", "npm", []string{"dev"}},
		{"Flask", []string{"app.py"}, "framework:flask", filepath.Join("PROJECT", ".venv", "bin", "python"), []string{"app.py"}},
		{"Django", []string{"manage.py"}, "framework:django", filepath.Join("PROJECT", ".venv", "bin", "python"), []string{"manage.py", "runserver", "127.0.0.1:23456"}},
		{"FastAPI", []string{"main.py"}, "framework:fastapi", filepath.Join("PROJECT", ".venv", "bin", "python"), []string{"uvicorn", "main:app", "--host", "127.0.0.1", "--port", "23456"}},
		{"Go HTTP server", []string{"go.mod", "main.go"}, "framework:go", "go", []string{"run", "."}},
		{"Gin (Go)", []string{"go.mod", "main.go"}, "framework:go", "go", []string{"run", "."}},
		{"Spring Boot", []string{"mvnw", "pom.xml"}, "framework:springboot", "sh", []string{"mvnw", "spring-boot:run", "--server.address=127.0.0.1", "--server.port=23456"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, path := range test.files {
				full := filepath.Join(dir, filepath.FromSlash(path))
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte("placeholder\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(dir, ".goampp-framework"), []byte(test.name+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			spec, err := projectRunSpecForDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if spec.PortMode != test.portMode {
				t.Fatalf("port mode = %q, want %q", spec.PortMode, test.portMode)
			}
			wantCommand := strings.ReplaceAll(test.command, "PROJECT", dir)
			if spec.Command != wantCommand {
				t.Fatalf("command = %q, want %q", spec.Command, wantCommand)
			}
			spec = projectRunSpecAtPort(spec, 23456)
			if spec.URL != "http://127.0.0.1:23456" || spec.Port != 23456 {
				t.Fatalf("framework preview URL/port = %q/%d", spec.URL, spec.Port)
			}
			joinedArgs := strings.Join(spec.Args, " ")
			for _, arg := range test.argsHas {
				if !strings.Contains(joinedArgs, arg) {
					t.Errorf("runner arguments %#v do not contain expected substring %q", spec.Args, arg)
				}
			}
			if strings.Contains(strings.Join(spec.Args, " "), "0.0.0.0") {
				t.Errorf("runner must not bind to all interfaces: %#v", spec.Args)
			}
		})
	}
}

func TestFrameworkMarkerRejectsUnknownNamesInsteadOfFallingBack(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".goampp-framework"), []byte("run arbitrary shell\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := projectRunSpecForDir(dir); err == nil {
		t.Fatal("unknown framework marker should fail closed")
	}
}

func TestPinNestToLoopback(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "src", "main.ts")
	if err := os.MkdirAll(filepath.Dir(mainPath), 0o755); err != nil {
		t.Fatal(err)
	}
	source := "async function bootstrap() {\n  const app = await NestFactory.create(AppModule);\n  await app.listen(process.env.PORT ?? 3000);\n}\n"
	if err := os.WriteFile(mainPath, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := pinNestToLoopback(root); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "127.0.0.1") || !strings.Contains(string(got), "process.env.GOAMPP_PORT") {
		t.Fatalf("NestJS entry point is not loopback-pinned: %s", got)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
