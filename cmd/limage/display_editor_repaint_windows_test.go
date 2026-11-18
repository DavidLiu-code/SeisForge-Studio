//go:build windows

package main

import (
	"math"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unsafe"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
)

func TestDisplayEditorPreviewInvalidatesMovedHandleAndClipsViewport(t *testing.T) {
	graph := RECT{Left: 18, Top: -25, Right: 380, Bottom: 100}
	gradient := RECT{Left: 18, Top: 100, Right: 380, Bottom: 123}
	client := RECT{Right: 381, Bottom: 180}
	if got, want := displayPreviewDirty(graph, gradient, client, 144), (RECT{Right: 381, Bottom: 174}); got != want {
		t.Fatalf("dirty area = %+v, want %+v; should erase moved handles and labels and clip at the body", got, want)
	}
	f := (displayEditorPlacement{visible: true}).flags()
	if want := uintptr(displayPositionNoRedraw | displayPositionNoCopy | displayPositionShow); f&want != want {
		t.Fatalf("visible controls copy or repaint stale pixels during scrolling: %#x", f)
	}
}

func displayNativeEditorFixture(t *testing.T, color bool) (*displayEditor, *displaySurfaceState) {
	t.Helper()
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)
	oldAsync := displayAsyncWindow
	displayEnsureWindows()
	t.Cleanup(func() {
		if oldAsync == 0 && displayAsyncWindow != 0 {
			pDestroyWindow.Call(displayAsyncWindow)
			displayAsyncWindow = 0
		}
	})
	hinst, _, _ := pGetModuleHandleW.Call(0)
	create := func() uintptr {
		h, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(u16("SeisForgeDisplaySettings"))), 0,
			WS_OVERLAPPEDWINDOW|WS_CLIPCHILDREN, 0, 0, 800, 650, 0, 0, hinst, 0)
		return h
	}
	owner := create()
	if owner == 0 {
		t.Fatal("could not create hidden owner")
	}
	t.Cleanup(func() { pDestroyWindow.Call(owner) })
	h := create()
	if h == 0 {
		t.Fatal("could not create hidden display editor")
	}
	surface := &displaySurfaceState{s: displaySurface{Identity: "fixture", Title: "Fixture",
		Plot:  RECT{Left: 100, Top: 100, Right: 900, Bottom: 700},
		Color: &displaycore.ColorMapping{Range: displaycore.Range{Min: -1, Max: 1}, Stops: displayPreset(0)},
		Axes: []displaycore.Axis{
			{ID: "x", Title: "Crossline", Data: displaycore.Range{Min: 0, Max: 1000}, View: displaycore.Range{Min: 200, Max: 800}},
			{ID: "y", Title: "Time", Unit: "ms", BaseUnit: "ms", Data: displaycore.Range{Min: 0, Max: 1000}, View: displaycore.Range{Min: 200, Max: 800}, Down: true},
		}}}
	state := &displayOwnerState{surfaces: map[string]*displaySurfaceState{"test": surface}}
	displayOwners[owner] = state
	t.Cleanup(func() { delete(displayOwners, owner) })
	w := &displayEditor{handle: h, owner: owner, key: "test", identity: "fixture", axis: "x", pageColor: color,
		controls: make(map[int]uintptr), labels: make(map[int]uintptr), drafts: make(map[string]displayDraft)}
	displayEditors[h] = w
	state.editor = w
	t.Cleanup(func() { pDestroyWindow.Call(h) })
	displayMakeEditor(w)
	displayLoadDraft(w)
	return w, surface
}

func displayNativeRect(h uintptr) (r RECT) {
	algorithmGetWindowRect.Call(h, uintptr(unsafe.Pointer(&r)))
	return
}

