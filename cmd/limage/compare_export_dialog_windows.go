//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"unsafe"
)

const (
	numericalExportID = 27120 + iota
	numericalExportObjectA
	numericalExportObjectB
	numericalExportObjectDelta
	numericalExportScope
	numericalExportStage
	numericalExportManualFirst
	numericalExportManualLast
	numericalExportManualT0
	numericalExportManualT1
	numericalExportPath
	numericalExportBrowse
	numericalExportWrite
	numericalExportCancel
	numericalExportFolder
	numericalExportReload
	numericalExportAutoName
	numericalExportDetails
)

const (
	numericalExportPreviewMessage = WM_USER + 230
	numericalExportTimer          = 0x534E
)

type numericalExportDialog struct {
	h, owner                    uintptr
	capture                     compareExportCapture
	object                      [3]uintptr
	scope, stage, path, preview uintptr
	head, availability, status  uintptr
	browse, write, cancel       uintptr
	folder, reload              uintptr
	scopeLabel, stageLabel      uintptr
	pathLabel                   uintptr
	spaceLabel, timeLabel       uintptr
	fieldStatus, nameMode       uintptr
	autoName, details           uintptr
	manualLabel, manualEdit     [4]uintptr
	manualDraft                 [4]string
	manualDraftReady            bool
	scopeOptions                []int
	selection                   int
	initializing, changedPath   bool
	closed, stale, writing      bool
	defaultFolder               string
	entryResultPath             string
	lastPath                    string
	errorDetail                 string
	captureError                error
	plan                        *compareExportPlan
	planReady                   bool
	previewCancel               context.CancelFunc
	writeCancel                 context.CancelFunc
	previewSerial               uint64
	writeGeneration             int64
	previews                    chan numericalExportPreview
	writes                      chan compareExportResult
}

type numericalExportPreview struct {
	serial uint64
	plan   compareExportPlan
	err    error
}

var (
	numericalExportWindow      *numericalExportDialog
	numericalExportClass       bool
	numericalExportCallback    = syscall.NewCallback(numericalExportWndProc)
	numericalExportActivePanel int
)

func startExportCurrentSection() {
	startCompareExportDialog(nil)
}

func startExportGeneratedSection(result *generatedPoststackResult) {
	if result == nil || result.Section == nil || result.retired.Load() {
		return
	}
	startCompareExportDialog(result)
}

func startCompareExportDialog(result *generatedPoststackResult) {
	cc.toolbar.popTooltip()
	var resultPath string
	if result != nil {
		resultPath = compareBPath
	}
	if d := numericalExportWindow; d != nil {
		d.refreshContext()
		if d.writing || (!d.stale && d.captureError == nil && (resultPath == "" || resultPath == d.entryResultPath)) {
			showOwnedSettingsWindow(d.h)
			return
		}
		pDestroyWindow.Call(d.h)
	}
	c, err := captureNumericalCompareExport()
	if compareHwnd == 0 {
		return
	}
	if result != nil && err == nil && !result.ownsBProduct(c.sources[1].path) {
		err = errors.New("选中的处理结果尚未成为当前 B；请等待 B 就绪后再导出")
	}
	const class = "Limage64PoststackNumericExport"
	hi, _, _ := pGetModuleHandleW.Call(0)
	if !numericalExportClass {
		cursor, _, _ := pLoadCursorW.Call(0, IDC_ARROW)
		registerClass(class, numericalExportCallback, COLOR_BTNFACE+1, hi, cursor)
		numericalExportClass = true
	}
	area, ratio := algorithmMonitorArea(compareHwnd), displayDPI(compareHwnd)
	w := min(int(area.Right-area.Left), displayPixelDIP(680, ratio))
	h := min(int(area.Bottom-area.Top), displayPixelDIP(452, ratio))
	x := int(area.Left) + (int(area.Right-area.Left)-w)/2
	y := int(area.Top) + (int(area.Bottom-area.Top)-h)/2
	window, _, _ := pCreateWindowExW.Call(settingsToolWindowStyle, uintptr(unsafe.Pointer(u16(class))),
		uintptr(unsafe.Pointer(u16("导出数据 — SeisForge Studio"))), WS_CAPTION|WS_SYSMENU|WS_CLIPCHILDREN|0x00040000, // WS_THICKFRAME
		uintptr(x), uintptr(y), uintptr(w), uintptr(h), compareHwnd, 0, hi, 0)
	if window == 0 {
		setText(cc.status, "无法打开导出数据窗口")
		return
	}
	source := c.sources[0].path
	if source == "" {
		source = c.key.a
	}
	d := &numericalExportDialog{h: window, owner: compareHwnd, capture: c, captureError: err, initializing: true,
		scopeOptions: []int{compareExportVisible, compareExportLine, compareExportAll}, defaultFolder: numericalExportDirectory(source),
		entryResultPath: resultPath,
		previews:        make(chan numericalExportPreview, 1), writes: make(chan compareExportResult, 1)}
	numericalExportWindow = d
	if c.rectangle != nil {
		d.scopeOptions = append(d.scopeOptions, compareExportRectangle)
	}
	d.scopeOptions = append(d.scopeOptions, compareExportManual)
	d.createControls(result != nil)
	d.initializing = false
	d.refreshPlan()
	pSetTimer.Call(window, numericalExportTimer, 140, 0)
	showOwnedSettingsWindow(window)
}

