package main

import (
	"testing"
	"time"
)

func TestMLGovernorGracePeriodDecay(t *testing.T) {
	g := NewMLGovernor()
	softMax := 58000
	hardMax := 62000

	// 1. Below soft max: should be exactly baseline idleGraceMs (1500ms)
	if grace := g.PredictGraceMs(1000, softMax, hardMax); grace != 1500 {
		t.Fatalf("expected 1500ms below softMax, got %d ms", grace)
	}
	if grace := g.PredictGraceMs(58000, softMax, hardMax); grace != 1500 {
		t.Fatalf("expected 1500ms at softMax, got %d ms", grace)
	}

	// 2. Midpoint between softMax and hardMax: 60000 (headroom = 2000, range = 4000) -> 50% decay: 750ms
	mid := g.PredictGraceMs(60000, softMax, hardMax)
	if mid != 750 {
		t.Fatalf("expected 750ms at midpoint, got %d ms", mid)
	}

	// 3. Near hardMax: 61900 (headroom = 100) -> decays smoothly towards 100ms floor
	nearHard := g.PredictGraceMs(61900, softMax, hardMax)
	if nearHard != 100 { // (100 * 1500) / 4000 = 37.5 -> clamped to 100ms
		t.Fatalf("expected 100ms floor, got %d ms", nearHard)
	}

	// 4. At hardMax or above: emergency floor 50ms
	atHard := g.PredictGraceMs(62000, softMax, hardMax)
	if atHard != 50 {
		t.Fatalf("expected 50ms at hardMax, got %d ms", atHard)
	}
}

func TestMLGovernorQConvergence(t *testing.T) {
	g := NewMLGovernor()
	softMax := 58000

	// Initial prior for small batch (< 4) should favor opportunistic drain (action 1)
	if !g.ShouldDrainExtra(2, 1000, softMax) {
		t.Fatalf("expected ShouldDrainExtra(2) = true initially")
	}

	// Initial prior for large batch (>= 16) should favor immediate execution (action 0)
	if g.ShouldDrainExtra(20, 1000, softMax) {
		t.Fatalf("expected ShouldDrainExtra(20) = false initially")
	}

	// Simulate high throughput with immediate execution (no coalescing) on small batches
	for i := 0; i < 50; i++ {
		g.Update(2, 1000, softMax, 50*time.Microsecond, false, false)
	}
}

func BenchmarkMLGovernorPredictGrace(b *testing.B) {
	g := NewMLGovernor()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = g.PredictGraceMs(59000, 58000, 62000)
	}
}

func BenchmarkMLGovernorShouldDrainExtra(b *testing.B) {
	g := NewMLGovernor()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = g.ShouldDrainExtra(4, 10000, 58000)
	}
}

func BenchmarkMLGovernorUpdate(b *testing.B) {
	g := NewMLGovernor()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		g.Update(8, 20000, 58000, 150*time.Microsecond, true, true)
	}
}
