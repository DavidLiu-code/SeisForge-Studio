//go:build windows

package main

import (
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segyanalysis"
)

const (
	IDTRACE_SOURCE = 7201 + iota
	IDTRACE_NUMBER
	IDTRACE_PREV
	IDTRACE_NEXT
	IDTRACE_GOTO
	IDTRACE_WINDOW
	IDTRACE_SCALE
	IDTRACE_COPY
	IDTRACE_CLOSE
	IDTRACE_TABS
	IDTRACE_TEXT_HEADER
	IDTRACE_ENCODING
	IDTRACE_HEADER_SUBTABS
	IDTRACE_BINARY_FILTER
	IDTRACE_BINARY_HEX
	IDTRACE_BINARY_FIELDS
	IDTRACE_TRACE_FILTER
	IDTRACE_TRACE_HEX
	IDTRACE_TRACE_FIELDS

	WM_TRACE_ANALYSIS_READY = WM_USER + 520

	traceESMultiline   = 0x0004
	traceESAutoVScroll = 0x0040
	traceCFUnicodeText = 13
	traceGMEMMoveable  = 0x0002

	traceWMSetRedraw             = 0x000B
	traceLVMDeleteAllItems       = 0x1009
	traceLVMGetNextItem          = 0x100C
	traceLVMEnsureVisible        = 0x1013
	traceLVMGetColumnWidth       = 0x101D
	traceLVMSetColumnWidth       = 0x101E
	traceLVMGetTopIndex          = 0x1027
	traceLVMSetExtendedListStyle = 0x1036
	traceLVMSetItemState         = 0x102B
	traceLVMInsertItemW          = 0x104D
	traceLVMSetItemW             = 0x104C
	traceLVMInsertColumnW        = 0x1061
	traceLVSReport               = 0x0001
	traceLVSSingleSel            = 0x0004
	traceLVSShowSelAlways        = 0x0008
	traceLVSNoSortHeader         = 0x8000
	traceLVSExGridLines          = 0x00000001
	traceLVSExFullRowSelect      = 0x00000020
	traceLVSExDoubleBuffer       = 0x00010000
	traceLVIFText                = 0x0001
	traceLVIFState               = 0x0008
	traceLVISSelected            = 0x0002
	traceLVISFocused             = 0x0001
	traceLVNISelected            = 0x0002
	traceLVCFFormat              = 0x0001
	traceLVCFWidth               = 0x0002
	traceLVCFText                = 0x0004
	traceLVCFSubItem             = 0x0008
	traceLVCFmtLeft              = 0
	traceLVCFmtRight             = 1
	traceAnsiFixedFont           = 11
)

var (
	traceClipboardOpen  = user32.NewProc("OpenClipboard")
	traceClipboardClose = user32.NewProc("CloseClipboard")
	traceClipboardEmpty = user32.NewProc("EmptyClipboard")
	traceClipboardSet   = user32.NewProc("SetClipboardData")
	traceGlobalAlloc    = kernel32.NewProc("GlobalAlloc")
	traceGlobalLock     = kernel32.NewProc("GlobalLock")
	traceGlobalUnlock   = kernel32.NewProc("GlobalUnlock")
	traceGlobalFree     = kernel32.NewProc("GlobalFree")
)

type traceAnalysisControls struct {
	source, number, prev, next, goTo uintptr
	window, scale                    uintptr
	copy, close, tabs                uintptr
	textHeaderLabel, textHeader      uintptr
	encodingLabel, encoding          uintptr
	headerSubTabs, headerSummary     uintptr
	headerText, traceSummary         uintptr
	binaryTable, traceTable          traceHeaderTableControls
}

type traceHeaderLVColumn struct {
	Mask       uint32
	Fmt        int32
	Cx         int32
	PszText    *uint16
	CchTextMax int32
	ISubItem   int32
	IImage     int32
	IOrder     int32
}

type traceHeaderLVItem struct {
	Mask               uint32
	IItem, ISubItem    int32
	State, StateMask   uint32
	PszText            *uint16
	CchTextMax, IImage int32
	LParam             uintptr
	IIndent, IGroupID  int32
	CColumns           uint32
	PuColumns          uintptr
	PiColFmt           uintptr
	IGroupIndex        int32
}

type traceHeaderTableControls struct {
	filterLabel, filter, count, hexToggle uintptr
	list, hexEdit                         uintptr
	fields                                []segy.HeaderField
	raw                                   []byte
	rawBase                               int
	importantKeys                         map[string]struct{}
	rows                                  []traceHeaderTableRow
	selectedKey, topKey                   string
	hexExpanded                           bool
}

type traceAnalysisFileResult struct {
	target       traceAnalysisTarget
	info         segy.Info
	textHeaders  []segy.TextHeader
	binaryHeader segy.BinaryHeader
	traceHeader  segy.TraceHeader
	sampleStart  int
	sampleEnd    int
	samples      []float64
	stats        segyanalysis.TraceStats
	spectrum     segyanalysis.Spectrum
	headerError  string
	waveError    string
	spectrumErr  string
}

type traceAnalysisSeries struct {
	role         string
	sampleStart  int
	dtUS         int
	markerSample int
	samples      []float64
	stats        segyanalysis.TraceStats
	spectrum     segyanalysis.Spectrum
	spectrumErr  string
}

type traceAnalysisResult struct {
	generation int64
	selection  traceAnalysisSelection
	files      []traceAnalysisFileResult
	series     []traceAnalysisSeries
	diffNote   string
}

var (
	traceAnalysisHwnd             uintptr
	traceAnalysisUI               traceAnalysisControls
	traceAnalysisSelectionCurrent traceAnalysisSelection
	traceAnalysisCurrent          *traceAnalysisResult
	traceAnalysisGeneration       int64
	traceAnalysisUseFull          bool
	traceAnalysisFullScale        bool
	traceAnalysisPage             = 2
	traceAnalysisHeaderPage       int
	traceAnalysisFieldFilter      traceHeaderFilter

	traceAnalysisReadyMu sync.Mutex
	traceAnalysisReady   = make(map[int64]*traceAnalysisResult)
)

func cloneTraceAnalysisSelection(selection traceAnalysisSelection) traceAnalysisSelection {
	selection.Targets = append([]traceAnalysisTarget(nil), selection.Targets...)
	return selection
}

// showTraceAnalysisSelection reuses one non-modal analysis window. Every
// refresh uses short-lived independent readers, so the source workspace keeps
// complete ownership of its own SEG-Y handles.
func showTraceAnalysisSelection(selection traceAnalysisSelection) {
	if len(selection.Targets) == 0 {
		return
	}
	traceAnalysisSelectionCurrent = cloneTraceAnalysisSelection(selection)
	if traceAnalysisHwnd == 0 {
		h, _, _ := pCreateWindowExW.Call(WS_EX_APPWINDOW,
			uintptr(unsafe.Pointer(u16("Limage64TraceAnalysis"))),
			uintptr(unsafe.Pointer(u16(APP_NAME+" - SEG-Y 文件分析"))),
			WS_OVERLAPPEDWINDOW|WS_CLIPCHILDREN, uintptr(CW_USEDEFAULT), uintptr(CW_USEDEFAULT),
			1120, 780, 0, 0, 0, 0)
		if h == 0 {
			return
		}
		traceAnalysisHwnd = h
		showTopLevelWindowRestored(h)
	} else {
		showTopLevelWindowRestored(traceAnalysisHwnd)
	}
	populateTraceAnalysisSourceCombo()
	startTraceAnalysisLoad()
}

