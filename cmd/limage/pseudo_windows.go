//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	geometrycore "github.com/DavidLiu-code/SeisForge-Studio/internal/geometry"
	projectcore "github.com/DavidLiu-code/SeisForge-Studio/internal/project"
	pseudo3dcore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudo3d"
	pseudocachecore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudocache"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	workspacecore "github.com/DavidLiu-code/SeisForge-Studio/internal/workspace"
)

const (
	IDPSEUDO_CLOSE            = 7201
	IDPSEUDO_ALL              = 7202
	IDPSEUDO_NONE             = 7203
	IDPSEUDO_CURRENT          = 7204
	IDPSEUDO_RESET            = 7205
	IDPSEUDO_PALETTE          = 7206
	IDPSEUDO_GAINMINUS        = 7207
	IDPSEUDO_GAINPLUS         = 7208
	IDPSEUDO_FOV              = 7209
	IDPSEUDO_ZMINUS           = 7210
	IDPSEUDO_ZPLUS            = 7211
	IDPSEUDO_LINES            = 7212
	IDPSEUDO_STATUS           = 7213
	IDPSEUDO_DEFAULT          = 7214
	IDPSEUDO_HELP             = 7215
	IDPSEUDO_AXISX            = 7216
	IDPSEUDO_AXISY            = 7217
	IDPSEUDO_AXISZ            = 7218
	IDPSEUDO_STYLE            = 7219
	IDPSEUDO_PROGRESS         = 7220
	WM_PSEUDO_LOAD_READY      = WM_USER + 451
	WM_PSEUDO_SCENE_READY     = WM_USER + 452
	WM_PSEUDO_PROGRESS_RENDER = WM_USER + 453
	WM_PSEUDO_LOAD_STAGE      = WM_USER + 454
)

const (
	pseudoMKShift       = 0x0004
	pseudoMKControl     = 0x0008
	pseudoPBMSetState   = WM_USER + 16
	pseudoPBSTNormal    = 1
	pseudoPBSTError     = 2
	pseudoSSCenter      = 0x0001
	pseudoDragThreshold = 5
)

const (
	LVM_FIRST                    = 0x1000
	LVM_SETITEMSTATE             = LVM_FIRST + 43
	LVM_GETITEMSTATE             = LVM_FIRST + 44
	LVM_DELETEALLITEMS           = LVM_FIRST + 9
	LVM_SETEXTENDEDLISTVIEWSTYLE = LVM_FIRST + 54
	LVM_INSERTITEMW              = LVM_FIRST + 77
	LVM_SETITEMW                 = LVM_FIRST + 76
	LVM_INSERTCOLUMNW            = LVM_FIRST + 97
	LVS_REPORT                   = 0x0001
	LVS_SINGLESEL                = 0x0004
	LVS_SHOWSELALWAYS            = 0x0008
	LVS_EX_CHECKBOXES            = 0x00000004
	LVS_EX_FULLROWSELECT         = 0x00000020
	LVIF_TEXT                    = 0x0001
	LVIF_STATE                   = 0x0008
	LVIS_SELECTED                = 0x0002
	LVIS_STATEIMAGEMASK          = 0xF000
	LVCF_FMT                     = 0x0001
	LVCF_WIDTH                   = 0x0002
	LVCF_TEXT                    = 0x0004
	LVCF_SUBITEM                 = 0x0008
	LVCFMT_LEFT                  = 0
	LVN_FIRST                    = -100
	LVN_ITEMCHANGED              = LVN_FIRST - 1
)

type pseudoLVColumn struct {
	Mask       uint32
	Fmt        int32
	Cx         int32
	PszText    *uint16
	CchTextMax int32
	ISubItem   int32
	IImage     int32
	IOrder     int32
}

type pseudoLVItem struct {
	Mask                  uint32
	IItem, ISubItem       int32
	State, StateMask      uint32
	PszText               *uint16
	CchTextMax, IImage    int32
	LParam                uintptr
	IIndent, IGroupID     int32
	CColumns              uint32
	PuColumns             uintptr
	PiColFmt, IGroupIndex uintptr
}

type pseudoNMListView struct {
	Hdr                  NMHDR
	IItem, ISubItem      int32
	UNewState, UOldState uint32
	UChanged             uint32
	PtAction             POINT
	LParam               uintptr
}

type pseudoControls struct {
	close, all, none, current, reset, saveDefault, help, exportRange uintptr
	palette, gainMinus, gainValue, gainPlus                          uintptr
	fov, zMinus, zValue, zPlus, axisX, axisY, axisZ                  uintptr
	styleLabel, style, lines, status, progressStatus                 uintptr
	progress                                                         uintptr
}

type pseudoViewDefaults struct {
	Version       int     `json:"version"`
	Azimuth       float64 `json:"azimuth"`
	Elevation     float64 `json:"elevation"`
	FOV           float64 `json:"fov"`
	Zoom          float64 `json:"zoom"`
	PanX          float64 `json:"pan_x"`
	PanY          float64 `json:"pan_y"`
	AxisXScale    float64 `json:"axis_x_scale,omitempty"`
	AxisYScale    float64 `json:"axis_y_scale,omitempty"`
	AxisZScale    float64 `json:"axis_z_scale,omitempty"`
	VerticalScale float64 `json:"vertical_scale,omitempty"`
}

type pseudoCurtainSegment struct {
	points                             []pseudo3dcore.Point
	u                                  []float64
	texture                            *pseudo3dcore.Texture
	length, positionStart, positionEnd float64
	traceIndices                       []int64
	positions                          []float64
	sampleStart, sampleEnd             int
	timeStartMS, timeEndMS             float64
	preview                            bool
}

type pseudoCurtainRef struct {
	lineIndex, segmentIndex int
}

type pseudoRangeHandle uint8

const (
	pseudoRangeXMin pseudoRangeHandle = 1 << iota
	pseudoRangeXMax
	pseudoRangeYMin
	pseudoRangeYMax
)

type pseudoTimeHandle uint8

const (
	pseudoTimeStart pseudoTimeHandle = 1 << iota
	pseudoTimeEnd
)

type pseudoLineState struct {
	line                 *projectcore.CrookedProjectLine
	geometry             *geometrycore.CrookedLineGeometry
	points               []pseudo3dcore.Point
	u                    []float64
	texture              *pseudo3dcore.Texture
	segments             []pseudoCurtainSegment
	size                 pseudo3dcore.TextureSize
	selected             bool
	inRange              bool
	eligibilityKnown     bool
	loading              bool
	ready                bool
	errorText            string
	token                int64
	multiplier           float64
	timeMaxMS            float64
	timeStartMS          float64
	timeEndMS            float64
	sampleStart          int
	sampleEnd            int
	timeInRange          bool
	estimatedRangeLength float64
	geometryCalibrated   bool
	geometryFingerprint  string
	canonicalUnavailable bool
	cachedSegments       []pseudoCurtainSegment
	cacheStamp           int64
}

type pseudoSession struct {
	project                 *projectcore.CrookedProject
	workspaceGeneration     uint64
	windowGeneration        int64
	lines                   []pseudoLineState
	activeLine              int
	camera                  pseudo3dcore.Camera
	paletteIndex            int
	gainPercent             float64
	spec                    segy.TraceCoordinateSpec
	image                   []byte
	imageWidth              int
	imageHeight             int
	renderStats             pseudo3dcore.RenderStats
	loadQueue               chan pseudoLoadJob
	renderQueue             chan pseudoRenderRequest
	cancel                  chan struct{}
	rotating                bool
	panning                 bool
	leftDragPending         bool
	leftDragPan             bool
	dragStartX              int
	dragStartY              int
	dragCamera              pseudo3dcore.Camera
	lastRenderRequest       time.Time
	projectMultiplier       float64
	deferredLinesQueued     bool
	spatialRange            pseudo3dcore.XYRange
	rangeGeneration         int64
	timeRange               pseudo3dcore.TimeRange
	timeRangeGeneration     int64
	zooming                 bool
	renderedScene           pseudo3dcore.Scene
	renderedCamera          pseudo3dcore.Camera
	renderedRefs            []pseudoCurtainRef
	hoverPick               pseudo3dcore.PickResult
	hoverValid              bool
	lastHoverUpdate         time.Time
	rangeDragging           bool
	rangeHandle             pseudoRangeHandle
	rangeHoverHandle        pseudoRangeHandle
	rangePlane              float64
	rangeOriginal           pseudo3dcore.XYRange
	rangeDraft              pseudo3dcore.XYRange
	rangeLastPaint          time.Time
	timeDragging            bool
	timeHandle              pseudoTimeHandle
	timeHoverHandle         pseudoTimeHandle
	timeOriginal            pseudo3dcore.TimeRange
	timeDraft               pseudo3dcore.TimeRange
	timeLastPaint           time.Time
	timeDragStartX          int
	timeDragStartY          int
	timeDragVectorX         float64
	timeDragVectorY         float64
	restoreSelections       map[string]bool
	linkedCursor            pseudo3dcore.LinkedCursor
	linkedCursorValid       bool
	linkedCursorOutside     bool
	linkedCursorTimeOutside bool
	rangePreviewActive      bool
	timePreviewActive       bool
	rangeReusedLines        int
	rangeUpdatingLines      int
	cacheClock              int64
	textureCache            *pseudocachecore.Cache
	cacheHits               int
	ioReadCalls             int
	ioReadBytes             int64
	cacheWrites             []pseudoCacheWrite
	cacheWriteDigests       map[string]bool
	cacheWriteQueue         chan []pseudoCacheWrite
	progressRenderArmed     bool
	progressRenderPending   bool
	progressRenderGen       int64
	lastProgressRender      time.Time
	finalRenderPending      bool
	finalRenderComplete     bool
	loadStarted             time.Time
	loadCompletedDuration   time.Duration
	loadStage               int
	inputTraceCount         int64
	supportTraceCount       int64
	ioLogicalBytes          int64
	ioDecodeNanos           int64
	decodedSampleCount      int64
	mappedSegments          int
	fallbackSegments        int
}

type pseudoLoadJob struct {
	windowGen            int64
	lineIndex            int
	token                int64
	line                 *projectcore.CrookedProjectLine
	geometry             *geometrycore.CrookedLineGeometry
	geometryCalibrated   bool
	geometryFingerprint  string
	canonicalUnavailable bool
	existingMultiplier   float64
	projectMultiplier    float64
	spec                 segy.TraceCoordinateSpec
	size                 pseudo3dcore.TextureSize
	gain                 float64
	agc                  bool
	clip                 float64
	displayMode          segy.DisplayMode
	readStrategy         segy.RenderReadStrategy
	spatialRange         pseudo3dcore.XYRange
	rangeGeneration      int64
	timeRange            pseudo3dcore.TimeRange
	timeRangeGeneration  int64
	sampleStart          int
	sampleEnd            int
	timeStartMS          float64
	timeEndMS            float64
	reusableSegments     []pseudoCurtainSegment
	textureCache         *pseudocachecore.Cache
	skipPersistentCache  bool
}

type pseudoLoadResult struct {
	windowGen           int64
	lineIndex           int
	token               int64
	geometry            *geometrycore.CrookedLineGeometry
	geometryCalibrated  bool
	geometryFingerprint string
	points              []pseudo3dcore.Point
	u                   []float64
	texture             *pseudo3dcore.Texture
	segments            []pseudoCurtainSegment
	outOfRange          bool
	rangeGeneration     int64
	timeRangeGeneration int64
	stale               bool
	multiplier          float64
	calibrationAccepted bool
	cacheHit            bool
	previewOnly         bool
	ioReadCalls         int
	ioReadBytes         int64
	inputTraceCount     int
	supportTraceCount   int
	ioLogicalBytes      int64
	ioDecodeNanos       int64
	decodedSampleCount  int64
	mappedSegments      int
	fallbackSegments    int
	cacheWrite          *pseudoCacheWrite
	err                 error
}

type pseudoRenderRequest struct {
	windowGen          int64
	renderGen          int64
	scene              pseudo3dcore.Scene
	camera             pseudo3dcore.Camera
	palette            [256]pseudo3dcore.Color
	width              int
	height             int
	refs               []pseudoCurtainRef
	preview            bool
	final              bool
	refineAfterPreview bool
}

type pseudoRenderResult struct {
	windowGen          int64
	renderGen          int64
	image              []byte
	width              int
	height             int
	stats              pseudo3dcore.RenderStats
	scene              pseudo3dcore.Scene
	camera             pseudo3dcore.Camera
	refs               []pseudoCurtainRef
	preview            bool
	final              bool
	refineAfterPreview bool
	err                error
}

var (
	pseudoHwnd           uintptr
	pseudoControlsUI     pseudoControls
	pseudoState          pseudoSession
	pseudoDefaults       pseudoViewDefaults
	pseudoDefaultsLoaded bool
	pseudoWindowGen      int64
	pseudoRenderGen      int64
	pseudoActiveRangeGen int64
	pseudoActiveTimeGen  int64
	pseudoPendingMu      sync.Mutex
	pseudoLoadPending    []*pseudoLoadResult
	pseudoRenderPending  *pseudoRenderResult
	pseudoListUpdating   bool
)

var pseudoFOVOptions = []float64{0, 5, 10, 15, 20, 25, 30, 35, 40, 45, 50, 55, 60, 70, 80, 90, 100, 110, 120}
var pseudoAxisFactorOptions = []float64{0.10, 0.15, 0.20, 0.25, 0.33, 0.40, 0.50, 0.67, 0.75, 0.85, 1.00, 1.10, 1.25, 1.50, 2.00, 2.50, 3.00, 4.00, 5.00, 6.00, 8.00}

func defaultPseudoView() pseudoViewDefaults {
	camera := pseudo3dcore.DefaultCamera()
	return pseudoViewDefaults{Version: 2, Azimuth: camera.Azimuth, Elevation: camera.Elevation, FOV: camera.FOV, Zoom: camera.Zoom,
		AxisXScale: camera.AxisXScale, AxisYScale: camera.AxisYScale, AxisZScale: camera.VerticalScale}
}

func pseudoDefaultsPath() string { return licenseDirectory() + `\pseudo_view.json` }

func loadPseudoDefaults() {
	if pseudoDefaultsLoaded {
		return
	}
	pseudoDefaultsLoaded = true
	pseudoDefaults = defaultPseudoView()
	if data, err := os.ReadFile(pseudoDefaultsPath()); err == nil {
		var loaded pseudoViewDefaults
		if json.Unmarshal(data, &loaded) == nil && (loaded.Version == 1 || loaded.Version == 2) {
			pseudoDefaults = normalizePseudoViewDefaults(loaded)
		}
	}
	pseudoDefaults = normalizePseudoViewDefaults(pseudoDefaults)
}

func normalizePseudoViewDefaults(loaded pseudoViewDefaults) pseudoViewDefaults {
	if loaded.Version == 1 {
		loaded.AxisXScale, loaded.AxisYScale, loaded.AxisZScale = 1, 1, loaded.VerticalScale
	}
	camera := pseudo3dcore.Camera{Azimuth: loaded.Azimuth, Elevation: loaded.Elevation, FOV: loaded.FOV,
		Zoom: loaded.Zoom, PanX: loaded.PanX, PanY: loaded.PanY, AxisXScale: loaded.AxisXScale,
		AxisYScale: loaded.AxisYScale, VerticalScale: loaded.AxisZScale}.Normalized()
	return pseudoViewDefaults{Version: 2, Azimuth: camera.Azimuth, Elevation: camera.Elevation, FOV: camera.FOV,
		Zoom: camera.Zoom, PanX: camera.PanX, PanY: camera.PanY, AxisXScale: camera.AxisXScale, AxisYScale: camera.AxisYScale, AxisZScale: camera.VerticalScale}
}

func savePseudoDefaults() {
	camera := pseudoState.camera.Normalized()
	pseudoDefaults = pseudoViewDefaults{Version: 2, Azimuth: camera.Azimuth, Elevation: camera.Elevation, FOV: camera.FOV,
		Zoom: camera.Zoom, PanX: camera.PanX, PanY: camera.PanY, AxisXScale: camera.AxisXScale, AxisYScale: camera.AxisYScale, AxisZScale: camera.VerticalScale}
	if err := os.MkdirAll(licenseDirectory(), 0o700); err != nil {
		return
	}
	if data, err := json.MarshalIndent(pseudoDefaults, "", "  "); err == nil {
		_ = os.WriteFile(pseudoDefaultsPath(), append(data, '\n'), 0o600)
	}
}

func pseudoCameraFromDefaults() pseudo3dcore.Camera {
	loadPseudoDefaults()
	return pseudo3dcore.Camera{Azimuth: pseudoDefaults.Azimuth, Elevation: pseudoDefaults.Elevation, FOV: pseudoDefaults.FOV,
		Zoom: pseudoDefaults.Zoom, PanX: pseudoDefaults.PanX, PanY: pseudoDefaults.PanY, AxisXScale: pseudoDefaults.AxisXScale,
		AxisYScale: pseudoDefaults.AxisYScale, VerticalScale: pseudoDefaults.AxisZScale}.Normalized()
}

func showPseudoWindow() {
	if crookedState.project == nil || crookedState.project.ValidLineCount() == 0 {
		message(crookedHwnd, "伪三维", "请先打开一个或多个二维弯线 SEG-Y。", MB_OK|MB_ICONINFORMATION)
		return
	}
	if pseudoHwnd != 0 && pseudoState.project == crookedState.project && pseudoState.rangeGeneration == crookedState.rangeGeneration && pseudoState.timeRangeGeneration == crookedState.timeRangeGeneration {
		showTopLevelWindowRestored(pseudoHwnd)
		return
	}
	closePseudoWindowForProjectChange()
	loadPseudoDefaults()
	h, _, _ := pCreateWindowExW.Call(WS_EX_APPWINDOW, uintptr(unsafe.Pointer(u16("Limage64Pseudo3D"))), uintptr(unsafe.Pointer(u16(APP_NAME+" v"+APP_VERSION+" [x64] - 弯线伪三维幕布"))),
		WS_OVERLAPPEDWINDOW|WS_CLIPCHILDREN, uintptr(CW_USEDEFAULT), uintptr(CW_USEDEFAULT), 1500, 920, 0, 0, 0, 0)
	if h == 0 {
		message(crookedHwnd, "伪三维", "无法创建伪三维窗口。", MB_OK|MB_ICONERROR)
		return
	}
	pseudoHwnd = h
	gen := atomic.AddInt64(&pseudoWindowGen, 1)
	textureCache, _ := pseudocachecore.New("", pseudocachecore.DefaultMaxBytes)
	pseudoState = pseudoSession{project: crookedState.project, workspaceGeneration: crookedState.workspaceGeneration, windowGeneration: gen,
		activeLine: crookedState.activeLine, camera: pseudoCameraFromDefaults(), paletteIndex: crookedState.paletteIndex,
		gainPercent: crookedState.gainPercent, spec: crookedState.spec, cancel: make(chan struct{}),
		loadQueue: make(chan pseudoLoadJob, maxInt(1, len(crookedState.projectLines)*2)), renderQueue: make(chan pseudoRenderRequest, 1),
		spatialRange: crookedState.pseudoRange.Normalized(), rangeGeneration: crookedState.rangeGeneration,
		timeRange: crookedState.pseudoTimeRange.Normalized(), timeRangeGeneration: crookedState.timeRangeGeneration,
		textureCache: textureCache, cacheWriteDigests: make(map[string]bool), cacheWriteQueue: make(chan []pseudoCacheWrite, 1), loadStarted: time.Now(),
		loadStage: int(segy.RenderStageSupportPlan)}
	if textureCache != nil {
		go func(cache *pseudocachecore.Cache) { _ = cache.Prune() }(textureCache)
	}
	atomic.StoreInt64(&pseudoActiveRangeGen, crookedState.rangeGeneration)
	atomic.StoreInt64(&pseudoActiveTimeGen, crookedState.timeRangeGeneration)
	createPseudoUI()
	initializePseudoLines()
	startPseudoWorkers()
	queuePseudoSceneRender(true)
	publishCrookedCursorToPseudo(true)
	showTopLevelWindowRestored(h)
	writeWorkspaceTrace(workspacecore.TraceEvent{Action: "pseudo3d_open_begin", Workspace: workspacecore.KindCrooked, Dataset: pseudoProjectName(), Geometry: geometrycore.KindCrookedLine, Generation: crookedState.workspaceGeneration, ProjectLines: len(pseudoState.lines)})
}

