//go:build windows

package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/prestackbatch"
)

// The execution report viewer is deliberately read-only.  It loads only the
// metadata sidecar produced by the batch executor and never opens a SEG-Y
// reader or touches the active Gather/Compare render state.
const (
	prestackExecutionReportClass = "Limage64PrestackExecutionReport"

	prestackExecutionReportEditID    = 1
	prestackExecutionReportPathID    = 2
	prestackExecutionReportStatusID  = 3
	prestackExecutionReportCloseID   = 4
	prestackExecutionReportFilterID  = 5
	prestackExecutionReportCopyID    = 6
	prestackExecutionReportOpenDirID = 7
)

const (
	executionReportFilterAll = iota
	executionReportFilterProblems
	executionReportFilterSucceeded
	executionReportFilterIncomplete
)

type prestackExecutionReportWindowState struct {
	hwnd, owner uintptr
	path        string
	report      prestackbatch.ExecutionReport
	pathLabel   uintptr
	statusLabel uintptr
	edit        uintptr
	close       uintptr
	filter      uintptr
	copyPath    uintptr
	openDir     uintptr
	filterIndex int
}

var prestackExecutionReportWindow *prestackExecutionReportWindowState

// openPrestackExecutionReportViewer asks for a metadata-only execution report
// and displays it in a separate top-level window.  Re-opening the command
// focuses the existing viewer so repeated toolbar clicks do not create stale
// windows or duplicate readers.
func openPrestackExecutionReportViewer() {
	if state := prestackExecutionReportWindow; state != nil && state.hwnd != 0 {
		showTopLevelWindowRestored(state.hwnd)
		return
	}
	path := openPrestackExecutionReportDialog(prestackHwnd)
	if path == "" {
		return
	}
	openPrestackExecutionReportAt(path)
}

