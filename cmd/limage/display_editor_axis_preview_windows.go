//go:build windows

package main

import displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"

type displayEditorAxisPreviewPlan struct {
	Plot      displayPlotLayout
	X, Y      displaycore.Axis
	Footprint RECT
	Rotation  bool
	Notice    string
}

type displayEditorCoordinateReference struct {
	Bar    RECT
	Axis   displayEditorAxisPreviewPlan
	Header RECT
	Hint   RECT
	Notice string
}

const displayEditorAxisNoRoom = "当前预览空间不足，无法完整显示坐标端点或标题。可放大窗口；这不影响主图设置。"

func displayEditorAxisIsHorizontal(w *displayEditor, axis string) bool {
	e := displayEditorSurface(w)
	return e != nil && len(e.s.Axes) > 0 && e.s.Axes[0].ID == axis
}

func displayEditorAxisDummy() displaycore.Axis {
	return displaycore.Axis{ID: "preview-placeholder", View: displaycore.Range{Min: 0, Max: 1}, HideAxis: true, HideLabels: true, HideTitle: true}
}

func displayEditorAxisPreviewTicks(a displaycore.Axis, font displayPlotFontMetrics, pixels int, horizontal, equal bool, position func(float64) float64) displaycore.TickPlan {
	if equal {
		return displayEqualAxisTicks(font, a, pixels, horizontal, position)
	}
	return displayAxisTickPlan(displayPlotMetrics{DPI: font.DPI, Height: font.Height, Measure: font.Measure}, a, pixels, horizontal, position)
}

func displayEditorAxisPreviewInput(a displaycore.Axis, horizontal bool) displayPlotInput {
	dummy := displayEditorAxisDummy()
	if horizontal {
		return displayPlotInput{X: a, Y: dummy}
	}
	return displayPlotInput{X: dummy, Y: a}
}

