//go:build windows

package main

import (
	"os"
	"reflect"
	"strings"
	"testing"

	projectcore "github.com/DavidLiu-code/SeisForge-Studio/internal/project"
	pseudo3dcore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudo3d"
)

func TestPseudoViewV1MigratesToIndependentAxes(t *testing.T) {
	got := normalizePseudoViewDefaults(pseudoViewDefaults{Version: 1, Azimuth: -120, Elevation: 20, FOV: 35, Zoom: 1.2, VerticalScale: .75})
	if got.Version != 2 || got.AxisXScale != 1 || got.AxisYScale != 1 || got.AxisZScale != .75 {
		t.Fatalf("unexpected v1 migration: %+v", got)
	}
}

func TestPseudoViewOptionsMatchVolume3D(t *testing.T) {
	if !reflect.DeepEqual(pseudoAxisFactorOptions, volumeAxisFactorOptions) {
		t.Fatalf("pseudo axis factors differ from Volume3D: pseudo=%v volume=%v", pseudoAxisFactorOptions, volumeAxisFactorOptions)
	}
	if !reflect.DeepEqual(pseudoFOVOptions, volumeFOVOptions) {
		t.Fatalf("pseudo FOV options differ from Volume3D: pseudo=%v volume=%v", pseudoFOVOptions, volumeFOVOptions)
	}
}

func TestPhase5RangeRoutesBeforeReaderAndDoesNotPersistInCrookedView(t *testing.T) {
	loader := phase1FunctionSource(t, "pseudo_windows.go", "performPseudoLoad")
	clipAt := strings.Index(loader, "ClipTrajectory")
	renderAt := strings.Index(loader, "RenderTracePositions")
	if clipAt < 0 || renderAt < 0 || clipAt > renderAt {
		t.Fatal("exact AOI clipping is not performed before amplitude rendering")
	}
	window := phase1FunctionSource(t, "crooked_windows.go", "crookedWndProc")
	if !strings.Contains(window, "rangeMode") || !strings.Contains(window, "finishCrookedPseudoRange") || !strings.Contains(window, "clearCrookedPseudoRange") {
		t.Fatal("Crooked map does not route range drag/clear interactions")
	}
	source, err := os.ReadFile("crooked_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	defaultsStart, defaultsEnd := strings.Index(string(source), "type crookedViewDefaults struct"), strings.Index(string(source), "type crookedSession struct")
	if defaultsStart < 0 || defaultsEnd <= defaultsStart || strings.Contains(string(source)[defaultsStart:defaultsEnd], "pseudoRange") {
		t.Fatal("session AOI leaked into crooked_view.json state")
	}
}

func TestPseudoRunTextureSizesStayWithinLineBudget(t *testing.T) {
	runs := []pseudo3dcore.TraceRun{{Length: 10}, {Length: 30}, {Length: 60}}
	sizes := pseudoRunTextureSizes(runs, pseudo3dcore.TextureSize{Width: 100, Height: 80})
	pixels := 0
	for _, size := range sizes {
		if size.Width < 2 || size.Height < 2 {
			t.Fatalf("invalid segment size: %+v", size)
		}
		pixels += size.Width * size.Height
	}
	if pixels > 100*80 {
		t.Fatalf("segment textures exceed line allocation: %d", pixels)
	}
	if sizes[2].Width <= sizes[1].Width || sizes[1].Width <= sizes[0].Width {
		t.Fatalf("widths do not follow in-range length: %+v", sizes)
	}
}

func TestPseudoInitialRangeRejectsNavigationBeforeReader(t *testing.T) {
	line := &projectcore.CrookedProjectLine{Navigation: []projectcore.NavigationPoint{{X: -20, Y: 5}, {X: -10, Y: 5}}}
	inRange, known, _, _, _ := pseudoInitialRangeState(nil, line, pseudo3dcore.XYRange{XMin: 0, XMax: 10, YMin: 0, YMax: 10, Valid: true})
	if !known || inRange {
		t.Fatalf("outside navigation was not rejected: inRange=%v known=%v", inRange, known)
	}
}
