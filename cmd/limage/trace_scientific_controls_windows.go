//go:build windows

package main

import (
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segyanalysis"
)

const (
	idTraceShowA = 7250 + iota
	idTraceShowB
	idTraceShowDiff
	idTraceLayout
	idTraceResetPlot
	idTraceWaveSettings
	idTraceSpectrumSettings
	idTraceExportSettings
	idTraceSave
	idTraceApplyAmp
	idTraceApplySpectrum
	idTracePaper
	idTraceSpectrumMode
	idTraceResetSpectrum
	idTraceCurveStyle
	idTraceApplyScope
	idTraceCancelScope
)

type traceScientificControls struct {
	tools                                                                                         *poststackToolbarResources
	checks                                                                                        [3]uintptr
	layout, reset, waveCfg, specCfg, save, exportCfg                                              uintptr
	ampLabel, ampLo, ampHi, symmetric, gainLabel, gain, splitLabel, split, timeUnits, applyAmp    uintptr
	widthLabel, widthEdit, curveStyle, resetSpec                                                  uintptr
	scopeStartLabel, scopeEndLabel, scopeFirst, scopeLast, scopeApply, scopeCancel, scopeFeedback uintptr
	specMode, hzLabel, hzLo, hzHi, levelLabel, levelLo, levelHi, applySpec                        uintptr
	preset, wxLabel, wx, hxLabel, hx, dpiLabel, dpi                                               uintptr
	info, warning                                                                                 uintptr
	stats                                                                                         traceScientificStatsControls
}

type traceScientificDisplay struct {
	visible                            [3]bool
	mode                               segyanalysis.AmplitudeScaleMode
	manual                             segyanalysis.DisplayRange
	symmetric, hasManual               bool
	waveOpen, spectrumOpen, exportOpen bool
	paper                              bool
	status                             string
	options                            traceScientificPlotOptions
	zoom                               traceScientificViewport
	// Actual source values remain here even if their field caption is shortened.
	ampFieldText  [2]string
	ampFieldRange segyanalysis.DisplayRange
}

var traceScientificStyle = traceScientificDisplay{visible: [3]bool{true, true, true}, symmetric: true, mode: segyanalysis.ScaleFull,
	options: traceScientificPlotOptions{TimeInMS: true, ResearchStyle: true}}

type traceScientificStylePreference struct {
	Research, HideGrid, TimeInMS bool
	LineWidthDIP                 float64
	CurveColor                   [3]uint32
	CurveColorSet                [3]bool
	CurvePattern                 [3]int
}

func loadTraceScientificStylePreference() {
	displayLoadPrefs()
	p := displayPrefs.TraceStyle
	if p == nil {
		return
	}
	o := &traceScientificStyle.options
	o.ResearchStyle, o.HideGrid, o.TimeInMS = p.Research, p.HideGrid, p.TimeInMS
	if traceScientificFinite(p.LineWidthDIP) && p.LineWidthDIP >= .5 && p.LineWidthDIP <= 4 {
		o.LineWidthDIP = p.LineWidthDIP
	}
	o.CurveColor, o.CurveColorSet, o.CurvePattern = p.CurveColor, p.CurveColorSet, p.CurvePattern
	for i, v := range o.CurvePattern {
		if v < 0 || v > 3 {
			o.CurvePattern[i] = 0
		}
	}
}

func persistTraceScientificStylePreference() {
	displayLoadPrefs()
	o := traceScientificStyle.options
	displayPrefs.TraceStyle = &traceScientificStylePreference{
		Research: o.ResearchStyle, HideGrid: o.HideGrid, TimeInMS: o.TimeInMS,
		LineWidthDIP: o.LineWidthDIP, CurveColor: o.CurveColor, CurveColorSet: o.CurveColorSet, CurvePattern: o.CurvePattern,
	}
	displayWritePrefs()
}

var traceScientificActiveScene traceScientificScene
var traceScientificContentTop int
var traceScientificDrag struct {
	active            bool
	plot              traceScientificBounds
	y                 int
	time, frequency   segyanalysis.DisplayRange
	spectrum, reverse bool
	x                 int
}

func traceScientificButton(id, icon int, label, name string, toggle ...bool) uintptr {
	u := &traceAnalysisUI.scientific
	if u.tools == nil {
		u.tools = &poststackToolbarResources{}
		u.tools.createTooltipWindow(traceAnalysisHwnd)
	}
	style := uint32(WS_TABSTOP | BS_PUSHBUTTON)
	if len(toggle) > 0 && toggle[0] {
		style = WS_TABSTOP | BS_AUTOCHECKBOX | BS_PUSHLIKE
	}
	h := createPoststackToolButtonWithAction(traceAnalysisHwnd, id, icon, label, name, style, 0, 0, 60, 24)
	u.tools.addControlTip(traceAnalysisHwnd, h, name)
	return h
}

func createTraceToolbarLabel(owner uintptr, text string) uintptr {
	// The layout gives captions the same row height as the buttons and edits.
	// Center the native text vertically within that row, but keep it left-aligned.
	const centerImage, notify = 0x0200, 0x0100 // SS_CENTERIMAGE, SS_NOTIFY
	return createCtrl(owner, "STATIC", text, WS_CHILD|centerImage|notify, 0, 0, 0, 0, 0)
}

