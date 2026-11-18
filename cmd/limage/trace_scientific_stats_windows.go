//go:build windows

package main

import (
	"fmt"
	"strconv"
	"strings"
	"unsafe"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segyanalysis"
)

const referenceSNRLabel = "A/B 信噪比 SNR（A：含噪；B：干净参考）"
const referenceSNRHelp = "默认把 B 当作干净参考。若 B 只是处理结果、并非真实干净信号，此值仅表示相对 B 的信噪比。"
const referenceSNRFormula = "SNR = 10 log10[Σ B² / Σ (A−B)²]。仅用真实道和采样时间对齐后的共同有限样点，先汇总能量，再计算一次 dB。显示增益、色表和 FFT 不参与计算。"

func ReferenceSNRText(result segyanalysis.ReferenceSNR, reason string) string {
	if result.FormulaVersion != segyanalysis.ReferenceSNRFormulaVersion && result.State != segyanalysis.SNRUnavailable {
		return "—（需按 B 参考重新计算）"
	}
	switch result.State {
	case segyanalysis.SNRFinite:
		// Prevent negative zero introduced by rounding a tiny negative value.
		db := result.DB
		if db > -.005 && db < .005 {
			db = 0
		}
		return fmt.Sprintf("%.2f dB", db)
	case segyanalysis.SNRPerfect:
		return "+∞ dB"
	case segyanalysis.SNRZeroReference:
		return "−∞ dB"
	case segyanalysis.SNRBothZero:
		return "—（参考与误差均为零）"
	default:
		if reason == "" {
			reason = "无共同有效样点"
		}
		return "—（" + reason + "）"
	}
}

type traceStatsTable struct {
	identity, snr, context, note string
	rows                         [][4]string
}

func traceScientificResultIssues(r *traceAnalysisResult) string {
	if r == nil {
		return ""
	}
	var issues []string
	for _, f := range r.files {
		if f.waveError != "" {
			issues = append(issues, f.target.Role+"："+f.waveError)
		}
	}
	if r.diffNote != "" {
		issues = append(issues, r.diffNote)
	}
	return strings.Join(issues, " · ")
}

