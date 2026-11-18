//go:build windows

package main

import (
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
)

func TestPrestackCompareDeltaVisibilityAndPanelCount(t *testing.T) {
	// UI-free callers historically construct a zero-value session and expect
	// the three-panel Compare view. An explicit checkbox state is required to
	// hide Delta.
	state := prestackSession{}
	if !prestackCompareDeltaVisible(&state) {
		t.Fatal("zero-value Compare session should keep Delta visible")
	}
	state.compareShowDeltaSet = true
	state.compareShowDelta = false
	if prestackCompareDeltaVisible(&state) {
		t.Fatal("explicitly disabled Delta should not be visible")
	}
	state.compareShowDelta = true
	state.compareBDataset = &dataset.SeismicDataset{}
	if prestackCompareDeltaVisible(&state) {
		t.Fatal("B without an index should keep Delta hidden")
	}
	state.compareBIndex = &prestackcore.PrestackIndex{}
	if !prestackCompareDeltaVisible(&state) {
		t.Fatal("B with an index should make Delta visible")
	}
	content := RECT{Left: 0, Top: 0, Right: 1200, Bottom: 900}
	panels := prestackComparePanelRectsForContentCount(content, 64, 2)
	if panels[0].Right <= panels[0].Left || panels[1].Right <= panels[1].Left {
		t.Fatalf("two-panel layout did not produce A/B panels: %+v", panels)
	}
	if panels[2] != (RECT{}) {
		t.Fatalf("disabled Delta should not reserve a third panel: %+v", panels[2])
	}
}

func TestPrestackCompareDeltaButtonRequiresReadyB(t *testing.T) {
	u, _ := prestackNarrowFakeControls()
	preB := prestackSession{page: 3, compareShowDelta: true, compareShowDeltaSet: true}
	placements := prestackNarrowPlacements(1400, 600, u, &preB)
	if placements[u.compareDeltaToggle].visible {
		t.Fatal("Delta button must stay hidden before B is indexed")
	}
	ready := prestackSession{page: 3, compareShowDelta: true, compareShowDeltaSet: true,
		compareBDataset: &dataset.SeismicDataset{}, compareBIndex: &prestackcore.PrestackIndex{}}
	placements = prestackNarrowPlacements(1400, 600, u, &ready)
	if !placements[u.compareDeltaToggle].visible {
		t.Fatal("Delta button should be visible after B index is ready")
	}
}
