//go:build linux

package linuxapp

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type projectRunSpec struct {
	Command  string
	Args     []string
	URL      string
	Port     int
	PortMode string
}

type managedProject struct {
	cmd  *exec.Cmd
	done chan struct{}
}

func (p *managedProject) finished() bool {
	if p == nil {
		return true
	}
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

var (
	runningProjectsMu sync.Mutex
	runningProjects   = make(map[string]*managedProject)
)

func availableLocalPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("unexpected local listener address type %T", listener.Addr())
	}
	return addr.Port, nil
}

func projectRunSpecAtPort(spec projectRunSpec, port int) projectRunSpec {
	spec.Port = port
	spec.URL = fmt.Sprintf("http://127.0.0.1:%d", port)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	switch spec.PortMode {
	case "php":
		spec.Args = []string{"-S", addr, "-t", "public", "router.php"}
	case "framework:laravel", "framework:livewire":
		spec.Args = []string{"artisan", "serve", "--host", "127.0.0.1", "--port", strconv.Itoa(port)}
	case "framework:symfony":
		spec.Args = []string{"-S", addr, "-t", "public", "public/index.php"}
	case "framework:codeigniter":
		spec.Args = []string{"spark", "serve", "--host", "127.0.0.1", "--port", strconv.Itoa(port)}
	case "framework:wordpress":
		spec.Args = []string{"-S", addr, "-t", ".", ".goampp-router.php"}
	case "framework:nextjs":
		spec.Args = []string{"run", "dev", "--", "--hostname", "127.0.0.1", "--port", strconv.Itoa(port)}
	case "framework:vite":
		spec.Args = []string{"run", "dev", "--", "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--strictPort"}
	case "framework:django":
		spec.Args = []string{"manage.py", "runserver", addr}
	case "framework:fastapi":
		spec.Args = []string{"-m", "uvicorn", "main:app", "--host", "127.0.0.1", "--port", strconv.Itoa(port)}
	case "framework:springboot":
		spec.Args = []string{"mvnw", "spring-boot:run", "-Dspring-boot.run.arguments=--server.address=127.0.0.1,--server.port=" + strconv.Itoa(port)}
	}
	return spec
}

func frameworkProjectRunSpec(dir, name string) (projectRunSpec, error) {
	spec := projectRunSpec{URL: "http://127.0.0.1:0", PortMode: "framework:"}
	switch strings.TrimSpace(name) {
	case "Laravel":
		spec.Command, spec.PortMode = "php", "framework:laravel"
	case "Laravel + Livewire":
		spec.Command, spec.PortMode = "php", "framework:livewire"
	case "Symfony":
		spec.Command, spec.PortMode = "php", "framework:symfony"
	case "CodeIgniter 4":
		spec.Command, spec.PortMode = "php", "framework:codeigniter"
	case "WordPress":
		spec.Command, spec.PortMode = "php", "framework:wordpress"
	case "Next.js":
		spec.Command, spec.PortMode = "npm", "framework:nextjs"
	case "Vite + React":
		spec.Command, spec.PortMode = "npm", "framework:vite"
	case "Express":
		spec.Command, spec.Args, spec.PortMode = "node", []string{"index.js"}, "framework:express"
	case "NestJS":
		spec.Command, spec.Args, spec.PortMode = "npm", []string{"run", "start:dev"}, "framework:nestjs"
	case "AdonisJS":
		spec.Command, spec.Args, spec.PortMode = "npm", []string{"run", "dev"}, "framework:adonis"
	case "Flask":
		spec.Command, spec.Args, spec.PortMode = filepath.Join(dir, ".venv", "bin", "python"), []string{"app.py"}, "framework:flask"
	case "Django":
		spec.Command, spec.PortMode = filepath.Join(dir, ".venv", "bin", "python"), "framework:django"
	case "FastAPI":
		spec.Command, spec.PortMode = filepath.Join(dir, ".venv", "bin", "python"), "framework:fastapi"
	case "Go HTTP server", "Gin (Go)":
		spec.Command, spec.Args, spec.PortMode = "go", []string{"run", "."}, "framework:go"
	case "Spring Boot":
		spec.Command, spec.PortMode = "sh", "framework:springboot"
	default:
		return projectRunSpec{}, fmt.Errorf("no supported runner is registered for framework %q", name)
	}
	switch spec.PortMode {
	case "framework:express":
		spec.URL = "http://127.0.0.1:3000"
	case "framework:nestjs":
		spec.URL = "http://127.0.0.1:3000"
	case "framework:adonis":
		spec.URL = "http://127.0.0.1:3333"
	case "framework:laravel", "framework:livewire", "framework:symfony", "framework:codeigniter", "framework:wordpress":
		spec.URL = "http://127.0.0.1:8000"
	case "framework:nextjs", "framework:vite":
		spec.URL = "http://127.0.0.1:3000"
	case "framework:django", "framework:fastapi":
		spec.URL = "http://127.0.0.1:8000"
	case "framework:go":
		spec.URL = "http://127.0.0.1:8080"
	case "framework:springboot":
		spec.URL = "http://127.0.0.1:8080"
	}
	return spec, nil
}

