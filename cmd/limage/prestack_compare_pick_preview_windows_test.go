//go:build windows

package main

import (
	"testing"

	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
)

func comparePickFixture() (prestackSession, [3]RECT) {
	rows := []prestackcore.CompareLogicalRow{
		{ATrace: 0, BTrace: 37, HasA: true, HasB: true, Classification: prestackcore.CompareRowMatched},
		{ATrace: 25, HasA: true, Classification: prestackcore.CompareRowAOnly},
		{BTrace: 9, HasB: true, Classification: prestackcore.CompareRowBOnly},
	}
	state := prestackSession{page: 3, compareDisplayRows: rows, compareWidth: 101, compareHeight: 80, compareRenderGeneration: 7, compareViewFirst: 15}
	state.compareBAxis.Compatible = true
	state.compareA = make([]byte, 101*80*4)
	state.compareB = make([]byte, 101*80*4)
	state.compareDelta = make([]byte, 101*80*4)
	var panels [3]RECT
	for i := range panels {
		panels[i] = RECT{Left: int32(10 + i*120), Top: 100, Right: int32(113 + i*120), Bottom: 215}
	}
	return state, panels
}

func TestPrestackCompareCtrlPreviewUsesPairedPhysicalRows(t *testing.T) {
	state, panels := comparePickFixture()
	for panel := range panels {
		r := prestackCompareRasterRect(panels[panel])
		pick := prestackComparePickAt(&state, panels, int(r.Left), int(r.Top)+10, true)
		if !pick.Active || pick.Panel != panel || pick.Row != 0 || pick.Generation != 7 {
			t.Fatalf("panel %d preview: %+v", panel, pick)
		}
		physical, role, ok := prestackCompareTraceForPanel(panel, state.compareDisplayRows[pick.Row], true)
		if !ok || (role == "B" && physical != 37) || (role == "A" && physical != 0) {
			t.Fatalf("preview targets wrong physical trace: %s/%d", role, physical)
		}
	}
	for _, tc := range []struct {
		panel, x int
		active   bool
	}{{0, 61, true}, {1, 181, false}, {2, 301, false}, {0, 111, false}, {1, 231, true}, {2, 351, false}} {
		if got := prestackComparePickAt(&state, panels, tc.x, 150, true); got.Active != tc.active {
			t.Fatalf("missing-side preview panel %d x %d: %+v", tc.panel, tc.x, got)
		}
	}
}

func TestPrestackCompareCtrlPreviewRejectsInactiveStaleAndOldSizeFrames(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*prestackSession, *[3]RECT)
	}{
		{"page", func(s *prestackSession, _ *[3]RECT) { s.page = 0 }},
		{"rendering", func(s *prestackSession, _ *[3]RECT) { s.compareRendering = true }},
		{"resize active", func(s *prestackSession, _ *[3]RECT) { s.resizeActive = true }},
		{"resize pending", func(s *prestackSession, _ *[3]RECT) { s.resizePending = true }},
		{"no pixels", func(s *prestackSession, _ *[3]RECT) { s.compareA = nil }},
		{"invalid BGRA", func(s *prestackSession, _ *[3]RECT) { s.compareA = s.compareA[:100] }},
		{"old width", func(_ *prestackSession, p *[3]RECT) { p[0].Right++ }},
		{"old height", func(_ *prestackSession, p *[3]RECT) { p[0].Bottom++ }},
		{"no rows", func(s *prestackSession, _ *[3]RECT) { s.compareDisplayRows = nil }},
		{"box zoom", func(s *prestackSession, _ *[3]RECT) { s.dragging = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p := comparePickFixture()
			tc.change(&s, &p)
			if got := prestackComparePickAt(&s, p, 11, 150, true); got.Active {
				t.Fatalf("unsafe preview: %+v", got)
			}
		})
	}
	state, panels := comparePickFixture()
	for _, point := range [][2]int{{11, 110}, {11, 215}, {114, 150}, {0, 150}} {
		if prestackComparePickAt(&state, panels, point[0], point[1], true).Active {
			t.Fatalf("preview outside image: %v", point)
		}
	}
	if prestackComparePickAt(&state, panels, 11, 150, false).Active {
		t.Fatal("plain hover shows Ctrl marker")
	}
	state.compareBAxis.Compatible = false
	if prestackComparePickAt(&state, panels, 251, 150, true).Active {
		t.Fatal("disabled Delta preview")
	}
}

func TestPrestackCompareCtrlPreviewSnapMatchesRasterAndClickMapping(t *testing.T) {
	r := RECT{Left: 10, Top: 20, Right: 111, Bottom: 80}
	for _, count := range []int{1, 3, 60, 68432} {
		for _, width := range []int{3, 101} {
			for x := 10; x < 111; x++ {
				got := prestackComparePickRow(x, r, width, count)
				column := prestackCompareAxisIndex(x, 10, 111, 0, width-1)
				want := prestackCompareLogicalColumn(column, width, count)
				if got != want {
					t.Fatalf("pixel %d: preview %d, raster %d", x, got, want)
				}
				snap := prestackComparePickX(got, count, r)
				if snap < 10 || snap >= 111 {
					t.Fatalf("out-of-scene snapped marker %d", snap)
				}
			}
		}
	}
	if prestackComparePickX(0, 1, r) != 60 || prestackComparePickX(2, 3, r) != 110 {
		t.Fatal("single/last trace position incorrect")
	}
}

func TestPrestackCompareRenderInvalidationDropsCtrlPreview(t *testing.T) {
	oldState, oldWindow := prestackState, prestackHwnd
	defer func() { prestackState, prestackHwnd = oldState, oldWindow }()
	prestackHwnd = 0 // Exercise state cleanup without a native window.
	prestackState, _ = comparePickFixture()
	prestackState.comparePickPreview = prestackComparePickPreview{Active: true, Generation: 7}
	clearPrestackComparePickPreview() // Ctrl-up, mouse-leave and focus loss use this path.
	if prestackState.comparePickPreview.Active {
		t.Fatal("released Ctrl retained marker")
	}
	prestackState.comparePickPreview.Active = true
	invalidatePrestackCompareRender()
	if prestackState.comparePickPreview.Active {
		t.Fatal("new selection retained old marker")
	}
}
