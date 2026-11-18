//go:build windows

package main

import (
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

const (
	IDVX_A                    = 7101
	IDVX_B                    = 7102
	IDVX_D                    = 7103
	IDVX_IL0                  = 7104
	IDVX_IL1                  = 7105
	IDVX_XL0                  = 7106
	IDVX_XL1                  = 7107
	IDVX_T0                   = 7108
	IDVX_T1                   = 7109
	IDVX_OK                   = 7110
	IDVX_CANCEL               = 7111
	WM_VOLUME_EXPORT_PROGRESS = WM_USER + 303
)

type volumeExportControls struct {
	a, b, d            uintptr
	il0, il1, xl0, xl1 uintptr
	t0, t1             uintptr
	ok, cancel         uintptr
}

type volumeExportResult struct {
	path    string
	kind    string
	traces  int
	samples int
	err     error
}

type volumeCoordKey struct{ il, xl int32 }

var (
	volumeExportHwnd     uintptr
	vex                  volumeExportControls
	volumeExportResultCh = make(chan volumeExportResult, 1)
)

func volumeExportKind() int {
	if vex.d != 0 && isChecked(vex.d) {
		return 2
	}
	if vex.b != 0 && isChecked(vex.b) {
		return 1
	}
	return 0
}

func volumeExportScene(kind int) (volumeSceneState, bool) {
	if kind == 1 {
		if volumeScenes[1].valid {
			return volumeScenes[1], true
		}
		return volumeSceneState{}, false
	}
	if volumeScenes[0].valid {
		return volumeScenes[0], true
	}
	if volumeF != nil {
		return captureVolumeSceneState(), true
	}
	return volumeSceneState{}, false
}

func setVolumeExportDefaults(kind int) {
	st, ok := volumeExportScene(kind)
	if !ok || st.g == nil || st.f == nil {
		return
	}
	b := st.g.Bounds()
	setText(vex.il0, fmt.Sprintf("%d", b.InlineMin))
	setText(vex.il1, fmt.Sprintf("%d", b.InlineMax))
	setText(vex.xl0, fmt.Sprintf("%d", b.CrosslineMin))
	setText(vex.xl1, fmt.Sprintf("%d", b.CrosslineMax))
	sm0, sm1 := st.sampleMin, st.sampleMax
	if sm0 < 0 {
		sm0 = 0
	}
	if sm1 < sm0 || sm1 >= st.f.Info.SamplesPerTrace {
		sm1 = st.f.Info.SamplesPerTrace - 1
	}
	dt := float64(st.f.Info.SampleIntervalUS) / 1000.0
	setText(vex.t0, formatNumber(float64(sm0)*dt))
	setText(vex.t1, formatNumber(float64(sm1)*dt))
}

func showVolumeExportDialog() {
	if volumeExportHwnd != 0 {
		pSetForeground.Call(volumeExportHwnd)
		return
	}
	if volumeF == nil || volumeG == nil {
		message(volumeHwnd, "导出三维范围", "当前没有可导出的三维数据。", MB_OK|MB_ICONINFORMATION)
		return
	}
	h, _, _ := pCreateWindowExW.Call(1, uintptr(unsafe.Pointer(u16("Limage64VolumeExport"))), uintptr(unsafe.Pointer(u16("导出三维 SEG-Y 范围"))),
		WS_POPUP|WS_CAPTION|WS_SYSMENU, uintptr(CW_USEDEFAULT), uintptr(CW_USEDEFAULT), 500, 320, volumeHwnd, 0, 0, 0)
	if h == 0 {
		return
	}
	volumeExportHwnd = h
	createVolumeExportUI()
	pShowWindow.Call(h, SW_SHOW)
	pUpdateWindow.Call(h)
}

func createVolumeExportUI() {
	vex = volumeExportControls{}
	createCtrl(volumeExportHwnd, "BUTTON", "导出对象", WS_CHILD|WS_VISIBLE|BS_GROUPBOX, 14, 12, 456, 54, 0)
	vex.a = createCtrl(volumeExportHwnd, "BUTTON", "A", WS_CHILD|WS_VISIBLE|WS_TABSTOP|WS_GROUP|BS_AUTORADIOBUTTON, 30, 34, 54, 18, IDVX_A)
	vex.b = createCtrl(volumeExportHwnd, "BUTTON", "B", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_AUTORADIOBUTTON, 112, 34, 54, 18, IDVX_B)
	vex.d = createCtrl(volumeExportHwnd, "BUTTON", "差 A-B", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_AUTORADIOBUTTON, 194, 34, 86, 18, IDVX_D)
	if !volumeScenes[1].valid {
		pEnableWindow.Call(vex.b, 0)
		pEnableWindow.Call(vex.d, 0)
	}
	kind := 0
	if volumeCompareMode && volumeActiveSide == 1 && volumeScenes[1].valid {
		kind = 1
	}
	setRadio(vex.a, kind == 0)
	setRadio(vex.b, kind == 1)
	setRadio(vex.d, false)

	createCtrl(volumeExportHwnd, "BUTTON", "空间 / 时间范围", WS_CHILD|WS_VISIBLE|BS_GROUPBOX, 14, 76, 456, 150, 0)
	// Use a wider label column than v1.7.1.  Chinese UI fonts at 125–150% DPI
	// were wrapping/truncating "Inline 起始" / "Crossline 起始" in 76 px.
	createLabel(volumeExportHwnd, "Inline 起始：", 28, 102, 104, 20)
	vex.il0 = createCtrl(volumeExportHwnd, "EDIT", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|WS_BORDER|ES_AUTOHSCROLL, 136, 99, 92, 22, IDVX_IL0)
	createLabel(volumeExportHwnd, "终止：", 254, 102, 48, 20)
	vex.il1 = createCtrl(volumeExportHwnd, "EDIT", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|WS_BORDER|ES_AUTOHSCROLL, 306, 99, 94, 22, IDVX_IL1)
	createLabel(volumeExportHwnd, "Crossline 起始：", 28, 136, 104, 20)
	vex.xl0 = createCtrl(volumeExportHwnd, "EDIT", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|WS_BORDER|ES_AUTOHSCROLL, 136, 133, 92, 22, IDVX_XL0)
	createLabel(volumeExportHwnd, "终止：", 254, 136, 48, 20)
	vex.xl1 = createCtrl(volumeExportHwnd, "EDIT", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|WS_BORDER|ES_AUTOHSCROLL, 306, 133, 94, 22, IDVX_XL1)
	createLabel(volumeExportHwnd, "Time 起始：", 28, 170, 104, 20)
	vex.t0 = createCtrl(volumeExportHwnd, "EDIT", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|WS_BORDER|ES_AUTOHSCROLL, 136, 167, 92, 22, IDVX_T0)
	createLabel(volumeExportHwnd, "ms", 232, 170, 20, 20)
	createLabel(volumeExportHwnd, "终止：", 254, 170, 48, 20)
	vex.t1 = createCtrl(volumeExportHwnd, "EDIT", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|WS_BORDER|ES_AUTOHSCROLL, 306, 167, 94, 22, IDVX_T1)
	createLabel(volumeExportHwnd, "默认填入当前 3D 数据可用范围，可直接修改为子体范围。", 28, 199, 416, 20)

	vex.ok = createCtrl(volumeExportHwnd, "BUTTON", "导出...", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_DEFPUSHBUTTON, 142, 242, 88, 28, IDVX_OK)
	vex.cancel = createCtrl(volumeExportHwnd, "BUTTON", "取消", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 268, 242, 88, 28, IDVX_CANCEL)
	setVolumeExportDefaults(kind)
}

func parseIntCtrl(h uintptr) (int32, error) {
	v, err := strconv.ParseInt(strings.TrimSpace(getText(h)), 10, 32)
	return int32(v), err
}
func parseFloatCtrl(h uintptr) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(getText(h)), 64)
}

