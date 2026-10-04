//go:build windows

package main

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"

	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
)

var prestackQCCache struct {
	index     *prestackcore.PrestackIndex
	selection prestackcore.GatherSelection
	count     int
	binSize   float64
	report    prestackcore.QCReport
}

const (
	prestackDTCalcRect = 0x0400
	prestackDTTop      = 0x0000
)

// drawPrestackQCText deliberately bypasses drawAxisText.  The latter is
// intended for one-line seismic labels and always adds DT_SINGLELINE, which
// prevents QC summaries from wrapping when the window becomes narrow.
func drawPrestackQCTextRect(hdc uintptr, text string, r *RECT, flags uint32) int {
	if r == nil {
		return 0
	}
	// Win32 DrawTextW treats CRLF as the portable paragraph separator.  The
	// report formatter uses LF internally, so normalize it at the drawing
	// boundary to keep measurement and the final paint identical.
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n")
	flags &^= DT_SINGLELINE
	flags |= prestackDTTop
	n, _, _ := pDrawTextW.Call(hdc, uintptr(unsafe.Pointer(u16(text))), uintptr(^uint32(0)), uintptr(unsafe.Pointer(r)), uintptr(flags))
	return int(n)
}

func drawPrestackQCText(hdc uintptr, text string, r RECT, flags uint32) int {
	return drawPrestackQCTextRect(hdc, text, &r, flags)
}

func measurePrestackQCText(hdc uintptr, text string, width int) int {
	if width < 1 {
		return 0
	}
	normalized := strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n")
	// DT_CALCRECT expands the rectangle from its initial height.  Start at a
	// small positive height and keep an explicit-line fallback so a platform
	// text renderer that returns an empty height can never collapse the report
	// into a one-pixel strip.
	r := RECT{Right: int32(width), Bottom: 1}
	flags := uint32(DT_LEFT | prestackDTTop | DT_WORDBREAK | prestackDTCalcRect)
	drawPrestackQCTextRect(hdc, normalized, &r, flags)
	height := int(r.Bottom - r.Top)
	lineHeight := 18
	minimum := (strings.Count(normalized, "\r\n") + 1) * lineHeight
	return maxInt(minimum, height)
}

// prestackQCContentRect excludes the native status/progress controls.  QC is
// painted by the parent window, so the chart area must end before those child
// windows even while the user is resizing the frame.
func prestackQCContentRect(r RECT) RECT {
	l := prestackPanelLayoutForSize(int(r.Right), int(r.Bottom), 4)
	return prestackLayoutRECT(l.Content)
}

func prestackRangeText(r prestackcore.ValueRange) string {
	if !r.Valid {
		return "n/a"
	}
	return fmt.Sprintf("%.6g..%.6g", r.Min, r.Max)
}

