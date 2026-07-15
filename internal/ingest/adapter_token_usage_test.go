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

// TestCopilotParseUsage covers the REAL session.shutdown shape, whose aggregate
// usage lives under data.modelMetrics[<model>].usage (NOT data.usage) — the plain
// data.usage schema (some turn events) is exercised by the subtest (#10 follow-up).
func TestCopilotParseUsage(t *testing.T) {
	// Real shutdown fixture (verified from a live events.jsonl): aggregate under
	// data.modelMetrics[currentModel].usage.
	p := writeLines(t, []string{
		`{"type":"user.message","data":{"content":"hi"}}`,
		`{"type":"session.shutdown","data":{"currentModel":"gpt-5.3-codex","modelMetrics":{"gpt-5.3-codex":{"usage":{"inputTokens":387154,"outputTokens":3912,"cacheReadTokens":345216,"cacheWriteTokens":0,"reasoningTokens":1552}}}}}`,
	})
	snap, err := copilotAdapter{}.ParseUsage(p)
	if err != nil || snap == nil {
		t.Fatalf("snap=%v err=%v", snap, err)
	}
	if snap.Kind != usage.KindTokenCount || snap.InputTokens != 387154 || snap.OutputTokens != 3912 || snap.CacheTokens != 345216 || snap.Model != "gpt-5.3-codex" {
		t.Fatalf("copilot modelMetrics usage yanlış: %+v", snap)
	}

	// Legacy/plain data.usage schema (some turn events) must still be read.
	t.Run("plainDataUsage", func(t *testing.T) {
		p := writeLines(t, []string{
			`{"type":"user.message","data":{"content":"hi"}}`,
			`{"type":"session.shutdown","data":{"usage":{"inputTokens":300,"outputTokens":40,"cacheReadTokens":250},"currentModel":"gpt-5.3-codex"}}`,
		})
		snap, err := copilotAdapter{}.ParseUsage(p)
		if err != nil || snap == nil {
			t.Fatalf("snap=%v err=%v", snap, err)
		}
		if snap.Kind != usage.KindTokenCount || snap.InputTokens != 300 || snap.OutputTokens != 40 || snap.CacheTokens != 250 || snap.Model != "gpt-5.3-codex" {
			t.Fatalf("copilot data.usage yanlış: %+v", snap)
		}
	})
}

// TestGeminiParseUsage uses the REAL chat schema: `tokens` is an OBJECT
// {input,output,cached,...}, NOT an integer. The prior fixture modeled it as an
// integer, which was WRONG — with `Tokens int64` json.Unmarshal failed on the first
// tokenized message and Gemini usage never appeared. The object already separates
// input/output, so the summing is per-field with no user/model heuristic (#10
// follow-up).
func TestGeminiParseUsage(t *testing.T) {
	p := filepath.Join(t.TempDir(), "session-x.json")
	os.WriteFile(p, []byte(`{"sessionId":"g","messages":[{"type":"gemini","model":"gemini-2.5-pro","tokens":{"input":8636,"output":29,"cached":2889,"thoughts":106,"tool":0,"total":8771}},{"type":"gemini","model":"gemini-2.5-pro","tokens":{"input":1000,"output":11,"cached":100,"total":1111}},{"type":"gemini","tokens":{"input":0,"output":0,"cached":0,"total":0}}]}`), 0600)
	snap, err := geminiAdapter{}.ParseUsage(p)
	if err != nil || snap == nil {
		t.Fatalf("snap=%v err=%v", snap, err)
	}
	// Per-field sum across messages (the all-zero message is skipped):
	// input 8636+1000=9636, output 29+11=40, cached 2889+100=2989.
	if snap.Kind != usage.KindTokenCount || snap.InputTokens != 9636 || snap.OutputTokens != 40 || snap.CacheTokens != 2989 {
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