func reloadPseudoForCrookedRange() {
	if pseudoHwnd == 0 || pseudoState.project != crookedState.project {
		return
	}
	updatePseudoRangeInPlace(crookedState.pseudoRange.Normalized(), crookedState.rangeGeneration)
}

func reloadPseudoForCrookedTimeRange() {
	if pseudoHwnd == 0 || pseudoState.project != crookedState.project {
		return
	}
	updatePseudoTimeRangeInPlace(crookedState.pseudoTimeRange.Normalized(), crookedState.timeRangeGeneration)
}

func pseudoSameSpan(startA, endA, startB, endB float64) bool {
	tolerance := math.Max(1e-8, math.Max(math.Abs(endA-startA), math.Abs(endB-startB))*1e-8)
	return math.Abs(startA-startB) <= tolerance && math.Abs(endA-endB) <= tolerance
}

func pseudoCopySegment(segment pseudoCurtainSegment) pseudoCurtainSegment {
	result := segment
	result.points = append([]pseudo3dcore.Point(nil), segment.points...)
	result.u = append([]float64(nil), segment.u...)
	result.traceIndices = append([]int64(nil), segment.traceIndices...)
	result.positions = append([]float64(nil), segment.positions...)
	return result
}

func pseudoCacheExactSegments(state *pseudoLineState, segments []pseudoCurtainSegment) {
	if state == nil {
		return
	}
	for _, segment := range segments {
		if segment.preview || !segment.texture.Valid() {
			continue
		}
		duplicate := false
		for _, cached := range state.cachedSegments {
			if pseudoSameSpan(segment.positionStart, segment.positionEnd, cached.positionStart, cached.positionEnd) &&
				segment.sampleStart == cached.sampleStart && segment.sampleEnd == cached.sampleEnd && segment.texture == cached.texture {
				duplicate = true
				break
			}
		}
		if !duplicate {
			state.cachedSegments = append(state.cachedSegments, pseudoCopySegment(segment))
		}
	}
	if len(state.cachedSegments) > 0 {
		pseudoState.cacheClock++
		state.cacheStamp = pseudoState.cacheClock
	}
}

func pseudoExactSegmentSources(state *pseudoLineState) []pseudoCurtainSegment {
	if state == nil {
		return nil
	}
	result := make([]pseudoCurtainSegment, 0, len(state.segments)+len(state.cachedSegments))
	for _, source := range append(append([]pseudoCurtainSegment(nil), state.segments...), state.cachedSegments...) {
		if !source.preview && source.texture.Valid() {
			result = append(result, source)
		}
	}
	return result
}

func pseudoExactSegmentSourcesForWindow(state *pseudoLineState, sampleStart, sampleEnd int) []pseudoCurtainSegment {
	result := make([]pseudoCurtainSegment, 0)
	for _, source := range pseudoExactSegmentSources(state) {
		if source.sampleStart == sampleStart && source.sampleEnd == sampleEnd {
			result = append(result, source)
		}
	}
	return result
}

func pseudoPreviewSegmentSourcesForWindow(state *pseudoLineState, sampleStart, sampleEnd int) []pseudoCurtainSegment {
	if state == nil || sampleEnd <= sampleStart {
		return nil
	}
	result := make([]pseudoCurtainSegment, 0)
	for _, source := range pseudoExactSegmentSources(state) {
		if source.sampleEnd <= source.sampleStart || source.sampleStart > sampleStart || source.sampleEnd < sampleEnd {
			continue
		}
		span := float64(source.sampleEnd - source.sampleStart)
		v0, v1 := float64(sampleStart-source.sampleStart)/span, float64(sampleEnd-source.sampleStart)/span
		preview := pseudoCopySegment(source)
		preview.texture = pseudoTextureWindow2D(source.texture, 0, 1, v0, v1, state.size)
		if preview.texture == nil {
			continue
		}
		preview.sampleStart, preview.sampleEnd = sampleStart, sampleEnd
		preview.timeStartMS, preview.timeEndMS = state.timeStartMS, state.timeEndMS
		preview.preview = true
		result = append(result, preview)
	}
	return result
}

func pseudoTextureWindow(source *pseudo3dcore.Texture, fractionStart, fractionEnd float64, maximum pseudo3dcore.TextureSize) *pseudo3dcore.Texture {
	return pseudoTextureWindow2D(source, fractionStart, fractionEnd, 0, 1, maximum)
}

func pseudoTextureWindow2D(source *pseudo3dcore.Texture, uStart, uEnd, vStart, vEnd float64, maximum pseudo3dcore.TextureSize) *pseudo3dcore.Texture {
	if !source.Valid() || maximum.Width < 2 || maximum.Height < 2 {
		return nil
	}
	uStart = math.Max(0, math.Min(1, uStart))
	uEnd = math.Max(uStart, math.Min(1, uEnd))
	vStart = math.Max(0, math.Min(1, vStart))
	vEnd = math.Max(vStart, math.Min(1, vEnd))
	if uEnd-uStart <= 1e-12 || vEnd-vStart <= 1e-12 {
		return nil
	}
	if uStart <= 1e-12 && uEnd >= 1-1e-12 && vStart <= 1e-12 && vEnd >= 1-1e-12 && source.Width <= maximum.Width && source.Height <= maximum.Height {
		return source
	}
	coveredWidth := maxInt(2, int(math.Ceil(float64(source.Width)*(uEnd-uStart))))
	coveredHeight := maxInt(2, int(math.Ceil(float64(source.Height)*(vEnd-vStart))))
	width, height := minInt(maximum.Width, coveredWidth), minInt(maximum.Height, coveredHeight)
	width, height = maxInt(2, width), maxInt(2, height)
	pixels := make([]byte, width*height)
	for y := 0; y < height; y++ {
		v := vStart + float64(y)/float64(maxInt(1, height-1))*(vEnd-vStart)
		sourceY := clampInt(int(math.Round(v*float64(source.Height-1))), 0, source.Height-1)
		for x := 0; x < width; x++ {
			u := uStart + float64(x)/float64(maxInt(1, width-1))*(uEnd-uStart)
			sourceX := clampInt(int(math.Round(u*float64(source.Width-1))), 0, source.Width-1)
			pixels[y*width+x] = source.Indices[sourceY*source.Width+sourceX]
		}
	}
	return &pseudo3dcore.Texture{Width: width, Height: height, Indices: pixels}
}

func pseudoSegmentsForRuns(runs []pseudo3dcore.TraceRun, size pseudo3dcore.TextureSize, sources []pseudoCurtainSegment) (segments []pseudoCurtainSegment, fullyCovered bool) {
	if len(runs) == 0 {
		return nil, false
	}
	sizes := pseudoRunTextureSizes(runs, size)
	fullyCovered = true
	for runIndex, run := range runs {
		covered := false
		for _, source := range sources {
			if run.PositionStart < source.positionStart-1e-8 || run.PositionEnd > source.positionEnd+1e-8 || !source.texture.Valid() {
				continue
			}
			span := math.Max(source.positionEnd-source.positionStart, 1e-12)
			texture := pseudoTextureWindow(source.texture, (run.PositionStart-source.positionStart)/span, (run.PositionEnd-source.positionStart)/span, sizes[runIndex])
			if texture == nil {
				continue
			}
			segments = append(segments, pseudoCurtainSegment{points: append([]pseudo3dcore.Point(nil), run.Points...), u: append([]float64(nil), run.U...), texture: texture,
				length: run.Length, positionStart: run.PositionStart, positionEnd: run.PositionEnd,
				traceIndices: append([]int64(nil), run.TraceIndices...), positions: append([]float64(nil), run.Positions...),
				sampleStart: source.sampleStart, sampleEnd: source.sampleEnd, timeStartMS: source.timeStartMS, timeEndMS: source.timeEndMS, preview: source.preview})
			covered = true
			break
		}
		if covered {
			continue
		}
		fullyCovered = false
		// Expansion can still show the already cached part immediately. It is
		// marked preview and is replaced after the missing span is read.
		for _, source := range sources {
			if source.positionStart >= run.PositionStart-1e-8 && source.positionEnd <= run.PositionEnd+1e-8 && source.texture.Valid() {
				preview := pseudoCopySegment(source)
				preview.texture = pseudoTextureWindow(source.texture, 0, 1, sizes[runIndex])
				if preview.texture == nil {
					continue
				}
				preview.preview = true
				segments = append(segments, preview)
				break
			}
		}
	}
	return segments, fullyCovered
}

func pseudoSetLineSegments(state *pseudoLineState, segments []pseudoCurtainSegment, ready bool) {
	state.segments, state.ready = segments, ready
	state.points, state.u, state.texture = nil, nil, nil
	if len(segments) > 0 {
		state.points, state.u, state.texture = segments[0].points, segments[0].u, segments[0].texture
	}
}

func pseudoRestoreLineFromCache(state *pseudoLineState) bool {
	if state == nil || state.geometry == nil || len(state.cachedSegments) == 0 || state.size.Width < 2 || state.size.Height < 2 {
		return false
	}
	runs, err := pseudo3dcore.ClipTrajectory(state.geometry.TraceIndices, state.geometry.X, state.geometry.Y, state.geometry.Distance, pseudoState.spatialRange)
	if err != nil || len(runs) == 0 {
		return false
	}
	segments, covered := pseudoSegmentsForRuns(runs, state.size, pseudoExactSegmentSourcesForWindow(state, state.sampleStart, state.sampleEnd))
	pseudoSetLineSegments(state, segments, covered)
	return covered
}

func pseudoReplanSelectedTextureSizes() {
	requests := make([]pseudo3dcore.TextureRequest, 0, len(pseudoState.lines))
	fullSamples := make(map[string]int, len(pseudoState.lines))
	for i := range pseudoState.lines {
		state := &pseudoState.lines[i]
		if state.selected && state.inRange && state.timeInRange && pseudoLineSelectable(state) {
			requests = append(requests, pseudo3dcore.TextureRequest{ID: state.line.ID, Traces: int(state.line.Dataset.Metadata.TraceCount), Samples: state.sampleEnd - state.sampleStart + 1})
			fullSamples[state.line.ID] = state.line.Dataset.Metadata.SamplesPerTrace
		}
	}
	plans := pseudo3dcore.PlanTimeWindowTextureSizes(requests, fullSamples, pseudo3dcore.TextureBudget)
	for i := range pseudoState.lines {
		if pseudoState.lines[i].line != nil {
			pseudoState.lines[i].size = plans[pseudoState.lines[i].line.ID]
		}
	}
}

func trimPseudoTextureCache() {
	live := make(map[*pseudo3dcore.Texture]bool)
	var total int64
	add := func(texture *pseudo3dcore.Texture) {
		if texture.Valid() && !live[texture] {
			live[texture] = true
			total += int64(len(texture.Indices))
		}
	}
	for i := range pseudoState.lines {
		for _, segment := range pseudoState.lines[i].segments {
			add(segment.texture)
		}
	}
	for i := range pseudoState.lines {
		for _, segment := range pseudoState.lines[i].cachedSegments {
			add(segment.texture)
		}
	}
	for total > pseudo3dcore.TextureBudget {
		oldest := -1
		for i := range pseudoState.lines {
			if len(pseudoState.lines[i].cachedSegments) > 0 && (oldest < 0 || pseudoState.lines[i].cacheStamp < pseudoState.lines[oldest].cacheStamp) {
				oldest = i
			}
		}
		if oldest < 0 {
			break
		}
		for _, segment := range pseudoState.lines[oldest].cachedSegments {
			usedByScene := false
			for i := range pseudoState.lines {
				for _, current := range pseudoState.lines[i].segments {
					usedByScene = usedByScene || current.texture == segment.texture
				}
			}
			if !usedByScene && live[segment.texture] {
				total -= int64(len(segment.texture.Indices))
				delete(live, segment.texture)
			}
		}
		pseudoState.lines[oldest].cachedSegments = nil
	}
}

func updatePseudoRangeInPlace(selected pseudo3dcore.XYRange, generation int64) {
	selected = selected.Normalized()
	pseudoBeginLoadCycle(true)
	pseudoState.spatialRange, pseudoState.rangeGeneration = selected, generation
	if crookedState.activeLine >= 0 && crookedState.activeLine < len(pseudoState.lines) {
		pseudoState.activeLine = crookedState.activeLine
	}
	atomic.StoreInt64(&pseudoActiveRangeGen, generation)
	pseudoState.deferredLinesQueued = true
	pseudoState.rangePreviewActive, pseudoState.rangeReusedLines, pseudoState.rangeUpdatingLines = true, 0, 0
	for i := range pseudoState.lines {
		state := &pseudoState.lines[i]
		oldKnown, oldInRange, oldSelected := state.eligibilityKnown, state.inRange, state.selected
		pseudoCacheExactSegments(state, state.segments)
		state.token++
		state.loading = false
		if state.canonicalUnavailable {
			state.inRange, state.eligibilityKnown, state.selected = false, true, false
			pseudoSetLineSegments(state, nil, false)
			continue
		}
		inRange, known, points, u, length := pseudoInitialRangeState(state.geometry, state.line, selected)
		state.inRange, state.eligibilityKnown, state.points, state.u, state.estimatedRangeLength = inRange, known, points, u, length
		entered := inRange && known && (!oldKnown || !oldInRange)
		if !inRange && known {
			state.selected, state.errorText = false, ""
			pseudoSetLineSegments(state, nil, false)
		} else {
			state.selected = (oldSelected || entered) && state.timeInRange
			pseudoSetLineSegments(state, nil, false)
		}
	}
	pseudoReplanSelectedTextureSizes()
	for i := range pseudoState.lines {
		state := &pseudoState.lines[i]
		if !state.selected || !pseudoLineSelectable(state) {
			continue
		}
		if state.geometry != nil {
			runs, err := pseudo3dcore.ClipTrajectory(state.geometry.TraceIndices, state.geometry.X, state.geometry.Y, state.geometry.Distance, selected)
			if err != nil || len(runs) == 0 {
				state.inRange, state.eligibilityKnown, state.selected = false, true, false
				state.errorText = ""
				continue
			}
			segments, covered := pseudoSegmentsForRuns(runs, state.size, pseudoExactSegmentSourcesForWindow(state, state.sampleStart, state.sampleEnd))
			pseudoSetLineSegments(state, segments, covered)
			if len(segments) > 0 {
				pseudoState.rangeReusedLines++
			}
			if !covered {
				pseudoState.rangeUpdatingLines++
			}
		} else {
			pseudoState.rangeUpdatingLines++
		}
	}
	pseudoListUpdating = true
	for i := range pseudoState.lines {
		setPseudoListChecked(i, pseudoState.lines[i].selected && pseudoLineSelectable(&pseudoState.lines[i]))
		refreshPseudoLineRow(i)
	}
	pseudoListUpdating = false
	trimPseudoTextureCache()
	queuePseudoScenePreviewRender()
	if pseudoState.activeLine >= 0 {
		enqueuePseudoLine(pseudoState.activeLine)
	}
	pseudoQueueDeferredLines()
	publishCrookedCursorToPseudo(true)
	updatePseudoStatus()
	pInvalidateRect.Call(pseudoHwnd, 0, 0)
	writeWorkspaceTrace(workspacecore.TraceEvent{Action: fmt.Sprintf("pseudo3d_range_incremental_reuse_%d_update_%d", pseudoState.rangeReusedLines, pseudoState.rangeUpdatingLines),
		Workspace: workspacecore.KindCrooked, Dataset: pseudoProjectName(), Geometry: geometrycore.KindCrookedLine,
		Generation: pseudoState.workspaceGeneration, ProjectLines: crookedPseudoRangeCandidateCount()})
}

func updatePseudoTimeRangeInPlace(selected pseudo3dcore.TimeRange, generation int64) {
	selected = selected.Normalized()
	pseudoBeginLoadCycle(true)
	pseudoState.timeRange, pseudoState.timeRangeGeneration = selected, generation
	atomic.StoreInt64(&pseudoActiveTimeGen, generation)
	pseudoState.deferredLinesQueued = true
	pseudoState.rangePreviewActive, pseudoState.timePreviewActive = false, true
	pseudoState.rangeReusedLines, pseudoState.rangeUpdatingLines = 0, 0
	for i := range pseudoState.lines {
		state := &pseudoState.lines[i]
		oldTimeInRange, oldSelected := state.timeInRange, state.selected
		pseudoCacheExactSegments(state, state.segments)
		state.token++
		state.loading, state.ready = false, false
		sampleStart, sampleEnd, actual, timeOK := pseudoLineTimeWindow(state.line, selected)
		state.sampleStart, state.sampleEnd, state.timeInRange = sampleStart, sampleEnd, timeOK
		state.timeStartMS, state.timeEndMS = 0, 0
		if timeOK {
			state.timeStartMS, state.timeEndMS = actual.StartMS, actual.EndMS
		}
		if state.canonicalUnavailable {
			state.selected = false
			pseudoSetLineSegments(state, nil, false)
			continue
		}
		entered := timeOK && !oldTimeInRange
		if !timeOK {
			state.selected, state.errorText = false, ""
		} else {
			state.selected = oldSelected || entered
		}
		pseudoSetLineSegments(state, nil, false)
	}
	pseudoReplanSelectedTextureSizes()
	for i := range pseudoState.lines {
		state := &pseudoState.lines[i]
		if !state.selected || !pseudoLineSelectable(state) {
			continue
		}
		if state.geometry == nil {
			pseudoState.rangeUpdatingLines++
			continue
		}
		runs, err := pseudo3dcore.ClipTrajectory(state.geometry.TraceIndices, state.geometry.X, state.geometry.Y, state.geometry.Distance, pseudoState.spatialRange)
		if err != nil || len(runs) == 0 {
			state.inRange, state.eligibilityKnown, state.selected = false, true, false
			continue
		}
		exactSources := pseudoExactSegmentSourcesForWindow(state, state.sampleStart, state.sampleEnd)
		segments, covered := pseudoSegmentsForRuns(runs, state.size, exactSources)
		if covered && len(segments) > 0 {
			pseudoSetLineSegments(state, segments, true)
			pseudoState.rangeReusedLines++
			continue
		}
		previewSources := pseudoPreviewSegmentSourcesForWindow(state, state.sampleStart, state.sampleEnd)
		segments, _ = pseudoSegmentsForRuns(runs, state.size, previewSources)
		pseudoSetLineSegments(state, segments, false)
		if len(segments) > 0 {
			pseudoState.rangeReusedLines++
		}
		pseudoState.rangeUpdatingLines++
	}
	pseudoListUpdating = true
	for i := range pseudoState.lines {
		setPseudoListChecked(i, pseudoState.lines[i].selected && pseudoLineSelectable(&pseudoState.lines[i]))
		refreshPseudoLineRow(i)
	}
	pseudoListUpdating = false
	trimPseudoTextureCache()
	queuePseudoScenePreviewRender()
	if pseudoState.activeLine >= 0 {
		enqueuePseudoLine(pseudoState.activeLine)
	}
	pseudoQueueDeferredLines()
	publishCrookedCursorToPseudo(true)
	selectedCount, loaded, _, failed := pseudoLoadingCounts()
	if selectedCount > 0 && loaded+failed >= selectedCount {
		pseudoState.finalRenderPending = true
		queuePseudoFinalRender()
	}
	updatePseudoStatus()
	pInvalidateRect.Call(pseudoHwnd, 0, 0)
	writeWorkspaceTrace(workspacecore.TraceEvent{Action: fmt.Sprintf("pseudo3d_time_incremental_reuse_%d_update_%d", pseudoState.rangeReusedLines, pseudoState.rangeUpdatingLines),
		Workspace: workspacecore.KindCrooked, Dataset: pseudoProjectName(), Geometry: geometrycore.KindCrookedLine,
		Generation: pseudoState.workspaceGeneration, ProjectLines: selectedCount})
}

func closePseudoWindowForProjectChange() {
	if pseudoHwnd != 0 {
		pDestroyWindow.Call(pseudoHwnd)
	}
}