func (d *numericalExportDialog) createControls(generated bool) {
	mk := func(class, title string, style uint32, id int) uintptr {
		return createCtrl(d.h, class, title, WS_CHILD|WS_VISIBLE|style, 0, 0, 1, 1, id)
	}
	button := func(text string, id int) uintptr { return mk("BUTTON", text, WS_TABSTOP|BS_PUSHBUTTON, id) }
	edit := func(text string, id int) uintptr { return mk("EDIT", text, WS_TABSTOP|WS_BORDER|ES_AUTOHSCROLL, id) }
	label := func(text string) uintptr { return mk("STATIC", text, 0, 0) }
	combo := func(id int, items []string) uintptr {
		h := mk("COMBOBOX", "", WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, id)
		for _, t := range items {
			pSendMessageW.Call(h, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(t))))
		}
		pSendMessageW.Call(h, CB_SETCURSEL, 0, 0)
		return h
	}
	d.head = label("SEG-Y 数值数据")
	d.availability = label("")
	d.status = label("")
	d.scopeLabel, d.stageLabel, d.pathLabel = label("范围"), label("数值阶段"), label("输出文件")
	spatial := d.capture.axisUnits
	if spatial == "" {
		spatial = "空间"
	}
	d.spaceLabel, d.timeLabel = label(spatial), label("时间（ms）")
	d.fieldStatus = label("")
	d.nameMode = label("自动命名")
	available := d.available()
	d.selection = min(2, max(0, numericalExportActivePanel))
	if generated && available[1] {
		d.selection = compareExportB
	}
	if !available[d.selection] {
		d.selection = 0
	}
	for i, name := range compareExportNames {
		d.object[i] = mk("BUTTON", name, WS_TABSTOP|0x0004, numericalExportObjectA+i) // BS_RADIOBUTTON; handles explicit selection
		pEnableWindow.Call(d.object[i], boolToFlag(available[i]))
	}
	d.markSelection()
	d.updateAvailability()
	var labels []string
	for _, scope := range d.scopeOptions {
		labels = append(labels, compareExportScopeNames[scope])
	}
	d.scope = combo(numericalExportScope, labels)
	d.stage = combo(numericalExportStage, []string{"当前有效结果", "基础数据（不含视图滤波）"})
	if generated {
		pSendMessageW.Call(d.scope, CB_SETCURSEL, compareExportAll, 0)
		pSendMessageW.Call(d.stage, CB_SETCURSEL, 1, 0)
	}
	manualLabel := [4]string{"起", "止", "起", "止"}
	for i := range d.manualEdit {
		d.manualLabel[i] = label(manualLabel[i])
		d.manualEdit[i] = edit("—", numericalExportManualFirst+i)
	}
	d.preview = mk("EDIT", "", WS_BORDER|WS_TABSTOP|WS_VSCROLL|ES_READONLY|traceESMultiline|traceESAutoVScroll, 0)
	d.path = edit("", numericalExportPath)
	d.browse, d.write, d.cancel = button("浏览保存位置", numericalExportBrowse), button("导出 SEG-Y", numericalExportWrite), button("关闭", numericalExportCancel)
	d.folder, d.reload = button("打开位置", numericalExportFolder), button("加载为 B", numericalExportReload)
	d.autoName, d.details = button("恢复自动命名", numericalExportAutoName), button("详情", numericalExportDetails)
	pEnableWindow.Call(d.folder, 0)
	pEnableWindow.Call(d.reload, 0)
	pEnableWindow.Call(d.autoName, 0)
	pEnableWindow.Call(d.details, 0)
	d.setRangeFields()
	d.layout()
}

func (d *numericalExportDialog) available() [3]bool {
	a, b := d.capture.sources[0], d.capture.sources[1]
	return [3]bool{d.captureError == nil, d.captureError == nil && b.path != "", d.captureError == nil && b.path != "" && d.capture.hasGeometry && a.dt > 0 && a.dt == b.dt}
}

