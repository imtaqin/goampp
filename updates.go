//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type InstalledPackage struct {
	Name        string    `json:"name"`
	Variant     string    `json:"variant,omitempty"`
	Version     string    `json:"version"`
	PackageID   string    `json:"package_id"`
	FileName    string    `json:"file_name"`
	URL         string    `json:"url,omitempty"`
	InstalledAt time.Time `json:"installed_at"`
	Legacy      bool      `json:"legacy,omitempty"`
}

type ServiceUpdateInfo struct {
	Name      string
	Installed bool
	Current   InstalledPackage
	Latest    DownloadResolution
	Available bool
	Err       error
}

var (
	packageMetaMu   sync.Mutex
	updateRefreshMu sync.Mutex
	updateStateMu   sync.RWMutex
	serviceUpdates  = map[string]ServiceUpdateInfo{}
)

var packageSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

func packageMetadataPath(baseDir, name string) string {
	slug := strings.Trim(packageSlugRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	return filepath.Join(baseDir, "data", ".goampp", "packages", slug+".json")
}

func writeInstalledPackage(baseDir, name, variant string, resolved DownloadResolution) error {
	packageMetaMu.Lock()
	defer packageMetaMu.Unlock()

	meta := InstalledPackage{
		Name:        name,
		Variant:     variant,
		Version:     resolved.Version,
		PackageID:   resolved.PackageID,
		FileName:    resolved.FileName,
		URL:         resolved.URL,
		InstalledAt: time.Now(),
	}

	path := packageMetadataPath(baseDir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func readInstalledPackage(baseDir, name, variant string) (InstalledPackage, bool) {
	if !IsInstalled(name, baseDir) {
		return InstalledPackage{}, false
	}

	// These services expose their exact installed version themselves. Prefer
	// that over metadata/catalogue guesses, particularly after branch aliases
	// such as PHP 8.4 or Node 22 have moved to a newer patch release.
	if isDynamicVariantService(name) {
		if version, ok := detectInstalledRuntimeVersion(baseDir, name); ok {
			return InstalledPackage{
				Name:      name,
				Variant:   effectiveVariant(name, variant),
				Version:   version,
				PackageID: packageIdentity(version, ""),
				Legacy:    true,
			}, true
		}
	}

	path := packageMetadataPath(baseDir, name)
	data, err := os.ReadFile(path)
	if err == nil {
		var meta InstalledPackage
		if json.Unmarshal(data, &meta) == nil && meta.Name != "" {
			return meta, true
		}
	}

	// Migration path for installs created before package metadata existed.
	// GoAMPP historically installed exactly the package named in the catalogue,
	// so use that as a best-effort baseline until the component is reinstalled.
	spec, ok := DownloadCatalog[name]
	if !ok {
		return InstalledPackage{}, false
	}

	meta := InstalledPackage{
		Name:      name,
		Variant:   variant,
		Version:   spec.Version,
		PackageID: packageIdentity(spec.Version, spec.FileName),
		FileName:  spec.FileName,
		URL:       spec.URL,
		Legacy:    true,
	}
	if len(spec.Variants) > 0 && variant != "" {
		for _, v := range spec.Variants {
			if v.Version != variant {
				continue
			}
			meta.Version = v.Version
			meta.PackageID = packageIdentity(v.Version, v.FileName)
			meta.FileName = v.FileName
			meta.URL = v.URL
			break
		}
	}
	return meta, true
}

func checkServiceUpdate(name, variant, baseDir string, log func(string)) ServiceUpdateInfo {
	info := ServiceUpdateInfo{Name: name}

	// Resolve the upstream package even when the component is not installed.
	// The Services page uses this same result for its visible version labels.
	var (
		latest DownloadResolution
		err    error
	)
	if isDynamicVariantService(name) {
		var ok bool
		latest, ok = resolvedVariantDownload(name, variant)
		if !ok {
			err = fmt.Errorf("latest %s branch package not resolved", effectiveVariant(name, variant))
		}
	} else {
		latest, err = resolveLatestDownload(name, variant, log)
	}
	if err != nil {
		info.Err = err
		return info
	}
	info.Latest = latest

	current, installed := readInstalledPackage(baseDir, name, variant)
	if !installed {
		return info
	}
	info.Installed = true
	info.Current = current

	if isDynamicVariantService(name) {
		info.Available = strings.TrimSpace(current.Version) != strings.TrimSpace(latest.Version)
		return info
	}

	if current.PackageID == "" || latest.PackageID == "" {
		info.Err = fmt.Errorf("package identity unavailable")
		return info
	}
	info.Available = current.PackageID != latest.PackageID
	return info
}

func cachedServiceUpdate(name string) (ServiceUpdateInfo, bool) {
	updateStateMu.RLock()
	defer updateStateMu.RUnlock()
	info, ok := serviceUpdates[name]
	return info, ok
}

func refreshAllServiceUpdates() {
	if !updateRefreshMu.TryLock() {
		app.appendLog("updates: refresh already in progress")
		return
	}
	defer updateRefreshMu.Unlock()

	app.appendLog("updates: resolving latest package versions...")
	if app.statusBar != nil {
		app.wnd.UiThread(func() {
			app.statusBar.Part(0).SetText("Checking package versions...")
		})
	}

	if err := refreshVariantCatalog(true); err != nil {
		app.appendLog("updates: variant resolution: " + err.Error())
	}

	type updateRequest struct {
		name    string
		variant string
	}
	var requests []updateRequest
	for _, ms := range app.services {
		if _, ok := DownloadCatalog[ms.Conf.Name]; !ok {
			continue
		}
		requests = append(requests, updateRequest{
			name:    ms.Conf.Name,
			variant: ms.Conf.ActiveVersion,
		})
	}

	results := make(chan ServiceUpdateInfo, len(requests))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, req := range requests {
		req := req
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results <- checkServiceUpdate(req.name, req.variant, app.baseDir, app.appendLog)
		}()
	}
	wg.Wait()
	close(results)

	fresh := make(map[string]ServiceUpdateInfo, len(requests))
	available := 0
	installed := 0
	failed := 0
	for info := range results {
		fresh[info.Name] = info
		if info.Installed {
			installed++
		}
		if info.Available {
			available++
		}
		if info.Err != nil {
			failed++
			app.appendLog(fmt.Sprintf("updates: %s: %v", info.Name, info.Err))
		}
	}

	updateStateMu.Lock()
	serviceUpdates = fresh
	updateStateMu.Unlock()

	if err := persistLatestCatalog(app.baseDir, fresh); err != nil {
		app.appendLog("updates: save cached versions: " + err.Error())
	}

	app.appendLog(fmt.Sprintf("updates: resolved %d package(s), %d installed, %d update(s), %d error(s)",
		len(requests), installed, available, failed))

	if app.wnd != nil {
		app.wnd.UiThread(func() {
			refreshServiceList()
			applyServiceUpdateDecorations()
			setUpdateRefreshBusy(false)
			if app.statusBar != nil {
				switch {
				case failed > 0:
					app.statusBar.Part(0).SetText(fmt.Sprintf("Versions refreshed — %d error(s)", failed))
				case available == 0:
					app.statusBar.Part(0).SetText("Package versions refreshed")
				default:
					app.statusBar.Part(0).SetText(fmt.Sprintf("%d installed update(s)", available))
				}
			}
		})
	}
}