func createPseudoUI() {
	pseudoControlsUI = pseudoControls{}
	pseudoControlsUI.close = createCtrl(pseudoHwnd, "BUTTON", "关闭", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 10, 8, 54, 24, IDPSEUDO_CLOSE)
	pseudoControlsUI.all = createCtrl(pseudoHwnd, "BUTTON", "全选", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 70, 8, 54, 24, IDPSEUDO_ALL)
	pseudoControlsUI.none = createCtrl(pseudoHwnd, "BUTTON", "清空", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 130, 8, 54, 24, IDPSEUDO_NONE)
	pseudoControlsUI.current = createCtrl(pseudoHwnd, "BUTTON", "仅当前线", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 190, 8, 74, 24, IDPSEUDO_CURRENT)
	pseudoControlsUI.reset = createCtrl(pseudoHwnd, "BUTTON", "复位视图", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 270, 8, 74, 24, IDPSEUDO_RESET)
	createCtrl(pseudoHwnd, "STATIC", "色标", WS_CHILD|WS_VISIBLE, 356, 12, 34, 18, 0)
	pseudoControlsUI.palette = createCtrl(pseudoHwnd, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 392, 7, 128, 360, IDPSEUDO_PALETTE)
	for _, name := range paletteNames {
		pSendMessageW.Call(pseudoControlsUI.palette, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(name))))
	}
	pSendMessageW.Call(pseudoControlsUI.palette, CB_SETCURSEL, uintptr(pseudoState.paletteIndex), 0)
	createCtrl(pseudoHwnd, "STATIC", "增益", WS_CHILD|WS_VISIBLE, 532, 12, 34, 18, 0)
	pseudoControlsUI.gainMinus = createCtrl(pseudoHwnd, "BUTTON", "−", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 568, 8, 28, 24, IDPSEUDO_GAINMINUS)
	pseudoControlsUI.gainValue = createCtrl(pseudoHwnd, "STATIC", fmt.Sprintf("%.0f%%", pseudoState.gainPercent), WS_CHILD|WS_VISIBLE|WS_BORDER, 599, 9, 48, 22, 0)
	pseudoControlsUI.gainPlus = createCtrl(pseudoHwnd, "BUTTON", "+", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 650, 8, 28, 24, IDPSEUDO_GAINPLUS)
	createCtrl(pseudoHwnd, "STATIC", "FOV", WS_CHILD|WS_VISIBLE, 690, 12, 30, 18, 0)
	pseudoControlsUI.fov = createCtrl(pseudoHwnd, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 722, 7, 72, 320, IDPSEUDO_FOV)
	for _, fov := range pseudoFOVOptions {
		label := "正交"
		if fov > 0 {
			label = fmt.Sprintf("%.0f°", fov)
		}
		pSendMessageW.Call(pseudoControlsUI.fov, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(label))))
	}
	setPseudoFOVCombo(pseudoState.camera.FOV)
	createCtrl(pseudoHwnd, "STATIC", "垂向", WS_CHILD|WS_VISIBLE, 806, 12, 34, 18, 0)
	pseudoControlsUI.zMinus = createCtrl(pseudoHwnd, "BUTTON", "−", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 842, 8, 28, 24, IDPSEUDO_ZMINUS)
	pseudoControlsUI.zValue = createCtrl(pseudoHwnd, "STATIC", fmt.Sprintf("×%.2f", pseudoState.camera.VerticalScale), WS_CHILD|WS_VISIBLE|WS_BORDER, 873, 9, 56, 22, 0)
	pseudoControlsUI.zPlus = createCtrl(pseudoHwnd, "BUTTON", "+", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 932, 8, 28, 24, IDPSEUDO_ZPLUS)
	createCtrl(pseudoHwnd, "STATIC", "X", WS_CHILD|WS_VISIBLE, 10, 43, 14, 18, 0)
	pseudoControlsUI.axisX = createCtrl(pseudoHwnd, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 25, 37, 68, 280, IDPSEUDO_AXISX)
	createCtrl(pseudoHwnd, "STATIC", "Y", WS_CHILD|WS_VISIBLE, 101, 43, 14, 18, 0)
	pseudoControlsUI.axisY = createCtrl(pseudoHwnd, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 116, 37, 68, 280, IDPSEUDO_AXISY)
	createCtrl(pseudoHwnd, "STATIC", "Z", WS_CHILD|WS_VISIBLE, 192, 43, 14, 18, 0)
	pseudoControlsUI.axisZ = createCtrl(pseudoHwnd, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 207, 37, 68, 280, IDPSEUDO_AXISZ)
	for _, factor := range pseudoAxisFactorOptions {
		label := fmt.Sprintf("×%.2f", factor)
		pSendMessageW.Call(pseudoControlsUI.axisX, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(label))))
		pSendMessageW.Call(pseudoControlsUI.axisY, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(label))))
		pSendMessageW.Call(pseudoControlsUI.axisZ, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(label))))
	}
	setPseudoAxisCombos()
	pseudoControlsUI.saveDefault = createCtrl(pseudoHwnd, "BUTTON", "设为默认", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 285, 38, 72, 24, IDPSEUDO_DEFAULT)
	pseudoControlsUI.help = createCtrl(pseudoHwnd, "BUTTON", "帮助", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 363, 38, 52, 24, IDPSEUDO_HELP)
	pseudoControlsUI.styleLabel = createCtrl(pseudoHwnd, "STATIC", "风格", WS_CHILD|WS_VISIBLE, 427, 42, 34, 18, 0)
	pseudoControlsUI.style = createCtrl(pseudoHwnd, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 463, 37, 92, 150, IDPSEUDO_STYLE)
	for _, name := range []string{"干净", "CIGVis", "解释", "标准"} {
		pSendMessageW.Call(pseudoControlsUI.style, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(name))))
	}
	pSendMessageW.Call(pseudoControlsUI.style, CB_SETCURSEL, uintptr(crookedStyleComboIndex(crookedState.styleMode)), 0)
	pseudoControlsUI.exportRange = createCtrl(pseudoHwnd, "BUTTON", "导出范围", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 565, 38, 76, 24, IDPSEUDO_EXPORT_RANGE)
	pEnableWindow.Call(pseudoControlsUI.exportRange, 0)
	pseudoControlsUI.status = createCtrl(pseudoHwnd, "STATIC", "正在准备伪三维幕布…", WS_CHILD|WS_VISIBLE, 647, 42, 418, 19, IDPSEUDO_STATUS)
	pseudoControlsUI.progressStatus = createCtrl(pseudoHwnd, "STATIC", "准备中", WS_CHILD|WS_VISIBLE|WS_BORDER|pseudoSSCenter, 0, 0, 126, 19, IDPSEUDO_PROGRESS)
	pseudoControlsUI.progress = createCtrl(pseudoHwnd, "msctls_progress32", "", WS_CHILD|WS_VISIBLE, 0, 0, 240, 17, 0)
	pSendMessageW.Call(pseudoControlsUI.progress, PBM_SETRANGE, 0, uintptr(uint32(100)<<16))
	pseudoControlsUI.lines = createCtrl(pseudoHwnd, "SysListView32", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|WS_BORDER|WS_VSCROLL|LVS_REPORT|LVS_SINGLESEL|LVS_SHOWSELALWAYS, 0, 0, 280, 500, IDPSEUDO_LINES)
	pSendMessageW.Call(pseudoControlsUI.lines, LVM_SETEXTENDEDLISTVIEWSTYLE, LVS_EX_CHECKBOXES|LVS_EX_FULLROWSELECT, LVS_EX_CHECKBOXES|LVS_EX_FULLROWSELECT)
	insertPseudoColumn(0, "测线", 142)
	insertPseudoColumn(1, "道数", 54)
	insertPseudoColumn(2, "状态", 76)
	layoutPseudoControls()
	updatePseudoExportButton()
}

func insertPseudoColumn(index int, label string, width int) {
	column := pseudoLVColumn{Mask: LVCF_FMT | LVCF_WIDTH | LVCF_TEXT | LVCF_SUBITEM, Fmt: LVCFMT_LEFT, Cx: int32(width), PszText: u16(label), ISubItem: int32(index)}
	pSendMessageW.Call(pseudoControlsUI.lines, LVM_INSERTCOLUMNW, uintptr(index), uintptr(unsafe.Pointer(&column)))
}

func pseudoSceneRect() RECT {
	cw, ch := clientSize(pseudoHwnd)
	return RECT{Left: 12, Top: 70, Right: int32(maxInt(150, cw-310)), Bottom: int32(maxInt(170, ch-38))}
}

func pseudoListRect() RECT {
	cw, ch := clientSize(pseudoHwnd)
	return RECT{Left: int32(maxInt(160, cw-292)), Top: 70, Right: int32(maxInt(290, cw-10)), Bottom: int32(maxInt(170, ch-38))}
}

func layoutPseudoControls() {
	if pseudoHwnd == 0 {
		return
	}
	cw, _ := clientSize(pseudoHwnd)
	list := pseudoListRect()
	pMoveWindow.Call(pseudoControlsUI.lines, uintptr(list.Left), uintptr(list.Top), uintptr(maxInt(1, int(list.Right-list.Left))), uintptr(maxInt(1, int(list.Bottom-list.Top))), 1)
	progressW, progressStatusW := 210, 150
	if cw < 1200 {
		progressW, progressStatusW = 155, 126
	}
	if cw < 1100 {
		progressW, progressStatusW = 120, 104
	}
	progressX := maxInt(790, cw-progressW-10)
	progressStatusX := progressX - progressStatusW - 8
	statusX := 647
	pMoveWindow.Call(pseudoControlsUI.status, uintptr(statusX), 42, uintptr(maxInt(1, progressStatusX-statusX-8)), 19, 1)
	pMoveWindow.Call(pseudoControlsUI.progressStatus, uintptr(progressStatusX), 42, uintptr(progressStatusW), 19, 1)
	pMoveWindow.Call(pseudoControlsUI.progress, uintptr(progressX), 43, uintptr(progressW), 17, 1)
}

func setPseudoFOVCombo(value float64) {
	best, delta := 0, math.Inf(1)
	for i, option := range pseudoFOVOptions {
		if d := math.Abs(option - value); d < delta {
			best, delta = i, d
		}
	}
	pSendMessageW.Call(pseudoControlsUI.fov, CB_SETCURSEL, uintptr(best), 0)
}

func nearestPseudoOption(options []float64, value float64) int {
	best, delta := 0, math.Inf(1)
	for i, option := range options {
		if d := math.Abs(option - value); d < delta {
			best, delta = i, d
		}
	}
	return best
}

func setPseudoAxisCombos() {
	if pseudoControlsUI.axisX == 0 {
		return
	}
	pSendMessageW.Call(pseudoControlsUI.axisX, CB_SETCURSEL, uintptr(nearestPseudoOption(pseudoAxisFactorOptions, pseudoState.camera.AxisXScale)), 0)
	pSendMessageW.Call(pseudoControlsUI.axisY, CB_SETCURSEL, uintptr(nearestPseudoOption(pseudoAxisFactorOptions, pseudoState.camera.AxisYScale)), 0)
	pSendMessageW.Call(pseudoControlsUI.axisZ, CB_SETCURSEL, uintptr(nearestPseudoOption(pseudoAxisFactorOptions, pseudoState.camera.VerticalScale)), 0)
}

func pseudoLineTimeWindow(line *projectcore.CrookedProjectLine, selected pseudo3dcore.TimeRange) (sampleStart, sampleEnd int, actual pseudo3dcore.TimeRange, ok bool) {
	if line == nil || line.Dataset == nil {
		return 0, 0, pseudo3dcore.TimeRange{}, false
	}
	metadata := line.Dataset.Metadata
	return selected.SampleWindow(metadata.SampleIntervalUS, metadata.SamplesPerTrace)
}

func initializePseudoLines() {
	pseudoState.lines = make([]pseudoLineState, len(pseudoState.project.Lines))
	requests := make([]pseudo3dcore.TextureRequest, 0, len(pseudoState.project.Lines))
	fullSamples := make(map[string]int, len(pseudoState.project.Lines))
	for i, line := range pseudoState.project.Lines {
		state := pseudoLineState{line: line, multiplier: 1}
		if line != nil && line.Dataset != nil {
			state.timeMaxMS = float64(maxInt(1, line.Dataset.Metadata.SamplesPerTrace-1)*line.Dataset.Metadata.SampleIntervalUS) / 1000
		}
		sampleStart, sampleEnd, actualTime, timeOK := pseudoLineTimeWindow(line, pseudoState.timeRange)
		state.sampleStart, state.sampleEnd, state.timeInRange = sampleStart, sampleEnd, timeOK
		if state.timeInRange {
			state.timeStartMS, state.timeEndMS = actualTime.StartMS, actualTime.EndMS
		}
		if canonical, tracked := pseudoCanonicalProjectLine(i, line); tracked {
			canonicalReady := crookedState.projectGeometryReady && canonical != nil && canonical.ready && canonical.geometry != nil &&
				len(canonical.geometry.TraceIndices) >= 2 && len(canonical.mapPath.Points) >= 2
			if canonicalReady {
				state.geometry = canonical.geometry
				state.multiplier = canonical.multiplier
				state.geometryCalibrated = true
				state.geometryFingerprint = canonical.fingerprint
				if state.geometryFingerprint == "" {
					state.geometryFingerprint = crookedGeometryFingerprint(line.ID, state.geometry)
				}
			} else {
				state.canonicalUnavailable = true
				state.errorText = "规范几何未发布"
				if canonical != nil && canonical.errorText != "" {
					state.errorText = canonical.errorText
				}
			}
		}
		if state.canonicalUnavailable {
			state.inRange, state.eligibilityKnown = false, true
		} else {
			state.inRange, state.eligibilityKnown, state.points, state.u, state.estimatedRangeLength = pseudoInitialRangeState(state.geometry, line, pseudoState.spatialRange)
		}
		state.selected = line != nil && line.Valid() && state.inRange && state.timeInRange
		if state.selected {
			if restored, ok := pseudoState.restoreSelections[line.ID]; ok {
				state.selected = restored
			}
		}
		if state.selected {
			requests = append(requests, pseudo3dcore.TextureRequest{ID: line.ID, Traces: int(line.Dataset.Metadata.TraceCount), Samples: state.sampleEnd - state.sampleStart + 1})
			fullSamples[line.ID] = line.Dataset.Metadata.SamplesPerTrace
		}
		pseudoState.lines[i] = state
	}
	plans := pseudo3dcore.PlanTimeWindowTextureSizes(requests, fullSamples, pseudo3dcore.TextureBudget)
	pseudoListUpdating = true
	for i := range pseudoState.lines {
		state := &pseudoState.lines[i]
		if state.line != nil {
			state.size = plans[state.line.ID]
		}
		insertPseudoLineRow(i)
	}
	pseudoListUpdating = false
	if pseudoState.activeLine < 0 || pseudoState.activeLine >= len(pseudoState.lines) || !pseudoState.lines[pseudoState.activeLine].selected {
		pseudoState.activeLine = -1
		for i := range pseudoState.lines {
			if pseudoState.lines[i].selected {
				pseudoState.activeLine = i
				break
			}
		}
	}
	if pseudoState.activeLine >= 0 {
		setPseudoListSelected(pseudoState.activeLine)
		enqueuePseudoLine(pseudoState.activeLine)
	}
	if pseudoState.projectMultiplier > 0 {
		pseudoQueueDeferredLines()
	}
	updatePseudoStatus()
}

// pseudoCanonicalProjectLine reports whether a line belongs to the currently
// published crooked project. In that context pseudo-3-D must consume only the
// canonical geometry already accepted by the project recognizer; DAT fallback
// remains available solely for genuine legacy/non-project callers.
func pseudoCanonicalProjectLine(index int, line *projectcore.CrookedProjectLine) (*crookedProjectLineState, bool) {
	if pseudoState.project == nil || crookedState.project == nil || pseudoState.project != crookedState.project {
		return nil, false
	}
	if index >= 0 && index < len(crookedState.projectLines) {
		candidate := &crookedState.projectLines[index]
		if line == nil || (candidate.line != nil && candidate.line.ID == line.ID) {
			return candidate, true
		}
	}
	if line != nil {
		for i := range crookedState.projectLines {
			if crookedState.projectLines[i].line != nil && crookedState.projectLines[i].line.ID == line.ID {
				return &crookedState.projectLines[i], true
			}
		}
	}
	return nil, true
}

func pseudoInitialRangeState(g *geometrycore.CrookedLineGeometry, line *projectcore.CrookedProjectLine, selected pseudo3dcore.XYRange) (inRange, known bool, preview []pseudo3dcore.Point, u []float64, length float64) {
	selected = selected.Normalized()
	if g != nil && len(g.X) >= 2 {
		points := make([]pseudo3dcore.Point, len(g.X))
		for i := range points {
			points[i] = pseudo3dcore.Point{X: g.X[i], Y: g.Y[i]}
		}
		inRange, known = !selected.Valid || pseudo3dcore.PolylineIntersectsRange(points, selected), true
		if !selected.Valid {
			preview, u, _ = pseudo3dcore.DecimateTrajectory(g.X, g.Y, g.Distance, pseudo3dcore.MaxCurtainPoints)
			length = g.TotalDistance
		} else if inRange {
			runs, _ := pseudo3dcore.ClipTrajectory(g.TraceIndices, g.X, g.Y, g.Distance, selected)
			for _, run := range runs {
				length += run.Length
			}
		}
		return
	}
	if line != nil && len(line.Navigation) >= 2 {
		points := make([]pseudo3dcore.Point, len(line.Navigation))
		for i, point := range line.Navigation {
			points[i] = pseudo3dcore.Point{X: point.X, Y: point.Y}
		}
		inRange, known = !selected.Valid || pseudo3dcore.PolylineIntersectsRange(points, selected), true
		preview, u = pseudoNavigationTrajectory(line.Navigation)
		for i := 1; i < len(points); i++ {
			if !selected.Valid || pseudo3dcore.PolylineIntersectsRange(points[i-1:i+1], selected) {
				length += math.Hypot(points[i].X-points[i-1].X, points[i].Y-points[i-1].Y)
			}
		}
		if selected.Valid {
			preview, u = nil, nil
		}
		return
	}
	return true, !selected.Valid, nil, nil, 0
}

func pseudoQueueDeferredLines() {
	indices := make([]int, 0, len(pseudoState.lines))
	for i := range pseudoState.lines {
		if i != pseudoState.activeLine && pseudoState.lines[i].selected && pseudoLineSelectable(&pseudoState.lines[i]) {
			indices = append(indices, i)
		}
	}
	sort.SliceStable(indices, func(i, j int) bool {
		a, b := &pseudoState.lines[indices[i]], &pseudoState.lines[indices[j]]
		if math.Abs(a.estimatedRangeLength-b.estimatedRangeLength) > 1e-9 {
			return a.estimatedRangeLength > b.estimatedRangeLength
		}
		return a.line.Name < b.line.Name
	})
	for _, index := range indices {
		enqueuePseudoLine(index)
	}
	pseudoState.deferredLinesQueued = true
}

func pseudoNavigationTrajectory(navigation []projectcore.NavigationPoint) ([]pseudo3dcore.Point, []float64) {
	if len(navigation) < 2 {
		return nil, nil
	}
	x, y, distance := make([]float64, len(navigation)), make([]float64, len(navigation)), make([]float64, len(navigation))
	for i, point := range navigation {
		x[i], y[i] = point.X, point.Y
		if i > 0 {
			distance[i] = distance[i-1] + math.Hypot(x[i]-x[i-1], y[i]-y[i-1])
		}
	}
	points, u, err := pseudo3dcore.DecimateTrajectory(x, y, distance, pseudo3dcore.MaxCurtainPoints)
	if err != nil {
		return nil, nil
	}
	return points, u
}

func insertPseudoLineRow(index int) {
	state := &pseudoState.lines[index]
	name, traces := "未命名", int64(0)
	valid := state.line != nil && state.line.Valid()
	if state.line != nil {
		name = state.line.Name
		if state.line.Dataset != nil {
			traces = state.line.Dataset.Metadata.TraceCount
		}
	}
	imageState := uint32(1 << 12)
	if valid && state.selected {
		imageState = 2 << 12
	}
	item := pseudoLVItem{Mask: LVIF_TEXT | LVIF_STATE, IItem: int32(index), PszText: u16(name), State: imageState, StateMask: LVIS_STATEIMAGEMASK}
	pSendMessageW.Call(pseudoControlsUI.lines, LVM_INSERTITEMW, 0, uintptr(unsafe.Pointer(&item)))
	setPseudoListChecked(index, valid && state.selected)
	setPseudoListText(index, 1, fmt.Sprintf("%d", traces))
	setPseudoListText(index, 2, pseudoLineStatus(state))
}

func setPseudoListText(index, subitem int, text string) {
	item := pseudoLVItem{Mask: LVIF_TEXT, IItem: int32(index), ISubItem: int32(subitem), PszText: u16(text)}
	pSendMessageW.Call(pseudoControlsUI.lines, LVM_SETITEMW, 0, uintptr(unsafe.Pointer(&item)))
}

