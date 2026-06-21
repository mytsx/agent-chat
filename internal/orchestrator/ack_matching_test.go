package orchestrator

import (
	"testing"

	"desktop/internal/types"
)

// TestAckWordBoundary, ack sınıflandırmasının kelime-sınırı duyarlı olduğunu
// doğrular: kısa pattern'ler ("ok") alakasız kelimelerin içinde eşleşmemeli
// (yanlış SKIP'i önler), ama gerçek ack'ler — Türkçe ekli formlar dahil —
// hâlâ SKIP edilmeli.
func TestAckWordBoundary(t *testing.T) {
	// Bu mesajlar ACK DEĞİL → notify edilmeli (önceki substring kodunda yanlışlıkla
	// skip ediliyordu çünkü "ok"/"oldu" başka kelimelerin içinde eşleşiyordu).
	notAcks := []string{
		"doktor randevum var yarin", // "ok" ⊂ "doktor"
		"okula gidiyorum simdi",     // "ok" kelime başı ama "okul" devamı
		"kitap okudum bu hafta",     // "ok" ⊂ "okudum"
		"sokakta bekliyorum seni",   // "ok" ⊂ "sokak"
		"soldu cicekler bahcede",    // "oldu" ⊂ "soldu" (sol önekli)
		"cok yogun bir konu vardi",  // "ok" ⊂ "cok"
		"hokey oynadik bugun",       // "okey"/"ok" ⊂ "hokey" (sol önekli)
		"poker gecesi yapalim",      // "oke"/"ok" ⊂ "poker" (sol önekli)
		"build status_ok degil mi",  // "ok" ⊂ "status_ok" (alt çizgi sınırı)
		"oldukca mutluyum bugun",    // "oldu" ⊂ "oldukca" (stem-önekli, tam-kelime fix)
		"pekistirmek lazim bunu",    // "peki" ⊂ "pekistirmek" (stem-önekli)
		"hayirli olsun yeni isin",   // "hayir" ⊂ "hayirli" (stem-önekli)
		"guzellesmek istiyorum hep", // "guzel" ⊂ "guzellesmek" (stem-önekli)
	}
	for _, msg := range notAcks {
		t.Run("notAck/"+msg, func(t *testing.T) {
			res := AnalyzeMessage(types.Message{Content: msg})
			if res.Action == "skip" {
				t.Errorf("yanlış SKIP: %q ack değil ama atlandı (reason=%q)", msg, res.Reason)
			}
		})
	}

	// Bu mesajlar GERÇEK ack → skip edilmeli (davranış korunmalı). Türkçe ekli
	// formlar (tesekkurler, tamamdir) ve standalone "ok" dahil.
	acks := []string{
		"ok",              // standalone kısa pattern
		"tamam ok",        // "ok" kelime olarak (sonda)
		"tamam",           // stem
		"tamamdir",        // ayrı pattern (whole-word)
		"tesekkurler",     // ayrı eklenen ekli form (whole-word)
		"tesekkur ederim", // "tesekkur" + boşluk (tam kelime)
		"sagol kardesim",  // stem + devam
		"harika olmus",    // "harika"
		"okay",            // "ok" değil ama "okay" pattern'i
		"thanks",          // ingilizce
		"got it",          // çok kelimeli ingilizce
		"perfect",         //
		"okey",            // yaygın Türkçe ack (eklendi)
		"oke",             // yaygın Türkçe ack (eklendi, kısa → tam kelime)
	}
	for _, msg := range acks {
		t.Run("ack/"+msg, func(t *testing.T) {
			res := AnalyzeMessage(types.Message{Content: msg})
			if res.Action != "skip" {
				t.Errorf("ack SKIP edilmedi: %q (action=%s reason=%q)", msg, res.Action, res.Reason)
			}
		})
	}
}

// TestMatchesAckPattern, eşleştirme yardımcısının sınır mantığını izole test eder.
func TestMatchesAckPattern(t *testing.T) {
	cases := []struct {
		s, p string
		want bool
	}{
		{"ok", "ok", true},                   // tam kelime
		{"tamam ok", "ok", true},             // sonda, soldan boşluk
		{"okul", "ok", false},                // kısa pattern, sağ sınır yok
		{"doktor", "ok", false},              // kısa pattern, sol sınır yok
		{"cok", "ok", false},                 // sol harf (c)
		{"tesekkurler", "tesekkur", false},   // tam-kelime: "tesekkur" + "ler" eki → false
		{"tesekkurler", "tesekkurler", true}, // ekli form artık ayrı pattern
		{"soldu", "oldu", false},             // sol harf (s)
		{"oldukca", "oldu", false},           // stem-önekli: "oldu" + "kca" → false
		{"oldu", "oldu", true},               // tam kelime
		{"bugun oldu", "oldu", true},         // kelime başı (boşluk sonrası)
		{"pekistirmek", "peki", false},       // stem-önekli: "peki" + "stirmek" → false
		{"okey", "okey", true},               // uzun pattern (4), tam
		{"hokey", "okey", false},             // sol harf (h)
		{"oke", "oke", true},                 // kısa pattern (3), tam kelime
		{"poker", "oke", false},              // kısa pattern, sol harf (p)
		{"okey", "oke", false},               // "oke" ⊂ "okey", sağ harf (y)
		{"status_ok", "ok", false},           // alt çizgi kelime karakteri (sol _)
		{"ok_done", "ok", false},             // alt çizgi kelime karakteri (sağ _)
		{"doktor ok", "ok", true},            // ilk (doktor) başarısız, geç eşleşme bulunur (skip-ahead)
		{"elbettelbette", "elbette", false},  // örtüşen occurrence — ikisi de harf-komşu, whole-word değil
		{"elbette", "elbette", true},         // standalone whole-word
	}
	for _, c := range cases {
		if got := matchesAckPattern(c.s, c.p); got != c.want {
			t.Errorf("matchesAckPattern(%q, %q) = %v; istenen %v", c.s, c.p, got, c.want)
		}
	}
}
