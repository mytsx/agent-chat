package hub

import (
	"reflect"
	"testing"

	"desktop/internal/types"
)

func TestDeriveHistoricalAgents(t *testing.T) {
	sys := func(content string) types.Message {
		return types.Message{Content: content, Type: types.MsgTypeSystem}
	}
	tests := []struct {
		name string
		msgs []types.Message
		want []string
	}{
		{
			name: "distinct join names sorted",
			msgs: []types.Message{
				sys("\U0001f7e2 Coder2 odaya katıldı (Rol: worker)"),
				sys("\U0001f7e2 Coder1 odaya katıldı"),
				sys("\U0001f534 Coder1 odadan ayrıldı"),
				sys("\U0001f7e2 Coder1 odaya katıldı"), // tekrar join → tek kez
			},
			want: []string{"Coder1", "Coder2"},
		},
		{
			name: "leave-only noise ignored",
			msgs: []types.Message{sys("\U0001f534 Ghost odadan ayrıldı")},
			want: nil,
		},
		{
			name: "non-system messages ignored",
			msgs: []types.Message{
				{Content: "\U0001f7e2 Fake odaya katıldı", Type: types.MsgTypeDirect},
			},
			want: nil,
		},
		{
			name: "name with space preserved",
			msgs: []types.Message{sys("\U0001f7e2 Coder 1 odaya katıldı")},
			want: []string{"Coder 1"},
		},
		{
			name: "empty input",
			msgs: nil,
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deriveHistoricalAgents(tt.msgs)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("deriveHistoricalAgents = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestSummary_HistoricalOnlyWhenRosterEmpty(t *testing.T) {
	// Roster boş oda → historical dolu.
	empty := NewRoomState()
	empty.mu.Lock()
	empty.messages = []types.Message{
		{Content: "\U0001f7e2 Solo odaya katıldı", Type: types.MsgTypeSystem, Timestamp: "2026-01-01T00:00:00Z"},
	}
	empty.mu.Unlock()
	s := empty.Summary("orphan", false)
	if !reflect.DeepEqual(s.HistoricalAgents, []string{"Solo"}) {
		t.Fatalf("HistoricalAgents = %#v, want [Solo]", s.HistoricalAgents)
	}

	// Roster dolu oda → historical nil.
	live := NewRoomState()
	live.mu.Lock()
	live.agents["Live1"] = types.Agent{Role: "worker"}
	live.messages = []types.Message{
		{Content: "\U0001f7e2 Live1 odaya katıldı", Type: types.MsgTypeSystem, Timestamp: "2026-01-01T00:00:00Z"},
	}
	live.mu.Unlock()
	if s2 := live.Summary("live", false); s2.HistoricalAgents != nil {
		t.Fatalf("roster doluyken HistoricalAgents nil olmalı, got %#v", s2.HistoricalAgents)
	}
}
