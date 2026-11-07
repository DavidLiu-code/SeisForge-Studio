//go:build windows

package main

import (
	"fmt"
	"strconv"
	"syscall"
	"unsafe"

	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
)

// The two auxiliary windows deliberately own only native controls and a small
// view of the active prestack state. They never open a Reader or retain
// amplitude buffers; all work is routed back through the existing UI-thread
// commands and generation guards.
const (
	prestackRangeClass = "SeisForgePrestackRange"
	prestackToolsClass = "SeisForgePrestackTools"

	prestackRangeSummaryID = 1
	prestackRangeStartID   = 2
	prestackRangeEndID     = 3
	prestackRangeModeID    = 4
	prestackRangeSampleID  = 5
	prestackRangeTimeID    = 6
	prestackRangeApplyID   = 7
	prestackRangeAllID     = 8
	prestackRangeCloseID   = 9
	prestackRangeStatusID  = 10

	prestackToolsSummaryID   = 1
	prestackToolsGatherID    = 2
	prestackToolsCSV         = 3
	prestackToolsFigure      = 4
	prestackToolsSessionSave = 5
	prestackToolsSessionOpen = 6
	prestackToolsPlan        = 7
	prestackToolsRun         = 8
	prestackToolsEdit        = 9
	prestackToolsReport      = 10
	prestackToolsCompare     = 11
	prestackToolsQC          = 12
	prestackToolsHistory     = 13
	prestackToolsClose       = 14
	prestackToolsStatus      = 15
)

type prestackRangeWindowState struct {
	hwnd, owner                                                         uintptr
	summary, start, end, mode, sample, times, apply, all, close, status uintptr
}

type prestackToolsWindowState struct {
	hwnd, owner                                                  uintptr
	summary                                                      uintptr
	gather, csv, figure, sessionSave, sessionOpen                uintptr
	plan, run, edit, report, compare, qc, history, close, status uintptr
}

var prestackRangeWindow *prestackRangeWindowState
var prestackToolsWindow *prestackToolsWindowState

var prestackRangeClassRegistered, prestackToolsClassRegistered bool

func ensurePrestackAuxClasses() {
	hi, _, _ := pGetModuleHandleW.Call(0)
	cursor, _, _ := pLoadCursorW.Call(0, IDC_ARROW)
	if !prestackRangeClassRegistered {
		registerClass(prestackRangeClass, syscall.NewCallback(prestackRangeWndProc), COLOR_WINDOW+1, hi, cursor)
		prestackRangeClassRegistered = true
	}
	if !prestackToolsClassRegistered {
		registerClass(prestackToolsClass, syscall.NewCallback(prestackToolsWndProc), COLOR_WINDOW+1, hi, cursor)
		prestackToolsClassRegistered = true
	}
	ensurePrestackHistoryClass()
}

func openPrestackRangeWindow() {
	if prestackRangeWindow != nil && prestackRangeWindow.hwnd != 0 {
		showTopLevelWindowRestored(prestackRangeWindow.hwnd)
		syncPrestackRangeWindow()
		return
	}
	ensurePrestackAuxClasses()
	state := &prestackRangeWindowState{owner: prestackHwnd}
	prestackRangeWindow = state
	h, _, _ := pCreateWindowExW.Call(WS_EX_APPWINDOW, uintptr(unsafe.Pointer(u16(prestackRangeClass))), uintptr(unsafe.Pointer(u16("叠前范围设置"))), WS_OVERLAPPEDWINDOW|WS_CLIPCHILDREN|WS_VISIBLE, uintptr(CW_USEDEFAULT), uintptr(CW_USEDEFAULT), 520, 300, prestackHwnd, 0, 0, 0)
	if h == 0 {
		prestackRangeWindow = nil
		return
	}
	state.hwnd = h
	showTopLevelWindowRestored(h)
	syncPrestackRangeWindow()
}