func createTraceHeaderTable(filterID, hexID, listID int, importantKeys map[string]struct{}) traceHeaderTableControls {
	view := traceHeaderTableControls{importantKeys: importantKeys}
	view.filterLabel = createLabel(traceAnalysisHwnd, "字段", 0, 0, 34, 20)
	view.filter = createCtrl(traceAnalysisHwnd, "COMBOBOX", "", WS_CHILD|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 0, 0, 94, 110, filterID)
	for _, label := range []string{"全部", "关键", "非零"} {
		pSendMessageW.Call(view.filter, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(label))))
	}
	pSendMessageW.Call(view.filter, CB_SETCURSEL, uintptr(traceAnalysisFieldFilter), 0)
	view.count = createCtrl(traceAnalysisHwnd, "STATIC", "显示 0/0 个字段", WS_CHILD, 0, 0, 150, 20, 0)
	view.hexToggle = createCtrl(traceAnalysisHwnd, "BUTTON", "显示原始 Hex", WS_CHILD|WS_TABSTOP|BS_PUSHBUTTON, 0, 0, 94, 23, hexID)
	view.list = createCtrl(traceAnalysisHwnd, "SysListView32", "", WS_CHILD|WS_TABSTOP|WS_BORDER|WS_VSCROLL|WS_HSCROLL|traceLVSReport|traceLVSSingleSel|traceLVSShowSelAlways|traceLVSNoSortHeader, 0, 0, 400, 300, listID)
	extended := uintptr(traceLVSExGridLines | traceLVSExFullRowSelect | traceLVSExDoubleBuffer)
	pSendMessageW.Call(view.list, traceLVMSetExtendedListStyle, extended, extended)
	for index, column := range []struct {
		label string
		width int
		fmt   int32
	}{{"字节位置（1-based）", 122, traceLVCFmtLeft}, {"字段", 230, traceLVCFmtLeft}, {"原始值", 150, traceLVCFmtRight}, {"解释值", 480, traceLVCFmtLeft}} {
		item := traceHeaderLVColumn{Mask: traceLVCFFormat | traceLVCFWidth | traceLVCFText | traceLVCFSubItem,
			Fmt: column.fmt, Cx: int32(column.width), PszText: u16(column.label), ISubItem: int32(index)}
		pSendMessageW.Call(view.list, traceLVMInsertColumnW, uintptr(index), uintptr(unsafe.Pointer(&item)))
	}
	editStyle := uint32(WS_CHILD | WS_BORDER | WS_VSCROLL | WS_HSCROLL | traceESMultiline | traceESAutoVScroll | ES_AUTOHSCROLL | ES_READONLY)
	view.hexEdit = createCtrl(traceAnalysisHwnd, "EDIT", "", editStyle, 0, 0, 400, 160, 0)
	fixedFont, _, _ := pGetStockObject.Call(traceAnsiFixedFont)
	if fixedFont != 0 {
		pSendMessageW.Call(view.hexEdit, WM_SETFONT, fixedFont, 1)
	}
	return view
}

func traceHeaderTablePositionKeys(view *traceHeaderTableControls) (selectedKey, topKey string) {
	if view == nil || view.list == 0 || len(view.rows) == 0 {
		return "", ""
	}
	selected, _, _ := pSendMessageW.Call(view.list, traceLVMGetNextItem, ^uintptr(0), traceLVNISelected)
	if index := int(int32(selected)); index >= 0 && index < len(view.rows) {
		selectedKey = view.rows[index].Key
	}
	top, _, _ := pSendMessageW.Call(view.list, traceLVMGetTopIndex, 0, 0)
	if index := int(top); index >= 0 && index < len(view.rows) {
		topKey = view.rows[index].Key
	}
	return selectedKey, topKey
}

func setTraceHeaderListCell(list uintptr, row, column int, text string) {
	item := traceHeaderLVItem{Mask: traceLVIFText, IItem: int32(row), ISubItem: int32(column), PszText: u16(text)}
	pSendMessageW.Call(list, traceLVMSetItemW, 0, uintptr(unsafe.Pointer(&item)))
}

func rebuildTraceHeaderTable(view *traceHeaderTableControls) {
	if view == nil || view.list == 0 {
		return
	}
	selectedKey, topKey := traceHeaderTablePositionKeys(view)
	if selectedKey != "" {
		view.selectedKey = selectedKey
	}
	if topKey != "" {
		view.topKey = topKey
	}
	rows := buildTraceHeaderTableRows(view.fields, traceAnalysisFieldFilter, view.importantKeys)
	pSendMessageW.Call(view.list, traceWMSetRedraw, 0, 0)
	pSendMessageW.Call(view.list, traceLVMDeleteAllItems, 0, 0)
	for rowIndex, row := range rows {
		item := traceHeaderLVItem{Mask: traceLVIFText, IItem: int32(rowIndex), PszText: u16(row.ByteRange)}
		pSendMessageW.Call(view.list, traceLVMInsertItemW, 0, uintptr(unsafe.Pointer(&item)))
		setTraceHeaderListCell(view.list, rowIndex, 1, row.Name)
		setTraceHeaderListCell(view.list, rowIndex, 2, row.Raw)
		setTraceHeaderListCell(view.list, rowIndex, 3, row.Explained)
	}
	view.rows = rows
	setText(view.count, fmt.Sprintf("显示 %d/%d 个字段", len(rows), len(view.fields)))
	selectedIndex, topIndex := -1, -1
	for index, row := range rows {
		if row.Key == view.selectedKey {
			selectedIndex = index
		}
		if row.Key == view.topKey {
			topIndex = index
		}
	}
	if selectedIndex < 0 && len(rows) > 0 {
		selectedIndex = 0
	}
	if topIndex < 0 && len(rows) > 0 {
		topIndex = 0
	}
	if topIndex >= 0 && len(rows) > 0 {
		pSendMessageW.Call(view.list, traceLVMEnsureVisible, uintptr(len(rows)-1), 0)
		pSendMessageW.Call(view.list, traceLVMEnsureVisible, uintptr(topIndex), 0)
	}
	if selectedIndex >= 0 {
		item := traceHeaderLVItem{Mask: traceLVIFState, IItem: int32(selectedIndex), State: traceLVISSelected | traceLVISFocused, StateMask: traceLVISSelected | traceLVISFocused}
		pSendMessageW.Call(view.list, traceLVMSetItemState, uintptr(selectedIndex), uintptr(unsafe.Pointer(&item)))
		view.selectedKey = rows[selectedIndex].Key
	}
	if topIndex >= 0 {
		view.topKey = rows[topIndex].Key
	}
	pSendMessageW.Call(view.list, traceWMSetRedraw, 1, 0)
	pInvalidateRect.Call(view.list, 0, 1)
}

func setTraceHeaderTableData(view *traceHeaderTableControls, fields []segy.HeaderField, raw []byte, rawBase int) {
	if view == nil {
		return
	}
	view.fields = fields
	view.raw = raw
	view.rawBase = rawBase
	setText(view.hexEdit, rawHexText(raw, rawBase))
	rebuildTraceHeaderTable(view)
}

func setTraceHeaderTableVisible(view *traceHeaderTableControls, visible bool) {
	if view == nil {
		return
	}
	for _, control := range []uintptr{view.filterLabel, view.filter, view.count, view.hexToggle, view.list} {
		if control != 0 {
			if visible {
				pShowWindow.Call(control, SW_SHOW)
			} else {
				pShowWindow.Call(control, SW_HIDE)
			}
		}
	}
	if view.hexEdit != 0 {
		if visible && view.hexExpanded {
			pShowWindow.Call(view.hexEdit, SW_SHOW)
		} else {
			pShowWindow.Call(view.hexEdit, SW_HIDE)
		}
	}
}

func layoutTraceHeaderTable(view *traceHeaderTableControls, left, top, width, height int) {
	if view == nil || view.list == 0 {
		return
	}
	width, height = maxInt(width, 200), maxInt(height, 100)
	pMoveWindow.Call(view.filterLabel, uintptr(left), uintptr(top+3), 34, 20, 1)
	pMoveWindow.Call(view.filter, uintptr(left+38), uintptr(top), 96, 110, 1)
	pMoveWindow.Call(view.count, uintptr(left+145), uintptr(top+3), uintptr(maxInt(width-260, 80)), 20, 1)
	pMoveWindow.Call(view.hexToggle, uintptr(left+width-104), uintptr(top), 104, 23, 1)
	listTop := top + 29
	listHeight := height - 29
	hexTop, hexHeight := 0, 0
	if view.hexExpanded {
		hexHeight = clampInt(height/3, 130, 185)
		listHeight = maxInt(80, height-29-hexHeight-6)
		hexTop = listTop + listHeight + 6
	}
	pMoveWindow.Call(view.list, uintptr(left), uintptr(listTop), uintptr(width), uintptr(maxInt(listHeight, 80)), 1)
	if view.hexExpanded {
		pMoveWindow.Call(view.hexEdit, uintptr(left), uintptr(hexTop), uintptr(width), uintptr(hexHeight), 1)
	}
	usedWidth := 0
	for column := 0; column < 3; column++ {
		value, _, _ := pSendMessageW.Call(view.list, traceLVMGetColumnWidth, uintptr(column), 0)
		usedWidth += int(value)
	}
	explainedWidth := maxInt(width-usedWidth-5, 240)
	pSendMessageW.Call(view.list, traceLVMSetColumnWidth, 3, uintptr(explainedWidth))
}

func toggleTraceHeaderHex(view *traceHeaderTableControls) {
	if view == nil {
		return
	}
	view.hexExpanded = !view.hexExpanded
	if view.hexExpanded {
		setText(view.hexToggle, "隐藏原始 Hex")
	} else {
		setText(view.hexToggle, "显示原始 Hex")
	}
	updateTraceAnalysisPageVisibility()
	layoutTraceAnalysis()
}