func TestDisplayEditorNativePagesHaveNoScrollOrHiddenFields(t *testing.T) {
	w, surface := displayNativeEditorFixture(t, true)
	surface.s.Compare = &displayCompareBinding{Keys: [3]string{"test", "b", "delta"}, CanLink: true}
	surface.s.equalTicks, surface.s.distance = true, true
	for _, dpi := range []int{96, 120, 144} {
		for _, size := range [][2]int{{800, 650}, {580, 570}} {
			pSetWindowPos.Call(w.handle, 0, 0, 0, uintptr(size[0]*dpi/96), uintptr(size[1]*dpi/96), 0x16)
			for _, color := range []bool{false, true} {
				w.pageColor = color
				displayLoadDraft(w)
				for section := 0; section < 5; section++ {
					if !color && section == 1 || color && section == 4 {
						continue
					}
					w.section = section
					displayLayoutEditorAtDPI(w, dpi)
					style, _, _ := algorithmGetWindowStyle.Call(w.body, ^uintptr(15))
					if style&(0x00200000|0x00100000) != 0 || w.scroll != 0 || w.scrollMax != 0 {
						t.Fatalf("settings body is scrollable at DPI %d", dpi)
					}
					body := displayNativeRect(w.body)
					for _, controls := range []map[int]uintptr{w.controls, w.labels} {
						for id, h := range controls {
							if id == deColor || id == deAxis || id == deTopTabs || id >= deReset && id <= deApplyPanels {
								continue
							}
							style, _, _ := algorithmGetWindowStyle.Call(h, ^uintptr(15))
							if style&WS_VISIBLE == 0 {
								continue
							}
							r := displayNativeRect(h)
							if r.Left < body.Left || r.Right > body.Right || r.Top < body.Top || r.Bottom > body.Bottom {
								t.Fatalf("DPI %d size %v color=%v section=%d field=%d bounds=%+v outside %+v", dpi, size, color, section, id, r, body)
							}
						}
					}
					if r := displayNativeRect(w.controls[deApply]); r.Top < body.Bottom {
						t.Fatalf("footer overlaps body at DPI %d: %+v / %+v", dpi, r, body)
					}
				}
			}
		}
	}
}

func TestDisplayEditorNoScrollPreservesFooterAndDraft(t *testing.T) {
	w, _ := displayNativeEditorFixture(t, true)
	setText(w.controls[deMin], "-0.75")
	footer, before := displayNativeRect(w.controls[deApply]), displayNativeRect(w.controls[deMin])
	pSendMessageW.Call(w.body, 0x0115, 7, 0) // WM_VSCROLL / SB_BOTTOM
	pSendMessageW.Call(w.body, 0x020a, uintptr(120<<16), 0)
	after := displayNativeRect(w.controls[deMin])
	if w.scroll != 0 || before != after || displayNativeRect(w.controls[deApply]) != footer {
		t.Fatal("obsolete scroll messages moved a field/footer")
	}
	if got := getText(w.controls[deMin]); got != "-0.75" {
		t.Fatalf("scrolling changed the unapplied edit: %q", got)
	}
}

func TestDisplayEditorActualTickStepFormatsAndUnits(t *testing.T) {
	w, _ := displayNativeEditorFixture(t, false)
	w.axis = "y"
	displayLoadDraft(w)
	step, err := strconv.ParseFloat(getText(w.controls[deStep]), 64)
	if err != nil || step <= 0 || !strings.Contains(getText(w.controls[deTickCurrent]), "自动（当前：") {
		t.Fatalf("auto step is a placeholder: %q / %q", getText(w.controls[deStep]), getText(w.controls[deTickCurrent]))
	}
	plan, _, err := displayEditorDraftTicks(w)
	if err != nil || plan.Step != step {
		t.Fatalf("editor/renderer auto step mismatch: %v / %g (%v)", plan, step, err)
	}
	displayCheck(w.controls[deTickAuto], false)
	displayEditorCommand(w, deTickAuto, 0)
	setText(w.controls[deUnit], "s")
	displayEditorCommand(w, deUnit, 0x0200) // EN_KILLFOCUS
	a, err := displayEditorDraftAxis(w)
	if err != nil || math.Abs(a.Step-step) > 1e-10 || a.View.Min != 200 || a.View.Max != 800 {
		t.Fatalf("unit conversion moved physical range/step: %+v, %v", a, err)
	}
	if got, _ := strconv.ParseFloat(getText(w.controls[deStep]), 64); math.Abs(got-step/1000) > 1e-12 {
		t.Fatalf("ms->s manual interval=%g want %g", got, step/1000)
	}
	for i, f := range []displaycore.NumberFormat{displaycore.AutoFormat, displaycore.IntegerFormat, displaycore.FixedFormat, displaycore.ScientificFormat} {
		if displayEditorFormat(i) != f || displayEditorFormatIndex(f) != i {
			t.Fatalf("format %d migration mismatch", i)
		}
	}
	setText(w.controls[deTickLength], "0")
	a, err = displayEditorDraftAxis(w)
	if err != nil || a.MajorTickDIP() != 0 || !a.TickLengthSet {
		t.Fatalf("explicit zero tick length lost: %+v %v", a, err)
	}
}

