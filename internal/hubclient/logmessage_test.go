package hubclient

import (
	"encoding/json"
	"testing"
)

// LogMessage must put the app-supplied delivery timestamp into the log_message
// payload so the hub can use it instead of stamping at RPC-arrival time —
// otherwise the timestamp threading (#58) is silently a no-op (the hub always
// falls back to its own clock).
func TestLogMessageData_IncludesTimestamp(t *testing.T) {
	raw := logMessageData("backend", "şu dosyayı düzelt", "2026-06-22T09:00:00.000000")

	var got struct {
		To        string `json:"to"`
		Content   string `json:"content"`
		Timestamp string `json:"timestamp"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got.To != "backend" {
		t.Errorf("to = %q, want backend", got.To)
	}
	if got.Content != "şu dosyayı düzelt" {
		t.Errorf("content = %q", got.Content)
	}
	if got.Timestamp != "2026-06-22T09:00:00.000000" {
		t.Errorf("timestamp = %q, want the supplied delivery timestamp", got.Timestamp)
	}
}
