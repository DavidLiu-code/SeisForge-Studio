//go:build windows

package main

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
)

// Font and tick-length controls do not decide the legality of the coordinate
// range or of N. The preview can use the last good value of just the invalid
// field; Apply still uses the same strict parser.
func displayEditorTypographyValue(w *displayEditor, id int, fallback float64, preview bool) (float64, error) {
	v, err := displayParseNumber(getText(w.controls[id]), false)
	if id == deTickLength {
		if err != nil || !displaycore.ValidMajorTickDIP(v) {
			err = fmt.Errorf("刻度长度必须在 0–%d DIP 之间", displaycore.MaxMajorTickDIP)
		}
	} else if err != nil || !displaycore.ValidEnteredFontPT(v) {
		err = fmt.Errorf("字号必须在 %d–%d pt 之间", displaycore.MinFontPT, displaycore.MaxFontPT)
	}
	if !preview {
		return v, err
	}
	key := displayDraftKey(w)
	if err == nil {
		if !w.loading && !w.visiting {
			if w.lastValidTypography == nil {
				w.lastValidTypography = make(map[string]map[int]float64)
			}
			if w.lastValidTypography[key] == nil {
				w.lastValidTypography[key] = make(map[int]float64)
			}
			w.lastValidTypography[key][id] = v
		}
		return v, nil
	}
	if last, ok := w.lastValidTypography[key][id]; ok {
		return last, nil
	}
	return fallback, nil
}

func displayEditorDigits(w *displayEditor, format displaycore.NumberFormat, saved int, preview bool) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(getText(w.controls[deDigits])))
	if err == nil && n >= 0 && n <= 12 {
		return n, nil
	}
	if preview || !displaycore.UsesFractionalDigits(format) {
		// The disabled editor never has authority to erase a saved precision.
		return saved, nil
	}
	return saved, fmt.Errorf("精度必须是 0–12 的整数")
}

func displayEditorParseDivisions(w *displayEditor) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(getText(w.controls[deTickDivisions])))
	if err != nil || n < displaycore.MinTickDivisions || n > displaycore.MaxTickDivisions {
		return 0, fmt.Errorf("等分段数必须是 %d–%d 的整数", displaycore.MinTickDivisions, displaycore.MaxTickDivisions)
	}
	return n, nil
}

func displayEditorRefreshTypographyHints(w *displayEditor) string {
	if w.visiting {
		return ""
	}
	axisFollows := !w.pageColor && displayEditorPoststackLinked(w)
	equal := displayEditorUsesEqualTicks(w)
	titleCaption := "坐标轴标题字号 (pt)"
	if w.pageColor {
		titleCaption = "色标标题字号 (pt)"
	}
	fields := []struct {
		id      int
		caption string
		active  bool
	}{
		{deTickLength, "主刻度长度 (DIP)", !axisFollows},
		{deTickFont, "刻度字号 (pt)", equal && !axisFollows},
		{deTitleFont, titleCaption, equal && !axisFollows},
		{dePlotTitleFont, "剖面标题字号 (pt)", equal},
	}
	var issue string
	for _, field := range fields {
		caption := field.caption
		if field.active {
			if _, err := displayEditorTypographyValue(w, field.id, 0, false); err != nil {
				if field.id == deTickLength {
					caption += fmt.Sprintf(" · 应为 0–%d", displaycore.MaxMajorTickDIP)
				} else {
					caption += fmt.Sprintf(" · 应为 %d–%d", displaycore.MinFontPT, displaycore.MaxFontPT)
				}
				if issue == "" {
					issue = field.caption + "：" + err.Error()
				}
			}
		}
		if h := w.labels[field.id]; h != 0 && getText(h) != caption {
			setText(h, caption)
		}
	}
	if !axisFollows {
		f := displayEditorFormat(displaySelection(w.controls[deFormat]))
		if _, err := displayEditorDigits(w, f, 0, false); err != nil && issue == "" {
			issue = err.Error()
		}
		if equal && issue == "" {
			if _, err := displayEditorParseDivisions(w); err != nil {
				issue = err.Error()
			}
		}
	}
	return issue
}

