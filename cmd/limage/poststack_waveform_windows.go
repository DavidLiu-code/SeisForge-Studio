//go:build windows

package main

import (
	"context"
	"fmt"
	"math"
	"sort"
	"unsafe"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

type poststackPlotMode uint8

const (
	poststackPlotColor poststackPlotMode = iota
	poststackPlotWiggle
	poststackPlotWiggleVA
	poststackPlotVA
)

type poststackWaveStyle struct {
	Mode                           poststackPlotMode
	BlackRed, Reverse              bool
	Spacing, TimeStepMS, Amplitude float64
	Swing, LineWidth, Opacity      float64
	Density                        poststackWaveDensity
	Stride                         int
	Baseline                       bool
	PositiveFill, NegativeFill     bool
	PositiveColor, NegativeColor   uint32
	VisualSet                      bool
}

type poststackWaveDensity uint8

const (
	poststackWaveDensityAuto poststackWaveDensity = iota
	poststackWaveDensityAll
	poststackWaveDensityStride
)

func poststackWaveDefaultStyle() poststackWaveStyle {
	return poststackWavePresetStyle(poststackWaveStyle{Swing: 1, Stride: 1, LineWidth: 1, Opacity: 1}, true)
}

func poststackWavePresetStyle(s poststackWaveStyle, blackRed bool) poststackWaveStyle {
	if !s.VisualSet {
		s.Opacity = 1
	}
	s.VisualSet, s.BlackRed = true, blackRed
	s.PositiveColor, s.NegativeColor = uint32(rgbRef(10, 10, 10)), uint32(rgbRef(10, 10, 10))
	s.PositiveFill, s.NegativeFill = true, blackRed
	if blackRed {
		s.NegativeColor = uint32(rgbRef(190, 28, 27))
	}
	return s
}

func (s poststackWaveStyle) effective() poststackWaveStyle {
	if !s.VisualSet {
		s = poststackWavePresetStyle(s, s.BlackRed)
	}
	if !seismicFinite(s.Swing) || s.Swing <= 0 {
		s.Swing = 1
	}
	if !seismicFinite(s.LineWidth) || s.LineWidth <= 0 {
		s.LineWidth = 1
	}
	if !seismicFinite(s.Opacity) {
		s.Opacity = 1
	}
	s.Opacity = max(0, min(1, s.Opacity))
	s.Stride = max(1, s.Stride)
	return s
}

func (s poststackWaveStyle) active() bool {
	return s.Mode >= poststackPlotWiggle && s.Mode <= poststackPlotVA
}

func (s poststackWaveStyle) rowsKey() poststackWaveStyle {
	if !s.active() {
		return poststackWaveStyle{}
	}
	// Only the choice to cache real rows and their draw selection affects a
	// filter worker. Color, VA-vs-line and display scaling are paint-only.
	return poststackWaveStyle{Mode: poststackPlotWiggle, Spacing: s.Spacing, Density: s.Density, Stride: max(1, s.Stride)}
}

// Keep raster as the initial mode. The first explicit Wiggle selection uses
// the black/red polarity preset, and later user palette choices remain intact.
var poststackWaveDisplay = poststackWaveDefaultStyle()

func poststackWaveActive() bool { return compareMode != 2 && poststackWaveDisplay.active() }

// The shared style changes presentation, not the algorithm input or dataset.
// The caller commits any other parameter fields before asking for a render.
func poststackApplyWaveDisplay(style poststackWaveStyle) {
	poststackWaveDisplay = style
	displayCompareLayoutCache.valid = false
	syncPoststackWavePalette()
}

func syncPoststackWavePalette() {
	if cc.palette == 0 {
		return
	}
	names, index := paletteNames, comparePaletteIndex
	if poststackWaveActive() {
		names, index = []string{"黑白", "黑红"}, boolToInt(poststackWaveDisplay.BlackRed)
	}
	count, _, _ := pSendMessageW.Call(cc.palette, 0x146 /* CB_GETCOUNT */, 0, 0)
	if int(count) != len(names) {
		pSendMessageW.Call(cc.palette, 0x14b /* CB_RESETCONTENT */, 0, 0)
		for _, name := range names {
			pSendMessageW.Call(cc.palette, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(name))))
		}
	}
	pSendMessageW.Call(cc.palette, CB_SETCURSEL, uintptr(index), 0)
}

