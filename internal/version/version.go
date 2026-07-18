// Package version provides dependency-free semantic-version parsing and comparison
// used by the in-app update check (#83). It intentionally avoids golang.org/x/mod
// (not a module dependency) — a small, well-tested SemVer 2.0.0 subset is enough to
// decide whether a GitHub release tag is newer than the embedded build version.
//
// All entry points fail safe: an unparseable input never causes a panic, and the
// higher-level helpers (Newer, IsStable) return the conservative answer (no update)
// so a malformed tag can only ever hide a banner, never show a wrong one.
package version

import (
	"strconv"
	"strings"
)

// Semver is a parsed MAJOR.MINOR.PATCH triple with an optional prerelease label.
// Build metadata (everything after '+') is discarded during parsing because it does
// not participate in precedence.
type Semver struct {
	Major int
	Minor int
	Patch int
	Pre   string // prerelease identifier without the leading '-'; "" when absent
}

// Parse parses a version string of the form [vV]MAJOR.MINOR.PATCH[-prerelease][+build].
// Surrounding whitespace and a single leading 'v'/'V' are tolerated. It returns
// ok=false for any malformed input (wrong field count, non-numeric or negative core
// fields, empty fields) rather than guessing.
func Parse(s string) (Semver, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Semver{}, false
	}
	if s[0] == 'v' || s[0] == 'V' {
		s = s[1:]
	}
	// Strip build metadata (does not affect precedence).
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	// Split off the prerelease label.
	var pre string
	if i := strings.IndexByte(s, '-'); i >= 0 {
		pre = s[i+1:]
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Semver{}, false
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, ok := parseNonNegInt(p)
		if !ok {
			return Semver{}, false
		}
		nums[i] = n
	}
	return Semver{Major: nums[0], Minor: nums[1], Patch: nums[2], Pre: pre}, true
}

// parseNonNegInt parses a non-empty, all-digit, non-negative integer. strconv.Atoi
// would accept a leading '+' or '-'; we reject those explicitly so only clean numeric
// core fields pass.
func parseNonNegInt(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// Compare returns -1, 0, or 1 as a is less than, equal to, or greater than b, per
// SemVer 2.0.0 precedence: compare MAJOR.MINOR.PATCH numerically, then apply the
// prerelease rule (a version with a prerelease has lower precedence than the same
// version without one).
func Compare(a, b Semver) int {
	if c := cmpInt(a.Major, b.Major); c != 0 {
		return c
	}
	if c := cmpInt(a.Minor, b.Minor); c != 0 {
		return c
	}
	if c := cmpInt(a.Patch, b.Patch); c != 0 {
		return c
	}
	return comparePre(a.Pre, b.Pre)
}

// comparePre implements SemVer 2.0.0 prerelease precedence. Empty (no prerelease) has
// the HIGHEST precedence. Otherwise identifiers are compared dot-by-dot: numeric
// identifiers compare numerically and rank below alphanumeric ones; a larger set of
// identifiers wins when all preceding fields are equal.
func comparePre(a, b string) int {
	if a == b {
		return 0
	}
	if a == "" {
		return 1 // a is the normal (higher) version, b has a prerelease
	}
	if b == "" {
		return -1
	}
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if c := comparePreField(as[i], bs[i]); c != 0 {
			return c
		}
	}
	return cmpInt(len(as), len(bs))
}

func comparePreField(a, b string) int {
	aNum := isNumericIdent(a)
	bNum := isNumericIdent(b)
	switch {
	case aNum && bNum:
		// Both are numeric identifiers WITHOUT a leading zero (guaranteed by
		// isNumericIdent), so the longer string is the larger number and equal-length
		// strings compare correctly lexically. This avoids an int overflow on a very
		// large identifier (strconv.Atoi would fail) and needs no numeric parse.
		if len(a) != len(b) {
			return cmpInt(len(a), len(b))
		}
		return strings.Compare(a, b)
	case aNum && !bNum:
		return -1 // numeric identifiers have lower precedence than alphanumeric
	case !aNum && bNum:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

// isNumericIdent reports whether s is a SemVer 2.0.0 numeric prerelease identifier:
// all digits and, per the spec, WITHOUT a leading zero. So "1" is numeric but "01" is
// alphanumeric (compared lexically) — which changes precedence, e.g. alpha.1 < alpha.01.
func isNumericIdent(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s == "0" || s[0] != '0'
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// Newer reports whether latest is strictly newer than current. Either input may carry
// a leading 'v'. If either fails to parse, Newer returns false so a malformed value
// can only suppress a banner, never trigger a spurious one.
func Newer(latest, current string) bool {
	l, okL := Parse(latest)
	c, okC := Parse(current)
	if !okL || !okC {
		return false
	}
	return Compare(l, c) > 0
}

// IsStable reports whether s parses as a version with no prerelease label — the only
// kind of release the update banner should advertise (recommendation 3: stable only).
func IsStable(s string) bool {
	v, ok := Parse(s)
	return ok && v.Pre == ""
}

// IsDevBuild reports whether the embedded build version indicates a non-release build,
// for which the update check must be skipped (no nagging during `make dev`/`make
// build`). It treats the known placeholders ("", "dev", "unknown", and the 0.1.0
// default in any form), any value carrying a "dirty" working-tree marker, and anything
// that does not parse as a version as a dev build.
func IsDevBuild(v string) bool {
	t := strings.TrimSpace(v)
	switch strings.ToLower(t) {
	case "", "dev", "unknown":
		return true
	}
	if strings.Contains(strings.ToLower(t), "dirty") {
		return true
	}
	parsed, ok := Parse(t)
	if !ok {
		return true // unparseable → treat as dev, don't nag
	}
	// The 0.1.0 placeholder (Makefile / wails.json default) in ANY form — bare,
	// prerelease ("0.1.0-rc1"), or with build metadata — is a dev build, not a real
	// release. Parsing first (rather than a "0.1.0" string prefix) avoids
	// misclassifying a genuine "0.1.05" and correctly catches the "v0.1.0" form.
	if parsed.Major == 0 && parsed.Minor == 1 && parsed.Patch == 0 {
		return true
	}
	return false
}
