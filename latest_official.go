//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var latestHTTPClient = &http.Client{Timeout: 20 * time.Second}

func fetchReleaseText(rawURL string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "GoAMPP")
	resp, err := latestHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, rawURL)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func resolveNginxStable(log func(string)) (urlOut, fileName, stripTop, version string, err error) {
	const pageURL = "https://nginx.org/en/download.html"
	log("  resolving latest nginx stable Windows build ...")
	body, err := fetchReleaseText(pageURL)
	if err != nil {
		return "", "", "", "", err
	}

	re := regexp.MustCompile(`(?is)Stable version.*?nginx/Windows-([0-9]+\.[0-9]+\.[0-9]+)`)
	m := re.FindStringSubmatch(body)
	if len(m) < 2 {
		return "", "", "", "", fmt.Errorf("nginx stable Windows version not found")
	}
	v := m[1]
	file := "nginx-" + v + ".zip"
	return "https://nginx.org/download/" + file, file, "nginx-" + v + "/", v + " stable", nil
}

type mariaDBLatestResponse struct {
	Releases map[string]struct {
		ReleaseID string `json:"release_id"`
		Files     []struct {
			FileName        string `json:"file_name"`
			FileDownloadURL string `json:"file_download_url"`
		} `json:"files"`
	} `json:"releases"`
}

func resolveMariaDB114Latest(log func(string)) (urlOut, fileName, stripTop, version string, err error) {
	const apiURL = "https://downloads.mariadb.org/rest-api/mariadb/11.4/latest/"
	log("  resolving latest MariaDB 11.4 LTS Windows build ...")

	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return "", "", "", "", err
	}
	req.Header.Set("User-Agent", "GoAMPP")
	resp, err := latestHTTPClient.Do(req)
	if err != nil {
		return "", "", "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", "", "", fmt.Errorf("MariaDB API returned HTTP %d", resp.StatusCode)
	}

	var data mariaDBLatestResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", "", "", "", err
	}
	assetRe := regexp.MustCompile(`^mariadb-([0-9]+\.[0-9]+\.[0-9]+)-winx64\.zip$`)
	for releaseKey, release := range data.Releases {
		for _, file := range release.Files {
			m := assetRe.FindStringSubmatch(file.FileName)
			if len(m) != 2 {
				continue
			}
			v := m[1]
			if release.ReleaseID != "" {
				v = release.ReleaseID
			} else if releaseKey != "" {
				v = releaseKey
			}
			if !strings.HasPrefix(v, "11.4.") {
				continue
			}
			log("  latest MariaDB 11.4 LTS: " + v)
			return file.FileDownloadURL, file.FileName, strings.TrimSuffix(file.FileName, ".zip") + "/", "MariaDB " + v + " LTS", nil
		}
	}
	return "", "", "", "", fmt.Errorf("MariaDB 11.4 winx64 zip not found")
}

type phpMyAdminVersion struct {
	Version string `json:"version"`
}

func resolvePhpMyAdminLatest(log func(string)) (urlOut, fileName, stripTop, version string, err error) {
	const apiURL = "https://www.phpmyadmin.net/home_page/version.json"
	log("  resolving latest phpMyAdmin stable release ...")
	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return "", "", "", "", err
	}
	req.Header.Set("User-Agent", "GoAMPP")
	resp, err := latestHTTPClient.Do(req)
	if err != nil {
		return "", "", "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", "", "", fmt.Errorf("phpMyAdmin version API returned HTTP %d", resp.StatusCode)
	}
	var data phpMyAdminVersion
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", "", "", "", err
	}
	if data.Version == "" {
		return "", "", "", "", fmt.Errorf("phpMyAdmin version missing")
	}
	file := fmt.Sprintf("phpMyAdmin-%s-all-languages.zip", data.Version)
	dlURL := fmt.Sprintf("https://files.phpmyadmin.net/phpMyAdmin/%s/%s", data.Version, file)
	return dlURL, file, strings.TrimSuffix(file, ".zip") + "/", data.Version, nil
}

func resolvePostgreSQL16Latest(log func(string)) (urlOut, fileName, stripTop, version string, err error) {
	const pageURL = "https://www.enterprisedb.com/download-postgresql-binaries"
	log("  resolving latest PostgreSQL 16 Windows binary archive ...")
	body, err := fetchReleaseText(pageURL)
	if err != nil {
		return "", "", "", "", err
	}
	re := regexp.MustCompile(`(?i)Binaries from installer Version\s+(16\.[0-9]+)`)
	m := re.FindStringSubmatch(body)
	if len(m) < 2 {
		return "", "", "", "", fmt.Errorf("PostgreSQL 16 binary version not found")
	}
	v := m[1]

	// EDB occasionally republishes an installer point release with a new build
	// suffix. Probe a few build numbers without downloading the archive.
	for build := 4; build >= 1; build-- {
		file := fmt.Sprintf("postgresql-%s-%d-windows-x64-binaries.zip", v, build)
		dlURL := "https://get.enterprisedb.com/postgresql/" + file
		req, err := http.NewRequest(http.MethodHead, dlURL, nil)
		if err != nil {
			continue
		}
		resp, err := latestHTTPClient.Do(req)
		if err != nil {
			continue
		}
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 400 {
			log(fmt.Sprintf("  latest PostgreSQL 16 binary: %s (EDB build %d)", v, build))
			return dlURL, file, "pgsql/", v, nil
		}
	}
	return "", "", "", "", fmt.Errorf("no reachable EDB PostgreSQL %s x64 binary archive", v)
}

func init() {
	registerLatestResolver("Apache", resolveApacheLatest)
	registerLatestResolver("Nginx", resolveNginxStable)
	registerLatestResolver("MySQL", resolveMariaDB114Latest)
	registerLatestResolver("PostgreSQL", resolvePostgreSQL16Latest)
	registerLatestResolver("phpMyAdmin", resolvePhpMyAdminLatest)
}
