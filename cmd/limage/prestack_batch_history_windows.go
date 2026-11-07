//go:build windows

package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/prestackbatch"
)

// The history viewer is metadata-only. It never opens a SEG-Y reader and it
// keeps only defensive copies of the compact history rows in memory.
const (
	prestackHistoryClass       = "Limage64PrestackBatchHistory"
	prestackHistoryListID      = 1
	prestackHistoryDetailID    = 2
	prestackHistoryFilterID    = 3
	prestackHistoryRefreshID   = 4
	prestackHistoryOpenID      = 5
	prestackHistoryCopyID      = 6
	prestackHistoryDirectoryID = 7
	prestackHistoryCloseID     = 8
	prestackHistoryStatusID    = 9
)

type prestackExecutionHistoryWindowState struct {
	hwnd, owner                    uintptr
	list, detail, filter           uintptr
	refresh, open, copy, directory uintptr
	close, status                  uintptr
	path                           string
	history                        prestackbatch.BatchHistory
	visible                        []prestackbatch.HistoryEntry
}

var prestackExecutionHistoryWindow *prestackExecutionHistoryWindowState
var prestackHistoryClassRegistered bool

func ensurePrestackHistoryClass() {
	if prestackHistoryClassRegistered {
		return
	}
	hi, _, _ := pGetModuleHandleW.Call(0)
	cursor, _, _ := pLoadCursorW.Call(0, IDC_ARROW)
	registerClass(prestackHistoryClass, syscall.NewCallback(prestackHistoryWndProc), COLOR_WINDOW+1, hi, cursor)
	prestackHistoryClassRegistered = true
}