func createTraceScientificControls() {
	loadTraceScientificStylePreference()
	u := &traceAnalysisUI.scientific
	owner := traceAnalysisHwnd
	label := func(s string) uintptr { return createCtrl(owner, "STATIC", s, WS_CHILD|0x0080, 0, 0, 80, 22, 0) }
	edit := func(s string) uintptr {
		return createCtrl(owner, "EDIT", s, WS_CHILD|WS_TABSTOP|WS_BORDER|ES_AUTOHSCROLL, 0, 0, 72, 24, 0)
	}
	check := func(s string, id int, on bool) uintptr {
		h := createCtrl(owner, "BUTTON", s, WS_CHILD|WS_TABSTOP|BS_AUTOCHECKBOX, 0, 0, 84, 24, id)
		pSendMessageW.Call(h, BM_SETCHECK, uintptr(boolToInt(on)), 0)
		return h
	}
	combo := func(items []string, id, selection int) uintptr {
		h := createCtrl(owner, "COMBOBOX", "", WS_CHILD|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 0, 0, 120, 120, id)
		poststackParamCombo(h, items, selection)
		return h
	}
	for i, s := range []string{"A", "B", "差 A−B"} {
		u.checks[i] = check(s, idTraceShowA+i, traceScientificStyle.visible[i])
	}
	u.layout = combo([]string{"波形叠加", "波形并列"}, idTraceLayout, boolToInt(traceScientificStyle.options.SideBySide))
	u.reset = traceScientificButton(idTraceResetPlot, IDCMP_REFRESH, "复位图内视窗", "恢复图内显示范围，不重新计算频谱")
	u.waveCfg = traceScientificButton(idTraceWaveSettings, ID_PARAMS, "振幅设置", "编辑振幅、显示增益、时间单位与曲线样式", true)
	u.specCfg = traceScientificButton(idTraceSpectrumSettings, IDCMP_SPECTRUM, "频谱范围", "调整频谱线性／dB、显示频率和纵轴范围", true)
	u.exportCfg = traceScientificButton(idTraceExportSettings, ID_PARAMS, "导出设置", "屏幕或论文尺寸及 PNG DPI；不改变数据", true)
	u.save = traceScientificButton(idTraceSave, IDCMP_EXPORT, "导出", "导出已显示的分析快照为 PNG 或原生矢量 SVG")
	u.ampLabel, u.ampLo, u.ampHi = label("振幅 下/上"), edit(""), edit("")
	u.symmetric = check("关于 0 对称", 0, traceScientificStyle.symmetric)
	u.gainLabel, u.gain = label("显示增益 ×"), edit("1")
	u.splitLabel, u.split = label("波形宽 %"), edit("50")
	u.timeUnits = combo([]string{"时间 (s)", "时间 (ms)"}, 0, boolToInt(traceScientificStyle.options.TimeInMS))
	u.applyAmp = traceScientificButton(idTraceApplyAmp, poststackGlyphGo, "应用", "仅应用波形显示设置")
	u.widthLabel, u.widthEdit = label("线宽 DIP"), edit("1.2")
	u.curveStyle = traceScientificButton(idTraceCurveStyle, ID_PARAMS, "图形样式", "切换科研简洁样式、网格或自定义 A、B 和差值曲线")
	u.specMode = combo([]string{"共同 dB", "线性振幅"}, idTraceSpectrumMode, boolToInt(traceScientificStyle.options.LinearSpectrum))
	u.hzLabel, u.hzLo, u.hzHi = label("频率 (Hz)"), edit(""), edit("")
	u.levelLabel, u.levelLo, u.levelHi = label("dB 范围"), edit(""), edit("")
	u.applySpec = traceScientificButton(idTraceApplySpectrum, poststackGlyphGo, "应用", "两端留空为完整自适应；手动上下限只裁剪显示，不改变 FFT")
	u.resetSpec = traceScientificButton(idTraceResetSpectrum, IDCMP_REFRESH, "恢复自动", "将基础频率及纵轴范围恢复自动")
	u.preset = combo([]string{"屏幕预设", "论文预设（300 DPI）"}, idTracePaper, boolToInt(traceScientificStyle.paper))
	u.wxLabel, u.wx = label("宽 px"), edit("")
	u.hxLabel, u.hx = label("高 px"), edit("")
	u.dpiLabel, u.dpi = label("DPI"), edit("")
	u.info = label("")
	u.warning = label("")
	// Win32 exposes a preceding static's text as the edit's accessible name.
	u.scopeStartLabel, u.scopeFirst = createTraceToolbarLabel(owner, "起 (s)"), edit("")
	u.scopeEndLabel, u.scopeLast = createTraceToolbarLabel(owner, "止 (s)"), edit("")
	u.scopeApply = traceScientificButton(idTraceApplyScope, poststackGlyphGo, "应用", "应用时间窗：按绝对物理时间（秒）更新单道波形、频谱和统计")
	u.scopeCancel = traceScientificButton(idTraceCancelScope, poststackGlyphClose, "取消", "取消未应用的手动时间窗，不改变分析结果")
	u.scopeFeedback = createTraceToolbarLabel(owner, "")
	if traceAnalysisManualDraft {
		traceAnalysisFillManualScope()
	}
	u.stats = createTraceScientificStats(owner)
	traceScientificFillAmpFields()
	traceScientificFillSpectrumFields()
	traceScientificFillExportFields()
}

type traceScientificPlacement struct {
	h     uintptr
	width float64
	combo bool
}
type traceScientificBar struct{ tabs, tabHeight, content int }

