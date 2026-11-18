//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/algorithms/registry"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/algorithms/runner"
)

type poststackAlgorithmAttempt struct {
	ID, Name        string
	Parameters      string // immutable normalized values actually sent to the runner
	Traces, Samples int
}

func newPoststackAlgorithmAttempt(module registry.Module, values map[string]any, nt, ns int) poststackAlgorithmAttempt {
	d := module.Descriptor()
	name := d.Name
	// Values were normalized before calling this; encoding also isolates the
	// log from any later changes to task parameters or parameter controls.
	raw, _ := json.MarshalIndent(values, "", "  ")
	return poststackAlgorithmAttempt{d.ID, name, string(raw), nt, ns}
}

type algorithmFailureNotice struct{ title, reason, details string }

var algorithmFailure algorithmFailureNotice

func describeAlgorithmFailure(a poststackAlgorithmAttempt, err error, retained, preflight, export bool) algorithmFailureNotice {
	if err == nil || errors.Is(err, context.Canceled) {
		return algorithmFailureNotice{}
	}
	title, reason := "处理失败", "未能生成有效结果；请查看详细日志定位原因。"
	if export {
		title, reason = "导出失败", "无法写出结果；请检查目标路径并查看详细日志。"
	}
	if a.Name != "" {
		title += " · " + a.Name
	}
	if errors.Is(err, context.DeadlineExceeded) {
		title, reason = "处理超时", "任务已达到设置的时限；可缩小处理范围或延长超时后重试。"
		if a.Name != "" {
			title += " · " + a.Name
		}
	}
	if retained {
		reason += " 仍显示原有 B 及可比差值。"
	}
	if preflight {
		title = "参数检查"
		if a.Name != "" {
			title += " · " + a.Name
		}
		reason = "存在未填写或超出范围的参数。请检查字段，详细日志列出对应错误。"
	}
	details := title + "\r\n" + reason + "\r\n"
	if a.Traces > 0 && a.Samples > 0 {
		details += fmt.Sprintf("输入范围：%d 道 × %d 样点\r\n", a.Traces, a.Samples)
	}
	if a.Parameters != "" && a.Parameters != "null" {
		details += "实际参数：\r\n" + strings.ReplaceAll(a.Parameters, "\n", "\r\n") + "\r\n"
	}
	heading := "原始错误（含退出状态，若后端提供）："
	if preflight {
		heading = "参数预检未启动进程："
	}
	raw := strings.ReplaceAll(strings.ReplaceAll(err.Error(), "\r\n", "\n"), "\n", "\r\n")
	details += heading + "\r\n" + raw
	return algorithmFailureNotice{title: title, reason: reason, details: details}
}

func visibleAlgorithmFailure() algorithmFailureNotice {
	if algorithmStatusSource == compareAPath && algorithmFailure.title != "" && cadzowCancel == nil {
		return algorithmFailure
	}
	return algorithmFailureNotice{}
}

func setPoststackAlgorithmFailure(a poststackAlgorithmAttempt, err error, retained, preflight, export bool) {
	algorithmFailure = describeAlgorithmFailure(a, err, retained, preflight, export)
	if errors.Is(err, context.Canceled) {
		setAlgorithmStatus("已取消；A 和原有 B 保持不变", 0)
		return
	}
	setAlgorithmStatus(algorithmFailure.title, 0)
	var recorded *runner.RecordedError
	if errors.As(err, &recorded) && recorded.RecordPath != "" {
		setAlgorithmLog("", recorded.RecordPath)
	}
}

func updateAlgorithmFailureUI() {
	if cz.failureTitle == 0 {
		return
	}
	notice := visibleAlgorithmFailure()
	if notice == cz.shownFailure {
		return
	}
	cz.shownFailure = notice
	setText(cz.failureTitle, notice.title)
	setText(cz.failureReason, notice.reason)
	layoutAlgorithmFailureControls()
	refreshAlgorithmLogWindow()
}

var algorithmFailureCard algorithmBox
var algorithmFailureBrush uintptr

func algorithmFailureColor() uintptr { return rgbRef(255, 239, 239) }

func layoutAlgorithmFailureControls() {
	b := algorithmFailureCard
	visible := cz.shownFailure.title != "" && b.w > 0
	algorithmShowBox(cz.status, b, !visible && b.w > 0)
	algorithmShowBox(cz.failureTitle, algorithmBox{b.x + 31, b.y + 5, maxInt(1, b.w-135), 21}, visible)
	algorithmShowBox(cz.failureReason, algorithmBox{b.x + 12, b.y + 29, maxInt(1, b.w-24), b.h - 31}, visible)
	algorithmShowBox(cz.failureLog, algorithmBox{b.x + b.w - 99, b.y + 3, 91, 24}, visible)
	if cadzowHwnd != 0 {
		r := RECT{Left: int32(algorithmPx(b.x)), Top: int32(algorithmPx(b.y)), Right: int32(algorithmPx(b.x + b.w)), Bottom: int32(algorithmPx(b.y + b.h))}
		pRedrawWindow.Call(cadzowHwnd, uintptr(unsafe.Pointer(&r)), 0, RDW_INVALIDATE|RDW_ERASE|RDW_ALLCHILDREN)
	}
}

func drawAlgorithmFailureCard(dc uintptr) {
	b := algorithmFailureCard
	if dc == 0 || cz.shownFailure.title == "" || b.w <= 0 {
		return
	}
	r := RECT{Left: int32(algorithmPx(b.x)), Top: int32(algorithmPx(b.y)), Right: int32(algorithmPx(b.x + b.w)), Bottom: int32(algorithmPx(b.y + b.h))}
	red := rgbRef(153, 33, 40)
	drawHomeRoundRect(dc, r, algorithmFailureColor(), rgbRef(243, 197, 197), algorithmPx(5))
	x, y := int(r.Left)+algorithmPx(11), int(r.Top)+algorithmPx(8)
	ic := RECT{Left: int32(x), Top: int32(y), Right: int32(x + algorithmPx(13)), Bottom: int32(y + algorithmPx(13))}
	drawHomeRoundRect(dc, ic, red, red, algorithmPx(13))
	drawHomeLine(dc, x+algorithmPx(6), y+algorithmPx(3), x+algorithmPx(6), y+algorithmPx(7), rgbRef(255, 255, 255), 1)
	drawHomeLine(dc, x+algorithmPx(6), y+algorithmPx(9), x+algorithmPx(6), y+algorithmPx(10), rgbRef(255, 255, 255), 1)
}
