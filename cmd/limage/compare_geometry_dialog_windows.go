//go:build windows

package main

import (
	"encoding/binary"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

const (
	geometryTarget = 26801 + iota
	geometryIL
	geometryXL
	geometryInspect
	geometryDetect
	geometrySwap
	geometryRestore
	geometryApply
	geometryOK
	geometryCancel
	geometryInlineNumber
	geometryCrosslineNumber
	geometryLocate
)

const compareGeometryDialogResultMessage = WM_USER + 248

const compareGeometryPreviewStyle = 0x0004 | 0x0040 // ES_MULTILINE | ES_AUTOVSCROLL

type compareGeometryDialog struct {
	h, owner, target, il, xl, details, status uintptr
	inlineNumber, crosslineNumber             uintptr
	buttons                                   map[int]uintptr
	aPath, bPath                              string
	textIL, textXL                            [2]string
	navPublished                              [2]string
	opened                                    [2]compareHeaderMapping
	role                                      int
	inheritsB                                 bool
	plan                                      *compareGeometryRebuild
	planGen                                   int64
	closeOnSuccess                            bool
	detectSerial                              uint64
}

type compareGeometryDialogDetection struct {
	owner  uintptr
	serial uint64
	path   string
	role   int
	result segy.GeometryDetectResult
	err    error
}

var activeCompareGeometryDialog *compareGeometryDialog
var compareGeometryDialogClass bool
var compareGeometryDialogCallback = syscall.NewCallback(compareGeometryDialogWndProc)
var compareGeometryDetectSerial uint64
var compareGeometryDialogReady sync.Map

func showCompareGeometryDialog() {
	if compareAPath == "" {
		setText(cc.status, "请先打开 A 数据")
		return
	}
	if d := activeCompareGeometryDialog; d != nil {
		if d.aPath == compareAPath && d.bPath == compareBPath {
			showOwnedSettingsWindow(d.h)
			pSetFocus.Call(d.target)
			return
		}
		closeCompareGeometryDialog()
	}
	a, b, ok := compareEffectiveHeaderMappings()
	if !ok {
		return
	}
	d := &compareGeometryDialog{
		owner: compareHwnd, aPath: compareAPath, bPath: compareBPath,
		inheritsB: generatedPoststackTraceOrder(compareBPath) != nil,
		opened:    [2]compareHeaderMapping{a, b}, buttons: make(map[int]uintptr),
	}
	if compareGeometrySourceA.path == d.aPath && compareGeometrySourceA.opened.valid() {
		d.opened[0] = compareGeometrySourceA.opened
	}
	if compareGeometrySourceB.path == d.bPath && compareGeometrySourceB.opened.valid() {
		d.opened[1] = compareGeometrySourceB.opened
	}
	d.textIL, d.textXL = [2]string{strconv.Itoa(a.il), strconv.Itoa(b.il)}, [2]string{strconv.Itoa(a.xl), strconv.Itoa(b.xl)}
	const class = "Limage64PoststackGeometry"
	hi, _, _ := pGetModuleHandleW.Call(0)
	if !compareGeometryDialogClass {
		cursor, _, _ := pLoadCursorW.Call(0, IDC_ARROW)
		registerClass(class, compareGeometryDialogCallback, COLOR_BTNFACE+1, hi, cursor)
		compareGeometryDialogClass = true
	}
	dpi := displayDPI(compareHwnd)
	scale := func(n int) int { return (n*dpi + 48) / 96 }
	area := algorithmMonitorArea(compareHwnd)
	w := minInt(scale(600), int(area.Right-area.Left))
	h := minInt(scale(500), int(area.Bottom-area.Top))
	if w <= 0 || h <= 0 {
		return
	}
	x, y := int(area.Left)+(int(area.Right-area.Left)-w)/2, int(area.Top)+(int(area.Bottom-area.Top)-h)/2
	hwnd, _, _ := pCreateWindowExW.Call(settingsToolWindowStyle, uintptr(unsafe.Pointer(u16(class))), uintptr(unsafe.Pointer(u16("几何设置 — SeisForge Studio"))),
		WS_CAPTION|WS_SYSMENU|WS_CLIPCHILDREN, uintptr(x), uintptr(y), uintptr(w), uintptr(h), d.owner, 0, hi, 0)
	if hwnd == 0 {
		setText(cc.status, "无法打开几何设置")
		return
	}
	d.h = hwnd
	activeCompareGeometryDialog = d
	clientW, clientH := rectWH(clientRect(hwnd))
	create := func(class, title string, style uint32, x, y, w, h, id int) uintptr {
		return createCtrl(hwnd, class, title, WS_CHILD|WS_VISIBLE|style, scale(x), scale(y), scale(w), scale(h), id)
	}
	button := func(label string, x, y, w, id int) {
		d.buttons[id] = createPoststackNamedToolButton(hwnd, id, 0, label, WS_VISIBLE|BS_PUSHBUTTON, scale(x), scale(y), scale(w), scale(25))
	}
	create("STATIC", "编辑对象", 0, 15, 15, 74, 20, 0)
	d.target = create("COMBOBOX", "", WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 94, 12, 292, 100, geometryTarget)
	geometryAddChoice(d.target, "A：输入剖面")
	if d.bPath != "" {
		if d.inheritsB {
			geometryAddChoice(d.target, "处理结果 B：继承输入几何（只读）")
		} else {
			geometryAddChoice(d.target, "B：独立加载的剖面")
		}
	}
	pSendMessageW.Call(d.target, CB_SETCURSEL, 0, 0)
	create("STATIC", "IL 起始字节", 0, 15, 55, 85, 20, 0)
	d.il = create("EDIT", "", WS_BORDER|WS_TABSTOP|ES_AUTOHSCROLL, 107, 52, 82, 24, geometryIL)
	create("STATIC", "XL 起始字节", 0, 212, 55, 91, 20, 0)
	d.xl = create("EDIT", "", WS_BORDER|WS_TABSTOP|ES_AUTOHSCROLL, 308, 52, 78, 24, geometryXL)
	create("STATIC", "1 起算；有符号 32 位，读取 4 字节；使用文件实际字节序。", 0, 15, 83, 552, 20, 0)
	button("解析预览", 15, 107, 92, geometryInspect)
	button("自动识别", 117, 107, 92, geometryDetect)
	button("交换 IL/XL", 219, 107, 100, geometrySwap)
	button("恢复打开时映射", 329, 107, 132, geometryRestore)
	create("STATIC", "定位 A 的数据编号（不改变道头设置）", 0, 15, 145, 430, 20, 0)
	create("STATIC", "Inline", 0, 15, 172, 85, 20, 0)
	create("STATIC", "Crossline", 0, 212, 172, 90, 20, 0)
	const navigationStyle = WS_BORDER | WS_TABSTOP | ES_AUTOHSCROLL
	d.inlineNumber = create("EDIT", "", navigationStyle, 107, 169, 82, 24, geometryInlineNumber)
	d.crosslineNumber = create("EDIT", "", navigationStyle, 308, 169, 78, 24, geometryCrosslineNumber)
	button("定位", 406, 169, 80, geometryLocate)
	d.details = createCtrl(hwnd, "EDIT", "", WS_CHILD|WS_VISIBLE|WS_BORDER|WS_TABSTOP|WS_VSCROLL|ES_READONLY|compareGeometryPreviewStyle,
		scale(15), scale(205), maxInt(scale(30), clientW-scale(30)), maxInt(scale(72), clientH-scale(287)), 0)
	d.status = createCtrl(hwnd, "STATIC", "", WS_CHILD|WS_VISIBLE|0x4000, scale(15), clientH-scale(72), maxInt(1, clientW-scale(30)), scale(23), 0)
	footerY := (clientH - scale(39)) * 96 / dpi
	button("应用", 274, footerY, 86, geometryApply)
	button("确定", 370, footerY, 86, geometryOK)
	button("取消", 466, footerY, 86, geometryCancel)
	initCompareGeometryNavigation(d)
	d.loadDraft()
	d.preview()
	showOwnedSettingsWindow(hwnd)
	pSetFocus.Call(d.target)
}

func (d *compareGeometryDialog) locate() {
	if d.plan != nil {
		return
	}
	if d.owner != compareHwnd || d.aPath != compareAPath || d.bPath != compareBPath {
		setText(d.status, "数据源已变化；请重新打开几何设置")
		return
	}
	il, xl, err := parseCompareNavigationNumbers(getText(d.inlineNumber), getText(d.crosslineNumber))
	if err == nil {
		err = locateCompareNavigation(il, xl)
	}
	if err != nil {
		setText(d.status, err.Error())
		return
	}
	d.navPublished = [2]string{strconv.FormatInt(int64(il), 10), strconv.FormatInt(int64(xl), 10)}
	setText(d.inlineNumber, d.navPublished[0])
	setText(d.crosslineNumber, d.navPublished[1])
	setText(d.status, fmt.Sprintf("已定位 Inline %d / Crossline %d（未修改道头设置）", il, xl))
}

func geometryAddChoice(handle uintptr, label string) {
	pSendMessageW.Call(handle, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(label))))
}

