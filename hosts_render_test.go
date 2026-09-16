//go:build windows

package main

import (
	"strings"
	"testing"
)

func TestBuildHostsContentWritesManagedDomain(t *testing.T) {
	out := string(buildHostsContent(
		[]byte("127.0.0.1 localhost\r\n"),
		[]Vhost{{Domain: "demo.test"}},
	))

	for _, want := range []string{
		hostsMarkerBegin,
		"127.0.0.1 demo.test",
		"::1       demo.test",
		hostsMarkerEnd,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("generated hosts content missing %q:\n%s", want, out)
		}
	}
}

func TestBuildHostsContentReplacesOldManagedBlock(t *testing.T) {
	existing := strings.Join([]string{
		"127.0.0.1 localhost",
		hostsMarkerBegin,
		"127.0.0.1 old.test",
		"::1       old.test",
		hostsMarkerEnd,
		"",
	}, "\r\n")

	out := string(buildHostsContent(
		[]byte(existing),
		[]Vhost{{Domain: "new.test"}},
	))

	if strings.Contains(out, "old.test") {
		t.Fatalf("old managed domain remained after replacement:\n%s", out)
	}
	if !strings.Contains(out, "127.0.0.1 new.test") ||
		!strings.Contains(out, "::1       new.test") {
		t.Fatalf("new managed domain missing after replacement:\n%s", out)
	}
}
