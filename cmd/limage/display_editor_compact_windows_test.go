//go:build windows

package main

import (
	"strings"
	"syscall"
	"testing"
	"unsafe"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
)

func displayEditorNativeVisible(h uintptr) bool {
	style, _, _ := algorithmGetWindowStyle.Call(h, ^uintptr(15))
	return h != 0 && style&WS_VISIBLE != 0
}

func displayRectsOverlap(a, b RECT) bool {
	return a.Left < b.Right && b.Left < a.Right && a.Top < b.Bottom && b.Top < a.Bottom
}

func TestDisplayEditorCompactRangeStrategiesAndRightCards(t *testing.T) {
	w, _ := displayNativeEditorPoststackProfiles(t, true)
	e := displayEditorSurface(w)
	e.s.equalTicks = true
	e.s.Title = strings.Repeat("很长的文件名_test_2026_", 25)
	setText(w.controls[deSummary], "数据范围：−501234.111 ～ 1234019.07；485920 个有效样点\r\n当前映射范围：−19430.6 ～ 43140.1")
	type expectation struct {
		policy displaycore.RangePolicy
		custom bool
	}
	policies := []expectation{{displaycore.FullRange, false}, {displaycore.PercentileRange, false}, {displaycore.PercentileRange, true}, {displaycore.ManualRange, false}}
	for _, dpi := range []int{96, 120, 144} {
		for _, size := range [][2]int{{860, 620}, {580, 570}, {700, 520}} {
			px := func(n float64) int { return displayPixelDIP(n, dpi) }
			pSetWindowPos.Call(w.handle, 0, 0, 0, uintptr(px(float64(size[0]))), uintptr(px(float64(size[1]))), 0x16)
			for _, section := range []int{displayPageRange, displayPageGradient, displayPageTicks, displayPageAppearance} {
				w.section = section
				for _, expect := range policies {
					displayEditorSetPolicy(w, expect.policy)
					preset := uintptr(2)
					if expect.custom {
						preset = 5
					}
					pSendMessageW.Call(w.controls[dePercentPreset], CB_SETCURSEL, preset, 0)
					displayLayoutEditorAtDPI(w, dpi)
					b := displayNativeRect(w.body)
					type placed struct {
						id   int
						rect RECT
					}
					var laidOut []placed
					for _, controls := range []map[int]uintptr{w.controls, w.labels} {
						for id, h := range controls {
							if id == deTopTabs || id >= deReset && id <= deApplyPanels || !displayEditorNativeVisible(h) {
								continue
							}
							r := displayNativeRect(h)
							if r.Left < b.Left || r.Top < b.Top || r.Right > b.Right || r.Bottom > b.Bottom {
								t.Fatalf("control %d clipped at %d DPI %v page %d policy %+v: %+v outside %+v", id, dpi, size, section, expect, r, b)
							}
							for _, p := range laidOut {
								if displayRectsOverlap(p.rect, r) {
									t.Fatalf("controls %d and %d overlap at %d DPI %v page %d policy %+v: %+v / %+v", p.id, id, dpi, size, section, expect, p.rect, r)
								}
							}
							laidOut = append(laidOut, placed{id, r})
						}
					}
					if !displayEditorNativeVisible(w.controls[deSummaryTitle]) || !displayEditorNativeVisible(w.controls[deSummary]) {
						t.Fatal("header lost data summary or title")
					}
					p := w.preview
					if p.Bottom <= p.Top || p.Right <= p.Left {
						t.Fatalf("empty preview at %d DPI %v page %d policy %+v: %+v", dpi, size, section, expect, p)
					}
					switch section {
					case displayPageRange:
						show := map[int]bool{
							deMin: true, deMax: true, dePercentPreset: expect.policy == displaycore.PercentileRange,
							dePercentLow: expect.custom, dePercentHigh: expect.custom,
							dePositionLow: expect.policy == displaycore.ManualRange, dePositionHigh: expect.policy == displaycore.ManualRange,
							deApplyPosition: expect.policy == displaycore.ManualRange, dePositionHeading: expect.policy == displaycore.ManualRange,
						}
						for id, want := range show {
							if got := displayEditorNativeVisible(w.controls[id]); got != want {
								t.Fatalf("mode %+v range control %d visibility = %v, want %v", expect, id, got, want)
							}
						}
						lo, hi := displayNativeRect(w.controls[deMin]), displayNativeRect(w.controls[deMax])
						if lo.Right-lo.Left != hi.Right-hi.Left {
							t.Fatalf("range endpoints have unequal edit widths %+v / %+v", lo, hi)
						}
						if p := w.controls[dePercentPreset]; displayEditorNativeVisible(p) && displayNativeRect(p).Top <= hi.Bottom {
							t.Fatal("percentile-specific controls need to follow the common range endpoints")
						}
						fallthrough
					case displayPageGradient:
						if p.Left != int32(px(12)) || p.Right != (b.Right-b.Left)-int32(px(12)) {
							t.Fatalf("stacked preview does not use full content width: %+v / %+v", p, b)
						}
						if w.graph.Bottom <= w.graph.Top || w.gradient.Bottom <= w.gradient.Top {
							t.Fatalf("distribution or gradient collapsed: dpi=%d size=%v section=%d policy=%+v graph=%+v gradient=%+v preview=%+v", dpi, size, section, expect, w.graph, w.gradient, p)
						}
						for _, field := range laidOut {
							if field.rect.Bottom > b.Top+p.Top {
								t.Fatalf("stacked control %d is not above preview", field.id)
							}
						}
					case displayPageTicks, displayPageAppearance:
						if width := int(p.Right - p.Left); width < px(220) || width > px(260) {
							t.Fatalf("style card width %d is not 220–260 DIP", width)
						}
						q := RECT{Left: b.Left + p.Left, Top: b.Top + p.Top, Right: b.Left + p.Right, Bottom: b.Top + p.Bottom}
						for _, field := range laidOut {
							if displayRectsOverlap(field.rect, q) {
								t.Fatalf("effect preview intersects control %d", field.id)
							}
						}
					}
				}
			}
		}
	}
}

