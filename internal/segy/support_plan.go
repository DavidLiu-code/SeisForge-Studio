package segy

import (
	"errors"
	"math"
	"sort"
)

// TraceSupportColumn describes the two source traces and horizontal fraction
// used by one output column. Left and Right index TraceSupportPlan.TraceIndices.
type TraceSupportColumn struct {
	Left, Right int
	Fraction    float64
}

// TraceSupportPlan is an output-driven list of the unique source traces that
// can affect a non-uniformly spaced section raster. Traces that do not
// contribute to any output column are deliberately omitted.
type TraceSupportPlan struct {
	Columns         []TraceSupportColumn
	TraceIndices    []int64
	InputTraceCount int
}

// BuildTraceSupportPlan preserves the exact search and interpolation rules of
// the legacy smooth crooked-line renderer while deduplicating its inputs.
func BuildTraceSupportPlan(traceIndices []int64, positions []float64, positionStart, positionEnd float64, width int) (TraceSupportPlan, error) {
	if width < 2 || len(traceIndices) < 2 || len(traceIndices) != len(positions) {
		return TraceSupportPlan{}, errors.New("trace support inputs are empty or unmatched")
	}
	for i, position := range positions {
		if math.IsNaN(position) || math.IsInf(position, 0) || (i > 0 && position < positions[i-1]) {
			return TraceSupportPlan{}, errors.New("trace support positions must be finite and monotonically increasing")
		}
	}
	if positionStart < positions[0] {
		positionStart = positions[0]
	}
	if positionEnd > positions[len(positions)-1] || positionEnd <= positionStart {
		positionEnd = positions[len(positions)-1]
	}
	if positionEnd <= positionStart {
		return TraceSupportPlan{}, errors.New("trace support range has zero width")
	}

	plan := TraceSupportPlan{Columns: make([]TraceSupportColumn, width), InputTraceCount: len(traceIndices)}
	supportBySource := make(map[int]int, width*2)
	addSupport := func(source int) int {
		if support, ok := supportBySource[source]; ok {
			return support
		}
		support := len(plan.TraceIndices)
		supportBySource[source] = support
		plan.TraceIndices = append(plan.TraceIndices, traceIndices[source])
		return support
	}
	for x := 0; x < width; x++ {
		target := positionStart + float64(x)*(positionEnd-positionStart)/float64(width-1)
		right := sort.Search(len(positions), func(i int) bool { return positions[i] >= target })
		if right >= len(positions) {
			right = len(positions) - 1
		}
		left, fraction := right, 0.0
		if right > 0 {
			left = right - 1
			if span := positions[right] - positions[left]; span > 0 {
				fraction = (target - positions[left]) / span
			}
		}
		plan.Columns[x] = TraceSupportColumn{Left: addSupport(left), Right: addSupport(right), Fraction: fraction}
	}
	return plan, nil
}