func setPseudoListChecked(index int, checked bool) {
	state := uint32(1 << 12)
	if checked {
		state = 2 << 12
	}
	item := pseudoLVItem{Mask: LVIF_STATE, IItem: int32(index), State: state, StateMask: LVIS_STATEIMAGEMASK}
	pSendMessageW.Call(pseudoControlsUI.lines, LVM_SETITEMSTATE, uintptr(index), uintptr(unsafe.Pointer(&item)))
}

func setPseudoListSelected(index int) {
	item := pseudoLVItem{Mask: LVIF_STATE, IItem: int32(index), State: LVIS_SELECTED, StateMask: LVIS_SELECTED}
	pSendMessageW.Call(pseudoControlsUI.lines, LVM_SETITEMSTATE, uintptr(index), uintptr(unsafe.Pointer(&item)))
}

func pseudoLineIndexByID(lineID string) int {
	if lineID == "" {
		return -1
	}
	for i := range pseudoState.lines {
		if pseudoState.lines[i].line != nil && pseudoState.lines[i].line.ID == lineID {
			return i
		}
	}
	return -1
}

func applyPseudoLinkedCursor(target pseudo3dcore.LinkedCursor) {
	if pseudoHwnd == 0 || !target.Valid || target.ProjectGeneration != pseudoState.workspaceGeneration {
		return
	}
	index := pseudoLineIndexByID(target.LineID)
	if index < 0 {
		return
	}
	state := &pseudoState.lines[index]
	spatialInside := !pseudoState.spatialRange.Normalized().Valid || pseudoState.spatialRange.Contains(pseudo3dcore.Point{X: target.X, Y: target.Y})
	timeInside := !pseudoState.timeRange.Normalized().Valid || pseudoState.timeRange.Contains(target.TimeMS)
	inside := spatialInside && timeInside
	pseudoState.linkedCursor, pseudoState.linkedCursorValid, pseudoState.linkedCursorOutside = target, inside, !inside
	pseudoState.linkedCursorTimeOutside = spatialInside && !timeInside
	activeChanged := pseudoState.activeLine != index
	pseudoState.activeLine = index
	setPseudoListSelected(index)
	if !inside {
		area := pseudoSceneRect()
		pInvalidateRect.Call(pseudoHwnd, uintptr(unsafe.Pointer(&area)), 0)
		if pseudoState.linkedCursorTimeOutside {
			setText(pseudoControlsUI.status, "当前二维时间在伪三维时间范围外；时间窗保持不变。")
		} else {
			setText(pseudoControlsUI.status, "当前二维位置在伪三维范围外；AOI 保持不变。")
		}
		return
	}
	if pseudoLineSelectable(state) && !state.selected {
		applyPseudoSelection(index, true, true)
	} else if activeChanged {
		queuePseudoSceneRender(true)
	}
	area := pseudoSceneRect()
	pInvalidateRect.Call(pseudoHwnd, uintptr(unsafe.Pointer(&area)), 0)
}

func pseudoLinkedCursorProjection(area RECT) (pseudo3dcore.ScreenPoint, pseudo3dcore.ScreenPoint, pseudo3dcore.ScreenPoint, bool) {
	if !pseudoState.linkedCursorValid || pseudoState.linkedCursorOutside || crookedState.styleMode == 3 || !pseudoState.renderedScene.Bounds.Valid {
		return pseudo3dcore.ScreenPoint{}, pseudo3dcore.ScreenPoint{}, pseudo3dcore.ScreenPoint{}, false
	}
	index := pseudoLineIndexByID(pseudoState.linkedCursor.LineID)
	if index < 0 || index >= len(pseudoState.lines) {
		return pseudo3dcore.ScreenPoint{}, pseudo3dcore.ScreenPoint{}, pseudo3dcore.ScreenPoint{}, false
	}
	state := &pseudoState.lines[index]
	visible := false
	for _, segment := range state.segments {
		if pseudoState.linkedCursor.Distance >= segment.positionStart-1e-9 && pseudoState.linkedCursor.Distance <= segment.positionEnd+1e-9 {
			visible = true
			break
		}
	}
	if !visible || state.timeEndMS <= state.timeStartMS {
		return pseudo3dcore.ScreenPoint{}, pseudo3dcore.ScreenPoint{}, pseudo3dcore.ScreenPoint{}, false
	}
	width, height := int(area.Right-area.Left), int(area.Bottom-area.Top)
	world := pseudo3dcore.Point{X: pseudoState.linkedCursor.X, Y: pseudoState.linkedCursor.Y}
	topFraction := pseudo3dcore.TimeFraction(pseudoState.renderedScene.Bounds, state.timeStartMS)
	bottomFraction := pseudo3dcore.TimeFraction(pseudoState.renderedScene.Bounds, state.timeEndMS)
	top, okTop := pseudo3dcore.ProjectPoint(pseudoState.renderedScene, pseudoState.renderedCamera, width, height, world, topFraction)
	bottom, okBottom := pseudo3dcore.ProjectPoint(pseudoState.renderedScene, pseudoState.renderedCamera, width, height, world, bottomFraction)
	fraction := pseudo3dcore.TimeFraction(pseudoState.renderedScene.Bounds, pseudoState.linkedCursor.TimeMS)
	point, okPoint := pseudo3dcore.ProjectPoint(pseudoState.renderedScene, pseudoState.renderedCamera, width, height, world, fraction)
	return top, bottom, point, okTop && okBottom && okPoint
}

func pseudoLineStatus(state *pseudoLineState) string {
	if state == nil || state.line == nil {
		return "无效"
	}
	if state.canonicalUnavailable {
		return "规范几何错误"
	}
	if state.line.OpenError != "" || state.errorText != "" {
		return "错误"
	}
	if state.eligibilityKnown && !state.inRange {
		return "范围外"
	}
	if !pseudoLineTimeEligible(state) {
		return "时间范围外"
	}
	if !state.eligibilityKnown {
		return "范围判断中"
	}
	if state.loading {
		return "加载中"
	}
	if state.ready && len(state.segments) > 0 {
		return "就绪"
	}
	if !state.selected {
		return "未显示"
	}
	return "等待"
}

func pseudoLineTimeEligible(state *pseudoLineState) bool {
	if state == nil {
		return false
	}
	if state.timeInRange {
		return true
	}
	// Preserve zero-value synthetic/UI fixtures from Phases 4-10. Production
	// lines always receive an explicit sample window during initialization.
	return !pseudoState.timeRange.Normalized().Valid && state.sampleStart == 0 && state.sampleEnd == 0 && state.timeStartMS == 0 && state.timeEndMS == 0
}

func refreshPseudoLineRow(index int) {
	if index >= 0 && index < len(pseudoState.lines) {
		setPseudoListText(index, 2, pseudoLineStatus(&pseudoState.lines[index]))
	}
}

func pseudoLineSelectable(state *pseudoLineState) bool {
	return state != nil && !state.canonicalUnavailable && state.line != nil && state.line.Valid() && pseudoLineTimeEligible(state) && (state.inRange || !state.eligibilityKnown)
}

func startPseudoWorkers() {
	gen, cancel := pseudoState.windowGeneration, pseudoState.cancel
	loadQueue, renderQueue := pseudoState.loadQueue, pseudoState.renderQueue
	for workerIndex := 0; workerIndex < pseudoLineWorkerCount(); workerIndex++ {
		go func() {
			for {
				select {
				case <-cancel:
					return
				case job := <-loadQueue:
					if job.windowGen != gen || job.rangeGeneration != atomic.LoadInt64(&pseudoActiveRangeGen) || job.timeRangeGeneration != atomic.LoadInt64(&pseudoActiveTimeGen) {
						continue
					}
					postPseudoLoadResult(performPseudoLoad(job))
				}
			}
		}()
	}
	go func() {
		for {
			select {
			case <-cancel:
				return
			case request := <-renderQueue:
				image, stats, err := pseudo3dcore.RenderWithCancel(request.scene, request.camera, request.width, request.height, request.palette, func() bool {
					return request.windowGen != atomic.LoadInt64(&pseudoWindowGen) || request.renderGen != atomic.LoadInt64(&pseudoRenderGen)
				})
				if errors.Is(err, pseudo3dcore.ErrRenderCanceled) {
					continue
				}
				postPseudoRenderResult(&pseudoRenderResult{windowGen: request.windowGen, renderGen: request.renderGen, image: image, width: request.width, height: request.height,
					stats: stats, scene: request.scene, camera: request.camera, refs: request.refs, preview: request.preview, final: request.final,
					refineAfterPreview: request.refineAfterPreview, err: err})
			}
		}
	}()
	if pseudoState.textureCache != nil && pseudoState.cacheWriteQueue != nil {
		cache, writeQueue := pseudoState.textureCache, pseudoState.cacheWriteQueue
		go func() {
			for {
				select {
				case <-cancel:
					return
				case writes := <-writeQueue:
					for _, write := range writes {
						select {
						case <-cancel:
							return
						default:
						}
						if err := cache.Store(write.key, write.entry); err != nil {
							continue
						}
					}
					_ = cache.Prune()
				}
			}
		}()
	}
}

func pseudoLineWorkerCount() int {
	workers := runtime.NumCPU() / 4
	if workers < 2 {
		workers = 2
	}
	if workers > 4 {
		workers = 4
	}
	return workers
}

func pseudoBlockWorkerCount() int {
	workers := 8 / pseudoLineWorkerCount()
	if workers < 1 {
		workers = 1
	}
	if workers > 4 {
		workers = 4
	}
	return workers
}

func enqueuePseudoLine(index int) {
	if pseudoHwnd == 0 || index < 0 || index >= len(pseudoState.lines) {
		return
	}
	state := &pseudoState.lines[index]
	if !state.selected || !pseudoLineSelectable(state) || state.ready || state.loading {
		return
	}
	if pseudoState.finalRenderComplete {
		pseudoBeginLoadCycle(false)
	}
	state.token++
	state.loading = true
	state.errorText = ""
	pseudoState.finalRenderPending, pseudoState.finalRenderComplete = false, false
	job := pseudoLoadJob{windowGen: pseudoState.windowGeneration, lineIndex: index, token: state.token, line: state.line,
		geometry: state.geometry, geometryCalibrated: state.geometryCalibrated, geometryFingerprint: state.geometryFingerprint,
		canonicalUnavailable: state.canonicalUnavailable, existingMultiplier: state.multiplier,
		projectMultiplier: pseudoState.projectMultiplier, spec: pseudoState.spec, size: state.size,
		gain: pseudoState.gainPercent, agc: agc, clip: clipPercent, displayMode: renderDisplayMode, spatialRange: pseudoState.spatialRange,
		rangeGeneration: pseudoState.rangeGeneration, timeRange: pseudoState.timeRange, timeRangeGeneration: pseudoState.timeRangeGeneration,
		sampleStart: state.sampleStart, sampleEnd: state.sampleEnd, timeStartMS: state.timeStartMS, timeEndMS: state.timeEndMS,
		reusableSegments: pseudoExactSegmentSourcesForWindow(state, state.sampleStart, state.sampleEnd), textureCache: pseudoState.textureCache}
	refreshPseudoLineRow(index)
	select {
	case pseudoState.loadQueue <- job:
	default:
		go func(queue chan pseudoLoadJob, cancel chan struct{}, item pseudoLoadJob) {
			select {
			case queue <- item:
			case <-cancel:
			}
		}(pseudoState.loadQueue, pseudoState.cancel, job)
	}
}

func performPseudoLoad(job pseudoLoadJob) *pseudoLoadResult {
	result := &pseudoLoadResult{windowGen: job.windowGen, lineIndex: job.lineIndex, token: job.token, rangeGeneration: job.rangeGeneration,
		timeRangeGeneration: job.timeRangeGeneration, multiplier: 1, geometryCalibrated: job.geometryCalibrated,
		geometryFingerprint: job.geometryFingerprint}
	if job.rangeGeneration != atomic.LoadInt64(&pseudoActiveRangeGen) || job.timeRangeGeneration != atomic.LoadInt64(&pseudoActiveTimeGen) {
		result.stale = true
		return result
	}
	if job.canonicalUnavailable {
		result.err = fmt.Errorf("canonical project geometry is unavailable")
		return result
	}
	if job.line == nil || !job.line.Valid() || job.size.Width < 2 || job.size.Height < 2 {
		result.err = fmt.Errorf("invalid pseudo-3D line source")
		return result
	}
	metadata := job.line.Dataset.Metadata
	if job.sampleStart < 0 || job.sampleEnd <= job.sampleStart || job.sampleEnd >= metadata.SamplesPerTrace {
		job.sampleStart, job.sampleEnd = 0, metadata.SamplesPerTrace-1
	}
	if job.timeEndMS <= job.timeStartMS {
		job.timeStartMS = float64(job.sampleStart) * float64(metadata.SampleIntervalUS) / 1000
		job.timeEndMS = float64(job.sampleEnd) * float64(metadata.SampleIntervalUS) / 1000
	}
	if cached, hit := pseudoTryPersistentCache(job); hit {
		return cached
	}
	if preview, hit := pseudoTryPersistentCoveringPreview(job); hit {
		postPseudoLoadResult(preview)
		if job.geometry == nil && preview.geometry != nil && preview.geometryCalibrated {
			job.geometry, job.geometryCalibrated = preview.geometry, true
			job.geometryFingerprint = preview.geometryFingerprint
			job.existingMultiplier = preview.multiplier
			job.line.Dataset.SetGeometry(preview.geometry)
		}
	}
	reader, err := job.line.Dataset.OpenReader()
	if err != nil {
		result.err = err
		return result
	}
	defer reader.Close()
	geometry := job.geometry
	geometryWasProvided := geometry != nil
	if job.geometryCalibrated {
		result.multiplier = job.existingMultiplier
		if result.multiplier <= 0 {
			result.multiplier = 1
		}
	}
	if geometry == nil {
		build, buildErr := geometrycore.BuildCrookedCachedProgress(reader, job.spec, maxInt(1, runtime.NumCPU()/2), nil)
		if buildErr != nil {
			result.err = buildErr
			return result
		}
		geometry = build.Geometry
		job.line.Dataset.SetGeometry(geometry)
	}
	if job.geometryCalibrated {
		if result.geometryFingerprint == "" {
			result.geometryFingerprint = crookedGeometryFingerprint(job.line.ID, geometry)
		}
	} else {
		result.geometryFingerprint = ""
	}
	if !job.geometryCalibrated {
		multiplier := job.projectMultiplier
		if multiplier <= 0 && len(job.line.Navigation) >= 2 {
			calibration := projectcore.InferCoordinateMultiplier(geometry, job.line.Navigation)
			if calibration.Accepted {
				multiplier = calibration.Multiplier
				result.calibrationAccepted = true
			}
		}
		if multiplier <= 0 {
			multiplier = 1
		}
		result.multiplier = multiplier
		currentMultiplier := 1.0
		if geometryWasProvided && job.existingMultiplier > 0 {
			currentMultiplier = job.existingMultiplier
		}
		ratio := multiplier / currentMultiplier
		if math.Abs(ratio-1) > 1e-12 {
			if scaled, scaleErr := projectcore.ScaleGeometry(geometry, ratio); scaleErr == nil {
				geometry = scaled
			}
		}
	}
	runs, err := pseudo3dcore.ClipTrajectory(geometry.TraceIndices, geometry.X, geometry.Y, geometry.Distance, job.spatialRange)
	if err != nil {
		result.err = err
		return result
	}
	result.geometry = geometry
	if len(runs) == 0 {
		result.outOfRange = true
		return result
	}
	sizes := pseudoRunTextureSizes(runs, job.size)
	result.segments = make([]pseudoCurtainSegment, 0, len(runs))
	for i, run := range runs {
		if job.rangeGeneration != atomic.LoadInt64(&pseudoActiveRangeGen) || job.timeRangeGeneration != atomic.LoadInt64(&pseudoActiveTimeGen) {
			result.stale = true
			return result
		}
		reused := false
		for _, source := range job.reusableSegments {
			if pseudoSameSpan(run.PositionStart, run.PositionEnd, source.positionStart, source.positionEnd) && source.texture.Valid() {
				segment := pseudoCopySegment(source)
				segment.texture = pseudoTextureWindow(source.texture, 0, 1, sizes[i])
				if segment.texture == nil {
					continue
				}
				segment.points, segment.u = append([]pseudo3dcore.Point(nil), run.Points...), append([]float64(nil), run.U...)
				segment.traceIndices, segment.positions = append([]int64(nil), run.TraceIndices...), append([]float64(nil), run.Positions...)
				segment.length, segment.positionStart, segment.positionEnd, segment.preview = run.Length, run.PositionStart, run.PositionEnd, false
				result.segments = append(result.segments, segment)
				reused = true
				break
			}
		}
		if reused {
			continue
		}
		readStrategy := job.readStrategy
		if readStrategy == segy.ReadStrategyDefault {
			readStrategy = segy.ReadStrategySparseMapped
		}
		pixels, renderStats, renderErr := reader.RenderTracePositions(run.TraceIndices, run.Positions, run.PositionStart, run.PositionEnd, segy.RenderOptions{
			Width: sizes[i].Width, Height: sizes[i].Height, AGC: job.agc, ClipPercent: job.clip, GainPercent: job.gain,
			SampleStart: job.sampleStart, SampleEnd: job.sampleEnd, DisplayMode: job.displayMode,
			ReadStrategy: readStrategy, Workers: pseudoBlockWorkerCount(),
			Stage: func(stage segy.RenderStage) { postPseudoLoadStage(job.windowGen, stage) },
		})
		if renderErr != nil {
			result.err = renderErr
			return result
		}
		result.segments = append(result.segments, pseudoCurtainSegment{points: run.Points, u: run.U, length: run.Length,
			positionStart: run.PositionStart, positionEnd: run.PositionEnd, traceIndices: append([]int64(nil), run.TraceIndices...), positions: append([]float64(nil), run.Positions...),
			sampleStart: job.sampleStart, sampleEnd: job.sampleEnd, timeStartMS: job.timeStartMS, timeEndMS: job.timeEndMS,
			texture: &pseudo3dcore.Texture{Width: sizes[i].Width, Height: sizes[i].Height, Indices: pixels}})
		result.ioReadCalls += renderStats.IOReadCalls
		result.ioReadBytes += renderStats.IOReadBytes
		result.inputTraceCount += renderStats.InputTraceCount
		result.supportTraceCount += renderStats.SupportTraceCount
		result.ioLogicalBytes += renderStats.IOLogicalBytes
		result.ioDecodeNanos += renderStats.IODecodeNanos
		result.decodedSampleCount += renderStats.DecodedSampleCount
		if renderStats.MappedIO {
			result.mappedSegments++
		}
		result.fallbackSegments += renderStats.IOFallbacks
	}
	result.points, result.u, result.texture = result.segments[0].points, result.segments[0].u, result.segments[0].texture
	result.cacheWrite = pseudoPrepareCacheWrite(job, result)
	return result
}

func postPseudoLoadStage(windowGeneration int64, stage segy.RenderStage) {
	if pseudoHwnd == 0 || windowGeneration != atomic.LoadInt64(&pseudoWindowGen) {
		return
	}
	pPostMessageW.Call(pseudoHwnd, WM_PSEUDO_LOAD_STAGE, uintptr(stage), uintptr(windowGeneration))
}

func pseudoRunTextureSizes(runs []pseudo3dcore.TraceRun, lineSize pseudo3dcore.TextureSize) []pseudo3dcore.TextureSize {
	result := make([]pseudo3dcore.TextureSize, len(runs))
	if len(runs) == 0 {
		return result
	}
	budget := maxInt(4*len(runs), lineSize.Width*lineSize.Height)
	totalWidth := maxInt(lineSize.Width, 2*len(runs))
	height := maxInt(2, minInt(lineSize.Height, budget/maxInt(1, totalWidth)))
	totalLength := 0.0
	for _, run := range runs {
		totalLength += math.Max(run.Length, 1e-12)
	}
	remaining := maxInt(0, totalWidth-2*len(runs))
	used := 0
	for i, run := range runs {
		width := 2
		if i == len(runs)-1 {
			width += remaining - used
		} else {
			extra := int(math.Floor(float64(remaining) * math.Max(run.Length, 1e-12) / totalLength))
			width += extra
			used += extra
		}
		result[i] = pseudo3dcore.TextureSize{Width: maxInt(2, width), Height: height}
	}
	return result
}

