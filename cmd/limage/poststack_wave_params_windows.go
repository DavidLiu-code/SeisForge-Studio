//go:build windows

package main

import (
	"fmt"
	"strconv"
	"unsafe"
)

const (
	idWaveRef = 2020 + iota
	idWaveDensity
	idWavePreset
	idWavePositiveFill
	idWaveNegativeFill
	idWavePositiveColor
	idWaveNegativeColor
	idWaveDefaults
	idWaveTimeStep
	idWavePageTabs
)

type poststackWaveParamControls struct {
	tabs, defaults, refMode, reference, swing, density, stride, lineWidth, opacity   uintptr
	baseline, positive, negative, positiveColor, negativeColor, densityInfo, quality uintptr
	pages                                                                            [4][]uintptr
	page                                                                             int
	colors                                                                           [2]uint32
	resetDraft                                                                       bool
}

func poststackTabSelection(h uintptr) int {
	r, _, _ := pSendMessageW.Call(h, TCM_GETCURSEL, 0, 0)
	return int(r)
}

func createPoststackWaveParamPage() {
	w := &pc.waveUI
	w.tabs = createCtrl(paramHwnd, "SysTabControl32", "", WS_CHILD|WS_TABSTOP|poststackToolbarClipSiblings, 14, 45, 342, 205, idWavePageTabs)
	addPageCtrl(2, w.tabs)
	for i, title := range []string{"模式与摆幅", "绘制密度", "曲线与填充", "绘制质量"} {
		item := TCITEM{Mask: TCIF_TEXT, PszText: u16(title)}
		pSendMessageW.Call(w.tabs, TCM_INSERTITEMW, uintptr(i), uintptr(unsafe.Pointer(&item)))
	}
	makeCtrl := func(page int, cls, s string, style uint32, x, y, width, height, id int) uintptr {
		h := createCtrl(paramHwnd, cls, s, WS_CHILD|poststackToolbarClipSiblings|style, x, y, width, height, id)
		w.pages[page] = append(w.pages[page], h)
		return h
	}
	label := func(p int, s string, x, y, width, height int) uintptr {
		return makeCtrl(p, "STATIC", s, 0, x, y, width, height, 0)
	}
	edit := func(p int, x, y, width, id int) uintptr {
		return makeCtrl(p, "EDIT", "", WS_TABSTOP|WS_BORDER|ES_AUTOHSCROLL, x, y, width, 21, id)
	}
	combo := func(p int, values []string, x, y, width, id int, editable bool) uintptr {
		style := uint32(CBS_DROPDOWNLIST)
		if editable {
			style = 0x0002 | 0x0040
		} // CBS_DROPDOWN | CBS_AUTOHSCROLL
		h := makeCtrl(p, "COMBOBOX", "", WS_TABSTOP|CBS_HASSTRINGS|style, x, y, width, 158, id)
		poststackParamCombo(h, values, 0)
		return h
	}
	check := func(p int, s string, x, y, width, id int) uintptr {
		return makeCtrl(p, "BUTTON", s, WS_TABSTOP|BS_AUTOCHECKBOX, x, y, width, 21, id)
	}
	button := func(p int, s string, x, y, width, id int) uintptr {
		h := createPoststackNamedToolButton(paramHwnd, id, 0, s, BS_PUSHBUTTON, x, y, width, 22)
		w.pages[p] = append(w.pages[p], h)
		return h
	}

	label(0, "显示模式", 24, 83, 75, 18)
	pc.plotType = combo(0, []string{"彩色图", "波形图", "波形＋变面积", "变面积图"}, 105, 79, 225, IDP_PLOT_TYPE, false)
	label(0, "显示摆幅", 24, 112, 75, 18)
	w.swing = combo(0, []string{"0.5", "1", "1.5", "2", "3"}, 105, 108, 84, 0, true)
	label(0, "×  横向摆幅（不改线宽）", 195, 112, 151, 18)
	label(0, "参考振幅", 24, 142, 75, 18)
	w.refMode = combo(0, []string{"自动", "手动"}, 105, 138, 84, idWaveRef, false)
	pc.waveFields[2] = edit(0, 198, 138, 130, 0)
	w.reference = label(0, "", 24, 172, 319, 66)

	label(1, "画道方式", 24, 83, 75, 18)
	w.density = combo(1, []string{"自动（约 12 DIP）", "全部可见道", "每隔 K 道"}, 105, 79, 225, idWaveDensity, false)
	label(1, "每隔 K 道", 24, 115, 80, 18)
	w.stride = edit(1, 105, 111, 84, 0)
	label(1, "K=1 即全部可见道", 198, 115, 148, 18)
	label(1, "最小道距", 24, 146, 75, 18)
	pc.waveFields[0] = edit(1, 105, 142, 84, 0)
	label(1, "轴原单位；0 = 自动", 198, 146, 148, 18)
	w.densityInfo = label(1, "", 24, 177, 319, 61)

	label(2, "着色方案", 24, 83, 75, 18)
	pc.waveColors = combo(2, []string{"黑白", "黑红"}, 105, 79, 98, idWavePreset, false)
	pc.waveReverse = check(2, "反转颜色", 222, 79, 116, 0)
	label(2, "线宽", 24, 113, 60, 18)
	w.lineWidth = edit(2, 79, 109, 59, 0)
	label(2, "DIP", 143, 113, 28, 18)
	label(2, "填充不透明度", 176, 113, 86, 18)
	w.opacity = edit(2, 262, 109, 56, 0)
	label(2, "%", 324, 113, 18, 18)
	w.positive = check(2, "填充正面积", 24, 139, 103, idWavePositiveFill)
	w.positiveColor = button(2, "", 130, 138, 199, idWavePositiveColor)
	w.negative = check(2, "填充负面积", 24, 166, 103, idWaveNegativeFill)
	w.negativeColor = button(2, "", 130, 165, 199, idWaveNegativeColor)
	w.baseline = check(2, "显示浅色零基线", 24, 195, 151, 0)
	label(2, "彩色图不使用波形样式；仅波形不填面积。", 24, 222, 318, 18)

	label(3, "彩色图插值", 24, 83, 88, 18)
	pc.displayMode = combo(3, displayModeNames, 119, 79, 211, IDP_DISPMODE, false)
	poststackSelectWaveCombo(pc.displayMode, int(renderDisplayMode))
	label(3, "显示时间步长", 24, 115, 93, 18)
	pc.waveFields[1] = edit(3, 119, 111, 79, idWaveTimeStep)
	label(3, "ms；0 = 每个真实样点", 205, 115, 143, 18)
	w.quality = label(3, "", 24, 145, 319, 42)
	pc.rasterHint = label(3, "默认线性连接真实样点，使用抗锯齿绘制。只影响显示，不改变采样、频谱或算法输入。", 24, 193, 315, 43)
	w.defaults = createPoststackNamedToolButton(paramHwnd, idWaveDefaults, 0, "恢复波形默认", BS_PUSHBUTTON, 21, 254, 116, 24)
	addPageCtrl(2, w.defaults)
	poststackFillWaveParamDraft(poststackWaveDisplay)
}

