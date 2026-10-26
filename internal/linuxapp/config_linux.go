//go:build linux

package linuxapp

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// atomicWriteLinuxFile replaces a file via a temporary file in the same
// directory. The caller chooses the permissions; parent directories are
// created as private user-owned directories when absent.
func atomicWriteLinuxFile(path string, data []byte, mode os.FileMode) error {
	path = filepath.Clean(path)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}
	if mode.Perm() == 0 {
		mode = 0o600
	}
	tmp, err := os.CreateTemp(dir, ".goampp-write-*")
	if err != nil {
		return fmt.Errorf("create temporary file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode.Perm()); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

func linuxConfigCandidates() []string {
	candidates := []string{
		"/etc/apache2/apache2.conf",
		"/etc/apache2/ports.conf",
		"/etc/httpd/conf/httpd.conf",
		"/etc/nginx/nginx.conf",
		"/etc/nginx/sites-available/default",
		"/etc/caddy/Caddyfile",
		"/etc/mysql/my.cnf",
		"/etc/my.cnf",
		"/etc/mysql/mariadb.conf.d/50-server.cnf",
		"/etc/redis/redis.conf",
		"/etc/redis.conf",
		"/etc/memcached.conf",
		"/etc/rabbitmq/rabbitmq.conf",
		"/etc/rabbitmq/advanced.config",
		"/etc/docker/daemon.json",
		"/etc/postgresql/postgresql.conf",
		"/var/lib/pgsql/data/postgresql.conf",
		"/etc/hosts",
		linuxVHostsPath(),
	}
	for _, pattern := range []string{
		"/etc/apache2/sites-available/*.conf",
		"/etc/apache2/conf-available/*.conf",
		"/etc/httpd/conf.d/*.conf",
		"/etc/nginx/sites-available/*",
		"/etc/nginx/conf.d/*.conf",
		"/etc/postgresql/*/main/postgresql.conf",
		"/etc/postgresql/*/main/pg_hba.conf",
		"/etc/mysql/mariadb.conf.d/*.cnf",
		"/etc/mysql/mysql.conf.d/*.cnf",
		"/etc/php/*/fpm/php.ini",
		"/etc/php/*/cli/php.ini",
		"/etc/php.ini",
		"/etc/php-fpm.conf",
		"/etc/php-fpm.d/www.conf",
	} {
		matches, _ := filepath.Glob(pattern)
		candidates = append(candidates, matches...)
	}
	seen := make(map[string]struct{})
	var existing []string
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		info, err := os.Stat(candidate)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		existing = append(existing, candidate)
	}
	sort.Strings(existing)
	return existing
}

func saveLinuxConfigFile(path string, data []byte) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect config file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("refusing to replace a symbolic link; open its target file instead")
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("only regular text files can be saved")
	}
	old, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read current config before saving: %w", err)
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil || cacheDir == "" {
		return "", fmt.Errorf("user cache directory is unavailable")
	}
	backupDir := filepath.Join(cacheDir, "goampp", "config-backups")
	backupName := time.Now().Format("20060102-150405.000000000") + "-" + filepath.Base(path) + ".bak"
	backupPath := filepath.Join(backupDir, backupName)
	if err := atomicWriteLinuxFile(backupPath, old, 0o600); err != nil {
		return "", fmt.Errorf("create backup before saving: %w", err)
	}
	if err := atomicWriteLinuxFile(path, data, info.Mode().Perm()); err != nil {
		return backupPath, err
	}
	return backupPath, nil
}

