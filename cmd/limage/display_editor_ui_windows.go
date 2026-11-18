//go:build windows

package main

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unsafe"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
)

const (
	displayPageRange = iota
	displayPageGradient
	displayPageTicks
	displayPageAppearance
	displayPageDistance
)

func displayEditorTabLabel(id int) string {
	switch id {
	case deColor:
		return "色标"
	case deAxis:
		return "坐标"
	case deRangePage:
		return "范围"
	case deGradientPage:
		return "渐变"
	case deTickPage:
		return "刻度"
	case deAppearancePage:
		return "外观"
	case deDistancePage:
		return "距离"
	}
	return ""
}

func displayEditorSyncTab(h uintptr, previous *[]int, items []int, selected int) {
	if h == 0 {
		return
	}
	if !slices.Equal(*previous, items) {
		pSendMessageW.Call(h, TCM_FIRST+9, 0, 0) // TCM_DELETEALLITEMS
		for i, id := range items {
			tab := TCITEM{Mask: TCIF_TEXT, PszText: u16(displayEditorTabLabel(id))}
			pSendMessageW.Call(h, TCM_INSERTITEMW, uintptr(i), uintptr(unsafe.Pointer(&tab)))
		}
		*previous = append((*previous)[:0], items...)
	}
	i := slices.Index(items, selected)
	if i >= 0 {
		pSendMessageW.Call(h, TCM_FIRST+12, uintptr(i), 0) // TCM_SETCURSEL, no notification
	}
}

func displayEditorSyncTabs(w *displayEditor, e *displaySurfaceState) {
	var top []int
	if e != nil && e.s.Color != nil {
		top = append(top, deColor)
	}
	if e != nil && len(e.s.Axes) > 0 {
		top = append(top, deAxis)
	}
	active := deAxis
	sub := []int{deRangePage, deTickPage, deAppearancePage}
	if w.pageColor {
		active = deColor
		sub = []int{deRangePage, deGradientPage, deTickPage, deAppearancePage}
	} else if displayEditorHasDistance(w, e) {
		sub = append(sub, deDistancePage)
	}
	selected := deRangePage + w.section
	if w.section == displayPageDistance {
		selected = deDistancePage
	}
	if !slices.Contains(sub, selected) {
		w.section, selected = displayPageRange, deRangePage
	}
	displayEditorSyncTab(w.controls[deTopTabs], &w.topTabs, top, active)
	displayEditorSyncTab(w.controls[deSubTabs], &w.subTabs, sub, selected)
}

func displayEditorTabNotify(w *displayEditor, lp uintptr) bool {
	if lp == 0 || w.loading {
		return false
	}
	n := (*NMHDR)(unsafe.Pointer(lp))
	if n.Code != TCN_SELCHANGE {
		return false
	}
	items := w.topTabs
	switch n.HwndFrom {
	case w.controls[deSubTabs]:
		items = w.subTabs
	case w.controls[deTopTabs]:
	default:
		return false
	}
	v, _, _ := pSendMessageW.Call(n.HwndFrom, TCM_GETCURSEL, 0, 0)
	i := int(v)
	if i < 0 || i >= len(items) {
		return true
	}
	id := items[i]
	// Commit the in-window gesture draft while its original tab geometry is
	// still in force. The subsequent tab action reuses that same draft.
	displayEditorFinishGesture(w, false, true)
	if n.HwndFrom == w.controls[deTopTabs] && (id == deColor) != w.pageColor {
		current, next := 1, 0
		if w.pageColor {
			current, next = 0, 1
		}
		w.tabSections[current] = w.section
		w.section = w.tabSections[next]
	}
	displayEditorCommand(w, id, 0)
	return true
}

func displayEditorMeasureWrapped(w *displayEditor, id, width, minimum, maximum int) int {
	dc, _, _ := displayGetDC.Call(w.body)
	if dc == 0 {
		return minimum
	}
	defer displayReleaseDC.Call(w.body, dc)
	if w.font != 0 {
		if old, _, _ := pSelectObject.Call(dc, w.font); old != 0 {
			defer pSelectObject.Call(dc, old)
		}
	}
	r := RECT{Right: int32(width)}
	const wordBreakCalc = 0x10 | 0x0400 | 0x0800 // DT_WORDBREAK | DT_CALCRECT | DT_NOPREFIX
	pDrawTextW.Call(dc, uintptr(unsafe.Pointer(u16(getText(w.controls[id])))), uintptr(^uint32(0)), uintptr(unsafe.Pointer(&r)), wordBreakCalc)
	return max(minimum, min(maximum, int(r.Bottom-r.Top)+4))
}

