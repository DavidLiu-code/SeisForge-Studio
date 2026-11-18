//go:build windows

package main

import (
	"os"
	"strings"
	"testing"

	pseudo3dcore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudo3d"
)

func TestPhase4HomeCrookedUsesEmptyWorkspaceWithoutPicker(t *testing.T) {
	source := phase1FunctionSource(t, "workspace_windows.go", "chooseHomeFile")
	if !strings.Contains(source, "openEmptyCrookedWorkspace") {
		t.Fatal("Home Crooked card does not activate the empty workspace")
	}
	if strings.Contains(source, "browseCrookedFolder") || strings.Contains(source, "openCrookedProjectFolder") {
		t.Fatal("Home Crooked card still opens a picker or project directly")
	}
	adapter := phase1FunctionSource(t, "app_phase1_windows.go", "OpenEmpty")
	if !strings.Contains(adapter, "resetCrookedToEmpty") || strings.Contains(adapter, "loadFile") || strings.Contains(adapter, "rerender") {
		t.Fatal("empty Crooked adapter crosses a legacy data route")
	}
}

func TestPhase4PseudoDefaultsAndFrozenVolumeIsolation(t *testing.T) {
	camera := pseudo3dcore.DefaultCamera()
	if camera.Azimuth != -145 || camera.Elevation != 24 || camera.FOV != 35 || camera.Zoom != 1 || camera.VerticalScale != .55 {
		t.Fatalf("pseudo-3D factory camera changed: %+v", camera)
	}
	if pseudo3dcore.TextureBudget != 192<<20 || pseudo3dcore.MaxTextureWidth != 1024 || pseudo3dcore.MaxTextureHeight != 1536 {
		t.Fatal("pseudo-3D texture budget or caps changed")
	}
	volumeBytes, err := os.ReadFile("volume_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	volumeSource := string(volumeBytes)
	if strings.Contains(volumeSource, "pseudo3d") || strings.Contains(volumeSource, "pseudo_view.json") {
		t.Fatal("regular Volume3D renderer was coupled to pseudo-3D")
	}
	if pseudoDefaultsPath() == volumeDefaultsPath() || !strings.HasSuffix(pseudoDefaultsPath(), `\pseudo_view.json`) {
		t.Fatal("pseudo-3D settings do not have an independent file")
	}
}

func TestPhase4PseudoBoundsAdaptiveLineWorkersAndKeepsLatestSceneQueue(t *testing.T) {
	workers := phase1FunctionSource(t, "pseudo_windows.go", "startPseudoWorkers")
	if !strings.Contains(workers, "workerIndex < pseudoLineWorkerCount()") {
		t.Fatal("pseudo-3D does not use the bounded adaptive line-reader count")
	}
	lineWorkers, blockWorkers := pseudoLineWorkerCount(), pseudoBlockWorkerCount()
	if lineWorkers < 2 || lineWorkers > 4 || lineWorkers*blockWorkers > 8 {
		t.Fatalf("pseudo-3D I/O concurrency escaped its bounds: lines=%d blocks/line=%d", lineWorkers, blockWorkers)
	}
	queue := phase1FunctionSource(t, "pseudo_windows.go", "queuePseudoSceneRender") + phase1FunctionSource(t, "pseudo_windows.go", "queuePseudoSceneRenderScale")
	if !strings.Contains(queue, "<-pseudoState.renderQueue") || !strings.Contains(queue, "atomic.AddInt64(&pseudoRenderGen") {
		t.Fatal("pseudo-3D scene queue does not replace stale queued camera results")
	}
}
