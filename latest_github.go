//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

func resolveGitHubLatestTag(owner, repo, downloadURL, fileName, stripTop string) LatestResolver {
	return func(log func(string)) (url, file, strip, version string, err error) {
		apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)
		log("  resolving latest release from " + apiURL)
		req, err := http.NewRequest(http.MethodGet, apiURL, nil)
		if err != nil {
			return "", "", "", "", err
		}
		req.Header.Set("User-Agent", "GoAMPP")
		req.Header.Set("Accept", "application/vnd.github+json")
		resp, err := latestHTTPClient.Do(req)
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
		v := strings.TrimPrefix(release.TagName, "v")
		return downloadURL, fileName, stripTop, v, nil
	}
}

func mapResolverVersion(resolver LatestResolver, fn func(string) string) LatestResolver {
	return func(log func(string)) (url, fileName, stripTop, version string, err error) {
		url, fileName, stripTop, version, err = resolver(log)
		if err != nil {
			return "", "", "", "", err
		}
		return url, fileName, stripTop, fn(version), nil
	}
}

func resolveTemurin21Latest(log func(string)) (url, fileName, stripTop, version string, err error) {
	url, fileName, _, rawVersion, err := resolveGitHubLatestAsset(
		"adoptium", "temurin21-binaries",
		regexp.MustCompile(`^OpenJDK21U-jdk_x64_windows_hotspot_.*\.zip$`),
	)(log)
	if err != nil {
		return "", "", "", "", err
	}
	jdkVersion := strings.TrimPrefix(rawVersion, "jdk-")
	return url, fileName, "jdk-" + jdkVersion + "/", "Temurin JDK " + jdkVersion, nil
}

func ensureRabbitMQErlang(installDir string, log func(string)) error {
	baseDir := filepath.Dir(filepath.Dir(installDir))
	erlangDir := filepath.Join(baseDir, "bin", "erlang")
	if _, err := os.Stat(filepath.Join(erlangDir, "bin", "erl.exe")); err == nil {
		return nil
	}

	resolved, err := resolveLatestDownload("Erlang", "", log)
	if err != nil {
		spec, ok := DownloadCatalog["Erlang"]
		if !ok {
			return fmt.Errorf("resolve Erlang dependency: %w", err)
		}
		log(fmt.Sprintf("  Erlang version resolve failed (%v), falling back to bundled package", err))
		resolved = DownloadResolution{
			Version:   spec.Version,
			PackageID: packageIdentity(spec.Version, spec.FileName),
			URL:       spec.URL,
			FileName:  spec.FileName,
			StripTop:  spec.StripTop,
		}
	}

	log(fmt.Sprintf("  Erlang not found — downloading %s...", resolved.Version))
	dlDir := filepath.Join(baseDir, "downloads")
	if err := os.MkdirAll(dlDir, 0o755); err != nil {
		return fmt.Errorf("erlang downloads dir: %w", err)
	}
	erlExe := filepath.Join(dlDir, resolved.FileName)
	if _, err := os.Stat(erlExe); err != nil {
		if err := httpDownload(resolved.URL, erlExe, log, nil); err != nil {
			return fmt.Errorf("erlang download: %w", err)
		}
	} else {
		log("  using cached download: " + resolved.FileName)
	}

	log("  installing Erlang OTP silently...")
	if err := os.MkdirAll(erlangDir, 0o755); err != nil {
		return fmt.Errorf("erlang dir: %w", err)
	}
	abs, _ := filepath.Abs(erlangDir)
	cmd := exec.Command(erlExe, "/S", "/D="+abs)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if out, err := cmd.CombinedOutput(); err != nil {
		log("  erlang installer: " + strings.TrimSpace(string(out)))
		return fmt.Errorf("erlang install: %w", err)
	}
	log("  Erlang OTP installed")

	// resolveLatestDownload marks resolver-backed installs for the normal
	// PostInstall metadata hook. RabbitMQ installs Erlang directly while the
	// download mutex is already held, so record the metadata here instead.
	installResolveMu.Lock()
	delete(resolvedFreshInstall, "Erlang")
	installResolveMu.Unlock()
	if err := writeInstalledPackage(baseDir, "Erlang", "", resolved); err != nil {
		log(fmt.Sprintf("  Erlang package metadata write failed: %v", err))
	} else {
		log(fmt.Sprintf("  recorded installed Erlang package %s", resolved.FileName))
	}
	return nil
}

