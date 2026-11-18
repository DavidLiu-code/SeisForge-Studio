//go:build windows

package main

import (
	"fmt"
	"maps"
	"slices"
	"unsafe"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
)

type displayBatchEdit struct {
	key, identity, axis                     string
	color, setLink, link, setFollow, follow bool
	axesFollow                              *bool
	distance                                *displayDistanceSettings
	settings                                displaycore.Settings
}

type displayBatchTarget struct {
	surface                    *displaySurfaceState
	saved                      *displaycore.Settings
	legend, followAxes         bool
	next                       displaycore.Settings
	nextLegend, nextFollowAxes bool
}

// Preflight cloned settings, links and the one shared viewport before publishing
// or persisting. Do not replay one scene adapter for each appearance copy.
func displayCommitBatch(owner uintptr, edits []displayBatchEdit) error {
	if len(edits) == 0 {
		return nil
	}
	o := displayOwners[owner]
	if o == nil {
		return fmt.Errorf("视图已关闭")
	}
	targets := map[string]*displayBatchTarget{}
	links := maps.Clone(o.links)
	get := func(key string) *displayBatchTarget {
		if t := targets[key]; t != nil {
			return t
		}
		e := o.surfaces[key]
		if e == nil || e.order == 0 {
			return nil
		}
		s := displayDefaults(e.s)
		if e.saved != nil {
			s = e.saved.Clone()
		}
		s.Axes = displayIndependentAxes(owner, key)
		t := &displayBatchTarget{surface: e, saved: e.saved, legend: e.legendShown, followAxes: e.bAxesFollow, next: s, nextLegend: e.legendShown, nextFollowAxes: e.bAxesFollow}
		targets[key] = t
		return t
	}
	type viewportAxis struct {
		key  string
		axis displaycore.Axis
	}
	view := map[string]viewportAxis{}
	var distance *displayDistanceSettings
	var adapter *displaySurfaceState
	var adapterKey string
	var tones bool
	for _, edit := range edits {
		t := get(edit.key)
		if t == nil || t.surface.s.Identity != edit.identity {
			return fmt.Errorf("数据已更新，请重选待应用对象：%s", edit.key)
		}
		if !displaycore.ValidFontPT(edit.settings.PlotTitleFontPT) {
			return fmt.Errorf("图标题字号不合法")
		}
		base, _ := displayCommitted(owner, edit.key)
		if edit.settings.PlotTitleFontPT != base.PlotTitleFontPT {
			t.next.PlotTitleFontPT = edit.settings.PlotTitleFontPT
		}
		if edit.distance != nil {
			if !t.surface.s.distance {
				return fmt.Errorf("该对象不支持距离横轴")
			}
			if distance != nil && *distance != *edit.distance {
				return fmt.Errorf("多个草稿的共同距离设置冲突")
			}
			distance = edit.distance
		}
		if edit.axesFollow != nil {
			if available, _ := displayBAxisLink(owner, edit.key); !available {
				return fmt.Errorf("当前面板不能跟随 A 坐标样式")
			}
			t.nextFollowAxes = *edit.axesFollow
		}
		if c := t.surface.s.Compare; c != nil && (edit.setLink || edit.setFollow) {
			if !c.CanLink {
				return fmt.Errorf("当前数据不支持共用色标")
			}
			b := links[c.Keys]
			if edit.setLink {
				b.Linked = edit.link
			}
			if edit.setFollow {
				b.FollowA = edit.follow
			}
			links[c.Keys] = b
			tones = true
		}
		if edit.color {
			if edit.settings.Color == nil || t.next.Color == nil {
				return fmt.Errorf("该对象不支持色标")
			}
			m := edit.settings.Color.Clone()
			if err := m.Valid(); err != nil {
				return err
			}
			t.nextLegend = !m.Hidden
			tones = tones || !displayColorToneEqual(base.Color, &m)
			t.next.Color = &m
		} else {
			for _, axis := range edit.settings.Axes {
				if axis.ID != edit.axis {
					continue
				}
				if err := axis.Valid(); err != nil {
					return err
				}
				if axis.ViewEdited {
					if p, ok := view[axis.ID]; ok && p.axis.View != axis.View {
						return fmt.Errorf("共同坐标 %s 的范围在多个草稿中冲突", axis.Title)
					}
					view[axis.ID] = viewportAxis{edit.key, axis}
					if adapter == nil {
						adapter, adapterKey = t.surface, edit.key
					}
				}
				save := axis
				if t.surface.s.LiveViewport {
					save.ViewEdited = false
				}
				for i, a := range t.next.Axes {
					if a.ID == axis.ID {
						t.next.Axes[i] = save
					}
				}
			}
		}
	}
	// Re-resolve A against the prospective linked region when linking changes.
	for keys, link := range links {
		old := o.links[keys]
		if link.Linked == old.Linked {
			continue
		}
		t := get(keys[0])
		if t == nil || t.next.Color == nil {
			return fmt.Errorf("A 色标尚未就绪")
		}
		m := t.next.Color
		if m.EffectiveRangePolicy() != displaycore.ManualRange {
			displayRequestMappingStats(owner, keys[0], link.Linked)
			res, ready, err := displayResolveMappingRange(owner, keys[0], *m, link.Linked)
			if err != nil {
				return err
			}
			if !ready {
				return fmt.Errorf("正在计算共用色标统计，请稍后应用")
			}
			m.Range = res.Range
		}
	}
	if distance != nil {
		if err := displayValidateDistanceSettings(owner, *distance); err != nil {
			return err
		}
	}
	var applyView displaycore.Settings
	if adapter != nil {
		if adapter.s.Apply == nil {
			return fmt.Errorf("当前视窗不能应用坐标范围")
		}
		applyView = targets[adapterKey].next.Clone()
		for id, p := range view {
			found := false
			for i, a := range applyView.Axes {
				if a.ID != id {
					continue
				}
				if a.CanonicalUnit() != p.axis.CanonicalUnit() || !a.Data.Valid() || p.axis.View.Min < a.Data.Min || p.axis.View.Max > a.Data.Max {
					return fmt.Errorf("坐标 %s 的共同范围超出数据边界", p.axis.Title)
				}
				a.View, a.ViewEdited = p.axis.View, true
				applyView.Axes[i], found = a, true
			}
			if !found {
				return fmt.Errorf("坐标 %s 与当前对象不兼容", p.axis.Title)
			}
		}
	}
	oldLinks := o.links
	rollback := func() {
		o.links = oldLinks
		for _, t := range targets {
			t.surface.saved, t.surface.legendShown, t.surface.bAxesFollow = t.saved, t.legend, t.followAxes
		}
	}
	o.links = links
	for _, t := range targets {
		t.surface.saved, t.surface.legendShown, t.surface.bAxesFollow = &t.next, t.nextLegend, t.nextFollowAxes
	}
	if adapter != nil {
		if err := adapter.s.Apply(applyView); err != nil {
			rollback()
			return err
		}
	}
	if distance != nil {
		if err := displaySetDistanceSettings(owner, *distance); err != nil {
			rollback()
			return err
		}
	}
	if tones && adapter == nil {
		if owner == compareHwnd {
			seismicRefreshCompareColors()
		} else if owner == hwnd && targets[mainDisplayKey] != nil {
			applyPalette()
		}
	}
	displayLoadPrefs()
	for key, t := range targets {
		p := t.next.Clone()
		for i := range p.Axes {
			p.Axes[i].View, p.Axes[i].Data, p.Axes[i].ViewEdited = displaycore.Range{}, displaycore.Range{}, false
		}
		if p.Color != nil {
			p.Color.Range = displaycore.Range{Min: -1, Max: 1}
			displayForgetLegendVisibility(p.Color)
		}
		displayPrefs.Views[key] = p
	}
	displayWritePrefs()
	displayCompareLayoutCache.valid = false
	displayInvalidateProfile(owner)
	return nil
}

