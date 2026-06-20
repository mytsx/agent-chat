package team

import "testing"

func names(agents []AgentConfig) []string {
	out := make([]string, len(agents))
	for i, a := range agents {
		out[i] = a.Name
	}
	return out
}

func TestAgentsInOpenOrderSortsBySlotIndex(t *testing.T) {
	in := []AgentConfig{
		{Name: "C", SlotIndex: 2},
		{Name: "A", SlotIndex: 0},
		{Name: "B", SlotIndex: 1},
	}
	got := AgentsInOpenOrder(in)
	wantNames := []string{"A", "B", "C"}
	for i, n := range wantNames {
		if got[i].Name != n {
			t.Fatalf("order wrong at %d: got %v want %v", i, names(got), wantNames)
		}
		if got[i].SlotIndex != i {
			t.Fatalf("slot preserved wrong at %d: %+v", i, got[i])
		}
	}
}

// Legacy records (written before slot_index existed) all default to 0. They must
// fall back to positional slots in array order so they don't pile into slot 0.
func TestAgentsInOpenOrderLegacyAllZeroFallsBackToPositional(t *testing.T) {
	in := []AgentConfig{
		{Name: "First", SlotIndex: 0},
		{Name: "Second", SlotIndex: 0},
		{Name: "Third", SlotIndex: 0},
	}
	got := AgentsInOpenOrder(in)
	wantNames := []string{"First", "Second", "Third"}
	for i := range got {
		if got[i].Name != wantNames[i] {
			t.Fatalf("order changed: got %v want %v", names(got), wantNames)
		}
		if got[i].SlotIndex != i {
			t.Fatalf("expected positional slot %d, got %d for %s", i, got[i].SlotIndex, got[i].Name)
		}
	}
}

// Any duplicate slot collision triggers the positional fallback for the whole set.
func TestAgentsInOpenOrderDuplicateFallsBackToPositional(t *testing.T) {
	in := []AgentConfig{
		{Name: "X", SlotIndex: 1},
		{Name: "Y", SlotIndex: 1},
	}
	got := AgentsInOpenOrder(in)
	if got[0].SlotIndex != 0 || got[1].SlotIndex != 1 {
		t.Fatalf("expected positional fallback, got %+v", got)
	}
	if got[0].Name != "X" || got[1].Name != "Y" {
		t.Fatalf("expected array order preserved, got %v", names(got))
	}
}

func TestAgentsInOpenOrderUniqueNonContiguousPreserved(t *testing.T) {
	in := []AgentConfig{
		{Name: "A", SlotIndex: 3},
		{Name: "B", SlotIndex: 0},
	}
	got := AgentsInOpenOrder(in)
	if got[0].Name != "B" || got[0].SlotIndex != 0 {
		t.Fatalf("expected B at slot 0, got %+v", got[0])
	}
	if got[1].Name != "A" || got[1].SlotIndex != 3 {
		t.Fatalf("expected A at slot 3 preserved, got %+v", got[1])
	}
}

func TestAgentsInOpenOrderEmpty(t *testing.T) {
	if got := AgentsInOpenOrder(nil); len(got) != 0 {
		t.Fatalf("expected empty, got %v", got)
	}
}

func TestGridCapacity(t *testing.T) {
	cases := []struct {
		layout string
		want   int
	}{
		{"1x1", 1},
		{"2x2", 4},
		{"2x3", 6},
		{"3x4", 12},
		{"custom", -1}, // unlimited
		{"", -1},       // unparseable → unlimited (lenient, never wrongly skips)
		{"garbage", -1},
		{"2x0", -1},
		{"0x2", -1},
		{"2xabc", -1},
	}
	for _, c := range cases {
		if got := GridCapacity(c.layout); got != c.want {
			t.Errorf("GridCapacity(%q) = %d, want %d", c.layout, got, c.want)
		}
	}
}

// The input slice must not be mutated (returns a copy).
func TestAgentsInOpenOrderDoesNotMutateInput(t *testing.T) {
	in := []AgentConfig{
		{Name: "A", SlotIndex: 0},
		{Name: "B", SlotIndex: 0},
	}
	_ = AgentsInOpenOrder(in)
	if in[1].SlotIndex != 0 {
		t.Fatalf("input was mutated: %+v", in)
	}
}
