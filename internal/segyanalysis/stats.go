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
	mean, m2 := 0.0, 0.0
	rmsScale, rmsSSQ := 0.0, 1.0

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

		// Welford's online update avoids the cancellation in E[x^2]-E[x]^2.
		delta := value - mean
		mean += delta / float64(stats.FiniteCount)
		m2 += delta * (value - mean)

		// A scaled sum of squares keeps RMS finite for a much wider range than
		// directly accumulating value*value.
		if absolute != 0 {
			if rmsScale < absolute {
				ratio := rmsScale / absolute
				rmsSSQ = 1 + rmsSSQ*ratio*ratio
				rmsScale = absolute
			} else {
				ratio := absolute / rmsScale
				rmsSSQ += ratio * ratio
			}
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

	stats.Min, stats.Max, stats.Mean = minimum, maximum, mean
	if rmsScale == 0 {
		stats.RMS = 0
	} else {
		stats.RMS = rmsScale * math.Sqrt(rmsSSQ/float64(stats.FiniteCount))
	}
	variance := m2 / float64(stats.FiniteCount)
	// Rounding can make an otherwise zero population variance slightly
	// negative; do not turn that harmless error into a NaN result.
	if variance < 0 && variance > -1e-15*math.Max(1, mean*mean) {
		variance = 0
	}
	stats.StdDev = math.Sqrt(variance)

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
	return sortedValues[lower] + fraction*(sortedValues[upper]-sortedValues[lower])
}
