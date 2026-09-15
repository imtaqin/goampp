//go:build windows

package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

// htmlErrorPage is the shape Apache Lounge serves, with HTTP 200, for a build
// it has taken down. This is the exact content that poisoned the cache.
const htmlErrorPage = `<html><head><link rel="stylesheet" href="style.css">` +
	` <TITLE>Apache Lounge</TITLE></head><body> Oops... That's an error,` +
	` the requested URL was not found on this server.</body></html>`

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// realZip writes an actual archive so the "not a zip" rule is proven against a
// genuine zip rather than a hand-rolled PK prefix.
func realZip(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	w, err := zw.Create("Apache24/conf/httpd.conf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("Define SRVROOT \"C:/goampp/bin/apache\"\r\n")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPurgeStaleRemovesOnlyProvablyBroken(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "downloads")
	c := newDownloadCache(filepath.Dir(dir), func(string) {})

	realZip(t, filepath.Join(dir, "httpd-good.zip"))
	writeFile(t, filepath.Join(dir, "httpd-poisoned.zip"), htmlErrorPage) // the bug
	writeFile(t, filepath.Join(dir, "httpd-empty.zip"), "")
	writeFile(t, filepath.Join(dir, "setup-page-not-exe.exe"), htmlErrorPage)

	// These are legitimate textual payloads that live in the same directory.
	// Deleting them would break runtimes, so they must survive untouched.
	writeFile(t, filepath.Join(dir, "adminer-5.4.2-en.php"), "<?php\n// adminer\n")
	writeFile(t, filepath.Join(dir, "composer-stable.phar"), "#!/usr/bin/env php\n<?php\n")
	writeFile(t, filepath.Join(dir, "get-pip.py"), "#!/usr/bin/env python\nprint('hi')\n")
	writeFile(t, filepath.Join(dir, "notes.txt"), htmlErrorPage) // not .zip/.exe: left alone

	got, err := c.PurgeStale()
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]bool{
		"httpd-poisoned.zip":     true,
		"httpd-empty.zip":        true,
		"setup-page-not-exe.exe": true,
	}
	seen := map[string]bool{}
	for _, name := range got {
		seen[name] = true
		if !want[name] {
			t.Errorf("removed %q, which is not provably broken", name)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("expected %q to be removed", name)
		}
	}

	// The conservative half: everything legitimate must still be there.
	for _, keep := range []string{
		"httpd-good.zip", "adminer-5.4.2-en.php",
		"composer-stable.phar", "get-pip.py", "notes.txt",
	} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Errorf("%q should have been kept: %v", keep, err)
		}
	}
}

// A file already on disk from an earlier version must not be trusted on the
// strength of os.Stat alone — that was the whole bug.
func TestEnsureValidatesLegacyFilesInsteadOfTrustingStat(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "downloads")
	c := newDownloadCache(filepath.Dir(dir), func(string) {})

	realZip(t, filepath.Join(dir, "valid.zip"))
	hit, err := c.Ensure("valid.zip")
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Error("a genuine leftover zip should be validated and reused, not re-downloaded")
	}

	writeFile(t, filepath.Join(dir, "poisoned.zip"), htmlErrorPage)
	hit, err = c.Ensure("poisoned.zip")
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Error("an HTML error page cached as .zip must not count as a cache hit")
	}
}

func TestEnsureMissesWhenAbsent(t *testing.T) {
	c := newDownloadCache(t.TempDir(), func(string) {})
	hit, err := c.Ensure("nothing-here.zip")
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Error("expected a cache miss for an absent file")
	}
}

// Once a file has been fetched by us it is adopted, and adoption is what makes
// it a cache hit on disk — no re-validation, and removing it drops the claim.
func TestAdoptionRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "downloads")
	c := newDownloadCache(filepath.Dir(dir), func(string) {})

	realZip(t, filepath.Join(dir, "fetched.zip"))
	c.markAdopted("fetched.zip", true)

	fresh := newDownloadCache(filepath.Dir(dir), func(string) {})
	hit, err := fresh.Ensure("fetched.zip")
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Error("an adopted file should be a hit after reloading the sidecar")
	}

	fresh.markAdopted("fetched.zip", false)
	fresh2 := newDownloadCache(filepath.Dir(dir), func(string) {})
	hit, err = fresh2.Ensure("fetched.zip")
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		// Still on disk and still a valid zip, so it is re-validated and
		// adopted again rather than treated as missing.
		t.Error("a valid file should be re-adopted after its claim is dropped")
	}
}

func TestPurgeStaleOnMissingDirIsNotAnError(t *testing.T) {
	c := newDownloadCache(t.TempDir(), func(string) {})
	removed, err := c.PurgeStale()
	if err != nil {
		t.Fatalf("a missing downloads dir is normal on first run: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("expected nothing removed, got %v", removed)
	}
}