func TestDisplayEditorRangeDragIsDraftAndNavigationKeepsDirtyRange(t *testing.T) {
	w, surface := displayNativeEditorFixture(t, false)
	b := w.rangeBar
	x := int(b.Left) + int(float64(b.Right-b.Left)*.5)
	y := int((b.Top + b.Bottom) / 2)
	if !displayEditorDragRange(w, x, y, true) || !displayEditorDragRange(w, x+int(b.Right-b.Left)/10, y, false) {
		t.Fatal("range whole drag did not start")
	}
	displayEditorBodyProc(w.body, WM_LBUTTONUP, 0, 0)
	_, draft, ok := displayEditorRange(w)
	if !ok || math.Abs((draft.Max-draft.Min)-600) > 1e-9 || draft.Min <= 200 {
		t.Fatalf("drag changed span: %+v", draft)
	}
	if surface.saved != nil || surface.s.Axes[0].View.Min != 200 {
		t.Fatal("drag applied draft to owner")
	}
	dirty := [2]string{getText(w.controls[deMin]), getText(w.controls[deMax])}
	surface.s.Axes[0].View = displaycore.Range{Min: 100, Max: 700}
	displayEditorRefreshViewport(w)
	if got := [2]string{getText(w.controls[deMin]), getText(w.controls[deMax])}; got != dirty || !w.viewportDifferent {
		t.Fatalf("navigation replaced dirty draft: %v -> %v", dirty, got)
	}
	displayEditorShowBounds(w, displaycore.Range{Min: 200, Max: 800}, false)
	displayEditorRefreshViewport(w)
	if got := getText(w.controls[deMin]); got != "100" || w.viewportDifferent {
		t.Fatalf("clean draft did not follow navigation: %q", got)
	}
	w.viewportRefreshPending = true
	displayEditorProc(w.handle, displayRefreshEditorLiveMessage, 0, 0)
	if w.viewportRefreshPending {
		t.Fatal("live refresh message was not consumed")
	}
}