// The exact same rectangles feed paint, hover and gesture hit testing.
func displayEditorLayoutPreview(w *displayEditor, dpi int) {
	p := w.preview
	if p.Right <= p.Left || p.Bottom <= p.Top {
		w.preview = RECT{}
		return
	}
	pad := int32(displayPixelDIP(10, dpi))
	px := func(v int) int32 { return int32(displayPixelDIP(float64(v), dpi)) }
	left, right := p.Left+pad, p.Right-pad
	if w.pageColor && (w.section == displayPageRange || w.section == displayPageGradient) {
		footer, strip, gap := px(54), px(12), px(5)
		if w.section == displayPageGradient {
			footer, gap = px(41), px(23)
		}
		top := p.Top + pad
		maxHistogram := px(200)
		if w.section == displayPageGradient {
			maxHistogram = px(108)
		}
		histogram := min(maxHistogram, p.Bottom-top-footer-strip-gap)
		if right <= left || histogram <= 0 {
			return
		}
		w.graph = RECT{Left: left, Right: right, Top: top, Bottom: top + histogram}
		w.gradient = RECT{Left: left, Right: right, Top: w.graph.Bottom + gap, Bottom: w.graph.Bottom + gap + strip}
		if w.section == displayPageGradient {
			w.nodeBand = RECT{Left: left - pad/2, Right: right + pad/2 + 1, Top: w.gradient.Bottom + px(3), Bottom: w.gradient.Bottom + px(17)}
		}
	} else if !w.pageColor && w.section == displayPageRange {
		dc, releaseDC := displayMeasurementDC(w.body)
		defer releaseDC()
		plan, closeFonts := displayEditorCoordinateReferencePlan(w, dc, dpi, p)
		defer closeFonts()
		w.rangeBar = plan.Bar
	}
}

func displayEditorPreviewColor(w *displayEditor) (displaycore.ColorMapping, displaycore.ColorMapping, *displaySurfaceState, bool) {
	e := displayEditorSurface(w)
	if e == nil || e.s.Color == nil {
		return displaycore.ColorMapping{}, displaycore.ColorMapping{}, e, false
	}
	state := displayEditorResolveRangeState(w)
	// Range geometry does not depend on tick/presentation validity, but its
	// annotation and visibility still reflect this panel's own draft fields.
	state.Draft.Title = strings.TrimSpace(getText(w.controls[deTitle]))
	state.Draft.Unit = strings.TrimSpace(getText(w.controls[deUnit]))
	state.Draft.Hidden = !displayChecked(w.controls[deHide])
	return state.Draft, state.Applied, e, state.Ready && state.Err == nil && state.Draft.Range.Valid()
}

type displayEditorLegendText struct {
	Text   string
	Bounds RECT
}

type displayEditorEffectPlan struct {
	Layout     displayPlotLayout
	TitleLines []displayEditorLegendText
	TickLabels []displayEditorLegendText
	Notice     string
}

const (
	displayEditorLegendHidden = "色标已隐藏。可在外观页勾选“主图显示色标”。"
	displayEditorLegendNoRoom = "当前预览空间不足，无法完整显示标题或刻度端点。请放大窗口或减小字号。"
)

// Break at words or between Han characters when possible. A lone long token
// can wrap at a safe Unicode boundary without splitting combining or joined
// character runs, and never uses an ellipsis.
func displayEditorWrapLegendTitle(text string, width, maxLines int, measure func(string) displayTextSize) ([]string, bool) {
	if width <= 0 || maxLines <= 0 {
		return nil, false
	}
	isContinuation := func(r, previous rune) bool {
		return unicode.Is(unicode.M, r) || r == '\u200d' || previous == '\u200d' || r >= '\U0001f3fb' && r <= '\U0001f3ff'
	}
	naturalBreak := func(previous, next rune) bool {
		if strings.ContainsRune("（([｛{《「『【", previous) || strings.ContainsRune("）)]｝}、。，：；！？》」』】", next) {
			return false
		}
		return unicode.IsSpace(previous) || unicode.IsSpace(next) || unicode.Is(unicode.Han, previous) || unicode.Is(unicode.Han, next)
	}
	var lines []string
	for _, paragraph := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
		for rest := strings.TrimSpace(paragraph); rest != ""; {
			if len(lines) == maxLines {
				return nil, false
			}
			var previous rune
			fit, preferred := 0, 0
			overflow := false
			for i, r := range rest {
				candidate := i > 0 && !isContinuation(r, previous)
				if candidate && measure(strings.TrimRightFunc(rest[:i], unicode.IsSpace)).Width > width {
					overflow = true
					break
				}
				if candidate {
					fit = i
					if naturalBreak(previous, r) {
						preferred = i
					}
				}
				previous = r
			}
			if !overflow && measure(rest).Width <= width {
				lines = append(lines, rest)
				break
			}
			if fit == 0 {
				return nil, false
			}
			if preferred != 0 {
				fit = preferred
			}
			lines = append(lines, strings.TrimSpace(rest[:fit]))
			rest = strings.TrimSpace(rest[fit:])
		}
	}
	return lines, len(lines) > 0
}

