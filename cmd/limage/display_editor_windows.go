//go:build windows

package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
)

const displayStatsMessage = 0x8071
const displayRefreshComparePickerMessage = 0x8072
const (
	deColor = 25701 + iota
	deAxis
	deAxisSelect
	deSummary
	deBody
	deStatus
	deTitle
	deUnit
	deMin
	deMax
	deStep
	deFormat
	deDigits
	deAuto
	deSym
	deReverse
	deHide
	deHideLabels
	deHideTitle
	deCompareMode
	deComparePanel
	dePreset
	deFit
	deAdd
	deDelete
	deChoose
	deRangePage
	deGradientPage
	deTickPage
	deAppearancePage
	deTickAuto
	deTickCurrent
	deTickLength
	deTickPreview
	deRangeSummary
	deCompareMatchA
	deTopTabs
	deSubTabs
	deSummaryTitle
	deNodePosition
	deNodeColor
	deNodeReadout
	deNodePending
	deNodeSwatch
	dePositionHeading
	deTickDivisions
	deTickFont
	deTitleFont
	dePlotTitleFont
	deDistancePage
	deDistanceMode
	deDistanceCalibration
	deDistanceSpacing
	deDistanceActual
	deReset
	deApply
	deOK
	deCancel
	deApplyPanels
	deRangePolicy
	dePercentPreset
	dePercentLow
	dePercentHigh
	dePositionLow
	dePositionHigh
	deApplyPosition
	deCoverage
	deCopyExact
	deTickModeScientific
)

type displayDraft struct {
	text                map[int]string
	selects             map[int]int
	checks              map[int]bool
	initial             [2]string
	stops               []displaycore.GradientStop
	gradient            displayGradientSession
	bounds              displayDraftBounds
	key, identity, axis string
	color               bool
	distance            displayDistanceSettings
	spacing             string
}
type displayEditorFollowKey struct {
	key, identity string
	color         bool
}
type displayEditor struct {
	handle, owner, body, status           uintptr
	controls, labels                      map[int]uintptr
	key, axis, identity                   string
	pageColor, invalid, loading, dragging bool
	drafts                                map[string]displayDraft
	stops                                 []displaycore.GradientStop
	initial                               [2]string
	exactBounds                           displayDraftBounds
	draftBaselines                        map[string]displayDraft
	lastValidTypography                   map[string]map[int]float64
	collect                               func(displayBatchEdit)
	visiting                              bool
	stateError                            string
	statsWaiting                          bool
	selection, scroll, scrollMax          int
	unit                                  displaycore.Axis
	graph, gradient                       RECT
	preview, nodeBand                     RECT
	topTabs, subTabs                      []int
	tabSections                           [2]int
	gestureDraft                          *displayDraft
	gestureNode                           int
	gestureMoved                          bool
	nodeEditError                         string
	nodePositionError, nodeColorError     string
	nodeUIKey                             string
	nodeUISelection                       int
	gradientKey                           string
	gradientEdit                          displayGradientSession
	gradientSessions                      map[string]displayGradientSession
	gradientSerial, gradientHovered       uint64
	gradientTracking                      bool
	gradientDragX                         int
	compareKeys                           [3]string
	compareTargets                        []int
	compareIndependent, compareModeDirty  bool
	compareFollowDraft                    map[displayEditorFollowKey]bool
	bColorLocalDraft                      *displayDraft
	comparePickerPending                  bool
	section, fontDPI                      int
	font                                  uintptr
	rangeBar                              RECT
	rangeDragging                         bool
	rangeDragColor, rangeDragSymmetric    bool
	rangeDragMode                         displaycore.RangeDragMode
	rangeDragStart                        int
	rangeDragBar                          RECT
	rangeDragBounds, rangeDragView        displaycore.Range
	rangeLayoutPolicy                     displaycore.RangePolicy
	summaryTip                            poststackToolbarResources
	viewportDifferent                     bool
	viewportRefreshPending                bool
	distanceDraft, distanceBase           displayDistanceSettings
	distanceDraftOwner                    string
	distanceDraftMode                     int
	distanceSpacingText                   string
}

var displayEditors = make(map[uintptr]*displayEditor)
var displayBodies = make(map[uintptr]*displayEditor)
var displayRegistered bool
var displayDPIProc = user32.NewProc("GetDpiForWindow")
var displayGetWorkArea = user32.NewProc("SystemParametersInfoW")
var displayMonitorFromWindow = user32.NewProc("MonitorFromWindow")
var displayGetMonitorInfo = user32.NewProc("GetMonitorInfoW")
var displaySetCapture = user32.NewProc("SetCapture")
var displayReleaseCapture = user32.NewProc("ReleaseCapture")
var displayChooseColor = syscall.NewLazyDLL("comdlg32.dll").NewProc("ChooseColorW")

type displayChooseColorData struct {
	Size                 uint32
	Owner, Instance      uintptr
	Result               uint32
	Custom               uintptr
	Flags                uint32
	Data, Hook, Template uintptr
}

var displayCustomColors [16]uint32

func displayEnsureWindows() {
	if !displayRegistered {
		icc := INITCOMMONCONTROLSEX{DwSize: uint32(unsafe.Sizeof(INITCOMMONCONTROLSEX{})), DwICC: 0x0008} // ICC_TAB_CLASSES
		pInitCommonCtrls.Call(uintptr(unsafe.Pointer(&icc)))
		displayRegistered = true
		h, _, _ := pGetModuleHandleW.Call(0)
		c, _, _ := pLoadCursorW.Call(0, IDC_ARROW)
		registerClass("SeisForgeDisplaySettings", syscall.NewCallback(displayEditorProc), COLOR_BTNFACE+1, h, c)
		registerClass("SeisForgeDisplaySettingsBody", syscall.NewCallback(displayEditorBodyProc), COLOR_BTNFACE+1, h, c)
	}
	if displayAsyncWindow == 0 {
		displayAsyncWindow, _, _ = pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(u16("SeisForgeDisplaySettings"))), 0, 0, 0, 0, 0, 0, ^uintptr(2), 0, 0, 0)
	}
}
func displayDPI(h uintptr) int {
	if displayDPIProc.Find() == nil {
		if v, _, _ := displayDPIProc.Call(h); v >= 72 && v <= 768 {
			return int(v)
		}
	}
	return 96
}

const displayEditorWindowStyle = WS_OVERLAPPEDWINDOW | WS_CLIPCHILDREN

func displayEditorWorkArea(h uintptr, suggested *RECT) RECT {
	var work RECT
	mi := struct {
		Size          uint32
		Monitor, Work RECT
		Flags         uint32
	}{}
	mi.Size = uint32(unsafe.Sizeof(mi))
	monitor := uintptr(0)
	if suggested != nil {
		monitor, _, _ = algorithmMonitorFromRect.Call(uintptr(unsafe.Pointer(suggested)), 2)
	} else {
		monitor, _, _ = displayMonitorFromWindow.Call(h, 2)
	}
	if ok, _, _ := displayGetMonitorInfo.Call(monitor, uintptr(unsafe.Pointer(&mi))); ok != 0 {
		return mi.Work
	}
	displayGetWorkArea.Call(0x30, 0, uintptr(unsafe.Pointer(&work)), 0)
	return work
}

func displayEditorOuterSize(dpi, clientWidth, clientHeight int) (int, int) {
	r := RECT{Right: int32(clientWidth), Bottom: int32(clientHeight)}
	adjusted := uintptr(0)
	if algorithmAdjustWindow.Find() == nil {
		adjusted, _, _ = algorithmAdjustWindow.Call(uintptr(unsafe.Pointer(&r)), displayEditorWindowStyle, 0, settingsToolWindowStyle, uintptr(dpi))
	}
	if adjusted == 0 {
		r = RECT{Right: int32(clientWidth), Bottom: int32(clientHeight)}
		pAdjustWindowRectEx.Call(uintptr(unsafe.Pointer(&r)), displayEditorWindowStyle, 0, settingsToolWindowStyle)
	}
	return int(r.Right - r.Left), int(r.Bottom - r.Top)
}

func displayEditorFitWorkArea(r, work RECT) RECT {
	if work.Right <= work.Left || work.Bottom <= work.Top {
		return r
	}
	width := max(int32(1), min(r.Right-r.Left, work.Right-work.Left))
	height := max(int32(1), min(r.Bottom-r.Top, work.Bottom-work.Top))
	left := max(work.Left, min(r.Left, work.Right-width))
	top := max(work.Top, min(r.Top, work.Bottom-height))
	return RECT{Left: left, Top: top, Right: left + width, Bottom: top + height}
}

func displayOpenEditor(owner uintptr, key, axis string, color bool) {
	o := displayOwners[owner]
	if o == nil {
		return
	}
	e := o.surfaces[key]
	if e == nil {
		return
	}
	displayEnsureWindows()
	w := o.editor
	if w == nil {
		dpi := displayDPI(owner)
		work := displayEditorWorkArea(owner, nil)
		ww, hh := displayEditorOuterSize(dpi, displayPixelDIP(840, dpi), displayPixelDIP(710, dpi))
		x, y := int(work.Left)+max(0, (int(work.Right-work.Left)-ww)/2), int(work.Top)+max(0, (int(work.Bottom-work.Top)-hh)/2)
		box := displayEditorFitWorkArea(RECT{Left: int32(x), Top: int32(y), Right: int32(x + ww), Bottom: int32(y + hh)}, work)
		h, _, _ := pCreateWindowExW.Call(settingsToolWindowStyle, uintptr(unsafe.Pointer(u16("SeisForgeDisplaySettings"))), uintptr(unsafe.Pointer(u16("显示设置"))), displayEditorWindowStyle, uintptr(box.Left), uintptr(box.Top), uintptr(box.Right-box.Left), uintptr(box.Bottom-box.Top), owner, 0, 0, 0)
		if h == 0 {
			return
		}
		w = &displayEditor{handle: h, owner: owner, controls: make(map[int]uintptr), labels: make(map[int]uintptr), drafts: make(map[string]displayDraft)}
		displayEditors[h] = w
		o.editor = w
		displayMakeEditor(w)
	} else {
		displayStoreDraft(w)
	}
	for k, d := range w.drafts {
		if d.key == key && d.identity != e.s.Identity {
			delete(w.drafts, k)
			delete(w.draftBaselines, k)
		}
	}
	w.key, w.identity, w.axis, w.pageColor, w.invalid = key, e.s.Identity, axis, color && e.s.Color != nil, false
	if len(e.s.Axes) == 0 && e.s.Color != nil {
		w.pageColor = true
	}
	displayLoadDraft(w)
	showOwnedSettingsWindow(w.handle)
}

