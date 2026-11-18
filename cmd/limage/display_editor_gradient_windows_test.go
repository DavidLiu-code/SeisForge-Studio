//go:build windows

package main

import (
	"math"
	"slices"
	"strings"
	"testing"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
)

func displayGradientFixture(t *testing.T) (*displayEditor, *displaySurfaceState) {
	t.Helper()
	w, e := displayNativeEditorFixture(t, true)
	e.order = 1
	e.stats.Raw = displaycore.Distribution{
		Range: displaycore.Range{Min: -10000, Max: 10000}, Count: 3, Samples: []float64{-10000, 0, 10000},
	}
	displayLoadDraft(w)
	displayEditorNativeSelect(t, w, deSubTabs, displayPageGradient)
	if w.gradient.Right <= w.gradient.Left || w.nodeBand.Right <= w.nodeBand.Left {
		t.Fatal("missing independent native editor geometry")
	}
	return w, e
}

func TestDisplayGradientIndependentPercentScaleAndDefaultAdd(t *testing.T) {
	w, e := displayGradientFixture(t)
	before := slices.Clone(w.stops)
	if len(before) != 2 || w.gradientEdit.pending != .5 || !strings.Contains(getText(w.controls[deNodePending]), "50%") {
		t.Fatalf("two-stop default pending = %+v, stop count %d", w.gradientEdit, len(before))
	}
	m, base, surface, ok := displayEditorPreviewColor(w)
	if !ok {
		t.Fatal("missing mapping")
	}
	shown, _ := displayEditorColorHistogram(w, surface, m, base)
	if shown.Max/m.Range.Max < 1000 {
		t.Fatalf("expected distant display and mapping domains: %+v / %+v", shown, m.Range)
	}
	bar := w.gradient
	if displayGradientNodePixel(bar, shown, m.Range, false, 0) != int(bar.Left) ||
		displayGradientNodePixel(bar, shown, m.Range, false, 1) != int(bar.Right) {
		t.Fatal("LUT handles are compressed by the histogram domain")
	}
	displayEditorCommand(w, deAdd, 0)
	if len(w.stops) != 3 || w.selection != 1 || w.stops[1].Position != .5 ||
		e.saved != nil || len(e.s.Color.Stops) != 2 {
		t.Fatalf("Add did not stage/select the 50%% point only: %+v", w.stops)
	}
	original, expanded := displaycore.ColorMapping{Stops: before}, displaycore.ColorMapping{Stops: w.stops}
	for i := 0; i <= 255; i++ {
		p := float64(i) / 255
		a, b := original.ColorAtPosition(p), expanded.ColorAtPosition(p)
		if absInt(int(a.R)-int(b.R)) > 1 || absInt(int(a.G)-int(b.G)) > 1 || absInt(int(a.B)-int(b.B)) > 1 {
			t.Fatalf("inserting an interpolated point changed LUT substantially at %.6f: %+v / %+v", p, a, b)
		}
	}
	id := w.gradientEdit.selected
	displayEditorCommand(w, deAdd, 0)
	if len(w.stops) != 3 || w.gradientEdit.selected != id {
		t.Fatal("a selected duplicate was added instead of preserving its identity")
	}
}

func TestDisplayGradientPointerDoubleClickAndStableDrag(t *testing.T) {
	w, _ := displayGradientFixture(t)
	bar := w.gradient
	x := int(bar.Left) + int(bar.Right-bar.Left)/3
	y := int((bar.Top + bar.Bottom) / 2)
	lp := uintptr(uint32(uint16(x)) | uint32(uint16(y))<<16)
	displayEditorBodyProc(w.body, WM_LBUTTONDOWN, 0, lp)
	displayEditorBodyProc(w.body, WM_LBUTTONUP, 0, lp)
	if len(w.stops) != 2 || w.gradientEdit.pending != displayGradientFractionAt(bar, x) {
		t.Fatal("blank single click did not only stage the pending location")
	}
	displayEditorBodyProc(w.body, WM_LBUTTONDBLCLK, 0, lp)
	displayEditorBodyProc(w.body, WM_LBUTTONUP, 0, lp)
	if len(w.stops) != 3 || w.selection != 1 || w.stops[1].Position != w.gradientEdit.pending {
		t.Fatalf("blank double click did not add exactly one node: %+v", w.stops)
	}
	id := w.gradientEdit.selected
	setText(w.controls[deNodePosition], "25")
	displayEditorCommand(w, deNodePosition, 0x0300)
	setText(w.controls[deNodeColor], "#138CF1")
	displayEditorCommand(w, deNodeColor, 0x0300)
	edited := displaycore.GradientStop{Position: .25, Color: displaycore.RGB{R: 0x13, G: 0x8c, B: 0xf1}}
	if w.stops[1] != edited || w.gradientEdit.selected != id {
		t.Fatalf("typed position/color or stable selected ID lost: %+v", w.stops[1])
	}
	snapshot := slices.Clone(w.stops)
	for i, reverse := range []bool{true, false} {
		displayCheck(w.controls[deReverse], reverse)
		displayEditorCommand(w, deReverse, 0)
		if value := getText(w.controls[deNodePosition]); value != [...]string{"75", "25"}[i] ||
			!slices.Equal(snapshot, w.stops) || w.gradientEdit.selected != id {
			t.Fatalf("reverse %v changed storage, ID or explicit visual position: %s / %+v", reverse, value, w.stops)
		}
	}
	center := displayGradientNodePixel(bar, displaycore.Range{}, displaycore.Range{}, false, edited.Position)
	yy := int((w.nodeBand.Top + w.nodeBand.Bottom) / 2)
	if !displayEditorGradientClick(w, center, yy, false) || !w.dragging || w.gradientEdit.selected != id {
		t.Fatal("the visible selected node did not get capture")
	}
	displayEditorDrag(w, int(bar.Right)+400, yy+400, false)
	displayEditorFinishGesture(w, false, true)
	if p := w.stops[1].Position; math.Abs(p-(1-1./displayGradientPrecision)) > 1e-12 ||
		len(w.stops) != 3 || w.gradientEdit.selected != id {
		t.Fatalf("drag outside the strip did not clamp without deletion: %+v", w.stops)
	}
	if displayEditorGradientDeleteKey(w, w.controls[deNodeColor]) || len(w.stops) != 3 {
		t.Fatal("Delete was consumed while HEX text was focused")
	}
	if !displayEditorGradientDeleteKey(w, w.body) {
		t.Fatal("Delete was not handled when the gradient strip owned focus")
	}
	if len(w.stops) != 2 || slices.Contains(w.gradientEdit.ids, id) ||
		w.stops[0].Position != 0 || w.stops[1].Position != 1 {
		t.Fatal("deletion did not preserve both endpoints and retire only the selected ID")
	}
}

