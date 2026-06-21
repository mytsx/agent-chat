package sanitize

import "testing"

func TestIsControl(t *testing.T) {
	// C0 (incl. \t, \n), DEL, and C1 are control. Runes are hex code points so the
	// test source stays pure ASCII.
	control := []rune{0x00, 0x09 /*tab*/, 0x0a /*newline*/, 0x1b /*ESC*/, 0x1f, 0x7f /*DEL*/, 0x80, 0x9b /*C1 CSI*/, 0x9f}
	for _, r := range control {
		if !IsControl(r) {
			t.Errorf("IsControl(U+%04X) = false, want true", r)
		}
	}
	notControl := []rune{' ', 'a', 'Z', '0', 0x00e7 /*ç*/, 0x011f /*ğ*/, 0x00a0 /*NBSP > C1*/, 0x202e /*bidi, not C0/C1*/}
	for _, r := range notControl {
		if IsControl(r) {
			t.Errorf("IsControl(U+%04X) = true, want false", r)
		}
	}
}

func TestIsInvisibleFormat(t *testing.T) {
	// All Unicode Cf format chars (bidi controls incl. ALM, zero-width set, BOM,
	// Tags block) plus the line/paragraph separators must report true.
	invisible := []rune{
		0x061c,         // ARABIC LETTER MARK (Cf bidi)
		0x200b,         // ZERO WIDTH SPACE
		0x200c,         // ZERO WIDTH NON-JOINER
		0x200d,         // ZERO WIDTH JOINER
		0x2060,         // WORD JOINER
		0x200e, 0x200f, // LRM / RLM
		0x202a, 0x202e, // bidi embedding / override
		0x2066, 0x2069, // bidi isolates
		0xfeff,         // BOM / ZWNBSP
		0x2028, 0x2029, // line / paragraph separators (Zl/Zp, not Cf)
		0xe0061,        // a Tags-block char (invisible-payload smuggling)
	}
	for _, r := range invisible {
		if !IsInvisibleFormat(r) {
			t.Errorf("IsInvisibleFormat(U+%04X) = false, want true", r)
		}
	}
	// Visible / ordinary runes (incl. plain space, newline, Turkish letters, an
	// emoji base, a CJK char) must report false.
	visible := []rune{'a', ' ', '\n', 0x00e7 /*ç*/, 0x1f680 /*🚀*/, 0x4e2d /*中*/}
	for _, r := range visible {
		if IsInvisibleFormat(r) {
			t.Errorf("IsInvisibleFormat(U+%04X) = true, want false", r)
		}
	}
}
