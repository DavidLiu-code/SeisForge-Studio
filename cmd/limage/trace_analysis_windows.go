//go:build windows

package main

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/poststack"
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
	source, number, prev, next, goTo      uintptr
	sourceLabel, numberLabel, windowLabel uintptr
	window, scale                         uintptr
	copy, close, tabs                     uintptr
	textHeaderLabel, textHeader           uintptr
	encodingLabel, encoding               uintptr
	headerSubTabs, headerSummary          uintptr
	headerText, traceSummary              uintptr
	binaryTable, traceTable               traceHeaderTableControls
	scientific                            traceScientificControls
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
	target           traceAnalysisTarget
	info             segy.Info
	sourceModifiedNS int64
	timeOriginUS     float64 // origin of the source (A-relative for generated B) sample axis
	textHeaders      []segy.TextHeader
	binaryHeader     segy.BinaryHeader
	traceHeader      segy.TraceHeader
	sampleStart      int
	sampleEnd        int
	sampleIntervalUS int // science analysis may use an explicit per-trace sampling interval
	samples          []float64
	stats            segyanalysis.TraceStats
	spectrum         segyanalysis.Spectrum
	headerError      string
	waveError        string
	spectrumErr      string
	manualScopeEmpty bool // readable trace/axis but no real sample in the requested interval
}

func (r traceAnalysisFileResult) analysisIntervalUS() int {
	if r.sampleIntervalUS > 0 {
		return r.sampleIntervalUS
	}
	return r.info.SampleIntervalUS
}

func traceAnalysisHeaderAxis(info segy.Info, header segy.TraceHeader) (count, dt int) {
	count, dt = info.SamplesPerTrace, info.SampleIntervalUS
	if header.SampleCount > 0 {
		count = min(count, header.SampleCount)
	}
	if header.SampleIntervalUS > 0 {
		dt = header.SampleIntervalUS
	}
	return
}

type traceAnalysisSeries struct {
	role         string
	sampleStart  int
	dtUS         int
	timeOriginUS float64
	markerSample int
	samples      []float64
	stats        segyanalysis.TraceStats
	spectrum     segyanalysis.Spectrum
	spectrumErr  string
}

type traceAnalysisResult struct {
	generation int64
	owner      uintptr
	version    tracePoststackVersion
	selection  traceAnalysisSelection
	files      []traceAnalysisFileResult
	series     []traceAnalysisSeries
	diffNote   string
	snapshot   *segyanalysis.TraceSnapshot
}

func (r *traceAnalysisResult) manualWindowHasNoSamples() bool {
	if r.selection.Window.mode != traceAnalysisManual || r.snapshot == nil ||
		len(r.snapshot.Series) != 0 || len(r.files) == 0 {
		return false
	}
	for _, f := range r.files {
		if !f.manualScopeEmpty {
			return false // A read/axis failure is not an intentionally empty view.
		}
	}
	return true
}