func displayEditorEffectTicks(c displaycore.ColorMapping, f displayPlotFontMetrics, height int, equal bool) displaycore.TickPlan {
	if equal {
		return displayEqualColorTicks(f, c, height)
	}
	m := displayPlotMetrics{DPI: f.DPI, Height: f.Height, Measure: f.Measure}
	p := displayColorTickPlan(m, c, height)
	// Legacy colorbars annotate their two exact mapping bounds separately.
	// For the measured effect preview reserve both just like equal endpoints.
	ends := displayColorEndpoints(c)
	if len(ends) != 2 {
		return p
	}
	ticks := []displaycore.Tick{ends[0]}
	for _, tick := range p.Ticks {
		if tick.Value > c.Range.Min && tick.Value < c.Range.Max {
			ticks = append(ticks, tick)
		}
	}
	p.Ticks = append(ticks, ends[1])
	return p
}

// Compute the full rendered geometry before touching the HDC. An impossible
// size yields an explicit notice; neither endpoint is dropped or abbreviated.
func displayEditorBuildEffectPlan(card RECT, c displaycore.ColorMapping, fonts displayCompareTypography, equal bool) displayEditorEffectPlan {
	if c.Hidden {
		return displayEditorEffectPlan{Notice: displayEditorLegendHidden}
	}
	if !c.Range.Valid() {
		return displayEditorEffectPlan{Notice: "色标范围有效后才能显示预览。"}
	}
	width := int(card.Right - card.Left)
	dpi := fonts.ColorTick.DPI
	gap, barWidth := displayPixelDIP(5, dpi), displayPixelDIP(16, dpi)
	maxTitleLines := (int(card.Bottom-card.Top) - 2*fonts.ColorTick.Height - 2*gap - 1) / max(1, fonts.ColorTitle.Height)
	lines, fits := displayEditorWrapLegendTitle(displayColorTitle(c), width, maxTitleLines, fonts.ColorTitle.Measure)
	if !fits {
		return displayEditorEffectPlan{Notice: displayEditorLegendNoRoom}
	}
	titleHeight := len(lines) * fonts.ColorTitle.Height
	top := int(card.Top) + titleHeight + gap + fonts.ColorTick.Height/2
	bottom := int(card.Bottom) - (fonts.ColorTick.Height+1)/2
	if bottom-top <= fonts.ColorTick.Height+gap {
		return displayEditorEffectPlan{Notice: displayEditorLegendNoRoom}
	}
	p := displayEditorEffectTicks(c, fonts.ColorTick, bottom-top, equal)
	if len(p.Ticks) < 2 {
		return displayEditorEffectPlan{Notice: "色标刻度有效后才能显示预览。"}
	}
	length := displayPixelDIP(c.MajorTickDIP(), dpi)
	groupWidth := barWidth + length + gap + displayCompareLabelWidth(p, fonts.ColorTick)
	for _, line := range lines {
		groupWidth = max(groupWidth, fonts.ColorTitle.Measure(line).Width)
	}
	if groupWidth > width {
		return displayEditorEffectPlan{Notice: displayEditorLegendNoRoom}
	}
	left := int(card.Left) + (width-groupWidth)/2
	bar := RECT{Left: int32(left), Top: int32(top), Right: int32(left + barWidth), Bottom: int32(bottom)}
	out := displayEditorEffectPlan{Layout: displayPlotLayout{
		Outer: card, Plot: card, ColorRegion: RECT{Left: int32(left), Top: card.Top, Right: card.Right, Bottom: card.Bottom}, ColorGradient: bar,
		ColorTicks: p, Typography: fonts,
	}}
	for i, line := range lines {
		w := fonts.ColorTitle.Measure(line).Width
		x, y := left+(groupWidth-w)/2, int(card.Top)+i*fonts.ColorTitle.Height
		out.TitleLines = append(out.TitleLines, displayEditorLegendText{line, RECT{Left: int32(x), Top: int32(y), Right: int32(x + w), Bottom: int32(y + fonts.ColorTitle.Height)}})
	}
	yPixel := func(v float64) int { return bottom - 1 - int(math.Round(float64(bottom-1-top)*c.Range.Fraction(v))) }
	visible := displayEqualTickLabelVisibility(p.Ticks, yPixel, func(string) int { return fonts.ColorTick.Height }, gap)
	for i, tick := range p.Ticks {
		if visible[i] {
			x, y := int(bar.Right)+length+gap, yPixel(tick.Value)-fonts.ColorTick.Height/2
			out.TickLabels = append(out.TickLabels, displayEditorLegendText{tick.Label, RECT{Left: int32(x), Top: int32(y), Right: int32(x + fonts.ColorTick.Measure(tick.Label).Width), Bottom: int32(y + fonts.ColorTick.Height)}})
		}
	}
	// Form diagnostics already explain overly dense or equal labels. Avoid
	// overlaying that warning over the constrained preview's title or ticks.
	out.Layout.ColorTicks.Warning = ""
	return out
}

