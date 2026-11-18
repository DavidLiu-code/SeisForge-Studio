// Package segyanalysis contains read-only, UI-independent calculations used
// by lightweight SEG-Y quality-control views.
package segyanalysis

import (
	"errors"
	"math"
	"sort"
)

// TraceStats summarizes one decoded trace. Population standard deviation is
// used because the samples describe the complete selected trace window, not a
// statistical sample of a larger population.
//
// P01 through P99 are exact, linearly interpolated percentiles of finite
// values. Non-finite values are counted but never enter the numeric summary.
// ZeroCount counts both +0 and -0 as valid zero-amplitude samples.
type TraceStats struct {
	SampleCount    int
	FiniteCount    int
	NonFiniteCount int
	ZeroCount      int
	FiniteRatio    float64
	ZeroRatio      float64
	HasFinite      bool

	Min     float64
	Max     float64
	Mean    float64
	RMS     float64
	StdDev  float64
	PeakAbs float64

	P01 float64
	P05 float64
	P50 float64
	P95 float64
	P99 float64
}

// ComputeTraceStats calculates a trace summary without modifying samples.
// Empty or entirely non-finite input returns HasFinite=false and NaN for every
// value statistic, while retaining the sample counters.
func ComputeTraceStats(samples []float64) TraceStats {
	stats := TraceStats{SampleCount: len(samples)}
	finite := make([]float64, 0, len(samples))
	minimum, maximum := math.Inf(1), math.Inf(-1)

	for _, value := range samples {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			stats.NonFiniteCount++
			continue
		}
		finite = append(finite, value)
		stats.FiniteCount++
		if value == 0 {
			stats.ZeroCount++
		}
		if value < minimum {
			minimum = value
		}
		if value > maximum {
			maximum = value
		}
		absolute := math.Abs(value)
		if absolute > stats.PeakAbs {
			stats.PeakAbs = absolute
		}
	}

	if stats.SampleCount > 0 {
		stats.FiniteRatio = float64(stats.FiniteCount) / float64(stats.SampleCount)
	}
	if stats.FiniteCount > 0 {
		stats.ZeroRatio = float64(stats.ZeroCount) / float64(stats.FiniteCount)
	}
	stats.HasFinite = stats.FiniteCount > 0
	if !stats.HasFinite {
		stats.Min, stats.Max, stats.Mean = math.NaN(), math.NaN(), math.NaN()
		stats.RMS, stats.StdDev, stats.PeakAbs = math.NaN(), math.NaN(), math.NaN()
		stats.P01, stats.P05, stats.P50, stats.P95, stats.P99 = math.NaN(), math.NaN(), math.NaN(), math.NaN(), math.NaN()
		return stats
	}

	stats.Min, stats.Max = minimum, maximum
	if stats.PeakAbs != 0 {
		// Welford differences and squared amplitudes can overflow despite
		// finite inputs and representable final mean/RMS/stddev. Move into
		// an exact power-of-two scale first; center the Welford recurrence on
		// the first value so nearby high-amplitude samples are distinguishable.
		_, exp := math.Frexp(stats.PeakAbs)
		anchor := math.Ldexp(finite[0], -exp)
		mean, m2, sq, sum, correction := 0.0, 0.0, 0.0, 0.0, 0.0
		for i, value := range finite {
			x := math.Ldexp(value, -exp)
			sq += x * x
			// Compensated sum for the reported mean preserves a small value
			// when large positive and negative amplitudes cancel out.
			next := sum + x
			if math.Abs(sum) >= math.Abs(x) {
				correction += (sum - next) + x
			} else {
				correction += (x - next) + sum
			}
			sum = next
			centered := x - anchor
			delta := centered - mean
			mean += delta / float64(i+1)
			m2 += delta * (centered - mean)
		}
		count, peak := float64(len(finite)), math.Ldexp(stats.PeakAbs, -exp)
		stats.Mean = math.Ldexp(max(-peak, min(peak, (sum+correction)/count)), exp)
		stats.RMS = math.Ldexp(min(peak, math.Sqrt(sq/count)), exp)
		stats.StdDev = math.Ldexp(min(peak, math.Sqrt(max(0, m2/count))), exp)
	}

	sort.Float64s(finite)
	stats.P01 = percentileSorted(finite, 1)
	stats.P05 = percentileSorted(finite, 5)
	stats.P50 = percentileSorted(finite, 50)
	stats.P95 = percentileSorted(finite, 95)
	stats.P99 = percentileSorted(finite, 99)
	return stats
}

// FinitePercentile returns an exact linearly interpolated percentile after
// omitting NaN and infinite values. Percent is inclusive in [0,100].
func FinitePercentile(samples []float64, percent float64) (float64, error) {
	if math.IsNaN(percent) || math.IsInf(percent, 0) || percent < 0 || percent > 100 {
		return 0, errors.New("percentile must be finite and between 0 and 100")
	}
	finite := make([]float64, 0, len(samples))
	for _, value := range samples {
		if !math.IsNaN(value) && !math.IsInf(value, 0) {
			finite = append(finite, value)
		}
	}
	if len(finite) == 0 {
		return 0, errors.New("percentile requires at least one finite sample")
	}
	sort.Float64s(finite)
	return percentileSorted(finite, percent), nil
}

func percentileSorted(sortedValues []float64, percent float64) float64 {
	if len(sortedValues) == 1 {
		return sortedValues[0]
	}
	position := percent / 100 * float64(len(sortedValues)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return sortedValues[lower]
	}
	fraction := position - float64(lower)
	a, b := sortedValues[lower], sortedValues[upper]
	if span := b - a; !math.IsInf(span, 1) {
		return a + fraction*span
	}
	// The span only overflows across opposite-signed finite endpoints. The
	// weighted endpoint sum remains finite and requires no out-of-range span.
	return (1-fraction)*a + fraction*b
}