func (d *numericalExportDialog) updateAvailability() {
	available := d.available()
	for i, enabled := range available {
		pEnableWindow.Call(d.object[i], boolToFlag(enabled && !d.writing))
	}
	note := "输出保留真实道号和采样位置；不包含显示增益或绘图抽稀。"
	if d.captureError != nil {
		note = "当前没有可导出的时间剖面。"
	} else if !available[1] {
		note = "B 和 A−B 尚不可用：请先加载 B 并等待几何就绪。"
	} else if !available[2] {
		if d.capture.sources[0].dt != d.capture.sources[1].dt {
			note = "A−B 不可用：A/B 采样间隔不同。"
		} else {
			note = "A−B 不可用：两侧没有可核实的共同几何。"
		}
	}
	setText(d.availability, note)
}

func (d *numericalExportDialog) layout() {
	w, h := clientSize(d.h)
	dpi := displayDPI(d.h)
	scale := func(n float64) int { return displayPixelDIP(n, dpi) }
	move := func(hwnd uintptr, x, y, width, height int) {
		pMoveWindow.Call(hwnd, uintptr(x), uintptr(y), uintptr(max(1, width)), uintptr(max(1, height)), 1)
	}
	m, gap, row, step := scale(13), scale(7), scale(25), scale(30)
	inner := max(1, w-2*m)
	move(d.head, m, m, inner, row)
	measure, release := poststackToolbarFontMeasure(hFont)
	g := numericalExportRangeLayoutMeasured(w, dpi, getText(d.spaceLabel), measure)
	release()
	box := func(handle uintptr, r RECT, drop bool) {
		height := int(r.Bottom - r.Top)
		if drop {
			height = scale(140) // Win32 drop-list height; the closed item is one row.
		}
		move(handle, int(r.Left), int(r.Top), int(r.Right-r.Left), height)
	}
	for i, ctl := range d.object {
		box(ctl, g.objects[i], false)
	}
	box(d.availability, g.availability, false)
	box(d.scopeLabel, g.scopeLabel, false)
	box(d.scope, g.scope, true)
	box(d.stageLabel, g.stageLabel, false)
	box(d.stage, g.stage, true)
	box(d.spaceLabel, g.spaceLabel, false)
	box(d.timeLabel, g.timeLabel, false)
	for i := range d.manualEdit {
		box(d.manualLabel[i], g.labels[i], false)
		box(d.manualEdit[i], g.edits[i], false)
	}
	box(d.fieldStatus, g.fieldStatus, false)
	foot := h - m - row
	pathY := foot - 3*step
	move(d.preview, m, g.previewTop, inner, max(1, pathY-g.previewTop-gap))
	move(d.pathLabel, m, pathY+scale(3), scale(62), row)
	move(d.path, m+scale(62), pathY, inner-scale(172), row)
	move(d.browse, w-m-scale(104), pathY, scale(104), row)
	move(d.nameMode, m, pathY+step+scale(3), scale(145), row)
	move(d.autoName, m+scale(146), pathY+step, scale(112), row)
	move(d.details, w-m-scale(70), pathY+step, scale(70), row)
	move(d.status, m, pathY+2*step, inner, step)
	move(d.folder, m, foot, scale(76), row)
	move(d.reload, m+scale(81), foot, scale(76), row)
	move(d.cancel, w-m-scale(69), foot, scale(69), row)
	move(d.write, w-m-scale(172), foot, scale(97), row)
}

type numericalExportRangeRects struct {
	objects                                                                                [3]RECT
	availability, scopeLabel, scope, stageLabel, stage, spaceLabel, timeLabel, fieldStatus RECT
	labels, edits                                                                          [4]RECT
	previewTop                                                                             int
}

// Geometry is independent of scope and values: "manual" changes editability
// in place. Both groups share the row below the scope selector whenever the
// measured labels leave enough room for the eight range controls.
func numericalExportRangeLayout(width, dpi int) numericalExportRangeRects {
	return numericalExportRangeLayoutMeasured(width, dpi, "Crossline", nil)
}

