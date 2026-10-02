//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	workspacecore "github.com/DavidLiu-code/SeisForge-Studio/internal/workspace"
)

const (
	IDPRESTACK_HOME = 8101 + iota
	IDPRESTACK_OPEN
	IDPRESTACK_TABS
	IDPRESTACK_KIND
	IDPRESTACK_KEY
	IDPRESTACK_PREV
	IDPRESTACK_NEXT
	IDPRESTACK_SORT
	IDPRESTACK_AXIS
	IDPRESTACK_DISPLAY
	IDPRESTACK_OFFSET_BIN
	IDPRESTACK_OFFSET_BIN_APPLY
	IDPRESTACK_RAW_TRACE_START
	IDPRESTACK_RAW_TRACE_END
	IDPRESTACK_RAW_SAMPLE_MODE
	IDPRESTACK_RAW_SAMPLE_START
	IDPRESTACK_RAW_SAMPLE_END
	IDPRESTACK_RAW_APPLY
	IDPRESTACK_RAW_ALL
	IDPRESTACK_PALETTE
	IDPRESTACK_GAINMINUS
	IDPRESTACK_GAINPLUS
	IDPRESTACK_AGC
	IDPRESTACK_RESET
	IDPRESTACK_HEADERS
	IDPRESTACK_MAPPING_AUTO
	IDPRESTACK_MAPPING_APPLY
	IDPRESTACK_LAYER_SOURCE
	IDPRESTACK_LAYER_RECEIVER
	IDPRESTACK_LAYER_MIDPOINT
	IDPRESTACK_LAYER_BIN
	IDPRESTACK_LAYER_FOLD
	IDPRESTACK_MAPRESET
	IDPRESTACK_MAPBYTE   = 8200
	WM_PRESTACK_READY    = WM_USER + 610
	WM_PRESTACK_PROGRESS = WM_USER + 611
	WM_PRESTACK_RENDER   = WM_USER + 612
	// WM_ENTERSIZEMOVE is not declared by the small Win32 constant set in
	// main_windows.go.  Keep the standard value local to this window and use a
	// private message to debounce the final resize render.
	WM_ENTERSIZEMOVE         = 0x0231
	WM_PRESTACK_RESIZE_FLUSH = WM_USER + 613
	// Post selection work so key-list rebuilding/render planning starts after
	// the native control has completed its notification transaction.
	WM_PRESTACK_COMBO_COMMIT = WM_USER + 614
	// Mouse capture belongs to the scene only while a drag is active.  A
	// native combo box takes capture while its drop list is open; without
	// clearing our scene state when that happens, subsequent clicks are still
	// interpreted as scene drags and the combo appears to be unresponsive.
	WM_CANCELMODE     = 0x001F
	WM_KILLFOCUS      = 0x0008
	WM_CAPTURECHANGED = 0x0215
	// Combo-box messages are kept local to the Prestack window so this
	// feature does not alter the legacy viewer constants.
	CB_RESETCONTENT    = 0x014B
	CB_GETDROPPEDSTATE = 0x0157
	CB_SETDROPPEDWIDTH = 0x0160
	CB_SETMINVISIBLE   = 0x1701
	// CBS_NOINTEGRALHEIGHT keeps the requested drop-list height instead of
	// letting the native control round it to a partial row.  Together with
	// WS_VSCROLL this makes the large gather-key list explicitly scrollable.
	prestackCBSNoIntegralHeight = 0x0400
	prestackSSCenter            = 0x00000001
	CBN_SELENDOK                = 9
	CBN_CLOSEUP                 = 8
)

type prestackControls struct {
	home, open, path, tabs, status, progress, progressLabel                             uintptr
	kind, key, prev, next, sort, axis, display, palette                                 uintptr
	offsetBinLabel, offsetBin, offsetBinApply                                           uintptr
	rawTraceStartLabel, rawTraceStart, rawTraceEndLabel, rawTraceEnd                    uintptr
	rawSampleMode, rawSampleStartLabel, rawSampleStart, rawSampleEndLabel, rawSampleEnd uintptr
	rawApply, rawAll                                                                    uintptr
	gainMinus, gain, gainPlus, agc, reset, headers                                      uintptr
	layers                                                                              [5]uintptr
	mapReset, mappingAuto, mappingApply, mappingText                                    uintptr
	mappingLabels, mappingEdits                                                         []uintptr
}

// All mutable presentation state belongs to this window, not to the legacy
// compare globals. Workers receive immutable snapshots and short-lived readers.
type prestackSession struct {
	dataset                                              *dataset.SeismicDataset
	index                                                *prestackcore.PrestackIndex
	mapping                                              prestackcore.HeaderMapping
	detection                                            prestackcore.MappingDetection
	selection                                            prestackcore.GatherSelection
	gather                                               prestackcore.GatherResult
	keys                                                 []prestackcore.GatherKey
	page, keyIndex, palette, display                     int
	rawSampleModeValue                                   int
	gain                                                 float64
	offsetBinSize                                        float64
	agc                                                  bool
	workspaceGeneration                                  uint64
	indexGeneration, renderGeneration                    int64
	resizeGeneration                                     int64
	indexCancel, renderCancel                            context.CancelFunc
	loading, rendering, needsIndex, progressDone         bool
	resizeActive, resizePending                          bool
	viewFirst, viewLast, sampleFirst, sampleLast         int
	indices, bgra                                        []byte
	imageWidth, imageHeight                              int
	stats                                                segy.RenderStats
	mapBounds                                            prestackcore.XYBounds
	mapLayers                                            [5]bool
	dragging, panning                                    bool
	ctrlClick                                            bool
	dragX, dragY, dragCurrentX, dragCurrentY             int
	dragMap                                              prestackcore.XYBounds
	dragFirst, dragLast, dragSampleFirst, dragSampleLast int
	lastHover                                            time.Time
}

type prestackIndexResult struct {
	generation int64
	index      *prestackcore.PrestackIndex
	detection  prestackcore.MappingDetection
	detected   bool
	err        error
}
type prestackRenderResult struct {
	generation          int64
	workspaceGeneration uint64
	indices, bgra       []byte
	width, height       int
	stats               segy.RenderStats
	err                 error
}

var (
	prestackHwnd                                    uintptr
	prestackUI                                      prestackControls
	prestackState                                   prestackSession
	prestackClassRegistered, prestackManagerClosing bool
	prestackGeneration                              int64
	prestackDeliveryMu                              sync.Mutex
	prestackDeliveryHwnd                            uintptr
	prestackPendingIndex                            *prestackIndexResult
	prestackPendingRender                           *prestackRenderResult
	prestackProgress                                int64
	prestackRenderSlot                              = make(chan struct{}, 1)
)

// MK_CONTROL is included in the low word of wParam for mouse messages.  A
// plain click keeps the 2-D navigation semantics (a double click resets the
// view); Ctrl+click is the explicit single-trace inspection gesture.
const prestackMKControl = uintptr(0x0008)

type prestackWorkspaceAdapter struct{}

func (*prestackWorkspaceAdapter) Kind() workspacecore.Kind { return workspacecore.KindPrestack }
func (*prestackWorkspaceAdapter) Open(r workspacecore.OpenRequest) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if !createPrestackWindowShell() {
		return errors.New("could not create Prestack window")
	}
	cancelPrestackJobs()
	prestackState = prestackSession{dataset: r.Dataset, mapping: prestackcore.DefaultHeaderMapping(), palette: 2,
		workspaceGeneration: r.Generation, sampleLast: r.Dataset.Metadata.SamplesPerTrace - 1,
		mapLayers: [5]bool{true, true, true, true, true}}
	prestackState.sampleFirst, prestackState.sampleLast = int(r.EffectiveSampleRange().Start), int(r.EffectiveSampleRange().End)
	setText(prestackUI.path, r.Dataset.Path)
	populatePrestackMapping()
	resetPrestackControls()
	startPrestackIndex(true)
	return nil
}
func (*prestackWorkspaceAdapter) Show() error {
	if prestackHwnd == 0 {
		return errors.New("Prestack window is unavailable")
	}
	showTopLevelWindowRestored(prestackHwnd)
	if prestackState.needsIndex {
		startPrestackIndex(false)
	} else if prestackState.index != nil && len(prestackState.indices) == 0 {
		startPrestackRender()
	}
	return nil
}
func (*prestackWorkspaceAdapter) Hide() error {
	if prestackHwnd != 0 {
		prestackState.needsIndex = prestackState.loading
		cancelPrestackJobs()
		pShowWindow.Call(prestackHwnd, SW_HIDE)
	}
	return nil
}
func (*prestackWorkspaceAdapter) Close() error {
	if prestackHwnd != 0 {
		prestackManagerClosing = true
		pDestroyWindow.Call(prestackHwnd)
		prestackManagerClosing = false
	}
	return nil
}

func createPrestackWindowShell() bool {
	if prestackHwnd != 0 {
		return true
	}
	if !prestackClassRegistered {
		hi, _, _ := pGetModuleHandleW.Call(0)
		cursor, _, _ := pLoadCursorW.Call(0, IDC_ARROW)
		registerClass("SeisForgePrestack", syscall.NewCallback(prestackWndProc), COLOR_WINDOW+1, hi, cursor)
		prestackClassRegistered = true
	}
	h, _, _ := pCreateWindowExW.Call(WS_EX_APPWINDOW, uintptr(unsafe.Pointer(u16("SeisForgePrestack"))), uintptr(unsafe.Pointer(u16(APP_NAME+" v"+APP_VERSION+" — 叠前道集"))),
		WS_OVERLAPPEDWINDOW|WS_CLIPCHILDREN, uintptr(CW_USEDEFAULT), uintptr(CW_USEDEFAULT), 1360, 880, 0, 0, 0, 0)
	if h == 0 {
		return false
	}
	prestackHwnd = h
	prestackDeliveryMu.Lock()
	prestackDeliveryHwnd = h
	prestackDeliveryMu.Unlock()
	createPrestackControls()
	acceptSegyDrops(h)
	registerOleSegyDropTarget(h, workspaceModePrestack)
	return true
}

func prestackCombo(id int, labels []string) uintptr {
	// Use the established 2-D toolbar's native combo style.  The common control
	// automatically adds a scroll bar for large key sets.
	style := uint32(WS_CHILD | WS_VISIBLE | WS_TABSTOP | CBS_DROPDOWNLIST | CBS_HASSTRINGS)
	if id == IDPRESTACK_KEY {
		// Do not rely solely on CB_SETMINVISIBLE: older common-controls builds
		// can omit the popup scrollbar when the combo has a large custom height.
		// Request it explicitly so every CMP/Shot/Receiver/Offset key remains
		// reachable with the mouse wheel, scrollbar, and keyboard arrows.
		style |= uint32(WS_VSCROLL | prestackCBSNoIntegralHeight)
	}
	h := createCtrl(prestackHwnd, "COMBOBOX", "", style, 0, 0, 100, prestackComboPopupHeight(id, 24), id)
	for _, s := range labels {
		pSendMessageW.Call(h, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(s))))
	}
	pSendMessageW.Call(h, CB_SETCURSEL, 0, 0)
	minVisible, droppedWidth := 4, 180
	switch id {
	case IDPRESTACK_KIND:
		minVisible, droppedWidth = 5, 220
	case IDPRESTACK_DISPLAY:
		minVisible, droppedWidth = 2, 170
	case IDPRESTACK_SORT:
		minVisible, droppedWidth = 4, 150
	case IDPRESTACK_AXIS:
		minVisible, droppedWidth = 2, 140
	case IDPRESTACK_RAW_SAMPLE_MODE:
		minVisible, droppedWidth = 3, 150
	case IDPRESTACK_PALETTE:
		minVisible, droppedWidth = maxInt(4, len(labels)), 190
	case IDPRESTACK_KEY:
		minVisible, droppedWidth = 24, 310
	}
	pSendMessageW.Call(h, CB_SETMINVISIBLE, uintptr(minVisible), 0)
	pSendMessageW.Call(h, CB_SETDROPPEDWIDTH, uintptr(droppedWidth), 0)
	return h
}

// COMBOBOX's requested height includes its drop list, not just the closed
// selection field.  MoveWindow(..., 24) therefore collapses the popup even
// though all CB_ADDSTRING entries remain in the control.  CB_SETMINVISIBLE
// only works with the v6 common-controls implementation and is not a safe
// replacement for a proper height.  Always retain a usable native popup
// height when the toolbar lays out or temporarily hides these controls.
func prestackComboPopupHeight(id, fieldHeight int) int {
	rows := 4
	switch id {
	case IDPRESTACK_KIND:
		rows = 5
	case IDPRESTACK_KEY:
		rows = 24
	case IDPRESTACK_SORT:
		rows = 4
	case IDPRESTACK_AXIS, IDPRESTACK_DISPLAY:
		rows = 2
	case IDPRESTACK_RAW_SAMPLE_MODE:
		rows = 3
	case IDPRESTACK_PALETTE:
		rows = len(paletteNames)
	default:
		return fieldHeight
	}
	return maxInt(fieldHeight, 26) + rows*20 + 4
}

