//go:build windows

package main

import (
	"math"
	"sort"
	"strings"
	"sync/atomic"
	"unicode"
	"unicode/utf16"
	"unsafe"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

// Text measurement, tick selection and scene allocation are intentionally shared
// by the on-screen renderer, the editor and image exports. No sample data enters
// this layer.
type displayTextSize struct{ Width, Height int }
type displayPlotMetrics struct {
	DPI, Height int
	Measure     func(string) displayTextSize
	measured    map[string]displayTextSize
}
type displayPlotInput struct {
	X, Y         displaycore.Axis
	Color        *displaycore.ColorMapping
	Position     func(float64) float64 // physical value -> rank-space fraction
	CompareTitle string
	LegendTitle  string
	Typography   displayCompareTypography
	FollowA      bool // poststack axes share the reference locator, not just style
}
type displayPlotLayout struct {
	Outer, Plot, ColorRegion RECT
	Regions                  map[string]RECT
	AxisTicks                map[string]displaycore.TickPlan
	ColorTicks               displaycore.TickPlan
	Metrics                  displayPlotMetrics
	Compare                  bool
	CompareTitle             string
	LegendTitle              string
	Typography               displayCompareTypography
	ColorGradient            RECT
	HorizontalAxis           displayHorizontalAxisRows
}
type displayPlotInsets struct{ Left, Top, Right, Bottom int }

// Mouse hit-testing asks for scene bounds very frequently. Its cache stores
// only numeric identities and measured layout, never a Reader or amplitude
// buffer. New frames, view changes, DPI and any relevant style invalidate it.
type displayLayoutColorKey struct {
	Range                   displaycore.Range
	Title, Unit             string
	Step, TickLength        float64
	TickFontPT, TitleFontPT float64
	Digits, Divisions       int
	Format                  displaycore.NumberFormat
	Hidden, TickLengthSet   bool
}
type displayLayoutPanelKey struct {
	Stats              segy.RenderStats
	Coords, BGRA       uintptr
	N, W, H, SampleDt  int
	SampleOrigin       int64
	ViewAxes, Filtered bool
	FollowsA           bool
	X0, X1, T0, T1     float64
	Axes               [2]displaycore.Axis
	Color              displayLayoutColorKey
	PlotTitleFontPT    float64
}
type displayCompareLayoutKey struct {
	Owner, Font, Data, Filter                                uintptr
	Width, Height, DPI, Count, Mode, SampleFirst, SampleLast int
	Line                                                     int32
	LineFirst, LineLast                                      float64
	Gen, SliceGen, GeometryGen, TraceFirst, TraceLast        int64
	APath, BPath                                             string
	Bounds                                                   segy.SliceBounds
	Panels                                                   [3]displayLayoutPanelKey
}

var displayCompareLayoutCache struct {
	key   displayCompareLayoutKey
	plans []displayPlotLayout
	valid bool
}

type displayMainLayoutKey struct {
	Owner, Font        uintptr
	Width, Height, DPI int
	Equal              bool
	X, Y               displaycore.Axis
	Color              displayLayoutColorKey
	PlotTitleFontPT    float64
}

var displayMainLayoutCache struct {
	key   displayMainLayoutKey
	plan  displayPlotLayout
	valid bool
}

func displayLayoutColorIdentity(c displaycore.ColorMapping) displayLayoutColorKey {
	return displayLayoutColorKey{Range: c.Range, Title: c.Title, Unit: c.Unit, Step: c.Step, TickLength: c.TickLength, TickFontPT: c.TickFontPT, TitleFontPT: c.TitleFontPT, Digits: c.Digits, Divisions: c.Divisions, Format: c.Format, Hidden: c.Hidden, TickLengthSet: c.TickLengthSet}
}

func seismicCompareLayoutKey(cw, ch, dpi, n int) displayCompareLayoutKey {
	k := displayCompareLayoutKey{Owner: compareHwnd, Font: hFont, Data: uintptr(unsafe.Pointer(compareTD)), Filter: uintptr(unsafe.Pointer(committedViewFilter())), Width: cw, Height: ch, DPI: dpi, Count: n, Mode: compareMode, Line: compareLineCoord, LineFirst: compareLineXMin, LineLast: compareLineXMax, SampleFirst: compareSampleStart, SampleLast: compareSampleEnd, TraceFirst: compareTraceStart, TraceLast: compareTraceEnd, APath: compareAPath, BPath: compareBPath, Gen: atomic.LoadInt64(&compareGen), SliceGen: atomic.LoadInt64(&compareSliceGen), GeometryGen: atomic.LoadInt64(&compareGeomGen)}
	if compareTD != nil {
		k.Bounds = compareTD.bounds
	}
	for i := 0; i < n; i++ {
		p := seismicComparePanel(i)
		if p == nil {
			continue
		}
		v := displayLayoutPanelKey{Stats: p.stats, N: len(p.coords), W: p.w, H: p.h, SampleDt: p.sampleDtUS, SampleOrigin: p.sampleOriginUS, ViewAxes: p.viewAxes, Filtered: p.filtered, X0: p.filterX0, X1: p.filterX1, T0: p.filterT0, T1: p.filterT1, Color: displayLayoutColorIdentity(seismicPanelColor(i, *p))}
		if len(p.coords) > 0 {
			v.Coords = uintptr(unsafe.Pointer(&p.coords[0]))
		}
		if len(p.bgra) > 0 {
			v.BGRA = uintptr(unsafe.Pointer(&p.bgra[0]))
		}
		s, _ := displayCommitted(compareHwnd, seismicCompareKey(i))
		v.Axes[0], v.Axes[1] = seismicCompareEffectiveAxes(i)
		v.FollowsA = seismicCompareFollowsAxes(i)
		v.PlotTitleFontPT = s.PlotTitleFontPT
		k.Panels[i] = v
	}
	return k
}

var displayGetTextExtent = gdi32.NewProc("GetTextExtentPoint32W")
var displayGetDeviceCaps = gdi32.NewProc("GetDeviceCaps")
var displayGetDC = user32.NewProc("GetDC")
var displayReleaseDC = user32.NewProc("ReleaseDC")
var displayGetTextMetrics = gdi32.NewProc("GetTextMetricsW")
var displayGetGlyphOutline = gdi32.NewProc("GetGlyphOutlineW")
var displayTextOut = gdi32.NewProc("TextOutW")
var displaySetTextAlign = gdi32.NewProc("SetTextAlign")

type displayCompareTypography struct {
	XTick, YTick, ColorTick, XTitle, YTitle, ColorTitle, PlotTitle displayPlotFontMetrics
}

type displayPlotFontMetrics struct {
	PT          float64
	DPI, Ascent int
	Measure     func(string) displayTextSize
	Height      int
	measured    map[string]displayTextSize
	ink         func(string) displayTextInk
	inkMeasured map[string]displayTextInk
}

// Glyph bounds relative to the GDI baseline, with Y increasing downwards.
type displayTextInk struct{ top, bottom int }

func (m displayPlotFontMetrics) inkBounds(s string) displayTextInk {
	if m.ink != nil {
		return m.ink(s)
	}
	return displayTextInk{-max(1, m.Ascent), max(0, m.Height-m.Ascent)}
}

func displayPlotFontPT(pt float64, title bool) float64 {
	if pt > 0 && !math.IsNaN(pt) && !math.IsInf(pt, 0) {
		return pt
	}
	if title {
		return 11
	}
	return 10
}

type displayPlotFontPair struct {
	latin, han uintptr
}

func displayNewPlotFonts(pt float64, dpi, angle int) displayPlotFontPair {
	height := -max(1, int(math.Round(pt*float64(dpi)/72)))
	create := func(face string) uintptr {
		h, _, _ := pCreateFontW.Call(uintptr(height), 0, uintptr(angle), uintptr(angle), 400, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(u16(face))))
		return h
	}
	return displayPlotFontPair{latin: create("Arial"), han: create("Microsoft YaHei UI")}
}

