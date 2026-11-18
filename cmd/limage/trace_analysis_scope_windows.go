//go:build windows

package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

type traceAnalysisScopeMode uint8

const (
	traceAnalysisFollow traceAnalysisScopeMode = iota
	traceAnalysisFull
	traceAnalysisManual
)

// A committed analysis window belongs to the source selection, not the plot.
// Integral physical microseconds match the SEG-Y/sfdata reader's time axis.
type traceAnalysisScope struct {
	mode            traceAnalysisScopeMode
	firstUS, lastUS int64
}

var traceAnalysisWindow traceAnalysisScope
var traceAnalysisManualDraft bool

var traceAnalysisScopeChoices = [...]string{"跟随主剖面可见时间窗", "完整道（各侧实际覆盖）", "手动时间窗（绝对秒）"}

func traceAnalysisSampleIndices(origin, first, last int64, dt, count int) (int, int, bool) {
	if dt <= 0 || count <= 0 || last < first || last < origin {
		return 0, 0, false
	}
	var lo uint64
	if first > origin {
		d := uint64(first) - uint64(origin)
		lo = d / uint64(dt)
		if d%uint64(dt) != 0 {
			lo++
		}
	}
	hi := min(uint64(count-1), (uint64(last)-uint64(origin))/uint64(dt))
	if lo > hi {
		return 0, 0, false
	}
	return int(lo), int(hi), true
}

func formatTraceAnalysisSeconds(us int64) string {
	var sign string
	n := uint64(us)
	if us < 0 {
		sign, n = "-", 0-n
	}
	if n%1_000_000 == 0 {
		return sign + strconv.FormatUint(n/1_000_000, 10)
	}
	return strings.TrimRight(fmt.Sprintf("%s%d.%06d", sign, n/1_000_000, n%1_000_000), "0")
}

func parseTraceAnalysisSeconds(text string) (int64, error) {
	s := strings.TrimSpace(text)
	if s == "" {
		return 0, fmt.Errorf("请输入绝对时间（秒）")
	}
	negative := s[0] == '-'
	if negative || s[0] == '+' {
		s = s[1:]
	}
	whole, fraction, havePoint := strings.Cut(s, ".")
	if whole == "" && fraction == "" {
		return 0, fmt.Errorf("请输入有效的时间（秒）")
	}
	digits := func(s string) bool {
		for _, c := range s {
			if c < '0' || c > '9' {
				return false
			}
		}
		return true
	}
	if !digits(whole) || !digits(fraction) || (havePoint && fraction == "") {
		return 0, fmt.Errorf("请输入十进制秒数，如 0.800001")
	}
	fraction = strings.TrimRight(fraction, "0")
	if len(fraction) > 6 {
		return 0, fmt.Errorf("时间只能精确到 1 微秒（秒的小数点后最多 6 位）")
	}
	seconds := uint64(0)
	if whole != "" {
		var err error
		seconds, err = strconv.ParseUint(whole, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("时间超出可表示范围")
		}
	}
	micros, _ := strconv.ParseUint(fraction+strings.Repeat("0", 6-len(fraction)), 10, 64)
	limit := uint64(math.MaxInt64)
	if negative {
		limit++
	}
	if seconds > (limit-micros)/1_000_000 {
		return 0, fmt.Errorf("时间超出可表示范围")
	}
	n := seconds*1_000_000 + micros
	if negative {
		return int64(0 - n), nil
	}
	return int64(n), nil
}

func traceAnalysisActualWindow(r *traceAnalysisResult) (first, last int64, ok bool) {
	if r == nil || r.snapshot == nil {
		return 0, 0, false
	}
	for _, s := range r.snapshot.Series {
		if len(s.Samples) == 0 || s.IntervalUS <= 0 {
			continue
		}
		if int64(len(s.Samples)-1) > math.MaxInt64/int64(s.IntervalUS) {
			continue
		}
		delta := int64(len(s.Samples)-1) * int64(s.IntervalUS)
		if s.StartUS > math.MaxInt64-delta {
			continue
		}
		end := s.StartUS + delta
		if !ok {
			first, last, ok = s.StartUS, end, true
		} else {
			first, last = min(first, s.StartUS), max(last, end)
		}
	}
	return
}

