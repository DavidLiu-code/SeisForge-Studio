//go:build windows

package main

import (
	"context"
	"fmt"
	"math"
	"strings"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
)

const prestackGeometryDisplayKey = "prestack/geometry"

// makeChartAxis preserves the actual extent for the editor and gives a
// constant-valued plot a usable (but not fabricated data) viewport.
func makeChartAxis(id, title, unit string, lo, hi float64, down bool) displaycore.Axis {
	data := displaycore.Range{Min: lo, Max: hi}
	return displaycore.Axis{ID: id, Title: title, BaseUnit: unit, Unit: unit,
		Data: data, View: data.Expanded(), Down: down}
}

func registerChartAxes(owner uintptr, key, title string, identity func() string, plot RECT, x, y displaycore.Axis) (displaycore.Axis, displaycore.Axis) {
	if owner == 0 {
		return x, y
	}
	captured := identity()
	displayRegister(owner, key, displaySurface{Title: title, Identity: captured, Axes: []displaycore.Axis{x, y},
		Regions: displayAxisRegions(plot, x.ID, y.ID), Plot: plot, Apply: func(displaycore.Settings) error {
			if identity() != captured || !chartDisplayIsLive(key) {
				return fmt.Errorf("数据已更新，请重新选择编辑对象")
			}
			return nil // display-only; existing chart values and FFT are unchanged
		}})
	return displayAxisFor(owner, key, x), displayAxisFor(owner, key, y)
}

func chartDisplayIsLive(key string) bool {
	switch key {
	case "spectrum/main":
		return !spectrumDraft.showRatio && len(spectrumData.A.Freq) > 0
	case "spectrum/ratio":
		return spectrumDraft.showRatio && len(spectrumData.DiffFreq) > 0
	case "trace-analysis/waveform", "trace-analysis/spectrum":
		return traceAnalysisPage == 2 && traceAnalysisCurrent != nil
	}
	return prestackState.page == 4 && prestackState.index != nil
}

func qcDisplayBar(plot RECT, xAxis, yAxis displaycore.Axis, lo, hi float64, count int, selected bool) (RECT, bool) {
	if count <= 0 || hi < xAxis.View.Min || lo > xAxis.View.Max || yAxis.View.Max < 0 || yAxis.View.Min > float64(count) {
		return RECT{}, false
	}
	if selected && lo < hi {
		hi = (displaycore.Range{Min: lo, Max: hi}).At(.5)
	}
	// The immutable bin's metadata determines bar width/position. View
	// clipping must not recalculate either bins or their category counts.
	lo, hi = math.Max(lo, xAxis.View.Min), math.Min(hi, xAxis.View.Max)
	if hi < lo {
		return RECT{}, false
	}
	x0 := xAxis.Pixel(lo, int(plot.Left), int(plot.Right))
	x1 := xAxis.Pixel(hi, int(plot.Left), int(plot.Right))
	y0 := chartAxisY(yAxis, math.Max(0, yAxis.View.Min), plot)
	y1 := chartAxisY(yAxis, math.Min(float64(count), yAxis.View.Max), plot)
	left, right := min(x0, x1), max(x0, x1)
	// Constant distributions still have a bin at the actual canonical value.
	// Give that bin a small line width without stretching its data domain.
	if right == left {
		left, right = max(left-2, int(plot.Left)), min(right+2, int(plot.Right)+1)
	} else if right-left > 2 {
		right--
	}
	top, bottom := min(y0, y1), max(y0, y1)
	return RECT{Left: int32(left), Top: int32(top), Right: int32(max(left+1, right)), Bottom: int32(max(top+1, bottom))}, true
}

func chartAxisY(a displaycore.Axis, value float64, plot RECT) int {
	if a.Down {
		return a.Pixel(value, int(plot.Top), int(plot.Bottom))
	}
	return a.Pixel(value, int(plot.Bottom), int(plot.Top))
}

