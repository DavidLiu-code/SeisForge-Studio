//go:build windows

package main

import (
	"os"
	"strings"
	"testing"
)

func TestPhase8GeometryZoomPreviewKeepsPersistentAOI(t *testing.T) {
	paint := phase1FunctionSource(t, "crooked_windows.go", "paintCrookedTransientOverlays")
	aoi := strings.Index(paint, "selected := crookedState.pseudoRange.Normalized()")
	zoom := strings.Index(paint, "drawCrookedZoomRectangle")
	if aoi < 0 || zoom <= aoi {
		t.Fatal("persistent AOI is not painted before the Geometry zoom preview")
	}
	if strings.Contains(paint, "else if crookedState.mapZoomDragging") {
		t.Fatal("Geometry zoom is still mutually exclusive with the persistent AOI")
	}
	zoomPaint := phase1FunctionSource(t, "crooked_windows.go", "drawCrookedZoomRectangle")
	for _, required := range []string{"rgbRef(148, 163, 184)", ", 1, 7, 4"} {
		if !strings.Contains(zoomPaint, required) {
			t.Fatalf("Geometry zoom preview is not the light one-pixel dashed style: missing %s", required)
		}
	}
}

func TestPhase8PlainDoubleClickAndDragThreshold(t *testing.T) {
	if pseudoDragThresholdReached(10, 10, 13, 13) {
		t.Fatal("sub-threshold click was promoted to a camera drag")
	}
	if !pseudoDragThresholdReached(10, 10, 13, 14) {
		t.Fatal("five-pixel movement did not promote to a camera drag")
	}
	wndProc := phase1FunctionSource(t, "pseudo_windows.go", "pseudoWndProc")
	doubleClick := strings.Index(wndProc, "case WM_LBUTTONDBLCLK:")
	leftDown := strings.Index(wndProc, "case WM_LBUTTONDOWN:")
	if doubleClick < 0 || leftDown <= doubleClick {
		t.Fatal("could not locate the pseudo-3-D double-click route")
	}
	route := wndProc[doubleClick:leftDown]
	if !strings.Contains(route, "wParam&pseudoMKControl == 0") || !strings.Contains(route, "navigatePseudoPickToCrooked") {
		t.Fatal("plain double-click does not navigate to Crooked 2-D")
	}
	source, err := os.ReadFile("pseudo_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(source), "Ctrl+双击") {
		t.Fatal("obsolete Ctrl+double-click guidance remains in the pseudo-3-D UI")
	}
}

func TestPhase8ProgressPresentationIsExplicit(t *testing.T) {
	tests := []struct {
		selected, loaded, failed int
		refining                 bool
		percent                  int
		text                     string
		state                    uintptr
	}{
		{text: "准备中", state: pseudoPBSTNormal},
		{selected: 10, loaded: 3, percent: 30, text: "加载 3/10", state: pseudoPBSTNormal},
		{selected: 10, loaded: 3, refining: true, percent: 30, text: "精修 3/10", state: pseudoPBSTNormal},
		{selected: 10, loaded: 10, percent: 100, text: "完成 10/10 ✓", state: pseudoPBSTNormal},
		{selected: 10, loaded: 8, failed: 2, percent: 100, text: "完成 8/10，失败 2", state: pseudoPBSTError},
	}
	for _, test := range tests {
		got := makePseudoProgressView(test.selected, test.loaded, test.failed, test.refining)
		if got.percent != test.percent || got.text != test.text || got.state != test.state {
			t.Fatalf("unexpected progress view for %+v: %+v", test, got)
		}
	}
}

func TestPhase8PseudoStyleIsSharedWithoutReaderReload(t *testing.T) {
	apply := phase1FunctionSource(t, "crooked_windows.go", "applyCrookedSharedStyle")
	for _, required := range []string{"crookedState.styleMode", "saveCrookedStyle", "crookedControlsUI.style", "pseudoApplyCrookedStyle"} {
		if !strings.Contains(apply, required) {
			t.Fatalf("shared style application is missing %s", required)
		}
	}
	pseudoApply := phase1FunctionSource(t, "pseudo_windows.go", "pseudoApplyCrookedStyle")
	for _, forbidden := range []string{"OpenReader", "enqueuePseudoLine", "pseudoReloadSelectedGain"} {
		if strings.Contains(pseudoApply, forbidden) {
			t.Fatalf("style switching reloads seismic data through %s", forbidden)
		}
	}
	ui := phase1FunctionSource(t, "pseudo_windows.go", "createPseudoUI")
	for _, label := range []string{"干净", "CIGVis", "解释", "标准"} {
		if !strings.Contains(ui, label) {
			t.Fatalf("pseudo style selector is missing %s", label)
		}
	}
}
