//go:build windows

package main

import (
	"strings"
	"syscall"
	"unsafe"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
)

const compareChromeRowLeft = 416

func compareNavigationRects(width int) map[uintptr]RECT {
	measure, release := poststackToolbarFontMeasure(hFont)
	defer release()
	return compareNavigationRectsMeasured(width, compareMode, displayDPI(compareHwnd), measure)
}

func compareNavigationRectsMeasured(width, mode, dpi int, measure func(string) int) map[uintptr]RECT {
	const gap = 6
	x := 10
	r := make(map[uintptr]RECT)
	add := func(h uintptr, w, top, height int) {
		r[h] = RECT{int32(x), int32(top), int32(x + w), int32(top + height)}
		x += w + gap
	}
	add(cc.modeLabel, 38, 39, 19)
	add(cc.mode, 106, 35, 120)
	add(cc.prev, 30, 35, 23)
	add(cc.next, 30, 35, 23)
	// Reserve space for the actual actions first; the slider is the flexible field.
	resetWidth := poststackVectorButtonWidth(IDCMP_REFRESH, false, dpi, measure)
	geometryWidth := poststackVectorButtonWidth(IDCMP_AUTOGEOM, false, dpi, measure)
	reserve := resetWidth + geometryWidth + 3*gap + 10
	if mode == 2 {
		reserve += 128
	}
	add(cc.slider, min(375, max(70, width-x-reserve)), 34, 25)
	if mode == 2 {
		add(cc.timeEdit, 84, 35, 23)
		add(cc.timeUnit, 32, 39, 19)
	}
	buttonHeight := poststackToolbarBottom - 2
	add(cc.refresh, resetWidth, 34, buttonHeight)
	add(cc.autoGeom, geometryWidth, 34, buttonHeight)
	return r
}

func layoutCompareChromeNavigation(width int) {
	if compareHwnd == 0 || compareAPath == "" {
		return
	}
	for h, r := range compareNavigationRects(width) {
		if h != 0 {
			pMoveWindow.Call(h, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), 1)
		}
	}
}

type compareStatusRects struct{ coordinates, progress, status RECT }

func compareChromeStatusRects(width int, progress bool) compareStatusRects {
	x, right := compareChromeRowLeft, max(compareChromeRowLeft, width-10)
	leftWidth := min(560, (right-x)*45/100)
	split := min(right, x+leftWidth+6)
	out := compareStatusRects{coordinates: RECT{int32(x), 70, int32(x + leftWidth), 90}, status: RECT{int32(split), 70, int32(right), 90}}
	if progress {
		pw := min(166, max(0, (right-split)/3))
		out.progress = RECT{int32(split), 70, int32(split + pw), 87}
		out.status.Left = int32(min(right, split+pw+6))
	}
	return out
}

type compareTickNotice struct{ role, text string }

func compareNoticeText(entries []compareTickNotice) string {
	type grouped struct {
		roles []string
		text  string
	}
	groups := make([]grouped, 0, len(entries))
	for _, e := range entries {
		if e.text == "" {
			continue
		}
		match := -1
		for i, g := range groups {
			if g.text == e.text {
				match = i
				break
			}
		}
		if match < 0 {
			groups = append(groups, grouped{[]string{e.role}, e.text})
			continue
		}
		g := &groups[match]
		if g.roles[len(g.roles)-1] != e.role {
			g.roles = append(g.roles, e.role)
		}
	}
	messages := make([]string, len(groups))
	for i, g := range groups {
		messages[i] = strings.Join(g.roles, "/") + "：" + g.text
	}
	return strings.Join(messages, " | ")
}

func poststackTickNotice(plan displaycore.TickPlan) string {
	for _, s := range []string{plan.ValueError, plan.FormatWarning, plan.Warning} {
		if s != "" {
			return s
		}
	}
	return ""
}

// Status is derived from the current screen plans, never drawn into an image
// or exported bitmap. Do not regenerate geometry, samples or filters here.
func updateCompareDisplayNotice() {
	if cc.notice == 0 || compareAPath == "" {
		return
	}
	var notices []compareTickNotice
	for i, p := range seismicCompareLayouts() {
		panel := seismicComparePanel(i)
		if panel == nil || len(panel.bgra) == 0 {
			continue
		}
		role := [...]string{"A", "B", "A−B"}[i]
		notices = append(notices, compareTickNotice{role, poststackWaveFrameNotice(panel)})
		x, y := seismicCompareEffectiveAxes(i)
		for _, a := range []displaycore.Axis{x, y} {
			plan := p.AxisTicks[a.ID]
			if !a.HideAxis && !a.HideLabels {
				notices = append(notices, compareTickNotice{role, poststackTickNotice(plan)})
			}
			if a.ID == x.ID && (plan.SinglePoint || compareMode != 2 && len(panel.coords) == 1) {
				notices = append(notices, compareTickNotice{role, "当前剖面 1 道"})
			} else if a.ID == "time" && plan.SinglePoint {
				notices = append(notices, compareTickNotice{role, "当前时间窗 1 个样点"})
			}
		}
		if p.ColorRegion.Right > p.ColorRegion.Left {
			notices = append(notices, compareTickNotice{role, poststackTickNotice(p.ColorTicks)})
		}
	}
	if compareActiveB() && compareShowDiff && len(compareD.bgra) == 0 && compareDeltaReason != "" {
		notices = append(notices, compareTickNotice{"A−B", compareDeltaReason})
	}
	status := compareNoticeText(notices)
	if getText(cc.notice) != status {
		setText(cc.notice, status)
	}
	visible := status != ""
	if visible != compareNoticeVisible {
		compareNoticeVisible = visible
		layoutCompareStatusProgress()
	}
	cc.toolbar.setPathTip(compareHwnd, 3, cc.notice, status)
	cc.toolbar.setPathTip(compareHwnd, 2, cc.status, getText(cc.status))
}

