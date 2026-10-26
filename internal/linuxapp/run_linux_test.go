//go:build linux

package linuxapp

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestProjectRunSpecsMatchStarterTemplates(t *testing.T) {
	tests := []struct {
		template string
		command  string
		python   bool
		url      string
	}{
		{"Static HTML", "", true, "http://127.0.0.1:8000"},
		{"PHP", "php", false, "http://127.0.0.1:8001"},
		{"Node.js HTTP", "npm", false, "http://127.0.0.1:3000"},
		{"Go net/http", "go", false, "http://127.0.0.1:8080"},
		{"Python HTTP", "", true, "http://127.0.0.1:8002"},
	}
	for _, test := range tests {
		t.Run(test.template, func(t *testing.T) {
			root := t.TempDir()
			projectDir, err := createProject(root, "starter", test.template)
			if err != nil {
				t.Fatal(err)
			}
			spec, err := projectRunSpecForDir(projectDir)
			if err != nil {
				t.Fatal(err)
			}
			if test.python {
				if spec.Command != "python3" && spec.Command != "python" {
					t.Fatalf("unexpected Python runner command: %q", spec.Command)
				}
			} else if spec.Command != test.command {
				t.Fatalf("unexpected run command: got %q, want %q", spec.Command, test.command)
			}
			if spec.URL != test.url {
				t.Fatalf("unexpected run URL: got %q, want %q", spec.URL, test.url)
			}
			if len(spec.Args) == 0 {
				t.Fatal("run spec has no arguments")
			}
			readme, err := os.ReadFile(filepath.Join(projectDir, "README.md"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(readme), spec.URL) {
				t.Fatalf("README does not mention the Run action URL %s", spec.URL)
			}
			if test.template == "Python HTTP" {
				server, err := os.ReadFile(filepath.Join(projectDir, "server.py"))
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(server), `GOAMPP_PORT", "8002"`) {
					t.Fatal("Python starter port does not match the Run action")
				}
			}
		})
	}
}

func TestResolveProjectDirRejectsSymlinkOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "index.html"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "outside-project")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks are not available in this environment: %v", err)
	}
	if _, err := resolveProjectDir(root, "outside-project"); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("expected symlink escape to be rejected, got %v", err)
	}
}

func TestResolveProjectDirRejectsUnsafeName(t *testing.T) {
	if _, err := resolveProjectDir(t.TempDir(), "../outside"); err == nil {
		t.Fatal("unsafe project name was accepted")
	}
}

