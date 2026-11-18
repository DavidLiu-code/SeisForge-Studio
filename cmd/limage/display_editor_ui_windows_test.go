//go:build windows

package main

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
	"unsafe"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
)

func displayEditorNativeSelect(t *testing.T, w *displayEditor, id, index int) {
	t.Helper()
	h := w.controls[id]
	parent := w.handle
	if id == deSubTabs {
		parent = w.body
	}
	if h == 0 {
		t.Fatalf("no native tab control %d", id)
	}
	pSendMessageW.Call(h, TCM_FIRST+12, uintptr(index), 0) // program selection does not emit a notification
	n := NMHDR{HwndFrom: h, IdFrom: uintptr(id), Code: TCN_SELCHANGE}
	pSendMessageW.Call(parent, WM_NOTIFY, uintptr(id), uintptr(unsafe.Pointer(&n)))
	selected, _, _ := pSendMessageW.Call(h, TCM_GETCURSEL, 0, 0)
	if int(selected) != index {
		t.Fatalf("native selection = %d, want %d", int(selected), index)
	}
}

func TestDisplayEditorNativeTabRoutingKeepsOneDraftAndPreview(t *testing.T) {
	w, e := displayNativeEditorFixture(t, true)
	e.s.distance, e.s.equalTicks = true, true
	displayLayoutEditor(w)
	if !slices.Equal(w.topTabs, []int{deColor, deAxis}) || !slices.Equal(w.subTabs, []int{deRangePage, deGradientPage, deTickPage, deAppearancePage}) {
		t.Fatalf("initial native tab maps = %v / %v", w.topTabs, w.subTabs)
	}
	for _, id := range []int{deTopTabs, deSubTabs} {
		h := w.controls[id]
		if count, _, _ := pSendMessageW.Call(h, TCM_FIRST+4, 0, 0); count < 2 { // TCM_GETITEMCOUNT
			t.Fatalf("navigation is not a populated native tab: id %d count %d", id, count)
		}
	}
	before := e.seq
	setText(w.controls[deMin], "-0.83")
	for page := 1; page <= 3; page++ {
		displayEditorNativeSelect(t, w, deSubTabs, page)
		if !w.pageColor || w.section != page || w.preview.Right <= w.preview.Left || w.preview.Bottom <= w.preview.Top {
			t.Fatalf("selection did not lay out the live preview: page %d, state %d, rect %+v", page, w.section, w.preview)
		}
		if got := getText(w.controls[deMin]); got != "-0.83" {
			t.Fatalf("switching subtab redrew input as %q", got)
		}
	}
	displayEditorNativeSelect(t, w, deTopTabs, 1)
	if w.pageColor || !slices.Equal(w.subTabs, []int{deRangePage, deTickPage, deAppearancePage, deDistancePage}) {
		t.Fatalf("coordinate tabs = %v; gradient must only appear for color", w.subTabs)
	}
	displayEditorNativeSelect(t, w, deTopTabs, 0)
	if got := getText(w.controls[deMin]); got != "-0.83" || w.section != displayPageAppearance || e.seq != before {
		t.Fatalf("top-level navigation reset draft, section or stats sequence: %q, %d, %d -> %d", got, w.section, before, e.seq)
	}
}