func prestackComboLayoutHeight(c uintptr, fieldHeight int) int {
	for _, entry := range []struct {
		handle uintptr
		id     int
	}{{prestackUI.kind, IDPRESTACK_KIND}, {prestackUI.key, IDPRESTACK_KEY},
		{prestackUI.sort, IDPRESTACK_SORT}, {prestackUI.axis, IDPRESTACK_AXIS},
		{prestackUI.display, IDPRESTACK_DISPLAY}, {prestackUI.rawSampleMode, IDPRESTACK_RAW_SAMPLE_MODE},
		{prestackUI.palette, IDPRESTACK_PALETTE}} {
		if c != 0 && c == entry.handle {
			return prestackComboPopupHeight(entry.id, fieldHeight)
		}
	}
	return fieldHeight
}
func configurePrestackKeyCombo() {
	if prestackUI.key == 0 {
		return
	}
	// Keep the complete key set in the native combo.  The popup remains a
	// normal scrollable list (rather than being clipped to the first handful
	// of entries), with enough rows for practical browsing and a wider drop
	// area for long XY/offset labels.
	pSendMessageW.Call(prestackUI.key, CB_SETMINVISIBLE, 24, 0)
	pSendMessageW.Call(prestackUI.key, CB_SETDROPPEDWIDTH, 310, 0)
}
func prestackButton(id int, label string) uintptr {
	return createCtrl(prestackHwnd, "BUTTON", label, WS_CHILD|WS_TABSTOP|BS_PUSHBUTTON, 0, 0, 80, 24, id)
}
func createPrestackControls() {
	u := &prestackUI
	*u = prestackControls{}
	u.home = prestackButton(IDPRESTACK_HOME, "首页")
	u.open = prestackButton(IDPRESTACK_OPEN, "打开 SEG-Y")
	u.path = createCtrl(prestackHwnd, "STATIC", "", WS_CHILD, 0, 0, 800, 24, 0)
	u.status = createCtrl(prestackHwnd, "STATIC", "", WS_CHILD, 0, 0, 900, 24, 0)
	u.progress = createCtrl(prestackHwnd, "msctls_progress32", "", WS_CHILD, 0, 0, 160, 18, 0)
	u.progressLabel = createCtrl(prestackHwnd, "STATIC", "准备中", WS_CHILD|WS_BORDER|prestackSSCenter, 0, 0, 160, 18, 0)
	pSendMessageW.Call(u.progress, PBM_SETRANGE, 0, uintptr(uint32(100)<<16))
	u.tabs = createCtrl(prestackHwnd, "SysTabControl32", "", WS_CHILD|WS_TABSTOP, 0, 0, 900, 28, IDPRESTACK_TABS)
	for i, label := range []string{"道集 Gather", "几何 Geometry", "道头映射 Mapping / Header", "对比 Compare"} {
		item := TCITEM{Mask: TCIF_TEXT, PszText: u16(label)}
		pSendMessageW.Call(u.tabs, TCM_INSERTITEMW, uintptr(i), uintptr(unsafe.Pointer(&item)))
	}
	u.kind = prestackCombo(IDPRESTACK_KIND, []string{"CMP / Bin", "Shot / 炮集", "Receiver / 检波点", "Common Offset / 共Offset", "原始叠前道序"})
	u.key = prestackCombo(IDPRESTACK_KEY, nil)
	u.prev = prestackButton(IDPRESTACK_PREV, "上一组")
	u.next = prestackButton(IDPRESTACK_NEXT, "下一组")
	u.sort = prestackCombo(IDPRESTACK_SORT, []string{"原始道序", "Offset", "|Offset|", "Azimuth"})
	u.axis = prestackCombo(IDPRESTACK_AXIS, []string{"Trace 横轴", "Offset 横轴"})
	u.display = prestackCombo(IDPRESTACK_DISPLAY, []string{"图像 Image", "波形 Wiggle"})
	u.offsetBinLabel = createCtrl(prestackHwnd, "STATIC", "分箱宽度", WS_CHILD, 0, 0, 70, 22, 0)
	u.offsetBin = createCtrl(prestackHwnd, "EDIT", "20", WS_CHILD|WS_BORDER|WS_TABSTOP|ES_AUTOHSCROLL, 0, 0, 68, 24, IDPRESTACK_OFFSET_BIN)
	u.offsetBinApply = prestackButton(IDPRESTACK_OFFSET_BIN_APPLY, "应用分箱")
	u.rawTraceStartLabel = createCtrl(prestackHwnd, "STATIC", "起始道", WS_CHILD, 0, 0, 52, 22, 0)
	u.rawTraceStart = createCtrl(prestackHwnd, "EDIT", "1", WS_CHILD|WS_BORDER|WS_TABSTOP|ES_AUTOHSCROLL, 0, 0, 78, 24, IDPRESTACK_RAW_TRACE_START)
	u.rawTraceEndLabel = createCtrl(prestackHwnd, "STATIC", "结束道", WS_CHILD, 0, 0, 52, 22, 0)
	u.rawTraceEnd = createCtrl(prestackHwnd, "EDIT", "0", WS_CHILD|WS_BORDER|WS_TABSTOP|ES_AUTOHSCROLL, 0, 0, 78, 24, IDPRESTACK_RAW_TRACE_END)
	u.rawSampleMode = prestackCombo(IDPRESTACK_RAW_SAMPLE_MODE, []string{"完整记录", "样点范围", "毫秒范围"})
	u.rawSampleStartLabel = createCtrl(prestackHwnd, "STATIC", "样点/毫秒起", WS_CHILD, 0, 0, 82, 22, 0)
	u.rawSampleStart = createCtrl(prestackHwnd, "EDIT", "1", WS_CHILD|WS_BORDER|WS_TABSTOP|ES_AUTOHSCROLL, 0, 0, 78, 24, IDPRESTACK_RAW_SAMPLE_START)
	u.rawSampleEndLabel = createCtrl(prestackHwnd, "STATIC", "止", WS_CHILD, 0, 0, 20, 22, 0)
	u.rawSampleEnd = createCtrl(prestackHwnd, "EDIT", "0", WS_CHILD|WS_BORDER|WS_TABSTOP|ES_AUTOHSCROLL, 0, 0, 78, 24, IDPRESTACK_RAW_SAMPLE_END)
	u.rawApply = prestackButton(IDPRESTACK_RAW_APPLY, "应用范围")
	u.rawAll = prestackButton(IDPRESTACK_RAW_ALL, "全部范围")
	u.palette = prestackCombo(IDPRESTACK_PALETTE, paletteNames[:])
	u.gainMinus = prestackButton(IDPRESTACK_GAINMINUS, "增益−")
	u.gain = createCtrl(prestackHwnd, "STATIC", "0%", WS_CHILD, 0, 0, 40, 20, 0)
	u.gainPlus = prestackButton(IDPRESTACK_GAINPLUS, "增益+")
	u.agc = createCtrl(prestackHwnd, "BUTTON", "AGC", WS_CHILD|WS_TABSTOP|BS_AUTOCHECKBOX, 0, 0, 58, 24, IDPRESTACK_AGC)
	u.reset = prestackButton(IDPRESTACK_RESET, "剖面复位")
	u.headers = prestackButton(IDPRESTACK_HEADERS, "卷头 / 道头")
	for i, label := range []string{"Source", "Receiver", "Midpoint", "Bin Grid", "Fold"} {
		u.layers[i] = createCtrl(prestackHwnd, "BUTTON", label, WS_CHILD|WS_TABSTOP|BS_AUTOCHECKBOX, 0, 0, 100, 24, IDPRESTACK_LAYER_SOURCE+i)
	}
	u.mapReset = prestackButton(IDPRESTACK_MAPRESET, "总图复位")
	u.mappingAuto = prestackButton(IDPRESTACK_MAPPING_AUTO, "自动检测")
	u.mappingApply = prestackButton(IDPRESTACK_MAPPING_APPLY, "确认映射并建立索引")
	u.mappingText = createCtrl(prestackHwnd, "EDIT", "", WS_CHILD|WS_BORDER|WS_VSCROLL|ES_READONLY|traceESMultiline|traceESAutoVScroll, 0, 0, 700, 300, 0)
	configurePrestackKeyCombo()
	for i, f := range prestackMappingFields(&prestackState.mapping) {
		u.mappingLabels = append(u.mappingLabels, createCtrl(prestackHwnd, "STATIC", f.label+" byte", WS_CHILD, 0, 0, 190, 22, 0))
		u.mappingEdits = append(u.mappingEdits, createCtrl(prestackHwnd, "EDIT", "", WS_CHILD|WS_BORDER|WS_TABSTOP|ES_AUTOHSCROLL, 0, 0, 80, 24, IDPRESTACK_MAPBYTE+i))
	}
	layoutPrestackControls()
}

type prestackMappingField struct {
	label string
	value *int
}

func prestackMappingFields(m *prestackcore.HeaderMapping) []prestackMappingField {
	return []prestackMappingField{{"Shot / Source ID", &m.SourceIDByte}, {"Receiver ID", &m.ReceiverIDByte}, {"CDP / Ensemble", &m.CDPByte}, {"Offset", &m.OffsetByte},
		{"Source X", &m.SourceXByte}, {"Source Y", &m.SourceYByte}, {"Receiver X", &m.ReceiverXByte}, {"Receiver Y", &m.ReceiverYByte},
		{"CDP X", &m.CDPXByte}, {"CDP Y", &m.CDPYByte}, {"Inline", &m.InlineByte}, {"Crossline", &m.CrosslineByte}, {"Coordinate scalar", &m.ScalarByte}, {"Coordinate units", &m.UnitsByte}}
}
func populatePrestackMapping() {
	for i, f := range prestackMappingFields(&prestackState.mapping) {
		setText(prestackUI.mappingEdits[i], strconv.Itoa(*f.value))
	}
}
func resetPrestackControls() {
	setPrestackProgress(false, "准备中")
	pSendMessageW.Call(prestackUI.kind, CB_SETCURSEL, 0, 0)
	pSendMessageW.Call(prestackUI.sort, CB_SETCURSEL, 0, 0)
	pSendMessageW.Call(prestackUI.axis, CB_SETCURSEL, 0, 0)
	pSendMessageW.Call(prestackUI.display, CB_SETCURSEL, 0, 0)
	pSendMessageW.Call(prestackUI.palette, CB_SETCURSEL, 2, 0)
	prestackState.offsetBinSize = prestackcore.DefaultOffsetBinSize
	prestackState.selection.OffsetBinSize = prestackState.offsetBinSize
	setText(prestackUI.offsetBin, formatPrestackOffsetBin(prestackState.offsetBinSize))
	pSendMessageW.Call(prestackUI.agc, BM_SETCHECK, 0, 0)
	for _, h := range prestackUI.layers {
		pSendMessageW.Call(h, BM_SETCHECK, BST_CHECKED, 0)
	}
	setText(prestackUI.gain, "0%")
	setRawRangeControlDefaults()
	setPrestackPage(0)
}
func formatPrestackOffsetBin(v float64) string {
	if v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		v = prestackcore.DefaultOffsetBinSize
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}
func prestackGatherKindLabel(kind prestackcore.GatherType) string {
	switch kind {
	case prestackcore.GatherCMP:
		return "CMP"
	case prestackcore.GatherShot:
		return "Shot"
	case prestackcore.GatherReceiver:
		return "Receiver"
	case prestackcore.GatherOffset:
		return "Common Offset"
	case prestackcore.GatherRaw:
		return "原始叠前道序"
	default:
		return "Gather"
	}
}

// prependPrestackAllRange keeps the complete acquisition range available in
// every keyed gather mode. The synthetic key is deliberately kept in the UI
// list only; the metadata index continues to expose compact real keys.
func prependPrestackAllRange(keys []prestackcore.GatherKey) []prestackcore.GatherKey {
	out := make([]prestackcore.GatherKey, len(keys)+1)
	out[0] = prestackcore.GatherKey{All: true}
	copy(out[1:], keys)
	return out
}

func setRawRangeControlDefaults() {
	if prestackState.dataset == nil {
		return
	}
	setText(prestackUI.rawTraceStart, "1")
	setText(prestackUI.rawTraceEnd, strconv.FormatInt(prestackState.dataset.Metadata.TraceCount, 10))
	setText(prestackUI.rawSampleStart, "1")
	setText(prestackUI.rawSampleEnd, strconv.Itoa(prestackState.dataset.Metadata.SamplesPerTrace))
	prestackState.rawSampleModeValue = 0
	pSendMessageW.Call(prestackUI.rawSampleMode, CB_SETCURSEL, 0, 0)
}

func parseRawInt(text string, fallback int64) (int64, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return fallback, nil
	}
	v, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, err
	}
	return v, nil
}