func makeTraceStatsTable(r *traceAnalysisResult) traceStatsTable {
	d := traceStatsTable{identity: "尚无分析结果"}
	if r == nil || r.snapshot == nil {
		return d
	}
	s := r.snapshot
	d.identity = traceScientificIdentityText(r)
	d.note = traceScientificResultIssues(r)
	short := displaycore.FormatCompactNumber
	window := func(start int64, dt, n int) string {
		end := float64(start) + float64(n-1)*float64(dt)
		return short(float64(start)/1e6) + "–" + short(end/1e6) + " s"
	}
	var columns [3]*segyanalysis.TraceSeries
	for i := range s.Series {
		v := &s.Series[i]
		switch traceScientificRole(v.Role) {
		case "A":
			columns[0] = v
		case "B":
			columns[1] = v
		case "A−B":
			columns[2] = v
		default:
			if !s.Comparison && columns[0] == nil {
				columns[0] = v // existing standalone callers
			}
		}
	}
	if s.Comparison {
		label, windowLabel, help := referenceSNRLabel, "SNR 共同窗", referenceSNRHelp
		why := ""
		if s.ComparisonOverlapSamples == 0 {
			why = strings.TrimPrefix(r.diffNote, "A−B 不可计算：")
			if why == "" && columns[1] == nil {
				why = "B 或对应道不可用"
			}
		}
		d.snr = label + "：" + ReferenceSNRText(s.ReferenceSNR, why)
		if s.ComparisonOverlapSamples > 0 {
			d.snr += fmt.Sprintf(" · %d 对有效样点", s.ReferenceSNR.Pairs)
			d.context = windowLabel + "：" + window(s.ComparisonStartUS, s.ComparisonIntervalUS, s.ComparisonOverlapSamples) + " · "
		}
		d.note = strings.Join(nonemptyTraceStatsNotes(help, d.note), "\r\n")
	} else {
		d.snr = referenceSNRLabel + "：" + ReferenceSNRText(s.ReferenceSNR, "当前没有可配对的 A/B")
	}
	if s.Reference.Fallback {
		d.context += "共同 dB 参考：1（固定 FFT 振幅单位）"
	} else {
		d.context += "共同 dB 参考：" + s.Reference.Role + " 非直流谱峰 " + short(s.Reference.Amplitude)
	}

	row := func(label string, f func(*segyanalysis.TraceSeries) string) [4]string {
		v := [4]string{label, "—", "—", "—"}
		for i, s := range columns {
			if s != nil {
				v[i+1] = f(s)
			}
		}
		return v
	}
	// Avoid repeating identical metadata, but make differences explicit.
	metadata := func(label string, f func(*segyanalysis.TraceSeries) string) {
		v := row(label, f)
		value, common := "", true
		for _, cell := range v[1:] {
			if cell == "—" {
				continue
			}
			if value == "" {
				value = cell
			} else if value != cell {
				common = false
			}
		}
		if value == "" {
			return
		}
		if common {
			d.note = strings.Join(nonemptyTraceStatsNotes(d.note, label+"："+value), " · ")
		} else {
			d.rows = append(d.rows, v)
		}
	}
	if s.ComparisonOverlapSamples == 0 {
		if columns[0] != nil && columns[1] != nil && (columns[0].StartUS != columns[1].StartUS || len(columns[0].Samples) != len(columns[1].Samples) || columns[0].IntervalUS != columns[1].IntervalUS) {
			d.rows = append(d.rows, row("各侧波形时间窗", func(s *segyanalysis.TraceSeries) string { return window(s.StartUS, s.IntervalUS, len(s.Samples)) }))
		}
	} else if columns[0] != nil && columns[1] != nil && (columns[0].StartUS != columns[1].StartUS || len(columns[0].Samples) != len(columns[1].Samples)) {
		d.rows = append(d.rows, row("各侧波形时间窗", func(s *segyanalysis.TraceSeries) string { return window(s.StartUS, s.IntervalUS, len(s.Samples)) }))
	}
	metadata("采样间隔", func(s *segyanalysis.TraceSeries) string {
		if s.IntervalUS <= 0 {
			return "—"
		}
		return short(float64(s.IntervalUS)/1000) + " ms"
	})
	count := func(f func(segyanalysis.TraceStats) int) func(*segyanalysis.TraceSeries) string {
		return func(s *segyanalysis.TraceSeries) string { return strconv.Itoa(f(s.Stats)) }
	}
	value := func(f func(segyanalysis.TraceStats) float64) func(*segyanalysis.TraceSeries) string {
		return func(s *segyanalysis.TraceSeries) string {
			if !s.Stats.HasFinite || !traceScientificFinite(f(s.Stats)) {
				return "—"
			}
			return short(f(s.Stats))
		}
	}
	d.rows = append(d.rows,
		row("样点数", count(func(s segyanalysis.TraceStats) int { return s.SampleCount })),
		row("有效样点", count(func(s segyanalysis.TraceStats) int { return s.FiniteCount })),
		row("非有限样点", count(func(s segyanalysis.TraceStats) int { return s.NonFiniteCount })),
		row("零值", count(func(s segyanalysis.TraceStats) int { return s.ZeroCount })),
		row("最小值", value(func(s segyanalysis.TraceStats) float64 { return s.Min })),
		row("最大值", value(func(s segyanalysis.TraceStats) float64 { return s.Max })),
		row("均值", value(func(s segyanalysis.TraceStats) float64 { return s.Mean })),
		row("RMS", value(func(s segyanalysis.TraceStats) float64 { return s.RMS })),
		row("标准差", value(func(s segyanalysis.TraceStats) float64 { return s.StdDev })),
		row("主频 (Hz)", func(s *segyanalysis.TraceSeries) string {
			if s.SpectrumError != "" {
				return "—"
			}
			if s.Spectrum.PeakAmplitude <= 0 {
				return "零能量"
			}
			return short(s.Spectrum.PeakHz)
		}),
	)
	fftLabel := func(s *segyanalysis.TraceSeries, f func(segyanalysis.TraceSpectrum) string) string {
		if s.SpectrumError != "" || s.Spectrum.NFFT == 0 {
			return "—"
		}
		return f(s.Spectrum)
	}
	metadata("FFT 窗", func(s *segyanalysis.TraceSeries) string {
		f := s.Spectrum
		if s.SpectrumError != "" || f.NFFT == 0 {
			return "—"
		}
		return "Hann / 去均值"
	})
	for _, v := range columns {
		if v != nil && v.Spectrum.NFFT > 0 && s.ComparisonOverlapSamples > 0 && (v.Spectrum.StartUS != s.ComparisonStartUS || v.Spectrum.InputSamples != s.ComparisonOverlapSamples) {
			metadata("FFT 时间窗", func(s *segyanalysis.TraceSeries) string {
				return fftLabel(s, func(f segyanalysis.TraceSpectrum) string { return window(f.StartUS, f.IntervalUS, f.InputSamples) })
			})
			break
		}
	}
	metadata("FFT 样点", func(s *segyanalysis.TraceSeries) string {
		return fftLabel(s, func(f segyanalysis.TraceSpectrum) string { return strconv.Itoa(f.InputSamples) })
	})
	metadata("NFFT", func(s *segyanalysis.TraceSeries) string {
		return fftLabel(s, func(f segyanalysis.TraceSpectrum) string { return strconv.Itoa(f.NFFT) })
	})
	metadata("Nyquist", func(s *segyanalysis.TraceSeries) string {
		return fftLabel(s, func(f segyanalysis.TraceSpectrum) string { return short(f.NyquistHz) + " Hz" })
	})
	for i, column := range columns {
		if column != nil && column.SpectrumError != "" {
			d.note = strings.Join(nonemptyTraceStatsNotes(d.note, [3]string{"A", "B", "A−B"}[i]+" 频谱："+column.SpectrumError), " · ")
		}
	}
	return d
}

