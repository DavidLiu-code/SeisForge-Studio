package segyanalysis

import (
	"math"
	"testing"
)

func TestReferenceSNREnergyAndMasks(t *testing.T) {
	for _, tt := range []struct {
		name  string
		a, b  []float64
		pairs int64
		state ReferenceSNRState
		db    float64
	}{
		{"same", []float64{4, -2}, []float64{4, -2}, 2, SNRPerfect, math.Inf(1)},
		{"twice the clean reference", []float64{2, 4}, []float64{1, 2}, 2, SNRFinite, 0},
		{"one and a half times the clean reference", []float64{1.5, 3}, []float64{1, 2}, 2, SNRFinite, 20 * math.Log10(2)},
		{"noisy input is zero", []float64{0, 0}, []float64{4, -2}, 2, SNRFinite, 0},
		{"true zeros", []float64{0, 0}, []float64{0, 0}, 2, SNRBothZero, math.NaN()},
		{"zero reference", []float64{1, 0}, []float64{0, 0}, 2, SNRZeroReference, math.Inf(-1)},
		{"aggregate", []float64{2, 11}, []float64{1, 10}, 2, SNRFinite, 10 * math.Log10(101.0/2)},
		{"mask", []float64{2, math.NaN(), 6}, []float64{1, 3, 2}, 2, SNRFinite, 10 * math.Log10(5.0/17)},
		{"unavailable", []float64{math.NaN(), 1}, []float64{0, math.Inf(1)}, 0, SNRUnavailable, math.NaN()},
		{"huge opposite sign", []float64{math.MaxFloat64}, []float64{-math.MaxFloat64}, 1, SNRFinite, -20 * math.Log10(2)},
		{"subnormal", []float64{2 * math.SmallestNonzeroFloat64}, []float64{math.SmallestNonzeroFloat64}, 1, SNRFinite, 0},
		{"mixed scales", []float64{math.MaxFloat64, math.SmallestNonzeroFloat64}, []float64{math.MaxFloat64, 0}, 2, SNRFinite, 20 * ((1024 + 1074) * math.Log10(2))},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var acc ReferenceSNRAccumulator
			for i := range tt.a {
				acc.Add(tt.a[i], tt.b[i])
			}
			r := acc.Result()
			if r.State != tt.state || r.Pairs != tt.pairs || r.FormulaVersion != ReferenceSNRFormulaVersion {
				t.Fatalf("got %+v; expected state %v %d pairs", r, tt.state, tt.pairs)
			}
			if r.DB != tt.db && !(math.IsNaN(r.DB) && math.IsNaN(tt.db)) && !(math.Abs(r.DB-tt.db) < 1e-10) {
				t.Fatalf("got %.12g dB, want %.12g", r.DB, tt.db)
			}
			t.Logf("computed %.12g dB; independent reference %.12g dB; %d valid pairs", r.DB, tt.db, r.Pairs)
		})
	}
}

func TestReferenceSNRUsesSameAlignedWindowAsTraceSnapshot(t *testing.T) {
	// A's two outer samples are not covered by B. B has an invalid sample in
	// the common window. Both facts must exclude those values from both energies.
	a := TraceInput{"A", 794000, 2000, []float64{10000, 999, 777, 1, 10, 999, 20000}}
	b := TraceInput{"B", 800000, 2000, []float64{0, 9, math.NaN()}}
	var region ReferenceSNRAccumulator
	if reason := region.AddAligned(a, b); reason != "" {
		t.Fatal(reason)
	}
	s := BuildTraceSnapshot([]TraceInput{a, b}, true)
	if s.ReferenceSNR != region.Result() || s.ReferenceSNR.Pairs != 2 || s.ComparisonStartUS != 800000 || s.ComparisonIntervalUS != 2000 || s.ComparisonOverlapSamples != 3 || !math.IsNaN(s.Series[2].Samples[2]) {
		t.Fatalf("trace/region disagree: trace %+v, region %+v", s, region.Result())
	}
	b.StartUS++
	before := region.Result()
	if reason := region.AddAligned(a, b); reason == "" || region.Result() != before {
		t.Fatalf("off-grid input was paired: %v, %+v", reason, region.Result())
	}
}