func numericalExportRangeLayoutMeasured(width, dpi int, spatialAxis string, measure func(string) int) numericalExportRangeRects {
	px := func(v float64) int { return displayPixelDIP(v, dpi) }
	m, gap, row, step := px(13), px(7), px(25), px(30)
	inner := max(1, width-2*m)
	labelWidth := func(text string, minimum float64) int {
		if measure != nil {
			return max(px(minimum), measure(text)+px(3))
		}
		return px(minimum)
	}
	rect := func(x, y, w, h int) RECT {
		return RECT{Left: int32(x), Top: int32(y), Right: int32(x + max(1, w)), Bottom: int32(y + h)}
	}
	var r numericalExportRangeRects
	y := m + step
	for i := range r.objects {
		r.objects[i] = rect(m+i*px(61), y, px(57), row)
	}
	stageX, stageW := m+inner/2, inner-inner/2
	stageLabel := labelWidth("数值阶段", 68)
	r.stageLabel = rect(stageX, y+px(3), stageLabel, row-px(3))
	r.stage = rect(stageX+stageLabel, y, stageW-stageLabel, row)
	y += step
	r.availability = rect(m, y, inner, row)
	y += step
	half := (inner - gap) / 2
	spaceHead, timeHead := labelWidth(spatialAxis, 60), labelWidth("时间（ms）", 60)
	head := max(spaceHead, timeHead)
	short := max(labelWidth("起", 15), labelWidth("止", 15))
	narrow := half < head+2*(short+gap+px(42))
	scopeW := half
	if narrow {
		scopeW = inner
	}
	scopeLabel := labelWidth("范围", 66)
	r.scopeLabel = rect(m, y+px(3), scopeLabel, row-px(3))
	r.scope = rect(m+scopeLabel, y, scopeW-scopeLabel, row)
	group := func(x, gy, groupW, i, head int) RECT {
		field := (groupW - head - 2*short - 2*gap) / 2
		labelX := x + head
		for j := 0; j < 2; j++ {
			r.labels[i+j] = rect(labelX, gy+px(3), short, row-px(3))
			r.edits[i+j] = rect(labelX+short, gy, field, row)
			labelX += short + field + gap
		}
		return rect(x, gy+px(3), head, row-px(3))
	}
	if narrow {
		r.spaceLabel = group(m, y+step, inner, 0, spaceHead)
		r.timeLabel = group(m, y+2*step, inner, 2, timeHead)
		y += 3 * step
	} else {
		rangeY := y + step
		r.spaceLabel = group(m, rangeY, half, 0, head)
		r.timeLabel = group(m+half+gap, rangeY, inner-half-gap, 2, head)
		y += 2 * step
	}
	r.fieldStatus = rect(m, y, inner, px(20))
	r.previewTop = y + px(24)
	return r
}

func (d *numericalExportDialog) setRangeFields() {
	manual := d.selectedScope() == compareExportManual
	text := [4]string{"—", "—", "—", "—"}
	if d.plan != nil {
		p := d.plan
		text[2], text[3] = exportExact(float64(p.t0)/1000), exportExact(float64(p.t1)/1000)
		if p.mapped > 0 {
			text[0], text[1] = exportExact(float64(p.first)), exportExact(float64(p.last))
		}
	}
	if manual {
		if !d.manualDraftReady {
			manual := d.capture.spatial
			if d.plan == nil {
				text[2], text[3] = exportExact(manual.t0/1000), exportExact(manual.t1/1000)
				if math.IsInf(manual.x0, 0) || math.IsInf(manual.x1, 0) || math.IsNaN(manual.x0) || math.IsNaN(manual.x1) {
					text[0], text[1] = "", ""
				} else {
					text[0], text[1] = exportExact(manual.x0), exportExact(manual.x1)
				}
			}
			d.manualDraft = text
			d.manualDraftReady = true
		}
		text = d.manualDraft
	}
	prior := d.initializing
	d.initializing = true
	defer func() { d.initializing = prior }()
	for i, h := range d.manualEdit {
		pSendMessageW.Call(h, 0x00CF, boolToFlag(!manual), 0) // EM_SETREADONLY
		if getText(h) != text[i] {
			setText(h, text[i])
		}
	}
}

func boolToFlag(v bool) uintptr {
	if v {
		return 1
	}
	return 0
}

func (d *numericalExportDialog) markSelection() {
	for i, h := range d.object {
		pSendMessageW.Call(h, BM_SETCHECK, boolToFlag(i == d.selection), 0)
	}
}

func (d *numericalExportDialog) selectedScope() int {
	r, _, _ := pSendMessageW.Call(d.scope, CB_GETCURSEL, 0, 0)
	if r < uintptr(len(d.scopeOptions)) {
		return d.scopeOptions[r]
	}
	return compareExportVisible
}