func nonemptyTraceStatsNotes(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func traceScientificStatsText(result *traceAnalysisResult) string {
	d := makeTraceStatsTable(result)
	var b strings.Builder
	b.WriteString(strings.Join(nonemptyTraceStatsNotes(d.identity, d.snr, d.context, d.note), "\r\n"))
	if len(d.rows) > 0 {
		b.WriteString("\r\n\r\n指标\tA\tB\tA−B")
	}
	for _, row := range d.rows {
		b.WriteString("\r\n" + strings.Join(row[:], "\t"))
	}
	return b.String()
}

type traceScientificStatsControls struct {
	identity, snr, context, note, list uintptr
	last                               *traceAnalysisResult
	built                              bool
}

func createTraceScientificStats(owner uintptr) traceScientificStatsControls {
	label := func() uintptr { return createCtrl(owner, "STATIC", "", WS_CHILD|0x0080, 0, 0, 1, 1, 0) }
	u := traceScientificStatsControls{identity: label(), snr: label(), context: label(), note: label()}
	u.list = createComparativeStatsList(owner, 7279)
	return u
}

func createComparativeStatsList(owner uintptr, id int) uintptr {
	list := createCtrl(owner, "SysListView32", "", WS_CHILD|WS_TABSTOP|WS_BORDER|WS_VSCROLL|traceLVSReport|traceLVSSingleSel|traceLVSNoSortHeader, 0, 0, 1, 1, id)
	flags := uintptr(traceLVSExGridLines | traceLVSExFullRowSelect | traceLVSExDoubleBuffer)
	pSendMessageW.Call(list, traceLVMSetExtendedListStyle, flags, flags)
	for i, title := range []string{"指标", "A", "B", "A−B"} {
		column := traceHeaderLVColumn{Mask: traceLVCFFormat | traceLVCFWidth | traceLVCFText | traceLVCFSubItem, Cx: 100, PszText: u16(title), ISubItem: int32(i)}
		pSendMessageW.Call(list, traceLVMInsertColumnW, uintptr(i), uintptr(unsafe.Pointer(&column)))
	}
	return list
}

func (u *traceScientificStatsControls) show(show bool, r *traceAnalysisResult, pending bool) {
	if !show || u.list == 0 {
		showControls([]uintptr{u.identity, u.snr, u.context, u.note, u.list}, false)
		return
	}
	pShowWindow.Call(u.list, SW_SHOW)
	d := makeTraceStatsTable(r)
	if pending {
		if r == nil {
			d.identity = "正在读取所选单道…"
		} else {
			d.identity = "正在更新（仍显示上一道） · " + d.identity
		}
	}
	for _, p := range []struct {
		h    uintptr
		text string
	}{{u.identity, d.identity}, {u.snr, d.snr}, {u.context, d.context}, {u.note, d.note}} {
		setText(p.h, p.text)
		showControls([]uintptr{p.h}, p.text != "")
	}
	if u.built && u.last == r {
		return
	}
	u.built, u.last = true, r
	updateComparativeStatsList(u.list, d.rows)
}

func updateComparativeStatsList(list uintptr, rows [][4]string) {
	pSendMessageW.Call(list, traceWMSetRedraw, 0, 0)
	pSendMessageW.Call(list, traceLVMDeleteAllItems, 0, 0)
	for i, row := range rows {
		item := traceHeaderLVItem{Mask: traceLVIFText, IItem: int32(i), PszText: u16(row[0])}
		pSendMessageW.Call(list, traceLVMInsertItemW, 0, uintptr(unsafe.Pointer(&item)))
		for j := 1; j < len(row); j++ {
			setTraceHeaderListCell(list, i, j, row[j])
		}
	}
	pSendMessageW.Call(list, traceWMSetRedraw, 1, 0)
	pInvalidateRect.Call(list, 0, 1)
}

func (u *traceScientificStatsControls) layout(x, y, width, height, dpi int) {
	px := func(n float64) int { return displayPixelDIP(n, dpi) }
	dc, _, _ := displayGetDC.Call(traceAnalysisHwnd)
	if dc != 0 {
		defer displayReleaseDC.Call(traceAnalysisHwnd, dc)
		if hFont != 0 {
			if old, _, _ := pSelectObject.Call(dc, hFont); old != 0 {
				defer pSelectObject.Call(dc, old)
			}
		}
	}
	for _, h := range []uintptr{u.identity, u.snr, u.context, u.note} {
		label := getText(h)
		if label == "" {
			continue
		}
		lineHeight := px(20)
		if dc != 0 {
			r := RECT{Right: int32(width)}
			const wordBreakCalc = 0x10 | 0x0400 | 0x0800 // DT_WORDBREAK | DT_CALCRECT | DT_NOPREFIX
			pDrawTextW.Call(dc, uintptr(unsafe.Pointer(u16(label))), uintptr(^uint32(0)), uintptr(unsafe.Pointer(&r)), wordBreakCalc)
			lineHeight = max(lineHeight, int(r.Bottom)+px(3))
		}
		pMoveWindow.Call(h, uintptr(x), uintptr(y), uintptr(width), uintptr(lineHeight), 1)
		y, height = y+lineHeight, height-lineHeight
	}
	y, height = y+px(3), height-px(3)
	pMoveWindow.Call(u.list, uintptr(x), uintptr(y), uintptr(width), uintptr(max(px(20), height)), 1)
	sizeComparativeStatsColumns(u.list, width, dpi)
}

func sizeComparativeStatsColumns(list uintptr, width, dpi int) {
	px := func(n float64) int { return displayPixelDIP(n, dpi) }
	first := min(px(160), width/3)
	cell := max(1, (width-first-px(21))/3)
	for i, w := range []int{first, cell, cell, cell} {
		pSendMessageW.Call(list, traceLVMSetColumnWidth, uintptr(i), uintptr(w))
	}
}
