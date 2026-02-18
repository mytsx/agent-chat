package validation

import (
	"strings"
	"testing"
)

func TestValidateName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		// Accept cases
		{"empty string (default)", "", false},
		{"simple name", "Agent-1", false},
		{"underscore name", "my_room", false},
		{"space in name", "my room", false},
		{"digits only", "12345", false},
		{"single char", "a", false},
		{"50 chars exactly", strings.Repeat("a", 50), false},
		{"mixed valid chars", "Agent_1 Team-2", false},
		{"dot in name", "api.v2", false},
		{"version style", "release-1.0", false},
		{"file style", "file.txt", false},

		// Reject: path traversal
		{"path traversal unix", "../../etc/passwd", true},
		{"double dot in middle", "a..b", true},
		{"double dot only", "..", true},
		{"double dot prefix", "..foo", true},

		// Reject: too long
		{"51 chars", strings.Repeat("a", 51), true},
		{"100 chars", strings.Repeat("x", 100), true},

		// Reject: forbidden characters
		{"forward slash", "foo/bar", true},
		{"backslash", "foo\\bar", true},
		{"emoji", "agent\U0001F600", true},
		{"null byte", "agent\x00name", true},
		{"newline", "agent\nname", true},
		{"tab", "agent\tname", true},
		{"leading dot", ".hidden", false},
		{"colon", "foo:bar", true},
		{"semicolon", "foo;bar", true},
		{"angle bracket", "foo<bar>", true},
		{"pipe", "foo|bar", true},
		{"at sign", "foo@bar", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}