func setTraceAnalysisFieldFilter(filter traceHeaderFilter) {
	traceAnalysisFieldFilter = filter
	for _, view := range []*traceHeaderTableControls{&traceAnalysisUI.binaryTable, &traceAnalysisUI.traceTable} {
		if view.filter != 0 {
			pSendMessageW.Call(view.filter, CB_SETCURSEL, uintptr(filter), 0)
		}
		rebuildTraceHeaderTable(view)
	}
}

func createTraceAnalysisUI() {
	traceAnalysisUI = traceAnalysisControls{}
	createLabel(traceAnalysisHwnd, "来源", 10, 11, 34, 20)
	traceAnalysisUI.source = createCtrl(traceAnalysisHwnd, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 45, 7, 150, 180, IDTRACE_SOURCE)
	createLabel(traceAnalysisHwnd, "道号", 204, 11, 34, 20)
	traceAnalysisUI.number = createCtrl(traceAnalysisHwnd, "EDIT", "1", WS_CHILD|WS_VISIBLE|WS_TABSTOP|WS_BORDER|ES_AUTOHSCROLL, 240, 7, 72, 23, IDTRACE_NUMBER)
	traceAnalysisUI.prev = createCtrl(traceAnalysisHwnd, "BUTTON", "上一道", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 318, 7, 58, 23, IDTRACE_PREV)
	traceAnalysisUI.next = createCtrl(traceAnalysisHwnd, "BUTTON", "下一道", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 379, 7, 58, 23, IDTRACE_NEXT)
	traceAnalysisUI.goTo = createCtrl(traceAnalysisHwnd, "BUTTON", "转到", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 440, 7, 46, 23, IDTRACE_GOTO)
	createLabel(traceAnalysisHwnd, "时间窗", 498, 11, 46, 20)
	traceAnalysisUI.window = createCtrl(traceAnalysisHwnd, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 548, 7, 102, 100, IDTRACE_WINDOW)
	for _, label := range []string{"当前可见", "完整道"} {
		pSendMessageW.Call(traceAnalysisUI.window, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(label))))
	}
	pSendMessageW.Call(traceAnalysisUI.window, CB_SETCURSEL, 0, 0)
	createLabel(traceAnalysisHwnd, "振幅", 660, 11, 34, 20)
	traceAnalysisUI.scale = createCtrl(traceAnalysisHwnd, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 697, 7, 104, 100, IDTRACE_SCALE)
	for _, label := range []string{"99% 自适应", "全振幅"} {
		pSendMessageW.Call(traceAnalysisUI.scale, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(label))))
	}
	pSendMessageW.Call(traceAnalysisUI.scale, CB_SETCURSEL, 0, 0)
	traceAnalysisUI.copy = createCtrl(traceAnalysisHwnd, "BUTTON", "复制当前页", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 813, 7, 86, 23, IDTRACE_COPY)
	traceAnalysisUI.close = createCtrl(traceAnalysisHwnd, "BUTTON", "关闭", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 904, 7, 56, 23, IDTRACE_CLOSE)

	traceAnalysisUI.tabs = createCtrl(traceAnalysisHwnd, "SysTabControl32", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP, 8, 38, 1090, 28, IDTRACE_TABS)
	for index, label := range []string{"卷头", "道头", "单道分析"} {
		item := TCITEM{Mask: TCIF_TEXT, PszText: u16(label)}
		pSendMessageW.Call(traceAnalysisUI.tabs, TCM_INSERTITEMW, uintptr(index), uintptr(unsafe.Pointer(&item)))
	}
	pSendMessageW.Call(traceAnalysisUI.tabs, TCM_FIRST+12, uintptr(traceAnalysisPage), 0) // TCM_SETCURSEL

	traceAnalysisUI.textHeaderLabel = createLabel(traceAnalysisHwnd, "文本头", 12, 75, 44, 20)
	traceAnalysisUI.textHeader = createCtrl(traceAnalysisHwnd, "COMBOBOX", "", WS_CHILD|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 58, 71, 172, 160, IDTRACE_TEXT_HEADER)
	traceAnalysisUI.encodingLabel = createLabel(traceAnalysisHwnd, "编码", 242, 75, 34, 20)
	traceAnalysisUI.encoding = createCtrl(traceAnalysisHwnd, "COMBOBOX", "", WS_CHILD|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 279, 71, 148, 120, IDTRACE_ENCODING)
	for _, label := range []string{"自动识别", "ASCII", "EBCDIC CP037"} {
		pSendMessageW.Call(traceAnalysisUI.encoding, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(label))))
	}
	pSendMessageW.Call(traceAnalysisUI.encoding, CB_SETCURSEL, 0, 0)
	editStyle := uint32(WS_CHILD | WS_BORDER | WS_VSCROLL | WS_HSCROLL | traceESMultiline | traceESAutoVScroll | ES_AUTOHSCROLL | ES_READONLY)
	traceAnalysisUI.headerSummary = createCtrl(traceAnalysisHwnd, "EDIT", "正在读取文件级卷头...", WS_CHILD|WS_BORDER|WS_VSCROLL|traceESMultiline|traceESAutoVScroll|ES_READONLY, 8, 71, 1090, 70, 0)
	traceAnalysisUI.headerSubTabs = createCtrl(traceAnalysisHwnd, "SysTabControl32", "", WS_CHILD|WS_TABSTOP, 8, 145, 1090, 28, IDTRACE_HEADER_SUBTABS)
	for index, label := range []string{"文本卷头", "二进制卷头"} {
		item := TCITEM{Mask: TCIF_TEXT, PszText: u16(label)}
		pSendMessageW.Call(traceAnalysisUI.headerSubTabs, TCM_INSERTITEMW, uintptr(index), uintptr(unsafe.Pointer(&item)))
	}
	pSendMessageW.Call(traceAnalysisUI.headerSubTabs, TCM_FIRST+12, uintptr(traceAnalysisHeaderPage), 0)
	traceAnalysisUI.headerText = createCtrl(traceAnalysisHwnd, "EDIT", "正在读取文本卷头...", editStyle, 8, 204, 1090, 517, 0)
	fixedFont, _, _ := pGetStockObject.Call(traceAnsiFixedFont)
	if fixedFont != 0 {
		pSendMessageW.Call(traceAnalysisUI.headerText, WM_SETFONT, fixedFont, 1)
	}
	traceAnalysisUI.binaryTable = createTraceHeaderTable(IDTRACE_BINARY_FILTER, IDTRACE_BINARY_HEX, IDTRACE_BINARY_FIELDS, binaryHeaderImportantKeys)
	traceAnalysisUI.traceSummary = createCtrl(traceAnalysisHwnd, "EDIT", "正在读取道头...", WS_CHILD|WS_BORDER|WS_VSCROLL|traceESMultiline|traceESAutoVScroll|ES_READONLY, 8, 71, 1090, 94, 0)
	traceAnalysisUI.traceTable = createTraceHeaderTable(IDTRACE_TRACE_FILTER, IDTRACE_TRACE_HEX, IDTRACE_TRACE_FIELDS, traceHeaderImportantKeys)
	updateTraceAnalysisPageVisibility()
}

func populateTraceAnalysisSourceCombo() {
	if traceAnalysisUI.source == 0 {
		return
	}
	previous, _, _ := pSendMessageW.Call(traceAnalysisUI.source, CB_GETCURSEL, 0, 0)
	pSendMessageW.Call(traceAnalysisUI.source, 0x014B, 0, 0) // CB_RESETCONTENT
	for _, target := range traceAnalysisSelectionCurrent.Targets {
		label := target.Role
		if label == "" {
			label = "数据"
		}
		label += "  " + filepath.Base(target.Path)
		pSendMessageW.Call(traceAnalysisUI.source, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(label))))
	}
	selected := int(previous)
	if selected < 0 || selected >= len(traceAnalysisSelectionCurrent.Targets) {
		selected = 0
	}
	pSendMessageW.Call(traceAnalysisUI.source, CB_SETCURSEL, uintptr(selected), 0)
	setText(traceAnalysisUI.number, strconv.FormatInt(traceAnalysisSelectionCurrent.Targets[selected].Trace+1, 10))
}

func selectedTraceAnalysisIndex() int {
	if len(traceAnalysisSelectionCurrent.Targets) == 0 {
		return -1
	}
	selected, _, _ := pSendMessageW.Call(traceAnalysisUI.source, CB_GETCURSEL, 0, 0)
	index := int(selected)
	if index < 0 || index >= len(traceAnalysisSelectionCurrent.Targets) {
		return 0
	}
	return index
}

