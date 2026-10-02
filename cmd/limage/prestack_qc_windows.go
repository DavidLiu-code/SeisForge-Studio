//go:build windows

package main

import (
	"fmt"
	"path/filepath"
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

func prestackRangeText(r prestackcore.ValueRange) string {
	if !r.Valid {
		return "n/a"
	}
	return fmt.Sprintf("%.6g..%.6g", r.Min, r.Max)
}

func paintPrestackQC(dc uintptr) {
	r := clientRect(prestackHwnd)
	if prestackState.index == nil {
		drawAxisText(dc, "索引完成后显示道头质量统计；QC 不读取地震振幅。", 20, 125, int(r.Right)-20, 160, DT_LEFT|DT_SINGLELINE)
		return
	}
	report := prestackQCReport()
	q := report.Quality
	summary := fmt.Sprintf("文件：物理道 %d | 已索引 %d | 有效道头 %d | 无效道头 %d\n缺失：CDP %d | FFID %d | Source %d | Receiver %d | Offset %d\n质量：坐标比例异常 %d | 样点数不一致 %d | 采样间隔不一致 %d\n道集：CMP %d | Shot %d | Receiver %d | 共Offset %d（分箱 %g）\nFold：%d..%d，平均 %.2f | Offset %s | Azimuth %s\n当前：%s %s | 物理道 %d | Fold %d | Offset %s | Azimuth %s",
		q.TotalTraces, q.IndexedTraces, q.ValidHeaderTraces, q.InvalidHeaderTraces,
		q.MissingCDP, q.MissingFFID, q.MissingSource, q.MissingReceiver, q.MissingOffset,
		q.CoordinateScalarErrors, q.SampleCountInconsistent, q.SampleIntervalInconsistent,
		q.CMPCount, q.ShotCount, q.ReceiverCount, q.CommonOffsetBinCount, prestackState.offsetBinSize,
		q.Fold.Min, q.Fold.Max, q.Fold.Mean, prestackRangeText(q.OffsetRange), prestackRangeText(q.AzimuthRange),
		report.CurrentGather.Type, report.CurrentGather.Key, report.CurrentGather.PhysicalTraceCount, report.CurrentGather.Fold,
		prestackRangeText(report.CurrentGather.OffsetRange), prestackRangeText(report.CurrentGather.AzimuthRange))
	drawAxisText(dc, summary, 20, 115, int(r.Right)-20, 268, DT_LEFT|DT_WORDBREAK)
	drawAxisText(dc, "全文件分布（蓝） / 当前道集（橙）；零值不是无效值。仅统计道头元数据。", 20, 271, int(r.Right)-20, 300, DT_LEFT|DT_SINGLELINE)
	charts := []struct {
		name string
		data prestackcore.Distribution
	}{{"Fold 分布", q.FoldDistribution}, {"Offset 分布", q.OffsetDistribution}, {"Azimuth 分布", q.AzimuthDistribution}}
	// At narrow widths stack charts instead of clipping their labels.
	if r.Right < 900 {
		height := maxInt(90, (int(r.Bottom)-345)/3)
		for i, chart := range charts {
			paintPrestackDistribution(dc, chart.name, chart.data, RECT{Left: 35, Top: int32(310 + i*height), Right: r.Right - 25, Bottom: int32(305 + (i+1)*height)})
		}
	} else {
		width := (int(r.Right) - 60) / 3
		for i, chart := range charts {
			paintPrestackDistribution(dc, chart.name, chart.data, RECT{Left: int32(25 + i*width), Top: 310, Right: int32(15 + (i+1)*width), Bottom: r.Bottom - 50})
		}
	}
}

func prestackQCReport() prestackcore.QCReport {
	if prestackQCCache.index == prestackState.index && prestackQCCache.selection == prestackState.gather.Selection &&
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
