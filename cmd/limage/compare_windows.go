//go:build windows

package main

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/poststack"
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
	IDCMP_CADZOW        = 5030
	IDCMP_CLOSE_B       = 5031

	TBM_GETPOS      = WM_USER
	TBM_SETPOS      = WM_USER + 5
	TBM_SETRANGE    = WM_USER + 6
	TBM_SETRANGEMIN = WM_USER + 7
	TBM_SETRANGEMAX = WM_USER + 8
	TBM_SETTICFREQ  = WM_USER + 20
	TBM_SETPAGESIZE = WM_USER + 21
	TBM_SETLINESIZE = WM_USER + 23

	WM_COMPARE_READY            = WM_USER + 201
	WM_COMPARE_SLICE_READY      = WM_USER + 202
	WM_COMPARE_GEOM_READY       = WM_USER + 203
	WM_COMPARE_PROGRESS         = WM_USER + 204
	WM_COMPARE_EXPORT_DONE      = WM_USER + 205
	WM_COMPARE_PREPARE_PROGRESS = WM_USER + 207
	WM_COMPARE_EXPORT_PROGRESS  = WM_USER + 208

	PBM_SETRANGE = WM_USER + 1
	PBM_SETPOS   = WM_USER + 2
)

type compareControls struct {
	// Main workspace toolbar migrated from the legacy entry window.
	open, save, fixed, mark, data, params, zoom, origin    uintptr
	mail, update, about, compare, volume, spectrum, export uintptr
	traceInspect                                           uintptr
	loadB                                                  uintptr
	toolbar                                                *poststackToolbarResources
	aLabel, bLabel                                         uintptr
	modeLabel, mode                                        uintptr
	slider, timeEdit                                       uintptr
	prev, next                                             uintptr
	timeUnit                                               uintptr
	refresh, close                                         uintptr
	status, progress, coordinates, notice                  uintptr
	ilLabel, xlLabel                                       uintptr
	ilByte, xlByte                                         uintptr
	autoGeom                                               uintptr
	palette, crosshair                                     uintptr
	rectTool, ellipseTool, lineTool, clearAnn              uintptr
	diff                                                   uintptr
	generateB                                              uintptr
	closeB                                                 uintptr
}

type comparePanel struct {
	indices                                []byte
	bgra                                   []byte
	displayValues                          []float64 // immutable pre-palette desktop raster, not a statistics source
	coords                                 []int32   // in the raster's exact source-trace order
	w, h                                   int
	stats                                  segy.RenderStats
	title                                  string
	filtered                               bool
	viewAxes                               bool // physical extents from a composed view snapshot, including unfiltered sides
	filterX0, filterX1, filterT0, filterT1 float64
	sampleOriginUS                         int64
	sampleDtUS                             int
	wave                                   *poststackWaveFrame // sample-domain rows; only present in waveform modes
}

type compareTimeData struct {
	fa, fb              *segy.File
	ga, gb              *segy.GeometryIndex
	ca, cb              *segy.TimeSliceCache
	bounds              segy.SliceBounds
	fullBounds          segy.SliceBounds
	maxSampleA          int
	minSampleA          int
	traceOrderFallback  bool
	bTraceOrderFallback bool
	sampleA             int
	sampleB             int
	indexStatus         string
	navKey              [3]int64
	navLines            [2][]int32
	navValid            bool
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
	retryRetiredPoststackResults()
}

type comparePrepareResult struct {
	owner              uintptr
	gen                int64
	data               *compareTimeData
	valsA, valsB       []float32
	err                error
	bErr               error
	mapping            *compareGeometryRebuild
	aMapping, bMapping compareHeaderMapping
}

type compareSliceResult struct {
	owner            uintptr
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
	Line           int32
	Source         string
	Geometry       int64
}

type compareGeomDetectResult struct {
	owner       uintptr
	gen         int64
	a, b        segy.GeometryDetectResult
	errA, errB  error
	hasB        bool
	interactive bool
}

type compareExportResult struct {
	path    string
	kind    string
	traces  int
	samples int
	err     error
	gen     int64
	owner   uintptr
}

type compareViewSnapshot struct {
	mode                   int
	lineXMin, lineXMax     float64
	sampleStart, sampleEnd int
	bounds                 segy.SliceBounds
}

var (
	compareHwnd                  uintptr
	cc                           compareControls
	compareAPath, compareBPath   string
	compareMode                  int // 0=Inline, 1=Crossline, 2=Time Slice
	compareA, compareB, compareD comparePanel
	compareShowDiff              bool
	compareDeltaReason           string
	compareBVisible              bool
	// The existing renderer serves two explicit toolbar pages. Merely opening
	// a section must never expose processing/B actions before the user enters 比.
	compareWorkspaceActive                                                 bool
	compareNoticeVisible                                                   bool
	compareMapMin, compareMapMax                                           float64
	compareTD                                                              *compareTimeData
	compareAsyncMu                                                         sync.Mutex
	comparePendingPrepare                                                  *comparePrepareResult
	comparePendingSlice                                                    *compareSliceResult
	comparePendingGeom                                                     *compareGeomDetectResult
	compareGen                                                             int64
	comparePrepareCancel                                                   context.CancelFunc
	compareSliceGen                                                        int64
	compareGeomGen                                                         int64
	compareAlgorithmGeometryPending                                        int64 // UI-thread generation while auto-detection may replace A's mapping
	compareAutoGeom                                                        bool
	compareAutoILA, compareAutoXLA, compareAutoILB, compareAutoXLB         int
	compareTraceStart, compareTraceEnd, compareTraceStep                   int64
	compareSampleStart, compareSampleEnd                                   int
	compareOriginTraceStart, compareOriginTraceEnd, compareOriginTraceStep int64
	compareOriginSampleStart, compareOriginSampleEnd                       int
	compareZoomDragging                                                    bool
	compareFilterPanDragging                                               bool
	compareFilterPanPanel, compareFilterPanX, compareFilterPanY            int
	compareZoomPanel                                                       int
	compareZoomX0, compareZoomY0, compareZoomX1, compareZoomY1             int
	comparePaletteIndex                                                    int
	compareCrosshairOn                                                     bool
	compareCrosshairValid                                                  bool
	compareCrosshairWorldX, compareCrosshairWorldY                         float64
	compareTool                                                            poststackCanvasTool
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

	// Persistent compare-scene cache. The expensive seismic raster, axes and
	// committed annotations are rendered only when content/layout changes.
	// Mouse hover restores only the invalid crosshair strips from this bitmap.
	compareBaseDC, compareBaseBmp, compareBaseOldBmp uintptr
	compareBaseW, compareBaseH                       int
	compareBaseDirty                                 bool = true
	compareLastHoverStatus                           time.Time
	compareBusyCount                                 int32
	compareExportGen                                 int64
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
	compareWorkspaceActive = false
	compareZoomToggleValid = false
	comparePaletteIndex = paletteIndex
	compareCrosshairOn = true
	compareCrosshairValid = false
	compareTool = poststackToolZoom
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
		WS_OVERLAPPEDWINDOW|WS_CLIPCHILDREN, uintptr(CW_USEDEFAULT), uintptr(CW_USEDEFAULT), 1560, 820, 0, 0, 0, 0)
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
	setPoststackCompareTool(poststackToolZoom)
	cancelCadzowTask()
	cancelPoststackCompareExport()
	invalidatePoststackViewFilterB()
	compareAPath = sf.Info.Path
	compareBPath = ""
	compareShowDiff = false
	compareBVisible = false
	compareWorkspaceActive = false
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
	releaseGeneratedPoststackResults()
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

const (
	poststackToolbarBottom       = 27
	poststackToolbarClipSiblings = 0x04000000 // WS_CLIPSIBLINGS
)

var poststackToolbarCaptions = map[int]string{
	ID_OPEN: "打开", ID_SAVE: "保存", ID_FIXED: "固定", ID_MARK: "色标",
	ID_DATA: "数据信息", ID_PARAMS: "参数", ID_ZOOM: "缩放", ID_ORIGIN: "复位",
	ID_MAIL: "反馈", ID_UPDATE: "更新", ID_ABOUT: "关于", ID_COMPARE: "比", ID_VOLUME: "体",
	IDCMP_B: "加载 B", IDCMP_CADZOW: "算法处理", IDCMP_CLOSE_B: "关闭 B", IDCMP_DIFF: "A−B",
	IDCMP_SPECTRUM: "谱", IDCMP_EXPORT: "出", IDCMP_TRACE_INSPECT: "道",
	IDCMP_REFRESH: "复位视图", IDCMP_AUTOGEOM: "几何设置",
}

func poststackToolbarCaption(id int, active bool) string {
	if id == ID_COMPARE && active {
		return "返回"
	}
	return poststackToolbarCaptions[id]
}

func poststackToolbarButtonWidth(caption string, measure func(string) int) int {
	textWidth := 0
	for _, r := range caption {
		if r <= 127 {
			textWidth += 8
		} else {
			textWidth += 13
		}
	}
	if measure != nil {
		textWidth = maxInt(textWidth, measure(caption))
	}
	return maxInt(34, textWidth+14) // reserve both edges, including the focus frame
}

func poststackToolbarFontMeasure(font uintptr) (func(string) int, func()) {
	if font != 0 {
		dc, _, _ := pCreateCompatibleDC.Call(0)
		if dc != 0 {
			old, _, _ := pSelectObject.Call(dc, font)
			return func(s string) int {
					var r RECT
					pDrawTextW.Call(dc, uintptr(unsafe.Pointer(u16(s))), ^uintptr(0), uintptr(unsafe.Pointer(&r)), DT_SINGLELINE|0x0400|0x0800) // DT_CALCRECT | DT_NOPREFIX
					return int(r.Right - r.Left)
				}, func() {
					if old != 0 {
						pSelectObject.Call(dc, old)
					}
					pDeleteDC.Call(dc)
				}
		}
	}
	return nil, func() {}
}

// Keep the layout testable without an HWND. Home has no toolbar; the B actions
// exist only in Compare. Paths can be shortened or omitted before any button.
func poststackToolbarRects(width int, hasA, active, hasB bool) map[int]RECT {
	return poststackToolbarRectsMeasured(width, hasA, active, hasB, nil)
}

func poststackToolbarRectsMeasured(width int, hasA, active, hasB bool, measure func(string) int) map[int]RECT {
	return poststackToolbarRectsAtDPI(width, hasA, active, hasB, displayDPI(compareHwnd), measure)
}

func poststackToolbarRectsAtDPI(width int, hasA, active, hasB bool, dpi int, measure func(string) int) map[int]RECT {
	r := make(map[int]RECT)
	if !hasA {
		return r
	}
	type toolSlot struct{ id, width int }
	var slots []toolSlot
	add := func(id, w int) {
		slots = append(slots, toolSlot{id, w})
	}
	button := func(id int) {
		if poststackVectorLabels[id] != "" {
			add(id, poststackVectorButtonWidth(id, active, dpi, measure))
			return
		}
		add(id, poststackToolbarButtonWidth(poststackToolbarCaption(id, active), measure))
	}
	palette := func() {
		w := 100
		if measure != nil {
			for _, name := range paletteNames {
				w = maxInt(w, measure(name)+28) // include the dropdown arrow and edges
			}
		}
		add(IDCMP_PALETTE, w)
	}
	if active {
		button(ID_COMPARE)
		button(IDCMP_B)
		button(IDCMP_CADZOW)
		if hasB {
			button(IDCMP_CLOSE_B)
			button(IDCMP_DIFF)
		}
		palette()
		for _, id := range []int{ID_MARK, ID_FIXED, ID_DATA, ID_PARAMS, ID_ZOOM, ID_ORIGIN, ID_VOLUME, IDCMP_SPECTRUM, IDCMP_TRACE_INSPECT, IDCMP_EXPORT} {
			button(id)
		}
	} else {
		for _, id := range []int{ID_OPEN, ID_SAVE} {
			button(id)
		}
		palette()
		for _, id := range []int{ID_FIXED, ID_MARK, ID_DATA, ID_PARAMS, ID_ZOOM, ID_ORIGIN, ID_MAIL, ID_UPDATE, ID_ABOUT} {
			button(id)
		}
		button(ID_COMPARE)
		for _, id := range []int{ID_VOLUME, IDCMP_SPECTRUM, IDCMP_TRACE_INSPECT, IDCMP_EXPORT} {
			button(id)
		}
	}
	// Only compress inter-button whitespace, not measured captions or icons.
	// Below the supported native minimum, drop secondary legacy actions before
	// clipping core Compare controls. They remain visible in supported sizes.
	buttonWidth := 0
	for _, item := range slots {
		buttonWidth += item.width
	}
	for _, id := range []int{ID_FIXED, ID_DATA, ID_MAIL, ID_UPDATE, ID_ABOUT, ID_ORIGIN} {
		if buttonWidth+len(slots)-1 <= width-20 {
			break
		}
		for i, item := range slots {
			if item.id == id {
				buttonWidth -= item.width
				slots = append(slots[:i], slots[i+1:]...)
				break
			}
		}
	}
	gap := clampInt((width-20-buttonWidth)/max(1, len(slots)-1), 1, 5)
	x := 10
	for _, item := range slots {
		r[item.id] = RECT{Left: int32(x), Top: 2, Right: int32(x + item.width), Bottom: poststackToolbarBottom}
		x += item.width + gap
	}
	available := width - x - 10
	if available >= 40 {
		if active && available >= 72 {
			each := (available - 8) / 2
			r[IDCMP_ALABEL] = RECT{Left: int32(x), Top: 5, Right: int32(x + each), Bottom: 23}
			r[IDCMP_BLABEL] = RECT{Left: int32(x + each + 8), Top: 5, Right: int32(width - 10), Bottom: 23}
		} else {
			r[IDCMP_ALABEL] = RECT{Left: int32(x), Top: 5, Right: int32(width - 10), Bottom: 23}
		}
	}
	return r
}

func compareToolbarControls() []struct {
	id   int
	hwnd uintptr
} {
	return []struct {
		id   int
		hwnd uintptr
	}{
		{ID_OPEN, cc.open}, {ID_SAVE, cc.save}, {IDCMP_PALETTE, cc.palette},
		{ID_FIXED, cc.fixed}, {ID_MARK, cc.mark}, {ID_DATA, cc.data}, {ID_PARAMS, cc.params},
		{ID_ZOOM, cc.zoom}, {ID_ORIGIN, cc.origin}, {ID_MAIL, cc.mail}, {ID_UPDATE, cc.update},
		{ID_ABOUT, cc.about}, {ID_COMPARE, cc.compare}, {ID_VOLUME, cc.volume},
		{IDCMP_B, cc.loadB}, {IDCMP_CADZOW, cc.generateB}, {IDCMP_CLOSE_B, cc.closeB},
		{IDCMP_DIFF, cc.diff}, {IDCMP_SPECTRUM, cc.spectrum}, {IDCMP_EXPORT, cc.export},
		{IDCMP_TRACE_INSPECT, cc.traceInspect}, {IDCMP_ALABEL, cc.aLabel}, {IDCMP_BLABEL, cc.bLabel},
	}
}

