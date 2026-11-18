//go:build windows

package main

import (
	"fmt"
	"slices"
	"sort"
	"unsafe"
)

type algorithmTab uint8

const (
	algorithmTabCommon algorithmTab = iota + 1
	algorithmTabAdvanced
	algorithmTabSettings
	algorithmTabResults
)

const (
	algorithmPageRow    = 62
	algorithmCompactRow = 36
	algorithmTabHeight  = 29
	algorithmPageNav    = 34
	algorithmTabSetCur  = TCM_FIRST + 12
)

type algorithmTabsLayout struct {
	tabs                         []algorithmTab
	active                       algorithmTab
	tab, commonTitle, panel, nav algorithmBox
	common, parameters           []algorithmParameterPlacement
	rows                         []int
	page, pageCount, columns     int
	compactNav                   bool
}

func algorithmTabTitle(tab algorithmTab) string {
	switch tab {
	case algorithmTabCommon:
		return "常用参数"
	case algorithmTabAdvanced:
		return "高级参数"
	case algorithmTabSettings:
		return "运行设置"
	case algorithmTabResults:
		return "处理结果"
	}
	return ""
}

func algorithmParameterWidths(width int, kind string, browse, rangeEnd bool) (label, value int) {
	label = minInt(235, maxInt(110, width/2))
	if rangeEnd {
		label = minInt(label, maxInt(110, width-2*100-2*algorithmGap))
	}
	value = maxInt(1, width-label-algorithmGap)
	if browse {
		value = maxInt(1, value-37)
	}
	if rangeEnd {
		value = minInt(132, maxInt(1, (value-algorithmGap)/2))
	} else if kind == "integer" || kind == "number" {
		value = minInt(138, value)
	}
	return
}

// A pinned file field takes a whole line and does not split the numeric grid.
func algorithmCommonGrid(indices []int, params []algorithmParameterControl, columns int) ([][]int, bool) {
	var rows [][]int
	partial, wide := false, false
	for _, i := range indices {
		if params[i].Wide {
			rows = append(rows, []int{i})
			partial, wide = false, true
			continue
		}
		if !partial {
			rows = append(rows, nil)
		}
		last := len(rows) - 1
		rows[last] = append(rows[last], i)
		partial = len(rows[last]) < columns
	}
	return rows, wide
}

func placeAlgorithmCommonGrid(lines [][]int, params []algorithmParameterControl, columns, x, y, inner, rowHeight int) (out []algorithmParameterPlacement) {
	cell := (inner - (columns-1)*algorithmGap) / columns
	for i, row := range lines {
		for j, index := range row {
			w := cell
			if params[index].Wide {
				w = inner
			}
			out = append(out, algorithmParameterPlacement{index, algorithmBox{x + j*(cell+algorithmGap), y + i*rowHeight, w, rowHeight}})
		}
	}
	return out
}