var (
	traceAnalysisHwnd             uintptr
	traceAnalysisUI               traceAnalysisControls
	traceAnalysisSelectionCurrent traceAnalysisSelection
	traceAnalysisCurrent          *traceAnalysisResult
	traceAnalysisGeneration       int64
	traceAnalysisRequestedVersion tracePoststackVersion
	traceAnalysisCancel           context.CancelFunc
	traceAnalysisPending          bool
	traceAnalysisManualGeneration int64 // nonzero while a manual candidate is not yet committed
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
	traceScientificStyle.status = ""
	if traceAnalysisHwnd == 0 {
		work := displayEditorWorkArea(selection.Anchor.Owner, nil)
		dpi := displayDPI(selection.Anchor.Owner)
		w, ht := min(work.Right-work.Left, int32(displayPixelDIP(1110, dpi))), min(work.Bottom-work.Top, int32(displayPixelDIP(720, dpi)))
		x, y := work.Left+(work.Right-work.Left-w)/2, work.Top+(work.Bottom-work.Top-ht)/2
		h, _, _ := pCreateWindowExW.Call(WS_EX_APPWINDOW,
			uintptr(unsafe.Pointer(u16("Limage64TraceAnalysis"))),
			uintptr(unsafe.Pointer(u16(APP_NAME+" - SEG-Y 文件分析"))),
			WS_OVERLAPPEDWINDOW|WS_CLIPCHILDREN, uintptr(x), uintptr(y), uintptr(w), uintptr(ht), 0, 0, 0, 0)
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
	traceAnalysisUI.sourceLabel = createTraceToolbarLabel(traceAnalysisHwnd, "卷头来源")
	traceAnalysisUI.source = createCtrl(traceAnalysisHwnd, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 45, 7, 150, 180, IDTRACE_SOURCE)
	traceAnalysisUI.numberLabel = createTraceToolbarLabel(traceAnalysisHwnd, "物理道号")
	traceAnalysisUI.number = createCtrl(traceAnalysisHwnd, "EDIT", "1", WS_CHILD|WS_VISIBLE|WS_TABSTOP|WS_BORDER|ES_AUTOHSCROLL, 240, 7, 72, 23, IDTRACE_NUMBER)
	traceAnalysisUI.prev = traceScientificButton(IDTRACE_PREV, poststackGlyphPrevious, "上一道", "选择剖面上一道")
	traceAnalysisUI.next = traceScientificButton(IDTRACE_NEXT, poststackGlyphNext, "下一道", "选择剖面下一道")
	traceAnalysisUI.goTo = traceScientificButton(IDTRACE_GOTO, poststackGlyphGo, "转到", "转到所选侧物理道号（从 1 起）")
	traceAnalysisUI.windowLabel = createTraceToolbarLabel(traceAnalysisHwnd, "分析时间窗")
	traceAnalysisUI.window = createCtrl(traceAnalysisHwnd, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 548, 7, 102, 100, IDTRACE_WINDOW)
	for _, label := range traceAnalysisScopeChoices {
		pSendMessageW.Call(traceAnalysisUI.window, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(label))))
	}
	traceAnalysisManualDraft = traceAnalysisWindow.mode == traceAnalysisManual
	pSendMessageW.Call(traceAnalysisUI.window, CB_SETCURSEL, uintptr(traceAnalysisWindow.mode), 0)
	traceAnalysisUI.scale = createCtrl(traceAnalysisHwnd, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 697, 7, 104, 100, IDTRACE_SCALE)
	for _, label := range []string{"共同自适应 P1–P99", "共同全幅自适应", "手动振幅"} {
		pSendMessageW.Call(traceAnalysisUI.scale, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(label))))
	}
	pSendMessageW.Call(traceAnalysisUI.scale, CB_SETCURSEL, uintptr(traceScientificStyle.mode), 0)
	traceAnalysisUI.copy = traceScientificButton(IDTRACE_COPY, poststackGlyphCopy, "复制", "复制图形或当前卷头、道头、统计文本")
	traceAnalysisUI.close = traceScientificButton(IDTRACE_CLOSE, poststackGlyphClose, "关闭", "关闭单道分析窗口")
	createTraceScientificControls()

	traceAnalysisUI.tabs = createCtrl(traceAnalysisHwnd, "SysTabControl32", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP, 8, 38, 1090, 28, IDTRACE_TABS)
	for index, label := range []string{"卷头", "道头", "单道分析", "统计与范围"} {
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
	targets := traceAnalysisCommittedTargets()
	for _, target := range targets {
		label := target.Role
		if label == "" {
			label = "数据"
		}
		label += "  " + filepath.Base(target.Path)
		pSendMessageW.Call(traceAnalysisUI.source, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(label))))
	}
	selected := int(previous)
	if selected < 0 || selected >= len(targets) {
		selected = 0
	}
	pSendMessageW.Call(traceAnalysisUI.source, CB_SETCURSEL, uintptr(selected), 0)
	if len(targets) > 0 && traceAnalysisSelectionCurrent.Anchor.Owner == 0 {
		setText(traceAnalysisUI.number, strconv.FormatInt(targets[selected].Trace+1, 10))
	}
}

func traceAnalysisCommittedTargets() []traceAnalysisTarget {
	if traceAnalysisCurrent != nil {
		return traceAnalysisCurrent.selection.Targets
	}
	return traceAnalysisSelectionCurrent.Targets
}

