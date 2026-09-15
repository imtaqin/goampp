//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// downloadCache owns the shared downloads/ directory.
//
// It exists because the same two mistakes were written out by hand in two
// places — download.go and frameworks.go — and both had the same bug: a bare
// os.Stat was treated as proof that a cached file was complete and valid. It
// is not. Apache Lounge answers a removed build with HTTP 200 and a small HTML
// error page, and that landed in downloads/ under a .zip name. os.Stat saw a
// file, the caller skipped the download, and every later run failed at extract
// off the same poisoned copy — forever, with no way out short of deleting the
// file by hand (issue #1).
//
// So caching goes through here: a file is adopted as valid at the moment it is
// written, or never. Anything already sitting in downloads/ from an earlier
// version of the app is treated as untrusted, because nothing recorded where it
// came from. See PurgeStale for how those are re-checked.
type downloadCache struct {
	dir    string
	log    func(string)
	valid  map[string]bool
	loaded bool
}

func newDownloadCache(baseDir string, log func(string)) *downloadCache {
	if log == nil {
		log = func(string) {}
	}
	return &downloadCache{
		dir:   filepath.Join(baseDir, "downloads"),
		log:   log,
		valid: map[string]bool{},
	}
}

// Ensure reports whether a usable file already exists at name, creating the
// cache directory on the way. A false return is a download instruction, not an
// error: only a failure to prepare the directory is an error.
func (c *downloadCache) Ensure(name string) (bool, error) {
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return false, err
	}
	if !c.loaded {
		c.loadAdopted()
	}
	path := filepath.Join(c.dir, name)
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() || fi.Size() == 0 {
		delete(c.valid, name)
		return false, nil
	}

	if !c.valid[name] {
		// On disk but not recorded as fetched by us — a leftover from an
		// earlier run. Validate before trusting it: a bare os.Stat is exactly
		// what let a poisoned file poison the cache in the first place.
		if reason := staleReason(path, name); reason != "" {
			return false, nil // PurgeStale will collect it; treat as absent
		}
		c.markAdopted(name, true)
	}
	return true, nil
}

// Path returns the cache location for a file name.
func (c *downloadCache) Path(name string) string {
	return filepath.Join(c.dir, name)
}

// Save downloads url into the cache and adopts the result as verified. A failed
// download leaves nothing behind to be mistaken for a cached file next run.
func (c *downloadCache) Save(name, url string, onProgress func(done, total int64)) error {
	if _, err := c.Ensure(name); err != nil {
		return err
	}
	path := c.Path(name)
	if err := httpDownload(url, path, c.log, onProgress); err != nil {
		// httpDownload renames its .part into place only on success, but be
		// explicit: a half-written file must not become tomorrow's cache hit.
		_ = os.Remove(path)
		delete(c.valid, name)
		return err
	}
	if fi, err := os.Stat(path); err != nil || fi.Size() == 0 {
		_ = os.Remove(path)
		return fmt.Errorf("downloaded %s is empty", name)
	}
	c.markAdopted(name, true)
	return nil
}

// Fetch returns the path to a verified cached copy of url, downloading it first
// if needed. The returned path is safe to hand to extractZip or run.
func (c *downloadCache) Fetch(name, url string, onProgress func(done, total int64)) (string, error) {
	hit, err := c.Ensure(name)
	if err != nil {
		return "", err
	}
	if hit {
		c.log("  using cached " + name)
	} else {
		if err := c.Save(name, url, onProgress); err != nil {
			return "", err
		}
	}
	return c.Path(name), nil
}

