//go:build windows

package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Service represents a managed background process (Apache, MySQL, etc).
type Service struct {
	Name     string // e.g. "Apache"
	ExePath  string // e.g. "bin/apache/httpd.exe"
	Args     []string
	Port     int // port to probe for status ("is it up?")
	WorkDir  string
	// Env is additional environment variables merged into the child process
	// environment. Each entry is "KEY=VALUE". Used by e.g. RabbitMQ to set
	// ERLANG_HOME without polluting the system environment.
	Env      []string

	mu      sync.Mutex
	cmd     *exec.Cmd
	onLog   func(line string)
	onState func(running bool, pid int)
}

// SetLogger registers a callback invoked for every log line produced by the
// service (stdout + stderr) and by the manager itself (start/stop messages).
func (s *Service) SetLogger(fn func(line string)) {
	s.onLog = fn
}

// SetStateCallback registers a callback invoked when the service starts or
// stops. pid is 0 when the service is not running.
func (s *Service) SetStateCallback(fn func(running bool, pid int)) {
	s.onState = fn
}

func (s *Service) log(format string, a ...any) {
	if s.onLog != nil {
		s.onLog(fmt.Sprintf("[%s] %s", s.Name, fmt.Sprintf(format, a...)))
	}
}

// Running reports whether the managed process is currently alive.
func (s *Service) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cmd != nil && s.cmd.Process != nil
}

// PID returns the process ID of the running service, or 0 if stopped.
func (s *Service) PID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil && s.cmd.Process != nil {
		return s.cmd.Process.Pid
	}
	return 0
}

