package version

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		in    string
		ok    bool
		major int
		minor int
		patch int
		pre   string
	}{
		{"0.6.0", true, 0, 6, 0, ""},
		{"v0.6.0", true, 0, 6, 0, ""},
		{"V1.2.3", true, 1, 2, 3, ""},
		{"  v10.20.30  ", true, 10, 20, 30, ""},
		{"1.0.0-rc1", true, 1, 0, 0, "rc1"},
		{"1.0.0-beta.2", true, 1, 0, 0, "beta.2"},
		{"1.2.3+build.5", true, 1, 2, 3, ""},        // build metadata stripped
		{"1.2.3-rc1+build.5", true, 1, 2, 3, "rc1"}, // pre kept, build stripped
		{"01.02.03", true, 1, 2, 3, ""},             // leading zeros tolerated
		{"dev", false, 0, 0, 0, ""},
		{"unknown", false, 0, 0, 0, ""},
		{"", false, 0, 0, 0, ""},
		{"1.2", false, 0, 0, 0, ""},     // too few
		{"1.2.3.4", false, 0, 0, 0, ""}, // too many
		{"1.2.x", false, 0, 0, 0, ""},   // non-numeric
		{"v.1.2", false, 0, 0, 0, ""},
		{"1..3", false, 0, 0, 0, ""},
		{"-1.2.3", false, 0, 0, 0, ""}, // negative
	}
	for _, tt := range tests {
		got, ok := Parse(tt.in)
		if ok != tt.ok {
			t.Errorf("Parse(%q) ok=%v, want %v", tt.in, ok, tt.ok)
			continue
		}
		if !ok {
			continue
		}
		if got.Major != tt.major || got.Minor != tt.minor || got.Patch != tt.patch || got.Pre != tt.pre {
			t.Errorf("Parse(%q) = %+v, want {%d %d %d %q}", tt.in, got, tt.major, tt.minor, tt.patch, tt.pre)
		}
	}
}

func TestCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.1", "1.0.0", 1},
		{"1.0.0", "1.0.1", -1},
		{"1.1.0", "1.0.9", 1},
		{"2.0.0", "1.9.9", 1},
		{"0.6.0", "0.10.0", -1}, // numeric, not lexical (6 < 10)
		{"0.10.0", "0.9.0", 1},
		{"v1.2.3", "1.2.3", 0}, // leading v ignored
		// prerelease precedence (SemVer 2.0.0): a pre-release version has lower
		// precedence than the associated normal version.
		{"1.0.0-rc1", "1.0.0", -1},
		{"1.0.0", "1.0.0-rc1", 1},
		{"1.0.0-alpha", "1.0.0-beta", -1},
		{"1.0.0-alpha.1", "1.0.0-alpha", 1},    // more fields wins
		{"1.0.0-alpha.1", "1.0.0-alpha.2", -1}, // numeric field compare
		{"1.0.0-1", "1.0.0-alpha", -1},         // numeric < alphanumeric
	}
	for _, tt := range tests {
		a, okA := Parse(tt.a)
		b, okB := Parse(tt.b)
		if !okA || !okB {
			t.Fatalf("Compare setup parse failed: %q(%v) %q(%v)", tt.a, okA, tt.b, okB)
		}
		if got := Compare(a, b); got != tt.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestNewer(t *testing.T) {
	tests := []struct {
		latest, current string
		want            bool
	}{
		{"0.7.0", "0.6.0", true},
		{"0.6.0", "0.6.0", false}, // equal
		{"0.5.0", "0.6.0", false}, // older
		{"1.0.0", "0.9.9", true},
		{"v0.7.0", "0.6.0", true},   // tag with v vs embedded without
		{"0.10.0", "0.9.0", true},   // numeric compare
		{"garbage", "0.6.0", false}, // unparseable latest → safe false (no banner)
		{"0.7.0", "dev", false},     // unparseable current → safe false
		{"", "", false},
		{"0.6.0-rc1", "0.6.0", false}, // prerelease latest is not newer than stable
	}
	for _, tt := range tests {
		if got := Newer(tt.latest, tt.current); got != tt.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tt.latest, tt.current, got, tt.want)
		}
	}
}

func TestIsStable(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"0.6.0", true},
		{"v1.2.3", true},
		{"1.0.0-rc1", false},
		{"1.0.0-beta.1", false},
		{"dev", false},
		{"", false},
		{"1.2", false},
	}
	for _, tt := range tests {
		if got := IsStable(tt.in); got != tt.want {
			t.Errorf("IsStable(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestIsDevBuild(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"dev", true},
		{"", true},
		{"unknown", true},
		{"0.1.0", true},       // Makefile/wails.json placeholder default
		{"  0.1.0 ", true},    // trimmed
		{"0.6.0-dirty", true}, // dirty working tree marker
		{"0.6.0-5-gabc-dirty", true},
		{"not-a-version", true}, // unparseable → treat as dev, don't nag
		{"0.6.0", false},        // real release
		{"v1.2.3", false},
		{"1.0.0-rc1", false}, // a prerelease build is not a dev build
	}
	for _, tt := range tests {
		if got := IsDevBuild(tt.in); got != tt.want {
			t.Errorf("IsDevBuild(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
