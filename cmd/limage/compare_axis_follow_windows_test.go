//go:build windows

package main

import (
	"reflect"
	"testing"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
)

func TestCompareCoordinateProfilesFollowExactReferenceTicks(t *testing.T) {
	a := displaycore.Axis{ID: "crossline", View: displaycore.Range{Min: 184, Max: 2456},
		Unit: "", Format: displaycore.IntegerFormat, Divisions: 4}
	b := a
	b.Divisions, b.Format = 5, displaycore.AutoFormat // emulate obsolete panel-local configuration
	input := [3]displayPlotInput{
		{X: a, Y: displayEditorAxisDummy()},
		{X: b, Y: displayEditorAxisDummy(), FollowA: true},
		{X: b, Y: displayEditorAxisDummy(), FollowA: true},
	}
	m := displayMetrics(0, 96)
	for i := range input {
		f, release := displayCompareTypographyFor(0, 96, input[i], 11)
		defer release()
		input[i].Typography = f
	}
	plans := displayCompareSceneLayouts(m, RECT{Right: 1700, Bottom: 630}, input[:])
	want := []displaycore.Tick{{Value: 184, Label: "184"}, {Value: 752, Label: "752"},
		{Value: 1320, Label: "1320"}, {Value: 1888, Label: "1888"}, {Value: 2456, Label: "2456"}}
	for i, p := range plans {
		if got := p.AxisTicks["crossline"].Ticks; !reflect.DeepEqual(got, want) {
			t.Fatalf("panel %d locations differ from A: %#v", i, got)
		}
	}
	b.View = displaycore.Range{Min: 800, Max: 1900}
	if got, ok := displayFollowingTicks(b, a, plans[0].AxisTicks["crossline"]); !ok || !reflect.DeepEqual(got.Ticks, want[2:4]) {
		t.Fatalf("partial coverage: %#v %t", got, ok)
	}
	input[1].FollowA = false
	plans = displayCompareSceneLayouts(m, RECT{Right: 1700, Bottom: 630}, input[:])
	if got := plans[1].AxisTicks["crossline"].Ticks; reflect.DeepEqual(got, want) {
		t.Fatal("independent B inherited A ticks")
	}
}

func TestCompareCoordinateProfilesDefaultAndKeepExplicitIndependence(t *testing.T) {
	owner, o := testDisplaySurfaces(t)
	keys := [3]string{"profile-a", "profile-b", "profile-diff"}
	b := &displayCompareBinding{Keys: keys, PoststackProfiles: true}
	apply := func(panel int, id string) {
		x := displaycore.Axis{ID: "crossline", View: displaycore.Range{Min: 184, Max: 2456}}
		displayRegister(owner, keys[panel], displaySurface{Identity: id, equalTicks: true, Compare: b,
			Axes: []displaycore.Axis{x}, LiveViewport: true})
	}
	apply(0, "A")
	apply(1, "B")
	apply(2, "A/B")
	for _, key := range keys[1:] {
		if can, follows := displayBAxisLink(owner, key); !can || !follows {
			t.Fatalf("%s coordinate inheritance is not initially on", key)
		}
	}
	o.surfaces[keys[1]].bAxesFollow = false
	apply(1, "new B")
	apply(2, "A/new B")
	if _, follows := displayBAxisLink(owner, keys[1]); follows {
		t.Fatal("replacing B reset its explicit independence")
	}
	if _, follows := displayBAxisLink(owner, keys[2]); !follows {
		t.Fatal("independent B changed the difference setting")
	}
	apply(0, "new A")
	apply(1, "new B")
	if _, follows := displayBAxisLink(owner, keys[1]); !follows {
		t.Fatal("new A inherited an earlier A's coordinate opt-out")
	}
}
