//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/prestackbatch"
	prestacksession "github.com/DavidLiu-code/SeisForge-Studio/internal/prestacksession"
)

type prestackBatchDoneResult struct {
	owner      uintptr
	ownerToken int64
	generation int64
	plan       string
	err        error
}

var prestackBatchDone = make(chan prestackBatchDoneResult, 4)

// savePrestackBatchPlan captures the current A/session state beside the plan,
// publishes a QC-only plan immediately, and then lets the background figure
// stage atomically add the current Gather/Compare recipe when it is valid.
func savePrestackBatchPlan() {
	if prestackState.page != 0 && prestackState.page != 3 {
		message(prestackHwnd, "保存批处理计划", "请在 Gather 或 Compare 页面保存批处理计划。", MB_OK|MB_ICONINFORMATION)
		return
	}
	if prestackState.dataset == nil || prestackState.index == nil {
		message(prestackHwnd, "保存批处理计划", "索引尚未完成，当前没有可保存的叠前道集。", MB_OK|MB_ICONINFORMATION)
		return
	}
	planPath := savePrestackBatchPlanDialog(prestackHwnd, prestackExportStem()+"_batch.json")
	if planPath == "" {
		return
	}
	planPath = filepath.Clean(planPath)
	if !strings.EqualFold(filepath.Ext(planPath), ".json") {
		planPath += ".json"
	}
	root := filepath.Dir(planPath)
	stem := strings.TrimSuffix(filepath.Base(planPath), filepath.Ext(planPath))
	sessionPath := filepath.Join(root, stem+".session.json")
	for _, output := range []string{filepath.Join(root, stem+".figure"), filepath.Join(root, stem+".png"), filepath.Join(root, stem+".figure.result.json"), filepath.Join(root, stem+"_qc.json")} {
		if _, err := os.Lstat(output); err == nil {
			message(prestackHwnd, "保存批处理计划", "计划输出已存在，请选择新的文件名："+filepath.Base(output), MB_OK|MB_ICONINFORMATION)
			return
		} else if !os.IsNotExist(err) {
			message(prestackHwnd, "保存批处理计划", err.Error(), MB_OK|MB_ICONERROR)
			return
		}
	}
	if _, err := os.Lstat(planPath); err == nil {
		message(prestackHwnd, "保存批处理计划", "目标计划已存在；请选择新的文件名。", MB_OK|MB_ICONINFORMATION)
		return
	} else if !os.IsNotExist(err) {
		message(prestackHwnd, "保存批处理计划", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	if _, err := os.Lstat(sessionPath); err == nil {
		message(prestackHwnd, "保存批处理计划", "目标流程快照已存在；请选择新的计划文件名。", MB_OK|MB_ICONINFORMATION)
		return
	} else if !os.IsNotExist(err) {
		message(prestackHwnd, "保存批处理计划", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	state, err := capturePrestackSessionState()
	if err != nil {
		message(prestackHwnd, "保存批处理计划", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	snapshot, err := prestacksession.CapturePrestackSessionSnapshot(state)
	if err != nil {
		message(prestackHwnd, "保存批处理计划", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	if err := prestacksession.Save(sessionPath, snapshot, false); err != nil {
		message(prestackHwnd, "保存批处理计划", "保存流程快照失败："+err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	draft := prestackbatch.NewDraft(APP_VERSION, root)
	draft.AddQC(sessionPath, stem+"_qc.json", prestackbatch.ReportJSON)
	plan, err := draft.Build()
	if err != nil {
		_ = os.Remove(sessionPath)
		message(prestackHwnd, "保存批处理计划", "生成计划失败："+err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	if err := prestackbatch.Save(planPath, plan, false); err != nil {
		_ = os.Remove(sessionPath)
		message(prestackHwnd, "保存批处理计划", "保存计划失败："+err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	if startPrestackBatchPlanFigure(planPath, sessionPath, root, stem) {
		return
	}
	setPrestackStatus(fmt.Sprintf("批处理计划已保存：%s（当前道集 QC）", planPath))
}

func savePrestackBatchPlanDialog(owner uintptr, defaultName string) string {
	buf := make([]uint16, 32768)
	copy(buf, syscall.StringToUTF16(defaultName))
	filter := utf16.Encode([]rune("叠前批处理计划 (*.json)\x00*.json\x00所有文件 (*.*)\x00*.*\x00\x00"))
	of := OPENFILENAME{LStructSize: uint32(unsafe.Sizeof(OPENFILENAME{})), HwndOwner: owner,
		NFilterIndex: 1, LpstrFilter: uintptr(unsafe.Pointer(&filter[0])), LpstrFile: uintptr(unsafe.Pointer(&buf[0])),
		NMaxFile: uint32(len(buf)), Flags: OFN_EXPLORER | OFN_OVERWRITEPROMPT, LpstrDefExt: uintptr(unsafe.Pointer(u16("json")))}
	if ok, _, _ := pGetSaveFileNameW.Call(uintptr(unsafe.Pointer(&of))); ok != 0 {
		return syscall.UTF16ToString(buf)
	}
	return ""
}

func openPrestackBatchPlanDialog(owner uintptr) string {
	buf := make([]uint16, 32768)
	filter := utf16.Encode([]rune("叠前批处理计划 (*.json)\x00*.json\x00所有文件 (*.*)\x00*.*\x00\x00"))
	of := OPENFILENAME{LStructSize: uint32(unsafe.Sizeof(OPENFILENAME{})), HwndOwner: owner,
		NFilterIndex: 1, LpstrFilter: uintptr(unsafe.Pointer(&filter[0])), LpstrFile: uintptr(unsafe.Pointer(&buf[0])),
		NMaxFile: uint32(len(buf)), Flags: OFN_EXPLORER | OFN_FILEMUSTEXIST, LpstrDefExt: uintptr(unsafe.Pointer(u16("json")))}
	if ok, _, _ := pGetOpenFileNameW.Call(uintptr(unsafe.Pointer(&of))); ok != 0 {
		return syscall.UTF16ToString(buf)
	}
	return ""
}

// runPrestackBatchPlanFromDialog is a UI-thread toggle. The executor owns
// all readers and temporary outputs; the window only receives a final result
// tagged with owner/generation so a closed or replaced workspace cannot be
// updated by an old worker.
func runPrestackBatchPlanFromDialog() {
	if prestackState.batchCancel != nil {
		cancelPrestackBatchRun()
		setPrestackStatus("已请求取消批处理；正在等待当前任务释放资源…")
		return
	}
	path := openPrestackBatchPlanDialog(prestackHwnd)
	if path == "" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	gen := atomic.AddInt64(&prestackGeneration, 1)
	owner, ownerToken := prestackHwnd, prestackState.ownerToken
	prestackState.batchCancel, prestackState.batchGeneration = cancel, gen
	setText(prestackUI.batchRun, "取消批处理")
	setPrestackStatus("正在执行批处理计划：" + path)
	go func() {
		err := runPrestackBatchPlan(ctx, prestackBatchCLIOptions{plan: path})
		result := prestackBatchDoneResult{owner: owner, ownerToken: ownerToken, generation: gen, plan: path, err: err}
		select {
		case prestackBatchDone <- result:
		default:
		}
		if owner != 0 {
			pPostMessageW.Call(owner, WM_PRESTACK_BATCH_DONE, uintptr(gen), 0)
		}
	}()
}

func cancelPrestackBatchRun() {
	if prestackState.batchCancel != nil {
		prestackState.batchCancel()
		prestackState.batchCancel = nil
		prestackState.batchGeneration = atomic.AddInt64(&prestackGeneration, 1)
		if prestackUI.batchRun != 0 {
			setText(prestackUI.batchRun, "执行批处理计划")
		}
	}
}

func receivePrestackBatchDone() {
	for {
		select {
		case result := <-prestackBatchDone:
			if result.owner != prestackHwnd || result.ownerToken != prestackState.ownerToken || result.generation != prestackState.batchGeneration {
				continue
			}
			prestackState.batchCancel = nil
			setText(prestackUI.batchRun, "执行批处理计划")
			if result.err != nil {
				setPrestackStatus("批处理失败：" + result.err.Error())
			} else {
				setPrestackStatus("批处理完成：" + result.plan)
			}
		default:
			return
		}
	}
}