func (f displayPlotFontPair) close() {
	if f.latin != 0 {
		pDeleteObject.Call(f.latin)
	}
	if f.han != 0 {
		pDeleteObject.Call(f.han)
	}
}

func (f displayPlotFontPair) runs(text string, fn func(string, uintptr)) {
	start := 0
	wasHan := false
	for i, r := range text {
		isHan := unicode.Is(unicode.Han, r) || (r >= 0x3000 && r <= 0x303f) || (r >= 0xff00 && r <= 0xffef)
		if i > start && isHan != wasHan {
			face := f.latin
			if wasHan && f.han != 0 {
				face = f.han
			}
			fn(text[start:i], face)
			start = i
		}
		wasHan = isHan
	}
	if start < len(text) {
		face := f.latin
		if wasHan && f.han != 0 {
			face = f.han
		}
		fn(text[start:], face)
	}
}

func displaySelectPlotFont(dc, font uintptr) func() {
	if dc == 0 || font == 0 {
		return func() {}
	}
	old, _, _ := pSelectObject.Call(dc, font)
	return func() {
		if old != 0 && old != ^uintptr(0) {
			pSelectObject.Call(dc, old)
		}
	}
}

func displayPlotTextExtent(dc uintptr, chars []uint16, fallback int) displayTextSize {
	if len(chars) == 0 {
		return displayTextSize{}
	}
	var size struct{ X, Y int32 }
	if dc != 0 {
		ok, _, _ := displayGetTextExtent.Call(dc, uintptr(unsafe.Pointer(&chars[0])), uintptr(len(chars)), uintptr(unsafe.Pointer(&size)))
		if ok != 0 && size.Y > 0 {
			return displayTextSize{int(size.X), int(size.Y)}
		}
	}
	return displayTextSize{len(chars) * max(1, fallback/2), fallback}
}

func displayPlotFontMeasure(dc uintptr, dpi int, pt float64, f displayPlotFontPair) displayPlotFontMetrics {
	fallback := max(1, int(math.Round(pt*float64(dpi)/72)))
	ascent, descent := fallback, max(1, fallback/5)
	for _, font := range []uintptr{f.latin, f.han} {
		if dc == 0 || font == 0 {
			continue
		}
		restore := displaySelectPlotFont(dc, font)
		// The first three fields of TEXTMETRICW are height, ascent and descent.
		var tm struct {
			Height, Ascent, Descent int32
			rest                    [52]byte
		}
		ok, _, _ := displayGetTextMetrics.Call(dc, uintptr(unsafe.Pointer(&tm)))
		if ok != 0 {
			ascent = max(ascent, int(tm.Ascent))
			descent = max(descent, int(tm.Descent))
		}
		restore()
	}
	cache := make(map[string]displayTextSize)
	measure := func(value string) displayTextSize {
		if size, ok := cache[value]; ok {
			return size
		}
		v := displayTextSize{Height: ascent + descent}
		f.runs(value, func(run string, font uintptr) {
			restore := displaySelectPlotFont(dc, font)
			v.Width += displayPlotTextExtent(dc, utf16.Encode([]rune(run)), fallback).Width
			restore()
		})
		cache[value] = v
		return v
	}
	inkCache := make(map[string]displayTextInk)
	type glyphMetrics struct {
		width, height      uint32
		x, y               int32
		advanceX, advanceY int16
	}
	ink := func(value string) displayTextInk {
		if v, ok := inkCache[value]; ok {
			return v
		}
		v := displayTextInk{}
		found := false
		// MAT2 identity, represented as four Win32 16.16 FIXED values.
		mat := [4]int32{1 << 16, 0, 0, 1 << 16}
		f.runs(value, func(run string, font uintptr) {
			restore := displaySelectPlotFont(dc, font)
			defer restore()
			for _, ch := range run {
				var gm glyphMetrics
				result, _, _ := displayGetGlyphOutline.Call(dc, uintptr(ch), 0, uintptr(unsafe.Pointer(&gm)), 0, 0, uintptr(unsafe.Pointer(&mat)))
				if uint32(result) != 0xffffffff && gm.height > 0 {
					v.top, v.bottom = min(v.top, -int(gm.y)), max(v.bottom, int(gm.height)-int(gm.y))
					found = true
				}
			}
		})
		if !found {
			v = displayTextInk{-ascent, descent}
		}
		inkCache[value] = v
		return v
	}
	return displayPlotFontMetrics{PT: pt, DPI: dpi, Ascent: ascent, Height: ascent + descent, Measure: measure, measured: cache, ink: ink, inkMeasured: inkCache}
}

func (m displayPlotFontMetrics) frozen() displayPlotFontMetrics {
	if m.measured == nil {
		return m
	}
	cache := make(map[string]displayTextSize, len(m.measured))
	for text, size := range m.measured {
		cache[text] = size
	}
	height := m.Height
	m.Measure = func(text string) displayTextSize {
		if size, ok := cache[text]; ok {
			return size
		}
		return displayTextSize{len(utf16.Encode([]rune(text))) * max(1, height/2), height}
	}
	m.measured = cache
	ink := make(map[string]displayTextInk, len(m.inkMeasured))
	for value, bounds := range m.inkMeasured {
		ink[value] = bounds
	}
	fallback := displayTextInk{-m.Ascent, m.Height - m.Ascent}
	m.inkMeasured = ink
	m.ink = func(value string) displayTextInk {
		if bounds, ok := ink[value]; ok {
			return bounds
		}
		return fallback
	}
	return m
}