// Start launches the service. Returns an error if the exe is missing, the
// port is already in use, or the process fails to spawn.
//
// CRITICAL: the mutex is released before any user callbacks (onState,
// s.log) fire, because those callbacks marshal to the UI thread, which
// can then call Running() / PID() — both of which take this same mutex.
// Holding the lock across a UI round-trip deadlocks the process since
// Go sync.Mutex isn't reentrant.
func (s *Service) Start() error {
	s.mu.Lock()

	if s.cmd != nil && s.cmd.Process != nil {
		pid := s.cmd.Process.Pid
		s.mu.Unlock()
		return fmt.Errorf("%s already running (pid %d)", s.Name, pid)
	}

	if s.Port > 0 && isPortBusy(s.Port) {
		s.mu.Unlock()
		return fmt.Errorf("port %d already in use", s.Port)
	}

	cmd := exec.Command(s.ExePath, s.Args...)
	if s.WorkDir != "" {
		cmd.Dir = s.WorkDir
	}
	// Detach from console so the child doesn't pop up a window.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}

	// Merge per-service extra environment variables into the child's env.
	if len(s.Env) > 0 {
		cmd.Env = append(os.Environ(), s.Env...)
	}

	// PostgreSQL refuses to start when the process token has the
	// Administrators SID enabled (pgwin32_is_admin() check in main.c).
	// Since we are running as an elevated process, we use the official
	// pg_ctl.exe tool to safely drop privileges and launch the server.
	if s.Name == "PostgreSQL" {
		var dataDir string
		for i, arg := range s.Args {
			if arg == "-D" && i+1 < len(s.Args) {
				dataDir = s.Args[i+1]
				break
			}
		}
		if dataDir == "" {
			s.mu.Unlock()
			return fmt.Errorf("PostgreSQL data directory (-D) not found in arguments")
		}

		// Prevent duplicate start attempts by creating a placeholder cmd
		// immediately under the lock. This keeps the UI responsive but disables
		// the "Start" button since s.Running() is immediately true.
		placeholderCmd := exec.Command(s.ExePath, s.Args...)
		// Create a dummy process handle so s.cmd.Process is not nil
		placeholderCmd.Process, _ = os.FindProcess(os.Getpid())
		s.cmd = placeholderCmd
		onState := s.onState
		s.mu.Unlock()

		// Clean up postmaster.pid first to ensure we detect the new one
		pidFile := filepath.Join(dataDir, "postmaster.pid")
		_ = os.Remove(pidFile)

		// Run in the background to prevent blocking the UI thread.
		go func() {
			s.log("launching postgres.exe directly in the background...")

			// Format command: runas /trustlevel:0x20000 "\"path\to\goampp.exe\" --hide-run \"path\to\postgres.exe\" -D \"path\to\data\""
			// This routes the startup through our own GUI application wrapper to prevent conhost/cmd windows from opening.
			var cmdString string
			selfExe, err := os.Executable()
			if err == nil {
				cmdString = fmt.Sprintf(`"%s" --hide-run "%s" -D "%s"`, selfExe, s.ExePath, dataDir)
			} else {
				cmdString = fmt.Sprintf(`"%s" -D "%s"`, s.ExePath, dataDir)
			}
			startCmd := exec.Command("runas", "/trustlevel:0x20000", cmdString)
			if s.WorkDir != "" {
				startCmd.Dir = s.WorkDir
			}
			startCmd.SysProcAttr = &syscall.SysProcAttr{
				HideWindow:    true,
				CreationFlags: 0x08000000, // CREATE_NO_WINDOW
			}

			if err := startCmd.Start(); err != nil {
				s.log("failed to spawn runas: %v", err)
				s.mu.Lock()
				if s.cmd == placeholderCmd {
					s.cmd = nil
				}
				s.mu.Unlock()
				if onState != nil {
					onState(false, 0)
				}
				return
			}

			// Read the real process ID from postmaster.pid
			pid, err := readPostmasterPID(dataDir)
			if err != nil {
				s.log("failed to read postmaster PID: %v", err)
				s.mu.Lock()
				if s.cmd == placeholderCmd {
					s.cmd = nil
				}
				s.mu.Unlock()
				if onState != nil {
					onState(false, 0)
				}
				return
			}

			s.mu.Lock()
			// Check if we were stopped while starting
			if s.cmd != placeholderCmd {
				s.mu.Unlock()
				return
			}

			// Attach the real postmaster process to our command
			placeholderCmd.Process, _ = os.FindProcess(pid)
			s.mu.Unlock()

			s.log("started (pid %d)", pid)
			if onState != nil {
				onState(true, pid)
			}

			// Start tailing the log file in a background goroutine
			stopTail := make(chan struct{})
			go s.tailPostgresLog(dataDir, stopTail)

			// Watcher goroutine: monitor the running database process and notify on exit
			go func() {
				state, _ := placeholderCmd.Process.Wait()
				close(stopTail) // Stop tailing log when process exits

				s.mu.Lock()
				if s.cmd == placeholderCmd {
					s.cmd = nil
				}
				cb := s.onState
				s.mu.Unlock()

				exitCode := -1
				if state != nil {
					exitCode = state.ExitCode()
				}

				if exitCode != 0 && exitCode != -1 {
					s.log("exited: exit code %d", exitCode)
				} else {
					s.log("exited cleanly")
				}
				if cb != nil {
					cb(false, 0)
				}
			}()
		}()
		return nil
	}

	// Capture stdout and stderr so users can see *why* a service failed.
	// Without this, Apache's "AH00072: make_sock: could not bind to..."
	// messages go straight to /dev/null and the user just sees "exit 1".
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("start %s: %w", s.Name, err)
	}
	s.cmd = cmd
	pid := cmd.Process.Pid
	onState := s.onState // snapshot under the lock
	s.mu.Unlock()

	// From here down we're mutex-free, so the callbacks are free to
	// bounce through the UI thread and call Running()/PID() on us.
	s.log("started (pid %d)", pid)
	if onState != nil {
		onState(true, pid)
	}

	// Pipe both streams into the same log. We don't bother distinguishing
	// stdout from stderr in the log prefix — most services only use one.
	go streamToLog(stdout, s.log)
	go streamToLog(stderr, s.log)

	// Watcher goroutine: notify when the process exits on its own.
	go func(c *exec.Cmd) {
		werr := c.Wait()
		s.mu.Lock()
		// Only clear state if this is still the current process.
		if s.cmd == c {
			s.cmd = nil
		}
		cb := s.onState
		s.mu.Unlock()

		// Callback happens OUTSIDE the lock for the same reentrancy
		// reason as above.
		if werr != nil {
			s.log("exited: %v", werr)
		} else {
			s.log("exited cleanly")
		}
		if cb != nil {
			cb(false, 0)
		}
	}(cmd)

	return nil
}

// Stop terminates the running service. A no-op if not running.
//
// Apache's winnt MPM (and a few other services) forks a worker child
// that keeps running — and keeps holding its port — even after the
// parent process dies. A plain cmd.Process.Kill only touches the
// parent, so we use `taskkill /F /T /PID` to nuke the whole tree.
func (s *Service) Stop() error {
	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid

	// Use official pg_ctl to stop PostgreSQL cleanly
	if s.Name == "PostgreSQL" {
		s.log("stopping (pid %d) via pg_ctl...", pid)
		pgCtlPath := filepath.Join(filepath.Dir(s.ExePath), "pg_ctl.exe")
		var dataDir string
		for i, arg := range s.Args {
			if arg == "-D" && i+1 < len(s.Args) {
				dataDir = s.Args[i+1]
				break
			}
		}
		if dataDir != "" {
			stopCmd := exec.Command(pgCtlPath, "stop", "-D", dataDir, "-m", "fast")
			stopCmd.SysProcAttr = &syscall.SysProcAttr{
				HideWindow:    true,
				CreationFlags: 0x08000000,
			}
			out, err := stopCmd.CombinedOutput()
			if err != nil {
				s.log("pg_ctl stop failed: %v (output: %q)", err, string(out))
				// Fall back to standard taskkill below
			} else {
				return nil
			}
		}
	}

	s.log("stopping (pid %d)...", pid)

	// taskkill /F /T /PID <pid> = force-kill the process AND all children.
	// We don't bother checking its exit code — if taskkill itself fails,
	// cmd.Process.Kill is a reasonable fallback.
	kill := exec.Command("taskkill", "/F", "/T", "/PID", fmt.Sprint(pid))
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if err := kill.Run(); err != nil {
		// Fall back to the single-process kill; at least the parent dies.
		if err2 := cmd.Process.Kill(); err2 != nil {
			return fmt.Errorf("kill %s: taskkill=%v, proc.Kill=%v", s.Name, err, err2)
		}
	}
	// Watcher goroutine will handle the state update.
	return nil
}