func TestDisplayEditorPreviewBoundsAtCommonDPIs(t *testing.T) {
	w, _ := displayNativeEditorFixture(t, true)
	pSendMessageW.Call(w.controls[deRangePolicy], CB_SETCURSEL, 1, 0)
	pSendMessageW.Call(w.controls[dePercentPreset], CB_SETCURSEL, 5, 0)
	for _, dpi := range []int{96, 120, 144} {
		for _, widthDIP := range []int{580, 860} {
			pSetWindowPos.Call(w.handle, 0, 0, 0, uintptr(widthDIP*dpi/96), uintptr(580*dpi/96), 0x16)
			tests := [][]int{
				{deRangePolicy, dePercentPreset, dePercentLow, dePercentHigh, deMin, deMax, deCoverage},
				{dePreset, deReverse, deNodePosition, deNodeColor, deAdd, deDelete, deChoose},
				{deFormat, deDigits, deTickCurrent, deTickPreview},
				{deTitle, deUnit, deHide},
			}
			for section, fields := range tests {
				w.section = section
				displayLayoutEditorAtDPI(w, dpi)
				body := displayNativeRect(w.body)
				b, bLocal := body, clientRect(w.body)
				top, sub := displayNativeRect(w.controls[deTopTabs]), displayNativeRect(w.controls[deSubTabs])
				if top.Bottom > body.Top || sub.Top < body.Top || sub.Bottom >= body.Bottom || sub.Right > body.Right {
					t.Fatalf("native tabs intersect headers/body at %d DPI %d DIP: top=%+v sub=%+v body=%+v", dpi, widthDIP, top, sub, body)
				}
				p := w.preview
				if p.Right <= p.Left || p.Bottom <= p.Top || p.Left < bLocal.Left || p.Right > bLocal.Right || p.Top < bLocal.Top || p.Bottom > bLocal.Bottom {
					t.Fatalf("preview out of client at %d DPI %d DIP page %d: %+v outside %+v", dpi, widthDIP, section, p, bLocal)
				}
				if section == displayPageRange {
					g, strip := w.graph, w.gradient
					if strip.Top <= g.Bottom || strip.Left != g.Left || strip.Right != g.Right || strip.Bottom-strip.Top != int32(displayPixelDIP(12, dpi)) || g.Bottom <= g.Top || p.Right-p.Left < bLocal.Right-bLocal.Left-int32(displayPixelDIP(30, dpi)) {
						t.Fatalf("stacked Range histogram/strip geometry: dpi=%d width=%d histogram=%+v strip=%+v preview=%+v", dpi, widthDIP, g, strip, p)
					}
				}
				if section == displayPageTicks || section == displayPageAppearance {
					width := int(p.Right - p.Left)
					if width < displayPixelDIP(220, dpi) || width > displayPixelDIP(260, dpi) {
						t.Fatalf("effect card at %d DPI %d DIP page %d should be 220–260 DIP: %+v", dpi, widthDIP, section, p)
					}
				}
				previewScreen := RECT{Left: p.Left + b.Left, Right: p.Right + b.Left, Top: p.Top + b.Top, Bottom: p.Bottom + b.Top}
				for _, id := range fields {
					h := w.controls[id]
					style, _, _ := algorithmGetWindowStyle.Call(h, ^uintptr(15))
					r := displayNativeRect(h)
					if style&WS_VISIBLE == 0 || r.Left < b.Left || r.Top < b.Top || r.Right > b.Right || r.Bottom > b.Bottom {
						t.Fatalf("field %d hidden/clipped at %d DPI %d DIP page %d: %+v / %+v", id, dpi, widthDIP, section, r, b)
					}
					if r.Left < previewScreen.Right && previewScreen.Left < r.Right && r.Top < previewScreen.Bottom && previewScreen.Top < r.Bottom {
						t.Fatalf("field %d intersects preview at %d DPI %d DIP page %d: %+v / %+v", id, dpi, widthDIP, section, r, previewScreen)
					}
					if section == displayPageRange && r.Bottom > previewScreen.Top {
						t.Fatalf("range field %d is not above histogram at %d DPI %d DIP: %+v / %+v", id, dpi, widthDIP, r, previewScreen)
					}
				}
				if r := displayNativeRect(w.controls[deApply]); r.Top < body.Bottom {
					t.Fatalf("fixed footer intersects the body: %+v / %+v", r, body)
				}
			}
		}
	}
}

func TestDisplayEditorEditingPreviewKeepsHiddenDraftButDoesNotFakeInvalidRange(t *testing.T) {
	w, e := displayNativeEditorFixture(t, true)
	e.order = 1
	displayLoadDraft(w)
	if displayChecked(w.controls[deHide]) {
		t.Fatal("new window unexpectedly enables the map legend")
	}
	m, _, _, ok := displayEditorPreviewColor(w)
	if !ok || !m.Hidden || w.gradient.Right <= w.gradient.Left {
		t.Fatalf("hidden legend should not disable the Range editing strip: %+v %+v", m, w.gradient)
	}
	setText(w.controls[deMin], "invalid")
	if _, base, _, ok := displayEditorPreviewColor(w); ok || base.Range != e.s.Color.Range {
		t.Fatal("invalid draft was silently replaced by the committed preview")
	}
	setText(w.controls[deMin], "-0.75")
	displayEditorNativeSelect(t, w, deSubTabs, displayPageAppearance)
	displayCheck(w.controls[deHide], true)
	m, _, _, ok = displayEditorPreviewColor(w)
	if !ok || m.Hidden || m.Range.Min != -0.75 || displayColorbarVisible(w.owner, w.key) {
		t.Fatal("effect preview should honor the draft without changing the owner before Apply")
	}
	displayCheck(w.controls[deHide], false)
	displayEditorNativeSelect(t, w, deSubTabs, displayPageGradient)
	if m, _, _, ok := displayEditorPreviewColor(w); !ok || !m.Hidden || w.gradient.Right <= w.gradient.Left {
		t.Fatal("toggling preview visibility disabled the Gradient editing strip")
	}
}

