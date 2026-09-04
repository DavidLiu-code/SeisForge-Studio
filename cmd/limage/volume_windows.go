//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

const (
	IDVOL_ILPREV    = 6001
	IDVOL_ILNEXT    = 6002
	IDVOL_IL        = 6003
	IDVOL_XLPREV    = 6004
	IDVOL_XLNEXT    = 6005
	IDVOL_XL        = 6006
	IDVOL_TPREV     = 6007
	IDVOL_TNEXT     = 6008
	IDVOL_TIME      = 6009
	IDVOL_PALETTE   = 6010
	IDVOL_AUTO      = 6011
	IDVOL_RESET     = 6012
	IDVOL_CLOSE     = 6013
	IDVOL_STATUS    = 6014
	IDVOL_VIEW      = 6015
	IDVOL_MIN       = 6016
	IDVOL_SIZE      = 6017
	IDVOL_ASPECT    = 6018
	IDVOL_STYLE     = 6019
	IDVOL_INTERACT  = 6020
	IDVOL_COORDS    = 6021
	IDVOL_AXIS_IL   = 6022
	IDVOL_AXIS_XL   = 6023
	IDVOL_AXIS_Z    = 6024
	IDVOL_FOV       = 6025
	IDVOL_DEFAULT   = 6026
	IDVOL_HELP      = 6027
	IDVOL_COMPARE   = 6028
	IDVOL_MATCHLEFT = 6029
	IDVOL_DIFF      = 6030
	IDVOL_EXPORT    = 6031
	IDVOL_TRACE     = 6032

	WM_VOLUME_READY          = WM_USER + 301
	WM_VOLUME_EXPORT_DONE    = WM_USER + 302
	WM_VOLUME_INDEX_PROGRESS = WM_USER + 304
)

type volumeControls struct {
	ilPrev, ilNext, ilEdit uintptr
	xlPrev, xlNext, xlEdit uintptr
	tPrev, tNext, tEdit    uintptr
	palette, auto          uintptr
	reset, close, status   uintptr
	indexProgress          uintptr
	view, minimal          uintptr
	size, aspect           uintptr
	style, interaction     uintptr
	coords                 uintptr
	axisIL, axisXL, axisZ  uintptr
	fov, saveDefault, help uintptr
	compare, matchLeft     uintptr
	diff, export, trace    uintptr
}

// volumeSceneState stores the data-specific slice state and the independent
// camera/geometry transform for one side of the 3-D compare workspace.
// Shared presentation choices (palette/style/coordinate visibility/gain) remain
// global so A/B can be compared under the same rendering convention.
type volumeSceneState struct {
	valid                                     bool
	path                                      string
	f                                         *segy.File
	g                                         *segy.GeometryIndex
	c                                         *segy.TimeSliceCache
	inline, crossline, time                   comparePanel
	il, xl                                    int32
	sample, sampleMin, sampleMax              int
	timeVals, timeRaster                      []float32
	timeMask                                  []bool
	inlineValues, crosslineValues             []float64
	timeMin, timeMax                          float64
	crossValid                                bool
	crossIL, crossXL, crossSample             float64
	activeAxis, hoverAxis                     int
	camAzimuth, camElevation, camZoom, camFOV float64
	camPanX, camPanY                          float64
	axisILFactor, axisXLFactor, axisZFactor   float64
	aspectMode                                int
}
type volumePrepareResult struct {
	gen    int64
	side   int
	path   string
	f      *segy.File
	g      *segy.GeometryIndex
	cache  *segy.TimeSliceCache
	detect segy.GeometryDetectResult
	stats  segy.GeometryBuildStats
	vals   []float32
	il, xl int32
	sample int
	err    error
}

var (
	volumeHwnd uintptr
	vc         volumeControls
	volumePath string
	volumeF    *segy.File
	volumeG    *segy.GeometryIndex
	volumeC    *segy.TimeSliceCache

	volumeInline, volumeCrossline, volumeTime comparePanel
	volumeIL, volumeXL                        int32
	volumeSample                              int
	volumeSampleMin, volumeSampleMax          int
	volumePaletteIndex                        int
	volumeTimeVals                            []float32
	volumeTimeRaster                          []float32
	volumeTimeMask                            []bool
	volumeInlineValues                        []float64
	volumeCrosslineValues                     []float64
	volumeTimeMin, volumeTimeMax              float64

	volumeCrossValid           bool
	volumeCrossIL              float64
	volumeCrossXL              float64
	volumeCrossSample          float64
	volumeBusy                 int32
	volumeGen                  int64
	volumePendingMu            sync.Mutex
	volumePending              *volumePrepareResult
	volumeIndexProgressVisible bool
	volumeIndexProgressGen     int64
	volumeBaseDC               uintptr
	volumeBaseBmp              uintptr
	volumeBaseOldBmp           uintptr
	volumeBaseW, volumeBaseH   int
	volumeBaseDirty            = true
	volume3D                   = true
	volumeActiveAxis           = 2 // 0 inline, 1 crossline, 2 time slice
	volumeHoverAxis            = -1
	volumeFocusPanel           = -1 // >=0: double-clicked 2-D full-view panel
	volumeMinimal              = true
	volumeViewSizeMode         = 3     // 0 compact, 1 standard, 2 large, 3 extra-large, 4 fill
	volumeAspectMode           = 0     // 0 true geometry, 1 balanced 1:1, 2 saved default shape
	volumeStyleMode            = 0     // 0 CIGVis intersections, 1 interpretation, 2 standard, 3 clean/no lines
	volumeInteractionMode      = 0     // 0 CIGVis controls, 1 classic Limage controls
	volumeDragMode             = false // CIGVis D key: left-drag plane directly
	volumeShowCoordinates      = true
	volumeAxisILFactor         = 1.0
	volumeAxisXLFactor         = 1.0
	volumeAxisZFactor          = 1.0
	volumeCamFOV               = 0.0 // 0 = orthographic; >0 = perspective FOV in degrees
	volumeDefaultAspectIL      = 1.0
	volumeDefaultAspectXL      = 1.0
	volumeDefaultAspectZ       = 0.55
	volumeHidCompare           bool
	volumeForce3DOnReady       bool // one-shot guard for a newly opened A volume
	volumeTraceInspectMode     bool

	// v1.7.0 dual 3-D compare workspace. Globals always represent the
	// currently active side; the inactive side is parked in volumeScenes.
	volumeCompareMode        bool
	volumeActiveSide         int
	volumeScenes             [2]volumeSceneState
	volumeRenderRectOverride *RECT
	volumeShowDiff           bool
	volumeResidualInline     comparePanel
	volumeResidualCrossline  comparePanel
	volumeResidualTime       comparePanel
	volumeResidualKey        string
	volumeResidualNote       string

	// CIGVis-style turntable camera for the native orthographic renderer.
	// The 3-D view remains dependency-free, but follows the same visual logic:
	// rotatable camera, true depth testing, draggable axis-aligned slice nodes,
	// intersection lines and an orientation widget.
	volumeCamAzimuth    = -145.0
	volumeCamElevation  = 24.0
	volumeCamZoom       = 1.0
	volumeCamRotating   bool
	volumeCamStartX     int
	volumeCamStartY     int
	volumeCamStartAz    float64
	volumeCamStartEl    float64
	volumeCamLastPaint  time.Time
	volumeCamPanX       float64
	volumeCamPanY       float64
	volumeCamPanning    bool
	volumeCamPanStartX  int
	volumeCamPanStartY  int
	volumeCamStartPanX  float64
	volumeCamStartPanY  float64
	volumeCamZooming    bool
	volumeCamZoomStartY int
	volumeCamStartZoom  float64
	volumeCamMoved      bool

	volumeDefaultView      volumeViewDefaults
	volumeDefaultViewReady bool

	volumeDragAxis         = -1 // -1 none, 0 Inline, 1 Crossline, 2 Time Slice
	volumeDragStartX       int
	volumeDragStartY       int
	volumeDragStartILIndex int
	volumeDragStartXLIndex int
	volumeDragStartSample  int
	volumeDragLastRender   time.Time
)

func captureVolumeSceneState() volumeSceneState {
	return volumeSceneState{
		valid: volumeF != nil && volumeG != nil,
		path:  volumePath,
		f:     volumeF, g: volumeG, c: volumeC,
		inline: volumeInline, crossline: volumeCrossline, time: volumeTime,
		il: volumeIL, xl: volumeXL, sample: volumeSample,
		sampleMin: volumeSampleMin, sampleMax: volumeSampleMax,
		timeVals: volumeTimeVals, timeRaster: volumeTimeRaster, timeMask: volumeTimeMask,
		inlineValues: volumeInlineValues, crosslineValues: volumeCrosslineValues,
		timeMin: volumeTimeMin, timeMax: volumeTimeMax,
		crossValid: volumeCrossValid, crossIL: volumeCrossIL, crossXL: volumeCrossXL, crossSample: volumeCrossSample,
		activeAxis: volumeActiveAxis, hoverAxis: volumeHoverAxis,
		camAzimuth: volumeCamAzimuth, camElevation: volumeCamElevation, camZoom: volumeCamZoom, camFOV: volumeCamFOV,
		camPanX: volumeCamPanX, camPanY: volumeCamPanY,
		axisILFactor: volumeAxisILFactor, axisXLFactor: volumeAxisXLFactor, axisZFactor: volumeAxisZFactor,
		aspectMode: volumeAspectMode,
	}
}

func applyVolumeSceneState(st volumeSceneState) {
	volumePath = st.path
	volumeF, volumeG, volumeC = st.f, st.g, st.c
	volumeInline, volumeCrossline, volumeTime = st.inline, st.crossline, st.time
	volumeIL, volumeXL, volumeSample = st.il, st.xl, st.sample
	volumeSampleMin, volumeSampleMax = st.sampleMin, st.sampleMax
	volumeTimeVals, volumeTimeRaster, volumeTimeMask = st.timeVals, st.timeRaster, st.timeMask
	volumeInlineValues, volumeCrosslineValues = st.inlineValues, st.crosslineValues
	volumeTimeMin, volumeTimeMax = st.timeMin, st.timeMax
	volumeCrossValid, volumeCrossIL, volumeCrossXL, volumeCrossSample = st.crossValid, st.crossIL, st.crossXL, st.crossSample
	volumeActiveAxis, volumeHoverAxis = st.activeAxis, st.hoverAxis
	volumeCamAzimuth, volumeCamElevation, volumeCamZoom, volumeCamFOV = st.camAzimuth, st.camElevation, st.camZoom, st.camFOV
	volumeCamPanX, volumeCamPanY = st.camPanX, st.camPanY
	volumeAxisILFactor, volumeAxisXLFactor, volumeAxisZFactor = st.axisILFactor, st.axisXLFactor, st.axisZFactor
	volumeAspectMode = st.aspectMode
}

func saveActiveVolumeScene() {
	if volumeActiveSide < 0 || volumeActiveSide > 1 || volumeF == nil {
		return
	}
	volumeScenes[volumeActiveSide] = captureVolumeSceneState()
}

func syncVolumeSceneControls() {
	if volumeF == nil {
		return
	}
	setVolumeFactorCombo(vc.axisIL, volumeAxisILFactor)
	setVolumeFactorCombo(vc.axisXL, volumeAxisXLFactor)
	setVolumeFactorCombo(vc.axisZ, volumeAxisZFactor)
	setVolumeFOVCombo(volumeCamFOV)
	if vc.aspect != 0 {
		pSendMessageW.Call(vc.aspect, CB_SETCURSEL, uintptr(volumeAspectMode), 0)
	}
	updateVolumeControls()
}

func activateVolumeScene(side int) bool {
	if side < 0 || side > 1 || !volumeScenes[side].valid {
		return false
	}
	if side == volumeActiveSide && volumeF == volumeScenes[side].f {
		return true
	}
	saveActiveVolumeScene()
	volumeActiveSide = side
	applyVolumeSceneState(volumeScenes[side])
	volumeHoverAxis = -1
	volumeDragAxis = -1
	volumeCamRotating, volumeCamPanning, volumeCamZooming = false, false, false
	syncVolumeSceneControls()
	invalidateVolumeBase()
	return true
}

func volumeSceneSideAtPoint(mx, my int) int {
	if !volumeCompareMode {
		return 0
	}
	rs := volumeCompareDisplayRects()
	for i := 0; i < 2 && i < len(rs); i++ {
		if mx >= int(rs[i].Left) && mx <= int(rs[i].Right) && my >= int(rs[i].Top) && my <= int(rs[i].Bottom) {
			return i
		}
	}
	return volumeActiveSide
}

func volumeActiveSideName() string {
	if !volumeCompareMode {
		return ""
	}
	if volumeActiveSide == 1 {
		return "B"
	}
	return "A"
}

func showVolumeWindow() {
	// Phase 1: the 2-D volume action reuses the active Dataset and carries the
	// current sample window into Workspace3D. The direct-path call below remains
	// only as a startup-failure compatibility fallback.
	if openCurrentDatasetInVolume() {
		return
	}
	if sf == nil {
		message(hwnd, "三视图联动", "请先打开一份叠后三维 SEG-Y 数据。", MB_OK|MB_ICONINFORMATION)
		return
	}
	showVolumeWindowForPath(sf.Info.Path, false)
}

// forceVolume3DViewState is deliberately idempotent.  The Home 3-D route calls
// it at several lifecycle boundaries (before window creation, after controls are
// created, and after volume data becomes ready).  This makes it impossible for
// a stale Full-View/panel state or an old tile-view state to win a race and make
// the explicit "三维数据体" entry appear as a 2-D section.
func forceVolume3DViewState() {
	volume3D = true
	volumeFocusPanel = -1
	setVolumeTraceInspectMode(false)
	volumeHoverAxis = -1
	volumeDragAxis = -1
	volumeActiveAxis = 2
	if vc.view != 0 {
		setText(vc.view, "平铺") // button action: click to leave 3-D and enter tiled view
	}
}

func setVolumeTraceInspectMode(enabled bool) {
	if enabled && (volumeFocusPanel < 0 || volumeFocusPanel > 2 || volumeF == nil || volumeG == nil) {
		enabled = false
	}
	volumeTraceInspectMode = enabled
	if vc.trace != 0 {
		state := uintptr(0)
		if enabled {
			state = BST_CHECKED
		}
		pSendMessageW.Call(vc.trace, BM_SETCHECK, state, 0)
	}
	if enabled {
		volumeHoverAxis = -1
		volumeDragAxis = -1
		volumeCamRotating, volumeCamPanning, volumeCamZooming = false, false, false
		pReleaseCapture.Call()
		updateVolumeStatusLine("2D Full View | 单道分析：单击切片选择真实 SEG-Y 道；模式保持开启，Esc 退出")
		if volumeHwnd != 0 {
			pSetFocus.Call(volumeHwnd)
		}
	}
}

func volumeTraceAnalysisSelectionAt(x, y int) (traceAnalysisSelection, error) {
	if volumeFocusPanel < 0 || volumeFocusPanel > 2 || volumeF == nil || volumeG == nil {
		return traceAnalysisSelection{}, fmt.Errorf("请先进入 Inline、Crossline 或 Time Slice 的 2D Full View")
	}
	ilValue, xlValue, sampleValue, ok := volumeWorldAtPixel(volumeFocusPanel, x, y)
	if !ok {
		return traceAnalysisSelection{}, fmt.Errorf("请在切片图像范围内选择一道")
	}
	return volumeTraceAnalysisSelectionForWorld(volumeFocusPanel, ilValue, xlValue, sampleValue)
}

// volumeTraceAnalysisSelectionForWorld is the HWND-free mapping core used by
// the Full View click handler and regression tests.
func volumeTraceAnalysisSelectionForWorld(panel int, ilValue, xlValue, sampleValue float64) (traceAnalysisSelection, error) {
	if panel < 0 || panel > 2 || volumeF == nil || volumeG == nil {
		return traceAnalysisSelection{}, fmt.Errorf("2D Full View 数据尚未就绪")
	}
	ilIndex := nearestVolumeIndex(volumeG.InlineValues, int32(math.Round(ilValue)))
	xlIndex := nearestVolumeIndex(volumeG.CrosslineValues, int32(math.Round(xlValue)))
	if ilIndex < 0 || xlIndex < 0 {
		return traceAnalysisSelection{}, fmt.Errorf("当前切片位置没有有效几何坐标")
	}
	il := volumeG.InlineValues[ilIndex]
	xl := volumeG.CrosslineValues[xlIndex]
	trace, ok := volumeG.TraceAt(il, xl)
	if !ok {
		return traceAnalysisSelection{}, fmt.Errorf("IL %d / XL %d 没有实际 SEG-Y 道", il, xl)
	}
	marker := clampInt(int(math.Round(sampleValue)), volumeSampleMin, volumeSampleMax)
	role := volumeActiveSideName()
	if role == "" {
		role = "A"
	}
	panelName := []string{"Inline", "Crossline", "Time Slice"}[panel]
	return traceAnalysisSelection{Targets: []traceAnalysisTarget{{Role: role, Path: volumeF.Info.Path, Trace: trace,
		SampleStart: volumeSampleMin, SampleEnd: volumeSampleMax, MarkerSample: marker,
		Inline: il, Crossline: xl, HasGeometry: true}},
		Context: fmt.Sprintf("%s | %s Full View | IL %d / XL %d | Time %s", role, panelName, il, xl,
			formatAdaptiveTimeMS(float64(marker*volumeF.Info.SampleIntervalUS)/1000))}, nil
}

// createVolumeWindowShell creates the real Volume top-level window without
// requiring a SEG-Y path.  The Start Center uses this BEFORE opening the file
// picker, so the 3-D card never routes through (or visibly falls back to) the
// 2-D comparison workspace.
func createVolumeWindowShell(directHomeOpen bool) bool {
	return createVolumeWindowShellMode(directHomeOpen, true)
}

// createVolumeWindowShellDeferred lets WorkspaceManager keep the previous
// workspace visible until the 3-D adapter has accepted the request.
func createVolumeWindowShellDeferred(directHomeOpen bool) bool {
	return createVolumeWindowShellMode(directHomeOpen, false)
}

func createVolumeWindowShellMode(directHomeOpen, showNow bool) bool {
	if volumeHwnd != 0 {
		if directHomeOpen {
			forceVolume3DViewState()
			destroyVolumeBase()
			invalidateVolumeBase()
		}
		if showNow {
			pShowWindow.Call(volumeHwnd, SW_SHOW)
			pSetForeground.Call(volumeHwnd)
		}
		return true
	}

	if directHomeOpen {
		originValid = false
		originSampleStart = 0
		originSampleEnd = -1
	}
	volumePath = ""
	volumeCrossValid = false
	volumeCompareMode = false
	volumeShowDiff = false
	volumeActiveSide = 0
	volumeScenes = [2]volumeSceneState{}
	volumeInline, volumeCrossline, volumeTime = comparePanel{}, comparePanel{}, comparePanel{}
	volumeInlineValues, volumeCrosslineValues = nil, nil
	volumeTimeVals, volumeTimeRaster, volumeTimeMask = nil, nil, nil
	volumeTimeMin, volumeTimeMax = -1, 1
	volumeF, volumeG, volumeC = nil, nil, nil
	loadVolumeViewDefaults()
	// loadVolumeViewDefaults intentionally restores presentation choices only.
	// Re-assert the actual display topology AFTER loading defaults.
	forceVolume3DViewState()

	h, _, _ := pCreateWindowExW.Call(WS_EX_APPWINDOW, uintptr(unsafe.Pointer(u16("Limage64Volume"))), uintptr(unsafe.Pointer(u16(APP_NAME+" v"+APP_VERSION+" - 三维数据体"))),
		WS_OVERLAPPEDWINDOW|WS_CLIPCHILDREN, uintptr(CW_USEDEFAULT), uintptr(CW_USEDEFAULT), 1520, 960, 0, 0, 0, 0)
	if h == 0 {
		return false
	}
	volumeHwnd = h
	createVolumeUI()
	forceVolume3DViewState()
	setText(vc.status, "3D View：请选择或拖入一份叠后三维 SEG-Y 数据。")
	acceptSegyDrops(h)
	registerOleSegyDropTarget(h, workspaceMode3D)

	volumeHidCompare = false
	if showNow {
		// Legacy callers still use the original immediate-show behavior. The
		// Phase 1 adapter defers this block until WorkspaceManager commits.
		if compareHwnd != 0 {
			pShowWindow.Call(compareHwnd, SW_HIDE)
			volumeHidCompare = true
		}
		pShowWindow.Call(h, SW_SHOW)
		pUpdateWindow.Call(h)
		pSetForeground.Call(h)
	}
	return true
}

// showVolumeHomeShell is the first half of the explicit Home -> 3-D route.
// It presents a genuine 3-D top-level window before any file chooser appears.
func showVolumeHomeShell() bool { return createVolumeWindowShell(true) }

// loadVolumePathIntoShell starts A-side preparation in an already-created
// Volume window.  It never touches sf/loadFile/rerender/workspaceSyncAFromMain.
func loadVolumePathIntoShell(path string, directHomeOpen bool) bool {
	if path == "" || volumeHwnd == 0 {
		return false
	}
	if directHomeOpen {
		originValid = false
		originSampleStart = 0
		originSampleEnd = -1
	}
	// If an empty shell is being reused after a cancelled/failed selection,
	// clear any partial A/B state before installing the new path.
	atomic.AddInt64(&volumeGen, 1)
	if volumeF != nil {
		volumeF.Close()
	}
	volumeF, volumeG, volumeC = nil, nil, nil
	volumeScenes = [2]volumeSceneState{}
	volumeCompareMode = false
	volumeShowDiff = false
	volumeActiveSide = 0
	volumeInline, volumeCrossline, volumeTime = comparePanel{}, comparePanel{}, comparePanel{}
	volumeInlineValues, volumeCrosslineValues = nil, nil
	volumeTimeVals, volumeTimeRaster, volumeTimeMask = nil, nil, nil
	volumeTimeMin, volumeTimeMax = -1, 1
	volumePath = path
	volumeForce3DOnReady = true
	forceVolume3DViewState()
	destroyVolumeBase()
	invalidateVolumeBase()
	setText(vc.status, "3D View：正在识别 Inline/Crossline 并建立三维体...")
	startVolumePrepareForSide(path, 0)
	return true
}

// showVolumeWindowForPath opens the 3-D workspace directly from a SEG-Y path.
// When directHomeOpen is true it is a true 3-D-only route: no 2-D loader or
// reconstructed section workspace participates in the operation.
func showVolumeWindowForPath(path string, directHomeOpen bool) {
	if path == "" {
		return
	}
	if !createVolumeWindowShell(directHomeOpen) {
		return
	}
	loadVolumePathIntoShell(path, directHomeOpen)
}

