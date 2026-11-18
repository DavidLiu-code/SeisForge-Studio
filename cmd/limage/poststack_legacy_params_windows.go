//go:build windows

package main

import (
	"fmt"
	"strconv"
	"strings"
	"unsafe"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
)

func poststackParamCombo(h uintptr, names []string, current int) {
	for _, s := range names {
		pSendMessageW.Call(h, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(s))))
	}
	pSendMessageW.Call(h, CB_SETCURSEL, uintptr(current), 0)
}

func poststackCurrentPlotCaption() string {
	if !poststackWaveActive() {
		return paletteNames[comparePaletteIndex]
	}
	m := [4]string{"彩色图", "波形图", "波形＋变面积", "变面积图"}[poststackWaveDisplay.Mode]
	return m + " · " + [2]string{"黑白", "黑红"}[boolToInt(poststackWaveDisplay.BlackRed)]
}

func poststackSyncParamModes() {
	if pc.plotType == 0 {
		return
	}
	line := paramOwner == compareHwnd && compareMode != 2
	mode := poststackPlotMode(displaySelection(pc.plotType))
	wave := line && mode >= poststackPlotWiggle && mode <= poststackPlotVA
	area := wave && mode != poststackPlotWiggle
	w := &pc.waveUI
	enable := func(on bool, controls ...uintptr) {
		for _, h := range controls {
			if h != 0 {
				pEnableWindow.Call(h, uintptr(boolToInt(on)))
			}
		}
	}
	enable(line, pc.plotType, w.defaults)
	enable(!wave, pc.displayMode)
	enable(wave, w.swing, w.refMode, w.density, pc.waveColors, pc.waveReverse, w.baseline, w.positiveColor, w.negativeColor, pc.waveFields[1])
	enable(wave && mode != poststackPlotVA, w.lineWidth)
	enable(area, w.opacity, w.positive, w.negative)
	enable(wave && displaySelection(w.refMode) == 1, pc.waveFields[2])
	enable(wave && displaySelection(w.density) == int(poststackWaveDensityAuto), pc.waveFields[0])
	enable(wave && displaySelection(w.density) == int(poststackWaveDensityStride), w.stride)
	if wave && displaySelection(w.refMode) == 0 {
		setText(w.reference, poststackWaveReferenceSummary())
	} else if wave {
		setText(w.reference, "手动参考须为正；它只是显示比例，不截断真实振幅。A/B 联动时使用同一尺度。")
	} else {
		setText(w.reference, "彩色图不使用波形振幅参数。切换波形模式后可以编辑。")
	}
	status := poststackWaveFrameNotice(&compareA)
	if status == "" {
		status = "绘图道密度只影响显示，处理使用全部有效道。"
	}
	setText(w.densityInfo, status)
	poststackSyncWaveQualityText()
}

func poststackParamKey(owner uintptr) string {
	if owner == compareHwnd {
		return seismicCompareKey(0)
	}
	return mainDisplayKey
}

func poststackBindParamSurface() {
	o := displayOwners[paramOwner]
	key := poststackParamKey(paramOwner)
	if o == nil || o.surfaces[key] == nil {
		for _, h := range pc.axes {
			pEnableWindow.Call(h, 0)
		}
		return
	}
	e := o.surfaces[key]
	pc.surfaceIdentity = e.s.Identity
	pc.surfaceKey = key
	state, _ := displayCommitted(paramOwner, key)
	if len(e.s.Axes) < 2 || len(state.Axes) < 2 {
		return
	}
	for j, axis := range e.s.Axes[:2] {
		axis = displayAxisFor(paramOwner, key, axis)
		fields := [3]int{2 * j, 2*j + 1, 4 + j}
		vals := [3]float64{axis.DisplayValue(axis.View.Min), axis.DisplayValue(axis.View.Max), axis.Step}
		if e.s.equalTicks {
			n := axis.Divisions
			if n == 0 {
				n = displaycore.DefaultTickDivisions
			}
			vals[2] = float64(n)
		}
		for i, field := range fields {
			pc.axisInitial[field] = displaycore.FormatExactNumber(vals[i])
			setText(pc.axes[field], pc.axisInitial[field])
			pEnableWindow.Call(pc.axes[field], uintptr(boolToInt(i == 2 || !axis.StyleOnly)))
		}
	}
	if state.Color != nil && paramOwner == compareHwnd && compareMode != 2 {
		pc.limitInitialOn = state.Color.RangePolicy == displaycore.ManualRange
		pSendMessageW.Call(pc.limit, BM_SETCHECK, uintptr(boolToInt(pc.limitInitialOn)), 0)
		for i, h := range []uintptr{pc.minEdit, pc.maxEdit} {
			val := [2]float64{state.Color.Range.Min, state.Color.Range.Max}[i]
			pc.limitInitial[i] = displaycore.FormatExactNumber(val)
			setText(h, pc.limitInitial[i])
			pEnableWindow.Call(h, uintptr(boolToInt(pc.limitInitialOn)))
		}
	}
}

func poststackParamNumber(text string, nonnegative bool) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil || !seismicFinite(v) || nonnegative && v < 0 {
		return 0, fmt.Errorf("必须是有限%s数值", map[bool]string{true: "非负"}[nonnegative])
	}
	return v, nil
}