func selectedTraceAnalysisIndex() int {
	targets := traceAnalysisCommittedTargets()
	if len(targets) == 0 {
		return -1
	}
	selected, _, _ := pSendMessageW.Call(traceAnalysisUI.source, CB_GETCURSEL, 0, 0)
	index := int(selected)
	if index < 0 || index >= len(targets) {
		return 0
	}
	return index
}

func startTraceAnalysisLoad() {
	startTraceAnalysisLoadInScope(traceAnalysisWindow, false)
}

func startTraceAnalysisLoadInScope(scope traceAnalysisScope, draft bool) {
	if traceAnalysisHwnd == 0 || (len(traceAnalysisSelectionCurrent.Targets) == 0 && traceAnalysisSelectionCurrent.Anchor.Owner == 0) {
		return
	}
	if traceAnalysisCancel != nil {
		traceAnalysisCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	traceAnalysisCancel = cancel
	owner := traceAnalysisHwnd
	generation := atomic.AddInt64(&traceAnalysisGeneration, 1)
	if draft {
		traceAnalysisManualGeneration = generation
		traceAnalysisSetScopeFeedback("计算中", "正在按真实采样网格验证时间窗；此前分析仍然有效。")
	} else if traceAnalysisManualGeneration != 0 {
		traceAnalysisManualGeneration = 0
		traceAnalysisSetScopeFeedback("", "")
	}
	useFull := scope.mode == traceAnalysisFull
	selection := freezeTraceAnalysisInScope(traceAnalysisSelectionCurrent, scope)
	version := tracePoststackVersion{SNRFormula: segyanalysis.ReferenceSNRFormulaVersion}
	if selection.Anchor.Owner != 0 {
		version = currentTracePoststackVersion()
	}
	traceAnalysisRequestedVersion = version
	traceAnalysisPending = true
	refreshTraceScientificUI()
	pInvalidateRect.Call(owner, 0, 0)
	go func() {
		result := buildTraceAnalysisResultContext(ctx, generation, selection, useFull)
		if result == nil || ctx.Err() != nil {
			return
		}
		result.owner, result.version = owner, version
		traceAnalysisReadyMu.Lock()
		if generation != atomic.LoadInt64(&traceAnalysisGeneration) || ctx.Err() != nil {
			traceAnalysisReadyMu.Unlock()
			return
		}
		traceAnalysisReady[generation] = result
		traceAnalysisReadyMu.Unlock()
		if posted, _, _ := pPostMessageW.Call(owner, WM_TRACE_ANALYSIS_READY, uintptr(generation), 0); posted == 0 {
			traceAnalysisReadyMu.Lock()
			delete(traceAnalysisReady, generation)
			traceAnalysisReadyMu.Unlock()
		}
	}()
}

func traceAnalysisAbortManualRequest() {
	if traceAnalysisManualGeneration == 0 {
		return
	}
	traceAnalysisManualGeneration = 0
	traceAnalysisPending = false
	atomic.AddInt64(&traceAnalysisGeneration, 1)
	if traceAnalysisCancel != nil {
		traceAnalysisCancel()
		traceAnalysisCancel = nil
	}
}

func buildTraceAnalysisResult(generation int64, selection traceAnalysisSelection, useFull bool) *traceAnalysisResult {
	return buildTraceAnalysisResultContext(context.Background(), generation, selection, useFull)
}

func buildTraceAnalysisResultContext(ctx context.Context, generation int64, selection traceAnalysisSelection, useFull bool) *traceAnalysisResult {
	result := &traceAnalysisResult{generation: generation, selection: selection, files: make([]traceAnalysisFileResult, 0, len(selection.Targets))}
	var inputs []segyanalysis.TraceInput
	for _, target := range selection.Targets {
		if ctx.Err() != nil {
			return nil
		}
		fileResult := readTraceAnalysisTargetContext(ctx, target, useFull, selection.Anchor.Owner != 0)
		if selection.Anchor.Owner == 0 {
			computeLegacyTraceStats(&fileResult)
		}
		if len(fileResult.samples) > 0 {
			dt := fileResult.analysisIntervalUS()
			inputs = append(inputs, segyanalysis.TraceInput{Role: target.Role, StartUS: int64(math.Round(fileResult.timeOriginUS + float64(fileResult.sampleStart)*float64(dt))), IntervalUS: dt, Samples: fileResult.samples})
		}
		result.files = append(result.files, fileResult)
		if selection.Anchor.Owner != 0 {
			continue
		}
		if len(fileResult.samples) > 0 {
			result.series = append(result.series, traceAnalysisSeries{role: target.Role, sampleStart: fileResult.sampleStart,
				dtUS: fileResult.info.SampleIntervalUS, timeOriginUS: fileResult.timeOriginUS, markerSample: target.MarkerSample + target.SourceSampleOffset, samples: fileResult.samples,
				stats: fileResult.stats, spectrum: fileResult.spectrum, spectrumErr: fileResult.spectrumErr})
		}
	}
	if selection.Anchor.Owner != 0 {
		data := segyanalysis.BuildTraceSnapshot(inputs, selection.CompareGroup)
		result.snapshot = &data
		result.diffNote = selection.PairNote
		if data.ResidualNote != "" && (selection.PairNote == "" || selection.Difference) {
			result.diffNote = data.ResidualNote
		}
		return result
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
		case a.timeOriginUS != b.timeOriginUS:
			result.diffNote = "A-B 不可计算：实际时间起点不一致"
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
				dtUS: a.info.SampleIntervalUS, timeOriginUS: a.timeOriginUS, markerSample: a.target.MarkerSample + a.target.SourceSampleOffset, samples: difference,
				stats: stats, spectrum: spectrum, spectrumErr: note})
		}
	}
	data := segyanalysis.BuildTraceSnapshot(inputs, selection.Difference && result.diffNote == "")
	result.snapshot = &data
	return result
}