func poststackWavePaletteSelection(index int) bool {
	if !poststackWaveActive() {
		return false
	}
	if index >= 0 && index <= 1 {
		poststackWaveDisplay = poststackWavePresetStyle(poststackWaveDisplay, index == 1)
		syncPoststackWavePalette()
		syncPoststackParameterContext()
		invalidateCompareBase()
		setText(cc.status, "波形配色："+[]string{"黑白（单侧填充）", "黑红（正负分色）"}[index])
	}
	return true
}

func poststackCycleWavePalette(delta int) bool {
	return poststackWavePaletteSelection((boolToInt(poststackWaveDisplay.BlackRed) + delta%2 + 2) % 2)
}

type poststackWaveRow struct {
	coord  float64
	values []float64
}

type poststackWaveFrame struct {
	rows        []poststackWaveRow
	traceCount  int
	first, last int
	intervalUS  int
	overlap     bool
	drawTarget  int
	amplitudes  poststackWaveAmplitudes
}

type poststackWaveSelection struct {
	coords  map[float64]struct{}
	chosen  []bool
	overlap bool
}

// Width is passed in DIP; spacing and stride are independent of geometry.
// A memory budget applies only in Automatic mode and only to retained
// drawing rows. The automatic reference still sees every visible sample.
func poststackSelectWaveform(coords []float64, width, samples int, style poststackWaveStyle) poststackWaveSelection {
	res := poststackWaveSelection{coords: make(map[float64]struct{}), chosen: make([]bool, len(coords))}
	capacity := max(1, min(width/12+1, 1048576/max(1, samples)))
	stride := 1
	switch style.Density {
	case poststackWaveDensityAll:
	case poststackWaveDensityStride:
		stride = max(1, style.Stride)
	default:
		stride = max(1, (len(coords)+capacity-1)/capacity)
	}
	previous := math.Inf(-1)
	picked := 0
	for i := 0; i < len(coords); i += stride {
		if x := coords[i]; style.Density != poststackWaveDensityAuto || style.Spacing <= 0 || math.Abs(x-previous) >= style.Spacing {
			res.coords[x] = struct{}{}
			res.chosen[i] = true
			previous = x
			picked++
		}
	}
	res.overlap = style.Density != poststackWaveDensityAuto && picked > max(1, width/12+1)
	return res
}

func poststackWaveCoordinates(traces []int64, coords []int32) []float64 {
	out := make([]float64, len(traces))
	for i, tr := range traces {
		if len(coords) == len(traces) {
			out[i] = float64(coords[i])
		} else {
			out[i] = float64(tr) + 1
		}
	}
	return out
}

func poststackBuildWaveFrame(traces []int64, coords []float64, first, last, count, dt int, selected poststackWaveSelection, read func(int64, int, int) ([]float64, error)) *poststackWaveFrame {
	return poststackBuildWaveFrameContext(context.Background(), traces, coords, first, last, count, dt, selected, read)
}

func poststackBuildWaveFrameContext(ctx context.Context, traces []int64, coords []float64, first, last, count, dt int, selected poststackWaveSelection, read func(int64, int, int) ([]float64, error)) *poststackWaveFrame {
	first, last = max(0, first), min(count-1, last)
	if first > last || dt <= 0 || len(traces) != len(coords) || read == nil {
		return nil
	}
	frame := &poststackWaveFrame{traceCount: len(traces), first: first, last: last, intervalUS: dt, overlap: selected.overlap}
	for i, tr := range traces {
		if ctx.Err() != nil {
			return nil
		}
		_, draw := selected.coords[coords[i]]
		if len(selected.chosen) == len(traces) {
			draw = selected.chosen[i]
		}
		if draw {
			frame.drawTarget++
		}
		values, err := read(tr, first, last)
		if err == nil && len(values) == last-first+1 {
			validBefore := frame.amplitudes.count
			frame.amplitudes.add(values)
			if draw && frame.amplitudes.count > validBefore {
				frame.rows = append(frame.rows, poststackWaveRow{coords[i], values})
			}
		}
	}
	return frame
}