func applyPrestackRawRange() {
	if prestackState.index == nil || prestackState.dataset == nil {
		return
	}
	total := prestackState.dataset.Metadata.TraceCount
	start, err := parseRawInt(getText(prestackUI.rawTraceStart), 1)
	if err != nil {
		message(prestackHwnd, "原始叠前道序", "起始道和结束道必须是整数。", MB_OK|MB_ICONERROR)
		return
	}
	end, err := parseRawInt(getText(prestackUI.rawTraceEnd), total)
	if err != nil {
		message(prestackHwnd, "原始叠前道序", "起始道和结束道必须是整数。", MB_OK|MB_ICONERROR)
		return
	}
	// UI endpoints are 1-based and inclusive; normalize them before converting
	// to the core's zero-based half-open interval so reversed input keeps both
	// boundary traces (8..1 becomes [0,8), not [1,7)).
	if start < 1 {
		start = 1
	}
	if end <= 0 {
		end = total
	}
	lo, hi := start, end
	if lo > hi {
		lo, hi = hi, lo
	}
	start, end, ok := prestackcore.NormalizeRawTraceRange(total, lo-1, hi)
	if !ok {
		message(prestackHwnd, "原始叠前道序", "道号范围为空。", MB_OK|MB_ICONERROR)
		return
	}
	mode, _, _ := pSendMessageW.Call(prestackUI.rawSampleMode, CB_GETCURSEL, 0, 0)
	prestackState.rawSampleModeValue = int(mode)
	ns := prestackState.dataset.Metadata.SamplesPerTrace
	s0, s1 := 0, ns-1
	if prestackState.rawSampleModeValue != 0 {
		v0, e0 := parseRawInt(getText(prestackUI.rawSampleStart), 1)
		v1, e1 := parseRawInt(getText(prestackUI.rawSampleEnd), int64(ns))
		if e0 != nil || e1 != nil {
			message(prestackHwnd, "原始叠前道序", "样点或毫秒范围必须是整数。", MB_OK|MB_ICONERROR)
			return
		}
		if prestackState.rawSampleModeValue == 2 {
			// The dataset metadata has no file-level delay field.  SEG-Y trace
			// delay is normally zero for this viewer; convert milliseconds using
			// the declared sample interval and clamp to valid samples.
			step := float64(maxInt(1, prestackState.dataset.Metadata.SampleIntervalUS)) / 1000
			v0 = int64(math.Round(float64(v0) / step))
			v1 = int64(math.Round(float64(v1) / step))
		}
		if v0 > v1 {
			v0, v1 = v1, v0
		}
		if prestackState.rawSampleModeValue == 1 {
			v0--
			v1--
		}
		s0 = clampInt(int(v0), 0, maxInt(0, ns-1))
		s1 = clampInt(int(v1), 0, maxInt(0, ns-1))
		if s0 > s1 {
			s0, s1 = s1, s0
		}
	}
	selection := prestackcore.GatherSelection{Type: prestackcore.GatherRaw, Key: prestackcore.GatherKey{Raw: true}, Sort: prestackcore.SortPhysical, Axis: prestackcore.AxisTrace,
		RawTraceStart: start, RawTraceEnd: end, SampleStart: s0, SampleEnd: s1}
	result, e := prestackState.index.Gather(selection)
	if e != nil {
		setPrestackStatus("原始叠前道序：" + e.Error())
		return
	}
	prestackState.selection = result.Selection
	prestackState.gather = result
	prestackState.keys = []prestackcore.GatherKey{{Raw: true}}
	prestackState.keyIndex = 0
	prestackState.viewFirst, prestackState.viewLast = 0, len(result.TraceIndices)-1
	prestackState.sampleFirst, prestackState.sampleLast = s0, s1
	setText(prestackUI.rawTraceStart, strconv.FormatInt(start+1, 10))
	setText(prestackUI.rawTraceEnd, strconv.FormatInt(end, 10))
	if prestackState.rawSampleModeValue != 0 {
		setText(prestackUI.rawSampleStart, strconv.Itoa(s0+1))
		setText(prestackUI.rawSampleEnd, strconv.Itoa(s1+1))
	}
	clearPrestackImage()
	startPrestackRender()
}
func setPrestackPage(page int) {
	prestackState.page = page
	if page != 0 {
		// A delayed resize flush belongs only to the Gather scene.  Do not let
		// it leak into a later tab switch as a stale placeholder state.
		prestackState.resizePending = false
	}
	pSendMessageW.Call(prestackUI.tabs, TCM_FIRST+12, uintptr(page), 0)
	layoutPrestackControls()
	if page == 0 && prestackState.index != nil && len(prestackState.indices) == 0 && !prestackState.rendering && !prestackState.resizeActive && !prestackState.resizePending {
		startPrestackRender()
	}
	invalidatePrestackScene()
}
func layoutPrestackControls() {
	if prestackHwnd == 0 {
		return
	}
	r := clientRect(prestackHwnd)
	w, h := int(r.Right), int(r.Bottom)
	u := &prestackUI
	place := func(c uintptr, x, y, ww, hh int, visible bool) {
		if c == 0 {
			return
		}
		height := prestackComboLayoutHeight(c, maxInt(1, hh))
		pMoveWindow.Call(c, uintptr(x), uintptr(y), uintptr(maxInt(1, ww)), uintptr(height), 0)
		show := SW_HIDE
		if visible {
			show = SW_SHOW
		}
		pShowWindow.Call(c, uintptr(show))
	}
	place(u.home, 10, 8, 54, 25, true)
	place(u.open, 70, 8, 98, 25, true)
	place(u.path, 180, 12, w-190, 21, true)
	place(u.tabs, 10, 41, w-20, 28, true)
	place(u.status, 10, h-27, w-195, 23, true)
	place(u.progress, w-177, h-23, 165, 16, !prestackState.progressDone)
	place(u.progressLabel, w-177, h-23, 165, 18, prestackState.progressDone)
	g := prestackState.page == 0
	isRaw := g && prestackState.selection.Type == prestackcore.GatherRaw
	x, y := 10, 76
	// Raw file-order mode uses the same two-row toolbar as the other gather
	// modes.  Keep the range controls on row one and the common display
	// controls on row two; do not let the hidden gather-key controls take part
	// in the wrapping calculation (doing so used to push palette/gain onto a
	// third row and moved the scene down).
	if isRaw {
		// Controls that only make sense for keyed gathers.
		for _, c := range []uintptr{u.prev, u.key, u.next, u.sort, u.axis, u.offsetBinLabel, u.offsetBin, u.offsetBinApply} {
			place(c, 0, 0, 1, 1, false)
		}

		// Row one: mode and raw trace/sample range.
		place(u.kind, x, y, 158, 24, true)
		x += 164
		place(u.rawTraceStartLabel, x, y+2, 52, 22, true)
		x += 56
		place(u.rawTraceStart, x, y, 78, 24, true)
		x += 84
		place(u.rawTraceEndLabel, x, y+2, 52, 22, true)
		x += 56
		place(u.rawTraceEnd, x, y, 78, 24, true)
		x += 84
		place(u.rawSampleMode, x, y, 110, 24, true)
		x += 116
		place(u.rawSampleStartLabel, x, y+2, 82, 22, true)
		x += 86
		place(u.rawSampleStart, x, y, 78, 24, true)
		x += 84
		place(u.rawSampleEndLabel, x, y+2, 20, 22, true)
		x += 24
		place(u.rawSampleEnd, x, y, 78, 24, true)
		x += 84
		place(u.rawApply, x, y, 80, 24, true)
		x += 86
		place(u.rawAll, x, y, 80, 24, true)

		// Row two: exactly the same common display controls used by gathers.
		x, y = 10, 107
		place(u.display, x, y, 112, 24, true)
		x += 118
		place(u.reset, x, y, 86, 25, true)
		x += 92
		place(u.headers, x, y, 100, 25, true)
		x += 106
		place(u.palette, x, y, 145, 24, true)
		x += 151
		place(u.gainMinus, x, y, 58, 24, true)
		x += 64
		place(u.gain, x, y+2, 40, 20, true)
		x += 46
		place(u.gainPlus, x, y, 58, 24, true)
		x += 64
		place(u.agc, x, y, 66, 24, true)
	} else {
		// Keep keyed gathers on the same deliberate two-row grid.  The old
		// wrapping loop counted hidden raw controls when calculating x, which
		// pushed palette/gain off the right edge and made the toolbar look
		// randomly staggered after switching modes.
		for _, c := range []uintptr{u.rawTraceStartLabel, u.rawTraceStart, u.rawTraceEndLabel, u.rawTraceEnd,
			u.rawSampleMode, u.rawSampleStartLabel, u.rawSampleStart, u.rawSampleEndLabel, u.rawSampleEnd, u.rawApply, u.rawAll} {
			place(c, 0, 0, 1, 1, false)
		}
		// First row: gather type, navigation, key, sort and horizontal axis.
		x, y = 10, 76
		place(u.kind, x, y, 150, 24, g)
		x += 156
		place(u.prev, x, y, 64, 25, g)
		x += 70
		place(u.key, x, y, 190, 24, g)
		x += 196
		place(u.next, x, y, 64, 25, g)
		x += 70
		place(u.sort, x, y, 104, 24, g)
		x += 110
		place(u.axis, x, y, 110, 24, g)
		// Second row: display, reset, headers, palette, gain and AGC.
		x, y = 10, 107
		place(u.display, x, y, 112, 24, g)
		x += 118
		place(u.reset, x, y, 86, 25, g)
		x += 92
		place(u.headers, x, y, 100, 25, g)
		x += 106
		place(u.palette, x, y, 145, 24, g)
		x += 151
		place(u.gainMinus, x, y, 58, 24, g)
		x += 64
		place(u.gain, x, y+2, 40, 20, g)
		x += 46
		place(u.gainPlus, x, y, 58, 24, g)
		x += 64
		place(u.agc, x, y, 66, 24, g)
	}
	// Common Offset controls are shown only for the corresponding gather
	// type.  They live on the second toolbar row so changing the type never
	// moves the image or mapping panels.
	isOffset := prestackState.selection.Type == prestackcore.GatherOffset
	place(u.offsetBinLabel, x, y+2, 68, 22, g && isOffset)
	x += 74
	place(u.offsetBin, x, y, 72, 24, g && isOffset)
	x += 78
	place(u.offsetBinApply, x, y, 74, 24, g && isOffset)
	m := prestackState.page == 1
	for i, c := range u.layers {
		place(c, 10+i*110, 76, 104, 25, m)
	}
	place(u.mapReset, 565, 76, 94, 25, m)
	mapPage := prestackState.page == 2
	for i := range u.mappingEdits {
		col, row := i/7, i%7
		xx := 18 + col*300
		yy := 84 + row*31
		place(u.mappingLabels[i], xx, yy+3, 182, 22, mapPage)
		place(u.mappingEdits[i], xx+185, yy, 88, 24, mapPage)
	}
	place(u.mappingAuto, 18, 311, 104, 27, mapPage)
	place(u.mappingApply, 130, 311, 205, 27, mapPage)
	place(u.mappingText, 18, 349, w-36, h-389, mapPage)
}
func prestackSceneRect() RECT {
	r := clientRect(prestackHwnd)
	top := int32(119)
	if prestackState.page == 0 {
		// Gather and raw file-order modes both use exactly two toolbar rows.
		// Keep the scene anchor stable on narrow windows as well; wrapping the
		// hidden/key controls used to push this down to a third row and made the
		// raw mode appear to have a different layout.
		top = 156
	}
	return RECT{Left: 62, Top: top, Right: maxInt32(63, r.Right-24), Bottom: maxInt32(top+1, r.Bottom-62)}
}
func maxInt32(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}
func invalidatePrestackScene() {
	if prestackHwnd == 0 {
		return
	}
	r := clientRect(prestackHwnd)
	r.Top = 70
	r.Bottom -= 29
	pInvalidateRect.Call(prestackHwnd, uintptr(unsafe.Pointer(&r)), 0)
}
func setPrestackStatus(s string) { setText(prestackUI.status, s) }

// Rebuilding a gather may fill thousands of keys, so queue that work after
// the native popup's accepted selection/close notification.
func prestackComboSelection(code int) bool {
	// CBN_SELENDOK is the unambiguous "accepted" notification.  Keep
	// CBN_CLOSEUP as a compatibility fallback (older common-controls builds do
	// not always send SELENDOK for keyboard selection), but callers must post
	// WM_PRESTACK_COMBO_COMMIT instead of rebuilding synchronously.
	return code == CBN_SELENDOK || code == CBN_CLOSEUP
}

// A combo's drop list is an owned popup, but Windows versions differ on
// whether keyboard messages are targeted at that popup or at its COMBOBOX
// owner.  The process-wide shortcut router must stay out of either case while
// a list is open so Escape/arrows reach the native control unchanged.
func prestackComboDropdownOpen() bool {
	for _, combo := range []uintptr{prestackUI.kind, prestackUI.key, prestackUI.sort, prestackUI.axis, prestackUI.display, prestackUI.rawSampleMode, prestackUI.palette} {
		if combo == 0 {
			continue
		}
		open, _, _ := pSendMessageW.Call(combo, CB_GETDROPPEDSTATE, 0, 0)
		if open != 0 {
			return true
		}
	}
	return false
}

func postPrestackComboCommit(id int) {
	if prestackHwnd != 0 {
		pPostMessageW.Call(prestackHwnd, WM_PRESTACK_COMBO_COMMIT, uintptr(id), 0)
	}
}