func TestDisplayEditorLongTitleHoverAndWorkArea(t *testing.T) {
	w, e := displayNativeEditorFixture(t, true)
	e.s.Title = strings.Repeat("SEG-Y_完整标题", 18)
	displayLayoutEditor(w)
	titleH := w.controls[deSummaryTitle]
	if w.summaryTip.tooltip == 0 || getText(titleH) != e.s.Title {
		t.Fatal("native title is not registered for a hover tooltip")
	}
	var buf [512]uint16
	info := makePoststackToolInfo(w.body, titleH, &buf[0])
	pSendMessageW.Call(w.summaryTip.tooltip, WM_USER+56, uintptr(len(buf)), uintptr(unsafe.Pointer(&info))) // TTM_GETTEXTW
	if text := strings.ReplaceAll(syscall.UTF16ToString(buf[:]), "\r\n", ""); text != e.s.Title {
		t.Fatalf("full hover title was truncated: %q", text)
	}
	work := RECT{Left: -900, Top: 22, Right: 0, Bottom: 670}
	cropped := displayEditorFitWorkArea(RECT{Left: -1100, Right: 270, Top: -50, Bottom: 900}, work)
	if cropped != work {
		t.Fatalf("work-area cap did not handle cross-monitor negative coordinates: %+v", cropped)
	}
	for _, dpi := range []int{96, 120, 144} {
		displayEditorFont(w, dpi)
		m, release := displayEditorMeasureControls(w, dpi)
		clientWidth, clientHeight := m.minimumClient()
		release()
		outerWidth, outerHeight := displayEditorOuterSize(dpi, clientWidth, clientHeight)
		if outerWidth <= clientWidth || outerHeight <= clientHeight {
			t.Fatalf("minimum window omits non-client chrome at DPI %d: client %dx%d, frame %dx%d", dpi, clientWidth, clientHeight, outerWidth, outerHeight)
		}
	}
}

