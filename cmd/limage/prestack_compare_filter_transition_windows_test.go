//go:build windows

package main

import (
	"context"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
)

// Exercise native checkbox notifications and the production renderer's row
// selection, not a duplicate of its viewport clamping. Test windows are hidden.
func TestPrestackCompareFilterTransitionsResetOnlyHorizontalViewport(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	owner := createCtrl(0, "STATIC", "Compare filter regression", WS_OVERLAPPEDWINDOW, 0, 0, 1000, 660, 0)
	if owner == 0 {
		t.Fatal("could not create hidden test owner")
	}
	defer pDestroyWindow.Call(owner)
	var controls [5]uintptr
	for i := range controls {
		controls[i] = createCtrl(owner, "BUTTON", "", WS_CHILD|BS_AUTOCHECKBOX, 0, 0, 40, 24, IDPRESTACK_COMPARE_FILTER_MATCHED+i)
		if controls[i] == 0 {
			t.Fatal("could not create hidden checkbox")
		}
	}
	status := createCtrl(owner, "STATIC", "", WS_CHILD, 0, 0, 500, 24, 0)
	if status == 0 {
		t.Fatal("could not create hidden status control")
	}
	savedState, savedUI, savedHwnd := prestackState, prestackUI, prestackHwnd
	prestackDeliveryMu.Lock()
	savedOwner, savedOwnerToken := prestackDeliveryHwnd, prestackDeliveryOwnerToken
	savedGeneration, savedPending := prestackDeliveryCompareRenderGeneration, prestackPendingCompareRender
	prestackDeliveryMu.Unlock()
	t.Cleanup(func() {
		if prestackState.compareRenderCancel != nil {
			prestackState.compareRenderCancel()
		}
		prestackState, prestackUI, prestackHwnd = savedState, savedUI, savedHwnd
		prestackDeliveryMu.Lock()
		prestackDeliveryHwnd, prestackDeliveryOwnerToken = savedOwner, savedOwnerToken
		prestackDeliveryCompareRenderGeneration, prestackPendingCompareRender = savedGeneration, savedPending
		prestackDeliveryMu.Unlock()
	})

	openFixture := func(name string) (*dataset.SeismicDataset, *prestackcore.PrestackIndex) {
		t.Helper()
		data, err := dataset.NewManager().Open(compareResourceFixture(t, name))
		if err != nil {
			t.Fatal(err)
		}
		reader, err := data.OpenReader()
		if err != nil {
			t.Fatal(err)
		}
		index, buildErr := prestackcore.BuildIndex(context.Background(), reader, prestackcore.DefaultHeaderMapping(), nil)
		closeErr := reader.Close()
		if buildErr != nil || closeErr != nil {
			t.Fatalf("fixture index/close: %v / %v", buildErr, closeErr)
		}
		return data, index
	}
	aData, aIndex := openFixture("filter_A.sgy")
	bData, bIndex := openFixture("filter_B.sgy")
	selection := prestackcore.GatherSelection{Type: prestackcore.GatherRaw}
	aGather, err := aIndex.Gather(selection)
	if err != nil {
		t.Fatal(err)
	}
	// Sixty records per side: 56 matched, one only, two ambiguous, one
	// invalid. The union has 64 logical rows, including absent-side slots.
	match := prestackcore.CompareMatchResult{Matched: 56,
		AOnly: []int64{56}, BOnly: []int64{56},
		AmbiguousA: []int64{57, 58}, AmbiguousB: []int64{57, 58},
		InvalidA: []int64{59}, InvalidB: []int64{59}}
	for i := 0; i < 56; i++ {
		match.Pairs = append(match.Pairs, prestackcore.TraceMatchPair{ATrace: int64(i), BTrace: int64(55 - i), AIndex: i, BIndex: 55 - i})
	}
	prestackHwnd = owner
	prestackUI = prestackControls{status: status,
		compareFilterMatched: controls[0], compareFilterAOnly: controls[1], compareFilterBOnly: controls[2],
		compareFilterAmbiguous: controls[3], compareFilterInvalid: controls[4]}
	prestackState = prestackSession{page: 3, ownerToken: 91, dataset: aData, index: aIndex,
		selection: selection, gather: aGather, sampleFirst: 3, sampleLast: 25,
		compareBDataset: bData, compareBIndex: bIndex, compareBMatch: match,
		compareBAxis:     prestackcore.SampleAxisCompatibility{Compatible: true},
		compareViewFirst: 0, compareViewLast: -1, gain: 5}
	prestackDeliveryMu.Lock()
	prestackDeliveryHwnd, prestackDeliveryOwnerToken = owner, prestackState.ownerToken
	prestackPendingCompareRender = nil
	prestackDeliveryMu.Unlock()

	awaitFrame := func() *prestackCompareRenderResult {
		t.Helper()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			prestackDeliveryMu.Lock()
			result := prestackPendingCompareRender
			prestackDeliveryMu.Unlock()
			if result != nil && result.generation == prestackState.compareRenderGeneration {
				if result.err != nil || result.bErr != nil {
					t.Fatalf("production frame failed: %v / %v", result.err, result.bErr)
				}
				return result
			}
			select {
			case <-tick.C:
			case <-deadline.C:
				t.Fatal("production filter frame did not complete")
			}
		}
	}
	setMask := func(mask uint32) {
		t.Helper()
		for i, control := range controls {
			bit := uint32(1) << i
			if (prestackState.compareFilterMask & bit) == (mask & bit) {
				continue
			}
			checked := uintptr(0)
			if mask&bit != 0 {
				checked = BST_CHECKED
			}
			prestackState.comparePickPreview = prestackComparePickPreview{Active: true, Row: 0}
			setPrestackStatus("Compare A | physical trace 999 (old hover)")
			previousGeneration := prestackState.compareDisplayGeneration
			pSendMessageW.Call(control, BM_SETCHECK, checked, 0)
			prestackWndProc(owner, WM_COMMAND, uintptr(IDPRESTACK_COMPARE_FILTER_MATCHED+i), control)
			if prestackState.comparePickPreview.Active || strings.Contains(getText(status), "physical trace") {
				t.Fatal("changed filter retained an old physical-trace preview/status")
			}
			if prestackState.compareDisplayGeneration == previousGeneration {
				t.Fatal("changed filter did not invalidate the old display token")
			}
			awaitFrame()
		}
	}

	for _, step := range []struct {
		mask uint32
		rows int
	}{
		{3, 57}, {1, 56}, {0, 0}, {2, 1}, {0, 0}, {4, 1},
		{0, 0}, {8, 4}, {0, 0}, {16, 2}, {24, 6}, {31, 64},
	} {
		setMask(step.mask)
		if len(prestackState.compareRows) != step.rows || len(prestackState.compareDisplayRows) != step.rows ||
			prestackState.compareViewFirst != 0 || prestackState.compareViewLast != step.rows-1 {
			t.Fatalf("mask %d retained old viewport: rows/display=%d/%d view=%d:%d want=%d", step.mask,
				len(prestackState.compareRows), len(prestackState.compareDisplayRows), prestackState.compareViewFirst, prestackState.compareViewLast, step.rows)
		}
		if prestackState.sampleFirst != 3 || prestackState.sampleLast != 25 {
			t.Fatal("horizontal filter transition reset the vertical sample window")
		}
		want := prestackcore.FilterCompareRows(match, aIndex.Records, bIndex.Records, prestackCompareFilterSet())
		if !reflect.DeepEqual(prestackState.compareDisplayRows, want) {
			t.Fatalf("mask %d lost physical row order or absent-side slots", step.mask)
		}
		for _, row := range prestackState.compareDisplayRows {
			for panel := 0; panel < 2; panel++ {
				trace, _, present := prestackCompareTraceForPanel(panel, row, true)
				expectedTrace, expectedPresent := row.ATrace, row.HasA
				if panel == 1 {
					expectedTrace, expectedPresent = row.BTrace, row.HasB
				}
				if present != expectedPresent || (present && trace != expectedTrace) {
					t.Fatal("filter transition changed Ctrl-click physical trace mapping")
				}
			}
		}
	}

	// A duplicate notification is not a filter change. It must preserve both
	// horizontal zoom and sample range and must not schedule another worker.
	prestackState.compareViewFirst, prestackState.compareViewLast = 10, 13
	prestackState.compareDisplayRows = append([]prestackcore.CompareLogicalRow(nil), prestackState.compareRows[10:14]...)
	prestackState.comparePickPreview = prestackComparePickPreview{Active: true, Panel: 0, Row: 1}
	before := prestackState
	setPrestackStatus("unchanged hover")
	for i, control := range controls {
		prestackWndProc(owner, WM_COMMAND, uintptr(IDPRESTACK_COMPARE_FILTER_MATCHED+i), control)
	}
	if prestackState.compareViewFirst != 10 || prestackState.compareViewLast != 13 ||
		prestackState.sampleFirst != 3 || prestackState.sampleLast != 25 ||
		prestackState.compareDisplayGeneration != before.compareDisplayGeneration ||
		prestackState.compareRenderGeneration != before.compareRenderGeneration ||
		prestackState.comparePickPreview != before.comparePickPreview ||
		!reflect.DeepEqual(prestackState.compareDisplayRows, before.compareDisplayRows) || getText(status) != "unchanged hover" {
		t.Fatal("same-mask notification discarded zoom/preview or started another render")
	}
	for _, path := range []string{aData.Path, bData.Path} {
		if err := compareResourceExclusiveRead(path); err != nil {
			t.Fatalf("filter-transition worker retained a Reader: %v", err)
		}
	}
}
