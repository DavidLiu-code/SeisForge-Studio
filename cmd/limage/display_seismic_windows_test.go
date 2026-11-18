//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"

	display "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

func TestDisplaySeismicMappingKeepsDataAndInvalidSeparate(t *testing.T) {
	samples := []float64{-2, -1, 0, 1, 3, math.NaN()}
	before := append([]float64(nil), samples...)
	var stats display.Stats
	seismicStatsAddTrace(&stats, samples, false)
	if stats.Raw.Count != 5 || stats.Raw.Range != (display.Range{Min: -2, Max: 3}) {
		t.Fatalf("exact raw stats = %+v", stats.Raw)
	}
	st := segy.RenderStats{MapMin: -1, MapMax: 1}
	palette := currentPalette()
	got := seismicColors(0, "", samples, seismicByteIndices(samples, st), palette)
	if !bytes.Equal(got[:4], got[4:8]) || !bytes.Equal(got[12:16], got[16:20]) || !bytes.Equal(got[20:23], []byte{255, 255, 255}) {
		t.Fatalf("clipping / invalid pixel = %v", got)
	}
	for i, want := range before {
		if math.Float64bits(want) != math.Float64bits(samples[i]) {
			t.Fatal("display mapping mutated raw samples")
		}
	}
	var normalized display.Stats
	seismicStatsAddTrace(&normalized, samples, true)
	if normalized.Raw.Range != stats.Raw.Range || normalized.Mapping().Range == stats.Raw.Range || normalized.Mapping().Count != 5 {
		t.Fatal("AGC display domain not tracked separately")
	}
}

func TestSeismicSingleTraceUsesOnlyMatchingDisplayCell(t *testing.T) {
	coords := []int32{10, 20, 90}
	fraction := func(v float64) (float64, bool) { return seismicAcrossFraction(coords, v) }
	if lo, hi, ok := seismicSingleTraceCell(coords, 20, fraction); !ok || lo != .25 || hi != .75 {
		t.Fatalf("single common trace broadcast or lost: %g %g %v", lo, hi, ok)
	}
	if lo, hi, ok := seismicSingleTraceCell(coords, 15, fraction); !ok || lo != .25 || hi != .25 {
		t.Fatalf("unpaired B trace occupied a false A cell: %g %g %v", lo, hi, ok)
	}
	axis := seismicAxis("crossline", "Crossline", "", display.Range{Min: 10, Max: 90}, display.Range{Min: 20, Max: 20}, false)
	axis.AllowsPoint, axis.ViewEdited = true, true
	if err := seismicValidateView(display.Settings{Axes: []display.Axis{axis}}, axis, display.Axis{ID: "time"}); err != nil {
		t.Fatalf("exact one-trace selection rejected: %v", err)
	}
	axis.View = display.Range{Min: 50, Max: 40}
	if err := seismicValidateView(display.Settings{Axes: []display.Axis{axis}}, axis, display.Axis{ID: "time"}); err == nil {
		t.Fatal("inverted range was mistaken for a single trace")
	}
}

