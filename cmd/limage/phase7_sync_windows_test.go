//go:build windows

package main

import (
	"math"
	"os"
	"strings"
	"testing"

	pseudo3dcore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudo3d"
)

func phase7Texture(width, height int) *pseudo3dcore.Texture {
	pixels := make([]byte, width*height)
	for i := range pixels {
		pixels[i] = byte(i % 256)
	}
	return &pseudo3dcore.Texture{Width: width, Height: height, Indices: pixels}
}

func phase7Run(start, end float64) pseudo3dcore.TraceRun {
	return pseudo3dcore.TraceRun{TraceIndices: []int64{0, 1}, Positions: []float64{start, end},
		Points: []pseudo3dcore.Point{{X: start, Y: 0}, {X: end, Y: 0}}, U: []float64{0, 1},
		PositionStart: start, PositionEnd: end, Length: end - start}
}

func TestPhase7RangeContractionUsesCachedTextureWithoutRefine(t *testing.T) {
	source := pseudoCurtainSegment{points: phase7Run(0, 10).Points, u: []float64{0, 1}, texture: phase7Texture(11, 4),
		positionStart: 0, positionEnd: 10, length: 10}
	segments, covered := pseudoSegmentsForRuns([]pseudo3dcore.TraceRun{phase7Run(2, 8)}, pseudo3dcore.TextureSize{Width: 8, Height: 4}, []pseudoCurtainSegment{source})
	if !covered || len(segments) != 1 || segments[0].preview {
		t.Fatalf("cached AOI contraction was not final: covered=%v segments=%+v", covered, segments)
	}
	if !segments[0].texture.Valid() || segments[0].texture.Width > 8 || segments[0].texture.Height > 4 {
		t.Fatalf("cached contraction ignored the new texture allocation: %+v", segments[0].texture)
	}
	if math.Abs(segments[0].positionStart-2) > 1e-9 || math.Abs(segments[0].positionEnd-8) > 1e-9 {
		t.Fatalf("cached contraction has the wrong trace span: %+v", segments[0])
	}
}

func TestPhase7RangeExpansionKeepsPreviewAndRequestsRefine(t *testing.T) {
	source := pseudoCurtainSegment{points: phase7Run(2, 8).Points, u: []float64{0, 1}, texture: phase7Texture(8, 4),
		positionStart: 2, positionEnd: 8, length: 6}
	segments, covered := pseudoSegmentsForRuns([]pseudo3dcore.TraceRun{phase7Run(0, 10)}, pseudo3dcore.TextureSize{Width: 10, Height: 4}, []pseudoCurtainSegment{source})
	if covered || len(segments) != 1 || !segments[0].preview {
		t.Fatalf("AOI expansion did not retain a provisional cached curtain: covered=%v segments=%+v", covered, segments)
	}
}

func TestPhase7GeometryGestureAndIncrementalRoute(t *testing.T) {
	source, err := os.ReadFile("crooked_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, removed := range []string{"放大范围", "总图复位", "IDCROOKED_RANGEZOOM", "IDCROOKED_MAPRESET", "geometryFollowRange"} {
		if strings.Contains(text, removed) {
			t.Fatalf("removed Geometry control/state remains: %s", removed)
		}
	}
	wndProc := phase1FunctionSource(t, "crooked_windows.go", "crookedWndProc")
	for _, required := range []string{"mapZoomPending", "mapZoomDragging", "finishCrookedMapZoom", "WM_LBUTTONDBLCLK", "WM_CROOKED_MAP_CLICK", "crookedMapClickGen", "resetCrookedMapView"} {
		if !strings.Contains(wndProc, required) {
			t.Fatalf("Geometry drag/double-click route is missing %s", required)
		}
	}
	reload := phase1FunctionSource(t, "pseudo_windows.go", "reloadPseudoForCrookedRange")
	if !strings.Contains(reload, "updatePseudoRangeInPlace") || strings.Contains(reload, "pseudoSession{") || strings.Contains(reload, "startPseudoWorkers") || strings.Contains(reload, "OpenReader") {
		t.Fatal("AOI update still restarts the pseudo-3D session or opens a Reader synchronously")
	}
}

func TestPhase7LinkedCursorAndOverlayClipping(t *testing.T) {
	publish := phase1FunctionSource(t, "crooked_windows.go", "publishCrookedCursorToPseudo")
	if !strings.Contains(publish, "33*time.Millisecond") || !strings.Contains(publish, "applyPseudoLinkedCursor") {
		t.Fatal("2-D cursor publishing is not throttled or linked to pseudo-3-D")
	}
	apply := phase1FunctionSource(t, "pseudo_windows.go", "applyPseudoLinkedCursor")
	for _, required := range []string{"ProjectGeneration", "setPseudoListSelected", "applyPseudoSelection", "范围外"} {
		if !strings.Contains(apply, required) {
			t.Fatalf("linked cursor behavior is missing %s", required)
		}
	}
	paint := phase1FunctionSource(t, "pseudo_windows.go", "paintPseudoDynamicOverlays")
	if !strings.Contains(paint, "pIntersectClipRect.Call") || !strings.Contains(paint, "pseudoLinkedCursorProjection") {
		t.Fatal("dynamic pseudo overlays are not clipped or do not paint the linked cursor")
	}
	clear := phase1FunctionSource(t, "pseudo_windows.go", "queuePseudoSceneRenderScale")
	if !strings.Contains(clear, "clearPseudoHoverOverlay") {
		t.Fatal("camera/scene changes do not clear the old hover projection")
	}
	ready := phase1FunctionSource(t, "pseudo_windows.go", "handlePseudoRenderResult")
	if !strings.Contains(ready, "result.preview") || !strings.Contains(ready, "queuePseudoSceneRender(true)") {
		t.Fatal("half-resolution AOI preview is not followed by a full-resolution frame")
	}
}