func environmentWith(key, value string) []string {
	return environmentWithValues(map[string]string{key: value})
}

func environmentWithValues(values map[string]string) []string {
	current := os.Environ()
	out := make([]string, 0, len(current)+len(values))
	for _, item := range current {
		key, _, _ := strings.Cut(item, "=")
		if _, replace := values[key]; !replace {
			out = append(out, item)
		}
	}
	for key, value := range values {
		out = append(out, key+"="+value)
	}
	return out
}

func projectRunSpecForDir(dir string) (projectRunSpec, error) {
	has := func(name string) bool {
		info, err := os.Stat(filepath.Join(dir, name))
		return err == nil && info.Mode().IsRegular()
	}
	if has(".goampp-framework") {
		data, err := os.ReadFile(filepath.Join(dir, ".goampp-framework"))
		if err != nil {
			return projectRunSpec{}, fmt.Errorf("read GoAMPP framework metadata: %w", err)
		}
		spec, err := frameworkProjectRunSpec(dir, strings.TrimSpace(string(data)))
		if err != nil {
			return projectRunSpec{}, err
		}
		switch spec.PortMode {
		case "framework:laravel", "framework:livewire":
			if !has("artisan") { return projectRunSpec{}, fmt.Errorf("Laravel artisan entry point was not found") }
		case "framework:symfony":
			if !has(filepath.Join("public", "index.php")) { return projectRunSpec{}, fmt.Errorf("Symfony public/index.php was not found") }
		case "framework:codeigniter":
			if !has("spark") { return projectRunSpec{}, fmt.Errorf("CodeIgniter spark entry point was not found") }
		case "framework:wordpress":
			if !has("index.php") || !has(".goampp-router.php") { return projectRunSpec{}, fmt.Errorf("WordPress entry point or local router was not found") }
		case "framework:nextjs", "framework:vite", "framework:nestjs", "framework:adonis":
			if !has("package.json") { return projectRunSpec{}, fmt.Errorf("package.json was not found") }
		case "framework:express":
			if !has("index.js") { return projectRunSpec{}, fmt.Errorf("Express index.js entry point was not found") }
		case "framework:flask":
			if !has("app.py") { return projectRunSpec{}, fmt.Errorf("Flask app.py entry point was not found") }
		case "framework:django":
			if !has("manage.py") { return projectRunSpec{}, fmt.Errorf("Django manage.py entry point was not found") }
		case "framework:fastapi":
			if !has("main.py") { return projectRunSpec{}, fmt.Errorf("FastAPI main.py entry point was not found") }
		case "framework:go":
			if !has("go.mod") || !has("main.go") { return projectRunSpec{}, fmt.Errorf("Go main.go or go.mod was not found") }
		case "framework:springboot":
			if !has("mvnw") || !has("pom.xml") { return projectRunSpec{}, fmt.Errorf("Spring Boot Maven wrapper or pom.xml was not found") }
		}
		return spec, nil
	}
	switch {
	case has("package.json") && has("server.js"):
		return projectRunSpec{Command: "npm", Args: []string{"start"}, URL: "http://127.0.0.1:3000", Port: 3000, PortMode: "env"}, nil
	case has("go.mod") && has("main.go"):
		return projectRunSpec{Command: "go", Args: []string{"run", "."}, URL: "http://127.0.0.1:8080", Port: 8080, PortMode: "env"}, nil
	case has("serve.py") && has("public/index.html"):
		command := "python3"
		if _, err := exec.LookPath(command); err != nil {
			command = "python"
		}
		return projectRunSpec{Command: command, Args: []string{"serve.py"}, URL: "http://127.0.0.1:8000", Port: 8000, PortMode: "env"}, nil
	case has("server.py"):
		command := "python3"
		if _, err := exec.LookPath(command); err != nil {
			command = "python"
		}
		return projectRunSpec{Command: command, Args: []string{"server.py"}, URL: "http://127.0.0.1:8002", Port: 8002, PortMode: "env"}, nil
	case has("public/index.php"):
		return projectRunSpec{Command: "php", Args: []string{"-S", "127.0.0.1:8001", "-t", "public", "router.php"}, URL: "http://127.0.0.1:8001", Port: 8001, PortMode: "php"}, nil
	default:
		return projectRunSpec{}, fmt.Errorf("no supported GoAMPP starter was detected in this folder")
	}
}

