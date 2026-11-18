//go:build windows

package main

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

func TestPoststackWaveDefaultIsBlackRedWithoutChangingRasterMode(t *testing.T) {
	style := poststackWaveDefaultStyle()
	if style.active() || !style.BlackRed || !style.PositiveFill || !style.NegativeFill || style.PositiveColor == style.NegativeColor {
		t.Fatalf("unexpected waveform default: %+v", style)
	}
	blackWhite := poststackWavePresetStyle(style, false)
	if blackWhite.BlackRed || blackWhite.NegativeFill || blackWhite.PositiveColor != blackWhite.NegativeColor {
		t.Fatalf("black-white alternative lost its original semantics: %+v", blackWhite)
	}
}

func TestPoststackWaveUsesSampleDomainFilterRows(t *testing.T) {
	a := viewFilterRows{traces: []int64{7, 9}, coords: []int32{100, 104}, ns: 6, dt: 2000, originUS: 800000, first: 2, last: 5,
		values: map[int64][]float64{7: {1, -3, math.NaN(), 3}, 9: {2, -6, math.NaN(), 6}}}
	b := viewFilterRows{traces: []int64{16}, coords: []int32{104}, ns: 6, dt: 2000, originUS: 800000, first: 2, last: 5,
		values: map[int64][]float64{16: {2, -6, math.NaN(), 6}}}
	d := viewFilterRows{traces: []int64{9}, coords: []int32{104}, ns: 6, dt: 2000, originUS: 800000, first: 2, last: 5,
		values: map[int64][]float64{9: {0, 0, math.NaN(), 0}}}
	snap := viewFilterSnapshot{rows: [3]viewFilterRows{a, b, d}, baseTone: viewFilterTone{lo: -7, hi: 7, valid: true}}
	style := viewFilterStyle{size: [3][2]int{{128, 256}, {128, 256}, {128, 256}}, wave: poststackWaveStyle{Mode: poststackPlotWiggle}}
	panels, err := renderViewFilterSnapshot(context.Background(), &snap, style)
	if err != nil {
		t.Fatal(err)
	}
	for i := range panels {
		frame := panels[i].wave
		if frame == nil || frame.first != 2 || frame.last != 5 || frame.intervalUS != 2000 || len(frame.rows) != []int{2, 1, 1}[i] || panels[i].filterT0 != 804 {
			t.Fatalf("panel %d: frame=%+v, origin=%v", i, frame, panels[i].filterT0)
		}
		for _, row := range frame.rows {
			if !math.IsNaN(row.values[2]) || i > 0 && row.coord != 104 {
				t.Fatalf("missing/absent trace repainted in panel %d: %+v", i, row)
			}
		}
	}
	for j, v := range panels[2].wave.rows[0].values {
		if j != 2 && v != 0 {
			t.Fatal("zero residual changed", v)
		}
	}
	if panels[0].wave.rows[0].values[1]*2 != panels[0].wave.rows[1].values[1] {
		t.Fatal("waveform sample ratios changed")
	}
}

func TestPoststackWaveSparseBRetainsABaselinesAndMissingCells(t *testing.T) {
	oldA, oldB, oldTD, oldMode, oldWave := compareA, compareB, compareTD, compareMode, poststackWaveDisplay
	oldPath, oldVisible := compareBPath, compareBVisible
	t.Cleanup(func() {
		compareA, compareB, compareTD, compareMode, poststackWaveDisplay = oldA, oldB, oldTD, oldMode, oldWave
		compareBPath, compareBVisible = oldPath, oldVisible
	})
	compareA = comparePanel{coords: []int32{10, 11, 12, 13, 14}, bgra: []byte{1}}
	compareB = comparePanel{coords: []int32{10, 11, 14}, bgra: []byte{1}}
	compareBPath, compareBVisible, compareTD, compareMode = "B.sgy", true, &compareTimeData{}, 0
	poststackWaveDisplay = poststackWaveStyle{Mode: poststackPlotWiggle}
	plot := RECT{Left: 20, Right: 421, Bottom: 200}
	if got, ok := poststackWaveTraceCenter(1, compareB, plot, plot, 11, false); !ok || got != 120 {
		t.Fatalf("sparse B baseline compressed vs A: %g %v", got, ok)
	}
	if got, ok := poststackWaveTraceCenter(1, compareB, plot, plot, 11.005, false); !ok || math.Abs(got-120.5) > 1e-9 {
		t.Fatalf("sparse B lost its fractional screen position: %g %v", got, ok)
	}
	if poststackWaveCoordinatePresent(1, 12) || !poststackWaveCoordinatePresent(1, 11) {
		t.Fatal("sparse B geometry accepted a missing cell or lost an actual column")
	}
}

