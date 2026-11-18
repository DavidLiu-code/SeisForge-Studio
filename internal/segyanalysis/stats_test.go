package segyanalysis

import (
	"math"
	"testing"
)

func nearlyEqual(left, right, tolerance float64) bool {
	return math.Abs(left-right) <= tolerance
}

func TestComputeTraceStatsCountsAndMoments(t *testing.T) {
	stats := ComputeTraceStats([]float64{-2, 0, 2, 4, math.NaN(), math.Inf(1)})
	if stats.SampleCount != 6 || stats.FiniteCount != 4 || stats.NonFiniteCount != 2 || stats.ZeroCount != 1 || !stats.HasFinite {
		t.Fatalf("unexpected counters: %+v", stats)
	}
	if stats.Min != -2 || stats.Max != 4 || stats.Mean != 1 || stats.PeakAbs != 4 {
		t.Fatalf("unexpected amplitude summary: %+v", stats)
	}
	if !nearlyEqual(stats.RMS, math.Sqrt(6), 1e-12) || !nearlyEqual(stats.StdDev, math.Sqrt(5), 1e-12) {
		t.Fatalf("unexpected RMS/stddev: %+v", stats)
	}
	if stats.P50 != 1 || !nearlyEqual(stats.FiniteRatio, 4.0/6.0, 1e-12) || stats.ZeroRatio != 0.25 {
		t.Fatalf("unexpected ratios/percentile: %+v", stats)
	}
}

func TestComputeTraceStatsAllZeroConstantAndNoFinite(t *testing.T) {
	zero := ComputeTraceStats(make([]float64, 8))
	if !zero.HasFinite || zero.ZeroCount != 8 || zero.Min != 0 || zero.Max != 0 || zero.RMS != 0 || zero.StdDev != 0 || zero.P99 != 0 {
		t.Fatalf("unexpected all-zero stats: %+v", zero)
	}
	constant := ComputeTraceStats([]float64{3.5, 3.5, 3.5})
	if constant.Mean != 3.5 || constant.RMS != 3.5 || constant.StdDev != 0 || constant.P01 != 3.5 || constant.P99 != 3.5 {
		t.Fatalf("unexpected constant stats: %+v", constant)
	}
	invalid := ComputeTraceStats([]float64{math.NaN(), math.Inf(-1)})
	if invalid.HasFinite || invalid.FiniteCount != 0 || invalid.NonFiniteCount != 2 || !math.IsNaN(invalid.Min) || !math.IsNaN(invalid.P50) {
		t.Fatalf("unexpected non-finite-only stats: %+v", invalid)
	}
}

func TestFinitePercentileInterpolatesAndRetainsFiniteExtreme(t *testing.T) {
	value, err := FinitePercentile([]float64{10, math.NaN(), 0}, 25)
	if err != nil || value != 2.5 {
		t.Fatalf("unexpected interpolated percentile: value=%g err=%v", value, err)
	}
	stats := ComputeTraceStats([]float64{-999999, -2, -1, 0, 1, 2, 3, 4, 5, 6, 7})
	if stats.Min != -999999 || stats.PeakAbs != 999999 || stats.FiniteCount != 11 {
		t.Fatalf("finite extreme was silently discarded: %+v", stats)
	}
	if _, err = FinitePercentile([]float64{1}, -1); err == nil {
		t.Fatal("invalid percentile was accepted")
	}
	if _, err = FinitePercentile([]float64{math.NaN()}, 50); err == nil {
		t.Fatal("non-finite-only percentile was accepted")
	}
}

func TestFiniteExtremeTraceStatisticsStayRepresentable(t *testing.T) {
	max := math.MaxFloat64
	stats := ComputeTraceStats([]float64{-max, math.NaN(), max})
	if stats.FiniteCount != 2 || stats.NonFiniteCount != 1 {
		t.Fatalf("unexpected counts: %+v", stats)
	}
	if stats.Min != -max || stats.Max != max || !(math.Abs(stats.Mean) <= max*1e-15) || !(math.Abs(stats.StdDev/max-1) <= 1e-15) || !(math.Abs(stats.RMS/max-1) <= 1e-15) {
		t.Errorf("extreme finite statistics: mean=%g rms=%g stddev=%g, want 0 / %g / %g", stats.Mean, stats.RMS, stats.StdDev, max, max)
	}
	if !(math.Abs(stats.P01/max+.98) <= 1e-15) || stats.P50 != 0 || !(math.Abs(stats.P99/max-.98) <= 1e-15) {
		t.Errorf("extreme finite percentiles: %g %g %g, want [-0.98 0 0.98]*MaxFloat64", stats.P01, stats.P50, stats.P99)
	}
	median, err := FinitePercentile([]float64{-max, max}, 50)
	if err != nil || median != 0 {
		t.Errorf("pooled median %g, %v; want finite zero", median, err)
	}
	if got := ComputeTraceStats([]float64{-max, 1, max}).Mean; !(math.Abs(got-1.0/3) <= 1e-15) {
		t.Errorf("mean with finite cancellation = %g, want 1/3", got)
	}
	t.Logf("±MaxFloat64: mean=%g stddev=%g RMS=%g P01/P50/P99=%g/%g/%g", stats.Mean, stats.StdDev, stats.RMS, stats.P01, stats.P50, stats.P99)
}