func startProject(root, name string) (string, error) {
	resolvedPath, err := resolveProjectDir(root, name)
	if err != nil {
		return "", err
	}
	spec, err := projectRunSpecForDir(resolvedPath)
	if err != nil {
		return "", err
	}
	port, err := availableLocalPort()
	if err != nil {
		return "", fmt.Errorf("choose a local preview port: %w", err)
	}
	spec = projectRunSpecAtPort(spec, port)
	binary, err := exec.LookPath(spec.Command)
	if err != nil {
		return "", fmt.Errorf("%s is not installed or not available on PATH", spec.Command)
	}
	runningProjectsMu.Lock()
	if managed := runningProjects[resolvedPath]; managed != nil && !managed.finished() {
		runningProjectsMu.Unlock()
		return "", fmt.Errorf("this project is already running")
	}
	runningProjectsMu.Unlock()

	logPath := filepath.Join(resolvedPath, ".goampp-run.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return "", fmt.Errorf("open project log: %w", err)
	}

	cmd := exec.Command(binary, spec.Args...)
	cmd.Dir = resolvedPath
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if spec.PortMode == "env" {
		cmd.Env = environmentWith("GOAMPP_PORT", strconv.Itoa(spec.Port))
	} else if strings.HasPrefix(spec.PortMode, "framework:") {
		cmd.Env = environmentWithValues(map[string]string{
			"GOAMPP_PORT": strconv.Itoa(spec.Port),
			"PORT": strconv.Itoa(spec.Port),
			"HOST": "127.0.0.1",
		})
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	runningProjectsMu.Lock()
	if managed := runningProjects[resolvedPath]; managed != nil && !managed.finished() {
		runningProjectsMu.Unlock()
		_ = logFile.Close()
		return "", fmt.Errorf("this project is already running")
	}
	if err := cmd.Start(); err != nil {
		runningProjectsMu.Unlock()
		_ = logFile.Close()
		return "", fmt.Errorf("start project: %w", err)
	}
	managed := &managedProject{cmd: cmd, done: make(chan struct{})}
	runningProjects[resolvedPath] = managed
	runningProjectsMu.Unlock()
	_ = logFile.Close()

	superviseProject(resolvedPath, logPath, cmd, managed)
	return spec.URL, nil
}

func resolveProjectDir(root, name string) (string, error) {
	if !projectNamePattern.MatchString(name) {
		return "", fmt.Errorf("invalid project directory name")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rootAbs, err = filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", fmt.Errorf("resolve projects directory: %w", err)
	}
	resolvedPath, err := filepath.EvalSymlinks(filepath.Join(rootAbs, name))
	if err != nil {
		return "", fmt.Errorf("resolve project folder: %w", err)
	}
	relative, err := filepath.Rel(rootAbs, resolvedPath)
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("project path is outside the projects directory")
	}
	info, err := os.Stat(resolvedPath)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("project folder does not exist")
	}
	return resolvedPath, nil
}

