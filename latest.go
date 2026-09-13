//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type DownloadResolution struct {
	Version   string
	PackageID string
	URL       string
	FileName  string
	StripTop  string
}

type LatestResolver func(log func(string)) (url, fileName, stripTop, version string, err error)

type latestCacheEntry struct {
	URL       string
	FileName  string
	StripTop  string
	Version   string
	CheckedAt time.Time
}

var (
	latestCacheMu sync.Mutex
	latestCache   = map[string]latestCacheEntry{}

	installResolveMu     sync.Mutex
	resolvedFreshInstall = map[string]bool{}
)

const latestCacheTTL = 5 * time.Minute

type githubRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func packageIdentity(version, fileName string) string {
	version = strings.TrimSpace(version)
	fileName = strings.TrimSpace(fileName)
	if version == "" {
		return fileName
	}
	if fileName == "" {
		return version
	}
	return version + "|" + fileName
}

func cachedLatestResolver(key string, resolver LatestResolver) LatestResolver {
	return func(log func(string)) (url, fileName, stripTop, version string, err error) {
		latestCacheMu.Lock()
		entry, ok := latestCache[key]
		if ok && time.Since(entry.CheckedAt) < latestCacheTTL {
			latestCacheMu.Unlock()
			return entry.URL, entry.FileName, entry.StripTop, entry.Version, nil
		}
		latestCacheMu.Unlock()

		url, fileName, stripTop, version, err = resolver(log)
		if err != nil {
			return "", "", "", "", err
		}
		latestCacheMu.Lock()
		latestCache[key] = latestCacheEntry{
			URL: url, FileName: fileName, StripTop: stripTop,
			Version: version, CheckedAt: time.Now(),
		}
		latestCacheMu.Unlock()
		return url, fileName, stripTop, version, nil
	}
}

func resolveGitHubLatestAsset(owner, repo string, assetPattern *regexp.Regexp) LatestResolver {
	return func(log func(string)) (url, fileName, stripTop, version string, err error) {
		apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)
		log("  resolving latest release from " + apiURL)

		req, err := http.NewRequest(http.MethodGet, apiURL, nil)
		if err != nil {
			return "", "", "", "", err
		}
		req.Header.Set("User-Agent", "GoAMPP")
		req.Header.Set("Accept", "application/vnd.github+json")

		client := &http.Client{Timeout: 20 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return "", "", "", "", err
		}
		defer resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return "", "", "", "", fmt.Errorf("GitHub returned HTTP %d", resp.StatusCode)
		}

		var release githubRelease
		if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
			return "", "", "", "", err
		}

		for _, asset := range release.Assets {
			if !assetPattern.MatchString(asset.Name) {
				continue
			}
			resolvedVersion := strings.TrimPrefix(release.TagName, "v")
			log(fmt.Sprintf("  latest %s/%s release: %s", owner, repo, resolvedVersion))
			return asset.BrowserDownloadURL, asset.Name, "", resolvedVersion, nil
		}

		return "", "", "", "", fmt.Errorf("no matching asset in %s/%s release %s", owner, repo, release.TagName)
	}
}

func resolveGitHubSeriesAsset(owner, repo, tagPrefix string, assetPattern *regexp.Regexp) LatestResolver {
	return func(log func(string)) (url, fileName, stripTop, version string, err error) {
		apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases?per_page=50", owner, repo)
		log(fmt.Sprintf("  resolving latest %s/%s release in %s series ...", owner, repo, tagPrefix))

		req, err := http.NewRequest(http.MethodGet, apiURL, nil)
		if err != nil {
			return "", "", "", "", err
		}
		req.Header.Set("User-Agent", "GoAMPP")
		req.Header.Set("Accept", "application/vnd.github+json")
		client := &http.Client{Timeout: 20 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return "", "", "", "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return "", "", "", "", fmt.Errorf("GitHub returned HTTP %d", resp.StatusCode)
		}

		var releases []githubRelease
		if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
			return "", "", "", "", err
		}
		for _, release := range releases {
			if release.Draft || release.Prerelease || !strings.HasPrefix(release.TagName, tagPrefix) {
				continue
			}
			for _, asset := range release.Assets {
				if !assetPattern.MatchString(asset.Name) {
					continue
				}
				resolvedVersion := strings.TrimPrefix(release.TagName, "v")
				return asset.BrowserDownloadURL, asset.Name, "", resolvedVersion, nil
			}
		}
		return "", "", "", "", fmt.Errorf("no matching %s/%s release in series %s", owner, repo, tagPrefix)
	}
}