// chartClipSegment clips the data segment, not its separately clamped
// endpoints. This avoids drawing false strokes along a range boundary while
// retaining segments whose two endpoints both lie outside the visible plot.
func chartClipSegment(xAxis, yAxis displaycore.Axis, x0, y0, x1, y1 float64, plot RECT) (int, int, int, int, bool) {
	x0, x1 = xAxis.Fraction(x0), xAxis.Fraction(x1)
	y0, y1 = yAxis.Fraction(y0), yAxis.Fraction(y1)
	if !yAxis.Down {
		y0, y1 = 1-y0, 1-y1
	}
	for _, v := range []float64{x0, y0, x1, y1} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, 0, 0, 0, false
		}
	}
	a, b := 0.0, 1.0
	dx, dy := x1-x0, y1-y0
	if math.IsInf(dx, 0) || math.IsInf(dy, 0) {
		return 0, 0, 0, 0, false
	}
	clip := func(p, q float64) bool {
		if p == 0 {
			return q >= 0
		}
		t := q / p
		if p < 0 {
			a = math.Max(a, t)
		} else {
			b = math.Min(b, t)
		}
		return a <= b
	}
	if !clip(-dx, x0) || !clip(dx, 1-x0) || !clip(-dy, y0) || !clip(dy, 1-y0) {
		return 0, 0, 0, 0, false
	}
	point := func(t float64) (int, int) {
		x := clampFloat(x0+t*dx, 0, 1)
		y := clampFloat(y0+t*dy, 0, 1)
		return int(plot.Left) + int(math.Round(x*float64(plot.Right-plot.Left))), int(plot.Top) + int(math.Round(y*float64(plot.Bottom-plot.Top)))
	}
	px0, py0 := point(a)
	px1, py1 := point(b)
	return px0, py0, px1, py1, true
}

func spectrumChartIdentity() string {
	key := viewFilterCurrentKey()
	return fmt.Sprintf("%q:%d:%q:%d", key.a, key.aVersion, key.b, key.bVersion)
}

func traceChartIdentity() string {
	if traceAnalysisCurrent == nil {
		return ""
	}
	var b strings.Builder
	for _, file := range traceAnalysisCurrent.files {
		fmt.Fprintf(&b, "%q:%q:%d:%d;", file.target.Role, file.target.Path, file.info.FileSize, file.sourceModifiedNS)
	}
	return b.String()
}

func prestackChartIdentity() string {
	// Reopening the same path must invalidate a draft made against the old
	// dataset/index; coordinate edits and gather navigation do not.
	return fmt.Sprintf("%d:%p:%p", prestackState.workspaceGeneration, prestackState.dataset, prestackState.index)
}

func prestackGeometryPlot(outer RECT, x, y displaycore.Range) RECT {
	// Keep the map aspect ratio, but reserve a separate axis gutter. The
	// rendered plot, hit-test clip and axis layout share this exact rectangle.
	outer.Bottom -= 32
	w, h := rectWH(outer)
	if w < 20 || h < 20 || !x.Valid() || !y.Valid() {
		return RECT{}
	}
	dx, dy := x.Max-x.Min, y.Max-y.Min
	if math.IsInf(dx, 0) || math.IsInf(dy, 0) {
		return RECT{}
	}
	scale := math.Min(float64(w)/dx, float64(h)/dy)
	plotWidth := clampInt(int(math.Round(dx*scale)), 1, w)
	plotHeight := clampInt(int(math.Round(dy*scale)), 1, h)
	left, top := outer.Left+int32(w-plotWidth)/2, outer.Top+int32(h-plotHeight)/2
	return RECT{Left: left, Top: top, Right: left + int32(plotWidth), Bottom: top + int32(plotHeight)}
}

func prestackGeometryAxes() (plot RECT, nativeX, nativeY, x, y displaycore.Axis) {
	idx, bounds := prestackState.index, prestackState.mapBounds
	if idx == nil || !bounds.Valid {
		return
	}
	nativeX = makeChartAxis("x", "X", "", idx.Bounds.XMin, idx.Bounds.XMax, false)
	nativeY = makeChartAxis("y", "Y", "", idx.Bounds.YMin, idx.Bounds.YMax, false)
	nativeX.View = (displaycore.Range{Min: bounds.XMin, Max: bounds.XMax}).Expanded()
	nativeY.View = (displaycore.Range{Min: bounds.YMin, Max: bounds.YMax}).Expanded()
	outer := prestackSceneRect()
	if prestackState.mapLayers[4] {
		settings, _ := displayCommitted(prestackHwnd, prestackGeometryDisplayKey)
		if settings.Color == nil || !settings.Color.Hidden {
			outer.Right -= 78
		}
	}
	plot = prestackGeometryPlot(outer, nativeX.View, nativeY.View)
	x, y = displayAxisFor(prestackHwnd, prestackGeometryDisplayKey, nativeX), displayAxisFor(prestackHwnd, prestackGeometryDisplayKey, nativeY)
	// Range is committed to the native viewport; zoom/pan/reset remain the
	// single authority for the next frame rather than fighting a sticky draft.
	x.View, y.View = nativeX.View, nativeY.View
	return
}

func prestackGeometryFoldDefaults(maxFold int) displaycore.ColorMapping {
	return displaycore.ColorMapping{Range: displaycore.Range{Min: 0, Max: float64(maxFold)}, Title: "Fold", Unit: "道", Hidden: true,
		Stops: []displaycore.GradientStop{
			{Position: 0, Color: displaycore.RGB{R: 40, G: 220, B: 220}},
			{Position: 1, Color: displaycore.RGB{R: 255, G: 120}},
		}}
}

