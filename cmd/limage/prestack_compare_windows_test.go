//go:build windows

package main

import "testing"

func TestPrestackCompareRenderTokenRejectsEveryGeneration(t *testing.T) {
	state := prestackSession{
		ownerToken: 2, workspaceGeneration: 3, datasetGeneration: 4,
		selectionGeneration: 5, mappingGeneration: 6,
		compareBGeneration: 7, compareBMappingGeneration: 8,
		compareModeGeneration: 9, compareDisplayGeneration: 10,
		resizeGeneration: 11, sampleFirst: 12, sampleLast: 99, page: 3,
	}
	r := &prestackCompareRenderResult{
		owner: 17, ownerToken: 2, workspaceGeneration: 3,
		datasetGeneration: 4, selectionGeneration: 5, aMappingGeneration: 6,
		bGeneration: 7, bMappingGeneration: 8, compareModeGeneration: 9,
		displayGeneration: 10, resizeGeneration: 11, sampleFirst: 12,
		sampleLast: 99, comparePage: 3, width: 20, height: 30,
	}
	if !prestackCompareRenderMatches(r, state, 17, 20, 30) {
		t.Fatal("current compare token was rejected")
	}
	mutate := func(name string, f func(*prestackCompareRenderResult, *prestackSession, *int, *int)) {
		copyResult := *r
		copyState := state
		w, h := 20, 30
		f(&copyResult, &copyState, &w, &h)
		if prestackCompareRenderMatches(&copyResult, copyState, 17, w, h) {
			t.Fatalf("stale compare token accepted for %s", name)
		}
	}
	mutate("owner", func(r *prestackCompareRenderResult, _ *prestackSession, _ *int, _ *int) { r.owner = 18 })
	mutate("workspace", func(_ *prestackCompareRenderResult, s *prestackSession, _ *int, _ *int) { s.workspaceGeneration++ })
	mutate("dataset", func(_ *prestackCompareRenderResult, s *prestackSession, _ *int, _ *int) { s.datasetGeneration++ })
	mutate("selection", func(_ *prestackCompareRenderResult, s *prestackSession, _ *int, _ *int) { s.selectionGeneration++ })
	mutate("mapping", func(_ *prestackCompareRenderResult, s *prestackSession, _ *int, _ *int) { s.mappingGeneration++ })
	mutate("B mapping", func(_ *prestackCompareRenderResult, s *prestackSession, _ *int, _ *int) {
		s.compareBMappingGeneration++
	})
	mutate("compare mode", func(_ *prestackCompareRenderResult, s *prestackSession, _ *int, _ *int) { s.compareModeGeneration++ })
	mutate("display", func(_ *prestackCompareRenderResult, s *prestackSession, _ *int, _ *int) { s.compareDisplayGeneration++ })
	mutate("resize", func(_ *prestackCompareRenderResult, s *prestackSession, _ *int, _ *int) { s.resizeGeneration++ })
	mutate("sample window", func(_ *prestackCompareRenderResult, s *prestackSession, _ *int, _ *int) { s.sampleLast++ })
	mutate("panel width", func(_ *prestackCompareRenderResult, _ *prestackSession, w, _ *int) { *w = 21 })
}

func TestPrestackCompareRasterUsesBGRAContract(t *testing.T) {
	indices := []byte{0, 64, 128, 255}
	bgra := crookedPaletteBGRA(indices, 2)
	if len(bgra) != len(indices)*4 {
		t.Fatalf("palette expansion length=%d, want %d", len(bgra), len(indices)*4)
	}
}