func withStripTop(resolver LatestResolver, strip func(fileName, version string) string) LatestResolver {
	return func(log func(string)) (url, fileName, stripTop, version string, err error) {
		url, fileName, _, version, err = resolver(log)
		if err != nil {
			return "", "", "", "", err
		}
		return url, fileName, strip(fileName, version), version, nil
	}
}

func resolveLatestDownload(name, variant string, log func(string)) (DownloadResolution, error) {
	spec, ok := DownloadCatalog[name]
	if !ok {
		return DownloadResolution{}, fmt.Errorf("no download info registered for %q", name)
	}

	resolved := DownloadResolution{
		Version:   spec.Version,
		PackageID: packageIdentity(spec.Version, spec.FileName),
		URL:       spec.URL,
		FileName:  spec.FileName,
		StripTop:  spec.StripTop,
	}

	if len(spec.Variants) > 0 && variant != "" {
		for _, v := range spec.Variants {
			if v.Version != variant {
				continue
			}
			return DownloadResolution{
				Version:   v.Version,
				PackageID: packageIdentity(v.Version, v.FileName),
				URL:       v.URL,
				FileName:  v.FileName,
				StripTop:  v.StripTop,
			}, nil
		}
		return DownloadResolution{}, fmt.Errorf("[%s] version %q not in catalogue", name, variant)
	}

	if spec.URLResolver == nil {
		return resolved, nil
	}

	url, fileName, stripTop, version, err := spec.URLResolver(log)
	if err != nil {
		return DownloadResolution{}, err
	}
	if fileName == "" {
		fileName = spec.FileName
	}
	if version == "" {
		version = spec.Version
	}
	return DownloadResolution{
		Version:   version,
		PackageID: packageIdentity(version, fileName),
		URL:       url,
		FileName:  fileName,
		StripTop:  stripTop,
	}, nil
}

func registerLatestResolver(name string, resolver LatestResolver) {
	spec, ok := DownloadCatalog[name]
	if !ok {
		return
	}

	cachedResolver := cachedLatestResolver(name, resolver)
	spec.URLResolver = func(log func(string)) (url, fileName, stripTop, version string, err error) {
		if app != nil {
			installResolveMu.Lock()
			resolvedFreshInstall[name] = !IsInstalled(name, app.baseDir)
			installResolveMu.Unlock()
		}
		return cachedResolver(log)
	}

	originalPostInstall := spec.PostInstall
	installDirParts := strings.FieldsFunc(filepath.ToSlash(spec.InstallDir), func(r rune) bool { return r == '/' })

	spec.PostInstall = func(installDir string, log func(string)) error {
		if originalPostInstall != nil {
			if err := originalPostInstall(installDir, log); err != nil {
				return err
			}
		}

		installResolveMu.Lock()
		freshInstall := resolvedFreshInstall[name]
		delete(resolvedFreshInstall, name)
		installResolveMu.Unlock()
		if !freshInstall {
			return nil
		}

		url, fileName, stripTop, version, err := cachedResolver(log)
		if err != nil {
			log(fmt.Sprintf("  package metadata skipped: %v", err))
			return nil
		}

		baseDir := installDir
		for range installDirParts {
			baseDir = filepath.Dir(baseDir)
		}
		resolved := DownloadResolution{
			Version:   version,
			PackageID: packageIdentity(version, fileName),
			URL:       url,
			FileName:  fileName,
			StripTop:  stripTop,
		}
		if err := writeInstalledPackage(baseDir, name, "", resolved); err != nil {
			log(fmt.Sprintf("  package metadata write failed: %v", err))
		} else {
			log(fmt.Sprintf("  recorded installed package %s", fileName))
		}
		return nil
	}

	DownloadCatalog[name] = spec
}

var resolveAdminerLatest = resolveGitHubLatestAsset(
	"vrana",
	"adminer",
	regexp.MustCompile(`^adminer-[0-9.]+-en\.php$`),
)

func init() {
	// Keep the bundled Adminer URL as the offline/failure fallback, but resolve
	// the newest official English build whenever an install is requested.
	registerLatestResolver("Adminer", resolveAdminerLatest)
}