func poststackSelectWaveCombo(h uintptr, value int) {
	pSendMessageW.Call(h, CB_SETCURSEL, uintptr(value), 0)
}

func poststackWaveParamCheck(h uintptr, value bool) {
	pSendMessageW.Call(h, BM_SETCHECK, uintptr(boolToInt(value)), 0)
}

func poststackFillWaveParamDraft(style poststackWaveStyle) {
	style = style.effective()
	w := &pc.waveUI
	poststackSelectWaveCombo(pc.plotType, int(style.Mode))
	poststackSelectWaveCombo(pc.waveColors, boolToInt(style.BlackRed))
	poststackSelectWaveCombo(w.refMode, boolToInt(style.Amplitude > 0))
	poststackSelectWaveCombo(w.density, int(style.Density))
	setText(w.swing, formatNumber(style.Swing))
	setText(w.stride, strconv.Itoa(style.Stride))
	setText(w.lineWidth, formatNumber(style.LineWidth))
	setText(w.opacity, formatNumber(style.Opacity*100))
	for i, value := range []float64{style.Spacing, style.TimeStepMS, style.Amplitude} {
		s := formatNumber(value)
		if i == 2 && value == 0 {
			s = ""
		}
		setText(pc.waveFields[i], s)
	}
	poststackWaveParamCheck(pc.waveReverse, style.Reverse)
	poststackWaveParamCheck(w.baseline, style.Baseline)
	poststackWaveParamCheck(w.positive, style.PositiveFill)
	poststackWaveParamCheck(w.negative, style.NegativeFill)
	w.colors = [2]uint32{style.PositiveColor, style.NegativeColor}
	poststackWaveParamColorText()
	poststackSyncParamModes()
}