// openPrestackExecutionReportAt opens a known report path. It is shared by
// the file-picker entry point and the read-only batch-history viewer.
func openPrestackExecutionReportAt(path string) {
	report, err := prestackbatch.LoadExecutionReport(path)
	if err != nil {
		message(prestackHwnd, "查看执行报告", "读取执行报告失败："+err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	state := &prestackExecutionReportWindowState{owner: prestackHwnd, path: filepath.Clean(path), report: report}
	prestackExecutionReportWindow = state
	title := "执行报告 - " + filepath.Base(path)
	h, _, errNo := pCreateWindowExW.Call(
		WS_EX_APPWINDOW,
		uintptr(unsafe.Pointer(u16(prestackExecutionReportClass))),
		uintptr(unsafe.Pointer(u16(title))),
		WS_OVERLAPPEDWINDOW|WS_CLIPCHILDREN|WS_VISIBLE,
		uintptr(CW_USEDEFAULT), uintptr(CW_USEDEFAULT), 900, 620,
		prestackHwnd, 0, 0, 0,
	)
	if h == 0 {
		prestackExecutionReportWindow = nil
		message(prestackHwnd, "查看执行报告", fmt.Sprintf("无法创建报告窗口：%v", errNo), MB_OK|MB_ICONERROR)
		return
	}
	state.hwnd = h
	showTopLevelWindowRestored(h)
	setPrestackStatus("已打开执行报告：" + path)
}

func openPrestackExecutionReportDialog(owner uintptr) string {
	buf := make([]uint16, 32768)
	filter := utf16.Encode([]rune("叠前执行报告 (*.execution.json)\x00*.execution.json\x00JSON 文件 (*.json)\x00*.json\x00所有文件 (*.*)\x00*.*\x00\x00"))
	of := OPENFILENAME{
		LStructSize:  uint32(unsafe.Sizeof(OPENFILENAME{})),
		HwndOwner:    owner,
		NFilterIndex: 1,
		LpstrFilter:  uintptr(unsafe.Pointer(&filter[0])),
		LpstrFile:    uintptr(unsafe.Pointer(&buf[0])),
		NMaxFile:     uint32(len(buf)),
		Flags:        OFN_EXPLORER | OFN_FILEMUSTEXIST,
		LpstrDefExt:  uintptr(unsafe.Pointer(u16("json"))),
	}
	if ok, _, _ := pGetOpenFileNameW.Call(uintptr(unsafe.Pointer(&of))); ok != 0 {
		return syscall.UTF16ToString(buf)
	}
	return ""
}

func executionReportKindLabel(kind prestackbatch.JobKind) string {
	if kind == prestackbatch.JobFigure {
		return "Figure"
	}
	return "QC"
}

func executionReportStatusLabel(status prestackbatch.ExecutionStatus) string {
	labels := map[prestackbatch.ExecutionStatus]string{
		prestackbatch.ExecutionPending:   "等待",
		prestackbatch.ExecutionRunning:   "运行中",
		prestackbatch.ExecutionSucceeded: "成功",
		prestackbatch.ExecutionFailed:    "失败",
		prestackbatch.ExecutionCanceled:  "已取消",
		prestackbatch.ExecutionSkipped:   "已跳过",
	}
	if label, ok := labels[status]; ok {
		return label + " (" + string(status) + ")"
	}
	return string(status)
}

func executionReportTaskMatchesFilter(task prestackbatch.ExecutionTaskReport, filter int) bool {
	switch filter {
	case executionReportFilterProblems:
		return task.Status == prestackbatch.ExecutionFailed || task.Status == prestackbatch.ExecutionCanceled
	case executionReportFilterSucceeded:
		return task.Status == prestackbatch.ExecutionSucceeded
	case executionReportFilterIncomplete:
		return task.Status == prestackbatch.ExecutionPending || task.Status == prestackbatch.ExecutionRunning || task.Status == prestackbatch.ExecutionSkipped
	default:
		return true
	}
}

func executionReportOutputDirectory(report prestackbatch.ExecutionReport) string {
	for _, task := range report.Tasks {
		for _, output := range []string{task.Output, task.Result} {
			if strings.TrimSpace(output) != "" {
				return filepath.Dir(filepath.Clean(output))
			}
		}
	}
	if strings.TrimSpace(report.PlanPath) != "" {
		return filepath.Dir(filepath.Clean(report.PlanPath))
	}
	return ""
}

// formatPrestackExecutionReport is kept separate from the window procedure so
// the report text remains deterministic and can be covered without creating a
// Win32 window.  Only report metadata is rendered; no samples or amplitudes
// are present in ExecutionReport by design.
func formatPrestackExecutionReport(path string, report prestackbatch.ExecutionReport) string {
	return formatPrestackExecutionReportFiltered(path, report, executionReportFilterAll)
}

func formatPrestackExecutionReportFiltered(path string, report prestackbatch.ExecutionReport, filter int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "叠前批处理执行报告\r\n")
	fmt.Fprintf(&b, "报告文件：%s\r\n", filepath.Clean(path))
	fmt.Fprintf(&b, "状态：%s\r\n", executionReportStatusLabel(report.Status))
	fmt.Fprintf(&b, "计划：%s\r\n", report.PlanPath)
	fmt.Fprintf(&b, "计划版本：%s\r\n", report.PlanApplicationVersion)
	fmt.Fprintf(&b, "运行 ID：%s\r\n", report.RunID)
	fmt.Fprintf(&b, "开始：%s\r\n", report.StartedUTC)
	if report.FinishedUTC != "" {
		fmt.Fprintf(&b, "结束：%s\r\n", report.FinishedUTC)
	}
	fmt.Fprintf(&b, "允许覆盖：%t\r\n", report.Overwrite)
	if report.PlanSHA256 != "" {
		fmt.Fprintf(&b, "计划 SHA-256：%s\r\n", report.PlanSHA256)
	}
	if report.Error != "" {
		fmt.Fprintf(&b, "执行错误：%s\r\n", report.Error)
	}
	fmt.Fprintf(&b, "\r\n任务（按 Figure → QC 顺序）：\r\n")
	visible := 0
	for _, task := range report.Tasks {
		if executionReportTaskMatchesFilter(task, filter) {
			visible++
		}
	}
	if visible == 0 {
		b.WriteString("（没有任务）\r\n")
	}
	ordinal := 0
	for _, task := range report.Tasks {
		if !executionReportTaskMatchesFilter(task, filter) {
			continue
		}
		ordinal++
		fmt.Fprintf(&b, "[%d] %s  %s  状态：%s\r\n", ordinal, executionReportKindLabel(task.Kind), task.ID, executionReportStatusLabel(task.Status))
		fmt.Fprintf(&b, "    输出：%s\r\n", task.Output)
		if task.Result != "" {
			fmt.Fprintf(&b, "    结果：%s\r\n", task.Result)
		}
		if task.StartedUTC != "" {
			fmt.Fprintf(&b, "    开始：%s\r\n", task.StartedUTC)
		}
		if task.FinishedUTC != "" {
			fmt.Fprintf(&b, "    结束：%s\r\n", task.FinishedUTC)
		}
		if task.Error != "" {
			fmt.Fprintf(&b, "    错误：%s\r\n", task.Error)
		}
	}
	return b.String()
}

func reportFilterLabel(filter int) string {
	switch filter {
	case executionReportFilterProblems:
		return "失败/取消"
	case executionReportFilterSucceeded:
		return "仅成功"
	case executionReportFilterIncomplete:
		return "未完成"
	default:
		return "全部任务"
	}
}

func copyPrestackReportPath(owner uintptr, path string) error {
	utf16Text, err := syscall.UTF16FromString(path)
	if err != nil || len(utf16Text) == 0 {
		return fmt.Errorf("报告路径为空")
	}
	opened, _, _ := traceClipboardOpen.Call(owner)
	if opened == 0 {
		return fmt.Errorf("无法打开剪贴板")
	}
	defer traceClipboardClose.Call()
	traceClipboardEmpty.Call()
	hMemory, _, _ := traceGlobalAlloc.Call(traceGMEMMoveable, uintptr(len(utf16Text)*2))
	if hMemory == 0 {
		return fmt.Errorf("无法分配剪贴板内存")
	}
	pointer, _, _ := traceGlobalLock.Call(hMemory)
	if pointer == 0 {
		traceGlobalFree.Call(hMemory)
		return fmt.Errorf("无法锁定剪贴板内存")
	}
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(pointer)), len(utf16Text)), utf16Text)
	traceGlobalUnlock.Call(hMemory)
	accepted, _, _ := traceClipboardSet.Call(traceCFUnicodeText, hMemory)
	if accepted == 0 {
		traceGlobalFree.Call(hMemory)
		return fmt.Errorf("无法写入剪贴板")
	}
	return nil
}