func poststackParamWaveDraft() (poststackWaveStyle, error) {
	w := poststackWaveDisplay
	if paramOwner != compareHwnd || compareMode == 2 || pc.plotType == 0 {
		return w, nil
	}
	n := displaySelection(pc.plotType)
	if n < 0 || n > int(poststackPlotVA) {
		return w, fmt.Errorf("请选择有效的显示类型")
	}
	w.Mode = poststackPlotMode(n)
	if w.Mode == poststackPlotColor {
		// Keep the worksheet's last committed waveform options while displaying
		// raster. The explicit default action has its own validated draft.
		if !pc.waveUI.resetDraft {
			return w, nil
		}
		return poststackWaveDefaultStyle(), nil
	}
	if pc.waveUI.resetDraft {
		w = poststackWaveDefaultStyle()
		w.Mode = poststackPlotMode(n)
	}
	w = w.effective()
	ui := &pc.waveUI
	w.BlackRed, w.Reverse = displaySelection(pc.waveColors) == 1, isChecked(pc.waveReverse)
	density := displaySelection(ui.density)
	if density < 0 || density > int(poststackWaveDensityStride) {
		return w, fmt.Errorf("请选择有效的绘制密度")
	}
	w.Density = poststackWaveDensity(density)
	type field struct {
		input  uintptr
		output *float64
		name   string
		lo, hi float64
		open   bool
	}
	fields := []field{
		{ui.swing, &w.Swing, "显示摆幅", 0, 20, true},
		{pc.waveFields[1], &w.TimeStepMS, "显示时间步长", 0, 0, false},
	}
	if displaySelection(ui.refMode) == 1 {
		fields = append(fields, field{pc.waveFields[2], &w.Amplitude, "手动参考振幅", 0, 0, true})
	} else {
		w.Amplitude = 0
	}
	if w.Density == poststackWaveDensityAuto {
		fields = append(fields, field{pc.waveFields[0], &w.Spacing, "最小道距", 0, 0, false})
	}
	if w.Mode != poststackPlotVA {
		fields = append(fields, field{ui.lineWidth, &w.LineWidth, "线宽 (DIP)", 0.25, 6, false})
	}
	opacity := w.Opacity * 100
	if w.Mode != poststackPlotWiggle {
		fields = append(fields, field{ui.opacity, &opacity, "填充不透明度 (%)", 0, 100, false})
	}
	for _, f := range fields {
		v, err := poststackParamNumber(getText(f.input), true)
		if err != nil || v < f.lo || f.open && v == f.lo || f.hi > 0 && v > f.hi {
			if f.hi > 0 {
				return w, fmt.Errorf("%s须%s，不超过 %g", f.name, map[bool]string{true: fmt.Sprintf("大于 %g", f.lo), false: fmt.Sprintf("至少 %g", f.lo)}[f.open], f.hi)
			}
			if f.open {
				return w, fmt.Errorf("%s须为有限正数", f.name)
			}
			return w, fmt.Errorf("%s须为有限非负数", f.name)
		}
		*f.output = v
	}
	if w.Density == poststackWaveDensityStride {
		stride, err := strconv.ParseInt(strings.TrimSpace(getText(ui.stride)), 10, 32)
		if err != nil || stride < 1 || stride > 1000000 {
			return w, fmt.Errorf("K 须为 1–1000000 的整数")
		}
		w.Stride = int(stride)
	}
	w.Opacity = opacity / 100
	w.PositiveFill, w.NegativeFill = isChecked(ui.positive), isChecked(ui.negative)
	w.PositiveColor, w.NegativeColor = ui.colors[0], ui.colors[1]
	w.Baseline, w.VisualSet = isChecked(ui.baseline), true
	return w, nil
}

func poststackSubmitParamSurface(limited bool, lo, hi float64, limitDirty bool) error {
	if pc.surfaceIdentity == "" {
		return nil
	}
	owner, key := paramOwner, poststackParamKey(paramOwner)
	o := displayOwners[owner]
	if o == nil || key != pc.surfaceKey || o.surfaces[key] == nil || o.surfaces[key].s.Identity != pc.surfaceIdentity {
		return fmt.Errorf("当前剖面已经变化，请重新打开参数窗口")
	}
	e := o.surfaces[key]
	s, _ := displayCommitted(owner, key)
	changed := false
	for j, axis := range s.Axes[:min(2, len(s.Axes))] {
		axis = displayAxisFor(owner, key, e.s.Axes[j])
		for i, field := range []int{2 * j, 2*j + 1, 4 + j} {
			text := getText(pc.axes[field])
			if text == pc.axisInitial[field] {
				continue
			}
			v, err := poststackParamNumber(text, i == 2)
			if err != nil {
				return fmt.Errorf("%s：%w", axis.Title, err)
			}
			if i == 2 && e.s.equalTicks {
				if v < 1 || v > 20 || v != float64(int(v)) {
					return fmt.Errorf("%s：目标等分须为 1–20 的整数", axis.Title)
				}
				axis.Divisions = int(v)
			} else if i == 2 {
				axis.Step = v
			} else if i == 0 {
				axis.View.Min, axis.ViewEdited = axis.BaseValue(v), true
			} else {
				axis.View.Max, axis.ViewEdited = axis.BaseValue(v), true
			}
			changed = true
		}
		s.Axes[j] = axis
	}
	if limitDirty && owner == compareHwnd && compareMode != 2 && s.Color != nil {
		changed = true
		if limited {
			s.Color.SetRangePolicy(displaycore.ManualRange)
			s.Color.Range = displaycore.Range{Min: lo, Max: hi}
		} else if e.s.Color != nil {
			// Resume the normal per-view gain range, retaining palette/style.
			s.Color.RangePolicy, s.Color.Auto = displaycore.LegacyRange, false
			s.Color.Range = e.s.Color.Range
		}
	}
	if !changed {
		return nil
	}
	linked, follow := displayCompareOptions(owner, key)
	return displaySubmit(owner, key, pc.surfaceIdentity, s, linked, follow)
}