func isVolumeExportNumericEdit(h uintptr) bool {
	if volumeExportHwnd == 0 || h == 0 {
		return false
	}
	return h == vex.il0 || h == vex.il1 || h == vex.xl0 || h == vex.xl1 || h == vex.t0 || h == vex.t1
}

// handleVolumeExportNumpadKey makes the physical numeric keypad deterministic
// in the export range fields.  With NumLock on Windows sends VK_NUMPAD0..9;
// with NumLock off the same physical keys become navigation keys (e.g.
// NumPad-1 -> VK_END).  The latter was reported as "NumPad 1 cannot be used".
// Dedicated navigation keys are extended keys, so they keep their normal edit
// behavior while the non-extended keypad variants are converted to digits.
func handleVolumeExportNumpadKey(h, key, lParam uintptr) bool {
	if !isVolumeExportNumericEdit(h) {
		return false
	}
	var ch uintptr
	if key >= VK_NUMPAD0 && key <= VK_NUMPAD9 {
		ch = uintptr('0') + (key - VK_NUMPAD0)
	} else {
		extended := (lParam & (uintptr(1) << 24)) != 0
		switch key {
		case VK_END:
			if !extended {
				ch = '1'
			}
		case VK_DOWN:
			if !extended {
				ch = '2'
			}
		case VK_NEXT:
			if !extended {
				ch = '3'
			}
		case VK_LEFT:
			if !extended {
				ch = '4'
			}
		case VK_CLEAR:
			if !extended {
				ch = '5'
			}
		case VK_RIGHT:
			if !extended {
				ch = '6'
			}
		case VK_HOME:
			if !extended {
				ch = '7'
			}
		case VK_UP:
			if !extended {
				ch = '8'
			}
		case VK_PRIOR:
			if !extended {
				ch = '9'
			}
		case VK_INSERT:
			if !extended {
				ch = '0'
			}
		case VK_DELETE:
			if !extended {
				ch = '.'
			}
		case VK_DECIMAL:
			ch = '.'
		case VK_ADD:
			ch = '+'
		case VK_SUBTRACT:
			ch = '-'
		}
	}
	if ch == 0 {
		return false
	}
	pSendMessageW.Call(h, WM_CHAR, ch, 1)
	return true
}