func layoutTraceScientificControls(width, height int) traceScientificBar {
	u := &traceAnalysisUI.scientific
	dpi := displayDPI(traceAnalysisHwnd)
	px := func(d float64) int { return displayPixelDIP(d, dpi) }
	row, gap, edge := px(30), px(5), px(9)
	font, _, _ := pSendMessageW.Call(traceAnalysisUI.windowLabel, 0x0031 /* WM_GETFONT */, 0, 0)
	measure, release := poststackToolbarFontMeasure(font)
	defer release()
	row = max(row, traceScientificControlTextSize(traceAnalysisUI.windowLabel).Height+px(13))
	inner := max(1, width-2*edge)
	y, x := edge, edge
	alignTop := true
	put := func(groups ...[]traceScientificPlacement) {
		for _, group := range groups {
			n := 0
			for _, p := range group {
				n += px(p.width) + gap
			}
			if x > edge && x+n > width-edge {
				x = edge
				y += row
			}
			for _, p := range group {
				w := min(px(p.width), inner)
				if x > edge && x+w > width-edge {
					x = edge
					y += row
				}
				h, top := row-px(6), y
				if p.h != 0 && (p.h == u.scopeFirst || p.h == u.scopeLast) {
					// A native single-line EDIT paints from the top of its
					// client area. Center its natural height in the row
					// without changing its keyboard, selection or IME behavior.
					if natural := traceScientificEditHeight(p.h, dpi); natural > 0 && natural < h {
						top += (h - natural + 1) / 2
						h = natural
					}
				}
				if p.combo {
					if alignTop && p.h != 0 {
						var rect RECT
						if ok, _, _ := algorithmGetWindowRect.Call(p.h, uintptr(unsafe.Pointer(&rect))); ok != 0 {
							top += max(0, (h-int(rect.Bottom-rect.Top))/2)
						}
					}
					h = px(135)
				}
				if p.h != 0 {
					const noCopyBits = 0x0100 // SWP_NOCOPYBITS: do not retain pixels from the previous layout.
					pSetWindowPos.Call(p.h, 0, uintptr(x), uintptr(top), uintptr(w), uintptr(h), SWP_NOZORDER|SWP_NOACTIVATE|noCopyBits)
				}
				x += w + gap
			}
		}
	}
	rowBreak := func() {
		if x > edge {
			x = edge
			y += row
		}
	}
	p := func(h uintptr, w float64) traceScientificPlacement { return traceScientificPlacement{h: h, width: w} }
	c := func(h uintptr, w float64) traceScientificPlacement {
		return traceScientificPlacement{h: h, width: w, combo: true}
	}
	dip := func(pixels int) float64 { return float64(pixels) * 96 / float64(dpi) }
	labelWidth := func(text string, minDIP float64) float64 {
		return dip(max(px(minDIP), measure(text)+px(3)))
	}
	buttonWidth := func(text string) float64 { return dip(poststackToolButtonTextWidth(text, dpi, measure)) }
	windowWidth := px(154)
	for _, caption := range traceAnalysisScopeChoices {
		windowWidth = max(windowWidth, measure(caption)+px(26)) // reserve the native drop-down arrow
	}
	if traceAnalysisPage == 0 || traceAnalysisPage == 1 {
		put([]traceScientificPlacement{p(traceAnalysisUI.sourceLabel, 61), c(traceAnalysisUI.source, 155)})
	}
	put([]traceScientificPlacement{
		p(traceAnalysisUI.numberLabel, labelWidth(getText(traceAnalysisUI.numberLabel), 57)), p(traceAnalysisUI.number, labelWidth("0000000", 53)),
		p(traceAnalysisUI.prev, buttonWidth("上一道")), p(traceAnalysisUI.next, buttonWidth("下一道")), p(traceAnalysisUI.goTo, buttonWidth("转到")),
	}, []traceScientificPlacement{p(traceAnalysisUI.windowLabel, labelWidth("分析时间窗", 59)), c(traceAnalysisUI.window, dip(windowWidth))})
	if traceAnalysisManualDraft {
		secondsWidth := dip(max(px(60), measure("-0.000001")+px(10)))
		// Place the existing draft fields immediately after the mode control.
		// Treat them as one group: only a genuinely narrow window needs a new row.
		put([]traceScientificPlacement{
			p(u.scopeStartLabel, labelWidth("起 (s)", 31)), p(u.scopeFirst, secondsWidth),
			p(u.scopeEndLabel, labelWidth("止 (s)", 31)), p(u.scopeLast, secondsWidth),
			p(u.scopeApply, buttonWidth("应用")), p(u.scopeCancel, buttonWidth("取消")),
			p(u.scopeFeedback, labelWidth("窗内无样点", 62)),
		})
	}
	if traceAnalysisPage != 2 {
		put([]traceScientificPlacement{p(traceAnalysisUI.copy, 56), p(traceAnalysisUI.close, 56)})
	}
	rowBreak()
	alignTop = false
	if traceAnalysisPage == 2 {
		put([]traceScientificPlacement{p(u.checks[0], 43), p(u.checks[1], 43), p(u.checks[2], 76), c(u.layout, 104), c(traceAnalysisUI.scale, 149)}, []traceScientificPlacement{p(u.waveCfg, 85), p(u.reset, 105)}, []traceScientificPlacement{p(traceAnalysisUI.copy, 56), p(u.save, 56), p(traceAnalysisUI.close, 56)})
		rowBreak()
	}
	bar := traceScientificBar{tabs: y, tabHeight: px(28), content: y + px(31)}
	if traceAnalysisPage == 3 {
		u.stats.layout(edge, bar.content, inner, height-bar.content-edge, dpi)
	}
	if traceAnalysisPage != 2 {
		traceScientificContentTop = bar.content
		return bar
	}
	y = bar.content
	put([]traceScientificPlacement{p(u.specCfg, 98), p(u.exportCfg, 88)})
	left := x
	if width-left-edge > max(px(175), traceScientificControlTextWidth(u.info)+px(4)) {
		pMoveWindow.Call(u.info, uintptr(left), uintptr(y+px(2)), uintptr(width-left-edge), uintptr(row-px(5)), 1)
		rowBreak()
	} else {
		rowBreak()
		pMoveWindow.Call(u.info, uintptr(edge), uintptr(y+px(2)), uintptr(inner), uintptr(row-px(5)), 1)
		y += row
	}
	if traceScientificStyle.waveOpen {
		put([]traceScientificPlacement{p(u.ampLabel, 66), p(u.ampLo, 74), p(u.ampHi, 74), p(u.symmetric, 98)},
			[]traceScientificPlacement{p(u.gainLabel, 78), p(u.gain, 44)},
			[]traceScientificPlacement{p(u.splitLabel, 64), p(u.split, 42)},
			[]traceScientificPlacement{c(u.timeUnits, 90)},
			[]traceScientificPlacement{p(u.widthLabel, 60), p(u.widthEdit, 42)},
			[]traceScientificPlacement{p(u.curveStyle, 88), p(u.applyAmp, 55)})
		rowBreak()
	}
	if traceScientificStyle.spectrumOpen {
		put([]traceScientificPlacement{c(u.specMode, 92), p(u.hzLabel, 78), p(u.hzLo, 57), p(u.hzHi, 57)}, []traceScientificPlacement{p(u.levelLabel, 90), p(u.levelLo, 62), p(u.levelHi, 62), p(u.applySpec, 55), p(u.resetSpec, 84)})
		rowBreak()
	}
	if traceScientificStyle.exportOpen {
		put([]traceScientificPlacement{c(u.preset, 145), p(u.wxLabel, 46), p(u.wx, 60), p(u.hxLabel, 46), p(u.hx, 60)}, []traceScientificPlacement{p(u.dpiLabel, 32), p(u.dpi, 54)})
		rowBreak()
	}
	if getText(u.warning) != "" {
		pMoveWindow.Call(u.warning, uintptr(edge), uintptr(y+px(2)), uintptr(inner), uintptr(row-px(5)), 1)
		y += row
	}
	traceScientificContentTop = y
	return bar
}