func openPrestackToolsWindow() {
	if prestackToolsWindow != nil && prestackToolsWindow.hwnd != 0 {
		showTopLevelWindowRestored(prestackToolsWindow.hwnd)
		syncPrestackToolsWindow()
		return
	}
	ensurePrestackAuxClasses()
	state := &prestackToolsWindowState{owner: prestackHwnd}
	prestackToolsWindow = state
	h, _, _ := pCreateWindowExW.Call(WS_EX_APPWINDOW, uintptr(unsafe.Pointer(u16(prestackToolsClass))), uintptr(unsafe.Pointer(u16("叠前导出与流程"))), WS_OVERLAPPEDWINDOW|WS_CLIPCHILDREN|WS_VISIBLE, uintptr(CW_USEDEFAULT), uintptr(CW_USEDEFAULT), 520, 410, prestackHwnd, 0, 0, 0)
	if h == 0 {
		prestackToolsWindow = nil
		return
	}
	state.hwnd = h
	showTopLevelWindowRestored(h)
	syncPrestackToolsWindow()
}

func destroyPrestackAuxWindows() {
	if prestackRangeWindow != nil && prestackRangeWindow.hwnd != 0 {
		pDestroyWindow.Call(prestackRangeWindow.hwnd)
	}
	if prestackToolsWindow != nil && prestackToolsWindow.hwnd != 0 {
		pDestroyWindow.Call(prestackToolsWindow.hwnd)
	}
	prestackRangeWindow, prestackToolsWindow = nil, nil
}

func prestackAuxPageLabel() string {
	switch prestackState.page {
	case 0:
		return "Gather"
	case 1:
		return "Geometry"
	case 2:
		return "Mapping"
	case 3:
		return "Compare"
	case 4:
		return "QC"
	default:
		return "叠前"
	}
}

func prestackRangeTotal() int {
	if prestackState.page == 3 {
		if len(prestackState.compareRows) > 0 {
			return len(prestackState.compareRows)
		}
		return len(prestackState.gather.TraceIndices)
	}
	if prestackState.selection.Type == prestackcore.GatherRaw && prestackState.dataset != nil {
		return int(prestackState.dataset.Metadata.TraceCount)
	}
	return len(prestackState.gather.TraceIndices)
}

func syncPrestackRangeWindow() {
	s := prestackRangeWindow
	if s == nil || s.hwnd == 0 {
		return
	}
	total := prestackRangeTotal()
	enabled := prestackState.dataset != nil && (prestackState.page == 0 || prestackState.page == 3) && total > 0
	pEnableWindow.Call(s.start, map[bool]uintptr{true: 1, false: 0}[enabled])
	pEnableWindow.Call(s.end, map[bool]uintptr{true: 1, false: 0}[enabled])
	pEnableWindow.Call(s.mode, map[bool]uintptr{true: 1, false: 0}[enabled])
	pEnableWindow.Call(s.sample, map[bool]uintptr{true: 1, false: 0}[enabled])
	pEnableWindow.Call(s.times, map[bool]uintptr{true: 1, false: 0}[enabled])
	pEnableWindow.Call(s.apply, map[bool]uintptr{true: 1, false: 0}[enabled])
	pEnableWindow.Call(s.all, map[bool]uintptr{true: 1, false: 0}[enabled])
	if !enabled {
		setText(s.summary, prestackAuxPageLabel()+"：暂无可编辑道集范围")
		setText(s.status, "请先加载 SEG-Y 并建立叠前索引。")
		return
	}
	first, last := prestackState.viewFirst, prestackState.viewLast
	if prestackState.page == 3 {
		first, last = prestackState.compareViewFirst, prestackState.compareViewLast
	}
	if last < first || first < 0 {
		first, last = 0, total-1
	}
	first = clampInt(first, 0, total-1)
	last = clampInt(last, first, total-1)
	setText(s.summary, fmt.Sprintf("%s | 当前范围 %d–%d / %d 道", prestackAuxPageLabel(), first+1, last+1, total))
	setText(s.start, strconv.Itoa(first+1))
	setText(s.end, strconv.Itoa(last+1))
	setText(s.sample, strconv.Itoa(prestackState.sampleFirst+1))
	setText(s.times, strconv.Itoa(prestackState.sampleLast+1))
	mode := prestackState.rawSampleModeValue
	if mode < 0 || mode > 2 {
		mode = 0
	}
	pSendMessageW.Call(s.mode, CB_SETCURSEL, uintptr(mode), 0)
	setText(s.status, fmt.Sprintf("样点 %d–%d；可输入道号、样点或毫秒范围。", prestackState.sampleFirst+1, prestackState.sampleLast+1))
}