func tracesInVolumeRange(g *segy.GeometryIndex, il0, il1, xl0, xl1 int32) []int64 {
	if g == nil {
		return nil
	}
	if il0 > il1 {
		il0, il1 = il1, il0
	}
	if xl0 > xl1 {
		xl0, xl1 = xl1, xl0
	}
	out := make([]int64, 0, len(g.TraceNumbers))
	n := len(g.TraceNumbers)
	if len(g.RowOfTrace) < n {
		n = len(g.RowOfTrace)
	}
	if len(g.ColOfTrace) < n {
		n = len(g.ColOfTrace)
	}
	for i := 0; i < n; i++ {
		r, c := int(g.RowOfTrace[i]), int(g.ColOfTrace[i])
		if r < 0 || r >= len(g.InlineValues) || c < 0 || c >= len(g.CrosslineValues) {
			continue
		}
		il, xl := g.InlineValues[r], g.CrosslineValues[c]
		if il >= il0 && il <= il1 && xl >= xl0 && xl <= xl1 {
			out = append(out, g.TraceNumbers[i])
		}
	}
	return out
}

func tracePairsInVolumeRange(a, b *segy.GeometryIndex, il0, il1, xl0, xl1 int32) ([]int64, []int64) {
	if a == nil || b == nil {
		return nil, nil
	}
	if il0 > il1 {
		il0, il1 = il1, il0
	}
	if xl0 > xl1 {
		xl0, xl1 = xl1, xl0
	}
	mb := make(map[volumeCoordKey]int64, len(b.TraceNumbers))
	nb := len(b.TraceNumbers)
	if len(b.RowOfTrace) < nb {
		nb = len(b.RowOfTrace)
	}
	if len(b.ColOfTrace) < nb {
		nb = len(b.ColOfTrace)
	}
	for i := 0; i < nb; i++ {
		r, c := int(b.RowOfTrace[i]), int(b.ColOfTrace[i])
		if r < 0 || r >= len(b.InlineValues) || c < 0 || c >= len(b.CrosslineValues) {
			continue
		}
		k := volumeCoordKey{b.InlineValues[r], b.CrosslineValues[c]}
		if _, ok := mb[k]; !ok {
			mb[k] = b.TraceNumbers[i]
		}
	}
	pa := make([]int64, 0)
	pb := make([]int64, 0)
	na := len(a.TraceNumbers)
	if len(a.RowOfTrace) < na {
		na = len(a.RowOfTrace)
	}
	if len(a.ColOfTrace) < na {
		na = len(a.ColOfTrace)
	}
	for i := 0; i < na; i++ {
		r, c := int(a.RowOfTrace[i]), int(a.ColOfTrace[i])
		if r < 0 || r >= len(a.InlineValues) || c < 0 || c >= len(a.CrosslineValues) {
			continue
		}
		il, xl := a.InlineValues[r], a.CrosslineValues[c]
		if il < il0 || il > il1 || xl < xl0 || xl > xl1 {
			continue
		}
		if tr, ok := mb[volumeCoordKey{il, xl}]; ok {
			pa = append(pa, a.TraceNumbers[i])
			pb = append(pb, tr)
		}
	}
	return pa, pb
}

