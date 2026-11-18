//go:build windows

package main

import (
	"reflect"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
)

func TestPrestackCloseBRebuildsIndependentAFrameAndClearsPending(t *testing.T) {
	savedState, savedUI, savedHwnd := prestackState, prestackUI, prestackHwnd
	prestackDeliveryMu.Lock()
	savedB, savedRender := prestackPendingCompareB, prestackPendingCompareRender
	prestackDeliveryMu.Unlock()
	t.Cleanup(func() {
		prestackState, prestackUI, prestackHwnd = savedState, savedUI, savedHwnd
		prestackDeliveryMu.Lock()
		prestackPendingCompareB, prestackPendingCompareRender = savedB, savedRender
		prestackDeliveryMu.Unlock()
	})
	prestackHwnd, prestackUI = 0, prestackControls{}
	a := &dataset.SeismicDataset{Path: "A.sgy"}
	aIndex := &prestackcore.PrestackIndex{}
	aMapping := prestackcore.DefaultHeaderMapping()
	aGather := prestackcore.GatherResult{TraceIndices: []int64{2, 0, 1}}
	for _, page := range []int{3, 0, 2, 4} {
		bCancelled, renderCancelled, renders := false, false, 0
		prestackState = prestackSession{
			page: page, dataset: a, index: aIndex, mapping: aMapping, gather: aGather,
			datasetGeneration: 101, mappingGeneration: 102, selectionGeneration: 103,
			sampleFirst: 12, sampleLast: 100, gain: 7, palette: 3, agc: true,
			compareUseLimits: true, compareLimitMin: -1, compareLimitMax: 1,
			compareBDataset: &dataset.SeismicDataset{Path: "B.sgy"}, compareBIndex: &prestackcore.PrestackIndex{},
			compareBLoading: true, compareBError: "old B error", compareRendering: true,
			compareBCancel: func() { bCancelled = true }, compareRenderCancel: func() { renderCancelled = true },
			compareA: []byte{1}, compareB: []byte{2}, compareDelta: []byte{3}, compareWidth: 10, compareHeight: 10,
			compareViewFirst: 1, compareViewLast: 1,
			compareRows:        []prestackcore.CompareLogicalRow{{HasB: true, BTrace: 7}},
			compareDisplayRows: []prestackcore.CompareLogicalRow{{HasB: true, BTrace: 7}},
		}
		prestackDeliveryMu.Lock()
		prestackPendingCompareB = &prestackCompareBResult{}
		prestackPendingCompareRender = &prestackCompareRenderResult{}
		prestackDeliveryMu.Unlock()
		closePrestackCompareB(func() {
			renders++
			if prestackState.compareBDataset != nil || prestackState.compareBIndex != nil || prestackState.compareBLoading {
				t.Fatal("A render started before B was detached")
			}
			// The production callback starts the A-only worker using these exact
			// unchanged A inputs; no B-only logical view may survive the close.
			if prestackState.compareViewFirst != 0 || prestackState.compareViewLast != -1 || len(prestackState.compareDisplayRows) != 0 {
				t.Fatal("old B filter/view leaked into A-only regeneration")
			}
		})
		if !bCancelled || !renderCancelled || prestackState.compareBCancel != nil || prestackState.compareRenderCancel != nil || prestackState.compareRendering {
			t.Fatalf("page %d left a B/render task attached", page)
		}
		if prestackState.compareBDataset != nil || prestackState.compareBIndex != nil || prestackState.compareBError != "" || len(prestackState.compareB) != 0 || len(prestackState.compareDelta) != 0 || prestackState.compareWidth != 0 || prestackState.compareHeight != 0 {
			t.Fatalf("page %d left B buffers or state", page)
		}
		if (page == 3 && renders != 1) || (page != 3 && renders != 0) {
			t.Fatalf("page %d scheduled %d A-only renders", page, renders)
		}
		if prestackState.dataset != a || prestackState.index != aIndex || prestackState.mapping != aMapping || !reflect.DeepEqual(prestackState.gather, aGather) || prestackState.datasetGeneration != 101 || prestackState.mappingGeneration != 102 || prestackState.selectionGeneration != 103 || prestackState.sampleFirst != 12 || prestackState.sampleLast != 100 || prestackState.gain != 7 || prestackState.palette != 3 || !prestackState.agc || !prestackState.compareUseLimits || prestackState.compareLimitMin != -1 || prestackState.compareLimitMax != 1 {
			t.Fatalf("page %d altered A or its display settings", page)
		}
		prestackDeliveryMu.Lock()
		pending := prestackPendingCompareB != nil || prestackPendingCompareRender != nil
		prestackDeliveryMu.Unlock()
		if pending {
			t.Fatal("pending B/render result retained after close")
		}
	}
}
