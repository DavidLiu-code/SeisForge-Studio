//go:build windows

package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestPhase3CrookedHoverUsesCachedPartialRepaint(t *testing.T) {
	update := phase1FunctionSource(t, "crooked_windows.go", "updateCrookedCursorFromSection")
	if strings.Contains(update, "pInvalidateRect.Call(crookedHwnd, 0, 0)") {
		t.Fatal("crooked hover still invalidates the entire client")
	}
	if !strings.Contains(update, "invalidateCrookedCursorState") {
		t.Fatal("crooked hover does not invalidate old/new cursor strips")
	}
	paint := phase1FunctionSource(t, "crooked_windows.go", "paintCrooked")
	if !strings.Contains(paint, "ensureCrookedBaseCache") || !strings.Contains(paint, "pBitBlt.Call") || !strings.Contains(paint, "paintCrookedTransientOverlays") {
		t.Fatal("crooked paint does not use the static-scene cache plus transient overlay")
	}
	destroy := phase1FunctionSource(t, "crooked_windows.go", "destroyCrookedBaseCache")
	for _, required := range []string{"pSelectObject.Call", "pDeleteObject.Call", "pDeleteDC.Call"} {
		if !strings.Contains(destroy, required) {
			t.Fatalf("crooked GDI cache cleanup is missing %s", required)
		}
	}
}

func TestPhase3ProjectRoutesAndFrozenViewContracts(t *testing.T) {
	openProject := phase1FunctionSource(t, filepath.Join("..", "..", "internal", "app", "app.go"), "OpenCrookedFolder")
	if !strings.Contains(openProject, "OpenCrookedProject") {
		t.Fatal("folder route bypasses Application project routing")
	}
	adapter := phase1FunctionSource(t, "app_phase1_windows.go", "OpenProject")
	if !strings.Contains(adapter, "startCrookedProject") || strings.Contains(adapter, "loadFile") || strings.Contains(adapter, "rerender") {
		t.Fatal("Crooked project adapter crossed the legacy 2-D route")
	}
	defaults := defaultCrookedView()
	if defaults.Version != 1 || defaults.PaletteIndex != 2 || defaults.GainPercent != 0 || defaults.AxisMode != crookedAxisDistance {
		t.Fatalf("Phase 3 changed frozen crooked defaults: %+v", defaults)
	}
}

func TestPhase3MapHitMathAndPerLineViewSnapshot(t *testing.T) {
	if got := distancePointToSegment(5, 3, 0, 0, 10, 0); got != 3 {
		t.Fatalf("line segment hit distance=%v want=3", got)
	}
	if got := distancePointToSegment(15, 4, 0, 0, 10, 0); got < 6.403 || got > 6.404 {
		// The expected endpoint distance is sqrt(5^2+4^2).
		t.Fatalf("endpoint hit distance=%v", got)
	}
	previous := crookedState
	defer func() { crookedState = previous }()
	crookedState = crookedSession{activeLine: 0, axisMode: crookedAxisCDP, viewStart: 0.2, viewEnd: 0.7, currentPosition: 17, currentSample: 33,
		projectLines: []crookedProjectLineState{{}}}
	saveCrookedActiveLineView()
	view := crookedState.projectLines[0].view
	if view.axisMode != crookedAxisCDP || view.viewStart != 0.2 || view.viewEnd != 0.7 || view.currentPosition != 17 || view.currentSample != 33 {
		t.Fatalf("per-line view snapshot mismatch: %+v", view)
	}
}