func exportExact(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

func (d *numericalExportDialog) currentRequest() (q compareExportSelection, err error) {
	r, _, _ := pSendMessageW.Call(d.stage, CB_GETCURSEL, 0, 0)
	q = compareExportSelection{kind: d.selection, scope: d.selectedScope(), effective: r == 0}
	if q.scope == compareExportManual {
		q.manual, err = parseCompareExportBounds(d.manualDraft)
	}
	return
}

func (d *numericalExportDialog) refreshPlan() {
	if d.initializing || d.closed || d.writing || d.stale {
		return
	}
	d.clearPreviousResult()
	if d.previewCancel != nil {
		d.previewCancel()
	}
	d.previewSerial++
	d.planReady = false
	pEnableWindow.Call(d.write, 0)
	if d.captureError != nil {
		setText(d.availability, "当前没有可导出的时间剖面。")
		setText(d.fieldStatus, d.captureError.Error())
		return
	}
	q, err := d.currentRequest()
	if err != nil {
		hint := err.Error()
		if d.plan != nil {
			hint += "；仍显示上次有效预览。"
		}
		setText(d.fieldStatus, hint)
		return
	}
	setText(d.fieldStatus, "正在核对真实道和采样位置…")
	if d.plan == nil {
		setText(d.preview, "")
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.previewCancel = cancel
	head, ch, serial, capture := d.h, d.previews, d.previewSerial, d.capture
	go func() {
		defer retryRetiredPoststackResults()
		p, err := planNumericalCompareExport(ctx, capture, q)
		if ctx.Err() != nil {
			return
		}
		result := numericalExportPreview{serial, p, err}
		select {
		case ch <- result:
		default:
			// A queued result is inexpensive and version checked. Wait only
			// until the UI accepts it or this preview is cancelled.
			select {
			case ch <- result:
			case <-ctx.Done():
				return
			}
		}
		pPostMessageW.Call(head, numericalExportPreviewMessage, 0, 0)
	}()
}

func (d *numericalExportDialog) setPathText(path string) {
	prior := d.initializing
	d.initializing = true
	setText(d.path, path)
	d.initializing = prior
}

func (d *numericalExportDialog) clearPreviousResult() {
	d.lastPath, d.errorDetail = "", ""
	for _, h := range []uintptr{d.folder, d.reload, d.details} {
		pEnableWindow.Call(h, 0)
	}
	setText(d.status, "")
}

func (d *numericalExportDialog) setExportError(status string, err error) {
	setText(d.status, status)
	d.errorDetail = ""
	if err != nil {
		d.errorDetail = err.Error()
		logNumericalExportError(err)
	}
	pEnableWindow.Call(d.details, boolToFlag(d.errorDetail != ""))
}

func (d *numericalExportDialog) showNameMode() {
	label := "自动命名 · 避免同名"
	if d.changedPath {
		label = "自定义命名"
	}
	setText(d.nameMode, label)
	pEnableWindow.Call(d.autoName, boolToFlag(d.changedPath && !d.writing && !d.stale))
}

func (d *numericalExportDialog) refreshSuggestedPath() {
	d.showNameMode()
	if d.plan == nil || !d.planReady || d.changedPath {
		return
	}
	base, err := numericalExportBasePath(d.defaultFolder, *d.plan)
	if err == nil {
		base, err = availableNumericalExportPath(base)
	}
	if err != nil {
		d.setPathText("")
		d.setExportError("无法建议输出文件名；请选择较短且可写的目录。", err)
		return
	}
	d.setPathText(base)
}

func (d *numericalExportDialog) refreshContext() {
	if d.closed || d.stale {
		return
	}
	key := numericExportKey()
	frameBecameReady := false
	stage, _, _ := pSendMessageW.Call(d.stage, CB_GETCURSEL, 0, 0)
	usesFilter := stage == 0 && ((d.selection != compareExportB && d.capture.settings[0].Enabled) ||
		(d.selection != compareExportA && d.capture.settings[1].Enabled))
	if frame := viewFilter.frame; !d.writing && d.capture.frame == nil && frame != nil &&
		usesFilter {
		frameKey := frame.key
		frameKey.render = 0
		frameBecameReady = frameKey == key && frame.settings == viewFilter.active
	}
	if d.owner == compareHwnd && d.capture.key == key && d.capture.settings == viewFilter.active && !frameBecameReady {
		return
	}
	// Navigation and committed filter changes on the same A update the plan
	// instead of silently leaving the old auto filename in the text field.
	// Writes and exports pinned to a generated result remain immutable.
	if !d.writing && d.owner == compareHwnd && d.capture.key.a == key.a && d.capture.key.aVersion == key.aVersion &&
		(d.entryResultPath == "" || d.entryResultPath == key.b) {
		scope := d.selectedScope()
		c, err := captureNumericalCompareExport()
		d.capture, d.captureError = c, err
		d.plan = nil
		if c.axisUnits != "" && c.axisUnits != getText(d.spaceLabel) {
			setText(d.spaceLabel, c.axisUnits)
			d.layout()
		}
		d.initializing = true
		pSendMessageW.Call(d.scope, CB_RESETCONTENT, 0, 0)
		d.scopeOptions = []int{compareExportVisible, compareExportLine, compareExportAll}
		if c.rectangle != nil {
			d.scopeOptions = append(d.scopeOptions, compareExportRectangle)
		}
		d.scopeOptions = append(d.scopeOptions, compareExportManual)
		index := 0
		for i, option := range d.scopeOptions {
			pSendMessageW.Call(d.scope, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(compareExportScopeNames[option]))))
			if option == scope {
				index = i
			}
		}
		pSendMessageW.Call(d.scope, CB_SETCURSEL, uintptr(index), 0)
		d.initializing = false
		d.updateAvailability()
		d.setRangeFields()
		setText(d.preview, "")
		d.refreshPlan()
		return
	}
	d.stale = true
	d.plan = nil
	d.planReady = false
	d.clearPreviousResult()
	if d.previewCancel != nil {
		d.previewCancel()
	}
	if d.writeCancel != nil {
		d.writeCancel()
		if atomic.CompareAndSwapInt64(&compareExportGen, d.writeGeneration, d.writeGeneration+1) {
			setCompareProgress(0, false)
		}
	}
	pEnableWindow.Call(d.write, 0)
	setText(d.preview, "")
	setText(d.fieldStatus, "主图、数据来源或滤波版本已变化；请从“出”重新打开。")
	d.showNameMode()
}

