//go:build windows

package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	geometrycore "github.com/DavidLiu-code/SeisForge-Studio/internal/geometry"
	projectcore "github.com/DavidLiu-code/SeisForge-Studio/internal/project"
	pseudo3dcore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudo3d"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	workspacecore "github.com/DavidLiu-code/SeisForge-Studio/internal/workspace"
)

const (
	IDCROOKED_HOME       = 7001
	IDCROOKED_OPEN       = 7002
	IDCROOKED_AXIS       = 7003
	IDCROOKED_PALETTE    = 7004
	IDCROOKED_GAINMINUS  = 7005
	IDCROOKED_GAINPLUS   = 7006
	IDCROOKED_RESET      = 7007
	IDCROOKED_HEADERS    = 7008
	IDCROOKED_RETRY      = 7009
	IDCROOKED_STATUS     = 7010
	IDCROOKED_FOLDER     = 7011
	IDCROOKED_LINES      = 7012
	IDCROOKED_PSEUDO     = 7013
	IDCROOKED_RANGE      = 7014
	IDCROOKED_RANGECLEAR = 7015
	IDCROOKED_TIMERANGE  = 7016
	IDCROOKED_TIMECLEAR  = 7017
	IDCROOKED_STYLE      = 7018
	IDCROOKED_TRACE      = 7019

	IDCROOKED_HEADER_SOURCE = 7051
	IDCROOKED_HEADER_X      = 7052
	IDCROOKED_HEADER_Y      = 7053
	IDCROOKED_HEADER_CDP    = 7054
	IDCROOKED_HEADER_SCALAR = 7055
	IDCROOKED_HEADER_UNITS  = 7056
	IDCROOKED_HEADER_APPLY  = 7057
	IDCROOKED_HEADER_CANCEL = 7058

	WM_CROOKED_READY         = WM_USER + 401
	WM_CROOKED_RENDER_READY  = WM_USER + 402
	WM_CROOKED_PROGRESS      = WM_USER + 403
	WM_CROOKED_PROJECT_READY = WM_USER + 404
	WM_CROOKED_MAP_CLICK     = WM_USER + 405
)

const (
	LBS_NOTIFY           = 0x0001
	WS_VSCROLL           = 0x00200000
	WS_HSCROLL           = 0x00100000
	LBN_SELCHANGE        = 1
	LB_ADDSTRING         = 0x0180
	LB_RESETCONTENT      = 0x0184
	LB_SETCURSEL         = 0x0186
	LB_GETCURSEL         = 0x0188
	LB_ERR               = ^uintptr(0)
	OFN_ALLOWMULTISELECT = 0x00000200
	BIF_RETURNONLYFSDIRS = 0x0001
	BIF_NEWDIALOGSTYLE   = 0x0040
)

const (
	crookedAxisTrace = iota
	crookedAxisCDP
	crookedAxisDistance
)

type crookedControls struct {
	home, open, folder, pseudo, spatialRange, rangeClear, timeRange, timeClear, axis, palette uintptr
	gainMinus, gainValue, gainPlus                                                            uintptr
	reset, headers, retry, trace, style, styleLabel                                           uintptr
	status, progress, lines                                                                   uintptr
}

type crookedHeaderControls struct {
	source                   uintptr
	x, y, cdp, scalar, units uintptr
	apply, cancel            uintptr
}

type crookedViewDefaults struct {
	Version      int                      `json:"version"`
	AxisMode     int                      `json:"axis_mode"`
	PaletteIndex int                      `json:"palette_index"`
	GainPercent  float64                  `json:"gain_percent"`
	Spec         segy.TraceCoordinateSpec `json:"coordinate_spec"`
}

type crookedStyleDefaults struct {
	Version   int `json:"version"`
	StyleMode int `json:"style_mode"`
}

type crookedNavigationTarget struct {
	projectGeneration   int64
	specGeneration      int64
	lineIndex           int
	lineID              string
	traceIndex          int64
	geometryFingerprint string
	timeMS              float64
}

type crookedSession struct {
	project              *projectcore.CrookedProject
	projectLines         []crookedProjectLineState
	activeLine           int
	hoverLine            int
	dataset              *dataset.SeismicDataset
	reader               *segy.File
	geometry             *geometrycore.CrookedLineGeometry
	workspaceGeneration  uint64
	path                 string
	sampleStart          int
	sampleEnd            int
	axisMode             int
	paletteIndex         int
	gainPercent          float64
	spec                 segy.TraceCoordinateSpec
	viewStart            float64
	viewEnd              float64
	currentPosition      int
	currentSample        int
	indices              []byte
	bgra                 []byte
	imageWidth           int
	imageHeight          int
	stats                segy.RenderStats
	errorText            string
	loading              bool
	rendering            bool
	progress             int
	dragging             bool
	dragMoved            bool
	dragStartX           int
	dragViewStart        float64
	dragViewEnd          float64
	lastStatusUpdate     time.Time
	pseudoRange          pseudo3dcore.XYRange
	rangeGeneration      int64
	rangeMode            bool
	rangeDragging        bool
	rangeStartX          int
	rangeStartY          int
	rangeCurrentX        int
	rangeCurrentY        int
	pseudoTimeRange      pseudo3dcore.TimeRange
	timeRangeGeneration  int64
	timeRangeMode        bool
	timeRangeDragging    bool
	timeRangeStartY      int
	timeRangeCurrentY    int
	traceInspectMode     bool
	geometryViewport     pseudo3dcore.XYRange
	mapZoomPending       bool
	mapZoomDragging      bool
	mapZoomStartX        int
	mapZoomStartY        int
	mapZoomCurrentX      int
	mapZoomCurrentY      int
	styleMode            int
	pendingNavigation    *crookedNavigationTarget
	pendingMapClick      *crookedNavigationTarget
	projectGeometryReady bool
	// projectGeometrySuspended records that leaving the Crooked workspace
	// invalidated in-flight header-only geometry workers.  Ready and failed
	// line states remain authoritative; Show only restarts unfinished lines.
	projectGeometrySuspended bool
	lastPseudoCursor         pseudo3dcore.LinkedCursor
	lastPseudoCursorAt       time.Time
}

type crookedLineView struct {
	axisMode, currentPosition, currentSample int
	viewStart, viewEnd                       float64
}

type crookedProjectLineState struct {
	line        *projectcore.CrookedProjectLine
	rawGeometry *geometrycore.CrookedLineGeometry
	geometry    *geometrycore.CrookedLineGeometry
	mapPath     crookedMapPath
	fingerprint string
	view        crookedLineView
	errorText   string
	loading     bool
	ready       bool
	multiplier  float64
	calibration projectcore.CoordinateCalibration
}

type crookedProjectGeometryResult struct {
	deliveryEpoch int64
	projectGen    int64
	specGen       int64
	lineIndex     int
	lineID        string
	geometry      *geometrycore.CrookedLineGeometry
	build         geometrycore.CrookedBuildResult
	err           error
}

type crookedProjectGeometryJob struct {
	index   int
	lineID  string
	line    *projectcore.CrookedProjectLine
	spec    segy.TraceCoordinateSpec
	specGen int64
}

type crookedPrepareResult struct {
	deliveryEpoch int64
	gen           int64
	projectGen    int64
	specGen       int64
	lineIndex     int
	lineID        string
	dataset       *dataset.SeismicDataset
	reader        *segy.File
	geometry      *geometrycore.CrookedLineGeometry
	build         geometrycore.CrookedBuildResult
	err           error
}

const crookedCanonicalMapPointLimit = 4096

type crookedMapPathPoint struct {
	X, Y       float64
	TraceIndex int64
	Position   int
}

type crookedMapPath struct {
	LineID      string
	Fingerprint string
	Points      []crookedMapPathPoint
}

type crookedMapHit struct {
	LineID      string
	LineIndex   int
	TraceIndex  int64
	Position    int
	Fingerprint string
	DistancePX  float64
	Valid       bool
}

type crookedRenderResult struct {
	deliveryEpoch int64
	gen           int64
	indices       []byte
	bgra          []byte
	width         int
	height        int
	stats         segy.RenderStats
	err           error
}

var (
	crookedHwnd                                      uintptr
	crookedHeaderHwnd                                uintptr
	crookedControlsUI                                crookedControls
	crookedHeaderUI                                  crookedHeaderControls
	crookedState                                     crookedSession
	crookedDefaults                                  crookedViewDefaults
	crookedDefaultsLoaded                            bool
	crookedStyle                                     crookedStyleDefaults
	crookedStyleLoaded                               bool
	crookedLoadGen                                   int64
	crookedRenderGen                                 int64
	crookedPendingMu                                 sync.Mutex
	crookedDeliveryEpoch                             int64
	crookedDeliveryHwnd                              uintptr
	crookedPending                                   *crookedPrepareResult
	crookedRenderPending                             *crookedRenderResult
	crookedManagerClosing                            bool
	crookedProjectGen                                int64
	crookedSpecGen                                   int64
	crookedMapClickGen                               int64
	crookedProjectPending                            []*crookedProjectGeometryResult
	crookedBaseDC, crookedBaseBmp, crookedBaseOldBmp uintptr
	crookedBaseW, crookedBaseH                       int
	crookedBaseDirty                                 = true
	crookedPostWindowMessage                         = func(hwnd uintptr, message uint32, wParam, lParam uintptr) bool {
		posted, _, _ := pPostMessageW.Call(hwnd, uintptr(message), wParam, lParam)
		return posted != 0
	}
)

var (
	pSHBrowseForFolderW   = shell32.NewProc("SHBrowseForFolderW")
	pSHGetPathFromIDListW = shell32.NewProc("SHGetPathFromIDListW")
	ole32Crooked          = syscall.NewLazyDLL("ole32.dll")
	pCoTaskMemFreeCrooked = ole32Crooked.NewProc("CoTaskMemFree")
)

// All worker result delivery is tied to a window-lifecycle epoch.  The HWND,
// epoch and pending result slots are protected by the same mutex so reset or
// destruction cannot drain a slot and then have an old worker install a reader
// behind it.  PostMessage is intentionally issued while holding the mutex: it
// is non-blocking, and this makes installation plus notification atomic with
// respect to invalidation.
func registerCrookedDeliveryWindow(hwnd uintptr) {
	var stale *crookedPrepareResult
	crookedPendingMu.Lock()
	crookedDeliveryEpoch++
	crookedDeliveryHwnd = hwnd
	stale = crookedPending
	crookedPending = nil
	crookedRenderPending = nil
	crookedProjectPending = nil
	crookedPendingMu.Unlock()
	closeCrookedPrepareReader(stale)
}

func crookedDeliverySnapshot() int64 {
	crookedPendingMu.Lock()
	epoch := crookedDeliveryEpoch
	crookedPendingMu.Unlock()
	return epoch
}

func closeCrookedPrepareReader(result *crookedPrepareResult) {
	if result != nil && result.reader != nil {
		_ = result.reader.Close()
		result.reader = nil
	}
}

func invalidateCrookedDeliveries(clearWindow bool) {
	var stale *crookedPrepareResult
	crookedPendingMu.Lock()
	crookedDeliveryEpoch++
	if clearWindow {
		crookedDeliveryHwnd = 0
	}
	stale = crookedPending
	crookedPending = nil
	crookedRenderPending = nil
	crookedProjectPending = nil
	crookedPendingMu.Unlock()
	closeCrookedPrepareReader(stale)
}

// failCrookedDeliveryLocked is called only while crookedPendingMu is held.
// A failed PostMessage means the registered HWND is no longer a safe delivery
// target, so every pending slot is detached before any reader is closed.
func failCrookedDeliveryLocked() *crookedPrepareResult {
	crookedDeliveryEpoch++
	crookedDeliveryHwnd = 0
	stale := crookedPending
	crookedPending = nil
	crookedRenderPending = nil
	crookedProjectPending = nil
	return stale
}

type crookedBrowseInfo struct {
	Owner, Root, DisplayName, Title uintptr
	Flags                           uint32
	_                               uint32
	Callback, LParam                uintptr
	Image                           int32
	_2                              uint32
}

func defaultCrookedView() crookedViewDefaults {
	return crookedViewDefaults{Version: 1, AxisMode: crookedAxisDistance, PaletteIndex: 2, GainPercent: 0,
		Spec: segy.TraceCoordinateSpec{Source: segy.CoordinateAuto, CDPByte: 21, ScalarByte: 71, UnitsByte: 89}}
}

func crookedDefaultsPath() string { return licenseDirectory() + `\crooked_view.json` }

func crookedStylePath() string { return licenseDirectory() + `\crooked_style.json` }

func loadCrookedStyle() {
	if crookedStyleLoaded {
		return
	}
	crookedStyleLoaded = true
	crookedStyle = crookedStyleDefaults{Version: 1, StyleMode: 2} // Standard preserves the Phase 5 appearance.
	if data, err := os.ReadFile(crookedStylePath()); err == nil {
		var loaded crookedStyleDefaults
		if json.Unmarshal(data, &loaded) == nil && loaded.Version == 1 && loaded.StyleMode >= 0 && loaded.StyleMode <= 3 {
			crookedStyle = loaded
		}
	}
}

func saveCrookedStyle() {
	crookedStyle.Version = 1
	if err := os.MkdirAll(licenseDirectory(), 0o700); err != nil {
		return
	}
	if data, err := json.MarshalIndent(crookedStyle, "", "  "); err == nil {
		_ = os.WriteFile(crookedStylePath(), append(data, '\n'), 0o600)
	}
}

var crookedStyleComboOrder = []int{3, 0, 1, 2} // Clean, CIGVis, Interpretation, Standard.

func crookedStyleFromCombo(index int) int {
	if index < 0 || index >= len(crookedStyleComboOrder) {
		return 2
	}
	return crookedStyleComboOrder[index]
}

func crookedStyleComboIndex(style int) int {
	for i, value := range crookedStyleComboOrder {
		if value == style {
			return i
		}
	}
	return len(crookedStyleComboOrder) - 1
}

func crookedPseudoDisplayStyle() pseudo3dcore.DisplayStyle {
	switch crookedState.styleMode {
	case 3:
		return pseudo3dcore.StyleClean
	case 0:
		return pseudo3dcore.StyleCIGVis
	case 1:
		return pseudo3dcore.StyleInterpretation
	default:
		return pseudo3dcore.StyleStandard
	}
}

func applyCrookedSharedStyle(style int) {
	if style < 0 || style > 3 {
		style = 2
	}
	crookedState.styleMode = style
	crookedStyle.StyleMode = style
	saveCrookedStyle()
	if crookedControlsUI.style != 0 {
		pSendMessageW.Call(crookedControlsUI.style, CB_SETCURSEL, uintptr(crookedStyleComboIndex(style)), 0)
	}
	invalidateCrookedBase()
	pseudoApplyCrookedStyle()
}

func loadCrookedDefaults() {
	if crookedDefaultsLoaded {
		return
	}
	crookedDefaultsLoaded = true
	crookedDefaults = defaultCrookedView()
	data, err := os.ReadFile(crookedDefaultsPath())
	if err == nil {
		var loaded crookedViewDefaults
		if json.Unmarshal(data, &loaded) == nil && loaded.Version == 1 {
			crookedDefaults = loaded
		}
	}
	if crookedDefaults.AxisMode < crookedAxisTrace || crookedDefaults.AxisMode > crookedAxisDistance {
		crookedDefaults.AxisMode = crookedAxisDistance
	}
	if crookedDefaults.PaletteIndex < 0 || crookedDefaults.PaletteIndex >= len(paletteNames) {
		crookedDefaults.PaletteIndex = 2
	}
	crookedDefaults.GainPercent = math.Max(0, math.Min(49, crookedDefaults.GainPercent))
	if crookedDefaults.Spec.Source == "" {
		crookedDefaults.Spec = defaultCrookedView().Spec
	}
}

func saveCrookedDefaults() {
	crookedDefaults.Version = 1
	if err := os.MkdirAll(licenseDirectory(), 0o700); err != nil {
		return
	}
	data, err := json.MarshalIndent(crookedDefaults, "", "  ")
	if err == nil {
		_ = os.WriteFile(crookedDefaultsPath(), data, 0o600)
	}
}

func createCrookedWindowShellDeferred() bool {
	if crookedHwnd != 0 {
		return true
	}
	loadCrookedDefaults()
	loadCrookedStyle()
	h, _, _ := pCreateWindowExW.Call(WS_EX_APPWINDOW, uintptr(unsafe.Pointer(u16("Limage64Crooked"))), uintptr(unsafe.Pointer(u16(APP_NAME+" v"+APP_VERSION+" [x64] - 二维叠后弯线"))),
		WS_OVERLAPPEDWINDOW|WS_CLIPCHILDREN, uintptr(CW_USEDEFAULT), uintptr(CW_USEDEFAULT), 1500, 920, 0, 0, 0, 0)
	if h == 0 {
		return false
	}
	crookedHwnd = h
	registerCrookedDeliveryWindow(h)
	crookedState = crookedSession{axisMode: crookedDefaults.AxisMode, paletteIndex: crookedDefaults.PaletteIndex, gainPercent: crookedDefaults.GainPercent,
		spec: crookedDefaults.Spec, viewStart: 0, viewEnd: 1, activeLine: -1, hoverLine: -1, styleMode: crookedStyle.StyleMode}
	createCrookedUI()
	acceptSegyDrops(h)
	registerOleSegyDropTarget(h, workspaceModeCrooked)
	return true
}

func resetCrookedToEmpty(workspaceGeneration uint64) {
	closePseudoWindowForProjectChange()
	atomic.AddInt64(&crookedLoadGen, 1)
	atomic.AddInt64(&crookedRenderGen, 1)
	atomic.AddInt64(&crookedProjectGen, 1)
	atomic.AddInt64(&crookedMapClickGen, 1)
	invalidateCrookedDeliveries(false)
	if crookedState.reader != nil {
		_ = crookedState.reader.Close()
	}
	if crookedHeaderHwnd != 0 {
		pDestroyWindow.Call(crookedHeaderHwnd)
	}
	crookedState = crookedSession{
		workspaceGeneration: workspaceGeneration,
		axisMode:            crookedDefaults.AxisMode,
		paletteIndex:        crookedDefaults.PaletteIndex,
		gainPercent:         crookedDefaults.GainPercent,
		spec:                crookedDefaults.Spec,
		viewStart:           0,
		viewEnd:             1,
		activeLine:          -1,
		hoverLine:           -1,
		styleMode:           crookedStyle.StyleMode,
	}
	pSendMessageW.Call(crookedControlsUI.lines, LB_RESETCONTENT, 0, 0)
	pSendMessageW.Call(crookedControlsUI.axis, CB_SETCURSEL, uintptr(crookedState.axisMode), 0)
	pSendMessageW.Call(crookedControlsUI.palette, CB_SETCURSEL, uintptr(crookedState.paletteIndex), 0)
	setText(crookedControlsUI.gainValue, fmt.Sprintf("%.0f%%", crookedState.gainPercent))
	setText(crookedControlsUI.status, "请选择一个或多个二维叠后弯线 SEG-Y，或打开包含测线的文件夹。")
	pSendMessageW.Call(crookedControlsUI.progress, PBM_SETPOS, 0, 0)
	pEnableWindow.Call(crookedControlsUI.pseudo, 0)
	pEnableWindow.Call(crookedControlsUI.spatialRange, 0)
	pEnableWindow.Call(crookedControlsUI.lines, 0)
	pEnableWindow.Call(crookedControlsUI.rangeClear, 0)
	pEnableWindow.Call(crookedControlsUI.timeRange, 0)
	pEnableWindow.Call(crookedControlsUI.timeClear, 0)
	pEnableWindow.Call(crookedControlsUI.trace, 0)
	setText(crookedControlsUI.spatialRange, "范围")
	setText(crookedControlsUI.timeRange, "时间范围")
	destroyCrookedBaseCache()
	layoutCrookedControls()
	invalidateCrookedBase()
}

