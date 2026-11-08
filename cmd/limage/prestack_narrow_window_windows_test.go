//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
)

var prestackNarrowWidths = []int{640, 800, 939, 940, 1100, 1119, 1120, 1279, 1280, 1306, 1320, 1360}

type prestackNarrowPlacement struct {
	rect    layoutRect
	visible bool
}

// These handles are local integer identifiers only. No native control or
// window is created, and no fake identifier is passed to a Win32 function.
func prestackNarrowFakeControls() (*prestackControls, map[uintptr]string) {
	u := &prestackControls{mappingLabels: make([]uintptr, 14), mappingEdits: make([]uintptr, 14)}
	names := make(map[uintptr]string)
	next := uintptr(1)
	fields := reflect.ValueOf(u).Elem()
	setHandle := func(field reflect.Value, name string) {
		// Control fields are unexported; write only this test-owned uintptr.
		*(*uintptr)(unsafe.Pointer(field.UnsafeAddr())) = next
		names[next] = name
		next++
	}
	for i := 0; i < fields.NumField(); i++ {
		field, name := fields.Field(i), fields.Type().Field(i).Name
		switch field.Kind() {
		case reflect.Uintptr:
			setHandle(field, name)
		case reflect.Array, reflect.Slice:
			for j := 0; j < field.Len(); j++ {
				setHandle(field.Index(j), fmt.Sprintf("%s[%d]", name, j))
			}
		}
	}
	return u, names
}

func prestackNarrowPlacements(width, height int, u *prestackControls, state *prestackSession) map[uintptr]prestackNarrowPlacement {
	placements := make(map[uintptr]prestackNarrowPlacement)
	prestackControlsLayoutForSize(width, height, u, state, func(handle uintptr, x, y, w, h int, visible bool) {
		// The pure layout commits only the final placement of each control.
		placements[handle] = prestackNarrowPlacement{layoutRect{x, y, x + w, y + h}, visible}
	})
	return placements
}

func prestackNarrowRectsOverlap(a, b layoutRect) bool {
	return a.Left < b.Right && b.Left < a.Right && a.Top < b.Bottom && b.Top < a.Bottom
}

func prestackNarrowFixedControls(u *prestackControls) map[uintptr]bool {
	return map[uintptr]bool{
		u.home: true, u.open: true, u.path: true, u.tabs: true,
		u.status: true, u.progress: true, u.progressLabel: true,
		u.toolbarScroll: true, u.mappingText: true,
	}
}

func TestPrestackNarrowViewportAndEveryScrollRequest(t *testing.T) {
	for _, width := range prestackNarrowWidths {
		for page, requiredWidth := range []int{1440, 670, 760, 1400, 160} {
			t.Run(fmt.Sprintf("%d/page%d", width, page), func(t *testing.T) {
				viewport := prestackToolbarViewportForSize(width, page)
				wantCanvas := maxLayoutInt(width, requiredWidth)
				if viewport.CanvasWidth != wantCanvas || viewport.MaxScroll != wantCanvas-width {
					t.Fatalf("incorrect virtual viewport: %+v, want canvas=%d", viewport, wantCanvas)
				}
				if viewport.MaxScroll > 0 {
					if viewport.ExtraTop != prestackToolbarScrollHeight || viewport.ScrollRect.Left < 0 || viewport.ScrollRect.Right > width ||
						viewport.ScrollRect.width() <= 0 || viewport.ScrollRect.Top < 69 || viewport.ScrollRect.Bottom >= 76+viewport.ExtraTop {
						t.Fatalf("scrollbar escapes client or overlaps tabs/page toolbar: %+v", viewport)
					}
				} else if viewport.ExtraTop != 0 {
					t.Fatalf("unneeded scrollbar shifted content: %+v", viewport)
				}
				mid, step := viewport.MaxScroll/2, maxLayoutInt(32, width/2)
				for _, tc := range []struct {
					request, track, want int
				}{
					{0, 0, maxLayoutInt(0, mid-32)},
					{1, 0, minLayoutInt(viewport.MaxScroll, mid+32)},
					{2, 0, maxLayoutInt(0, mid-step)},
					{3, 0, minLayoutInt(viewport.MaxScroll, mid+step)},
					{4, viewport.MaxScroll + 99, viewport.MaxScroll},
					{5, -99, 0},
					{6, 0, 0}, {7, 0, viewport.MaxScroll}, {8, 0, mid},
				} {
					if got := prestackToolbarScrollPosition(width, page, mid, tc.request, tc.track); got != tc.want {
						t.Fatalf("SB%d moved to %d, want %d", tc.request, got, tc.want)
					}
				}
				for _, position := range []int{-999, viewport.MaxScroll + 999} {
					want := minLayoutInt(viewport.MaxScroll, maxLayoutInt(0, position))
					if got := prestackToolbarScrollPosition(width, page, position, 8, 0); got != want {
						t.Fatalf("resize/page transition did not clamp offset %d: got=%d want=%d", position, got, want)
					}
				}
			})
		}
	}
}

