//go:build windows

package main

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"math"
	"os"
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
	IDPRESTACK_WIGGLE_DECIM
	IDPRESTACK_OFFSET_BIN
	IDPRESTACK_OFFSET_BIN_APPLY
	IDPRESTACK_CMP_BIN
	IDPRESTACK_CMP_BIN_APPLY
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
	IDPRESTACK_QC_EXPORT
	IDPRESTACK_EXPORT_GATHER
	IDPRESTACK_EXPORT_CSV
	IDPRESTACK_MULTI_APPLY
	IDPRESTACK_COMPARE_OPEN_B
	IDPRESTACK_COMPARE_CLOSE_B
	IDPRESTACK_COMPARE_EXPORT
	IDPRESTACK_MAPBYTE          = 8200
	WM_PRESTACK_READY           = WM_USER + 610
	WM_PRESTACK_PROGRESS        = WM_USER + 611
	WM_PRESTACK_RENDER          = WM_USER + 612
	WM_PRESTACK_EXPORT_DONE     = WM_USER + 615
	WM_PRESTACK_EXPORT_PROGRESS = WM_USER + 616
	// WM_ENTERSIZEMOVE is not declared by the small Win32 constant set in
	// main_windows.go.  Keep the standard value local to this window and use a
	// private message to debounce the final resize render.
	WM_ENTERSIZEMOVE         = 0x0231
	WM_PRESTACK_RESIZE_FLUSH = WM_USER + 613
	// Post selection work so key-list rebuilding/render planning starts after
	// the native control has completed its notification transaction.
	WM_PRESTACK_COMBO_COMMIT    = WM_USER + 614
	WM_PRESTACK_COMPARE_B_READY = WM_USER + 617
	WM_PRESTACK_COMPARE_RENDER  = WM_USER + 618
	WM_TIMER                    = 0x0113
	prestackCtrlClickTimer      = 1
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
	home, open, path, tabs, status, progress, progressLabel                                       uintptr
	qcExport, exportGather, exportCSV                                                             uintptr
	compareOpenB, compareCloseB, compareExport                                                    uintptr
	kind, key, prev, next, sortLabel, sort, axis, display, wiggleDecimLabel, wiggleDecim, palette uintptr
	offsetBinLabel, offsetBin, offsetBinApply                                                     uintptr
	cmpBinLabel, cmpBin, cmpBinApply                                                              uintptr
	multiLabel, multiEdit, multiApply                                                             uintptr
	rawTraceStartLabel, rawTraceStart, rawTraceEndLabel, rawTraceEnd                              uintptr
	rawSampleMode, rawSampleStartLabel, rawSampleStart, rawSampleEndLabel, rawSampleEnd           uintptr
	rawApply, rawAll                                                                              uintptr
	gainMinus, gain, gainPlus, agc, reset, headers                                                uintptr
	layers                                                                                        [5]uintptr
	mapReset, mappingAuto, mappingApply, mappingText                                              uintptr
	mappingLabels, mappingEdits                                                                   []uintptr
}

// All mutable presentation state belongs to this window, not to the legacy
// compare globals. Workers receive immutable snapshots and short-lived readers.
type prestackSession struct {
	dataset                                             *dataset.SeismicDataset
	index                                               *prestackcore.PrestackIndex
	mapping                                             prestackcore.HeaderMapping
	detection                                           prestackcore.MappingDetection
	selection                                           prestackcore.GatherSelection
	gather                                              prestackcore.GatherResult
	keys                                                []prestackcore.GatherKey
	page, keyIndex, palette, display, wiggleDecimation  int
	rawSampleModeValue                                  int
	gain                                                float64
	offsetBinSize                                       float64
	cmpBinSize                                          float64
	cmpBinOriginX, cmpBinOriginY                        float64
	azimuthBinSize                                      float64
	agc                                                 bool
	workspaceGeneration                                 uint64
	datasetGeneration, selectionGeneration, ownerToken  int64
	indexGeneration, renderGeneration, exportGeneration int64
	mappingGeneration, compareBMappingGeneration        int64
	compareModeGeneration, compareDisplayGeneration     int64
	resizeGeneration                                    int64
	indexCancel, renderCancel, exportCancel             context.CancelFunc
	loading, rendering, needsIndex, progressDone        bool
	resizeActive, resizePending                         bool
	suppressPageRender                                  bool
	viewFirst, viewLast, sampleFirst, sampleLast        int
	indices, bgra                                       []byte
	imageWidth, imageHeight                             int
	// renderedTraces/renderedPositions describe the physical trace represented
	// by each output column in the last frame. The gather itself can contain
	// tens of thousands of physical traces while the raster is sampled by the
	// sparse renderer; keeping this mapping prevents picks and labels from
	// referring to the unsampled list.
	renderedTraces                                       []int64
	renderedPositions                                    []float64
	renderedColumns                                      int
	stats                                                segy.RenderStats
	mapBounds                                            prestackcore.XYBounds
	mapLayers                                            [5]bool
	dragging, panning                                    bool
	ctrlClick                                            bool
	dragX, dragY, dragCurrentX, dragCurrentY             int
	dragMap                                              prestackcore.XYBounds
	dragFirst, dragLast, dragSampleFirst, dragSampleLast int
	lastHover                                            time.Time
	linkKind                                             prestackcore.GatherType
	linkIndex                                            int
	linkGeneration                                       uint64
	selectedBinIndex                                     int
	pendingCtrlTrace                                     int64
	pendingCtrlX, pendingCtrlY                           int
	pendingCtrlGeneration                                uint64
	pendingCtrlValid                                     bool
	// Compare B is intentionally independent from A. Its dataset/index are
	// metadata-only and are never used to mutate A's gather or renderer.
	compareBDataset                  *dataset.SeismicDataset
	compareBIndex                    *prestackcore.PrestackIndex
	compareBMapping                  prestackcore.HeaderMapping
	compareBGeneration               int64
	compareBCancel                   context.CancelFunc
	compareBLoading                  bool
	compareBError                    string
	compareBMatch                    prestackcore.CompareMatchResult
	compareBAxis                     prestackcore.SampleAxisCompatibility
	compareRenderCancel              context.CancelFunc
	compareRenderGeneration          int64
	compareRendering                 bool
	compareA, compareB, compareDelta []byte
	compareWidth, compareHeight      int
}

// Workers capture this identity on the UI thread and never read mutable
// presentation state. HWND alone is insufficient because Windows reuses it.
type prestackAsyncToken struct {
	owner                                                              uintptr
	ownerToken, datasetGeneration, selectionGeneration, sizeGeneration int64
	workspaceGeneration                                                uint64
	sceneWidth, sceneHeight, page                                      int
}

type prestackIndexResult struct {
	token      prestackAsyncToken
	generation int64
	index      *prestackcore.PrestackIndex
	detection  prestackcore.MappingDetection
	detected   bool
	err        error
}
type prestackCompareBResult struct {
	generation, datasetGeneration, selectionGeneration, mappingGeneration int64
	compareModeGeneration                                                 int64
	workspaceGeneration                                                   uint64
	ownerToken                                                            int64
	owner                                                                 uintptr
	dataset                                                               *dataset.SeismicDataset
	index                                                                 *prestackcore.PrestackIndex
	mapping                                                               prestackcore.HeaderMapping
	err                                                                   error
}
type prestackCompareRenderResult struct {
	generation, datasetGeneration, selectionGeneration, aMappingGeneration    int64
	bGeneration, bMappingGeneration, compareModeGeneration, displayGeneration int64
	resizeGeneration, sampleFirst, sampleLast                                 int64
	workspaceGeneration                                                       uint64
	owner, ownerToken                                                         int64
	comparePage                                                               int
	width, height                                                             int
	palette                                                                   int
	a, b, delta                                                               []byte
	err, bErr                                                                 error
}
type prestackRenderResult struct {
	token               prestackAsyncToken
	generation          int64
	workspaceGeneration uint64
	palette             int
	indices, bgra       []byte
	traces              []int64
	positions           []float64
	width, height       int
	stats               segy.RenderStats
	err                 error
}
type prestackExportResult struct {
	generation, datasetGeneration int64
	path, format                  string
	traces, samples               int
	err                           error
}

var (
	prestackHwnd                                                      uintptr
	prestackUI                                                        prestackControls
	prestackState                                                     prestackSession
	prestackClassRegistered, prestackManagerClosing                   bool
	prestackGeneration                                                int64
	prestackDeliveryMu                                                sync.Mutex
	prestackDeliveryHwnd                                              uintptr
	prestackOwnerToken                                                int64
	prestackDeliveryOwnerToken                                        int64
	prestackDeliveryIndexGeneration, prestackDeliveryRenderGeneration int64
	prestackPendingIndex                                              *prestackIndexResult
	prestackPendingRender                                             *prestackRenderResult
	prestackPendingCompareB                                           *prestackCompareBResult
	prestackPendingCompareRender                                      *prestackCompareRenderResult
	prestackExportDone                                                = make(chan prestackExportResult, 4)
	prestackProgress                                                  int64
	prestackRenderSlot                                                = make(chan struct{}, 1)
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
		datasetGeneration: atomic.AddInt64(&prestackGeneration, 1), ownerToken: prestackOwnerToken,
		workspaceGeneration: r.Generation, sampleLast: r.Dataset.Metadata.SamplesPerTrace - 1,
		mapLayers: [5]bool{true, true, true, true, true}}
	prestackState.mappingGeneration = atomic.AddInt64(&prestackGeneration, 1)
	prestackState.compareModeGeneration = atomic.AddInt64(&prestackGeneration, 1)
	prestackState.compareDisplayGeneration = atomic.AddInt64(&prestackGeneration, 1)
	prestackState.selectedBinIndex = -1
	prestackState.pendingCtrlTrace = -1
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
	// The layout is specified in client pixels. Convert that target to an
	// outer frame before creation so the restored window and maximized window
	// use the same client-area contract.
	outerW, outerH := prestackFrameExtent(1360, 880)
	style := uint32(WS_OVERLAPPEDWINDOW | WS_CLIPCHILDREN)
	h, _, _ := pCreateWindowExW.Call(WS_EX_APPWINDOW, uintptr(unsafe.Pointer(u16("SeisForgePrestack"))), uintptr(unsafe.Pointer(u16(APP_NAME+" v"+APP_VERSION+" — 叠前道集"))),
		uintptr(style), uintptr(CW_USEDEFAULT), uintptr(CW_USEDEFAULT), uintptr(outerW), uintptr(outerH), 0, 0, 0, 0)
	if h == 0 {
		return false
	}
	prestackHwnd = h
	prestackOwnerToken = atomic.AddInt64(&prestackGeneration, 1)
	prestackDeliveryMu.Lock()
	prestackDeliveryHwnd = h
	prestackDeliveryOwnerToken = prestackOwnerToken
	prestackDeliveryMu.Unlock()
	createPrestackControls()
	acceptSegyDrops(h)
	registerOleSegyDropTarget(h, workspaceModePrestack)
	return true
}

