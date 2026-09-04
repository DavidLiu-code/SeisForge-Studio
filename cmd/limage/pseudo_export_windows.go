//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	projectcore "github.com/DavidLiu-code/SeisForge-Studio/internal/project"
	pseudoexportcore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudoexport"
)

const (
	IDPSEUDO_EXPORT_RANGE = 7221

	IDPSEUDO_EXPORT_CANCEL = 7241
	IDPSEUDO_EXPORT_OPEN   = 7242
	IDPSEUDO_EXPORT_CLOSE  = 7243

	WM_PSEUDO_EXPORT_PROGRESS = WM_USER + 461
	WM_PSEUDO_EXPORT_DONE     = WM_USER + 462
)

type pseudoExportControls struct {
	status, detail, progress uintptr
	cancel, open, close      uintptr
}

type pseudoExportCompletion struct {
	result pseudoexportcore.PseudoExportResult
	err    error
}

type pseudoExportDeliveryState struct {
	generation     int64
	hwnd           uintptr
	running        bool
	cancel         context.CancelFunc
	progress       *pseudoexportcore.PseudoExportProgress
	progressPosted bool
	completion     *pseudoExportCompletion
}

var (
	pseudoExportHwnd uintptr
	pseudoExportUI   pseudoExportControls
	pseudoExportMu   sync.Mutex
	pseudoExportWork pseudoExportDeliveryState
	pseudoExportGen  int64
)

// pseudoExportAvailability is kept free of Win32 calls so the button contract
// can be regression-tested without opening a native window.
type pseudoExportAvailability struct {
	Enabled      bool
	VisibleReady int
	Reason       string
}

func pseudoExportAvailabilityForSession(session *pseudoSession, exporting bool) pseudoExportAvailability {
	if session == nil || session.project == nil {
		return pseudoExportAvailability{Reason: "当前没有伪三维项目"}
	}
	spatial, timeRange := session.spatialRange.Normalized(), session.timeRange.Normalized()
	if !spatial.Valid && !timeRange.Valid {
		return pseudoExportAvailability{Reason: "请先设置空间或时间范围"}
	}
	if exporting {
		return pseudoExportAvailability{Reason: "导出任务正在运行"}
	}
	if session.rangeDragging || session.timeDragging || session.leftDragPending || session.rotating || session.panning || session.zooming {
		return pseudoExportAvailability{Reason: "请先结束当前拖动"}
	}
	if session.rangePreviewActive || session.timePreviewActive || session.progressRenderPending || session.finalRenderPending {
		return pseudoExportAvailability{Reason: "请等待当前预览完成"}
	}
	selected, processed := 0, 0
	for i := range session.lines {
		line := &session.lines[i]
		if !line.selected || !line.inRange || !line.timeInRange || line.canonicalUnavailable || line.line == nil || !line.line.Valid() {
			continue
		}
		selected++
		if line.loading {
			return pseudoExportAvailability{Reason: "请等待测线加载完成"}
		}
		if line.errorText != "" || (line.line != nil && line.line.OpenError != "") {
			processed++
		} else if line.ready && len(line.segments) > 0 {
			processed++
		}
	}
	if selected > processed {
		return pseudoExportAvailability{Reason: "请等待测线加载完成"}
	}
	visible := 0
	for i := range session.lines {
		if pseudoExportLineVisible(&session.lines[i]) {
			visible++
		}
	}
	if visible == 0 {
		return pseudoExportAvailability{Reason: "当前没有已勾选且实际显示完成的测线"}
	}
	return pseudoExportAvailability{Enabled: true, VisibleReady: visible}
}

func pseudoExportLineVisible(line *pseudoLineState) bool {
	return line != nil && line.selected && line.inRange && line.timeInRange && line.ready && !line.loading &&
		line.errorText == "" && line.geometry != nil && len(line.geometry.TraceIndices) >= 2 && len(line.segments) > 0 &&
		line.line != nil && line.line.Valid() && !line.canonicalUnavailable
}

