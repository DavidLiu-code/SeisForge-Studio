//go:build windows

package main

import (
	"context"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
)

func TestPrestackMappingAutoTargetsBWithoutChangingA(t *testing.T) {
	aMapping := prestackcore.DefaultHeaderMapping()
	bMapping := aMapping
	bMapping.SourceIDByte = 17
	a := &dataset.SeismicDataset{Path: "A.sgy"}
	b := &dataset.SeismicDataset{Path: "B.sgy"}
	state := prestackSession{dataset: a, index: &prestackcore.PrestackIndex{}, mapping: aMapping,
		mappingTargetB: true, compareBDataset: b, compareBMapping: bMapping,
		datasetGeneration: 8, mappingGeneration: 9, selectionGeneration: 10,
		gather: prestackcore.GatherResult{TraceIndices: []int64{2, 0, 1}}}
	before := state
	aCalls, bCalls := 0, 0
	detectA := func() { aCalls++ }
	detectB := func(path string, m prestackcore.HeaderMapping) {
		bCalls++
		if path != b.Path || m != bMapping {
			t.Fatalf("B detection captured A input: path=%s mapping=%+v", path, m)
		}
	}
	if !dispatchPrestackMappingAuto(state, detectA, detectB) || aCalls != 0 || bCalls != 1 {
		t.Fatalf("B automatic detection routed incorrectly: A=%d B=%d", aCalls, bCalls)
	}
	if !reflect.DeepEqual(state, before) {
		t.Fatal("dispatch modified A or caller state")
	}
	for _, unavailable := range []prestackSession{
		{mappingTargetB: true},
		{mappingTargetB: true, compareBDataset: b, compareBLoading: true},
	} {
		if dispatchPrestackMappingAuto(unavailable, detectA, detectB) || aCalls != 0 || bCalls != 1 {
			t.Fatal("unavailable B fell back to rebuilding A")
		}
	}
	state.mappingTargetB = false
	if !dispatchPrestackMappingAuto(state, detectA, detectB) || aCalls != 1 || bCalls != 1 {
		t.Fatal("A's existing automatic detection route changed")
	}
}

func compareBRetryFixture() (prestackSession, prestackCompareBResult) {
	state := prestackSession{
		ownerToken: 2, workspaceGeneration: 3, datasetGeneration: 4, mappingGeneration: 5,
		selectionGeneration: 6, compareBGeneration: 7, compareBDatasetGeneration: 8,
		compareBMappingGeneration: 9, compareModeGeneration: 10,
		compareBLoading: true, compareBMapping: prestackcore.DefaultHeaderMapping(),
		dataset: &dataset.SeismicDataset{Path: "A.sgy"}, index: &prestackcore.PrestackIndex{},
		gather: prestackcore.GatherResult{TraceIndices: []int64{2, 0, 1}}, sampleFirst: 12, sampleLast: 100,
	}
	r := prestackCompareBResult{generation: 7, owner: 17, ownerToken: 2, workspaceGeneration: 3,
		datasetGeneration: 4, aMappingGeneration: 5, selectionGeneration: 6,
		bDatasetGeneration: 8, mappingGeneration: 9, compareModeGeneration: 10,
		path: "B.sgy", mapping: state.compareBMapping, detected: true,
	}
	return state, r
}

func TestPrestackCompareBViewChangeRetriesWithoutOrphaningLoad(t *testing.T) {
	for _, change := range []string{"page", "selection", "both"} {
		t.Run(change, func(t *testing.T) {
			state, result := compareBRetryFixture()
			before := state
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			state.compareBCancel = cancel
			if change != "selection" {
				state.page = 2
				state.compareModeGeneration++
			}
			if change != "page" {
				state.selectionGeneration++
			}
			if prestackCompareBResultMatches(&result, state, 17, 7) {
				t.Fatal("stale page/selection result became displayable")
			}
			restarts := 0
			if !retryPrestackCompareBViewChange(&result, &state, 17, 7, func(path string, mapping *prestackcore.HeaderMapping, detect bool) {
				restarts++
				if ctx.Err() == nil || state.compareBCancel != nil || state.compareBLoading {
					t.Fatal("old task still attached when replacement starts")
				}
				if path != "B.sgy" || *mapping != state.compareBMapping || !detect {
					t.Fatal("restart lost B path, independent mapping or detection request")
				}
				state.compareBGeneration++
				state.compareBLoading = true
			}) || restarts != 1 {
				t.Fatal("view change silently orphaned loading B")
			}
			if state.dataset != before.dataset || state.index != before.index || state.mapping != before.mapping ||
				!reflect.DeepEqual(state.gather, before.gather) || state.datasetGeneration != before.datasetGeneration ||
				state.mappingGeneration != before.mappingGeneration || state.sampleFirst != before.sampleFirst || state.sampleLast != before.sampleLast {
				t.Fatal("B retry altered A metadata, gather, mapping or sample view")
			}
			if retryPrestackCompareBViewChange(&result, &state, 17, 7, func(string, *prestackcore.HeaderMapping, bool) { t.Fatal("old completion restarted a newer B request") }) {
				t.Fatal("same stale completion restarted twice")
			}
		})
	}
}

