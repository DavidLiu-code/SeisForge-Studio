//go:build windows

package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

const (
	IDCMP_A             = 5001
	IDCMP_B             = 5002
	IDCMP_MODE          = 5003
	IDCMP_SLIDER        = 5004
	IDCMP_TIME          = 5005
	IDCMP_PREV          = 5006
	IDCMP_NEXT          = 5007
	IDCMP_REFRESH       = 5008
	IDCMP_CLOSE         = 5009
	IDCMP_STATUS        = 5010
	IDCMP_ALABEL        = 5011
	IDCMP_BLABEL        = 5012
	IDCMP_ILBYTE        = 5013
	IDCMP_XLBYTE        = 5014
	IDCMP_AUTOGEOM      = 5015
	IDCMP_SWAPGEOM      = 5016
	IDCMP_PALETTE       = 5017
	IDCMP_CROSSHAIR     = 5018
	IDCMP_RECTTOOL      = 5019
	IDCMP_ELLIPSETOOL   = 5020
	IDCMP_LINETOOL      = 5021
	IDCMP_CLEARANN      = 5022
	IDCMP_DIFF          = 5023
	IDCMP_SPECTRUM      = 5024
	IDCMP_EXPORT        = 5025
	IDCMP_WELCOME_2D    = 5026
	IDCMP_WELCOME_3D    = 5027
	IDCMP_WELCOME_CURVE = 5028
	IDCMP_TRACE_INSPECT = 5029

	TBM_GETPOS      = WM_USER
	TBM_SETPOS      = WM_USER + 5
	TBM_SETRANGE    = WM_USER + 6
	TBM_SETRANGEMIN = WM_USER + 7
	TBM_SETRANGEMAX = WM_USER + 8
	TBM_SETTICFREQ  = WM_USER + 20
	TBM_SETPAGESIZE = WM_USER + 21
	TBM_SETLINESIZE = WM_USER + 23

	WM_COMPARE_READY       = WM_USER + 201
	WM_COMPARE_SLICE_READY = WM_USER + 202
	WM_COMPARE_GEOM_READY  = WM_USER + 203
	WM_COMPARE_PROGRESS    = WM_USER + 204
	WM_COMPARE_EXPORT_DONE = WM_USER + 205

	PBM_SETRANGE = WM_USER + 1
	PBM_SETPOS   = WM_USER + 2
)

type compareControls struct {
	// Main workspace toolbar migrated from the legacy entry window.
	open, save, fixed, mark, data, params, zoom, origin    uintptr
	mail, update, about, compare, volume, spectrum, export uintptr
	traceInspect                                           uintptr
	aLabel, bLabel                                         uintptr
	modeLabel, mode                                        uintptr
	slider, timeEdit                                       uintptr
	prev, next                                             uintptr
	timeUnit                                               uintptr
	refresh, close                                         uintptr
	status, progress                                       uintptr
	ilLabel, xlLabel                                       uintptr
	ilByte, xlByte                                         uintptr
	autoGeom, swapGeom                                     uintptr
	palette, crosshair                                     uintptr
	rectTool, ellipseTool, lineTool, clearAnn              uintptr
	diff                                                   uintptr
}

type comparePanel struct {
	indices []byte
	bgra    []byte
	w, h    int
	stats   segy.RenderStats
	title   string
}

type compareTimeData struct {
	fa, fb      *segy.File
	ga, gb      *segy.GeometryIndex
	ca, cb      *segy.TimeSliceCache
	bounds      segy.SliceBounds
	fullBounds  segy.SliceBounds
	maxSampleA  int
	sampleA     int
	sampleB     int
	indexStatus string
	// Keep a compact copy of the currently displayed slices. Palette/gain
	// changes can then remap A/B immediately even if background prefetch has
	// evicted the source slab from the LRU cache.
	currentValsA []float32
	currentValsB []float32
}

func (d *compareTimeData) close() {
	if d == nil {
		return
	}
	if d.fa != nil {
		_ = d.fa.Close()
	}
	if d.fb != nil {
		_ = d.fb.Close()
	}
}

type comparePrepareResult struct {
	gen          int64
	data         *compareTimeData
	valsA, valsB []float32
	err          error
}

type compareSliceResult struct {
	gen              int64
	sampleA, sampleB int
	valsA, valsB     []float32
	statA, statB     segy.TimeSliceBlockStats
	err              error
}

type compareAnnotation struct {
	Mode           int     // 0 section, 1 time slice
	Kind           int     // 0 rectangle, 1 ellipse, 2 line
	X0, Y0, X1, Y1 float64 // world coordinates: trace/sample or crossline/inline
}

type compareGeomDetectResult struct {
	gen        int64
	a, b       segy.GeometryDetectResult
	errA, errB error
	hasB       bool
}

type compareExportResult struct {
	path    string
	kind    string
	traces  int
	samples int
	err     error
}

type compareViewSnapshot struct {
	mode                   int
	lineXMin, lineXMax     float64
	sampleStart, sampleEnd int
	bounds                 segy.SliceBounds
}

var (
	compareHwnd                                                            uintptr
	cc                                                                     compareControls
	compareAPath, compareBPath                                             string
	compareMode                                                            int // 0=Inline, 1=Crossline, 2=Time Slice
	compareA, compareB, compareD                                           comparePanel
	compareShowDiff                                                        bool
	compareBVisible                                                        bool
	compareMapMin, compareMapMax                                           float64
	compareTD                                                              *compareTimeData
	compareAsyncMu                                                         sync.Mutex
	comparePendingPrepare                                                  *comparePrepareResult
	comparePendingSlice                                                    *compareSliceResult
	comparePendingGeom                                                     *compareGeomDetectResult
	compareGen                                                             int64
	compareSliceGen                                                        int64
	compareGeomGen                                                         int64
	compareAutoGeom                                                        bool
	compareAutoILA, compareAutoXLA, compareAutoILB, compareAutoXLB         int
	compareTraceStart, compareTraceEnd, compareTraceStep                   int64
	compareSampleStart, compareSampleEnd                                   int
	compareOriginTraceStart, compareOriginTraceEnd, compareOriginTraceStep int64
	compareOriginSampleStart, compareOriginSampleEnd                       int
	compareZoomDragging                                                    bool
	compareZoomPanel                                                       int
	compareZoomX0, compareZoomY0, compareZoomX1, compareZoomY1             int
	comparePaletteIndex                                                    int
	compareCrosshairOn                                                     bool
	compareCrosshairValid                                                  bool
	compareCrosshairWorldX, compareCrosshairWorldY                         float64
	compareTool                                                            int // 0 zoom, 1 rect, 2 ellipse, 3 line
	compareAnnotDragging                                                   bool
	compareAnnotPanel                                                      int
	compareAnnotX0, compareAnnotY0, compareAnnotX1, compareAnnotY1         int
	compareAnnotations                                                     []compareAnnotation
	compareDtAUS                                                           int
	compareLineCoord                                                       int32
	compareLineXMin, compareLineXMax                                       float64
	compareLineOriginXMin, compareLineOriginXMax                           float64
	compareZoomToggleValid, compareZoomAtAfter                             bool
	compareZoomBefore, compareZoomAfter                                    compareViewSnapshot
	compareProgressVisible                                                 bool
	compareTraceInspectMode                                                bool

	// Persistent compare-scene cache. The expensive seismic raster, axes and
	// committed annotations are rendered only when content/layout changes.
	// Mouse hover restores only the invalid crosshair strips from this bitmap.
	compareBaseDC, compareBaseBmp, compareBaseOldBmp uintptr
	compareBaseW, compareBaseH                       int
	compareBaseDirty                                 bool = true
	compareLastHoverStatus                           time.Time
	compareBusyCount                                 int32
	compareExportResultCh                            = make(chan compareExportResult, 1)
)

func showCompareWindow() {
	if compareHwnd != 0 {
		pSetForeground.Call(compareHwnd)
		return
	}
	compareAPath = ""
	compareBPath = ""
	if sf != nil {
		compareAPath = sf.Info.Path
	}
	compareMode = 0
	compareShowDiff = false
	compareBVisible = false
	compareZoomToggleValid = false
	comparePaletteIndex = paletteIndex
	compareCrosshairOn = true
	compareCrosshairValid = false
	compareTool = 0
	compareAnnotations = nil
	if sf != nil {
		compareTraceStart, compareTraceEnd, compareTraceStep = traceStart, traceEnd, traceStep
		compareSampleStart, compareSampleEnd = sampleStart, sampleEnd
	} else {
		compareTraceStart, compareTraceEnd, compareTraceStep = 0, -1, 1
		compareSampleStart, compareSampleEnd = 0, -1
	}
	compareOriginTraceStart, compareOriginTraceEnd, compareOriginTraceStep = compareTraceStart, compareTraceEnd, compareTraceStep
	compareOriginSampleStart, compareOriginSampleEnd = compareSampleStart, compareSampleEnd
	h, _, _ := pCreateWindowExW.Call(WS_EX_APPWINDOW, uintptr(unsafe.Pointer(u16("Limage64Compare"))), uintptr(unsafe.Pointer(u16(APP_NAME+" v"+APP_VERSION+" [x64] - 地震浏览与对比工作台"))),
		WS_OVERLAPPEDWINDOW, uintptr(CW_USEDEFAULT), uintptr(CW_USEDEFAULT), 1560, 820, 0, 0, 0, 0)
	if h == 0 {
		return
	}
	compareHwnd = h
	createCompareUI()
	acceptSegyDrops(h)
	registerOleSegyDropTarget(h, 0)
	pShowWindow.Call(h, SW_SHOW)
	pUpdateWindow.Call(h)
	if compareAPath != "" {
		startAutoDetectGeometry()
	}
}

func compareHasB() bool    { return compareBPath != "" }
func compareActiveB() bool { return compareBVisible && compareBPath != "" }

// workspaceSyncAFromMain is called after the legacy range-first load dialog
// commits A. The hidden legacy main window remains only as a compatibility
// controller for the reconstructed dialogs; this workspace is the visible UI.
func workspaceSyncAFromMain() {
	if compareHwnd == 0 || sf == nil {
		return
	}
	setCompareTraceInspectMode(false)
	compareAPath = sf.Info.Path
	compareBPath = ""
	compareShowDiff = false
	compareBVisible = false
	compareZoomToggleValid = false
	compareTraceStart, compareTraceEnd, compareTraceStep = traceStart, traceEnd, traceStep
	compareSampleStart, compareSampleEnd = sampleStart, sampleEnd
	compareOriginTraceStart, compareOriginTraceEnd, compareOriginTraceStep = compareTraceStart, compareTraceEnd, compareTraceStep
	compareOriginSampleStart, compareOriginSampleEnd = compareSampleStart, compareSampleEnd
	comparePaletteIndex = paletteIndex
	if cc.palette != 0 {
		pSendMessageW.Call(cc.palette, CB_SETCURSEL, uintptr(comparePaletteIndex), 0)
	}
	layoutComparePathLabels()
	if cc.diff != 0 {
		pSendMessageW.Call(cc.diff, BM_SETCHECK, 0, 0)
		pEnableWindow.Call(cc.diff, 0)
	}
	invalidateCompareTimeData()
	compareAutoGeom = false
	compareCrosshairValid = false
	setText(cc.status, "A 已载入，正在自动识别 Inline/Crossline...")
	startAutoDetectGeometry()
}

func shortPath(p string, maxLen int) string {
	if len([]rune(p)) <= maxLen {
		return p
	}
	r := []rune(p)
	if maxLen < 8 {
		return string(r[:maxLen])
	}
	return string(r[:maxLen/2-2]) + "..." + string(r[len(r)-(maxLen/2):])
}

func layoutComparePathLabels() {
	if compareHwnd == 0 || cc.aLabel == 0 || cc.bLabel == 0 {
		return
	}
	if cc.traceInspect != 0 {
		show := uintptr(SW_HIDE)
		if compareAPath != "" {
			show = SW_SHOW
		}
		pShowWindow.Call(cc.traceInspect, show)
	}
	cw, _ := clientSize(compareHwnd)
	start := 670
	avail := cw - start - 12
	if avail < 360 {
		avail = 360
	}
	if compareActiveB() {
		gap := 8
		each := (avail - gap) / 2
		pMoveWindow.Call(cc.aLabel, uintptr(start), 5, uintptr(each), 18, 1)
		pMoveWindow.Call(cc.bLabel, uintptr(start+each+gap), 5, uintptr(each), 18, 1)
		maxChars := maxInt(28, each/7)
		setText(cc.aLabel, "A: "+shortPath(compareAPath, maxChars))
		setText(cc.bLabel, "B: "+shortPath(compareBPath, maxChars))
		return
	}
	// A-only browsing is the default, so give the path most of the row.
	bW := 190
	if avail < 520 {
		bW = 150
	}
	aW := avail - bW - 8
	pMoveWindow.Call(cc.aLabel, uintptr(start), 5, uintptr(aW), 18, 1)
	pMoveWindow.Call(cc.bLabel, uintptr(start+aW+8), 5, uintptr(bW), 18, 1)
	maxChars := maxInt(36, aW/7)
	if compareAPath != "" {
		setText(cc.aLabel, "A: "+shortPath(compareAPath, maxChars))
	} else {
		setText(cc.aLabel, "A: 未加载（点击打开按钮）")
	}
	if compareHasB() {
		setText(cc.bLabel, "B: 已缓存（点击 比 显示）")
	} else {
		setText(cc.bLabel, "B: 未加载（点击 比）")
	}
	layoutCompareStatusProgress()
}

func layoutCompareStatusProgress() {
	if compareHwnd == 0 || cc.status == 0 {
		return
	}
	cw, _ := clientSize(compareHwnd)
	x := 416
	right := cw - 12
	if right < x+200 {
		right = x + 200
	}
	if compareProgressVisible && cc.progress != 0 {
		pw := 210
		if cw < 1280 {
			pw = 150
		}
		px := right - pw
		sw := px - x - 8
		if sw < 180 {
			sw = 180
		}
		pMoveWindow.Call(cc.status, uintptr(x), 70, uintptr(sw), 20, 1)
		pMoveWindow.Call(cc.progress, uintptr(px), 69, uintptr(pw), 17, 1)
		pShowWindow.Call(cc.progress, SW_SHOW)
	} else {
		pMoveWindow.Call(cc.status, uintptr(x), 70, uintptr(right-x), 20, 1)
		if cc.progress != 0 {
			pShowWindow.Call(cc.progress, SW_HIDE)
		}
	}
}

func setCompareProgress(pct int, visible bool) {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	compareProgressVisible = visible
	if cc.progress != 0 {
		pSendMessageW.Call(cc.progress, PBM_SETPOS, uintptr(pct), 0)
	}
	layoutCompareStatusProgress()
}

func postCompareProgress(pct int) {
	if compareHwnd != 0 {
		pPostMessageW.Call(compareHwnd, WM_COMPARE_PROGRESS, uintptr(pct), 0)
	}
}