func init() {
	registerLatestResolver("Redis", resolveGitHubLatestAsset(
		"tporadowski", "redis",
		regexp.MustCompile(`(?i)^Redis-x64-[0-9.]+\.zip$`),
	))

	registerLatestResolver("pgweb", resolveGitHubLatestAsset(
		"sosedoff", "pgweb",
		regexp.MustCompile(`^pgweb_windows_amd64\.zip$`),
	))

	registerLatestResolver("Mailpit", resolveGitHubLatestAsset(
		"axllent", "mailpit",
		regexp.MustCompile(`^mailpit-windows-amd64\.zip$`),
	))

	registerLatestResolver("Kotlin", withStripTop(
		resolveGitHubLatestAsset(
			"JetBrains", "kotlin",
			regexp.MustCompile(`^kotlin-compiler-[0-9][0-9A-Za-z.\-]*\.zip$`),
		),
		func(_, _ string) string { return "kotlinc/" },
	))

	registerLatestResolver("Ruby", resolveGitHubLatestAsset(
		"oneclick", "rubyinstaller2",
		regexp.MustCompile(`^rubyinstaller-[0-9.]+-[0-9]+-x64\.exe$`),
	))

	registerLatestResolver("Crystal", withStripTop(
		resolveGitHubLatestAsset(
			"crystal-lang", "crystal",
			regexp.MustCompile(`^crystal-.*-windows-x86_64-msvc.*\.zip$`),
		),
		func(fileName, _ string) string { return strings.TrimSuffix(fileName, ".zip") + "/" },
	))

	registerLatestResolver("Elixir", mapResolverVersion(
		resolveGitHubLatestAsset(
			"elixir-lang", "elixir",
			regexp.MustCompile(`^elixir-otp-27\.zip$`),
		),
		func(v string) string { return v + " (OTP 27)" },
	))

	rabbitResolver := withStripTop(
		resolveGitHubSeriesAsset(
			"rabbitmq", "rabbitmq-server", "v4.3.",
			regexp.MustCompile(`^rabbitmq-server-windows-4\.3\.[0-9]+\.zip$`),
		),
		func(_, version string) string {
			return "rabbitmq_server-" + strings.TrimPrefix(version, "v") + "/"
		},
	)
	registerLatestResolver("RabbitMQ", rabbitResolver)

	erlangResolver := mapResolverVersion(
		resolveGitHubSeriesAsset(
			"erlang", "otp", "OTP-27.",
			regexp.MustCompile(`^otp_win64_27\.[0-9.]+\.exe$`),
		),
		func(v string) string {
			return strings.TrimPrefix(v, "OTP-") + " (OTP-27)"
		},
	)
	registerLatestResolver("Erlang", erlangResolver)

	// RabbitMQ's legacy post-install closure contains an OTP 27.3.4 fallback.
	// Pre-install the dynamically resolved OTP 27 package first; the legacy
	// fallback then sees erl.exe and cleanly skips its hard-coded branch.
	if spec, ok := DownloadCatalog["RabbitMQ"]; ok {
		originalPostInstall := spec.PostInstall
		spec.PostInstall = func(installDir string, log func(string)) error {
			if err := ensureRabbitMQErlang(installDir, log); err != nil {
				return err
			}
			if originalPostInstall != nil {
				return originalPostInstall(installDir, log)
			}
			return nil
		}
		DownloadCatalog["RabbitMQ"] = spec
	}

	registerLatestResolver("Java", resolveTemurin21Latest)

	registerLatestResolver("Composer", resolveGitHubLatestTag(
		"composer", "composer",
		"https://getcomposer.org/composer-stable.phar",
		"composer-stable.phar", "",
	))

	registerLatestResolver("MinIO", resolveGitHubLatestTag(
		"minio", "minio",
		"https://dl.min.io/server/minio/release/windows-amd64/minio.exe",
		"minio.exe", "",
	))
}
