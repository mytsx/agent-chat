package ingest

// AdapterFor returns the session adapter for a CLI type, or nil if that CLI is
// not ingestable (plain shell, empty, or unknown).
func AdapterFor(cliType string) SessionAdapter {
	switch cliType {
	case "claude":
		return claudeAdapter{}
	case "copilot":
		return copilotAdapter{}
	case "codex":
		return codexAdapter{}
	case "gemini":
		return geminiAdapter{}
	default:
		return nil
	}
}

// Compile-time checks: every AI-CLI adapter implements UsageParser (#10).
var (
	_ UsageParser = codexAdapter{}
	_ UsageParser = claudeAdapter{}
	_ UsageParser = copilotAdapter{}
	_ UsageParser = geminiAdapter{}
)