// Layout positions whole rows only. Very short work areas use the same
// pagination for common fields as for advanced fields, with no scroll state.
func layoutAlgorithmTabs(w, h int, params []algorithmParameterControl, selected algorithmTab, page int) algorithmTabsLayout {
	var common, advanced []int
	for i, p := range params {
		if p.Omit {
			continue
		}
		if p.Common && p.Descriptor.Type != "group" {
			common = append(common, i)
		} else {
			advanced = append(advanced, i)
		}
	}
	sort.SliceStable(common, func(i, j int) bool { return params[common[i]].CommonOrder < params[common[j]].CommonOrder })
	x, y := minInt(algorithmGutter, maxInt(4, w/12)), 3
	inner := maxInt(1, w-2*x)
	l := algorithmTabsLayout{columns: 2}
	if w < 575 {
		l.columns = 1
	}
	paramHeight := func(i int) int {
		if params[i].Compact {
			return algorithmCompactRow
		}
		return algorithmPageRow
	}
	commonRow := algorithmCompactRow
	for _, i := range common {
		commonRow = maxInt(commonRow, paramHeight(i))
	}
	commonLines, hasWide := algorithmCommonGrid(common, params, l.columns)
	commonHeight := 22 + len(commonLines)*commonRow
	if len(common) > 0 && h >= commonHeight+algorithmTabHeight+algorithmPageRow+algorithmPageNav+15 {
		l.commonTitle = algorithmBox{x, y, inner, 20}
		y += 22
		l.common = placeAlgorithmCommonGrid(commonLines, params, l.columns, x, y, inner, commonRow)
		y += commonHeight - 22 + 3
	} else if len(common) > 0 {
		l.tabs = append(l.tabs, algorithmTabCommon)
	}
	if len(advanced) > 0 {
		l.tabs = append(l.tabs, algorithmTabAdvanced)
	}
	l.tabs = append(l.tabs, algorithmTabSettings, algorithmTabResults)
	if !slices.Contains(l.tabs, selected) {
		selected = l.tabs[0]
	}
	l.active = selected
	l.tab = algorithmBox{x, y, inner, algorithmTabHeight}
	y += algorithmTabHeight + 4
	l.panel = algorithmBox{x, y, inner, maxInt(0, h-y-2)}
	// Keep tab bounds stable when switching sections on a very short screen.
	l.compactNav = l.panel.h < algorithmPageRow+algorithmPageNav
	if l.compactNav {
		l.tab.w = maxInt(1, l.tab.w-117)
	}
	if selected == algorithmTabCommon && hasWide {
		// On short windows use the same two-column grid inside the Common
		// tab. A wide horizon and adjacent key controls remain on page one.
		capacity := l.panel.h
		if len(commonLines)*commonRow > capacity && len(commonLines) > 1 {
			if l.compactNav {
				l.nav = algorithmBox{x + inner - 112, l.tab.y, 112, 30}
			} else {
				capacity = maxInt(0, capacity-algorithmPageNav)
				l.nav = algorithmBox{x, y + capacity, inner, minInt(algorithmPageNav, l.panel.h)}
				l.panel.h = capacity
			}
		}
		perPage := maxInt(1, capacity/commonRow)
		l.pageCount = maxInt(1, (len(commonLines)+perPage-1)/perPage)
		l.page = maxInt(0, minInt(page, l.pageCount-1))
		if capacity >= commonRow {
			start := l.page * perPage
			lines := commonLines[start:minInt(len(commonLines), start+perPage)]
			l.parameters = placeAlgorithmCommonGrid(lines, params, l.columns, x, y, inner, commonRow)
			for _, p := range l.parameters {
				l.rows = append(l.rows, p.index)
			}
		}
		return l
	}
	var source []int
	rowHeight := func(_ int) int { return algorithmPageRow }
	switch selected {
	case algorithmTabCommon:
		source = common
		rowHeight = paramHeight
	case algorithmTabAdvanced:
		source = advanced
		rowHeight = func(i int) int {
			if params[i].Descriptor.Type == "group" {
				return 24
			}
			return algorithmPageRow
		}
	case algorithmTabSettings:
		source = []int{0}
		rowHeight = func(_ int) int { return 36 }
	case algorithmTabResults:
		source = []int{0, 1, 2}
		rowHeight = func(_ int) int { return 32 }
	}
	total := 0
	for _, i := range source {
		total += rowHeight(i)
	}
	capacity := l.panel.h
	if total > capacity && len(source) > 1 {
		if l.compactNav {
			// Reuse the tab row for short screens: the next/previous
			// controls no longer consume the space needed by a whole field.
			l.nav = algorithmBox{x + inner - 112, l.tab.y, 112, 30}
		} else {
			capacity = maxInt(0, capacity-algorithmPageNav)
			l.nav = algorithmBox{x, y + capacity, inner, minInt(algorithmPageNav, l.panel.h)}
			l.panel.h = capacity
		}
	}
	var pages [][]int
	var used int
	group := func(i int) string {
		p := params[i]
		if len(p.Path) >= 2 || len(p.Path) == 1 && p.Descriptor.Type == "group" {
			return p.Path[0]
		}
		return ""
	}
	for at, i := range source {
		d := rowHeight(i)
		keepGroup := false
		if selected == algorithmTabAdvanced && used > 0 && group(i) != "" && (at == 0 || group(source[at-1]) != group(i)) {
			groupHeight := d
			for _, following := range source[at+1:] {
				if group(following) != group(i) {
					break
				}
				groupHeight += rowHeight(following)
			}
			keepGroup = groupHeight <= capacity && used+groupHeight > capacity
		}
		if len(pages) == 0 || used > 0 && (used+d > capacity || keepGroup) {
			pages = append(pages, nil)
			used = 0
		}
		pages[len(pages)-1] = append(pages[len(pages)-1], i)
		used += d
	}
	l.pageCount = maxInt(1, len(pages))
	l.page = maxInt(0, minInt(page, l.pageCount-1))
	if len(pages) > 0 {
		for _, index := range pages[l.page] {
			height := rowHeight(index)
			if y+height > l.panel.y+l.panel.h {
				break
			}
			l.rows = append(l.rows, index)
			if selected == algorithmTabCommon || selected == algorithmTabAdvanced {
				indent := minInt(44, maxInt(0, len(params[index].Path)-1)*11)
				l.parameters = append(l.parameters, algorithmParameterPlacement{index, algorithmBox{x + indent, y, inner - indent, height}})
			}
			y += height
		}
	}
	return l
}