func TestPoststackWaveSegmentsCrossAtZeroAndBreakOnMissing(t *testing.T) {
	type segment struct {
		a, b     poststackWaveVertex
		negative bool
	}
	var seen []segment
	poststackWaveSegments([]float64{1, -3, math.NaN(), -2, 2, math.Inf(1), 1}, 8, func(a, b poststackWaveVertex, negative bool) {
		seen = append(seen, segment{a, b, negative})
	})
	want := []segment{
		{poststackWaveVertex{0, 1}, poststackWaveVertex{.25, 0}, false},
		{poststackWaveVertex{.25, 0}, poststackWaveVertex{1, -3}, true},
		{poststackWaveVertex{3, -2}, poststackWaveVertex{3.5, 0}, true},
		{poststackWaveVertex{3.5, 0}, poststackWaveVertex{4, 2}, false},
	}
	if len(seen) != len(want) {
		t.Fatalf("segments = %#v", seen)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("segment %d = %#v, want %#v", i, seen[i], want[i])
		}
	}
}

func TestPoststackWaveRenderingModesAndPolarityExport(t *testing.T) {
	savedWave, savedMode, savedPath := poststackWaveDisplay, compareMode, compareAPath
	savedOwner, savedWorkspace := compareHwnd, compareWorkspaceActive
	t.Cleanup(func() {
		poststackWaveDisplay, compareMode, compareAPath = savedWave, savedMode, savedPath
		compareHwnd, compareWorkspaceActive = savedOwner, savedWorkspace
	})
	compareHwnd, compareWorkspaceActive, compareMode = 0, false, 0
	coords := []int32{100, 110, 120}
	frame := &poststackWaveFrame{traceCount: 3, first: 400, last: 404, intervalUS: 2000,
		rows: []poststackWaveRow{{110, []float64{0, 1, -1, 0, 1}}}}
	p := comparePanel{coords: coords, wave: frame, stats: segy.RenderStats{MapMin: -1, MapMax: 1}}
	rect := RECT{Left: 20, Top: 20, Right: 200, Bottom: 200}
	type pixels struct{ black, red int }
	count := func(mode poststackPlotMode, blackRed bool) pixels {
		t.Helper()
		poststackWaveDisplay = poststackWaveStyle{Mode: mode, BlackRed: blackRed, Amplitude: 1}
		path := filepath.Join(t.TempDir(), "wave.bmp")
		if err := saveSeismicDisplayBMP(path, 220, 220, func(dc uintptr) {
			paintPoststackWaveforms(dc, rect, rect, p, 0, false, false)
		}); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		offset := int(binary.LittleEndian.Uint32(b[10:14]))
		bpp := int(binary.LittleEndian.Uint16(b[28:30])) / 8
		if bpp != 3 && bpp != 4 {
			t.Fatalf("unexpected BMP bpp %d", bpp)
		}
		var marked pixels
		for y := 0; y < 220; y++ {
			base := offset + y*((220*bpp+3)&^3)
			for x := 0; x < 220; x++ {
				i := base + x*bpp
				if b[i+2] < 185 && b[i+2] == b[i+1] && b[i+1] == b[i] {
					marked.black++
				}
				if int(b[i+2])-int(b[i]) > 45 {
					marked.red++
				}
			}
		}
		return marked
	}
	waveBW, wave := count(poststackPlotWiggle, false), count(poststackPlotWiggle, true)
	vaBW, va, both := count(poststackPlotVA, false), count(poststackPlotVA, true), count(poststackPlotWiggleVA, true)
	if waveBW.black == 0 || waveBW.red != 0 || wave.black == 0 || wave.red == 0 || vaBW.red != 0 || va.black <= wave.black || va.red <= wave.red || both.black < va.black || both.red < va.red {
		t.Fatalf("wave BW=%+v wave BR=%+v VA BW=%+v VA BR=%+v both=%+v", waveBW, wave, vaBW, va, both)
	}
	poststackWaveDisplay = poststackWaveStyle{Mode: poststackPlotColor}
	if paintPoststackWaveforms(0, rect, rect, p, 0, false, false) {
		t.Fatal("color raster mode must not use waveform painting")
	}
}