func displayEditorUseBatch(w *displayEditor) bool {
	e := displayEditorSurface(w)
	return e != nil && (w.key == mainDisplayKey || e.s.Compare != nil && e.s.Compare.PoststackProfiles)
}

// The existing Win32 controls are bound without painting or launching stats
// while preflighting each typed draft. The user's active page is restored.
func displayEditorVisit(w *displayEditor, d displayDraft, fn func()) {
	restore := displayCaptureDraft(w)
	oldSection, oldError, oldNode := w.section, w.stateError, w.nodeEditError
	was := w.visiting
	w.visiting = true
	defer func() {
		w.key, w.identity, w.axis, w.pageColor = restore.key, restore.identity, restore.axis, restore.color
		w.section = oldSection
		displayLoadDraft(w)
		displayRestoreDraftControls(w, restore)
		w.stateError, w.nodeEditError = oldError, oldNode
		w.visiting = was
	}()
	w.key, w.identity, w.axis, w.pageColor = d.key, d.identity, d.axis, d.color
	displayLoadDraft(w)
	if d.text != nil {
		displayRestoreDraftControls(w, d)
	}
	if !w.pageColor {
		w.unit.Unit = getText(w.controls[deUnit])
	}
	fn()
}

func displayApplyEditor(w *displayEditor, closeAfter bool) {
	if !displayEditorUseBatch(w) {
		displayApplyEditorPage(w, closeAfter)
		return
	}
	if w.invalid {
		setText(w.status, "数据已更新，不能提交旧对象的草稿。")
		return
	}
	displayStoreDraft(w)
	order := slices.Sorted(maps.Keys(w.drafts))
	var edits []displayBatchEdit
	for _, k := range order {
		d := w.drafts[k]
		if e := displayOwners[w.owner].surfaces[d.key]; e == nil || e.order == 0 || e.s.Identity != d.identity {
			setText(w.status, "有草稿所属数据已更新，未应用任何设置。")
			return
		}
		n := len(edits)
		displayEditorVisit(w, d, func() {
			w.collect = func(p displayBatchEdit) { edits = append(edits, p) }
			defer func() { w.collect = nil }()
			displayApplyEditorPage(w, false)
		})
		if len(edits) == n {
			return
		}
	}
	if err := displayCommitBatch(w.owner, edits); err != nil {
		setText(w.status, err.Error())
		return
	}
	clear(w.drafts)
	clear(w.compareFollowDraft)
	w.compareModeDirty = false
	w.stateError = ""
	if closeAfter {
		displayDestroyEditor(w)
		return
	}
	displayLoadDraft(w)
}

