package ingest

import (
	"os"
	"path/filepath"
	"testing"

	"desktop/internal/usage"
)

func TestClaudeParseUsage(t *testing.T) {
	p := writeLines(t, []string{
		`{"type":"user","message":{"role":"user","content":"selam"}}`,
		`{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":20}}}`,
		`{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"more"}],"usage":{"input_tokens":8,"output_tokens":7,"cache_read_input_tokens":50,"cache_creation_input_tokens":0}}}`,
	})
	snap, err := claudeAdapter{}.ParseUsage(p)
	if err != nil || snap == nil {
		t.Fatalf("snap=%v err=%v", snap, err)
	}
	if snap.Kind != usage.KindTokenCount {
		t.Fatalf("Kind = %v", snap.Kind)
	}
	if snap.Primary != nil || snap.Secondary != nil {
		t.Fatal("token-CLI'da limit penceresi olmamalı")
	}
	if snap.InputTokens != 18 || snap.OutputTokens != 12 {
		t.Fatalf("token toplamı yanlış: in=%d out=%d", snap.InputTokens, snap.OutputTokens)
	}
	if snap.CacheTokens != 170 {
		t.Fatalf("cache toplamı = %d, want 170", snap.CacheTokens)
	}
	if snap.Model != "claude-opus-4-8" {
		t.Fatalf("Model = %q", snap.Model)
	}
}

func TestCopilotParseUsage(t *testing.T) {
	p := writeLines(t, []string{
		`{"type":"user.message","data":{"content":"hi"}}`,
		`{"type":"session.shutdown","data":{"usage":{"inputTokens":300,"outputTokens":40,"cacheReadTokens":250},"currentModel":"gpt-5.3-codex"}}`,
	})
	snap, err := copilotAdapter{}.ParseUsage(p)
	if err != nil || snap == nil {
		t.Fatalf("snap=%v err=%v", snap, err)
	}
	if snap.Kind != usage.KindTokenCount || snap.InputTokens != 300 || snap.OutputTokens != 40 || snap.CacheTokens != 250 || snap.Model != "gpt-5.3-codex" {
		t.Fatalf("copilot usage yanlış: %+v", snap)
	}
}

func TestGeminiParseUsage(t *testing.T) {
	p := filepath.Join(t.TempDir(), "session-x.json")
	os.WriteFile(p, []byte(`{"sessionId":"g","messages":[{"type":"user","tokens":5},{"type":"gemini","model":"gemini-2.5-pro","tokens":30,"cached":12},{"type":"gemini","tokens":10,"cached":3}]}`), 0600)
	snap, err := geminiAdapter{}.ParseUsage(p)
	if err != nil || snap == nil {
		t.Fatalf("snap=%v err=%v", snap, err)
	}
	// user-message tokens → InputTokens (5); model-message tokens → OutputTokens (30+10=40).
	if snap.Kind != usage.KindTokenCount || snap.InputTokens != 5 || snap.OutputTokens != 40 || snap.CacheTokens != 15 {
		t.Fatalf("gemini token toplamı yanlış: %+v", snap)
	}
	if snap.Model != "gemini-2.5-pro" {
		t.Fatalf("Model = %q", snap.Model)
	}
}

func TestParseUsageNoSignal(t *testing.T) {
	p := writeLines(t, []string{`{"type":"user","message":{"role":"user","content":"x"}}`})
	snap, err := claudeAdapter{}.ParseUsage(p)
	if err != nil || snap != nil {
		t.Fatalf("sinyalsiz dosyada nil beklendi: snap=%v err=%v", snap, err)
	}
}