func TestPoststackWaveDensityDoesNotLimitAmplitudeStatistics(t *testing.T) {
	const n, samples = 253, 46
	traces, coords := make([]int64, n), make([]float64, n)
	for i := range traces {
		traces[i], coords[i] = int64(i), float64(100+2*i)
	}
	calls := 0
	read := func(trace int64, first, last int) ([]float64, error) {
		calls++
		values := make([]float64, last-first+1)
		for i := range values {
			if trace%4 == 0 {
				values[i] = 1
			} else {
				values[i] = 24
			}
		}
		values[0] = math.NaN()
		return values, nil
	}
	build := func(density poststackWaveDensity, stride int) *poststackWaveFrame {
		before := calls
		pick := poststackSelectWaveform(coords, 122, samples, poststackWaveStyle{Density: density, Stride: stride})
		f := poststackBuildWaveFrame(traces, coords, 0, samples-1, samples, 2000, pick, read)
		if calls-before != n || f.amplitudes.count != n*(samples-1) || f.amplitudes.reference(.98) != 24 {
			t.Fatalf("mode %v K=%d did not count all finite samples: reads=%d, count=%d ref=%g", density, stride, calls-before, f.amplitudes.count, f.amplitudes.reference(.98))
		}
		return f
	}
	all := build(poststackWaveDensityAll, 14)
	if len(all.rows) != n || !all.overlap {
		t.Fatalf("explicit all hid rows: %d/%d (overlap %t)", len(all.rows), n, all.overlap)
	}
	each := build(poststackWaveDensityStride, 4)
	if len(each.rows) != (n+3)/4 {
		t.Fatalf("every 4th selection: %d", len(each.rows))
	}
	for i, row := range each.rows {
		if row.coord != coords[4*i] || row.values[1] != 1 {
			t.Fatalf("selected rows were reordered or normalized: %v", row)
		}
	}
	if got := len(build(poststackWaveDensityStride, 1).rows); got != n {
		t.Fatalf("K=1 selected %d/%d", got, n)
	}
	if got := len(build(poststackWaveDensityAuto, 1).rows); got >= n || got > 11 {
		t.Fatalf("auto selection did not honor display density: %d", got)
	}
	if got := poststackWaveTimeStep(poststackWaveStyle{}, 2000, samples); got != 1 {
		t.Fatalf("default unexpectedly thins time axis by %d", got)
	}
	if got := poststackWaveTimeStep(poststackWaveStyle{TimeStepMS: 5}, 2000, samples); got != 3 {
		t.Fatalf("5 ms should use 3 samples / 6 ms at dt=2 ms, got %d", got)
	}
	empty := poststackWaveAmplitudes{}
	empty.add([]float64{0, 0, math.Inf(-1)})
	if empty.count != 2 || empty.reference(.98) != 1 {
		t.Fatal("all-zero valid samples do not have a safe fallback")
	}
}

func TestPoststackWaveCoverageUsesSubpixelPositionAndFillOpacity(t *testing.T) {
	style := poststackWaveDefaultStyle()
	r := newPoststackWaveRaster(12, 10, true, [2]bool{true, false})
	r.stroke(r.line[0], poststackWavePoint{3.75, .5}, poststackWavePoint{3.75, 9.5}, 1)
	r.area(0, 8, poststackWavePoint{10, 1}, poststackWavePoint{10, 9})
	r.finish(poststackWaveInks(style), .5)
	row := 5 * r.w * 4
	get := func(x int) byte { return r.bgra[row+x*4] }
	if left, right := get(3), get(4); left >= right || right >= 255 || left <= get(8)-100 {
		t.Fatalf("fractional x=3.75 didn't give two antialiased pixels: %d/%d", left, right)
	}
	if fill := get(8); fill < 125 || fill > 145 || get(11) != 255 {
		t.Fatalf("50%% fill or outside pixel wrong: %d/%d", fill, get(11))
	}
	if a, b := poststackWaveBaseSwing(240, 250, 96), poststackWaveBaseSwing(240, 250, 144); a != 6 || b != 9 {
		t.Fatalf("DIP readability floor: %g/%g", a, b)
	}
}

func TestPoststackWaveReferenceComparisonLinkAndResidual(t *testing.T) {
	oldA, oldB, oldD := compareA, compareB, compareD
	oldOwner, oldPath, oldVisible, oldActive, oldAGC := compareHwnd, compareBPath, compareBVisible, compareWorkspaceActive, agc
	const own = 0xa52
	oldBinding := displayOwners[own]
	t.Cleanup(func() {
		compareA, compareB, compareD = oldA, oldB, oldD
		compareHwnd, compareBPath, compareBVisible, compareWorkspaceActive, agc = oldOwner, oldPath, oldVisible, oldActive, oldAGC
		if oldBinding != nil {
			displayOwners[own] = oldBinding
		} else {
			delete(displayOwners, own)
		}
	})
	panel := func(v float64) comparePanel {
		f := &poststackWaveFrame{}
		f.amplitudes.add([]float64{v, -v})
		return comparePanel{bgra: []byte{1}, wave: f}
	}
	compareA, compareB, compareD = panel(100), panel(20), panel(80)
	compareHwnd, compareBPath, compareBVisible, compareWorkspaceActive, agc = own, "b.sgy", true, true, true
	if got := poststackWaveReference(1, compareB, poststackWaveStyle{}); got != 100 {
		t.Fatalf("linked B normalized its weaker data or used raster AGC: %g", got)
	}
	if got := poststackWaveReference(2, compareD, poststackWaveStyle{}); got != 100 {
		t.Fatalf("following residual did not use A scale: %g", got)
	}
	b := *seismicCompareCompareGroup()
	b.Linked, b.FollowA = false, false
	displayOwners[own] = &displayOwnerState{links: map[[3]string]displayCompareBinding{b.Keys: b}}
	if got := poststackWaveReference(1, compareB, poststackWaveStyle{}); got != 20 {
		t.Fatalf("independent B did not use its scale: %g", got)
	}
	if got := poststackWaveReference(2, compareD, poststackWaveStyle{}); got != 80 {
		t.Fatalf("independent residual did not use its scale: %g", got)
	}
	if got := poststackWaveReference(2, compareD, poststackWaveStyle{Amplitude: 42.5}); got != 42.5 {
		t.Fatalf("manual reference was altered: %g", got)
	}
}