// Short UI strings never replace these canonical bounds unless edited.
type displayDraftBounds struct {
	exact  displaycore.Range
	shown  [2]string
	forced bool
	last   int
}

func displayDraftEquivalent(a, b displayDraft) bool {
	if !slices.Equal(a.stops, b.stops) || !maps.Equal(a.selects, b.selects) || !maps.Equal(a.checks, b.checks) || a.distance != b.distance || a.spacing != b.spacing ||
		a.bounds.forced != b.bounds.forced || a.bounds.forced && a.bounds.exact != b.bounds.exact {
		return false
	}
	for _, id := range displayTextIDs {
		if id == deMin || id == deMax || id == dePositionLow || id == dePositionHigh || id == deStep && a.checks[deTickAuto] {
			continue
		}
		if a.text[id] != b.text[id] {
			return false
		}
	}
	for i, id := range [2]int{deMin, deMax} {
		if a.text[id] != a.bounds.shown[i] && a.text[id] != b.text[id] {
			return false
		}
	}
	return true
}

func displayEditorFollowChanged(w *displayEditor) bool {
	if w.compareModeDirty && w.pageColor {
		return true
	}
	e := displayEditorSurface(w)
	v, ok := w.compareFollowDraft[displayEditorFollowKeyFor(w)]
	if !ok || e == nil || e.s.Compare == nil {
		return false
	}
	if !w.pageColor {
		_, old := displayBAxisLink(w.owner, w.key)
		return v != old
	}
	link, follow := displayCompareOptions(w.owner, w.key)
	if w.key == e.s.Compare.Keys[1] {
		return v != link
	}
	return v != follow
}

func displayEditorShowBounds(w *displayEditor, r displaycore.Range, edited bool) {
	lo, hi := r.Min, r.Max
	if !w.pageColor {
		lo, hi = w.unit.DisplayValue(lo), w.unit.DisplayValue(hi)
	}
	w.exactBounds = displayDraftBounds{exact: r, shown: [2]string{displaycore.FormatCompactNumber(lo), displaycore.FormatCompactNumber(hi)}, forced: edited}
	w.initial = w.exactBounds.shown
	if edited {
		w.initial = [2]string{}
	}
	was := w.loading
	w.loading = true
	displayField(w, deMin, w.exactBounds.shown[0])
	displayField(w, deMax, w.exactBounds.shown[1])
	displayEditorSyncAxisPositions(w, r)
	w.loading = was
}

func displayEditorTypedBounds(w *displayEditor, axis *displaycore.Axis) (displaycore.Range, bool, error) {
	b := w.exactBounds
	out := [2]float64{b.exact.Min, b.exact.Max}
	var edited [2]bool
	for i, id := range [2]int{deMin, deMax} {
		t := getText(w.controls[id])
		if t == b.shown[i] {
			continue
		}
		v, err := displayParseNumber(t, false)
		if err != nil {
			return displaycore.Range{}, true, fmt.Errorf("%s必须是有限数值", [...]string{"最小值", "最大值"}[i])
		}
		if axis != nil {
			v = axis.BaseValue(v)
		}
		out[i], edited[i] = v, true
	}
	r := displaycore.Range{Min: out[0], Max: out[1]}
	dirty := b.forced || edited[0] || edited[1]
	if axis == nil && displayChecked(w.controls[deSym]) {
		if edited[0] || edited[1] {
			n := min(max(0, b.last), 1)
			if !edited[n] {
				n = 1 - n
			}
			s, err := displaycore.SymmetricRangeFromEndpoint(out[n])
			return s, true, err
		}
		if r.Valid() {
			r = r.Symmetric()
		}
	}
	valid := r.Valid()
	if axis != nil {
		valid = axis.ValidRange(r)
	}
	if !valid && (axis == nil || !axis.StyleOnly || dirty) {
		if axis != nil && axis.AllowsPoint {
			return r, dirty, fmt.Errorf("范围最小值不得大于最大值")
		}
		return r, dirty, fmt.Errorf("范围最小值必须小于最大值")
	}
	return r, dirty, nil
}

