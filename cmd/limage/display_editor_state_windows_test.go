//go:build windows

package main

import (
	"errors"
	"strings"
	"testing"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
)

func TestDisplayEditorCompactBoundsKeepHiddenPrecisionOnApply(t *testing.T) {
	w, keys := displayNativeEditorPoststackProfiles(t, true)
	w.key, w.identity = keys[0], keys[0]
	surface := displayOwners[w.owner].surfaces[keys[0]]
	want := displaycore.Range{Min: -7138.39212345678, Max: 7138.39278123456}
	surface.s.Color.Range = want
	displayLoadDraft(w)
	if got := [2]string{getText(w.controls[deMin]), getText(w.controls[deMax])}; got != [2]string{"-7138.39", "7138.39"} {
		t.Fatalf("visible fields are not compact: %v", got)
	}
	for _, command := range []int{deGradientPage, deTickPage, deAppearancePage, deRangePage} {
		displayEditorCommand(w, command, 0)
	}
	displayEditorCommand(w, deMin, 0x0200) // EN_KILLFOCUS (no edit)
	displayEditorCommand(w, deMax, 0x0200)
	if got, dirty, err := displayEditorTypedBounds(w, nil); err != nil || dirty || got != want {
		t.Fatalf("visiting tabs/focus rounded or dirtied the range: %+v, dirty=%v, %v", got, dirty, err)
	}
	// A genuine annotation edit forces Apply to commit. Both untouched range
	// fields must still write their exact values, not the short displayed text.
	setText(w.controls[deUnit], "米")
	displayApplyEditor(w, false)
	settings, _ := displayCommitted(w.owner, w.key)
	if settings.Color == nil || settings.Color.Unit != "米" || settings.Color.Range != want {
		t.Fatalf("applied unit did not preserve exact bounds: %+v (%s)", settings.Color, getText(w.status))
	}
	setText(w.controls[deMin], "-7000.123456789")
	if got, dirty, err := displayEditorTypedBounds(w, nil); err != nil || !dirty || got.Min != -7000.123456789 || got.Max != want.Max {
		t.Fatalf("editing one bound rounded the other: %+v, dirty=%v, %v", got, dirty, err)
	}
	setText(w.controls[deMin], "NaN")
	if _, dirty, err := displayEditorTypedBounds(w, nil); err == nil || !dirty {
		t.Fatal("invalid edited bound was silently accepted")
	}
}

func TestDisplayEditorResolvedFollowingRangeKeepsDraftAndAppliedSeparate(t *testing.T) {
	w, keys := displayNativeEditorPoststackProfiles(t, true)
	o := displayOwners[w.owner]
	link := o.links[keys]
	link.Linked = false
	o.links[keys] = link
	a := o.surfaces[keys[0]]
	b := o.surfaces[keys[1]]
	applied := b.s.Color.Range
	a.s.Color.Range = displaycore.Range{Min: -19.123456789, Max: 23.987654321}
	displayLoadDraft(w)
	setText(w.controls[deTitle], "B 的草稿标题")
	displayCheck(w.controls[deCompareMatchA], true) // Prospective follow, not applied.
	state := displayEditorResolveRangeState(w)
	if !state.Ready || !state.Following || state.Applied.Range != applied || state.Draft.Range != a.s.Color.Range {
		t.Fatalf("follow candidate did not separate local/applied A/B: %+v", state)
	}
	displayEditorRefreshRangeStatus(w)
	if w.exactBounds.exact != a.s.Color.Range || w.exactBounds.forced || getText(w.controls[deTitle]) != "B 的草稿标题" ||
		!strings.Contains(getText(w.controls[deCoverage]), "草稿范围") {
		t.Fatalf("follower numeric fields or annotations disagree: %s, %s", getText(w.controls[deTitle]), getText(w.controls[deCoverage]))
	}
	if actual, _ := displayCommitted(w.owner, w.key); actual.Color.Range != applied {
		t.Fatal("rendered B changed before Apply")
	}
}

