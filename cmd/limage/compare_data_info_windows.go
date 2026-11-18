//go:build windows

package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segyanalysis"
)

const (
	compareInfoTimer   = 0x5349
	compareInfoReady   = WM_USER + 259
	compareInfoCloseID = 27001
	compareInfoHelpID  = 27002
	compareInfoCopyID  = 27003
)

type compareInfoKey struct {
	snrFormula uint8
	view       viewFilterKey
	settings   [2]SideFilterState
	bounds     segy.SliceBounds
	samples    [2]int
	preparing  bool
	frameReady bool
	filterWait bool
}

type compareInfoSource struct {
	path     string
	meta     string
	identity os.FileInfo
	geometry *segy.GeometryIndex // read-only; dense lookup initialized by sync.Once
	physical [3]int64
	window   [2]int
	alignToA bool // external B uses A's visible physical time, not A's local indices
	view     *viewFilterRows
	status   string
}

type compareInfoRequest struct {
	key      compareInfoKey
	sources  [2]compareInfoSource
	geometry bool
}

type compareInfoPick struct {
	trace int64
	key   [2]int32 // actual IL/XL; never geometry inferred from array length
}

type compareInfoMoments struct {
	total, valid    int64
	lo, hi          float64
	scale, mean, m2 float64 // normalized Welford state, bounded even for large float64 inputs
}

func (m *compareInfoMoments) add(x float64) {
	m.total++
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return
	}
	if m.valid == 0 {
		m.lo, m.hi = x, x
	} else {
		m.lo, m.hi = math.Min(m.lo, x), math.Max(m.hi, x)
	}
	m.valid++
	if a := math.Abs(x); a > m.scale {
		factor := m.scale / a
		m.mean, m.m2, m.scale = m.mean*factor, m.m2*factor*factor, a
	}
	if m.scale > 0 {
		v := x / m.scale
		d := v - m.mean
		m.mean += d / float64(m.valid)
		m.m2 += d * (v - m.mean)
	}
}

type compareInfoResult struct {
	caption, detail, sides, files string
	paths                         [2]string
	rows                          [][4]string
}

type compareInfoWindow struct {
	h, owner, heading, scope, states, list, fileLabel, body, help, copyPath, close uintptr
	key                                                                            compareInfoKey
	serial                                                                         uint64
	requested                                                                      bool
	cancel                                                                         context.CancelFunc
	headingFont                                                                    uintptr
	fontDPI                                                                        int
	paths                                                                          [2]string
}

func compareInfoTable(stats [3]compareInfoMoments, traceCounts [3]int) [][4]string {
	rows := [][4]string{{"参与统计道数"}, {"有限样点 / 统计样点"}, {"最小值"}, {"最大值"}, {"均值"}, {"RMS"}, {"标准差（总体）"}}
	short := displaycore.FormatCompactNumber
	for i, m := range stats {
		col := i + 1
		for j := range rows {
			rows[j][col] = "—"
		}
		if traceCounts[i] > 0 {
			rows[0][col] = fmt.Sprint(traceCounts[i])
			rows[1][col] = fmt.Sprintf("%d / %d", m.valid, m.total)
		}
		if m.valid == 0 {
			continue
		}
		meanSquareDeviation := math.Max(0, m.m2/float64(m.valid))
		vals := [...]float64{m.lo, m.hi, m.scale * m.mean, m.scale * math.Sqrt(math.Min(1, meanSquareDeviation+m.mean*m.mean)), m.scale * math.Sqrt(math.Min(1, meanSquareDeviation))}
		for j, v := range vals {
			rows[j+2][col] = short(v)
		}
	}
	return rows
}

type compareInfoCompletion struct {
	owner, window uintptr
	serial        uint64
	key           compareInfoKey
	result        compareInfoResult
}

var activeCompareInfo *compareInfoWindow
var compareInfoClass bool
var compareInfoCallback = syscall.NewCallback(compareInfoWndProc)
var compareInfoSerial uint64 // allocated on the UI thread; survives HWND reuse
var compareInfoMailbox = struct {
	sync.Mutex
	serial uint64
	ready  *compareInfoCompletion
}{}

