//go:build windows

package main

import (
	"reflect"
	"testing"
	"time"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
)

func TestDisplayAxisPanDirectionUnitsAndLimits(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		vertical, down, reverse bool
		unit                    string
		want                    displaycore.Range
	}{
		{"x-right", false, false, false, "ms", displaycore.Range{Min: 180, Max: 380}},
		{"x-reverse", false, false, true, "ms", displaycore.Range{Min: 220, Max: 420}},
		{"time-down", true, true, false, "ms", displaycore.Range{Min: 180, Max: 380}},
		{"time-up", true, false, false, "ms", displaycore.Range{Min: 220, Max: 420}},
		{"time-down-reverse", true, true, true, "ms", displaycore.Range{Min: 220, Max: 420}},
		{"seconds-same-samples", true, true, false, "s", displaycore.Range{Min: 180, Max: 380}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := displayAxisNavigation{
				axis: displaycore.Axis{ID: "time", Unit: tc.unit, BaseUnit: "ms", Data: displaycore.Range{Min: 0, Max: 1000}, View: displaycore.Range{Min: 200, Max: 400}, Down: tc.down, Reverse: tc.reverse},
				plot: RECT{Right: 201, Bottom: 201}, start: 100, vertical: tc.vertical, threshold: 4, quantum: 2,
			}
			if n.move(102) || n.dragging {
				t.Fatal("single click became a drag")
			}
			if !n.move(120) || n.candidate != tc.want {
				t.Fatalf("pan = %+v; want %+v", n.candidate, tc.want)
			}
			n.move(10000)
			if n.candidate.Max-n.candidate.Min != 200 || n.candidate.Min < 0 || n.candidate.Max > 1000 {
				t.Fatalf("limit changed span: %+v", n.candidate)
			}
		})
	}
}

func TestDisplayAxisPanUsesRealTraceRanks(t *testing.T) {
	n := displayAxisNavigation{
		axis: displaycore.Axis{ID: "crossline", Data: displaycore.Range{Min: 0, Max: 300}, View: displaycore.Range{Min: 100, Max: 201}},
		plot: RECT{Right: 301, Bottom: 101}, coords: []int32{100, 101, 200, 201}, start: 100, threshold: 4, quantum: 1,
	}
	if got := n.valueAt(100); got != 101 {
		t.Fatalf("trace rank 1 maps to %v", got)
	}
	if !n.move(200) || n.candidate != (displaycore.Range{Min: 1, Max: 102}) {
		t.Fatalf("physical coordinates were replaced with pixels: %+v", n.candidate)
	}
}

func testAxisNavigationSurface(t *testing.T) (uintptr, *displaySurfaceState, *int) {
	t.Helper()
	owner, o := testDisplaySurfaces(t)
	calls := new(int)
	x := displaycore.Axis{ID: "trace", Data: displaycore.Range{Min: 1, Max: 1001}, View: displaycore.Range{Min: 201, Max: 401}}
	y := displaycore.Axis{ID: "time", Unit: "ms", Data: displaycore.Range{Min: 0, Max: 1000}, View: displaycore.Range{Min: 100, Max: 500}, Down: true}
	e := &displaySurfaceState{order: 1, s: displaySurface{
		Identity: "a-v1", StatsVersion: "current", LiveViewport: true, Plot: RECT{Left: 80, Top: 40, Right: 281, Bottom: 241},
		Axes: []displaycore.Axis{x, y}, Regions: map[string]RECT{"trace": {Left: 80, Top: 241, Right: 281, Bottom: 275}, "time": {Left: 25, Top: 40, Right: 80, Bottom: 241}},
		Apply: func(s displaycore.Settings) error {
			*calls++
			if !s.Axes[0].ViewEdited || s.Axes[1].ViewEdited {
				t.Fatal("pan committed an unrelated axis")
			}
			if s.Axes[0].View != (displaycore.Range{Min: 181, Max: 381}) {
				t.Fatalf("committed wrong view: %+v", s.Axes[0])
			}
			return nil
		},
	}}
	o.surfaces["plot"] = e
	t.Cleanup(func() { delete(displayAxisNavigations, owner); delete(displayAxisDragEnd, owner) })
	return owner, e, calls
}