func traceAnalysisFillManualScope() {
	u := &traceAnalysisUI.scientific
	var first, last int64
	ok := true
	if traceAnalysisWindow.mode == traceAnalysisManual {
		first, last = traceAnalysisWindow.firstUS, traceAnalysisWindow.lastUS
	} else {
		first, last, ok = traceAnalysisActualWindow(traceAnalysisCurrent)
	}
	if !ok {
		setText(u.scopeFirst, "")
		setText(u.scopeLast, "")
		return
	}
	setText(u.scopeFirst, formatTraceAnalysisSeconds(first))
	setText(u.scopeLast, formatTraceAnalysisSeconds(last))
}

func traceAnalysisChooseScope(index int) {
	if index < 0 || index > int(traceAnalysisManual) {
		return
	}
	mode := traceAnalysisScopeMode(index)
	if mode == traceAnalysisManual {
		if !traceAnalysisManualDraft {
			traceAnalysisManualDraft = true
			traceAnalysisFillManualScope()
		}
		traceScientificStyle.status = ""
		traceScientificRepaint()
		return
	}
	traceAnalysisManualDraft = false
	traceAnalysisAbortManualRequest()
	traceAnalysisSetScopeFeedback("", "")
	traceScientificStyle.status = ""
	if mode != traceAnalysisWindow.mode {
		traceAnalysisWindow.mode = mode
		startTraceAnalysisLoad()
	} else {
		traceScientificRepaint()
	}
}

func traceAnalysisApplyManualScope() {
	if !traceAnalysisManualDraft || traceAnalysisPending {
		return
	}
	u := &traceAnalysisUI.scientific
	first, e1 := parseTraceAnalysisSeconds(getText(u.scopeFirst))
	last, e2 := parseTraceAnalysisSeconds(getText(u.scopeLast))
	if e1 != nil || e2 != nil || first >= last {
		switch {
		case e1 != nil:
			traceAnalysisSetScopeFeedback("起点无效", "分析起点："+e1.Error())
			pSetFocus.Call(u.scopeFirst)
		case e2 != nil:
			traceAnalysisSetScopeFeedback("终点无效", "分析终点："+e2.Error())
			pSetFocus.Call(u.scopeLast)
		default:
			traceAnalysisSetScopeFeedback("起止倒置", "分析起点必须早于终点；当前分析不变。")
			pSetFocus.Call(u.scopeLast)
		}
		traceScientificRepaint()
		return
	}
	window := traceAnalysisScope{traceAnalysisManual, first, last}
	traceAnalysisSetScopeFeedback("", "")
	traceScientificStyle.status = ""
	if window == traceAnalysisWindow {
		traceScientificResetTimeView()
		traceScientificRepaint()
		return
	}
	// Resolve against the real trace grids. A valid empty interval is a new
	// empty view; read failures leave the previous analysis committed.
	startTraceAnalysisLoadInScope(window, true)
}

func traceAnalysisCancelManualScope() {
	if !traceAnalysisManualDraft {
		return
	}
	traceAnalysisAbortManualRequest()
	traceAnalysisManualDraft = traceAnalysisWindow.mode == traceAnalysisManual
	if traceAnalysisManualDraft {
		traceAnalysisFillManualScope()
	}
	traceAnalysisSetScopeFeedback("", "")
	pSendMessageW.Call(traceAnalysisUI.window, CB_SETCURSEL, uintptr(traceAnalysisWindow.mode), 0)
	traceScientificStyle.status = ""
	traceScientificRepaint()
}

func traceAnalysisSetScopeFeedback(short, detail string) {
	u := &traceAnalysisUI.scientific
	setText(u.scopeFeedback, short)
	if u.tools != nil {
		u.tools.setPathTip(traceAnalysisHwnd, 0, u.scopeFeedback, detail)
	}
}