func TestPrestackNarrowActualControlsRemainReachableAtScrollEnds(t *testing.T) {
	u, names := prestackNarrowFakeControls()
	fixed := prestackNarrowFixedControls(u)
	var states []prestackSession
	for _, kind := range []prestackcore.GatherType{prestackcore.GatherCMP, prestackcore.GatherShot, prestackcore.GatherReceiver,
		prestackcore.GatherOffset, prestackcore.GatherAzimuth, prestackcore.GatherRaw} {
		for _, display := range []int{0, 1} {
			states = append(states, prestackSession{selection: prestackcore.GatherSelection{Type: kind}, display: display})
		}
	}
	for page := 1; page <= 4; page++ {
		for _, hasB := range []bool{false, true} {
			state := prestackSession{page: page}
			if hasB {
				state.compareBDataset = &dataset.SeismicDataset{}
			}
			states = append(states, state)
		}
	}
	for _, width := range prestackNarrowWidths {
		for _, original := range states {
			for _, done := range []bool{false, true} {
				t.Run(fmt.Sprintf("%d/page%d/kind%d/display%d/B%v/done%v", width, original.page, original.selection.Type, original.display, original.compareBDataset != nil, done), func(t *testing.T) {
					state := original
					state.progressDone = done
					state.sampleFirst, state.sampleLast = 0, 255
					state.renderGeneration, state.selectionGeneration, state.resizeGeneration = 23, 24, 25
					viewport := prestackToolbarViewportForState(width, state.page, &state)
					baseline := prestackNarrowPlacements(viewport.CanvasWidth, 480, u, &state)
					left := prestackNarrowPlacements(width, 480, u, &state)
					state.toolbarScrollOffset = viewport.MaxScroll
					before := state
					right := prestackNarrowPlacements(width, 480, u, &state)
					if !reflect.DeepEqual(state, before) {
						t.Fatal("toolbar placement changed selection, sample window or render generations")
					}
					for handle, expected := range baseline {
						if fixed[handle] {
							if !reflect.DeepEqual(left[handle], right[handle]) {
								t.Fatalf("fixed header/footer/content %s moved while scrolling", names[handle])
							}
							continue
						}
						if !expected.visible {
							if left[handle].visible || right[handle].visible {
								t.Fatalf("inactive control %s became visible", names[handle])
							}
							continue
						}
						if expected.rect.Left < 0 || expected.rect.Right > viewport.CanvasWidth || expected.rect.width() <= 0 || expected.rect.height() <= 0 {
							t.Fatalf("%s escapes virtual canvas %d: %+v", names[handle], viewport.CanvasWidth, expected.rect)
						}
						// Scrolling is continuous; an endpoint is not guaranteed to
						// align with every control when the row has two variants (for
						// example before/after loading B). Verify that some legal
						// scroll position can expose the complete control.
						minScroll := maxLayoutInt(0, expected.rect.Right-width)
						maxScroll := minLayoutInt(viewport.MaxScroll, expected.rect.Left)
						if minScroll > maxScroll {
							t.Fatalf("%s cannot be fully reached in viewport: rect=%+v width=%d maxScroll=%d", names[handle], expected.rect, width, viewport.MaxScroll)
						}
						for _, endpoint := range []struct {
							position int
							placed   prestackNarrowPlacement
						}{{0, left[handle]}, {viewport.MaxScroll, right[handle]}} {
							p := endpoint.placed
							if p.rect.Left+endpoint.position != expected.rect.Left || p.rect.Right+endpoint.position != expected.rect.Right ||
								p.rect.Top != expected.rect.Top+viewport.ExtraTop || p.rect.Bottom != expected.rect.Bottom+viewport.ExtraTop {
								t.Fatalf("%s changed size/row instead of translating: %+v", names[handle], p)
							}
							if p.visible && viewport.MaxScroll > 0 && prestackNarrowRectsOverlap(p.rect, viewport.ScrollRect) {
								t.Fatalf("%s overlaps the new scrollbar band", names[handle])
							}
						}
					}
					if state.page == 0 || state.page == 2 {
						// Both the Gather/Raw toolbars and Mapping's target/fields
						// must remain disjoint at each horizontal scroll position.
						for a, pa := range baseline {
							if !pa.visible || fixed[a] {
								continue
							}
							for b, pb := range baseline {
								if b <= a || !pb.visible || fixed[b] {
									continue
								}
								if prestackNarrowRectsOverlap(pa.rect, pb.rect) {
									t.Fatalf("page %d controls overlap: %s %+v and %s %+v", state.page, names[a], pa.rect, names[b], pb.rect)
								}
							}
						}
					}
					if state.page == 0 {
						panel := prestackPanelLayoutForSize(width, 480, 0)
						if left[u.kind].rect.Top != 76+viewport.ExtraTop || left[u.display].rect.Top != 107+viewport.ExtraTop ||
							panel.ToolbarBottom >= panel.Scene.Top {
							t.Fatal("Gather/Raw lost its two fixed rows or covered the seismic scene")
						}
					}
				})
			}
		}
	}
}