func readTraceAnalysisTarget(target traceAnalysisTarget, useFull bool) traceAnalysisFileResult {
	r := readTraceAnalysisTargetContext(context.Background(), target, useFull, false)
	computeLegacyTraceStats(&r)
	return r
}

func computeLegacyTraceStats(r *traceAnalysisFileResult) {
	if len(r.samples) == 0 {
		return
	}
	r.stats = segyanalysis.ComputeTraceStats(r.samples)
	var err error
	if r.spectrum, err = segyanalysis.ComputeTraceSpectrum(r.samples, r.info.SampleIntervalUS); err != nil {
		r.spectrumErr = err.Error()
	}
}

func readTraceAnalysisTargetContext(ctx context.Context, target traceAnalysisTarget, useFull, useTraceAxis bool) traceAnalysisFileResult {
	result := traceAnalysisFileResult{target: target}
	reader, err := openPoststackCompareSource(target.Path)
	if err != nil {
		result.headerError, result.waveError = err.Error(), err.Error()
		return result
	}
	defer reader.Close()
	result.info = reader.Info
	if info, statErr := reader.SourceFileInfo(); statErr == nil {
		result.sourceModifiedNS = info.ModTime().UnixNano()
	}
	sourceUnchanged := func() bool {
		source, err := viewFilterSourceIdentity(reader, target.Path, nil)
		return err == nil && (result.sourceModifiedNS == 0 || source.ModTime().UnixNano() == result.sourceModifiedNS)
	}
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
	originUS, err := reader.TraceTimeOriginUS(target.Trace)
	if err != nil {
		result.waveError = err.Error()
		return result
	}
	count, dt := reader.Info.SamplesPerTrace, reader.Info.SampleIntervalUS
	if useTraceAxis {
		count, dt = traceAnalysisHeaderAxis(reader.Info, result.traceHeader)
		// Leave Info and both headers unchanged for the file/header pages.
		result.sampleIntervalUS = dt
	}
	if reader.Info.FormatCode == 4 {
		result.waveError = "样点格式码 4（带增益定点数）暂不支持波形解码；卷头和道头仍可查看"
		return result
	}
	start, end := target.SampleStart, target.SampleEnd
	if useFull {
		start, end = 0, count-1
	} else if target.WindowUnavailable {
		result.waveError = "主剖面所选时间窗在此道没有样点"
		return result
	} else if target.PhysicalWindow {
		if dt <= 0 || count <= 0 {
			result.waveError = "采样间隔或样点数无效"
			return result
		}
		var ok bool
		start, end, ok = traceAnalysisSampleIndices(originUS, target.FirstUS, target.LastUS, dt, count)
		if !ok {
			result.waveError = "该道与主剖面时间窗没有重叠"
			if target.ManualWindow {
				result.waveError = "该道在手动分析时间窗内没有真实样点"
				if !sourceUnchanged() {
					result.waveError = "读取期间数据源已改变"
					return result
				}
				result.manualScopeEmpty = true
			}
			return result
		}
	}
	if useTraceAxis && (count <= 0 || start >= count || end < max(0, start)) {
		result.waveError = "所选时间窗在此道没有样点"
		return result
	}
	start = clampInt(start, 0, count-1)
	end = clampInt(end, start, count-1)
	// Read B's local sample window, but carry its immutable A-relative axis
	// into waveform time, marker placement, and A-B compatibility. Never look
	// up the currently selected result here: the UI may have changed meanwhile.
	if target.SourceSampleOffset < 0 || target.SourceSampleOffset > int(^uint(0)>>1)-end ||
		(target.MarkerSample >= 0 && target.SourceSampleOffset > int(^uint(0)>>1)-target.MarkerSample) {
		result.waveError = "无效的生成结果样点坐标偏移"
		return result
	}
	result.sampleStart, result.sampleEnd = start+target.SourceSampleOffset, end+target.SourceSampleOffset
	result.timeOriginUS = float64(originUS) - float64(target.SourceSampleOffset)*float64(dt)
	if !useFull && len(target.FrozenSamples) == end-start+1 {
		result.samples = target.FrozenSamples
	} else if target.IsFiltered {
		var whole []float64
		whole, err = reader.ReadTraceWindow(target.Trace, 0, count-1)
		if err == nil {
			result.samples, err = poststack.FilterTraceWindow(ctx, whole, dt, target.Bandpass, start, end)
		}
	} else if target.Filtered != nil {
		result.samples, err = target.Filtered.read(target.Trace, start, end)
		if err != nil && useFull {
			err = fmt.Errorf("完整道不在已滤波的显示窗内；请选择「当前可见」")
		}
	} else {
		result.samples, err = reader.ReadTraceWindow(target.Trace, start, end)
	}
	if err != nil {
		result.waveError = err.Error()
		return result
	}
	if len(target.FrozenSamples) == 0 {
		// Source checks include the open handle and its current name, preventing
		// a replaced B from being associated with a completed older snapshot.
		if !sourceUnchanged() {
			result.samples = nil
			result.waveError = "读取期间数据源已改变"
			return result
		}
	}
	return result
}