func TestDisplayEditorAutomaticRangeDragDefersPolicyReflowUntilFinished(t *testing.T) {
	for _, finishOnTab := range []bool{false, true} {
		name := "release"
		if finishOnTab {
			name = "tab"
		}
		t.Run(name, func(t *testing.T) {
			w, e := displayNativeEditorFixture(t, true)
			e.order = 1
			e.stats.Raw = displaycore.Distribution{Range: displaycore.Range{Min: -20, Max: 20}, Count: 5, Samples: []float64{-20, -10, 0, 10, 20}}
			e.s.Color.SetRangePolicy(displaycore.PercentileRange)
			displayLoadDraft(w)
			showPosition := func() bool {
				style, _, _ := algorithmGetWindowStyle.Call(w.controls[dePositionLow], ^uintptr(15))
				return style&WS_VISIBLE != 0
			}
			bar, graph := w.gradient, w.graph
			m, base, e, ok := displayEditorPreviewColor(w)
			if !ok || showPosition() {
				t.Fatalf("expected ready percentile range, no span inputs: %v %+v", ok, m)
			}
			bounds, _ := displayEditorColorHistogram(w, e, m, base)
			x, y := displayEditorRangePixel(bar, bounds, m.Range.Min), int((bar.Top+bar.Bottom)/2)
			if !displayEditorDragRange(w, x, y, true) || !displayEditorDragRange(w, x+35, y, false) {
				t.Fatal("drag did not establish a valid captured range")
			}
			staged := displayEditorResolveRangeState(w)
			if staged.Draft.EffectiveRangePolicy() != displaycore.ManualRange || !staged.Ready || staged.Err != nil || staged.Draft.Range.Min <= m.Range.Min || w.gradient != bar || w.graph != graph || showPosition() {
				t.Fatalf("while captured, policy failed to switch or geometry reflowed: draft=%+v strip=%+v old=%+v showPosition=%v", staged, w.gradient, bar, showPosition())
			}
			if finishOnTab {
				displayEditorNativeSelect(t, w, deSubTabs, displayPageTicks)
				if w.rangeDragging || w.section != displayPageTicks {
					t.Fatal("tab did not finish the gesture and select the requested card")
				}
				displayEditorNativeSelect(t, w, deSubTabs, displayPageRange)
			} else {
				displayEditorBodyProc(w.body, WM_LBUTTONUP, 0, 0)
			}
			final := displayEditorResolveRangeState(w)
			if !showPosition() || w.rangeDragging || !final.Ready || final.Err != nil || final.Draft.Range != staged.Draft.Range || e.s.Color.EffectiveRangePolicy() != displaycore.PercentileRange {
				t.Fatalf("gesture finish did not reflow and keep the exact uncommitted drag: %+v staged=%+v visible=%v", final, staged, showPosition())
			}
		})
	}
}