func displayDestroyEditor(w *displayEditor) {
	if w == nil || displayEditors[w.handle] != w {
		return
	}
	displayEditorFinishGesture(w, false, true)
	closeOwnedSettingsWindow(w.handle, w.owner)
}
func displayMakeEditor(w *displayEditor) {
	// Native tabs own their complete strip boundary. Clip the sibling copy
	// button above the right end without shortening the tab control itself.
	w.controls[deTopTabs] = createCtrl(w.handle, "SysTabControl32", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|poststackToolbarClipSiblings, 0, 0, 1, 1, deTopTabs)
	body, _, _ := pCreateWindowExW.Call(0x10000, uintptr(unsafe.Pointer(u16("SeisForgeDisplaySettingsBody"))), 0, WS_CHILD|WS_VISIBLE|WS_CLIPCHILDREN|poststackToolbarClipSiblings, 0, 0, 1, 1, w.handle, deBody, 0, 0)
	w.body = body
	displayBodies[body] = w
	w.controls[deSubTabs] = createCtrl(body, "SysTabControl32", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|poststackToolbarClipSiblings, 0, 0, 1, 1, deSubTabs)
	for _, row := range []struct {
		id    int
		title string
	}{{deReset, "恢复默认"}, {deApply, "应用"}, {deOK, "确定"}, {deCancel, "取消"}, {deApplyPanels, "复制设置到…"}} {
		w.controls[row.id] = createPoststackNamedToolButton(w.handle, row.id, 0, row.title, WS_VISIBLE|BS_PUSHBUTTON, 0, 0, 1, 1)
	}
	w.status = createCtrl(w.handle, "STATIC", "", WS_CHILD|WS_VISIBLE|0x4000, 0, 0, 1, 1, deStatus)
	for _, row := range []struct {
		id               int
		cls, text, label string
		style            uint32
	}{
		{deSummary, "STATIC", "", "", 0}, {deAxisSelect, "COMBOBOX", "", "坐标轴", CBS_DROPDOWNLIST | CBS_HASSTRINGS | WS_VSCROLL},
		{deSummaryTitle, "STATIC", "", "", poststackPathLabelStyle},
		{deTitle, "EDIT", "", "标题", WS_BORDER | ES_AUTOHSCROLL}, {deUnit, "EDIT", "", "单位", WS_BORDER | ES_AUTOHSCROLL},
		{deMin, "EDIT", "", "最小值", WS_BORDER | ES_AUTOHSCROLL}, {deMax, "EDIT", "", "最大值", WS_BORDER | ES_AUTOHSCROLL},
		{deStep, "EDIT", "", "手动主刻度间隔", WS_BORDER | ES_AUTOHSCROLL},
		{deFormat, "COMBOBOX", "", "数字格式", CBS_DROPDOWNLIST | CBS_HASSTRINGS},
		{deDigits, "EDIT", "", "小数位（0–12）", WS_BORDER | ES_AUTOHSCROLL},
		{deAuto, "BUTTON", "自动范围", "", BS_AUTOCHECKBOX}, {deSym, "BUTTON", "以 0 对称", "", BS_AUTOCHECKBOX},
		{deReverse, "BUTTON", "反转", "", BS_AUTOCHECKBOX}, {deHide, "BUTTON", "隐藏", "", BS_AUTOCHECKBOX},
		{deHideLabels, "BUTTON", "隐藏刻度数字", "", BS_AUTOCHECKBOX}, {deHideTitle, "BUTTON", "隐藏标题", "", BS_AUTOCHECKBOX},
		{deCompareMode, "COMBOBOX", "", "色标模式", CBS_DROPDOWNLIST | CBS_HASSTRINGS},
		{deComparePanel, "COMBOBOX", "", "编辑对象", CBS_DROPDOWNLIST | CBS_HASSTRINGS},
		{deCompareMatchA, "BUTTON", "与 A 一致", "", BS_AUTOCHECKBOX},
		{dePreset, "COMBOBOX", "", "色表预设", CBS_DROPDOWNLIST | CBS_HASSTRINGS | WS_VSCROLL},
		{deFit, "BUTTON", "适配数据范围", "", BS_PUSHBUTTON}, {deAdd, "BUTTON", "添加控制点", "", BS_PUSHBUTTON},
		{deDelete, "BUTTON", "删除选中点", "", BS_PUSHBUTTON}, {deChoose, "BUTTON", "修改选中色", "", BS_PUSHBUTTON},
		{deNodePosition, "EDIT", "", "选中点位置（%）", WS_BORDER | ES_AUTOHSCROLL},
		{deNodeColor, "EDIT", "", "颜色 (#RRGGBB)", WS_BORDER | ES_AUTOHSCROLL},
		{deNodeReadout, "STATIC", "", "", 0},
		{deNodePending, "STATIC", "", "", 0},
		{deNodeSwatch, "BUTTON", "颜色块：修改选中色", "", algorithmOwnerDraw},
		{dePositionHeading, "STATIC", "范围位置（占完整数值跨度 %）", "", 0},
		{deTickAuto, "BUTTON", "自动刻度间隔", "", BS_AUTOCHECKBOX},
		{deTickModeScientific, "COMBOBOX", "", "主刻度模式", CBS_DROPDOWNLIST | CBS_HASSTRINGS},
		{deTickCurrent, "STATIC", "", "", 0},
		{deTickLength, "EDIT", "", "主刻度长度 (DIP)", WS_BORDER | ES_AUTOHSCROLL},
		{deTickPreview, "STATIC", "", "", 0},
		{deTickDivisions, "COMBOBOX", "", "等分段数 (1–20)", 0x0002 | CBS_HASSTRINGS | WS_VSCROLL}, // CBS_DROPDOWN
		{deTickFont, "EDIT", "", "刻度文字 (pt)", WS_BORDER | ES_AUTOHSCROLL},
		{deTitleFont, "EDIT", "", "轴／色标标题 (pt)", WS_BORDER | ES_AUTOHSCROLL},
		{dePlotTitleFont, "EDIT", "", "剖面标题 (pt)", WS_BORDER | ES_AUTOHSCROLL},
		{deDistanceMode, "COMBOBOX", "", "横坐标模式", CBS_DROPDOWNLIST | CBS_HASSTRINGS},
		{deDistanceCalibration, "COMBOBOX", "", "道间距", CBS_DROPDOWNLIST | CBS_HASSTRINGS},
		{deDistanceSpacing, "EDIT", "", "手动间距 (m)", WS_BORDER | ES_AUTOHSCROLL},
		{deDistanceActual, "STATIC", "", "", 0},
		{deRangeSummary, "STATIC", "", "", 0},
		{deRangePolicy, "COMBOBOX", "", "范围方式", CBS_DROPDOWNLIST | CBS_HASSTRINGS},
		{dePercentPreset, "COMBOBOX", "", "中央保留", CBS_DROPDOWNLIST | CBS_HASSTRINGS},
		{dePercentLow, "EDIT", "", "低分位 (%)", WS_BORDER | ES_AUTOHSCROLL},
		{dePercentHigh, "EDIT", "", "高分位 (%)", WS_BORDER | ES_AUTOHSCROLL},
		{dePositionLow, "EDIT", "", "起点 (%)", WS_BORDER | ES_AUTOHSCROLL},
		{dePositionHigh, "EDIT", "", "终点 (%)", WS_BORDER | ES_AUTOHSCROLL},
		{deApplyPosition, "BUTTON", "设置跨度位置", "", BS_PUSHBUTTON},
		{deCoverage, "STATIC", "", "", 0},
		{deCopyExact, "BUTTON", "复制精确值", "", BS_PUSHBUTTON},
	} {
		style := uint32(WS_CHILD|poststackToolbarClipSiblings) | row.style
		if row.cls != "STATIC" {
			style |= WS_TABSTOP
		}
		if row.cls == "BUTTON" && style&0xf == BS_PUSHBUTTON {
			w.controls[row.id] = createPoststackNamedToolButton(body, row.id, 0, row.text, style, 0, 0, 1, 1)
		} else {
			w.controls[row.id] = createCtrl(body, row.cls, row.text, style, 0, 0, 1, 1, row.id)
		}
		if row.label != "" {
			w.labels[row.id] = createCtrl(body, "STATIC", row.label, WS_CHILD|poststackToolbarClipSiblings|0x4000, 0, 0, 1, 1, 0)
		}
	}
	displayEditorCombo(w.controls[deFormat], []string{"自动", "整数", "固定小数", "科学计数"}, 0)
	displayEditorCombo(w.controls[deTickDivisions], []string{"4", "5", "6", "8", "10"}, 1)
	displayEditorCombo(w.controls[deTickModeScientific], []string{"自动整洁刻度", "手动间隔", "手动等分"}, 0)
	displayEditorCombo(w.controls[deDistanceMode], []string{"原始编号", "距离"}, 0)
	displayEditorCombo(w.controls[deDistanceCalibration], []string{"自动", "手动"}, 0)
	displayEditorCombo(w.controls[deRangePolicy], []string{"数据全范围（自动）", "百分位范围（自动）", "手动范围"}, 2)
	displayEditorCombo(w.controls[dePercentPreset], []string{"90%", "95%", "98%", "99%", "100%", "自定义低/高分位"}, 2)
}
func displayEditorCombo(h uintptr, labels []string, index int) {
	pSendMessageW.Call(h, 0x014b, 0, 0)
	for _, s := range labels {
		pSendMessageW.Call(h, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(s))))
	}
	pSendMessageW.Call(h, CB_SETCURSEL, uintptr(index), 0)
}
func displaySelection(h uintptr) int {
	v, _, _ := pSendMessageW.Call(h, CB_GETCURSEL, 0, 0)
	return int(v)
}
func displayChecked(h uintptr) bool { v, _, _ := pSendMessageW.Call(h, 0xf0, 0, 0); return v == 1 }
func displayCheck(h uintptr, b bool) {
	v := uintptr(0)
	if b {
		v = 1
	}
	pSendMessageW.Call(h, 0xf1, v, 0)
}
func displayEnable(h uintptr, b bool) {
	v := uintptr(0)
	if b {
		v = 1
	}
	pEnableWindow.Call(h, v)
}
func displayDraftKey(w *displayEditor) string {
	a := w.axis
	if w.pageColor {
		a = "color"
	}
	return w.key + "\x00" + w.identity + "\x00" + a
}

var displayTextIDs = []int{deTitle, deUnit, deMin, deMax, deStep, deDigits, deTickLength, deTickDivisions, deTickFont, deTitleFont, dePlotTitleFont, dePercentLow, dePercentHigh, dePositionLow, dePositionHigh}
var displayCheckIDs = []int{deAuto, deSym, deReverse, deHide, deHideLabels, deHideTitle, deTickAuto}

func displayEditorFormat(index int) displaycore.NumberFormat {
	switch index {
	case 1:
		return displaycore.IntegerFormat
	case 2:
		return displaycore.FixedFormat
	case 3:
		return displaycore.ScientificFormat
	default:
		return displaycore.AutoFormat
	}
}
func displayEditorFormatIndex(f displaycore.NumberFormat) int {
	switch f {
	case displaycore.IntegerFormat:
		return 1
	case displaycore.FixedFormat:
		return 2
	case displaycore.ScientificFormat:
		return 3
	default:
		return 0
	}
}

func displayEditorCompareLinked(w *displayEditor) bool {
	e := displayEditorSurface(w)
	if e == nil || e.s.Compare == nil || !e.s.Compare.CanLink {
		return false
	}
	if b := e.s.Compare; b.PoststackProfiles {
		if w.pageColor && w.key == b.Keys[1] {
			return displayChecked(w.controls[deCompareMatchA])
		}
		if o := displayOwners[w.owner]; o != nil {
			if peer := o.surfaces[b.Keys[1]]; peer != nil {
				if on, ok := w.compareFollowDraft[displayEditorFollowKey{key: b.Keys[1], identity: peer.s.Identity, color: true}]; ok {
					return on
				}
			}
		}
		linked, _ := displayCompareOptions(w.owner, w.key)
		return linked
	}
	return !w.compareIndependent
}

func displayEditorPoststackFollow(w *displayEditor, e *displaySurfaceState) (available, linked bool) {
	b := e.s.Compare
	if b == nil || !b.PoststackProfiles {
		return false, false
	}
	if w.pageColor && (w.key == b.Keys[1] || w.key == b.Keys[2]) {
		a, d := displayCompareOptions(w.owner, w.key)
		return b.CanLink, (w.key == b.Keys[1] && a) || (w.key == b.Keys[2] && d)
	}
	if !w.pageColor && (w.key == b.Keys[1] || w.key == b.Keys[2]) {
		return displayBAxisLink(w.owner, w.key)
	}
	return false, false
}

func displayEditorFollowKeyFor(w *displayEditor) displayEditorFollowKey {
	return displayEditorFollowKey{key: w.key, identity: w.identity, color: w.pageColor}
}

func displayEditorPoststackLinked(w *displayEditor) bool {
	e := displayEditorSurface(w)
	if e == nil {
		return false
	}
	available, _ := displayEditorPoststackFollow(w, e)
	return available && displayChecked(w.controls[deCompareMatchA])
}

func displayEditorColorFollowsA(w *displayEditor, e *displaySurfaceState) bool {
	if e == nil || e.s.Compare == nil {
		return false
	}
	if e.s.Compare.PoststackProfiles {
		return w.pageColor && displayEditorPoststackLinked(w)
	}
	return w.key == e.s.Compare.Keys[2] && displayEditorCompareLinked(w)
}

func displayEditorCompareTargets(w *displayEditor, b *displayCompareBinding) []int {
	if w.pageColor {
		return displayColorTargets(w.owner, b.Keys)
	}
	var result []int
	if o := displayOwners[w.owner]; o != nil {
		for i, key := range b.Keys {
			if e := o.surfaces[key]; e != nil && e.order != 0 && len(e.s.Axes) > 0 {
				result = append(result, i)
			}
		}
	}
	return result
}

func displayPopulateComparePicker(w *displayEditor, e *displaySurfaceState) {
	b := e.s.Compare
	if b == nil {
		w.compareKeys, w.compareTargets, w.compareModeDirty = [3]string{}, nil, false
		return
	}
	if !b.PoststackProfiles {
		if w.compareKeys != b.Keys || !w.compareModeDirty {
			l, f := displayCompareOptions(w.owner, w.key)
			w.compareKeys, w.compareIndependent, w.compareModeDirty = b.Keys, !(l && f), false
		}
		mode := 0
		if w.compareIndependent || !b.CanLink {
			mode = 1
		}
		displayEditorCombo(w.controls[deCompareMode], []string{"一致：A、B、差值共用", "独立：分别设置 A、B、差值"}, mode)
	} else {
		w.compareKeys = b.Keys
		available, checked := displayEditorPoststackFollow(w, e)
		if pending, ok := w.compareFollowDraft[displayEditorFollowKeyFor(w)]; available && ok {
			checked = pending
		}
		displayCheck(w.controls[deCompareMatchA], available && checked)
		label := "与 A 一致"
		if w.pageColor {
			if w.key == b.Keys[1] {
				label += "（色标样式）"
			} else {
				label += "（色表与振幅范围）"
			}
		} else {
			label += "（坐标刻度）"
		}
		setText(w.controls[deCompareMatchA], label)
	}
	names := []string{"A", "B", "差值 (A−B)"}
	w.compareTargets = displayEditorCompareTargets(w, b)
	var labels []string
	selected := 0
	for i, panel := range w.compareTargets {
		labels = append(labels, names[panel])
		if w.key == b.Keys[panel] {
			selected = i
		}
	}
	displayEditorCombo(w.controls[deComparePanel], labels, selected)
}

func displayRefreshComparePicker(w *displayEditor) {
	w.comparePickerPending = false
	e := displayEditorSurface(w)
	if e == nil || w.invalid || !w.pageColor && (e.s.Compare == nil || !e.s.Compare.PoststackProfiles) {
		return
	}
	var keys [3]string
	var targets []int
	if b := e.s.Compare; b != nil {
		keys = b.Keys
		targets = displayEditorCompareTargets(w, b)
		// When the difference is closed or B replaced, a stale object must
		// not keep its controls behind a picker that now displays A.
		if b.PoststackProfiles && e.order == 0 && len(targets) > 0 {
			displayOpenEditor(w.owner, keys[targets[0]], w.axis, w.pageColor)
			return
		}
	}
	if keys == w.compareKeys && len(targets) == len(w.compareTargets) {
		same := true
		for i, v := range targets {
			same = same && v == w.compareTargets[i]
		}
		if same {
			return
		}
	}
	w.loading = true
	displayPopulateComparePicker(w, e)
	displayLayoutEditor(w)
	w.loading = false
}

func displayStoreDraft(w *displayEditor) {
	if w.key == "" || w.invalid {
		return
	}
	d, k := displayCaptureDraft(w), displayDraftKey(w)
	if w.pageColor {
		if w.gradientSessions == nil {
			w.gradientSessions = make(map[string]displayGradientSession)
		}
		w.gradientSessions[k] = d.gradient
	}
	if base, ok := w.draftBaselines[k]; ok && displayDraftEquivalent(d, base) && !displayEditorFollowChanged(w) {
		delete(w.drafts, k)
		return
	}
	w.drafts[k] = d
}

func displayCaptureDraft(w *displayEditor) displayDraft {
	d := displayDraft{text: map[int]string{}, selects: map[int]int{}, checks: map[int]bool{}, initial: w.initial, stops: append([]displaycore.GradientStop(nil), w.stops...),
		bounds: w.exactBounds, key: w.key, identity: w.identity, axis: w.axis, color: w.pageColor, distance: w.distanceDraft, spacing: w.distanceSpacingText}
	if w.pageColor {
		displayEditorBindGradient(w)
		d.gradient = w.gradientEdit.clone()
	}
	for _, id := range displayTextIDs {
		d.text[id] = getText(w.controls[id])
	}
	for _, id := range displayCheckIDs {
		d.checks[id] = displayChecked(w.controls[id])
	}
	for _, id := range []int{deFormat, dePreset, deRangePolicy, dePercentPreset} {
		d.selects[id] = displaySelection(w.controls[id])
	}
	if displayScientificTickEditor(w) {
		d.selects[deTickModeScientific] = displaySelection(w.controls[deTickModeScientific])
	}
	return d
}

func displayRestoreDraftControls(w *displayEditor, d displayDraft) {
	wasLoading := w.loading
	w.loading = true
	defer func() { w.loading = wasLoading }()
	for id, v := range d.text {
		displayField(w, id, v)
	}
	for id, v := range d.selects {
		pSendMessageW.Call(w.controls[id], CB_SETCURSEL, uintptr(v), 0)
	}
	for id, b := range d.checks {
		displayCheck(w.controls[id], b)
	}
	w.initial, w.stops = d.initial, append([]displaycore.GradientStop(nil), d.stops...)
	if w.pageColor {
		displayEditorRestoreGradient(w, d.gradient)
	}
	w.exactBounds = d.bounds
	w.distanceDraft, w.distanceSpacingText = d.distance, d.spacing
}

func displayEditorIsPoststackBColor(w *displayEditor, e *displaySurfaceState) bool {
	return w.pageColor && e != nil && e.s.Compare != nil && e.s.Compare.PoststackProfiles && w.key == e.s.Compare.Keys[1]
}

func displayLoadColorFields(w *displayEditor, m *displaycore.ColorMapping) {
	w.stops = append(w.stops[:0], m.Stops...)
	if len(w.stops) == 0 {
		w.stops = displayPreset(0)
	}
	displayField(w, deTitle, m.Title)
	displayField(w, deUnit, m.Unit)
	displayEditorShowBounds(w, m.Range, false)
	displayEditorSetPolicy(w, m.EffectiveRangePolicy())
	displayEditorSetPercentileFields(w, *m)
	displayField(w, deStep, displayStep(m.Step))
	displayField(w, deDigits, strconv.Itoa(m.Digits))
	displayField(w, deTickLength, displayNum(m.MajorTickDIP()))
	displayEditorLoadTypography(w, m.Divisions, m.TickFontPT, m.TitleFontPT)
	displayCheck(w.controls[deTickAuto], m.Step == 0)
	pSendMessageW.Call(w.controls[deFormat], CB_SETCURSEL, uintptr(displayEditorFormatIndex(m.Format)), 0)
	for id, checked := range map[int]bool{deAuto: m.Auto, deSym: m.Symmetric, deReverse: m.Reverse} {
		displayCheck(w.controls[id], checked)
	}
}

