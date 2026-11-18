//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
)

func TestDisplayPreferencesDiskRoundTripKeepsStyleAndNotOldData(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	oldPrefs, oldLoaded, oldTraceStyle := displayPrefs, displayPrefsLoaded, traceScientificStyle
	t.Cleanup(func() { displayPrefs, displayPrefsLoaded, traceScientificStyle = oldPrefs, oldLoaded, oldTraceStyle })
	reset := func() {
		displayPrefsLoaded = false
		displayPrefs.Version, displayPrefs.Views, displayPrefs.TraceStyle = 0, nil, nil
	}
	reset()
	axis := displaycore.Axis{
		ID: "crossline", Title: "XL", Data: displaycore.Range{Min: 184, Max: 2456}, View: displaycore.Range{Min: 1120, Max: 1322},
		ViewEdited: true, Divisions: 8, TickFontPT: 13, TickLengthSet: true, TickLength: 0,
		Format: displaycore.ScientificFormat, Digits: 2,
	}
	color := displaycore.ColorMapping{Range: displaycore.Range{Min: -520, Max: 430}, Stops: displayPreset(0), Title: "振幅", Unit: "counts", VisibilitySet: true}
	settings := displaycore.Settings{Axes: []displaycore.Axis{axis}, Color: &color, PlotTitleFontPT: 15}
	displayPersist("section-a", settings)
	traceScientificStyle.options = traceScientificPlotOptions{ResearchStyle: true, TimeInMS: true, HideGrid: true, LineWidthDIP: 2.5,
		CurvePattern: [3]int{1, 2, 3}, CurveColorSet: [3]bool{true, false, false}, CurveColor: [3]uint32{0x123456}}
	persistTraceScientificStylePreference() // atomically replaces the existing JSON
	if entries, err := os.ReadDir(filepath.Dir(displayPrefsPath())); err != nil || len(entries) != 1 || entries[0].Name() != "display_settings_v1.json" {
		t.Fatalf("preference write left unexpected files: %v / %v", entries, err)
	}
	if color.Range.Max != 430 || color.Hidden || axis.ViewEdited != settings.Axes[0].ViewEdited || axis.View != settings.Axes[0].View {
		t.Fatal("persisting modified live display settings")
	}
	reset()
	displayLoadPrefs()
	p, ok := displayPrefs.Views["section-a"]
	if !ok || p.PlotTitleFontPT != 15 || len(p.Axes) != 1 || p.Color == nil {
		t.Fatalf("missing persisted display style: %+v", p)
	}
	if a := p.Axes[0]; a.Data != (displaycore.Range{}) || a.View != (displaycore.Range{}) || a.ViewEdited || a.Divisions != 8 || a.Format != displaycore.ScientificFormat || a.Digits != 2 || a.TickFontPT != 13 || !a.TickLengthSet || a.MajorTickDIP() != 0 {
		t.Fatalf("dataset range leaked or axis style failed to round-trip: %+v", a)
	}
	if p.Color.Range != (displaycore.Range{Min: -1, Max: 1}) || !p.Color.Hidden || p.Color.VisibilitySet || p.Color.Unit != "counts" {
		t.Fatalf("dataset mapping/visibility leaked or color style failed to round-trip: %+v", p.Color)
	}
	newAxis := displaycore.Axis{ID: "crossline", Data: displaycore.Range{Min: 100, Max: 400}, View: displaycore.Range{Min: 150, Max: 350}}
	newColor := displaycore.ColorMapping{Range: displaycore.Range{Min: -2, Max: 3}, Stops: displayPreset(0)}
	migrated, ok := displayMigrateStyle(displaySurface{Axes: []displaycore.Axis{newAxis}, Color: &newColor}, p)
	if !ok || migrated.Axes[0].View != newAxis.View || migrated.Axes[0].Data != newAxis.Data || migrated.Axes[0].ViewEdited || migrated.Color.Range != newColor.Range || !migrated.Color.Hidden {
		t.Fatalf("another data source inherited the old viewport: %+v", migrated)
	}
	traceScientificStyle.options = traceScientificPlotOptions{}
	loadTraceScientificStylePreference()
	o := traceScientificStyle.options
	if !o.TimeInMS || !o.ResearchStyle || !o.HideGrid || o.LineWidthDIP != 2.5 || o.CurvePattern != ([3]int{1, 2, 3}) || !o.CurveColorSet[0] || o.CurveColor[0] != 0x123456 {
		t.Fatalf("trace style failed to round-trip: %+v", o)
	}
	t.Log("display JSON replaced and reread from an isolated profile; style, explicit zero tick length and curve preferences survived without leaking previous viewport, amplitude limits or legend visibility")
}