func TestDisplayAxisPanHitDoesNotStealImageOrReadOnlyAxes(t *testing.T) {
	owner, e, _ := testAxisNavigationSurface(t)
	if _, _, _, _, ok := displayNavigationHit(owner, 150, 120); ok {
		t.Fatal("image tools intercepted")
	}
	if _, a, _, vertical, ok := displayNavigationHit(owner, 150, 250); !ok || a.ID != "trace" || vertical {
		t.Fatal("horizontal axis not hittable")
	}
	if _, a, _, vertical, ok := displayNavigationHit(owner, 50, 120); !ok || a.ID != "time" || !vertical {
		t.Fatal("vertical axis not hittable")
	}
	e.s.Axes[0].StyleOnly = true
	if _, _, _, _, ok := displayNavigationHit(owner, 150, 250); ok {
		t.Fatal("style-only axis can change viewport")
	}
	e.s.Axes[0].StyleOnly = false
	e.s.Axes[0].HideAxis = true
	if _, _, _, _, ok := displayNavigationHit(owner, 150, 250); ok {
		t.Fatal("hidden axis retained drag target")
	}
}

func TestDisplayAxisPanPreviewsThenCommitsOnce(t *testing.T) {
	owner, e, calls := testAxisNavigationSurface(t)
	before := displayDefaults(e.s)
	e.saved = &before
	displayAxisNavigations[owner] = &displayAxisNavigation{key: "plot", identity: e.s.Identity, version: e.s.StatsVersion, axis: e.s.Axes[0], plot: e.s.Plot, start: 180, threshold: 4, quantum: 1, candidate: e.s.Axes[0].View}
	for _, x := range []int{181, 183, 188, 195, 200} {
		displayNavigationMove(owner, x, 250)
	}
	if *calls != 0 {
		t.Fatal("pointer motion started expensive viewport work")
	}
	if !reflect.DeepEqual(*e.saved, before) {
		t.Fatal("preview changed settings state")
	}
	if !displayNavigationEnd(owner, 200, 250) || *calls != 1 {
		t.Fatalf("commit count = %d", *calls)
	}
	if displayNavigationEnd(owner, 200, 250) || *calls != 1 {
		t.Fatal("duplicate release committed again")
	}
	if !reflect.DeepEqual(*e.saved, before) {
		t.Fatal("navigation overwrote display style/draft")
	}
	if !displayNavigationDoubleClick(owner, 200, 250) {
		t.Fatal("drag release allowed accidental double click")
	}
	if displayNavigationDoubleClick(owner, 200, 250) {
		t.Fatal("double click suppression lasted longer than one event")
	}
}

func TestDisplayAxisPanStaleAndCancellationNeverCommit(t *testing.T) {
	for _, mutation := range []string{"dataset", "range", "size", "version", "inactive", "cancel"} {
		t.Run(mutation, func(t *testing.T) {
			owner, e, calls := testAxisNavigationSurface(t)
			n := &displayAxisNavigation{key: "plot", identity: e.s.Identity, version: e.s.StatsVersion, axis: e.s.Axes[0], plot: e.s.Plot, start: 180, threshold: 4, quantum: 1, candidate: e.s.Axes[0].View}
			displayAxisNavigations[owner] = n
			displayNavigationMove(owner, 200, 250)
			switch mutation {
			case "dataset":
				e.s.Identity = "replacement"
			case "range":
				e.s.Axes[0].View = displaycore.Range{Min: 301, Max: 501}
			case "size":
				e.s.Plot.Right++
			case "version":
				e.s.StatsVersion = "new-values"
			case "inactive":
				e.order = 0
			case "cancel":
				displayNavigationCancel(owner)
			}
			displayNavigationEnd(owner, 200, 250)
			if *calls != 0 || displayAxisNavigations[owner] != nil {
				t.Fatal("stale/cancelled gesture changed viewport or retained capture state")
			}
		})
	}
}

func TestDisplayAxisClickAndDoubleClickRemainAvailable(t *testing.T) {
	owner, e, calls := testAxisNavigationSurface(t)
	displayAxisNavigations[owner] = &displayAxisNavigation{key: "plot", identity: e.s.Identity, version: e.s.StatsVersion, axis: e.s.Axes[0], plot: e.s.Plot, start: 180, threshold: 4, candidate: e.s.Axes[0].View}
	displayNavigationEnd(owner, 181, 250)
	if *calls != 0 || displayNavigationDoubleClick(owner, 181, 250) {
		t.Fatal("ordinary double click blocked")
	}
	displayAxisDragEnd[owner] = displayAxisDragCompletion{x: 181, y: 250, until: time.Now().Add(-time.Second)}
	if displayNavigationDoubleClick(owner, 181, 250) {
		t.Fatal("expired drag blocked double click")
	}
}