func poststackWaveSelectionForPanel(coords []float64, inherited poststackWaveSelection, sameLine bool, width, ns int, style poststackWaveStyle) poststackWaveSelection {
	if sameLine && style.Density == poststackWaveDensityAuto && len(inherited.coords) > 0 {
		for _, x := range coords {
			if _, ok := inherited.coords[x]; ok {
				inherited.chosen = nil // indices belong to A, not necessarily this subset
				return inherited
			}
		}
	}
	// A sparse B may contain only traces that A's selection skipped. Keep
	// those actual baselines rather than dropping a single valid B entirely.
	return poststackSelectWaveform(coords, width, ns, style)
}

func poststackWaveDifferenceReader(a, b *segy.File, tracesA, tracesB []int64, offset int) func(int64, int, int) ([]float64, error) {
	pairs := make(map[int64]int64, len(tracesA))
	for i, tr := range tracesA {
		pairs[tr] = tracesB[i]
	}
	return func(trace int64, first, last int) ([]float64, error) {
		other, ok := pairs[trace]
		if !ok {
			return nil, fmt.Errorf("no waveform residual pair")
		}
		av, err := a.ReadTraceWindow(trace, first, last)
		if err != nil {
			return nil, err
		}
		bv, err := b.ReadTraceWindow(other, first-offset, last-offset)
		if err != nil {
			return nil, err
		}
		for i := range av {
			av[i] -= bv[i]
		}
		return av, nil
	}
}

type poststackWaveVertex struct{ t, amp float64 }

// Emit sample-domain segments. Missing samples break both curves and fills;
// for a true sign change the two lobes meet at the interpolated zero, not at
// either sample. Time thinning cannot skip a closure or a sign change.
func poststackWaveSegments(values []float64, step int, emit func(a, b poststackWaveVertex, negative bool)) {
	step = max(1, step)
	var previous poststackWaveVertex
	hasPrevious := false
	for i, v := range values {
		if !seismicFinite(v) {
			hasPrevious = false
			continue
		}
		if hasPrevious && i%step != 0 && i+1 < len(values) && seismicFinite(values[i+1]) &&
			v != 0 && (v < 0) == (previous.amp < 0) && (v < 0) == (values[i+1] < 0) {
			continue
		}
		x := poststackWaveVertex{float64(i), v}
		if hasPrevious {
			if previous.amp != 0 && v != 0 && (previous.amp < 0) != (v < 0) {
				a, b := math.Abs(previous.amp), math.Abs(v)
				// Scale before summing, so even extreme finite inputs have a
				// finite, scale-independent fractional zero-crossing location.
				scale := max(a, b)
				f := (a / scale) / (a/scale + b/scale)
				zero := poststackWaveVertex{previous.t + (x.t-previous.t)*f, 0}
				emit(previous, zero, previous.amp < 0)
				emit(zero, x, v < 0)
			} else {
				emit(previous, x, previous.amp < 0 || v < 0)
			}
		}
		previous, hasPrevious = x, true
	}
}

// Every finite, full-resolution sample enters a deterministic IEEE-754
// magnitude sketch (16 bins per octave; no reservoir sampling or sorting).
// The observed peak remains exact. A sparse trace with >98% zeros falls back
// to its true peak rather than multiplying the few nonzero samples arbitrarily.
type poststackWaveAmplitudes struct {
	count, zero       uint64
	peak              float64
	bins              []uint64
	firstBin, lastBin int
}

func (a *poststackWaveAmplitudes) add(values []float64) {
	for _, v := range values {
		if !seismicFinite(v) {
			continue
		}
		a.count++
		v = math.Abs(v)
		if v == 0 {
			a.zero++
			continue
		}
		a.peak = max(a.peak, v)
		bin := int(math.Float64bits(v) >> 48)
		if a.bins == nil {
			a.bins = make([]uint64, 0x7ff0)
			a.firstBin, a.lastBin = bin, bin
		}
		a.firstBin, a.lastBin = min(a.firstBin, bin), max(a.lastBin, bin)
		a.bins[bin]++
	}
}

func (a *poststackWaveAmplitudes) reference(q float64) float64 {
	if a == nil || a.peak == 0 {
		return 1
	}
	rank := uint64(math.Ceil(float64(a.count) * q))
	if rank <= a.zero {
		return a.peak
	}
	seen := a.zero
	for i := a.firstBin; i <= a.lastBin; i++ {
		seen += a.bins[i]
		if seen >= rank {
			return min(a.peak, math.Float64frombits(uint64(i)<<48|1<<47))
		}
	}
	return a.peak
}

