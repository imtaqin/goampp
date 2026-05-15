//go:build windows

package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
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
	// Provide a filtered token with the Administrators SID set to
	// deny-only so the check passes while we're still running elevated.
	if s.Name == "PostgreSQL" {
		if tok, err := postgresToken(); err == nil {
			cmd.SysProcAttr.Token = syscall.Token(tok)
		} else {
			s.mu.Unlock()
			return fmt.Errorf("PostgreSQL token: %w", err)
		}
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