// The shared painter consumes a synthetic one-axis plot. A horizontal axis
// is not the bottom of a tall blank seismic panel; its baseline and labels
// are centered as one short group. A vertical group includes the actual
// rotated title, tick-length and label widths in its horizontal allocation.
func displayEditorBuildAxisPreview(card RECT, a displaycore.Axis, f displayCompareTypography, horizontal, equal bool, position func(float64) float64, custom ...func(int) displaycore.TickPlan) displayEditorAxisPreviewPlan {
	if a.HideAxis {
		return displayEditorAxisPreviewPlan{Notice: "当前草稿隐藏此坐标轴。"}
	}
	if !a.ValidRange(a.View) {
		return displayEditorAxisPreviewPlan{Notice: "坐标范围有效后才能显示预览。"}
	}
	if !equal && !a.TickLengthSet {
		// The shared painter otherwise supplies the equal-tick default (6 DIP).
		a.TickLength, a.TickLengthSet = a.MajorTickDIP(), true
	}
	in := displayEditorAxisPreviewInput(a, horizontal)
	out := displayEditorAxisPreviewPlan{X: in.X, Y: in.Y, Rotation: !horizontal && !a.HideTitle}
	width, height := int(card.Right-card.Left), int(card.Bottom-card.Top)
	font := f.YTick
	if horizontal {
		font = f.XTick
	}
	dpi := font.DPI
	gap := max(2, displayPixelDIP(5, dpi))
	length := displayPixelDIP(displayEqualAxisTickDIP(a), dpi)
	locate := func(pixels int) displaycore.TickPlan {
		if len(custom) != 0 {
			return custom[0](pixels)
		}
		return displayEditorAxisPreviewTicks(a, font, pixels, horizontal, equal, position)
	}
	var ticks displaycore.TickPlan
	if horizontal {
		// Label text may straddle the endpoints. Measure the actual candidate
		// labels and retain sufficient space on both sides, rather than relying
		// on a fixed count/average character width.
		ticks = locate(width)
		pad := 0
		if !a.HideLabels {
			pad = (displayCompareLabelWidth(ticks, f.XTick) + 1) / 2
		}
		if width-2*pad < 2 {
			return displayEditorAxisPreviewPlan{Notice: displayEditorAxisNoRoom}
		}
		x0, x1 := int(card.Left)+pad, int(card.Right)-pad
		ticks = locate(x1 - x0)
		rows := displayCompareHorizontalRows(displayPlotInput{X: out.X, Y: out.Y, Typography: f}, ticks, displaycore.TickPlan{}, displaycore.TickPlan{}, x1-x0)
		h := rows.bottom
		if !a.HideTitle {
			if f.XTitle.Measure(displayAxisTitle(a)).Width > width {
				return displayEditorAxisPreviewPlan{Notice: displayEditorAxisNoRoom}
			}
		}
		if h > height {
			return displayEditorAxisPreviewPlan{Notice: displayEditorAxisNoRoom}
		}
		base := int(card.Top) + (height-h)/2
		out.Plot.HorizontalAxis = rows
		out.Plot.Plot = RECT{Left: int32(x0), Right: int32(x1), Top: int32(base), Bottom: int32(base + 1)}
		out.Footprint = RECT{Left: card.Left, Right: card.Right, Top: int32(base), Bottom: int32(base + h)}
	} else {
		pad := 0
		if !a.HideLabels {
			pad = (f.YTick.Height + 1) / 2
		}
		y0, y1 := int(card.Top)+pad, int(card.Bottom)-pad
		if y1-y0 < 2 || !a.HideTitle && f.YTitle.Measure(displayAxisTitle(a)).Width > y1-y0 {
			return displayEditorAxisPreviewPlan{Notice: displayEditorAxisNoRoom}
		}
		ticks = locate(y1 - y0)
		group := 1 + length
		if !a.HideLabels {
			group += gap + displayCompareLabelWidth(ticks, f.YTick)
		}
		if !a.HideTitle {
			group += gap + f.YTitle.Height
		}
		if group > width {
			return displayEditorAxisPreviewPlan{Notice: displayEditorAxisNoRoom}
		}
		x0 := int(card.Left) + (width-group)/2
		base := x0 + group - 1
		out.Plot.Plot = RECT{Left: int32(base), Right: int32(base + 1), Top: int32(y0), Bottom: int32(y1)}
		out.Footprint = RECT{Left: int32(x0), Right: int32(base + 1), Top: card.Top, Bottom: card.Bottom}
	}
	// A local thumbnail collision isn't a main-plot warning or an Apply gate.
	// The renderer keeps end glyphs and every mark, resolving only overlaps.
	ticks.Warning = ""
	out.Plot.Typography = f
	out.Plot.AxisTicks = map[string]displaycore.TickPlan{a.ID: ticks}
	return out
}

// The slider is always a horizontal view of the full numeric data domain.
// It intentionally does not honor visual direction reversal or adopt the
// current zoom endpoints as the reference domain. All hit/paint positions
// correspond to the compact reference axis immediately below the slider.
func displayEditorBuildCoordinateReference(card RECT, a displaycore.Axis, f displayCompareTypography, equal bool) displayEditorCoordinateReference {
	if a.AllowsPoint && a.Data.IsPoint() {
		return displayEditorCoordinateReference{Notice: "当前剖面 1 道 · 真实坐标 " + displaycore.FormatExactNumber(a.DisplayValue(a.Data.Min)) + " " + a.Unit}
	}
	if !a.Data.Valid() {
		return displayEditorCoordinateReference{Notice: "当前坐标没有可用的完整数据范围。"}
	}
	a.View = a.Data
	a.Reverse, a.HideAxis, a.HideLabels, a.HideTitle = false, false, false, true
	dpi := f.XTick.DPI
	px := func(v float64) int32 { return int32(displayPixelDIP(v, dpi)) }
	headerHeight := max(px(18), int32(f.XTick.Height))
	header := RECT{Left: card.Left, Right: card.Right, Top: card.Top, Bottom: card.Top + headerHeight}
	barTop := header.Bottom + px(4)
	barBottom := barTop + px(18)
	axisTop := barBottom + px(4)
	hintHeight := max(px(22), int32(f.XTick.Height))
	axisCard := RECT{Left: card.Left, Right: card.Right, Top: axisTop, Bottom: card.Bottom - hintHeight}
	ax := displayEditorBuildAxisPreview(axisCard, a, f, true, equal, nil)
	if ax.Notice != "" {
		return displayEditorCoordinateReference{Notice: ax.Notice}
	}
	// Keep the baseline next to the slider even if the outer card is tall.
	dy := axisTop - ax.Plot.Plot.Top
	ax.Plot.Plot.Top += dy
	ax.Plot.Plot.Bottom += dy
	ax.Footprint.Top += dy
	ax.Footprint.Bottom += dy
	return displayEditorCoordinateReference{
		Bar: RECT{Left: ax.Plot.Plot.Left, Right: ax.Plot.Plot.Right - 1, Top: barTop, Bottom: barBottom}, Axis: ax, Header: header,
		Hint: RECT{Left: card.Left, Top: ax.Footprint.Bottom + px(2), Right: card.Right, Bottom: min(card.Bottom, ax.Footprint.Bottom+px(2)+hintHeight)},
	}
}