// commitPrestackCombo runs outside the native notification call stack so the
// relatively expensive gather/list work does not block a popup transition.
func commitPrestackCombo(id int) {
	switch id {
	case IDPRESTACK_KIND:
		v, _, _ := pSendMessageW.Call(prestackUI.kind, CB_GETCURSEL, 0, 0)
		kind := prestackcore.GatherType(v)
		if kind != prestackState.selection.Type {
			selectPrestackGatherType(kind)
		}
	case IDPRESTACK_RAW_SAMPLE_MODE:
		if prestackState.selection.Type == prestackcore.GatherRaw {
			v, _, _ := pSendMessageW.Call(prestackUI.rawSampleMode, CB_GETCURSEL, 0, 0)
			if int(v) != prestackState.rawSampleModeValue {
				applyPrestackRawRange()
			}
		}
	case IDPRESTACK_KEY:
		v, _, _ := pSendMessageW.Call(prestackUI.key, CB_GETCURSEL, 0, 0)
		if int(v) != prestackState.keyIndex {
			selectPrestackGather(int(v))
		}
	case IDPRESTACK_SORT:
		v, _, _ := pSendMessageW.Call(prestackUI.sort, CB_GETCURSEL, 0, 0)
		mode := prestackcore.SortMode(v)
		if mode == prestackState.selection.Sort {
			return
		}
		prestackState.selection.Sort = mode
		if prestackState.selection.Axis == prestackcore.AxisOffset && v != 1 {
			prestackState.selection.Axis = prestackcore.AxisTrace
			pSendMessageW.Call(prestackUI.axis, CB_SETCURSEL, 0, 0)
		}
		selectPrestackGather(prestackState.keyIndex)
	case IDPRESTACK_AXIS:
		v, _, _ := pSendMessageW.Call(prestackUI.axis, CB_GETCURSEL, 0, 0)
		axis := prestackcore.AxisMode(v)
		if axis == prestackState.selection.Axis {
			return
		}
		prestackState.selection.Axis = axis
		if v == 1 {
			prestackState.selection.Sort = prestackcore.SortOffset
			pSendMessageW.Call(prestackUI.sort, CB_SETCURSEL, 1, 0)
		}
		selectPrestackGather(prestackState.keyIndex)
	case IDPRESTACK_DISPLAY:
		v, _, _ := pSendMessageW.Call(prestackUI.display, CB_GETCURSEL, 0, 0)
		if int(v) == prestackState.display {
			return
		}
		prestackState.display = int(v)
		startPrestackRender()
	case IDPRESTACK_PALETTE:
		v, _, _ := pSendMessageW.Call(prestackUI.palette, CB_GETCURSEL, 0, 0)
		if int(v) == prestackState.palette {
			return
		}
		prestackState.palette = int(v)
		prestackState.bgra = crookedPaletteBGRA(prestackState.indices, int(v))
		invalidatePrestackScene()
	}
}

// setPrestackProgress keeps the native progress bar for active work and
// replaces it with a stable text badge once the current index/gather is
// complete.  A filled progress control otherwise looks as if it is still
// loading, especially when it remains at the bottom-right of a maximized
// window.
func setPrestackProgress(done bool, label string) {
	prestackState.progressDone = done
	if prestackUI.progressLabel != 0 {
		setText(prestackUI.progressLabel, label)
		pShowWindow.Call(prestackUI.progressLabel, uintptr(map[bool]int{true: SW_SHOW, false: SW_HIDE}[done]))
	}
	if prestackUI.progress != 0 {
		pShowWindow.Call(prestackUI.progress, uintptr(map[bool]int{true: SW_HIDE, false: SW_SHOW}[done]))
	}
	if done && prestackUI.progress != 0 {
		pSendMessageW.Call(prestackUI.progress, PBM_SETPOS, 100, 0)
	}
}

func cancelPrestackJobs() {
	if prestackState.indexCancel != nil {
		prestackState.indexCancel()
		prestackState.indexCancel = nil
	}
	if prestackState.renderCancel != nil {
		prestackState.renderCancel()
		prestackState.renderCancel = nil
	}
	prestackState.indexGeneration = atomic.AddInt64(&prestackGeneration, 1)
	prestackState.renderGeneration = atomic.AddInt64(&prestackGeneration, 1)
	prestackState.loading, prestackState.rendering = false, false
	prestackDeliveryMu.Lock()
	prestackPendingIndex = nil
	prestackPendingRender = nil
	prestackDeliveryMu.Unlock()
}
func startPrestackIndex(detect bool) {
	if prestackState.dataset == nil {
		return
	}
	cancelPrestackJobs()
	ctx, cancel := context.WithCancel(context.Background())
	prestackState.indexCancel = cancel
	gen := atomic.AddInt64(&prestackGeneration, 1)
	prestackState.indexGeneration = gen
	prestackState.loading = true
	prestackState.needsIndex = false
	data, mapping, owner := prestackState.dataset, prestackState.mapping, prestackHwnd
	prestackState.index = nil
	prestackState.indices = nil
	prestackState.bgra = nil
	prestackState.gather = prestackcore.GatherResult{}
	setPrestackStatus("正在检查道头映射；仅扫描道头，不读取全文件振幅…")
	setPrestackProgress(false, "扫描道头")
	pSendMessageW.Call(prestackUI.progress, PBM_SETPOS, 0, 0)
	invalidatePrestackScene()
	go func() {
		result := &prestackIndexResult{generation: gen}
		reader, err := data.OpenReader()
		if err != nil {
			result.err = err
		} else {
			defer reader.Close()
			if detect {
				result.detected = true
				result.detection, err = prestackcore.DetectHeaderMapping(ctx, reader)
				mapping = result.detection.Mapping
			}
			if err == nil && (!detect || !result.detection.NeedsConfirmation) {
				last := time.Time{}
				result.index, err = prestackcore.BuildIndex(ctx, reader, mapping, func(p prestackcore.IndexProgress) {
					if time.Since(last) < 50*time.Millisecond && p.Done < p.Total {
						return
					}
					last = time.Now()
					pct := int64(0)
					if p.Total > 0 {
						pct = p.Done * 100 / p.Total
					}
					atomic.StoreInt64(&prestackProgress, pct)
					if ctx.Err() == nil {
						pPostMessageW.Call(owner, WM_PRESTACK_PROGRESS, uintptr(gen), 0)
					}
				})
			}
			result.err = err
		}
		if ctx.Err() != nil {
			return
		}
		prestackDeliveryMu.Lock()
		defer prestackDeliveryMu.Unlock()
		if prestackDeliveryHwnd != owner || ctx.Err() != nil {
			return
		}
		prestackPendingIndex = result
		posted, _, _ := pPostMessageW.Call(owner, WM_PRESTACK_READY, uintptr(gen), 0)
		if posted == 0 {
			prestackPendingIndex = nil
		}
	}()
}
func receivePrestackIndex(gen int64) {
	prestackDeliveryMu.Lock()
	r := prestackPendingIndex
	if r != nil && r.generation == gen {
		prestackPendingIndex = nil
	} else {
		r = nil
	}
	prestackDeliveryMu.Unlock()
	if r == nil || gen != prestackState.indexGeneration {
		return
	}
	prestackState.loading = false
	if r.err != nil {
		setPrestackProgress(true, "加载失败")
		setPrestackStatus("叠前索引失败：" + r.err.Error())
		return
	}
	if r.detected {
		prestackState.detection = r.detection
		prestackState.mapping = r.detection.Mapping
		populatePrestackMapping()
	}
	var summary strings.Builder
	summary.WriteString("SEG-Y 字节为 1-based。0 表示禁用可选字段。Offset 不应用坐标比例因子。\r\n")
	summary.WriteString(fmt.Sprintf("自动检测置信度 %.1f%%\r\n", 100*prestackState.detection.Confidence))
	for _, c := range prestackState.detection.Candidates {
		fmt.Fprintf(&summary, "%s | byte %d | 覆盖 %.1f%% | 唯一值 %d | 置信度 %.1f%% | %s\r\n", c.Field, c.Byte, c.Coverage*100, c.UniqueCount, c.Confidence*100, c.Explanation)
	}
	for _, s := range prestackState.detection.Warnings {
		summary.WriteString("警告：" + s + "\r\n")
	}
	if r.index == nil {
		summary.WriteString("\r\n请确认或修改映射后点击“确认映射并建立索引”。不自动把低置信度字段当作正确几何。")
		setText(prestackUI.mappingText, summary.String())
		setPrestackProgress(true, "需确认映射")
		setPrestackPage(2)
		setPrestackStatus("道头映射需要确认。请查看候选、覆盖率及警告。")
		return
	}
	prestackState.index = r.index
	prestackState.mapBounds = r.index.Bounds
	for _, s := range r.index.Warnings {
		summary.WriteString("索引警告：" + s + "\r\n")
	}
	setText(prestackUI.mappingText, summary.String())
	pSendMessageW.Call(prestackUI.progress, PBM_SETPOS, 100, 0)
	setPrestackProgress(false, "读取道集")
	setPrestackPage(0)
	selectPrestackGatherType(prestackcore.GatherCMP)
}
func applyPrestackMapping() {
	m := prestackState.mapping
	for i, f := range prestackMappingFields(&m) {
		v, e := strconv.Atoi(strings.TrimSpace(getText(prestackUI.mappingEdits[i])))
		if e != nil {
			message(prestackHwnd, "道头映射", "请输入整数 1-based 字节；可选字段可填 0。", MB_OK|MB_ICONERROR)
			return
		}
		*f.value = v
	}
	if e := m.Validate(); e != nil {
		message(prestackHwnd, "道头映射", e.Error(), MB_OK|MB_ICONERROR)
		return
	}
	prestackState.mapping = m
	startPrestackIndex(false)
}

func selectPrestackGatherType(kind prestackcore.GatherType) {
	if prestackState.index == nil {
		return
	}
	if kind == prestackcore.GatherRaw {
		prestackState.selection.Type = kind
		prestackState.selection.Sort = prestackcore.SortPhysical
		prestackState.selection.Axis = prestackcore.AxisTrace
		prestackState.keys = []prestackcore.GatherKey{{Raw: true}}
		pSendMessageW.Call(prestackUI.key, CB_RESETCONTENT, 0, 0)
		pSendMessageW.Call(prestackUI.key, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16("全部原始道序"))))
		pSendMessageW.Call(prestackUI.key, CB_SETCURSEL, 0, 0)
		setRawRangeControlDefaults()
		layoutPrestackControls()
		applyPrestackRawRange()
		return
	}
	previousType, previousKey := prestackState.selection.Type, prestackState.selection.Key
	prestackState.selection.Type = kind
	if kind == prestackcore.GatherOffset {
		if prestackState.offsetBinSize <= 0 {
			prestackState.offsetBinSize = prestackcore.DefaultOffsetBinSize
		}
		prestackState.selection.OffsetBinSize = prestackState.offsetBinSize
		prestackState.keys = prependPrestackAllRange(prestackState.index.AvailableGathersConfigured(kind, prestackState.offsetBinSize))
	} else {
		prestackState.keys = prependPrestackAllRange(prestackState.index.AvailableGathers(kind))
	}
	// The controls are part of the toolbar and must follow the selected kind
	// immediately, even before the first image arrives.
	layoutPrestackControls()
	pSendMessageW.Call(prestackUI.key, CB_RESETCONTENT, 0, 0)
	configurePrestackKeyCombo()
	best, bestFold, preferred := 0, -1, -1
	for i, k := range prestackState.keys {
		pSendMessageW.Call(prestackUI.key, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(k.String()))))
		g, e := prestackState.index.Gather(prestackcore.GatherSelection{Type: kind, Key: k, OffsetBinSize: prestackState.offsetBinSize})
		if e == nil && len(g.TraceIndices) > bestFold {
			best, bestFold = i, len(g.TraceIndices)
		}
		if kind == prestackcore.GatherOffset && previousType == kind && previousKey.OffsetBin && k.OffsetBin &&
			math.Abs(k.OffsetCenter-previousKey.OffsetCenter) <= math.Max(1e-9, math.Abs(k.OffsetCenter)*1e-12) {
			preferred = i
		}
	}
	if len(prestackState.keys) == 0 {
		prestackState.gather = prestackcore.GatherResult{}
		prestackState.indices = nil
		prestackState.bgra = nil
		if kind == prestackcore.GatherOffset {
			setPrestackStatus(fmt.Sprintf("共Offset分箱：%s，键值 0 组；当前映射没有可用的有效 Offset。", formatPrestackOffsetBin(prestackState.offsetBinSize)))
		} else {
			setPrestackStatus("当前映射没有可用的该类型道集，请检查 Mapping。")
		}
		invalidatePrestackScene()
		return
	}
	if kind == prestackcore.GatherOffset {
		realCount := maxInt(0, len(prestackState.keys)-1) // first item is 全部范围
		setPrestackStatus(fmt.Sprintf("共Offset分箱：%s，全部范围 + %d 个分箱", formatPrestackOffsetBin(prestackState.offsetBinSize), realCount))
	}
	if preferred >= 0 {
		best = preferred
	}
	selectPrestackGather(best)
}

