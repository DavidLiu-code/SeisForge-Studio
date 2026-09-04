//go:build windows

package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

const (
	IDL_BROWSE   = 4001
	IDL_FILELIST = 4002
	IDL_HDSUN    = 4003
	IDL_NOHDSUN  = 4004
	IDL_HDPC     = 4005
	IDL_NOHDPC   = 4006
	IDL_BINARY   = 4007
	IDL_BEGTRACE = 4008
	IDL_ENDTRACE = 4009
	IDL_SAMPLES  = 4010
	IDL_DT       = 4011
	IDL_INTERVAL = 4012
	IDL_GAIN     = 4013
	IDL_BEGTIME  = 4014
	IDL_ENDTIME  = 4015
	IDL_POST     = 4016
	IDL_PRE      = 4017
	IDL_TIME     = 4018
	IDL_DEPTH    = 4019
	IDL_OK       = 4020
	IDL_CANCEL   = 4021
)

type loadControls struct {
	input, browse, fileList                   uintptr
	hdsun, nohdsun, hdpc, nohdpc, binary      uintptr
	begTrace, endTrace, samples, dt, interval uintptr
	gain, begTime, endTime                    uintptr
	post, pre, time, depth                    uintptr
	ok, cancel                                uintptr
}

var (
	loadHwnd   uintptr
	loadOwner  uintptr
	lc         loadControls
	probedPath string
	probedInfo segy.Info
	probeValid bool
)

func openDataDialog(owner uintptr) string {
	buf := make([]uint16, 32768)
	// Keep the original "all files" behavior but make SEG-Y convenient.
	s := "SEG-Y 文件 (*.sgy;*.segy)\x00*.sgy;*.segy\x00所有文件 (*.*)\x00*.*\x00\x00"
	fil := make([]uint16, 0, len(s))
	for _, c := range s {
		fil = append(fil, uint16(c))
	}
	ofn := OPENFILENAME{LStructSize: uint32(unsafe.Sizeof(OPENFILENAME{})), HwndOwner: owner,
		LpstrFilter: uintptr(unsafe.Pointer(&fil[0])), NFilterIndex: 1,
		LpstrFile: uintptr(unsafe.Pointer(&buf[0])), NMaxFile: uint32(len(buf)),
		Flags: OFN_EXPLORER | OFN_FILEMUSTEXIST | OFN_HIDEREADONLY}
	r, _, _ := pGetOpenFileNameW.Call(uintptr(unsafe.Pointer(&ofn)))
	if r == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf)
}

func setRadio(h uintptr, checked bool) {
	v := uintptr(0)
	if checked {
		v = BST_CHECKED
	}
	pSendMessageW.Call(h, BM_SETCHECK, v, 0)
}

