package ingest

import (
	"os"
	"path/filepath"
)

// ResumeSeed tells a resumed watcher to skip the content already present in a
// specific transcript file. The watcher applies Cur only when it discovers
// exactly Path, so a CLI that resumes into a NEW file is unaffected (#40).
type ResumeSeed struct {
	Path string // the existing transcript the resumed CLI appends to
	Cur  Cursor // cursor past the content present at spawn (skip up to here)
}

// ResumeSeedFor returns a ResumeSeed that makes a resumed watcher skip the
// transcript a CLI is continuing, or nil when no seed is needed. It is called at
// spawn time (before the resumed CLI writes), so the snapshot captures exactly
// the pre-resume content — a prompt typed right after resume lands past it and is
// still ingested (Codex P2-round2).
//
// Only Copilot appends to its prior file on resume
// (~/.copilot/session-state/{id}/events.jsonl); reading that from offset 0 would
// re-log the whole prior conversation (Codex P1). Claude and Codex resume into a
// NEW session file (new uuid / new rollout), so their fresh file needs no seed and
// they return nil. A missing/unreadable file also returns nil (the watcher then
// starts at 0, which is correct for a not-yet-created file).
func ResumeSeedFor(cliType, sessionID string) *ResumeSeed {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return resumeSeedForRoot(home, cliType, sessionID)
}

// resumeSeedForRoot is ResumeSeedFor with the home root injected, so tests can
// point it at a t.TempDir() instead of touching the developer's real ~/.copilot
// (Copilot review).
func resumeSeedForRoot(home, cliType, sessionID string) *ResumeSeed {
	if !safeSessionIDComponent(sessionID) {
		return nil
	}
	switch cliType {
	case "copilot":
		p := filepath.Join(home, ".copilot", "session-state", sessionID, "events.jsonl")
		info, err := os.Stat(p)
		if err != nil {
			return nil
		}
		return &ResumeSeed{Path: p, Cur: Cursor{Offset: info.Size()}}
	default:
		// Claude/Codex resume into a new file → fresh watcher, no seed needed.
		return nil
	}
}