func startTraceAnalysisLoad() {
	if traceAnalysisHwnd == 0 || len(traceAnalysisSelectionCurrent.Targets) == 0 {
		return
	}
	generation := atomic.AddInt64(&traceAnalysisGeneration, 1)
	selection := cloneTraceAnalysisSelection(traceAnalysisSelectionCurrent)
	useFull := traceAnalysisUseFull
	traceAnalysisCurrent = nil
	setText(traceAnalysisUI.headerSummary, "正在读取文件级卷头...")
	setText(traceAnalysisUI.headerText, "正在读取文本卷头...")
	setText(traceAnalysisUI.traceSummary, "正在读取所选道头...")
	setTraceHeaderTableData(&traceAnalysisUI.binaryTable, nil, nil, 3201)
	setTraceHeaderTableData(&traceAnalysisUI.traceTable, nil, nil, 1)
	pInvalidateRect.Call(traceAnalysisHwnd, 0, 0)
	go func() {
		result := buildTraceAnalysisResult(generation, selection, useFull)
		traceAnalysisReadyMu.Lock()
		if generation != atomic.LoadInt64(&traceAnalysisGeneration) || traceAnalysisHwnd == 0 {
			traceAnalysisReadyMu.Unlock()
			return
		}
		traceAnalysisReady[generation] = result
		traceAnalysisReadyMu.Unlock()
		h := traceAnalysisHwnd
		if h != 0 {
			pPostMessageW.Call(h, WM_TRACE_ANALYSIS_READY, uintptr(generation), 0)
		}
	}()
}

func buildTraceAnalysisResult(generation int64, selection traceAnalysisSelection, useFull bool) *traceAnalysisResult {
	result := &traceAnalysisResult{generation: generation, selection: selection, files: make([]traceAnalysisFileResult, 0, len(selection.Targets))}
	for _, target := range selection.Targets {
		fileResult := readTraceAnalysisTarget(target, useFull)
		result.files = append(result.files, fileResult)
		if len(fileResult.samples) > 0 {
			result.series = append(result.series, traceAnalysisSeries{role: target.Role, sampleStart: fileResult.sampleStart,
				dtUS: fileResult.info.SampleIntervalUS, markerSample: target.MarkerSample, samples: fileResult.samples,
				stats: fileResult.stats, spectrum: fileResult.spectrum, spectrumErr: fileResult.spectrumErr})
		}
	}
	if selection.Difference && len(result.files) >= 2 {
		a, b := result.files[0], result.files[1]
		switch {
		case len(a.samples) == 0 || len(b.samples) == 0:
			result.diffNote = "A-B 不可计算：A 或 B 波形不可用"
		case a.info.SampleIntervalUS != b.info.SampleIntervalUS:
			result.diffNote = fmt.Sprintf("A-B 不可计算：采样间隔不一致（%d / %d us）", a.info.SampleIntervalUS, b.info.SampleIntervalUS)
		case a.sampleStart != b.sampleStart || len(a.samples) != len(b.samples):
			result.diffNote = "A-B 不可计算：当前样点窗不一致"
		default:
			difference := make([]float64, len(a.samples))
			for index := range difference {
				difference[index] = a.samples[index] - b.samples[index]
			}
			stats := segyanalysis.ComputeTraceStats(difference)
			spectrum, spectrumErr := segyanalysis.ComputeTraceSpectrum(difference, a.info.SampleIntervalUS)
			note := ""
			if spectrumErr != nil {
				note = spectrumErr.Error()
			}
			result.series = append(result.series, traceAnalysisSeries{role: "A-B", sampleStart: a.sampleStart,
				dtUS: a.info.SampleIntervalUS, markerSample: a.target.MarkerSample, samples: difference,
				stats: stats, spectrum: spectrum, spectrumErr: note})
		}
	}
	return result
}

func readTraceAnalysisTarget(target traceAnalysisTarget, useFull bool) traceAnalysisFileResult {
	result := traceAnalysisFileResult{target: target}
	reader, err := segy.Open(target.Path)
	if err != nil {
		result.headerError, result.waveError = err.Error(), err.Error()
		return result
	}
	defer reader.Close()
	result.info = reader.Info
	result.binaryHeader, err = reader.ReadBinaryHeader()
	if err != nil {
		result.headerError = err.Error()
	}
	if headers, textErr := reader.ReadTextHeaders(); textErr != nil {
		if result.headerError != "" {
			result.headerError += "; "
		}
		result.headerError += textErr.Error()
	} else {
		result.textHeaders = headers
	}
	result.traceHeader, err = reader.ReadTraceHeader(target.Trace)
	if err != nil {
		result.waveError = err.Error()
		return result
	}
	start, end := target.SampleStart, target.SampleEnd
	if useFull {
		start, end = 0, reader.Info.SamplesPerTrace-1
	}
	start = clampInt(start, 0, reader.Info.SamplesPerTrace-1)
	end = clampInt(end, start, reader.Info.SamplesPerTrace-1)
	result.sampleStart, result.sampleEnd = start, end
	if reader.Info.FormatCode == 4 {
		result.waveError = "样点格式码 4（带增益定点数）暂不支持波形解码；卷头和道头仍可查看"
		return result
	}
	result.samples, err = reader.ReadTraceWindow(target.Trace, start, end)
	if err != nil {
		result.waveError = err.Error()
		return result
	}
	result.stats = segyanalysis.ComputeTraceStats(result.samples)
	result.spectrum, err = segyanalysis.ComputeTraceSpectrum(result.samples, reader.Info.SampleIntervalUS)
	if err != nil {
		result.spectrumErr = err.Error()
	}
	return result
}

func handleTraceAnalysisReady(generation int64) {
	traceAnalysisReadyMu.Lock()
	result := traceAnalysisReady[generation]
	delete(traceAnalysisReady, generation)
	traceAnalysisReadyMu.Unlock()
	if result == nil || traceAnalysisHwnd == 0 || generation != atomic.LoadInt64(&traceAnalysisGeneration) {
		return
	}
	traceAnalysisCurrent = result
	rebuildTraceAnalysisTextSelectors()
	rebuildTraceAnalysisReports()
	pInvalidateRect.Call(traceAnalysisHwnd, 0, 0)
}

func rebuildTraceAnalysisTextSelectors() {
	if traceAnalysisUI.textHeader == 0 {
		return
	}
	pSendMessageW.Call(traceAnalysisUI.textHeader, 0x014B, 0, 0)
	file := selectedTraceAnalysisFile()
	if file == nil || len(file.textHeaders) == 0 {
		pSendMessageW.Call(traceAnalysisUI.textHeader, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16("无可用文本卷头"))))
		pSendMessageW.Call(traceAnalysisUI.textHeader, CB_SETCURSEL, 0, 0)
		return
	}
	for index, header := range file.textHeaders {
		label := "主文本卷头"
		if index > 0 || header.Extended {
			label = fmt.Sprintf("扩展文本卷头 %d", index)
		}
		label += "（" + textEncodingLabel(header.Encoding) + "）"
		pSendMessageW.Call(traceAnalysisUI.textHeader, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(label))))
	}
	pSendMessageW.Call(traceAnalysisUI.textHeader, CB_SETCURSEL, 0, 0)
}

func selectedTraceAnalysisFile() *traceAnalysisFileResult {
	if traceAnalysisCurrent == nil || len(traceAnalysisCurrent.files) == 0 {
		return nil
	}
	index := selectedTraceAnalysisIndex()
	if index < 0 || index >= len(traceAnalysisCurrent.files) {
		index = 0
	}
	return &traceAnalysisCurrent.files[index]
}

func textEncodingLabel(encoding segy.TextEncoding) string {
	if encoding == segy.TextEncodingEBCDICCP037 {
		return "EBCDIC CP037"
	}
	return "ASCII"
}

func selectedTextEncoding() segy.TextEncoding {
	selected, _, _ := pSendMessageW.Call(traceAnalysisUI.encoding, CB_GETCURSEL, 0, 0)
	switch int(selected) {
	case 1:
		return segy.TextEncodingASCII
	case 2:
		return segy.TextEncodingEBCDICCP037
	default:
		return segy.TextEncodingUnknown
	}
}

func rawHexText(raw []byte, base int) string {
	var builder strings.Builder
	for offset := 0; offset < len(raw); offset += 16 {
		end := minInt(offset+16, len(raw))
		fmt.Fprintf(&builder, "%04d: ", base+offset)
		for index := offset; index < offset+16; index++ {
			if index < end {
				fmt.Fprintf(&builder, "%02X ", raw[index])
			} else {
				builder.WriteString("   ")
			}
		}
		builder.WriteString(" ")
		for _, value := range raw[offset:end] {
			if value >= 32 && value <= 126 {
				builder.WriteByte(value)
			} else {
				builder.WriteByte('.')
			}
		}
		builder.WriteString("\r\n")
	}
	return builder.String()
}