func paintPrestackQC(dc uintptr) {
	r := clientRect(prestackHwnd)
	content := prestackQCContentRect(r)
	saved, _, _ := pSaveDC.Call(dc)
	pIntersectClipRect.Call(dc, uintptr(content.Left), uintptr(content.Top), uintptr(content.Right), uintptr(content.Bottom))
	defer pRestoreDC.Call(dc, saved)
	if prestackState.index == nil {
		drawPrestackQCText(dc, "索引完成后显示道头质量统计；QC 不读取地震振幅。", content, DT_LEFT|prestackDTTop|DT_WORDBREAK)
		return
	}
	report := prestackQCReport()
	q := report.Quality
	currentFold := fmt.Sprintf("%d", report.CurrentGather.Fold)
	currentKey := report.CurrentGather.Key
	if report.CurrentGather.All {
		currentFold = "全文件"
		currentKey += "（全文件）"
	}
	summary := fmt.Sprintf("文件：物理道 %d | 已索引 %d | 有效道头 %d | 无效道头 %d\n缺失：CDP %d | FFID %d | Source %d | Receiver %d | Offset %d\n质量：坐标比例异常 %d | 样点数不一致 %d | 采样间隔不一致 %d\n道集：CMP %d | Shot %d | Receiver %d | 共Offset %d（分箱 %g）\nFold：%d..%d，平均 %.2f | Offset %s | Azimuth %s\n当前：%s %s | 物理道 %d | Fold %s | Offset %s | Azimuth %s",
		q.TotalTraces, q.IndexedTraces, q.ValidHeaderTraces, q.InvalidHeaderTraces,
		q.MissingCDP, q.MissingFFID, q.MissingSource, q.MissingReceiver, q.MissingOffset,
		q.CoordinateScalarErrors, q.SampleCountInconsistent, q.SampleIntervalInconsistent,
		q.CMPCount, q.ShotCount, q.ReceiverCount, q.CommonOffsetBinCount, prestackState.offsetBinSize,
		q.Fold.Min, q.Fold.Max, q.Fold.Mean, prestackRangeText(q.OffsetRange), prestackRangeText(q.AzimuthRange),
		report.CurrentGather.Type, currentKey, report.CurrentGather.PhysicalTraceCount, currentFold,
		prestackRangeText(report.CurrentGather.OffsetRange), prestackRangeText(report.CurrentGather.AzimuthRange))
	width := int(content.Right - content.Left)
	summaryHeight := measurePrestackQCText(dc, summary, width)
	summaryBottom := content.Top + int32(summaryHeight)
	if summaryBottom > content.Bottom {
		summaryBottom = content.Bottom
	}
	summaryRect := RECT{Left: content.Left, Top: content.Top, Right: content.Right, Bottom: summaryBottom}
	drawPrestackQCText(dc, summary, summaryRect, DT_LEFT|prestackDTTop|DT_WORDBREAK)
	// When the frame is too short even the wrapped summary can consume the
	// entire QC content area.  Do not construct inverted legend/chart
	// rectangles in that case; the parent background remains clean and the
	// user gets a deterministic resize hint once there is room for it.
	if summaryBottom >= content.Bottom-12 {
		return
	}
	legendY := summaryRect.Bottom + 10
	legend := "全文件分布（蓝） / 当前道集（橙）；零值不是无效值。仅统计道头元数据。"
	if report.CurrentGather.All {
		legend = "全文件分布（蓝）；当前为全文件范围，不重复叠加橙色。零值不是无效值。仅统计道头元数据。"
	}
	drawAxisText(dc, legend, int(content.Left), int(legendY), int(content.Right), int(legendY)+22, DT_LEFT|DT_SINGLELINE)
	chartTop := legendY + 30
	charts := []struct {
		name string
		data prestackcore.Distribution
	}{{"Fold 分布", q.FoldDistribution}, {"Offset 分布", q.OffsetDistribution}, {"Azimuth 分布", q.AzimuthDistribution}}
	chartBottom := content.Bottom - 25
	if chartBottom <= chartTop+60 {
		drawAxisText(dc, "窗口高度不足，请放大窗口以显示 QC 分布图。", int(content.Left), int(chartTop), int(content.Right), int(content.Bottom), DT_CENTER|DT_VCENTER|DT_SINGLELINE)
		return
	}
	// At narrow widths stack charts instead of clipping their labels.  The
	// available height is computed from the wrapped summary, so charts never
	// occupy a stale fixed area after a resize.
	if content.Right-content.Left < 880 {
		height := (int(chartBottom) - int(chartTop)) / 3
		for i, chart := range charts {
			top := int(chartTop) + i*height
			bottom := int(chartTop) + (i+1)*height - 3
			paintPrestackDistribution(dc, chart.name, chart.data, RECT{Left: content.Left + 15, Top: int32(top), Right: content.Right - 5, Bottom: int32(bottom)})
		}
	} else {
		width := (int(content.Right-content.Left) - 10) / 3
		for i, chart := range charts {
			left := int(content.Left) + i*width
			paintPrestackDistribution(dc, chart.name, chart.data, RECT{Left: int32(left), Top: chartTop, Right: int32(left + width - 8), Bottom: chartBottom})
		}
	}
}

func prestackQCReport() prestackcore.QCReport {
	if prestackQCCache.index == prestackState.index && reflect.DeepEqual(prestackQCCache.selection, prestackState.gather.Selection) &&
		prestackQCCache.count == len(prestackState.gather.TraceIndices) && prestackQCCache.binSize == prestackState.offsetBinSize {
		return prestackQCCache.report
	}
	report := prestackcore.BuildQCReport(prestackState.index, prestackState.gather)
	if prestackState.index != nil {
		report.Quality.CommonOffsetBinCount = len(prestackState.index.AvailableGathersConfigured(prestackcore.GatherOffset, prestackState.offsetBinSize))
	}
	prestackQCCache.index, prestackQCCache.selection = prestackState.index, prestackState.gather.Selection
	prestackQCCache.count, prestackQCCache.binSize = len(prestackState.gather.TraceIndices), prestackState.offsetBinSize
	prestackQCCache.report = report
	return report
}

