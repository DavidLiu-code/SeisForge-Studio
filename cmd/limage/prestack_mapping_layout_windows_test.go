//go:build windows

package main

import (
	"fmt"
	"testing"
)

func TestPrestackMappingTargetOwnRowFieldsButtonsAndSummary(t *testing.T) {
	u, names := prestackNarrowFakeControls()
	handles := []uintptr{u.compareMappingTarget, u.mappingAuto, u.mappingApply, u.mappingText}
	handles = append(handles, u.mappingLabels...)
	handles = append(handles, u.mappingEdits...)
	for _, width := range []int{320, 480, 610, 640, 1120, 1680} {
		for _, height := range []int{480, 700, 1089} {
			t.Run(fmt.Sprintf("%dx%d", width, height), func(t *testing.T) {
				state := prestackSession{page: 2}
				viewport := prestackToolbarViewportForSize(width, state.page)
				base := prestackNarrowPlacements(width, height, u, &state)
				target := base[u.compareMappingTarget].rect
				for _, field := range append(append([]uintptr{}, u.mappingLabels...), u.mappingEdits...) {
					if base[field].rect.Top < target.Bottom+8 {
						t.Fatalf("%s overlaps target row: target=%+v field=%+v", names[field], target, base[field].rect)
					}
					if base[field].rect.Bottom > base[u.mappingAuto].rect.Top {
						t.Fatalf("%s overlaps mapping action row", names[field])
					}
				}
				if base[u.mappingText].rect.Top <= base[u.mappingApply].rect.Bottom || base[u.mappingText].rect.Bottom >= base[u.status].rect.Top {
					t.Fatal("mapping summary overlaps buttons or status footer")
				}
				for _, position := range []int{0, viewport.MaxScroll / 2, viewport.MaxScroll} {
					state.toolbarScrollOffset = position
					placed := prestackNarrowPlacements(width, height, u, &state)
					for i, a := range handles {
						if !placed[a].visible {
							continue
						}
						for _, b := range handles[i+1:] {
							if placed[b].visible && prestackNarrowRectsOverlap(placed[a].rect, placed[b].rect) {
								t.Fatalf("mapping controls overlap: %s %+v, %s %+v", names[a], placed[a].rect, names[b], placed[b].rect)
							}
						}
					}
				}
				// A narrow client retains the existing horizontal viewport. Every
				// edit/button is fully reachable, not silently shrunk or discarded.
				for _, handle := range handles {
					if handle == u.mappingText {
						continue // summary is fixed to the client and has its own vertical scroll
					}
					state.toolbarScrollOffset = clampInt(base[handle].rect.Right-width, 0, viewport.MaxScroll)
					actual := prestackNarrowPlacements(width, height, u, &state)[handle]
					if !actual.visible || actual.rect.Left < 0 || actual.rect.Right > width {
						t.Fatalf("mapping %s not reachable at width %d: %+v", names[handle], width, actual)
					}
				}
			})
		}
	}
}

func TestPrestackMappingTargetRetainsTwoChoiceDropdown(t *testing.T) {
	saved := prestackUI
	t.Cleanup(func() { prestackUI = saved })
	prestackUI = prestackControls{compareMappingTarget: 123}
	for _, fieldHeight := range []int{1, 24, 32} {
		got := prestackComboLayoutHeight(123, fieldHeight)
		if got != prestackComboPopupHeight(IDPRESTACK_MAPPING_TARGET, fieldHeight) || got <= fieldHeight+40 {
			t.Fatalf("A/B popup collapsed after layout: height=%d field=%d", got, fieldHeight)
		}
	}
}