func TestDisplayEditorEffectLegendWrapsTitlesAndKeepsFullEndpointGeometry(t *testing.T) {
	for _, dpi := range []int{96, 120, 144} {
		font := func(pt float64, width, height int) displayPlotFontMetrics {
			return displayPlotFontMetrics{DPI: dpi, PT: pt, Height: displayPixelDIP(float64(height), dpi), Measure: func(s string) displayTextSize {
				return displayTextSize{Width: displayPixelDIP(float64(utf8.RuneCountInString(s)*width), dpi), Height: displayPixelDIP(float64(height), dpi)}
			}}
		}
		fonts := displayCompareTypography{ColorTitle: font(13, 9, 20), ColorTick: font(11, 8, 17)}
		card := RECT{Left: 10, Top: 12, Right: 10 + int32(displayPixelDIP(236, dpi)), Bottom: 12 + int32(displayPixelDIP(290, dpi))}
		c := displaycore.ColorMapping{Range: displaycore.Range{Min: -2.058612345e10, Max: 2.668456789e11}, Title: "用于验证完整显示的长振幅标签 Long title with useful unit", Unit: "参考测量单位", Format: displaycore.ScientificFormat, Digits: 5, Divisions: 5, TickLength: 0, TickLengthSet: true}
		inside := func(b RECT) bool {
			return b.Left >= card.Left && b.Right <= card.Right && b.Top >= card.Top && b.Bottom <= card.Bottom
		}
		for _, equal := range []bool{false, true} {
			p := displayEditorBuildEffectPlan(card, c, fonts, equal)
			if p.Notice != "" || len(p.TitleLines) < 2 || len(p.TickLabels) < 2 || !inside(p.Layout.ColorGradient) || int(p.Layout.ColorGradient.Right-p.Layout.ColorGradient.Left) != displayPixelDIP(16, dpi) {
				t.Fatalf("effect card not measured at DPI %d/equal=%v: %+v", dpi, equal, p)
			}
			var got strings.Builder
			for _, line := range p.TitleLines {
				if !inside(line.Bounds) || line.Bounds.Bottom >= p.Layout.ColorGradient.Top {
					t.Fatalf("line %q outside title band: %+v", line.Text, line.Bounds)
				}
				got.WriteString(strings.Join(strings.Fields(line.Text), ""))
			}
			if want := strings.Join(strings.Fields(displayColorTitle(c)), ""); got.String() != want {
				t.Fatalf("title clipped or unfaithful: got %q, want %q", got.String(), want)
			}
			lo, hi := p.TickLabels[0], p.TickLabels[len(p.TickLabels)-1]
			if lo.Text != displaycore.FormatNumber(c.Range.Min, c.Format, c.Digits) || hi.Text != displaycore.FormatNumber(c.Range.Max, c.Format, c.Digits) || !inside(lo.Bounds) || !inside(hi.Bounds) || hi.Bounds.Top >= lo.Bounds.Top {
				t.Fatalf("real bounds clipped or moved: low=%+v high=%+v", lo, hi)
			}
		}
		c.Hidden = true
		p := displayEditorBuildEffectPlan(card, c, fonts, true)
		if p.Notice != displayEditorLegendHidden || len(p.TitleLines) != 0 || len(p.TickLabels) != 0 || p.Layout.ColorGradient != (RECT{}) {
			t.Fatalf("draft-hidden card still contains legend geometry: %+v", p)
		}
		c.Hidden = false
		card.Right = card.Left + 3
		if p := displayEditorBuildEffectPlan(card, c, fonts, true); p.Notice != displayEditorLegendNoRoom || p.Layout.ColorGradient != (RECT{}) {
			t.Fatalf("impossibly small preview silently clipped the legend: %+v", p)
		}
	}
}