func TestDisplayGradientEndpointsChooserCancelLegacyLUTAndFollow(t *testing.T) {
	w, _ := displayGradientFixture(t)
	displayEditorSelectGradientNode(w, 0)
	displayEditorRefreshNode(w, true)
	if !strings.Contains(getText(w.controls[deNodeReadout]), "端点位置固定") {
		t.Fatal("disabled endpoint position has no explanation")
	}
	displayEditorCommand(w, deDelete, 0)
	before := w.stops[0]
	if edited := displayEditorChooseGradientColorWith(w, func(c displaycore.RGB) (displaycore.RGB, bool) {
		if c != before.Color {
			t.Fatal("chooser did not receive the exact draft color")
		}
		return displaycore.RGB{R: 49}, false
	}); edited || len(w.stops) != 2 || w.stops[0] != before {
		t.Fatal("cancel or endpoint deletion altered the gradient")
	}
	setText(w.controls[deNodeColor], "#880011")
	displayEditorCommand(w, deNodeColor, 0x0300)
	if w.stops[0].Position != 0 || w.stops[0].Color != (displaycore.RGB{R: 0x88, B: 0x11}) {
		t.Fatal("fixed endpoint did not allow color editing")
	}
	pSendMessageW.Call(w.controls[dePreset], CB_SETCURSEL, 7, 0)
	displayEditorCommand(w, dePreset, 1)
	lut := remainingLegacyPalette(2)
	if len(w.stops) != len(lut) {
		t.Fatalf("selected legacy LUT collapsed to %d anchors", len(w.stops))
	}
	displayEditorCommand(w, deAdd, 0)
	m := displaycore.ColorMapping{Stops: w.stops}
	for i := range lut {
		want, p := lut[255-i], float64(i)/255
		if got := m.ColorAtPosition(p); got != (displaycore.RGB{R: want.r, G: want.g, B: want.b}) {
			t.Fatalf("legacy palette changed after Add at index %d", i)
		}
	}

	follower, keys := displayNativeEditorPoststackProfiles(t, true)
	displayEditorNativeSelect(t, follower, deSubTabs, displayPageGradient)
	if !displayEditorColorRangeLocked(follower) || !strings.Contains(getText(follower.controls[deNodePending]), "跟随 A") {
		t.Fatal("B does not clearly report that its gradient is linked")
	}
	a0, _ := displayCommitted(follower.owner, keys[0])
	b0 := slices.Clone(follower.stops)
	displayEditorCommand(follower, deAdd, 0)
	displayEditorCommand(follower, deDelete, 0)
	a1, _ := displayCommitted(follower.owner, keys[0])
	if !slices.Equal(a0.Color.Stops, a1.Color.Stops) || !slices.Equal(b0, follower.stops) {
		t.Fatal("following panel changed either its own or the A palette")
	}
}

func TestDisplayGradientDraftSelectionAndPendingStayWithObject(t *testing.T) {
	w, keys := displayNativeEditorPoststackProfiles(t, true)
	draftTarget := func(key string) {
		displayStoreDraft(w)
		w.key, w.identity = key, displayOwners[w.owner].surfaces[key].s.Identity
		displayLoadDraft(w)
		w.section = displayPageGradient
		displayLayoutEditor(w)
	}
	draftTarget(keys[0])
	w.gradientEdit.pending = .37
	displayEditorCommand(w, deAdd, 0)
	id := w.gradientEdit.selected
	if id == 0 || w.stops[w.selection].Position != .37 {
		t.Fatal("could not stage a selected gradient point in A")
	}
	draftTarget(keys[1])
	if w.gradientEdit.selected != 0 || w.gradientEdit.pending != .5 {
		t.Fatal("B picked up A's selected/pending gradient state")
	}
	draftTarget(keys[0])
	if len(w.stops) != 3 || w.gradientEdit.selected != id || w.gradientEdit.pending != .37 {
		t.Fatalf("A's gradient state was not restored: %+v", w.gradientEdit)
	}
}
