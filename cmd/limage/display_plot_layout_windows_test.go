//go:build windows

package main

import (
	"fmt"
	"math"
	"reflect"
	"runtime"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	display "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
)

func displayLayoutTestMetrics(dpi int) displayPlotMetrics {
	h := displayPixelDIP(16, dpi)
	return displayPlotMetrics{DPI: dpi, Height: h, Measure: func(s string) displayTextSize {
		return displayTextSize{displayPixelDIP(float64(utf8.RuneCountInString(s)*7), dpi), h}
	}}
}

func TestDisplayMeasuredLayoutLabelsAndLegendsRemainInsidePanels(t *testing.T) {
	for _, dpi := range []int{96, 120, 144} {
		for _, width := range []int{960, 1360, 1920} {
			t.Run(fmt.Sprintf("dpi%d_width%d", dpi, width), func(t *testing.T) {
				m := displayLayoutTestMetrics(dpi)
				x := seismicAxis("x", "Crossline", "", display.Range{Min: -1234567.25, Max: 1234567.75}, display.Range{Min: -1234567.25, Max: 1234567.75}, false)
				x.Format, x.Digits = display.ScientificFormat, 3
				y := seismicAxis("y", "时间", "ms", display.Range{Min: -800.5, Max: 1500.5}, display.Range{Min: -800.5, Max: 1500.5}, true)
				y.Format, y.Digits = display.FixedFormat, 2
				color := display.ColorMapping{Title: "振幅", Range: display.Range{Min: -38972.71484375, Max: 47235.31640625}, Format: display.ScientificFormat, Digits: 2, TickLength: 12, TickLengthSet: true}
				in := []displayPlotInput{{X: x, Y: y, Color: &color}, {X: x, Y: y}, {X: x, Y: y, Color: &color}}
				layouts := displaySceneLayouts(m, RECT{Top: 118, Right: int32(width), Bottom: 900}, in)
				for i, p := range layouts {
					if p.Plot.Left < p.Outer.Left || p.Plot.Right > p.Outer.Right || p.Plot.Top < p.Outer.Top || p.Plot.Bottom > p.Outer.Bottom {
						t.Fatalf("plot escapes panel: %+v", p)
					}
					if p.Plot.Top != layouts[0].Plot.Top || p.Plot.Bottom != layouts[0].Plot.Bottom || p.Plot.Right-p.Plot.Left != layouts[0].Plot.Right-layouts[0].Plot.Left {
						t.Fatal("A/B/difference data plots misaligned")
					}
					if p.Regions[x.ID].Top < p.Plot.Bottom || p.Regions[y.ID].Right > p.Plot.Left {
						t.Fatal("axis gestures can steal plot input")
					}
					for _, tick := range p.AxisTicks[x.ID].Ticks {
						pos := x.Pixel(tick.Value, int(p.Plot.Left), int(p.Plot.Right))
						w := m.Measure(tick.Label).Width
						if pos-w/2 < int(p.Outer.Left) || pos+(w+1)/2 > int(p.Outer.Right) {
							t.Fatalf("X label clipped: %s %+v", tick.Label, p)
						}
					}
					for _, tick := range p.AxisTicks[y.ID].Ticks {
						left := int(p.Plot.Left) - displayPixelDIP(y.MajorTickDIP(), dpi) - displayPixelDIP(5, dpi) - m.Measure(tick.Label).Width
						if left < int(p.Outer.Left) {
							t.Fatalf("Y label clipped: %s %+v", tick.Label, p)
						}
					}
					if in[i].Color != nil {
						gap := displayPixelDIP(5, dpi)
						if int(p.ColorRegion.Top)+m.Height+gap > int(p.Plot.Top)-(m.Height+1)/2 {
							t.Fatal("legend title overlaps upper endpoint")
						}
						if int(p.Plot.Top)-(m.Height+1)/2-gap-m.Height < int(p.Outer.Top) {
							t.Fatal("Y heading escapes top margin")
						}
						labels := append(append([]display.Tick(nil), p.ColorTicks.Ticks...), displayColorEndpoints(*in[i].Color)...)
						for _, tick := range labels {
							right := int(p.ColorRegion.Left) + displayPixelDIP(16, dpi) + displayPixelDIP(color.MajorTickDIP(), dpi) + displayPixelDIP(5, dpi) + m.Measure(tick.Label).Width
							if right > int(p.Outer.Right) {
								t.Fatalf("legend label clipped: %s %+v", tick.Label, p)
							}
						}
					}
				}
			})
		}
	}
}