func applyPrestackRangeWindow() {
	s := prestackRangeWindow
	if s == nil || prestackState.dataset == nil {
		return
	}
	// The existing range functions intentionally consume the main controls so
	// exports, snapshots and Compare continue to see one canonical state.
	setText(prestackUI.rawTraceStart, getText(s.start))
	setText(prestackUI.rawTraceEnd, getText(s.end))
	setText(prestackUI.rawSampleStart, getText(s.sample))
	setText(prestackUI.rawSampleEnd, getText(s.times))
	v, _, _ := pSendMessageW.Call(s.mode, CB_GETCURSEL, 0, 0)
	pSendMessageW.Call(prestackUI.rawSampleMode, CB_SETCURSEL, v, 0)
	if prestackState.page == 3 {
		applyPrestackCompareDisplayRange()
	} else if prestackState.selection.Type == prestackcore.GatherRaw {
		applyPrestackRawRange()
	} else if prestackState.page == 0 {
		applyPrestackGatherDisplayRange()
	}
	syncPrestackRangeWindow()
	syncPrestackToolsWindow()
}

func applyPrestackRangeWindowAll() {
	if prestackState.page == 3 {
		setPrestackCompareRangeControlDefaults()
	} else if prestackState.selection.Type == prestackcore.GatherRaw {
		setRawRangeControlDefaults()
	} else if prestackState.page == 0 {
		setPrestackGatherRangeControlDefaults()
	}
	syncPrestackRangeWindow()
	applyPrestackRangeWindow()
}

func syncPrestackToolsWindow() {
	s := prestackToolsWindow
	if s == nil || s.hwnd == 0 {
		return
	}
	setText(s.summary, fmt.Sprintf("当前页面：%s | %s", prestackAuxPageLabel(), prestackSelectionKeyLabel(prestackState.selection)))
	for _, c := range []uintptr{s.gather, s.csv, s.figure, s.sessionSave, s.sessionOpen, s.plan, s.run, s.edit, s.report, s.compare, s.qc, s.history} {
		pShowWindow.Call(c, SW_HIDE)
	}
	pEnableWindow.Call(s.gather, map[bool]uintptr{true: 1, false: 0}[prestackState.dataset != nil && prestackState.index != nil])
	pEnableWindow.Call(s.csv, map[bool]uintptr{true: 1, false: 0}[prestackState.dataset != nil && prestackState.index != nil])
	pEnableWindow.Call(s.compare, map[bool]uintptr{true: 1, false: 0}[prestackState.compareBDataset != nil && prestackState.compareBIndex != nil])
	show := func(c uintptr) { pShowWindow.Call(c, SW_SHOWNA) }
	switch prestackState.page {
	case 0:
		for _, c := range []uintptr{s.gather, s.csv, s.figure, s.sessionSave, s.sessionOpen, s.plan, s.run, s.edit, s.report, s.history} {
			show(c)
		}
	case 3:
		for _, c := range []uintptr{s.compare, s.figure, s.sessionSave, s.sessionOpen, s.plan, s.run, s.edit, s.report, s.history} {
			show(c)
		}
	case 4:
		show(s.qc)
		show(s.history)
	}
	if prestackState.compareBDataset != nil {
		setText(s.status, fmt.Sprintf("B：已加载；差 %s。", map[bool]string{true: "显示", false: "关闭"}[prestackCompareDeltaVisible(&prestackState)]))
	} else {
		setText(s.status, "B：未加载；差按钮将在 B 索引完成后显示。")
	}
}

func prestackAuxLayoutRange(s *prestackRangeWindowState, width, height int) {
	if s == nil {
		return
	}
	pMoveWindow.Call(s.summary, 14, 12, uintptr(maxInt(1, width-28)), 24, 1)
	pMoveWindow.Call(s.start, 86, 52, 90, 24, 1)
	pMoveWindow.Call(s.end, 268, 52, 90, 24, 1)
	pMoveWindow.Call(s.mode, 86, 88, 160, 24, 1)
	pMoveWindow.Call(s.sample, 86, 124, 90, 24, 1)
	pMoveWindow.Call(s.times, 268, 124, 90, 24, 1)
	pMoveWindow.Call(s.apply, 14, 166, 100, 27, 1)
	pMoveWindow.Call(s.all, 122, 166, 100, 27, 1)
	pMoveWindow.Call(s.close, uintptr(maxInt(230, width-110)), 166, 90, 27, 1)
	pMoveWindow.Call(s.status, 14, uintptr(maxInt(202, height-42)), uintptr(maxInt(1, width-28)), 24, 1)
}