func compareInfoCurrentKey() compareInfoKey {
	k := compareInfoKey{snrFormula: segyanalysis.ReferenceSNRFormulaVersion, view: viewFilterCurrentKey(), settings: viewFilter.active, preparing: poststackCompareInputPreparing()}
	// A bitmap repaint, display gain and color changes do not change samples.
	k.view.render = 0
	if d := compareTD; d != nil && compareMode == 2 {
		k.bounds, k.samples = d.bounds, [2]int{d.sampleA, d.sampleB}
	} else if compareMode != 2 {
		k.frameReady = compareInfoFrame(k) != nil
		k.filterWait = !k.frameReady && viewFilterAnyEnabled(k.settings) && viewFilterIsPending()
	}
	return k
}

func compareInfoFrame(k compareInfoKey) *viewFilterSnapshot {
	if f := viewFilter.frame; f != nil && f.settings == k.settings {
		key := f.key
		key.render = 0
		if key == k.view {
			return f
		}
	}
	return nil
}

func captureCompareInfo(k compareInfoKey) compareInfoRequest {
	r := compareInfoRequest{key: k}
	d := compareTD
	if d == nil || d.fa == nil {
		if sf != nil {
			r.sources[0].meta = sf.Info.String()
		}
		return r
	}
	r.geometry = compareDifferenceGeometryKnown(d)
	var frame *viewFilterSnapshot
	if k.frameReady {
		frame = compareInfoFrame(k)
	}
	for i, f := range [2]*segy.File{d.fa, d.fb} {
		if f == nil || (i == 1 && k.view.b != f.Info.Path) {
			continue
		}
		g := d.ga
		if i == 1 {
			g = d.gb
		}
		id, _ := f.SourceFileInfo()
		first, last, step := compareTraceRangeForGeometry(f)
		s := compareInfoSource{path: f.Info.Path, meta: f.Info.String(), identity: id, geometry: g, physical: [3]int64{first, last, step}}
		if i == 1 {
			s.status = compareExportBDescription(f.Info.Path)
		}
		if g != nil {
			s.meta += fmt.Sprintf("\r\nIL byte: %d · XL byte: %d", g.InlineByte, g.CrosslineByte)
		}
		lo, hi := k.view.first, k.view.last
		if k.view.mode == 2 {
			lo, hi = k.samples[i], k.samples[i]
		} else if i == 1 {
			if offset, known := generatedPoststackSourceStart(f.Info.Path); known {
				if start, end, ok := compareSourceSampleOverlap(lo, hi, d.fa.Info.SamplesPerTrace, offset, f.Info.SamplesPerTrace); ok {
					lo, hi = start-offset, end-offset
				} else {
					lo, hi = 1, 0
				}
			} else {
				s.alignToA = true
			}
		}
		if hi < 0 && k.view.mode != 2 {
			hi = f.Info.SamplesPerTrace - 1
		}
		if k.view.mode == 2 && lo < 0 {
			lo, hi = 1, 0 // no actual sample for this source
		}
		s.window = [2]int{max(0, lo), min(hi, f.Info.SamplesPerTrace-1)}
		if frame != nil {
			s.alignToA = false // use the exact windows already committed by the filter renderer
			if row := &frame.rows[i]; row.path != "" && row.path == s.path {
				s.view, s.window = row, [2]int{row.first, row.last}
			} else {
				s.window = [2]int{1, 0}
			}
		}
		r.sources[i] = s
	}
	return r
}