func openEmptyCrookedWorkspace(owner uintptr) bool {
	if application == nil {
		if err := initializePhase1Application(); err != nil {
			message(owner, APP_NAME, err.Error(), MB_OK|MB_ICONERROR)
			return false
		}
	}
	if err := application.OpenWorkspace(workspacecore.KindCrooked); err != nil {
		message(owner, "打开二维弯线", err.Error(), MB_OK|MB_ICONERROR)
		return false
	}
	activeWorkspaceMode = workspaceModeCrooked
	return true
}

func createCrookedUI() {
	crookedControlsUI = crookedControls{}
	crookedControlsUI.home = createCtrl(crookedHwnd, "BUTTON", "首页", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 10, 8, 54, 24, IDCROOKED_HOME)
	crookedControlsUI.folder = createCtrl(crookedHwnd, "BUTTON", "打开文件夹", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 70, 8, 82, 24, IDCROOKED_FOLDER)
	crookedControlsUI.open = createCtrl(crookedHwnd, "BUTTON", "打开文件", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 158, 8, 72, 24, IDCROOKED_OPEN)
	createCtrl(crookedHwnd, "STATIC", "横轴", WS_CHILD|WS_VISIBLE, 242, 12, 34, 18, 0)
	crookedControlsUI.axis = createCtrl(crookedHwnd, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 278, 7, 100, 120, IDCROOKED_AXIS)
	for _, label := range []string{"Trace", "CDP", "Distance"} {
		pSendMessageW.Call(crookedControlsUI.axis, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(label))))
	}
	pSendMessageW.Call(crookedControlsUI.axis, CB_SETCURSEL, uintptr(crookedState.axisMode), 0)
	createCtrl(crookedHwnd, "STATIC", "色标", WS_CHILD|WS_VISIBLE, 390, 12, 34, 18, 0)
	crookedControlsUI.palette = createCtrl(crookedHwnd, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 426, 7, 125, 360, IDCROOKED_PALETTE)
	for _, label := range paletteNames {
		pSendMessageW.Call(crookedControlsUI.palette, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(label))))
	}
	pSendMessageW.Call(crookedControlsUI.palette, CB_SETCURSEL, uintptr(crookedState.paletteIndex), 0)
	createCtrl(crookedHwnd, "STATIC", "增益", WS_CHILD|WS_VISIBLE, 563, 12, 34, 18, 0)
	crookedControlsUI.gainMinus = createCtrl(crookedHwnd, "BUTTON", "−", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 599, 8, 28, 24, IDCROOKED_GAINMINUS)
	crookedControlsUI.gainValue = createCtrl(crookedHwnd, "STATIC", fmt.Sprintf("%.0f%%", crookedState.gainPercent), WS_CHILD|WS_VISIBLE|WS_BORDER, 630, 9, 48, 22, 0)
	crookedControlsUI.gainPlus = createCtrl(crookedHwnd, "BUTTON", "+", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 681, 8, 28, 24, IDCROOKED_GAINPLUS)
	crookedControlsUI.reset = createCtrl(crookedHwnd, "BUTTON", "剖面复位", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 721, 8, 72, 24, IDCROOKED_RESET)
	crookedControlsUI.headers = createCtrl(crookedHwnd, "BUTTON", "道头设置", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 799, 8, 78, 24, IDCROOKED_HEADERS)
	crookedControlsUI.retry = createCtrl(crookedHwnd, "BUTTON", "重试", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 883, 8, 54, 24, IDCROOKED_RETRY)
	crookedControlsUI.spatialRange = createCtrl(crookedHwnd, "BUTTON", "范围", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 10, 38, 54, 24, IDCROOKED_RANGE)
	crookedControlsUI.rangeClear = createCtrl(crookedHwnd, "BUTTON", "清除范围", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 70, 38, 70, 24, IDCROOKED_RANGECLEAR)
	crookedControlsUI.styleLabel = createCtrl(crookedHwnd, "STATIC", "风格", WS_CHILD|WS_VISIBLE, 154, 42, 34, 18, 0)
	crookedControlsUI.style = createCtrl(crookedHwnd, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 190, 37, 92, 150, IDCROOKED_STYLE)
	for _, name := range []string{"干净", "CIGVis", "解释", "标准"} {
		pSendMessageW.Call(crookedControlsUI.style, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(name))))
	}
	pSendMessageW.Call(crookedControlsUI.style, CB_SETCURSEL, uintptr(crookedStyleComboIndex(crookedState.styleMode)), 0)
	crookedControlsUI.pseudo = createCtrl(crookedHwnd, "BUTTON", "伪", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 290, 38, 46, 24, IDCROOKED_PSEUDO)
	crookedControlsUI.timeRange = createCtrl(crookedHwnd, "BUTTON", "时间范围", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 346, 38, 72, 24, IDCROOKED_TIMERANGE)
	crookedControlsUI.timeClear = createCtrl(crookedHwnd, "BUTTON", "清除时间", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 424, 38, 72, 24, IDCROOKED_TIMECLEAR)
	crookedControlsUI.trace = createCtrl(crookedHwnd, "BUTTON", "道", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_AUTOCHECKBOX|BS_PUSHLIKE, 504, 38, 46, 24, IDCROOKED_TRACE)
	pEnableWindow.Call(crookedControlsUI.pseudo, 0)
	pEnableWindow.Call(crookedControlsUI.spatialRange, 0)
	pEnableWindow.Call(crookedControlsUI.rangeClear, 0)
	pEnableWindow.Call(crookedControlsUI.timeRange, 0)
	pEnableWindow.Call(crookedControlsUI.timeClear, 0)
	pEnableWindow.Call(crookedControlsUI.trace, 0)
	crookedControlsUI.status = createCtrl(crookedHwnd, "STATIC", "请选择文件夹或多个二维叠后弯线 SEG-Y。", WS_CHILD|WS_VISIBLE, 10, 69, 1180, 19, IDCROOKED_STATUS)
	crookedControlsUI.progress = createCtrl(crookedHwnd, "msctls_progress32", "", WS_CHILD, 1200, 70, 280, 17, 0)
	crookedControlsUI.lines = createCtrl(crookedHwnd, "LISTBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|WS_BORDER|WS_VSCROLL|WS_HSCROLL|LBS_NOTIFY, 0, 0, 220, 180, IDCROOKED_LINES)
	pSendMessageW.Call(crookedControlsUI.progress, PBM_SETRANGE, 0, uintptr(uint32(100)<<16))
	layoutCrookedControls()
}

func layoutCrookedControls() {
	if crookedHwnd == 0 {
		return
	}
	cw, _ := clientSize(crookedHwnd)
	if crookedState.loading || crookedState.rendering {
		progressWidth := 270
		if cw < 1000 {
			progressWidth = 190
		}
		progressX := cw - progressWidth - 10
		pMoveWindow.Call(crookedControlsUI.status, 10, 69, uintptr(maxInt(200, progressX-20)), 19, 1)
		pMoveWindow.Call(crookedControlsUI.progress, uintptr(progressX), 70, uintptr(progressWidth), 17, 1)
		pShowWindow.Call(crookedControlsUI.progress, SW_SHOW)
	} else {
		pMoveWindow.Call(crookedControlsUI.status, 10, 69, uintptr(maxInt(200, cw-20)), 19, 1)
		pShowWindow.Call(crookedControlsUI.progress, SW_HIDE)
	}
	list := crookedLineListRect()
	pMoveWindow.Call(crookedControlsUI.lines, uintptr(list.Left), uintptr(list.Top), uintptr(maxInt(1, int(list.Right-list.Left))), uintptr(maxInt(1, int(list.Bottom-list.Top))), 1)
}

func crookedMapRect() RECT {
	cw, ch := clientSize(crookedHwnd)
	top := 106
	usable := maxInt(300, ch-top-52)
	mapHeight := maxInt(150, usable*30/100)
	return RECT{Left: 58, Top: int32(top), Right: int32(maxInt(120, cw-258)), Bottom: int32(top + mapHeight)}
}

func crookedLineListRect() RECT {
	cw, _ := clientSize(crookedHwnd)
	mapRect := crookedMapRect()
	return RECT{Left: mapRect.Right + 12, Top: mapRect.Top, Right: int32(maxInt(int(mapRect.Right)+100, cw-26)), Bottom: mapRect.Bottom}
}

func crookedSectionRect() RECT {
	cw, ch := clientSize(crookedHwnd)
	mapRect := crookedMapRect()
	top := int(mapRect.Bottom) + 48
	return RECT{Left: 58, Top: int32(top), Right: int32(maxInt(120, cw-26)), Bottom: int32(maxInt(top+100, ch-38))}
}

func rectContains(rect RECT, x, y int) bool {
	return x >= int(rect.Left) && x <= int(rect.Right) && y >= int(rect.Top) && y <= int(rect.Bottom)
}

func browseCrookedFolder(owner uintptr) string {
	display := make([]uint16, 32768)
	info := crookedBrowseInfo{Owner: owner, DisplayName: uintptr(unsafe.Pointer(&display[0])), Title: uintptr(unsafe.Pointer(u16("选择包含弯线 SEG-Y 的文件夹"))), Flags: BIF_RETURNONLYFSDIRS | BIF_NEWDIALOGSTYLE}
	pidl, _, _ := pSHBrowseForFolderW.Call(uintptr(unsafe.Pointer(&info)))
	if pidl == 0 {
		return ""
	}
	defer pCoTaskMemFreeCrooked.Call(pidl)
	path := make([]uint16, 32768)
	ok, _, _ := pSHGetPathFromIDListW.Call(pidl, uintptr(unsafe.Pointer(&path[0])))
	if ok == 0 {
		return ""
	}
	return syscall.UTF16ToString(path)
}

func openCrookedMultiDialog(owner uintptr) []string {
	buf := make([]uint16, 256*1024)
	filter := filterUTF16()
	of := OPENFILENAME{LStructSize: uint32(unsafe.Sizeof(OPENFILENAME{})), HwndOwner: owner,
		LpstrFilter: uintptr(unsafe.Pointer(&filter[0])), NFilterIndex: 1, LpstrFile: uintptr(unsafe.Pointer(&buf[0])),
		NMaxFile: uint32(len(buf)), Flags: OFN_EXPLORER | OFN_FILEMUSTEXIST | OFN_HIDEREADONLY | OFN_ALLOWMULTISELECT}
	result, _, _ := pGetOpenFileNameW.Call(uintptr(unsafe.Pointer(&of)))
	if result == 0 {
		return nil
	}
	parts := make([]string, 0, 16)
	for start := 0; start < len(buf) && buf[start] != 0; {
		end := start
		for end < len(buf) && buf[end] != 0 {
			end++
		}
		parts = append(parts, syscall.UTF16ToString(buf[start:end]))
		start = end + 1
	}
	if len(parts) <= 1 {
		return parts
	}
	paths := make([]string, 0, len(parts)-1)
	for _, name := range parts[1:] {
		paths = append(paths, filepath.Join(parts[0], name))
	}
	return paths
}

func openCrookedProjectFolder(path string) bool {
	if application == nil {
		if err := initializePhase1Application(); err != nil {
			message(crookedHwnd, APP_NAME, err.Error(), MB_OK|MB_ICONERROR)
			return false
		}
	}
	p, err := application.OpenCrookedFolder(path)
	if err != nil {
		message(crookedHwnd, "打开弯线项目", err.Error(), MB_OK|MB_ICONERROR)
		return false
	}
	activeWorkspaceMode = workspaceModeCrooked
	rememberRecentCrookedProject(p.Root, nil)
	return true
}

func openCrookedProjectPaths(paths []string) bool {
	if len(paths) == 0 {
		return false
	}
	if application == nil {
		if err := initializePhase1Application(); err != nil {
			message(crookedHwnd, APP_NAME, err.Error(), MB_OK|MB_ICONERROR)
			return false
		}
	}
	p, err := application.OpenCrookedPaths(paths)
	if err != nil {
		message(crookedHwnd, "打开弯线项目", err.Error(), MB_OK|MB_ICONERROR)
		return false
	}
	activeWorkspaceMode = workspaceModeCrooked
	root := p.Root
	if len(paths) == 1 {
		root = paths[0]
	}
	rememberRecentCrookedProject(root, paths)
	return true
}

func crookedPaletteBGRA(indices []byte, paletteIndex int) []byte {
	if paletteIndex < 0 || paletteIndex >= len(legacyAnchors) {
		paletteIndex = 2
	}
	anchors := legacyAnchors[paletteIndex]
	start, middle, end := tcolor(anchors[2]), tcolor(anchors[1]), tcolor(anchors[0])
	var palette [256]rgb
	for i := 0; i < 128; i++ {
		palette[i] = rgb{lerp(start.r, middle.r, i), lerp(start.g, middle.g, i), lerp(start.b, middle.b, i)}
		palette[128+i] = rgb{lerp(middle.r, end.r, i), lerp(middle.g, end.g, i), lerp(middle.b, end.b, i)}
	}
	out := make([]byte, len(indices)*4)
	for i, value := range indices {
		color := palette[int(value)]
		j := i * 4
		out[j], out[j+1], out[j+2], out[j+3] = color.b, color.g, color.r, 0
	}
	return out
}

func startCrookedProject(p *projectcore.CrookedProject, workspaceGeneration uint64) bool {
	if p == nil || p.ValidLineCount() == 0 || crookedHwnd == 0 {
		return false
	}
	closePseudoWindowForProjectChange()
	projectGen := atomic.AddInt64(&crookedProjectGen, 1)
	atomic.AddInt64(&crookedLoadGen, 1)
	atomic.AddInt64(&crookedRenderGen, 1)
	atomic.AddInt64(&crookedMapClickGen, 1)
	invalidateCrookedDeliveries(false)
	if crookedState.reader != nil {
		_ = crookedState.reader.Close()
		crookedState.reader = nil
	}
	crookedState.project = p
	crookedState.projectLines = make([]crookedProjectLineState, len(p.Lines))
	crookedState.workspaceGeneration = workspaceGeneration
	crookedState.activeLine, crookedState.hoverLine = -1, -1
	crookedState.dataset, crookedState.geometry = nil, nil
	crookedState.indices, crookedState.bgra = nil, nil
	crookedState.errorText = ""
	crookedState.pseudoRange = pseudo3dcore.XYRange{}
	crookedState.rangeGeneration++
	crookedState.rangeMode, crookedState.rangeDragging = false, false
	crookedState.pseudoTimeRange = pseudo3dcore.TimeRange{}
	crookedState.timeRangeGeneration++
	crookedState.timeRangeMode, crookedState.timeRangeDragging = false, false
	crookedState.geometryViewport = pseudo3dcore.XYRange{}
	crookedState.mapZoomPending, crookedState.mapZoomDragging = false, false
	crookedState.pendingNavigation, crookedState.pendingMapClick = nil, nil
	crookedState.projectGeometryReady = false
	crookedState.projectGeometrySuspended = false
	for i, line := range p.Lines {
		crookedState.projectLines[i] = crookedProjectLineState{line: line, multiplier: 1,
			view: crookedLineView{axisMode: crookedDefaults.AxisMode, viewStart: 0, viewEnd: 1, currentPosition: -1}}
		if crookedState.activeLine < 0 && line.Valid() {
			crookedState.activeLine = i
		}
	}
	refreshCrookedLineList()
	pEnableWindow.Call(crookedControlsUI.pseudo, 0)
	pEnableWindow.Call(crookedControlsUI.spatialRange, 0)
	pEnableWindow.Call(crookedControlsUI.lines, 0)
	pEnableWindow.Call(crookedControlsUI.rangeClear, 0)
	pEnableWindow.Call(crookedControlsUI.timeRange, 1)
	pEnableWindow.Call(crookedControlsUI.timeClear, 0)
	pEnableWindow.Call(crookedControlsUI.trace, 0)
	setCrookedTraceInspectMode(false)
	setText(crookedControlsUI.timeRange, "时间范围")
	invalidateCrookedBase()
	writeWorkspaceTrace(workspacecore.TraceEvent{Action: "crooked_project_ready", Workspace: workspacecore.KindCrooked, Dataset: filepath.Base(p.Root), Geometry: geometrycore.KindCrookedLine, Generation: workspaceGeneration, ProjectLines: len(p.Lines)})
	// The active line gets the foreground worker first. Remaining valid lines
	// then build their header-only canonical geometry in the background.
	started := activateCrookedProjectLine(crookedState.activeLine)
	startCrookedProjectBackground(projectGen)
	return started
}

func refreshCrookedLineList() {
	if crookedControlsUI.lines == 0 {
		return
	}
	pSendMessageW.Call(crookedControlsUI.lines, LB_RESETCONTENT, 0, 0)
	for _, state := range crookedState.projectLines {
		prefix := "○"
		suffix := ""
		if state.line == nil {
			prefix, suffix = "×", " 无效"
		} else if state.line.OpenError != "" || state.errorText != "" {
			prefix, suffix = "×", " 错误"
		} else if state.ready {
			prefix = "✓"
		} else if state.loading {
			prefix, suffix = "…", " 加载中"
		}
		name := "未命名"
		traces := int64(0)
		if state.line != nil {
			name = state.line.Name
			if state.line.Dataset != nil {
				traces = state.line.Dataset.Metadata.TraceCount
			}
		}
		label := fmt.Sprintf("%s %s  [%d 道]%s", prefix, name, traces, suffix)
		pSendMessageW.Call(crookedControlsUI.lines, LB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(label))))
	}
	if crookedState.activeLine >= 0 {
		pSendMessageW.Call(crookedControlsUI.lines, LB_SETCURSEL, uintptr(crookedState.activeLine), 0)
	}
}

func saveCrookedActiveLineView() {
	i := crookedState.activeLine
	if i < 0 || i >= len(crookedState.projectLines) {
		return
	}
	crookedState.projectLines[i].view = crookedLineView{axisMode: crookedState.axisMode, viewStart: crookedState.viewStart, viewEnd: crookedState.viewEnd,
		currentPosition: crookedState.currentPosition, currentSample: crookedState.currentSample}
}

func crookedGeometryFingerprint(lineID string, geometry *geometrycore.CrookedLineGeometry) string {
	if geometry == nil || len(geometry.TraceIndices) == 0 {
		return ""
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(lineID))
	var encoded [8]byte
	binary.LittleEndian.PutUint64(encoded[:], uint64(len(geometry.TraceIndices)))
	_, _ = hash.Write(encoded[:])
	for i, trace := range geometry.TraceIndices {
		binary.LittleEndian.PutUint64(encoded[:], uint64(trace))
		_, _ = hash.Write(encoded[:])
		if i < len(geometry.X) {
			binary.LittleEndian.PutUint64(encoded[:], math.Float64bits(geometry.X[i]))
			_, _ = hash.Write(encoded[:])
		}
		if i < len(geometry.Y) {
			binary.LittleEndian.PutUint64(encoded[:], math.Float64bits(geometry.Y[i]))
			_, _ = hash.Write(encoded[:])
		}
	}
	return fmt.Sprintf("%016x", hash.Sum64())
}

