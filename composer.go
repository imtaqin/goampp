//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const composerVersionsURL = "https://getcomposer.org/versions"

func composerDir(baseDir string) string {
	return filepath.Join(baseDir, "bin", "composer")
}

func composerPharPath(baseDir string) string {
	return filepath.Join(composerDir(baseDir), "composer.phar")
}

func legacyComposerPharPath(baseDir string) string {
	return filepath.Join(baseDir, "bin", "php", "composer.phar")
}

func composerVersionFileName(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return "composer-stable.phar"
	}
	return "composer-" + version + ".phar"
}

func resolveComposerLatest(log func(string)) (urlOut, fileName, stripTop, version string, err error) {
	log("  resolving latest Composer stable release ...")
	body, err := fetchReleaseText(composerVersionsURL)
	if err != nil {
		return "", "", "", "", err
	}

	var channels map[string][]struct {
		Path    string `json:"path"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(body), &channels); err != nil {
		return "", "", "", "", err
	}
	stable := channels["stable"]
	if len(stable) == 0 {
		return "", "", "", "", fmt.Errorf("Composer stable release missing")
	}

	best := stable[0]
	for _, candidate := range stable[1:] {
		if numericVersionGT(candidate.Version, best.Version) {
			best = candidate
		}
	}
	if best.Version == "" || best.Path == "" {
		return "", "", "", "", fmt.Errorf("Composer stable release metadata incomplete")
	}

	dlURL := best.Path
	if strings.HasPrefix(dlURL, "/") {
		dlURL = "https://getcomposer.org" + dlURL
	} else if !strings.HasPrefix(dlURL, "http://") && !strings.HasPrefix(dlURL, "https://") {
		dlURL = "https://getcomposer.org/" + strings.TrimLeft(dlURL, "/")
	}
	log("  latest Composer stable: " + best.Version)
	return dlURL, composerVersionFileName(best.Version), "", best.Version, nil
}

func composerPostInstall(installDir string, log func(string)) error {
	baseDir := filepath.Dir(filepath.Dir(installDir))
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return err
	}

	wrapper := "@echo off\r\n" +
		"setlocal\r\n" +
		"set \"GOAMPP_PHP=%~dp0..\\php\\php.exe\"\r\n" +
		"if not exist \"%GOAMPP_PHP%\" (\r\n" +
		"  echo GoAMPP PHP was not found at \"%GOAMPP_PHP%\". 1>&2\r\n" +
		"  exit /b 1\r\n" +
		")\r\n" +
		"\"%GOAMPP_PHP%\" \"%~dp0composer.phar\" %*\r\n"
	if err := os.WriteFile(filepath.Join(installDir, "composer.bat"), []byte(wrapper), 0o644); err != nil {
		return fmt.Errorf("write composer.bat: %w", err)
	}

	// Keep a tiny compatibility shim in bin/php for existing PATH entries.
	phpDir := filepath.Join(baseDir, "bin", "php")
	if fi, err := os.Stat(phpDir); err == nil && fi.IsDir() {
		shim := "@echo off\r\n" +
			"call \"%~dp0..\\composer\\composer.bat\" %*\r\n" +
			"exit /b %errorlevel%\r\n"
		if err := os.WriteFile(filepath.Join(phpDir, "composer.bat"), []byte(shim), 0o644); err != nil {
			return fmt.Errorf("write Composer compatibility shim: %w", err)
		}
	}

	if log != nil {
		log("  configured Composer wrapper")
	}
	return nil
}

// migrateLegacyComposer moves Composer out of bin/php so PHP upgrades and
// version switches cannot remove it. Existing PATH entries keep working via a
// small compatibility shim in bin/php.
func migrateLegacyComposer(baseDir string, log func(string)) error {
	targetDir := composerDir(baseDir)
	target := composerPharPath(baseDir)
	legacy := legacyComposerPharPath(baseDir)

	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}

	source := ""
	migrated := false
	if _, err := os.Stat(target); err != nil {
		switch {
		case composerFileExists(legacy):
			source = legacy
			migrated = true
		case composerFileExists(filepath.Join(baseDir, "downloads", "composer-stable.phar")):
			source = filepath.Join(baseDir, "downloads", "composer-stable.phar")
			migrated = true
		}
		if source != "" {
			if err := copyFile(source, target); err != nil {
				return fmt.Errorf("migrate Composer: %w", err)
			}
		}
	}

	if !composerFileExists(target) {
		return nil
	}
	if err := composerPostInstall(targetDir, log); err != nil {
		return err
	}

	if composerFileExists(legacy) {
		if err := os.Remove(legacy); err != nil {
			return fmt.Errorf("remove legacy Composer PHAR: %w", err)
		}
	}
	if migrated && log != nil {
		log("composer: moved managed files from bin/php to bin/composer")
	}

	// Make the standalone Composer directory available to new terminals. The
	// compatibility shim above keeps existing bin/php PATH entries working too.
	if app != nil {
		if n, err := AddGoamppToUserPath(); err != nil {
			if log != nil {
				log("composer: PATH update skipped: " + err.Error())
			}
		} else if n > 0 && log != nil {
			log(fmt.Sprintf("composer: added %d GoAMPP bin dir(s) to user PATH", n))
		}
	}
	return nil
}

func composerFileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

func init() {
	spec, ok := DownloadCatalog["Composer"]
	if !ok {
		return
	}

	// Composer is its own tool. Keeping it in bin/php made PHP updates and
	// version switches capable of removing the PHAR or wrapper.
	spec.InstallDir = "bin/composer"
	spec.TargetFile = "composer.phar"
	spec.CheckFile = "composer.phar"
	spec.PostInstall = composerPostInstall
	DownloadCatalog["Composer"] = spec
	registerLatestResolver("Composer", resolveComposerLatest)
}
