//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type updateFileSnapshot struct {
	path string
	data []byte
	mode os.FileMode
}

func availableServiceUpdates() []ServiceUpdateInfo {
	updateStateMu.RLock()
	defer updateStateMu.RUnlock()

	out := make([]ServiceUpdateInfo, 0)
	for _, ms := range app.services {
		info, ok := serviceUpdates[ms.Conf.Name]
		if !ok || !info.Installed || !info.Available || info.Err != nil {
			continue
		}
		out = append(out, info)
	}
	return out
}

func applyServiceUpdates(names []string) {
	if !updateOperationMu.TryLock() {
		app.appendLog("updates: another package operation is already in progress")
		if app.wnd != nil {
			app.wnd.UiThread(func() { setUpdateApplyBusy(false) })
		}
		return
	}
	defer updateOperationMu.Unlock()

	updated := 0
	failed := 0
	for _, name := range names {
		if err := applyServiceUpdate(name); err != nil {
			failed++
			app.appendLog(fmt.Sprintf("updates: %s: %v", name, err))
			continue
		}
		updated++
	}

	if app.wnd != nil {
		app.wnd.UiThread(func() {
			refreshServiceList()
			applyServiceUpdateDecorations()
			setUpdateApplyBusy(false)
			if app.statusBar != nil {
				switch {
				case failed > 0 && updated > 0:
					app.statusBar.Part(0).SetText(fmt.Sprintf("Updated %d package(s), %d failed", updated, failed))
				case failed > 0:
					app.statusBar.Part(0).SetText(fmt.Sprintf("Update failed for %d package(s)", failed))
				default:
					app.statusBar.Part(0).SetText(fmt.Sprintf("Updated %d package(s)", updated))
				}
			}
		})
	}
}

func applyServiceUpdate(name string) error {
	ms := app.findService(name)
	if ms == nil {
		return fmt.Errorf("service is no longer configured")
	}

	info, ok := cachedServiceUpdate(name)
	if !ok || !info.Installed || !info.Available || info.Err != nil {
		return fmt.Errorf("no resolved update is available")
	}
	if info.Latest.URL == "" || info.Latest.FileName == "" {
		return fmt.Errorf("resolved update package is incomplete")
	}

	stopped, err := stopServicesForPackageUpdate(ms)
	if err != nil {
		restartServicesAfterPackageUpdate(stopped)
		return err
	}
	defer restartServicesAfterPackageUpdate(stopped)

	variant := ""
	if spec, ok := DownloadCatalog[name]; ok && len(spec.Variants) > 0 {
		variant = effectiveVariant(name, ms.Conf.ActiveVersion)
	}

	preserve := preservedUpdateFiles(ms)
	app.appendLog(fmt.Sprintf("[%s] updating %s → %s", name,
		compactServiceVersion(name, info.Current.Version),
		compactServiceVersion(name, info.Latest.Version)))
	if err := updatePackageFromResolution(name, variant, app.baseDir, info.Latest, preserve, app.appendLog, uiDownloadProgress); err != nil {
		return err
	}

	if err := writeInstalledPackage(app.baseDir, name, variant, info.Latest); err != nil {
		return fmt.Errorf("record installed package: %w", err)
	}
	markServiceUpdated(name, variant, info.Latest)
	return nil
}

func markServiceUpdated(name, variant string, latest DownloadResolution) {
	now := time.Now()
	meta := InstalledPackage{
		Name:        name,
		Variant:     variant,
		Version:     latest.Version,
		PackageID:   latest.PackageID,
		FileName:    latest.FileName,
		URL:         latest.URL,
		InstalledAt: now,
	}

	updateStateMu.Lock()
	serviceUpdates[name] = ServiceUpdateInfo{
		Name:      name,
		Installed: true,
		Current:   meta,
		Latest:    latest,
		Available: false,
	}
	updateStateMu.Unlock()
}

func stopServicesForPackageUpdate(ms *ManagedService) ([]*ManagedService, error) {
	candidates := []*ManagedService{ms}
	if ms.Conf.Name == "Erlang" {
		if rabbit := app.findService("RabbitMQ"); rabbit != nil {
			candidates = append(candidates, rabbit)
		}
	}

	stopped := make([]*ManagedService, 0, len(candidates))
	seen := map[*ManagedService]bool{}
	for _, candidate := range candidates {
		if candidate == nil || candidate.Service == nil || seen[candidate] || !candidate.Service.Running() {
			continue
		}
		seen[candidate] = true
		app.appendLog(fmt.Sprintf("[%s] stopping for package update", candidate.Conf.Name))
		if err := candidate.Service.Stop(); err != nil {
			return stopped, fmt.Errorf("stop %s: %w", candidate.Conf.Name, err)
		}
		stopped = append(stopped, candidate)
	}

	deadline := time.Now().Add(10 * time.Second)
	for _, candidate := range stopped {
		for candidate.Service.Running() && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		if candidate.Service.Running() {
			return stopped, fmt.Errorf("%s did not stop in time", candidate.Conf.Name)
		}
	}
	return stopped, nil
}