func createVolumeUI() {
	vc = volumeControls{}
	// Compact CIGVis-style controller strip: keep the scene visually dominant.
	createCtrl(volumeHwnd, "STATIC", "IL", WS_CHILD|WS_VISIBLE, 10, 12, 20, 18, 0)
	vc.ilPrev = createCtrl(volumeHwnd, "BUTTON", "◀", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 32, 8, 25, 23, IDVOL_ILPREV)
	vc.ilEdit = createCtrl(volumeHwnd, "EDIT", "-", WS_CHILD|WS_VISIBLE|WS_BORDER|ES_READONLY, 60, 8, 60, 23, IDVOL_IL)
	vc.ilNext = createCtrl(volumeHwnd, "BUTTON", "▶", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 123, 8, 25, 23, IDVOL_ILNEXT)

	createCtrl(volumeHwnd, "STATIC", "XL", WS_CHILD|WS_VISIBLE, 160, 12, 22, 18, 0)
	vc.xlPrev = createCtrl(volumeHwnd, "BUTTON", "◀", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 184, 8, 25, 23, IDVOL_XLPREV)
	vc.xlEdit = createCtrl(volumeHwnd, "EDIT", "-", WS_CHILD|WS_VISIBLE|WS_BORDER|ES_READONLY, 212, 8, 60, 23, IDVOL_XL)
	vc.xlNext = createCtrl(volumeHwnd, "BUTTON", "▶", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 275, 8, 25, 23, IDVOL_XLNEXT)

	createCtrl(volumeHwnd, "STATIC", "T", WS_CHILD|WS_VISIBLE, 314, 12, 14, 18, 0)
	vc.tPrev = createCtrl(volumeHwnd, "BUTTON", "◀", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 330, 8, 25, 23, IDVOL_TPREV)
	vc.tEdit = createCtrl(volumeHwnd, "EDIT", "-", WS_CHILD|WS_VISIBLE|WS_BORDER|ES_READONLY, 358, 8, 76, 23, IDVOL_TIME)
	vc.tNext = createCtrl(volumeHwnd, "BUTTON", "▶", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 437, 8, 25, 23, IDVOL_TNEXT)

	createCtrl(volumeHwnd, "STATIC", "色标", WS_CHILD|WS_VISIBLE, 480, 12, 30, 18, 0)
	vc.palette = createCtrl(volumeHwnd, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 512, 7, 124, 380, IDVOL_PALETTE)
	for _, pi := range volumePaletteOrder {
		name := paletteNames[pi]
		pSendMessageW.Call(vc.palette, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(name))))
	}
	pSendMessageW.Call(vc.palette, CB_SETCURSEL, uintptr(volumePaletteComboIndex(volumePaletteIndex)), 0)

	createCtrl(volumeHwnd, "STATIC", "大小", WS_CHILD|WS_VISIBLE, 648, 12, 30, 18, 0)
	vc.size = createCtrl(volumeHwnd, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 680, 7, 82, 150, IDVOL_SIZE)
	for _, name := range []string{"紧凑", "标准", "大", "超大", "填充"} {
		pSendMessageW.Call(vc.size, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(name))))
	}
	pSendMessageW.Call(vc.size, CB_SETCURSEL, uintptr(volumeViewSizeMode), 0)

	createCtrl(volumeHwnd, "STATIC", "比例", WS_CHILD|WS_VISIBLE, 774, 12, 30, 18, 0)
	vc.aspect = createCtrl(volumeHwnd, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 806, 7, 82, 100, IDVOL_ASPECT)
	for _, name := range []string{"实际", "均衡", "默认"} {
		pSendMessageW.Call(vc.aspect, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(name))))
	}
	pSendMessageW.Call(vc.aspect, CB_SETCURSEL, uintptr(volumeAspectMode), 0)

	createCtrl(volumeHwnd, "STATIC", "风格", WS_CHILD|WS_VISIBLE, 898, 12, 30, 18, 0)
	vc.style = createCtrl(volumeHwnd, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 930, 7, 88, 120, IDVOL_STYLE)
	for _, mode := range volumeStyleComboOrder {
		name := []string{"CIGVis", "解释", "标准", "干净"}[mode]
		pSendMessageW.Call(vc.style, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(name))))
	}
	pSendMessageW.Call(vc.style, CB_SETCURSEL, uintptr(volumeStyleComboIndex(volumeStyleMode)), 0)

	// Interaction is intentionally no longer exposed as a UI option. The native
	// CIGVis-style interaction is the single default: left drag rotates,
	// Ctrl+left drags a slice, Shift+left pans, right drag/wheel zooms.
	volumeInteractionMode = 0
	vc.auto = createCtrl(volumeHwnd, "BUTTON", "识别", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 1032, 8, 46, 23, IDVOL_AUTO)
	vc.reset = createCtrl(volumeHwnd, "BUTTON", "复位", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 1082, 8, 46, 23, IDVOL_RESET)
	vc.view = createCtrl(volumeHwnd, "BUTTON", "平铺", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 1132, 8, 46, 23, IDVOL_VIEW)
	vc.close = createCtrl(volumeHwnd, "BUTTON", "关闭", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 1182, 8, 50, 23, IDVOL_CLOSE)

	// Second row: view geometry / camera controls. This mirrors the useful CIGVis
	// z/FOV controls without cluttering the primary slice navigation row.
	vc.coords = createCtrl(volumeHwnd, "BUTTON", "坐标轴", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_AUTOCHECKBOX, 12, 36, 58, 22, IDVOL_COORDS)
	if volumeShowCoordinates {
		pSendMessageW.Call(vc.coords, BM_SETCHECK, BST_CHECKED, 0)
	}
	createCtrl(volumeHwnd, "STATIC", "IL×", WS_CHILD|WS_VISIBLE, 82, 40, 24, 18, 0)
	vc.axisIL = createCtrl(volumeHwnd, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 108, 35, 64, 180, IDVOL_AXIS_IL)
	createCtrl(volumeHwnd, "STATIC", "XL×", WS_CHILD|WS_VISIBLE, 182, 40, 26, 18, 0)
	vc.axisXL = createCtrl(volumeHwnd, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 210, 35, 64, 180, IDVOL_AXIS_XL)
	createCtrl(volumeHwnd, "STATIC", "Z×", WS_CHILD|WS_VISIBLE, 284, 40, 22, 18, 0)
	vc.axisZ = createCtrl(volumeHwnd, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 308, 35, 64, 180, IDVOL_AXIS_Z)
	for _, h := range []uintptr{vc.axisIL, vc.axisXL, vc.axisZ} {
		for _, v := range volumeAxisFactorOptions {
			pSendMessageW.Call(h, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(fmt.Sprintf("%.2f", v)))))
		}
	}
	setVolumeFactorCombo(vc.axisIL, volumeAxisILFactor)
	setVolumeFactorCombo(vc.axisXL, volumeAxisXLFactor)
	setVolumeFactorCombo(vc.axisZ, volumeAxisZFactor)

	createCtrl(volumeHwnd, "STATIC", "FOV", WS_CHILD|WS_VISIBLE, 384, 40, 30, 18, 0)
	vc.fov = createCtrl(volumeHwnd, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 416, 35, 72, 360, IDVOL_FOV)
	for _, v := range volumeFOVOptions {
		label := fmt.Sprintf("%.0f°", v)
		if v == 0 {
			label = "0° Ortho"
		}
		pSendMessageW.Call(vc.fov, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(label))))
	}
	setVolumeFOVCombo(volumeCamFOV)

	vc.saveDefault = createCtrl(volumeHwnd, "BUTTON", "设为默认", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 500, 35, 72, 23, IDVOL_DEFAULT)
	vc.help = createCtrl(volumeHwnd, "BUTTON", "帮助", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 578, 35, 52, 23, IDVOL_HELP)
	vc.compare = createCtrl(volumeHwnd, "BUTTON", "比", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 640, 35, 42, 23, IDVOL_COMPARE)
	vc.matchLeft = createCtrl(volumeHwnd, "BUTTON", "与左图一致", WS_CHILD|WS_TABSTOP|BS_PUSHBUTTON, 688, 35, 86, 23, IDVOL_MATCHLEFT)
	vc.diff = createCtrl(volumeHwnd, "BUTTON", "差", WS_CHILD|WS_TABSTOP|BS_AUTOCHECKBOX|BS_PUSHLIKE, 780, 35, 42, 23, IDVOL_DIFF)
	vc.export = createCtrl(volumeHwnd, "BUTTON", "出", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 828, 35, 42, 23, IDVOL_EXPORT)
	vc.trace = createCtrl(volumeHwnd, "BUTTON", "道", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_AUTOCHECKBOX|BS_PUSHLIKE, 878, 35, 42, 23, IDVOL_TRACE)
	pEnableWindow.Call(vc.trace, 0)
	vc.status = createCtrl(volumeHwnd, "STATIC", "正在准备三维体...", WS_CHILD|WS_VISIBLE, 10, 64, 1480, 18, IDVOL_STATUS)
	vc.indexProgress = createCtrl(volumeHwnd, "msctls_progress32", "", WS_CHILD, 1240, 64, 250, 17, 0)
	pSendMessageW.Call(vc.indexProgress, PBM_SETRANGE, 0, uintptr(uint32(100)<<16))
	layoutVolumeIndexProgress()
}

func layoutVolumeIndexProgress() {
	if volumeHwnd == 0 || vc.status == 0 {
		return
	}
	cw, _ := clientSize(volumeHwnd)
	if volumeIndexProgressVisible && vc.indexProgress != 0 {
		progressWidth := 250
		if cw < 980 {
			progressWidth = 190
		}
		progressX := maxInt(250, cw-progressWidth-10)
		pMoveWindow.Call(vc.status, 10, 64, uintptr(maxInt(220, progressX-20)), 18, 1)
		pMoveWindow.Call(vc.indexProgress, uintptr(progressX), 64, uintptr(progressWidth), 17, 1)
		pShowWindow.Call(vc.indexProgress, SW_SHOW)
		return
	}
	pMoveWindow.Call(vc.status, 10, 64, uintptr(maxInt(220, cw-20)), 18, 1)
	if vc.indexProgress != 0 {
		pShowWindow.Call(vc.indexProgress, SW_HIDE)
	}
}

func showVolumeIndexProgress(gen int64, percent int) {
	if volumeHwnd == 0 || vc.indexProgress == 0 {
		return
	}
	volumeIndexProgressGen = gen
	volumeIndexProgressVisible = true
	percent = clampInt(percent, 0, 100)
	pSendMessageW.Call(vc.indexProgress, PBM_SETPOS, uintptr(percent), 0)
	layoutVolumeIndexProgress()
}

func hideVolumeIndexProgress(gen int64) {
	if gen != 0 && volumeIndexProgressGen != gen {
		return
	}
	volumeIndexProgressVisible = false
	volumeIndexProgressGen = 0
	if vc.indexProgress != 0 {
		pSendMessageW.Call(vc.indexProgress, PBM_SETPOS, 0, 0)
	}
	layoutVolumeIndexProgress()
}

func postVolumeIndexProgress(gen int64, percent int) {
	if volumeHwnd != 0 {
		pPostMessageW.Call(volumeHwnd, WM_VOLUME_INDEX_PROGRESS, uintptr(clampInt(percent, 0, 100)), uintptr(gen))
	}
}

func handleVolumeIndexProgress(percent int, gen int64) {
	if gen != atomic.LoadInt64(&volumeGen) || !phase1VolumeResultCurrent() {
		return
	}
	showVolumeIndexProgress(gen, percent)
	switch {
	case percent < 10:
		setText(vc.status, "正在打开 SEG-Y 并识别三维几何...")
	case percent < 90:
		setText(vc.status, fmt.Sprintf("正在建立三维索引... %d%%", percent))
	case percent < 100:
		setText(vc.status, "三维索引已就绪，正在加载首个切片...")
	default:
		setText(vc.status, "首个切片已加载，正在准备三维场景...")
	}
}

func volumePanelRects() [3]RECT {
	cw, ch := clientSize(volumeHwnd)
	top := 130
	bottom := 42
	margin := 50
	gap := 54
	total := cw - margin*2 - gap*2
	if total < 600 {
		total = 600
	}
	each := total / 3
	h := ch - top - bottom
	if h < 250 {
		h = 250
	}
	var out [3]RECT
	for i := 0; i < 3; i++ {
		left := margin + i*(each+gap)
		out[i] = RECT{Left: int32(left), Top: int32(top), Right: int32(left + each), Bottom: int32(top + h)}
	}
	return out
}

type volumePt struct{ x, y float64 }

func addVolumePt(a, b volumePt) volumePt           { return volumePt{a.x + b.x, a.y + b.y} }
func scaleVolumePt(a volumePt, s float64) volumePt { return volumePt{a.x * s, a.y * s} }

type volumePlaneGeom struct {
	panel        int
	origin, u, v volumePt
	corners      [4]volumePt // p00, p10, p11, p01
}

func volumeSingleCubeRect() RECT {
	cw, ch := clientSize(volumeHwnd)
	left, right := 30, 30
	top, bottom := 84, 22
	if volumeShowCoordinates {
		left, right, bottom = 92, 36, 26
	}
	if cw < 900 {
		left, right = 54, 24
		if !volumeShowCoordinates {
			left = 16
		}
	}
	if cw < 560 {
		left, right = 24, 14
	}
	if ch < 520 {
		top, bottom = 80, 24
	}
	return RECT{Left: int32(left), Top: int32(top), Right: int32(maxInt(left+10, cw-right)), Bottom: int32(maxInt(top+10, ch-bottom))}
}

func volumeCompareDisplayRects() []RECT {
	cw, ch := clientSize(volumeHwnd)
	top, bottom := 104, 28
	outer := 20
	gap := 34
	if volumeShowCoordinates {
		outer = 44
		gap = 44
	}
	n := 2
	if volumeShowDiff {
		n = 3
	}
	avail := cw - 2*outer - (n-1)*gap
	if avail < n*180 {
		outer = 10
		gap = 18
		avail = cw - 2*outer - (n-1)*gap
	}
	each := maxInt(avail/n, 160)
	out := make([]RECT, n)
	for i := 0; i < n; i++ {
		left := outer + i*(each+gap)
		right := left + each
		if i == n-1 {
			right = cw - outer
		}
		out[i] = RECT{Left: int32(left), Top: int32(top), Right: int32(maxInt(left+10, right)), Bottom: int32(maxInt(top+10, ch-bottom))}
	}
	return out
}

func volumeCompareSceneRects() [2]RECT {
	rs := volumeCompareDisplayRects()
	var out [2]RECT
	if len(rs) > 0 {
		out[0] = rs[0]
	}
	if len(rs) > 1 {
		out[1] = rs[1]
	}
	return out
}

func volumeCubeRect() RECT {
	if volumeRenderRectOverride != nil {
		return *volumeRenderRectOverride
	}
	if volumeCompareMode {
		rs := volumeCompareSceneRects()
		if volumeActiveSide < 0 || volumeActiveSide > 1 {
			return rs[0]
		}
		return rs[volumeActiveSide]
	}
	return volumeSingleCubeRect()
}

func volumeFocusRect() RECT {
	cw, ch := clientSize(volumeHwnd)
	left, right := 72, 42
	top, bottom := 116, 58
	return RECT{Left: int32(left), Top: int32(top), Right: int32(maxInt(left+10, cw-right)), Bottom: int32(maxInt(top+10, ch-bottom))}
}

func volumePanelData(panel int) comparePanel {
	switch panel {
	case 0:
		return volumeInline
	case 1:
		return volumeCrossline
	default:
		return volumeTime
	}
}

type volumeVec3 struct{ x, y, z float64 }

func addVolumeVec3(a, b volumeVec3) volumeVec3 { return volumeVec3{a.x + b.x, a.y + b.y, a.z + b.z} }
func scaleVolumeVec3(a volumeVec3, s float64) volumeVec3 {
	return volumeVec3{a.x * s, a.y * s, a.z * s}
}
func dotVolumeVec3(a, b volumeVec3) float64 { return a.x*b.x + a.y*b.y + a.z*b.z }
func crossVolumeVec3(a, b volumeVec3) volumeVec3 {
	return volumeVec3{a.y*b.z - a.z*b.y, a.z*b.x - a.x*b.z, a.x*b.y - a.y*b.x}
}
func normVolumeVec3(a volumeVec3) float64 { return math.Sqrt(dotVolumeVec3(a, a)) }
func normalizeVolumeVec3(a volumeVec3) volumeVec3 {
	n := normVolumeVec3(a)
	if n < 1e-12 {
		return volumeVec3{}
	}
	return scaleVolumeVec3(a, 1/n)
}

var volumeAxisFactorOptions = []float64{0.10, 0.15, 0.20, 0.25, 0.33, 0.40, 0.50, 0.67, 0.75, 0.85, 1.00, 1.10, 1.25, 1.50, 2.00, 2.50, 3.00, 4.00, 5.00, 6.00, 8.00}
var volumeFOVOptions = []float64{0, 5, 10, 15, 20, 25, 30, 35, 40, 45, 50, 55, 60, 70, 80, 90, 100, 110, 120}
var pGetKeyStateVolume = user32.NewProc("GetKeyState")

const vkShiftVolume = 0x10

type volumeViewDefaults struct {
	Version         int     `json:"version"`
	ShowCoordinates bool    `json:"show_coordinates"`
	ViewSizeMode    int     `json:"view_size_mode"`
	AspectMode      int     `json:"aspect_mode"`
	StyleMode       int     `json:"style_mode"`
	InteractionMode int     `json:"interaction_mode"`
	PaletteIndex    int     `json:"palette_index"`
	AxisILFactor    float64 `json:"axis_il_factor"`
	AxisXLFactor    float64 `json:"axis_xl_factor"`
	AxisZFactor     float64 `json:"axis_z_factor"`
	FOV             float64 `json:"fov"`
	Azimuth         float64 `json:"azimuth"`
	Elevation       float64 `json:"elevation"`
	Zoom            float64 `json:"zoom"`
	PanX            float64 `json:"pan_x"`
	PanY            float64 `json:"pan_y"`
	DefaultAspectIL float64 `json:"default_aspect_il"`
	DefaultAspectXL float64 `json:"default_aspect_xl"`
	DefaultAspectZ  float64 `json:"default_aspect_z"`
}

func volumeDefaultsPath() string { return licenseDirectory() + `\volume_view.json` }

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func nearestVolumeOptionIndex(opts []float64, v float64) int {
	best := 0
	bestD := math.Inf(1)
	for i, x := range opts {
		d := math.Abs(x - v)
		if d < bestD {
			best, bestD = i, d
		}
	}
	return best
}

func setVolumeFactorCombo(h uintptr, v float64) {
	if h == 0 {
		return
	}
	pSendMessageW.Call(h, CB_SETCURSEL, uintptr(nearestVolumeOptionIndex(volumeAxisFactorOptions, v)), 0)
}

func setVolumeFOVCombo(v float64) {
	if vc.fov == 0 {
		return
	}
	pSendMessageW.Call(vc.fov, CB_SETCURSEL, uintptr(nearestVolumeOptionIndex(volumeFOVOptions, v)), 0)
}

func volumeShiftDown() bool {
	r, _, _ := pGetKeyStateVolume.Call(vkShiftVolume)
	return int16(r&0xffff) < 0
}

func captureVolumeViewDefaults() volumeViewDefaults {
	return volumeViewDefaults{
		Version:         1,
		ShowCoordinates: volumeShowCoordinates,
		ViewSizeMode:    volumeViewSizeMode,
		AspectMode:      volumeAspectMode,
		StyleMode:       volumeStyleMode,
		InteractionMode: 0,
		PaletteIndex:    volumePaletteIndex,
		AxisILFactor:    volumeAxisILFactor,
		AxisXLFactor:    volumeAxisXLFactor,
		AxisZFactor:     volumeAxisZFactor,
		FOV:             volumeCamFOV,
		Azimuth:         volumeCamAzimuth,
		Elevation:       volumeCamElevation,
		Zoom:            volumeCamZoom,
		PanX:            volumeCamPanX,
		PanY:            volumeCamPanY,
		DefaultAspectIL: volumeDefaultAspectIL,
		DefaultAspectXL: volumeDefaultAspectXL,
		DefaultAspectZ:  volumeDefaultAspectZ,
	}
}

func applyVolumeViewDefaults(d volumeViewDefaults) {
	volumeShowCoordinates = d.ShowCoordinates
	volumeViewSizeMode = clampInt(d.ViewSizeMode, 0, 4)
	volumeAspectMode = clampInt(d.AspectMode, 0, 2)
	volumeStyleMode = clampInt(d.StyleMode, 0, 3)
	volumeInteractionMode = 0 // single default interaction; legacy saved value is ignored
	if d.PaletteIndex >= 0 && d.PaletteIndex < len(paletteNames) {
		volumePaletteIndex = d.PaletteIndex
	}
	volumeAxisILFactor = clampFloat(d.AxisILFactor, .10, 8)
	volumeAxisXLFactor = clampFloat(d.AxisXLFactor, .10, 8)
	volumeAxisZFactor = clampFloat(d.AxisZFactor, .10, 8)
	if volumeAxisILFactor == 0 {
		volumeAxisILFactor = 1
	}
	if volumeAxisXLFactor == 0 {
		volumeAxisXLFactor = 1
	}
	if volumeAxisZFactor == 0 {
		volumeAxisZFactor = 1
	}
	volumeCamFOV = clampFloat(d.FOV, 0, 120)
	volumeCamAzimuth = d.Azimuth
	volumeCamElevation = clampFloat(d.Elevation, -80, 80)
	volumeCamZoom = clampFloat(d.Zoom, .55, 3.5)
	if volumeCamZoom == 0 {
		volumeCamZoom = 1
	}
	volumeCamPanX, volumeCamPanY = d.PanX, d.PanY
	volumeDefaultAspectIL = d.DefaultAspectIL
	volumeDefaultAspectXL = d.DefaultAspectXL
	volumeDefaultAspectZ = d.DefaultAspectZ
	if volumeDefaultAspectIL <= 0 || volumeDefaultAspectXL <= 0 || volumeDefaultAspectZ <= 0 {
		volumeDefaultAspectIL, volumeDefaultAspectXL, volumeDefaultAspectZ = 1, 1, .55
	}
	volumeMinimal = volumeStyleMode != 2
	volumeDragMode = false
}

func factoryVolumeViewDefaults() volumeViewDefaults {
	return volumeViewDefaults{
		Version:         1,
		ShowCoordinates: true,
		ViewSizeMode:    3,
		AspectMode:      0,
		StyleMode:       0,
		InteractionMode: 0,
		PaletteIndex:    paletteIndex,
		AxisILFactor:    1,
		AxisXLFactor:    1,
		AxisZFactor:     1,
		FOV:             35,
		Azimuth:         -145,
		Elevation:       24,
		Zoom:            1,
		PanX:            0,
		PanY:            0,
		DefaultAspectIL: 1,
		DefaultAspectXL: 1,
		DefaultAspectZ:  .55,
	}
}

func loadVolumeViewDefaults() {
	d := factoryVolumeViewDefaults()
	if b, err := os.ReadFile(volumeDefaultsPath()); err == nil {
		var saved volumeViewDefaults
		if json.Unmarshal(b, &saved) == nil && saved.Version == 1 {
			d = saved
		}
	}
	applyVolumeViewDefaults(d)
	volumeDefaultView = captureVolumeViewDefaults()
	volumeDefaultViewReady = true
	volumeCamRotating, volumeCamPanning, volumeCamZooming = false, false, false
}

