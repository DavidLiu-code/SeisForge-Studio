//go:build windows

package main

import (
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

func compareTraceSelectionTestGeometry(traces []int64) *segy.GeometryIndex {
	return &segy.GeometryIndex{
		TraceCount:      24,
		InlineByte:      189,
		CrosslineByte:   193,
		InlineValues:    []int32{10, 20},
		CrosslineValues: []int32{100, 110, 120},
		TraceNumbers:    append([]int64(nil), traces...),
		RowOfTrace:      []int32{0, 0, 0, 1, 1, 1},
		ColOfTrace:      []int32{0, 1, 2, 0, 1, 2},
		ValidTraceCount: 6,
		Poststack:       true,
	}
}

func installCompareTraceSelectionFixture(t *testing.T) func() {
	t.Helper()
	pathA := writePhase9PseudoFixture(t)
	pathB := writePhase9PseudoFixture(t)
	fileA, err := segy.Open(pathA)
	if err != nil {
		t.Fatal(err)
	}
	fileB, err := segy.Open(pathB)
	if err != nil {
		_ = fileA.Close()
		t.Fatal(err)
	}
	oldTD := compareTD
	oldMode, oldLine := compareMode, compareLineCoord
	oldXMin, oldXMax := compareLineXMin, compareLineXMax
	oldSampleStart, oldSampleEnd := compareSampleStart, compareSampleEnd
	oldBPath, oldBVisible, oldDiff := compareBPath, compareBVisible, compareShowDiff

	compareTD = &compareTimeData{
		fa: fileA, fb: fileB,
		ga:      compareTraceSelectionTestGeometry([]int64{0, 1, 2, 3, 4, 5}),
		gb:      compareTraceSelectionTestGeometry([]int64{6, 7, 8, 9, 10, 11}),
		sampleA: 12, sampleB: 14,
	}
	compareMode, compareLineCoord = 0, 20
	compareLineXMin, compareLineXMax = 100, 120
	compareSampleStart, compareSampleEnd = 5, 30
	compareBPath, compareBVisible, compareShowDiff = pathB, true, true

	return func() {
		_ = fileA.Close()
		_ = fileB.Close()
		compareTD = oldTD
		compareMode, compareLineCoord = oldMode, oldLine
		compareLineXMin, compareLineXMax = oldXMin, oldXMax
		compareSampleStart, compareSampleEnd = oldSampleStart, oldSampleEnd
		compareBPath, compareBVisible, compareShowDiff = oldBPath, oldBVisible, oldDiff
	}
}

func TestNearestCompareLineTraceUsesNearestGeometryCoordinate(t *testing.T) {
	trace, coordinate, ok := nearestCompareLineTrace([]int64{7, 9, 11}, []int32{100, 110, 120}, 116)
	if !ok || trace != 11 || coordinate != 120 {
		t.Fatalf("unexpected nearest trace: trace=%d coordinate=%d ok=%v", trace, coordinate, ok)
	}
	trace, coordinate, ok = nearestCompareLineTrace([]int64{7, 9}, []int32{100, 110}, 105)
	if !ok || trace != 7 || coordinate != 100 {
		t.Fatalf("tie did not remain deterministic: trace=%d coordinate=%d ok=%v", trace, coordinate, ok)
	}
}

func TestCompareTraceSelectionMapsInlinePanelsAndDifferencePair(t *testing.T) {
	restore := installCompareTraceSelectionFixture(t)
	defer restore()

	a, err := compareTraceSelectionForWorld(0, 112, 17)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Targets) != 1 || a.Targets[0].Role != "A" || a.Targets[0].Trace != 4 || a.Targets[0].Inline != 20 || a.Targets[0].Crossline != 110 || a.Targets[0].MarkerSample != 17 {
		t.Fatalf("unexpected A selection: %+v", a)
	}
	b, err := compareTraceSelectionForWorld(1, 112, 18)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Targets) != 1 || b.Targets[0].Role != "B" || b.Targets[0].Trace != 10 || b.Targets[0].Inline != 20 || b.Targets[0].Crossline != 110 {
		t.Fatalf("unexpected B selection: %+v", b)
	}
	difference, err := compareTraceSelectionForWorld(2, 118, 19)
	if err != nil {
		t.Fatal(err)
	}
	if !difference.Difference || len(difference.Targets) != 2 || difference.Targets[0].Trace != 5 || difference.Targets[1].Trace != 11 {
		t.Fatalf("unexpected paired difference selection: %+v", difference)
	}
	for _, target := range difference.Targets {
		if target.Inline != 20 || target.Crossline != 120 || target.SampleStart != 5 || target.SampleEnd != 30 || !target.HasGeometry {
			t.Fatalf("difference target did not preserve the selected geometry/window: %+v", target)
		}
	}
}

func TestCompareTraceSelectionMapsCrosslineAndTimeSlice(t *testing.T) {
	restore := installCompareTraceSelectionFixture(t)
	defer restore()

	compareMode, compareLineCoord = 1, 110
	compareLineXMin, compareLineXMax = 10, 20
	crossline, err := compareTraceSelectionForWorld(0, 18, 16)
	if err != nil {
		t.Fatal(err)
	}
	if len(crossline.Targets) != 1 || crossline.Targets[0].Trace != 4 || crossline.Targets[0].Inline != 20 || crossline.Targets[0].Crossline != 110 {
		t.Fatalf("unexpected Crossline selection: %+v", crossline)
	}

	compareMode = 2
	timeSlice, err := compareTraceSelectionForWorld(2, 109.7, 19.7)
	if err != nil {
		t.Fatal(err)
	}
	if !timeSlice.Difference || len(timeSlice.Targets) != 2 || timeSlice.Targets[0].Trace != 4 || timeSlice.Targets[1].Trace != 10 {
		t.Fatalf("unexpected Time Slice pair: %+v", timeSlice)
	}
	if timeSlice.Targets[0].MarkerSample != 12 || timeSlice.Targets[1].MarkerSample != 14 {
		t.Fatalf("Time Slice markers did not use each source sample: %+v", timeSlice.Targets)
	}
	betweenBins, err := compareTraceSelectionForWorld(0, 115, 20)
	if err != nil || betweenBins.Targets[0].Trace != 4 || betweenBins.Targets[0].Crossline != 110 {
		t.Fatalf("Time Slice did not follow nearest-bin raster selection: selection=%+v err=%v", betweenBins, err)
	}
	if _, err := compareTraceSelectionForWorld(0, 125, 20); err == nil {
		t.Fatal("coordinate outside the Time Slice geometry unexpectedly selected a trace")
	}
}