func TestDisplayEditorSymmetricDraftSynchronizesExactRange(t *testing.T) {
	w, keys := displayNativeEditorPoststackProfiles(t, true)
	w.key, w.identity = keys[0], keys[0]
	c := displayOwners[w.owner].surfaces[w.key].s.Color
	want := displaycore.Range{Min: -3.123456789, Max: 9.123456789}
	c.Range = want
	displayLoadDraft(w)
	displayCheck(w.controls[deSym], true)
	state := displayEditorResolveRangeState(w)
	displayEditorRefreshRangeStatus(w)
	if !state.Ready || state.Applied.Range != want || state.Draft.Range != want.Symmetric() || w.exactBounds.exact != want.Symmetric() ||
		!w.exactBounds.forced || !strings.Contains(getText(w.controls[deCoverage]), "草稿范围") {
		t.Fatalf("unchecked exact applied range escaped or new symmetric draft is inconsistent: %+v, fields %+v", state, w.exactBounds)
	}
	if c.Range != want {
		t.Fatal("draft synchronization wrote to the renderer")
	}
}

func TestDisplayEditorOpeningAStoredManualMappingDoesNotNormalizeIt(t *testing.T) {
	w, keys := displayNativeEditorPoststackProfiles(t, true)
	w.key, w.identity = keys[0], keys[0]
	c := displayOwners[w.owner].surfaces[w.key].s.Color
	// Old configurations can have an asymmetric range with the symmetry
	// option recorded. The already applied pixels still use those endpoints.
	want := displaycore.Range{Min: -3.123456789, Max: 9.123456789}
	c.Range, c.Symmetric = want, true
	displayLoadDraft(w)
	state := displayEditorResolveRangeState(w)
	displayEditorRefreshRangeStatus(w)
	if !state.Ready || state.Applied.Range != want || state.Draft.Range != want || w.exactBounds.exact != want ||
		w.exactBounds.forced || !strings.Contains(getText(w.controls[deCoverage]), "已应用范围") {
		t.Fatalf("opening normalized or mislabeled an applied mapping: %+v, fields %+v", state, w.exactBounds)
	}
}

func TestDisplayEditorOpeningAppliedAutomaticRangeDoesNotPreviewUnpublishedEndpoints(t *testing.T) {
	w, keys := displayNativeEditorPoststackProfiles(t, true)
	w.key, w.identity = keys[0], keys[0]
	o := displayOwners[w.owner]
	a := o.surfaces[keys[0]]
	applied := displaycore.Range{Min: -7.123456789, Max: 8.123456789}
	a.s.Color.Range = applied
	a.s.Color.SetRangePolicy(displaycore.FullRange)
	joined := displaycore.Range{Min: -90, Max: 150}
	for i, key := range keys[:2] {
		s := o.surfaces[key]
		s.statsVersion, s.s.StatsVersion = "ready", "ready"
		s.stats.Add([]float64{joined.Min, 80}[i])
		s.stats.Add([]float64{61, joined.Max}[i])
	}
	displayLoadDraft(w)
	state := displayEditorResolveRangeState(w)
	if !state.Ready || state.Applied.Range != applied || state.Draft.Range != applied ||
		!state.Resolution.Coverage.Valid || state.Reference != joined || w.exactBounds.exact != applied || w.exactBounds.forced {
		t.Fatalf("statistics silently altered the applied view or user baseline: %+v", state)
	}
	displayEditorSetPolicy(w, displaycore.PercentileRange)
	setText(w.controls[dePercentLow], "0")
	setText(w.controls[dePercentHigh], "100")
	state = displayEditorResolveRangeState(w)
	displayEditorRefreshRangeStatus(w)
	displayEditorRefreshFooter(w)
	if !state.Ready || state.Applied.Range != applied || state.Draft.Range != joined ||
		!strings.Contains(getText(w.controls[deCoverage]), "草稿范围") || !strings.Contains(getText(w.status), "有未应用更改") {
		t.Fatalf("explicit new policy did not offer a differentiated draft: %+v (%q, %q)", state, getText(w.controls[deCoverage]), getText(w.status))
	}
	o.surfaces[keys[1]].pending = true
	displayEditorSetPolicy(w, displaycore.FullRange)
	state = displayEditorResolveRangeState(w)
	if !state.Ready || state.Draft.Range != applied || state.Err != nil {
		t.Fatalf("pending stats hid an already active range: %+v", state)
	}
}