func handleTraceAnalysisReady(generation int64) {
	traceAnalysisReadyMu.Lock()
	result := traceAnalysisReady[generation]
	delete(traceAnalysisReady, generation)
	traceAnalysisReadyMu.Unlock()
	if !traceAnalysisResultIsCurrent(result, traceAnalysisHwnd, generation, atomic.LoadInt64(&traceAnalysisGeneration), traceAnalysisRequestedVersion) {
		return
	}
	if result.selection.Anchor.Owner != 0 {
		live := currentTracePoststackVersion()
		if live != result.version {
			refreshScientificTraceFromCompare()
			return
		}
	}
	traceAnalysisPending = false
	if traceAnalysisCancel != nil {
		traceAnalysisCancel()
		traceAnalysisCancel = nil
	}
	if traceAnalysisManualGeneration == generation {
		traceAnalysisManualGeneration = 0
		if result.snapshot == nil || (len(result.snapshot.Series) == 0 && !result.manualWindowHasNoSamples()) {
			detail := traceScientificResultIssues(result)
			if detail != "" {
				detail += "；"
			}
			traceAnalysisSetScopeFeedback("读取失败", detail+"保留上一次有效分析。")
			traceScientificRepaint()
			return
		}
		traceAnalysisWindow = result.selection.Window
		if result.selection.Anchor.Owner != 0 {
			result.version = currentTracePoststackVersion()
			traceAnalysisRequestedVersion = result.version
		}
		traceAnalysisSetScopeFeedback("", "")
	}
	if previous := traceAnalysisCurrent; previous != nil && previous.selection.Window != result.selection.Window &&
		(previous.selection.Window.mode == traceAnalysisManual || result.selection.Window.mode == traceAnalysisManual) {
		traceScientificResetTimeView()
	}
	traceAnalysisCurrent = result
	populateTraceAnalysisSourceCombo()
	rebuildTraceAnalysisTextSelectors()
	rebuildTraceAnalysisReports()
	refreshTraceScientificUI()
	pInvalidateRect.Call(traceAnalysisHwnd, 0, 0)
}