func displayEditorDrawPreviewNotice(dc uintptr, card RECT, notice string) {
	const wordWrap = 0x10 | 0x0800 // DT_WORDBREAK | DT_NOPREFIX
	pDrawTextW.Call(dc, uintptr(unsafe.Pointer(u16(notice))), uintptr(^uint32(0)), uintptr(unsafe.Pointer(&card)), uintptr(DT_CENTER|wordWrap))
}

// Use the actual plot's layout and label renderers for the thumbnail, rather
// than a second interpretation of percentiles, units or tick spacing.
func displayEditorPaintStyle(w *displayEditor, dc uintptr, area RECT) {
	if area.Right <= area.Left || area.Bottom <= area.Top {
		return
	}
	saved, _, _ := pSaveDC.Call(dc)
	if saved == 0 {
		return
	}
	defer pRestoreDC.Call(dc, saved)
	pIntersectClipRect.Call(dc, uintptr(area.Left), uintptr(area.Top), uintptr(area.Right), uintptr(area.Bottom))
	dpi := displayDPI(w.handle)
	g := int32(displayPixelDIP(9, dpi))
	card := RECT{Left: area.Left + g, Top: area.Top + g, Right: area.Right - g, Bottom: area.Bottom - g}
	if card.Right <= card.Left || card.Bottom <= card.Top {
		return
	}
	// The checkbox is a draft field and retains authority even when another
	// field is temporarily invalid. Range/Gradient do not enter this branch.
	if w.pageColor && !displayChecked(w.controls[deHide]) {
		displayEditorDrawPreviewNotice(dc, card, displayEditorLegendHidden)
		return
	}
	if e := displayEditorSurface(w); e != nil && w.section == displayPageAppearance && displayEditorUsesEqualTicks(w) {
		committed, _ := displayCommitted(w.owner, w.key)
		p := displayEditorPreviewPlotFont(w, displayPlotFontPT(committed.PlotTitleFontPT, true))
		f := displayNewPlotFonts(p, dpi, 0)
		t := displayPlotFontMeasure(dc, dpi, p, f)
		paint, done := displayMeasuredTextPainter(dc, t, false)
		label := e.s.Title
		x := int(card.Left) + max(0, (int(card.Right-card.Left)-t.Measure(label).Width)/2)
		paint(label, x, int(card.Top))
		done()
		f.close()
		card.Top += int32(t.Height) + g
	}
	dummy := displayEditorAxisDummy()
	in := displayPlotInput{X: dummy, Y: dummy}
	if w.pageColor {
		state := displayEditorResolveRangeState(w)
		if state.Err != nil {
			displayEditorDrawPreviewNotice(dc, card, "色标范围有效后才能显示预览："+state.Err.Error())
			return
		}
		if !state.Ready {
			displayEditorDrawPreviewNotice(dc, card, "正在计算当前区域的范围…")
			return
		}
		c, err := displayEditorPreviewMapping(w, state.Draft)
		if err != nil {
			displayEditorDrawPreviewNotice(dc, card, "校验色标样式后可预览："+err.Error())
			return
		}
		in.Color = &c
		fonts, closeFonts := displayCompareTypographyFor(dc, dpi, in, 0)
		defer closeFonts()
		plan := displayEditorBuildEffectPlan(card, c, fonts, displayEditorUsesEqualTicks(w))
		if plan.Notice != "" {
			displayEditorDrawPreviewNotice(dc, card, plan.Notice)
			return
		}
		displayDrawMeasuredCompareColorbar(dc, plan.Layout, c)
		paint, done := displayMeasuredTextPainter(dc, fonts.ColorTitle, false)
		defer done()
		for _, line := range plan.TitleLines {
			paint(line.Text, int(line.Bounds.Left), int(line.Bounds.Top))
		}
		return
	}
	a, err := displayEditorPreviewAxis(w)
	if err != nil {
		drawAxisText(dc, "范围或刻度校验后可预览坐标。", int(card.Left), int(card.Top), int(card.Right), int(card.Bottom), DT_CENTER)
		return
	}
	e := displayEditorSurface(w)
	if e == nil {
		return
	}
	if !displayEditorPoststackLinked(w) {
		a.HideAxis = displayChecked(w.controls[deHide])
		a.HideLabels = displayChecked(w.controls[deHideLabels])
		a.HideTitle = displayChecked(w.controls[deHideTitle])
	}
	if a.HideAxis {
		drawAxisText(dc, "当前草稿隐藏此坐标轴", int(card.Left), int(card.Top), int(card.Right), int(card.Bottom), DT_CENTER)
		return
	}
	horizontal := displayEditorAxisIsHorizontal(w, a.ID)
	in = displayEditorAxisPreviewInput(a, horizontal)
	if horizontal && a.BaseUnit != "m" {
		in.Position = seismicSurfacePosition(w.owner, w.key, a.View)
	}
	f, closeFonts := displayCompareTypographyFor(dc, dpi, in, 0)
	defer closeFonts()
	var locator []func(int) displaycore.TickPlan
	if displayScientificTickEditor(w) {
		amplitude := a.ID == "amplitude" && (w.key == traceScientificWaveKey || a.Unit != "dB")
		em := float64(displayPixelDIP(traceScientificFontPT(a, false)*96/72, dpi))
		locator = append(locator, func(pixels int) displaycore.TickPlan {
			return traceScientificAxisTicks(a, float64(pixels), em, horizontal, amplitude)
		})
	} else if displayEditorPoststackLinked(w) {
		if p, ok := displayCompareEditorReferenceTicks(w.owner, w.key, a); ok {
			locator = append(locator, func(int) displaycore.TickPlan { return p })
		}
	}
	plan := displayEditorBuildAxisPreview(card, a, f, horizontal, displayEditorUsesEqualTicks(w), in.Position, locator...)
	if plan.Notice != "" {
		displayEditorDrawPreviewNotice(dc, card, plan.Notice)
		return
	}
	displayDrawMeasuredCompareAxes(dc, plan.Plot, plan.X, plan.Y, in.Position)
}

