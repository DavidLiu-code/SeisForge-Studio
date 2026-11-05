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

	prestackExecutionReportEditID   = 1
	prestackExecutionReportPathID   = 2
	prestackExecutionReportStatusID = 3
	prestackExecutionReportCloseID  = 4
)

type prestackExecutionReportWindowState struct {
	hwnd, owner uintptr
	path        string
	report      prestackbatch.ExecutionReport
	pathLabel   uintptr
	statusLabel uintptr
	edit        uintptr
	close       uintptr
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

// formatPrestackExecutionReport is kept separate from the window procedure so
// the report text remains deterministic and can be covered without creating a
// Win32 window.  Only report metadata is rendered; no samples or amplitudes
// are present in ExecutionReport by design.
func formatPrestackExecutionReport(path string, report prestackbatch.ExecutionReport) string {
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
	if len(report.Tasks) == 0 {
		b.WriteString("（没有任务）\r\n")
	}
	for i, task := range report.Tasks {
		fmt.Fprintf(&b, "[%d] %s  %s  状态：%s\r\n", i+1, executionReportKindLabel(task.Kind), task.ID, executionReportStatusLabel(task.Status))
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

func prestackExecutionReportWndProc(h uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	state := prestackExecutionReportWindow
	switch msg {
	case WM_CREATE:
		if state == nil {
			return 0
		}
		state.hwnd = h
		state.pathLabel = createCtrl(h, "STATIC", "", WS_CHILD|WS_VISIBLE, 12, 10, 820, 22, prestackExecutionReportPathID)
		state.edit = createCtrl(h, "EDIT", "", WS_CHILD|WS_VISIBLE|WS_BORDER|WS_VSCROLL|WS_HSCROLL|traceESMultiline|traceESAutoVScroll|ES_AUTOHSCROLL|ES_READONLY, 12, 38, 820, 480, prestackExecutionReportEditID)
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
		if int(wParam&0xffff) == prestackExecutionReportCloseID {
			pDestroyWindow.Call(h)
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
	pathH := 22
	bottomH := 26
	editH := height - margin*2 - pathH - bottomH - 12
	if editH < 80 {
		editH = 80
	}
	editW := width - margin*2
	if editW < 120 {
		editW = 120
	}
	pMoveWindow.Call(state.pathLabel, uintptr(margin), uintptr(margin), uintptr(editW), uintptr(pathH), 1)
	pMoveWindow.Call(state.edit, uintptr(margin), uintptr(margin+pathH+6), uintptr(editW), uintptr(editH), 1)
	bottomY := margin + pathH + 6 + editH + 8
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