func TestCompareEqualLayoutMeasuresPerPanelAndKeepsAlignedImages(t *testing.T) {
	for _, dpi := range []int{96, 120, 144} {
		t.Run(fmt.Sprintf("dpi%d", dpi), func(t *testing.T) {
			m := displayLayoutTestMetrics(dpi)
			f := displayPlotFontMetrics{PT: 10, DPI: dpi, Ascent: m.Height - 2, Height: m.Height, Measure: m.Measure}
			fonts := displayCompareTypography{f, f, f, f, f, f, f}
			x := seismicAxis("x", "Crossline", "", display.Range{Min: 184, Max: 2456}, display.Range{Min: 184, Max: 2456}, false)
			y := seismicAxis("time", "时间", "ms", display.Range{Min: 800, Max: 1500}, display.Range{Min: 800, Max: 1500}, true)
			y.Divisions, x.Divisions = 4, 4
			c := display.ColorMapping{Range: display.Range{Min: -20586, Max: 26684}, Divisions: 5}
			in := []displayPlotInput{{X: x, Y: y, Color: &c, CompareTitle: "A · Inline 1870", Typography: fonts}, {X: x, Y: y, CompareTitle: "B · Inline 1870", Typography: fonts}, {X: x, Y: y, Color: &c, CompareTitle: "A−B · Inline 1870", Typography: fonts}}
			p := displayCompareSceneLayouts(m, RECT{Top: 100, Right: 1370, Bottom: 760}, in)
			if p[0].Outer.Right-p[0].Plot.Right <= p[1].Outer.Right-p[1].Plot.Right {
				t.Fatal("panel without a legend reserved the other panel's legend width")
			}
			for i, l := range p {
				if !l.Compare || l.Plot.Right-l.Plot.Left != p[0].Plot.Right-p[0].Plot.Left || l.Plot.Top != p[0].Plot.Top || l.Plot.Bottom != p[0].Plot.Bottom {
					t.Fatalf("unaligned plot %d: %+v", i, l)
				}
				if l.Plot.Left < l.Outer.Left || l.Plot.Right > l.Outer.Right || l.Plot.Top < l.Outer.Top || l.Plot.Bottom > l.Outer.Bottom {
					t.Fatalf("plot escapes panel %d", i)
				}
				if i > 0 && int(l.Outer.Left-p[i-1].Outer.Right) != displayPixelDIP(3, dpi) {
					t.Fatalf("non-compact group gap")
				}
				xp, yp := l.AxisTicks[x.ID], l.AxisTicks[y.ID]
				if len(xp.Ticks) != 5 || len(yp.Ticks) != 5 || yp.Ticks[0].Value != 800 || yp.Ticks[4].Value != 1500 || yp.Step != 175 {
					t.Fatalf("axis ticks %+v %+v", xp, yp)
				}
				if in[i].Color != nil {
					if len(l.ColorTicks.Ticks) != 6 || l.ColorGradient.Top != l.Plot.Top || l.ColorGradient.Bottom != l.Plot.Bottom || int(l.ColorGradient.Left-l.Plot.Right) != displayPixelDIP(7, dpi) {
						t.Fatalf("legend not aligned: %+v", l)
					}
					for _, tick := range l.ColorTicks.Ticks {
						labelRight := int(l.ColorGradient.Right) + displayPixelDIP(c.MajorTickDIP()+5, dpi) + f.Measure(tick.Label).Width
						if labelRight > int(l.Outer.Right) {
							t.Fatalf("legend tick %s clipped", tick.Label)
						}
					}
				}
			}
		})
	}
}