func poststackWaveParamColorText() {
	w := &pc.waveUI
	for i, h := range []uintptr{w.positiveColor, w.negativeColor} {
		c := w.colors[i]
		setText(h, fmt.Sprintf("%s颜色  #%02X%02X%02X", []string{"正区", "负区"}[i], byte(c), byte(c>>8), byte(c>>16)))
	}
}

func showPoststackWaveParamSection(visible bool) {
	w := &pc.waveUI
	// Both native tabs and controls are siblings, so notification delivery
	// stays with the owning dialog. Put the active fields above the nested
	// tab, and that tab above the outer tab's paint surface.
	raise := func(h uintptr) {
		pSetWindowPos.Call(h, 0, 0, 0, 0, 0, 0x0001|0x0002|SWP_NOACTIVATE) // SWP_NOSIZE | SWP_NOMOVE
	}
	if visible {
		raise(w.tabs)
		raise(w.defaults)
	}
	for i, controls := range w.pages {
		if visible && i == w.page {
			for _, h := range controls {
				raise(h)
			}
		}
		showControls(controls, visible && i == w.page)
	}
	if visible {
		poststackSyncParamModes()
	}
}

func poststackWaveParamCommand(id, code int) bool {
	w := &pc.waveUI
	if w.tabs == 0 || id < idWaveRef || id > idWaveTimeStep {
		return false
	}
	switch id {
	case idWaveRef, idWaveDensity:
		if code == 1 {
			poststackSyncParamModes()
		}
	case idWaveTimeStep:
		if code == 0x0300 {
			poststackSyncWaveQualityText()
		}
	case idWavePreset:
		if code == 1 {
			s := poststackWavePresetStyle(poststackWaveDefaultStyle(), displaySelection(pc.waveColors) == 1)
			w.colors = [2]uint32{s.PositiveColor, s.NegativeColor}
			poststackWaveParamCheck(w.positive, s.PositiveFill)
			poststackWaveParamCheck(w.negative, s.NegativeFill)
			poststackWaveParamColorText()
			poststackSyncParamModes()
		}
	case idWaveDefaults:
		if code == 0 {
			w.resetDraft = true
			poststackFillWaveParamDraft(poststackWaveDefaultStyle())
		}
	case idWavePositiveColor, idWaveNegativeColor:
		if code == 0 {
			i := boolToInt(id == idWaveNegativeColor)
			choice := displayChooseColorData{Owner: paramHwnd, Result: w.colors[i], Custom: uintptr(unsafe.Pointer(&displayCustomColors[0])), Flags: 3}
			choice.Size = uint32(unsafe.Sizeof(choice))
			owner := paramHwnd
			if ok, _, _ := displayChooseColor.Call(uintptr(unsafe.Pointer(&choice))); ok != 0 && owner == paramHwnd {
				w.colors[i] = choice.Result
				poststackWaveParamColorText()
			}
		}
	}
	return true
}

func poststackSyncWaveQualityText() {
	step, err := poststackParamNumber(getText(pc.waveFields[1]), true)
	dt := compareA.sampleDtUS
	if dt <= 0 {
		dt = compareDtAUS
	}
	s := "实际显示步长：等待有效采样间隔。"
	if err != nil {
		s = "显示时间步长须为有限非负数。"
	} else if dt > 0 {
		n := max(1, compareA.stats.SampleEnd-compareA.stats.SampleStart+1)
		if compareA.wave != nil {
			n = max(1, compareA.wave.last-compareA.wave.first+1)
		}
		actual := float64(poststackWaveTimeStep(poststackWaveStyle{TimeStepMS: step}, dt, n)) * float64(dt) / 1000
		s = fmt.Sprintf("实际显示步长：%.6g ms（原始采样 %.6g ms）。", actual, float64(dt)/1000)
	}
	setText(pc.waveUI.quality, s)
}
