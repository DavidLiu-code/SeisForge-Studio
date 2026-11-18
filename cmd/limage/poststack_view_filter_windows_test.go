//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/poststack"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/testsegy"
)

func makeViewFilterFixture(t *testing.T, path string, gain float64) {
	t.Helper()
	const ns = 256
	if err := testsegy.Write(path, testsegy.Options{Rows: 2, Cols: 8, Samples: ns, RegularGrid: true}); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	buf := make([]byte, ns*4)
	for trace := 0; trace < 16; trace++ {
		for i := 0; i < ns; i++ {
			value := float32(gain * (math.Sin(2*math.Pi*8*float64(i)/ns+float64(trace)*0.21) + 0.8*math.Sin(2*math.Pi*45*float64(i)/ns)))
			binary.BigEndian.PutUint32(buf[i*4:], math.Float32bits(value))
		}
		if _, err = f.WriteAt(buf, int64(3600+trace*(240+ns*4)+240)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestViewFilterSnapshotUsesIndependentContextAndNumericDifference(t *testing.T) {
	dir := t.TempDir()
	aPath := filepath.Join(dir, "a.sgy")
	bPath := filepath.Join(dir, "b.sgy")
	makeViewFilterFixture(t, aPath, 1)
	makeViewFilterFixture(t, bPath, 0.5)
	before, _ := os.ReadFile(aPath)
	beforeB, _ := os.ReadFile(bPath)
	a, err := segy.Open(aPath)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	cropPath := filepath.Join(dir, "crop.sgy")
	if err := a.ExportSection(cropPath, segy.SectionExportOptions{TraceIndices: []int64{3, 1}, SampleStart: 40, SampleEnd: 180}); err != nil {
		t.Fatal(err)
	}
	parameters := poststack.ViewBandpass{LowHz: 8, HighHz: 40}
	r := viewFilterRequest{key: viewFilterKey{a: aPath, b: cropPath, first: 30, last: 74},
		settings: withViewFilterTarget([2]SideFilterState{}, viewFilterBoth, true, parameters),
		rows: [2]viewFilterRows{{traces: []int64{1, 3}, coords: []int32{100, 102}, path: aPath, ns: 256, dt: 2000},
			{traces: []int64{1, 0}, coords: []int32{100, 102}, path: cropPath, ns: 141, dt: 2000, offset: 40}},
		pairedA: []int64{1, 3}, pairedB: []int64{1, 0}, pairCoords: []int32{100, 102}}
	s, err := computeViewFilterSnapshot(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if s.first != 30 || s.last != 74 || s.rows[1].first != 0 || s.rows[1].last != 34 || s.rows[2].first != 40 || s.rows[2].last != 74 || !s.unequalContext {
		t.Fatalf("cropped axes: %+v", s.rows)
	}
	// The unfiltered and filtered local B are real cropped sources. A's
	// temporal context is independent of that crop, even if B has the same
	// samples in the overlap. Delta uses only the actual intersection.
	for _, tr := range r.pairedA {
		full, err := a.ReadTraceWindow(tr, 0, 255)
		if err != nil {
			t.Fatal(err)
		}
		want, err := poststack.FilterTraceWindow(context.Background(), full, 2000, parameters, 30, 74)
		if err != nil || !slices.Equal(want, s.rows[0].values[tr]) {
			t.Fatalf("A was cropped to B's input context: %v", err)
		}
	}
	if _, err := s.rows[1].read(0, 34, 35); err == nil {
		t.Fatal("snapshot exposed invented samples outside display")
	}
	if _, err := s.rows[2].read(1, 39, 40); err == nil {
		t.Fatal("delta exposed samples outside the common range")
	}
	r.key.b = bPath
	r.key.first = 0
	r.key.last = 255
	r.rows[1].path = bPath
	r.rows[1].ns = 256
	r.rows[1].offset = 0
	r.rows[1].traces = []int64{1, 3}
	r.pairedB = []int64{1, 3}
	s, err = computeViewFilterSnapshot(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := computeViewFilterSnapshot(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range r.pairedA {
		for i, v := range s.rows[2].values[tr] {
			if v != s.rows[0].values[tr][i]-s.rows[1].values[tr][i] || v != repeat.rows[2].values[tr][i] {
				t.Fatal("difference/repeat not derived from the two base traces")
			}
		}
	}
	style := viewFilterStyle{size: [3][2]int{{100, 80}, {100, 80}, {100, 80}}, mode: segy.DisplaySmooth}
	panels, err := renderViewFilterSnapshot(context.Background(), s, style)
	if err != nil {
		t.Fatal(err)
	}
	if len(panels[2].indices) != 8000 || panels[0].stats.MapMin != panels[1].stats.MapMin || panels[2].stats.MapMin != -panels[2].stats.MapMax {
		t.Fatal("filtered panel mapping incorrect")
	}
	got, _ := os.ReadFile(aPath)
	gotB, _ := os.ReadFile(bPath)
	if !bytes.Equal(got, before) || !bytes.Equal(gotB, beforeB) {
		t.Fatal("changed source")
	}
	if viewFilterResultCurrent(&viewFilterResult{id: 6, request: r}, 7, r.key, r.style) {
		t.Fatal("restore accepted old generation")
	}
	changed := r.key
	changed.line++
	if viewFilterResultCurrent(&viewFilterResult{id: 6, request: r}, 6, changed, r.style) {
		t.Fatal("new line accepted old result")
	}
	changed = r.key
	changed.b = cropPath
	if viewFilterResultCurrent(&viewFilterResult{id: 6, request: r}, 6, changed, r.style) {
		t.Fatal("new B accepted old result")
	}
	r.key.b = cropPath
	r.rows[1].path = cropPath
	r.rows[1].ns = 141
	r.rows[1].offset = 41
	r.rows[1].traces = []int64{1, 0}
	r.pairedB = []int64{1, 0}
	if mismatched, err := computeViewFilterSnapshot(context.Background(), r); err != nil || mismatched.deltaReason == "" || len(mismatched.rows[2].values) != 0 || len(mismatched.rows[1].values) == 0 {
		t.Fatalf("incompatible B must be browsable but not comparable: %v", err)
	}
}

func TestViewFilterNativeApplyRestore(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	path := filepath.Join(t.TempDir(), "a.sgy")
	makeViewFilterFixture(t, path, 1)
	fa, err := segy.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fa.Close()
	ga, _, err := fa.BuildGeometryIndexCached(189, 193, 1)
	if err != nil {
		t.Fatal(err)
	}
	h := createCtrl(0, "STATIC", "filter fixture", WS_OVERLAPPEDWINDOW, 0, 0, 1024, 740, 0)
	if h == 0 {
		t.Fatal("native fixture not created")
	}
	defer pDestroyWindow.Call(h)
	oldH, oldTD, oldPath, oldB := compareHwnd, compareTD, compareAPath, compareBPath
	oldMode, oldActive, oldBVisible, oldDiff := compareMode, compareWorkspaceActive, compareBVisible, compareShowDiff
	oldLine, oldX0, oldX1 := compareLineCoord, compareLineXMin, compareLineXMax
	oldS0, oldS1, oldTr0, oldTr1, oldStep := compareSampleStart, compareSampleEnd, compareTraceStart, compareTraceEnd, compareTraceStep
	oldA, oldPanelB, oldD := compareA, compareB, compareD
	oldFilter := viewFilter
	defer func() {
		cancelPoststackViewFilter()
		viewFilter = oldFilter
		compareHwnd, compareTD, compareAPath, compareBPath = oldH, oldTD, oldPath, oldB
		compareMode, compareWorkspaceActive, compareBVisible, compareShowDiff = oldMode, oldActive, oldBVisible, oldDiff
		compareLineCoord, compareLineXMin, compareLineXMax = oldLine, oldX0, oldX1
		compareSampleStart, compareSampleEnd, compareTraceStart, compareTraceEnd, compareTraceStep = oldS0, oldS1, oldTr0, oldTr1, oldStep
		compareA, compareB, compareD = oldA, oldPanelB, oldD
	}()
	compareHwnd, compareTD, compareAPath, compareBPath = h, &compareTimeData{fa: fa, ga: ga}, path, ""
	compareMode, compareWorkspaceActive, compareBVisible, compareShowDiff = 0, false, false, false
	compareLineCoord, compareLineXMin, compareLineXMax = 1000, 2000, 2003
	compareSampleStart, compareSampleEnd, compareTraceStart, compareTraceEnd, compareTraceStep = 0, 255, 0, 3, 1
	renderCompareLineBase()
	raw := append([]byte(nil), compareA.indices...)
	if len(raw) == 0 {
		t.Fatal("no baseline raster")
	}
	if err = applyPoststackViewFilterTarget(viewFilterB, poststack.ViewBandpass{LowHz: 8, HighHz: 40}); err == nil {
		t.Fatal("B allowed with no B loaded")
	}
	if err = applyPoststackViewFilterTarget(viewFilterA, poststack.ViewBandpass{LowHz: 8, HighHz: 40}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) && viewFilterIsPending() {
		handleViewFilterReady()
		time.Sleep(time.Millisecond)
	}
	if committedViewFilter() == nil || !compareA.filtered || bytes.Equal(raw, compareA.indices) || len(compareB.indices) != 0 {
		t.Fatalf("single panel did not filter: %s", viewFilter.status)
	}
	if err = applyPoststackViewFilterTarget(viewFilterA, poststack.ViewBandpass{LowHz: 10, HighHz: 50}); err != nil {
		t.Fatal(err)
	}
	if err := restorePoststackViewFilterTarget(viewFilterA); err != nil {
		t.Fatal(err)
	}
	handleViewFilterReady()
	if committedViewFilter() != nil || compareA.filtered || !bytes.Equal(raw, compareA.indices) {
		t.Fatal("restore did not recover original view")
	}

	// Compare's first B-only action starts from the legacy unfiltered frame.
	// Check the published frame, not just the worker's two sample maps.
	bPath := filepath.Join(t.TempDir(), "b.sgy")
	makeViewFilterFixture(t, bPath, 8)
	fb, err := segy.Open(bPath)
	if err != nil {
		t.Fatal(err)
	}
	defer fb.Close()
	gb, _, err := fb.BuildGeometryIndexCached(189, 193, 1)
	if err != nil {
		t.Fatal(err)
	}
	oldMapMin, oldMapMax, oldDeltaReason := compareMapMin, compareMapMax, compareDeltaReason
	defer func() { compareMapMin, compareMapMax, compareDeltaReason = oldMapMin, oldMapMax, oldDeltaReason }()
	compareTD.fb, compareTD.gb, compareBPath = fb, gb, bPath
	compareWorkspaceActive, compareBVisible, compareShowDiff = true, true, true
	renderCompareLineBase()
	aBefore, bBefore := compareA, compareB
	if len(aBefore.bgra) == 0 || len(bBefore.bgra) == 0 || !viewFilter.baseTone.valid {
		t.Fatal("unfiltered Compare fixture did not publish a shared display range")
	}
	if err := applyPoststackViewFilterTarget(viewFilterB, poststack.ViewBandpass{LowHz: 7, HighHz: 27}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) && viewFilterIsPending() {
		handleViewFilterReady()
		time.Sleep(time.Millisecond)
	}
	frame := committedViewFilter()
	if frame == nil || compareA.filtered || !compareB.filtered || !bytes.Equal(aBefore.bgra, compareA.bgra) || bytes.Equal(bBefore.bgra, compareB.bgra) || len(compareD.bgra) == 0 {
		t.Fatalf("B-only action did not preserve the already displayed A: %s", viewFilter.status)
	}
}

func TestViewFilterSideCombinationsAndUnchangedSideCache(t *testing.T) {
	dir := t.TempDir()
	aPath, bPath := filepath.Join(dir, "a.sgy"), filepath.Join(dir, "b.sgy")
	makeViewFilterFixture(t, aPath, 1)
	makeViewFilterFixture(t, bPath, 1)
	r := viewFilterRequest{key: viewFilterKey{a: aPath, b: bPath, first: 17, last: 26},
		rows: [2]viewFilterRows{
			{traces: []int64{1}, coords: []int32{100}, path: aPath, ns: 256, dt: 2000},
			{traces: []int64{1}, coords: []int32{100}, path: bPath, ns: 256, dt: 2000},
		}, pairedA: []int64{1}, pairedB: []int64{1}, pairCoords: []int32{100}}
	ctx := context.Background()
	r.settings = [2]SideFilterState{{BaseVersion: 7}, {BaseVersion: 9}}
	base, err := computeViewFilterSnapshot(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	p, q := poststack.ViewBandpass{LowHz: 8, HighHz: 40}, poststack.ViewBandpass{LowHz: 12, HighHz: 55}
	steps := []struct {
		name      string
		target    viewFilterTarget
		enabled   bool
		p         poststack.ViewBandpass
		bothEqual bool
	}{
		{"A only", viewFilterA, true, p, false},
		{"B only", viewFilterB, true, q, false},
		{"both", viewFilterBoth, true, p, true},
		{"both repeated", viewFilterBoth, true, p, true},
		{"restore A", viewFilterA, false, poststack.ViewBandpass{}, false},
		{"restore B", viewFilterB, false, poststack.ViewBandpass{}, true},
	}
	previous := base
	for _, step := range steps {
		r.settings = withViewFilterTarget(previous.settings, step.target, step.enabled, step.p)
		r.force = step.target
		current, err := computeViewFilterSnapshotWithCache(ctx, r, previous)
		if err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		a, b, delta := current.rows[0].values[1], current.rows[1].values[1], current.rows[2].values[1]
		for i, v := range delta {
			if v != a[i]-b[i] || (step.bothEqual && v != 0) {
				t.Fatalf("%s: delta %g vs %g", step.name, v, a[i]-b[i])
			}
		}
		for i, side := range current.settings {
			values := current.rows[i].values[1]
			if !step.target.includes(i) && (side != previous.settings[i] || &values[0] != &previous.rows[i].values[1][0]) {
				t.Fatalf("%s: other side changed or recalculated", step.name)
			}
			if !side.Enabled && !slices.Equal(values, base.rows[i].values[1]) {
				t.Fatalf("%s: restored side differs from its base", step.name)
			}
			if step.name == "both repeated" && !slices.Equal(values, previous.rows[i].values[1]) {
				t.Fatalf("%s: repeated application accumulated", step.name)
			}
		}
		previous = current
	}
}

func TestViewFilterDoesNotRecolorOtherPanel(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.sgy"), filepath.Join(dir, "b.sgy")
	makeViewFilterFixture(t, a, 1)
	makeViewFilterFixture(t, b, 8) // B dominates the pre-filter shared contrast
	tr, co := []int64{1, 3, 5}, []int32{100, 102, 104}
	request := viewFilterRequest{key: viewFilterKey{a: a, b: b, first: 19, last: 140},
		rows:    [2]viewFilterRows{{traces: tr, coords: co, path: a, ns: 256, dt: 2000}, {traces: tr, coords: co, path: b, ns: 256, dt: 2000}},
		pairedA: tr, pairedB: tr, pairCoords: co}
	style := viewFilterStyle{size: [3][2]int{{66, 70}, {65, 70}, {65, 70}}, mode: segy.DisplaySmooth}
	params := poststack.ViewBandpass{LowHz: 7, HighHz: 27}
	check := func(style viewFilterStyle, first, last int) {
		t.Helper()
		request.key.first, request.key.last = first, last
		request.settings, request.force = [2]SideFilterState{}, 0
		before, err := computeViewFilterSnapshot(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		previous, err := renderViewFilterSnapshot(ctx, before, style)
		if err != nil {
			t.Fatal(err)
		}
		for _, step := range []struct {
			target  viewFilterTarget
			enable  bool
			changed int
		}{{viewFilterB, true, 1}, {viewFilterA, true, 0}, {viewFilterB, false, 1}, {viewFilterA, false, 0}} {
			request.settings = withViewFilterTarget(before.settings, step.target, step.enable, params)
			request.force = step.target
			after, err := computeViewFilterSnapshotWithCache(ctx, request, before)
			if err != nil {
				t.Fatal(err)
			}
			panels, err := renderViewFilterSnapshot(ctx, after, style)
			if err != nil {
				t.Fatal(err)
			}
			unchanged := 1 - step.changed
			if !bytes.Equal(panels[unchanged].indices, previous[unchanged].indices) || panels[unchanged].stats.MapMin != previous[unchanged].stats.MapMin || panels[unchanged].stats.MapMax != previous[unchanged].stats.MapMax {
				t.Fatalf("filter target %d changed the opposite panel's raster or display range", step.target)
			}
			if bytes.Equal(panels[step.changed].indices, previous[step.changed].indices) || bytes.Equal(panels[2].indices, previous[2].indices) {
				t.Fatalf("filter target %d failed to update its own raster or Delta", step.target)
			}
			for _, trace := range tr {
				for i, av := range after.rows[0].values[trace] {
					if d := after.rows[2].values[trace][i]; d != av-after.rows[1].values[trace][i] {
						t.Fatalf("Delta differs from current visible samples at %d: %g", i, d)
					}
				}
			}
			if after.rows[2].first != first || after.rows[2].last != last {
				t.Fatal("Delta plotted the wrong time window")
			}
			if style.agc {
				noAGC := style
				noAGC.agc = false
				without, err := renderViewFilterSnapshot(ctx, after, noAGC)
				if err != nil || !bytes.Equal(panels[2].indices, without[2].indices) {
					t.Fatalf("AGC changed the already-computed difference: %v", err)
				}
			}
			before, previous = after, panels
		}
	}
	check(style, 19, 140)
	style.agc = true
	check(style, 58, 105) // new zoom window with display AGC still leaves Delta raw
}

func TestViewFilterCompositorSurvivesZoomAfterRestore(t *testing.T) {
	old := viewFilterKey{owner: 123, a: "A", b: "B", aVersion: 4, bVersion: 8, first: 0, last: 100}
	current := old
	current.first, current.last = 10, 50
	if !viewFilterSameComparePair(old, current) {
		t.Fatal("zoom after restore would fall back to the legacy Delta renderer")
	}
	current.bVersion++
	if viewFilterSameComparePair(old, current) {
		t.Fatal("compositor reused across a new B")
	}
}

func TestViewFilterNewBInvalidatesSamePathWithoutResettingA(t *testing.T) {
	oldFilter := viewFilter
	defer func() { viewFilter = oldFilter }()
	p, q := poststack.ViewBandpass{LowHz: 8, HighHz: 40}, poststack.ViewBandpass{LowHz: 12, HighHz: 55}
	viewFilter.active = [2]SideFilterState{{Enabled: true, Params: p, BaseVersion: 10}, {Enabled: true, Params: q, BaseVersion: 20}}
	before := viewFilterCurrentKey()
	id := viewFilter.id
	request := viewFilter.active
	viewFilter.requested = &request
	invalidatePoststackViewFilterB()
	after := viewFilterCurrentKey()
	if before.a != after.a || before.b != after.b || before.aVersion != after.aVersion || before.bVersion+1 != after.bVersion ||
		!viewFilter.active[0].Enabled || viewFilter.active[0].Params != p || viewFilter.active[1].Enabled || viewFilter.id == id {
		t.Fatal("new B must invalidate only B even if the paths are unchanged")
	}
	if viewFilter.requested != nil {
		t.Fatal("old request survived B reload")
	}
}

func TestViewFilterNativeIndependentBTimeWindow(t *testing.T) {
	dir := t.TempDir()
	aPath, bPath := filepath.Join(dir, "a.sgy"), filepath.Join(dir, "b.sgy")
	makeViewFilterFixture(t, aPath, 1)
	makeViewFilterFixture(t, bPath, 0.5)
	b, err := os.OpenFile(bPath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	var word [2]byte
	writeWord := func(off int64, value uint16) {
		t.Helper()
		binary.BigEndian.PutUint16(word[:], value)
		if _, err := b.WriteAt(word[:], off); err != nil {
			t.Fatal(err)
		}
	}
	writeWord(3216, 4000)
	for trace := 0; trace < 16; trace++ {
		base := int64(3600 + trace*(240+256*4))
		writeWord(base+108, 12)
		writeWord(base+116, 4000)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	p := poststack.ViewBandpass{LowHz: 8, HighHz: 40}
	r := viewFilterRequest{key: viewFilterKey{a: aPath, b: bPath, first: 9, last: 27},
		settings: withViewFilterTarget([2]SideFilterState{}, viewFilterBoth, true, p),
		rows: [2]viewFilterRows{
			{traces: []int64{1}, coords: []int32{100}, path: aPath, ns: 256, dt: 2000},
			{traces: []int64{1}, coords: []int32{100}, path: bPath, ns: 256, dt: 4000, physicalTimeWindow: true, requestFirstUS: 18000, requestLastUS: 54000},
		}, pairedA: []int64{1}, pairedB: []int64{1}, pairCoords: []int32{100}}
	got, err := computeViewFilterSnapshot(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if got.rows[0].first != 9 || got.rows[0].last != 27 || got.rows[1].first != 2 || got.rows[1].last != 10 || got.rows[1].originUS != 12000 {
		t.Fatalf("expected only native B samples in A's 18–54ms interval, got A %d–%d B %d–%d (origin %d)", got.rows[0].first, got.rows[0].last, got.rows[1].first, got.rows[1].last, got.rows[1].originUS)
	}
	if len(got.rows[1].values[1]) != 9 || got.deltaReason == "" || len(got.rows[2].values) != 0 {
		t.Fatal("incompatible native sample grid must remain independent without a Delta")
	}
}

// Opt-in generator for the visible GUI smoke test; ordinary CI never writes
// outside its own TempDir or overwrites an existing acceptance fixture.
func TestWriteViewFilterGUIFixture(t *testing.T) {
	dir := os.Getenv("SEISFORGE_VIEW_FILTER_FIXTURE_DIR")
	if dir == "" {
		t.Skip("no GUI fixture directory requested")
	}
	for i, name := range []string{"ViewFilter_A.sgy", "ViewFilter_B.sgy"} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("fixture path already exists or cannot be verified: %s", path)
		}
		makeViewFilterFixture(t, path, 1-float64(i)*0.5)
	}
}