func (d *compareGeometryDialog) saveDraft() {
	if d.role != 1 || !d.inheritsB {
		d.textIL[d.role], d.textXL[d.role] = getText(d.il), getText(d.xl)
	}
}

func (d *compareGeometryDialog) loadDraft() {
	role := d.role
	inherited := role == 1 && d.inheritsB
	if inherited {
		role = 0
	}
	setText(d.il, d.textIL[role])
	setText(d.xl, d.textXL[role])
	for _, h := range []uintptr{d.il, d.xl, d.buttons[geometryDetect], d.buttons[geometrySwap], d.buttons[geometryRestore]} {
		if inherited {
			pEnableWindow.Call(h, 0)
		} else {
			pEnableWindow.Call(h, 1)
		}
	}
}

func (d *compareGeometryDialog) sourcePath() string {
	if d.role == 1 {
		return d.bPath
	}
	return d.aPath
}

func compareMappingPreview(file *segy.File, m compareHeaderMapping) (string, error) {
	var order binary.ByteOrder = binary.BigEndian
	endian := "大端"
	if file.Info.Endian == segy.Little {
		order, endian = binary.LittleEndian, "小端"
	}
	var raw [240]byte
	n := minInt(64, int(file.Info.TraceCount))
	if n == 0 {
		return "", fmt.Errorf("没有有效数据道")
	}
	ils, xls, locations := map[int32]bool{}, map[int32]bool{}, map[[2]int32]bool{}
	var loIL, hiIL, loXL, hiXL int32
	valid, duplicate := 0, 0
	representative := make([]string, 0, 5)
	for i := 0; i < n; i++ {
		trace := int64(i) * (file.Info.TraceCount - 1) / int64(maxInt(1, n-1))
		if err := file.ReadTraceHeaderBytes(trace, raw[:]); err != nil {
			return "", err
		}
		il := int32(order.Uint32(raw[m.il-1 : m.il+3]))
		xl := int32(order.Uint32(raw[m.xl-1 : m.xl+3]))
		if i < 4 || i == n-1 {
			line := fmt.Sprintf("道 %d   Inline %d   Crossline %d", trace+1, il, xl)
			if il == 0 && xl == 0 {
				line += "（双零：缺失几何）"
			}
			representative = append(representative, line)
		}
		if il == 0 && xl == 0 {
			continue
		}
		if locations[[2]int32{il, xl}] {
			duplicate++
		}
		locations[[2]int32{il, xl}], ils[il], xls[xl] = true, true, true
		if valid == 0 || il < loIL {
			loIL = il
		}
		if valid == 0 || il > hiIL {
			hiIL = il
		}
		if valid == 0 || xl < loXL {
			loXL = xl
		}
		if valid == 0 || xl > hiXL {
			hiXL = xl
		}
		valid++
	}
	label := "完整道头"
	if int64(n) != file.Info.TraceCount {
		label = "均匀抽样，非全卷统计"
	}
	text := fmt.Sprintf("%s：%s，%s %d 道，有效 %d，重复位置 %d\r\n",
		filepath.Base(file.Info.Path), endian, label, n, valid, duplicate)
	if valid != 0 {
		text += fmt.Sprintf("Inline %d–%d（%d 个值）  Crossline %d–%d（%d 个值）\r\n", loIL, hiIL, len(ils), loXL, hiXL, len(xls))
	}
	return text + strings.Join(representative, "\r\n"), nil
}