func applyPrestackOffsetBinSize() {
	if prestackState.index == nil {
		return
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(getText(prestackUI.offsetBin)), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 || v > 1e9 {
		message(prestackHwnd, "共Offset分箱", "请输入有限的正数分箱宽度（0.001–1e9）。", MB_OK|MB_ICONERROR)
		setText(prestackUI.offsetBin, formatPrestackOffsetBin(prestackState.offsetBinSize))
		return
	}
	if v < 0.001 {
		message(prestackHwnd, "共Offset分箱", "分箱宽度不能小于 0.001。", MB_OK|MB_ICONERROR)
		setText(prestackUI.offsetBin, formatPrestackOffsetBin(prestackState.offsetBinSize))
		return
	}
	prestackState.offsetBinSize = v
	prestackState.selection.OffsetBinSize = v
	setText(prestackUI.offsetBin, formatPrestackOffsetBin(v))
	if prestackState.selection.Type == prestackcore.GatherOffset {
		selectPrestackGatherType(prestackcore.GatherOffset)
	}
}

// invalidatePrestackRender clears the currently displayed gather before a
// page/key transition.  Rendering is generation guarded, but clearing the
// buffers here also prevents a result from the previous page (Geometry) from
// being painted using the new Gather scene dimensions.
func invalidatePrestackRender() {
	if prestackState.renderCancel != nil {
		prestackState.renderCancel()
		prestackState.renderCancel = nil
	}
	prestackState.renderGeneration = atomic.AddInt64(&prestackGeneration, 1)
	prestackState.rendering = false
	prestackState.indices = nil
	prestackState.bgra = nil
	prestackState.imageWidth = 0
	prestackState.imageHeight = 0
	prestackDeliveryMu.Lock()
	prestackPendingRender = nil
	prestackDeliveryMu.Unlock()
}

// openPrestackGather switches from Geometry to one exact gather atomically.
// In particular it must not call selectPrestackGatherType first: that helper
// chooses the largest-fold gather and starts a render, which used to race the
// subsequent target-gather render and leave black/stale lines on the page.
func openPrestackGather(kind prestackcore.GatherType, key prestackcore.GatherKey) {
	if prestackState.index == nil {
		return
	}
	invalidatePrestackRender()
	prestackState.selection.Type = kind
	prestackState.selection.OffsetBinSize = prestackState.offsetBinSize
	setPrestackPage(0)
	// Keep the visible gather-type control in sync with the exact target. This
	// matters when a Bin is opened from Geometry while the user previously had
	// Shot, Receiver, or Common Offset selected.
	pSendMessageW.Call(prestackUI.kind, CB_SETCURSEL, uintptr(kind), 0)
	if kind == prestackcore.GatherOffset {
		prestackState.keys = prependPrestackAllRange(prestackState.index.AvailableGathersConfigured(kind, prestackState.offsetBinSize))
	} else {
		prestackState.keys = prependPrestackAllRange(prestackState.index.AvailableGathers(kind))
	}
	pSendMessageW.Call(prestackUI.key, CB_RESETCONTENT, 0, 0)
	configurePrestackKeyCombo()
	target := -1
	for i, k := range prestackState.keys {
		pSendMessageW.Call(prestackUI.key, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(k.String()))))
		if k == key {
			target = i
		}
	}
	if target < 0 {
		setPrestackStatus("未找到所选 Bin 对应的 CMP 道集。")
		invalidatePrestackScene()
		return
	}
	// selectPrestackGather performs the single new render after the page and
	// complete key list are ready.
	selectPrestackGather(target)
}

func selectPrestackGather(index int) {
	if prestackState.index == nil || len(prestackState.keys) == 0 {
		return
	}
	if prestackState.selection.Type == prestackcore.GatherRaw {
		applyPrestackRawRange()
		return
	}
	index = clampInt(index, 0, len(prestackState.keys)-1)
	prestackState.keyIndex = index
	prestackState.selection.Key = prestackState.keys[index]
	prestackState.selection.OffsetBinSize = prestackState.offsetBinSize
	pSendMessageW.Call(prestackUI.key, CB_SETCURSEL, uintptr(index), 0)
	result, err := prestackState.index.Gather(prestackState.selection)
	if err != nil {
		setPrestackStatus(err.Error())
		return
	}
	prestackState.gather = result
	prestackState.viewFirst = 0
	prestackState.viewLast = len(result.TraceIndices) - 1
	prestackState.sampleFirst = 0
	prestackState.sampleLast = prestackState.dataset.Metadata.SamplesPerTrace - 1
	prestackState.indices = nil
	prestackState.bgra = nil
	prestackState.imageWidth = 0
	prestackState.imageHeight = 0
	startPrestackRender()
}

func prestackDisplayedTraces() ([]int64, []float64) {
	g := prestackState.gather
	if len(g.TraceIndices) == 0 {
		return nil, nil
	}
	a := clampInt(prestackState.viewFirst, 0, len(g.TraceIndices)-1)
	b := clampInt(prestackState.viewLast, a, len(g.TraceIndices)-1)
	return g.TraceIndices[a : b+1], g.Positions[a : b+1]
}

// Offset display uses nearest actual traces on a physical offset axis. It
// never invents amplitudes or uses a physical SEG-Y trace number as an offset.
func prestackOffsetColumns(traces []int64, positions []float64, width int) []int64 {
	if len(traces) < 2 || len(positions) != len(traces) || positions[len(positions)-1] <= positions[0] {
		return append([]int64(nil), traces...)
	}
	out := make([]int64, width)
	lo, hi := positions[0], positions[len(positions)-1]
	for x := range out {
		v := lo + (hi-lo)*float64(x)/float64(maxInt(1, width-1))
		i := sort.Search(len(positions), func(i int) bool { return positions[i] >= v })
		if i == len(positions) {
			i--
		} else if i > 0 && v-positions[i-1] <= positions[i]-v {
			i--
		}
		out[x] = traces[i]
	}
	return out
}

// Bound the raster's source columns without changing gather membership. The
// returned physical trace list is a deterministic first/middle/last sample,
// preserving acquisition order and avoiding a full-gather float buffer.
func prestackRasterColumns(traces []int64, positions []float64, limit int) ([]int64, []float64) {
	if limit < 2 || len(traces) <= limit {
		return traces, positions
	}
	out := make([]int64, limit)
	pos := make([]float64, 0, limit)
	if len(positions) == len(traces) {
		pos = make([]float64, limit)
	}
	for i := 0; i < limit; i++ {
		j := i * (len(traces) - 1) / (limit - 1)
		out[i] = traces[j]
		if len(pos) == limit {
			pos[i] = positions[j]
		}
	}
	return out, pos
}

// clearPrestackImage drops the last exchanged frame before a page/key/range
// transition.  It is deliberately not called from the native resize path:
// while the user is dragging the border the last complete frame is the best
// preview and avoids the white/black placeholder that used to flash between
// WM_SIZE messages.
func clearPrestackImage() {
	prestackState.indices = nil
	prestackState.bgra = nil
	prestackState.imageWidth = 0
	prestackState.imageHeight = 0
}

// schedulePrestackResizeRender posts one delayed flush for the current resize
// generation.  WM_SIZE can arrive many times per drag; doing work for each
// message both starves the UI and allows an old-size result to win the race.
func schedulePrestackResizeRender(owner uintptr, generation int64) {
	go func() {
		time.Sleep(100 * time.Millisecond)
		if owner == 0 || prestackHwnd != owner {
			return
		}
		pPostMessageW.Call(owner, WM_PRESTACK_RESIZE_FLUSH, uintptr(generation), 0)
	}()
}

func startPrestackRender() {
	// A resize has its own debounce path.  Ignoring incidental toolbar or
	// shortcut-triggered renders while the native border is moving prevents an
	// old-size frame from racing the final client rectangle.
	if prestackState.resizeActive || prestackState.resizePending || prestackState.dataset == nil || prestackState.index == nil {
		return
	}
	traces, positions := prestackDisplayedTraces()
	if len(traces) == 0 {
		return
	}
	if prestackState.renderCancel != nil {
		prestackState.renderCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	prestackState.renderCancel = cancel
	gen := atomic.AddInt64(&prestackGeneration, 1)
	prestackState.renderGeneration = gen
	prestackState.rendering = true
	setPrestackProgress(false, "读取道集")
	// Keep the last complete frame until the replacement is ready.  This is
	// particularly important after a resize: the renderer works at the final
	// client dimensions, while WM_SIZE can deliver dozens of intermediate
	// rectangles.  receivePrestackRender exchanges the frame atomically, so a
	// cancelled/failed render can never leave a partially painted bitmap.
	r := prestackSceneRect()
	w, h := minInt(1536, maxInt(2, int(r.Right-r.Left))), minInt(1536, maxInt(2, int(r.Bottom-r.Top)))
	mode := segy.DisplayAdaptive
	if prestackState.selection.Axis == prestackcore.AxisOffset {
		traces = prestackOffsetColumns(traces, positions, w)
		mode = segy.DisplayNearest
	} else {
		traces, positions = prestackRasterColumns(traces, positions, minInt(w*2, 4096))
	}
	if prestackState.display == 1 {
		w = minInt(len(traces), minInt(192, maxInt(2, w/5)))
		mode = segy.DisplayNearest
	}
	data, owner, palette := prestackState.dataset, prestackHwnd, prestackState.palette
	workspaceGeneration := prestackState.workspaceGeneration
	// Keep the displayed gain value exact.  Previously a hidden +0.5% was
	// added even when the toolbar showed 0%, which percentile-clipped the
	// strongest 0.5% of samples.  On gathers with a few very strong traces
	// this produced conspicuous black vertical bands while dragging/zooming
	// (palette index 0 is the maximum/black end of the default palette).
	options := segy.RenderOptions{Width: w, Height: h, SampleStart: prestackState.sampleFirst, SampleEnd: prestackState.sampleLast,
		AGC: prestackState.agc, ClipPercent: 99, GainPercent: math.Max(0, math.Min(49, prestackState.gain)), DisplayMode: mode, Workers: 2, ReadStrategy: segy.ReadStrategySparseMapped}
	setPrestackStatus(fmt.Sprintf("正在读取道集 %s | %d 道 | 样点 %d..%d", prestackState.selection.Key.String(), len(prestackState.gather.TraceIndices), options.SampleStart+1, options.SampleEnd+1))
	pSendMessageW.Call(prestackUI.progress, PBM_SETPOS, 0, 0)
	invalidatePrestackScene()
	go func() {
		select {
		case prestackRenderSlot <- struct{}{}:
		case <-ctx.Done():
			return
		}
		defer func() { <-prestackRenderSlot }()
		if ctx.Err() != nil {
			return
		}
		result := &prestackRenderResult{generation: gen, workspaceGeneration: workspaceGeneration, width: w, height: h}
		reader, err := data.OpenReader()
		if err == nil {
			result.indices, result.stats, err = reader.RenderTraceIndices(traces, options)
			reader.Close()
		}
		result.err = err
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			result.bgra = crookedPaletteBGRA(result.indices, palette)
		}
		prestackDeliveryMu.Lock()
		defer prestackDeliveryMu.Unlock()
		if prestackDeliveryHwnd != owner || ctx.Err() != nil {
			return
		}
		prestackPendingRender = result
		posted, _, _ := pPostMessageW.Call(owner, WM_PRESTACK_RENDER, uintptr(gen), 0)
		if posted == 0 {
			prestackPendingRender = nil
		}
	}()
}
func receivePrestackRender(gen int64) {
	prestackDeliveryMu.Lock()
	r := prestackPendingRender
	if r != nil && r.generation == gen {
		prestackPendingRender = nil
	} else {
		r = nil
	}
	prestackDeliveryMu.Unlock()
	if r == nil || gen != prestackState.renderGeneration || r.workspaceGeneration != prestackState.workspaceGeneration {
		return
	}
	prestackState.rendering = false
	if r.err != nil {
		setPrestackProgress(true, "加载失败")
		setPrestackStatus("道集显示失败：" + r.err.Error())
		return
	}
	prestackState.indices, prestackState.bgra = r.indices, r.bgra
	prestackState.imageWidth, prestackState.imageHeight = r.width, r.height
	prestackState.stats = r.stats
	pSendMessageW.Call(prestackUI.progress, PBM_SETPOS, 100, 0)
	setPrestackProgress(true, "加载完成")
	offsetLabel := "Offset n/a"
	if prestackState.gather.OffsetRange.Valid {
		offsetLabel = fmt.Sprintf("Offset %.4g..%.4g", prestackState.gather.OffsetRange.Min, prestackState.gather.OffsetRange.Max)
	}
	kindLabel := prestackGatherKindLabel(prestackState.selection.Type)
	keyPosition := ""
	if len(prestackState.keys) > 0 {
		keyPosition = fmt.Sprintf(" | 键值 %d/%d", prestackState.keyIndex+1, len(prestackState.keys))
	}
	gainHint := ""
	if prestackState.gain > 0 {
		gainHint = fmt.Sprintf(" | 增益 %.0f%%（黑色为超出增益范围的峰值）", prestackState.gain)
		observed := prestackState.stats.ObservedMax - prestackState.stats.ObservedMin
		mapped := prestackState.stats.MapMax - prestackState.stats.MapMin
		if mapped > 0 && observed > mapped*10 {
			gainHint += "；检测到极端振幅道，可将增益调为0或启用AGC"
		}
	}
	if prestackState.selection.Type == prestackcore.GatherRaw {
		setPrestackStatus(fmt.Sprintf("完成 | 原始叠前道序 | 道 %d–%d（%d 道） | 样点 %d–%d | %s%s | 左拖缩放，右拖平移，双击复位，Ctrl+单击查看真实道", prestackState.gather.RawTraceStart+1, prestackState.gather.RawTraceEnd, len(prestackState.gather.TraceIndices), prestackState.sampleFirst+1, prestackState.sampleLast+1, offsetLabel, gainHint))
	} else if prestackState.selection.Type == prestackcore.GatherOffset {
		setPrestackStatus(fmt.Sprintf("完成 | %s %s | 实际 %s | %d 道 | Fold %d%s%s | 左拖缩放，右拖平移，双击复位，Ctrl+单击查看真实道", kindLabel, prestackState.selection.Key.String(), offsetLabel, len(prestackState.gather.TraceIndices), len(prestackState.gather.TraceIndices), keyPosition, gainHint))
	} else {
		setPrestackStatus(fmt.Sprintf("完成 | %s %s | %d 道 | Fold %d | %s%s%s | 左拖缩放，右拖平移，双击复位，Ctrl+单击查看真实道", kindLabel, prestackState.selection.Key.String(), len(prestackState.gather.TraceIndices), len(prestackState.gather.TraceIndices), offsetLabel, keyPosition, gainHint))
	}
	invalidatePrestackScene()
}