func displayEditorGradientNodeVisible(w *displayEditor, i int) bool {
	n := len(w.stops)
	return n <= 32 || i == 0 || i == n-1 || i == w.selection || i%max(1, n/24) == 0 ||
		slices.Contains(w.gradientEdit.authored, w.gradientEdit.ids[i])
}

// The editor strip is its own 0–100% scale. Variance in the histogram range
// cannot compress or move a control point on this strip.
func displayGradientNodePixel(bar RECT, _ displaycore.Range, _ displaycore.Range, reverse bool, at float64) int {
	return displayEditorRangePixel(bar, displaycore.Range{Min: 0, Max: 1}, displayGradientVisual(at, reverse))
}

func displayEditorRangePixel(bar RECT, shown displaycore.Range, v float64) int {
	return int(bar.Left) + int(math.Round(shown.Fraction(v)*float64(bar.Right-bar.Left)))
}

func displayEditorPaintGradientNodes(w *displayEditor, dc uintptr, shown displaycore.Range, m displaycore.ColorMapping) {
	b := w.nodeBand
	if b.Bottom <= b.Top || b.Right <= b.Left {
		return
	}
	displayEditorBindGradient(w)
	dpi := displayDPI(w.handle)
	d := int32(displayPixelDIP(6, dpi))
	y := b.Top + (b.Bottom-b.Top)/2 - 1
	p := int32(displayGradientNodePixel(w.gradient, shown, m.Range, false, w.gradientEdit.pending))
	dot := displaycore.RGB{R: 79, G: 101, B: 128}
	for row := w.gradient.Top; row < b.Bottom; row += int32(max(2, displayPixelDIP(3, dpi))) {
		displayFill(dc, RECT{Left: p, Top: row, Right: p + 1, Bottom: row + 1}, dot)
	}
	drawNode := func(i int) {
		node := w.stops[i]
		x := int32(displayGradientNodePixel(w.gradient, shown, m.Range, m.Reverse, node.Position))
		outline := rgbRef(68, 85, 106)
		bold := max(1, int(d)/4)
		if i == w.selection {
			outline = rgbRef(25, 106, 200)
			bold = max(2, displayPixelDIP(2, dpi))
		} else if w.gradientEdit.ids[i] == w.gradientHovered {
			outline = rgbRef(27, 141, 181)
			bold = max(2, displayPixelDIP(2, dpi))
		}
		pen, _, _ := pCreatePen.Call(PS_SOLID, uintptr(bold), outline)
		brush, _, _ := pCreateSolidBrush.Call(rgbRef(node.Color.R, node.Color.G, node.Color.B))
		if pen != 0 && brush != 0 {
			op, _, _ := pSelectObject.Call(dc, pen)
			ob, _, _ := pSelectObject.Call(dc, brush)
			points := [...]POINT{{X: x, Y: y - d}, {X: x + d, Y: y}, {X: x, Y: y + d}, {X: x - d, Y: y}}
			pPolygon.Call(dc, uintptr(unsafe.Pointer(&points[0])), uintptr(len(points)))
			pSelectObject.Call(dc, ob)
			pSelectObject.Call(dc, op)
		}
		if brush != 0 {
			pDeleteObject.Call(brush)
		}
		if pen != 0 {
			pDeleteObject.Call(pen)
		}
	}
	for i := range w.stops {
		if i != w.selection && displayEditorGradientNodeVisible(w, i) {
			drawNode(i)
		}
	}
	if w.selection >= 0 && w.selection < len(w.stops) {
		drawNode(w.selection)
	}
}