func restartServicesAfterPackageUpdate(services []*ManagedService) {
	for i := len(services) - 1; i >= 0; i-- {
		ms := services[i]
		if ms == nil || ms.Service == nil || ms.Service.Running() {
			continue
		}
		if err := ms.Service.Start(); err != nil {
			app.appendLog(fmt.Sprintf("[%s] restart after update: %v", ms.Conf.Name, err))
		} else {
			app.appendLog(fmt.Sprintf("[%s] restarted after update", ms.Conf.Name))
		}
	}
}

func preservedUpdateFiles(ms *ManagedService) []string {
	var paths []string
	if ms != nil && ms.Conf.ConfigFile != "" {
		paths = append(paths, ExpandPath(ms.Conf.ConfigFile, app.baseDir))
	}
	if ms != nil && ms.Conf.Name == "phpMyAdmin" {
		paths = append(paths, filepath.Join(app.baseDir, "www", "phpmyadmin", "config.inc.php"))
	}
	return paths
}

func captureUpdateFiles(paths []string) ([]updateFileSnapshot, error) {
	seen := map[string]bool{}
	snapshots := make([]updateFileSnapshot, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		clean := filepath.Clean(path)
		key := strings.ToLower(clean)
		if seen[key] {
			continue
		}
		seen[key] = true

		info, err := os.Stat(clean)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			continue
		}
		data, err := os.ReadFile(clean)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, updateFileSnapshot{path: clean, data: data, mode: info.Mode()})
	}
	return snapshots, nil
}

func restoreUpdateFiles(snapshots []updateFileSnapshot) error {
	for _, snapshot := range snapshots {
		if err := os.MkdirAll(filepath.Dir(snapshot.path), 0o755); err != nil {
			return err
		}
		mode := snapshot.mode.Perm()
		if mode == 0 {
			mode = 0o644
		}
		if err := os.WriteFile(snapshot.path, snapshot.data, mode); err != nil {
			return err
		}
	}
	return nil
}