func defaultVolumeExportName(src, kind string, il0, il1, xl0, xl1 int32, t0, t1 float64) string {
	base := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	if base == "" {
		base = "SeisForgeStudio"
	}
	kp := kind
	if kind == "差" {
		kp = "A-B"
	}
	return fmt.Sprintf("%s_%s_IL%d-%d_XL%d-%d_T%.0f-%.0fms.sgy", base, kp, il0, il1, xl0, xl1, t0, t1)
}

func startVolumeRangeExport() {
	kindN := volumeExportKind()
	st, ok := volumeExportScene(kindN)
	if !ok || st.f == nil || st.g == nil {
		message(volumeExportHwnd, "导出三维范围", "所选数据不可用。", MB_OK|MB_ICONERROR)
		return
	}
	il0, e0 := parseIntCtrl(vex.il0)
	il1, e1 := parseIntCtrl(vex.il1)
	xl0, e2 := parseIntCtrl(vex.xl0)
	xl1, e3 := parseIntCtrl(vex.xl1)
	t0, e4 := parseFloatCtrl(vex.t0)
	t1, e5 := parseFloatCtrl(vex.t1)
	if e0 != nil || e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil {
		message(volumeExportHwnd, "导出三维范围", "请输入有效的 IL / XL / Time 数值。", MB_OK|MB_ICONERROR)
		return
	}
	if il0 > il1 {
		il0, il1 = il1, il0
	}
	if xl0 > xl1 {
		xl0, xl1 = xl1, xl0
	}
	if t0 > t1 {
		t0, t1 = t1, t0
	}
	bnd := st.g.Bounds()
	if il0 < bnd.InlineMin || il1 > bnd.InlineMax || xl0 < bnd.CrosslineMin || xl1 > bnd.CrosslineMax {
		message(volumeExportHwnd, "导出三维范围", fmt.Sprintf("范围超出当前数据：IL %d..%d，XL %d..%d。", bnd.InlineMin, bnd.InlineMax, bnd.CrosslineMin, bnd.CrosslineMax), MB_OK|MB_ICONERROR)
		return
	}
	dt := float64(st.f.Info.SampleIntervalUS) / 1000.0
	sm0 := clampInt(int(math.Round(t0/dt)), 0, st.f.Info.SamplesPerTrace-1)
	sm1 := clampInt(int(math.Round(t1/dt)), 0, st.f.Info.SamplesPerTrace-1)
	if sm0 > sm1 {
		sm0, sm1 = sm1, sm0
	}
	kind := "A"
	srcA := volumeScenes[0].path
	srcB := ""
	var ta, tb []int64
	if kindN == 1 {
		kind = "B"
		srcA = volumeScenes[1].path
		ta = tracesInVolumeRange(st.g, il0, il1, xl0, xl1)
	} else if kindN == 2 {
		kind = "差"
		if !volumeScenes[0].valid || !volumeScenes[1].valid {
			message(volumeExportHwnd, "导出三维范围", "A/B 数据不完整。", MB_OK|MB_ICONERROR)
			return
		}
		if volumeScenes[0].f.Info.SampleIntervalUS != volumeScenes[1].f.Info.SampleIntervalUS {
			message(volumeExportHwnd, "导出三维范围", "A-B 输出要求相同采样率。", MB_OK|MB_ICONERROR)
			return
		}
		srcA = volumeScenes[0].path
		srcB = volumeScenes[1].path
		ta, tb = tracePairsInVolumeRange(volumeScenes[0].g, volumeScenes[1].g, il0, il1, xl0, xl1)
	} else {
		ta = tracesInVolumeRange(st.g, il0, il1, xl0, xl1)
	}
	if len(ta) == 0 {
		message(volumeExportHwnd, "导出三维范围", "所选范围内没有可输出 traces。", MB_OK|MB_ICONERROR)
		return
	}
	name := defaultVolumeExportName(srcA, kind, il0, il1, xl0, xl1, t0, t1)
	out := saveSegyDialog(volumeExportHwnd, name)
	if out == "" {
		return
	}
	pDestroyWindow.Call(volumeExportHwnd)
	setVolumeBusy(1)
	updateVolumeStatusLine(fmt.Sprintf("正在导出 %s 子体：%d traces × %d samples ...", kind, len(ta), sm1-sm0+1))
	go func(kind, path, pa, pb string, aTr, bTr []int64, s0, s1 int) {
		last := -1
		progress := func(done, total int) {
			if total <= 0 {
				return
			}
			pct := done * 100 / total
			if pct != last {
				last = pct
				if volumeHwnd != 0 {
					pPostMessageW.Call(volumeHwnd, WM_VOLUME_EXPORT_PROGRESS, uintptr(pct), 0)
				}
			}
		}
		var err error
		fa, ea := segy.Open(pa)
		if ea != nil {
			err = ea
		} else {
			defer fa.Close()
			if kind == "B" {
				err = fa.ExportSection(path, segy.SectionExportOptions{TraceIndices: aTr, SampleStart: s0, SampleEnd: s1, Progress: progress})
			} else if kind == "差" {
				fb, eb := segy.Open(pb)
				if eb != nil {
					err = eb
				} else {
					defer fb.Close()
					err = segy.ExportDifferenceSection(path, fa, fb, aTr, bTr, s0, s1, progress)
				}
			} else {
				err = fa.ExportSection(path, segy.SectionExportOptions{TraceIndices: aTr, SampleStart: s0, SampleEnd: s1, Progress: progress})
			}
		}
		volumeExportResultCh <- volumeExportResult{path: path, kind: kind, traces: len(aTr), samples: s1 - s0 + 1, err: err}
		if volumeHwnd != 0 {
			pPostMessageW.Call(volumeHwnd, WM_VOLUME_EXPORT_DONE, 0, 0)
		}
	}(kind, out, srcA, srcB, append([]int64(nil), ta...), append([]int64(nil), tb...), sm0, sm1)
}