func prestackAuxLayoutTools(s *prestackToolsWindowState, width, height int) {
	if s == nil {
		return
	}
	pMoveWindow.Call(s.summary, 14, 12, uintptr(maxInt(1, width-28)), 24, 1)
	buttons := []uintptr{s.gather, s.csv, s.figure, s.sessionSave, s.sessionOpen, s.plan, s.run, s.edit, s.report, s.compare, s.qc, s.history}
	for i, c := range buttons {
		col, row := i%2, i/2
		pMoveWindow.Call(c, uintptr(14+col*220), uintptr(52+row*34), 200, 27, 1)
	}
	pMoveWindow.Call(s.close, uintptr(maxInt(230, width-110)), uintptr(maxInt(52, height-78)), 90, 27, 1)
	pMoveWindow.Call(s.status, 14, uintptr(maxInt(52, height-42)), uintptr(maxInt(1, width-28)), 24, 1)
}

func prestackRangeWndProc(h uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	s := prestackRangeWindow
	switch msg {
	case WM_CREATE:
		if s == nil {
			return 0
		}
		s.hwnd = h
		s.summary = createCtrl(h, "STATIC", "", WS_CHILD|WS_VISIBLE, 14, 12, 480, 24, prestackRangeSummaryID)
		createCtrl(h, "STATIC", "道起", WS_CHILD|WS_VISIBLE, 14, 55, 60, 22, 0)
		s.start = createCtrl(h, "EDIT", "1", WS_CHILD|WS_VISIBLE|WS_BORDER|WS_TABSTOP|ES_AUTOHSCROLL, 86, 52, 90, 24, prestackRangeStartID)
		createCtrl(h, "STATIC", "道止", WS_CHILD|WS_VISIBLE, 196, 55, 60, 22, 0)
		s.end = createCtrl(h, "EDIT", "1", WS_CHILD|WS_VISIBLE|WS_BORDER|WS_TABSTOP|ES_AUTOHSCROLL, 268, 52, 90, 24, prestackRangeEndID)
		s.mode = createCtrl(h, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 86, 88, 160, 110, prestackRangeModeID)
		for _, label := range []string{"完整记录", "样点范围", "毫秒范围"} {
			pSendMessageW.Call(s.mode, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(label))))
		}
		pSendMessageW.Call(s.mode, CB_SETCURSEL, 0, 0)
		createCtrl(h, "STATIC", "时间模式", WS_CHILD|WS_VISIBLE, 14, 91, 60, 22, 0)
		createCtrl(h, "STATIC", "时间起", WS_CHILD|WS_VISIBLE, 14, 127, 60, 22, 0)
		s.sample = createCtrl(h, "EDIT", "1", WS_CHILD|WS_VISIBLE|WS_BORDER|WS_TABSTOP|ES_AUTOHSCROLL, 86, 124, 90, 24, prestackRangeSampleID)
		createCtrl(h, "STATIC", "时间止", WS_CHILD|WS_VISIBLE, 196, 127, 60, 22, 0)
		s.times = createCtrl(h, "EDIT", "1", WS_CHILD|WS_VISIBLE|WS_BORDER|WS_TABSTOP|ES_AUTOHSCROLL, 268, 124, 90, 24, prestackRangeTimeID)
		s.apply = createCtrl(h, "BUTTON", "应用范围", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 14, 166, 100, 27, prestackRangeApplyID)
		s.all = createCtrl(h, "BUTTON", "全部范围", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 122, 166, 100, 27, prestackRangeAllID)
		s.close = createCtrl(h, "BUTTON", "关闭", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 410, 166, 90, 27, prestackRangeCloseID)
		s.status = createCtrl(h, "STATIC", "", WS_CHILD|WS_VISIBLE|WS_BORDER, 14, 202, 480, 24, prestackRangeStatusID)
		return 0
	case WM_SIZE:
		prestackAuxLayoutRange(s, int(uint16(lParam)), int(uint16(lParam>>16)))
		return 0
	case WM_GETMINMAXINFO:
		if lParam != 0 {
			m := (*MINMAXINFO)(unsafe.Pointer(lParam))
			m.PtMinTrackSize.X, m.PtMinTrackSize.Y = 430, 250
		}
		return 0
	case WM_COMMAND:
		switch int(wParam & 0xffff) {
		case prestackRangeApplyID:
			applyPrestackRangeWindow()
		case prestackRangeAllID:
			applyPrestackRangeWindowAll()
		case prestackRangeCloseID:
			pDestroyWindow.Call(h)
		}
		return 0
	case WM_CLOSE:
		pDestroyWindow.Call(h)
		return 0
	case WM_DESTROY:
		if prestackRangeWindow != nil && prestackRangeWindow.hwnd == h {
			prestackRangeWindow = nil
		}
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(h, uintptr(msg), wParam, lParam)
	return r
}