func poststackShortcutAction(key uintptr) (gain, palette int) {
	switch key {
	case VK_Q:
		return 1, 0
	case VK_W:
		return -1, 0
	case VK_E:
		return 0, 1
	case VK_R:
		return 0, -1
	}
	return 0, 0
}

func poststackSceneColor(owner uintptr, key string) bool {
	return owner != 0 && (owner == compareHwnd && strings.HasPrefix(key, "poststack-") || owner == hwnd && key == mainDisplayKey)
}

func poststackLiveColorRange(saved, live displaycore.ColorMapping) displaycore.ColorMapping {
	// RangePolicy zero is the scene's legacy/default clipping range, not a
	// user lock. Saving typography or a gradient must not freeze that range.
	if saved.RangePolicy == displaycore.LegacyRange && !saved.Auto && live.Range.Valid() {
		saved.Range = live.Range
	}
	return saved
}

func poststackGainStatus() string {
	msg := "显示增益：" + formatNumber(gainPercent) + "%（Q 增加 / W 减少）"
	if poststackWaveActive() && poststackWaveDisplay.Amplitude > 0 {
		return msg + "；固定波形振幅参考优先。"
	}
	if s, _ := displayCommitted(compareHwnd, seismicCompareKey(0)); s.Color != nil && s.Color.RangePolicy != displaycore.LegacyRange {
		return msg + "；A 使用独立色标范围策略。可在参数 → 其它设置选择“改用 Q/W 增益范围”。"
	}
	return msg
}

// Accept the canvas and push/trackbar controls, never a text field, combo,
// owned dialog, or modified accelerator. Autorepeated keydowns stay intact.
func poststackShortcutFocus(target, owner uintptr, class string, modified bool) bool {
	if modified || owner == 0 || target == 0 {
		return false
	}
	if target == owner {
		return true
	}
	return strings.EqualFold(class, "Button") || strings.EqualFold(class, "msctls_trackbar32")
}

var poststackGetClassName = user32.NewProc("GetClassNameW")
var poststackGetKeyState = user32.NewProc("GetKeyState")
var poststackIMEVirtualKey = syscall.NewLazyDLL("imm32.dll").NewProc("ImmGetVirtualKey")

func translatePoststackDisplayShortcut(target, owner, key uintptr, cycle func(int)) bool {
	hasSource := sf != nil
	if owner == compareHwnd {
		hasSource = compareAPath != ""
	}
	const vkProcessKey = 0xE5
	gain, palette := poststackShortcutAction(key)
	if target == 0 || owner == 0 || (gain == 0 && palette == 0 && key != vkProcessKey) || !hasSource || loadHwnd != 0 {
		return false
	}
	modified := false
	for _, modifier := range []uintptr{0x11, 0x12, 0x5B, 0x5C} { // Ctrl, Alt, either Windows key
		state, _, _ := poststackGetKeyState.Call(modifier)
		modified = modified || uint16(state)&0x8000 != 0
	}
	class := ""
	if target != owner {
		child, _, _ := pIsChild.Call(owner, target)
		if child == 0 {
			return false
		}
		var buf [64]uint16
		n, _, _ := poststackGetClassName.Call(target, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if n == 0 || n >= uintptr(len(buf)) {
			return false
		}
		class = syscall.UTF16ToString(buf[:n])
	}
	if !poststackShortcutFocus(target, owner, class, modified) {
		return false
	}
	if key == vkProcessKey && poststackIMEVirtualKey.Find() == nil {
		physical, _, _ := poststackIMEVirtualKey.Call(target)
		gain, palette = poststackShortcutAction(physical)
	}
	if gain == 0 && palette == 0 {
		return false
	}
	if gain != 0 {
		changeGainPercent(float64(gain))
		if owner == compareHwnd {
			setText(cc.status, poststackGainStatus())
		}
	} else {
		cycle(palette)
	}
	return true
}