func openPrestackReportOutputDirectory(owner uintptr, report prestackbatch.ExecutionReport) error {
	directory := executionReportOutputDirectory(report)
	if directory == "" {
		return fmt.Errorf("报告没有可用的输出目录")
	}
	r, _, _ := pShellExecuteW.Call(owner, uintptr(unsafe.Pointer(u16("open"))), uintptr(unsafe.Pointer(u16(directory))), 0, 0, SW_SHOW)
	if r <= 32 {
		return fmt.Errorf("无法打开输出目录：%s", directory)
	}
	return nil
}

func prestackExecutionReportWndProc(h uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	state := prestackExecutionReportWindow
	switch msg {
	case WM_CREATE:
		if state == nil {
			return 0
		}
		state.hwnd = h
		state.filter = createCtrl(h, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 82, 8, 120, 180, prestackExecutionReportFilterID)
		for _, label := range []string{"全部任务", "失败/取消", "仅成功", "未完成"} {
			pSendMessageW.Call(state.filter, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(label))))
		}
		pSendMessageW.Call(state.filter, CB_SETCURSEL, 0, 0)
		state.copyPath = createCtrl(h, "BUTTON", "复制报告路径", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 212, 8, 108, 26, prestackExecutionReportCopyID)
		state.openDir = createCtrl(h, "BUTTON", "打开输出目录", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 326, 8, 108, 26, prestackExecutionReportOpenDirID)
		state.pathLabel = createCtrl(h, "STATIC", "", WS_CHILD|WS_VISIBLE, 446, 10, 390, 22, prestackExecutionReportPathID)
		state.edit = createCtrl(h, "EDIT", "", WS_CHILD|WS_VISIBLE|WS_BORDER|WS_VSCROLL|WS_HSCROLL|traceESMultiline|traceESAutoVScroll|ES_AUTOHSCROLL|ES_READONLY, 12, 42, 820, 476, prestackExecutionReportEditID)
		state.statusLabel = createCtrl(h, "STATIC", "只读元数据；报告不包含振幅或样点数组。", WS_CHILD|WS_VISIBLE|WS_BORDER, 12, 526, 720, 24, prestackExecutionReportStatusID)
		state.close = createCtrl(h, "BUTTON", "关闭", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 744, 524, 88, 26, prestackExecutionReportCloseID)
		setText(state.pathLabel, "报告："+state.path)
		setText(state.edit, formatPrestackExecutionReport(state.path, state.report))
		return 0
	case WM_SIZE:
		if state == nil {
			return 0
		}
		prestackExecutionReportLayout(state, int(uint16(lParam)), int(uint16(lParam>>16)))
		return 0
	case WM_GETMINMAXINFO:
		if lParam != 0 {
			mmi := (*MINMAXINFO)(unsafe.Pointer(lParam))
			mmi.PtMinTrackSize.X = 560
			mmi.PtMinTrackSize.Y = 360
		}
		return 0
	case WM_COMMAND:
		id := int(wParam & 0xffff)
		code := int((wParam >> 16) & 0xffff)
		switch id {
		case prestackExecutionReportCloseID:
			pDestroyWindow.Call(h)
			return 0
		case prestackExecutionReportFilterID:
			if code == 1 && state != nil {
				selected, _, _ := pSendMessageW.Call(state.filter, CB_GETCURSEL, 0, 0)
				state.filterIndex = int(selected)
				setText(state.edit, formatPrestackExecutionReportFiltered(state.path, state.report, state.filterIndex))
				setText(state.statusLabel, fmt.Sprintf("只读元数据；当前筛选：%s；报告不包含振幅或样点数组。", reportFilterLabel(state.filterIndex)))
			}
			return 0
		case prestackExecutionReportCopyID:
			if state != nil {
				if err := copyPrestackReportPath(h, state.path); err != nil {
					message(h, "复制报告路径", err.Error(), MB_OK|MB_ICONERROR)
				} else {
					setText(state.statusLabel, "报告路径已复制；报告不包含振幅或样点数组。")
				}
			}
			return 0
		case prestackExecutionReportOpenDirID:
			if state != nil {
				if err := openPrestackReportOutputDirectory(h, state.report); err != nil {
					message(h, "打开输出目录", err.Error(), MB_OK|MB_ICONERROR)
				}
			}
			return 0
		}
	case WM_CLOSE:
		pDestroyWindow.Call(h)
		return 0
	case WM_DESTROY:
		if prestackExecutionReportWindow != nil && prestackExecutionReportWindow.hwnd == h {
			prestackExecutionReportWindow = nil
		}
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(h, uintptr(msg), wParam, lParam)
	return r
}