func (t displayCompareTypography) frozen() displayCompareTypography {
	return displayCompareTypography{t.XTick.frozen(), t.YTick.frozen(), t.ColorTick.frozen(), t.XTitle.frozen(), t.YTitle.frozen(), t.ColorTitle.frozen(), t.PlotTitle.frozen()}
}

func displayCompareTypographyFor(dc uintptr, dpi int, in displayPlotInput, plotPT float64) (displayCompareTypography, func()) {
	opened := make(map[float64]displayPlotFontPair)
	measured := make(map[float64]displayPlotFontMetrics)
	metric := func(pt float64, title bool) displayPlotFontMetrics {
		pt = displayPlotFontPT(pt, title)
		if m, ok := measured[pt]; ok {
			return m
		}
		f := displayNewPlotFonts(pt, dpi, 0)
		opened[pt] = f
		m := displayPlotFontMeasure(dc, dpi, pt, f)
		measured[pt] = m
		return m
	}
	var colorTick, colorTitle float64
	if in.Color != nil {
		colorTick, colorTitle = in.Color.TickFontPT, in.Color.TitleFontPT
	}
	t := displayCompareTypography{metric(in.X.TickFontPT, false), metric(in.Y.TickFontPT, false), metric(colorTick, false), metric(in.X.TitleFontPT, true), metric(in.Y.TitleFontPT, true), metric(colorTitle, true), metric(plotPT, true)}
	return t, func() {
		for _, f := range opened {
			f.close()
		}
	}
}

func displayDCDPI(dc uintptr) int {
	d, _, _ := displayGetDeviceCaps.Call(dc, 88)
	if d < 48 || d > 768 {
		return 96
	}
	return int(d)
}

func displayPixelDIP(dip float64, dpi int) int {
	return max(0, int(math.Round(dip*float64(max(1, dpi))/96)))
}

func displayMetrics(dc uintptr, dpi int) displayPlotMetrics {
	cache := make(map[string]displayTextSize)
	measure := func(text string) displayTextSize {
		if size, ok := cache[text]; ok {
			return size
		}
		chars := utf16.Encode([]rune(text))
		var size struct{ X, Y int32 }
		n := len(chars)
		if n == 0 {
			return displayTextSize{}
		}
		ok, _, _ := displayGetTextExtent.Call(dc, uintptr(unsafe.Pointer(&chars[0])), uintptr(n), uintptr(unsafe.Pointer(&size)))
		if ok != 0 && size.Y > 0 {
			v := displayTextSize{int(size.X), int(size.Y)}
			cache[text] = v
			return v
		}
		v := displayTextSize{n * max(1, 7*dpi/96), max(1, 15*dpi/96)}
		cache[text] = v
		return v
	}
	h := measure("Ag时间−1.23e+06").Height
	return displayPlotMetrics{DPI: dpi, Height: max(1, h), Measure: measure, measured: cache}
}

func displayFreezeMetrics(m displayPlotMetrics) displayPlotMetrics {
	if m.measured == nil {
		return m
	}
	cache := make(map[string]displayTextSize, len(m.measured))
	for text, size := range m.measured {
		cache[text] = size
	}
	dpi, height := m.DPI, m.Height
	m.Measure = func(text string) displayTextSize {
		if size, ok := cache[text]; ok {
			return size
		}
		return displayTextSize{len(utf16.Encode([]rune(text))) * max(1, 7*dpi/96), height}
	}
	m.measured = cache
	return m
}

// A measurement DC always selects the same font as the corresponding scene.
func displayMeasurementDC(owner uintptr) (uintptr, func()) {
	dc, _, _ := displayGetDC.Call(owner)
	if dc == 0 {
		dc, _, _ = pCreateCompatibleDC.Call(0)
		if hFont != 0 {
			pSelectObject.Call(dc, hFont)
		}
		return dc, func() {
			if dc != 0 {
				pDeleteDC.Call(dc)
			}
		}
	}
	old := uintptr(0)
	if hFont != 0 {
		old, _, _ = pSelectObject.Call(dc, hFont)
	}
	return dc, func() {
		if old != 0 {
			pSelectObject.Call(dc, old)
		}
		displayReleaseDC.Call(owner, dc)
	}
}

func displayAxisTickPlan(m displayPlotMetrics, a displaycore.Axis, pixels int, horizontal bool, position func(float64) float64) displaycore.TickPlan {
	measure := func(s string) int { return m.Measure(s).Width }
	if !horizontal {
		measure = func(string) int { return m.Height }
	}
	return displaycore.BuildTickPlan(a.View, a.Step, displaycore.TickOptions{
		Pixels: max(1, pixels), Gap: max(2, displayPixelDIP(7, m.DPI)), Measure: measure,
		Position: position, DisplayValue: a.DisplayValue, Format: a.Format, Digits: a.Digits,
	})
}

func displayColorTickPlan(m displayPlotMetrics, c displaycore.ColorMapping, pixels int) displaycore.TickPlan {
	return displaycore.BuildTickPlan(c.Range, c.Step, displaycore.TickOptions{
		Pixels: max(1, pixels), Gap: max(2, displayPixelDIP(7, m.DPI)), Measure: func(string) int { return m.Height },
		Format: c.Format, Digits: c.Digits, Amplitude: true,
	})
}

func displayEqualAxisTicks(m displayPlotFontMetrics, a displaycore.Axis, pixels int, horizontal bool, position func(float64) float64) displaycore.TickPlan {
	measure := func(s string) int { return m.Measure(s).Width }
	if !horizontal {
		measure = func(string) int { return m.Height }
	}
	return a.EqualCoordinateTicks(displaycore.TickOptions{
		Pixels: max(1, pixels-1), Gap: max(2, displayPixelDIP(5, m.DPI)), Measure: measure,
		Position: position,
	})
}

func displayEqualAxisTickDIP(a displaycore.Axis) float64 {
	if !a.TickLengthSet && a.TickLength == 0 {
		return 6
	}
	return a.MajorTickDIP()
}

func displayEqualColorTicks(m displayPlotFontMetrics, c displaycore.ColorMapping, pixels int) displaycore.TickPlan {
	return displaycore.BuildEqualTickPlan(c.Range, c.Divisions, displaycore.TickOptions{
		Pixels: max(1, pixels-1), Gap: max(2, displayPixelDIP(5, m.DPI)), Measure: func(string) int { return m.Height },
		Format: c.Format, Digits: c.Digits, Amplitude: true,
	})
}