func displayEditorRangePolicy(w *displayEditor) displaycore.RangePolicy {
	switch displaySelection(w.controls[deRangePolicy]) {
	case 0:
		return displaycore.FullRange
	case 1:
		return displaycore.PercentileRange
	default:
		return displaycore.ManualRange
	}
}

func displayEditorSetPolicy(w *displayEditor, policy displaycore.RangePolicy) {
	n := 2
	if policy == displaycore.FullRange {
		n = 0
	}
	if policy == displaycore.PercentileRange {
		n = 1
	}
	pSendMessageW.Call(w.controls[deRangePolicy], CB_SETCURSEL, uintptr(n), 0)
	displayCheck(w.controls[deAuto], n != 2)
}

func displayEditorReadPolicy(w *displayEditor, m *displaycore.ColorMapping) error {
	policy := displayEditorRangePolicy(w)
	if policy != m.EffectiveRangePolicy() {
		m.SetRangePolicy(policy)
	}
	if policy == displaycore.PercentileRange {
		lo, a := displayParseNumber(getText(w.controls[dePercentLow]), false)
		hi, b := displayParseNumber(getText(w.controls[dePercentHigh]), false)
		if a != nil || b != nil || !displaycore.ValidPercentiles(lo, hi) {
			return fmt.Errorf("分位数必须满足 0 ≤ 低分位 < 高分位 ≤ 100")
		}
		m.LowPercentile, m.HighPercentile = lo, hi
	}
	return nil
}

func displayEditorSetPercentileFields(w *displayEditor, m displaycore.ColorMapping) {
	lo, hi := m.LowPercentile, m.HighPercentile
	if !displaycore.ValidPercentiles(lo, hi) {
		lo, hi = 1, 99
	}
	choice := 5
	for i, keep := range []float64{90, 95, 98, 99, 100} {
		a, b, _ := displaycore.CentralPercentiles(keep)
		if a == lo && b == hi {
			choice = i
			break
		}
	}
	pSendMessageW.Call(w.controls[dePercentPreset], CB_SETCURSEL, uintptr(choice), 0)
	displayField(w, dePercentLow, displaycore.FormatExactNumber(lo))
	displayField(w, dePercentHigh, displaycore.FormatExactNumber(hi))
}

func displayEditorSetRangeDraft(w *displayEditor, r displaycore.Range, drag displaycore.RangeDragMode) {
	if w.pageColor {
		if displayEditorColorFollowsA(w, displayEditorSurface(w)) {
			return
		}
		if displayChecked(w.controls[deSym]) {
			switch drag {
			case displaycore.DragMinimum:
				r, _ = displaycore.SymmetricRangeFromEndpoint(r.Min)
			case displaycore.DragMaximum:
				r, _ = displaycore.SymmetricRangeFromEndpoint(r.Max)
			default:
				r = r.Symmetric()
			}
		}
		if !r.Valid() {
			return
		}
		displayEditorSetPolicy(w, displaycore.ManualRange)
	}
	if r.Valid() {
		displayEditorShowBounds(w, r, true)
		displayEditorRefreshRangeStatus(w)
		displayEditorRefreshFooter(w)
	}
}

func displayEditorPositionReference(w *displayEditor) (displaycore.Range, bool) {
	if !w.pageColor {
		return w.unit.Data, w.unit.Data.Valid()
	}
	state := displayEditorResolveRangeState(w)
	return state.Reference, state.ReferenceValid
}

// Applied is the current renderer mapping. Draft uses A if the pending
// editor selection follows A. Resolution is valid only when Ready; an
// unresolved candidate must not masquerade as Applied.
type displayEditorRangeState struct {
	Applied, Draft               displaycore.ColorMapping
	Resolution                   displaycore.RangeResolution
	Distribution                 displaycore.RangeDistribution
	Reference                    displaycore.Range
	HasColor, Ready, Following   bool
	ReferenceValid, BoundsEdited bool
	CoveragePending              bool
	Err                          error
	AppliedError                 string
}