func (d *compareGeometryDialog) preview() {
	if d.role == 1 && d.inheritsB {
		text := "处理结果 B 通过源物理道继承 A 的几何和实际时间范围，不读取内部结果的道头字节。应用 A 新映射时同步重建 B。"
		if compareTD != nil && compareTD.gb != nil && compareBPath == d.bPath {
			g := compareTD.gb
			b := g.Bounds()
			text += fmt.Sprintf("\r\n实际覆盖：IL %d–%d（%d 个），XL %d–%d（%d 个），%d 道。",
				b.InlineMin, b.InlineMax, len(g.InlineValues), b.CrosslineMin, b.CrosslineMax, len(g.CrosslineValues), g.ValidTraceCount)
		}
		setText(d.details, text)
		return
	}
	m, err := parseCompareHeaderMapping(getText(d.il), getText(d.xl))
	if err != nil {
		setText(d.status, err.Error())
		return
	}
	file, err := openPoststackCompareSource(d.sourcePath())
	if err != nil {
		setText(d.status, err.Error())
		return
	}
	defer file.Close()
	text, err := compareMappingPreview(file, m)
	if err != nil {
		setText(d.status, "预览失败："+err.Error())
		return
	}
	setText(d.details, text)
	setText(d.status, "解析预览未改变当前几何；应用后才替换。")
}