// Endpoint annotations refer to the exact mapping bounds, but their text can
// be compact. They never insert unequal intervals into the regular tick plan.
func displayColorEndpoints(c displaycore.ColorMapping) []displaycore.Tick {
	if !c.Range.Valid() {
		return nil
	}
	lo, hi := displaycore.FormatAmplitudeBounds(c.Range)
	if c.Format != displaycore.AutoFormat {
		lo, hi = displaycore.FormatNumber(c.Range.Min, c.Format, c.Digits), displaycore.FormatNumber(c.Range.Max, c.Format, c.Digits)
	}
	return []displaycore.Tick{
		{Value: c.Range.Min, Label: lo},
		{Value: c.Range.Max, Label: hi},
	}
}

// Keep the requested grid intact; only select non-colliding label glyphs.
// Reversed axes are sorted by screen position before resolving overlaps.
func displayTickLabelVisibility(ticks []displaycore.Tick, pixel func(float64) int, measure func(string) int, gap int) []bool {
	visible := make([]bool, len(ticks))
	order := make([]int, len(ticks))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool { return pixel(ticks[order[i]].Value) < pixel(ticks[order[j]].Value) })
	lastRight := math.MinInt
	labels := make(map[string]bool, len(ticks))
	for _, i := range order {
		w, q := measure(ticks[i].Label), pixel(ticks[i].Value)
		left, right := q-w/2, q+(w+1)/2
		if labels[ticks[i].Label] || lastRight != math.MinInt && left < lastRight+gap {
			continue
		}
		visible[i] = true
		lastRight = right
		labels[ticks[i].Label] = true
	}
	return visible
}

// Compare preserves both endpoint labels even in a client smaller than the
// requested typography. A density warning accompanies any interior skips.
func displayEqualTickLabelVisibility(ticks []displaycore.Tick, pixel func(float64) int, measure func(string) int, gap int) []bool {
	if len(ticks) <= 2 {
		shown := make([]bool, len(ticks))
		for i := range shown {
			shown[i] = true
		}
		return shown
	}
	shown := make([]bool, len(ticks))
	shown[0], shown[len(ticks)-1] = true, true
	for i := 1; i+1 < len(ticks); i++ {
		show := true
		for j, used := range shown {
			if !used {
				continue
			}
			want := (measure(ticks[i].Label)+measure(ticks[j].Label)+1)/2 + gap
			if ticks[i].Label == ticks[j].Label || absInt(pixel(ticks[i].Value)-pixel(ticks[j].Value)) < want {
				show = false
				break
			}
		}
		shown[i] = show
	}
	return shown
}

func displayColorbarLabels(c displaycore.ColorMapping, plan displaycore.TickPlan, m displayPlotMetrics, top, bottom int) []displaycore.Tick {
	labels := displayColorEndpoints(c)
	if len(labels) == 0 {
		return nil
	}
	gap := max(2, displayPixelDIP(5, m.DPI))
	pixel := func(v float64) int { return bottom - int(math.Round(float64(bottom-top)*c.Range.Fraction(v))) }
	if bottom-top < m.Height+gap {
		return labels[:1]
	}
	for _, tick := range plan.Ticks {
		q := pixel(tick.Value)
		overlap := false
		for _, prior := range labels {
			if tick.Label == prior.Label || absInt(q-pixel(prior.Value)) < m.Height+gap {
				overlap = true
				break
			}
		}
		if !overlap {
			labels = append(labels, tick)
		}
	}
	return labels
}

func displayMeasuredInsets(m displayPlotMetrics, in displayPlotInput, width, height int) (displayPlotInsets, map[string]displaycore.TickPlan, displaycore.TickPlan) {
	gap, pad := max(2, displayPixelDIP(5, m.DPI)), max(2, displayPixelDIP(6, m.DPI))
	xp := displayAxisTickPlan(m, in.X, width, true, in.Position)
	yp := displayAxisTickPlan(m, in.Y, height, false, nil)
	ticks := map[string]displaycore.TickPlan{in.X.ID: xp, in.Y.ID: yp}
	ins := displayPlotInsets{pad, m.Height + 2*gap, pad, pad} // panel title is above the plot
	if !in.X.HideAxis {
		ins.Bottom += displayPixelDIP(in.X.MajorTickDIP(), m.DPI)
		if !in.X.HideLabels {
			ins.Bottom += gap + m.Height
			for _, t := range xp.Ticks {
				w := m.Measure(t.Label).Width
				// End tick labels may straddle the data edge; reserve their
				// measured extent rather than shortening them with ellipses.
				f := in.X.Fraction(t.Value)
				if in.Position != nil {
					f = in.Position(t.Value)
					if in.X.Reverse {
						f = 1 - f
					}
				}
				ins.Left = max(ins.Left, int(math.Ceil(float64(w)/2-f*float64(width)))+pad)
				ins.Right = max(ins.Right, int(math.Ceil(float64(w)/2-(1-f)*float64(width)))+pad)
			}
		}
		if !in.X.HideTitle {
			m.Measure(displayAxisTitle(in.X))
			ins.Bottom += gap + m.Height
		}
	}
	if !in.Y.HideAxis {
		labelWidth := 0
		if !in.Y.HideLabels {
			for _, t := range yp.Ticks {
				labelWidth = max(labelWidth, m.Measure(t.Label).Width)
			}
		}
		left := displayPixelDIP(in.Y.MajorTickDIP(), m.DPI) + gap + labelWidth + pad
		if !in.Y.HideTitle {
			left = max(left, m.Measure(displayAxisTitle(in.Y)).Width+gap+pad)
			ins.Top = max(ins.Top, m.Height+(m.Height+1)/2+gap+pad)
		}
		ins.Left = max(ins.Left, left)
		if !in.Y.HideLabels {
			ins.Top = max(ins.Top, m.Height/2+pad)
			ins.Bottom = max(ins.Bottom, (m.Height+1)/2+pad)
		}
	}
	var cp displaycore.TickPlan
	if in.Color != nil && !in.Color.Hidden {
		cp = displayColorTickPlan(m, *in.Color, height)
		labelWidth := 0
		for _, t := range cp.Ticks {
			labelWidth = max(labelWidth, m.Measure(t.Label).Width)
		}
		for _, t := range displayColorEndpoints(*in.Color) {
			labelWidth = max(labelWidth, m.Measure(t.Label).Width)
		}
		barWidth := displayPixelDIP(16, m.DPI)
		colWidth := max(m.Measure(displayColorTitle(*in.Color)).Width, barWidth+displayPixelDIP(in.Color.MajorTickDIP(), m.DPI)+gap+labelWidth)
		ins.Right = max(ins.Right, 2*gap+colWidth+pad)
		ins.Top = max(ins.Top, m.Height+(m.Height+1)/2+gap+pad)
		ins.Bottom = max(ins.Bottom, (m.Height+1)/2+pad)
	}
	return ins, ticks, cp
}

