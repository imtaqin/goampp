//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

var apacheLoungeZipRe = regexp.MustCompile(`/download/VS(\d+)/binaries/httpd-(\d+\.\d+\.\d+)-(\d+)-Win64-VS\d+\.zip`)

// resolveApacheLatest scrapes the Apache Lounge download index for the newest
// Win64 build. Apache Lounge keeps no persistent URL: the binaries directory
// itself 404s, and every filename embeds both the version and the build date,
// so a hardcoded URL rots as soon as a new build is cut (that is what broke
// Apache installs — see issue #1).
func resolveApacheLatest(log func(string)) (url, fileName, stripTop, version string, err error) {
	log("  resolving latest Apache Win64 build from apachelounge.com/download/ ...")
	resp, err := http.Get("https://www.apachelounge.com/download/")
	if err != nil {
		return "", "", "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", "", "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", "", "", err
	}

	link, ver, build, vs, ok := pickNewestApacheBuild(string(body))
	if !ok {
		return "", "", "", "", fmt.Errorf("no Win64 VS build listed on download page")
	}
	log(fmt.Sprintf("  latest Apache: %s (VS%s, build %s)", ver, vs, build))
	return "https://www.apachelounge.com" + link,
		fmt.Sprintf("httpd-%s-%s-Win64-VS%s.zip", ver, build, vs),
		"Apache24/",
		fmt.Sprintf("%s (VS%s, win64)", ver, vs),
		nil
}

// pickNewestApacheBuild picks the newest version, then the newest build of that
// version, then the newest toolset — so a build published only for a newer VS
// still wins, matching the catalogue's existing VS18 default.
func pickNewestApacheBuild(html string) (link, ver, build, vs string, ok bool) {
	var bestVer, bestBuild, bestVS int
	for _, m := range apacheLoungeZipRe.FindAllStringSubmatch(html, -1) {
		cvs, cver, cbuild := versionNum(m[1]), versionNum(m[2]), versionNum(m[3])
		if !ok || cver > bestVer ||
			(cver == bestVer && cbuild > bestBuild) ||
			(cver == bestVer && cbuild == bestBuild && cvs > bestVS) {
			link, ver, build, vs = m[0], m[2], m[3], m[1]
			bestVS, bestVer, bestBuild = cvs, cver, cbuild
			ok = true
		}
	}
	return link, ver, build, vs, ok
}

// versionNum turns a dotted version ("2.4.68") into a sortable int. Build
// dates ("260827") and VS tags ("18") have no dots, so they fall out as plain
// ints — one helper covers all three fields compared below.
func versionNum(s string) int {
	n := 0
	for _, part := range strings.Split(s, ".") {
		v := 0
		fmt.Sscanf(part, "%d", &v)
		n = n*1000 + v
	}
	return n
}

func resolveZigLatest(log func(string)) (url, fileName, stripTop, version string, err error) {
	log("  resolving latest Zig stable from ziglang.org/download/index.json ...")
	resp, err := http.Get("https://ziglang.org/download/index.json")
	if err != nil {
		return "", "", "", "", err
	}
	defer resp.Body.Close()
	var index map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&index); err != nil {
		return "", "", "", "", err
	}
	var versions []string
	for k := range index {
		if k == "master" {
			continue
		}
		versions = append(versions, k)
	}
	sort.Slice(versions, func(i, j int) bool {
		return zigVersionGT(versions[i], versions[j])
	})
	if len(versions) == 0 {
		return "", "", "", "", fmt.Errorf("no stable versions found")
	}
	latest := versions[0]
	var entry map[string]json.RawMessage
	if err := json.Unmarshal(index[latest], &entry); err != nil {
		return "", "", "", "", err
	}
	var asset struct {
		Tarball string `json:"tarball"`
	}
	raw, ok := entry["x86_64-windows"]
	if !ok {
		return "", "", "", "", fmt.Errorf("no x86_64-windows asset for %s", latest)
	}
	if err := json.Unmarshal(raw, &asset); err != nil {
		return "", "", "", "", err
	}
	dlURL := asset.Tarball
	parts := strings.Split(dlURL, "/")
	file := parts[len(parts)-1]
	strip := strings.TrimSuffix(file, ".zip") + "/"
	log(fmt.Sprintf("  latest Zig stable: %s", latest))
	return dlURL, file, strip, latest, nil
}

func zigVersionGT(a, b string) bool {
	pa := strings.Split(a, ".")
	pb := strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		var na, nb int
		if i < len(pa) {
			fmt.Sscanf(pa[i], "%d", &na)
		}
		if i < len(pb) {
			fmt.Sscanf(pb[i], "%d", &nb)
		}
		if na != nb {
			return na > nb
		}
	}
	return false
}