// canonicalCrookedMapPath is the only project-map representation. It is
// derived from calibrated SEG-Y trace headers; DAT navigation never enters
// bounds, drawing, hit-testing, or cursor placement.
func canonicalCrookedMapPath(state *crookedProjectLineState, maxPoints int) crookedMapPath {
	if state == nil || state.geometry == nil || len(state.geometry.TraceIndices) == 0 {
		return crookedMapPath{}
	}
	geometry := state.geometry
	lineID := ""
	if state.line != nil {
		lineID = state.line.ID
	}
	fingerprint := state.fingerprint
	if fingerprint == "" {
		fingerprint = crookedGeometryFingerprint(lineID, geometry)
	}
	count := len(geometry.TraceIndices)
	if maxPoints <= 0 || maxPoints >= count {
		maxPoints = count
	}
	positions := make(map[int]struct{}, maxPoints+4)
	if maxPoints == 1 {
		positions[0] = struct{}{}
	} else {
		for i := 0; i < maxPoints; i++ {
			position := int(math.Round(float64(i) * float64(count-1) / float64(maxPoints-1)))
			positions[position] = struct{}{}
		}
	}
	// Preserve the true bounds even when the acquisition path is sampled.
	xMin, xMax, yMin, yMax := 0, 0, 0, 0
	for i := 1; i < count; i++ {
		if geometry.X[i] < geometry.X[xMin] {
			xMin = i
		}
		if geometry.X[i] > geometry.X[xMax] {
			xMax = i
		}
		if geometry.Y[i] < geometry.Y[yMin] {
			yMin = i
		}
		if geometry.Y[i] > geometry.Y[yMax] {
			yMax = i
		}
	}
	positions[xMin], positions[xMax], positions[yMin], positions[yMax] = struct{}{}, struct{}{}, struct{}{}, struct{}{}
	ordered := make([]int, 0, len(positions))
	for position := range positions {
		ordered = append(ordered, position)
	}
	sort.Ints(ordered)
	path := crookedMapPath{LineID: lineID, Fingerprint: fingerprint, Points: make([]crookedMapPathPoint, 0, len(ordered))}
	for _, position := range ordered {
		path.Points = append(path.Points, crookedMapPathPoint{X: geometry.X[position], Y: geometry.Y[position], TraceIndex: geometry.TraceIndices[position], Position: position})
	}
	return path
}

func crookedMapPathBounds(path crookedMapPath) geometrycore.Bounds {
	if len(path.Points) == 0 {
		return geometrycore.Bounds{}
	}
	bounds := geometrycore.Bounds{TraceMin: path.Points[0].TraceIndex, TraceMax: path.Points[0].TraceIndex,
		XMin: path.Points[0].X, XMax: path.Points[0].X, YMin: path.Points[0].Y, YMax: path.Points[0].Y, HasXY: true}
	for _, point := range path.Points[1:] {
		if point.TraceIndex < bounds.TraceMin {
			bounds.TraceMin = point.TraceIndex
		}
		if point.TraceIndex > bounds.TraceMax {
			bounds.TraceMax = point.TraceIndex
		}
		bounds.XMin, bounds.XMax = math.Min(bounds.XMin, point.X), math.Max(bounds.XMax, point.X)
		bounds.YMin, bounds.YMax = math.Min(bounds.YMin, point.Y), math.Max(bounds.YMax, point.Y)
	}
	return bounds
}

func applyProjectCalibration(line *projectcore.CrookedProjectLine, raw *geometrycore.CrookedLineGeometry) (*geometrycore.CrookedLineGeometry, projectcore.CoordinateCalibration, error) {
	calibration := projectcore.CoordinateCalibration{Multiplier: 1, Accepted: true}
	if raw == nil {
		return nil, calibration, fmt.Errorf("测线道头几何为空")
	}
	if line == nil || len(line.Navigation) < 2 {
		return raw, calibration, nil
	}
	calibration = projectcore.InferCoordinateMultiplier(raw, line.Navigation)
	if !calibration.Accepted {
		reason := calibration.FailureReason
		if reason == "" {
			reason = "倍率或平移不明确"
		}
		return nil, calibration, fmt.Errorf("DAT 与 SEG-Y 道头坐标无法可靠校准（匹配 %d，残差 %.3g：%s）", calibration.Matches, calibration.Residual, reason)
	}
	geometry, err := projectcore.TransformGeometry(raw, calibration)
	if err != nil {
		return nil, calibration, err
	}
	return geometry, calibration, nil
}

func setCrookedProjectLineGeometry(state *crookedProjectLineState, raw *geometrycore.CrookedLineGeometry) error {
	if state == nil {
		return fmt.Errorf("测线状态为空")
	}
	geometry, calibration, err := applyProjectCalibration(state.line, raw)
	state.rawGeometry, state.calibration = raw, calibration
	state.multiplier = calibration.Multiplier
	if err != nil {
		state.geometry, state.mapPath, state.fingerprint, state.ready = nil, crookedMapPath{}, "", false
		state.errorText = err.Error()
		return err
	}
	state.geometry, state.ready, state.errorText = geometry, true, ""
	lineID := ""
	if state.line != nil {
		lineID = state.line.ID
	}
	state.fingerprint = crookedGeometryFingerprint(lineID, geometry)
	state.mapPath = canonicalCrookedMapPath(state, crookedCanonicalMapPointLimit)
	return nil
}

func crookedProjectResultMatches(result *crookedProjectGeometryResult, state *crookedProjectLineState, projectGen, specGen int64) bool {
	return result != nil && state != nil && state.line != nil && result.projectGen == projectGen && result.specGen == specGen && result.lineID != "" && result.lineID == state.line.ID
}

func crookedProjectGeometryComplete() bool {
	done, total := crookedProjectRecognitionProgress()
	return total > 0 && done == total
}

// crookedProjectLineNeedsGeometry is the single scheduling predicate for
// canonical project-map geometry.  Completed and explicitly failed lines are
// terminal until the user retries them; an in-flight line is never queued a
// second time.
func crookedProjectLineNeedsGeometry(state *crookedProjectLineState) bool {
	return state != nil && state.line != nil && state.line.Valid() && !state.ready && state.errorText == "" && !state.loading
}

// suspendCrookedPreparationForHide invalidates every worker generation and
// delivery slot together.  It deliberately preserves ready/error results,
// while clearing only transient loading state so Show can resume the missing
// header-only geometry without rescanning completed lines.
func suspendCrookedPreparationForHide() {
	atomic.AddInt64(&crookedLoadGen, 1)
	atomic.AddInt64(&crookedRenderGen, 1)
	atomic.AddInt64(&crookedProjectGen, 1)
	atomic.AddInt64(&crookedMapClickGen, 1)
	invalidateCrookedDeliveries(false)
	crookedState.loading = false
	crookedState.rendering = false
	crookedState.pendingNavigation = nil
	crookedState.pendingMapClick = nil
	crookedState.projectGeometrySuspended = crookedState.project != nil && !crookedState.projectGeometryReady
	if crookedState.project != nil {
		for i := range crookedState.projectLines {
			crookedState.projectLines[i].loading = false
		}
	}
}

func crookedProjectResumeActiveLine() int {
	index := crookedState.activeLine
	if index < 0 || index >= len(crookedState.projectLines) || !crookedProjectLineNeedsGeometry(&crookedState.projectLines[index]) {
		return -1
	}
	return index
}

// crookedActiveSectionPrepareDataset returns the active line only when its
// canonical map geometry is already terminal-ready but its foreground
// section preparation was interrupted.  This is distinct from project-map
// resume: a ready line must not be rescanned by a background worker, yet its
// reader/section still has to be restored after Hide cancelled the foreground
// load.
func crookedActiveSectionPrepareDataset() *dataset.SeismicDataset {
	if crookedState.project == nil || crookedState.geometry != nil || crookedState.loading || crookedState.activeLine < 0 || crookedState.activeLine >= len(crookedState.projectLines) {
		return nil
	}
	state := &crookedState.projectLines[crookedState.activeLine]
	if !state.ready || state.line == nil || !state.line.Valid() || state.line.Dataset != crookedState.dataset {
		return nil
	}
	return state.line.Dataset
}

func beginCrookedProjectLineLoading(state *crookedProjectLineState) {
	if state == nil {
		return
	}
	state.errorText = ""
	state.loading = true
}

// resumeCrookedProjectPreparation restarts the current project generation
// after Hide cancelled its workers.  Background jobs remain header-only and
// the scheduling predicate prevents ready, failed, or already-running lines
// from being duplicated.
func resumeCrookedProjectPreparation() {
	if crookedState.project == nil || crookedState.projectGeometryReady {
		crookedState.projectGeometrySuspended = false
		return
	}
	if crookedState.projectGeometrySuspended {
		for i := range crookedState.projectLines {
			crookedState.projectLines[i].loading = false
		}
	}
	crookedState.projectGeometrySuspended = false
	updateCrookedProjectPublication()
	if crookedState.projectGeometryReady {
		refreshCrookedLineList()
		return
	}
	if active := crookedProjectResumeActiveLine(); active >= 0 {
		activateCrookedProjectLine(active)
	}
	startCrookedProjectBackground(atomic.LoadInt64(&crookedProjectGen))
	updateCrookedProjectPublication()
	refreshCrookedLineList()
}

func updateCrookedProjectPublication() {
	ready := crookedProjectGeometryComplete()
	if ready == crookedState.projectGeometryReady {
		return
	}
	crookedState.projectGeometryReady = ready
	enabled := uintptr(0)
	if ready {
		enabled = 1
	}
	if crookedControlsUI.pseudo != 0 {
		pEnableWindow.Call(crookedControlsUI.pseudo, enabled)
	}
	if crookedControlsUI.spatialRange != 0 {
		pEnableWindow.Call(crookedControlsUI.spatialRange, enabled)
	}
	if crookedControlsUI.lines != 0 {
		pEnableWindow.Call(crookedControlsUI.lines, enabled)
	}
	if crookedControlsUI.trace != 0 {
		pEnableWindow.Call(crookedControlsUI.trace, enabled)
	}
	if ready {
		crookedState.geometryViewport = pseudo3dcore.XYRange{}
	}
	invalidateCrookedBase()
}

func setCrookedTraceInspectMode(enabled bool) {
	if crookedState.geometry == nil || len(crookedState.geometry.TraceIndices) == 0 {
		enabled = false
	}
	crookedState.traceInspectMode = enabled
	if crookedControlsUI.trace != 0 {
		state := uintptr(0)
		if enabled {
			state = BST_CHECKED
		}
		pSendMessageW.Call(crookedControlsUI.trace, BM_SETCHECK, state, 0)
	}
	if enabled {
		crookedState.rangeMode, crookedState.rangeDragging = false, false
		crookedState.timeRangeMode, crookedState.timeRangeDragging = false, false
		crookedState.mapZoomPending, crookedState.mapZoomDragging = false, false
		pReleaseCapture.Call()
		setText(crookedControlsUI.spatialRange, "范围")
		setText(crookedControlsUI.timeRange, "时间范围")
		setText(crookedControlsUI.status, "单道查看：在下方地震剖面中单击，可连续查看不同道；Esc 退出。")
	}
}

func crookedTraceAnalysisSelectionAt(x, y int) (traceAnalysisSelection, bool) {
	geometry := crookedState.geometry
	if geometry == nil || crookedState.dataset == nil || len(geometry.TraceIndices) == 0 || !rectContains(crookedSectionRect(), x, y) {
		return traceAnalysisSelection{}, false
	}
	position := clampInt(crookedPositionAtSectionX(x), 0, len(geometry.TraceIndices)-1)
	section := crookedSectionRect()
	fractionY := float64(clampInt(y-int(section.Top), 0, int(section.Bottom-section.Top))) / float64(maxInt(1, int(section.Bottom-section.Top)))
	sample := crookedState.sampleStart + int(math.Round(fractionY*float64(crookedState.sampleEnd-crookedState.sampleStart)))
	path := crookedState.dataset.Path
	lineName := filepath.Base(path)
	if crookedState.project != nil && crookedState.activeLine >= 0 && crookedState.activeLine < len(crookedState.projectLines) {
		if line := crookedState.projectLines[crookedState.activeLine].line; line != nil {
			lineName = line.Name
		}
	}
	context := fmt.Sprintf("弯线 %s | Trace %d", lineName, geometry.TraceIndices[position]+1)
	if position < len(geometry.HasCDP) && geometry.HasCDP[position] && position < len(geometry.CDP) {
		context += fmt.Sprintf(" | CDP %d", geometry.CDP[position])
	}
	if position < len(geometry.X) && position < len(geometry.Y) {
		context += fmt.Sprintf(" | X %.3f Y %.3f", geometry.X[position], geometry.Y[position])
	}
	return traceAnalysisSelection{Targets: []traceAnalysisTarget{{
		Role: "A", Path: path, Trace: geometry.TraceIndices[position],
		SampleStart: crookedState.sampleStart, SampleEnd: crookedState.sampleEnd, MarkerSample: sample,
	}}, Context: context}, true
}

func activateCrookedProjectLine(index int) bool {
	if index < 0 || index >= len(crookedState.projectLines) {
		return false
	}
	state := &crookedState.projectLines[index]
	if state.line == nil || !state.line.Valid() {
		return false
	}
	if index != crookedState.activeLine {
		saveCrookedActiveLineView()
		if crookedState.activeLine >= 0 && crookedState.activeLine < len(crookedState.projectLines) {
			crookedState.projectLines[crookedState.activeLine].loading = false
		}
	}
	crookedState.activeLine = index
	crookedState.axisMode = state.view.axisMode
	crookedState.viewStart, crookedState.viewEnd = state.view.viewStart, state.view.viewEnd
	crookedState.currentPosition, crookedState.currentSample = state.view.currentPosition, state.view.currentSample
	pSendMessageW.Call(crookedControlsUI.axis, CB_SETCURSEL, uintptr(crookedState.axisMode), 0)
	pSendMessageW.Call(crookedControlsUI.lines, LB_SETCURSEL, uintptr(index), 0)
	if state.loading && state.geometry == nil && state.errorText == "" {
		setText(crookedControlsUI.status, fmt.Sprintf("正在准备测线 %s…", state.line.Name))
		invalidateCrookedBase()
		return true
	}
	sampleEnd := state.line.Dataset.Metadata.SamplesPerTrace - 1
	if !startCrookedPrepare(state.line.Dataset, crookedState.workspaceGeneration, 0, sampleEnd) {
		return false
	}
	// A retry must stop contributing its previous terminal error to project
	// recognition before it becomes loading.  Otherwise loading+error is still
	// counted as done and can publish an incomplete project map.
	beginCrookedProjectLineLoading(state)
	updateCrookedProjectPublication()
	// startCrookedPrepare resets the view for a new dataset; restore this line's
	// in-session view before the asynchronous result can render.
	crookedState.axisMode = state.view.axisMode
	crookedState.viewStart, crookedState.viewEnd = state.view.viewStart, state.view.viewEnd
	crookedState.currentPosition, crookedState.currentSample = state.view.currentPosition, state.view.currentSample
	refreshCrookedLineList()
	invalidateCrookedBase()
	return true
}

func applyCrookedPendingNavigation(renderIfNeeded bool) bool {
	target := crookedState.pendingNavigation
	if target == nil || target.projectGeneration != atomic.LoadInt64(&crookedProjectGen) || target.specGeneration != atomic.LoadInt64(&crookedSpecGen) || target.lineIndex != crookedState.activeLine || crookedState.geometry == nil {
		return false
	}
	if target.lineIndex < 0 || target.lineIndex >= len(crookedState.projectLines) {
		crookedState.pendingNavigation = nil
		return false
	}
	state := &crookedState.projectLines[target.lineIndex]
	if state.line == nil || state.line.ID != target.lineID || state.fingerprint == "" || state.fingerprint != target.geometryFingerprint {
		crookedState.pendingNavigation = nil
		return false
	}
	geometry := crookedState.geometry
	if len(geometry.TraceIndices) == 0 {
		return false
	}
	position, best := 0, math.Inf(1)
	for i, trace := range geometry.TraceIndices {
		delta := math.Abs(float64(trace - target.traceIndex))
		if delta < best {
			position, best = i, delta
		}
	}
	oldPosition, oldSample := crookedState.currentPosition, crookedState.currentSample
	crookedState.currentPosition = position
	dt := 0
	if crookedState.dataset != nil {
		dt = crookedState.dataset.Metadata.SampleIntervalUS
	}
	if dt > 0 {
		crookedState.currentSample = clampInt(int(math.Round(target.timeMS*1000/float64(dt))), crookedState.sampleStart, crookedState.sampleEnd)
	}
	viewChanged := false
	if len(geometry.TraceIndices) > 1 {
		fraction := float64(position) / float64(len(geometry.TraceIndices)-1)
		if fraction < crookedState.viewStart || fraction > crookedState.viewEnd {
			span := math.Max(.01, math.Min(1, crookedState.viewEnd-crookedState.viewStart))
			start := fraction - span/2
			start = math.Max(0, math.Min(1-span, start))
			crookedState.viewStart, crookedState.viewEnd = start, start+span
			viewChanged = true
		}
	}
	crookedState.pendingNavigation = nil
	saveCrookedActiveLineView()
	updateCrookedStatus()
	publishCrookedCursorToPseudo(true)
	if renderIfNeeded && viewChanged {
		startCrookedRender()
	} else {
		invalidateCrookedCursorState(oldPosition, oldSample)
		invalidateCrookedCursorState(crookedState.currentPosition, crookedState.currentSample)
	}
	return true
}

func navigateCrookedFromPseudo(lineIndex int, traceIndex int64, timeMS float64) {
	if lineIndex < 0 || lineIndex >= len(crookedState.projectLines) {
		return
	}
	state := &crookedState.projectLines[lineIndex]
	if state.line == nil || state.fingerprint == "" {
		return
	}
	crookedState.pendingNavigation = &crookedNavigationTarget{projectGeneration: atomic.LoadInt64(&crookedProjectGen), specGeneration: atomic.LoadInt64(&crookedSpecGen), lineIndex: lineIndex,
		lineID: state.line.ID, traceIndex: traceIndex, geometryFingerprint: state.fingerprint, timeMS: timeMS}
	if lineIndex == crookedState.activeLine && crookedState.geometry != nil {
		applyCrookedPendingNavigation(true)
	} else if !activateCrookedProjectLine(lineIndex) {
		crookedState.pendingNavigation = nil
		return
	}
	if pseudoHwnd != 0 {
		pShowWindow.Call(pseudoHwnd, SW_HIDE)
	}
	showTopLevelWindowRestored(crookedHwnd)
}

func startCrookedProjectBackground(projectGen int64) {
	jobs := make(chan crookedProjectGeometryJob)
	deliveryEpoch := crookedDeliverySnapshot()
	jobList := make([]crookedProjectGeometryJob, 0, len(crookedState.projectLines))
	for i := range crookedState.projectLines {
		state := &crookedState.projectLines[i]
		if i == crookedState.activeLine || !crookedProjectLineNeedsGeometry(state) {
			continue
		}
		state.loading = true
		jobList = append(jobList, crookedProjectGeometryJob{index: i, lineID: state.line.ID, line: state.line, spec: crookedState.spec, specGen: atomic.LoadInt64(&crookedSpecGen)})
	}
	refreshCrookedLineList()
	worker := func() {
		for item := range jobs {
			result := &crookedProjectGeometryResult{deliveryEpoch: deliveryEpoch, projectGen: projectGen, specGen: item.specGen, lineIndex: item.index, lineID: item.lineID}
			reader, err := item.line.Dataset.OpenReader()
			if err == nil {
				build, buildErr := geometrycore.BuildCrookedCachedProgress(reader, item.spec, maxInt(1, runtime.NumCPU()/2), nil)
				_ = reader.Close()
				result.build, result.geometry, result.err = build, build.Geometry, buildErr
			} else {
				result.err = err
			}
			postCrookedProjectGeometry(result)
		}
	}
	go worker()
	go worker()
	go func() {
		defer close(jobs)
		for _, item := range jobList {
			if projectGen != atomic.LoadInt64(&crookedProjectGen) {
				return
			}
			jobs <- item
		}
	}()
}