func saveVolumeViewDefaults() error {
	// "Set as default" freezes the CURRENT base display shape. New datasets can
	// then choose 比例=默认 to reproduce this same visual proportion rather than
	// recomputing from their geometry. X/Y/Z user multipliers remain independent.
	bi, bx, bz := volumeAspectBaseScales()
	volumeDefaultAspectIL, volumeDefaultAspectXL, volumeDefaultAspectZ = bi, bx, bz
	volumeAspectMode = 2
	if vc.aspect != 0 {
		pSendMessageW.Call(vc.aspect, CB_SETCURSEL, 2, 0)
	}
	d := captureVolumeViewDefaults()
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(licenseDirectory(), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(volumeDefaultsPath(), append(b, '\n'), 0600); err != nil {
		return err
	}
	volumeDefaultView = d
	volumeDefaultViewReady = true
	return nil
}

func volumeProjectionName() string {
	if volumeCamFOV <= .01 {
		return "Orthographic"
	}
	return fmt.Sprintf("Perspective %.0f°", volumeCamFOV)
}

func adjustVolumeAxisFactor(axis int, increase bool) {
	cur := volumeAxisZFactor
	h := vc.axisZ
	if axis == 0 {
		cur, h = volumeAxisILFactor, vc.axisIL
	}
	if axis == 1 {
		cur, h = volumeAxisXLFactor, vc.axisXL
	}
	i := nearestVolumeOptionIndex(volumeAxisFactorOptions, cur)
	if increase && i < len(volumeAxisFactorOptions)-1 {
		i++
	}
	if !increase && i > 0 {
		i--
	}
	v := volumeAxisFactorOptions[i]
	switch axis {
	case 0:
		volumeAxisILFactor = v
	case 1:
		volumeAxisXLFactor = v
	default:
		volumeAxisZFactor = v
	}
	setVolumeFactorCombo(h, v)
	invalidateVolumeBase()
}

func adjustVolumeFOV(increase bool) {
	i := nearestVolumeOptionIndex(volumeFOVOptions, volumeCamFOV)
	if increase && i < len(volumeFOVOptions)-1 {
		i++
	}
	if !increase && i > 0 {
		i--
	}
	volumeCamFOV = volumeFOVOptions[i]
	setVolumeFOVCombo(volumeCamFOV)
	invalidateVolumeBase()
}

func adjustVolumeOverallScale(increase bool) {
	if increase {
		volumeCamZoom *= 1.10
	} else {
		volumeCamZoom /= 1.10
	}
	volumeCamZoom = clampFloat(volumeCamZoom, .40, 5.0)
	invalidateVolumeBase()
}

func volumeShortcutHelpText() string {
	return APP_NAME + " 3D View 快捷键\n\n" +
		"X / Shift+X    增大 / 减小 X（Inline）轴长度（0.10×–8.00×）\n" +
		"Y / Shift+Y    增大 / 减小 Y（Crossline）轴长度（0.10×–8.00×）\n" +
		"Z / Shift+Z    增大 / 减小 Z（Time）轴长度（0.10×–8.00×）\n" +
		"R / Shift+R    整体放大 / 缩小三维体\n" +
		"F / Shift+F    增大 / 减小 FOV（0° 为正交投影）\n" +
		"Q / W          体显示增益 +1% / -1%（Inline / Crossline / Time 同步刷新）\n" +
		"E / Shift+E    下一 / 上一色标\n" +
		"A              显示当前 Camera / Axis 参数\n" +
		"D              CIGVis Drag Mode 开 / 关\n" +
		"Space          恢复已保存的默认 Camera\n" +
		"Esc            从 2D Full View 返回 3D\n\n" +
		"双击切片       打开该切片 2D Full View（不改变三维交点）\n" +
		"Ctrl+双击      在点击处重新定位三维交点（保持 3D）\n\n" +
		"坐标轴：IL / XL 分别贴近后上方数据边，T 单独贴近屏幕左侧竖边；三轴均显示最小值、当前值和最大值。\n" +
		"风格=干净：只显示地震纹理；不画 Bounding Box、切片边框或三面交界线。\n\n" +
		"3D 对比：点击“比”加载 B，左右视图可独立旋转/缩放/FOV/轴比例/切片位置；点击任一侧后顶部控件作用于该侧。\n" +
		"差：在 A/B 旁增加 A-B 三维残差视图；残差位置和 Camera 跟随左图，便于同位置 QC。\n" +
		"与左图一致：把 B 的 IL/XL/Time、Camera、FOV、Zoom/Pan 与 X/Y/Z 比例快速同步到 A。\n" +
		"出：按 IL / XL / Time 自定义范围导出 A、B 或 A-B 三维子体 SEG-Y。\n\n" +
		"CIGVis 交互：左键旋转，Ctrl+左键拖切片，Shift+左键平移，右键拖动/滚轮缩放。"
}

func handleVolumeShortcut(key uintptr) bool {
	if volumeHwnd == 0 || !volume3D || volumeFocusPanel >= 0 {
		return false
	}
	increase := !volumeShiftDown()
	switch key {
	case 'X', 'x':
		adjustVolumeAxisFactor(0, increase)
		updateVolumeStatusLine(fmt.Sprintf("X / Inline axis ×%.2f", volumeAxisILFactor))
		return true
	case 'Y', 'y':
		adjustVolumeAxisFactor(1, increase)
		updateVolumeStatusLine(fmt.Sprintf("Y / Crossline axis ×%.2f", volumeAxisXLFactor))
		return true
	case 'Z', 'z':
		adjustVolumeAxisFactor(2, increase)
		updateVolumeStatusLine(fmt.Sprintf("Z / Time axis ×%.2f", volumeAxisZFactor))
		return true
	case 'R', 'r':
		adjustVolumeOverallScale(increase)
		updateVolumeStatusLine(fmt.Sprintf("Overall scale %.2fx", volumeCamZoom))
		return true
	case 'F', 'f':
		adjustVolumeFOV(increase)
		updateVolumeStatusLine("Projection: " + volumeProjectionName())
		return true
	case 'A', 'a':
		updateVolumeStatusLine(fmt.Sprintf("Camera | Az %.1f°  El %.1f°  Scale %.2fx  FOV %.0f°  Pan(%.0f,%.0f)  Axis X/IL×%.2f Y/XL×%.2f Z/T×%.2f", volumeCamAzimuth, volumeCamElevation, volumeCamZoom, volumeCamFOV, volumeCamPanX, volumeCamPanY, volumeAxisILFactor, volumeAxisXLFactor, volumeAxisZFactor))
		return true
	case 'D', 'd':
		if volumeInteractionMode != 0 {
			return false
		}
		volumeDragMode = !volumeDragMode
		volumeHoverAxis = -1
		invalidateVolumeBase()
		if volumeDragMode {
			updateVolumeStatusLine("Drag Mode: ON | Left drag slice")
		} else {
			updateVolumeStatusLine("Drag Mode: OFF | Left rotate, Ctrl+Left drag")
		}
		return true
	case VK_SPACE:
		resetVolumeCamera()
		volumeHoverAxis = -1
		volumeCrossValid = false
		invalidateVolumeBase()
		updateVolumeStatusLine("Camera reset | " + volumeProjectionName())
		return true
	}
	return false
}

var volumePaletteOrder = []int{1, 2, 0, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19}

func volumePaletteComboIndex(palette int) int {
	for i, p := range volumePaletteOrder {
		if p == palette {
			return i
		}
	}
	return 1 // default 黑灰白
}

func volumePaletteFromCombo(combo int) int {
	if combo < 0 || combo >= len(volumePaletteOrder) {
		return 2
	}
	return volumePaletteOrder[combo]
}

// volumeAspectBaseScales returns the base scene shape before the independent
// X/Y/Z user multipliers are applied.
//
//	实际: true IL:XL survey ratio (no visual clamp/compression).
//	均衡: 1:1 horizontal interpretation view.
//	默认: the exact base shape captured by the last "设为默认" action.
func volumeAspectBaseScales() (si, sx, st float64) {
	si, sx, st = 1, 1, .55
	switch volumeAspectMode {
	case 2:
		if volumeDefaultAspectIL > 0 && volumeDefaultAspectXL > 0 && volumeDefaultAspectZ > 0 {
			return volumeDefaultAspectIL, volumeDefaultAspectXL, volumeDefaultAspectZ
		}
		return
	case 1:
		return
	}
	if volumeG == nil {
		return
	}
	b := volumeG.Bounds()
	spanIL := math.Abs(float64(b.InlineMax - b.InlineMin))
	spanXL := math.Abs(float64(b.CrosslineMax - b.CrosslineMin))
	if spanIL < 1 && len(volumeG.InlineValues) > 1 {
		spanIL = float64(len(volumeG.InlineValues) - 1)
	}
	if spanXL < 1 && len(volumeG.CrosslineValues) > 1 {
		spanXL = float64(len(volumeG.CrosslineValues) - 1)
	}
	if spanIL >= 1 && spanXL >= 1 {
		ratio := spanIL / spanXL
		if ratio > 0 && !math.IsInf(ratio, 0) && !math.IsNaN(ratio) {
			// Geometric-mean normalization preserves the TRUE horizontal ratio while
			// keeping auto-fit numerically stable. Z remains independently controlled.
			si = math.Sqrt(ratio)
			sx = 1 / si
		}
	}
	return
}

func volumeAxisScales() (si, sx, st float64) {
	si, sx, st = volumeAspectBaseScales()
	si *= volumeAxisILFactor
	sx *= volumeAxisXLFactor
	st *= volumeAxisZFactor
	return
}

func volumeViewFitFraction() float64 {
	switch volumeViewSizeMode {
	case 0:
		return .70
	case 1:
		return .78
	case 2:
		return .84
	case 4:
		return .96
	default: // extra-large, default
		return .90
	}
}

func resetVolumeCamera() {
	// Space / Reset returns to the user-saved camera when available. The saved
	// axis/FOV/style settings remain independent controls and are not overwritten.
	az, el, zoom, panX, panY, fov := -145.0, 24.0, 1.0, 0.0, 0.0, 35.0
	if volumeDefaultViewReady {
		az, el = volumeDefaultView.Azimuth, volumeDefaultView.Elevation
		zoom, panX, panY = volumeDefaultView.Zoom, volumeDefaultView.PanX, volumeDefaultView.PanY
		fov = volumeDefaultView.FOV
	}
	volumeCamAzimuth = az
	volumeCamElevation = el
	volumeCamZoom = zoom
	volumeCamPanX, volumeCamPanY = panX, panY
	volumeCamFOV = fov
	volumeCamRotating = false
	volumeCamPanning = false
	volumeCamZooming = false
	setVolumeFOVCombo(volumeCamFOV)
}

var volumeStyleComboOrder = []int{3, 0, 1, 2} // clean first: least overlay lines

func volumeStyleComboIndex(mode int) int {
	mode = clampInt(mode, 0, 3)
	for i, m := range volumeStyleComboOrder {
		if m == mode {
			return i
		}
	}
	return 0
}

func volumeStyleFromCombo(index int) int {
	if index < 0 || index >= len(volumeStyleComboOrder) {
		return 3
	}
	return volumeStyleComboOrder[index]
}

func volumeStyleName() string {
	return []string{"CIGVis", "解释", "标准", "干净"}[clampInt(volumeStyleMode, 0, 3)]
}

func updateVolumeStyleStatus() {
	if volumeFocusPanel >= 0 {
		updateVolumeStatusLine("2D Full View | 风格: " + volumeStyleName() + " | 双击或 Esc 返回 3D")
		return
	}
	updateVolumeStatusLine("3D View | 风格: " + volumeStyleName())
}

func volumeInteractionName() string {
	return []string{"CIGVis", "经典"}[clampInt(volumeInteractionMode, 0, 1)]
}

func applyVolumeStyleMode() {
	// Keep the legacy boolean only as a compatibility switch for compact labels.
	volumeMinimal = volumeStyleMode != 2
	volumeHoverAxis = -1
	volumeCrossValid = false
	destroyVolumeBase()
	invalidateVolumeBase()
}

func volumeCameraFrame() (right, up, towardCamera volumeVec3) {
	a := volumeCamAzimuth * math.Pi / 180.0
	e := volumeCamElevation * math.Pi / 180.0
	// Time is positive downward. The camera is placed above the cube for
	// positive elevation, hence the negative z component.
	towardCamera = normalizeVolumeVec3(volumeVec3{
		x: math.Cos(e) * math.Cos(a),
		y: math.Cos(e) * math.Sin(a),
		z: -math.Sin(e),
	})
	worldUp := volumeVec3{0, 0, -1}
	right = normalizeVolumeVec3(crossVolumeVec3(towardCamera, worldUp))
	if normVolumeVec3(right) < 1e-9 {
		right = volumeVec3{1, 0, 0}
	}
	up = normalizeVolumeVec3(crossVolumeVec3(right, towardCamera))
	return
}

func volumeCameraDistance() float64 {
	si, sx, st := volumeAxisScales()
	radius := .5 * math.Sqrt(si*si+sx*sx+st*st)
	if radius < .25 {
		radius = .25
	}
	if volumeCamFOV <= .01 {
		return math.Inf(1)
	}
	half := clampFloat(volumeCamFOV, 1, 120) * math.Pi / 360.0
	// Put the camera far enough that the whole cuboid remains in front while
	// keeping FOV changes visually meaningful after auto-fit.
	return radius/math.Tan(half) + radius*1.10
}

func volumeCameraCoordsNorm(il, xl, tm float64) (x, y, q float64) {
	right, up, towardCamera := volumeCameraFrame()
	si, sx, st := volumeAxisScales()
	p := volumeVec3{(il - .5) * si, (xl - .5) * sx, (tm - .5) * st}
	x = dotVolumeVec3(p, right)
	y = -dotVolumeVec3(p, up)
	q = dotVolumeVec3(p, towardCamera) // larger = closer to camera
	return
}

func volumeRawProjectNorm(il, xl, tm float64) volumePt {
	x, y, q := volumeCameraCoordsNorm(il, xl, tm)
	if volumeCamFOV > .01 {
		d := volumeCameraDistance()
		den := d - q
		if den < 1e-6 {
			den = 1e-6
		}
		k := d / den
		x *= k
		y *= k
	}
	return volumePt{x, y}
}

func volumeProjectionFit() (rawCenter volumePt, screenCenter volumePt, scale float64) {
	r := volumeCubeRect()
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, il := range []float64{0, 1} {
		for _, xl := range []float64{0, 1} {
			for _, tm := range []float64{0, 1} {
				p := volumeRawProjectNorm(il, xl, tm)
				minX = math.Min(minX, p.x)
				maxX = math.Max(maxX, p.x)
				minY = math.Min(minY, p.y)
				maxY = math.Max(maxY, p.y)
			}
		}
	}
	rawCenter = volumePt{(minX + maxX) / 2, (minY + maxY) / 2}
	// v1.6.8 composition bias: place the seismic volume slightly below the
	// geometric center. IL/XL rulers live above the cube, while the old layout
	// left excessive blank space below it. The bias is screen-space so it remains
	// visually stable for orthographic and perspective cameras alike.
	verticalBias := float64(r.Bottom-r.Top) * .028
	if !volumeShowCoordinates {
		verticalBias = float64(r.Bottom-r.Top) * .018
	}
	screenCenter = volumePt{float64(r.Left+r.Right)/2 + volumeCamPanX, float64(r.Top+r.Bottom)/2 + volumeCamPanY + verticalBias}
	rw := math.Max(maxX-minX, 1e-6)
	rh := math.Max(maxY-minY, 1e-6)
	fit := volumeViewFitFraction()
	availW := float64(r.Right-r.Left) * fit
	availH := float64(r.Bottom-r.Top) * fit
	scale = math.Min(availW/rw, availH/rh) * volumeCamZoom
	return
}

func volumeProjectNorm(il, xl, tm float64) volumePt {
	raw := volumeRawProjectNorm(il, xl, tm)
	rc, sc, scale := volumeProjectionFit()
	return volumePt{sc.x + (raw.x-rc.x)*scale, sc.y + (raw.y-rc.y)*scale}
}

func volumeCubeBasis() (origin, vx, vi, vt volumePt) {
	origin = volumeProjectNorm(0, 0, 0)
	px := volumeProjectNorm(0, 1, 0)
	pi := volumeProjectNorm(1, 0, 0)
	pt := volumeProjectNorm(0, 0, 1)
	vx = volumePt{px.x - origin.x, px.y - origin.y}
	vi = volumePt{pi.x - origin.x, pi.y - origin.y}
	vt = volumePt{pt.x - origin.x, pt.y - origin.y}
	return
}

func volumeNorms() (fi, fx, ft float64) {
	if volumeG == nil {
		return .5, .5, .5
	}
	b := volumeG.Bounds()
	fi = (float64(volumeIL) - float64(b.InlineMin)) / math.Max(float64(b.InlineMax-b.InlineMin), 1)
	fx = (float64(volumeXL) - float64(b.CrosslineMin)) / math.Max(float64(b.CrosslineMax-b.CrosslineMin), 1)
	ft = (float64(volumeSample) - float64(volumeSampleMin)) / math.Max(float64(volumeSampleMax-volumeSampleMin), 1)
	fi = math.Max(0, math.Min(1, fi))
	fx = math.Max(0, math.Min(1, fx))
	ft = math.Max(0, math.Min(1, ft))
	return
}

func volumePlane(panel int) volumePlaneGeom {
	fi, fx, ft := volumeNorms()
	var c [4]volumePt
	switch panel {
	case 0: // Inline: u=Crossline, v=Time
		c = [4]volumePt{
			volumeProjectNorm(fi, 0, 0), volumeProjectNorm(fi, 1, 0),
			volumeProjectNorm(fi, 1, 1), volumeProjectNorm(fi, 0, 1),
		}
	case 1: // Crossline: u=Inline, v=Time
		c = [4]volumePt{
			volumeProjectNorm(0, fx, 0), volumeProjectNorm(1, fx, 0),
			volumeProjectNorm(1, fx, 1), volumeProjectNorm(0, fx, 1),
		}
	default: // Time slice: u=Crossline, v=Inline
		c = [4]volumePt{
			volumeProjectNorm(0, 0, ft), volumeProjectNorm(0, 1, ft),
			volumeProjectNorm(1, 1, ft), volumeProjectNorm(1, 0, ft),
		}
	}
	return volumePlaneGeom{
		panel:   panel,
		origin:  c[0],
		u:       volumePt{c[1].x - c[0].x, c[1].y - c[0].y},
		v:       volumePt{c[3].x - c[0].x, c[3].y - c[0].y},
		corners: c,
	}
}

func volumeTriangleBarycentric(p, a, b, c volumePt) (w0, w1, w2 float64, ok bool) {
	den := (b.y-c.y)*(a.x-c.x) + (c.x-b.x)*(a.y-c.y)
	if math.Abs(den) < 1e-9 {
		return 0, 0, 0, false
	}
	w0 = ((b.y-c.y)*(p.x-c.x) + (c.x-b.x)*(p.y-c.y)) / den
	w1 = ((c.y-a.y)*(p.x-c.x) + (a.x-c.x)*(p.y-c.y)) / den
	w2 = 1 - w0 - w1
	const eps = -0.002
	if w0 < eps || w1 < eps || w2 < eps {
		return 0, 0, 0, false
	}
	return w0, w1, w2, true
}

func volumeTriangleUV(p, a, b, c volumePt, ua, va, ub, vb, uc, vc float64) (u, v float64, ok bool) {
	w0, w1, w2, ok := volumeTriangleBarycentric(p, a, b, c)
	if !ok {
		return 0, 0, false
	}
	return w0*ua + w1*ub + w2*uc, w0*va + w1*vb + w2*vc, true
}

func volumePlaneUVAtPoint(g volumePlaneGeom, x, y float64) (u, v float64, ok bool) {
	if volumeCamFOV > .01 {
		p := volumePt{x, y}
		c := g.corners
		type uvv struct {
			idx  int
			u, v float64
		}
		try := func(a, b, cc uvv) (float64, float64, bool) {
			w0, w1, w2, hit := volumeTriangleBarycentric(p, c[a.idx], c[b.idx], c[cc.idx])
			if !hit {
				return 0, 0, false
			}
			d := volumeCameraDistance()
			zFor := func(q uvv) float64 {
				il, xl, tm := volumePlaneNormPoint(g.panel, q.u, q.v)
				_, _, cq := volumeCameraCoordsNorm(il, xl, tm)
				return math.Max(1e-6, d-cq)
			}
			za, zb, zc := zFor(a), zFor(b), zFor(cc)
			iz := w0/za + w1/zb + w2/zc
			if iz <= 1e-12 {
				return 0, 0, false
			}
			z := 1 / iz
			uu := (w0*a.u/za + w1*b.u/zb + w2*cc.u/zc) * z
			vv := (w0*a.v/za + w1*b.v/zb + w2*cc.v/zc) * z
			return uu, vv, uu >= -.015 && uu <= 1.015 && vv >= -.015 && vv <= 1.015
		}
		if u, v, ok = try(uvv{0, 0, 0}, uvv{1, 1, 0}, uvv{2, 1, 1}); ok {
			return
		}
		return try(uvv{0, 0, 0}, uvv{2, 1, 1}, uvv{3, 0, 1})
	}
	dx, dy := x-g.origin.x, y-g.origin.y
	det := g.u.x*g.v.y - g.u.y*g.v.x
	if math.Abs(det) < 1e-9 {
		return 0, 0, false
	}
	u = (dx*g.v.y - dy*g.v.x) / det
	v = (g.u.x*dy - g.u.y*dx) / det
	const eps = 0.015
	return u, v, u >= -eps && u <= 1+eps && v >= -eps && v <= 1+eps
}

func volumePlanePoint(g volumePlaneGeom, u, v float64) volumePt {
	if volumeCamFOV > .01 {
		// Bilinear interpolation is used only for interaction feedback; rasterization
		// itself uses the projected quad triangles below.
		c := g.corners
		w00, w10 := (1-u)*(1-v), u*(1-v)
		w11, w01 := u*v, (1-u)*v
		return volumePt{
			x: c[0].x*w00 + c[1].x*w10 + c[2].x*w11 + c[3].x*w01,
			y: c[0].y*w00 + c[1].y*w10 + c[2].y*w11 + c[3].y*w01,
		}
	}
	return addVolumePt(g.origin, addVolumePt(scaleVolumePt(g.u, u), scaleVolumePt(g.v, v)))
}

func volumeBlendPixel(dst []byte, j int, sb, sg, sr byte, alpha int) {
	if j < 0 || j+3 >= len(dst) {
		return
	}
	if alpha >= 255 {
		dst[j], dst[j+1], dst[j+2], dst[j+3] = sb, sg, sr, 0
		return
	}
	ia := 255 - alpha
	dst[j] = byte((int(sb)*alpha + int(dst[j])*ia) / 255)
	dst[j+1] = byte((int(sg)*alpha + int(dst[j+1])*ia) / 255)
	dst[j+2] = byte((int(sr)*alpha + int(dst[j+2])*ia) / 255)
	dst[j+3] = 0
}

func volumeDepthNorm(il, xl, tm float64) float64 {
	// True depth-test coordinate for the current turntable camera. Use the same
	// stretched geometry as projection so display aspect and Z-buffer can never
	// disagree about which slice is in front.
	_, _, towardCamera := volumeCameraFrame()
	si, sx, st := volumeAxisScales()
	p := volumeVec3{(il - .5) * si, (xl - .5) * sx, (tm - .5) * st}
	return -dotVolumeVec3(p, towardCamera)
}

func volumePlaneDepth(panel int, u, v float64) float64 {
	fi, fx, ft := volumeNorms()
	switch panel {
	case 0: // Inline plane
		return volumeDepthNorm(fi, u, v)
	case 1: // Crossline plane
		return volumeDepthNorm(u, fx, v)
	default: // Time slice
		return volumeDepthNorm(v, u, ft)
	}
}

func volumePlaneNormPoint(panel int, u, v float64) (il, xl, tm float64) {
	fi, fx, ft := volumeNorms()
	switch panel {
	case 0: // Inline: u=XL, v=T
		return fi, u, v
	case 1: // Crossline: u=IL, v=T
		return u, fx, v
	default: // Time: u=XL, v=IL
		return v, u, ft
	}
}

func volumeMixPixel(dst []byte, j int, sb, sg, sr byte, alpha int) {
	if alpha <= 0 || j < 0 || j+3 >= len(dst) {
		return
	}
	if alpha >= 255 {
		dst[j], dst[j+1], dst[j+2], dst[j+3] = sb, sg, sr, 0
		return
	}
	ia := 255 - alpha
	dst[j] = byte((int(sb)*alpha + int(dst[j])*ia) / 255)
	dst[j+1] = byte((int(sg)*alpha + int(dst[j+1])*ia) / 255)
	dst[j+2] = byte((int(sr)*alpha + int(dst[j+2])*ia) / 255)
	dst[j+3] = 0
}

func volumeWarpPlaneZPerspective(scene []byte, zbuf []float32, sw, sh int, panel int, g volumePlaneGeom, tex []byte, tw, th, alpha int) {
	if sw <= 0 || sh <= 0 || tw <= 0 || th <= 0 || len(tex) < tw*th*4 || len(zbuf) < sw*sh {
		return
	}
	r := volumeCubeRect()
	c := g.corners
	for i := range c {
		c[i].x -= float64(r.Left)
		c[i].y -= float64(r.Top)
	}
	type vtx struct {
		p       volumePt
		u, v, z float64 // z is positive camera distance d-q
	}
	d := volumeCameraDistance()
	mk := func(idx int, u, v float64) vtx {
		il, xl, tm := volumePlaneNormPoint(panel, u, v)
		_, _, q := volumeCameraCoordsNorm(il, xl, tm)
		z := math.Max(1e-6, d-q)
		return vtx{p: c[idx], u: u, v: v, z: z}
	}
	vv := [4]vtx{mk(0, 0, 0), mk(1, 1, 0), mk(2, 1, 1), mk(3, 0, 1)}
	type tri struct{ a, b, c vtx }
	tris := []tri{{vv[0], vv[1], vv[2]}, {vv[0], vv[2], vv[3]}}
	for _, t := range tris {
		minX := maxInt(0, int(math.Floor(math.Min(t.a.p.x, math.Min(t.b.p.x, t.c.p.x)))))
		maxX := minInt(sw-1, int(math.Ceil(math.Max(t.a.p.x, math.Max(t.b.p.x, t.c.p.x)))))
		minY := maxInt(0, int(math.Floor(math.Min(t.a.p.y, math.Min(t.b.p.y, t.c.p.y)))))
		maxY := minInt(sh-1, int(math.Ceil(math.Max(t.a.p.y, math.Max(t.b.p.y, t.c.p.y)))))
		for y := minY; y <= maxY; y++ {
			for x := minX; x <= maxX; x++ {
				w0, w1, w2, ok := volumeTriangleBarycentric(volumePt{float64(x) + .5, float64(y) + .5}, t.a.p, t.b.p, t.c.p)
				if !ok {
					continue
				}
				// Perspective-correct interpolation. Using 1/z here is essential:
				// without it, texture/depth interpolation is affine in screen space and
				// the Z-buffer seam drifts away from the true projected intersection line.
				iz := w0/t.a.z + w1/t.b.z + w2/t.c.z
				if iz <= 1e-12 {
					continue
				}
				z := 1.0 / iz
				u := (w0*t.a.u/t.a.z + w1*t.b.u/t.b.z + w2*t.c.u/t.c.z) * z
				v := (w0*t.a.v/t.a.z + w1*t.b.v/t.b.z + w2*t.c.v/t.c.z) * z
				if u < -0.002 || u > 1.002 || v < -0.002 || v > 1.002 {
					continue
				}
				u = clampFloat(u, 0, 1)
				v = clampFloat(v, 0, 1)
				tx := clampInt(int(math.Round(u*float64(tw-1))), 0, tw-1)
				ty := clampInt(int(math.Round(v*float64(th-1))), 0, th-1)
				si := (ty*tw + tx) * 4
				di := (y*sw + x) * 4
				zi := y*sw + x
				depth := float32(z)
				if depth >= zbuf[zi] {
					continue
				}
				zbuf[zi] = depth
				volumeMixPixel(scene, di, tex[si], tex[si+1], tex[si+2], alpha)
			}
		}
	}
}

func volumeWarpPlaneZ(scene []byte, zbuf []float32, sw, sh int, panel int, g volumePlaneGeom, tex []byte, tw, th, alpha int) {
	if sw <= 0 || sh <= 0 || tw <= 0 || th <= 0 || len(tex) < tw*th*4 || len(zbuf) < sw*sh {
		return
	}
	if volumeCamFOV > .01 {
		volumeWarpPlaneZPerspective(scene, zbuf, sw, sh, panel, g, tex, tw, th, alpha)
		return
	}
	r := volumeCubeRect()
	g.origin.x -= float64(r.Left)
	g.origin.y -= float64(r.Top)
	p0 := g.origin
	p1 := addVolumePt(g.origin, g.u)
	p2 := addVolumePt(g.origin, g.v)
	p3 := addVolumePt(p1, g.v)
	minX := int(math.Floor(math.Min(math.Min(p0.x, p1.x), math.Min(p2.x, p3.x))))
	maxX := int(math.Ceil(math.Max(math.Max(p0.x, p1.x), math.Max(p2.x, p3.x))))
	minY := int(math.Floor(math.Min(math.Min(p0.y, p1.y), math.Min(p2.y, p3.y))))
	maxY := int(math.Ceil(math.Max(math.Max(p0.y, p1.y), math.Max(p2.y, p3.y))))
	minX, minY = maxInt(minX, 0), maxInt(minY, 0)
	maxX, maxY = minInt(maxX, sw-1), minInt(maxY, sh-1)
	det := g.u.x*g.v.y - g.u.y*g.v.x
	if math.Abs(det) < 1e-9 {
		return
	}
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			dx := float64(x) + .5 - g.origin.x
			dy := float64(y) + .5 - g.origin.y
			u := (dx*g.v.y - dy*g.v.x) / det
			v := (g.u.x*dy - g.u.y*dx) / det
			if u < 0 || u > 1 || v < 0 || v > 1 {
				continue
			}
			tx := clampInt(int(math.Round(u*float64(tw-1))), 0, tw-1)
			ty := clampInt(int(math.Round(v*float64(th-1))), 0, th-1)
			si := (ty*tw + tx) * 4
			di := (y*sw + x) * 4
			zi := y*sw + x
			depth := float32(volumePlaneDepth(panel, u, v))
			if depth >= zbuf[zi] {
				continue
			}
			zbuf[zi] = depth
			volumeMixPixel(scene, di, tex[si], tex[si+1], tex[si+2], alpha)
		}
	}
}