func isProjectRunning(root, name string) bool {
	resolvedPath, err := resolveProjectDir(root, name)
	if err != nil {
		return false
	}
	runningProjectsMu.Lock()
	managed := runningProjects[resolvedPath]
	runningProjectsMu.Unlock()
	return managed != nil && !managed.finished()
}

func stopProject(root, name string) error {
	resolvedPath, err := resolveProjectDir(root, name)
	if err != nil {
		return err
	}
	runningProjectsMu.Lock()
	managed := runningProjects[resolvedPath]
	runningProjectsMu.Unlock()
	if managed == nil || managed.finished() || managed.cmd == nil || managed.cmd.Process == nil {
		return fmt.Errorf("this project is not running")
	}

	pid := managed.cmd.Process.Pid
	if err := terminateProcessGroup(pid, 600*time.Millisecond); err != nil {
		_ = managed.cmd.Process.Kill()
		if !waitForManagedProjectExit(managed, time.Second) {
			return fmt.Errorf("stop project: %w; timed out waiting for its process to exit", err)
		}
		return fmt.Errorf("stop project: %w", err)
	}
	if !waitForManagedProjectExit(managed, time.Second) {
		return fmt.Errorf("timed out waiting for the project process group to exit")
	}
	return nil
}

func superviseProject(resolvedPath, logPath string, cmd *exec.Cmd, managed *managedProject) {
	go func() {
		waitErr := cmd.Wait()
		groupErr := terminateProcessGroup(cmd.Process.Pid, 250*time.Millisecond)
		if file, openErr := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0o600); openErr == nil {
			if waitErr != nil {
				_, _ = fmt.Fprintf(file, "\n[GoAMPP] process exited: %v\n", waitErr)
			} else {
				_, _ = fmt.Fprintln(file, "\n[GoAMPP] process exited")
			}
			if groupErr != nil {
				_, _ = fmt.Fprintf(file, "[GoAMPP] process group cleanup failed: %v\n", groupErr)
			}
			_ = file.Close()
		}
		close(managed.done)
		runningProjectsMu.Lock()
		if runningProjects[resolvedPath] == managed {
			delete(runningProjects, resolvedPath)
		}
		runningProjectsMu.Unlock()
	}()
}

func processGroupExists(pgid int) bool {
	if pgid <= 0 {
		return false
	}
	entries, err := os.ReadDir("/proc")
	if err == nil {
		uncertain := false
		for _, entry := range entries {
			if _, parseErr := strconv.Atoi(entry.Name()); parseErr != nil {
				continue
			}
			data, readErr := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
			if readErr != nil {
				if !errors.Is(readErr, os.ErrNotExist) {
					uncertain = true
				}
				continue
			}
			state, group, ok := parseLinuxProcStat(string(data))
			if ok && group == pgid && state != 'Z' && state != 'X' {
				return true
			}
		}
		if !uncertain {
			return false
		}
	}
	// Fall back when /proc is unavailable or does not expose every process.
	// SIGCONT is not sent; signal zero only checks process-group existence.
	checkErr := syscall.Kill(-pgid, 0)
	return checkErr == nil || !errors.Is(checkErr, syscall.ESRCH)
}

func parseLinuxProcStat(stat string) (state byte, processGroup int, ok bool) {
	closeParen := strings.LastIndexByte(stat, ')')
	if closeParen < 0 || closeParen+1 >= len(stat) {
		return 0, 0, false
	}
	fields := strings.Fields(stat[closeParen+1:])
	if len(fields) < 3 || len(fields[0]) != 1 {
		return 0, 0, false
	}
	group, err := strconv.Atoi(fields[2])
	if err != nil {
		return 0, 0, false
	}
	return fields[0][0], group, true
}