func poststackWaveReferencePercentile() float64 {
	// Gain is the existing percentile-trim display control. Apply it once
	// here, never again to the underlying samples or a manual reference.
	return max(.05, .98-2*compareMappingGain()/100)
}

func poststackWaveLinks() (linked, follow bool) {
	b := seismicCompareCompareGroup()
	if b == nil || !compareActiveB() || compareB.wave == nil {
		return false, false
	}
	// Read the existing explicit scale preferences, not the raster's
	// AGC-dependent ability to share a physical amplitude colorbar. Wiggle
	// values never go through the raster's per-trace AGC normalization.
	if o := displayOwners[compareHwnd]; o != nil {
		if v, ok := o.links[b.Keys]; ok {
			return v.Linked, v.FollowA
		}
	}
	return b.Linked, b.FollowA
}

func poststackWaveReferenceFrame(panel int, p comparePanel) *poststackWaveFrame {
	linked, follow := poststackWaveLinks()
	if compareA.wave != nil && (panel == 1 && linked || panel == 2 && follow) {
		return compareA.wave
	}
	return p.wave
}

func poststackWaveReference(panel int, p comparePanel, style poststackWaveStyle) float64 {
	if seismicFinite(style.Amplitude) && style.Amplitude > 0 {
		return style.Amplitude
	}
	if f := poststackWaveReferenceFrame(panel, p); f != nil {
		return f.amplitudes.reference(poststackWaveReferencePercentile())
	}
	return 1
}

func poststackWaveReferenceSummary() string {
	items := []string{}
	linked, follow := poststackWaveLinks()
	for i, p := range [...]comparePanel{compareA, compareB, compareD} {
		if p.wave == nil || i > 0 && !compareActiveB() || i == 2 && !compareShowDiff {
			continue
		}
		f := poststackWaveReferenceFrame(i, p)
		if f == nil || f.amplitudes.count == 0 {
			continue
		}
		s := fmt.Sprintf("%s %.6g", [...]string{"A", "B", "A−B"}[i], f.amplitudes.reference(poststackWaveReferencePercentile()))
		if i == 1 && linked || i == 2 && follow {
			s += "（跟随 A）"
		}
		if f.amplitudes.peak == 0 {
			s += "（全零，绘图回退 1）"
		}
		items = append(items, s)
	}
	if len(items) == 0 {
		return "自动参考由当前可见道/时间内全部有效样点计算；待绘制后显示数值。"
	}
	s := fmt.Sprintf("自动参考 · 当前可见道与时间 · |振幅| P%.4g（估算）", 100*poststackWaveReferencePercentile())
	for _, item := range items {
		s += " · " + item
	}
	return s
}

type poststackWaveInk struct {
	color uintptr
	fill  bool
}

func poststackWaveInks(style poststackWaveStyle) [2]poststackWaveInk {
	style = style.effective()
	inks := [2]poststackWaveInk{{uintptr(style.PositiveColor), style.PositiveFill}, {uintptr(style.NegativeColor), style.NegativeFill}}
	if style.Reverse {
		inks[0], inks[1] = inks[1], inks[0]
	}
	return inks
}

func poststackWaveTraceFraction(panel int, p comparePanel, x float64) (float64, bool) {
	if f, ok := displayCompareDistanceFraction(panel, x); ok {
		return f, true
	}
	if f, ok := seismicAcrossFraction(p.coords, x); ok {
		return f, true
	}
	if p.stats.TraceStart == p.stats.TraceEnd {
		return .5, x-1 == float64(p.stats.TraceStart)
	}
	f := (x - 1 - float64(p.stats.TraceStart)) / float64(p.stats.TraceEnd-p.stats.TraceStart)
	return f, f >= 0 && f <= 1
}

func poststackWaveTraceCenter(panel int, p comparePanel, plot, raster RECT, x float64, reverse bool) (float64, bool) {
	// For a partial B or residual, don't re-interpolate the matching traces
	// against B's compressed ranks: every real coordinate keeps A's exact
	// pixel center and missing intervals remain empty.
	area := raster
	if poststackWaveSharedHorizontal(panel) {
		area, panel, p = plot, 0, compareA
	}
	f, ok := poststackWaveTraceFraction(panel, p, x)
	if !ok || f < 0 || f > 1 {
		return 0, false
	}
	if reverse {
		f = 1 - f
	}
	return float64(area.Left) + f*float64(area.Right-area.Left-1), true
}

