package mcpserver

import (
	"encoding/json"
	"testing"
)

func TestInvalidNamesResult(t *testing.T) {
	t.Parallel()

	if result := invalidNamesResult("agent", "room"); result != nil {
		t.Fatalf("invalidNamesResult(valid names) returned error result: %#v", result)
	}
	if result := invalidNamesResult("bad/name", "room"); result == nil {
		t.Fatal("invalidNamesResult(invalid agent) returned nil")
	}
	if result := invalidNamesResult("agent", "bad/name"); result == nil {
		t.Fatal("invalidNamesResult(invalid room) returned nil")
	}
}

func TestExtractLastMessageID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data json.RawMessage
		want int
	}{
		{
			name: "numeric last id",
			data: json.RawMessage([]byte("{\"last_id\":42}")),
			want: 42,
		},
		{
			name: "missing last id",
			data: json.RawMessage([]byte("{\"text\":\"ok\"}")),
			want: 0,
		},
		{
			name: "malformed json",
			data: json.RawMessage(`not-json`),
			want: 0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := extractLastMessageID(tt.data); got != tt.want {
				t.Fatalf("extractLastMessageID() = %d, want %d", got, tt.want)
			}
		})
	}
}