func TestDisplayColorbarDefaultMigrationAndHit(t *testing.T) {
	owner, o := testDisplaySurfaces(t)
	defaultColor := displaycore.ColorMapping{Range: displaycore.Range{Min: -2, Max: 3}, Hidden: true, Stops: displayPreset(0)}
	legacyColor := defaultColor.Clone()
	legacyColor.Hidden = false
	legacyColor.VisibilitySet = true // even an explicit choice in prefs belongs to the previous data
	surface := displaySurface{Identity: "first", Color: &defaultColor, ColorRegion: RECT{Left: 200, Top: 20, Right: 260, Bottom: 190}}
	old := displaycore.Settings{Color: &legacyColor}
	migrated, ok := displayMigrateStyle(surface, old)
	if !ok || !migrated.Color.Hidden || migrated.Color.VisibilitySet {
		t.Fatal("legacy visibility should not override the new hidden default")
	}
	e := &displaySurfaceState{s: surface, saved: &migrated, order: 1}
	o.surfaces["plot"] = e
	if key, _, _ := displayHit(owner, 218, 40, false); key != "" {
		t.Fatal("hidden legend retained a double-click target")
	}
	if !displaySetColorbarVisible(owner, "plot", "first", true) {
		t.Fatal("cannot show the legend on its active data")
	}
	if key, _, isColor := displayHit(owner, 218, 40, false); key != "plot" || !isColor {
		t.Fatal("shown legend could not be double-clicked")
	}
	if !displaySetColorbarVisible(owner, "plot", "first", false) || displayColorbarReserved(owner, "plot") {
		t.Fatal("hiding the legend must release its plot space")
	}
	if key, _, _ := displayHit(owner, 218, 40, false); key != "" {
		t.Fatal("a hidden legend is still active")
	}
}