func traceScientificControlTextWidth(h uintptr) int {
	return traceScientificControlTextSize(h).Width
}

func traceScientificControlTextSize(h uintptr) displayTextSize {
	return traceScientificTextSize(h, getText(h))
}

func traceScientificTextSize(h uintptr, text string) displayTextSize {
	font, _, _ := pSendMessageW.Call(h, 0x0031 /* WM_GETFONT */, 0, 0)
	dc, _, _ := displayGetDC.Call(h)
	if dc == 0 {
		return displayTextSize{}
	}
	defer displayReleaseDC.Call(h, dc)
	restore := displaySelectPlotFont(dc, font)
	defer restore()
	return displayPlotTextExtent(dc, syscall.StringToUTF16(text), 16)
}

func traceScientificEditHeight(h uintptr, dpi int) int {
	var outer RECT
	if ok, _, _ := algorithmGetWindowRect.Call(h, uintptr(unsafe.Pointer(&outer))); ok == 0 {
		return 0
	}
	inner := clientRect(h)
	line := traceScientificTextSize(h, "0").Height
	if line <= 0 {
		return 0
	}
	return line + max(0, int((outer.Bottom-outer.Top)-(inner.Bottom-inner.Top))) + displayPixelDIP(2, dpi)
}

func traceScientificHasRole(role string) bool {
	if traceAnalysisCurrent == nil || traceAnalysisCurrent.snapshot == nil {
		return false
	}
	for _, v := range traceAnalysisCurrent.snapshot.Series {
		if traceScientificRole(v.Role) == role {
			return true
		}
	}
	return false
}

func traceScientificUnavailable() string {
	return traceScientificResultIssues(traceAnalysisCurrent)
}

func refreshTraceScientificUI() {
	u := &traceAnalysisUI.scientific
	if traceAnalysisHwnd == 0 || u.info == 0 {
		return
	}
	analysis := traceAnalysisPage == 2
	showControls([]uintptr{traceAnalysisUI.source, traceAnalysisUI.sourceLabel}, !analysis && traceAnalysisPage != 3)
	showControls([]uintptr{traceAnalysisUI.number, traceAnalysisUI.numberLabel, traceAnalysisUI.prev, traceAnalysisUI.next, traceAnalysisUI.goTo, traceAnalysisUI.windowLabel, traceAnalysisUI.window, traceAnalysisUI.copy, traceAnalysisUI.close}, true)
	showControls([]uintptr{u.scopeStartLabel, u.scopeEndLabel, u.scopeFirst, u.scopeLast, u.scopeApply, u.scopeCancel}, traceAnalysisManualDraft)
	showControls([]uintptr{u.scopeFeedback}, traceAnalysisManualDraft && getText(u.scopeFeedback) != "")
	for _, h := range []uintptr{u.scopeFirst, u.scopeLast, u.scopeApply} {
		pEnableWindow.Call(h, uintptr(boolToInt(!traceAnalysisPending)))
	}
	showControls([]uintptr{u.checks[0], u.checks[1], u.checks[2], u.layout, traceAnalysisUI.scale, u.reset, u.waveCfg, u.specCfg, u.save, u.exportCfg, u.info}, analysis)
	showControls([]uintptr{u.ampLabel, u.ampLo, u.ampHi, u.symmetric, u.gainLabel, u.gain, u.splitLabel, u.split, u.timeUnits, u.widthLabel, u.widthEdit, u.curveStyle, u.applyAmp}, analysis && traceScientificStyle.waveOpen)
	showControls([]uintptr{u.specMode, u.hzLabel, u.hzLo, u.hzHi, u.levelLabel, u.levelLo, u.levelHi, u.applySpec, u.resetSpec}, analysis && traceScientificStyle.spectrumOpen)
	showControls([]uintptr{u.preset, u.wxLabel, u.wx, u.hxLabel, u.hx, u.dpiLabel, u.dpi}, analysis && traceScientificStyle.exportOpen)
	for _, fold := range []struct {
		h    uintptr
		open bool
	}{{u.waveCfg, traceScientificStyle.waveOpen}, {u.specCfg, traceScientificStyle.spectrumOpen}, {u.exportCfg, traceScientificStyle.exportOpen}} {
		pSendMessageW.Call(fold.h, BM_SETCHECK, uintptr(boolToInt(fold.open)), 0)
	}
	u.stats.show(traceAnalysisPage == 3, traceAnalysisCurrent, traceAnalysisPending)
	status := traceScientificStyle.status
	if traceAnalysisPending && traceAnalysisManualGeneration == 0 {
		status = "正在更新分析；下方仍对应上一次已完成的选择。"
	} else if note := traceScientificUnavailable(); status == "" {
		status = note
	}
	if status == "" {
		status = traceScientificClippingNotice()
	}
	setText(u.warning, status)
	showControls([]uintptr{u.warning}, analysis && status != "")
	for i, role := range []string{"A", "B", "A−B"} {
		pEnableWindow.Call(u.checks[i], uintptr(boolToInt(traceScientificHasRole(role))))
		pSendMessageW.Call(u.checks[i], BM_SETCHECK, uintptr(boolToInt(traceScientificStyle.visible[i] && traceScientificHasRole(role))), 0)
	}
	if traceAnalysisCurrent != nil {
		setText(u.info, traceScientificIdentityText(traceAnalysisCurrent))
	} else {
		setText(u.info, "等待所选道样点与频谱")
	}
	if a := traceAnalysisSelectionCurrent.Anchor; a.Owner != 0 {
		setText(traceAnalysisUI.numberLabel, a.Role+" 物理道号")
		if traceAnalysisPending || getText(traceAnalysisUI.number) == "" {
			setText(traceAnalysisUI.number, strconv.FormatInt(a.Trace+1, 10))
		}
	} else {
		setText(traceAnalysisUI.numberLabel, "物理道号")
	}
	for _, h := range []uintptr{u.ampLo, u.ampHi, u.symmetric} {
		pEnableWindow.Call(h, uintptr(boolToInt(traceScientificStyle.mode == segyanalysis.ScaleManual)))
	}
	if traceScientificStyle.mode != segyanalysis.ScaleManual {
		traceScientificFillAmpBoundFields()
	}
	layoutTraceAnalysis()
}

