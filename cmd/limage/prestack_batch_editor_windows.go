//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/prestackbatch"
)

// The batch editor deliberately lives in its own top-level window.  It only
// owns a defensive PlanEditor copy and therefore never opens a SEG-Y reader or
// touches the active Gather/Compare render state.
const (
	prestackBatchEditorClass = "Limage64PrestackBatchEditor"
	batchEditListID          = 1
	batchEditDirID           = 2
	batchEditOutputID        = 3
	batchEditResultID        = 4
	batchEditFormatID        = 5
	batchEditApplyID         = 6
	batchEditUpID            = 7
	batchEditDownID          = 8
	batchEditRemoveID        = 9
	batchEditDirApplyID      = 10
	batchEditSaveAsID        = 11
	batchEditCancelID        = 12
	batchEditStatusID        = 13
	batchEditLabelDirID      = 17
	batchEditLabelOutputID   = 14
	batchEditLabelResultID   = 15
	batchEditLabelFormatID   = 16

	batchEditListNotifySelChange = 1
	batchEditWSVScroll           = 0x00200000
	batchEditLBSNotify           = 0x00000001
	batchEditLBSNoIntegralHeight = 0x00000100
)

const (
	batchEditLBResetContent = 0x0184
	batchEditLBAddString    = 0x0180
	batchEditLBGetCurSel    = 0x0188
	batchEditLBSetCurSel    = 0x0186
)

type prestackBatchEditorWindowState struct {
	hwnd, owner uintptr
	planPath    string
	editor      *prestackbatch.PlanEditor
	tasks       []prestackbatch.TaskSummary
	selected    int

	list      uintptr
	dir       uintptr
	output    uintptr
	result    uintptr
	format    uintptr
	apply     uintptr
	up        uintptr
	down      uintptr
	remove    uintptr
	dirApply  uintptr
	saveAs    uintptr
	cancel    uintptr
	status    uintptr
	dirLbl    uintptr
	outputLbl uintptr
	resultLbl uintptr
	formatLbl uintptr
}

var prestackBatchEditorWindow *prestackBatchEditorWindowState