func TestPoststackNewResultInheritsCurrentAxisAndReplacementPreservesIndependentStyle(t *testing.T) {
	owner, o := testDisplaySurfaces(t)
	prefs, loaded := displayPrefs, displayPrefsLoaded
	t.Cleanup(func() { displayPrefs, displayPrefsLoaded = prefs, loaded })
	displayPrefsLoaded, displayPrefs.Views = true, nil
	group := displayCompareBinding{Keys: [3]string{"section-a", "section-b", "section-d"}, CanLink: true, PoststackProfiles: true}
	axis := displaycore.Axis{ID: "crossline", Unit: "", Format: displaycore.IntegerFormat, Divisions: 8, View: displaycore.Range{Min: 22, Max: 68}}
	style := displaycore.Settings{Axes: []displaycore.Axis{axis}}
	displayRegister(owner, group.Keys[0], displaySurface{Identity: "A1", Axes: []displaycore.Axis{axis}, Compare: &group})
	o.surfaces[group.Keys[0]].saved = &style
	for _, key := range group.Keys[1:] {
		local := axis
		local.View, local.Divisions, local.AllowsPoint = displaycore.Range{Min: 99, Max: 99}, 0, true
		m := displaycore.ColorMapping{Range: displaycore.Range{Min: -5, Max: 5}, Stops: displayPreset(0)}
		s := displaySurface{equalTicks: true, LiveViewport: true, Identity: "result1", Compare: &group, Axes: []displaycore.Axis{local}, Color: &m}
		displayRegister(owner, key, s)
		if available, followed := displayBAxisLink(owner, key); !available || !followed {
			t.Fatalf("fresh %s did not default to following A", key)
		}
		if got := displayAxisFor(owner, key, local); got.Divisions != 8 || got.Format != displaycore.IntegerFormat || !got.AllowsPoint || got.View != local.View || o.surfaces[key].saved != nil {
			t.Fatalf("fresh %s did not inherit only A annotation: %+v", key, got)
		}
		// An independent draft becomes visible only after the user explicitly
		// turns off live following. Editing a saved style alone does not unlink.
		checked := false
		current, _ := displayCommitted(owner, key)
		if err := displaySubmitWithBAxisLink(owner, key, s.Identity, current, false, false, &checked); err != nil {
			t.Fatalf("unlink %s: %v", key, err)
		}
		e := o.surfaces[key]
		localColor := m.Clone()
		independent := displaycore.Settings{Axes: []displaycore.Axis{local}, Color: &localColor}
		independent.Axes[0].Divisions, independent.Axes[0].Format, independent.Axes[0].Digits = 7, displaycore.FixedFormat, 2
		independent.Color.Range = displaycore.Range{Min: -2.2, Max: 2.2}
		e.saved, e.legendShown = &independent, true
		s.Identity = "result2"
		displayRegister(owner, key, s)
		if _, followed := displayBAxisLink(owner, key); followed {
			t.Fatalf("replacement %s reset the explicit axis opt-out", key)
		}
		set, ok := displayCommitted(owner, key)
		if !ok || set.Axes[0].Divisions != 7 || set.Axes[0].Format != displaycore.FixedFormat || set.Axes[0].Digits != 2 || set.Color.Range != independent.Color.Range || set.Color.Hidden || !set.Axes[0].AllowsPoint {
			t.Fatalf("replacement %s lost its independent display: %+v", key, set)
		}
	}
}

func TestDisplayLegendVisibilityDoesNotFreezeAmplitudeAndResetsOnNewData(t *testing.T) {
	owner, o := testDisplaySurfaces(t)
	prefs, loaded := displayPrefs, displayPrefsLoaded
	t.Cleanup(func() { displayPrefs, displayPrefsLoaded = prefs, loaded })
	displayPrefsLoaded = true
	displayPrefs.Views = nil
	color := displaycore.ColorMapping{Range: displaycore.Range{Min: -5, Max: 3}, Hidden: true, Step: 1e-5, Stops: displayPreset(0)}
	s := displaySurface{Identity: "first", LiveViewport: true, Color: &color, Apply: func(displaycore.Settings) error {
		t.Fatal("visibility change reran the data/color adapter")
		return nil
	}}
	displayRegister(owner, "plot", s)
	if displayColorbarReserved(owner, "plot") || displayColorbarReserved(owner, "missing") {
		t.Fatal("fresh data reserved unrequested legend space")
	}
	draft, _ := displayCommitted(owner, "plot")
	draft.Color.Hidden, draft.Color.VisibilitySet = false, true
	draft.Color.TickLength, draft.Color.TickLengthSet = draft.Color.MajorTickDIP(), true
	if err := displaySubmit(owner, "plot", s.Identity, draft, false, false); err != nil {
		t.Fatalf("show legend despite density of existing manual ticks: %v", err)
	}
	color.Range = displaycore.Range{Min: -2, Max: 8}
	displayRegister(owner, "plot", s)
	if got := displayEffectiveLegend(owner, "plot", color); got.Hidden || got.Range != color.Range || o.surfaces["plot"].saved != nil {
		t.Fatalf("legend visibility froze implicit amplitude scale: %+v", got)
	}
	draft, _ = displayCommitted(owner, "plot")
	draft.Color.Hidden = true
	if err := displaySubmit(owner, "plot", s.Identity, draft, false, false); err != nil || !displayEffectiveLegend(owner, "plot", color).Hidden {
		t.Fatalf("cannot hide the legend: %v", err)
	}
	displaySetColorbarVisible(owner, "plot", s.Identity, true)
	s.Identity = "second"
	displayRegister(owner, "plot", s)
	if displayColorbarVisible(owner, "plot") || displaySetColorbarVisible(owner, "plot", "first", true) {
		t.Fatal("a replacement dataset or stale owner retains old visibility")
	}
}

