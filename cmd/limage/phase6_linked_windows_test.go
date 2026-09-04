//go:build windows

package main

import (
	"os"
	"strings"
	"testing"

	pseudo3dcore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudo3d"
)

func TestPhase6RangeResizeKeepsOppositeEdgesAndProjectLimits(t *testing.T) {
	project := pseudo3dcore.XYRange{XMin: 0, XMax: 100, YMin: 0, YMax: 200, Valid: true}
	original := pseudo3dcore.XYRange{XMin: 20, XMax: 80, YMin: 40, YMax: 160, Valid: true}
	left := resizedPseudoRange(original, project, pseudoRangeXMin, pseudo3dcore.Point{X: 35, Y: 99})
	if left.XMin != 35 || left.XMax != 80 || left.YMin != 40 || left.YMax != 160 {
		t.Fatalf("single-edge resize moved an opposite edge: %+v", left)
	}
	corner := resizedPseudoRange(original, project, pseudoRangeXMax|pseudoRangeYMin, pseudo3dcore.Point{X: 500, Y: -500})
	if corner.XMin != 20 || corner.XMax != 100 || corner.YMin != 0 || corner.YMax != 160 {
		t.Fatalf("corner resize did not clamp to project bounds: %+v", corner)
	}
	minimum := resizedPseudoRange(original, project, pseudoRangeXMin|pseudoRangeYMax, pseudo3dcore.Point{X: 200, Y: -200})
	if minimum.XMax-minimum.XMin < 1 || minimum.YMax-minimum.YMin < 2 {
		t.Fatalf("minimum one-percent span was not enforced: %+v", minimum)
	}
}

func TestPhase6StyleOrderAndPseudoMappingMatchVolume(t *testing.T) {
	want := []int{3, 0, 1, 2}
	if len(crookedStyleComboOrder) != len(want) {
		t.Fatalf("unexpected style count: %v", crookedStyleComboOrder)
	}
	for i := range want {
		if crookedStyleComboOrder[i] != want[i] || crookedStyleFromCombo(i) != want[i] {
			t.Fatalf("style order differs from Volume3D: %v", crookedStyleComboOrder)
		}
	}
	previous := crookedState.styleMode
	defer func() { crookedState.styleMode = previous }()
	crookedState.styleMode = 3
	if crookedPseudoDisplayStyle() != pseudo3dcore.StyleClean {
		t.Fatal("clean Crooked style did not propagate to pseudo-3D")
	}
	crookedState.styleMode = 2
	if crookedPseudoDisplayStyle() != pseudo3dcore.StyleStandard {
		t.Fatal("standard Crooked style did not propagate to pseudo-3D")
	}
}

func TestPhase6KeepsStyleAndAOIOutOfFrozenViewFiles(t *testing.T) {
	source, err := os.ReadFile("crooked_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	viewStart, viewEnd := strings.Index(text, "type crookedViewDefaults struct"), strings.Index(text, "type crookedStyleDefaults struct")
	if viewStart < 0 || viewEnd <= viewStart {
		t.Fatal("could not locate frozen crooked view schema")
	}
	frozen := text[viewStart:viewEnd]
	if strings.Contains(frozen, "StyleMode") || strings.Contains(frozen, "pseudoRange") || strings.Contains(frozen, "geometryViewport") {
		t.Fatal("Phase 6 session/style state leaked into crooked_view.json v1")
	}
	if !strings.Contains(text, "crooked_style.json") {
		t.Fatal("independent crooked_style.json persistence is missing")
	}
}

func TestPhase6WindowRestoreAndRangePreviewDoNotLoadReaders(t *testing.T) {
	mainSource, err := os.ReadFile("main_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mainSource), "pIsIconic.Call") || !strings.Contains(string(mainSource), "SW_RESTORE") {
		t.Fatal("top-level workspace restore does not handle minimized windows")
	}
	preview := phase1FunctionSource(t, "pseudo_windows.go", "updatePseudoRangeDrag")
	for _, forbidden := range []string{"OpenReader", "enqueuePseudoLine", "reloadPseudoForCrookedRange", "RenderTracePositions"} {
		if strings.Contains(preview, forbidden) {
			t.Fatalf("range preview performs loading work through %s", forbidden)
		}
	}
	finish := phase1FunctionSource(t, "pseudo_windows.go", "finishPseudoRangeDrag")
	if !strings.Contains(finish, "commitCrookedPseudoRange") {
		t.Fatal("range drag is not committed exactly at mouse release")
	}
}
