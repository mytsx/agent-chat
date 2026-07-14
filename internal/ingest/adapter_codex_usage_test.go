package ingest

import (
	"os"
	"path/filepath"
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