func TestDisplayEditorAmplitudeRangeHandlesAndWholeDrag(t *testing.T) {
	w, surface := displayNativeEditorFixture(t, true)
	surface.order = 1
	surface.s.Color.Range = displaycore.Range{Min: -2, Max: 6}
	for _, sample := range []float64{-10, 0, 20} {
		surface.stats.Add(sample)
	}
	displayLoadDraft(w)
	b := w.gradient
	y := int((b.Top + b.Bottom) / 2)
	width := int(b.Right - b.Left)
	px := func(v float64) int { return int(b.Left) + int(math.Round((v+10)*float64(width)/30)) }
	read := func() displaycore.Range {
		t.Helper()
		s, _ := displayCommitted(w.owner, w.key)
		m, err := displayDraftMapping(w, *s.Color)
		if err != nil {
			t.Fatal(err)
		}
		return m.Range
	}
	drag := func(x, dx int, mode displaycore.RangeDragMode) {
		t.Helper()
		if !displayEditorDragRange(w, x, y, true) || w.rangeDragMode != mode || !displayEditorDragRange(w, x+dx, y, false) {
			t.Fatalf("amplitude handle did not start: mode %v, actual %v", mode, w.rangeDragMode)
		}
		displayEditorBodyProc(w.body, WM_LBUTTONUP, 0, 0)
	}
	drag(px(-2), width/10, displaycore.DragMinimum)
	a := read()
	if a.Min < 0 || a.Max != 6 {
		t.Fatalf("min handle modified the wrong controls: %+v", a)
	}
	drag(px(6), width/8, displaycore.DragMaximum)
	bounds := read()
	if bounds.Max <= 6 || bounds.Min != a.Min {
		t.Fatalf("max handle modified the wrong controls: %+v", bounds)
	}
	drag(px((bounds.Min+bounds.Max)/2), width/8, displaycore.DragWhole)
	c := read()
	if c.Min <= bounds.Min || math.Abs((c.Max-c.Min)-(bounds.Max-bounds.Min)) > 1e-9 {
		t.Fatalf("whole-range pan did not preserve span: %+v -> %+v", bounds, c)
	}
	if surface.saved != nil || surface.s.Color.Range != (displaycore.Range{Min: -2, Max: 6}) || displayEditorDrag(w, px(5), y, true) {
		t.Fatal("amplitude drag touched the owner or used the gradient point editor")
	}
}

func TestDisplayEditorAmplitudeAutoAndSymmetricGestures(t *testing.T) {
	w, surface := displayNativeEditorFixture(t, true)
	surface.order = 1
	surface.s.Color.Auto, surface.s.Color.Symmetric = true, true
	surface.stats.Add(-4)
	surface.stats.Add(7)
	// Model the already committed symmetric auto range; opening the editor
	// must not infer new applied endpoints from the statistics on its own.
	surface.s.Color.Range = displaycore.Range{Min: -7, Max: 7}
	displayLoadDraft(w)
	b := w.gradient
	y, right := int((b.Top+b.Bottom)/2), int(b.Right)
	// Disallow panning away from zero; endpoints mirror instead.
	if displayEditorDragRange(w, int((b.Left+b.Right)/2), y, true) {
		t.Fatal("zero-symmetric interval could pan away from zero")
	}
	if !displayEditorDragRange(w, right, y, true) || !displayEditorDragRange(w, right, y, false) || !displayChecked(w.controls[deAuto]) {
		t.Fatal("click alone changed the auto range")
	}
	if !displayEditorDragRange(w, right-int(b.Right-b.Left)/4, y, false) || displayChecked(w.controls[deAuto]) {
		t.Fatal("actual movement failed to enter manual range")
	}
	displayEditorBodyProc(w.body, WM_LBUTTONUP, 0, 0)
	lo, errLo := strconv.ParseFloat(getText(w.controls[deMin]), 64)
	hi, errHi := strconv.ParseFloat(getText(w.controls[deMax]), 64)
	if errLo != nil || errHi != nil || lo != -hi || hi <= 0 || hi >= 7 || surface.saved != nil {
		t.Fatalf("symmetric/auto draft state incorrect: %g..%g (%v, %v)", lo, hi, errLo, errHi)
	}
}