func TestDisplayEditorTypographyKeepsNumericPlanAndAllowsCrowdedFivePointApply(t *testing.T) {
	w, keys := displayNativeEditorPoststackProfiles(t, false)
	w.key, w.identity, w.axis = keys[0], keys[0], "x"
	e := displayOwners[w.owner].surfaces[w.key]
	e.s.equalTicks = true
	e.s.Plot = RECT{Left: 22, Right: 37, Top: 22, Bottom: 450} // deliberately too narrow for all labels
	x := &e.s.Axes[0]
	x.Data = displaycore.Range{Min: 0, Max: 4000}
	x.View = displaycore.Range{Min: 184, Max: 2456}
	x.Format = displaycore.DefaultNumberFormat
	displayLoadDraft(w)
	setText(w.controls[deTickDivisions], "8")
	setText(w.controls[deTickLength], "2")
	for _, step := range []struct {
		text string
		want float64
	}{{"10", 10}, {"16", 16}, {"8", 8}, {"5", 5}} {
		setText(w.controls[deTickFont], step.text)
		displayEditorRefreshTicks(w)
		a, err := displayEditorPreviewAxis(w)
		if err != nil || displayPlotFontPT(a.TickFontPT, false) != step.want {
			t.Fatalf("preview did not use %s pt: %v, %+v", step.text, err, a)
		}
	}
	p, _, err := displayEditorDraftTicks(w)
	if err != nil || p.Step != 284 || len(p.Ticks) != 9 || p.LayoutWarning == "" {
		t.Fatalf("wrong plan at 5 pt and N=8: %+v, %v", p, err)
	}
	setText(w.controls[deTickFont], "4")
	displayEditorRefreshTicks(w)
	a, err := displayEditorPreviewAxis(w)
	if err != nil || a.TickFontPT != 5 || a.MajorTickDIP() != 2 || !strings.Contains(getText(w.controls[deTickCurrent]), "284") {
		t.Fatalf("invalid typography reset a legal numeric plan: %+v, %v", a, err)
	}
	if errText := getText(w.controls[deTickPreview]); !strings.Contains(errText, "5–36") ||
		!strings.Contains(errText, "上一次有效") || strings.Contains(errText, "重叠") {
		t.Fatalf("invalid typography left an obsolete density hint: %q", errText)
	}
	if _, err := displayEditorDraftAxis(w); err == nil || !strings.Contains(getText(w.labels[deTickFont]), "5–36") {
		t.Fatalf("invalid typed font did not flag its own field: %v, %q", err, getText(w.labels[deTickFont]))
	}
	setText(w.controls[deTickFont], "5")
	displayEditorRefreshTicks(w)
	if strings.Contains(getText(w.labels[deTickFont]), "5–36") {
		t.Fatal("typing a legal value left the field error")
	}
	displayApplyEditor(w, false)
	s, _ := displayCommitted(w.owner, w.key)
	if got := s.Axes[0]; got.TickFontPT != 5 || got.MajorTickDIP() != 2 || got.Divisions != 8 || got.View != x.View {
		t.Fatalf("valid but crowded typography was not applied: %+v (%s)", got, getText(w.status))
	}
}

func TestDisplayEditorNativeHorizontalTickResetOnlyStagesDefaults(t *testing.T) {
	w, keys := displayNativeEditorPoststackProfiles(t, false)
	w.key, w.identity, w.axis, w.section = keys[0], keys[0], "x", displayPageTicks
	e := displayOwners[w.owner].surfaces[w.key]
	e.s.equalTicks, e.s.distance = true, true
	e.s.Axes[0].Format = displaycore.DefaultNumberFormat
	saved := displayDefaults(e.s)
	saved.Axes[0].Format, saved.Axes[0].Digits, saved.Axes[0].TickFontPT = displaycore.FixedFormat, 4, 16
	e.saved = &saved
	displayLoadDraft(w)
	if got := displaySelection(w.controls[deFormat]); got != displayEditorFormatIndex(displaycore.FixedFormat) {
		t.Fatalf("fixture did not load saved formatting: %d", got)
	}
	for range 5 {
		// Real Win32 command delivery (BN_CLICKED=0), including the
		// synchronous control notifications that Reset's reload generates.
		pSendMessageW.Call(w.handle, WM_COMMAND, deReset, w.controls[deReset])
		pUpdateWindow.Call(w.handle)
		pUpdateWindow.Call(w.body)
		if w.loading || w.section != displayPageTicks || displayEditors[w.handle] != w || e.saved != &saved {
			t.Fatalf("Reset lost the window, selected page, or saved settings: page=%d loading=%v", w.section, w.loading)
		}
		if got := displaySelection(w.controls[deFormat]); got != displayEditorFormatIndex(displaycore.IntegerFormat) {
			t.Fatalf("Reset did not stage integer defaults: %d", got)
		}
		if saved.Axes[0].Format != displaycore.FixedFormat || saved.Axes[0].TickFontPT != 16 || saved.Axes[0].Digits != 4 {
			t.Fatal("Reset changed the already applied style")
		}
	}
}