func TestDisplayPoststackTickStyleCopyDoesNotTouchGeometryVisibilityOrColors(t *testing.T) {
	a := display.Settings{
		Axes:            []display.Axis{{ID: "crossline", View: display.Range{Min: 20, Max: 25}, Divisions: 4, TickFontPT: 12, Format: display.ScientificFormat}},
		Color:           &display.ColorMapping{Range: display.Range{Min: -2, Max: 4}, Divisions: 8, TickLengthSet: true, TickFontPT: 14},
		PlotTitleFontPT: 15,
	}
	b := display.Settings{
		Axes:  []display.Axis{{ID: "crossline", Title: "独立 B", View: display.Range{Min: 10, Max: 40}, Reverse: true}},
		Color: &display.ColorMapping{Range: display.Range{Min: -1, Max: 2}, Stops: displayPreset(1), Hidden: true},
	}
	color := b.Color.ColorAt(1)
	seismicCopyTickStyle(&b, a, true, 0)
	seismicCopyTickStyle(&b, a, false, 0)
	if b.PlotTitleFontPT != 15 || b.Color.Divisions != 8 || b.Color.MajorTickDIP() != 0 || b.Axes[0].Divisions != 4 || b.Axes[0].TickFontPT != 12 {
		t.Fatalf("style was not copied: %+v", b)
	}
	if b.Color.Range != (display.Range{Min: -1, Max: 2}) || b.Color.ColorAt(1) != color || !b.Color.Hidden ||
		b.Axes[0].View != (display.Range{Min: 10, Max: 40}) || b.Axes[0].Title != "独立 B" || !b.Axes[0].Reverse {
		t.Fatal("copying tick style changed range, colors, geometry, direction or visibility")
	}
	if got := seismicDefaultTimeText(1201.69779286927); got != "1201.7 ms" {
		t.Fatalf("status time = %q", got)
	}
}

func TestDisplayCompareToolbarPalettePreservesIndependentPanels(t *testing.T) {
	oldOwner, oldMode, oldActive, oldIndex := compareHwnd, compareMode, compareWorkspaceActive, comparePaletteIndex
	compareHwnd, compareMode, compareWorkspaceActive, comparePaletteIndex = 0xb250, 0, true, 2
	owner := compareHwnd
	defer func() {
		delete(displayOwners, owner)
		compareHwnd, compareMode, compareWorkspaceActive, comparePaletteIndex = oldOwner, oldMode, oldActive, oldIndex
	}()
	group := displayCompareBinding{Keys: [3]string{seismicCompareKey(0), seismicCompareKey(1), seismicCompareKey(2)}, CanLink: true}
	o := &displayOwnerState{surfaces: make(map[string]*displaySurfaceState), links: map[[3]string]displayCompareBinding{group.Keys: group}}
	displayOwners[owner] = o
	for i, key := range group.Keys {
		c := display.ColorMapping{Range: display.Range{Min: -1, Max: 1}, Stops: displayPreset(i)}
		o.surfaces[key] = &displaySurfaceState{s: displaySurface{Color: &c, Compare: &group}}
	}
	initial := func(i int) display.RGB {
		s, _ := displayCommitted(owner, group.Keys[i])
		return s.Color.ColorAt(.25)
	}
	a, b, d := initial(0), initial(1), initial(2)
	if got := seismicUseCompareToolbarPalette(0); got != "独立 · A" {
		t.Fatalf("wrong quick palette scope: %q", got)
	}
	if initial(0) == a || initial(1) != b || initial(2) != d {
		t.Fatal("quick palette either failed to apply on A or recolored independent B / delta")
	}
	group.Linked, group.FollowA = true, true
	o.links[group.Keys] = group
	if got := seismicUseCompareToolbarPalette(2); got != "一致" || initial(0) != initial(1) || initial(0) != initial(2) {
		t.Fatalf("linked quick palette was not shared: %q", got)
	}
}