func updateNumericalExportContext() {
	if d := numericalExportWindow; d != nil {
		d.refreshContext()
	}
}

func closeNumericalCompareExport() {
	if d := numericalExportWindow; d != nil {
		closeOwnedSettingsWindow(d.h, d.owner)
	}
}

func translateNumericalExportMessage(m *MSG) bool {
	d := numericalExportWindow
	if d == nil || m == nil || m.Message != WM_KEYDOWN || (m.WParam != 0x09 && m.WParam != VK_ESCAPE) {
		return false
	}
	if child, _, _ := pIsChild.Call(d.h, m.Hwnd); m.Hwnd != d.h && child == 0 {
		return false
	}
	if m.WParam == VK_ESCAPE {
		for _, control := range []uintptr{d.scope, d.stage} {
			if open, _, _ := pSendMessageW.Call(control, CB_GETDROPPEDSTATE, 0, 0); open != 0 {
				return false
			}
		}
		pPostMessageW.Call(d.h, WM_CLOSE, 0, 0)
		return true
	}
	handled, _, _ := algorithmIsDialogMessage.Call(d.h, uintptr(unsafe.Pointer(m)))
	return handled != 0
}

func (d *numericalExportDialog) beginWrite() {
	d.refreshContext()
	if d.writing || d.stale || d.plan == nil || !d.planReady {
		return
	}
	draft, err := d.currentRequest()
	if err != nil || draft != d.plan.request {
		d.refreshPlan()
		return
	}
	plan := d.plan
	d.clearPreviousResult()
	var path, autoBase string
	var target os.FileInfo
	if d.changedPath {
		path, err = resolveNumericalExportPath(getText(d.path), d.defaultFolder)
		if err == nil {
			target, err = os.Lstat(path)
			if errors.Is(err, fs.ErrNotExist) {
				err = nil
			}
		}
	} else {
		autoBase, err = numericalExportBasePath(d.defaultFolder, *d.plan)
		if err == nil {
			path, err = availableNumericalExportPath(autoBase)
		}
	}
	if err != nil {
		if d.changedPath && path == "" {
			d.setExportError(err.Error(), nil)
		} else {
			d.setExportError("无法使用该输出位置，请检查名称或选择其他目录。", err)
		}
		return
	}
	if target != nil {
		if !target.Mode().IsRegular() {
			d.setExportError("该名称不是普通文件，请另选文件名。", nil)
			return
		}
		for _, s := range d.capture.sources {
			if s.identity != nil && os.SameFile(target, s.identity) {
				d.setExportError("不能覆盖正在使用的 A/B 源文件，请另存为。", nil)
				return
			}
		}
		// Custom targets alone offer an explicit overwrite. No is Save As,
		// Cancel does nothing, and the default is deliberately not Yes.
		answer, _, _ := pMessageBoxW.Call(d.h, uintptr(unsafe.Pointer(u16("目标文件已存在：\r\n"+path+"\r\n\r\n是：覆盖；否：另存为；取消：返回。"))), uintptr(unsafe.Pointer(u16("确认保存位置"))), MB_YESNOCANCEL|MB_ICONINFORMATION|0x100)
		if answer == IDNO {
			d.browseDestination()
			return
		}
		if answer != IDYES {
			return
		}
		d.refreshContext()
		if d.closed || d.stale || d.writing || !d.planReady || d.plan != plan {
			return
		}
	}
	d.setPathText(path)
	ctx, cancel := context.WithCancel(context.Background())
	d.writeCancel, d.writing = cancel, true
	d.writeGeneration = atomic.AddInt64(&compareExportGen, 1)
	d.setWriting(true)
	setText(d.status, "正在写入、重新读取和校验 SEG-Y…")
	setText(cc.status, "正在导出 SEG-Y 数值数据")
	setCompareProgress(0, true)
	p := *plan
	gen, owner, writes := d.writeGeneration, d.owner, d.writes
	go func() {
		defer retryRetiredPoststackResults()
		last := -1
		progress := func(done, total int) {
			if total <= 0 || ctx.Err() != nil {
				return
			}
			percent := done * 95 / total
			if percent != last {
				last = percent
				postCompareOwnedProgress(owner, gen, percent, true)
			}
		}
		e := executeNumericalCompareExportWithTarget(ctx, path, p, target, progress)
		// A process can claim the proposed path after preview. The no-replace
		// publication remains authoritative; in auto mode retry a fresh name.
		for retry := 0; autoBase != "" && ctx.Err() == nil && errors.Is(e, fs.ErrExist) && retry < 6; retry++ {
			path, e = availableNumericalExportPath(autoBase)
			if e != nil {
				break
			}
			last = -1
			e = executeNumericalCompareExportWithTarget(ctx, path, p, nil, progress)
		}
		r := compareExportResult{path: path, kind: compareExportNames[p.request.kind], traces: len(p.rows),
			samples: p.samples, err: e, gen: gen, owner: owner}
		writes <- r
		pPostMessageW.Call(owner, WM_COMPARE_EXPORT_DONE, 0, 0)
	}()
}