func TestDisplayEditorAxisPreviewUsesDirectionAndOneRangeReference(t *testing.T) {
	for _, dpi := range []int{96, 120, 144} {
		px := func(v float64) int32 { return int32(displayPixelDIP(v, dpi)) }
		font := func(pt float64, w, h int) displayPlotFontMetrics {
			return displayPlotFontMetrics{DPI: dpi, PT: pt, Height: int(px(float64(h))), Measure: func(s string) displayTextSize {
				return displayTextSize{Width: int(px(float64(utf8.RuneCountInString(s) * w))), Height: int(px(float64(h)))}
			}}
		}
		f := displayCompareTypography{XTick: font(10, 8, 17), YTick: font(10, 8, 17), XTitle: font(11, 9, 18), YTitle: font(11, 9, 18)}
		x := displaycore.Axis{ID: "crossline", Title: "Crossline", Data: displaycore.Range{Min: 100, Max: 2600}, View: displaycore.Range{Min: 184, Max: 2456}, Divisions: 5, Format: displaycore.IntegerFormat, TickLength: 2, TickLengthSet: true}
		w := RECT{Left: px(8), Top: px(20), Right: px(640), Bottom: px(160)}
		p := displayEditorBuildAxisPreview(w, x, f, true, true, nil)
		if p.Notice != "" || p.Rotation || p.Plot.Plot.Right-p.Plot.Plot.Left < px(580) || p.Plot.Plot.Bottom-p.Plot.Plot.Top != 1 || p.Footprint.Top+p.Footprint.Bottom < w.Top+w.Bottom-1 || p.Footprint.Top+p.Footprint.Bottom > w.Top+w.Bottom+1 {
			t.Fatalf("DPI %d: wide short preview is not centered as one axis: %+v", dpi, p)
		}
		var labels []string
		for _, tick := range p.Plot.AxisTicks[x.ID].Ticks {
			labels = append(labels, tick.Label)
		}
		if !slices.Equal(labels, []string{"184", "638", "1093", "1547", "2002", "2456"}) || p.Plot.AxisTicks[x.ID].Step != 454.4 {
			t.Fatalf("DPI %d: incorrect exact tick interval/labels: %+v", dpi, p.Plot.AxisTicks[x.ID])
		}
		if got := p.Plot.AxisTicks[x.ID]; got.Ticks[1].Value != 638 || got.Ticks[2].Value != 1093 || got.IntegerMinStep != 454 || got.IntegerMaxStep != 455 {
			t.Fatalf("DPI %d: preview kept nominal instead of real integer ticks: %+v", dpi, got)
		}
		y := displaycore.Axis{ID: "time", Title: "时间", Unit: "ms", Data: displaycore.Range{Min: 0, Max: 2000}, View: displaycore.Range{Min: 800, Max: 1500}, Down: true, Divisions: 4, Format: displaycore.IntegerFormat}
		v := displayEditorBuildAxisPreview(RECT{Right: px(225), Bottom: px(280)}, y, f, false, true, nil)
		if v.Notice != "" || !v.Rotation || !v.Y.Down || v.Plot.Plot.Right-v.Plot.Plot.Left != 1 || v.Plot.Plot.Bottom-v.Plot.Plot.Top < px(250) || v.Plot.AxisTicks[y.ID].Ticks[0].Value != 800 {
			t.Fatalf("DPI %d: Time axis lost vertical direction or extent: %+v", dpi, v)
		}
		x.Reverse = true
		r := displayEditorBuildCoordinateReference(w, x, f, true)
		if r.Notice != "" || r.Axis.X.View != x.Data || r.Axis.X.Reverse || r.Bar.Bottom >= r.Axis.Plot.Plot.Top || r.Axis.Plot.Plot.Top-r.Bar.Bottom != px(4) || r.Hint.Top-r.Axis.Footprint.Bottom != px(2) {
			t.Fatalf("DPI %d: range reference is not a single compact full-domain component: %+v", dpi, r)
		}
		for _, point := range []float64{x.Data.Min, x.View.Min, x.View.Max, x.Data.Max} {
			plot := r.Axis.Plot.Plot
			if displayEditorRangePixel(r.Bar, x.Data, point) != r.Axis.X.Pixel(point, int(plot.Left), int(plot.Right)-1) {
				t.Fatalf("DPI %d: reference and hit coordinate differ at %g", dpi, point)
			}
		}
		if p := displayEditorBuildAxisPreview(RECT{Right: px(15), Bottom: px(25)}, x, f, true, true, nil); p.Notice != displayEditorAxisNoRoom {
			t.Fatalf("DPI %d: mathematically impossible preview silently clips: %+v", dpi, p)
		}
	}
}

func TestDisplayEditorNativeAxisCardsFollowSelectedOrientation(t *testing.T) {
	w, e := displayNativeEditorFixture(t, false)
	e.s.equalTicks = true
	for _, dpi := range []int{96, 120, 144} {
		px := func(v float64) int32 { return int32(displayPixelDIP(v, dpi)) }
		pSetWindowPos.Call(w.handle, 0, 0, 0, uintptr(px(860)), uintptr(px(650)), 0x16)
		w.axis, w.section = "x", displayPageTicks
		displayLoadDraft(w)
		displayLayoutEditorAtDPI(w, dpi)
		x, body := w.preview, displayNativeRect(w.body)
		h := x.Bottom - x.Top
		last := displayNativeRect(w.controls[deDigits])
		if h < px(120) || h > px(180) || x.Right-x.Left < body.Right-body.Left-px(32) || last.Bottom > body.Top+x.Top {
			t.Fatalf("DPI %d: X parameters and wide shallow preview are not stacked: preview=%+v parameters=%+v body=%+v", dpi, x, last, body)
		}
		w.axis, w.section = "y", displayPageAppearance
		displayLoadDraft(w)
		displayLayoutEditorAtDPI(w, dpi)
		y := w.preview
		if y.Bottom-y.Top <= y.Right-y.Left || y.Left <= x.Left || y.Right-y.Left >= x.Right-x.Left {
			t.Fatalf("DPI %d: Time axis did not retain a vertical card: X=%+v Time=%+v", dpi, x, y)
		}
		w.axis, w.section = "x", displayPageRange
		displayLoadDraft(w)
		displayLayoutEditorAtDPI(w, dpi)
		if b := w.rangeBar; b.Right <= b.Left || b.Top-w.preview.Top > px(38) || b.Bottom-b.Top != px(18) {
			t.Fatalf("DPI %d: range slider is not a compact usable part of its reference card: bar=%+v card=%+v", dpi, b, w.preview)
		}
	}
}