func prestackFrameExtent(clientW, clientH int) (int, int) {
	frame := RECT{Right: int32(maxInt(1, clientW)), Bottom: int32(maxInt(1, clientH))}
	style := uint32(WS_OVERLAPPEDWINDOW | WS_CLIPCHILDREN)
	if r, _, _ := pAdjustWindowRectEx.Call(uintptr(unsafe.Pointer(&frame)), uintptr(style), 0, uintptr(WS_EX_APPWINDOW)); r == 0 {
		return maxInt(1, clientW), maxInt(1, clientH)
	}
	return maxInt(1, int(frame.Right-frame.Left)), maxInt(1, int(frame.Bottom-frame.Top))
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
		minVisible, droppedWidth = 6, 220
	case IDPRESTACK_DISPLAY:
		minVisible, droppedWidth = 2, 170
	case IDPRESTACK_WIGGLE_DECIM:
		minVisible, droppedWidth = 5, 150
	case IDPRESTACK_SORT:
		// The secondary-ordering list contains both scalar orders and
		// acquisition categories.  Keep enough rows visible for it to be
		// browsable without sacrificing the native scrollbar.
		minVisible, droppedWidth = 8, 190
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
		rows = 6
	case IDPRESTACK_KEY:
		rows = 24
	case IDPRESTACK_SORT:
		rows = 8
	case IDPRESTACK_AXIS, IDPRESTACK_DISPLAY, IDPRESTACK_WIGGLE_DECIM:
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

var prestackWiggleDecimationOptions = []int{1, 2, 4, 5, 8}

// prestackWiggleRasterWidth returns the number of source columns used by a
// Wiggle frame.  The decimation is applied before the SEG-Y renderer is
// called, so a larger factor really means fewer traces are read/drawn rather
// than merely squeezing the same full-gather raster into a smaller bitmap.
func prestackWiggleRasterWidth(traceCount, sceneWidth, factor int) int {
	if traceCount <= 0 {
		return 0
	}
	if factor <= 0 {
		factor = 5
	}
	if sceneWidth < 2 {
		sceneWidth = 2
	}
	width := sceneWidth / factor
	if width < 2 {
		width = 2
	}
	if width > 1024 {
		width = 1024
	}
	if width > traceCount {
		width = traceCount
	}
	return width
}

func prestackWiggleDecimationValue(index int) int {
	if index < 0 || index >= len(prestackWiggleDecimationOptions) {
		return 5
	}
	return prestackWiggleDecimationOptions[index]
}

func prestackWiggleDecimationIndex(value int) int {
	for i, option := range prestackWiggleDecimationOptions {
		if option == value {
			return i
		}
	}
	return 3
}

func prestackComboLayoutHeight(c uintptr, fieldHeight int) int {
	for _, entry := range []struct {
		handle uintptr
		id     int
	}{{prestackUI.kind, IDPRESTACK_KIND}, {prestackUI.key, IDPRESTACK_KEY},
		{prestackUI.sort, IDPRESTACK_SORT}, {prestackUI.axis, IDPRESTACK_AXIS},
		{prestackUI.display, IDPRESTACK_DISPLAY}, {prestackUI.rawSampleMode, IDPRESTACK_RAW_SAMPLE_MODE},
		{prestackUI.wiggleDecim, IDPRESTACK_WIGGLE_DECIM}, {prestackUI.palette, IDPRESTACK_PALETTE}} {
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
	for i, label := range []string{"道集 Gather", "几何 Geometry", "道头映射 Mapping / Header", "对比 Compare", "QC"} {
		item := TCITEM{Mask: TCIF_TEXT, PszText: u16(label)}
		pSendMessageW.Call(u.tabs, TCM_INSERTITEMW, uintptr(i), uintptr(unsafe.Pointer(&item)))
	}
	u.kind = prestackCombo(IDPRESTACK_KIND, []string{"CMP / Bin", "Shot / 炮集", "Receiver / 检波点", "Common Offset / 共Offset", "原始叠前道序", "Azimuth / 方位角"})
	u.key = prestackCombo(IDPRESTACK_KEY, nil)
	u.prev = prestackButton(IDPRESTACK_PREV, "上一组")
	u.next = prestackButton(IDPRESTACK_NEXT, "下一组")
	u.sortLabel = createCtrl(prestackHwnd, "STATIC", "二级", WS_CHILD, 0, 0, 42, 22, 0)
	// The gather type/key controls define the first-level category.  This
	// combo is deliberately labelled as the second-level ordering in the
	// toolbar; for an all-range selection the index groups by the first-level
	// type before applying this order within each group.
	u.sort = prestackCombo(IDPRESTACK_SORT, []string{"二级：原始道序", "二级：Offset", "二级：|Offset|", "二级：Azimuth", "二级：CMP / CDP", "二级：Shot / FFID", "二级：Receiver", "二级：Common Offset"})
	u.axis = prestackCombo(IDPRESTACK_AXIS, []string{"Trace 横轴", "Offset 横轴"})
	u.display = prestackCombo(IDPRESTACK_DISPLAY, []string{"图像 Image", "波形 Wiggle"})
	u.wiggleDecimLabel = createCtrl(prestackHwnd, "STATIC", "Wiggle减采样", WS_CHILD, 0, 0, 90, 22, 0)
	u.wiggleDecim = prestackCombo(IDPRESTACK_WIGGLE_DECIM, []string{"1×（最密）", "2×", "4×", "5×（默认）", "8×（最稀）"})
	u.offsetBinLabel = createCtrl(prestackHwnd, "STATIC", "分箱宽度", WS_CHILD, 0, 0, 70, 22, 0)
	u.offsetBin = createCtrl(prestackHwnd, "EDIT", "20", WS_CHILD|WS_BORDER|WS_TABSTOP|ES_AUTOHSCROLL, 0, 0, 68, 24, IDPRESTACK_OFFSET_BIN)
	u.offsetBinApply = prestackButton(IDPRESTACK_OFFSET_BIN_APPLY, "应用分箱")
	u.cmpBinLabel = createCtrl(prestackHwnd, "STATIC", "CMP网格", WS_CHILD, 0, 0, 62, 22, 0)
	u.cmpBin = createCtrl(prestackHwnd, "EDIT", "0", WS_CHILD|WS_BORDER|WS_TABSTOP|ES_AUTOHSCROLL, 0, 0, 68, 24, IDPRESTACK_CMP_BIN)
	u.cmpBinApply = prestackButton(IDPRESTACK_CMP_BIN_APPLY, "应用CMP")
	u.multiLabel = createCtrl(prestackHwnd, "STATIC", "多道集", WS_CHILD, 0, 0, 52, 22, 0)
	u.multiEdit = createCtrl(prestackHwnd, "EDIT", "", WS_CHILD|WS_BORDER|WS_TABSTOP|ES_AUTOHSCROLL, 0, 0, 176, 24, 0)
	u.multiApply = prestackButton(IDPRESTACK_MULTI_APPLY, "应用多道集")
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
	u.qcExport = prestackButton(IDPRESTACK_QC_EXPORT, "导出 QC 报告")
	u.exportGather = prestackButton(IDPRESTACK_EXPORT_GATHER, "导出道集")
	u.exportCSV = prestackButton(IDPRESTACK_EXPORT_CSV, "导出清单")
	u.compareOpenB = prestackButton(IDPRESTACK_COMPARE_OPEN_B, "加载 B")
	u.compareCloseB = prestackButton(IDPRESTACK_COMPARE_CLOSE_B, "关闭 B")
	u.compareExport = prestackButton(IDPRESTACK_COMPARE_EXPORT, "导出匹配报告")
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
	prestackState.wiggleDecimation = 5
	pSendMessageW.Call(prestackUI.wiggleDecim, CB_SETCURSEL, uintptr(prestackWiggleDecimationIndex(prestackState.wiggleDecimation)), 0)
	pSendMessageW.Call(prestackUI.palette, CB_SETCURSEL, 2, 0)
	prestackState.offsetBinSize = prestackcore.DefaultOffsetBinSize
	prestackState.selection.OffsetBinSize = prestackState.offsetBinSize
	prestackState.selection.Keys = nil
	setText(prestackUI.multiEdit, "")
	setText(prestackUI.offsetBin, formatPrestackOffsetBin(prestackState.offsetBinSize))
	prestackState.cmpBinSize = 0
	prestackState.selection.CMPBin = prestackcore.CMPBinConfig{}
	setText(prestackUI.cmpBin, "0")
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

func formatPrestackCMPBin(v float64) string {
	if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		v = 0
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

func currentPrestackCMPBinConfig() prestackcore.CMPBinConfig {
	return prestackcore.CMPBinConfig{Size: prestackState.cmpBinSize, OriginX: prestackState.cmpBinOriginX, OriginY: prestackState.cmpBinOriginY}
}

func applyPrestackCMPBinSize() {
	if prestackState.index == nil {
		return
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(getText(prestackUI.cmpBin)), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1e9 {
		message(prestackHwnd, "CMP 分箱", "请输入 0（恢复原始 CMP）或有限的正数分箱宽度。", MB_OK|MB_ICONERROR)
		setText(prestackUI.cmpBin, formatPrestackCMPBin(prestackState.cmpBinSize))
		return
	}
	if v > 0 && v < 0.001 {
		message(prestackHwnd, "CMP 分箱", "分箱宽度不能小于 0.001。", MB_OK|MB_ICONERROR)
		setText(prestackUI.cmpBin, formatPrestackCMPBin(prestackState.cmpBinSize))
		return
	}
	prestackState.cmpBinSize = v
	prestackState.cmpBinOriginX, prestackState.cmpBinOriginY = 0, 0
	prestackState.selection.CMPBin = currentPrestackCMPBinConfig()
	setText(prestackUI.cmpBin, formatPrestackCMPBin(v))
	if prestackState.selection.Type == prestackcore.GatherCMP {
		selectPrestackGatherType(prestackcore.GatherCMP)
	} else {
		layoutPrestackControls()
	}
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
	case prestackcore.GatherAzimuth:
		return "Azimuth"
	default:
		return "Gather"
	}
}

func prestackSelectionKeyLabel(selection prestackcore.GatherSelection) string {
	if len(selection.Keys) > 0 {
		return fmt.Sprintf("多道集 %d 组", len(selection.Keys))
	}
	return selection.Key.String()
}

func prestackSortLabel(mode prestackcore.SortMode) string {
	switch mode {
	case prestackcore.SortOffset:
		return "Offset"
	case prestackcore.SortAbsoluteOffset:
		return "|Offset|"
	case prestackcore.SortAzimuth:
		return "Azimuth"
	default:
		return "原始道序"
	}
}

// prestackSecondaryModeFromCombo converts the compact UI index to the
// explicit second-level model.  SecondarySortMode reserves zero for
// SecondaryUnset, while the first item in this combo is the user-visible
// physical-order option.
func prestackSecondaryModeFromCombo(index int) prestackcore.SecondarySortMode {
	if index < 0 {
		index = 0
	}
	mode := prestackcore.SecondarySortMode(index + 1)
	if mode > prestackcore.SecondaryCommonOffset {
		return prestackcore.SecondaryPhysical
	}
	return mode
}

func prestackSecondaryLabel(selection prestackcore.GatherSelection) string {
	if selection.Secondary != prestackcore.SecondaryUnset {
		if label := selection.Secondary.String(); label != "" {
			return label
		}
	}
	return prestackSortLabel(selection.Sort)
}

func prestackSecondaryComboIndex(mode prestackcore.SecondarySortMode) int {
	if mode == prestackcore.SecondaryUnset {
		return 0
	}
	index := int(mode) - 1
	if index < 0 || index >= 8 {
		return 0
	}
	return index
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
	cancelPrestackExport()
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
	selection := prestackcore.GatherSelection{Type: prestackcore.GatherRaw, Key: prestackcore.GatherKey{Raw: true}, Sort: prestackcore.SortPhysical, Secondary: prestackcore.SecondaryPhysical, Axis: prestackcore.AxisTrace,
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
	oldPage := prestackState.page
	if page != prestackState.page {
		prestackClearLink()
		prestackState.compareModeGeneration = atomic.AddInt64(&prestackGeneration, 1)
		if oldPage == 3 && page != 3 {
			// Leaving Compare must stop the worker immediately.  The result is
			// additionally guarded by compareModeGeneration/page, but cancelling
			// here avoids keeping two SEG-Y readers busy after a tab switch.
			clearPrestackCompareRender()
		}
	}
	prestackState.page = page
	if page != 0 {
		// A delayed resize flush belongs only to the Gather scene.  Do not let
		// it leak into a later tab switch as a stale placeholder state.
		prestackState.resizePending = false
	}
	pSendMessageW.Call(prestackUI.tabs, TCM_FIRST+12, uintptr(page), 0)
	layoutPrestackControls()
	if page == 0 && prestackState.index != nil && len(prestackState.indices) == 0 && !prestackState.rendering && !prestackState.resizeActive && !prestackState.resizePending && !prestackState.suppressPageRender {
		startPrestackRender()
	}
	if page == 3 && prestackState.index != nil {
		startPrestackCompareRender()
	}
	invalidatePrestackScene()
	redrawPrestackWholeClient()
}

// redrawPrestackWholeClient is stronger than InvalidateRect for QC. The
// window clips its parent around native child controls; after a progress or
// completion control is moved/hidden, the exposed parent area is not
// guaranteed to enter the next update region on every common-controls/theme
// combination. RedrawWindow refreshes the parent and all child bounds in one
// synchronous pass.
func redrawPrestackWholeClient() {
	if prestackHwnd == 0 {
		return
	}
	flags := uintptr(RDW_INVALIDATE | RDW_ERASE | RDW_UPDATENOW | RDW_ALLCHILDREN)
	pRedrawWindow.Call(prestackHwnd, 0, 0, flags)
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
		for _, c := range []uintptr{u.prev, u.key, u.next, u.sortLabel, u.sort, u.axis, u.offsetBinLabel, u.offsetBin, u.offsetBinApply, u.cmpBinLabel, u.cmpBin, u.cmpBinApply, u.multiLabel, u.multiEdit, u.multiApply, u.exportGather, u.exportCSV} {
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
		wiggle := prestackState.display == 1
		setText(u.wiggleDecimLabel, map[bool]string{true: "减采样", false: "色标"}[wiggle])
		place(u.wiggleDecimLabel, x, y+2, 62, 22, true)
		place(u.palette, x+66, y, 145, 24, !wiggle)
		place(u.wiggleDecim, x+66, y, 145, 24, wiggle)
		x += 217
		place(u.gainMinus, x, y, 58, 24, true)
		x += 64
		place(u.gain, x, y+2, 40, 20, true)
		x += 46
		place(u.gainPlus, x, y, 58, 24, true)
		x += 64
		place(u.agc, x, y, 66, 24, true)
		x += 72
		place(u.exportGather, x, y, 82, 24, true)
		x += 88
		place(u.exportCSV, x, y, 82, 24, true)
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
		// At the minimum restored width the optional bin controls must still fit
		// inside the client.  Use a compact two-row variant instead of allowing
		// the last controls to spill into the non-client area (which also caused
		// stale pixels to appear after a resize).
		compact := w < 1280
		kindW, prevW, keyW, nextW := 150, 64, 190, 64
		sortLabelW, sortW, axisW := 42, 124, 110
		multiLabelW, multiEditW, multiApplyW := 50, 150, 76
		if compact {
			kindW, prevW, keyW, nextW = 130, 56, 145, 56
			sortLabelW, sortW, axisW = 34, 96, 90
			multiLabelW, multiEditW, multiApplyW = 40, 100, 70
		}
		x, y = 10, 76
		place(u.kind, x, y, kindW, 24, g)
		x += kindW + 6
		place(u.prev, x, y, prevW, 25, g)
		x += prevW + 6
		place(u.key, x, y, keyW, 24, g)
		x += keyW + 6
		place(u.next, x, y, nextW, 25, g)
		x += nextW + 6
		place(u.sortLabel, x, y+2, sortLabelW, 22, g)
		x += sortLabelW + 4
		place(u.sort, x, y, sortW, 24, g)
		x += sortW + 4
		place(u.axis, x, y, axisW, 24, g)
		x += axisW + 4
		// The multi-bin command stays on the first row so it is available for
		// every keyed gather without creating a third toolbar row.  Keep a
		// small gap around the edit so the text (notably "20") never touches a
		// neighboring border.
		place(u.multiLabel, x, y+2, multiLabelW, 22, g)
		x += multiLabelW + 4
		place(u.multiEdit, x, y, multiEditW, 24, g)
		x += multiEditW + 4
		place(u.multiApply, x, y, multiApplyW, 24, g)
		x += multiApplyW + 8
		isCMP := g && prestackState.selection.Type == prestackcore.GatherCMP
		isOffset := g && prestackState.selection.Type == prestackcore.GatherOffset
		binLabelW, binEditW, binApplyW := 62, 68, 76
		if compact {
			binLabelW, binEditW, binApplyW = 52, 58, 68
		}
		place(u.cmpBinLabel, x, y+2, binLabelW, 22, isCMP)
		x += binLabelW + 4
		place(u.cmpBin, x, y, binEditW, 24, isCMP)
		x += binEditW + 4
		place(u.cmpBinApply, x, y, binApplyW, 24, isCMP)
		place(u.offsetBinLabel, x-binLabelW-binEditW-8, y+2, binLabelW, 22, isOffset)
		place(u.offsetBin, x-binEditW-4, y, binEditW, 24, isOffset)
		place(u.offsetBinApply, x, y, binApplyW, 24, isOffset)
		// Second row: display, reset, headers, palette, gain and AGC.
		x, y = 10, 107
		place(u.display, x, y, 112, 24, g)
		x += 118
		place(u.reset, x, y, 86, 25, g)
		x += 92
		place(u.headers, x, y, 100, 25, g)
		x += 106
		wiggle := g && prestackState.display == 1
		setText(u.wiggleDecimLabel, map[bool]string{true: "减采样", false: "色标"}[wiggle])
		place(u.wiggleDecimLabel, x, y+2, 62, 22, g)
		place(u.palette, x+66, y, 145, 24, g && !wiggle)
		place(u.wiggleDecim, x+66, y, 145, 24, wiggle)
		x += 217
		place(u.gainMinus, x, y, 58, 24, g)
		x += 64
		place(u.gain, x, y+2, 40, 20, g)
		x += 46
		place(u.gainPlus, x, y, 58, 24, g)
		x += 64
		place(u.agc, x, y, 66, 24, g)
		x += 72
		place(u.exportGather, x, y, 82, 24, g)
		x += 88
		place(u.exportCSV, x, y, 82, 24, g)
	}
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
	place(u.qcExport, 18, 78, 130, 27, prestackState.page == 4)
	comparePage := prestackState.page == 3
	place(u.compareOpenB, 18, 78, 92, 27, comparePage)
	place(u.compareCloseB, 116, 78, 92, 27, comparePage && prestackState.compareBDataset != nil)
	place(u.compareExport, 214, 78, 145, 27, comparePage && prestackState.compareBDataset != nil)
}
func prestackSceneRect() RECT {
	r := clientRect(prestackHwnd)
	// Gather and raw file-order modes both use exactly two toolbar rows. Keep
	// this in the same pure layout contract used by QC and page painting so a
	// resize cannot leave an old frame under the footer or toolbar.
	l := prestackPanelLayoutForSize(int(r.Right), int(r.Bottom), prestackState.page)
	return prestackLayoutRECT(l.Scene)
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
	if prestackState.page == 4 {
		// QC charts and the bottom progress/status controls can move outside
		// the seismic scene after a resize.  Invalidate the whole client so the
		// off-screen white background removes pixels from the old geometry.
		pInvalidateRect.Call(prestackHwnd, 0, 0)
		return
	}
	r := clientRect(prestackHwnd)
	r.Top = 70
	r.Bottom -= 29
	pInvalidateRect.Call(prestackHwnd, uintptr(unsafe.Pointer(&r)), 0)
}
func setPrestackStatus(s string) { setText(prestackUI.status, s) }

func savePrestackCSVDialog(owner uintptr, defaultName string) string {
	buf := make([]uint16, 32768)
	if defaultName != "" {
		u := syscall.StringToUTF16(defaultName)
		if len(u) > len(buf) {
			u = u[:len(buf)]
		}
		copy(buf, u)
	}
	fil := []uint16{'C', 'S', 'V', ' ', 'f', 'i', 'l', 'e', ' ', '(', '*', '.', 'c', 's', 'v', ')', 0, '*', '.', 'c', 's', 'v', 0, 0}
	of := OPENFILENAME{LStructSize: uint32(unsafe.Sizeof(OPENFILENAME{})), HwndOwner: owner,
		LpstrFilter: uintptr(unsafe.Pointer(&fil[0])), NFilterIndex: 1, LpstrFile: uintptr(unsafe.Pointer(&buf[0])),
		NMaxFile: uint32(len(buf)), Flags: OFN_EXPLORER | OFN_OVERWRITEPROMPT, LpstrDefExt: uintptr(unsafe.Pointer(u16("csv")))}
	if r, _, _ := pGetSaveFileNameW.Call(uintptr(unsafe.Pointer(&of))); r != 0 {
		return syscall.UTF16ToString(buf)
	}
	return ""
}

func prestackExportStem() string {
	name := filepath.Base(prestackState.dataset.Path)
	name = strings.TrimSuffix(name, filepath.Ext(name))
	key := "all"
	if !prestackState.selection.Key.All {
		key = prestackState.selection.Key.String()
	}
	for _, r := range []string{"\\", "/", ":", "*", "?", "\"", "<", ">", "|", " "} {
		key = strings.ReplaceAll(key, r, "_")
	}
	return name + "_" + prestackGatherKindLabel(prestackState.selection.Type) + "_" + key
}

func postPrestackExportProgress(generation int64, done, total int) {
	if prestackHwnd == 0 || total <= 0 {
		return
	}
	pct := done * 100 / total
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	pPostMessageW.Call(prestackHwnd, WM_PRESTACK_EXPORT_PROGRESS, uintptr(generation), uintptr(pct))
}

func exportPrestackGatherSEGY() {
	visibleTraces, _ := prestackDisplayedTraces()
	if prestackState.dataset == nil || prestackState.index == nil || len(visibleTraces) == 0 {
		message(prestackHwnd, "导出道集", "当前没有可导出的道集。", MB_OK|MB_ICONINFORMATION)
		return
	}
	path := saveSegyDialog(prestackHwnd, prestackExportStem()+".sgy")
	if path == "" {
		return
	}
	cancelPrestackExport()
	ctx, cancel := context.WithCancel(context.Background())
	prestackState.exportCancel = cancel
	gen := atomic.AddInt64(&prestackGeneration, 1)
	prestackState.exportGeneration = gen
	datasetGeneration := prestackState.datasetGeneration
	// Export the current logical view range, not the screen-raster support
	// columns.  The latter are intentionally downsampled for display and must
	// never silently reduce the physical traces written to SEG-Y.
	source, traces := prestackState.dataset.Path, append([]int64(nil), visibleTraces...)
	s0, s1 := prestackState.sampleFirst, prestackState.sampleLast
	setPrestackProgress(false, "正在导出道集")
	setPrestackStatus(fmt.Sprintf("正在导出道集：%d 道 × %d 样点…", len(traces), s1-s0+1))
	go func() {
		var err error
		lastProgress := time.Time{}
		if f, e := segy.Open(source); e != nil {
			err = e
		} else {
			err = f.ExportSection(path, segy.SectionExportOptions{TraceIndices: traces, SampleStart: s0, SampleEnd: s1, Context: ctx,
				Progress: func(done, total int) {
					if done == total || time.Since(lastProgress) >= 80*time.Millisecond {
						lastProgress = time.Now()
						postPrestackExportProgress(gen, done, total)
					}
				},
			})
			f.Close()
		}
		select {
		case prestackExportDone <- prestackExportResult{generation: gen, datasetGeneration: datasetGeneration, path: path, format: "SEG-Y", traces: len(traces), samples: s1 - s0 + 1, err: err}:
		default:
			// The window may have been closed while an older export completion is
			// still queued. Do not leave the worker blocked on a UI-only channel.
		}
		if prestackHwnd != 0 {
			pPostMessageW.Call(prestackHwnd, WM_PRESTACK_EXPORT_DONE, 0, 0)
		}
	}()
}

func exportPrestackGatherCSV() {
	visibleTraces, _ := prestackDisplayedTraces()
	if prestackState.dataset == nil || prestackState.index == nil || len(visibleTraces) == 0 {
		message(prestackHwnd, "导出清单", "当前没有可导出的道集。", MB_OK|MB_ICONINFORMATION)
		return
	}
	path := savePrestackCSVDialog(prestackHwnd, prestackExportStem()+".csv")
	if path == "" {
		return
	}
	cancelPrestackExport()
	ctx, cancel := context.WithCancel(context.Background())
	prestackState.exportCancel = cancel
	gen := atomic.AddInt64(&prestackGeneration, 1)
	prestackState.exportGeneration = gen
	datasetGeneration := prestackState.datasetGeneration
	idx, traces := prestackState.index, append([]int64(nil), visibleTraces...)
	s0, s1 := prestackState.sampleFirst, prestackState.sampleLast
	selection := prestackState.selection
	setPrestackProgress(false, "正在导出清单")
	setPrestackStatus(fmt.Sprintf("正在导出道集清单：%d 道…", len(traces)))
	go func() {
		var err error
		f, e := os.Create(path)
		if e != nil {
			err = e
		} else {
			w := csv.NewWriter(f)
			err = w.Write([]string{"output_order", "physical_trace_1based", "gather_type", "gather_key", "cdp", "inline", "crossline", "shot", "receiver", "header_offset", "computed_offset", "azimuth", "source_x", "source_y", "receiver_x", "receiver_y", "midpoint_x", "midpoint_y", "sample_start_1based", "sample_end_1based"})
			if err == nil {
				for i, trace := range traces {
					if ctx.Err() != nil {
						err = ctx.Err()
						break
					}
					if trace < 0 || trace >= int64(len(idx.Records)) {
						continue
					}
					r := idx.Records[trace]
					err = w.Write([]string{strconv.Itoa(i + 1), strconv.FormatInt(trace+1, 10), prestackGatherKindLabel(selection.Type), selection.Key.String(), strconv.FormatInt(int64(r.CDP), 10), strconv.FormatInt(int64(r.Inline), 10), strconv.FormatInt(int64(r.Crossline), 10), strconv.FormatInt(int64(r.SourceID), 10), strconv.FormatInt(int64(r.ReceiverID), 10), strconv.FormatFloat(r.HeaderOffset, 'g', -1, 64), strconv.FormatFloat(r.ComputedOffset, 'g', -1, 64), strconv.FormatFloat(r.Azimuth, 'g', -1, 64), strconv.FormatFloat(r.SourceX, 'g', -1, 64), strconv.FormatFloat(r.SourceY, 'g', -1, 64), strconv.FormatFloat(r.ReceiverX, 'g', -1, 64), strconv.FormatFloat(r.ReceiverY, 'g', -1, 64), strconv.FormatFloat(r.MidpointX, 'g', -1, 64), strconv.FormatFloat(r.MidpointY, 'g', -1, 64), strconv.Itoa(s0 + 1), strconv.Itoa(s1 + 1)})
					if err != nil {
						break
					}
					if i == len(traces)-1 || i%maxInt(1, len(traces)/100) == 0 {
						postPrestackExportProgress(gen, i+1, len(traces))
					}
				}
			}
			w.Flush()
			if err == nil {
				err = w.Error()
			}
			if closeErr := f.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				_ = os.Remove(path)
			}
		}
		select {
		case prestackExportDone <- prestackExportResult{generation: gen, datasetGeneration: datasetGeneration, path: path, format: "CSV", traces: len(traces), samples: s1 - s0 + 1, err: err}:
		default:
			// See the SEG-Y export path above: cancellation must not strand the
			// goroutine if the native window is already gone.
		}
		if prestackHwnd != 0 {
			pPostMessageW.Call(prestackHwnd, WM_PRESTACK_EXPORT_DONE, 0, 0)
		}
	}()
}

func receivePrestackExport() {
	select {
	case r := <-prestackExportDone:
		if r.generation != prestackState.exportGeneration || r.datasetGeneration != prestackState.datasetGeneration {
			return
		}
		prestackState.exportCancel = nil
		if r.err != nil {
			setPrestackProgress(true, "导出失败")
			setPrestackStatus("导出失败：" + r.err.Error())
			return
		}
		setPrestackProgress(true, "导出完成")
		setPrestackStatus(fmt.Sprintf("导出完成 | %s | %d 道 × %d 样点 | %s", r.format, r.traces, r.samples, r.path))
	default:
	}
}

func currentPrestackAsyncToken() prestackAsyncToken {
	r := prestackSceneRect()
	return prestackAsyncToken{owner: prestackHwnd, ownerToken: prestackState.ownerToken,
		datasetGeneration: prestackState.datasetGeneration, selectionGeneration: prestackState.selectionGeneration,
		workspaceGeneration: prestackState.workspaceGeneration, sizeGeneration: prestackState.resizeGeneration,
		sceneWidth: int(r.Right - r.Left), sceneHeight: int(r.Bottom - r.Top), page: prestackState.page}
}

func prestackAsyncSourceMatches(a, b prestackAsyncToken) bool {
	return a.owner != 0 && a.owner == b.owner && a.ownerToken == b.ownerToken &&
		a.datasetGeneration == b.datasetGeneration && a.workspaceGeneration == b.workspaceGeneration
}

func prestackAsyncRenderMatches(a, b prestackAsyncToken) bool {
	return prestackAsyncSourceMatches(a, b) && a.selectionGeneration == b.selectionGeneration &&
		a.sizeGeneration == b.sizeGeneration &&
		a.page == 0 && b.page == 0
}

func prestackValidRenderBuffers(r *prestackRenderResult) bool {
	return r != nil && r.width > 0 && r.height > 0 && r.width <= 4096 && r.height <= 1536 &&
		len(r.indices) == r.width*r.height && len(r.bgra) == r.width*r.height*4
}

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
	for _, combo := range []uintptr{prestackUI.kind, prestackUI.key, prestackUI.sort, prestackUI.axis, prestackUI.display, prestackUI.wiggleDecim, prestackUI.rawSampleMode, prestackUI.palette} {
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
		secondary := prestackSecondaryModeFromCombo(int(v))
		if secondary == prestackState.selection.Secondary {
			return
		}
		prestackState.selection.Secondary = secondary
		// Keep the historical Sort field synchronized for callers that still
		// consume the four scalar modes.  Category secondary modes are handled
		// by the index with physical order as their scalar tie-breaker.
		switch secondary {
		case prestackcore.SecondaryPhysical:
			prestackState.selection.Sort = prestackcore.SortPhysical
		case prestackcore.SecondaryOffset:
			prestackState.selection.Sort = prestackcore.SortOffset
		case prestackcore.SecondaryAbsoluteOffset:
			prestackState.selection.Sort = prestackcore.SortAbsoluteOffset
		case prestackcore.SecondaryAzimuth:
			prestackState.selection.Sort = prestackcore.SortAzimuth
		default:
			prestackState.selection.Sort = prestackcore.SortPhysical
		}
		if len(prestackState.selection.Keys) > 0 {
			applyPrestackGatherSelection()
		} else {
			selectPrestackGather(prestackState.keyIndex)
		}
	case IDPRESTACK_AXIS:
		v, _, _ := pSendMessageW.Call(prestackUI.axis, CB_GETCURSEL, 0, 0)
		axis := prestackcore.AxisMode(v)
		if axis == prestackState.selection.Axis {
			return
		}
		prestackState.selection.Axis = axis
		if v == 1 {
			prestackState.selection.Secondary = prestackcore.SecondaryOffset
			prestackState.selection.Sort = prestackcore.SortOffset
			pSendMessageW.Call(prestackUI.sort, CB_SETCURSEL, uintptr(prestackSecondaryComboIndex(prestackcore.SecondaryOffset)), 0)
		}
		if len(prestackState.selection.Keys) > 0 {
			applyPrestackGatherSelection()
		} else {
			selectPrestackGather(prestackState.keyIndex)
		}
	case IDPRESTACK_DISPLAY:
		v, _, _ := pSendMessageW.Call(prestackUI.display, CB_GETCURSEL, 0, 0)
		if int(v) == prestackState.display {
			return
		}
		prestackState.display = int(v)
		prestackState.compareDisplayGeneration = atomic.AddInt64(&prestackGeneration, 1)
		clearPrestackCompareRender()
		layoutPrestackControls()
		startPrestackRender()
		if prestackState.page == 3 && prestackState.compareBDataset != nil {
			startPrestackCompareRender()
		}
	case IDPRESTACK_WIGGLE_DECIM:
		v, _, _ := pSendMessageW.Call(prestackUI.wiggleDecim, CB_GETCURSEL, 0, 0)
		factor := prestackWiggleDecimationValue(int(v))
		if factor == prestackState.wiggleDecimation {
			return
		}
		prestackState.wiggleDecimation = factor
		prestackState.compareDisplayGeneration = atomic.AddInt64(&prestackGeneration, 1)
		clearPrestackCompareRender()
		if prestackState.display == 1 {
			startPrestackRender()
		}
		if prestackState.page == 3 && prestackState.compareBDataset != nil {
			startPrestackCompareRender()
		}
	case IDPRESTACK_PALETTE:
		v, _, _ := pSendMessageW.Call(prestackUI.palette, CB_GETCURSEL, 0, 0)
		if int(v) == prestackState.palette {
			return
		}
		prestackState.palette = int(v)
		prestackState.compareDisplayGeneration = atomic.AddInt64(&prestackGeneration, 1)
		clearPrestackCompareRender()
		prestackState.bgra = crookedPaletteBGRA(prestackState.indices, int(v))
		invalidatePrestackScene()
		if prestackState.page == 3 && prestackState.compareBDataset != nil {
			startPrestackCompareRender()
		}
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
	// The progress control and its completion badge are child windows.  When
	// QC is active, changing their visibility can expose pixels from the old
	// chart layout; redraw the parent immediately so the new footer boundary
	// is reflected in the same frame.
	if prestackHwnd != 0 {
		layoutPrestackControls()
		redrawPrestackWholeClient()
	}
}

func cancelPrestackJobs() {
	if prestackState.compareBCancel != nil {
		prestackState.compareBCancel()
		prestackState.compareBCancel = nil
	}
	if prestackState.indexCancel != nil {
		prestackState.indexCancel()
		prestackState.indexCancel = nil
	}
	if prestackState.renderCancel != nil {
		prestackState.renderCancel()
		prestackState.renderCancel = nil
	}
	if prestackState.exportCancel != nil {
		prestackState.exportCancel()
		prestackState.exportCancel = nil
	}
	if prestackState.compareRenderCancel != nil {
		prestackState.compareRenderCancel()
		prestackState.compareRenderCancel = nil
	}
	prestackState.indexGeneration = atomic.AddInt64(&prestackGeneration, 1)
	prestackState.renderGeneration = atomic.AddInt64(&prestackGeneration, 1)
	prestackState.exportGeneration = atomic.AddInt64(&prestackGeneration, 1)
	prestackState.compareBGeneration = atomic.AddInt64(&prestackGeneration, 1)
	prestackState.compareBMappingGeneration = atomic.AddInt64(&prestackGeneration, 1)
	prestackState.compareModeGeneration = atomic.AddInt64(&prestackGeneration, 1)
	prestackState.compareRenderGeneration = atomic.AddInt64(&prestackGeneration, 1)
	prestackState.loading, prestackState.rendering = false, false
	prestackState.compareBLoading = false
	prestackDeliveryMu.Lock()
	prestackDeliveryIndexGeneration = prestackState.indexGeneration
	prestackDeliveryRenderGeneration = prestackState.renderGeneration
	prestackPendingIndex = nil
	prestackPendingRender = nil
	prestackPendingCompareB = nil
	prestackPendingCompareRender = nil
	prestackDeliveryMu.Unlock()
	prestackState.compareRendering = false
	prestackState.compareA, prestackState.compareB, prestackState.compareDelta = nil, nil, nil
	prestackState.compareWidth, prestackState.compareHeight = 0, 0
}
func startPrestackIndex(detect bool) {
	if prestackState.dataset == nil {
		return
	}
	cancelPrestackJobs()
	// Rebuilding A's index also establishes a new mapping snapshot.  Keep this
	// generation separate from selectionGeneration so an old Compare B result
	// cannot be paired with records decoded using a newer header mapping.
	prestackState.mappingGeneration = atomic.AddInt64(&prestackGeneration, 1)
	ctx, cancel := context.WithCancel(context.Background())
	prestackState.indexCancel = cancel
	gen := atomic.AddInt64(&prestackGeneration, 1)
	prestackState.indexGeneration = gen
	prestackState.selectionGeneration = atomic.AddInt64(&prestackGeneration, 1)
	token := currentPrestackAsyncToken()
	prestackDeliveryMu.Lock()
	prestackDeliveryIndexGeneration = gen
	prestackDeliveryMu.Unlock()
	prestackState.loading = true
	prestackState.needsIndex = false
	data, mapping, owner := prestackState.dataset, prestackState.mapping, prestackHwnd
	prestackState.index = nil
	clearPrestackImage()
	prestackState.gather = prestackcore.GatherResult{}
	setPrestackStatus("正在检查道头映射；仅扫描道头，不读取全文件振幅…")
	setPrestackProgress(false, "扫描道头")
	pSendMessageW.Call(prestackUI.progress, PBM_SETPOS, 0, 0)
	invalidatePrestackScene()
	go func() {
		result := &prestackIndexResult{generation: gen, token: token}
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
					prestackDeliveryMu.Lock()
					if prestackDeliveryHwnd == owner && prestackDeliveryOwnerToken == token.ownerToken && prestackDeliveryIndexGeneration == gen && ctx.Err() == nil {
						atomic.StoreInt64(&prestackProgress, pct)
						pPostMessageW.Call(owner, WM_PRESTACK_PROGRESS, uintptr(gen), 0)
					}
					prestackDeliveryMu.Unlock()
				})
			}
			result.err = err
		}
		if ctx.Err() != nil {
			return
		}
		prestackDeliveryMu.Lock()
		defer prestackDeliveryMu.Unlock()
		if prestackDeliveryHwnd != owner || prestackDeliveryOwnerToken != token.ownerToken || prestackDeliveryIndexGeneration != gen || ctx.Err() != nil {
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
	if r == nil || gen != prestackState.indexGeneration || !prestackAsyncSourceMatches(r.token, currentPrestackAsyncToken()) {
		return
	}
	prestackState.loading = false
	if prestackState.indexCancel != nil {
		prestackState.indexCancel()
		prestackState.indexCancel = nil
	}
	if r.err != nil {
		setPrestackProgress(true, "加载失败")
		setPrestackStatus("叠前索引失败：" + r.err.Error())
		return
	}
	if r.detected {
		prestackState.detection = r.detection
		prestackState.mapping = r.detection.Mapping
		prestackState.mappingGeneration = atomic.AddInt64(&prestackGeneration, 1)
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
	prestackState.mappingGeneration = atomic.AddInt64(&prestackGeneration, 1)
	startPrestackIndex(false)
}

func selectPrestackGatherType(kind prestackcore.GatherType) {
	if prestackState.index == nil {
		return
	}
	prestackClearLink()
	invalidatePrestackRender()
	// Changing the primary category invalidates a command that was parsed
	// against the previous key list.
	prestackState.selection.Keys = nil
	setText(prestackUI.multiEdit, "")
	if kind == prestackcore.GatherRaw {
		prestackState.selection.Type = kind
		prestackState.selection.Sort = prestackcore.SortPhysical
		prestackState.selection.Secondary = prestackcore.SecondaryPhysical
		prestackState.selection.Axis = prestackcore.AxisTrace
		pSendMessageW.Call(prestackUI.sort, CB_SETCURSEL, uintptr(prestackSecondaryComboIndex(prestackcore.SecondaryPhysical)), 0)
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
	// The zero value is kept as a compatibility sentinel in GatherSelection.
	// The UI, however, must make the new two-level behavior explicit on the
	// first non-raw selection so an all-range CMP/Shot/Receiver/Offset view is
	// grouped by its primary category instead of silently retaining the legacy
	// physical-order result.
	if prestackState.selection.Secondary == prestackcore.SecondaryUnset {
		secondary := prestackcore.SecondaryPhysical
		switch prestackState.selection.Sort {
		case prestackcore.SortOffset:
			secondary = prestackcore.SecondaryOffset
		case prestackcore.SortAbsoluteOffset:
			secondary = prestackcore.SecondaryAbsoluteOffset
		case prestackcore.SortAzimuth:
			secondary = prestackcore.SecondaryAzimuth
		}
		prestackState.selection.Secondary = secondary
		pSendMessageW.Call(prestackUI.sort, CB_SETCURSEL, uintptr(prestackSecondaryComboIndex(secondary)), 0)
	}
	// Make the physical-order choice explicit for keyed gathers.  The core
	// index uses an explicit secondary mode to enable natural primary grouping
	// for an all-range selection; leaving the zero-value unset would preserve
	// legacy raw-file ordering and make "全部范围" look like one acquisition
	// group.
	if prestackState.selection.Secondary == prestackcore.SecondaryUnset {
		prestackState.selection.Secondary = prestackcore.SecondaryPhysical
		prestackState.selection.Sort = prestackcore.SortPhysical
		pSendMessageW.Call(prestackUI.sort, CB_SETCURSEL, uintptr(prestackSecondaryComboIndex(prestackcore.SecondaryPhysical)), 0)
	}
	if kind == prestackcore.GatherOffset {
		if prestackState.offsetBinSize <= 0 {
			prestackState.offsetBinSize = prestackcore.DefaultOffsetBinSize
		}
		prestackState.selection.OffsetBinSize = prestackState.offsetBinSize
		prestackState.keys = prependPrestackAllRange(prestackState.index.AvailableGathersConfigured(kind, prestackState.offsetBinSize))
	} else if kind == prestackcore.GatherAzimuth {
		if prestackState.selection.AzimuthBinSize <= 0 {
			prestackState.selection.AzimuthBinSize = prestackcore.DefaultAzimuthBinSize
		}
		prestackState.keys = prependPrestackAllRange(prestackState.index.AvailableGathersAzimuthConfigured(prestackState.selection.AzimuthBinSize))
	} else if kind == prestackcore.GatherCMP && prestackState.cmpBinSize > 0 {
		cfg := currentPrestackCMPBinConfig()
		prestackState.selection.CMPBin = cfg
		prestackState.keys = prependPrestackAllRange(prestackState.index.AvailableCMPGathersConfigured(cfg))
	} else {
		prestackState.selection.CMPBin = prestackcore.CMPBinConfig{}
		prestackState.keys = prependPrestackAllRange(prestackState.index.AvailableGathers(kind))
	}
	// The controls are part of the toolbar and must follow the selected kind
	// immediately, even before the first image arrives.
	layoutPrestackControls()
	pSendMessageW.Call(prestackUI.key, CB_RESETCONTENT, 0, 0)
	configurePrestackKeyCombo()
	best, preferred := 0, -1
	for i, k := range prestackState.keys {
		pSendMessageW.Call(prestackUI.key, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(k.String()))))
		if kind == prestackcore.GatherOffset && previousType == kind && previousKey.OffsetBin && k.OffsetBin &&
			math.Abs(k.OffsetCenter-previousKey.OffsetCenter) <= math.Max(1e-9, math.Abs(k.OffsetCenter)*1e-12) {
			preferred = i
		} else if kind == prestackcore.GatherCMP && previousType == kind && previousKey.CMPBin && k.CMPBin &&
			previousKey.CMPBinXIndex == k.CMPBinXIndex && previousKey.CMPBinYIndex == k.CMPBinYIndex {
			preferred = i
		} else if kind == prestackcore.GatherAzimuth && previousType == kind && previousKey.AzimuthBin && k.AzimuthBin &&
			previousKey.AzimuthBinIndex == k.AzimuthBinIndex {
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
	prestackState.selectionGeneration = atomic.AddInt64(&prestackGeneration, 1)
	prestackState.rendering = false
	prestackState.indices = nil
	prestackState.bgra = nil
	prestackState.imageWidth = 0
	prestackState.imageHeight = 0
	prestackState.renderedTraces = nil
	prestackState.renderedPositions = nil
	prestackState.renderedColumns = 0
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
	prestackState.selection.Keys = nil
	setText(prestackUI.multiEdit, "")
	if kind != prestackcore.GatherRaw && prestackState.selection.Secondary == prestackcore.SecondaryUnset {
		prestackState.selection.Secondary = prestackcore.SecondaryPhysical
		prestackState.selection.Sort = prestackcore.SortPhysical
		pSendMessageW.Call(prestackUI.sort, CB_SETCURSEL, uintptr(prestackSecondaryComboIndex(prestackcore.SecondaryPhysical)), 0)
	}
	prestackState.selection.OffsetBinSize = prestackState.offsetBinSize
	prestackState.suppressPageRender = true
	setPrestackPage(0)
	prestackState.suppressPageRender = false
	// Keep the visible gather-type control in sync with the exact target. This
	// matters when a Bin is opened from Geometry while the user previously had
	// Shot, Receiver, or Common Offset selected.
	pSendMessageW.Call(prestackUI.kind, CB_SETCURSEL, uintptr(kind), 0)
	if kind == prestackcore.GatherOffset {
		prestackState.keys = prependPrestackAllRange(prestackState.index.AvailableGathersConfigured(kind, prestackState.offsetBinSize))
	} else if kind == prestackcore.GatherAzimuth {
		if prestackState.selection.AzimuthBinSize <= 0 {
			prestackState.selection.AzimuthBinSize = prestackcore.DefaultAzimuthBinSize
		}
		prestackState.keys = prependPrestackAllRange(prestackState.index.AvailableGathersAzimuthConfigured(prestackState.selection.AzimuthBinSize))
	} else if kind == prestackcore.GatherCMP && prestackState.cmpBinSize > 0 {
		prestackState.keys = prependPrestackAllRange(prestackState.index.AvailableCMPGathersConfigured(currentPrestackCMPBinConfig()))
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
	cancelPrestackExport()
	prestackClearLink()
	invalidatePrestackRender()
	if prestackState.selection.Type == prestackcore.GatherRaw {
		applyPrestackRawRange()
		return
	}
	index = clampInt(index, 0, len(prestackState.keys)-1)
	prestackState.keyIndex = index
	// A normal combo selection replaces any previous multi-bin command.
	prestackState.selection.Keys = nil
	setText(prestackUI.multiEdit, "")
	prestackState.selection.Key = prestackState.keys[index]
	prestackState.selection.OffsetBinSize = prestackState.offsetBinSize
	if prestackState.selection.Type == prestackcore.GatherAzimuth {
		if prestackState.selection.AzimuthBinSize <= 0 {
			prestackState.selection.AzimuthBinSize = prestackcore.DefaultAzimuthBinSize
		}
	}
	if prestackState.selection.Type == prestackcore.GatherCMP && prestackState.selection.Key.CMPBin {
		prestackState.selection.CMPBin = prestackcore.CMPBinConfig{Size: prestackState.selection.Key.CMPBinSize, OriginX: prestackState.selection.Key.CMPBinOriginX, OriginY: prestackState.selection.Key.CMPBinOriginY}
	} else if prestackState.selection.Type == prestackcore.GatherCMP {
		prestackState.selection.CMPBin = currentPrestackCMPBinConfig()
	}
	pSendMessageW.Call(prestackUI.key, CB_SETCURSEL, uintptr(index), 0)
	applyPrestackGatherSelection()
}

// applyPrestackGatherSelection resolves the current immutable selection and
// starts one complete render.  Keeping this step separate lets the multi-bin
// command use the exact same validation, sample reset and generation guards as
// an ordinary combo selection without temporarily selecting a single key.
func applyPrestackGatherSelection() {
	if prestackState.index == nil {
		return
	}
	cancelPrestackExport()
	prestackClearLink()
	invalidatePrestackRender()
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
	if prestackState.compareBDataset != nil {
		refreshPrestackCompareMatch()
	}
}

// startPrestackCompareB loads a second dataset/index without touching A's
// reader, gather, mapping or render generations.  The worker owns a short
// lived SEG-Y reader only while building the metadata index; no amplitude
// samples are copied or retained.
func startPrestackCompareB(path string) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || prestackState.dataset == nil {
		return
	}
	if strings.EqualFold(filepath.Clean(path), filepath.Clean(prestackState.dataset.Path)) {
		message(prestackHwnd, "叠前 A/B", "A 与 B 不能使用同一个 SEG-Y 文件。", MB_OK|MB_ICONINFORMATION)
		return
	}
	if prestackState.compareBCancel != nil {
		prestackState.compareBCancel()
		prestackState.compareBCancel = nil
	}
	// A replacement B must not leave the previous A/B/Δ frame visible while
	// its metadata index is being built. Keep A's gather untouched, but clear
	// only the compare exchange buffers and their pending generation.
	clearPrestackCompareRender()
	ctx, cancel := context.WithCancel(context.Background())
	prestackState.compareBCancel = cancel
	gen := atomic.AddInt64(&prestackGeneration, 1)
	prestackState.compareBGeneration = gen
	prestackState.compareBMappingGeneration = atomic.AddInt64(&prestackGeneration, 1)
	prestackState.compareModeGeneration = atomic.AddInt64(&prestackGeneration, 1)
	prestackState.compareBLoading = true
	prestackState.compareBError = ""
	prestackState.compareBDataset = nil
	prestackState.compareBIndex = nil
	prestackState.compareBMatch = prestackcore.CompareMatchResult{}
	prestackState.compareBAxis = prestackcore.SampleAxisCompatibility{}
	prestackState.compareBMapping = prestackState.mapping
	owner, ownerToken := prestackHwnd, prestackState.ownerToken
	mapping := prestackState.compareBMapping
	workspaceGeneration := prestackState.workspaceGeneration
	datasetGeneration := prestackState.datasetGeneration
	selectionGeneration := prestackState.selectionGeneration
	mappingGeneration := prestackState.compareBMappingGeneration
	compareModeGeneration := prestackState.compareModeGeneration
	setPrestackStatus("正在加载 B：" + filepath.Base(path) + "（仅扫描道头）")
	layoutPrestackControls()
	redrawPrestackWholeClient()
	go func() {
		result := &prestackCompareBResult{generation: gen, datasetGeneration: datasetGeneration, selectionGeneration: selectionGeneration, mappingGeneration: mappingGeneration, compareModeGeneration: compareModeGeneration, workspaceGeneration: workspaceGeneration, ownerToken: ownerToken, owner: owner, mapping: mapping}
		manager := dataset.NewManager()
		data, err := manager.Open(path)
		if err != nil {
			result.err = err
		} else {
			result.dataset = data
			reader, openErr := data.OpenReader()
			if openErr != nil {
				result.err = openErr
			} else {
				result.index, result.err = prestackcore.BuildIndex(ctx, reader, mapping, func(prestackcore.IndexProgress) {})
				_ = reader.Close()
			}
		}
		if ctx.Err() != nil {
			return
		}
		prestackDeliveryMu.Lock()
		defer prestackDeliveryMu.Unlock()
		if prestackDeliveryHwnd != owner || prestackDeliveryOwnerToken != ownerToken || prestackState.compareBGeneration != gen || prestackState.workspaceGeneration != workspaceGeneration || prestackState.datasetGeneration != datasetGeneration || prestackState.selectionGeneration != selectionGeneration || prestackState.compareBMappingGeneration != mappingGeneration || prestackState.compareModeGeneration != compareModeGeneration || ctx.Err() != nil {
			return
		}
		prestackPendingCompareB = result
		if posted, _, _ := pPostMessageW.Call(owner, WM_PRESTACK_COMPARE_B_READY, uintptr(gen), 0); posted == 0 {
			prestackPendingCompareB = nil
		}
	}()
}

func cancelPrestackCompareB() {
	if prestackState.compareBCancel != nil {
		prestackState.compareBCancel()
		prestackState.compareBCancel = nil
	}
	prestackState.compareBGeneration = atomic.AddInt64(&prestackGeneration, 1)
	prestackState.compareBMappingGeneration = atomic.AddInt64(&prestackGeneration, 1)
	prestackState.compareModeGeneration = atomic.AddInt64(&prestackGeneration, 1)
	prestackState.compareBLoading = false
	prestackState.compareBDataset = nil
	prestackState.compareBIndex = nil
	prestackState.compareBError = ""
	prestackState.compareBMatch = prestackcore.CompareMatchResult{}
	prestackState.compareBAxis = prestackcore.SampleAxisCompatibility{}
	clearPrestackCompareRender()
	prestackDeliveryMu.Lock()
	prestackPendingCompareB = nil
	prestackDeliveryMu.Unlock()
	layoutPrestackControls()
	redrawPrestackWholeClient()
}

func receivePrestackCompareB(gen int64) {
	prestackDeliveryMu.Lock()
	r := prestackPendingCompareB
	if r != nil && r.generation == gen {
		prestackPendingCompareB = nil
	} else {
		r = nil
	}
	prestackDeliveryMu.Unlock()
	if r == nil || gen != prestackState.compareBGeneration || r.owner != prestackHwnd || r.ownerToken != prestackState.ownerToken || r.workspaceGeneration != prestackState.workspaceGeneration || r.datasetGeneration != prestackState.datasetGeneration || r.selectionGeneration != prestackState.selectionGeneration || r.mappingGeneration != prestackState.compareBMappingGeneration || r.compareModeGeneration != prestackState.compareModeGeneration {
		return
	}
	prestackState.compareBLoading = false
	if prestackState.compareBCancel != nil {
		prestackState.compareBCancel()
		prestackState.compareBCancel = nil
	}
	if r.err != nil || r.dataset == nil || r.index == nil {
		prestackState.compareBError = "B 索引失败：" + errorText(r.err, "未知错误")
		setPrestackStatus(prestackState.compareBError + "；A 仍可继续浏览")
		if prestackState.page == 3 {
			startPrestackCompareRender()
		}
		layoutPrestackControls()
		redrawPrestackWholeClient()
		return
	}
	prestackState.compareBDataset = r.dataset
	prestackState.compareBIndex = r.index
	prestackState.compareBMapping = r.mapping
	prestackState.compareBError = ""
	if prestackState.index != nil && len(prestackState.gather.TraceIndices) > 0 {
		bGather, err := r.index.Gather(prestackState.selection)
		if err == nil {
			prestackState.compareBMatch, _ = prestackcore.MatchGatherResults(prestackState.index, prestackState.gather, r.index, bGather)
		} else {
			// A's current selection may not exist in B. Never silently replace it
			// with all B records: that produces plausible but incorrect matches.
			prestackState.compareBMatch = prestackcore.CompareMatchResult{}
			window := prestackcore.SampleWindow{Start: prestackState.sampleFirst, End: prestackState.sampleLast}
			prestackState.compareBAxis = prestackcore.SampleAxisCompatibility{AWindow: window, BWindow: window, Reason: "B 当前道集不可用"}
			setPrestackStatus("B 当前道集不可用；A 仍可继续浏览")
		}
	}
	if len(prestackState.compareBMatch.Pairs) > 0 {
		prestackState.compareBAxis = comparePrestackGatherAxes(prestackState.index, prestackState.gather, r.index, rGatherForSelection(r.index, prestackState.selection))
	}
	startPrestackCompareRender()
	setPrestackStatus(fmt.Sprintf("B 已加载：%s | 匹配 %d 对；A 顺序保持不变", filepath.Base(r.dataset.Path), len(prestackState.compareBMatch.Pairs)))
	layoutPrestackControls()
	redrawPrestackWholeClient()
}

func errorText(err error, fallback string) string {
	if err == nil {
		return fallback
	}
	return err.Error()
}

// applyPrestackMultiKeys parses the command edit against the complete key
// list currently available for the selected primary gather type.  It never
// invents bins: numeric values, ranges and #positions are resolved only to
// keys already present in the dropdown.
func applyPrestackMultiKeys() {
	if prestackState.index == nil || prestackState.selection.Type == prestackcore.GatherRaw {
		return
	}
	text := strings.TrimSpace(getText(prestackUI.multiEdit))
	if text == "" {
		message(prestackHwnd, "多道集", "请输入多个键值，例如 9150,9152 或 -40,0,40。", MB_OK|MB_ICONERROR)
		return
	}
	keys, err := parsePrestackMultiKeyCommand(text, prestackState.keys)
	if err != nil {
		message(prestackHwnd, "多道集", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	for _, key := range keys {
		if key.All {
			// Explicit 全部范围 is equivalent to the normal first dropdown item.
			prestackState.selection.Keys = nil
			prestackState.keyIndex = 0
			prestackState.selection.Key = prestackcore.GatherKey{All: true}
			pSendMessageW.Call(prestackUI.key, CB_SETCURSEL, 0, 0)
			setText(prestackUI.multiEdit, "")
			applyPrestackGatherSelection()
			return
		}
	}
	prestackState.selection.Keys = append([]prestackcore.GatherKey(nil), keys...)
	prestackState.selection.Key = prestackcore.GatherKey{All: true}
	prestackState.keyIndex = 0
	pSendMessageW.Call(prestackUI.key, CB_SETCURSEL, 0, 0)
	applyPrestackGatherSelection()
}

func cancelPrestackExport() {
	if prestackState.exportCancel != nil {
		prestackState.exportCancel()
		prestackState.exportCancel = nil
	}
	prestackState.exportGeneration = atomic.AddInt64(&prestackGeneration, 1)
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

// prestackRenderedColumns returns the exact source-column mapping used for the
// last exchanged raster.  A large gather is represented by one physical trace
// per output column; the complete logical gather remains in GatherResult and
// is still used for export. Falling back to the logical gather keeps the
// toolbar usable while the first frame is still loading.
func prestackRenderedColumns() ([]int64, []float64) {
	if len(prestackState.renderedTraces) > 0 {
		n := len(prestackState.renderedTraces)
		if len(prestackState.renderedPositions) < n {
			n = len(prestackState.renderedPositions)
		}
		if n > 0 {
			return prestackState.renderedTraces[:n], prestackState.renderedPositions[:n]
		}
	}
	return prestackDisplayedTraces()
}

// Offset display uses nearest actual traces on a physical offset axis. It
// never invents amplitudes or uses a physical SEG-Y trace number as an offset.
func prestackOffsetColumns(traces []int64, positions []float64, width int) ([]int64, []float64) {
	if len(traces) < 2 || len(positions) != len(traces) || positions[len(positions)-1] <= positions[0] {
		return append([]int64(nil), traces...), append([]float64(nil), positions...)
	}
	out := make([]int64, width)
	outPos := make([]float64, width)
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
		outPos[x] = positions[i]
	}
	return out, outPos
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

// prestackOutputColumns returns the physical trace represented by each raster
// column.  It is also used to build the exact source list handed to the
// renderer, so the exchanged bitmap and the pick/hover mapping stay in lock
// step for both Image and Wiggle (including Wiggle decimation).
func prestackOutputColumns(traces []int64, positions []float64, width int) ([]int64, []float64) {
	if len(traces) == 0 || width <= 0 {
		return nil, nil
	}
	out := make([]int64, width)
	var outPositions []float64
	if len(positions) == len(traces) {
		outPositions = make([]float64, width)
	}
	for x := 0; x < width; x++ {
		i := 0
		if width > 1 && len(traces) > 1 {
			i = int(math.Round(float64(x) * float64(len(traces)-1) / float64(width-1)))
		}
		if i < 0 {
			i = 0
		}
		if i >= len(traces) {
			i = len(traces) - 1
		}
		out[x] = traces[i]
		if len(outPositions) == width {
			outPositions[x] = positions[i]
		}
	}
	return out, outPositions
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
	prestackState.renderedTraces = nil
	prestackState.renderedPositions = nil
	prestackState.renderedColumns = 0
}

// schedulePrestackResizeRender posts one delayed flush for the current resize
// generation.  WM_SIZE can arrive many times per drag; doing work for each
// message both starves the UI and allows an old-size result to win the race.
func schedulePrestackResizeRender(owner uintptr, generation int64) {
	ownerToken := prestackState.ownerToken
	go func() {
		time.Sleep(100 * time.Millisecond)
		if owner == 0 || prestackHwnd != owner || prestackOwnerToken != ownerToken || prestackState.ownerToken != ownerToken {
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
	prestackState.selectionGeneration = atomic.AddInt64(&prestackGeneration, 1)
	prestackState.renderGeneration = gen
	token := currentPrestackAsyncToken()
	prestackDeliveryMu.Lock()
	prestackDeliveryRenderGeneration = gen
	prestackPendingRender = nil
	prestackDeliveryMu.Unlock()
	prestackState.rendering = true
	setPrestackProgress(false, "读取道集")
	// Keep the last complete frame until the replacement is ready.  This is
	// particularly important after a resize: the renderer works at the final
	// client dimensions, while WM_SIZE can deliver dozens of intermediate
	// rectangles.  receivePrestackRender exchanges the frame atomically, so a
	// cancelled/failed render can never leave a partially painted bitmap.
	r := prestackSceneRect()
	// The gather remains a complete logical trace list.  The raster only needs
	// one column per visible screen pixel; the old fixed 1536 ceiling made a
	// wide window look as if the all-range gather had been truncated.  Scale to
	// the actual scene width, with a conservative 4096-pixel upper bound for
	// very large monitors.
	w, h := minInt(4096, maxInt(2, int(r.Right-r.Left))), minInt(1536, maxInt(2, int(r.Bottom-r.Top)))
	mode := segy.DisplayAdaptive
	if prestackState.selection.Axis == prestackcore.AxisOffset {
		traces, positions = prestackOffsetColumns(traces, positions, w)
		mode = segy.DisplayNearest
	} else {
		// AxisTrace preserves the complete logical gather.  The SEG-Y renderer
		// already maps output columns to the nearest physical trace (and the
		// sparse path only reads support traces), so pre-rasterizing here loses
		// trace identity and makes zoom/pick labels jump between sampled points.
		// Keep the full list for deterministic first/middle/last mapping; only
		// the Wiggle mode below intentionally limits its display width.
	}
	if prestackState.display == 1 {
		factor := prestackState.wiggleDecimation
		// Keep the default compatible with the previous Wiggle density while
		// allowing 1×/2×/4× selections to make the trace spacing denser. The
		// hard ceiling prevents a noisy 15,000-sample gather from turning every
		// paint into an excessive number of GDI line/polygon operations.
		w = prestackWiggleRasterWidth(len(traces), w, factor)
		mode = segy.DisplayNearest
	}
	tracesForFrame, positionsForFrame := prestackOutputColumns(traces, positions, w)
	data, owner, palette := prestackState.dataset, prestackHwnd, prestackState.palette
	workspaceGeneration := prestackState.workspaceGeneration
	// Keep the displayed gain value exact.  Previously a hidden +0.5% was
	// added even when the toolbar showed 0%, which percentile-clipped the
	// strongest 0.5% of samples.  On gathers with a few very strong traces
	// this produced conspicuous black vertical bands while dragging/zooming
	// (palette index 0 is the maximum/black end of the default palette).
	options := segy.RenderOptions{Width: w, Height: h, SampleStart: prestackState.sampleFirst, SampleEnd: prestackState.sampleLast,
		AGC: prestackState.agc, ClipPercent: 99, GainPercent: math.Max(0, math.Min(49, prestackState.gain)), DisplayMode: mode, Workers: 2, ReadStrategy: segy.ReadStrategySparseMapped}
	setPrestackStatus(fmt.Sprintf("正在读取道集 %s | %d 道 | 样点 %d..%d", prestackSelectionKeyLabel(prestackState.selection), len(prestackState.gather.TraceIndices), options.SampleStart+1, options.SampleEnd+1))
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
		result := &prestackRenderResult{generation: gen, workspaceGeneration: workspaceGeneration, width: w, height: h, traces: tracesForFrame, positions: positionsForFrame, token: token, palette: palette}
		reader, err := data.OpenReader()
		if err == nil {
			// Render exactly the source columns selected for this frame.  In
			// particular, Wiggle's decimation factor is expressed by
			// tracesForFrame: passing the complete logical gather here would
			// make the renderer remap every output column across the full gather
			// and effectively hide the user's 1x/2x/4x/5x/8x choice.
			// Keeping the same list in the result mapping also makes Ctrl+click
			// and hover agree with the trace represented by each raster column.
			result.indices, result.stats, err = reader.RenderTraceIndices(tracesForFrame, options)
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
		if prestackDeliveryHwnd != owner || prestackDeliveryOwnerToken != token.ownerToken || prestackDeliveryRenderGeneration != gen || ctx.Err() != nil {
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
	if r == nil || gen != prestackState.renderGeneration || r.workspaceGeneration != prestackState.workspaceGeneration || !prestackAsyncRenderMatches(r.token, currentPrestackAsyncToken()) || !prestackValidRenderBuffers(r) {
		return
	}
	prestackState.rendering = false
	if r.err != nil {
		setPrestackProgress(true, "加载失败")
		setPrestackStatus("道集显示失败：" + r.err.Error())
		return
	}
	prestackState.indices = r.indices
	// The worker's palette is only a convenience for stale-result diagnostics;
	// recolour with the current palette at the exchange point so an E/Shift+E
	// change cannot be overwritten by an older render completion.
	prestackState.bgra = crookedPaletteBGRA(r.indices, prestackState.palette)
	prestackState.imageWidth, prestackState.imageHeight = r.width, r.height
	prestackState.renderedTraces = append([]int64(nil), r.traces...)
	prestackState.renderedPositions = append([]float64(nil), r.positions...)
	prestackState.renderedColumns = r.width
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
	} else if prestackState.selection.Key.All {
		keyText := "全部范围（全文件聚合）"
		if len(prestackState.selection.Keys) > 0 {
			keyText = fmt.Sprintf("多道集 %d 组（全文件范围内）", len(prestackState.selection.Keys))
		}
		setPrestackStatus(fmt.Sprintf("完成 | 一级 %s·%s | 二级 %s | 物理道 %d | 显示列 %d | %s%s | 左拖缩放，右拖平移，双击复位，Ctrl+单击查看真实道", kindLabel, keyText, prestackSecondaryLabel(prestackState.selection), len(prestackState.gather.TraceIndices), r.width, offsetLabel, gainHint))
	} else if prestackState.selection.Type == prestackcore.GatherOffset {
		setPrestackStatus(fmt.Sprintf("完成 | 一级 %s %s | 二级 %s | 实际 %s | %d 道 | Fold %d%s%s | 左拖缩放，右拖平移，双击复位，Ctrl+单击查看真实道", kindLabel, prestackSelectionKeyLabel(prestackState.selection), prestackSecondaryLabel(prestackState.selection), offsetLabel, len(prestackState.gather.TraceIndices), len(prestackState.gather.TraceIndices), keyPosition, gainHint))
	} else {
		setPrestackStatus(fmt.Sprintf("完成 | 一级 %s %s | 二级 %s | %d 道 | Fold %d | %s%s%s | 左拖缩放，右拖平移，双击复位，Ctrl+单击查看真实道", kindLabel, prestackSelectionKeyLabel(prestackState.selection), prestackSecondaryLabel(prestackState.selection), len(prestackState.gather.TraceIndices), len(prestackState.gather.TraceIndices), offsetLabel, keyPosition, gainHint))
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
	traces, positions := prestackRenderedColumns()
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
	if i < 0 {
		return -1
	}
	// The source list may be uniformly sampled or offset-resampled. Resolve
	// the selected physical trace back to the logical gather index instead of
	// treating the raster column as a contiguous gather position.
	trace := traces[clampInt(i, 0, len(traces)-1)]
	for j := prestackState.viewFirst; j <= prestackState.viewLast && j < len(prestackState.gather.TraceIndices); j++ {
		if prestackState.gather.TraceIndices[j] == trace {
			return j
		}
	}
	return -1
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
	context := fmt.Sprintf("叠前 %s | physical trace %d | Shot %d | Receiver %d | CDP %d | Header Offset %.5g | Computed Offset %.5g | Azimuth %.3f | Source (%.6g,%.6g) Receiver (%.6g,%.6g) Midpoint (%.6g,%.6g)", prestackSelectionKeyLabel(prestackState.selection), trace+1, r.SourceID, r.ReceiverID, r.CDP, r.HeaderOffset, r.ComputedOffset, r.Azimuth, r.SourceX, r.SourceY, r.ReceiverX, r.ReceiverY, r.MidpointX, r.MidpointY)
	showTraceAnalysisSelection(traceAnalysisSelection{Context: context, Targets: []traceAnalysisTarget{{Role: "A", Path: prestackState.dataset.Path, Trace: trace, SampleStart: prestackState.sampleFirst, SampleEnd: prestackState.sampleLast, MarkerSample: sample, Inline: r.Inline, Crossline: r.Crossline, HasGeometry: prestackState.index.UsesGrid}}})
}

func prestackInspectPhysicalTrace(trace int64, x, y int) {
	if prestackState.index == nil || trace < 0 || trace >= int64(len(prestackState.index.Records)) {
		return
	}
	r := prestackState.index.Records[trace]
	scene := prestackSceneRect()
	sample := prestackState.sampleFirst
	if y >= int(scene.Top) && y < int(scene.Bottom) {
		sample = prestackState.sampleFirst + int(math.Round(float64(y-int(scene.Top))/float64(maxInt(1, int(scene.Bottom-scene.Top)))*float64(prestackState.sampleLast-prestackState.sampleFirst)))
	}
	context := fmt.Sprintf("叠前 Source/Receiver | physical trace %d | Shot %d | Receiver %d | CDP %d | Header Offset %.5g | Computed Offset %.5g | Azimuth %.3f | Source (%.6g,%.6g) Receiver (%.6g,%.6g) Midpoint (%.6g,%.6g)", trace+1, r.SourceID, r.ReceiverID, r.CDP, r.HeaderOffset, r.ComputedOffset, r.Azimuth, r.SourceX, r.SourceY, r.ReceiverX, r.ReceiverY, r.MidpointX, r.MidpointY)
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
	keyText := prestackSelectionKeyLabel(prestackState.selection)
	if prestackState.selection.Key.All {
		// Keep the full-file context in the hover status.  The trace-specific
		// fields are useful, but displaying only them made an all-range view
		// look like a single-shot gather even though Gather returned every
		// physical trace.
		visibleCount := 0
		if len(prestackState.gather.TraceIndices) > 0 {
			a := clampInt(prestackState.viewFirst, 0, len(prestackState.gather.TraceIndices)-1)
			b := clampInt(prestackState.viewLast, a, len(prestackState.gather.TraceIndices)-1)
			visibleCount = b - a + 1
		}
		keyText = fmt.Sprintf("全部范围（全文件 %d 道，当前显示 %d 道）", len(prestackState.gather.TraceIndices), visibleCount)
	}
	setPrestackStatus(fmt.Sprintf("叠前 %s %s | physical trace %d | CDP %d | %s | Azimuth %.3f | Ctrl+单击查看道分析", prestackState.selection.Type, keyText, r.TraceNumber+1, r.CDP, offset, r.Azimuth))
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
	// A compatible DC starts with its own GDI text state. Set it explicitly so
	// QC labels do not inherit a light/transparent text color from a previous
	// page or theme while switching between restored and maximized sizes.
	pSetTextColor.Call(mem, rgbRef(0, 0, 0))
	if hFont != 0 {
		pSelectObject.Call(mem, hFont)
	}
	switch prestackState.page {
	case 0:
		paintPrestackGather(mem)
	case 1:
		paintPrestackGeometry(mem)
	case 3:
		paintPrestackCompare(mem)
	case 4:
		paintPrestackQC(mem)
	}
	// The destination DC retains WM_PAINT's update and child-window clipping;
	// drawing never repaints toolbar controls or uses their pixels as overlays.
	pBitBlt.Call(hdc, 0, 0, uintptr(w), uintptr(h), mem, 0, 0, SRCCOPY)
}

func paintPrestackCompare(hdc uintptr) {
	r := prestackLayoutRECT(prestackPanelLayoutForSize(int(clientWidth(prestackHwnd)), int(clientHeight(prestackHwnd)), 3).Content)
	if r.Right <= r.Left || r.Bottom <= r.Top {
		return
	}
	saved, _, _ := pSaveDC.Call(hdc)
	pIntersectClipRect.Call(hdc, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right), uintptr(r.Bottom))
	lines := []string{"叠前只读 A/B 对比基础", "A 保持当前道集顺序；B 仅通过元数据键匹配，不按物理道号直接配对。"}
	if prestackState.compareBLoading {
		lines = append(lines, "正在加载 B：仅扫描道头，不读取振幅…")
	} else if prestackState.compareBError != "" {
		lines = append(lines, prestackState.compareBError, "A 仍可继续浏览；请重新点击“加载 B”。")
	} else if prestackState.compareBDataset == nil {
		lines = append(lines, "请点击“加载 B”选择第二个 SEG-Y 文件。")
	} else {
		lines = append(lines, fmt.Sprintf("A：%s | 当前物理道 %d", filepath.Base(prestackState.dataset.Path), len(prestackState.gather.TraceIndices)))
		lines = append(lines, fmt.Sprintf("B：%s | 索引物理道 %d", filepath.Base(prestackState.compareBDataset.Path), len(prestackState.compareBIndex.Records)))
		m := prestackState.compareBMatch
		lines = append(lines, fmt.Sprintf("匹配策略：%s | 匹配 %d 对 | A 独有 %d | B 独有 %d | 歧义 A/B %d/%d | 无效 A/B %d/%d", m.Strategy, len(m.Pairs), len(m.AOnly), len(m.BOnly), len(m.AmbiguousA), len(m.AmbiguousB), len(m.InvalidA), len(m.InvalidB)))
		if !prestackState.compareBAxis.Compatible {
			lines = append(lines, "Δ 已禁用："+prestackState.compareBAxis.Reason)
		} else {
			lines = append(lines, "采样轴兼容：A/B 可进行浮点样点差值（当前页面仅显示匹配元数据）。")
		}
	}
	text := strings.Join(lines, "\r\n")
	drawPrestackQCText(hdc, text, r, DT_LEFT|prestackDTTop|DT_WORDBREAK)
	// Reserve three deterministic rectangles for the later A/B/Δ raster path;
	// drawing the labels now makes the page explicit without pretending that
	// metadata matching is already an amplitude result.
	top := r.Top + 170
	bottom := r.Bottom - 20
	if bottom > top {
		width := int(r.Right-r.Left-24) / 3
		for i, label := range []string{"A（当前道集）", "B（匹配道集）", "Δ（A−B）"} {
			left := int(r.Left) + i*(int(width)+12)
			rr := RECT{Left: int32(left), Top: int32(top), Right: int32(left + width), Bottom: int32(bottom)}
			border, _, _ := pCreateSolidBrush.Call(rgbRef(190, 190, 190))
			pFillRect.Call(hdc, uintptr(unsafe.Pointer(&rr)), border)
			inner := RECT{Left: rr.Left + 1, Top: rr.Top + 1, Right: rr.Right - 1, Bottom: rr.Bottom - 1}
			white, _, _ := pGetStockObject.Call(WHITE_BRUSH)
			pFillRect.Call(hdc, uintptr(unsafe.Pointer(&inner)), white)
			pDeleteObject.Call(border)
			var pixels []byte
			switch i {
			case 0:
				pixels = prestackState.compareA
			case 1:
				pixels = prestackState.compareB
			case 2:
				pixels = prestackState.compareDelta
			}
			drawAxisText(hdc, label, int(rr.Left), int(rr.Top)+8, int(rr.Right), int(rr.Top)+30, DT_CENTER|DT_SINGLELINE)
			imageRect := RECT{Left: rr.Left + 1, Top: rr.Top + 34, Right: rr.Right - 1, Bottom: rr.Bottom - 1}
			paintPrestackCompareRaster(hdc, imageRect, pixels, prestackState.compareWidth, prestackState.compareHeight)
		}
	}
	pRestoreDC.Call(hdc, saved)
}

func prestackComparePanelRects() [3]RECT {
	r := prestackLayoutRECT(prestackPanelLayoutForSize(clientWidth(prestackHwnd), clientHeight(prestackHwnd), 3).Content)
	top, bottom := r.Top+170, r.Bottom-20
	if bottom <= top {
		return [3]RECT{}
	}
	width := int(r.Right-r.Left-24) / 3
	var out [3]RECT
	for i := range out {
		left := int(r.Left) + i*(width+12)
		out[i] = RECT{Left: int32(left), Top: int32(top), Right: int32(left + width), Bottom: int32(bottom)}
	}
	return out
}

func clearPrestackCompareRender() {
	invalidatePrestackCompareRender()
	prestackState.compareA, prestackState.compareB, prestackState.compareDelta = nil, nil, nil
	prestackState.compareWidth, prestackState.compareHeight = 0, 0
}

// invalidatePrestackCompareRender cancels only the in-flight operation while
// retaining the last complete A/B/Δ frame. It is used during WM_SIZE so the
// user sees a clipped preview instead of a white/black placeholder.
func invalidatePrestackCompareRender() {
	if prestackState.compareRenderCancel != nil {
		prestackState.compareRenderCancel()
		prestackState.compareRenderCancel = nil
	}
	prestackState.compareRenderGeneration = atomic.AddInt64(&prestackGeneration, 1)
	prestackState.compareRendering = false
	prestackDeliveryMu.Lock()
	prestackPendingCompareRender = nil
	prestackDeliveryMu.Unlock()
}

func refreshPrestackCompareMatch() {
	if prestackState.compareBDataset == nil || prestackState.compareBIndex == nil || prestackState.index == nil {
		clearPrestackCompareRender()
		return
	}
	bGather, err := prestackState.compareBIndex.Gather(prestackState.selection)
	if err != nil {
		prestackState.compareBMatch = prestackcore.CompareMatchResult{}
		window := prestackcore.SampleWindow{Start: prestackState.sampleFirst, End: prestackState.sampleLast}
		prestackState.compareBAxis = prestackcore.SampleAxisCompatibility{AWindow: window, BWindow: window, Reason: "B 当前道集不可用"}
		startPrestackCompareRender()
		return
	}
	prestackState.compareBMatch, _ = prestackcore.MatchGatherResults(prestackState.index, prestackState.gather, prestackState.compareBIndex, bGather)
	prestackState.compareBAxis = comparePrestackGatherAxes(prestackState.index, prestackState.gather, prestackState.compareBIndex, bGather)
	startPrestackCompareRender()
}

func rGatherForSelection(index *prestackcore.PrestackIndex, selection prestackcore.GatherSelection) prestackcore.GatherResult {
	if index == nil {
		return prestackcore.GatherResult{}
	}
	g, err := index.Gather(selection)
	if err == nil {
		return g
	}
	return prestackcore.GatherResult{}
}

// comparePrestackGatherAxes checks the file-level axis and the trace-header
// delay/sample metadata used by the current selections.  A SEG-Y file may
// legally contain records with different delay or sample counts; in that
// case A/B remain viewable but Δ is disabled rather than silently shifting
// samples or truncating to a shorter record.
func comparePrestackGatherAxes(aIndex *prestackcore.PrestackIndex, aGather prestackcore.GatherResult, bIndex *prestackcore.PrestackIndex, bGather prestackcore.GatherResult) prestackcore.SampleAxisCompatibility {
	window := prestackcore.SampleWindow{Start: prestackState.sampleFirst, End: prestackState.sampleLast}
	axisFromIndex := func(index *prestackcore.PrestackIndex, gather prestackcore.GatherResult) prestackcore.SampleAxis {
		axis := prestackcore.SampleAxis{}
		if index != nil {
			axis.SampleCount = index.BinarySampleCount
			axis.SampleIntervalUS = index.BinarySampleIntervalUS
		}
		return axis
	}
	firstAxis := func(index *prestackcore.PrestackIndex, gather prestackcore.GatherResult) (prestackcore.SampleAxis, bool, string) {
		axis := axisFromIndex(index, gather)
		var have bool
		for _, trace := range gather.TraceIndices {
			if index == nil || trace < 0 || trace >= int64(len(index.Records)) {
				continue
			}
			r := index.Records[trace]
			candidate := prestackcore.SampleAxis{SampleCount: r.SampleCount, SampleIntervalUS: r.SampleIntervalUS, DelayMS: r.DelayMS, HasTimeOrigin: true, TimeOriginMS: float64(r.DelayMS)}
			if !have {
				axis = candidate
				have = true
				continue
			}
			if candidate.SampleCount != axis.SampleCount || candidate.SampleIntervalUS != axis.SampleIntervalUS || candidate.DelayMS != axis.DelayMS {
				return axis, false, "当前道集内部采样轴不一致"
			}
		}
		return axis, true, ""
	}
	a, aOK, aReason := firstAxis(aIndex, aGather)
	b, bOK, bReason := firstAxis(bIndex, bGather)
	if !aOK || !bOK {
		reason := aReason
		if reason == "" {
			reason = bReason
		}
		return prestackcore.SampleAxisCompatibility{A: a, B: b, AWindow: window, BWindow: window, Reason: reason}
	}
	return prestackcore.CompareSampleAxesWithWindows(a, b, window, window)
}

func startPrestackCompareRender() {
	if prestackState.dataset == nil || prestackState.index == nil || len(prestackState.gather.TraceIndices) == 0 {
		clearPrestackCompareRender()
		return
	}
	panels := prestackComparePanelRects()
	width, height := int(panels[0].Right-panels[0].Left), int(panels[0].Bottom-panels[0].Top)
	if width < 2 || height < 2 {
		clearPrestackCompareRender()
		return
	}
	clearPrestackCompareRender()
	ctx, cancel := context.WithCancel(context.Background())
	prestackState.compareRenderCancel = cancel
	gen := atomic.AddInt64(&prestackGeneration, 1)
	prestackState.compareRenderGeneration = gen
	prestackState.compareRendering = true
	owner, ownerToken := prestackHwnd, prestackState.ownerToken
	datasetGeneration, selectionGeneration, bGeneration, resizeGeneration := prestackState.datasetGeneration, prestackState.selectionGeneration, prestackState.compareBGeneration, prestackState.resizeGeneration
	aMappingGeneration, bMappingGeneration := prestackState.mappingGeneration, prestackState.compareBMappingGeneration
	compareModeGeneration, displayGeneration := prestackState.compareModeGeneration, prestackState.compareDisplayGeneration
	workspaceGeneration := prestackState.workspaceGeneration
	aData, bData := prestackState.dataset, prestackState.compareBDataset
	pairs := append([]prestackcore.TraceMatchPair(nil), prestackState.compareBMatch.Pairs...)
	if len(pairs) == 0 {
		// Keep A usable even before B is loaded or when B has no matching
		// current gather.  B and Δ remain empty with an explanatory status.
		pairs = nil
	}
	aGatherTraces := append([]int64(nil), prestackState.gather.TraceIndices...)
	s0, s1 := prestackState.sampleFirst, prestackState.sampleLast
	agc, gain, palette := prestackState.agc, prestackState.gain, prestackState.palette
	axisCompatible := prestackState.compareBAxis.Compatible
	go func() {
		result := &prestackCompareRenderResult{generation: gen, datasetGeneration: datasetGeneration, selectionGeneration: selectionGeneration, aMappingGeneration: aMappingGeneration, bGeneration: bGeneration, bMappingGeneration: bMappingGeneration, compareModeGeneration: compareModeGeneration, displayGeneration: displayGeneration, sampleFirst: int64(s0), sampleLast: int64(s1), resizeGeneration: resizeGeneration, workspaceGeneration: workspaceGeneration, owner: int64(owner), ownerToken: ownerToken, comparePage: 3, width: width, height: height, palette: palette}
		tracesA, tracesB := make([]int64, len(pairs)), make([]int64, len(pairs))
		if len(pairs) == 0 {
			tracesA = aGatherTraces
		}
		for i, p := range pairs {
			tracesA[i], tracesB[i] = p.ATrace, p.BTrace
		}
		ra, err := aData.OpenReader()
		if err != nil {
			result.err = err
		} else {
			defer ra.Close()
			var rb *segy.File
			if len(pairs) > 0 && bData != nil {
				var openErr error
				rb, openErr = bData.OpenReader()
				if openErr != nil {
					// A failed B reader must not hide the independent A panel.
					// Keep the error attached to B so the UI can explain why B/Δ
					// are empty while A remains usable.
					result.bErr = openErr
				}
			}
			if rb != nil {
				defer rb.Close()
			}
			opts := segy.RenderOptions{Width: width, Height: height, SampleStart: s0, SampleEnd: s1, AGC: agc, GainPercent: gain, ClipPercent: 99, DisplayMode: segy.DisplayAdaptive, ReadStrategy: segy.ReadStrategySparseMapped, Workers: 2}
			var av, bv []float64
			av, _, err = ra.RenderTraceIndicesValues(tracesA, opts)
			if err != nil {
				result.err = err
			} else {
				if rb != nil {
					bv, _, err = rb.RenderTraceIndicesValues(tracesB, opts)
					if err != nil {
						result.bErr = err
						bv = nil
					}
				}
				minV, maxV := math.Inf(1), math.Inf(-1)
				for _, v := range av {
					if math.IsNaN(v) || math.IsInf(v, 0) {
						continue
					}
					minV, maxV = math.Min(minV, v), math.Max(maxV, v)
				}
				for _, v := range bv {
					if math.IsNaN(v) || math.IsInf(v, 0) {
						continue
					}
					minV, maxV = math.Min(minV, v), math.Max(maxV, v)
				}
				if !finiteFloat(minV) || !finiteFloat(maxV) || minV >= maxV {
					minV, maxV = -1, 1
				}
				result.a = crookedPaletteBGRA(segy.MapAmplitudeValues(av, minV, maxV), palette)
				if len(bv) > 0 {
					result.b = crookedPaletteBGRA(segy.MapAmplitudeValues(bv, minV, maxV), palette)
				}
				if axisCompatible && len(bv) > 0 {
					diff, diffErr := prestackcore.DifferenceValues(av, bv)
					if diffErr != nil {
						result.bErr = diffErr
					} else {
						lo, hi := prestackcore.SymmetricValueRange(diff)
						result.delta = crookedPaletteBGRA(segy.MapAmplitudeValues(diff, lo, hi), palette)
					}
				}
			}
		}
		if ctx.Err() != nil {
			return
		}
		prestackDeliveryMu.Lock()
		defer prestackDeliveryMu.Unlock()
		if prestackDeliveryHwnd != owner || prestackDeliveryOwnerToken != ownerToken || prestackState.compareRenderGeneration != gen || ctx.Err() != nil {
			return
		}
		prestackPendingCompareRender = result
		if posted, _, _ := pPostMessageW.Call(owner, WM_PRESTACK_COMPARE_RENDER, uintptr(gen), 0); posted == 0 {
			prestackPendingCompareRender = nil
		}
	}()
}

func finiteFloat(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// prestackCompareRenderMatches is the pure delivery gate for a Compare frame.
// Keeping the complete predicate in one place makes it testable and prevents
// a future token field from being checked on one async path but omitted on
// another.
func prestackCompareRenderMatches(r *prestackCompareRenderResult, state prestackSession, owner uintptr, currentWidth, currentHeight int) bool {
	return r != nil && r.owner == int64(owner) && r.ownerToken == state.ownerToken &&
		r.workspaceGeneration == state.workspaceGeneration &&
		r.datasetGeneration == state.datasetGeneration &&
		r.selectionGeneration == state.selectionGeneration &&
		r.aMappingGeneration == state.mappingGeneration &&
		r.bGeneration == state.compareBGeneration &&
		r.bMappingGeneration == state.compareBMappingGeneration &&
		r.compareModeGeneration == state.compareModeGeneration &&
		r.displayGeneration == state.compareDisplayGeneration &&
		r.resizeGeneration == state.resizeGeneration &&
		r.sampleFirst == int64(state.sampleFirst) && r.sampleLast == int64(state.sampleLast) &&
		r.comparePage == state.page && r.width > 0 && r.height > 0 &&
		currentWidth == r.width && currentHeight == r.height
}

func receivePrestackCompareRender(gen int64) {
	prestackDeliveryMu.Lock()
	r := prestackPendingCompareRender
	if r != nil && r.generation == gen {
		prestackPendingCompareRender = nil
	} else {
		r = nil
	}
	prestackDeliveryMu.Unlock()
	panels := prestackComparePanelRects()
	currentWidth, currentHeight := 0, 0
	if panels[0].Right > panels[0].Left && panels[0].Bottom > panels[0].Top {
		currentWidth = int(panels[0].Right - panels[0].Left)
		currentHeight = int(panels[0].Bottom - panels[0].Top)
	}
	if gen != prestackState.compareRenderGeneration || !prestackCompareRenderMatches(r, prestackState, prestackHwnd, currentWidth, currentHeight) {
		return
	}
	prestackState.compareRendering = false
	if prestackState.compareRenderCancel != nil {
		prestackState.compareRenderCancel = nil
	}
	if r.err != nil {
		setPrestackStatus("A/B 图像渲染失败：" + r.err.Error())
		return
	}
	if r.bErr != nil {
		prestackState.compareBError = "B 图像读取失败：" + r.bErr.Error()
		setPrestackStatus(prestackState.compareBError + "；A 仍可继续浏览")
	}
	prestackState.compareA, prestackState.compareB, prestackState.compareDelta = r.a, r.b, r.delta
	prestackState.compareWidth, prestackState.compareHeight = r.width, r.height
	invalidatePrestackScene()
}

func paintPrestackCompareRaster(hdc uintptr, r RECT, pixels []byte, width, height int) {
	if width <= 0 || height <= 0 || r.Right <= r.Left || r.Bottom <= r.Top || len(pixels) != width*height*4 {
		return
	}
	saved, _, _ := pSaveDC.Call(hdc)
	pIntersectClipRect.Call(hdc, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right), uintptr(r.Bottom))
	bmi := BITMAPINFO{Header: BITMAPINFOHEADER{Size: uint32(unsafe.Sizeof(BITMAPINFOHEADER{})), Width: int32(width), Height: -int32(height), Planes: 1, BitCount: 32, Compression: BI_RGB}}
	pSetStretchBltMode.Call(hdc, HALFTONE)
	pStretchDIBits.Call(hdc, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), 0, 0, uintptr(width), uintptr(height), uintptr(unsafe.Pointer(&pixels[0])), uintptr(unsafe.Pointer(&bmi)), DIB_RGB_COLORS, SRCCOPY)
	pRestoreDC.Call(hdc, saved)
}

func clientWidth(hwnd uintptr) int  { r := clientRect(hwnd); return int(r.Right - r.Left) }
func clientHeight(hwnd uintptr) int { r := clientRect(hwnd); return int(r.Bottom - r.Top) }
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
		// Fill each lobe back to the zero-amplitude baseline.  RenderTraceIndices
		// maps the display maximum to 0 and the display minimum to 255, so the
		// palette midpoint (128) is the zero-amplitude separator after
		// gain/clip/AGC have been applied.  The fill is what distinguishes this
		// from a collection of unfilled trace lines when the gather is dense.
		black := rgbRef(0, 0, 0)
		red := rgbRef(220, 0, 0)
		blackPen, _, _ := pCreatePen.Call(PS_SOLID, 1, black)
		redPen, _, _ := pCreatePen.Call(PS_SOLID, 1, red)
		blackBrush, _, _ := pCreateSolidBrush.Call(black)
		redBrush, _, _ := pCreateSolidBrush.Call(red)
		old, _, _ := pSelectObject.Call(hdc, blackPen)
		oldBrush, _, _ := pSelectObject.Call(hdc, blackBrush)
		wavePoint := func(cx, scale float64, y int, index byte) POINT {
			return POINT{
				X: int32(cx + (127.5-float64(index))/127.5*scale),
				Y: int32(r.Top) + int32(y*int(r.Bottom-r.Top)/maxInt(1, h-1)),
			}
		}
		for x := 0; x < w; x++ {
			cx := float64(r.Left) + float64(x+1)*float64(r.Right-r.Left)/float64(w+1)
			scale := float64(r.Right-r.Left) / float64(w+1) * 0.85
			// First pass: build one positive and one negative polygon per trace.
			// Samples on the opposite side are clamped to the baseline. This keeps
			// the fill bounded at every zero crossing while avoiding one GDI
			// Polygon call for every noisy sign run.
			baseline := func(y int) POINT {
				return POINT{X: int32(cx), Y: int32(r.Top) + int32(y*int(r.Bottom-r.Top)/maxInt(1, h-1))}
			}
			positivePoints := make([]POINT, 0, h+2)
			negativePoints := make([]POINT, 0, h+2)
			positivePoints = append(positivePoints, baseline(0))
			negativePoints = append(negativePoints, baseline(0))
			for y := 0; y < h; y++ {
				point := wavePoint(cx, scale, y, prestackState.indices[y*w+x])
				if prestackState.indices[y*w+x] < 128 {
					positivePoints = append(positivePoints, point)
					negativePoints = append(negativePoints, baseline(y))
				} else {
					positivePoints = append(positivePoints, baseline(y))
					negativePoints = append(negativePoints, point)
				}
			}
			positivePoints = append(positivePoints, baseline(h-1))
			negativePoints = append(negativePoints, baseline(h-1))
			pSelectObject.Call(hdc, redPen)
			pSelectObject.Call(hdc, redBrush)
			pPolygon.Call(hdc, uintptr(unsafe.Pointer(&positivePoints[0])), uintptr(len(positivePoints)))
			pSelectObject.Call(hdc, blackPen)
			pSelectObject.Call(hdc, blackBrush)
			pPolygon.Call(hdc, uintptr(unsafe.Pointer(&negativePoints[0])), uintptr(len(negativePoints)))

			// Second pass: redraw the complete trace in black, then overdraw
			// positive portions in red so the lobe boundaries remain crisp.
			pSelectObject.Call(hdc, blackPen)
			for y := 0; y < h; y++ {
				point := wavePoint(cx, scale, y, prestackState.indices[y*w+x])
				if y == 0 {
					pMoveToEx.Call(hdc, uintptr(point.X), uintptr(point.Y), 0)
				} else {
					pLineTo.Call(hdc, uintptr(point.X), uintptr(point.Y))
				}
			}
			// Keeping the red segments separate avoids drawing a red bridge across
			// a negative sample when the waveform crosses zero.
			pSelectObject.Call(hdc, redPen)
			positive := false
			for y := 0; y < h; y++ {
				idx := prestackState.indices[y*w+x]
				point := wavePoint(cx, scale, y, idx)
				if idx < 128 {
					if !positive {
						pMoveToEx.Call(hdc, uintptr(point.X), uintptr(point.Y), 0)
						positive = true
					} else {
						pLineTo.Call(hdc, uintptr(point.X), uintptr(point.Y))
					}
				} else {
					positive = false
				}
			}
			pSelectObject.Call(hdc, blackPen)
		}
		pSelectObject.Call(hdc, oldBrush)
		pSelectObject.Call(hdc, old)
		pDeleteObject.Call(redPen)
		pDeleteObject.Call(blackPen)
		pDeleteObject.Call(redBrush)
		pDeleteObject.Call(blackBrush)
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
	_, positions := prestackRenderedColumns()
	if len(positions) > 0 {
		axisName := "Trace"
		if prestackState.selection.Axis == prestackcore.AxisOffset {
			axisName = "Offset"
		}
		firstLabel, middleLabel, lastLabel := "", "", ""
		if prestackState.selection.Axis == prestackcore.AxisOffset {
			labelAt := func(i int) string { return fmt.Sprintf("%.6g", positions[i]) }
			firstLabel = fmt.Sprintf("%s %s", axisName, labelAt(0))
			lastLabel = labelAt(len(positions) - 1)
			if len(positions) > 2 {
				middleLabel = labelAt(len(positions) / 2)
			}
		} else {
			// The raster may contain fewer columns than the current logical
			// view because it is sampled to the window width.  Axis labels must
			// describe the page's trace range, not those implementation columns.
			logicalCount := len(prestackState.gather.TraceIndices)
			if prestackState.viewLast >= prestackState.viewFirst && logicalCount > 0 {
				logicalCount = prestackState.viewLast - prestackState.viewFirst + 1
			}
			logicalCount = maxInt(1, logicalCount)
			firstLabel = fmt.Sprintf("显示道 1")
			lastLabel = strconv.Itoa(logicalCount)
			if logicalCount > 2 {
				middleLabel = strconv.Itoa(logicalCount/2 + 1)
			}
		}
		drawAxisText(hdc, firstLabel, int(r.Left), int(r.Bottom)+5, int(r.Left)+180, int(r.Bottom)+28, DT_LEFT|DT_SINGLELINE)
		if middleLabel != "" {
			drawAxisText(hdc, middleLabel, int(r.Left+r.Right)/2-70, int(r.Bottom)+5, int(r.Left+r.Right)/2+70, int(r.Bottom)+28, DT_CENTER|DT_SINGLELINE)
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
	titleKey := prestackState.selection.Key.String()
	physicalCount := len(prestackState.gather.TraceIndices)
	displayColumns := prestackState.imageWidth
	if displayColumns <= 0 {
		displayColumns = len(positions)
	}
	if prestackState.selection.Key.All {
		kindLabel := prestackGatherKindLabel(prestackState.selection.Type)
		if len(prestackState.selection.Keys) > 0 {
			titleKey = fmt.Sprintf("%s 多道集 %d 组（一级 %s 分组，二级 %s）", kindLabel, len(prestackState.selection.Keys), kindLabel, prestackSecondaryLabel(prestackState.selection))
		} else {
			titleKey = fmt.Sprintf("%s 全部范围（全文件聚合，一级 %s 分组，二级 %s）", kindLabel, kindLabel, prestackSecondaryLabel(prestackState.selection))
		}
	}
	title := fmt.Sprintf("%s — %s — 物理道 %d | 显示列 %d — %s", filepath.Base(prestackState.dataset.Path), titleKey, physicalCount, displayColumns, offsetText)
	if prestackState.selection.Type == prestackcore.GatherRaw {
		title = fmt.Sprintf("%s — 原始叠前道序 %d–%d — 物理道 %d | 显示列 %d — %s", filepath.Base(prestackState.dataset.Path), prestackState.gather.RawTraceStart+1, prestackState.gather.RawTraceEnd, physicalCount, displayColumns, offsetText)
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
		if (prestackState.selection.Type == prestackcore.GatherCMP && b.Key == prestackState.selection.Key) || prestackState.selectedBinIndex == i {
			point(b.X, b.Y, 0x000000FF, 5)
		}
	}
	// Source/Receiver relationship is a dynamic overlay. It is deliberately
	// drawn after the static points and clipped to the Geometry scene so it
	// never invalidates or paints over toolbar/status controls.
	if prestackState.linkGeneration == prestackState.workspaceGeneration && prestackState.linkIndex >= 0 &&
		(prestackState.linkKind == prestackcore.GatherShot || prestackState.linkKind == prestackcore.GatherReceiver) {
		points := idx.AcquisitionPoints(prestackState.linkKind)
		if prestackState.linkIndex < len(points) {
			if assoc, ok := idx.AcquisitionAssociation(prestackState.linkKind, points[prestackState.linkIndex].Key); ok {
				otherKind := prestackcore.GatherReceiver
				if prestackState.linkKind == prestackcore.GatherReceiver {
					otherKind = prestackcore.GatherShot
				}
				other := idx.AcquisitionPoints(otherKind)
				pen, _, _ := pCreatePen.Call(PS_SOLID, 1, 0x00F0A020)
				oldPen, _, _ := pSelectObject.Call(hdc, pen)
				if prestackState.linkIndex < len(points) {
					p := points[prestackState.linkIndex]
					px, py := prestackMapPixel(p.X, p.Y)
					for _, oi := range assoc.CounterpartIndices {
						if oi < 0 || oi >= len(other) {
							continue
						}
						if !p.HasCoordinates || !other[oi].HasCoordinates {
							continue
						}
						ox, oy := prestackMapPixel(other[oi].X, other[oi].Y)
						pMoveToEx.Call(hdc, uintptr(px), uintptr(py), 0)
						pLineTo.Call(hdc, uintptr(ox), uintptr(oy))
					}
				}
				pSelectObject.Call(hdc, oldPen)
				pDeleteObject.Call(pen)
				// Selected and counterpart points use distinct sizes/colors.
				if prestackState.linkIndex < len(points) && points[prestackState.linkIndex].HasCoordinates {
					point(points[prestackState.linkIndex].X, points[prestackState.linkIndex].Y, 0x000000FF, 5)
				}
				for _, oi := range assoc.CounterpartIndices {
					if oi >= 0 && oi < len(other) && other[oi].HasCoordinates {
						point(other[oi].X, other[oi].Y, 0x0000A5FF, 3)
					}
				}
			}
		}
	}
	if prestackState.dragging && !prestackState.panning {
		drawCrookedZoomRectangle(hdc, prestackState.dragX, prestackState.dragY, prestackState.dragCurrentX, prestackState.dragCurrentY)
	}
	pRestoreDC.Call(hdc, saved)
	drawAxisText(hdc, fmt.Sprintf("X %.6g..%.6g   Y %.6g..%.6g | %d traces / %d bins | 左拖放大，右拖平移，滚轮缩放，双击 Bin/Source/Receiver 打开道集", idx.Bounds.XMin, idx.Bounds.XMax, idx.Bounds.YMin, idx.Bounds.YMax, len(idx.Records), len(idx.Bins)), int(r.Left), int(r.Bottom)+8, int(r.Right), int(r.Bottom)+32, DT_LEFT|DT_SINGLELINE)
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

func prestackHitAcquisition(x, y int) (prestackcore.GatherType, prestackcore.AcquisitionPick, bool) {
	if prestackState.index == nil || !prestackPointInside(x, y) {
		return prestackcore.GatherCMP, prestackcore.AcquisitionPick{}, false
	}
	// Pick in screen space so zoom/pan and non-square client areas retain the
	// same hit tolerance as the drawn points.
	b := prestackState.mapBounds
	r := prestackSceneRect()
	dx, dy := b.XMax-b.XMin, b.YMax-b.YMin
	if dx <= 0 {
		dx = 1
	}
	if dy <= 0 {
		dy = 1
	}
	scale := math.Min(float64(r.Right-r.Left)/dx, float64(r.Bottom-r.Top)/dy) * .94
	trf := prestackcore.AcquisitionScreenTransform{OriginX: float64(r.Left+r.Right)/2 - (b.XMin+b.XMax)/2*scale, OriginY: float64(r.Top+r.Bottom)/2 + (b.YMin+b.YMax)/2*scale, ScaleX: scale, ScaleY: -scale}
	bestKind := prestackcore.GatherCMP
	var best prestackcore.AcquisitionPick
	found := false
	for _, kind := range []prestackcore.GatherType{prestackcore.GatherShot, prestackcore.GatherReceiver} {
		if kind == prestackcore.GatherShot && !prestackState.mapLayers[0] || kind == prestackcore.GatherReceiver && !prestackState.mapLayers[1] {
			continue
		}
		p, ok := prestackState.index.PickAcquisitionPoint(kind, float64(x), float64(y), trf, 10)
		if ok && (!found || p.DistancePx < best.DistancePx) {
			bestKind, best, found = kind, p, true
		}
	}
	return bestKind, best, found
}

func prestackSetLink(kind prestackcore.GatherType, point int) {
	if prestackState.linkKind == kind && prestackState.linkIndex == point {
		prestackClearLink()
		return
	}
	prestackState.linkKind, prestackState.linkIndex = kind, point
	prestackState.selectedBinIndex = -1
	prestackState.linkGeneration = prestackState.workspaceGeneration
	if prestackState.index == nil {
		return
	}
	points := prestackState.index.AcquisitionPoints(kind)
	if point < 0 || point >= len(points) {
		return
	}
	p := points[point]
	other := 0
	if assoc, ok := prestackState.index.AcquisitionAssociation(kind, p.Key); ok {
		other = len(assoc.CounterpartIndices)
	}
	label := "震源"
	if kind == prestackcore.GatherReceiver {
		label = "检波点"
	}
	setPrestackStatus(fmt.Sprintf("%s %s | 关联点 %d 个 | 物理道 %d 道 | Offset %s | Azimuth %s", label, p.Key.String(), other, len(p.TraceIndices), prestackRangeText(p.OffsetRange), prestackRangeText(p.AzimuthRange)))
	invalidatePrestackScene()
}

func prestackSetBinSelection(index int) {
	if prestackState.selectedBinIndex == index {
		prestackClearLink()
		return
	}
	prestackState.linkKind, prestackState.linkIndex = prestackcore.GatherCMP, -1
	prestackState.selectedBinIndex = index
	prestackState.linkGeneration = prestackState.workspaceGeneration
	if prestackState.index != nil && index >= 0 && index < len(prestackState.index.Bins) {
		b := prestackState.index.Bins[index]
		setPrestackStatus(fmt.Sprintf("Bin %s | Fold %d | 单击选中，再次单击取消；Ctrl+双击打开 CMP 道集", b.Key.String(), b.Fold))
	}
	invalidatePrestackScene()
}

func prestackClearLink() {
	clearPrestackPendingCtrlClick()
	prestackState.linkKind, prestackState.linkIndex = prestackcore.GatherCMP, -1
	prestackState.linkGeneration = 0
	prestackState.selectedBinIndex = -1
	invalidatePrestackScene()
}

func clearPrestackPendingCtrlClick() {
	if prestackHwnd != 0 {
		pKillTimer.Call(prestackHwnd, prestackCtrlClickTimer)
	}
	prestackState.pendingCtrlValid = false
	prestackState.pendingCtrlTrace = -1
	prestackState.pendingCtrlGeneration = 0
}

func queuePrestackCtrlTrace(trace int64, x, y int) {
	if prestackHwnd == 0 || prestackState.index == nil {
		return
	}
	clearPrestackPendingCtrlClick()
	prestackState.pendingCtrlTrace = trace
	prestackState.pendingCtrlX, prestackState.pendingCtrlY = x, y
	prestackState.pendingCtrlGeneration = prestackState.workspaceGeneration
	prestackState.pendingCtrlValid = true
	delay, _, _ := pGetDoubleClickTime.Call()
	if delay == 0 {
		delay = 500
	}
	pSetTimer.Call(prestackHwnd, prestackCtrlClickTimer, delay, 0)
}

func commitPrestackPendingCtrlClick() {
	if !prestackState.pendingCtrlValid {
		return
	}
	trace, x, y := prestackState.pendingCtrlTrace, prestackState.pendingCtrlX, prestackState.pendingCtrlY
	valid := prestackState.pendingCtrlGeneration == prestackState.workspaceGeneration
	clearPrestackPendingCtrlClick()
	if valid {
		prestackInspectPhysicalTrace(trace, x, y)
	}
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
		if prestackState.page == 3 {
			invalidatePrestackCompareRender()
		}
		prestackState.renderGeneration = atomic.AddInt64(&prestackGeneration, 1)
		prestackState.rendering = false
		invalidatePrestackScene()
		return 0
	case WM_GETMINMAXINFO:
		if lParam != 0 {
			mmi := (*MINMAXINFO)(unsafe.Pointer(lParam))
			// Keep both toolbar rows, the QC summary and a usable chart area
			// visible while the restored window is being resized.
			minW, minH := prestackFrameExtent(1120, 700)
			mmi.PtMinTrackSize.X = int32(minW)
			mmi.PtMinTrackSize.Y = int32(minH)
		}
		return 0
	case WM_SIZE:
		if prestackHwnd != 0 {
			layoutPrestackControls()
			// Restore/maximize can emit WM_SIZE without a surrounding
			// WM_ENTERSIZEMOVE/WM_EXITSIZEMOVE pair.  Treat that path as a
			// one-shot resize as well; minimized windows (wParam == 1) are
			// intentionally left without a render.
			if wParam != 1 && prestackState.page == 0 && prestackState.index != nil {
				prestackState.resizeGeneration++
				prestackState.resizePending = true
				if !prestackState.resizeActive {
					schedulePrestackResizeRender(h, prestackState.resizeGeneration)
				}
			} else if wParam != 1 && prestackState.page == 3 && prestackState.index != nil {
				prestackState.resizeGeneration++
				prestackState.resizePending = true
				invalidatePrestackCompareRender()
				if !prestackState.resizeActive {
					schedulePrestackResizeRender(h, prestackState.resizeGeneration)
				}
			}
			invalidatePrestackScene()
			// All prestack pages share the same parent/client and child toolbar.
			// Refresh the whole client so moving a tab, status label or progress
			// control cannot leave pixels from the previous page or size.
			redrawPrestackWholeClient()
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
		} else if prestackState.page == 3 && prestackState.index != nil {
			prestackState.resizeGeneration++
			prestackState.resizePending = true
			schedulePrestackResizeRender(h, prestackState.resizeGeneration)
		} else {
			prestackState.resizePending = false
		}
		invalidatePrestackScene()
		redrawPrestackWholeClient()
		return 0
	case WM_PRESTACK_RESIZE_FLUSH:
		// Ignore delayed messages from a previous drag or a destroyed/recreated
		// window.  Only the latest size is allowed to enter the renderer.
		if prestackState.page == 0 && !prestackState.resizeActive && prestackState.resizePending && int64(wParam) == prestackState.resizeGeneration {
			prestackState.resizePending = false
			startPrestackRender()
		} else if prestackState.page == 3 && !prestackState.resizeActive && prestackState.resizePending && int64(wParam) == prestackState.resizeGeneration {
			prestackState.resizePending = false
			startPrestackCompareRender()
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
	case WM_PRESTACK_COMPARE_B_READY:
		receivePrestackCompareB(int64(wParam))
		return 0
	case WM_PRESTACK_COMPARE_RENDER:
		receivePrestackCompareRender(int64(wParam))
		return 0
	case WM_PRESTACK_EXPORT_DONE:
		receivePrestackExport()
		return 0
	case WM_PRESTACK_EXPORT_PROGRESS:
		if int64(wParam) == prestackState.exportGeneration && prestackState.dataset != nil {
			pct := int(lParam)
			if pct < 0 {
				pct = 0
			}
			if pct > 100 {
				pct = 100
			}
			pSendMessageW.Call(prestackUI.progress, PBM_SETPOS, uintptr(pct), 0)
			setPrestackProgress(false, fmt.Sprintf("导出 %d%%", pct))
			setPrestackStatus(fmt.Sprintf("正在导出道集 %d%%…", pct))
		}
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
		case IDPRESTACK_QC_EXPORT:
			exportPrestackQC()
		case IDPRESTACK_COMPARE_OPEN_B:
			if code == 0 || code == 1 {
				if path := openDialog(h); path != "" {
					startPrestackCompareB(path)
				}
			}
		case IDPRESTACK_COMPARE_CLOSE_B:
			if code == 0 || code == 1 {
				cancelPrestackCompareB()
				setPrestackStatus("已关闭 B；A 仍可继续浏览")
			}
		case IDPRESTACK_COMPARE_EXPORT:
			if code == 0 || code == 1 {
				exportPrestackCompareReport()
			}
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
		case IDPRESTACK_CMP_BIN_APPLY:
			if code == 0 || code == 1 {
				applyPrestackCMPBinSize()
			}
		case IDPRESTACK_MULTI_APPLY:
			if code == 0 || code == 1 {
				applyPrestackMultiKeys()
			}
		case IDPRESTACK_EXPORT_GATHER:
			if code == 0 || code == 1 {
				exportPrestackGatherSEGY()
			}
		case IDPRESTACK_EXPORT_CSV:
			if code == 0 || code == 1 {
				exportPrestackGatherCSV()
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
		case IDPRESTACK_WIGGLE_DECIM:
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
			prestackState.compareDisplayGeneration = atomic.AddInt64(&prestackGeneration, 1)
			clearPrestackCompareRender()
			startPrestackRender()
			if prestackState.page == 3 && prestackState.compareBDataset != nil {
				startPrestackCompareRender()
			}
		case IDPRESTACK_AGC:
			v, _, _ := pSendMessageW.Call(prestackUI.agc, BM_GETCHECK, 0, 0)
			prestackState.agc = v == BST_CHECKED
			prestackState.compareDisplayGeneration = atomic.AddInt64(&prestackGeneration, 1)
			clearPrestackCompareRender()
			startPrestackRender()
			if prestackState.page == 3 && prestackState.compareBDataset != nil {
				startPrestackCompareRender()
			}
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
			prestackClearLink()
			invalidatePrestackScene()
		}
		return 0
	case WM_KEYDOWN:
		if wParam == VK_ESCAPE {
			cancelPrestackPointerDrag(true)
			prestackClearLink()
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
				if kind, pick, ok := prestackHitAcquisition(x, y); ok {
					label := "Source"
					if kind == prestackcore.GatherReceiver {
						label = "Receiver"
					}
					setPrestackStatus(fmt.Sprintf("%s %s | %d physical traces | Offset %s | Azimuth %s | 单击联动，双击打开道集，Ctrl+单击查看道分析", label, pick.Point.Key.String(), len(pick.Point.TraceIndices), prestackRangeText(pick.Point.OffsetRange), prestackRangeText(pick.Point.AzimuthRange)))
				} else if i := prestackHitBin(x, y); i >= 0 {
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
			if !prestackState.panning && dx < 5 && dy < 5 {
				if kind, pick, ok := prestackHitAcquisition(x, y); ok {
					if prestackState.ctrlClick && len(pick.Point.TraceIndices) > 0 {
						// Delay Ctrl+single until the double-click window expires;
						// Windows delivers the first button-up before WM_LBUTTONDBLCLK.
						queuePrestackCtrlTrace(pick.Point.TraceIndices[0], x, y)
					} else if !prestackState.ctrlClick {
						prestackSetLink(kind, pick.PointIndex)
					}
				} else if i := prestackHitBin(x, y); i >= 0 {
					if prestackState.ctrlClick && i < len(prestackState.index.Bins) {
						g, err := prestackState.index.Gather(prestackcore.GatherSelection{Type: prestackcore.GatherCMP, Key: prestackState.index.Bins[i].Key})
						if err == nil && len(g.TraceIndices) > 0 {
							queuePrestackCtrlTrace(g.TraceIndices[0], x, y)
						}
					} else if !prestackState.ctrlClick {
						prestackSetBinSelection(i)
					}
				}
			}
			invalidatePrestackScene()
			prestackState.ctrlClick = false
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
		if prestackState.page == 1 && (wParam&prestackMKControl) != 0 {
			clearPrestackPendingCtrlClick()
			if kind, pick, ok := prestackHitAcquisition(x, y); ok {
				if kind == prestackcore.GatherShot || kind == prestackcore.GatherReceiver {
					openPrestackGather(kind, pick.Point.Key)
				}
			} else if i := prestackHitBin(x, y); i >= 0 {
				key := prestackState.index.Bins[i].Key
				// Geometry bins are the historical exact CMP keys. If the Gather
				// page is currently using midpoint XY bins, resolve the clicked
				// bin's real midpoint to the configured XY key before switching;
				// otherwise the exact key is not present in the configured list.
				if prestackState.cmpBinSize > 0 && i < len(prestackState.index.Bins) && prestackState.index.Bins[i].HasCoordinates {
					cfg := currentPrestackCMPBinConfig()
					candidates := prestackState.index.AvailableCMPGathersConfigured(cfg)
					best, bestDistance := prestackcore.GatherKey{}, math.Inf(1)
					for _, candidate := range candidates {
						dx := candidate.CMPBinCenterX - prestackState.index.Bins[i].X
						dy := candidate.CMPBinCenterY - prestackState.index.Bins[i].Y
						d := dx*dx + dy*dy
						if d < bestDistance {
							best, bestDistance = candidate, d
						}
					}
					if best.CMPBin {
						key = best
					}
				}
				openPrestackGather(prestackcore.GatherCMP, key)
			}
		} else if prestackState.page == 0 {
			resetPrestackSection()
		}
		return 0
	case WM_TIMER:
		if wParam == prestackCtrlClickTimer {
			commitPrestackPendingCtrlClick()
			return 0
		}
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
		prestackDeliveryOwnerToken = 0
		prestackDeliveryIndexGeneration = 0
		prestackDeliveryRenderGeneration = 0
		prestackPendingIndex = nil
		prestackPendingRender = nil
		prestackDeliveryMu.Unlock()
		revokeOleSegyDropTarget(h)
		prestackHwnd = 0
		prestackOwnerToken = 0
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