type displayEditorRangeTarget struct {
	mode         displaycore.RangeDragMode
	bar          RECT
	bounds, view displaycore.Range
	symmetric    bool
}

func displayEditorRangeHit(w *displayEditor, x, y int) (displayEditorRangeTarget, bool) {
	var hit displayEditorRangeTarget
	if w.section != displayPageRange || w.invalid || !w.pageColor && (w.unit.StyleOnly || displayEditorPoststackLinked(w)) {
		return hit, false
	}
	dpi := displayDPI(w.handle)
	tolerance := max(6, displayPixelDIP(8, dpi)) // full hit width ≥16 DIP
	b := w.rangeBar
	if w.pageColor {
		b = w.gradient
		if displayEditorColorRangeLocked(w) {
			return hit, false
		}
	}
	if b.Right <= b.Left || b.Bottom <= b.Top || x < int(b.Left)-tolerance || x > int(b.Right)+tolerance {
		return hit, false
	}
	top, bottom := int(b.Top)-tolerance/2, int(b.Bottom)+tolerance
	if w.pageColor {
		top = max(int(w.graph.Top)+displayPixelDIP(19, dpi), int(w.graph.Top))
	}
	if y < top || y > bottom {
		return hit, false
	}
	hit.bar = b
	if w.pageColor {
		m, base, e, ok := displayEditorPreviewColor(w)
		if !ok {
			return hit, false
		}
		hit.bounds, _ = displayEditorColorHistogram(w, e, m, base)
		hit.view, hit.symmetric = m.Range, m.Symmetric
	} else {
		var ok bool
		hit.bounds, hit.view, ok = displayEditorRange(w)
		if !ok {
			return hit, false
		}
	}
	if !hit.bounds.Valid() || !hit.view.Valid() && (w.pageColor || !w.unit.ValidRange(hit.view)) {
		return hit, false
	}
	lx, rx := displayEditorRangePixel(b, hit.bounds, hit.view.Min), displayEditorRangePixel(b, hit.bounds, hit.view.Max)
	ld, rd := absInt(x-lx), absInt(x-rx)
	switch {
	case ld <= tolerance && ld <= rd:
		hit.mode = displaycore.DragMinimum
	case rd <= tolerance:
		hit.mode = displaycore.DragMaximum
	case lx < x && x < rx && !hit.symmetric && (!w.pageColor || y >= int(b.Top)):
		hit.mode = displaycore.DragWhole
	default:
		return hit, false
	}
	return hit, true
}