func TestCompareLabelPaddingUsesDIPAndProtectsLargeEndLabels(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	for _, dpi := range []int{96, 120, 144} {
		x := display.Axis{TickLengthSet: true, TickLength: 6}
		y := display.Axis{}
		makeMetric := func(pt float64) (displayPlotFontMetrics, func()) {
			dc, done := displayMeasurementDC(0)
			font := displayNewPlotFonts(pt, dpi, 0)
			return displayPlotFontMeasure(dc, dpi, pt, font), func() { font.close(); done() }
		}
		xFont, done := makeMetric(10)
		defer done()
		yFont, done := makeMetric(36)
		defer done()
		f := displayCompareTypography{
			XTick: xFont, YTick: yFont, XTitle: xFont,
		}
		const label = "2456"
		xp := display.TickPlan{Ticks: []display.Tick{{Value: 2456, Label: label}}}
		ink := xFont.inkBounds(label)
		if ink.top >= 0 || ink.top <= -xFont.Ascent {
			t.Fatalf("%d DPI: no measurable difference between numeric ink and line-box top: %+v, ascent %d", dpi, ink, xFont.Ascent)
		}
		rows := displayCompareHorizontalRows(displayPlotInput{X: x, Y: display.Axis{HideAxis: true}, Typography: f}, xp, display.TickPlan{}, display.TickPlan{}, 300)
		if got := rows.baseline + ink.top - displayPixelDIP(6, dpi); got != 1+displayPixelDIP(4, dpi) || rows.titleTop <= rows.baseline+ink.bottom {
			t.Errorf("%d DPI: numeric ink padding = %d, rows %+v", dpi, got, rows)
		}
		yp := display.TickPlan{Ticks: []display.Tick{{Value: 1500, Label: "1500"}}}
		withY := displayCompareHorizontalRows(displayPlotInput{X: x, Y: y, Typography: f}, xp, yp, display.TickPlan{}, 300)
		if got := withY.baseline + ink.top; got <= yFont.Ascent-yFont.Height/2+yFont.inkBounds("1500").bottom {
			t.Errorf("%d DPI: X label touches vertical endpoint annotation", dpi)
		}
		verifyCompareNumericInkGaps(t, dpi)
	}
}