func traceAnalysisFileSummary(file *traceAnalysisFileResult) string {
	if file == nil {
		return "正在读取文件级卷头..."
	}
	info := file.info
	endian := "大端"
	if info.Endian == segy.Little {
		endian = "小端"
	}
	durationMS := float64(maxInt(info.SamplesPerTrace-1, 0)*info.SampleIntervalUS) / 1000
	nyquist := 0.0
	if info.SampleIntervalUS > 0 {
		nyquist = 1e6 / (2 * float64(info.SampleIntervalUS))
	}
	var summary strings.Builder
	fmt.Fprintf(&summary, "文件：%s\r\n", info.Path)
	fmt.Fprintf(&summary, "大小：%d 字节    道数：%d    样点：%d    间隔：%d us    记录：%.3f ms    Nyquist：%.3f Hz\r\n",
		info.FileSize, info.TraceCount, info.SamplesPerTrace, info.SampleIntervalUS, durationMS, nyquist)
	fmt.Fprintf(&summary, "格式：%d（%s）    端序：%s    SEG-Y：Rev %d.%d    定长：%d    扩展文本头：%d    数据起点：%d",
		info.FormatCode, segy.SampleFormatName(info.FormatCode), endian, file.binaryHeader.RevisionMajor, file.binaryHeader.RevisionMinor,
		file.binaryHeader.FixedLengthTraceFlag, file.binaryHeader.ExtendedTextHeaderCount, info.DataStart)
	warnings := make([]string, 0, len(file.binaryHeader.Warnings)+1)
	if file.headerError != "" {
		warnings = append(warnings, file.headerError)
	}
	warnings = append(warnings, file.binaryHeader.Warnings...)
	if len(warnings) > 0 {
		summary.WriteString("\r\n警告：" + strings.Join(warnings, "；"))
	}
	return summary.String()
}

func traceAnalysisDecodedTextHeader(file *traceAnalysisFileResult) string {
	if file == nil || len(file.textHeaders) == 0 {
		return "无可用文本卷头"
	}
	selected, _, _ := pSendMessageW.Call(traceAnalysisUI.textHeader, CB_GETCURSEL, 0, 0)
	index := clampInt(int(selected), 0, len(file.textHeaders)-1)
	textHeader := file.textHeaders[index]
	encoding := selectedTextEncoding()
	if encoding == segy.TextEncodingUnknown {
		encoding = textHeader.Encoding
	}
	text, _, err := textHeader.Decode(encoding)
	if err != nil {
		return "解码失败：" + err.Error()
	}
	return "编码：" + textEncodingLabel(encoding) + "\r\n" + text
}

func refreshTraceAnalysisDecodedTextHeader() {
	setText(traceAnalysisUI.headerText, traceAnalysisDecodedTextHeader(selectedTraceAnalysisFile()))
}

func traceAnalysisTraceSummary(file *traceAnalysisFileResult) string {
	if file == nil {
		return "正在读取所选道头..."
	}
	info, header := file.info, file.traceHeader
	var summary strings.Builder
	fmt.Fprintf(&summary, "来源：%s    文件道号：%d（1-based）", file.target.Role, file.target.Trace+1)
	inline, crossline := header.Inline, header.Crossline
	if inline == 0 && crossline == 0 && file.target.HasGeometry {
		inline, crossline = file.target.Inline, file.target.Crossline
	}
	if inline != 0 || crossline != 0 {
		fmt.Fprintf(&summary, "    当前几何：Inline %d / Crossline %d", inline, crossline)
	}
	fmt.Fprintf(&summary, "\r\nTrace序号：%d    FFID：%d    CDP：%d    Offset：%d",
		header.TraceSequenceFile, header.FieldRecord, header.CDP, header.Offset)
	fmt.Fprintf(&summary, "\r\nCDP X/Y：%.6g / %.6g    Source X/Y：%.6g / %.6g    Group X/Y：%.6g / %.6g",
		header.CDPX, header.CDPY, header.SourceX, header.SourceY, header.GroupX, header.GroupY)
	fmt.Fprintf(&summary, "\r\nDelay：%d ms    道内样点/间隔：%d / %d us    Shotpoint：%.6g",
		header.DelayMS, header.SampleCount, header.SampleIntervalUS, header.Shotpoint)
	sampleStatus := "样点数一致或道头未填写"
	if header.SampleCount != 0 && header.SampleCount != info.SamplesPerTrace {
		sampleStatus = fmt.Sprintf("样点数不一致（道头 %d / 二进制卷头 %d）", header.SampleCount, info.SamplesPerTrace)
	}
	intervalStatus := "采样间隔一致或道头未填写"
	if header.SampleIntervalUS != 0 && header.SampleIntervalUS != info.SampleIntervalUS {
		intervalStatus = fmt.Sprintf("采样间隔不一致（道头 %d / 二进制卷头 %d us）", header.SampleIntervalUS, info.SampleIntervalUS)
	}
	fmt.Fprintf(&summary, "\r\n一致性：%s；%s", sampleStatus, intervalStatus)
	warnings := append([]string(nil), header.Warnings...)
	if file.waveError != "" {
		warnings = append(warnings, "波形："+file.waveError)
	}
	if len(warnings) > 0 {
		summary.WriteString("\r\n警告：" + strings.Join(warnings, "；"))
	}
	return summary.String()
}

func rebuildTraceAnalysisReports() {
	file := selectedTraceAnalysisFile()
	if file == nil {
		setText(traceAnalysisUI.headerSummary, "正在读取文件级卷头...")
		setText(traceAnalysisUI.headerText, "正在读取文本卷头...")
		setText(traceAnalysisUI.traceSummary, "正在读取所选道头...")
		setTraceHeaderTableData(&traceAnalysisUI.binaryTable, nil, nil, 3201)
		setTraceHeaderTableData(&traceAnalysisUI.traceTable, nil, nil, 1)
		return
	}
	setText(traceAnalysisUI.number, strconv.FormatInt(file.target.Trace+1, 10))
	setText(traceAnalysisUI.headerSummary, traceAnalysisFileSummary(file))
	setText(traceAnalysisUI.headerText, traceAnalysisDecodedTextHeader(file))
	setText(traceAnalysisUI.traceSummary, traceAnalysisTraceSummary(file))
	setTraceHeaderTableData(&traceAnalysisUI.binaryTable, file.binaryHeader.Fields, file.binaryHeader.Raw, 3201)
	setTraceHeaderTableData(&traceAnalysisUI.traceTable, file.traceHeader.Fields, file.traceHeader.Raw, 1)
}

func updateTraceAnalysisPageVisibility() {
	header := traceAnalysisPage == 0
	trace := traceAnalysisPage == 1
	textHeader := header && traceAnalysisHeaderPage == 0
	binaryHeader := header && traceAnalysisHeaderPage == 1
	for _, control := range []uintptr{traceAnalysisUI.headerSummary, traceAnalysisUI.headerSubTabs} {
		if control != 0 {
			if header {
				pShowWindow.Call(control, SW_SHOW)
			} else {
				pShowWindow.Call(control, SW_HIDE)
			}
		}
	}
	for _, control := range []uintptr{traceAnalysisUI.textHeaderLabel, traceAnalysisUI.textHeader, traceAnalysisUI.encodingLabel, traceAnalysisUI.encoding, traceAnalysisUI.headerText} {
		if control != 0 {
			if textHeader {
				pShowWindow.Call(control, SW_SHOW)
			} else {
				pShowWindow.Call(control, SW_HIDE)
			}
		}
	}
	setTraceHeaderTableVisible(&traceAnalysisUI.binaryTable, binaryHeader)
	if traceAnalysisUI.traceSummary != 0 {
		if trace {
			pShowWindow.Call(traceAnalysisUI.traceSummary, SW_SHOW)
		} else {
			pShowWindow.Call(traceAnalysisUI.traceSummary, SW_HIDE)
		}
	}
	setTraceHeaderTableVisible(&traceAnalysisUI.traceTable, trace)
	if traceAnalysisHwnd != 0 {
		pInvalidateRect.Call(traceAnalysisHwnd, 0, 0)
	}
}