// changePrestackPalette follows the keyboard behaviour used by the existing
// 2-D/3-D viewers.  Palette changes recolour the already rendered index image
// and therefore do not reopen the SEG-Y reader.
func changePrestackPalette(delta int) {
	if prestackHwnd == 0 || len(paletteNames) == 0 {
		return
	}
	prestackState.palette = (prestackState.palette + delta) % len(paletteNames)
	if prestackState.palette < 0 {
		prestackState.palette += len(paletteNames)
	}
	if prestackUI.palette != 0 {
		pSendMessageW.Call(prestackUI.palette, CB_SETCURSEL, uintptr(prestackState.palette), 0)
	}
	if len(prestackState.indices) != 0 {
		prestackState.bgra = crookedPaletteBGRA(prestackState.indices, prestackState.palette)
	}
	setPrestackStatus(fmt.Sprintf("色标：%s | 增益 %.0f%% | Q/W 调整增益，E/Shift+E 切换色标", paletteNames[prestackState.palette], prestackState.gain))
	invalidatePrestackScene()
}

// handlePrestackShortcut is called by the process message loop so shortcuts
// remain active while a combo box, list or button owns keyboard focus.
func handlePrestackShortcut(key uintptr) bool {
	if prestackHwnd == 0 || prestackState.dataset == nil {
		return false
	}
	switch key {
	case VK_Q:
		prestackState.gain = math.Max(0, math.Min(49, prestackState.gain+1))
		setText(prestackUI.gain, fmt.Sprintf("%.0f%%", prestackState.gain))
		startPrestackRender()
		return true
	case VK_W:
		prestackState.gain = math.Max(0, math.Min(49, prestackState.gain-1))
		setText(prestackUI.gain, fmt.Sprintf("%.0f%%", prestackState.gain))
		startPrestackRender()
		return true
	case VK_E:
		if volumeShiftDown() {
			changePrestackPalette(-1)
		} else {
			changePrestackPalette(1)
		}
		return true
	case VK_R:
		// R is retained as the previous-palette alias used by the legacy 2-D
		// viewer, while Shift+E is the explicit 3-D-compatible reverse key.
		changePrestackPalette(-1)
		return true
	case VK_ESCAPE:
		prestackState.dragging = false
		prestackState.ctrlClick = false
		pReleaseCapture.Call()
		invalidatePrestackScene()
		return true
	}
	return false
}

func prestackPickColumn(x int) int {
	r := prestackSceneRect()
	traces, positions := prestackDisplayedTraces()
	if len(traces) == 0 {
		return -1
	}
	fraction := math.Max(0, math.Min(1, float64(x-int(r.Left))/float64(maxInt(1, int(r.Right-r.Left)))))
	i := int(math.Round(fraction * float64(len(traces)-1)))
	if prestackState.selection.Axis == prestackcore.AxisOffset && len(positions) == len(traces) && len(positions) > 1 {
		v := positions[0] + fraction*(positions[len(positions)-1]-positions[0])
		i = sort.Search(len(positions), func(j int) bool { return positions[j] >= v })
		if i == len(positions) {
			i--
		} else if i > 0 && v-positions[i-1] <= positions[i]-v {
			i--
		}
	}
	return prestackState.viewFirst + i
}
func prestackInspectTrace(x, y int) {
	i := prestackPickColumn(x)
	if i < 0 || prestackState.index == nil {
		return
	}
	trace := prestackState.gather.TraceIndices[i]
	r := prestackState.index.Records[trace]
	scene := prestackSceneRect()
	sample := prestackState.sampleFirst + int(math.Round(float64(clampInt(y-int(scene.Top), 0, int(scene.Bottom-scene.Top)))/float64(maxInt(1, int(scene.Bottom-scene.Top)))*float64(prestackState.sampleLast-prestackState.sampleFirst)))
	context := fmt.Sprintf("叠前 %s | physical trace %d | Shot %d | Receiver %d | CDP %d | Header Offset %.5g | Computed Offset %.5g | Azimuth %.3f | Source (%.6g,%.6g) Receiver (%.6g,%.6g) Midpoint (%.6g,%.6g)", prestackState.selection.Key.String(), trace+1, r.SourceID, r.ReceiverID, r.CDP, r.HeaderOffset, r.ComputedOffset, r.Azimuth, r.SourceX, r.SourceY, r.ReceiverX, r.ReceiverY, r.MidpointX, r.MidpointY)
	showTraceAnalysisSelection(traceAnalysisSelection{Context: context, Targets: []traceAnalysisTarget{{Role: "A", Path: prestackState.dataset.Path, Trace: trace, SampleStart: prestackState.sampleFirst, SampleEnd: prestackState.sampleLast, MarkerSample: sample, Inline: r.Inline, Crossline: r.Crossline, HasGeometry: prestackState.index.UsesGrid}}})
}

func prestackHoverGather(x, y int) {
	if prestackState.index == nil || len(prestackState.gather.TraceIndices) == 0 {
		return
	}
	i := prestackPickColumn(x)
	if i < 0 || i >= len(prestackState.gather.TraceIndices) {
		return
	}
	r := prestackState.index.Records[prestackState.gather.TraceIndices[i]]
	offset := "Offset n/a"
	if r.HasOffset {
		offset = fmt.Sprintf("Offset %.6g", r.Offset)
	}
	if r.HasOffset && r.HasHeaderOffset && r.HasComputedOffset && math.Abs(r.HeaderOffset-r.ComputedOffset) > 1e-9 {
		offset = fmt.Sprintf("Offset %.6g（头 %.6g / 计算 %.6g）", r.Offset, r.HeaderOffset, r.ComputedOffset)
	}
	setPrestackStatus(fmt.Sprintf("叠前 %s %s | physical trace %d | CDP %d | %s | Azimuth %.3f | Ctrl+单击查看道分析", prestackState.selection.Type, prestackState.selection.Key.String(), r.TraceNumber+1, r.CDP, offset, r.Azimuth))
}