func pseudoExportIsRunning() bool {
	pseudoExportMu.Lock()
	running := pseudoExportWork.running
	pseudoExportMu.Unlock()
	return running
}

func updatePseudoExportButton() {
	if pseudoControlsUI.exportRange == 0 {
		return
	}
	availability := pseudoExportAvailabilityForSession(&pseudoState, pseudoExportIsRunning())
	enabled := uintptr(0)
	if availability.Enabled {
		enabled = 1
	}
	pEnableWindow.Call(pseudoControlsUI.exportRange, enabled)
}

func pseudoExportCalibration(line *pseudoLineState, projectLines []crookedProjectLineState) projectcore.CoordinateCalibration {
	if line != nil && line.line != nil {
		for i := range projectLines {
			candidate := &projectLines[i]
			if candidate.line != nil && candidate.line.ID == line.line.ID {
				return candidate.calibration
			}
		}
	}
	multiplier := 1.0
	if line != nil && line.multiplier > 0 {
		multiplier = line.multiplier
	}
	return projectcore.CoordinateCalibration{Multiplier: multiplier, Accepted: line != nil && line.geometryCalibrated}
}

// pseudoExportPlanInputFromSession freezes only checked curtains that are
// actually present in the current completed scene. Every geometry slice is
// deep-copied so later range, selection, or geometry changes cannot alter an
// export already running in the background.
func pseudoExportPlanInputFromSession(session *pseudoSession, projectLines []crookedProjectLineState) pseudoexportcore.PlanInput {
	input := pseudoexportcore.PlanInput{}
	if session == nil || session.project == nil {
		return input
	}
	input.ProjectName = filepath.Base(session.project.Root)
	if input.ProjectName == "." || input.ProjectName == "" {
		input.ProjectName = "crooked-project"
	}
	input.NavigationPath = session.project.NavigationPath
	input.SpatialRange = session.spatialRange.Normalized()
	input.TimeRange = session.timeRange.Normalized()
	input.Lines = make([]pseudoexportcore.LineInput, 0, len(session.lines))
	for i := range session.lines {
		state := &session.lines[i]
		if !pseudoExportLineVisible(state) {
			continue
		}
		geometry := cloneCrookedGeometry(state.geometry)
		metadata := state.line.Dataset.Metadata
		fingerprint := state.geometryFingerprint
		if fingerprint == "" {
			fingerprint = crookedGeometryFingerprint(state.line.ID, &geometry)
		}
		sourcePath := state.line.Path
		if sourcePath == "" {
			sourcePath = state.line.Dataset.Path
		}
		input.Lines = append(input.Lines, pseudoexportcore.LineInput{
			ID:                  state.line.ID,
			Name:                state.line.Name,
			SourcePath:          sourcePath,
			GeometryFingerprint: fingerprint,
			Geometry:            &geometry,
			Calibration:         pseudoExportCalibration(state, projectLines),
			SampleIntervalUS:    metadata.SampleIntervalUS,
			SamplesPerTrace:     metadata.SamplesPerTrace,
			BytesPerSample:      metadata.BytesPerSample,
			DataStart:           metadata.DataStart,
			FormatCode:          metadata.FormatCode,
			Endian:              metadata.Endian,
			TraceCount:          metadata.TraceCount,
			FileSize:            metadata.FileSize,
			ExtendedTextHeaders: metadata.ExtendedTextHeaders,
			SampleStart:         state.sampleStart,
			SampleEnd:           state.sampleEnd,
			SampleWindowSet:     input.TimeRange.Valid,
		})
	}
	return input
}

func currentPseudoExportPlan() (pseudoexportcore.PseudoViewExportPlan, error) {
	availability := pseudoExportAvailabilityForSession(&pseudoState, pseudoExportIsRunning())
	if !availability.Enabled {
		return pseudoexportcore.PseudoViewExportPlan{}, errors.New(availability.Reason)
	}
	input := pseudoExportPlanInputFromSession(&pseudoState, crookedState.projectLines)
	return pseudoexportcore.BuildPlan(input)
}