func layoutTraceAnalysis() {
	if traceAnalysisHwnd == 0 {
		return
	}
	w, h := clientSize(traceAnalysisHwnd)
	pMoveWindow.Call(traceAnalysisUI.tabs, 8, 38, uintptr(maxInt(w-16, 80)), 28, 1)
	contentWidth := maxInt(w-16, 80)
	pMoveWindow.Call(traceAnalysisUI.headerSummary, 8, 71, uintptr(contentWidth), 76, 1)
	pMoveWindow.Call(traceAnalysisUI.headerSubTabs, 8, 151, uintptr(contentWidth), 28, 1)
	pMoveWindow.Call(traceAnalysisUI.textHeaderLabel, 12, 185, 44, 20, 1)
	pMoveWindow.Call(traceAnalysisUI.textHeader, 58, 181, 172, 160, 1)
	pMoveWindow.Call(traceAnalysisUI.encodingLabel, 242, 185, 34, 20, 1)
	pMoveWindow.Call(traceAnalysisUI.encoding, 279, 181, 148, 120, 1)
	pMoveWindow.Call(traceAnalysisUI.headerText, 8, 211, uintptr(contentWidth), uintptr(maxInt(h-219, 80)), 1)
	layoutTraceHeaderTable(&traceAnalysisUI.binaryTable, 8, 181, contentWidth, maxInt(h-189, 100))
	pMoveWindow.Call(traceAnalysisUI.traceSummary, 8, 71, uintptr(contentWidth), 106, 1)
	layoutTraceHeaderTable(&traceAnalysisUI.traceTable, 8, 183, contentWidth, maxInt(h-191, 100))
}

func traceAnalysisStatsText(series traceAnalysisSeries) string {
	stats := series.stats
	return fmt.Sprintf("%s  samples=%d  finite=%d  zero=%d  NaN/Inf=%d  Min %.6g  Max %.6g  Mean %.6g  RMS %.6g  Std %.6g  |Peak| %.6g",
		series.role, stats.SampleCount, stats.FiniteCount, stats.ZeroCount, stats.NonFiniteCount,
		stats.Min, stats.Max, stats.Mean, stats.RMS, stats.StdDev, stats.PeakAbs)
}

func traceAnalysisHeaderTableCopyText(summary, fieldsTitle, hexTitle string, view *traceHeaderTableControls) string {
	if view == nil {
		return summary
	}
	return summary + "\r\n\r\n===== " + fieldsTitle + " =====\r\n" + traceHeaderRowsTSV(view.rows) +
		"\r\n===== " + hexTitle + " =====\r\n" + rawHexText(view.raw, view.rawBase)
}

func traceAnalysisCopyText() string {
	if traceAnalysisPage == 0 {
		if traceAnalysisHeaderPage == 0 {
			return getText(traceAnalysisUI.headerSummary) + "\r\n\r\n===== 文本卷头 =====\r\n" + getText(traceAnalysisUI.headerText)
		}
		return traceAnalysisHeaderTableCopyText(getText(traceAnalysisUI.headerSummary), "二进制卷头字段", "二进制卷头原始 Hex（400 字节）", &traceAnalysisUI.binaryTable)
	}
	if traceAnalysisPage == 1 {
		return traceAnalysisHeaderTableCopyText(getText(traceAnalysisUI.traceSummary), "道头字段", "道头原始 Hex（240 字节）", &traceAnalysisUI.traceTable)
	}
	if traceAnalysisCurrent == nil {
		return "单道分析尚未完成"
	}
	var builder strings.Builder
	builder.WriteString(traceAnalysisCurrent.selection.Context + "\r\n")
	for _, series := range traceAnalysisCurrent.series {
		builder.WriteString(traceAnalysisStatsText(series) + "\r\n")
		if series.spectrumErr != "" {
			builder.WriteString("频谱不可用：" + series.spectrumErr + "\r\n")
		} else {
			fmt.Fprintf(&builder, "主频 %.6g Hz  Nyquist %.6g Hz  NFFT %d\r\n", series.spectrum.PeakHz, series.spectrum.NyquistHz, series.spectrum.NFFT)
		}
	}
	for _, file := range traceAnalysisCurrent.files {
		if file.waveError != "" {
			builder.WriteString(file.target.Role + " 波形不可用：" + file.waveError + "\r\n")
		}
	}
	if traceAnalysisCurrent.diffNote != "" {
		builder.WriteString(traceAnalysisCurrent.diffNote + "\r\n")
	}
	return builder.String()
}

func copyTraceAnalysisPage() {
	text := traceAnalysisCopyText()
	utf16, err := syscall.UTF16FromString(text)
	if err != nil || len(utf16) == 0 {
		return
	}
	opened, _, _ := traceClipboardOpen.Call(traceAnalysisHwnd)
	if opened == 0 {
		message(traceAnalysisHwnd, "复制当前页", "无法打开剪贴板。", MB_OK|MB_ICONERROR)
		return
	}
	defer traceClipboardClose.Call()
	traceClipboardEmpty.Call()
	hMemory, _, _ := traceGlobalAlloc.Call(traceGMEMMoveable, uintptr(len(utf16)*2))
	if hMemory == 0 {
		return
	}
	pointer, _, _ := traceGlobalLock.Call(hMemory)
	if pointer == 0 {
		traceGlobalFree.Call(hMemory)
		return
	}
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(pointer)), len(utf16)), utf16)
	traceGlobalUnlock.Call(hMemory)
	accepted, _, _ := traceClipboardSet.Call(traceCFUnicodeText, hMemory)
	if accepted == 0 {
		traceGlobalFree.Call(hMemory)
	}
}

func changeTraceAnalysisTrace(delta int64, fromEdit bool) {
	index := selectedTraceAnalysisIndex()
	if index < 0 {
		return
	}
	target := &traceAnalysisSelectionCurrent.Targets[index]
	requested := target.Trace + delta
	if fromEdit {
		value, err := strconv.ParseInt(strings.TrimSpace(getText(traceAnalysisUI.number)), 10, 64)
		if err != nil || value < 1 {
			message(traceAnalysisHwnd, "单道分析", "请输入有效的 1-based 道号。", MB_OK|MB_ICONINFORMATION)
			return
		}
		requested = value - 1
	}
	maxTrace := int64(-1)
	if traceAnalysisCurrent != nil && index < len(traceAnalysisCurrent.files) {
		maxTrace = traceAnalysisCurrent.files[index].info.TraceCount - 1
	}
	if maxTrace >= 0 {
		if requested > maxTrace {
			requested = maxTrace
		}
		requested = maxInt64(0, requested)
	} else if requested < 0 {
		requested = 0
	}
	if requested != target.Trace {
		// Geometry captured from the original 2-D click describes only that
		// trace. After manual trace navigation, prefer the freshly decoded
		// trace-header Inline/Crossline instead of displaying stale click data.
		target.HasGeometry = false
		target.Inline, target.Crossline = 0, 0
	}
	target.Trace = requested
	setText(traceAnalysisUI.number, strconv.FormatInt(requested+1, 10))
	startTraceAnalysisLoad()
}

func traceSeriesColor(index int, role string) uintptr {
	switch role {
	case "B":
		return rgbRef(220, 55, 45)
	case "A-B":
		return rgbRef(20, 145, 75)
	default:
		if index == 1 {
			return rgbRef(220, 55, 45)
		}
		return rgbRef(38, 92, 205)
	}
}

func traceVisualScale(series traceAnalysisSeries) float64 {
	if traceAnalysisFullScale {
		return math.Max(series.stats.PeakAbs, 1e-30)
	}
	value := math.Max(math.Abs(series.stats.P01), math.Abs(series.stats.P99))
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		value = series.stats.PeakAbs
	}
	return math.Max(value, 1e-30)
}

type traceWaveformRow struct {
	seen                  bool
	first, last, min, max float64
	firstRun, lastRun     int
}

type traceWaveformSegment struct {
	row0, row1     int
	value0, value1 float64
	envelope       bool
}