func selectAlgorithmTab(tab algorithmTab) {
	cz.tab = tab
	layoutCadzowWindow("切换页签")
}

func updateAlgorithmTabs(l algorithmTabsLayout) {
	if cz.tabs == 0 {
		return
	}
	if !slices.Equal(cz.visibleTabs, l.tabs) {
		pSendMessageW.Call(cz.tabs, TCM_FIRST+9, 0, 0) // TCM_DELETEALLITEMS
		for i, key := range l.tabs {
			item := TCITEM{Mask: TCIF_TEXT, PszText: u16(algorithmTabTitle(key))}
			pSendMessageW.Call(cz.tabs, TCM_INSERTITEMW, uintptr(i), uintptr(unsafe.Pointer(&item)))
		}
		cz.visibleTabs = append(cz.visibleTabs[:0], l.tabs...)
	}
	pSendMessageW.Call(cz.tabs, algorithmTabSetCur, uintptr(slices.Index(l.tabs, l.active)), 0)
	cz.tabPages[l.active] = l.page
	algorithmMoveBox(cz.tabs, l.tab)
	visible := l.pageCount > 1 && l.nav.h >= 28
	x := l.nav.x + l.nav.w
	next, prev, count := algorithmBox{x - 74, l.nav.y + 3, 74, 27}, algorithmBox{x - 230, l.nav.y + 3, 74, 27}, algorithmBox{x - 149, l.nav.y + 8, 70, 22}
	prevText, nextText := "上一页", "下一页"
	page := fmt.Sprintf("%d / %d 页", l.page+1, l.pageCount)
	if l.compactNav {
		next, prev, count = algorithmBox{x - 38, l.nav.y + 1, 38, 27}, algorithmBox{x - 112, l.nav.y + 1, 38, 27}, algorithmBox{x - 74, l.nav.y + 5, 36, 22}
		prevText, nextText = "上页", "下页"
		page = fmt.Sprintf("%d/%d", l.page+1, l.pageCount)
	}
	algorithmShowBox(cz.pageNext, next, visible)
	algorithmShowBox(cz.pagePrev, prev, visible)
	algorithmShowBox(cz.pageCount, count, visible)
	if visible {
		setText(cz.pagePrev, prevText)
		setText(cz.pageNext, nextText)
		setText(cz.pageCount, page)
		pEnableWindow.Call(cz.pagePrev, uintptr(boolToInt(l.page > 0)))
		pEnableWindow.Call(cz.pageNext, uintptr(boolToInt(l.page+1 < l.pageCount)))
	}
}