func (r compareInfoRequest) picks(ctx context.Context, i int) []compareInfoPick {
	s := r.sources[i]
	if s.geometry == nil || (!s.alignToA && s.window[1] < s.window[0]) {
		return nil
	}
	makeKey := func(actual, across int32) [2]int32 {
		if r.key.view.mode == 1 {
			return [2]int32{across, actual}
		}
		return [2]int32{actual, across}
	}
	if r.key.view.mode != 2 {
		tr, co, line, err := s.geometry.NumericLineTraceNumbers(r.key.view.mode, r.key.view.line)
		if err != nil {
			return nil
		}
		if s.view != nil {
			tr, co = s.view.traces, s.view.coords
		} else {
			if s.physical[1] < s.physical[0] {
				return nil
			}
			tr, co = filterPhysicalTraceRange(tr, co, s.physical[0], s.physical[1], s.physical[2])
			tr, co = filterLineRange(tr, co, math.Float64frombits(r.key.view.x0), math.Float64frombits(r.key.view.x1))
		}
		out := make([]compareInfoPick, len(tr))
		for j, id := range tr {
			out[j] = compareInfoPick{id, makeKey(line, co[j])}
		}
		return out
	}
	g, b := s.geometry, r.key.bounds
	var out []compareInfoPick
	for j, id := range g.TraceNumbers {
		if j%2048 == 0 && ctx.Err() != nil {
			return nil
		}
		if j >= len(g.RowOfTrace) || j >= len(g.ColOfTrace) {
			break
		}
		ri, ci := int(g.RowOfTrace[j]), int(g.ColOfTrace[j])
		if ri < 0 || ci < 0 || ri >= len(g.InlineValues) || ci >= len(g.CrosslineValues) {
			continue
		}
		il, xl := g.InlineValues[ri], g.CrosslineValues[ci]
		if il >= b.InlineMin && il <= b.InlineMax && xl >= b.CrosslineMin && xl <= b.CrosslineMax {
			out = append(out, compareInfoPick{id, [2]int32{il, xl}})
		}
	}
	return out
}

type compareInfoTimeWindow struct {
	first, last int64
	valid       bool
}

func compareInfoVisibleTime(t segyanalysis.TraceInput) compareInfoTimeWindow {
	if len(t.Samples) == 0 || t.IntervalUS <= 0 || int64(len(t.Samples)-1) > math.MaxInt64/int64(t.IntervalUS) {
		return compareInfoTimeWindow{}
	}
	span := int64(len(t.Samples)-1) * int64(t.IntervalUS)
	if t.StartUS > math.MaxInt64-span {
		return compareInfoTimeWindow{}
	}
	return compareInfoTimeWindow{t.StartUS, t.StartUS + span, true}
}

func compareInfoTrace(f *segy.File, s compareInfoSource, trace int64, aWindow compareInfoTimeWindow) (segyanalysis.TraceInput, error) {
	h, err := f.ReadTraceHeader(trace)
	if err != nil {
		return segyanalysis.TraceInput{}, err
	}
	count, dt := traceAnalysisHeaderAxis(f.Info, h)
	origin, err := f.TraceTimeOriginUS(trace)
	if err != nil {
		return segyanalysis.TraceInput{}, err
	}
	lo, hi := s.window[0], min(s.window[1], count-1)
	if s.alignToA && aWindow.valid {
		var ok bool
		lo, hi, ok = traceAnalysisSampleIndices(origin, aWindow.first, aWindow.last, dt, count)
		if !ok {
			return segyanalysis.TraceInput{}, nil
		}
	}
	if hi < lo {
		return segyanalysis.TraceInput{}, nil
	}
	var values []float64
	if s.view != nil {
		values, err = s.view.read(trace, lo, hi)
	} else {
		values, err = f.ReadTraceWindow(trace, lo, hi)
	}
	return segyanalysis.TraceInput{StartUS: origin + int64(lo)*int64(dt), IntervalUS: dt, Samples: values}, err
}

func compareInfoUnique(picks []compareInfoPick) map[[2]int32]int {
	m := make(map[[2]int32]int, len(picks))
	for i, p := range picks {
		if _, exists := m[p.key]; exists {
			m[p.key] = -1
		} else {
			m[p.key] = i
		}
	}
	return m
}