func displaySceneLayouts(m displayPlotMetrics, outer RECT, inputs []displayPlotInput) []displayPlotLayout {
	if len(inputs) == 0 {
		return nil
	}
	n := len(inputs)
	gap := displayPixelDIP(10, m.DPI)
	available := max(n*2, int(outer.Right-outer.Left)-gap*(n-1))
	cellWidth := available / n
	cellHeight := max(2, int(outer.Bottom-outer.Top))
	common := displayPlotInsets{}
	// Extents depend on the selected tick interval, which depends on plot
	// size. Increasing common margins converges and keeps all panels aligned.
	for pass := 0; pass < 6; pass++ {
		next := common
		for _, in := range inputs {
			i, _, _ := displayMeasuredInsets(m, in, max(2, cellWidth-common.Left-common.Right), max(2, cellHeight-common.Top-common.Bottom))
			next.Left = max(next.Left, i.Left)
			next.Top = max(next.Top, i.Top)
			next.Right = max(next.Right, i.Right)
			next.Bottom = max(next.Bottom, i.Bottom)
		}
		if next == common {
			break
		}
		common = next
	}
	out := make([]displayPlotLayout, n)
	for i, in := range inputs {
		cell := RECT{Left: outer.Left + int32(i*(cellWidth+gap)), Top: outer.Top, Right: outer.Left + int32(i*(cellWidth+gap)+cellWidth), Bottom: outer.Bottom}
		// Extremely small clients retain valid, contained rectangles instead
		// of inventing a 180 px panel that extends outside the window.
		l := min(common.Left, max(0, cellWidth-2))
		r := min(common.Right, max(0, cellWidth-l-2))
		t := min(common.Top, max(0, cellHeight-2))
		b := min(common.Bottom, max(0, cellHeight-t-2))
		plot := RECT{Left: cell.Left + int32(l), Top: cell.Top + int32(t), Right: cell.Right - int32(r), Bottom: cell.Bottom - int32(b)}
		_, plans, cp := displayMeasuredInsets(m, in, int(plot.Right-plot.Left), int(plot.Bottom-plot.Top))
		bar := RECT{}
		if in.Color != nil && !in.Color.Hidden {
			bar = displayColorRegion(m, plot, cell.Right)
		}
		regions := map[string]RECT{
			in.X.ID: {Left: plot.Left, Top: plot.Bottom, Right: plot.Right, Bottom: cell.Bottom},
			in.Y.ID: {Left: cell.Left, Top: cell.Top, Right: plot.Left, Bottom: plot.Bottom},
		}
		out[i] = displayPlotLayout{Outer: cell, Plot: plot, ColorRegion: bar, Regions: regions, AxisTicks: plans, ColorTicks: cp, Metrics: m}
	}
	// Stored layouts outlive the measurement DC. Freeze text extents before
	// returning so repaint/export never use a released HDC.
	frozen := displayFreezeMetrics(m)
	for i := range out {
		out[i].Metrics = frozen
	}
	return out
}

func displayColorRegion(m displayPlotMetrics, plot RECT, right int32) RECT {
	gap := displayPixelDIP(5, m.DPI)
	return RECT{Left: plot.Right + int32(gap), Right: right - int32(displayPixelDIP(6, m.DPI)), Top: plot.Top - int32(m.Height+(m.Height+1)/2+gap), Bottom: plot.Bottom + int32((m.Height+1)/2)}
}

const (
	displayComparePanelGapDIP = 3
	// After GDI glyph placement this is visually close to the side-axis gap,
	// which includes the numeric run's trailing bearing.
	displayCompareXLabelGapDIP   = 4
	displayCompareXTitleGapDIP   = 3
	displayCompareYLabelGapDIP   = 4
	displayCompareBarWidthDIP    = 16
	displayCompareBarGapDIP      = 7
	displayCompareBarLabelGapDIP = 5
)

type displayHorizontalAxisRows struct{ baseline, stride, titleTop, bottom int }

// Rows are offsets from the last pixel of the plot. Reserve the same geometry
// that gets painted: a numeric glyph starts below the tick, not below an
// extra font cell. Only side labels that can meet the X endpoints need clearance.
func displayCompareHorizontalRows(in displayPlotInput, xp, yp, cp displaycore.TickPlan, width int) displayHorizontalAxisRows {
	f, x, y := in.Typography, in.X, in.Y
	dpi := f.XTick.DPI
	length := displayPixelDIP(displayEqualAxisTickDIP(x), dpi)
	top := length + 1 + displayPixelDIP(displayCompareXLabelGapDIP, dpi)
	rows := displayHorizontalAxisRows{titleTop: top, bottom: length + 1}
	if !x.HideLabels && len(xp.Ticks) != 0 {
		ink, halfWidth := displayTextInk{}, 0
		for _, tick := range xp.Ticks {
			b := f.XTick.inkBounds(tick.Label)
			ink.top, ink.bottom = min(ink.top, b.top), max(ink.bottom, b.bottom)
			halfWidth = max(halfWidth, (f.XTick.Measure(tick.Label).Width+1)/2)
		}
		protect := func(font displayPlotFontMetrics, plan displaycore.TickPlan) {
			for _, tick := range plan.Ticks {
				bottom := font.Ascent - font.Height/2 + font.inkBounds(tick.Label).bottom
				top = max(top, bottom+1+displayPixelDIP(1, dpi))
			}
		}
		if !y.HideAxis && !y.HideLabels && halfWidth >= displayPixelDIP(displayEqualAxisTickDIP(y)+displayCompareYLabelGapDIP, dpi) {
			protect(f.YTick, yp)
		}
		if c := in.Color; c != nil && !c.Hidden && halfWidth >= displayPixelDIP(displayCompareBarGapDIP+displayCompareBarWidthDIP+displayCompareBarLabelGapDIP+c.MajorTickDIP(), dpi) {
			protect(f.ColorTick, cp)
		}
		rows.baseline = top - ink.top
		end := rows.baseline + ink.bottom + 1
		if displayCompareStaggerEndpoints(xp, f.XTick, width) {
			rows.stride = ink.bottom - ink.top + 1 + displayPixelDIP(2, dpi)
			end += rows.stride
		}
		rows.bottom = end
		rows.titleTop = end + displayPixelDIP(displayCompareXTitleGapDIP, dpi)
	}
	if !x.HideTitle {
		rows.bottom = max(rows.bottom, rows.titleTop+f.XTitle.Height)
	}
	return rows
}

func displayCompareLabelWidth(plan displaycore.TickPlan, font displayPlotFontMetrics) int {
	w := 0
	for _, tick := range plan.Ticks {
		w = max(w, font.Measure(tick.Label).Width)
	}
	return w
}