func TestDisplayEditorVisibilityAndRangeIgnoreUnchangedDenseTicks(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	oldPrefs, oldLoaded := displayPrefs, displayPrefsLoaded
	displayPrefsLoaded = false
	t.Cleanup(func() { displayPrefs, displayPrefsLoaded = oldPrefs, oldLoaded })
	w, surface := displayNativeEditorFixture(t, true)
	surface.order = 1
	surface.s.Color.Step = 0.00001
	if !displaySetColorbarVisible(w.owner, w.key, w.identity, true) {
		t.Fatal("fixture could not enable colorbar")
	}
	displayLoadDraft(w)
	plan, _, err := displayEditorDraftTicks(w)
	if err != nil || plan.Warning == "" {
		t.Fatalf("test requires a previously valid interval with density warning: %+v, %v", plan, err)
	}
	displayCheck(w.controls[deHide], false)
	setText(w.controls[deMin], "-0.8")
	displayApplyEditor(w, false)
	got, _ := displayCommitted(w.owner, w.key)
	if got.Color == nil || !got.Color.Hidden || got.Color.Range.Min != -0.8 || got.Color.Step != 0.00001 {
		t.Fatalf("dense legacy interval prevented hide/range apply: %+v, status %q", got.Color, getText(w.status))
	}
	displayCheck(w.controls[deHide], true)
	displayApplyEditor(w, false)
	got, _ = displayCommitted(w.owner, w.key)
	if got.Color == nil || got.Color.Hidden || got.Color.Step != 0.00001 {
		t.Fatalf("dense legacy interval prevented show: %+v", got.Color)
	}
}

func TestDisplayEditorDraftUnitAndPerAxisTickValuesSurvivePages(t *testing.T) {
	w, _ := displayNativeEditorFixture(t, false)
	w.axis = "y"
	displayLoadDraft(w)
	setText(w.controls[deUnit], "s")
	displayEditorCommand(w, deUnit, 0x0200)
	setText(w.controls[deMin], "0.3")
	setText(w.controls[deTickLength], "11")
	displayStoreDraft(w)
	w.axis = "x"
	displayLoadDraft(w)
	if getText(w.controls[deTickLength]) == "11" {
		t.Fatal("Y tick length leaked to X")
	}
	w.axis = "y"
	displayLoadDraft(w)
	_, r, ok := displayEditorRange(w)
	if !ok || math.Abs(r.Min-300) > 1e-9 || r.Max != 800 || getText(w.controls[deTickLength]) != "11" {
		t.Fatalf("axis reopen lost canonical range or draft ticks: %+v / %+v", w.unit, r)
	}
	setText(w.controls[deMin], "-0.1")
	if _, err := displayEditorDraftAxis(w); err == nil {
		t.Fatal("precise input bypassed range-bar data boundary validation")
	}
}

func TestDisplayEditorLinkedDeltaKeepsIndependentTickPresentation(t *testing.T) {
	w, surface := displayNativeEditorFixture(t, true)
	owner := displayOwners[w.owner]
	keys := [3]string{"a", "b", "test"}
	surface.s.Compare = &displayCompareBinding{Keys: keys, CanLink: true}
	a := surface.s
	a.Color = &displaycore.ColorMapping{Range: displaycore.Range{Min: -5, Max: 5}, Stops: displayPreset(0)}
	owner.surfaces["a"] = &displaySurfaceState{s: a}
	owner.links = map[[3]string]displayCompareBinding{keys: {Linked: true, FollowA: true}}
	displayLoadDraft(w)
	displayCheck(w.controls[deTickAuto], false)
	setText(w.controls[deStep], "2")
	setText(w.controls[deTickLength], "0")
	setText(w.controls[deDigits], "3")
	pSendMessageW.Call(w.controls[deFormat], CB_SETCURSEL, 3, 0)
	m, err := displayDraftMapping(w, *surface.s.Color)
	if err != nil || m.Range.Min != -5 || m.Step != 2 || m.MajorTickDIP() != 0 || m.Format != displaycore.ScientificFormat || m.Digits != 3 {
		t.Fatalf("linked delta discarded its tick presentation or changed amplitude link: %+v, %v", m, err)
	}
}