func TestDisplayCompareDeltaUsesLocalLegendAndHideReclaimsPlot(t *testing.T) {
	owner, o := testDisplaySurfaces(t)
	oldOwner, oldMode, oldActive, oldVisible, oldBPath, oldB, oldAGC, oldDirty := compareHwnd, compareMode, compareWorkspaceActive, compareBVisible, compareBPath, compareB, agc, compareBaseDirty
	defer func() {
		compareHwnd, compareMode, compareWorkspaceActive, compareBVisible, compareBPath, compareB, agc, compareBaseDirty = oldOwner, oldMode, oldActive, oldVisible, oldBPath, oldB, oldAGC, oldDirty
	}()
	compareHwnd, compareMode, compareWorkspaceActive, compareBVisible, compareBPath, agc = owner, 0, true, true, "b.sgy", false
	compareB.indices = []byte{0}
	group := seismicCompareCompareGroup()
	if group == nil {
		t.Fatal("test compare group was not bound")
	}
	o.links[group.Keys] = *group
	for i, key := range group.Keys {
		m := display.ColorMapping{Range: display.Range{Min: -5 - float64(i), Max: 1 + float64(i)}, Title: "振幅", TickLength: float64(6 + i), TickLengthSet: true, Stops: displayPreset(i)}
		if i == 2 {
			m.Title, m.Format, m.Digits = "差剖面振幅", display.ScientificFormat, 2
		}
		v := display.Settings{Color: &m}
		o.surfaces[key] = &displaySurfaceState{s: displaySurface{Identity: seismicCompareIdentity(i), Color: &m, Compare: group}, saved: &v, order: 1}
	}
	a := seismicPanelColor(0, compareA)
	deltaKey := group.Keys[2]
	if !displaySetColorbarVisible(owner, deltaKey, seismicCompareIdentity(2), true) {
		t.Fatal("show Δ legend failed")
	}
	d := seismicPanelColor(2, compareD)
	if d.Hidden || !a.Hidden || d.Range != a.Range || !slices.Equal(d.Stops, a.Stops) || d.MajorTickDIP() != 8 || d.Format != display.ScientificFormat || d.Title != "差剖面振幅" {
		t.Fatalf("linked color did not keep local delta legend style: %v (%v)", d.Title, d.Range)
	}
	x := seismicAxis("x", "道号", "", display.Range{Min: 1, Max: 50}, display.Range{Min: 1, Max: 50}, false)
	y := seismicAxis("y", "时间", "ms", display.Range{Min: 0, Max: 500}, display.Range{Min: 0, Max: 500}, true)
	outer := RECT{Top: 118, Right: 1360, Bottom: 900}
	m := displayLayoutTestMetrics(120)
	with := displaySceneLayouts(m, outer, []displayPlotInput{{X: x, Y: y, Color: &a}, {X: x, Y: y}, {X: x, Y: y, Color: &d}})
	displaySetColorbarVisible(owner, deltaKey, seismicCompareIdentity(2), false)
	d = seismicPanelColor(2, compareD)
	without := displaySceneLayouts(m, outer, []displayPlotInput{{X: x, Y: y, Color: &a}, {X: x, Y: y}, {X: x, Y: y, Color: &d}})
	if !d.Hidden || without[2].ColorRegion != (RECT{}) || without[2].Plot.Right-without[2].Plot.Left <= with[2].Plot.Right-with[2].Plot.Left {
		t.Fatal("hidden delta legend still reserves space in the measured Compare layout")
	}
}