func poststackWaveSharedHorizontal(panel int) bool {
	if !poststackWaveActive() || panel <= 0 || len(compareA.coords) == 0 || !compareActiveB() {
		return false
	}
	_, _, shared := seismicComparePartial(panel, false)
	return shared
}

func poststackWaveCoordinatePresent(panel int, coord float64) bool {
	if !poststackWaveSharedHorizontal(panel) || !compareDifferenceGeometryKnown(compareTD) {
		return true
	}
	p := seismicComparePanel(panel)
	if p == nil || len(p.coords) == 0 {
		return false
	}
	i := sort.Search(len(compareA.coords), func(i int) bool { return float64(compareA.coords[i]) >= coord })
	if i == len(compareA.coords) || i > 0 && coord-float64(compareA.coords[i-1]) <= float64(compareA.coords[i])-coord {
		i--
	}
	want := compareA.coords[i]
	j := sort.Search(len(p.coords), func(j int) bool { return p.coords[j] >= want })
	return j < len(p.coords) && p.coords[j] == want
}

func poststackWaveBaseSwing(plotWidth, traceCount, dpi int) float64 {
	// Use actual unthinned trace pitch, floored at twelve logical pixels.
	// This readability floor is independent of the automatic/explicit draw
	// selection. A+B shared-scale panels use A's full visible trace count.
	pitch := float64(max(1, plotWidth-1)) / float64(max(1, traceCount-1))
	return min(max(pitch, 12*float64(dpi)/96)*.5, float64(max(6, plotWidth/4)))
}

func poststackWaveTimeStep(style poststackWaveStyle, dt, samples int) int {
	if !seismicFinite(style.TimeStepMS) || style.TimeStepMS <= 0 || dt <= 0 {
		return 1
	}
	return int(min(float64(max(1, samples)), max(1, math.Ceil(style.TimeStepMS*1000/float64(dt)))))
}

func poststackWaveWidthDIP(width, dpi int) int { return max(1, width*96/max(1, dpi)) }

func paintPoststackWaveforms(dc uintptr, plot, raster RECT, p comparePanel, panel int, reverseX, reverseY bool) bool {
	if !poststackWaveActive() {
		return false
	}
	// We explicitly refuse a raster fallback for wave modes (including an
	// unavailable/empty waveform). The plot background is already white.
	if p.wave == nil || len(p.wave.rows) == 0 {
		return true
	}
	f := p.wave
	style := poststackWaveDisplay.effective()
	colors := poststackWaveInks(style)
	amp := poststackWaveReference(panel, p, style)
	area := RECT{Left: plot.Left, Top: raster.Top, Right: plot.Right, Bottom: raster.Bottom}
	w, h := rectWH(area)
	if w < 1 || h < 1 {
		return true
	}
	dpi := displayDPI(compareHwnd)
	traceCount := f.traceCount
	linked, follow := poststackWaveLinks()
	if compareA.wave != nil && (panel == 1 && linked || panel == 2 && follow) {
		traceCount = compareA.wave.traceCount
	}
	swing := poststackWaveBaseSwing(w, traceCount, dpi) * style.Swing
	if reverseX {
		swing = -swing
	}
	step := poststackWaveTimeStep(style, f.intervalUS, f.last-f.first+1)
	curves := style.Mode != poststackPlotVA
	areas := style.Mode != poststackPlotWiggle && style.Opacity > 0 && (colors[0].fill || colors[1].fill)
	if !curves && !areas && !style.Baseline {
		return true
	}
	r := newPoststackWaveRaster(w, h, curves, [2]bool{areas && colors[0].fill, areas && colors[1].fill})
	lineWidth := style.LineWidth * float64(dpi) / 96
	for _, row := range f.rows {
		base, ok := poststackWaveTraceCenter(panel, p, plot, raster, row.coord, reverseX)
		if !ok {
			continue
		}
		base += .5 - float64(area.Left)
		if style.Baseline {
			for _, v := range row.values {
				if seismicFinite(v) {
					r.baseline(base)
					break
				}
			}
		}
		point := func(v poststackWaveVertex) poststackWavePoint {
			fy := .5
			if f.last > f.first {
				fy = v.t / float64(f.last-f.first)
			}
			if reverseY {
				fy = 1 - fy
			}
			displacement := swing * (v.amp / amp)
			// Saturate only at a finite coordinate far beyond the canvas so
			// the coverage renderer can still clip extreme projections.
			limit := float64(max(w, h)) * 1e12
			displacement = max(-limit, min(limit, displacement))
			return poststackWavePoint{base + displacement, .5 + fy*float64(h-1)}
		}
		poststackWaveSegments(row.values, step, func(a, b poststackWaveVertex, negative bool) {
			i := boolToInt(negative)
			p0, p1 := point(a), point(b)
			if curves {
				r.stroke(r.line[i], p0, p1, lineWidth)
			}
			if areas && colors[i].fill {
				r.area(i, base, p0, p1)
			}
		})
		if curves && len(row.values) == 1 && seismicFinite(row.values[0]) {
			v := row.values[0]
			pixel := point(poststackWaveVertex{amp: v})
			r.stroke(r.line[boolToInt(v < 0)], pixel, pixel, lineWidth)
		}
	}
	r.finish(colors, style.Opacity)
	seismicPlot(dc, area, r.bgra, w, h, false, false)
	return true
}