func formatPseudoExportBytes(value int64) string {
	if value < 1024 {
		return fmt.Sprintf("%d B", value)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	amount := float64(value)
	unit := "B"
	for _, candidate := range units {
		amount /= 1024
		unit = candidate
		if amount < 1024 {
			break
		}
	}
	return fmt.Sprintf("%.2f %s", amount, unit)
}

func pseudoExportConfirmationText(plan pseudoexportcore.PseudoViewExportPlan) string {
	groups := make(map[int]int)
	for _, line := range plan.Lines {
		groups[line.SampleEnd-line.SampleStart+1]++
	}
	counts := make([]int, 0, len(groups))
	for samples := range groups {
		counts = append(counts, samples)
	}
	// Usually every line has the same sample count. Sorting makes mixed-rate
	// projects deterministic and keeps the confirmation compact enough for a
	// native MessageBox even with dozens of lines.
	for i := 1; i < len(counts); i++ {
		for j := i; j > 0 && counts[j] < counts[j-1]; j-- {
			counts[j], counts[j-1] = counts[j-1], counts[j]
		}
	}
	parts := make([]string, 0, len(counts))
	for _, samples := range counts {
		parts = append(parts, fmt.Sprintf("%d samples × %d 条", samples, groups[samples]))
	}
	return fmt.Sprintf("将按当前伪三维范围导出：\n\n文件：%d 个 SEG-Y\n总道数：%d\n每线样点：%s\n预计空间：%s\n\n当前视图仅指勾选测线、XY 范围和时间窗；不包含相机旋转、缩放或遮挡。\n\n继续选择输出父目录吗？",
		len(plan.Lines), plan.TotalTraces, strings.Join(parts, "；"), formatPseudoExportBytes(plan.EstimatedBytes))
}

func browsePseudoExportParent(owner uintptr) string {
	display := make([]uint16, 32768)
	info := crookedBrowseInfo{Owner: owner, DisplayName: uintptr(unsafe.Pointer(&display[0])),
		Title: uintptr(unsafe.Pointer(u16("选择伪三维裁剪导出的父目录"))), Flags: BIF_RETURNONLYFSDIRS | BIF_NEWDIALOGSTYLE}
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

func startPseudoRangeExport() {
	plan, err := currentPseudoExportPlan()
	if err != nil {
		message(pseudoHwnd, "导出伪三维范围", err.Error(), MB_OK|MB_ICONINFORMATION)
		updatePseudoExportButton()
		return
	}
	if askYesNo(pseudoHwnd, "导出伪三维范围", pseudoExportConfirmationText(plan)) != IDYES {
		return
	}
	parent := browsePseudoExportParent(pseudoHwnd)
	if parent == "" {
		return
	}
	beginPseudoExport(plan, parent)
}

func beginPseudoExport(plan pseudoexportcore.PseudoViewExportPlan, parent string) {
	if pseudoExportIsRunning() {
		if pseudoExportHwnd != 0 {
			pSetForeground.Call(pseudoExportHwnd)
		}
		return
	}
	if pseudoExportHwnd != 0 {
		pDestroyWindow.Call(pseudoExportHwnd)
	}
	pseudoExportCompletedDirectory = ""
	h, _, _ := pCreateWindowExW.Call(WS_EX_APPWINDOW, uintptr(unsafe.Pointer(u16("Limage64PseudoExport"))),
		uintptr(unsafe.Pointer(u16(APP_NAME+" - 伪三维范围导出"))), WS_POPUP|WS_CAPTION|WS_SYSMENU,
		uintptr(CW_USEDEFAULT), uintptr(CW_USEDEFAULT), 520, 230, pseudoHwnd, 0, 0, 0)
	if h == 0 {
		message(pseudoHwnd, "导出伪三维范围", "无法创建导出进度窗口。", MB_OK|MB_ICONERROR)
		return
	}
	pseudoExportHwnd = h
	createPseudoExportUI(plan)
	ctx, cancel := context.WithCancel(context.Background())
	generation := atomic.AddInt64(&pseudoExportGen, 1)
	pseudoExportMu.Lock()
	pseudoExportWork = pseudoExportDeliveryState{generation: generation, hwnd: h, running: true, cancel: cancel}
	pseudoExportMu.Unlock()
	updatePseudoExportButton()
	pShowWindow.Call(h, SW_SHOW)
	pUpdateWindow.Call(h)

	go func(snapshot pseudoexportcore.PseudoViewExportPlan, outputParent string, token int64) {
		result, exportErr := pseudoexportcore.Export(ctx, snapshot, outputParent, func(progress pseudoexportcore.PseudoExportProgress) {
			postPseudoExportProgress(token, progress)
		})
		postPseudoExportCompletion(token, pseudoExportCompletion{result: result, err: exportErr})
	}(plan, parent, generation)
}

func createPseudoExportUI(plan pseudoexportcore.PseudoViewExportPlan) {
	pseudoExportUI = pseudoExportControls{}
	pseudoExportUI.status = createCtrl(pseudoExportHwnd, "STATIC", "正在准备导出目录…", WS_CHILD|WS_VISIBLE, 18, 18, 474, 22, 0)
	pseudoExportUI.detail = createCtrl(pseudoExportHwnd, "STATIC",
		fmt.Sprintf("%d 个文件 | %d traces | 预计 %s", len(plan.Lines), plan.TotalTraces, formatPseudoExportBytes(plan.EstimatedBytes)),
		WS_CHILD|WS_VISIBLE, 18, 48, 474, 40, 0)
	pseudoExportUI.progress = createCtrl(pseudoExportHwnd, "msctls_progress32", "", WS_CHILD|WS_VISIBLE, 18, 96, 474, 20, 0)
	pSendMessageW.Call(pseudoExportUI.progress, PBM_SETRANGE, 0, uintptr(uint32(100)<<16))
	pSendMessageW.Call(pseudoExportUI.progress, PBM_SETPOS, 0, 0)
	pseudoExportUI.cancel = createCtrl(pseudoExportHwnd, "BUTTON", "取消", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 116, 144, 86, 28, IDPSEUDO_EXPORT_CANCEL)
	pseudoExportUI.open = createCtrl(pseudoExportHwnd, "BUTTON", "打开目录", WS_CHILD|WS_TABSTOP|BS_PUSHBUTTON, 216, 144, 86, 28, IDPSEUDO_EXPORT_OPEN)
	pseudoExportUI.close = createCtrl(pseudoExportHwnd, "BUTTON", "关闭", WS_CHILD|WS_TABSTOP|BS_PUSHBUTTON, 316, 144, 86, 28, IDPSEUDO_EXPORT_CLOSE)
}

func postPseudoExportProgress(generation int64, progress pseudoexportcore.PseudoExportProgress) {
	pseudoExportMu.Lock()
	hwnd, shouldPost := coalescePseudoExportProgress(&pseudoExportWork, generation, progress)
	if !shouldPost {
		pseudoExportMu.Unlock()
		return
	}
	pPostMessageW.Call(hwnd, WM_PSEUDO_EXPORT_PROGRESS, uintptr(generation), 0)
	pseudoExportMu.Unlock()
}

func coalescePseudoExportProgress(state *pseudoExportDeliveryState, generation int64, progress pseudoexportcore.PseudoExportProgress) (uintptr, bool) {
	if state == nil || !state.running || state.generation != generation || state.hwnd == 0 {
		return 0, false
	}
	copy := progress
	state.progress = &copy
	if state.progressPosted {
		return 0, false
	}
	state.progressPosted = true
	return state.hwnd, true
}

func postPseudoExportCompletion(generation int64, completion pseudoExportCompletion) {
	pseudoExportMu.Lock()
	if !pseudoExportWork.running || pseudoExportWork.generation != generation || pseudoExportWork.hwnd == 0 {
		pseudoExportMu.Unlock()
		return
	}
	copy := completion
	pseudoExportWork.completion = &copy
	hwnd := pseudoExportWork.hwnd
	pPostMessageW.Call(hwnd, WM_PSEUDO_EXPORT_DONE, uintptr(generation), 0)
	pseudoExportMu.Unlock()
}

func takePseudoExportProgress(generation int64) *pseudoexportcore.PseudoExportProgress {
	pseudoExportMu.Lock()
	if pseudoExportWork.generation != generation {
		pseudoExportMu.Unlock()
		return nil
	}
	progress := pseudoExportWork.progress
	pseudoExportWork.progress = nil
	pseudoExportWork.progressPosted = false
	pseudoExportMu.Unlock()
	return progress
}

func takePseudoExportCompletion(generation int64) *pseudoExportCompletion {
	pseudoExportMu.Lock()
	if pseudoExportWork.generation != generation {
		pseudoExportMu.Unlock()
		return nil
	}
	completion := pseudoExportWork.completion
	pseudoExportWork.completion = nil
	if completion != nil {
		pseudoExportWork.running = false
		pseudoExportWork.cancel = nil
	}
	pseudoExportMu.Unlock()
	return completion
}

func pseudoExportStageLabel(stage string) string {
	switch stage {
	case "starting":
		return "正在准备"
	case "exporting":
		return "正在写出"
	case "line_complete":
		return "测线完成"
	case "failed":
		return "测线失败，继续下一条"
	case "partial":
		return "部分完成"
	case "completed":
		return "全部完成"
	case "cancelled":
		return "已取消"
	default:
		return stage
	}
}

func handlePseudoExportProgress(generation int64) {
	progress := takePseudoExportProgress(generation)
	if progress == nil || pseudoExportHwnd == 0 {
		return
	}
	percent := int(math.Round(math.Max(0, math.Min(100, progress.Percent))))
	pSendMessageW.Call(pseudoExportUI.progress, PBM_SETPOS, uintptr(percent), 0)
	setText(pseudoExportUI.status, fmt.Sprintf("%s  %d%%", pseudoExportStageLabel(progress.Stage), percent))
	if progress.LineIndex > 0 {
		setText(pseudoExportUI.detail, fmt.Sprintf("测线 %d/%d：%s\n道 %d/%d（总计 %d/%d）", progress.LineIndex, progress.LineCount,
			progress.LineName, progress.TraceDone, progress.TraceTotal, progress.ProcessedTraces, progress.TotalTraces))
	}
}

func requestPseudoExportCancel() {
	pseudoExportMu.Lock()
	cancel := pseudoExportWork.cancel
	running := pseudoExportWork.running
	pseudoExportMu.Unlock()
	if running && cancel != nil {
		cancel()
		setText(pseudoExportUI.status, "正在取消；已完整写出的文件将保留…")
		setText(pseudoExportUI.cancel, "正在取消")
		pEnableWindow.Call(pseudoExportUI.cancel, 0)
	}
}

func handlePseudoExportDone(generation int64) {
	completion := takePseudoExportCompletion(generation)
	if completion == nil || pseudoExportHwnd == 0 {
		return
	}
	result, exportErr := completion.result, completion.err
	pEnableWindow.Call(pseudoExportUI.cancel, 0)
	pShowWindow.Call(pseudoExportUI.cancel, SW_HIDE)
	pShowWindow.Call(pseudoExportUI.close, SW_SHOW)
	if result.OutputDirectory != "" {
		pShowWindow.Call(pseudoExportUI.open, SW_SHOW)
	}
	if result.Cancelled || errors.Is(exportErr, context.Canceled) {
		setText(pseudoExportUI.status, "导出已取消；已完成文件和状态清单已保留。")
		setText(pseudoExportUI.detail, fmt.Sprintf("完成 %d 条，失败 %d 条\n%s", result.CompletedLines, result.FailedLines, result.OutputDirectory))
	} else if exportErr != nil {
		setText(pseudoExportUI.status, "导出失败："+exportErr.Error())
		setText(pseudoExportUI.detail, result.OutputDirectory)
	} else if result.Status == "failed" || (result.CompletedLines == 0 && result.FailedLines > 0) {
		pSendMessageW.Call(pseudoExportUI.progress, PBM_SETPOS, 100, 0)
		setText(pseudoExportUI.status, "导出失败；详情已写入状态清单。")
		setText(pseudoExportUI.detail, fmt.Sprintf("完成 %d 条，失败 %d 条\n%s", result.CompletedLines, result.FailedLines, result.OutputDirectory))
	} else if result.FailedLines > 0 || result.Status == "partial" || result.NavigationCopy.Status == "failed" || len(result.Errors) > 0 {
		pSendMessageW.Call(pseudoExportUI.progress, PBM_SETPOS, 100, 0)
		setText(pseudoExportUI.status, "导出完成但有警告；详情已写入状态清单。")
		setText(pseudoExportUI.detail, fmt.Sprintf("完成 %d 条，失败 %d 条，警告 %d 项\n%s", result.CompletedLines, result.FailedLines, len(result.Errors), result.OutputDirectory))
	} else {
		pSendMessageW.Call(pseudoExportUI.progress, PBM_SETPOS, 100, 0)
		setText(pseudoExportUI.status, "导出完成。")
		setText(pseudoExportUI.detail, fmt.Sprintf("完成 %d 条测线\n%s", result.CompletedLines, result.OutputDirectory))
	}
	updatePseudoExportButton()
}

func openPseudoExportDirectory() {
	if pseudoExportCompletedDirectory == "" {
		return
	}
	r, _, _ := pShellExecuteW.Call(pseudoExportHwnd, uintptr(unsafe.Pointer(u16("open"))),
		uintptr(unsafe.Pointer(u16(pseudoExportCompletedDirectory))), 0, 0, SW_SHOW)
	if r <= 32 {
		message(pseudoExportHwnd, "打开输出目录", "无法打开目录：\n"+pseudoExportCompletedDirectory, MB_OK|MB_ICONERROR)
	}
}

var pseudoExportCompletedDirectory string

func cancelPseudoExportForProjectClose() {
	generation := atomic.AddInt64(&pseudoExportGen, 1)
	pseudoExportMu.Lock()
	cancel := pseudoExportWork.cancel
	pseudoExportWork.generation = generation
	pseudoExportWork.hwnd = 0
	pseudoExportWork.running = false
	pseudoExportWork.cancel = nil
	pseudoExportWork.progress = nil
	pseudoExportWork.progressPosted = false
	pseudoExportWork.completion = nil
	pseudoExportMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if pseudoExportHwnd != 0 {
		hwnd := pseudoExportHwnd
		pseudoExportHwnd = 0
		pDestroyWindow.Call(hwnd)
	}
	pseudoExportUI = pseudoExportControls{}
	pseudoExportCompletedDirectory = ""
}

func pseudoExportWndProc(h uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case WM_COMMAND:
		switch int(wParam & 0xffff) {
		case IDPSEUDO_EXPORT_CANCEL:
			requestPseudoExportCancel()
		case IDPSEUDO_EXPORT_OPEN:
			openPseudoExportDirectory()
		case IDPSEUDO_EXPORT_CLOSE:
			pDestroyWindow.Call(h)
		}
		return 0
	case WM_PSEUDO_EXPORT_PROGRESS:
		handlePseudoExportProgress(int64(wParam))
		return 0
	case WM_PSEUDO_EXPORT_DONE:
		generation := int64(wParam)
		pseudoExportMu.Lock()
		if pseudoExportWork.generation == generation && pseudoExportWork.completion != nil {
			pseudoExportCompletedDirectory = pseudoExportWork.completion.result.OutputDirectory
		}
		pseudoExportMu.Unlock()
		handlePseudoExportDone(generation)
		return 0
	case WM_CLOSE:
		if pseudoExportIsRunning() {
			requestPseudoExportCancel()
			return 0
		}
		pDestroyWindow.Call(h)
		return 0
	case WM_DESTROY:
		if pseudoExportHwnd == h {
			pseudoExportHwnd = 0
			pseudoExportUI = pseudoExportControls{}
		}
		return 0
	}
	result, _, _ := pDefWindowProcW.Call(h, uintptr(msg), wParam, lParam)
	return result
}