func displayEditorNodeHit(w *displayEditor, x, y int, shown displaycore.Range, m displaycore.ColorMapping) int {
	if !w.pageColor || w.section != displayPageGradient || !displayWithin(w.nodeBand, x, y) {
		return -1
	}
	displayEditorBindGradient(w)
	best := -1
	distance := displayPixelDIP(8, displayDPI(w.handle)) + 1
	for i, node := range w.stops {
		if displayEditorGradientNodeVisible(w, i) {
			q := displayGradientNodePixel(w.gradient, shown, m.Range, m.Reverse, node.Position)
			if d := absInt(x - q); d < distance || d == distance && i == w.selection {
				best, distance = i, d
			}
		}
	}
	return best
}

func displayEditorCursor(w *displayEditor, x, y int) uintptr {
	if w.rangeDragging {
		if w.rangeDragMode == displaycore.DragWhole {
			return IDC_SIZEALL
		}
		return IDC_SIZEWE
	}
	if w.dragging {
		return IDC_SIZEWE
	}
	if h, ok := displayEditorRangeHit(w, x, y); ok {
		if h.mode == displaycore.DragWhole {
			return IDC_SIZEALL
		}
		return IDC_SIZEWE
	}
	if w.pageColor && w.section == displayPageGradient && !displayEditorColorRangeLocked(w) && displayWithin(w.nodeBand, x, y) {
		if m, _, _, ok := displayEditorPreviewColor(w); ok {
			if i := displayEditorNodeHit(w, x, y, displaycore.Range{}, m); i > 0 && i < len(w.stops)-1 {
				return IDC_SIZEWE
			}
		}
	}
	return IDC_ARROW
}

var displayEditorGetCapture = user32.NewProc("GetCapture")

func displayEditorFinishGesture(w *displayEditor, cancel, release bool) bool {
	if !w.dragging && !w.rangeDragging {
		return false
	}
	d, node := w.gestureDraft, w.gestureNode
	policyChanged := w.rangeDragging && w.rangeDragColor && d != nil && d.selects[deRangePolicy] != displaySelection(w.controls[deRangePolicy])
	w.dragging, w.rangeDragging, w.gestureDraft = false, false, nil
	if cancel && d != nil {
		loading := w.loading
		w.loading = true
		displayRestoreDraftControls(w, *d)
		if w.pageColor {
			displayEditorSelectGradientNode(w, node)
		}
		w.nodeEditError = ""
		w.loading = loading
	}
	if release {
		if owner, _, _ := displayEditorGetCapture.Call(); owner == w.body {
			displayReleaseCapture.Call()
		}
	}
	if policyChanged || cancel && d != nil {
		// Changing policy makes its controls reflow, which would move the
		// histogram and handles under the pointer during the gesture.
		displayLayoutEditor(w)
	}
	if cancel && d != nil {
		displayEditorRefreshNode(w, true)
	}
	displayEditorRefreshTicks(w)
	displayEditorRefreshRangeStatus(w)
	displayEditorRefreshFooter(w)
	displayInvalidatePreview(w)
	return true
}