func postPseudoLoadResult(result *pseudoLoadResult) {
	if result == nil || result.stale || result.windowGen != atomic.LoadInt64(&pseudoWindowGen) || result.rangeGeneration != atomic.LoadInt64(&pseudoActiveRangeGen) || result.timeRangeGeneration != atomic.LoadInt64(&pseudoActiveTimeGen) || pseudoHwnd == 0 {
		if result != nil {
			writeWorkspaceTrace(workspacecore.TraceEvent{Action: "pseudo3d_line_stale", Workspace: workspacecore.KindCrooked, Geometry: geometrycore.KindCrookedLine, Generation: pseudoState.workspaceGeneration})
		}
		return
	}
	pseudoPendingMu.Lock()
	pseudoLoadPending = append(pseudoLoadPending, result)
	pseudoPendingMu.Unlock()
	pPostMessageW.Call(pseudoHwnd, WM_PSEUDO_LOAD_READY, 0, 0)
}

func handlePseudoLoadResults() {
	pseudoPendingMu.Lock()
	results := pseudoLoadPending
	pseudoLoadPending = nil
	pseudoPendingMu.Unlock()
	activeLineReady := false
	acceptedResult := false
	for _, result := range results {
		if result == nil || result.stale || result.windowGen != pseudoState.windowGeneration || result.rangeGeneration != pseudoState.rangeGeneration || result.timeRangeGeneration != pseudoState.timeRangeGeneration || result.lineIndex < 0 || result.lineIndex >= len(pseudoState.lines) {
			continue
		}
		state := &pseudoState.lines[result.lineIndex]
		if result.token != state.token || !state.selected {
			writeWorkspaceTrace(workspacecore.TraceEvent{Action: "pseudo3d_line_stale", Workspace: workspacecore.KindCrooked, Dataset: state.line.Name, Geometry: geometrycore.KindCrookedLine, Generation: pseudoState.workspaceGeneration})
			continue
		}
		if result.previewOnly {
			acceptedResult = true
			state.geometry = result.geometry
			state.multiplier, state.geometryCalibrated = result.multiplier, result.geometryCalibrated
			state.geometryFingerprint = result.geometryFingerprint
			pseudoSetLineSegments(state, result.segments, false)
			state.loading, state.inRange, state.eligibilityKnown = true, true, true
			if result.cacheHit {
				pseudoState.cacheHits++
			}
			activeLineReady = activeLineReady || result.lineIndex == pseudoState.activeLine
			refreshPseudoLineRow(result.lineIndex)
			continue
		}
		state.loading = false
		acceptedResult = true
		pseudoState.ioReadCalls += result.ioReadCalls
		pseudoState.ioReadBytes += result.ioReadBytes
		pseudoState.inputTraceCount += int64(result.inputTraceCount)
		pseudoState.supportTraceCount += int64(result.supportTraceCount)
		pseudoState.ioLogicalBytes += result.ioLogicalBytes
		pseudoState.ioDecodeNanos += result.ioDecodeNanos
		pseudoState.decodedSampleCount += result.decodedSampleCount
		pseudoState.mappedSegments += result.mappedSegments
		pseudoState.fallbackSegments += result.fallbackSegments
		if result.cacheHit {
			pseudoState.cacheHits++
		}
		pseudoRememberCacheWrite(result.cacheWrite)
		if result.err != nil {
			state.errorText = result.err.Error()
			writeWorkspaceTrace(workspacecore.TraceEvent{Action: "pseudo3d_line_failed", Workspace: workspacecore.KindCrooked, Dataset: state.line.Name, Geometry: geometrycore.KindUnknown, Generation: pseudoState.workspaceGeneration, Err: result.err})
		} else {
			if result.outOfRange {
				state.geometry = result.geometry
				state.geometryCalibrated = result.geometryCalibrated
				state.geometryFingerprint = result.geometryFingerprint
				state.inRange, state.eligibilityKnown, state.selected, state.ready = false, true, false, false
				state.segments, state.texture, state.points, state.u = nil, nil, nil, nil
				pseudoListUpdating = true
				setPseudoListChecked(result.lineIndex, false)
				pseudoListUpdating = false
				refreshPseudoLineRow(result.lineIndex)
				continue
			}
			if !result.geometryCalibrated && result.calibrationAccepted && result.multiplier > 0 && pseudoState.projectMultiplier <= 0 {
				pseudoState.projectMultiplier = result.multiplier
				applyPseudoProjectMultiplierToReadyLines(result.multiplier)
			}
			if !result.geometryCalibrated && pseudoState.projectMultiplier > 0 && result.multiplier != pseudoState.projectMultiplier {
				ratio := pseudoState.projectMultiplier / math.Max(result.multiplier, 1e-12)
				if scaled, scaleErr := projectcore.ScaleGeometry(result.geometry, ratio); scaleErr == nil {
					state.geometry, state.multiplier, state.geometryCalibrated = scaled, pseudoState.projectMultiplier, false
					state.geometryFingerprint = ""
					state.segments, state.cachedSegments, state.texture = nil, nil, nil
					enqueuePseudoLine(result.lineIndex)
					continue
				}
			}
			pseudoCacheExactSegments(state, state.segments)
			state.geometry = result.geometry
			pseudoSetLineSegments(state, result.segments, true)
			state.multiplier, state.ready, state.errorText = result.multiplier, true, ""
			state.inRange, state.eligibilityKnown = true, true
			state.geometryCalibrated = result.geometryCalibrated
			state.geometryFingerprint = result.geometryFingerprint
			activeLineReady = activeLineReady || result.lineIndex == pseudoState.activeLine
			writeWorkspaceTrace(workspacecore.TraceEvent{Action: "pseudo3d_line_ready", Workspace: workspacecore.KindCrooked, Dataset: state.line.Name, Geometry: geometrycore.KindCrookedLine, Generation: pseudoState.workspaceGeneration})
		}
		refreshPseudoLineRow(result.lineIndex)
	}
	if !pseudoState.deferredLinesQueued {
		pseudoQueueDeferredLines()
	}
	trimPseudoTextureCache()
	selected, loaded, _, failed := pseudoLoadingCounts()
	if selected > 0 && loaded+failed >= selected {
		pseudoState.progressRenderArmed = false
		pseudoState.progressRenderGen++
		if !pseudoState.finalRenderPending && !pseudoState.finalRenderComplete {
			pseudoState.finalRenderPending = true
			queuePseudoFinalRender()
		}
	} else if acceptedResult {
		schedulePseudoProgressRender(activeLineReady)
	}
	updatePseudoStatus()
}

func applyPseudoProjectMultiplierToReadyLines(multiplier float64) {
	if multiplier <= 0 {
		return
	}
	for i := range pseudoState.lines {
		state := &pseudoState.lines[i]
		if state.geometry == nil || !state.ready || state.geometryCalibrated || state.multiplier == multiplier {
			continue
		}
		ratio := multiplier / math.Max(state.multiplier, 1e-12)
		if scaled, err := projectcore.ScaleGeometry(state.geometry, ratio); err == nil {
			state.geometry = scaled
			state.multiplier = multiplier
			state.geometryCalibrated = false
			state.geometryFingerprint = ""
			state.token++
			state.loading, state.ready, state.texture, state.segments, state.cachedSegments = false, false, nil, nil, nil
			enqueuePseudoLine(i)
		}
	}
}

func pseudoPalette(index int) [256]pseudo3dcore.Color {
	if index < 0 || index >= len(legacyAnchors) {
		index = 2
	}
	anchors := legacyAnchors[index]
	start, middle, end := tcolor(anchors[2]), tcolor(anchors[1]), tcolor(anchors[0])
	var palette [256]pseudo3dcore.Color
	for i := 0; i < 128; i++ {
		palette[i] = pseudo3dcore.Color{R: lerp(start.r, middle.r, i), G: lerp(start.g, middle.g, i), B: lerp(start.b, middle.b, i)}
		palette[128+i] = pseudo3dcore.Color{R: lerp(middle.r, end.r, i), G: lerp(middle.g, end.g, i), B: lerp(middle.b, end.b, i)}
	}
	return palette
}

func currentPseudoScene() (pseudo3dcore.Scene, []pseudoCurtainRef) {
	curtains := make([]pseudo3dcore.Curtain, 0, len(pseudoState.lines))
	refs := make([]pseudoCurtainRef, 0, len(pseudoState.lines))
	for i := range pseudoState.lines {
		state := &pseudoState.lines[i]
		if !state.selected || !state.inRange || !pseudoLineSelectable(state) {
			continue
		}
		if len(state.segments) > 0 {
			for segmentIndex, segment := range state.segments {
				curtains = append(curtains, pseudo3dcore.Curtain{ID: fmt.Sprintf("%s#%d", state.line.ID, segmentIndex), Name: state.line.Name,
					Points: append([]pseudo3dcore.Point(nil), segment.points...), U: append([]float64(nil), segment.u...),
					TimeStartMS: segment.timeStartMS, TimeEndMS: segment.timeEndMS, TimeMaxMS: segment.timeEndMS,
					Texture: segment.texture, Highlighted: i == pseudoState.activeLine})
				refs = append(refs, pseudoCurtainRef{lineIndex: i, segmentIndex: segmentIndex})
			}
		} else if len(state.points) >= 2 && len(state.points) == len(state.u) {
			curtains = append(curtains, pseudo3dcore.Curtain{ID: state.line.ID, Name: state.line.Name, Points: append([]pseudo3dcore.Point(nil), state.points...),
				U: append([]float64(nil), state.u...), TimeStartMS: state.timeStartMS, TimeEndMS: state.timeEndMS,
				TimeMaxMS: state.timeEndMS, Highlighted: i == pseudoState.activeLine})
			refs = append(refs, pseudoCurtainRef{lineIndex: i, segmentIndex: -1})
		}
	}
	scene := pseudo3dcore.Scene{Curtains: curtains, Style: crookedPseudoDisplayStyle()}
	timeBounds := pseudoState.timeRange.Normalized()
	if !timeBounds.Valid {
		timeBounds = pseudo3dcore.TimeRange{StartMS: 0, EndMS: 1, Valid: true}
		for i := range pseudoState.lines {
			if pseudoState.lines[i].selected {
				timeBounds.EndMS = math.Max(timeBounds.EndMS, pseudoState.lines[i].timeMaxMS)
			}
		}
	}
	if selected := pseudoState.spatialRange.Normalized(); selected.Valid {
		scene.Bounds = pseudo3dcore.Bounds{XMin: selected.XMin, XMax: selected.XMax, YMin: selected.YMin, YMax: selected.YMax,
			TimeMinMS: timeBounds.StartMS, TimeMaxMS: timeBounds.EndMS, Valid: true}
	} else if bounds := pseudoProjectRangeBounds(); bounds.Valid {
		scene.Bounds = pseudo3dcore.Bounds{XMin: bounds.XMin, XMax: bounds.XMax, YMin: bounds.YMin, YMax: bounds.YMax,
			TimeMinMS: timeBounds.StartMS, TimeMaxMS: timeBounds.EndMS, Valid: true}
	}
	return scene, refs
}

func queuePseudoSceneRender(force bool) {
	final := pseudoState.finalRenderPending && !pseudoState.finalRenderComplete
	queuePseudoSceneRenderScale(force, 1, final, false)
}

func queuePseudoScenePreviewRender() {
	queuePseudoSceneRenderScale(true, .5, false, true)
}

func queuePseudoProgressRender() {
	pseudoState.lastProgressRender = time.Now()
	pseudoState.progressRenderPending = true
	queuePseudoSceneRenderScale(true, .5, false, false)
}

func queuePseudoFinalRender() {
	queuePseudoSceneRenderScale(true, 1, true, false)
}

func schedulePseudoProgressRender(immediate bool) {
	if pseudoHwnd == 0 {
		return
	}
	remaining := 500*time.Millisecond - time.Since(pseudoState.lastProgressRender)
	if immediate || remaining <= 0 {
		pseudoState.progressRenderArmed = false
		pseudoState.progressRenderGen++
		queuePseudoProgressRender()
		return
	}
	if pseudoState.progressRenderArmed {
		return
	}
	pseudoState.progressRenderArmed = true
	pseudoState.progressRenderGen++
	scheduleGen, windowGen, cancel := pseudoState.progressRenderGen, pseudoState.windowGeneration, pseudoState.cancel
	go func() {
		timer := time.NewTimer(remaining)
		defer timer.Stop()
		select {
		case <-timer.C:
			if pseudoHwnd != 0 && windowGen == atomic.LoadInt64(&pseudoWindowGen) {
				pPostMessageW.Call(pseudoHwnd, WM_PSEUDO_PROGRESS_RENDER, uintptr(scheduleGen), 0)
			}
		case <-cancel:
		}
	}()
}

func queuePseudoSceneRenderScale(force bool, scale float64, final, refineAfterPreview bool) {
	if pseudoHwnd == 0 || pseudoState.renderQueue == nil {
		return
	}
	clearPseudoHoverOverlay()
	if !force && time.Since(pseudoState.lastRenderRequest) < 33*time.Millisecond {
		return
	}
	area := pseudoSceneRect()
	width, height := int(area.Right-area.Left), int(area.Bottom-area.Top)
	if width < 2 || height < 2 {
		return
	}
	scale = math.Max(.25, math.Min(1, scale))
	width, height = maxInt(2, int(math.Round(float64(width)*scale))), maxInt(2, int(math.Round(float64(height)*scale)))
	scene, refs := currentPseudoScene()
	request := pseudoRenderRequest{windowGen: pseudoState.windowGeneration, renderGen: atomic.AddInt64(&pseudoRenderGen, 1),
		scene: scene, camera: pseudoState.camera, palette: pseudoPalette(pseudoState.paletteIndex), width: width, height: height, refs: refs,
		preview: scale < .999, final: final, refineAfterPreview: refineAfterPreview}
	pseudoState.lastRenderRequest = time.Now()
	select {
	case pseudoState.renderQueue <- request:
	default:
		select {
		case <-pseudoState.renderQueue:
		default:
		}
		select {
		case pseudoState.renderQueue <- request:
		default:
		}
	}
}

func postPseudoRenderResult(result *pseudoRenderResult) {
	if result == nil || result.windowGen != atomic.LoadInt64(&pseudoWindowGen) || result.renderGen != atomic.LoadInt64(&pseudoRenderGen) || pseudoHwnd == 0 {
		return
	}
	pseudoPendingMu.Lock()
	pseudoRenderPending = result
	pseudoPendingMu.Unlock()
	pPostMessageW.Call(pseudoHwnd, WM_PSEUDO_SCENE_READY, 0, 0)
}

func handlePseudoRenderResult() {
	pseudoPendingMu.Lock()
	result := pseudoRenderPending
	pseudoRenderPending = nil
	pseudoPendingMu.Unlock()
	if result == nil || result.windowGen != pseudoState.windowGeneration || result.renderGen != atomic.LoadInt64(&pseudoRenderGen) {
		return
	}
	if result.err != nil {
		setText(pseudoControlsUI.status, "伪三维渲染失败："+result.err.Error())
		return
	}
	pseudoState.image, pseudoState.imageWidth, pseudoState.imageHeight, pseudoState.renderStats = result.image, result.width, result.height, result.stats
	pseudoState.renderedScene, pseudoState.renderedCamera, pseudoState.renderedRefs = result.scene, result.camera, result.refs
	pseudoState.hoverValid = false
	pInvalidateRect.Call(pseudoHwnd, 0, 0)
	if result.final {
		pseudoState.finalRenderPending = false
		pseudoState.finalRenderComplete = true
		pseudoState.loadCompletedDuration = time.Since(pseudoState.loadStarted)
		startPseudoDeferredCacheWrites()
		writeWorkspaceTrace(workspacecore.TraceEvent{Action: fmt.Sprintf("pseudo3d_complete_ms_%d_cache_%d_reads_%d_bytes_%d_logical_%d_support_%d_of_%d_samples_%d_mapped_%d_fallback_%d_decode_ms_%d", pseudoState.loadCompletedDuration.Milliseconds(), pseudoState.cacheHits, pseudoState.ioReadCalls, pseudoState.ioReadBytes, pseudoState.ioLogicalBytes,
			pseudoState.supportTraceCount, pseudoState.inputTraceCount, pseudoState.decodedSampleCount, pseudoState.mappedSegments, pseudoState.fallbackSegments, pseudoState.ioDecodeNanos/int64(time.Millisecond)),
			Workspace: workspacecore.KindCrooked, Dataset: pseudoProjectName(), Geometry: geometrycore.KindCrookedLine,
			Generation: pseudoState.workspaceGeneration, ProjectLines: len(pseudoState.lines)})
	}
	if result.preview && !result.refineAfterPreview {
		pseudoState.progressRenderPending = false
	}
	updatePseudoStatus()
	if result.preview && result.refineAfterPreview {
		queuePseudoSceneRender(true)
	}
}

func pseudoLoadingCounts() (selected, loaded, loading, failed int) {
	for i := range pseudoState.lines {
		state := &pseudoState.lines[i]
		if !state.selected {
			continue
		}
		selected++
		if len(state.segments) > 0 && state.ready {
			loaded++
		}
		if state.loading {
			loading++
		}
		if state.errorText != "" || (state.line != nil && state.line.OpenError != "") {
			failed++
		}
	}
	return
}

type pseudoProgressView struct {
	percent int
	text    string
	state   uintptr
}

func makePseudoProgressView(selected, loaded, failed int, refining bool) pseudoProgressView {
	if selected <= 0 {
		return pseudoProgressView{text: "准备中", state: pseudoPBSTNormal}
	}
	processed := loaded + failed
	if processed > selected {
		processed = selected
	}
	view := pseudoProgressView{percent: 100 * processed / selected, state: pseudoPBSTNormal}
	if failed > 0 {
		view.state = pseudoPBSTError
	}
	if processed >= selected {
		view.percent = 100
		if failed > 0 {
			view.text = fmt.Sprintf("完成 %d/%d，失败 %d", loaded, selected, failed)
		} else {
			view.text = fmt.Sprintf("完成 %d/%d ✓", loaded, selected)
		}
		return view
	}
	if refining {
		view.text = fmt.Sprintf("精修 %d/%d", processed, selected)
	} else {
		view.text = fmt.Sprintf("加载 %d/%d", processed, selected)
	}
	return view
}

func applyPseudoProgressView(view pseudoProgressView) {
	setText(pseudoControlsUI.progressStatus, view.text)
	pSendMessageW.Call(pseudoControlsUI.progress, pseudoPBMSetState, view.state, 0)
	pSendMessageW.Call(pseudoControlsUI.progress, PBM_SETPOS, uintptr(view.percent), 0)
}