func TestDisplayCompareLegendVisibilityLeavesOtherPanelsAlone(t *testing.T) {
	owner, o := testDisplaySurfaces(t)
	oldWindow, oldDirty := compareHwnd, compareBaseDirty
	compareHwnd, compareBaseDirty = owner, false
	t.Cleanup(func() { compareHwnd, compareBaseDirty = oldWindow, oldDirty })
	b := displayCompareBinding{Keys: [3]string{"a", "b", "d"}, CanLink: true, Linked: true, FollowA: true}
	o.links[b.Keys] = b
	for i, key := range b.Keys {
		m := displaycore.ColorMapping{Range: displaycore.Range{Min: -2, Max: 3}, Hidden: true, Step: float64(i + 1), Stops: displayPreset(0)}
		o.surfaces[key] = &displaySurfaceState{s: displaySurface{Identity: key, Compare: &b, Color: &m}, order: 1}
	}
	if !displaySetColorbarVisible(owner, "d", "d", true) || !compareBaseDirty {
		t.Fatal("showing the difference legend did not invalidate the cached scene")
	}
	for i, key := range b.Keys {
		set, _ := displayCommitted(owner, key)
		if set.Color.Hidden != (key != "d") || set.Color.Step != float64(i+1) {
			t.Fatalf("%s inherited another panel's legend state or style: %+v", key, set.Color)
		}
	}
}

func TestDisplayCompareSwitchModesWithAllThreePanels(t *testing.T) {
	owner, o := testDisplaySurfaces(t)
	b := displayCompareBinding{Keys: [3]string{"a", "b", "d"}, CanLink: true, Linked: true, FollowA: true}
	o.links[b.Keys] = b
	for i, key := range b.Keys {
		m := displaycore.ColorMapping{Range: displaycore.Range{Min: -1 - float64(i), Max: 1 + float64(i)}, Hidden: true, Stops: displayPreset(i)}
		o.surfaces[key] = &displaySurfaceState{s: displaySurface{Identity: key, Compare: &b, Color: &m}, order: 1}
	}
	if got := displayColorTargets(owner, b.Keys); !slices.Equal(got, []int{0, 1, 2}) {
		t.Fatalf("wrong editor targets %v", got)
	}
	o.surfaces["d"].order = 0
	if got := displayColorTargets(owner, b.Keys); !slices.Equal(got, []int{0, 1}) {
		t.Fatalf("disabled difference remains selectable: %v", got)
	}
	o.surfaces["d"].order = 1

	probe := fmt.Errorf("probe without user preference writes")
	as, _ := displayCommitted(owner, "a")
	oldA := as.Color.ColorAt(-.5)
	as.Color.Stops = displayPreset(3)
	o.surfaces["a"].s.Apply = func(displaycore.Settings) error {
		a, _ := displayCommitted(owner, "a")
		other, _ := displayCommitted(owner, "b")
		d, _ := displayCommitted(owner, "d")
		if linked, follow := displayCompareOptions(owner, "a"); linked || follow {
			t.Fatal("independent mode left a panel linked")
		}
		if a.Color.ColorAt(-.5) == oldA || other.Color.ColorAt(-.5) != oldA || d.Color.ColorAt(-.5) != oldA {
			t.Fatal("disabling links while editing A changed B or difference")
		}
		return probe
	}
	if err := displaySubmit(owner, "a", "a", as, false, false); err != probe {
		t.Fatalf("independent submit: %v", err)
	}

	b.Linked, b.FollowA = false, false
	o.links[b.Keys] = b
	bs, _ := displayCommitted(owner, "b")
	o.surfaces["b"].s.Apply = func(displaycore.Settings) error {
		for _, key := range b.Keys {
			value, _ := displayCommitted(owner, key)
			if value.Color.Range != bs.Color.Range || value.Color.ColorAt(0) != bs.Color.ColorAt(0) {
				t.Fatalf("%s does not use the selected common LUT", key)
			}
		}
		return probe
	}
	if err := displaySubmit(owner, "b", "b", bs, true, true); err != probe {
		t.Fatalf("shared submit: %v", err)
	}
}