func displayEditorRefreshNode(w *displayEditor, force bool) {
	if !w.pageColor || w.section != displayPageGradient {
		return
	}
	displayEditorBindGradient(w)
	i := w.selection
	key := displayDraftKey(w)
	if w.nodeUIKey != key || w.nodeUISelection != i {
		w.nodeUIKey, w.nodeUISelection = key, i
		force = true
	}
	selected := i >= 0 && i < len(w.stops)
	editable := !displayEditorColorRangeLocked(w)
	inside := selected && i > 0 && i < len(w.stops)-1
	displayEnable(w.controls[deAdd], editable && len(w.stops) >= 2 && len(w.stops) < 512)
	displayEnable(w.controls[deDelete], editable && inside)
	displayEnable(w.controls[deChoose], editable && selected)
	displayEnable(w.controls[deNodeSwatch], editable && selected)
	displayEnable(w.controls[deNodePosition], editable && inside)
	displayEnable(w.controls[deNodeColor], editable && selected)
	pInvalidateRect.Call(w.controls[deNodeSwatch], 0, 0)
	previous := w.loading
	w.loading = true
	defer func() { w.loading = previous }()
	if force {
		w.nodeEditError, w.nodePositionError, w.nodeColorError = "", "", ""
	}
	pending := displayEditorGradientPendingText(w)
	if !editable {
		pending = "色表与映射范围跟随 A；解除跟随后才可编辑控制点。"
	}
	if getText(w.controls[deNodePending]) != pending {
		displayField(w, deNodePending, pending)
	}
	if !selected {
		displayField(w, deNodePosition, "")
		displayField(w, deNodeColor, "")
		displayField(w, deNodeReadout, "可点击选择色带下方的已有控制点。")
		return
	}
	p := w.stops[i]
	f := displayGradientVisual(p.Position, displayChecked(w.controls[deReverse]))
	if force {
		displayField(w, deNodePosition, displayGradientPercent(f))
		displayField(w, deNodeColor, fmt.Sprintf("#%02X%02X%02X", p.Color.R, p.Color.G, p.Color.B))
	}
	text := fmt.Sprintf("颜色 #%02X%02X%02X", p.Color.R, p.Color.G, p.Color.B)
	if m, _, _, ok := displayEditorPreviewColor(w); ok {
		text = fmt.Sprintf("%s%% → %s  ·  %s", displayGradientPercent(f), displaycore.FormatCompactNumber(m.Range.At(f)), text)
	}
	if !inside {
		text += "  ·  端点位置固定"
	}
	if w.nodeEditError != "" {
		text = w.nodeEditError
	}
	if getText(w.controls[deNodeReadout]) != text {
		displayField(w, deNodeReadout, text)
	}
}

func displayEditorGradientCommand(w *displayEditor, id, code int) bool {
	if id == deReverse && !w.loading && w.pageColor && w.section == displayPageGradient && !displayEditorColorRangeLocked(w) {
		// Stored stops keep their order/IDs; refresh the displayed percent
		// and readout using the one Reverse flag.
		displayEditorRefreshNode(w, true)
		displayInvalidatePreview(w)
		return false
	}
	switch id {
	case deNodePosition, deNodeColor, deNodeSwatch, deAdd, deDelete, deChoose:
	default:
		return false
	}
	if w.loading || !w.pageColor || w.section != displayPageGradient || displayEditorColorRangeLocked(w) {
		return true
	}
	displayEditorBindGradient(w)
	i := w.selection
	selected := i >= 0 && i < len(w.stops)
	internal := selected && i > 0 && i < len(w.stops)-1
	force := false
	switch id {
	case deNodePosition:
		if code != 0x0300 || !internal { // EN_CHANGE
			return true
		}
		v, err := displayParseNumber(getText(w.controls[id]), false)
		p := v / 100
		if err != nil || math.Abs(displayGradientQuantize(p)-p) > 1e-12 || !displayEditorMoveGradientNode(w, p, false) {
			w.nodePositionError = "位置需位于相邻节点之间，最多 4 位小数。"
		} else {
			w.nodePositionError = ""
		}
	case deNodeColor:
		if code != 0x0300 || !selected {
			return true
		}
		raw := strings.TrimSpace(getText(w.controls[id]))
		v, err := strconv.ParseUint(strings.TrimPrefix(raw, "#"), 16, 24)
		if len(raw) != 7 || !strings.HasPrefix(raw, "#") || err != nil {
			w.nodeColorError = "颜色请输入 #RRGGBB。"
		} else {
			w.stops[i].Color = displaycore.RGB{R: byte(v >> 16), G: byte(v >> 8), B: byte(v)}
			displayEditorTrackGradient(w)
			w.nodeColorError = ""
		}
	case deAdd, deDelete, deChoose, deNodeSwatch:
		if code != 0 { // BN_CLICKED, not button focus/notification noise
			return true
		}
		switch id {
		case deAdd:
			force = displayEditorAddGradientNode(w)
		case deDelete:
			force = displayEditorDeleteGradientNode(w, i)
		default:
			force = displayEditorChooseGradientColor(w)
		}
	}
	w.nodeEditError = w.nodePositionError
	if w.nodeEditError == "" {
		w.nodeEditError = w.nodeColorError
	}
	displayEditorRefreshNode(w, force)
	displayEditorRefreshFooter(w)
	displayInvalidatePreview(w)
	return true
}