func openPrestackExecutionHistoryViewer() {
	if state := prestackExecutionHistoryWindow; state != nil && state.hwnd != 0 {
		showTopLevelWindowRestored(state.hwnd)
		syncPrestackExecutionHistoryWindow(state)
		return
	}
	path, err := prestackbatch.DefaultHistoryPath()
	if err != nil {
		message(prestackHwnd, "批处理历史", "无法定位历史文件："+err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	history, err := prestackbatch.LoadHistory(path)
	if err != nil {
		message(prestackHwnd, "批处理历史", "读取历史失败："+err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	ensurePrestackHistoryClass()
	state := &prestackExecutionHistoryWindowState{owner: prestackHwnd, path: path, history: history}
	prestackExecutionHistoryWindow = state
	h, _, errNo := pCreateWindowExW.Call(
		WS_EX_APPWINDOW,
		uintptr(unsafe.Pointer(u16(prestackHistoryClass))),
		uintptr(unsafe.Pointer(u16("批处理执行历史"))),
		WS_OVERLAPPEDWINDOW|WS_CLIPCHILDREN|WS_VISIBLE,
		uintptr(CW_USEDEFAULT), uintptr(CW_USEDEFAULT), 920, 620,
		prestackHwnd, 0, 0, 0,
	)
	if h == 0 {
		prestackExecutionHistoryWindow = nil
		message(prestackHwnd, "批处理历史", fmt.Sprintf("无法创建历史窗口：%v", errNo), MB_OK|MB_ICONERROR)
		return
	}
	state.hwnd = h
	showTopLevelWindowRestored(h)
	syncPrestackExecutionHistoryWindow(state)
}

func historyStatusLabel(status prestackbatch.ExecutionStatus) string {
	return executionReportStatusLabel(status)
}

func formatPrestackHistoryEntry(entry prestackbatch.HistoryEntry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "运行 ID：%s\r\n", entry.RunID)
	fmt.Fprintf(&b, "状态：%s\r\n", historyStatusLabel(entry.Status))
	fmt.Fprintf(&b, "计划：%s\r\n", entry.PlanPath)
	fmt.Fprintf(&b, "执行报告：%s\r\n", entry.ExecutionReportPath)
	fmt.Fprintf(&b, "输出目录：%s\r\n", entry.OutputDirectory)
	fmt.Fprintf(&b, "开始：%s\r\n", entry.StartedUTC)
	if entry.FinishedUTC != "" {
		fmt.Fprintf(&b, "结束：%s\r\n", entry.FinishedUTC)
	}
	fmt.Fprintf(&b, "版本：%s\r\n", entry.ApplicationVersion)
	fmt.Fprintf(&b, "任务：Figure %d；QC %d；成功 %d；失败 %d；取消 %d；未完成 %d\r\n",
		entry.FigureCount, entry.QCCount, entry.SucceededCount, entry.FailedCount, entry.CanceledCount, entry.IncompleteCount)
	return b.String()
}

func historyEntryMatchesFilter(entry prestackbatch.HistoryEntry, filter int) bool {
	switch filter {
	case executionReportFilterProblems:
		return entry.Status == prestackbatch.ExecutionFailed || entry.Status == prestackbatch.ExecutionCanceled
	case executionReportFilterSucceeded:
		return entry.Status == prestackbatch.ExecutionSucceeded
	case executionReportFilterIncomplete:
		return entry.Status == prestackbatch.ExecutionPending || entry.Status == prestackbatch.ExecutionRunning
	default:
		return true
	}
}

func syncPrestackExecutionHistoryWindow(state *prestackExecutionHistoryWindowState) {
	if state == nil || state.hwnd == 0 {
		return
	}
	filter, _, _ := pSendMessageW.Call(state.filter, CB_GETCURSEL, 0, 0)
	history, err := prestackbatch.LoadHistory(state.path)
	if err != nil {
		setText(state.status, "读取历史失败："+err.Error())
		return
	}
	state.history = history
	state.visible = state.visible[:0]
	pSendMessageW.Call(state.list, CB_RESETCONTENT, 0, 0)
	for _, entry := range history.Entries {
		if !historyEntryMatchesFilter(entry, int(filter)) {
			continue
		}
		state.visible = append(state.visible, entry)
		label := fmt.Sprintf("%s | %s | %s", entry.StartedUTC, historyStatusLabel(entry.Status), filepath.Base(entry.ExecutionReportPath))
		pSendMessageW.Call(state.list, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(label))))
	}
	if len(state.visible) > 0 {
		pSendMessageW.Call(state.list, CB_SETCURSEL, 0, 0)
		setText(state.detail, formatPrestackHistoryEntry(state.visible[0]))
		setText(state.status, fmt.Sprintf("历史 %d 条；最多保留 200 条。", len(state.visible)))
	} else {
		setText(state.detail, "暂无批处理执行历史。\r\n历史仅保存路径、状态、计数和时间，不包含振幅或样点。")
		setText(state.status, "暂无符合筛选条件的历史记录。")
	}
	pEnableWindow.Call(state.open, map[bool]uintptr{true: 1, false: 0}[len(state.visible) > 0])
	pEnableWindow.Call(state.copy, map[bool]uintptr{true: 1, false: 0}[len(state.visible) > 0])
	pEnableWindow.Call(state.directory, map[bool]uintptr{true: 1, false: 0}[len(state.visible) > 0])
}

func selectedPrestackHistoryEntry(state *prestackExecutionHistoryWindowState) (prestackbatch.HistoryEntry, bool) {
	if state == nil || len(state.visible) == 0 {
		return prestackbatch.HistoryEntry{}, false
	}
	selected, _, _ := pSendMessageW.Call(state.list, CB_GETCURSEL, 0, 0)
	if int(selected) < 0 || int(selected) >= len(state.visible) {
		return prestackbatch.HistoryEntry{}, false
	}
	return state.visible[int(selected)], true
}

func prestackHistoryWndProc(h uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	state := prestackExecutionHistoryWindow
	switch msg {
	case WM_CREATE:
		if state == nil {
			return 0
		}
		state.hwnd = h
		state.filter = createCtrl(h, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 12, 10, 120, 170, prestackHistoryFilterID)
		for _, label := range []string{"全部历史", "失败/取消", "仅成功", "未完成"} {
			pSendMessageW.Call(state.filter, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(label))))
		}
		pSendMessageW.Call(state.filter, CB_SETCURSEL, 0, 0)
		state.refresh = createCtrl(h, "BUTTON", "刷新", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 144, 9, 78, 27, prestackHistoryRefreshID)
		state.open = createCtrl(h, "BUTTON", "打开执行报告", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 234, 9, 120, 27, prestackHistoryOpenID)
		state.copy = createCtrl(h, "BUTTON", "复制报告路径", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 366, 9, 120, 27, prestackHistoryCopyID)
		state.directory = createCtrl(h, "BUTTON", "打开输出目录", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 498, 9, 120, 27, prestackHistoryDirectoryID)
		state.list = createCtrl(h, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS|WS_BORDER, 12, 48, 880, 220, prestackHistoryListID)
		state.detail = createCtrl(h, "EDIT", "", WS_CHILD|WS_VISIBLE|WS_BORDER|WS_VSCROLL|traceESMultiline|traceESAutoVScroll|ES_READONLY, 12, 84, 880, 430, prestackHistoryDetailID)
		state.status = createCtrl(h, "STATIC", "", WS_CHILD|WS_VISIBLE|WS_BORDER, 12, 526, 780, 24, prestackHistoryStatusID)
		state.close = createCtrl(h, "BUTTON", "关闭", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 802, 524, 90, 27, prestackHistoryCloseID)
		return 0
	case WM_SIZE:
		if state != nil {
			prestackHistoryLayout(state, int(uint16(lParam)), int(uint16(lParam>>16)))
		}
		return 0
	case WM_GETMINMAXINFO:
		if lParam != 0 {
			mmi := (*MINMAXINFO)(unsafe.Pointer(lParam))
			mmi.PtMinTrackSize.X, mmi.PtMinTrackSize.Y = 560, 360
		}
		return 0
	case WM_COMMAND:
		if state == nil {
			return 0
		}
		id := int(wParam & 0xffff)
		code := int((wParam >> 16) & 0xffff)
		switch id {
		case prestackHistoryRefreshID:
			syncPrestackExecutionHistoryWindow(state)
		case prestackHistoryFilterID:
			if code == 1 {
				syncPrestackExecutionHistoryWindow(state)
			}
		case prestackHistoryListID:
			if code == 1 {
				if entry, ok := selectedPrestackHistoryEntry(state); ok {
					setText(state.detail, formatPrestackHistoryEntry(entry))
				}
			}
		case prestackHistoryOpenID:
			if entry, ok := selectedPrestackHistoryEntry(state); ok {
				openPrestackExecutionReportAt(entry.ExecutionReportPath)
			}
		case prestackHistoryCopyID:
			if entry, ok := selectedPrestackHistoryEntry(state); ok {
				if err := copyPrestackReportPath(h, entry.ExecutionReportPath); err != nil {
					message(h, "复制报告路径", err.Error(), MB_OK|MB_ICONERROR)
				} else {
					setText(state.status, "执行报告路径已复制。")
				}
			}
		case prestackHistoryDirectoryID:
			if entry, ok := selectedPrestackHistoryEntry(state); ok {
				directory := entry.OutputDirectory
				if strings.TrimSpace(directory) == "" {
					directory = filepath.Dir(entry.ExecutionReportPath)
				}
				r, _, _ := pShellExecuteW.Call(h, uintptr(unsafe.Pointer(u16("open"))), uintptr(unsafe.Pointer(u16(directory))), 0, 0, SW_SHOW)
				if r <= 32 {
					message(h, "打开输出目录", "无法打开输出目录："+directory, MB_OK|MB_ICONERROR)
				}
			}
		case prestackHistoryCloseID:
			pDestroyWindow.Call(h)
		}
		return 0
	case WM_CLOSE:
		pDestroyWindow.Call(h)
		return 0
	case WM_DESTROY:
		if prestackExecutionHistoryWindow != nil && prestackExecutionHistoryWindow.hwnd == h {
			prestackExecutionHistoryWindow = nil
		}
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(h, uintptr(msg), wParam, lParam)
	return r
}

