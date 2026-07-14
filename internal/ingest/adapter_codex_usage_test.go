package ingest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"desktop/internal/usage"
)

func writeLines(t *testing.T, lines []string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rollout-test.jsonl")
	if err := os.WriteFile(p, []byte(joinNL(lines)), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
func joinNL(lines []string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}

func TestCodexParseUsage(t *testing.T) {
	lines := []string{
		`{"type":"session_meta","payload":{"id":"abc","cwd":"/x"}}`,
		`{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"gpt-5.6-sol"}}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}},"rate_limits":{"primary":{"used_percent":10.0,"window_minutes":10080,"resets_at":111},"secondary":null,"plan_type":"pro"}}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":200,"cached_input_tokens":80,"output_tokens":50}},"rate_limits":{"primary":{"used_percent":43.0,"window_minutes":10080,"resets_at":222},"secondary":null,"plan_type":"pro"}}}`,
	}
	p := writeLines(t, lines)
	snap, err := codexAdapter{}.ParseUsage(p)
	if err != nil {
		t.Fatal(err)
	}
	if snap == nil {
		t.Fatal("snapshot nil, beklenen dolu")
	}
	if snap.Kind != usage.KindPercentLimit {
		t.Fatalf("Kind = %v", snap.Kind)
	}
	if snap.Primary == nil || snap.Primary.UsedPercent != 43.0 || snap.Primary.WindowMinutes != 10080 || snap.Primary.ResetsAt != 222 {
		t.Fatalf("Primary son satırdan alınmadı: %+v", snap.Primary)
	}
	if snap.Secondary != nil {
		t.Fatalf("Secondary null olmalı: %+v", snap.Secondary)
	}
	if snap.PlanType != "pro" {
		t.Fatalf("PlanType = %q", snap.PlanType)
	}
	if snap.Model != "gpt-5.6-sol" {
		t.Fatalf("Model = %q", snap.Model)
	}
	if snap.InputTokens != 200 || snap.OutputTokens != 50 || snap.CacheTokens != 80 {
		t.Fatalf("token toplamı son satırdan alınmadı: in=%d out=%d cache=%d", snap.InputTokens, snap.OutputTokens, snap.CacheTokens)
	}
}

// TestCodexParseUsageHugeLine proves a single rollout record larger than the old
// 8 MiB bufio.Scanner cap no longer aborts the scan: a huge irrelevant line placed
// BEFORE a small valid rate_limits line must not prevent that later snapshot from
// being returned (#10). Under the old bufio.Scanner impl this returned
// bufio.ErrTooLong and froze usage tracking for the rest of the session.
func TestCodexParseUsageHugeLine(t *testing.T) {
	// A >8 MiB line the fast-path skips (no "event_msg"): a big tool-result-ish
	// record. strings.Repeat builds a valid-ish JSONL line well over the old cap.
	huge := `{"type":"response_item","payload":{"type":"tool_result","output":"` +
		strings.Repeat("A", 9*1024*1024) + `"}}`
	lines := []string{
		`{"type":"session_meta","payload":{"id":"abc","cwd":"/x"}}`,
		huge,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":300,"cached_input_tokens":90,"output_tokens":70}},"rate_limits":{"primary":{"used_percent":55.0,"window_minutes":10080,"resets_at":333},"secondary":null,"plan_type":"pro"}}}`,
	}
	p := writeLines(t, lines)
	snap, err := codexAdapter{}.ParseUsage(p)
	if err != nil {
		t.Fatalf("huge line aborted scan: %v", err)
	}
	if snap == nil {
		t.Fatal("snapshot nil — huge line before rate_limits swallowed the later usage")
	}
	if snap.Primary == nil || snap.Primary.UsedPercent != 55.0 || snap.Primary.ResetsAt != 333 {
		t.Fatalf("rate_limits after the >8MiB line not parsed: %+v", snap.Primary)
	}
	if snap.InputTokens != 300 || snap.OutputTokens != 70 || snap.CacheTokens != 90 {
		t.Fatalf("token totals after the >8MiB line not parsed: in=%d out=%d cache=%d", snap.InputTokens, snap.OutputTokens, snap.CacheTokens)
	}
}

func TestCodexParseUsageNoRateLimits(t *testing.T) {
	p := writeLines(t, []string{
		`{"type":"session_meta","payload":{"id":"abc"}}`,
		`{"type":"event_msg","payload":{"type":"user_message","message":"selam"}}`,
	})
	snap, err := codexAdapter{}.ParseUsage(p)
	if err != nil {
		t.Fatal(err)
	}
	if snap != nil {
		t.Fatalf("rate_limits yokken nil beklenirdi: %+v", snap)
	}
}