func displayEditorLoadTypography(w *displayEditor, divisions int, tick, title float64) {
	if divisions == 0 {
		divisions = displaycore.DefaultTickDivisions
	}
	displayField(w, deTickDivisions, strconv.Itoa(divisions))
	displayField(w, deTickFont, displayNum(displayPlotFontPT(tick, false)))
	displayField(w, deTitleFont, displayNum(displayPlotFontPT(title, true)))
}

func displayEditorUsesEqualTicks(w *displayEditor) bool {
	e := displayEditorSurface(w)
	return e != nil && e.s.equalTicks
}

func displayScientificTickEditor(w *displayEditor) bool {
	return !w.pageColor && (w.key == traceScientificWaveKey || w.key == traceScientificSpecKey)
}

func displayEditorParseFont(w *displayEditor, id int) (float64, error) {
	return displayEditorTypographyValue(w, id, 0, false)
}

func displayEditorPreviewPlotFont(w *displayEditor, fallback float64) float64 {
	n, _ := displayEditorTypographyValue(w, dePlotTitleFont, displayPlotFontPT(fallback, true), true)
	return n
}

func displayEditorParseTypography(w *displayEditor) (divisions int, tick, title float64, err error) {
	return displayEditorParseTypographyMode(w, false, 0, 0)
}

func displayEditorParseTypographyMode(w *displayEditor, preview bool, tickFallback, titleFallback float64) (divisions int, tick, title float64, err error) {
	divisions, err = displayEditorParseDivisions(w)
	if err != nil {
		return
	}
	if tick, err = displayEditorTypographyValue(w, deTickFont, displayPlotFontPT(tickFallback, false), preview); err == nil {
		title, err = displayEditorTypographyValue(w, deTitleFont, displayPlotFontPT(titleFallback, true), preview)
	}
	return
}

func displayUnchangedFontDefault(parsed, previous float64, title bool) float64 {
	if previous == 0 && parsed == displayPlotFontPT(previous, title) {
		return previous
	}
	return parsed
}

func displayEditorHasDistance(w *displayEditor, e *displaySurfaceState) bool {
	return !w.pageColor && e != nil && e.s.distance && len(e.s.Axes) > 0 && w.axis == e.s.Axes[0].ID
}

func displayEditorBindDistance(w *displayEditor, e *displaySurfaceState) {
	if !e.s.distance {
		return
	}
	identity := e.s.Identity
	if e.s.Compare != nil {
		if owner := displayOwners[w.owner]; owner != nil {
			if a := owner.surfaces[e.s.Compare.Keys[0]]; a != nil {
				identity = a.s.Identity
			}
		}
	}
	base := displayDistanceSettingsFor(w.owner)
	mode := displayDistanceCurrentKey(w.owner).mode
	if w.distanceDraftOwner != identity || w.distanceDraftMode != mode || w.distanceBase != base {
		w.distanceDraftOwner, w.distanceDraft, w.distanceBase = identity, base, base
		w.distanceDraftMode = mode
		w.distanceSpacingText = displayNum(base.SpacingM)
	}
	displayEditorPresentDistance(w)
}

func displayEditorPresentDistance(w *displayEditor) {
	set := w.distanceDraft
	setCombo := func(id int, on bool) {
		n := uintptr(0)
		if on {
			n = 1
		}
		pSendMessageW.Call(w.controls[id], CB_SETCURSEL, n, 0)
	}
	setCombo(deDistanceMode, set.Enabled)
	setCombo(deDistanceCalibration, set.Manual)
	displayField(w, deDistanceSpacing, w.distanceSpacingText)
	displayEditorRefreshDistance(w)
}

func displayEditorReadDistance(w *displayEditor) (displayDistanceSettings, error) {
	set := w.distanceDraft
	if v, err := displayParseNumber(w.distanceSpacingText, false); err == nil && v > 0 {
		set.SpacingM = v
	} else if set.Enabled && set.Manual {
		return set, fmt.Errorf("手动道间距必须为有限正数（m）")
	}
	return set, nil
}

func displayEditorRefreshDistance(w *displayEditor) {
	if w.section != 4 || !displayEditorHasDistance(w, displayEditorSurface(w)) {
		return
	}
	set, err := displayEditorReadDistance(w)
	text := "应用后更新横坐标；保留当前逻辑道范围，不改变数据几何或样点。"
	if err != nil {
		text += "\r\n" + err.Error()
	} else if set.Enabled {
		spacing := "随真实道位置变化（非等距）"
		if v, ok := displayDistanceEffectiveSpacing(w.owner, set); ok {
			spacing = strconv.FormatFloat(v, 'g', 10, 64) + " m"
		}
		text += "\r\n实际道间距：" + spacing + "；来源：" + displayDistanceSourceWithSettings(w.owner, set)
		text += "\r\n零点：当前完整剖面首个道中心，不随缩放或平移改变。"
	} else {
		text += "\r\n当前草稿使用原始道号、Inline 或 Crossline 编号。"
	}
	if getText(w.controls[deDistanceActual]) != text {
		displayField(w, deDistanceActual, text)
	}
}

func displayEditorPresentBColorLink(w *displayEditor, e *displaySurfaceState) {
	if !displayEditorIsPoststackBColor(w, e) {
		return
	}
	wasLoading := w.loading
	w.loading = true
	defer func() { w.loading = wasLoading }()
	// Effective tone is consumed by the preview; never reload B's local
	// title/unit/annotation edits during a live statistics update.
	w.bColorLocalDraft = nil
}
func displayEditorSurface(w *displayEditor) *displaySurfaceState {
	if o := displayOwners[w.owner]; o != nil {
		return o.surfaces[w.key]
	}
	return nil
}
func displayField(w *displayEditor, id int, s string) { setText(w.controls[id], s) }
func displayNum(v float64) string                     { return strconv.FormatFloat(v, 'g', -1, 64) }
func displayStep(v float64) string {
	if v <= 0 {
		return ""
	}
	return displayNum(v)
}

func displayEditorFont(w *displayEditor, dpi int) {
	if w.font != 0 && w.fontDPI == dpi {
		return
	}
	old := w.font
	height := -max(1, 13*dpi/96)
	w.font, _, _ = pCreateFontW.Call(uintptr(height), 0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(u16("Microsoft YaHei UI"))))
	w.fontDPI = dpi
	if w.font != 0 {
		for _, h := range w.controls {
			pSendMessageW.Call(h, WM_SETFONT, w.font, 0)
		}
		for _, h := range w.labels {
			pSendMessageW.Call(h, WM_SETFONT, w.font, 0)
		}
		pSendMessageW.Call(w.status, WM_SETFONT, w.font, 0)
	}
	if old != 0 {
		pDeleteObject.Call(old)
	}
}

func displayEditorDraftAxis(w *displayEditor) (displaycore.Axis, error) {
	return displayEditorDraftAxisMode(w, false)
}

func displayEditorPreviewAxis(w *displayEditor) (displaycore.Axis, error) {
	return displayEditorDraftAxisMode(w, true)
}

func displayEditorDraftAxisMode(w *displayEditor, preview bool) (displaycore.Axis, error) {
	a := w.unit
	if !w.pageColor && displayEditorPoststackLinked(w) {
		if e := displayEditorSurface(w); e != nil && e.s.Compare != nil {
			if o := displayOwners[w.owner]; o != nil {
				key := e.s.Compare.Keys[0]
				if peer := o.surfaces[key]; peer != nil {
					for _, axis := range peer.s.Axes {
						if axis.ID == a.ID && axis.CanonicalUnit() == a.CanonicalUnit() {
							styled := displayAxisFor(w.owner, key, axis)
							styled.Data, styled.View, styled.Down, styled.StyleOnly, styled.ViewEdited, styled.AllowsPoint = a.Data, a.View, a.Down, a.StyleOnly, false, a.AllowsPoint
							return styled, styled.Valid()
						}
					}
				}
			}
		}
		return a, a.Valid()
	}
	a.Unit = strings.TrimSpace(getText(w.controls[deUnit]))
	a.Title = strings.TrimSpace(getText(w.controls[deTitle]))
	a.Format = displayEditorFormat(displaySelection(w.controls[deFormat]))
	var err error
	a.Digits, err = displayEditorDigits(w, a.Format, a.Digits, preview)
	if err != nil {
		return a, err
	}
	fallbackLength := a.MajorTickDIP()
	if displayEditorUsesEqualTicks(w) {
		fallbackLength = displayEqualAxisTickDIP(a)
	}
	length, err := displayEditorTypographyValue(w, deTickLength, fallbackLength, preview)
	if err != nil {
		return a, err
	}
	if displayEditorUsesEqualTicks(w) {
		divisions, tick, title, err := displayEditorParseTypographyMode(w, preview, a.TickFontPT, a.TitleFontPT)
		if err != nil {
			return a, err
		}
		if a.Divisions != 0 || divisions != displaycore.DefaultTickDivisions {
			a.Divisions = divisions
		}
		a.TickFontPT = displayUnchangedFontDefault(tick, a.TickFontPT, false)
		a.TitleFontPT = displayUnchangedFontDefault(title, a.TitleFontPT, true)
		if length != displayEqualAxisTickDIP(a) {
			a.TickLength, a.TickLengthSet = length, true
		}
	} else {
		a.TickLength, a.TickLengthSet = length, true
		a.Step = 0
		mode := displaySelection(w.controls[deTickModeScientific])
		scientific := displayScientificTickEditor(w)
		if scientific {
			a.Divisions = 0
			if mode == 2 {
				n, err := displayEditorParseDivisions(w)
				if err != nil {
					return a, err
				}
				a.Divisions = n
			}
		}
		if scientific && mode == 1 || !scientific && !displayChecked(w.controls[deTickAuto]) {
			step, err := displayParseNumber(getText(w.controls[deStep]), false)
			if err != nil || step <= 0 {
				return a, fmt.Errorf("手动间隔必须为有限正数")
			}
			a.Step = a.BaseValue(step)
		}
	}
	view, edited, err := displayEditorTypedBounds(w, &a)
	if err != nil {
		return a, err
	}
	a.View = view
	if (!displayScientificTickEditor(w) || edited) && !a.StyleOnly && a.ValidRange(a.Data) && (a.View.Min < a.Data.Min || a.View.Max > a.Data.Max) {
		return a, fmt.Errorf("视窗范围须在数据边界 %s ～ %s %s 内", displayNum(a.DisplayValue(a.Data.Min)), displayNum(a.DisplayValue(a.Data.Max)), a.Unit)
	}
	a.Reverse = !a.StyleOnly && displayChecked(w.controls[deReverse])
	return a, a.Valid()
}

func displayEditorDraftTicks(w *displayEditor) (displaycore.TickPlan, string, error) {
	e := displayEditorSurface(w)
	if e == nil {
		return displaycore.TickPlan{}, "", fmt.Errorf("显示对象已关闭")
	}
	if w.pageColor {
		s, _ := displayCommitted(w.owner, w.key)
		if s.Color == nil {
			return displaycore.TickPlan{}, "", fmt.Errorf("没有连续色标")
		}
		m, err := displayEditorPreviewMapping(w, *s.Color)
		if err != nil {
			return displaycore.TickPlan{}, "", err
		}
		return displayDraftTickPlan(w.owner, w.key, nil, &m), m.Unit, nil
	}
	a, err := displayEditorPreviewAxis(w)
	if err != nil {
		return displaycore.TickPlan{}, "", err
	}
	p, followed := displaycore.TickPlan{}, false
	if displayEditorPoststackLinked(w) {
		p, followed = displayCompareEditorReferenceTicks(w.owner, w.key, a)
	}
	if !followed {
		p = displayDraftTickPlan(w.owner, w.key, &a, nil)
	}
	p.Step = a.DisplayValue(p.Step)
	return p, a.Unit, nil
}

func displayEditorRefreshTicks(w *displayEditor) {
	issue := displayEditorRefreshTypographyHints(w)
	plan, unit, err := displayEditorDraftTicks(w)
	if err != nil {
		setText(w.controls[deTickCurrent], "当前间隔：参数待校验")
		setText(w.controls[deTickPreview], err.Error())
		return
	}
	mode := "手动"
	if displayChecked(w.controls[deTickAuto]) {
		mode = "自动"
	}
	if displayEditorUsesEqualTicks(w) {
		mode = "等分"
	}
	if displayScientificTickEditor(w) {
		i := displaySelection(w.controls[deTickModeScientific])
		if i >= 0 && i <= 2 {
			mode = [...]string{"自动整洁", "手动间隔", "手动等分"}[i]
		}
	}
	if !w.pageColor && displayEditorPoststackLinked(w) {
		mode = "与 A 一致"
	}
	unitSuffix := ""
	if unit != "" {
		unitSuffix = " " + unit
	}
	text := fmt.Sprintf("%s（当前：%s%s）", mode, displaycore.FormatCompactNumber(plan.Step), unitSuffix)
	if plan.SinglePoint {
		text = "当前剖面 1 道 · 1 个真实坐标刻度"
	} else if plan.IntegerPositions {
		text = fmt.Sprintf("目标等分间隔 %s%s · 实际 %d 个刻度", displaycore.FormatCompactNumber(plan.Step), unitSuffix, len(plan.Ticks))
		if plan.IntegerMinStep > 0 {
			spacing := displaycore.FormatCompactNumber(plan.IntegerMinStep)
			if plan.IntegerMinStep != plan.IntegerMaxStep {
				spacing += "～" + displaycore.FormatCompactNumber(plan.IntegerMaxStep)
			}
			text = fmt.Sprintf("整数间隔 %s%s，实际 %d 个刻度 · 目标等分间隔 %s%s", spacing, unitSuffix, len(plan.Ticks), displaycore.FormatCompactNumber(plan.Step), unitSuffix)
		}
	} else if displayEditorUsesEqualTicks(w) {
		text = fmt.Sprintf("%s · 实际间隔：%s", mode, displaycore.FormatCompactNumber(plan.Step))
		if unit != "" {
			text += " " + unit
		}
	}
	if getText(w.controls[deTickCurrent]) != text {
		setText(w.controls[deTickCurrent], text)
	}
	if !displayEditorUsesEqualTicks(w) && displayChecked(w.controls[deTickAuto]) && plan.Step > 0 {
		wasLoading := w.loading
		w.loading = true
		displayField(w, deStep, displayNum(plan.Step))
		w.loading = wasLoading
	}
	text = "格式预览：" + displaycore.TickLabelSummary(plan.Ticks)
	if issue != "" {
		text += "\r\n" + issue + " · 使用上一次有效样式预览。"
	} else if plan.Warning != "" {
		text += "\r\n" + plan.Warning
	}
	if getText(w.controls[deTickPreview]) != text {
		setText(w.controls[deTickPreview], text)
	}
}

