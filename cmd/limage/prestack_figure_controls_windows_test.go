//go:build windows

package main

import (
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
)

func TestPrestackFigureButtonSharesExistingToolbarRows(t *testing.T) {
	u, names := prestackNarrowFakeControls()
	fixed := prestackNarrowFixedControls(u)
	for _, width := range []int{640, 940, 1280, 1340, 1680} {
		for _, page := range []int{0, 1, 2, 3, 4} {
			for _, hasB := range []bool{false, true} {
				for _, kind := range []prestackcore.GatherType{prestackcore.GatherCMP, prestackcore.GatherRaw} {
					state := prestackSession{page: page, selection: prestackcore.GatherSelection{Type: kind}}
					if hasB {
						state.compareBDataset = &dataset.SeismicDataset{}
					}
					viewport := prestackToolbarViewportForSize(width, page)
					// Full canvas lets us check the final row without clipping;
					// the existing narrow tests verify native scrollbar reachability.
					placed := prestackNarrowPlacements(viewport.CanvasWidth, 600, u, &state)
					p := placed[u.toolsWindow]
					for _, hidden := range []uintptr{u.figureExport, u.exportGather, u.exportCSV, u.sessionSave, u.sessionOpen, u.batchPlan, u.batchRun, u.batchEdit, u.batchReport, u.rawTraceStart, u.rawSampleMode} {
						if placed[hidden].visible {
							t.Fatalf("legacy action/range control remained in main toolbar on page %d: %s", page, names[hidden])
						}
					}
					if p.visible != (page == 0 || page == 3 || page == 4) {
						t.Fatalf("tools action visibility on page %d: %+v", page, p)
					}
					if !p.visible {
						continue
					}
					if (p.rect.Top < 76 || p.rect.Top > 132) || p.rect.Right > viewport.CanvasWidth {
						t.Fatalf("tools action adds a row or escapes the toolbar: %+v", p)
					}
					for handle, other := range placed {
						if handle != u.toolsWindow && !fixed[handle] && other.visible && prestackNarrowRectsOverlap(p.rect, other.rect) {
							t.Fatalf("figure action overlaps %s", names[handle])
						}
					}
				}
			}
		}
	}
}