func terminateProcessGroup(pgid int, grace time.Duration) error {
	if pgid <= 0 {
		return fmt.Errorf("invalid process group ID %d", pgid)
	}
	// Do not signal a stale process-group ID after its leader has exited and
	// the entire group has disappeared; the numeric ID could be reused.
	if !processGroupExists(pgid) {
		return nil
	}
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return fmt.Errorf("send SIGTERM to process group %d: %w", pgid, err)
	}

	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if !processGroupExists(pgid) {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !processGroupExists(pgid) {
		return nil
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("send SIGKILL to process group %d: %w", pgid, err)
	}
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !processGroupExists(pgid) {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	if processGroupExists(pgid) {
		return fmt.Errorf("process group %d did not exit after SIGKILL", pgid)
	}
	return nil
}

func waitForManagedProjectExit(managed *managedProject, timeout time.Duration) bool {
	if managed == nil || managed.done == nil {
		return false
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-managed.done:
		return true
	case <-timer.C:
		return false
	}
}

func projectPreviewReadyTimeout(projectDir string) time.Duration {
	data, err := os.ReadFile(filepath.Join(projectDir, ".goampp-framework"))
	if err != nil {
		return 15 * time.Second
	}
	switch strings.TrimSpace(string(data)) {
	case "Spring Boot":
		return 2 * time.Minute
	case "Next.js", "Vite + React", "NestJS", "AdonisJS", "Laravel", "Laravel + Livewire",
		"Symfony", "CodeIgniter 4", "WordPress", "Flask", "Django", "FastAPI", "Gin (Go)":
		return 75 * time.Second
	default:
		return 30 * time.Second
	}
}

func openPreviewWhenReady(projectDir, projectName, previewURL string, onDone func(error)) {
	go func() {
		err := waitForProjectEndpoint(projectDir, projectName, previewURL, projectPreviewReadyTimeout(projectDir))
		if err == nil {
			err = openExternal(previewURL)
		}
		if err != nil {
			appendProjectLog(projectDir, projectName, "[GoAMPP] preview: "+err.Error())
		}
		if onDone != nil {
			onDone(err)
		}
	}()
}

func waitForProjectEndpoint(root, name, rawURL string, timeout time.Duration) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("invalid preview URL")
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if !isProjectRunning(root, name) {
			return fmt.Errorf("project process exited before its preview became ready")
		}
		conn, err := net.DialTimeout("tcp", parsed.Host, 150*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			if !isProjectRunning(root, name) {
				return fmt.Errorf("project process exited while its preview was starting")
			}
			return nil
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timed out waiting for %s", parsed.Host)
	}
	return fmt.Errorf("waiting for %s: %w", parsed.Host, lastErr)
}

func appendProjectLog(projectDir, projectName, line string) {
	if !projectNamePattern.MatchString(projectName) {
		return
	}
	path := filepath.Join(projectDir, projectName, ".goampp-run.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintln(file, line)
	_ = file.Close()
}

func stopAllProjects() {
	runningProjectsMu.Lock()
	projects := make([]*managedProject, 0, len(runningProjects))
	for _, managed := range runningProjects {
		if managed != nil && !managed.finished() && managed.cmd != nil && managed.cmd.Process != nil {
			projects = append(projects, managed)
		}
	}
	runningProjectsMu.Unlock()

	for _, managed := range projects {
		if !managed.finished() && processGroupExists(managed.cmd.Process.Pid) {
			_ = syscall.Kill(-managed.cmd.Process.Pid, syscall.SIGTERM)
		}
	}
	if len(projects) > 0 {
		time.Sleep(250 * time.Millisecond)
	}
	for _, managed := range projects {
		if managed.finished() || managed.cmd == nil || managed.cmd.Process == nil {
			continue
		}
		pid := managed.cmd.Process.Pid
		if processGroupExists(pid) {
			if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				_ = managed.cmd.Process.Kill()
			}
		}
	}
	deadline := time.Now().Add(time.Second)
	for _, managed := range projects {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		_ = waitForManagedProjectExit(managed, remaining)
	}
}