// Called by the owner after navigation/paint. Never replace a dirty range
// draft with a navigation result, nor turn an automatic tick into manual.
func displayEditorRefreshViewport(w *displayEditor) {
	if w == nil || w.loading || w.invalid || displayEditorSurface(w) == nil {
		return
	}
	if !w.pageColor {
		e := displayEditorSurface(w)
		for _, current := range e.s.Axes {
			if current.ID != w.axis {
				continue
			}
			a := displayAxisFor(w.owner, w.key, current)
			current, edited, err := displayEditorTypedBounds(w, &w.unit)
			if !edited && err == nil {
				displayEditorShowBounds(w, a.View, false)
				w.viewportDifferent = false
			} else {
				w.viewportDifferent = err != nil || current != a.View
			}
			w.unit.Data, w.unit.View = a.Data, a.View
			break
		}
	} else {
		displayEditorPresentBColorLink(w, displayEditorSurface(w))
	}
	displayEditorSummary(w)
	displayEditorRefreshTicks(w)
	displayEditorRefreshDistance(w)
	displayEditorRefreshRangeStatus(w)
	displayEditorRefreshFooter(w)
	pInvalidateRect.Call(w.body, 0, 0)
}
func displayLoadDraft(w *displayEditor) {
	e := displayEditorSurface(w)
	if e == nil {
		return
	}
	w.loading = true
	defer func() { w.loading = false }()
	s, _ := displayCommitted(w.owner, w.key)
	displayField(w, dePlotTitleFont, displayNum(displayPlotFontPT(s.PlotTitleFontPT, true)))
	if w.pageColor && s.Color == nil {
		w.pageColor = false
	}
	aIndex := 0
	names := make([]string, len(e.s.Axes))
	for i, a := range e.s.Axes {
		names[i] = a.Title
		if names[i] == "" {
			names[i] = a.ID
		}
		if a.ID == w.axis {
			aIndex = i
		}
	}
	if len(e.s.Axes) > 0 {
		w.axis = e.s.Axes[aIndex].ID
	}
	if w.section == 4 && !displayEditorHasDistance(w, e) {
		w.section = 0
	}
	displayEditorBindDistance(w, e)
	displayEditorCombo(w.controls[deAxisSelect], names, aIndex)
	for _, id := range displayCheckIDs {
		displayCheck(w.controls[id], false)
	}
	w.stops = nil
	w.bColorLocalDraft = nil
	w.selection = 0
	w.scroll = 0
	w.viewportDifferent = false
	if w.pageColor || e.s.Compare != nil && e.s.Compare.PoststackProfiles {
		displayPopulateComparePicker(w, e)
	}
	if !w.pageColor && w.section == 1 {
		w.section = 0
	}
	if w.pageColor {
		setText(w.controls[deHide], "主图显示色标")
		m := s.Color
		if b := e.s.Compare; b != nil && b.PoststackProfiles && w.key != b.Keys[0] && !displayEditorColorFollowsA(w, e) {
			if local := displayIndependentColor(w.owner, w.key); local != nil {
				m = local
			}
		}
		displayLoadColorFields(w, m)
		displayCheck(w.controls[deHide], !m.Hidden)
		displayEditorCombo(w.controls[dePreset], append([]string{"自定义/原色表", "灰度", "反向灰度", "蓝白红", "连续彩色"}, paletteNames...), 0)
	} else if aIndex < len(e.s.Axes) {
		setText(w.controls[deHide], "隐藏坐标轴")
		a := displayAxisFor(w.owner, w.key, e.s.Axes[aIndex])
		if b := e.s.Compare; b != nil && b.PoststackProfiles && w.key == b.Keys[1] {
			for _, local := range displayIndependentAxes(w.owner, w.key) {
				if local.ID == a.ID {
					a = local
					break
				}
			}
		}
		w.unit = a
		displayField(w, deTitle, a.Title)
		displayField(w, deUnit, a.Unit)
		displayEditorShowBounds(w, a.View, false)
		displayField(w, deStep, displayStep(a.DisplayValue(a.Step)))
		displayField(w, deDigits, strconv.Itoa(a.Digits))
		length := a.MajorTickDIP()
		if e.s.equalTicks {
			length = displayEqualAxisTickDIP(a)
		}
		displayField(w, deTickLength, displayNum(length))
		displayEditorLoadTypography(w, a.Divisions, a.TickFontPT, a.TitleFontPT)
		displayCheck(w.controls[deTickAuto], a.Step == 0)
		if displayScientificTickEditor(w) {
			mode := 0
			if a.Step > 0 {
				mode = 1
			} else if a.Divisions > 0 {
				mode = 2
			}
			pSendMessageW.Call(w.controls[deTickModeScientific], CB_SETCURSEL, uintptr(mode), 0)
			displayCheck(w.controls[deTickAuto], mode == 0)
		}
		pSendMessageW.Call(w.controls[deFormat], CB_SETCURSEL, uintptr(displayEditorFormatIndex(a.Format)), 0)
		for id, b := range map[int]bool{deReverse: a.Reverse, deHide: a.HideAxis, deHideLabels: a.HideLabels, deHideTitle: a.HideTitle} {
			displayCheck(w.controls[id], b)
		}
	}
	if w.pageColor {
		displayField(w, dePositionLow, "0")
		displayField(w, dePositionHigh, "100")
	}
	if w.draftBaselines == nil {
		w.draftBaselines = make(map[string]displayDraft)
	}
	w.draftBaselines[displayDraftKey(w)] = displayCaptureDraft(w)
	if d, ok := w.drafts[displayDraftKey(w)]; ok {
		displayRestoreDraftControls(w, d)
		if !w.pageColor {
			// Saved range text is already in the draft unit. Changing axis/page
			// must not interpret a seconds draft as milliseconds on reopening.
			w.unit.Unit = getText(w.controls[deUnit])
		}
	}
	w.nodeEditError = ""
	if w.visiting {
		return
	}
	displayEditorPresentBColorLink(w, e)
	setText(w.handle, "显示设置 — "+e.s.Title)
	setText(w.status, "草稿仅在应用或确定后提交。")
	displayEditorSummary(w)
	displayLayoutEditor(w)
	displayEditorRefreshTicks(w)
	displayEditorRefreshRangeStatus(w)
	if w.pageColor {
		displayRequestStats(w.owner, w.key, e)
		if b := e.s.Compare; b != nil && (displayEditorCompareLinked(w) || displayEditorColorFollowsA(w, e)) {
			for _, k := range b.Keys[:2] {
				if p := displayOwners[w.owner].surfaces[k]; p != nil {
					displayRequestStats(w.owner, k, p)
				}
			}
		}
	}
	w.loading = false
	displayEditorRefreshFooter(w)
}
func displayPreset(i int) []displaycore.GradientStop {
	dark, light := displaycore.RGB{}, displaycore.RGB{R: 255, G: 255, B: 255}
	switch i {
	case 1:
		return []displaycore.GradientStop{{Position: 0, Color: light}, {Position: 1, Color: dark}}
	case 2:
		return []displaycore.GradientStop{{Position: 0, Color: displaycore.RGB{B: 255}}, {Position: .5, Color: light}, {Position: 1, Color: displaycore.RGB{R: 255}}}
	case 3:
		return []displaycore.GradientStop{{Position: 0, Color: displaycore.RGB{R: 68, G: 1, B: 84}}, {Position: .5, Color: displaycore.RGB{R: 33, G: 145, B: 140}}, {Position: 1, Color: displaycore.RGB{R: 253, G: 231, B: 37}}}
	default:
		return []displaycore.GradientStop{{Position: 0, Color: dark}, {Position: 1, Color: light}}
	}
}
func displayEditorSummary(w *displayEditor) {
	e := displayEditorSurface(w)
	if e == nil {
		return
	}
	displayEditorUpdateTitleTip(w, e)
	var lines []string
	if w.pageColor {
		switch {
		case e.pending || e.statsVersion != e.s.StatsVersion && e.statsError == "":
			lines = append(lines, "数据范围：正在统计当前可见区域…")
		case e.statsError != "":
			lines = append(lines, "数据范围："+e.statsError)
		case e.stats.Raw.Count == 0:
			lines = append(lines, "数据范围：无有效数据")
		default:
			d := e.stats.Raw
			lo, hi := displaycore.FormatCompactNumber(d.Range.Min), displaycore.FormatCompactNumber(d.Range.Max)
			lines = append(lines, fmt.Sprintf("数据范围：%s ～ %s  ·  %d 个有效样点", lo, hi, d.Count))
			if e.stats.MappedDomain {
				m := e.stats.Mapped
				lo, hi = displaycore.FormatAmplitudeBounds(m.Range)
				lines[0] += fmt.Sprintf("  ·  归一化输入：%s ～ %s", lo, hi)
			}
		}
		if c, _ := displayCommitted(w.owner, w.key); c.Color != nil {
			lines = append(lines, "主图已应用范围："+displaycore.FormatCompactNumber(c.Color.Range.Min)+" ～ "+displaycore.FormatCompactNumber(c.Color.Range.Max))
		}
		if e.s.Compare != nil && !e.s.Compare.CanLink {
			lines = append(lines, "当前显示变换或 B 状态不支持物理振幅共用。")
		}
	} else {
		for _, a := range e.s.Axes {
			if a.ID == w.axis {
				u := a
				u.Unit = getText(w.controls[deUnit])
				label := fmt.Sprintf("数据边界：%g ～ %g %s", u.DisplayValue(a.Data.Min), u.DisplayValue(a.Data.Max), u.Unit)
				if a.StyleOnly {
					label += "  ·  范围与方向只读"
				}
				lines = append(lines, label)
				break
			}
		}
	}
	summary := strings.Join(lines, "\r\n")
	if getText(w.controls[deSummary]) != summary {
		setText(w.controls[deSummary], summary)
	}
	if !w.pageColor {
		a := displayAxisFor(w.owner, w.key, w.unit)
		text := fmt.Sprintf("当前生效范围：%s ～ %s %s", displayNum(w.unit.DisplayValue(a.View.Min)), displayNum(w.unit.DisplayValue(a.View.Max)), getText(w.controls[deUnit]))
		if w.viewportDifferent {
			text += "\r\n视窗已变化；保留未应用的范围草稿。"
		}
		setText(w.controls[deRangeSummary], text)
	}
}

func displayLayoutEditor(w *displayEditor) {
	displayLayoutEditorAtDPI(w, displayDPI(w.handle))
}

type displayEditorNativeMeasurements struct {
	dpi, line, control, combo, rowGap int
	dc                                uintptr
	plot                              displayPlotMetrics
}

func displayEditorMeasureControls(w *displayEditor, dpi int) (displayEditorNativeMeasurements, func()) {
	px := func(n int) int { return displayPixelDIP(float64(n), dpi) }
	dc, _, _ := displayGetDC.Call(w.body)
	old := uintptr(0)
	if dc != 0 && w.font != 0 {
		old, _, _ = pSelectObject.Call(dc, w.font)
	}
	plot := displayMetrics(dc, dpi)
	line := max(px(15), plot.Height)
	control := max(px(23), line+px(7))
	combo := control
	if h := w.controls[deAxisSelect]; h != 0 {
		const cbGetItemHeight = 0x0154
		v, _, _ := pSendMessageW.Call(h, cbGetItemHeight, ^uintptr(0), 0)
		if int(v) > 0 && int(v) < px(60) {
			combo = max(combo, int(v)+px(4))
		}
	}
	m := displayEditorNativeMeasurements{dpi: dpi, line: line, control: control, combo: combo, rowGap: px(6), dc: dc, plot: plot}
	return m, func() {
		if dc != 0 {
			if old != 0 {
				pSelectObject.Call(dc, old)
			}
			displayReleaseDC.Call(w.body, dc)
		}
	}
}

func (m displayEditorNativeMeasurements) px(dip float64) int {
	return displayPixelDIP(dip, m.dpi)
}

func (m displayEditorNativeMeasurements) textHeight(text string, width int) int {
	if m.dc == 0 || strings.TrimSpace(text) == "" {
		return m.line
	}
	r := RECT{Right: int32(max(1, width))}
	const flags = 0x10 | 0x0400 | 0x0800 // DT_WORDBREAK | DT_CALCRECT | DT_NOPREFIX
	pDrawTextW.Call(m.dc, uintptr(unsafe.Pointer(u16(text))), uintptr(^uint32(0)), uintptr(unsafe.Pointer(&r)), flags)
	return max(m.line, int(r.Bottom-r.Top))
}

func (m displayEditorNativeMeasurements) frameRows() (head, foot, button int) {
	button = max(m.control, m.px(25))
	head = m.px(7) + max(button, m.line+m.px(9))
	foot = 2*m.line + button + m.px(15)
	return
}

func (m displayEditorNativeMeasurements) minimumClient() (int, int) {
	head, foot, button := m.frameRows()
	// Reserve the complete largest compact form and space for a useful
	// preview; this is deliberately independent of the active tab/policy.
	header := 4*m.line + m.combo + button + 4*m.rowGap
	rangeForm := 3*m.combo + button + 3*m.line + 7*m.rowGap
	return m.px(540), max(m.px(500), head+foot+header+rangeForm+m.px(115))
}

func displayEditorUpdateTitleTip(w *displayEditor, e *displaySurfaceState) {
	caption := ""
	if e != nil {
		caption = e.s.Title
	}
	h := w.controls[deSummaryTitle]
	if h == 0 {
		return
	}
	if caption != getText(h) {
		setText(h, caption)
	}
	if w.summaryTip.tooltip == 0 && caption != "" {
		const exTopmostNoActivate = 0x00000008 | 0x08000000
		w.summaryTip.tooltip, _, _ = pCreateWindowExW.Call(exTopmostNoActivate, uintptr(unsafe.Pointer(u16("tooltips_class32"))), 0,
			WS_POPUP|poststackTTSNoPrefix, 0, 0, 0, 0, w.handle, 0, 0, 0)
		if w.summaryTip.tooltip != 0 {
			pSendMessageW.Call(w.summaryTip.tooltip, poststackTTMMaxWidth, 0, 640)
		}
	}
	w.summaryTip.setPathTip(w.body, 0, h, caption)
	if title := w.controls[deTitle]; title != 0 {
		w.summaryTip.setPathTip(w.body, 1, title, getText(title))
	}
}

func displayEditorComboControl(id int) bool {
	switch id {
	case deFormat, dePreset, deAxisSelect, deComparePanel, deCompareMode, deTickDivisions, deTickModeScientific, deDistanceMode, deDistanceCalibration, deRangePolicy, dePercentPreset:
		return true
	}
	return false
}