func openPrestackBatchEditor() {
	if prestackBatchEditorWindow != nil && prestackBatchEditorWindow.hwnd != 0 {
		showTopLevelWindowRestored(prestackBatchEditorWindow.hwnd)
		return
	}
	path := openPrestackBatchPlanDialog(prestackHwnd)
	if path == "" {
		return
	}
	plan, err := prestackbatch.Load(path)
	if err != nil {
		message(prestackHwnd, "编辑批处理计划", "读取计划失败："+err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	editor, err := prestackbatch.NewEditor(plan)
	if err != nil {
		message(prestackHwnd, "编辑批处理计划", "计划无效："+err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	state := &prestackBatchEditorWindowState{owner: prestackHwnd, planPath: filepath.Clean(path), editor: editor, selected: -1}
	prestackBatchEditorWindow = state
	h, _, errNo := pCreateWindowExW.Call(WS_EX_APPWINDOW,
		uintptr(unsafe.Pointer(u16(prestackBatchEditorClass))),
		uintptr(unsafe.Pointer(u16("批处理计划编辑器"))),
		WS_OVERLAPPEDWINDOW|WS_CLIPCHILDREN|WS_VISIBLE,
		uintptr(CW_USEDEFAULT), uintptr(CW_USEDEFAULT), 840, 560,
		prestackHwnd, 0, 0, 0)
	if h == 0 {
		prestackBatchEditorWindow = nil
		message(prestackHwnd, "编辑批处理计划", fmt.Sprintf("无法创建编辑窗口：%v", errNo), MB_OK|MB_ICONERROR)
		return
	}
	state.hwnd = h
	showTopLevelWindowRestored(h)
}

func batchEditorSetStatus(text string) {
	if state := prestackBatchEditorWindow; state != nil && state.status != 0 {
		setText(state.status, text)
	}
}

func batchEditorTaskLabel(task prestackbatch.TaskSummary) string {
	kind := "Figure"
	if task.Ref.Kind == prestackbatch.JobQC {
		kind = "QC"
	}
	name := filepath.Base(task.Output)
	if task.Ref.Kind == prestackbatch.JobFigure {
		return fmt.Sprintf("%s  %s  %s", kind, task.Ref.ID, name)
	}
	return fmt.Sprintf("%s  %s  %s (%s)", kind, task.Ref.ID, name, task.Format)
}

func batchEditorRelativePath(state *prestackBatchEditorWindowState, target string) string {
	if state == nil || target == "" {
		return filepath.Base(target)
	}
	root := state.editor.Plan().OutputDirectory
	if rel, err := filepath.Rel(root, target); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return rel
	}
	return filepath.Base(target)
}

func batchEditorRefreshList(state *prestackBatchEditorWindowState, preferred int) {
	if state == nil || state.editor == nil || state.list == 0 {
		return
	}
	state.tasks = state.editor.Tasks()
	if len(state.tasks) == 0 {
		state.selected = -1
	} else {
		if preferred < 0 {
			preferred = 0
		}
		if preferred >= len(state.tasks) {
			preferred = len(state.tasks) - 1
		}
		state.selected = preferred
	}
	pSendMessageW.Call(state.list, batchEditLBResetContent, 0, 0)
	for _, task := range state.tasks {
		text := batchEditorTaskLabel(task)
		pSendMessageW.Call(state.list, batchEditLBAddString, 0, uintptr(unsafe.Pointer(u16(text))))
	}
	if state.selected >= 0 {
		pSendMessageW.Call(state.list, batchEditLBSetCurSel, uintptr(state.selected), 0)
	}
	batchEditorSyncSelection(state)
}

func batchEditorSyncSelection(state *prestackBatchEditorWindowState) {
	if state == nil {
		return
	}
	if state.selected < 0 || state.selected >= len(state.tasks) {
		setText(state.output, "")
		setText(state.result, "")
		pEnableWindow.Call(state.apply, 0)
		pEnableWindow.Call(state.up, 0)
		pEnableWindow.Call(state.down, 0)
		pEnableWindow.Call(state.remove, 0)
		return
	}
	task := state.tasks[state.selected]
	setText(state.output, batchEditorRelativePath(state, task.Output))
	if task.Ref.Kind == prestackbatch.JobFigure {
		setText(state.result, batchEditorRelativePath(state, task.Result))
		pShowWindow.Call(state.resultLbl, SW_SHOW)
		pShowWindow.Call(state.result, SW_SHOW)
		pShowWindow.Call(state.formatLbl, SW_HIDE)
		pShowWindow.Call(state.format, SW_HIDE)
	} else {
		setText(state.result, "")
		pShowWindow.Call(state.resultLbl, SW_HIDE)
		pShowWindow.Call(state.result, SW_HIDE)
		pShowWindow.Call(state.formatLbl, SW_SHOW)
		pShowWindow.Call(state.format, SW_SHOW)
		idx := uintptr(0)
		if task.Format == prestackbatch.ReportCSV {
			idx = 1
		}
		pSendMessageW.Call(state.format, CB_SETCURSEL, idx, 0)
	}
	pEnableWindow.Call(state.apply, 1)
	groupIndex := state.selected
	if task.Ref.Kind == prestackbatch.JobQC {
		groupIndex -= len(state.editor.Plan().Figures)
	}
	pEnableWindow.Call(state.up, uintptr(boolToInt(groupIndex > 0)))
	groupLen := len(state.editor.Plan().Figures)
	if task.Ref.Kind == prestackbatch.JobQC {
		groupLen = len(state.editor.Plan().QCReports)
	}
	pEnableWindow.Call(state.down, uintptr(boolToInt(groupIndex+1 < groupLen)))
	pEnableWindow.Call(state.remove, 1)
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func batchEditorApplySelected(state *prestackBatchEditorWindowState) error {
	if state == nil || state.selected < 0 || state.selected >= len(state.tasks) {
		return fmt.Errorf("请选择一个任务")
	}
	task := state.tasks[state.selected]
	output := strings.TrimSpace(getText(state.output))
	if task.Ref.Kind == prestackbatch.JobFigure {
		result := strings.TrimSpace(getText(state.result))
		return state.editor.UpdateFigureOutput(task.Ref, output, result)
	}
	format := prestackbatch.ReportJSON
	if v, _, _ := pSendMessageW.Call(state.format, CB_GETCURSEL, 0, 0); v == 1 {
		format = prestackbatch.ReportCSV
	}
	return state.editor.UpdateQCOutput(task.Ref, output, format)
}

func batchEditorSetDirectory(state *prestackBatchEditorWindowState) error {
	if state == nil {
		return fmt.Errorf("编辑器状态不可用")
	}
	return state.editor.SetOutputDirectory(strings.TrimSpace(getText(state.dir)))
}

func batchEditorMove(state *prestackBatchEditorWindowState, delta int) error {
	if state == nil || state.selected < 0 || state.selected >= len(state.tasks) {
		return fmt.Errorf("请选择一个任务")
	}
	task := state.tasks[state.selected]
	groupIndex := state.selected
	groupLen := len(state.editor.Plan().Figures)
	if task.Ref.Kind == prestackbatch.JobQC {
		groupIndex -= groupLen
		groupLen = len(state.editor.Plan().QCReports)
	}
	destination := groupIndex + delta
	if destination < 0 || destination >= groupLen {
		return nil
	}
	if err := state.editor.Move(task.Ref, destination); err != nil {
		return err
	}
	newIndex := destination
	if task.Ref.Kind == prestackbatch.JobQC {
		newIndex += len(state.editor.Plan().Figures)
	}
	batchEditorRefreshList(state, newIndex)
	return nil
}

func batchEditorRemove(state *prestackBatchEditorWindowState) error {
	if state == nil || state.selected < 0 || state.selected >= len(state.tasks) {
		return fmt.Errorf("请选择一个任务")
	}
	selected := state.selected
	if err := state.editor.Remove(state.tasks[selected].Ref); err != nil {
		return err
	}
	batchEditorRefreshList(state, selected)
	return nil
}

func batchEditorSaveAs(state *prestackBatchEditorWindowState) error {
	if state == nil || state.editor == nil {
		return fmt.Errorf("编辑器状态不可用")
	}
	path := savePrestackBatchPlanDialog(state.hwnd, filepath.Base(state.planPath)+".edited.json")
	if path == "" {
		return nil
	}
	path = filepath.Clean(path)
	if !strings.EqualFold(filepath.Ext(path), ".json") {
		path += ".json"
	}
	if strings.EqualFold(path, state.planPath) {
		return fmt.Errorf("另存为必须选择新的计划文件名")
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("目标计划已存在；编辑器不会覆盖原计划")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := state.editor.Save(path, false); err != nil {
		return err
	}
	plan, err := prestackbatch.Load(path)
	if err != nil {
		return fmt.Errorf("保存后校验失败：%w", err)
	}
	if _, err := prestackbatch.NewEditor(plan); err != nil {
		return fmt.Errorf("保存后验证计划失败：%w", err)
	}
	state.planPath = path
	setText(state.hwnd, "批处理计划编辑器 - "+filepath.Base(path))
	status := fmt.Sprintf("已另存为：%s（Figure %d，QC %d）", path, len(plan.Figures), len(plan.QCReports))
	batchEditorSetStatus(status)
	if state.owner == prestackHwnd {
		setPrestackStatus(status)
	}
	return nil
}

func prestackBatchEditorWndProc(h uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	state := prestackBatchEditorWindow
	switch msg {
	case WM_CREATE:
		if state == nil {
			return 0
		}
		state.hwnd = h
		state.list = createCtrl(h, "LISTBOX", "", WS_CHILD|WS_VISIBLE|WS_BORDER|batchEditWSVScroll|batchEditLBSNotify|batchEditLBSNoIntegralHeight, 12, 44, 470, 380, batchEditListID)
		state.dirLbl = createCtrl(h, "STATIC", "输出目录", WS_CHILD|WS_VISIBLE, 12, 12, 90, 22, batchEditLabelDirID)
		state.dir = createCtrl(h, "EDIT", "", WS_CHILD|WS_VISIBLE|WS_BORDER|WS_TABSTOP|ES_AUTOHSCROLL, 104, 8, 370, 24, batchEditDirID)
		state.outputLbl = createCtrl(h, "STATIC", "输出文件名", WS_CHILD|WS_VISIBLE, 500, 88, 100, 22, batchEditLabelOutputID)
		state.output = createCtrl(h, "EDIT", "", WS_CHILD|WS_VISIBLE|WS_BORDER|WS_TABSTOP|ES_AUTOHSCROLL, 600, 84, 210, 24, batchEditOutputID)
		state.resultLbl = createCtrl(h, "STATIC", "Figure result", WS_CHILD|WS_VISIBLE, 500, 124, 100, 22, batchEditLabelResultID)
		state.result = createCtrl(h, "EDIT", "", WS_CHILD|WS_VISIBLE|WS_BORDER|WS_TABSTOP|ES_AUTOHSCROLL, 600, 120, 210, 24, batchEditResultID)
		state.formatLbl = createCtrl(h, "STATIC", "QC 格式", WS_CHILD|WS_VISIBLE, 500, 124, 100, 22, batchEditLabelFormatID)
		state.format = batchEditorCombo(h, batchEditFormatID, []string{"JSON", "CSV"})
		pMoveWindow.Call(state.format, 600, 120, 210, 24, 1)
		state.apply = createCtrl(h, "BUTTON", "应用任务修改", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 500, 164, 150, 25, batchEditApplyID)
		state.up = createCtrl(h, "BUTTON", "上移", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 12, 432, 80, 25, batchEditUpID)
		state.down = createCtrl(h, "BUTTON", "下移", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 98, 432, 80, 25, batchEditDownID)
		state.remove = createCtrl(h, "BUTTON", "删除", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 184, 432, 80, 25, batchEditRemoveID)
		state.dirApply = createCtrl(h, "BUTTON", "应用输出目录", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 500, 8, 130, 25, batchEditDirApplyID)
		state.saveAs = createCtrl(h, "BUTTON", "另存为", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_DEFPUSHBUTTON, 646, 164, 80, 25, batchEditSaveAsID)
		state.cancel = createCtrl(h, "BUTTON", "取消", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 732, 164, 78, 25, batchEditCancelID)
		state.status = createCtrl(h, "STATIC", "", WS_CHILD|WS_VISIBLE|WS_BORDER, 12, 468, 798, 24, batchEditStatusID)
		setText(state.dir, state.editor.Plan().OutputDirectory)
		setText(state.status, "只编辑元数据；保存采用另存为，不改变原计划")
		batchEditorRefreshList(state, 0)
		return 0
	case WM_SIZE:
		if state == nil {
			return 0
		}
		batchEditorLayout(state, int(uint16(lParam)), int(uint16(lParam>>16)))
		return 0
	case WM_GETMINMAXINFO:
		if lParam != 0 {
			mmi := (*MINMAXINFO)(unsafe.Pointer(lParam))
			mmi.PtMinTrackSize.X = 700
			mmi.PtMinTrackSize.Y = 420
		}
		return 0
	case WM_COMMAND:
		if state == nil {
			return 0
		}
		id, code := int(wParam&0xffff), int(wParam>>16)
		if id == batchEditListID && code == batchEditListNotifySelChange {
			if v, _, _ := pSendMessageW.Call(state.list, batchEditLBGetCurSel, 0, 0); int(v) >= 0 {
				state.selected = int(v)
				batchEditorSyncSelection(state)
			}
			return 0
		}
		var err error
		switch id {
		case batchEditApplyID:
			err = batchEditorApplySelected(state)
			if err == nil {
				batchEditorRefreshList(state, state.selected)
			}
		case batchEditDirApplyID:
			err = batchEditorSetDirectory(state)
			if err == nil {
				batchEditorRefreshList(state, state.selected)
			}
		case batchEditUpID:
			err = batchEditorMove(state, -1)
		case batchEditDownID:
			err = batchEditorMove(state, 1)
		case batchEditRemoveID:
			err = batchEditorRemove(state)
		case batchEditSaveAsID:
			err = batchEditorSaveAs(state)
		case batchEditCancelID:
			pDestroyWindow.Call(h)
			return 0
		}
		if err != nil {
			batchEditorSetStatus("错误：" + err.Error())
			message(h, "批处理计划编辑器", err.Error(), MB_OK|MB_ICONERROR)
		} else if id == batchEditApplyID || id == batchEditDirApplyID {
			batchEditorSetStatus("修改已应用到内存；点击“另存为”保存新计划")
		}
		return 0
	case WM_CLOSE:
		pDestroyWindow.Call(h)
		return 0
	case WM_DESTROY:
		if prestackBatchEditorWindow != nil && prestackBatchEditorWindow.hwnd == h {
			prestackBatchEditorWindow = nil
		}
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(h, uintptr(msg), wParam, lParam)
	return r
}

func batchEditorCombo(parent uintptr, id int, items []string) uintptr {
	h := createCtrl(parent, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 0, 0, 140, 24, id)
	for _, item := range items {
		pSendMessageW.Call(h, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(item))))
	}
	pSendMessageW.Call(h, CB_SETCURSEL, 0, 0)
	return h
}

func batchEditorLayout(state *prestackBatchEditorWindowState, width, height int) {
	if state == nil {
		return
	}
	if width < 1 || height < 1 {
		return
	}
	leftW := width * 58 / 100
	if leftW < 360 {
		leftW = 360
	}
	rightX := leftW + 22
	rightW := width - rightX - 18
	if rightW < 240 {
		rightW = 240
	}
	listH := height - 120
	if listH < 180 {
		listH = 180
	}
	m := func(h uintptr, x, y, w, hh int) {
		pMoveWindow.Call(h, uintptr(x), uintptr(y), uintptr(w), uintptr(hh), 1)
	}
	m(state.list, 12, 44, leftW, listH)
	m(state.dirLbl, rightX, 12, 96, 22)
	m(state.dirApply, rightX+100, 8, 130, 25)
	m(state.dir, rightX, 44, rightW, 24)
	m(state.outputLbl, rightX, 88, 96, 22)
	m(state.output, rightX+100, 84, rightW-100, 24)
	m(state.resultLbl, rightX, 124, 96, 22)
	m(state.result, rightX+100, 120, rightW-100, 24)
	m(state.formatLbl, rightX, 124, 96, 22)
	m(state.format, rightX+100, 120, rightW-100, 24)
	m(state.apply, rightX, 164, 150, 25)
	m(state.saveAs, rightX+160, 164, 80, 25)
	m(state.cancel, rightX+246, 164, 78, 25)
	m(state.up, 12, listH+54, 80, 25)
	m(state.down, 98, listH+54, 80, 25)
	m(state.remove, 184, listH+54, 80, 25)
	m(state.status, 12, height-42, width-24, 24)
}
