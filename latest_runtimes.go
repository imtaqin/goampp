//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

func numericVersionGT(a, b string) bool {
	pa := strings.Split(a, ".")
	pb := strings.Split(b, ".")
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		var ai, bi int
		if i < len(pa) {
			ai, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			bi, _ = strconv.Atoi(pb[i])
		}
		if ai != bi {
			return ai > bi
		}
	}
	return false
}

func resolveGoLatest(log func(string)) (urlOut, fileName, stripTop, version string, err error) {
	const apiURL = "https://go.dev/dl/?mode=json"
	log("  resolving latest stable Go Windows amd64 archive ...")
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
		return "", "", "", "", fmt.Errorf("Go download API returned HTTP %d", resp.StatusCode)
	}
	var releases []struct {
		Version string `json:"version"`
		Stable  bool   `json:"stable"`
		Files   []struct {
			Filename string `json:"filename"`
			OS       string `json:"os"`
			Arch     string `json:"arch"`
			Kind     string `json:"kind"`
		} `json:"files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return "", "", "", "", err
	}
	for _, release := range releases {
		if !release.Stable {
			continue
		}
		for _, file := range release.Files {
			if file.OS == "windows" && file.Arch == "amd64" && file.Kind == "archive" {
				v := strings.TrimPrefix(release.Version, "go")
				return "https://go.dev/dl/" + file.Filename, file.Filename, "go/", v, nil
			}
		}
	}
	return "", "", "", "", fmt.Errorf("stable Go windows-amd64 archive not found")
}

func resolveJuliaLatest(log func(string)) (urlOut, fileName, stripTop, version string, err error) {
	const apiURL = "https://julialang-s3.julialang.org/bin/versions.json"
	log("  resolving latest stable Julia Windows x64 archive ...")
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
		return "", "", "", "", fmt.Errorf("Julia versions API returned HTTP %d", resp.StatusCode)
	}
	var versions map[string]struct {
		Stable bool `json:"stable"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&versions); err != nil {
		return "", "", "", "", err
	}
	latest := ""
	stableRe := regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	for v, info := range versions {
		if !info.Stable || !stableRe.MatchString(v) {
			continue
		}
		if latest == "" || numericVersionGT(v, latest) {
			latest = v
		}
	}
	if latest == "" {
		return "", "", "", "", fmt.Errorf("no stable Julia version found")
	}
	parts := strings.Split(latest, ".")
	series := parts[0] + "." + parts[1]
	file := "julia-" + latest + "-win64.zip"
	dlURL := fmt.Sprintf("https://julialang-s3.julialang.org/bin/winnt/x64/%s/%s", series, file)
	return dlURL, file, "julia-" + latest + "/", latest, nil
}

func resolveDartLatest(log func(string)) (urlOut, fileName, stripTop, version string, err error) {
	const apiURL = "https://storage.googleapis.com/dart-archive/channels/stable/release/latest/VERSION"
	log("  resolving latest stable Dart SDK ...")
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
		return "", "", "", "", fmt.Errorf("Dart archive returned HTTP %d", resp.StatusCode)
	}
	var data struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", "", "", "", err
	}
	if data.Version == "" {
		return "", "", "", "", fmt.Errorf("Dart stable version missing")
	}
	file := "dartsdk-windows-x64-release.zip"
	dlURL := fmt.Sprintf("https://storage.googleapis.com/dart-archive/channels/stable/release/%s/sdk/%s", data.Version, file)
	return dlURL, file, "dart-sdk/", data.Version, nil
}

func resolveLua54Latest(log func(string)) (urlOut, fileName, stripTop, version string, err error) {
	const pageURL = "https://sourceforge.net/projects/luabinaries/files/"
	log("  resolving latest LuaBinaries 5.4 Windows x64 build ...")
	body, err := fetchReleaseText(pageURL)
	if err != nil {
		return "", "", "", "", err
	}
	re := regexp.MustCompile(`\b(5\.4\.[0-9]+)\b`)
	latest := ""
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		if len(m) == 2 && (latest == "" || numericVersionGT(m[1], latest)) {
			latest = m[1]
		}
	}
	if latest == "" {
		return "", "", "", "", fmt.Errorf("LuaBinaries 5.4 version not found")
	}
	file := "lua-" + latest + "_Win64_bin.zip"
	dlURL := fmt.Sprintf("https://downloads.sourceforge.net/project/luabinaries/%s/Tools%%20Executables/%s", latest, file)
	return dlURL, file, "", latest, nil
}

func resolveGHCLatest(log func(string)) (urlOut, fileName, stripTop, version string, err error) {
	const pageURL = "https://downloads.haskell.org/ghc/latest/"
	log("  resolving latest stable GHC Windows x86_64 archive ...")
	body, err := fetchReleaseText(pageURL)
	if err != nil {
		return "", "", "", "", err
	}
	re := regexp.MustCompile(`(ghc-([0-9]+\.[0-9]+\.[0-9]+)-x86_64-unknown-mingw32\.zip)`)
	m := re.FindStringSubmatch(body)
	if len(m) < 3 {
		return "", "", "", "", fmt.Errorf("GHC Windows zip not found")
	}
	return pageURL + m[1], m[1], "ghc-" + m[2] + "/", "GHC " + m[2], nil
}

func resolveScala213Latest(log func(string)) (urlOut, fileName, stripTop, version string, err error) {
	const pageURL = "https://www.scala-lang.org/download/"
	log("  resolving latest Scala 2.13 release ...")
	body, err := fetchReleaseText(pageURL)
	if err != nil {
		return "", "", "", "", err
	}
	re := regexp.MustCompile(`(?is)Current 2\.13\.x release.*?Scala\s+(2\.13\.[0-9]+)`)
	m := re.FindStringSubmatch(body)
	if len(m) < 2 {
		return "", "", "", "", fmt.Errorf("Scala 2.13 release not found")
	}
	v := m[1]
	file := "scala-" + v + ".zip"
	return "https://downloads.lightbend.com/scala/" + v + "/" + file, file, "scala-" + v + "/", v, nil
}

func resolveRustStable(log func(string)) (urlOut, fileName, stripTop, version string, err error) {
	const manifestURL = "https://static.rust-lang.org/dist/channel-rust-stable.toml"
	log("  resolving latest Rust stable toolchain ...")
	body, err := fetchReleaseText(manifestURL)
	if err != nil {
		return "", "", "", "", err
	}
	re := regexp.MustCompile(`(?s)\[pkg\.rust\].*?version\s*=\s*"([0-9]+\.[0-9]+\.[0-9]+)`)
	m := re.FindStringSubmatch(body)
	if len(m) < 2 {
		return "", "", "", "", fmt.Errorf("Rust stable version not found")
	}
	return "https://static.rust-lang.org/rustup/dist/x86_64-pc-windows-msvc/rustup-init.exe",
		"rustup-init.exe", "", m[1] + " stable", nil
}

func init() {
	registerLatestResolver("Go", resolveGoLatest)
	registerLatestResolver("Julia", resolveJuliaLatest)
	registerLatestResolver("Zig", resolveZigLatest)
	registerLatestResolver("Dart", resolveDartLatest)
	registerLatestResolver("Lua", resolveLua54Latest)
	registerLatestResolver("Haskell", resolveGHCLatest)
	registerLatestResolver("Scala", resolveScala213Latest)
	registerLatestResolver("Rust", resolveRustStable)
}
