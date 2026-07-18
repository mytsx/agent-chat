//go:build !darwin

package main

// enableNativeFullscreen is a no-op off macOS; the fullscreen-button fix is
// Cocoa-specific. The app ships only for macOS, but the stub keeps non-darwin
// tooling (go vet, editors) building.
func enableNativeFullscreen() {}