// Inspect the pixels laid down by the production GDI painter. Identical
// numeric text on both axes makes this about visible ink, not line boxes.
func verifyCompareNumericInkGaps(t *testing.T, dpi int) {
	t.Helper()
	const size = 256
	dc, _, _ := pCreateCompatibleDC.Call(0)
	if dc == 0 {
		t.Fatal("could not create GDI axis test DC")
	}
	defer pDeleteDC.Call(dc)
	bmi := BITMAPINFO{Header: BITMAPINFOHEADER{Size: uint32(unsafe.Sizeof(BITMAPINFOHEADER{})), Width: size, Height: -size, Planes: 1, BitCount: 32, Compression: BI_RGB}}
	var bits uintptr
	bm, _, _ := pCreateDIBSection.Call(dc, uintptr(unsafe.Pointer(&bmi)), DIB_RGB_COLORS, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bm == 0 || bits == 0 {
		t.Fatal("could not create GDI axis test bitmap")
	}
	defer pDeleteObject.Call(bm)
	old, _, _ := pSelectObject.Call(dc, bm)
	defer pSelectObject.Call(dc, old)
	pixels := unsafe.Slice((*uint32)(unsafe.Pointer(bits)), size*size)
	for i := range pixels {
		pixels[i] = 0x00ffffff
	}
	fonts := displayNewPlotFonts(10, dpi, 0)
	defer fonts.close()
	m := displayPlotFontMeasure(dc, dpi, 10, fonts)
	x := display.Axis{ID: "x", View: display.Range{Min: 0, Max: 2}, HideTitle: true}
	y := x
	y.ID, y.Down = "y", true
	plan := display.TickPlan{Ticks: []display.Tick{{Value: 1, Label: "1500"}}}
	p := RECT{Left: 80, Top: 30, Right: 220, Bottom: 197}
	layout := displayPlotLayout{Plot: p, Typography: displayCompareTypography{XTick: m, YTick: m}, AxisTicks: map[string]display.TickPlan{"x": plan, "y": plan}}
	displayDrawMeasuredCompareAxes(dc, layout, x, y, nil)
	gdi32.NewProc("GdiFlush").Call()
	inkAt := func(x, y int) bool { return pixels[y*size+x]&0xff < 210 }
	b, l := int(p.Bottom)-1, int(p.Left)
	xc, yc := x.Pixel(1, l, int(p.Right)-1), y.Pixel(1, int(p.Top), b)
	length := displayPixelDIP(6, dpi)
	// Exclude the strokes themselves. Search for the closest visible text
	// pixel in each label band, then count the white pixels in between.
	xGap, yGap := -1, -1
	for dy := length + 1; dy < 45 && xGap < 0; dy++ {
		for px := xc - 30; px <= xc+30; px++ {
			if inkAt(px, b+dy) {
				xGap = dy - length - 1
				break
			}
		}
	}
	for dx := length + 1; dx < 70 && yGap < 0; dx++ {
		for py := yc - 22; py <= yc+22; py++ {
			if inkAt(l-dx, py) {
				yGap = dx - length - 1
				break
			}
		}
	}
	t.Logf("%d DPI GDI numeric ink gaps: horizontal %d px, vertical %d px", dpi, xGap, yGap)
	if xGap < displayPixelDIP(3, dpi) || yGap <= 0 || math.Abs(float64(xGap-yGap)) > float64(displayPixelDIP(2, dpi)) {
		t.Errorf("%d DPI: horizontal and vertical numeric ink gaps not comparable", dpi)
	}
}

func TestCompareDenseEqualLabelsPreserveEndpointsAndAllMarks(t *testing.T) {
	p := display.BuildEqualTickPlan(display.Range{Min: -22.2, Max: 23.1}, 20, display.TickOptions{Pixels: 140, Measure: func(string) int { return 34 }})
	if len(p.Ticks) != 21 || p.Warning == "" {
		t.Fatalf("dense equal plan %+v", p)
	}
	for _, reverse := range []bool{false, true} {
		a := display.Axis{View: display.Range{Min: -22.2, Max: 23.1}, Reverse: reverse}
		vis := displayEqualTickLabelVisibility(p.Ticks, func(v float64) int { return a.Pixel(v, 0, 140) }, func(string) int { return 34 }, 5)
		if !vis[0] || !vis[len(vis)-1] || len(vis) != len(p.Ticks) {
			t.Fatalf("endpoints suppressed %+v", vis)
		}
	}
	a := display.Axis{}
	if displayEqualAxisTickDIP(a) != 6 {
		t.Fatal("new axis length default is not 6 DIP")
	}
	a.TickLengthSet = true
	if displayEqualAxisTickDIP(a) != 0 {
		t.Fatal("explicit zero restored a tick length")
	}
}

func TestCompareNativePlotFontUsesArialAndMeasuresDPI(t *testing.T) {
	dc, _, _ := pCreateCompatibleDC.Call(0)
	if dc == 0 {
		t.Fatal("could not create measurement DC")
	}
	defer pDeleteDC.Call(dc)
	textFace := gdi32.NewProc("GetTextFaceW")
	var earlier displayTextSize
	for _, dpi := range []int{96, 120, 144} {
		font := displayNewPlotFonts(displayPlotFontPT(0, false), dpi, 0)
		restore := displaySelectPlotFont(dc, font.latin)
		var face [64]uint16
		n, _, _ := textFace.Call(dc, uintptr(len(face)), uintptr(unsafe.Pointer(&face[0])))
		restore()
		if n == 0 || n > uintptr(len(face)) {
			font.close()
			t.Fatalf("unable to identify Latin font: %d", n)
		}
		end := 0
		for end < len(face) && face[end] != 0 {
			end++
		}
		faceName := string(utf16.Decode(face[:end]))
		if faceName != "Arial" {
			font.close()
			t.Fatalf("Latin font %q was not Arial", faceName)
		}
		m := displayPlotFontMeasure(dc, dpi, displayPlotFontPT(0, false), font)
		size := m.Measure("时间 (ms) −123.45")
		font.close()
		if size.Width <= earlier.Width || size.Height <= earlier.Height {
			t.Fatalf("DPI did not increase measured glyph size: %d %+v -> %+v", dpi, earlier, size)
		}
		earlier = size
	}
}

func TestDisplayColorbarEndpointsRemainAnnotationsAndAvoidGridOverlap(t *testing.T) {
	m := displayLayoutTestMetrics(120)
	c := display.ColorMapping{Range: display.Range{Min: -38972.71484375, Max: 47235.31640625}, Step: 10000, Format: display.AutoFormat}
	p := displayColorTickPlan(m, c, 600)
	before := append([]display.Tick(nil), p.Ticks...)
	labels := displayColorbarLabels(c, p, m, 0, 600)
	if len(labels) < 2 || labels[0].Value != c.Range.Min || labels[1].Value != c.Range.Max {
		t.Fatalf("mapping endpoints missing: %+v", labels)
	}
	if labels[0].Label != "-38973" || labels[1].Label != "47235" {
		t.Fatalf("automatic endpoint labels are not compact: %+v", labels[:2])
	}
	if !reflect.DeepEqual(before, p.Ticks) {
		t.Fatal("annotations modified regular main ticks")
	}
	for i, a := range labels {
		for j, b := range labels {
			if j <= i {
				continue
			}
			d := math.Abs(c.Range.Fraction(a.Value)-c.Range.Fraction(b.Value)) * 600
			if d < float64(m.Height+displayPixelDIP(5, m.DPI))-1 {
				t.Fatalf("overlapping color labels: %+v %+v", a, b)
			}
		}
	}
	// A bound exactly on the regular grid is still drawn only once. A grid
	// label one pixel from a bound must not obscure the complete bound text.
	c.Range = display.Range{Min: 0, Max: 10}
	p = display.TickPlan{Step: 1, Ticks: []display.Tick{{Value: 0, Label: "0"}, {Value: 0.01, Label: "0.01"}, {Value: 5, Label: "5"}, {Value: 10, Label: "10"}}}
	labels = displayColorbarLabels(c, p, m, 0, 200)
	if len(labels) != 3 {
		t.Fatalf("duplicate/near-bound labels were not suppressed: %+v", labels)
	}
	c.Range = display.Range{Min: -38972.71484375, Max: 47235.31640625}
	c.Format, c.Digits = display.ScientificFormat, 2
	labels = displayColorEndpoints(c)
	if labels[0].Label != "-3.90e+04" || labels[1].Label != "4.72e+04" {
		t.Fatalf("explicit legend format was ignored: %+v", labels)
	}
}

func TestDisplayManualTickCollisionSuppressesGlyphsNotGrid(t *testing.T) {
	m := displayLayoutTestMetrics(96)
	a := seismicAxis("x", "X", "", display.Range{Min: 0, Max: 10}, display.Range{Min: 0, Max: 10}, false)
	a.Step, a.Format, a.Digits = 1, display.FixedFormat, 3
	p := displayAxisTickPlan(m, a, 120, true, nil)
	if p.Warning == "" {
		t.Fatal("overlapping manual interval has no explicit warning")
	}
	if len(p.Ticks) != 11 || p.Step != 1 {
		t.Fatalf("manual grid silently changed: %+v", p)
	}
	for _, reverse := range []bool{false, true} {
		a.Reverse = reverse
		pixel := func(v float64) int { return a.Pixel(v, 0, 120) }
		shown := displayTickLabelVisibility(p.Ticks, pixel, func(s string) int { return m.Measure(s).Width }, 5)
		count := 0
		for i, on := range shown {
			if !on {
				continue
			}
			count++
			for j, other := range shown {
				if j <= i || !other {
					continue
				}
				distance := absInt(pixel(p.Ticks[i].Value) - pixel(p.Ticks[j].Value))
				need := (m.Measure(p.Ticks[i].Label).Width+m.Measure(p.Ticks[j].Label).Width)/2 + 5
				if distance < need {
					t.Fatal("manual labels still overlap")
				}
			}
		}
		if count == 0 || count >= len(p.Ticks) {
			t.Fatalf("invalid collision selection: %+v", shown)
		}
	}
	if len(p.Ticks) != 11 || p.Step != 1 {
		t.Fatal("glyph selection modified tick plan")
	}
}

func TestDisplayMeasuredLayoutReflowsForStyleAndDPIWithoutChangingData(t *testing.T) {
	x := seismicAxis("x", "Crossline", "", display.Range{Min: 184, Max: 2456}, display.Range{Min: 184, Max: 2456}, false)
	y := seismicAxis("y", "时间", "ms", display.Range{Min: 800, Max: 1500}, display.Range{Min: 800, Max: 1500}, true)
	in := displayPlotInput{X: x, Y: y}
	m := displayLayoutTestMetrics(96)
	outer := RECT{Right: 1000, Bottom: 700}
	before := displaySceneLayouts(m, outer, []displayPlotInput{in})[0]
	in.Y.TickLength, in.Y.TickLengthSet = 36, true
	in.Y.Format, in.Y.Digits = display.FixedFormat, 8
	after := displaySceneLayouts(m, outer, []displayPlotInput{in})[0]
	if after.Plot.Left <= before.Plot.Left {
		t.Fatal("long label and outer tick did not reserve measured left margin")
	}
	if in.Y.View != y.View || in.X.View != x.View {
		t.Fatal("layout changed numerical data window")
	}
	large := displaySceneLayouts(displayLayoutTestMetrics(144), outer, []displayPlotInput{in})[0]
	if large.Plot.Left <= after.Plot.Left {
		t.Fatal("DPI did not remeasure space")
	}
	if before.AxisTicks[x.ID].Step <= 0 || before.AxisTicks[y.ID].Step <= 0 {
		t.Fatal("actual auto interval not published")
	}
	for _, tick := range before.AxisTicks[x.ID].Ticks {
		q := tick.Value / before.AxisTicks[x.ID].Step
		if math.Abs(q-math.Round(q)) > 1e-8 {
			t.Fatal("range endpoint fabricated as an irregular main tick")
		}
	}
}

func TestDisplayEditorTickPlanUsesLivePlotAndUnits(t *testing.T) {
	owner := uintptr(0x7721)
	old := displayOwners[owner]
	defer func() {
		if old == nil {
			delete(displayOwners, owner)
		} else {
			displayOwners[owner] = old
		}
	}()
	x := seismicAxis("trace", "道", "", display.Range{Min: 1, Max: 1000}, display.Range{Min: 1, Max: 1000}, false)
	y := seismicAxis("time", "时间", "ms", display.Range{Min: 0, Max: 1000}, display.Range{Min: 0, Max: 1000}, true)
	displayOwners[owner] = &displayOwnerState{surfaces: map[string]*displaySurfaceState{"panel": {s: displaySurface{Axes: []display.Axis{x, y}, Plot: RECT{Left: 100, Top: 80, Right: 600, Bottom: 500}}}}}
	a := displayDraftTickPlan(owner, "panel", &y, nil)
	y.Unit = "s"
	b := displayDraftTickPlan(owner, "panel", &y, nil)
	if a.Step != b.Step || len(a.Ticks) != len(b.Ticks) {
		t.Fatalf("unit conversion moved physical ticks: %+v %+v", a, b)
	}
	if b.Step <= 0 || math.Abs(y.DisplayValue(b.Step)*1000-a.Step) > 1e-8 {
		t.Fatal("effective interval not canonical")
	}
}

func TestDisplayMeasuredMetricsSurviveMeasurementDCRelease(t *testing.T) {
	dc, done := displayMeasurementDC(0)
	m := displayMetrics(dc, 144)
	want := m.Measure("−12345.67e+09 时间 (ms)")
	frozen := displayFreezeMetrics(m)
	done()
	if got := frozen.Measure("−12345.67e+09 时间 (ms)"); got != want {
		t.Fatalf("stored metrics changed after DC release: %+v %+v", got, want)
	}
}

func TestDisplayLayoutCacheKeyTracksAxisAndLegendStyle(t *testing.T) {
	owner := compareHwnd
	prior := displayOwners[owner]
	dirty := compareBaseDirty
	defer func() {
		compareBaseDirty = dirty
		if prior == nil {
			delete(displayOwners, owner)
		} else {
			displayOwners[owner] = prior
		}
	}()
	x := seismicAxis("crossline", "Crossline", "", display.Range{Min: 184, Max: 2456}, display.Range{Min: 184, Max: 2456}, false)
	y := seismicAxis("time", "时间", "ms", display.Range{Min: 0, Max: 700}, display.Range{Min: 0, Max: 700}, true)
	c := seismicPanelColor(0, compareA)
	state := display.Settings{Axes: []display.Axis{x, y}, Color: &c}
	key, id := seismicCompareKey(0), seismicCompareIdentity(0)
	displayOwners[owner] = &displayOwnerState{surfaces: map[string]*displaySurfaceState{key: {s: displaySurface{Identity: id, Axes: state.Axes, Color: &c}, saved: &state, order: 1}}}
	a := seismicCompareLayoutKey(1360, 880, 96, 1)
	state.Axes[1].TickLength, state.Axes[1].TickLengthSet = 24, true
	b := seismicCompareLayoutKey(1360, 880, 96, 1)
	if a == b {
		t.Fatal("changed tick length reused stale layout")
	}
	if !displaySetColorbarVisible(owner, key, id, true) {
		t.Fatal("active surface refused legend toggle")
	}
	d := seismicCompareLayoutKey(1360, 880, 96, 1)
	if b == d {
		t.Fatal("changed legend visibility reused stale layout")
	}
	if d == seismicCompareLayoutKey(1360, 880, 144, 1) {
		t.Fatal("DPI reused stale layout")
	}
}