func createCompareUI() {
	cc = compareControls{}
	// Row 1: migrate the original Limage/Fimage entry toolbar into the workspace.
	cc.open = createIconButton(compareHwnd, 10, ID_OPEN, 0)
	cc.save = createIconButton(compareHwnd, 37, ID_SAVE, 1)
	cc.palette = createCtrl(compareHwnd, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 72, 2, 92, 260, IDCMP_PALETTE)
	for _, name := range paletteNames {
		pSendMessageW.Call(cc.palette, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(name))))
	}
	pSendMessageW.Call(cc.palette, CB_SETCURSEL, uintptr(comparePaletteIndex), 0)
	cc.fixed = createIconButtonStyled(compareHwnd, 171, ID_FIXED, 20, BS_AUTOCHECKBOX|BS_PUSHLIKE)
	pSendMessageW.Call(cc.fixed, BM_SETCHECK, BST_CHECKED, 0)
	cc.mark = createIconButton(compareHwnd, 198, ID_MARK, 5)
	cc.data = createIconButton(compareHwnd, 225, ID_DATA, 21)
	cc.params = createIconButton(compareHwnd, 252, ID_PARAMS, 23)
	cc.zoom = createIconButtonStyled(compareHwnd, 288, ID_ZOOM, 19, BS_AUTOCHECKBOX|BS_PUSHLIKE)
	cc.origin = createIconButton(compareHwnd, 315, ID_ORIGIN, 18)
	cc.mail = createIconButton(compareHwnd, 351, ID_MAIL, 13)
	cc.update = createIconButton(compareHwnd, 378, ID_UPDATE, 10)
	cc.about = createIconButton(compareHwnd, 405, ID_ABOUT, 17)
	cc.compare = createCtrl(compareHwnd, "BUTTON", "比", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_AUTOCHECKBOX|BS_PUSHLIKE, 438, 2, 34, 23, ID_COMPARE)
	cc.diff = createCtrl(compareHwnd, "BUTTON", "差", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_AUTOCHECKBOX|BS_PUSHLIKE, 476, 2, 34, 23, IDCMP_DIFF)
	cc.volume = createCtrl(compareHwnd, "BUTTON", "体", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 514, 2, 34, 23, ID_VOLUME)
	cc.spectrum = createCtrl(compareHwnd, "BUTTON", "谱", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 552, 2, 34, 23, IDCMP_SPECTRUM)
	cc.export = createCtrl(compareHwnd, "BUTTON", "出", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 590, 2, 34, 23, IDCMP_EXPORT)
	cc.traceInspect = createCtrl(compareHwnd, "BUTTON", "道", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_AUTOCHECKBOX|BS_PUSHLIKE, 628, 2, 34, 23, IDCMP_TRACE_INSPECT)
	pEnableWindow.Call(cc.diff, 0)

	aTxt := "A: 未加载（点击打开按钮）"
	if compareAPath != "" {
		aTxt = "A: " + shortPath(compareAPath, 100)
	}
	cc.aLabel = createCtrl(compareHwnd, "STATIC", aTxt, WS_CHILD|WS_VISIBLE, 670, 5, 410, 18, IDCMP_ALABEL)
	cc.bLabel = createCtrl(compareHwnd, "STATIC", "B: 未加载（点击 比）", WS_CHILD|WS_VISIBLE, 1088, 5, 452, 18, IDCMP_BLABEL)
	layoutComparePathLabels()

	// Row 2: view direction and navigation. A alone already supports all modes.
	cc.modeLabel = createCtrl(compareHwnd, "STATIC", "方向：", WS_CHILD|WS_VISIBLE, 10, 39, 42, 20, 0)
	cc.mode = createCtrl(compareHwnd, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 54, 35, 105, 120, IDCMP_MODE)
	for _, s := range []string{"Inline", "Crossline", "Time Slice"} {
		pSendMessageW.Call(cc.mode, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(s))))
	}
	pSendMessageW.Call(cc.mode, CB_SETCURSEL, uintptr(compareMode), 0)
	cc.prev = createCtrl(compareHwnd, "BUTTON", "◀", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 174, 35, 31, 23, IDCMP_PREV)
	cc.next = createCtrl(compareHwnd, "BUTTON", "▶", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 209, 35, 31, 23, IDCMP_NEXT)
	cc.slider = createCtrl(compareHwnd, "msctls_trackbar32", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP, 248, 34, 360, 25, IDCMP_SLIDER)
	cc.timeEdit = createCtrl(compareHwnd, "EDIT", "", WS_CHILD|WS_VISIBLE|WS_BORDER|ES_READONLY, 615, 35, 90, 22, IDCMP_TIME)
	cc.timeUnit = createCtrl(compareHwnd, "STATIC", "IL", WS_CHILD|WS_VISIBLE, 709, 39, 28, 18, 0)
	cc.refresh = createCtrl(compareHwnd, "BUTTON", "还原", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 744, 35, 52, 23, IDCMP_REFRESH)
	cc.autoGeom = createCtrl(compareHwnd, "BUTTON", "自动识别", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 802, 35, 70, 23, IDCMP_AUTOGEOM)
	cc.swapGeom = createCtrl(compareHwnd, "BUTTON", "IL/XL交换", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 878, 35, 72, 23, IDCMP_SWAPGEOM)
	cc.ilLabel = createCtrl(compareHwnd, "STATIC", "IL byte", WS_CHILD|WS_VISIBLE, 958, 39, 45, 18, 0)
	cc.ilByte = createCtrl(compareHwnd, "EDIT", "189", WS_CHILD|WS_VISIBLE|WS_BORDER|ES_AUTOHSCROLL, 1004, 35, 42, 22, IDCMP_ILBYTE)
	cc.xlLabel = createCtrl(compareHwnd, "STATIC", "XL", WS_CHILD|WS_VISIBLE, 1052, 39, 20, 18, 0)
	cc.xlByte = createCtrl(compareHwnd, "EDIT", "193", WS_CHILD|WS_VISIBLE|WS_BORDER|ES_AUTOHSCROLL, 1074, 35, 42, 22, IDCMP_XLBYTE)

	// Row 3: linked QC and annotations.
	cc.crosshair = createCtrl(compareHwnd, "BUTTON", "十字联动", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_AUTOCHECKBOX|BS_PUSHLIKE, 10, 67, 76, 23, IDCMP_CROSSHAIR)
	pSendMessageW.Call(cc.crosshair, BM_SETCHECK, BST_CHECKED, 0)
	cc.rectTool = createCtrl(compareHwnd, "BUTTON", "矩形", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_AUTOCHECKBOX|BS_PUSHLIKE, 94, 67, 54, 23, IDCMP_RECTTOOL)
	cc.ellipseTool = createCtrl(compareHwnd, "BUTTON", "椭圆", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_AUTOCHECKBOX|BS_PUSHLIKE, 152, 67, 54, 23, IDCMP_ELLIPSETOOL)
	cc.lineTool = createCtrl(compareHwnd, "BUTTON", "线段", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_AUTOCHECKBOX|BS_PUSHLIKE, 210, 67, 54, 23, IDCMP_LINETOOL)
	cc.clearAnn = createCtrl(compareHwnd, "BUTTON", "清除标注", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 272, 67, 72, 23, IDCMP_CLEARANN)
	cc.close = createCtrl(compareHwnd, "BUTTON", "关闭", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 350, 67, 52, 23, IDCMP_CLOSE)
	cc.status = createCtrl(compareHwnd, "STATIC", "打开 A 后即可浏览 Inline / Crossline / Time Slice；点击 比 加载 B。", WS_CHILD|WS_VISIBLE, 416, 70, 890, 20, IDCMP_STATUS)
	cc.progress = createCtrl(compareHwnd, "msctls_progress32", "", WS_CHILD, 1320, 69, 210, 17, 0)
	pSendMessageW.Call(cc.progress, PBM_SETRANGE, 0, uintptr(uint32(100)<<16))

	// Start Center is painted as one clean parent scene; no legacy child buttons.

	showTimeControls(true)
	layoutCompareStatusProgress()
	setText(cc.timeUnit, "IL")
	updateCompareWelcomeState()
}

func showTimeControls(show bool) {
	cmd := uintptr(SW_HIDE)
	if show {
		cmd = SW_SHOW
	}
	for _, h := range []uintptr{cc.prev, cc.next, cc.slider, cc.timeEdit, cc.timeUnit, cc.ilLabel, cc.ilByte, cc.xlLabel, cc.xlByte, cc.autoGeom, cc.swapGeom} {
		if h != 0 {
			pShowWindow.Call(h, cmd)
		}
	}
}

func comparePanelRectsAll() []RECT {
	cw, ch := clientSize(compareHwnd)
	top := 148
	bottom := 44
	marginX := 50
	n := 1
	if compareActiveB() {
		n = 2
		if compareShowDiff {
			n = 3
		}
	}
	gap := 46
	totalW := cw - 2*marginX - gap*(n-1)
	if totalW < n*180 {
		totalW = n * 180
	}
	each := totalW / n
	hh := ch - top - bottom
	if hh < 180 {
		hh = 180
	}
	out := make([]RECT, n)
	for i := 0; i < n; i++ {
		left := marginX + i*(each+gap)
		out[i] = RECT{Left: int32(left), Top: int32(top), Right: int32(left + each), Bottom: int32(top + hh)}
	}
	return out
}

func comparePanelRects() (RECT, RECT) {
	r := comparePanelRectsAll()
	if len(r) < 2 {
		return RECT{}, RECT{}
	}
	return r[0], r[1]
}

func comparePanelRect(panel int) RECT {
	r := comparePanelRectsAll()
	if panel < 0 || panel >= len(r) {
		return RECT{}
	}
	return r[panel]
}

func comparePanelCount() int {
	if !compareActiveB() {
		return 1
	}
	if compareShowDiff {
		return 3
	}
	return 2
}

func rectWH(r RECT) (int, int) { return int(r.Right - r.Left), int(r.Bottom - r.Top) }

func saveComparePanelBMP(path string, p comparePanel) error {
	if p.w <= 0 || p.h <= 0 || len(p.bgra) < p.w*p.h*4 {
		return fmt.Errorf("no rendered panel")
	}
	stride := ((p.w*3 + 3) / 4) * 4
	off := 54
	dataSize := stride * p.h
	buf := make([]byte, off+dataSize)
	buf[0], buf[1] = 'B', 'M'
	binary.LittleEndian.PutUint32(buf[2:6], uint32(len(buf)))
	binary.LittleEndian.PutUint32(buf[10:14], uint32(off))
	binary.LittleEndian.PutUint32(buf[14:18], 40)
	binary.LittleEndian.PutUint32(buf[18:22], uint32(p.w))
	binary.LittleEndian.PutUint32(buf[22:26], uint32(p.h))
	binary.LittleEndian.PutUint16(buf[26:28], 1)
	binary.LittleEndian.PutUint16(buf[28:30], 24)
	binary.LittleEndian.PutUint32(buf[34:38], uint32(dataSize))
	for y := 0; y < p.h; y++ {
		srcY := p.h - 1 - y
		dst := off + y*stride
		for x := 0; x < p.w; x++ {
			j := (srcY*p.w + x) * 4
			buf[dst+x*3] = p.bgra[j]
			buf[dst+x*3+1] = p.bgra[j+1]
			buf[dst+x*3+2] = p.bgra[j+2]
		}
	}
	return os.WriteFile(path, buf, 0644)
}

func compareCurrentPalette() [256]rgb {
	idx := comparePaletteIndex
	if idx < 0 || idx >= len(legacyAnchors) {
		idx = 2
	}
	a := legacyAnchors[idx]
	start, mid, end := tcolor(a[2]), tcolor(a[1]), tcolor(a[0])
	var p [256]rgb
	for i := 0; i < 128; i++ {
		p[i] = rgb{lerp(start.r, mid.r, i), lerp(start.g, mid.g, i), lerp(start.b, mid.b, i)}
		p[128+i] = rgb{lerp(mid.r, end.r, i), lerp(mid.g, end.g, i), lerp(mid.b, end.b, i)}
	}
	return p
}

func comparePaletteBGRA(idx []byte) []byte {
	pal := compareCurrentPalette()
	out := make([]byte, len(idx)*4)
	for i, v := range idx {
		c := pal[int(v)]
		j := i * 4
		out[j], out[j+1], out[j+2], out[j+3] = c.b, c.g, c.r, 0
	}
	return out
}