func paintPrestack(hdc uintptr) {
	r := clientRect(prestackHwnd)
	w, h := int(r.Right), int(r.Bottom)
	if w < 1 || h < 1 {
		return
	}
	mem, _, _ := pCreateCompatibleDC.Call(hdc)
	bmp, _, _ := pCreateCompatibleBitmap.Call(hdc, uintptr(w), uintptr(h))
	old, _, _ := pSelectObject.Call(mem, bmp)
	defer func() { pSelectObject.Call(mem, old); pDeleteObject.Call(bmp); pDeleteDC.Call(mem) }()
	brush, _, _ := pGetStockObject.Call(WHITE_BRUSH)
	pFillRect.Call(mem, uintptr(unsafe.Pointer(&r)), brush)
	pSetBkMode.Call(mem, TRANSPARENT)
	if hFont != 0 {
		pSelectObject.Call(mem, hFont)
	}
	switch prestackState.page {
	case 0:
		paintPrestackGather(mem)
	case 1:
		paintPrestackGeometry(mem)
	case 3:
		drawAxisText(mem, "叠前 A/B 对比将在后续版本提供。\n本模块只读浏览，不进行 NMO、叠加、插值或写回 SEG-Y。", 40, 125, w-40, h-70, DT_LEFT|DT_WORDBREAK)
	}
	// The destination DC retains WM_PAINT's update and child-window clipping;
	// drawing never repaints toolbar controls or uses their pixels as overlays.
	pBitBlt.Call(hdc, 0, 0, uintptr(w), uintptr(h), mem, 0, 0, SRCCOPY)
}
func paintPrestackGather(hdc uintptr) {
	r := prestackSceneRect()
	if len(prestackState.indices) == 0 {
		if prestackState.resizeActive || prestackState.resizePending {
			// Keep the scene readable during a border drag.  There may not be a
			// last frame yet (for example immediately after opening a gather), so
			// use the same neutral grey used by the 2-D viewer instead of clearing
			// the entire client area to white.  The clip is deliberately limited
			// to the scene rectangle so the toolbar and status bar are untouched.
			saved, _, _ := pSaveDC.Call(hdc)
			pIntersectClipRect.Call(hdc, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right), uintptr(r.Bottom))
			brush, _, _ := pCreateSolidBrush.Call(0x00E6E6E6)
			pFillRect.Call(hdc, uintptr(unsafe.Pointer(&r)), brush)
			pDeleteObject.Call(brush)
			pRestoreDC.Call(hdc, saved)
			drawAxisText(hdc, "调整窗口后刷新道集…", int(r.Left), int(r.Top), int(r.Right), int(r.Bottom), DT_CENTER|DT_VCENTER|DT_SINGLELINE)
			return
		}
		text := "请选择 SEG-Y 并确认道头映射。索引就绪后选择 CMP、Shot、Receiver 或 Common Offset 道集。"
		if prestackState.rendering {
			text = "正在刷新当前道集…"
		}
		drawAxisText(hdc, text, int(r.Left), int(r.Top), int(r.Right), int(r.Bottom), DT_LEFT|DT_WORDBREAK)
		return
	}
	w, h := prestackState.imageWidth, prestackState.imageHeight
	// A stale/partial result must never be passed to StretchDIBits or indexed
	// with a mismatched stride. This can happen briefly while switching from
	// Geometry to Gather or while a cancelled render is being delivered.
	if w <= 0 || h <= 0 || len(prestackState.indices) != w*h ||
		(prestackState.display == 0 && len(prestackState.bgra) != w*h*4) {
		drawAxisText(hdc, "正在准备当前道集…", int(r.Left), int(r.Top), int(r.Right), int(r.Bottom), DT_LEFT|DT_SINGLELINE)
		return
	}
	saved, _, _ := pSaveDC.Call(hdc)
	pIntersectClipRect.Call(hdc, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right), uintptr(r.Bottom))
	if prestackState.display == 0 {
		bmi := BITMAPINFO{Header: BITMAPINFOHEADER{Size: uint32(unsafe.Sizeof(BITMAPINFOHEADER{})), Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32, Compression: BI_RGB}}
		// Match the ordinary 2-D viewer's resize path.  The default
		// COLORONCOLOR mode can leave a dark one-pixel seam when the last
		// complete frame is stretched into a series of intermediate scene
		// rectangles during WM_SIZE.
		pSetStretchBltMode.Call(hdc, HALFTONE)
		pStretchDIBits.Call(hdc, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), 0, 0, uintptr(w), uintptr(h), uintptr(unsafe.Pointer(&prestackState.bgra[0])), uintptr(unsafe.Pointer(&bmi)), DIB_RGB_COLORS, SRCCOPY)
	} else {
		pen, _, _ := pCreatePen.Call(PS_SOLID, 1, 0x00333333)
		old, _, _ := pSelectObject.Call(hdc, pen)
		for x := 0; x < w; x++ {
			cx := float64(r.Left) + float64(x+1)*float64(r.Right-r.Left)/float64(w+1)
			scale := float64(r.Right-r.Left) / float64(w+1) * 0.85
			for y := 0; y < h; y++ {
				px := int(cx + (127.5-float64(prestackState.indices[y*w+x]))/127.5*scale)
				py := int(r.Top) + y*int(r.Bottom-r.Top)/maxInt(1, h-1)
				if y == 0 {
					pMoveToEx.Call(hdc, uintptr(px), uintptr(py), 0)
				} else {
					pLineTo.Call(hdc, uintptr(px), uintptr(py))
				}
			}
		}
		pSelectObject.Call(hdc, old)
		pDeleteObject.Call(pen)
	}
	if prestackState.dragging && !prestackState.panning {
		drawCrookedZoomRectangle(hdc, prestackState.dragX, prestackState.dragY, prestackState.dragCurrentX, prestackState.dragCurrentY)
	}
	pRestoreDC.Call(hdc, saved)
	for j := 0; j <= 4; j++ {
		sample := prestackState.sampleFirst + int(float64(j)*float64(prestackState.sampleLast-prestackState.sampleFirst)/4)
		ms := float64(sample*prestackState.dataset.Metadata.SampleIntervalUS) / 1000
		y := int(r.Top) + j*int(r.Bottom-r.Top)/4
		drawAxisText(hdc, fmt.Sprintf("%.0f ms", ms), 0, y-8, int(r.Left)-5, y+16, DT_RIGHT|DT_SINGLELINE)
	}
	_, positions := prestackDisplayedTraces()
	if len(positions) > 0 {
		axisName := "Trace"
		if prestackState.selection.Axis == prestackcore.AxisOffset {
			axisName = "Offset"
		}
		firstLabel := fmt.Sprintf("%s %.6g", axisName, positions[0])
		lastLabel := fmt.Sprintf("%.6g", positions[len(positions)-1])
		drawAxisText(hdc, firstLabel, int(r.Left), int(r.Bottom)+5, int(r.Left)+180, int(r.Bottom)+28, DT_LEFT|DT_SINGLELINE)
		if len(positions) > 2 {
			mid := positions[len(positions)/2]
			drawAxisText(hdc, fmt.Sprintf("%.6g", mid), int(r.Left+r.Right)/2-70, int(r.Bottom)+5, int(r.Left+r.Right)/2+70, int(r.Bottom)+28, DT_CENTER|DT_SINGLELINE)
		}
		drawAxisText(hdc, lastLabel, int(r.Right)-180, int(r.Bottom)+5, int(r.Right), int(r.Bottom)+28, DT_RIGHT|DT_SINGLELINE)
	}
	// Keep the offset range visible even when Trace is the horizontal axis.  It
	// is easy to lose this important acquisition context when switching between
	// CMP/Shot/Receiver gathers, so show the same range in the title and in the
	// completion status rather than requiring the user to infer it from the
	// Offset axis mode.
	offsetText := "Offset n/a"
	if prestackState.gather.OffsetRange.Valid {
		offsetText = fmt.Sprintf("Offset %.6g..%.6g", prestackState.gather.OffsetRange.Min, prestackState.gather.OffsetRange.Max)
	}
	title := fmt.Sprintf("%s — %s — %d physical traces — %s", filepath.Base(prestackState.dataset.Path), prestackState.selection.Key.String(), len(prestackState.gather.TraceIndices), offsetText)
	if prestackState.selection.Type == prestackcore.GatherRaw {
		title = fmt.Sprintf("%s — 原始叠前道序 %d–%d — %d physical traces — %s", filepath.Base(prestackState.dataset.Path), prestackState.gather.RawTraceStart+1, prestackState.gather.RawTraceEnd, len(prestackState.gather.TraceIndices), offsetText)
	}
	drawAxisText(hdc, title, int(r.Left), int(r.Top)-25, int(r.Right), int(r.Top)-2, DT_CENTER|DT_SINGLELINE)
}
func prestackMapPixel(x, y float64) (int, int) {
	r := prestackSceneRect()
	b := prestackState.mapBounds
	dx, dy := b.XMax-b.XMin, b.YMax-b.YMin
	if dx <= 0 {
		dx = 1
	}
	if dy <= 0 {
		dy = 1
	}
	scale := math.Min(float64(r.Right-r.Left)/dx, float64(r.Bottom-r.Top)/dy) * .94
	cx, cy := float64(r.Left+r.Right)/2, float64(r.Top+r.Bottom)/2
	return int(cx + (x-(b.XMin+b.XMax)/2)*scale), int(cy - (y-(b.YMin+b.YMax)/2)*scale)
}
func prestackMapWorld(x, y int) (float64, float64) {
	r := prestackSceneRect()
	b := prestackState.mapBounds
	dx, dy := b.XMax-b.XMin, b.YMax-b.YMin
	if dx <= 0 {
		dx = 1
	}
	if dy <= 0 {
		dy = 1
	}
	scale := math.Min(float64(r.Right-r.Left)/dx, float64(r.Bottom-r.Top)/dy) * .94
	return (float64(x)-float64(r.Left+r.Right)/2)/scale + (b.XMin+b.XMax)/2, (float64(r.Top+r.Bottom)/2-float64(y))/scale + (b.YMin+b.YMax)/2
}
func paintPrestackGeometry(hdc uintptr) {
	idx := prestackState.index
	r := prestackSceneRect()
	if idx == nil || !idx.Bounds.Valid {
		drawAxisText(hdc, "没有可用 XY 坐标。道集仍可按有效头字段浏览；请在 Mapping 检查坐标字节和单位。", int(r.Left), int(r.Top), int(r.Right), int(r.Bottom), DT_LEFT|DT_WORDBREAK)
		return
	}
	saved, _, _ := pSaveDC.Call(hdc)
	pIntersectClipRect.Call(hdc, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right), uintptr(r.Bottom))
	point := func(x, y float64, color uintptr, size int) {
		px, py := prestackMapPixel(x, y)
		if px < int(r.Left) || px > int(r.Right) || py < int(r.Top) || py > int(r.Bottom) {
			return
		}
		rr := RECT{Left: int32(px - size), Top: int32(py - size), Right: int32(px + size + 1), Bottom: int32(py + size + 1)}
		brush, _, _ := pCreateSolidBrush.Call(color)
		pFillRect.Call(hdc, uintptr(unsafe.Pointer(&rr)), brush)
		pDeleteObject.Call(brush)
	}
	stride := maxInt(1, (len(idx.Records)+11999)/12000)
	for i := 0; i < len(idx.Records); i += stride {
		t := idx.Records[i]
		if prestackState.mapLayers[0] && t.HasSource {
			point(t.SourceX, t.SourceY, 0x00CB783A, 1)
		}
		if prestackState.mapLayers[1] && t.HasReceiver {
			point(t.ReceiverX, t.ReceiverY, 0x004AAA64, 1)
		}
		if prestackState.mapLayers[2] && t.HasMidpoint {
			point(t.MidpointX, t.MidpointY, 0x00909090, 1)
		}
	}
	maxFold := 1
	for _, b := range idx.Bins {
		if b.Fold > maxFold {
			maxFold = b.Fold
		}
	}
	binStride := maxInt(1, (len(idx.Bins)+19999)/20000)
	for i := 0; i < len(idx.Bins); i += binStride {
		b := idx.Bins[i]
		if !b.HasCoordinates {
			continue
		}
		if prestackState.mapLayers[4] {
			ratio := float64(b.Fold) / float64(maxFold)
			point(b.X, b.Y, uintptr(uint32(220*(1-ratio))<<16|uint32(120+100*(1-ratio))<<8|uint32(40+215*ratio)), 3)
		} else if prestackState.mapLayers[3] {
			point(b.X, b.Y, 0x008B7055, 2)
		}
		if prestackState.selection.Type == prestackcore.GatherCMP && b.Key == prestackState.selection.Key {
			point(b.X, b.Y, 0x000000FF, 5)
		}
	}
	if prestackState.dragging && !prestackState.panning {
		drawCrookedZoomRectangle(hdc, prestackState.dragX, prestackState.dragY, prestackState.dragCurrentX, prestackState.dragCurrentY)
	}
	pRestoreDC.Call(hdc, saved)
	drawAxisText(hdc, fmt.Sprintf("X %.6g..%.6g   Y %.6g..%.6g | %d traces / %d bins | 左拖放大，右拖平移，滚轮缩放，双击 Bin 打开 CMP", idx.Bounds.XMin, idx.Bounds.XMax, idx.Bounds.YMin, idx.Bounds.YMax, len(idx.Records), len(idx.Bins)), int(r.Left), int(r.Bottom)+8, int(r.Right), int(r.Bottom)+32, DT_LEFT|DT_SINGLELINE)
}
func prestackHitBin(x, y int) int {
	if prestackState.index == nil {
		return -1
	}
	best, distance := -1, 64.0
	for i, b := range prestackState.index.Bins {
		if !b.HasCoordinates {
			continue
		}
		px, py := prestackMapPixel(b.X, b.Y)
		d := float64((px-x)*(px-x) + (py-y)*(py-y))
		if d < distance {
			best, distance = i, d
		}
	}
	return best
}
func prestackPointInside(x, y int) bool {
	r := prestackSceneRect()
	return x >= int(r.Left) && x < int(r.Right) && y >= int(r.Top) && y < int(r.Bottom)
}

// cancelPrestackPointerDrag is deliberately idempotent.  It is called when
// focus/capture moves from the scene to a child control (especially a combo
// box) and when a modal/native popup closes.  Keeping the parent capture
// after that transition starves child controls of mouse messages, which is
// why every dropdown can look clickable yet fail to open or select an item.
func cancelPrestackPointerDrag(releaseCapture bool) {
	if !prestackState.dragging && !prestackState.panning && !prestackState.ctrlClick {
		return
	}
	prestackState.dragging = false
	prestackState.panning = false
	prestackState.ctrlClick = false
	// WM_CAPTURECHANGED is also delivered when a native combo opens its drop
	// list.  Do not release the *new* owner there or the popup closes before a
	// selection can be made.  Only release capture when this window still owns
	// the active gesture (WM_CANCELMODE or a toolbar click while dragging).
	if releaseCapture {
		pReleaseCapture.Call()
	}
	invalidatePrestackScene()
}
func resetPrestackSection() {
	if prestackState.dataset == nil {
		return
	}
	if prestackState.selection.Type == prestackcore.GatherRaw {
		// Restore the full visual extent of the selected raw range without
		// discarding its explicit trace/sample window.
		applyPrestackRawRange()
		return
	}
	prestackState.viewFirst = 0
	prestackState.viewLast = len(prestackState.gather.TraceIndices) - 1
	prestackState.sampleFirst = 0
	prestackState.sampleLast = prestackState.dataset.Metadata.SamplesPerTrace - 1
	startPrestackRender()
}