func TestWaitForProjectEndpointRequiresManagedProcess(t *testing.T) {
	root := t.TempDir()
	projectDir, err := createProject(root, "starter", "Static HTML")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	if err := waitForProjectEndpoint(root, "starter", "http://"+listener.Addr().String(), time.Second); err == nil {
		t.Fatal("an unrelated listening port must not count as a running GoAMPP project")
	}

	resolved, err := filepath.EvalSymlinks(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	runningProjectsMu.Lock()
	runningProjects[resolved] = &managedProject{cmd: &exec.Cmd{}, done: make(chan struct{})}
	runningProjectsMu.Unlock()
	defer func() {
		runningProjectsMu.Lock()
		delete(runningProjects, resolved)
		runningProjectsMu.Unlock()
	}()

	if err := waitForProjectEndpoint(root, "starter", "http://"+listener.Addr().String(), time.Second); err != nil {
		t.Fatalf("managed project should be allowed to open an available preview: %v", err)
	}
}


func TestNodeStarterServesOnlyPublicFiles(t *testing.T) {
	npm, err := exec.LookPath("npm")
	if err != nil {
		t.Skip("npm is not installed")
	}
	projectDir, err := createProject(t.TempDir(), "node-safe", "Node.js HTTP")
	if err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()
	secretPath := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("outside secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secretPath, filepath.Join(projectDir, "public", "outside-secret.txt")); err != nil {
		t.Skipf("symlinks are not available: %v", err)
	}

	port, err := availableLocalPort()
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(projectDir, "node-starter-test.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(npm, "start")
	cmd.Dir = projectDir
	cmd.Env = environmentWith("GOAMPP_PORT", strconv.Itoa(port))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatal(err)
	}
	_ = logFile.Close()
	finished := make(chan struct{})
	exitResult := make(chan error, 1)
	go func() {
		exitResult <- cmd.Wait()
		close(finished)
	}()
	t.Cleanup(func() {
		managed := &managedProject{cmd: cmd, done: finished}
		if !managed.finished() {
			_ = terminateProcessGroup(cmd.Process.Pid, 250*time.Millisecond)
			_ = waitForManagedProjectExit(managed, 2*time.Second)
		}
	})

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 250 * time.Millisecond}
	deadline := time.Now().Add(10 * time.Second)
	var homepage []byte
	for {
		response, requestErr := client.Get(baseURL + "/")
		if requestErr == nil {
			var readErr error
			homepage, readErr = io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr != nil {
				t.Fatalf("read Node starter homepage: %v", readErr)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("homepage status = %d, want 200", response.StatusCode)
			}
			break
		}
		select {
		case processErr := <-exitResult:
			t.Fatalf("Node starter exited before becoming ready: %v", processErr)
		default:
		}
		if time.Now().After(deadline) {
			log, _ := os.ReadFile(logPath)
			t.Fatalf("Node starter did not become ready: %v; process output: %s", requestErr, log)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(string(homepage), "Node.js is running.") {
		t.Fatal("Node starter homepage content was not served")
	}

	for path, wantStatus := range map[string]int{
		"/README.md":           http.StatusNotFound,
		"/outside-secret.txt": http.StatusForbidden,
	} {
		response, requestErr := client.Get(baseURL + path)
		if requestErr != nil {
			t.Fatalf("request %s: %v", path, requestErr)
		}
		_ = response.Body.Close()
		if response.StatusCode != wantStatus {
			t.Errorf("GET %s status = %d, want %d", path, response.StatusCode, wantStatus)
		}
	}
}

func TestNonNodeStarterTemplatesServeOnlyPublicFiles(t *testing.T) {
	tests := []struct {
		template string
		command  string
		args     []string
	}{
		{"Go net/http", "go", []string{"run", "."}},
		{"Python HTTP", "python3", []string{"server.py"}},
		{"Static HTML", "python3", []string{"serve.py"}},
		{"PHP", "php", nil},
	}
	for _, test := range tests {
		t.Run(test.template, func(t *testing.T) {
			command, err := exec.LookPath(test.command)
			if err != nil && test.command == "python3" {
				command, err = exec.LookPath("python")
			}
			if err != nil {
				t.Skipf("%s runtime is not installed", test.command)
			}
			projectDir, err := createProject(t.TempDir(), "safe-web-root", test.template)
			if err != nil {
				t.Fatal(err)
			}
			outsideDir := t.TempDir()
			secretPath := filepath.Join(outsideDir, "secret.txt")
			if err := os.WriteFile(secretPath, []byte("outside secret"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(secretPath, filepath.Join(projectDir, "public", "outside-secret.txt")); err != nil {
				t.Skipf("symlinks are not available: %v", err)
			}

			port, err := availableLocalPort()
			if err != nil {
				t.Fatal(err)
			}
			args := test.args
			if test.template == "PHP" {
				args = []string{"-S", fmt.Sprintf("127.0.0.1:%d", port), "-t", "public", "router.php"}
			}
			cmd := exec.Command(command, args...)
			cmd.Dir = projectDir
			cmd.Env = environmentWith("GOAMPP_PORT", strconv.Itoa(port))
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			finished := make(chan struct{})
			exitResult := make(chan error, 1)
			go func() {
				exitResult <- cmd.Wait()
				close(finished)
			}()
			t.Cleanup(func() {
				managed := &managedProject{cmd: cmd, done: finished}
				if !managed.finished() {
					_ = terminateProcessGroup(cmd.Process.Pid, 250*time.Millisecond)
					_ = waitForManagedProjectExit(managed, 2*time.Second)
				}
			})

			baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
			client := &http.Client{Timeout: 500 * time.Millisecond}
			deadline := time.Now().Add(15 * time.Second)
			for {
				response, requestErr := client.Get(baseURL + "/")
				if requestErr == nil {
					body, readErr := io.ReadAll(response.Body)
					_ = response.Body.Close()
					if readErr != nil {
						t.Fatalf("read %s homepage: %v", test.template, readErr)
					}
					if response.StatusCode != http.StatusOK {
						t.Fatalf("homepage status = %d, want 200; body=%q", response.StatusCode, body)
					}
					break
				}
				select {
				case processErr := <-exitResult:
					t.Fatalf("%s server exited before becoming ready: %v", test.template, processErr)
				default:
				}
				if time.Now().After(deadline) {
					t.Fatalf("%s server did not become ready: %v", test.template, requestErr)
				}
				time.Sleep(50 * time.Millisecond)
			}

			for path, wantStatus := range map[string]int{
				"/README.md":          http.StatusNotFound,
				"/outside-secret.txt": http.StatusForbidden,
			} {
				response, requestErr := client.Get(baseURL + path)
				if requestErr != nil {
					t.Fatalf("request %s: %v", path, requestErr)
				}
				_ = response.Body.Close()
				if response.StatusCode != wantStatus {
					t.Errorf("GET %s status = %d, want %d", path, response.StatusCode, wantStatus)
				}
			}
		})
	}
}

func TestStarterServersRejectSymlinkedPublicDirectory(t *testing.T) {
	tests := []struct {
		template string
		command  string
		args     []string
	}{
		{"Node.js HTTP", "npm", []string{"start"}},
		{"Go net/http", "go", []string{"run", "."}},
		{"Python HTTP", "python3", []string{"server.py"}},
		{"Static HTML", "python3", []string{"serve.py"}},
	}
	for _, test := range tests {
		t.Run(test.template, func(t *testing.T) {
			command, err := exec.LookPath(test.command)
			if err != nil && test.command == "python3" {
				command, err = exec.LookPath("python")
			}
			if err != nil {
				t.Skipf("%s runtime is not installed", test.command)
			}
			projectDir, err := createProject(t.TempDir(), "symlink-root", test.template)
			if err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			if err := os.WriteFile(filepath.Join(outside, "index.html"), []byte("outside root"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(filepath.Join(projectDir, "public")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(projectDir, "public")); err != nil {
				t.Skipf("symlinks are not available: %v", err)
			}

			port, err := availableLocalPort()
			if err != nil {
				t.Fatal(err)
			}
			logPath := filepath.Join(projectDir, "server-test.log")
			logFile, err := os.Create(logPath)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(command, test.args...)
			cmd.Dir = projectDir
			cmd.Env = environmentWith("GOAMPP_PORT", strconv.Itoa(port))
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Stdout, cmd.Stderr = logFile, logFile
			if err := cmd.Start(); err != nil {
				_ = logFile.Close()
				t.Fatal(err)
			}
			_ = logFile.Close()
			exitResult := make(chan error, 1)
			go func() { exitResult <- cmd.Wait() }()
			select {
			case runErr := <-exitResult:
				if runErr == nil {
					t.Fatalf("%s unexpectedly started with a symlinked public directory", test.template)
				}
			case <-time.After(15 * time.Second):
				_ = terminateProcessGroup(cmd.Process.Pid, 250*time.Millisecond)
				select {
				case <-exitResult:
				case <-time.After(2 * time.Second):
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
					_ = cmd.Process.Kill()
					select {
					case <-exitResult:
					case <-time.After(time.Second):
					}
				}
				log, _ := os.ReadFile(logPath)
				t.Fatalf("%s did not reject a symlinked public directory; output: %s", test.template, log)
			}
			log, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(log), "public must be a real directory") {
				t.Fatalf("%s exited for an unexpected reason: %s", test.template, log)
			}
		})
	}
}

func TestPHPServerRejectsSymlinkedPublicDirectory(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("PHP CLI is not installed")
	}
	projectDir, err := createProject(t.TempDir(), "php-safe-root", "PHP")
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "index.php"), []byte("outside secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(projectDir, "public")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(projectDir, "public")); err != nil {
		t.Skipf("symlinks are not available: %v", err)
	}

	port, err := availableLocalPort()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(php, "-S", fmt.Sprintf("127.0.0.1:%d", port), "-t", "public", "router.php")
	cmd.Dir = projectDir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	exitResult := make(chan error, 1)
	go func() {
		exitResult <- cmd.Wait()
		close(finished)
	}()
	t.Cleanup(func() {
		select {
		case <-finished:
			return
		default:
		}
		_ = terminateProcessGroup(cmd.Process.Pid, 250*time.Millisecond)
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Kill()
		}
	})

	client := &http.Client{Timeout: 500 * time.Millisecond}
	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	deadline := time.Now().Add(5 * time.Second)
	for {
		response, requestErr := client.Get(url)
		if requestErr == nil {
			body, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr != nil {
				t.Fatalf("read PHP router response: %v", readErr)
			}
			if response.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "Invalid public directory") {
				t.Fatalf("PHP router response = %d %q, want 403 for symlinked public root", response.StatusCode, body)
			}
			return
		}
		select {
		case processErr := <-exitResult:
			t.Fatalf("PHP server exited before request could be checked: %v", processErr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("PHP server did not answer root-symlink check: %v", requestErr)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestProjectRunSpecRejectsUnconfinedStaticFolder(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "custom")
	if err := os.MkdirAll(filepath.Join(projectDir, "public"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "public", "index.html"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := projectRunSpecForDir(projectDir); err == nil {
		t.Fatal("a generic public/index.html folder without a confined runner must not be started automatically")
	}
}

func TestProjectRunSpecAtPort(t *testing.T) {
	tests := []struct {
		template string
		portMode string
		wantArgs []string
	}{
		{"PHP", "php", []string{"-S", "127.0.0.1:8001", "-t", "public", "router.php"}},
		{"Static HTML", "env", []string{"serve.py"}},
		{"Node.js HTTP", "env", []string{"start"}},
		{"Go net/http", "env", []string{"run", "."}},
		{"Python HTTP", "env", []string{"server.py"}},
	}
	for i, test := range tests {
		t.Run(test.template, func(t *testing.T) {
			root := t.TempDir()
			projectDir, err := createProject(root, "starter", test.template)
			if err != nil {
				t.Fatal(err)
			}
			spec, err := projectRunSpecForDir(projectDir)
			if err != nil {
				t.Fatal(err)
			}
			if spec.PortMode != test.portMode {
				t.Fatalf("PortMode = %q, want %q", spec.PortMode, test.portMode)
			}
			port := 19001 + i
			spec = projectRunSpecAtPort(spec, port)
			wantURL := fmt.Sprintf("http://127.0.0.1:%d", port)
			if spec.URL != wantURL || spec.Port != port {
				t.Fatalf("URL/Port = %q/%d, want %q/%d", spec.URL, spec.Port, wantURL, port)
			}
			wantArgs := test.wantArgs
			switch spec.PortMode {
			case "php":
				wantArgs = []string{"-S", fmt.Sprintf("127.0.0.1:%d", port), "-t", "public", "router.php"}
			}
			if !reflect.DeepEqual(spec.Args, wantArgs) {
				t.Fatalf("Args = %#v, want %#v", spec.Args, wantArgs)
			}
		})
	}
}



func TestStopProjectStopsManagedProcessGroup(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh is not available")
	}
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep is not available")
	}
	root := t.TempDir()
	projectDir, err := createProject(root, "stop-me", "Static HTML")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(shell, "-c", "sleep 30 & wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	managed := &managedProject{cmd: cmd, done: make(chan struct{})}
	runningProjectsMu.Lock()
	previous, hadPrevious := runningProjects[resolved]
	runningProjects[resolved] = managed
	runningProjectsMu.Unlock()

	logPath := filepath.Join(projectDir, ".goampp-run.log")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	superviseProject(resolved, logPath, cmd, managed)
	t.Cleanup(func() {
		if !managed.finished() && cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = waitForManagedProjectExit(managed, 2*time.Second)
		}
		runningProjectsMu.Lock()
		if hadPrevious {
			runningProjects[resolved] = previous
		} else if runningProjects[resolved] == managed {
			delete(runningProjects, resolved)
		}
		runningProjectsMu.Unlock()
	})

	if err := stopProject(root, "stop-me"); err != nil {
		t.Fatalf("stopProject returned an error: %v", err)
	}
	if !waitForManagedProjectExit(managed, 100*time.Millisecond) {
		t.Fatal("stopProject returned before the managed process was reaped")
	}
}

func TestParseLinuxProcStatHandlesParenthesesInCommandName(t *testing.T) {
	state, group, ok := parseLinuxProcStat("123 (worker ) with (parentheses)) S 1 456 456 0 0 0")
	if !ok {
		t.Fatal("expected valid /proc stat data to parse")
	}
	if state != 'S' || group != 456 {
		t.Fatalf("parsed state/group = %q/%d, want S/456", state, group)
	}
	if _, _, ok := parseLinuxProcStat("not a proc stat line"); ok {
		t.Fatal("malformed /proc stat data should be rejected")
	}
}

func TestSuperviseProjectCleansUpOrphanedChild(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh is not available")
	}
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep is not available")
	}
	root := t.TempDir()
	projectDir, err := createProject(root, "orphan-test", "Static HTML")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	childPIDFile := filepath.Join(projectDir, "child.pid")
	cmd := exec.Command(shell, "-c", `sleep 30 & echo $! > "$1"; exit 0`, "sh", childPIDFile)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	managed := &managedProject{cmd: cmd, done: make(chan struct{})}
	logPath := filepath.Join(projectDir, ".goampp-run.log")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		t.Fatal(err)
	}
	runningProjectsMu.Lock()
	previous, hadPrevious := runningProjects[resolved]
	runningProjects[resolved] = managed
	runningProjectsMu.Unlock()
	superviseProject(resolved, logPath, cmd, managed)
	t.Cleanup(func() {
		if !managed.finished() && cmd.Process != nil && processGroupExists(cmd.Process.Pid) {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		_ = waitForManagedProjectExit(managed, 2*time.Second)
		runningProjectsMu.Lock()
		if hadPrevious {
			runningProjects[resolved] = previous
		} else if runningProjects[resolved] == managed {
			delete(runningProjects, resolved)
		}
		runningProjectsMu.Unlock()
	})

	if !waitForManagedProjectExit(managed, 3*time.Second) {
		t.Fatal("supervisor did not finish cleaning up the process group")
	}
	if processGroupExists(cmd.Process.Pid) {
		t.Fatal("supervisor left a live process in the managed process group")
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(log), "process group cleanup failed") {
		t.Fatalf("supervisor reported process-group cleanup failure: %s", log)
	}
	childPIDData, err := os.ReadFile(childPIDFile)
	if err != nil {
		t.Fatalf("child process did not write its PID: %v", err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(string(childPIDData)))
	if err != nil {
		t.Fatalf("invalid child PID %q: %v", childPIDData, err)
	}
	if stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(childPID), "stat")); err == nil {
		fields := strings.Fields(string(stat))
		if len(fields) > 2 && fields[2] != "Z" && fields[2] != "X" {
			t.Fatalf("orphaned child process %d is still running (state %s)", childPID, fields[2])
		}
	}
}

func TestWaitForManagedProjectExit(t *testing.T) {
	managed := &managedProject{done: make(chan struct{})}
	if waitForManagedProjectExit(managed, 10*time.Millisecond) {
		t.Fatal("expected an open done channel to time out")
	}
	close(managed.done)
	if !waitForManagedProjectExit(managed, time.Second) {
		t.Fatal("expected a closed done channel to report process exit")
	}
}

func TestManagedProjectFinishedUsesDoneChannel(t *testing.T) {
	managed := &managedProject{cmd: &exec.Cmd{}, done: make(chan struct{})}
	if managed.finished() {
		t.Fatal("newly managed process should not be finished")
	}
	close(managed.done)
	if !managed.finished() {
		t.Fatal("closed done channel should mark the process as finished")
	}
}

func TestEnvironmentWithReplacesExistingValue(t *testing.T) {
	env := environmentWith("GOAMPP_PORT", "19234")
	count := 0
	for _, item := range env {
		if strings.HasPrefix(item, "GOAMPP_PORT=") {
			count++
			if item != "GOAMPP_PORT=19234" {
				t.Fatalf("unexpected GOAMPP_PORT value: %q", item)
			}
		}
	}
	if count != 1 {
		t.Fatalf("GOAMPP_PORT appeared %d times, want exactly once", count)
	}
}