func prestackGeometryFoldColor() displaycore.ColorMapping {
	if s, _ := displayCommitted(prestackHwnd, prestackGeometryDisplayKey); s.Color != nil {
		return *s.Color
	}
	return prestackGeometryFoldDefaults(prestackGeometryMaxFold(prestackState.index))
}

func prestackGeometryMaxFold(idx *prestackcore.PrestackIndex) int {
	if idx == nil {
		return 1
	}
	return max(1, idx.QualityStats().Fold.Max)
}

func registerPrestackGeometryChart(plot, outer RECT, x, y displaycore.Axis, idx *prestackcore.PrestackIndex) {
	identity := prestackChartIdentity()
	s := displaySurface{Title: "Geometry 地图", Identity: identity, LiveViewport: true,
		Axes: []displaycore.Axis{x, y}, Regions: displayAxisRegions(plot, x.ID, y.ID), Plot: plot, Apply: func(settings displaycore.Settings) error {
			if prestackChartIdentity() != identity {
				return fmt.Errorf("数据已更新，请重新选择编辑对象")
			}
			return commitPrestackGeometryAxes(settings)
		}}
	if prestackState.mapLayers[4] {
		m := prestackGeometryFoldDefaults(prestackGeometryMaxFold(idx))
		s.Color = &m
		s.ColorRegion = RECT{Left: outer.Right - 72, Top: plot.Top, Right: outer.Right, Bottom: plot.Bottom}
		s.StatsVersion = fmt.Sprintf("%s:%v:%v", s.Identity, x.View, y.View)
		s.ReadStats = prestackGeometryFoldStats(idx, x.View, y.View)
	}
	displayRegister(prestackHwnd, prestackGeometryDisplayKey, s)
}

func drawPrestackGeometryFoldBar(dc uintptr, outer, plot RECT) {
	if prestackState.mapLayers[4] {
		displayDrawColorbar(dc, RECT{Left: outer.Right - 72, Top: plot.Top, Right: outer.Right, Bottom: plot.Bottom}, prestackGeometryFoldColor())
	}
}

func prestackGeometryFoldStats(idx *prestackcore.PrestackIndex, x, y displaycore.Range) func(context.Context) (displaycore.Stats, error) {
	return func(ctx context.Context) (displaycore.Stats, error) {
		var s displaycore.Stats
		for i, b := range idx.Bins {
			if i%1024 == 0 {
				if err := ctx.Err(); err != nil {
					return s, err
				}
			}
			if b.HasCoordinates && b.X >= x.Min && b.X <= x.Max && b.Y >= y.Min && b.Y <= y.Max {
				s.Add(float64(b.Fold))
			}
		}
		return s, nil
	}
}

func prestackGeometryDisplayTransform(plot RECT, x, y displaycore.Axis) prestackcore.AcquisitionScreenTransform {
	x0, x1 := float64(plot.Left), float64(plot.Right)
	y0, y1 := float64(plot.Bottom), float64(plot.Top)
	if x.Reverse {
		x0, x1 = x1, x0
	}
	if y.Reverse != y.Down {
		y0, y1 = y1, y0
	}
	sx, sy := (x1-x0)/(x.View.Max-x.View.Min), (y1-y0)/(y.View.Max-y.View.Min)
	return prestackcore.AcquisitionScreenTransform{OriginX: x0 - x.View.Min*sx, OriginY: y0 - y.View.Min*sy, ScaleX: sx, ScaleY: sy}
}

func commitPrestackGeometryAxes(settings displaycore.Settings) error {
	if prestackState.page != 1 || prestackState.index == nil {
		return fmt.Errorf("当前 Geometry 视图不可用")
	}
	bounds := prestackState.mapBounds
	changed := false
	for _, axis := range settings.Axes {
		if !axis.ViewEdited {
			continue
		}
		if !axis.View.Valid() {
			return fmt.Errorf("Geometry 坐标范围无效")
		}
		if span := axis.View.Max - axis.View.Min; math.IsInf(span, 0) || span <= 0 || math.IsInf(65536/span, 0) {
			return fmt.Errorf("Geometry 坐标跨度超出可显示范围")
		}
		switch axis.ID {
		case "x":
			bounds.XMin, bounds.XMax = axis.View.Min, axis.View.Max
			changed = true
		case "y":
			bounds.YMin, bounds.YMax = axis.View.Min, axis.View.Max
			changed = true
		}
	}
	if !changed {
		return nil
	}
	prestackState.mapBounds = bounds
	invalidatePrestackScene()
	return nil
}