func displayLayoutEditorAtDPI(w *displayEditor, dpi int) {
	r := clientRect(w.handle)
	sw, sh := int(r.Right), int(r.Bottom)
	if sw <= 0 || sh <= 0 {
		return
	}
	displayEditorFont(w, dpi)
	m, closeMetrics := displayEditorMeasureControls(w, dpi)
	defer closeMetrics()
	px := m.px
	shortWindow := sh < px(545)
	if shortWindow {
		m.rowGap = px(4)
	}
	pad := px(12)
	head, foot, button := m.frameRows()
	var frame, body []displayEditorPlacement
	moveFrame := func(h uintptr, x, y, ww, hh int) {
		frame = append(frame, displayEditorPlacement{h, x, y, ww, hh, true})
	}
	e := displayEditorSurface(w)
	displayEditorSyncTabs(w, e)
	displayEditorUpdateTitleTip(w, e)
	tabEdge := 2 * pad // Body inset plus the inner form inset.
	moveFrame(w.controls[deTopTabs], tabEdge, px(5), sw-2*tabEdge, head-px(6))
	if e != nil && e.s.applyPanelStyle != nil {
		moveFrame(w.controls[deApplyPanels], sw-tabEdge-px(125), px(3), px(125), min(button, head-px(9)))
		displayEnable(w.controls[deApplyPanels], !w.invalid)
	} else {
		frame = append(frame, displayEditorPlacement{h: w.controls[deApplyPanels]})
	}
	moveFrame(w.body, pad, head, sw-2*pad, max(1, sh-head-foot))
	moveFrame(w.status, pad, max(head, sh-foot+px(3)), sw-2*pad, 2*m.line+px(2))
	gap := px(8)
	bw := max(1, min(px(104), (sw-2*pad-3*gap)/4))
	for i, id := range []int{deReset, deApply, deOK, deCancel} {
		moveFrame(w.controls[id], max(pad, sw-pad-(4-i)*bw-(3-i)*gap), max(head, sh-px(7)-button), bw, button)
	}
	displayEditorPlaceWindows(frame)

	client := clientRect(w.body)
	width, bottom := int(client.Right), int(client.Bottom)
	x, y, areaWidth := pad, px(5), max(1, width-2*pad)
	fullWidth := areaWidth
	compact := sh < px(570)
	visible := map[int]bool{}
	place := func(id, x, y, ww, hh int) {
		h := w.controls[id]
		if h == 0 {
			return
		}
		visible[id] = true
		body = append(body, displayEditorPlacement{h, x, y, max(1, ww), hh, true})
	}
	textRow := func(id, lines int) {
		text := getText(w.controls[id])
		stable := id == deCoverage || id == deRangeSummary || id == deTickPreview
		if text == "" && !stable {
			return
		}
		textHeight := m.textHeight(text, areaWidth)
		if stable {
			textHeight = max(textHeight, 2*m.line)
		}
		hh := min(lines*m.line, textHeight) + px(3)
		place(id, x, y, areaWidth, hh)
		y += hh + m.rowGap
	}
	fields := func(ids ...int) {
		if len(ids) == 0 {
			return
		}
		unit := (areaWidth - (len(ids)-1)*gap) / len(ids)
		if unit <= 0 {
			return
		}
		laneWidth := func(i int) int { return unit }
		laneLeft := func(i int) int { return x + i*(unit+gap) }
		if len(ids) == 2 && ids[0] == deTitle && ids[1] == deUnit {
			unitWidth := min(px(145), max(px(80), areaWidth/3))
			titleWidth := max(1, areaWidth-unitWidth-gap)
			laneWidth = func(i int) int { return [...]int{titleWidth, unitWidth}[i] }
			laneLeft = func(i int) int { return [...]int{x, x + titleWidth + gap}[i] }
		} else if len(ids) == 3 && ids[2] == deNodeSwatch {
			swatchWidth := px(42)
			leftWidth := (areaWidth - swatchWidth - 2*gap) / 2
			laneWidth = func(i int) int {
				return [...]int{leftWidth, areaWidth - leftWidth - swatchWidth - 2*gap, swatchWidth}[i]
			}
			laneLeft = func(i int) int { return [...]int{x, x + leftWidth + gap, x + areaWidth - swatchWidth}[i] }
		}
		rowHeight := m.control
		for _, id := range ids {
			if displayEditorComboControl(id) {
				rowHeight = max(rowHeight, m.combo)
			}
		}
		inline := compact || w.section == displayPageRange || w.section == displayPageGradient
		maxLabelHeight := 0
		for i, id := range ids {
			if lab := w.labels[id]; lab != 0 {
				label := getText(lab)
				wide := laneWidth(i)
				if m.plot.Measure(label).Width+gap+px(74) > wide {
					inline = false
				}
				maxLabelHeight = max(maxLabelHeight, m.textHeight(label, wide))
			}
		}
		labelBand := 0
		if !inline && maxLabelHeight > 0 {
			labelBand = maxLabelHeight + px(3)
		}
		for i, id := range ids {
			left, ww := laneLeft(i), laneWidth(i)
			if lab := w.labels[id]; lab != 0 {
				if inline {
					labelWidth := m.plot.Measure(getText(lab)).Width + px(3)
					body = append(body, displayEditorPlacement{lab, left, y + (rowHeight-m.line)/2, labelWidth, m.line + 2, true})
					left, ww = left+labelWidth+gap, ww-labelWidth-gap
				} else {
					body = append(body, displayEditorPlacement{lab, left, y, ww, labelBand, true})
				}
			}
			controlHeight := rowHeight
			if displayEditorComboControl(id) {
				// Window height is the expanded drop-down height; the native
				// collapsed height, used above, was queried separately.
				controlHeight = px(205)
			}
			switch id {
			case deMin, deMax:
				ww = min(ww, px(240))
			case dePercentLow, dePercentHigh, dePositionLow, dePositionHigh, deTickLength, deNodePosition, deUnit:
				ww = min(ww, px(120))
			case deTickFont, deTitleFont, dePlotTitleFont:
				ww = min(ww, px(84))
			case deApplyPosition:
				ww = min(ww, max(px(85), m.plot.Measure(getText(w.controls[id])).Width+px(24)))
			}
			place(id, left, y+labelBand, ww, controlHeight)
		}
		y += labelBand + rowHeight + m.rowGap
	}
	buttons := func(ids ...int) {
		start := x
		for _, id := range ids {
			label := getText(w.controls[id])
			extra := px(23)
			if id == deSym || id == deHide || id == deReverse || id == deHideLabels || id == deHideTitle || id == deTickAuto {
				extra = px(24)
			}
			target := max(px(70), m.plot.Measure(label).Width+extra)
			if start > x && start+target > x+areaWidth {
				start = x
				y += button + m.rowGap
			}
			ww := min(areaWidth, target)
			place(id, start, y, ww, button)
			start += ww + gap
		}
		y += button + m.rowGap
	}
	// The title owns a hover tip and is always single-line. On short
	// screens its line can share the header row with the compact selectors.
	var selectorIDs []int
	if e != nil && e.s.Compare != nil && (w.pageColor || e.s.Compare.PoststackProfiles) {
		b := e.s.Compare
		if !b.PoststackProfiles {
			selectorIDs = append(selectorIDs, deCompareMode)
		}
		selectorIDs = append(selectorIDs, deComparePanel)
		if !w.pageColor {
			selectorIDs = append(selectorIDs, deAxisSelect)
		}
		if available, _ := displayEditorPoststackFollow(w, e); available {
			selectorIDs = append(selectorIDs, deCompareMatchA)
		}
	} else if !w.pageColor {
		selectorIDs = append(selectorIDs, deAxisSelect)
	}
	if shortWindow && len(selectorIDs) != 0 && fullWidth >= px(610) {
		titleWidth := min(fullWidth/3, px(220))
		areaWidth = fullWidth - titleWidth - gap
		titleTop := y
		fields(selectorIDs...)
		place(deSummaryTitle, x+areaWidth+gap, titleTop+(y-titleTop-m.rowGap-m.line)/2, titleWidth, m.line+px(3))
		areaWidth = fullWidth
	} else {
		place(deSummaryTitle, x, y, fullWidth, m.line+px(3))
		y += m.line + m.rowGap
		fields(selectorIDs...)
	}
	textRow(deSummary, 4)
	place(deSubTabs, x, y, fullWidth, max(button, m.line+px(9)))
	y += max(button, m.line+px(9)) + gap
	contentY := y
	section := w.section
	stylePage := section == displayPageTicks || section == displayPageAppearance
	horizontalStyle := stylePage && !w.pageColor && displayEditorAxisIsHorizontal(w, w.axis)
	rightSide := stylePage && !horizontalStyle && fullWidth >= px(260)+px(220)+px(12)
	cardWidth := 0
	if rightSide {
		cardWidth = min(px(260), max(px(220), fullWidth/3))
		areaWidth = fullWidth - cardWidth - px(12)
	}

	w.preview, w.graph, w.gradient, w.rangeBar, w.nodeBand = RECT{}, RECT{}, RECT{}, RECT{}, RECT{}
	switch section {
	case displayPageRange:
		if w.pageColor {
			policy := displayEditorRangePolicy(w)
			w.rangeLayoutPolicy = policy
			fields(deRangePolicy)
			fields(deMin, deMax)
			buttons(deSym, deFit, deCopyExact)
			if policy == displaycore.PercentileRange {
				if displaySelection(w.controls[dePercentPreset]) == 5 {
					fields(dePercentPreset, dePercentLow, dePercentHigh)
				} else {
					fields(dePercentPreset)
				}
			}
			textRow(deCoverage, 2)
			if policy == displaycore.ManualRange {
				textRow(dePositionHeading, 1)
				fields(dePositionLow, dePositionHigh, deApplyPosition)
			}
		} else {
			fields(deMin, deMax)
			buttons(deFit, deCopyExact)
			textRow(deRangeSummary, 2)
			textRow(dePositionHeading, 1)
			fields(dePositionLow, dePositionHigh, deApplyPosition)
		}
	case displayPageGradient:
		fields(dePreset, deReverse)
		fields(deNodePosition, deNodeColor, deNodeSwatch)
		infoWidth := max(1, (areaWidth-gap)/2)
		infoHeight := min(2*m.line+px(3), max(m.textHeight(getText(w.controls[deNodeReadout]), infoWidth), m.textHeight(getText(w.controls[deNodePending]), infoWidth))+px(3))
		place(deNodeReadout, x, y, infoWidth, infoHeight)
		place(deNodePending, x+infoWidth+gap, y, areaWidth-infoWidth-gap, infoHeight)
		y += infoHeight + m.rowGap
		buttons(deAdd, deDelete, deChoose)
	case displayPageTicks:
		if displayScientificTickEditor(w) {
			fields(deTickModeScientific, deTickLength)
			switch displaySelection(w.controls[deTickModeScientific]) {
			case 1:
				fields(deStep)
			case 2:
				fields(deTickDivisions)
			}
		} else if !displayEditorUsesEqualTicks(w) {
			buttons(deTickAuto)
			fields(deStep, deTickLength)
		} else {
			fields(deTickDivisions, deTickLength, deTickFont)
		}
		fields(deFormat, deDigits)
		textRow(deTickCurrent, 1)
		textRow(deTickPreview, 3)
	case displayPageDistance:
		fields(deDistanceMode, deDistanceCalibration)
		fields(deDistanceSpacing)
		textRow(deDistanceActual, 4)
	case displayPageAppearance:
		fields(deTitle, deUnit)
		if displayEditorUsesEqualTicks(w) {
			fields(deTitleFont, dePlotTitleFont)
		}
		if w.pageColor {
			buttons(deHide)
		} else {
			buttons(deHide, deHideLabels, deHideTitle)
			buttons(deReverse)
		}
	}

	if rightSide {
		w.preview = RECT{Left: int32(x + areaWidth + px(12)), Top: int32(contentY), Right: int32(x + areaWidth + px(12) + cardWidth), Bottom: int32(bottom - pad)}
	} else {
		w.preview = RECT{Left: int32(x), Top: int32(y + px(4)), Right: int32(x + fullWidth), Bottom: int32(bottom - px(5))}
		if horizontalStyle || !w.pageColor && section == displayPageRange {
			w.preview.Bottom = min(w.preview.Bottom, w.preview.Top+int32(px(145)))
		} else if stylePage && fullWidth >= px(220) {
			w.preview.Left = w.preview.Right - int32(min(fullWidth, px(260)))
		}
	}
	displayEditorLayoutPreview(w, dpi)
	w.scroll, w.scrollMax = 0, 0
	for id, h := range w.controls {
		if id == deTopTabs || id >= deReset && id <= deApplyPanels || visible[id] {
			continue
		}
		body = append(body, displayEditorPlacement{h: h})
		if lab := w.labels[id]; lab != 0 {
			body = append(body, displayEditorPlacement{h: lab})
		}
	}
	displayEditorPlaceWindows(body)
	axisLocked := !w.pageColor && displayEditorPoststackLinked(w)
	if e != nil {
		locked := false
		if b := e.s.Compare; b != nil && (w.pageColor || b.PoststackProfiles) {
			displayEnable(w.controls[deCompareMode], w.pageColor && b.CanLink && !b.PoststackProfiles)
			displayEnable(w.controls[deComparePanel], len(w.compareTargets) > 1)
			available, _ := displayEditorPoststackFollow(w, e)
			displayEnable(w.controls[deCompareMatchA], available)
			locked = w.pageColor && displayEditorColorFollowsA(w, e)
		}
		if !w.pageColor {
			locked = w.unit.StyleOnly || axisLocked
		}
		for _, id := range []int{deMin, deMax, deFit, deReverse, deAuto, deSym, dePreset, deRangePolicy, dePercentPreset, dePercentLow, dePercentHigh, dePositionLow, dePositionHigh, deApplyPosition} {
			enable := !locked
			if w.pageColor && id == deFit {
				enable = enable && displayRangeFor(w.owner, w.key, displayEditorCompareLinked(w)).Valid()
			}
			displayEnable(w.controls[id], enable)
		}
		for _, id := range []int{deTitle, deUnit, deHide, deHideLabels, deHideTitle, deTickAuto, deTickLength, deFormat, deTickDivisions, deTickFont, deTitleFont} {
			displayEnable(w.controls[id], !axisLocked)
		}
		displayEnable(w.controls[dePlotTitleFont], true)
		displayEnable(w.controls[deDistanceCalibration], displaySelection(w.controls[deDistanceMode]) == 1)
		displayEnable(w.controls[deDistanceSpacing], displaySelection(w.controls[deDistanceMode]) == 1 && displaySelection(w.controls[deDistanceCalibration]) == 1)
	}
	displayEnable(w.controls[deStep], !axisLocked && !displayChecked(w.controls[deTickAuto]))
	f := displayEditorFormat(displaySelection(w.controls[deFormat]))
	displayEnable(w.controls[deDigits], !axisLocked && (f == displaycore.FixedFormat || f == displaycore.ScientificFormat))
	reverseLabel := "反转坐标方向"
	if w.pageColor {
		reverseLabel = "反转色表"
	}
	if getText(w.controls[deReverse]) != reverseLabel {
		setText(w.controls[deReverse], reverseLabel)
	}
	if f == displaycore.ScientificFormat {
		setText(w.labels[deDigits], "尾数小数位 (0–12)")
	} else {
		setText(w.labels[deDigits], "小数位 (0–12)")
	}
	displayEditorRefreshNode(w, false)
	pRedrawWindow.Call(w.handle, 0, 0, RDW_INVALIDATE|RDW_ERASE|RDW_ALLCHILDREN|0x0400)
}

type displayEditorPlacement struct {
	h                   uintptr
	x, y, width, height int
	visible             bool
}

const (
	displayPositionNoRedraw     = 0x0008
	displayPositionShow         = 0x0040
	displayPositionHide         = 0x0080
	displayPositionNoCopy       = 0x0100
	displayPositionNoMoveOrSize = 0x0003
)

var displayBeginDefer = user32.NewProc("BeginDeferWindowPos")
var displayDeferPosition = user32.NewProc("DeferWindowPos")
var displayEndDefer = user32.NewProc("EndDeferWindowPos")

func (p displayEditorPlacement) flags() uintptr {
	base := uintptr(SWP_NOZORDER | SWP_NOACTIVATE | displayPositionNoRedraw | displayPositionNoCopy)
	if p.visible {
		return base | displayPositionShow
	}
	return base | displayPositionHide | displayPositionNoMoveOrSize
}

// Every entry in a batch has the same parent. A failed native batch is safe
// to replay without redrawing; paint is scheduled only after both batches.
func displayEditorPlaceWindows(positions []displayEditorPlacement) {
	batch, _, _ := displayBeginDefer.Call(uintptr(len(positions)))
	if batch != 0 {
		for _, p := range positions {
			if p.h == 0 {
				continue
			}
			batch, _, _ = displayDeferPosition.Call(batch, p.h, 0, uintptr(p.x), uintptr(p.y), uintptr(max(1, p.width)), uintptr(max(1, p.height)), p.flags())
			if batch == 0 {
				break
			}
		}
		if batch != 0 {
			if ok, _, _ := displayEndDefer.Call(batch); ok != 0 {
				return
			}
		}
	}
	for _, p := range positions {
		if p.h != 0 {
			pSetWindowPos.Call(p.h, 0, uintptr(p.x), uintptr(p.y), uintptr(max(1, p.width)), uintptr(max(1, p.height)), p.flags())
		}
	}
}

func displayParseNumber(text string, allowEmpty bool) (float64, error) {
	text = strings.TrimSpace(text)
	if allowEmpty && text == "" {
		return 0, nil
	}
	v, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("请输入有限数值")
	}
	return v, nil
}
func displayDraftMapping(w *displayEditor, m displaycore.ColorMapping) (displaycore.ColorMapping, error) {
	return displayEditorDraftMappingMode(w, m, false)
}