func TestPrestackNarrowPanelsUseClientWidthNotToolbarCanvas(t *testing.T) {
	for _, width := range prestackNarrowWidths {
		for page := 0; page <= 4; page++ {
			viewport := prestackToolbarViewportForSize(width, page)
			left := prestackPanelLayoutForSize(width, 600, page)
			// Scroll position belongs only to the control viewport, not panels.
			position := prestackToolbarScrollPosition(width, page, 0, 7, 0)
			if position != viewport.MaxScroll || !reflect.DeepEqual(left, prestackPanelLayoutForSize(width, 600, page)) {
				t.Fatal("scrolling changed page geometry")
			}
			if left.Scene.Left != 62 || left.Scene.Right != width-24 || left.Content.Right != width-20 || left.Footer.Right != width ||
				left.Footer.Top != 571 || left.Content.Bottom > left.Footer.Top || left.Scene.Bottom > left.Footer.Top {
				t.Fatalf("page%d panels use virtual width or overlap footer at %d: %+v", page, width, left)
			}
			if page == 3 {
				panels := prestackComparePanelRectsForContent(prestackLayoutRECT(left.Content), prestackCompareSummaryHeight)
				if panels != prestackComparePanelRectsForContent(prestackLayoutRECT(left.Content), prestackCompareSummaryHeight) {
					t.Fatal("Compare panel dimensions changed with toolbar scroll")
				}
				for i, panel := range panels {
					if panel.Right <= panel.Left || panel.Bottom <= panel.Top || int(panel.Left) < left.Content.Left || int(panel.Right) > left.Content.Right ||
						int(panel.Top) < left.Content.Top || int(panel.Bottom) > left.Content.Bottom {
						t.Fatalf("Compare panel%d escapes actual narrow content: %+v", i, panel)
					}
				}
			}
		}
	}
}