func postCrookedProjectGeometry(result *crookedProjectGeometryResult) {
	if result == nil {
		return
	}
	var stale *crookedPrepareResult
	crookedPendingMu.Lock()
	if crookedDeliveryHwnd == 0 || result.deliveryEpoch != crookedDeliveryEpoch || result.projectGen != atomic.LoadInt64(&crookedProjectGen) || result.specGen != atomic.LoadInt64(&crookedSpecGen) {
		crookedPendingMu.Unlock()
		return
	}
	crookedProjectPending = append(crookedProjectPending, result)
	if !crookedPostWindowMessage(crookedDeliveryHwnd, WM_CROOKED_PROJECT_READY, 0, 0) {
		stale = failCrookedDeliveryLocked()
	}
	crookedPendingMu.Unlock()
	closeCrookedPrepareReader(stale)
}

func handleCrookedProjectGeometry() {
	crookedPendingMu.Lock()
	results := crookedProjectPending
	crookedProjectPending = nil
	deliveryEpoch, deliveryActive := crookedDeliveryEpoch, crookedDeliveryHwnd != 0
	crookedPendingMu.Unlock()
	projectGen := atomic.LoadInt64(&crookedProjectGen)
	specGen := atomic.LoadInt64(&crookedSpecGen)
	for _, result := range results {
		if result == nil || !deliveryActive || result.deliveryEpoch != deliveryEpoch || result.lineIndex < 0 || result.lineIndex >= len(crookedState.projectLines) {
			continue
		}
		state := &crookedState.projectLines[result.lineIndex]
		if !crookedProjectResultMatches(result, state, projectGen, specGen) {
			continue
		}
		state.loading = false
		if result.err != nil {
			state.errorText = result.err.Error()
			writeWorkspaceTrace(workspacecore.TraceEvent{Action: "crooked_project_line_failed", Workspace: workspacecore.KindCrooked, Dataset: filepath.Base(state.line.Path), Geometry: geometrycore.KindUnknown, Generation: crookedState.workspaceGeneration, Err: result.err})
		} else {
			if err := setCrookedProjectLineGeometry(state, result.geometry); err != nil {
				writeWorkspaceTrace(workspacecore.TraceEvent{Action: "crooked_project_line_calibration_failed", Workspace: workspacecore.KindCrooked, Dataset: filepath.Base(state.line.Path), Geometry: geometrycore.KindUnknown, Generation: crookedState.workspaceGeneration, Err: err})
			} else {
				writeWorkspaceTrace(workspacecore.TraceEvent{Action: "crooked_project_line_ready", Workspace: workspacecore.KindCrooked, Dataset: filepath.Base(state.line.Path), Geometry: geometrycore.KindCrookedLine, Generation: crookedState.workspaceGeneration})
			}
		}
		if result.lineIndex == crookedState.activeLine && crookedState.geometry == nil {
			activateCrookedProjectLine(result.lineIndex)
		}
	}
	updateCrookedProjectPublication()
	refreshCrookedLineList()
	if crookedState.geometry != nil {
		updateCrookedStatus()
	} else if done, total := crookedProjectRecognitionProgress(); total > 0 {
		setText(crookedControlsUI.status, fmt.Sprintf("正在识别项目几何：已识别 %d/%d", done, total))
	}
	invalidateCrookedBase()
}

func startCrookedPrepare(data *dataset.SeismicDataset, workspaceGeneration uint64, sampleStart, sampleEnd int) bool {
	if data == nil || crookedHwnd == 0 {
		return false
	}
	gen := atomic.AddInt64(&crookedLoadGen, 1)
	projectGen, specGen := atomic.LoadInt64(&crookedProjectGen), atomic.LoadInt64(&crookedSpecGen)
	deliveryEpoch := crookedDeliverySnapshot()
	lineIndex, lineID := -1, ""
	if crookedState.activeLine >= 0 && crookedState.activeLine < len(crookedState.projectLines) {
		state := &crookedState.projectLines[crookedState.activeLine]
		if state.line != nil && state.line.Dataset == data {
			lineIndex, lineID = crookedState.activeLine, state.line.ID
		}
	}
	atomic.AddInt64(&crookedRenderGen, 1)
	if crookedState.reader != nil {
		_ = crookedState.reader.Close()
		crookedState.reader = nil
	}
	crookedState.dataset = data
	crookedState.workspaceGeneration = workspaceGeneration
	crookedState.path = data.Path
	crookedState.geometry = nil
	crookedState.sampleStart = sampleStart
	crookedState.sampleEnd = sampleEnd
	crookedState.viewStart, crookedState.viewEnd = 0, 1
	crookedState.currentPosition = 0
	crookedState.currentSample = sampleStart
	crookedState.indices, crookedState.bgra = nil, nil
	crookedState.errorText = ""
	crookedState.loading, crookedState.rendering = true, false
	crookedState.progress = 0
	pSendMessageW.Call(crookedControlsUI.progress, PBM_SETPOS, 0, 0)
	setText(crookedControlsUI.status, "正在识别弯线坐标道头...")
	layoutCrookedControls()
	invalidateCrookedBase()
	spec := crookedState.spec
	writeWorkspaceTrace(workspacecore.TraceEvent{Action: "crooked_index_begin", Workspace: workspacecore.KindCrooked, Dataset: data.Basename(), Geometry: geometrycore.KindUnknown, Generation: workspaceGeneration})
	go func(loadGen, capturedProjectGen, capturedSpecGen, capturedDeliveryEpoch int64, capturedLineIndex int, capturedLineID string, source *dataset.SeismicDataset, coordinateSpec segy.TraceCoordinateSpec) {
		result := &crookedPrepareResult{deliveryEpoch: capturedDeliveryEpoch, gen: loadGen, projectGen: capturedProjectGen, specGen: capturedSpecGen, lineIndex: capturedLineIndex, lineID: capturedLineID, dataset: source}
		reader, err := source.OpenReader()
		if err != nil {
			result.err = err
			postCrookedReady(result)
			return
		}
		postCrookedProgress(loadGen, 8)
		build, err := geometrycore.BuildCrookedCachedProgress(reader, coordinateSpec, runtime.NumCPU(), func(done, total int) {
			if total > 0 {
				postCrookedProgress(loadGen, 10+80*done/total)
			}
		})
		if err != nil {
			_ = reader.Close()
			result.err = err
			result.build = build
			postCrookedReady(result)
			return
		}
		result.reader, result.geometry, result.build = reader, build.Geometry, build
		postCrookedProgress(loadGen, 92)
		postCrookedReady(result)
	}(gen, projectGen, specGen, deliveryEpoch, lineIndex, lineID, data, spec)
	return true
}

func postCrookedProgress(gen int64, percent int) {
	var stale *crookedPrepareResult
	crookedPendingMu.Lock()
	if crookedDeliveryHwnd != 0 && gen == atomic.LoadInt64(&crookedLoadGen) {
		if !crookedPostWindowMessage(crookedDeliveryHwnd, WM_CROOKED_PROGRESS, uintptr(clampInt(percent, 0, 100)), uintptr(gen)) {
			stale = failCrookedDeliveryLocked()
		}
	}
	crookedPendingMu.Unlock()
	closeCrookedPrepareReader(stale)
}

func postCrookedReady(result *crookedPrepareResult) {
	if result == nil {
		return
	}
	var stale, failed *crookedPrepareResult
	crookedPendingMu.Lock()
	if crookedDeliveryHwnd == 0 || result.deliveryEpoch != crookedDeliveryEpoch || result.gen != atomic.LoadInt64(&crookedLoadGen) || result.projectGen != atomic.LoadInt64(&crookedProjectGen) || result.specGen != atomic.LoadInt64(&crookedSpecGen) {
		crookedPendingMu.Unlock()
		closeCrookedPrepareReader(result)
		return
	}
	stale = crookedPending
	crookedPending = result
	if !crookedPostWindowMessage(crookedDeliveryHwnd, WM_CROOKED_READY, 0, 0) {
		failed = failCrookedDeliveryLocked()
	}
	crookedPendingMu.Unlock()
	if stale != failed {
		closeCrookedPrepareReader(stale)
	}
	closeCrookedPrepareReader(failed)
}

func handleCrookedReady() {
	crookedPendingMu.Lock()
	result := crookedPending
	crookedPending = nil
	currentDelivery := result != nil && result.deliveryEpoch == crookedDeliveryEpoch && crookedDeliveryHwnd != 0
	crookedPendingMu.Unlock()
	if !currentDelivery || result.gen != atomic.LoadInt64(&crookedLoadGen) {
		closeCrookedPrepareReader(result)
		return
	}
	var projectState *crookedProjectLineState
	if result.lineID != "" {
		if result.projectGen != atomic.LoadInt64(&crookedProjectGen) || result.specGen != atomic.LoadInt64(&crookedSpecGen) || result.lineIndex < 0 || result.lineIndex >= len(crookedState.projectLines) {
			if result.reader != nil {
				_ = result.reader.Close()
			}
			return
		}
		projectState = &crookedState.projectLines[result.lineIndex]
		if projectState.line == nil || projectState.line.ID != result.lineID || projectState.line.Dataset != result.dataset || result.lineIndex != crookedState.activeLine {
			if result.reader != nil {
				_ = result.reader.Close()
			}
			return
		}
	}
	crookedState.loading = false
	if result.err != nil {
		crookedState.errorText = result.err.Error()
		crookedState.rendering = false
		setText(crookedControlsUI.status, "弯线坐标识别失败："+result.err.Error()+"；请检查道头设置后重试。")
		writeWorkspaceTrace(workspacecore.TraceEvent{Action: "crooked_index_failed", Workspace: workspacecore.KindCrooked, Dataset: result.dataset.Basename(), Geometry: geometrycore.KindUnknown, Generation: crookedState.workspaceGeneration, Err: result.err})
		if projectState != nil {
			projectState.loading, projectState.ready, projectState.errorText = false, false, result.err.Error()
			refreshCrookedLineList()
			updateCrookedProjectPublication()
		}
		layoutCrookedControls()
		invalidateCrookedBase()
		return
	}
	result.dataset.SetGeometry(result.geometry)
	displayGeometry := result.geometry
	if projectState != nil {
		if err := setCrookedProjectLineGeometry(projectState, result.geometry); err != nil {
			_ = result.reader.Close()
			projectState.loading = false
			crookedState.geometry, crookedState.errorText, crookedState.rendering = nil, err.Error(), false
			setText(crookedControlsUI.status, "测线坐标校准失败："+err.Error())
			writeWorkspaceTrace(workspacecore.TraceEvent{Action: "crooked_coordinate_calibration_failed", Workspace: workspacecore.KindCrooked, Dataset: result.dataset.Basename(), Geometry: geometrycore.KindUnknown, Generation: crookedState.workspaceGeneration, Err: err})
			refreshCrookedLineList()
			updateCrookedProjectPublication()
			layoutCrookedControls()
			invalidateCrookedBase()
			return
		}
		displayGeometry = projectState.geometry
		projectState.loading = false
		calibration := projectState.calibration
		writeWorkspaceTrace(workspacecore.TraceEvent{Action: fmt.Sprintf("crooked_coordinate_transform_%.3g", calibration.Multiplier), Workspace: workspacecore.KindCrooked, Dataset: result.dataset.Basename(), Geometry: geometrycore.KindCrookedLine, Generation: crookedState.workspaceGeneration})
		refreshCrookedLineList()
		updateCrookedProjectPublication()
	}
	crookedState.reader = result.reader
	crookedState.geometry = displayGeometry
	crookedState.errorText = ""
	pEnableWindow.Call(crookedControlsUI.trace, 1)
	action := "crooked_index_ready"
	if result.build.Stats.FromCache {
		action = "crooked_index_cache_hit"
	}
	writeWorkspaceTrace(workspacecore.TraceEvent{Action: action, Workspace: workspacecore.KindCrooked, Dataset: result.dataset.Basename(), Geometry: geometrycore.KindCrookedLine, Generation: crookedState.workspaceGeneration})
	if len(displayGeometry.TraceIndices) > 0 && (crookedState.currentPosition < 0 || crookedState.currentPosition >= len(displayGeometry.TraceIndices)) {
		crookedState.currentPosition = len(displayGeometry.TraceIndices) / 2
	}
	if crookedState.currentSample < crookedState.sampleStart || crookedState.currentSample > crookedState.sampleEnd {
		crookedState.currentSample = crookedState.sampleStart
	}
	applyCrookedPendingNavigation(false)
	invalidateCrookedBase()
	startCrookedRender()
	publishCrookedCursorToPseudo(true)
}

func crookedAxisPositions(geometry *geometrycore.CrookedLineGeometry, axisMode int) ([]float64, bool) {
	if geometry == nil {
		return nil, false
	}
	positions := make([]float64, len(geometry.TraceIndices))
	switch axisMode {
	case crookedAxisDistance:
		copy(positions, geometry.Distance)
		return positions, true
	case crookedAxisCDP:
		if geometry.CDPMonotonic() {
			descending := geometry.CDP[len(geometry.CDP)-1] < geometry.CDP[0]
			for i, value := range geometry.CDP {
				positions[i] = float64(value)
				if descending {
					positions[i] = -positions[i]
				}
			}
			return positions, true
		}
	}
	for i := range positions {
		positions[i] = float64(i)
	}
	return positions, axisMode != crookedAxisCDP
}

func crookedVisibleAxisRange(positions []float64) (float64, float64) {
	if len(positions) < 2 {
		return 0, 1
	}
	start, end := crookedState.viewStart, crookedState.viewEnd
	start = math.Max(0, math.Min(1, start))
	end = math.Max(start+0.0001, math.Min(1, end))
	minimum, maximum := positions[0], positions[len(positions)-1]
	return minimum + start*(maximum-minimum), minimum + end*(maximum-minimum)
}

func startCrookedRender() {
	if crookedHwnd == 0 || crookedState.dataset == nil || crookedState.geometry == nil {
		return
	}
	rect := crookedSectionRect()
	width := clampInt(int(rect.Right-rect.Left), 2, 1800)
	height := clampInt(int(rect.Bottom-rect.Top), 2, 1400)
	positions, _ := crookedAxisPositions(crookedState.geometry, crookedState.axisMode)
	positionStart, positionEnd := crookedVisibleAxisRange(positions)
	traceIndices := append([]int64(nil), crookedState.geometry.TraceIndices...)
	positions = append([]float64(nil), positions...)
	path := crookedState.path
	sampleStart, sampleEnd := crookedState.sampleStart, crookedState.sampleEnd
	gain, palette := crookedState.gainPercent, crookedState.paletteIndex
	gen := atomic.AddInt64(&crookedRenderGen, 1)
	progressGen := atomic.LoadInt64(&crookedLoadGen)
	deliveryEpoch := crookedDeliverySnapshot()
	crookedState.rendering = true
	crookedState.progress = maxInt(crookedState.progress, 92)
	layoutCrookedControls()
	go func(renderGen, loadGen, capturedDeliveryEpoch int64) {
		result := &crookedRenderResult{deliveryEpoch: capturedDeliveryEpoch, gen: renderGen, width: width, height: height}
		reader, err := segy.Open(path)
		if err != nil {
			result.err = err
			postCrookedRenderReady(result)
			return
		}
		defer reader.Close()
		options := segy.RenderOptions{Width: width, Height: height, AGC: agc, ClipPercent: clipPercent, GainPercent: gain,
			UseValueLimits: false, SampleStart: sampleStart, SampleEnd: sampleEnd, DisplayMode: renderDisplayMode,
			Progress: func(done, total int) {
				if total > 0 {
					postCrookedProgress(loadGen, 92+8*done/total)
				}
			}}
		pixels, stats, err := reader.RenderTracePositions(traceIndices, positions, positionStart, positionEnd, options)
		if err != nil {
			result.err = err
		} else {
			result.indices, result.bgra, result.stats = pixels, crookedPaletteBGRA(pixels, palette), stats
		}
		postCrookedRenderReady(result)
	}(gen, progressGen, deliveryEpoch)
}

func postCrookedRenderReady(result *crookedRenderResult) {
	if result == nil {
		return
	}
	var stale *crookedPrepareResult
	crookedPendingMu.Lock()
	if crookedDeliveryHwnd == 0 || result.deliveryEpoch != crookedDeliveryEpoch || result.gen != atomic.LoadInt64(&crookedRenderGen) {
		crookedPendingMu.Unlock()
		return
	}
	if crookedRenderPending == nil || result.gen >= crookedRenderPending.gen {
		crookedRenderPending = result
	}
	if !crookedPostWindowMessage(crookedDeliveryHwnd, WM_CROOKED_RENDER_READY, 0, 0) {
		stale = failCrookedDeliveryLocked()
	}
	crookedPendingMu.Unlock()
	closeCrookedPrepareReader(stale)
}

func handleCrookedRenderReady() {
	crookedPendingMu.Lock()
	result := crookedRenderPending
	crookedRenderPending = nil
	currentDelivery := result != nil && result.deliveryEpoch == crookedDeliveryEpoch && crookedDeliveryHwnd != 0
	crookedPendingMu.Unlock()
	if !currentDelivery || result.gen != atomic.LoadInt64(&crookedRenderGen) {
		return
	}
	crookedState.rendering = false
	if result.err != nil {
		crookedState.errorText = result.err.Error()
		setText(crookedControlsUI.status, "弯线剖面渲染失败："+result.err.Error())
		layoutCrookedControls()
		return
	}
	crookedState.indices, crookedState.bgra = result.indices, result.bgra
	crookedState.imageWidth, crookedState.imageHeight, crookedState.stats = result.width, result.height, result.stats
	crookedState.progress = 100
	pSendMessageW.Call(crookedControlsUI.progress, PBM_SETPOS, 100, 0)
	updateCrookedStatus()
	layoutCrookedControls()
	invalidateCrookedBase()
}