func (d *compareGeometryDialog) detect() {
	if d.inheritsB && d.role == 1 {
		return
	}
	d.detectSerial = atomic.AddUint64(&compareGeometryDetectSerial, 1)
	owner, serial, path, role := d.h, d.detectSerial, d.sourcePath(), d.role
	setText(d.status, "正在读取道头，请稍候…")
	go func() {
		r := &compareGeometryDialogDetection{owner: owner, serial: serial, path: path, role: role}
		f, err := openPoststackCompareSource(path)
		if err != nil {
			r.err = err
		} else {
			r.result, r.err = f.DetectPoststackSectionGeometryBytes(3072)
			f.Close()
		}
		compareGeometryDialogReady.Store(serial, r)
		if ret, _, _ := pPostMessageW.Call(owner, compareGeometryDialogResultMessage, uintptr(serial), 0); ret == 0 {
			compareGeometryDialogReady.Delete(serial)
		}
	}()
}

func (d *compareGeometryDialog) apply(closeAfter bool) {
	if d.plan != nil {
		return
	}
	if d.aPath != compareAPath || d.bPath != compareBPath {
		setText(d.status, "数据源已变化；请重新打开几何设置")
		return
	}
	d.saveDraft()
	a, err := parseCompareHeaderMapping(d.textIL[0], d.textXL[0])
	if err != nil {
		setText(d.status, "A："+err.Error())
		return
	}
	b := compareHeaderMapping{189, 193}
	if d.bPath != "" && !d.inheritsB {
		b, err = parseCompareHeaderMapping(d.textIL[1], d.textXL[1])
		if err != nil {
			setText(d.status, "B："+err.Error())
			return
		}
	}
	oldA, oldB, oldOK := compareEffectiveHeaderMappings()
	hasGeometry := compareTD != nil && compareTD.ga != nil && !compareTD.traceOrderFallback
	if oldOK && hasGeometry && oldA == a && (d.bPath == "" || d.inheritsB || oldB == b) {
		setText(d.status, "当前几何未改变")
		if closeAfter {
			closeOwnedSettingsWindow(d.h, d.owner)
		}
		return
	}
	d.plan = &compareGeometryRebuild{aPath: d.aPath, bPath: d.bPath, a: a, b: b, swapA: oldOK && a == oldA.swap()}
	d.closeOnSuccess = closeAfter
	d.detectSerial = 0
	for id, handle := range d.buttons {
		if id != geometryCancel {
			pEnableWindow.Call(handle, 0)
		}
	}
	for _, handle := range []uintptr{d.target, d.il, d.xl, d.inlineNumber, d.crosslineNumber} {
		pEnableWindow.Call(handle, 0)
	}
	setText(d.status, "正在验证并重建索引；当前 A/B 仍保持可用…")
	startPrepareTimeCompareMapping(d.plan)
	d.planGen = atomic.LoadInt64(&compareGen)
}