func TestPrestackNarrowCompareShortEmptyAndMinimumThreeRows(t *testing.T) {
	for _, tc := range []struct {
		height int
		empty  bool
	}{{240, true}, {480, false}, {600, false}} {
		layout := prestackPanelLayoutForSize(640, tc.height, 3)
		panels := prestackComparePanelRectsForContent(prestackLayoutRECT(layout.Content), prestackCompareSummaryHeight)
		if tc.empty {
			if panels != ([3]RECT{}) {
				t.Fatalf("640x%d should show increase-height empty state, not unusable panels: %+v", tc.height, panels)
			}
			continue
		}
		for i, panel := range panels {
			if panel.Bottom-panel.Top < 42 || int(panel.Right-panel.Left) != layout.Content.width() {
				t.Fatalf("640x%d panel%d cannot contain its title/image: %+v", tc.height, i, panel)
			}
			if i > 0 && (panels[i-1].Bottom >= panel.Top || panels[i-1].Left != panel.Left || panels[i-1].Right != panel.Right) {
				t.Fatalf("narrow Compare did not stack three separate rows: %+v", panels)
			}
		}
	}
}

func TestPrestackNarrowMinMaxOnlyChangesPrestackLimits(t *testing.T) {
	if prestackMinClientWidth != 640 || prestackMinClientHeight != 480 {
		t.Fatalf("unexpected Prestack minimum client size: %dx%d", prestackMinClientWidth, prestackMinClientHeight)
	}
	wantWidth, wantHeight := prestackFrameExtent(640, 480)
	// Native messages own this structure outside the Go stack. Allocate the
	// test's equivalent on the heap so a stack growth cannot stale lParam.
	mmi := reflect.New(reflect.TypeOf(MINMAXINFO{})).Interface().(*MINMAXINFO)
	mmi.PtMaxTrackSize = POINT{X: 2048, Y: 1024}
	got := prestackWndProc(0, WM_GETMINMAXINFO, 0, uintptr(unsafe.Pointer(mmi)))
	runtime.KeepAlive(mmi)
	if got != 0 ||
		int(mmi.PtMinTrackSize.X) != wantWidth || int(mmi.PtMinTrackSize.Y) != wantHeight ||
		mmi.PtMaxTrackSize != (POINT{X: 2048, Y: 1024}) {
		t.Fatalf("Prestack min/max handler changed non-minimum bounds or ignored client-to-frame conversion: %+v", mmi)
	}
	// Reading other handlers protects their existing independent policies;
	// none is invoked, and the test creates no real window.
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate window-policy source")
	}
	for _, tc := range []struct {
		file       string
		minW, minH int
	}{
		{"main_windows.go", 500, 320}, {"volume_windows.go", 920, 620},
		{"crooked_windows.go", 900, 650}, {"pseudo_windows.go", 1000, 650},
	} {
		content, err := os.ReadFile(filepath.Join(filepath.Dir(source), tc.file))
		if err != nil {
			t.Fatal(err)
		}
		text := string(content)
		start := strings.Index(text, "case WM_GETMINMAXINFO:")
		if start < 0 {
			t.Fatalf("%s has no independent minimum-size handler", tc.file)
		}
		block := text[start:]
		if end := strings.Index(block[1:], "\n\tcase "); end >= 0 {
			block = block[:end+1]
		}
		if !strings.Contains(block, fmt.Sprintf("PtMinTrackSize.X = %d", tc.minW)) || !strings.Contains(block, fmt.Sprintf("PtMinTrackSize.Y = %d", tc.minH)) ||
			strings.Contains(block, "prestackMinClient") || strings.Contains(block, "prestackToolbar") {
			t.Fatalf("Prestack repair affected %s minimum-size policy: %s", tc.file, block)
		}
	}
}