func updatePseudoStatus() {
	if pseudoHwnd == 0 {
		return
	}
	defer updatePseudoExportButton()
	selected, loaded, loading, failed := pseudoLoadingCounts()
	progress := makePseudoProgressView(selected, loaded, failed, pseudoState.rangePreviewActive || pseudoState.timePreviewActive)
	if pseudoState.finalRenderPending {
		progress.percent, progress.text, progress.state = 100, "最终渲染", pseudoPBSTNormal
	} else if pseudoState.progressRenderPending {
		progress.text = fmt.Sprintf("合并预览 %d/%d", loaded+failed, selected)
	} else if loaded+failed < selected && !pseudoState.rangePreviewActive && !pseudoState.timePreviewActive {
		stage := "规划支撑道"
		if pseudoState.loadStage >= int(segy.RenderStageDecodeMap) {
			stage = "解码与映射"
		} else if pseudoState.loadStage >= int(segy.RenderStageRead) {
			stage = "读取幕布"
		}
		progress.text = fmt.Sprintf("%s %d/%d", stage, loaded+failed, selected)
	} else if loaded+failed >= selected && selected > 0 && pseudoState.finalRenderComplete {
		if failed > 0 {
			progress.text = fmt.Sprintf("完成 %d/%d，失败 %d，%.2fs", loaded, selected, failed, pseudoState.loadCompletedDuration.Seconds())
		} else {
			progress.text = fmt.Sprintf("完成 %d/%d ✓，%.2fs", loaded, selected, pseudoState.loadCompletedDuration.Seconds())
		}
	}
	applyPseudoProgressView(progress)
	if pseudoState.linkedCursorOutside {
		if pseudoState.linkedCursorTimeOutside {
			setText(pseudoControlsUI.status, "当前二维时间在伪三维时间范围外；时间窗保持不变。")
		} else {
			setText(pseudoControlsUI.status, "当前二维位置在伪三维范围外；AOI 保持不变。")
		}
		return
	}
	prefix := ""
	if pseudoState.spatialRange.Valid {
		prefix = "范围内"
	}
	timeLabel := "完整时间"
	if selectedTime := pseudoState.timeRange.Normalized(); selectedTime.Valid {
		timeLabel = fmt.Sprintf("T %.3g–%.3g ms", selectedTime.StartMS, selectedTime.EndMS)
		if prefix != "" {
			prefix += "·"
		}
		prefix += "时间窗内"
	}
	if selected == 0 {
		setText(pseudoControlsUI.status, prefix+"未选择可显示测线；请调整范围或在右侧勾选测线。")
	} else if pseudoState.finalRenderPending {
		setText(pseudoControlsUI.status, fmt.Sprintf("最终渲染：%d 条幕布（缓存命中 %d）", loaded, pseudoState.cacheHits))
	} else if loaded+failed < selected {
		if pseudoState.rangePreviewActive || pseudoState.timePreviewActive {
			kind := "AOI 即时预览"
			if pseudoState.timePreviewActive {
				kind = "时间裁剪预览"
			}
			setText(pseudoControlsUI.status, fmt.Sprintf("%s：复用 %d 条，更新 %d 条 | %s精修 %d/%d（Reader ≤ %d）", kind, pseudoState.rangeReusedLines, pseudoState.rangeUpdatingLines, prefix, loaded+failed, selected, pseudoLineWorkerCount()))
		} else {
			setText(pseudoControlsUI.status, fmt.Sprintf("%s加载幕布 %d/%d（并发 Reader ≤ %d，加载中 %d）", prefix, loaded+failed, selected, pseudoLineWorkerCount(), loading))
		}
	} else {
		pseudoState.rangePreviewActive, pseudoState.timePreviewActive = false, false
		support := "支撑道：缓存恢复"
		if pseudoState.inputTraceCount > 0 {
			support = fmt.Sprintf("支撑道 %d/%d（%.1f%%）", pseudoState.supportTraceCount, pseudoState.inputTraceCount,
				float64(pseudoState.supportTraceCount)*100/float64(pseudoState.inputTraceCount))
		}
		setText(pseudoControlsUI.status, fmt.Sprintf("伪三维完成：显示 %d 条，失败 %d 条，%s，耗时 %.2fs，%s，映射 %d，回退 %d，缓存命中 %d | 左键旋转，Shift+左键平移，右键/滚轮缩放",
			loaded, failed, timeLabel, pseudoState.loadCompletedDuration.Seconds(), support, pseudoState.mappedSegments, pseudoState.fallbackSegments, pseudoState.cacheHits))
	}
}

func pseudoProjectName() string {
	if pseudoState.project == nil {
		return "crooked-project"
	}
	name := pseudoState.project.Root
	if name == "" {
		return "crooked-project"
	}
	return filepath.Base(name)
}

func applyPseudoSelection(index int, checked, updateControl bool) {
	if index < 0 || index >= len(pseudoState.lines) {
		return
	}
	state := &pseudoState.lines[index]
	valid := pseudoLineSelectable(state)
	if !valid {
		checked = false
	}
	if updateControl {
		pseudoListUpdating = true
		setPseudoListChecked(index, checked)
		pseudoListUpdating = false
	}
	if state.selected == checked {
		return
	}
	state.selected = checked
	state.token++
	state.loading = false
	if !checked {
		pseudoCacheExactSegments(state, state.segments)
		pseudoSetLineSegments(state, nil, false)
		state.errorText = ""
	} else {
		if !pseudoRestoreLineFromCache(state) {
			enqueuePseudoLine(index)
		}
	}
	refreshPseudoLineRow(index)
	updatePseudoStatus()
	queuePseudoSceneRender(true)
}

func setAllPseudoSelections(checked bool) {
	pseudoListUpdating = true
	for i := range pseudoState.lines {
		state := &pseudoState.lines[i]
		valid := pseudoLineSelectable(state)
		want := checked && valid
		setPseudoListChecked(i, want)
		if state.selected != want {
			state.selected = want
			state.token++
			state.loading = false
			if !want {
				pseudoCacheExactSegments(state, state.segments)
				pseudoSetLineSegments(state, nil, false)
				state.errorText = ""
			} else {
				pseudoRestoreLineFromCache(state)
			}
		}
	}
	pseudoListUpdating = false
	if checked {
		if pseudoState.activeLine >= 0 {
			enqueuePseudoLine(pseudoState.activeLine)
		}
		for i := range pseudoState.lines {
			if i != pseudoState.activeLine {
				enqueuePseudoLine(i)
			}
		}
	}
	for i := range pseudoState.lines {
		refreshPseudoLineRow(i)
	}
	updatePseudoStatus()
	queuePseudoSceneRender(true)
}

func showOnlyCurrentPseudoLine() {
	if pseudoState.activeLine < 0 || pseudoState.activeLine >= len(pseudoState.lines) {
		return
	}
	pseudoListUpdating = true
	for i := range pseudoState.lines {
		want := i == pseudoState.activeLine && pseudoLineSelectable(&pseudoState.lines[i])
		setPseudoListChecked(i, want)
		state := &pseudoState.lines[i]
		if state.selected != want {
			state.selected = want
			state.token++
			state.loading = false
			if !want {
				pseudoCacheExactSegments(state, state.segments)
				pseudoSetLineSegments(state, nil, false)
				state.errorText = ""
			} else {
				pseudoRestoreLineFromCache(state)
			}
		}
	}
	pseudoListUpdating = false
	enqueuePseudoLine(pseudoState.activeLine)
	for i := range pseudoState.lines {
		refreshPseudoLineRow(i)
	}
	updatePseudoStatus()
	queuePseudoSceneRender(true)
}

func pseudoReloadSelectedGain(value float64) {
	if pseudoHwnd == 0 {
		return
	}
	pseudoState.gainPercent = math.Max(0, math.Min(49, value))
	pseudoBeginLoadCycle(true)
	setText(pseudoControlsUI.gainValue, fmt.Sprintf("%.0f%%", pseudoState.gainPercent))
	for i := range pseudoState.lines {
		state := &pseudoState.lines[i]
		state.cachedSegments = nil
		if !state.selected || state.line == nil || !state.line.Valid() {
			continue
		}
		state.token++
		state.loading, state.ready, state.texture, state.segments, state.errorText = false, false, nil, nil, ""
		enqueuePseudoLine(i)
		refreshPseudoLineRow(i)
	}
	updatePseudoStatus()
	queuePseudoSceneRender(true)
}

func pseudoApplyPalette(index int) {
	if pseudoHwnd == 0 || index < 0 || index >= len(paletteNames) {
		return
	}
	pseudoState.paletteIndex = index
	pSendMessageW.Call(pseudoControlsUI.palette, CB_SETCURSEL, uintptr(index), 0)
	queuePseudoSceneRender(true)
}

func pseudoApplyCrookedStyle() {
	if pseudoHwnd == 0 {
		return
	}
	if pseudoControlsUI.style != 0 {
		pSendMessageW.Call(pseudoControlsUI.style, CB_SETCURSEL, uintptr(crookedStyleComboIndex(crookedState.styleMode)), 0)
	}
	pseudoState.hoverValid = false
	queuePseudoSceneRender(true)
	pInvalidateRect.Call(pseudoHwnd, 0, 0)
}

func changePseudoGain(delta float64) {
	value := math.Max(0, math.Min(49, pseudoState.gainPercent+delta))
	if value == pseudoState.gainPercent {
		return
	}
	crookedState.gainPercent = value
	crookedDefaults.GainPercent = value
	setText(crookedControlsUI.gainValue, fmt.Sprintf("%.0f%%", value))
	saveCrookedDefaults()
	if crookedState.geometry != nil {
		startCrookedRender()
	}
	pseudoReloadSelectedGain(value)
}

func changePseudoPalette(index int) {
	if index < 0 || index >= len(paletteNames) || index == pseudoState.paletteIndex {
		return
	}
	crookedState.paletteIndex = index
	crookedDefaults.PaletteIndex = index
	pSendMessageW.Call(crookedControlsUI.palette, CB_SETCURSEL, uintptr(index), 0)
	saveCrookedDefaults()
	if len(crookedState.indices) > 0 {
		crookedState.bgra = crookedPaletteBGRA(crookedState.indices, index)
		invalidateCrookedBase()
	}
	pseudoApplyPalette(index)
}

func adjustPseudoVertical(increase bool) {
	adjustPseudoAxis(2, increase)
	setText(pseudoControlsUI.zValue, fmt.Sprintf("×%.2f", pseudoState.camera.VerticalScale))
}

func resetPseudoCamera() {
	defaults := pseudoCameraFromDefaults()
	pseudoState.camera.Azimuth, pseudoState.camera.Elevation = defaults.Azimuth, defaults.Elevation
	pseudoState.camera.FOV, pseudoState.camera.Zoom = defaults.FOV, defaults.Zoom
	pseudoState.camera.PanX, pseudoState.camera.PanY = defaults.PanX, defaults.PanY
	pseudoState.camera = pseudoState.camera.Normalized()
	setPseudoFOVCombo(pseudoState.camera.FOV)
	setText(pseudoControlsUI.zValue, fmt.Sprintf("×%.2f", pseudoState.camera.VerticalScale))
	queuePseudoSceneRender(true)
}

func adjustPseudoAxis(axis int, increase bool) {
	value := pseudoState.camera.VerticalScale
	if axis == 0 {
		value = pseudoState.camera.AxisXScale
	} else if axis == 1 {
		value = pseudoState.camera.AxisYScale
	}
	index := nearestPseudoOption(pseudoAxisFactorOptions, value)
	if increase && index < len(pseudoAxisFactorOptions)-1 {
		index++
	} else if !increase && index > 0 {
		index--
	}
	value = pseudoAxisFactorOptions[index]
	switch axis {
	case 0:
		pseudoState.camera.AxisXScale = value
	case 1:
		pseudoState.camera.AxisYScale = value
	default:
		pseudoState.camera.VerticalScale = value
	}
	setPseudoAxisCombos()
	setText(pseudoControlsUI.zValue, fmt.Sprintf("×%.2f", pseudoState.camera.VerticalScale))
	queuePseudoSceneRender(true)
}

func setPseudoAxisFromCombo(axis, index int) {
	if index < 0 || index >= len(pseudoAxisFactorOptions) {
		return
	}
	value := pseudoAxisFactorOptions[index]
	switch axis {
	case 0:
		pseudoState.camera.AxisXScale = value
	case 1:
		pseudoState.camera.AxisYScale = value
	default:
		pseudoState.camera.VerticalScale = value
		setText(pseudoControlsUI.zValue, fmt.Sprintf("×%.2f", value))
	}
	queuePseudoSceneRender(true)
}

func adjustPseudoFOV(increase bool) {
	index := nearestPseudoOption(pseudoFOVOptions, pseudoState.camera.FOV)
	if increase && index < len(pseudoFOVOptions)-1 {
		index++
	} else if !increase && index > 0 {
		index--
	}
	pseudoState.camera.FOV = pseudoFOVOptions[index]
	setPseudoFOVCombo(pseudoState.camera.FOV)
	queuePseudoSceneRender(true)
}

func adjustPseudoOverallScale(increase bool) {
	if increase {
		pseudoState.camera.Zoom *= 1.10
	} else {
		pseudoState.camera.Zoom /= 1.10
	}
	pseudoState.camera.Zoom = math.Max(.40, math.Min(5, pseudoState.camera.Zoom))
	queuePseudoSceneRender(true)
}

func cyclePseudoPalette(delta int) {
	current := 0
	for i, palette := range volumePaletteOrder {
		if palette == pseudoState.paletteIndex {
			current = i
			break
		}
	}
	current = (current + delta + len(volumePaletteOrder)) % len(volumePaletteOrder)
	changePseudoPalette(volumePaletteOrder[current])
}

func pseudoPickAtWindow(x, y int) (pseudo3dcore.PickResult, pseudoCurtainRef, bool) {
	area := pseudoSceneRect()
	if !rectContains(area, x, y) || !pseudoState.renderedScene.Bounds.Valid {
		return pseudo3dcore.PickResult{}, pseudoCurtainRef{}, false
	}
	width, height := int(area.Right-area.Left), int(area.Bottom-area.Top)
	pick, ok := pseudo3dcore.Pick(pseudoState.renderedScene, pseudoState.renderedCamera, width, height,
		float64(x-int(area.Left)), float64(y-int(area.Top)))
	if !ok || pick.CurtainIndex < 0 || pick.CurtainIndex >= len(pseudoState.renderedRefs) {
		return pseudo3dcore.PickResult{}, pseudoCurtainRef{}, false
	}
	return pick, pseudoState.renderedRefs[pick.CurtainIndex], true
}

func pseudoPickPosition(pick pseudo3dcore.PickResult, ref pseudoCurtainRef) (int, int64, float64, string, bool) {
	if ref.lineIndex < 0 || ref.lineIndex >= len(pseudoState.lines) {
		return 0, 0, 0, "-", false
	}
	state := &pseudoState.lines[ref.lineIndex]
	geometry := state.geometry
	if geometry == nil || len(geometry.TraceIndices) == 0 || len(geometry.Distance) != len(geometry.TraceIndices) {
		return 0, 0, 0, "-", false
	}
	start, end := geometry.Distance[0], geometry.Distance[len(geometry.Distance)-1]
	if ref.segmentIndex >= 0 && ref.segmentIndex < len(state.segments) {
		segment := &state.segments[ref.segmentIndex]
		start, end = segment.positionStart, segment.positionEnd
	}
	target := start + math.Max(0, math.Min(1, pick.U))*(end-start)
	position, best := 0, math.Inf(1)
	for i, value := range geometry.Distance {
		if delta := math.Abs(value - target); delta < best {
			position, best = i, delta
		}
	}
	cdp := "-"
	if position < len(geometry.HasCDP) && geometry.HasCDP[position] {
		cdp = strconv.FormatInt(int64(geometry.CDP[position]), 10)
	}
	return position, geometry.TraceIndices[position], pick.TimeMS, cdp, true
}

func pseudoCurtainOverlayRect(curtainIndex int) (RECT, bool) {
	if curtainIndex < 0 || curtainIndex >= len(pseudoState.renderedScene.Curtains) {
		return RECT{}, false
	}
	area := pseudoSceneRect()
	width, height := int(area.Right-area.Left), int(area.Bottom-area.Top)
	projection := pseudo3dcore.ProjectCurtain(pseudoState.renderedScene, pseudoState.renderedCamera, width, height, curtainIndex)
	if !projection.Valid {
		return RECT{}, false
	}
	left, top, right, bottom := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, points := range [][]pseudo3dcore.ScreenPoint{projection.Top, projection.Bottom} {
		for _, point := range points {
			left, right = math.Min(left, point.X), math.Max(right, point.X)
			top, bottom = math.Min(top, point.Y), math.Max(bottom, point.Y)
		}
	}
	if math.IsNaN(left) || math.IsInf(left, 0) || math.IsNaN(top) || math.IsInf(top, 0) {
		return RECT{}, false
	}
	result := RECT{Left: int32(math.Floor(left)) + area.Left - 5, Top: int32(math.Floor(top)) + area.Top - 5,
		Right: int32(math.Ceil(right)) + area.Left + 6, Bottom: int32(math.Ceil(bottom)) + area.Top + 6}
	result.Left, result.Top = int32(maxInt(int(result.Left), int(area.Left))), int32(maxInt(int(result.Top), int(area.Top)))
	result.Right, result.Bottom = int32(minInt(int(result.Right), int(area.Right))), int32(minInt(int(result.Bottom), int(area.Bottom)))
	return result, result.Right > result.Left && result.Bottom > result.Top
}

func invalidatePseudoCurtainOverlay(curtainIndex int) {
	if rect, ok := pseudoCurtainOverlayRect(curtainIndex); ok {
		pInvalidateRect.Call(pseudoHwnd, uintptr(unsafe.Pointer(&rect)), 0)
	}
}

func clearPseudoHoverOverlay() {
	if !pseudoState.hoverValid {
		return
	}
	old := pseudoState.hoverPick.CurtainIndex
	pseudoState.hoverValid = false
	invalidatePseudoCurtainOverlay(old)
}

func updatePseudoHover(x, y int) {
	if !pseudoState.lastHoverUpdate.IsZero() && time.Since(pseudoState.lastHoverUpdate) < 33*time.Millisecond {
		return
	}
	pseudoState.lastHoverUpdate = time.Now()
	oldValid, oldCurtain := pseudoState.hoverValid, pseudoState.hoverPick.CurtainIndex
	pick, ref, ok := pseudoPickAtWindow(x, y)
	if !ok {
		pseudoState.hoverValid = false
		if oldValid {
			invalidatePseudoCurtainOverlay(oldCurtain)
			updatePseudoStatus()
		}
		return
	}
	pseudoState.hoverPick, pseudoState.hoverValid = pick, true
	if !oldValid || oldCurtain != pick.CurtainIndex {
		if oldValid {
			invalidatePseudoCurtainOverlay(oldCurtain)
		}
		invalidatePseudoCurtainOverlay(pick.CurtainIndex)
	}
	position, trace, timeMS, cdp, details := pseudoPickPosition(pick, ref)
	if details {
		_ = position
		setText(pseudoControlsUI.status, fmt.Sprintf("%s | Trace %d | CDP %s | X %.3f Y %.3f | Time %.3f ms | 双击返回二维",
			pick.CurtainName, trace+1, cdp, pick.World.X, pick.World.Y, timeMS))
	}
}

func navigatePseudoPickToCrooked(x, y int) {
	pick, ref, ok := pseudoPickAtWindow(x, y)
	if !ok {
		setText(pseudoControlsUI.status, "双击未命中已加载的地震幕布。")
		return
	}
	_, trace, timeMS, _, ok := pseudoPickPosition(pick, ref)
	if !ok {
		setText(pseudoControlsUI.status, "命中幕布尚未具备可返回的道号信息。")
		return
	}
	pseudoState.activeLine = ref.lineIndex
	setPseudoListSelected(ref.lineIndex)
	queuePseudoSceneRender(true)
	navigateCrookedFromPseudo(ref.lineIndex, trace, timeMS)
}

func pseudoProjectRangeBounds() pseudo3dcore.XYRange {
	bounds := crookedProjectBounds()
	add := func(x, y float64) {
		if !bounds.HasXY {
			bounds.XMin, bounds.XMax, bounds.YMin, bounds.YMax, bounds.HasXY = x, x, y, y, true
			return
		}
		bounds.XMin, bounds.XMax = math.Min(bounds.XMin, x), math.Max(bounds.XMax, x)
		bounds.YMin, bounds.YMax = math.Min(bounds.YMin, y), math.Max(bounds.YMax, y)
	}
	for i := range pseudoState.lines {
		if geometry := pseudoState.lines[i].geometry; geometry != nil {
			for j := range geometry.X {
				add(geometry.X[j], geometry.Y[j])
			}
		}
	}
	if !bounds.HasXY {
		return pseudo3dcore.XYRange{}
	}
	return pseudo3dcore.XYRange{XMin: bounds.XMin, XMax: bounds.XMax, YMin: bounds.YMin, YMax: bounds.YMax, Valid: true}
}

func closestPseudoSegmentT(px, py float64, a, b pseudo3dcore.ScreenPoint) (float64, float64) {
	dx, dy := b.X-a.X, b.Y-a.Y
	den := dx*dx + dy*dy
	if den <= 1e-12 {
		return 0, math.Hypot(px-a.X, py-a.Y)
	}
	t := ((px-a.X)*dx + (py-a.Y)*dy) / den
	t = math.Max(0, math.Min(1, t))
	return t, math.Hypot(px-(a.X+t*dx), py-(a.Y+t*dy))
}