// buildTraceWaveformSegments combines two requirements that matter at very
// different zoom levels. Adjacent occupied pixel rows are connected using the
// first/last sample order, while every row also retains its exact finite
// minimum/maximum envelope. The former prevents zoomed traces from degrading
// into disconnected horizontal dashes; the latter preserves sub-pixel spikes
// when many samples collapse into one screen row.
func buildTraceWaveformSegments(curve traceAnalysisSeries, timeMinUS, timeMaxUS float64, height int) []traceWaveformSegment {
	if len(curve.samples) == 0 || curve.dtUS <= 0 || height < 1 || timeMaxUS <= timeMinUS {
		return nil
	}
	rows := make([]traceWaveformRow, height)
	runID := 0
	previousFinite := false
	for sampleIndex, value := range curve.samples {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			if previousFinite {
				runID++
			}
			previousFinite = false
			continue
		}
		previousFinite = true
		timeUS := float64((curve.sampleStart + sampleIndex) * curve.dtUS)
		if timeUS < timeMinUS || timeUS > timeMaxUS {
			continue
		}
		rowIndex := clampInt(int(math.Round((timeUS-timeMinUS)/(timeMaxUS-timeMinUS)*float64(height-1))), 0, height-1)
		row := &rows[rowIndex]
		if !row.seen {
			row.seen = true
			row.first, row.min, row.max = value, value, value
			row.firstRun = runID
		} else {
			row.min = math.Min(row.min, value)
			row.max = math.Max(row.max, value)
		}
		row.last = value
		row.lastRun = runID
	}
	segments := make([]traceWaveformSegment, 0, height*2)
	previousRow := -1
	var previous traceWaveformRow
	for rowIndex, row := range rows {
		if !row.seen {
			continue
		}
		if previousRow >= 0 && previous.lastRun == row.firstRun {
			segments = append(segments, traceWaveformSegment{row0: previousRow, row1: rowIndex, value0: previous.last, value1: row.first})
		}
		segments = append(segments, traceWaveformSegment{row0: rowIndex, row1: rowIndex, value0: row.min, value1: row.max, envelope: true})
		previousRow, previous = rowIndex, row
	}
	return segments
}

func drawTraceAnalysisWaveform(hdc uintptr, rect RECT, series []traceAnalysisSeries) {
	w, h := rectWH(rect)
	if w < 40 || h < 40 {
		return
	}
	drawLine(hdc, int(rect.Left), int(rect.Top), int(rect.Left), int(rect.Bottom))
	drawLine(hdc, int(rect.Left), int(rect.Bottom), int(rect.Right), int(rect.Bottom))
	center := int(rect.Left) + w/2
	drawLine(hdc, center, int(rect.Top), center, int(rect.Bottom))
	drawAxisText(hdc, "波形（时间向下 / 振幅横向）", int(rect.Left), int(rect.Top)-23, int(rect.Right), int(rect.Top)-3, DT_CENTER)
	timeMinUS, timeMaxUS := math.Inf(1), math.Inf(-1)
	for _, curve := range series {
		if len(curve.samples) == 0 || curve.dtUS <= 0 {
			continue
		}
		timeMinUS = math.Min(timeMinUS, float64(curve.sampleStart*curve.dtUS))
		timeMaxUS = math.Max(timeMaxUS, float64((curve.sampleStart+len(curve.samples)-1)*curve.dtUS))
	}
	if math.IsInf(timeMinUS, 1) || timeMaxUS <= timeMinUS {
		timeMinUS, timeMaxUS = 0, 1
	}
	for seriesIndex, curve := range series {
		if len(curve.samples) == 0 {
			continue
		}
		pen, _, _ := pCreatePen.Call(PS_SOLID, 1, traceSeriesColor(seriesIndex, curve.role))
		oldPen, _, _ := pSelectObject.Call(hdc, pen)
		scale := traceVisualScale(curve)
		halfWidth := float64(maxInt(w/2-8, 1))
		for _, segment := range buildTraceWaveformSegments(curve, timeMinUS, timeMaxUS, h) {
			x0 := center + int(math.Round(clampFloat(segment.value0/scale, -1, 1)*halfWidth))
			x1 := center + int(math.Round(clampFloat(segment.value1/scale, -1, 1)*halfWidth))
			y0, y1 := int(rect.Top)+segment.row0, int(rect.Top)+segment.row1
			if segment.envelope && x0 == x1 {
				// GDI LineTo excludes the final endpoint, so a zero-length row
				// would otherwise disappear entirely.
				drawLine(hdc, x0-1, y0, x0+2, y0)
			} else {
				drawLine(hdc, x0, y0, x1, y1)
			}
		}
		pSelectObject.Call(hdc, oldPen)
		pDeleteObject.Call(pen)
	}
	markerTimes := make([]float64, 0, len(series))
	for _, curve := range series {
		marker := curve.markerSample - curve.sampleStart
		if marker < 0 || marker >= len(curve.samples) || curve.dtUS <= 0 {
			continue
		}
		markerUS := float64(curve.markerSample * curve.dtUS)
		duplicate := false
		for _, existing := range markerTimes {
			if math.Abs(existing-markerUS) < 0.5 {
				duplicate = true
				break
			}
		}
		if !duplicate {
			markerTimes = append(markerTimes, markerUS)
		}
	}
	if len(markerTimes) > 0 {
		markerColor := rgbRef(224, 145, 35)
		pen, _, _ := pCreatePen.Call(PS_DOT, 1, markerColor)
		oldPen, _, _ := pSelectObject.Call(hdc, pen)
		oldColor, _, _ := pSetTextColor.Call(hdc, markerColor)
		for _, markerUS := range markerTimes {
			y := int(rect.Top) + clampInt(int(math.Round((markerUS-timeMinUS)/(timeMaxUS-timeMinUS)*float64(h-1))), 0, h-1)
			drawLine(hdc, int(rect.Left), y, int(rect.Right), y)
			drawAxisText(hdc, "光标", int(rect.Right)-42, y-17, int(rect.Right)-2, y-1, DT_RIGHT)
		}
		pSetTextColor.Call(hdc, oldColor)
		pSelectObject.Call(hdc, oldPen)
		pDeleteObject.Call(pen)
	}
	drawAxisText(hdc, formatAdaptiveTimeMS(timeMinUS/1000), int(rect.Left)+3, int(rect.Top), int(rect.Left)+90, int(rect.Top)+18, DT_LEFT)
	drawAxisText(hdc, formatAdaptiveTimeMS(timeMaxUS/1000), int(rect.Left)+3, int(rect.Bottom)-20, int(rect.Left)+90, int(rect.Bottom), DT_LEFT)
}

func drawTraceAnalysisSpectrum(hdc uintptr, rect RECT, series []traceAnalysisSeries) {
	w, h := rectWH(rect)
	if w < 40 || h < 40 {
		return
	}
	drawLine(hdc, int(rect.Left), int(rect.Top), int(rect.Left), int(rect.Bottom))
	drawLine(hdc, int(rect.Left), int(rect.Bottom), int(rect.Right), int(rect.Bottom))
	drawAxisText(hdc, "归一化频谱（dB）", int(rect.Left), int(rect.Top)-23, int(rect.Right), int(rect.Top)-3, DT_CENTER)
	sharedNyquist := 0.0
	for _, curve := range series {
		sharedNyquist = math.Max(sharedNyquist, curve.spectrum.NyquistHz)
	}
	for db := -80; db <= 0; db += 20 {
		y := int(rect.Bottom) - int(math.Round(float64(db+80)/80*float64(h)))
		drawLine(hdc, int(rect.Left)-3, y, int(rect.Left)+3, y)
		drawAxisText(hdc, fmt.Sprintf("%d", db), int(rect.Left)-38, y-9, int(rect.Left)-5, y+9, DT_RIGHT)
	}
	for seriesIndex, curve := range series {
		spectrum := curve.spectrum
		if len(spectrum.FrequencyHz) < 2 || len(spectrum.NormalizedDB) != len(spectrum.FrequencyHz) || spectrum.NyquistHz <= 0 || sharedNyquist <= 0 {
			continue
		}
		pen, _, _ := pCreatePen.Call(PS_SOLID, 1, traceSeriesColor(seriesIndex, curve.role))
		oldPen, _, _ := pSelectObject.Call(hdc, pen)
		for index := 1; index < len(spectrum.FrequencyHz); index++ {
			x0 := int(rect.Left) + int(math.Round(spectrum.FrequencyHz[index-1]/sharedNyquist*float64(w)))
			x1 := int(rect.Left) + int(math.Round(spectrum.FrequencyHz[index]/sharedNyquist*float64(w)))
			y0 := int(rect.Bottom) - int(math.Round((clampFloat(spectrum.NormalizedDB[index-1], -80, 0)+80)/80*float64(h)))
			y1 := int(rect.Bottom) - int(math.Round((clampFloat(spectrum.NormalizedDB[index], -80, 0)+80)/80*float64(h)))
			drawLine(hdc, x0, y0, x1, y1)
		}
		pSelectObject.Call(hdc, oldPen)
		pDeleteObject.Call(pen)
	}
	if sharedNyquist > 0 {
		drawAxisText(hdc, "0 Hz", int(rect.Left), int(rect.Bottom)+3, int(rect.Left)+60, int(rect.Bottom)+21, DT_LEFT)
		drawAxisText(hdc, fmt.Sprintf("%.3g Hz", sharedNyquist), int(rect.Right)-90, int(rect.Bottom)+3, int(rect.Right), int(rect.Bottom)+21, DT_RIGHT)
	}
}