func computeCompareInfo(ctx context.Context, r compareInfoRequest) (compareInfoResult, error) {
	var files [2]*segy.File
	var picks [2][]compareInfoPick
	var stats [3]compareInfoMoments
	for i, source := range r.sources {
		if source.path == "" || source.geometry == nil {
			continue
		}
		f, err := openPoststackCompareSource(source.path)
		if err != nil {
			return compareInfoResult{}, err
		}
		defer f.Close()
		files[i] = f
		if _, err = viewFilterSourceIdentity(f, source.path, source.identity); err != nil {
			return compareInfoResult{}, err
		}
		picks[i] = r.picks(ctx, i)
	}
	var snr segyanalysis.ReferenceSNRAccumulator
	var coverage int
	first, last := int64(math.MaxInt64), int64(math.MinInt64)
	reason := "没有可验证的共同道／样点"
	switch {
	case r.key.view.b == "":
		reason = "未加载 B"
	case files[1] == nil:
		reason = "B 尚未就绪"
	case !r.geometry:
		reason = "A/B 缺少可核实的共同几何"
	}
	var indexA, indexB map[[2]int32]int
	if r.geometry && len(picks[0]) > 0 && len(picks[1]) > 0 {
		indexA, indexB = compareInfoUnique(picks[0]), compareInfoUnique(picks[1])
	}
	read := func(side int, p compareInfoPick, aWindow compareInfoTimeWindow) (segyanalysis.TraceInput, error) {
		if err := ctx.Err(); err != nil {
			return segyanalysis.TraceInput{}, err
		}
		tr, err := compareInfoTrace(files[side], r.sources[side], p.trace, aWindow)
		for _, v := range tr.Samples {
			stats[side].add(v)
		}
		return tr, err
	}
	usedB := make([]bool, len(picks[1]))
	// B-only traces have no matching A trace. Use the first valid visible A
	// axis for their standalone summary; they still never contribute to SNR.
	var referenceWindow compareInfoTimeWindow
	for i, p := range picks[0] {
		a, err := read(0, p, compareInfoTimeWindow{})
		if err != nil {
			return compareInfoResult{}, fmt.Errorf("A 道 %d：%w", p.trace+1, err)
		}
		aWindow := compareInfoVisibleTime(a)
		if !referenceWindow.valid {
			referenceWindow = aWindow
		}
		j, ok := indexB[p.key]
		if !ok || j < 0 || indexA[p.key] != i {
			continue
		}
		b, err := read(1, picks[1][j], aWindow)
		if err != nil {
			return compareInfoResult{}, fmt.Errorf("B 道 %d：%w", picks[1][j].trace+1, err)
		}
		usedB[j] = true
		a, b, why := segyanalysis.CompareTraceWindows(a, b)
		if why != "" {
			reason = why
			continue
		}
		matched := false
		for n, v := range a.Samples {
			if snr.Add(v, b.Samples[n]) {
				t := a.StartUS + int64(n)*int64(a.IntervalUS)
				first, last, matched = min(first, t), max(last, t), true
				stats[2].add(v - b.Samples[n])
			}
		}
		if matched {
			coverage++
		} else {
			reason = "无共同有限样点"
		}
	}
	for j, p := range picks[1] {
		if !usedB[j] {
			if _, err := read(1, p, referenceWindow); err != nil {
				return compareInfoResult{}, fmt.Errorf("B 道 %d：%w", p.trace+1, err)
			}
		}
	}
	for i, f := range files {
		if f != nil {
			if _, err := viewFilterSourceIdentity(f, r.sources[i].path, r.sources[i].identity); err != nil {
				return compareInfoResult{}, err
			}
		}
	}
	metric := snr.Result()
	caption := referenceSNRLabel + "：" + ReferenceSNRText(metric, reason)
	mode := [...]string{"Inline", "Crossline", "时间切片"}[r.key.view.mode]
	if r.key.view.mode < 2 {
		mode = fmt.Sprintf("%s %d", mode, r.key.view.line)
	} else {
		q := r.key.bounds
		mode = fmt.Sprintf("%s · IL %d–%d / XL %d–%d", mode, q.InlineMin, q.InlineMax, q.CrosslineMin, q.CrosslineMax)
	}
	res := compareInfoResult{caption: caption, rows: compareInfoTable(stats, [3]int{len(picks[0]), len(picks[1]), coverage})}
	var out strings.Builder
	fmt.Fprintf(&out, "统计范围：主剖面当前可见区域 · %s（非全文件）\r\n共同配对：%d 道 · %d 个有效样点", mode, coverage, metric.Pairs)
	if coverage > 0 {
		f := displaycore.FormatCompactNumber
		fmt.Fprintf(&out, " · 实取 %s–%s ms", f(float64(first)/1000), f(float64(last)/1000))
	}
	res.detail = out.String()
	var states []string
	for i, role := range [2]string{"A", "B"} {
		if i == 1 && r.key.view.b == "" {
			states = append(states, "B：未加载")
			continue
		}
		state := "基准（未启用视图滤波）"
		if r.key.view.mode != 2 && r.sources[i].view != nil && r.key.settings[i].Enabled {
			state = r.key.settings[i].Params.Summary()
		}
		if origin := r.sources[i].status; origin != "" {
			state = origin + " · " + state
		}
		states = append(states, role+"："+state)
	}
	res.sides = strings.Join(states, "\r\n")
	out.Reset()
	for i, source := range r.sources {
		if source.meta != "" {
			if out.Len() > 0 {
				out.WriteString("\r\n")
			}
			fmt.Fprintf(&out, "%c · 文件信息\r\n%s\r\n", 'A'+i, strings.ReplaceAll(strings.ReplaceAll(source.meta, "\r\n", "\n"), "\n", "\r\n"))
			res.paths[i] = source.path
		}
	}
	res.files = out.String()
	return res, ctx.Err()
}