func TestDisplayEditorRangeCursorEscapeAndNodePreciseEdit(t *testing.T) {
	w, e := displayNativeEditorFixture(t, true)
	e.stats.Raw = displaycore.Distribution{Range: displaycore.Range{Min: -5, Max: 5}, Count: 2, Samples: []float64{-5, 5}}
	displayLayoutEditor(w)
	m, base, surf, ok := displayEditorPreviewColor(w)
	if !ok {
		t.Fatal("color draft cannot be read")
	}
	bounds, _ := displayEditorColorHistogram(w, surf, m, base)
	b := w.gradient
	y := int((b.Top + b.Bottom) / 2)
	x := func(v float64) int { return displayEditorRangePixel(b, bounds, v) }
	left, center := x(m.Range.Min), x((m.Range.Min+m.Range.Max)/2)
	if displayEditorCursor(w, left, y) != IDC_SIZEWE || displayEditorCursor(w, center, y) != IDC_SIZEALL {
		t.Fatal("range mouse feedback does not match end and whole-interval hit zones")
	}
	minBefore := getText(w.controls[deMin])
	if !displayEditorDragRange(w, left, y, true) || !displayEditorDragRange(w, left+25, y, false) {
		t.Fatal("valid handle did not establish a gesture")
	}
	if getText(w.controls[deMin]) == minBefore || !translateDisplayEditorMessage(&MSG{Hwnd: w.body, Message: WM_KEYDOWN, WParam: VK_ESCAPE}) ||
		w.rangeDragging || getText(w.controls[deMin]) != minBefore || displayEditors[w.handle] != w {
		t.Fatal("Escape did not undo only the current gesture")
	}
	displayEditorNativeSelect(t, w, deSubTabs, displayPageGradient)
	w.selection = -1
	displayEditorCommand(w, deAdd, 0)
	if w.selection <= 0 || w.selection >= len(w.stops)-1 {
		t.Fatal("add without selection did not create an editable internal node")
	}
	m, base, surf, _ = displayEditorPreviewColor(w)
	bounds, _ = displayEditorColorHistogram(w, surf, m, base)
	xx := displayGradientNodePixel(w.gradient, bounds, m.Range, m.Reverse, w.stops[w.selection].Position)
	yy := int((w.nodeBand.Top + w.nodeBand.Bottom) / 2)
	if displayEditorNodeHit(w, xx, yy, bounds, m) != w.selection || displayEditorNodeHit(w, xx, int(w.gradient.Top), bounds, m) != -1 {
		t.Fatal("nodes share the gradient or range handle hit rectangles")
	}
	i := w.selection
	pos := 100 * (w.stops[i-1].Position + .75*(w.stops[i+1].Position-w.stops[i-1].Position))
	setText(w.controls[deNodePosition], displaycore.FormatExactNumber(pos))
	displayEditorCommand(w, deNodePosition, 0x0300)
	setText(w.controls[deNodeColor], "#1234FF")
	displayEditorCommand(w, deNodeColor, 0x0300)
	if w.stops[i].Position != pos/100 || w.stops[i].Color != (displaycore.RGB{R: 0x12, G: 0x34, B: 0xff}) ||
		!strings.Contains(getText(w.controls[deNodeReadout]), "#1234FF") {
		t.Fatalf("node precision/color did not reach the single gradient draft: %+v", w.stops[i])
	}
	setText(w.controls[deNodePosition], "invalid")
	displayEditorCommand(w, deNodePosition, 0x0300)
	setText(w.controls[deNodeColor], "#1234FE")
	displayEditorCommand(w, deNodeColor, 0x0300)
	displayEditorNativeSelect(t, w, deSubTabs, displayPageTicks)
	displayEditorNativeSelect(t, w, deSubTabs, displayPageGradient)
	if w.nodeEditError == "" || getText(w.controls[deNodePosition]) != "invalid" || w.stops[i].Position != pos/100 {
		t.Fatal("a tab change or color edit silently cleared the unfinished node position")
	}
}
