package pty

import "testing"

func TestSanitizeCopilotInput(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "collapses crlf and flattens controls",
			in:   "one\r\ntwo	three\x1b[31m",
			want: "one two three [31m",
		},
		{
			name: "flattens lone newlines and carriage returns",
			in:   "one\ntwo\rthree",
			want: "one two three",
		},
		{
			name: "flattens embedded paste markers as literal keystrokes",
			in:   "before" + bracketPasteOpen + "middle" + bracketPasteClose + "after",
			want: "before [200~middle [201~after",
		},
		{
			name: "drops invisible format runes",
			in:   "safe\u202Etext",
			want: "safetext",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeCopilotInput(tt.in); got != tt.want {
				t.Fatalf("sanitizeCopilotInput(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSanitizeBracketedPasteInput(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "removes embedded paste markers",
			in:   "before" + bracketPasteOpen + "middle" + bracketPasteClose + "after",
			want: "beforemiddleafter",
		},
		{
			name: "preserves newline and tab but strips other controls",
			in:   "one\ntwo\tthree\x1b[31m",
			want: "one\ntwo\tthree[31m",
		},
		{
			name: "drops invisible format runes",
			in:   "safe\u202Etext",
			want: "safetext",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeBracketedPasteInput(tt.in); got != tt.want {
				t.Fatalf("sanitizeBracketedPasteInput(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