func showCompareDataInfo() {
	if d := activeCompareInfo; d != nil {
		showOwnedSettingsWindow(d.h)
		d.refresh()
		return
	}
	const class = "Limage64PoststackDataInfo"
	hi, _, _ := pGetModuleHandleW.Call(0)
	if !compareInfoClass {
		cursor, _, _ := pLoadCursorW.Call(0, IDC_ARROW)
		registerClass(class, compareInfoCallback, COLOR_BTNFACE+1, hi, cursor)
		compareInfoClass = true
	}
	area := algorithmMonitorArea(compareHwnd)
	dpi := displayDPI(compareHwnd)
	w := min(int(area.Right-area.Left), displayPixelDIP(790, dpi))
	h := min(int(area.Bottom-area.Top), displayPixelDIP(560, dpi))
	x, y := int(area.Left)+(int(area.Right-area.Left)-w)/2, int(area.Top)+(int(area.Bottom-area.Top)-h)/2
	window, _, _ := pCreateWindowExW.Call(settingsToolWindowStyle, uintptr(unsafe.Pointer(u16(class))), uintptr(unsafe.Pointer(u16("查看数据信息 — SeisForge Studio"))), WS_OVERLAPPEDWINDOW|WS_CLIPCHILDREN, uintptr(x), uintptr(y), uintptr(w), uintptr(h), compareHwnd, 0, hi, 0)
	if window == 0 {
		return
	}
	d := &compareInfoWindow{h: window, owner: compareHwnd}
	activeCompareInfo = d
	d.heading = createCtrl(window, "STATIC", "", WS_CHILD|WS_VISIBLE, 0, 0, 1, 1, 0)
	d.scope = createCtrl(window, "STATIC", "", WS_CHILD|WS_VISIBLE, 0, 0, 1, 1, 0)
	d.states = createCtrl(window, "STATIC", "", WS_CHILD|WS_VISIBLE, 0, 0, 1, 1, 0)
	d.list = createComparativeStatsList(window, 27004)
	pShowWindow.Call(d.list, SW_SHOW)
	d.fileLabel = createCtrl(window, "STATIC", "文件与道头信息（可选择并复制）", WS_CHILD|WS_VISIBLE, 0, 0, 1, 1, 0)
	d.body = createCtrl(window, "EDIT", "", WS_CHILD|WS_VISIBLE|WS_BORDER|WS_VSCROLL|WS_TABSTOP|ES_READONLY|traceESMultiline|traceESAutoVScroll, 0, 0, 1, 1, 0)
	d.help = createCtrl(window, "BUTTON", "SNR 说明", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 0, 0, 1, 1, compareInfoHelpID)
	d.copyPath = createCtrl(window, "BUTTON", "复制数据路径", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 0, 0, 1, 1, compareInfoCopyID)
	d.close = createCtrl(window, "BUTTON", "关闭", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 0, 0, 1, 1, compareInfoCloseID)
	d.layout()
	pSetTimer.Call(window, compareInfoTimer, 180, 0)
	d.refresh()
	showOwnedSettingsWindow(window)
}