func testDisplaySurfaces(t *testing.T) (uintptr, *displayOwnerState) {
	t.Helper()
	const owner = uintptr(0x213715)
	o := &displayOwnerState{surfaces: make(map[string]*displaySurfaceState), links: make(map[[3]string]displayCompareBinding)}
	displayOwners[owner] = o
	t.Cleanup(func() { delete(displayOwners, owner) })
	return owner, o
}

func TestDisplaySurfaceSharedAndIndependentColor(t *testing.T) {
	owner, o := testDisplaySurfaces(t)
	b := displayCompareBinding{Keys: [3]string{"a", "b", "d"}, CanLink: true, Linked: true, FollowA: true}
	o.links[b.Keys] = b
	for i, k := range b.Keys {
		m := displaycore.ColorMapping{Range: displaycore.Range{Min: float64(-1 - i), Max: float64(i + 2)}, Stops: displayPreset(i)}
		e := &displaySurfaceState{s: displaySurface{Compare: &b, Color: &m, StatsVersion: "fresh"}, order: uint64(i + 1), statsVersion: "fresh"}
		e.stats.Add(float64(i - 1))
		e.stats.Add(float64(2*i + 1))
		o.surfaces[k] = e
	}
	a, _ := displayCommitted(owner, "a")
	d, _ := displayCommitted(owner, "d")
	if a.Color.Range != d.Color.Range || a.Color.ColorAt(1) != d.Color.ColorAt(1) {
		t.Fatal("difference not following A under the default binding")
	}
	bi, _ := displayCommitted(owner, "b")
	if a.Color.ColorAt(0) != bi.Color.ColorAt(0) {
		t.Fatal("linked values do not have equal colors")
	}
	if r := displayRangeFor(owner, "a", true); r != (displaycore.Range{Min: -1, Max: 3}) {
		t.Fatalf("wrong common auto range: %v", r)
	}
	o.surfaces["b"].pending = true
	if displayRangeFor(owner, "a", true).Valid() {
		t.Fatal("published half of a new joint range")
	}
	o.surfaces["b"].pending = false
	b.Linked, b.FollowA = false, false
	o.links[b.Keys] = b
	di, _ := displayCommitted(owner, "d")
	if a.Color.Range == di.Color.Range {
		t.Fatal("independent delta still mirrors A")
	}
}

func TestDisplaySurfaceHitAndCancelledCommit(t *testing.T) {
	owner, o := testDisplaySurfaces(t)
	r := RECT{Left: 80, Top: 40, Right: 280, Bottom: 230}
	x := displaycore.Axis{ID: "x", Data: displaycore.Range{Min: 0, Max: 10}, View: displaycore.Range{Min: 1, Max: 9}}
	e := &displaySurfaceState{s: displaySurface{Identity: "v1", Axes: []displaycore.Axis{x}, Regions: displayAxisRegions(r, "x", "y"), Plot: r, Apply: func(displaycore.Settings) error { return fmt.Errorf("cannot apply") }}, order: 1}
	o.surfaces["plot"] = e
	if key, _, _ := displayHit(owner, 120, 90, false); key != "" {
		t.Fatal("dialog stole image double-click")
	}
	if key, id, _ := displayHit(owner, 120, 241, false); key != "plot" || id != "x" {
		t.Fatal("axis did not select the plot")
	}
	before := displayDefaults(e.s)
	e.saved = &before
	after := before.Clone()
	after.Axes[0].Title = "draft"
	if err := displaySubmit(owner, "plot", "v1", after, false, false); err == nil || e.saved.Axes[0].Title != "" {
		t.Fatal("failed application leaked into committed state")
	}
	displayBeginPaint(owner)
	if err := displaySubmit(owner, "plot", "v1", after, false, false); err == nil {
		t.Fatal("inactive plot accepted stale draft")
	}
}

