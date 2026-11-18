//go:build windows

package main

import (
	"testing"

	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
)

func TestPrestackCompareNestedZoomUsesVisibleLogicalRange(t *testing.T) {
	a, b := prestackCompareZoomRange(125, 175, 100, 201, 0, 59)
	if a != 15 || b != 44 {
		t.Fatalf("first zoom = %d..%d", a, b)
	}
	a, b = prestackCompareZoomRange(125, 175, 100, 201, a, b)
	if a != 22 || b != 37 {
		t.Fatalf("nested zoom reset to full range: %d..%d", a, b)
	}
	a, b = prestackCompareZoomRange(200, 100, 100, 201, a, b)
	if a != 22 || b != 37 {
		t.Fatalf("reverse full raster zoom = %d..%d", a, b)
	}
}

func TestPrestackCompareRasterSamplesExcludeTitleAndIncludeLastPixel(t *testing.T) {
	r := prestackCompareRasterRect(RECT{Left: 20, Top: 100, Right: 222, Bottom: 236})
	if r.Left != 21 || r.Right != 221 || r.Top != 134 || r.Bottom != 235 {
		t.Fatalf("wrong raster %+v", r)
	}
	if got := prestackCompareAxisIndex(134, int(r.Top), int(r.Bottom), 100, 300); got != 100 {
		t.Fatalf("first sample = %d", got)
	}
	if got := prestackCompareAxisIndex(234, int(r.Top), int(r.Bottom), 100, 300); got != 300 {
		t.Fatalf("last sample = %d", got)
	}
	a, b := prestackCompareZoomRange(159, 209, int(r.Top), int(r.Bottom), 100, 300)
	if a != 150 || b != 250 {
		t.Fatalf("sample zoom = %d..%d", a, b)
	}
	a, b = prestackCompareZoomRange(159, 209, int(r.Top), int(r.Bottom), a, b)
	if a != 175 || b != 225 {
		t.Fatalf("nested sample zoom = %d..%d", a, b)
	}
}

func TestPrestackComparePanUsesVisibleSpan(t *testing.T) {
	a, b := prestackComparePanRange(150, 125, 101, 20, 40, 60)
	if a != 25 || b != 45 {
		t.Fatalf("pan = %d..%d", a, b)
	}
	a, b = prestackComparePanRange(100, 200, 101, 2, 22, 60)
	if a != 0 || b != 20 {
		t.Fatalf("left clamp = %d..%d", a, b)
	}
	a, b = prestackComparePanRange(200, 100, 101, 30, 50, 60)
	if a != 39 || b != 59 {
		t.Fatalf("right clamp = %d..%d", a, b)
	}
}

func TestPrestackComparePhysicalMappingNeverFallsBackAcrossEmptySides(t *testing.T) {
	row := prestackcore.CompareLogicalRow{ATrace: 0, BTrace: 37, HasA: true, HasB: true, Classification: prestackcore.CompareRowMatched}
	for _, tc := range []struct {
		panel int
		trace int64
		role  string
	}{{0, 0, "A"}, {1, 37, "B"}, {2, 0, "A"}} {
		trace, role, ok := prestackCompareTraceForPanel(tc.panel, row, true)
		if !ok || trace != tc.trace || role != tc.role {
			t.Fatalf("panel %d: %d %s %v", tc.panel, trace, role, ok)
		}
	}
	if _, _, ok := prestackCompareTraceForPanel(2, row, false); ok {
		t.Fatal("incompatible Delta is pickable")
	}
	row.HasB = false
	row.Classification = prestackcore.CompareRowAOnly
	for _, panel := range []int{1, 2} {
		if _, _, ok := prestackCompareTraceForPanel(panel, row, true); ok {
			t.Fatalf("empty panel %d fell back to A", panel)
		}
	}
	row.HasA, row.HasB = false, true
	row.Classification = prestackcore.CompareRowBOnly
	if _, _, ok := prestackCompareTraceForPanel(0, row, true); ok {
		t.Fatal("empty A panel fell back to B")
	}
}

func TestPrestackTextInputKeepsScientificExponentKeys(t *testing.T) {
	for _, class := range []string{"Edit", "EDIT", "RichEdit20W", "RICHEDIT50W"} {
		if !prestackTextInputClass(class) {
			t.Errorf("text input %q would lose shortcut character", class)
		}
	}
	for _, class := range []string{"Button", "ComboBox", "SeisForge", ""} {
		if prestackTextInputClass(class) {
			t.Errorf("non-text input %q lost workspace shortcuts", class)
		}
	}
}

func TestPrestackDisplayChangeRunsOnlyVisibleRenderer(t *testing.T) {
	for _, page := range []int{0, 3} {
		gather, compare := 0, 0
		renderPrestackDisplayChange(page, func() { gather++ }, func() { compare++ })
		if page == 3 && (gather != 0 || compare != 1) {
			t.Fatalf("Compare reads Gather too: %d/%d", gather, compare)
		}
		if page == 0 && (gather != 1 || compare != 0) {
			t.Fatalf("Gather render routing changed: %d/%d", gather, compare)
		}
	}
}