func displayCompareStaggerEndpoints(plan displaycore.TickPlan, font displayPlotFontMetrics, width int) bool {
	if len(plan.Ticks) < 2 {
		return false
	}
	a, b := plan.Ticks[0], plan.Ticks[len(plan.Ticks)-1]
	return (font.Measure(a.Label).Width+font.Measure(b.Label).Width+1)/2+displayPixelDIP(5, font.DPI) > width-1
}

// displayFollowingTicks keeps the reference's exact coordinates and formatting.
// A local coverage which does not span the full common view never receives
// ticks outside its own valid display range.
func displayFollowingTicks(local, reference displaycore.Axis, source displaycore.TickPlan) (displaycore.TickPlan, bool) {
	if local.ID != reference.ID || local.CanonicalUnit() != reference.CanonicalUnit() || local.Unit != reference.Unit {
		return displaycore.TickPlan{}, false
	}
	p := source
	if local.View != reference.View {
		p.Ticks = nil
		p.SinglePoint = local.View.IsPoint()
		for _, tick := range source.Ticks {
			if tick.Value >= local.View.Min && tick.Value <= local.View.Max {
				p.Ticks = append(p.Ticks, tick)
			}
		}
		// Preserve the exact position of a real, isolated trace coordinate.
		if p.SinglePoint && len(p.Ticks) == 0 && local.AllowsPoint {
			p = local.EqualCoordinateTicks(displaycore.TickOptions{})
		}
	}
	return p, true
}

type displayCompareReferenceTicks struct {
	input displayPlotInput
	plans map[string]displaycore.TickPlan
}

func displayCompareEditorReferenceTicks(owner uintptr, key string, axis displaycore.Axis) (displaycore.TickPlan, bool) {
	if o := displayOwners[owner]; o != nil {
		if e := o.surfaces[key]; e != nil && e.s.Compare != nil {
			if a := o.surfaces[e.s.Compare.Keys[0]]; a != nil {
				for _, native := range a.s.Axes {
					if p, ok := a.s.AxisTicks[native.ID]; ok && native.ID == axis.ID {
						return displayFollowingTicks(axis, displayAxisFor(owner, e.s.Compare.Keys[0], native), p)
					}
				}
			}
		}
	}
	return displaycore.TickPlan{}, false
}

func displayCompareMeasuredInsets(in displayPlotInput, width, height int, shared ...displayCompareReferenceTicks) (displayPlotInsets, map[string]displaycore.TickPlan, displaycore.TickPlan) {
	f := in.Typography
	dpi := f.PlotTitle.DPI
	pad, gap := max(2, displayPixelDIP(3, dpi)), max(2, displayPixelDIP(5, dpi))
	hasColor := in.Color != nil && !in.Color.Hidden
	var xp, yp displaycore.TickPlan
	var hasX, hasY bool
	if len(shared) != 0 && in.FollowA {
		ref := shared[0]
		xp, hasX = displayFollowingTicks(in.X, ref.input.X, ref.plans[ref.input.X.ID])
		yp, hasY = displayFollowingTicks(in.Y, ref.input.Y, ref.plans[ref.input.Y.ID])
	}
	if !hasX {
		xp = displayEqualAxisTicks(f.XTick, in.X, width, true, in.Position)
	}
	if !hasY {
		yp = displayEqualAxisTicks(f.YTick, in.Y, height, false, nil)
	}
	plans := map[string]displaycore.TickPlan{in.X.ID: xp, in.Y.ID: yp}
	ins := displayPlotInsets{pad, f.PlotTitle.Height + gap, pad, pad}
	var cp displaycore.TickPlan
	if hasColor {
		cp = displayEqualColorTicks(f.ColorTick, *in.Color, height)
	}
	plotTitleWidth := f.PlotTitle.Measure(in.CompareTitle).Width
	if plotTitleWidth > width {
		ins.Left = max(ins.Left, (plotTitleWidth-width+1)/2+pad)
		ins.Right = max(ins.Right, (plotTitleWidth-width+1)/2+pad)
	}
	if !in.X.HideAxis {
		ins.Bottom += displayCompareHorizontalRows(in, xp, yp, cp, width).bottom
		if !in.X.HideLabels {
			for _, tick := range xp.Ticks {
				frac := in.X.Fraction(tick.Value)
				if in.Position != nil {
					frac = in.Position(tick.Value)
					if in.X.Reverse {
						frac = 1 - frac
					}
				}
				if math.IsNaN(frac) || math.IsInf(frac, 0) {
					continue
				}
				w := (f.XTick.Measure(tick.Label).Width + 1) / 2
				ins.Left = max(ins.Left, w-int(math.Round(frac*float64(max(1, width-1))))+pad)
				ins.Right = max(ins.Right, w-int(math.Round((1-frac)*float64(max(1, width-1))))+pad)
			}
		}
		if !in.X.HideTitle {
			w := f.XTitle.Measure(displayAxisTitle(in.X)).Width
			if w > width {
				ins.Left, ins.Right = max(ins.Left, (w-width+1)/2+pad), max(ins.Right, (w-width+1)/2+pad)
			}
		}
	}
	if !in.Y.HideAxis {
		left := displayPixelDIP(displayEqualAxisTickDIP(in.Y), dpi)
		if !in.Y.HideLabels {
			left += displayPixelDIP(displayCompareYLabelGapDIP, dpi) + displayCompareLabelWidth(yp, f.YTick)
			ins.Top = max(ins.Top, (f.YTick.Height+1)/2+pad)
			ins.Bottom = max(ins.Bottom, (f.YTick.Height+1)/2+pad)
		}
		if !in.Y.HideTitle {
			// A rotated run needs its line height in the horizontal gutter.
			left += gap + f.YTitle.Height
			w := f.YTitle.Measure(displayAxisTitle(in.Y)).Width
			if w > height {
				ins.Top, ins.Bottom = max(ins.Top, (w-height+1)/2+pad), max(ins.Bottom, (w-height+1)/2+pad)
			}
		}
		ins.Left = max(ins.Left, left+pad)
	}
	if hasColor {
		barW := displayPixelDIP(displayCompareBarWidthDIP, dpi)
		labels := displayCompareLabelWidth(cp, f.ColorTick)
		title := f.ColorTitle.Measure(in.LegendTitle).Width
		col := max(title, barW+displayPixelDIP(in.Color.MajorTickDIP(), dpi)+displayPixelDIP(displayCompareBarLabelGapDIP, dpi)+labels)
		ins.Right = max(ins.Right, displayPixelDIP(displayCompareBarGapDIP, dpi)+col+pad)
		ins.Top = max(ins.Top, f.ColorTitle.Height+gap+(f.ColorTick.Height+1)/2)
		ins.Bottom = max(ins.Bottom, (f.ColorTick.Height+1)/2+pad)
	}
	return ins, plans, cp
}