func displayEditorResolveRangeState(w *displayEditor) displayEditorRangeState {
	var state displayEditorRangeState
	if w == nil || !w.pageColor {
		return state
	}
	e := displayEditorSurface(w)
	if e == nil {
		return state
	}
	committed, _ := displayCommitted(w.owner, w.key)
	if committed.Color == nil {
		return state
	}
	state.HasColor = true
	state.AppliedError = e.mappingError
	o := displayOwners[w.owner]
	appliedLinked, appliedFollow := displayCompareOptions(w.owner, w.key)
	appliedFollower := false
	if b := e.s.Compare; b != nil {
		appliedFollower = (w.key == b.Keys[1] && appliedLinked) || (w.key == b.Keys[2] && appliedFollow)
		if appliedFollower {
			if a := o.surfaces[b.Keys[0]]; a != nil {
				state.AppliedError = a.mappingError
			}
		}
	}
	state.Applied, state.Draft = committed.Color.Clone(), committed.Color.Clone()
	if state.Applied.EffectiveRangePolicy() == displaycore.ManualRange {
		state.AppliedError = ""
	}
	m := &state.Draft
	state.Following = displayEditorColorFollowsA(w, e)
	linked := displayEditorCompareLinked(w)
	key := w.key
	if state.Following {
		key = e.s.Compare.Keys[0]
		source, _ := displayCommitted(w.owner, key)
		if source.Color == nil {
			state.Err = fmt.Errorf("A 的映射尚未就绪")
			return state
		}
		state.Draft = source.Color.Clone()
		displayCopyLegendStyle(m, &state.Applied)
	}
	distribution, statsReady, statsErr := displayMappingDistribution(w.owner, key, linked)
	state.CoveragePending = !statsReady && statsErr == nil
	if statsReady && statsErr == nil {
		state.Distribution = distribution
		state.Reference = distribution.Range
		state.ReferenceValid = distribution.Count != 0 && distribution.Range.Valid()
	}
	if state.Following {
		// Never resolve a follower's own percentile to replace A's
		// committed tone. Retain the respective coverage population for Δ.
		coverage, ready, err := displayMappingDistribution(w.owner, w.key, linked)
		state.CoveragePending = !ready && err == nil
		if !ready || err != nil {
			coverage = displaycore.RangeDistribution{}
		}
		state.Resolution = displaycore.RangeResolution{
			Range: m.Range, Candidate: m.Range, Policy: m.EffectiveRangePolicy(), Coverage: coverage.Coverage(m.Range),
		}
		state.Ready = m.Range.Valid()
		return state
	}
	m.Reverse = displayChecked(w.controls[deReverse])
	m.Symmetric = displayChecked(w.controls[deSym])
	m.Stops = append([]displaycore.GradientStop(nil), w.stops...)
	if state.Err = displayEditorReadPolicy(w, m); state.Err != nil {
		return state
	}
	policy := m.EffectiveRangePolicy()
	if policy == displaycore.ManualRange {
		m.Range, state.BoundsEdited, state.Err = displayEditorTypedBounds(w, nil)
		if state.Err != nil {
			return state
		}
		if state.BoundsEdited && poststackSceneColor(w.owner, w.key) {
			m.SetRangePolicy(displaycore.ManualRange)
		}
		if !state.BoundsEdited && m.Symmetric == state.Applied.Symmetric &&
			policy == state.Applied.EffectiveRangePolicy() && w.exactBounds.exact == state.Applied.Range {
			// An untouched manual mapping is what is on screen, including
			// legacy settings. Presentation never normalizes it again.
			m.Range = state.Applied.Range
			state.Resolution = displaycore.RangeResolution{
				Range: m.Range, Candidate: m.Range, Policy: policy, Coverage: state.Distribution.Coverage(m.Range),
			}
			state.Ready = true
			return state
		}
	} else {
		unchangedPolicy := policy == state.Applied.EffectiveRangePolicy() && m.Symmetric == state.Applied.Symmetric &&
			(policy != displaycore.PercentileRange || (m.LowPercentile == state.Applied.LowPercentile && m.HighPercentile == state.Applied.HighPercentile))
		if unchangedPolicy && !appliedFollower &&
			displayMappingScopes(o, w.key, linked) == displayMappingScopes(o, w.key, appliedLinked) {
			// Opening the settings or editing only annotations is not a
			// range operation. Even if new statistics are pending or could
			// produce different endpoints, reflect what the renderer has
			// actually installed; the automatic scene transaction owns that
			// future update.
			m.Range = state.Applied.Range
			state.Resolution = displaycore.RangeResolution{
				Range: m.Range, Candidate: m.Range, Policy: policy, Coverage: state.Distribution.Coverage(m.Range),
			}
			state.Ready = m.Range.Valid()
			return state
		}
		if !statsReady || statsErr != nil {
			state.Err = statsErr
			return state
		}
	}
	state.Resolution, state.Err = m.ResolveMappingRange(state.Distribution)
	state.Ready = state.Err == nil && state.Resolution.Range.Valid()
	if state.Ready {
		m.Range = state.Resolution.Range
	}
	return state
}

