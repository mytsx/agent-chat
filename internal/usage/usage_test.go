package usage

import "testing"

func w(p float64) *Window { return &Window{UsedPercent: p, WindowMinutes: 10080, ResetsAt: 1} }

func TestEvaluate(t *testing.T) {
	def := DefaultThresholds()
	tests := []struct {
		name string
		s    Snapshot
		th   Thresholds
		want Status
	}{
		{"nil-both-percentkind", Snapshot{Kind: KindPercentLimit}, def, StatusUnknown},
		{"primary-ok", Snapshot{Kind: KindPercentLimit, Primary: w(10)}, def, StatusOK},
		{"primary-warn-boundary", Snapshot{Kind: KindPercentLimit, Primary: w(85)}, def, StatusWarn},
		{"primary-just-below-warn", Snapshot{Kind: KindPercentLimit, Primary: w(84.9)}, def, StatusOK},
		{"primary-critical-boundary", Snapshot{Kind: KindPercentLimit, Primary: w(95)}, def, StatusCritical},
		{"secondary-drives-max", Snapshot{Kind: KindPercentLimit, Primary: w(10), Secondary: w(96)}, def, StatusCritical},
		{"tokencount-always-unknown", Snapshot{Kind: KindTokenCount, InputTokens: 999999}, def, StatusUnknown},
		{"none-kind-unknown", Snapshot{Kind: KindNone}, def, StatusUnknown},
		{"zero-thresholds-fall-back-to-default", Snapshot{Kind: KindPercentLimit, Primary: w(90)}, Thresholds{}, StatusWarn},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Evaluate(tt.s, tt.th); got != tt.want {
				t.Fatalf("Evaluate(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestMaxUsedPercent(t *testing.T) {
	if _, ok := MaxUsedPercent(Snapshot{Kind: KindPercentLimit}); ok {
		t.Fatal("nil slotlarda ok=true beklenmiyordu")
	}
	if v, ok := MaxUsedPercent(Snapshot{Primary: w(30), Secondary: w(70)}); !ok || v != 70 {
		t.Fatalf("max = %v ok=%v, want 70 true", v, ok)
	}
}
