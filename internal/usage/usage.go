// Package usage models per-CLI usage/limit signals extracted from CLI session
// files and evaluates them against warn/critical thresholds. Pure and std-only
// (leaf, like internal/sanitize) so it is CLI-agnostic and fully unit-testable.
package usage

// Kind classifies the usage signal a Snapshot carries.
type Kind int

const (
	KindNone Kind = iota // no usable signal (badge hidden / "—")
	// KindPercentLimit: authoritative used-percent + reset (Codex). Drives color
	// status and auto-switch.
	KindPercentLimit
	// KindTokenCount: consumption only, no denominator (Claude/Copilot/Gemini).
	// Display-only — never colored, never triggers auto-switch.
	KindTokenCount
)

// Window is one Codex rate-limit window. WindowMinutes identifies it (10080 =
// weekly, 300 = 5h, etc.); NO semantic meaning is attached to which slot
// (primary/secondary) a window occupies — the CLI orders them arbitrarily and
// the secondary slot is frequently null.
type Window struct {
	UsedPercent   float64 `json:"usedPercent"`
	WindowMinutes int     `json:"windowMinutes"`
	ResetsAt      int64   `json:"resetsAt"` // epoch seconds; 0 = unknown
}

// Snapshot is one terminal's latest usage reading. Primary/Secondary are non-nil
// only for KindPercentLimit (Codex); token fields carry consumption for every CLI.
type Snapshot struct {
	SessionID    string  `json:"sessionID"`
	CLI          string  `json:"cli"`
	Kind         Kind    `json:"kind"`
	Primary      *Window `json:"primary,omitempty"`
	Secondary    *Window `json:"secondary,omitempty"`
	PlanType     string  `json:"planType,omitempty"`
	InputTokens  int64   `json:"inputTokens,omitempty"`
	OutputTokens int64   `json:"outputTokens,omitempty"`
	CacheTokens  int64   `json:"cacheTokens,omitempty"`
	Model        string  `json:"model,omitempty"`
	UpdatedAt    int64   `json:"updatedAt"` // epoch seconds, stamped by the caller
}

// Thresholds are the warn/critical used-percent cutoffs (0..100).
type Thresholds struct {
	WarnPercent     float64 `json:"warnPercent"`
	CriticalPercent float64 `json:"criticalPercent"`
}

// Status is the evaluated severity of a Snapshot.
type Status int

const (
	StatusUnknown Status = iota // no percent signal (token-only / empty)
	StatusOK
	StatusWarn
	StatusCritical
)

// DefaultThresholds is the shipped default (warn 85%, critical 95%).
func DefaultThresholds() Thresholds { return Thresholds{WarnPercent: 85, CriticalPercent: 95} }

// Normalized coerces out-of-range or unset thresholds back to defaults, keeping
// warn < critical. A zero value (unset settings.json) yields DefaultThresholds.
func (t Thresholds) Normalized() Thresholds {
	d := DefaultThresholds()
	if t.WarnPercent <= 0 || t.WarnPercent > 100 {
		t.WarnPercent = d.WarnPercent
	}
	if t.CriticalPercent <= 0 || t.CriticalPercent > 100 {
		t.CriticalPercent = d.CriticalPercent
	}
	if t.CriticalPercent < t.WarnPercent {
		t.CriticalPercent = t.WarnPercent
	}
	return t
}

// MaxUsedPercent returns the larger used_percent across the non-nil windows and
// whether any window existed. Basis for the badge and Evaluate.
func MaxUsedPercent(s Snapshot) (float64, bool) {
	max, ok := 0.0, false
	for _, wnd := range []*Window{s.Primary, s.Secondary} {
		if wnd == nil {
			continue
		}
		if !ok || wnd.UsedPercent > max {
			max, ok = wnd.UsedPercent, true
		}
	}
	return max, ok
}

// Evaluate maps a Snapshot to a Status. Only KindPercentLimit with at least one
// window yields a colored status; everything else (token-only, empty) is
// StatusUnknown so it never triggers the switch dialog.
func Evaluate(s Snapshot, t Thresholds) Status {
	if s.Kind != KindPercentLimit {
		return StatusUnknown
	}
	pct, ok := MaxUsedPercent(s)
	if !ok {
		return StatusUnknown
	}
	th := t.Normalized()
	switch {
	case pct >= th.CriticalPercent:
		return StatusCritical
	case pct >= th.WarnPercent:
		return StatusWarn
	default:
		return StatusOK
	}
}
