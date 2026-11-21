//go:build darwin

package main

// handleCLI is a no-op on macOS, where removing the app bundle uninstalls it.
func handleCLI() bool { return false }
