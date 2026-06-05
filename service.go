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

type Service struct {
	Name    string
	ExePath string
	Args    []string
	Port    int
	WorkDir string

	Env []string

	mu      sync.Mutex
	cmd     *exec.Cmd
	onLog   func(line string)
	onState func(running bool, pid int)
}

func (s *Service) SetLogger(fn func(line string)) {
	s.onLog = fn
}

func (s *Service) SetStateCallback(fn func(running bool, pid int)) {
	s.onState = fn
}

func (s *Service) log(format string, a ...any) {
	if s.onLog != nil {
		s.onLog(fmt.Sprintf("[%s] %s", s.Name, fmt.Sprintf(format, a...)))
	}
}

func (s *Service) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cmd != nil && s.cmd.Process != nil
}

func (s *Service) PID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil && s.cmd.Process != nil {
		return s.cmd.Process.Pid
	}
	return 0
}

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

	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000,
	}

	if len(s.Env) > 0 {
		cmd.Env = append(os.Environ(), s.Env...)
	}

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

		placeholderCmd := exec.Command(s.ExePath, s.Args...)

		placeholderCmd.Process, _ = os.FindProcess(os.Getpid())
		s.cmd = placeholderCmd
		onState := s.onState
		s.mu.Unlock()

		pidFile := filepath.Join(dataDir, "postmaster.pid")
		_ = os.Remove(pidFile)

		go func() {
			s.log("launching postgres.exe directly in the background...")

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
				CreationFlags: 0x08000000,
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

			if s.cmd != placeholderCmd {
				s.mu.Unlock()
				return
			}

			placeholderCmd.Process, _ = os.FindProcess(pid)
			s.mu.Unlock()

			s.log("started (pid %d)", pid)
			if onState != nil {
				onState(true, pid)
			}

			stopTail := make(chan struct{})
			go s.tailPostgresLog(dataDir, stopTail)

			go func() {
				state, _ := placeholderCmd.Process.Wait()
				close(stopTail)

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
	onState := s.onState
	s.mu.Unlock()

	s.log("started (pid %d)", pid)
	if onState != nil {
		onState(true, pid)
	}

	go streamToLog(stdout, s.log)
	go streamToLog(stderr, s.log)

	go func(c *exec.Cmd) {
		werr := c.Wait()
		s.mu.Lock()

		if s.cmd == c {
			s.cmd = nil
		}
		cb := s.onState
		s.mu.Unlock()

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

func (s *Service) Stop() error {
	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid

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

			} else {
				return nil
			}
		}
	}

	s.log("stopping (pid %d)...", pid)

	kill := exec.Command("taskkill", "/F", "/T", "/PID", fmt.Sprint(pid))
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if err := kill.Run(); err != nil {

		if err2 := cmd.Process.Kill(); err2 != nil {
			return fmt.Errorf("kill %s: taskkill=%v, proc.Kill=%v", s.Name, err, err2)
		}
	}

	return nil
}

func readPostmasterPID(dataDir string) (int, error) {
	pidFile := filepath.Join(dataDir, "postmaster.pid")
	var data []byte
	var err error

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

func isPortBusy(port int) bool {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return true
	}
	_ = ln.Close()

	time.Sleep(10 * time.Millisecond)
	return false
}

func streamToLog(r io.Reader, logf func(format string, a ...any)) {
	sc := bufio.NewScanner(r)

	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line != "" {
			logf("%s", line)
		}
	}
}

func (s *Service) tailPostgresLog(dataDir string, stopCh <-chan struct{}) {
	var logFilePath string
	currentLogfile := filepath.Join(dataDir, "current_logfiles")

	for i := 0; i < 20; i++ {
		select {
		case <-stopCh:
			return
		default:
		}

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

func (s *Service) logPostgresLine(line string) {

	idx := strings.Index(line, ":  ")
	if idx != -1 {
		prefix := line[:idx]
		msg := line[idx+3:]

		words := strings.Fields(prefix)
		if len(words) > 0 {
			level := words[len(words)-1]
			s.log("%s: %s", level, msg)
			return
		}
	}
	s.log("%s", line)
}