// Keep the three plot rectangles aligned and equal but only reserve the
// horizontal decorations a panel actually uses (not all panels' maximum).
func displayCompareSceneLayouts(m displayPlotMetrics, outer RECT, inputs []displayPlotInput) []displayPlotLayout {
	n := len(inputs)
	if n == 0 {
		return nil
	}
	width, height := max(0, int(outer.Right-outer.Left)), max(0, int(outer.Bottom-outer.Top))
	gap := max(0, min(displayPixelDIP(displayComparePanelGapDIP, m.DPI), (width-n)/max(1, n-1)))
	avail := max(0, width-gap*(n-1))
	ins := make([]displayPlotInsets, n)
	var commonTop, commonBottom int
	plotW, plotH := max(1, avail/n), max(1, height)
	for pass := 0; pass < 4; pass++ {
		horizontal := 0
		ref := displayCompareReferenceTicks{input: inputs[0]}
		for i, in := range inputs {
			a, plans, _ := displayCompareMeasuredInsets(in, plotW, plotH, ref)
			if i == 0 {
				ref.plans = plans
			}
			ins[i].Left, ins[i].Right = max(ins[i].Left, a.Left), max(ins[i].Right, a.Right)
			commonTop, commonBottom = max(commonTop, a.Top), max(commonBottom, a.Bottom)
			horizontal += ins[i].Left + ins[i].Right
		}
		w, h := max(1, (avail-horizontal)/n), max(1, height-commonTop-commonBottom)
		if w == plotW && h == plotH {
			break
		}
		plotW, plotH = w, h
	}
	// Far below minimum window size give the plot a contained fallback, and
	// let the existing diagnostics indicate that requested labels need room.
	decorWidth := 0
	for _, a := range ins {
		decorWidth += a.Left + a.Right
	}
	limit := max(0, avail-n*plotW)
	if decorWidth > limit {
		for i, a := range ins {
			ins[i].Left, ins[i].Right = a.Left*limit/decorWidth, a.Right*limit/decorWidth
		}
	}
	commonTop = min(commonTop, max(0, height-plotH))
	commonBottom = min(commonBottom, max(0, height-plotH-commonTop))
	out := make([]displayPlotLayout, n)
	x0 := int(outer.Left)
	ref := displayCompareReferenceTicks{input: inputs[0]}
	for i, in := range inputs {
		a := ins[i]
		cell := RECT{Left: int32(x0), Top: outer.Top, Right: min(outer.Right, int32(x0+a.Left+plotW+a.Right)), Bottom: outer.Bottom}
		plot := RECT{Left: min(cell.Right, int32(x0+a.Left)), Top: outer.Top + int32(commonTop), Right: min(cell.Right, int32(x0+a.Left+plotW)), Bottom: min(outer.Bottom, outer.Top+int32(commonTop+plotH))}
		_, plans, cp := displayCompareMeasuredInsets(in, int(plot.Right-plot.Left), int(plot.Bottom-plot.Top), ref)
		if i == 0 {
			ref.plans = plans
		}
		bar, gradient := RECT{}, RECT{}
		if in.Color != nil && !in.Color.Hidden {
			f := in.Typography
			left := plot.Right + int32(displayPixelDIP(displayCompareBarGapDIP, m.DPI))
			gradient = RECT{Left: left, Top: plot.Top, Right: min(cell.Right, left+int32(displayPixelDIP(displayCompareBarWidthDIP, m.DPI))), Bottom: plot.Bottom}
			bar = RECT{Left: left, Top: plot.Top - int32(f.ColorTitle.Height+(f.ColorTick.Height+1)/2+displayPixelDIP(5, m.DPI)), Right: cell.Right, Bottom: min(cell.Bottom, plot.Bottom+int32((f.ColorTick.Height+1)/2))}
		}
		regions := map[string]RECT{
			in.X.ID: {Left: plot.Left, Top: plot.Bottom, Right: plot.Right, Bottom: cell.Bottom},
			in.Y.ID: {Left: cell.Left, Top: plot.Top, Right: plot.Left, Bottom: plot.Bottom},
		}
		rows := displayCompareHorizontalRows(in, plans[in.X.ID], plans[in.Y.ID], cp, int(plot.Right-plot.Left))
		out[i] = displayPlotLayout{Outer: cell, Plot: plot, ColorRegion: bar, ColorGradient: gradient, Regions: regions, AxisTicks: plans, ColorTicks: cp, Metrics: m, Compare: true, CompareTitle: in.CompareTitle, LegendTitle: in.LegendTitle, Typography: in.Typography.frozen(), HorizontalAxis: rows}
		x0 = int(cell.Right) + gap
	}
	frozen := displayFreezeMetrics(m)
	for i := range out {
		out[i].Metrics = frozen
	}
	return out
}

func displayDraftTickPlan(owner uintptr, key string, axis *displaycore.Axis, color *displaycore.ColorMapping) displaycore.TickPlan {
	o := displayOwners[owner]
	if o == nil || o.surfaces[key] == nil {
		return displaycore.TickPlan{}
	}
	e := o.surfaces[key]
	dc, done := displayMeasurementDC(owner)
	defer done()
	m := displayMetrics(dc, displayDPI(owner))
	equal := e.s.equalTicks
	if color != nil {
		if equal {
			f, release := displayCompareTypographyFor(dc, m.DPI, displayPlotInput{Color: color}, 0)
			defer release()
			return displayEqualColorTicks(f.ColorTick, *color, int(e.s.Plot.Bottom-e.s.Plot.Top))
		}
		return displayColorTickPlan(m, *color, int(e.s.Plot.Bottom-e.s.Plot.Top))
	}
	if axis == nil {
		return displaycore.TickPlan{}
	}
	horizontal := len(e.s.Axes) > 0 && e.s.Axes[0].ID == axis.ID
	pixels := int(e.s.Plot.Bottom - e.s.Plot.Top)
	var position func(float64) float64
	if horizontal {
		pixels = int(e.s.Plot.Right - e.s.Plot.Left)
		if axis.BaseUnit != "m" {
			position = seismicSurfacePosition(owner, key, axis.View)
		}
	}
	if key == traceScientificWaveKey || key == traceScientificSpecKey {
		em := float64(displayPixelDIP(traceScientificFontPT(*axis, false)*96/72, m.DPI))
		amplitude := axis.ID == "amplitude" && (key == traceScientificWaveKey || axis.Unit != "dB")
		return traceScientificAxisTicks(*axis, float64(pixels), em, horizontal, amplitude)
	}
	if equal {
		f, release := displayCompareTypographyFor(dc, m.DPI, displayPlotInput{X: *axis, Y: *axis}, 0)
		defer release()
		return displayEqualAxisTicks(f.XTick, *axis, pixels, horizontal, position)
	}
	return displayAxisTickPlan(m, *axis, pixels, horizontal, position)
}

