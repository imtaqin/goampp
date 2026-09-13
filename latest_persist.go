//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const latestResolutionCacheSchema = 1

type persistedDownloadResolution struct {
	Version   string `json:"version"`
	PackageID string `json:"package_id,omitempty"`
	URL       string `json:"url,omitempty"`
	FileName  string `json:"file_name,omitempty"`
	StripTop  string `json:"strip_top,omitempty"`
}

type persistedLatestCatalog struct {
	Schema   int                                                `json:"schema"`
	SavedAt  time.Time                                          `json:"saved_at"`
	Services map[string]persistedDownloadResolution              `json:"services,omitempty"`
	Variants map[string]map[string]persistedDownloadResolution   `json:"variants,omitempty"`
}

var (
	persistedLatestMu       sync.RWMutex
	persistedLatestServices = map[string]DownloadResolution{}
)

func latestResolutionCachePath(baseDir string) string {
	return filepath.Join(baseDir, "data", ".goampp", "latest.json")
}

func persistedResolution(r DownloadResolution) persistedDownloadResolution {
	return persistedDownloadResolution{
		Version:   r.Version,
		PackageID: r.PackageID,
		URL:       r.URL,
		FileName:  r.FileName,
		StripTop:  r.StripTop,
	}
}

func downloadResolution(r persistedDownloadResolution) DownloadResolution {
	return DownloadResolution{
		Version:   r.Version,
		PackageID: r.PackageID,
		URL:       r.URL,
		FileName:  r.FileName,
		StripTop:  r.StripTop,
	}
}

func cachedPersistedLatest(name string) (DownloadResolution, bool) {
	persistedLatestMu.RLock()
	defer persistedLatestMu.RUnlock()
	resolved, ok := persistedLatestServices[name]
	return resolved, ok
}

func applyVariantResolutionsToCatalog(resolutions map[variantKey]DownloadResolution) {
	if len(resolutions) == 0 {
		return
	}

	nextCatalog := make(map[string]DownloadSpec, len(DownloadCatalog))
	for name, spec := range DownloadCatalog {
		if len(spec.Variants) > 0 {
			spec.Variants = append([]VariantSpec(nil), spec.Variants...)
		}
		nextCatalog[name] = spec
	}

	for _, serviceName := range []string{"PHP-FPM", "Node.js", "Python"} {
		spec, ok := nextCatalog[serviceName]
		if !ok {
			continue
		}
		changed := false
		for i := range spec.Variants {
			key := variantKey{Name: serviceName, Variant: spec.Variants[i].Version}
			resolved, ok := resolutions[key]
			if !ok {
				continue
			}
			spec.Variants[i].URL = resolved.URL
			spec.Variants[i].FileName = resolved.FileName
			spec.Variants[i].StripTop = resolved.StripTop
			changed = true
		}
		if changed {
			nextCatalog[serviceName] = spec
		}
	}
	DownloadCatalog = nextCatalog
}

func loadPersistedLatestCatalog(baseDir string) error {
	data, err := os.ReadFile(latestResolutionCachePath(baseDir))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}

	var snapshot persistedLatestCatalog
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return err
	}
	if snapshot.Schema != latestResolutionCacheSchema {
		return nil
	}

	services := make(map[string]DownloadResolution, len(snapshot.Services))
	for name, saved := range snapshot.Services {
		resolved := downloadResolution(saved)
		if resolved.Version == "" {
			continue
		}
		services[name] = resolved
	}
	persistedLatestMu.Lock()
	persistedLatestServices = services
	persistedLatestMu.Unlock()

	loadedVariants := map[variantKey]DownloadResolution{}
	for name, variants := range snapshot.Variants {
		for variant, saved := range variants {
			resolved := downloadResolution(saved)
			if resolved.Version == "" {
				continue
			}
			loadedVariants[variantKey{Name: name, Variant: variant}] = resolved
		}
	}
	if len(loadedVariants) > 0 {
		variantStateMu.Lock()
		for key, resolved := range loadedVariants {
			latestVariants[key] = resolved
		}
		variantStateMu.Unlock()
		applyVariantResolutionsToCatalog(loadedVariants)
	}
	return nil
}

func persistLatestCatalog(baseDir string, fresh map[string]ServiceUpdateInfo) error {
	persistedLatestMu.Lock()
	for name, info := range fresh {
		if isDynamicVariantService(name) || info.Err != nil || info.Latest.Version == "" {
			continue
		}
		persistedLatestServices[name] = info.Latest
	}
	services := make(map[string]DownloadResolution, len(persistedLatestServices))
	for name, resolved := range persistedLatestServices {
		services[name] = resolved
	}
	persistedLatestMu.Unlock()

	variants := map[variantKey]DownloadResolution{}
	variantStateMu.RLock()
	for key, resolved := range latestVariants {
		if resolved.Version != "" {
			variants[key] = resolved
		}
	}
	variantStateMu.RUnlock()

	snapshot := persistedLatestCatalog{
		Schema:   latestResolutionCacheSchema,
		SavedAt:  time.Now(),
		Services: make(map[string]persistedDownloadResolution, len(services)),
		Variants: map[string]map[string]persistedDownloadResolution{},
	}
	for name, resolved := range services {
		snapshot.Services[name] = persistedResolution(resolved)
	}
	for key, resolved := range variants {
		if snapshot.Variants[key.Name] == nil {
			snapshot.Variants[key.Name] = map[string]persistedDownloadResolution{}
		}
		snapshot.Variants[key.Name][key.Variant] = persistedResolution(resolved)
	}

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	path := latestResolutionCachePath(baseDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	_ = os.Remove(path)
	return os.Rename(tmp, path)
}