func layoutComparePathLabels() {
	if compareHwnd == 0 || cc.aLabel == 0 || cc.bLabel == 0 {
		return
	}
	cw, _ := clientSize(compareHwnd)
	active := compareWorkspaceActive && compareAPath != ""
	measure, release := poststackToolbarFontMeasure(hFont)
	defer release()
	r := poststackToolbarRectsMeasured(cw, compareAPath != "", active, compareHasB(), measure)
	cc.toolbar.popTooltip()
	controls := compareToolbarControls()
	// Move without repainting. The two modes share controls whose new positions
	// can overlap old sibling positions until the whole row has been laid out.
	for _, c := range controls {
		if _, visible := r[c.id]; c.hwnd != 0 && !visible {
			pShowWindow.Call(c.hwnd, SW_HIDE)
		}
	}
	for _, c := range controls {
		if rect, visible := r[c.id]; c.hwnd != 0 && visible {
			height := int(rect.Bottom - rect.Top)
			if c.id == IDCMP_PALETTE {
				height = 260
			}
			pMoveWindow.Call(c.hwnd, uintptr(rect.Left), uintptr(rect.Top), uintptr(rect.Right-rect.Left), uintptr(height), 0)
		}
	}
	if cc.compare != 0 {
		caption := poststackToolAccessibleCaption(ID_COMPARE, active)
		if getText(cc.compare) != caption {
			setText(cc.compare, caption)
		}
		if pSendMessageWCheck(cc.compare) != active {
			pSendMessageW.Call(cc.compare, BM_SETCHECK, uintptr(boolToInt(active)), 0)
		}
	}
	if cc.diff != 0 {
		pEnableWindow.Call(cc.diff, uintptr(boolToInt(compareActiveB())))
	}
	for slot, label := range []struct {
		role, path string
		id         int
		control    uintptr
	}{
		{"A", compareAPath, IDCMP_ALABEL, cc.aLabel}, {"B", compareBPath, IDCMP_BLABEL, cc.bLabel},
	} {
		rect, visible := r[label.id]
		caption := poststackPathCaption(label.role, label.path, int(rect.Right-rect.Left), measure)
		if getText(label.control) != caption {
			setText(label.control, caption)
		}
		path := label.path
		if !visible {
			path = ""
		}
		cc.toolbar.setPathTip(compareHwnd, slot, label.control, path)
	}
	for _, c := range controls {
		if _, visible := r[c.id]; c.hwnd != 0 && visible {
			pShowWindow.Call(c.hwnd, SW_SHOW)
		}
	}
	if cw > 0 {
		row := RECT{Right: int32(cw), Bottom: poststackToolbarBottom + 3}
		pRedrawWindow.Call(compareHwnd, uintptr(unsafe.Pointer(&row)), 0, RDW_INVALIDATE|RDW_ERASE|RDW_ALLCHILDREN)
	}
	layoutCompareChromeNavigation(cw)
	layoutCompareStatusProgress()
}

func setPoststackCompareWorkspace(active bool) {
	if compareAPath == "" {
		active = false
	}
	cancelCadzowTask()
	cancelPoststackCompareExport()
	cancelPoststackComparePrepare()
	atomic.AddInt64(&compareGen, 1)
	atomic.AddInt64(&compareSliceGen, 1)
	atomic.AddInt64(&compareGeomGen, 1)
	compareWorkspaceActive = active
	compareBVisible = active && compareHasB()
	compareCrosshairValid = false
	compareZoomToggleValid = false
	if !active {
		compareShowDiff = false
		compareD = comparePanel{}
		pSendMessageW.Call(cc.diff, BM_SETCHECK, 0, 0)
		if cadzowHwnd != 0 {
			pShowWindow.Call(cadzowHwnd, SW_HIDE)
		}
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
		if active {
			startPrepareTimeCompare()
		}
	}
	// The visible layout changes even if A's current frame cannot be rebuilt
	// synchronously. The cached image is still usable in the new A rectangle.
	invalidateCompareBase()
	if active {
		pSetWindowTextW.Call(compareHwnd, uintptr(unsafe.Pointer(u16(APP_NAME+" v"+APP_VERSION+" [x64] - 叠后 Compare"))))
		setText(cc.status, "叠后 Compare：加载 B 或打开算法处理；A 保持只读，返回剖面可退出对比。")
	} else {
		pSetWindowTextW.Call(compareHwnd, uintptr(unsafe.Pointer(u16(APP_NAME+" v"+APP_VERSION+" [x64] - 地震剖面"))))
		setText(cc.status, "已返回 A 剖面；点击 比 可重新进入叠后 Compare。")
	}
	syncPoststackParameterContext()
	syncPoststackSpectrumControls()
	refreshViewFilterSpectrum()
}

// closePoststackCompareB releases B only; A and its current logical view remain
// usable. Generated result file cleanup is owned by the processing session.
func closePoststackCompareB() {
	invalidatePoststackViewFilterB()
	cancelCadzowTask()
	cancelPoststackCompareExport()
	cancelPoststackComparePrepare()
	atomic.AddInt64(&compareGen, 1)
	atomic.AddInt64(&compareSliceGen, 1)
	atomic.AddInt64(&compareGeomGen, 1)
	compareAsyncMu.Lock()
	if comparePendingPrepare != nil && comparePendingPrepare.data != nil {
		comparePendingPrepare.data.close()
	}
	comparePendingPrepare, comparePendingSlice, comparePendingGeom = nil, nil, nil
	compareAsyncMu.Unlock()
	if compareTD != nil {
		if compareTD.fb != nil {
			_ = compareTD.fb.Close()
			retryRetiredPoststackResults()
		}
		compareTD.fb, compareTD.gb, compareTD.cb, compareTD.currentValsB = nil, nil, nil, nil
		if compareTD.fa != nil {
			compareTD.maxSampleA = compareTD.fa.Info.SamplesPerTrace - 1
		}
	}
	releaseGeneratedPoststackResults()
	compareBPath = ""
	compareBVisible, compareShowDiff = false, false
	compareB, compareD = comparePanel{}, comparePanel{}
	compareDeltaReason = ""
	pSendMessageW.Call(cc.diff, BM_SETCHECK, 0, 0)
	layoutComparePathLabels()
	destroyCompareBaseCache()
	if compareTD != nil && compareMode != 2 {
		renderCompareLine()
	} else {
		invalidateCompareBase()
	}
	setText(cc.status, "已关闭 B；A 和当前视图范围保持不变。")
	syncPoststackParameterContext()
	syncPoststackSpectrumControls()
	refreshViewFilterSpectrum()
}

func cancelPoststackCompareExport() {
	updateNumericalExportContext()
}

func layoutCompareStatusProgress() {
	if compareHwnd == 0 || cc.status == 0 {
		return
	}
	cw, _ := clientSize(compareHwnd)
	active := compareAPath != ""
	progress := active && compareProgressVisible
	showNotice := active && compareNoticeVisible && !progress
	r := compareChromeStatusRects(cw, progress)
	put := func(h uintptr, r RECT, visible bool) {
		if h != 0 {
			pMoveWindow.Call(h, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), 1)
			if visible && r.Right > r.Left {
				pShowWindow.Call(h, SW_SHOW)
			} else {
				pShowWindow.Call(h, SW_HIDE)
			}
		}
	}
	put(cc.coordinates, r.coordinates, active)
	put(cc.status, r.status, active && !showNotice)
	put(cc.notice, r.status, showNotice)
	put(cc.progress, r.progress, progress)
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

// Tokenized progress is addressed to the worker's captured window, never the
// current global window. The receiving UI checks the token again because a
// message can already be queued when B is closed or replaced.
func postCompareOwnedProgress(owner uintptr, gen int64, pct int, export bool) {
	message, current := uint32(WM_COMPARE_PREPARE_PROGRESS), atomic.LoadInt64(&compareGen)
	if export {
		message, current = WM_COMPARE_EXPORT_PROGRESS, atomic.LoadInt64(&compareExportGen)
	}
	if owner != 0 && gen == current {
		pPostMessageW.Call(owner, uintptr(message), uintptr(pct), uintptr(gen))
	}
}

func compareProgressTokenCurrent(owner, currentOwner uintptr, gen, currentGen int64) bool {
	return owner != 0 && owner == currentOwner && gen == currentGen
}

func cancelPoststackComparePrepare() {
	if comparePrepareCancel != nil {
		comparePrepareCancel()
		comparePrepareCancel = nil
		atomic.AddInt64(&compareGen, 1)
	}
}

func createPoststackToolbarButton(id int, style uint32) uintptr {
	return createPoststackToolButton(compareHwnd, id, style, 0, 2, 0, poststackToolbarBottom-2)
}

func createPoststackToolbar() {
	const toggle = poststackPushToggle
	cc.toolbar = &poststackToolbarResources{}
	cc.open = createPoststackToolbarButton(ID_OPEN, BS_PUSHBUTTON)
	cc.save = createPoststackToolbarButton(ID_SAVE, BS_PUSHBUTTON)
	cc.palette = createCtrl(compareHwnd, "COMBOBOX", "", WS_CHILD|WS_TABSTOP|poststackToolbarClipSiblings|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 0, 2, 100, 260, IDCMP_PALETTE)
	for _, name := range paletteNames {
		pSendMessageW.Call(cc.palette, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(name))))
	}
	pSendMessageW.Call(cc.palette, CB_SETCURSEL, uintptr(comparePaletteIndex), 0)
	cc.fixed = createPoststackToolbarButton(ID_FIXED, toggle)
	pSendMessageW.Call(cc.fixed, BM_SETCHECK, BST_CHECKED, 0)
	cc.mark = createPoststackToolbarButton(ID_MARK, BS_PUSHBUTTON)
	cc.data = createPoststackToolbarButton(ID_DATA, BS_PUSHBUTTON)
	cc.params = createPoststackToolbarButton(ID_PARAMS, BS_PUSHBUTTON)
	cc.zoom = createPoststackToolbarButton(ID_ZOOM, toggle)
	cc.origin = createPoststackToolbarButton(ID_ORIGIN, BS_PUSHBUTTON)
	cc.mail = createPoststackToolbarButton(ID_MAIL, BS_PUSHBUTTON)
	cc.update = createPoststackToolbarButton(ID_UPDATE, BS_PUSHBUTTON)
	cc.about = createPoststackToolbarButton(ID_ABOUT, BS_PUSHBUTTON)
	cc.compare = createPoststackToolbarButton(ID_COMPARE, toggle)
	cc.loadB = createPoststackToolbarButton(IDCMP_B, BS_PUSHBUTTON)
	cc.diff = createPoststackToolbarButton(IDCMP_DIFF, toggle)
	cc.volume = createPoststackToolbarButton(ID_VOLUME, BS_PUSHBUTTON)
	cc.spectrum = createPoststackToolbarButton(IDCMP_SPECTRUM, BS_PUSHBUTTON)
	cc.export = createPoststackToolbarButton(IDCMP_EXPORT, BS_PUSHBUTTON)
	cc.traceInspect = createPoststackToolbarButton(IDCMP_TRACE_INSPECT, toggle)
	cc.generateB = createPoststackToolbarButton(IDCMP_CADZOW, BS_PUSHBUTTON)
	cc.closeB = createPoststackToolbarButton(IDCMP_CLOSE_B, BS_PUSHBUTTON)
	pEnableWindow.Call(cc.diff, 0)

	cc.aLabel = createCtrl(compareHwnd, "STATIC", "", WS_CHILD|poststackToolbarClipSiblings|poststackPathLabelStyle, 0, 5, 0, 18, IDCMP_ALABEL)
	cc.bLabel = createCtrl(compareHwnd, "STATIC", "", WS_CHILD|poststackToolbarClipSiblings|poststackPathLabelStyle, 0, 5, 0, 18, IDCMP_BLABEL)
	cc.toolbar.createTooltips(compareHwnd)
}

func createCompareUI() {
	cc = compareControls{}
	// Row 1: the original icons plus native Unicode hover descriptions.
	createPoststackToolbar()

	// Row 2: view direction and navigation. A alone already supports all modes.
	cc.modeLabel = createCtrl(compareHwnd, "STATIC", "方向：", WS_CHILD|WS_VISIBLE, 10, 39, 42, 20, 0)
	cc.mode = createCtrl(compareHwnd, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 54, 35, 105, 120, IDCMP_MODE)
	for _, s := range []string{"Inline", "Crossline", "Time Slice"} {
		pSendMessageW.Call(cc.mode, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(s))))
	}
	pSendMessageW.Call(cc.mode, CB_SETCURSEL, uintptr(compareMode), 0)
	cc.prev = createPoststackToolButtonWithAction(compareHwnd, IDCMP_PREV, poststackGlyphPrevious, "", "上一剖面", WS_VISIBLE|BS_PUSHBUTTON, 174, 35, 31, 23)
	cc.next = createPoststackToolButtonWithAction(compareHwnd, IDCMP_NEXT, poststackGlyphNext, "", "下一剖面", WS_VISIBLE|BS_PUSHBUTTON, 209, 35, 31, 23)
	cc.slider = createCtrl(compareHwnd, "msctls_trackbar32", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP, 248, 34, 360, 25, IDCMP_SLIDER)
	cc.timeEdit = createCtrl(compareHwnd, "EDIT", "", WS_CHILD|WS_BORDER|ES_READONLY, 615, 35, 90, 22, IDCMP_TIME)
	cc.timeUnit = createCtrl(compareHwnd, "STATIC", "IL", WS_CHILD|WS_VISIBLE, 709, 39, 28, 18, 0)
	cc.refresh = createPoststackToolbarButton(IDCMP_REFRESH, WS_VISIBLE|BS_PUSHBUTTON)
	cc.autoGeom = createPoststackToolbarButton(IDCMP_AUTOGEOM, WS_VISIBLE|BS_PUSHBUTTON)
	cc.ilByte = createCtrl(compareHwnd, "EDIT", "189", WS_CHILD, 0, 0, 1, 1, IDCMP_ILBYTE)
	cc.xlByte = createCtrl(compareHwnd, "EDIT", "193", WS_CHILD, 0, 0, 1, 1, IDCMP_XLBYTE)

	// Row 3: linked QC and annotations.
	cc.crosshair = createPoststackNamedToolButton(compareHwnd, IDCMP_CROSSHAIR, 0, "十字联动", WS_VISIBLE|poststackPushToggle, 10, 67, 76, 23)
	pSendMessageW.Call(cc.crosshair, BM_SETCHECK, BST_CHECKED, 0)
	cc.rectTool = createPoststackNamedToolButton(compareHwnd, IDCMP_RECTTOOL, 0, "矩形", WS_VISIBLE|poststackPushToggle, 94, 67, 54, 23)
	cc.ellipseTool = createPoststackNamedToolButton(compareHwnd, IDCMP_ELLIPSETOOL, 0, "椭圆", WS_VISIBLE|poststackPushToggle, 152, 67, 54, 23)
	cc.lineTool = createPoststackNamedToolButton(compareHwnd, IDCMP_LINETOOL, 0, "线段", WS_VISIBLE|poststackPushToggle, 210, 67, 54, 23)
	cc.clearAnn = createPoststackNamedToolButton(compareHwnd, IDCMP_CLEARANN, 0, "清除标注", WS_VISIBLE|BS_PUSHBUTTON, 272, 67, 72, 23)
	cc.close = createPoststackNamedToolButton(compareHwnd, IDCMP_CLOSE, 0, "关闭", WS_VISIBLE|BS_PUSHBUTTON, 350, 67, 52, 23)
	syncPoststackCompareToolButtons()
	cc.coordinates = createCtrl(compareHwnd, "STATIC", "当前坐标：—", WS_CHILD|poststackPathLabelStyle, 416, 70, 0, 20, 0)
	cc.notice = createCtrl(compareHwnd, "STATIC", "", WS_CHILD|poststackPathLabelStyle, 416, 70, 0, 20, 0)
	cc.status = createCtrl(compareHwnd, "STATIC", "打开 A 后即可浏览 Inline / Crossline / Time Slice；可加载 B 或打开算法处理。", WS_CHILD|poststackPathLabelStyle, 416, 70, 0, 20, IDCMP_STATUS)
	cc.progress = createCtrl(compareHwnd, "msctls_progress32", "", WS_CHILD, 1320, 69, 210, 17, 0)
	pSendMessageW.Call(cc.progress, PBM_SETRANGE, 0, uintptr(uint32(100)<<16))
	cc.toolbar.addControlTip(compareHwnd, cc.prev, "上一剖面；Time Slice 中为上一样点")
	cc.toolbar.addControlTip(compareHwnd, cc.next, "下一剖面；Time Slice 中为下一样点")
	cc.toolbar.addControlTip(compareHwnd, cc.refresh, "复位当前视图范围；不改写道头字节设置")
	cc.toolbar.addControlTip(compareHwnd, cc.autoGeom, "定位 Inline / Crossline，或设置、预览和恢复 A / 独立 B 的道头映射")
	cc.toolbar.addControlTip(compareHwnd, cc.close, "关闭地震浏览与对比窗口；不是结束标注")

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
	for _, h := range []uintptr{cc.prev, cc.next, cc.slider, cc.autoGeom} {
		if h != 0 {
			pShowWindow.Call(h, cmd)
		}
	}
	aux := uintptr(SW_HIDE)
	if show && compareMode == 2 {
		aux = SW_SHOW
	}
	pShowWindow.Call(cc.timeEdit, aux)
	pShowWindow.Call(cc.timeUnit, aux)
	width, _ := clientSize(compareHwnd)
	layoutCompareChromeNavigation(width)
}