func formatNumber(v float64) string {
	if math.Abs(v-math.Round(v)) < 1e-9 {
		return fmt.Sprintf("%.0f", v)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// probeLoadPath deliberately reads only the SEG-Y binary header and file metadata.
// No seismic samples are decoded here, so range selection happens before expensive I/O.
func probeLoadPath(path string, reportError bool) bool {
	f, err := segy.Open(path)
	if err != nil {
		probeValid = false
		probedPath = ""
		if reportError {
			message(loadHwnd, "数据加载", "无法识别为带卷头的 SEG-Y 文件：\n\n"+err.Error()+"\n\n当前 x64 版本首先恢复“有卷头工作站/微机格式”；无卷头与纯二进制格式将在下一阶段接入。", MB_OK|MB_ICONERROR)
		}
		return false
	}
	info := f.Info
	f.Close()
	probedPath = path
	probedInfo = info
	probeValid = true

	setText(lc.input, path)
	setText(lc.begTrace, "1")
	setText(lc.endTrace, strconv.FormatInt(info.TraceCount, 10))
	setText(lc.samples, strconv.Itoa(info.SamplesPerTrace))
	dtMS := float64(info.SampleIntervalUS) / 1000.0
	setText(lc.dt, formatNumber(dtMS))
	setText(lc.interval, "0")
	// Preserve the current display gain when browsing another file. The
	// original default remains 0% on first launch.
	setText(lc.gain, formatNumber(gainPercent))
	setText(lc.begTime, "0")
	setText(lc.endTime, formatNumber(float64(info.SamplesPerTrace-1)*dtMS))

	// Original terminology: workstation = big endian; PC = little endian.
	setRadio(lc.hdsun, info.Endian == segy.Big)
	setRadio(lc.nohdsun, false)
	setRadio(lc.hdpc, info.Endian == segy.Little)
	setRadio(lc.nohdpc, false)
	setRadio(lc.binary, false)
	return true
}

// restoreCurrentLoadSelection repopulates the load dialog from the section that
// is currently cached/displayed. Re-clicking Open therefore behaves like the
// original workstation tool: it starts from the previous file and range rather
// than clearing the form.
func restoreCurrentLoadSelection() bool {
	if sf == nil {
		return false
	}
	info := sf.Info
	probedPath = info.Path
	probedInfo = info
	probeValid = true
	setText(lc.input, info.Path)
	setText(lc.begTrace, strconv.FormatInt(traceStart+1, 10))
	end := traceEnd
	if end < 0 || end >= info.TraceCount {
		end = info.TraceCount - 1
	}
	setText(lc.endTrace, strconv.FormatInt(end+1, 10))
	setText(lc.samples, strconv.Itoa(info.SamplesPerTrace))
	dtMS := float64(info.SampleIntervalUS) / 1000.0
	setText(lc.dt, formatNumber(dtMS))
	step := traceStep
	if step < 1 {
		step = 1
	}
	setText(lc.interval, strconv.FormatInt(step-1, 10))
	setText(lc.gain, formatNumber(gainPercent))
	s0 := sampleStart
	if s0 < 0 {
		s0 = 0
	}
	s1 := sampleEnd
	if s1 < 0 || s1 >= info.SamplesPerTrace {
		s1 = info.SamplesPerTrace - 1
	}
	setText(lc.begTime, formatNumber(float64(s0)*dtMS))
	setText(lc.endTime, formatNumber(float64(s1)*dtMS))
	setRadio(lc.hdsun, info.Endian == segy.Big)
	setRadio(lc.nohdsun, false)
	setRadio(lc.hdpc, info.Endian == segy.Little)
	setRadio(lc.nohdpc, false)
	setRadio(lc.binary, false)
	return true
}

func createLabel(parent uintptr, text string, x, y, w, h int) uintptr {
	return createCtrl(parent, "STATIC", text, WS_CHILD|WS_VISIBLE, x, y, w, h, 0)
}

func createLoadUI(h uintptr) {
	// Coordinates reconstructed from TypeForm_original.dfm (393 x 374 client pixels).
	createCtrl(h, "BUTTON", "输入文件", WS_CHILD|WS_VISIBLE|BS_GROUPBOX, 16, 16, 361, 51, 0)
	lc.input = createCtrl(h, "EDIT", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|WS_BORDER|ES_AUTOHSCROLL, 24, 37, 283, 22, 0)
	lc.browse = createCtrl(h, "BUTTON", "浏览", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 310, 36, 37, 23, IDL_BROWSE)
	lc.fileList = createCtrl(h, "BUTTON", "↓", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 348, 36, 17, 23, IDL_FILELIST)

	createCtrl(h, "BUTTON", "类型选择", WS_CHILD|WS_VISIBLE|BS_GROUPBOX, 16, 80, 361, 249, 0)
	lc.hdsun = createCtrl(h, "BUTTON", "有卷头工作站格式", WS_CHILD|WS_VISIBLE|WS_TABSTOP|WS_GROUP|BS_AUTORADIOBUTTON, 32, 104, 137, 18, IDL_HDSUN)
	lc.nohdsun = createCtrl(h, "BUTTON", "无卷头工作站格式", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_AUTORADIOBUTTON, 32, 130, 137, 18, IDL_NOHDSUN)
	lc.hdpc = createCtrl(h, "BUTTON", "有卷头微机格式", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_AUTORADIOBUTTON, 32, 157, 137, 18, IDL_HDPC)
	lc.nohdpc = createCtrl(h, "BUTTON", "无卷头微机格式", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_AUTORADIOBUTTON, 32, 183, 137, 18, IDL_NOHDPC)
	lc.binary = createCtrl(h, "BUTTON", "二进制数据文件", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_AUTORADIOBUTTON, 32, 210, 137, 18, IDL_BINARY)

	// Right-hand geometry/range fields.
	createLabel(h, "起始道：", 216, 106, 56, 18)
	lc.begTrace = createCtrl(h, "EDIT", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|WS_BORDER|ES_AUTOHSCROLL, 272, 103, 73, 22, IDL_BEGTRACE)
	createLabel(h, "终止道：", 216, 132, 56, 18)
	lc.endTrace = createCtrl(h, "EDIT", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|WS_BORDER|ES_AUTOHSCROLL, 272, 129, 73, 22, IDL_ENDTRACE)
	createLabel(h, "采样点：", 216, 159, 56, 18)
	lc.samples = createCtrl(h, "EDIT", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|WS_BORDER|ES_AUTOHSCROLL, 272, 156, 73, 22, IDL_SAMPLES)
	createLabel(h, "采样率：", 216, 185, 56, 18)
	lc.dt = createCtrl(h, "EDIT", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|WS_BORDER|ES_AUTOHSCROLL, 272, 182, 73, 22, IDL_DT)
	createLabel(h, "毫秒", 347, 185, 30, 18)
	createLabel(h, "间隔道：", 216, 212, 56, 18)
	lc.interval = createCtrl(h, "EDIT", "0", WS_CHILD|WS_VISIBLE|WS_TABSTOP|WS_BORDER|ES_AUTOHSCROLL, 272, 209, 73, 22, IDL_INTERVAL)

	// Lower left: post/pre stack and time/depth, matching the original grouping.
	createCtrl(h, "BUTTON", "", WS_CHILD|WS_VISIBLE|BS_GROUPBOX, 24, 244, 161, 37, 0)
	lc.post = createCtrl(h, "BUTTON", "叠后记录", WS_CHILD|WS_VISIBLE|WS_TABSTOP|WS_GROUP|BS_AUTORADIOBUTTON, 32, 258, 65, 18, IDL_POST)
	lc.pre = createCtrl(h, "BUTTON", "叠前记录", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_AUTORADIOBUTTON, 112, 258, 65, 18, IDL_PRE)
	setRadio(lc.post, true)

	createCtrl(h, "BUTTON", "", WS_CHILD|WS_VISIBLE|BS_GROUPBOX, 24, 283, 161, 37, 0)
	lc.time = createCtrl(h, "BUTTON", "时间域", WS_CHILD|WS_VISIBLE|WS_TABSTOP|WS_GROUP|BS_AUTORADIOBUTTON, 32, 297, 64, 18, IDL_TIME)
	lc.depth = createCtrl(h, "BUTTON", "深度域", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_AUTORADIOBUTTON, 112, 297, 64, 18, IDL_DEPTH)
	setRadio(lc.time, true)

	createLabel(h, "数据增益：", 204, 250, 68, 18)
	lc.gain = createCtrl(h, "EDIT", "0", WS_CHILD|WS_VISIBLE|WS_TABSTOP|WS_BORDER|ES_AUTOHSCROLL, 272, 247, 73, 22, IDL_GAIN)
	createLabel(h, "%", 350, 250, 18, 18)
	createLabel(h, "起始时间：", 204, 277, 68, 18)
	lc.begTime = createCtrl(h, "EDIT", "0", WS_CHILD|WS_VISIBLE|WS_TABSTOP|WS_BORDER|ES_AUTOHSCROLL, 272, 274, 73, 22, IDL_BEGTIME)
	createLabel(h, "毫秒", 347, 277, 30, 18)
	createLabel(h, "终止时间：", 204, 304, 68, 18)
	lc.endTime = createCtrl(h, "EDIT", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|WS_BORDER|ES_AUTOHSCROLL, 272, 301, 73, 22, IDL_ENDTIME)
	createLabel(h, "毫秒", 347, 304, 30, 18)

	lc.ok = createCtrl(h, "BUTTON", "✓  确定", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_DEFPUSHBUTTON, 80, 341, 81, 27, IDL_OK)
	lc.cancel = createCtrl(h, "BUTTON", "✕  取消", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 248, 341, 81, 27, IDL_CANCEL)
}

func showLoadDialog(initialPath string) {
	if loadHwnd != 0 {
		pSetForeground.Call(loadHwnd)
		if initialPath != "" {
			probeLoadPath(initialPath, true)
		}
		return
	}
	loadOwner = hwnd
	if compareHwnd != 0 {
		loadOwner = compareHwnd
	}
	pEnableWindow.Call(loadOwner, 0)
	// Outer size chosen so the client area is approximately the original 393x374 at 96 DPI.
	h, _, _ := pCreateWindowExW.Call(1, uintptr(unsafe.Pointer(u16("Limage64Load"))), uintptr(unsafe.Pointer(u16("数据加载"))),
		WS_POPUP|WS_CAPTION|WS_SYSMENU, uintptr(CW_USEDEFAULT), uintptr(CW_USEDEFAULT), 409, 413, loadOwner, 0, 0, 0)
	if h == 0 {
		pEnableWindow.Call(loadOwner, 1)
		return
	}
	loadHwnd = h
	lc = loadControls{}
	probeValid = false
	probedPath = ""
	createLoadUI(h)
	acceptSegyDrops(h)
	pShowWindow.Call(h, SW_SHOW)
	pUpdateWindow.Call(h)
	if initialPath != "" {
		// If the requested path is already displayed, restore its exact cached
		// range instead of resetting the dialog to the full file.
		if sf != nil && sf.Info.Path == initialPath {
			restoreCurrentLoadSelection()
		} else {
			setText(lc.input, initialPath)
			probeLoadPath(initialPath, true)
		}
	} else {
		// Re-click Open: show the current cached file/range as the starting point.
		restoreCurrentLoadSelection()
	}
}

func parseIntField(h uintptr, name string) (int64, bool) {
	v, err := strconv.ParseInt(strings.TrimSpace(getText(h)), 10, 64)
	if err != nil {
		message(loadHwnd, "数据加载", name+" 必须是整数。", MB_OK|MB_ICONERROR)
		return 0, false
	}
	return v, true
}

func parseFloatField(h uintptr, name string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(getText(h)), 64)
	if err != nil {
		message(loadHwnd, "数据加载", name+" 必须是数值。", MB_OK|MB_ICONERROR)
		return 0, false
	}
	return v, true
}

func selectedSupportedHeaderType() bool {
	return isChecked(lc.hdsun) || isChecked(lc.hdpc)
}

// applyLoadSelection converts the original dialog's 1-based trace range and
// millisecond time range into zero-based byte windows before any sample data is read.
func applyLoadSelection() bool {
	path := strings.TrimSpace(getText(lc.input))
	if path == "" {
		message(loadHwnd, "数据加载", "请先选择输入文件。", MB_OK|MB_ICONINFORMATION)
		return false
	}
	if !probeValid || probedPath != path {
		if !probeLoadPath(path, true) {
			return false
		}
	}
	if !selectedSupportedHeaderType() {
		message(loadHwnd, "数据加载", "该原版输入类型的界面已恢复，但当前 v3 的计算内核先支持“有卷头工作站格式”和“有卷头微机格式”。\n\n请选择带卷头格式；无卷头/纯二进制读取会继续按原版补齐。", MB_OK|MB_ICONINFORMATION)
		return false
	}

	beg, ok := parseIntField(lc.begTrace, "起始道")
	if !ok {
		return false
	}
	end, ok := parseIntField(lc.endTrace, "终止道")
	if !ok {
		return false
	}
	sampleCount, ok := parseIntField(lc.samples, "采样点")
	if !ok {
		return false
	}
	interval, ok := parseIntField(lc.interval, "间隔道")
	if !ok {
		return false
	}
	dtMS, ok := parseFloatField(lc.dt, "采样率")
	if !ok {
		return false
	}
	gainInt, ok := parseIntField(lc.gain, "数据增益")
	if !ok {
		return false
	}
	gain := float64(gainInt)
	t0, ok := parseFloatField(lc.begTime, "起始时间")
	if !ok {
		return false
	}
	t1, ok := parseFloatField(lc.endTime, "终止时间")
	if !ok {
		return false
	}

	if beg < 1 || end < beg || end > probedInfo.TraceCount {
		message(loadHwnd, "数据加载", fmt.Sprintf("道范围应满足 1 ≤ 起始道 ≤ 终止道 ≤ %d。", probedInfo.TraceCount), MB_OK|MB_ICONERROR)
		return false
	}
	if interval < 0 {
		message(loadHwnd, "数据加载", "间隔道不能小于 0。0 表示连续读取。", MB_OK|MB_ICONERROR)
		return false
	}
	if sampleCount < 1 || sampleCount > int64(probedInfo.SamplesPerTrace) {
		message(loadHwnd, "数据加载", fmt.Sprintf("采样点应在 1–%d 之间。", probedInfo.SamplesPerTrace), MB_OK|MB_ICONERROR)
		return false
	}
	if dtMS <= 0 {
		message(loadHwnd, "数据加载", "采样率必须大于 0。", MB_OK|MB_ICONERROR)
		return false
	}
	if gainInt < 0 || gainInt > 49 {
		message(loadHwnd, "数据加载", "数据增益应在 0–49% 之间。原版中 Q 增加 1%，W 减少 1%。", MB_OK|MB_ICONERROR)
		return false
	}
	maxTime := float64(sampleCount-1) * dtMS
	if t0 < 0 || t1 < t0 || t0 > maxTime {
		message(loadHwnd, "数据加载", fmt.Sprintf("时间范围应满足 0 ≤ 起始时间 ≤ 终止时间 ≤ %.6g ms。", maxTime), MB_OK|MB_ICONERROR)
		return false
	}
	if t1 > maxTime {
		t1 = maxTime
	}

	sm0 := int(math.Round(t0 / dtMS))
	sm1 := int(math.Round(t1 / dtMS))
	if sm0 < 0 {
		sm0 = 0
	}
	if sm1 >= int(sampleCount) {
		sm1 = int(sampleCount) - 1
	}
	if sm1 < sm0 {
		message(loadHwnd, "数据加载", "按当前采样率换算后，时间窗为空。", MB_OK|MB_ICONERROR)
		return false
	}

	// Transactional load: keep the currently displayed section alive until the
	// new file/range has opened AND rendered successfully. If anything fails,
	// restore the previous cached display parameters without clearing the image.
	oldTraceStart, oldTraceEnd, oldTraceStep := traceStart, traceEnd, traceStep
	oldSampleStart, oldSampleEnd := sampleStart, sampleEnd
	oldGain, oldUseLimits := gainPercent, useLimits

	traceStart = beg - 1
	traceEnd = end - 1
	traceStep = interval + 1 // 原版“间隔道=0”表示逐道读取。
	sampleStart = sm0
	sampleEnd = sm1
	gainPercent = gain
	useLimits = false

	if !loadSelectedFile(path) {
		traceStart, traceEnd, traceStep = oldTraceStart, oldTraceEnd, oldTraceStep
		sampleStart, sampleEnd = oldSampleStart, oldSampleEnd
		gainPercent, useLimits = oldGain, oldUseLimits
		return false
	}
	// This accepted range is the new “Origin”. Subsequent zoom operations can
	// be restored without ever expanding back to the full SEG-Y volume.
	storeOriginRange()
	setZoomMode(false)
	workspaceSyncAFromMain()
	completeWorkspaceOpen()
	return true
}

func loadSelectedFile(path string) bool {
	f, err := segy.Open(path)
	if err != nil {
		message(loadHwnd, APP_NAME+" - Open error", err.Error(), MB_OK|MB_ICONERROR)
		return false
	}

	// Do not close or replace the current file yet. Render the pending selection
	// first; this preserves the previous cached seismic section on cancel/error.
	oldSF := sf
	sf = f
	if !rerender() {
		sf = oldSF
		f.Close()
		return false
	}
	if oldSF != nil && oldSF != f {
		oldSF.Close()
	}

	setMainTitle(fmt.Sprintf("%s  v%s  [x64] - %s", APP_NAME, APP_VERSION, path))
	dtMS := float64(f.Info.SampleIntervalUS) / 1000.0
	t0 := float64(sampleStart) * dtMS
	t1 := float64(sampleEnd) * dtMS
	setStatusBase(fmt.Sprintf(" Traces %d-%d  step %d   Time %.3g-%.3g ms   Gain %.0f%%   %s", traceStart+1, traceEnd+1, traceStep, t0, t1, gainPercent, paletteNames[paletteIndex]))
	enableDataControls(true)
	return true
}

func loadWndProc(h uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case WM_DROPFILES:
		paths := droppedSegyPaths(wParam)
		if len(paths) > 0 {
			setText(lc.input, paths[0])
			probeLoadPath(paths[0], true)
		}
		return 0
	case WM_COMMAND:
		id := int(wParam & 0xffff)
		switch id {
		case IDL_BROWSE, IDL_FILELIST:
			if p := openDataDialog(h); p != "" {
				setText(lc.input, p)
				probeLoadPath(p, true)
			}
		case IDL_OK:
			if applyLoadSelection() {
				pDestroyWindow.Call(h)
			}
		case IDL_CANCEL:
			clearPendingWorkspaceMode()
			pDestroyWindow.Call(h)
		}
		return 0
	case WM_CLOSE:
		clearPendingWorkspaceMode()
		pDestroyWindow.Call(h)
		return 0
	case WM_DESTROY:
		loadHwnd = 0
		lc = loadControls{}
		probeValid = false
		probedPath = ""
		if loadOwner != 0 {
			pEnableWindow.Call(loadOwner, 1)
			pSetForeground.Call(loadOwner)
		}
		loadOwner = 0
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(h, uintptr(msg), wParam, lParam)
	return r
}