func seismicSurfacePosition(owner uintptr, key string, view displaycore.Range) func(float64) float64 {
	if owner != compareHwnd || compareMode == 2 {
		return nil
	}
	for panel := 0; panel < 3; panel++ {
		if key != seismicCompareKey(panel) {
			continue
		}
		p := seismicComparePanel(panel)
		if p == nil || len(p.coords) < 2 {
			return nil
		}
		coords := p.coords
		if _, _, ok := seismicComparePartial(panel, false); ok {
			coords = compareA.coords
		}
		// A draft range may be a subrange of the current rendered axis.
		lo, lok := seismicAcrossFraction(coords, view.Min)
		hi, hok := seismicAcrossFraction(coords, view.Max)
		if !lok || !hok || hi <= lo {
			return nil
		}
		return func(v float64) float64 {
			f, ok := seismicAcrossFraction(coords, v)
			if !ok {
				return math.NaN()
			}
			return (f - lo) / (hi - lo)
		}
	}
	return nil
}

func seismicMainLayout() displayPlotLayout {
	cw, ch := clientSize(hwnd)
	dpi := displayDPI(hwnd)
	x, y := seismicMainAxes()
	x = displayAxisFor(hwnd, mainDisplayKey, x)
	y = displayAxisFor(hwnd, mainDisplayKey, y)
	c := seismicCurrentColor(hwnd, mainDisplayKey, seismicDisplayColor(lastStats, currentPalette(), agc))
	pt, equal := seismicPlotTitleFont(hwnd, mainDisplayKey), seismicMainEqualTicks()
	key := displayMainLayoutKey{Owner: hwnd, Font: hFont, Width: cw, Height: ch, DPI: dpi, Equal: equal, X: x, Y: y, Color: displayLayoutColorIdentity(c), PlotTitleFontPT: pt}
	if displayMainLayoutCache.valid && displayMainLayoutCache.key == key {
		return displayMainLayoutCache.plan
	}
	dc, done := displayMeasurementDC(hwnd)
	defer done()
	m := displayMetrics(dc, dpi)
	outer := RECT{Top: legacyToolbarHeight, Right: int32(cw), Bottom: int32(max(legacyToolbarHeight+2, ch-legacyStatusHeight))}
	input := displayPlotInput{X: x, Y: y, Color: &c, CompareTitle: "二维剖面", LegendTitle: displayColorTitle(c)}
	var p displayPlotLayout
	if equal {
		f, release := displayCompareTypographyFor(dc, m.DPI, input, pt)
		defer release()
		input.Typography = f
		p = displayCompareSceneLayouts(m, outer, []displayPlotInput{input})[0]
	} else {
		p = displaySceneLayouts(m, outer, []displayPlotInput{input})[0]
	}
	displayMainLayoutCache.key, displayMainLayoutCache.plan, displayMainLayoutCache.valid = key, p, true
	return p
}

func seismicCompareLayouts() []displayPlotLayout {
	cw, ch := clientSize(compareHwnd)
	dpi := displayDPI(compareHwnd)
	n := compareDisplayPanelCount(compareWorkspaceActive, compareActiveB(), compareShowDiff)
	key := seismicCompareLayoutKey(cw, ch, dpi, n)
	if displayCompareLayoutCache.valid && displayCompareLayoutCache.key == key {
		return displayCompareLayoutCache.plans
	}
	dc, done := displayMeasurementDC(compareHwnd)
	defer done()
	m := displayMetrics(dc, dpi)
	inputs := make([]displayPlotInput, n)
	equal := seismicCompareEqualTicks()
	for i := range inputs {
		x, y := seismicCompareEffectiveAxes(i)
		key := seismicCompareKey(i)
		if equal {
			y = seismicCompareCompactYAxis(i, y)
		}
		var position func(float64) float64
		if x.BaseUnit != "m" {
			position = seismicSurfacePosition(compareHwnd, key, x.View)
		}
		inputs[i] = displayPlotInput{X: x, Y: y, Position: position, CompareTitle: seismicComparePlotTitle(i), FollowA: seismicCompareFollowsAxes(i)}
		if p := seismicComparePanel(i); p != nil {
			c := seismicPanelColor(i, *p)
			if poststackWaveActive() {
				c = poststackWaveLegendLayout(c)
			}
			inputs[i].Color = &c
			inputs[i].LegendTitle = displayColorTitle(c)
			if poststackWaveActive() {
				inputs[i].LegendTitle = "极性"
			}
		}
		if equal {
			f, release := displayCompareTypographyFor(dc, dpi, inputs[i], seismicPlotTitleFont(compareHwnd, key))
			defer release()
			inputs[i].Typography = f
		}
	}
	var plans []displayPlotLayout
	if equal {
		// The current native toolbar's third row ends at pixel 90.
		top := min(ch, 90+displayPixelDIP(12, dpi))
		plans = displayCompareSceneLayouts(m, RECT{Top: int32(top), Right: int32(cw), Bottom: int32(ch)}, inputs)
	} else {
		plans = displaySceneLayouts(m, RECT{Top: 118, Right: int32(cw), Bottom: int32(max(120, ch))}, inputs)
	}
	displayCompareLayoutCache.key, displayCompareLayoutCache.plans, displayCompareLayoutCache.valid = key, plans, true
	return plans
}

func displayColorTitle(m displaycore.ColorMapping) string {
	title := strings.TrimSpace(m.Title)
	if title == "" {
		title = "振幅"
	}
	if unit := strings.TrimSpace(m.Unit); unit != "" {
		return title + "（" + unit + "）"
	}
	return title
}

func displayTranslateRect(r RECT, dx, dy int32) RECT {
	r.Left += dx
	r.Right += dx
	r.Top += dy
	r.Bottom += dy
	return r
}

func displayTranslateLayout(p displayPlotLayout, plot RECT) displayPlotLayout {
	dx, dy := plot.Left-p.Plot.Left, plot.Top-p.Plot.Top
	p.Outer = displayTranslateRect(p.Outer, dx, dy)
	p.ColorRegion = displayTranslateRect(p.ColorRegion, dx, dy)
	p.ColorGradient = displayTranslateRect(p.ColorGradient, dx, dy)
	regions := make(map[string]RECT, len(p.Regions))
	for id, r := range p.Regions {
		regions[id] = displayTranslateRect(r, dx, dy)
	}
	p.Regions = regions
	p.Plot = plot
	return p
}

func seismicCompareLayout(panel int, r RECT) displayPlotLayout {
	plans := seismicCompareLayouts()
	if panel < 0 || panel >= len(plans) {
		return displayPlotLayout{Plot: r}
	}
	return displayTranslateLayout(plans[panel], r)
}
