//go:build windows

package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

const hostsHelperArg = "--goampp-apply-hosts"

// handleElevatedHostsHelper handles the short-lived elevated helper mode used
// to update the Windows hosts file without running the whole UI as admin.
func handleElevatedHostsHelper() bool {
	if len(os.Args) < 5 || os.Args[1] != hostsHelperArg {
		return false
	}

	payloadPath := os.Args[2]
	targetPath := os.Args[3]
	resultPath := os.Args[4]

	data, err := os.ReadFile(payloadPath)
	if err == nil {
		err = writeHostsInPlace(targetPath, data)
	}
	if err == nil {
		err = verifyHostsManagedBlock(targetPath, data)
	}

	result := "ok"
	if err != nil {
		result = "error: " + err.Error()
	}
	_ = os.WriteFile(resultPath, []byte(result), 0o600)
	return true
}

func applyHostsFile(path string, data []byte) error {
	if IsElevated() {
		if err := writeHostsInPlace(path, data); err != nil {
			return err
		}
		return verifyHostsManagedBlock(path, data)
	}

	payload, err := os.CreateTemp("", "goampp-hosts-*.txt")
	if err != nil {
		return err
	}
	payloadPath := payload.Name()
	if _, err := payload.Write(data); err != nil {
		payload.Close()
		_ = os.Remove(payloadPath)
		return err
	}
	if err := payload.Close(); err != nil {
		_ = os.Remove(payloadPath)
		return err
	}
	resultPath := payloadPath + ".result"

	exe, err := os.Executable()
	if err != nil {
		_ = os.Remove(payloadPath)
		return err
	}
	args := []string{hostsHelperArg, payloadPath, path, resultPath}
	parts := make([]string, len(args))
	for i, arg := range args {
		parts[i] = syscall.EscapeArg(arg)
	}
	params, _ := syscall.UTF16PtrFromString(strings.Join(parts, " "))
	verb, _ := syscall.UTF16PtrFromString("runas")
	file, _ := syscall.UTF16PtrFromString(exe)
	if err := windows.ShellExecute(0, verb, file, params, nil, windows.SW_HIDE); err != nil {
		_ = os.Remove(payloadPath)
		return fmt.Errorf("UAC hosts update: %w", err)
	}

	// Do not block the UI while the UAC prompt/helper is active. The helper
	// reports completion through a tiny result file; verify the managed block
	// again from the unelevated process before reporting success.
	go func() {
		defer os.Remove(payloadPath)
		defer os.Remove(resultPath)

		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			result, err := os.ReadFile(resultPath)
			if err == nil {
				msg := strings.TrimSpace(string(result))
				if msg == "ok" {
					if err := verifyHostsManagedBlock(path, data); err != nil {
						if app != nil {
							app.appendLog("hosts: verification failed: " + err.Error())
						}
						return
					}
					if app != nil {
						app.appendLog("hosts: elevated update verified")
					}
					return
				}
				if app != nil {
					app.appendLog("hosts helper: " + msg)
				}
				return
			}
			time.Sleep(250 * time.Millisecond)
		}
		if app != nil {
			app.appendLog("hosts helper: timed out waiting for elevated write")
		}
	}()

	return nil
}

// writeHostsInPlace intentionally does not replace the hosts file with a
// renamed temporary file. Opening the existing file with O_TRUNC preserves
// the Windows file object/ACLs better than delete+rename in a protected system
// directory.
func writeHostsInPlace(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if os.IsNotExist(err) {
		f, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	}
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func verifyHostsManagedBlock(path string, desired []byte) error {
	actual, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	want := extractManagedBlock(desired, hostsMarkerBegin, hostsMarkerEnd)
	if len(want) == 0 {
		return fmt.Errorf("generated hosts block is empty")
	}
	got := extractManagedBlock(actual, hostsMarkerBegin, hostsMarkerEnd)
	if !bytes.Equal(normalizeHostBlock(got), normalizeHostBlock(want)) {
		return fmt.Errorf("managed hosts block did not match after write")
	}
	return nil
}

func extractManagedBlock(src []byte, begin, end string) []byte {
	text := strings.ReplaceAll(string(src), "\r\n", "\n")
	start := strings.Index(text, begin)
	if start < 0 {
		return nil
	}
	rest := text[start:]
	stop := strings.Index(rest, end)
	if stop < 0 {
		return nil
	}
	stop += len(end)
	return []byte(rest[:stop])
}

func normalizeHostBlock(src []byte) []byte {
	return bytes.TrimSpace(bytes.ReplaceAll(src, []byte("\r\n"), []byte("\n")))
}