func displayEditorPreviewMapping(w *displayEditor, m displaycore.ColorMapping) (displaycore.ColorMapping, error) {
	return displayEditorDraftMappingMode(w, m, true)
}

func displayEditorDraftMappingMode(w *displayEditor, m displaycore.ColorMapping, preview bool) (displaycore.ColorMapping, error) {
	state := displayEditorResolveRangeState(w)
	if state.Err != nil {
		return m, state.Err
	}
	if !state.Ready {
		return m, fmt.Errorf("正在计算当前区域的草稿范围，请稍后应用")
	}
	m = state.Draft
	m.Title = strings.TrimSpace(getText(w.controls[deTitle]))
	m.Unit = strings.TrimSpace(getText(w.controls[deUnit]))
	m.Hidden = !displayChecked(w.controls[deHide])
	m.VisibilitySet = true
	var err error
	if displayEditorUsesEqualTicks(w) {
		divisions, tick, title, err := displayEditorParseTypographyMode(w, preview, m.TickFontPT, m.TitleFontPT)
		if err != nil {
			return m, err
		}
		if m.Divisions != 0 || divisions != displaycore.DefaultTickDivisions {
			m.Divisions = divisions
		}
		m.TickFontPT = displayUnchangedFontDefault(tick, m.TickFontPT, false)
		m.TitleFontPT = displayUnchangedFontDefault(title, m.TitleFontPT, true)
	} else {
		m.Step = 0
		if !displayChecked(w.controls[deTickAuto]) {
			m.Step, err = displayParseNumber(getText(w.controls[deStep]), false)
			if err != nil || m.Step <= 0 {
				return m, fmt.Errorf("手动间隔必须为有限正数")
			}
		}
	}
	tickLength, err := displayEditorTypographyValue(w, deTickLength, m.MajorTickDIP(), preview)
	if err != nil {
		return m, err
	}
	if tickLength != m.MajorTickDIP() {
		m.TickLength, m.TickLengthSet = tickLength, true
	}
	m.Format = displayEditorFormat(displaySelection(w.controls[deFormat]))
	m.Digits, err = displayEditorDigits(w, m.Format, m.Digits, preview)
	if err != nil {
		return m, err
	}
	return m, m.Valid()
}

func displayEditorTicksChanged(equal bool, step, oldStep float64, format, oldFormat displaycore.NumberFormat, digits, oldDigits, divisions, oldDivisions int, tick, oldTick float64) bool {
	if format != oldFormat || digits != oldDigits {
		return true
	}
	if !equal {
		return step != oldStep
	}
	if divisions == 0 {
		divisions = displaycore.DefaultTickDivisions
	}
	if oldDivisions == 0 {
		oldDivisions = displaycore.DefaultTickDivisions
	}
	return divisions != oldDivisions || displayPlotFontPT(tick, false) != displayPlotFontPT(oldTick, false)
}

func displayEditorColorTicksChanged(w *displayEditor, a, b displaycore.ColorMapping) bool {
	return displayEditorTicksChanged(displayEditorUsesEqualTicks(w), a.Step, b.Step, a.Format, b.Format, a.Digits, b.Digits, a.Divisions, b.Divisions, a.TickFontPT, b.TickFontPT)
}

// The explicit bulk action reads only typography/tick fields. No viewport,
// amplitude bounds, legend visibility or pending link changes are submitted.
func displayEditorApplyPanelStyle(w *displayEditor, e *displaySurfaceState) {
	if w.invalid || e.s.applyPanelStyle == nil || !e.s.equalTicks {
		return
	}
	s, _ := displayCommitted(w.owner, w.key)
	pt, err := displayEditorParseFont(w, dePlotTitleFont)
	if err != nil {
		setText(w.status, err.Error())
		return
	}
	s.PlotTitleFontPT = pt
	n := displaySelection(w.controls[deAxisSelect])
	a := displaycore.Axis{}
	if n >= 0 && n < len(s.Axes) {
		a = s.Axes[n]
	} else if !w.pageColor {
		return
	}
	if w.pageColor && s.Color == nil {
		return
	}
	if displayEditorPoststackLinked(w) && (!w.pageColor || displayEditorIsPoststackBColor(w, e)) {
		if w.pageColor {
			if peer, _ := displayCommitted(w.owner, e.s.Compare.Keys[0]); peer.Color != nil {
				s.Color = peer.Color
			}
		} else {
			a, err = displayEditorDraftAxis(w)
			s.Axes[n] = a
		}
	} else {
		divisions, tick, title, parseErr := displayEditorParseTypography(w)
		length, lengthErr := displayParseNumber(getText(w.controls[deTickLength]), false)
		digits, digitsErr := strconv.Atoi(strings.TrimSpace(getText(w.controls[deDigits])))
		format := displayEditorFormat(displaySelection(w.controls[deFormat]))
		switch {
		case parseErr != nil:
			err = parseErr
		case lengthErr != nil || length < 0 || length > displaycore.MaxMajorTickDIP:
			err = fmt.Errorf("刻度长度必须在 0–%d DIP 之间", displaycore.MaxMajorTickDIP)
		case digitsErr != nil || digits < 0 || digits > 12:
			err = fmt.Errorf("精度必须是 0–12 的整数")
		case w.pageColor:
			m := s.Color.Clone()
			m.Divisions, m.TickFontPT, m.TitleFontPT = divisions, tick, title
			m.Step, m.TickLength, m.TickLengthSet, m.Format, m.Digits = 0, length, true, format, digits
			s.Color = &m
		default:
			a.Divisions, a.TickFontPT, a.TitleFontPT = divisions, tick, title
			a.Step, a.TickLength, a.TickLengthSet, a.Format, a.Digits = 0, length, true, format, digits
			s.Axes[n] = a
		}
	}
	if err != nil {
		setText(w.status, err.Error())
		return
	}
	if err := e.s.applyPanelStyle(s, w.pageColor, n); err != nil {
		setText(w.status, err.Error())
		return
	}
	setText(w.status, "已将刻度和字体样式应用到 A/B/差；各图范围、显隐和联动保持不变。")
}

func displayApplyEditorPage(w *displayEditor, closeAfter bool) {
	if w.invalid {
		setText(w.status, "数据已更新，不能提交旧对象的草稿。")
		return
	}
	e := displayEditorSurface(w)
	if e == nil {
		return
	}
	if w.nodeEditError != "" {
		setText(w.status, w.nodeEditError)
		return
	}
	s, _ := displayCommitted(w.owner, w.key)
	if displayEditorUsesEqualTicks(w) {
		pt, err := displayEditorParseFont(w, dePlotTitleFont)
		if err != nil {
			setText(w.status, err.Error())
			return
		}
		s.PlotTitleFontPT = displayUnchangedFontDefault(pt, s.PlotTitleFontPT, true)
	}
	if b := e.s.Compare; b != nil && b.PoststackProfiles && w.key == b.Keys[1] {
		s.Axes = displayIndependentAxes(w.owner, w.key)
	}
	for i := range s.Axes {
		s.Axes[i].ViewEdited = false
	}
	link, follow := displayCompareOptions(w.owner, w.key)
	var axesFollow *bool
	if available, _ := displayEditorPoststackFollow(w, e); available && !w.pageColor {
		checked := displayEditorPoststackLinked(w)
		axesFollow = &checked
	}
	var distance *displayDistanceSettings
	if displayEditorHasDistance(w, e) && (w.distanceDraft != w.distanceBase || w.distanceSpacingText != displayNum(w.distanceBase.SpacingM)) {
		set, err := displayEditorReadDistance(w)
		if err != nil {
			setText(w.status, err.Error())
			return
		}
		if set != w.distanceBase {
			distance = &set
		}
	}
	if w.pageColor && s.Color != nil {
		if b := e.s.Compare; b != nil {
			if !b.PoststackProfiles {
				link = displayEditorCompareLinked(w)
				follow = link
			} else if available, _ := displayEditorPoststackFollow(w, e); available {
				switch w.key {
				case b.Keys[1]:
					link = displayEditorPoststackLinked(w)
				case b.Keys[2]:
					follow = displayEditorPoststackLinked(w)
				}
			}
		}
		m, err := displayDraftMapping(w, *s.Color)
		if err != nil {
			setText(w.status, err.Error())
			return
		}
		s.Color = &m
	} else if axesFollow != nil && *axesFollow {
		// Linking only changes presentation for B. Leave independent axes
		// and the already-shared plot range as the viewer last set them.
	} else {
		n := displaySelection(w.controls[deAxisSelect])
		if n < 0 || n >= len(e.s.Axes) {
			return
		}
		base := e.s.Axes[n]
		a, err := displayEditorDraftAxis(w)
		if err != nil {
			setText(w.status, err.Error())
			return
		}
		a.ViewEdited = false
		a.BaseUnit = base.CanonicalUnit()
		a.Reverse = !a.StyleOnly && displayChecked(w.controls[deReverse])
		a.HideAxis = displayChecked(w.controls[deHide])
		a.HideLabels = displayChecked(w.controls[deHideLabels])
		a.HideTitle = displayChecked(w.controls[deHideTitle])
		if _, dirty, err := displayEditorTypedBounds(w, &a); err == nil && !a.StyleOnly && dirty {
			a.ViewEdited = true
		}
		if err := a.Valid(); err != nil {
			setText(w.status, err.Error())
			return
		}
		if n < len(s.Axes) {
			s.Axes[n] = a
		}
	}
	if w.collect != nil {
		p := displayBatchEdit{key: w.key, identity: w.identity, axis: w.axis, color: w.pageColor, link: link, follow: follow, axesFollow: axesFollow, distance: distance, settings: s}
		if b := e.s.Compare; w.pageColor && b != nil {
			_, changed := w.compareFollowDraft[displayEditorFollowKeyFor(w)]
			p.setLink = changed && w.key == b.Keys[1]
			p.setFollow = changed && w.key == b.Keys[2]
		}
		w.collect(p)
		return
	}
	if err := displaySubmitWithDistance(w.owner, w.key, w.identity, s, link, follow, axesFollow, distance); err != nil {
		setText(w.status, err.Error())
		return
	}
	if w.pageColor {
		w.compareModeDirty = false
	}
	delete(w.compareFollowDraft, displayEditorFollowKeyFor(w))
	delete(w.drafts, displayDraftKey(w))
	if closeAfter {
		displayDestroyEditor(w)
		return
	}
	displayLoadDraft(w)
	setText(w.status, "已应用。")
}