func displayEditorSyncRangeFields(w *displayEditor, state displayEditorRangeState) {
	if !state.Ready || !state.Resolution.Range.Valid() {
		return
	}
	policy := state.Draft.EffectiveRangePolicy()
	untouched := getText(w.controls[deMin]) == w.exactBounds.shown[0] && getText(w.controls[deMax]) == w.exactBounds.shown[1]
	r := state.Resolution.Range
	if state.Following {
		if !untouched || w.exactBounds.exact != r || w.exactBounds.forced {
			displayEditorShowBounds(w, r, false)
		}
		return
	}
	if !untouched {
		// Leave the caret and text alone. The existing focus-loss handler
		// finishes a typed symmetric endpoint when the edit is valid.
		return
	}
	if policy != displaycore.ManualRange {
		if w.exactBounds.exact != r || w.exactBounds.forced {
			displayEditorShowBounds(w, r, false)
		}
	} else if state.Draft.Symmetric && w.exactBounds.exact != r {
		// Checking symmetry is a deliberate draft edit; keep the exact
		// derived endpoints even though only short text is displayed.
		displayEditorShowBounds(w, r, true)
	}
}

func displayEditorRefreshRangeStatus(w *displayEditor) {
	if w.visiting || w.controls[deCoverage] == 0 {
		return
	}
	var state displayEditorRangeState
	scope, referenceOK := w.unit.Data, w.unit.Data.Valid()
	if w.pageColor {
		state = displayEditorResolveRangeState(w)
		scope, referenceOK = state.Reference, state.ReferenceValid
		displayEditorSyncRangeFields(w, state)
	}
	for _, id := range []int{dePositionLow, dePositionHigh, deApplyPosition} {
		displayEnable(w.controls[id], referenceOK && !state.Following && (w.pageColor || !w.unit.StyleOnly))
	}
	w.statsWaiting = false
	status := ""
	if w.pageColor && state.HasColor {
		if state.Following {
			status = "跟随 A · 标题和单位仍可独立编辑。\r\n"
		}
		m, r := state.Draft, state.Resolution
		if state.Err != nil {
			status += state.Err.Error()
		} else if !state.Ready {
			w.statsWaiting = true
			status += "正在计算草稿范围…"
		} else {
			prefix := "已应用范围："
			if r.Range != state.Applied.Range || m.EffectiveRangePolicy() != state.Applied.EffectiveRangePolicy() ||
				m.Symmetric != state.Applied.Symmetric ||
				m.EffectiveRangePolicy() == displaycore.PercentileRange && (m.LowPercentile != state.Applied.LowPercentile || m.HighPercentile != state.Applied.HighPercentile) {
				prefix = "草稿范围："
			}
			status += prefix + displaycore.FormatCompactNumber(r.Range.Min) + " ～ " + displaycore.FormatCompactNumber(r.Range.Max)
			if m.EffectiveRangePolicy() == displaycore.PercentileRange {
				status += fmt.Sprintf("  (P%s–P%s)", displaycore.FormatCompactNumber(m.LowPercentile), displaycore.FormatCompactNumber(m.HighPercentile))
			}
			if m.Symmetric {
				status += " · 零对称"
			}
			if r.Coverage.Valid {
				if r.Coverage.Estimated || r.Estimated {
					status += " · 抽样估算"
				}
				status += fmt.Sprintf("\r\n低端 %.2g%%  高端 %.2g%%  覆盖 %.4g%%", r.Coverage.LowSaturation, r.Coverage.HighSaturation, r.Coverage.Covered)
			} else if state.CoveragePending {
				status += "\r\n正在统计覆盖率…"
			}
			if referenceOK && (r.Range.Min < scope.Min || r.Range.Max > scope.Max) {
				status += " · 超出数据跨度"
			}
		}
	} else if !w.pageColor && !referenceOK {
		status = "完整坐标范围没有可换算的跨度。"
	}
	if getText(w.controls[deCoverage]) != status {
		setText(w.controls[deCoverage], status)
	}
}