func updateCrookedStatus() {
	geometry := crookedState.geometry
	if geometry == nil || len(geometry.TraceIndices) == 0 {
		return
	}
	position := clampInt(crookedState.currentPosition, 0, len(geometry.TraceIndices)-1)
	dt := 0
	if crookedState.reader != nil {
		dt = crookedState.reader.Info.SampleIntervalUS
	} else if crookedState.dataset != nil {
		dt = crookedState.dataset.Metadata.SampleIntervalUS
	}
	timeMS := float64(crookedState.currentSample) * float64(dt) / 1000
	cdp := "-"
	if position < len(geometry.HasCDP) && geometry.HasCDP[position] {
		cdp = strconv.FormatInt(int64(geometry.CDP[position]), 10)
	}
	unit := "坐标单位"
	if geometry.CoordinateUnits == 1 {
		unit = "长度单位"
	}
	lineName := filepath.Base(crookedState.path)
	if crookedState.activeLine >= 0 && crookedState.activeLine < len(crookedState.projectLines) && crookedState.projectLines[crookedState.activeLine].line != nil {
		lineName = crookedState.projectLines[crookedState.activeLine].line.Name
	}
	projectProgress := ""
	if done, total := crookedProjectRecognitionProgress(); total > 1 && done < total {
		projectProgress = fmt.Sprintf(" | 已识别 %d/%d", done, total)
	}
	timeWindow := ""
	if selected := crookedState.pseudoTimeRange.Normalized(); selected.Valid {
		timeWindow = fmt.Sprintf(" | 伪时间 %.3g–%.3g ms", selected.StartMS, selected.EndMS)
	}
	setText(crookedControlsUI.status, fmt.Sprintf("%s | Trace %d | CDP %s | Distance %.3f %s | X %.3f Y %.3f | Time %.3f ms | Gain %.0f%% | %s%s%s",
		lineName, geometry.TraceIndices[position]+1, cdp, geometry.Distance[position], unit, geometry.X[position], geometry.Y[position], timeMS, crookedState.gainPercent, paletteNames[crookedState.paletteIndex], timeWindow, projectProgress))
}

func currentCrookedLinkedCursor() (pseudo3dcore.LinkedCursor, bool) {
	geometry := crookedState.geometry
	lineIndex := crookedState.activeLine
	if geometry == nil || lineIndex < 0 || lineIndex >= len(crookedState.projectLines) || len(geometry.TraceIndices) == 0 {
		return pseudo3dcore.LinkedCursor{}, false
	}
	line := crookedState.projectLines[lineIndex].line
	if line == nil {
		return pseudo3dcore.LinkedCursor{}, false
	}
	position := clampInt(crookedState.currentPosition, 0, len(geometry.TraceIndices)-1)
	target := pseudo3dcore.LinkedCursor{ProjectGeneration: crookedState.workspaceGeneration, LineID: line.ID, Position: position,
		TraceIndex: geometry.TraceIndices[position], X: geometry.X[position], Y: geometry.Y[position], Distance: geometry.Distance[position],
		Sample: crookedState.currentSample, Valid: true}
	if position < len(geometry.CDP) && position < len(geometry.HasCDP) && geometry.HasCDP[position] {
		target.CDP, target.HasCDP = geometry.CDP[position], true
	}
	if crookedState.dataset != nil && crookedState.dataset.Metadata.SampleIntervalUS > 0 {
		target.TimeMS = float64(target.Sample) * float64(crookedState.dataset.Metadata.SampleIntervalUS) / 1000
	}
	return target, true
}

func publishCrookedCursorToPseudo(force bool) {
	target, ok := currentCrookedLinkedCursor()
	if !ok || pseudoHwnd == 0 || pseudoState.project != crookedState.project {
		return
	}
	last := crookedState.lastPseudoCursor
	if !force && last.Valid && last.ProjectGeneration == target.ProjectGeneration && last.LineID == target.LineID && last.TraceIndex == target.TraceIndex && last.Sample == target.Sample {
		return
	}
	if !force && !crookedState.lastPseudoCursorAt.IsZero() && time.Since(crookedState.lastPseudoCursorAt) < 33*time.Millisecond {
		return
	}
	crookedState.lastPseudoCursor, crookedState.lastPseudoCursorAt = target, time.Now()
	applyPseudoLinkedCursor(target)
}

func crookedProjectRecognitionProgress() (int, int) {
	done, total := 0, 0
	for _, state := range crookedState.projectLines {
		if state.line == nil || !state.line.Valid() {
			continue
		}
		total++
		if state.ready || state.errorText != "" {
			done++
		}
	}
	return done, total
}

type crookedMapTransform struct {
	scale, offsetX, offsetY float64
	bounds                  geometrycore.Bounds
	rect                    RECT
}

func makeCrookedMapTransform(rect RECT, bounds geometrycore.Bounds) crookedMapTransform {
	width, height := float64(rect.Right-rect.Left), float64(rect.Bottom-rect.Top)
	dx, dy := bounds.XMax-bounds.XMin, bounds.YMax-bounds.YMin
	if dx <= 0 {
		dx = 1
	}
	if dy <= 0 {
		dy = 1
	}
	scale := 0.88 * math.Min(width/dx, height/dy)
	contentWidth, contentHeight := dx*scale, dy*scale
	return crookedMapTransform{scale: scale, offsetX: float64(rect.Left) + (width-contentWidth)/2 - bounds.XMin*scale,
		offsetY: float64(rect.Top) + (height-contentHeight)/2 + bounds.YMax*scale, bounds: bounds, rect: rect}
}

func (transform crookedMapTransform) toPixel(x, y float64) (int, int) {
	return int(math.Round(transform.offsetX + x*transform.scale)), int(math.Round(transform.offsetY - y*transform.scale))
}

func (transform crookedMapTransform) toWorld(x, y int) (float64, float64) {
	return (float64(x) - transform.offsetX) / transform.scale, (transform.offsetY - float64(y)) / transform.scale
}

func drawCrookedFrame(hdc uintptr, rect RECT, color uintptr) {
	pen, _, _ := pCreatePen.Call(PS_SOLID, 1, color)
	old, _, _ := pSelectObject.Call(hdc, pen)
	pMoveToEx.Call(hdc, uintptr(rect.Left), uintptr(rect.Top), 0)
	pLineTo.Call(hdc, uintptr(rect.Right), uintptr(rect.Top))
	pLineTo.Call(hdc, uintptr(rect.Right), uintptr(rect.Bottom))
	pLineTo.Call(hdc, uintptr(rect.Left), uintptr(rect.Bottom))
	pLineTo.Call(hdc, uintptr(rect.Left), uintptr(rect.Top))
	pSelectObject.Call(hdc, old)
	pDeleteObject.Call(pen)
}

func crookedProjectBounds() geometrycore.Bounds {
	if crookedState.project != nil && !crookedState.projectGeometryReady {
		return geometrycore.Bounds{}
	}
	bounds := geometrycore.Bounds{}
	for _, state := range crookedState.projectLines {
		lineBounds := crookedMapPathBounds(state.mapPath)
		if !lineBounds.HasXY {
			continue
		}
		if !bounds.HasXY {
			bounds = lineBounds
		} else {
			bounds.XMin, bounds.XMax = math.Min(bounds.XMin, lineBounds.XMin), math.Max(bounds.XMax, lineBounds.XMax)
			bounds.YMin, bounds.YMax = math.Min(bounds.YMin, lineBounds.YMin), math.Max(bounds.YMax, lineBounds.YMax)
		}
	}
	if !bounds.HasXY && crookedState.geometry != nil {
		return crookedState.geometry.Bounds()
	}
	return bounds
}

func crookedMapViewBounds() geometrycore.Bounds {
	projectBounds := crookedProjectBounds()
	view := crookedState.geometryViewport.Normalized()
	if !projectBounds.HasXY || !view.Valid {
		return projectBounds
	}
	view.XMin, view.XMax = math.Max(view.XMin, projectBounds.XMin), math.Min(view.XMax, projectBounds.XMax)
	view.YMin, view.YMax = math.Max(view.YMin, projectBounds.YMin), math.Min(view.YMax, projectBounds.YMax)
	view = view.Normalized()
	if !view.Valid {
		return projectBounds
	}
	return geometrycore.Bounds{XMin: view.XMin, XMax: view.XMax, YMin: view.YMin, YMax: view.YMax, HasXY: true}
}

func resetCrookedMapView() {
	crookedState.geometryViewport = pseudo3dcore.XYRange{}
	invalidateCrookedBase()
}

func finishCrookedMapZoom(x, y int) bool {
	if !crookedState.mapZoomDragging {
		return false
	}
	mapRect := crookedMapRect()
	x = clampInt(x, int(mapRect.Left), int(mapRect.Right))
	y = clampInt(y, int(mapRect.Top), int(mapRect.Bottom))
	view, project := crookedMapViewBounds(), crookedProjectBounds()
	if !view.HasXY || !project.HasXY {
		return false
	}
	transform := makeCrookedMapTransform(mapRect, view)
	x0, y0 := transform.toWorld(crookedState.mapZoomStartX, crookedState.mapZoomStartY)
	x1, y1 := transform.toWorld(x, y)
	selected := (pseudo3dcore.XYRange{XMin: x0, XMax: x1, YMin: y0, YMax: y1, Valid: true}).Normalized()
	if !selected.Valid {
		return false
	}
	// Expand the shorter world axis around the selection centre so the map
	// keeps its aspect ratio instead of stretching the project coordinates.
	mapAspect := float64(maxInt(1, int(mapRect.Right-mapRect.Left))) / float64(maxInt(1, int(mapRect.Bottom-mapRect.Top)))
	dx, dy := selected.XMax-selected.XMin, selected.YMax-selected.YMin
	if dx/dy < mapAspect {
		half := dy * mapAspect / 2
		center := (selected.XMin + selected.XMax) / 2
		selected.XMin, selected.XMax = center-half, center+half
	} else {
		half := dx / mapAspect / 2
		center := (selected.YMin + selected.YMax) / 2
		selected.YMin, selected.YMax = center-half, center+half
	}
	shiftRange := func(minimum, maximum, projectMinimum, projectMaximum float64) (float64, float64) {
		span := maximum - minimum
		projectSpan := projectMaximum - projectMinimum
		if span >= projectSpan {
			return projectMinimum, projectMaximum
		}
		if minimum < projectMinimum {
			maximum += projectMinimum - minimum
			minimum = projectMinimum
		}
		if maximum > projectMaximum {
			minimum -= maximum - projectMaximum
			maximum = projectMaximum
		}
		return minimum, maximum
	}
	selected.XMin, selected.XMax = shiftRange(selected.XMin, selected.XMax, project.XMin, project.XMax)
	selected.YMin, selected.YMax = shiftRange(selected.YMin, selected.YMax, project.YMin, project.YMax)
	selected = selected.Normalized()
	if !selected.Valid {
		return false
	}
	crookedState.geometryViewport = selected
	setText(crookedControlsUI.status, "Geometry 总图已放大；双击总图恢复全区。")
	invalidateCrookedBase()
	return true
}

func drawCrookedProjectLine(hdc uintptr, transform crookedMapTransform, index int, color uintptr, width int) {
	if index < 0 || index >= len(crookedState.projectLines) {
		return
	}
	state := &crookedState.projectLines[index]
	path := state.mapPath
	if len(path.Points) < 2 {
		return
	}
	saved, _, _ := pSaveDC.Call(hdc)
	pIntersectClipRect.Call(hdc, uintptr(transform.rect.Left), uintptr(transform.rect.Top), uintptr(transform.rect.Right+1), uintptr(transform.rect.Bottom+1))
	defer func() {
		if saved != 0 {
			pRestoreDC.Call(hdc, saved)
		}
	}()
	pen, _, _ := pCreatePen.Call(PS_SOLID, uintptr(width), color)
	old, _, _ := pSelectObject.Call(hdc, pen)
	for i, point := range path.Points {
		x, y := transform.toPixel(point.X, point.Y)
		if i == 0 {
			pMoveToEx.Call(hdc, uintptr(x), uintptr(y), 0)
		} else {
			pLineTo.Call(hdc, uintptr(x), uintptr(y))
		}
	}
	pSelectObject.Call(hdc, old)
	pDeleteObject.Call(pen)
}

