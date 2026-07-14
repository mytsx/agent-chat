package main

import "testing"

func TestUsageThresholdsRoundTrip(t *testing.T) {
	a := &App{dataDir: t.TempDir()}
	// default (dosya yok)
	def := a.GetUsageThresholds()
	if def.WarnPercent != 85 || def.CriticalPercent != 95 {
		t.Fatalf("default eşik = %+v", def)
	}
	if err := a.SetUsageThresholds(70, 90); err != nil {
		t.Fatal(err)
	}
	got := a.GetUsageThresholds()
	if got.WarnPercent != 70 || got.CriticalPercent != 90 {
		t.Fatalf("kayıt sonrası = %+v", got)
	}
	// bozuk aralık normalize edilir (crit<warn → crit=warn)
	if err := a.SetUsageThresholds(80, 50); err != nil {
		t.Fatal(err)
	}
	n := a.GetUsageThresholds()
	if n.CriticalPercent < n.WarnPercent {
		t.Fatalf("normalize edilmedi: %+v", n)
	}
	// deferral ayarı korunmalı (aynı dosya)
	if err := a.SetDeferralEnabled(true); err != nil {
		t.Fatal(err)
	}
	if !a.GetDeferralEnabled() {
		t.Fatal("deferral usage kaydında ezildi")
	}
	if a.GetUsageThresholds().WarnPercent != 80 {
		t.Fatal("usage eşiği deferral kaydında ezildi")
	}
}