func TestDisplayEditorStagedExactRangeHonorsLinkedGroupAndSymmetry(t *testing.T) {
	w, keys := displayNativeEditorPoststackProfiles(t, true)
	o := displayOwners[w.owner]
	w.key, w.identity = keys[2], keys[2]
	want := displaycore.Range{Min: -2.1234567890123, Max: 4.9876543210123}
	o.surfaces[keys[2]].s.Color.Range = want
	aSurf := o.surfaces[keys[0]]
	before := aSurf.s.Color.Range
	aSurf.s.Color.Symmetric = true
	displayLoadDraft(w)
	if err := displayEditorStageCopy(w, keys, [3]bool{false, true, false}, displayCopyRange); err == nil || !strings.Contains(err.Error(), "零对称") {
		t.Fatalf("non-symmetric exact endpoints accepted against the linked group's symmetry: %v", err)
	}
	for _, d := range w.drafts {
		if d.key == keys[0] || d.key == keys[1] {
			t.Fatalf("failed copy still left a linked target draft: %+v", d)
		}
	}
	aSurf.s.Color.Symmetric = false
	if err := displayEditorStageCopy(w, keys, [3]bool{false, true, false}, displayCopyRange); err != nil {
		t.Fatal(err)
	}
	for _, key := range keys[:2] {
		if s, _ := displayCommitted(w.owner, key); s.Color.Range != before {
			t.Fatal("linked group changed before Apply")
		}
	}
	displayEditorRefreshFooter(w)
	if text := getText(w.status); !strings.Contains(text, "A、B") {
		t.Errorf("pending copy did not name both linked targets: %q", text)
	}
	displayApplyEditor(w, false)
	for _, key := range keys[:2] {
		s, _ := displayCommitted(w.owner, key)
		if s.Color.Range != want || s.Color.EffectiveRangePolicy() != displaycore.ManualRange {
			t.Fatalf("%s: exact copied endpoints lost or not converted to manual (%s): %+v", key, getText(w.status), s.Color)
		}
	}
}

func TestDisplayEditorBatchRollsBackSavedValuesOnViewportFailure(t *testing.T) {
	w, keys := displayNativeEditorPoststackProfiles(t, true)
	o := displayOwners[w.owner]
	a, _ := displayCommitted(w.owner, keys[0])
	b, _ := displayCommitted(w.owner, keys[1])
	before := a.Color.Unit
	a.Color.Unit = "Pa"
	b.Axes[0].View, b.Axes[0].ViewEdited = displaycore.Range{Min: 250, Max: 750}, true
	oldA, oldB := o.surfaces[keys[0]].saved, o.surfaces[keys[1]].saved
	want := errors.New("fake viewport error")
	var calls int
	o.surfaces[keys[1]].s.Apply = func(settings displaycore.Settings) error {
		calls++
		if !settings.Axes[0].ViewEdited || settings.Axes[0].View != b.Axes[0].View {
			t.Fatalf("adapter received wrong viewport: %+v", settings.Axes[0])
		}
		return want
	}
	if err := displayCommitBatch(w.owner, []displayBatchEdit{
		{key: keys[0], identity: keys[0], color: true, settings: a},
		{key: keys[1], identity: keys[1], axis: "x", settings: b},
	}); !errors.Is(err, want) || calls != 1 {
		t.Fatalf("unexpected adapter result %v (%d calls)", err, calls)
	}
	after, _ := displayCommitted(w.owner, keys[0])
	if after.Color.Unit != before || o.surfaces[keys[0]].saved != oldA || o.surfaces[keys[1]].saved != oldB {
		t.Fatalf("failed adapter left a partial settings commit: %+v", after.Color)
	}
}

