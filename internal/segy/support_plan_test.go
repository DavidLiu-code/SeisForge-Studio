package segy

import (
	"math"
	"reflect"
	"testing"
)

func TestTraceSupportPlanOmitsUnusedInputsAndPreservesColumns(t *testing.T) {
	indices := make([]int64, 100)
	positions := make([]float64, 100)
	for i := range indices {
		indices[i], positions[i] = int64(i*3), float64(i*i+i)
	}
	plan, err := BuildTraceSupportPlan(indices, positions, positions[3], positions[96], 17)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Columns) != 17 || plan.InputTraceCount != 100 || len(plan.TraceIndices) >= 40 {
		t.Fatalf("support plan did not stay output-driven: columns=%d input=%d support=%d", len(plan.Columns), plan.InputTraceCount, len(plan.TraceIndices))
	}
	for x, column := range plan.Columns {
		if column.Left < 0 || column.Left >= len(plan.TraceIndices) || column.Right < 0 || column.Right >= len(plan.TraceIndices) {
			t.Fatalf("column %d contains an invalid support index: %+v", x, column)
		}
		target := positions[3] + float64(x)*(positions[96]-positions[3])/16
		leftTrace, rightTrace := plan.TraceIndices[column.Left], plan.TraceIndices[column.Right]
		leftSource, rightSource := int(leftTrace/3), int(rightTrace/3)
		want := positions[leftSource]*(1-column.Fraction) + positions[rightSource]*column.Fraction
		if math.Abs(want-target) > 1e-9 {
			t.Fatalf("column %d changed interpolation target: got=%g want=%g", x, want, target)
		}
	}
}

func TestTraceSupportPlanMatchesLegacyDuplicatePositionRule(t *testing.T) {
	plan, err := BuildTraceSupportPlan([]int64{10, 11, 12, 13}, []float64{0, 1, 1, 3}, 0, 3, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.TraceIndices, []int64{10, 11, 12, 13}) {
		t.Fatalf("unexpected support order: %v", plan.TraceIndices)
	}
	if plan.Columns[1].Right != 1 || plan.Columns[1].Fraction != 1 {
		t.Fatalf("duplicate-position search no longer matches legacy rule: %+v", plan.Columns[1])
	}
}
