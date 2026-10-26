//go:build linux

package linuxapp

import (
	"bytes"
	"testing"
)

func TestLinuxVisualAssetsAreEmbedded(t *testing.T) {
	if linuxLogoResource() == nil {
		t.Fatal("GoAMPP logo should be available from the embedded asset filesystem")
	}
	for name, path := range linuxServiceAssets {
		if linuxAsset(path) == nil {
			t.Errorf("service image for %q could not be loaded from %q", name, path)
		}
	}
	for name, path := range linuxToolAssets {
		if linuxAsset(path) == nil {
			t.Errorf("developer-tool image for %q could not be loaded from %q", name, path)
		}
	}
}

func TestPNGFromLinuxICOExtractsEmbeddedPNG(t *testing.T) {
	icon := linuxAsset("icons/apache.ico")
	if icon == nil {
		t.Fatal("Apache icon could not be converted into a Fyne resource")
	}
	data := icon.Content()
	signature := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	if !bytes.HasPrefix(data, signature) {
		t.Fatal("converted icon is not a PNG image")
	}
}