func prestackExecutionReportLayout(state *prestackExecutionReportWindowState, width, height int) {
	if state == nil || width < 1 || height < 1 {
		return
	}
	margin := 12
	pathH := 26
	bottomH := 26
	editTop := 42
	editH := height - editTop - margin - bottomH - 12
	if editH < 80 {
		editH = 80
	}
	editW := width - margin*2
	if editW < 120 {
		editW = 120
	}
	filterW := 120
	copyW := 108
	openW := 108
	pathX := margin + 434
	pathY := margin
	if pathX > width-180 {
		// On the minimum report-window width the path moves below the action
		// row instead of overlapping the filter and path buttons.
		pathX = margin
		pathY = 34
		editTop = 58
		editH = height - editTop - margin - bottomH - 12
		if editH < 80 {
			editH = 80
		}
	}
	pMoveWindow.Call(state.filter, uintptr(margin+70), 8, uintptr(filterW), 24, 1)
	pMoveWindow.Call(state.copyPath, uintptr(margin+200), 8, uintptr(copyW), 26, 1)
	pMoveWindow.Call(state.openDir, uintptr(margin+314), 8, uintptr(openW), 26, 1)
	pMoveWindow.Call(state.pathLabel, uintptr(pathX), uintptr(pathY), uintptr(maxInt(editW-(pathX-margin), 120)), uintptr(pathH), 1)
	pMoveWindow.Call(state.edit, uintptr(margin), uintptr(editTop), uintptr(editW), uintptr(editH), 1)
	bottomY := editTop + editH + 8
	statusW := editW - 100
	if statusW < 120 {
		statusW = editW
	}
	pMoveWindow.Call(state.statusLabel, uintptr(margin), uintptr(bottomY), uintptr(statusW), uintptr(bottomH), 1)
	if editW > 130 {
		pMoveWindow.Call(state.close, uintptr(margin+statusW+10), uintptr(bottomY), 90, uintptr(bottomH), 1)
	} else {
		pMoveWindow.Call(state.close, uintptr(margin), uintptr(bottomY+bottomH+4), 90, uintptr(bottomH), 1)
	}
}