func displayNativeEditorPoststackProfiles(t *testing.T, color bool) (*displayEditor, [3]string) {
	t.Helper()
	t.Setenv("LOCALAPPDATA", t.TempDir())
	oldPrefs, oldLoaded := displayPrefs, displayPrefsLoaded
	displayPrefsLoaded = false
	t.Cleanup(func() { displayPrefs, displayPrefsLoaded = oldPrefs, oldLoaded })
	w, fixture := displayNativeEditorFixture(t, color)
	o := displayOwners[w.owner]
	keys := [3]string{"test", "b", "delta"}
	binding := &displayCompareBinding{Keys: keys, CanLink: true, Linked: true, PoststackProfiles: true}
	for i, key := range keys {
		s := fixture.s
		s.Identity, s.Compare, s.LiveViewport = key, binding, true
		s.Axes = append([]displaycore.Axis(nil), s.Axes...)
		s.Axes[0].Title = []string{"A crossline", "B crossline", "Difference crossline"}[i]
		m := s.Color.Clone()
		m.Range = displaycore.Range{Min: -float64(5 + i), Max: float64(5 + i)}
		m.Title = []string{"A amplitude", "B amplitude", "Difference amplitude"}[i]
		m.TickLength, m.TickLengthSet = float64(5+i), true
		s.Color = &m
		o.surfaces[key] = &displaySurfaceState{s: s, order: uint64(i + 1)}
	}
	o.links = map[[3]string]displayCompareBinding{keys: *binding}
	w.key, w.identity = keys[1], keys[1]
	displayLoadDraft(w)
	return w, keys
}

func TestDisplayEditorPoststackEachHiddenLegendIsPickableAndIndependent(t *testing.T) {
	w, keys := displayNativeEditorPoststackProfiles(t, true)
	if len(w.compareTargets) != 3 || displaySelection(w.controls[deComparePanel]) != 1 || !displayChecked(w.controls[deCompareMatchA]) {
		t.Fatalf("B color controls not initialized: targets %v, selected %d", w.compareTargets, displaySelection(w.controls[deComparePanel]))
	}
	if !displayEditorColorRangeLocked(w) {
		t.Fatal("B offered competing amplitude range controls while following A")
	}
	if getText(w.controls[deTitle]) != "B amplitude" || getText(w.controls[deTickLength]) != "6" || getText(w.controls[deMin]) != "-5" {
		t.Fatal("linked B did not preserve its local annotations and use A's range")
	}
	displayCheck(w.controls[deHide], true)
	displayApplyEditor(w, false)
	if !displayColorbarVisible(w.owner, keys[1]) || displayColorbarVisible(w.owner, keys[0]) || displayColorbarVisible(w.owner, keys[2]) {
		t.Fatal("B legend did not display independently while its palette followed A")
	}
	displayCheck(w.controls[deCompareMatchA], false)
	displayEditorCommand(w, deCompareMatchA, 0)
	if getText(w.controls[deTitle]) != "B amplitude" || getText(w.controls[deTickLength]) != "6" || getText(w.controls[deMin]) != "-5" {
		t.Fatal("detaching B did not keep its annotations and effective mapping")
	}
	setText(w.controls[deMin], "-18")
	displayApplyEditor(w, false)
	a, _ := displayCommitted(w.owner, keys[0])
	b, _ := displayCommitted(w.owner, keys[1])
	d, _ := displayCommitted(w.owner, keys[2])
	if a.Color.Range.Min != -5 || b.Color.Range.Min != -18 || d.Color.Range.Min != -7 {
		t.Fatalf("independent B edited another panel: %+v %+v %+v", a.Color.Range, b.Color.Range, d.Color.Range)
	}
	w.key, w.identity = keys[2], keys[2]
	displayLoadDraft(w)
	displayCheck(w.controls[deHide], true)
	displayApplyEditor(w, false)
	if !displayColorbarVisible(w.owner, keys[1]) || !displayColorbarVisible(w.owner, keys[2]) || displayColorbarVisible(w.owner, keys[0]) {
		t.Fatal("Δ could not be displayed without changing the visibility of A or B")
	}
}