type displayCopyFields uint8

const (
	displayCopyType displayCopyFields = 1 << iota
	displayCopyTitle
	displayCopyGradient
	displayCopyRange
)

func displayEditorCopyTo(w *displayEditor, e *displaySurfaceState) {
	if w.invalid || e.s.Compare == nil || !e.s.Compare.PoststackProfiles {
		return
	}
	panels := displayEditorCompareTargets(w, e.s.Compare)
	if len(panels) < 2 {
		return
	}
	var chosen [3]bool
	for _, p := range panels {
		if e.s.Compare.Keys[p] != w.key {
			chosen[p] = true
			break
		}
	}
	fields := displayCopyType
	fieldRows := []struct {
		title string
		flag  displayCopyFields
	}{
		{"刻度、字体与数字格式", displayCopyType}, {"标题和单位", displayCopyTitle},
	}
	if w.pageColor {
		fieldRows = append(fieldRows, struct {
			title string
			flag  displayCopyFields
		}{"色表及反转", displayCopyGradient}, struct {
			title string
			flag  displayCopyFields
		}{"精确色标端点（转手动）", displayCopyRange})
	}
	var r RECT
	algorithmGetWindowRect.Call(w.controls[deApplyPanels], uintptr(unsafe.Pointer(&r)))
	for {
		menu, _, _ := displayCreatePopup.Call()
		if menu == 0 {
			return
		}
		item := func(id, flags uintptr, title string) {
			displayAppendMenu.Call(menu, flags, id, uintptr(unsafe.Pointer(u16(title))))
		}
		item(0, 1, "选择目标面板")
		for _, i := range panels {
			if e.s.Compare.Keys[i] == w.key {
				continue
			}
			flag := uintptr(0)
			if chosen[i] {
				flag = 8
			}
			item(uintptr(10+i), flag, [...]string{"A", "B", "差值"}[i])
		}
		item(0, 0x800, "")
		item(0, 1, "选择要复制的字段")
		for j, p := range fieldRows {
			flag := uintptr(0)
			if fields&p.flag != 0 {
				flag = 8
			}
			item(uintptr(20+j), flag, p.title)
		}
		item(0, 0x800, "")
		flags := uintptr(0)
		if fields == 0 || chosen == [3]bool{} {
			flags = 1
		}
		item(1, flags, "暂存复制")
		selected, _, _ := displayTrackPopup.Call(menu, 0x0100|0x0080, uintptr(r.Left), uintptr(r.Bottom), 0, w.handle, 0)
		displayDestroyMenu.Call(menu)
		switch {
		case selected == 0:
			return
		case selected == 1:
			if err := displayEditorStageCopy(w, e.s.Compare.Keys, chosen, fields); err != nil {
				w.stateError = err.Error()
			}
			displayEditorRefreshFooter(w)
			return
		case selected >= 10 && selected < 13:
			chosen[selected-10] = !chosen[selected-10]
		case selected >= 20 && int(selected-20) < len(fieldRows):
			fields ^= fieldRows[selected-20].flag
		}
	}
}