func updatePackageFromResolution(name, variant, baseDir string, resolved DownloadResolution, preserve []string, log func(string), progress ProgressFunc) error {
	spec, ok := DownloadCatalog[name]
	if !ok {
		return fmt.Errorf("no download info registered for %q", name)
	}
	if resolved.URL == "" || resolved.FileName == "" {
		return fmt.Errorf("[%s] resolved package is incomplete", name)
	}
	if progress == nil {
		progress = NopProgress
	}

	notes := spec.Notes
	if len(spec.Variants) > 0 && variant != "" {
		found := false
		for _, v := range spec.Variants {
			if v.Version != variant {
				continue
			}
			found = true
			if v.Notes != "" {
				notes = v.Notes
			}
			break
		}
		if !found {
			return fmt.Errorf("[%s] version %q not in catalogue", name, variant)
		}
	}

	downloadMu.Lock()
	defer downloadMu.Unlock()

	canonicalDir := filepath.Join(baseDir, filepath.FromSlash(spec.InstallDir))
	installDir := canonicalDir
	if len(spec.Variants) > 0 && variant != "" {
		installDir = canonicalDir + "-" + variant
	}

	snapshots, err := captureUpdateFiles(preserve)
	if err != nil {
		return fmt.Errorf("preserve configuration: %w", err)
	}
	restored := false
	defer func() {
		if restored || len(snapshots) == 0 {
			return
		}
		if err := restoreUpdateFiles(snapshots); err != nil {
			log(fmt.Sprintf("[%s] configuration restore after failed update: %v", name, err))
		}
	}()

	progress("starting", name, 0, 0)
	log(fmt.Sprintf("[%s] %s — starting update", name, resolved.Version))
	if notes != "" {
		log("  note: " + notes)
	}

	dlDir := filepath.Join(baseDir, "downloads")
	if err := os.MkdirAll(dlDir, 0o755); err != nil {
		progress("idle", name, 0, 0)
		return fmt.Errorf("create downloads dir: %w", err)
	}
	dlPath := filepath.Join(dlDir, resolved.FileName)
	// Updates always re-download the resolved package. This matters for stable
	// URLs such as MinIO and Composer whose filename does not change per release.
	if err := httpDownload(resolved.URL, dlPath, log, func(done, total int64) {
		progress("downloading", name, done, total)
	}); err != nil {
		progress("idle", name, 0, 0)
		return fmt.Errorf("download: %w", err)
	}

	if err := ensureInstallDir(installDir); err != nil {
		progress("idle", name, 0, 0)
		return fmt.Errorf("create install dir: %w", err)
	}

	switch spec.Kind {
	case "zip":
		log(fmt.Sprintf("  extracting update into %s", installDir))
		progress("extracting", name, 0, 0)
		if err := extractZip(dlPath, installDir, resolved.StripTop, func(done, total int64) {
			progress("extracting", name, done, total)
		}); err != nil {
			progress("idle", name, 0, 0)
			return fmt.Errorf("extract: %w", err)
		}
	case "file":
		target := spec.TargetFile
		if target == "" {
			target = resolved.FileName
		}
		log(fmt.Sprintf("  replacing %s/%s", installDir, target))
		if err := copyFile(dlPath, filepath.Join(installDir, target)); err != nil {
			progress("idle", name, 0, 0)
			return fmt.Errorf("copy: %w", err)
		}
	case "exe":
		log(fmt.Sprintf("  running silent updater → %s (this may take 30–60s)", installDir))
		progress("post-install", name, 0, 0)
		absInstallDir, _ := filepath.Abs(installDir)
		cmd := exec.Command(dlPath, "/S", "/D="+absInstallDir)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
		if out, err := cmd.CombinedOutput(); err != nil {
			log("  installer output: " + strings.TrimSpace(string(out)))
			progress("idle", name, 0, 0)
			return fmt.Errorf("silent installer: %w", err)
		}
	default:
		progress("idle", name, 0, 0)
		return fmt.Errorf("unknown kind: %q", spec.Kind)
	}

	if installDir != canonicalDir {
		if err := mergeVariantUpdate(canonicalDir, installDir, log); err != nil {
			progress("idle", name, 0, 0)
			return err
		}
	}

	if err := restoreUpdateFiles(snapshots); err != nil {
		progress("idle", name, 0, 0)
		return fmt.Errorf("restore configuration: %w", err)
	}
	restored = true

	if spec.PostInstall != nil {
		progress("post-install", name, 0, 0)
		if err := spec.PostInstall(canonicalDir, log); err != nil {
			progress("idle", name, 0, 0)
			return fmt.Errorf("post-install: %w", err)
		}
	}

	if spec.CheckFile != "" {
		if _, err := os.Stat(filepath.Join(canonicalDir, filepath.FromSlash(spec.CheckFile))); err != nil {
			progress("idle", name, 0, 0)
			return fmt.Errorf("updated package validation: %w", err)
		}
	}

	log(fmt.Sprintf("[%s] update complete", name))
	if n, err := AddGoamppToUserPath(); err != nil {
		log(fmt.Sprintf("[%s] PATH update skipped: %v", name, err))
	} else if n > 0 {
		log(fmt.Sprintf("[%s] added %d bin dir(s) to user PATH — open a new terminal to use them", name, n))
	}

	progress("done", name, 0, 0)
	time.Sleep(500 * time.Millisecond)
	progress("idle", name, 0, 0)
	return nil
}

// mergeVariantUpdate refreshes the active branch without mirroring/deleting
// files that users installed into the canonical runtime directory (npm globals,
// pip packages, php.ini, and similar local state).
func mergeVariantUpdate(canonical, target string, log func(string)) error {
	if fi, err := os.Lstat(canonical); err == nil {
		if fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			if err := os.Remove(canonical); err != nil {
				return fmt.Errorf("remove old junction: %w", err)
			}
		}
	}
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		return fmt.Errorf("create canonical dir: %w", err)
	}

	cmd := exec.Command("robocopy", target, canonical,
		"/E", "/NJH", "/NJS", "/NFL", "/NDL", "/NP", "/R:1", "/W:1")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	out, err := cmd.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code := exitErr.ExitCode()
			if code >= 0 && code < 8 {
				err = nil
			}
		}
	}
	if err != nil {
		return fmt.Errorf("robocopy update %s → %s: %v: %s",
			target, canonical, err, strings.TrimSpace(string(out)))
	}
	log(fmt.Sprintf("  refreshed active version from %s (preserved local extras)", filepath.Base(target)))
	return nil
}