func drawPoststackWaveLegend(dc uintptr, layout displayPlotLayout, c displaycore.ColorMapping) {
	if c.Hidden || layout.ColorRegion.Right <= layout.ColorRegion.Left {
		return
	}
	f := layout.Typography
	left, top := int(layout.ColorRegion.Left), int(layout.ColorRegion.Top)
	dpi := max(96, f.ColorTick.DPI)
	box, gap := displayPixelDIP(10, dpi), displayPixelDIP(4, dpi)
	paint, done := displayMeasuredTextPainter(dc, f.ColorTitle, false)
	paint("极性", left, top)
	done()
	top += f.ColorTitle.Height + 2*gap
	paint, done = displayMeasuredTextPainter(dc, f.ColorTick, false)
	defer done()
	for i, ink := range poststackWaveInks(poststackWaveDisplay) {
		r := RECT{int32(left), int32(top), int32(left + box), int32(top + box)}
		if poststackWaveDisplay.Mode == poststackPlotWiggle || ink.fill {
			brush, _, _ := pCreateSolidBrush.Call(ink.color)
			if brush != 0 {
				pFillRect.Call(dc, uintptr(unsafe.Pointer(&r)), brush)
				pDeleteObject.Call(brush)
			}
		} else {
			displayFill(dc, r, displaycore.RGB{R: 225, G: 225, B: 225})
			displayFill(dc, RECT{r.Left + 1, r.Top + 1, r.Right - 1, r.Bottom - 1}, displaycore.RGB{R: 255, G: 255, B: 255})
		}
		paint([]string{"+", "−"}[i], left+box+gap, top)
		top += max(box, f.ColorTick.Height) + gap
	}
}

func poststackWaveLegendLayout(c displaycore.ColorMapping) displaycore.ColorMapping {
	c.Range = displaycore.Range{Min: -1, Max: 1}
	c.Divisions, c.Format, c.Digits = 1, displaycore.IntegerFormat, 0
	return c
}

func poststackWaveFrameNotice(p *comparePanel) string {
	if !poststackWaveActive() || p.wave == nil {
		return ""
	}
	s := fmt.Sprintf("波形绘制 %d/%d 道", len(p.wave.rows), p.wave.traceCount)
	if p.wave.drawTarget < p.wave.traceCount {
		s += "（显示抽稀）"
	}
	if len(p.wave.rows) < p.wave.drawTarget {
		s += "（部分道没有有效样点）"
	}
	if p.wave.overlap {
		s += "（较密，可能重叠）"
	}
	return s
}

// Choose the next sampled trace where physical order is available; legacy
// explicit geometry files already arrive in their actual display order.
func poststackLegacyWaveTraceRange(first, last, step int64) []int64 {
	if first > last || first < 0 {
		return nil
	}
	step = max(1, step)
	count := (last-first)/step + 1
	if count > int64(math.MaxInt32) {
		return nil
	}
	out := make([]int64, int(count))
	for i := range out {
		out[i] = first + int64(i)*step
	}
	return out
}
