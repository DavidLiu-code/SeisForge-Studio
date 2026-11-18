//go:build windows

package main

import (
	"context"
	"fmt"
	"math"
	"sort"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
	geometrycore "github.com/DavidLiu-code/SeisForge-Studio/internal/geometry"
	pseudo3dcore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudo3d"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

const displayCrookedMapKey = "crooked.map"
const displayCrookedSectionKey = "crooked.section"

func crookedHonestSectionCoordinates() (lo, hi float64, descending bool, id, title string) {
	g := crookedState.geometry
	if g == nil || len(g.TraceIndices) == 0 {
		return
	}
	switch crookedState.axisMode {
	case crookedAxisDistance:
		return g.Distance[0], g.Distance[len(g.Distance)-1], false, "distance", "距离（坐标单位）"
	case crookedAxisCDP:
		if g.CDPMonotonic() {
			a, b := float64(g.CDP[0]), float64(g.CDP[len(g.CDP)-1])
			return math.Min(a, b), math.Max(a, b), a > b, "cdp", "CDP"
		}
		return 1, float64(len(g.TraceIndices)), false, "trace", "显示道序（CDP 非单调）"
	default:
		return 1, float64(len(g.TraceIndices)), false, "trace", "显示道序"
	}
}

func crookedDisplaySectionAxes() (x, y displaycore.Axis) {
	if crookedState.geometry == nil {
		return
	}
	d0, d1, desc, id, title := crookedHonestSectionCoordinates()
	if id == "" {
		return
	}
	a, b := crookedState.viewStart, crookedState.viewEnd
	if desc {
		a, b = 1-b, 1-a
	}
	lo, hi := d0+a*(d1-d0), d0+b*(d1-d0)
	x = remainingAxis(id, title, "", remainingRange(d0, d1), remainingRange(lo, hi), false)
	x.Reverse = desc
	if crookedState.dataset != nil {
		md := crookedState.dataset.Metadata
		dt := float64(md.SampleIntervalUS) / 1000
		y = remainingAxis("time", "时间", "ms", remainingRange(0, float64(md.SamplesPerTrace-1)*dt), remainingRange(float64(crookedState.sampleStart)*dt, float64(crookedState.sampleEnd)*dt), true)
	}
	return
}

func crookedDisplayMapAxes(view geometrycore.Bounds) (x, y displaycore.Axis) {
	b := crookedProjectBounds()
	return remainingAxis("x", "X", "", remainingRange(b.XMin, b.XMax), remainingRange(view.XMin, view.XMax), false),
		remainingAxis("y", "Y", "", remainingRange(b.YMin, b.YMax), remainingRange(view.YMin, view.YMax), false)
}

func reverseFraction(value float64, reverse bool) float64 {
	if reverse {
		return 1 - value
	}
	return value
}

func crookedDisplayReverse(section bool) (bool, bool) {
	var x, y displaycore.Axis
	key := displayCrookedMapKey
	if section {
		x, y = crookedDisplaySectionAxes()
		key = displayCrookedSectionKey
	} else {
		x.ID, y.ID = "x", "y"
	}
	return displayAxisFor(crookedHwnd, key, x).Reverse != x.Reverse, displayAxisFor(crookedHwnd, key, y).Reverse != y.Reverse
}

func crookedApplyDisplaySection(s displaycore.Settings) error {
	x, y := crookedDisplaySectionAxes()
	a, b, p, q := crookedState.viewStart, crookedState.viewEnd, crookedState.sampleStart, crookedState.sampleEnd
	for _, v := range s.Axes {
		if !v.ViewEdited {
			continue
		}
		var base displaycore.Range
		switch v.ID {
		case x.ID:
			base = x.Data
		case y.ID:
			base = y.Data
		default:
			continue
		}
		if !base.Valid() || v.View.Min < base.Min || v.View.Max > base.Max {
			return fmt.Errorf("坐标范围超出弯线剖面数据边界")
		}
		switch v.ID {
		case x.ID:
			a, b = base.Fraction(v.View.Min), base.Fraction(v.View.Max)
			if x.Reverse {
				a, b = 1-b, 1-a
			}
		case y.ID:
			dt := float64(crookedState.dataset.Metadata.SampleIntervalUS) / 1000
			p, q = int(math.Ceil(v.View.Min/dt-1e-10)), int(math.Floor(v.View.Max/dt+1e-10))
		}
	}
	if p > q {
		return fmt.Errorf("所选坐标范围内没有有效样点")
	}
	if a == crookedState.viewStart && b == crookedState.viewEnd && p == crookedState.sampleStart && q == crookedState.sampleEnd {
		crookedRecolorDisplay()
		invalidateCrookedBase()
		return nil
	}
	crookedState.viewStart, crookedState.viewEnd, crookedState.sampleStart, crookedState.sampleEnd = a, b, p, q
	saveCrookedActiveLineView()
	startCrookedRender()
	invalidateCrookedBase()
	return nil
}

func crookedRecolorDisplay() {
	if len(crookedState.displayValues) == 0 {
		return
	}
	m := remainingMapping(crookedState.stats.MapMin, crookedState.stats.MapMax, agc, remainingLegacyPalette(crookedState.paletteIndex))
	m = remainingColor(crookedHwnd, displayCrookedSectionKey, m)
	crookedState.bgra = remainingRaster(crookedState.displayValues, m)
}

func crookedDrawDisplaySection(dc uintptr, r RECT) bool {
	x, y := crookedDisplaySectionAxes()
	if x.ID == "" || y.ID == "" {
		return false
	}
	color := remainingMapping(crookedState.stats.MapMin, crookedState.stats.MapMax, agc, remainingLegacyPalette(crookedState.paletteIndex))
	bar := remainingBar(r)
	path, first, last, normalize := crookedState.path, crookedState.sampleStart, crookedState.sampleEnd, agc
	positions, _ := crookedAxisPositions(crookedState.geometry, crookedState.axisMode)
	p0, p1 := crookedVisibleAxisRange(positions)
	l := sort.Search(len(positions), func(i int) bool { return positions[i] >= p0 })
	h := sort.Search(len(positions), func(i int) bool { return positions[i] > p1 })
	if l == h && l < len(positions) {
		h++
	}
	traces := crookedState.geometry.TraceIndices[l:h]
	displayRegister(crookedHwnd, displayCrookedSectionKey, displaySurface{Title: "弯线剖面", Identity: path, LiveViewport: true, StatsVersion: fmt.Sprintf("%d:%d:%d:%d:%t", l, h, first, last, normalize),
		Axes: []displaycore.Axis{x, y}, Regions: displayAxisRegions(r, x.ID, y.ID), Color: &color, ColorDomain: remainingColorDomain(agc), Plot: r, ColorRegion: bar,
		ReadStats: func(ctx context.Context) (displaycore.Stats, error) {
			var st displaycore.Stats
			f, err := segy.Open(path)
			if err != nil {
				return st, err
			}
			defer f.Close()
			err = remainingTraceStats(ctx, &st, f, traces, first, last, normalize)
			return st, err
		}, Apply: crookedApplyDisplaySection})
	if len(crookedState.bgra) > 0 {
		displayDrawColorbar(dc, bar, remainingColor(crookedHwnd, displayCrookedSectionKey, color))
	}
	if _, edited := displayCommitted(crookedHwnd, displayCrookedSectionKey); edited {
		a, b := displayAxisFor(crookedHwnd, displayCrookedSectionKey, x), displayAxisFor(crookedHwnd, displayCrookedSectionKey, y)
		a.View, b.View = x.View, y.View
		displayDrawAxes(dc, r, a, b)
		return true
	}
	return false
}

func crookedDrawDisplayMap(dc uintptr, transform crookedMapTransform) {
	x, y := crookedDisplayMapAxes(transform.bounds)
	a, b := transform.toPixel(x.View.Min, y.View.Min)
	c, d := transform.toPixel(x.View.Max, y.View.Max)
	r := RECT{Left: int32(minInt(a, c)), Right: int32(maxInt(a, c)), Top: int32(minInt(b, d)), Bottom: int32(maxInt(b, d))}
	displayRegister(crookedHwnd, displayCrookedMapKey, displaySurface{Title: "弯线平面图", Identity: fmt.Sprintf("%d", crookedState.workspaceGeneration), LiveViewport: true,
		Axes: []displaycore.Axis{x, y}, Regions: displayAxisRegions(r, "x", "y"), Plot: r, Apply: func(s displaycore.Settings) error {
			x, y := crookedDisplayMapAxes(crookedMapViewBounds())
			change := false
			for _, v := range s.Axes {
				if !v.ViewEdited {
					continue
				}
				a := &x
				if v.ID == "y" {
					a = &y
				}
				if !a.Data.Valid() || v.View.Min < a.Data.Min || v.View.Max > a.Data.Max {
					return fmt.Errorf("坐标范围超出测线项目边界")
				}
				a.View = v.View
				change = true
			}
			if change {
				crookedState.geometryViewport = pseudo3dcore.XYRange{XMin: x.View.Min, XMax: x.View.Max, YMin: y.View.Min, YMax: y.View.Max, Valid: true}
			}
			invalidateCrookedBase()
			return nil
		}})
	aX, aY := displayAxisFor(crookedHwnd, displayCrookedMapKey, x), displayAxisFor(crookedHwnd, displayCrookedMapKey, y)
	aX.View, aY.View = x.View, y.View
	displayDrawAxes(dc, r, aX, aY)
}
