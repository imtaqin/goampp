//go:build linux

package linuxapp

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeFrameworkZIP(t *testing.T, entries []struct {
	name string
	body string
	mode os.FileMode
}) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name}
		header.SetMode(entry.mode)
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func serveFrameworkZIP(t *testing.T, payload []byte) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(payload)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestDownloadLinuxFrameworkZIPStripsOptionalProjectDirectory(t *testing.T) {
	tests := []struct {
		name    string
		entries []struct {
			name string
			body string
			mode os.FileMode
		}
	}{
		{
			name: "wrapped archive",
			entries: []struct {
				name string
				body string
				mode os.FileMode
			}{
				{"demo/pom.xml", "<project/>", 0o644},
				{"demo/src/main/App.java", "class App {}", 0o644},
			},
		},
		{
			name: "root archive",
			entries: []struct {
				name string
				body string
				mode os.FileMode
			}{
				{"pom.xml", "<project/>", 0o644},
				{"src/main/App.java", "class App {}", 0o644},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := makeFrameworkZIP(t, test.entries)
			server := serveFrameworkZIP(t, payload)
			root := t.TempDir()
			if err := downloadLinuxFrameworkZip(server.URL, root, "demo/", nil); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"pom.xml", filepath.Join("src", "main", "App.java")} {
				if _, err := os.Stat(filepath.Join(root, name)); err != nil {
					t.Errorf("expected extracted file %q: %v", name, err)
				}
			}
			if _, err := os.Stat(filepath.Join(root, "demo")); err == nil {
				t.Error("wrapper folder should not remain in the project root")
			}
		})
	}
}

func TestDownloadLinuxFrameworkZIPRejectsPathTraversal(t *testing.T) {
	payload := makeFrameworkZIP(t, []struct {
		name string
		body string
		mode os.FileMode
	}{
		{"../outside.txt", "do not write outside the root", 0o644},
	})
	server := serveFrameworkZIP(t, payload)
	parent := t.TempDir()
	root := filepath.Join(parent, "project")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := downloadLinuxFrameworkZip(server.URL, root, "", nil); err == nil {
		t.Fatal("expected archive path traversal to be rejected")
	}
	if _, err := os.Stat(filepath.Join(parent, "outside.txt")); !os.IsNotExist(err) {
		t.Fatalf("archive wrote outside the project root: %v", err)
	}
}

func TestDownloadLinuxFrameworkZIPRejectsSymlinkEntries(t *testing.T) {
	payload := makeFrameworkZIP(t, []struct {
		name string
		body string
		mode os.FileMode
	}{
		{"link", "../outside.txt", os.ModeSymlink | 0o777},
	})
	server := serveFrameworkZIP(t, payload)
	root := t.TempDir()
	err := downloadLinuxFrameworkZip(server.URL, root, "", nil)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "symbolic link") {
		t.Fatalf("expected symbolic link entry rejection, got %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "link")); !os.IsNotExist(err) {
		t.Fatalf("archive created a symlink: %v", err)
	}
}