func completeCompareGeometryDialog(plan *compareGeometryRebuild, err error) {
	d := activeCompareGeometryDialog
	if d == nil || d.plan != plan {
		return
	}
	d.plan = nil
	if err == nil && d.closeOnSuccess {
		closeOwnedSettingsWindow(d.h, d.owner)
		return
	}
	for _, handle := range d.buttons {
		pEnableWindow.Call(handle, 1)
	}
	for _, h := range []uintptr{d.target, d.inlineNumber, d.crosslineNumber} {
		pEnableWindow.Call(h, 1)
	}
	d.loadDraft()
	if err == nil {
		d.preview()
		setText(d.status, "几何已应用。")
	} else {
		setText(d.status, err.Error())
	}
}

func closeCompareGeometryDialog() {
	if d := activeCompareGeometryDialog; d != nil {
		closeOwnedSettingsWindow(d.h, d.owner)
	}
}

func translateCompareGeometryDialogMessage(m *MSG) bool {
	d := activeCompareGeometryDialog
	if d == nil || m == nil || m.Message != WM_KEYDOWN || (m.WParam != VK_ESCAPE && m.WParam != 0x09) {
		return false
	}
	if child, _, _ := pIsChild.Call(d.h, m.Hwnd); m.Hwnd != d.h && child == 0 {
		return false
	}
	if m.WParam == VK_ESCAPE {
		if open, _, _ := pSendMessageW.Call(d.target, CB_GETDROPPEDSTATE, 0, 0); open != 0 {
			return false // Close the native combo first, not the modeless dialog.
		}
		pPostMessageW.Call(d.h, WM_CLOSE, 0, 0)
		return true
	}
	handled, _, _ := algorithmIsDialogMessage.Call(d.h, uintptr(unsafe.Pointer(m)))
	return handled != 0
}

func compareGeometryDialogWndProc(h uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	d := activeCompareGeometryDialog
	if msg == compareGeometryDialogResultMessage {
		v, ok := compareGeometryDialogReady.LoadAndDelete(uint64(wParam))
		if !ok {
			return 0
		}
		r := v.(*compareGeometryDialogDetection)
		if d == nil || r.owner != h || d.h != h || d.detectSerial != r.serial || r.path != d.sourcePath() || r.role != d.role {
			return 0
		}
		if r.err != nil {
			setText(d.status, "没有可信自动候选；可手动设置："+r.err.Error())
			return 0
		}
		setText(d.il, strconv.Itoa(r.result.InlineByte))
		setText(d.xl, strconv.Itoa(r.result.CrosslineByte))
		d.preview()
		setText(d.status, fmt.Sprintf("自动候选 %d/%d，置信度%s（仅草稿）", r.result.InlineByte, r.result.CrosslineByte, confidenceName(r.result.Confidence, r.result.Ambiguous)))
		return 0
	}
	if d != nil && d.h == h {
		switch msg {
		case WM_COMMAND:
			switch int(wParam & 0xFFFF) {
			case geometryTarget:
				if (wParam>>16)&0xFFFF == 1 {
					d.saveDraft()
					if v, _, _ := pSendMessageW.Call(d.target, CB_GETCURSEL, 0, 0); v <= 1 {
						d.role = int(v)
					}
					d.detectSerial = 0
					d.loadDraft()
					d.preview()
				}
			case geometryInspect:
				d.preview()
			case geometryDetect:
				d.detect()
			case geometrySwap:
				il, xl := getText(d.il), getText(d.xl)
				setText(d.il, xl)
				setText(d.xl, il)
				d.preview()
			case geometryRestore:
				m := d.opened[d.role]
				setText(d.il, strconv.Itoa(m.il))
				setText(d.xl, strconv.Itoa(m.xl))
				d.preview()
			case geometryApply:
				d.apply(false)
			case geometryOK:
				d.apply(true)
			case geometryLocate:
				d.locate()
			case geometryCancel:
				closeOwnedSettingsWindow(h, d.owner)
			}
			return 0
		case WM_CLOSE:
			closeOwnedSettingsWindow(h, d.owner)
			return 0
		case WM_DESTROY:
			if d.plan != nil && d.planGen == atomic.LoadInt64(&compareGen) {
				cancelPoststackComparePrepare()
				atomic.AddInt64(&compareGen, 1)
				setCompareProgress(0, false)
				setText(cc.status, "已取消几何修改；当前 A/B 保持不变")
			}
			activeCompareGeometryDialog = nil
		}
	}
	ret, _, _ := pDefWindowProcW.Call(h, uintptr(msg), wParam, lParam)
	return ret
}