// PurgeStale removes cache entries that are provably not the file they claim to
// be, returning the names it removed.
//
// Deliberately conservative. downloads/ is not uniform: it holds zips, .exe
// installers, and legitimately textual payloads (Adminer ships as a .php file,
// Composer as a .phar, pip's bootstrap as .py). Deleting "anything that isn't a
// known binary format" would destroy those, so a file is only removed when
// there is positive evidence it is wrong:
//
//   - zero size — cannot be a usable download, whatever it is;
//   - a .zip that is not a zip, by magic bytes — this is the poisoned case;
//   - content that is HTML where the caller expects a .zip or .exe — this is
//     the exact shape of an error page served under a 200.
//
// Everything else is left alone. Worst case an odd-but-real file stays cached;
// that is strictly better than deleting a runtime somebody's project needs.
func (c *downloadCache) PurgeStale() (removed []string, err error) {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(c.dir, e.Name())
		if reason := staleReason(path, e.Name()); reason != "" {
			if rmErr := os.Remove(path); rmErr != nil {
				c.log(fmt.Sprintf("  could not remove stale %s: %v", e.Name(), rmErr))
				continue
			}
			c.log(fmt.Sprintf("  removed stale cached %s — %s", e.Name(), reason))
			removed = append(removed, e.Name())
		}
	}

	c.loaded = false // disk changed underneath the adoption set
	return removed, nil
}

// staleReason returns why a cached file cannot be trusted, or "" if it is fine.
func staleReason(path, name string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	if fi.IsDir() {
		return ""
	}
	if fi.Size() == 0 {
		return "file is empty"
	}

	kind := strings.ToLower(filepath.Ext(name))
	if kind != ".zip" && kind != ".exe" {
		return ""
	}

	head := make([]byte, 512)
	n, _ := f.Read(head)
	if n == 0 {
		return ""
	}
	head = head[:n]

	if kind == ".zip" && !hasZipMagic(head) {
		return "not a zip (truncated or error page saved under a zip name)"
	}
	if isHTML(head) {
		return "looks like an HTML error page"
	}
	return ""
}

func hasZipMagic(b []byte) bool {
	return len(b) >= 4 && b[0] == 'P' && b[1] == 'K' &&
		(b[2] == 3 || b[2] == 5 || b[2] == 7) && (b[3] == 4 || b[3] == 6 || b[3] == 8)
}

func isHTML(b []byte) bool {
	s := strings.ToLower(strings.TrimSpace(string(b[:min(len(b), 256)])))
	for _, marker := range []string{"<!doctype html", "<html", "<?xml", "<head>", "<body"} {
		if strings.HasPrefix(s, marker) {
			return true
		}
	}
	return false
}

// Adoption sidecar. Files fetched by this process need no re-checking, so their
// names are recorded next to the cache. Entries that predate the sidecar — or
// that the user replaced by hand — are not in it, which is exactly what makes
// PurgeStale look at them.
func (c *downloadCache) sidecarPath() string {
	return filepath.Join(c.dir, ".goampp-verified")
}

func (c *downloadCache) loadAdopted() {
	c.loaded = true
	data, err := os.ReadFile(c.sidecarPath())
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			c.valid[name] = true
		}
	}
}

func (c *downloadCache) markAdopted(name string, ok bool) {
	if !c.loaded {
		c.loadAdopted()
	}
	if ok {
		c.valid[name] = true
	} else {
		delete(c.valid, name)
	}

	var sb strings.Builder
	for n := range c.valid {
		sb.WriteString(n)
		sb.WriteString("\n")
	}
	_ = os.WriteFile(c.sidecarPath(), []byte(sb.String()), 0o644)
}

// DownloadCache is the process-wide cache, built once baseDir is known. Before
// then baseDir is empty, which would resolve downloads/ into the current
// directory — so callers get a cache rooted at "" only after main sets it.
var DownloadCache *downloadCache

func setDownloadCache(baseDir string, log func(string)) {
	DownloadCache = newDownloadCache(baseDir, log)
}

// cacheFor returns the shared cache, falling back to constructing one when it
// has not been initialised yet (tests, and direct calls before main starts).
func cacheFor(baseDir string, log func(string)) *downloadCache {
	if DownloadCache != nil {
		return DownloadCache
	}
	return newDownloadCache(baseDir, log)
}

// sweepDownloadCache clears provably-broken files left by an earlier run, so an
// install that already hit the Apache Lounge bug heals on next launch instead of
// failing at extract once more. Runs once at startup.
func sweepDownloadCache(baseDir string, log func(string)) {
	c := cacheFor(baseDir, log)
	removed, err := c.PurgeStale()
	if err != nil {
		log("downloads cache check: " + err.Error())
		return
	}
	if len(removed) > 0 {
		log(fmt.Sprintf("downloads cache: cleared %d stale file(s) — they will be fetched again", len(removed)))
	}
}