func displayEditorCopyExact(w *displayEditor) {
	var r displaycore.Range
	var err error
	if w.pageColor {
		state := displayEditorResolveRangeState(w)
		if !state.HasColor {
			return
		}
		if !state.Ready && state.Err == nil {
			err = fmt.Errorf("草稿范围尚未就绪")
		} else {
			err = state.Err
		}
		r = state.Resolution.Range
	} else {
		r, _, err = displayEditorTypedBounds(w, &w.unit)
		r = displaycore.Range{Min: w.unit.DisplayValue(r.Min), Max: w.unit.DisplayValue(r.Max)}
	}
	if err != nil {
		setText(w.status, err.Error())
		return
	}
	strValue := "最小值\t" + displaycore.FormatExactNumber(r.Min) + "\r\n最大值\t" + displaycore.FormatExactNumber(r.Max)
	if plan, unit, err := displayEditorDraftTicks(w); err == nil && plan.Step > 0 {
		label := "计算间隔"
		if plan.IntegerPositions {
			label = "目标等分间隔"
		}
		strValue += "\r\n" + label + "\t" + displaycore.FormatExactNumber(plan.Step)
		if unit != "" {
			strValue += " " + unit
		}
	}
	str, _ := syscall.UTF16FromString(strValue)
	if opened, _, _ := traceClipboardOpen.Call(w.handle); opened != 0 {
		defer traceClipboardClose.Call()
		h, _, _ := traceGlobalAlloc.Call(2, uintptr(len(str)*2))
		if h == 0 {
			return
		}
		p, _, _ := traceGlobalLock.Call(h)
		if p != 0 {
			copy(unsafe.Slice((*uint16)(unsafe.Pointer(p)), len(str)), str)
			traceGlobalUnlock.Call(h)
			traceClipboardEmpty.Call()
			if ok, _, _ := traceClipboardSet.Call(traceCFUnicodeText, h); ok != 0 {
				return
			}
		}
		traceGlobalFree.Call(h)
	}
}