func TestPrestackCompareBRetryNeverResurrectsClosedOrReplacedIdentity(t *testing.T) {
	cases := map[string]func(*prestackSession, *prestackCompareBResult){
		"closed":        func(s *prestackSession, _ *prestackCompareBResult) { s.compareBLoading = false },
		"replacement":   func(s *prestackSession, _ *prestackCompareBResult) { s.compareBGeneration++ },
		"owner":         func(_ *prestackSession, r *prestackCompareBResult) { r.owner++ },
		"owner token":   func(s *prestackSession, _ *prestackCompareBResult) { s.ownerToken++ },
		"workspace":     func(s *prestackSession, _ *prestackCompareBResult) { s.workspaceGeneration++ },
		"A dataset":     func(s *prestackSession, _ *prestackCompareBResult) { s.datasetGeneration++ },
		"A mapping":     func(s *prestackSession, _ *prestackCompareBResult) { s.mappingGeneration++ },
		"B dataset":     func(s *prestackSession, _ *prestackCompareBResult) { s.compareBDatasetGeneration++ },
		"B mapping":     func(s *prestackSession, _ *prestackCompareBResult) { s.compareBMappingGeneration++ },
		"missing path":  func(_ *prestackSession, r *prestackCompareBResult) { r.path = "" },
		"current token": func(s *prestackSession, r *prestackCompareBResult) { r.compareModeGeneration = s.compareModeGeneration },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			state, r := compareBRetryFixture()
			state.compareModeGeneration++
			mutate(&state, &r)
			cancelled := false
			state.compareBCancel = func() { cancelled = true }
			if retryPrestackCompareBViewChange(&r, &state, 17, 7, func(string, *prestackcore.HeaderMapping, bool) { t.Fatal("invalid retry") }) || cancelled {
				t.Fatal("stale B completion disturbed a newer/closed lifecycle")
			}
		})
	}
}

func TestPrestackCompareBResultChecksAMapping(t *testing.T) {
	state, r := compareBRetryFixture()
	if !prestackCompareBResultMatches(&r, state, 17, 7) {
		t.Fatal("current B metadata rejected")
	}
	state.mappingGeneration++
	if prestackCompareBResultMatches(&r, state, 17, 7) {
		t.Fatal("B metadata decoded against old A mapping accepted")
	}
}

func TestPrestackCompareLoadingBHasCancelableNonoverlappingToolbar(t *testing.T) {
	u := prestackControls{compareOpenB: 1, compareCloseB: 2, palette: 3}
	state := prestackSession{page: 3, compareBLoading: true}
	type placement struct {
		left, right int
		visible     bool
	}
	controls := map[uintptr]placement{}
	prestackControlsLayoutForSize(1680, 900, &u, &state, func(h uintptr, x, _ int, w, _ int, visible bool) {
		controls[h] = placement{x, x + w, visible}
	})
	if !controls[2].visible || controls[2].left < controls[1].right || controls[3].left < controls[2].right {
		t.Fatalf("loading B cannot be closed or palette covers Close B: %+v", controls)
	}
}

func TestPrestackCompareBUnconfirmedMappingIsExplicit(t *testing.T) {
	state := prestackSession{compareBNeedsConfirmation: true, compareBDataset: &dataset.SeismicDataset{Path: "B.sgy"}}
	if !strings.Contains(strings.Join(prestackCompareSummaryLines(state), "\n"), "需要确认") ||
		!strings.Contains(prestackCompareDeltaEmptyMessage(state), "确认 B") {
		t.Fatal("low-confidence B detection looked loaded or delta-ready")
	}
}