func prestackToolsWndProc(h uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	s := prestackToolsWindow
	switch msg {
	case WM_CREATE:
		if s == nil {
			return 0
		}
		s.hwnd = h
		s.summary = createCtrl(h, "STATIC", "", WS_CHILD|WS_VISIBLE, 14, 12, 480, 24, prestackToolsSummaryID)
		newButton := func(text string, id int) uintptr {
			return createCtrl(h, "BUTTON", text, WS_CHILD|WS_TABSTOP|BS_PUSHBUTTON, 14, 52, 200, 27, id)
		}
		s.gather = newButton("导出当前道集", prestackToolsGatherID)
		s.csv = newButton("导出道集清单", prestackToolsCSV)
		s.figure = newButton("生成绘图代码", prestackToolsFigure)
		s.sessionSave = newButton("保存流程", prestackToolsSessionSave)
		s.sessionOpen = newButton("打开流程", prestackToolsSessionOpen)
		s.plan = newButton("保存批处理计划", prestackToolsPlan)
		s.run = newButton("执行批处理计划", prestackToolsRun)
		s.edit = newButton("编辑批处理计划", prestackToolsEdit)
		s.report = newButton("查看执行报告", prestackToolsReport)
		s.compare = newButton("导出匹配报告", prestackToolsCompare)
		s.qc = newButton("导出 QC 报告", prestackToolsQC)
		s.history = newButton("查看批处理历史", prestackToolsHistory)
		s.close = createCtrl(h, "BUTTON", "关闭", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 410, 332, 90, 27, prestackToolsClose)
		s.status = createCtrl(h, "STATIC", "", WS_CHILD|WS_VISIBLE|WS_BORDER, 14, 366, 480, 24, prestackToolsStatus)
		return 0
	case WM_SIZE:
		prestackAuxLayoutTools(s, int(uint16(lParam)), int(uint16(lParam>>16)))
		return 0
	case WM_GETMINMAXINFO:
		if lParam != 0 {
			m := (*MINMAXINFO)(unsafe.Pointer(lParam))
			m.PtMinTrackSize.X, m.PtMinTrackSize.Y = 460, 300
		}
		return 0
	case WM_COMMAND:
		if s == nil {
			return 0
		}
		switch int(wParam & 0xffff) {
		case prestackToolsGatherID:
			exportPrestackGatherSEGY()
		case prestackToolsCSV:
			exportPrestackGatherCSV()
		case prestackToolsFigure:
			exportPrestackFigure()
		case prestackToolsSessionSave:
			savePrestackSessionSnapshot()
		case prestackToolsSessionOpen:
			openPrestackSessionSnapshot()
		case prestackToolsPlan:
			savePrestackBatchPlan()
		case prestackToolsRun:
			runPrestackBatchPlanFromDialog()
		case prestackToolsEdit:
			openPrestackBatchEditor()
		case prestackToolsReport:
			openPrestackExecutionReportViewer()
		case prestackToolsCompare:
			exportPrestackCompareReport()
		case prestackToolsQC:
			exportPrestackQC()
		case prestackToolsHistory:
			openPrestackExecutionHistoryViewer()
		case prestackToolsClose:
			pDestroyWindow.Call(h)
		}
		syncPrestackToolsWindow()
		return 0
	case WM_CLOSE:
		pDestroyWindow.Call(h)
		return 0
	case WM_DESTROY:
		if prestackToolsWindow != nil && prestackToolsWindow.hwnd == h {
			prestackToolsWindow = nil
		}
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(h, uintptr(msg), wParam, lParam)
	return r
}