func prestackHistoryLayout(state *prestackExecutionHistoryWindowState, width, height int) {
	if state == nil || width < 1 || height < 1 {
		return
	}
	margin := 12
	contentW := maxInt(160, width-margin*2)
	pMoveWindow.Call(state.filter, uintptr(margin), 9, 120, 24, 1)
	pMoveWindow.Call(state.refresh, uintptr(margin+132), 9, 78, 27, 1)
	pMoveWindow.Call(state.open, uintptr(margin+222), 9, 120, 27, 1)
	pMoveWindow.Call(state.copy, uintptr(margin+354), 9, 120, 27, 1)
	pMoveWindow.Call(state.directory, uintptr(margin+486), 9, 120, 27, 1)
	pMoveWindow.Call(state.list, uintptr(margin), 48, uintptr(contentW), 28, 1)
	bottomH := 27
	detailH := maxInt(80, height-48-28-margin-bottomH-12)
	pMoveWindow.Call(state.detail, uintptr(margin), 84, uintptr(contentW), uintptr(detailH), 1)
	bottomY := 84 + detailH + 8
	statusW := maxInt(120, contentW-100)
	pMoveWindow.Call(state.status, uintptr(margin), uintptr(bottomY), uintptr(statusW), uintptr(bottomH), 1)
	pMoveWindow.Call(state.close, uintptr(margin+statusW+10), uintptr(bottomY), 90, uintptr(bottomH), 1)
}