func pseudoFrameHandleAt(x, y int) (pseudoRangeHandle, float64, bool) {
	area := pseudoSceneRect()
	if !rectContains(area, x, y) || !pseudoState.renderedScene.Bounds.Valid {
		return 0, 0, false
	}
	frame := pseudo3dcore.ProjectFrame(pseudoState.renderedScene, pseudoState.renderedCamera, int(area.Right-area.Left), int(area.Bottom-area.Top))
	if !frame.Valid {
		return 0, 0, false
	}
	px, py := float64(x-int(area.Left))+.5, float64(y-int(area.Top))+.5
	cornerHandles := [4]pseudoRangeHandle{pseudoRangeXMin | pseudoRangeYMin, pseudoRangeXMax | pseudoRangeYMin,
		pseudoRangeXMax | pseudoRangeYMax, pseudoRangeXMin | pseudoRangeYMax}
	bestDistance, bestHandle, bestPlane := math.Inf(1), pseudoRangeHandle(0), 0.0
	for i := 0; i < 4; i++ {
		for _, candidate := range []struct {
			point pseudo3dcore.ScreenPoint
			plane float64
		}{{frame.Base[i], 0}, {frame.Top[i], 1}} {
			if distance := math.Hypot(px-candidate.point.X, py-candidate.point.Y); distance <= 10 && distance < bestDistance {
				bestDistance, bestHandle, bestPlane = distance, cornerHandles[i], candidate.plane
			}
		}
		t, distance := closestPseudoSegmentT(px, py, frame.Base[i], frame.Top[i])
		if distance <= 8 && distance < bestDistance {
			bestDistance, bestHandle, bestPlane = distance, cornerHandles[i], t
		}
	}
	if bestHandle != 0 {
		return bestHandle, bestPlane, true
	}
	edgeHandles := [4]pseudoRangeHandle{pseudoRangeYMin, pseudoRangeXMax, pseudoRangeYMax, pseudoRangeXMin}
	for edge := 0; edge < 4; edge++ {
		next := (edge + 1) % 4
		for _, candidate := range []struct {
			a, b  pseudo3dcore.ScreenPoint
			plane float64
		}{{frame.Base[edge], frame.Base[next], 0}, {frame.Top[edge], frame.Top[next], 1}} {
			_, distance := closestPseudoSegmentT(px, py, candidate.a, candidate.b)
			if distance <= 8 && distance < bestDistance {
				bestDistance, bestHandle, bestPlane = distance, edgeHandles[edge], candidate.plane
			}
		}
	}
	return bestHandle, bestPlane, bestHandle != 0
}

func setPseudoRangeCursor(handle pseudoRangeHandle) {
	if handle == 0 {
		return
	}
	cursorID := uintptr(IDC_SIZEALL)
	xAxis := handle&(pseudoRangeXMin|pseudoRangeXMax) != 0
	yAxis := handle&(pseudoRangeYMin|pseudoRangeYMax) != 0
	if xAxis && !yAxis {
		cursorID = IDC_SIZEWE
	} else if yAxis && !xAxis {
		cursorID = IDC_SIZENS
	}
	cursor, _, _ := pLoadCursorW.Call(0, cursorID)
	if cursor != 0 {
		pSetCursor.Call(cursor)
	}
}

func resizedPseudoRange(original, project pseudo3dcore.XYRange, handle pseudoRangeHandle, world pseudo3dcore.Point) pseudo3dcore.XYRange {
	original, project = original.Normalized(), project.Normalized()
	if !original.Valid || !project.Valid || handle == 0 {
		return original
	}
	result := original
	minX, minY := math.Max((project.XMax-project.XMin)*.01, 1e-12), math.Max((project.YMax-project.YMin)*.01, 1e-12)
	if handle&pseudoRangeXMin != 0 {
		result.XMin = math.Max(project.XMin, math.Min(original.XMax-minX, world.X))
	}
	if handle&pseudoRangeXMax != 0 {
		result.XMax = math.Min(project.XMax, math.Max(original.XMin+minX, world.X))
	}
	if handle&pseudoRangeYMin != 0 {
		result.YMin = math.Max(project.YMin, math.Min(original.YMax-minY, world.Y))
	}
	if handle&pseudoRangeYMax != 0 {
		result.YMax = math.Min(project.YMax, math.Max(original.YMin+minY, world.Y))
	}
	return result.Normalized()
}

func beginPseudoRangeDrag(x, y int) bool {
	clearPseudoHoverOverlay()
	handle, plane, ok := pseudoFrameHandleAt(x, y)
	if !ok {
		setText(pseudoControlsUI.status, "Ctrl+左键请从 XY 外框边/角或时间起止面中央开始拖动。")
		return false
	}
	setPseudoRangeCursor(handle)
	original := pseudoState.spatialRange.Normalized()
	if !original.Valid {
		original = pseudoProjectRangeBounds()
	}
	if !original.Valid {
		return false
	}
	pseudoState.rangeDragging, pseudoState.rangeHandle, pseudoState.rangePlane = true, handle, plane
	pseudoState.rangeOriginal, pseudoState.rangeDraft = original, original
	updatePseudoExportButton()
	pSetCapture.Call(pseudoHwnd)
	return true
}

func updatePseudoRangeDrag(x, y int) {
	if !pseudoState.rangeDragging {
		return
	}
	area := pseudoSceneRect()
	world, ok := pseudo3dcore.UnprojectTimePlane(pseudoState.renderedScene, pseudoState.renderedCamera, int(area.Right-area.Left), int(area.Bottom-area.Top),
		float64(x-int(area.Left))+.5, float64(y-int(area.Top))+.5, pseudoState.rangePlane)
	if !ok {
		return
	}
	next := resizedPseudoRange(pseudoState.rangeOriginal, pseudoProjectRangeBounds(), pseudoState.rangeHandle, world)
	if !next.Valid {
		return
	}
	pseudoState.rangeDraft = next
	if pseudoState.rangeLastPaint.IsZero() || time.Since(pseudoState.rangeLastPaint) >= 33*time.Millisecond {
		pseudoState.rangeLastPaint = time.Now()
		area := pseudoSceneRect()
		pInvalidateRect.Call(pseudoHwnd, uintptr(unsafe.Pointer(&area)), 0)
		setText(pseudoControlsUI.status, fmt.Sprintf("范围预览 | X %.3f – %.3f | Y %.3f – %.3f（松开后加载）", next.XMin, next.XMax, next.YMin, next.YMax))
	}
}

func cancelPseudoRangeDrag() {
	if !pseudoState.rangeDragging {
		return
	}
	pseudoState.rangeDragging, pseudoState.rangeHandle = false, 0
	pseudoState.rangeDraft = pseudo3dcore.XYRange{}
	pReleaseCapture.Call()
	area := pseudoSceneRect()
	pInvalidateRect.Call(pseudoHwnd, uintptr(unsafe.Pointer(&area)), 0)
	setText(pseudoControlsUI.status, "已取消伪三维范围调整。")
	updatePseudoExportButton()
}

func finishPseudoRangeDrag() {
	if !pseudoState.rangeDragging {
		return
	}
	draft := pseudoState.rangeDraft.Normalized()
	pseudoState.rangeDragging, pseudoState.rangeHandle = false, 0
	pReleaseCapture.Call()
	if !draft.Valid || !commitCrookedPseudoRange(draft, "pseudo") {
		setText(pseudoControlsUI.status, "伪三维范围调整无效，已保留原范围。")
	}
	updatePseudoExportButton()
}

func pseudoTimeFrameHandleAt(x, y int) (pseudoTimeHandle, bool) {
	area := pseudoSceneRect()
	if !rectContains(area, x, y) || !pseudoState.renderedScene.Bounds.Valid {
		return 0, false
	}
	frame := pseudo3dcore.ProjectFrame(pseudoState.renderedScene, pseudoState.renderedCamera, int(area.Right-area.Left), int(area.Bottom-area.Top))
	if !frame.Valid {
		return 0, false
	}
	centroid := func(points [4]pseudo3dcore.ScreenPoint) (float64, float64) {
		x, y := 0.0, 0.0
		for _, point := range points {
			x, y = x+point.X, y+point.Y
		}
		return x / 4, y / 4
	}
	baseX, baseY := centroid(frame.Base)
	topX, topY := centroid(frame.Top)
	px, py := float64(x-int(area.Left))+.5, float64(y-int(area.Top))+.5
	baseDistance, topDistance := math.Hypot(px-baseX, py-baseY), math.Hypot(px-topX, py-topY)
	if baseDistance <= 14 && baseDistance <= topDistance {
		return pseudoTimeStart, true
	}
	if topDistance <= 14 {
		return pseudoTimeEnd, true
	}
	return 0, false
}

func resizedPseudoTimeRange(original, project pseudo3dcore.TimeRange, handle pseudoTimeHandle, timeMS float64) pseudo3dcore.TimeRange {
	original, project = original.Normalized(), project.Normalized()
	if !original.Valid || !project.Valid || handle == 0 || math.IsNaN(timeMS) || math.IsInf(timeMS, 0) {
		return original
	}
	minimum := math.Max((project.EndMS-project.StartMS)*.01, 1e-6)
	result := original
	if handle == pseudoTimeStart {
		result.StartMS = math.Max(project.StartMS, math.Min(original.EndMS-minimum, timeMS))
	} else if handle == pseudoTimeEnd {
		result.EndMS = math.Min(project.EndMS, math.Max(original.StartMS+minimum, timeMS))
	}
	return result.Normalized()
}

func beginPseudoTimeDrag(x, y int) bool {
	handle, ok := pseudoTimeFrameHandleAt(x, y)
	if !ok {
		return false
	}
	original := pseudoState.timeRange.Normalized()
	if !original.Valid {
		original = crookedProjectTimeBounds()
	}
	project := crookedProjectTimeBounds()
	if !original.Valid || !project.Valid {
		return false
	}
	area := pseudoSceneRect()
	frame := pseudo3dcore.ProjectFrame(pseudoState.renderedScene, pseudoState.renderedCamera, int(area.Right-area.Left), int(area.Bottom-area.Top))
	if !frame.Valid {
		return false
	}
	vectorX, vectorY := 0.0, 0.0
	for i := 0; i < 4; i++ {
		vectorX += frame.Top[i].X - frame.Base[i].X
		vectorY += frame.Top[i].Y - frame.Base[i].Y
	}
	vectorX, vectorY = vectorX/4, vectorY/4
	if vectorX*vectorX+vectorY*vectorY <= 16 {
		return false
	}
	clearPseudoHoverOverlay()
	pseudoState.timeDragging, pseudoState.timeHandle = true, handle
	pseudoState.timeOriginal, pseudoState.timeDraft = original, original
	pseudoState.timeDragStartX, pseudoState.timeDragStartY = x, y
	pseudoState.timeDragVectorX, pseudoState.timeDragVectorY = vectorX, vectorY
	updatePseudoExportButton()
	cursor, _, _ := pLoadCursorW.Call(0, uintptr(IDC_SIZENS))
	if cursor != 0 {
		pSetCursor.Call(cursor)
	}
	pSetCapture.Call(pseudoHwnd)
	return true
}

func updatePseudoTimeDrag(x, y int) {
	if !pseudoState.timeDragging {
		return
	}
	vx, vy := pseudoState.timeDragVectorX, pseudoState.timeDragVectorY
	denominator := vx*vx + vy*vy
	if denominator <= 1e-9 {
		return
	}
	dx, dy := float64(x-pseudoState.timeDragStartX), float64(y-pseudoState.timeDragStartY)
	deltaFraction := (dx*vx + dy*vy) / denominator
	span := pseudoState.timeOriginal.EndMS - pseudoState.timeOriginal.StartMS
	target := pseudoState.timeOriginal.StartMS + deltaFraction*span
	if pseudoState.timeHandle == pseudoTimeEnd {
		target = pseudoState.timeOriginal.EndMS + deltaFraction*span
	}
	next := resizedPseudoTimeRange(pseudoState.timeOriginal, crookedProjectTimeBounds(), pseudoState.timeHandle, target)
	if !next.Valid {
		return
	}
	pseudoState.timeDraft = next
	if pseudoState.timeLastPaint.IsZero() || time.Since(pseudoState.timeLastPaint) >= 33*time.Millisecond {
		pseudoState.timeLastPaint = time.Now()
		area := pseudoSceneRect()
		pInvalidateRect.Call(pseudoHwnd, uintptr(unsafe.Pointer(&area)), 0)
		setText(pseudoControlsUI.status, fmt.Sprintf("时间范围预览 %.3g–%.3g ms（松开后加载）", next.StartMS, next.EndMS))
	}
}

func cancelPseudoTimeDrag() {
	if !pseudoState.timeDragging {
		return
	}
	pseudoState.timeDragging, pseudoState.timeHandle = false, 0
	pseudoState.timeDraft = pseudo3dcore.TimeRange{}
	pReleaseCapture.Call()
	area := pseudoSceneRect()
	pInvalidateRect.Call(pseudoHwnd, uintptr(unsafe.Pointer(&area)), 0)
	setText(pseudoControlsUI.status, "已取消伪三维时间范围调整。")
	updatePseudoExportButton()
}

func finishPseudoTimeDrag() {
	if !pseudoState.timeDragging {
		return
	}
	draft := pseudoState.timeDraft.Normalized()
	pseudoState.timeDragging, pseudoState.timeHandle = false, 0
	pReleaseCapture.Call()
	if !draft.Valid || !commitCrookedPseudoTimeRange(draft, "pseudo") {
		setText(pseudoControlsUI.status, "伪三维时间范围调整无效，已保留原时间窗。")
	}
	updatePseudoExportButton()
}

func pseudoShortcutHelpText() string {
	return APP_NAME + " 伪三维快捷键\n\n" +
		"X / Shift+X    增大 / 减小 X 轴长度（0.10×–8.00×）\n" +
		"Y / Shift+Y    增大 / 减小 Y 轴长度（0.10×–8.00×）\n" +
		"Z / Shift+Z    增大 / 减小 Z（Time）轴长度\n" +
		"R / Shift+R    整体放大 / 缩小\n" +
		"F / Shift+F    增大 / 减小 FOV（0° 为正交投影）\n" +
		"Q / W          显示增益 +1% / -1%\n" +
		"E / Shift+E    下一 / 上一色标\n" +
		"A              显示当前 Camera / Axis 参数\n" +
		"Space          恢复已保存的默认 Camera\n\n" +
		"鼠标：左键旋转，Shift+左键平移，右键拖动/滚轮缩放。\n" +
		"双击幕布：返回对应二维弯线、道和时间。\n" +
		"Ctrl+拖动 XY 外框边/角：调整伪三维范围。\n" +
		"Ctrl+拖动时间起止面中央：调整伪三维时间窗；Esc 取消。\n" +
		"导出范围：仅导出当前勾选并已显示完成的测线；范围由 XY AOI 和时间窗决定，\n" +
		"          不包含相机旋转、缩放、遮挡或屏幕视锥裁剪。\n" +
		"伪三维没有 IL/XL/Time 正交切片，因此 D 不移动切片。"
}

func handlePseudoShortcut(key uintptr) bool {
	if pseudoHwnd == 0 {
		return false
	}
	increase := !volumeShiftDown()
	switch key {
	case 'X', 'x':
		adjustPseudoAxis(0, increase)
	case 'Y', 'y':
		adjustPseudoAxis(1, increase)
	case 'Z', 'z':
		adjustPseudoAxis(2, increase)
	case 'R', 'r':
		adjustPseudoOverallScale(increase)
	case 'F', 'f':
		adjustPseudoFOV(increase)
	case VK_Q:
		changePseudoGain(1)
	case VK_W:
		changePseudoGain(-1)
	case VK_E:
		if increase {
			cyclePseudoPalette(1)
		} else {
			cyclePseudoPalette(-1)
		}
	case 'A', 'a':
		setText(pseudoControlsUI.status, fmt.Sprintf("Camera | Az %.1f° El %.1f° Zoom %.2fx FOV %.0f° Pan(%.0f,%.0f) Axis X×%.2f Y×%.2f Z×%.2f",
			pseudoState.camera.Azimuth, pseudoState.camera.Elevation, pseudoState.camera.Zoom, pseudoState.camera.FOV,
			pseudoState.camera.PanX, pseudoState.camera.PanY, pseudoState.camera.AxisXScale, pseudoState.camera.AxisYScale, pseudoState.camera.VerticalScale))
	case 'D', 'd':
		setText(pseudoControlsUI.status, "伪三维无 IL/XL/Time 正交切片，D 不执行切片拖动。")
	case VK_SPACE:
		resetPseudoCamera()
	case VK_ESCAPE:
		if pseudoState.timeDragging {
			cancelPseudoTimeDrag()
		} else if pseudoState.rangeDragging {
			cancelPseudoRangeDrag()
		} else {
			return false
		}
	default:
		return false
	}
	return true
}

func drawPseudoDashedSegment(hdc uintptr, area RECT, a, b pseudo3dcore.ScreenPoint, color uintptr, width int) {
	const pieces = 18
	for i := 0; i < pieces; i += 2 {
		t0, t1 := float64(i)/pieces, float64(i+1)/pieces
		x0 := int(area.Left) + int(math.Round(a.X+(b.X-a.X)*t0))
		y0 := int(area.Top) + int(math.Round(a.Y+(b.Y-a.Y)*t0))
		x1 := int(area.Left) + int(math.Round(a.X+(b.X-a.X)*t1))
		y1 := int(area.Top) + int(math.Round(a.Y+(b.Y-a.Y)*t1))
		drawHomeLine(hdc, x0, y0, x1, y1, color, width)
	}
}

func drawPseudoProjectedFrame(hdc uintptr, area RECT, frame pseudo3dcore.FrameProjection, dashed bool) {
	if !frame.Valid {
		return
	}
	draw := func(a, b pseudo3dcore.ScreenPoint) {
		if dashed {
			drawPseudoDashedSegment(hdc, area, a, b, rgbRef(37, 99, 235), 2)
		} else {
			drawHomeLine(hdc, int(area.Left)+int(math.Round(a.X)), int(area.Top)+int(math.Round(a.Y)),
				int(area.Left)+int(math.Round(b.X)), int(area.Top)+int(math.Round(b.Y)), rgbRef(37, 99, 235), 2)
		}
	}
	for i := 0; i < 4; i++ {
		next := (i + 1) % 4
		draw(frame.Base[i], frame.Base[next])
		draw(frame.Top[i], frame.Top[next])
		draw(frame.Base[i], frame.Top[i])
	}
}

func drawPseudoTimeFace(hdc uintptr, area RECT, points [4]pseudo3dcore.ScreenPoint) {
	for i := 0; i < 4; i++ {
		drawPseudoDashedSegment(hdc, area, points[i], points[(i+1)%4], rgbRef(37, 99, 235), 2)
	}
	cx, cy := 0.0, 0.0
	for _, point := range points {
		cx, cy = cx+point.X, cy+point.Y
	}
	cx, cy = cx/4+float64(area.Left), cy/4+float64(area.Top)
	brush, _, _ := pCreateSolidBrush.Call(rgbRef(37, 99, 235))
	old, _, _ := pSelectObject.Call(hdc, brush)
	pEllipse.Call(hdc, uintptr(int(math.Round(cx))-4), uintptr(int(math.Round(cy))-4), uintptr(int(math.Round(cx))+5), uintptr(int(math.Round(cy))+5))
	pSelectObject.Call(hdc, old)
	pDeleteObject.Call(brush)
}

func pseudoTimeDraftFrame(area RECT) pseudo3dcore.FrameProjection {
	frame := pseudo3dcore.ProjectFrame(pseudoState.renderedScene, pseudoState.renderedCamera, int(area.Right-area.Left), int(area.Bottom-area.Top))
	original, draft := pseudoState.timeOriginal.Normalized(), pseudoState.timeDraft.Normalized()
	if !frame.Valid || !original.Valid || !draft.Valid {
		return pseudo3dcore.FrameProjection{}
	}
	span := original.EndMS - original.StartMS
	startFraction, endFraction := (draft.StartMS-original.StartMS)/span, (draft.EndMS-original.StartMS)/span
	result := frame
	result.Bounds.TimeMinMS, result.Bounds.TimeMaxMS = draft.StartMS, draft.EndMS
	for i := 0; i < 4; i++ {
		dx, dy, dd := frame.Top[i].X-frame.Base[i].X, frame.Top[i].Y-frame.Base[i].Y, frame.Top[i].Depth-frame.Base[i].Depth
		result.Base[i] = pseudo3dcore.ScreenPoint{X: frame.Base[i].X + dx*startFraction, Y: frame.Base[i].Y + dy*startFraction, Depth: frame.Base[i].Depth + dd*startFraction}
		result.Top[i] = pseudo3dcore.ScreenPoint{X: frame.Base[i].X + dx*endFraction, Y: frame.Base[i].Y + dy*endFraction, Depth: frame.Base[i].Depth + dd*endFraction}
	}
	return result
}