func displayEditorCommand(w *displayEditor, id, code int) {
	// Cancel must work even when its plot disappeared or switched sources.
	// Do not run a refresh after destroying the dialog and its controls.
	if id == deCancel {
		displayDestroyEditor(w)
		return
	}
	if w.loading {
		return
	}
	e := displayEditorSurface(w)
	if e == nil {
		return
	}
	if id != deApply && id != deOK {
		w.stateError = ""
		defer func() { displayEditorRefreshRangeStatus(w); displayEditorRefreshFooter(w) }()
	}
	if displayEditorGradientCommand(w, id, code) {
		return
	}
	switch id {
	case deApplyPanels:
		displayEditorCopyTo(w, e)
		return
	case deCopyExact:
		displayEditorCopyExact(w)
		return
	case deRangePolicy:
		if code != 1 || !w.pageColor || displayEditorColorFollowsA(w, e) {
			return
		}
		next := displayEditorRangePolicy(w)
		displayEditorSetPolicy(w, next)
		if next != displaycore.ManualRange {
			displayRequestMappingStats(w.owner, w.key, displayEditorCompareLinked(w))
		}
		displayLayoutEditor(w)
	case dePercentPreset:
		if code != 1 {
			return
		}
		if n := displaySelection(w.controls[id]); n >= 0 && n < 5 {
			lo, hi, _ := displaycore.CentralPercentiles([...]float64{90, 95, 98, 99, 100}[n])
			w.loading = true
			displayField(w, dePercentLow, displaycore.FormatExactNumber(lo))
			displayField(w, dePercentHigh, displaycore.FormatExactNumber(hi))
			w.loading = false
		}
		displayLayoutEditor(w)
	case dePercentLow, dePercentHigh:
		if code != 0x0300 && code != 0x0200 {
			return
		}
		pSendMessageW.Call(w.controls[dePercentPreset], CB_SETCURSEL, 5, 0)
	case dePositionLow, dePositionHigh:
		return // Values become a range only on the explicit button.
	case deApplyPosition:
		reference, ok := displayEditorPositionReference(w)
		lo, loErr := displayParseNumber(getText(w.controls[dePositionLow]), false)
		hi, hiErr := displayParseNumber(getText(w.controls[dePositionHigh]), false)
		if !ok || loErr != nil || hiErr != nil {
			w.stateError = "请输入有效的跨度位置"
			return
		}
		r, err := displaycore.RangeFromPositions(reference, lo, hi)
		if err != nil {
			w.stateError = err.Error()
			return
		}
		displayEditorSetRangeDraft(w, r, displaycore.DragWhole)
		displayLayoutEditor(w)
	case deMin, deMax:
		if code != 0x0300 && code != 0x0200 {
			return
		}
		if id == deMax {
			w.exactBounds.last = 1
		} else {
			w.exactBounds.last = 0
		}
		if w.pageColor && !displayEditorColorFollowsA(w, e) {
			if getText(w.controls[id]) != w.exactBounds.shown[w.exactBounds.last] && displayEditorRangePolicy(w) != displaycore.ManualRange {
				displayEditorSetPolicy(w, displaycore.ManualRange)
			}
			if code == 0x0200 && displayChecked(w.controls[deSym]) {
				if r, dirty, err := displayEditorTypedBounds(w, nil); err == nil && dirty {
					displayEditorShowBounds(w, r, true)
				}
			}
			if code == 0x0200 && w.section == displayPageRange && displayEditorRangePolicy(w) != w.rangeLayoutPolicy {
				// The field being edited stays at the same position while
				// typing. Reflow strategy-specific rows after focus leaves.
				displayLayoutEditor(w)
			}
		} else if !w.pageColor {
			if _, view, ok := displayEditorRange(w); ok {
				displayEditorSyncAxisPositions(w, view)
			}
		}
	case deRangePage, deGradientPage, deTickPage, deAppearancePage:
		w.section = id - deRangePage
		displayLayoutEditor(w)
		displayEditorRefreshTicks(w)
		return
	case deDistancePage:
		if displayEditorHasDistance(w, e) {
			w.section = 4
			displayEditorRefreshDistance(w)
			displayLayoutEditor(w)
		}
		return
	case deDistanceMode, deDistanceCalibration, deDistanceSpacing:
		if !displayEditorHasDistance(w, e) {
			return
		}
		if id == deDistanceSpacing {
			if code != 0x0300 { // EN_CHANGE
				return
			}
			w.distanceSpacingText = getText(w.controls[deDistanceSpacing])
		} else {
			if code != 1 { // CBN_SELCHANGE
				return
			}
			w.distanceDraft.Enabled = displaySelection(w.controls[deDistanceMode]) == 1
			w.distanceDraft.Manual = displaySelection(w.controls[deDistanceCalibration]) == 1
		}
		displayEditorRefreshDistance(w)
		if id != deDistanceSpacing {
			displayLayoutEditor(w)
		}
		return
	case deApply, deOK:
		displayApplyEditor(w, id == deOK)
	case deColor, deAxis:
		if w.pageColor == (id == deColor) {
			return
		}
		displayStoreDraft(w)
		w.pageColor = id == deColor
		displayLoadDraft(w)
	case deAxisSelect:
		if code != 1 {
			return
		}
		n := displaySelection(w.controls[deAxisSelect]) // CBN_SELCHANGE
		if n >= 0 && n < len(e.s.Axes) {
			displayStoreDraft(w)
			w.axis = e.s.Axes[n].ID
			displayLoadDraft(w)
		}
	case dePreset:
		if code != 1 {
			return
		}
		n := displaySelection(w.controls[dePreset]) // CBN_SELCHANGE
		if n >= 1 && n <= 4 {
			w.stops = displayPreset(n - 1)
		}
		if n >= 5 && n-5 < len(legacyAnchors) {
			lut := remainingLegacyPalette(n - 5)
			w.stops = displaycore.LegacyStops(func(i int) displaycore.RGB {
				c := lut[i]
				return displaycore.RGB{R: c.r, G: c.g, B: c.b}
			})
		}
		displayEditorBindGradient(w)
		displayEditorRefreshNode(w, true)
	case deReset:
		key := displayDraftKey(w)
		baseline := w.draftBaselines[key]
		delete(w.drafts, displayDraftKey(w))
		// Initialize the active draft from the unmodified adapter defaults.
		saved, inherited := e.saved, e.inheritedAxes
		e.saved, e.inheritedAxes = nil, nil
		displayLoadDraft(w)
		e.saved, e.inheritedAxes = saved, inherited
		w.draftBaselines[key] = baseline
		if displayEditorHasDistance(w, e) {
			w.distanceDraft = displayDistanceSettings{SpacingM: displaycore.DefaultDistanceSpacingM}
			w.distanceSpacingText = displayNum(w.distanceDraft.SpacingM)
			w.loading = true
			displayEditorPresentDistance(w)
			w.loading = false
			displayLayoutEditor(w)
		}
		if !w.pageColor && !w.unit.StyleOnly {
			// A reset is a deliberate request to restore full available bounds.
			displayEditorShowBounds(w, w.unit.Data, true)
		}
		setText(w.status, "默认设置已载入草稿；点击应用提交。")
	case deFit:
		var r displaycore.Range
		if w.pageColor {
			r = displayRangeFor(w.owner, w.key, displayEditorCompareLinked(w))
		} else {
			r = w.unit.Data
		}
		if !r.Valid() {
			setText(w.status, "没有可用于适配的有效范围。")
			return
		}
		displayEditorSetRangeDraft(w, r, displaycore.DragWhole)
		displayLayoutEditor(w)
	case deUnit:
		const enKillFocus = 0x0200
		if w.pageColor || code != enKillFocus {
			return
		}
		displayEditorChangeUnit(w)
		displayEditorSummary(w)
	case deTitle:
		if code == 0x0300 { // EN_CHANGE; retain the full text in the tooltip.
			displayEditorUpdateTitleTip(w, e)
		}
	case deCompareMode:
		if code != 1 {
			return
		} // CBN_SELCHANGE
		b := e.s.Compare
		if b == nil || !b.CanLink {
			return
		}
		w.compareIndependent = displaySelection(w.controls[deCompareMode]) == 1
		w.compareModeDirty = true
		if !w.compareIndependent {
			for _, k := range b.Keys[:2] {
				if p := displayOwners[w.owner].surfaces[k]; p != nil {
					displayRequestStats(w.owner, k, p)
				}
			}
		}
		displayLayoutEditor(w)
	case deCompareMatchA:
		available, _ := displayEditorPoststackFollow(w, e)
		if !available {
			return
		}
		if w.compareFollowDraft == nil {
			w.compareFollowDraft = make(map[displayEditorFollowKey]bool)
		}
		w.compareFollowDraft[displayEditorFollowKeyFor(w)] = displayEditorPoststackLinked(w)
		if w.pageColor && !displayEditorPoststackLinked(w) {
			if a, _ := displayCommitted(w.owner, e.s.Compare.Keys[0]); a.Color != nil {
				displayCheck(w.controls[deSym], a.Color.Symmetric)
				displayCheck(w.controls[deReverse], a.Color.Reverse)
				w.stops = append([]displaycore.GradientStop(nil), a.Color.Stops...)
				displayEditorSetRangeDraft(w, a.Color.Range, displaycore.DragWhole)
			}
		}
		displayEditorPresentBColorLink(w, e)
		displayLayoutEditor(w)
	case deComparePanel:
		if code != 1 || e.s.Compare == nil {
			return
		} // CBN_SELCHANGE
		n := displaySelection(w.controls[deComparePanel])
		if n >= 0 && n < len(w.compareTargets) {
			key := e.s.Compare.Keys[w.compareTargets[n]]
			if key != w.key {
				displayOpenEditor(w.owner, key, w.axis, w.pageColor)
			}
		}
	case deAuto, deSym:
		displayLayoutEditor(w)
	case deTickModeScientific:
		if code != 1 {
			return
		}
		displayCheck(w.controls[deTickAuto], displaySelection(w.controls[deTickModeScientific]) == 0)
		displayLayoutEditor(w)
	case deTickAuto:
		if !displayChecked(w.controls[deTickAuto]) {
			// The disabled field continuously exposes the effective auto step.
			if step, err := displayParseNumber(getText(w.controls[deStep]), false); err != nil || step <= 0 {
				displayCheck(w.controls[deTickAuto], true)
				setText(w.status, "当前范围尚无有效自动间隔。")
			}
		}
		displayLayoutEditor(w)
	case deFormat:
		if code != 1 {
			return
		}
		displayLayoutEditor(w)
	case deTickDivisions:
		if code != 1 && code != 5 { // CBN_SELCHANGE, CBN_EDITCHANGE
			return
		}
		if code == 1 {
			presets := [...]int{4, 5, 6, 8, 10}
			if n := displaySelection(w.controls[deTickDivisions]); n >= 0 && n < len(presets) {
				w.loading = true
				displayField(w, deTickDivisions, strconv.Itoa(presets[n]))
				w.loading = false
			}
		}
	}
	displayEditorRefreshTicks(w)
	if !w.pageColor {
		displayEditorSummary(w)
	}
	pInvalidateRect.Call(w.body, 0, 0)
}

func displayHistogramRange(m displaycore.ColorMapping, d displaycore.RangeDistribution) displaycore.Range {
	r := m.Range
	if d.Count > 0 {
		r.Min = math.Min(r.Min, d.Range.Min)
		r.Max = math.Max(r.Max, d.Range.Max)
	}
	return r.Expanded()
}

func displayEditorColorRangeLocked(w *displayEditor) bool {
	e := displayEditorSurface(w)
	return e == nil || e.s.Color == nil || displayEditorColorFollowsA(w, e)
}

func displayEditorColorHistogram(w *displayEditor, e *displaySurfaceState, m, base displaycore.ColorMapping) (displaycore.Range, displaycore.RangeDistribution) {
	key := w.key
	if displayEditorColorFollowsA(w, e) {
		key = e.s.Compare.Keys[0]
	}
	d, ready, err := displayMappingDistribution(w.owner, key, displayEditorCompareLinked(w))
	if !ready || err != nil {
		d = displaycore.RangeDistribution{}
	}
	if w.section == 0 {
		if w.rangeDragging && w.rangeDragColor {
			return w.rangeDragBounds, d
		}
		// Keep the previously committed limits reachable even before statistics
		// arrive, or if the user has narrowed the draft by typing or dragging.
		m.Range.Min = math.Min(m.Range.Min, base.Range.Min)
		m.Range.Max = math.Max(m.Range.Max, base.Range.Max)
		if all := d.Range; d.Count != 0 && all.Valid() {
			m.Range.Min = math.Min(m.Range.Min, all.Min)
			m.Range.Max = math.Max(m.Range.Max, all.Max)
		}
	}
	r := displayHistogramRange(m, d)
	if w.section == 0 && m.Symmetric {
		r = r.Symmetric()
	}
	return r, d
}
func displayEditorPaint(w *displayEditor, dc uintptr) {
	if w.preview.Right <= w.preview.Left || w.preview.Bottom <= w.preview.Top {
		return
	}
	dpi := displayDPI(w.handle)
	px := func(v int) int32 { return int32(displayPixelDIP(float64(v), dpi)) }
	p := w.preview
	saved, _, _ := pSaveDC.Call(dc)
	if saved == 0 {
		return
	}
	defer pRestoreDC.Call(dc, saved)
	pIntersectClipRect.Call(dc, uintptr(p.Left), uintptr(p.Top), uintptr(p.Right), uintptr(p.Bottom))
	displayFill(dc, p, displaycore.RGB{R: 215, G: 223, B: 235})
	card := RECT{Left: p.Left + 1, Top: p.Top + 1, Right: p.Right - 1, Bottom: p.Bottom - 1}
	displayFill(dc, card, displaycore.RGB{R: 252, G: 253, B: 255})
	if w.font != 0 {
		pSelectObject.Call(dc, w.font)
	} else if hFont != 0 {
		pSelectObject.Call(dc, hFont)
	}
	pSetBkMode.Call(dc, TRANSPARENT)
	pSetTextColor.Call(dc, rgbRef(38, 54, 74))
	if !w.pageColor || w.section == displayPageTicks || w.section == displayPageAppearance || w.section == displayPageDistance {
		if !w.pageColor && w.section == displayPageRange {
			displayEditorPaintCoordinateRange(w, dc, w.preview)
			return
		}
		displayEditorPaintStyle(w, dc, card)
		return
	}
	state := displayEditorResolveRangeState(w)
	m, base, e := state.Draft, state.Applied, displayEditorSurface(w)
	if state.Err != nil {
		displayEditorDrawPreviewNotice(dc, card, state.Err.Error())
		return
	}
	if !state.Ready || !m.Range.Valid() || e == nil {
		if state.HasColor {
			displayEditorDrawPreviewNotice(dc, card, "正在准备当前范围的分布预览…")
		}
		return
	}
	rangeValue, d := displayEditorColorHistogram(w, e, m, base)
	gradientPage := w.section == displayPageGradient
	if gradientPage && d.Count > 0 && d.Range.Expanded().Valid() {
		// This histogram describes the full measured distribution,
		// independent of the current mapping or its 0–100% stop editor.
		rangeValue = d.Range.Expanded()
	}
	g := w.graph
	bar := w.gradient
	if g.Right <= g.Left || g.Bottom <= g.Top || bar.Right <= bar.Left {
		displayEditorDrawPreviewNotice(dc, card, "空间不足，放大窗口可查看分布预览。")
		return
	}
	pSetBkMode.Call(dc, TRANSPARENT)
	pSetTextColor.Call(dc, rgbRef(38, 54, 74))
	displayFill(dc, g, displaycore.RGB{R: 244, G: 247, B: 252})
	bins := d.Bins(rangeValue, 64)
	highest := uint64(1)
	for _, n := range bins {
		highest = max(highest, n)
	}
	for i, n := range bins {
		if n > 0 {
			left, right := g.Left+int32(i)*(g.Right-g.Left)/64, g.Left+int32(i+1)*(g.Right-g.Left)/64
			height := int32(float64(n) / float64(highest) * float64(max(1, g.Bottom-g.Top-px(20))))
			displayFill(dc, RECT{Left: left, Right: max(left+1, right-1), Top: g.Bottom - height, Bottom: g.Bottom}, displaycore.RGB{R: 107, G: 149, B: 197})
		}
	}
	n := min(256, int(bar.Right-bar.Left))
	for i := 0; i < n; i++ {
		x0, x1 := bar.Left+int32(i)*(bar.Right-bar.Left)/int32(n), bar.Left+int32(i+1)*(bar.Right-bar.Left)/int32(n)
		frac := (float64(i) + .5) / float64(n)
		color := m.ColorAtPosition(frac)
		if !gradientPage {
			color = m.ColorAt(rangeValue.At(frac))
		}
		displayFill(dc, RECT{Left: x0, Right: x1, Top: bar.Top, Bottom: bar.Bottom}, color)
	}
	if gradientPage {
		label := "数据分布"
		if d.Estimated {
			label += "（抽样）"
		}
		if d.Count == 0 {
			label += "：暂无有效数据"
		} else {
			// Report the true measured extrema, not an artificially
			// expanded plotting interval for constant data.
			label += "  " + displaycore.FormatCompactNumber(d.Range.Min) + " ～ " + displaycore.FormatCompactNumber(d.Range.Max)
		}
		drawAxisText(dc, label, int(g.Left+px(5)), int(g.Top+1), int(g.Right), int(g.Top+px(19)), DT_LEFT)
		drawAxisText(dc, "渐变位置（映射范围内）", int(bar.Left), int(bar.Top-px(21)), int(bar.Right), int(bar.Top-px(3)), DT_LEFT)
		if !displayEditorColorRangeLocked(w) {
			displayEditorPaintGradientNodes(w, dc, rangeValue, m)
		}
		labelY := w.nodeBand.Bottom + px(2)
		drawAxisText(dc, "0%  ·  "+displaycore.FormatCompactNumber(m.Range.Min), int(bar.Left), int(labelY), int(bar.Right), int(labelY+px(18)), DT_LEFT)
		drawAxisText(dc, "100%  ·  "+displaycore.FormatCompactNumber(m.Range.Max), int(bar.Left), int(labelY), int(bar.Right), int(labelY+px(18)), DT_RIGHT)
		return
	}
	pixel := func(v float64) int {
		return displayEditorRangePixel(bar, rangeValue, v)
	}
	line := func(x int, color displaycore.RGB) {
		displayFill(dc, RECT{Left: int32(x - 1), Right: int32(x + 1), Top: g.Top + px(19), Bottom: bar.Bottom}, color)
	}
	if rangeValue.Min <= 0 && rangeValue.Max >= 0 {
		line(pixel(0), displaycore.RGB{R: 90, G: 104, B: 126})
	}
	left, right := pixel(m.Range.Min), pixel(m.Range.Max)
	handle := displaycore.RGB{R: 45, G: 78, B: 130}
	if w.section == displayPageRange && !displayEditorColorRangeLocked(w) {
		handle = displaycore.RGB{R: 33, G: 101, B: 180}
	}
	line(left, handle)
	line(right, handle)
	if w.section == displayPageRange {
		if right > left {
			displayFill(dc, RECT{Left: int32(left), Right: int32(right), Top: bar.Bottom + px(3), Bottom: bar.Bottom + px(6)}, handle)
		}
		for _, x := range []int{left, right} {
			displayFill(dc, RECT{Left: int32(x) - px(4), Right: int32(x) + px(4), Top: bar.Bottom - px(3), Bottom: bar.Bottom + px(9)}, handle)
		}
	}
	label := "分布（频数）"
	if d.Estimated {
		label = "抽样分布（频数）"
	}
	if d.Count == 0 {
		label = "分布：无有效数据或统计中"
	}
	if w.section == displayPageRange {
		label = "范围编辑 · " + label
	} else if w.section == displayPageGradient {
		label = "渐变编辑 · " + label
	}
	drawAxisText(dc, label, int(g.Left+px(5)), int(g.Top+1), int(g.Right), int(g.Top+px(19)), DT_LEFT)
	lo, hi := displaycore.FormatCompactNumber(rangeValue.Min), displaycore.FormatCompactNumber(rangeValue.Max)
	labelY := bar.Bottom + px(14)
	drawAxisText(dc, "显示跨度  "+lo, int(g.Left), int(labelY), int(g.Right), int(labelY+px(18)), DT_LEFT)
	drawAxisText(dc, hi, int(g.Left), int(labelY), int(g.Right), int(labelY+px(18)), DT_RIGHT)
	if w.section == displayPageRange {
		help := "两端缩放；蓝色区间平移。"
		switch {
		case displayEditorColorRangeLocked(w):
			help = "色表和映射范围跟随 A。"
		case m.Symmetric:
			help = "拖动任一端对称调整。"
		case m.EffectiveRangePolicy() != displaycore.ManualRange:
			help = "拖动后转为手动范围。"
		}
		actual := "映射端点  " + displaycore.FormatCompactNumber(m.Range.Min) + " ～ " + displaycore.FormatCompactNumber(m.Range.Max)
		if !w.rangeDragging {
			actual += "  ·  " + help
		}
		drawAxisText(dc, actual, int(g.Left), int(bar.Bottom+px(32)), int(g.Right), int(bar.Bottom+px(52)), DT_LEFT)
	}
}