func traceScientificClippingNotice() string {
	if traceScientificStyle.mode != segyanalysis.ScaleRobust || traceAnalysisCurrent == nil || traceAnalysisCurrent.snapshot == nil {
		return ""
	}
	d := traceAnalysisCurrent.snapshot
	full, robust := d.AmplitudeRange(segyanalysis.ScaleFull, segyanalysis.DisplayRange{}, true), traceScientificEffectiveAmp()
	if full.Min < robust.Min || full.Max > robust.Max {
		return "P1–P99 视窗裁剪超出范围的尖峰；切换共同全幅可查看。数据与统计未改变。"
	}
	return ""
}

func traceScientificIdentityText(r *traceAnalysisResult) string {
	if r == nil {
		return ""
	}
	text := r.selection.Context
	if lo, hi, ok := traceAnalysisActualWindow(r); ok {
		text += fmt.Sprintf(" · 实取 %s–%s s", formatTraceAnalysisSeconds(lo), formatTraceAnalysisSeconds(hi))
	} else if r.manualWindowHasNoSamples() {
		text += " · 实取 0 样点"
	}
	if r.selection.Window.mode == traceAnalysisManual {
		w := r.selection.Window
		text += fmt.Sprintf(" · 手动 %s–%s s", formatTraceAnalysisSeconds(w.firstUS), formatTraceAnalysisSeconds(w.lastUS))
	}
	return text
}

func traceScientificEffectiveAmp() segyanalysis.DisplayRange {
	d := traceScientificStyle
	if d.mode == segyanalysis.ScaleManual && d.hasManual {
		return d.manual
	}
	if traceAnalysisCurrent != nil && traceAnalysisCurrent.snapshot != nil {
		a := traceAnalysisCurrent.snapshot.AmplitudeRange(d.mode, d.manual, true)
		if d.mode == segyanalysis.ScaleFull {
			return traceScientificPaddedAmplitude(a)
		}
		return a
	}
	return segyanalysis.DisplayRange{Min: -1, Max: 1}
}

func traceScientificFillAmpBoundFields() {
	d, u := &traceScientificStyle, &traceAnalysisUI.scientific
	r := traceScientificEffectiveAmp()
	text := [2]string{displaycore.FormatCompactNumber(r.Min), displaycore.FormatCompactNumber(r.Max)}
	d.ampFieldRange, d.ampFieldText = r, text
	if getText(u.ampLo) != text[0] {
		setText(u.ampLo, text[0])
	}
	if getText(u.ampHi) != text[1] {
		setText(u.ampHi, text[1])
	}
}

func traceScientificFillAmpFields() {
	d, u := &traceScientificStyle, &traceAnalysisUI.scientific
	traceScientificFillAmpBoundFields()
	gain := d.options.WaveGain
	if gain == 0 {
		gain = 1
	}
	setText(u.gain, displaycore.FormatCompactNumber(gain))
	split := d.options.WaveWidthPercent
	if split == 0 {
		split = 50
	}
	setText(u.split, strconv.Itoa(split))
	width := d.options.LineWidthDIP
	if width == 0 {
		width = traceScientificDefaultLineWidth
	}
	setText(u.widthEdit, displaycore.FormatCompactNumber(width))
}