func TestPrestackCompareBMappingConfirmationSurvivesWorkspaceHide(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// This owner is created without WS_VISIBLE. Exercise the actual workspace
	// Hide route without showing or manipulating the user's running window.
	owner := createCtrl(0, "STATIC", "B confirmation hide regression", WS_OVERLAPPEDWINDOW, 0, 0, 640, 480, 0)
	if owner == 0 {
		t.Fatal("could not create hidden workspace test owner")
	}
	defer pDestroyWindow.Call(owner)
	savedState, savedUI, savedHwnd := prestackState, prestackUI, prestackHwnd
	prestackDeliveryMu.Lock()
	oldIndex, oldRender, oldB, oldCompare := prestackPendingIndex, prestackPendingRender, prestackPendingCompareB, prestackPendingCompareRender
	oldIndexGen, oldRenderGen, oldBGen := prestackDeliveryIndexGeneration, prestackDeliveryRenderGeneration, prestackDeliveryCompareBGeneration
	prestackDeliveryMu.Unlock()
	t.Cleanup(func() {
		prestackState, prestackUI, prestackHwnd = savedState, savedUI, savedHwnd
		prestackDeliveryMu.Lock()
		prestackPendingIndex, prestackPendingRender, prestackPendingCompareB, prestackPendingCompareRender = oldIndex, oldRender, oldB, oldCompare
		prestackDeliveryIndexGeneration, prestackDeliveryRenderGeneration, prestackDeliveryCompareBGeneration = oldIndexGen, oldRenderGen, oldBGen
		prestackDeliveryMu.Unlock()
	})
	prestackHwnd, prestackUI = owner, prestackControls{}
	a, b := &dataset.SeismicDataset{Path: "A.sgy"}, &dataset.SeismicDataset{Path: "B.sgy"}
	ai := &prestackcore.PrestackIndex{}
	mapping := prestackcore.DefaultHeaderMapping()
	prestackState = prestackSession{
		page: 2, dataset: a, index: ai, mapping: mapping, mappingTargetB: true,
		compareBDataset: b, compareBMapping: mapping, compareBNeedsConfirmation: true,
		compareBDetection: prestackcore.MappingDetection{NeedsConfirmation: true, Confidence: 0.25},
		datasetGeneration: 101, mappingGeneration: 102, selectionGeneration: 103,
		gather: prestackcore.GatherResult{TraceIndices: []int64{2, 0, 1}}, sampleFirst: 3, sampleLast: 19,
	}
	before := prestackState
	if err := (&prestackWorkspaceAdapter{}).Hide(); err != nil {
		t.Fatal(err)
	}
	if !prestackState.compareBNeedsConfirmation || !prestackState.mappingTargetB ||
		prestackState.compareBDataset != b || prestackState.compareBIndex != nil || prestackState.compareBMapping != mapping ||
		!reflect.DeepEqual(prestackState.compareBDetection, before.compareBDetection) {
		t.Fatal("Hide discarded B's unconfirmed metadata or switched its mapping target")
	}
	if prestackState.dataset != a || prestackState.index != ai || prestackState.mapping != mapping ||
		prestackState.datasetGeneration != 101 || prestackState.mappingGeneration != 102 ||
		!reflect.DeepEqual(prestackState.gather, before.gather) || prestackState.sampleFirst != 3 || prestackState.sampleLast != 19 {
		t.Fatal("suspending B confirmation changed A")
	}
	// These are the same read-only presentation/dispatch paths used after the
	// workspace is shown again. No extra native Show is needed in this test.
	if !strings.Contains(strings.Join(prestackCompareSummaryLines(prestackState), "\n"), "需要确认") ||
		!strings.Contains(prestackCompareDeltaEmptyMessage(prestackState), "确认 B") {
		t.Fatal("restored Compare lost its pending-B confirmation explanation")
	}
	calledB := false
	if !dispatchPrestackMappingAuto(prestackState, func() { t.Fatal("restored B target routed to A") }, func(path string, _ prestackcore.HeaderMapping) {
		calledB = path == b.Path
	}) || !calledB {
		t.Fatal("restored B mapping cannot be independently confirmed/re-detected")
	}
}

func TestPrestackMappingTargetSwitchSynchronizesNativeSummary(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	owner := createCtrl(0, "STATIC", "Mapping summary regression", WS_OVERLAPPEDWINDOW, 0, 0, 640, 480, 0)
	if owner == 0 {
		t.Fatal("could not create hidden test owner")
	}
	defer pDestroyWindow.Call(owner)
	summary := createCtrl(owner, "EDIT", "", WS_CHILD|ES_READONLY|traceESMultiline, 0, 0, 600, 200, 0)
	if summary == 0 {
		t.Fatal("could not create hidden summary edit")
	}
	savedState, savedUI := prestackState, prestackUI
	t.Cleanup(func() { prestackState, prestackUI = savedState, savedUI })
	mapping := prestackcore.DefaultHeaderMapping()
	prestackUI = prestackControls{mappingText: summary}
	prestackState = prestackSession{mapping: mapping, compareBMapping: mapping,
		compareBDataset: &dataset.SeismicDataset{Path: "B.sgy"},
		detection: prestackcore.MappingDetection{Mapping: mapping, Confidence: 0.9,
			Candidates: []prestackcore.MappingCandidate{{Field: "A_candidate", Byte: 9}}, Warnings: []string{"A_warning"}},
		compareBDetection: prestackcore.MappingDetection{Mapping: mapping, Confidence: 0.25,
			Candidates: []prestackcore.MappingCandidate{{Field: "B_candidate", Byte: 17}}, Warnings: []string{"B_warning"}},
	}
	for _, targetB := range []bool{true, false, true, false} {
		prestackState.mappingTargetB = targetB
		populatePrestackMapping()
		text := getText(summary)
		want, reject, confidence := "A_candidate", "B_candidate", "90.0%"
		if targetB {
			want, reject, confidence = "B_candidate", "A_candidate", "25.0%"
		}
		if !strings.Contains(text, want) || strings.Contains(text, reject) || !strings.Contains(text, confidence) {
			t.Fatalf("target B=%v left another target's summary: %s", targetB, text)
		}
	}
	if prestackState.mapping != mapping || prestackState.compareBMapping != mapping {
		t.Fatal("switching summary mutated A/B header mappings")
	}
}
