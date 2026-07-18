package main

// Build metadata injected at link time via -ldflags "-X main.buildVersion=... " by
// scripts/build-universal.sh. Defaults identify a non-release (dev) build. buildVersion
// is intentionally NOT named "version" so it can't shadow the desktop/internal/version
// package if a future main-package file imports it.
var (
	buildVersion = "dev"
	commit       = "unknown"
	buildDate    = "unknown"
)