func displayEditorCoordinateReferenceCard(r RECT, dpi int) RECT {
	p := int32(displayPixelDIP(9, dpi))
	return RECT{Left: r.Left + p, Top: r.Top + p, Right: r.Right - p, Bottom: r.Bottom - p}
}

func displayEditorCoordinateReferencePlan(w *displayEditor, dc uintptr, dpi int, r RECT) (displayEditorCoordinateReference, func()) {
	a := w.unit
	if draft, err := displayEditorPreviewAxis(w); err == nil {
		a = draft
	}
	in := displayEditorAxisPreviewInput(a, true)
	f, closeFonts := displayCompareTypographyFor(dc, dpi, in, 0)
	return displayEditorBuildCoordinateReference(displayEditorCoordinateReferenceCard(r, dpi), a, f, displayEditorUsesEqualTicks(w)), closeFonts
}

func displayEditorPaintCoordinateRange(w *displayEditor, dc uintptr, area RECT) {
	dpi := displayDPI(w.handle)
	plan, done := displayEditorCoordinateReferencePlan(w, dc, dpi, area)
	defer done()
	if plan.Notice != "" {
		displayEditorDrawPreviewNotice(dc, displayEditorCoordinateReferenceCard(area, dpi), plan.Notice)
		return
	}
	label := func(text string, r RECT) {
		drawAxisText(dc, text, int(r.Left), int(r.Top), int(r.Right), int(r.Bottom), DT_LEFT)
	}
	label("完整数据范围标尺", plan.Header)
	bar := w.rangeBar
	if bar.Right <= bar.Left || bar.Bottom <= bar.Top {
		bar = plan.Bar
	}
	// Use the exact same x domain/extent as the previously laid out hit rect.
	plan.Axis.Plot.Plot.Left, plan.Axis.Plot.Plot.Right = bar.Left, bar.Right+1
	displayDrawMeasuredCompareAxes(dc, plan.Axis.Plot, plan.Axis.X, plan.Axis.Y, nil)
	px := func(v float64) int32 { return int32(displayPixelDIP(v, dpi)) }
	mid := (bar.Top + bar.Bottom) / 2
	displayFill(dc, RECT{Left: bar.Left, Right: bar.Right + 1, Top: mid - px(2), Bottom: mid + px(2)}, displaycore.RGB{R: 206, G: 214, B: 224})
	bounds, r, ok := displayEditorRange(w)
	if displayEditorPoststackLinked(w) {
		label("坐标样式和刻度跟随 A；请在 A 调整范围。", plan.Hint)
		return
	}
	if w.unit.StyleOnly {
		label("当前坐标范围只读。", plan.Hint)
		return
	}
	if !ok {
		label("请输入数据边界内的有效上下限。", plan.Hint)
		return
	}
	a, b := displayEditorRangePixel(bar, bounds, r.Min), displayEditorRangePixel(bar, bounds, r.Max)
	if b >= a {
		displayFill(dc, RECT{Left: int32(a), Right: int32(b) + 1, Top: mid - px(3), Bottom: mid + px(3)}, displaycore.RGB{R: 82, G: 131, B: 205})
		for _, x := range []int{a, b} {
			displayFill(dc, RECT{Left: int32(x) - px(3), Right: int32(x) + px(3), Top: bar.Top, Bottom: bar.Bottom}, displaycore.RGB{R: 38, G: 92, B: 170})
		}
	}
	label("拖动端点调整范围，拖动蓝色区间可整体平移（仅草稿）。", plan.Hint)
}