func prestackWndProc(h uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case WM_ERASEBKGND:
		return 1
	case WM_CANCELMODE:
		// A combo/list popup, edit control, or another child can take focus and
		// mouse capture while the user is interacting with the toolbar.  End any
		// scene gesture before the child receives its next mouse message.
		cancelPrestackPointerDrag(true)
		return 0
	case WM_CAPTURECHANGED, WM_KILLFOCUS:
		// The popup/child now owns capture.  Reset our gesture state but leave
		// the new capture owner untouched; calling ReleaseCapture here would
		// immediately close every combo drop list.
		cancelPrestackPointerDrag(false)
		return 0
	case WM_ENTERSIZEMOVE:
		// Keep the last complete frame as the resize preview.  The frame is
		// clipped/scaled into the current scene rectangle by paintPrestackGather;
		// only the final render is scheduled after the user releases the border.
		prestackState.resizeActive = true
		prestackState.resizePending = false
		prestackState.resizeGeneration++
		if prestackState.renderCancel != nil {
			prestackState.renderCancel()
			prestackState.renderCancel = nil
		}
		prestackState.renderGeneration = atomic.AddInt64(&prestackGeneration, 1)
		prestackState.rendering = false
		invalidatePrestackScene()
		return 0
	case WM_SIZE:
		if prestackHwnd != 0 {
			layoutPrestackControls()
			// Restore/maximize can emit WM_SIZE without a surrounding
			// WM_ENTERSIZEMOVE/WM_EXITSIZEMOVE pair.  Treat that path as a
			// one-shot resize as well; minimized windows (wParam == 1) are
			// intentionally left without a render.
			if wParam != 1 && !prestackState.resizeActive && prestackState.page == 0 && prestackState.index != nil {
				prestackState.resizeGeneration++
				prestackState.resizePending = true
				schedulePrestackResizeRender(h, prestackState.resizeGeneration)
			}
			invalidatePrestackScene()
		}
		return 0
	case WM_EXITSIZEMOVE:
		// Always leave resize mode, including Geometry/Mapping pages.  If we
		// only clear this flag on the Gather page, resizing while another tab is
		// active leaves the session permanently painting the resize placeholder
		// when the user later returns to Gather.
		prestackState.resizeActive = false
		if prestackState.page == 0 {
			prestackState.resizePending = true
			prestackState.resizeGeneration++
			schedulePrestackResizeRender(h, prestackState.resizeGeneration)
		} else {
			prestackState.resizePending = false
		}
		invalidatePrestackScene()
		return 0
	case WM_PRESTACK_RESIZE_FLUSH:
		// Ignore delayed messages from a previous drag or a destroyed/recreated
		// window.  Only the latest size is allowed to enter the renderer.
		if prestackState.page == 0 && !prestackState.resizeActive && prestackState.resizePending && int64(wParam) == prestackState.resizeGeneration {
			prestackState.resizePending = false
			startPrestackRender()
		}
		return 0
	case WM_PAINT:
		var ps PAINTSTRUCT
		dc, _, _ := pBeginPaint.Call(h, uintptr(unsafe.Pointer(&ps)))
		paintPrestack(dc)
		pEndPaint.Call(h, uintptr(unsafe.Pointer(&ps)))
		return 0
	case WM_PRESTACK_READY:
		receivePrestackIndex(int64(wParam))
		return 0
	case WM_PRESTACK_RENDER:
		receivePrestackRender(int64(wParam))
		return 0
	case WM_PRESTACK_COMBO_COMMIT:
		commitPrestackCombo(int(wParam))
		return 0
	case WM_PRESTACK_PROGRESS:
		if int64(wParam) == prestackState.indexGeneration {
			pct := atomic.LoadInt64(&prestackProgress)
			pSendMessageW.Call(prestackUI.progress, PBM_SETPOS, uintptr(pct), 0)
			setPrestackStatus(fmt.Sprintf("正在扫描叠前道头 %d%%（不读取振幅）", pct))
		}
		return 0
	case WM_DROPFILES:
		handleWorkspaceDrop(wParam, workspaceModePrestack)
		return 0
	case WM_NOTIFY:
		if lParam != 0 {
			n := (*NMHDR)(unsafe.Pointer(lParam))
			if n.HwndFrom == prestackUI.tabs && n.Code == TCN_SELCHANGE {
				v, _, _ := pSendMessageW.Call(prestackUI.tabs, TCM_GETCURSEL, 0, 0)
				setPrestackPage(int(v))
				return 0
			}
		}
	case WM_COMMAND:
		id, code := int(wParam&0xffff), int(wParam>>16)
		switch id {
		case IDPRESTACK_HOME:
			pPostMessageW.Call(h, WM_CLOSE, 0, 0)
		case IDPRESTACK_OPEN:
			if path := openDialog(h); path != "" {
				openApplicationPath(path, workspaceModePrestack)
			}
		case IDPRESTACK_MAPPING_AUTO:
			startPrestackIndex(true)
		case IDPRESTACK_MAPPING_APPLY:
			applyPrestackMapping()
		case IDPRESTACK_PREV:
			selectPrestackGather(prestackState.keyIndex - 1)
		case IDPRESTACK_NEXT:
			selectPrestackGather(prestackState.keyIndex + 1)
		case IDPRESTACK_KIND:
			if prestackComboSelection(code) {
				postPrestackComboCommit(id)
			}
		case IDPRESTACK_OFFSET_BIN_APPLY:
			if code == 0 || code == 1 {
				applyPrestackOffsetBinSize()
			}
		case IDPRESTACK_RAW_APPLY:
			if code == 0 || code == 1 {
				applyPrestackRawRange()
			}
		case IDPRESTACK_RAW_ALL:
			if code == 0 || code == 1 {
				setRawRangeControlDefaults()
				applyPrestackRawRange()
			}
		case IDPRESTACK_RAW_SAMPLE_MODE:
			if prestackComboSelection(code) && prestackState.selection.Type == prestackcore.GatherRaw {
				postPrestackComboCommit(id)
			}
		case IDPRESTACK_KEY:
			if prestackComboSelection(code) {
				postPrestackComboCommit(id)
			}
		case IDPRESTACK_SORT:
			if prestackComboSelection(code) {
				postPrestackComboCommit(id)
			}
		case IDPRESTACK_AXIS:
			if prestackComboSelection(code) {
				postPrestackComboCommit(id)
			}
		case IDPRESTACK_DISPLAY:
			if prestackComboSelection(code) {
				postPrestackComboCommit(id)
			}
		case IDPRESTACK_PALETTE:
			if prestackComboSelection(code) {
				postPrestackComboCommit(id)
			}
		case IDPRESTACK_GAINMINUS, IDPRESTACK_GAINPLUS:
			delta := 1.0
			if id == IDPRESTACK_GAINMINUS {
				delta = -1
			}
			prestackState.gain = math.Max(0, math.Min(49, prestackState.gain+delta))
			setText(prestackUI.gain, fmt.Sprintf("%.0f%%", prestackState.gain))
			startPrestackRender()
		case IDPRESTACK_AGC:
			v, _, _ := pSendMessageW.Call(prestackUI.agc, BM_GETCHECK, 0, 0)
			prestackState.agc = v == BST_CHECKED
			startPrestackRender()
		case IDPRESTACK_RESET:
			resetPrestackSection()
		case IDPRESTACK_HEADERS:
			r := prestackSceneRect()
			prestackInspectTrace(int(r.Left), int(r.Top))
		case IDPRESTACK_MAPRESET:
			if prestackState.index != nil {
				prestackState.mapBounds = prestackState.index.Bounds
				invalidatePrestackScene()
			}
		case IDPRESTACK_LAYER_SOURCE, IDPRESTACK_LAYER_RECEIVER, IDPRESTACK_LAYER_MIDPOINT, IDPRESTACK_LAYER_BIN, IDPRESTACK_LAYER_FOLD:
			i := id - IDPRESTACK_LAYER_SOURCE
			v, _, _ := pSendMessageW.Call(prestackUI.layers[i], BM_GETCHECK, 0, 0)
			prestackState.mapLayers[i] = v == BST_CHECKED
			invalidatePrestackScene()
		}
		return 0
	case WM_KEYDOWN:
		if wParam == VK_ESCAPE {
			prestackState.dragging = false
			pReleaseCapture.Call()
			invalidatePrestackScene()
			return 0
		}
	case WM_LBUTTONDOWN, WM_RBUTTONDOWN:
		x, y := int(int16(lParam&0xffff)), int(int16(lParam>>16))
		if !prestackPointInside(x, y) || prestackState.page > 1 {
			// If this message arrived while the parent still owned capture, it is
			// the first click on a child/toolbar after a drag.  Release capture so
			// the next click is delivered to the native control itself.
			cancelPrestackPointerDrag(true)
			return 0
		}
		prestackState.dragging = true
		prestackState.panning = msg == WM_RBUTTONDOWN
		prestackState.ctrlClick = msg == WM_LBUTTONDOWN && (wParam&prestackMKControl) != 0
		prestackState.dragX, prestackState.dragY = x, y
		prestackState.dragCurrentX, prestackState.dragCurrentY = x, y
		prestackState.dragMap = prestackState.mapBounds
		prestackState.dragFirst, prestackState.dragLast = prestackState.viewFirst, prestackState.viewLast
		prestackState.dragSampleFirst, prestackState.dragSampleLast = prestackState.sampleFirst, prestackState.sampleLast
		pSetCapture.Call(h)
		pSetFocus.Call(h)
		return 0
	case WM_MOUSEMOVE:
		x, y := int(int16(lParam&0xffff)), int(int16(lParam>>16))
		if prestackState.dragging {
			r := prestackSceneRect()
			x = clampInt(x, int(r.Left), int(r.Right))
			y = clampInt(y, int(r.Top), int(r.Bottom))
			prestackState.dragCurrentX, prestackState.dragCurrentY = x, y
			if prestackState.panning && prestackState.page == 1 {
				prestackState.mapBounds = prestackState.dragMap
				a, b := prestackMapWorld(prestackState.dragX, prestackState.dragY)
				c, d := prestackMapWorld(x, y)
				prestackState.mapBounds.XMin += a - c
				prestackState.mapBounds.XMax += a - c
				prestackState.mapBounds.YMin += b - d
				prestackState.mapBounds.YMax += b - d
			}
			invalidatePrestackScene()
		} else if prestackPointInside(x, y) && time.Since(prestackState.lastHover) > 50*time.Millisecond {
			prestackState.lastHover = time.Now()
			if prestackState.page == 1 {
				if i := prestackHitBin(x, y); i >= 0 {
					b := prestackState.index.Bins[i]
					setPrestackStatus(fmt.Sprintf("Bin %s | XY %.6g, %.6g | Fold %d | Offset %.5g..%.5g | Azimuth %.3g..%.3g", b.Key.String(), b.X, b.Y, b.Fold, b.OffsetRange.Min, b.OffsetRange.Max, b.AzimuthRange.Min, b.AzimuthRange.Max))
				}
			} else if prestackState.page == 0 {
				prestackHoverGather(x, y)
			}
		}
		return 0
	case WM_LBUTTONUP, WM_RBUTTONUP:
		if !prestackState.dragging {
			return 0
		}
		prestackState.dragging = false
		pReleaseCapture.Call()
		if msg == WM_LBUTTONUP && (wParam&prestackMKControl) != 0 {
			prestackState.ctrlClick = true
		}
		x, y := prestackState.dragCurrentX, prestackState.dragCurrentY
		dx, dy := absInt(x-prestackState.dragX), absInt(y-prestackState.dragY)
		r := prestackSceneRect()
		if prestackState.page == 1 {
			if !prestackState.panning && dx >= 5 && dy >= 5 {
				a, b := prestackMapWorld(prestackState.dragX, prestackState.dragY)
				c, d := prestackMapWorld(x, y)
				prestackState.mapBounds = prestackcore.XYBounds{XMin: math.Min(a, c), XMax: math.Max(a, c), YMin: math.Min(b, d), YMax: math.Max(b, d), Valid: true}
			}
			invalidatePrestackScene()
			return 0
		}
		if prestackState.panning {
			n := prestackState.dragLast - prestackState.dragFirst
			shift := int(math.Round(float64(prestackState.dragX-x) * float64(n) / float64(maxInt(1, int(r.Right-r.Left)))))
			prestackState.viewFirst = clampInt(prestackState.dragFirst+shift, 0, maxInt(0, len(prestackState.gather.TraceIndices)-1-n))
			prestackState.viewLast = prestackState.viewFirst + n
			ns := prestackState.dragSampleLast - prestackState.dragSampleFirst
			shiftS := int(math.Round(float64(prestackState.dragY-y) * float64(ns) / float64(maxInt(1, int(r.Bottom-r.Top)))))
			prestackState.sampleFirst = clampInt(prestackState.dragSampleFirst+shiftS, 0, maxInt(0, prestackState.dataset.Metadata.SamplesPerTrace-1-ns))
			prestackState.sampleLast = prestackState.sampleFirst + ns
			startPrestackRender()
		} else if dx >= 5 && dy >= 5 {
			a, b := prestackPickColumn(prestackState.dragX), prestackPickColumn(x)
			if a >= 0 && b >= 0 {
				prestackState.viewFirst, prestackState.viewLast = minInt(a, b), maxInt(a, b)
				n := prestackState.sampleLast - prestackState.sampleFirst
				sa := prestackState.sampleFirst + int(float64(minInt(prestackState.dragY, y)-int(r.Top))/float64(maxInt(1, int(r.Bottom-r.Top)))*float64(n))
				sb := prestackState.sampleFirst + int(float64(maxInt(prestackState.dragY, y)-int(r.Top))/float64(maxInt(1, int(r.Bottom-r.Top)))*float64(n))
				prestackState.sampleFirst, prestackState.sampleLast = sa, maxInt(sa, sb)
				startPrestackRender()
			}
		}
		if dx < 5 && dy < 5 && prestackState.ctrlClick {
			prestackInspectTrace(x, y)
		}
		prestackState.ctrlClick = false
		invalidatePrestackScene()
		return 0
	case WM_LBUTTONDBLCLK:
		x, y := int(int16(lParam&0xffff)), int(int16(lParam>>16))
		if !prestackPointInside(x, y) {
			return 0
		}
		prestackState.dragging = false
		pReleaseCapture.Call()
		if prestackState.page == 1 {
			if i := prestackHitBin(x, y); i >= 0 {
				key := prestackState.index.Bins[i].Key
				openPrestackGather(prestackcore.GatherCMP, key)
			} else if prestackState.index != nil {
				prestackState.mapBounds = prestackState.index.Bounds
				invalidatePrestackScene()
			}
		} else if prestackState.page == 0 {
			resetPrestackSection()
		}
		return 0
	case WM_MOUSEWHEEL:
		if prestackState.page == 1 && prestackState.mapBounds.Valid {
			factor := 1.25
			if int16(wParam>>16) > 0 {
				factor = .8
			}
			b := prestackState.mapBounds
			cx, cy := (b.XMin+b.XMax)/2, (b.YMin+b.YMax)/2
			dx, dy := (b.XMax-b.XMin)*factor/2, (b.YMax-b.YMin)*factor/2
			prestackState.mapBounds = prestackcore.XYBounds{XMin: cx - dx, XMax: cx + dx, YMin: cy - dy, YMax: cy + dy, Valid: true}
			invalidatePrestackScene()
		}
		return 0
	case WM_CLOSE:
		if application != nil && !prestackManagerClosing {
			_ = application.Workspaces.CloseActive()
		} else {
			pDestroyWindow.Call(h)
		}
		return 0
	case WM_DESTROY:
		cancelPrestackJobs()
		prestackDeliveryMu.Lock()
		prestackDeliveryHwnd = 0
		prestackPendingIndex = nil
		prestackPendingRender = nil
		prestackDeliveryMu.Unlock()
		revokeOleSegyDropTarget(h)
		prestackHwnd = 0
		prestackUI = prestackControls{}
		prestackState = prestackSession{}
		if application != nil && !prestackManagerClosing {
			application.Workspaces.NotifyClosed(workspacecore.KindPrestack)
		}
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(h, uintptr(msg), wParam, lParam)
	return r
}