func TestPoststackBAxisFollowKeepsLocalGeometryAndRestoresStyle(t *testing.T) {
	owner, o := testDisplaySurfaces(t)
	b := displayCompareBinding{Keys: [3]string{"a", "b", "d"}, CanLink: true, PoststackProfiles: true}
	o.links[b.Keys] = b
	a := displaycore.Axis{ID: "time", BaseUnit: "ms", Unit: "s", Title: "A 时间", TickLength: 14, TickLengthSet: true, Step: 50, Reverse: true, View: displaycore.Range{Min: 0, Max: 1000}, Data: displaycore.Range{Min: 0, Max: 1200}, Down: true}
	local := displaycore.Axis{ID: "time", BaseUnit: "ms", Unit: "ms", Title: "B 时间", Step: 28, View: displaycore.Range{Min: 10, Max: 800}, Data: displaycore.Range{Min: 10, Max: 950}}
	for k, axis := range map[string]displaycore.Axis{"a": a, "b": local, "d": local} {
		o.surfaces[k] = &displaySurfaceState{s: displaySurface{Identity: k, Compare: &b, Axes: []displaycore.Axis{axis}, LiveViewport: true, Apply: func(displaycore.Settings) error {
			t.Fatal("coordinate style link ran the data adapter")
			return nil
		}}, order: 1}
	}
	if available, checked := displayBAxisLink(owner, "b"); !available || checked {
		t.Fatal("B alone should offer an initially independent coordinate profile")
	}
	if a, _ := displayBAxisLink(owner, "a"); a {
		t.Fatal("A offers an invalid self-link")
	}
	s, _ := displayCommitted(owner, "b")
	checked := true
	if err := displaySubmitWithBAxisLink(owner, "b", "b", s, false, false, &checked); err != nil {
		t.Fatal(err)
	}
	linked := displayAxisFor(owner, "b", local)
	if linked.Title != a.Title || linked.MajorTickDIP() != a.MajorTickDIP() || linked.Unit != a.Unit || linked.Reverse != a.Reverse || linked.Data != local.Data || linked.View != local.View || linked.Down != local.Down {
		t.Fatalf("effective B style did not preserve local geometry: %+v", linked)
	}
	a.Title = "A 已改"
	o.surfaces["a"].s.Axes[0] = a
	if got := displayAxisFor(owner, "b", local); got.Title != a.Title {
		t.Fatal("B copied a static snapshot rather than following A")
	}
	if got := displayAxisFor(owner, "d", local); got.Title != local.Title {
		t.Fatal("following B changed the difference panel")
	}
	s, _ = displayCommitted(owner, "b")
	checked = false
	if err := displaySubmitWithBAxisLink(owner, "b", "b", s, false, false, &checked); err != nil {
		t.Fatal(err)
	}
	if got := displayAxisFor(owner, "b", local); got != local {
		t.Fatalf("unlinked B lost its independent style: %+v", got)
	}
}

