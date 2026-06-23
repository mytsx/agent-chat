package ingest

import "testing"

func TestFingerprintStore_ConsumeMatchesOnce(t *testing.T) {
	fs := newFingerprintStore()
	fs.Add("şu dosyayı düzelt")

	if !fs.Consume("şu dosyayı düzelt") {
		t.Fatal("first Consume of an added injection must match (suppress)")
	}
	if fs.Consume("şu dosyayı düzelt") {
		t.Fatal("second Consume must NOT match — the user genuinely retyping the same text is logged")
	}
}

func TestFingerprintStore_NormalizesWhitespaceAndPasteMarkers(t *testing.T) {
	fs := newFingerprintStore()
	fs.Add("merhaba dünya")
	// The CLI may store the message with a trailing newline / collapsed spaces /
	// stripped bracketed-paste markers — normalization must still match.
	if !fs.Consume("  merhaba   dünya\n") {
		t.Fatal("normalized variant of an injection must be suppressed")
	}
}

func TestFingerprintStore_UnrelatedNotConsumed(t *testing.T) {
	fs := newFingerprintStore()
	fs.Add("birinci mesaj")
	if fs.Consume("bambaşka bir mesaj") {
		t.Fatal("an unrelated (directly typed) message must NOT be suppressed")
	}
}

func TestNormalizeFingerprint(t *testing.T) {
	cases := map[string]string{
		"  hello   world \n":    "hello world",
		"a\x1b[200~b\x1b[201~c": "abc", // bracketed-paste markers stripped
		"line1\r\nline2":        "line1 line2",
	}
	for in, want := range cases {
		if got := normalizeFingerprint(in); got != want {
			t.Errorf("normalizeFingerprint(%q) = %q, want %q", in, got, want)
		}
	}
}