func displayEditorStageCopy(w *displayEditor, keys [3]string, selected [3]bool, fields displayCopyFields) error {
	e := displayEditorSurface(w)
	o := displayOwners[w.owner]
	if e == nil || e.s.Compare == nil || o == nil || fields == 0 {
		return fmt.Errorf("没有可复制的显示设置")
	}
	displayStoreDraft(w)
	source := displayCaptureDraft(w)
	var exact displaycore.Range
	if fields&displayCopyRange != 0 {
		s, _ := displayCommitted(w.owner, w.key)
		if s.Color == nil || !w.pageColor {
			return fmt.Errorf("只有色标端点可复制")
		}
		m, err := displayDraftMapping(w, *s.Color)
		if err != nil {
			return err
		}
		exact = m.Range
	}
	tone := fields & (displayCopyGradient | displayCopyRange)
	linked, follow := displayCompareOptions(w.owner, w.key)
	if b := o.surfaces[keys[1]]; b != nil {
		if v, ok := w.compareFollowDraft[displayEditorFollowKey{key: keys[1], identity: b.s.Identity, color: true}]; ok {
			linked = v
		}
	}
	if d := o.surfaces[keys[2]]; d != nil {
		if v, ok := w.compareFollowDraft[displayEditorFollowKey{key: keys[2], identity: d.s.Identity, color: true}]; ok {
			follow = v
		}
	}
	local := fields &^ (displayCopyGradient | displayCopyRange)
	var masks [3]displayCopyFields
	for i, yes := range selected {
		if yes {
			masks[i] = local
			if tone == 0 {
				continue
			}
			if i == 2 && follow {
				return fmt.Errorf("差值色标正在跟随 A；先解除或编辑 A")
			}
			if linked && i < 2 {
				masks[0] |= tone
			} else {
				masks[i] |= tone
			}
		}
	}
	prepared := maps.Clone(w.drafts)
	if prepared == nil {
		prepared = map[string]displayDraft{}
	}
	var active *displayDraft
	for i, mask := range masks {
		if mask == 0 {
			continue
		}
		key := keys[i]
		dst := o.surfaces[key]
		if dst == nil || dst.order == 0 {
			return fmt.Errorf("待复制的面板已关闭")
		}
		d := displayDraft{key: key, identity: dst.s.Identity, axis: w.axis, color: w.pageColor}
		var stageErr error
		displayEditorVisit(w, d, func() {
			if !w.pageColor {
				if w.axis != source.axis {
					stageErr = fmt.Errorf("目标坐标轴不兼容")
					return
				}
				if displayEditorPoststackLinked(w) {
					stageErr = fmt.Errorf("目标坐标样式正在跟随 A；先解除或编辑 A")
					return
				}
			}
			if mask&displayCopyRange != 0 && displayChecked(w.controls[deSym]) && exact.Min != -exact.Max {
				stageErr = fmt.Errorf("目标零对称设置与精确端点冲突，请先解除零对称")
				return
			}
			w.loading = true
			if mask&displayCopyType != 0 {
				for _, id := range []int{deDigits, deTickLength, deTickDivisions, deTickFont, deTitleFont, dePlotTitleFont, deStep} {
					displayField(w, id, source.text[id])
				}
				displayCheck(w.controls[deTickAuto], source.checks[deTickAuto])
				pSendMessageW.Call(w.controls[deFormat], CB_SETCURSEL, uintptr(source.selects[deFormat]), 0)
			}
			if mask&displayCopyTitle != 0 {
				if !w.pageColor {
					unit := source.text[deUnit]
					a := w.unit
					a.Unit = unit
					if a.Valid() != nil || w.unit.BaseUnit == "m" && unit != w.unit.Unit {
						stageErr = fmt.Errorf("轴单位不兼容或由距离轴自动控制")
						w.loading = false
						return
					}
					displayField(w, deUnit, unit)
					displayEditorChangeUnit(w)
				} else {
					displayField(w, deUnit, source.text[deUnit])
				}
				displayField(w, deTitle, source.text[deTitle])
			}
			if mask&displayCopyGradient != 0 {
				w.stops = append([]displaycore.GradientStop(nil), source.stops...)
				if src := o.surfaces[source.key]; src != nil && source.color {
					if source.key == keys[1] && linked || source.key == keys[2] && follow {
						if s, _ := displayCommitted(w.owner, keys[0]); s.Color != nil {
							w.stops = append([]displaycore.GradientStop(nil), s.Color.Stops...)
							source.checks[deReverse] = s.Color.Reverse
						}
					}
				}
				displayCheck(w.controls[deReverse], source.checks[deReverse])
				pSendMessageW.Call(w.controls[dePreset], CB_SETCURSEL, 0, 0)
			}
			if mask&displayCopyRange != 0 {
				displayEditorSetPolicy(w, displaycore.ManualRange)
				displayEditorShowBounds(w, exact, true)
			}
			w.loading = false
			staged := displayCaptureDraft(w)
			prepared[displayDraftKey(w)] = staged
			if staged.key == source.key {
				active = &staged
			}
		})
		if stageErr != nil {
			return stageErr
		}
	}
	w.drafts = prepared
	if active != nil {
		displayRestoreDraftControls(w, *active)
	}
	displayEditorRefreshViewport(w)
	return nil
}