func traceAnalysisResultIsCurrent(r *traceAnalysisResult, owner uintptr, generation, latest int64, version tracePoststackVersion) bool {
	return r != nil && owner != 0 && r.owner == owner && r.generation == generation && generation == latest &&
		r.version == version && (r.snapshot == nil || r.snapshot.ReferenceSNR.FormulaVersion == segyanalysis.ReferenceSNRFormulaVersion)
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
	if traceAnalysisCurrent.selection.Anchor.Owner == 0 {
		setText(traceAnalysisUI.number, strconv.FormatInt(file.target.Trace+1, 10))
	}
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
	refreshTraceScientificUI()
	if traceAnalysisHwnd != 0 {
		// Page changes move shared controls to different rows. Redraw both the old
		// exposed areas and the child controls, rather than reusing moved pixels.
		pRedrawWindow.Call(traceAnalysisHwnd, 0, 0, RDW_INVALIDATE|RDW_ERASE|RDW_ALLCHILDREN)
	}
}

func layoutTraceAnalysis() {
	if traceAnalysisHwnd == 0 {
		return
	}
	w, h := clientSize(traceAnalysisHwnd)
	bar := layoutTraceScientificControls(w, h)
	pMoveWindow.Call(traceAnalysisUI.tabs, 8, uintptr(bar.tabs), uintptr(maxInt(w-16, 80)), uintptr(bar.tabHeight), 1)
	contentWidth := maxInt(w-16, 80)
	top := bar.content
	pMoveWindow.Call(traceAnalysisUI.headerSummary, 8, uintptr(top), uintptr(contentWidth), 76, 1)
	pMoveWindow.Call(traceAnalysisUI.headerSubTabs, 8, uintptr(top+80), uintptr(contentWidth), 28, 1)
	pMoveWindow.Call(traceAnalysisUI.textHeaderLabel, 12, uintptr(top+114), 44, 20, 1)
	pMoveWindow.Call(traceAnalysisUI.textHeader, 58, uintptr(top+110), 172, 160, 1)
	pMoveWindow.Call(traceAnalysisUI.encodingLabel, 242, uintptr(top+114), 34, 20, 1)
	pMoveWindow.Call(traceAnalysisUI.encoding, 279, uintptr(top+110), 148, 120, 1)
	pMoveWindow.Call(traceAnalysisUI.headerText, 8, uintptr(top+140), uintptr(contentWidth), uintptr(maxInt(h-top-148, 80)), 1)
	layoutTraceHeaderTable(&traceAnalysisUI.binaryTable, 8, top+110, contentWidth, maxInt(h-top-118, 100))
	pMoveWindow.Call(traceAnalysisUI.traceSummary, 8, uintptr(top), uintptr(contentWidth), 106, 1)
	layoutTraceHeaderTable(&traceAnalysisUI.traceTable, 8, top+112, contentWidth, maxInt(h-top-120, 100))
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
	if traceAnalysisPage == 3 {
		return traceScientificStatsText(traceAnalysisCurrent)
	}
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
	if traceAnalysisPage == 2 {
		traceScientificCopyFigure()
		return
	}
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
	if traceAnalysisSelectionCurrent.Anchor.Owner != 0 {
		changeScientificTrace(delta, fromEdit)
		return
	}
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

func (curve traceAnalysisSeries) timeUS(sample int) float64 {
	return curve.timeOriginUS + float64(sample)*float64(curve.dtUS)
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
		timeUS := curve.timeUS(curve.sampleStart + sampleIndex)
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

func paintTraceAnalysis() {
	displayBeginPaint(traceAnalysisHwnd)
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
	top := traceScientificContentTop
	if traceAnalysisCurrent == nil {
		drawAxisText(hdc, "正在读取所选单道…", 16, top+18, w-16, top+50, DT_CENTER)
		return
	}
	if h > top {
		traceScientificActiveScene = traceScientificPlotScene(w, h-top, traceAnalysisCurrent, traceScientificOptions(traceAnalysisCurrent))
		traceScientificActiveScene.registerAxes(traceAnalysisHwnd, 0, top)
		traceScientificActiveScene.paint(hdc, 0, top)
		paintTraceScientificBox(traceAnalysisHwnd, hdc, top)
	}
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
				if index >= 0 && traceAnalysisSelectionCurrent.Anchor.Owner == 0 {
					setText(traceAnalysisUI.number, strconv.FormatInt(traceAnalysisCommittedTargets()[index].Trace+1, 10))
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
				traceAnalysisChooseScope(displaySelection(traceAnalysisUI.window))
			}
		case IDTRACE_SCALE:
			if notify == 1 || notify == 9 {
				traceScientificChooseScale()
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
		default:
			traceScientificCommand(id, int(notify))
		}
		return 0
	case WM_NOTIFY:
		if lParam != 0 {
			header := (*NMHDR)(unsafe.Pointer(lParam))
			if header.IdFrom == IDTRACE_TABS && header.Code == TCN_SELCHANGE {
				traceScientificCancelBox(h, true)
				selected, _, _ := pSendMessageW.Call(traceAnalysisUI.tabs, TCM_GETCURSEL, 0, 0)
				traceAnalysisPage = clampInt(int(selected), 0, 3)
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
		traceScientificCancelBox(h, true)
		layoutTraceAnalysis()
		pInvalidateRect.Call(h, 0, 0)
		return 0
	case 0x0006: // WM_ACTIVATE
		if wParam&0xffff == 0 { // WA_INACTIVE
			traceScientificCancelBox(h, true)
		}
	case WM_MOUSEWHEEL, WM_LBUTTONDOWN, WM_LBUTTONUP, WM_RBUTTONDOWN, WM_RBUTTONUP, WM_MOUSEMOVE, 0x0215, 0x001F:
		if traceScientificMouse(h, msg, wParam, lParam) {
			return 0
		}
	case 0x02E0: // WM_DPICHANGED
		if lParam != 0 {
			r := (*RECT)(unsafe.Pointer(lParam))
			pSetWindowPos.Call(h, 0, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), SWP_NOZORDER|SWP_NOACTIVATE)
		}
		layoutTraceAnalysis()
		return 0
	case WM_LBUTTONDBLCLK:
		x, y := int(int16(lParam)), int(int16(lParam>>16))
		if traceAnalysisPage == 2 && traceScientificActiveScene.openAxesAt(h, x, y, 0, traceScientificContentTop) {
			return 0
		}
		if displaySurfaceClick(h, x, y, false) {
			return 0
		}
	case 0x007B: // WM_CONTEXTMENU
		if displayContextMenu(h, lParam) {
			return 0
		}
	case WM_GETMINMAXINFO:
		if lParam != 0 {
			mmi := (*MINMAXINFO)(unsafe.Pointer(lParam))
			work := displayEditorWorkArea(h, nil)
			mmi.PtMinTrackSize.X = min(int32(displayPixelDIP(570, displayDPI(h))), work.Right-work.Left)
			// Reserve room for the two graph regions even when the compact setting
			// tray wraps. Never grow/move the outer window when the tray opens.
			mmi.PtMinTrackSize.Y = min(int32(displayPixelDIP(470, displayDPI(h))), work.Bottom-work.Top)
		}
		return 0
	case WM_KEYDOWN:
		if wParam == VK_ESCAPE {
			if traceScientificCancelBox(h, true) {
				return 0
			}
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
		traceScientificCancelBox(h, true)
		displayCloseOwner(h)
		if traceAnalysisCancel != nil {
			traceAnalysisCancel()
			traceAnalysisCancel = nil
		}
		if traceAnalysisUI.scientific.tools != nil {
			traceAnalysisUI.scientific.tools.close()
		}
		traceAnalysisPending = false
		traceAnalysisManualGeneration = 0
		traceScientificActiveScene = traceScientificScene{}
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