func displayEditorRefreshFooter(w *displayEditor) {
	if w.visiting || w.loading {
		return
	}
	active := displayCaptureDraft(w)
	keys := map[string]bool{}
	activeKey := displayDraftKey(w)
	for k, d := range w.drafts {
		if k != activeKey {
			keys[d.key] = true
		}
	}
	if base, ok := w.draftBaselines[displayDraftKey(w)]; !ok || !displayDraftEquivalent(active, base) || displayEditorFollowChanged(w) {
		keys[w.key] = true
	}
	hasTone := false
	if e := displayEditorSurface(w); e != nil && e.s.Compare != nil && displayEditorCompareLinked(w) {
		group := e.s.Compare.Keys
		if active.color && active.key == group[0] {
			hasTone = displayEditorMappingDraftChanged(active, w.draftBaselines[activeKey])
		}
		for k, d := range w.drafts {
			if k != activeKey && d.color && d.key == group[0] && displayEditorMappingDraftChanged(d, w.draftBaselines[k]) {
				hasTone = true
			}
		}
		if hasTone {
			keys[group[0]], keys[group[1]] = true, true
		}
	}
	var names []string
	o := displayOwners[w.owner]
	if e := displayEditorSurface(w); o != nil && e != nil && e.s.Compare != nil {
		for i, k := range e.s.Compare.Keys {
			if keys[k] {
				names = append(names, [...]string{"A", "B", "差值"}[i])
				delete(keys, k)
			}
		}
	}
	for k := range keys {
		if o != nil && o.surfaces[k] != nil {
			names = append(names, o.surfaces[k].s.Title)
		} else {
			names = append(names, k)
		}
	}
	text := "已应用"
	if len(names) > 0 {
		text = "有未应用更改 · 待应用：" + strings.Join(names, "、")
		if hasTone {
			text += " 的色标映射与所选显示字段"
		}
	}
	if w.statsWaiting && len(names) > 0 {
		text = "正在计算 · " + text
	}
	failure := w.stateError
	if failure == "" {
		failure = w.nodeEditError
	}
	if failure == "" && len(names) > 0 {
		failure = displayEditorRefreshTypographyHints(w)
	}
	if failure == "" && len(names) > 0 {
		if w.pageColor && !displayEditorColorFollowsA(w, displayEditorSurface(w)) {
			state := displayEditorResolveRangeState(w)
			if state.Err != nil {
				failure = state.Err.Error()
			}
		} else if !w.pageColor && !displayEditorPoststackLinked(w) && !w.unit.StyleOnly {
			if _, _, err := displayEditorTypedBounds(w, &w.unit); err != nil {
				failure = err.Error()
			}
		}
	}
	if failure != "" {
		text = "错误：" + failure
	}
	displayEnable(w.controls[deApply], len(names) > 0 && !w.statsWaiting && failure == "" && !w.invalid)
	displayEnable(w.controls[deOK], (len(names) == 0 || !w.statsWaiting) && failure == "" && !w.invalid)
	if getText(w.status) != text {
		setText(w.status, text)
	}
}

func displayEditorMappingDraftChanged(d, b displayDraft) bool {
	if d.selects[deRangePolicy] != b.selects[deRangePolicy] || d.checks[deSym] != b.checks[deSym] || d.checks[deReverse] != b.checks[deReverse] ||
		d.text[dePercentLow] != b.text[dePercentLow] || d.text[dePercentHigh] != b.text[dePercentHigh] || d.bounds.forced || !slices.Equal(d.stops, b.stops) {
		return true
	}
	return d.text[deMin] != d.bounds.shown[0] || d.text[deMax] != d.bounds.shown[1]
}

func displayEditorChangeUnit(w *displayEditor) {
	old, next := w.unit, w.unit
	next.Unit = strings.TrimSpace(getText(w.controls[deUnit]))
	if next.Unit == old.Unit || next.Valid() != nil {
		return
	}
	r, edited, err := displayEditorTypedBounds(w, &old)
	if err != nil {
		return
	} // Keep invalid text in the user's input unit.
	w.unit = next
	displayEditorShowBounds(w, r, edited)
	if !displayChecked(w.controls[deTickAuto]) {
		if step, err := displayParseNumber(getText(w.controls[deStep]), false); err == nil && step > 0 {
			was := w.loading
			w.loading = true
			displayField(w, deStep, displaycore.FormatExactNumber(next.DisplayValue(old.BaseValue(step))))
			w.loading = was
		}
	}
}
