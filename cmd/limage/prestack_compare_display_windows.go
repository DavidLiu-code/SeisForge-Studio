//go:build windows

package main

import (
	"math"
	"sort"
)

// Compare's float-reader deliberately returns unquantized values; unlike the
// ordinary RenderWithOptions path it has not yet applied display gain. Match
// mapRenderValues' legacy g / (100-g) order statistics here, using one pooled
// range for A/B. ClipPercent is not an extra percentile in that legacy path.
// Only current screen rasters are copied, never source trace/sample volumes.
func prestackCompareSharedDisplayRange(a, b []float64, gain float64, useLimits bool, minimum, maximum float64) (float64, float64) {
	if useLimits && prestackCompareRangeIsValid(true, minimum, maximum) {
		return minimum, maximum
	}
	gain = prestackCompareDisplayGain(gain)
	lo, hi := math.Inf(1), math.Inf(-1)
	var ordered []float64
	if gain > 0 {
		ordered = make([]float64, 0, len(a)+len(b))
	}
	for _, values := range [][]float64{a, b} {
		for _, value := range values {
			if !finiteFloat(value) {
				continue // Missing-side slots are not zero-amplitude samples.
			}
			lo, hi = math.Min(lo, value), math.Max(hi, value)
			if gain > 0 {
				ordered = append(ordered, value)
			}
		}
	}
	if !finiteFloat(lo) || !finiteFloat(hi) {
		return -1, 1
	}
	if len(ordered) > 0 {
		sort.Float64s(ordered)
		lo = ordered[prestackComparePercentileIndex(len(ordered), gain)]
		hi = ordered[prestackComparePercentileIndex(len(ordered), 100-gain)]
	}
	if hi <= lo {
		hi = lo + 1 // Identical to mapRenderValues' constant-raster fallback.
	}
	if !finiteFloat(hi) || hi <= lo {
		return -1, 1
	}
	return lo, hi
}

// Use the ordinary A-B renderer's absolute-value percentile, then reflect it
// around zero. Manual limits override gain but are symmetrized for Delta.
func prestackCompareDeltaDisplayRange(difference []float64, gain float64, useLimits bool, minimum, maximum float64) (float64, float64) {
	amp := 0.0
	if useLimits && prestackCompareRangeIsValid(true, minimum, maximum) {
		amp = math.Max(math.Abs(minimum), math.Abs(maximum))
	} else {
		gain = prestackCompareDisplayGain(gain)
		var ordered []float64
		if gain > 0 {
			ordered = make([]float64, 0, len(difference))
		}
		for _, value := range difference {
			if !finiteFloat(value) {
				continue
			}
			value = math.Abs(value)
			amp = math.Max(amp, value)
			if gain > 0 {
				ordered = append(ordered, value)
			}
		}
		if len(ordered) > 0 {
			sort.Float64s(ordered)
			amp = ordered[prestackComparePercentileIndex(len(ordered), 100-gain)]
		}
	}
	if amp <= 0 || !finiteFloat(amp) {
		amp = 1
	}
	return -amp, amp
}

func prestackCompareDisplayGain(gain float64) float64 {
	if !finiteFloat(gain) {
		return 0
	}
	return math.Max(0, math.Min(49, gain))
}

func prestackComparePercentileIndex(count int, percent float64) int {
	return clampInt(int(float64(count)*percent/100), 0, maxInt(0, count-1))
}