func volumeWarpPlane(scene []byte, sw, sh int, g volumePlaneGeom, tex []byte, tw, th, alpha int) {
	if sw <= 0 || sh <= 0 || tw <= 0 || th <= 0 || len(tex) < tw*th*4 {
		return
	}
	r := volumeCubeRect()
	// Convert absolute window coordinates to the local scene rectangle.
	g.origin.x -= float64(r.Left)
	g.origin.y -= float64(r.Top)
	p0 := g.origin
	p1 := addVolumePt(g.origin, g.u)
	p2 := addVolumePt(g.origin, g.v)
	p3 := addVolumePt(p1, g.v)
	minX := int(math.Floor(math.Min(math.Min(p0.x, p1.x), math.Min(p2.x, p3.x))))
	maxX := int(math.Ceil(math.Max(math.Max(p0.x, p1.x), math.Max(p2.x, p3.x))))
	minY := int(math.Floor(math.Min(math.Min(p0.y, p1.y), math.Min(p2.y, p3.y))))
	maxY := int(math.Ceil(math.Max(math.Max(p0.y, p1.y), math.Max(p2.y, p3.y))))
	minX, minY = maxInt(minX, 0), maxInt(minY, 0)
	maxX, maxY = minInt(maxX, sw-1), minInt(maxY, sh-1)
	det := g.u.x*g.v.y - g.u.y*g.v.x
	if math.Abs(det) < 1e-9 {
		return
	}
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			dx := float64(x) + .5 - g.origin.x
			dy := float64(y) + .5 - g.origin.y
			u := (dx*g.v.y - dy*g.v.x) / det
			v := (g.u.x*dy - g.u.y*dx) / det
			if u < 0 || u > 1 || v < 0 || v > 1 {
				continue
			}
			tx := clampInt(int(math.Round(u*float64(tw-1))), 0, tw-1)
			ty := clampInt(int(math.Round(v*float64(th-1))), 0, th-1)
			si := (ty*tw + tx) * 4
			di := (y*sw + x) * 4
			volumeBlendPixel(scene, di, tex[si], tex[si+1], tex[si+2], alpha)
		}
	}
}

func volumeHull(points []volumePt) []volumePt {
	if len(points) <= 2 {
		return points
	}
	pts := append([]volumePt(nil), points...)
	sort.Slice(pts, func(i, j int) bool {
		if math.Abs(pts[i].x-pts[j].x) < 1e-9 {
			return pts[i].y < pts[j].y
		}
		return pts[i].x < pts[j].x
	})
	cross := func(o, a, b volumePt) float64 {
		return (a.x-o.x)*(b.y-o.y) - (a.y-o.y)*(b.x-o.x)
	}
	lower := make([]volumePt, 0, len(pts))
	for _, p := range pts {
		for len(lower) >= 2 && cross(lower[len(lower)-2], lower[len(lower)-1], p) <= 0 {
			lower = lower[:len(lower)-1]
		}
		lower = append(lower, p)
	}
	upper := make([]volumePt, 0, len(pts))
	for i := len(pts) - 1; i >= 0; i-- {
		p := pts[i]
		for len(upper) >= 2 && cross(upper[len(upper)-2], upper[len(upper)-1], p) <= 0 {
			upper = upper[:len(upper)-1]
		}
		upper = append(upper, p)
	}
	if len(lower) > 0 {
		lower = lower[:len(lower)-1]
	}
	if len(upper) > 0 {
		upper = upper[:len(upper)-1]
	}
	return append(lower, upper...)
}

type volumeCoordTick struct {
	f        float64
	label    string
	current  bool
	point    volumePt // exact projected 3-D tick point after ruler screen offset
	hasPoint bool
}

func lerpVolumePt(a, b volumePt, f float64) volumePt {
	return volumePt{a.x + (b.x-a.x)*f, a.y + (b.y-a.y)*f}
}

func volumeFOVLabelScale() float64 {
	// Perspective makes labels near converging edges visually crowd together.
	// Increase only the screen-space text separation; geometry/tick positions
	// remain exact projections of their 3-D locations.
	if volumeCamFOV <= 30 {
		return 1
	}
	return 1 + clampFloat((volumeCamFOV-30)/90, 0, 1)*.55
}

func drawVolumeCoordRuler(hdc uintptr, start, end, center volumePt, title string, ticks []volumeCoordTick) {
	dx, dy := end.x-start.x, end.y-start.y
	n := math.Hypot(dx, dy)
	if n < 1e-6 {
		return
	}
	ux, uy := dx/n, dy/n
	px, py := -uy, ux
	mid := lerpVolumePt(start, end, .5)
	// Labels always go to the outside of the volume. Coordinate rulers are kept
	// close to the seismic cube; only the text receives an FOV-aware separation.
	if px*(mid.x-center.x)+py*(mid.y-center.y) < 0 {
		px, py = -px, -py
	}
	pen, _, _ := pCreatePen.Call(PS_SOLID, 1, rgbRef(145, 145, 145))
	old, _, _ := pSelectObject.Call(hdc, pen)
	drawLine(hdc, int(math.Round(start.x)), int(math.Round(start.y)), int(math.Round(end.x)), int(math.Round(end.y)))
	for _, tk := range ticks {
		p0 := lerpVolumePt(start, end, clampFloat(tk.f, 0, 1))
		if tk.hasPoint {
			p0 = tk.point
		}
		length := 3.5
		if tk.current {
			length = 5
		}
		a := volumePt{p0.x - px*length, p0.y - py*length}
		b := volumePt{p0.x + px*length, p0.y + py*length}
		drawLine(hdc, int(math.Round(a.x)), int(math.Round(a.y)), int(math.Round(b.x)), int(math.Round(b.y)))
	}
	pSelectObject.Call(hdc, old)
	if pen != 0 {
		pDeleteObject.Call(pen)
	}

	sep := volumeFOVLabelScale()
	for _, tk := range ticks {
		f := clampFloat(tk.f, 0, 1)
		p0 := lerpVolumePt(start, end, f)
		if tk.hasPoint {
			p0 = tk.point
		}
		normalOff := 12.0 * sep
		if tk.current {
			normalOff = 17 * sep
		}
		// Endpoint text is pushed *away from the corner along its own ruler*.
		// This separates IL/XL endpoint values even when two rulers meet at the
		// same projected cube corner.
		tangentOff := 0.0
		if f <= .001 {
			tangentOff = -14 * sep
		} else if f >= .999 {
			tangentOff = 14 * sep
		}
		lp := volumePt{
			p0.x + px*normalOff + ux*tangentOff,
			p0.y + py*normalOff + uy*tangentOff,
		}
		drawAxisText(hdc, tk.label, int(lp.x)-44, int(lp.y)-9, int(lp.x)+44, int(lp.y)+9, DT_CENTER)
	}
	tp := volumePt{mid.x + px*(25*sep), mid.y + py*(25*sep)}
	drawAxisText(hdc, title, int(tp.x)-26, int(tp.y)-9, int(tp.x)+26, int(tp.y)+9, DT_CENTER)
}

func volumeAxisOutwardShift(start, end, center volumePt, pixels float64) volumePt {
	dx, dy := end.x-start.x, end.y-start.y
	n := math.Hypot(dx, dy)
	if n < 1e-6 {
		return volumePt{}
	}
	px, py := -dy/n, dx/n
	mid := lerpVolumePt(start, end, .5)
	if px*(mid.x-center.x)+py*(mid.y-center.y) < 0 {
		px, py = -px, -py
	}
	return volumePt{px * pixels, py * pixels}
}

func volumeOffsetAxisOutward(start, end, center volumePt, pixels float64) (volumePt, volumePt, volumePt) {
	shift := volumeAxisOutwardShift(start, end, center, pixels)
	return addVolumePt(start, shift), addVolumePt(end, shift), shift
}

func volumeDrawCoordinateAxes(hdc uintptr) {
	if volumeG == nil || !volumeShowCoordinates {
		return
	}

	// v1.6.8: independent, edge-attached, perspective-calibrated coordinate rulers.
	// IL and XL each use their own rear/top edge; T uses the screen-leftmost
	// vertical edge. This avoids the label pile-up caused by a shared triad origin.
	center := volumeProjectNorm(.5, .5, .5)
	b := volumeG.Bounds()

	// Rear TOP IL edge: choose between XL=0 and XL=1 by camera depth.
	bestXL := 0.0
	bestQ := math.Inf(1)
	for _, xl := range []float64{0, 1} {
		_, _, q := volumeCameraCoordsNorm(.5, xl, 0)
		if q < bestQ {
			bestQ, bestXL = q, xl
		}
	}
	ilStartRaw := volumeProjectNorm(0, bestXL, 0)
	ilEndRaw := volumeProjectNorm(1, bestXL, 0)
	ilStart, ilEnd, ilShift := volumeOffsetAxisOutward(ilStartRaw, ilEndRaw, center, 8)

	// Rear TOP XL edge: choose between IL=0 and IL=1 by camera depth.
	bestIL := 0.0
	bestQ = math.Inf(1)
	for _, il := range []float64{0, 1} {
		_, _, q := volumeCameraCoordsNorm(il, .5, 0)
		if q < bestQ {
			bestQ, bestIL = q, il
		}
	}
	xlStartRaw := volumeProjectNorm(bestIL, 0, 0)
	xlEndRaw := volumeProjectNorm(bestIL, 1, 0)
	xlStart, xlEnd, xlShift := volumeOffsetAxisOutward(xlStartRaw, xlEndRaw, center, 8)

	// T ruler: choose the visually leftmost vertical volume edge. A small depth
	// preference keeps the ruler from jumping to a foreground edge when two
	// candidates have nearly the same screen x.
	bestTIL, bestTXL := 0.0, 0.0
	bestX := math.Inf(1)
	bestTQ := math.Inf(1)
	for _, il := range []float64{0, 1} {
		for _, xl := range []float64{0, 1} {
			sp := volumeProjectNorm(il, xl, .5)
			_, _, q := volumeCameraCoordsNorm(il, xl, .5)
			if sp.x < bestX-3 || (math.Abs(sp.x-bestX) <= 3 && q < bestTQ) {
				bestX, bestTQ = sp.x, q
				bestTIL, bestTXL = il, xl
			}
		}
	}
	tStartRaw := volumeProjectNorm(bestTIL, bestTXL, 0)
	tEndRaw := volumeProjectNorm(bestTIL, bestTXL, 1)
	tStart, tEnd, tShift := volumeOffsetAxisOutward(tStartRaw, tEndRaw, center, 9)

	spanIL := float64(b.InlineMax - b.InlineMin)
	spanXL := float64(b.CrosslineMax - b.CrosslineMin)
	ilF, xlF := .5, .5
	if math.Abs(spanIL) > 1e-9 {
		ilF = clampFloat(float64(volumeIL-b.InlineMin)/spanIL, 0, 1)
	}
	if math.Abs(spanXL) > 1e-9 {
		xlF = clampFloat(float64(volumeXL-b.CrosslineMin)/spanXL, 0, 1)
	}
	makeTicks := func(curF float64, startLabel, curLabel, endLabel string, startP, curP, endP volumePt) []volumeCoordTick {
		t := []volumeCoordTick{
			{f: 0, label: startLabel, point: startP, hasPoint: true},
			{f: 1, label: endLabel, point: endP, hasPoint: true},
		}
		if curF > .07 && curF < .93 {
			t = append(t, volumeCoordTick{f: curF, label: curLabel, current: true, point: curP, hasPoint: true})
		}
		return t
	}

	// v1.6.8 FOV calibration: tick locations are projected from their exact 3-D
	// coordinates, rather than linearly interpolated between the two screen-space
	// endpoints. The latter is only correct for orthographic projection; with a
	// perspective FOV it visibly drifts from the seismic geometry.
	ilCurP := addVolumePt(volumeProjectNorm(ilF, bestXL, 0), ilShift)
	xlCurP := addVolumePt(volumeProjectNorm(bestIL, xlF, 0), xlShift)

	// Geometry coordinates always increase from normalized 0 -> 1; the screen
	// projection may reverse visually, but labels remain bound to true data axes.
	drawVolumeCoordRuler(hdc, ilStart, ilEnd, center, "IL", makeTicks(ilF,
		fmt.Sprintf("%d", b.InlineMin), fmt.Sprintf("%d", volumeIL), fmt.Sprintf("%d", b.InlineMax), ilStart, ilCurP, ilEnd))
	drawVolumeCoordRuler(hdc, xlStart, xlEnd, center, "XL", makeTicks(xlF,
		fmt.Sprintf("%d", b.CrosslineMin), fmt.Sprintf("%d", volumeXL), fmt.Sprintf("%d", b.CrosslineMax), xlStart, xlCurP, xlEnd))

	if volumeF != nil {
		t0 := float64(volumeSampleMin*volumeF.Info.SampleIntervalUS) / 1000.0
		t1 := float64(volumeSampleMax*volumeF.Info.SampleIntervalUS) / 1000.0
		ft := .5
		if volumeSampleMax > volumeSampleMin {
			ft = clampFloat(float64(volumeSample-volumeSampleMin)/float64(volumeSampleMax-volumeSampleMin), 0, 1)
		}
		tCur := float64(volumeSample*volumeF.Info.SampleIntervalUS) / 1000.0
		tCurP := addVolumePt(volumeProjectNorm(bestTIL, bestTXL, ft), tShift)
		drawVolumeCoordRuler(hdc, tStart, tEnd, center, "T", makeTicks(ft,
			formatAdaptiveTimeMS(t0), formatAdaptiveTimeMS(tCur), formatAdaptiveTimeMS(t1), tStart, tCurP, tEnd))
	}
}

func volumeDrawCubeWire(hdc uintptr) {
	if volumeG == nil {
		return
	}
	// Three visual presets:
	//   CIGVis: no outer slice/volume border at all; intersections carry geometry.
	//   Interpretation: very faint projected hull only.
	//   Standard: all 12 volume edges plus range hints.
	if volumeStyleMode == 1 || volumeStyleMode == 2 {
		corners := [8]volumePt{
			volumeProjectNorm(0, 0, 0), volumeProjectNorm(0, 1, 0), volumeProjectNorm(1, 0, 0), volumeProjectNorm(1, 1, 0),
			volumeProjectNorm(0, 0, 1), volumeProjectNorm(0, 1, 1), volumeProjectNorm(1, 0, 1), volumeProjectNorm(1, 1, 1),
		}
		wireColor := rgbRef(246, 247, 249)
		if volumeStyleMode == 2 {
			wireColor = rgbRef(232, 236, 242)
		}
		pen, _, _ := pCreatePen.Call(PS_SOLID, 1, wireColor)
		old, _, _ := pSelectObject.Call(hdc, pen)
		if volumeStyleMode == 1 {
			pts := make([]volumePt, 0, 8)
			for _, p := range corners {
				pts = append(pts, p)
			}
			h := volumeHull(pts)
			for i := 0; i < len(h); i++ {
				a, bb := h[i], h[(i+1)%len(h)]
				drawLine(hdc, int(math.Round(a.x)), int(math.Round(a.y)), int(math.Round(bb.x)), int(math.Round(bb.y)))
			}
		} else {
			edges := [][2]int{{0, 1}, {0, 2}, {1, 3}, {2, 3}, {4, 5}, {4, 6}, {5, 7}, {6, 7}, {0, 4}, {1, 5}, {2, 6}, {3, 7}}
			for _, e := range edges {
				a, bb := corners[e[0]], corners[e[1]]
				drawLine(hdc, int(math.Round(a.x)), int(math.Round(a.y)), int(math.Round(bb.x)), int(math.Round(bb.y)))
			}
		}
		pSelectObject.Call(hdc, old)
		if pen != 0 {
			pDeleteObject.Call(pen)
		}
	}
	if !volumeShowCoordinates {
		return
	}
	volumeDrawCoordinateAxes(hdc)
	r := volumeCubeRect()

	// Compact orientation widget is always retained because camera rotation makes
	// IL/XL direction ambiguous. Minimal mode simply renders it smaller/lighter.
	_, clientH := clientSize(volumeHwnd)
	baseY := math.Min(float64(r.Bottom)+28, float64(clientH)-34)
	base := volumePt{float64(r.Left) + 28, baseY}
	_, vx, vi, vt := volumeCubeBasis()
	normalize := func(v volumePt, length float64) volumePt {
		n := math.Hypot(v.x, v.y)
		if n < 1e-9 {
			return volumePt{}
		}
		return volumePt{v.x / n * length, v.y / n * length}
	}
	drawArrow := func(v volumePt, label string) {
		axisLen := 24.0
		axisColor := rgbRef(118, 118, 118)
		if volumeStyleMode != 2 {
			axisLen = 20
			axisColor = rgbRef(150, 150, 150)
		}
		v = normalize(v, axisLen)
		end := addVolumePt(base, v)
		apen, _, _ := pCreatePen.Call(PS_SOLID, 1, axisColor)
		aold, _, _ := pSelectObject.Call(hdc, apen)
		drawLine(hdc, int(base.x), int(base.y), int(end.x), int(end.y))
		// Arrow head.
		perp := normalize(volumePt{-v.y, v.x}, 4)
		back := normalize(volumePt{-v.x, -v.y}, 6)
		h1 := addVolumePt(end, addVolumePt(back, perp))
		h2 := addVolumePt(end, addVolumePt(back, scaleVolumePt(perp, -1)))
		drawLine(hdc, int(end.x), int(end.y), int(h1.x), int(h1.y))
		drawLine(hdc, int(end.x), int(end.y), int(h2.x), int(h2.y))
		pSelectObject.Call(hdc, aold)
		if apen != 0 {
			pDeleteObject.Call(apen)
		}
		drawAxisText(hdc, label, int(end.x)-8, int(end.y)-16, int(end.x)+24, int(end.y)+4, DT_CENTER)
	}
	drawArrow(vi, "IL")
	drawArrow(vx, "XL")
	drawArrow(vt, "T")

}

func drawVolumeLabelBox(hdc uintptr, text string, left, top, right, bottom int, flags uint32) {
	r := RECT{Left: int32(left), Top: int32(top), Right: int32(right), Bottom: int32(bottom)}
	white, _, _ := pGetStockObject.Call(WHITE_BRUSH)
	pFillRect.Call(hdc, uintptr(unsafe.Pointer(&r)), white)
	pen, _, _ := pCreatePen.Call(PS_SOLID, 1, rgbRef(170, 170, 170))
	old, _, _ := pSelectObject.Call(hdc, pen)
	drawLine(hdc, left, top, right-1, top)
	drawLine(hdc, right-1, top, right-1, bottom-1)
	drawLine(hdc, right-1, bottom-1, left, bottom-1)
	drawLine(hdc, left, bottom-1, left, top)
	pSelectObject.Call(hdc, old)
	if pen != 0 {
		pDeleteObject.Call(pen)
	}
	drawAxisText(hdc, text, left+5, top+2, right-5, bottom-2, flags|DT_VCENTER)
}

func volumeDrawPlaneOutlines(hdc uintptr) {
	if volumeG == nil {
		return
	}

	// CIGVis and Clean presets intentionally have NO ordinary slice rectangle border.
	// Interpretation shows only interactive/current borders; Standard shows all.
	if volumeStyleMode == 1 || volumeStyleMode == 2 {
		for panel := 0; panel < 3; panel++ {
			if volumeStyleMode == 1 && panel != volumeActiveAxis && panel != volumeHoverAxis && panel != volumeDragAxis {
				continue
			}
			col := rgbRef(224, 230, 238)
			width := uintptr(1)
			if panel == volumeActiveAxis {
				col = rgbRef(140, 170, 220)
				if volumeStyleMode == 2 {
					width = 2
				}
			}
			if panel == volumeHoverAxis {
				col = rgbRef(236, 185, 42)
				width = 2
			}
			if panel == volumeDragAxis {
				col = rgbRef(239, 146, 38)
				if volumeStyleMode == 2 {
					width = 3
				} else {
					width = 2
				}
			}
			pen, _, _ := pCreatePen.Call(PS_SOLID, width, col)
			old, _, _ := pSelectObject.Call(hdc, pen)
			g := volumePlane(panel)
			p0, p1, p2, p3 := g.corners[0], g.corners[1], g.corners[2], g.corners[3]
			pts := []volumePt{p0, p1, p2, p3, p0}
			for i := 0; i < 4; i++ {
				drawLine(hdc, int(pts[i].x), int(pts[i].y), int(pts[i+1].x), int(pts[i+1].y))
			}
			pSelectObject.Call(hdc, old)
			if pen != 0 {
				pDeleteObject.Call(pen)
			}
		}
	}

	// Clean preset means seismic texture only: no outer box, no slice borders,
	// and no three-way intersection lines. Coordinates remain separately toggleable.
	if volumeStyleMode == 3 {
		return
	}

	// Geometry is communicated primarily by the three true slice intersections.
	// In the strict preset the idle line is neutral white/gray, matching CIGVis;
	// only the two lines belonging to the hovered/dragged slice are emphasized.
	fi, fx, ft := volumeNorms()
	type interSeg struct {
		a, b   volumePt
		p0, p1 int
	}
	segs := []interSeg{
		{volumeProjectNorm(fi, fx, 0), volumeProjectNorm(fi, fx, 1), 0, 1}, // IL x XL
		{volumeProjectNorm(fi, 0, ft), volumeProjectNorm(fi, 1, ft), 0, 2}, // IL x T
		{volumeProjectNorm(0, fx, ft), volumeProjectNorm(1, fx, ft), 1, 2}, // XL x T
	}
	for _, seg := range segs {
		width := uintptr(1)
		color := rgbRef(242, 242, 242)
		if volumeStyleMode == 1 {
			color = rgbRef(246, 231, 192)
		}
		if volumeStyleMode == 2 {
			color = rgbRef(243, 222, 166)
		}
		selected := volumeHoverAxis
		if volumeDragAxis >= 0 {
			selected = volumeDragAxis
		}
		if selected >= 0 && (seg.p0 == selected || seg.p1 == selected) {
			width = 2
			color = rgbRef(236, 185, 42)
			if volumeDragAxis >= 0 {
				color = rgbRef(239, 146, 38)
			}
		}
		pen, _, _ := pCreatePen.Call(PS_SOLID, width, color)
		old, _, _ := pSelectObject.Call(hdc, pen)
		drawLine(hdc, int(seg.a.x), int(seg.a.y), int(seg.b.x), int(seg.b.y))
		pSelectObject.Call(hdc, old)
		if pen != 0 {
			pDeleteObject.Call(pen)
		}
	}
}