func displayEditorRange(w *displayEditor) (displaycore.Range, displaycore.Range, bool) {
	if w.pageColor || w.unit.StyleOnly {
		return displaycore.Range{}, displaycore.Range{}, false
	}
	r, _, err := displayEditorTypedBounds(w, &w.unit)
	bounds := w.unit.Data
	return bounds, r, err == nil && bounds.Valid() && w.unit.ValidRange(r) && r.Min >= bounds.Min && r.Max <= bounds.Max
}

// Update the range selector's percentages from canonical axis values, never
// from rounded label/edit strings. Editing a percentage remains prospective
// until the explicit span button applies it to the exact draft.
func displayEditorSyncAxisPositions(w *displayEditor, view displaycore.Range) {
	if w.pageColor {
		return
	}
	data := w.unit.Data
	lo, hi := "0", "100"
	if data.Valid() && w.unit.ValidRange(view) && view.Min >= data.Min && view.Max <= data.Max {
		lo = displaycore.FormatCompactNumber(100 * data.Fraction(view.Min))
		hi = displaycore.FormatCompactNumber(100 * data.Fraction(view.Max))
	}
	loading := w.loading
	w.loading = true
	for id, value := range map[int]string{dePositionLow: lo, dePositionHigh: hi} {
		if getText(w.controls[id]) != value {
			displayField(w, id, value)
		}
	}
	w.loading = loading
}
func displayEditorDragRange(w *displayEditor, x, y int, start bool) bool {
	if start {
		h, ok := displayEditorRangeHit(w, x, y)
		if !ok {
			return false
		}
		d := displayCaptureDraft(w)
		w.gestureDraft, w.gestureNode = &d, w.selection
		w.gestureMoved = false
		w.rangeDragging = true
		w.rangeDragColor, w.rangeDragSymmetric = w.pageColor, h.symmetric
		w.rangeDragStart, w.rangeDragMode, w.rangeDragBar = x, h.mode, h.bar
		w.rangeDragBounds, w.rangeDragView = h.bounds, h.view
		displaySetCapture.Call(w.body)
		return true
	}
	if !w.rangeDragging {
		return false
	}
	b := w.rangeDragBar
	delta := float64(x-w.rangeDragStart) / float64(b.Right-b.Left) * (w.rangeDragBounds.Max - w.rangeDragBounds.Min)
	minimumSpan := min(w.rangeDragView.Max-w.rangeDragView.Min,
		(w.rangeDragBounds.Max-w.rangeDragBounds.Min)/float64(b.Right-b.Left))
	switch w.rangeDragMode {
	case displaycore.DragMinimum:
		delta = min(delta, w.rangeDragView.Max-w.rangeDragView.Min-minimumSpan)
	case displaycore.DragMaximum:
		delta = max(delta, minimumSpan-(w.rangeDragView.Max-w.rangeDragView.Min))
	}
	r := displaycore.DragRange(w.rangeDragBounds, w.rangeDragView, w.rangeDragMode, delta)
	if !w.rangeDragColor && w.unit.AllowsPoint && w.rangeDragView.IsPoint() {
		// Both handles initially coincide. Moving either way expands the
		// corresponding endpoint, without creating a fictional data cell.
		v := w.rangeDragView.Min
		target := max(w.rangeDragBounds.Min, min(w.rangeDragBounds.Max, v+float64(x-w.rangeDragStart)/float64(b.Right-b.Left)*(w.rangeDragBounds.Max-w.rangeDragBounds.Min)))
		r = displaycore.Range{Min: min(v, target), Max: max(v, target)}
	}
	if w.rangeDragColor && w.rangeDragSymmetric {
		floor := min(w.rangeDragView.Max, minimumSpan/2)
		v := w.rangeDragView.Max
		if w.rangeDragMode == displaycore.DragMinimum {
			v -= delta
		} else {
			v += delta
		}
		v = max(floor, min(w.rangeDragBounds.Max, v))
		r = displaycore.Range{Min: -v, Max: v}
	}
	if x == w.rangeDragStart && !w.gestureMoved {
		return true
	}
	w.gestureMoved = true
	displayEditorSetRangeDraft(w, r, w.rangeDragMode)
	displayEditorRefreshTicks(w)
	displayInvalidatePreview(w)
	return true
}

func displayEditorDrag(w *displayEditor, x, y int, start bool) bool {
	if start {
		return displayEditorGradientClick(w, x, y, false)
	}
	if !w.dragging || !w.pageColor || w.section != displayPageGradient || displayEditorColorRangeLocked(w) || w.gradient.Right <= w.gradient.Left {
		return false
	}
	// Mouse down selects the point. A tiny motion should not accidentally
	// move the point or clear its edited value just before double-click.
	if !w.gestureMoved && absInt(x-w.gradientDragX) < max(1, displayPixelDIP(2, displayDPI(w.handle))) {
		return true
	}
	w.gestureMoved = true
	if displayEditorMoveGradientNode(w, displayGradientFractionAt(w.gradient, x), true) {
		displayEditorRefreshNode(w, true)
		displayEditorRefreshFooter(w)
		displayInvalidatePreview(w)
	}
	return true
}

func displayInvalidatePreview(w *displayEditor) {
	client := clientRect(w.body)
	r := w.preview
	r.Left, r.Top = max(client.Left, r.Left), max(client.Top, r.Top)
	r.Right, r.Bottom = min(client.Right, r.Right), min(client.Bottom, r.Bottom)
	if r.Right > r.Left && r.Bottom > r.Top {
		pInvalidateRect.Call(w.body, uintptr(unsafe.Pointer(&r)), 0)
	}
}

func displayPreviewDirty(graph, gradient, client RECT, dpi int) RECT {
	if graph.Right <= graph.Left || gradient.Right <= gradient.Left {
		return RECT{}
	}
	gutter := int32(max(8, 12*dpi/96))
	// The lower region includes the control point handles and range labels.
	// Clearing only the gradient leaves trails when a point is dragged.
	return RECT{
		Left:   max(client.Left, min(graph.Left, gradient.Left)-gutter),
		Top:    max(client.Top, min(graph.Top, gradient.Top)),
		Right:  min(client.Right, max(graph.Right, gradient.Right)+gutter),
		Bottom: min(client.Bottom, max(graph.Bottom, gradient.Bottom+int32(max(32, 34*dpi/96)))),
	}
}

var displayPreviewOrigin = gdi32.NewProc("SetViewportOrgEx")

func displayEditorPaintBody(w *displayEditor, h uintptr) {
	var ps PAINTSTRUCT
	dc, _, _ := pBeginPaint.Call(h, uintptr(unsafe.Pointer(&ps)))
	defer pEndPaint.Call(h, uintptr(unsafe.Pointer(&ps)))
	dirty := ps.RcPaint
	width, height := dirty.Right-dirty.Left, dirty.Bottom-dirty.Top
	if dc == 0 || width <= 0 || height <= 0 {
		return
	}
	brush, _, _ := algorithmSysColorBrush.Call(COLOR_BTNFACE)
	draw := func(dst uintptr) {
		pFillRect.Call(dst, uintptr(unsafe.Pointer(&dirty)), brush)
		displayEditorPaint(w, dst)
	}
	// The paint DC clips native child windows. Build the background and plot
	// in a DC covering the dirty rectangle, then publish them in one step.
	mem, _, _ := pCreateCompatibleDC.Call(dc)
	if mem != 0 {
		defer pDeleteDC.Call(mem)
		bmp, _, _ := pCreateCompatibleBitmap.Call(dc, uintptr(width), uintptr(height))
		if bmp != 0 {
			defer pDeleteObject.Call(bmp)
			old, _, _ := pSelectObject.Call(mem, bmp)
			if old != 0 && old != ^uintptr(0) {
				defer pSelectObject.Call(mem, old)
				if ok, _, _ := displayPreviewOrigin.Call(mem, uintptr(-dirty.Left), uintptr(-dirty.Top), 0); ok != 0 {
					draw(mem)
					displayPreviewOrigin.Call(mem, 0, 0, 0)
					if copied, _, _ := pBitBlt.Call(dc, uintptr(dirty.Left), uintptr(dirty.Top), uintptr(width), uintptr(height), mem, 0, 0, SRCCOPY); copied != 0 {
						return
					}
				}
			}
		}
	}
	draw(dc)
}

func displayEditorBodyProc(h uintptr, msg uint32, wp, lp uintptr) uintptr {
	w := displayBodies[h]
	if w != nil {
		switch msg {
		case WM_NOTIFY:
			if displayEditorTabNotify(w, lp) {
				return 0
			}
		case WM_COMMAND:
			displayEditorCommand(w, int(wp&65535), int(wp>>16&65535))
			return 0
		case algorithmWMDrawItem:
			if int(wp) == deNodeSwatch && lp != 0 {
				displayEditorDrawGradientSwatch(w, (*algorithmDrawItem)(unsafe.Pointer(lp)))
				return 1
			}
		case WM_ERASEBKGND:
			return 1 // WM_PAINT draws the complete dirty backdrop offscreen.
		case WM_PAINT:
			displayEditorPaintBody(w, h)
			return 0
		case 0x0115, 0x020a: // Settings are tabbed, never scroll hidden content.
			return 0
		case WM_SETCURSOR:
			if wp == h && lp&65535 == 1 { // HTCLIENT
				var cursor POINT
				if ok, _, _ := pGetCursorPos.Call(uintptr(unsafe.Pointer(&cursor))); ok != 0 {
					pScreenToClient.Call(h, uintptr(unsafe.Pointer(&cursor)))
					if c, _, _ := pLoadCursorW.Call(0, displayEditorCursor(w, int(cursor.X), int(cursor.Y))); c != 0 {
						pSetCursor.Call(c)
						return 1
					}
				}
			}
		case WM_LBUTTONDOWN:
			if displayEditorDragRange(w, int(int16(lp)), int(int16(lp>>16)), true) {
				return 0
			}
			if displayEditorDrag(w, int(int16(lp)), int(int16(lp>>16)), true) {
				return 0
			}
		case WM_LBUTTONDBLCLK:
			if displayEditorGradientClick(w, int(int16(lp)), int(int16(lp>>16)), true) {
				return 0
			}
		case WM_KEYDOWN:
			if wp == VK_DELETE {
				if focus, _, _ := algorithmGetFocus.Call(); displayEditorGradientDeleteKey(w, focus) {
					return 0
				}
			}
		case WM_MOUSEMOVE:
			if w.rangeDragging {
				displayEditorDragRange(w, int(int16(lp)), int(int16(lp>>16)), false)
				return 0
			}
			if w.dragging {
				displayEditorDrag(w, int(int16(lp)), int(int16(lp>>16)), false)
				return 0
			}
			if w.pageColor && w.section == displayPageGradient {
				displayEditorTrackGradientHover(w, int(int16(lp)), int(int16(lp>>16)))
			}
		case 0x02a3: // WM_MOUSELEAVE
			displayEditorClearGradientHover(w)
		case WM_LBUTTONUP, 0x0215, 0x001f: // WM_CAPTURECHANGED / WM_CANCELMODE
			if displayEditorFinishGesture(w, false, msg != 0x0215) {
				return 0
			}
		case WM_DESTROY:
			delete(displayBodies, h)
		}
	}
	ret, _, _ := pDefWindowProcW.Call(h, uintptr(msg), wp, lp)
	return ret
}
func displayEditorProc(h uintptr, msg uint32, wp, lp uintptr) uintptr {
	if msg == displayStatsMessage {
		displayReceiveStats()
		return 0
	}
	if w := displayEditors[h]; w != nil {
		switch msg {
		case displayRefreshEditorLiveMessage:
			w.viewportRefreshPending = false
			displayEditorRefreshViewport(w)
			return 0
		case displayRefreshComparePickerMessage:
			displayRefreshComparePicker(w)
			return 0
		case WM_NOTIFY:
			if displayEditorTabNotify(w, lp) {
				return 0
			}
		case WM_COMMAND:
			displayEditorCommand(w, int(wp&65535), int(wp>>16&65535))
			return 0
		case WM_SIZE:
			if wp == 1 { // SIZE_MINIMIZED
				return 0
			}
			displayEditorFinishGesture(w, false, true)
			displayEditorRefreshDistance(w)
			displayLayoutEditor(w)
			displayEditorRefreshTicks(w)
			return 0
		case 0x0024: // WM_GETMINMAXINFO
			if lp != 0 {
				i := (*MINMAXINFO)(unsafe.Pointer(lp))
				dpi := displayDPI(h)
				displayEditorFont(w, dpi)
				measured, closeMetrics := displayEditorMeasureControls(w, dpi)
				cw, ch := measured.minimumClient()
				closeMetrics()
				width, height := displayEditorOuterSize(dpi, cw, ch)
				work := displayEditorWorkArea(h, nil)
				if work.Right > work.Left && work.Bottom > work.Top {
					width = min(width, int(work.Right-work.Left))
					height = min(height, int(work.Bottom-work.Top))
				}
				i.PtMinTrackSize = POINT{X: int32(width), Y: int32(height)}
			}
			return 0
		case 0x02E0:
			displayEditorFinishGesture(w, false, true)
			if lp != 0 {
				r := (*RECT)(unsafe.Pointer(lp))
				box := displayEditorFitWorkArea(*r, displayEditorWorkArea(h, r))
				pSetWindowPos.Call(h, 0, uintptr(box.Left), uintptr(box.Top), uintptr(box.Right-box.Left), uintptr(box.Bottom-box.Top), 0x14)
			}
			displayEditorRefreshDistance(w)
			dpi := int(wp & 0xffff)
			if dpi <= 0 {
				dpi = displayDPI(h)
			}
			displayLayoutEditorAtDPI(w, dpi)
			displayEditorRefreshTicks(w)
			return 0
		case 0x020a:
			return 0
		case 0x0006: // WM_ACTIVATE
			if wp&65535 == 0 {
				displayEditorFinishGesture(w, false, true)
			}
		case WM_CLOSE:
			displayDestroyEditor(w)
			return 0
		case WM_DESTROY:
			w.summaryTip.close()
			if w.font != 0 {
				pDeleteObject.Call(w.font)
				w.font = 0
			}
			delete(displayEditors, h)
			delete(displayBodies, w.body)
			if o := displayOwners[w.owner]; o != nil && o.editor == w {
				o.editor = nil
			}
			return 0
		}
	}
	ret, _, _ := pDefWindowProcW.Call(h, uintptr(msg), wp, lp)
	return ret
}

func translateDisplayEditorMessage(m *MSG) bool {
	if m == nil || m.Message != WM_KEYDOWN || (m.WParam != VK_ESCAPE && m.WParam != 0x09) {
		return false
	}
	for h := range displayEditors {
		child, _, _ := pIsChild.Call(h, m.Hwnd)
		if m.Hwnd != h && child == 0 {
			continue
		}
		if m.WParam == VK_ESCAPE {
			if w := displayEditors[h]; w != nil && displayEditorFinishGesture(w, true, true) {
				return true
			}
			pPostMessageW.Call(h, WM_CLOSE, 0, 0)
			return true
		}
		handled, _, _ := algorithmIsDialogMessage.Call(h, uintptr(unsafe.Pointer(m)))
		return handled != 0
	}
	return false
}
