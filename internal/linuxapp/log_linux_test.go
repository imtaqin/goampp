//go:build linux

package linuxapp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadProjectLogTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".goampp-run.log")
	content := strings.Repeat("x", 100) + "END"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, truncated, err := readProjectLogTail(path, 16)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated {
		t.Fatal("expected oversized log preview to be marked truncated")
	}
	if got != strings.Repeat("x", 13)+"END" {
		t.Fatalf("tail = %q", got)
	}
	got, truncated, err = readProjectLogTail(path, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if truncated || got != content {
		t.Fatalf("small log should be returned in full; truncated=%v len=%d", truncated, len(got))
	}
}

func TestReadProjectLogTailRejectsInvalidLimit(t *testing.T) {
	if _, _, err := readProjectLogTail("anything", 0); err == nil {
		t.Fatal("zero-byte limit must fail")
	}
}