func drawVolumeCubeStatic(hdc uintptr) {
	r := volumeCubeRect()
	w, h := rectWH(r)
	if w < 10 || h < 10 {
		return
	}
	scene := make([]byte, w*h*4)
	zbuf := make([]float32, w*h)
	for i := 0; i < len(scene); i += 4 {
		scene[i], scene[i+1], scene[i+2], scene[i+3] = 252, 252, 252, 0
	}
	for i := range zbuf {
		zbuf[i] = 1e9
	}
	// Final opacity hierarchy: vertical sections are effectively opaque; the
	// horizontal Time Slice remains around 0.88; its texture contrast is also reduced by a 1.15x auto clim.
	alphas := [3]int{250, 250, 224} // IL/XL ~0.98, Time ~0.88
	if volumeActiveAxis < 2 {
		alphas[volumeActiveAxis] = 255
	} else {
		alphas[2] = 228 // active Time still intentionally below 0.90
	}
	if volumeHoverAxis >= 0 {
		if volumeHoverAxis == 2 {
			alphas[2] = 230
		} else {
			alphas[volumeHoverAxis] = 253
		}
	}
	if volumeDragAxis >= 0 {
		if volumeDragAxis == 2 {
			alphas[2] = 232
		} else {
			alphas[volumeDragAxis] = 255
		}
	}
	// A mostly-opaque active slice plus slightly lighter inactive slices gives a
	// cleaner CIGVis-like separation while still relying on the Z-buffer for true
	// front/back visibility.
	volumeWarpPlaneZ(scene, zbuf, w, h, 0, volumePlane(0), volumeInline.bgra, volumeInline.w, volumeInline.h, alphas[0])
	volumeWarpPlaneZ(scene, zbuf, w, h, 1, volumePlane(1), volumeCrossline.bgra, volumeCrossline.w, volumeCrossline.h, alphas[1])
	volumeWarpPlaneZ(scene, zbuf, w, h, 2, volumePlane(2), volumeTime.bgra, volumeTime.w, volumeTime.h, alphas[2])
	bmi := BITMAPINFO{Header: BITMAPINFOHEADER{Size: uint32(unsafe.Sizeof(BITMAPINFOHEADER{})), Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32, Compression: BI_RGB, SizeImage: uint32(len(scene))}}
	pSetStretchBltMode.Call(hdc, HALFTONE)
	pStretchDIBits.Call(hdc, uintptr(r.Left), uintptr(r.Top), uintptr(w), uintptr(h), 0, 0, uintptr(w), uintptr(h), uintptr(unsafe.Pointer(&scene[0])), uintptr(unsafe.Pointer(&bmi)), DIB_RGB_COLORS, SRCCOPY)
	volumeDrawCubeWire(hdc)
	volumeDrawPlaneOutlines(hdc)
}

