// Package sanitize provides shared rune classifiers for cleaning free text that
// is written verbatim to an agent PTY: the room charter (team.sanitizeCharter,
// startup-prompt path) and broadcast injection (pty.InjectText). The two call
// sites consume these predicates differently (drop vs flatten-to-space, with or
// without preserving \n/\t), but share the classification so they cannot drift —
// previously each hand-maintained a duplicate control/format list and both
// omitted characters (e.g. U+061C and the zero-width set).
package sanitize

import (
	"strings"
	"unicode"
)

// StripForTerminalPaste cleans free text that will be written verbatim into a
// bracketed-paste startup injection (Claude/Gemini agent startup prompts). It
// removes the bracketed-paste markers (\x1b[200~ / \x1b[201~) first — a raw one
// could end paste mode early and run the rest as live keystrokes — then drops
// every C0/C1 control byte, DEL, and invisible Unicode format rune (Trojan-Source
// class), preserving only \n and \t so multi-line prose survives.
//
// Shared by the room charter (team.sanitizeCharter), the room summary, and the
// summarized transcript (#29) — all of which reach the same paste sink — so the
// cleaning cannot drift between them.
func StripForTerminalPaste(text string) string {
	const (
		bracketOpen  = "\x1b[200~"
		bracketClose = "\x1b[201~"
	)
	text = strings.ReplaceAll(text, bracketOpen, "")
	text = strings.ReplaceAll(text, bracketClose, "")
	return strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\t':
			return r
		}
		if IsControl(r) || IsInvisibleFormat(r) {
			return -1
		}
		return r
	}, text)
}

// IsControl reports C0 control bytes, DEL, and C1 control bytes (the 8-bit forms
// of ESC-prefixed sequences, e.g. U+009B ≈ CSI). These are unsafe as live
// keystrokes and corrupt bracketed-paste streams.
//
// NOTE: \n (0x0A) and \t (0x09) are C0 controls and report true here. Callers that
// want to keep them inside a bracketed paste must special-case them before
// consulting this predicate.
func IsControl(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// IsInvisibleFormat reports invisible Unicode format characters — the whole Cf
// category (bidi controls incl. LRM/RLM and ARABIC LETTER MARK, the bidi
// embeddings/overrides/isolates, the zero-width set ZWSP/ZWNJ/ZWJ/WORD JOINER,
// BOM/ZWNBSP, and the Tags block used for invisible-payload smuggling) plus the
// line/paragraph separators U+2028/U+2029 (categories Zl/Zp, which are not Cf).
//
// These can make the text a human reviews differ from the bytes an agent actually
// receives (Trojan-Source / invisible-payload class). Using the unicode.Cf range
// table (rather than a hand-maintained list) keeps the set complete and drift-free.
// ZWJ/ZWNJ also occur in legitimate emoji sequences and Persian/Arabic text;
// stripping them is an intentional choice for the agent-command context, where
// invisible formatting carries no functional meaning.
func IsInvisibleFormat(r rune) bool {
	return unicode.Is(unicode.Cf, r) || r == 0x2028 || r == 0x2029
}