func TestDisplayEditorPoststackAxisSelectorAndFollowADraft(t *testing.T) {
	w, keys := displayNativeEditorPoststackProfiles(t, false)
	if len(w.compareTargets) != 3 || displaySelection(w.controls[deComparePanel]) != 1 || displayChecked(w.controls[deCompareMatchA]) {
		t.Fatalf("B axes picker not initialized: targets %v, selected %d", w.compareTargets, displaySelection(w.controls[deComparePanel]))
	}
	displayCheck(w.controls[deCompareMatchA], true)
	displayEditorCommand(w, deCompareMatchA, 0)
	pSendMessageW.Call(w.controls[deAxisSelect], CB_SETCURSEL, 1, 0)
	displayEditorCommand(w, deAxisSelect, 1)
	if !displayChecked(w.controls[deCompareMatchA]) || w.axis != "y" {
		t.Fatal("the B axes draft was reset on changing axes")
	}
	if _, committed := displayBAxisLink(w.owner, keys[1]); committed {
		t.Fatal("B axes link applied before pressing Apply")
	}
	displayApplyEditor(w, false)
	e := displayEditorSurface(w)
	if _, committed := displayBAxisLink(w.owner, keys[1]); !committed || displayAxisFor(w.owner, keys[1], e.s.Axes[0]).Title != "A crossline" {
		t.Fatal("B axes checkbox did not commit A style")
	}
	if a, d := displayCompareOptions(w.owner, keys[1]); !a || d {
		t.Fatal("axis checkbox changed color-scale settings")
	}
	displayCheck(w.controls[deCompareMatchA], false)
	displayEditorCommand(w, deCompareMatchA, 0)
	displayApplyEditor(w, false)
	if _, committed := displayBAxisLink(w.owner, keys[1]); committed || displayAxisFor(w.owner, keys[1], e.s.Axes[0]).Title != "B crossline" {
		t.Fatal("B axes independence did not restore its own style")
	}
}

func TestDisplayEditorEqualDivisionsTypographyAndStagedCopy(t *testing.T) {
	w, keys := displayNativeEditorPoststackProfiles(t, false)
	e := displayEditorSurface(w)
	e.s.equalTicks = true
	e.s.distance = true
	for _, k := range keys {
		displayOwners[w.owner].surfaces[k].s.equalTicks = true
	}
	displayLoadDraft(w)
	displayEditorCommand(w, deTickPage, 0)
	if got := getText(w.controls[deTickDivisions]); got != "5" {
		t.Fatalf("initial divisions = %q", got)
	}
	if actual := getText(w.controls[deTickCurrent]); !strings.Contains(actual, "实际间隔：120") {
		t.Fatalf("actual equal interval = %q", actual)
	}
	if getText(w.controls[deTickFont]) != "10" || getText(w.controls[deTitleFont]) != "11" || getText(w.controls[dePlotTitleFont]) != "11" {
		t.Fatal("missing effective font defaults")
	}
	for _, id := range []int{deStep, deTickAuto} {
		style, _, _ := algorithmGetWindowStyle.Call(w.controls[id], ^uintptr(15))
		if style&WS_VISIBLE != 0 {
			t.Fatalf("legacy interval control %d visible with equal divisions", id)
		}
	}
	pSendMessageW.Call(w.controls[deTickDivisions], CB_SETCURSEL, 3, 0)
	displayEditorCommand(w, deTickDivisions, 1)
	if !strings.Contains(getText(w.controls[deTickCurrent]), "实际间隔：75") {
		t.Fatal("preset did not update effective interval")
	}
	setText(w.controls[deTickDivisions], "7")
	setText(w.controls[deTickLength], "0")
	setText(w.controls[deTickFont], "13")
	setText(w.controls[deTitleFont], "16")
	setText(w.controls[dePlotTitleFont], "15")
	setText(w.controls[deTitle], "staged title")
	setText(w.controls[deMin], "invalid staged bounds")
	if err := displayEditorStageCopy(w, keys, [3]bool{true, false, true}, displayCopyType); err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		if s, _ := displayCommitted(w.owner, key); s.Axes[0].Divisions != 0 || s.PlotTitleFontPT != 0 {
			t.Fatal("copy applied an object before footer Apply")
		}
	}
	if getText(w.controls[deMin]) != "invalid staged bounds" {
		t.Fatal("copy overwrote the current invalid draft")
	}
	for _, key := range []string{keys[0], keys[2]} {
		found := false
		for _, d := range w.drafts {
			if d.key != key || d.color || d.axis != "x" {
				continue
			}
			found = true
			if d.text[deTickDivisions] != "7" || d.text[dePlotTitleFont] != "15" || d.text[deTickLength] != "0" || d.text[deTitle] == "staged title" || d.text[deMin] != "200" || d.bounds.exact != (displaycore.Range{Min: 200, Max: 800}) {
				t.Fatalf("staged style incorrectly changed target %s's annotations or exact viewport: %+v", key, d)
			}
		}
		if !found {
			t.Fatalf("copy did not stage selected target %s", key)
		}
	}
	setText(w.controls[deMin], "200")
	setText(w.controls[deTitle], "B crossline")
	displayApplyEditor(w, false)
	a, _ := displayCommitted(w.owner, keys[0])
	b, _ := displayCommitted(w.owner, keys[1])
	d, _ := displayCommitted(w.owner, keys[2])
	for i, s := range []displaycore.Settings{a, b, d} {
		x := s.Axes[0]
		wantTitle := []string{"A crossline", "B crossline", "Difference crossline"}[i]
		if x.Divisions != 7 || x.TickFontPT != 13 || x.TitleFontPT != 16 || x.MajorTickDIP() != 0 || s.PlotTitleFontPT != 15 || x.Title != wantTitle || x.View != (displaycore.Range{Min: 200, Max: 800}) {
			t.Fatalf("batch Apply did not preserve target %d's exact viewport/style (status %q): %+v / %+v", i, getText(w.status), x, s.PlotTitleFontPT)
		}
	}
}