func paintPseudoDynamicOverlays(hdc uintptr, area RECT) {
	saved, _, _ := pSaveDC.Call(hdc)
	pIntersectClipRect.Call(hdc, uintptr(area.Left), uintptr(area.Top), uintptr(area.Right), uintptr(area.Bottom))
	defer func() {
		if saved != 0 {
			pRestoreDC.Call(hdc, saved)
		}
	}()
	if pseudoState.hoverValid && crookedState.styleMode != 3 {
		projection := pseudo3dcore.ProjectCurtain(pseudoState.renderedScene, pseudoState.renderedCamera, int(area.Right-area.Left), int(area.Bottom-area.Top), pseudoState.hoverPick.CurtainIndex)
		if projection.Valid {
			for i := 0; i+1 < len(projection.Top); i++ {
				drawHomeLine(hdc, int(area.Left)+int(math.Round(projection.Top[i].X)), int(area.Top)+int(math.Round(projection.Top[i].Y)),
					int(area.Left)+int(math.Round(projection.Top[i+1].X)), int(area.Top)+int(math.Round(projection.Top[i+1].Y)), rgbRef(245, 122, 22), 2)
				drawHomeLine(hdc, int(area.Left)+int(math.Round(projection.Bottom[i].X)), int(area.Top)+int(math.Round(projection.Bottom[i].Y)),
					int(area.Left)+int(math.Round(projection.Bottom[i+1].X)), int(area.Top)+int(math.Round(projection.Bottom[i+1].Y)), rgbRef(245, 122, 22), 2)
			}
			last := len(projection.Top) - 1
			for _, i := range []int{0, last} {
				drawHomeLine(hdc, int(area.Left)+int(math.Round(projection.Top[i].X)), int(area.Top)+int(math.Round(projection.Top[i].Y)),
					int(area.Left)+int(math.Round(projection.Bottom[i].X)), int(area.Top)+int(math.Round(projection.Bottom[i].Y)), rgbRef(245, 122, 22), 2)
			}
		}
	}
	if top, bottom, point, ok := pseudoLinkedCursorProjection(area); ok {
		x0, y0 := int(area.Left)+int(math.Round(top.X)), int(area.Top)+int(math.Round(top.Y))
		x1, y1 := int(area.Left)+int(math.Round(bottom.X)), int(area.Top)+int(math.Round(bottom.Y))
		px, py := int(area.Left)+int(math.Round(point.X)), int(area.Top)+int(math.Round(point.Y))
		drawHomeLine(hdc, x0, y0, x1, y1, rgbRef(220, 38, 38), 2)
		brush, _, _ := pCreateSolidBrush.Call(rgbRef(220, 38, 38))
		old, _, _ := pSelectObject.Call(hdc, brush)
		pEllipse.Call(hdc, uintptr(px-5), uintptr(py-5), uintptr(px+6), uintptr(py+6))
		pSelectObject.Call(hdc, old)
		pDeleteObject.Call(brush)
	}
	if pseudoState.rangeDragging && pseudoState.rangeDraft.Valid {
		frame := pseudo3dcore.ProjectRange(pseudoState.renderedScene, pseudoState.renderedCamera, int(area.Right-area.Left), int(area.Bottom-area.Top), pseudoState.rangeDraft)
		drawPseudoProjectedFrame(hdc, area, frame, true)
	} else if pseudoState.rangeHoverHandle != 0 {
		frame := pseudo3dcore.ProjectFrame(pseudoState.renderedScene, pseudoState.renderedCamera, int(area.Right-area.Left), int(area.Bottom-area.Top))
		drawPseudoProjectedFrame(hdc, area, frame, true)
	}
	if pseudoState.timeDragging && pseudoState.timeDraft.Valid {
		frame := pseudoTimeDraftFrame(area)
		if frame.Valid {
			drawPseudoTimeFace(hdc, area, frame.Base)
			drawPseudoTimeFace(hdc, area, frame.Top)
		}
	} else if pseudoState.timeHoverHandle != 0 {
		frame := pseudo3dcore.ProjectFrame(pseudoState.renderedScene, pseudoState.renderedCamera, int(area.Right-area.Left), int(area.Bottom-area.Top))
		if pseudoState.timeHoverHandle == pseudoTimeStart {
			drawPseudoTimeFace(hdc, area, frame.Base)
		} else if pseudoState.timeHoverHandle == pseudoTimeEnd {
			drawPseudoTimeFace(hdc, area, frame.Top)
		}
	}
}

func paintPseudo() {
	var paint PAINTSTRUCT
	hdc, _, _ := pBeginPaint.Call(pseudoHwnd, uintptr(unsafe.Pointer(&paint)))
	if hdc != 0 {
		area := pseudoSceneRect()
		brush, _, _ := pCreateSolidBrush.Call(rgbRef(245, 248, 250))
		pFillRect.Call(hdc, uintptr(unsafe.Pointer(&area)), brush)
		pDeleteObject.Call(brush)
		width, height := int(area.Right-area.Left), int(area.Bottom-area.Top)
		if pseudoState.imageWidth >= 2 && pseudoState.imageHeight >= 2 && len(pseudoState.image) == pseudoState.imageWidth*pseudoState.imageHeight*4 {
			bitmap := BITMAPINFO{Header: BITMAPINFOHEADER{Size: uint32(unsafe.Sizeof(BITMAPINFOHEADER{})), Width: int32(width), Height: -int32(height), Planes: 1, BitCount: 32, Compression: BI_RGB, SizeImage: uint32(len(pseudoState.image))}}
			bitmap.Header.Width, bitmap.Header.Height = int32(pseudoState.imageWidth), -int32(pseudoState.imageHeight)
			pStretchDIBits.Call(hdc, uintptr(area.Left), uintptr(area.Top), uintptr(width), uintptr(height), 0, 0, uintptr(pseudoState.imageWidth), uintptr(pseudoState.imageHeight),
				uintptr(unsafe.Pointer(&pseudoState.image[0])), uintptr(unsafe.Pointer(&bitmap)), DIB_RGB_COLORS, SRCCOPY)
		} else {
			drawHomeText(hdc, hFont, rgbRef(100, 116, 139), "正在准备真实坐标下的地震剖面幕布…", area, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
		}
		if crookedState.styleMode != 3 || pseudoState.rangeDragging || pseudoState.rangeHoverHandle != 0 || pseudoState.timeDragging || pseudoState.timeHoverHandle != 0 {
			drawCrookedFrame(hdc, area, rgbRef(148, 163, 184))
		}
		bounds := pseudoState.renderStats.Bounds
		if bounds.Valid && (crookedState.styleMode == 1 || crookedState.styleMode == 2) {
			labelRect := RECT{Left: area.Left + 8, Top: area.Top + 6, Right: area.Right - 8, Bottom: area.Top + 26}
			drawHomeText(hdc, hFont, rgbRef(51, 65, 85), fmt.Sprintf("X %.3f – %.3f    Y %.3f – %.3f    Time %.1f – %.1f ms", bounds.XMin, bounds.XMax, bounds.YMin, bounds.YMax, bounds.TimeMinMS, bounds.TimeMaxMS), labelRect, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		}
		paintPseudoDynamicOverlays(hdc, area)
	}
	pEndPaint.Call(pseudoHwnd, uintptr(unsafe.Pointer(&paint)))
}

func pseudoDragThresholdReached(startX, startY, x, y int) bool {
	dx, dy := x-startX, y-startY
	return dx*dx+dy*dy >= pseudoDragThreshold*pseudoDragThreshold
}

func cancelPseudoLeftDrag() {
	pseudoState.leftDragPending = false
	pseudoState.leftDragPan = false
	pseudoState.rotating = false
	pseudoState.panning = false
	pReleaseCapture.Call()
	updatePseudoExportButton()
}

func pseudoWndProc(h uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case WM_COMMAND:
		id := int(wParam & 0xffff)
		notifyCode := int((wParam >> 16) & 0xffff)
		switch id {
		case IDPSEUDO_CLOSE:
			pDestroyWindow.Call(h)
		case IDPSEUDO_ALL:
			setAllPseudoSelections(true)
		case IDPSEUDO_NONE:
			setAllPseudoSelections(false)
		case IDPSEUDO_CURRENT:
			showOnlyCurrentPseudoLine()
		case IDPSEUDO_RESET:
			resetPseudoCamera()
		case IDPSEUDO_DEFAULT:
			savePseudoDefaults()
			setText(pseudoControlsUI.status, "当前 Camera、FOV 和 X/Y/Z 比例已设为伪三维默认视图。")
		case IDPSEUDO_HELP:
			message(pseudoHwnd, "伪三维帮助", pseudoShortcutHelpText(), MB_OK|MB_ICONINFORMATION)
		case IDPSEUDO_EXPORT_RANGE:
			startPseudoRangeExport()
		case IDPSEUDO_GAINMINUS:
			changePseudoGain(-1)
		case IDPSEUDO_GAINPLUS:
			changePseudoGain(1)
		case IDPSEUDO_PALETTE:
			if notifyCode == 1 {
				selection, _, _ := pSendMessageW.Call(pseudoControlsUI.palette, CB_GETCURSEL, 0, 0)
				changePseudoPalette(int(selection))
			}
		case IDPSEUDO_STYLE:
			if notifyCode == 1 {
				selection, _, _ := pSendMessageW.Call(pseudoControlsUI.style, CB_GETCURSEL, 0, 0)
				applyCrookedSharedStyle(crookedStyleFromCombo(int(selection)))
			}
		case IDPSEUDO_FOV:
			if notifyCode == 1 {
				selection, _, _ := pSendMessageW.Call(pseudoControlsUI.fov, CB_GETCURSEL, 0, 0)
				if int(selection) >= 0 && int(selection) < len(pseudoFOVOptions) {
					pseudoState.camera.FOV = pseudoFOVOptions[int(selection)]
					queuePseudoSceneRender(true)
				}
			}
		case IDPSEUDO_AXISX, IDPSEUDO_AXISY, IDPSEUDO_AXISZ:
			if notifyCode == 1 {
				control := pseudoControlsUI.axisX
				axis := 0
				if id == IDPSEUDO_AXISY {
					control, axis = pseudoControlsUI.axisY, 1
				} else if id == IDPSEUDO_AXISZ {
					control, axis = pseudoControlsUI.axisZ, 2
				}
				selection, _, _ := pSendMessageW.Call(control, CB_GETCURSEL, 0, 0)
				setPseudoAxisFromCombo(axis, int(selection))
			}
		case IDPSEUDO_ZMINUS:
			adjustPseudoVertical(false)
		case IDPSEUDO_ZPLUS:
			adjustPseudoVertical(true)
		}
		return 0
	case WM_NOTIFY:
		if lParam != 0 {
			notify := (*pseudoNMListView)(unsafe.Pointer(lParam))
			if notify.Hdr.IdFrom == IDPSEUDO_LINES && notify.Hdr.Code == LVN_ITEMCHANGED && !pseudoListUpdating {
				index := int(notify.IItem)
				if index >= 0 && index < len(pseudoState.lines) {
					oldImage, newImage := (notify.UOldState&LVIS_STATEIMAGEMASK)>>12, (notify.UNewState&LVIS_STATEIMAGEMASK)>>12
					if oldImage != newImage && newImage != 0 {
						applyPseudoSelection(index, newImage == 2, true)
					}
					if notify.UNewState&LVIS_SELECTED != 0 && notify.UOldState&LVIS_SELECTED == 0 && pseudoState.lines[index].inRange && pseudoState.lines[index].timeInRange {
						pseudoState.activeLine = index
						queuePseudoSceneRender(true)
					}
				}
			}
		}
		return 0
	case WM_PSEUDO_LOAD_READY:
		handlePseudoLoadResults()
		return 0
	case WM_PSEUDO_SCENE_READY:
		handlePseudoRenderResult()
		return 0
	case WM_PSEUDO_PROGRESS_RENDER:
		if int64(wParam) == pseudoState.progressRenderGen && pseudoState.progressRenderArmed {
			pseudoState.progressRenderArmed = false
			selected, loaded, _, failed := pseudoLoadingCounts()
			if selected > 0 && loaded+failed < selected {
				queuePseudoProgressRender()
				updatePseudoStatus()
			}
		}
		return 0
	case WM_PSEUDO_LOAD_STAGE:
		if int64(lParam) == pseudoState.windowGeneration && int(wParam) > pseudoState.loadStage {
			pseudoState.loadStage = int(wParam)
			updatePseudoStatus()
		}
		return 0
	case WM_LBUTTONDBLCLK:
		x, y := mousePoint(lParam)
		if wParam&pseudoMKControl == 0 && rectContains(pseudoSceneRect(), x, y) {
			if pseudoState.timeDragging {
				cancelPseudoTimeDrag()
			}
			if pseudoState.rangeDragging {
				cancelPseudoRangeDrag()
			}
			cancelPseudoLeftDrag()
			navigatePseudoPickToCrooked(x, y)
			return 0
		}
	case WM_LBUTTONDOWN:
		x, y := mousePoint(lParam)
		if rectContains(pseudoSceneRect(), x, y) {
			if wParam&pseudoMKControl != 0 {
				if !beginPseudoTimeDrag(x, y) {
					beginPseudoRangeDrag(x, y)
				}
				return 0
			}
			pseudoState.leftDragPending = true
			pseudoState.leftDragPan = wParam&pseudoMKShift != 0
			pseudoState.rotating, pseudoState.panning = false, false
			pseudoState.dragStartX, pseudoState.dragStartY, pseudoState.dragCamera = x, y, pseudoState.camera
			updatePseudoExportButton()
			pSetCapture.Call(h)
			return 0
		}
	case WM_RBUTTONDOWN:
		x, y := mousePoint(lParam)
		if rectContains(pseudoSceneRect(), x, y) {
			pseudoState.zooming = true
			pseudoState.dragStartX, pseudoState.dragStartY, pseudoState.dragCamera = x, y, pseudoState.camera
			updatePseudoExportButton()
			pSetCapture.Call(h)
			return 0
		}
	case WM_MOUSEMOVE:
		x, y := mousePoint(lParam)
		if pseudoState.timeDragging {
			updatePseudoTimeDrag(x, y)
			return 0
		}
		if pseudoState.rangeDragging {
			updatePseudoRangeDrag(x, y)
			return 0
		}
		if pseudoState.leftDragPending {
			if !pseudoDragThresholdReached(pseudoState.dragStartX, pseudoState.dragStartY, x, y) {
				return 0
			}
			pseudoState.leftDragPending = false
			if pseudoState.leftDragPan {
				pseudoState.panning = true
			} else {
				pseudoState.rotating = true
			}
			clearPseudoHoverOverlay()
		}
		if pseudoState.rotating {
			pseudoState.camera.Azimuth = pseudoState.dragCamera.Azimuth + float64(x-pseudoState.dragStartX)*.38
			pseudoState.camera.Elevation = math.Max(-80, math.Min(80, pseudoState.dragCamera.Elevation-float64(y-pseudoState.dragStartY)*.28))
			queuePseudoSceneRender(false)
			return 0
		}
		if pseudoState.panning {
			pseudoState.camera.PanX = pseudoState.dragCamera.PanX + float64(x-pseudoState.dragStartX)
			pseudoState.camera.PanY = pseudoState.dragCamera.PanY + float64(y-pseudoState.dragStartY)
			queuePseudoSceneRender(false)
			return 0
		}
		if pseudoState.zooming {
			dy := float64(y - pseudoState.dragStartY)
			pseudoState.camera.Zoom = pseudoState.dragCamera.Zoom * math.Exp(-dy*.008)
			pseudoState.camera.Zoom = math.Max(.55, math.Min(3.5, pseudoState.camera.Zoom))
			queuePseudoSceneRender(false)
			return 0
		}
		oldHandle, oldTimeHandle := pseudoState.rangeHoverHandle, pseudoState.timeHoverHandle
		if wParam&pseudoMKControl != 0 && rectContains(pseudoSceneRect(), x, y) {
			if timeHandle, timeOK := pseudoTimeFrameHandleAt(x, y); timeOK {
				pseudoState.timeHoverHandle, pseudoState.rangeHoverHandle = timeHandle, 0
				cursor, _, _ := pLoadCursorW.Call(0, uintptr(IDC_SIZENS))
				if cursor != 0 {
					pSetCursor.Call(cursor)
				}
			} else {
				pseudoState.timeHoverHandle = 0
				handle, _, ok := pseudoFrameHandleAt(x, y)
				if ok {
					pseudoState.rangeHoverHandle = handle
					setPseudoRangeCursor(handle)
				} else {
					pseudoState.rangeHoverHandle = 0
				}
			}
		} else {
			pseudoState.rangeHoverHandle, pseudoState.timeHoverHandle = 0, 0
		}
		if oldHandle != pseudoState.rangeHoverHandle || oldTimeHandle != pseudoState.timeHoverHandle {
			area := pseudoSceneRect()
			pInvalidateRect.Call(pseudoHwnd, uintptr(unsafe.Pointer(&area)), 0)
		}
		if wParam&pseudoMKControl == 0 && rectContains(pseudoSceneRect(), x, y) {
			updatePseudoHover(x, y)
		} else if pseudoState.hoverValid && !rectContains(pseudoSceneRect(), x, y) {
			old := pseudoState.hoverPick.CurtainIndex
			pseudoState.hoverValid = false
			invalidatePseudoCurtainOverlay(old)
			updatePseudoStatus()
		}
		return 0
	case WM_LBUTTONUP:
		if pseudoState.timeDragging {
			finishPseudoTimeDrag()
			return 0
		}
		if pseudoState.rangeDragging {
			finishPseudoRangeDrag()
			return 0
		}
		if pseudoState.leftDragPending {
			cancelPseudoLeftDrag()
			return 0
		}
		if pseudoState.rotating {
			pseudoState.rotating = false
			pseudoState.leftDragPan = false
			pReleaseCapture.Call()
			queuePseudoSceneRender(true)
			updatePseudoExportButton()
			return 0
		}
		if pseudoState.panning {
			pseudoState.panning = false
			pseudoState.leftDragPan = false
			pReleaseCapture.Call()
			queuePseudoSceneRender(true)
			updatePseudoExportButton()
			return 0
		}
	case WM_RBUTTONUP:
		if pseudoState.zooming {
			pseudoState.zooming = false
			pReleaseCapture.Call()
			queuePseudoSceneRender(true)
			updatePseudoExportButton()
			return 0
		}
	case WM_MOUSEWHEEL:
		delta := int(int16(uint16((wParam >> 16) & 0xffff)))
		if delta > 0 {
			pseudoState.camera.Zoom *= 1.12
		} else if delta < 0 {
			pseudoState.camera.Zoom /= 1.12
		}
		pseudoState.camera.Zoom = math.Max(.55, math.Min(3.5, pseudoState.camera.Zoom))
		queuePseudoSceneRender(true)
		return 0
	case WM_KEYDOWN:
		if handlePseudoShortcut(wParam) {
			return 0
		}
	case WM_SIZE:
		layoutPseudoControls()
		pseudoState.image = nil
		pseudoState.hoverValid, pseudoState.rangeHoverHandle, pseudoState.timeHoverHandle = false, 0, 0
		queuePseudoSceneRender(true)
		pInvalidateRect.Call(h, 0, 0)
		return 0
	case WM_GETMINMAXINFO:
		if lParam != 0 {
			limits := (*MINMAXINFO)(unsafe.Pointer(lParam))
			limits.PtMinTrackSize.X = 1000
			limits.PtMinTrackSize.Y = 650
		}
		return 0
	case WM_ERASEBKGND:
		return 1
	case WM_PAINT:
		paintPseudo()
		return 0
	case WM_CLOSE:
		pDestroyWindow.Call(h)
		return 0
	case WM_DESTROY:
		projectName, generation := pseudoProjectName(), pseudoState.workspaceGeneration
		cancelPseudoExportForProjectClose()
		atomic.AddInt64(&pseudoWindowGen, 1)
		atomic.AddInt64(&pseudoRenderGen, 1)
		if pseudoState.cancel != nil {
			close(pseudoState.cancel)
		}
		pseudoPendingMu.Lock()
		pseudoLoadPending, pseudoRenderPending = nil, nil
		pseudoPendingMu.Unlock()
		pseudoState = pseudoSession{}
		pseudoControlsUI = pseudoControls{}
		pseudoHwnd = 0
		writeWorkspaceTrace(workspacecore.TraceEvent{Action: "pseudo3d_closed", Workspace: workspacecore.KindCrooked, Dataset: projectName, Geometry: geometrycore.KindCrookedLine, Generation: generation})
		return 0
	}
	result, _, _ := pDefWindowProcW.Call(h, uintptr(msg), wParam, lParam)
	return result
}