func renderCompareSection() {
	if compareAPath == "" || compareBPath == "" {
		return
	}
	fa, err := segy.Open(compareAPath)
	if err != nil {
		message(compareHwnd, "对比", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	defer fa.Close()
	fb, err := segy.Open(compareBPath)
	if err != nil {
		message(compareHwnd, "对比", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	defer fb.Close()
	compareDtAUS = fa.Info.SampleIntervalUS
	ar, br := comparePanelRects()
	aw, ah := rectWH(ar)
	bw, bh := rectWH(br)
	aw = maxInt(aw, 2)
	ah = maxInt(ah, 2)
	bw = maxInt(bw, 2)
	bh = maxInt(bh, 2)
	if aw > 1000 {
		aw = 1000
	}
	if ah > 1000 {
		ah = 1000
	}
	if bw > 1000 {
		bw = 1000
	}
	if bh > 1000 {
		bh = 1000
	}
	renderOne := func(f *segy.File, w, h int) (comparePanel, error) {
		tr1 := compareTraceEnd
		if tr1 < 0 || tr1 >= f.Info.TraceCount {
			tr1 = f.Info.TraceCount - 1
		}
		tr0 := compareTraceStart
		if tr0 < 0 {
			tr0 = 0
		}
		if tr0 > tr1 {
			tr0 = 0
		}
		sm1 := compareSampleEnd
		if sm1 < 0 || sm1 >= f.Info.SamplesPerTrace {
			sm1 = f.Info.SamplesPerTrace - 1
		}
		sm0 := compareSampleStart
		if sm0 < 0 {
			sm0 = 0
		}
		if sm0 > sm1 {
			sm0 = 0
		}
		pix, st, e := f.RenderWithOptions(segy.RenderOptions{Width: w, Height: h, AGC: agc, ClipPercent: clipPercent, GainPercent: gainPercent,
			UseValueLimits: useLimits, MinValue: limitMin, MaxValue: limitMax, TraceStart: tr0, TraceEnd: tr1, TraceStep: traceStep, SampleStart: sm0, SampleEnd: sm1, DisplayMode: renderDisplayMode})
		if e != nil {
			return comparePanel{}, e
		}
		return comparePanel{indices: pix, bgra: comparePaletteBGRA(pix), w: w, h: h, stats: st}, nil
	}
	pa, err := renderOne(fa, aw, ah)
	if err != nil {
		message(compareHwnd, "对比", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	pb, err := renderOne(fb, bw, bh)
	if err != nil {
		message(compareHwnd, "对比", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	pa.title = "A  " + shortPath(compareAPath, 42)
	pb.title = "B  " + shortPath(compareBPath, 42)
	compareA, compareB = pa, pb
	setText(cc.status, fmt.Sprintf("剖面对比 | 在任一窗口拖动框选 = A/B 同步 Zoom | 增益 %.0f%% | %s", gainPercent, paletteNames[comparePaletteIndex]))
	invalidateCompareBase()
}

func configureCompareModeSlider() {
	if compareTD == nil {
		return
	}
	if compareMode == 2 {
		pSendMessageW.Call(cc.slider, TBM_SETRANGEMIN, 1, 0)
		pSendMessageW.Call(cc.slider, TBM_SETRANGEMAX, 1, uintptr(compareTD.maxSampleA))
		freq := maxInt(compareTD.maxSampleA/50, 1)
		page := maxInt(compareTD.maxSampleA/100, 1)
		pSendMessageW.Call(cc.slider, TBM_SETTICFREQ, uintptr(freq), 0)
		pSendMessageW.Call(cc.slider, TBM_SETPAGESIZE, 0, uintptr(page))
		pSendMessageW.Call(cc.slider, TBM_SETLINESIZE, 0, 1)
		pSendMessageW.Call(cc.slider, TBM_SETPOS, 1, uintptr(compareTD.sampleA))
		setText(cc.timeUnit, "ms")
		return
	}
	b := compareTD.fullBounds
	var mn, mx int32
	if compareMode == 0 {
		mn, mx = b.InlineMin, b.InlineMax
		setText(cc.timeUnit, "IL")
	} else {
		mn, mx = b.CrosslineMin, b.CrosslineMax
		setText(cc.timeUnit, "XL")
	}
	if mx <= mn {
		mx = mn + 1
	}
	if compareLineCoord < mn || compareLineCoord > mx {
		compareLineCoord = mn + (mx-mn)/2
	}
	pSendMessageW.Call(cc.slider, TBM_SETRANGEMIN, 1, uintptr(int64(mn)))
	pSendMessageW.Call(cc.slider, TBM_SETRANGEMAX, 1, uintptr(int64(mx)))
	rng := maxInt(int(mx-mn), 1)
	pSendMessageW.Call(cc.slider, TBM_SETTICFREQ, uintptr(maxInt(rng/50, 1)), 0)
	pSendMessageW.Call(cc.slider, TBM_SETPAGESIZE, 0, uintptr(maxInt(rng/100, 1)))
	pSendMessageW.Call(cc.slider, TBM_SETLINESIZE, 0, 1)
	pSendMessageW.Call(cc.slider, TBM_SETPOS, 1, uintptr(int64(compareLineCoord)))
	setText(cc.timeEdit, strconv.Itoa(int(compareLineCoord)))
}

func resetCompareLineBounds() {
	if compareTD == nil {
		return
	}
	b := compareTD.fullBounds
	if compareMode == 0 {
		compareLineXMin, compareLineXMax = float64(b.CrosslineMin), float64(b.CrosslineMax)
	} else {
		compareLineXMin, compareLineXMax = float64(b.InlineMin), float64(b.InlineMax)
	}
	compareLineOriginXMin, compareLineOriginXMax = compareLineXMin, compareLineXMax
}

func filterLineRange(traces []int64, coords []int32, lo, hi float64) ([]int64, []int32) {
	if hi <= lo || len(traces) != len(coords) {
		return traces, coords
	}
	ot := make([]int64, 0, len(traces))
	oc := make([]int32, 0, len(coords))
	for i, c := range coords {
		if float64(c) >= lo && float64(c) <= hi {
			ot = append(ot, traces[i])
			oc = append(oc, c)
		}
	}
	if len(ot) < 2 {
		return traces, coords
	}
	return ot, oc
}

func pairLineTracesByCoord(tA []int64, cA []int32, tB []int64, cB []int32) ([]int64, []int64, []int32) {
	mb := make(map[int32]int64, len(cB))
	for i, c := range cB {
		if i < len(tB) {
			mb[c] = tB[i]
		}
	}
	pa := make([]int64, 0, minInt(len(tA), len(tB)))
	pb := make([]int64, 0, cap(pa))
	pc := make([]int32, 0, cap(pa))
	for i, c := range cA {
		if i >= len(tA) {
			break
		}
		if tb, ok := mb[c]; ok {
			pa = append(pa, tA[i])
			pb = append(pb, tb)
			pc = append(pc, c)
		}
	}
	return pa, pb, pc
}

func compareExportModeName() string {
	if compareMode == 0 {
		return "Inline"
	}
	if compareMode == 1 {
		return "Crossline"
	}
	return "TimeSlice"
}

func chooseCompareExportKind() string {
	if !compareActiveB() || compareTD == nil || compareTD.fb == nil || compareTD.gb == nil {
		return "A"
	}
	if compareShowDiff {
		r := askYesNoCancel(compareHwnd, "导出当前剖面", "当前显示 A / B / 差。\n\n是：导出差 (A-B)\n否：继续选择 A 或 B\n取消：退出")
		if r == IDCANCEL {
			return ""
		}
		if r == IDYES {
			return "差"
		}
	}
	r := askYesNoCancel(compareHwnd, "导出当前剖面", "请选择导出对象：\n\n是：A\n否：B\n取消：退出")
	if r == IDYES {
		return "A"
	}
	if r == IDNO {
		return "B"
	}
	return ""
}

func exportDefaultName(srcPath, kind, mode string, coord int32) string {
	base := strings.TrimSuffix(filepath.Base(srcPath), filepath.Ext(srcPath))
	if base == "" {
		base = "SeisForgeStudio"
	}
	kindPart := kind
	if kind == "差" {
		kindPart = "A-B"
	}
	return fmt.Sprintf("%s_%s_%s_%d.sgy", base, kindPart, mode, coord)
}

func startExportCurrentSection() {
	if compareTD == nil || compareTD.fa == nil || compareTD.ga == nil {
		message(compareHwnd, "导出当前剖面", "请先打开 A 数据并完成几何加载。", MB_OK|MB_ICONINFORMATION)
		return
	}
	if compareMode == 2 {
		message(compareHwnd, "导出当前剖面", "Time Slice 是平面切片，不是普通二维时间剖面。\n\n请先切换到 Inline 或 Crossline，再导出当前显示剖面。", MB_OK|MB_ICONINFORMATION)
		return
	}
	kind := chooseCompareExportKind()
	if kind == "" {
		return
	}
	axis := compareMode
	tA, cA, actualA, err := compareTD.ga.LineTraceNumbers(axis, compareLineCoord)
	if err != nil {
		message(compareHwnd, "导出当前剖面", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	tA, cA = filterLineRange(tA, cA, compareLineXMin, compareLineXMax)
	if len(tA) == 0 {
		message(compareHwnd, "导出当前剖面", "当前显示范围内没有可导出的 traces。", MB_OK|MB_ICONERROR)
		return
	}
	modeName := compareExportModeName()
	actual := actualA
	srcPath := compareAPath
	var tB []int64
	if kind == "B" || kind == "差" {
		if compareTD.fb == nil || compareTD.gb == nil {
			message(compareHwnd, "导出当前剖面", "当前没有可用的 B 数据。", MB_OK|MB_ICONERROR)
			return
		}
		var cB []int32
		var actualB int32
		tB, cB, actualB, err = compareTD.gb.LineTraceNumbers(axis, compareLineCoord)
		if err != nil {
			message(compareHwnd, "导出当前剖面", err.Error(), MB_OK|MB_ICONERROR)
			return
		}
		tB, cB = filterLineRange(tB, cB, compareLineXMin, compareLineXMax)
		if kind == "B" {
			actual = actualB
			srcPath = compareBPath
		} else {
			if actualA != actualB {
				message(compareHwnd, "导出当前剖面", fmt.Sprintf("A/B 实际 %s 不一致 (%d / %d)，无法计算残差。", modeName, actualA, actualB), MB_OK|MB_ICONERROR)
				return
			}
			var common []int32
			tA, tB, common = pairLineTracesByCoord(tA, cA, tB, cB)
			_ = common
			if len(tA) == 0 {
				message(compareHwnd, "导出当前剖面", "A/B 当前范围没有共同几何 traces，无法导出残差。", MB_OK|MB_ICONERROR)
				return
			}
			srcPath = compareAPath
		}
	}
	defaultName := exportDefaultName(srcPath, kind, modeName, actual)
	outPath := saveSegyDialog(compareHwnd, defaultName)
	if outPath == "" {
		return
	}
	sm0, sm1 := compareSampleStart, compareSampleEnd
	if sm0 < 0 {
		sm0 = 0
	}
	if sm1 < sm0 {
		sm1 = sm0
	}
	traceCount := len(tA)
	if kind == "B" {
		traceCount = len(tB)
	}
	sampleCount := sm1 - sm0 + 1
	setCompareBusy(1)
	setCompareProgress(0, true)
	setText(cc.status, fmt.Sprintf("正在导出 %s：%s %d | %d traces × %d samples ...", kind, modeName, actual, traceCount, sampleCount))
	pathA, pathB := compareAPath, compareBPath
	go func(kind, path, srcA, srcB string, aTraces, bTraces []int64, samples0, samples1 int, tracesN, samplesN int) {
		lastPct := -1
		progress := func(done, total int) {
			if total <= 0 {
				return
			}
			pct := done * 100 / total
			if pct != lastPct {
				lastPct = pct
				postCompareProgress(pct)
			}
		}
		var e error
		fa, ea := segy.Open(srcA)
		if ea != nil {
			e = ea
		} else {
			defer fa.Close()
			switch kind {
			case "B":
				fb, eb := segy.Open(srcB)
				if eb != nil {
					e = eb
				} else {
					defer fb.Close()
					e = fb.ExportSection(path, segy.SectionExportOptions{TraceIndices: bTraces, SampleStart: samples0, SampleEnd: samples1, Progress: progress})
				}
			case "差":
				fb, eb := segy.Open(srcB)
				if eb != nil {
					e = eb
				} else {
					defer fb.Close()
					e = segy.ExportDifferenceSection(path, fa, fb, aTraces, bTraces, samples0, samples1, progress)
				}
			default:
				e = fa.ExportSection(path, segy.SectionExportOptions{TraceIndices: aTraces, SampleStart: samples0, SampleEnd: samples1, Progress: progress})
			}
		}
		compareExportResultCh <- compareExportResult{path: path, kind: kind, traces: tracesN, samples: samplesN, err: e}
		if compareHwnd != 0 {
			pPostMessageW.Call(compareHwnd, WM_COMPARE_EXPORT_DONE, 0, 0)
		}
	}(kind, outPath, pathA, pathB, append([]int64(nil), tA...), append([]int64(nil), tB...), sm0, sm1, traceCount, sampleCount)
}

func handleCompareExportDone() {
	var r compareExportResult
	select {
	case r = <-compareExportResultCh:
	default:
		return
	}
	setCompareBusy(-1)
	setCompareProgress(100, false)
	if r.err != nil {
		setText(cc.status, "导出失败："+r.err.Error())
		message(compareHwnd, "导出 SEG-Y 失败", r.err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	setText(cc.status, fmt.Sprintf("导出完成 | %s | %d traces × %d samples | %s", r.kind, r.traces, r.samples, r.path))
	message(compareHwnd, "导出当前剖面", fmt.Sprintf("导出完成。\n\n对象：%s\nTraces：%d\nSamples/trace：%d\n\n%s", r.kind, r.traces, r.samples, r.path), MB_OK|MB_ICONINFORMATION)
}

func renderCompareLine() {
	d := compareTD
	if d == nil || (compareMode != 0 && compareMode != 1) {
		return
	}
	axis := compareMode
	target := compareLineCoord
	rects := comparePanelRectsAll()
	if len(rects) < 1 {
		return
	}
	aw, ah := rectWH(rects[0])
	aw = minInt(maxInt(aw, 2), 1000)
	ah = minInt(maxInt(ah, 2), 1000)
	tA, cA, actualA, eA := d.ga.LineTraceNumbers(axis, target)
	if eA != nil {
		setText(cc.status, eA.Error())
		return
	}
	tA, cA = filterLineRange(tA, cA, compareLineXMin, compareLineXMax)
	opts := func(w, h int) segy.RenderOptions {
		return segy.RenderOptions{Width: w, Height: h, AGC: agc, ClipPercent: clipPercent, GainPercent: gainPercent, UseValueLimits: useLimits, MinValue: limitMin, MaxValue: limitMax, SampleStart: compareSampleStart, SampleEnd: compareSampleEnd, DisplayMode: renderDisplayMode}
	}
	paPix, paSt, e := d.fa.RenderTraceIndices(tA, opts(aw, ah))
	if e != nil {
		setText(cc.status, e.Error())
		return
	}
	modeName := "Inline"
	axisName := "Crossline"
	if compareMode == 1 {
		modeName = "Crossline"
		axisName = "Inline"
	}
	compareA = comparePanel{indices: paPix, bgra: comparePaletteBGRA(paPix), w: aw, h: ah, stats: paSt, title: fmt.Sprintf("A  %s %d", modeName, actualA)}
	compareB = comparePanel{}
	compareD = comparePanel{}
	hasB := compareActiveB() && d.gb != nil && d.fb != nil && len(rects) >= 2
	diffNote := ""
	actualB := int32(0)
	if hasB {
		bw, bh := rectWH(rects[1])
		bw = minInt(maxInt(bw, 2), 1000)
		bh = minInt(maxInt(bh, 2), 1000)
		tB, cB, aB, eB := d.gb.LineTraceNumbers(axis, target)
		actualB = aB
		if eB != nil {
			setText(cc.status, eB.Error())
			return
		}
		tB, cB = filterLineRange(tB, cB, compareLineXMin, compareLineXMax)
		pbPix, pbSt, er := d.fb.RenderTraceIndices(tB, opts(bw, bh))
		if er != nil {
			setText(cc.status, er.Error())
			return
		}
		compareB = comparePanel{indices: pbPix, bgra: comparePaletteBGRA(pbPix), w: bw, h: bh, stats: pbSt, title: fmt.Sprintf("B  %s %d", modeName, actualB)}
		if compareShowDiff && len(rects) >= 3 {
			if actualA != actualB {
				diffNote = fmt.Sprintf(" | 差未计算：A/B实际%s不同(%d/%d)", modeName, actualA, actualB)
			} else {
				pA, pB, _ := pairLineTracesByCoord(tA, cA, tB, cB)
				if len(pA) >= 2 {
					dw, dh := rectWH(rects[2])
					dw = minInt(maxInt(dw, 2), 1000)
					dh = minInt(maxInt(dh, 2), 1000)
					dPix, dSt, de := segy.RenderTraceDifferencePairs(d.fa, d.fb, pA, pB, opts(dw, dh))
					if de == nil {
						compareD = comparePanel{indices: dPix, bgra: comparePaletteBGRA(dPix), w: dw, h: dh, stats: dSt, title: fmt.Sprintf("差  A-B  %s %d", modeName, actualA)}
					} else {
						diffNote = " | 差未计算：" + de.Error()
					}
				} else {
					diffNote = " | 差未计算：A/B 没有足够共同几何位置"
				}
			}
		}
	}
	setText(cc.timeEdit, strconv.Itoa(int(target)))
	pSendMessageW.Call(cc.slider, TBM_SETPOS, 1, uintptr(int64(target)))
	if hasB {
		extra := ""
		if compareShowDiff && diffNote == "" {
			extra = " | 差=A-B（零对称范围）"
		}
		setText(cc.status, fmt.Sprintf("%s 对比 | A=%d B=%d | %s %.0f..%.0f | 增益 %.0f%% | %s%s%s", modeName, actualA, actualB, axisName, compareLineXMin, compareLineXMax, gainPercent, paletteNames[comparePaletteIndex], extra, diffNote))
	} else {
		setText(cc.status, fmt.Sprintf("A 浏览 | %s %d | %s %.0f..%.0f | 增益 %.0f%% | %s | 点击 比 加载 B", modeName, actualA, axisName, compareLineXMin, compareLineXMax, gainPercent, paletteNames[comparePaletteIndex]))
	}
	invalidateCompareBase()
}

func startLoadComparePosition(pos int) {
	if compareTD == nil {
		return
	}
	if compareMode == 2 {
		startLoadTimeSlice(pos)
		return
	}
	compareLineCoord = int32(pos)
	renderCompareLine()
}

func refreshComparePaletteOnly() {
	if compareHwnd == 0 {
		return
	}
	if compareMode != 2 {
		if len(compareA.indices) > 0 {
			compareA.bgra = comparePaletteBGRA(compareA.indices)
		}
		if len(compareB.indices) > 0 {
			compareB.bgra = comparePaletteBGRA(compareB.indices)
		}
		if len(compareD.indices) > 0 {
			compareD.bgra = comparePaletteBGRA(compareD.indices)
		}
		invalidateCompareBase()
		return
	}
	if compareTD != nil {
		if len(compareTD.currentValsA) > 0 {
			updateTimeSliceImages(compareTD.currentValsA, compareTD.currentValsB)
			return
		}
		if va, ok := compareTD.ca.GetSliceCached(compareTD.sampleA); ok {
			if compareTD.cb == nil {
				updateTimeSliceImages(va, nil)
				return
			}
			if vb, ok2 := compareTD.cb.GetSliceCached(compareTD.sampleB); ok2 {
				updateTimeSliceImages(va, vb)
			}
		}
	}
}

func refreshCompareGainOrLimits() {
	if compareHwnd == 0 {
		return
	}
	if compareMode != 2 {
		renderCompareLine()
		return
	}
	if compareTD != nil {
		if len(compareTD.currentValsA) > 0 {
			updateTimeSliceImages(compareTD.currentValsA, compareTD.currentValsB)
			return
		}
		if va, ok := compareTD.ca.GetSliceCached(compareTD.sampleA); ok {
			if compareTD.cb == nil {
				updateTimeSliceImages(va, nil)
				return
			}
			if vb, ok2 := compareTD.cb.GetSliceCached(compareTD.sampleB); ok2 {
				updateTimeSliceImages(va, vb)
			}
		}
	}
}

func chooseComparePath(which int) bool {
	setCompareTraceInspectMode(false)
	p := openDataDialog(compareHwnd)
	if p == "" {
		return false
	}
	if which == 0 {
		compareAPath = p
		compareBPath = ""
		compareBVisible = false
		compareShowDiff = false
		compareZoomToggleValid = false
		pSendMessageW.Call(cc.compare, BM_SETCHECK, 0, 0)
		pSendMessageW.Call(cc.diff, BM_SETCHECK, 0, 0)
		pEnableWindow.Call(cc.diff, 0)
	} else {
		compareBPath = p
		compareBVisible = true
		compareZoomToggleValid = false
		pSendMessageW.Call(cc.compare, BM_SETCHECK, BST_CHECKED, 0)
		pEnableWindow.Call(cc.diff, 1)
	}
	layoutComparePathLabels()
	compareAutoGeom = false
	invalidateCompareTimeData()
	if compareAPath != "" {
		// Auto geometry is the default for both A-only browsing and A/B QC.
		startAutoDetectGeometry()
	}
	return true
}

func invalidateCompareTimeData() {
	atomic.AddInt64(&compareGen, 1)
	atomic.AddInt64(&compareSliceGen, 1)
	if compareTD != nil {
		compareTD.close()
		compareTD = nil
	}
}

func compareGeometryBytesForFiles() (int, int, int, int, bool) {
	il, xl, ok := compareHeaderBytes()
	if !ok {
		return 0, 0, 0, 0, false
	}
	if compareAutoGeom && il == compareAutoILA && xl == compareAutoXLA {
		return compareAutoILA, compareAutoXLA, compareAutoILB, compareAutoXLB, true
	}
	return il, xl, il, xl, true
}

func setCompareBusy(delta int32) {
	if delta > 0 {
		atomic.AddInt32(&compareBusyCount, delta)
	} else if delta < 0 {
		for {
			v := atomic.LoadInt32(&compareBusyCount)
			if v <= 0 {
				atomic.StoreInt32(&compareBusyCount, 0)
				break
			}
			if atomic.CompareAndSwapInt32(&compareBusyCount, v, v+delta) {
				break
			}
		}
	}
	if compareHwnd == 0 {
		return
	}
	cursorID := uintptr(IDC_ARROW)
	if atomic.LoadInt32(&compareBusyCount) > 0 {
		cursorID = IDC_WAIT
	}
	c, _, _ := pLoadCursorW.Call(0, cursorID)
	if c != 0 {
		pSetCursor.Call(c)
	}
}

func cycleComparePalette(delta int) {
	if len(paletteNames) == 0 {
		return
	}
	comparePaletteIndex = (comparePaletteIndex + delta) % len(paletteNames)
	if comparePaletteIndex < 0 {
		comparePaletteIndex += len(paletteNames)
	}
	if cc.palette != 0 {
		pSendMessageW.Call(cc.palette, CB_SETCURSEL, uintptr(comparePaletteIndex), 0)
	}
	paletteIndex = comparePaletteIndex
	if comboColor != 0 {
		pSendMessageW.Call(comboColor, CB_SETCURSEL, uintptr(paletteIndex), 0)
	}
	refreshComparePaletteOnly()
	setText(cc.status, "对比色标："+paletteNames[comparePaletteIndex]+"（A/B/差 联动）")
}

func startAutoDetectGeometry() {
	if compareAPath == "" {
		message(compareHwnd, "自动识别", "请先选择 A 数据。", MB_OK|MB_ICONINFORMATION)
		return
	}
	gen := atomic.AddInt64(&compareGeomGen, 1)
	setCompareBusy(1)
	setCompareProgress(3, true)
	setText(cc.status, "正在自动识别 Inline/Crossline 字节位置...（仅抽样读取 240-byte 道头）")
	go func(aPath, bPath string, g int64) {
		r := &compareGeomDetectResult{gen: g, hasB: bPath != ""}
		detect := func(path string) (segy.GeometryDetectResult, error) {
			f, err := segy.Open(path)
			if err != nil {
				return segy.GeometryDetectResult{}, err
			}
			defer f.Close()
			return f.DetectGeometryBytes(3072)
		}
		if r.hasB {
			var wg sync.WaitGroup
			wg.Add(2)
			go func() { defer wg.Done(); r.a, r.errA = detect(aPath) }()
			go func() { defer wg.Done(); r.b, r.errB = detect(bPath) }()
			wg.Wait()
		} else {
			r.a, r.errA = detect(aPath)
		}
		compareAsyncMu.Lock()
		comparePendingGeom = r
		compareAsyncMu.Unlock()
		if compareHwnd != 0 {
			pPostMessageW.Call(compareHwnd, WM_COMPARE_GEOM_READY, 0, 0)
		}
	}(compareAPath, compareBPath, gen)
}

func confidenceName(v float64, ambiguous bool) string {
	if !ambiguous && v >= 0.82 {
		return "高"
	}
	if v >= 0.62 {
		return "中"
	}
	return "低"
}

func handleGeometryDetectReady() {
	defer setCompareBusy(-1)
	setCompareProgress(12, true)
	compareAsyncMu.Lock()
	r := comparePendingGeom
	comparePendingGeom = nil
	compareAsyncMu.Unlock()
	if r == nil || r.gen != atomic.LoadInt64(&compareGeomGen) {
		return
	}
	if r.errA != nil {
		compareAutoGeom = false
		setText(cc.status, "自动识别失败，将使用当前手动字节位置："+r.errA.Error())
		if compareAPath != "" {
			startPrepareTimeCompare()
		}
		return
	}
	if r.a.Ambiguous {
		setText(cc.status, fmt.Sprintf("A 自动识别候选 IL/XL=%d/%d，score %.1f，置信度%s；建议核对或使用 IL/XL交换。", r.a.InlineByte, r.a.CrosslineByte, r.a.Score, confidenceName(r.a.Confidence, true)))
	}
	compareAutoILA, compareAutoXLA = r.a.InlineByte, r.a.CrosslineByte
	compareAutoILB, compareAutoXLB = compareAutoILA, compareAutoXLA
	if r.hasB {
		if r.errB != nil {
			// Fall back to A's detected layout for B. This is common for before/after
			// processing pairs and still allows a manual override when necessary.
			compareAutoILB, compareAutoXLB = compareAutoILA, compareAutoXLA
		} else {
			compareAutoILB, compareAutoXLB = r.b.InlineByte, r.b.CrosslineByte
		}
	}
	compareAutoGeom = true
	setText(cc.ilByte, strconv.Itoa(compareAutoILA))
	setText(cc.xlByte, strconv.Itoa(compareAutoXLA))
	msg := fmt.Sprintf("自动识别 A: IL/XL %d/%d | score %.1f | 置信度%s | 抽样 %d headers, %.2fs",
		r.a.InlineByte, r.a.CrosslineByte, r.a.Score, confidenceName(r.a.Confidence, r.a.Ambiguous), r.a.SampleHeaders, r.a.Duration.Seconds())
	if r.hasB {
		if r.errB != nil {
			msg += fmt.Sprintf("   B: 识别失败，暂用 A 的 %d/%d", compareAutoILB, compareAutoXLB)
		} else {
			msg += fmt.Sprintf("   B: %d/%d | score %.1f | 置信度%s", r.b.InlineByte, r.b.CrosslineByte, r.b.Score, confidenceName(r.b.Confidence, r.b.Ambiguous))
			if r.a.InlineByte != r.b.InlineByte || r.a.CrosslineByte != r.b.CrosslineByte {
				msg += " | A/B 字节位置不同，" + APP_NAME + " 将分别使用各自结果"
			}
		}
	}
	setText(cc.status, msg)
	bWarn := r.hasB && r.errB == nil && (r.b.Ambiguous || r.b.LabelsHeuristic)
	if r.a.Ambiguous || r.a.LabelsHeuristic || bWarn {
		message(compareHwnd, "自动识别", msg+"\n\n注意："+APP_NAME+" 对两个网格轴的字节位置可以给出统计置信度；但非标准 SEG-Y 缺少明确元数据时，Inline/Crossline 的名字本身可能只能根据道序推断。如方向反了，请点“IL/XL交换”。", MB_OK|MB_ICONINFORMATION)
	}
	invalidateCompareTimeData()
	if compareAPath != "" {
		startPrepareTimeCompare()
	}
}

func swapCompareGeometryBytes() {
	il, xl, ok := compareHeaderBytes()
	if !ok {
		return
	}
	setText(cc.ilByte, strconv.Itoa(xl))
	setText(cc.xlByte, strconv.Itoa(il))
	if compareAutoGeom {
		compareAutoILA, compareAutoXLA = compareAutoXLA, compareAutoILA
		compareAutoILB, compareAutoXLB = compareAutoXLB, compareAutoILB
	}
	invalidateCompareTimeData()
	if compareAPath != "" {
		startPrepareTimeCompare()
	}
}

func compareHeaderBytes() (int, int, bool) {
	il, err1 := strconv.Atoi(strings.TrimSpace(getText(cc.ilByte)))
	xl, err2 := strconv.Atoi(strings.TrimSpace(getText(cc.xlByte)))
	if err1 != nil || err2 != nil || il < 1 || il > 237 || xl < 1 || xl > 237 {
		message(compareHwnd, "Time Slice", "Inline/Crossline header byte 应为 1–237 的整数。标准 SEG-Y Rev-1 通常为 189 / 193。", MB_OK|MB_ICONERROR)
		return 0, 0, false
	}
	return il, xl, true
}

func startPrepareTimeCompare() {
	if compareAPath == "" {
		setText(cc.status, "请先打开 A 数据。")
		return
	}
	ilA, xlA, ilB, xlB, okBytes := compareGeometryBytesForFiles()
	if !okBytes {
		return
	}
	hasB := compareHasB()
	gen := atomic.AddInt64(&compareGen, 1)
	setCompareBusy(1)
	setCompareProgress(15, true)
	if hasB {
		setText(cc.status, fmt.Sprintf("正在建立/读取几何索引并准备 A/B | A %d/%d, B %d/%d...", ilA, xlA, ilB, xlB))
	} else {
		setText(cc.status, fmt.Sprintf("正在建立/读取 A 的 Inline–Crossline 几何索引 | %d/%d...", ilA, xlA))
	}
	go func(aPath, bPath string, g int64, ilA, xlA, ilB, xlB int, hasB bool) {
		res := &comparePrepareResult{gen: g}
		fa, e := segy.Open(aPath)
		if e != nil {
			res.err = e
			postComparePrepare(res)
			return
		}
		var fb *segy.File
		if hasB {
			fb, e = segy.Open(bPath)
			if e != nil {
				fa.Close()
				res.err = e
				postComparePrepare(res)
				return
			}
		}
		type giRes struct {
			idx *segy.GeometryIndex
			st  segy.GeometryBuildStats
			err error
		}
		cha := make(chan giRes, 1)
		var progA, progB int64
		reportProgress := func(which int, done, total int) {
			if total <= 0 {
				return
			}
			p := int64(done * 100 / total)
			if which == 0 {
				atomic.StoreInt64(&progA, p)
			} else {
				atomic.StoreInt64(&progB, p)
			}
			combined := atomic.LoadInt64(&progA)
			div := int64(1)
			if hasB {
				combined += atomic.LoadInt64(&progB)
				div = 2
			}
			pct := 15 + int(65*(combined/div)/100)
			postCompareProgress(pct)
		}
		go func() {
			x, st, e := fa.BuildGeometryIndexCachedProgress(ilA, xlA, 0, func(d, t int) { reportProgress(0, d, t) })
			cha <- giRes{x, st, e}
		}()
		var chb chan giRes
		if hasB {
			chb = make(chan giRes, 1)
			go func() {
				x, st, e := fb.BuildGeometryIndexCachedProgress(ilB, xlB, 0, func(d, t int) { reportProgress(1, d, t) })
				chb <- giRes{x, st, e}
			}()
		}
		ra := <-cha
		if ra.err != nil {
			fa.Close()
			if fb != nil {
				fb.Close()
			}
			res.err = ra.err
			postComparePrepare(res)
			return
		}
		var rb giRes
		if hasB {
			rb = <-chb
			if rb.err != nil {
				fa.Close()
				fb.Close()
				res.err = rb.err
				postComparePrepare(res)
				return
			}
		}
		if !ra.idx.Poststack || (hasB && !rb.idx.Poststack) {
			fa.Close()
			if fb != nil {
				fb.Close()
			}
			res.err = fmt.Errorf("Inline/Crossline/Time Slice 浏览需要叠后三维体：检测到同一 IL/XL bin 存在较多重复道（可能是叠前数据）")
			postComparePrepare(res)
			return
		}
		ca := segy.NewTimeSliceCache(fa, ra.idx)
		var cb *segy.TimeSliceCache
		if hasB {
			cb = segy.NewTimeSliceCache(fb, rb.idx)
		}
		maxA := fa.Info.SamplesPerTrace - 1
		if hasB {
			if dtA, dtB := int64(fa.Info.SampleIntervalUS), int64(fb.Info.SampleIntervalUS); dtA > 0 && dtB > 0 {
				m := int((int64(fb.Info.SamplesPerTrace-1) * dtB) / dtA)
				if m < maxA {
					maxA = m
				}
			}
		}
		init := compareSampleStart
		if init < 0 {
			init = 0
		}
		if init > maxA {
			init = maxA
		}
		var sampleB int
		if hasB {
			sampleB = sampleBForA(init, fa, fb)
		}
		// v1.2.5: Time Slice is lazy. Preparing an Inline/Crossline view must
		// never touch one sample from every trace in the 3-D volume. The old
		// eager slab load was the dominant first-open bottleneck on large cubes.
		// A slab is now requested only when the user actually switches to Time Slice.
		postCompareProgress(92)
		var va, vb []float32
		cacheTag := func(s segy.GeometryBuildStats) string {
			if s.FromCache {
				return "cache"
			}
			if s.FastRegular {
				return fmt.Sprintf("fast-grid %.2fs/%d hdr", s.Duration.Seconds(), s.HeaderReads)
			}
			return fmt.Sprintf("full-scan %.2fs", s.Duration.Seconds())
		}
		fullBounds := ra.idx.Bounds()
		status := fmt.Sprintf("Geometry A:%s [%d/%d] | slab:%d samples", cacheTag(ra.st), ilA, xlA, ca.BlockSamples())
		if hasB {
			fullBounds = segy.UnionSliceBounds(ra.idx, rb.idx)
			status = fmt.Sprintf("Geometry A:%s [%d/%d], B:%s [%d/%d] | slab A:%d, B:%d samples", cacheTag(ra.st), ilA, xlA, cacheTag(rb.st), ilB, xlB, ca.BlockSamples(), cb.BlockSamples())
		}
		d := &compareTimeData{fa: fa, fb: fb, ga: ra.idx, gb: rb.idx, ca: ca, cb: cb, bounds: fullBounds, fullBounds: fullBounds, maxSampleA: maxA, sampleA: init, sampleB: sampleB, indexStatus: status}
		res.data, res.valsA, res.valsB = d, va, vb
		postComparePrepare(res)
	}(compareAPath, compareBPath, gen, ilA, xlA, ilB, xlB, hasB)
}

func postComparePrepare(r *comparePrepareResult) {
	if compareHwnd == 0 {
		if r != nil && r.data != nil {
			r.data.close()
		}
		return
	}
	compareAsyncMu.Lock()
	if comparePendingPrepare != nil && comparePendingPrepare.data != nil {
		comparePendingPrepare.data.close()
	}
	comparePendingPrepare = r
	compareAsyncMu.Unlock()
	pPostMessageW.Call(compareHwnd, WM_COMPARE_READY, 0, 0)
}
func postCompareSlice(r *compareSliceResult) {
	if compareHwnd == 0 {
		return
	}
	compareAsyncMu.Lock()
	comparePendingSlice = r
	compareAsyncMu.Unlock()
	pPostMessageW.Call(compareHwnd, WM_COMPARE_SLICE_READY, 0, 0)
}

func sampleBForA(sampleA int, fa, fb *segy.File) int {
	us := int64(sampleA) * int64(fa.Info.SampleIntervalUS)
	b := int(math.Round(float64(us) / float64(fb.Info.SampleIntervalUS)))
	if b < 0 {
		b = 0
	}
	if b >= fb.Info.SamplesPerTrace {
		b = fb.Info.SamplesPerTrace - 1
	}
	return b
}

func startLoadTimeSlice(sampleA int) {
	d := compareTD
	if d == nil {
		return
	}
	if sampleA < 0 {
		sampleA = 0
	}
	if sampleA > d.maxSampleA {
		sampleA = d.maxSampleA
	}
	hasB := d.fb != nil && d.cb != nil
	sampleB := 0
	if hasB {
		sampleB = sampleBForA(sampleA, d.fa, d.fb)
	}
	if va, oka := d.ca.GetSliceCached(sampleA); oka {
		if !hasB {
			d.sampleA = sampleA
			updateTimeSliceImages(va, nil)
			prefetchAround(d, sampleA, 0)
			return
		}
		if vb, okb := d.cb.GetSliceCached(sampleB); okb {
			d.sampleA, d.sampleB = sampleA, sampleB
			updateTimeSliceImages(va, vb)
			prefetchAround(d, sampleA, sampleB)
			return
		}
	}
	gen := atomic.AddInt64(&compareSliceGen, 1)
	setCompareBusy(1)
	setText(cc.status, fmt.Sprintf("正在缓存 Time Slice slab...  %s", formatAdaptiveTimeMS(float64(sampleA*d.fa.Info.SampleIntervalUS)/1000.0)))
	go func(dd *compareTimeData, sa, sb int, g int64, hasB bool) {
		r := &compareSliceResult{gen: g, sampleA: sa, sampleB: sb}
		var ea, eb error
		if hasB {
			var wg sync.WaitGroup
			wg.Add(2)
			go func() { defer wg.Done(); r.valsA, r.statA, ea = dd.ca.GetSlice(sa) }()
			go func() { defer wg.Done(); r.valsB, r.statB, eb = dd.cb.GetSlice(sb) }()
			wg.Wait()
		} else {
			r.valsA, r.statA, ea = dd.ca.GetSlice(sa)
		}
		if ea != nil {
			r.err = ea
		} else if eb != nil {
			r.err = eb
		}
		postCompareSlice(r)
	}(d, sampleA, sampleB, gen, hasB)
}

func prefetchAround(d *compareTimeData, sa, sb int) {
	if d == nil || d.ca == nil {
		return
	}
	ba := d.ca.BlockSamples()
	d.ca.Prefetch(sa + ba)
	d.ca.Prefetch(sa - ba)
	if d.cb != nil {
		bb := d.cb.BlockSamples()
		d.cb.Prefetch(sb + bb)
		d.cb.Prefetch(sb - bb)
	}
}

func sharedSliceRange(a []float32, ma []bool, b []float32, mb []bool) (float64, float64) {
	if useLimits && limitMax > limitMin {
		return limitMin, limitMax
	}
	total := len(a) + len(b)
	step := 1
	if total > 200000 {
		step = total / 200000
	}
	vals := make([]float64, 0, minInt(total/step+2, 200002))
	add := func(v []float32, m []bool) {
		for i := 0; i < len(v); i += step {
			if i < len(m) && m[i] {
				x := float64(v[i])
				if !math.IsNaN(x) && !math.IsInf(x, 0) {
					vals = append(vals, x)
				}
			}
		}
	}
	add(a, ma)
	add(b, mb)
	if len(vals) == 0 {
		return -1, 1
	}
	sort.Float64s(vals)
	g := gainPercent
	if g < 0 {
		g = 0
	}
	if g > 49 {
		g = 49
	}
	li := int(float64(len(vals)) * g / 100)
	hi := int(float64(len(vals)) * (100 - g) / 100)
	if li < 0 {
		li = 0
	}
	if li >= len(vals) {
		li = len(vals) - 1
	}
	if hi < 0 {
		hi = 0
	}
	if hi >= len(vals) {
		hi = len(vals) - 1
	}
	lo, hv := vals[li], vals[hi]
	if hv < lo {
		lo, hv = hv, lo
	}
	if hv <= lo {
		hv = lo + 1
	}
	return lo, hv
}

func symmetricResidualRange(v []float32, mask []bool) (float64, float64) {
	vals := make([]float64, 0, minInt(len(v), 200000))
	step := 1
	if len(v) > 200000 {
		step = len(v) / 200000
	}
	for i := 0; i < len(v); i += step {
		if i < len(mask) && mask[i] {
			x := math.Abs(float64(v[i]))
			if !math.IsNaN(x) && !math.IsInf(x, 0) {
				vals = append(vals, x)
			}
		}
	}
	if len(vals) == 0 {
		return -1, 1
	}
	sort.Float64s(vals)
	g := gainPercent
	if g < 0 {
		g = 0
	}
	if g > 49 {
		g = 49
	}
	hi := int(float64(len(vals)) * (100.0 - g) / 100.0)
	if hi < 0 {
		hi = 0
	}
	if hi >= len(vals) {
		hi = len(vals) - 1
	}
	amp := vals[hi]
	if useLimits && limitMax > limitMin {
		amp = math.Max(math.Abs(limitMin), math.Abs(limitMax))
	}
	if amp <= 0 {
		amp = 1
	}
	return -amp, amp
}

func differenceRaster(a []float32, ma []bool, b []float32, mb []bool) ([]float32, []bool) {
	n := minInt(len(a), len(b))
	out := make([]float32, n)
	mask := make([]bool, n)
	for i := 0; i < n; i++ {
		if i < len(ma) && i < len(mb) && ma[i] && mb[i] {
			out[i] = a[i] - b[i]
			mask[i] = true
		}
	}
	return out, mask
}

func rasterToBGRA(v []float32, mask []bool, lo, hi float64) []byte {
	pal := compareCurrentPalette()
	out := make([]byte, len(v)*4)
	den := hi - lo
	if den <= 0 {
		den = 1
	}
	for i, x32 := range v {
		j := i * 4
		if i >= len(mask) || !mask[i] {
			out[j], out[j+1], out[j+2], out[j+3] = 255, 255, 255, 0
			continue
		}
		x := float64(x32)
		if x < lo {
			x = lo
		}
		if x > hi {
			x = hi
		}
		idx := int(math.Round(255 * (hi - x) / den))
		if idx < 0 {
			idx = 0
		}
		if idx > 255 {
			idx = 255
		}
		c := pal[idx]
		out[j], out[j+1], out[j+2], out[j+3] = c.b, c.g, c.r, 0
	}
	return out
}

func updateTimeSliceImages(valsA, valsB []float32) {
	d := compareTD
	if d == nil || len(valsA) == 0 {
		return
	}
	d.currentValsA = append(d.currentValsA[:0], valsA...)
	if len(valsB) > 0 {
		d.currentValsB = append(d.currentValsB[:0], valsB...)
	} else {
		d.currentValsB = d.currentValsB[:0]
	}
	r := comparePanelRectsAll()
	if len(r) < 1 {
		return
	}
	aw, ah := rectWH(r[0])
	aw = minInt(maxInt(aw, 2), 900)
	ah = minInt(maxInt(ah, 2), 900)
	ra, ma, ea := d.ga.RasterizeTimeSlice(valsA, aw, ah, d.bounds)
	if ea != nil {
		message(compareHwnd, "Time Slice", ea.Error(), MB_OK|MB_ICONERROR)
		return
	}
	hasB := compareActiveB() && d.gb != nil && len(valsB) > 0 && len(r) >= 2
	var rb []float32
	var mb []bool
	if hasB {
		bw, bh := rectWH(r[1])
		bw = minInt(maxInt(bw, 2), 900)
		bh = minInt(maxInt(bh, 2), 900)
		var eb error
		rb, mb, eb = d.gb.RasterizeTimeSlice(valsB, bw, bh, d.bounds)
		if eb != nil {
			message(compareHwnd, "Time Slice", eb.Error(), MB_OK|MB_ICONERROR)
			return
		}
	}
	lo, hi := sharedSliceRange(ra, ma, rb, mb)
	compareMapMin, compareMapMax = lo, hi
	compareA = comparePanel{bgra: rasterToBGRA(ra, ma, lo, hi), w: aw, h: ah, title: "A  " + shortPath(compareAPath, 42)}
	compareB = comparePanel{}
	compareD = comparePanel{}
	if hasB {
		bw, bh := rectWH(r[1])
		bw = minInt(maxInt(bw, 2), 900)
		bh = minInt(maxInt(bh, 2), 900)
		compareB = comparePanel{bgra: rasterToBGRA(rb, mb, lo, hi), w: bw, h: bh, title: "B  " + shortPath(compareBPath, 42)}
		if compareShowDiff && len(r) >= 3 {
			dw, dh := rectWH(r[2])
			dw = minInt(maxInt(dw, 2), 900)
			dh = minInt(maxInt(dh, 2), 900)
			rda, mda, e1 := d.ga.RasterizeTimeSlice(valsA, dw, dh, d.bounds)
			rdb, mdb, e2 := d.gb.RasterizeTimeSlice(valsB, dw, dh, d.bounds)
			if e1 == nil && e2 == nil {
				rd, md := differenceRaster(rda, mda, rdb, mdb)
				dlo, dhi := symmetricResidualRange(rd, md)
				compareD = comparePanel{bgra: rasterToBGRA(rd, md, dlo, dhi), w: dw, h: dh, title: fmt.Sprintf("差  A-B  [%.4g, %.4g]", dlo, dhi)}
			}
		}
	}
	tm := float64(d.sampleA*d.fa.Info.SampleIntervalUS) / 1000.0
	setText(cc.timeEdit, fmt.Sprintf("%.3f", tm))
	pSendMessageW.Call(cc.slider, TBM_SETPOS, 1, uintptr(d.sampleA))
	if hasB {
		extra := ""
		if compareShowDiff {
			extra = " | 差=A-B"
		}
		setText(cc.status, fmt.Sprintf("Time Slice %s | shared display %.5g..%.5g | 增益 %.0f%% | %s%s | %s", formatAdaptiveTimeMS(tm), lo, hi, gainPercent, paletteNames[comparePaletteIndex], extra, d.indexStatus))
	} else {
		setText(cc.status, fmt.Sprintf("A 浏览 | Time Slice %s | display %.5g..%.5g | 增益 %.0f%% | %s | 点击 比 加载 B", formatAdaptiveTimeMS(tm), lo, hi, gainPercent, paletteNames[comparePaletteIndex]))
	}
	invalidateCompareBase()
}

func handlePrepareReady() {
	defer setCompareBusy(-1)
	compareAsyncMu.Lock()
	r := comparePendingPrepare
	comparePendingPrepare = nil
	compareAsyncMu.Unlock()
	if r == nil {
		return
	}
	if r.gen != atomic.LoadInt64(&compareGen) {
		if r.data != nil {
			r.data.close()
		}
		return
	}
	if r.err != nil {
		setCompareProgress(0, false)
		setText(cc.status, "Time Slice: "+r.err.Error())
		message(compareHwnd, "Time Slice", r.err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	if compareTD != nil {
		compareTD.close()
	}
	compareTD = r.data
	if compareMode != 2 {
		resetCompareLineBounds()
	}
	configureCompareModeSlider()
	if compareMode == 2 {
		// Slab I/O is intentionally deferred until Time Slice is visible.
		startLoadTimeSlice(compareTD.sampleA)
	} else {
		renderCompareLine()
	}
	setCompareProgress(100, false)
}

func handleSliceReady() {
	defer setCompareBusy(-1)
	compareAsyncMu.Lock()
	r := comparePendingSlice
	comparePendingSlice = nil
	compareAsyncMu.Unlock()
	if r == nil || compareTD == nil {
		return
	}
	if r.gen != atomic.LoadInt64(&compareSliceGen) {
		return
	}
	if r.err != nil {
		setText(cc.status, "Time Slice: "+r.err.Error())
		return
	}
	compareTD.sampleA, compareTD.sampleB = r.sampleA, r.sampleB
	updateTimeSliceImages(r.valsA, r.valsB)
	hit := "disk"
	if compareTD.fb == nil {
		if r.statA.CacheHit {
			hit = "RAM cache"
		}
	} else if r.statA.CacheHit && r.statB.CacheHit {
		hit = "RAM cache"
	}
	tm := float64(r.sampleA*compareTD.fa.Info.SampleIntervalUS) / 1000
	if compareTD.fb == nil {
		setText(cc.status, fmt.Sprintf("Time Slice %s | %s | A %.0f ms | slab %d samples | 点击 比 加载 B", formatAdaptiveTimeMS(tm), hit, r.statA.Duration.Seconds()*1000, r.statA.BlockSamples))
	} else {
		setText(cc.status, fmt.Sprintf("Time Slice %s | %s | A %.0f ms, B %.0f ms | slab %d/%d samples", formatAdaptiveTimeMS(tm), hit, r.statA.Duration.Seconds()*1000, r.statB.Duration.Seconds()*1000, r.statA.BlockSamples, r.statB.BlockSamples))
	}
	prefetchAround(compareTD, r.sampleA, r.sampleB)
}

func paintComparePanel(hdc uintptr, r RECT, p comparePanel, isTime bool) {
	if len(p.bgra) == 0 || p.w < 1 || p.h < 1 {
		return
	}
	w, h := rectWH(r)
	bmi := BITMAPINFO{Header: BITMAPINFOHEADER{Size: uint32(unsafe.Sizeof(BITMAPINFOHEADER{})), Width: int32(p.w), Height: -int32(p.h), Planes: 1, BitCount: 32, Compression: BI_RGB, SizeImage: uint32(len(p.bgra))}}
	pSetStretchBltMode.Call(hdc, HALFTONE)
	pStretchDIBits.Call(hdc, uintptr(r.Left), uintptr(r.Top), uintptr(w), uintptr(h), 0, 0, uintptr(p.w), uintptr(p.h), uintptr(unsafe.Pointer(&p.bgra[0])), uintptr(unsafe.Pointer(&bmi)), DIB_RGB_COLORS, SRCCOPY)
	pen, _, _ := pCreatePen.Call(PS_SOLID, 1, rgbRef(0, 0, 255))
	old, _, _ := pSelectObject.Call(hdc, pen)
	drawLine(hdc, int(r.Left), int(r.Top), int(r.Right)-1, int(r.Top))
	drawLine(hdc, int(r.Right)-1, int(r.Top), int(r.Right)-1, int(r.Bottom)-1)
	drawLine(hdc, int(r.Right)-1, int(r.Bottom)-1, int(r.Left), int(r.Bottom)-1)
	drawLine(hdc, int(r.Left), int(r.Bottom)-1, int(r.Left), int(r.Top))
	pSelectObject.Call(hdc, old)
	if pen != 0 {
		pDeleteObject.Call(pen)
	}
	drawAxisText(hdc, p.title, int(r.Left), int(r.Top)-25, int(r.Right), int(r.Top)-5, DT_CENTER)
	if isTime && compareTD != nil {
		b := compareTD.bounds
		drawAxisText(hdc, fmt.Sprintf("XL %d", b.CrosslineMin), int(r.Left), int(r.Bottom)+5, int(r.Left)+100, int(r.Bottom)+24, DT_LEFT)
		drawAxisText(hdc, fmt.Sprintf("XL %d", b.CrosslineMax), int(r.Right)-110, int(r.Bottom)+5, int(r.Right), int(r.Bottom)+24, DT_RIGHT)
		drawAxisText(hdc, fmt.Sprintf("IL %d", b.InlineMin), int(r.Left)-50, int(r.Top)-7, int(r.Left)-4, int(r.Top)+14, DT_RIGHT)
		drawAxisText(hdc, fmt.Sprintf("IL %d", b.InlineMax), int(r.Left)-50, int(r.Bottom)-14, int(r.Left)-4, int(r.Bottom)+7, DT_RIGHT)
	} else if compareTD != nil {
		xname := "Crossline"
		if compareMode == 1 {
			xname = "Inline"
		}
		drawAxisText(hdc, fmt.Sprintf("%s %.0f", xname, compareLineXMin), int(r.Left), int(r.Bottom)+5, int(r.Left)+130, int(r.Bottom)+24, DT_LEFT)
		drawAxisText(hdc, fmt.Sprintf("%s %.0f", xname, compareLineXMax), int(r.Right)-140, int(r.Bottom)+5, int(r.Right), int(r.Bottom)+24, DT_RIGHT)
		dt := compareTD.fa.Info.SampleIntervalUS
		if dt <= 0 {
			dt = 1000
		}
		t0 := float64(compareSampleStart*dt) / 1000.0
		t1 := float64(compareSampleEnd*dt) / 1000.0
		drawAxisText(hdc, formatAdaptiveTimeMS(t0), int(r.Left)-64, int(r.Top)-7, int(r.Left)-4, int(r.Top)+14, DT_RIGHT)
		drawAxisText(hdc, formatAdaptiveTimeMS(t1), int(r.Left)-64, int(r.Bottom)-14, int(r.Left)-4, int(r.Bottom)+7, DT_RIGHT)
	}
}

func compareWorldAtPixel(panel, px, py int) (float64, float64, bool) {
	r := compareZoomRectForPanel(panel)
	if !comparePointInRect(px, py, r) {
		return 0, 0, false
	}
	rw, rh := rectWH(r)
	if rw < 2 || rh < 2 {
		return 0, 0, false
	}
	fx := float64(px-int(r.Left)) / float64(rw-1)
	fy := float64(py-int(r.Top)) / float64(rh-1)
	if compareMode != 2 {
		x := compareLineXMin + fx*(compareLineXMax-compareLineXMin)
		y := float64(compareSampleStart) + fy*float64(maxInt(compareSampleEnd-compareSampleStart, 0))
		return x, y, true
	}
	if compareTD == nil {
		return 0, 0, false
	}
	b := compareTD.bounds
	x := float64(b.CrosslineMin) + fx*float64(b.CrosslineMax-b.CrosslineMin)
	y := float64(b.InlineMin) + fy*float64(b.InlineMax-b.InlineMin)
	return x, y, true
}

// nearestCompareLineTrace maps a displayed horizontal geometry coordinate
// back to the physical zero-based SEG-Y trace that supplied that column.  It
// deliberately works on the geometry line rather than the rendered bitmap so
// resampling and window width never change which source trace is selected.
func nearestCompareLineTrace(traces []int64, coords []int32, target float64) (int64, int32, bool) {
	if len(traces) == 0 || len(traces) != len(coords) {
		return 0, 0, false
	}
	best := 0
	bestDistance := math.Abs(float64(coords[0]) - target)
	for index := 1; index < len(coords); index++ {
		distance := math.Abs(float64(coords[index]) - target)
		if distance < bestDistance {
			best, bestDistance = index, distance
		}
	}
	return traces[best], coords[best], true
}

func nearestCompareGeometryValue(values []int32, target float64) (int32, bool) {
	if len(values) == 0 || target < float64(values[0]) || target > float64(values[len(values)-1]) {
		return 0, false
	}
	index := sort.Search(len(values), func(i int) bool { return float64(values[i]) >= target })
	if index <= 0 {
		return values[0], true
	}
	if index >= len(values) {
		return values[len(values)-1], true
	}
	if target-float64(values[index-1]) <= float64(values[index])-target {
		index--
	}
	return values[index], true
}

func commonCompareGeometryValues(a, b []int32) []int32 {
	common := make([]int32, 0, minInt(len(a), len(b)))
	for ia, ib := 0, 0; ia < len(a) && ib < len(b); {
		switch {
		case a[ia] < b[ib]:
			ia++
		case b[ib] < a[ia]:
			ib++
		default:
			common = append(common, a[ia])
			ia++
			ib++
		}
	}
	return common
}

func compareAnalysisTarget(role string, file *segy.File, trace int64, marker int, inline, crossline int32) (traceAnalysisTarget, error) {
	if file == nil {
		return traceAnalysisTarget{}, fmt.Errorf("%s 数据尚未就绪", role)
	}
	if trace < 0 || trace >= file.Info.TraceCount {
		return traceAnalysisTarget{}, fmt.Errorf("%s 道号超出 SEG-Y 范围", role)
	}
	sampleStart, sampleEnd := compareSampleStart, compareSampleEnd
	if sampleStart < 0 {
		sampleStart = 0
	}
	if sampleEnd < 0 || sampleEnd >= file.Info.SamplesPerTrace {
		sampleEnd = file.Info.SamplesPerTrace - 1
	}
	if sampleStart > sampleEnd {
		sampleStart, sampleEnd = 0, file.Info.SamplesPerTrace-1
	}
	marker = clampInt(marker, sampleStart, sampleEnd)
	return traceAnalysisTarget{Role: role, Path: file.Info.Path, Trace: trace, SampleStart: sampleStart, SampleEnd: sampleEnd,
		MarkerSample: marker, Inline: inline, Crossline: crossline, HasGeometry: true}, nil
}

func compareLineSelectionForWorld(panel int, worldX, worldY float64) (traceAnalysisSelection, error) {
	d := compareTD
	if d == nil || d.fa == nil || d.ga == nil {
		return traceAnalysisSelection{}, fmt.Errorf("二维几何尚未就绪")
	}
	marker := int(math.Round(worldY))
	line := compareLineCoord
	lineTargets := func(file *segy.File, geometry *segy.GeometryIndex) ([]int64, []int32, int32, error) {
		if file == nil || geometry == nil {
			return nil, nil, 0, fmt.Errorf("数据几何尚未就绪")
		}
		traces, coords, actual, err := geometry.LineTraceNumbers(compareMode, line)
		if err != nil {
			return nil, nil, 0, err
		}
		traces, coords = filterLineRange(traces, coords, compareLineXMin, compareLineXMax)
		return traces, coords, actual, nil
	}
	makeGeometry := func(actual, across int32) (int32, int32) {
		if compareMode == 0 {
			return actual, across
		}
		return across, actual
	}

	if panel == 2 {
		if !compareShowDiff || !compareActiveB() || d.fb == nil || d.gb == nil {
			return traceAnalysisSelection{}, fmt.Errorf("当前没有可选取的差值剖面")
		}
		tracesA, coordsA, actualA, err := lineTargets(d.fa, d.ga)
		if err != nil {
			return traceAnalysisSelection{}, err
		}
		tracesB, coordsB, actualB, err := lineTargets(d.fb, d.gb)
		if err != nil {
			return traceAnalysisSelection{}, err
		}
		if actualA != actualB {
			return traceAnalysisSelection{}, fmt.Errorf("A/B 当前几何线不一致：%d / %d", actualA, actualB)
		}
		pairedA, pairedB, common := pairLineTracesByCoord(tracesA, coordsA, tracesB, coordsB)
		traceA, across, ok := nearestCompareLineTrace(pairedA, common, worldX)
		if !ok {
			return traceAnalysisSelection{}, fmt.Errorf("A/B 当前范围没有共同几何道")
		}
		traceB, _, ok := nearestCompareLineTrace(pairedB, common, float64(across))
		if !ok {
			return traceAnalysisSelection{}, fmt.Errorf("无法配对 A/B 几何道")
		}
		inline, crossline := makeGeometry(actualA, across)
		targetA, err := compareAnalysisTarget("A", d.fa, traceA, marker, inline, crossline)
		if err != nil {
			return traceAnalysisSelection{}, err
		}
		targetB, err := compareAnalysisTarget("B", d.fb, traceB, marker, inline, crossline)
		if err != nil {
			return traceAnalysisSelection{}, err
		}
		modeName := []string{"Inline", "Crossline"}[compareMode]
		return traceAnalysisSelection{Targets: []traceAnalysisTarget{targetA, targetB}, Difference: true,
			Context: fmt.Sprintf("差 A-B | %s %d | IL %d / XL %d", modeName, actualA, inline, crossline)}, nil
	}

	role, file, geometry := "A", d.fa, d.ga
	if panel == 1 {
		if !compareActiveB() || d.fb == nil || d.gb == nil {
			return traceAnalysisSelection{}, fmt.Errorf("B 数据尚未就绪")
		}
		role, file, geometry = "B", d.fb, d.gb
	} else if panel != 0 {
		return traceAnalysisSelection{}, fmt.Errorf("无效的二维面板")
	}
	traces, coords, actual, err := lineTargets(file, geometry)
	if err != nil {
		return traceAnalysisSelection{}, err
	}
	trace, across, ok := nearestCompareLineTrace(traces, coords, worldX)
	if !ok {
		return traceAnalysisSelection{}, fmt.Errorf("当前剖面没有可选取的 SEG-Y 道")
	}
	inline, crossline := makeGeometry(actual, across)
	target, err := compareAnalysisTarget(role, file, trace, marker, inline, crossline)
	if err != nil {
		return traceAnalysisSelection{}, err
	}
	modeName := []string{"Inline", "Crossline"}[compareMode]
	return traceAnalysisSelection{Targets: []traceAnalysisTarget{target},
		Context: fmt.Sprintf("%s | %s %d | IL %d / XL %d", role, modeName, actual, inline, crossline)}, nil
}

func compareTimeSliceSelectionForWorld(panel int, worldX, worldY float64) (traceAnalysisSelection, error) {
	d := compareTD
	if d == nil || d.fa == nil || d.ga == nil {
		return traceAnalysisSelection{}, fmt.Errorf("Time Slice 几何尚未就绪")
	}
	makeTarget := func(role string, file *segy.File, geometry *segy.GeometryIndex, marker int, inline, crossline int32) (traceAnalysisTarget, error) {
		if file == nil || geometry == nil {
			return traceAnalysisTarget{}, fmt.Errorf("%s 数据尚未就绪", role)
		}
		trace, ok := geometry.TraceAt(inline, crossline)
		if !ok {
			return traceAnalysisTarget{}, fmt.Errorf("%s 在 IL %d / XL %d 没有有效道", role, inline, crossline)
		}
		return compareAnalysisTarget(role, file, trace, marker, inline, crossline)
	}
	if panel == 2 {
		if !compareShowDiff || !compareActiveB() || d.fb == nil || d.gb == nil {
			return traceAnalysisSelection{}, fmt.Errorf("当前没有可选取的差值平面")
		}
		inlineValues := commonCompareGeometryValues(d.ga.InlineValues, d.gb.InlineValues)
		crosslineValues := commonCompareGeometryValues(d.ga.CrosslineValues, d.gb.CrosslineValues)
		inline, okIL := nearestCompareGeometryValue(inlineValues, worldY)
		crossline, okXL := nearestCompareGeometryValue(crosslineValues, worldX)
		if !okIL || !okXL {
			return traceAnalysisSelection{}, fmt.Errorf("A/B 在该位置没有共同几何坐标")
		}
		targetA, err := makeTarget("A", d.fa, d.ga, d.sampleA, inline, crossline)
		if err != nil {
			return traceAnalysisSelection{}, err
		}
		targetB, err := makeTarget("B", d.fb, d.gb, d.sampleB, inline, crossline)
		if err != nil {
			return traceAnalysisSelection{}, err
		}
		return traceAnalysisSelection{Targets: []traceAnalysisTarget{targetA, targetB}, Difference: true,
			Context: fmt.Sprintf("差 A-B | Time Slice | IL %d / XL %d", inline, crossline)}, nil
	}
	if panel == 1 {
		if !compareActiveB() {
			return traceAnalysisSelection{}, fmt.Errorf("B 数据尚未显示")
		}
		inline, okIL := nearestCompareGeometryValue(d.gb.InlineValues, worldY)
		crossline, okXL := nearestCompareGeometryValue(d.gb.CrosslineValues, worldX)
		if !okIL || !okXL {
			return traceAnalysisSelection{}, fmt.Errorf("B 在该位置没有有效几何坐标")
		}
		target, err := makeTarget("B", d.fb, d.gb, d.sampleB, inline, crossline)
		if err != nil {
			return traceAnalysisSelection{}, err
		}
		return traceAnalysisSelection{Targets: []traceAnalysisTarget{target}, Context: fmt.Sprintf("B | Time Slice | IL %d / XL %d", inline, crossline)}, nil
	}
	if panel != 0 {
		return traceAnalysisSelection{}, fmt.Errorf("无效的二维面板")
	}
	inline, okIL := nearestCompareGeometryValue(d.ga.InlineValues, worldY)
	crossline, okXL := nearestCompareGeometryValue(d.ga.CrosslineValues, worldX)
	if !okIL || !okXL {
		return traceAnalysisSelection{}, fmt.Errorf("A 在该位置没有有效几何坐标")
	}
	target, err := makeTarget("A", d.fa, d.ga, d.sampleA, inline, crossline)
	if err != nil {
		return traceAnalysisSelection{}, err
	}
	return traceAnalysisSelection{Targets: []traceAnalysisTarget{target}, Context: fmt.Sprintf("A | Time Slice | IL %d / XL %d", inline, crossline)}, nil
}

// compareTraceSelectionForWorld is the calculation-only core used by both the
// mouse integration and regression tests. It never opens a Reader or touches
// an HWND.
func compareTraceSelectionForWorld(panel int, worldX, worldY float64) (traceAnalysisSelection, error) {
	if compareMode == 0 || compareMode == 1 {
		return compareLineSelectionForWorld(panel, worldX, worldY)
	}
	if compareMode == 2 {
		return compareTimeSliceSelectionForWorld(panel, worldX, worldY)
	}
	return traceAnalysisSelection{}, fmt.Errorf("不支持的二维显示方向")
}

func compareTraceSelectionAt(panel, x, y int) (traceAnalysisSelection, error) {
	// A true 2-D line may not have usable Inline/Crossline headers. In that
	// case the visible section is still a faithful original-trace-order image;
	// map the click through the accepted load range instead of requiring a 3-D
	// GeometryIndex merely to inspect one physical SEG-Y trace.
	if compareTD == nil && compareMode != 2 && panel == 0 && sf != nil && compareAPath != "" {
		r := compareZoomRectForPanel(panel)
		if !comparePointInRect(x, y, r) {
			return traceAnalysisSelection{}, fmt.Errorf("请在地震图像范围内选择一道")
		}
		rw, rh := rectWH(r)
		step := compareTraceStep
		if step < 1 {
			step = 1
		}
		count := (compareTraceEnd-compareTraceStart)/step + 1
		if count < 1 {
			return traceAnalysisSelection{}, fmt.Errorf("当前二维剖面没有有效道范围")
		}
		column := int64(math.Round(float64(x-int(r.Left)) * float64(count-1) / float64(maxInt(rw-1, 1))))
		trace := compareTraceStart + column*step
		if trace < 0 {
			trace = 0
		}
		if trace >= sf.Info.TraceCount {
			trace = sf.Info.TraceCount - 1
		}
		sample := compareSampleStart + int(math.Round(float64(y-int(r.Top))*float64(maxInt(compareSampleEnd-compareSampleStart, 0))/float64(maxInt(rh-1, 1))))
		sample = clampInt(sample, compareSampleStart, compareSampleEnd)
		target, err := compareAnalysisTarget("A", sf, trace, sample, 0, 0)
		if err != nil {
			return traceAnalysisSelection{}, err
		}
		target.HasGeometry = false
		return traceAnalysisSelection{Targets: []traceAnalysisTarget{target},
			Context: fmt.Sprintf("A | 二维原始道序 | Trace %d | Time %s", trace+1,
				formatAdaptiveTimeMS(float64(sample*sf.Info.SampleIntervalUS)/1000))}, nil
	}
	worldX, worldY, ok := compareWorldAtPixel(panel, x, y)
	if !ok {
		return traceAnalysisSelection{}, fmt.Errorf("请在地震图像范围内选择一道")
	}
	return compareTraceSelectionForWorld(panel, worldX, worldY)
}

func comparePixelAtWorld(panel int, x, y float64) (int, int, bool) {
	r := compareZoomRectForPanel(panel)
	rw, rh := rectWH(r)
	if rw < 2 || rh < 2 {
		return 0, 0, false
	}
	var fx, fy float64
	if compareMode != 2 {
		denx := compareLineXMax - compareLineXMin
		deny := float64(compareSampleEnd - compareSampleStart)
		if denx <= 0 || deny <= 0 {
			return 0, 0, false
		}
		fx = (x - compareLineXMin) / denx
		fy = (y - float64(compareSampleStart)) / deny
	} else {
		if compareTD == nil {
			return 0, 0, false
		}
		b := compareTD.bounds
		denx := float64(b.CrosslineMax - b.CrosslineMin)
		deny := float64(b.InlineMax - b.InlineMin)
		if denx <= 0 || deny <= 0 {
			return 0, 0, false
		}
		fx = (x - float64(b.CrosslineMin)) / denx
		fy = (y - float64(b.InlineMin)) / deny
	}
	if fx < 0 || fx > 1 || fy < 0 || fy > 1 {
		return 0, 0, false
	}
	px := int(r.Left) + int(math.Round(fx*float64(rw-1)))
	py := int(r.Top) + int(math.Round(fy*float64(rh-1)))
	return px, py, true
}

func setCompareTool(tool int) {
	compareTool = tool
	for i, h := range []uintptr{cc.rectTool, cc.ellipseTool, cc.lineTool} {
		state := uintptr(0)
		if tool == i+1 {
			state = BST_CHECKED
		}
		if h != 0 {
			pSendMessageW.Call(h, BM_SETCHECK, state, 0)
		}
	}
	if tool == 0 {
		setText(cc.status, "导航模式：拖动框选 = 当前视图同步 Zoom；加载 B 后 A/B/差联动。")
	} else {
		setText(cc.status, "标注模式：在任一窗口拖动；加载 B 后标注按数据坐标同步到 A/B/差。")
	}
}

func setCompareTraceInspectMode(enabled bool) {
	compareTraceInspectMode = enabled
	if cc.traceInspect != 0 {
		state := uintptr(0)
		if enabled {
			state = BST_CHECKED
		}
		pSendMessageW.Call(cc.traceInspect, BM_SETCHECK, state, 0)
	}
	if !enabled {
		return
	}
	if compareZoomDragging || compareAnnotDragging {
		compareZoomDragging, compareAnnotDragging = false, false
		pReleaseCapture.Call()
		invalidateCompareOverlayAll()
	}
	compareTool = 0
	for _, control := range []uintptr{cc.zoom, cc.rectTool, cc.ellipseTool, cc.lineTool} {
		if control != 0 {
			pSendMessageW.Call(control, BM_SETCHECK, 0, 0)
		}
	}
	compareCrosshairValid = false
	setText(cc.status, "单道分析：单击 A、B 或差值图选择一道；模式保持开启，Esc 退出。")
	if compareHwnd != 0 {
		pSetFocus.Call(compareHwnd)
	}
}

func showCompareTraceSelectionAt(panel, x, y int) {
	selection, err := compareTraceSelectionAt(panel, x, y)
	if err != nil {
		setText(cc.status, "单道分析："+err.Error())
		return
	}
	showTraceAnalysisSelection(selection)
}

func addCompareAnnotation(kind, panel, x0, y0, x1, y1 int) {
	wx0, wy0, ok0 := compareWorldAtPixel(panel, x0, y0)
	wx1, wy1, ok1 := compareWorldAtPixel(panel, x1, y1)
	if !ok0 || !ok1 {
		return
	}
	if math.Abs(float64(x1-x0)) < 3 && math.Abs(float64(y1-y0)) < 3 {
		return
	}
	compareAnnotations = append(compareAnnotations, compareAnnotation{Mode: compareMode, Kind: kind, X0: wx0, Y0: wy0, X1: wx1, Y1: wy1})
	invalidateCompareBase()
}

func drawCompareShape(hdc uintptr, kind, x0, y0, x1, y1 int, color uintptr) {
	if x1 < x0 && kind != 2 {
		x0, x1 = x1, x0
	}
	if y1 < y0 && kind != 2 {
		y0, y1 = y1, y0
	}
	pen, _, _ := pCreatePen.Call(PS_SOLID, 2, uintptr(color))
	old, _, _ := pSelectObject.Call(hdc, pen)
	if kind == 0 {
		drawLine(hdc, x0, y0, x1, y0)
		drawLine(hdc, x1, y0, x1, y1)
		drawLine(hdc, x1, y1, x0, y1)
		drawLine(hdc, x0, y1, x0, y0)
	} else if kind == 1 {
		hollow, _, _ := pGetStockObject.Call(HOLLOW_BRUSH)
		oldBrush, _, _ := pSelectObject.Call(hdc, hollow)
		pEllipse.Call(hdc, uintptr(x0), uintptr(y0), uintptr(x1), uintptr(y1))
		pSelectObject.Call(hdc, oldBrush)
	} else {
		drawLine(hdc, x0, y0, x1, y1)
	}
	pSelectObject.Call(hdc, old)
	if pen != 0 {
		pDeleteObject.Call(pen)
	}
}

func paintCompareCommittedAnnotations(hdc uintptr) {
	colors := []uintptr{rgbRef(255, 0, 0), rgbRef(0, 180, 0), rgbRef(0, 180, 220)}
	for _, a := range compareAnnotations {
		if a.Mode != compareMode {
			continue
		}
		for panel := 0; panel < comparePanelCount(); panel++ {
			x0, y0, o0 := comparePixelAtWorld(panel, a.X0, a.Y0)
			x1, y1, o1 := comparePixelAtWorld(panel, a.X1, a.Y1)
			if o0 && o1 {
				drawCompareShape(hdc, a.Kind, x0, y0, x1, y1, colors[a.Kind%len(colors)])
			}
		}
	}
}

func paintCompareTransientOverlays(hdc uintptr) {
	colors := []uintptr{rgbRef(255, 0, 0), rgbRef(0, 180, 0), rgbRef(0, 180, 220)}
	if compareZoomDragging {
		x0, x1 := compareZoomX0, compareZoomX1
		y0, y1 := compareZoomY0, compareZoomY1
		if x1 < x0 {
			x0, x1 = x1, x0
		}
		if y1 < y0 {
			y0, y1 = y1, y0
		}
		pen, _, _ := pCreatePen.Call(PS_SOLID, 1, rgbRef(255, 0, 0))
		oldPen, _, _ := pSelectObject.Call(hdc, pen)
		drawLine(hdc, x0, y0, x1, y0)
		drawLine(hdc, x1, y0, x1, y1)
		drawLine(hdc, x1, y1, x0, y1)
		drawLine(hdc, x0, y1, x0, y0)
		pSelectObject.Call(hdc, oldPen)
		if pen != 0 {
			pDeleteObject.Call(pen)
		}
	}
	if compareAnnotDragging {
		kind := compareTool - 1
		if kind >= 0 {
			drawCompareShape(hdc, kind, compareAnnotX0, compareAnnotY0, compareAnnotX1, compareAnnotY1, colors[kind%len(colors)])
		}
	}
	if compareCrosshairOn && compareCrosshairValid {
		pen, _, _ := pCreatePen.Call(PS_SOLID, 1, rgbRef(255, 210, 0))
		old, _, _ := pSelectObject.Call(hdc, pen)
		for panel := 0; panel < comparePanelCount(); panel++ {
			if x, y, ok := comparePixelAtWorld(panel, compareCrosshairWorldX, compareCrosshairWorldY); ok {
				r := compareZoomRectForPanel(panel)
				drawLine(hdc, x, int(r.Top), x, int(r.Bottom)-1)
				drawLine(hdc, int(r.Left), y, int(r.Right)-1, y)
			}
		}
		pSelectObject.Call(hdc, old)
		if pen != 0 {
			pDeleteObject.Call(pen)
		}
	}
}

func updateCompareCrosshair(mx, my int) {
	if !compareCrosshairOn {
		compareCrosshairValid = false
		return
	}
	panel := -1
	for i, r := range comparePanelRectsAll() {
		if comparePointInRect(mx, my, r) {
			panel = i
			break
		}
	}
	if panel < 0 {
		compareCrosshairValid = false
		return
	}
	wx, wy, ok := compareWorldAtPixel(panel, mx, my)
	if !ok {
		compareCrosshairValid = false
		return
	}
	compareCrosshairWorldX, compareCrosshairWorldY = wx, wy
	compareCrosshairValid = true

	// Status text is useful but does not need mouse-event frequency. Updating a
	// child STATIC on every WM_MOUSEMOVE can itself cause visible churn, so cap
	// it to ~25 Hz while leaving the crosshair motion unthrottled.
	now := time.Now()
	if !compareLastHoverStatus.IsZero() && now.Sub(compareLastHoverStatus) < 40*time.Millisecond {
		return
	}
	compareLastHoverStatus = now
	if compareMode != 2 {
		tm := 0.0
		if compareDtAUS > 0 {
			tm = wy * float64(compareDtAUS) / 1000.0
		}
		if compareMode == 0 {
			setText(cc.status, fmt.Sprintf("十字联动 | Inline %d | Crossline %.0f | Time %s", compareLineCoord, wx, formatAdaptiveTimeMS(tm)))
		} else {
			setText(cc.status, fmt.Sprintf("十字联动 | Crossline %d | Inline %.0f | Time %s", compareLineCoord, wx, formatAdaptiveTimeMS(tm)))
		}
	} else {
		tm := 0.0
		if compareTD != nil && compareTD.fa != nil {
			tm = float64(compareTD.sampleA*compareTD.fa.Info.SampleIntervalUS) / 1000.0
		}
		setText(cc.status, fmt.Sprintf("十字联动 | Inline %.0f | Crossline %.0f | Time %s", wy, wx, formatAdaptiveTimeMS(tm)))
	}
}

func destroyCompareBaseCache() {
	if compareBaseDC != 0 && compareBaseOldBmp != 0 {
		pSelectObject.Call(compareBaseDC, compareBaseOldBmp)
	}
	if compareBaseBmp != 0 {
		pDeleteObject.Call(compareBaseBmp)
	}
	if compareBaseDC != 0 {
		pDeleteDC.Call(compareBaseDC)
	}
	compareBaseDC, compareBaseBmp, compareBaseOldBmp = 0, 0, 0
	compareBaseW, compareBaseH = 0, 0
	compareBaseDirty = true
}

func drawCompareStaticScene(hdc uintptr) {
	r := clientRect(compareHwnd)
	if compareAPath == "" {
		bg, _, _ := pCreateSolidBrush.Call(rgbRef(248, 250, 252))
		pFillRect.Call(hdc, uintptr(unsafe.Pointer(&r)), bg)
		pDeleteObject.Call(bg)
	} else {
		white, _, _ := pGetStockObject.Call(WHITE_BRUSH)
		pFillRect.Call(hdc, uintptr(unsafe.Pointer(&r)), white)
	}
	if hFont != 0 {
		pSelectObject.Call(hdc, hFont)
	}
	pSetBkMode.Call(hdc, TRANSPARENT)
	pSetTextColor.Call(hdc, rgbRef(0, 0, 0))
	if compareAPath == "" {
		drawStartCenter(hdc)
		return
	}
	panels := comparePanelRectsAll()
	if len(panels) >= 1 {
		paintComparePanel(hdc, panels[0], compareA, compareMode == 2)
	}
	if compareActiveB() && len(panels) >= 2 {
		paintComparePanel(hdc, panels[1], compareB, compareMode == 2)
	}
	if compareShowDiff && len(panels) >= 3 {
		paintComparePanel(hdc, panels[2], compareD, compareMode == 2)
	}
	paintCompareCommittedAnnotations(hdc)
	if compareMode == 2 && compareTD != nil && compareActiveB() && len(panels) >= 2 {
		right := panels[len(panels)-1].Right
		drawAxisText(hdc, fmt.Sprintf("A/B共享幅度范围 %.5g .. %.5g；差值独立零对称范围", compareMapMin, compareMapMax), int(panels[0].Left), 94, int(right), 114, DT_CENTER)
	}
}

func ensureCompareBaseCache(hdc uintptr) bool {
	r := clientRect(compareHwnd)
	w := int(r.Right - r.Left)
	h := int(r.Bottom - r.Top)
	if w <= 0 || h <= 0 {
		return false
	}
	if compareBaseDC == 0 || compareBaseBmp == 0 || compareBaseW != w || compareBaseH != h {
		destroyCompareBaseCache()
		mem, _, _ := pCreateCompatibleDC.Call(hdc)
		bmp, _, _ := pCreateCompatibleBitmap.Call(hdc, uintptr(w), uintptr(h))
		if mem == 0 || bmp == 0 {
			if bmp != 0 {
				pDeleteObject.Call(bmp)
			}
			if mem != 0 {
				pDeleteDC.Call(mem)
			}
			return false
		}
		old, _, _ := pSelectObject.Call(mem, bmp)
		compareBaseDC, compareBaseBmp, compareBaseOldBmp = mem, bmp, old
		compareBaseW, compareBaseH = w, h
		compareBaseDirty = true
	}
	if compareBaseDirty {
		drawCompareStaticScene(compareBaseDC)
		compareBaseDirty = false
	}
	return true
}

func invalidateCompareBase() {
	compareBaseDirty = true
	if compareHwnd != 0 {
		pInvalidateRect.Call(compareHwnd, 0, 0)
	}
}

func invalidateCompareOverlayAll() {
	if compareHwnd != 0 {
		pInvalidateRect.Call(compareHwnd, 0, 0)
	}
}

func invalidateCompareCrosshairAt(x, y float64) {
	if compareHwnd == 0 {
		return
	}
	const pad = 2
	for panel := 0; panel < comparePanelCount(); panel++ {
		px, py, ok := comparePixelAtWorld(panel, x, y)
		if !ok {
			continue
		}
		r := compareZoomRectForPanel(panel)
		vr := RECT{Left: int32(px - pad), Top: r.Top, Right: int32(px + pad + 1), Bottom: r.Bottom}
		hr := RECT{Left: r.Left, Top: int32(py - pad), Right: r.Right, Bottom: int32(py + pad + 1)}
		pInvalidateRect.Call(compareHwnd, uintptr(unsafe.Pointer(&vr)), 0)
		pInvalidateRect.Call(compareHwnd, uintptr(unsafe.Pointer(&hr)), 0)
	}
}

func invalidateCompareCrosshairTransition(oldValid bool, oldX, oldY float64, newValid bool, newX, newY float64) {
	if oldValid {
		invalidateCompareCrosshairAt(oldX, oldY)
	}
	if newValid {
		invalidateCompareCrosshairAt(newX, newY)
	}
}

func paintCompare() {
	var ps PAINTSTRUCT
	hdc, _, _ := pBeginPaint.Call(compareHwnd, uintptr(unsafe.Pointer(&ps)))
	if hdc != 0 {
		if ensureCompareBaseCache(hdc) {
			pr := ps.RcPaint
			w := int(pr.Right - pr.Left)
			h := int(pr.Bottom - pr.Top)
			if w > 0 && h > 0 {
				// BeginPaint's DC retains the exact update-region clip, so even when
				// RcPaint is a bounding box of several crosshair strips only those
				// strips are actually copied. No seismic raster is regenerated here.
				pBitBlt.Call(hdc, uintptr(pr.Left), uintptr(pr.Top), uintptr(w), uintptr(h), compareBaseDC, uintptr(pr.Left), uintptr(pr.Top), SRCCOPY)
				paintCompareTransientOverlays(hdc)
			}
		} else {
			drawCompareStaticScene(hdc)
			paintCompareTransientOverlays(hdc)
		}
	}
	pEndPaint.Call(compareHwnd, uintptr(unsafe.Pointer(&ps)))
}

func comparePointInRect(x, y int, r RECT) bool {
	return x >= int(r.Left) && x < int(r.Right) && y >= int(r.Top) && y < int(r.Bottom)
}

func compareZoomRectForPanel(panel int) RECT {
	return comparePanelRect(panel)
}

func currentCompareViewSnapshot() (compareViewSnapshot, bool) {
	s := compareViewSnapshot{mode: compareMode, lineXMin: compareLineXMin, lineXMax: compareLineXMax, sampleStart: compareSampleStart, sampleEnd: compareSampleEnd}
	if compareMode == 2 {
		if compareTD == nil {
			return s, false
		}
		s.bounds = compareTD.bounds
	}
	return s, true
}

func applyCompareViewSnapshot(s compareViewSnapshot) bool {
	if s.mode != compareMode {
		return false
	}
	compareCrosshairValid = false
	if compareMode == 2 {
		if compareTD == nil {
			return false
		}
		compareTD.bounds = s.bounds
		if len(compareTD.currentValsA) > 0 {
			updateTimeSliceImages(compareTD.currentValsA, compareTD.currentValsB)
			return true
		}
		return false
	}
	compareLineXMin, compareLineXMax = s.lineXMin, s.lineXMax
	compareSampleStart, compareSampleEnd = s.sampleStart, s.sampleEnd
	renderCompareLine()
	return true
}

func toggleCompareZoomSnapshot() {
	if !compareZoomToggleValid || compareZoomBefore.mode != compareMode || compareZoomAfter.mode != compareMode {
		return
	}
	if compareZoomDragging {
		compareZoomDragging = false
		pReleaseCapture.Call()
	}
	if compareZoomAtAfter {
		if applyCompareViewSnapshot(compareZoomBefore) {
			compareZoomAtAfter = false
			setText(cc.status, "双击：已取消上一次放大；再次双击可恢复相同区域。")
		}
	} else {
		if applyCompareViewSnapshot(compareZoomAfter) {
			compareZoomAtAfter = true
			setText(cc.status, "双击：已恢复上一次放大区域。")
		}
	}
}

func applyCompareZoom(panel int, x0, y0, x1, y1 int) {
	before, haveBefore := currentCompareViewSnapshot()
	r := compareZoomRectForPanel(panel)
	if x1 < x0 {
		x0, x1 = x1, x0
	}
	if y1 < y0 {
		y0, y1 = y1, y0
	}
	if x1-x0 < 4 || y1-y0 < 4 {
		return
	}
	x0 = clampInt(x0, int(r.Left), int(r.Right)-1)
	x1 = clampInt(x1, int(r.Left), int(r.Right)-1)
	y0 = clampInt(y0, int(r.Top), int(r.Bottom)-1)
	y1 = clampInt(y1, int(r.Top), int(r.Bottom)-1)
	rw, rh := rectWH(r)
	if rw < 2 || rh < 2 {
		return
	}
	fx0 := float64(x0-int(r.Left)) / float64(rw-1)
	fx1 := float64(x1-int(r.Left)) / float64(rw-1)
	fy0 := float64(y0-int(r.Top)) / float64(rh-1)
	fy1 := float64(y1-int(r.Top)) / float64(rh-1)
	if compareMode != 2 {
		oldX0, oldX1 := compareLineXMin, compareLineXMax
		oldS0, oldS1 := compareSampleStart, compareSampleEnd
		compareLineXMin = oldX0 + fx0*(oldX1-oldX0)
		compareLineXMax = oldX0 + fx1*(oldX1-oldX0)
		compareSampleStart = oldS0 + int(math.Floor(fy0*float64(maxInt(oldS1-oldS0, 0))))
		compareSampleEnd = oldS0 + int(math.Ceil(fy1*float64(maxInt(oldS1-oldS0, 0))))
		if compareLineXMax <= compareLineXMin || compareSampleEnd <= compareSampleStart {
			compareLineXMin, compareLineXMax = oldX0, oldX1
			compareSampleStart, compareSampleEnd = oldS0, oldS1
			return
		}
		renderCompareLine()
		if haveBefore {
			if after, ok := currentCompareViewSnapshot(); ok {
				compareZoomBefore, compareZoomAfter = before, after
				compareZoomToggleValid, compareZoomAtAfter = true, true
			}
		}
		return
	}
	if compareTD == nil {
		return
	}
	b := compareTD.bounds
	xl0 := float64(b.CrosslineMin) + fx0*float64(b.CrosslineMax-b.CrosslineMin)
	xl1 := float64(b.CrosslineMin) + fx1*float64(b.CrosslineMax-b.CrosslineMin)
	il0 := float64(b.InlineMin) + fy0*float64(b.InlineMax-b.InlineMin)
	il1 := float64(b.InlineMin) + fy1*float64(b.InlineMax-b.InlineMin)
	nb := segy.SliceBounds{InlineMin: int32(math.Round(il0)), InlineMax: int32(math.Round(il1)), CrosslineMin: int32(math.Round(xl0)), CrosslineMax: int32(math.Round(xl1))}
	if nb.InlineMax <= nb.InlineMin || nb.CrosslineMax <= nb.CrosslineMin {
		return
	}
	compareTD.bounds = nb
	if len(compareTD.currentValsA) > 0 {
		updateTimeSliceImages(compareTD.currentValsA, compareTD.currentValsB)
	} else if va, ok := compareTD.ca.GetSliceCached(compareTD.sampleA); ok {
		if compareTD.cb == nil {
			updateTimeSliceImages(va, nil)
		} else if vb, ok2 := compareTD.cb.GetSliceCached(compareTD.sampleB); ok2 {
			updateTimeSliceImages(va, vb)
		}
	}
	if haveBefore {
		if after, ok := currentCompareViewSnapshot(); ok {
			compareZoomBefore, compareZoomAfter = before, after
			compareZoomToggleValid, compareZoomAtAfter = true, true
		}
	}
}

func pSendMessageWCheck(h uintptr) bool {
	r, _, _ := pSendMessageW.Call(h, BM_GETCHECK, 0, 0)
	return r == BST_CHECKED
}

func compareWndProc(h uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case WM_DROPFILES:
		handleWorkspaceDrop(wParam, 0)
		return 0
	case WM_COMMAND:
		id := int(wParam & 0xffff)
		switch id {
		case ID_OPEN:
			showLoadDialog(compareAPath)
		case ID_SAVE:
			if len(compareA.bgra) == 0 {
				message(compareHwnd, "保存", "当前没有可保存的 A 图像。", MB_OK|MB_ICONINFORMATION)
				break
			}
			if p := saveDialog(compareHwnd); p != "" {
				if err := saveComparePanelBMP(p, compareA); err != nil {
					message(compareHwnd, "Save error", err.Error(), MB_OK|MB_ICONERROR)
				}
			}
		case ID_FIXED:
			if isChecked(cc.fixed) {
				setText(cc.status, "Fixed：调整窗口时优先缩放缓存图像。")
			} else {
				setText(cc.status, "Fit：调整窗口后按当前视图重新绘制。")
			}
		case ID_MARK:
			paletteIndex = comparePaletteIndex
			if comboColor != 0 {
				pSendMessageW.Call(comboColor, CB_SETCURSEL, uintptr(paletteIndex), 0)
			}
			showMarkWindow()
		case ID_DATA:
			if compareTD != nil && compareTD.fa != nil {
				message(compareHwnd, "数据信息 A", compareTD.fa.Info.String()+fmt.Sprintf("\n\nIL byte: %d\nXL byte: %d", compareTD.ga.InlineByte, compareTD.ga.CrosslineByte), MB_OK|MB_ICONINFORMATION)
			} else if sf != nil {
				message(compareHwnd, "数据信息 A", sf.Info.String(), MB_OK|MB_ICONINFORMATION)
			}
		case ID_PARAMS:
			showParamDialog()
		case ID_ZOOM:
			setCompareTraceInspectMode(false)
			setCompareTool(0)
			setText(cc.status, "Zoom：在当前图像拖动矩形，A/B/差按数据坐标同步。")
		case ID_ORIGIN:
			pSendMessageW.Call(compareHwnd, WM_COMMAND, IDCMP_REFRESH, 0)
		case ID_MAIL:
			openFeedbackEmail(compareHwnd)
		case ID_UPDATE:
			message(compareHwnd, "检查更新", APP_NAME+" 当前为本地 x64 版本。\n\n项目主页："+APP_PROJECT_URL, MB_OK|MB_ICONINFORMATION)
		case ID_ABOUT:
			message(compareHwnd, "About "+APP_NAME, APP_NAME+" v"+APP_VERSION+"\nSeismic Visualization, Reconstruction & Enhancement\n\nA-only: Inline / Crossline / Time Slice\nA+B: linked comparison / residual / spectrum\n\nProject: "+APP_PROJECT_URL+"\n\n"+licenseSummary(), MB_OK|MB_ICONINFORMATION)
		case ID_COMPARE:
			if compareAPath == "" {
				pSendMessageW.Call(cc.compare, BM_SETCHECK, 0, 0)
				message(compareHwnd, "比", "请先打开 A 数据。", MB_OK|MB_ICONINFORMATION)
				break
			}
			wantOn := pSendMessageWCheck(cc.compare)
			if !compareHasB() {
				if !wantOn {
					break
				}
				if !chooseComparePath(1) {
					pSendMessageW.Call(cc.compare, BM_SETCHECK, 0, 0)
				}
				break
			}
			compareBVisible = wantOn
			compareCrosshairValid = false
			compareZoomToggleValid = false
			if !compareBVisible {
				compareShowDiff = false
				pSendMessageW.Call(cc.diff, BM_SETCHECK, 0, 0)
				pEnableWindow.Call(cc.diff, 0)
				setText(cc.status, "已退出对比，B 保留在缓存中；再次点击 比 可立即恢复。")
			} else {
				pEnableWindow.Call(cc.diff, 1)
				setText(cc.status, "已恢复 A/B 对比（B 使用缓存数据）。")
			}
			layoutComparePathLabels()
			destroyCompareBaseCache()
			if compareTD != nil {
				if compareMode == 2 && len(compareTD.currentValsA) > 0 {
					updateTimeSliceImages(compareTD.currentValsA, compareTD.currentValsB)
				} else if compareMode != 2 {
					renderCompareLine()
				}
			} else {
				invalidateCompareBase()
			}
		case ID_VOLUME:
			showVolumeWindow()
		case IDCMP_EXPORT:
			startExportCurrentSection()
		case IDCMP_SPECTRUM:
			showSpectrumWindow()
		case IDCMP_TRACE_INSPECT:
			setCompareTraceInspectMode(pSendMessageWCheck(cc.traceInspect))
		case IDCMP_A:
			chooseComparePath(0)
		case IDCMP_B:
			chooseComparePath(1)
		case IDCMP_MODE:
			if (wParam>>16)&0xffff == 1 {
				r, _, _ := pSendMessageW.Call(cc.mode, CB_GETCURSEL, 0, 0)
				compareMode = int(r)
				compareZoomToggleValid = false
				showTimeControls(true)
				setText(cc.refresh, "还原")
				compareCrosshairValid = false
				if compareTD == nil {
					if compareAutoGeom {
						startPrepareTimeCompare()
					} else if compareAPath != "" {
						startAutoDetectGeometry()
					}
				} else {
					if compareMode != 2 {
						resetCompareLineBounds()
					}
					configureCompareModeSlider()
					if compareMode == 2 {
						shown := false
						if va, ok := compareTD.ca.GetSliceCached(compareTD.sampleA); ok {
							if compareTD.cb == nil {
								updateTimeSliceImages(va, nil)
								shown = true
							} else if vb, ok2 := compareTD.cb.GetSliceCached(compareTD.sampleB); ok2 {
								updateTimeSliceImages(va, vb)
								shown = true
							}
						}
						if !shown {
							startLoadTimeSlice(compareTD.sampleA)
						}
					} else {
						renderCompareLine()
					}
				}
				invalidateCompareBase()
			}
		case IDCMP_PREV:
			if compareTD != nil {
				if compareMode == 2 {
					startLoadTimeSlice(compareTD.sampleA - 1)
				} else {
					startLoadComparePosition(int(compareLineCoord) - 1)
				}
			}
		case IDCMP_NEXT:
			if compareTD != nil {
				if compareMode == 2 {
					startLoadTimeSlice(compareTD.sampleA + 1)
				} else {
					startLoadComparePosition(int(compareLineCoord) + 1)
				}
			}
		case IDCMP_REFRESH:
			compareZoomToggleValid = false
			if compareTD == nil {
				if compareAutoGeom {
					startPrepareTimeCompare()
				} else if compareAPath != "" {
					startAutoDetectGeometry()
				}
				break
			}
			ilA, xlA, ilB, xlB, okb := compareGeometryBytesForFiles()
			if !okb {
				return 0
			}
			if ilA != compareTD.ga.InlineByte || xlA != compareTD.ga.CrosslineByte || (compareTD.gb != nil && (ilB != compareTD.gb.InlineByte || xlB != compareTD.gb.CrosslineByte)) {
				invalidateCompareTimeData()
				startPrepareTimeCompare()
				return 0
			}
			if compareMode == 2 {
				compareTD.bounds = compareTD.fullBounds
				if va, ok := compareTD.ca.GetSliceCached(compareTD.sampleA); ok {
					if compareTD.cb == nil {
						updateTimeSliceImages(va, nil)
					} else if vb, ok2 := compareTD.cb.GetSliceCached(compareTD.sampleB); ok2 {
						updateTimeSliceImages(va, vb)
					}
				}
			} else {
				compareSampleStart, compareSampleEnd = compareOriginSampleStart, compareOriginSampleEnd
				resetCompareLineBounds()
				configureCompareModeSlider()
				renderCompareLine()
			}
		case IDCMP_AUTOGEOM:
			startAutoDetectGeometry()
		case IDCMP_SWAPGEOM:
			swapCompareGeometryBytes()
		case IDCMP_PALETTE:
			notify := (wParam >> 16) & 0xffff
			if notify == 1 || notify == 9 { // CBN_SELCHANGE / CBN_SELENDOK
				r, _, _ := pSendMessageW.Call(cc.palette, CB_GETCURSEL, 0, 0)
				if int(r) >= 0 && int(r) < len(paletteNames) {
					comparePaletteIndex = int(r)
					paletteIndex = comparePaletteIndex
					if comboColor != 0 {
						pSendMessageW.Call(comboColor, CB_SETCURSEL, uintptr(paletteIndex), 0)
					}
					refreshComparePaletteOnly()
					setText(cc.status, "对比色标："+paletteNames[comparePaletteIndex]+"（A/B/差 联动）")
				}
			}
		case IDCMP_CROSSHAIR:
			oldValid, oldX, oldY := compareCrosshairValid, compareCrosshairWorldX, compareCrosshairWorldY
			compareCrosshairOn = pSendMessageWCheck(cc.crosshair)
			if !compareCrosshairOn {
				compareCrosshairValid = false
			}
			invalidateCompareCrosshairTransition(oldValid, oldX, oldY, compareCrosshairValid, compareCrosshairWorldX, compareCrosshairWorldY)
		case IDCMP_RECTTOOL:
			setCompareTraceInspectMode(false)
			if pSendMessageWCheck(cc.rectTool) {
				setCompareTool(1)
			} else {
				setCompareTool(0)
			}
		case IDCMP_ELLIPSETOOL:
			setCompareTraceInspectMode(false)
			if pSendMessageWCheck(cc.ellipseTool) {
				setCompareTool(2)
			} else {
				setCompareTool(0)
			}
		case IDCMP_LINETOOL:
			setCompareTraceInspectMode(false)
			if pSendMessageWCheck(cc.lineTool) {
				setCompareTool(3)
			} else {
				setCompareTool(0)
			}
		case IDCMP_CLEARANN:
			compareAnnotations = nil
			invalidateCompareBase()
			setText(cc.status, "已清除全部对比标注。")
		case IDCMP_DIFF:
			if !compareActiveB() {
				pSendMessageW.Call(cc.diff, BM_SETCHECK, 0, 0)
				message(compareHwnd, "差", "请先点击 比 加载 B 数据。", MB_OK|MB_ICONINFORMATION)
				break
			}
			compareShowDiff = pSendMessageWCheck(cc.diff)
			compareCrosshairValid = false
			destroyCompareBaseCache()
			if compareTD != nil {
				if compareMode == 2 && len(compareTD.currentValsA) > 0 && len(compareTD.currentValsB) > 0 {
					updateTimeSliceImages(compareTD.currentValsA, compareTD.currentValsB)
				} else if compareMode != 2 {
					renderCompareLine()
				}
			} else {
				invalidateCompareBase()
			}
		case IDCMP_CLOSE:
			pDestroyWindow.Call(h)
		}
		return 0
	case WM_KEYDOWN:
		if wParam == VK_ESCAPE && compareTraceInspectMode {
			setCompareTraceInspectMode(false)
			setText(cc.status, "已退出单道分析选取；恢复拖动缩放。")
			return 0
		}
	case WM_LBUTTONDOWN:
		if compareAPath == "" {
			mx, my := mousePoint(lParam)
			if beginStartHomePress(mx, my) {
				pSetCapture.Call(h)
				return 0
			}
		}
		if len(compareA.bgra) > 0 {
			mx, my := mousePoint(lParam)
			panel := -1
			for i, r := range comparePanelRectsAll() {
				if comparePointInRect(mx, my, r) {
					panel = i
					break
				}
			}
			if panel >= 0 {
				if compareTraceInspectMode {
					showCompareTraceSelectionAt(panel, mx, my)
					return 0
				}
				if compareTool > 0 {
					compareAnnotDragging = true
					compareAnnotPanel = panel
					compareAnnotX0, compareAnnotY0, compareAnnotX1, compareAnnotY1 = mx, my, mx, my
				} else {
					compareZoomDragging = true
					compareZoomPanel = panel
					compareZoomX0, compareZoomY0, compareZoomX1, compareZoomY1 = mx, my, mx, my
				}
				pSetCapture.Call(h)
				return 0
			}
		}
	case WM_LBUTTONDBLCLK:
		if compareTraceInspectMode {
			return 0
		}
		mx, my := mousePoint(lParam)
		for _, r := range comparePanelRectsAll() {
			if comparePointInRect(mx, my, r) {
				toggleCompareZoomSnapshot()
				return 0
			}
		}
	case WM_MOUSEMOVE:
		mx, my := mousePoint(lParam)
		if compareAPath == "" {
			updateStartHomeHover(mx, my)
			return 0
		}
		oldValid, oldX, oldY := compareCrosshairValid, compareCrosshairWorldX, compareCrosshairWorldY
		updateCompareCrosshair(mx, my)
		invalidateCompareCrosshairTransition(oldValid, oldX, oldY, compareCrosshairValid, compareCrosshairWorldX, compareCrosshairWorldY)
		if compareAnnotDragging {
			r := compareZoomRectForPanel(compareAnnotPanel)
			compareAnnotX1 = clampInt(mx, int(r.Left), int(r.Right)-1)
			compareAnnotY1 = clampInt(my, int(r.Top), int(r.Bottom)-1)
			// Dragging is less frequent than passive hover; redraw the overlay
			// against the cached static scene, without regenerating seismic data.
			invalidateCompareOverlayAll()
			return 0
		}
		if compareZoomDragging {
			r := compareZoomRectForPanel(compareZoomPanel)
			compareZoomX1 = clampInt(mx, int(r.Left), int(r.Right)-1)
			compareZoomY1 = clampInt(my, int(r.Top), int(r.Bottom)-1)
			invalidateCompareOverlayAll()
			return 0
		}
		// Passive hover invalidates only the old/new crosshair strips above.
		// No whole-window InvalidateRect here: this is the v1.0.2 flicker fix.
		return 0
	case WM_LBUTTONUP:
		if compareAPath == "" && startHomePressed >= 0 {
			mx, my := mousePoint(lParam)
			pReleaseCapture.Call()
			endStartHomePress(mx, my)
			return 0
		}
		if compareAnnotDragging {
			mx, my := mousePoint(lParam)
			r := compareZoomRectForPanel(compareAnnotPanel)
			compareAnnotX1 = clampInt(mx, int(r.Left), int(r.Right)-1)
			compareAnnotY1 = clampInt(my, int(r.Top), int(r.Bottom)-1)
			compareAnnotDragging = false
			pReleaseCapture.Call()
			addCompareAnnotation(compareTool-1, compareAnnotPanel, compareAnnotX0, compareAnnotY0, compareAnnotX1, compareAnnotY1)
			return 0
		}
		if compareZoomDragging {
			mx, my := mousePoint(lParam)
			r := compareZoomRectForPanel(compareZoomPanel)
			compareZoomX1 = clampInt(mx, int(r.Left), int(r.Right)-1)
			compareZoomY1 = clampInt(my, int(r.Top), int(r.Bottom)-1)
			compareZoomDragging = false
			pReleaseCapture.Call()
			x0, y0, x1, y1 := compareZoomX0, compareZoomY0, compareZoomX1, compareZoomY1
			invalidateCompareOverlayAll()
			applyCompareZoom(compareZoomPanel, x0, y0, x1, y1)
			return 0
		}
	case WM_HSCROLL:
		if compareTD != nil && lParam == cc.slider {
			pos, _, _ := pSendMessageW.Call(cc.slider, TBM_GETPOS, 0, 0)
			startLoadComparePosition(int(int32(pos)))
			return 0
		}
	case WM_COMPARE_PROGRESS:
		setCompareProgress(int(wParam), true)
		return 0
	case WM_COMPARE_READY:
		handlePrepareReady()
		return 0
	case WM_COMPARE_SLICE_READY:
		handleSliceReady()
		return 0
	case WM_COMPARE_EXPORT_DONE:
		handleCompareExportDone()
		return 0
	case WM_COMPARE_GEOM_READY:
		handleGeometryDetectReady()
		return 0
	case WM_GETMINMAXINFO:
		if lParam != 0 {
			mmi := (*MINMAXINFO)(unsafe.Pointer(lParam))
			mmi.PtMinTrackSize.X = 1120
			mmi.PtMinTrackSize.Y = 520
		}
		return 0
	case WM_SIZE:
		layoutComparePathLabels()
		layoutCompareStatusProgress()
		layoutCompareWelcomeControls()
		// Panel geometry changes with the client size, so rebuild the static
		// bitmap once. Subsequent mouse hover again uses strip-only restores.
		destroyCompareBaseCache()
		invalidateCompareBase()
		return 0
	case WM_SETCURSOR:
		if atomic.LoadInt32(&compareBusyCount) > 0 {
			c, _, _ := pLoadCursorW.Call(0, IDC_WAIT)
			if c != 0 {
				pSetCursor.Call(c)
			}
			return 1
		}
	case WM_ERASEBKGND:
		// The compare window is fully painted from an off-screen back buffer.
		// Suppress the default background erase to prevent white/gray flashes.
		return 1
	case WM_PAINT:
		paintCompare()
		return 0
	case WM_CLOSE:
		revokeOleSegyDropTarget(h)
		pDestroyWindow.Call(h)
		return 0
	case WM_DESTROY:
		revokeOleSegyDropTarget(h)
		atomic.AddInt64(&compareGen, 1)
		atomic.AddInt64(&compareSliceGen, 1)
		atomic.AddInt64(&compareGeomGen, 1)
		if compareTD != nil {
			compareTD.close()
			compareTD = nil
		}
		destroyCompareBaseCache()
		releaseStartHomeFonts()
		compareHwnd = 0
		cc = compareControls{}
		compareTraceInspectMode = false
		compareA = comparePanel{}
		compareB = comparePanel{}
		compareD = comparePanel{}
		compareShowDiff = false
		compareAutoGeom = false
		compareAnnotations = nil
		compareCrosshairValid = false
		atomic.StoreInt32(&compareBusyCount, 0)
		// This workspace is the visible application root in v1.2. Closing it
		// also closes the hidden legacy compatibility controller.
		if hwnd != 0 {
			pDestroyWindow.Call(hwnd)
			hwnd = 0
		}
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(h, uintptr(msg), wParam, lParam)
	return r
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