func paintTraceAnalysis() {
	var ps PAINTSTRUCT
	hdc, _, _ := pBeginPaint.Call(traceAnalysisHwnd, uintptr(unsafe.Pointer(&ps)))
	defer pEndPaint.Call(traceAnalysisHwnd, uintptr(unsafe.Pointer(&ps)))
	client := clientRect(traceAnalysisHwnd)
	white, _, _ := pGetStockObject.Call(WHITE_BRUSH)
	pFillRect.Call(hdc, uintptr(unsafe.Pointer(&client)), white)
	if hFont != 0 {
		old, _, _ := pSelectObject.Call(hdc, hFont)
		defer pSelectObject.Call(hdc, old)
	}
	pSetBkMode.Call(hdc, TRANSPARENT)
	if traceAnalysisPage != 2 {
		return
	}
	w, h := rectWH(client)
	if traceAnalysisCurrent == nil {
		drawAxisText(hdc, "正在读取所选单道...", 16, 86, w-16, 116, DT_CENTER)
		return
	}
	drawAxisText(hdc, traceAnalysisCurrent.selection.Context, 14, 73, w-14, 93, DT_LEFT)
	y := 96
	for index, series := range traceAnalysisCurrent.series {
		pSetTextColor.Call(hdc, traceSeriesColor(index, series.role))
		drawAxisText(hdc, traceAnalysisStatsText(series), 14, y, w-14, y+18, DT_LEFT)
		y += 18
		if series.spectrumErr != "" {
			drawAxisText(hdc, series.role+" 频谱不可用："+series.spectrumErr, 30, y, w-14, y+18, DT_LEFT)
			y += 18
		} else {
			drawAxisText(hdc, fmt.Sprintf("%s  主频 %.3f Hz  Nyquist %.3f Hz  NFFT %d", series.role, series.spectrum.PeakHz, series.spectrum.NyquistHz, series.spectrum.NFFT), 30, y, w-14, y+18, DT_LEFT)
			y += 18
		}
	}
	for _, file := range traceAnalysisCurrent.files {
		if file.waveError != "" {
			pSetTextColor.Call(hdc, rgbRef(180, 70, 20))
			drawAxisText(hdc, file.target.Role+" 波形不可用："+file.waveError, 14, y, w-14, y+18, DT_LEFT)
			y += 18
		}
	}
	if traceAnalysisCurrent.diffNote != "" {
		pSetTextColor.Call(hdc, rgbRef(180, 70, 20))
		drawAxisText(hdc, traceAnalysisCurrent.diffNote, 14, y, w-14, y+18, DT_LEFT)
		y += 20
	}
	pSetTextColor.Call(hdc, rgbRef(20, 20, 20))
	plotTop := maxInt(y+26, 205)
	plotBottom := h - 45
	if plotBottom <= plotTop+60 {
		return
	}
	gap := 70
	leftWidth := (w - 50 - gap) / 2
	waveRect := RECT{Left: 48, Top: int32(plotTop), Right: int32(48 + leftWidth), Bottom: int32(plotBottom)}
	spectrumRect := RECT{Left: int32(48 + leftWidth + gap), Top: int32(plotTop), Right: int32(w - 24), Bottom: int32(plotBottom)}
	drawTraceAnalysisWaveform(hdc, waveRect, traceAnalysisCurrent.series)
	drawTraceAnalysisSpectrum(hdc, spectrumRect, traceAnalysisCurrent.series)
}

func traceAnalysisWndProc(h uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case WM_CREATE:
		traceAnalysisHwnd = h
		createTraceAnalysisUI()
		return 0
	case WM_COMMAND:
		id := int(wParam & 0xffff)
		notify := (wParam >> 16) & 0xffff
		switch id {
		case IDTRACE_SOURCE:
			if notify == 1 || notify == 9 {
				index := selectedTraceAnalysisIndex()
				if index >= 0 {
					setText(traceAnalysisUI.number, strconv.FormatInt(traceAnalysisSelectionCurrent.Targets[index].Trace+1, 10))
				}
				rebuildTraceAnalysisTextSelectors()
				rebuildTraceAnalysisReports()
				pInvalidateRect.Call(h, 0, 0)
			}
		case IDTRACE_PREV:
			changeTraceAnalysisTrace(-1, false)
		case IDTRACE_NEXT:
			changeTraceAnalysisTrace(1, false)
		case IDTRACE_GOTO:
			changeTraceAnalysisTrace(0, true)
		case IDTRACE_WINDOW:
			if notify == 1 || notify == 9 {
				selected, _, _ := pSendMessageW.Call(traceAnalysisUI.window, CB_GETCURSEL, 0, 0)
				traceAnalysisUseFull = int(selected) == 1
				startTraceAnalysisLoad()
			}
		case IDTRACE_SCALE:
			if notify == 1 || notify == 9 {
				selected, _, _ := pSendMessageW.Call(traceAnalysisUI.scale, CB_GETCURSEL, 0, 0)
				traceAnalysisFullScale = int(selected) == 1
				pInvalidateRect.Call(h, 0, 0)
			}
		case IDTRACE_TEXT_HEADER, IDTRACE_ENCODING:
			if notify == 1 || notify == 9 {
				refreshTraceAnalysisDecodedTextHeader()
			}
		case IDTRACE_BINARY_FILTER, IDTRACE_TRACE_FILTER:
			if notify == 1 || notify == 9 {
				control := traceAnalysisUI.binaryTable.filter
				if id == IDTRACE_TRACE_FILTER {
					control = traceAnalysisUI.traceTable.filter
				}
				selected, _, _ := pSendMessageW.Call(control, CB_GETCURSEL, 0, 0)
				setTraceAnalysisFieldFilter(traceHeaderFilter(clampInt(int(selected), int(traceHeaderFilterAll), int(traceHeaderFilterNonZero))))
			}
		case IDTRACE_BINARY_HEX:
			toggleTraceHeaderHex(&traceAnalysisUI.binaryTable)
		case IDTRACE_TRACE_HEX:
			toggleTraceHeaderHex(&traceAnalysisUI.traceTable)
		case IDTRACE_COPY:
			copyTraceAnalysisPage()
		case IDTRACE_CLOSE:
			pDestroyWindow.Call(h)
		}
		return 0
	case WM_NOTIFY:
		if lParam != 0 {
			header := (*NMHDR)(unsafe.Pointer(lParam))
			if header.IdFrom == IDTRACE_TABS && header.Code == TCN_SELCHANGE {
				selected, _, _ := pSendMessageW.Call(traceAnalysisUI.tabs, TCM_GETCURSEL, 0, 0)
				traceAnalysisPage = clampInt(int(selected), 0, 2)
				updateTraceAnalysisPageVisibility()
				return 0
			}
			if header.IdFrom == IDTRACE_HEADER_SUBTABS && header.Code == TCN_SELCHANGE {
				selected, _, _ := pSendMessageW.Call(traceAnalysisUI.headerSubTabs, TCM_GETCURSEL, 0, 0)
				traceAnalysisHeaderPage = clampInt(int(selected), 0, 1)
				updateTraceAnalysisPageVisibility()
				layoutTraceAnalysis()
				return 0
			}
		}
	case WM_TRACE_ANALYSIS_READY:
		handleTraceAnalysisReady(int64(wParam))
		return 0
	case WM_SIZE:
		layoutTraceAnalysis()
		pInvalidateRect.Call(h, 0, 0)
		return 0
	case WM_GETMINMAXINFO:
		if lParam != 0 {
			mmi := (*MINMAXINFO)(unsafe.Pointer(lParam))
			mmi.PtMinTrackSize.X = 1000
			mmi.PtMinTrackSize.Y = 560
		}
		return 0
	case WM_KEYDOWN:
		if wParam == VK_ESCAPE {
			pDestroyWindow.Call(h)
			return 0
		}
	case WM_ERASEBKGND:
		return 1
	case WM_PAINT:
		paintTraceAnalysis()
		return 0
	case WM_CLOSE:
		pDestroyWindow.Call(h)
		return 0
	case WM_DESTROY:
		atomic.AddInt64(&traceAnalysisGeneration, 1)
		traceAnalysisHwnd = 0
		traceAnalysisUI = traceAnalysisControls{}
		traceAnalysisCurrent = nil
		traceAnalysisReadyMu.Lock()
		traceAnalysisReady = make(map[int64]*traceAnalysisResult)
		traceAnalysisReadyMu.Unlock()
		return 0
	}
	result, _, _ := pDefWindowProcW.Call(h, uintptr(msg), wParam, lParam)
	return result
}
