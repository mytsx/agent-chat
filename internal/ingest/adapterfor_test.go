package ingest

import "testing"

func TestAdapterFor(t *testing.T) {
	for _, ai := range []string{"claude", "copilot", "codex", "gemini"} {
		if AdapterFor(ai) == nil {
			t.Errorf("AdapterFor(%q) = nil, want an adapter", ai)
		}
	}
	for _, no := range []string{"", "shell", "bash", "unknown"} {
		if AdapterFor(no) != nil {
			t.Errorf("AdapterFor(%q) != nil, want nil (not an ingestable CLI)", no)
		}
	}
}
