//go:build windows

package main

import (
	"context"
	"math"
	"reflect"
	"testing"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
)

func TestChartDisplaySegmentClipsRatherThanSaturatingOutsideData(t *testing.T) {
	plot := RECT{Left: 20, Top: 10, Right: 120, Bottom: 110}
	x := makeChartAxis("x", "X", "Hz", 1, 3, false)
	y := makeChartAxis("y", "Y", "dB", 1, 3, false)
	x0, y0, x1, y1, ok := chartClipSegment(x, y, 0, 0, 4, 4, plot)
	if !ok || x0 != 20 || y0 != 110 || x1 != 120 || y1 != 10 {
		t.Fatalf("crossing segment clip=(%d,%d)-(%d,%d), %v", x0, y0, x1, y1, ok)
	}
	if _, _, _, _, ok := chartClipSegment(x, y, 1, 0, 3, 0, plot); ok {
		t.Fatal("fully clipped segment produced a false line on the plot boundary")
	}
	if _, _, _, _, ok := chartClipSegment(x, y, math.NaN(), 1, 3, 3, plot); ok {
		t.Fatal("non-finite segment was drawn")
	}
	x.Reverse, y.Down = true, true
	x0, y0, x1, y1, ok = chartClipSegment(x, y, 0, 0, 4, 4, plot)
	if !ok || x0 != 120 || y0 != 10 || x1 != 20 || y1 != 110 {
		t.Fatalf("reversed downward projection=(%d,%d)-(%d,%d), %v", x0, y0, x1, y1, ok)
	}
}

func TestChartDisplayWaveformPreservesPhysicalTimeAcrossUnitsAndDownwardDirection(t *testing.T) {
	curve := traceAnalysisSeries{dtUS: 2000, sampleStart: 5, timeOriginUS: 390000, samples: []float64{0, 1, 0}}
	a := makeChartAxis("time", "时间", "s", .400, .404, true)
	segments := buildTraceWaveformSegments(curve, 400000, 404000, 41)
	if len(segments) != 5 || curve.timeUS(5) != 400000 {
		t.Fatalf("time origin ignored: first=%f samples=%+v", curve.timeUS(5), segments)
	}
	plot := RECT{Top: 15, Bottom: 55}
	a.Unit = "ms"
	if got := a.DisplayValue(.400); got != 400 || chartAxisY(a, a.BaseValue(got), plot) != 15 {
		t.Fatalf("time unit changed selected sample: label %g", got)
	}
	a.Reverse = true
	if got := chartAxisY(a, .400, plot); got != 55 {
		t.Fatalf("reversed time mark displaced: %d", got)
	}
}

func TestChartDisplayQCViewportClipsImmutableBin(t *testing.T) {
	plot := RECT{Left: 0, Right: 100, Top: 0, Bottom: 100}
	x := makeChartAxis("value", "Fold", "道", 0, 20, false)
	y := makeChartAxis("count", "数量", "", 0, 100, false)
	x.View = displaycore.Range{Min: 10, Max: 15}
	y.View = displaycore.Range{Min: 20, Max: 60}
	bin := prestackcore.DistributionBin{Min: 8, Max: 12, Count: 40, CurrentCount: 15}
	want := bin
	b, ok := qcDisplayBar(plot, x, y, bin.Min, bin.Max, bin.Count, false)
	if !ok || b.Left != 0 || b.Right != 39 || b.Top != 50 || b.Bottom != 100 {
		t.Fatalf("clipped bar=%+v, %v", b, ok)
	}
	if _, ok := qcDisplayBar(plot, x, y, bin.Min, bin.Max, bin.CurrentCount, true); ok {
		t.Fatal("invisible selected bar must not appear")
	}
	if !reflect.DeepEqual(bin, want) {
		t.Fatal("display modifications re-binned QC data")
	}
}

func TestChartDisplayGeometryTransformAgreesWithAxesForPicking(t *testing.T) {
	x := makeChartAxis("x", "X", "", 4000, 5000, false)
	y := makeChartAxis("y", "Y", "", 2000, 2500, false)
	x.Reverse, y.Reverse = true, true
	p := prestackGeometryPlot(RECT{Left: 22, Top: 58, Right: 740, Bottom: 440}, x.View, y.View)
	transform := prestackGeometryDisplayTransform(p, x, y)
	for _, xy := range [][2]float64{{4000, 2000}, {4380, 2201}, {5000, 2500}} {
		px, py := transform.Project(xy[0], xy[1])
		wantX, wantY := x.Pixel(xy[0], int(p.Left), int(p.Right)), chartAxisY(y, xy[1], p)
		if int(math.Round(px)) != wantX || int(math.Round(py)) != wantY {
			t.Fatalf("display/picker mismatch: (%g,%g) vs (%d,%d)", px, py, wantX, wantY)
		}
	}
}

func TestChartDisplayGeometryFoldStatisticsUsesVisibleValidBins(t *testing.T) {
	idx := &prestackcore.PrestackIndex{Bins: []prestackcore.Bin{
		{X: 1, Y: 1, Fold: 3, HasCoordinates: true}, {X: 2, Y: 2, Fold: 9, HasCoordinates: true},
		{X: 5, Y: 1, Fold: 200, HasCoordinates: true}, {X: 1, Y: 1, Fold: 100},
	}}
	read := prestackGeometryFoldStats(idx, displaycore.Range{Min: 0, Max: 2}, displaycore.Range{Min: 0, Max: 2})
	got, err := read(context.Background())
	if err != nil || got.Raw.Count != 2 || got.Raw.Range != (displaycore.Range{Min: 3, Max: 9}) {
		t.Fatalf("visible Fold statistics=%+v: %v", got.Raw, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := read(ctx); err != context.Canceled {
		t.Fatalf("obsolete fold range not canceled: %v", err)
	}
}

func TestChartDisplayGeometryCommitUpdatesNativeBoundsWithoutResettingOtherAxis(t *testing.T) {
	old, oldOwner := prestackState, prestackHwnd
	t.Cleanup(func() { prestackState, prestackHwnd = old, oldOwner })
	prestackHwnd = 0
	prestackState = prestackSession{page: 1, index: &prestackcore.PrestackIndex{},
		mapBounds: prestackcore.XYBounds{Valid: true, XMin: 0, XMax: 100, YMin: 50, YMax: 150}}
	x := makeChartAxis("x", "X", "", 10, 30, false)
	y := makeChartAxis("y", "Y", "", 20, 40, false)
	x.ViewEdited = true
	if err := commitPrestackGeometryAxes(displaycore.Settings{Axes: []displaycore.Axis{x, y}}); err != nil {
		t.Fatal(err)
	}
	got := prestackState.mapBounds
	if got.XMin != 10 || got.XMax != 30 || got.YMin != 50 || got.YMax != 150 {
		t.Fatalf("editing X reset the unrelated live Y viewport: %+v", got)
	}
	x.View = displaycore.Range{Min: -math.MaxFloat64, Max: math.MaxFloat64}
	if err := commitPrestackGeometryAxes(displaycore.Settings{Axes: []displaycore.Axis{x}}); err == nil || prestackState.mapBounds != got {
		t.Fatalf("invalid edit was not atomic: bounds=%+v error=%v", prestackState.mapBounds, err)
	}
}