func (d *numericalExportDialog) browseDestination() {
	before := getText(d.path)
	initial, err := resolveNumericalExportPath(before, d.defaultFolder)
	if err != nil {
		initial = filepath.Join(d.defaultFolder, "section.sgy")
	}
	path := saveSegyDialogWithOverwrite(d.h, initial, false)
	if path == "" || d != numericalExportWindow || d.closed || d.stale {
		return
	}
	d.clearPreviousResult()
	// Moving the directory alone is not a request to freeze the auto name.
	_, targetExists := os.Lstat(path)
	d.changedPath = d.changedPath || targetExists == nil || !strings.EqualFold(filepath.Base(path), filepath.Base(before))
	d.defaultFolder = filepath.Dir(path)
	rememberNumericalExportDirectory(d.defaultFolder)
	d.setPathText(path)
	if !d.changedPath {
		d.refreshSuggestedPath()
	} else {
		d.showNameMode()
	}
}

func (d *numericalExportDialog) setWriting(writing bool) {
	for _, h := range append([]uintptr{d.scope, d.stage, d.path, d.browse, d.write, d.autoName, d.details}, d.manualEdit[:]...) {
		pEnableWindow.Call(h, boolToFlag(!writing))
	}
	for i, available := range d.available() {
		pEnableWindow.Call(d.object[i], boolToFlag(!writing && available))
	}
	pEnableWindow.Call(d.write, boolToFlag(!writing && !d.stale && d.plan != nil && d.planReady))
	pEnableWindow.Call(d.folder, boolToFlag(!writing && d.lastPath != ""))
	pEnableWindow.Call(d.reload, boolToFlag(!writing && d.lastPath != ""))
	if writing {
		setText(d.cancel, "取消导出")
	} else {
		setText(d.cancel, "关闭")
	}
	d.showNameMode()
	pEnableWindow.Call(d.details, boolToFlag(!writing && d.errorDetail != ""))
}

func handleCompareExportDone() {
	d := numericalExportWindow
	if d == nil || d.closed {
		return
	}
	select {
	case r := <-d.writes:
		if r.owner != compareHwnd || r.gen != d.writeGeneration {
			return
		}
		if d.writeCancel != nil {
			d.writeCancel()
		}
		d.writeCancel, d.writing = nil, false
		if d.stale {
			d.setWriting(false)
			pEnableWindow.Call(d.cancel, 1)
			setText(d.status, "导出上下文已变化；请重新打开。")
			return
		}
		setCompareProgress(100, false)
		status := ""
		if errors.Is(r.err, context.Canceled) {
			status = "导出已取消；没有发布半成品。"
		} else if r.err != nil {
			status = "导出失败；请检查保存位置、权限或数据读取状态。"
			if errors.Is(r.err, fs.ErrExist) {
				status = "目标文件在导出时已存在或已变化；请重试或另存为。"
			}
			d.setExportError(status, r.err)
		} else {
			d.lastPath = r.path
			d.setPathText(r.path)
			d.defaultFolder = filepath.Dir(r.path)
			rememberNumericalExportDirectory(d.defaultFolder)
			status = fmt.Sprintf("已校验：%s · %d 道 × %d 样点/道", filepath.Base(r.path), r.traces, r.samples)
			setText(d.preview, d.planSummaryOrEmpty()+"\r\n保存位置："+r.path)
		}
		d.setWriting(false)
		pEnableWindow.Call(d.cancel, 1)
		setText(d.status, status)
		setText(cc.status, status)
	default:
	}
}

func (d *numericalExportDialog) planSummaryOrEmpty() string {
	if d.plan != nil {
		return d.plan.summary
	}
	return ""
}