func volumePalette() [256]rgb {
	idx := volumePaletteIndex
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

func volumePaletteBGRA(idx []byte) []byte {
	pal := volumePalette()
	out := make([]byte, len(idx)*4)
	for i, v := range idx {
		c := pal[int(v)]
		j := i * 4
		out[j], out[j+1], out[j+2], out[j+3] = c.b, c.g, c.r, 0
	}
	return out
}

func volumeRasterBGRA(v []float32, mask []bool, lo, hi float64) []byte {
	pal := volumePalette()
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

func setVolumeBusy(delta int32) {
	if delta > 0 {
		atomic.AddInt32(&volumeBusy, delta)
	} else if delta < 0 {
		for {
			v := atomic.LoadInt32(&volumeBusy)
			if v <= 0 {
				volumeCamRotating = false
				atomic.StoreInt32(&volumeBusy, 0)
				break
			}
			if atomic.CompareAndSwapInt32(&volumeBusy, v, v+delta) {
				break
			}
		}
	}
	if volumeHwnd == 0 {
		return
	}
	id := uintptr(IDC_ARROW)
	if atomic.LoadInt32(&volumeBusy) > 0 {
		id = IDC_WAIT
	}
	c, _, _ := pLoadCursorW.Call(0, id)
	if c != 0 {
		pSetCursor.Call(c)
	}
}

func startVolumePrepare() {
	startVolumePrepareForSide(volumePath, volumeActiveSide)
}

func startVolumePrepareForSide(path string, side int) {
	if volumeHwnd == 0 || path == "" || side < 0 || side > 1 {
		return
	}
	gen := atomic.AddInt64(&volumeGen, 1)
	setVolumeBusy(1)
	showVolumeIndexProgress(gen, 0)
	phase1TraceVolumeIndexStart(path)
	if side == 0 {
		setText(vc.status, "自动识别 Inline/Crossline 字节位置，并建立三维几何索引...")
	} else {
		setText(vc.status, "正在加载 B 并建立三维几何索引...")
	}
	// Capture the left reference before the worker starts. B is initialized at
	// the nearest matching IL/XL/time so side-by-side comparison is immediately useful.
	refIL, refXL, refTimeMS := int32(0), int32(0), -1.0
	if side == 1 && volumeScenes[0].valid {
		left := volumeScenes[0]
		if volumeActiveSide == 0 && volumeF != nil {
			left = captureVolumeSceneState()
		}
		refIL, refXL = left.il, left.xl
		if left.f != nil {
			refTimeMS = float64(left.sample*left.f.Info.SampleIntervalUS) / 1000.0
		}
	}
	go func(path string, side int, gnum int64, targetIL, targetXL int32, targetTimeMS float64) {
		r := &volumePrepareResult{gen: gnum, side: side, path: path}
		f, err := segy.Open(path)
		if err != nil {
			r.err = err
			postVolumeReady(r)
			return
		}
		postVolumeIndexProgress(gnum, 5)
		det, err := f.DetectGeometryBytes(3000)
		if err != nil {
			f.Close()
			r.err = fmt.Errorf("自动识别 IL/XL 失败: %w", err)
			postVolumeReady(r)
			return
		}
		postVolumeIndexProgress(gnum, 10)
		idx, st, err := f.BuildGeometryIndexCachedProgress(det.InlineByte, det.CrosslineByte, 0, func(done, total int) {
			if total <= 0 {
				return
			}
			postVolumeIndexProgress(gnum, 10+80*done/total)
		})
		if err != nil {
			f.Close()
			r.err = err
			postVolumeReady(r)
			return
		}
		postVolumeIndexProgress(gnum, 90)
		if !idx.Poststack {
			f.Close()
			r.err = fmt.Errorf("“体”三视图目前用于叠后三维：检测到同一 IL/XL bin 有较多重复道")
			postVolumeReady(r)
			return
		}
		cache := segy.NewTimeSliceCache(f, idx)
		il := idx.InlineValues[len(idx.InlineValues)/2]
		xl := idx.CrosslineValues[len(idx.CrosslineValues)/2]
		if side == 1 {
			if i := nearestVolumeIndex(idx.InlineValues, targetIL); i >= 0 {
				il = idx.InlineValues[i]
			}
			if i := nearestVolumeIndex(idx.CrosslineValues, targetXL); i >= 0 {
				xl = idx.CrosslineValues[i]
			}
		}
		smin, smax := 0, f.Info.SamplesPerTrace-1
		if originValid {
			smin, smax = originSampleStart, originSampleEnd
			if smin < 0 {
				smin = 0
			}
			if smax < 0 || smax >= f.Info.SamplesPerTrace {
				smax = f.Info.SamplesPerTrace - 1
			}
		}
		sample := smin + (smax-smin)/2
		if side == 1 && targetTimeMS >= 0 && f.Info.SampleIntervalUS > 0 {
			sample = int(math.Round(targetTimeMS * 1000.0 / float64(f.Info.SampleIntervalUS)))
			sample = clampInt(sample, smin, smax)
		}
		vals, _, err := cache.GetSlice(sample)
		if err != nil {
			f.Close()
			r.err = err
			postVolumeReady(r)
			return
		}
		postVolumeIndexProgress(gnum, 100)
		r.f, r.g, r.cache = f, idx, cache
		r.detect, r.stats, r.vals = det, st, append([]float32(nil), vals...)
		r.il, r.xl, r.sample = il, xl, sample
		postVolumeReady(r)
	}(path, side, gen, refIL, refXL, refTimeMS)
}

func postVolumeReady(r *volumePrepareResult) {
	if volumeHwnd == 0 {
		if r != nil && r.f != nil {
			r.f.Close()
		}
		return
	}
	volumePendingMu.Lock()
	if volumePending != nil && volumePending.f != nil {
		volumePending.f.Close()
	}
	volumePending = r
	volumePendingMu.Unlock()
	pPostMessageW.Call(volumeHwnd, WM_VOLUME_READY, 0, 0)
}

func handleVolumeReady() {
	defer setVolumeBusy(-1)
	volumePendingMu.Lock()
	r := volumePending
	volumePending = nil
	volumePendingMu.Unlock()
	if r != nil {
		defer hideVolumeIndexProgress(r.gen)
	}
	if r == nil || r.gen != atomic.LoadInt64(&volumeGen) || !phase1VolumeResultCurrent() {
		if r != nil && r.f != nil {
			r.f.Close()
		}
		if r != nil {
			phase1TraceVolumeDiscarded()
		}
		return
	}
	if r.err != nil {
		phase1TraceVolumeError(r.err)
		setText(vc.status, r.err.Error())
		message(volumeHwnd, "三视图联动", r.err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	phase1TraceVolumeIndexReady(r.path, r.stats)
	if r.side == 0 {
		phase1AttachVolumeGeometry(r.path, r.g)
	}

	// Preserve the currently active side before temporarily installing the
	// newly prepared dataset into the legacy renderer globals.
	if volumeF != nil && volumeScenes[volumeActiveSide].valid {
		saveActiveVolumeScene()
	}
	previousSide := volumeActiveSide
	previousState := volumeSceneState{}
	previousValid := false
	if previousSide >= 0 && previousSide < 2 && volumeScenes[previousSide].valid {
		previousState = volumeScenes[previousSide]
		previousValid = true
	}

	// Replacing an already loaded side must release its independent file handle.
	if volumeScenes[r.side].valid && volumeScenes[r.side].f != nil && volumeScenes[r.side].f != r.f {
		volumeScenes[r.side].f.Close()
	}

	volumePath = r.path
	volumeF, volumeG, volumeC = r.f, r.g, r.cache
	volumeIL, volumeXL, volumeSample = r.il, r.xl, r.sample
	volumeSampleMin, volumeSampleMax = 0, volumeF.Info.SamplesPerTrace-1
	if originValid {
		volumeSampleMin, volumeSampleMax = originSampleStart, originSampleEnd
		if volumeSampleMin < 0 {
			volumeSampleMin = 0
		}
		if volumeSampleMax < 0 || volumeSampleMax >= volumeF.Info.SamplesPerTrace {
			volumeSampleMax = volumeF.Info.SamplesPerTrace - 1
		}
	}
	volumeTimeVals = append([]float32(nil), r.vals...)
	volumeTimeRaster, volumeTimeMask = nil, nil
	volumeInlineValues, volumeCrosslineValues = nil, nil
	volumeTimeMin, volumeTimeMax = -1, 1
	volumeInline, volumeCrossline, volumeTime = comparePanel{}, comparePanel{}, comparePanel{}
	volumeCrossValid = true
	volumeCrossIL, volumeCrossXL, volumeCrossSample = float64(volumeIL), float64(volumeXL), float64(volumeSample)
	volumeActiveAxis, volumeHoverAxis = 2, -1
	// A newly opened A-side volume is guarded once more at the async completion
	// boundary. Re-running geometry detection later does not unexpectedly change
	// a user's manually selected tiled view.
	if r.side == 0 && volumeForce3DOnReady {
		forceVolume3DViewState()
		volumeForce3DOnReady = false
	}
	if r.side == 0 {
		home3DTracef("volume ready: IL/XL bytes=%d/%d grid=%dx%d volume3D=%v focus=%d", r.detect.InlineByte, r.detect.CrosslineByte, len(r.g.InlineValues), len(r.g.CrosslineValues), volume3D, volumeFocusPanel)
	}

	if r.side == 1 && volumeScenes[0].valid {
		// Start B with the same camera/shape as A. From this point onward both sides
		// are independent until the user presses “与左图一致” again.
		left := volumeScenes[0]
		volumeCamAzimuth, volumeCamElevation = left.camAzimuth, left.camElevation
		volumeCamZoom, volumeCamFOV = left.camZoom, left.camFOV
		volumeCamPanX, volumeCamPanY = left.camPanX, left.camPanY
		volumeAxisILFactor, volumeAxisXLFactor, volumeAxisZFactor = left.axisILFactor, left.axisXLFactor, left.axisZFactor
		volumeAspectMode = left.aspectMode
	}
	renderVolumeInline()
	renderVolumeCrossline()
	updateVolumeTimePanel()
	volumeScenes[r.side] = captureVolumeSceneState()
	volumeScenes[r.side].valid = true

	cacheTag := "full-scan"
	if r.stats.FromCache {
		cacheTag = "cache"
	} else if r.stats.FastRegular {
		cacheTag = fmt.Sprintf("fast-grid/%d hdr", r.stats.HeaderReads)
	}

	if r.side == 0 {
		volumeActiveSide = 0
		applyVolumeSceneState(volumeScenes[0])
		aspectTag := "均衡 1:1"
		if volumeAspectMode == 0 && volumeG != nil {
			b := volumeG.Bounds()
			spanIL := math.Max(math.Abs(float64(b.InlineMax-b.InlineMin)), 1)
			spanXL := math.Max(math.Abs(float64(b.CrosslineMax-b.CrosslineMin)), 1)
			aspectTag = fmt.Sprintf("实际 IL:XL %.2f:1", spanIL/spanXL)
		} else if volumeAspectMode == 2 {
			aspectTag = "默认比例"
		}
		updateVolumeControls()
		updateVolumeStatusLine(fmt.Sprintf("3D View | 自动 IL/XL=%d/%d | confidence %.2f | geometry %s | %s", r.detect.InlineByte, r.detect.CrosslineByte, r.detect.Confidence, cacheTag, aspectTag))
	} else {
		volumeCompareMode = true
		volumeActiveSide = 1
		applyVolumeSceneState(volumeScenes[1])
		if vc.compare != 0 {
			setText(vc.compare, "单")
		}
		if vc.matchLeft != 0 {
			pShowWindow.Call(vc.matchLeft, SW_SHOW)
		}
		if vc.diff != 0 {
			pShowWindow.Call(vc.diff, SW_SHOW)
		}
		volumeResidualKey = ""
		syncVolumeSceneControls()
		updateVolumeStatusLine(fmt.Sprintf("B 已加载 | geometry %s | 左右 3D 可独立变换；点击“与左图一致”可快速同步", cacheTag))
	}
	if r.side == 1 && previousValid && previousSide == 0 {
		// Leave B active intentionally: the user just loaded it and can immediately
		// inspect/adjust the right view. Left remains parked unchanged.
		_ = previousState
	}
	destroyVolumeBase()
	invalidateVolumeBase()
}

func startVolumeCompare() {
	if volumeCompareMode {
		saveActiveVolumeScene()
		volumeCompareMode = false
		if volumeScenes[0].valid {
			volumeActiveSide = 0
			applyVolumeSceneState(volumeScenes[0])
		}
		if vc.compare != 0 {
			setText(vc.compare, "比")
		}
		if vc.matchLeft != 0 {
			pShowWindow.Call(vc.matchLeft, SW_HIDE)
		}
		if vc.diff != 0 {
			pShowWindow.Call(vc.diff, SW_HIDE)
			pSendMessageW.Call(vc.diff, BM_SETCHECK, 0, 0)
		}
		volumeShowDiff = false
		syncVolumeSceneControls()
		updateVolumeStatusLine("单体 3D View")
		destroyVolumeBase()
		invalidateVolumeBase()
		return
	}
	if volumeScenes[1].valid {
		saveActiveVolumeScene()
		volumeCompareMode = true
		volumeActiveSide = 1
		applyVolumeSceneState(volumeScenes[1])
		setText(vc.compare, "单")
		pShowWindow.Call(vc.matchLeft, SW_SHOW)
		pShowWindow.Call(vc.diff, SW_SHOW)
		syncVolumeSceneControls()
		updateVolumeStatusLine("A/B 3D 对比 | 左右视图独立")
		destroyVolumeBase()
		invalidateVolumeBase()
		return
	}
	path := ""
	// If B was already loaded in the existing 2-D compare workspace, reuse it;
	// otherwise ask for a SEG-Y file exactly like the original “比” workflow.
	if compareBPath != "" && compareBPath != volumeScenes[0].path {
		path = compareBPath
	} else {
		path = openDataDialog(volumeHwnd)
	}
	if path == "" {
		return
	}
	startVolumePrepareForSide(path, 1)
}

func matchVolumeRightToLeft() {
	if !volumeScenes[0].valid || !volumeScenes[1].valid {
		return
	}
	saveActiveVolumeScene()
	left := volumeScenes[0]
	right := volumeScenes[1]

	// Camera / view transform.
	right.camAzimuth, right.camElevation = left.camAzimuth, left.camElevation
	right.camZoom, right.camFOV = left.camZoom, left.camFOV
	right.camPanX, right.camPanY = left.camPanX, left.camPanY
	right.axisILFactor, right.axisXLFactor, right.axisZFactor = left.axisILFactor, left.axisXLFactor, left.axisZFactor
	right.aspectMode = left.aspectMode

	// Position: use actual IL/XL values and physical time, then map to the nearest
	// valid bin/sample in B. This works even when A/B geometry or dt differ.
	if right.g != nil {
		if i := nearestVolumeIndex(right.g.InlineValues, left.il); i >= 0 {
			right.il = right.g.InlineValues[i]
		}
		if i := nearestVolumeIndex(right.g.CrosslineValues, left.xl); i >= 0 {
			right.xl = right.g.CrosslineValues[i]
		}
	}
	if left.f != nil && right.f != nil && right.f.Info.SampleIntervalUS > 0 {
		timeUS := float64(left.sample * left.f.Info.SampleIntervalUS)
		right.sample = clampInt(int(math.Round(timeUS/float64(right.f.Info.SampleIntervalUS))), right.sampleMin, right.sampleMax)
	}
	volumeScenes[1] = right
	volumeActiveSide = 1
	applyVolumeSceneState(right)
	volumeActiveAxis, volumeHoverAxis = left.activeAxis, -1
	// Re-render B at the newly matched orthoslice positions.
	renderVolumeInline()
	renderVolumeCrossline()
	loadVolumeTimeSlice(volumeSample)
	volumeCrossIL, volumeCrossXL, volumeCrossSample = float64(volumeIL), float64(volumeXL), float64(volumeSample)
	volumeCrossValid = true
	volumeScenes[1] = captureVolumeSceneState()
	syncVolumeSceneControls()
	updateVolumeStatusLine("B 已与左图同步：切片位置 + Camera + FOV + X/Y/Z 比例 + Zoom/Pan")
	destroyVolumeBase()
	invalidateVolumeBase()
}

func nearestVolumeIndex(vals []int32, target int32) int {
	if len(vals) == 0 {
		return -1
	}
	best := 0
	bd := math.Abs(float64(vals[0] - target))
	for i := 1; i < len(vals); i++ {
		d := math.Abs(float64(vals[i] - target))
		if d < bd {
			best, bd = i, d
		}
	}
	return best
}

const volumeNormalizationSampleLimit = 300000

// volumeSharedSliceRange computes one display range for the three currently
// visible orthogonal slices. Zero is a valid seismic amplitude and remains in
// the distribution; only invalid Time Slice raster cells and non-finite values
// are omitted. A single range prevents one plane's local endpoint from turning
// the same palette almost entirely black while its neighbours remain normal.
func volumeSharedSliceRange(inlineValues, crosslineValues []float64, timeValues []float32, timeMask []bool,
	gain, clip float64, manual bool, manualMin, manualMax float64) (float64, float64) {
	if manual && manualMax > manualMin {
		return manualMin, manualMax
	}
	if gain < 0 {
		gain = 0
	}
	if gain > 49 {
		gain = 49
	}
	if clip <= 0 {
		clip = 99
	}
	if clip > 100 {
		clip = 100
	}
	tailPercent := gain + (100-clip)/2
	if tailPercent > 49 {
		tailPercent = 49
	}
	if tailPercent == 0 {
		lo, hi := math.Inf(1), math.Inf(-1)
		add := func(value float64) {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return
			}
			if value < lo {
				lo = value
			}
			if value > hi {
				hi = value
			}
		}
		for _, value := range inlineValues {
			add(value)
		}
		for _, value := range crosslineValues {
			add(value)
		}
		for index, value := range timeValues {
			if index < len(timeMask) && timeMask[index] {
				add(float64(value))
			}
		}
		return normalizeVolumeDisplayRange(lo, hi)
	}

	total := len(inlineValues) + len(crosslineValues) + len(timeValues)
	step := 1
	if total > volumeNormalizationSampleLimit {
		step = (total + volumeNormalizationSampleLimit - 1) / volumeNormalizationSampleLimit
	}
	values := make([]float64, 0, minInt(total/step+3, volumeNormalizationSampleLimit+3))
	position := 0
	add := func(value float64, valid bool) {
		selected := position%step == 0
		position++
		if !selected || !valid || math.IsNaN(value) || math.IsInf(value, 0) {
			return
		}
		values = append(values, value)
	}
	for _, value := range inlineValues {
		add(value, true)
	}
	for _, value := range crosslineValues {
		add(value, true)
	}
	for index, value := range timeValues {
		add(float64(value), index < len(timeMask) && timeMask[index])
	}
	if len(values) == 0 {
		return -1, 1
	}
	sort.Float64s(values)
	lowIndex := int(float64(len(values)) * tailPercent / 100)
	highIndex := int(float64(len(values)) * (100 - tailPercent) / 100)
	lowIndex = clampInt(lowIndex, 0, len(values)-1)
	highIndex = clampInt(highIndex, 0, len(values)-1)
	return normalizeVolumeDisplayRange(values[lowIndex], values[highIndex])
}

func normalizeVolumeDisplayRange(lo, hi float64) (float64, float64) {
	if math.IsInf(lo, 1) || math.IsInf(hi, -1) || math.IsNaN(lo) || math.IsNaN(hi) {
		return -1, 1
	}
	if hi < lo {
		lo, hi = hi, lo
	}
	if hi <= lo {
		half := math.Max(math.Abs(lo)*0.01, 1e-6)
		return lo - half, hi + half
	}
	return lo, hi
}

func remapVolumeSharedNormalization() {
	lo, hi := volumeSharedSliceRange(volumeInlineValues, volumeCrosslineValues, volumeTimeRaster, volumeTimeMask,
		gainPercent, clipPercent, useLimits, limitMin, limitMax)
	volumeTimeMin, volumeTimeMax = lo, hi
	if len(volumeInlineValues) == volumeInline.w*volumeInline.h && len(volumeInlineValues) > 0 {
		volumeInline.indices = segy.MapAmplitudeValues(volumeInlineValues, lo, hi)
		volumeInline.bgra = volumePaletteBGRA(volumeInline.indices)
		volumeInline.stats.MapMin, volumeInline.stats.MapMax = lo, hi
	}
	if len(volumeCrosslineValues) == volumeCrossline.w*volumeCrossline.h && len(volumeCrosslineValues) > 0 {
		volumeCrossline.indices = segy.MapAmplitudeValues(volumeCrosslineValues, lo, hi)
		volumeCrossline.bgra = volumePaletteBGRA(volumeCrossline.indices)
		volumeCrossline.stats.MapMin, volumeCrossline.stats.MapMax = lo, hi
	}
	if len(volumeTimeRaster) == volumeTime.w*volumeTime.h && len(volumeTimeRaster) > 0 {
		volumeTime.bgra = volumeRasterBGRA(volumeTimeRaster, volumeTimeMask, lo, hi)
		volumeTime.stats.MapMin, volumeTime.stats.MapMax = lo, hi
	}
}

func renderVolumeInline() {
	if volumeF == nil || volumeG == nil {
		return
	}
	r := volumePanelRects()[0]
	w, h := rectWH(r)
	w = minInt(maxInt(w, 2), 900)
	h = minInt(maxInt(h, 2), 900)
	tr, _, actual, err := volumeG.LineTraceNumbers(0, volumeIL)
	if err != nil {
		setText(vc.status, err.Error())
		return
	}
	opts := segy.RenderOptions{Width: w, Height: h, AGC: agc, ClipPercent: clipPercent, GainPercent: gainPercent, UseValueLimits: useLimits, MinValue: limitMin, MaxValue: limitMax, SampleStart: volumeSampleMin, SampleEnd: volumeSampleMax, DisplayMode: renderDisplayMode}
	values, st, err := volumeF.RenderTraceIndicesValues(tr, opts)
	if err != nil {
		setText(vc.status, err.Error())
		return
	}
	volumeIL = actual
	volumeInlineValues = values
	volumeInline = comparePanel{w: w, h: h, stats: st, title: fmt.Sprintf("Inline %d", actual)}
	remapVolumeSharedNormalization()
}

func renderVolumeCrossline() {
	if volumeF == nil || volumeG == nil {
		return
	}
	r := volumePanelRects()[1]
	w, h := rectWH(r)
	w = minInt(maxInt(w, 2), 900)
	h = minInt(maxInt(h, 2), 900)
	tr, _, actual, err := volumeG.LineTraceNumbers(1, volumeXL)
	if err != nil {
		setText(vc.status, err.Error())
		return
	}
	opts := segy.RenderOptions{Width: w, Height: h, AGC: agc, ClipPercent: clipPercent, GainPercent: gainPercent, UseValueLimits: useLimits, MinValue: limitMin, MaxValue: limitMax, SampleStart: volumeSampleMin, SampleEnd: volumeSampleMax, DisplayMode: renderDisplayMode}
	values, st, err := volumeF.RenderTraceIndicesValues(tr, opts)
	if err != nil {
		setText(vc.status, err.Error())
		return
	}
	volumeXL = actual
	volumeCrosslineValues = values
	volumeCrossline = comparePanel{w: w, h: h, stats: st, title: fmt.Sprintf("Crossline %d", actual)}
	remapVolumeSharedNormalization()
}

func loadVolumeTimeSlice(sample int) {
	if volumeF == nil || volumeG == nil || volumeC == nil {
		return
	}
	if sample < volumeSampleMin {
		sample = volumeSampleMin
	}
	if sample > volumeSampleMax {
		sample = volumeSampleMax
	}
	vals, ok := volumeC.GetSliceCached(sample)
	if !ok {
		setVolumeBusy(1)
		updateVolumeStatusLine("正在缓存 Time Slice slab...")
		var err error
		vals, _, err = volumeC.GetSlice(sample)
		setVolumeBusy(-1)
		if err != nil {
			setText(vc.status, err.Error())
			return
		}
	}
	volumeSample = sample
	volumeTimeVals = append(volumeTimeVals[:0], vals...)
	updateVolumeTimePanel()
	bs := volumeC.BlockSamples()
	volumeC.Prefetch(sample + bs)
	volumeC.Prefetch(sample - bs)
}

func updateVolumeTimePanel() {
	if volumeG == nil || len(volumeTimeVals) == 0 {
		return
	}
	r := volumePanelRects()[2]
	w, h := rectWH(r)
	w = minInt(maxInt(w, 2), 900)
	h = minInt(maxInt(h, 2), 900)
	raster, mask, err := volumeG.RasterizeTimeSlice(volumeTimeVals, w, h, volumeG.Bounds())
	if err != nil {
		setText(vc.status, err.Error())
		return
	}
	volumeTimeRaster, volumeTimeMask = raster, mask
	tm := float64(volumeSample*volumeF.Info.SampleIntervalUS) / 1000.0
	volumeTime = comparePanel{w: w, h: h, title: "Time Slice " + formatAdaptiveTimeMS(tm)}
	remapVolumeSharedNormalization()
}

func renderVolumeAllFromCurrent(haveVals bool) {
	if volumeF == nil || volumeG == nil {
		return
	}
	renderVolumeInline()
	renderVolumeCrossline()
	if haveVals && len(volumeTimeVals) > 0 {
		updateVolumeTimePanel()
	} else {
		volumeActiveAxis = 2
		loadVolumeTimeSlice(volumeSample)
	}
	updateVolumeControls()
	volumeCrossIL, volumeCrossXL, volumeCrossSample = float64(volumeIL), float64(volumeXL), float64(volumeSample)
	volumeCrossValid = true
	invalidateVolumeBase()
}

func volumeSlicesStatusText() string {
	if volumeF == nil {
		return ""
	}
	prefix := ""
	if volumeCompareMode {
		prefix = volumeActiveSideName() + " | "
	}
	text := fmt.Sprintf("%sSlices: IL %d | XL %d | Time %s", prefix, volumeIL, volumeXL, formatAdaptiveTimeMS(float64(volumeSample*volumeF.Info.SampleIntervalUS)/1000.0))
	if volumeTimeMax > volumeTimeMin {
		text += fmt.Sprintf(" | Shared %.5g .. %.5g", volumeTimeMin, volumeTimeMax)
	}
	return text
}

func volumeCursorStatusText() string {
	if !volumeCrossValid || volumeF == nil {
		return ""
	}
	return fmt.Sprintf("Cursor: IL %.0f | XL %.0f | Time %s", volumeCrossIL, volumeCrossXL, formatAdaptiveTimeMS(volumeCrossSample*float64(volumeF.Info.SampleIntervalUS)/1000.0))
}

func updateVolumeStatusLine(extra string) {
	base := volumeSlicesStatusText()
	cur := volumeCursorStatusText()
	text := base
	if cur != "" {
		text += "   ||   " + cur
	}
	if extra != "" {
		if text != "" {
			text += "   ||   "
		}
		text += extra
	}
	if text != "" && vc.status != 0 {
		setText(vc.status, text)
	}
}

func updateVolumeControls() {
	if volumeF == nil {
		return
	}
	setText(vc.ilEdit, fmt.Sprintf("%d", volumeIL))
	setText(vc.xlEdit, fmt.Sprintf("%d", volumeXL))
	setText(vc.tEdit, formatAdaptiveTimeMS(float64(volumeSample*volumeF.Info.SampleIntervalUS)/1000.0))
	setVolumeFactorCombo(vc.axisIL, volumeAxisILFactor)
	setVolumeFactorCombo(vc.axisXL, volumeAxisXLFactor)
	setVolumeFactorCombo(vc.axisZ, volumeAxisZFactor)
	setVolumeFOVCombo(volumeCamFOV)
	if vc.aspect != 0 {
		pSendMessageW.Call(vc.aspect, CB_SETCURSEL, uintptr(volumeAspectMode), 0)
	}
	updateVolumeStatusLine(fmt.Sprintf("3D View | %s | Az %.0f° El %.0f° | 滚轮缩放，Space 复位", volumeProjectionName(), volumeCamAzimuth, volumeCamElevation))
}

func cycleVolumePalette(delta int) {
	if len(paletteNames) == 0 {
		return
	}
	volumePaletteIndex = (volumePaletteIndex + delta) % len(paletteNames)
	if volumePaletteIndex < 0 {
		volumePaletteIndex += len(paletteNames)
	}
	pSendMessageW.Call(vc.palette, CB_SETCURSEL, uintptr(volumePaletteComboIndex(volumePaletteIndex)), 0)
	refreshVolumePaletteOnly()
}

func refreshVolumePaletteCurrent() {
	if len(volumeInline.indices) > 0 {
		volumeInline.bgra = volumePaletteBGRA(volumeInline.indices)
	}
	if len(volumeCrossline.indices) > 0 {
		volumeCrossline.bgra = volumePaletteBGRA(volumeCrossline.indices)
	}
	if len(volumeTimeRaster) > 0 {
		volumeTime.bgra = volumeRasterBGRA(volumeTimeRaster, volumeTimeMask, volumeTimeMin, volumeTimeMax)
	}
}

func refreshVolumePaletteOnly() {
	if volumeCompareMode && volumeScenes[0].valid && volumeScenes[1].valid {
		active := volumeActiveSide
		saveActiveVolumeScene()
		for side := 0; side < 2; side++ {
			applyVolumeSceneState(volumeScenes[side])
			refreshVolumePaletteCurrent()
			volumeScenes[side] = captureVolumeSceneState()
		}
		volumeActiveSide = active
		applyVolumeSceneState(volumeScenes[active])
	} else {
		refreshVolumePaletteCurrent()
		if volumeScenes[volumeActiveSide].valid {
			volumeScenes[volumeActiveSide] = captureVolumeSceneState()
		}
	}
	invalidateVolumeBase()
}

func refreshVolumeGainCurrent() {
	if volumeF == nil {
		return
	}
	renderVolumeInline()
	renderVolumeCrossline()
	updateVolumeTimePanel()
}

func refreshVolumeGainOrLimits() {
	if volumeF == nil {
		return
	}
	if volumeCompareMode && volumeScenes[0].valid && volumeScenes[1].valid {
		active := volumeActiveSide
		saveActiveVolumeScene()
		for side := 0; side < 2; side++ {
			applyVolumeSceneState(volumeScenes[side])
			refreshVolumeGainCurrent()
			volumeScenes[side] = captureVolumeSceneState()
		}
		volumeActiveSide = active
		applyVolumeSceneState(volumeScenes[active])
	} else {
		refreshVolumeGainCurrent()
		if volumeScenes[volumeActiveSide].valid {
			volumeScenes[volumeActiveSide] = captureVolumeSceneState()
		}
	}
	invalidateVolumeBase()
}

func moveVolumeAxis(axis, delta int) {
	if volumeG == nil || volumeF == nil {
		return
	}
	volumeActiveAxis = axis
	switch axis {
	case 0:
		i := nearestVolumeIndex(volumeG.InlineValues, volumeIL)
		i = clampInt(i+delta, 0, len(volumeG.InlineValues)-1)
		volumeIL = volumeG.InlineValues[i]
		renderVolumeInline()
	case 1:
		i := nearestVolumeIndex(volumeG.CrosslineValues, volumeXL)
		i = clampInt(i+delta, 0, len(volumeG.CrosslineValues)-1)
		volumeXL = volumeG.CrosslineValues[i]
		renderVolumeCrossline()
	case 2:
		loadVolumeTimeSlice(volumeSample + delta)
	}
	updateVolumeControls()
	volumeCrossIL, volumeCrossXL, volumeCrossSample = float64(volumeIL), float64(volumeXL), float64(volumeSample)
	volumeCrossValid = true
	invalidateVolumeBase()
}

func paintVolumePanel(hdc uintptr, r RECT, p comparePanel, panel int) {
	if len(p.bgra) == 0 || p.w < 1 || p.h < 1 {
		return
	}
	w, h := rectWH(r)
	bmi := BITMAPINFO{Header: BITMAPINFOHEADER{Size: uint32(unsafe.Sizeof(BITMAPINFOHEADER{})), Width: int32(p.w), Height: -int32(p.h), Planes: 1, BitCount: 32, Compression: BI_RGB, SizeImage: uint32(len(p.bgra))}}
	pSetStretchBltMode.Call(hdc, HALFTONE)
	pStretchDIBits.Call(hdc, uintptr(r.Left), uintptr(r.Top), uintptr(w), uintptr(h), 0, 0, uintptr(p.w), uintptr(p.h), uintptr(unsafe.Pointer(&p.bgra[0])), uintptr(unsafe.Pointer(&bmi)), DIB_RGB_COLORS, SRCCOPY)

	// The focused 2-D slice inherits the 3-D style semantics instead of always
	// drawing the old blue rectangle. Clean/CIGVis stay borderless; Interpretation
	// keeps only a very faint edge; Standard retains the clearest frame.
	if volumeFocusPanel >= 0 && (volumeStyleMode == 1 || volumeStyleMode == 2) {
		borderColor := rgbRef(226, 231, 238)
		borderWidth := uintptr(1)
		if volumeStyleMode == 2 {
			borderColor = rgbRef(165, 183, 208)
			borderWidth = 2
		}
		pen, _, _ := pCreatePen.Call(PS_SOLID, borderWidth, borderColor)
		old, _, _ := pSelectObject.Call(hdc, pen)
		drawLine(hdc, int(r.Left), int(r.Top), int(r.Right)-1, int(r.Top))
		drawLine(hdc, int(r.Right)-1, int(r.Top), int(r.Right)-1, int(r.Bottom)-1)
		drawLine(hdc, int(r.Right)-1, int(r.Bottom)-1, int(r.Left), int(r.Bottom)-1)
		drawLine(hdc, int(r.Left), int(r.Bottom)-1, int(r.Left), int(r.Top))
		pSelectObject.Call(hdc, old)
		if pen != 0 {
			pDeleteObject.Call(pen)
		}
	} else if volumeFocusPanel < 0 {
		// Preserve the established tiled 2-D layout. v1.8.3 changes only the
		// full-view slice entered from the 3-D volume.
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
	}

	// In a 2-D full view, the two fixed lines are the true intersections with
	// the other two orthogonal slices. This mirrors the line hierarchy of the
	// 3-D volume and is intentionally disabled by the Clean preset.
	if volumeFocusPanel >= 0 {
		drawVolumeFocusedIntersectionGuides(hdc, r, panel)
	}

	drawAxisText(hdc, p.title, int(r.Left), int(r.Top)-24, int(r.Right), int(r.Top)-4, DT_CENTER)
	if volumeG == nil || volumeF == nil {
		return
	}
	b := volumeG.Bounds()
	if panel == 0 {
		drawAxisText(hdc, fmt.Sprintf("XL %d", b.CrosslineMin), int(r.Left), int(r.Bottom)+4, int(r.Left)+100, int(r.Bottom)+23, DT_LEFT)
		drawAxisText(hdc, fmt.Sprintf("XL %d", b.CrosslineMax), int(r.Right)-110, int(r.Bottom)+4, int(r.Right), int(r.Bottom)+23, DT_RIGHT)
	} else if panel == 1 {
		drawAxisText(hdc, fmt.Sprintf("IL %d", b.InlineMin), int(r.Left), int(r.Bottom)+4, int(r.Left)+100, int(r.Bottom)+23, DT_LEFT)
		drawAxisText(hdc, fmt.Sprintf("IL %d", b.InlineMax), int(r.Right)-110, int(r.Bottom)+4, int(r.Right), int(r.Bottom)+23, DT_RIGHT)
	} else {
		drawAxisText(hdc, fmt.Sprintf("XL %d", b.CrosslineMin), int(r.Left), int(r.Bottom)+4, int(r.Left)+100, int(r.Bottom)+23, DT_LEFT)
		drawAxisText(hdc, fmt.Sprintf("XL %d", b.CrosslineMax), int(r.Right)-110, int(r.Bottom)+4, int(r.Right), int(r.Bottom)+23, DT_RIGHT)
		drawAxisText(hdc, fmt.Sprintf("IL %d", b.InlineMin), int(r.Left)-55, int(r.Top)-7, int(r.Left)-4, int(r.Top)+14, DT_RIGHT)
		drawAxisText(hdc, fmt.Sprintf("IL %d", b.InlineMax), int(r.Left)-55, int(r.Bottom)-14, int(r.Left)-4, int(r.Bottom)+7, DT_RIGHT)
		return
	}
	t0 := float64(volumeSampleMin*volumeF.Info.SampleIntervalUS) / 1000
	t1 := float64(volumeSampleMax*volumeF.Info.SampleIntervalUS) / 1000
	drawAxisText(hdc, formatAdaptiveTimeMS(t0), int(r.Left)-60, int(r.Top)-7, int(r.Left)-4, int(r.Top)+14, DT_RIGHT)
	drawAxisText(hdc, formatAdaptiveTimeMS(t1), int(r.Left)-60, int(r.Bottom)-14, int(r.Left)-4, int(r.Bottom)+7, DT_RIGHT)
}

func drawVolumeFocusedIntersectionGuides(hdc uintptr, r RECT, panel int) {
	if volumeStyleMode == 3 || volumeG == nil { // Clean: seismic texture only.
		return
	}
	x, y, ok := volumePixelAtWorld(panel, float64(volumeIL), float64(volumeXL), float64(volumeSample))
	if !ok {
		return
	}
	color := rgbRef(236, 236, 236) // CIGVis: neutral, unobtrusive intersections.
	width := uintptr(1)
	switch volumeStyleMode {
	case 1: // Interpretation: warmer guide, still thin.
		color = rgbRef(246, 222, 164)
	case 2: // Standard: clearest fixed locator.
		color = rgbRef(183, 202, 226)
		width = 2
	}
	pen, _, _ := pCreatePen.Call(PS_SOLID, width, color)
	old, _, _ := pSelectObject.Call(hdc, pen)
	drawLine(hdc, x, int(r.Top), x, int(r.Bottom)-1)
	drawLine(hdc, int(r.Left), y, int(r.Right)-1, y)
	pSelectObject.Call(hdc, old)
	if pen != 0 {
		pDeleteObject.Call(pen)
	}
}

func volumePixelAtWorld(panel int, il, xl, sample float64) (int, int, bool) {
	if volumeG == nil {
		return 0, 0, false
	}
	b := volumeG.Bounds()
	fx := (xl - float64(b.CrosslineMin)) / math.Max(float64(b.CrosslineMax-b.CrosslineMin), 1)
	fi := (il - float64(b.InlineMin)) / math.Max(float64(b.InlineMax-b.InlineMin), 1)
	ft := (sample - float64(volumeSampleMin)) / math.Max(float64(volumeSampleMax-volumeSampleMin), 1)
	if fx < 0 || fx > 1 || fi < 0 || fi > 1 || ft < 0 || ft > 1 {
		return 0, 0, false
	}
	if volumeFocusPanel >= 0 {
		r := volumeFocusRect()
		rw, rh := rectWH(r)
		var pxn, pyn float64
		switch volumeFocusPanel {
		case 0:
			pxn, pyn = fx, ft
		case 1:
			pxn, pyn = fi, ft
		default:
			pxn, pyn = fx, fi
		}
		return int(r.Left) + int(math.Round(pxn*float64(rw-1))), int(r.Top) + int(math.Round(pyn*float64(rh-1))), true
	}
	if volume3D {
		g := volumePlane(panel)
		var p volumePt
		switch panel {
		case 0:
			p = volumePlanePoint(g, fx, ft)
		case 1:
			p = volumePlanePoint(g, fi, ft)
		default:
			p = volumePlanePoint(g, fx, fi)
		}
		return int(math.Round(p.x)), int(math.Round(p.y)), true
	}
	r := volumePanelRects()[panel]
	rw, rh := rectWH(r)
	if rw < 2 || rh < 2 {
		return 0, 0, false
	}
	var pxn, pyn float64
	switch panel {
	case 0:
		pxn, pyn = fx, ft
	case 1:
		pxn, pyn = fi, ft
	default:
		pxn, pyn = fx, fi
	}
	return int(r.Left) + int(math.Round(pxn*float64(rw-1))), int(r.Top) + int(math.Round(pyn*float64(rh-1))), true
}

func volumeWorldAtPixel(panel, px, py int) (il, xl, sample float64, ok bool) {
	if volumeG == nil {
		return 0, 0, 0, false
	}
	b := volumeG.Bounds()
	if volumeFocusPanel >= 0 {
		r := volumeFocusRect()
		if !comparePointInRect(px, py, r) {
			return 0, 0, 0, false
		}
		rw, rh := rectWH(r)
		fxn := float64(px-int(r.Left)) / float64(maxInt(rw-1, 1))
		fyn := float64(py-int(r.Top)) / float64(maxInt(rh-1, 1))
		switch volumeFocusPanel {
		case 0:
			return float64(volumeIL), float64(b.CrosslineMin) + fxn*float64(b.CrosslineMax-b.CrosslineMin), float64(volumeSampleMin) + fyn*float64(volumeSampleMax-volumeSampleMin), true
		case 1:
			return float64(b.InlineMin) + fxn*float64(b.InlineMax-b.InlineMin), float64(volumeXL), float64(volumeSampleMin) + fyn*float64(volumeSampleMax-volumeSampleMin), true
		default:
			return float64(b.InlineMin) + fyn*float64(b.InlineMax-b.InlineMin), float64(b.CrosslineMin) + fxn*float64(b.CrosslineMax-b.CrosslineMin), float64(volumeSample), true
		}
	}
	if volume3D {
		u, v, inside := volumePlaneUVAtPoint(volumePlane(panel), float64(px), float64(py))
		if !inside {
			return 0, 0, 0, false
		}
		u = math.Max(0, math.Min(1, u))
		v = math.Max(0, math.Min(1, v))
		switch panel {
		case 0:
			return float64(volumeIL), float64(b.CrosslineMin) + u*float64(b.CrosslineMax-b.CrosslineMin), float64(volumeSampleMin) + v*float64(volumeSampleMax-volumeSampleMin), true
		case 1:
			return float64(b.InlineMin) + u*float64(b.InlineMax-b.InlineMin), float64(volumeXL), float64(volumeSampleMin) + v*float64(volumeSampleMax-volumeSampleMin), true
		default:
			return float64(b.InlineMin) + v*float64(b.InlineMax-b.InlineMin), float64(b.CrosslineMin) + u*float64(b.CrosslineMax-b.CrosslineMin), float64(volumeSample), true
		}
	}
	r := volumePanelRects()[panel]
	if !comparePointInRect(px, py, r) {
		return 0, 0, 0, false
	}
	rw, rh := rectWH(r)
	fx := float64(px-int(r.Left)) / float64(maxInt(rw-1, 1))
	fy := float64(py-int(r.Top)) / float64(maxInt(rh-1, 1))
	switch panel {
	case 0:
		return float64(volumeIL), float64(b.CrosslineMin) + fx*float64(b.CrosslineMax-b.CrosslineMin), float64(volumeSampleMin) + fy*float64(volumeSampleMax-volumeSampleMin), true
	case 1:
		return float64(b.InlineMin) + fx*float64(b.InlineMax-b.InlineMin), float64(volumeXL), float64(volumeSampleMin) + fy*float64(volumeSampleMax-volumeSampleMin), true
	default:
		return float64(b.InlineMin) + fy*float64(b.InlineMax-b.InlineMin), float64(b.CrosslineMin) + fx*float64(b.CrosslineMax-b.CrosslineMin), float64(volumeSample), true
	}
}

func volumePointSegmentDistance(px, py float64, a, b volumePt) float64 {
	dx, dy := b.x-a.x, b.y-a.y
	den := dx*dx + dy*dy
	if den <= 1e-12 {
		return math.Hypot(px-a.x, py-a.y)
	}
	t := ((px-a.x)*dx + (py-a.y)*dy) / den
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	qx, qy := a.x+t*dx, a.y+t*dy
	return math.Hypot(px-qx, py-qy)
}

func volumePlaneEdgeDistance(panel int, px, py int) float64 {
	if volumeG == nil || !volume3D {
		return math.Inf(1)
	}
	g := volumePlane(panel)
	p0 := g.origin
	p1 := addVolumePt(g.origin, g.u)
	p2 := addVolumePt(p1, g.v)
	p3 := addVolumePt(g.origin, g.v)
	d := volumePointSegmentDistance(float64(px), float64(py), p0, p1)
	for _, e := range [][2]volumePt{{p1, p2}, {p2, p3}, {p3, p0}} {
		v := volumePointSegmentDistance(float64(px), float64(py), e[0], e[1])
		if v < d {
			d = v
		}
	}
	return d
}

func volumeDragAxisAtPoint(px, py int) int {
	if !volume3D || volumeFocusPanel >= 0 || volumeG == nil {
		return -1
	}
	best, bestD := -1, 9.0
	bestDepth := math.Inf(1)
	for panel := 0; panel < 3; panel++ {
		d := volumePlaneEdgeDistance(panel, px, py)
		if d >= 9.0 {
			continue
		}
		u, v, _ := volumePlaneUVAtPoint(volumePlane(panel), float64(px), float64(py))
		u = math.Max(0, math.Min(1, u))
		v = math.Max(0, math.Min(1, v))
		depth := volumePlaneDepth(panel, u, v)
		if d < bestD-0.5 || (math.Abs(d-bestD) <= 0.5 && depth < bestDepth) {
			best, bestD, bestDepth = panel, d, depth
		}
	}
	return best
}

func volumeCursorForAxis(axis int) uintptr {
	id := uintptr(IDC_ARROW)
	switch axis {
	case 0:
		id = IDC_SIZENESW // Inline plane moves along the oblique inline vector.
	case 1:
		id = IDC_SIZENWSE // Crossline plane moves along the other oblique vector.
	case 2:
		id = IDC_SIZENS // Time Slice moves vertically through the cube.
	}
	c, _, _ := pLoadCursorW.Call(0, id)
	return c
}

func setVolumeDragCursor(axis int) {
	if c := volumeCursorForAxis(axis); c != 0 {
		pSetCursor.Call(c)
	}
}

func volumeDragVector(axis int) volumePt {
	_, vx, vi, vt := volumeCubeBasis()
	switch axis {
	case 0:
		return vi
	case 1:
		return vx
	default:
		return vt
	}
}

func beginVolumePlaneDrag(axis, mx, my int) {
	if axis < 0 || volumeG == nil {
		return
	}
	volumeDragAxis = axis
	volumeActiveAxis = axis
	volumeHoverAxis = axis
	volumeDragStartX, volumeDragStartY = mx, my
	volumeDragStartILIndex = nearestVolumeIndex(volumeG.InlineValues, volumeIL)
	volumeDragStartXLIndex = nearestVolumeIndex(volumeG.CrosslineValues, volumeXL)
	volumeDragStartSample = volumeSample
	volumeDragLastRender = time.Time{}
	pSetCapture.Call(volumeHwnd)
	setVolumeDragCursor(axis)
	volumeCrossValid = false
}

func dragVolumePlane(mx, my int, final bool) {
	if volumeDragAxis < 0 || volumeG == nil || volumeF == nil {
		return
	}
	v := volumeDragVector(volumeDragAxis)
	den := v.x*v.x + v.y*v.y
	if den <= 1e-9 {
		return
	}
	dx := float64(mx - volumeDragStartX)
	dy := float64(my - volumeDragStartY)
	deltaNorm := (dx*v.x + dy*v.y) / den
	now := time.Now()
	allowRender := final || volumeDragLastRender.IsZero() || now.Sub(volumeDragLastRender) >= 40*time.Millisecond

	switch volumeDragAxis {
	case 0:
		n := len(volumeG.InlineValues)
		if n == 0 {
			return
		}
		i := clampInt(volumeDragStartILIndex+int(math.Round(deltaNorm*float64(maxInt(n-1, 1)))), 0, n-1)
		newIL := volumeG.InlineValues[i]
		if newIL != volumeIL {
			volumeIL = newIL
			if allowRender {
				renderVolumeInline()
				volumeDragLastRender = now
			}
			updateVolumeControls()
			updateVolumeStatusLine("Dragging: Inline")
			invalidateVolumeBase()
		}
	case 1:
		n := len(volumeG.CrosslineValues)
		if n == 0 {
			return
		}
		i := clampInt(volumeDragStartXLIndex+int(math.Round(deltaNorm*float64(maxInt(n-1, 1)))), 0, n-1)
		newXL := volumeG.CrosslineValues[i]
		if newXL != volumeXL {
			volumeXL = newXL
			if allowRender {
				renderVolumeCrossline()
				volumeDragLastRender = now
			}
			updateVolumeControls()
			updateVolumeStatusLine("Dragging: Crossline")
			invalidateVolumeBase()
		}
	case 2:
		n := maxInt(volumeSampleMax-volumeSampleMin, 1)
		sm := clampInt(volumeDragStartSample+int(math.Round(deltaNorm*float64(n))), volumeSampleMin, volumeSampleMax)
		if sm != volumeSample {
			volumeSample = sm // Moves the Time Slice plane immediately in 3-D.
			if vals, ok := volumeC.GetSliceCached(sm); ok {
				if allowRender {
					volumeTimeVals = append(volumeTimeVals[:0], vals...)
					updateVolumeTimePanel()
					volumeDragLastRender = now
				}
			} else {
				// Do not show the previous Time Slice texture at a new time position.
				// The plane outline still moves continuously; the correct texture is
				// loaded on mouse release, with the wait cursor if disk I/O is needed.
				volumeTime.bgra = nil
				updateVolumeStatusLine("Time Slice " + formatAdaptiveTimeMS(float64(sm*volumeF.Info.SampleIntervalUS)/1000.0) + "：松开鼠标加载未缓存切片")
			}
			updateVolumeControls()
			updateVolumeStatusLine("Dragging: Time Slice")
			invalidateVolumeBase()
		}
	}

	if final {
		switch volumeDragAxis {
		case 0:
			renderVolumeInline()
		case 1:
			renderVolumeCrossline()
		case 2:
			loadVolumeTimeSlice(volumeSample)
		}
		updateVolumeControls()
		volumeCrossIL, volumeCrossXL, volumeCrossSample = float64(volumeIL), float64(volumeXL), float64(volumeSample)
		volumeCrossValid = true
		invalidateVolumeBase()
	}
}

func endVolumePlaneDrag(mx, my int) {
	if volumeDragAxis < 0 {
		return
	}
	dragVolumePlane(mx, my, true)
	volumeDragAxis = -1
	volumeActiveAxis = 2
	pReleaseCapture.Call()
	setVolumeDragCursor(-1)
}

func volumePanelAtPoint(px, py int) int {
	if volumeFocusPanel >= 0 {
		if comparePointInRect(px, py, volumeFocusRect()) {
			return volumeFocusPanel
		}
		return -1
	}
	if volume3D {
		best := -1
		bestDepth := math.Inf(1)
		for panel := 0; panel < 3; panel++ {
			u, v, ok := volumePlaneUVAtPoint(volumePlane(panel), float64(px), float64(py))
			if !ok {
				continue
			}
			u = math.Max(0, math.Min(1, u))
			v = math.Max(0, math.Min(1, v))
			depth := volumePlaneDepth(panel, u, v)
			if depth < bestDepth {
				best, bestDepth = panel, depth
			}
		}
		return best
	}
	for i, r := range volumePanelRects() {
		if comparePointInRect(px, py, r) {
			return i
		}
	}
	return -1
}

func updateVolumeCrosshair(mx, my int) {
	volumeCrossValid = false
	panel := volumePanelAtPoint(mx, my)
	if panel < 0 {
		updateVolumeStatusLine("")
		return
	}
	il, xl, sm, ok := volumeWorldAtPixel(panel, mx, my)
	if !ok {
		return
	}
	volumeCrossIL, volumeCrossXL, volumeCrossSample = il, xl, sm
	volumeCrossValid = true
	if volumeF != nil {
		updateVolumeStatusLine("单击切面可驱动另外两切面")
	}
}

func applyVolumeClick(panel, mx, my int) {
	volumeActiveAxis = panel
	il, xl, sm, ok := volumeWorldAtPixel(panel, mx, my)
	if !ok || volumeG == nil {
		return
	}
	if panel == 0 {
		i := nearestVolumeIndex(volumeG.CrosslineValues, int32(math.Round(xl)))
		volumeXL = volumeG.CrosslineValues[i]
		volumeSample = clampInt(int(math.Round(sm)), volumeSampleMin, volumeSampleMax)
		renderVolumeCrossline()
		loadVolumeTimeSlice(volumeSample)
	} else if panel == 1 {
		i := nearestVolumeIndex(volumeG.InlineValues, int32(math.Round(il)))
		volumeIL = volumeG.InlineValues[i]
		volumeSample = clampInt(int(math.Round(sm)), volumeSampleMin, volumeSampleMax)
		renderVolumeInline()
		loadVolumeTimeSlice(volumeSample)
	} else {
		ii := nearestVolumeIndex(volumeG.InlineValues, int32(math.Round(il)))
		ix := nearestVolumeIndex(volumeG.CrosslineValues, int32(math.Round(xl)))
		volumeIL, volumeXL = volumeG.InlineValues[ii], volumeG.CrosslineValues[ix]
		renderVolumeInline()
		renderVolumeCrossline()
	}
	updateVolumeControls()
	volumeCrossIL, volumeCrossXL, volumeCrossSample = float64(volumeIL), float64(volumeXL), float64(volumeSample)
	volumeCrossValid = true
	invalidateVolumeBase()
}

func destroyVolumeBase() {
	if volumeBaseDC != 0 && volumeBaseOldBmp != 0 {
		pSelectObject.Call(volumeBaseDC, volumeBaseOldBmp)
	}
	if volumeBaseBmp != 0 {
		pDeleteObject.Call(volumeBaseBmp)
	}
	if volumeBaseDC != 0 {
		pDeleteDC.Call(volumeBaseDC)
	}
	volumeBaseDC, volumeBaseBmp, volumeBaseOldBmp = 0, 0, 0
	volumeBaseW, volumeBaseH = 0, 0
	volumeBaseDirty = true
}

func drawVolumeFocusedPanel(hdc uintptr) {
	if volumeFocusPanel < 0 || volumeFocusPanel > 2 {
		return
	}
	r := volumeFocusRect()
	paintVolumePanel(hdc, r, volumePanelData(volumeFocusPanel), volumeFocusPanel)
	name := []string{"Inline", "Crossline", "Time Slice"}[volumeFocusPanel]
	drawAxisText(hdc, name+" 2D Full View  —  双击或 Esc 返回 3D", int(r.Left), int(r.Top)-32, int(r.Right), int(r.Top)-8, DT_CENTER)
}

func volumeResidualRenderKey() string {
	if !volumeScenes[0].valid || !volumeScenes[1].valid {
		return ""
	}
	a, b := volumeScenes[0], volumeScenes[1]
	return fmt.Sprintf("%s|%s|%d|%d|%d|%d|%d|%.3f|%t|%.6g|%.6g|%d|%d",
		a.path, b.path, a.il, a.xl, a.sample, a.sampleMin, a.sampleMax,
		gainPercent, useLimits, limitMin, limitMax, renderDisplayMode, volumePaletteIndex)
}

func ensureVolumeResidualPanels() {
	if !volumeShowDiff || !volumeScenes[0].valid || !volumeScenes[1].valid {
		return
	}
	key := volumeResidualRenderKey()
	if key != "" && key == volumeResidualKey && len(volumeResidualInline.bgra) > 0 && len(volumeResidualCrossline.bgra) > 0 && len(volumeResidualTime.bgra) > 0 {
		return
	}
	volumeResidualInline, volumeResidualCrossline, volumeResidualTime = comparePanel{}, comparePanel{}, comparePanel{}
	volumeResidualNote = ""
	a, b := volumeScenes[0], volumeScenes[1]
	if a.f == nil || b.f == nil || a.g == nil || b.g == nil || a.c == nil || b.c == nil {
		volumeResidualNote = "A/B geometry 未准备完成"
		return
	}
	if a.f.Info.SampleIntervalUS != b.f.Info.SampleIntervalUS {
		volumeResidualNote = fmt.Sprintf("A-B 需要相同采样率 (%d/%d us)", a.f.Info.SampleIntervalUS, b.f.Info.SampleIntervalUS)
		return
	}
	rs := volumeCompareDisplayRects()
	w, h := 520, 620
	if len(rs) >= 3 {
		w, h = rectWH(rs[2])
		w = minInt(maxInt(w, 180), 760)
		h = minInt(maxInt(h, 180), 820)
	}
	opts := segy.RenderOptions{Width: w, Height: h, AGC: agc, ClipPercent: clipPercent, GainPercent: gainPercent,
		UseValueLimits: useLimits, MinValue: limitMin, MaxValue: limitMax,
		SampleStart: a.sampleMin, SampleEnd: a.sampleMax, DisplayMode: renderDisplayMode}

	// Inline residual at A's actual inline, matched by Crossline coordinate.
	tA, cA, actualA, eA := a.g.LineTraceNumbers(0, a.il)
	tB, cB, actualB, eB := b.g.LineTraceNumbers(0, a.il)
	if eA == nil && eB == nil && actualA == actualB {
		pA, pB, _ := pairLineTracesByCoord(tA, cA, tB, cB)
		if len(pA) >= 2 {
			pix, st, err := segy.RenderTraceDifferencePairs(a.f, b.f, pA, pB, opts)
			if err == nil {
				volumeResidualInline = comparePanel{indices: pix, bgra: volumePaletteBGRA(pix), w: w, h: h, stats: st, title: fmt.Sprintf("A-B Inline %d", actualA)}
			}
		}
	}

	// Crossline residual at A's actual crossline, matched by Inline coordinate.
	tA, cA, actualA, eA = a.g.LineTraceNumbers(1, a.xl)
	tB, cB, actualB, eB = b.g.LineTraceNumbers(1, a.xl)
	if eA == nil && eB == nil && actualA == actualB {
		pA, pB, _ := pairLineTracesByCoord(tA, cA, tB, cB)
		if len(pA) >= 2 {
			pix, st, err := segy.RenderTraceDifferencePairs(a.f, b.f, pA, pB, opts)
			if err == nil {
				volumeResidualCrossline = comparePanel{indices: pix, bgra: volumePaletteBGRA(pix), w: w, h: h, stats: st, title: fmt.Sprintf("A-B Crossline %d", actualA)}
			}
		}
	}

	// Time-slice residual follows A's physical time and is rasterized on A's
	// geometry bounds, so the residual plane has exactly the same spatial frame.
	bSample := a.sample
	if b.f.Info.SampleIntervalUS > 0 {
		timeUS := float64(a.sample * a.f.Info.SampleIntervalUS)
		bSample = clampInt(int(math.Round(timeUS/float64(b.f.Info.SampleIntervalUS))), b.sampleMin, b.sampleMax)
	}
	valsA, _, errA := a.c.GetSlice(a.sample)
	valsB, _, errB := b.c.GetSlice(bSample)
	if errA == nil && errB == nil {
		ra, ma, ea := a.g.RasterizeTimeSlice(valsA, w, h, a.g.Bounds())
		rb, mb, eb := b.g.RasterizeTimeSlice(valsB, w, h, a.g.Bounds())
		if ea == nil && eb == nil {
			rd, md := differenceRaster(ra, ma, rb, mb)
			lo, hi := symmetricResidualRange(rd, md)
			tm := float64(a.sample*a.f.Info.SampleIntervalUS) / 1000.0
			volumeResidualTime = comparePanel{bgra: volumeRasterBGRA(rd, md, lo, hi), w: w, h: h, title: "A-B Time " + formatAdaptiveTimeMS(tm)}
		}
	}
	if len(volumeResidualInline.bgra) == 0 || len(volumeResidualCrossline.bgra) == 0 || len(volumeResidualTime.bgra) == 0 {
		volumeResidualNote = "部分残差不可用：请确认 A/B 几何、采样率和共同 IL/XL 范围"
	}
	volumeResidualKey = key
}

func drawVolumeCompareStatic(hdc uintptr) {
	if !volumeScenes[0].valid || !volumeScenes[1].valid {
		drawVolumeCubeStatic(hdc)
		return
	}
	active := volumeActiveSide
	saveActiveVolumeScene()
	if volumeShowDiff {
		ensureVolumeResidualPanels()
	}
	rs := volumeCompareDisplayRects()
	oldDrag := volumeDragAxis
	for side := 0; side < 2 && side < len(rs); side++ {
		applyVolumeSceneState(volumeScenes[side])
		if side != active {
			volumeDragAxis = -1
		}
		r := rs[side]
		volumeRenderRectOverride = &r
		drawVolumeCubeStatic(hdc)
		volumeRenderRectOverride = nil

		label := "A 左图"
		if side == 1 {
			label = "B 右图"
		}
		if volumeScenes[side].path != "" {
			label += "  " + shortPath(volumeScenes[side].path, 34)
		}
		oldColor, _, _ := pSetTextColor.Call(hdc, rgbRef(95, 95, 95))
		if side == active {
			pSetTextColor.Call(hdc, rgbRef(55, 105, 185))
			label += "  [当前]"
		}
		drawAxisText(hdc, label, int(r.Left), int(r.Top)-21, int(r.Right), int(r.Top)-3, DT_CENTER)
		pSetTextColor.Call(hdc, oldColor)
	}

	if volumeShowDiff && len(rs) >= 3 {
		// Residual is a data-derived QC view: it follows A's slice position and
		// camera so A / B / A-B can be judged in a common spatial frame.
		left := volumeScenes[0]
		applyVolumeSceneState(left)
		volumeInline, volumeCrossline, volumeTime = volumeResidualInline, volumeResidualCrossline, volumeResidualTime
		volumeDragAxis, volumeHoverAxis = -1, -1
		r := rs[2]
		volumeRenderRectOverride = &r
		drawVolumeCubeStatic(hdc)
		volumeRenderRectOverride = nil
		label := "差  A-B  [跟随左图]"
		if volumeResidualNote != "" {
			label += "  |  " + volumeResidualNote
		}
		oldColor, _, _ := pSetTextColor.Call(hdc, rgbRef(145, 75, 55))
		drawAxisText(hdc, label, int(r.Left), int(r.Top)-21, int(r.Right), int(r.Top)-3, DT_CENTER)
		pSetTextColor.Call(hdc, oldColor)
	}

	volumeDragAxis = oldDrag
	volumeRenderRectOverride = nil
	volumeActiveSide = active
	applyVolumeSceneState(volumeScenes[active])

	// Very light separators stay outside the seismic geometry.
	pen, _, _ := pCreatePen.Call(PS_SOLID, 1, rgbRef(242, 242, 242))
	old, _, _ := pSelectObject.Call(hdc, pen)
	for i := 0; i+1 < len(rs); i++ {
		x := (int(rs[i].Right) + int(rs[i+1].Left)) / 2
		drawLine(hdc, x, int(rs[i].Top)-2, x, int(rs[i].Bottom))
	}
	pSelectObject.Call(hdc, old)
	if pen != 0 {
		pDeleteObject.Call(pen)
	}
}

func drawVolumeStatic(hdc uintptr) {
	r := clientRect(volumeHwnd)
	white, _, _ := pGetStockObject.Call(WHITE_BRUSH)
	pFillRect.Call(hdc, uintptr(unsafe.Pointer(&r)), white)
	if hFont != 0 {
		pSelectObject.Call(hdc, hFont)
	}
	pSetBkMode.Call(hdc, TRANSPARENT)
	pSetTextColor.Call(hdc, rgbRef(0, 0, 0))
	if volumeFocusPanel >= 0 {
		drawVolumeFocusedPanel(hdc)
		return
	}
	if volume3D {
		if volumeCompareMode {
			drawVolumeCompareStatic(hdc)
		} else {
			drawVolumeCubeStatic(hdc)
		}
		return
	}
	prs := volumePanelRects()
	paintVolumePanel(hdc, prs[0], volumeInline, 0)
	paintVolumePanel(hdc, prs[1], volumeCrossline, 1)
	paintVolumePanel(hdc, prs[2], volumeTime, 2)
}

func ensureVolumeBase(hdc uintptr) bool {
	r := clientRect(volumeHwnd)
	w, h := int(r.Right-r.Left), int(r.Bottom-r.Top)
	if w <= 0 || h <= 0 {
		return false
	}
	if volumeBaseDC == 0 || volumeBaseBmp == 0 || volumeBaseW != w || volumeBaseH != h {
		destroyVolumeBase()
		mem, _, _ := pCreateCompatibleDC.Call(hdc)
		bmp, _, _ := pCreateCompatibleBitmap.Call(hdc, uintptr(w), uintptr(h))
		if mem == 0 || bmp == 0 {
			return false
		}
		old, _, _ := pSelectObject.Call(mem, bmp)
		volumeBaseDC, volumeBaseBmp, volumeBaseOldBmp = mem, bmp, old
		volumeBaseW, volumeBaseH = w, h
		volumeBaseDirty = true
	}
	if volumeBaseDirty {
		drawVolumeStatic(volumeBaseDC)
		volumeBaseDirty = false
	}
	return true
}

func invalidateVolumeBase() {
	volumeBaseDirty = true
	if volumeHwnd != 0 {
		// Never invalidate the controller strip while the 3-D camera is moving.
		// The parent scene is double-buffered, but native child buttons/combos are
		// separate HWNDs; repainting underneath them caused the toolbar flash seen
		// during left-drag rotation. Restrict redraw to the scene/status area.
		cw, ch := clientSize(volumeHwnd)
		r := RECT{Left: 0, Top: 62, Right: int32(cw), Bottom: int32(ch)}
		pInvalidateRect.Call(volumeHwnd, uintptr(unsafe.Pointer(&r)), 0)
	}
}

func invalidateVolumeCrosshair(il, xl, sample float64) {
	if volumeHwnd == 0 {
		return
	}
	if volumeFocusPanel >= 0 {
		r := volumeFocusRect()
		pInvalidateRect.Call(volumeHwnd, uintptr(unsafe.Pointer(&r)), 0)
		return
	}
	if volume3D {
		r := volumeCubeRect()
		pInvalidateRect.Call(volumeHwnd, uintptr(unsafe.Pointer(&r)), 0)
		return
	}
	const pad = 2
	prs := volumePanelRects()
	for panel := 0; panel < 3; panel++ {
		x, y, ok := volumePixelAtWorld(panel, il, xl, sample)
		if !ok {
			continue
		}
		r := prs[panel]
		vr := RECT{Left: int32(x - pad), Top: r.Top, Right: int32(x + pad + 1), Bottom: r.Bottom}
		hr := RECT{Left: r.Left, Top: int32(y - pad), Right: r.Right, Bottom: int32(y + pad + 1)}
		pInvalidateRect.Call(volumeHwnd, uintptr(unsafe.Pointer(&vr)), 0)
		pInvalidateRect.Call(volumeHwnd, uintptr(unsafe.Pointer(&hr)), 0)
	}
}

func paintVolumeTransient(hdc uintptr) {
	if !volumeCrossValid {
		return
	}
	// Strict CIGVis preset never overlays a cursor crosshair in 3-D: the three
	// true slice intersections are the only persistent geometry lines.
	if volume3D && volumeFocusPanel < 0 && volumeStyleMode == 0 {
		return
	}
	pen, _, _ := pCreatePen.Call(PS_SOLID, 1, rgbRef(255, 190, 0))
	old, _, _ := pSelectObject.Call(hdc, pen)
	if volumeFocusPanel >= 0 {
		// 2-D full-view cursor overlays follow the selected 3-D style:
		// Clean/CIGVis use no moving crosshair; Interpretation uses a compact
		// local crosshair; Standard uses the full-span locator.
		if volumeStyleMode == 3 || volumeStyleMode == 0 {
			pSelectObject.Call(hdc, old)
			if pen != 0 {
				pDeleteObject.Call(pen)
			}
			return
		}
		x, y, ok := volumePixelAtWorld(volumeFocusPanel, volumeCrossIL, volumeCrossXL, volumeCrossSample)
		if ok {
			r := volumeFocusRect()
			if volumeStyleMode == 1 {
				const half = 22
				x0, x1 := maxInt(int(r.Left), x-half), minInt(int(r.Right)-1, x+half)
				y0, y1 := maxInt(int(r.Top), y-half), minInt(int(r.Bottom)-1, y+half)
				drawLine(hdc, x, y0, x, y1)
				drawLine(hdc, x0, y, x1, y)
			} else {
				drawLine(hdc, x, int(r.Top), x, int(r.Bottom)-1)
				drawLine(hdc, int(r.Left), y, int(r.Right)-1, y)
			}
		}
	} else if volume3D && volumeG != nil {
		b := volumeG.Bounds()
		fx := (volumeCrossXL - float64(b.CrosslineMin)) / math.Max(float64(b.CrosslineMax-b.CrosslineMin), 1)
		fi := (volumeCrossIL - float64(b.InlineMin)) / math.Max(float64(b.InlineMax-b.InlineMin), 1)
		ft := (volumeCrossSample - float64(volumeSampleMin)) / math.Max(float64(volumeSampleMax-volumeSampleMin), 1)
		fx, fi, ft = math.Max(0, math.Min(1, fx)), math.Max(0, math.Min(1, fi)), math.Max(0, math.Min(1, ft))
		drawOn := func(panel int) {
			g := volumePlane(panel)
			var a, b0, c, d volumePt
			switch panel {
			case 0:
				a, b0 = volumePlanePoint(g, 0, ft), volumePlanePoint(g, 1, ft)
				c, d = volumePlanePoint(g, fx, 0), volumePlanePoint(g, fx, 1)
			case 1:
				a, b0 = volumePlanePoint(g, 0, ft), volumePlanePoint(g, 1, ft)
				c, d = volumePlanePoint(g, fi, 0), volumePlanePoint(g, fi, 1)
			default:
				a, b0 = volumePlanePoint(g, 0, fi), volumePlanePoint(g, 1, fi)
				c, d = volumePlanePoint(g, fx, 0), volumePlanePoint(g, fx, 1)
			}
			drawLine(hdc, int(a.x), int(a.y), int(b0.x), int(b0.y))
			drawLine(hdc, int(c.x), int(c.y), int(d.x), int(d.y))
		}
		if volumeMinimal {
			panel := volumeActiveAxis
			if volumeHoverAxis >= 0 {
				panel = volumeHoverAxis
			}
			drawOn(panel)
		} else {
			for panel := 0; panel < 3; panel++ {
				drawOn(panel)
			}
		}
	} else {
		prs := volumePanelRects()
		for panel := 0; panel < 3; panel++ {
			x, y, ok := volumePixelAtWorld(panel, volumeCrossIL, volumeCrossXL, volumeCrossSample)
			if ok {
				r := prs[panel]
				drawLine(hdc, x, int(r.Top), x, int(r.Bottom)-1)
				drawLine(hdc, int(r.Left), y, int(r.Right)-1, y)
			}
		}
	}
	pSelectObject.Call(hdc, old)
	if pen != 0 {
		pDeleteObject.Call(pen)
	}
}

func paintVolume() {
	var ps PAINTSTRUCT
	hdc, _, _ := pBeginPaint.Call(volumeHwnd, uintptr(unsafe.Pointer(&ps)))
	if hdc != 0 {
		if ensureVolumeBase(hdc) {
			pr := ps.RcPaint
			w, h := int(pr.Right-pr.Left), int(pr.Bottom-pr.Top)
			if w > 0 && h > 0 {
				pBitBlt.Call(hdc, uintptr(pr.Left), uintptr(pr.Top), uintptr(w), uintptr(h), volumeBaseDC, uintptr(pr.Left), uintptr(pr.Top), SRCCOPY)
				paintVolumeTransient(hdc)
			}
		} else {
			drawVolumeStatic(hdc)
			paintVolumeTransient(hdc)
		}
	}
	pEndPaint.Call(volumeHwnd, uintptr(unsafe.Pointer(&ps)))
}

func beginVolumeCameraRotate(mx, my int) {
	if !volume3D || volumeFocusPanel >= 0 {
		return
	}
	volumeCamRotating = true
	volumeCamStartX, volumeCamStartY = mx, my
	volumeCamStartAz, volumeCamStartEl = volumeCamAzimuth, volumeCamElevation
	volumeCamMoved = false
	volumeCamLastPaint = time.Time{}
	pSetCapture.Call(volumeHwnd)
	if c, _, _ := pLoadCursorW.Call(0, IDC_SIZEALL); c != 0 {
		pSetCursor.Call(c)
	}
}

func rotateVolumeCamera(mx, my int, final bool) {
	if !volumeCamRotating {
		return
	}
	dx := float64(mx - volumeCamStartX)
	dy := float64(my - volumeCamStartY)
	if math.Abs(dx)+math.Abs(dy) > 4 {
		volumeCamMoved = true
	}
	volumeCamAzimuth = volumeCamStartAz + dx*.38
	volumeCamElevation = math.Max(-80, math.Min(80, volumeCamStartEl-dy*.28))
	now := time.Now()
	if final || volumeCamLastPaint.IsZero() || now.Sub(volumeCamLastPaint) >= 30*time.Millisecond {
		volumeCamLastPaint = now
		volumeHoverAxis = -1
		volumeCrossValid = false
		invalidateVolumeBase()
		// Keep native controller HWNDs completely idle while rotating. Updating
		// the status control every 30 ms made the whole controller strip appear
		// to flash even though the 3-D canvas itself was double-buffered.
		if final {
			updateVolumeStatusLine(fmt.Sprintf("Camera: Az %.1f° | El %.1f° | %s", volumeCamAzimuth, volumeCamElevation, volumeProjectionName()))
		}
	}
}

func endVolumeCameraRotate(mx, my int) {
	if !volumeCamRotating {
		return
	}
	rotateVolumeCamera(mx, my, true)
	volumeCamRotating = false
	pReleaseCapture.Call()
	if c, _, _ := pLoadCursorW.Call(0, IDC_ARROW); c != 0 {
		pSetCursor.Call(c)
	}
}

func beginVolumeCameraPan(mx, my int) {
	if !volume3D || volumeFocusPanel >= 0 {
		return
	}
	volumeCamPanning = true
	volumeCamPanStartX, volumeCamPanStartY = mx, my
	volumeCamStartPanX, volumeCamStartPanY = volumeCamPanX, volumeCamPanY
	pSetCapture.Call(volumeHwnd)
}

func panVolumeCamera(mx, my int) {
	if !volumeCamPanning {
		return
	}
	volumeCamPanX = volumeCamStartPanX + float64(mx-volumeCamPanStartX)
	volumeCamPanY = volumeCamStartPanY + float64(my-volumeCamPanStartY)
	volumeHoverAxis = -1
	invalidateVolumeBase()
	updateVolumeStatusLine("Camera: Pan | Shift+Left")
}

func endVolumeCameraPan(mx, my int) {
	if !volumeCamPanning {
		return
	}
	panVolumeCamera(mx, my)
	volumeCamPanning = false
	pReleaseCapture.Call()
}

func beginVolumeCameraZoomDrag(my int) {
	if !volume3D || volumeFocusPanel >= 0 {
		return
	}
	volumeCamZooming = true
	volumeCamZoomStartY = my
	volumeCamStartZoom = volumeCamZoom
	pSetCapture.Call(volumeHwnd)
}

func dragVolumeCameraZoom(my int) {
	if !volumeCamZooming {
		return
	}
	dy := float64(my - volumeCamZoomStartY)
	volumeCamZoom = volumeCamStartZoom * math.Exp(-dy*.008)
	volumeCamZoom = math.Max(.55, math.Min(3.5, volumeCamZoom))
	invalidateVolumeBase()
	updateVolumeStatusLine(fmt.Sprintf("Camera: Zoom %.2fx | Right drag", volumeCamZoom))
}

func endVolumeCameraZoomDrag(my int) {
	if !volumeCamZooming {
		return
	}
	dragVolumeCameraZoom(my)
	volumeCamZooming = false
	pReleaseCapture.Call()
}

func zoomVolumeCamera(delta int) {
	if delta > 0 {
		volumeCamZoom *= 1.12
	} else if delta < 0 {
		volumeCamZoom /= 1.12
	}
	volumeCamZoom = math.Max(.55, math.Min(3.5, volumeCamZoom))
	invalidateVolumeBase()
	updateVolumeStatusLine(fmt.Sprintf("Camera: Az %.0f° | El %.0f° | Zoom %.2fx", volumeCamAzimuth, volumeCamElevation, volumeCamZoom))
}

func volumeWndProc(h uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case WM_DROPFILES:
		handleWorkspaceDrop(wParam, workspaceMode3D)
		return 0
	case WM_COMMAND:
		switch int(wParam & 0xffff) {
		case IDVOL_ILPREV:
			moveVolumeAxis(0, -1)
		case IDVOL_ILNEXT:
			moveVolumeAxis(0, 1)
		case IDVOL_XLPREV:
			moveVolumeAxis(1, -1)
		case IDVOL_XLNEXT:
			moveVolumeAxis(1, 1)
		case IDVOL_TPREV:
			moveVolumeAxis(2, -1)
		case IDVOL_TNEXT:
			moveVolumeAxis(2, 1)
		case IDVOL_PALETTE:
			notify := (wParam >> 16) & 0xffff
			if notify == 1 || notify == 9 {
				r, _, _ := pSendMessageW.Call(vc.palette, CB_GETCURSEL, 0, 0)
				if int(r) >= 0 && int(r) < len(volumePaletteOrder) {
					volumePaletteIndex = volumePaletteFromCombo(int(r))
					refreshVolumePaletteOnly()
					updateVolumeStatusLine("色标: " + paletteNames[volumePaletteIndex])
				}
			}
		case IDVOL_SIZE:
			notify := (wParam >> 16) & 0xffff
			if notify == 1 || notify == 9 {
				r, _, _ := pSendMessageW.Call(vc.size, CB_GETCURSEL, 0, 0)
				if int(r) >= 0 && int(r) <= 4 {
					volumeViewSizeMode = int(r)
					volumeCamZoom = 1.0
					invalidateVolumeBase()
					updateVolumeStatusLine("视图大小: " + []string{"紧凑", "标准", "大", "超大", "填充"}[volumeViewSizeMode])
				}
			}
		case IDVOL_ASPECT:
			notify := (wParam >> 16) & 0xffff
			if notify == 1 || notify == 9 {
				r, _, _ := pSendMessageW.Call(vc.aspect, CB_GETCURSEL, 0, 0)
				if int(r) >= 0 && int(r) <= 2 {
					volumeAspectMode = int(r)
					volumeCamZoom = 1.0
					invalidateVolumeBase()
					switch volumeAspectMode {
					case 0:
						updateVolumeStatusLine("比例: 实际（IL:XL 严格按当前数据真实范围）")
					case 1:
						updateVolumeStatusLine("比例: 均衡 1:1")
					default:
						updateVolumeStatusLine("比例: 默认（使用上次“设为默认”保存的显示比例）")
					}
				}
			}
		case IDVOL_STYLE:
			notify := (wParam >> 16) & 0xffff
			if notify == 1 || notify == 9 {
				r, _, _ := pSendMessageW.Call(vc.style, CB_GETCURSEL, 0, 0)
				if int(r) >= 0 && int(r) < len(volumeStyleComboOrder) {
					volumeStyleMode = volumeStyleFromCombo(int(r))
					applyVolumeStyleMode()
					updateVolumeStyleStatus()
				}
			}
		case IDVOL_COORDS:
			volumeShowCoordinates = isChecked(vc.coords)
			invalidateVolumeBase()
			if volumeShowCoordinates {
				updateVolumeStatusLine("坐标显示: ON")
			} else {
				updateVolumeStatusLine("坐标显示: OFF")
			}
		case IDVOL_AXIS_IL, IDVOL_AXIS_XL, IDVOL_AXIS_Z:
			notify := (wParam >> 16) & 0xffff
			if notify == 1 || notify == 9 {
				var hctrl uintptr
				axis := 0
				switch int(wParam & 0xffff) {
				case IDVOL_AXIS_IL:
					hctrl = vc.axisIL
					axis = 0
				case IDVOL_AXIS_XL:
					hctrl = vc.axisXL
					axis = 1
				default:
					hctrl = vc.axisZ
					axis = 2
				}
				r, _, _ := pSendMessageW.Call(hctrl, CB_GETCURSEL, 0, 0)
				if int(r) >= 0 && int(r) < len(volumeAxisFactorOptions) {
					v := volumeAxisFactorOptions[int(r)]
					switch axis {
					case 0:
						volumeAxisILFactor = v
					case 1:
						volumeAxisXLFactor = v
					default:
						volumeAxisZFactor = v
					}
					invalidateVolumeBase()
					updateVolumeStatusLine(fmt.Sprintf("轴长: IL×%.2f  XL×%.2f  Z×%.2f", volumeAxisILFactor, volumeAxisXLFactor, volumeAxisZFactor))
				}
			}
		case IDVOL_FOV:
			notify := (wParam >> 16) & 0xffff
			if notify == 1 || notify == 9 {
				r, _, _ := pSendMessageW.Call(vc.fov, CB_GETCURSEL, 0, 0)
				if int(r) >= 0 && int(r) < len(volumeFOVOptions) {
					volumeCamFOV = volumeFOVOptions[int(r)]
					invalidateVolumeBase()
					updateVolumeStatusLine("Projection: " + volumeProjectionName())
				}
			}
		case IDVOL_DEFAULT:
			if err := saveVolumeViewDefaults(); err != nil {
				message(volumeHwnd, "保存默认视图", "保存失败："+err.Error(), MB_OK|MB_ICONERROR)
			} else {
				updateVolumeStatusLine("已设为默认：比例 / 轴长 / FOV / 相机 / 风格 / 色标 / 坐标显示均已保存")
			}
		case IDVOL_HELP:
			message(volumeHwnd, "3D View 帮助", volumeShortcutHelpText(), MB_OK|MB_ICONINFORMATION)
		case IDVOL_COMPARE:
			startVolumeCompare()
		case IDVOL_MATCHLEFT:
			matchVolumeRightToLeft()
		case IDVOL_DIFF:
			if !volumeCompareMode || !volumeScenes[1].valid {
				pSendMessageW.Call(vc.diff, BM_SETCHECK, 0, 0)
				message(volumeHwnd, "三维残差", "请先点击“比”加载 B 数据。", MB_OK|MB_ICONINFORMATION)
				break
			}
			volumeShowDiff = isChecked(vc.diff)
			volumeResidualKey = ""
			destroyVolumeBase()
			invalidateVolumeBase()
			if volumeShowDiff {
				updateVolumeStatusLine("A/B/差 三维 QC | 差=A-B，位置与视角跟随左图")
			} else {
				updateVolumeStatusLine("A/B 3D 对比 | 左右视图独立")
			}
		case IDVOL_EXPORT:
			showVolumeExportDialog()
		case IDVOL_AUTO:
			startVolumePrepare()
		case IDVOL_RESET:
			if volumeG != nil && volumeF != nil {
				resetVolumeCamera()
				volumeIL = volumeG.InlineValues[len(volumeG.InlineValues)/2]
				volumeXL = volumeG.CrosslineValues[len(volumeG.CrosslineValues)/2]
				volumeSample = volumeSampleMin + (volumeSampleMax-volumeSampleMin)/2
				volumeActiveAxis = 2
				loadVolumeTimeSlice(volumeSample)
				renderVolumeInline()
				renderVolumeCrossline()
				updateVolumeControls()
				volumeCrossIL, volumeCrossXL, volumeCrossSample = float64(volumeIL), float64(volumeXL), float64(volumeSample)
				volumeCrossValid = true
				invalidateVolumeBase()
			}
		case IDVOL_VIEW:
			setVolumeTraceInspectMode(false)
			pEnableWindow.Call(vc.trace, 0)
			if volumeFocusPanel >= 0 {
				volumeFocusPanel = -1
			}
			volume3D = !volume3D
			if volume3D {
				setText(vc.view, "平铺")
				updateVolumeStatusLine("3D View | " + volumeStyleName() + " | " + volumeInteractionName())
			} else {
				setText(vc.view, "3D")
				updateVolumeStatusLine("平铺模式：Inline / Crossline / Time Slice 三视图联动")
			}
			destroyVolumeBase()
			invalidateVolumeBase()
		case IDVOL_TRACE:
			setVolumeTraceInspectMode(!volumeTraceInspectMode)
		case IDVOL_MIN:
			volumeStyleMode = (volumeStyleMode + 1) % 4
			applyVolumeStyleMode()
			if vc.style != 0 {
				pSendMessageW.Call(vc.style, CB_SETCURSEL, uintptr(volumeStyleComboIndex(volumeStyleMode)), 0)
			}
			updateVolumeStyleStatus()
		case IDVOL_CLOSE:
			pDestroyWindow.Call(h)
		}
		return 0
	case WM_MOUSEMOVE:
		mx, my := mousePoint(lParam)
		if volumeCamPanning {
			panVolumeCamera(mx, my)
			return 0
		}
		if volumeCamZooming {
			dragVolumeCameraZoom(my)
			return 0
		}
		if volumeCamRotating {
			if c, _, _ := pLoadCursorW.Call(0, IDC_SIZEALL); c != 0 {
				pSetCursor.Call(c)
			}
			rotateVolumeCamera(mx, my, false)
			return 0
		}
		if volumeDragAxis >= 0 {
			setVolumeDragCursor(volumeDragAxis)
			dragVolumePlane(mx, my, false)
			return 0
		}

		if volumeInteractionMode == 0 {
			// CIGVis semantics: hovering/selecting a visual node is relevant while
			// Ctrl is held or D drag-mode is enabled. Ordinary motion stays clean.
			ctrl := (wParam & 0x0008) != 0
			if ctrl || volumeDragMode {
				if axis := volumePanelAtPoint(mx, my); axis >= 0 {
					if volumeHoverAxis != axis {
						volumeHoverAxis = axis
						invalidateVolumeBase()
					}
					setVolumeDragCursor(axis)
					updateVolumeStatusLine("Slice: " + []string{"Inline", "Crossline", "Time"}[axis] + " | Ctrl+Left drag")
					return 0
				}
			}
		} else {
			// Classic Limage semantics: edge proximity advertises direct dragging.
			if axis := volumeDragAxisAtPoint(mx, my); axis >= 0 {
				if volumeHoverAxis != axis {
					volumeHoverAxis = axis
					invalidateVolumeBase()
				}
				setVolumeDragCursor(axis)
				if volumeCrossValid {
					invalidateVolumeCrosshair(volumeCrossIL, volumeCrossXL, volumeCrossSample)
					volumeCrossValid = false
				}
				updateVolumeStatusLine("Hover: " + []string{"Inline", "Crossline", "Time Slice"}[axis] + "（拖动边缘移动切面）")
				return 0
			}
		}
		if volumeHoverAxis != -1 {
			volumeHoverAxis = -1
			invalidateVolumeBase()
		}
		setVolumeDragCursor(-1)
		oldValid, oi, ox, os := volumeCrossValid, volumeCrossIL, volumeCrossXL, volumeCrossSample
		updateVolumeCrosshair(mx, my)
		if oldValid {
			invalidateVolumeCrosshair(oi, ox, os)
		}
		if volumeCrossValid {
			invalidateVolumeCrosshair(volumeCrossIL, volumeCrossXL, volumeCrossSample)
		}
		return 0
	case WM_RBUTTONDOWN:
		mx, my := mousePoint(lParam)
		if volumeCompareMode {
			activateVolumeScene(volumeSceneSideAtPoint(mx, my))
		}
		if volumeInteractionMode == 0 {
			beginVolumeCameraZoomDrag(my)
		} else {
			beginVolumeCameraRotate(mx, my)
		}
		return 0
	case WM_RBUTTONUP:
		mx, my := mousePoint(lParam)
		if volumeCamZooming {
			endVolumeCameraZoomDrag(my)
			return 0
		}
		if volumeCamRotating {
			endVolumeCameraRotate(mx, my)
			return 0
		}
	case WM_MOUSEWHEEL:
		if volume3D && volumeFocusPanel < 0 {
			if volumeCompareMode {
				var pt POINT
				if ok, _, _ := pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt))); ok != 0 {
					pScreenToClient.Call(volumeHwnd, uintptr(unsafe.Pointer(&pt)))
					activateVolumeScene(volumeSceneSideAtPoint(int(pt.X), int(pt.Y)))
				}
			}
			delta := int(int16((wParam >> 16) & 0xffff))
			zoomVolumeCamera(delta)
			return 0
		}
	case WM_LBUTTONDBLCLK:
		mx, my := mousePoint(lParam)
		if volumeCompareMode && volumeFocusPanel < 0 {
			activateVolumeScene(volumeSceneSideAtPoint(mx, my))
		}
		if volumeFocusPanel >= 0 {
			setVolumeTraceInspectMode(false)
			pEnableWindow.Call(vc.trace, 0)
			volumeFocusPanel = -1
			volumeHoverAxis = -1
			updateVolumeStatusLine("3D View | " + volumeStyleName())
			destroyVolumeBase()
			invalidateVolumeBase()
			return 0
		}
		if volume3D {
			if panel := volumePanelAtPoint(mx, my); panel >= 0 {
				ctrl := (wParam & 0x0008) != 0
				if ctrl {
					// Explicit relocation only. This prevents a normal double-click used
					// for opening 2-D from unexpectedly moving the 3-D intersection.
					applyVolumeClick(panel, mx, my)
					updateVolumeStatusLine("3D View | Ctrl+双击：三维交点已重新定位")
					return 0
				}
				volumeFocusPanel = panel
				volumeActiveAxis = panel
				volumeHoverAxis = -1
				setVolumeTraceInspectMode(false)
				pEnableWindow.Call(vc.trace, 1)
				updateVolumeStatusLine("2D Full View | 风格: " + volumeStyleName() + " | “道”可连续选择单道 | 双击或 Esc 返回 3D")
				destroyVolumeBase()
				invalidateVolumeBase()
				return 0
			}
		}
	case WM_KEYDOWN:
		// Most 3-D shortcuts are routed in the application message loop so they
		// remain active even when a combo box or button owns keyboard focus.
		if wParam == VK_ESCAPE && volumeTraceInspectMode {
			setVolumeTraceInspectMode(false)
			updateVolumeStatusLine("2D Full View | 已退出单道分析模式 | 双击或 Esc 返回 3D")
			return 0
		}
		if wParam == VK_ESCAPE && volumeFocusPanel >= 0 {
			setVolumeTraceInspectMode(false)
			pEnableWindow.Call(vc.trace, 0)
			volumeFocusPanel = -1
			volumeHoverAxis = -1
			updateVolumeStatusLine("3D View | " + volumeStyleName())
			destroyVolumeBase()
			invalidateVolumeBase()
			return 0
		}

	case WM_LBUTTONDOWN:
		mx, my := mousePoint(lParam)
		if volumeTraceInspectMode && volumeFocusPanel >= 0 {
			selection, err := volumeTraceAnalysisSelectionAt(mx, my)
			if err != nil {
				updateVolumeStatusLine("单道分析：" + err.Error())
			} else {
				showTraceAnalysisSelection(selection)
				updateVolumeStatusLine(selection.Context + " | 分析窗口已刷新")
			}
			return 0
		}
		if volumeCompareMode {
			activateVolumeScene(volumeSceneSideAtPoint(mx, my))
		}
		if volumeInteractionMode == 0 {
			shift := (wParam & 0x0004) != 0
			ctrl := (wParam & 0x0008) != 0
			if shift {
				beginVolumeCameraPan(mx, my)
				return 0
			}
			if ctrl || volumeDragMode {
				if axis := volumePanelAtPoint(mx, my); axis >= 0 {
					beginVolumePlaneDrag(axis, mx, my)
					return 0
				}
			}
			beginVolumeCameraRotate(mx, my)
			return 0
		}
		// Classic interaction keeps the original edge-drag/click workflow.
		if axis := volumeDragAxisAtPoint(mx, my); axis >= 0 {
			beginVolumePlaneDrag(axis, mx, my)
			return 0
		}
		if panel := volumePanelAtPoint(mx, my); panel >= 0 {
			// Normal click only selects. Repositioning is explicitly reserved for
			// Ctrl+double-click so opening 2-D cannot move the 3-D intersection.
			volumeActiveAxis = panel
			volumeHoverAxis = -1
			invalidateVolumeBase()
			return 0
		}
	case WM_LBUTTONUP:
		mx, my := mousePoint(lParam)
		if volumeCamPanning {
			endVolumeCameraPan(mx, my)
			return 0
		}
		if volumeDragAxis >= 0 {
			endVolumePlaneDrag(mx, my)
			return 0
		}
		if volumeCamRotating {
			moved := volumeCamMoved
			endVolumeCameraRotate(mx, my)
			// A plain click only selects the plane. Repositioning the orthoslice
			// intersection is intentionally reserved for Ctrl+double-click.
			if volumeInteractionMode == 0 && !moved {
				if panel := volumePanelAtPoint(mx, my); panel >= 0 {
					volumeActiveAxis = panel
					volumeHoverAxis = -1
					invalidateVolumeBase()
				}
			}
			return 0
		}
	case WM_VOLUME_READY:
		handleVolumeReady()
		return 0
	case WM_VOLUME_INDEX_PROGRESS:
		handleVolumeIndexProgress(int(wParam), int64(lParam))
		return 0
	case WM_VOLUME_EXPORT_DONE:
		handleVolumeExportDone()
		return 0
	case WM_VOLUME_EXPORT_PROGRESS:
		updateVolumeStatusLine(fmt.Sprintf("导出三维子体... %d%%", int(wParam)))
		return 0
	case WM_SIZE:
		destroyVolumeBase()
		invalidateVolumeBase()
		layoutVolumeIndexProgress()
		return 0
	case WM_GETMINMAXINFO:
		if lParam != 0 {
			mmi := (*MINMAXINFO)(unsafe.Pointer(lParam))
			mmi.PtMinTrackSize.X = 920
			mmi.PtMinTrackSize.Y = 620
		}
		return 0
	case WM_SETCURSOR:
		if atomic.LoadInt32(&volumeBusy) > 0 {
			c, _, _ := pLoadCursorW.Call(0, IDC_WAIT)
			if c != 0 {
				pSetCursor.Call(c)
			}
			return 1
		}
		if volumeCamRotating || volumeCamPanning {
			if c, _, _ := pLoadCursorW.Call(0, IDC_SIZEALL); c != 0 {
				pSetCursor.Call(c)
			}
			return 1
		}
		if volumeCamZooming {
			if c, _, _ := pLoadCursorW.Call(0, IDC_SIZENS); c != 0 {
				pSetCursor.Call(c)
			}
			return 1
		}
		if volumeDragAxis >= 0 {
			setVolumeDragCursor(volumeDragAxis)
			return 1
		}
		if volume3D {
			var pt POINT
			if ok, _, _ := pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt))); ok != 0 {
				pScreenToClient.Call(volumeHwnd, uintptr(unsafe.Pointer(&pt)))
				if axis := volumeDragAxisAtPoint(int(pt.X), int(pt.Y)); axis >= 0 {
					setVolumeDragCursor(axis)
					return 1
				}
			}
		}
	case WM_ERASEBKGND:
		return 1
	case WM_PAINT:
		paintVolume()
		return 0
	case WM_CLOSE:
		setVolumeTraceInspectMode(false)
		revokeOleSegyDropTarget(h)
		pDestroyWindow.Call(h)
		return 0
	case WM_DESTROY:
		revokeOleSegyDropTarget(h)
		atomic.AddInt64(&volumeGen, 1)
		saveActiveVolumeScene()
		seen := map[*segy.File]bool{}
		for i := 0; i < 2; i++ {
			if f := volumeScenes[i].f; f != nil && !seen[f] {
				seen[f] = true
				f.Close()
			}
		}
		volumeF, volumeG, volumeC = nil, nil, nil
		volumeScenes = [2]volumeSceneState{}
		volumeCompareMode = false
		volumeShowDiff = false
		volumeResidualInline, volumeResidualCrossline, volumeResidualTime = comparePanel{}, comparePanel{}, comparePanel{}
		volumeResidualKey, volumeResidualNote = "", ""
		volumeActiveSide = 0
		destroyVolumeBase()
		volumeHwnd = 0
		vc = volumeControls{}
		volumeInline, volumeCrossline, volumeTime = comparePanel{}, comparePanel{}, comparePanel{}
		volumeInlineValues, volumeCrosslineValues = nil, nil
		volumeTimeVals, volumeTimeRaster, volumeTimeMask = nil, nil, nil
		volumeTimeMin, volumeTimeMax = -1, 1
		volumeCrossValid = false
		// A 2-D Full View belongs only to the lifetime of this Volume window.
		// Reset it on close as well as on open so Home -> 3D can never inherit it.
		volumeFocusPanel = -1
		volumeTraceInspectMode = false
		volumeHoverAxis = -1
		volume3D = true
		volumeForce3DOnReady = false
		volumeDragAxis = -1
		volumeCamRotating = false
		volumeCamPanning = false
		volumeCamZooming = false
		volumeIndexProgressVisible = false
		volumeIndexProgressGen = 0
		atomic.StoreInt32(&volumeBusy, 0)
		if volumeHidCompare && compareHwnd != 0 {
			pShowWindow.Call(compareHwnd, SW_SHOW)
			pUpdateWindow.Call(compareHwnd)
			pSetForeground.Call(compareHwnd)
		}
		volumeHidCompare = false
		phase1NotifyVolumeClosed()
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(h, uintptr(msg), wParam, lParam)
	return r
}

// Keep syscall imported in this Windows-only file because volumeWndProc is
// registered with syscall.NewCallback from main_windows.go.
var _ = syscall.NewCallback