func traceScientificReadAmp(a, b string, prior [2]string, exact segyanalysis.DisplayRange, symmetric bool) (segyanalysis.DisplayRange, error) {
	r := exact
	for i, s := range []string{a, b} {
		if s == prior[i] {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil || !traceScientificFinite(v) {
			return exact, fmt.Errorf("振幅边界须为有限数值，未完成的输入未应用")
		}
		if i == 0 {
			r.Min = v
		} else {
			r.Max = v
		}
	}
	if !(r.Min < r.Max) || !traceScientificFinite(r.Max-r.Min) {
		return exact, fmt.Errorf("振幅范围必须满足最小值 < 最大值")
	}
	if symmetric {
		v := max(math.Abs(r.Min), math.Abs(r.Max))
		r.Min, r.Max = -v, v
	}
	return r, nil
}

func traceScientificChooseScale() {
	m := displaySelection(traceAnalysisUI.scale)
	if m < 0 || m > 2 {
		return
	}
	d := &traceScientificStyle
	if segyanalysis.AmplitudeScaleMode(m) == segyanalysis.ScaleManual {
		if d.mode != segyanalysis.ScaleManual && !d.hasManual {
			d.manual = traceScientificEffectiveAmp()
			seen := traceScientificActiveScene
			if traceAnalysisCurrent != nil && seen.style.Data == traceAnalysisCurrent.snapshot && seen.effectiveAxes[0].x.View.Valid() {
				r := seen.effectiveAxes[0].x.View
				d.manual = segyanalysis.DisplayRange{Min: r.Min, Max: r.Max}
			}
			d.hasManual = true
		}
		d.waveOpen = true
	}
	d.mode = segyanalysis.AmplitudeScaleMode(m)
	d.zoom.waveX = displaycore.Range{}
	d.status = ""
	traceScientificFillAmpBoundFields()
	traceScientificRepaint()
}

func traceScientificRepaint() {
	refreshTraceScientificUI()
	pRedrawWindow.Call(traceAnalysisHwnd, 0, 0, RDW_INVALIDATE|RDW_ERASE|RDW_ALLCHILDREN)
}

func traceScientificFillSpectrumFields() {
	d, u := traceScientificStyle.options, &traceAnalysisUI.scientific
	setPair := func(a, b uintptr, on bool, lo, hi float64) {
		if !on {
			setText(a, "")
			setText(b, "")
			return
		}
		setText(a, displaycore.FormatExactNumber(lo))
		setText(b, displaycore.FormatExactNumber(hi))
	}
	setPair(u.hzLo, u.hzHi, d.FrequencySet, d.FrequencyMinHz, d.FrequencyMaxHz)
	caption := "Hz（自动）"
	if d.FrequencySet {
		caption = "Hz（手动）"
	}
	setText(u.hzLabel, caption)
	traceScientificFillSpectrumLevel(d.LinearSpectrum)
}

func traceScientificFillSpectrumLevel(linear bool) {
	d, u := traceScientificStyle.options, &traceAnalysisUI.scientific
	setPair := func(on bool, lo, hi float64) {
		if !on {
			setText(u.levelLo, "")
			setText(u.levelHi, "")
			return
		}
		setText(u.levelLo, displaycore.FormatExactNumber(lo))
		setText(u.levelHi, displaycore.FormatExactNumber(hi))
	}
	if linear {
		caption := "线性（自适应）"
		if d.LinearSet {
			caption = "线性（手动）"
		}
		setText(u.levelLabel, caption)
		setPair(d.LinearSet, d.LinearMin, d.LinearMax)
	} else {
		caption := "dB（自适应）"
		if d.DBSet {
			caption = "dB（手动）"
		}
		setText(u.levelLabel, caption)
		setPair(d.DBSet, d.DBMin, d.DBMax)
	}
}

func traceScientificFillExportFields() {
	u := &traceAnalysisUI.scientific
	if traceScientificStyle.paper {
		setText(u.wx, "2400")
		setText(u.hx, "1500")
		setText(u.dpi, "300")
	} else {
		setText(u.wx, "")
		setText(u.hx, "")
		setText(u.dpi, "")
	}
}

func traceScientificCommand(id, notify int) {
	u, d := &traceAnalysisUI.scientific, &traceScientificStyle
	if id >= idTraceShowA && id <= idTraceShowDiff && notify == 0 {
		d.visible[id-idTraceShowA] = isChecked(u.checks[id-idTraceShowA])
		traceScientificRepaint()
		return
	}
	switch id {
	case idTraceApplyScope:
		traceAnalysisApplyManualScope()
		return
	case idTraceCancelScope:
		traceAnalysisCancelManualScope()
		return
	case idTraceLayout:
		if notify != 1 {
			return
		}
		d.options.SideBySide = displaySelection(u.layout) == 1
	case idTraceWaveSettings:
		d.waveOpen = !d.waveOpen
		if d.waveOpen {
			d.spectrumOpen, d.exportOpen = false, false
		}
	case idTraceSpectrumSettings:
		d.spectrumOpen = !d.spectrumOpen
		if d.spectrumOpen {
			d.waveOpen, d.exportOpen = false, false
		}
	case idTraceSpectrumMode:
		if notify != 1 {
			return
		}
		traceScientificFillSpectrumLevel(displaySelection(u.specMode) == 1)
		return
	case idTraceExportSettings:
		d.exportOpen = !d.exportOpen
		if d.exportOpen {
			d.waveOpen, d.spectrumOpen = false, false
		}
	case idTraceResetPlot:
		d.options.TimeSet = false
		d.zoom = traceScientificViewport{}
	case idTraceResetSpectrum:
		d.options.FrequencySet, d.options.DBSet, d.options.LinearSet = false, false, false
		d.zoom.specX, d.zoom.specDB, d.zoom.specLinear = displaycore.Range{}, displaycore.Range{}, displaycore.Range{}
		traceScientificFillSpectrumFields()
	case idTraceCurveStyle:
		traceScientificCurveMenu()
		return
	case idTracePaper:
		if notify != 1 {
			return
		}
		d.paper = displaySelection(u.preset) == 1
		traceScientificFillExportFields()
	case idTraceSave:
		traceScientificSaveFigure()
		return
	case idTraceApplyAmp:
		gain, err := strconv.ParseFloat(strings.TrimSpace(getText(u.gain)), 64)
		split, e2 := strconv.Atoi(strings.TrimSpace(getText(u.split)))
		line, e3 := strconv.ParseFloat(strings.TrimSpace(getText(u.widthEdit)), 64)
		if err != nil || !traceScientificFinite(gain) || gain <= 0 || gain > 100 || e2 != nil || split < 25 || split > 75 || e3 != nil || !traceScientificFinite(line) || line < .5 || line > 4 {
			d.status = "显示增益须为 0–100 的正数，波形宽度须为 25–75%，线宽须为 0.5–4 DIP；未应用。"
			traceScientificRepaint()
			return
		}
		if d.mode == segyanalysis.ScaleManual {
			sym := isChecked(u.symmetric)
			r, e := traceScientificReadAmp(getText(u.ampLo), getText(u.ampHi), d.ampFieldText, d.ampFieldRange, sym)
			if e != nil {
				d.status = e.Error()
				traceScientificRepaint()
				return
			}
			if r != d.manual {
				d.zoom.waveX = displaycore.Range{}
			}
			d.manual, d.hasManual, d.symmetric = r, true, sym
		}
		d.options.LineWidthDIP = line
		d.options.WaveGain, d.options.WaveWidthPercent, d.options.TimeInMS = gain, split, displaySelection(u.timeUnits) == 1
		traceScientificUseLocalTimeUnit(traceAnalysisHwnd, d.options.TimeInMS)
		persistTraceScientificStylePreference()
		traceScientificFillAmpFields()
	case idTraceApplySpectrum:
		parse := func(a, b uintptr, nonnegative bool) (float64, float64, bool, error) {
			x, y := strings.TrimSpace(getText(a)), strings.TrimSpace(getText(b))
			if x == "" && y == "" {
				return 0, 0, false, nil
			}
			lo, e := strconv.ParseFloat(x, 64)
			hi, f := strconv.ParseFloat(y, 64)
			if e != nil || f != nil || !traceScientificFinite(hi-lo) || !(lo < hi) || nonnegative && lo < 0 {
				return 0, 0, false, fmt.Errorf("请输入有效上下限，或两端均留空使用自动范围")
			}
			return lo, hi, true, nil
		}
		linear := displaySelection(u.specMode) == 1
		x0, x1, xSet, e1 := parse(u.hzLo, u.hzHi, true)
		lo, hi, ySet, e2 := parse(u.levelLo, u.levelHi, linear)
		if e1 != nil || e2 != nil {
			d.status = "频谱范围无效：请输入有限且递增的上下限；两端留空为自动。"
			traceScientificRepaint()
			return
		}
		d.options.LinearSpectrum = linear
		d.zoom.specX, d.zoom.specDB, d.zoom.specLinear = displaycore.Range{}, displaycore.Range{}, displaycore.Range{}
		d.options.FrequencyMinHz, d.options.FrequencyMaxHz, d.options.FrequencySet = x0, x1, xSet
		if linear {
			d.options.LinearMin, d.options.LinearMax, d.options.LinearSet = lo, hi, ySet
		} else {
			d.options.DBMin, d.options.DBMax, d.options.DBSet = lo, hi, ySet
		}
		traceScientificFillSpectrumFields()
	default:
		return
	}
	d.status = ""
	traceScientificRepaint()
}

func traceScientificOptions(r *traceAnalysisResult) traceScientificPlotOptions {
	o := traceScientificStyle.options
	o.DPI = displayDPI(traceAnalysisHwnd)
	o.Data = r.snapshot
	o.Title = traceScientificIdentityText(r)
	o.HideSceneTitle = true
	o.VisibleRoles = make([]string, 0, 3)
	for i, role := range []string{"A", "B", "A−B"} {
		if traceScientificStyle.visible[i] {
			o.VisibleRoles = append(o.VisibleRoles, role)
		}
	}
	if traceScientificStyle.mode == segyanalysis.ScaleManual && traceScientificStyle.hasManual {
		a := traceScientificStyle.manual
		o.AmpSet, o.AmpMin, o.AmpMax = true, a.Min, a.Max
	}
	o.RobustWindow = traceScientificStyle.mode == segyanalysis.ScaleRobust
	if w := r.selection.Window; w.mode == traceAnalysisManual && !o.TimeSet {
		o.TimeSet, o.TimeMinUS, o.TimeMaxUS = true, float64(w.firstUS), float64(w.lastUS)
	}
	traceScientificStyle.zoom.apply(&o)
	if anchor := r.selection.Anchor; anchor.HasMarkerTime {
		o.CursorUS, o.CursorSet = anchor.MarkerTimeUS, true
	} else if len(r.files) > 0 {
		f := r.files[0]
		if dt := f.analysisIntervalUS(); f.target.MarkerSample >= 0 && dt > 0 {
			o.CursorUS = f.timeOriginUS + float64(f.target.MarkerSample+f.target.SourceSampleOffset)*float64(dt)
			o.CursorSet = true
		}
	}
	traceScientificDecorateOptions(traceAnalysisHwnd, r, &o)
	return o
}

func traceScientificResetTimeView() {
	traceScientificStyle.options.TimeSet = false
	traceScientificStyle.zoom.waveY = displaycore.Range{}
	if !traceScientificBox.spectrum {
		traceScientificCancelBox(traceAnalysisHwnd, true)
	}
	if traceScientificDrag.active && !traceScientificDrag.spectrum {
		traceScientificDrag.active = false
		pReleaseCapture.Call()
	}
}

func changeScientificTrace(delta int64, fromEdit bool) {
	d := &traceScientificStyle
	var physical int64
	var err error
	if fromEdit {
		physical, err = strconv.ParseInt(strings.TrimSpace(getText(traceAnalysisUI.number)), 10, 64)
		if err != nil || physical < 1 {
			d.status = "请输入从 1 开始的有效物理道号。"
			traceScientificRepaint()
			return
		}
		physical--
	}
	a, ok := tracePoststackNavigate(traceAnalysisSelectionCurrent.Anchor, physical, delta, fromEdit)
	if !ok {
		d.status = "目标道不在所选剖面上；请在主图选另一条线。"
		traceScientificRepaint()
		return
	}
	s, ok := resolveTracePoststackAnchor(a)
	if !ok {
		d.status = "所选位置没有可用物理道。"
		traceScientificRepaint()
		return
	}
	d.status = ""
	traceAnalysisSelectionCurrent = s
	startTraceAnalysisLoad()
}

func traceScientificSceneForExport() (traceScientificScene, int, error) {
	r := traceAnalysisCurrent
	if r == nil || r.snapshot == nil {
		return traceScientificScene{}, 96, fmt.Errorf("尚无已完成的分析")
	}
	u := &traceAnalysisUI.scientific
	dpi := displayDPI(traceAnalysisHwnd)
	w, h := clientSize(traceAnalysisHwnd)
	h = max(360, h-traceScientificContentTop)
	if traceScientificStyle.paper {
		w, h, dpi = 2400, 1500, 300
	}
	for _, f := range []struct {
		control uintptr
		value   *int
		lo, hi  int
	}{{u.wx, &w, 320, 10000}, {u.hx, &h, 240, 10000}, {u.dpi, &dpi, 72, 600}} {
		s := strings.TrimSpace(getText(f.control))
		if s == "" {
			continue
		}
		n, e := strconv.Atoi(s)
		if e != nil || n < f.lo || n > f.hi {
			return traceScientificScene{}, dpi, fmt.Errorf("导出尺寸或 DPI 无效（宽 320–10000，高 240–10000，DPI 72–600）")
		}
		*f.value = n
	}
	if int64(w)*int64(h) > 36_000_000 {
		return traceScientificScene{}, dpi, fmt.Errorf("PNG 图像上限为 3600 万像素")
	}
	o := traceScientificOptions(r)
	o.DPI = dpi
	o.HideSceneTitle = false // exported graph does not have the live toolbar summary
	// Freeze both data-derived geometry and display choices before a native
	// save dialog can service a new trace or B completion in its message loop.
	return traceScientificPlotScene(w, h, r, o), dpi, nil
}

func traceScientificCopyFigure() {
	s, dpi, err := traceScientificSceneForExport()
	if err == nil {
		err = s.copyPNG(traceAnalysisHwnd, dpi)
	}
	if err != nil {
		traceScientificStyle.status = err.Error()
		traceScientificRepaint()
	}
}

func traceScientificSaveFigure() {
	owner := traceAnalysisHwnd
	s, dpi, err := traceScientificSceneForExport()
	if err != nil {
		traceScientificStyle.status = err.Error()
		traceScientificRepaint()
		return
	}
	var buf [32768]uint16
	copy(buf[:], syscall.StringToUTF16("单道分析"))
	filter, _ := syscall.UTF16FromString("PNG 图片")
	filter = append(filter, syscall.StringToUTF16("*.png")...)
	filter = append(filter, syscall.StringToUTF16("SVG 矢量")...)
	filter = append(filter, syscall.StringToUTF16("*.svg")...)
	filter = append(filter, 0)
	ofn := OPENFILENAME{LStructSize: uint32(unsafe.Sizeof(OPENFILENAME{})), HwndOwner: owner, LpstrFilter: uintptr(unsafe.Pointer(&filter[0])), NFilterIndex: 1, LpstrFile: uintptr(unsafe.Pointer(&buf[0])), NMaxFile: uint32(len(buf)), Flags: OFN_EXPLORER | OFN_OVERWRITEPROMPT}
	if ok, _, _ := pGetSaveFileNameW.Call(uintptr(unsafe.Pointer(&ofn))); ok == 0 {
		return
	}
	path := syscall.UTF16ToString(buf[:])
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		if ofn.NFilterIndex == 2 {
			ext = ".svg"
		} else {
			ext = ".png"
		}
		path += ext
	}
	switch ext {
	case ".svg":
		err = s.writeSVG(path)
	case ".png":
		err = s.writePNG(path, dpi)
	default:
		err = fmt.Errorf("仅支持 PNG 或 SVG 输出")
	}
	if owner != traceAnalysisHwnd {
		return
	}
	if err != nil {
		traceScientificStyle.status = "导出失败：" + err.Error()
	} else {
		traceScientificStyle.status = "已导出：" + path
	}
	traceScientificRepaint()
}

