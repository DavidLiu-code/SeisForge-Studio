//go:build windows

package main

import (
	"context"
	"fmt"
	"math"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
	pseudo3dcore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudo3d"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

const (
	displayVolumeKey = "volume.coordinates"
	displayPseudoKey = "pseudo.coordinates"
)

// -1 is the normal single volume; 0/1/2 are the compare surfaces being drawn.
var displayVolumeDrawingSide = -1

func volumeDisplaySceneKey() string {
	if displayVolumeDrawingSide >= 0 {
		return fmt.Sprintf("%s.%d", displayVolumeKey, displayVolumeDrawingSide)
	}
	if volumeCompareMode && volumeScenes[0].valid && volumeScenes[1].valid {
		return fmt.Sprintf("%s.%d", displayVolumeKey, volumeActiveSide)
	}
	return displayVolumeKey
}

func volumeDisplaySliceKey(panel int) string {
	return fmt.Sprintf("%s.slice.%d", volumeDisplaySceneKey(), panel)
}

func volumeDisplaySliceAxes(panel int) (x, y displaycore.Axis) {
	axes := volumeDisplayAxes()
	if len(axes) < 3 {
		return
	}
	if panel == 0 {
		x, y = axes[1], axes[2]
	} else if panel == 1 {
		x, y = axes[0], axes[2]
	} else {
		x, y = axes[1], axes[0]
	}
	y.Down = true
	return
}

func drawVolumeDisplaySlice(dc uintptr, r RECT, panel int) bool {
	x, y := volumeDisplaySliceAxes(panel)
	if x.ID == "" || y.ID == "" {
		return false
	}
	key := volumeDisplaySliceKey(panel)
	displayRegister(volumeHwnd, key, displaySurface{Title: []string{"Inline 切片", "Crossline 切片", "时间切片"}[panel], Identity: volumePath,
		Axes: []displaycore.Axis{x, y}, Plot: r, Regions: displayAxisRegions(r, x.ID, y.ID), Apply: func(displaycore.Settings) error { invalidateVolumeBase(); return nil }})
	// The same amplitude configuration drives all three orthogonal planes;
	// the legend's own entry edits that shared configuration whether these
	// planes are tiled, focused or shown in perspective.
	if panel == 2 || volumeFocusPanel >= 0 {
		volumeDisplayRegister(dc, r, nil)
	}
	if _, edited := displayCommitted(volumeHwnd, key); edited {
		displayDrawAxes(dc, r, displayAxisFor(volumeHwnd, key, x), displayAxisFor(volumeHwnd, key, y))
		return true
	}
	return false
}

func remainingRange(lo, hi float64) displaycore.Range { return displaycore.Range{Min: lo, Max: hi} }

func remainingMapping(lo, hi float64, normalized bool, palette [256]rgb) displaycore.ColorMapping {
	if !finiteFloat(lo) || !finiteFloat(hi) || lo >= hi {
		lo, hi = -1, 1
	}
	title := "振幅"
	if normalized {
		title = "归一化显示振幅"
	}
	return displaycore.ColorMapping{Range: remainingRange(lo, hi), Title: title, Hidden: true, Stops: displaycore.LegacyStops(func(index int) displaycore.RGB {
		p := palette[index]
		return displaycore.RGB{R: p.r, G: p.g, B: p.b}
	})}
}

func remainingColor(owner uintptr, key string, fallback displaycore.ColorMapping) displaycore.ColorMapping {
	return displayEffectiveLegend(owner, key, fallback)
}

func remainingBar(plot RECT) RECT {
	return RECT{Left: plot.Right + 5, Top: plot.Top + 6, Right: plot.Right + 68, Bottom: plot.Bottom - 8}
}

func remainingRaster(values []float64, mapping displaycore.ColorMapping) []byte {
	var colors [256]displaycore.RGB
	for i := range colors {
		colors[i] = mapping.ColorAtPosition(float64(i) / 255)
	}
	dst := make([]byte, len(values)*4)
	for i, v := range values {
		if !finiteFloat(v) {
			dst[i*4], dst[i*4+1], dst[i*4+2] = 245, 245, 245
			continue
		}
		f := 0.0
		if v >= mapping.Range.Max {
			f = 1
		} else if v > mapping.Range.Min {
			f = mapping.Range.Fraction(v)
		}
		if !finiteFloat(f) {
			continue
		}
		c := colors[clampInt(int(math.Round(f*255)), 0, 255)]
		dst[i*4], dst[i*4+1], dst[i*4+2] = c.B, c.G, c.R
	}
	return dst
}

func remainingColorDomain(normalize bool) string {
	if normalize {
		return "agc-rms"
	}
	return "amplitude"
}

// remainingTraceStats walks the selected logical traces, not the texture or
// screen raster. The mapped domain mirrors the renderer's per-window RMS AGC.
func remainingTraceStats(ctx context.Context, stats *displaycore.Stats, reader *segy.File, traces []int64, first, last int, normalize bool) error {
	for _, trace := range traces {
		if err := ctx.Err(); err != nil {
			return err
		}
		x, err := reader.ReadTraceWindow(trace, first, last)
		if err != nil {
			return err
		}
		remainingCollectTrace(stats, x, nil, normalize)
	}
	return nil
}

func remainingCollectTrace(stats *displaycore.Stats, a, b []float64, normalize bool) {
	valueAt := func(i int) float64 {
		if b != nil {
			return a[i] - b[i]
		}
		return a[i]
	}
	rms := 1.0
	if normalize {
		var sum float64
		var n int
		for i := range a {
			v := valueAt(i)
			if finiteFloat(v) {
				sum += v * v
				n++
			}
		}
		if n > 0 && sum > 0 {
			rms = math.Sqrt(sum / float64(n))
		}
	}
	for i := range a {
		value := valueAt(i)
		stats.Add(value)
		if normalize && finiteFloat(value) {
			stats.AddMapped(value / rms)
		}
	}
}

func projectedDisplayAxis(id, title, unit string, lo, hi float64) displaycore.Axis {
	r := remainingRange(lo, hi)
	return displaycore.Axis{ID: id, Title: title, Unit: unit, Data: r, View: r, StyleOnly: true}
}

func projectedRulerHit(x0, y0, x1, y1 int) RECT {
	// A tight rectangle over the center of the projected ruler, rather than a
	// box around the entire diagonal that would steal clicks from the image.
	x, y := int32((x0+x1)/2), int32((y0+y1)/2)
	return RECT{Left: x - 23, Top: y - 15, Right: x + 24, Bottom: y + 16}
}

// projectedDisplayRuler uses the scene projection for every coordinate;
// directly interpolating projected endpoints would misplace intermediate
// tick marks when perspective is enabled.
func projectedDisplayRuler(dc uintptr, a displaycore.Axis, p func(float64) (int, int)) RECT {
	x0, y0 := p(0)
	x1, y1 := p(1)
	if a.HideAxis || math.Hypot(float64(x1-x0), float64(y1-y0)) < 16 {
		return RECT{}
	}
	l := math.Hypot(float64(x1-x0), float64(y1-y0))
	nx, ny := float64(y1-y0)/l, float64(x0-x1)/l
	drawHomeLine(dc, x0, y0, x1, y1, rgbRef(84, 98, 114), 1)
	for _, v := range displaycore.Ticks(a.View, a.Step, max(2, min(7, int(l)/74))) {
		x, y := p(a.Fraction(v))
		drawHomeLine(dc, x, y, x+int(5*nx), y+int(5*ny), rgbRef(84, 98, 114), 1)
		if !a.HideLabels {
			x += int(15 * nx)
			y += int(15 * ny)
			drawAxisText(dc, displayAxisLabel(a, v), x-34, y-9, x+35, y+11, DT_CENTER|0x8000)
		}
	}
	x, y := p(.5)
	if a.HideTitle {
		return projectedRulerHit(x0, y0, x1, y1)
	}
	x += int(25 * nx)
	y += int(25 * ny)
	drawAxisText(dc, displayAxisTitle(a), x-64, y-10, x+65, y+12, DT_CENTER|0x8000)
	return RECT{Left: int32(x - 63), Top: int32(y - 11), Right: int32(x + 65), Bottom: int32(y + 11)}
}

func volumeDisplayAxes() []displaycore.Axis {
	if volumeG == nil {
		return nil
	}
	b := volumeG.Bounds()
	out := []displaycore.Axis{
		projectedDisplayAxis("inline", "Inline", "", float64(b.InlineMin), float64(b.InlineMax)),
		projectedDisplayAxis("crossline", "Crossline", "", float64(b.CrosslineMin), float64(b.CrosslineMax)),
	}
	if volumeF != nil {
		dt := float64(volumeF.Info.SampleIntervalUS) / 1000
		out = append(out, projectedDisplayAxis("time", "时间", "ms", float64(volumeSampleMin)*dt, float64(volumeSampleMax)*dt))
	}
	return out
}

func volumeDisplayStatistics() func(context.Context) (displaycore.Stats, error) {
	path, g, il, xl := volumePath, volumeG, volumeIL, volumeXL
	first, last, sample, normalize := volumeSampleMin, volumeSampleMax, volumeSample, agc
	return func(ctx context.Context) (displaycore.Stats, error) {
		var stats displaycore.Stats
		reader, err := segy.Open(path)
		if err != nil {
			return stats, err
		}
		defer reader.Close()
		seen := map[int64]bool{}
		for axis, coord := range []int32{il, xl} {
			traces, _, _, err := g.LineTraceNumbers(axis, coord)
			if err != nil {
				return stats, err
			}
			for _, trace := range traces {
				if !seen[trace] {
					if err := remainingTraceStats(ctx, &stats, reader, []int64{trace}, first, last, normalize); err != nil {
						return stats, err
					}
					seen[trace] = true
				}
			}
		}
		for _, trace := range g.TraceNumbers {
			if seen[trace] && sample >= first && sample <= last {
				continue
			}
			if err := ctx.Err(); err != nil {
				return stats, err
			}
			v, err := reader.ReadSample(trace, sample)
			if err != nil {
				return stats, err
			}
			stats.Add(v)
			if normalize {
				stats.AddMapped(v) // Time Slice is not AGC-normalized.
			}
		}
		return stats, nil
	}
}

func volumeDisplayDifferenceStatistics() func(context.Context) (displaycore.Stats, error) {
	a, b := volumeScenes[0], volumeScenes[1]
	return func(ctx context.Context) (displaycore.Stats, error) {
		var stats displaycore.Stats
		if a.g == nil || b.g == nil {
			return stats, nil
		}
		fa, err := segy.Open(a.path)
		if err != nil {
			return stats, err
		}
		defer fa.Close()
		fb, err := segy.Open(b.path)
		if err != nil {
			return stats, err
		}
		defer fb.Close()
		x0, x1 := a.sampleMin, min(a.sampleMax, fa.Info.SamplesPerTrace-1, fb.Info.SamplesPerTrace-1)
		seen := map[int64]bool{}
		if x0 <= x1 {
			for axis, coord := range []int32{a.il, a.xl} {
				indicesA, coordsA, lineA, eA := a.g.LineTraceNumbers(axis, coord)
				indicesB, coordsB, lineB, eB := b.g.LineTraceNumbers(axis, coord)
				if eA != nil || eB != nil || lineA != lineB {
					continue
				}
				pairsA, pairsB, _ := pairLineTracesByCoord(indicesA, coordsA, indicesB, coordsB)
				for i, ta := range pairsA {
					if err := ctx.Err(); err != nil {
						return stats, err
					}
					if seen[ta] {
						continue
					}
					x, err := fa.ReadTraceWindow(ta, x0, x1)
					if err != nil {
						return stats, err
					}
					y, err := fb.ReadTraceWindow(pairsB[i], x0, x1)
					if err != nil {
						return stats, err
					}
					remainingCollectTrace(&stats, x, y, false)
					seen[ta] = true
				}
			}
		}
		if a.sample < 0 || a.sample >= fa.Info.SamplesPerTrace || a.sample >= fb.Info.SamplesPerTrace {
			return stats, nil
		}
		for i, ta := range a.g.TraceNumbers {
			if err := ctx.Err(); err != nil {
				return stats, err
			}
			if seen[ta] && a.sample >= x0 && a.sample <= x1 {
				continue
			}
			if i >= len(a.g.RowOfTrace) || i >= len(a.g.ColOfTrace) {
				break
			}
			r, c := int(a.g.RowOfTrace[i]), int(a.g.ColOfTrace[i])
			if r < 0 || r >= len(a.g.InlineValues) || c < 0 || c >= len(a.g.CrosslineValues) {
				continue
			}
			tb, ok := b.g.TraceAt(a.g.InlineValues[r], a.g.CrosslineValues[c])
			if !ok || tb < 0 {
				continue
			}
			x, err := fa.ReadSample(ta, a.sample)
			if err != nil {
				return stats, err
			}
			y, err := fb.ReadSample(tb, a.sample)
			if err != nil {
				return stats, err
			}
			stats.Add(x - y)
		}
		return stats, nil
	}
}

func volumeDisplayRegister(hdc uintptr, area RECT, regions map[string]RECT) {
	axes := volumeDisplayAxes()
	if len(axes) == 0 {
		return
	}
	bar := remainingBar(area)
	color := remainingMapping(volumeTimeMin, volumeTimeMax, agc, volumePalette())
	if agc {
		color.Title = "显示值（剖面已 AGC）"
	}
	var mapping *displaycore.ColorMapping
	var stats func(context.Context) (displaycore.Stats, error)
	key, side := volumeDisplaySceneKey(), displayVolumeDrawingSide
	identity, domain := volumePath, remainingColorDomain(agc)
	if side < 2 {
		mapping = &color
		stats = volumeDisplayStatistics()
	} else if len(volumeResidualInline.bgra) > 0 || len(volumeResidualCrossline.bgra) > 0 || len(volumeResidualTime.bgra) > 0 {
		identity = volumeScenes[0].path + "\x00" + volumeScenes[1].path
		domain = remainingColorDomain(false)
		color = remainingMapping(volumeResidualDisplayRange[0], volumeResidualDisplayRange[1], false, volumePalette())
		color.Title = "差值振幅"
		mapping, stats = &color, volumeDisplayDifferenceStatistics()
	}
	displayRegister(volumeHwnd, key, displaySurface{Title: "正交切片 · Inline / Crossline / 时间", Identity: identity,
		StatsVersion: fmt.Sprintf("%d:%d:%d:%d:%d:%t", volumeIL, volumeXL, volumeSample, volumeSampleMin, volumeSampleMax, agc),
		Axes:         axes, Color: mapping, ColorDomain: domain, Plot: area, Regions: regions, ColorRegion: bar, ReadStats: stats, Apply: func(displaycore.Settings) error {
			if side < 0 {
				remapVolumeSharedNormalization()
			} else if side < 2 && volumeScenes[side].valid {
				active := volumeActiveSide
				live := captureVolumeSceneState()
				applyVolumeSceneState(volumeScenes[side])
				displayVolumeDrawingSide = side
				remapVolumeSharedNormalization()
				volumeScenes[side] = captureVolumeSceneState()
				displayVolumeDrawingSide = -1
				if active == side {
					applyVolumeSceneState(volumeScenes[side])
				} else {
					applyVolumeSceneState(live)
				}
			} else if side == 2 {
				volumeResidualKey = ""
			}
			invalidateVolumeBase()
			return nil
		}})
	if mapping != nil {
		displayDrawColorbar(hdc, bar, remainingColor(volumeHwnd, key, color))
	}
}

func drawVolumeEditedRulers(hdc uintptr, area RECT, edges [][2]volumePt, project []func(float64) volumePt) bool {
	axes := volumeDisplayAxes()
	regions := make(map[string]RECT, len(axes))
	_, edited := displayCommitted(volumeHwnd, volumeDisplaySceneKey())
	for i, a := range axes {
		if i >= len(edges) {
			break
		}
		e := edges[i]
		x0, y0, x1, y1 := int(math.Round(e[0].x)), int(math.Round(e[0].y)), int(math.Round(e[1].x)), int(math.Round(e[1].y))
		regions[a.ID] = projectedRulerHit(x0, y0, x1, y1)
		if edited && i < len(project) {
			f := project[i]
			regions[a.ID] = projectedDisplayRuler(hdc, displayAxisFor(volumeHwnd, volumeDisplaySceneKey(), a), func(t float64) (int, int) { p := f(t); return int(math.Round(p.x)), int(math.Round(p.y)) })
		}
	}
	volumeDisplayRegister(hdc, area, regions)
	return edited
}

func remapVolumeEditedColor() bool {
	settings, ok := displayCommitted(volumeHwnd, volumeDisplaySceneKey())
	if !ok || settings.Color == nil {
		return false
	}
	mapping := *settings.Color
	volumeTimeMin, volumeTimeMax = mapping.Range.Min, mapping.Range.Max
	for _, pane := range []struct {
		values []float64
		panel  *comparePanel
	}{{volumeInlineValues, &volumeInline}, {volumeCrosslineValues, &volumeCrossline}} {
		if len(pane.values) > 0 && len(pane.values) == pane.panel.w*pane.panel.h {
			pane.panel.bgra = remainingRaster(pane.values, mapping)
			pane.panel.indices = segy.MapAmplitudeValues(pane.values, volumeTimeMin, volumeTimeMax)
			pane.panel.stats.MapMin, pane.panel.stats.MapMax = volumeTimeMin, volumeTimeMax
		}
	}
	if len(volumeTimeRaster) > 0 && len(volumeTimeRaster) == volumeTime.w*volumeTime.h {
		v := make([]float64, len(volumeTimeRaster))
		for i, x := range volumeTimeRaster {
			v[i] = math.NaN()
			if i < len(volumeTimeMask) && volumeTimeMask[i] {
				v[i] = float64(x)
			}
		}
		volumeTime.bgra = remainingRaster(v, mapping)
		volumeTime.stats.MapMin, volumeTime.stats.MapMax = volumeTimeMin, volumeTimeMax
	}
	return true
}

func drawPseudoDisplayAxes(hdc uintptr, area RECT) {
	w, h := int(area.Right-area.Left), int(area.Bottom-area.Top)
	frame := pseudo3dcore.ProjectFrame(pseudoState.renderedScene, pseudoState.renderedCamera, w, h)
	if !frame.Valid {
		return
	}
	bounds := frame.Bounds
	axes := []displaycore.Axis{
		projectedDisplayAxis("x", "X", "", bounds.XMin, bounds.XMax),
		projectedDisplayAxis("y", "Y", "", bounds.YMin, bounds.YMax),
		projectedDisplayAxis("time", "时间", "ms", bounds.TimeMinMS, bounds.TimeMaxMS),
	}
	regions := make(map[string]RECT, 3)
	for i, a := range axes {
		regions[a.ID] = projectedDisplayRuler(hdc, displayAxisFor(pseudoHwnd, displayPseudoKey, a), func(f float64) (int, int) {
			x, y, timeFraction := bounds.XMin, bounds.YMin, 0.0
			switch i {
			case 0:
				x += f * (bounds.XMax - bounds.XMin)
			case 1:
				y += f * (bounds.YMax - bounds.YMin)
			case 2:
				timeFraction = f
			}
			p, _ := pseudo3dcore.ProjectPoint(pseudoState.renderedScene, pseudoState.renderedCamera, w, h, pseudo3dcore.Point{X: x, Y: y}, timeFraction)
			return int(area.Left) + int(math.Round(p.X)), int(area.Top) + int(math.Round(p.Y))
		})
	}
	bar := remainingBar(area)
	color, showScale := pseudoDisplayedColor()
	displayRegister(pseudoHwnd, displayPseudoKey, displaySurface{Title: "伪三维幕布 · X / Y / 时间", Identity: fmt.Sprintf("%d", pseudoState.workspaceGeneration),
		StatsVersion: pseudoDisplayedStatsVersion(),
		Axes:         axes, Regions: regions, Color: color, ColorDomain: remainingColorDomain(agc), Plot: area, ColorRegion: bar, ReadStats: pseudoDisplayStatistics(), Apply: func(settings displaycore.Settings) error {
			if settings.Color != nil && (!showScale || color == nil || settings.Color.Range != color.Range) {
				pseudoReloadSelectedGain(pseudoState.gainPercent)
			} else if settings.Color != nil {
				queuePseudoSceneRender(true)
			}
			pInvalidateRect.Call(pseudoHwnd, 0, 0)
			return nil
		}})
	if showScale && color != nil {
		displayDrawColorbar(hdc, bar, remainingColor(pseudoHwnd, displayPseudoKey, *color))
	} else if color != nil && !color.Hidden {
		drawAxisText(hdc, "各幕布独立归一化 · 双击设定共用色标", int(bar.Left)-155, int(bar.Top), int(area.Right)-4, int(bar.Top)+22, DT_RIGHT)
	}
}

func pseudoDisplayedStatsVersion() string {
	v := fmt.Sprintf("%d:%d:%t", pseudoState.rangeGeneration, pseudoState.timeRangeGeneration, agc)
	for i, line := range pseudoState.lines {
		if !line.selected {
			continue
		}
		v += fmt.Sprintf(";%d:%d:%t", i, len(line.segments), line.ready)
	}
	return v
}

type pseudoDisplayTraceWindow struct {
	path        string
	traces      []int64
	first, last int
}

func pseudoDisplayStatistics() func(context.Context) (displaycore.Stats, error) {
	var windows []pseudoDisplayTraceWindow
	for i := range pseudoState.lines {
		state := &pseudoState.lines[i]
		if !state.selected || state.line == nil || state.line.Dataset == nil {
			continue
		}
		for _, s := range state.segments {
			if s.preview || !s.texture.Valid() || len(s.traceIndices) == 0 {
				continue
			}
			windows = append(windows, pseudoDisplayTraceWindow{path: state.line.Dataset.Path, traces: s.traceIndices, first: s.sampleStart, last: s.sampleEnd})
		}
	}
	normalize := agc
	return func(ctx context.Context) (displaycore.Stats, error) {
		var stats displaycore.Stats
		for _, w := range windows {
			if err := ctx.Err(); err != nil {
				return stats, err
			}
			f, err := segy.Open(w.path)
			if err != nil {
				return stats, err
			}
			err = remainingTraceStats(ctx, &stats, f, w.traces, w.first, w.last, normalize)
			f.Close()
			if err != nil {
				return stats, err
			}
		}
		return stats, nil
	}
}

func pseudoDisplayedColor() (*displaycore.ColorMapping, bool) {
	pal := pseudoPalette(pseudoState.paletteIndex)
	var p [256]rgb
	for i, v := range pal {
		p[i] = rgb{v.R, v.G, v.B}
	}
	mapRange, any, common := displaycore.Range{}, false, true
	for i := range pseudoState.lines {
		state := &pseudoState.lines[i]
		if !state.selected {
			continue
		}
		for _, s := range state.segments {
			if !s.texture.Valid() {
				continue
			}
			if !finiteFloat(s.displayMin) || !finiteFloat(s.displayMax) || s.displayMin >= s.displayMax || (any && (s.displayMin != mapRange.Min || s.displayMax != mapRange.Max)) {
				common = false
			}
			if !any {
				mapRange, any = remainingRange(s.displayMin, s.displayMax), true
			}
		}
	}
	m := remainingMapping(mapRange.Min, mapRange.Max, agc, p)
	return &m, any && common
}

func pseudoEditedPalette() ([256]pseudo3dcore.Color, bool) {
	s, ok := displayCommitted(pseudoHwnd, displayPseudoKey)
	var p [256]pseudo3dcore.Color
	if !ok || s.Color == nil {
		return p, false
	}
	for i := range p {
		c := s.Color.ColorAtPosition(1 - float64(i)/255)
		p[i] = pseudo3dcore.Color{R: c.R, G: c.G, B: c.B}
	}
	return p, true
}