func TestPoststackColorFollowIsOneWayAndLeavesDeltaIndependent(t *testing.T) {
	owner, o := testDisplaySurfaces(t)
	b := displayCompareBinding{Keys: [3]string{"a", "b", "d"}, CanLink: true, PoststackProfiles: true}
	o.links[b.Keys] = b
	for i, k := range b.Keys {
		m := displaycore.ColorMapping{Range: displaycore.Range{Min: -3 - float64(i), Max: 3 + float64(i)}, Title: k, Step: float64(i + 1), Stops: displayPreset(i)}
		if k == "b" {
			m.Unit = "米"
		}
		value := displaycore.Settings{Color: &m}
		o.surfaces[k] = &displaySurfaceState{s: displaySurface{Identity: k, Compare: &b, Color: &m}, saved: &value, order: 1}
	}
	oldA, oldB, oldD := displayIndependentColor(owner, "a"), displayIndependentColor(owner, "b"), displayIndependentColor(owner, "d")
	s, _ := displayCommitted(owner, "b")
	s.Color.Hidden = false
	stop := fmt.Errorf("probe linked state and roll it back without changing preferences")
	o.surfaces["b"].s.Apply = func(displaycore.Settings) error {
		a, _ := displayCommitted(owner, "a")
		b, _ := displayCommitted(owner, "b")
		d, _ := displayCommitted(owner, "d")
		if a.Color.Range != oldA.Range || a.Color.ColorAt(0) != oldA.ColorAt(0) || b.Color.Range != a.Color.Range || b.Color.ColorAt(0) != a.Color.ColorAt(0) || b.Color.Title != oldB.Title || b.Color.Unit != oldB.Unit || b.Color.Step != oldB.Step || b.Color.Hidden || !a.Color.Hidden || d.Color.Range != oldD.Range || d.Color.ColorAt(0) != oldD.ColorAt(0) {
			t.Fatal("B color link changed A/Delta or failed to preserve B's own visibility")
		}
		if local := displayIndependentColor(owner, "b"); local.Range != oldB.Range || local.ColorAt(0) != oldB.ColorAt(0) || local.Step != oldB.Step || local.Title != oldB.Title {
			t.Fatal("linking discarded B's independent mapping")
		}
		return stop
	}
	if err := displaySubmit(owner, "b", "b", s, true, false); err != stop {
		t.Fatalf("unexpected commit result: %v", err)
	}
	if linked, follow := displayCompareOptions(owner, "b"); linked || follow {
		t.Fatal("failed commit did not roll back the link switches")
	}
}

func TestLinkedDisplayPercentileUsesCurrentUnionAndKeepsFollowingDelta(t *testing.T) {
	owner, o := testDisplaySurfaces(t)
	b := displayCompareBinding{Keys: [3]string{"a", "b", "d"}, CanLink: true, Linked: true, FollowA: true, PoststackProfiles: true}
	o.links[b.Keys] = b
	for i, key := range b.Keys {
		m := displaycore.ColorMapping{Range: displaycore.Range{Min: -1, Max: 1}, Stops: displayPreset(i), Title: "振幅"}
		m.SetRangePolicy(displaycore.PercentileRange)
		m.LowPercentile, m.HighPercentile = 1, 99
		e := &displaySurfaceState{s: displaySurface{Identity: key, Compare: &b, Color: &m, StatsVersion: "range"}, order: uint64(i + 1), statsVersion: "range"}
		switch key {
		case "a":
			for n := 0; n < 45; n++ {
				e.stats.Add(float64(n))
			}
		case "b":
			for n := 45; n < 100; n++ {
				e.stats.Add(float64(n))
			}
			e.stats.Add(10_000)
		default:
			e.stats.Add(-50_000)
			e.stats.Add(50_000)
			m.Unit = "米"
		}
		e.saved = &displaycore.Settings{Color: &m}
		o.surfaces[key] = e
	}
	o.surfaces["b"].pending = true
	o.surfaces["b"].s.ReadStats = func(context.Context) (displaycore.Stats, error) { return displaycore.Stats{}, nil }
	displayRefreshAuto(owner, o, "a")
	if r := o.surfaces["a"].saved.Color.Range; r.Min != -1 || r.Max != 1 {
		t.Fatalf("partial A/B stats installed a new range: %+v", r)
	}
	o.surfaces["b"].pending = false
	displayRefreshAuto(owner, o, "b")
	for _, key := range b.Keys {
		c, _ := displayCommitted(owner, key)
		if c.Color.Range.Min != 1 || c.Color.Range.Max != 99 {
			t.Fatalf("%s does not use the pooled linked percentile: %+v", key, c.Color.Range)
		}
	}
	c, _ := displayCommitted(owner, "d")
	if c.Color.Unit != "米" {
		t.Fatal("following silently took A's unit")
	}
	d, ready, err := displayMappingDistribution(owner, "a", true)
	if !ready || err != nil || d.Count != 101 || d.Estimated {
		t.Fatalf("pooled statistics %+v, ready=%v, err=%v", d, ready, err)
	}
}