func handleVolumeExportDone() {
	var r volumeExportResult
	select {
	case r = <-volumeExportResultCh:
	default:
		return
	}
	setVolumeBusy(-1)
	if r.err != nil {
		updateVolumeStatusLine("导出失败: " + r.err.Error())
		message(volumeHwnd, "导出三维范围", r.err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	updateVolumeStatusLine(fmt.Sprintf("导出完成：%s | %d traces × %d samples", r.kind, r.traces, r.samples))
	message(volumeHwnd, "导出三维范围", "导出完成：\n\n"+r.path, MB_OK|MB_ICONINFORMATION)
}

func volumeExportWndProc(h uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case WM_COMMAND:
		switch int(wParam & 0xffff) {
		case IDVX_A:
			setVolumeExportDefaults(0)
		case IDVX_B:
			setVolumeExportDefaults(1)
		case IDVX_D:
			setVolumeExportDefaults(0)
		case IDVX_OK:
			startVolumeRangeExport()
		case IDVX_CANCEL:
			pDestroyWindow.Call(h)
		}
		return 0
	case WM_CLOSE:
		pDestroyWindow.Call(h)
		return 0
	case WM_DESTROY:
		volumeExportHwnd = 0
		vex = volumeExportControls{}
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(h, uintptr(msg), wParam, lParam)
	return r
}

var _ = syscall.NewCallback