func traceScientificMouse(h uintptr, msg uint32, wParam, lParam uintptr) bool {
	if msg == 0x0215 || msg == 0x001F {
		traceScientificDrag.active = false
		traceScientificCancelBox(h, msg == 0x001F)
		return false
	}
	if traceAnalysisPage != 2 || traceAnalysisCurrent == nil || traceAnalysisCurrent.snapshot == nil {
		return false
	}
	x, y := int(int16(lParam)), int(int16(lParam>>16))
	if msg == WM_MOUSEWHEEL {
		p := POINT{X: int32(x), Y: int32(y)}
		pScreenToClient.Call(h, uintptr(unsafe.Pointer(&p)))
		x, y = int(p.X), int(p.Y)
	}
	y -= traceScientificContentTop
	if traceScientificMouseBox(h, msg, traceScientificPoint{float64(x), float64(y)}) {
		return true
	}
	s := &traceScientificActiveScene
	inside := func(r traceScientificBounds) bool {
		return float64(x) >= r.l && float64(x) <= r.r && float64(y) >= r.t && float64(y) <= r.b
	}
	if msg == WM_RBUTTONUP && traceScientificDrag.active {
		traceScientificDrag.active = false
		pReleaseCapture.Call()
		return true
	}
	z := &traceScientificStyle.zoom
	if msg == WM_MOUSEMOVE && traceScientificDrag.active {
		d := traceScientificDrag
		direction := 1.0
		if d.reverse {
			direction = -1
		}
		if d.spectrum {
			delta := direction * float64(d.x-x) / max(1, d.plot.width()) * (d.frequency.Max - d.frequency.Min)
			width := d.frequency.Max - d.frequency.Min
			lo := max(0, d.frequency.Min+delta)
			z.specX = displaycore.Range{Min: lo, Max: lo + width}
		} else {
			delta := direction * float64(d.y-y) / max(1, d.plot.height()) * (d.time.Max - d.time.Min)
			z.waveY = displaycore.Range{Min: (d.time.Min + delta) / 1e6, Max: (d.time.Max + delta) / 1e6}
		}
		pInvalidateRect.Call(h, 0, 0)
		return true
	}
	var r traceScientificBounds
	spec, in := false, false
	if s.SpectrumBounds != nil && inside(*s.SpectrumBounds) {
		r, spec, in = *s.SpectrumBounds, true, true
	} else {
		for _, v := range s.WaveBounds {
			if inside(v) {
				r, in = v, true
				break
			}
		}
	}
	if !in || msg != WM_MOUSEWHEEL && msg != WM_RBUTTONDOWN {
		return false
	}
	d := traceScientificPlotData(traceAnalysisCurrent, traceScientificOptions(traceAnalysisCurrent))
	reverse := d.axes[0].y.Reverse
	if spec {
		reverse = d.axes[1].x.Reverse
	}
	if msg == WM_RBUTTONDOWN {
		traceScientificDrag.active, traceScientificDrag.plot, traceScientificDrag.spectrum = true, r, spec
		traceScientificDrag.reverse = reverse
		traceScientificDrag.y, traceScientificDrag.x = y, x
		traceScientificDrag.time = segyanalysis.DisplayRange{Min: d.time.Min, Max: d.time.Max}
		traceScientificDrag.frequency = segyanalysis.DisplayRange{Min: d.frequency.Min, Max: d.frequency.Max}
		pSetCapture.Call(h)
		return true
	}
	factor := 1.25
	if int16(wParam>>16) > 0 {
		factor = .8
	}
	zoom := func(lo, hi, f float64) (float64, float64) {
		if reverse {
			f = 1 - f
		}
		c := lo + (hi-lo)*f
		return c + (lo-c)*factor, c + (hi-c)*factor
	}
	if spec {
		lo, hi := zoom(d.frequency.Min, d.frequency.Max, (float64(x)-r.l)/max(1, r.width()))
		if hi > max(0, lo) {
			z.specX = displaycore.Range{Min: max(0, lo), Max: hi}
		}
	} else {
		lo, hi := zoom(d.time.Min, d.time.Max, (float64(y)-r.t)/max(1, r.height()))
		if hi > lo && hi-lo >= 1 {
			z.waveY = displaycore.Range{Min: lo / 1e6, Max: hi / 1e6}
		}
	}
	pInvalidateRect.Call(h, 0, 0)
	return true
}