func comparePanelRectsAll() []RECT {
	plans := seismicCompareLayouts()
	out := make([]RECT, len(plans))
	for i, p := range plans {
		out[i] = p.Plot
	}
	return out
}

// Compare reserves a B panel before a result exists. Keep this separate from
// comparePanelCount, which counts real panels for crosshairs and hit testing.
func compareDisplayPanelCount(workspace, hasB, showDiff bool) int {
	if hasB && showDiff {
		return 3
	}
	if workspace || hasB {
		return 2
	}
	return 1
}

func compareLayoutRects(cw, ch, n int) []RECT {
	top := 148
	bottom := 44
	marginX := 50
	gap := 46
	totalW := cw - 2*marginX - gap*(n-1)
	if n < 1 {
		return nil
	}
	if totalW < n*2 {
		marginX = min(50, max(0, cw/(n*4)))
		gap = min(46, max(0, cw/(n*4)))
		totalW = max(n*2, cw-2*marginX-gap*(n-1))
	}
	each := totalW / n
	hh := ch - top - bottom
	if hh < 2 {
		top = min(top, max(0, ch-2))
		hh = max(2, ch-top)
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

func compareCurrentPalette() [256]rgb {
	return paletteRGB(comparePaletteIndex)
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

// compareMappingGain combines the user's gain with the existing 99% clip
// setting for the visible 2-D renderer.  The SEG-Y core historically used
// GainPercent for percentile trimming and ignored ClipPercent, so one finite
// spike could stretch the whole 2-D palette.  The volume renderer already
// treats ClipPercent as symmetric tail rejection; use the same effective
// percentile here without changing the frozen core/3-D rendering path.
func compareMappingGain() float64 {
	g := gainPercent
	if clipPercent > 0 && clipPercent < 100 {
		g += (100 - clipPercent) / 2
	}
	if g < 0 {
		g = 0
	}
	if g > 49 {
		g = 49
	}
	return g
}

func renderCompareSection() {
	defer retryRetiredPoststackResults()
	if compareAPath == "" || compareBPath == "" {
		return
	}
	fa, err := openPoststackCompareSource(compareAPath)
	if err != nil {
		message(compareHwnd, "对比", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	defer fa.Close()
	fb, err := openPoststackCompareSource(compareBPath)
	if err != nil {
		message(compareHwnd, "对比", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	defer fb.Close()
	compareDtAUS = fa.Info.SampleIntervalUS
	ar, br := comparePanelRects()
	aw, ah := rectWH(ar)
	bw, bh := rectWH(br)
	aDrawWidth, bDrawWidth := aw, bw
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
	renderOne := func(f *segy.File, w, h, drawWidth int) (comparePanel, error) {
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
		var values []float64
		pix, st, e := f.RenderWithOptions(segy.RenderOptions{Width: w, Height: h, AGC: agc, ClipPercent: clipPercent, GainPercent: compareMappingGain(), KeepInvalid: true, CaptureDisplayValues: func(v []float64) { values = v },
			UseValueLimits: useLimits, MinValue: limitMin, MaxValue: limitMax, TraceStart: tr0, TraceEnd: tr1, TraceStep: traceStep, SampleStart: sm0, SampleEnd: sm1, DisplayMode: renderDisplayMode})
		if e != nil {
			return comparePanel{}, e
		}
		p := comparePanel{indices: pix, bgra: comparePaletteBGRA(pix), displayValues: values, w: w, h: h, stats: st}
		if poststackWaveActive() {
			traces := poststackLegacyWaveTraceRange(tr0, tr1, traceStep)
			coordinates := poststackWaveCoordinates(traces, nil)
			pick := poststackSelectWaveform(coordinates, poststackWaveWidthDIP(drawWidth, displayDPI(compareHwnd)), sm1-sm0+1, poststackWaveDisplay)
			p.wave = poststackBuildWaveFrame(traces, coordinates, sm0, sm1, f.Info.SamplesPerTrace, f.Info.SampleIntervalUS, pick, f.ReadTraceWindow)
		}
		return p, nil
	}
	pa, err := renderOne(fa, aw, ah, aDrawWidth)
	if err != nil {
		message(compareHwnd, "对比", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	pb, err := renderOne(fb, bw, bh, bDrawWidth)
	if err != nil {
		message(compareHwnd, "对比", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	pa.title = "A  " + shortPath(compareAPath, 42)
	pb.title = "B  " + shortPath(compareBPath, 42)
	compareA, compareB = pa, pb
	seismicRefreshCompareColors()
	setText(cc.status, fmt.Sprintf("剖面对比 | 在任一窗口拖动框选 = A/B 同步 Zoom | 增益 %.0f%% | %s", gainPercent, paletteNames[comparePaletteIndex]))
	invalidateCompareBase()
}

func configureCompareModeSlider() {
	if compareTD == nil || compareTD.ga == nil {
		return
	}
	if compareMode == 2 {
		pSendMessageW.Call(cc.slider, TBM_SETRANGEMIN, 1, uintptr(compareTD.minSampleA))
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
	lines := compareNavigationLines(compareTD, compareMode)
	if len(lines) == 0 {
		return
	}
	rank, exact := compareLineRank(lines, compareLineCoord)
	if !exact {
		if rank == len(lines) || rank == 0 {
			rank = len(lines) / 2
		} else if int64(compareLineCoord)-int64(lines[rank-1]) <= int64(lines[rank])-int64(compareLineCoord) {
			rank--
		}
		compareLineCoord = lines[rank]
	}
	pSendMessageW.Call(cc.slider, TBM_SETRANGEMIN, 1, 0)
	pSendMessageW.Call(cc.slider, TBM_SETRANGEMAX, 1, uintptr(len(lines)-1))
	rng := maxInt(len(lines)-1, 1)
	pSendMessageW.Call(cc.slider, TBM_SETTICFREQ, uintptr(maxInt(rng/50, 1)), 0)
	pSendMessageW.Call(cc.slider, TBM_SETPAGESIZE, 0, uintptr(maxInt(rng/100, 1)))
	pSendMessageW.Call(cc.slider, TBM_SETLINESIZE, 0, 1)
	pSendMessageW.Call(cc.slider, TBM_SETPOS, 1, uintptr(rank))
}

func resetCompareLineBounds() {
	if compareTD == nil {
		return
	}
	b := compareTD.fullBounds
	// The legacy Open dialog accepts a physical trace window before any
	// geometry worker starts. A regular post-stack cube still has a complete
	// GeometryIndex, so resetting directly to fullBounds used to discard a
	// smaller "终止道". Restrict the across-line bounds to the accepted
	// physical range; renderCompareLine applies the same filter to traces.
	if start, end, step := compareTraceRangeForGeometry(compareTD.fa); end >= start {
		axis := compareMode
		if traces, coords, _, err := compareTD.ga.LineTraceNumbers(axis, compareLineCoord); err == nil {
			_, selectedCoords := filterPhysicalTraceRange(traces, coords, start, end, step)
			if len(selectedCoords) > 0 {
				lo, hi := float64(selectedCoords[0]), float64(selectedCoords[0])
				for _, c := range selectedCoords[1:] {
					if float64(c) < lo {
						lo = float64(c)
					}
					if float64(c) > hi {
						hi = float64(c)
					}
				}
				compareLineXMin, compareLineXMax = lo, hi
				compareLineOriginXMin, compareLineOriginXMax = lo, hi
				return
			}
		}
	}
	if compareMode == 0 {
		compareLineXMin, compareLineXMax = float64(b.CrosslineMin), float64(b.CrosslineMax)
	} else {
		compareLineXMin, compareLineXMax = float64(b.InlineMin), float64(b.InlineMax)
	}
	compareLineOriginXMin, compareLineOriginXMax = compareLineXMin, compareLineXMax
}

func compareTraceRangeForGeometry(file *segy.File) (start, end, step int64) {
	start, end, step = compareTraceStart, compareTraceEnd, compareTraceStep
	if file == nil {
		return 0, -1, 1
	}
	if generatedPoststackTraceOrder(file.Info.Path) != nil {
		// Generated B rows are compact local indices, already selected from A.
		// Do not apply A's physical start/end/step a second time to B.
		return 0, file.Info.TraceCount - 1, 1
	}
	if start < 0 {
		start = 0
	}
	if end < 0 || end >= file.Info.TraceCount {
		end = file.Info.TraceCount - 1
	}
	if start >= file.Info.TraceCount {
		return 0, -1, 1
	}
	if step < 1 {
		step = 1
	}
	return start, end, step
}

func filterPhysicalTraceRange(traces []int64, coords []int32, start, end, step int64) ([]int64, []int32) {
	if len(traces) != len(coords) || end < start {
		return traces, coords
	}
	if step < 1 {
		step = 1
	}
	outTraces := make([]int64, 0, len(traces))
	outCoords := make([]int32, 0, len(coords))
	for i, trace := range traces {
		if trace < start || trace > end || (trace-start)%step != 0 {
			continue
		}
		outTraces = append(outTraces, trace)
		outCoords = append(outCoords, coords[i])
	}
	return outTraces, outCoords
}

func filterLineRange(traces []int64, coords []int32, lo, hi float64) ([]int64, []int32) {
	if hi < lo || len(traces) != len(coords) {
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
	return ot, oc
}

func pairLineTracesByCoord(tA []int64, cA []int32, tB []int64, cB []int32) ([]int64, []int64, []int32) {
	mb := make(map[int32]int64, len(cB))
	countA, countB := make(map[int32]int, len(cA)), make(map[int32]int, len(cB))
	for i, c := range cB {
		if i < len(tB) {
			mb[c] = tB[i]
			countB[c]++
		}
	}
	for i, c := range cA {
		if i < len(tA) {
			countA[c]++
		}
	}
	pa := make([]int64, 0, minInt(len(tA), len(tB)))
	pb := make([]int64, 0, cap(pa))
	pc := make([]int32, 0, cap(pa))
	for i, c := range cA {
		if i >= len(tA) {
			break
		}
		if tb, ok := mb[c]; ok && countA[c] == 1 && countB[c] == 1 {
			pa = append(pa, tA[i])
			pb = append(pb, tb)
			pc = append(pc, c)
		}
	}
	return pa, pb, pc
}

func renderCompareLine() {
	if poststackInputViewChanged() {
		cancelCadzowTask()
	}
	cancelPoststackCompareExport()
	if scheduleCurrentViewFilter() {
		return
	}
	renderCompareLineBase()
}

func renderCompareLineBase() {
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
	aDrawWidth := aw
	aw = minInt(maxInt(aw, 2), 1000)
	ah = minInt(maxInt(ah, 2), 1000)
	tA, cA, actualA, eA := d.ga.LineTraceNumbers(axis, target)
	if eA != nil {
		setText(cc.status, eA.Error())
		return
	}
	if start, end, step := compareTraceRangeForGeometry(d.fa); end >= start {
		tA, cA = filterPhysicalTraceRange(tA, cA, start, end, step)
	}
	tA, cA = filterLineRange(tA, cA, compareLineXMin, compareLineXMax)
	opts := func(w, h int) segy.RenderOptions {
		return segy.RenderOptions{Width: w, Height: h, AGC: agc, ClipPercent: clipPercent, GainPercent: compareMappingGain(), UseValueLimits: useLimits, MinValue: limitMin, MaxValue: limitMax, SampleStart: compareSampleStart, SampleEnd: compareSampleEnd, DisplayMode: renderDisplayMode, KeepInvalid: true}
	}
	hasB := compareActiveB() && d.gb != nil && d.fb != nil && len(rects) >= 2
	var valuesA []float64
	var paPix []byte
	var paSt segy.RenderStats
	var e error
	if compareWorkspaceActive && hasB {
		valuesA, paSt, e = d.fa.RenderTraceIndicesValues(tA, opts(aw, ah))
		lo, hi := poststackSharedSectionRange(valuesA, nil, compareMappingGain(), useLimits, limitMin, limitMax)
		paSt.MapMin, paSt.MapMax = lo, hi
		paPix = segy.MapAmplitudeValues(valuesA, lo, hi)
	} else {
		o := opts(aw, ah)
		o.CaptureDisplayValues = func(v []float64) { valuesA = v }
		paPix, paSt, e = d.fa.RenderTraceIndices(tA, o)
	}
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
	if d.traceOrderFallback {
		modeName, axisName = "单线道序", "物理道"
	}
	compareA = comparePanel{indices: paPix, bgra: comparePaletteBGRA(paPix), displayValues: valuesA, coords: cA, w: aw, h: ah, stats: paSt, title: fmt.Sprintf("A  %s %d", modeName, actualA)}
	seismicSetPanelTime(&compareA, d.fa, tA)
	waveActive := poststackWaveActive()
	var waveA poststackWaveSelection
	if waveActive {
		coords := poststackWaveCoordinates(tA, cA)
		waveA = poststackSelectWaveform(coords, poststackWaveWidthDIP(aDrawWidth, displayDPI(compareHwnd)), paSt.SampleEnd-paSt.SampleStart+1, poststackWaveDisplay)
		compareA.wave = poststackBuildWaveFrame(tA, coords, paSt.SampleStart, paSt.SampleEnd, d.fa.Info.SamplesPerTrace, d.fa.Info.SampleIntervalUS, waveA, d.fa.ReadTraceWindow)
	}
	compareB = comparePanel{}
	compareD = comparePanel{}
	compareDeltaReason = ""
	diffNote := ""
	actualB := int32(0)
	if hasB {
		bw, bh := rectWH(rects[1])
		bDrawWidth := bw
		bw = minInt(maxInt(bw, 2), 1000)
		bh = minInt(maxInt(bh, 2), 1000)
		tB, cB, aB, eB := d.gb.LineTraceNumbers(axis, target)
		actualB = aB
		if generatedPoststackTraceOrder(d.fb.Info.Path) != nil && (eB != nil || actualB != actualA) {
			compareDeltaReason = "处理结果 B 不覆盖当前几何线"
			setText(cc.status, compareDeltaReason+"；A 保持可浏览。")
			refreshCompareNavigationFields()
			invalidateCompareBase()
			return
		}
		if eB != nil {
			setText(cc.status, eB.Error())
			invalidateCompareBase()
			return
		}
		if start, end, step := compareTraceRangeForGeometry(d.fb); end >= start {
			tB, cB = filterPhysicalTraceRange(tB, cB, start, end, step)
		}
		tB, cB = filterLineRange(tB, cB, compareLineXMin, compareLineXMax)
		if len(tB) == 0 {
			compareDeltaReason = "B 在当前范围没有有效数据道"
			setText(cc.status, compareDeltaReason+"；A 保持可浏览。")
			refreshCompareNavigationFields()
			invalidateCompareBase()
			return
		}
		bOpts := opts(bw, bh)
		if offset, known := generatedPoststackSourceStart(d.fb.Info.Path); known {
			first, last, ok := compareSourceSampleOverlap(bOpts.SampleStart, bOpts.SampleEnd, d.fa.Info.SamplesPerTrace, offset, d.fb.Info.SamplesPerTrace)
			if !ok {
				compareDeltaReason = "B 当前采样窗没有对应样点"
				setText(cc.status, "B 当前采样窗没有对应样点；A 保持可浏览。")
				invalidateCompareBase()
				return
			}
			bOpts.SampleStart, bOpts.SampleEnd = first-offset, last-offset
		}
		var pbPix []byte
		var pbSt segy.RenderStats
		var valuesB []float64
		var er error
		if compareWorkspaceActive {
			valuesB, pbSt, er = d.fb.RenderTraceIndicesValues(tB, bOpts)
			if er == nil {
				lo, hi := poststackSharedSectionRange(valuesA, valuesB, compareMappingGain(), useLimits, limitMin, limitMax)
				viewFilter.baseKey, viewFilter.baseStyle = viewFilterCurrentKey(), viewFilterCurrentStyle()
				viewFilter.baseTone = viewFilterTone{lo, hi, true}
				compareMapMin, compareMapMax = lo, hi
				paSt.MapMin, paSt.MapMax, pbSt.MapMin, pbSt.MapMax = lo, hi, lo, hi
				compareA.indices = segy.MapAmplitudeValues(valuesA, lo, hi)
				compareA.bgra, compareA.stats = comparePaletteBGRA(compareA.indices), paSt
				pbPix = segy.MapAmplitudeValues(valuesB, lo, hi)
			}
		} else {
			bOpts.CaptureDisplayValues = func(v []float64) { valuesB = v }
			pbPix, pbSt, er = d.fb.RenderTraceIndices(tB, bOpts)
		}
		if er != nil {
			setText(cc.status, er.Error())
			return
		}
		compareB = comparePanel{indices: pbPix, bgra: comparePaletteBGRA(pbPix), displayValues: valuesB, coords: cB, w: bw, h: bh, stats: pbSt, title: fmt.Sprintf("B  %s %d", modeName, actualB)}
		seismicSetPanelTime(&compareB, d.fb, tB)
		if waveActive {
			coords := poststackWaveCoordinates(tB, cB)
			pick := poststackWaveSelectionForPanel(coords, waveA, actualA == actualB, poststackWaveWidthDIP(bDrawWidth, displayDPI(compareHwnd)), pbSt.SampleEnd-pbSt.SampleStart+1, poststackWaveDisplay)
			compareB.wave = poststackBuildWaveFrame(tB, coords, pbSt.SampleStart, pbSt.SampleEnd, d.fb.Info.SamplesPerTrace, d.fb.Info.SampleIntervalUS, pick, d.fb.ReadTraceWindow)
		}
		if compareShowDiff && len(rects) >= 3 {
			if !compareDifferenceGeometryKnown(d) {
				diffNote = " | 差未计算：独立 A/B 缺少可核实的共同几何"
				compareDeltaReason = "独立 A/B 缺少可核实的共同几何"
			} else if actualA != actualB {
				diffNote = fmt.Sprintf(" | 差未计算：A/B实际%s不同(%d/%d)", modeName, actualA, actualB)
				compareDeltaReason = "A/B 实际几何线不同"
			} else {
				pA, pB, common := pairLineTracesByCoord(tA, cA, tB, cB)
				if len(pA) >= 1 {
					dw, dh := rectWH(rects[2])
					dw = minInt(maxInt(dw, 2), 1000)
					dh = minInt(maxInt(dh, 2), 1000)
					var dPix []byte
					var dValues []float64
					var dSt segy.RenderStats
					var de error
					dOpts := opts(dw, dh)
					dOpts.AGC = false
					dOpts.CaptureDisplayValues = func(v []float64) { dValues = v }
					bOffset := 0
					if compareWorkspaceActive {
						offset, known := generatedPoststackSourceStart(d.fb.Info.Path)
						bOffset = offset
						first, last, overlap := compareSourceSampleOverlap(dOpts.SampleStart, dOpts.SampleEnd, d.fa.Info.SamplesPerTrace, offset, d.fb.Info.SamplesPerTrace)
						if !overlap {
							de = fmt.Errorf("当前采样窗无共同有效样点")
						} else if !known && d.fa.Info.SamplesPerTrace != d.fb.Info.SamplesPerTrace {
							de = fmt.Errorf("SampleCount 不兼容：A=%d B=%d", d.fa.Info.SamplesPerTrace, d.fb.Info.SamplesPerTrace)
						} else {
							dOpts.SampleStart, dOpts.SampleEnd = first, last
							dPix, dSt, de = segy.RenderTraceDifferencePairsWindowed(d.fa, d.fb, pA, pB, dOpts, offset)
						}
					} else {
						dPix, dSt, de = segy.RenderTraceDifferencePairs(d.fa, d.fb, pA, pB, dOpts)
					}
					if de == nil {
						compareD = comparePanel{indices: dPix, bgra: comparePaletteBGRA(dPix), displayValues: dValues, coords: common, w: dw, h: dh, stats: dSt, title: fmt.Sprintf("差  A-B  %s %d", modeName, actualA)}
						seismicSetPanelTime(&compareD, d.fa, pA)
						if waveActive {
							coords := poststackWaveCoordinates(pA, common)
							drawWidth, _ := rectWH(rects[2])
							pick := poststackWaveSelectionForPanel(coords, waveA, true, poststackWaveWidthDIP(drawWidth, displayDPI(compareHwnd)), dSt.SampleEnd-dSt.SampleStart+1, poststackWaveDisplay)
							compareD.wave = poststackBuildWaveFrame(pA, coords, dSt.SampleStart, dSt.SampleEnd, d.fa.Info.SamplesPerTrace, d.fa.Info.SampleIntervalUS, pick, poststackWaveDifferenceReader(d.fa, d.fb, pA, pB, bOffset))
						}
					} else {
						diffNote = " | 差未计算：" + de.Error()
						compareDeltaReason = de.Error()
					}
				} else {
					diffNote = " | 差未计算：A/B 没有可唯一配对的共同几何位置"
					compareDeltaReason = "A/B 没有可唯一配对的共同几何位置"
				}
			}
		}
	}
	if rank, found := compareLineRank(compareNavigationLines(d, compareMode), actualA); found {
		pSendMessageW.Call(cc.slider, TBM_SETPOS, 1, uintptr(rank))
	}
	refreshCompareNavigationFields()
	if hasB {
		extra := ""
		if compareShowDiff && diffNote == "" {
			extra = " | 差=A−B"
		}
		setText(cc.status, fmt.Sprintf("%s 对比 | A=%d B=%d | %s %.0f..%.0f | 增益 %.0f%% | %s%s%s", modeName, actualA, actualB, axisName, compareLineXMin, compareLineXMax, gainPercent, poststackCurrentPlotCaption(), extra, diffNote))
	} else {
		hint := "点击 比 进入叠后 Compare"
		if compareWorkspaceActive {
			hint = "点击 加载 B 或 算法处理…"
		}
		setText(cc.status, fmt.Sprintf("A 浏览 | %s %d | %s %.0f..%.0f | 增益 %.0f%% | %s | %s", modeName, actualA, axisName, compareLineXMin, compareLineXMax, gainPercent, poststackCurrentPlotCaption(), hint))
	}
	seismicRefreshCompareColors()
	invalidateCompareBase()
	syncPoststackParameterContext()
	syncPoststackSpectrumControls()
	refreshViewFilterSpectrum()
	refreshScientificTraceFromCompare()
}

func poststackSharedSectionRange(a, b []float64, gain float64, manual bool, minimum, maximum float64) (float64, float64) {
	if manual && maximum > minimum && !math.IsNaN(minimum) && !math.IsNaN(maximum) && !math.IsInf(minimum, 0) && !math.IsInf(maximum, 0) {
		return minimum, maximum
	}
	values := make([]float64, 0, len(a)+len(b))
	for _, source := range [][]float64{a, b} {
		for _, v := range source {
			if !math.IsNaN(v) && !math.IsInf(v, 0) {
				values = append(values, v)
			}
		}
	}
	if len(values) == 0 {
		return -1, 1
	}
	sort.Float64s(values)
	gain = math.Max(0, math.Min(49, gain))
	lo := values[minInt(int(float64(len(values))*gain/100), len(values)-1)]
	hi := values[minInt(int(float64(len(values))*(100-gain)/100), len(values)-1)]
	if hi <= lo {
		hi = lo + 1
	}
	return lo, hi
}

func startLoadComparePosition(pos int) {
	if compareTD == nil {
		return
	}
	if compareMode == 2 {
		startLoadTimeSlice(pos)
		return
	}
	lines := compareNavigationLines(compareTD, compareMode)
	if pos < 0 || pos >= len(lines) || compareLineCoord == lines[pos] {
		return
	}
	wasFull := compareLineXMin == compareLineOriginXMin && compareLineXMax == compareLineOriginXMax
	compareLineCoord = lines[pos]
	if wasFull {
		resetCompareLineBounds()
	}
	renderCompareLine()
}

func refreshComparePaletteOnly() {
	if compareHwnd == 0 {
		return
	}
	if compareMode != 2 {
		seismicRefreshCompareColors()
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
	p := openDataDialog(compareHwnd)
	if p == "" {
		return false
	}
	return applyComparePath(which, p)
}

func applyComparePath(which int, p string) bool {
	if which != 0 && strings.EqualFold(filepath.Clean(p), filepath.Clean(compareAPath)) {
		message(compareHwnd, "加载 B", "A 与 B 不能使用同一个文件。", MB_OK|MB_ICONINFORMATION)
		return false
	}
	cancelCadzowTask()
	invalidatePoststackViewFilterB()
	if which == 0 {
		setPoststackCompareTool(poststackToolZoom)
		displayCloseDistance(compareHwnd)
		cancelPoststackCompareExport()
		compareAPath = p
		compareBPath = ""
		compareBVisible = false
		compareWorkspaceActive = false
		compareShowDiff = false
		compareZoomToggleValid = false
		pSendMessageW.Call(cc.compare, BM_SETCHECK, 0, 0)
		pSendMessageW.Call(cc.diff, BM_SETCHECK, 0, 0)
		pEnableWindow.Call(cc.diff, 0)
	} else {
		cancelPoststackCompareExport()
		compareBPath = p
		compareBVisible = true
		compareWorkspaceActive = true
		compareZoomToggleValid = false
		pSendMessageW.Call(cc.compare, BM_SETCHECK, BST_CHECKED, 0)
		pEnableWindow.Call(cc.diff, 1)
	}
	layoutComparePathLabels()
	compareAutoGeom = false
	invalidateCompareTimeData()
	if which == 0 {
		releaseGeneratedPoststackResults()
	} else {
		retirePoststackPreviewsExcept(nil)
		refreshPoststackResultChoices()
		refreshPoststackOutputPreview()
	}
	if compareAPath != "" {
		// Auto geometry is the default for both A-only browsing and A/B QC.
		startAutoDetectGeometry()
	}
	return true
}

func invalidateCompareTimeData() {
	syncViewFilterSource()
	cancelPoststackViewFilter()
	if viewFilterWanted() != nil {
		compareA, compareB, compareD = comparePanel{}, comparePanel{}, comparePanel{}
		invalidateCompareBase()
	}
	cancelPoststackComparePrepare()
	atomic.AddInt64(&compareGen, 1)
	atomic.AddInt64(&compareSliceGen, 1)
	if compareTD != nil {
		compareTD.close()
		compareTD = nil
	}
	syncPoststackSpectrumControls()
	refreshViewFilterSpectrum()
}

func compareGeometryBytesForFiles() (int, int, int, int, bool) {
	a, b, ok := compareEffectiveHeaderMappings()
	return a.il, a.xl, b.il, b.xl, ok
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
	if poststackCycleWavePalette(delta) {
		return
	}
	if len(paletteNames) == 0 {
		return
	}
	next := (comparePaletteIndex + delta) % len(paletteNames)
	if next < 0 {
		next += len(paletteNames)
	}
	if cc.palette != 0 {
		pSendMessageW.Call(cc.palette, CB_SETCURSEL, uintptr(next), 0)
	}
	applyCompareToolbarPalette(next)
}

func applyCompareToolbarPalette(index int) {
	if index < 0 || index >= len(paletteNames) {
		return
	}
	mode := seismicUseCompareToolbarPalette(index)
	if !compareWorkspaceActive {
		paletteIndex = index
		if comboColor != 0 {
			pSendMessageW.Call(comboColor, CB_SETCURSEL, uintptr(paletteIndex), 0)
		}
	}
	refreshComparePaletteOnly()
	setText(cc.status, "色标："+paletteNames[index]+"（"+mode+"）")
}

func startAutoDetectGeometry() { startAutoDetectGeometryMode(false) }

func startAutoDetectGeometryInteractive() { startAutoDetectGeometryMode(true) }

func startAutoDetectGeometryMode(interactive bool) {
	if compareAPath == "" {
		if interactive {
			message(compareHwnd, "自动识别", "请先选择 A 数据。", MB_OK|MB_ICONINFORMATION)
		}
		return
	}
	savedA, hasA := compareSourceMapping(compareAPath, compareGeometrySourceA)
	savedB, hasB := compareSourceMapping(compareBPath, compareGeometrySourceB)
	generatedB := generatedPoststackTraceOrder(compareBPath) != nil
	if !interactive && hasA && (compareBPath == "" || hasB || generatedB) {
		atomic.AddInt64(&compareGeomGen, 1)
		compareAlgorithmGeometryPending = 0
		startPrepareTimeCompare()
		return
	}
	gen := atomic.AddInt64(&compareGeomGen, 1)
	compareAlgorithmGeometryPending = gen
	updateCadzowAlgorithmUI()
	setCompareBusy(1)
	setCompareProgress(3, true)
	setText(cc.status, "正在自动识别 Inline/Crossline 字节位置...（仅抽样读取 240-byte 道头）")
	go func(aPath, bPath string, g int64, owner uintptr) {
		r := &compareGeomDetectResult{gen: g, owner: owner, hasB: bPath != "" && !generatedB, interactive: interactive}
		detect := func(path string) (segy.GeometryDetectResult, error) {
			defer retryRetiredPoststackResults()
			f, err := openPoststackCompareSource(path)
			if err != nil {
				return segy.GeometryDetectResult{}, err
			}
			defer f.Close()
			return f.DetectPoststackSectionGeometryBytes(3072)
		}
		detectUnlessSaved := func(path string, known bool, m compareHeaderMapping) (segy.GeometryDetectResult, error) {
			if known && !interactive {
				return segy.GeometryDetectResult{InlineByte: m.il, CrosslineByte: m.xl, Confidence: 1}, nil
			}
			return detect(path)
		}
		if r.hasB {
			var wg sync.WaitGroup
			wg.Add(2)
			go func() { defer wg.Done(); r.a, r.errA = detectUnlessSaved(aPath, hasA, savedA) }()
			go func() { defer wg.Done(); r.b, r.errB = detectUnlessSaved(bPath, hasB, savedB) }()
			wg.Wait()
		} else {
			r.a, r.errA = detectUnlessSaved(aPath, hasA, savedA)
		}
		compareAsyncMu.Lock()
		if g != atomic.LoadInt64(&compareGeomGen) || owner != compareHwnd {
			compareAsyncMu.Unlock()
			setCompareBusy(-1)
			return
		}
		comparePendingGeom = r
		compareAsyncMu.Unlock()
		if compareHwnd != 0 {
			pPostMessageW.Call(compareHwnd, WM_COMPARE_GEOM_READY, 0, 0)
		}
	}(compareAPath, compareBPath, gen, compareHwnd)
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
	if r == nil || r.owner != compareHwnd || r.gen != atomic.LoadInt64(&compareGeomGen) {
		return
	}
	defer func() {
		if compareAlgorithmGeometryPending == r.gen {
			compareAlgorithmGeometryPending = 0
		}
		updateCadzowAlgorithmUI()
	}()
	if r.errA != nil {
		compareAutoGeom = r.hasB && r.errB == nil && (compareHeaderMapping{r.b.InlineByte, r.b.CrosslineByte}).valid()
		compareAutoILA, compareAutoXLA = 189, 193
		if compareAutoGeom {
			compareAutoILB, compareAutoXLB = r.b.InlineByte, r.b.CrosslineByte
		}
		setText(cc.ilByte, "189")
		setText(cc.xlByte, "193")
		setText(cc.status, "自动识别失败，将使用当前手动字节位置："+r.errA.Error())
		if r.interactive {
			message(compareHwnd, "自动识别失败", "A 的 Inline/Crossline 自动识别失败。\n\n请修改 IL byte / XL byte 后点击“应用几何”。\n\n原因："+r.errA.Error(), MB_OK|MB_ICONINFORMATION)
		}
		if compareAPath != "" {
			startPrepareTimeCompare()
		}
		return
	}
	if r.a.Ambiguous {
		setText(cc.status, fmt.Sprintf("A 自动识别候选 IL/XL=%d/%d，score %.1f，置信度%s；建议核对或使用 IL/XL交换。", r.a.InlineByte, r.a.CrosslineByte, r.a.Score, confidenceName(r.a.Confidence, true)))
	}
	compareAutoILA, compareAutoXLA = r.a.InlineByte, r.a.CrosslineByte
	compareAutoILB, compareAutoXLB = 189, 193
	if r.hasB {
		if r.errB != nil {
			// A and independently loaded B never silently share header mappings.
			compareAutoILB, compareAutoXLB = 189, 193
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
			msg += fmt.Sprintf("   B: 识别失败，试用默认字节 %d/%d", compareAutoILB, compareAutoXLB)
		} else {
			msg += fmt.Sprintf("   B: %d/%d | score %.1f | 置信度%s", r.b.InlineByte, r.b.CrosslineByte, r.b.Score, confidenceName(r.b.Confidence, r.b.Ambiguous))
			if r.a.InlineByte != r.b.InlineByte || r.a.CrosslineByte != r.b.CrosslineByte {
				msg += " | A/B 字节位置不同，" + APP_NAME + " 将分别使用各自结果"
			}
		}
	}
	setText(cc.status, msg)
	bWarn := r.hasB && r.errB == nil && (r.b.Ambiguous || r.b.LabelsHeuristic)
	if r.interactive {
		note := "已应用识别结果。如方向反了，请修改 IL byte / XL byte 或点击“IL/XL交换”后再应用几何。"
		if r.a.Ambiguous || r.a.LabelsHeuristic || bWarn {
			note = "注意：自动识别置信度有限，请核对 IL/XL byte；方向反了可点击“IL/XL交换”。"
		}
		message(compareHwnd, "自动识别结果", msg+"\n\n"+note, MB_OK|MB_ICONINFORMATION)
	}
	invalidateCompareTimeData()
	if compareAPath != "" {
		startPrepareTimeCompare()
	}
}

func swapCompareGeometryBytes() {
	a, b, ok := compareEffectiveHeaderMappings()
	if !ok || compareTD == nil {
		return
	}
	startPrepareTimeCompareMapping(&compareGeometryRebuild{aPath: compareAPath, bPath: compareBPath, a: a.swap(), b: b, swapA: true})
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
	startPrepareTimeCompareMapping(nil)
}

func startPrepareTimeCompareMapping(mapping *compareGeometryRebuild) {
	cancelPoststackComparePrepare()
	if d := activeCompareGeometryDialog; d != nil && d.plan != nil && d.plan != mapping {
		completeCompareGeometryDialog(d.plan, fmt.Errorf("此几何任务已被更新的操作替代；未应用旧草稿"))
	}
	if compareAPath == "" {
		setText(cc.status, "请先打开 A 数据。")
		return
	}
	ilA, xlA, ilB, xlB, okBytes := compareGeometryBytesForFiles()
	if mapping != nil {
		ilA, xlA, ilB, xlB, okBytes = mapping.a.il, mapping.a.xl, mapping.b.il, mapping.b.xl, mapping.a.valid() && mapping.b.valid()
		atomic.AddInt64(&compareGeomGen, 1)
		compareAlgorithmGeometryPending = 0
		cancelPoststackCompareExport()
		cancelCadzowTask()
	}
	if !okBytes {
		finishPoststackGeneratedBDisplay(fmt.Errorf("当前道头映射无效，无法加载 B 剖面"))
		return
	}
	hasB := compareHasB()
	_, hasAppliedMappingA := compareSourceMapping(compareAPath, compareGeometrySourceA)
	_, hasAppliedMappingB := compareSourceMapping(compareBPath, compareGeometrySourceB)
	useExportTraceOrderA, useExportTraceOrderB := mapping == nil && !hasAppliedMappingA, mapping == nil && !hasAppliedMappingB
	// Snapshot the physical selection accepted by the legacy Open dialog. The
	// worker must use this immutable range when it creates virtual 2-D geometry;
	// otherwise the fallback silently rebuilds the entire file and ignores
	// “终止道”.
	selectedStart, selectedEnd, selectedStep := compareTraceStart, compareTraceEnd, compareTraceStep
	gen := atomic.AddInt64(&compareGen, 1)
	ctx, cancel := context.WithCancel(context.Background())
	comparePrepareCancel = cancel
	updateCadzowAlgorithmUI()
	setCompareBusy(1)
	setCompareProgress(15, true)
	if hasB {
		setText(cc.status, fmt.Sprintf("正在建立/读取几何索引并准备 A/B | A %d/%d, B %d/%d...", ilA, xlA, ilB, xlB))
		invalidateCompareBase()
	} else {
		setText(cc.status, fmt.Sprintf("正在建立/读取 A 的 Inline–Crossline 几何索引 | %d/%d...", ilA, xlA))
	}
	go func(aPath, bPath string, g int64, ilA, xlA, ilB, xlB int, hasB, processingWorkspace bool, initialSample int, owner uintptr, selectedStart, selectedEnd, selectedStep int64) {
		defer retryRetiredPoststackResults()
		res := &comparePrepareResult{gen: g, owner: owner, mapping: mapping, aMapping: compareHeaderMapping{ilA, xlA}, bMapping: compareHeaderMapping{ilB, xlB}}
		fa, e := openPoststackCompareSource(aPath)
		if e != nil {
			res.err = e
			postComparePrepare(res)
			return
		}
		var fb *segy.File
		if hasB {
			fb, e = openPoststackCompareSource(bPath)
			if e != nil {
				res.bErr = e
				hasB = false
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
			if ctx.Err() == nil {
				postCompareOwnedProgress(owner, g, pct, false)
			}
		}
		go func() {
			if useExportTraceOrderA && fa.NumericalExportMetadata().TraceOrder {
				cha <- giRes{}
				return
			}
			var x *segy.GeometryIndex
			var st segy.GeometryBuildStats
			var e error
			x, st, e = fa.BuildPoststackSectionGeometryIndexContextProgress(ctx, ilA, xlA, 0, func(d, t int) { reportProgress(0, d, t) })
			cha <- giRes{x, st, e}
		}()
		var chb chan giRes
		if hasB {
			chb = make(chan giRes, 1)
			go func() {
				if generatedPoststackTraceOrder(bPath) != nil {
					// Generated sections use A's exact geometry below. Never build
					// or persist a synthetic geometry cache for an artifact.
					chb <- giRes{}
					return
				}
				if useExportTraceOrderB && fb.NumericalExportMetadata().TraceOrder {
					chb <- giRes{}
					return
				}
				var x *segy.GeometryIndex
				var st segy.GeometryBuildStats
				var e error
				x, st, e = fb.BuildPoststackSectionGeometryIndexContextProgress(ctx, ilB, xlB, 0, func(d, t int) { reportProgress(1, d, t) })
				chb <- giRes{x, st, e}
			}()
		}
		ra := <-cha
		var rb giRes
		if hasB {
			rb = <-chb
		}
		if ctx.Err() != nil {
			_ = fa.Close()
			if fb != nil {
				_ = fb.Close()
			}
			setCompareBusy(-1)
			return
		}
		traceOrder := false
		if mapping != nil && ra.idx != nil && ra.idx.DuplicateCount > 0 {
			ra.err = fmt.Errorf("A 有 %d 个重复 IL/XL 位置；原几何保持不变", ra.idx.DuplicateCount)
		}
		if mapping == nil && (processingWorkspace || !hasB) && (ra.err != nil || ra.idx == nil || !ra.idx.Poststack) && ctx.Err() == nil {
			selected := poststack.TraceIndicesForRange(fa.Info.TraceCount, selectedStart, selectedEnd, selectedStep)
			if len(selected) == 0 {
				ra.err = fmt.Errorf("叠后道范围无有效 trace：%d..%d step %d", selectedStart+1, selectedEnd+1, selectedStep)
			} else {
				ra.idx, ra.err = poststack.TraceOrderSourceGeometry(aPath, selected)
			}
			traceOrder = ra.err == nil
		}
		if ra.err != nil || ra.idx == nil || !ra.idx.Poststack {
			fa.Close()
			if fb != nil {
				fb.Close()
			}
			res.err = ra.err
			if res.err == nil {
				res.err = fmt.Errorf("没有可用的叠后三维几何；点击 比 可按当前道序作为单线处理")
			}
			postComparePrepare(res)
			return
		}
		generatedBSelection := generatedPoststackTraceOrder(bPath)
		bTraceOrder := false
		if hasB && generatedBSelection != nil && ra.idx != nil && ra.idx.Poststack {
			// Generated processing outputs retain A's geometry metadata
			// semantically, but their physical B trace numbers are compacted.
			// Project them onto A's real IL/XL coordinates instead of creating
			// a virtual Inline=1 geometry that disables Delta.
			rb.idx, rb.err = poststack.GeometryForTraceSelection(bPath, ra.idx, generatedBSelection)
		} else if mapping == nil && hasB && processingWorkspace && (rb.err != nil || rb.idx == nil || !rb.idx.Poststack) {
			selectedB := generatedBSelection
			if selectedB == nil {
				selectedB = poststack.TraceIndicesForRange(fb.Info.TraceCount, selectedStart, selectedEnd, selectedStep)
			}
			if len(selectedB) == 0 {
				rb.err = fmt.Errorf("B 叠后道范围无有效 trace：%d..%d step %d", selectedStart+1, selectedEnd+1, selectedStep)
			} else {
				rb.idx, rb.err = poststack.TraceOrderGeometry(bPath, int64(len(selectedB)), selectedB)
				bTraceOrder = rb.err == nil
			}
		}
		if mapping != nil && rb.idx != nil && rb.idx.DuplicateCount > 0 {
			rb.err = fmt.Errorf("B 有 %d 个重复 IL/XL 位置；原几何保持不变", rb.idx.DuplicateCount)
		}
		if hasB && (rb.err != nil || rb.idx == nil || !rb.idx.Poststack) {
			res.bErr = rb.err
			if res.bErr == nil {
				res.bErr = fmt.Errorf("B 没有可用的叠后几何")
			}
			_ = fb.Close()
			fb = nil
			rb.idx = nil
			hasB = false
		}
		ca := segy.NewTimeSliceCache(fa, ra.idx)
		var cb *segy.TimeSliceCache
		if hasB {
			cb = segy.NewTimeSliceCache(fb, rb.idx)
		}
		minA, maxA := 0, fa.Info.SamplesPerTrace-1
		if hasB {
			if dtA, dtB := int64(fa.Info.SampleIntervalUS), int64(fb.Info.SampleIntervalUS); dtA > 0 && dtB > 0 {
				m := int((int64(fb.Info.SamplesPerTrace-1) * dtB) / dtA)
				if offset, known := generatedPoststackSourceStart(bPath); known {
					m += offset
					minA = offset
				}
				if m < maxA {
					maxA = m
				}
			}
		}
		init := initialSample
		if init < minA {
			init = minA
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
		postCompareOwnedProgress(owner, g, 92, false)
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
		if traceOrder {
			status = "单线道序（虚拟显示坐标，不是原始 IL/XL） | " + status
		}
		d := &compareTimeData{fa: fa, fb: fb, ga: ra.idx, gb: rb.idx, ca: ca, cb: cb, bounds: fullBounds, fullBounds: fullBounds, minSampleA: minA, maxSampleA: maxA, sampleA: init, sampleB: sampleB, indexStatus: status,
			traceOrderFallback: traceOrder, bTraceOrderFallback: bTraceOrder}
		res.data, res.valsA, res.valsB = d, va, vb
		postComparePrepare(res)
	}(compareAPath, compareBPath, gen, ilA, xlA, ilB, xlB, hasB, compareWorkspaceActive, compareSampleStart, compareHwnd, selectedStart, selectedEnd, selectedStep)
}

func postComparePrepare(r *comparePrepareResult) {
	previous, accepted := queueComparePrepareResult(r)
	if !accepted {
		if r != nil && r.data != nil {
			r.data.close()
		}
		setCompareBusy(-1)
		return
	}
	if previous != nil {
		if previous.data != nil {
			previous.data.close()
		}
		setCompareBusy(-1)
	}
	posted, _, _ := pPostMessageW.Call(r.owner, WM_COMPARE_READY, 0, 0)
	if posted == 0 {
		compareAsyncMu.Lock()
		owned := comparePendingPrepare == r
		if owned {
			comparePendingPrepare = nil
		}
		compareAsyncMu.Unlock()
		if owned {
			if r.data != nil {
				r.data.close()
			}
			setCompareBusy(-1)
		}
	}
}

func queueComparePrepareResult(r *comparePrepareResult) (*comparePrepareResult, bool) {
	compareAsyncMu.Lock()
	defer compareAsyncMu.Unlock()
	// Validate inside the mailbox lock. An old worker must not overwrite a
	// newer result after it was suspended between validation and publishing.
	if r == nil || !compareProgressTokenCurrent(r.owner, compareHwnd, r.gen, atomic.LoadInt64(&compareGen)) {
		return nil, false
	}
	previous := comparePendingPrepare
	comparePendingPrepare = r
	return previous, true
}

func postCompareSlice(r *compareSliceResult) {
	previous, accepted := queueCompareSliceResult(r)
	if !accepted {
		setCompareBusy(-1)
		return
	}
	if previous != nil {
		setCompareBusy(-1)
	}
	posted, _, _ := pPostMessageW.Call(r.owner, WM_COMPARE_SLICE_READY, 0, 0)
	if posted == 0 {
		compareAsyncMu.Lock()
		owned := comparePendingSlice == r
		if owned {
			comparePendingSlice = nil
		}
		compareAsyncMu.Unlock()
		if owned {
			setCompareBusy(-1)
		}
	}
}

func queueCompareSliceResult(r *compareSliceResult) (*compareSliceResult, bool) {
	compareAsyncMu.Lock()
	defer compareAsyncMu.Unlock()
	if r == nil || !compareProgressTokenCurrent(r.owner, compareHwnd, r.gen, atomic.LoadInt64(&compareSliceGen)) {
		return nil, false
	}
	previous := comparePendingSlice
	comparePendingSlice = r
	return previous, true
}

func sampleBForA(sampleA int, fa, fb *segy.File) int {
	if offset, known := generatedPoststackSourceStart(fb.Info.Path); known {
		sampleA -= offset
	}
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
	cancelPoststackViewFilter()
	syncPoststackSpectrumControls()
	if cadzowCancel != nil {
		cancelCadzowTask()
	}
	cancelPoststackCompareExport()
	d := compareTD
	if d == nil {
		return
	}
	if sampleA < d.minSampleA {
		sampleA = d.minSampleA
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
	go func(ca, cb *segy.TimeSliceCache, sa, sb int, g int64, hasB bool, owner uintptr) {
		r := &compareSliceResult{gen: g, owner: owner, sampleA: sa, sampleB: sb}
		var ea, eb error
		if hasB {
			var wg sync.WaitGroup
			wg.Add(2)
			go func() { defer wg.Done(); r.valsA, r.statA, ea = ca.GetSlice(sa) }()
			go func() { defer wg.Done(); r.valsB, r.statB, eb = cb.GetSlice(sb) }()
			wg.Wait()
		} else {
			r.valsA, r.statA, ea = ca.GetSlice(sa)
		}
		if ea != nil {
			r.err = ea
		} else if eb != nil {
			r.err = eb
		}
		postCompareSlice(r)
	}(d.ca, d.cb, sampleA, sampleB, gen, hasB, compareHwnd)
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
	compareDeltaReason = ""
	d := compareTD
	if d == nil || len(valsA) == 0 {
		return
	}
	// Slice-cache publishes copied, immutable value vectors. Keep those vectors
	// immutable here as well so background statistics can safely retain them.
	d.currentValsA, d.currentValsB = valsA, valsB
	r := comparePanelRectsAll()
	if len(r) < 1 {
		return
	}
	aw, ah := rectWH(r[0])
	aw = minInt(maxInt(aw, 2), 900)
	ah = minInt(maxInt(ah, 2), 900)
	ra, ma, ea := d.ga.RasterizeTimeSliceDisplay(valsA, aw, ah, d.bounds)
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
		rb, mb, eb = d.gb.RasterizeTimeSliceDisplay(valsB, bw, bh, d.bounds)
		if eb != nil {
			message(compareHwnd, "Time Slice", eb.Error(), MB_OK|MB_ICONERROR)
			return
		}
	}
	lo, hi := sharedSliceRange(ra, ma, rb, mb)
	compareMapMin, compareMapMax = lo, hi
	compareA = comparePanel{bgra: rasterToBGRA(ra, ma, lo, hi), w: aw, h: ah, title: "A  " + shortPath(compareAPath, 42)}
	seismicSetSliceValues(&compareA, ra, ma, lo, hi)
	compareB = comparePanel{}
	compareD = comparePanel{}
	if hasB {
		bw, bh := rectWH(r[1])
		bw = minInt(maxInt(bw, 2), 900)
		bh = minInt(maxInt(bh, 2), 900)
		compareB = comparePanel{bgra: rasterToBGRA(rb, mb, lo, hi), w: bw, h: bh, title: "B  " + shortPath(compareBPath, 42)}
		seismicSetSliceValues(&compareB, rb, mb, lo, hi)
		if compareShowDiff && len(r) >= 3 {
			if compareWorkspaceActive {
				if err := poststackTimeSliceDifferenceCompatibility(d); err != nil {
					compareDeltaReason = err.Error()
				}
			}
			dw, dh := rectWH(r[2])
			dw = minInt(maxInt(dw, 2), 900)
			dh = minInt(maxInt(dh, 2), 900)
			if compareDeltaReason == "" {
				diff := seismicTimeSliceDifference(d.ga, d.gb, valsA, valsB, d.bounds)
				rd, md, err := d.ga.RasterizeTimeSliceDisplay(diff, dw, dh, d.bounds)
				if err != nil {
					compareDeltaReason = err.Error()
				} else {
					dlo, dhi := symmetricResidualRange(rd, md)
					compareD = comparePanel{bgra: rasterToBGRA(rd, md, dlo, dhi), w: dw, h: dh, title: fmt.Sprintf("差  A-B  [%.4g, %.4g]", dlo, dhi)}
					seismicSetSliceValues(&compareD, rd, md, dlo, dhi)
				}
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
			if compareDeltaReason != "" {
				extra = " | 差已禁用：" + compareDeltaReason
			}
		}
		setText(cc.status, fmt.Sprintf("Time Slice %s | shared display %.5g..%.5g | 增益 %.0f%% | %s%s | %s", formatAdaptiveTimeMS(tm), lo, hi, gainPercent, paletteNames[comparePaletteIndex], extra, d.indexStatus))
	} else {
		hint := "点击 比 进入叠后 Compare"
		if compareWorkspaceActive {
			hint = "点击 加载 B 或 算法处理…"
		}
		setText(cc.status, fmt.Sprintf("A 浏览 | Time Slice %s | display %.5g..%.5g | 增益 %.0f%% | %s | %s", formatAdaptiveTimeMS(tm), lo, hi, gainPercent, paletteNames[comparePaletteIndex], hint))
	}
	seismicRefreshCompareColors()
	invalidateCompareBase()
	syncPoststackParameterContext()
	refreshViewFilterSpectrum()
}

func poststackTimeSliceDifferenceCompatibility(d *compareTimeData) error {
	if d == nil || d.fa == nil || d.fb == nil || d.ga == nil || d.gb == nil {
		return fmt.Errorf("A/B 尚未就绪")
	}
	offset, generated := generatedPoststackSourceStart(d.fb.Info.Path)
	if !generated && d.fa.Info.SamplesPerTrace != d.fb.Info.SamplesPerTrace {
		return fmt.Errorf("SampleCount 不兼容")
	}
	var a, b []int64
	for i, trace := range d.ga.TraceNumbers {
		if i >= len(d.ga.RowOfTrace) || i >= len(d.ga.ColOfTrace) {
			continue
		}
		row, col := int(d.ga.RowOfTrace[i]), int(d.ga.ColOfTrace[i])
		if row < 0 || col < 0 || row >= len(d.ga.InlineValues) || col >= len(d.ga.CrosslineValues) {
			continue
		}
		il, xl := d.ga.InlineValues[row], d.ga.CrosslineValues[col]
		if il < d.bounds.InlineMin || il > d.bounds.InlineMax || xl < d.bounds.CrosslineMin || xl > d.bounds.CrosslineMax {
			continue
		}
		if bt, ok := d.gb.TraceAt(il, xl); ok {
			a = append(a, trace)
			b = append(b, bt)
		}
	}
	return segy.ValidateDifferenceWindow(d.fa, d.fb, a, b, d.sampleA, d.sampleA, offset)
}

func handlePrepareReady() {
	compareAsyncMu.Lock()
	r := comparePendingPrepare
	comparePendingPrepare = nil
	compareAsyncMu.Unlock()
	if r == nil {
		return
	}
	defer setCompareBusy(-1)
	if r.owner != compareHwnd || r.gen != atomic.LoadInt64(&compareGen) {
		if r.data != nil {
			r.data.close()
		}
		return
	}
	if comparePrepareCancel != nil {
		comparePrepareCancel()
		comparePrepareCancel = nil
	}
	defer updateCadzowAlgorithmUI()
	if r.mapping != nil {
		if r.mapping.aPath != compareAPath || r.mapping.bPath != compareBPath {
			if r.data != nil {
				r.data.close()
			}
			completeCompareGeometryDialog(r.mapping, fmt.Errorf("数据源已变化；未应用几何"))
			return
		}
		if r.err != nil || r.bErr != nil || r.data == nil {
			if r.data != nil {
				r.data.close()
			}
			err := r.err
			if err == nil {
				err = r.bErr
			}
			if err == nil {
				err = fmt.Errorf("未取得完整几何索引")
			}
			setCompareProgress(0, false)
			setText(cc.status, "几何未应用；原 A/B 保持不变："+err.Error())
			completeCompareGeometryDialog(r.mapping, err)
			return
		}
	}
	if r.err != nil {
		setCompareProgress(0, false)
		setText(cc.status, "Time Slice: "+r.err.Error())
		finishPoststackGeneratedBDisplay(r.err)
		invalidateCompareBase()
		message(compareHwnd, "Time Slice", r.err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	ilBefore, xlBefore, physical, hadTrace := int32(0), int32(0), int64(0), false
	geometryChanged := r.mapping != nil && compareTD != nil && compareTD.ga != nil &&
		(compareTD.ga.InlineByte != r.aMapping.il || compareTD.ga.CrosslineByte != r.aMapping.xl)
	if geometryChanged {
		ilBefore, xlBefore, physical, hadTrace = compareCurrentPosition(compareTD)
		cancelPoststackViewFilter()
		atomic.AddInt64(&compareSliceGen, 1)
	}
	hadData := compareTD != nil
	if compareTD != nil {
		if r.mapping != nil && r.data != nil && compareMode == 2 && (!geometryChanged || r.mapping.swapA) {
			before := compareTD.bounds
			if geometryChanged && r.mapping.swapA {
				before.InlineMin, before.InlineMax, before.CrosslineMin, before.CrosslineMax = before.CrosslineMin, before.CrosslineMax, before.InlineMin, before.InlineMax
			}
			r.data.bounds = before
		}
		compareTD.close()
	}
	compareTD = r.data
	if compareTD != nil && !compareTD.traceOrderFallback {
		saveCompareSourceMapping(&compareGeometrySourceA, compareAPath, r.aMapping)
		setText(cc.ilByte, strconv.Itoa(r.aMapping.il))
		setText(cc.xlByte, strconv.Itoa(r.aMapping.xl))
	}
	if compareTD != nil && compareTD.gb != nil && generatedPoststackTraceOrder(compareBPath) == nil &&
		compareTD.gb.InlineByte == r.bMapping.il && compareTD.gb.CrosslineByte == r.bMapping.xl {
		saveCompareSourceMapping(&compareGeometrySourceB, compareBPath, r.bMapping)
	}
	var relocation string
	if geometryChanged {
		il, xl, ok := compareGeometryAtTrace(compareTD.ga, physical)
		if !hadTrace {
			ok = false
		}
		if r.mapping.swapA && compareMode != 2 {
			// The same physical profile becomes the opposite direction when
			// the complete interpretation is swapped.
			compareMode = 1 - compareMode
			pSendMessageW.Call(cc.mode, CB_SETCURSEL, uintptr(compareMode), 0)
		}
		compareZoomToggleValid = false
		compareCrosshairValid = compareCrosshairValid && ok
		if ok {
			switch compareMode {
			case 0:
				compareLineCoord, compareCrosshairWorldX = il, float64(xl)
			case 1:
				compareLineCoord, compareCrosshairWorldX = xl, float64(il)
			default:
				compareCrosshairWorldX, compareCrosshairWorldY = float64(xl), float64(il)
			}
			relocation = fmt.Sprintf("保留源道 %d；IL/XL %d/%d → %d/%d", physical+1, ilBefore, xlBefore, il, xl)
		} else if hadTrace {
			relocation = fmt.Sprintf("源道 %d 在新映射下无有效位置，已清除原十字位置", physical+1)
		}
	}
	if r.bErr != nil {
		finishPoststackGeneratedBDisplay(r.bErr)
		invalidatePoststackViewFilterB()
		compareBPath = ""
		compareBVisible, compareShowDiff = false, false
		compareB, compareD = comparePanel{}, comparePanel{}
		layoutComparePathLabels()
	}
	if compareTD.traceOrderFallback {
		compareMode = 0
		pSendMessageW.Call(cc.mode, CB_SETCURSEL, 0, 0)
	}
	configureCompareModeSlider()
	if compareMode != 2 {
		if (r.mapping == nil && !compareWorkspaceActive) || (!hadData && compareLineXMin == 0 && compareLineXMax == 0) ||
			compareLineXMax < compareLineXMin || (geometryChanged && !r.mapping.swapA) {
			resetCompareLineBounds()
		}
	}
	if compareMode == 2 {
		// Slab I/O is intentionally deferred until Time Slice is visible.
		startLoadTimeSlice(compareTD.sampleA)
	} else {
		renderCompareLine()
	}
	setCompareProgress(100, false)
	if r.bErr != nil {
		setText(cc.status, "B 加载失败；A 保持可浏览："+r.bErr.Error())
	} else if compareWorkspaceActive && compareActiveB() && compareMode != 2 {
		if len(compareB.bgra) == 0 && !viewFilterIsPending() {
			finishPoststackGeneratedBDisplay(fmt.Errorf("当前范围未生成 B 图像：%s", getText(cc.status)))
		} else if len(compareB.bgra) != 0 {
			finishPoststackGeneratedBDisplay(nil)
		}
		invalidateCompareBase()
	}
	if r.mapping != nil {
		note := "几何已应用"
		if relocation != "" {
			note += "；" + relocation
		}
		setText(cc.status, note)
		completeCompareGeometryDialog(r.mapping, nil)
	}
}

func handleSliceReady() {
	compareAsyncMu.Lock()
	r := comparePendingSlice
	comparePendingSlice = nil
	compareAsyncMu.Unlock()
	if r == nil {
		return
	}
	defer setCompareBusy(-1)
	if compareTD == nil {
		return
	}
	if r.owner != compareHwnd || r.gen != atomic.LoadInt64(&compareSliceGen) {
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
		setText(cc.status, fmt.Sprintf("Time Slice %s | %s | A %.0f ms | slab %d samples | 点击 加载 B", formatAdaptiveTimeMS(tm), hit, r.statA.Duration.Seconds()*1000, r.statA.BlockSamples))
	} else {
		setText(cc.status, fmt.Sprintf("Time Slice %s | %s | A %.0f ms, B %.0f ms | slab %d/%d samples", formatAdaptiveTimeMS(tm), hit, r.statA.Duration.Seconds()*1000, r.statB.Duration.Seconds()*1000, r.statA.BlockSamples, r.statB.BlockSamples))
	}
	prefetchAround(compareTD, r.sampleA, r.sampleB)
}

func paintSeismicComparePanel(dc uintptr, r RECT, p comparePanel, panel int, register bool) {
	if len(p.bgra) == 0 || p.w < 1 || p.h < 1 {
		return
	}
	if register {
		p = seismicRegisterCompare(r, p, panel)
	}
	rx, ry := seismicCompareReverse(panel)
	raster := seismicCompareRasterRect(panel, r)
	if !paintPoststackWaveforms(dc, r, raster, p, panel, rx, ry) && !displayPaintCompareDistance(dc, raster, p, panel, rx, ry) {
		seismicPlot(dc, raster, p.bgra, p.w, p.h, rx, ry)
	}
	if !seismicCompareEqualTicks() {
		m := displayMetrics(dc, displayDPI(compareHwnd))
		gap := displayPixelDIP(5, m.DPI)
		drawAxisText(dc, p.title, int(r.Left), int(r.Top)-m.Height-gap, int(r.Right), int(r.Top)-gap, DT_CENTER)
	}
	seismicCompareDecorate(dc, r, p, panel)
}

func compareWorldAtPixel(panel, px, py int) (float64, float64, bool) {
	if panel < 0 || panel >= comparePanelCount() {
		return 0, 0, false
	}
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
	rx, ry := seismicCompareReverse(panel)
	if rx {
		fx = 1 - fx
	}
	if ry {
		fy = 1 - fy
	}
	if compareMode != 2 {
		x0, x1 := viewFilterPanelRange(panel)
		first, last := seismicPanelTimeWindow(panel)
		x := x0 + fx*(x1-x0)
		worldPanel := panel
		if poststackWaveSharedHorizontal(panel) {
			plot := comparePanelRect(panel)
			fx = float64(px-int(plot.Left)) / float64(max(1, int(plot.Right-plot.Left)-1))
			if rx {
				fx = 1 - fx
			}
			worldPanel = 0
		}
		if n, measured := displayCompareNativeAtFraction(worldPanel, fx); measured {
			x = n
		} else if p := seismicComparePanel(worldPanel); p != nil && len(p.coords) > 0 {
			x = seismicAcrossAt(p.coords, fx)
		}
		y := float64(first) + fy*float64(maxInt(last-first, 0))
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
	sourceSampleOffset := 0
	if role == "B" {
		if offset, known := generatedPoststackSourceStart(file.Info.Path); known {
			if isPoststackArtifactResult(file.Info.Path) {
				sourceSampleOffset = offset
			}
			sampleStart -= offset
			sampleEnd -= offset
			if compareMode != 2 {
				marker -= offset
			}
		}
	}
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
		MarkerSample: marker, SourceSampleOffset: sourceSampleOffset, Inline: inline, Crossline: crossline, HasGeometry: true}, nil
}

func compareLineSelectionForWorld(panel int, worldX, worldY float64) (traceAnalysisSelection, error) {
	d := compareTD
	if d == nil || d.fa == nil || d.ga == nil {
		return traceAnalysisSelection{}, fmt.Errorf("二维几何尚未就绪")
	}
	marker := int(math.Round(worldY))
	if panel == 1 && committedViewFilter() == nil && d.fb != nil {
		// B's unfiltered raster reports a B-local sample. The legacy
		// inspection target subtracts the generated offset itself.
		if offset, ok := generatedPoststackSourceStart(d.fb.Info.Path); ok {
			marker += offset
		}
	}
	line := compareLineCoord
	lineTargets := func(file *segy.File, geometry *segy.GeometryIndex) ([]int64, []int32, int32, error) {
		if file == nil || geometry == nil {
			return nil, nil, 0, fmt.Errorf("数据几何尚未就绪")
		}
		traces, coords, actual, err := geometry.LineTraceNumbers(compareMode, line)
		if err != nil {
			return nil, nil, 0, err
		}
		if compareWorkspaceActive {
			start, end, step := compareTraceRangeForGeometry(file)
			if end < start {
				return nil, nil, actual, nil
			}
			traces, coords = filterPhysicalTraceRange(traces, coords, start, end, step)
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
		if !compareDifferenceGeometryKnown(d) {
			return traceAnalysisSelection{}, fmt.Errorf("独立 A/B 缺少可核实的共同几何")
		}
		if s := committedViewFilter(); s != nil && s.deltaReason != "" {
			return traceAnalysisSelection{}, fmt.Errorf("差值不可用：%s", s.deltaReason)
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
	var selection traceAnalysisSelection
	var err error
	if compareMode == 0 || compareMode == 1 {
		selection, err = compareLineSelectionForWorld(panel, worldX, worldY)
	} else if compareMode == 2 {
		selection, err = compareTimeSliceSelectionForWorld(panel, worldX, worldY)
	} else {
		return traceAnalysisSelection{}, fmt.Errorf("不支持的二维显示方向")
	}
	if err == nil {
		selection = attachViewFilterTraceSelection(selection)
		if compareMode != 2 {
			selection.Anchor = tracePoststackAnchorFor(selection, panel)
			// The picked Y is local to the visible panel; it cannot be
			// interpreted as an A sample when B starts at a different time.
			// Use the same physical mapping as the panel cursor readout.
			p := seismicComparePanel(panel)
			if p != nil && (p.viewAxes || p.sampleDtUS > 0) {
				t := seismicCompareTimeMS(panel, worldY) * 1000
				if !math.IsNaN(t) && !math.IsInf(t, 0) {
					selection.Anchor.MarkerTimeUS, selection.Anchor.HasMarkerTime = t, true
				}
			}
		}
	}
	return selection, err
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
		rx, ry := seismicCompareReverse(panel)
		if rx {
			x = int(r.Left+r.Right) - 1 - x
		}
		if ry {
			y = int(r.Top+r.Bottom) - 1 - y
		}
		step := compareTraceStep
		if step < 1 {
			step = 1
		}
		count := (compareTraceEnd-compareTraceStart)/step + 1
		if count < 1 {
			return traceAnalysisSelection{}, fmt.Errorf("当前二维剖面没有有效道范围")
		}
		f := float64(x-int(r.Left)) / float64(maxInt(rw-1, 1))
		rank := f * float64(count-1)
		if native, measured := displayCompareNativeAtFraction(panel, f); measured {
			rank = (native - float64(compareTraceStart+1)) / float64(step)
		}
		column := int64(math.Round(rank))
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
	if compareMode != 2 && !displayCompareDistancePixelAllowed(panel, worldX) {
		return traceAnalysisSelection{}, fmt.Errorf("该距离位置没有有效地震道")
	}
	if !poststackWaveCoordinatePresent(panel, worldX) {
		return traceAnalysisSelection{}, fmt.Errorf("所选共同位置没有该面板的有效道")
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
	plotX, xWidth := int(r.Left), rw
	if compareMode != 2 {
		x0, x1 := viewFilterPanelRange(panel)
		first, last := seismicPanelTimeWindow(panel)
		denx := x1 - x0
		deny := float64(last - first)
		if denx < 0 || deny < 0 {
			return 0, 0, false
		}
		if denx == 0 {
			if x != x0 {
				return 0, 0, false
			}
			fx = 0.5
		} else {
			fx = (x - x0) / denx
		}
		worldPanel := panel
		if poststackWaveSharedHorizontal(panel) {
			if !poststackWaveCoordinatePresent(panel, x) {
				return 0, 0, false
			}
			plot := comparePanelRect(panel)
			plotX, xWidth = int(plot.Left), int(plot.Right-plot.Left)
			worldPanel = 0
		}
		if f, measured := displayCompareDistanceFraction(worldPanel, x); measured {
			fx = f
		} else if p := seismicComparePanel(worldPanel); p != nil && len(p.coords) > 0 {
			var ok bool
			fx, ok = seismicAcrossFraction(p.coords, x)
			if !ok {
				return 0, 0, false
			}
		}
		localY := seismicPanelLocalSample(panel, y)
		if deny == 0 {
			if localY != float64(first) {
				return 0, 0, false
			}
			fy = 0.5
		} else {
			fy = (localY - float64(first)) / deny
		}
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
	rx, ry := seismicCompareReverse(panel)
	if rx {
		fx = 1 - fx
	}
	if ry {
		fy = 1 - fy
	}
	px := plotX + int(math.Round(fx*float64(xWidth-1)))
	py := int(r.Top) + int(math.Round(fy*float64(rh-1)))
	return px, py, true
}

func showCompareTraceSelectionAt(panel, x, y int) {
	selection, err := compareTraceSelectionAt(panel, x, y)
	if err != nil {
		setText(cc.status, "单道分析："+err.Error())
		return
	}
	if compareMode != 2 {
		anchor := selection.Anchor
		if anchor.Owner == 0 { // 2-D fallback has no indexed Compare panel.
			anchor = tracePoststackAnchorFor(selection, panel)
		}
		if group, ok := resolveTracePoststackAnchor(anchor); ok {
			selection = group
		}
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
	compareAnnotations = append(compareAnnotations, compareAnnotation{Mode: compareMode, Kind: kind, Line: compareLineCoord, Source: compareAPath, Geometry: atomic.LoadInt64(&compareGeomGen),
		X0: wx0, Y0: seismicPanelCanonicalSample(panel, wy0), X1: wx1, Y1: seismicPanelCanonicalSample(panel, wy1)})
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
	paintCompareCommittedAnnotationsOffset(hdc, 0)
}

func paintCompareCommittedAnnotationsOffset(hdc uintptr, dy int) {
	colors := []uintptr{rgbRef(255, 0, 0), rgbRef(0, 180, 0), rgbRef(0, 180, 220)}
	for _, a := range compareAnnotations {
		if a.Mode != compareMode {
			continue
		}
		for panel := 0; panel < comparePanelCount(); panel++ {
			x0, y0, o0 := comparePixelAtWorld(panel, a.X0, a.Y0)
			x1, y1, o1 := comparePixelAtWorld(panel, a.X1, a.Y1)
			if o0 && o1 {
				drawCompareShape(hdc, a.Kind, x0, y0+dy, x1, y1+dy, colors[a.Kind%len(colors)])
			}
		}
	}
}

func paintCompareTransientOverlays(hdc uintptr) {
	// Dynamic overlays must never enter the toolbar/status area. In addition to
	// narrow invalidation rectangles this clip protects against an old cursor
	// or drag rectangle repainting native buttons during a parent WM_PAINT.
	savedDC, _, _ := pSaveDC.Call(hdc)
	if savedDC != 0 {
		client := clientRect(compareHwnd)
		// Keep the whole third toolbar row (the 十字联动/矩形/椭圆/线段
		// controls at y=67..90) outside the transient paint region.  The
		// previous boundary (60) intersected those child buttons whenever a
		// crosshair or annotation changed, making the native buttons visibly
		// flash while the parent was repainted.
		top := int32(94)
		if client.Bottom < top {
			top = client.Bottom
		}
		pIntersectClipRect.Call(hdc, 0, uintptr(top), uintptr(client.Right), uintptr(client.Bottom))
	}
	if savedDC != 0 {
		defer pRestoreDC.Call(hdc, savedDC)
	}
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
		kind := poststackAnnotationKind(compareTool)
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
	compareCrosshairWorldX, compareCrosshairWorldY = wx, seismicPanelCanonicalSample(panel, wy)
	compareCrosshairValid = true

	// Status text is useful but does not need mouse-event frequency. Updating a
	// child STATIC on every WM_MOUSEMOVE can itself cause visible churn, so cap
	// it to ~25 Hz while leaving the crosshair motion unthrottled.
	if poststackTaskBusy() {
		return
	}
	now := time.Now()
	if !compareLastHoverStatus.IsZero() && now.Sub(compareLastHoverStatus) < 40*time.Millisecond {
		return
	}
	compareLastHoverStatus = now
	if compareMode != 2 {
		tm := seismicCompareTimeMS(panel, wy)
		timeLabel := seismicDefaultTimeText(tm)
		if _, ok := displayCommitted(compareHwnd, seismicCompareKey(panel)); ok {
			_, y := seismicCompareAxes(panel)
			timeLabel = seismicAxisTimeText(tm, displayAxisFor(compareHwnd, seismicCompareKey(panel), y))
		}
		distance := displayDistanceStatus(compareHwnd, wx)
		sampleStatus := ""
		if displayCompareDistancePixelAllowed(panel, wx) {
			sampleStatus = viewFilterHover(panel, wx, wy)
		} else {
			sampleStatus = " | 无有效地震道"
		}
		if compareTD == nil || compareTD.traceOrderFallback {
			setText(cc.coordinates, fmt.Sprintf("物理道 %.0f%s | Time %s%s", wx, distance, timeLabel, sampleStatus))
		} else if compareMode == 0 {
			setText(cc.coordinates, fmt.Sprintf("Inline %d | Crossline %.0f%s | Time %s%s", compareLineCoord, wx, distance, timeLabel, sampleStatus))
		} else {
			setText(cc.coordinates, fmt.Sprintf("Inline %.0f | Crossline %d%s | Time %s%s", wx, compareLineCoord, distance, timeLabel, sampleStatus))
		}
	} else {
		tm := 0.0
		if compareTD != nil && compareTD.fa != nil {
			tm = float64(compareTD.sampleA*compareTD.fa.Info.SampleIntervalUS) / 1000.0
		}
		setText(cc.coordinates, fmt.Sprintf("Inline %.0f | Crossline %.0f | Time %s", wy, wx, formatAdaptiveTimeMS(tm)))
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
	displayBeginPaint(compareHwnd)
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
	if frame := committedViewFilter(); frame != nil && len(panels) > 0 {
		drawAxisText(hdc, viewFilterFrameLabel(frame), int(panels[0].Left), 94, int(panels[len(panels)-1].Right), 114, DT_CENTER)
	}
	if len(panels) >= 1 {
		if viewFilterIsPending() && len(compareA.bgra) == 0 {
			paintComparePlaceholder(hdc, panels[0], "A  当前剖面", "正在更新视图滤波", "将同步刷新可比较的剖面。")
		} else {
			paintSeismicComparePanel(hdc, panels[0], compareA, 0, true)
		}
	}
	if len(panels) >= 2 {
		if compareActiveB() && len(compareB.bgra) != 0 {
			paintSeismicComparePanel(hdc, panels[1], compareB, 1, true)
		} else {
			title, detail := compareBPlaceholderText(compareActiveB(), comparePrepareCancel != nil, poststackTaskBusy(), algorithmStatusSource == compareAPath, algorithmStatus)
			paintComparePlaceholder(hdc, panels[1], "B  处理结果 / 对比数据", title, detail)
		}
	}
	if compareShowDiff && len(panels) >= 3 {
		paintSeismicComparePanel(hdc, panels[2], compareD, 2, true)
		if len(compareD.bgra) == 0 && compareDeltaReason != "" {
			r := panels[2]
			r.Top += (r.Bottom - r.Top) / 2
			r.Left += 8
			r.Right -= 8
			text := "差值不可用"
			pDrawTextW.Call(hdc, uintptr(unsafe.Pointer(u16(text))), ^uintptr(0), uintptr(unsafe.Pointer(&r)), DT_CENTER|DT_WORDBREAK|0x800) // DT_NOPREFIX
		} else if len(compareD.bgra) == 0 {
			paintComparePlaceholder(hdc, panels[2], "差  A−B", "等待 A/B 剖面就绪", "两幅剖面加载完成后计算差值。")
		}
	}
	paintCompareCommittedAnnotations(hdc)
}

func compareBPlaceholderText(hasB, preparing, running, currentStatus bool, status string) (string, string) {
	if hasB {
		if preparing {
			return "正在加载 B 剖面…", "A 保持可浏览；加载完成后显示 B。"
		}
		return "B 当前剖面暂无图像", "请检查上方状态，或重新加载 B。"
	}
	if running {
		if !currentStatus || status == "" {
			status = "等待当前处理任务返回结果。"
		}
		return "正在生成 B…", status
	}
	return "B 尚未加载", "点击「加载 B」选择文件，或在「算法处理…」中运行处理。"
}

func paintComparePlaceholder(hdc uintptr, r RECT, panelTitle, title, detail string) {
	if r.Right <= r.Left || r.Bottom <= r.Top {
		return
	}
	drawHomeRoundRect(hdc, r, rgbRef(248, 250, 252), rgbRef(203, 213, 225), 8)
	drawAxisText(hdc, panelTitle, int(r.Left), int(r.Top)-25, int(r.Right), int(r.Top)-5, DT_CENTER)
	y := r.Top + (r.Bottom-r.Top)/2
	drawHomeText(hdc, hFont, rgbRef(30, 41, 59), title,
		RECT{Left: r.Left + 10, Top: y - 23, Right: r.Right - 10, Bottom: y}, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
	drawHomeText(hdc, hFont, rgbRef(71, 85, 105), detail,
		RECT{Left: r.Left + 18, Top: y + 7, Right: r.Right - 18, Bottom: r.Bottom - 8}, DT_CENTER|DT_WORDBREAK|0x800)
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
		invalidateCompareScene(false)
	}
}

func invalidateCompareOverlayAll() {
	if compareHwnd != 0 {
		invalidateCompareScene(false)
	}
}

func invalidateCompareScene(erase bool) {
	if compareHwnd == 0 {
		return
	}
	r := clientRect(compareHwnd)
	// Toolbar controls occupy three rows (the third row contains the linked
	// crosshair and shape tools at y=67..90); the status/progress controls are
	// children as well. Repaint only the axis/canvas area below those controls.
	if r.Bottom <= 94 {
		return
	}
	r.Top = 94
	bg := uintptr(0)
	if erase {
		bg = 1
	}
	pInvalidateRect.Call(compareHwnd, uintptr(unsafe.Pointer(&r)), bg)
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
		displayPaintAxisNavigation(hdc, compareHwnd)
	}
	pEndPaint.Call(compareHwnd, uintptr(unsafe.Pointer(&ps)))
	updateCompareDisplayNotice()
}

func comparePointInRect(x, y int, r RECT) bool {
	return x >= int(r.Left) && x < int(r.Right) && y >= int(r.Top) && y < int(r.Bottom)
}

func compareZoomRectForPanel(panel int) RECT {
	return seismicCompareRasterRect(panel, comparePanelRect(panel))
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
	if !seismicFinite(s.lineXMin) || !seismicFinite(s.lineXMax) || s.lineXMax < s.lineXMin {
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
	rx, ry := seismicCompareReverse(panel)
	if rx {
		fx0, fx1 = 1-fx1, 1-fx0
	}
	if ry {
		fy0, fy1 = 1-fy1, 1-fy0
	}
	if compareMode != 2 {
		oldX0, oldX1 := compareLineXMin, compareLineXMax
		oldS0, oldS1 := compareSampleStart, compareSampleEnd
		fromX, toX := viewFilterPanelRange(panel)
		fromS, toS := seismicPanelTimeWindow(panel)
		compareLineXMin = fromX + fx0*(toX-fromX)
		compareLineXMax = fromX + fx1*(toX-fromX)
		if p := seismicComparePanel(panel); p != nil && len(p.coords) > 0 {
			compareLineXMin, compareLineXMax = seismicAcrossAt(p.coords, fx0), seismicAcrossAt(p.coords, fx1)
		}
		if a, ok := displayCompareNativeAtFraction(panel, fx0); ok {
			if b, ok := displayCompareNativeAtFraction(panel, fx1); ok {
				compareLineXMin, compareLineXMax = a, b
			}
		}
		span := float64(maxInt(toS-fromS, 0))
		compareSampleStart = int(math.Floor(seismicPanelCanonicalSample(panel, float64(fromS)+fy0*span)))
		compareSampleEnd = int(math.Ceil(seismicPanelCanonicalSample(panel, float64(fromS)+fy1*span)))
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

// Right drag commits one data-coordinate pan on mouse-up. The filter is never
// scheduled from mouse motion, and the legacy time-slice pan is not changed.
func applyCompareSectionPan(panel, dx, dy int) {
	if compareTD == nil || compareTD.fa == nil || compareTD.ga == nil || compareMode == 2 || (absInt(dx) < 4 && absInt(dy) < 4) {
		return
	}
	rx, ry := seismicCompareReverse(panel)
	if rx {
		dx = -dx
	}
	if ry {
		dy = -dy
	}
	tr, co, _, err := compareTD.ga.LineTraceNumbers(compareMode, compareLineCoord)
	if err != nil {
		return
	}
	first, last, step := compareTraceRangeForGeometry(compareTD.fa)
	if last < first {
		return
	}
	_, co = filterPhysicalTraceRange(tr, co, first, last, step)
	if len(co) == 0 {
		return
	}
	boundX0, boundX1 := float64(co[0]), float64(co[len(co)-1])
	x0, x1 := viewFilterPanelRange(panel)
	s0, s1 := seismicPanelTimeWindow(panel)
	s0, s1 = int(math.Floor(seismicPanelCanonicalSample(panel, float64(s0)))), int(math.Ceil(seismicPanelCanonicalSample(panel, float64(s1))))
	w, h := rectWH(compareZoomRectForPanel(panel))
	if w < 2 || h < 2 {
		return
	}
	distanceBounds := displayCompareDistanceNativeBounds(0)
	distanceWidth, _ := rectWH(comparePanelRect(panel))
	if a, b, moved := displayCompareDistancePan(distanceBounds.Min, distanceBounds.Max, boundX0, boundX1, dx, distanceWidth); moved {
		x0, x1 = a, b
	} else if span := x1 - x0; span > 0 && span <= boundX1-boundX0 {
		x0 = clampFloat(x0-float64(dx)*span/float64(w-1), boundX0, boundX1-span)
		x1 = x0 + span
	} else {
		x0, x1 = compareLineXMin, compareLineXMax
	}
	boundS0 := maxInt(0, compareOriginSampleStart)
	boundS1 := compareOriginSampleEnd
	if boundS1 < 0 || boundS1 >= compareTD.fa.Info.SamplesPerTrace {
		boundS1 = compareTD.fa.Info.SamplesPerTrace - 1
	}
	if span := s1 - s0; span > 0 && span <= boundS1-boundS0 {
		s0 = clampInt(s0-int(math.Round(float64(dy)*float64(span)/float64(h-1))), boundS0, boundS1-span)
		s1 = s0 + span
	} else {
		s0, s1 = compareSampleStart, compareSampleEnd
	}
	if x0 == compareLineXMin && x1 == compareLineXMax && s0 == compareSampleStart && s1 == compareSampleEnd {
		return
	}
	compareLineXMin, compareLineXMax, compareSampleStart, compareSampleEnd = x0, x1, s0, s1
	compareCrosshairValid, compareZoomToggleValid = false, false
	renderCompareLine()
}

func pSendMessageWCheck(h uintptr) bool {
	r, _, _ := pSendMessageW.Call(h, BM_GETCHECK, 0, 0)
	return r == BST_CHECKED
}

func compareWndProc(h uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case 0x007b: // WM_CONTEXTMENU: copy full A/B path from the toolbar.
		if showPoststackPathMenu(h, wParam, lParam) {
			return 0
		}
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
				if err := saveSeismicCompareBMP(p); err != nil {
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
			if !displayOpenToolbarColor(h, seismicCompareKey(0)) {
				pUpdateWindow.Call(h)
				if !displayOpenToolbarColor(h, seismicCompareKey(0)) {
					setText(cc.status, "请等待剖面绘制完成后打开色标设置。")
				}
			}
		case ID_DATA:
			showCompareDataInfo()
		case ID_PARAMS:
			showParamDialog(h)
		case ID_ZOOM:
			togglePoststackCompareTool(poststackToolZoom)
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
			setPoststackCompareWorkspace(pSendMessageWCheck(cc.compare))
		case ID_VOLUME:
			if isPoststackArtifactResult(compareBPath) {
				message(compareHwnd, "三维体", "算法结果是当前可见剖面，不是完整三维体。请先导出 SEG-Y，再单独打开。", MB_OK|MB_ICONINFORMATION)
				return 0
			}
			showVolumeWindow()
		case IDCMP_EXPORT:
			startExportCurrentSection()
		case IDCMP_SPECTRUM:
			showSpectrumWindow()
		case IDCMP_TRACE_INSPECT:
			togglePoststackCompareTool(poststackToolTrace)
		case IDCMP_A:
			chooseComparePath(0)
		case IDCMP_B:
			if compareWorkspaceActive {
				chooseComparePath(1)
			}
		case IDCMP_CADZOW:
			if compareWorkspaceActive {
				showCadzowDialog()
			}
		case IDCMP_CLOSE_B:
			if compareWorkspaceActive {
				closePoststackCompareB()
			}
		case IDCMP_MODE:
			if (wParam>>16)&0xffff == 1 {
				il, xl, _, located := compareCurrentPosition(compareTD)
				r, _, _ := pSendMessageW.Call(cc.mode, CB_GETCURSEL, 0, 0)
				if r > 2 {
					break
				}
				compareMode = int(r)
				syncPoststackWavePalette()
				compareZoomToggleValid = false
				showTimeControls(true)
				compareCrosshairValid = false
				if located && compareMode != 2 {
					compareLineCoord = il
					if compareMode == 1 {
						compareLineCoord = xl
					}
				}
				if compareTD == nil {
					if compareAutoGeom {
						startPrepareTimeCompare()
					} else if compareAPath != "" {
						startAutoDetectGeometry()
					}
				} else {
					configureCompareModeSlider()
					if compareMode != 2 {
						resetCompareLineBounds()
					}
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
					rank, _ := compareLineRank(compareNavigationLines(compareTD, compareMode), compareLineCoord)
					startLoadComparePosition(rank - 1)
				}
			}
		case IDCMP_NEXT:
			if compareTD != nil {
				if compareMode == 2 {
					startLoadTimeSlice(compareTD.sampleA + 1)
				} else {
					rank, _ := compareLineRank(compareNavigationLines(compareTD, compareMode), compareLineCoord)
					startLoadComparePosition(rank + 1)
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
			if compareTD.ga == nil {
				setText(cc.status, "几何索引尚未准备完成，暂时无法还原。")
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
			showCompareGeometryDialog()
		case IDCMP_SWAPGEOM:
			swapCompareGeometryBytes()
		case IDCMP_PALETTE:
			notify := (wParam >> 16) & 0xffff
			if notify == 1 || notify == 9 { // CBN_SELCHANGE / CBN_SELENDOK
				r, _, _ := pSendMessageW.Call(cc.palette, CB_GETCURSEL, 0, 0)
				if !poststackWavePaletteSelection(int(r)) {
					applyCompareToolbarPalette(int(r))
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
			togglePoststackCompareTool(poststackToolRect)
		case IDCMP_ELLIPSETOOL:
			togglePoststackCompareTool(poststackToolEllipse)
		case IDCMP_LINETOOL:
			togglePoststackCompareTool(poststackToolLine)
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
		if wParam == VK_ESCAPE && displayNavigationCancel(h) {
			return 0
		}
		if wParam == VK_ESCAPE && (compareTool != poststackToolIdle || compareFilterPanDragging) {
			setPoststackCompareTool(poststackToolIdle)
			return 0
		}
	case WM_LBUTTONDOWN:
		mx, my := mousePoint(lParam)
		if displayNavigationBegin(h, mx, my) {
			return 0
		}
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
				if i >= comparePanelCount() {
					break
				}
				if comparePointInRect(mx, my, r) {
					panel = i
					break
				}
			}
			if panel >= 0 {
				numericalExportActivePanel = panel
				pSetFocus.Call(h)
				if compareTool == poststackToolTrace {
					showCompareTraceSelectionAt(panel, mx, my)
					return 0
				}
				if poststackAnnotationKind(compareTool) >= 0 {
					compareAnnotDragging = true
					compareAnnotPanel = panel
					compareAnnotX0, compareAnnotY0, compareAnnotX1, compareAnnotY1 = mx, my, mx, my
				} else if compareTool == poststackToolZoom {
					compareZoomDragging = true
					compareZoomPanel = panel
					compareZoomX0, compareZoomY0, compareZoomX1, compareZoomY1 = mx, my, mx, my
				} else {
					return 0 // Navigation mode: click to focus; do not latch a hidden drag tool.
				}
				pSetCapture.Call(h)
				return 0
			}
		}
	case WM_LBUTTONDBLCLK:
		mx, my := mousePoint(lParam)
		if displayNavigationDoubleClick(h, mx, my) {
			return 0
		}
		if displaySurfaceClick(h, mx, my, false) {
			return 0
		}
		if compareTool == poststackToolTrace || poststackAnnotationKind(compareTool) >= 0 {
			return 0
		}
		for i, r := range comparePanelRectsAll() {
			if i >= comparePanelCount() {
				break
			}
			if comparePointInRect(mx, my, r) {
				toggleCompareZoomSnapshot()
				return 0
			}
		}
	case WM_RBUTTONDOWN:
		if viewFilterWanted() != nil && compareMode != 2 && len(compareA.bgra) > 0 {
			x, y := mousePoint(lParam)
			for panel := 0; panel < comparePanelCount(); panel++ {
				if comparePointInRect(x, y, compareZoomRectForPanel(panel)) {
					compareFilterPanDragging = true
					compareFilterPanPanel, compareFilterPanX, compareFilterPanY = panel, x, y
					pSetCapture.Call(h)
					setPoststackCompareToolCursor(h)
					return 0
				}
			}
		}
	case WM_RBUTTONUP:
		if compareFilterPanDragging {
			compareFilterPanDragging = false
			pReleaseCapture.Call()
			setPoststackCompareToolCursor(h)
			x, y := mousePoint(lParam)
			dx, dy := x-compareFilterPanX, y-compareFilterPanY
			threshold := maxInt(1, displayPixelDIP(4, displayDPI(h)))
			if absInt(dx) >= threshold || absInt(dy) >= threshold {
				applyCompareSectionPan(compareFilterPanPanel, dx, dy)
				return 0
			}
			// A stationary right click belongs to the panel's display menu,
			// even with the optional filtered-view pan gesture available.
		}
		x, y := mousePoint(lParam)
		if displaySurfaceClick(h, x, y, true) {
			return 0
		}
	case 0x0215: // WM_CAPTURECHANGED
		displayNavigationCancel(h)
		cancelPoststackCompareToolDrag(false)
	case 0x001F: // WM_CANCELMODE
		displayNavigationCancel(h)
		cancelPoststackCompareToolDrag(true)
	case 0x0006: // WM_ACTIVATE
		if uint16(wParam) == 0 {
			displayNavigationCancel(h)
			cancelPoststackCompareToolDrag(true)
		}
	case WM_MOUSEMOVE:
		mx, my := mousePoint(lParam)
		if displayNavigationMove(h, mx, my) || (!compareZoomDragging && !compareAnnotDragging && !compareFilterPanDragging && displayNavigationHover(h, mx, my)) {
			if compareCrosshairValid {
				compareCrosshairValid = false
				invalidateCompareCrosshairAt(compareCrosshairWorldX, compareCrosshairWorldY)
			}
			return 0
		}
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
		mx, my := mousePoint(lParam)
		if displayNavigationEnd(h, mx, my) {
			return 0
		}
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
			if kind := poststackAnnotationKind(compareTool); kind >= 0 {
				addCompareAnnotation(kind, compareAnnotPanel, compareAnnotX0, compareAnnotY0, compareAnnotX1, compareAnnotY1)
			}
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
	case WM_COMPARE_PREPARE_PROGRESS:
		if comparePrepareCancel != nil && compareProgressTokenCurrent(h, compareHwnd, int64(lParam), atomic.LoadInt64(&compareGen)) {
			setCompareProgress(int(wParam), true)
		}
		return 0
	case WM_COMPARE_EXPORT_PROGRESS:
		if compareProgressTokenCurrent(h, compareHwnd, int64(lParam), atomic.LoadInt64(&compareExportGen)) {
			setCompareProgress(int(wParam), true)
		}
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
	case WM_CADZOW_DONE:
		handleCadzowDone()
		return 0
	case viewFilterReadyMessage:
		handleViewFilterReady()
		return 0
	case displayDistanceReadyMessage:
		displayDistanceReady(h)
		return 0
	case 0x0113: // WM_TIMER
		if wParam == viewFilterTimer {
			runScheduledViewFilter()
			return 0
		}
	case WM_EXITSIZEMOVE:
		if compareMode != 2 {
			scheduleCurrentViewFilter()
		}
	case WM_GETMINMAXINFO:
		if lParam != 0 {
			mmi := (*MINMAXINFO)(unsafe.Pointer(lParam))
			mmi.PtMinTrackSize.X = 1120
			mmi.PtMinTrackSize.Y = 520
		}
		return 0
	case WM_SIZE:
		displayNavigationCancel(h)
		cancelPoststackCompareExport()
		if poststackInputViewChanged() {
			cancelCadzowTask()
		}
		layoutComparePathLabels()
		layoutCompareStatusProgress()
		layoutCompareWelcomeControls()
		// Panel geometry changes with the client size, so rebuild the static
		// bitmap once. Subsequent mouse hover again uses strip-only restores.
		destroyCompareBaseCache()
		invalidateCompareBase()
		return 0
	case WM_SETCURSOR:
		if uint16(lParam) == 1 && wParam == h && displayNavigationCursor(h) {
			return 1
		}
		if atomic.LoadInt32(&compareBusyCount) > 0 {
			c, _, _ := pLoadCursorW.Call(0, IDC_WAIT)
			if c != 0 {
				pSetCursor.Call(c)
			}
			return 1
		}
		if uint16(lParam) == 1 && wParam == h && setPoststackCompareToolCursor(h) {
			return 1
		}
	case WM_ERASEBKGND:
		// The compare window is fully painted from an off-screen back buffer.
		// Suppress the default background erase to prevent white/gray flashes.
		return 1
	case displayRepaintCompareLayoutMessage:
		invalidateCompareBase()
		return 0
	case WM_PAINT:
		paintCompare()
		return 0
	case WM_CLOSE:
		if requestPoststackCompareClose(h) {
			return 0
		}
		revokeOleSegyDropTarget(h)
		pDestroyWindow.Call(h)
		return 0
	case WM_DESTROY:
		closeNumericalCompareExport()
		closeCompareGeometryDialog()
		closeCompareDataInfo()
		displayNavigationClose(h)
		displayCloseDistance(h)
		displayCloseOwner(h)
		compareFilterPanDragging = false
		closePoststackSpectrum()
		resetPoststackViewFilter()
		cancelCadzowTask()
		cancelPoststackCompareExport()
		cancelPoststackComparePrepare()
		revokeOleSegyDropTarget(h)
		atomic.AddInt64(&compareGen, 1)
		atomic.AddInt64(&compareSliceGen, 1)
		atomic.AddInt64(&compareGeomGen, 1)
		if compareTD != nil {
			compareTD.close()
			compareTD = nil
		}
		releaseGeneratedPoststackResults()
		destroyCompareBaseCache()
		releaseStartHomeFonts()
		cc.toolbar.close()
		compareHwnd = 0
		cc = compareControls{}
		compareTool = poststackToolIdle
		compareA = comparePanel{}
		compareB = comparePanel{}
		compareD = comparePanel{}
		compareShowDiff = false
		compareWorkspaceActive = false
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