func buildLinuxConfigEditorPage(w fyne.Window) fyne.CanvasObject {
	files := widget.NewSelect(nil, nil)
	files.PlaceHolder = "Select a config file"
	pathLabel := widget.NewLabel("No file loaded")
	pathLabel.Wrapping = fyne.TextWrapBreak
	status := widget.NewLabel("System files may require administrator permissions. Saves create a backup in your user cache first.")
	status.Wrapping = fyne.TextWrapWord
	editor := widget.NewMultiLineEntry()
	editor.TextStyle = fyne.TextStyle{Monospace: true}
	editor.Wrapping = fyne.TextWrapOff
	editor.SetPlaceHolder("Open a configuration file to begin editing…")
	var currentPath string
	var loading bool
	var dirty bool
	var candidates []string
	var labels []string

	setStatus := func(message string) {
		status.SetText(message)
	}
	refreshOptions := func() {
		candidates = linuxConfigCandidates()
		labels = make([]string, 0, len(candidates))
		for _, path := range candidates {
			labels = append(labels, filepath.Base(path)+"  ·  "+path)
		}
		files.SetOptions(labels)
		if currentPath != "" {
			for i, candidate := range candidates {
				if candidate == currentPath {
					files.SetSelected(labels[i])
					return
				}
			}
			files.ClearSelected()
		}
	}
	loadPath := func(path string) {
		data, err := os.ReadFile(path)
		if err != nil {
			dialog.ShowError(fmt.Errorf("open %s: %w", path, err), w)
			return
		}
		if len(data) > 4*1024*1024 {
			dialog.ShowError(fmt.Errorf("file is larger than 4 MiB; use a dedicated editor for large files"), w)
			return
		}
		if strings.IndexByte(string(data), 0) >= 0 {
			dialog.ShowError(fmt.Errorf("this file contains binary data and is not suitable for the text editor"), w)
			return
		}
		loading = true
		editor.SetText(strings.ReplaceAll(string(data), "\r\n", "\n"))
		loading = false
		currentPath = filepath.Clean(path)
		dirty = false
		pathLabel.SetText(currentPath)
		setStatus(fmt.Sprintf("Loaded %d bytes. Saving creates a timestamped backup under the GoAMPP user cache.", len(data)))
		refreshOptions()
		linuxAddLog("Config editor loaded " + currentPath)
	}
	requestLoadPath := func(nextPath string) {
		if nextPath == "" || filepath.Clean(nextPath) == currentPath {
			return
		}
		if dirty && currentPath != "" {
			dialog.ShowConfirm("Discard unsaved changes?", "Switching files will discard unsaved edits to "+currentPath+". Save first if you need to keep them.", func(ok bool) {
				if ok {
					loadPath(nextPath)
				} else {
					refreshOptions()
				}
			}, w)
			return
		}
		loadPath(nextPath)
	}
	files.OnChanged = func(label string) {
		for i, option := range labels {
			if option == label && i < len(candidates) && candidates[i] != currentPath {
				requestLoadPath(candidates[i])
				return
			}
		}
	}
	editor.OnChanged = func(string) {
		if !loading && currentPath != "" {
			dirty = true
			setStatus("Unsaved changes · a backup will be created before saving")
		}
	}
	openButton := widget.NewButtonWithIcon("Open file…", theme.FolderOpenIcon(), func() {
		d := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if err != nil {
				dialog.ShowError(err, w)
				return
			}
			if reader == nil {
				return
			}
			path := reader.URI().Path()
			_ = reader.Close()
			requestLoadPath(path)
		}, w)
		d.Show()
	})
	reloadButton := widget.NewButtonWithIcon("Reload", theme.ViewRefreshIcon(), func() {
		if currentPath == "" {
			refreshOptions()
			return
		}
		if dirty {
			dialog.ShowConfirm("Discard changes?", "Reloading will discard unsaved edits.", func(ok bool) {
				if ok {
					loadPath(currentPath)
				}
			}, w)
			return
		}
		loadPath(currentPath)
	})
	openExternalButton := widget.NewButtonWithIcon("External editor", theme.FolderOpenIcon(), func() {
		if currentPath == "" {
			return
		}
		if err := openExternal(currentPath); err != nil {
			dialog.ShowError(err, w)
		}
	})
	saveButton := widget.NewButtonWithIcon("Save", theme.DocumentSaveIcon(), func() {
		if currentPath == "" {
			dialog.ShowInformation("Config editor", "Choose or open a file before saving.", w)
			return
		}
		backup, err := saveLinuxConfigFile(currentPath, []byte(strings.ReplaceAll(editor.Text, "\r\n", "\n")))
		if err != nil {
			setStatus("Save failed: " + err.Error())
			dialog.ShowError(err, w)
			linuxAddLog("Config editor save failed: " + err.Error())
			return
		}
		dirty = false
		setStatus("Saved. Backup: " + backup)
		linuxAddLog("Config editor saved " + currentPath + " (backup: " + backup + ")")
	})
	top := container.NewBorder(
		nil, nil,
		container.NewHBox(files, openButton),
		container.NewHBox(reloadButton, saveButton, openExternalButton),
		pathLabel,
	)
	body := linuxSurface(container.NewBorder(
		container.NewVBox(top, linuxVSpace(6), status),
		nil, nil, nil,
		editor,
	), 10, 10, 12, 12)
	refreshOptions()
	return container.NewBorder(
		nil, nil, nil, nil,
		linuxPad(container.NewVBox(
			linuxSurface(container.NewHBox(
				linuxBoldText("Configuration editor", 14),
				linuxHSpace(8),
				linuxMutedText("Read and edit local text configuration files.", 11.5),
			), 8, 8, 10, 10),
			linuxVSpace(8),
			body,
		), 0, 10, 0, 0),
	)
}
