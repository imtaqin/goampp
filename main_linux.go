//go:build linux

// GoAMPP's native Linux entry point. The Windows application remains
// isolated in main.go and retains its existing behaviour.
package main

import "goampp/internal/linuxapp"

func main() {
	linuxapp.Run()
}