func (d *compareInfoWindow) layout() {
	w, h := clientSize(d.h)
	dpi := displayDPI(d.h)
	scale := func(x float64) int { return displayPixelDIP(x, dpi) }
	if d.fontDPI != dpi {
		font, _, _ := pCreateFontW.Call(uintptr(-scale(18)), 0, 0, 0, 650, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(u16("Microsoft YaHei UI"))))
		if font != 0 {
			pSendMessageW.Call(d.heading, WM_SETFONT, font, 1)
			if d.headingFont != 0 {
				pDeleteObject.Call(d.headingFont)
			}
			d.headingFont, d.fontDPI = font, dpi
		}
	}
	p, inner, footer := scale(12), max(1, w-2*scale(12)), max(scale(218), h-scale(40))
	move := func(ctl uintptr, x, y, width, height int) {
		pMoveWindow.Call(ctl, uintptr(x), uintptr(y), uintptr(max(1, width)), uintptr(max(1, height)), 1)
	}
	move(d.heading, p, p, inner, scale(28))
	move(d.scope, p, p+scale(31), inner, scale(36))
	move(d.states, p, p+scale(70), inner, scale(36))
	tableTop := p + scale(110)
	tableHeight := max(scale(64), min(scale(164), footer-tableTop-scale(76)))
	move(d.list, p, tableTop, inner, tableHeight)
	sizeComparativeStatsColumns(d.list, inner, dpi)
	detailsTop := tableTop + tableHeight + scale(9)
	move(d.fileLabel, p, detailsTop, inner, scale(21))
	move(d.body, p, detailsTop+scale(21), inner, footer-detailsTop-scale(28))
	move(d.help, p, footer, scale(86), scale(28))
	move(d.copyPath, p+scale(94), footer, scale(115), scale(28))
	move(d.close, max(p, w-p-scale(76)), footer, scale(76), scale(28))
}

func (d *compareInfoWindow) show(r compareInfoResult) {
	setText(d.heading, r.caption)
	setText(d.scope, r.detail)
	setText(d.states, r.sides)
	setText(d.body, r.files)
	d.paths = r.paths
	pEnableWindow.Call(d.copyPath, uintptr(boolToInt(r.paths != [2]string{})))
	updateComparativeStatsList(d.list, r.rows)
}