func readPostmasterPID(dataDir string) (int, error) {
	pidFile := filepath.Join(dataDir, "postmaster.pid")
	var data []byte
	var err error
	// Try reading up to 10 times with a brief sleep, in case pg_ctl is still writing the file
	for i := 0; i < 10; i++ {
		data, err = os.ReadFile(pidFile)
		if err == nil && len(data) > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		return 0, err
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 {
		return 0, fmt.Errorf("empty postmaster.pid")
	}
	var pid int
	_, err = fmt.Sscanf(strings.TrimSpace(lines[0]), "%d", &pid)
	if err != nil {
		return 0, fmt.Errorf("invalid PID in postmaster.pid: %w", err)
	}
	return pid, nil
}

// isPortBusy returns true if the given TCP port is already bound on localhost.
func isPortBusy(port int) bool {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return true
	}
	_ = ln.Close()
	// Tiny delay to let the socket fully release before a subsequent bind.
	time.Sleep(10 * time.Millisecond)
	return false
}

// streamToLog reads from a process's stdout/stderr pipe line-by-line and
// forwards each non-empty line to the service logger. Trimming \r handles
// Apache's \r\n line endings cleanly so log entries don't have gaps.
func streamToLog(r io.Reader, logf func(format string, a ...any)) {
	sc := bufio.NewScanner(r)
	// httpd.conf errors with long include paths can blow past the default
	// 64 KB scanner line limit. Bump it.
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line != "" {
			logf("%s", line)
		}
	}
}

// tailPostgresLog runs in the background, tails the active PostgreSQL log file,
// and streams its output in real-time to the GoAMPP log panel.
func (s *Service) tailPostgresLog(dataDir string, stopCh <-chan struct{}) {
	var logFilePath string
	currentLogfile := filepath.Join(dataDir, "current_logfiles")

	// Poll up to 20 times (4 seconds) for the log file to appear
	for i := 0; i < 20; i++ {
		select {
		case <-stopCh:
			return
		default:
		}

		// Try current_logfiles first
		if data, err := os.ReadFile(currentLogfile); err == nil && len(data) > 0 {
			lines := strings.Split(string(data), "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "stderr ") {
					relPath := strings.TrimPrefix(line, "stderr ")
					logFilePath = filepath.Join(dataDir, relPath)
					break
				}
			}
		}

		// Fallback: list log/ directory and find the newest file
		if logFilePath == "" {
			logDir := filepath.Join(dataDir, "log")
			files, err := os.ReadDir(logDir)
			if err == nil && len(files) > 0 {
				var newestFile os.FileInfo
				for _, entry := range files {
					if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".log") {
						continue
					}
					info, err := entry.Info()
					if err == nil {
						if newestFile == nil || info.ModTime().After(newestFile.ModTime()) {
							newestFile = info
						}
					}
				}
				if newestFile != nil {
					logFilePath = filepath.Join(logDir, newestFile.Name())
				}
			}
		}

		if logFilePath != "" {
			if _, err := os.Stat(logFilePath); err == nil {
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}

	if logFilePath == "" {
		s.log("warning: could not locate PostgreSQL log file")
		return
	}

	file, err := os.Open(logFilePath)
	if err != nil {
		s.log("warning: failed to open PostgreSQL log file: %v", err)
		return
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	var lineBuf strings.Builder

	for {
		select {
		case <-stopCh:
			return
		default:
		}

		r, _, err := reader.ReadRune()
		if err != nil {
			if err == io.EOF {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			break
		}

		if r == '\n' {
			line := lineBuf.String()
			lineBuf.Reset()
			line = strings.TrimRight(line, "\r")
			if line != "" {
				s.logPostgresLine(line)
			}
		} else {
			lineBuf.WriteRune(r)
		}
	}
}

// logPostgresLine cleans up PostgreSQL log messages by stripping the long timestamps
// and PID prefixes, forwarding a clean and professional entry (e.g. LOG: ..., ERROR: ...) to s.log.
func (s *Service) logPostgresLine(line string) {
	// Look for the level delimiter ":  "
	idx := strings.Index(line, ":  ")
	if idx != -1 {
		prefix := line[:idx]
		msg := line[idx+3:]

		// Extract level (the last word in prefix, e.g. "LOG", "WARNING", "ERROR", etc.)
		words := strings.Fields(prefix)
		if len(words) > 0 {
			level := words[len(words)-1]
			s.log("%s: %s", level, msg)
			return
		}
	}
	s.log("%s", line)
}