func TestDisplayEditorDistanceIsStagedAndOnlyExposedForX(t *testing.T) {
	w, e := displayNativeEditorFixture(t, false)
	e.s.distance = true
	displayLoadDraft(w)
	displayEditorCommand(w, deDistancePage, 0)
	if w.section != 4 || !displayEditorHasDistance(w, e) {
		t.Fatal("eligible horizontal axis did not expose a distance page")
	}
	rawMin := getText(w.controls[deMin])
	pSendMessageW.Call(w.controls[deDistanceMode], CB_SETCURSEL, 1, 0)
	displayEditorCommand(w, deDistanceMode, 1)
	pSendMessageW.Call(w.controls[deDistanceCalibration], CB_SETCURSEL, 1, 0)
	displayEditorCommand(w, deDistanceCalibration, 1)
	setText(w.controls[deDistanceSpacing], "-10")
	displayEditorCommand(w, deDistanceSpacing, 0x0300)
	if !strings.Contains(getText(w.controls[deDistanceActual]), "有限正数") || getText(w.controls[deMin]) != rawMin {
		t.Fatal("invalid staged calibration changed current coordinates or was not diagnosed")
	}
	displayApplyEditor(w, false)
	if !strings.Contains(getText(w.status), "有限正数") || displayDistanceSettingsFor(w.owner).Enabled {
		t.Fatal("invalid staged calibration was applied")
	}
	setText(w.controls[deDistanceSpacing], "27.5")
	displayEditorCommand(w, deDistanceSpacing, 0x0300)
	pSendMessageW.Call(w.controls[deAxisSelect], CB_SETCURSEL, 1, 0)
	displayEditorCommand(w, deAxisSelect, 1)
	if w.section == 4 || displayEditorHasDistance(w, e) {
		t.Fatal("vertical axis incorrectly offers distance calibration")
	}
	pSendMessageW.Call(w.controls[deAxisSelect], CB_SETCURSEL, 0, 0)
	displayEditorCommand(w, deAxisSelect, 1)
	set, err := displayEditorReadDistance(w)
	if err != nil || !set.Enabled || !set.Manual || set.SpacingM != 27.5 || displayDistanceSettingsFor(w.owner).Enabled {
		t.Fatalf("switching axes lost or applied a distance draft: %+v, %v", set, err)
	}
}