func drawCrookedStaticScene(hdc uintptr) {
	client := clientRect(crookedHwnd)
	white, _, _ := pGetStockObject.Call(WHITE_BRUSH)
	pFillRect.Call(hdc, uintptr(unsafe.Pointer(&client)), white)
	if hFont != 0 {
		pSelectObject.Call(hdc, hFont)
	}
	pSetBkMode.Call(hdc, TRANSPARENT)
	mapRect, sectionRect := crookedMapRect(), crookedSectionRect()
	if crookedState.styleMode != 3 {
		drawCrookedFrame(hdc, mapRect, rgbRef(148, 163, 184))
	}
	drawCrookedFrame(hdc, sectionRect, rgbRef(100, 116, 139))
	drawHomeText(hdc, hFont, rgbRef(15, 23, 42), "XY 测线总图", RECT{Left: mapRect.Left, Top: mapRect.Top - 24, Right: mapRect.Right, Bottom: mapRect.Top - 3}, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	drawHomeText(hdc, hFont, rgbRef(15, 23, 42), "活动测线叠后地震剖面", RECT{Left: sectionRect.Left, Top: sectionRect.Top - 24, Right: sectionRect.Right, Bottom: sectionRect.Top - 3}, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	if crookedState.styleMode == 2 {
		for i := 1; i < 5; i++ {
			x := int(mapRect.Left) + i*int(mapRect.Right-mapRect.Left)/5
			y := int(mapRect.Top) + i*int(mapRect.Bottom-mapRect.Top)/5
			drawHomeLine(hdc, x, int(mapRect.Top), x, int(mapRect.Bottom), rgbRef(226, 232, 240), 1)
			drawHomeLine(hdc, int(mapRect.Left), y, int(mapRect.Right), y, rgbRef(226, 232, 240), 1)
		}
	}
	bounds := crookedMapViewBounds()
	if bounds.HasXY {
		transform := makeCrookedMapTransform(mapRect, bounds)
		for i := range crookedState.projectLines {
			if i != crookedState.activeLine {
				drawCrookedProjectLine(hdc, transform, i, rgbRef(148, 163, 184), 1)
			}
		}
		if crookedState.activeLine >= 0 {
			color, width := rgbRef(37, 99, 235), 2
			if crookedState.styleMode == 3 {
				color, width = rgbRef(100, 116, 139), 1
			} else if crookedState.styleMode == 1 {
				width = 3
			}
			drawCrookedProjectLine(hdc, transform, crookedState.activeLine, color, width)
		}
		if crookedState.styleMode == 1 || crookedState.styleMode == 2 {
			drawHomeText(hdc, hFont, rgbRef(71, 85, 105), fmt.Sprintf("X %.3f – %.3f    Y %.3f – %.3f", bounds.XMin, bounds.XMax, bounds.YMin, bounds.YMax),
				RECT{Left: mapRect.Left + 6, Top: mapRect.Bottom - 22, Right: mapRect.Right - 6, Bottom: mapRect.Bottom - 3}, DT_RIGHT|DT_VCENTER|DT_SINGLELINE)
		}
	} else {
		drawHomeText(hdc, hFont, rgbRef(100, 116, 139), "正在读取项目测线坐标…", mapRect, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
	}
	geometry := crookedState.geometry
	if geometry == nil {
		text, color := "正在读取活动线道头并建立弯线索引…", rgbRef(100, 116, 139)
		if crookedState.errorText != "" {
			text, color = "活动线加载失败，请检查道头设置后重试。", rgbRef(185, 28, 28)
		}
		drawHomeText(hdc, hFont, color, text, sectionRect, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
		return
	}
	if len(crookedState.bgra) == crookedState.imageWidth*crookedState.imageHeight*4 && len(crookedState.bgra) > 0 {
		bitmap := BITMAPINFO{Header: BITMAPINFOHEADER{Size: uint32(unsafe.Sizeof(BITMAPINFOHEADER{})), Width: int32(crookedState.imageWidth), Height: -int32(crookedState.imageHeight), Planes: 1, BitCount: 32, Compression: BI_RGB, SizeImage: uint32(len(crookedState.bgra))}}
		pStretchDIBits.Call(hdc, uintptr(sectionRect.Left), uintptr(sectionRect.Top), uintptr(sectionRect.Right-sectionRect.Left), uintptr(sectionRect.Bottom-sectionRect.Top),
			0, 0, uintptr(crookedState.imageWidth), uintptr(crookedState.imageHeight), uintptr(unsafe.Pointer(&crookedState.bgra[0])), uintptr(unsafe.Pointer(&bitmap)), DIB_RGB_COLORS, SRCCOPY)
	}
	drawCrookedAxes(hdc, sectionRect)
}

func paintCrookedTransientOverlays(hdc uintptr) {
	bounds := crookedMapViewBounds()
	if bounds.HasXY {
		transform := makeCrookedMapTransform(crookedMapRect(), bounds)
		// The AOI belongs to pseudo-3-D and remains visible while the independent
		// Geometry viewport is being changed. Clean style hides persistent guides.
		if selected := crookedState.pseudoRange.Normalized(); selected.Valid && crookedState.styleMode != 3 {
			x0, y0 := transform.toPixel(selected.XMin, selected.YMin)
			x1, y1 := transform.toPixel(selected.XMax, selected.YMax)
			drawCrookedRangeRectangle(hdc, x0, y0, x1, y1)
		}
		if crookedState.rangeDragging {
			drawCrookedRangeRectangle(hdc, crookedState.rangeStartX, crookedState.rangeStartY, crookedState.rangeCurrentX, crookedState.rangeCurrentY)
		}
		if crookedState.mapZoomDragging {
			drawCrookedZoomRectangle(hdc, crookedState.mapZoomStartX, crookedState.mapZoomStartY, crookedState.mapZoomCurrentX, crookedState.mapZoomCurrentY)
		}
		if (crookedState.styleMode == 1 || crookedState.styleMode == 2) && crookedState.hoverLine >= 0 && crookedState.hoverLine != crookedState.activeLine {
			drawCrookedProjectLine(hdc, transform, crookedState.hoverLine, rgbRef(245, 158, 11), 2)
		}
		geometry := crookedState.geometry
		if crookedState.styleMode != 3 && geometry != nil && len(geometry.X) > 0 {
			position := clampInt(crookedState.currentPosition, 0, len(geometry.X)-1)
			x, y := transform.toPixel(geometry.X[position], geometry.Y[position])
			brush, _, _ := pCreateSolidBrush.Call(rgbRef(220, 38, 38))
			old, _, _ := pSelectObject.Call(hdc, brush)
			pEllipse.Call(hdc, uintptr(x-5), uintptr(y-5), uintptr(x+6), uintptr(y+6))
			pSelectObject.Call(hdc, old)
			pDeleteObject.Call(brush)
		}
	}
	geometry := crookedState.geometry
	if geometry == nil {
		return
	}
	section := crookedSectionRect()
	drawCrookedTimeRangeOverlay(hdc, section)
	position := clampInt(crookedState.currentPosition, 0, len(geometry.TraceIndices)-1)
	positions, _ := crookedAxisPositions(geometry, crookedState.axisMode)
	axisStart, axisEnd := crookedVisibleAxisRange(positions)
	if position < len(positions) && axisEnd > axisStart && positions[position] >= axisStart && positions[position] <= axisEnd {
		x := int(section.Left) + int(math.Round((positions[position]-axisStart)/(axisEnd-axisStart)*float64(section.Right-section.Left)))
		drawHomeLine(hdc, x, int(section.Top), x, int(section.Bottom), rgbRef(220, 38, 38), 1)
	}
	if crookedState.sampleEnd > crookedState.sampleStart {
		y := int(section.Top) + int(math.Round(float64(crookedState.currentSample-crookedState.sampleStart)/float64(crookedState.sampleEnd-crookedState.sampleStart)*float64(section.Bottom-section.Top)))
		drawHomeLine(hdc, int(section.Left), y, int(section.Right), y, rgbRef(220, 38, 38), 1)
	}
}

func drawCrookedTimeRangeOverlay(hdc uintptr, section RECT) {
	drawBand := func(y0, y1 int, preview bool) {
		y0, y1 = clampInt(minInt(y0, y1), int(section.Top), int(section.Bottom)), clampInt(maxInt(y0, y1), int(section.Top), int(section.Bottom))
		if y1-y0 < 1 {
			return
		}
		saved, _, _ := pSaveDC.Call(hdc)
		pIntersectClipRect.Call(hdc, uintptr(section.Left), uintptr(section.Top), uintptr(section.Right+1), uintptr(section.Bottom+1))
		// A sparse pale hatch preserves the seismic pixels below it while giving
		// the selected interval the appearance of a translucent blue band.
		fillColor := rgbRef(191, 219, 254)
		if preview {
			fillColor = rgbRef(219, 234, 254)
		}
		for y := y0 + 2; y < y1; y += 5 {
			drawHomeLine(hdc, int(section.Left)+1, y, int(section.Right)-1, y, fillColor, 1)
		}
		drawCrookedDashedRectangle(hdc, int(section.Left), y0, int(section.Right), y1, rgbRef(37, 99, 235), 1, 7, 4)
		if saved != 0 {
			pRestoreDC.Call(hdc, saved)
		}
	}
	if selected := crookedState.pseudoTimeRange.Normalized(); selected.Valid {
		visible := crookedActiveVisibleTimeBounds().Normalized()
		intersection := selected.Clamped(visible.StartMS, visible.EndMS)
		if intersection.Valid {
			y0, ok0 := crookedSectionYForTime(intersection.StartMS)
			y1, ok1 := crookedSectionYForTime(intersection.EndMS)
			if ok0 && ok1 {
				drawBand(y0, y1, false)
			}
		}
	}
	if crookedState.timeRangeDragging {
		drawBand(crookedState.timeRangeStartY, crookedState.timeRangeCurrentY, true)
	}
}

func drawCrookedRangeRectangle(hdc uintptr, x0, y0, x1, y1 int) {
	drawCrookedDashedRectangle(hdc, x0, y0, x1, y1, rgbRef(37, 99, 235), 2, 10, 6)
}

func drawCrookedZoomRectangle(hdc uintptr, x0, y0, x1, y1 int) {
	// Geometry zoom is intentionally lighter and finer than the persistent AOI.
	drawCrookedDashedRectangle(hdc, x0, y0, x1, y1, rgbRef(148, 163, 184), 1, 7, 4)
}

func drawCrookedDashedRectangle(hdc uintptr, x0, y0, x1, y1 int, color uintptr, width int, dashStep, dashLength float64) {
	left, right := minInt(x0, x1), maxInt(x0, x1)
	top, bottom := minInt(y0, y1), maxInt(y0, y1)
	drawDashed := func(ax, ay, bx, by int) {
		length := math.Hypot(float64(bx-ax), float64(by-ay))
		if length <= 0 {
			return
		}
		for start := 0.0; start < length; start += dashStep {
			end := math.Min(length, start+dashLength)
			t0, t1 := start/length, end/length
			drawHomeLine(hdc, ax+int(math.Round(float64(bx-ax)*t0)), ay+int(math.Round(float64(by-ay)*t0)),
				ax+int(math.Round(float64(bx-ax)*t1)), ay+int(math.Round(float64(by-ay)*t1)), color, width)
		}
	}
	drawDashed(left, top, right, top)
	drawDashed(right, top, right, bottom)
	drawDashed(right, bottom, left, bottom)
	drawDashed(left, bottom, left, top)
}

func crookedPseudoLinePoints(index int) []pseudo3dcore.Point {
	if index < 0 || index >= len(crookedState.projectLines) {
		return nil
	}
	state := &crookedState.projectLines[index]
	if len(state.mapPath.Points) >= 2 {
		points := make([]pseudo3dcore.Point, len(state.mapPath.Points))
		for i, point := range state.mapPath.Points {
			points[i] = pseudo3dcore.Point{X: point.X, Y: point.Y}
		}
		return points
	}
	return nil
}

func crookedPseudoRangeCandidateCount() int {
	if crookedState.project != nil && !crookedState.projectGeometryReady {
		return 0
	}
	selected := crookedState.pseudoRange.Normalized()
	count := 0
	for i, state := range crookedState.projectLines {
		if state.line == nil || !state.line.Valid() || !state.ready || state.geometry == nil {
			continue
		}
		points := crookedPseudoLinePoints(i)
		if len(points) >= 2 && (!selected.Valid || pseudo3dcore.PolylineIntersectsRange(points, selected)) {
			count++
		}
	}
	return count
}

func commitCrookedPseudoRange(selected pseudo3dcore.XYRange, source string) bool {
	projectBounds := crookedProjectBounds()
	selected = selected.Normalized()
	if !selected.Valid || !projectBounds.HasXY {
		return false
	}
	selected.XMin, selected.XMax = math.Max(selected.XMin, projectBounds.XMin), math.Min(selected.XMax, projectBounds.XMax)
	selected.YMin, selected.YMax = math.Max(selected.YMin, projectBounds.YMin), math.Min(selected.YMax, projectBounds.YMax)
	selected = selected.Normalized()
	if !selected.Valid {
		return false
	}
	crookedState.pseudoRange = selected
	crookedState.rangeGeneration++
	crookedState.rangeMode, crookedState.rangeDragging = false, false
	setText(crookedControlsUI.spatialRange, "范围")
	pEnableWindow.Call(crookedControlsUI.rangeClear, 1)
	candidates := crookedPseudoRangeCandidateCount()
	setText(crookedControlsUI.status, fmt.Sprintf("伪三维范围已设定：候选测线 %d/%d。", candidates, len(crookedState.projectLines)))
	action := "pseudo3d_range_set"
	if source != "" {
		action += "_" + source
	}
	writeWorkspaceTrace(workspacecore.TraceEvent{Action: action, Workspace: workspacecore.KindCrooked, Dataset: filepath.Base(crookedState.project.Root), Geometry: geometrycore.KindCrookedLine, Generation: crookedState.workspaceGeneration, ProjectLines: candidates})
	reloadPseudoForCrookedRange()
	invalidateCrookedBase()
	return true
}

func beginCrookedPseudoRangeMode() {
	if crookedState.project == nil {
		return
	}
	crookedState.rangeMode = !crookedState.rangeMode
	crookedState.rangeDragging = false
	if crookedState.rangeMode {
		crookedState.timeRangeMode, crookedState.timeRangeDragging = false, false
		setText(crookedControlsUI.timeRange, "时间范围")
		setText(crookedControlsUI.spatialRange, "取消范围")
		setText(crookedControlsUI.status, "请在上方 XY 总图中按住左键拖动，划定伪三维显示范围；Esc 取消。")
	} else {
		setText(crookedControlsUI.spatialRange, "范围")
	}
	pInvalidateRect.Call(crookedHwnd, 0, 0)
}

func finishCrookedPseudoRange(x, y int) {
	if !crookedState.rangeDragging {
		return
	}
	crookedState.rangeDragging = false
	pReleaseCapture.Call()
	mapRect := crookedMapRect()
	x = clampInt(x, int(mapRect.Left), int(mapRect.Right))
	y = clampInt(y, int(mapRect.Top), int(mapRect.Bottom))
	if absInt(x-crookedState.rangeStartX) < 5 || absInt(y-crookedState.rangeStartY) < 5 {
		setText(crookedControlsUI.status, "范围过小，请重新拖动至少 5×5 像素的矩形。")
		pInvalidateRect.Call(crookedHwnd, 0, 0)
		return
	}
	viewBounds, projectBounds := crookedMapViewBounds(), crookedProjectBounds()
	if !viewBounds.HasXY || !projectBounds.HasXY {
		return
	}
	transform := makeCrookedMapTransform(mapRect, viewBounds)
	x0, y0 := transform.toWorld(crookedState.rangeStartX, crookedState.rangeStartY)
	x1, y1 := transform.toWorld(x, y)
	selected := (pseudo3dcore.XYRange{XMin: x0, XMax: x1, YMin: y0, YMax: y1, Valid: true}).Normalized()
	selected.XMin, selected.XMax = math.Max(selected.XMin, projectBounds.XMin), math.Min(selected.XMax, projectBounds.XMax)
	selected.YMin, selected.YMax = math.Max(selected.YMin, projectBounds.YMin), math.Min(selected.YMax, projectBounds.YMax)
	selected = selected.Normalized()
	if !selected.Valid {
		setText(crookedControlsUI.status, "所选范围未覆盖项目坐标，请重新划定。")
		return
	}
	commitCrookedPseudoRange(selected, "map")
}

func clearCrookedPseudoRange() {
	if !crookedState.pseudoRange.Valid && !crookedState.rangeMode {
		return
	}
	crookedState.pseudoRange = pseudo3dcore.XYRange{}
	crookedState.rangeGeneration++
	crookedState.rangeMode, crookedState.rangeDragging = false, false
	setText(crookedControlsUI.spatialRange, "范围")
	pEnableWindow.Call(crookedControlsUI.rangeClear, 0)
	setText(crookedControlsUI.status, fmt.Sprintf("伪三维范围已清除：恢复全部 %d 条有效测线。", crookedState.project.ValidLineCount()))
	writeWorkspaceTrace(workspacecore.TraceEvent{Action: "pseudo3d_range_clear", Workspace: workspacecore.KindCrooked, Dataset: filepath.Base(crookedState.project.Root), Geometry: geometrycore.KindCrookedLine, Generation: crookedState.workspaceGeneration, ProjectLines: crookedState.project.ValidLineCount()})
	reloadPseudoForCrookedRange()
	pInvalidateRect.Call(crookedHwnd, 0, 0)
}

func crookedProjectTimeBounds() pseudo3dcore.TimeRange {
	maximum := 0.0
	for _, state := range crookedState.projectLines {
		if state.line == nil || state.line.Dataset == nil {
			continue
		}
		metadata := state.line.Dataset.Metadata
		if metadata.SamplesPerTrace < 2 || metadata.SampleIntervalUS <= 0 {
			continue
		}
		maximum = math.Max(maximum, float64(metadata.SamplesPerTrace-1)*float64(metadata.SampleIntervalUS)/1000)
	}
	if maximum <= 0 {
		return pseudo3dcore.TimeRange{}
	}
	return pseudo3dcore.TimeRange{StartMS: 0, EndMS: maximum, Valid: true}
}

func crookedActiveVisibleTimeBounds() pseudo3dcore.TimeRange {
	if crookedState.dataset == nil || crookedState.dataset.Metadata.SampleIntervalUS <= 0 || crookedState.sampleEnd <= crookedState.sampleStart {
		return pseudo3dcore.TimeRange{}
	}
	dt := float64(crookedState.dataset.Metadata.SampleIntervalUS) / 1000
	return pseudo3dcore.TimeRange{StartMS: float64(crookedState.sampleStart) * dt, EndMS: float64(crookedState.sampleEnd) * dt, Valid: true}
}

func crookedSectionYForTime(timeMS float64) (int, bool) {
	visible := crookedActiveVisibleTimeBounds().Normalized()
	if !visible.Valid || timeMS < visible.StartMS || timeMS > visible.EndMS {
		return 0, false
	}
	section := crookedSectionRect()
	fraction := (timeMS - visible.StartMS) / (visible.EndMS - visible.StartMS)
	return int(section.Top) + int(math.Round(fraction*float64(section.Bottom-section.Top))), true
}

func crookedTimeAtSectionY(y int) (float64, bool) {
	visible := crookedActiveVisibleTimeBounds().Normalized()
	if !visible.Valid {
		return 0, false
	}
	section := crookedSectionRect()
	fraction := float64(clampInt(y-int(section.Top), 0, int(section.Bottom-section.Top))) / float64(maxInt(1, int(section.Bottom-section.Top)))
	return visible.StartMS + fraction*(visible.EndMS-visible.StartMS), true
}

func crookedTimeRangeCandidateCount(selected pseudo3dcore.TimeRange) int {
	selected = selected.Normalized()
	count := 0
	for _, state := range crookedState.projectLines {
		if state.line == nil || !state.line.Valid() || state.line.Dataset == nil {
			continue
		}
		metadata := state.line.Dataset.Metadata
		_, _, _, ok := selected.SampleWindow(metadata.SampleIntervalUS, metadata.SamplesPerTrace)
		if ok {
			count++
		}
	}
	return count
}

func commitCrookedPseudoTimeRange(selected pseudo3dcore.TimeRange, source string) bool {
	project := crookedProjectTimeBounds().Normalized()
	selected = selected.Normalized()
	if !project.Valid || !selected.Valid {
		return false
	}
	selected = selected.Clamped(project.StartMS, project.EndMS)
	if !selected.Valid {
		return false
	}
	crookedState.pseudoTimeRange = selected
	crookedState.timeRangeGeneration++
	crookedState.timeRangeMode, crookedState.timeRangeDragging = false, false
	setText(crookedControlsUI.timeRange, "时间范围")
	pEnableWindow.Call(crookedControlsUI.timeClear, 1)
	candidates := crookedTimeRangeCandidateCount(selected)
	setText(crookedControlsUI.status, fmt.Sprintf("伪三维时间范围 %.3g–%.3g ms：候选测线 %d/%d。", selected.StartMS, selected.EndMS, candidates, len(crookedState.projectLines)))
	action := "pseudo3d_time_range_set"
	if source != "" {
		action += "_" + source
	}
	writeWorkspaceTrace(workspacecore.TraceEvent{Action: action, Workspace: workspacecore.KindCrooked, Dataset: filepath.Base(crookedState.project.Root), Geometry: geometrycore.KindCrookedLine, Generation: crookedState.workspaceGeneration, ProjectLines: candidates})
	reloadPseudoForCrookedTimeRange()
	section := crookedSectionRect()
	pInvalidateRect.Call(crookedHwnd, uintptr(unsafe.Pointer(&section)), 0)
	return true
}

func beginCrookedPseudoTimeRangeMode() {
	if crookedState.project == nil || crookedState.dataset == nil {
		return
	}
	crookedState.timeRangeMode = !crookedState.timeRangeMode
	crookedState.timeRangeDragging = false
	if crookedState.timeRangeMode {
		crookedState.rangeMode, crookedState.rangeDragging = false, false
		setText(crookedControlsUI.spatialRange, "范围")
		setText(crookedControlsUI.timeRange, "取消时间")
		setText(crookedControlsUI.status, "请在下方完整剖面中按住左键垂直拖动，选择伪三维时间范围；Esc 取消。")
	} else {
		setText(crookedControlsUI.timeRange, "时间范围")
	}
	section := crookedSectionRect()
	pInvalidateRect.Call(crookedHwnd, uintptr(unsafe.Pointer(&section)), 0)
}

func finishCrookedPseudoTimeRange(y int) {
	if !crookedState.timeRangeDragging {
		return
	}
	crookedState.timeRangeDragging = false
	pReleaseCapture.Call()
	section := crookedSectionRect()
	y = clampInt(y, int(section.Top), int(section.Bottom))
	if absInt(y-crookedState.timeRangeStartY) < 5 {
		setText(crookedControlsUI.status, "时间范围过小，请至少垂直拖动 5 像素。")
		pInvalidateRect.Call(crookedHwnd, uintptr(unsafe.Pointer(&section)), 0)
		return
	}
	start, okStart := crookedTimeAtSectionY(crookedState.timeRangeStartY)
	end, okEnd := crookedTimeAtSectionY(y)
	if !okStart || !okEnd || !commitCrookedPseudoTimeRange(pseudo3dcore.TimeRange{StartMS: start, EndMS: end, Valid: true}, "section") {
		setText(crookedControlsUI.status, "无法从当前剖面确定有效时间范围。")
	}
}

func clearCrookedPseudoTimeRange() {
	if !crookedState.pseudoTimeRange.Valid && !crookedState.timeRangeMode {
		return
	}
	crookedState.pseudoTimeRange = pseudo3dcore.TimeRange{}
	crookedState.timeRangeGeneration++
	crookedState.timeRangeMode, crookedState.timeRangeDragging = false, false
	setText(crookedControlsUI.timeRange, "时间范围")
	pEnableWindow.Call(crookedControlsUI.timeClear, 0)
	setText(crookedControlsUI.status, "伪三维时间范围已清除：恢复完整采样时间。")
	writeWorkspaceTrace(workspacecore.TraceEvent{Action: "pseudo3d_time_range_clear", Workspace: workspacecore.KindCrooked, Dataset: filepath.Base(crookedState.project.Root), Geometry: geometrycore.KindCrookedLine, Generation: crookedState.workspaceGeneration, ProjectLines: crookedState.project.ValidLineCount()})
	reloadPseudoForCrookedTimeRange()
	section := crookedSectionRect()
	pInvalidateRect.Call(crookedHwnd, uintptr(unsafe.Pointer(&section)), 0)
}

func destroyCrookedBaseCache() {
	if crookedBaseDC != 0 && crookedBaseOldBmp != 0 {
		pSelectObject.Call(crookedBaseDC, crookedBaseOldBmp)
	}
	if crookedBaseBmp != 0 {
		pDeleteObject.Call(crookedBaseBmp)
	}
	if crookedBaseDC != 0 {
		pDeleteDC.Call(crookedBaseDC)
	}
	crookedBaseDC, crookedBaseBmp, crookedBaseOldBmp = 0, 0, 0
	crookedBaseW, crookedBaseH, crookedBaseDirty = 0, 0, true
}

func ensureCrookedBaseCache(hdc uintptr) bool {
	r := clientRect(crookedHwnd)
	w, h := int(r.Right-r.Left), int(r.Bottom-r.Top)
	if w <= 0 || h <= 0 {
		return false
	}
	if crookedBaseDC == 0 || crookedBaseW != w || crookedBaseH != h {
		destroyCrookedBaseCache()
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
		crookedBaseDC, crookedBaseBmp, crookedBaseOldBmp = mem, bmp, old
		crookedBaseW, crookedBaseH, crookedBaseDirty = w, h, true
	}
	if crookedBaseDirty {
		drawCrookedStaticScene(crookedBaseDC)
		crookedBaseDirty = false
	}
	return true
}

func invalidateCrookedBase() {
	crookedBaseDirty = true
	if crookedHwnd != 0 {
		pInvalidateRect.Call(crookedHwnd, 0, 0)
	}
}

func paintCrooked() {
	var paint PAINTSTRUCT
	hdc, _, _ := pBeginPaint.Call(crookedHwnd, uintptr(unsafe.Pointer(&paint)))
	if hdc != 0 {
		if ensureCrookedBaseCache(hdc) {
			r := paint.RcPaint
			w, h := int(r.Right-r.Left), int(r.Bottom-r.Top)
			if w > 0 && h > 0 {
				pBitBlt.Call(hdc, uintptr(r.Left), uintptr(r.Top), uintptr(w), uintptr(h), crookedBaseDC, uintptr(r.Left), uintptr(r.Top), SRCCOPY)
				paintCrookedTransientOverlays(hdc)
			}
		} else {
			drawCrookedStaticScene(hdc)
			paintCrookedTransientOverlays(hdc)
		}
	}
	pEndPaint.Call(crookedHwnd, uintptr(unsafe.Pointer(&paint)))
}

func drawCrookedAxes(hdc uintptr, rect RECT) {
	geometry := crookedState.geometry
	if geometry == nil {
		return
	}
	positions, cdpUsable := crookedAxisPositions(geometry, crookedState.axisMode)
	axisStart, axisEnd := crookedVisibleAxisRange(positions)
	for i := 0; i <= 5; i++ {
		x := int(rect.Left) + i*int(rect.Right-rect.Left)/5
		target := axisStart + float64(i)*(axisEnd-axisStart)/5
		index := sort.Search(len(positions), func(j int) bool { return positions[j] >= target })
		if index >= len(positions) {
			index = len(positions) - 1
		}
		label := ""
		switch crookedState.axisMode {
		case crookedAxisDistance:
			label = fmt.Sprintf("%.3g", geometry.Distance[index])
		case crookedAxisCDP:
			if index < len(geometry.HasCDP) && geometry.HasCDP[index] {
				label = strconv.FormatInt(int64(geometry.CDP[index]), 10)
			} else {
				label = "-"
			}
		default:
			label = strconv.FormatInt(geometry.TraceIndices[index]+1, 10)
		}
		drawHomeLine(hdc, x, int(rect.Bottom), x, int(rect.Bottom)+4, rgbRef(71, 85, 105), 1)
		drawHomeText(hdc, hFont, rgbRef(51, 65, 85), label, RECT{Left: int32(x - 45), Top: rect.Bottom + 5, Right: int32(x + 45), Bottom: rect.Bottom + 24}, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
	}
	axisTitle := []string{"Trace", "CDP", "Distance（坐标单位）"}[crookedState.axisMode]
	if crookedState.axisMode == crookedAxisCDP && !cdpUsable {
		axisTitle = "CDP（非单调，按道序显示）"
	}
	drawHomeText(hdc, hFont, rgbRef(15, 23, 42), axisTitle, RECT{Left: rect.Left, Top: rect.Bottom + 23, Right: rect.Right, Bottom: rect.Bottom + 40}, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
	if crookedState.reader == nil {
		return
	}
	for i := 0; i <= 5; i++ {
		y := int(rect.Top) + i*int(rect.Bottom-rect.Top)/5
		sample := crookedState.sampleStart + i*(crookedState.sampleEnd-crookedState.sampleStart)/5
		timeMS := float64(sample) * float64(crookedState.reader.Info.SampleIntervalUS) / 1000
		drawHomeLine(hdc, int(rect.Left)-4, y, int(rect.Left), y, rgbRef(71, 85, 105), 1)
		drawHomeText(hdc, hFont, rgbRef(51, 65, 85), formatAdaptiveTimeMS(timeMS), RECT{Left: 2, Top: int32(y - 9), Right: rect.Left - 7, Bottom: int32(y + 9)}, DT_RIGHT|DT_VCENTER|DT_SINGLELINE)
	}
}

func crookedPositionAtSectionX(x int) int {
	geometry := crookedState.geometry
	if geometry == nil {
		return 0
	}
	rect := crookedSectionRect()
	positions, _ := crookedAxisPositions(geometry, crookedState.axisMode)
	start, end := crookedVisibleAxisRange(positions)
	fraction := float64(clampInt(x-int(rect.Left), 0, int(rect.Right-rect.Left))) / float64(maxInt(1, int(rect.Right-rect.Left)))
	target := start + fraction*(end-start)
	index := sort.Search(len(positions), func(i int) bool { return positions[i] >= target })
	if index >= len(positions) {
		index = len(positions) - 1
	}
	if index > 0 && target-positions[index-1] <= positions[index]-target {
		index--
	}
	return index
}

func updateCrookedCursorFromSection(x, y int) {
	if crookedState.geometry == nil {
		return
	}
	oldPosition, oldSample := crookedState.currentPosition, crookedState.currentSample
	rect := crookedSectionRect()
	crookedState.currentPosition = crookedPositionAtSectionX(x)
	fractionY := float64(clampInt(y-int(rect.Top), 0, int(rect.Bottom-rect.Top))) / float64(maxInt(1, int(rect.Bottom-rect.Top)))
	crookedState.currentSample = crookedState.sampleStart + int(math.Round(fractionY*float64(crookedState.sampleEnd-crookedState.sampleStart)))
	if oldPosition == crookedState.currentPosition && oldSample == crookedState.currentSample {
		return
	}
	invalidateCrookedCursorState(oldPosition, oldSample)
	invalidateCrookedCursorState(crookedState.currentPosition, crookedState.currentSample)
	if crookedState.lastStatusUpdate.IsZero() || time.Since(crookedState.lastStatusUpdate) >= 33*time.Millisecond {
		crookedState.lastStatusUpdate = time.Now()
		updateCrookedStatus()
	}
	publishCrookedCursorToPseudo(false)
}

func invalidateCrookedCursorState(position, sample int) {
	geometry := crookedState.geometry
	if crookedHwnd == 0 || geometry == nil || len(geometry.TraceIndices) == 0 {
		return
	}
	position = clampInt(position, 0, len(geometry.TraceIndices)-1)
	section := crookedSectionRect()
	positions, _ := crookedAxisPositions(geometry, crookedState.axisMode)
	start, end := crookedVisibleAxisRange(positions)
	if end > start && positions[position] >= start && positions[position] <= end {
		x := int(section.Left) + int(math.Round((positions[position]-start)/(end-start)*float64(section.Right-section.Left)))
		r := RECT{Left: int32(x - 2), Top: section.Top, Right: int32(x + 3), Bottom: section.Bottom}
		pInvalidateRect.Call(crookedHwnd, uintptr(unsafe.Pointer(&r)), 0)
	}
	if crookedState.sampleEnd > crookedState.sampleStart {
		y := int(section.Top) + int(math.Round(float64(sample-crookedState.sampleStart)/float64(crookedState.sampleEnd-crookedState.sampleStart)*float64(section.Bottom-section.Top)))
		r := RECT{Left: section.Left, Top: int32(y - 2), Right: section.Right, Bottom: int32(y + 3)}
		pInvalidateRect.Call(crookedHwnd, uintptr(unsafe.Pointer(&r)), 0)
	}
	bounds := crookedMapViewBounds()
	if bounds.HasXY {
		transform := makeCrookedMapTransform(crookedMapRect(), bounds)
		mx, my := transform.toPixel(geometry.X[position], geometry.Y[position])
		r := RECT{Left: int32(mx - 8), Top: int32(my - 8), Right: int32(mx + 9), Bottom: int32(my + 9)}
		pInvalidateRect.Call(crookedHwnd, uintptr(unsafe.Pointer(&r)), 0)
	}
}

func selectCrookedActiveMapPoint(x, y int) {
	geometry := crookedState.geometry
	bounds := crookedMapViewBounds()
	if geometry == nil || !bounds.HasXY {
		return
	}
	oldPosition, oldSample := crookedState.currentPosition, crookedState.currentSample
	transform := makeCrookedMapTransform(crookedMapRect(), bounds)
	worldX, worldY := transform.toWorld(x, y)
	_, position, ok := geometry.NearestTraceXY(worldX, worldY)
	if ok {
		crookedState.currentPosition = position
		updateCrookedStatus()
		publishCrookedCursorToPseudo(true)
		invalidateCrookedCursorState(oldPosition, oldSample)
		invalidateCrookedCursorState(crookedState.currentPosition, crookedState.currentSample)
	}
}

func distancePointToSegment(px, py, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	if dx == 0 && dy == 0 {
		return math.Hypot(px-ax, py-ay)
	}
	t := ((px-ax)*dx + (py-ay)*dy) / (dx*dx + dy*dy)
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(px-(ax+t*dx), py-(ay+t*dy))
}

func nearestCrookedMapPathSegment(path crookedMapPath, x, y int, transform crookedMapTransform) (int, int64, float64, bool) {
	if len(path.Points) < 2 {
		return 0, 0, math.Inf(1), false
	}
	bestPosition, bestTrace, bestDistance := 0, int64(0), math.Inf(1)
	for i := 1; i < len(path.Points); i++ {
		a, b := path.Points[i-1], path.Points[i]
		ax, ay := transform.toPixel(a.X, a.Y)
		bx, by := transform.toPixel(b.X, b.Y)
		dx, dy := float64(bx-ax), float64(by-ay)
		projection := 0.0
		if dx != 0 || dy != 0 {
			projection = ((float64(x-ax) * dx) + (float64(y-ay) * dy)) / (dx*dx + dy*dy)
			projection = math.Max(0, math.Min(1, projection))
		}
		distance := distancePointToSegment(float64(x), float64(y), float64(ax), float64(ay), float64(bx), float64(by))
		if distance >= bestDistance {
			continue
		}
		position := int(math.Round(float64(a.Position) + projection*float64(b.Position-a.Position)))
		trace := a.TraceIndex
		if b.Position != a.Position {
			trace = int64(math.Round(float64(a.TraceIndex) + float64(position-a.Position)*float64(b.TraceIndex-a.TraceIndex)/float64(b.Position-a.Position)))
		}
		bestPosition, bestTrace, bestDistance = position, trace, distance
	}
	return bestPosition, bestTrace, bestDistance, !math.IsInf(bestDistance, 1)
}

func crookedMapHitAt(x, y int) crookedMapHit {
	if crookedState.project != nil && !crookedState.projectGeometryReady {
		return crookedMapHit{}
	}
	bounds := crookedMapViewBounds()
	if !bounds.HasXY || !rectContains(crookedMapRect(), x, y) {
		return crookedMapHit{}
	}
	transform := makeCrookedMapTransform(crookedMapRect(), bounds)
	best := crookedMapHit{LineIndex: -1, DistancePX: math.Inf(1)}
	for index, state := range crookedState.projectLines {
		position, trace, distance, ok := nearestCrookedMapPathSegment(state.mapPath, x, y, transform)
		if ok && state.geometry != nil && len(state.geometry.TraceIndices) > 0 {
			position = clampInt(position, 0, len(state.geometry.TraceIndices)-1)
			trace = state.geometry.TraceIndices[position]
		}
		if ok && distance < best.DistancePX {
			best = crookedMapHit{LineID: state.mapPath.LineID, LineIndex: index, TraceIndex: trace, Position: position,
				Fingerprint: state.mapPath.Fingerprint, DistancePX: distance, Valid: true}
		}
	}
	if !best.Valid || best.DistancePX > 8 {
		return crookedMapHit{}
	}
	return best
}

func crookedLineHitAt(x, y int) int {
	hit := crookedMapHitAt(x, y)
	if !hit.Valid {
		return -1
	}
	return hit.LineIndex
}

func hoverCrookedMapLine(x, y int) {
	next := crookedLineHitAt(x, y)
	if next == crookedState.hoverLine {
		return
	}
	crookedState.hoverLine = next
	r := crookedMapRect()
	pInvalidateRect.Call(crookedHwnd, uintptr(unsafe.Pointer(&r)), 0)
}

func crookedMapTargetFromHit(hit crookedMapHit, timeMS float64) *crookedNavigationTarget {
	if !hit.Valid || hit.LineIndex < 0 || hit.LineIndex >= len(crookedState.projectLines) {
		return nil
	}
	state := &crookedState.projectLines[hit.LineIndex]
	if state.line == nil || state.line.ID != hit.LineID || state.fingerprint == "" || state.fingerprint != hit.Fingerprint {
		return nil
	}
	return &crookedNavigationTarget{
		projectGeneration:   atomic.LoadInt64(&crookedProjectGen),
		specGeneration:      atomic.LoadInt64(&crookedSpecGen),
		lineIndex:           hit.LineIndex,
		lineID:              hit.LineID,
		traceIndex:          hit.TraceIndex,
		geometryFingerprint: hit.Fingerprint,
		timeMS:              timeMS,
	}
}

func crookedMapTargetCurrent(target *crookedNavigationTarget) bool {
	if target == nil || target.projectGeneration != atomic.LoadInt64(&crookedProjectGen) || target.specGeneration != atomic.LoadInt64(&crookedSpecGen) || target.lineIndex < 0 || target.lineIndex >= len(crookedState.projectLines) {
		return false
	}
	state := &crookedState.projectLines[target.lineIndex]
	return state.line != nil && state.line.ID == target.lineID && state.fingerprint != "" && state.fingerprint == target.geometryFingerprint
}

func commitCrookedMapTarget(target *crookedNavigationTarget) {
	if !crookedMapTargetCurrent(target) {
		return
	}
	copyTarget := *target
	crookedState.pendingNavigation = &copyTarget
	if target.lineIndex == crookedState.activeLine && crookedState.geometry != nil {
		applyCrookedPendingNavigation(true)
		return
	}
	if !activateCrookedProjectLine(target.lineIndex) {
		crookedState.pendingNavigation = nil
	}
}

func queueCrookedMapClickAt(x, y int) bool {
	hit := crookedMapHitAt(x, y)
	timeMS := 0.0
	if crookedState.dataset != nil && crookedState.dataset.Metadata.SampleIntervalUS > 0 {
		timeMS = float64(crookedState.currentSample) * float64(crookedState.dataset.Metadata.SampleIntervalUS) / 1000
	}
	target := crookedMapTargetFromHit(hit, timeMS)
	crookedState.pendingMapClick = target
	return target != nil
}

func takeCrookedMapClick(clickGeneration int64) *crookedNavigationTarget {
	if clickGeneration != atomic.LoadInt64(&crookedMapClickGen) {
		return nil
	}
	target := crookedState.pendingMapClick
	crookedState.pendingMapClick = nil
	if !crookedMapTargetCurrent(target) {
		return nil
	}
	return target
}

func selectCrookedMapPoint(x, y int) {
	hit := crookedMapHitAt(x, y)
	if !hit.Valid || hit.LineIndex < 0 || hit.LineIndex >= len(crookedState.projectLines) {
		return
	}
	state := &crookedState.projectLines[hit.LineIndex]
	if state.line == nil || state.line.ID != hit.LineID || state.fingerprint != hit.Fingerprint {
		return
	}
	timeMS := 0.0
	if crookedState.dataset != nil && crookedState.dataset.Metadata.SampleIntervalUS > 0 {
		timeMS = float64(crookedState.currentSample) * float64(crookedState.dataset.Metadata.SampleIntervalUS) / 1000
	}
	crookedState.pendingNavigation = &crookedNavigationTarget{projectGeneration: atomic.LoadInt64(&crookedProjectGen), specGeneration: atomic.LoadInt64(&crookedSpecGen),
		lineIndex: hit.LineIndex, lineID: hit.LineID, traceIndex: hit.TraceIndex, geometryFingerprint: hit.Fingerprint, timeMS: timeMS}
	if hit.LineIndex == crookedState.activeLine && crookedState.geometry != nil {
		applyCrookedPendingNavigation(true)
		return
	}
	if !activateCrookedProjectLine(hit.LineIndex) {
		crookedState.pendingNavigation = nil
	}
}

func zoomCrookedAt(x int, wheelDelta int) {
	if crookedState.geometry == nil || wheelDelta == 0 {
		return
	}
	rect := crookedSectionRect()
	fraction := float64(clampInt(x-int(rect.Left), 0, int(rect.Right-rect.Left))) / float64(maxInt(1, int(rect.Right-rect.Left)))
	span := crookedState.viewEnd - crookedState.viewStart
	factor := 0.8
	if wheelDelta < 0 {
		factor = 1.25
	}
	newSpan := math.Max(0.01, math.Min(1, span*factor))
	anchor := crookedState.viewStart + fraction*span
	start := anchor - fraction*newSpan
	end := start + newSpan
	if start < 0 {
		end -= start
		start = 0
	}
	if end > 1 {
		start -= end - 1
		end = 1
	}
	crookedState.viewStart, crookedState.viewEnd = math.Max(0, start), math.Min(1, end)
	startCrookedRender()
}

func resetCrookedView() {
	crookedState.viewStart, crookedState.viewEnd = 0, 1
	if crookedState.geometry != nil {
		crookedState.currentPosition = len(crookedState.geometry.TraceIndices) / 2
	}
	crookedState.currentSample = crookedState.sampleStart
	startCrookedRender()
	publishCrookedCursorToPseudo(true)
}

func changeCrookedGain(delta float64) {
	value := math.Max(0, math.Min(49, crookedState.gainPercent+delta))
	if value == crookedState.gainPercent {
		return
	}
	crookedState.gainPercent = value
	crookedDefaults.GainPercent = value
	setText(crookedControlsUI.gainValue, fmt.Sprintf("%.0f%%", value))
	saveCrookedDefaults()
	startCrookedRender()
	pseudoReloadSelectedGain(value)
}

func showCrookedHeaderSettings() {
	if crookedHeaderHwnd != 0 {
		pSetForeground.Call(crookedHeaderHwnd)
		return
	}
	h, _, _ := pCreateWindowExW.Call(WS_EX_APPWINDOW, uintptr(unsafe.Pointer(u16("Limage64CrookedHeaders"))), uintptr(unsafe.Pointer(u16("弯线道头设置"))),
		WS_POPUP|WS_CAPTION|WS_SYSMENU, uintptr(CW_USEDEFAULT), uintptr(CW_USEDEFAULT), 390, 315, crookedHwnd, 0, 0, 0)
	if h == 0 {
		return
	}
	crookedHeaderHwnd = h
	createCrookedHeaderUI()
	pShowWindow.Call(h, SW_SHOW)
	pUpdateWindow.Call(h)
	pSetForeground.Call(h)
}

func createCrookedHeaderUI() {
	crookedHeaderUI = crookedHeaderControls{}
	createLabel(crookedHeaderHwnd, "坐标来源", 20, 22, 90, 20)
	crookedHeaderUI.source = createCtrl(crookedHeaderHwnd, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 122, 18, 220, 150, IDCROOKED_HEADER_SOURCE)
	for _, label := range []string{"自动标准字段", "Ensemble X/Y", "Source X/Y", "Group X/Y", "自定义字节"} {
		pSendMessageW.Call(crookedHeaderUI.source, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(label))))
	}
	selection := 0
	switch crookedState.spec.Source {
	case segy.CoordinateEnsemble:
		selection = 1
	case segy.CoordinateSourceXY:
		selection = 2
	case segy.CoordinateGroup:
		selection = 3
	case segy.CoordinateCustom:
		selection = 4
	}
	pSendMessageW.Call(crookedHeaderUI.source, CB_SETCURSEL, uintptr(selection), 0)
	spec := crookedState.spec
	if spec.XByte == 0 {
		spec = segy.DefaultCoordinateSpec()
	}
	fields := []struct {
		label  string
		value  int
		id     int
		target *uintptr
	}{
		{"X 起始字节", spec.XByte, IDCROOKED_HEADER_X, &crookedHeaderUI.x},
		{"Y 起始字节", spec.YByte, IDCROOKED_HEADER_Y, &crookedHeaderUI.y},
		{"CDP 起始字节", spec.CDPByte, IDCROOKED_HEADER_CDP, &crookedHeaderUI.cdp},
		{"坐标比例字节", spec.ScalarByte, IDCROOKED_HEADER_SCALAR, &crookedHeaderUI.scalar},
		{"坐标单位字节", spec.UnitsByte, IDCROOKED_HEADER_UNITS, &crookedHeaderUI.units},
	}
	for i, field := range fields {
		y := 58 + i*34
		createLabel(crookedHeaderHwnd, field.label, 20, y+3, 100, 20)
		*field.target = createCtrl(crookedHeaderHwnd, "EDIT", strconv.Itoa(field.value), WS_CHILD|WS_VISIBLE|WS_BORDER|ES_AUTOHSCROLL, 122, y, 90, 22, field.id)
	}
	createLabel(crookedHeaderHwnd, "字节位置按 SEG-Y 手册从 1 开始。", 222, 62, 145, 100)
	crookedHeaderUI.apply = createCtrl(crookedHeaderHwnd, "BUTTON", "应用并重建", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_DEFPUSHBUTTON, 178, 242, 92, 25, IDCROOKED_HEADER_APPLY)
	crookedHeaderUI.cancel = createCtrl(crookedHeaderHwnd, "BUTTON", "取消", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 278, 242, 64, 25, IDCROOKED_HEADER_CANCEL)
}

func applyCrookedHeaderSettings() {
	readByte := func(handle uintptr) (int, error) {
		value, err := strconv.Atoi(strings.TrimSpace(getText(handle)))
		if err != nil {
			return 0, fmt.Errorf("请输入有效的整数道头字节")
		}
		return value, nil
	}
	x, err := readByte(crookedHeaderUI.x)
	if err != nil {
		message(crookedHeaderHwnd, "弯线道头", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	y, err := readByte(crookedHeaderUI.y)
	if err != nil {
		message(crookedHeaderHwnd, "弯线道头", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	cdp, err := readByte(crookedHeaderUI.cdp)
	if err != nil {
		message(crookedHeaderHwnd, "弯线道头", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	scalar, err := readByte(crookedHeaderUI.scalar)
	if err != nil {
		message(crookedHeaderHwnd, "弯线道头", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	units, err := readByte(crookedHeaderUI.units)
	if err != nil {
		message(crookedHeaderHwnd, "弯线道头", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	selected, _, _ := pSendMessageW.Call(crookedHeaderUI.source, CB_GETCURSEL, 0, 0)
	spec := segy.TraceCoordinateSpec{CDPByte: cdp, ScalarByte: scalar, UnitsByte: units}
	switch int(selected) {
	case 0:
		spec.Source = segy.CoordinateAuto
	case 1:
		spec = segy.DefaultCoordinateSpecs()[0]
	case 2:
		spec = segy.DefaultCoordinateSpecs()[1]
	case 3:
		spec = segy.DefaultCoordinateSpecs()[2]
	default:
		spec.Source, spec.XByte, spec.YByte = segy.CoordinateCustom, x, y
	}
	if spec.Source != segy.CoordinateAuto {
		if _, err := segy.NormalizeCoordinateSpec(spec); err != nil {
			message(crookedHeaderHwnd, "弯线道头", err.Error(), MB_OK|MB_ICONERROR)
			return
		}
	}
	// A pseudo-3D window is keyed by the project pointer as well as its
	// geometry.  Close it before changing the coordinate specification so a
	// same-project rebuild can never reuse curtains made from old header bytes.
	closePseudoWindowForProjectChange()
	crookedState.spec = spec
	crookedDefaults.Spec = spec
	saveCrookedDefaults()
	pDestroyWindow.Call(crookedHeaderHwnd)
	atomic.AddInt64(&crookedSpecGen, 1)
	atomic.AddInt64(&crookedMapClickGen, 1)
	crookedState.pendingMapClick = nil
	invalidateCrookedDeliveries(false)
	if crookedState.project != nil {
		projectGen := atomic.AddInt64(&crookedProjectGen, 1)
		for i := range crookedState.projectLines {
			state := &crookedState.projectLines[i]
			state.rawGeometry, state.geometry, state.mapPath, state.fingerprint = nil, nil, crookedMapPath{}, ""
			state.ready, state.loading, state.errorText = false, false, ""
		}
		crookedState.projectGeometryReady, crookedState.projectGeometrySuspended = false, false
		crookedState.pendingNavigation, crookedState.pendingMapClick = nil, nil
		pEnableWindow.Call(crookedControlsUI.pseudo, 0)
		pEnableWindow.Call(crookedControlsUI.spatialRange, 0)
		pEnableWindow.Call(crookedControlsUI.lines, 0)
		activateCrookedProjectLine(crookedState.activeLine)
		startCrookedProjectBackground(projectGen)
	} else if crookedState.dataset != nil {
		startCrookedPrepare(crookedState.dataset, crookedState.workspaceGeneration, crookedState.sampleStart, crookedState.sampleEnd)
	}
}

func crookedHeaderWndProc(h uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case WM_COMMAND:
		switch int(wParam & 0xffff) {
		case IDCROOKED_HEADER_APPLY:
			applyCrookedHeaderSettings()
			return 0
		case IDCROOKED_HEADER_CANCEL:
			pDestroyWindow.Call(h)
			return 0
		}
	case WM_CLOSE:
		pDestroyWindow.Call(h)
		return 0
	case WM_DESTROY:
		crookedHeaderHwnd = 0
		crookedHeaderUI = crookedHeaderControls{}
		return 0
	}
	result, _, _ := pDefWindowProcW.Call(h, uintptr(msg), wParam, lParam)
	return result
}

func crookedWndProc(h uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case WM_DROPFILES:
		paths := droppedCrookedPaths(wParam)
		if len(paths) == 1 {
			if stat, err := os.Stat(paths[0]); err == nil && stat.IsDir() {
				openCrookedProjectFolder(paths[0])
			} else {
				openCrookedProjectPaths(paths)
			}
		} else if len(paths) > 1 {
			openCrookedProjectPaths(paths)
		}
		return 0
	case WM_COMMAND:
		id := int(wParam & 0xffff)
		notify := int((wParam >> 16) & 0xffff)
		switch id {
		case IDCROOKED_HOME:
			if application != nil {
				_ = application.Workspaces.CloseActive()
			}
		case IDCROOKED_OPEN:
			if paths := openCrookedMultiDialog(crookedHwnd); len(paths) > 0 {
				openCrookedProjectPaths(paths)
			}
		case IDCROOKED_FOLDER:
			if path := browseCrookedFolder(crookedHwnd); path != "" {
				openCrookedProjectFolder(path)
			}
		case IDCROOKED_LINES:
			if notify == LBN_SELCHANGE {
				selection, _, _ := pSendMessageW.Call(crookedControlsUI.lines, LB_GETCURSEL, 0, 0)
				if selection != LB_ERR {
					activateCrookedProjectLine(int(selection))
				}
			}
		case IDCROOKED_AXIS:
			if notify == 1 {
				selection, _, _ := pSendMessageW.Call(crookedControlsUI.axis, CB_GETCURSEL, 0, 0)
				if int(selection) >= crookedAxisTrace && int(selection) <= crookedAxisDistance {
					crookedState.axisMode = int(selection)
					saveCrookedActiveLineView()
					crookedDefaults.AxisMode = crookedState.axisMode
					saveCrookedDefaults()
					startCrookedRender()
				}
			}
		case IDCROOKED_PALETTE:
			if notify == 1 {
				selection, _, _ := pSendMessageW.Call(crookedControlsUI.palette, CB_GETCURSEL, 0, 0)
				if int(selection) >= 0 && int(selection) < len(paletteNames) {
					crookedState.paletteIndex = int(selection)
					crookedDefaults.PaletteIndex = int(selection)
					saveCrookedDefaults()
					if len(crookedState.indices) > 0 {
						crookedState.bgra = crookedPaletteBGRA(crookedState.indices, crookedState.paletteIndex)
						updateCrookedStatus()
						invalidateCrookedBase()
					}
					pseudoApplyPalette(crookedState.paletteIndex)
				}
			}
		case IDCROOKED_GAINMINUS:
			changeCrookedGain(-1)
		case IDCROOKED_GAINPLUS:
			changeCrookedGain(1)
		case IDCROOKED_RESET:
			resetCrookedView()
		case IDCROOKED_HEADERS:
			showCrookedHeaderSettings()
		case IDCROOKED_RETRY:
			if crookedState.project != nil && crookedState.activeLine >= 0 {
				activateCrookedProjectLine(crookedState.activeLine)
			} else if crookedState.dataset != nil {
				startCrookedPrepare(crookedState.dataset, crookedState.workspaceGeneration, crookedState.sampleStart, crookedState.sampleEnd)
			}
		case IDCROOKED_PSEUDO:
			showPseudoWindow()
		case IDCROOKED_RANGE:
			setCrookedTraceInspectMode(false)
			beginCrookedPseudoRangeMode()
		case IDCROOKED_RANGECLEAR:
			clearCrookedPseudoRange()
		case IDCROOKED_TIMERANGE:
			setCrookedTraceInspectMode(false)
			beginCrookedPseudoTimeRangeMode()
		case IDCROOKED_TIMECLEAR:
			clearCrookedPseudoTimeRange()
		case IDCROOKED_STYLE:
			if notify == 1 {
				selection, _, _ := pSendMessageW.Call(crookedControlsUI.style, CB_GETCURSEL, 0, 0)
				applyCrookedSharedStyle(crookedStyleFromCombo(int(selection)))
			}
		case IDCROOKED_TRACE:
			setCrookedTraceInspectMode(isChecked(crookedControlsUI.trace))
		}
		return 0
	case WM_CROOKED_PROGRESS:
		if int64(lParam) == atomic.LoadInt64(&crookedLoadGen) {
			crookedState.progress = int(wParam)
			pSendMessageW.Call(crookedControlsUI.progress, PBM_SETPOS, wParam, 0)
			if int(wParam) < 10 {
				setText(crookedControlsUI.status, "正在识别弯线坐标道头...")
			} else if int(wParam) < 92 {
				setText(crookedControlsUI.status, fmt.Sprintf("正在建立弯线索引... %d%%", int(wParam)))
			} else if int(wParam) < 100 {
				setText(crookedControlsUI.status, "弯线索引已就绪，正在渲染剖面...")
			}
		}
		return 0
	case WM_CROOKED_READY:
		handleCrookedReady()
		return 0
	case WM_CROOKED_RENDER_READY:
		handleCrookedRenderReady()
		return 0
	case WM_CROOKED_PROJECT_READY:
		handleCrookedProjectGeometry()
		return 0
	case WM_CROOKED_MAP_CLICK:
		if int64(lParam) == atomic.LoadInt64(&crookedMapClickGen) {
			if !crookedState.rangeMode && !crookedState.mapZoomPending {
				commitCrookedMapTarget(takeCrookedMapClick(int64(lParam)))
			} else {
				crookedState.pendingMapClick = nil
			}
		}
		return 0
	case WM_LBUTTONDBLCLK:
		x, y := mousePoint(lParam)
		if rectContains(crookedMapRect(), x, y) && !crookedState.rangeMode {
			atomic.AddInt64(&crookedMapClickGen, 1)
			crookedState.pendingMapClick = nil
			crookedState.mapZoomPending, crookedState.mapZoomDragging = false, false
			pReleaseCapture.Call()
			resetCrookedMapView()
			setText(crookedControlsUI.status, "Geometry 总图已恢复完整项目范围。")
			return 0
		}
	case WM_LBUTTONDOWN:
		x, y := mousePoint(lParam)
		if rectContains(crookedMapRect(), x, y) {
			if crookedState.rangeMode {
				crookedState.rangeDragging = true
				crookedState.rangeStartX, crookedState.rangeStartY = x, y
				crookedState.rangeCurrentX, crookedState.rangeCurrentY = x, y
				pSetCapture.Call(crookedHwnd)
				return 0
			}
			crookedState.mapZoomPending, crookedState.mapZoomDragging = true, false
			crookedState.mapZoomStartX, crookedState.mapZoomStartY = x, y
			crookedState.mapZoomCurrentX, crookedState.mapZoomCurrentY = x, y
			pSetCapture.Call(crookedHwnd)
			return 0
		}
		if rectContains(crookedSectionRect(), x, y) {
			if crookedState.traceInspectMode {
				updateCrookedCursorFromSection(x, y)
				if selection, ok := crookedTraceAnalysisSelectionAt(x, y); ok {
					showTraceAnalysisSelection(selection)
				}
				return 0
			}
			if crookedState.timeRangeMode {
				crookedState.timeRangeDragging = true
				crookedState.timeRangeStartY, crookedState.timeRangeCurrentY = y, y
				pSetCapture.Call(crookedHwnd)
				return 0
			}
			updateCrookedCursorFromSection(x, y)
			crookedState.dragging, crookedState.dragMoved = true, false
			crookedState.dragStartX = x
			crookedState.dragViewStart, crookedState.dragViewEnd = crookedState.viewStart, crookedState.viewEnd
			pSetCapture.Call(crookedHwnd)
			return 0
		}
	case WM_MOUSEMOVE:
		x, y := mousePoint(lParam)
		if crookedState.timeRangeDragging {
			section := crookedSectionRect()
			crookedState.timeRangeCurrentY = clampInt(y, int(section.Top), int(section.Bottom))
			pInvalidateRect.Call(crookedHwnd, uintptr(unsafe.Pointer(&section)), 0)
		} else if crookedState.rangeDragging {
			mapRect := crookedMapRect()
			crookedState.rangeCurrentX = clampInt(x, int(mapRect.Left), int(mapRect.Right))
			crookedState.rangeCurrentY = clampInt(y, int(mapRect.Top), int(mapRect.Bottom))
			pInvalidateRect.Call(crookedHwnd, uintptr(unsafe.Pointer(&mapRect)), 0)
		} else if crookedState.mapZoomPending {
			mapRect := crookedMapRect()
			crookedState.mapZoomCurrentX = clampInt(x, int(mapRect.Left), int(mapRect.Right))
			crookedState.mapZoomCurrentY = clampInt(y, int(mapRect.Top), int(mapRect.Bottom))
			if absInt(crookedState.mapZoomCurrentX-crookedState.mapZoomStartX) >= 5 && absInt(crookedState.mapZoomCurrentY-crookedState.mapZoomStartY) >= 5 {
				crookedState.mapZoomDragging = true
			}
			if crookedState.mapZoomDragging {
				pInvalidateRect.Call(crookedHwnd, uintptr(unsafe.Pointer(&mapRect)), 0)
			}
		} else if crookedState.dragging {
			dx := x - crookedState.dragStartX
			if absInt(dx) > 2 {
				crookedState.dragMoved = true
				width := maxInt(1, int(crookedSectionRect().Right-crookedSectionRect().Left))
				span := crookedState.dragViewEnd - crookedState.dragViewStart
				shift := -float64(dx) / float64(width) * span
				start, end := crookedState.dragViewStart+shift, crookedState.dragViewEnd+shift
				if start < 0 {
					end -= start
					start = 0
				}
				if end > 1 {
					start -= end - 1
					end = 1
				}
				crookedState.viewStart, crookedState.viewEnd = math.Max(0, start), math.Min(1, end)
			}
		} else if rectContains(crookedSectionRect(), x, y) && !crookedState.timeRangeMode {
			updateCrookedCursorFromSection(x, y)
		} else if rectContains(crookedMapRect(), x, y) {
			hoverCrookedMapLine(x, y)
		} else if crookedState.hoverLine != -1 {
			hoverCrookedMapLine(-10000, -10000)
		}
		return 0
	case WM_LBUTTONUP:
		if crookedState.timeRangeDragging {
			_, y := mousePoint(lParam)
			finishCrookedPseudoTimeRange(y)
			return 0
		}
		if crookedState.rangeDragging {
			x, y := mousePoint(lParam)
			finishCrookedPseudoRange(x, y)
			return 0
		}
		if crookedState.mapZoomPending {
			x, y := mousePoint(lParam)
			dragged := crookedState.mapZoomDragging
			crookedState.mapZoomPending = false
			pReleaseCapture.Call()
			if dragged {
				crookedState.mapZoomDragging = true
				finishCrookedMapZoom(x, y)
			} else {
				crookedState.mapZoomDragging = false
				gen := atomic.AddInt64(&crookedMapClickGen, 1)
				if queueCrookedMapClickAt(x, y) {
					delay, _, _ := pGetDoubleClickTime.Call()
					if delay == 0 {
						delay = 500
					}
					go func(hwnd uintptr, clickGen int64, milliseconds uintptr) {
						time.Sleep(time.Duration(milliseconds) * time.Millisecond)
						crookedPostWindowMessage(hwnd, WM_CROOKED_MAP_CLICK, 0, uintptr(clickGen))
					}(crookedHwnd, gen, delay)
				}
			}
			crookedState.mapZoomDragging = false
			mapRect := crookedMapRect()
			pInvalidateRect.Call(crookedHwnd, uintptr(unsafe.Pointer(&mapRect)), 0)
			return 0
		}
		if crookedState.dragging {
			crookedState.dragging = false
			pReleaseCapture.Call()
			publishCrookedCursorToPseudo(true)
			if crookedState.dragMoved {
				startCrookedRender()
			}
			return 0
		}
	case WM_MOUSEWHEEL:
		point := POINT{X: int32(int16(uint16(lParam & 0xffff))), Y: int32(int16(uint16((lParam >> 16) & 0xffff)))}
		pScreenToClient.Call(crookedHwnd, uintptr(unsafe.Pointer(&point)))
		if rectContains(crookedSectionRect(), int(point.X), int(point.Y)) {
			zoomCrookedAt(int(point.X), int(int16(uint16((wParam>>16)&0xffff))))
			return 0
		}
	case WM_KEYDOWN:
		if wParam == VK_ESCAPE && crookedState.traceInspectMode {
			setCrookedTraceInspectMode(false)
			setText(crookedControlsUI.status, "已退出单道查看模式。")
			return 0
		}
		if wParam == VK_ESCAPE && (crookedState.rangeMode || crookedState.rangeDragging || crookedState.mapZoomPending || crookedState.timeRangeMode || crookedState.timeRangeDragging) {
			atomic.AddInt64(&crookedMapClickGen, 1)
			crookedState.pendingMapClick = nil
			crookedState.rangeMode, crookedState.rangeDragging = false, false
			crookedState.mapZoomPending, crookedState.mapZoomDragging = false, false
			crookedState.timeRangeMode, crookedState.timeRangeDragging = false, false
			pReleaseCapture.Call()
			setText(crookedControlsUI.spatialRange, "范围")
			setText(crookedControlsUI.timeRange, "时间范围")
			setText(crookedControlsUI.status, "已取消范围选择。")
			pInvalidateRect.Call(crookedHwnd, 0, 0)
			return 0
		}
	case WM_SIZE:
		layoutCrookedControls()
		destroyCrookedBaseCache()
		if crookedState.geometry != nil {
			startCrookedRender()
		}
		invalidateCrookedBase()
		return 0
	case WM_GETMINMAXINFO:
		if lParam != 0 {
			limits := (*MINMAXINFO)(unsafe.Pointer(lParam))
			limits.PtMinTrackSize.X = 900
			limits.PtMinTrackSize.Y = 650
		}
		return 0
	case WM_ERASEBKGND:
		return 1
	case WM_PAINT:
		paintCrooked()
		return 0
	case WM_CLOSE:
		revokeOleSegyDropTarget(h)
		pDestroyWindow.Call(h)
		return 0
	case WM_DESTROY:
		atomic.AddInt64(&crookedMapClickGen, 1)
		crookedState.pendingMapClick = nil
		closePseudoWindowForProjectChange()
		revokeOleSegyDropTarget(h)
		atomic.AddInt64(&crookedLoadGen, 1)
		atomic.AddInt64(&crookedRenderGen, 1)
		atomic.AddInt64(&crookedProjectGen, 1)
		invalidateCrookedDeliveries(true)
		if crookedHeaderHwnd != 0 {
			pDestroyWindow.Call(crookedHeaderHwnd)
		}
		if crookedState.reader != nil {
			_ = crookedState.reader.Close()
		}
		destroyCrookedBaseCache()
		crookedState = crookedSession{}
		crookedHwnd = 0
		crookedControlsUI = crookedControls{}
		if application != nil && !crookedManagerClosing {
			_ = application.Workspaces.NotifyClosed(workspacecore.KindCrooked)
		}
		return 0
	}
	result, _, _ := pDefWindowProcW.Call(h, uintptr(msg), wParam, lParam)
	return result
}

var _ = filepath.Base
