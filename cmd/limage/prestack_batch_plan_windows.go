//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/prestackbatch"
	prestacksession "github.com/DavidLiu-code/SeisForge-Studio/internal/prestacksession"
)

// savePrestackBatchPlan captures the current A/session state into a small
// snapshot beside the plan, then creates a metadata-only QC job. Figure jobs
// continue to use the existing "生成绘图代码" recipe capture button; a later
// plan can include those recipe.json files together with this QC job.
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