func TestDisplayEditorRangePreviewRetainsChartWithTallFont(t *testing.T) {
	// The Windows CI runner gave the manual-range pane this 150%-DPI card.
	// Reproduce the short client area independently of the installed UI font.
	card := RECT{Left: 18, Top: 466, Right: 800, Bottom: 580}
	g, bar, labels := displayEditorColorRangePreview(card, 144, 29)
	if g.Bottom <= labels.heading.Bottom || bar.Top <= g.Bottom || bar.Bottom <= bar.Top ||
		labels.bounds.Top <= bar.Bottom || labels.bounds.Bottom > card.Bottom ||
		labels.bounds.Bottom-labels.bounds.Top != 29 || labels.detail != (RECT{}) {
		t.Fatalf("chart, strip and full-size labels do not fit the compact card: graph=%+v strip=%+v labels=%+v", g, bar, labels)
	}
	card.Bottom += 180
	g, bar, labels = displayEditorColorRangePreview(card, 144, 29)
	if g.Bottom <= labels.heading.Bottom || labels.detail.Top <= labels.bounds.Bottom || labels.detail.Bottom > card.Bottom {
		t.Fatalf("normal card did not restore range help: graph=%+v strip=%+v labels=%+v", g, bar, labels)
	}
}

func TestDisplayEditorTwoNativeStripsMatchContent(t *testing.T) {
	w, _ := displayNativeEditorPoststackProfiles(t, false)
	e := displayEditorSurface(w)
	e.s.applyPanelStyle = func(displaycore.Settings, bool, int) error { return nil }
	for _, dpi := range []int{96, 120, 144, 192} {
		for _, width := range []int{600, 860} {
			px := func(n int) uintptr { return uintptr(displayPixelDIP(float64(n), dpi)) }
			pSetWindowPos.Call(w.handle, 0, 0, 0, px(width), px(660), 0x16)
			for _, section := range []int{displayPageRange, displayPageTicks, displayPageAppearance} {
				w.section = section
				displayLayoutEditorAtDPI(w, dpi)
				top, sub := displayNativeRect(w.controls[deTopTabs]), displayNativeRect(w.controls[deSubTabs])
				copyButton := displayNativeRect(w.controls[deApplyPanels])
				if top.Left != sub.Left || top.Right != sub.Right || copyButton.Right != top.Right || copyButton.Bottom >= top.Bottom || top.Bottom >= sub.Top {
					t.Fatalf("tabs and copy button misaligned: dpi=%d width=%d top=%+v sub=%+v copy=%+v", dpi, width, top, sub, copyButton)
				}
			}
		}
	}
}

func TestDisplayEditorCoordinateRangePercentTracksExactDraft(t *testing.T) {
	w, _ := displayNativeEditorFixture(t, false)
	if got := getText(w.controls[dePositionLow]); got != "20" {
		t.Fatalf("fresh cropped view showed incorrect start percent: %q", got)
	}
	displayEditorSetRangeDraft(w, displaycore.Range{Min: 250, Max: 750}, displaycore.DragWhole)
	if lo, hi := getText(w.controls[dePositionLow]), getText(w.controls[dePositionHigh]); lo != "25" || hi != "75" {
		t.Fatalf("new range did not update both percentages: %q..%q", lo, hi)
	}
	setText(w.controls[deMin], "333.123456789")
	displayEditorCommand(w, deMin, 0x0300)
	if _, span, ok := displayEditorRange(w); !ok || span.Min != 333.123456789 || getText(w.controls[dePositionLow]) != "33.3123" {
		t.Fatalf("manual range or percentage is stale: %+v, %q", span, getText(w.controls[dePositionLow]))
	}
}