func paintPrestackDistribution(dc uintptr, name string, d prestackcore.Distribution, r RECT) {
	if r.Bottom-r.Top < 50 || r.Right-r.Left < 70 {
		return
	}
	drawAxisText(dc, name, int(r.Left), int(r.Top), int(r.Right), int(r.Top)+23, DT_CENTER|DT_SINGLELINE)
	r.Top += 32
	r.Bottom -= 25
	if len(d.Bins) == 0 {
		drawAxisText(dc, "无有效数据", int(r.Left), int(r.Top), int(r.Right), int(r.Bottom), DT_CENTER|DT_SINGLELINE)
		return
	}
	maxCount := 1
	for _, b := range d.Bins {
		maxCount = maxInt(maxCount, maxInt(b.Count, b.CurrentCount))
	}
	saved, _, _ := pSaveDC.Call(dc)
	pIntersectClipRect.Call(dc, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right), uintptr(r.Bottom))
	blue, _, _ := pCreateSolidBrush.Call(0x00DFA060)
	orange, _, _ := pCreateSolidBrush.Call(0x0068A5ED)
	defer func() { pDeleteObject.Call(blue); pDeleteObject.Call(orange) }()
	for i, b := range d.Bins {
		left := r.Left + int32(i)*(r.Right-r.Left)/int32(len(d.Bins))
		right := r.Left + int32(i+1)*(r.Right-r.Left)/int32(len(d.Bins))
		bar := RECT{Left: left, Right: maxInt32(left+1, right-1), Top: r.Bottom - int32(b.Count)*(r.Bottom-r.Top)/int32(maxCount), Bottom: r.Bottom}
		pFillRect.Call(dc, uintptr(unsafe.Pointer(&bar)), blue)
		if b.CurrentCount > 0 {
			bar.Top = r.Bottom - int32(b.CurrentCount)*(r.Bottom-r.Top)/int32(maxCount)
			bar.Right = maxInt32(left+1, left+(right-left)/2)
			pFillRect.Call(dc, uintptr(unsafe.Pointer(&bar)), orange)
		}
	}
	pRestoreDC.Call(dc, saved)
	drawAxisText(dc, fmt.Sprintf("%.5g", d.Min), int(r.Left), int(r.Bottom)+3, int(r.Left)+110, int(r.Bottom)+23, DT_LEFT|DT_SINGLELINE)
	drawAxisText(dc, fmt.Sprintf("%.5g", d.Max), int(r.Right)-110, int(r.Bottom)+3, int(r.Right), int(r.Bottom)+23, DT_RIGHT|DT_SINGLELINE)
}

func exportPrestackQC() {
	if prestackState.index == nil {
		setPrestackStatus("索引尚未完成，无法导出 QC。")
		return
	}
	buf := make([]uint16, 32768)
	filter := utf16.Encode([]rune("CSV 报告 (*.csv)\x00*.csv\x00JSON 报告 (*.json)\x00*.json\x00\x00"))
	nameSlice := utf16.Encode([]rune(strings.TrimSuffix(filepath.Base(prestackState.dataset.Path), filepath.Ext(prestackState.dataset.Path)) + "_qc.csv"))
	nameSlice = append(nameSlice, 0)
	copy(buf, nameSlice)
	of := OPENFILENAME{LStructSize: uint32(unsafe.Sizeof(OPENFILENAME{})), HwndOwner: prestackHwnd, NFilterIndex: 1, LpstrFilter: uintptr(unsafe.Pointer(&filter[0])), LpstrFile: uintptr(unsafe.Pointer(&buf[0])), NMaxFile: uint32(len(buf)), Flags: OFN_EXPLORER | OFN_OVERWRITEPROMPT, LpstrDefExt: uintptr(unsafe.Pointer(u16("csv")))}
	ok, _, _ := pGetSaveFileNameW.Call(uintptr(unsafe.Pointer(&of)))
	if ok == 0 {
		return
	}
	path := syscall.UTF16ToString(buf)
	if err := prestackQCReport().ExportQCReport(path); err != nil {
		message(prestackHwnd, "QC 导出", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	setPrestackStatus("QC 报告已导出：" + path)
}