func TestDisplayLegendTitleAndDetachedEffectiveScale(t *testing.T) {
	m := displaycore.ColorMapping{Title: "振幅", Unit: "米", Range: displaycore.Range{Min: -20, Max: 30}, Stops: displayPreset(2)}
	m.SetRangePolicy(displaycore.PercentileRange)
	local := displaycore.ColorMapping{Title: "自定义振幅", Unit: "  ", Range: displaycore.Range{Min: -500, Max: 500}, Stops: displayPreset(0)}
	if got := displayColorTitle(m); got != "振幅（米）" {
		t.Fatalf("legend title %q", got)
	}
	if got := displayColorTitle(displaycore.ColorMapping{Unit: "米"}); got != "振幅（米）" {
		t.Fatalf("default legend title %q", got)
	}
	d := displayDetachedColor(&m, &local)
	if d.Range != m.Range || !slices.Equal(d.Stops, m.Stops) || d.EffectiveRangePolicy() != displaycore.ManualRange || displayColorTitle(*d) != "自定义振幅" {
		t.Fatalf("unlinked mapping %+v", d)
	}
	d.Stops[0].Position = 0.01
	if m.Stops[0].Position != 0 {
		t.Fatal("detached gradient aliases A")
	}
}

func TestDisplayAutomaticMappingRollsBackAfterSceneAdapterFailure(t *testing.T) {
	owner, o := testDisplaySurfaces(t)
	m := displaycore.ColorMapping{Range: displaycore.Range{Min: -3, Max: 3}, Stops: displayPreset(0)}
	m.SetRangePolicy(displaycore.FullRange)
	saved := &displaycore.Settings{Color: &m}
	e := &displaySurfaceState{s: displaySurface{Color: &m, StatsVersion: "ready"}, order: 1, saved: saved, statsVersion: "ready"}
	o.surfaces["a"] = e
	e.stats.Add(-30)
	e.stats.Add(80)
	fail := errors.New("frame unavailable")
	var calls int
	readScale := func(s displaycore.Settings) {
		calls++
		if s.Color == nil || s.Color.Range != (displaycore.Range{Min: -30, Max: 80}) {
			t.Fatalf("adapter didn't receive the proposed effective mapping: %+v", s.Color)
		}
	}
	e.s.Apply = func(s displaycore.Settings) error { readScale(s); return fail }
	if err := displayRefreshAuto(owner, o, "a"); !errors.Is(err, fail) || e.saved != saved || calls != 1 || e.mappingError == "" {
		t.Fatalf("assembly failure committed or disappeared: %v, %s, %d", err, e.mappingError, calls)
	}
	e.s.Apply = func(s displaycore.Settings) error { readScale(s); return nil }
	if err := displayRefreshAuto(owner, o, "a"); err != nil || e.saved == saved || e.saved.Color.Range.Min != -30 || e.mappingError != "" || calls != 2 {
		t.Fatalf("valid retry didn't publish: %v, %s, %d", err, e.mappingError, calls)
	}
	if saved.Color.Range != (displaycore.Range{Min: -3, Max: 3}) {
		t.Fatal("old committed settings were mutated in place")
	}
}