func (d *compareInfoWindow) refresh() {
	if d.owner != compareHwnd {
		pDestroyWindow.Call(d.h)
		return
	}
	k := compareInfoCurrentKey()
	if d.requested && d.key == k {
		return
	}
	if d.cancel != nil {
		d.cancel()
		d.cancel = nil
	}
	compareInfoSerial++
	d.serial, d.key, d.requested = compareInfoSerial, k, true
	compareInfoMailbox.Lock()
	compareInfoMailbox.serial, compareInfoMailbox.ready = d.serial, nil
	compareInfoMailbox.Unlock()
	if k.preparing || k.filterWait {
		d.show(compareInfoResult{caption: referenceSNRLabel + "：等待当前数据就绪…", detail: "等待几何或视图滤波完成，不显示旧范围的统计。"})
		return
	}
	r := captureCompareInfo(k)
	d.show(compareInfoResult{caption: referenceSNRLabel + "：正在统计当前范围…", detail: "按实际几何与采样位置配对。不使用屏幕像素、显示增益或色标裁剪。"})
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	owner, window, serial := d.owner, d.h, d.serial
	go func() {
		result, err := computeCompareInfo(ctx, r)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			result = compareInfoResult{caption: referenceSNRLabel + "：—（未完成）", detail: err.Error()}
		}
		compareInfoMailbox.Lock()
		publish := compareInfoMailbox.serial == serial && ctx.Err() == nil
		if publish {
			compareInfoMailbox.ready = &compareInfoCompletion{owner, window, serial, k, result}
		}
		compareInfoMailbox.Unlock()
		if publish {
			pPostMessageW.Call(window, compareInfoReady, 0, 0)
		}
	}()
}

func compareInfoWndProc(h uintptr, msg uint32, w, l uintptr) uintptr {
	d := activeCompareInfo
	if d != nil && d.h == h {
		switch msg {
		case WM_GETMINMAXINFO:
			if l != 0 {
				m := (*MINMAXINFO)(unsafe.Pointer(l))
				dpi, area := displayDPI(h), algorithmMonitorArea(h)
				m.PtMinTrackSize.X = min(area.Right-area.Left, int32(displayPixelDIP(468, dpi)))
				m.PtMinTrackSize.Y = min(area.Bottom-area.Top, int32(displayPixelDIP(360, dpi)))
			}
			return 0
		case WM_SIZE, 0x02E0: // WM_DPICHANGED
			if msg == 0x02E0 && l != 0 {
				r := (*RECT)(unsafe.Pointer(l))
				pSetWindowPos.Call(h, 0, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), SWP_NOACTIVATE|SWP_NOZORDER)
			}
			d.layout()
			return 0
		case WM_TIMER:
			if w == compareInfoTimer {
				d.refresh()
				return 0
			}
		case compareInfoReady:
			compareInfoMailbox.Lock()
			r := compareInfoMailbox.ready
			compareInfoMailbox.ready = nil
			compareInfoMailbox.Unlock()
			if r != nil && r.window == h && r.owner == compareHwnd && r.serial == d.serial && r.key == compareInfoCurrentKey() {
				d.show(r.result)
			}
			return 0
		case WM_CLOSE:
			pDestroyWindow.Call(h)
			return 0
		case WM_COMMAND:
			switch w & 0xffff {
			case compareInfoCloseID:
				pDestroyWindow.Call(h)
				return 0
			case compareInfoHelpID:
				label, help := "A/B 信噪比说明", referenceSNRFormula+"\r\n\r\n"+referenceSNRHelp
				pMessageBoxW.Call(h, uintptr(unsafe.Pointer(u16(help))), uintptr(unsafe.Pointer(u16(label))), MB_ICONINFORMATION)
				return 0
			case compareInfoCopyID:
				copyPoststackPath(h, strings.Join(nonemptyTraceStatsNotes(d.paths[:]...), "\r\n"))
				return 0
			}
		case WM_DESTROY:
			pKillTimer.Call(h, compareInfoTimer)
			if d.cancel != nil {
				d.cancel()
			}
			if d.headingFont != 0 {
				pDeleteObject.Call(d.headingFont)
			}
			compareInfoMailbox.Lock()
			compareInfoMailbox.serial, compareInfoMailbox.ready = 0, nil
			compareInfoMailbox.Unlock()
			activeCompareInfo = nil
			return 0
		}
	}
	ret, _, _ := pDefWindowProcW.Call(h, uintptr(msg), w, l)
	return ret
}

func closeCompareDataInfo() {
	if d := activeCompareInfo; d != nil {
		pDestroyWindow.Call(d.h)
	}
}