func TestDisplayEditorCompactAxisUnitsDoNotRoundCanonicalEndpoints(t *testing.T) {
	w, surface := displayNativeEditorFixture(t, false)
	w.axis = "y"
	want := displaycore.Range{Min: 200.1234567890123, Max: 799.9876543210987}
	surface.s.Axes[1].View = want
	displayLoadDraft(w)
	setText(w.controls[deUnit], "s")
	displayEditorChangeUnit(w)
	if got := [2]string{getText(w.controls[deMin]), getText(w.controls[deMax])}; got != [2]string{"0.200123", "0.799988"} {
		t.Fatalf("axis did not present the short seconds values: %v", got)
	}
	if got, dirty, err := displayEditorTypedBounds(w, &w.unit); err != nil || dirty || got != want {
		t.Fatalf("unit-only switch rounded or edited underlying milliseconds: %+v, dirty=%v, %v", got, dirty, err)
	}
	setText(w.controls[deMax], "0.75")
	if got, dirty, err := displayEditorTypedBounds(w, &w.unit); err != nil || !dirty || got.Min != want.Min || got.Max != 750 {
		t.Fatalf("edit in seconds lost canonical range/units: %+v, dirty=%v, %v", got, dirty, err)
	}
}

func TestDisplayEditorBatchRejectsEveryEditWhenATargetIsStale(t *testing.T) {
	w, keys := displayNativeEditorPoststackProfiles(t, true)
	o := displayOwners[w.owner]
	a, _ := displayCommitted(w.owner, keys[0])
	b, _ := displayCommitted(w.owner, keys[1])
	before := a.Color.Clone()
	a.Color.Unit = "Pa"
	a.Color.Range = displaycore.Range{Min: -401, Max: 302}
	oldA, oldB := o.surfaces[keys[0]].saved, o.surfaces[keys[1]].saved
	oldLink, oldFollow := displayCompareOptions(w.owner, keys[0])
	var repaintCalls int
	o.surfaces[keys[0]].s.Apply = func(displaycore.Settings) error { repaintCalls++; return nil }
	err := displayCommitBatch(w.owner, []displayBatchEdit{
		{key: keys[0], identity: keys[0], color: true, setLink: true, link: !oldLink, settings: a},
		{key: keys[1], identity: "previous-B-dataset", color: true, settings: b},
	})
	if err == nil {
		t.Fatal("stale B was accepted")
	}
	after, _ := displayCommitted(w.owner, keys[0])
	link, follow := displayCompareOptions(w.owner, keys[0])
	if after.Color.Range != before.Range || after.Color.Unit != before.Unit || o.surfaces[keys[0]].saved != oldA || o.surfaces[keys[1]].saved != oldB || link != oldLink || follow != oldFollow || repaintCalls != 0 {
		t.Fatalf("first edit escaped a failed batch: color=%+v linked=%v follow=%v adapterCalls=%d", after.Color, link, follow, repaintCalls)
	}
}

func TestDisplayEditorBatchCommitsLinkedAnnotationsWithoutChangingSharedMap(t *testing.T) {
	w, keys := displayNativeEditorPoststackProfiles(t, true)
	a, _ := displayCommitted(w.owner, keys[0])
	b, _ := displayCommitted(w.owner, keys[1])
	c, _ := displayCommitted(w.owner, keys[2])
	shared, delta, deltaUnit := a.Color.Range, c.Color.Range, c.Color.Unit
	a.Color.Title, a.Color.Unit = "振幅", "Pa"
	b.Color.Title, b.Color.Unit = "振幅", "米"
	if err := displayCommitBatch(w.owner, []displayBatchEdit{
		{key: keys[0], identity: keys[0], color: true, settings: a},
		{key: keys[1], identity: keys[1], color: true, settings: b},
	}); err != nil {
		t.Fatal(err)
	}
	a, _ = displayCommitted(w.owner, keys[0])
	b, _ = displayCommitted(w.owner, keys[1])
	c, _ = displayCommitted(w.owner, keys[2])
	linked, followed := displayCompareOptions(w.owner, keys[0])
	if !linked || followed || a.Color.Range != shared || b.Color.Range != shared || c.Color.Range != delta || a.Color.Unit != "Pa" || b.Color.Unit != "米" || c.Color.Unit != deltaUnit {
		t.Fatalf("batch changed links or crossed panels: A %+v, B %+v, delta %+v; links %v,%v", a.Color, b.Color, c.Color, linked, followed)
	}
}