func TestPoststackBColorLinkRecolorsRasterAndHidesOnlyItsLegend(t *testing.T) {
	owner, o := testDisplaySurfaces(t)
	oldOwner, oldMode, oldActive, oldVisible, oldBPath, oldB, oldAGC, oldDirty := compareHwnd, compareMode, compareWorkspaceActive, compareBVisible, compareBPath, compareB, agc, compareBaseDirty
	oldScale := compareFixedColorScale
	t.Cleanup(func() {
		compareHwnd, compareMode, compareWorkspaceActive, compareBVisible, compareBPath, compareB, agc, compareBaseDirty = oldOwner, oldMode, oldActive, oldVisible, oldBPath, oldB, oldAGC, oldDirty
		compareFixedColorScale = oldScale
	})
	compareHwnd, compareMode, compareWorkspaceActive, compareBVisible, compareBPath, agc = owner, 0, true, true, "b.sgy", false
	// A ready bitmap still registers a profile if its render path does not
	// retain the legacy indexed bytes.
	compareB = comparePanel{bgra: []byte{0, 0, 0, 0}}
	group := seismicCompareCompareGroup()
	if group == nil || !group.CanLink || !group.PoststackProfiles {
		t.Fatal("a ready B with no indexed bytes did not get a Compare profile")
	}
	group.Linked, group.FollowA = false, false
	o.links[group.Keys] = *group
	for i, key := range group.Keys {
		m := display.ColorMapping{Range: display.Range{Min: -2, Max: 2}, Stops: displayPreset(i)}
		v := display.Settings{Color: &m}
		o.surfaces[key] = &displaySurfaceState{s: displaySurface{Identity: seismicCompareIdentity(i), ColorDomain: seismicMappingDomain(false), Color: &m, Compare: group}, saved: &v, order: 1}
	}
	if !displaySetColorbarVisible(owner, group.Keys[1], seismicCompareIdentity(1), true) {
		t.Fatal("cannot show the B legend")
	}
	raster := func(panel int) []byte {
		p := comparePanel{indices: []byte{120}, displayValues: []float64{0.65}}
		seismicCompareColorsForPanel(panel, &p)
		return p.bgra
	}
	a, b, d := raster(0), raster(1), raster(2)
	if slices.Equal(a, b) {
		t.Fatal("independent B is not using its own color mapping")
	}
	group.Linked = true
	o.links[group.Keys] = *group
	if !slices.Equal(a, raster(1)) || !slices.Equal(d, raster(2)) {
		t.Fatal("following A did not update B raster or changed independent Delta")
	}
	if !displayColorbarVisible(owner, group.Keys[1]) || displayColorbarVisible(owner, group.Keys[0]) || displayColorbarVisible(owner, group.Keys[2]) {
		t.Fatal("B color linkage leaked its legend visibility to another panel")
	}
	group.Linked = false
	o.links[group.Keys] = *group
	if !slices.Equal(b, raster(1)) {
		t.Fatal("unchecking B color linkage did not restore its own mapping")
	}
}

func TestDisplaySeismicCoordsAndUnits(t *testing.T) {
	x := []int32{17, 20, 120, 121}
	for _, v := range []float64{17, 18, 20, 70, 120, 121} {
		f, ok := seismicAcrossFraction(x, v)
		if !ok || math.Abs(seismicAcrossAt(x, f)-v) > 1e-9 {
			t.Fatalf("coordinate %g round-trip failed", v)
		}
	}
	if got := seismicAcrossAt(x, 2.0/3); got != 120 {
		t.Fatalf("rank-based trace mapping is %g", got)
	}
	if _, ok := seismicAcrossFraction(x, 130); ok {
		t.Fatal("non-overlap must not appear as valid data")
	}
	a := display.Axis{BaseUnit: "ms", Unit: "s"}
	if got := seismicAxisTimeText(400, a); got != "0.4 s" {
		t.Fatalf("time label = %s", got)
	}
}