func numericalExportWndProc(h uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	d := numericalExportWindow
	if d == nil || h != d.h {
		r, _, _ := pDefWindowProcW.Call(h, uintptr(msg), wParam, lParam)
		return r
	}
	switch msg {
	case WM_GETMINMAXINFO:
		if lParam != 0 {
			info := (*MINMAXINFO)(unsafe.Pointer(lParam))
			area, dpi := algorithmMonitorArea(h), displayDPI(h)
			info.PtMinTrackSize.X = min(area.Right-area.Left, int32(displayPixelDIP(430, dpi)))
			info.PtMinTrackSize.Y = min(area.Bottom-area.Top, int32(displayPixelDIP(435, dpi)))
		}
		return 0
	case WM_SIZE:
		d.layout()
		return 0
	case 0x02E0: // WM_DPICHANGED
		if lParam != 0 {
			r := (*RECT)(unsafe.Pointer(lParam))
			pMoveWindow.Call(h, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), 1)
		}
		d.layout()
		return 0
	case 0x0113: // WM_TIMER
		d.refreshContext()
		return 0
	case numericalExportPreviewMessage:
		d.refreshContext()
		select {
		case a := <-d.previews:
			if !d.stale && a.serial == d.previewSerial {
				if d.previewCancel != nil {
					d.previewCancel()
				}
				d.previewCancel = nil
				if a.err != nil {
					setText(d.fieldStatus, "无法解析该范围；请检查起止、数据覆盖或数值阶段。")
					d.setExportError("范围尚不可用，请依据详情调整后再导出。", a.err)
				} else {
					d.plan = &a.plan
					d.planReady = true
					d.setRangeFields()
					setText(d.fieldStatus, "")
					setText(d.preview, a.plan.summary)
					d.refreshSuggestedPath()
					pEnableWindow.Call(d.write, 1)
				}
			}
		default:
		}
		return 0
	case WM_COMMAND:
		if d.initializing {
			return 0
		}
		id, code := int(uint16(wParam)), uint16(wParam>>16)
		switch id {
		case numericalExportObjectA, numericalExportObjectB, numericalExportObjectDelta:
			if code == 0 && !d.writing && d.available()[id-numericalExportObjectA] {
				d.selection = id - numericalExportObjectA
				d.markSelection()
				d.refreshPlan()
			}
		case numericalExportScope, numericalExportStage:
			if code == 1 { // CBN_SELCHANGE
				d.setRangeFields()
				d.refreshPlan()
			}
		case numericalExportManualFirst, numericalExportManualLast, numericalExportManualT0, numericalExportManualT1:
			if code == 0x0300 && d.selectedScope() == compareExportManual { // EN_CHANGE
				for i, h := range d.manualEdit {
					d.manualDraft[i] = getText(h)
				}
				d.refreshPlan()
			}
		case numericalExportPath:
			if code == 0x0300 && !d.writing {
				d.changedPath = true
				d.clearPreviousResult()
				d.showNameMode()
			}
		case numericalExportBrowse:
			d.browseDestination()
		case numericalExportAutoName:
			d.clearPreviousResult()
			d.changedPath = false
			d.refreshSuggestedPath()
		case numericalExportDetails:
			if d.errorDetail != "" {
				message(h, "导出详情", d.errorDetail, MB_OK|MB_ICONINFORMATION)
			}
		case numericalExportWrite:
			d.beginWrite()
		case numericalExportFolder:
			d.refreshContext()
			if !d.writing && d.lastPath != "" {
				pShellExecuteW.Call(h, uintptr(unsafe.Pointer(u16("open"))), uintptr(unsafe.Pointer(u16("explorer.exe"))),
					uintptr(unsafe.Pointer(u16(`/select,"`+d.lastPath+`"`))), 0, 1)
			}
		case numericalExportReload:
			d.refreshContext()
			if !d.writing && d.lastPath != "" {
				path := d.lastPath
				closeOwnedSettingsWindow(h, d.owner)
				applyComparePath(1, path)
			}
		case numericalExportCancel, IDCANCEL:
			if d.writing {
				d.writeCancel()
				pEnableWindow.Call(d.cancel, 0)
				setText(d.status, "正在取消并清理临时文件…")
			} else {
				closeOwnedSettingsWindow(h, d.owner)
			}
		}
		return 0
	case WM_CLOSE:
		if d.writing {
			pSendMessageW.Call(h, WM_COMMAND, numericalExportCancel, 0)
		} else {
			closeOwnedSettingsWindow(h, d.owner)
		}
		return 0
	case WM_DESTROY:
		pKillTimer.Call(h, numericalExportTimer)
		d.closed = true
		if d.previewCancel != nil {
			d.previewCancel()
		}
		if d.writeCancel != nil {
			d.writeCancel()
			atomic.CompareAndSwapInt64(&compareExportGen, d.writeGeneration, d.writeGeneration+1)
		}
		numericalExportWindow = nil
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(h, uintptr(msg), wParam, lParam)
	return r
}