func TestDisplayTimeSliceStatsPairValidBinsOnly(t *testing.T) {
	a := &segy.GeometryIndex{InlineValues: []int32{1, 2}, CrosslineValues: []int32{10, 20}, RowOfTrace: []int32{0, 0, 1, 1}, ColOfTrace: []int32{0, 1, 0, 1}, TraceNumbers: []int64{0, 1, 2, 3}}
	b := &segy.GeometryIndex{InlineValues: []int32{1, 2}, CrosslineValues: []int32{10, 20}, RowOfTrace: []int32{0, 1, 1}, ColOfTrace: []int32{0, 0, 1}, TraceNumbers: []int64{0, 1, 2}}
	d := &compareTimeData{ga: a, gb: b, bounds: a.Bounds(), currentValsA: []float32{0, 99, 3, float32(math.NaN())}, currentValsB: []float32{0, 5, 4}}
	diff := seismicTimeSliceDifference(a, b, d.currentValsA, d.currentValsB, d.bounds)
	if diff[0] != 0 || !math.IsNaN(float64(diff[1])) || diff[2] != -2 || !math.IsNaN(float64(diff[3])) {
		t.Fatalf("paired raw bins = %v", diff)
	}
	_, mask, e := a.RasterizeTimeSliceDisplay(diff, 2, 2, d.bounds)
	if e != nil || len(mask) != 4 || !mask[0] || mask[1] || !mask[2] || mask[3] {
		t.Fatalf("paired display mask = %v, %v", mask, e)
	}
	s, err := seismicTimeSliceStatsReader(2, d)(context.Background())
	if err != nil || s.Raw.Count != 2 || s.Raw.Range != (display.Range{Min: -2, Max: 0}) {
		t.Fatalf("Δ stats: %+v, %v", s.Raw, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = seismicTimeSliceStatsReader(2, d)(ctx); err == nil {
		t.Fatal("cancelled statistics did not stop")
	}
}

func TestDisplaySeismicPartialBLeavesBlankAndMapsSamples(t *testing.T) {
	oldA, oldB, oldD := compareA, compareB, compareD
	oldTD, oldMode, oldAPath, oldBPath, oldShow, oldActive, oldBVisible := compareTD, compareMode, compareAPath, compareBPath, compareShowDiff, compareWorkspaceActive, compareBVisible
	oldFilter := viewFilter
	defer func() {
		compareA, compareB, compareD = oldA, oldB, oldD
		compareTD, compareMode, compareAPath, compareBPath, compareShowDiff, compareWorkspaceActive, compareBVisible = oldTD, oldMode, oldAPath, oldBPath, oldShow, oldActive, oldBVisible
		viewFilter = oldFilter
	}()
	viewFilter.frame = nil
	compareTD, compareMode, compareAPath, compareBPath, compareShowDiff, compareWorkspaceActive, compareBVisible = &compareTimeData{}, 0, "a.sgy", "b.sgy", false, true, true
	bright := []byte{0, 0, 255, 0, 0, 0, 255, 0, 0, 0, 255, 0, 0, 0, 255, 0}
	compareA = comparePanel{w: 2, h: 2, bgra: bright, coords: []int32{10, 20, 30}, sampleDtUS: 1000, stats: segy.RenderStats{SampleStart: 0, SampleEnd: 100}}
	compareB = comparePanel{w: 2, h: 2, bgra: bright, coords: []int32{20, 30}, sampleOriginUS: 25000, sampleDtUS: 1000, stats: segy.RenderStats{SampleStart: 0, SampleEnd: 49}}
	rect := RECT{Left: 20, Top: 20, Right: 120, Bottom: 120}
	clip := seismicCompareRasterRect(1, rect)
	if clip.Left != 70 || clip.Right != 120 || clip.Top != 45 || clip.Bottom != 94 {
		t.Fatalf("partial B raster = %+v", clip)
	}
	if got := seismicPanelCanonicalSample(1, 17); got != 42 {
		t.Fatalf("B-local to A = %g", got)
	}
	if got := seismicPanelLocalSample(1, 42); got != 17 {
		t.Fatalf("A to B-local = %g", got)
	}
	path := filepath.Join(t.TempDir(), "partial.bmp")
	if err := saveSeismicDisplayBMP(path, 140, 140, func(dc uintptr) { seismicPlot(dc, clip, bright, 2, 2, false, false) }); err != nil {
		t.Fatal(err)
	}
	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	w := int(binary.LittleEndian.Uint32(buf[18:]))
	h := int(binary.LittleEndian.Uint32(buf[22:]))
	stride := (w*3 + 3) &^ 3
	color := func(x, y int) []byte { off := 54 + (h-1-y)*stride + x*3; return buf[off : off+3] }
	if !bytes.Equal(color(75, 50), []byte{0, 0, 255}) || !bytes.Equal(color(23, 50), []byte{255, 255, 255}) || !bytes.Equal(color(75, 24), []byte{255, 255, 255}) {
		t.Fatal("frame/export drew over missing B region")
	}
}
